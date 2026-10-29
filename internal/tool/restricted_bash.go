package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// restrictedCommand 是只读模式允许执行的简单查询命令。它不携带 shell，
// 只保留已经校验过的程序和参数。paths 保存已经解析的路径操作数，供默认
// 模式判断是否需要路径审批。
type restrictedCommand struct {
	program    string
	args       []string
	paths      []string
	readsStdin bool
}

type queryPathResolver interface {
	resolve(path string) (string, error)
	resolveExisting(path string) (string, error)
}

var errRestrictedSyntax = errors.New("只读命令格式无效")

// tokenizeRestricted 把一条简单命令拆成 argv。引号内的 shell 元字符按
// 字面量保留，方便 grep 使用正则；引号外的管道、重定向、命令替换和复合
// 命令一律拒绝。这里不做变量、通配符或命令替换展开。
func tokenizeRestricted(command string) ([]string, error) {
	var tokens []string
	var current strings.Builder
	tokenStarted := false
	inSingle, inDouble, escaped := false, false, false

	flush := func() {
		if tokenStarted {
			tokens = append(tokens, current.String())
			current.Reset()
			tokenStarted = false
		}
	}

	for i := 0; i < len(command); {
		r, size := utf8.DecodeRuneInString(command[i:])
		if r == utf8.RuneError && size == 1 {
			return nil, fmt.Errorf("%w：包含无效 UTF-8", errRestrictedSyntax)
		}
		i += size
		if r == 0 {
			return nil, fmt.Errorf("%w：包含 NUL", errRestrictedSyntax)
		}
		if escaped {
			escaped = false
			if inDouble && r != '"' && r != '\\' && r != '$' && r != '`' {
				// 双引号内未识别的反斜杠保留，避免破坏 \d 等正则。
				current.WriteByte('\\')
			}
			current.WriteRune(r)
			tokenStarted = true
			continue
		}
		switch {
		case inSingle:
			if r == '\'' {
				inSingle = false
			} else {
				if r == '\n' || r == '\r' {
					return nil, fmt.Errorf("%w：不支持多行命令", errRestrictedSyntax)
				}
				current.WriteRune(r)
			}
			tokenStarted = true
		case inDouble:
			switch r {
			case '"':
				inDouble = false
			case '\\':
				escaped = true
			case '\n', '\r':
				return nil, fmt.Errorf("%w：不支持多行命令", errRestrictedSyntax)
			default:
				current.WriteRune(r)
			}
			tokenStarted = true
		default:
			switch r {
			case ' ', '\t':
				flush()
			case '\n', '\r':
				return nil, fmt.Errorf("%w：不支持多行命令", errRestrictedSyntax)
			case '\'':
				inSingle = true
				tokenStarted = true
			case '"':
				inDouble = true
				tokenStarted = true
			case '|', '&', ';', '<', '>', '$', '`', '(', ')':
				return nil, fmt.Errorf("%w：不支持 shell 操作符 %q", errRestrictedSyntax, r)
			case '\\':
				escaped = true
				tokenStarted = true
			default:
				current.WriteRune(r)
				tokenStarted = true
			}
		}
	}
	if escaped {
		return nil, fmt.Errorf("%w：末尾存在未完成转义", errRestrictedSyntax)
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("%w：存在未闭合引号", errRestrictedSyntax)
	}
	flush()
	if len(tokens) == 0 {
		return nil, fmt.Errorf("%w：命令不能为空", errRestrictedSyntax)
	}
	return tokens, nil
}

// buildRestrictedCommand 校验只读白名单并返回实际执行的 argv。它只允许
// 固定查询命令的受限形式；任何未知程序或参数都在启动进程前拒绝。
func buildRestrictedCommand(command string, scope queryPathResolver) (restrictedCommand, error) {
	tokens, err := tokenizeRestricted(command)
	if err != nil {
		return restrictedCommand{}, permissionDenied("只读模式不支持该 shell 命令", err)
	}
	return buildRestrictedCommandTokens(tokens, scope, false)
}

// buildRestrictedCommandTokens 按固定表解析一个命令段。stdin 为 true 表示该段处于
// 管道中，没有路径操作数的形式读取上一段的输出。
func buildRestrictedCommandTokens(tokens []string, scope queryPathResolver, stdin bool) (restrictedCommand, error) {
	if len(tokens) == 0 || strings.TrimSpace(tokens[0]) == "" {
		return restrictedCommand{}, invalidArguments("命令不能为空", nil)
	}
	spec, ok := fixedQueryCommandByName[tokens[0]]
	if !ok {
		return restrictedCommand{}, permissionDenied(fmt.Sprintf("只读模式不允许执行 %q", tokens[0]), nil)
	}
	return spec.parse(tokens, scope, stdin)
}

func parsePwd(tokens []string, _ queryPathResolver, _ bool) (restrictedCommand, error) {
	if len(tokens) != 1 {
		return restrictedCommand{}, permissionDenied("只读模式的 pwd 不接受参数", nil)
	}
	return restrictedCommand{program: "pwd"}, nil
}

func resolveQueryPaths(scope queryPathResolver, paths []string) ([]string, error) {
	resolved := make([]string, 0, len(paths))
	for _, path := range paths {
		target, err := scope.resolveExisting(path)
		if err != nil {
			return nil, queryPathError(path, err)
		}
		resolved = append(resolved, target)
	}
	return resolved, nil
}

func queryPathError(path string, err error) error {
	if errors.Is(err, errPathOutsideWorkspace) {
		return permissionDenied(fmt.Sprintf("只读模式不允许访问工作目录外的路径 %q", path), err)
	}
	return executionError(fmt.Sprintf("只读查询路径不可用：%s", path), err)
}

// restrictedBash 执行经过 buildRestrictedCommand 校验的查询命令。它永远
// 不启动 shell，只直接执行白名单程序。
type restrictedBash struct {
	runtime Runtime
	scope   workspaceScope
}

func newRestrictedBash(runtime Runtime) (*restrictedBash, error) {
	runtime = normalizeRuntime(runtime)
	scope, err := newWorkspaceScope(runtime.WorkDir)
	if err != nil {
		return nil, err
	}
	return &restrictedBash{runtime: runtime, scope: scope}, nil
}

func (b *restrictedBash) execute(ctx context.Context, command string, timeoutSeconds *int) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(command) == "" {
		return Result{}, invalidArguments("bash 的 command 不能为空", nil)
	}
	timeout, err := commandTimeout(b.runtime, timeoutSeconds)
	if err != nil {
		return Result{}, err
	}
	chain, parseErr := parseShellCommand(command)
	if parseErr != nil {
		return Result{}, permissionDenied("只读模式不支持该命令："+parseErr.Error(), parseErr)
	}
	controlled, err := buildRestrictedChain(chain, b.scope, b.runtime.Env)
	if err != nil {
		return Result{}, err
	}
	return executeControlledChain(ctx, b.runtime, controlled, timeout)
}

// workspaceChainResolver 在虚拟 cd 之后继续把相对路径解析到新的基准目录，
// 同时仍然要求结果位于只读工作目录内。
type workspaceChainResolver struct {
	scope workspaceScope
	base  string
}

func (r workspaceChainResolver) resolve(path string) (string, error) {
	return resolveWorkspaceFrom(r.scope, r.base, path, false)
}

func (r workspaceChainResolver) resolveExisting(path string) (string, error) {
	return resolveWorkspaceFrom(r.scope, r.base, path, true)
}

func resolveWorkspaceFrom(scope workspaceScope, base, path string, existing bool) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("路径不能为空")
	}
	if strings.IndexByte(path, 0) >= 0 {
		return "", errors.New("路径包含无效字符")
	}
	if strings.TrimSpace(base) == "" {
		return "", errors.New("路径基准不能为空")
	}
	// 不做 TrimSpace/Clean：符号链接之后的 ".." 必须按内核语义解析，
	// 且以空格结尾的目录名是合法目标。
	resolved, err := resolvePathFromBase(base, path)
	if err != nil {
		return "", err
	}
	if !scope.contains(resolved) {
		return "", fmt.Errorf("%w: %s", errPathOutsideWorkspace, path)
	}
	if existing {
		if _, err := os.Stat(resolved); err != nil {
			return "", err
		}
	}
	return resolved, nil
}

// buildRestrictedChain 在启动任何进程之前校验全部命令段、路径和 cd 目标。
func buildRestrictedChain(chain bashChain, scope workspaceScope, baseEnv []string) (controlledChain, error) {
	currentDir := scope.root
	env := restrictedEnvironment(baseEnv)
	controlled := controlledChain{pipelines: make([]controlledPipeline, 0, len(chain.pipelines))}
	for _, pipeline := range chain.pipelines {
		planned := controlledPipeline{operator: pipeline.operator}
		for index := pipeline.start; index < pipeline.end; index++ {
			segment := chain.segments[index]
			if segment.tokens[0] == "cd" {
				target, err := resolveWorkspaceFrom(scope, currentDir, segment.tokens[1], true)
				if err != nil {
					if errors.Is(err, errPathOutsideWorkspace) {
						return controlledChain{}, permissionDenied(fmt.Sprintf("只读模式不允许切换到工作目录外的目录 %q", segment.tokens[1]), err)
					}
					return controlledChain{}, executionError(fmt.Sprintf("cd 目标不可用：%s", segment.tokens[1]), err)
				}
				info, err := os.Stat(target)
				if err != nil {
					return controlledChain{}, executionError(fmt.Sprintf("cd 目标不可用：%s", segment.tokens[1]), err)
				}
				if !info.IsDir() {
					return controlledChain{}, executionError(fmt.Sprintf("cd 目标不是目录：%s", segment.tokens[1]), nil)
				}
				currentDir = target
				planned.virtual = true
				continue
			}
			resolver := workspaceChainResolver{scope: scope, base: currentDir}
			spec, err := buildRestrictedCommandTokens(segment.tokens, resolver, segment.pipe)
			if err != nil {
				return controlledChain{}, err
			}
			planned.segments = append(planned.segments, queryControlledSegment(spec, segment.redirects, currentDir, env))
		}
		controlled.pipelines = append(controlled.pipelines, planned)
	}
	return controlled, nil
}

// restrictedEnvironment 只保留查询命令运行所需的最小环境，避免把 Johnny、
// 模型服务和云凭据变量传给子进程。它不是沙箱，命令仍可读取磁盘文件。
func restrictedEnvironment(base []string) []string {
	if base == nil {
		base = os.Environ()
	}
	env := make([]string, 0, 8)
	hasPath := false
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if name == "PATH" {
			hasPath = true
			env = append(env, entry)
			continue
		}
		if name == "LANG" || strings.HasPrefix(name, "LC_") {
			env = append(env, entry)
		}
	}
	if !hasPath {
		if path := os.Getenv("PATH"); path != "" {
			env = append(env, "PATH="+path)
		}
	}
	return env
}
