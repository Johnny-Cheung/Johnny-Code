package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePathAndLimitedWriter(t *testing.T) {
	runtime := Runtime{WorkDir: "/tmp/work"}
	if got, err := resolvePath(runtime, "src/main.go"); err != nil || got != filepath.Join("/tmp/work", "src", "main.go") {
		t.Fatalf("相对路径解析错误：%q %v", got, err)
	}
	if got, err := resolvePath(runtime, "/tmp/other.go"); err != nil || got != "/tmp/other.go" {
		t.Fatalf("绝对路径解析错误：%q %v", got, err)
	}
	for _, path := range []string{"", "bad\x00path"} {
		if _, err := resolvePath(runtime, path); err == nil {
			t.Fatalf("无效路径应被拒绝：%q", path)
		}
	}
	writer := &limitedWriter{limit: 5}
	if n, err := writer.Write([]byte("123456789")); err != nil || n != 9 {
		t.Fatalf("有界 writer 不应阻塞上游：n=%d err=%v", n, err)
	}
	if !strings.Contains(writer.String(), "输出已截断") || !strings.HasPrefix(writer.String(), "12345") {
		t.Fatalf("截断输出错误：%q", writer.String())
	}
}

func TestAtomicWriteFilePreservesModeAndCleansFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("旧内容"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(path, []byte("新内容"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "新内容" {
		t.Fatalf("原子写内容错误：%q %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("原子写权限错误：%v", err)
	}
	if err := atomicWriteFile(filepath.Join(dir, "missing", "file.txt"), []byte("x"), 0644); err == nil {
		t.Fatal("不存在父目录时原子写应失败")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".johnny-tmp-") {
			t.Fatalf("失败后留下临时文件：%s", entry.Name())
		}
	}
}
