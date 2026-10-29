package tool

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
		"stat file.txt", "du -sh --max-depth=1 .", "realpath file.txt",
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
	d1 := filepath.Join(root, "d1")
	d2 := filepath.Join(root, "d2")
	for _, dir := range []string{d1, d2} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	d1Resolved := mustEvalSymlinks(t, d1)
	d2Resolved := mustEvalSymlinks(t, d2)
	cases := []struct {
		command string
		args    []string
	}{
		// 短选项取值统一粘连，避免跨平台“我们当取值、实际程序当布尔”时
		// 把未解析 token 变成操作数。
		{"grep -e foo -e bar file.txt", []string{"-efoo", "-ebar", "--", file}},
		{"grep -nA 2 foo file.txt", []string{"-nA2", "--", "foo", file}},
		{"grep -r foo", []string{"-r", "--", "foo", rootResolved}},
		{"head -20 file.txt", []string{"-20", "--", file}},
		{"tail -n +2 file.txt", []string{"-n+2", "--", file}},
		{"ls -lh", []string{"-lh", "--", rootResolved}},
		{"basename /a/b.txt .txt", []string{"/a/b.txt", ".txt"}},
		// 长选项的“--name 值”形式归一化成 --name=值。
		{"grep --max-count 2 foo file.txt", []string{"--max-count=2", "--", "foo", file}},
		{"grep --include '*.txt' foo .", []string{"--include=*.txt", "--", "foo", filepath.Dir(file)}},
		{"head --lines 5 file.txt", []string{"--lines=5", "--", file}},
		{"tail --lines +2 file.txt", []string{"--lines=+2", "--", file}},
		{"du --max-depth 1 .", []string{"--max-depth=1", "--", filepath.Dir(file)}},
		{"grep --context=2 foo file.txt", []string{"--context=2", "--", "foo", file}},
		{"diff -r d1 d2", []string{"-r", "--no-dereference", "--", d1Resolved, d2Resolved}},
		{"diff file.txt file.txt", []string{"--", file, file}},
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

func TestTierOneOptionalLongArgumentsDoNotConsumeOperands(t *testing.T) {
	cases := []struct {
		program string
		spec    optionSpec
		tokens  []string
	}{
		{"grep", grepOptions, []string{"grep", "--color", "never", "foo", "file"}},
		{"grep", grepOptions, []string{"grep", "--context", "2", "foo", "file"}},
		{"diff", diffOptions, []string{"diff", "--unified", "1", "a", "b"}},
		{"diff", diffOptions, []string{"diff", "--context", "1", "a", "b"}},
	}
	for _, c := range cases {
		opts, err := parseOptions(c.program, c.tokens, c.spec)
		if err != nil {
			t.Fatalf("%v 应允许：%v", c.tokens, err)
		}
		if len(opts.values) != 0 {
			t.Fatalf("%v 的可选长选项不应消费取值：%#v", c.tokens, opts.values)
		}
		if !reflect.DeepEqual(opts.operands, c.tokens[2:]) {
			t.Fatalf("%v 的操作数错误：%#v", c.tokens, opts.operands)
		}
	}
}

func TestTierOneRecursiveDiffDoesNotFollowSymlinks(t *testing.T) {
	skipWithoutCommand(t, "diff")
	rt := testRuntime(t)
	outside := t.TempDir()
	if err := writeTestFile(outside, "secret.txt", "TOPSECRET\n"); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(rt.WorkDir, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(rt.WorkDir, "a", "item")); err != nil {
		t.Skipf("无法创建符号链接：%v", err)
	}
	if err := writeTestFile(filepath.Join(rt.WorkDir, "b"), "item", "public\n"); err != nil {
		t.Fatal(err)
	}
	b, err := newRestrictedBash(rt)
	if err != nil {
		t.Fatal(err)
	}
	result, err := b.execute(context.Background(), "diff -r a b", nil)
	if err != nil {
		t.Fatalf("递归 diff 执行失败：%v\n%s", err, result.Content)
	}
	if strings.Contains(result.Content, "TOPSECRET") {
		t.Fatalf("递归 diff 不应跟随符号链接读出工作目录外的内容：%q", result.Content)
	}
}

func TestStatOptionsFollowPlatformArity(t *testing.T) {
	scope, _ := testWorkspace(t)
	outside := t.TempDir()
	linux := statOptionsFor("linux")
	bsd := statOptionsFor("darwin")

	// GNU：-f 是布尔量，-c 才带取值。BSD：-f/-t 带取值，-c 不存在。
	if _, isInt := linux.shortInts['f']; isInt {
		t.Fatal("GNU stat -f 不应被建模为带值选项")
	}
	if !strings.ContainsRune(linux.bools, 'f') || !strings.ContainsRune(linux.shortStrs, 'c') {
		t.Fatal("GNU stat 选项表错误")
	}
	if !strings.ContainsRune(bsd.shortStrs, 'f') || !strings.ContainsRune(bsd.shortStrs, 't') {
		t.Fatal("BSD stat -f/-t 应是带值选项")
	}

	switch runtime.GOOS {
	case "linux":
		if _, err := buildRestrictedCommandTokens([]string{"stat", "-f", outside, "file.txt"}, scope, false); err == nil {
			t.Fatal("GNU stat -f 后面的 token 是路径操作数，工作目录外应被拒绝")
		}
		for _, command := range []string{"stat -c %s file.txt", "stat -f file.txt", "stat -t file.txt"} {
			if _, err := buildRestrictedCommand(command, scope); err != nil {
				t.Fatalf("GNU stat 命令 %q 应允许：%v", command, err)
			}
		}
		for _, command := range []string{"stat -x file.txt", "stat -s file.txt"} {
			_, err := buildRestrictedCommand(command, scope)
			requireToolKind(t, err, KindPermissionDenied)
		}
	case "darwin", "freebsd", "netbsd", "openbsd", "dragonfly":
		// BSD 的 -f 取值是格式串，不是一个未解析的路径；不允许 -c。
		for _, command := range []string{"stat -f %z file.txt", "stat -t %Y file.txt", "stat -s file.txt", "stat -x file.txt"} {
			if _, err := buildRestrictedCommand(command, scope); err != nil {
				t.Fatalf("BSD stat 命令 %q 应允许：%v", command, err)
			}
		}
		_, err := buildRestrictedCommand("stat -c %s file.txt", scope)
		requireToolKind(t, err, KindPermissionDenied)
	}
}
