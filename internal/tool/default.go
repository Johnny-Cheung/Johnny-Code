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
	scope.setSession(gate.Session())
	return &Default{
		base:    base,
		runtime: runtime,
		scope:   scope,
		rules:   rules,
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
			definitions[i].Description += " 默认模式按分段检查用户级查询白名单或已记录的项目命令前缀；受控的 cd、&&、||、;、多段固定查询管道和 2>&1、/dev/null 尾部重定向会先检查全部分段再直接执行，其他命令或 shell 语法需要审批。"
		case "skill":
			definitions[i].Description += " 默认模式自动加载已发现的 Skill。"
		case "memory":
			definitions[i].Description += " 默认模式自动查询长期记忆。"
		case "remember":
			definitions[i].Description += " 默认模式写入长期记忆需要审批。"
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
	case "skill":
		// Skill 只读取启动时已加载的本地指导，不接受路径参数，不触发审批。
		return d.base.Execute(ctx, call)
	case "memory":
		// 记忆查询只读，不触发审批。
		return d.base.Execute(ctx, call)
	case "task", "workflow":
		// 派出子任务本身不访问文件；子任务内的每次工具调用各自按本模式检查。
		return d.base.Execute(ctx, call)
	case "remember":
		var args rememberArguments
		if err := json.Unmarshal(arguments, &args); err != nil {
			return Result{}, invalidArguments("remember 参数格式无效", err)
		}
		request := ApprovalRequest{
			ToolCallID: call.ID,
			Tool:       "remember",
			WorkDir:    d.runtime.WorkDir,
			Target:     args.Name,
			Reason:     "写入长期记忆需要审批",
			Detail:     fmt.Sprintf("目标：%s；描述：%s", args.Target, args.Description),
		}
		decision, err := d.approve(ctx, request)
		if err != nil {
			return Result{}, err
		}
		if !decision.Allow {
			return d.deny(approvalDeniedMessage(request, decision))
		}
		return d.base.Execute(ctx, call)
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
	if rule, err := canonicalPathRule(resolved, ""); err == nil {
		request.SessionPaths = []PathRule{rule}
		if d.rules.CanRecord() {
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

type chainSegmentPlan struct {
	index          int
	pipeline       int
	pipe           bool
	operator       string
	tokens         []string
	redirects      []bashRedirect
	cwd            string
	cd             bool
	cdTarget       string
	query          *restrictedCommand
	prefix         []string
	outside        []string
	dangerous      string
	reason         string
	auto           bool
	recordable     bool
	sessionBlocked bool
}

type chainPlan struct {
	command          string
	segments         []chainSegmentPlan
	controlled       controlledChain
	approvalSegments []int
	recordPrefixes   [][]string
	recordPaths      []PathRule
	auto             bool
	recordable       bool
	hasOr            bool
	reason           string
	sessionCommands  []CommandGrant
	sessionPaths     []PathRule
	sessionBlocked   bool
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
	chain, parseErr := parseShellCommand(args.Command)
	if parseErr != nil {
		// 通配符在默认模式也直接拒绝：受控执行不展开通配符，走完整
		// shell 审批会变成每次换参数都重新询问，而且无法加入会话授权。
		if errors.Is(parseErr, errShellGlob) {
			return Result{}, permissionDenied("默认模式拒绝该命令："+parseErr.Error()+"；需要 shell 通配符时可使用 --skip 放行模式", parseErr)
		}
		return d.approveShellFallback(ctx, call, args.Command, parseErr)
	}
	plan, err := d.analyzeShellChain(chain)
	if err != nil {
		return Result{}, err
	}
	if plan.auto {
		return executeControlledChain(ctx, d.runtime, plan.controlled, timeout)
	}
	return d.approveBashChainThenExecute(ctx, call, plan, timeout)
}

// approveShellFallback 对无法安全转成 argv 的命令只做一次完整 shell 审批，
// 且不提供项目规则或会话授权目标。
func (d *Default) approveShellFallback(ctx context.Context, call model.ToolCall, command string, parseErr error) (Result, error) {
	request := ApprovalRequest{
		ToolCallID: call.ID,
		Tool:       "bash",
		WorkDir:    d.runtime.WorkDir,
		Target:     command,
		Command:    command,
		Reason:     "命令无法直接执行：" + parseErr.Error() + "；需要完整 shell 审批，本次不能记录或加入会话授权",
		Detail:     parseErr.Error(),
	}
	decision, err := d.approve(ctx, request)
	if err != nil {
		return Result{}, err
	}
	if !decision.Allow {
		return d.deny(approvalDeniedMessage(request, decision))
	}
	return d.shell.Execute(ctx, call.Arguments)
}

func (d *Default) analyzeShellChain(chain bashChain) (chainPlan, error) {
	plan := chainPlan{
		command:    chain.command,
		auto:       true,
		recordable: true,
	}
	currentDir := d.scope.workDir
	seenPrefixes := make(map[string]bool)
	seenPaths := make(map[string]bool)
	addReason := func(reason string) {
		if reason == "" {
			return
		}
		if plan.reason == "" {
			plan.reason = reason
			return
		}
		if !strings.Contains(plan.reason, reason) {
			plan.reason += "；" + reason
		}
	}
	addPath := func(path string, kind PathKind) {
		rule, err := canonicalPathRule(path, kind)
		if err != nil {
			return
		}
		key := rule.Path + "\x00" + string(rule.Kind)
		if seenPaths[key] {
			return
		}
		seenPaths[key] = true
		plan.recordPaths = append(plan.recordPaths, rule)
	}
	addPrefix := func(prefix []string) {
		key := strings.Join(prefix, "\x00")
		if seenPrefixes[key] {
			return
		}
		seenPrefixes[key] = true
		plan.recordPrefixes = append(plan.recordPrefixes, append([]string(nil), prefix...))
	}
	finishPart := func(part chainSegmentPlan) {
		commands, paths, eligible := d.segmentSessionRequirements(part)
		if !eligible {
			plan.sessionBlocked = true
		} else {
			plan.sessionCommands = append(plan.sessionCommands, commands...)
			plan.sessionPaths = append(plan.sessionPaths, paths...)
			if len(commands) == 0 && len(paths) == 0 {
				part.auto = true
			}
		}
		if !part.auto {
			plan.auto = false
			plan.approvalSegments = append(plan.approvalSegments, part.index)
		}
		plan.segments = append(plan.segments, part)
	}

	for pipelineIndex, pipeline := range chain.pipelines {
		plannedPipeline := controlledPipeline{operator: pipeline.operator}
		if pipeline.operator == "||" {
			plan.hasOr = true
		}
		for segmentIndex := pipeline.start; segmentIndex < pipeline.end; segmentIndex++ {
			segment := chain.segments[segmentIndex]
			part := chainSegmentPlan{
				index:      segmentIndex + 1,
				pipeline:   pipelineIndex + 1,
				pipe:       segment.pipe,
				operator:   segment.operator,
				tokens:     append([]string(nil), segment.tokens...),
				redirects:  append([]bashRedirect(nil), segment.redirects...),
				cwd:        currentDir,
				auto:       true,
				recordable: true,
			}
			if segment.tokens[0] == "cd" {
				resolved, err := d.scope.resolveExistingFrom(currentDir, segment.tokens[1])
				if err != nil {
					return chainPlan{}, executionError(fmt.Sprintf("cd 目标不可用：%s", segment.tokens[1]), err)
				}
				info, err := os.Stat(resolved)
				if err != nil {
					return chainPlan{}, executionError(fmt.Sprintf("cd 目标不可用：%s", segment.tokens[1]), err)
				}
				if !info.IsDir() {
					return chainPlan{}, executionError(fmt.Sprintf("cd 目标不是目录：%s", segment.tokens[1]), nil)
				}
				part.cd = true
				part.cdTarget = resolved
				if !d.scope.contains(resolved) {
					part.auto = false
					addReason("cd 目标不在项目路径白名单")
					addPath(resolved, PathKindDir)
				}
				finishPart(part)
				currentDir = resolved
				plannedPipeline.virtual = true
				continue
			}

			resolver := chainPathResolver{scope: d.scope, base: currentDir}
			if hit, ok := dangerousSuffixHit(segment.tokens[0], segment.tokens); ok {
				part.dangerous = hit
				part.auto = false
				part.recordable = false
				plan.recordable = false
				addReason(fmt.Sprintf("分段命中固定危险后缀 %s", hit))
				plannedPipeline.segments = append(plannedPipeline.segments, directControlledSegment(segment.tokens, segment.redirects, currentDir, d.runtime.Env))
				finishPart(part)
				continue
			}
			spec, queryErr := buildRestrictedCommandTokens(segment.tokens, resolver, segment.pipe)
			if queryErr == nil {
				part.query = &spec
				part.outside = d.outsidePaths(spec.paths)
				if len(part.outside) > 0 {
					part.auto = false
					addReason("查询路径不在项目路径白名单")
					for _, path := range part.outside {
						addPath(path, "")
					}
				}
				plannedPipeline.segments = append(plannedPipeline.segments, queryControlledSegment(spec, segment.redirects, currentDir, restrictedEnvironment(d.runtime.Env)))
				finishPart(part)
				continue
			}

			if prefix, ok := recordablePrefix(segment.tokens); ok {
				outside, pathErr := d.commandOutsidePathsFrom(segment.tokens, currentDir)
				if pathErr != nil {
					part.auto = false
					part.recordable = false
					part.sessionBlocked = true
					plan.recordable = false
					addReason("分段包含无法解析的路径参数")
				} else {
					part.prefix = append([]string(nil), prefix...)
					part.outside = outside
					if len(outside) > 0 {
						part.auto = false
						addReason("命令路径不在项目路径白名单")
						for _, path := range outside {
							addPath(path, "")
						}
					}
					if !d.rules.ContainsPrefix(segment.tokens) {
						part.auto = false
						addPrefix(prefix)
						addReason("分段命令不在项目前缀白名单")
					}
				}
				plannedPipeline.segments = append(plannedPipeline.segments, directControlledSegment(segment.tokens, segment.redirects, currentDir, d.runtime.Env))
				finishPart(part)
				continue
			}

			// 未知程序仍按直接 argv 执行；本次审批通过后不会回退到 shell。
			part.auto = false
			part.recordable = false
			plan.recordable = false
			if _, known := fixedQueryCommandByName[segment.tokens[0]]; known {
				addReason("查询形式未通过自动放行校验：" + queryErr.Error())
			} else {
				addReason("分段命令不在用户级白名单或项目前缀白名单")
			}
			plannedPipeline.segments = append(plannedPipeline.segments, directControlledSegment(segment.tokens, segment.redirects, currentDir, d.runtime.Env))
			finishPart(part)
		}
		plan.controlled.pipelines = append(plan.controlled.pipelines, plannedPipeline)
	}
	if plan.hasOr {
		plan.recordable = false
		plan.recordPrefixes = nil
		plan.recordPaths = nil
	}
	return plan, nil
}

func (d *Default) approveBashChainThenExecute(ctx context.Context, call model.ToolCall, plan chainPlan, timeout time.Duration) (Result, error) {
	detail := fmt.Sprintf("将按 %d 段直接执行，不经过 shell。", len(plan.segments))
	if len(plan.approvalSegments) > 0 {
		detail += "需要审批的分段：" + formatSegmentIndices(plan.approvalSegments) + "。"
	}
	request := ApprovalRequest{
		ToolCallID: call.ID,
		Tool:       "bash",
		WorkDir:    d.runtime.WorkDir,
		Target:     plan.command,
		Command:    plan.command,
		Reason:     plan.reason,
		Detail:     detail,
	}
	if !plan.sessionBlocked {
		request.SessionCommands = plan.sessionCommands
		request.SessionPaths = plan.sessionPaths
	}
	if d.rules.CanRecord() && plan.recordable {
		request.RecordPrefixes = clonePrefixes(plan.recordPrefixes)
		request.RecordPaths = append([]PathRule(nil), plan.recordPaths...)
		for _, prefix := range plan.recordPrefixes {
			if warning := prefixExecutionWarning(prefix); warning != "" {
				request.Detail += " " + warning
			}
		}
	}
	decision, err := d.approve(ctx, request)
	if err != nil {
		return Result{}, err
	}
	if !decision.Allow {
		return d.deny(approvalDeniedMessage(request, decision))
	}
	return executeControlledChain(ctx, d.runtime, plan.controlled, timeout)
}

func formatSegmentIndices(indices []int) string {
	if len(indices) == 0 {
		return "无"
	}
	parts := make([]string, 0, len(indices))
	for _, index := range indices {
		parts = append(parts, strconv.Itoa(index))
	}
	return strings.Join(parts, "、")
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

func (d *Default) commandOutsidePathsFrom(tokens []string, base string) ([]string, error) {
	seen := make(map[string]bool)
	outside := make([]string, 0, 2)
	for _, candidate := range commandPathCandidates(tokens) {
		resolved, err := d.scope.resolveFrom(base, candidate)
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
	request.Origin = OriginFrom(ctx)
	if d.gate.TrySession(request) {
		return ApprovalDecision{Allow: true, Source: ApprovalSourceSession, Reason: "命中本会话授权"}, nil
	}
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
	return d.rules.AddRules(request.RecordPrefixes, request.RecordPaths)
}

func (d *Default) deny(message string) (Result, error) {
	return Result{Content: message, IsError: true}, permissionDenied(message, nil)
}

var shellBuiltinCommands = map[string]bool{
	"cd": true, ":": true, "export": true, "set": true, "unset": true, "alias": true,
	"unalias": true, "source": true, ".": true, "eval": true, "exec": true,
	"command": true, "builtin": true, "ulimit": true, "umask": true,
	"read": true, "shift": true, "trap": true, "type": true, "hash": true,
	"jobs": true, "fg": true, "bg": true, "wait": true, "return": true,
	"break": true, "continue": true, "test": true, "[": true,
	"local": true, "declare": true, "typeset": true, "let": true, "mapfile": true,
	"readarray": true, "caller": true, "enable": true, "help": true, "history": true,
	"logout": true, "popd": true, "pushd": true, "dirs": true, "disown": true,
	"suspend": true, "times": true, "bind": true, "compgen": true, "complete": true,
	"compopt": true, "shopt": true, "getopts": true,
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
