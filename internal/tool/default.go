package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

// Default 实现默认权限模式：路径白名单内文件操作自动执行；用户级查询
// 白名单和项目前缀白名单自动执行；其余操作进入审批。它仍是应用层策略，
// 不是 OS 沙箱。
type Default struct {
	base    *Registry
	runtime Runtime
	scope   *multiPathScope
	rules   *ProjectRules
	bash    *restrictedBash
	shell   *Bash
	gate    *ApprovalGate
}

func NewDefault(base *Registry, runtime Runtime, rules *ProjectRules, gate *ApprovalGate) (*Default, error) {
	if base == nil {
		return nil, errors.New("默认模式工具注册表不能为空")
	}
	if rules == nil {
		return nil, errors.New("项目权限规则不能为空")
	}
	runtime = normalizeRuntime(runtime)
	runtime.Env = defaultEnvironment(runtime.Env)
	scope, err := newMultiPathScope(runtime.WorkDir, rules)
	if err != nil {
		return nil, err
	}
	return &Default{
		base:    base,
		runtime: runtime,
		scope:   scope,
		rules:   rules,
		bash:    &restrictedBash{runtime: runtime, scope: scope},
		shell:   NewBash(runtime),
		gate:    gate,
	}, nil
}

func (d *Default) Definitions() []model.ToolDefinition {
	definitions := d.base.Definitions()
	for i := range definitions {
		switch definitions[i].Name {
		case "read":
			definitions[i].Description += " 默认模式自动读取路径白名单内文件，范围外需要审批。"
		case "write", "edit":
			definitions[i].Description += " 默认模式自动执行路径白名单内修改，范围外需要审批。"
		case "bash":
			definitions[i].Description += " 默认模式自动执行用户级查询白名单或已记录的项目命令前缀，其他命令需要审批。"
		}
	}
	return definitions
}

func (d *Default) Execute(ctx context.Context, call model.ToolCall) (Result, error) {
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	if d == nil || d.base == nil {
		return Result{}, invalidArguments("默认模式工具注册表不能为空", nil)
	}
	if _, ok := d.base.Get(call.Name); !ok {
		return d.base.Execute(ctx, call)
	}
	arguments := call.Arguments
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if !jsonObject(arguments) {
		return Result{}, invalidArguments("工具参数必须是 JSON 对象", nil)
	}
	switch call.Name {
	case "read", "write", "edit":
		return d.executeFile(ctx, call, arguments)
	case "bash":
		return d.executeBash(ctx, call, arguments)
	default:
		return d.deny(fmt.Sprintf("默认模式未授权工具 %q", call.Name))
	}
}

func (d *Default) executeFile(ctx context.Context, call model.ToolCall, arguments json.RawMessage) (Result, error) {
	path, detail, err := fileArgument(call.Name, arguments)
	if err != nil || strings.TrimSpace(path) == "" {
		return d.base.Execute(ctx, call)
	}
	resolved, err := d.scope.resolve(path)
	if err != nil {
		return d.base.Execute(ctx, call)
	}
	if d.scope.contains(resolved) {
		return d.base.Execute(ctx, call)
	}
	target := resolved
	if path != resolved {
		target = fmt.Sprintf("%s（%s）", path, resolved)
	}
	request := ApprovalRequest{
		ToolCallID: call.ID,
		Tool:       call.Name,
		WorkDir:    d.runtime.WorkDir,
		Target:     target,
		Reason:     "路径不在项目路径白名单",
		Detail:     detail,
	}
	if d.rules.CanRecord() {
		if rule, err := canonicalPathRule(resolved, ""); err == nil {
			request.RecordPaths = []PathRule{rule}
		}
	}
	decision, err := d.approve(ctx, request)
	if err != nil {
		return Result{}, err
	}
	if !decision.Allow {
		return d.deny(approvalDeniedMessage(request, decision))
	}
	return d.base.Execute(ctx, call)
}

func fileArgument(toolName string, arguments json.RawMessage) (path, detail string, err error) {
	switch toolName {
	case "read":
		var args readArguments
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", "", err
		}
		return args.Path, "", nil
	case "write":
		var args writeArguments
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", "", err
		}
		if args.Path != nil {
			path = *args.Path
		}
		if args.Content != nil {
			detail = fmt.Sprintf("写入 %d 字节，内容 %s", len(*args.Content), previewForApproval(*args.Content))
		}
		return path, detail, nil
	case "edit":
		var args editArguments
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", "", err
		}
		if args.Path != nil {
			path = *args.Path
		}
		if args.OldString != nil && args.NewString != nil {
			detail = fmt.Sprintf("替换 %s → %s", previewForApproval(*args.OldString), previewForApproval(*args.NewString))
		}
		return path, detail, nil
	default:
		return "", "", errors.New("未知文件工具")
	}
}

func previewForApproval(text string) string {
	const limit = 160
	if len(text) > limit {
		text = strings.ToValidUTF8(text[:limit], "") + "…"
	}
	return strconv.Quote(text)
}

func (d *Default) executeBash(ctx context.Context, call model.ToolCall, arguments json.RawMessage) (Result, error) {
	var args bashArguments
	if err := json.Unmarshal(arguments, &args); err != nil {
		return Result{}, invalidArguments("bash 参数格式无效", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return Result{}, invalidArguments("bash 的 command 不能为空", nil)
	}
	timeout, err := commandTimeout(d.runtime, args.TimeoutSeconds)
	if err != nil {
		return Result{}, err
	}
	tokens, tokenErr := tokenizeRestricted(args.Command)
	if tokenErr == nil {
		if hit, ok := dangerousSuffixHit(tokens[0], tokens); ok {
			return d.approveBashThenExecute(ctx, call, args.Command, tokens, nil, nil, timeout,
				fmt.Sprintf("命中固定危险后缀 %s，不能记录", hit))
		}
		if spec, parseErr := buildRestrictedCommand(args.Command, d.scope); parseErr == nil {
			outside := d.outsidePaths(spec.paths)
			if len(outside) == 0 {
				return executeRestrictedSpec(ctx, d.runtime, spec, timeout)
			}
			return d.approveWhitelistOutside(ctx, call, args.Command, spec, outside, timeout)
		}
		prefix, recordable := recordablePrefix(tokens)
		if recordable {
			outside, pathErr := d.commandOutsidePaths(tokens)
			if pathErr != nil {
				return d.approveBashThenExecute(ctx, call, args.Command, tokens, nil, nil, timeout,
					"命令包含无法解析的路径参数，不能自动放行")
			}
			if d.rules.ContainsPrefix(tokens) {
				if len(outside) == 0 {
					return d.executeDirect(ctx, tokens, timeout)
				}
				return d.approveBashThenExecute(ctx, call, args.Command, tokens, nil, outside, timeout,
					"命令路径不在项目路径白名单")
			}
			reason := "命令不在用户级白名单或项目前缀白名单"
			if len(outside) > 0 {
				reason += "，且路径不在项目路径白名单"
			}
			return d.approveBashThenExecute(ctx, call, args.Command, tokens, prefix, outside, timeout, reason)
		}
	}
	return d.approveBashThenExecute(ctx, call, args.Command, tokens, nil, nil, timeout,
		bashApprovalReason(tokenErr, tokens))
}

func (d *Default) approveWhitelistOutside(ctx context.Context, call model.ToolCall, command string, spec restrictedCommand, outside []string, timeout time.Duration) (Result, error) {
	request := ApprovalRequest{
		ToolCallID: call.ID,
		Tool:       "bash",
		WorkDir:    d.runtime.WorkDir,
		Target:     command,
		Command:    command,
		Reason:     "查询路径不在项目路径白名单",
	}
	if d.rules.CanRecord() {
		request.RecordPaths = d.recordPathRules(outside)
	}
	decision, err := d.approve(ctx, request)
	if err != nil {
		return Result{}, err
	}
	if !decision.Allow {
		return d.deny(approvalDeniedMessage(request, decision))
	}
	return executeRestrictedSpec(ctx, d.runtime, spec, timeout)
}

func (d *Default) approveBashThenExecute(ctx context.Context, call model.ToolCall, command string, tokens, prefix, outside []string, timeout time.Duration, reason string) (Result, error) {
	request := ApprovalRequest{
		ToolCallID: call.ID,
		Tool:       "bash",
		WorkDir:    d.runtime.WorkDir,
		Target:     command,
		Command:    command,
		Reason:     reason,
	}
	if d.rules.CanRecord() {
		if len(prefix) > 0 {
			request.RecordPrefix = append([]string(nil), prefix...)
			request.Detail = prefixExecutionWarning(prefix)
		}
		request.RecordPaths = d.recordPathRules(outside)
	}
	decision, err := d.approve(ctx, request)
	if err != nil {
		return Result{}, err
	}
	if !decision.Allow {
		return d.deny(approvalDeniedMessage(request, decision))
	}
	if tokens == nil || requiresShell(tokens) {
		return d.shell.Execute(ctx, call.Arguments)
	}
	return d.executeDirect(ctx, tokens, timeout)
}

func (d *Default) recordPathRules(paths []string) []PathRule {
	if !d.rules.CanRecord() {
		return nil
	}
	rules := make([]PathRule, 0, len(paths))
	seen := make(map[string]bool)
	for _, path := range paths {
		rule, err := canonicalPathRule(path, "")
		if err != nil || seen[rule.Path+"\x00"+string(rule.Kind)] {
			continue
		}
		seen[rule.Path+"\x00"+string(rule.Kind)] = true
		rules = append(rules, rule)
	}
	return rules
}

func prefixExecutionWarning(prefix []string) string {
	if len(prefix) < 2 {
		return ""
	}
	switch prefix[0] {
	case "make":
		return "该前缀会执行 Makefile 目标，脚本内部路径访问不受路径白名单约束。"
	case "go":
		if prefix[1] == "test" || prefix[1] == "run" || prefix[1] == "build" {
			return "该前缀会执行 Go 构建或测试代码，内部路径访问不受路径白名单约束。"
		}
	case "cargo":
		if prefix[1] == "test" || prefix[1] == "run" || prefix[1] == "build" || prefix[1] == "bench" || prefix[1] == "clippy" {
			return "该前缀会执行 Cargo 构建或测试代码，内部路径访问不受路径白名单约束。"
		}
	case "npm", "yarn", "pnpm", "bun":
		if prefix[1] == "run" || prefix[1] == "test" || prefix[1] == "start" || prefix[1] == "build" {
			return "该前缀会执行项目脚本，脚本内部路径访问不受路径白名单约束。"
		}
	}
	return ""
}

func (d *Default) executeDirect(ctx context.Context, tokens []string, timeout time.Duration) (Result, error) {
	if len(tokens) == 0 {
		return Result{}, invalidArguments("命令不能为空", nil)
	}
	spec := restrictedCommand{program: tokens[0], args: append([]string(nil), tokens[1:]...)}
	return executeArgv(ctx, d.runtime, spec, timeout, d.runtime.Env, nil)
}

func (d *Default) outsidePaths(paths []string) []string {
	seen := make(map[string]bool)
	outside := make([]string, 0, len(paths))
	for _, path := range paths {
		if d.scope.contains(path) || seen[path] {
			continue
		}
		seen[path] = true
		outside = append(outside, path)
	}
	return outside
}

// commandOutsidePaths 对项目前缀命令做可识别的路径参数检查。它只检查
// 命令行中形似路径的 token 和常见选项值，无法约束脚本内部访问；真正的
// 硬边界仍需要 OS sandbox。
func (d *Default) commandOutsidePaths(tokens []string) ([]string, error) {
	seen := make(map[string]bool)
	outside := make([]string, 0, 2)
	for _, candidate := range commandPathCandidates(tokens) {
		resolved, err := d.scope.resolve(candidate)
		if err != nil {
			return nil, err
		}
		if d.scope.contains(resolved) || seen[resolved] {
			continue
		}
		seen[resolved] = true
		outside = append(outside, resolved)
	}
	return outside, nil
}

func commandPathCandidates(tokens []string) []string {
	candidates := make([]string, 0, 2)
	for _, token := range tokens[1:] {
		if token == "" || token == "--" {
			continue
		}
		value := token
		if strings.HasPrefix(token, "-") {
			if name, optionValue, ok := strings.Cut(token, "="); ok && name != "" {
				value = optionValue
			} else if len(token) > 2 && (token[2] == '/' || token[2] == '.') {
				value = token[2:]
			} else {
				continue
			}
		} else if _, optionValue, ok := strings.Cut(token, "="); ok {
			value = optionValue
		}
		if likelyPathToken(value) {
			candidates = append(candidates, value)
		}
	}
	return candidates
}

func likelyPathToken(value string) bool {
	if value == "" || strings.Contains(value, "://") {
		return false
	}
	if filepath.IsAbs(value) || value == "." || value == ".." ||
		strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") {
		return true
	}
	return strings.ContainsAny(value, `/\`)
}

func (d *Default) approve(ctx context.Context, request ApprovalRequest) (ApprovalDecision, error) {
	decision, err := d.gate.Ask(ctx, request)
	if err != nil {
		d.gate.Audit(request, ApprovalDecision{Source: "error", Reason: err.Error()})
		return ApprovalDecision{}, err
	}
	if decision.Allow && decision.Record {
		if err := d.record(request); err != nil {
			decision.Allow = false
			decision.Record = false
			decision.Reason = "记录失败：" + err.Error()
			d.gate.Audit(request, decision)
			return decision, nil
		}
	}
	d.gate.Audit(request, decision)
	return decision, nil
}

func (d *Default) record(request ApprovalRequest) error {
	if len(request.RecordPrefix) > 0 {
		if err := d.rules.AddPrefix(append([]string(nil), request.RecordPrefix...)); err != nil {
			return err
		}
	}
	for _, path := range request.RecordPaths {
		if err := d.rules.AddPath(path.Path, path.Kind); err != nil {
			return err
		}
	}
	return nil
}

func (d *Default) deny(message string) (Result, error) {
	return Result{Content: message, IsError: true}, permissionDenied(message, nil)
}

func bashApprovalReason(tokenErr error, tokens []string) string {
	if tokenErr != nil {
		return "命令包含未解析的 shell 语法，不能记录"
	}
	if tokens != nil && requiresShell(tokens) {
		return "命令需要 shell 语义，不能记录"
	}
	return "命令不在用户级白名单或项目前缀白名单"
}

var shellBuiltinCommands = map[string]bool{
	"cd": true, "export": true, "set": true, "unset": true, "alias": true,
	"unalias": true, "source": true, ".": true, "eval": true, "exec": true,
	"command": true, "builtin": true, "ulimit": true, "umask": true,
	"read": true, "shift": true, "trap": true, "type": true, "hash": true,
	"jobs": true, "fg": true, "bg": true, "wait": true, "return": true,
	"break": true, "continue": true, "test": true, "[": true,
}

func requiresShell(tokens []string) bool {
	if len(tokens) == 0 {
		return true
	}
	if strings.Contains(tokens[0], "=") {
		return true
	}
	return shellBuiltinCommands[tokens[0]]
}

// defaultEnvironment 保留开发命令需要的一般环境，移除 Johnny、模型服务和
// 常见令牌字段。它不是沙箱：命令仍可读取项目 .env 或其他磁盘配置。
func defaultEnvironment(base []string) []string {
	if base == nil {
		base = os.Environ()
	}
	env := make([]string, 0, len(base))
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || sensitiveEnvironmentName(name) {
			continue
		}
		env = append(env, entry)
	}
	return env
}

func sensitiveEnvironmentName(name string) bool {
	for _, prefix := range []string{"JOHNNY_", "ANTHROPIC_", "OPENAI_", "CLAUDE_"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	switch name {
	case "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "GITHUB_TOKEN", "GH_TOKEN",
		"GITLAB_TOKEN", "NPM_TOKEN", "PYPI_TOKEN", "NUGET_API_KEY":
		return true
	default:
		return false
	}
}
