package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		BaseURL: server.URL + "/gateway/v1",
		Model:   "relay-deepseek",
	}
}

func TestStreamOutputsBeforeResponseCompletes(t *testing.T) {
	firstWritten := make(chan struct{})
	output := &observingWriter{onWrite: func() { close(firstWritten) }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/gateway/v1/chat/completions" {
			t.Errorf("请求方法或地址错误：%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("缺少正确的鉴权头")
		}
		var body struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "relay-deepseek" || !body.Stream || len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content != "你好" {
			t.Error("请求未正确传递模型名称、流式选项或用户输入")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"你好\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-firstWritten:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"，世界\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
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
	for _, reason := range []string{"", "length"} {
		t.Run("finish_reason="+reason, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"部分回复\"},\"finish_reason\":%q}]}\n\n", reason)
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
		fmt.Fprint(w, `{"error":{"message":"invalid key: test-key","type":"invalid_api_key"}}`)
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
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"invalid key: test-key\",\"type\":\"insufficient_quota\"}}\n\n")
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
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"开始回复\"},\"finish_reason\":null}]}\n\n")
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
