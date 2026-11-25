//go:build darwin

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestTerminalPTYSandboxDeniesWriteOutside(t *testing.T) {
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.txt")
	t.Setenv("OUTSIDE", outsideFile)

	var requests atomic.Int32
	var mu sync.Mutex
	var secondBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		if requests.Add(1) == 1 {
			terminalTools(w, []map[string]any{{
				"type": "tool_use", "id": "sandbox-pty", "name": "bash",
				"input": map[string]any{"command": `echo no > "$OUTSIDE"`},
			}})
			return
		}
		mu.Lock()
		secondBody = string(data)
		mu.Unlock()
		terminalAnswer(w, "SANDBOX-DONE")
	}))
	defer server.Close()

	p := startTerminalMode(t, server.URL, "allow")
	p.write(t, "run\r")
	end := p.expect(t, 0, "SANDBOX-DONE")
	p.expect(t, end, "空闲")
	p.write(t, "/exit\r")
	p.wait(t, 0)

	mu.Lock()
	body := secondBody
	mu.Unlock()
	if !strings.Contains(body, "Operation not permitted") && !strings.Contains(body, "exit code 1") {
		t.Fatalf("工具结果没有回到模型或被沙箱拒绝：%s", body)
	}
	if _, err := os.Stat(outsideFile); !os.IsNotExist(err) {
		t.Fatalf("沙箱外文件不应存在：%v", err)
	}
}
