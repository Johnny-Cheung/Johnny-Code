package tool

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeTestFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
}

func TestTierTwoAllowsClosedForms(t *testing.T) {
	scope, _ := testWorkspace(t)
	for _, command := range []string{
		"find . -name '*.go' -o -name '*.md'", "find . -type f -size +1k -mtime -3", "find . -maxdepth 2 -mindepth 1 -iname 'a*' -print0",
		"find . . -path './x/*' -prune -o -print", "find . -name -delete",
		"rg foo", "rg -n -i foo .", "rg -e foo -e bar file.txt", "rg -g '*.go' -t go -A 2 foo .", "rg --files", "rg --max-count=2 foo file.txt",
		"sed -n 1,20p file.txt", "sed -n '/foo/,/bar/p' file.txt", "sed -n -e 5p -e '$p' file.txt", "sed -nE '3p' file.txt",
		"sort file.txt", "sort -rn -k2 -t, file.txt", "sort --reverse --key=1 file.txt", "uniq -c file.txt", "uniq -f 1 file.txt",
		"tree", "tree -L 2 -I node_modules .", "tree -a -d --dirsfirst --noreport", "file file.txt", "file -b --mime-type file.txt",
	} {
		if _, err := buildRestrictedCommand(command, scope); err != nil {
			t.Fatalf("命令 %q 应允许：%v", command, err)
		}
	}
}

func TestTierTwoRejectsEscapeHatches(t *testing.T) {
	scope, _ := testWorkspace(t)
	for _, command := range []string{
		"find . -delete", "find . -exec cat x", "find . -execdir cat x", "find . -ok cat x", "find . -fprint out",
		"find . -fprintf out x", "find . -fls out", "find . -L", "find . -follow", "find . -newer file.txt", "find . -samefile file.txt",
		"rg --pre cat foo .", "rg --pre=cat foo .", "rg --pre-glob '*.gz' foo .", "rg -z foo .", "rg --search-zip foo .",
		"rg -L foo .", "rg --follow foo .", "rg -f list .", "rg --file=list .", "rg --ignore-file=x foo .", "rg --hostname-bin=cat foo .",
		"rg --type-add 'x:*.x' foo .", "rg --pr=cat foo .",
		"sed -i p file.txt", "sed -n w.out file.txt", "sed -n 'w out' file.txt", "sed -n '1p;2w out' file.txt", "sed -n 's/a/b/w out' file.txt",
		"sed -n '1e id' file.txt", "sed -n 'r file.txt' file.txt", "sed -f script file.txt", "sed -n 1d file.txt", "sed --in-place p file.txt",
		"sort -o out file.txt", "sort --output=out file.txt", "sort -T /tmp file.txt", "sort --compress-program=sh file.txt",
		"sort --files0-from=list", "sort --random-source=/dev/zero file.txt", "sort -oout file.txt",
		"tree -o out", "tree -R", "tree -l", "tree --fromfile list", "file -C", "file -m magic file.txt", "file -f list", "file -L file.txt", "file -p file.txt",
	} {
		_, err := buildRestrictedCommand(command, scope)
		if err == nil {
			t.Fatalf("命令 %q 应被拒绝", command)
		}
	}
	for _, command := range []string{"uniq file.txt out.txt", "uniq -c file.txt out.txt"} {
		_, err := buildRestrictedCommand(command, scope)
		requireToolKind(t, err, KindPermissionDenied)
	}
}

func TestTierTwoBuildsExpectedArguments(t *testing.T) {
	scope, root := testWorkspace(t)
	rootResolved := mustEvalSymlinks(t, root)
	file := filepath.Join(rootResolved, "file.txt")
	cases := []struct {
		command string
		args    []string
	}{
		{"find . -name a -o -name b", []string{rootResolved, "-name", "a", "-o", "-name", "b"}},
		{"find . file.txt -type f", []string{rootResolved, file, "-type", "f"}},
		{"rg foo", []string{"--no-config", "--", "foo", rootResolved}},
		{"rg -e foo -g '*.go' file.txt", []string{"--no-config", "-e", "foo", "-g", "*.go", "--", file}},
		{"sed -n 2,3p file.txt", []string{"-n", "-e", "2,3p", "--", file}},
		{"sed -ne 2p file.txt", []string{"-ne", "2p", "--", file}},
		{"sort -r file.txt", []string{"-r", "--", file}},
		{"tree -L 2", []string{"-L", "2", "--", rootResolved}},
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
}

func TestTierTwoStdinFormsOnlyInsidePipelines(t *testing.T) {
	scope, _ := testWorkspace(t)
	for _, tokens := range [][]string{
		{"rg", "foo"}, {"sed", "-n", "1,5p"}, {"sort"}, {"sort", "-rn"}, {"uniq", "-c"},
	} {
		spec, err := buildRestrictedCommandTokens(tokens, scope, true)
		if err != nil || !spec.readsStdin {
			t.Fatalf("管道中的 %v 应读取标准输入：%+v %v", tokens, spec, err)
		}
	}
	spec, err := buildRestrictedCommandTokens([]string{"rg", "foo"}, scope, false)
	if err != nil || spec.readsStdin || len(spec.paths) != 1 {
		t.Fatalf("单独的 rg 应搜索当前目录：%+v %v", spec, err)
	}
	_, err = buildRestrictedCommandTokens([]string{"sort"}, scope, false)
	requireToolKind(t, err, KindInvalidArguments)
}

func TestTierTwoRunsAvailablePrograms(t *testing.T) {
	runtime := testRuntime(t)
	for _, command := range []string{
		"find . -name file.txt -o -name none",
		"rg --no-messages hello file.txt",
		"sed -n 1p file.txt",
		"sort file.txt",
		"uniq -c file.txt",
		"file file.txt",
	} {
		skipWithoutCommand(t, strings.Fields(command)[0])
		b, err := newRestrictedBash(runtime)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeTestFile(runtime.WorkDir, "file.txt", "hello\nhello\n"); err != nil {
			t.Fatal(err)
		}
		result, err := b.execute(context.Background(), command, nil)
		if err != nil {
			t.Fatalf("命令 %q 执行失败：%v\n%s", command, err, result.Content)
		}
		if strings.TrimSpace(result.Content) == "" {
			t.Fatalf("命令 %q 应有输出", command)
		}
	}
}
