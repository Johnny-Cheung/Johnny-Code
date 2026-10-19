package agent_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func TestAnthropicEmptyToolResultsContinue(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "empty.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	calls := []protocolToolCall{
		{Name: "bash", CallID: "silent-command", Arguments: `{"command":"true"}`},
		{Name: "read", CallID: "empty-file", Arguments: `{"path":"empty.txt"}`},
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if request == 1 {
			protocolWriteAnthropicFirst(w, calls)
			return
		}
		var body protocolAnthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		count := 0
		for _, message := range body.Messages {
			for _, block := range message.Content {
				if block.Type != "tool_result" {
					continue
				}
				if count >= len(calls) || block.ToolUseID != calls[count].CallID || block.IsError || len(block.Content) != 0 {
					t.Errorf("空成功结果不应包含空 text 块：%+v", block)
					http.Error(w, "text content blocks must be non-empty", http.StatusBadRequest)
					return
				}
				count++
			}
		}
		if count != len(calls) {
			t.Errorf("结果数为 %d，预期 %d", count, len(calls))
		}
		protocolWriteAnthropicFinal(w)
	}))
	defer server.Close()
	client, err := model.NewClient(model.Options{API: model.AnthropicMessages, BaseURL: server.URL, APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := tool.NewRuntime(work)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tool.NewDefaultRegistry(runtime)
	if err != nil {
		t.Fatal(err)
	}
	results := 0
	result, err := agent.Run(context.Background(), agent.Options{Client: client, Tools: registry, Model: "test-model"}, "验证静默成功结果", func(event agent.Event) error {
		if event.Type == agent.EventToolEnd {
			results++
			if event.ToolResult.Content != "" || event.ToolResult.IsError {
				t.Errorf("协议转换不应改变原始工具结果：%+v", event.ToolResult)
			}
		}
		return nil
	})
	if err != nil || result.Status != agent.StatusCompleted || requests.Load() != 2 || results != 2 {
		t.Fatalf("空工具结果未能继续：result=%+v err=%v requests=%d results=%d", result, err, requests.Load(), results)
	}
}
