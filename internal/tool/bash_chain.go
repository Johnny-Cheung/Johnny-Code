package tool

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const maxCommandChainSegments = 8

var errUnclaimedBashChain = errors.New("命令包含当前分段执行器未覆盖的 shell 语法")

type bashChainSegment struct {
	text   string
	tokens []string
}

type bashChain struct {
	command  string
	segments []bashChainSegment
}

// parseBashChain 只认默认模式可以安全转换为多个直接 argv 调用的 && 链。
// 返回 false 表示交给原有单命令/审批路径处理，不代表命令本身无效。
func parseBashChain(command string) (bashChain, bool) {
	parts, ok := splitBashChain(command)
	if !ok || len(parts) < 2 || len(parts) > maxCommandChainSegments {
		return bashChain{}, false
	}
	chain := bashChain{command: command, segments: make([]bashChainSegment, 0, len(parts))}
	for index, part := range parts {
		tokens, err := tokenizeRestricted(part)
		if err != nil || len(tokens) == 0 || unsupportedChainExpansion(part) {
			return bashChain{}, false
		}
		if tokens[0] == "cd" {
			if index != 0 || len(tokens) != 2 || strings.HasPrefix(tokens[1], "-") {
				return bashChain{}, false
			}
		} else if requiresShell(tokens) {
			return bashChain{}, false
		}
		chain.segments = append(chain.segments, bashChainSegment{text: part, tokens: tokens})
	}
	return chain, true
}

// splitBashChain 只拆分未引用的 &&。其他 shell 操作符由调用方的旧
// 审批路径处理，避免新的分段执行器意外改变 shell 语义。
func splitBashChain(command string) ([]string, bool) {
	if strings.TrimSpace(command) == "" || !utf8.ValidString(command) {
		return nil, false
	}
	parts := make([]string, 0, 2)
	start := 0
	inSingle, inDouble, escaped := false, false, false
	for i := 0; i < len(command); {
		r := command[i]
		if escaped {
			escaped = false
			i++
			continue
		}
		if inSingle {
			if r == '\'' {
				inSingle = false
			}
			i++
			continue
		}
		if inDouble {
			switch r {
			case '\\':
				escaped = true
			case '"':
				inDouble = false
			case '\n', '\r':
				return nil, false
			}
			i++
			continue
		}
		switch r {
		case '\'', '"':
			if r == '\'' {
				inSingle = true
			} else {
				inDouble = true
			}
			i++
		case '\\':
			escaped = true
			i++
		case '&':
			if i+1 >= len(command) || command[i+1] != '&' {
				return nil, false
			}
			part := strings.TrimSpace(command[start:i])
			if part == "" {
				return nil, false
			}
			parts = append(parts, part)
			i += 2
			start = i
		case '|', ';', '<', '>', '$', '`', '(', ')', '\n', '\r':
			return nil, false
		default:
			i++
		}
	}
	if escaped || inSingle || inDouble {
		return nil, false
	}
	part := strings.TrimSpace(command[start:])
	if part == "" {
		return nil, false
	}
	parts = append(parts, part)
	return parts, true
}

// unsupportedChainExpansion 过滤直接 exec 与 shell 之间会产生不同含义的
// 未引用展开。引号中的 glob 仍作为普通参数交给 find/grep 等程序处理。
func unsupportedChainExpansion(command string) bool {
	inSingle, inDouble, escaped := false, false, false
	tokenStart := true
	for i := 0; i < len(command); i++ {
		r := command[i]
		if escaped {
			escaped = false
			tokenStart = false
			continue
		}
		if inSingle {
			if r == '\'' {
				inSingle = false
			}
			continue
		}
		if inDouble {
			if r == '\\' {
				escaped = true
			} else if r == '"' {
				inDouble = false
			}
			continue
		}
		switch r {
		case '\'', '"':
			if r == '\'' {
				inSingle = true
			} else {
				inDouble = true
			}
			tokenStart = false
		case '\\':
			escaped = true
			tokenStart = false
		case ' ', '\t':
			tokenStart = true
		case '~':
			if tokenStart {
				return true
			}
			tokenStart = false
		case '*', '?', '[', ']':
			return true
		case '#':
			if tokenStart {
				return true
			}
			tokenStart = false
		default:
			tokenStart = false
		}
	}
	return escaped || inSingle || inDouble
}

type chainPathResolver struct {
	scope *multiPathScope
	base  string
}

func (r chainPathResolver) resolve(path string) (string, error) {
	return r.scope.resolveFrom(r.base, path)
}

func (r chainPathResolver) resolveExisting(path string) (string, error) {
	return r.scope.resolveExistingFrom(r.base, path)
}
