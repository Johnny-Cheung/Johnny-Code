package tui

import (
	"strings"

	"charm.land/glamour/v2"
	glamouransi "charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"github.com/charmbracelet/x/ansi"
)

// markdownStream 把流式文本按 Markdown 块提交。完整块交给 glamour 渲染，
// 未完成块只作为动态区域的原文预览，避免在滚动历史里重复或重排内容。
type markdownStream struct {
	block       strings.Builder
	line        strings.Builder
	code        strings.Builder
	inFence     bool
	fenceChar   byte
	fenceLen    int
	fenceLang   string
	width       int
	renderer    *glamour.TermRenderer
	renderWidth int
}

func (s *markdownStream) SetWidth(width int) {
	s.width = max(1, width)
}

func (s *markdownStream) Reset() {
	s.block.Reset()
	s.line.Reset()
	s.code.Reset()
	s.inFence = false
	s.fenceChar = 0
	s.fenceLen = 0
	s.fenceLang = ""
}

// Write 接收一段正文，返回已经完成的 Markdown 块（终端样式文本）。
func (s *markdownStream) Write(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = safeText(text)
	var output strings.Builder
	for _, r := range text {
		s.line.WriteRune(r)
		if r != '\n' {
			continue
		}
		line := s.line.String()
		s.line.Reset()
		if rendered := s.consume(line); rendered != "" {
			if output.Len() > 0 {
				output.WriteString("\n\n")
			}
			output.WriteString(rendered)
		}
	}
	return output.String()
}

// Preview 返回尚未提交的原文，供动态区域显示流式进度。
func (s *markdownStream) Preview() string {
	return s.block.String() + s.line.String()
}

// Flush 提交最后一行和当前块。即使响应被取消，已经收到的文字仍会保留。
func (s *markdownStream) Flush() string {
	var parts []string
	if s.inFence {
		if s.line.Len() > 0 {
			s.code.WriteString(s.line.String())
			s.line.Reset()
		}
		if rendered := s.renderCode(s.code.String(), s.fenceLang); rendered != "" {
			parts = append(parts, rendered)
		}
		s.code.Reset()
		s.inFence = false
		s.fenceLang = ""
	} else if s.line.Len() > 0 {
		s.block.WriteString(s.line.String())
		s.line.Reset()
	}
	if rendered := s.renderBlock(); rendered != "" {
		parts = append(parts, rendered)
	}
	return strings.Join(parts, "\n\n")
}

func (s *markdownStream) consume(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	char, length, isFence, isClose := fenceInfo(trimmed)
	if s.inFence {
		if isFence && char == s.fenceChar && length >= s.fenceLen && isClose {
			code, lang := s.code.String(), s.fenceLang
			s.code.Reset()
			s.inFence = false
			s.fenceLang = ""
			return s.renderCode(code, lang)
		}
		s.code.WriteString(line)
		return ""
	}
	if isFence {
		// 代码块独立渲染；先提交它前面的普通 Markdown 文本。
		previous := s.renderBlock()
		s.inFence, s.fenceChar, s.fenceLen = true, char, length
		s.fenceLang = fenceLanguage(trimmed, length)
		s.code.Reset()
		return previous
	}
	if strings.TrimSpace(line) == "" {
		return s.renderBlock()
	}
	s.block.WriteString(line)
	return ""
}

func fenceLanguage(trimmed string, markerLength int) string {
	rest := strings.TrimSpace(trimmed[markerLength:])
	if rest == "" {
		return ""
	}
	field := strings.Fields(rest)[0]
	return strings.ToLower(strings.Trim(field, "{}"))
}

func plainCodeLanguage(lang string) bool {
	switch lang {
	case "", "text", "plain", "plaintext", "txt", "tree":
		return true
	default:
		return false
	}
}

// renderCode 对未标注或纯文本代码块使用无高亮的等宽输出，避免词法分析器把
// 目录树、中文说明等标记为错误；其余语言继续使用 Markdown 语法高亮。
func (s *markdownStream) renderCode(code, lang string) string {
	code = strings.TrimRight(code, "\n")
	if strings.TrimSpace(code) == "" {
		return ""
	}
	if plainCodeLanguage(lang) {
		return s.renderPlainCode(code)
	}
	source := "```" + lang + "\n" + code + "\n```\n"
	rendered, err := s.render(source)
	if err != nil {
		return s.renderPlainCode(code)
	}
	return rendered
}

func (s *markdownStream) renderPlainCode(code string) string {
	var lines []string
	for _, raw := range strings.Split(code, "\n") {
		raw = strings.TrimRight(raw, "\r")
		if raw == "" {
			lines = append(lines, "")
			continue
		}
		// 为续行预留 4 个缩进列，确保每一行都不超过终端宽度。
		available := max(1, s.width-4)
		if ansi.StringWidth(raw) > available {
			// 中文顿号/逗号等不是 ANSI 换行器认可的断点；只在需要换行时
			// 在标点后补充空格，避免把 settings 这类英文词从中间切断。
			raw = strings.NewReplacer("、", "、 ", "，", "， ", "；", "； ", "：", "： ").Replace(raw)
		}
		wrapped := strings.Split(ansi.Wrap(raw, available, ""), "\n")
		for i, line := range wrapped {
			if i == 0 {
				lines = append(lines, "  "+line)
			} else {
				lines = append(lines, "    "+line)
			}
		}
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

func fenceInfo(trimmed string) (char byte, length int, ok, closing bool) {
	if len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0, false, false
	}
	char = trimmed[0]
	for length < len(trimmed) && trimmed[length] == char {
		length++
	}
	if length < 3 {
		return 0, 0, false, false
	}
	return char, length, true, strings.TrimSpace(trimmed[length:]) == ""
}

func isFenceClose(line string, char byte, minLength int) bool {
	c, length, ok, closing := fenceInfo(strings.TrimLeft(line, " \t"))
	return ok && closing && c == char && length >= minLength
}

func (s *markdownStream) renderBlock() string {
	source := strings.TrimRight(s.block.String(), "\n")
	s.block.Reset()
	if strings.TrimSpace(source) == "" {
		return ""
	}
	rendered, err := s.render(source)
	if err != nil {
		// 渲染失败时退化为安全原文，正文不能因此丢失。
		return wrap(strings.TrimRight(source, "\n"), s.width)
	}
	return rendered + ansi.ResetStyle
}

func (s *markdownStream) render(source string) (string, error) {
	if s.renderer == nil || s.renderWidth != s.width {
		style := markdownStyle(styles.DarkStyleConfig)
		renderer, err := glamour.NewTermRenderer(
			glamour.WithStyles(style),
			glamour.WithWordWrap(max(10, s.width-4)),
		)
		if err != nil {
			return "", err
		}
		s.renderer, s.renderWidth = renderer, s.width
	}
	rendered, err := s.renderer.Render(source)
	if err != nil {
		return "", err
	}
	rendered = trimBlankLines(rendered)
	if rendered == "" {
		return "", nil
	}
	return wrap(rendered, s.width), nil
}

func markdownStyle(style glamouransi.StyleConfig) glamouransi.StyleConfig {
	// 标题已有颜色和加粗样式，去掉 Markdown 的 # 前缀。
	style.H2.Prefix, style.H3.Prefix = "", ""
	style.H4.Prefix, style.H5.Prefix, style.H6.Prefix = "", "", ""
	// 未标注语言的代码块会被词法分析器归为错误 token；默认样式会给它们
	// 加红色背景。保留错误分类但改为普通前景色，避免整块目录树变红。
	if style.CodeBlock.Chroma != nil {
		neutral := "#C4C4C4"
		style.CodeBlock.Chroma.Error = glamouransi.StylePrimitive{Color: &neutral}
	}
	return style
}

func trimBlankLines(text string) string {
	lines := strings.Split(text, "\n")
	blank := func(line string) bool { return strings.TrimSpace(ansi.Strip(line)) == "" }
	start, end := 0, len(lines)
	for start < end && blank(lines[start]) {
		start++
	}
	for end > start && blank(lines[end-1]) {
		end--
	}
	return strings.Join(lines[start:end], "\n")
}
