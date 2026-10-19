package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

type protocolToolCall struct {
	Name           string
	CallID         string
	ItemID         string
	Arguments      string
	ResultContains string
	ResultExact    string
}

type protocolToolResult struct {
	ID      string
	Content string
}

func protocolToolCalls() []protocolToolCall {
	return []protocolToolCall{
		{
			Name:           "write",
			CallID:         "call_write",
			ItemID:         "fc_write",
			Arguments:      `{"path":"note.txt","content":"hello"}`,
			ResultContains: "已写入 note.txt",
		},
		{
			Name:           "read",
			CallID:         "call_read",
			ItemID:         "fc_read",
			Arguments:      `{"path":"note.txt"}`,
			ResultContains: "hello",
		},
		{
			Name:           "edit",
			CallID:         "call_edit",
			ItemID:         "fc_edit",
			Arguments:      `{"path":"note.txt","old_string":"hello","new_string":"world"}`,
			ResultContains: "已更新 note.txt",
		},
		{
			Name:        "bash",
			CallID:      "call_bash",
			ItemID:      "fc_bash",
			Arguments:   `{"command":"cat note.txt"}`,
			ResultExact: "world",
		},
	}
}

func protocolSSEData(w io.Writer, data string) {
	fmt.Fprintf(w, "data: %s\n\n", data)
}

func protocolAnthropicEvent(w io.Writer, kind, data string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
}

func protocolWriteAnthropicFirst(w io.Writer, calls []protocolToolCall) {
	protocolAnthropicEvent(w, "message_start", `{"type":"message_start","message":{"id":"msg_1","role":"assistant","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}`)
	protocolAnthropicEvent(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`)
	protocolAnthropicEvent(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"先分析"}}`)
	protocolAnthropicEvent(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-1"}}`)
	protocolAnthropicEvent(w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
	for i, call := range calls {
		index := i + 1
		protocolAnthropicEvent(w, "content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":%q,"name":%q,"input":{}}}`, index, call.CallID, call.Name))
		protocolAnthropicEvent(w, "content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":%q}}`, index, call.Arguments))
		protocolAnthropicEvent(w, "content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, index))
	}
	protocolAnthropicEvent(w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`)
	protocolAnthropicEvent(w, "message_stop", `{"type":"message_stop"}`)
}

func protocolWriteAnthropicFinal(w io.Writer) {
	protocolAnthropicEvent(w, "message_start", `{"type":"message_start","message":{"id":"msg_2","role":"assistant","content":[],"usage":{"input_tokens":20,"output_tokens":0}}}`)
	protocolAnthropicEvent(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	protocolAnthropicEvent(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"完成"}}`)
	protocolAnthropicEvent(w, "content_block_stop", `{"type":"content_block_stop","index":0}`)
	protocolAnthropicEvent(w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`)
	protocolAnthropicEvent(w, "message_stop", `{"type":"message_stop"}`)
}

func protocolWriteChatFirst(w io.Writer, calls []protocolToolCall) {
	toolCalls := make([]string, len(calls))
	for i, call := range calls {
		toolCalls[i] = fmt.Sprintf(`{"index":%d,"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}`, i, call.CallID, call.Name, call.Arguments)
	}
	protocolSSEData(w, fmt.Sprintf(`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"protocol-test","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[%s]}}]}`, strings.Join(toolCalls, ",")))
	protocolSSEData(w, `{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"protocol-test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
	protocolSSEData(w, "[DONE]")
}

func protocolWriteChatFinal(w io.Writer) {
	protocolSSEData(w, `{"id":"chatcmpl-2","object":"chat.completion.chunk","model":"protocol-test","choices":[{"index":0,"delta":{"content":"完成"}}]}`)
	protocolSSEData(w, `{"id":"chatcmpl-2","object":"chat.completion.chunk","model":"protocol-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	protocolSSEData(w, "[DONE]")
}

func protocolWriteResponsesFirst(w io.Writer, calls []protocolToolCall) {
	protocolSSEData(w, `{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`)
	protocolSSEData(w, `{"type":"response.reasoning_summary_text.delta","output_index":0,"item_id":"rs_1","summary_index":0,"delta":"先分析"}`)
	reasoning := `{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"先分析"}],"encrypted_content":"enc-1"}`
	protocolSSEData(w, `{"type":"response.output_item.done","output_index":0,"item":`+reasoning+`}`)

	items := []string{reasoning}
	for i, call := range calls {
		outputIndex := i + 1
		added := fmt.Sprintf(`{"id":%q,"type":"function_call","status":"in_progress","call_id":%q,"name":%q,"arguments":""}`, call.ItemID, call.CallID, call.Name)
		protocolSSEData(w, fmt.Sprintf(`{"type":"response.output_item.added","output_index":%d,"item":%s}`, outputIndex, added))
		protocolSSEData(w, fmt.Sprintf(`{"type":"response.function_call_arguments.delta","output_index":%d,"item_id":%q,"delta":%q}`, outputIndex, call.ItemID, call.Arguments))
		done := fmt.Sprintf(`{"id":%q,"type":"function_call","status":"completed","call_id":%q,"name":%q,"arguments":%q}`, call.ItemID, call.CallID, call.Name, call.Arguments)
		protocolSSEData(w, fmt.Sprintf(`{"type":"response.output_item.done","output_index":%d,"item":%s}`, outputIndex, done))
		items = append(items, done)
	}
	protocolSSEData(w, fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[%s]}}`, strings.Join(items, ",")))
}

func protocolWriteResponsesFinal(w io.Writer) {
	protocolSSEData(w, `{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_2","type":"message","status":"in_progress","role":"assistant","content":[]}}`)
	protocolSSEData(w, `{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_2","delta":"完成"}`)
	protocolSSEData(w, `{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"完成","annotations":[]}]}}`)
	protocolSSEData(w, `{"type":"response.completed","response":{"id":"resp_2","status":"completed","output":[{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"完成","annotations":[]}]}]}}`)
}

func protocolJSONEqual(raw json.RawMessage, want string) bool {
	var gotValue, wantValue any
	if err := json.Unmarshal(raw, &gotValue); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		return false
	}
	return reflect.DeepEqual(gotValue, wantValue)
}

func protocolCheckToolDefinitions(raw json.RawMessage, calls []protocolToolCall) error {
	if len(raw) == 0 {
		return fmt.Errorf("tool definitions are missing")
	}
	definitions := string(raw)
	for _, call := range calls {
		if !strings.Contains(definitions, fmt.Sprintf(`"name":%q`, call.Name)) {
			return fmt.Errorf("tool definition %q is missing", call.Name)
		}
	}
	return nil
}

func protocolAssertToolResults(calls []protocolToolCall, results []protocolToolResult) error {
	if len(results) != len(calls) {
		return fmt.Errorf("got %d tool results, want %d: %+v", len(results), len(calls), results)
	}
	for i, call := range calls {
		result := results[i]
		if result.ID != call.CallID {
			return fmt.Errorf("tool result %d id = %q, want %q", i, result.ID, call.CallID)
		}
		if call.ResultExact != "" && result.Content != call.ResultExact {
			return fmt.Errorf("tool result %q = %q, want %q", call.Name, result.Content, call.ResultExact)
		}
		if call.ResultContains != "" && !strings.Contains(result.Content, call.ResultContains) {
			return fmt.Errorf("tool result %q = %q, want it to contain %q", call.Name, result.Content, call.ResultContains)
		}
	}
	return nil
}

type protocolAnthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
}

type protocolAnthropicRequest struct {
	Messages []struct {
		Role    string                   `json:"role"`
		Content []protocolAnthropicBlock `json:"content"`
	} `json:"messages"`
	Tools json.RawMessage `json:"tools"`
}

func protocolAnthropicText(raw json.RawMessage) (string, error) {
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("decode Anthropic tool result content: %w", err)
	}
	var text strings.Builder
	for _, part := range parts {
		text.WriteString(part.Text)
	}
	return text.String(), nil
}

func protocolVerifyAnthropicSecond(body []byte, calls []protocolToolCall) error {
	var request protocolAnthropicRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return fmt.Errorf("decode Anthropic request: %w", err)
	}
	if err := protocolCheckToolDefinitions(request.Tools, calls); err != nil {
		return err
	}

	var assistant []protocolAnthropicBlock
	assistantSeen := false
	for _, message := range request.Messages {
		if message.Role == "assistant" {
			assistant = message.Content
			assistantSeen = true
			break
		}
	}
	if !assistantSeen {
		return fmt.Errorf("assistant message is missing")
	}
	if len(assistant) != len(calls)+1 {
		return fmt.Errorf("assistant has %d blocks, want thinking + %d tool calls", len(assistant), len(calls))
	}
	thinking := assistant[0]
	if thinking.Type != "thinking" || thinking.Thinking != "先分析" || thinking.Signature != "sig-1" {
		return fmt.Errorf("thinking metadata was not replayed: %+v", thinking)
	}
	for i, call := range calls {
		block := assistant[i+1]
		if block.Type != "tool_use" || block.ID != call.CallID || block.Name != call.Name {
			return fmt.Errorf("tool call %d = %+v, want id %q name %q", i, block, call.CallID, call.Name)
		}
		if !protocolJSONEqual(block.Input, call.Arguments) {
			return fmt.Errorf("tool call %q arguments = %s, want %s", call.Name, block.Input, call.Arguments)
		}
	}

	var results []protocolToolResult
	for _, message := range request.Messages {
		for _, block := range message.Content {
			if block.Type != "tool_result" {
				continue
			}
			if message.Role != "user" {
				return fmt.Errorf("Anthropic tool result was sent with role %q", message.Role)
			}
			if block.IsError {
				return fmt.Errorf("tool result %q unexpectedly marked as error", block.ToolUseID)
			}
			content, err := protocolAnthropicText(block.Content)
			if err != nil {
				return err
			}
			results = append(results, protocolToolResult{ID: block.ToolUseID, Content: content})
		}
	}
	return protocolAssertToolResults(calls, results)
}

type protocolChatRequest struct {
	Messages []struct {
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		ToolCalls []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
		ToolCallID string `json:"tool_call_id"`
	} `json:"messages"`
	Tools json.RawMessage `json:"tools"`
}

func protocolVerifyChatSecond(body []byte, calls []protocolToolCall) error {
	var request protocolChatRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return fmt.Errorf("decode Chat request: %w", err)
	}
	if err := protocolCheckToolDefinitions(request.Tools, calls); err != nil {
		return err
	}

	assistantSeen := false
	for _, message := range request.Messages {
		if message.Role != "assistant" {
			continue
		}
		assistantSeen = true
		if len(message.ToolCalls) != len(calls) {
			return fmt.Errorf("assistant has %d tool calls, want %d", len(message.ToolCalls), len(calls))
		}
		for i, call := range calls {
			toolCall := message.ToolCalls[i]
			if toolCall.ID != call.CallID || toolCall.Type != "function" || toolCall.Function.Name != call.Name || toolCall.Function.Arguments != call.Arguments {
				return fmt.Errorf("tool call %d = %+v, want id %q name %q arguments %s", i, toolCall, call.CallID, call.Name, call.Arguments)
			}
		}
		break
	}
	if !assistantSeen {
		return fmt.Errorf("assistant message is missing")
	}

	var results []protocolToolResult
	for _, message := range request.Messages {
		if message.Role != "tool" {
			continue
		}
		var content string
		if err := json.Unmarshal(message.Content, &content); err != nil {
			return fmt.Errorf("decode Chat tool result: %w", err)
		}
		results = append(results, protocolToolResult{ID: message.ToolCallID, Content: content})
	}
	return protocolAssertToolResults(calls, results)
}

type protocolResponsesItem struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	CallID           string          `json:"call_id"`
	Name             string          `json:"name"`
	Arguments        string          `json:"arguments"`
	EncryptedContent string          `json:"encrypted_content"`
	Output           json.RawMessage `json:"output"`
}

type protocolResponsesRequest struct {
	Input []protocolResponsesItem `json:"input"`
	Tools json.RawMessage         `json:"tools"`
}

func protocolVerifyResponsesSecond(body []byte, calls []protocolToolCall) error {
	var request protocolResponsesRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return fmt.Errorf("decode Responses request: %w", err)
	}
	if err := protocolCheckToolDefinitions(request.Tools, calls); err != nil {
		return err
	}

	reasoningIndex, firstCallIndex, lastCallIndex, firstOutputIndex := -1, -1, -1, -1
	var callItems []protocolResponsesItem
	var results []protocolToolResult
	for i, item := range request.Input {
		switch item.Type {
		case "reasoning":
			if reasoningIndex >= 0 {
				return fmt.Errorf("duplicate reasoning item")
			}
			reasoningIndex = i
		case "function_call":
			if firstCallIndex < 0 {
				firstCallIndex = i
			}
			lastCallIndex = i
			callItems = append(callItems, item)
		case "function_call_output":
			if firstOutputIndex < 0 {
				firstOutputIndex = i
			}
			var content string
			if err := json.Unmarshal(item.Output, &content); err != nil {
				return fmt.Errorf("decode Responses tool output: %w", err)
			}
			results = append(results, protocolToolResult{ID: item.CallID, Content: content})
		}
	}

	if reasoningIndex < 0 {
		return fmt.Errorf("reasoning item is missing")
	}
	if request.Input[reasoningIndex].EncryptedContent != "enc-1" {
		return fmt.Errorf("reasoning encrypted_content = %q, want enc-1", request.Input[reasoningIndex].EncryptedContent)
	}
	if len(callItems) != len(calls) {
		return fmt.Errorf("got %d function call items, want %d", len(callItems), len(calls))
	}
	if !(reasoningIndex < firstCallIndex && lastCallIndex < firstOutputIndex) {
		return fmt.Errorf("input order is wrong: reasoning=%d calls=%d..%d outputs start=%d", reasoningIndex, firstCallIndex, lastCallIndex, firstOutputIndex)
	}
	for i, call := range calls {
		item := callItems[i]
		if item.ID != call.ItemID {
			return fmt.Errorf("function call %d output item id = %q, want %q", i, item.ID, call.ItemID)
		}
		if item.CallID != call.CallID || item.Name != call.Name || item.Arguments != call.Arguments {
			return fmt.Errorf("function call %d = %+v, want call_id %q name %q arguments %s", i, item, call.CallID, call.Name, call.Arguments)
		}
	}
	return protocolAssertToolResults(calls, results)
}

func TestProtocolToolRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		api    model.API
		path   string
		first  func(io.Writer, []protocolToolCall)
		final  func(io.Writer)
		verify func([]byte, []protocolToolCall) error
	}{
		{
			name:   "anthropic-messages",
			api:    model.AnthropicMessages,
			path:   "/gateway/v1/messages",
			first:  protocolWriteAnthropicFirst,
			final:  protocolWriteAnthropicFinal,
			verify: protocolVerifyAnthropicSecond,
		},
		{
			name:   "openai-responses",
			api:    model.OpenAIResponses,
			path:   "/gateway/v1/responses",
			first:  protocolWriteResponsesFirst,
			final:  protocolWriteResponsesFinal,
			verify: protocolVerifyResponsesSecond,
		},
		{
			name:   "openai-chat-completions",
			api:    model.OpenAIChatCompletions,
			path:   "/gateway/v1/chat/completions",
			first:  protocolWriteChatFirst,
			final:  protocolWriteChatFinal,
			verify: protocolVerifyChatSecond,
		},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			calls := protocolToolCalls()
			workDir := t.TempDir()
			runtime, err := tool.NewRuntime(workDir)
			if err != nil {
				t.Fatalf("tool.NewRuntime() error = %v", err)
			}
			registry, err := tool.NewDefaultRegistry(runtime)
			if err != nil {
				t.Fatalf("tool.NewDefaultRegistry() error = %v", err)
			}

			var requestCount atomic.Int32
			secondRequest := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestNumber := requestCount.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != testCase.path {
					t.Errorf("request %d path = %s %s, want POST %s", requestNumber, r.Method, r.URL.Path, testCase.path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer protocol-key" {
					t.Errorf("request %d Authorization = %q", requestNumber, got)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request %d body: %v", requestNumber, err)
					http.Error(w, "read body", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				switch requestNumber {
				case 1:
					testCase.first(w, calls)
				case 2:
					secondRequest <- body
					testCase.final(w)
				default:
					t.Errorf("unexpected request %d", requestNumber)
				}
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}))
			defer server.Close()

			client, err := model.NewClient(model.Options{
				API:     testCase.api,
				APIKey:  "protocol-key",
				BaseURL: server.URL + "/gateway",
			})
			if err != nil {
				t.Fatalf("model.NewClient() error = %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := agent.Run(ctx, agent.Options{
				Client:   client,
				Tools:    registry,
				Model:    "protocol-test",
				MaxTurns: 2,
			}, "请把 note.txt 从 hello 改成 world，再读取并确认", nil)
			if err != nil {
				t.Fatalf("agent.Run() error = %v", err)
			}
			if result.Status != agent.StatusCompleted {
				t.Fatalf("result status = %q, want completed", result.Status)
			}
			if result.Turns != 2 {
				t.Fatalf("result turns = %d, want 2", result.Turns)
			}
			if got := requestCount.Load(); got != 2 {
				t.Fatalf("request count = %d, want 2", got)
			}

			var body []byte
			select {
			case body = <-secondRequest:
			default:
				t.Fatal("second model request was not observed")
			}
			if err := testCase.verify(body, calls); err != nil {
				t.Fatalf("second request is invalid: %v", err)
			}

			content, err := os.ReadFile(filepath.Join(workDir, "note.txt"))
			if err != nil {
				t.Fatalf("read note.txt: %v", err)
			}
			if string(content) != "world" {
				t.Fatalf("note.txt = %q, want world", content)
			}

			var finalText strings.Builder
			for _, block := range result.Message.Content {
				if block.Type == model.BlockText {
					finalText.WriteString(block.Text)
				}
			}
			if finalText.String() != "完成" {
				t.Fatalf("final text = %q, want 完成", finalText.String())
			}
		})
	}
}
