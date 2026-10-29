//go:build darwin || linux

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
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
)

// 在真实子进程中调用生产 run，沿用 main 的信号设置，并在会话撤销前检查恢复。
func TestTerminalHelperProcess(t *testing.T) {
	if os.Getenv("JOHNNY_TUI_TEST_HELPER") != "1" {
		return
	}
	configFileBase64 = base64.StdEncoding.EncodeToString([]byte(os.Getenv("JOHNNY_TUI_TEST_CONFIG")))
	if os.Getenv("JOHNNY_TUI_TEST_PIPE") == "1" {
		os.Exit(run(context.Background(), nil, strings.NewReader("hello\n/exit\n"), os.Stdout, os.Stderr))
	}
	before, err := term.GetState(os.Stdin.Fd())
	if err != nil {
		os.Exit(90)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() { <-ctx.Done(); stop() }()
	var args []string
	switch mode := os.Getenv("JOHNNY_TUI_TEST_MODE"); mode {
	case "", "default":
	case "read-only":
		args = []string{"--read"}
	case "allow":
		args = []string{"--skip"}
	default:
		args = []string{"--permission-mode", mode}
	}
	code := run(ctx, args, os.Stdin, os.Stdout, os.Stderr)
	stop()
	// macOS 在会话首进程退出后撤销 slave，必须在退出前检查恢复结果。
	after, err := term.GetState(os.Stdin.Fd())
	if err != nil || !reflect.DeepEqual(before, after) {
		os.Exit(91)
	}
	fmt.Fprintln(os.Stdout, "TERMINAL_RESTORED")
	os.Exit(code)
}

type terminalProcess struct {
	cmd           *exec.Cmd
	master, slave *os.File
	mu            sync.Mutex
	output        bytes.Buffer
	changed       chan struct{}
	done          chan struct{}
	err           error
	dir, home     string
}

func startTerminal(t *testing.T, serverURL string) *terminalProcess {
	return startTerminalMode(t, serverURL, "allow")
}

func startTerminalMode(t *testing.T, serverURL, mode string, extraEnv ...string) *terminalProcess {
	t.Helper()
	dir, home := t.TempDir(), t.TempDir()
	path := filepath.Join(dir, "test.env")
	if err := os.WriteFile(path, []byte("JOHNNY_API_KEY=test-key\nJOHNNY_BASE_URL="+serverURL+"\nJOHNNY_MODEL=terminal-test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := pty.Setsize(master, &pty.Winsize{Rows: 24, Cols: 90}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestTerminalHelperProcess$")
	cmd.Dir = dir
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "JOHNNY_") && name != "HOME" && name != "TERM" && name != "NO_COLOR" {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "JOHNNY_TUI_TEST_HELPER=1", "JOHNNY_TUI_TEST_CONFIG="+path,
		"JOHNNY_TUI_TEST_MODE="+mode, "HOME="+home, "TERM=xterm-256color")
	hasAutoMemory := false
	for _, entry := range extraEnv {
		if strings.HasPrefix(entry, "JOHNNY_MEMORY_AUTO=") {
			hasAutoMemory = true
		}
	}
	if !hasAutoMemory {
		cmd.Env = append(cmd.Env, "JOHNNY_MEMORY_AUTO=off")
	}
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	p := &terminalProcess{cmd: cmd, master: master, slave: slave, changed: make(chan struct{}, 1), done: make(chan struct{}), dir: dir, home: home}
	if err := cmd.Start(); err != nil {
		master.Close()
		slave.Close()
		t.Fatal(err)
	}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	go func() {
		buffer := make([]byte, 32<<10)
		for {
			n, err := master.Read(buffer)
			if n > 0 {
				p.mu.Lock()
				p.output.Write(buffer[:n])
				p.mu.Unlock()
				select {
				case p.changed <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = cmd.Process.Kill()
			<-p.done
		}
		_ = master.Close()
		_ = slave.Close()
	})
	p.expect(t, 0, "Enter 发送")
	return p
}

func (p *terminalProcess) text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.ReplaceAll(ansi.Strip(p.output.String()), "\r", "")
}

func (p *terminalProcess) write(t *testing.T, text string) {
	t.Helper()
	if _, err := io.WriteString(p.master, text); err != nil {
		t.Fatal(err)
	}
}

func (p *terminalProcess) expect(t *testing.T, offset int, text string) int {
	t.Helper()
	// 全量测试会并行构建和启动多个子进程；给 PTY 渲染留出足够调度时间，
	// 同时仍能暴露真实的界面卡死。
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	for {
		got := p.text()
		if offset <= len(got) {
			if index := strings.Index(got[offset:], text); index >= 0 {
				return offset + index + len(text)
			}
		}
		select {
		case <-p.changed:
		case <-timer.C:
			t.Fatalf("终端缺少 %q：\n%s", text, got)
		case <-p.done:
			// 读线程可能稍后才收到终端最后一帧，先给它消费机会。
			select {
			case <-p.changed:
			case <-time.After(50 * time.Millisecond):
			}
			got = p.text()
			if offset <= len(got) && strings.Contains(got[offset:], text) {
				continue
			}
			t.Fatalf("终端提前退出 %v，缺少 %q：\n%s", p.err, text, got)
		}
	}
}

func (p *terminalProcess) wait(t *testing.T, code int) {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(20 * time.Second):
		t.Fatal("终端未退出")
	}
	if p.cmd.ProcessState.ExitCode() != code {
		t.Fatalf("退出码 %d，预期 %d：%v\n%s", p.cmd.ProcessState.ExitCode(), code, p.err, p.text())
	}
	p.expect(t, 0, "TERMINAL_RESTORED")
}

func terminalAnswer(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	delta, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": text}})
	fmt.Fprintf(w, "event: content_block_delta\ndata: %s\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", delta)
}

func terminalTools(w http.ResponseWriter, tools []map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	for index, call := range tools {
		data, _ := json.Marshal(map[string]any{"type": "content_block_start", "index": index, "content_block": call})
		fmt.Fprintf(w, "event: content_block_start\ndata: %s\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":%d}\n\n", data, index)
	}
	fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
}

func TestTerminalPTYDefaultModeApproval(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 1 {
			terminalTools(w, []map[string]any{{
				"type": "tool_use", "id": "approval-bash", "name": "bash",
				"input": map[string]any{"command": "touch approved.txt"},
			}})
			return
		}
		terminalAnswer(w, "APPROVED")
	}))
	defer server.Close()
	p := startTerminalMode(t, server.URL, "default")
	p.write(t, "run\r")
	end := p.expect(t, 0, "权限确认")
	p.expect(t, end, "touch approved.txt")
	p.expect(t, end, "拒绝本次")
	if strings.Contains(p.text(), "批准并记录") {
		t.Fatal("不可记录命令不应显示记录选项")
	}
	p.write(t, "k\r")
	end = p.expect(t, end, "APPROVED")
	if _, err := os.Stat(filepath.Join(p.dir, "approved.txt")); err != nil {
		t.Fatalf("批准后命令未执行：%v", err)
	}
	p.write(t, "/exit\r")
	p.wait(t, 0)
	logs, _ := filepath.Glob(filepath.Join(p.home, ".johnny", "sessions", "*.jsonl"))
	if len(logs) != 1 {
		t.Fatalf("应生成一份会话记录：%v", logs)
	}
	log, _ := os.ReadFile(logs[0])
	if !bytes.Contains(log, []byte(`"type":"permission"`)) || !bytes.Contains(log, []byte(`"decision":"allow"`)) || !bytes.Contains(log, []byte(`"source":"tui"`)) {
		t.Fatalf("缺少 TUI 审批记录：%s", log)
	}
}

func TestTerminalPTYSlashMenuCompletion(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		t.Error("斜杠菜单本地命令不应请求模型")
	}))
	defer server.Close()
	p := startTerminalMode(t, server.URL, "default")
	start := len(p.text())
	p.write(t, "/sk")
	end := p.expect(t, start, "/skills")
	p.expect(t, start, "/skills reload")
	p.expect(t, start, "Enter 补全")
	p.write(t, "\r")
	end = p.expect(t, end, "已补全 /skills")
	p.write(t, "\r")
	end = p.expect(t, end, "未发现 Skill")
	p.write(t, "/exit\r")
	p.wait(t, 0)
	if requests.Load() != 0 {
		t.Fatalf("菜单和本地命令不应请求模型：%d", requests.Load())
	}
}

func TestTerminalPTYSkillReloadAndInvocation(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		switch count.Add(1) {
		case 1:
			if !strings.Contains(string(body), "## 可用 Skills") || !strings.Contains(string(body), "go-test [项目]：PTY 测试技能。") {
				t.Errorf("重载后的系统提示缺少 Skill 元数据：%s", body)
			}
			if strings.Contains(string(body), "PTY_SKILL_BODY") {
				t.Errorf("系统提示不应包含 Skill 正文：%s", body)
			}
			terminalTools(w, []map[string]any{{
				"type": "tool_use", "id": "skill-auto", "name": "skill",
				"input": map[string]any{"name": "go-test", "arguments": "自动参数"},
			}})
		case 2:
			if !strings.Contains(string(body), "PTY_SKILL_BODY 执行 自动参数。") {
				t.Errorf("模型调用 skill 后缺少正文：%s", body)
			}
			terminalAnswer(w, "SKILL_OK")
		case 3:
			if !strings.Contains(string(body), "PTY_SKILL_BODY 执行 显式参数。") {
				t.Errorf("/skill 展开后缺少参数替换：%s", body)
			}
			terminalAnswer(w, "EXPLICIT_OK")
		default:
			t.Errorf("不应发出第 %d 次请求", count.Load())
			terminalAnswer(w, "多余")
		}
	}))
	defer server.Close()
	p := startTerminalMode(t, server.URL, "default")
	p.write(t, "/skills\r")
	end := p.expect(t, 0, "未发现 Skill")
	writeCommandSkill(t, filepath.Join(p.dir, ".johnny", "skills"), "go-test", "PTY 测试技能。", "PTY_SKILL_BODY 执行 $ARGUMENTS。")
	p.write(t, "/skills reload\r")
	end = p.expect(t, end, "Skill 已重载")
	p.expect(t, end, "go-test")
	end = p.expect(t, end, "[项目]")
	p.write(t, "/")
	end = p.expect(t, end, "go-test")
	p.expect(t, end, "Enter 补全")
	p.write(t, "\x1b")
	p.write(t, "\x03")
	runStart := len(p.text())
	p.write(t, "run\r")
	end = p.expect(t, end, "SKILL_OK")
	runText := p.text()[runStart:end]
	if !strings.Contains(runText, "skill") || !strings.Contains(runText, "go-test") {
		t.Fatalf("运行期间应显示 skill 工具和名称：%s", runText)
	}
	p.write(t, "/skill go-test 显式参数\r")
	end = p.expect(t, end, "EXPLICIT_OK")
	if strings.Contains(p.text(), "PTY_SKILL_BODY") {
		t.Fatalf("终端不应显示 Skill 正文：%s", p.text())
	}
	p.write(t, "/exit\r")
	p.wait(t, 0)
	logs, _ := filepath.Glob(filepath.Join(p.home, ".johnny", "sessions", "*.jsonl"))
	if len(logs) != 1 {
		t.Fatalf("应生成一份会话记录：%v", logs)
	}
	log, _ := os.ReadFile(logs[0])
	for _, want := range []string{`"skill"`, "PTY_SKILL_BODY 执行 自动参数。"} {
		if !bytes.Contains(log, []byte(want)) {
			t.Fatalf("会话记录缺少 %q：%s", want, log)
		}
	}
}

func TestTerminalPTYDefaultModeBashChainApproval(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 1 {
			terminalTools(w, []map[string]any{{
				"type": "tool_use", "id": "chain-bash", "name": "bash",
				"input": map[string]any{"command": "cd sub && make test"},
			}})
			return
		}
		terminalAnswer(w, "CHAIN_OK")
	}))
	defer server.Close()
	p := startTerminalMode(t, server.URL, "default")
	if err := os.Mkdir(filepath.Join(p.dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.dir, "sub", "Makefile"), []byte("test:\n\t@touch chain-marker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p.write(t, "run\r")
	end := p.expect(t, 0, "权限确认")
	p.expect(t, end, "cd sub && make test")
	p.expect(t, end, "命令前缀 make test")
	p.expect(t, end, "本会话内允许")
	p.expect(t, end, "批准并记录")
	// 默认焦点在拒绝；下移两次越过“本会话内允许”到达“批准并记录”。
	p.write(t, "jj\r")
	p.expect(t, end, "CHAIN_OK")
	if _, err := os.Stat(filepath.Join(p.dir, "sub", "chain-marker")); err != nil {
		t.Fatalf("命令链中的 Makefile 未执行：%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(p.dir, ".johnny", "permissions.local.json")); err != nil || !bytes.Contains(data, []byte(`"make"`)) || !bytes.Contains(data, []byte(`"test"`)) {
		t.Fatalf("命令链前缀未写入项目规则：%v %s", err, data)
	}
	p.write(t, "/exit\r")
	p.wait(t, 0)
	logs, _ := filepath.Glob(filepath.Join(p.home, ".johnny", "sessions", "*.jsonl"))
	if len(logs) != 1 {
		t.Fatalf("应生成一份会话记录：%v", logs)
	}
	log, _ := os.ReadFile(logs[0])
	if !bytes.Contains(log, []byte(`"recorded_prefixes":[["make","test"]]`)) {
		t.Fatalf("缺少复合命令前缀审计：%s", log)
	}
}

func TestTerminalPTYMultilineToolsCancelAndHistory(t *testing.T) {
	var count atomic.Int32
	requests := make(chan string, 8)
	slow := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- string(body)
		switch count.Add(1) {
		case 1:
			terminalTools(w, []map[string]any{
				{"type": "tool_use", "id": "write-1", "name": "write", "input": map[string]any{"path": "note.txt", "content": "secret-one"}},
				{"type": "tool_use", "id": "read-1", "name": "read", "input": map[string]any{"path": "note.txt"}},
				{"type": "tool_use", "id": "edit-1", "name": "edit", "input": map[string]any{"path": "note.txt", "old_string": "secret-one", "new_string": "secret-two"}},
				{"type": "tool_use", "id": "bash-1", "name": "bash", "input": map[string]any{"command": "cat note.txt"}},
			})
		case 2:
			var text strings.Builder
			for i := 0; i < 100; i++ {
				fmt.Fprintf(&text, "- history-line-%03d\n", i)
			}
			text.WriteString("\n```text\nbackend/\n├── main.go    10    # 入口\n└── docs/\n```\n")
			text.WriteString("\n```go\nfmt.Println(\"ok\")\n```\n\nFIRST_DONE")
			terminalAnswer(w, text.String())
		case 3:
			close(slow)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		default:
			terminalAnswer(w, "CONTINUED_OK")
		}
	}))
	defer server.Close()
	defer close(release)
	p := startTerminal(t, server.URL)
	start := len(p.text())
	p.write(t, "\x1b[200~请处理代码\nfunc main() {}\x1b[201~")
	p.expect(t, start, "func main() {}")
	if count.Load() != 0 {
		t.Fatal("粘贴自动发送了请求")
	}
	p.write(t, "\x1b\r")
	p.write(t, "再运行检查")
	p.expect(t, start, "再运行检查")
	if count.Load() != 0 {
		t.Fatal("Alt+Enter 自动发送了请求")
	}
	p.write(t, "\r")
	summaryEnd := p.expect(t, start, "已折叠 4 次工具调用")
	p.expect(t, summaryEnd, "Ctrl+O 展开")
	firstEnd := p.expect(t, summaryEnd, "FIRST_DONE")
	p.expect(t, firstEnd, "Enter 发送")
	if text := p.text(); !strings.Contains(text, "├── main.go") || !strings.Contains(text, "└── docs/") {
		t.Fatalf("目录树未按结构提交到终端：\n%s", text)
	}
	if text := p.text(); strings.Count(text, "FIRST_DONE") != 1 {
		var contexts []string
		for start := 0; ; {
			index := strings.Index(text[start:], "FIRST_DONE")
			if index < 0 {
				break
			}
			index += start
			lo, hi := max(0, index-300), min(len(text), index+100)
			contexts = append(contexts, text[lo:hi])
			start = index + len("FIRST_DONE")
		}
		t.Fatalf("最终回答不应重复输出，实际出现 %d 次：\n---\n%s", len(contexts), strings.Join(contexts, "\n---\n"))
	}
	if count := strings.Count(p.text(), "已折叠 4 次工具调用"); count != 1 {
		t.Fatalf("折叠摘要应只出现一次，实际出现 %d 次", count)
	}
	firstBody := <-requests
	if !strings.Contains(firstBody, `请处理代码\nfunc main() {}\n再运行检查`) {
		t.Fatalf("多行内容不完整：%s", firstBody)
	}
	secondBody := <-requests
	for _, want := range []string{"write-1", "read-1", "edit-1", "bash-1", "secret-two", "cat note.txt"} {
		if !strings.Contains(secondBody, want) {
			t.Fatalf("缺少工具结果 %q", want)
		}
	}
	data, err := os.ReadFile(filepath.Join(p.dir, "note.txt"))
	if err != nil || string(data) != "secret-two" {
		t.Fatalf("文件未修改：%s %v", data, err)
	}
	for _, want := range []string{"history-line-000", "history-line-099", "FIRST_DONE"} {
		if !strings.Contains(p.text(), want) {
			t.Fatalf("scrollback 缺少 %q", want)
		}
	}
	for _, want := range []string{"write", "read", "edit", "bash", "cat note.txt"} {
		if !strings.Contains(p.text(), want) {
			t.Fatalf("实时进展缺少工具 %q", want)
		}
	}
	for _, unwanted := range []string{"→ write", "✓ 成功", "secret-two"} {
		if strings.Contains(p.text(), unwanted) {
			t.Fatalf("折叠前不应在滚动历史显示工具结果 %q", unwanted)
		}
	}
	expandOffset := len(p.text())
	p.write(t, "\x0f")
	p.expect(t, expandOffset, "最近一次工具过程")
	p.expect(t, expandOffset, "secret-two")
	p.expect(t, expandOffset, "Enter 发送")
	p.write(t, "slow\r")
	select {
	case <-slow:
	case <-time.After(5 * time.Second):
		t.Fatal("慢请求未开始")
	}
	if err := pty.Setsize(p.master, &pty.Winsize{Rows: 12, Cols: 42}); err != nil {
		t.Fatal(err)
	}
	offset := len(p.text())
	p.write(t, "\x03")
	end := p.expect(t, offset, "已取消本轮")
	p.expect(t, end, "Enter 发送")
	resized := p.text()[offset:]
	if !strings.Contains(resized, strings.Repeat("─", 41)) || strings.Contains(resized, strings.Repeat("─", 42)) {
		t.Fatal("实际终端 resize 没有更新输入区域宽度")
	}
	p.write(t, "continue\r")
	end = p.expect(t, end, "CONTINUED_OK")
	p.expect(t, end, "Enter 发送")
	p.write(t, "/exit\r")
	p.wait(t, 0)
	files, _ := filepath.Glob(filepath.Join(p.home, ".johnny", "sessions", "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("每会话应一份记录：%v", files)
	}
	log, _ := os.ReadFile(files[0])
	for _, want := range []string{`"user_turn":1`, `"user_turn":2`, `"user_turn":3`, `"status":"canceled"`, "secret-two", "write-1", "cat note.txt"} {
		if !bytes.Contains(log, []byte(want)) {
			t.Fatalf("日志缺少 %s", want)
		}
	}
}

func TestTerminalPTYCancelBashOrTerminate(t *testing.T) {
	for _, terminate := range []bool{false, true} {
		t.Run(fmt.Sprintf("terminate=%v", terminate), func(t *testing.T) {
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if count.Add(1) == 1 {
					terminalTools(w, []map[string]any{{"type": "tool_use", "id": "slow-bash", "name": "bash", "input": map[string]any{"command": "printf started > started.txt; sleep 30; printf late > late.txt"}}})
					return
				}
				if !strings.Contains(string(body), `"is_error":true`) || !strings.Contains(string(body), "slow-bash") {
					t.Error("取消后的工具结果没有保留")
				}
				terminalAnswer(w, "AFTER_BASH_CANCEL")
			}))
			defer server.Close()
			p := startTerminal(t, server.URL)
			p.write(t, "run\r")
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
		waitStarted:
			for {
				if _, err := os.Stat(filepath.Join(p.dir, "started.txt")); err == nil {
					break
				}
				select {
				case <-ticker.C:
				case <-deadline.C:
					t.Fatal("Bash 未真正执行")
					break waitStarted
				}
			}
			if terminate {
				if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				p.wait(t, 130)
			} else {
				offset := len(p.text())
				p.write(t, "\x03")
				end := p.expect(t, offset, "已取消本轮")
				p.expect(t, end, "Enter 发送")
				p.write(t, "next\r")
				end = p.expect(t, end, "AFTER_BASH_CANCEL")
				p.expect(t, end, "Enter 发送")
				p.write(t, "\x04")
				p.wait(t, 0)
			}
			if _, err := os.Stat(filepath.Join(p.dir, "late.txt")); !os.IsNotExist(err) {
				t.Fatalf("取消后命令仍继续执行：%v", err)
			}
		})
	}
}

func TestTerminalPTYPlainReaderIsRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("非终端输入不应请求模型")
	}))
	defer server.Close()
	dir, home := t.TempDir(), t.TempDir()
	path := filepath.Join(dir, "test.env")
	if err := os.WriteFile(path, []byte("JOHNNY_API_KEY=test-key\nJOHNNY_BASE_URL="+server.URL+"\nJOHNNY_MODEL=plain-test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	cmd := exec.Command(binary, "-test.run=^TestTerminalHelperProcess$")
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "JOHNNY_") && name != "HOME" && name != "TERM" {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "JOHNNY_TUI_TEST_HELPER=1", "JOHNNY_TUI_TEST_CONFIG="+path, "JOHNNY_TUI_TEST_PIPE=1", "HOME="+home, "TERM=xterm-256color")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	readDone := make(chan []byte, 1)
	go func() { output, _ := io.ReadAll(master); readDone <- output }()
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("非终端输入应以 1 退出：%v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		_ = slave.Close()
		_ = master.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		t.Fatal("非终端输入未被快速拒绝")
	}
	_ = slave.Close()
	var output []byte
	select {
	case output = <-readDone:
	case <-time.After(time.Second):
		_ = master.Close()
		output = <-readDone
	}
	if !bytes.Contains(output, []byte("只支持在终端")) || bytes.Contains(output, []byte{'\x1b'}) {
		t.Fatalf("非终端输入应返回纯文本限制提示：%q", output)
	}
}

func TestTerminalPTYExitWithoutTask(t *testing.T) {
	for _, key := range []string{"/exit\r", "\x04"} {
		t.Run(fmt.Sprintf("key=%q", key), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("退出不应请求模型") }))
			defer server.Close()
			p := startTerminal(t, server.URL)
			p.write(t, key)
			p.wait(t, 0)
			if _, err := os.Stat(filepath.Join(p.home, ".johnny")); !os.IsNotExist(err) {
				t.Fatalf("空会话创建了日志：%v", err)
			}
		})
	}
}
