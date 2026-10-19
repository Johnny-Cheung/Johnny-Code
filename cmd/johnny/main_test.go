package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func useConfigPath(t *testing.T, path string) {
	t.Helper()
	previous := configFileBase64
	configFileBase64 = base64.StdEncoding.EncodeToString([]byte(path))
	t.Cleanup(func() { configFileBase64 = previous })
	for _, name := range []string{"JOHNNY_API_KEY", "JOHNNY_BASE_URL", "JOHNNY_MODEL", "JOHNNY_API"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHelpAndVersionWithoutConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.env")
	useConfigPath(t, path)
	for _, args := range [][]string{{"--help"}, {"-h"}, {"--version"}} {
		var output, stderr bytes.Buffer
		if code := run(context.Background(), args, strings.NewReader(""), &output, &stderr); code != 0 {
			t.Fatalf("%v 返回 %d：%s", args, code, stderr.String())
		}
		if args[0] == "--version" {
			if !strings.Contains(output.String(), "v"+version) || !strings.Contains(output.String(), commit) {
				t.Fatal("版本输出缺少构建信息")
			}
		} else if !strings.Contains(output.String(), path) || !strings.Contains(output.String(), "johnny") {
			t.Fatal("帮助输出缺少用法或现有配置位置")
		}
		if stderr.Len() != 0 {
			t.Fatalf("本地查询不应尝试加载配置：%s", stderr.String())
		}
	}
	var output, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--unknown"}, strings.NewReader(""), &output, &stderr); code == 0 {
		t.Fatal("未知选项应返回错误")
	}
}

func TestRunSingleQuestionFromAnotherDirectory(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		input    string
		requests int32
		code     int
	}{
		{"参数输入", []string{"你好"}, "", 1, 0},
		{"无参数只读取一个问题", nil, "你好\n不应发送的第二个问题\n", 1, 0},
		{"没有换行的输入", nil, "你好", 1, 0},
		{"输入结束", nil, "", 0, 0},
		{"空白问题", nil, " \n", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var body struct {
					Messages []struct {
						Content []struct{ Text string } `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(body.Messages) != 1 || len(body.Messages[0].Content) != 1 || body.Messages[0].Content[0].Text != "你好" {
					t.Error("应只发送本次的第一个问题")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"答复\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "existing config.env")
			data := "JOHNNY_API_KEY=test-key\nJOHNNY_BASE_URL=" + server.URL + "\nJOHNNY_MODEL=test-model\n"
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			useConfigPath(t, path)
			t.Chdir(t.TempDir())
			before, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			var output, stderr bytes.Buffer
			code := run(context.Background(), tc.args, strings.NewReader(tc.input), &output, &stderr)
			if code != tc.code || requests.Load() != tc.requests {
				t.Fatalf("code=%d requests=%d stderr=%s", code, requests.Load(), stderr.String())
			}
			if tc.requests == 1 && output.String() != "答复\n" {
				t.Fatalf("正文输出错误：%q", output.String())
			}
			if tc.requests == 0 && output.Len() != 0 {
				t.Fatal("没有请求时不应输出模型正文")
			}
			if after, err := os.Getwd(); err != nil || after != before {
				t.Fatal("问答过程不应切换工作目录")
			}
		})
	}
}

type blockingReader struct {
	started chan struct{}
	release chan struct{}
}

func (r blockingReader) Read([]byte) (int, error) {
	close(r.started)
	<-r.release
	return 0, io.EOF
}

func TestWaitingForInputCanBeCancelled(t *testing.T) {
	reader := blockingReader{started: make(chan struct{}), release: make(chan struct{})}
	defer close(reader.release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := readPrompt(ctx, reader)
		finished <- err
	}()
	<-reader.started
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("应报告取消，实际为 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待输入时取消未能及时结束")
	}
}
