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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
)

func useConfigPath(t *testing.T, path string) {
	t.Helper()
	previous := configFileBase64
	configFileBase64 = base64.StdEncoding.EncodeToString([]byte(path))
	t.Cleanup(func() { configFileBase64 = previous })
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"JOHNNY_API_KEY", "JOHNNY_BASE_URL", "JOHNNY_MODEL", "JOHNNY_API"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

func assertNoSessionLog(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".johnny")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("不应创建会话日志目录：%v", err)
	}
}

func writeTextResponse(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", text)
	fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
}

func writeToolUseResponse(w http.ResponseWriter, text, callID, name, arguments string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", text)
	fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":%q,\"name\":%q,\"input\":{}}}\n\n", callID, name)
	fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":%q}}\n\n", arguments)
	fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
	fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
}

func TestHelpAndVersionWithoutConfigOrLogs(t *testing.T) {
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
		} else if !strings.Contains(output.String(), path) || !strings.Contains(output.String(), "johnny") || !strings.Contains(output.String(), "--max-turns") {
			t.Fatal("帮助输出缺少用法、次数上限或现有配置位置")
		}
		if stderr.Len() != 0 {
			t.Fatalf("本地查询不应尝试加载配置：%s", stderr.String())
		}
		assertNoSessionLog(t)
	}
}

func TestInvalidFlagsDoNotRequestOrCreateLogs(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeTextResponse(w, "不应出现")
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "existing config.env")
	data := "JOHNNY_API_KEY=test-key\nJOHNNY_BASE_URL=" + server.URL + "\nJOHNNY_MODEL=test-model\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	useConfigPath(t, path)
	t.Chdir(t.TempDir())
	for _, args := range [][]string{
		{"--unknown"},
		{"--max-turns", "0", "你好"},
		{"--max-turns", "-1", "你好"},
		{"--max-turns", "abc", "你好"},
	} {
		var output, stderr bytes.Buffer
		if code := run(context.Background(), args, strings.NewReader(""), &output, &stderr); code == 0 {
			t.Fatalf("%v 应返回错误", args)
		}
		if stderr.Len() == 0 {
			t.Fatalf("%v 应说明参数错误", args)
		}
		if requests.Load() != 0 {
			t.Fatalf("%v 不应发出模型请求", args)
		}
	}
	assertNoSessionLog(t)
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
				writeTextResponse(w, "答复")
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

func TestCLIToolLoopAndSessionRecord(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		switch count {
		case 1:
			writeToolUseResponse(w, "我先创建文件。", "toolu_1", "write", `{"path":"created.txt","content":"hello"}`)
		case 2:
			if !strings.Contains(string(body), "created.txt") || !strings.Contains(string(body), "tool_result") {
				t.Errorf("第二次请求缺少工具结果：%s", body)
			}
			writeTextResponse(w, "完成")
		default:
			t.Errorf("不应发出第 %d 次请求", count)
			writeTextResponse(w, "多余")
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "existing config.env")
	data := "JOHNNY_API_KEY=test-key\nJOHNNY_BASE_URL=" + server.URL + "\nJOHNNY_MODEL=test-model\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	useConfigPath(t, path)
	home := os.Getenv("HOME")
	work := t.TempDir()
	t.Chdir(work)

	var output, stderr bytes.Buffer
	code := run(context.Background(), []string{"创建文件"}, strings.NewReader(""), &output, &stderr)
	if code != 0 || requests.Load() != 2 {
		t.Fatalf("code=%d requests=%d stderr=%s", code, requests.Load(), stderr.String())
	}
	if output.String() != "我先创建文件。\n完成\n" {
		t.Fatalf("正文输出错误：%q", output.String())
	}
	for _, want := range []string{"[write] created.txt", "[成功]", "会话记录："} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr 缺少 %q：%s", want, stderr.String())
		}
	}
	if content, err := os.ReadFile(filepath.Join(work, "created.txt")); err != nil || string(content) != "hello" {
		t.Fatalf("工具未在启动目录创建文件：%q %v", content, err)
	}
	logs, err := filepath.Glob(filepath.Join(home, ".johnny", "sessions", "*.jsonl"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("应在 HOME 会话目录生成一条记录：%v %v", logs, err)
	}
	record, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"created.txt", "tool_use", "tool_call_id"} {
		if !strings.Contains(string(record), want) {
			t.Fatalf("会话记录缺少 %q：%s", want, record)
		}
	}
	if _, err := os.Stat(filepath.Join(work, ".johnny")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("工作目录不应创建日志目录")
	}
}

type failingRecorder struct {
	recordErr  error
	closeErr   error
	records    int
	closeCalls int
}

func (f *failingRecorder) Path() string { return "/tmp/fake-session.jsonl" }

func (f *failingRecorder) Record(agent.Event) error {
	f.records++
	return f.recordErr
}

func (f *failingRecorder) Close() error {
	f.closeCalls++
	return f.closeErr
}

func TestSystemPromptIncludesWorkDirAndToolGuidance(t *testing.T) {
	prompt := systemPrompt("/tmp/johnny work")
	for _, want := range []string{"/tmp/johnny work", "当前工作目录", "使用可用工具实际", "工具成功返回"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("系统提示缺少 %q：%s", want, prompt)
		}
	}
}

func TestSessionRecordAndCloseFailuresWarnOnce(t *testing.T) {
	t.Run("记录失败后停止写入且只警告一次", func(t *testing.T) {
		recorder := &failingRecorder{recordErr: errors.New("磁盘已满"), closeErr: errors.New("关闭失败")}
		var stderr bytes.Buffer
		logger := newSessionLogger(recorder, &stderr)
		for i := 0; i < 3; i++ {
			logger.record(agent.Event{Type: agent.EventStart})
		}
		logger.close()
		if recorder.records != 1 || recorder.closeCalls != 1 {
			t.Fatalf("写失败后应停止记录并关闭已写文件：records=%d closes=%d", recorder.records, recorder.closeCalls)
		}
		if logger.recorder != nil {
			t.Fatal("写失败后应停用 recorder")
		}
		if strings.Count(stderr.String(), "警告：") != 1 || !strings.Contains(stderr.String(), "写入失败") || !strings.Contains(stderr.String(), "已降级继续任务") {
			t.Fatalf("记录失败应只警告一次并继续：%s", stderr.String())
		}
		if strings.Contains(stderr.String(), "关闭失败") {
			t.Fatalf("已警告后不应重复警告关闭失败：%s", stderr.String())
		}
	})
	t.Run("关闭失败单独警告", func(t *testing.T) {
		recorder := &failingRecorder{closeErr: errors.New("关闭失败")}
		var stderr bytes.Buffer
		logger := newSessionLogger(recorder, &stderr)
		logger.record(agent.Event{Type: agent.EventStart})
		logger.close()
		if strings.Count(stderr.String(), "警告：") != 1 || !strings.Contains(stderr.String(), "关闭失败") {
			t.Fatalf("关闭失败应警告一次：%s", stderr.String())
		}
	})
}

func TestSessionCreationFailureDegrades(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeTextResponse(w, "答复")
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "existing config.env")
	data := "JOHNNY_API_KEY=test-key\nJOHNNY_BASE_URL=" + server.URL + "\nJOHNNY_MODEL=test-model\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	useConfigPath(t, path)
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".johnny"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())

	var output, stderr bytes.Buffer
	code := run(context.Background(), []string{"你好"}, strings.NewReader(""), &output, &stderr)
	if code != 0 || output.String() != "答复\n" {
		t.Fatalf("日志失败不应影响任务：code=%d output=%q stderr=%s", code, output.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "警告：会话记录创建失败") || !strings.Contains(stderr.String(), "已降级继续任务") {
		t.Fatalf("缺少降级警告：%s", stderr.String())
	}
	if strings.Count(stderr.String(), "警告：") != 1 {
		t.Fatalf("日志失败只应警告一次：%s", stderr.String())
	}
}

func TestCancellationReturns130(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	// 服务端处理器不读取请求体，客户端取消后不一定能观察到连接关闭，
	// 测试结束时主动放行，避免 httptest.Server.Close 等待。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	defer server.Close()
	defer unblock()
	path := filepath.Join(t.TempDir(), "existing config.env")
	data := "JOHNNY_API_KEY=test-key\nJOHNNY_BASE_URL=" + server.URL + "\nJOHNNY_MODEL=test-model\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	useConfigPath(t, path)
	t.Chdir(t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan int, 1)
	var output, stderr bytes.Buffer
	go func() {
		finished <- run(ctx, []string{"你好"}, strings.NewReader(""), &output, &stderr)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("模型请求未启动")
	}
	cancel()
	select {
	case code := <-finished:
		if code != 130 {
			t.Fatalf("取消应返回 130，实际 %d：%s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "取消") {
			t.Fatalf("取消应说明原因：%s", stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消后任务未及时结束")
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
