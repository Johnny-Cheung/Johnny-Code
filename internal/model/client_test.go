package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func anthropicEvent(w io.Writer, kind, data string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
}

func anthropicTextStart(w io.Writer) {
	anthropicEvent(w, "message_start", `{"type":"message_start","message":{"id":"msg_1","role":"assistant","content":[],"usage":{"input_tokens":10,"cache_read_input_tokens":3,"cache_creation_input_tokens":2,"output_tokens":0}}}`)
	anthropicEvent(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
}

func sendText(w io.Writer, text string) {
	anthropicEvent(w, "content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, text))
}

func sendStop(w io.Writer, reason string) {
	anthropicEvent(w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
	anthropicEvent(w, "message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":2,"output_tokens_details":{"thinking_tokens":1}}}`, reason))
	anthropicEvent(w, "message_stop", `{"type":"message_stop"}`)
}

func makeTestClient(t *testing.T, server *httptest.Server) Client {
	t.Helper()
	client, err := NewClient(Options{API: AnthropicMessages, APIKey: "test-key", BaseURL: server.URL + "/gateway"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func textRequest() Request {
	return Request{Model: "relay-deepseek", Messages: []Message{{Role: RoleUser, Content: []ContentBlock{{Type: BlockText, Text: "你好"}}}}, MaxOutputTokens: 4096}
}

func TestStreamOutputsBeforeResponseCompletes(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "unrelated-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "unrelated-token")
	firstWritten := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/gateway/v1/messages" {
			t.Errorf("请求地址错误：%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("X-Api-Key") != "" || r.Header.Get("Anthropic-Version") == "" {
			t.Error("凭据或版本头错误")
		}
		var body struct {
			Model     string
			Stream    bool
			MaxTokens int `json:"max_tokens"`
			Messages  []struct {
				Role    string
				Content []struct{ Text string }
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "relay-deepseek" || !body.Stream || body.MaxTokens != 4096 || len(body.Messages) != 1 || body.Messages[0].Content[0].Text != "你好" {
			t.Error("请求没有正确映射")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		anthropicTextStart(w)
		sendText(w, "你好")
		w.(http.Flusher).Flush()
		select {
		case <-firstWritten:
		case <-r.Context().Done():
			return
		}
		sendText(w, "，世界")
		sendStop(w, "end_turn")
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var text strings.Builder
	var events []Event
	res, err := makeTestClient(t, server).Stream(ctx, textRequest(), func(e Event) error {
		events = append(events, e)
		if e.Type == EventBlockDelta {
			text.WriteString(e.Delta)
			if text.String() == "你好" {
				close(firstWritten)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if text.String() != "你好，世界" || res.Message.Content[0].Text != text.String() || res.StopReason != StopEndTurn {
		t.Fatalf("流式和完整结果不一致：%+v", res)
	}
	if len(events) != 4 || events[0].Type != EventBlockStart || events[3].Type != EventBlockEnd {
		t.Fatal("块事件契约不正确")
	}
	if res.Usage.InputTokens == nil || *res.Usage.InputTokens != 15 || res.Usage.OutputTokens == nil || *res.Usage.OutputTokens != 2 || res.Usage.ReasoningTokens == nil || *res.Usage.ReasoningTokens != 1 {
		t.Fatal("用量映射错误")
	}
}

func TestAnthropicTerminalStatesAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		stopped      bool
		want         StopReason
		wantErr      bool
	}{
		{"正常", "end_turn", true, StopEndTurn, false},
		{"上限", "max_tokens", true, StopMaxTokens, false},
		{"拒绝", "refusal", true, StopRefusal, false},
		{"未知结束原因", "unexpected", true, "", true},
		{"提前断流", "", false, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				anthropicTextStart(w)
				sendText(w, "部分回复")
				if tc.stopped {
					sendStop(w, tc.reason)
				}
			}))
			defer server.Close()
			res, err := makeTestClient(t, server).Stream(context.Background(), textRequest(), nil)
			if (err != nil) != tc.wantErr || res.StopReason != tc.want || res.Message.Content[0].Text != "部分回复" {
				t.Fatalf("reason=%s err=%v", res.StopReason, err)
			}
		})
	}
}

func TestStreamReportsSafeErrors(t *testing.T) {
	for _, status := range []int{401, 429, 500, 200} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if status == 200 {
					w.Header().Set("Content-Type", "text/event-stream")
					anthropicEvent(w, "error", `{"type":"error","error":{"message":"invalid key: test-key","type":"overloaded_error"}}`)
				} else {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					fmt.Fprint(w, `{"type":"error","error":{"message":"invalid key: test-key"}}`)
				}
			}))
			defer server.Close()
			_, err := makeTestClient(t, server).Stream(context.Background(), textRequest(), nil)
			var modelErr *Error
			if !errors.As(err, &modelErr) || strings.Contains(err.Error(), "test-key") {
				t.Fatalf("错误不安全或没有类型：%v", err)
			}
			if status == 401 && modelErr.Kind != ErrAuthentication || status == 429 && modelErr.Kind != ErrRateLimit || status == 500 && modelErr.Kind != ErrService {
				t.Fatal("错误分类错误")
			}
		})
	}
}

func TestStreamCancellationAndCallbackFailure(t *testing.T) {
	for _, cancelRequest := range []bool{true, false} {
		t.Run(fmt.Sprint(cancelRequest), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				anthropicTextStart(w)
				sendText(w, "开始回复")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			outputErr := errors.New("输出中断")
			res, err := makeTestClient(t, server).Stream(ctx, textRequest(), func(e Event) error {
				if e.Type == EventBlockDelta {
					if cancelRequest {
						cancel()
					} else {
						return outputErr
					}
				}
				return nil
			})
			want := outputErr
			if cancelRequest {
				want = context.Canceled
			}
			if !errors.Is(err, want) || res.StopReason != "" {
				t.Fatalf("未正确处理取消或输出错误：%v", err)
			}
		})
	}
}

func TestAnthropicToolsAndThinkingRoundTrip(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !strings.Contains(string(body["system"]), "系统指令") || !strings.Contains(string(body["tools"]), "read_file") {
			t.Error("系统指令或工具定义没有传递")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 2 {
			var messages []struct {
				Role    string
				Content []map[string]json.RawMessage
			}
			if err := json.Unmarshal(body["messages"], &messages); err != nil {
				t.Error(err)
			}
			if len(messages) != 3 || string(messages[1].Content[0]["signature"]) != `"signature-1"` || !strings.Contains(string(body["messages"]), `"tool_use_id":"call_1"`) || !strings.Contains(string(body["messages"]), `"is_error":true`) {
				t.Error("工具或思考续接数据丢失")
			}
			anthropicTextStart(w)
			sendText(w, "完成")
			sendStop(w, "end_turn")
			return
		}
		anthropicEvent(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`)
		anthropicEvent(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"分析"}}`)
		anthropicEvent(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signature-1"}}`)
		anthropicEvent(w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
		anthropicEvent(w, "content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"read_file","input":{}}}`)
		anthropicEvent(w, "content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`)
		anthropicEvent(w, "content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"main.go\"}"}}`)
		anthropicEvent(w, "content_block_stop", `{"type":"content_block_stop","index":1}`)
		anthropicEvent(w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`)
		anthropicEvent(w, "message_stop", `{"type":"message_stop"}`)
	}))
	defer server.Close()
	client := makeTestClient(t, server)
	req := textRequest()
	req.System = "系统指令"
	req.Tools = []ToolDefinition{{Name: "read_file", Description: "读取文件", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}}
	var retained []Event
	res, err := client.Stream(context.Background(), req, func(e Event) error { retained = append(retained, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != StopToolUse || res.Message.Content[1].ToolCall.ID != "call_1" || string(res.Message.Content[1].ToolCall.Arguments) != `{"path":"main.go"}` {
		t.Fatal("工具调用组装错误")
	}
	if retained[3].Block.ToolCall.Arguments != nil {
		t.Fatal("开始事件不应被后续增量修改")
	}
	req.Messages = append(req.Messages, res.Message, Message{Role: RoleTool, Content: []ContentBlock{{Type: BlockToolResult, ToolResult: &ToolResult{ToolCallID: "call_1", Content: "文件不存在", IsError: true}}}})
	if _, err := client.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	req.Model = "another-model"
	if _, err := client.Stream(context.Background(), req, nil); err == nil {
		t.Fatal("专用思考数据不能直接跨模型重放")
	}
	if requests != 2 {
		t.Fatal("无效历史不应发请求")
	}
}

func TestInvalidRequestsDoNotReachService(t *testing.T) {
	client, err := NewClient(Options{API: AnthropicMessages, BaseURL: "http://127.0.0.1:1", APIKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []Request{
		{},
		{Model: "model", Messages: []Message{{Role: RoleTool, Content: []ContentBlock{{Type: BlockToolResult, ToolResult: &ToolResult{ToolCallID: "missing"}}}}}},
		{Model: "model", Messages: []Message{{Role: RoleAssistant, Content: []ContentBlock{{Type: BlockToolCall, ToolCall: &ToolCall{ID: "call", Name: "read", Arguments: json.RawMessage(`[]`)}}}}}},
	} {
		_, err := client.Stream(context.Background(), req, nil)
		var modelErr *Error
		if !errors.As(err, &modelErr) || modelErr.Kind != ErrInvalidRequest {
			t.Fatalf("应在本地拒绝非法请求：%v", err)
		}
	}
}
