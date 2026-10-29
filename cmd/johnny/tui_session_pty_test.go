//go:build darwin || linux

package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestTerminalPTYSessionApprovalAndClear 验证面板“本会话内允许”在真实终端中
// 记住不可记录命令的完整 argv，第二次自动执行；clear-session 后重新询问。
func TestTerminalPTYSessionApprovalAndClear(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := count.Add(1)
		switch n {
		case 1, 3, 5:
			terminalTools(w, []map[string]any{{
				"type": "tool_use", "id": fmt.Sprintf("session-bash-%d", n), "name": "bash",
				"input": map[string]any{"command": "touch session-once.txt"},
			}})
		case 2:
			terminalAnswer(w, "FIRST_OK")
		case 4:
			terminalAnswer(w, "SECOND_OK")
		default:
			terminalAnswer(w, "THIRD_OK")
		}
	}))
	defer server.Close()
	p := startTerminalMode(t, server.URL, "default")

	p.write(t, "first\r")
	end := p.expect(t, 0, "权限确认")
	p.expect(t, end, "本会话内允许")
	if strings.Contains(p.text(), "批准并记录") {
		t.Fatalf("不可记录命令不应显示记录选项：%q", p.text())
	}
	// 默认焦点在拒绝；下移一次到“本会话内允许”。
	p.write(t, "j\r")
	end = p.expect(t, end, "FIRST_OK")
	if _, err := os.Stat(filepath.Join(p.dir, "session-once.txt")); err != nil {
		t.Fatalf("首次会话允许后命令应执行：%v", err)
	}
	end = p.expect(t, end, "空闲")

	secondStart := len(p.text())
	p.write(t, "second\r")
	end = p.expect(t, end, "SECOND_OK")
	if strings.Contains(p.text()[secondStart:end], "权限确认") {
		t.Fatalf("相同 argv 不应再次显示审批：%s", p.text()[secondStart:end])
	}
	end = p.expect(t, end, "空闲")

	p.write(t, "/permissions\r")
	end = p.expect(t, end, "本会话授权")
	p.expect(t, end, "session-once.txt")
	p.expect(t, end, "clear-session")
	p.write(t, "/permissions clear-session\r")
	end = p.expect(t, end, "已清除 1 条本会话授权")

	p.write(t, "third\r")
	end = p.expect(t, end, "权限确认")
	p.write(t, "k\r")
	end = p.expect(t, end, "THIRD_OK")
	end = p.expect(t, end, "空闲")

	p.write(t, "/exit\r")
	p.wait(t, 0)

	logs := sessionLogs(t, p.home)
	if len(logs) != 1 {
		t.Fatalf("应生成一份主会话记录：%v", logs)
	}
	data, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"type":"permission"`, `"decision":"allow_session"`, `"session_commands"`,
		`"cwd":"`, `"argv":["touch","session-once.txt"]`,
	} {
		if !bytes.Contains(data, []byte(want)) {
			t.Fatalf("会话记录缺少 %q：%s", want, data)
		}
	}
	if got := bytes.Count(data, []byte(`"type":"permission"`)); got != 2 {
		t.Fatalf("应只有创建授权和 clear 后的再次允许两条权限事件：%d\n%s", got, data)
	}
}
