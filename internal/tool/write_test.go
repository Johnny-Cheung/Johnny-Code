package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCreatesAndOverwritesFiles(t *testing.T) {
	dir := t.TempDir()
	runtime, err := NewRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	writer := NewWrite(runtime)
	if _, err := writer.Execute(context.Background(), []byte(`{"path":"new.txt","content":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("write 创建内容错误：%q %v", data, err)
	}
	path := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Execute(context.Background(), []byte(`{"path":"existing.txt","content":"new"}`)); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "new" {
		t.Fatalf("write 覆盖内容错误：%q %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("write 未保留权限：%v", err)
	}
	if _, err := writer.Execute(context.Background(), []byte(`{"path":"empty.txt","content":""}`)); err != nil {
		t.Fatal(err)
	}
}

func TestWriteRejectsInvalidTargetsAndCancellation(t *testing.T) {
	dir := t.TempDir()
	runtime, err := NewRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	writer := NewWrite(runtime)
	for _, input := range []string{
		`{"path":"missing-content"}`,
		`{"path":"","content":"x"}`,
		`{"path":"nested/file","content":"x"}`,
	} {
		if _, err := writer.Execute(context.Background(), []byte(input)); err == nil {
			t.Fatalf("非法 write 应失败：%s", input)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Execute(context.Background(), []byte(`{"path":"folder","content":"x"}`)); err == nil {
		t.Fatal("write 不应覆盖目录")
	}
	if _, err := os.Stat(filepath.Join(dir, "nested")); !os.IsNotExist(err) {
		t.Fatal("write 不应自动创建父目录")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := writer.Execute(ctx, []byte(`{"path":"cancelled","content":"x"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("write 未响应取消：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cancelled")); !os.IsNotExist(err) {
		t.Fatal("取消的 write 不应创建文件")
	}
}
