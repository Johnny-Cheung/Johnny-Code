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

// openaiData 写入一条 OpenAI 风格的 SSE 数据事件。
func openaiData(w io.Writer, data string) {
	fmt.Fprintf(w, "data: %s\n\n", data)
}

// openaiBody 读取并解析测试服务收到的 JSON 请求体。
func openaiBody(t *testing.T, r *http.Request) map[string]json.RawMessage {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("请求体不是 JSON：%v", err)
	}
	return body
}

// openaiAssertRequest 校验请求方法、地址与凭据，并返回解析后的请求体。
func openaiAssertRequest(t *testing.T, r *http.Request, path string) map[string]json.RawMessage {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != path {
		t.Errorf("请求地址错误：%s %s", r.Method, r.URL.Path)
	}
	if r.Header.Get("Authorization") != "Bearer openai-key" {
		t.Error("应使用配置的 Bearer Key 调用模型服务")
	}
	return openaiBody(t, r)
}

// openaiTestClient 构造指向测试服务的 OpenAI 客户端。
func openaiTestClient(t *testing.T, api API, server *httptest.Server) Client {
	t.Helper()
	client, err := NewClient(Options{API: api, APIKey: "openai-key", BaseURL: server.URL + "/gateway"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// openaiSimpleRequest 构造最简文本请求。
func openaiSimpleRequest() Request {
	return Request{
		Model:           "gpt-test",
		Messages:        []Message{{Role: RoleUser, Content: []ContentBlock{{Type: BlockText, Text: "你好"}}}},
		MaxOutputTokens: 4096,
	}
}

// openaiChatChunk 写入一个携带增量内容的 Chat Completions 分片。
func openaiChatChunk(w io.Writer, delta string) {
	openaiData(w, fmt.Sprintf(`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":%s}]}`, delta))
}

// openaiChatFinish 写入一个携带结束原因的 Chat Completions 分片。
func openaiChatFinish(w io.Writer, reason string) {
	openaiData(w, fmt.Sprintf(`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":%q}]}`, reason))
}

// openaiSimpleStream 按协议写入一段最简正常回复。
func openaiSimpleStream(w io.Writer, api API, text string) {
	if api == OpenAIChatCompletions {
		openaiChatChunk(w, fmt.Sprintf(`{"content":%q}`, text))
		openaiChatFinish(w, "stop")
		openaiData(w, "[DONE]")
		return
	}
	openaiData(w, `{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`)
	openaiData(w, fmt.Sprintf(`{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_1","delta":%q}`, text))
	openaiData(w, `{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"完成","annotations":[]}]}}`)
	openaiData(w, `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`)
}

func TestOpenAIResponsesTextStreamingAndUsage(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-key")
	t.Setenv("OPENAI_ORG_ID", "env-org")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:1")
	firstWritten := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := openaiAssertRequest(t, r, "/gateway/v1/responses")
		if r.Header.Get("OpenAI-Organization") != "" {
			t.Error("不应继承进程环境里的其他凭据")
		}
		if string(body["model"]) != `"gpt-test"` || string(body["stream"]) != "true" || string(body["store"]) != "false" || string(body["max_output_tokens"]) != "4096" {
			t.Errorf("模型名、流式或输出上限参数错误：%s", body)
		}
		if string(body["include"]) != `["reasoning.encrypted_content"]` {
			t.Errorf("应请求加密推理内容以便无状态重放：%s", body["include"])
		}
		if string(body["instructions"]) != `"系统指令"` {
			t.Errorf("系统指令未传递：%s", body["instructions"])
		}
		if !strings.Contains(string(body["input"]), `"role":"user"`) || !strings.Contains(string(body["input"]), `"你好"`) {
			t.Errorf("用户消息未正确映射：%s", body["input"])
		}
		if !strings.Contains(string(body["tools"]), `"read_file"`) || !strings.Contains(string(body["tools"]), `"type":"function"`) {
			t.Errorf("工具定义未传递：%s", body["tools"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		openaiData(w, `{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`)
		openaiData(w, `{"type":"response.content_part.added","output_index":0,"content_index":0,"item_id":"msg_1","part":{"type":"output_text","text":"","annotations":[]}}`)
		openaiData(w, `{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_1","delta":"你好"}`)
		w.(http.Flusher).Flush()
		select {
		case <-firstWritten:
		case <-r.Context().Done():
			return
		}
		openaiData(w, `{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_1","delta":"，世界"}`)
		openaiData(w, `{"type":"response.output_text.done","output_index":0,"content_index":0,"item_id":"msg_1","text":"你好，世界"}`)
		openaiData(w, `{"type":"response.content_part.done","output_index":0,"content_index":0,"item_id":"msg_1","part":{"type":"output_text","text":"你好，世界","annotations":[]}}`)
		openaiData(w, `{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"你好，世界","annotations":[]}]}}`)
		openaiData(w, `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"你好，世界","annotations":[]}]}],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":4,"cache_write_tokens":1},"output_tokens":7,"output_tokens_details":{"reasoning_tokens":3},"total_tokens":17}}}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req := openaiSimpleRequest()
	req.System = "系统指令"
	req.Tools = []ToolDefinition{{Name: "read_file", Description: "读取文件", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}}
	var text strings.Builder
	var events []Event
	res, err := openaiTestClient(t, OpenAIResponses, server).Stream(ctx, req, func(e Event) error {
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
	if text.String() != "你好，世界" || len(res.Message.Content) != 1 || res.Message.Content[0].Text != text.String() || res.StopReason != StopEndTurn {
		t.Fatalf("流式结果与完整结果不一致：%+v", res)
	}
	if len(events) != 4 || events[0].Type != EventBlockStart || events[3].Type != EventBlockEnd {
		t.Fatal("块事件契约不正确")
	}
	usage := res.Usage
	if usage.InputTokens == nil || *usage.InputTokens != 10 || usage.OutputTokens == nil || *usage.OutputTokens != 7 {
		t.Fatal("输入或输出用量映射错误")
	}
	if usage.CacheReadTokens == nil || *usage.CacheReadTokens != 4 || usage.CacheWriteTokens == nil || *usage.CacheWriteTokens != 1 || usage.ReasoningTokens == nil || *usage.ReasoningTokens != 3 {
		t.Fatal("缓存与推理分项映射错误")
	}
}

func TestOpenAIResponsesToolAndReasoningRoundTrip(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body := openaiAssertRequest(t, r, "/gateway/v1/responses")
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 2 {
			input := string(body["input"])
			encrypted := strings.Index(input, `"encrypted_content":"encrypted-1"`)
			text := strings.Index(input, "我来读取文件")
			call := strings.Index(input, `"name":"read_file"`)
			output := strings.Index(input, "文件不存在")
			if encrypted < 0 || !strings.Contains(input, `"type":"reasoning"`) {
				t.Errorf("思考项未按原样重放：%s", input)
			}
			if text < 0 || call < 0 || output < 0 || !(encrypted < text && text < call && call < output) {
				t.Errorf("重放的消息顺序错误：%s", input)
			}
			if !strings.Contains(input, `"call_id":"call_1"`) || !strings.Contains(input, `"arguments":"{\"path\":\"main.go\"}"`) {
				t.Errorf("工具调用或结果未正确关联：%s", input)
			}
			if !strings.Contains(input, `"id":"fc_1"`) {
				t.Errorf("工具调用输出项 ID 未保留：%s", input)
			}
			if !strings.Contains(input, `"type":"function_call_output"`) {
				t.Errorf("缺少工具结果输入项：%s", input)
			}
			openaiData(w, `{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_2","type":"message","status":"in_progress","role":"assistant","content":[]}}`)
			openaiData(w, `{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_2","delta":"完成"}`)
			openaiData(w, `{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"完成","annotations":[]}]}}`)
			openaiData(w, `{"type":"response.completed","response":{"id":"resp_3","status":"completed","output":[{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"完成","annotations":[]}]}]}}`)
			return
		}
		openaiData(w, `{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`)
		openaiData(w, `{"type":"response.reasoning_summary_text.delta","output_index":0,"item_id":"rs_1","summary_index":0,"delta":"先分析"}`)
		openaiData(w, `{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"先分析"}],"encrypted_content":"encrypted-1"}}`)
		openaiData(w, `{"type":"response.output_item.added","output_index":1,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`)
		openaiData(w, `{"type":"response.output_text.delta","output_index":1,"content_index":0,"item_id":"msg_1","delta":"我来读取文件"}`)
		openaiData(w, `{"type":"response.output_item.done","output_index":1,"item":{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"我来读取文件","annotations":[]}]}}`)
		openaiData(w, `{"type":"response.output_item.added","output_index":2,"item":{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"read_file","arguments":""}}`)
		openaiData(w, `{"type":"response.function_call_arguments.delta","output_index":2,"item_id":"fc_1","delta":"{\"path\":"}`)
		openaiData(w, `{"type":"response.function_call_arguments.delta","output_index":2,"item_id":"fc_1","delta":"\"main.go\"}"}`)
		openaiData(w, `{"type":"response.function_call_arguments.done","output_index":2,"item_id":"fc_1","arguments":"{\"path\":\"main.go\"}"}`)
		openaiData(w, `{"type":"response.output_item.done","output_index":2,"item":{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"main.go\"}"}}`)
		openaiData(w, `{"type":"response.completed","response":{"id":"resp_2","status":"completed","output":[{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"先分析"}],"encrypted_content":"encrypted-1"},{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"我来读取文件","annotations":[]}]},{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"main.go\"}"}]}}`)
	}))
	defer server.Close()
	client := openaiTestClient(t, OpenAIResponses, server)
	res, err := client.Stream(context.Background(), openaiSimpleRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != StopToolUse || len(res.Message.Content) != 3 {
		t.Fatalf("工具回合结果错误：%+v", res)
	}
	thinking := res.Message.Content[0]
	if thinking.Type != BlockThinking || thinking.Text != "先分析" || thinking.Replay == nil || !strings.Contains(string(thinking.Replay.Data), `"encrypted_content":"encrypted-1"`) {
		t.Fatalf("思考块或加密推理数据缺失：%+v", thinking)
	}
	if thinking.Replay.Source != sourceFor(Options{API: OpenAIResponses, BaseURL: "http://" + strings.TrimPrefix(server.URL, "http://") + "/gateway/v1/", APIKey: "openai-key"}, "gpt-test") {
		t.Fatal("思考重放数据来源标记错误")
	}
	if res.Message.Content[1].Text != "我来读取文件" {
		t.Fatalf("正文块错误：%+v", res.Message.Content[1])
	}
	call := res.Message.Content[2].ToolCall
	if call == nil || call.ID != "call_1" || call.Name != "read_file" || string(call.Arguments) != `{"path":"main.go"}` {
		t.Fatalf("工具调用组装错误：%+v", call)
	}
	req := openaiSimpleRequest()
	req.Messages = append(req.Messages, res.Message, Message{Role: RoleTool, Content: []ContentBlock{{Type: BlockToolResult, ToolResult: &ToolResult{ToolCallID: "call_1", Content: "文件不存在", IsError: true}}}})
	if _, err := client.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatal("续接请求没有发送")
	}
}

func TestOpenAIResponsesTruncationKeepsPartialResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openaiAssertRequest(t, r, "/gateway/v1/responses")
		w.Header().Set("Content-Type", "text/event-stream")
		openaiData(w, `{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`)
		openaiData(w, `{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_1","delta":"部分回复"}`)
		openaiData(w, `{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"部分回复","annotations":[]}]}}`)
		openaiData(w, `{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"read_file","arguments":""}}`)
		openaiData(w, `{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_1","delta":"{\"path\":"}`)
		openaiData(w, `{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"部分回复","annotations":[]}]},{"id":"fc_1","type":"function_call","status":"incomplete","call_id":"call_1","name":"read_file","arguments":"{\"path\":"}]}}`)
	}))
	defer server.Close()
	var events []Event
	res, err := openaiTestClient(t, OpenAIResponses, server).Stream(context.Background(), openaiSimpleRequest(), func(e Event) error {
		events = append(events, e)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != StopMaxTokens || len(res.Message.Content) != 2 || res.Message.Content[0].Text != "部分回复" {
		t.Fatalf("截断结果错误：%+v", res)
	}
	call := res.Message.Content[1].ToolCall
	if call == nil || string(call.Arguments) != `{"path":` {
		t.Fatalf("被截断的工具调用应原样保留：%+v", call)
	}
	ends := 0
	for _, event := range events {
		if event.Type == EventBlockEnd {
			ends++
			if event.BlockIndex != 0 {
				t.Fatal("被截断的工具调用不应标记为完成")
			}
		}
	}
	if ends != 1 {
		t.Fatalf("截断场景的事件契约不正确：%+v", events)
	}
}

func TestOpenAIChatTextStreamingAndUsage(t *testing.T) {
	firstWritten := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := openaiAssertRequest(t, r, "/gateway/v1/chat/completions")
		if string(body["model"]) != `"gpt-test"` || string(body["stream"]) != "true" || string(body["max_completion_tokens"]) != "4096" {
			t.Errorf("模型名、流式或输出上限参数错误：%s", body)
		}
		if !strings.Contains(string(body["stream_options"]), `"include_usage":true`) {
			t.Errorf("应请求流末用量：%s", body["stream_options"])
		}
		var messages []map[string]json.RawMessage
		if err := json.Unmarshal(body["messages"], &messages); err != nil {
			t.Error(err)
		}
		if len(messages) != 2 || string(messages[0]["role"]) != `"system"` || string(messages[0]["content"]) != `"系统指令"` || string(messages[1]["role"]) != `"user"` || string(messages[1]["content"]) != `"你好"` {
			t.Errorf("系统指令或用户消息未正确映射：%s", body["messages"])
		}
		if !strings.Contains(string(body["tools"]), `"read_file"`) || !strings.Contains(string(body["tools"]), `"parameters"`) {
			t.Errorf("工具定义未传递：%s", body["tools"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		openaiChatChunk(w, `{"role":"assistant","content":""}`)
		openaiChatChunk(w, `{"content":"你好"}`)
		w.(http.Flusher).Flush()
		select {
		case <-firstWritten:
		case <-r.Context().Done():
			return
		}
		openaiChatChunk(w, `{"content":"，世界"}`)
		openaiChatFinish(w, "stop")
		openaiData(w, `{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-test","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17,"prompt_tokens_details":{"cached_tokens":6,"cache_write_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}}`)
		openaiData(w, "[DONE]")
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req := openaiSimpleRequest()
	req.System = "系统指令"
	req.Tools = []ToolDefinition{{Name: "read_file", Description: "读取文件", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}}
	var text strings.Builder
	var events []Event
	res, err := openaiTestClient(t, OpenAIChatCompletions, server).Stream(ctx, req, func(e Event) error {
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
	if text.String() != "你好，世界" || len(res.Message.Content) != 1 || res.Message.Content[0].Text != text.String() || res.StopReason != StopEndTurn {
		t.Fatalf("流式结果与完整结果不一致：%+v", res)
	}
	if len(events) != 4 || events[0].Type != EventBlockStart || events[3].Type != EventBlockEnd {
		t.Fatal("块事件契约不正确")
	}
	usage := res.Usage
	if usage.InputTokens == nil || *usage.InputTokens != 12 || usage.OutputTokens == nil || *usage.OutputTokens != 5 {
		t.Fatal("输入或输出用量映射错误")
	}
	if usage.CacheReadTokens == nil || *usage.CacheReadTokens != 6 || usage.CacheWriteTokens == nil || *usage.CacheWriteTokens != 2 || usage.ReasoningTokens == nil || *usage.ReasoningTokens != 1 {
		t.Fatal("缓存与推理分项映射错误")
	}
}

func TestOpenAIChatToolCallRoundTrip(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body := openaiAssertRequest(t, r, "/gateway/v1/chat/completions")
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 2 {
			var messages []map[string]json.RawMessage
			if err := json.Unmarshal(body["messages"], &messages); err != nil {
				t.Error(err)
			}
			if len(messages) != 3 {
				t.Fatalf("续接消息数量错误：%s", body["messages"])
			}
			assistant, tool := messages[1], messages[2]
			if string(assistant["role"]) != `"assistant"` || !strings.Contains(string(assistant["tool_calls"]), `"id":"call_1"`) || !strings.Contains(string(assistant["tool_calls"]), `"name":"read_file"`) || !strings.Contains(string(assistant["tool_calls"]), `"arguments":"{\"path\":\"main.go\"}"`) {
				t.Errorf("历史工具调用未正确传递：%s", body["messages"])
			}
			if content, ok := assistant["content"]; ok && string(content) != "null" {
				t.Errorf("仅有工具调用的助手消息不应携带正文：%s", content)
			}
			var toolContent string
			_ = json.Unmarshal(tool["content"], &toolContent)
			if string(tool["role"]) != `"tool"` || string(tool["tool_call_id"]) != `"call_1"` || toolContent != `{"content":"文件不存在","is_error":true}` {
				t.Errorf("工具结果未与调用关联：%s", tool)
			}
			openaiChatChunk(w, `{"content":"完成"}`)
			openaiChatFinish(w, "stop")
			openaiData(w, "[DONE]")
			return
		}
		openaiChatChunk(w, `{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":""}}]}`)
		openaiChatChunk(w, `{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}`)
		openaiChatChunk(w, `{"tool_calls":[{"index":0,"function":{"arguments":"\"main.go\"}"}}]}`)
		openaiChatFinish(w, "tool_calls")
		openaiData(w, "[DONE]")
	}))
	defer server.Close()
	client := openaiTestClient(t, OpenAIChatCompletions, server)
	res, err := client.Stream(context.Background(), openaiSimpleRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != StopToolUse || len(res.Message.Content) != 1 {
		t.Fatalf("工具调用结果错误：%+v", res)
	}
	call := res.Message.Content[0].ToolCall
	if call == nil || call.ID != "call_1" || call.Name != "read_file" || string(call.Arguments) != `{"path":"main.go"}` {
		t.Fatalf("工具调用组装错误：%+v", call)
	}
	req := openaiSimpleRequest()
	req.Messages = append(req.Messages, res.Message, Message{Role: RoleTool, Content: []ContentBlock{{Type: BlockToolResult, ToolResult: &ToolResult{ToolCallID: "call_1", Content: "文件不存在", IsError: true}}}})
	if _, err := client.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatal("续接请求没有发送")
	}
}

func TestOpenAIChatTerminalStatesAndTruncation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stream  func(w io.Writer)
		want    StopReason
		wantErr bool
	}{
		{"正常结束", func(w io.Writer) {
			openaiChatChunk(w, `{"content":"回复"}`)
			openaiChatFinish(w, "stop")
			openaiData(w, "[DONE]")
		}, StopEndTurn, false},
		{"长度截断", func(w io.Writer) {
			openaiChatChunk(w, `{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":"}}]}`)
			openaiChatFinish(w, "length")
			openaiData(w, "[DONE]")
		}, StopMaxTokens, false},
		{"内容过滤", func(w io.Writer) {
			openaiChatChunk(w, `{"content":"被过滤的回复"}`)
			openaiChatFinish(w, "content_filter")
			openaiData(w, "[DONE]")
		}, StopRefusal, false},
		{"未知结束原因", func(w io.Writer) {
			openaiChatChunk(w, `{"content":"回复"}`)
			openaiChatFinish(w, "unexpected")
			openaiData(w, "[DONE]")
		}, "", true},
		{"缺少结束原因", func(w io.Writer) {
			openaiChatChunk(w, `{"content":"回复"}`)
			openaiData(w, "[DONE]")
		}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				openaiAssertRequest(t, r, "/gateway/v1/chat/completions")
				w.Header().Set("Content-Type", "text/event-stream")
				tc.stream(w)
			}))
			defer server.Close()
			res, err := openaiTestClient(t, OpenAIChatCompletions, server).Stream(context.Background(), openaiSimpleRequest(), nil)
			if (err != nil) != tc.wantErr || res.StopReason != tc.want {
				t.Fatalf("结束状态错误：reason=%s err=%v", res.StopReason, err)
			}
			if tc.name == "长度截断" {
				call := res.Message.Content[0].ToolCall
				if call == nil || string(call.Arguments) != `{"path":` {
					t.Fatalf("被截断的工具调用应原样保留：%+v", call)
				}
			}
		})
	}
}

func TestOpenAIStreamsReportSafeErrors(t *testing.T) {
	for _, api := range []API{OpenAIResponses, OpenAIChatCompletions} {
		path := "/gateway/v1/responses"
		if api == OpenAIChatCompletions {
			path = "/gateway/v1/chat/completions"
		}
		for _, tc := range []struct {
			name   string
			status int
			kind   ErrorKind
			stream func(w io.Writer)
		}{
			{"认证失败", http.StatusUnauthorized, ErrAuthentication, nil},
			{"限流", http.StatusTooManyRequests, ErrRateLimit, nil},
			{"服务错误", http.StatusInternalServerError, ErrService, nil},
			{"流内错误", http.StatusOK, ErrService, func(w io.Writer) {
				if api == OpenAIChatCompletions {
					openaiData(w, `{"error":{"message":"invalid key: openai-key","type":"server_error"}}`)
					return
				}
				openaiData(w, `{"type":"error","code":"server_error","message":"invalid key: openai-key","param":null}`)
			}},
		} {
			t.Run(fmt.Sprintf("%s/%s", api, tc.name), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					openaiAssertRequest(t, r, path)
					if tc.stream == nil {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(tc.status)
						fmt.Fprint(w, `{"error":{"message":"invalid key: openai-key","type":"authentication_error"}}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					tc.stream(w)
				}))
				defer server.Close()
				res, err := openaiTestClient(t, api, server).Stream(context.Background(), openaiSimpleRequest(), nil)
				var modelErr *Error
				if !errors.As(err, &modelErr) || modelErr.Kind != tc.kind {
					t.Fatalf("错误分类错误：%v", err)
				}
				if strings.Contains(err.Error(), "openai-key") || len(res.Message.Content) != 0 {
					t.Fatalf("不应回显凭据或服务端载荷：%v", err)
				}
			})
		}
	}
}

func TestOpenAIToolResultErrorEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name string
		api  API
		path string
	}{
		{"Responses", OpenAIResponses, "/gateway/v1/responses"},
		{"Chat", OpenAIChatCompletions, "/gateway/v1/chat/completions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body := openaiAssertRequest(t, r, tc.path)
				failed, succeeded := "", ""
				if tc.api == OpenAIResponses {
					var items []map[string]json.RawMessage
					if err := json.Unmarshal(body["input"], &items); err != nil {
						t.Error(err)
					}
					for _, item := range items {
						var kind, callID, output string
						_ = json.Unmarshal(item["type"], &kind)
						if kind != "function_call_output" {
							continue
						}
						_ = json.Unmarshal(item["call_id"], &callID)
						_ = json.Unmarshal(item["output"], &output)
						switch callID {
						case "call_1":
							failed = output
						case "call_2":
							succeeded = output
						}
					}
				} else {
					var messages []map[string]json.RawMessage
					if err := json.Unmarshal(body["messages"], &messages); err != nil {
						t.Error(err)
					}
					for _, message := range messages {
						var role, callID, content string
						_ = json.Unmarshal(message["role"], &role)
						if role != "tool" {
							continue
						}
						_ = json.Unmarshal(message["tool_call_id"], &callID)
						_ = json.Unmarshal(message["content"], &content)
						switch callID {
						case "call_1":
							failed = content
						case "call_2":
							succeeded = content
						}
					}
				}
				if failed != `{"content":"文件不存在","is_error":true}` {
					t.Errorf("失败的工具结果应保留错误语义：%q", failed)
				}
				if succeeded != "文件内容" {
					t.Errorf("成功的工具结果应保持原文：%q", succeeded)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				openaiSimpleStream(w, tc.api, "完成")
			}))
			defer server.Close()
			req := openaiSimpleRequest()
			req.Messages = append(req.Messages,
				Message{Role: RoleAssistant, Content: []ContentBlock{
					{Type: BlockToolCall, ToolCall: &ToolCall{ID: "call_1", Name: "read_file", Arguments: json.RawMessage(`{"path":"missing.go"}`)}},
					{Type: BlockToolCall, ToolCall: &ToolCall{ID: "call_2", Name: "read_file", Arguments: json.RawMessage(`{"path":"main.go"}`)}},
				}},
				Message{Role: RoleTool, Content: []ContentBlock{
					{Type: BlockToolResult, ToolResult: &ToolResult{ToolCallID: "call_1", Content: "文件不存在", IsError: true}},
					{Type: BlockToolResult, ToolResult: &ToolResult{ToolCallID: "call_2", Content: "文件内容"}},
				}},
			)
			if _, err := openaiTestClient(t, tc.api, server).Stream(context.Background(), req, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenAIChatRejectsUnrepresentableContent(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		openaiData(w, `{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"reasoning_content":"思考中"}}]}`)
	}))
	defer server.Close()
	client := openaiTestClient(t, OpenAIChatCompletions, server)
	// 思考块无法用 Chat Completions 表达，来源匹配时必须由适配器本身明确拒绝。
	thinking := openaiSimpleRequest()
	thinking.Messages = append(thinking.Messages, Message{Role: RoleAssistant, Content: []ContentBlock{{Type: BlockThinking, Replay: &Replay{
		Source: Source{API: OpenAIChatCompletions, Provider: strings.TrimRight(server.URL+"/gateway/v1/", "/"), Model: "gpt-test"},
		Data:   json.RawMessage(`{"type":"reasoning"}`),
	}}}})
	res, err := client.Stream(context.Background(), thinking, nil)
	var modelErr *Error
	if !errors.As(err, &modelErr) || modelErr.Kind != ErrUnsupported || !strings.Contains(err.Error(), "无法表达思考内容") {
		t.Fatalf("思考块应被适配器明确拒绝：%v", err)
	}
	if requests != 0 || len(res.Message.Content) != 0 {
		t.Fatal("不支持的请求不应发往服务")
	}
	// 额外增量字段（如推理模型的 reasoning_content）无法无损表达，也必须明确失败。
	res, err = client.Stream(context.Background(), openaiSimpleRequest(), nil)
	if !errors.As(err, &modelErr) || modelErr.Kind != ErrUnsupported {
		t.Fatalf("额外增量字段应被明确拒绝：%v", err)
	}
	if strings.Contains(err.Error(), "思考中") || len(res.Message.Content) != 0 {
		t.Fatal("不应回显服务端增量载荷")
	}
}

func TestOpenAITruncationKeepsCallbackFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		api    API
		path   string
		stream func(w io.Writer)
	}{
		{"Responses", OpenAIResponses, "/gateway/v1/responses", func(w io.Writer) {
			openaiData(w, `{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"read_file","arguments":""}}`)
			openaiData(w, `{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"{\"path\":\"main.go\"}"}`)
			openaiData(w, `{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"fc_1","type":"function_call","status":"incomplete","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"main.go\"}"}]}}`)
		}},
		{"Chat", OpenAIChatCompletions, "/gateway/v1/chat/completions", func(w io.Writer) {
			openaiChatChunk(w, `{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"main.go\"}"}}]}`)
			openaiChatFinish(w, "length")
			openaiData(w, "[DONE]")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				openaiAssertRequest(t, r, tc.path)
				w.Header().Set("Content-Type", "text/event-stream")
				tc.stream(w)
			}))
			defer server.Close()
			outputErr := errors.New("输出中断")
			_, err := openaiTestClient(t, tc.api, server).Stream(context.Background(), openaiSimpleRequest(), func(e Event) error {
				if e.Type == EventBlockEnd {
					return outputErr
				}
				return nil
			})
			if !errors.Is(err, outputErr) {
				t.Fatalf("截断路径不应吞掉输出回调错误：%v", err)
			}
		})
	}
}

func TestOpenAICancellationAndCallbackFailure(t *testing.T) {
	cases := []struct {
		name  string
		api   API
		path  string
		open  string
		delta string
	}{
		{"Responses", OpenAIResponses, "/gateway/v1/responses",
			`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
			`{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_1","delta":"开始回复"}`},
		{"Chat", OpenAIChatCompletions, "/gateway/v1/chat/completions",
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"content":"开始回复"}}]}`},
	}
	for _, tc := range cases {
		for _, cancelRequest := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/%v", tc.name, cancelRequest), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					openaiAssertRequest(t, r, tc.path)
					w.Header().Set("Content-Type", "text/event-stream")
					openaiData(w, tc.open)
					openaiData(w, tc.delta)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				outputErr := errors.New("输出中断")
				res, err := openaiTestClient(t, tc.api, server).Stream(ctx, openaiSimpleRequest(), func(e Event) error {
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
				if !errors.Is(err, want) || res.StopReason != "" || len(res.Message.Content) != 1 {
					t.Fatalf("未正确处理取消或输出错误：%v", err)
				}
			})
		}
	}
}
