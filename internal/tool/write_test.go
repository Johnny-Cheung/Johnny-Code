package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestWriteAppendsWithoutRewriting(t *testing.T) {
	dir := t.TempDir()
	runtime, err := NewRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	writer := NewWrite(runtime)
	path := filepath.Join(dir, "report.md")
	if err := os.WriteFile(path, []byte("# 标题\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Execute(context.Background(), []byte(`{"path":"report.md","content":"第一段\n","append":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Execute(context.Background(), []byte(`{"path":"report.md","content":"第二段\n","append":true}`)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "# 标题\n第一段\n第二段\n" {
		t.Fatalf("append 内容错误：%q %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("append 应保留已有权限：%v", err)
	}
	if _, err := writer.Execute(context.Background(), []byte(`{"path":"new-report.md","content":"开头\n","append":true}`)); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "new-report.md"))
	if err != nil || string(data) != "开头\n" {
		t.Fatalf("append 应能创建新文件：%q %v", data, err)
	}
}

func TestWriteAppendDoesNotFollowHardlinkOutside(t *testing.T) {
	dir := t.TempDir()
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "authorized_keys")
	if err := os.WriteFile(outside, []byte("ORIGINAL\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "notes.md")
	if err := os.Link(outside, link); err != nil {
		t.Skipf("当前文件系统不支持硬链接：%v", err)
	}
	runtime, err := NewRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewWrite(runtime).Execute(context.Background(), []byte(`{"path":"notes.md","content":"APPENDED\n","append":true}`)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "ORIGINAL\n" {
		t.Fatalf("append 不应跟随工作区内的硬链接写到外部文件：%q %v", data, err)
	}
	data, err = os.ReadFile(link)
	if err != nil || string(data) != "ORIGINAL\nAPPENDED\n" {
		t.Fatalf("工作区路径应得到追加后的副本：%q %v", data, err)
	}
}

func TestWriteApprovalDetailDistinguishesAppend(t *testing.T) {
	_, detail, err := fileArgument("write", []byte(`{"path":"report.md","content":"下一段","append":true}`))
	if err != nil || !strings.Contains(detail, "追加") || !strings.Contains(detail, "下一段") {
		t.Fatalf("追加写应在审批里标明：%q %v", detail, err)
	}
	_, detail, err = fileArgument("write", []byte(`{"path":"report.md","content":"下一段"}`))
	if err != nil || !strings.Contains(detail, "写入") {
		t.Fatalf("覆盖写应保持原文案：%q %v", detail, err)
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
