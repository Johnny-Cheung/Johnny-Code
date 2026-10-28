package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

func int64Ptr(value int64) *int64 {
	return &value
}

func testHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func newTestRecorder(t *testing.T) *Recorder {
	t.Helper()
	testHome(t)
	recorder, err := New(Metadata{
		WorkDir:        "/workspace/project",
		API:            "anthropic-messages",
		Model:          "claude-test",
		PermissionMode: "read-only",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		_ = recorder.Close()
	})
	return recorder
}

func readJSONLines(t *testing.T, path string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session log: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("session log is empty")
	}
	if data[len(data)-1] != '\n' {
		t.Fatal("session log does not end with a newline")
	}
	parts := strings.Split(string(data[:len(data)-1]), "\n")
	lines := make([][]byte, len(parts))
	for i, part := range parts {
		if !json.Valid([]byte(part)) {
			t.Fatalf("line %d is not valid JSON: %q", i+1, part)
		}
		lines[i] = []byte(part)
	}
	return lines
}

func parseObject(t *testing.T, line []byte) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(line, &object); err != nil {
		t.Fatalf("decode JSON object: %v", err)
	}
	return object
}

func field(t *testing.T, object map[string]json.RawMessage, key string) json.RawMessage {
	t.Helper()
	value, ok := object[key]
	if !ok {
		t.Fatalf("missing field %q in %v", key, object)
	}
	return value
}

func stringField(t *testing.T, object map[string]json.RawMessage, key string) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(field(t, object, key), &value); err != nil {
		t.Fatalf("decode field %q: %v", key, err)
	}
	return value
}

func intField(t *testing.T, object map[string]json.RawMessage, key string) int {
	t.Helper()
	var value int
	if err := json.Unmarshal(field(t, object, key), &value); err != nil {
		t.Fatalf("decode field %q: %v", key, err)
	}
	return value
}

func assertRecordTime(t *testing.T, object map[string]json.RawMessage) {
	t.Helper()
	var recordedTime time.Time
	if err := json.Unmarshal(field(t, object, "time"), &recordedTime); err != nil {
		t.Fatalf("decode record time: %v", err)
	}
	if recordedTime.IsZero() {
		t.Fatal("record time is zero")
	}
}

func TestRecordUserTurnsAndSkippedTool(t *testing.T) {
	recorder := newTestRecorder(t)
	call := &model.ToolCall{
		ID:        "call_skipped",
		Name:      "read",
		Arguments: json.RawMessage(`{"path":"missing.txt"}`),
	}
	result := &model.ToolResult{ToolCallID: "call_skipped", Content: "未执行", IsError: true}
	userMessage := &model.Message{
		Role:    model.RoleUser,
		Content: []model.ContentBlock{{Type: model.BlockText, Text: "继续"}},
	}
	events := []agent.Event{
		{Type: agent.EventStart, UserTurn: 1, Message: userMessage},
		{Type: agent.EventToolStart, UserTurn: 2, Turn: 3, Call: call},
		{Type: agent.EventToolSkipped, UserTurn: 2, Turn: 3, Call: call, ToolResult: result, Error: "用户取消"},
		{Type: agent.EventEnd, UserTurn: 2, Turn: 3, Status: agent.StatusCanceled, Error: "canceled"},
	}
	for i, event := range events {
		if err := recorder.Record(event); err != nil {
			t.Fatalf("Record(event %d) error = %v", i, err)
		}
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	lines := readJSONLines(t, recorder.Path())
	wantTypes := []string{"meta", "start", "tool_start", "tool_skipped", "end"}
	if len(lines) != len(wantTypes) {
		t.Fatalf("got %d records, want %d: %q", len(lines), len(wantTypes), lines)
	}
	for i, want := range wantTypes {
		if got := stringField(t, parseObject(t, lines[i]), "type"); got != want {
			t.Fatalf("record %d type = %q, want %q", i, got, want)
		}
	}
	meta := parseObject(t, lines[0])
	if _, ok := meta["user_turn"]; ok {
		t.Fatal("metadata must not contain user_turn")
	}
	for i, wantUserTurn := range []int{1, 2, 2} {
		event := parseObject(t, lines[i+1])
		if got := intField(t, event, "user_turn"); got != wantUserTurn {
			t.Fatalf("record %d user_turn = %d, want %d", i+2, got, wantUserTurn)
		}
	}
	if got := intField(t, parseObject(t, lines[3]), "turn"); got != 3 {
		t.Fatalf("tool_skipped turn = %d, want 3", got)
	}
	skipped := parseObject(t, lines[3])
	if got := stringField(t, skipped, "error"); got != "用户取消" {
		t.Fatalf("tool_skipped error = %q", got)
	}
	var recordedCall struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		ArgumentsRaw string `json:"arguments_raw"`
	}
	if err := json.Unmarshal(field(t, skipped, "call"), &recordedCall); err != nil {
		t.Fatalf("decode tool_skipped call: %v", err)
	}
	if recordedCall.ID != "call_skipped" || recordedCall.Name != "read" || recordedCall.ArgumentsRaw != `{"path":"missing.txt"}` {
		t.Fatalf("tool_skipped call = %+v", recordedCall)
	}
	var recordedResult model.ToolResult
	if err := json.Unmarshal(field(t, skipped, "result"), &recordedResult); err != nil {
		t.Fatalf("decode tool_skipped result: %v", err)
	}
	if recordedResult.ToolCallID != "call_skipped" || recordedResult.Content != "未执行" || !recordedResult.IsError {
		t.Fatalf("tool_skipped result = %+v", recordedResult)
	}
}

func TestRecordPermission(t *testing.T) {
	recorder := newTestRecorder(t)
	record := PermissionRecord{
		ToolCallID: "call-1",
		Tool:       "bash",
		Target:     "git log --output=x",
		Decision:   "record",
		Source:     "tui",
		Reason:     "批准并记录",
		Prefixes:   [][]string{{"git", "log"}},
		Paths:      []PermissionPath{{Path: "/tmp/out", Kind: "file"}},
	}
	if err := recorder.RecordPermission(record); err != nil {
		t.Fatal(err)
	}
	lines := readJSONLines(t, recorder.Path())
	if len(lines) != 2 {
		t.Fatalf("权限记录数量错误：%d", len(lines))
	}
	object := parseObject(t, lines[1])
	for key, want := range map[string]string{
		"type": "permission", "tool_call_id": "call-1", "tool": "bash",
		"target": "git log --output=x", "decision": "record", "source": "tui",
	} {
		var got string
		if err := json.Unmarshal(object[key], &got); err != nil || got != want {
			t.Fatalf("%s = %q (%v)，want %q", key, got, err, want)
		}
	}
	var prefixes [][]string
	if err := json.Unmarshal(object["recorded_prefixes"], &prefixes); err != nil || len(prefixes) != 1 || len(prefixes[0]) != 2 || prefixes[0][0] != "git" {
		t.Fatalf("recorded_prefixes 异常：%v %v", prefixes, err)
	}
	var paths []PermissionPath
	if err := json.Unmarshal(object["recorded_paths"], &paths); err != nil || len(paths) != 1 || paths[0].Kind != "file" {
		t.Fatalf("recorded_paths 异常：%#v %v", paths, err)
	}
}

func TestRecorderWritesCompleteOrderedLog(t *testing.T) {
	recorder := newTestRecorder(t)

	source := model.Source{
		API:      model.AnthropicMessages,
		Provider: "anthropic",
		Model:    "claude-test",
	}
	userMessage := &model.Message{
		Role: model.RoleUser,
		Content: []model.ContentBlock{{
			Type: model.BlockText,
			Text: "检查项目",
		}},
	}
	call := &model.ToolCall{
		ID:        "call_1",
		Name:      "read",
		Arguments: json.RawMessage(`{"path":"README.md"}`),
	}
	response := &model.Response{
		Message: model.Message{
			Role:   model.RoleAssistant,
			Source: &source,
			Content: []model.ContentBlock{
				{
					Type: model.BlockText,
					Text: "我先读取文件",
					Replay: &model.Replay{
						Source: source,
						Data:   json.RawMessage(`{"type":"message","id":"msg_1"}`),
					},
				},
				{
					Type:     model.BlockToolCall,
					ToolCall: call,
				},
			},
		},
		StopReason: model.StopToolUse,
		Usage: model.Usage{
			InputTokens:      int64Ptr(120),
			OutputTokens:     int64Ptr(34),
			CacheReadTokens:  int64Ptr(5),
			CacheWriteTokens: int64Ptr(6),
			ReasoningTokens:  int64Ptr(7),
		},
	}
	toolResult := &model.ToolResult{
		ToolCallID: "call_1",
		Content:    "README content",
	}

	events := []agent.Event{
		{Type: agent.EventStart, Turn: 0, Message: userMessage},
		{Type: agent.EventRequestStart, Turn: 1},
		{
			Type:       agent.EventModel,
			Turn:       1,
			ModelEvent: &model.Event{Type: model.EventBlockDelta, BlockIndex: 0, Delta: "流式片段"},
		},
		{Type: agent.EventResponse, Turn: 1, Response: response},
		{Type: agent.EventToolStart, Turn: 1, Call: call},
		{Type: agent.EventToolEnd, Turn: 1, Call: call, ToolResult: toolResult},
		{Type: agent.EventEnd, Turn: 1, Status: agent.StatusFailed, Error: "tool failed"},
	}
	for i, event := range events {
		if err := recorder.Record(event); err != nil {
			t.Fatalf("Record(event %d) error = %v", i, err)
		}
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	lines := readJSONLines(t, recorder.Path())
	wantTypes := []string{"meta", "start", "request_start", "response", "tool_start", "tool_end", "end"}
	if len(lines) != len(wantTypes) {
		t.Fatalf("got %d records, want %d: %q", len(lines), len(wantTypes), lines)
	}
	for i, want := range wantTypes {
		got := stringField(t, parseObject(t, lines[i]), "type")
		if got != want {
			t.Fatalf("record %d type = %q, want %q", i, got, want)
		}
	}

	meta := parseObject(t, lines[0])
	id := stringField(t, meta, "id")
	if filepath.Base(recorder.Path()) != id+".jsonl" {
		t.Fatalf("file name = %q, want %q", filepath.Base(recorder.Path()), id+".jsonl")
	}
	if got := stringField(t, meta, "cwd"); got != "/workspace/project" {
		t.Fatalf("meta cwd = %q", got)
	}
	if got := stringField(t, meta, "api"); got != "anthropic-messages" {
		t.Fatalf("meta api = %q", got)
	}
	if got := stringField(t, meta, "model"); got != "claude-test" {
		t.Fatalf("meta model = %q", got)
	}
	if got := stringField(t, meta, "permission_mode"); got != "read-only" {
		t.Fatalf("meta permission_mode = %q", got)
	}
	assertRecordTime(t, meta)
	for key := range meta {
		switch key {
		case "type", "id", "time", "cwd", "api", "model", "permission_mode":
		default:
			t.Fatalf("unexpected metadata field %q", key)
		}
	}

	for i := 1; i < len(lines); i++ {
		event := parseObject(t, lines[i])
		assertRecordTime(t, event)
		_ = field(t, event, "turn")
	}
	if got := intField(t, parseObject(t, lines[1]), "turn"); got != 0 {
		t.Fatalf("start turn = %d, want 0", got)
	}

	var recordedStart struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(field(t, parseObject(t, lines[1]), "message"), &recordedStart); err != nil {
		t.Fatalf("decode start message: %v", err)
	}
	if recordedStart.Role != string(model.RoleUser) {
		t.Fatalf("start message role = %q", recordedStart.Role)
	}
	if len(recordedStart.Content) != 1 || recordedStart.Content[0].Type != string(model.BlockText) || recordedStart.Content[0].Text != "检查项目" {
		t.Fatalf("start message content = %+v", recordedStart.Content)
	}

	var recordedResponse struct {
		Message struct {
			Role    string        `json:"role"`
			Source  *model.Source `json:"source"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ToolCall *struct {
					ID           string `json:"id"`
					Name         string `json:"name"`
					ArgumentsRaw string `json:"arguments_raw"`
				} `json:"tool_call"`
				Replay *struct {
					Source  model.Source    `json:"source"`
					Data    json.RawMessage `json:"data"`
					DataRaw string          `json:"data_raw"`
				} `json:"replay"`
			} `json:"content"`
		} `json:"message"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens      *int64 `json:"input_tokens"`
			OutputTokens     *int64 `json:"output_tokens"`
			CacheReadTokens  *int64 `json:"cache_read_tokens"`
			CacheWriteTokens *int64 `json:"cache_write_tokens"`
			ReasoningTokens  *int64 `json:"reasoning_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(field(t, parseObject(t, lines[3]), "response"), &recordedResponse); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if recordedResponse.Message.Role != string(model.RoleAssistant) {
		t.Fatalf("response role = %q", recordedResponse.Message.Role)
	}
	if recordedResponse.Message.Source == nil || *recordedResponse.Message.Source != source {
		t.Fatalf("response source = %+v", recordedResponse.Message.Source)
	}
	if recordedResponse.StopReason != string(model.StopToolUse) {
		t.Fatalf("stop reason = %q", recordedResponse.StopReason)
	}
	if len(recordedResponse.Message.Content) != 2 {
		t.Fatalf("response content blocks = %d", len(recordedResponse.Message.Content))
	}
	textBlock := recordedResponse.Message.Content[0]
	if textBlock.Text != "我先读取文件" || textBlock.Replay == nil {
		t.Fatalf("text block = %+v", textBlock)
	}
	if string(textBlock.Replay.Data) != `{"type":"message","id":"msg_1"}` {
		t.Fatalf("replay data = %q", textBlock.Replay.Data)
	}
	if textBlock.Replay.DataRaw != "" {
		t.Fatalf("valid replay unexpectedly used data_raw: %q", textBlock.Replay.DataRaw)
	}
	if textBlock.Replay.Source != source {
		t.Fatalf("replay source = %+v", textBlock.Replay.Source)
	}
	toolBlock := recordedResponse.Message.Content[1]
	if toolBlock.ToolCall == nil || toolBlock.ToolCall.ID != "call_1" || toolBlock.ToolCall.Name != "read" {
		t.Fatalf("tool call block = %+v", toolBlock)
	}
	if toolBlock.ToolCall.ArgumentsRaw != `{"path":"README.md"}` {
		t.Fatalf("arguments_raw = %q", toolBlock.ToolCall.ArgumentsRaw)
	}
	if recordedResponse.Usage.InputTokens == nil || *recordedResponse.Usage.InputTokens != 120 ||
		recordedResponse.Usage.OutputTokens == nil || *recordedResponse.Usage.OutputTokens != 34 ||
		recordedResponse.Usage.CacheReadTokens == nil || *recordedResponse.Usage.CacheReadTokens != 5 ||
		recordedResponse.Usage.CacheWriteTokens == nil || *recordedResponse.Usage.CacheWriteTokens != 6 ||
		recordedResponse.Usage.ReasoningTokens == nil || *recordedResponse.Usage.ReasoningTokens != 7 {
		t.Fatalf("usage = %+v", recordedResponse.Usage)
	}

	var recordedToolStart struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		ArgumentsRaw string `json:"arguments_raw"`
	}
	if err := json.Unmarshal(field(t, parseObject(t, lines[4]), "call"), &recordedToolStart); err != nil {
		t.Fatalf("decode tool_start call: %v", err)
	}
	if recordedToolStart.ID != "call_1" || recordedToolStart.Name != "read" || recordedToolStart.ArgumentsRaw != `{"path":"README.md"}` {
		t.Fatalf("tool_start call = %+v", recordedToolStart)
	}

	toolEnd := parseObject(t, lines[5])
	var recordedToolEnd struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		ArgumentsRaw string `json:"arguments_raw"`
	}
	if err := json.Unmarshal(field(t, toolEnd, "call"), &recordedToolEnd); err != nil {
		t.Fatalf("decode tool_end call: %v", err)
	}
	if recordedToolEnd.ID != "call_1" {
		t.Fatalf("tool_end call = %+v", recordedToolEnd)
	}
	var recordedResult model.ToolResult
	if err := json.Unmarshal(field(t, toolEnd, "result"), &recordedResult); err != nil {
		t.Fatalf("decode tool_end result: %v", err)
	}
	if recordedResult.ToolCallID != "call_1" || recordedResult.Content != "README content" || recordedResult.IsError {
		t.Fatalf("tool_end result = %+v", recordedResult)
	}

	end := parseObject(t, lines[6])
	if got := stringField(t, end, "status"); got != string(agent.StatusFailed) {
		t.Fatalf("end status = %q", got)
	}
	if got := stringField(t, end, "error"); got != "tool failed" {
		t.Fatalf("end error = %q", got)
	}
}

func TestRecordPreservesTruncatedToolArgumentsAndInvalidReplay(t *testing.T) {
	recorder := newTestRecorder(t)

	source := model.Source{API: model.OpenAIResponses, Provider: "openai", Model: "gpt-test"}
	invalidArguments := json.RawMessage(`{"path":"README.md"`)
	invalidReplayData := json.RawMessage(`{"partial":`)
	validReplayData := json.RawMessage(`{"type":"reasoning","encrypted_content":"abc"}`)
	wantArguments := append(json.RawMessage(nil), invalidArguments...)
	wantInvalidReplay := append(json.RawMessage(nil), invalidReplayData...)

	call := &model.ToolCall{ID: "call_partial", Name: "read", Arguments: invalidArguments}
	invalidReplay := &model.Replay{Source: source, Data: invalidReplayData}
	response := &model.Response{
		Message: model.Message{
			Role:   model.RoleAssistant,
			Source: &source,
			Content: []model.ContentBlock{
				{
					Type:   model.BlockThinking,
					Replay: invalidReplay,
				},
				{
					Type:   model.BlockText,
					Text:   "参数被截断",
					Replay: &model.Replay{Source: source, Data: validReplayData},
				},
				{
					Type:     model.BlockToolCall,
					ToolCall: call,
				},
			},
		},
		StopReason: model.StopMaxTokens,
	}

	if err := recorder.Record(agent.Event{Type: agent.EventResponse, Turn: 2, Response: response}); err != nil {
		t.Fatalf("Record() with invalid raw payloads error = %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	lines := readJSONLines(t, recorder.Path())
	if len(lines) != 2 {
		t.Fatalf("got %d records, want meta + response", len(lines))
	}
	var recorded struct {
		Response struct {
			Message struct {
				Content []struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					ToolCall *struct {
						ArgumentsRaw string `json:"arguments_raw"`
					} `json:"tool_call"`
					Replay *struct {
						Data    json.RawMessage `json:"data"`
						DataRaw string          `json:"data_raw"`
					} `json:"replay"`
				} `json:"content"`
			} `json:"message"`
			StopReason string `json:"stop_reason"`
		} `json:"response"`
	}
	if err := json.Unmarshal(lines[1], &recorded); err != nil {
		t.Fatalf("decode response record: %v", err)
	}
	content := recorded.Response.Message.Content
	if len(content) != 3 {
		t.Fatalf("content blocks = %d", len(content))
	}
	if content[0].Replay == nil || content[0].Replay.DataRaw != string(invalidReplayData) {
		t.Fatalf("invalid replay = %+v", content[0].Replay)
	}
	if len(content[0].Replay.Data) != 0 {
		t.Fatalf("invalid replay unexpectedly used data: %q", content[0].Replay.Data)
	}
	if content[1].Text != "参数被截断" || content[1].Replay == nil {
		t.Fatalf("valid replay block = %+v", content[1])
	}
	if string(content[1].Replay.Data) != string(validReplayData) {
		t.Fatalf("valid replay data = %q", content[1].Replay.Data)
	}
	if content[2].ToolCall == nil || content[2].ToolCall.ArgumentsRaw != string(invalidArguments) {
		t.Fatalf("tool call = %+v", content[2].ToolCall)
	}
	if recorded.Response.StopReason != string(model.StopMaxTokens) {
		t.Fatalf("stop reason = %q", recorded.Response.StopReason)
	}

	if !bytes.Equal(call.Arguments, wantArguments) {
		t.Fatalf("Record mutated tool arguments: %q", call.Arguments)
	}
	if !bytes.Equal(invalidReplay.Data, wantInvalidReplay) {
		t.Fatalf("Record mutated invalid replay data: %q", invalidReplay.Data)
	}
}

func TestSessionFilesUsePrivatePermissionsAndUniqueIDs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not meaningful on Windows")
	}
	recorder := newTestRecorder(t)
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	dir := filepath.Dir(recorder.Path())
	if filepath.Base(dir) != "sessions" || filepath.Base(filepath.Dir(dir)) != ".johnny" {
		t.Fatalf("unexpected sessions directory: %q", dir)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat session directory: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("session directory mode = %o, want %o", got, 0o700)
	}
	fileInfo, err := os.Stat(recorder.Path())
	if err != nil {
		t.Fatalf("stat session file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("session file mode = %o, want %o", got, 0o600)
	}

	second, err := New(Metadata{WorkDir: "/workspace/project", API: "anthropic-messages", Model: "claude-test"})
	if err != nil {
		t.Fatalf("second New() error = %v", err)
	}
	if second.Path() == recorder.Path() {
		t.Fatalf("two sessions used the same path %q", second.Path())
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestNewSessionDoesNotReadOldLogs(t *testing.T) {
	home := testHome(t)
	dir := filepath.Join(home, ".johnny", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create sessions directory: %v", err)
	}
	oldPath := filepath.Join(dir, "old-session.jsonl")
	oldContent := `{"type":"start","marker":"OLD-HISTORY-MUST-NOT-APPEAR"}` + "\n"
	if err := os.WriteFile(oldPath, []byte(oldContent), 0o600); err != nil {
		t.Fatalf("write old session log: %v", err)
	}

	recorder, err := New(Metadata{WorkDir: "/workspace/project", API: "anthropic-messages", Model: "claude-test"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if recorder.Path() == oldPath {
		t.Fatal("new session reused the old log path")
	}
	if err := recorder.Record(agent.Event{
		Type: agent.EventStart,
		Turn: 0,
		Message: &model.Message{
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Type: model.BlockText, Text: "新任务"}},
		},
	}); err != nil {
		t.Fatalf("Record(start) error = %v", err)
	}
	if err := recorder.Record(agent.Event{Type: agent.EventEnd, Turn: 0, Status: agent.StatusCompleted}); err != nil {
		t.Fatalf("Record(end) error = %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	newData, err := os.ReadFile(recorder.Path())
	if err != nil {
		t.Fatalf("read new session log: %v", err)
	}
	if bytes.Contains(newData, []byte("OLD-HISTORY-MUST-NOT-APPEAR")) {
		t.Fatalf("new session log contains old history: %s", newData)
	}
	lines := readJSONLines(t, recorder.Path())
	if len(lines) != 3 {
		t.Fatalf("new session has %d records, want meta + start + end", len(lines))
	}
	var newStart struct {
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(lines[1], &newStart); err != nil {
		t.Fatalf("decode new start record: %v", err)
	}
	if len(newStart.Message.Content) != 1 || newStart.Message.Content[0].Text != "新任务" {
		t.Fatalf("new user message = %+v", newStart.Message.Content)
	}

	oldData, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatalf("read old session log: %v", err)
	}
	if string(oldData) != oldContent {
		t.Fatalf("old session log was modified: %q", oldData)
	}
}

func TestMetadataDoesNotIncludeCredentialsOrEnvironment(t *testing.T) {
	testHome(t)
	secrets := map[string]string{
		"JOHNNY_API_KEY":    "sk-johnny-secret",
		"OPENAI_API_KEY":    "sk-openai-secret",
		"ANTHROPIC_API_KEY": "sk-anthropic-secret",
		"ENV_SNAPSHOT":      "environment-secret-value",
	}
	for key, value := range secrets {
		t.Setenv(key, value)
	}

	recorder, err := New(Metadata{WorkDir: "/workspace/project", API: "openai-responses", Model: "gpt-test"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	data, err := os.ReadFile(recorder.Path())
	if err != nil {
		t.Fatalf("read session log: %v", err)
	}
	for key, value := range secrets {
		if bytes.Contains(data, []byte(value)) {
			t.Fatalf("session metadata contains %s value", key)
		}
	}
	meta := parseObject(t, readJSONLines(t, recorder.Path())[0])
	for key := range meta {
		switch key {
		case "type", "id", "time", "cwd", "api", "model", "permission_mode":
		default:
			t.Fatalf("session metadata contains unexpected field %q", key)
		}
	}
	if _, ok := meta["env"]; ok {
		t.Fatal("session metadata contains an environment snapshot")
	}
	if _, ok := meta["api_key"]; ok {
		t.Fatal("session metadata contains an API key")
	}
}

type stubWriteCloser struct {
	writeErr error
	closeErr error
	data     bytes.Buffer
}

func (s *stubWriteCloser) Write(p []byte) (int, error) {
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return s.data.Write(p)
}

func (s *stubWriteCloser) Close() error {
	return s.closeErr
}

type shortWriteCloser struct{}

func (shortWriteCloser) Write(p []byte) (int, error) {
	return len(p) - 1, nil
}

func (shortWriteCloser) Close() error {
	return nil
}

func TestRecorderReturnsWriteFailures(t *testing.T) {
	writeErr := errors.New("disk full")
	writer := &stubWriteCloser{writeErr: writeErr}
	recorder := newRecorder(writer, "/tmp/session.jsonl")

	err := recorder.Record(agent.Event{
		Type: agent.EventStart,
		Turn: 1,
		Message: &model.Message{
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Type: model.BlockText, Text: "hello"}},
		},
	})
	if !errors.Is(err, writeErr) {
		t.Fatalf("Record() error = %v, want %v", err, writeErr)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := recorder.Record(agent.Event{Type: agent.EventEnd}); !errors.Is(err, errRecorderClosed) {
		t.Fatalf("Record() after Close() error = %v, want %v", err, errRecorderClosed)
	}
}

func TestRecorderReturnsShortWriteFailure(t *testing.T) {
	recorder := newRecorder(shortWriteCloser{}, "/tmp/session.jsonl")
	err := recorder.Record(agent.Event{Type: agent.EventRequestStart, Turn: 1})
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Record() error = %v, want %v", err, io.ErrShortWrite)
	}
}

func TestRecorderReturnsCloseFailure(t *testing.T) {
	closeErr := errors.New("close failed")
	recorder := newRecorder(&stubWriteCloser{closeErr: closeErr}, "/tmp/session.jsonl")
	if err := recorder.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close() error = %v, want %v", err, closeErr)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("second Close() error = %v, want nil", err)
	}
}

func TestRecordMemory(t *testing.T) {
	recorder := newTestRecorder(t)
	if err := recorder.RecordMemory(MemoryRecord{
		Scope: "project", Action: "created", Name: "go-test",
		Path: "/tmp/memory/go-test.md", Source: "session-1",
	}); err != nil {
		t.Fatal(err)
	}
	lines := readJSONLines(t, recorder.Path())
	if len(lines) != 2 {
		t.Fatalf("记忆记录数量错误：%d", len(lines))
	}
	object := parseObject(t, lines[1])
	for key, want := range map[string]string{
		"type": "memory_write", "scope": "project", "action": "created",
		"name": "go-test", "path": "/tmp/memory/go-test.md", "source": "session-1",
	} {
		var got string
		if err := json.Unmarshal(object[key], &got); err != nil || got != want {
			t.Fatalf("%s = %q (%v)，want %q", key, got, err, want)
		}
	}
}

func TestRecordInjectedInboxMessageWithSources(t *testing.T) {
	recorder := newTestRecorder(t)
	message := model.Message{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.BlockText, Text: "[子任务 ab12 状态：completed]"}}}
	if err := recorder.Record(agent.Event{Type: agent.EventInject, UserTurn: 2, Turn: 3, Message: &message, Sources: []string{"subagent:ab12"}}); err != nil {
		t.Fatal(err)
	}
	lines := readJSONLines(t, recorder.Path())
	object := parseObject(t, lines[len(lines)-1])
	if stringField(t, object, "type") != "inject" || intField(t, object, "user_turn") != 2 || intField(t, object, "turn") != 3 {
		t.Fatalf("注入记录基础字段错误：%s", lines[len(lines)-1])
	}
	var sources []string
	if err := json.Unmarshal(field(t, object, "sources"), &sources); err != nil || len(sources) != 1 || sources[0] != "subagent:ab12" {
		t.Fatalf("注入来源未记录：%v %v", sources, err)
	}
	if !strings.Contains(string(field(t, object, "message")), "子任务 ab12") {
		t.Fatal("注入记录应包含消息正文")
	}
}

func TestRecordPermissionKeepsSubagentOrigin(t *testing.T) {
	recorder := newTestRecorder(t)
	if err := recorder.RecordPermission(PermissionRecord{ToolCallID: "c1", Tool: "write", Target: "/tmp/x", Decision: "allow", Origin: "ab12"}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordPermission(PermissionRecord{ToolCallID: "c2", Tool: "write", Target: "/tmp/y", Decision: "deny"}); err != nil {
		t.Fatal(err)
	}
	lines := readJSONLines(t, recorder.Path())
	child, main := parseObject(t, lines[1]), parseObject(t, lines[2])
	if stringField(t, child, "origin") != "ab12" {
		t.Fatalf("子任务审批应记录来源：%s", lines[1])
	}
	if _, ok := main["origin"]; ok {
		t.Fatalf("主 Agent 的审批不应带来源：%s", lines[2])
	}
}

func TestSubagentSessionMetadataAndParentRecords(t *testing.T) {
	testHome(t)
	child, err := New(Metadata{
		ID: "parent-sub-ab12", WorkDir: "/workspace/project", API: "anthropic-messages", Model: "claude-test", PermissionMode: "default",
		ParentID: "parent", SubagentID: "ab12", Kind: "fresh", Access: "write", Paths: []string{"/workspace/project/pkg"}, Description: "重构 pkg",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	meta := parseObject(t, readJSONLines(t, child.Path())[0])
	for key, want := range map[string]string{"parent_id": "parent", "subagent_id": "ab12", "kind": "fresh", "access": "write", "description": "重构 pkg"} {
		if stringField(t, meta, key) != want {
			t.Fatalf("子会话元数据 %s 错误：%s", key, meta[key])
		}
	}
	var paths []string
	if err := json.Unmarshal(field(t, meta, "paths"), &paths); err != nil || len(paths) != 1 || paths[0] != "/workspace/project/pkg" {
		t.Fatalf("子会话范围未记录：%v %v", paths, err)
	}

	parent := newTestRecorder(t)
	if err := parent.RecordSubagent(SubagentRecord{Phase: "start", ID: "ab12", Description: "重构 pkg", Kind: "fresh", Access: "write", Paths: []string{"/workspace/project/pkg"}, ChildSession: "parent-sub-ab12"}); err != nil {
		t.Fatal(err)
	}
	if err := parent.RecordSubagent(SubagentRecord{Phase: "end", ID: "ab12", Description: "重构 pkg", Kind: "fresh", Access: "write", Status: "max_turns", Requests: 30, DurationMS: 1500, Partial: true, Truncated: true, Injected: true}); err != nil {
		t.Fatal(err)
	}
	lines := readJSONLines(t, parent.Path())
	start, end := parseObject(t, lines[1]), parseObject(t, lines[2])
	if stringField(t, start, "type") != "subagent_start" || stringField(t, start, "child_session") != "parent-sub-ab12" {
		t.Fatalf("起始记录错误：%s", lines[1])
	}
	if stringField(t, end, "type") != "subagent_end" || stringField(t, end, "status") != "max_turns" || intField(t, end, "requests") != 30 || intField(t, end, "duration_ms") != 1500 {
		t.Fatalf("结束记录错误：%s", lines[2])
	}
	for _, key := range []string{"partial", "truncated", "injected"} {
		if string(field(t, end, key)) != "true" {
			t.Fatalf("结束记录缺少 %s：%s", key, lines[2])
		}
	}
	assertRecordTime(t, end)
}
