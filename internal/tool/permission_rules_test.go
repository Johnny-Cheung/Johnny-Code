package tool

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFixedQueryWhitelistTable(t *testing.T) {
	names := fixedQueryCommandNames()
	want := []string{
		"basename", "cat", "diff", "dirname", "du", "echo", "find", "grep", "head", "ls",
		"pwd", "realpath", "stat", "tail", "wc",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("固定查询名单错误：%#v", names)
	}
	lines := fixedQueryCommandLines()
	if len(lines) != len(want) {
		t.Fatalf("显示文本数量错误：%d", len(lines))
	}
	for _, line := range lines {
		if line == "" {
			t.Fatal("显示文本不能为空")
		}
	}
}

func TestBuildRestrictedCommandAllowsExpandedQueries(t *testing.T) {
	scope, root := testWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "second.txt"), []byte("second\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		"cat file.txt",
		"cat -n file.txt second.txt",
		"head -n 1 file.txt",
		"head -q -v file.txt",
		"tail -n +1 file.txt",
		"tail -q file.txt",
		"wc -l file.txt",
		"wc -lwc file.txt second.txt",
		"stat file.txt",
		"du -sh .",
		"du -d 0 .",
		"diff -u file.txt second.txt",
		"diff -r . dir",
	} {
		if _, err := buildRestrictedCommand(command, scope); err != nil {
			t.Fatalf("命令 %q 应允许：%v", command, err)
		}
	}
}

func TestBuildRestrictedCommandRejectsExpandedQueryVariants(t *testing.T) {
	scope, _ := testWorkspace(t)
	for _, command := range []string{
		"cat -",
		"cat --follow file.txt",
		"head -f file.txt",
		"head -n -1 file.txt",
		"tail -f file.txt",
		"tail -F file.txt",
		"tail -n -1 file.txt",
		"wc --files0-from=list",
		"stat -f file.txt",
		"du -L .",
		"du -d 99 .",
		"diff --color file.txt file.txt",
		"diff -U 99 file.txt file.txt",
	} {
		if _, err := buildRestrictedCommand(command, scope); err == nil {
			t.Fatalf("命令 %q 应被拒绝", command)
		}
	}
}

func TestBuildRestrictedCommandExpandedQueriesRequireOperands(t *testing.T) {
	scope, _ := testWorkspace(t)
	for _, command := range []string{
		"cat",
		"head",
		"tail",
		"wc",
		"stat",
		"diff file.txt",
		"diff file.txt file.txt extra",
	} {
		if _, err := buildRestrictedCommand(command, scope); err == nil {
			t.Fatalf("命令 %q 应被拒绝", command)
		} else {
			requireToolKind(t, err, KindInvalidArguments)
		}
	}
}

func TestDangerousSuffixMatching(t *testing.T) {
	cases := []struct {
		program string
		tokens  []string
		hit     string
		ok      bool
	}{
		{"git", []string{"git", "log", "--output=x"}, "--output", true},
		{"git", []string{"git", "log", "--textconv"}, "--textconv", true},
		{"git", []string{"git", "log", "--", "--output"}, "", false},
		{"go", []string{"go", "test", "-exec=echo"}, "-exec", true},
		{"cargo", []string{"cargo", "test", "--manifest-path", "x"}, "--manifest-path", true},
		{"npm", []string{"npm", "run", "build", "--prefix", "/tmp/x"}, "--prefix", true},
		{"make", []string{"make", "test", "-f", "evil.mk"}, "-f", true},
		{"git", []string{"git", "status", "--short"}, "", false},
	}
	for _, c := range cases {
		hit, ok := dangerousSuffixHit(c.program, c.tokens)
		if hit != c.hit || ok != c.ok {
			t.Fatalf("%s %v：got %q %v，want %q %v", c.program, c.tokens, hit, ok, c.hit, c.ok)
		}
	}
}

func TestRecordablePrefix(t *testing.T) {
	cases := []struct {
		tokens []string
		want   []string
		ok     bool
	}{
		{[]string{"go", "test", "./..."}, []string{"go", "test"}, true},
		{[]string{"git", "log", "--oneline"}, []string{"git", "log"}, true},
		{[]string{"npm", "run", "build", "--prod"}, []string{"npm", "run", "build"}, true},
		{[]string{"bun", "build", "app.ts"}, []string{"bun", "build"}, true},
		{[]string{"make", "test"}, []string{"make", "test"}, true},
		{[]string{"git", "push", "origin", "main"}, nil, false},
		{[]string{"git", "add", "."}, nil, false},
		{[]string{"npm", "install"}, nil, false},
		{[]string{"npm", "run"}, nil, false},
		{[]string{"git", "-C", "dir", "status"}, nil, false},
		{[]string{"python", "main.py"}, nil, false},
		{[]string{"/usr/bin/make", "test"}, nil, false},
	}
	for _, c := range cases {
		got, ok := recordablePrefix(c.tokens)
		if ok != c.ok || !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%v：got %v %v，want %v %v", c.tokens, got, ok, c.want, c.ok)
		}
	}
}

func TestValidRecordablePrefix(t *testing.T) {
	for _, prefix := range [][]string{
		{"go", "test"},
		{"npm", "run", "build"},
		{"git", "log"},
		{"make", "test"},
	} {
		if !validRecordablePrefix(prefix) {
			t.Fatalf("前缀应有效：%v", prefix)
		}
	}
	for _, prefix := range [][]string{
		{"git", "push"},
		{"npm", "install"},
		{"npm", "run"},
		{"git", "log", "--output"},
	} {
		if validRecordablePrefix(prefix) {
			t.Fatalf("前缀应无效：%v", prefix)
		}
	}
}
