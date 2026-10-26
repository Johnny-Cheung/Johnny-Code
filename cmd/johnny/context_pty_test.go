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
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

const simBigCommand = `for i in $(seq 1 1200); do printf "%0100d\n" "$i"; done`
const simSmallCommand = `for i in $(seq 1 100); do printf "%0100d\n" "$i"; done`

func contextSimulationServer(t *testing.T, tasks, summaries *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("上下文压缩器")) || bytes.Contains(body, []byte("请根据上面的对话历史生成压缩摘要")) {
			n := summaries.Add(1)
			terminalAnswer(w, fmt.Sprintf("<analysis>模拟整理</analysis><summary>SIM-SUMMARY-%d</summary>", n))
			return
		}
		n := int(tasks.Add(1))
		turn := (n + 1) / 2
		if n%2 == 1 {
			command := simSmallCommand
			if turn == 1 {
				command = simBigCommand
			}
			terminalTools(w, []map[string]any{{
				"type": "tool_use", "id": fmt.Sprintf("sim-tool-%d", n), "name": "bash",
				"input": map[string]any{"command": command},
			}})
			return
		}
		terminalAnswer(w, fmt.Sprintf("DONE-%d", turn))
	}))
}

func runContextTurns(t *testing.T, p *terminalProcess, count int) int {
	t.Helper()
	end := len(p.text())
	for turn := 1; turn <= count; turn++ {
		p.write(t, fmt.Sprintf("turn-%d\r", turn))
		end = p.expect(t, end, fmt.Sprintf("DONE-%d", turn))
		// 等界面回到空闲态再发送下一轮，避免高负载下输入落在收尾阶段被忽略。
		end = p.expect(t, end, "空闲")
	}
	return end
}

func readSessionLog(t *testing.T, home string) []byte {
	t.Helper()
	logs, err := filepath.Glob(filepath.Join(home, ".johnny", "sessions", "*.jsonl"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("会话日志数量错误：%v %v", logs, err)
	}
	data, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func lastMessageTokens(t *testing.T, text string) int {
	t.Helper()
	matches := regexp.MustCompile(`消息 (\d+)`).FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		t.Fatalf("/context 缺少消息占用：%s", text)
	}
	value, err := strconv.Atoi(matches[len(matches)-1][1])
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestTerminalContextPreviewAndManualCompact(t *testing.T) {
	var tasks, summaries atomic.Int32
	server := contextSimulationServer(t, &tasks, &summaries)
	defer server.Close()
	p := startTerminalMode(t, server.URL, "allow", "JOHNNY_CONTEXT_WINDOW=1000000")

	end := runContextTurns(t, p, 6)
	if !strings.Contains(p.text(), "本轮工具输出已轮级压缩") {
		t.Fatal("未看到轮级压缩提示")
	}
	artifacts, _ := filepath.Glob(filepath.Join(p.dir, ".johnny", "artifacts", "*", "turn-*", "call-*.log"))
	if len(artifacts) == 0 {
		t.Fatalf("未生成工具输出存档：%v", artifacts)
	}
	artifact := artifacts[0]

	p.write(t, "/context\r")
	end = p.expect(t, end, "上下文估算")
	p.expect(t, end, "工具输出存档")
	messageTokens := lastMessageTokens(t, p.text())
	if messageTokens == 0 || messageTokens >= 20_000 {
		t.Fatalf("大工具输出未按预览进入上下文：%d token", messageTokens)
	}

	p.write(t, "/compact\r")
	end = p.expect(t, end, "上下文已压缩")
	p.write(t, "/context\r")
	end = p.expect(t, end, "当前摘要")
	p.expect(t, end, "SIM-SUMMARY")

	p.write(t, "/exit\r")
	p.wait(t, 0)
	if summaries.Load() == 0 {
		t.Fatal("手动压缩未请求摘要")
	}
	if _, err := os.Stat(artifact); !os.IsNotExist(err) {
		t.Fatalf("退出后存档未清理：%v", err)
	}
	remaining, _ := filepath.Glob(filepath.Join(p.dir, ".johnny", "artifacts", "*"))
	if len(remaining) != 0 {
		t.Fatalf("退出后仍有会话存档：%v", remaining)
	}
	log := readSessionLog(t, p.home)
	for _, want := range []string{`"artifact":`, `"summary":`, `"notice":`} {
		if !bytes.Contains(log, []byte(want)) {
			t.Fatalf("JSONL 缺少 %s：%s", want, log)
		}
	}
}

func TestTerminalAutoSummary(t *testing.T) {
	var tasks, summaries atomic.Int32
	server := contextSimulationServer(t, &tasks, &summaries)
	defer server.Close()
	p := startTerminalMode(t, server.URL, "allow", "JOHNNY_CONTEXT_WINDOW=32768")

	end := runContextTurns(t, p, 7)
	if summaries.Load() == 0 {
		t.Fatalf("小窗口未触发自动摘要；任务请求 %d 次", tasks.Load())
	}
	if !strings.Contains(p.text(), "上下文摘要已更新") {
		t.Fatal("未看到自动摘要提示")
	}
	p.write(t, "/context\r")
	end = p.expect(t, end, "当前摘要")
	p.expect(t, end, "SIM-SUMMARY")
	p.write(t, "/exit\r")
	p.wait(t, 0)
	log := readSessionLog(t, p.home)
	if !bytes.Contains(log, []byte(`"summary":`)) {
		t.Fatalf("JSONL 缺少摘要事件：%s", log)
	}
	if !bytes.Contains(log, []byte("SIM-SUMMARY")) {
		t.Fatalf("JSONL 摘要正文错误：%s", log)
	}
}
