package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

// ClassificationDecision 是 JEV 对 bash 命令的处理建议。
type ClassificationDecision string

const (
	ClassificationAllow ClassificationDecision = "allow"
	ClassificationAsk   ClassificationDecision = "ask"
	ClassificationDeny  ClassificationDecision = "deny"
)

// Classification 是分类器返回的最小结果。工具层不依赖具体的 JEV 客户端，
// 测试和后续接入其他模型都可以实现 BashClassifier。
type Classification struct {
	Decision      ClassificationDecision
	Probabilities map[string]float64
	Reason        string
	Model         string
}

// BashClassifier 判断一条原始 bash 命令应该放行、审批还是拒绝。
type BashClassifier interface {
	Classify(ctx context.Context, command, workDir string) (Classification, error)
}

const jevFailureLimit = 3

var errJEVDisabled = errors.New("JEV 连续失败，已在本会话停用")

// JEV 包装 Default，只接管 bash。JEV 模式下没有固定查询白名单、项目命令
// 前缀或危险后缀名单：原始命令直接交给分类器，allow 用完整 shell 执行，
// ask 进入审批，deny 直接拒绝。分类器不可用时，整条命令回退到 Default。
type JEV struct {
	base       *Default
	classifier BashClassifier
	onFallback func(error)

	mu       sync.Mutex
	failures int
	disabled bool
}

func NewJEV(base *Default, classifier BashClassifier, onFallback func(error)) (*JEV, error) {
	if base == nil {
		return nil, errors.New("JEV 模式需要默认工具集合")
	}
	if classifier == nil {
		return nil, errors.New("JEV 模式需要 bash 分类器")
	}
	return &JEV{base: base, classifier: classifier, onFallback: onFallback}, nil
}

func (j *JEV) Definitions() []model.ToolDefinition {
	definitions := append([]model.ToolDefinition(nil), j.base.Definitions()...)
	for index := range definitions {
		if definitions[index].Name == "bash" {
			definitions[index].Description = "在工作目录中执行一条 shell 命令。JEV 模式会把原始命令交给 JEV 判断：allow 直接执行，ask 需要用户审批，deny 拒绝；JEV 不可用时回退默认权限策略。"
		}
	}
	return definitions
}

func (j *JEV) Execute(ctx context.Context, call model.ToolCall) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if call.Name != "bash" {
		return j.base.Execute(ctx, call)
	}
	raw := call.Arguments
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var args bashArguments
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, invalidArguments("bash 参数格式无效", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return Result{}, invalidArguments("bash 的 command 不能为空", nil)
	}
	// 先校验 timeout，避免为一个本来就会失败的参数调用 JEV。
	if _, err := commandTimeout(j.base.runtime, args.TimeoutSeconds); err != nil {
		return Result{}, err
	}

	classification, err := j.classify(ctx, args.Command)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, ctxErr
		}
		if j.onFallback != nil {
			j.onFallback(err)
		}
		return j.base.Execute(ctx, call)
	}
	if classification.Decision == "" {
		err := errors.New("JEV 返回空决策")
		if j.onFallback != nil {
			j.onFallback(err)
		}
		return j.base.Execute(ctx, call)
	}

	request := ApprovalRequest{
		ToolCallID:              call.ID,
		Tool:                    "bash",
		WorkDir:                 j.base.runtime.WorkDir,
		Target:                  args.Command,
		Command:                 args.Command,
		Reason:                  "JEV 判断：" + string(classification.Decision),
		Detail:                  classificationDetail(classification),
		Classifier:              "jev",
		ClassifiedModel:         classification.Model,
		ClassifiedDecision:      string(classification.Decision),
		ClassifiedReason:        classification.Reason,
		ClassifiedProbabilities: cloneProbabilities(classification.Probabilities),
	}
	request.Origin = OriginFrom(ctx)
	applySandboxInfo(j.base.runtime, &request)

	switch classification.Decision {
	case ClassificationAllow:
		j.audit(request, ApprovalDecision{
			Allow:  true,
			Source: "jev",
			Reason: classificationReason("JEV 允许执行", classification),
		})
		return j.base.shell.Execute(ctx, raw)
	case ClassificationAsk:
		decision, err := j.base.gate.Ask(ctx, request)
		if err != nil {
			j.audit(request, ApprovalDecision{Source: "error", Reason: err.Error()})
			return Result{}, err
		}
		j.audit(request, decision)
		if !decision.Allow {
			return j.base.deny(approvalDeniedMessage(request, decision))
		}
		return j.base.shell.Execute(ctx, raw)
	case ClassificationDeny:
		reason := classificationReason("JEV 拒绝执行", classification)
		j.audit(request, ApprovalDecision{Allow: false, Source: "jev", Reason: reason})
		return j.base.deny(reason + "；如需强制执行，请退出后用 --permission-mode default 或 --skip 重新运行。")
	default:
		err := fmt.Errorf("JEV 返回未知决策 %q", classification.Decision)
		if j.onFallback != nil {
			j.onFallback(err)
		}
		return j.base.Execute(ctx, call)
	}
}

func (j *JEV) classify(ctx context.Context, command string) (Classification, error) {
	j.mu.Lock()
	disabled := j.disabled
	j.mu.Unlock()
	if disabled {
		return Classification{}, errJEVDisabled
	}
	classification, err := j.classifier.Classify(ctx, command, j.base.runtime.WorkDir)
	if err != nil && ctx != nil && ctx.Err() != nil {
		// 用户取消或本轮取消不是 JEV 故障，不参与连续失败熔断。
		return Classification{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err != nil {
		j.failures++
		if j.failures >= jevFailureLimit {
			j.disabled = true
		}
		return Classification{}, err
	}
	j.failures = 0
	return classification, nil
}

func (j *JEV) audit(request ApprovalRequest, decision ApprovalDecision) {
	if j.base.gate == nil {
		return
	}
	j.base.gate.Audit(request, decision)
}

func classificationDetail(classification Classification) string {
	parts := []string{"决策：" + string(classification.Decision)}
	if classification.Reason != "" {
		parts = append(parts, "原因："+classification.Reason)
	}
	if classification.Model != "" {
		parts = append(parts, "模型："+classification.Model)
	}
	if len(classification.Probabilities) > 0 {
		parts = append(parts, "概率："+formatProbabilities(classification.Probabilities))
	}
	return strings.Join(parts, "；")
}

func classificationReason(prefix string, classification Classification) string {
	if classification.Reason == "" {
		return prefix
	}
	return prefix + "（" + classification.Reason + "）"
}

func formatProbabilities(probabilities map[string]float64) string {
	keys := make([]string, 0, len(probabilities))
	for key := range probabilities {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%.3f", key, probabilities[key]))
	}
	return strings.Join(parts, ", ")
}

func cloneProbabilities(probabilities map[string]float64) map[string]float64 {
	if len(probabilities) == 0 {
		return nil
	}
	cloned := make(map[string]float64, len(probabilities))
	for key, value := range probabilities {
		cloned[key] = value
	}
	return cloned
}
