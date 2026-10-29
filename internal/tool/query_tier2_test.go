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

func writeTestFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
}

func TestTierTwoAllowsClosedForms(t *testing.T) {
	scope, _ := testWorkspace(t)
	for _, command := range []string{
		"find . -name '*.go' -o -name '*.md'", "find . -type f -size +1k -mtime -3", "find . -maxdepth 2 -mindepth 1 -iname 'a*' -print0",
		"find . . -path './x/*' -prune -o -print", "find . -name -delete", "find . -path -exec", "find . -iname -fprint",
		"rg foo", "rg -n -i foo .", "rg -e foo -e bar file.txt", "rg -g '*.go' -t go -A 2 foo .", "rg --files", "rg --max-count=2 foo file.txt",
		"rg -uuu foo .", "rg --unrestricted --unrestricted --unrestricted foo .",
		"sed -n 1,20p file.txt", "sed -n '/foo/,/bar/p' file.txt", "sed -n -e 5p -e '$p' file.txt", "sed -nE '3p' file.txt",
		"sed -ne 2p file.txt", "sed -ne2p file.txt", "sed -n --expression 1p file.txt", "sed -n --expression=1p file.txt",
		"sed -n '/a;w out/p' file.txt",
		"sort file.txt", "sort -rn -k2 -t, file.txt", "sort --reverse --key=1 file.txt", "sort --sort numeric file.txt", "sort --key 1 file.txt",
		"uniq -c file.txt", "uniq -f 1 file.txt", "uniq --skip-fields 1 file.txt",
		"tree", "tree -L 2 -I node_modules .", "tree -a -d --dirsfirst --noreport", "tree -A", "tree --charset UTF-8 .",
		"file file.txt", "file -b --mime-type file.txt",
	} {
		if _, err := buildRestrictedCommand(command, scope); err != nil {
			t.Fatalf("命令 %q 应允许：%v", command, err)
		}
	}
}

func TestTierTwoRejectsEscapeHatches(t *testing.T) {
	scope, _ := testWorkspace(t)
	for _, command := range []string{
		"find . -delete", "find . -exec cat x", "find . -execdir cat x", "find . -ok cat x", "find . -okdir cat x", "find . -fprint out",
		"find . -fprint0 out", "find . -fprintf out x", "find . -fls out", "find . -L", "find . -H", "find . -follow",
		"find . -newer file.txt", "find . -samefile file.txt", "find . -printf x", "find . -ls",
		"find . -name x -delete", "find . -name x -exec cat",
		"rg --pre cat foo .", "rg --pre=cat foo .", "rg --pre-glob '*.gz' foo .", "rg -z foo .", "rg --search-zip foo .",
		"rg -L foo .", "rg --follow foo .", "rg -f list .", "rg --file=list .", "rg --ignore-file=x foo .", "rg --hostname-bin=cat foo .",
		"rg --type-add 'x:*.x' foo .", "rg --pr=cat foo .",
		"sed -i p file.txt", "sed -n w.out file.txt", "sed -n 'w out' file.txt", "sed -n '1p;2w out' file.txt",
		"sed -n 's/a/b/w out' file.txt", "sed -n 's/a/b/e' file.txt", "sed -n '1p;w out' file.txt", "sed -n '1p;e id' file.txt",
		"sed -n '1e id' file.txt", "sed -n 'r file.txt' file.txt", "sed -f script file.txt", "sed -n 1d file.txt", "sed --in-place p file.txt",
		"sed -n --expression 'w out' file.txt", "sed -n --expression '1p;w out' file.txt", "sed -n -e '1p' -e 'w out' file.txt",
		"sed -e'w out' file.txt", "sed -e'1p;w out' file.txt", "sed --expression 'r file.txt' file.txt",
		"sort -o out file.txt", "sort --output=out file.txt", "sort --output out file.txt", "sort -T /tmp file.txt",
		"sort --temporary-directory /tmp file.txt", "sort --compress-program=sh file.txt", "sort --compress-program sh file.txt",
		"sort --files0-from=list", "sort --random-source=/dev/zero file.txt", "sort --buffer-size 1M file.txt", "sort -S 1M file.txt",
		"sort -oout file.txt", "sort -R file.txt",
		"tree -o out", "tree -R", "tree -l", "tree --fromfile list", "tree --gitfile .gitignore .", "tree --infofile .info .",
		"tree --hintro h .", "tree --houtro h .", "tree --timefmt %Y .", "tree --filelimit 10 .", "tree --sort mtime .",
		"tree -H .", "tree -T t", "tree -S", "tree -X .",
		"file -C", "file -m magic file.txt", "file -M magic file.txt", "file --magic-file=magic file.txt", "file -f list", "file --files-from=list",
		"file -e text file.txt", "file --exclude=text file.txt", "file -P indir=1 file.txt", "file --parameter indir=1 file.txt",
		"file -S file.txt", "file --no-sandbox file.txt", "file -z file.txt", "file -Z file.txt", "file -c file.txt",
		"file -d file.txt", "file -D file.txt", "file -s file.txt", "file -p file.txt", "file -L file.txt", "file -0 file.txt",
		"file -r file.txt", "file -l file.txt", "file -F : file.txt",
	} {
		_, err := buildRestrictedCommand(command, scope)
		if err == nil {
			t.Fatalf("命令 %q 应被拒绝", command)
		}
		requireToolKind(t, err, KindPermissionDenied)
	}
	for _, command := range []string{"uniq file.txt out.txt", "uniq -c file.txt out.txt", "uniq file.txt -c", "uniq file.txt --"} {
		_, err := buildRestrictedCommand(command, scope)
		requireToolKind(t, err, KindPermissionDenied)
	}
	for _, command := range []string{"sort --sort=random file.txt", "sort --sort random file.txt", "sort --sort=randomx file.txt"} {
		_, err := buildRestrictedCommand(command, scope)
		requireToolKind(t, err, KindInvalidArguments)
	}
	for _, command := range []string{
		"grep --include", "grep --max-count", "head --lines", "tail --bytes", "sort --key", "sort --field-separator",
		"uniq --skip-fields", "rg --regexp", "sed --expression", "tree --charset", "du --max-depth",
		"find . -maxdepth -delete", "find . -type -delete", "find . -size -delete", "find . -mtime -delete",
	} {
		_, err := buildRestrictedCommand(command, scope)
		requireToolKind(t, err, KindInvalidArguments)
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
		{"find . -name -delete", []string{rootResolved, "-name", "-delete"}},
		{"find . -path -exec", []string{rootResolved, "-path", "-exec"}},
		{"rg foo", []string{"--no-config", "--", "foo", rootResolved}},
		{"rg -e foo -g '*.go' file.txt", []string{"--no-config", "-efoo", "-g*.go", "--", file}},
		{"rg --max-count 2 foo file.txt", []string{"--no-config", "--max-count=2", "--", "foo", file}},
		{"sed -n 2,3p file.txt", []string{"-n", "-e2,3p", "--", file}},
		{"sed -ne 2p file.txt", []string{"-ne2p", "--", file}},
		{"sed -ne2p file.txt", []string{"-ne2p", "--", file}},
		{"sed -e2p file.txt", []string{"-e2p", "--", file}},
		{"sed -n --expression 1p file.txt", []string{"-n", "--expression=1p", "--", file}},
		{"sort -r file.txt", []string{"-r", "--", file}},
		{"sort --key 1 file.txt", []string{"--key=1", "--", file}},
		{"uniq --skip-fields 1 file.txt", []string{"--skip-fields=1", "--", file}},
		{"tree -L 2", []string{"-L2", "--", rootResolved}},
		{"tree --charset UTF-8", []string{"--charset=UTF-8", "--", rootResolved}},
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

func TestTierTwoDashDashOperands(t *testing.T) {
	scope, root := testWorkspace(t)
	rootResolved := mustEvalSymlinks(t, root)
	file := filepath.Join(rootResolved, "file.txt")
	for name, content := range map[string]string{"--": "dashdash\n", "-c": "dashc\n", "-n": "dashn\n"} {
		if err := writeTestFile(root, name, content); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		command string
		args    []string
	}{
		{"cat -- --", []string{"--", filepath.Join(rootResolved, "--")}},
		{"head -- -n", []string{"--", filepath.Join(rootResolved, "-n")}},
		{"uniq -- -c", []string{"--", filepath.Join(rootResolved, "-c")}},
		{"grep -e -- file.txt", []string{"-e--", "--", file}},
		{"grep -- -- file.txt", []string{"--", "--", file}},
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
	// 引号不改变 getopt 的 `--` 语义；要读名为 -- 的文件必须写成 `-- --`。
	_, err := buildRestrictedCommand("cat '--'", scope)
	requireToolKind(t, err, KindInvalidArguments)
}

func TestTierTwoRejectsEscapedLineBreaks(t *testing.T) {
	scope, _ := testWorkspace(t)
	// 反斜杠加换行能把真实换行带进 token；sed 按行解析命令，必须在语义
	// 解析前拒绝。下面的 sed 脚本看似匹配“地址+p”，第二行却是 w 命令。
	for _, command := range []string{
		"sed -n /a\\\nw\\ pwned/p file.txt",
		"find . -name x\\\ny",
		"echo x\\\ny",
	} {
		_, err := buildRestrictedCommand(command, scope)
		requireToolKind(t, err, KindPermissionDenied)
	}
}

func TestTierTwoUniqDoesNotWriteOutputOperand(t *testing.T) {
	runtime := testRuntime(t)
	if err := writeTestFile(runtime.WorkDir, "file.txt", "a\na\n"); err != nil {
		t.Fatal(err)
	}
	b, err := newRestrictedBash(runtime)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// BSD uniq 会把操作数后的 -c 当输出文件；必须在启动前拒绝。
	_, err = b.execute(ctx, "uniq file.txt -c", nil)
	requireToolKind(t, err, KindPermissionDenied)
	if _, statErr := os.Stat(filepath.Join(runtime.WorkDir, "-c")); !os.IsNotExist(statErr) {
		t.Fatalf("uniq 不应创建 -c 输出文件：%v", statErr)
	}
	// 操作数后出现 -- 同样有歧义。
	_, err = b.execute(ctx, "uniq file.txt --", nil)
	requireToolKind(t, err, KindPermissionDenied)
	// 选项写在前面时正常工作。
	result, err := b.execute(ctx, "uniq -c file.txt", nil)
	if err != nil || !strings.Contains(result.Content, "2 a") {
		t.Fatalf("uniq -c 结果异常：%#v %v", result, err)
	}
	// -- 在操作数之前时，后面的 -c 是输入文件名，不是输出文件。
	if err := writeTestFile(runtime.WorkDir, "-c", "dashc\n"); err != nil {
		t.Fatal(err)
	}
	result, err = b.execute(ctx, "uniq -- -c", nil)
	if err != nil || !strings.Contains(result.Content, "dashc") {
		t.Fatalf("uniq -- -c 结果异常：%#v %v", result, err)
	}
}

func TestTierTwoSedCannotExecuteOrWrite(t *testing.T) {
	runtime := testRuntime(t)
	if err := writeTestFile(runtime.WorkDir, "file.txt", "a;w out\na;w out\n"); err != nil {
		t.Fatal(err)
	}
	b, err := newRestrictedBash(runtime)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// 地址正则里的分号是字面量，不构成 w 命令。
	result, err := b.execute(ctx, "sed -n '/a;w out/p' file.txt", nil)
	if err != nil || !strings.Contains(result.Content, "a;w out") {
		t.Fatalf("sed 正则地址结果异常：%#v %v", result, err)
	}
	if _, statErr := os.Stat(filepath.Join(runtime.WorkDir, "out")); !os.IsNotExist(statErr) {
		t.Fatalf("sed 正则地址不应写文件：%v", statErr)
	}
	for _, command := range []string{
		"sed -n '1p;w out2' file.txt",
		"sed -n --expression 'w out3' file.txt",
		"sed -n --expression '1p;e id' file.txt",
	} {
		_, err := b.execute(ctx, command, nil)
		requireToolKind(t, err, KindPermissionDenied)
	}
	for _, name := range []string{"out2", "out3"} {
		if _, statErr := os.Stat(filepath.Join(runtime.WorkDir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("sed 不应创建 %s：%v", name, statErr)
		}
	}
	// 反斜杠换行不能把第二行 w 命令偷带进来。
	if err := os.Mkdir(filepath.Join(runtime.WorkDir, "pwned"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = b.execute(ctx, "sed -n /a\\\nw\\ pwned/p file.txt", nil)
	requireToolKind(t, err, KindPermissionDenied)
	if _, statErr := os.Stat(filepath.Join(runtime.WorkDir, "pwned", "p")); !os.IsNotExist(statErr) {
		t.Fatalf("sed 不应通过转义换行写文件：%v", statErr)
	}
}

func TestTierTwoRgUnrestrictedDoesNotFollowSymlinks(t *testing.T) {
	skipWithoutCommand(t, "rg")
	runtime := testRuntime(t)
	outside := t.TempDir()
	if err := writeTestFile(outside, "secret.txt", "needle\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(runtime.WorkDir, "outlink")); err != nil {
		t.Skipf("无法创建符号链接：%v", err)
	}
	if err := writeTestFile(runtime.WorkDir, "file.txt", "hay\n"); err != nil {
		t.Fatal(err)
	}
	b, err := newRestrictedBash(runtime)
	if err != nil {
		t.Fatal(err)
	}
	result, err := b.execute(context.Background(), "rg -uuu needle .", nil)
	if err != nil {
		t.Fatalf("rg -uuu 不应失败：%v", err)
	}
	if strings.Contains(result.Content, "needle") {
		t.Fatalf("rg -uuu 不应跟随符号链接：%q", result.Content)
	}
	if !strings.Contains(result.Content, "无匹配") {
		t.Fatalf("rg -uuu 应报告无匹配：%q", result.Content)
	}
	_, err = buildRestrictedCommand("rg -L needle .", b.scope)
	requireToolKind(t, err, KindPermissionDenied)
}

func TestTierTwoEnvironmentCannotInjectOptions(t *testing.T) {
	base := []string{
		"PATH=/bin:/usr/bin", "LANG=C", "LC_ALL=C",
		"MAGIC=/tmp/evil.magic", "POSIXLY_CORRECT=1", "RIPGREP_CONFIG_PATH=/tmp/rgrc",
		"GREP_OPTIONS=--file=/tmp/list", "TMPDIR=/tmp/evil", "SORT_TMPDIR=/tmp/evil",
	}
	got := restrictedEnvironment(base)
	want := []string{"PATH=/bin:/usr/bin", "LANG=C", "LC_ALL=C"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("查询环境应只保留 PATH/LANG/LC_*：%#v", got)
	}
}

func TestTierTwoSortStaysEscapeTier(t *testing.T) {
	spec, ok := fixedQueryCommandByName["sort"]
	if !ok || spec.tier != tierEscape || !strings.Contains(spec.summary, "临时文件") {
		t.Fatalf("sort 必须留在逃逸口档并注明临时文件风险：%+v %v", spec, ok)
	}
	scope, _ := testWorkspace(t)
	for _, command := range []string{"sort -T /tmp file.txt", "sort --temporary-directory=/tmp file.txt", "sort -o out file.txt"} {
		_, err := buildRestrictedCommand(command, scope)
		requireToolKind(t, err, KindPermissionDenied)
	}
}

func TestTierTwoNormalizedArgumentsRun(t *testing.T) {
	rt := testRuntime(t)
	if err := writeTestFile(rt.WorkDir, "file.txt", "one\ntwo\nthree\n"); err != nil {
		t.Fatal(err)
	}
	b, err := newRestrictedBash(rt)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cases := []struct {
		command string
		want    string
	}{
		// BSD head 不重排操作数后的 -n；归一化后应仍能执行。
		{"head file.txt -n 1", "one"},
		{"tail file.txt -n +2", "two"},
		{"grep -nA 1 two file.txt", "2:two"},
		{"sed -ne 2p file.txt", "two"},
		{"grep --max-count 1 one file.txt", "one"},
		{"sort --key 1 file.txt", "one"},
	}
	for _, c := range cases {
		skipWithoutCommand(t, strings.Fields(c.command)[0])
		result, err := b.execute(ctx, c.command, nil)
		if err != nil {
			t.Fatalf("命令 %q 执行失败：%v\n%s", c.command, err, result.Content)
		}
		if !strings.Contains(result.Content, c.want) {
			t.Fatalf("命令 %q 输出缺少 %q：%q", c.command, c.want, result.Content)
		}
	}
	statCommand := "stat -c %s file.txt"
	if runtime.GOOS != "linux" {
		statCommand = "stat -f %z file.txt"
	}
	skipWithoutCommand(t, "stat")
	result, err := b.execute(ctx, statCommand, nil)
	if err != nil || strings.TrimSpace(result.Content) == "" {
		t.Fatalf("平台 stat 命令 %q 执行失败：%#v %v", statCommand, result, err)
	}
}

func TestTierTwoRunsAvailablePrograms(t *testing.T) {
	runtime := testRuntime(t)
	for _, command := range []string{
		"find . -name file.txt -o -name none",
		"rg --no-messages hello file.txt",
		"sed -n 1p file.txt",
		"sort file.txt",
		"sort --sort numeric file.txt",
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
