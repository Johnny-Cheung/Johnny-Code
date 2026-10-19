package tool

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func skipWithoutBash(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("bash 工具依赖类 Unix shell")
	}
}

func TestBashRunsInRuntimeAndReturnsOutput(t *testing.T) {
	skipWithoutBash(t)
	dir := t.TempDir()
	runtime, err := NewRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Env = append(os.Environ(), "JOHNNY_TOOL_TEST=ok")
	bash := NewBash(runtime)
	result, err := bash.Execute(context.Background(), []byte(`{"command":"printf '%s' \"$JOHNNY_TOOL_TEST\"; pwd"}`))
	if err != nil || !strings.Contains(result.Content, "ok") || !strings.Contains(result.Content, dir) {
		t.Fatalf("bash 输出或工作目录错误：%q %v", result.Content, err)
	}
	result, err = bash.Execute(context.Background(), []byte(`{"command":"printf 'error' >&2; exit 3"}`))
	if err == nil || !result.IsError || !strings.Contains(result.Content, "[stderr]") || !strings.Contains(result.Content, "[exit code 3]") {
		t.Fatalf("bash 非零退出处理错误：%q %v", result.Content, err)
	}
	var toolErr *Error
	if !errors.As(err, &toolErr) || toolErr.Kind != KindExecution {
		t.Fatalf("bash 错误分类错误：%v", err)
	}
	if !strings.Contains(result.Content, "error") {
		t.Fatal("bash 未保留 stderr")
	}
}

func TestBashTimeoutCancellationAndOutputLimit(t *testing.T) {
	skipWithoutBash(t)
	dir := t.TempDir()
	runtime, err := NewRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	bash := NewBash(runtime)
	started := time.Now()
	result, err := bash.Execute(context.Background(), []byte(`{"command":"sleep 5","timeout_seconds":1}`))
	if !errors.Is(err, context.DeadlineExceeded) || !result.IsError || time.Since(started) > 3*time.Second {
		t.Fatalf("bash 超时处理错误：%q %v", result.Content, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	started = time.Now()
	_, err = bash.Execute(ctx, []byte(`{"command":"sleep 5"}`))
	if !errors.Is(err, context.Canceled) || time.Since(started) > 3*time.Second {
		t.Fatalf("bash 取消处理错误：%v", err)
	}
	runtime.MaxOutputBytes = 5
	bash = NewBash(runtime)
	result, err = bash.Execute(context.Background(), []byte(`{"command":"printf 123456789"}`))
	if err != nil || !strings.Contains(result.Content, "输出已截断") {
		t.Fatalf("bash 输出截断错误：%q %v", result.Content, err)
	}
}

func TestBashRejectsInvalidArguments(t *testing.T) {
	skipWithoutBash(t)
	runtime, err := NewRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bash := NewBash(runtime)
	for _, input := range []string{`{"command":""}`, `{"command":"true","timeout_seconds":0}`, `{"command":"true","timeout_seconds":601}`, `{"command":"true","timeout_seconds":"1"}`} {
		if _, err := bash.Execute(context.Background(), []byte(input)); err == nil {
			t.Fatalf("非法 bash 参数应失败：%s", input)
		}
	}
	if _, err := bash.Execute(context.Background(), []byte(`{"command":"definitely-not-a-real-command"}`)); err == nil {
		t.Fatal("不存在命令应返回非零退出错误")
	}
	if _, err := os.Stat(filepath.Join(runtime.WorkDir, "unexpected")); !os.IsNotExist(err) {
		t.Fatal("bash 测试产生了意外文件")
	}
}

func assertNoProcess(t *testing.T, pattern string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for exec.Command("pgrep", "-f", pattern).Run() == nil {
		if time.Now().After(deadline) {
			_ = exec.Command("pkill", "-f", pattern).Run()
			t.Fatalf("仍有残留进程：%s", pattern)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestBashBackgroundProcesses(t *testing.T) {
	skipWithoutBash(t)
	dir := t.TempDir()
	runtime, err := NewRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	bash := NewBash(runtime)

	// shell 先退出、后台子进程仍占用输出管道时，命令本身已经成功；
	// 工具返回前应清理该后台进程，不误报失败。
	result, err := bash.Execute(context.Background(), []byte(`{"command":"sleep 987654 & echo ok"}`))
	if err != nil || result.IsError || !strings.Contains(result.Content, "ok") {
		t.Fatalf("后台进程不应导致命令误报失败：%q %v", result.Content, err)
	}
	assertNoProcess(t, "sleep 987654")

	// 前台命令超时后应终止整个进程组，不残留子进程。
	result, err = bash.Execute(context.Background(), []byte(`{"command":"sleep 987654; echo done","timeout_seconds":1}`))
	if !errors.Is(err, context.DeadlineExceeded) || !result.IsError {
		t.Fatalf("前台命令超时应被报告：%q %v", result.Content, err)
	}
	assertNoProcess(t, "sleep 987654")
}
