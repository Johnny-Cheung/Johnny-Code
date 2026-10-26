//go:build darwin || linux

package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func memoryReviewServer(tasks, reviews *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("长期记忆审查器")) {
			reviews.Add(1)
			terminalAnswer(w, `{"memories":[{"action":"create","scope":"project","type":"project","name":"auto-memory","description":"自动记录的项目事实","body":"自动记录正文","confidence":"high"}],"user_agents":[],"project_agents_suggestions":[]}`)
			return
		}
		n := tasks.Add(1)
		terminalAnswer(w, fmt.Sprintf("DONE-%d", n))
	}))
}

func waitForPath(t *testing.T, pattern string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		matches, _ := filepath.Glob(pattern)
		if len(matches) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待文件超时：%s", pattern)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTerminalMemoryReviewAndView(t *testing.T) {
	var tasks, reviews atomic.Int32
	server := memoryReviewServer(&tasks, &reviews)
	defer server.Close()
	p := startTerminalMode(t, server.URL, "allow", "JOHNNY_MEMORY_AUTO=on")

	end := len(p.text())
	p.write(t, "first\r")
	end = p.expect(t, end, "DONE-1")
	end = p.expect(t, end, "空闲")
	waitForPath(t, filepath.Join(p.home, ".johnny", "projects", "*", "memory", "auto-memory.md"))
	if reviews.Load() == 0 {
		t.Fatal("未触发轮末记忆审查")
	}

	p.write(t, "second\r")
	end = p.expect(t, end, "已自动记录 1 条长期记忆")
	end = p.expect(t, end, "DONE-2")
	end = p.expect(t, end, "空闲")

	p.write(t, "/memory\r")
	end = p.expect(t, end, "用户记忆索引")
	p.expect(t, end, "auto-memory")

	p.write(t, "/exit\r")
	p.wait(t, 0)
	if _, err := os.Stat(filepath.Join(p.home, ".johnny", "projects")); err != nil {
		t.Fatalf("记忆目录未创建：%v", err)
	}
}
