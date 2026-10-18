package model

import (
	"bytes"
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

	"github.com/Johnny-Cheung/Johnny-Code/internal/config"
)

type observingWriter struct {
	buffer  bytes.Buffer
	onWrite func()
}

func (w *observingWriter) Write(p []byte) (int, error) {
	n, err := w.buffer.Write(p)
	if w.onWrite != nil {
		w.onWrite()
		w.onWrite = nil
	}
	return n, err
}

func testConfig(server *httptest.Server) config.Config {
	return config.Config{
		APIKey:  "test-key",
		BaseURL: server.URL + "/gateway",
		Model:   "relay-deepseek",
	}
}

func sendText(w io.Writer, text string) {
	fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", text)
}

func sendStop(w io.Writer, reason string) {
	fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":2}}\n\n", reason)
	fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
}

func TestStreamOutputsBeforeResponseCompletes(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "unrelated-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "unrelated-token")
	firstWritten := make(chan struct{})
	output := &observingWriter{onWrite: func() { close(firstWritten) }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/gateway/v1/messages" {
			t.Errorf("请求方法或地址错误：%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("X-Api-Key") != "" {
			t.Error("应只使用当前配置的 Bearer Token，不能混入其他凭据")
		}
		if r.Header.Get("Anthropic-Version") == "" {
			t.Error("缺少原生协议版本头")
		}
		var body struct {
			Model     string `json:"model"`
			Stream    bool   `json:"stream"`
			MaxTokens int64  `json:"max_tokens"`
			Messages  []struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "relay-deepseek" || !body.Stream || body.MaxTokens != 4096 || len(body.Messages) != 1 || body.Messages[0].Role != "user" || len(body.Messages[0].Content) != 1 || body.Messages[0].Content[0].Text != "你好" {
			t.Error("请求未正确传递模型名称、流式选项或用户输入")
		}
		w.Header().Set("Content-Type", "text/event-stream")
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

	if err := Stream(ctx, testConfig(server), "你好", output); err != nil {
		t.Fatal(err)
	}
	if got := output.buffer.String(); got != "你好，世界\n" {
		t.Fatalf("流式输出错误：%q", got)
	}
}

func TestStreamRejectsIncompleteResponses(t *testing.T) {
	for _, reason := range []string{"", "max_tokens"} {
		t.Run("stop_reason="+reason, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				sendText(w, "部分回复")
				if reason != "" {
					sendStop(w, reason)
				}
			}))
			defer server.Close()
			var output bytes.Buffer
			if err := Stream(context.Background(), testConfig(server), "你好", &output); err == nil {
				t.Fatal("不完整的回复应返回错误")
			}
			if output.String() != "部分回复\n" {
				t.Fatal("应保留已收到的部分回复")
			}
		})
	}
}

func TestStreamReportsHTTPErrorWithoutResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"type":"error","error":{"message":"invalid key: test-key","type":"authentication_error"}}`)
	}))
	defer server.Close()
	var output bytes.Buffer
	err := Stream(context.Background(), testConfig(server), "你好", &output)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("应报告 HTTP 状态，实际为 %v", err)
	}
	if strings.Contains(err.Error(), "test-key") || output.Len() != 0 {
		t.Fatal("不应输出凭据或把错误响应当作模型回复")
	}
}

func TestStreamReportsServiceErrorWithoutPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"invalid key: test-key\",\"type\":\"overloaded_error\"}}\n\n")
	}))
	defer server.Close()
	var output bytes.Buffer
	err := Stream(context.Background(), testConfig(server), "你好", &output)
	if err == nil || !strings.Contains(err.Error(), "模型服务在流中返回错误") {
		t.Fatalf("应区分服务端流内错误，实际为 %v", err)
	}
	if strings.Contains(err.Error(), "test-key") || output.Len() != 0 {
		t.Fatal("不应回显服务端错误载荷或其中的凭据")
	}
}

func TestStreamCanBeCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sendText(w, "开始回复")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output := &observingWriter{onWrite: cancel}

	if err := Stream(ctx, testConfig(server), "你好", output); !errors.Is(err, context.Canceled) {
		t.Fatalf("应报告取消，实际为 %v", err)
	}
}
