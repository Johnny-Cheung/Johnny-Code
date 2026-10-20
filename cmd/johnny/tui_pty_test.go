//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
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
	code := run(ctx, nil, os.Stdin, os.Stdout, os.Stderr)
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
	cmd.Env = append(cmd.Env, "JOHNNY_TUI_TEST_HELPER=1", "JOHNNY_TUI_TEST_CONFIG="+path, "HOME="+home, "TERM=xterm-256color")
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
	timer := time.NewTimer(10 * time.Second)
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
	case <-time.After(10 * time.Second):
		t.Fatal("终端未退出")
	}
	if p.cmd.ProcessState.ExitCode() != code {
		t.Fatalf("退出码 %d，预期 %d：%v\n%s", p.cmd.ProcessState.ExitCode(), code, p.err, p.text())
	}
	p.expect(t, 0, "TERMINAL_RESTORED")
	p.mu.Lock()
	raw := p.output.String()
	p.mu.Unlock()
	if strings.Contains(raw, "johnny-fence=") {
		t.Fatal("内部写入屏障泄漏到终端")
	}
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
				{"type": "tool_use", "id": "write-1", "name": "write", "input": map[string]any{"path": "note.txt", "content": "hello"}},
				{"type": "tool_use", "id": "read-1", "name": "read", "input": map[string]any{"path": "note.txt"}},
				{"type": "tool_use", "id": "edit-1", "name": "edit", "input": map[string]any{"path": "note.txt", "old_string": "hello", "new_string": "world"}},
				{"type": "tool_use", "id": "bash-1", "name": "bash", "input": map[string]any{"command": "printf bash-ok"}},
			})
		case 2:
			var text strings.Builder
			text.WriteString("```text\n")
			for i := 0; i < 100; i++ {
				fmt.Fprintf(&text, "history-line-%03d\n", i)
			}
			text.WriteString("```\n\n```go\nfmt.Println(\"world\")\n```\n\nFIRST_DONE")
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
	firstEnd := p.expect(t, start, "FIRST_DONE")
	p.expect(t, firstEnd, "Enter 发送")
	firstBody := <-requests
	if !strings.Contains(firstBody, `请处理代码\nfunc main() {}\n再运行检查`) {
		t.Fatalf("多行内容不完整：%s", firstBody)
	}
	secondBody := <-requests
	for _, want := range []string{"write-1", "read-1", "edit-1", "bash-1", "bash-ok"} {
		if !strings.Contains(secondBody, want) {
			t.Fatalf("缺少工具结果 %q", want)
		}
	}
	data, err := os.ReadFile(filepath.Join(p.dir, "note.txt"))
	if err != nil || string(data) != "world" {
		t.Fatalf("文件未修改：%s %v", data, err)
	}
	for _, want := range []string{"history-line-000", "history-line-099", "→ write", "→ read", "→ edit", "→ bash", "bash-ok"} {
		if !strings.Contains(p.text(), want) {
			t.Fatalf("scrollback 缺少 %q", want)
		}
	}
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
	for _, want := range []string{`"user_turn":1`, `"user_turn":2`, `"user_turn":3`, `"status":"canceled"`} {
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

func TestTerminalPTYOutputWithPlainReaderStaysPlain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		terminalAnswer(w, "PLAIN_OUTPUT")
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
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		_ = slave.Close()
		_ = master.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		t.Fatal("普通文本模式被终端查询阻塞")
	}
	_ = slave.Close()
	var output []byte
	select {
	case output = <-readDone:
	case <-time.After(time.Second):
		_ = master.Close()
		output = <-readDone
	}
	if !bytes.Contains(output, []byte("PLAIN_OUTPUT")) || bytes.Contains(output, []byte{'\x1b'}) {
		t.Fatalf("管道输入不应触发 TUI 或颜色查询：%q", output)
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
