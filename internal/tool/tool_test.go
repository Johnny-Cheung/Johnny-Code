package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testRuntime(t *testing.T) Runtime {
	t.Helper()
	runtime, err := NewRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func TestErrorAndModelResult(t *testing.T) {
	cause := errors.New("底层错误")
	err := &Error{Kind: KindExecution, Message: "执行失败", Err: cause}
	if err.Error() != "执行失败" || !errors.Is(err, cause) {
		t.Fatal("工具错误没有保留消息或错误链")
	}
	result := ToModelResult("call-1", Result{Content: "结果"}, nil)
	if result.ToolCallID != "call-1" || result.Content != "结果" || result.IsError {
		t.Fatal("成功工具结果转换错误")
	}
	result = ToModelResult("call-2", Result{}, err)
	if result.ToolCallID != "call-2" || result.Content != "执行失败" || !result.IsError {
		t.Fatal("失败工具结果转换错误")
	}
	result = ToModelResult("call-3", Result{Content: "部分结果", IsError: true}, nil)
	if result.Content != "部分结果" || !result.IsError {
		t.Fatal("带错误标记的工具结果转换错误")
	}
}

func TestNewRuntime(t *testing.T) {
	dir := t.TempDir()
	runtime, err := NewRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.WorkDir != absolute || runtime.CommandTimeout != DefaultCommandTimeout || runtime.MaxOutputBytes != DefaultMaxOutputBytes {
		t.Fatalf("运行时默认值错误：%+v", runtime)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRuntime(file); err == nil {
		t.Fatal("文件不应作为工作目录")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(contextError(ctx), context.Canceled) {
		t.Fatal("取消上下文没有被识别")
	}
	if DefaultCommandTimeout < time.Second {
		t.Fatal("默认命令超时异常")
	}
}
