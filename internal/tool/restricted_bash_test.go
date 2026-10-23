package tool

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
		"cat file.txt",
		"sed -n 1p file.txt",
		"sh -c id",
		"echo hello",
		"grep foo | head",
		"ls > out",
		"pwd extra",
		"ls -R",
		"ls --color",
		"ls -aR",
		"grep -R foo .",
		"grep -f list .",
		"grep --file=list .",
		"grep -e foo file.txt",
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
		_, err := buildRestrictedCommand(command, scope)
		requireToolKind(t, err, KindExecution)
	}
}
