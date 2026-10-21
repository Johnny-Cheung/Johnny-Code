package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// treeRow 是目录树中的一行。head 包含树形缩进和条目名，count 是行数等
// 数字列，tail 是说明或注释。把列拆开后再排版，终端较窄时仍能保持树形
// 结构，而不是把整行从中间折断。
type treeRow struct {
	raw    string
	head   string
	count  string
	tail   string
	indent int
	parsed bool
	blank  bool
}

// looksLikeTree 判断一段文本是否包含足够多的树形绘制字符，适合使用
// 保留缩进的目录树排版，而不是普通 Markdown 段落或语法高亮。
func looksLikeTree(text string) bool {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if isTreeLine(line) {
			count++
		}
	}
	return count >= 2
}

func isTreeLine(line string) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	for _, r := range line {
		switch r {
		case '├', '└', '│', '┬', '┴', '┼', '┌', '┐', '┘', '┤', '╭', '╮', '╯', '╰':
			return true
		}
	}
	trimmed := strings.TrimLeft(line, " \t")
	return strings.HasPrefix(trimmed, "|--") ||
		strings.HasPrefix(trimmed, "`--") ||
		strings.HasPrefix(trimmed, "+--")
}

func isTreeRootLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.ContainsAny(trimmed, " \t") {
		return false
	}
	if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") ||
		strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, ">") {
		return false
	}
	return strings.HasSuffix(trimmed, "/") || strings.HasSuffix(trimmed, "\\")
}

// parseTreeRow 把一行拆成树形前缀、条目名、数字列和尾部说明。目录名本身
// 可能包含单个空格，因此只在连续两个及以上空格处拆列。
func parseTreeRow(line string) treeRow {
	row := treeRow{raw: line}
	if strings.TrimSpace(line) == "" {
		row.blank = true
		return row
	}
	entryStart := treeEntryStart(line)
	if entryStart < 0 {
		return row
	}
	prefix := line[:entryStart]
	entry := line[entryStart:]
	// 目录树的长说明可能续写到下一行，行首只有树形竖线和一个 # 注释，
	// 没有条目名。把它当作注释续行，不能把整段说明算进条目名列。
	if strings.HasPrefix(entry, "#") {
		row.head = prefix
		row.indent = ansi.StringWidth(prefix)
		row.tail = strings.TrimSpace(entry)
		row.parsed = true
		return row
	}
	commentAt := commentStart(entry)
	meta := entry
	comment := ""
	if commentAt >= 0 {
		meta = entry[:commentAt]
		comment = entry[commentAt:]
	}
	fields := splitWideSpaces(strings.TrimRight(meta, " \t"))
	if len(fields) == 0 {
		return row
	}
	// 较长的文件名会把数字列挤到只剩一个空格，这时也要能识别出行数。
	if head, count, ok := splitTrailingCount(fields[0]); ok {
		fields[0] = head
		row.count = count
	}
	parenTail := ""
	if head, tail, ok := splitTrailingParen(fields[0]); ok {
		fields[0] = head
		parenTail = tail
	}
	row.head = prefix + fields[0]
	row.indent = ansi.StringWidth(prefix)
	row.parsed = true
	rest := fields[1:]
	description := ""
	if len(rest) > 0 && isDigits(rest[0]) {
		row.count = rest[0]
		rest = rest[1:]
	}
	description = strings.Join(rest, "  ")
	var tailParts []string
	if description != "" {
		tailParts = append(tailParts, description)
	}
	if parenTail != "" {
		tailParts = append(tailParts, parenTail)
	}
	if comment != "" {
		tailParts = append(tailParts, comment)
	}
	row.tail = strings.Join(tailParts, "  ")
	return row
}

func treeEntryStart(line string) int {
	i := 0
	for i < len(line) {
		r, size := utf8.DecodeRuneInString(line[i:])
		if !isTreePrefixRune(r) {
			return i
		}
		i += size
	}
	return -1
}

func isTreePrefixRune(r rune) bool {
	switch r {
	case ' ', '\t', '│', '├', '└', '─', '┬', '┴', '┼', '┌', '┐', '┘', '┤', '╭', '╮', '╯', '╰':
		return true
	}
	return false
}

func commentStart(entry string) int {
	for i := 1; i < len(entry); i++ {
		if entry[i] == '#' && (entry[i-1] == ' ' || entry[i-1] == '\t') {
			return i
		}
	}
	return -1
}

// splitWideSpaces 以连续两个及以上空格拆分列，保留单个空格作为名称或
// 说明的一部分。
func splitWideSpaces(text string) []string {
	var fields []string
	start := 0
	for i := 0; i < len(text); {
		if text[i] != ' ' && text[i] != '\t' {
			i++
			continue
		}
		j := i
		for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
			j++
		}
		if j-i >= 2 {
			if i > start {
				fields = append(fields, text[start:i])
			}
			start = j
		}
		i = j
	}
	if start < len(text) {
		fields = append(fields, text[start:])
	}
	return fields
}

func isDigits(text string) bool {
	if text == "" {
		return false
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return false
		}
	}
	return true
}

// splitTrailingCount 从 "main.go 384" 这样的条目中拆出末尾行数。文件名
// 本身以数字结尾时中间没有空格，不会被误拆。
func splitTrailingCount(text string) (string, string, bool) {
	end := len(text)
	i := end
	for i > 0 && text[i-1] >= '0' && text[i-1] <= '9' {
		i--
	}
	if i == end || i == 0 {
		return text, "", false
	}
	j := i
	for j > 0 && (text[j-1] == ' ' || text[j-1] == '\t') {
		j--
	}
	if j == i || j == 0 {
		return text, "", false
	}
	head := strings.TrimRight(text[:j], " \t")
	if head == "" {
		return text, "", false
	}
	return head, text[i:end], true
}

// splitTrailingParen 拆出长文件名后只隔一个空格的 "(5 行)"、"(87 B)"
// 等元数据，避免它们被算进条目名列导致整棵树重新排版。
func splitTrailingParen(text string) (string, string, bool) {
	trimmed := strings.TrimRight(text, " \t")
	if !strings.HasSuffix(trimmed, ")") {
		return text, "", false
	}
	index := strings.LastIndex(trimmed, " (")
	if index <= 0 {
		return text, "", false
	}
	head := strings.TrimRight(trimmed[:index], " \t")
	if head == "" {
		return text, "", false
	}
	return head, trimmed[index+1:], true
}

type treeSegment struct {
	text string
	tree bool
}

// splitTreeSegments 把普通 Markdown 块中的无围栏目录树拆出来，避免
// Markdown 把连续换行当作软换行合并成一段。至少两行树形内容才视为目录树，
// 并尽量把紧邻的根目录行（如 backend/）一起纳入。
func splitTreeSegments(source string) []treeSegment {
	lines := strings.Split(source, "\n")
	tree := make([]bool, len(lines))
	for i, line := range lines {
		tree[i] = isTreeLine(line)
	}
	for i := 0; i < len(lines); {
		if !tree[i] {
			i++
			continue
		}
		j := i
		for j < len(lines) && tree[j] {
			j++
		}
		if j-i < 2 {
			for k := i; k < j; k++ {
				tree[k] = false
			}
			i = j
			continue
		}
		if i > 0 && !tree[i-1] && isTreeRootLine(lines[i-1]) {
			tree[i-1] = true
			i--
		}
		i = j
	}
	var segments []treeSegment
	start := 0
	for i := 1; i <= len(lines); i++ {
		if i < len(lines) && tree[i] == tree[start] {
			continue
		}
		text := strings.Join(lines[start:i], "\n")
		if strings.TrimSpace(text) != "" {
			segments = append(segments, treeSegment{text: text, tree: tree[start]})
		}
		start = i
	}
	return segments
}

// renderFreeform 用于关系图、调用链等没有稳定元数据列的内容：保留每行
// 原有缩进和树形前缀，只对超宽行换行，续行对齐到条目内容起始列。
func (s *markdownStream) renderFreeform(lines []string, contentWidth int) string {
	var out []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		if ansi.StringWidth(line) <= contentWidth {
			out = append(out, "  "+line)
			continue
		}
		entryStart := treeEntryStart(line)
		if entryStart < 0 {
			entryStart = 0
		}
		gutter := line[:entryStart]
		content := line[entryStart:]
		indent := ansi.StringWidth(gutter)
		if indent == 0 {
			indent = 4
		}
		indent = min(indent, max(4, contentWidth-8))
		chunks := wrapChunks(content, max(1, contentWidth-indent))
		if len(chunks) == 0 {
			out = append(out, "  "+line)
			continue
		}
		out = append(out, "  "+gutter+chunks[0])
		for _, chunk := range chunks[1:] {
			out = append(out, "  "+strings.Repeat(" ", indent)+chunk)
		}
	}
	return clampLines(out, s.width)
}

// renderTree 按目录树语义输出代码块：窄终端下压缩数字列、把注释换行到
// 说明列下方，而不是在树形分支中间断开。宽度足够时保持模型的原始布局。
func (s *markdownStream) renderTree(code string) string {
	code = strings.TrimRight(code, "\n")
	if strings.TrimSpace(code) == "" {
		return ""
	}
	lines := strings.Split(code, "\n")
	contentWidth := max(1, s.width-2)
	fits := true
	for _, line := range lines {
		if ansi.StringWidth(line) > contentWidth {
			fits = false
			break
		}
	}
	if fits {
		var out []string
		for _, line := range lines {
			if line == "" {
				out = append(out, "")
			} else {
				out = append(out, "  "+line)
			}
		}
		return clampLines(out, s.width)
	}

	rows := make([]treeRow, len(lines))
	baseWidth, countWidth := 0, 0
	hasCount, hasTail := false, false
	structured := 0
	for i, line := range lines {
		row := parseTreeRow(line)
		rows[i] = row
		if !row.parsed {
			continue
		}
		if row.count != "" || strings.HasPrefix(row.tail, "#") || strings.HasPrefix(row.tail, "(") {
			structured++
		}
		baseWidth = max(baseWidth, ansi.StringWidth(row.head))
		if row.count != "" {
			hasCount = true
			countWidth = max(countWidth, ansi.StringWidth(row.count))
		}
		if row.tail != "" {
			hasTail = true
		}
	}
	// 关系图、调用链等自由排版的树通常只有个别括号说明，没有成列的
	// 数字或注释；这类内容按原有缩进逐行换行，不能套用表格列对齐。
	if structured < 3 {
		return s.renderFreeform(lines, contentWidth)
	}
	leftWidth := baseWidth
	if hasCount {
		leftWidth += 2 + countWidth
	}
	tailColumn := leftWidth + 2
	// 说明列至少保留一段可读空间；否则改为把说明放到条目下方。太窄时
	// 仅 "#" 就会占一行，反而更难读。
	alignedTail := hasTail && contentWidth-tailColumn >= 16

	var out []string
	for _, row := range rows {
		if row.blank {
			out = append(out, "")
			continue
		}
		if !row.parsed {
			out = append(out, "  "+ansi.Truncate(row.raw, contentWidth, "…"))
			continue
		}
		if ansi.StringWidth(row.head) > contentWidth {
			out = append(out, "  "+ansi.Truncate(row.head, contentWidth, "…"))
			for _, chunk := range wrapChunks(row.tail, max(1, contentWidth-4)) {
				out = append(out, "  "+strings.Repeat(" ", 4)+chunk)
			}
			continue
		}
		left := padDisplayRight(row.head, baseWidth)
		if hasCount {
			if row.count != "" {
				left += "  " + padDisplayLeft(row.count, countWidth)
			} else {
				left += strings.Repeat(" ", 2+countWidth)
			}
		}
		leftOverflow := ansi.StringWidth(left) > contentWidth
		if leftOverflow {
			left = ansi.Truncate(left, contentWidth, "…")
		}
		if row.tail == "" {
			out = append(out, "  "+strings.TrimRight(left, " "))
			continue
		}
		left = strings.TrimRight(left, " ")
		if alignedTail && !leftOverflow {
			chunks := wrapChunks(row.tail, contentWidth-tailColumn)
			if len(chunks) == 0 {
				out = append(out, "  "+left)
				continue
			}
			out = append(out, "  "+padDisplayRight(left, tailColumn)+chunks[0])
			continuation := tailColumn
			if strings.HasPrefix(row.tail, "# ") {
				continuation += 2
			}
			for _, chunk := range chunks[1:] {
				out = append(out, "  "+strings.Repeat(" ", continuation)+chunk)
			}
			continue
		}
		// 说明列太靠近右边界时，把说明移到树条目下方，缩进到条目名
		// 附近，避免每次只换两三个字符造成大量碎片行。
		out = append(out, "  "+left)
		indent := max(4, row.indent)
		indent = min(indent, max(4, contentWidth-12))
		for _, chunk := range wrapChunks(row.tail, max(1, contentWidth-indent)) {
			out = append(out, "  "+strings.Repeat(" ", indent)+chunk)
		}
	}
	return clampLines(out, s.width)
}

func clampLines(lines []string, width int) string {
	for i, line := range lines {
		if ansi.StringWidth(line) > width {
			lines[i] = ansi.Truncate(line, width, "…")
		}
	}
	return strings.Join(lines, "\n")
}

func wrapChunks(text string, width int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	// 注释的 "#" 单独作为一个词时，换行器会先把 "#" 留在行尾再把整句
	// 推到下一行；把标记和正文作为一个整体排版。
	prefix := ""
	if strings.HasPrefix(text, "# ") {
		prefix = "# "
		text = strings.TrimSpace(text[2:])
	}
	limit := max(1, width-ansi.StringWidth(prefix))
	wrapped := ansi.Wrap(text, limit, "/.,;:")
	var chunks []string
	for _, line := range strings.Split(wrapped, "\n") {
		line = strings.TrimRight(line, " ")
		if line == "" {
			continue
		}
		if ansi.StringWidth(line) <= limit {
			chunks = append(chunks, line)
			continue
		}
		// 长路径或中文串没有可用断点时，按显示宽度硬换行，宁可断开
		// 单词也不能让上层按窗口截断，导致说明内容丢失。
		for _, hard := range strings.Split(ansi.Hardwrap(line, limit, false), "\n") {
			if hard = strings.TrimRight(hard, " "); hard != "" {
				chunks = append(chunks, hard)
			}
		}
	}
	if prefix != "" && len(chunks) > 0 {
		chunks[0] = prefix + chunks[0]
	}
	return chunks
}

func padDisplayRight(text string, width int) string {
	if pad := width - ansi.StringWidth(text); pad > 0 {
		return text + strings.Repeat(" ", pad)
	}
	return text
}

func padDisplayLeft(text string, width int) string {
	if pad := width - ansi.StringWidth(text); pad > 0 {
		return strings.Repeat(" ", pad) + text
	}
	return text
}
