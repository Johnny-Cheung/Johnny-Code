package main

import (
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func TestLocalPermissionCommandsShowAndClearSessionGrants(t *testing.T) {
	root := t.TempDir()
	rules, err := tool.NewProjectRules(root, root)
	if err != nil {
		t.Fatal(err)
	}
	grants := tool.NewSessionGrants()
	if _, _, err := grants.Store([]tool.CommandGrant{
		{WorkDir: root, Prefix: []string{"go", "test"}},
		{WorkDir: root, Argv: []string{"./build.sh", "--fast"}, Redirects: []string{"2>&1"}},
	}, []tool.PathRule{{Path: t.TempDir(), Kind: tool.PathKindDir}}); err != nil {
		t.Fatal(err)
	}
	source := func() *tool.SessionGrants { return grants }

	text, handled := handleLocalCommand("/permissions", rules, source)
	if !handled {
		t.Fatal("/permissions 应被本地处理")
	}
	for _, want := range []string{"固定只读白名单", "项目规则", "本会话授权", "go test", "./build.sh --fast", "2>&1", "clear-session"} {
		if !strings.Contains(text, want) {
			t.Fatalf("/permissions 缺少 %q：%q", want, text)
		}
	}

	text, handled = handleLocalCommand("/permissions clear-session", rules, source)
	if !handled || !strings.Contains(text, "已清除 3 条") {
		t.Fatalf("清除会话授权输出异常：%v %q", handled, text)
	}
	if commands, paths := grants.Len(); commands != 0 || paths != 0 {
		t.Fatalf("清除后仍有授权：%d %d", commands, paths)
	}
	text, handled = handleLocalCommand("/permissions clear-session", rules, source)
	if !handled || !strings.Contains(text, "已清除 0 条") {
		t.Fatalf("重复清除输出异常：%v %q", handled, text)
	}
}

func TestLocalPermissionCommandsSessionWithoutProjectRules(t *testing.T) {
	grants := tool.NewSessionGrants()
	if _, _, err := grants.Store([]tool.CommandGrant{{WorkDir: t.TempDir(), Argv: []string{"echo", "x"}}}, nil); err != nil {
		t.Fatal(err)
	}
	source := func() *tool.SessionGrants { return grants }
	text, handled := handleLocalCommand("/permissions", nil, source)
	if !handled || !strings.Contains(text, "固定只读白名单") || !strings.Contains(text, "本会话授权") {
		t.Fatalf("无项目规则时仍应显示固定名单和会话授权：%v %q", handled, text)
	}
	text, handled = handleLocalCommand("/permissions clear-session", nil, source)
	if !handled || !strings.Contains(text, "已清除 1 条") {
		t.Fatalf("无项目规则时也应能清除会话授权：%v %q", handled, text)
	}
}

func TestLocalPermissionHelpMentionsSession(t *testing.T) {
	text, handled := handleLocalCommand("/permissions help", nil)
	if !handled || !strings.Contains(text, "clear-session") || !strings.Contains(text, "本会话授权") {
		t.Fatalf("帮助应说明会话授权：%v %q", handled, text)
	}
}
