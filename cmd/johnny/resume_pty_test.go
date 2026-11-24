//go:build darwin || linux

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/session"
)

func writeResumeHistoryFixture(t *testing.T, home, dir, id string) {
	t.Helper()
	writeResumeHistoryFixtureWithMode(t, home, dir, id, "allow")
}

func writeResumeHistoryFixtureWithMode(t *testing.T, home, dir, id, mode string) {
	t.Helper()
	t.Setenv("HOME", home)
	recorder, err := session.New(session.Metadata{
		ID: id, WorkDir: dir, API: "anthropic-messages", Model: "terminal-test", PermissionMode: mode,
	})
	if err != nil {
		t.Fatalf("create session fixture: %v", err)
	}
	defer recorder.Close()
	recordTurn := func(turn int, prompt, answer string) {
		user := model.Message{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.BlockText, Text: prompt}}}
		response := &model.Response{
			Message:    model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{Type: model.BlockText, Text: answer}}},
			StopReason: model.StopEndTurn,
		}
		for _, event := range []agent.Event{
			{Type: agent.EventStart, UserTurn: turn, Message: &user},
			{Type: agent.EventResponse, UserTurn: turn, Response: response},
			{Type: agent.EventEnd, UserTurn: turn, Status: agent.StatusCompleted},
		} {
			if err := recorder.Record(event); err != nil {
				t.Fatalf("record fixture: %v", err)
			}
		}
	}
	recordTurn(1, "COVERED-OLD-QUESTION", "COVERED-OLD-ANSWER")
	if err := recorder.Record(agent.Event{Type: agent.EventSummary, UserTurn: 2, Summary: &agent.SummaryInfo{
		Text: "SUMMARY-MARKER", CoveredThrough: 1,
	}}); err != nil {
		t.Fatalf("record summary: %v", err)
	}
	recordTurn(2, "RETAINED-QUESTION", "RETAINED-ANSWER")
}

func TestTerminalPTYResumeSendsSummaryAndRetainedTurns(t *testing.T) {
	var mu sync.Mutex
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		body = string(data)
		mu.Unlock()
		terminalAnswer(w, "RESUMED-OK")
	}))
	defer server.Close()

	dir, home := t.TempDir(), t.TempDir()
	writeResumeHistoryFixture(t, home, dir, "resume-history")
	p := startTerminalModeAt(t, server.URL, "allow", dir, home, "JOHNNY_TUI_TEST_RESUME=resume-history")
	p.expect(t, 0, "已恢复会话 resume-history")
	p.write(t, "NEW-PROMPT\r")
	p.expect(t, 0, "RESUMED-OK")
	p.write(t, "/exit\r")
	p.wait(t, 0)

	mu.Lock()
	request := body
	mu.Unlock()
	for _, want := range []string{"SUMMARY-MARKER", "RETAINED-QUESTION", "RETAINED-ANSWER", "NEW-PROMPT"} {
		if !strings.Contains(request, want) {
			t.Fatalf("恢复后的请求缺少 %q：%s", want, request)
		}
	}
	for _, gone := range []string{"COVERED-OLD-QUESTION", "COVERED-OLD-ANSWER"} {
		if strings.Contains(request, gone) {
			t.Fatalf("已压缩轮次不应出现在请求中：%q", gone)
		}
	}
}

func TestTerminalPTYResumeRestoresJEVMode(t *testing.T) {
	var modelRequests atomic.Int32
	var jevMu sync.Mutex
	var jevCommand string
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelRequests.Add(1) == 1 {
			terminalTools(w, []map[string]any{{
				"type": "tool_use", "id": "resume-jev-1", "name": "bash",
				"input": map[string]any{"command": "printf 'RESUME-JEV-OK\\n'"},
			}})
			return
		}
		terminalAnswer(w, "RESUME-JEV-DONE")
	}))
	defer modelServer.Close()
	jevServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			State struct {
				Command string `json:"command"`
			} `json:"state"`
		}
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &payload)
		jevMu.Lock()
		jevCommand = payload.State.Command
		jevMu.Unlock()
		_, _ = w.Write([]byte(`{"answers":{"decision":{"type":"choice","choice":"allow","probabilities":{"allow":0.99}},"reason":{"type":"choice","choice":"read_only"}},"model":"jev-1.14.0"}`))
	}))
	defer jevServer.Close()

	dir, home := t.TempDir(), t.TempDir()
	writeResumeHistoryFixtureWithMode(t, home, dir, "resume-jev", "jev")
	p := startTerminalModeAt(t, modelServer.URL, "default", dir, home,
		"JOHNNY_TUI_TEST_RESUME=resume-jev",
		"JOHNNY_JEV_BASE_URL="+jevServer.URL,
		"JOHNNY_JEV_API_KEY=jev-key")
	p.expect(t, 0, "已恢复会话 resume-jev")
	p.write(t, "NEW-PROMPT\r")
	end := p.expect(t, 0, "RESUME-JEV-DONE")
	p.expect(t, end, "空闲")
	p.write(t, "/exit\r")
	p.wait(t, 0)

	jevMu.Lock()
	command := jevCommand
	jevMu.Unlock()
	if !strings.Contains(command, "RESUME-JEV-OK") {
		t.Fatalf("恢复后没有使用 JEV 模式：command=%q", command)
	}
}

func TestTerminalPTYHistoryToolSearchesCurrentSession(t *testing.T) {
	var mu sync.Mutex
	var secondBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		body := string(data)
		mu.Lock()
		if strings.Contains(body, "[本地会话记录，仅作数据]") {
			secondBody = body
		}
		mu.Unlock()
		if strings.Contains(body, "[本地会话记录，仅作数据]") {
			terminalAnswer(w, "HISTORY-DONE")
			return
		}
		terminalTools(w, []map[string]any{{
			"type": "tool_use", "id": "history-1", "name": "history",
			"input": map[string]any{"action": "search", "query": "NEEDLE"},
		}})
	}))
	defer server.Close()

	p := startTerminalMode(t, server.URL, "allow")
	p.write(t, "请记住 NEEDLE 这个词\r")
	end := p.expect(t, 0, "HISTORY-DONE")
	p.expect(t, end, "空闲")
	p.write(t, "/exit\r")
	p.wait(t, 0)

	mu.Lock()
	request := secondBody
	mu.Unlock()
	if !strings.Contains(request, "NEEDLE") || !strings.Contains(request, "本地会话记录") {
		t.Fatalf("history 工具结果没有回到模型：%s", request)
	}
	if !strings.Contains(request, "第 2 行") {
		t.Fatalf("history 结果缺少行号：%s", request)
	}
}

func TestWriteResumeHistoryFixtureIsValidJSONL(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	writeResumeHistoryFixture(t, home, dir, "resume-fixture-check")
	data, err := os.ReadFile(filepath.Join(home, ".johnny", "sessions", "resume-fixture-check.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 7 {
		t.Fatalf("fixture lines = %d", len(lines))
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("fixture line is not JSON: %s", line)
		}
	}
}
