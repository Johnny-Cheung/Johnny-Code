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

func TestReadReturnsNumberedTextAndRanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("第一行\n第二行\n第三行\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	reader := NewRead(runtime)
	result, err := reader.Execute(context.Background(), []byte(`{"path":"notes.txt"}`))
	if err != nil || !strings.Contains(result.Content, "     1\t第一行\n") || !strings.Contains(result.Content, "     3\t第三行\n") {
		t.Fatalf("read 内容错误：%q %v", result.Content, err)
	}
	result, err = reader.Execute(context.Background(), []byte(`{"path":"notes.txt","offset":2,"limit":1}`))
	if err != nil || result.Content != "     2\t第二行\n" {
		t.Fatalf("read 范围错误：%q %v", result.Content, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	result, err = reader.Execute(context.Background(), []byte(`{"path":"empty.txt"}`))
	if err != nil || result.Content != "" {
		t.Fatalf("空文件读取错误：%q %v", result.Content, err)
	}
}

func TestReadRejectsInvalidFilesAndSupportsOutputLimit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "binary"), []byte{0xff, 0xfe}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	reader := NewRead(runtime)
	for _, input := range []string{`{"path":"missing"}`, `{"path":"folder"}`, `{"path":"binary"}`, `{"path":"file","offset":9}`} {
		if input == `{"path":"file","offset":9}` {
			if err := os.WriteFile(filepath.Join(dir, "file"), []byte("one\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		_, err := reader.Execute(context.Background(), []byte(input))
		if err == nil {
			t.Fatalf("无效文件输入应失败：%s", input)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "large"), []byte("1234567890\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runtime.MaxOutputBytes = 8
	reader = NewRead(runtime)
	result, err := reader.Execute(context.Background(), []byte(`{"path":"large"}`))
	if err != nil || !strings.Contains(result.Content, "输出已截断") || result.IsError {
		t.Fatalf("read 截断处理错误：%q %v", result.Content, err)
	}
}

func TestReadUsesRuntimeDirectoryAndRespondsToCancel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	t.Chdir(other)
	if _, err := NewRead(runtime).Execute(context.Background(), []byte(`{"path":"file"}`)); err != nil {
		t.Fatalf("read 不应依赖进程 cwd：%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewRead(runtime).Execute(ctx, []byte(`{"path":"file"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("read 未响应预取消：%v", err)
	}
}

func TestReadRejectsFIFOWithoutBlocking(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("FIFO 测试依赖类 Unix 系统")
	}
	dir := t.TempDir()
	if output, err := exec.Command("mkfifo", filepath.Join(dir, "pipe")).CombinedOutput(); err != nil {
		t.Skipf("无法创建 FIFO：%v %s", err, output)
	}
	runtime, err := NewRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	reader := NewRead(runtime)
	done := make(chan error, 1)
	go func() {
		_, err := reader.Execute(context.Background(), []byte(`{"path":"pipe"}`))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("read 不应读取 FIFO")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("read 在 FIFO 上阻塞")
	}
}

func TestReadTruncatesVeryLongLines(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("a", 4096)
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), []byte(long+"\nshort\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	runtime.MaxOutputBytes = 1024
	result, err := NewRead(runtime).Execute(context.Background(), []byte(`{"path":"long.txt"}`))
	if err != nil || !strings.Contains(result.Content, "输出已截断") || len(result.Content) > 2048 {
		t.Fatalf("超长行截断处理错误：长度 %d，err=%v", len(result.Content), err)
	}
}
