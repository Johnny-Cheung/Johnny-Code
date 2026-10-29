package tool

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTierOneAllowsCommonReadingForms(t *testing.T) {
	scope, _ := testWorkspace(t)
	for _, command := range []string{
		"ls -R", "ls -aR .", "ls --color=auto", "ls -lhtr", "ls --group-directories-first",
		"grep -v foo file.txt", "grep -C 3 foo file.txt", "grep -A2 foo file.txt", "grep -5 foo file.txt",
		"grep -rn --include=*.go foo .", "grep -e foo -e bar file.txt", "grep -c foo file.txt",
		"grep --max-count=3 foo file.txt", "grep -r foo",
		"head -20 file.txt", "head -c 10 file.txt", "tail -n 5 file.txt", "tail -c +3 file.txt",
		"wc --lines file.txt", "cat --number file.txt", "cat -A file.txt",
		"stat -c %s file.txt", "du -sh --max-depth=1 .", "realpath file.txt",
		"basename /a/b.txt", "dirname /a/b.txt .go",
	} {
		if _, err := buildRestrictedCommand(command, scope); err != nil {
			t.Fatalf("命令 %q 应允许：%v", command, err)
		}
	}
}

func TestTierOneRejectsScopeCrossingAndBlockingForms(t *testing.T) {
	scope, _ := testWorkspace(t)
	for _, command := range []string{
		"ls -L", "ls -H .", "grep -R foo .", "grep -f list .", "grep --file=list .",
		"grep --exclude-from=list .", "grep --devices=read foo .", "grep --rec foo .",
		"tail -f file.txt", "tail -F file.txt", "tail -r file.txt", "wc --files0-from=list",
		"du -L .", "du --files0-from=list", "realpath -x file.txt", "cat --follow file.txt",
	} {
		_, err := buildRestrictedCommand(command, scope)
		requireToolKind(t, err, KindPermissionDenied)
	}
	for _, command := range []string{
		"grep --include foo .", "head -n x file.txt", "head -n 5000000000 file.txt", "grep -A x foo file.txt",
	} {
		_, err := buildRestrictedCommand(command, scope)
		requireToolKind(t, err, KindInvalidArguments)
	}
}

func TestTierOneStdinFormsOnlyInsidePipelines(t *testing.T) {
	scope, _ := testWorkspace(t)
	for _, tokens := range [][]string{
		{"grep", "foo"}, {"grep", "-v", "foo"}, {"head", "-n", "5"}, {"head", "-5"},
		{"tail", "-n", "+2"}, {"wc", "-l"}, {"cat"}, {"cat", "-n"},
	} {
		spec, err := buildRestrictedCommandTokens(tokens, scope, true)
		if err != nil || !spec.readsStdin || len(spec.paths) != 0 {
			t.Fatalf("管道中的 %v 应读取标准输入：%+v %v", tokens, spec, err)
		}
		_, err = buildRestrictedCommandTokens(tokens, scope, false)
		requireToolKind(t, err, KindInvalidArguments)
	}
	spec, err := buildRestrictedCommandTokens([]string{"cat", "file.txt"}, scope, true)
	if err != nil || spec.readsStdin || len(spec.paths) != 1 {
		t.Fatalf("管道中带路径的 cat 应读取文件：%+v %v", spec, err)
	}
}

func TestTierOneBuildsExpectedArguments(t *testing.T) {
	scope, root := testWorkspace(t)
	rootResolved := mustEvalSymlinks(t, root)
	file := filepath.Join(rootResolved, "file.txt")
	cases := []struct {
		command string
		args    []string
	}{
		{"grep -e foo -e bar file.txt", []string{"-e", "foo", "-e", "bar", "--", file}},
		{"grep -nA 2 foo file.txt", []string{"-nA", "2", "--", "foo", file}},
		{"grep -r foo", []string{"-r", "--", "foo", rootResolved}},
		{"head -20 file.txt", []string{"-20", "--", file}},
		{"tail -n +2 file.txt", []string{"-n", "+2", "--", file}},
		{"ls -lh", []string{"-lh", "--", rootResolved}},
		{"basename /a/b.txt .txt", []string{"/a/b.txt", ".txt"}},
	}
	for _, c := range cases {
		got, err := buildRestrictedCommand(c.command, scope)
		if err != nil {
			t.Fatalf("命令 %q 应允许：%v", c.command, err)
		}
		if !reflect.DeepEqual(got.args, c.args) {
			t.Fatalf("命令 %q 的参数错误：%#v", c.command, got.args)
		}
	}
	if _, err := buildRestrictedCommand("grep foo ../outside", scope); err == nil || !strings.Contains(err.Error(), "工作目录") {
		t.Fatalf("工作目录外的路径应被拒绝：%v", err)
	}
}
