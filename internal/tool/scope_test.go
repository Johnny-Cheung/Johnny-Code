package tool

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWorkspaceScopeResolvesInsidePaths(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inside, []byte("inside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=1"), 0600); err != nil {
		t.Fatal(err)
	}
	scope, err := newWorkspaceScope(root)
	if err != nil {
		t.Fatal(err)
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"inside.txt", ".env", ".", "./sub/../inside.txt", inside} {
		resolved, err := scope.resolve(path)
		if err != nil {
			t.Fatalf("解析工作目录内路径 %q 失败：%v", path, err)
		}
		if !filepath.IsAbs(resolved) || !scope.contains(resolved) {
			t.Fatalf("路径 %q 未解析为工作目录内绝对路径：%q", path, resolved)
		}
	}
	if resolved, err := scope.resolve("inside.txt"); err != nil || resolved != filepath.Join(rootResolved, "inside.txt") {
		t.Fatalf("相对路径解析错误：%q %v", resolved, err)
	}
	// 不存在的目标仍可解析，交给 read 返回原有的不存在错误。
	missing, err := scope.resolve("new-dir/new.txt")
	if err != nil {
		t.Fatalf("工作目录内缺失目标应可解析：%v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("缺失目标解析结果异常：%v", err)
	}
}

func TestWorkspaceScopeRejectsOutsidePaths(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	other := filepath.Join(base, "project-other")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(other, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "outside.txt"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	scope, err := newWorkspaceScope(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"../project-other/outside.txt",
		filepath.Join(other, "outside.txt"),
		"../../etc/passwd",
	} {
		if _, err := scope.resolve(path); !errors.Is(err, errPathOutsideWorkspace) {
			t.Fatalf("越界路径 %q 未被拒绝：%v", path, err)
		}
	}
	// 共享字符串前缀不是同一目录。
	if _, err := scope.resolve("../project-other"); !errors.Is(err, errPathOutsideWorkspace) {
		t.Fatalf("共享前缀目录未被拒绝：%v", err)
	}
}

func TestWorkspaceScopeChecksSymlinkTargets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 的符号链接测试需要额外权限")
	}
	base := t.TempDir()
	root := filepath.Join(base, "project")
	outsideDir := filepath.Join(base, "outside")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outsideDir, 0700); err != nil {
		t.Fatal(err)
	}
	insideFile := filepath.Join(root, "sub", "inside.txt")
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(insideFile, []byte("inside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsideFile, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(insideFile, filepath.Join(root, "link-inside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(root, "link-outside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(root, "dir-outside")); err != nil {
		t.Fatal(err)
	}
	scope, err := newWorkspaceScope(root)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := scope.resolve("link-inside"); err != nil || resolved != mustEvalSymlinks(t, insideFile) {
		t.Fatalf("工作目录内的符号链接应允许：%q %v", resolved, err)
	}
	for _, path := range []string{"link-outside", "dir-outside", "dir-outside/new.txt"} {
		if _, err := scope.resolve(path); !errors.Is(err, errPathOutsideWorkspace) {
			t.Fatalf("指向工作目录外的符号链接未被拒绝 %q：%v", path, err)
		}
	}
}

func TestWorkspaceScopeHandlesSymlinkedWorkDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 的符号链接测试需要额外权限")
	}
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	linkRoot := filepath.Join(base, "link")
	if err := os.MkdirAll(realRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realRoot, "file.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	scope, err := newWorkspaceScope(linkRoot)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := scope.resolve("file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != mustEvalSymlinks(t, filepath.Join(realRoot, "file.txt")) {
		t.Fatalf("工作目录符号链接未被规范化：%q", resolved)
	}
}

func mustEvalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestWorkspaceScopeResolveExisting(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	scope, err := newWorkspaceScope(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scope.resolveExisting("file.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := scope.resolveExisting("missing.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("缺失目标应返回 not exist：%v", err)
	}
	if _, err := scope.resolveExisting(".."); !errors.Is(err, errPathOutsideWorkspace) {
		t.Fatalf("越界目标未被拒绝：%v", err)
	}
}

func TestWorkspaceScopeRejectsInvalidWorkDirAndPaths(t *testing.T) {
	if _, err := newWorkspaceScope(""); err == nil {
		t.Fatal("空工作目录应失败")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newWorkspaceScope(file); err == nil {
		t.Fatal("文件工作目录应失败")
	}
	if _, err := newWorkspaceScope(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("不存在的工作目录应失败")
	}
	scope, err := newWorkspaceScope(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "  ", "bad\x00path"} {
		if _, err := scope.resolve(path); err == nil {
			t.Fatalf("非法路径 %q 应失败", path)
		}
	}
}

func TestWorkspaceScopeSymlinkLoopDoesNotPanic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 的符号链接测试需要额外权限")
	}
	root := t.TempDir()
	if err := os.Symlink("loop", filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	scope, err := newWorkspaceScope(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scope.resolve("loop"); err == nil {
		t.Fatal("符号链接环应失败")
	}
}

func TestWorkspaceScopeErrorMessagesDoNotLeakPaths(t *testing.T) {
	root := t.TempDir()
	scope, err := newWorkspaceScope(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = scope.resolve("../outside")
	if err == nil || !strings.Contains(err.Error(), "路径超出工作目录") {
		t.Fatalf("越界错误信息不明确：%v", err)
	}
}
