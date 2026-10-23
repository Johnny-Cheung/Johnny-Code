package tool

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// restrictedCommand 是只读模式允许执行的简单查询命令。它不携带 shell，
// 只保留已经校验过的程序和参数。
type restrictedCommand struct {
	program string
	args    []string
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
// pwd、ls、find、grep 的受限形式；任何未知程序或参数都在启动进程前拒绝。
func buildRestrictedCommand(command string, scope workspaceScope) (restrictedCommand, error) {
	tokens, err := tokenizeRestricted(command)
	if err != nil {
		return restrictedCommand{}, permissionDenied("只读模式不支持该 shell 命令", err)
	}
	switch tokens[0] {
	case "pwd":
		return parsePwd(tokens)
	case "ls":
		return parseLs(tokens, scope)
	case "find":
		return parseFind(tokens, scope)
	case "grep":
		return parseGrep(tokens, scope)
	default:
		return restrictedCommand{}, permissionDenied(fmt.Sprintf("只读模式不允许执行 %q", tokens[0]), nil)
	}
}

func parsePwd(tokens []string) (restrictedCommand, error) {
	if len(tokens) != 1 {
		return restrictedCommand{}, permissionDenied("只读模式的 pwd 不接受参数", nil)
	}
	return restrictedCommand{program: "pwd"}, nil
}

func parseLs(tokens []string, scope workspaceScope) (restrictedCommand, error) {
	var flags []string
	var paths []string
	optionsEnded := false
	for _, token := range tokens[1:] {
		if !optionsEnded {
			if token == "--" {
				optionsEnded = true
				continue
			}
			if strings.HasPrefix(token, "-") && token != "-" {
				if strings.HasPrefix(token, "--") {
					return restrictedCommand{}, permissionDenied(fmt.Sprintf("只读模式的 ls 不支持选项 %q", token), nil)
				}
				for _, option := range token[1:] {
					if !strings.ContainsRune("alh1Fd", option) {
						return restrictedCommand{}, permissionDenied(fmt.Sprintf("只读模式的 ls 不支持选项 %q", token), nil)
					}
				}
				flags = append(flags, token)
				continue
			}
		}
		paths = append(paths, token)
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}
	resolved, err := resolveQueryPaths(scope, paths)
	if err != nil {
		return restrictedCommand{}, err
	}
	args := append([]string{}, flags...)
	args = append(args, "--")
	args = append(args, resolved...)
	return restrictedCommand{program: "ls", args: args}, nil
}

func parseFind(tokens []string, scope workspaceScope) (restrictedCommand, error) {
	rest := tokens[1:]
	path := "."
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		path = rest[0]
		rest = rest[1:]
	}
	if path == "" || strings.HasPrefix(path, "-") {
		return restrictedCommand{}, permissionDenied("只读模式的 find 起始路径不能以 - 开头", nil)
	}
	resolvedPath, err := scope.resolveExisting(path)
	if err != nil {
		return restrictedCommand{}, queryPathError(path, err)
	}
	args := []string{resolvedPath}
	seen := map[string]bool{}
	for i := 0; i < len(rest); i++ {
		expression := rest[i]
		if seen[expression] {
			return restrictedCommand{}, permissionDenied(fmt.Sprintf("只读模式的 find 重复参数 %q", expression), nil)
		}
		switch expression {
		case "-maxdepth":
			if i+1 >= len(rest) {
				return restrictedCommand{}, invalidArguments("只读模式的 find 缺少 -maxdepth 值", nil)
			}
			value, err := strconv.Atoi(rest[i+1])
			if err != nil || value < 0 || value > 32 {
				return restrictedCommand{}, invalidArguments("只读模式的 find -maxdepth 必须在 0 到 32 之间", err)
			}
			seen[expression] = true
			args = append(args, expression, strconv.Itoa(value))
			i++
		case "-type":
			if i+1 >= len(rest) || (rest[i+1] != "f" && rest[i+1] != "d") {
				return restrictedCommand{}, invalidArguments("只读模式的 find -type 只能是 f 或 d", nil)
			}
			seen[expression] = true
			args = append(args, expression, rest[i+1])
			i++
		case "-name":
			if i+1 >= len(rest) || rest[i+1] == "" {
				return restrictedCommand{}, invalidArguments("只读模式的 find 缺少 -name 模式", nil)
			}
			// -name 的下一个 token 即使以 - 开头也是模式值，不会成为新的
			// primary，因此 -delete 不会被误执行。
			seen[expression] = true
			args = append(args, expression, rest[i+1])
			i++
		case "-print":
			seen[expression] = true
			args = append(args, expression)
		default:
			return restrictedCommand{}, permissionDenied(fmt.Sprintf("只读模式的 find 不支持参数 %q", expression), nil)
		}
	}
	return restrictedCommand{program: "find", args: args}, nil
}

func parseGrep(tokens []string, scope workspaceScope) (restrictedCommand, error) {
	var flags []string
	optionsEnded := false
	index := 1
	for index < len(tokens) {
		token := tokens[index]
		if !optionsEnded {
			if token == "--" {
				optionsEnded = true
				index++
				continue
			}
			if strings.HasPrefix(token, "-") && token != "-" {
				if strings.HasPrefix(token, "--") {
					return restrictedCommand{}, permissionDenied(fmt.Sprintf("只读模式的 grep 不支持选项 %q", token), nil)
				}
				for _, option := range token[1:] {
					if !strings.ContainsRune("nirEF", option) {
						return restrictedCommand{}, permissionDenied(fmt.Sprintf("只读模式的 grep 不支持选项 %q", token), nil)
					}
				}
				flags = append(flags, token)
				index++
				continue
			}
		}
		break
	}
	if index >= len(tokens) || tokens[index] == "" {
		return restrictedCommand{}, invalidArguments("只读模式的 grep 缺少搜索模式", nil)
	}
	pattern := tokens[index]
	index++
	if index >= len(tokens) {
		return restrictedCommand{}, invalidArguments("只读模式的 grep 至少需要一个路径", nil)
	}
	resolved, err := resolveQueryPaths(scope, tokens[index:])
	if err != nil {
		return restrictedCommand{}, err
	}
	args := append([]string{}, flags...)
	args = append(args, "--", pattern)
	args = append(args, resolved...)
	return restrictedCommand{program: "grep", args: args}, nil
}

func resolveQueryPaths(scope workspaceScope, paths []string) ([]string, error) {
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
