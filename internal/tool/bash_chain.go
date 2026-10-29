package tool

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxCommandChainSegments = 8

var (
	errShellNeedsShell = errors.New("命令包含需要 shell 语义的语法")
	errShellSyntax     = errors.New("命令包含不支持的 shell 语法")
	// errShellGlob 单独标记未引用的通配符：默认模式直接拒绝，不再回退
	// 到完整 shell 审批，避免每次换参数都重新询问。
	errShellGlob = errors.New("命令包含未引用的通配符")
)

// shellSyntaxError 说明命令为什么不能直接转换成 argv。needsShell 为真时，
// 默认模式回退到一次完整 shell 审批；只读模式一律拒绝。glob 为真时默认
// 模式也直接拒绝，并提示改用 find -name。
type shellSyntaxError struct {
	message    string
	needsShell bool
	glob       bool
}

func (e *shellSyntaxError) Error() string { return e.message }

func (e *shellSyntaxError) Unwrap() error {
	if e.glob {
		return errShellGlob
	}
	if e.needsShell {
		return errShellNeedsShell
	}
	return errShellSyntax
}

func shellNeedsShell(format string, args ...any) error {
	return &shellSyntaxError{message: fmt.Sprintf(format, args...), needsShell: true}
}

func shellGlob(format string, args ...any) error {
	return &shellSyntaxError{message: fmt.Sprintf(format, args...), needsShell: true, glob: true}
}

func shellUnsupported(format string, args ...any) error {
	return &shellSyntaxError{message: fmt.Sprintf(format, args...)}
}

// bashRedirect 只表示已经确认安全的尾部文件描述符重定向。target 为 "&1"
// 表示复制 stdout，"/dev/null" 表示丢弃。
type bashRedirect struct {
	fd     int
	target string
}

// String 返回规范化的重定向写法，供审批展示和会话授权保存使用。
func (r bashRedirect) String() string {
	switch r.target {
	case "&1":
		return "2>&1"
	case "/dev/null":
		if r.fd == 2 {
			return "2>/dev/null"
		}
		return ">/dev/null"
	default:
		return ""
	}
}

// bashChainSegment 是管道绑定更紧之后的一条简单命令。operator 是它前面的
// 操作符（&&、||、; 或管道 |）；pipe 表示它读取上一条命令的标准输出。
type bashChainSegment struct {
	text      string
	tokens    []string
	operator  string
	pipe      bool
	redirects []bashRedirect
}

type bashPipeline struct {
	operator string
	start    int
	end      int
}

type bashChain struct {
	command   string
	segments  []bashChainSegment
	pipelines []bashPipeline
}

type shellPart struct {
	text     string
	operator string
}

// parseShellCommand 是默认模式和只读模式共用的分段器。它只认领可以安全
// 转换为直接 argv 调用的简单命令、管道、&&/||/; 链和尾部 fd 重定向。
// 返回错误表示该命令需要 shell 语义或包含不支持的语法。
func parseShellCommand(command string) (bashChain, error) {
	parts, err := splitShellParts(command)
	if err != nil {
		return bashChain{}, err
	}
	if len(parts) > maxCommandChainSegments {
		return bashChain{}, shellUnsupported("命令共 %d 段，超过 %d 段上限；请拆成多条 bash 调用", len(parts), maxCommandChainSegments)
	}
	chain := bashChain{command: command}
	pipelineStart := -1
	pipelineOperator := ""
	closePipeline := func(end int) {
		if pipelineStart < 0 {
			return
		}
		chain.pipelines = append(chain.pipelines, bashPipeline{
			operator: pipelineOperator,
			start:    pipelineStart,
			end:      end,
		})
		pipelineStart = -1
	}
	for _, part := range parts {
		if part.operator != "|" {
			closePipeline(len(chain.segments))
			pipelineOperator = part.operator
			pipelineStart = len(chain.segments)
		}
		scan, err := scanShellSegment(part.text)
		if err != nil {
			return bashChain{}, err
		}
		chain.segments = append(chain.segments, bashChainSegment{
			text:      part.text,
			tokens:    scan.tokens,
			operator:  part.operator,
			pipe:      part.operator == "|",
			redirects: scan.redirects,
		})
	}
	closePipeline(len(chain.segments))

	if err := validateShellChain(chain); err != nil {
		return bashChain{}, err
	}
	return chain, nil
}

// validateShellChain 应用两条全局规则：cd 只能作为第一条命令单独出现，
// 且其余段不能是 shell 内建或变量赋值。
func validateShellChain(chain bashChain) error {
	for index, segment := range chain.segments {
		if segment.tokens[0] != "cd" {
			continue
		}
		if index != 0 || segment.pipe || len(segment.tokens) != 2 ||
			strings.HasPrefix(segment.tokens[1], "-") || len(segment.redirects) > 0 {
			return shellNeedsShell("cd 只支持作为第一条命令并带一个目录参数")
		}
		if index+1 < len(chain.segments) {
			next := chain.segments[index+1].operator
			if next == "|" || next == "||" {
				return shellNeedsShell("cd 不能参与管道或紧邻 ||")
			}
		}
	}
	for _, segment := range chain.segments {
		if segment.tokens[0] == "cd" {
			continue
		}
		if requiresShell(segment.tokens) || shellReservedWords[segment.tokens[0]] {
			return shellNeedsShell("命令 %q 需要 shell 内建或赋值语义", segment.tokens[0])
		}
	}
	return nil
}

// splitShellParts 按未引用的 &&、||、; 和 | 拆分。重定向里的 >& 不当作
// 后台操作符；其余单个 & 留给完整 shell 审批。
func splitShellParts(command string) ([]shellPart, error) {
	if strings.TrimSpace(command) == "" || !utf8.ValidString(command) {
		return nil, shellUnsupported("命令不能为空或包含无效 UTF-8")
	}
	parts := make([]shellPart, 0, 2)
	start := 0
	pending := ""
	addPart := func(end int, operator string, width int) error {
		text := strings.TrimSpace(command[start:end])
		if text == "" {
			return shellUnsupported("命令包含空分段")
		}
		parts = append(parts, shellPart{text: text, operator: pending})
		pending = operator
		start = end + width
		return nil
	}
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
				return nil, shellUnsupported("不支持多行命令")
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
			if i+1 < len(command) && command[i+1] == '&' {
				if err := addPart(i, "&&", 2); err != nil {
					return nil, err
				}
				i += 2
				continue
			}
			if i > 0 && command[i-1] == '>' {
				// 2>&1 的重定向内容。
				i++
				continue
			}
			return nil, shellNeedsShell("不支持后台执行或单独的 &")
		case '|':
			if i+1 < len(command) && command[i+1] == '|' {
				if err := addPart(i, "||", 2); err != nil {
					return nil, err
				}
				i += 2
				continue
			}
			if i+1 < len(command) && command[i+1] == '&' {
				return nil, shellUnsupported("不支持 |& 合并标准错误")
			}
			if err := addPart(i, "|", 1); err != nil {
				return nil, err
			}
			i++
		case ';':
			if err := addPart(i, ";", 1); err != nil {
				return nil, err
			}
			i++
		case '\n', '\r':
			return nil, shellUnsupported("不支持多行命令")
		default:
			i++
		}
	}
	if escaped || inSingle || inDouble {
		return nil, shellUnsupported("命令存在未完成的转义或引号")
	}
	text := strings.TrimSpace(command[start:])
	if text == "" {
		return nil, shellUnsupported("命令包含空分段")
	}
	parts = append(parts, shellPart{text: text, operator: pending})
	return parts, nil
}

type shellSegmentScan struct {
	tokens    []string
	redirects []bashRedirect
}

// scanShellSegment 把一个命令段拆成 argv 和尾部重定向。引号内和转义后的
// 元字符按字面量保留；未引用的 glob、~、变量、命令替换和普通文件重定向
// 都回退到完整 shell 审批。
func scanShellSegment(text string) (shellSegmentScan, error) {
	var scan shellSegmentScan
	var current strings.Builder
	started := false
	// wordQuoted 标记当前词包含引号或转义，此时数字不能再当作 fd 前缀。
	wordQuoted := false
	redirects := false
	flush := func() {
		if started {
			scan.tokens = append(scan.tokens, current.String())
			current.Reset()
			started = false
			wordQuoted = false
		}
	}
	inSingle, inDouble, escaped := false, false, false
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == utf8.RuneError && size == 1 {
			return scan, shellUnsupported("命令包含无效 UTF-8")
		}
		if r == 0 {
			return scan, shellUnsupported("命令包含 NUL")
		}
		if escaped {
			escaped = false
			if r == '\n' || r == '\r' {
				return scan, shellUnsupported("不支持多行命令")
			}
			if inDouble && r != '"' && r != '\\' && r != '$' && r != '`' {
				// 双引号内未识别的反斜杠保留，避免破坏 \d 等正则。
				current.WriteByte('\\')
			}
			current.WriteRune(r)
			started = true
			i += size
			continue
		}
		if inSingle {
			if r == '\'' {
				inSingle = false
			} else {
				if r == '\n' || r == '\r' {
					return scan, shellUnsupported("不支持多行命令")
				}
				current.WriteRune(r)
			}
			started = true
			i += size
			continue
		}
		if inDouble {
			switch r {
			case '"':
				inDouble = false
			case '\\':
				escaped = true
			case '\n', '\r':
				return scan, shellUnsupported("不支持多行命令")
			case '`':
				return scan, shellNeedsShell("不支持反引号命令替换")
			case '$':
				if next, ok := nextRune(text[i+size:]); ok && dollarStartsExpansion(next) {
					return scan, shellNeedsShell("不支持引号内的变量或命令替换")
				}
				current.WriteRune(r)
			default:
				current.WriteRune(r)
			}
			started = true
			i += size
			continue
		}
		if redirects {
			switch {
			case r == ' ' || r == '\t':
				i += size
				continue
			case r >= '0' && r <= '9':
				j := i
				for j < len(text) && text[j] >= '0' && text[j] <= '9' {
					j++
				}
				if j >= len(text) || text[j] != '>' {
					return scan, shellUnsupported("重定向只能位于命令末尾")
				}
				if text[i:j] != "2" {
					return scan, shellUnsupported("只支持 fd 2 的尾部重定向")
				}
				redirect, next, err := parseShellRedirect(text, j, 2)
				if err != nil {
					return scan, err
				}
				scan.redirects = append(scan.redirects, redirect)
				i = next
				continue
			case r == '>':
				redirect, next, err := parseShellRedirect(text, i, 1)
				if err != nil {
					return scan, err
				}
				scan.redirects = append(scan.redirects, redirect)
				i = next
				continue
			default:
				return scan, shellUnsupported("重定向只能位于命令末尾")
			}
		}
		switch r {
		case ' ', '\t':
			flush()
			i += size
		case '\n', '\r':
			return scan, shellUnsupported("不支持多行命令")
		case '\'':
			inSingle = true
			started = true
			wordQuoted = true
			i += size
		case '"':
			inDouble = true
			started = true
			wordQuoted = true
			i += size
		case '\\':
			escaped = true
			started = true
			wordQuoted = true
			i += size
		case '|', ';', '&':
			return scan, shellUnsupported("命令段包含未拆分的 shell 操作符 %q", r)
		case '<':
			return scan, shellNeedsShell("不支持输入重定向")
		case '>':
			fd := 1
			if started && !wordQuoted && isDigits(current.String()) {
				number, err := strconv.Atoi(current.String())
				if err != nil || number != 2 {
					return scan, shellUnsupported("只支持 fd 2 的尾部重定向")
				}
				fd = number
				current.Reset()
				started = false
			} else {
				flush()
			}
			redirect, next, err := parseShellRedirect(text, i, fd)
			if err != nil {
				return scan, err
			}
			scan.redirects = append(scan.redirects, redirect)
			redirects = true
			i = next
		case '$':
			return scan, shellNeedsShell("不支持未引用的变量或命令替换")
		case '`':
			return scan, shellNeedsShell("不支持反引号命令替换")
		case '(', ')':
			return scan, shellNeedsShell("不支持子 shell 或分组语法")
		case '{', '}':
			return scan, shellNeedsShell("不支持未引用的花括号展开")
		case '*', '?', '[', ']':
			return scan, shellGlob("不支持未引用的通配符 %q；受控执行不展开通配符。查找文件请用 find -name '模式' 或逐个列出路径；作为选项值时请加引号，例如 --include='*.php'", string(r))
		case '~':
			if !started {
				return scan, shellNeedsShell("不支持未引用的 ~ 展开")
			}
			current.WriteRune(r)
			started = true
			i += size
		case '#':
			if !started {
				return scan, shellNeedsShell("不支持注释语法")
			}
			current.WriteRune(r)
			started = true
			i += size
		default:
			current.WriteRune(r)
			started = true
			i += size
		}
	}
	if escaped {
		return scan, shellUnsupported("命令存在未完成的转义")
	}
	if inSingle || inDouble {
		return scan, shellUnsupported("命令存在未闭合的引号")
	}
	flush()
	if len(scan.tokens) == 0 || scan.tokens[0] == "" {
		return scan, shellUnsupported("命令段不能为空")
	}
	return scan, nil
}

// parseShellRedirect 解析 >/dev/null、2>/dev/null 和 2>&1。index 指向 '>'。
func parseShellRedirect(text string, index, fd int) (bashRedirect, int, error) {
	j := index + 1
	if j < len(text) && text[j] == '>' {
		return bashRedirect{}, j, shellUnsupported("不支持 >> 追加重定向")
	}
	if j < len(text) && text[j] == '&' {
		if fd != 2 {
			return bashRedirect{}, j, shellUnsupported("只支持 2>&1")
		}
		j++
		for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
			j++
		}
		if j >= len(text) || text[j] != '1' {
			return bashRedirect{}, j, shellUnsupported("只支持 2>&1")
		}
		j++
		if j < len(text) && !isRedirectBoundary(text[j]) {
			return bashRedirect{}, j, shellUnsupported("2>&1 之后不能再有参数")
		}
		return bashRedirect{fd: 2, target: "&1"}, j, nil
	}
	for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
		j++
	}
	start := j
	for j < len(text) && text[j] != ' ' && text[j] != '\t' && text[j] != '>' {
		j++
	}
	target := text[start:j]
	if target != "/dev/null" {
		return bashRedirect{}, j, shellUnsupported("只支持重定向到 /dev/null")
	}
	return bashRedirect{fd: fd, target: "/dev/null"}, j, nil
}

func isRedirectBoundary(r byte) bool {
	return r == ' ' || r == '\t' || r == '>'
}

func nextRune(text string) (rune, bool) {
	if text == "" {
		return 0, false
	}
	r, _ := utf8.DecodeRuneInString(text)
	return r, true
}

// dollarStartsExpansion 判断双引号内的 $ 是否会被 shell 展开。单独的 $
// （例如正则锚点 "end$"）保留为字面量。
func dollarStartsExpansion(next rune) bool {
	switch next {
	case '(', '{', '`', '$', '@', '*', '#', '?', '!', '-', '_':
		return true
	}
	return unicode.IsLetter(next) || unicode.IsDigit(next)
}

// shellReservedWords 是 shell 关键字。它们必须回退到完整 shell 审批，不能
// 当作普通程序直接执行。
var shellReservedWords = map[string]bool{
	"if": true, "then": true, "elif": true, "else": true, "fi": true,
	"for": true, "while": true, "until": true, "do": true, "done": true,
	"case": true, "esac": true, "select": true, "function": true,
	"time": true, "coproc": true, "in": true, "{": true, "}": true,
	"!": true, "[[": true, "]]": true, "((": true, "))": true,
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
