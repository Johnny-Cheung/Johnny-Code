package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// symlinkDotDotFixture 建立 work/wl -> outside/002 的布局。wl 是符号链接，
// 因此 work/wl/../002 在真实文件系统中位于 outside，而词法清理会错误地
// 得到 work/002。
func symlinkDotDotFixture(t *testing.T) (base, work, outside string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows 符号链接测试需要额外权限")
	}
	base = mustEvalSymlinks(t, t.TempDir())
	work = filepath.Join(base, "work")
	outside = filepath.Join(base, "outside", "002")
	for _, dir := range []string{work, outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(work, "wl")); err != nil {
		t.Fatal(err)
	}
	return base, work, outside
}

// rawDotDotPath 构造未做任何词法清理的绝对路径 parent/link/../rest。
func rawDotDotPath(parent, link, rest string) string {
	return filepath.Join(parent, link) + string(filepath.Separator) + ".." + string(filepath.Separator) + rest
}

func TestResolveExistingAncestorsFollowsSymlinkBeforeDotDot(t *testing.T) {
	_, work, outside := symlinkDotDotFixture(t)
	raw := rawDotDotPath(work, "wl", filepath.Join("002", "written.txt"))
	got, err := resolveExistingAncestors(raw)
	want := filepath.Join(outside, "written.txt")
	if err != nil || got != want {
		t.Fatalf("符号链接后的 .. 解析错误：got=%q want=%q err=%v", got, want, err)
	}
	if got == filepath.Join(work, "002", "written.txt") {
		t.Fatalf("解析结果仍是词法清理后的错误目标：%q", got)
	}

	// 不存在的尾部（包括多层目录）必须保留，供尚未创建的文件使用。
	raw = rawDotDotPath(work, "wl", filepath.Join("002", "new", "deep", "file.txt"))
	got, err = resolveExistingAncestors(raw)
	want = filepath.Join(outside, "new", "deep", "file.txt")
	if err != nil || got != want {
		t.Fatalf("不存在尾部的解析错误：got=%q want=%q err=%v", got, want, err)
	}

	// 相对路径同样不能先拼接再词法清理。
	got, err = resolvePathFromBase(work, "wl/../002/written.txt")
	want = filepath.Join(outside, "written.txt")
	if err != nil || got != want {
		t.Fatalf("相对路径解析错误：got=%q want=%q err=%v", got, want, err)
	}

	// 相对符号链接目标也以链接所在目录为基准。
	if err := os.Symlink(filepath.Join("..", "outside", "002"), filepath.Join(work, "rel")); err != nil {
		t.Fatal(err)
	}
	got, err = resolvePathFromBase(work, "rel/../002/written.txt")
	if err != nil || got != want {
		t.Fatalf("相对符号链接解析错误：got=%q want=%q err=%v", got, want, err)
	}
}

func TestWorkspaceScopeRejectsSymlinkDotDotEscape(t *testing.T) {
	_, work, _ := symlinkDotDotFixture(t)
	scope, err := newWorkspaceScope(work)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"wl/../002/written.txt", "wl/../002"} {
		if _, err := scope.resolve(path); !errors.Is(err, errPathOutsideWorkspace) {
			t.Fatalf("符号链接 + .. 逃逸未被拒绝 %q：%v", path, err)
		}
		if _, err := scope.resolveExisting(path); !errors.Is(err, errPathOutsideWorkspace) {
			t.Fatalf("resolveExisting 符号链接 + .. 逃逸未被拒绝 %q：%v", path, err)
		}
	}
	// 普通的 .. 仍在允许范围内。
	inside := filepath.Join(work, "inside.txt")
	if err := os.WriteFile(inside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if resolved, err := scope.resolve("wl/../../work/inside.txt"); err != nil || resolved != inside {
		t.Fatalf("合法 .. 解析错误：%q %v", resolved, err)
	}
}

func TestMultiPathScopeDoesNotInheritLexicalProjectRule(t *testing.T) {
	base, work, outside := symlinkDotDotFixture(t)
	granted := filepath.Join(base, "grants", "002")
	if err := os.MkdirAll(granted, 0o700); err != nil {
		t.Fatal(err)
	}
	grantsRoot := filepath.Join(base, "grants")
	if err := os.Symlink(outside, filepath.Join(grantsRoot, "wl")); err != nil {
		t.Fatal(err)
	}
	rules, err := NewProjectRules(base, work)
	if err != nil {
		t.Fatal(err)
	}
	if err := rules.AddPath(granted, PathKindDir); err != nil {
		t.Fatal(err)
	}
	scope, err := newMultiPathScope(work, rules)
	if err != nil {
		t.Fatal(err)
	}
	// 词法清理会得到 grants/002/written.txt（已被路径规则授权），
	// 真实目标却是 outside/002/written.txt。
	raw := rawDotDotPath(grantsRoot, "wl", filepath.Join("002", "written.txt"))
	resolved, err := scope.resolve(raw)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(outside, "written.txt"); resolved != want {
		t.Fatalf("路径规则解析错误：got=%q want=%q", resolved, want)
	}
	if scope.contains(resolved) {
		t.Fatalf("词法上的项目路径规则不应覆盖真实的外部目标：%q", resolved)
	}
	// 对照：词法目标本身确实被规则覆盖，说明上面的拒绝来自真实解析。
	if !scope.contains(filepath.Join(granted, "written.txt")) {
		t.Fatal("测试前提错误：词法目标本应在路径规则内")
	}
}

func TestCanonicalPathRuleUsesSymlinkBeforeDotDot(t *testing.T) {
	base, _, outside := symlinkDotDotFixture(t)
	grantsRoot := filepath.Join(base, "grants")
	if err := os.MkdirAll(grantsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(grantsRoot, "wl")); err != nil {
		t.Fatal(err)
	}
	raw := rawDotDotPath(grantsRoot, "wl", filepath.Join("002", "note.txt"))
	rule, err := canonicalPathRule(raw, PathKindFile)
	if err != nil {
		t.Fatal(err)
	}
	want := PathRule{Path: filepath.Join(outside, "note.txt"), Kind: PathKindFile}
	if rule != want {
		t.Fatalf("路径规则未按真实目标规范化：got=%+v want=%+v", rule, want)
	}
	if rule.Path == filepath.Join(grantsRoot, "002", "note.txt") {
		t.Fatalf("路径规则仍是词法清理结果：%q", rule.Path)
	}
}

func TestResolveProgramPathFollowsSymlinkBeforeDotDot(t *testing.T) {
	_, work, outside := symlinkDotDotFixture(t)
	script := filepath.Join(outside, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	absolute := rawDotDotPath(work, "wl", filepath.Join("002", "run.sh"))
	if got, err := resolveProgramPath(absolute, "", work); err != nil || got != script {
		t.Fatalf("绝对程序路径解析错误：got=%q want=%q err=%v", got, script, err)
	}
	if got, err := resolveProgramPath("wl/../002/run.sh", work, ""); err != nil || got != script {
		t.Fatalf("相对程序路径解析错误：got=%q want=%q err=%v", got, script, err)
	}
	if _, err := resolveProgramPath("wl/../002/missing.sh", work, ""); err == nil {
		t.Fatal("不存在的程序应报错")
	}
}

func TestPathResolutionPreservesTrailingSpaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 文件名不允许以空格结尾")
	}
	base := mustEvalSymlinks(t, t.TempDir())
	spaced := filepath.Join(base, "project ")
	plain := filepath.Join(base, "project")
	for _, dir := range []string{spaced, plain} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(spaced, "file.txt"), []byte("spaced"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plain, "file.txt"), []byte("plain"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 以空格结尾的真实目录不能因为 TrimSpace 变成同名兄弟。
	if got, err := canonicalDirectory(spaced); err != nil || got != spaced {
		t.Fatalf("canonicalDirectory 丢失尾部空格：got=%q want=%q err=%v", got, spaced, err)
	}
	rules, err := NewProjectRules(base, spaced)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := newMultiPathScope(spaced, rules)
	if err != nil {
		t.Fatal(err)
	}
	if scope.workDir != spaced {
		t.Fatalf("工作目录被规范化到错误目标：got=%q want=%q", scope.workDir, spaced)
	}
	resolved, err := scope.resolve("file.txt")
	if err != nil || resolved != filepath.Join(spaced, "file.txt") {
		t.Fatalf("相对路径丢失尾部空格：got=%q err=%v", resolved, err)
	}
	if resolved == filepath.Join(plain, "file.txt") {
		t.Fatal("相对路径落到了同名兄弟目录")
	}
	// 操作数自身的尾部空格也必须保留。
	if err := os.WriteFile(filepath.Join(spaced, "file.txt "), []byte("spaced-name"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err = scope.resolve("file.txt ")
	if err != nil || resolved != filepath.Join(spaced, "file.txt ") {
		t.Fatalf("路径操作数丢失尾部空格：got=%q err=%v", resolved, err)
	}
	// 路径规则同样保留原始字节。
	rule, err := canonicalPathRule(filepath.Join(spaced, "file.txt "), PathKindFile)
	if err != nil || rule.Path != filepath.Join(spaced, "file.txt ") {
		t.Fatalf("路径规则丢失尾部空格：%+v %v", rule, err)
	}
}

func TestFileToolsUseKernelResolvedTargets(t *testing.T) {
	_, work, outside := symlinkDotDotFixture(t)
	decoy := filepath.Join(work, "002")
	if err := os.MkdirAll(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(work)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	raw := "wl/../002/file.txt"

	writeArgs, _ := json.Marshal(map[string]string{"path": raw, "content": "outside-content"})
	if _, err := NewWrite(runtime).Execute(ctx, writeArgs); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(outside, "file.txt"))
	if err != nil || string(data) != "outside-content" {
		t.Fatalf("write 未写入真实目标：%q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(decoy, "file.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write 不应在词法兄弟目录创建文件：%v", err)
	}

	readArgs, _ := json.Marshal(map[string]string{"path": raw})
	result, err := NewRead(runtime).Execute(ctx, readArgs)
	if err != nil || !strings.Contains(result.Content, "outside-content") {
		t.Fatalf("read 未读取真实目标：%q %v", result.Content, err)
	}

	editArgs, _ := json.Marshal(map[string]string{"path": raw, "old_string": "outside-content", "new_string": "edited"})
	if _, err := NewEdit(runtime).Execute(ctx, editArgs); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(outside, "file.txt"))
	if err != nil || string(data) != "edited" {
		t.Fatalf("edit 未修改真实目标：%q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(decoy, "file.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("edit 不应在词法兄弟目录创建文件：%v", err)
	}

	// 以空格结尾的文件名整条 CRUD 链路保持原字节。
	spacedName := "note "
	spacedArgs, _ := json.Marshal(map[string]string{"path": spacedName, "content": "spaced"})
	if _, err := NewWrite(runtime).Execute(ctx, spacedArgs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, "note ")); err != nil {
		t.Fatalf("write 未创建带尾部空格的文件：%v", err)
	}
	if _, err := os.Stat(filepath.Join(work, "note")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write 不应创建去掉空格的兄弟文件：%v", err)
	}
	readSpaced, _ := json.Marshal(map[string]string{"path": spacedName})
	result, err = NewRead(runtime).Execute(ctx, readSpaced)
	if err != nil || !strings.Contains(result.Content, "spaced") {
		t.Fatalf("read 带尾部空格的文件失败：%q %v", result.Content, err)
	}
	editSpaced, _ := json.Marshal(map[string]string{"path": spacedName, "old_string": "spaced", "new_string": "changed"})
	if _, err := NewEdit(runtime).Execute(ctx, editSpaced); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(work, "note ")); err != nil || string(data) != "changed" {
		t.Fatalf("edit 带尾部空格的文件失败：%q %v", data, err)
	}
}

func TestSessionPathGrantNotBypassedBySymlinkDotDot(t *testing.T) {
	base, _, outside := symlinkDotDotFixture(t)
	granted := filepath.Join(base, "grants", "002")
	if err := os.MkdirAll(granted, 0o700); err != nil {
		t.Fatal(err)
	}
	grantsRoot := filepath.Join(base, "grants")
	if err := os.Symlink(outside, filepath.Join(grantsRoot, "wl")); err != nil {
		t.Fatal(err)
	}
	raw := rawDotDotPath(grantsRoot, "wl", filepath.Join("002", "written.txt"))

	grants := NewSessionGrants()
	if _, _, err := grants.Store(nil, []PathRule{{Path: granted, Kind: PathKindDir}}); err != nil {
		t.Fatal(err)
	}
	// 词法目标是 grants/002/written.txt，但真实目标在 outside，
	// 已有的 grants/002 授权不能被用来绕过审批。
	if grants.ContainsPath(raw) {
		t.Fatalf("词法路径授权不应覆盖真实目标：%q", raw)
	}
	if !grants.ContainsPath(filepath.Join(granted, "written.txt")) {
		t.Fatal("测试前提错误：词法目标本应命中路径授权")
	}
	if _, _, err := grants.Store(nil, []PathRule{{Path: outside, Kind: PathKindDir}}); err != nil {
		t.Fatal(err)
	}
	if !grants.ContainsPath(raw) {
		t.Fatalf("真实目标授权后应命中：%q", raw)
	}
}

func TestScopeGuardUsesKernelResolvedTarget(t *testing.T) {
	_, work, outside := symlinkDotDotFixture(t)
	runtime, err := NewRuntime(work)
	if err != nil {
		t.Fatal(err)
	}
	claims := NewFileClaims()
	if err := claims.Claim("kid", "子任务", []string{outside}); err != nil {
		t.Fatal(err)
	}
	main := guardedTools(t, claims, runtime, "")
	// 词法目标是 work/002/a.txt，不在子任务范围内；真实目标是
	// outside/a.txt，必须被范围守卫拒绝。
	result, err := main.Execute(context.Background(), writeCall("wl/../002/a.txt", "x"))
	if err == nil || !result.IsError || !strings.Contains(err.Error(), "kid") {
		t.Fatalf("主 Agent 通过符号链接 + .. 写子任务范围应被拒绝：%+v %v", result, err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "a.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("被拒绝的写入不应创建真实目标文件：%v", statErr)
	}
}
