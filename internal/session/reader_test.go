package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

func readerHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func readerRecorder(t *testing.T, id string) *Recorder {
	t.Helper()
	recorder, err := New(Metadata{
		ID:             id,
		WorkDir:        "/workspace/project",
		API:            "anthropic-messages",
		Model:          "test-model",
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return recorder
}

func recordUserTurn(t *testing.T, recorder *Recorder, userTurn int, prompt, answer string, status agent.Status) {
	t.Helper()
	user := model.Message{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.BlockText, Text: prompt}}}
	if err := recorder.Record(agent.Event{Type: agent.EventStart, UserTurn: userTurn, Message: &user}); err != nil {
		t.Fatalf("record start: %v", err)
	}
	if answer != "" {
		response := &model.Response{
			Message:    model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{Type: model.BlockText, Text: answer}}},
			StopReason: model.StopEndTurn,
		}
		if err := recorder.Record(agent.Event{Type: agent.EventResponse, UserTurn: userTurn, Response: response}); err != nil {
			t.Fatalf("record response: %v", err)
		}
	}
	if err := recorder.Record(agent.Event{Type: agent.EventEnd, UserTurn: userTurn, Status: status}); err != nil {
		t.Fatalf("record end: %v", err)
	}
}

func TestListAndResumeCompleteSession(t *testing.T) {
	readerHome(t)
	recorder := readerRecorder(t, "session-one")
	recordUserTurn(t, recorder, 1, "给 calc 增加 pow 命令", "已完成。", agent.StatusCompleted)
	if err := recorder.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	infos, err := List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("List() = %d sessions, want 1", len(infos))
	}
	info := infos[0]
	if info.ID != "session-one" || info.Turns != 1 || info.Incomplete {
		t.Fatalf("unexpected info: %+v", info)
	}
	if info.Title != "给 calc 增加 pow 命令" {
		t.Fatalf("title = %q", info.Title)
	}
	if info.WorkDir != "/workspace/project" || info.Model != "test-model" || info.PermissionMode != "default" {
		t.Fatalf("metadata = %+v", info)
	}

	state, err := LoadResume("session-one")
	if err != nil {
		t.Fatalf("LoadResume() error = %v", err)
	}
	defer state.Close()
	if state.UserTurn != 1 || state.LastStatus != agent.StatusCompleted || state.Dropped {
		t.Fatalf("state = %+v", state)
	}
	if len(state.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(state.Steps))
	}
	if state.Steps[0].Message.Role != model.RoleUser || state.Steps[1].Message.Role != model.RoleAssistant {
		t.Fatalf("roles = %v, %v", state.Steps[0].Message.Role, state.Steps[1].Message.Role)
	}
}

func TestResumeUsesSummaryAndDropsCoveredTurns(t *testing.T) {
	readerHome(t)
	recorder := readerRecorder(t, "session-two")
	recordUserTurn(t, recorder, 1, "第一轮", "回答一", agent.StatusCompleted)
	summary := &agent.SummaryInfo{Text: "第一轮的摘要", Before: 5000, After: 200, CoveredThrough: 1}
	if err := recorder.Record(agent.Event{Type: agent.EventSummary, UserTurn: 2, Summary: summary}); err != nil {
		t.Fatalf("record summary: %v", err)
	}
	recordUserTurn(t, recorder, 2, "第二轮", "回答二", agent.StatusCompleted)
	if err := recorder.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	state, err := LoadResume("session-two")
	if err != nil {
		t.Fatalf("LoadResume() error = %v", err)
	}
	defer state.Close()
	if state.Summary == nil || state.Summary.Text != "第一轮的摘要" || state.Summary.CoveredThrough != 1 {
		t.Fatalf("summary = %+v", state.Summary)
	}
	if state.UserTurn != 2 {
		t.Fatalf("userTurn = %d, want 2", state.UserTurn)
	}
	if len(state.Steps) != 2 {
		t.Fatalf("steps = %d, want only turn 2", len(state.Steps))
	}
	for _, step := range state.Steps {
		if step.UserTurn != 2 {
			t.Fatalf("kept turn %d, want only 2", step.UserTurn)
		}
	}
	if text := firstText(t, state.Steps[0].Message); text != "第二轮" {
		t.Fatalf("first retained text = %q", text)
	}
	if len(state.Recorder.Path()) == 0 {
		t.Fatal("resume recorder has no path")
	}
}

func TestResumeDropsIncompleteTail(t *testing.T) {
	readerHome(t)
	recorder := readerRecorder(t, "session-three")
	recordUserTurn(t, recorder, 1, "第一轮", "回答一", agent.StatusCompleted)
	start := model.Message{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.BlockText, Text: "未完成"}}}
	if err := recorder.Record(agent.Event{Type: agent.EventStart, UserTurn: 2, Message: &start}); err != nil {
		t.Fatalf("record start: %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	state, err := LoadResume("session-three")
	if err != nil {
		t.Fatalf("LoadResume() error = %v", err)
	}
	defer state.Close()
	if !state.Dropped {
		t.Fatal("Dropped = false, want true")
	}
	if state.UserTurn != 1 || len(state.Steps) != 2 {
		t.Fatalf("state = userTurn %d, steps %d", state.UserTurn, len(state.Steps))
	}
}

func TestResumeIgnoresCorruptTrailingLine(t *testing.T) {
	home := readerHome(t)
	recorder := readerRecorder(t, "session-four")
	recordUserTurn(t, recorder, 1, "第一轮", "回答一", agent.StatusCompleted)
	if err := recorder.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	path := filepath.Join(home, ".johnny", "sessions", "session-four.jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := file.WriteString(`{"type":"start","user_turn":2`); err != nil {
		t.Fatalf("write partial line: %v", err)
	}
	_ = file.Close()

	state, err := LoadResume("session-four")
	if err != nil {
		t.Fatalf("LoadResume() error = %v", err)
	}
	defer state.Close()
	if !state.Dropped {
		t.Fatal("Dropped = false, want true")
	}
	if len(state.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(state.Steps))
	}
}

func TestResumeRejectsSessionWithoutEnd(t *testing.T) {
	readerHome(t)
	recorder := readerRecorder(t, "session-five")
	user := model.Message{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.BlockText, Text: "没有结束"}}}
	if err := recorder.Record(agent.Event{Type: agent.EventStart, UserTurn: 1, Message: &user}); err != nil {
		t.Fatalf("record start: %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := LoadResume("session-five"); !errors.Is(err, ErrNoCompleteTurn) {
		t.Fatalf("LoadResume() error = %v, want ErrNoCompleteTurn", err)
	}
}

func TestListSkipsChildSessionsAndSortsByModified(t *testing.T) {
	home := readerHome(t)
	first := readerRecorder(t, "session-old")
	recordUserTurn(t, first, 1, "旧会话", "ok", agent.StatusCompleted)
	_ = first.Close()

	second := readerRecorder(t, "session-new")
	recordUserTurn(t, second, 1, "新会话", "ok", agent.StatusCompleted)
	_ = second.Close()

	child, err := New(Metadata{
		ID: "session-parent-sub-ab12", WorkDir: "/workspace/project", API: "anthropic-messages",
		Model: "test-model", ParentID: "session-parent", SubagentID: "ab12",
	})
	if err != nil {
		t.Fatalf("New child: %v", err)
	}
	_ = child.Close()

	dir := filepath.Join(home, ".johnny", "sessions")
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "session-old.jsonl"), oldTime, oldTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	infos, err := ListDir(dir)
	if err != nil {
		t.Fatalf("ListDir() error = %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("ListDir() = %d sessions, want 2: %+v", len(infos), infos)
	}
	if infos[0].ID != "session-new" || infos[1].ID != "session-old" {
		t.Fatalf("order = %s, %s", infos[0].ID, infos[1].ID)
	}
}

func TestResumeRestoresToolCallAndResult(t *testing.T) {
	readerHome(t)
	recorder := readerRecorder(t, "session-tools")
	user := model.Message{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.BlockText, Text: "读文件"}}}
	call := model.ToolCall{ID: "call-1", Name: "read", Arguments: []byte(`{"path":"a.txt"}`)}
	response := &model.Response{
		Message: model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
			Type: model.BlockToolCall, ToolCall: &call,
		}}},
		StopReason: model.StopToolUse,
	}
	result := model.ToolResult{ToolCallID: "call-1", Content: "文件内容"}
	for _, event := range []agent.Event{
		{Type: agent.EventStart, UserTurn: 1, Message: &user},
		{Type: agent.EventResponse, UserTurn: 1, Response: response},
		{Type: agent.EventToolEnd, UserTurn: 1, Call: &call, ToolResult: &result},
		{Type: agent.EventEnd, UserTurn: 1, Status: agent.StatusCompleted},
	} {
		if err := recorder.Record(event); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	_ = recorder.Close()

	state, err := LoadResume("session-tools")
	if err != nil {
		t.Fatalf("LoadResume() error = %v", err)
	}
	defer state.Close()
	if len(state.Steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(state.Steps))
	}
	toolStep := state.Steps[2]
	if toolStep.Call == nil || toolStep.Call.Name != "read" || !bytesEqual(toolStep.Call.Arguments, `{"path":"a.txt"}`) {
		t.Fatalf("call = %+v", toolStep.Call)
	}
	if toolStep.Message.Role != model.RoleTool || toolStep.Message.Content[0].ToolResult.Content != "文件内容" {
		t.Fatalf("tool message = %+v", toolStep.Message)
	}
}

func TestResumeRejectsConcurrentUse(t *testing.T) {
	readerHome(t)
	recorder := readerRecorder(t, "session-locked")
	recordUserTurn(t, recorder, 1, "第一轮", "回答一", agent.StatusCompleted)
	_ = recorder.Close()

	first, err := LoadResume("session-locked")
	if err != nil {
		t.Fatalf("first LoadResume() error = %v", err)
	}
	defer first.Close()
	if _, err := LoadResume("session-locked"); !errors.Is(err, ErrSessionInUse) {
		t.Fatalf("second LoadResume() error = %v, want ErrSessionInUse", err)
	}
}

func firstText(t *testing.T, message model.Message) string {
	t.Helper()
	for _, block := range message.Content {
		if block.Type == model.BlockText {
			return block.Text
		}
	}
	return ""
}

func bytesEqual(value []byte, want string) bool {
	return string(value) == want
}

func TestAuditRecordAfterEndDoesNotMarkIncomplete(t *testing.T) {
	readerHome(t)
	recorder := readerRecorder(t, "session-audit")
	recordUserTurn(t, recorder, 1, "第一轮", "回答", agent.StatusCompleted)
	if err := recorder.RecordMemory(MemoryRecord{Scope: "user_memory", Action: "create", Name: "note", Path: "/tmp/note.md"}); err != nil {
		t.Fatalf("record memory: %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	infos, err := List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(infos) != 1 || infos[0].Incomplete {
		t.Fatalf("审计记录不应把会话标成未完成：%+v", infos)
	}

	state, err := LoadResume("session-audit")
	if err != nil {
		t.Fatalf("LoadResume() error = %v", err)
	}
	defer state.Close()
	if state.Dropped {
		t.Fatal("审计记录不应被当成未完成尾部")
	}
	path, err := LogPath("session-audit")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"type":"memory_write"`) {
		t.Fatal("恢复不应截断 end 之后的审计记录")
	}
}
