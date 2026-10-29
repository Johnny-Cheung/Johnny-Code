package tool

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseShellCommandChain(t *testing.T) {
	chain, err := parseShellCommand("cd sub && echo \"---\" && ls -la")
	if err != nil {
		t.Fatal(err)
	}
	if len(chain.segments) != 3 || len(chain.pipelines) != 3 {
		t.Fatalf("分段/流水线数量错误：%#v", chain)
	}
	if !reflect.DeepEqual(chain.segments[0].tokens, []string{"cd", "sub"}) {
		t.Fatalf("cd 分段错误：%#v", chain.segments[0].tokens)
	}
	if !reflect.DeepEqual(chain.segments[1].tokens, []string{"echo", "---"}) {
		t.Fatalf("echo 分段错误：%#v", chain.segments[1].tokens)
	}
	if !reflect.DeepEqual(chain.segments[2].tokens, []string{"ls", "-la"}) {
		t.Fatalf("ls 分段错误：%#v", chain.segments[2].tokens)
	}
	if chain.pipelines[1].operator != "&&" || chain.pipelines[2].operator != "&&" {
		t.Fatalf("控制操作符错误：%#v", chain.pipelines)
	}
}

func TestParseShellCommandPipelineBindsTighter(t *testing.T) {
	chain, err := parseShellCommand("ls | grep go | head -n 2 && echo ok || echo fail")
	if err != nil {
		t.Fatal(err)
	}
	if len(chain.pipelines) != 3 {
		t.Fatalf("流水线数量错误：%#v", chain.pipelines)
	}
	if got := chain.pipelines[0]; got.operator != "" || got.start != 0 || got.end != 3 {
		t.Fatalf("第一组管道错误：%#v", got)
	}
	if got := chain.pipelines[1]; got.operator != "&&" || got.start != 3 || got.end != 4 {
		t.Fatalf("&& 流水线错误：%#v", got)
	}
	if got := chain.pipelines[2]; got.operator != "||" || got.start != 4 || got.end != 5 {
		t.Fatalf("|| 流水线错误：%#v", got)
	}
	if !chain.segments[1].pipe || !chain.segments[2].pipe || chain.segments[3].pipe {
		t.Fatalf("管道标记错误：%#v", chain.segments)
	}
}

func TestParseShellCommandPreservesQuotedOperators(t *testing.T) {
	chain, err := parseShellCommand("cd sub && grep -E 'a&&b|a|b' \"end$\"")
	if err != nil || len(chain.segments) != 2 {
		t.Fatalf("引号内操作符不应拆分：%#v %v", chain, err)
	}
	want := []string{"grep", "-E", "a&&b|a|b", "end$"}
	if !reflect.DeepEqual(chain.segments[1].tokens, want) {
		t.Fatalf("引号内操作符被错误处理：%#v", chain.segments[1].tokens)
	}
}

func TestParseShellCommandRedirections(t *testing.T) {
	chain, err := parseShellCommand("grep absent file 2>&1 | wc -l >/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(chain.segments[0].redirects, []bashRedirect{{fd: 2, target: "&1"}}) {
		t.Fatalf("2>&1 解析错误：%#v", chain.segments[0].redirects)
	}
	if !reflect.DeepEqual(chain.segments[1].redirects, []bashRedirect{{fd: 1, target: "/dev/null"}}) {
		t.Fatalf(">/dev/null 解析错误：%#v", chain.segments[1].redirects)
	}
	chain, err = parseShellCommand(`sh -c 'echo x' 2> /dev/null 2>&1 >/dev/null`)
	if err != nil {
		t.Fatal(err)
	}
	want := []bashRedirect{{fd: 2, target: "/dev/null"}, {fd: 2, target: "&1"}, {fd: 1, target: "/dev/null"}}
	if !reflect.DeepEqual(chain.segments[0].redirects, want) {
		t.Fatalf("多重定向顺序错误：%#v", chain.segments[0].redirects)
	}
	chain, err = parseShellCommand("echo hi 2> /dev/null 2>& 1")
	if err != nil {
		t.Fatal(err)
	}
	want = []bashRedirect{{fd: 2, target: "/dev/null"}, {fd: 2, target: "&1"}}
	if !reflect.DeepEqual(chain.segments[0].redirects, want) {
		t.Fatalf("带空格的 fd 重定向解析错误：%#v", chain.segments[0].redirects)
	}
}

func TestParseShellCommandLeavesShellSyntaxForApproval(t *testing.T) {
	for _, command := range []string{
		"ls > out",
		"ls >> out",
		"ls < in",
		"echo $(pwd)",
		"echo `pwd`",
		"echo \"$(pwd)\"",
		"echo \"$HOME\"",
		"echo $HOME",
		"ls *.go",
		"cd ~/sub && ls",
		"cd sub && cd nested && ls",
		"ls && cd sub",
		"ls; cd sub",
		"cd sub | cat",
		"cd sub || pwd",
		"echo a &",
		"echo a |& cat",
		"for x in a; do echo $x; done",
		"FOO=bar make test",
		"echo a 1>/dev/null 2>/dev/null",
	} {
		if _, err := parseShellCommand(command); err == nil {
			t.Fatalf("命令 %q 不应进入直接执行分段器", command)
		}
	}
}

func TestParseShellCommandAcceptsLiteralDollarAndEscapes(t *testing.T) {
	for _, test := range []struct {
		command string
		want    []string
	}{
		{`grep 'end$' file`, []string{"grep", "end$", "file"}},
		{`grep "end$" file`, []string{"grep", "end$", "file"}},
		{`echo "a|b&&c"`, []string{"echo", "a|b&&c"}},
		{`echo \$HOME`, []string{"echo", "$HOME"}},
		{`echo '$(pwd)'`, []string{"echo", "$(pwd)"}},
		{`echo a\|b`, []string{"echo", "a|b"}},
		{`echo \>literal`, []string{"echo", ">literal"}},
	} {
		chain, err := parseShellCommand(test.command)
		if err != nil {
			t.Fatalf("%q 应可解析：%v", test.command, err)
		}
		if !reflect.DeepEqual(chain.segments[0].tokens, test.want) {
			t.Fatalf("%q token 错误：%#v", test.command, chain.segments[0].tokens)
		}
	}
}

func TestParseShellCommandRejectsInvalidCd(t *testing.T) {
	for _, command := range []string{
		"cd && ls",
		"cd -P sub && ls",
		"cd sub nested && ls",
		"cd sub >/dev/null",
		"echo x | cd sub",
	} {
		if _, err := parseShellCommand(command); err == nil {
			t.Fatalf("命令 %q 的 cd 形式应回退审批", command)
		}
	}
}

func TestParseShellCommandSegmentLimit(t *testing.T) {
	command := "pwd"
	for i := 0; i < maxCommandChainSegments-1; i++ {
		command += " && pwd"
	}
	if _, err := parseShellCommand(command); err != nil {
		t.Fatalf("%d 段应允许：%v", maxCommandChainSegments, err)
	}
	if _, err := parseShellCommand(command + " && pwd"); err == nil {
		t.Fatal("超过 8 段应回退")
	} else if !strings.Contains(err.Error(), "9 段") || !strings.Contains(err.Error(), "拆成多条") {
		t.Fatalf("段数超限应说明实际段数和处理建议：%v", err)
	}
}

func TestParseShellCommandRejectsGlobWithHint(t *testing.T) {
	for _, command := range []string{"ls *.go", "grep --include=*.php pattern .", "cat *.conf", "ls file?.txt", "ls [ab].txt"} {
		_, err := parseShellCommand(command)
		if err == nil {
			t.Fatalf("命令 %q 应回退审批", command)
		}
		if !strings.Contains(err.Error(), "通配符") || !strings.Contains(err.Error(), "加引号") {
			t.Fatalf("通配符错误应说明原因和处理建议：%v", err)
		}
	}
}

func TestMultiPathScopeResolveFrom(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	rules, err := NewProjectRules(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := newMultiPathScope(dir, rules)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := scope.resolveFrom(dir, "sub")
	if err != nil || resolved != mustEvalSymlinks(t, sub) {
		t.Fatalf("基于虚拟目录解析错误：%q %v", resolved, err)
	}
}

func TestParseShellCommandQuotedDigitsAreNotFD(t *testing.T) {
	for _, command := range []string{`echo "2">/dev/null`, `echo \2>/dev/null`, `echo 2"" >/dev/null`} {
		chain, err := parseShellCommand(command)
		if err != nil {
			t.Fatalf("%q 应可解析：%v", command, err)
		}
		if !reflect.DeepEqual(chain.segments[0].tokens, []string{"echo", "2"}) {
			t.Fatalf("%q 的引号数字被当成 fd：%#v", command, chain.segments[0].tokens)
		}
		if !reflect.DeepEqual(chain.segments[0].redirects, []bashRedirect{{fd: 1, target: "/dev/null"}}) {
			t.Fatalf("%q 的重定向 fd 错误：%#v", command, chain.segments[0].redirects)
		}
	}
	chain, err := parseShellCommand("echo 2>/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	if len(chain.segments[0].tokens) != 1 || !reflect.DeepEqual(chain.segments[0].redirects, []bashRedirect{{fd: 2, target: "/dev/null"}}) {
		t.Fatalf("未引用 2>/dev/null 解析错误：%#v", chain.segments[0])
	}
}
