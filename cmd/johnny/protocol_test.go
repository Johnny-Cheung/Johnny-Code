package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIModelSwitchingAcrossAPIs(t *testing.T) {
	for _, api := range []string{"anthropic-messages", "openai-responses", "openai-chat-completions"} {
		t.Run(api, func(t *testing.T) {
			count := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count++
				var body struct{ Model string }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Model != "model-a" && body.Model != "model-b" {
					t.Error("--model 未覆盖配置文件")
				}
				wantPath := map[string]string{"anthropic-messages": "/gateway/v1/messages", "openai-responses": "/gateway/v1/responses", "openai-chat-completions": "/gateway/v1/chat/completions"}[api]
				if r.URL.Path != wantPath || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("切换协议后的路径或认证错误：%s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				switch api {
				case "anthropic-messages":
					fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
					fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", body.Model)
					fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				case "openai-chat-completions":
					fmt.Fprintf(w, "data: {\"id\":\"chat-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%q},\"finish_reason\":null}]}\n\n", body.Model)
					fmt.Fprint(w, "data: {\"id\":\"chat-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				case "openai-responses":
					item := fmt.Sprintf(`{"type":"message","id":"msg-1","role":"assistant","status":"completed","content":[{"type":"output_text","text":%q,"annotations":[]}]}`, body.Model)
					for _, event := range []string{
						`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg-1","role":"assistant","status":"in_progress","content":[]}}`,
						`{"type":"response.content_part.added","output_index":0,"content_index":0,"item_id":"msg-1","part":{"type":"output_text","text":"","annotations":[]}}`,
						fmt.Sprintf(`{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg-1","delta":%q}`, body.Model),
						fmt.Sprintf(`{"type":"response.output_text.done","output_index":0,"content_index":0,"item_id":"msg-1","text":%q}`, body.Model),
						fmt.Sprintf(`{"type":"response.content_part.done","output_index":0,"content_index":0,"item_id":"msg-1","part":{"type":"output_text","text":%q,"annotations":[]}}`, body.Model),
						fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":%s}`, item),
						fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[%s],"usage":{"input_tokens":2,"output_tokens":2,"total_tokens":4}}}`, item),
					} {
						fmt.Fprintf(w, "data: %s\n\n", event)
					}
				}
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), ".env")
			data := "JOHNNY_API_KEY=test-key\nJOHNNY_BASE_URL=" + server.URL + "/gateway/v1\nJOHNNY_MODEL=file-model\nJOHNNY_API=invalid\n"
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			useConfigPath(t, path)
			for _, name := range []string{"model-a", "model-b"} {
				var stdout, stderr bytes.Buffer
				code := run(context.Background(), []string{"--api", api, "--model", name, "你好"}, strings.NewReader(""), &stdout, &stderr)
				if code != 0 || stdout.String() != name+"\n" {
					t.Fatalf("模型 %s 切换失败：code=%d stdout=%q stderr=%s", name, code, stdout.String(), stderr.String())
				}
			}
			if count != 2 {
				t.Fatal("每个模型应只调用一次，不应自动重试")
			}
			actual, err := os.ReadFile(path)
			if err != nil || string(actual) != data {
				t.Fatal("临时切换不应修改配置文件")
			}
		})
	}
}
