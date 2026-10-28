package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

// ReadOnly 包装默认工具注册表，实现策略性只读模式。它不是操作系统沙箱：
// read 被限制在工作目录内，write/edit 被拒绝，bash 只执行受限查询白名单。
type ReadOnly struct {
	base  *Registry
	scope workspaceScope
	bash  *restrictedBash
}

func NewReadOnly(base *Registry, runtime Runtime) (*ReadOnly, error) {
	if base == nil {
		return nil, errors.New("只读工具注册表不能为空")
	}
	runtime = normalizeRuntime(runtime)
	scope, err := newWorkspaceScope(runtime.WorkDir)
	if err != nil {
		return nil, err
	}
	return &ReadOnly{
		base:  base,
		scope: scope,
		bash:  &restrictedBash{runtime: runtime, scope: scope},
	}, nil
}

// Definitions 保留四个工具的名称、顺序和 schema，只补充模式说明，避免模型
// 看到与当前权限不一致的工具描述。
func (r *ReadOnly) Definitions() []model.ToolDefinition {
	definitions := r.base.Definitions()
	for i := range definitions {
		switch definitions[i].Name {
		case "read":
			definitions[i].Description += " 只读模式仅允许读取工作目录内文件。"
		case "bash":
			definitions[i].Description += " 只读模式只支持用户级只读查询白名单，不经过 shell；允许固定查询命令组成的两段只读管道。"
		case "skill":
			definitions[i].Description += " 只读模式可以加载已发现的 Skill。"
		case "memory":
			definitions[i].Description += " 只读模式可以查询长期记忆。"
		case "remember":
			definitions[i].Description += " 只读模式下此工具会被拒绝。"
		case "task":
			definitions[i].Description += " 只读模式只能派出 access=read 的子任务。"
		case "workflow":
			definitions[i].Description += " 只读模式只能启动 access=read 的任务。"
		case "write", "edit":
			definitions[i].Description += " 只读模式下此工具会被拒绝。"
		}
	}
	return definitions
}

func (r *ReadOnly) Execute(ctx context.Context, call model.ToolCall) (Result, error) {
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	if r == nil || r.base == nil {
		return Result{}, invalidArguments("只读工具注册表不能为空", nil)
	}
	if _, ok := r.base.Get(call.Name); !ok {
		return r.base.Execute(ctx, call)
	}
	arguments := call.Arguments
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if !jsonObject(arguments) {
		return Result{}, invalidArguments("工具参数必须是 JSON 对象", nil)
	}
	switch call.Name {
	case "read":
		return r.read(ctx, arguments)
	case "write", "edit":
		return r.deny(fmt.Sprintf("只读模式不允许使用 %s 修改文件；如需写入请切换到放行模式", call.Name))
	case "bash":
		return r.executeBash(ctx, arguments)
	case "skill":
		// Skill 只读取已加载的本地指导，不修改文件，也不接受路径参数。
		return r.base.Execute(ctx, call)
	case "memory":
		// 记忆查询只读，不修改文件。
		return r.base.Execute(ctx, call)
	case "task", "workflow":
		// 子任务由 Spawner 按父模式限制访问级别，子任务内的工具调用各自受只读包装约束。
		return r.base.Execute(ctx, call)
	case "remember":
		return r.deny("只读模式不允许写入长期记忆；如需写入请切换到放行模式或默认模式")
	default:
		// 已注册但未显式授权的工具默认拒绝，避免以后新增工具意外绕过只读策略。
		return r.deny(fmt.Sprintf("只读模式未授权工具 %q", call.Name))
	}
}

func (r *ReadOnly) read(ctx context.Context, arguments json.RawMessage) (Result, error) {
	var args readArguments
	if err := json.Unmarshal(arguments, &args); err != nil {
		return Result{}, invalidArguments("read 参数格式无效", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return Result{}, invalidArguments("read 缺少 path 参数", nil)
	}
	if _, err := r.scope.resolve(args.Path); err != nil {
		if errors.Is(err, errPathOutsideWorkspace) {
			return r.deny(fmt.Sprintf("只读模式只允许读取工作目录内的文件：%s", args.Path))
		}
		return Result{}, invalidArguments("read 路径无效", err)
	}
	return r.base.Execute(ctx, model.ToolCall{Name: "read", Arguments: arguments})
}

func (r *ReadOnly) executeBash(ctx context.Context, arguments json.RawMessage) (Result, error) {
	var args bashArguments
	if err := json.Unmarshal(arguments, &args); err != nil {
		return Result{}, invalidArguments("bash 参数格式无效", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return Result{}, invalidArguments("bash 的 command 不能为空", nil)
	}
	return r.bash.execute(ctx, args.Command, args.TimeoutSeconds)
}

func (r *ReadOnly) deny(message string) (Result, error) {
	return Result{Content: message, IsError: true}, permissionDenied(message, nil)
}
