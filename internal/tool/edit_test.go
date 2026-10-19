package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditReplacesTextAndPreservesMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("one\ntwo\none\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	editor := NewEdit(runtime)
	result, err := editor.Execute(context.Background(), []byte(`{"path":"main.go","old_string":"two","new_string":"changed"}`))
	if err != nil || !strings.Contains(result.Content, "替换 1 处") {
		t.Fatalf("edit 成功结果错误：%q %v", result.Content, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "one\nchanged\none\n" {
		t.Fatalf("edit 内容错误：%q %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("edit 未保留权限：%v", err)
	}
	result, err = editor.Execute(context.Background(), []byte(`{"path":"main.go","old_string":"one","new_string":"1","replace_all":true}`))
	if err != nil || !strings.Contains(result.Content, "替换 2 处") {
		t.Fatalf("edit replace_all 错误：%q %v", result.Content, err)
	}
}

func TestEditFailuresDoNotModifyOrCreateFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	original := []byte("same same")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	editor := NewEdit(runtime)
	for _, input := range []string{
		`{"path":"main.go","old_string":"missing","new_string":"x"}`,
		`{"path":"main.go","old_string":"same","new_string":"x"}`,
		`{"path":"main.go","old_string":"","new_string":"x"}`,
		`{"path":"main.go","old_string":"same","new_string":"same"}`,
	} {
		if _, err := editor.Execute(context.Background(), []byte(input)); err == nil {
			t.Fatalf("非法 edit 应失败：%s", input)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || string(data) != string(original) {
			t.Fatalf("失败 edit 修改了文件：%q %v", data, readErr)
		}
	}
	missing := filepath.Join(dir, "missing.go")
	_, err = editor.Execute(context.Background(), []byte(`{"path":"missing.go","old_string":"x","new_string":"y"}`))
	if err == nil || !errors.As(err, new(*Error)) {
		t.Fatal("不存在目标文件应返回工具错误")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("edit 不应创建不存在的文件")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := editor.Execute(ctx, []byte(`{"path":"main.go","old_string":"same","new_string":"x"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("edit 未响应取消：%v", err)
	}
}

func TestEditFollowsSymbolicLink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(real, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("当前系统不支持创建符号链接：%v", err)
	}
	runtime, err := NewRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewEdit(runtime).Execute(context.Background(), []byte(`{"path":"link.txt","old_string":"before","new_string":"after"}`)); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("edit 不应替换符号链接本身")
	}
	if data, err := os.ReadFile(real); err != nil || string(data) != "after" {
		t.Fatalf("edit 应写入链接指向的文件：%q %v", data, err)
	}
}
