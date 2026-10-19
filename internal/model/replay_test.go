package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResponsesReplaysOutputMessageMetadata(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body struct {
			Input []json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if requests == 2 {
			if len(body.Input) != 3 {
				t.Error("历史消息数量错误")
			} else {
				var item struct{ ID, Phase string }
				if err := json.Unmarshal(body.Input[1], &item); err != nil || item.ID != "msg_1" || item.Phase != "commentary" {
					t.Error("输出消息的 ID 和 phase 未保留")
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"response_1\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_1\",\"status\":\"completed\",\"role\":\"assistant\",\"phase\":\"commentary\",\"content\":[{\"type\":\"output_text\",\"text\":\"回复\",\"annotations\":[]}]}]}}\n\n")
	}))
	defer server.Close()
	client, err := NewClient(Options{API: OpenAIResponses, BaseURL: server.URL, APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	req := textRequest()
	res, err := client.Stream(context.Background(), req, nil)
	if err != nil || len(res.Message.Content) != 1 || res.Message.Content[0].Text != "回复" || res.Message.Content[0].Replay == nil {
		t.Fatalf("终态消息组装错误：%+v %v", res, err)
	}
	req.Messages = append(req.Messages, res.Message, Message{Role: RoleUser, Content: []ContentBlock{{Type: BlockText, Text: "继续"}}})
	if _, err := client.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
}

func TestAnthropicTruncatedToolsKeepStopReason(t *testing.T) {
	for _, args := range []string{`{"path":`, `{"path":"main.go"}`} {
		t.Run(args, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				anthropicEvent(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"read_file","input":{}}}`)
				anthropicEvent(w, "content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%q}}`, args))
				sendStop(w, "max_tokens")
			}))
			defer server.Close()
			res, err := makeTestClient(t, server).Stream(context.Background(), textRequest(), nil)
			if err != nil || res.StopReason != StopMaxTokens {
				t.Fatalf("应保留达到上限的结束原因：%+v %v", res, err)
			}
			callbackErr := errors.New("终态输出失败")
			_, err = makeTestClient(t, server).Stream(context.Background(), textRequest(), func(e Event) error {
				if e.Type == EventBlockEnd {
					return callbackErr
				}
				return nil
			})
			if jsonObject(json.RawMessage(args)) && !errors.Is(err, callbackErr) {
				t.Fatalf("不能吞掉工具块结束时的输出错误：%v", err)
			}
		})
	}
}
