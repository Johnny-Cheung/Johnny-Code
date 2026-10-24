package tool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

var ErrApprovalUnavailable = errors.New("当前入口不支持交互审批")

// ApprovalRequest 是工具层交给审批界面的有界数据。Target/Command/Detail
// 只用于展示，授权判断已经由调用方完成。
type ApprovalRequest struct {
	ToolCallID     string
	Tool           string
	WorkDir        string
	Target         string
	Command        string
	Reason         string
	Detail         string
	RecordPrefixes [][]string
	RecordPaths    []PathRule
}

func (r ApprovalRequest) CanRecord() bool {
	return len(r.RecordPrefixes) > 0 || len(r.RecordPaths) > 0
}

func (r ApprovalRequest) RecordLabel() string {
	var parts []string
	for _, prefix := range r.RecordPrefixes {
		if len(prefix) > 0 {
			parts = append(parts, "命令前缀 "+strings.Join(prefix, " "))
		}
	}
	for _, rule := range r.RecordPaths {
		kind := "文件"
		if rule.Kind == PathKindDir {
			kind = "目录"
		}
		parts = append(parts, kind+" "+rule.Path)
	}
	return strings.Join(parts, "；")
}

type ApprovalDecision struct {
	Allow  bool
	Record bool
	Source string
	Reason string
}

// ApprovalAudit 只记录审批决策，不包含工具完整输出。Prefix/Paths 是实际
// 记录成功的目标，用于排查“为什么后续自动放行”。
type ApprovalAudit struct {
	ToolCallID string
	Tool       string
	Target     string
	Decision   string
	Source     string
	Reason     string
	Prefixes   [][]string
	Paths      []PathRule
}

type ApprovalFunc func(context.Context, ApprovalRequest) (ApprovalDecision, error)
type AuditFunc func(ApprovalAudit)

// ApprovalGate 连接工具执行线程和 TUI。Set 在启动任务前调用；Ask 在工具
// 执行线程阻塞等待，ctx 取消时必须返回。
type ApprovalGate struct {
	mu    sync.RWMutex
	ask   ApprovalFunc
	audit AuditFunc
}

func NewApprovalGate() *ApprovalGate {
	return &ApprovalGate{}
}

func (g *ApprovalGate) Set(fn ApprovalFunc) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ask = fn
}

func (g *ApprovalGate) SetAudit(fn AuditFunc) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.audit = fn
}

func (g *ApprovalGate) Ask(ctx context.Context, request ApprovalRequest) (ApprovalDecision, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ApprovalDecision{}, err
	}
	if g == nil {
		return ApprovalDecision{Source: "unavailable", Reason: ErrApprovalUnavailable.Error()}, nil
	}
	g.mu.RLock()
	fn := g.ask
	g.mu.RUnlock()
	if fn == nil {
		return ApprovalDecision{Source: "unavailable", Reason: ErrApprovalUnavailable.Error()}, nil
	}
	decision, err := fn(ctx, request)
	if err != nil {
		return ApprovalDecision{}, err
	}
	if !decision.Allow {
		decision.Record = false
	}
	if decision.Record && !request.CanRecord() {
		decision.Record = false
	}
	if decision.Source == "" {
		decision.Source = "approval"
	}
	if decision.Reason == "" {
		if decision.Allow {
			decision.Reason = "用户允许"
		} else {
			decision.Reason = "用户拒绝"
		}
	}
	return decision, nil
}

func (g *ApprovalGate) Audit(request ApprovalRequest, decision ApprovalDecision) {
	if g == nil {
		return
	}
	g.mu.RLock()
	audit := g.audit
	g.mu.RUnlock()
	auditDecision(audit, request, decision)
}

func auditDecision(audit AuditFunc, request ApprovalRequest, decision ApprovalDecision) {
	if audit == nil {
		return
	}
	result := "deny"
	if decision.Allow {
		result = "allow"
		if decision.Record {
			result = "record"
		}
	}
	var prefixes [][]string
	var paths []PathRule
	if decision.Record {
		prefixes = clonePrefixes(request.RecordPrefixes)
		paths = append([]PathRule(nil), request.RecordPaths...)
	}
	audit(ApprovalAudit{
		ToolCallID: request.ToolCallID,
		Tool:       request.Tool,
		Target:     approvalTarget(request),
		Decision:   result,
		Source:     decision.Source,
		Reason:     decision.Reason,
		Prefixes:   prefixes,
		Paths:      paths,
	})
}

func approvalTarget(request ApprovalRequest) string {
	if request.Target != "" {
		return request.Target
	}
	if request.Command != "" {
		return request.Command
	}
	return request.RecordLabel()
}

func approvalDeniedMessage(request ApprovalRequest, decision ApprovalDecision) string {
	reason := strings.TrimSpace(decision.Reason)
	if reason == "" {
		reason = "用户拒绝"
	}
	return fmt.Sprintf("权限审批未通过：%s（%s）", reason, approvalTarget(request))
}
