package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func writeProjectRules(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".johnny")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "permissions.local.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadProjectPermissionsTrustFlow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	body := `{"version":1,"command_prefixes":[["go","test"]],"paths":[]}`
	writeProjectRules(t, root, body)

	var output bytes.Buffer
	rules, err := loadProjectPermissions(root, false, strings.NewReader(""), &output)
	if err != nil {
		t.Fatal(err)
	}
	if rules.Trusted() {
		t.Fatal("非交互且未确认的规则不应加载")
	}
	if !strings.Contains(output.String(), "未受信任") {
		t.Fatalf("缺少未受信任提示：%s", output.String())
	}
	if rules.ContainsPrefix([]string{"go", "test"}) {
		t.Fatal("未受信任规则不应生效")
	}

	_, hash, _, err := tool.LoadProjectRules(root, root)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := tool.FindProjectRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := rememberProjectTrust(canonicalRoot, hash); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	rules, err = loadProjectPermissions(root, false, strings.NewReader(""), &output)
	if err != nil {
		t.Fatal(err)
	}
	if !rules.Trusted() || !rules.ContainsPrefix([]string{"go", "test"}) {
		t.Fatal("已确认且哈希匹配的规则应加载")
	}

	// 内容变化后需要重新确认。
	writeProjectRules(t, root, `{"version":1,"command_prefixes":[["make","test"]],"paths":[]}`)
	output.Reset()
	rules, err = loadProjectPermissions(root, true, strings.NewReader("n\n"), &output)
	if err != nil {
		t.Fatal(err)
	}
	if rules.Trusted() {
		t.Fatal("拒绝确认后不应加载规则")
	}
	if !strings.Contains(output.String(), "本次未加载项目权限规则") {
		t.Fatalf("缺少拒绝提示：%s", output.String())
	}

	output.Reset()
	rules, err = loadProjectPermissions(root, true, strings.NewReader("y\n"), &output)
	if err != nil {
		t.Fatal(err)
	}
	if !rules.Trusted() || !rules.ContainsPrefix([]string{"make", "test"}) {
		t.Fatal("交互确认后应加载规则")
	}
	if !strings.Contains(output.String(), "是否加载这些规则") {
		t.Fatalf("缺少确认提示：%s", output.String())
	}
}

func TestLoadProjectPermissionsIgnoresInvalidFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	writeProjectRules(t, root, `{"version":1,"command_prefixes":[["git","push"]],"paths":[]}`)
	var output bytes.Buffer
	rules, err := loadProjectPermissions(root, false, strings.NewReader(""), &output)
	if err != nil {
		t.Fatal(err)
	}
	if rules == nil || !rules.Trusted() {
		t.Fatal("无效规则文件应降级为空的可写规则集")
	}
	if !strings.Contains(output.String(), "无法加载") {
		t.Fatalf("缺少无效规则提示：%s", output.String())
	}
}

func TestLocalPermissionCommands(t *testing.T) {
	root := t.TempDir()
	rules, err := tool.NewProjectRules(root, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := rules.AddPrefix([]string{"go", "test"}); err != nil {
		t.Fatal(err)
	}
	if err := rules.AddPrefix([]string{"make", "test"}); err != nil {
		t.Fatal(err)
	}
	text, handled := handleLocalCommand("/permissions", rules)
	if !handled || !strings.Contains(text, "固定只读白名单") || !strings.Contains(text, "go test") {
		t.Fatalf("/permissions 输出异常：%v %q", handled, text)
	}
	text, handled = handleLocalCommand("/permissions remove-command 1", rules)
	if !handled || !strings.Contains(text, "已删除") {
		t.Fatalf("删除命令失败：%v %q", handled, text)
	}
	if len(rules.CommandPrefixes()) != 1 {
		t.Fatalf("删除后剩余数量错误：%d", len(rules.CommandPrefixes()))
	}
	if text, handled = handleLocalCommand("/permissions remove-path 9", rules); !handled || !strings.Contains(text, "超出范围") {
		t.Fatalf("越界删除应报错：%v %q", handled, text)
	}
	if text, handled = handleLocalCommand("/permissions clear", rules); !handled || !strings.Contains(text, "已清空") {
		t.Fatalf("清空失败：%v %q", handled, text)
	}
	if len(rules.CommandPrefixes()) != 0 {
		t.Fatal("清空后仍有规则")
	}
	if _, handled = handleLocalCommand("/permissionsx", rules); handled {
		t.Fatal("未知命令不应被当作本地权限命令")
	}
}

func TestLocalPermissionCommandsWithoutRules(t *testing.T) {
	text, handled := handleLocalCommand("/permissions", nil)
	if !handled || !strings.Contains(text, "当前模式未加载项目规则") {
		t.Fatalf("无项目规则时输出异常：%v %q", handled, text)
	}
	if text, handled = handleLocalCommand("/permissions clear", nil); !handled || !strings.Contains(text, "当前模式未加载") {
		t.Fatalf("无项目规则清空输出异常：%v %q", handled, text)
	}
}
