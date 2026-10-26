package memory

import (
	"strings"
	"testing"
	"time"
)

func TestItemMarkdownRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	item := Item{
		Name:        "go-test-before-commit",
		Description: "提交前运行 go test ./...",
		Type:        TypeProject,
		Scope:       ScopeProject,
		Project:     "johnny-code-test",
		Created:     now,
		Updated:     now,
		Source:      "session-test",
		Confidence:  ConfidenceHigh,
		Body:        "## 事实\n\n提交前运行测试。",
	}
	data, err := item.Markdown()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseItem("/tmp/go-test-before-commit.md", data)
	if err != nil {
		t.Fatalf("解析失败：%v\n%s", err, data)
	}
	if parsed.Name != item.Name || parsed.Description != item.Description || parsed.Type != item.Type || parsed.Scope != item.Scope || parsed.Project != item.Project || parsed.Confidence != item.Confidence {
		t.Fatalf("往返字段不一致：%+v", parsed)
	}
	if !strings.Contains(parsed.Body, "提交前运行测试") {
		t.Fatalf("正文丢失：%q", parsed.Body)
	}
	if !parsed.Created.Equal(now) || !parsed.Updated.Equal(now) {
		t.Fatalf("时间不一致：%v %v", parsed.Created, parsed.Updated)
	}
}

func TestItemValidation(t *testing.T) {
	base := Item{
		Name:        "valid-name",
		Description: "一行描述",
		Type:        TypeProject,
		Scope:       ScopeProject,
		Project:     "project",
		Confidence:  ConfidenceHigh,
		Body:        "正文",
	}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Item){
		"名称":   func(item *Item) { item.Name = "Bad Name" },
		"描述换行": func(item *Item) { item.Description = "a\nb" },
		"描述过长": func(item *Item) { item.Description = strings.Repeat("中", MaxDescriptionTokens+1) },
		"类型":   func(item *Item) { item.Type = "other" },
		"作用域":  func(item *Item) { item.Scope = "other" },
		"项目缺失": func(item *Item) { item.Project = "" },
		"置信度":  func(item *Item) { item.Confidence = "certain" },
		"正文为空": func(item *Item) { item.Body = " " },
		"正文过长": func(item *Item) { item.Body = strings.Repeat("x", MaxBodyBytes+1) },
		"密钥":   func(item *Item) { item.Body = "JOHNNY_API_KEY=secret-value" },
	} {
		t.Run(name, func(t *testing.T) {
			item := base
			mutate(&item)
			if err := item.Validate(); err == nil {
				t.Fatalf("应拒绝：%+v", item)
			}
		})
	}
}

func TestParseItemRequiresFrontmatter(t *testing.T) {
	if _, err := ParseItem("/tmp/x.md", []byte("没有 frontmatter")); err == nil {
		t.Fatal("缺少 frontmatter 应报错")
	}
}
