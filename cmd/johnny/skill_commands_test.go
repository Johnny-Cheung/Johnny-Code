package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/skill"
)

func writeCommandSkill(t *testing.T, skillsDir, name, description, body string) string {
	t.Helper()
	dir := filepath.Join(skillsDir, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func testCommandSkillStore(t *testing.T) (*skill.Store, string) {
	t.Helper()
	work, home := t.TempDir(), t.TempDir()
	dir := filepath.Join(work, ".johnny", "skills")
	writeCommandSkill(t, dir, "go-test", "运行 Go 测试时使用。", "执行 $ARGUMENTS 并检查结果。")
	return skill.DiscoverWithHome(work, home), dir
}

func TestSkillLocalCommands(t *testing.T) {
	store, _ := testCommandSkillStore(t)
	handler := newLocalCommandHandler(nil, store, nil)

	result, handled := handler.Handle("/skills")
	if !handled || result.Prompt != "" {
		t.Fatalf("/skills 应只显示结果：%+v %v", result, handled)
	}
	for _, want := range []string{"已加载 1 个", "go-test", "[项目]", "运行 Go 测试时使用。", "SKILL.md"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("/skills 缺少 %q：%s", want, result.Output)
		}
	}

	result, handled = handler.Handle("/skill go-test 修复 TestFoo")
	if !handled || result.Prompt == "" || result.Output != "" {
		t.Fatalf("/skill 应生成本轮任务：%+v %v", result, handled)
	}
	for _, want := range []string{"请使用下面的 Skill", "执行 修复 TestFoo 并检查结果。", "来源：项目级", "不能授予权限"} {
		if !strings.Contains(result.Prompt, want) {
			t.Fatalf("/skill 展开缺少 %q：%s", want, result.Prompt)
		}
	}

	result, handled = handler.Handle("/skill missing")
	if !handled || result.Prompt != "" || !strings.Contains(result.Output, "未知 Skill") || !strings.Contains(result.Output, "go-test") {
		t.Fatalf("未知 Skill 应直接提示：%+v %v", result, handled)
	}
	result, handled = handler.Handle("/skill")
	if !handled || !strings.Contains(result.Output, "用法") {
		t.Fatalf("缺少 /skill 用法：%+v %v", result, handled)
	}
	result, handled = handler.Handle("/skills reload")
	if !handled || !strings.Contains(result.Output, "未加载 Skill") {
		t.Fatalf("没有重载回调时应提示：%+v %v", result, handled)
	}
	if _, handled = handler.Handle("/unknown"); handled {
		t.Fatal("未知本地命令不应被拦截")
	}
}

func TestSkillReloadCommand(t *testing.T) {
	store, dir := testCommandSkillStore(t)
	handler := newLocalCommandHandler(nil, store, store.Reload)
	result, handled := handler.Handle("/skills reload")
	if !handled || !strings.Contains(result.Output, "已重载") || !strings.Contains(result.Output, "go-test") {
		t.Fatalf("重载结果异常：%+v %v", result, handled)
	}

	writeCommandSkill(t, dir, "new-skill", "新增 Skill。", "body")
	result, _ = handler.Handle("/skills reload")
	if !strings.Contains(result.Output, "new-skill") {
		t.Fatalf("重载后应出现新增 Skill：%s", result.Output)
	}

	handler = newLocalCommandHandler(nil, store, func() error {
		return os.ErrPermission
	})
	result, _ = handler.Handle("/skills reload")
	if !strings.Contains(result.Output, "重载失败") || !strings.Contains(result.Output, "保留原集合") {
		t.Fatalf("重载失败提示异常：%s", result.Output)
	}
}

func TestSkillSummaryEmptyAndProblems(t *testing.T) {
	work, home := t.TempDir(), t.TempDir()
	store := skill.DiscoverWithHome(work, home)
	result, handled := newLocalCommandHandler(nil, store, nil).Handle("/skills")
	if !handled || !strings.Contains(result.Output, "未发现 Skill") || !strings.Contains(result.Output, filepath.Join(".johnny", "skills")) {
		t.Fatalf("空 Skill 列表提示异常：%+v", result)
	}

	invalid := filepath.Join(work, ".johnny", "skills", "broken")
	if err := os.MkdirAll(invalid, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalid, "SKILL.md"), []byte("缺少 frontmatter"), 0600); err != nil {
		t.Fatal(err)
	}
	store = skill.DiscoverWithHome(work, home)
	result, _ = newLocalCommandHandler(nil, store, nil).Handle("/skills")
	if !strings.Contains(result.Output, "未加载") || !strings.Contains(result.Output, "broken") || !strings.Contains(result.Output, "缺少 frontmatter") {
		t.Fatalf("无效 Skill 展示异常：%s", result.Output)
	}
}

func TestPrintSkillNotice(t *testing.T) {
	var output bytes.Buffer
	printSkillNotice(&output, skill.DiscoverWithHome(t.TempDir(), t.TempDir()))
	if output.Len() != 0 {
		t.Fatalf("没有 Skill 时不应输出提示：%q", output.String())
	}
	store, _ := testCommandSkillStore(t)
	printSkillNotice(&output, store)
	if !strings.Contains(output.String(), "已加载 1 个") || !strings.Contains(output.String(), "/skills") {
		t.Fatalf("启动提示异常：%s", output.String())
	}
}
