package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/memory"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

// RememberTool 保存用户明确要求记住的记忆或全局规则。
type RememberTool struct {
	store  *memory.Store
	rules  *memory.Rules
	source string
}

type rememberArguments struct {
	Target      string `json:"target"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Confidence  string `json:"confidence"`
	Evidence    string `json:"evidence"`
}

func NewRememberTool(store *memory.Store, rules *memory.Rules, source string) (*RememberTool, error) {
	if store == nil {
		return nil, errorsForRegistry("记忆存储不能为空")
	}
	if rules == nil {
		return nil, errorsForRegistry("用户规则存储不能为空")
	}
	return &RememberTool{store: store, rules: rules, source: source}, nil
}

func (r *RememberTool) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name:        "remember",
		Description: "保存用户明确要求记住的内容或长期规则。只在用户明确要求、明确表达全局规则，或确认提升时调用；不要用于模型自行推断的偏好，那类内容由轮末审查处理。",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"target":{"type":"string","enum":["user_memory","project_memory","user_agents"],"description":"写入目标"},"type":{"type":"string","enum":["preference","feedback","project","reference","environment"],"description":"记忆类型"},"name":{"type":"string","description":"kebab-case 唯一名称"},"description":{"type":"string","description":"一行描述；user_agents 时作为规则正文"},"body":{"type":"string","description":"记忆正文；user_agents 可省略"},"confidence":{"type":"string","enum":["high","medium","low"],"description":"置信度"},"evidence":{"type":"string","description":"用户原话或判断依据；user_agents 必填"}},"required":["target","name","description"]}`),
	}
}

func (r *RememberTool) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var args rememberArguments
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, invalidArguments("remember 参数格式无效", err)
	}
	target := strings.TrimSpace(args.Target)
	name := strings.TrimSpace(args.Name)
	description := strings.TrimSpace(args.Description)
	confidence := memory.Confidence(strings.TrimSpace(args.Confidence))
	if confidence == "" {
		confidence = memory.ConfidenceMedium
	}
	if name == "" {
		return Result{}, invalidArguments("remember 缺少 name 参数", nil)
	}
	if description == "" {
		return Result{}, invalidArguments("remember 缺少 description 参数", nil)
	}

	switch target {
	case "user_memory", "project_memory":
		scope := memory.ScopeUser
		if target == "project_memory" {
			scope = memory.ScopeProject
		}
		itemType := memory.Type(strings.TrimSpace(args.Type))
		if itemType == "" {
			itemType = memory.TypeFeedback
		}
		item := memory.Item{
			Name:        name,
			Description: description,
			Type:        itemType,
			Scope:       scope,
			Confidence:  confidence,
			Body:        strings.TrimSpace(args.Body),
			Source:      r.source,
		}
		result, err := r.store.Write(ctx, item)
		if err != nil {
			return Result{}, executionError("写入记忆失败", err)
		}
		return Result{Content: formatRememberResult(scope, result)}, nil
	case "user_agents":
		if confidence != memory.ConfidenceHigh {
			return Result{}, invalidArguments("写入用户 AGENTS 需要 confidence=high", nil)
		}
		if strings.TrimSpace(args.Evidence) == "" {
			return Result{}, invalidArguments("写入用户 AGENTS 需要 evidence 提供用户原话", nil)
		}
		path, err := r.rules.AddWithSource(ctx, name, description, r.source)
		if err != nil {
			return Result{}, executionError("写入用户 AGENTS 失败", err)
		}
		return Result{Content: fmt.Sprintf("已记录用户规则：%s\n路径：%s\n说明：当前会话继续使用旧规则，新规则在下一次新会话或压缩后进入固定前缀。", name, path)}, nil
	default:
		return Result{}, invalidArguments("remember 的 target 必须是 user_memory、project_memory 或 user_agents", nil)
	}
}

func formatRememberResult(scope memory.Scope, result memory.WriteResult) string {
	label := "用户记忆"
	if scope == memory.ScopeProject {
		label = "项目记忆"
	}
	return fmt.Sprintf("已记录%s：%s（%s）\n路径：%s\n说明：当前会话的固定索引保持不变，新记忆可通过 memory search 查找，并在下一次新会话或压缩后进入固定索引。", label, result.Item.Name, result.Action, result.Path)
}
