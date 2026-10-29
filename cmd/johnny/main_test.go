package main

import (
	"bufio"
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
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/session"
	"github.com/Johnny-Cheung/Johnny-Code/internal/skill"
)

func useConfigPath(t *testing.T, path string) {
	t.Helper()
	previous := configFileBase64
	configFileBase64 = base64.StdEncoding.EncodeToString([]byte(path))
	t.Cleanup(func() { configFileBase64 = previous })
	t.Setenv("HOME", t.TempDir())
	t.Setenv("JOHNNY_MEMORY_AUTO", "off")
	for _, name := range []string{"JOHNNY_API_KEY", "JOHNNY_BASE_URL", "JOHNNY_MODEL", "JOHNNY_API"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

func runLegacy(ctx context.Context, args []string, input io.Reader, output, stderr io.Writer) int {
	return runWithEntryPolicy(ctx, args, input, output, stderr, false)
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

// newCLITestServer 创建使用临时 HOME 和工作目录的 CLI 测试服务。
func newCLITestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "existing config.env")
	data := "JOHNNY_API_KEY=test-key\nJOHNNY_BASE_URL=" + server.URL + "\nJOHNNY_MODEL=test-model\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	useConfigPath(t, path)
	t.Chdir(t.TempDir())
	return server
}

type trackingReader struct {
	reader io.Reader
	reads  atomic.Int32
}

func (r *trackingReader) Read(p []byte) (int, error) {
	r.reads.Add(1)
	return r.reader.Read(p)
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func readSessionObjects(t *testing.T, home string) []map[string]json.RawMessage {
	t.Helper()
	logs, err := filepath.Glob(filepath.Join(home, ".johnny", "sessions", "*.jsonl"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("应生成且只生成一个会话日志：%v %v", logs, err)
	}
	data, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	objects := make([]map[string]json.RawMessage, len(lines))
	for i, line := range lines {
		if err := json.Unmarshal([]byte(line), &objects[i]); err != nil {
			t.Fatalf("解析会话记录第 %d 行失败：%v", i+1, err)
		}
	}
	return objects
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
		} else if !strings.Contains(output.String(), path) || !strings.Contains(output.String(), "johnny") || !strings.Contains(output.String(), "--max-turns") || !strings.Contains(output.String(), "--read") || !strings.Contains(output.String(), "--skip") || !strings.Contains(output.String(), "--permission-mode") || !strings.Contains(output.String(), "/exit") || !strings.Contains(output.String(), "EOF") || !strings.Contains(output.String(), "只支持在终端") || !strings.Contains(output.String(), "管道和重定向会被拒绝") {
			t.Fatal("帮助输出缺少 TUI 入口、退出方式或现有配置位置")
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
		{"--permission-mode", "bad", "你好"},
		{"--permission-mode", "", "你好"},
		{"--read", "--skip", "你好"},
		{"--read", "--permission-mode", "allow", "你好"},
	} {
		var output, stderr bytes.Buffer
		if code := runLegacy(context.Background(), args, strings.NewReader(""), &output, &stderr); code == 0 {
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

func TestArgumentModeExecutesOnceWithoutReadingStdin(t *testing.T) {
	var requests atomic.Int32
	newCLITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeTextResponse(w, "答复")
	})
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	input := &trackingReader{reader: strings.NewReader("不应读取的下一行\n")}
	var output, stderr bytes.Buffer
	code := runLegacy(context.Background(), []string{"你好"}, input, &output, &stderr)
	if code != 0 || requests.Load() != 1 || input.reads.Load() != 0 {
		t.Fatalf("参数模式应只执行一次且不读 stdin：code=%d requests=%d reads=%d stderr=%s", code, requests.Load(), input.reads.Load(), stderr.String())
	}
	if output.String() != "答复\n" {
		t.Fatalf("正文输出错误：%q", output.String())
	}
	if !strings.Contains(stderr.String(), "权限模式：默认") {
		t.Fatalf("不带权限参数应使用默认模式：%s", stderr.String())
	}
	if after, err := os.Getwd(); err != nil || after != before {
		t.Fatal("问答过程不应切换工作目录")
	}
}

func TestInteractiveMultiTurnKeepsContextAndSingleSessionLog(t *testing.T) {
	var requests atomic.Int32
	newCLITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		count := requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		switch count {
		case 1:
			writeToolUseResponse(w, "creating", "toolu_1", "write", `{"path":"created.txt","content":"hello"}`)
		case 2:
			if !strings.Contains(string(body), "created.txt") || !strings.Contains(string(body), "tool_result") {
				t.Errorf("第一轮后续请求缺少工具历史：%s", body)
			}
			writeTextResponse(w, "first answer")
		case 3:
			for _, want := range []string{"first prompt", "second prompt", "first answer", "tool_result"} {
				if !strings.Contains(string(body), want) {
					t.Errorf("第二轮请求缺少历史 %q：%s", want, body)
				}
			}
			writeTextResponse(w, "second answer")
		default:
			t.Errorf("不应发出第 %d 次请求", count)
		}
	})
	home := os.Getenv("HOME")

	var output, stderr bytes.Buffer
	code := runLegacy(context.Background(), nil, strings.NewReader("first prompt\nsecond prompt\n/exit\n"), &output, &stderr)
	if code != 0 || requests.Load() != 3 {
		t.Fatalf("code=%d requests=%d stderr=%s", code, requests.Load(), stderr.String())
	}
	if output.String() != "creating\nfirst answer\nsecond answer\n" {
		t.Fatalf("正文输出错误：%q", output.String())
	}
	if content, err := os.ReadFile("created.txt"); err != nil || string(content) != "hello" {
		t.Fatalf("工具未在工作目录执行：%q %v", content, err)
	}

	objects := readSessionObjects(t, home)
	metaCount := 0
	requestTurns := make(map[int][]int)
	for _, object := range objects {
		var record struct {
			Type     string `json:"type"`
			UserTurn int    `json:"user_turn"`
			Turn     int    `json:"turn"`
		}
		raw, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		switch record.Type {
		case "meta":
			metaCount++
			if _, ok := object["user_turn"]; ok {
				t.Fatal("metadata 不应包含 user_turn")
			}
		case "request_start":
			if record.UserTurn < 1 {
				t.Fatalf("事件缺少 user_turn：%v", record)
			}
			requestTurns[record.UserTurn] = append(requestTurns[record.UserTurn], record.Turn)
		}
	}
	if metaCount != 1 {
		t.Fatalf("metadata 应只记录一次，实际 %d 次", metaCount)
	}
	if len(requestTurns[1]) != 2 || requestTurns[1][0] != 1 || requestTurns[1][1] != 2 {
		t.Fatalf("第一个用户轮次的模型请求编号错误：%v", requestTurns)
	}
	if len(requestTurns[2]) != 1 || requestTurns[2][0] != 1 {
		t.Fatalf("第二个用户轮次应重新从 1 计数：%v", requestTurns)
	}
}

func TestInteractiveExitBlankAndEOFDoNotRequestOrLog(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
	}{
		{"exit", "/exit\n"},
		{"padded exit", "  /exit  \n"},
		{"immediate EOF", ""},
		{"blank lines then EOF", "\n \n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			newCLITestServer(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				writeTextResponse(w, "不应出现")
			})
			var output, stderr bytes.Buffer
			code := runLegacy(context.Background(), nil, strings.NewReader(tc.input), &output, &stderr)
			if code != 0 || requests.Load() != 0 || output.Len() != 0 {
				t.Fatalf("应正常退出且不请求模型：code=%d requests=%d output=%q stderr=%s", code, requests.Load(), output.String(), stderr.String())
			}
			assertNoSessionLog(t)
		})
	}
}

func TestInteractiveProcessesFinalLineWithoutNewline(t *testing.T) {
	var requests atomic.Int32
	newCLITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeTextResponse(w, "answer")
	})
	var output, stderr bytes.Buffer
	code := runLegacy(context.Background(), nil, strings.NewReader("first\nsecond"), &output, &stderr)
	if code != 0 || requests.Load() != 2 || output.String() != "answer\nanswer\n" {
		t.Fatalf("EOF 末行未处理：code=%d requests=%d output=%q stderr=%s", code, requests.Load(), output.String(), stderr.String())
	}
}

func TestInteractiveResetsMaxTurnsForEachUserInput(t *testing.T) {
	var requests atomic.Int32
	newCLITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch requests.Add(1) {
		case 1:
			writeToolUseResponse(w, "", "toolu_1", "write", `{"path":"turn.txt","content":"x"}`)
		case 2:
			writeTextResponse(w, "first answer")
		case 3:
			writeTextResponse(w, "second answer")
		default:
			t.Error("不应发出多余请求")
			writeTextResponse(w, "多余")
		}
	})
	var output, stderr bytes.Buffer
	code := runLegacy(context.Background(), []string{"--max-turns", "2"}, strings.NewReader("first\nsecond\n/exit\n"), &output, &stderr)
	if code != 0 || requests.Load() != 3 || output.String() != "first answer\nsecond answer\n" {
		t.Fatalf("每轮请求预算未重置：code=%d requests=%d output=%q stderr=%s", code, requests.Load(), output.String(), stderr.String())
	}
}

func TestInteractiveContinuesAfterFailedTurn(t *testing.T) {
	var requests atomic.Int32
	newCLITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		writeTextResponse(w, "recovered")
	})
	var output, stderr bytes.Buffer
	code := runLegacy(context.Background(), nil, strings.NewReader("first\nsecond\n/exit\n"), &output, &stderr)
	if code != 0 || requests.Load() != 2 || output.String() != "recovered\n" {
		t.Fatalf("失败后应继续下一轮：code=%d requests=%d output=%q stderr=%s", code, requests.Load(), output.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "任务失败") {
		t.Fatalf("应报告本轮失败：%s", stderr.String())
	}
}

func TestInteractiveStopsWhenOutputFails(t *testing.T) {
	var requests atomic.Int32
	newCLITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeTextResponse(w, "answer")
	})
	var stderr bytes.Buffer
	code := runLegacy(context.Background(), nil, strings.NewReader("first\nsecond\n/exit\n"), failingWriter{err: errors.New("输出坏了")}, &stderr)
	if code != 1 || requests.Load() != 1 {
		t.Fatalf("输出失败应停止会话：code=%d requests=%d stderr=%s", code, requests.Load(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "输出坏了") {
		t.Fatalf("应报告输出错误：%s", stderr.String())
	}
}

func TestInteractivePromptShortWriteStopsBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	newCLITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeTextResponse(w, "不应请求")
	})
	var output bytes.Buffer
	code := runLegacy(context.Background(), nil, strings.NewReader("first\n/exit\n"), &output, shortWriter{})
	if code != 1 || requests.Load() != 0 || output.Len() != 0 {
		t.Fatalf("输入提示短写后不应请求模型：code=%d requests=%d", code, requests.Load())
	}
	assertNoSessionLog(t)
}

func TestInteractiveReadFailureStops(t *testing.T) {
	var requests atomic.Int32
	newCLITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeTextResponse(w, "不应出现")
	})
	var output, stderr bytes.Buffer
	code := runLegacy(context.Background(), nil, failingReader{err: errors.New("读取坏了")}, &output, &stderr)
	if code != 1 || requests.Load() != 0 || output.Len() != 0 {
		t.Fatalf("读取失败应退出：code=%d requests=%d output=%q", code, requests.Load(), output.String())
	}
	if !strings.Contains(stderr.String(), "读取输入失败") {
		t.Fatalf("应报告读取错误：%s", stderr.String())
	}
	assertNoSessionLog(t)
}

func TestReadPromptReusesBufferedLines(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("first\nsecond\nthird"))
	for _, want := range []string{"first", "second", "third"} {
		got, err := readPrompt(context.Background(), reader)
		if err != nil || got != want {
			t.Fatalf("readPrompt() = %q, %v; want %q", got, err, want)
		}
	}
	if _, err := readPrompt(context.Background(), reader); !errors.Is(err, io.EOF) {
		t.Fatalf("输入结束后应返回 EOF，实际 %v", err)
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
	code := runLegacy(context.Background(), []string{"创建文件"}, strings.NewReader(""), &output, &stderr)
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
	for _, want := range []string{"created.txt", "tool_use", "tool_call_id", `"user_turn":1`} {
		if !strings.Contains(string(record), want) {
			t.Fatalf("会话记录缺少 %q：%s", want, record)
		}
	}
	if _, err := os.Stat(filepath.Join(work, ".johnny")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("工作目录不应创建日志目录")
	}
}

func TestCLISkillDiscoveryAndToolLoad(t *testing.T) {
	var requests atomic.Int32
	newCLITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		count := requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		switch count {
		case 1:
			if !strings.Contains(string(body), "## 可用 Skills") || !strings.Contains(string(body), "- go-test [项目]：运行 Go 测试时使用。") {
				t.Errorf("第一次请求缺少 Skill 元数据：%s", body)
			}
			if strings.Contains(string(body), "执行 修复 TestFoo 并检查结果。") {
				t.Errorf("系统提示不应包含 Skill 正文：%s", body)
			}
			writeToolUseResponse(w, "加载 Skill。", "toolu_skill_1", "skill", `{"name":"go-test","arguments":"修复 TestFoo"}`)
		case 2:
			if !strings.Contains(string(body), "执行 修复 TestFoo 并检查结果。") {
				t.Errorf("第二次请求缺少 Skill 工具结果：%s", body)
			}
			writeTextResponse(w, "完成")
		default:
			t.Errorf("不应发出第 %d 次请求", count)
			writeTextResponse(w, "多余")
		}
	})
	work := t.TempDir()
	writeCommandSkill(t, filepath.Join(work, ".johnny", "skills"), "go-test", "运行 Go 测试时使用。", "执行 $ARGUMENTS 并检查结果。")
	t.Chdir(work)

	var output, stderr bytes.Buffer
	code := runLegacy(context.Background(), []string{"--permission-mode", "default", "使用技能"}, strings.NewReader(""), &output, &stderr)
	if code != 0 || requests.Load() != 2 {
		t.Fatalf("code=%d requests=%d stderr=%s", code, requests.Load(), stderr.String())
	}
	if !strings.Contains(output.String(), "完成") {
		t.Fatalf("最终回答缺失：%q", output.String())
	}
	if !strings.Contains(stderr.String(), "Skill：已加载 1 个") || !strings.Contains(stderr.String(), "[skill] go-test") {
		t.Fatalf("启动提示或工具显示缺失：%s", stderr.String())
	}
	logs, err := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".johnny", "sessions", "*.jsonl"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("应生成一条会话记录：%v %v", logs, err)
	}
	record, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"skill"`, "执行 修复 TestFoo 并检查结果。"} {
		if !strings.Contains(string(record), want) {
			t.Fatalf("会话记录缺少 %q：%s", want, record)
		}
	}
}

func TestCLIReadOnlyModeRejectsWritesAndAllowsQueries(t *testing.T) {
	var requests atomic.Int32
	server := newCLITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		count := requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		switch count {
		case 1:
			writeToolUseResponse(w, "尝试写入。", "toolu_ro_1", "write", `{"path":"created.txt","content":"hello"}`)
		case 2:
			if !strings.Contains(string(body), "只读模式不允许使用 write") {
				t.Errorf("第二次请求缺少权限拒绝结果：%s", body)
			}
			writeToolUseResponse(w, "读取环境文件。", "toolu_ro_2", "read", `{"path":".env"}`)
		case 3:
			if !strings.Contains(string(body), "SECRET=1") {
				t.Errorf("第三次请求缺少 allow 的 .env 内容：%s", body)
			}
			writeToolUseResponse(w, "查询目录。", "toolu_ro_3", "bash", `{"command":"ls -a"}`)
		case 4:
			if !strings.Contains(string(body), `.env`) {
				t.Errorf("只读查询结果缺少 .env：%s", body)
			}
			writeTextResponse(w, "只读完成")
		default:
			t.Errorf("不应发出第 %d 次请求", count)
			writeTextResponse(w, "多余")
		}
	})
	work, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".env"), []byte("SECRET=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var output, stderr bytes.Buffer
	code := runLegacy(context.Background(), []string{"--read", "检查项目"}, strings.NewReader(""), &output, &stderr)
	if code != 0 || requests.Load() != 4 {
		t.Fatalf("code=%d requests=%d stderr=%s", code, requests.Load(), stderr.String())
	}
	if !strings.Contains(output.String(), "只读完成") {
		t.Fatalf("最终回答缺失：%q", output.String())
	}
	if !strings.Contains(stderr.String(), "权限模式：只读") || !strings.Contains(stderr.String(), "[失败]") {
		t.Fatalf("普通模式应显示只读提示和拒绝状态：%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(work, "created.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("只读模式不应创建文件：%v", err)
	}
	logs, err := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".johnny", "sessions", "*.jsonl"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("应生成一条会话记录：%v %v", logs, err)
	}
	record, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(record), "只读模式不允许使用 write") || !strings.Contains(string(record), "SECRET=1") {
		t.Fatalf("会话记录缺少拒绝或读取结果：%s", record)
	}
	if !strings.Contains(string(record), `"permission_mode":"read-only"`) {
		t.Fatalf("会话元数据缺少权限模式：%s", record)
	}
	_ = server
}

func TestCLIReadOnlyModeNegative(t *testing.T) {
	// 旧测试环境的 HOME 可能复用；这里只验证非法模式在读取配置前失败。
	var output, stderr bytes.Buffer
	code := runLegacy(context.Background(), []string{"--permission-mode", "unknown", "任务"}, strings.NewReader(""), &output, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "permission-mode") {
		t.Fatalf("非法权限模式应失败：code=%d stderr=%s", code, stderr.String())
	}
}

func TestDefaultModeNonInteractiveDeniesApproval(t *testing.T) {
	var requests atomic.Int32
	var secondBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if requests.Add(1) == 1 {
			writeToolUseResponse(w, "", "deny-bash", "bash", `{"command":"touch denied.txt"}`)
			return
		}
		secondBody = string(body)
		writeTextResponse(w, "DONE")
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "existing config.env")
	data := "JOHNNY_API_KEY=test-key\nJOHNNY_BASE_URL=" + server.URL + "\nJOHNNY_MODEL=test-model\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	useConfigPath(t, path)
	t.Chdir(t.TempDir())

	var output, stderr bytes.Buffer
	code := runLegacy(context.Background(), []string{"--permission-mode", "default", "run"}, strings.NewReader(""), &output, &stderr)
	if code != 0 || !strings.Contains(output.String(), "DONE") {
		t.Fatalf("默认模式非交互任务失败：code=%d output=%q stderr=%s", code, output.String(), stderr.String())
	}
	if _, err := os.Stat("denied.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("非交互审批拒绝后命令仍执行：%v", err)
	}
	if !strings.Contains(secondBody, `"is_error":true`) || !strings.Contains(secondBody, "审批") {
		t.Fatalf("拒绝结果未回传模型：%s", secondBody)
	}
	objects := readSessionObjects(t, os.Getenv("HOME"))
	var permission map[string]json.RawMessage
	metadataMode := ""
	for _, object := range objects {
		var recordType string
		_ = json.Unmarshal(object["type"], &recordType)
		if recordType == "meta" {
			_ = json.Unmarshal(object["permission_mode"], &metadataMode)
		}
		if recordType == "permission" {
			permission = object
		}
	}
	if metadataMode != "default" {
		t.Fatalf("会话元数据权限模式错误：%q", metadataMode)
	}
	if permission == nil {
		t.Fatal("缺少权限审批记录")
	}
	var decision, source, target string
	_ = json.Unmarshal(permission["decision"], &decision)
	_ = json.Unmarshal(permission["source"], &source)
	_ = json.Unmarshal(permission["target"], &target)
	if decision != "deny" || source != "unavailable" || !strings.Contains(target, "touch denied.txt") {
		t.Fatalf("权限记录异常：decision=%q source=%q target=%q", decision, source, target)
	}
}

func TestDefaultModeAutoAllowsWhitelistQuery(t *testing.T) {
	var requests atomic.Int32
	var secondBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if requests.Add(1) == 1 {
			writeToolUseResponse(w, "", "read-bash", "bash", `{"command":"cat note.txt"}`)
			return
		}
		secondBody = string(body)
		writeTextResponse(w, "DONE")
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "existing config.env")
	data := "JOHNNY_API_KEY=test-key\nJOHNNY_BASE_URL=" + server.URL + "\nJOHNNY_MODEL=test-model\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	useConfigPath(t, path)
	t.Chdir(t.TempDir())
	if err := os.WriteFile("note.txt", []byte("hello-default"), 0600); err != nil {
		t.Fatal(err)
	}

	var output, stderr bytes.Buffer
	code := runLegacy(context.Background(), []string{"--permission-mode", "default", "run"}, strings.NewReader(""), &output, &stderr)
	if code != 0 || !strings.Contains(output.String(), "DONE") {
		t.Fatalf("默认模式自动放行任务失败：code=%d output=%q stderr=%s", code, output.String(), stderr.String())
	}
	if !strings.Contains(secondBody, "hello-default") || strings.Contains(secondBody, `"is_error":true`) {
		t.Fatalf("白名单查询结果未正常回传：%s", secondBody)
	}
	objects := readSessionObjects(t, os.Getenv("HOME"))
	for _, object := range objects {
		var recordType string
		_ = json.Unmarshal(object["type"], &recordType)
		if recordType == "permission" {
			t.Fatal("自动放行的查询不应产生审批记录")
		}
	}
}

func TestRunRequiresTerminalTUI(t *testing.T) {
	var output, stderr bytes.Buffer
	code := run(context.Background(), []string{"任务"}, strings.NewReader(""), &output, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "不接受参数任务") {
		t.Fatalf("参数任务应被拒绝：code=%d stderr=%s", code, stderr.String())
	}
	output.Reset()
	stderr.Reset()
	code = run(context.Background(), nil, strings.NewReader(""), &output, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "管道和重定向不支持") {
		t.Fatalf("非终端输入应被拒绝：code=%d stderr=%s", code, stderr.String())
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

func (f *failingRecorder) RecordPermission(session.PermissionRecord) error {
	f.records++
	return f.recordErr
}

func (f *failingRecorder) Close() error {
	f.closeCalls++
	return f.closeErr
}

func TestSystemPromptIncludesWorkDirAndToolGuidance(t *testing.T) {
	prompt := systemPrompt("/tmp/johnny work", permissionAllow, nil)
	for _, want := range []string{"/tmp/johnny work", "当前工作目录", "使用可用工具实际", "工具成功返回", "围栏代码块"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("系统提示缺少 %q：%s", want, prompt)
		}
	}
	readOnly := systemPrompt("/tmp/johnny work", permissionReadOnly, nil)
	for _, want := range []string{"/tmp/johnny work", "只读", "pwd", "ls", "find", "grep", "多段管道", "重新启动 johnny", "--skip", "不要尝试 write、edit"} {
		if !strings.Contains(readOnly, want) {
			t.Fatalf("只读系统提示缺少 %q：%s", want, readOnly)
		}
	}
	if strings.Contains(readOnly, "修改代码或执行命令") {
		t.Fatalf("只读系统提示仍在鼓励修改：%s", readOnly)
	}
	defaultPrompt := systemPrompt("/tmp/johnny work", permissionDefault, nil)
	for _, want := range []string{"/tmp/johnny work", "权限模式：默认", "路径白名单", "审批", "不要尝试绕过权限层"} {
		if !strings.Contains(defaultPrompt, want) {
			t.Fatalf("默认模式系统提示缺少 %q：%s", want, defaultPrompt)
		}
	}
}

func TestSystemPromptIncludesSkillMetadataOnly(t *testing.T) {
	work, home := t.TempDir(), t.TempDir()
	dir := filepath.Join(work, ".johnny", "skills", "go-test")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: go-test\ndescription: 运行 Go 测试时使用。\n---\n系统提示不应包含这段正文。"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	store := skill.DiscoverWithHome(work, home)
	prompt := systemPrompt(work, permissionDefault, store)
	for _, want := range []string{"## 可用 Skills", "- go-test [项目]：运行 Go 测试时使用。", "不能授予权限"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("系统提示缺少 %q：%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "系统提示不应包含这段正文") {
		t.Fatalf("系统提示不应包含 Skill 正文：%s", prompt)
	}
	empty := systemPrompt(work, permissionDefault, skill.DiscoverWithHome(t.TempDir(), t.TempDir()))
	if strings.Contains(empty, "可用 Skills") {
		t.Fatalf("没有 Skill 时不应注入区块：%s", empty)
	}
}

func TestToolKeyIncludesSkillName(t *testing.T) {
	key := toolKey(&model.ToolCall{Name: "skill", Arguments: []byte(`{"name":"go-test"}`)})
	if key != "go-test" {
		t.Fatalf("skill 工具显示名错误：%q", key)
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
	code := runLegacy(context.Background(), []string{"你好"}, strings.NewReader(""), &output, &stderr)
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
		finished <- runLegacy(ctx, []string{"你好"}, strings.NewReader(""), &output, &stderr)
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

func TestInteractiveCancellationReturns130(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	newCLITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	})
	defer unblock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan int, 1)
	var output, stderr bytes.Buffer
	go func() {
		finished <- runLegacy(ctx, nil, strings.NewReader("first\nsecond\n/exit\n"), &output, &stderr)
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
	case <-time.After(5 * time.Second):
		t.Fatal("取消后会话未及时结束")
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
