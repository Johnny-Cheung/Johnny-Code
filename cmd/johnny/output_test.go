package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func textDelta(text string) agent.Event {
	return agent.Event{
		Type:       agent.EventModel,
		ModelEvent: &model.Event{Type: model.EventBlockDelta, Block: model.ContentBlock{Type: model.BlockText}, Delta: text},
	}
}

func TestRendererDisplaysOnlyTextAndFinishesResponse(t *testing.T) {
	var output, stderr bytes.Buffer
	display := newRenderer(&output, &stderr)
	events := []agent.Event{
		{Type: agent.EventRequestStart, Turn: 1},
		{Type: agent.EventModel, ModelEvent: &model.Event{Type: model.EventBlockDelta, Block: model.ContentBlock{Type: model.BlockThinking}, Delta: "隐藏的思考"}},
		textDelta("回复"),
		{Type: agent.EventModel, ModelEvent: &model.Event{Type: model.EventBlockDelta, Block: model.ContentBlock{Type: model.BlockToolCall}, Delta: `{"path":"main.go"}`}},
		{Type: agent.EventResponse, Response: &model.Response{Message: model.Message{Content: []model.ContentBlock{{Type: model.BlockText, Text: "回复"}}}}},
		{Type: agent.EventEnd, Status: agent.StatusCompleted},
	}
	for _, event := range events {
		if err := display.Handle(event); err != nil {
			t.Fatal(err)
		}
	}
	if output.String() != "回复\n" {
		t.Fatalf("正文输出错误：%q", output.String())
	}
	if !strings.Contains(stderr.String(), "正在等待模型回复") {
		t.Fatalf("缺少每轮等待提示：%q", stderr.String())
	}
}

func TestRendererFinishesPartialResponseOnFailure(t *testing.T) {
	var output, stderr bytes.Buffer
	display := newRenderer(&output, &stderr)
	if err := display.Handle(textDelta("部分回复")); err != nil {
		t.Fatal(err)
	}
	if err := display.Handle(agent.Event{Type: agent.EventEnd, Status: agent.StatusFailed, Error: "network"}); err != nil {
		t.Fatal(err)
	}
	if output.String() != "部分回复\n" {
		t.Fatalf("失败响应后应保留部分正文并补换行：%q", output.String())
	}
}

func TestRendererDoesNotAddDuplicateNewline(t *testing.T) {
	var output, stderr bytes.Buffer
	display := newRenderer(&output, &stderr)
	for _, event := range []agent.Event{
		textDelta("回复\n"),
		{Type: agent.EventResponse},
		{Type: agent.EventToolStart, Call: &model.ToolCall{Name: "read", Arguments: json.RawMessage(`{"path":"main.go"}`)}},
	} {
		if err := display.Handle(event); err != nil {
			t.Fatal(err)
		}
	}
	if output.String() != "回复\n" {
		t.Fatalf("不应重复补换行：%q", output.String())
	}
	if !strings.Contains(stderr.String(), "[read] main.go") {
		t.Fatalf("工具关键参数显示错误：%q", stderr.String())
	}
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) { return 0, nil }

func TestRendererPropagatesShortWrite(t *testing.T) {
	var stderr bytes.Buffer
	display := newRenderer(shortWriter{}, &stderr)
	err := display.Handle(textDelta("回复"))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("应保留短写错误链：%v", err)
	}
	if !strings.Contains(err.Error(), "输出回复失败") {
		t.Fatalf("错误应说明输出失败：%v", err)
	}
}

func TestRendererPropagatesStderrShortWrite(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event agent.Event
	}{
		{"等待提示", agent.Event{Type: agent.EventRequestStart, Turn: 1}},
		{"工具开始", agent.Event{Type: agent.EventToolStart, Call: &model.ToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"echo hi"}`)}}},
		{"工具结束", agent.Event{Type: agent.EventToolEnd, ToolResult: &model.ToolResult{Content: "ok"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			display := newRenderer(&output, shortWriter{})
			err := display.Handle(tc.event)
			if !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("应保留 stderr 短写错误链：%v", err)
			}
			if !strings.Contains(err.Error(), "输出任务进度失败") {
				t.Fatalf("错误应说明进度输出失败：%v", err)
			}
		})
	}
}

func TestRendererLimitsKeyParameterPreview(t *testing.T) {
	command := strings.Repeat("x", previewMaxKeyBytes*4)
	arguments, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	var output, stderr bytes.Buffer
	display := newRenderer(&output, &stderr)
	if err := display.Handle(agent.Event{Type: agent.EventToolStart, Call: &model.ToolCall{Name: "bash", Arguments: arguments}}); err != nil {
		t.Fatal(err)
	}
	got := stderr.String()
	if strings.Contains(got, command) {
		t.Fatalf("关键参数未限制长度：%q", got)
	}
	if !strings.Contains(got, "参数已截断") {
		t.Fatalf("关键参数截断缺少标记：%q", got)
	}
	if len(got) > previewMaxKeyBytes+128 {
		t.Fatalf("关键参数预览仍然过长：%d", len(got))
	}
}

type recordingTools struct{ executions int }

func (t *recordingTools) Definitions() []model.ToolDefinition { return nil }

func (t *recordingTools) Execute(context.Context, model.ToolCall) (tool.Result, error) {
	t.executions++
	return tool.Result{Content: "ok"}, nil
}

type toolRequestClient struct{}

func (toolRequestClient) Stream(context.Context, model.Request, func(model.Event) error) (model.Response, error) {
	return model.Response{
		Message: model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
			Type:     model.BlockToolCall,
			ToolCall: &model.ToolCall{ID: "call-1", Name: "bash", Arguments: json.RawMessage(`{"command":"touch side-effect"}`)},
		}}},
		StopReason: model.StopToolUse,
	}, nil
}

// failOnNthWriter 第 failAt 次 Write 返回短写，其余正常。
type failOnNthWriter struct {
	failAt int
	calls  int
}

func (w *failOnNthWriter) Write(data []byte) (int, error) {
	w.calls++
	if w.calls == w.failAt {
		return 0, nil
	}
	return len(data), nil
}

func TestToolStartDisplayFailurePreventsExecution(t *testing.T) {
	tools := &recordingTools{}
	var output bytes.Buffer
	// 第 1 次 stderr 写是等待提示，第 2 次是工具开始行。
	display := newRenderer(&output, &failOnNthWriter{failAt: 2})
	_, err := agent.Run(context.Background(), agent.Options{
		Client:   toolRequestClient{},
		Tools:    tools,
		Model:    "test-model",
		MaxTurns: 2,
	}, "执行命令", display.Handle)
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("工具开始显示失败应返回短写错误：%v", err)
	}
	if tools.executions != 0 {
		t.Fatalf("工具开始显示失败后不应执行工具，实际执行 %d 次", tools.executions)
	}
}

func TestToolPreviewEscapesControlsAndLimits(t *testing.T) {
	var output, stderr bytes.Buffer
	display := newRenderer(&output, &stderr)
	arguments, err := json.Marshal(map[string]string{"command": "printf '\x1b[31mred\x1b[0m\nnext\tline'"})
	if err != nil {
		t.Fatal(err)
	}
	call := &model.ToolCall{Name: "bash", Arguments: arguments}
	content := strings.Repeat("line\n", 30) + strings.Repeat("x", previewMaxBytes+100) + "\x1b[2J"
	for _, event := range []agent.Event{
		{Type: agent.EventToolStart, Call: call},
		{Type: agent.EventToolEnd, ToolResult: &model.ToolResult{Content: content, IsError: true}},
	} {
		if err := display.Handle(event); err != nil {
			t.Fatal(err)
		}
	}
	got := stderr.String()
	if strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\x07') || strings.ContainsRune(got, '\r') {
		t.Fatalf("终端输出不应包含原始控制字符：%q", got)
	}
	for _, want := range []string{`\x1b[31m`, `\nnext`, `\tline`, "[失败]", "预览已截断"} {
		if !strings.Contains(got, want) {
			t.Fatalf("终端预览缺少 %q：%q", want, got)
		}
	}
	if strings.Count(got, `line\n`) > previewMaxLines {
		t.Fatalf("预览超过 %d 行：%q", previewMaxLines, got)
	}
}
