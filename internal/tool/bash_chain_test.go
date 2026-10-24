package tool

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseBashChain(t *testing.T) {
	chain, ok := parseBashChain("cd sub && echo \"---\" && ls -la")
	if !ok {
		t.Fatal("安全的 && 命令链应被识别")
	}
	if len(chain.segments) != 3 {
		t.Fatalf("分段数量错误：%d", len(chain.segments))
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
}

func TestParseBashChainPreservesQuotedOperators(t *testing.T) {
	chain, ok := parseBashChain("cd sub && grep -E 'a&&b' file.txt")
	if !ok || len(chain.segments) != 2 {
		t.Fatalf("引号内 && 不应拆分：%#v %v", chain, ok)
	}
	if !reflect.DeepEqual(chain.segments[1].tokens, []string{"grep", "-E", "a&&b", "file.txt"}) {
		t.Fatalf("引号内操作符被错误处理：%#v", chain.segments[1].tokens)
	}
}

func TestParseBashChainLeavesUnsupportedShellForApproval(t *testing.T) {
	for _, command := range []string{
		"cd sub && ls | head",
		"cd sub && ls > out",
		"cd sub && ls > out",
		"cd sub && ls ||",
		"cd sub && echo $(pwd)",
		"cd sub && for x in *; do echo $x; done",
		"ls *.go && pwd",
		"cd ~/sub && ls",
		"cd sub && cd nested && ls",
		"cd sub && ls &&",
	} {
		if _, ok := parseBashChain(command); ok {
			t.Fatalf("命令 %q 不应进入直接执行分段器", command)
		}
	}
}

func TestParseBashChainRejectsInvalidCd(t *testing.T) {
	for _, command := range []string{
		"cd && ls",
		"cd -P sub && ls",
		"cd sub nested && ls",
		"ls && cd sub",
	} {
		if _, ok := parseBashChain(command); ok {
			t.Fatalf("命令 %q 的 cd 形式应回退审批", command)
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
