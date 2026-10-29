package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testWorkspace(t *testing.T) (workspaceScope, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	scope, err := newWorkspaceScope(root)
	if err != nil {
		t.Fatal(err)
	}
	return scope, root
}

func requireToolKind(t *testing.T, err error, kind ErrorKind) {
	t.Helper()
	if err == nil {
		t.Fatalf("预期错误类型 %s，实际成功", kind)
	}
	var toolErr *Error
	if !errors.As(err, &toolErr) || toolErr.Kind != kind {
		t.Fatalf("错误类型应为 %s，实际 %v", kind, err)
	}
}

func TestTokenizeRestrictedQuotesAndEscapes(t *testing.T) {
	tokens, err := tokenizeRestricted(`grep -E 'a|b' "file name" foo\ bar`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"grep", "-E", "a|b", "file name", "foo bar"}
	if !reflect.DeepEqual(tokens, want) {
		t.Fatalf("tokenizer 结果错误：%#v", tokens)
	}
	if tokens, err := tokenizeRestricted(`"foo"'bar' ""`); err != nil || !reflect.DeepEqual(tokens, []string{"foobar", ""}) {
		t.Fatalf("相邻引号处理错误：%#v %v", tokens, err)
	}
	if tokens, err := tokenizeRestricted(`grep "\\d" "end$"`); err != nil || !reflect.DeepEqual(tokens, []string{"grep", `\d`, "end$"}) {
		t.Fatalf("双引号转义处理错误：%#v %v", tokens, err)
	}
}

func TestTokenizeRestrictedRejectsShellSyntax(t *testing.T) {
	for _, command := range []string{
		"ls | head",
		"ls > out",
		"ls < in",
		"find . && rm file",
		"find . ; rm file",
		"grep $(id) .",
		"grep `id` .",
		"grep foo\nls",
		"grep 'unclosed",
	} {
		if _, err := tokenizeRestricted(command); err == nil {
			t.Fatalf("命令应被拒绝：%q", command)
		}
	}
	// 引号内的 shell 元字符只是字面量。
	tokens, err := tokenizeRestricted(`grep -E 'a|b' "end$"`)
	if err != nil || !reflect.DeepEqual(tokens, []string{"grep", "-E", "a|b", "end$"}) {
		t.Fatalf("引号内元字符应保留：%#v %v", tokens, err)
	}
}

func TestBuildRestrictedCommandAllowsQueries(t *testing.T) {
	scope, root := testWorkspace(t)
	rootResolved := mustEvalSymlinks(t, root)

	cases := []struct {
		command string
		program string
		args    []string
	}{
		{"pwd", "pwd", nil},
		{"ls -la .", "ls", []string{"-la", "--", rootResolved}},
		{"ls", "ls", []string{"--", rootResolved}},
		{"find . -maxdepth 2 -type f -name '*.go'", "find", []string{rootResolved, "-maxdepth", "2", "-type", "f", "-name", "*.go"}},
		{"find -name '*.go'", "find", []string{rootResolved, "-name", "*.go"}},
		{"grep -nE 'foo|bar' .", "grep", []string{"-nE", "--", "foo|bar", rootResolved}},
		{"grep -- 'end$' file.txt", "grep", []string{"--", "end$", filepath.Join(rootResolved, "file.txt")}},
	}
	for _, c := range cases {
		got, err := buildRestrictedCommand(c.command, scope)
		if err != nil {
			t.Fatalf("命令 %q 应允许：%v", c.command, err)
		}
		if got.program != c.program || !reflect.DeepEqual(got.args, c.args) {
			t.Fatalf("命令 %q 解析错误：%#v", c.command, got)
		}
	}
	// -name 的取值即使以 - 开头也只是模式，不会变成 find primary。
	got, err := buildRestrictedCommand("find . -name -delete", scope)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{rootResolved, "-name", "-delete"}; !reflect.DeepEqual(got.args, want) {
		t.Fatalf("-name 模式值处理错误：%#v", got.args)
	}
}

func TestBuildRestrictedCommandRejectsCommandsAndOptions(t *testing.T) {
	scope, _ := testWorkspace(t)
	for _, command := range []string{
		"sed -i 1p file.txt",
		"awk 1 file.txt",
		"sh -c id",
		"ls | cat",
		"ls > out",
		"pwd extra",
		"ls -L",
		"ls --time-style=iso",
		"ls -aL",
		"grep -R foo .",
		"grep -f list .",
		"grep --file=list .",
		"grep --exclude-from=list .",
		"find . -delete",
		"find . -exec cat {} ;",
		"find . -fprint out",
		"find . -L",
	} {
		_, err := buildRestrictedCommand(command, scope)
		requireToolKind(t, err, KindPermissionDenied)
	}
	for _, command := range []string{
		"find . -maxdepth 99",
		"find . -maxdepth x",
		"find . -type x",
		"find . -name",
		"grep",
		"grep foo",
	} {
		_, err := buildRestrictedCommand(command, scope)
		requireToolKind(t, err, KindInvalidArguments)
	}
}

func TestBuildRestrictedCommandChecksPaths(t *testing.T) {
	scope, _ := testWorkspace(t)
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "outside.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside.txt")
	for _, command := range []string{
		"ls ..",
		"find ..",
		"grep foo ../outside.txt",
		"ls " + outside,
	} {
		_, err := buildRestrictedCommand(command, scope)
		requireToolKind(t, err, KindPermissionDenied)
	}
	for _, command := range []string{
		"ls missing",
		"find missing",
		"grep foo missing.txt",
	} {
		// 不存在的路径由实际程序报告，不再在预检阶段失败。
		if _, err := buildRestrictedCommand(command, scope); err != nil {
			t.Fatalf("%q 应交给程序报告路径不存在：%v", command, err)
		}
	}
}

func skipWithoutCommand(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("系统缺少 %s", name)
	}
}

func TestRestrictedBashRunsQueries(t *testing.T) {
	runtime := testRuntime(t)
	root := mustEvalSymlinks(t, runtime.WorkDir)
	if err := os.WriteFile(filepath.Join(runtime.WorkDir, "file.txt"), []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := newRestrictedBash(runtime)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		command string
		want    string
	}{
		{"pwd", root},
		{"ls -a", "file.txt"},
		{"find . -maxdepth 1 -type f -name '*.txt'", "file.txt"},
		{"grep -n hello file.txt", "1:hello"},
	}
	for _, c := range cases {
		skipWithoutCommand(t, strings.Fields(c.command)[0])
		result, err := b.execute(context.Background(), c.command, nil)
		if err != nil {
			t.Fatalf("命令 %q 执行失败：%v", c.command, err)
		}
		if !strings.Contains(result.Content, c.want) {
			t.Fatalf("命令 %q 输出缺少 %q：%q", c.command, c.want, result.Content)
		}
	}
}

func TestRestrictedBashExpandedQueriesRun(t *testing.T) {
	runtime := testRuntime(t)
	for name, content := range map[string]string{
		"file.txt":   "hello\n",
		"second.txt": "world\n",
	} {
		if err := os.WriteFile(filepath.Join(runtime.WorkDir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	b, err := newRestrictedBash(runtime)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		command string
		want    string
	}{
		{"cat file.txt", "hello"},
		{"cat -n file.txt", "1\thello"},
		{"head -n 1 file.txt", "hello"},
		{"tail -n +1 file.txt", "hello"},
		{"wc -l file.txt", "1"},
		{"stat file.txt", "file.txt"},
		{"du -sh .", "."},
		{"diff -u file.txt second.txt", "---"},
	}
	for _, c := range cases {
		skipWithoutCommand(t, strings.Fields(c.command)[0])
		result, err := b.execute(context.Background(), c.command, nil)
		if err != nil {
			t.Fatalf("命令 %q 执行失败：%v", c.command, err)
		}
		if !strings.Contains(result.Content, c.want) {
			t.Fatalf("命令 %q 输出缺少 %q：%q", c.command, c.want, result.Content)
		}
	}
}

func TestRestrictedBashGrepNoMatches(t *testing.T) {
	skipWithoutCommand(t, "grep")
	runtime := testRuntime(t)
	if err := os.WriteFile(filepath.Join(runtime.WorkDir, "file.txt"), []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := newRestrictedBash(runtime)
	if err != nil {
		t.Fatal(err)
	}
	result, err := b.execute(context.Background(), "grep -n absent file.txt", nil)
	if err != nil {
		t.Fatalf("grep 无匹配不应作为执行失败：%v", err)
	}
	if !strings.Contains(result.Content, "无匹配") || result.IsError {
		t.Fatalf("grep 无匹配结果异常：%#v", result)
	}
}

func TestRestrictedBashRejectsBeforeStarting(t *testing.T) {
	runtime := testRuntime(t)
	if err := os.WriteFile(filepath.Join(runtime.WorkDir, "keep.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := newRestrictedBash(runtime)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		"find . -delete",
		"ls | sh",
		"ls -L",
	} {
		_, err := b.execute(context.Background(), command, nil)
		requireToolKind(t, err, KindPermissionDenied)
	}
	content, err := os.ReadFile(filepath.Join(runtime.WorkDir, "keep.txt"))
	if err != nil || string(content) != "keep" {
		t.Fatalf("被拒绝的命令产生了副作用：%q %v", content, err)
	}
}

func TestRestrictedBashRejectsOutsidePaths(t *testing.T) {
	runtime := testRuntime(t)
	outsideBase := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideBase, "outside.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := newRestrictedBash(runtime)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.execute(context.Background(), "grep x "+filepath.Join(outsideBase, "outside.txt"), nil)
	requireToolKind(t, err, KindPermissionDenied)
}

func TestRestrictedBashCancellation(t *testing.T) {
	runtime := testRuntime(t)
	b, err := newRestrictedBash(runtime)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.execute(ctx, "pwd", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消上下文应直接返回取消错误：%v", err)
	}
}

func TestRestrictedBashOutputLimit(t *testing.T) {
	skipWithoutCommand(t, "find")
	runtime := testRuntime(t)
	runtime.MaxOutputBytes = 8
	for i := 0; i < 20; i++ {
		if err := os.WriteFile(filepath.Join(runtime.WorkDir, fmt.Sprintf("file-%02d.txt", i)), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	b, err := newRestrictedBash(runtime)
	if err != nil {
		t.Fatal(err)
	}
	result, err := b.execute(context.Background(), "find . -maxdepth 1 -type f -name '*.txt'", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "输出已截断") {
		t.Fatalf("输出限制未生效：%q", result.Content)
	}
}

func TestRestrictedBashMissingProgram(t *testing.T) {
	runtime := testRuntime(t)
	b, err := newRestrictedBash(runtime)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	_, err = b.execute(context.Background(), "pwd", nil)
	requireToolKind(t, err, KindExecution)
}

func TestRestrictedEnvironmentAllowlist(t *testing.T) {
	base := []string{
		"PATH=/bin:/usr/bin",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"HOME=/home/user",
		"JOHNNY_API_KEY=secret",
		"ANTHROPIC_AUTH_TOKEN=secret",
		"AWS_SECRET_ACCESS_KEY=secret",
	}
	got := restrictedEnvironment(base)
	want := []string{"PATH=/bin:/usr/bin", "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("环境 allowlist 结果错误：%#v", got)
	}
}
