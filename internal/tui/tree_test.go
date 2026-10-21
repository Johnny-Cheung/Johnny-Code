package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func renderMarkdown(t *testing.T, width int, source string) string {
	t.Helper()
	m := testModel(t)
	m.markdown.SetWidth(width)
	var parts []string
	if part := m.markdown.Write(source); part != "" {
		parts = append(parts, part)
	}
	if part := m.markdown.Flush(); part != "" {
		parts = append(parts, part)
	}
	return strings.Join(parts, "\n\n")
}

func testTreeSource() string {
	return "```text\n" + `backend/
├── cmd/
│   └── server/
│       └── main.go                                   384    # 服务入口：组装基础设施、路由和消费者
├── internal/
│   ├── api/                                                # HTTP 处理器层（9 文件 / 2391 行）
│   │   ├── auth_handler.go                           275
│   │   └── response.go                               240    # 统一响应封装
│   └── service/                                            # 业务服务层（14 文件 / 6488 行代码）
│       ├── auth_service.go                           219
│       └── auth_service_test.go                      449
├── docs/
│   ├── 项目系统架构.md                                 179
│   └── 010_moment_comments_auto_increment_guard.sql 5
├── go.mod                                             64
└── README.md
` + "```\n"
}

func TestFencedTreeKeepsStructureAtNarrowWidth(t *testing.T) {
	const width = 72
	plain := ansi.Strip(renderMarkdown(t, width, testTreeSource()))
	for _, want := range []string{
		"backend/", "├── cmd/", "│   └── server/", "│       └── main.go",
		"│   ├── api/", "│   │   ├── auth_handler.go", "│   └── service/",
		"│       └── auth_service_test.go", "├── docs/", "└── README.md",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("目录树缺少 %q：\n%s", want, plain)
		}
	}
	for _, want := range []string{"服务入口", "HTTP 处理器层", "统一响应封装", "业务服务层"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("目录树说明丢失 %q：\n%s", want, plain)
		}
	}
	for _, line := range strings.Split(plain, "\n") {
		if ansi.StringWidth(line) > width {
			t.Fatalf("目录树行超过终端宽度 %d：%q", width, line)
		}
	}
	// 数字列必须右对齐。
	var columns []int
	for _, line := range strings.Split(plain, "\n") {
		trimmed := strings.TrimRight(line, " ")
		for _, count := range []string{"384", "275", "240", "219", "449", "179", "64"} {
			if idx := strings.Index(trimmed, count); idx >= 0 && (idx+len(count) == len(trimmed) || trimmed[idx+len(count)] == ' ') {
				columns = append(columns, ansi.StringWidth(trimmed[:idx])+ansi.StringWidth(count))
				break
			}
		}
	}
	if len(columns) < 7 {
		t.Fatalf("应有至少 7 行带数字列：%v\n%s", columns, plain)
	}
	for _, column := range columns[1:] {
		if column != columns[0] {
			t.Fatalf("数字列未右对齐：%v\n%s", columns, plain)
		}
	}
}

func TestTreeWithLanguageUsesPlainLayout(t *testing.T) {
	source := "```bash\nbackend/\n├── main.go    10    # 入口\n└── README.md\n```\n"
	out := renderMarkdown(t, 80, source)
	if strings.Contains(out, "\x1b[48;") {
		t.Fatalf("目录树不应带代码块错误背景：%q", out)
	}
	plain := ansi.Strip(out)
	for _, want := range []string{"backend/", "├── main.go", "└── README.md", "# 入口"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("目录树缺少 %q：\n%s", want, plain)
		}
	}
}

func TestUnfencedTreeIsNotCollapsedIntoParagraph(t *testing.T) {
	source := "项目结构如下：\nbackend/\n├── cmd/\n│   └── main.go\n└── README.md\n\n完成。"
	plain := ansi.Strip(renderMarkdown(t, 80, source))
	for _, want := range []string{"项目结构如下：", "backend/", "├── cmd/", "│   └── main.go", "└── README.md", "完成。"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("无围栏目录树缺少 %q：\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "backend/ ├──") || strings.Contains(plain, "main.go └──") {
		t.Fatalf("目录树行被 Markdown 合并：\n%s", plain)
	}
}

func TestTreeKeepsOriginalLayoutWhenItFits(t *testing.T) {
	line := "│       └── main.go                                   384    # 服务入口"
	source := "```text\nbackend/\n" + line + "\n└── README.md\n```\n"
	plain := ansi.Strip(renderMarkdown(t, 120, source))
	if !strings.Contains(plain, "  "+line) {
		t.Fatalf("宽度足够时应保留原始列布局：\n%s", plain)
	}
}

func TestParseTreeRowColumns(t *testing.T) {
	row := parseTreeRow("│       └── main.go                                   384    # 服务入口")
	if !row.parsed || row.head != "│       └── main.go" || row.count != "384" || row.tail != "# 服务入口" {
		t.Fatalf("目录树行拆分错误：%+v", row)
	}
	// 长文件名把数字列挤成单个空格时也要识别行数。
	row = parseTreeRow("│       ├── 010_moment_comments_auto_increment_guard.sql 5")
	if row.head != "│       ├── 010_moment_comments_auto_increment_guard.sql" || row.count != "5" || row.tail != "" {
		t.Fatalf("长文件名行拆分错误：%+v", row)
	}
	// 名称里的单个空格不能被当作列分隔。
	row = parseTreeRow("│   ├── API 参考文档.md                                 708")
	if row.head != "│   ├── API 参考文档.md" || row.count != "708" {
		t.Fatalf("含空格名称拆分错误：%+v", row)
	}
	row = parseTreeRow("│")
	if row.parsed {
		t.Fatalf("纯树形连接线不应按条目解析：%+v", row)
	}
}

func TestTreeRenderingNeverExceedsTerminalWidth(t *testing.T) {
	for width := 40; width <= 120; width += 2 {
		plain := ansi.Strip(renderMarkdown(t, width, testTreeSource()))
		for _, line := range strings.Split(plain, "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatalf("宽度 %d 下目录树行超宽：%q", width, line)
			}
		}
		if !strings.Contains(plain, "backend/") || !strings.Contains(plain, "auth_service_test.go") {
			t.Fatalf("宽度 %d 下目录树内容丢失：\n%s", width, plain)
		}
	}
}
