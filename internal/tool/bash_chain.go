package tool

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const maxCommandChainSegments = 8

var errUnclaimedBashChain = errors.New("命令包含当前分段执行器未覆盖的 shell 语法")

type bashChainSegment struct {
	text     string
	tokens   []string
	operator string
}

type bashChain struct {
	command  string
	segments []bashChainSegment
}

// parseBashChain 只认默认模式可以安全转换为多个直接 argv 调用的 && 链。
// 返回 false 表示交给原有单命令/审批路径处理，不代表命令本身无效。
func parseBashChain(command string) (bashChain, bool) {
	parts, operators, ok := splitBashChain(command)
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
		operator := ""
		if index > 0 {
			operator = operators[index-1]
		}
		chain.segments = append(chain.segments, bashChainSegment{text: part, tokens: tokens, operator: operator})
	}
	if chain.segments[0].tokens[0] == "cd" && operators[0] == "||" {
		return bashChain{}, false
	}
	return chain, true
}

// splitBashChain 拆分未引用的 &&、|| 和 ;。单独的 | 仍留给
// 只读管道解析器，默认模式不认领管道。
func splitBashChain(command string) ([]string, []string, bool) {
	if strings.TrimSpace(command) == "" || !utf8.ValidString(command) {
		return nil, nil, false
	}
	parts := make([]string, 0, 2)
	operators := make([]string, 0, 1)
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
				return nil, nil, false
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
				return nil, nil, false
			}
			if !appendChainPart(&parts, &operators, command[start:i], "&&") {
				return nil, nil, false
			}
			i += 2
			start = i
		case '|':
			if i+1 < len(command) && command[i+1] == '|' {
				if !appendChainPart(&parts, &operators, command[start:i], "||") {
					return nil, nil, false
				}
				i += 2
				start = i
				continue
			}
			return nil, nil, false
		case ';':
			if !appendChainPart(&parts, &operators, command[start:i], ";") {
				return nil, nil, false
			}
			i++
			start = i
		case '<', '>', '$', '`', '(', ')', '\n', '\r':
			return nil, nil, false
		default:
			i++
		}
	}
	if escaped || inSingle || inDouble {
		return nil, nil, false
	}
	part := strings.TrimSpace(command[start:])
	if part == "" {
		return nil, nil, false
	}
	parts = append(parts, part)
	if len(operators) != len(parts)-1 {
		return nil, nil, false
	}
	return parts, operators, true
}

func appendChainPart(parts *[]string, operators *[]string, raw, operator string) bool {
	part := strings.TrimSpace(raw)
	if part == "" {
		return false
	}
	*parts = append(*parts, part)
	*operators = append(*operators, operator)
	return true
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
