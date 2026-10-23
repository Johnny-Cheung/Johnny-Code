package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectRulesSaveLoadAndTrust(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "project")
	if err := os.MkdirAll(workDir, 0700); err != nil {
		t.Fatal(err)
	}
	rules, err := NewProjectRules(root, workDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := rules.AddPrefix([]string{"go", "test"}); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0700); err != nil {
		t.Fatal(err)
	}
	outsideResolved := mustEvalSymlinks(t, outside)
	if err := rules.AddPath(outside, PathKindDir); err != nil {
		t.Fatal(err)
	}
	if err := rules.AddPrefix([]string{"git", "push"}); err == nil {
		t.Fatal("危险前缀不应写入项目规则")
	}
	if err := rules.AddPath("relative/path", PathKindFile); err == nil {
		t.Fatal("相对路径不应写入项目规则")
	}

	loaded, hash, exists, err := LoadProjectRules(root, workDir)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || hash == "" {
		t.Fatalf("项目规则文件/哈希缺失：exists=%v hash=%q", exists, hash)
	}
	if loaded.Trusted() {
		t.Fatal("从磁盘加载的规则默认不应受信任")
	}
	if loaded.ContainsPrefix([]string{"go", "test"}) || loaded.ContainsPath(outsideResolved) {
		t.Fatal("未受信任的规则不应生效")
	}
	loaded.SetTrusted(true)
	if !loaded.ContainsPrefix([]string{"go", "test"}) || !loaded.ContainsPath(outsideResolved) {
		t.Fatal("受信任后规则应生效")
	}
	if !loaded.ContainsPath(filepath.Join(outsideResolved, "child")) {
		t.Fatal("目录规则应覆盖子项")
	}

	if err := loaded.RemovePrefix(1); err != nil {
		t.Fatal(err)
	}
	if err := loaded.RemovePath(1); err != nil {
		t.Fatal(err)
	}
	loaded, _, _, err = LoadProjectRules(root, workDir)
	if err != nil {
		t.Fatal(err)
	}
	loaded.SetTrusted(true)
	if len(loaded.CommandPrefixes()) != 0 || len(loaded.PathsRules()) != 0 {
		t.Fatal("删除规则后仍有残留")
	}
	if err := loaded.AddPrefix([]string{"make", "test"}); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Clear(); err != nil {
		t.Fatal(err)
	}
	loaded, _, _, err = LoadProjectRules(root, workDir)
	if err != nil {
		t.Fatal(err)
	}
	loaded.SetTrusted(true)
	if len(loaded.CommandPrefixes()) != 0 || len(loaded.PathsRules()) != 0 {
		t.Fatal("清空后仍有残留")
	}
}

func TestProjectRulesUntrustedCannotRecord(t *testing.T) {
	root := t.TempDir()
	rules, err := NewProjectRules(root, root)
	if err != nil {
		t.Fatal(err)
	}
	rules.SetTrusted(false)
	if err := rules.AddPrefix([]string{"go", "test"}); err == nil {
		t.Fatal("未受信任规则不应允许新增")
	}
	if rules.CanRecord() {
		t.Fatal("未受信任规则不应可记录")
	}
	if !rules.ContainsPath(mustEvalSymlinks(t, root)) {
		t.Fatal("工作目录即使规则未受信任也应允许")
	}
}

func TestProjectRulesSaveFailureRollsBack(t *testing.T) {
	root := t.TempDir()
	rules, err := NewProjectRules(root, root)
	if err != nil {
		t.Fatal(err)
	}
	rules.SetSaveHook(func(string) error { return os.ErrPermission })
	if err := rules.AddPrefix([]string{"go", "test"}); err == nil {
		t.Fatal("信任记录写入失败时不应报告成功")
	}
	if len(rules.CommandPrefixes()) != 0 {
		t.Fatal("保存失败后内存规则应回滚")
	}
	if _, err := os.Stat(projectRulesPath(root)); !os.IsNotExist(err) {
		t.Fatalf("信任记录失败时不应写入规则文件：%v", err)
	}
}

func TestProjectRulesRejectInvalidFile(t *testing.T) {
	root := t.TempDir()
	path := projectRulesPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"command_prefixes":[["git","push"]],"paths":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, exists, err := LoadProjectRules(root, root); err == nil || !exists {
		t.Fatalf("危险前缀规则文件应被拒绝：exists=%v err=%v", exists, err)
	}
}

func TestFindProjectRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	got, err := FindProjectRoot(nested)
	if err != nil {
		t.Fatal(err)
	}
	want := mustEvalSymlinks(t, root)
	if got != want {
		t.Fatalf("项目根目录错误：%q != %q", got, want)
	}
	plain := t.TempDir()
	got, err = FindProjectRoot(plain)
	if err != nil {
		t.Fatal(err)
	}
	if got != mustEvalSymlinks(t, plain) {
		t.Fatalf("无 Git 时应使用工作目录：%q", got)
	}
}

func TestMultiPathScopeResolveAndContains(t *testing.T) {
	root := t.TempDir()
	rules, err := NewProjectRules(root, root)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := newMultiPathScope(root, rules)
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inside, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	resolved, err := scope.resolve("inside.txt")
	if err != nil || !scope.contains(resolved) {
		t.Fatalf("工作目录内解析失败：%q %v", resolved, err)
	}
	outsideBase := t.TempDir()
	outside := filepath.Join(outsideBase, "outside.txt")
	if err := os.WriteFile(outside, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	resolved, err = scope.resolve(outside)
	if err != nil || scope.contains(resolved) {
		t.Fatalf("范围外解析结果异常：%q %v", resolved, err)
	}
	if err := rules.AddPath(outsideBase, PathKindDir); err != nil {
		t.Fatal(err)
	}
	if !scope.contains(resolved) {
		t.Fatal("新增目录规则后路径应包含")
	}
}

func TestPermissionSummary(t *testing.T) {
	root := t.TempDir()
	rules, err := NewProjectRules(root, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := rules.AddPrefix([]string{"go", "test"}); err != nil {
		t.Fatal(err)
	}
	summary := PermissionSummary(rules)
	for _, want := range []string{"固定只读白名单", "cat", "固定危险后缀黑名单", "git", "项目规则", "go test", "/permissions clear"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("权限摘要缺少 %q：%s", want, summary)
		}
	}
}
