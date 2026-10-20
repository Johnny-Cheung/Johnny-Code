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
	inFence     bool
	fenceChar   byte
	fenceLen    int
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
	s.inFence = false
	s.fenceChar = 0
	s.fenceLen = 0
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
	if s.line.Len() > 0 {
		line := s.line.String()
		s.line.Reset()
		if s.inFence && isFenceClose(line, s.fenceChar, s.fenceLen) {
			s.inFence = false
		}
		s.block.WriteString(line)
	}
	return s.renderBlock()
}

func (s *markdownStream) consume(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	char, length, isFence, isClose := fenceInfo(trimmed)
	if s.inFence {
		s.block.WriteString(line)
		if isFence && char == s.fenceChar && length >= s.fenceLen && isClose {
			s.inFence = false
			return s.renderBlock()
		}
		return ""
	}
	if isFence {
		s.inFence, s.fenceChar, s.fenceLen = true, char, length
		s.block.WriteString(line)
		return ""
	}
	if strings.TrimSpace(line) == "" {
		return s.renderBlock()
	}
	s.block.WriteString(line)
	return ""
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
		style := headingStyle(styles.DarkStyleConfig)
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

func headingStyle(style glamouransi.StyleConfig) glamouransi.StyleConfig {
	// 标题已有颜色和加粗样式，去掉 Markdown 的 # 前缀。
	style.H2.Prefix, style.H3.Prefix = "", ""
	style.H4.Prefix, style.H5.Prefix, style.H6.Prefix = "", "", ""
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
