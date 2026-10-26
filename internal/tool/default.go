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
			definitions[i].Description += " 默认模式按分段检查用户级查询白名单或已记录的项目命令前缀；受控的 cd && 简单命令链、|| 和 ; 可按规则执行，只读模式支持固定查询两段管道，其他命令需要审批。"
		case "skill":
			definitions[i].Description += " 默认模式自动加载已发现的 Skill。"
		case "memory":
			definitions[i].Description += " 默认模式自动查询长期记忆。"
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

type chainSegmentPlan struct {
	index      int
	operator   string
	tokens     []string
	cwd        string
	cd         bool
	cdTarget   string
	query      *restrictedCommand
	prefix     []string
	outside    []string
	dangerous  string
	reason     string
	auto       bool
	recordable bool
}

type chainPlan struct {
	command        string
	segments       []chainSegmentPlan
	recordPrefixes [][]string
	recordPaths    []PathRule
	auto           bool
	recordable     bool
	hasOr          bool
	reason         string
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
	if chain, claimed := parseBashChain(args.Command); claimed {
		return d.executeBashChain(ctx, call, chain, timeout)
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
			return d.approveBashThenExecute(ctx, call, args.Command, tokens, [][]string{prefix}, outside, timeout, reason)
		}
	}
	return d.approveBashThenExecute(ctx, call, args.Command, tokens, nil, nil, timeout,
		bashApprovalReason(tokenErr, tokens))
}

func (d *Default) executeBashChain(ctx context.Context, call model.ToolCall, chain bashChain, timeout time.Duration) (Result, error) {
	plan, err := d.analyzeBashChain(chain)
	if err != nil {
		return Result{}, err
	}
	if plan.auto {
		return executeChainPlan(ctx, d.runtime, plan, timeout)
	}
	return d.approveBashChainThenExecute(ctx, call, plan, timeout)
}

func (d *Default) analyzeBashChain(chain bashChain) (chainPlan, error) {
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

	for index, segment := range chain.segments {
		part := chainSegmentPlan{
			index:      index + 1,
			operator:   segment.operator,
			tokens:     append([]string(nil), segment.tokens...),
			cwd:        currentDir,
			auto:       true,
			recordable: true,
		}
		if segment.operator == "||" {
			plan.hasOr = true
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
			plan.segments = append(plan.segments, part)
			if !part.auto {
				plan.auto = false
			}
			currentDir = resolved
			continue
		}

		resolver := chainPathResolver{scope: d.scope, base: currentDir}
		if hit, ok := dangerousSuffixHit(segment.tokens[0], segment.tokens); ok {
			part.dangerous = hit
			part.auto = false
			part.recordable = false
			plan.recordable = false
			addReason(fmt.Sprintf("分段命中固定危险后缀 %s", hit))
			plan.segments = append(plan.segments, part)
			plan.auto = false
			continue
		}
		if spec, err := buildRestrictedCommandTokens(segment.tokens, resolver); err == nil {
			part.query = &spec
			part.outside = d.outsidePaths(spec.paths)
			if len(part.outside) > 0 {
				part.auto = false
				addReason("查询路径不在项目路径白名单")
				for _, path := range part.outside {
					addPath(path, "")
				}
			}
			plan.segments = append(plan.segments, part)
			if !part.auto {
				plan.auto = false
			}
			continue
		}

		prefix, ok := recordablePrefix(segment.tokens)
		if ok {
			outside, err := d.commandOutsidePathsFrom(segment.tokens, currentDir)
			if err != nil {
				part.auto = false
				part.recordable = false
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
			plan.segments = append(plan.segments, part)
			if !part.auto {
				plan.auto = false
			}
			continue
		}

		part.auto = false
		part.recordable = false
		plan.recordable = false
		addReason("分段命令不在用户级白名单或项目前缀白名单")
		plan.segments = append(plan.segments, part)
		plan.auto = false
	}
	if plan.hasOr {
		plan.recordable = false
		plan.recordPrefixes = nil
		plan.recordPaths = nil
	}
	return plan, nil
}

func (d *Default) approveBashChainThenExecute(ctx context.Context, call model.ToolCall, plan chainPlan, timeout time.Duration) (Result, error) {
	request := ApprovalRequest{
		ToolCallID: call.ID,
		Tool:       "bash",
		WorkDir:    d.runtime.WorkDir,
		Target:     plan.command,
		Command:    plan.command,
		Reason:     plan.reason,
		Detail:     fmt.Sprintf("将按 %d 段直接执行，不经过 shell。", len(plan.segments)),
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
	return executeChainPlan(ctx, d.runtime, plan, timeout)
}

func executeChainPlan(ctx context.Context, runtime Runtime, plan chainPlan, timeout time.Duration) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(timeout)
	var output strings.Builder
	truncated := false
	appendOutput := func(content string) {
		if truncated || content == "" {
			return
		}
		limit := runtime.MaxOutputBytes
		if limit <= 0 || output.Len()+len(content) <= limit {
			output.WriteString(content)
			return
		}
		remaining := limit - output.Len()
		if remaining > 0 {
			output.WriteString(content[:remaining])
		}
		truncated = true
	}

	lastCode := 0
	var lastResult Result
	var lastErr error
	lastCompleted := true
	for index, segment := range plan.segments {
		if index > 0 {
			switch segment.operator {
			case "&&":
				if lastCode != 0 {
					continue
				}
			case "||":
				if lastCode == 0 {
					continue
				}
			case ";":
				// Always execute after a completed command.
			}
		}
		if segment.cd {
			lastCode = 0
			lastCompleted = true
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return Result{Content: output.String(), IsError: true}, executionError("命令链执行超时", context.DeadlineExceeded)
		}
		segmentRuntime := runtime
		segmentRuntime.WorkDir = segment.cwd
		var outcome argvOutcome
		if segment.query != nil {
			outcome = executeRestrictedSpecOutcome(ctx, segmentRuntime, *segment.query, remaining)
		} else {
			if len(segment.tokens) == 0 {
				return Result{Content: output.String(), IsError: true}, invalidArguments("命令链包含空分段", nil)
			}
			spec := restrictedCommand{program: segment.tokens[0], args: append([]string(nil), segment.tokens[1:]...)}
			outcome = executeArgvOutcome(ctx, segmentRuntime, spec, remaining, segmentRuntime.Env, nil)
		}
		appendOutput(outcome.result.Content)
		lastResult, lastErr, lastCode = outcome.result, outcome.err, outcome.exitCode
		lastCompleted = outcome.err == nil || outcome.exitCode >= 0
		if outcome.err != nil && outcome.exitCode < 0 {
			content := output.String()
			if truncated && !strings.HasSuffix(content, "\n[输出已截断：超过限制，已省略后续内容]") {
				content += "\n[输出已截断：超过限制，已省略后续内容]"
			}
			return Result{Content: content, IsError: true}, outcome.err
		}
	}
	content := output.String()
	if truncated {
		content += "\n[输出已截断：超过限制，已省略后续内容]"
	}
	if !lastCompleted {
		return Result{Content: content, IsError: true}, lastErr
	}
	if lastCode != 0 {
		return Result{Content: content, IsError: lastResult.IsError}, lastErr
	}
	return Result{Content: content}, nil
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

func (d *Default) approveBashThenExecute(ctx context.Context, call model.ToolCall, command string, tokens []string, prefixes [][]string, outside []string, timeout time.Duration, reason string) (Result, error) {
	request := ApprovalRequest{
		ToolCallID: call.ID,
		Tool:       "bash",
		WorkDir:    d.runtime.WorkDir,
		Target:     command,
		Command:    command,
		Reason:     reason,
	}
	if d.rules.CanRecord() {
		request.RecordPrefixes = clonePrefixes(prefixes)
		for _, prefix := range prefixes {
			if warning := prefixExecutionWarning(prefix); warning != "" {
				request.Detail += warning
			}
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
	return d.commandOutsidePathsFrom(tokens, d.scope.workDir)
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

func bashApprovalReason(tokenErr error, tokens []string) string {
	if tokenErr != nil {
		return "命令包含未解析的 shell 语法，不能记录；可拆成 cd && 简单命令，管道和重定向仍需本次审批"
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
