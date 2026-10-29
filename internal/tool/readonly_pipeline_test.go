package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadOnlyRestrictedPipelinesAndChains(t *testing.T) {
	readOnly, runtime := readOnlyTest(t)
	if err := os.WriteFile(filepath.Join(runtime.WorkDir, "file.txt"), []byte("hello\nworld\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		command string
		want    string
	}{
		{"ls | head -n 1", "file.txt"},
		{"ls | wc -l", ""},
		{"cat file.txt | wc -l", "2"},
		{"cat file.txt | grep hello | wc -l | head -n 1", "1"},
		{"grep -n hello file.txt | head -n 1", "1:hello"},
		{"cat file.txt | grep -n world | wc -l", "1"},
		{"grep absent file.txt; echo after-semicolon", "after-semicolon"},
		{"grep absent file.txt && echo skipped || echo fallback", "fallback"},
		{"grep absent file.txt || echo fallback | grep fallback", "fallback"},
		{"grep -E 'a&&b|a|b' file.txt; echo quoted-ok", "quoted-ok"},
		{"cat file.txt >/dev/null", ""},
		{"cat file.txt 2>/dev/null", "hello"},
		{"cat file.txt 2>&1 | wc -l", "2"},
	} {
		result, err := executeReadOnly(t, readOnly, "bash", map[string]any{"command": test.command})
		if err != nil {
			t.Fatalf("命令 %q 失败：%v", test.command, err)
		}
		if test.want != "" && !strings.Contains(result.Content, test.want) {
			t.Fatalf("命令 %q 输出缺少 %q：%q", test.command, test.want, result.Content)
		}
	}
}

func TestReadOnlyRestrictedPipelinesRejectUnsupportedForms(t *testing.T) {
	readOnly, runtime := readOnlyTest(t)
	if err := os.WriteFile(filepath.Join(runtime.WorkDir, "file.txt"), []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		"ls | sh",
		"ls > out.txt",
		"ls >> out.txt",
		"ls 2> out.txt",
		"ls < in.txt",
		"ls |& head",
		"ls | | head",
		"echo $(pwd)",
		"echo `pwd`",
		"echo \"$(pwd)\"",
		"echo $HOME",
		"ls *.go",
		"find . -delete",
		"cat file.txt 1>/dev/null",
	} {
		_, err := executeReadOnly(t, readOnly, "bash", map[string]any{"command": command})
		requireToolKind(t, err, KindPermissionDenied)
	}
}

func TestReadOnlySyntaxRejectionsExplainReason(t *testing.T) {
	readOnly, _ := readOnlyTest(t)
	longChain := "pwd"
	for i := 0; i < maxCommandChainSegments; i++ {
		longChain += " && pwd"
	}
	for _, test := range []struct {
		command string
		want    string
	}{
		{"ls *.go", "通配符"},
		{"ls *.go", "find -name"},
		{"grep --include=*.php pattern .", "find -name"},
		{longChain, fmt.Sprintf("%d 段", maxCommandChainSegments+1)},
		{longChain, "拆成多条"},
		{"echo $(pwd)", "命令替换"},
	} {
		_, err := executeReadOnly(t, readOnly, "bash", map[string]any{"command": test.command})
		requireToolKind(t, err, KindPermissionDenied)
		if !strings.Contains(err.Error(), test.want) {
			t.Fatalf("命令 %q 的拒绝原因缺少 %q：%v", test.command, test.want, err)
		}
	}
}

func TestReadOnlyMissingPathReportsProgramError(t *testing.T) {
	readOnly, _ := readOnlyTest(t)
	result, err := executeReadOnly(t, readOnly, "bash", map[string]any{"command": "cat missing.txt"})
	if err == nil || !strings.Contains(result.Content, "missing.txt") {
		t.Fatalf("不存在的路径应由程序报告：%q %v", result.Content, err)
	}
	requireToolKind(t, err, KindExecution)
	if strings.Contains(err.Error(), "路径不可用") {
		t.Fatalf("不存在路径不应在预检阶段报路径不可用：%v", err)
	}
}

func TestReadOnlyVirtualCwd(t *testing.T) {
	readOnly, runtime := readOnlyTest(t)
	sub := filepath.Join(runtime.WorkDir, "sub")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	resolvedSub := mustEvalSymlinks(t, sub)
	if err := os.WriteFile(filepath.Join(sub, "inside.txt"), []byte("inside\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := executeReadOnly(t, readOnly, "bash", map[string]any{"command": "cd sub && cat inside.txt"})
	if err != nil || !strings.Contains(result.Content, "inside") {
		t.Fatalf("虚拟 cd 后查询失败：%q %v", result.Content, err)
	}
	result, err = executeReadOnly(t, readOnly, "bash", map[string]any{"command": "cd sub; pwd"})
	if err != nil || !strings.Contains(result.Content, resolvedSub) {
		t.Fatalf("虚拟 cd 的 pwd 错误：%q %v", result.Content, err)
	}
	if _, err := executeReadOnly(t, readOnly, "bash", map[string]any{"command": "cd .. && pwd"}); err == nil {
		t.Fatal("只读模式不应允许 cd 到工作目录外")
	} else {
		requireToolKind(t, err, KindPermissionDenied)
	}
	outside := t.TempDir()
	_, err = executeReadOnly(t, readOnly, "bash", map[string]any{"command": "cd " + outside + " && pwd"})
	requireToolKind(t, err, KindPermissionDenied)
}

func TestReadOnlyGrepNoMatchInsideChain(t *testing.T) {
	readOnly, runtime := readOnlyTest(t)
	if err := os.WriteFile(filepath.Join(runtime.WorkDir, "file.txt"), []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := executeReadOnly(t, readOnly, "bash", map[string]any{"command": "grep absent file.txt"})
	if err != nil || result.IsError || !strings.Contains(result.Content, "无匹配") {
		t.Fatalf("grep 无匹配应是非错误结果：%#v %v", result, err)
	}
	result, err = executeReadOnly(t, readOnly, "bash", map[string]any{"command": "grep absent file.txt && echo skipped || echo fallback"})
	if err != nil || !strings.Contains(result.Content, "fallback") || strings.Contains(result.Content, "skipped") {
		t.Fatalf("grep 原始退出码未驱动链：%q %v", result.Content, err)
	}
}
