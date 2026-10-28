//go:build darwin || linux

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// subagentServer 扮演模型：主 Agent 第一次请求派出子任务，之后分别回复“已派出”和“汇总”；
// 子 Agent 的请求交给 child 处理。
func subagentServer(taskInput map[string]any, child func(w http.ResponseWriter, r *http.Request, body string)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		body := string(data)
		switch {
		case strings.Contains(body, "你是 Johnny-Code 派出的后台子 Agent"):
			child(w, r, body)
		case strings.Contains(body, "仅作数据"):
			terminalAnswer(w, "SUMMARY-DONE")
		case strings.Contains(body, "子任务已启动"):
			terminalAnswer(w, "DISPATCHED")
		default:
			terminalTools(w, []map[string]any{{"type": "tool_use", "id": "task-1", "name": "task", "input": taskInput}})
		}
	}))
}

var freshReadTask = map[string]any{"description": "查日志", "prompt": "检查日志找出原因", "kind": "fresh"}

// holdChild 让子任务的模型请求一直挂起，直到 release 关闭或请求被取消。
func holdChild(release chan struct{}, answer string) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, _ string) {
		select {
		case <-release:
			terminalAnswer(w, answer)
		case <-r.Context().Done():
		}
	}
}

func sessionLogs(t *testing.T, home string) []string {
	t.Helper()
	logs, err := filepath.Glob(filepath.Join(home, ".johnny", "sessions", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return logs
}

func TestTerminalPTYSubagentRunsInBackgroundAndWakesMainAgent(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	closeRelease := func() { once.Do(func() { close(release) }) }
	t.Cleanup(closeRelease)
	server := subagentServer(freshReadTask, holdChild(release, "CHILD-FINDING-X"))
	defer server.Close()
	p := startTerminalMode(t, server.URL, "allow")

	p.write(t, "请派出调查\r")
	end := p.expect(t, 0, "DISPATCHED")
	end = p.expect(t, end, "运行中")
	closeRelease()
	end = p.expect(t, end, "已完成")
	p.expect(t, end, "SUMMARY-DONE")

	p.write(t, "/exit\r")
	p.wait(t, 0)
	logs := sessionLogs(t, p.home)
	if len(logs) != 2 {
		t.Fatalf("应有主会话和一个子任务会话记录：%v", logs)
	}
	var parent, child string
	for _, path := range logs {
		data, _ := os.ReadFile(path)
		switch {
		case strings.Contains(string(data), `"type":"subagent_start"`):
			parent = string(data)
		case strings.Contains(string(data), `"parent_id"`):
			child = string(data)
		}
	}
	for _, want := range []string{`"type":"subagent_end"`, `"status":"completed"`, `"injected":true`, `"sources":["subagent:`} {
		if !strings.Contains(parent, want) {
			t.Fatalf("主会话记录缺少 %s：%s", want, parent)
		}
	}
	if !strings.Contains(child, `"subagent_id"`) || !strings.Contains(child, "CHILD-FINDING-X") {
		t.Fatalf("子任务会话记录缺少关联或过程：%s", child)
	}
}

func TestTerminalPTYExitCancelsRunningSubagents(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	server := subagentServer(freshReadTask, holdChild(release, "NEVER"))
	defer server.Close()
	p := startTerminalMode(t, server.URL, "allow")

	p.write(t, "请派出调查\r")
	end := p.expect(t, 0, "DISPATCHED")
	end = p.expect(t, end, "运行中")
	p.write(t, "/exit\r")
	p.expect(t, end, "1 个子任务将被取消")
	p.wait(t, 0)
	if strings.Contains(p.text(), "SUMMARY-DONE") {
		t.Fatal("被取消的子任务不应注入或唤醒主 Agent")
	}
}

func TestTerminalPTYCtrlCCancelsSubagentsWhenIdle(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	server := subagentServer(freshReadTask, holdChild(release, "NEVER"))
	defer server.Close()
	p := startTerminalMode(t, server.URL, "allow")

	p.write(t, "请派出调查\r")
	end := p.expect(t, 0, "DISPATCHED")
	end = p.expect(t, end, "运行中")
	p.write(t, "\x03")
	end = p.expect(t, end, "已取消")
	p.write(t, "/exit\r")
	p.wait(t, 0)
	if strings.Contains(p.text(), "SUMMARY-DONE") {
		t.Fatal("被取消的子任务不应唤醒主 Agent")
	}
}

func TestTerminalPTYWakeWaitsUntilInputIsEmpty(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	closeRelease := func() { once.Do(func() { close(release) }) }
	t.Cleanup(closeRelease)
	server := subagentServer(freshReadTask, holdChild(release, "CHILD-FINDING-X"))
	defer server.Close()
	p := startTerminalMode(t, server.URL, "allow")

	p.write(t, "请派出调查\r")
	end := p.expect(t, 0, "DISPATCHED")
	end = p.expect(t, end, "运行中")
	p.write(t, "x")
	closeRelease()
	end = p.expect(t, end, "已完成")
	time.Sleep(600 * time.Millisecond)
	if strings.Contains(p.text()[end:], "SUMMARY-DONE") {
		t.Fatal("用户正在输入时不应抢占唤醒主 Agent")
	}
	p.write(t, "\x03")
	p.expect(t, end, "SUMMARY-DONE")
	p.write(t, "/exit\r")
	p.wait(t, 0)
}

func TestTerminalPTYSubagentApprovalShowsOriginWhileMainIsIdle(t *testing.T) {
	task := map[string]any{"description": "建目录", "prompt": "建立目录", "kind": "fresh", "access": "write", "paths": []string{"newdir"}}
	server := subagentServer(task, func(w http.ResponseWriter, r *http.Request, body string) {
		if strings.Contains(body, "tool_result") {
			terminalAnswer(w, "CHILD-DONE")
			return
		}
		terminalTools(w, []map[string]any{{"type": "tool_use", "id": "child-bash", "name": "bash", "input": map[string]any{"command": "mkdir newdir"}}})
	})
	defer server.Close()
	p := startTerminalMode(t, server.URL, "default")

	p.write(t, "请派出建目录\r")
	end := p.expect(t, 0, "DISPATCHED")
	end = p.expect(t, end, "权限确认")
	end = p.expect(t, end, "来源：子任务")
	p.expect(t, end, "mkdir newdir")
	p.write(t, "k\r")
	end = p.expect(t, end, "已完成")
	p.expect(t, end, "SUMMARY-DONE")
	if info, err := os.Stat(filepath.Join(p.dir, "newdir")); err != nil || !info.IsDir() {
		t.Fatalf("批准后子任务的命令应执行：%v", err)
	}
	p.write(t, "/exit\r")
	p.wait(t, 0)
	var parent string
	for _, path := range sessionLogs(t, p.home) {
		if data, _ := os.ReadFile(path); strings.Contains(string(data), `"type":"subagent_start"`) {
			parent = string(data)
		}
	}
	for _, want := range []string{`"type":"permission"`, `"origin":"`, `"decision":"allow"`, `"source":"tui"`} {
		if !strings.Contains(parent, want) {
			t.Fatalf("主会话记录缺少 %s：%s", want, parent)
		}
	}
}
