//go:build darwin || linux

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func workflowCall(id string, input map[string]any) []map[string]any {
	return []map[string]any{{"type": "tool_use", "id": id, "name": "workflow", "input": input}}
}

func workflowTask(description string) map[string]any {
	return map[string]any{"description": description, "prompt": "检查" + description, "kind": "fresh"}
}

// workflowServer 扮演模型：主 Agent 先声明两步大纲，再分别启动第 1 步（两个任务）和第 2 步（一个任务），
// 每一步结束的整步消息到达后继续推进，最后汇总；子 Agent 的请求交给 child 处理。
func workflowServer(child func(w http.ResponseWriter, r *http.Request, body string)) *httptest.Server {
	start := map[string]any{"action": "start", "title": "演示", "steps": []map[string]any{{"title": "调研", "goal": "看一看"}, {"title": "收尾", "goal": "总结"}}}
	runOne := map[string]any{"action": "run", "step": 1, "tasks": []map[string]any{workflowTask("甲"), workflowTask("乙")}}
	runTwo := map[string]any{"action": "run", "step": 2, "tasks": []map[string]any{workflowTask("丙")}}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		body := string(data)
		switch {
		case strings.Contains(body, "你是 Johnny-Code 派出的后台子 Agent"):
			child(w, r, body)
		case strings.Contains(body, "所有 2 步都已结束"):
			terminalAnswer(w, "SUMMARY-DONE")
		case strings.Contains(body, "第 2 步「收尾」已启动"):
			terminalAnswer(w, "DISPATCHED-2")
		case strings.Contains(body, "下一步：第 2 步"):
			terminalTools(w, workflowCall("wf-run-2", runTwo))
		case strings.Contains(body, "第 1 步「调研」已启动"):
			terminalAnswer(w, "DISPATCHED-1")
		case strings.Contains(body, "已登记"):
			terminalTools(w, workflowCall("wf-run-1", runOne))
		default:
			terminalTools(w, workflowCall("wf-start", start))
		}
	}))
}

func TestTerminalPTYWorkflowRunsStepsAndWakesMainAgent(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	closeRelease := func() { once.Do(func() { close(release) }) }
	t.Cleanup(closeRelease)
	server := workflowServer(holdChild(release, "CHILD-OK"))
	defer server.Close()
	p := startTerminalMode(t, server.URL, "allow")

	p.write(t, "请用工作流\r")
	end := p.expect(t, 0, "DISPATCHED-1")
	end = p.expect(t, end, "workflow 演示")
	end = p.expect(t, end, "运行中")
	closeRelease()
	end = p.expect(t, end, "第 1/2 步「调研」已结束：2 个任务，全部完成")
	for _, want := range []string{"DISPATCHED-2", "第 2/2 步「收尾」已结束", "workflow「演示」已完成：2 步全部结束", "SUMMARY-DONE"} {
		p.expect(t, end, want)
	}

	p.write(t, "/exit\r")
	p.wait(t, 0)
	logs := sessionLogs(t, p.home)
	if len(logs) != 4 {
		t.Fatalf("应有主会话和三个子任务的会话记录：%v", logs)
	}
	var parent string
	children := 0
	for _, path := range logs {
		data, _ := os.ReadFile(path)
		switch {
		case strings.Contains(string(data), `"type":"workflow_start"`):
			parent = string(data)
		case strings.Contains(string(data), `"workflow_step":`):
			children++
		}
	}
	for _, want := range []string{`"type":"workflow_step_start"`, `"type":"workflow_step_end"`, `"type":"workflow_end"`, `"status":"completed"`, `"sources":["workflow:step-1"]`, `"step":1`} {
		if !strings.Contains(parent, want) {
			t.Fatalf("主会话记录缺少 %s：%s", want, parent)
		}
	}
	if children != 3 {
		t.Fatalf("三个子任务的会话记录都应标明所属步骤：%d", children)
	}
}

func TestTerminalPTYCtrlCCancelsWorkflowWithoutWaking(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	server := workflowServer(holdChild(release, "NEVER"))
	defer server.Close()
	p := startTerminalMode(t, server.URL, "allow")

	p.write(t, "请用工作流\r")
	end := p.expect(t, 0, "DISPATCHED-1")
	end = p.expect(t, end, "运行中")
	p.write(t, "\x03")
	end = p.expect(t, end, "workflow「演示」已取消：完成 0/2 步，其余未执行")
	p.write(t, "/exit\r")
	p.wait(t, 0)
	if text := p.text(); strings.Contains(text, "SUMMARY-DONE") || strings.Contains(text, "步已结束") {
		t.Fatalf("取消后不应有步骤结果或唤醒主 Agent：%s", text)
	}
}
