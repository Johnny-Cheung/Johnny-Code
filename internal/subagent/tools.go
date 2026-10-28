package subagent

import (
	"context"
	"errors"
	"fmt"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

// Mode 是父会话的权限模式，决定写入类子任务沿用的策略。
type Mode string

const (
	ModeReadOnly Mode = "read-only"
	ModeDefault  Mode = "default"
	ModeAllow    Mode = "allow"
)

// Toolbox 为子任务构造工具集合。Registry 是父会话的完整注册表（含 remember、task）。
type Toolbox struct {
	Runtime  tool.Runtime
	Registry *tool.Registry
	Mode     Mode
	// Rules 和 Gate 仅 default 模式使用，写入类子任务与父会话共享项目规则和审批队列。
	Rules *tool.ProjectRules
	Gate  *tool.ApprovalGate
	// Claims 由 Manager 填写。
	Claims *tool.FileClaims
	// Definitions 返回父会话看到的工具定义，fork 子会话沿用它以保持请求前缀一致。
	Definitions func() []model.ToolDefinition
}

var freshReadTools = []string{"bash", "read", "skill", "memory"}
var freshWriteTools = []string{"bash", "read", "write", "edit", "skill", "memory"}

var forkDenied = map[string]string{
	"task":     "子任务不能再派出子任务，请直接完成任务并在最终回复中汇报。",
	"workflow": "子任务不能使用工作流，请直接完成任务并在最终回复中汇报。",
	"remember": "子任务不能写入长期记忆，值得记住的内容请写进最终报告，由主 Agent 决定。",
}

// build 返回子任务的工具集合：
//   - read：不论父权限模式，一律使用只读包装；
//   - write：沿用父权限策略，并受 owner 声明的文件范围约束；
//   - fork：沿用父会话的工具定义，但执行时拒绝 task 和 remember。
func (b Toolbox) build(kind tool.SpawnKind, access tool.SpawnAccess, owner string) (agent.Tools, error) {
	if b.Registry == nil {
		return nil, errors.New("子任务工具箱缺少工具注册表")
	}
	registry := b.Registry
	if kind == tool.SpawnFresh {
		names := freshReadTools
		if access == tool.SpawnWrite {
			names = freshWriteTools
		}
		var list []tool.Tool
		for _, name := range names {
			if item, ok := b.Registry.Get(name); ok {
				list = append(list, item)
			}
		}
		var err error
		if registry, err = tool.NewRegistry(list...); err != nil {
			return nil, err
		}
	}
	var tools agent.Tools
	if access == tool.SpawnRead {
		readOnly, err := tool.NewReadOnly(registry, b.Runtime)
		if err != nil {
			return nil, err
		}
		tools = readOnly
	} else {
		switch b.Mode {
		case ModeDefault:
			permissioned, err := tool.NewDefault(registry, b.Runtime, b.Rules, b.Gate)
			if err != nil {
				return nil, err
			}
			tools = permissioned
		case ModeAllow:
			tools = registry
		default:
			return nil, fmt.Errorf("权限模式 %q 不允许写入类子任务", b.Mode)
		}
		guard, err := tool.NewScopeGuard(tools, b.Claims, owner, b.Runtime.WorkDir)
		if err != nil {
			return nil, err
		}
		tools = guard
	}
	if kind == tool.SpawnFork {
		tools = &restricted{inner: tools, definitions: b.Definitions, denied: forkDenied}
	}
	return tools, nil
}

// restricted 拒绝指定工具，并可替换工具定义。
type restricted struct {
	inner       agent.Tools
	definitions func() []model.ToolDefinition
	denied      map[string]string
}

func (r *restricted) Definitions() []model.ToolDefinition {
	if r.definitions != nil {
		return r.definitions()
	}
	return r.inner.Definitions()
}

func (r *restricted) Execute(ctx context.Context, call model.ToolCall) (tool.Result, error) {
	if message, ok := r.denied[call.Name]; ok {
		return tool.Result{Content: message, IsError: true}, &tool.Error{Kind: tool.KindPermissionDenied, Message: message}
	}
	return r.inner.Execute(ctx, call)
}
