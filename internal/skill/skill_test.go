package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, skillsDir, name, description, body string) string {
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

func writeRawSkill(t *testing.T, skillsDir, name, content string) string {
	t.Helper()
	dir := filepath.Join(skillsDir, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func testDirs(t *testing.T) (workDir, homeDir string) {
	t.Helper()
	return t.TempDir(), t.TempDir()
}

func TestDiscoverProjectOverridesUser(t *testing.T) {
	work, home := testDirs(t)
	userDir := filepath.Join(home, ".johnny", "skills")
	projectDir := filepath.Join(work, ".johnny", "skills")
	writeSkill(t, userDir, "go-test", "用户级测试指导。", "user body")
	projectSkill := writeSkill(t, projectDir, "go-test", "项目级测试指导。", "project body")

	store := DiscoverWithHome(work, home)
	got, ok := store.Lookup("go-test")
	if !ok {
		t.Fatal("应加载项目级 go-test")
	}
	if got.Source != SourceProject || got.Body != "project body" || got.Dir != projectSkill {
		t.Fatalf("项目级应覆盖用户级：%+v", got)
	}
	shadowed := store.Shadowed()
	if len(shadowed) != 1 || shadowed[0].Name != "go-test" || !strings.Contains(shadowed[0].Path, filepath.Join(".johnny", "skills", "go-test")) {
		t.Fatalf("用户级覆盖记录异常：%+v", shadowed)
	}
	prompt := store.Prompt()
	if !strings.Contains(prompt, "- go-test [项目]：项目级测试指导。") {
		t.Fatalf("系统提示缺少项目级元数据：%s", prompt)
	}
	if strings.Contains(prompt, "user body") || strings.Contains(prompt, "project body") {
		t.Fatalf("系统提示不应包含正文：%s", prompt)
	}
	stats := store.Stats()
	if stats.Loaded != 1 || stats.Project != 1 || stats.User != 0 || stats.Injected != 1 || stats.Shadowed != 1 {
		t.Fatalf("统计异常：%+v", stats)
	}
}

func TestDiscoverInvalidSkillsAreSkipped(t *testing.T) {
	work, home := testDirs(t)
	dir := filepath.Join(work, ".johnny", "skills")
	writeSkill(t, dir, "good", "可用的 Skill。", "good body")
	writeRawSkill(t, dir, "no-front", "没有 frontmatter")
	writeRawSkill(t, dir, "wrong-name", "---\nname: other\ndescription: 名称不一致\n---\nbody")
	writeSkill(t, dir, "empty-description", "", "body")
	writeSkill(t, dir, "empty-body", "描述", "   ")
	writeSkill(t, dir, "huge-description", strings.Repeat("描", MaxDescriptionBytes), "body")
	writeSkill(t, dir, "huge-body", "描述", strings.Repeat("x", MaxBodyBytes+1))
	writeRawSkill(t, dir, "with-tools", "---\nname: with-tools\ndescription: 带工具限制的 Skill\nallowed-tools: Read, Grep\n---\nbody")

	store := DiscoverWithHome(work, home)
	names := store.Names()
	if len(names) != 2 || names[0] != "good" || names[1] != "with-tools" {
		t.Fatalf("应只加载 good 和 with-tools：%v", names)
	}
	if got, ok := store.Lookup("with-tools"); !ok || !strings.Contains(got.Body, "body") {
		t.Fatal("allowed-tools 应被静默忽略，不影响加载")
	}
	problems := store.Problems()
	if len(problems) != 6 {
		t.Fatalf("应有 6 个被跳过的 Skill：%+v", problems)
	}
	for _, problem := range problems {
		if problem.Reason == "" {
			t.Fatalf("问题缺少原因：%+v", problem)
		}
	}
}

func TestPromptCountAndBudget(t *testing.T) {
	work, home := testDirs(t)
	dir := filepath.Join(work, ".johnny", "skills")
	for i := 0; i < MaxSkills+2; i++ {
		name := "skill-" + twoDigits(i)
		writeSkill(t, dir, name, "描述 "+name, "body")
	}
	store := DiscoverWithHome(work, home)
	stats := store.Stats()
	if stats.Loaded != MaxSkills {
		t.Fatalf("加载数量应限制为 %d：%+v", MaxSkills, stats)
	}
	if stats.Omitted != 2 {
		t.Fatalf("应记录 2 个超过数量上限的 Skill：%+v", stats)
	}
	if got := store.Names(); len(got) != MaxSkills || got[0] != "skill-00" || got[MaxSkills-1] != "skill-63" {
		t.Fatalf("数量上限应保留名称排序后的前 %d 个：%v", MaxSkills, got)
	}
}

func TestPromptMetadataBudget(t *testing.T) {
	work, home := testDirs(t)
	dir := filepath.Join(work, ".johnny", "skills")
	description := strings.Repeat("a", 500)
	for i := 0; i < 20; i++ {
		writeSkill(t, dir, "budget-"+twoDigits(i), description, "body")
	}
	store := DiscoverWithHome(work, home)
	stats := store.Stats()
	if stats.Loaded != 20 || stats.Injected >= stats.Loaded {
		t.Fatalf("元数据预算应只注入部分 Skill：%+v", stats)
	}
	if stats.Omitted == 0 {
		t.Fatalf("应记录未注入提示的 Skill：%+v", stats)
	}
	for _, omitted := range store.Omitted() {
		if omitted.Reason != "超过系统提示元数据预算" {
			t.Fatalf("未注入原因异常：%+v", omitted)
		}
		if store.Injected(omitted.Name) {
			t.Fatalf("未注入项不应标记为已注入：%s", omitted.Name)
		}
	}
	if len(store.Prompt()) > MaxPromptBytes+512 {
		t.Fatalf("系统提示元数据过大：%d", len(store.Prompt()))
	}
}

func TestReloadAddsAndRemoves(t *testing.T) {
	work, home := testDirs(t)
	dir := filepath.Join(work, ".johnny", "skills")
	first := writeSkill(t, dir, "first", "第一个。", "first body")
	store := DiscoverWithHome(work, home)
	if _, ok := store.Lookup("first"); !ok {
		t.Fatal("初始加载失败")
	}
	if err := os.RemoveAll(first); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, dir, "second", "第二个。", "second body")
	if err := store.Reload(); err != nil {
		t.Fatalf("重载失败：%v", err)
	}
	if _, ok := store.Lookup("first"); ok {
		t.Fatal("重载后不应保留已删除的 Skill")
	}
	if _, ok := store.Lookup("second"); !ok {
		t.Fatal("重载后应发现新增的 Skill")
	}
	if !strings.Contains(store.Prompt(), "second") || strings.Contains(store.Prompt(), "first") {
		t.Fatalf("重载后系统提示未更新：%s", store.Prompt())
	}
}

func TestReloadFailurePreservesSkills(t *testing.T) {
	work, home := testDirs(t)
	projectRoot := filepath.Join(work, ".johnny", "skills")
	userRoot := filepath.Join(home, ".johnny", "skills")
	writeSkill(t, projectRoot, "stable", "稳定项。", "stable body")
	store := DiscoverWithHome(work, home)
	if _, ok := store.Lookup("stable"); !ok {
		t.Fatal("初始加载失败")
	}
	for _, root := range []string{projectRoot, userRoot} {
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(root, []byte("not a directory"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Reload(); err == nil {
		t.Fatal("两个目录都不可读时应返回错误")
	}
	if _, ok := store.Lookup("stable"); !ok {
		t.Fatal("重载失败应保留原集合")
	}
}

func TestReloadPartialFailurePreservesAll(t *testing.T) {
	work, home := testDirs(t)
	projectRoot := filepath.Join(work, ".johnny", "skills")
	userRoot := filepath.Join(home, ".johnny", "skills")
	writeSkill(t, projectRoot, "stable", "稳定项。", "stable body")
	store := DiscoverWithHome(work, home)
	writeSkill(t, projectRoot, "new-skill", "新增项。", "new body")
	if err := os.MkdirAll(filepath.Dir(userRoot), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userRoot, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err == nil {
		t.Fatal("任一目录不可读时应返回错误")
	}
	if _, ok := store.Lookup("stable"); !ok {
		t.Fatal("重载失败应保留原有项目级 Skill")
	}
	if _, ok := store.Lookup("new-skill"); ok {
		t.Fatal("重载失败不应应用部分扫描结果")
	}
	if err := os.Remove(userRoot); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err != nil {
		t.Fatalf("修复目录后应重载成功：%v", err)
	}
	if _, ok := store.Lookup("new-skill"); !ok {
		t.Fatal("重载成功后应发现新增 Skill")
	}
}

func TestLoadedArguments(t *testing.T) {
	item := Skill{Name: "demo", Source: SourceProject, Dir: "/tmp/demo", Body: "执行 $ARGUMENTS 并检查。"}
	rendered := item.Loaded("go test ./...")
	if !strings.Contains(rendered, "执行 go test ./... 并检查。") {
		t.Fatalf("$ARGUMENTS 未替换：%s", rendered)
	}
	if !strings.Contains(rendered, "来源：项目级") || !strings.Contains(rendered, "目录：/tmp/demo") || !strings.Contains(rendered, "不能授予权限") {
		t.Fatalf("缺少来源、目录或权限边界：%s", rendered)
	}
	withoutPlaceholder := Skill{Name: "demo", Source: SourceUser, Dir: "/tmp/demo", Body: "按步骤执行。"}
	rendered = withoutPlaceholder.Loaded("修复测试")
	if !strings.Contains(rendered, "用户参数：修复测试") {
		t.Fatalf("无占位符时应追加参数：%s", rendered)
	}
	if strings.Contains(withoutPlaceholder.Loaded(""), "用户参数") {
		t.Fatal("没有参数时不应追加参数说明")
	}
}

func TestDiscoverWithoutUserHome(t *testing.T) {
	work := t.TempDir()
	writeSkill(t, filepath.Join(work, ".johnny", "skills"), "project-only", "项目级。", "body")
	store := DiscoverWithHome(work, "")
	if _, ok := store.Lookup("project-only"); !ok {
		t.Fatal("没有用户目录时应继续加载项目级 Skill")
	}
	if stats := store.Stats(); stats.Problems != 0 {
		t.Fatalf("空用户目录不应产生问题：%+v", stats)
	}
}

func twoDigits(value int) string {
	return fmt.Sprintf("%02d", value)
}
