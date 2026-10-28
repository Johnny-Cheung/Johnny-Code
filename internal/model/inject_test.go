package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 子任务结果会在工具结果之后以 user 消息注入，三种协议都必须原样接受。
func TestInjectedUserMessageAfterToolResultsIsAccepted(t *testing.T) {
	injected := "[子任务 ab12 状态：completed]\n报告正文"
	for _, api := range []API{AnthropicMessages, OpenAIResponses, OpenAIChatCompletions} {
		t.Run(string(api), func(t *testing.T) {
			var body map[string]json.RawMessage
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if api == AnthropicMessages {
					anthropicTextStart(w)
					sendText(w, "完成")
					sendStop(w, "end_turn")
					return
				}
				openaiSimpleStream(w, api, "完成")
			}))
			defer server.Close()
			client, err := NewClient(Options{API: api, APIKey: "test-key", BaseURL: server.URL + "/gateway"})
			if err != nil {
				t.Fatal(err)
			}
			call := &ToolCall{ID: "call_1", Name: "read_file", Arguments: json.RawMessage(`{}`)}
			req := Request{
				Model: "model",
				Messages: []Message{
					{Role: RoleUser, Content: []ContentBlock{{Type: BlockText, Text: "任务"}}},
					{Role: RoleAssistant, Content: []ContentBlock{{Type: BlockToolCall, ToolCall: call}}},
					{Role: RoleTool, Content: []ContentBlock{{Type: BlockToolResult, ToolResult: &ToolResult{ToolCallID: "call_1", Content: "ok"}}}},
					{Role: RoleUser, Content: []ContentBlock{{Type: BlockText, Text: injected}}},
					{Role: RoleUser, Content: []ContentBlock{{Type: BlockText, Text: "[子任务 cd34 状态：failed]"}}},
				},
				Tools:           []ToolDefinition{{Name: "read_file", Description: "读取", InputSchema: json.RawMessage(`{"type":"object"}`)}},
				MaxOutputTokens: 4096,
			}
			if _, err := client.Stream(context.Background(), req, nil); err != nil {
				t.Fatalf("注入后的历史应被接受：%v", err)
			}
			encoded := string(body["messages"])
			if api == OpenAIResponses {
				encoded = string(body["input"])
			}
			if !strings.Contains(encoded, "报告正文") || !strings.Contains(encoded, "cd34") {
				t.Fatalf("注入消息没有发送给模型：%s", encoded)
			}
			if strings.Index(encoded, "call_1") > strings.Index(encoded, "报告正文") {
				t.Fatalf("注入消息应排在工具结果之后：%s", encoded)
			}
		})
	}
}
