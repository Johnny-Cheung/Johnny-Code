package tool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/memory"
	"github.com/Johnny-Cheung/Johnny-Code/internal/skill"
)

func sessionDefaultTest(t *testing.T) (*Default, Runtime, *ProjectRules, *fakeApprover, *ApprovalGate) {
	t.Helper()
	runtime := testRuntime(t)
	registry, err := NewDefaultRegistry(runtime)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := NewProjectRules(runtime.WorkDir, runtime.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	return newSessionDefault(t, registry, runtime, rules)
}

func newSessionDefault(t *testing.T, registry *Registry, runtime Runtime, rules *ProjectRules) (*Default, Runtime, *ProjectRules, *fakeApprover, *ApprovalGate) {
	t.Helper()
	gate := NewApprovalGate()
	approver := &fakeApprover{}
	gate.Set(func(ctx context.Context, request ApprovalRequest) (ApprovalDecision, error) {
		approver.mu.Lock()
		approver.requests = append(approver.requests, request)
		decision, err := approver.decision, approver.err
		approver.mu.Unlock()
		return decision, err
	})
	permissioned, err := NewDefault(registry, runtime, rules, gate)
	if err != nil {
		t.Fatal(err)
	}
	return permissioned, runtime, rules, approver, gate
}

func mustBash(t *testing.T, permissioned *Default, command string) Result {
	t.Helper()
	result, err := executeDefault(t, permissioned, "bash", map[string]any{"command": command})
	if err != nil {
		t.Fatalf("bash %q 失败：%v", command, err)
	}
	return result
}

func requireApprovals(t *testing.T, approver *fakeApprover, want int) []ApprovalRequest {
	t.Helper()
	requests := approver.snapshot()
	if len(requests) != want {
		t.Fatalf("审批次数应为 %d，实际 %d：%#v", want, len(requests), requests)
	}
	return requests
}

func TestDefaultSessionExactArgvKey(t *testing.T) {
	permissioned, runtime, _, approver, gate := sessionDefaultTest(t)
	approver.setDecision(ApprovalDecision{Allow: true, Session: true})

	mustBash(t, permissioned, "touch one.txt")
	requests := requireApprovals(t, approver, 1)
	if !requests[0].CanSession() || len(requests[0].SessionCommands) != 1 {
		t.Fatalf("直接命令应提供精确 argv 会话键：%#v", requests[0])
	}
	grant := requests[0].SessionCommands[0]
	if grant.WorkDir != permissioned.scope.workDir || !stringSlicesEqual(grant.Argv, []string{"touch", "one.txt"}) || len(grant.Prefix) != 0 || len(grant.Redirects) != 0 {
		t.Fatalf("精确 argv 会话键异常：%#v", grant)
	}
	if !gate.TrySession(requests[0]) {
		t.Fatal("选择会话授权后应命中")
	}

	// 完全相同 argv 不再询问。
	mustBash(t, permissioned, "touch one.txt")
	requireApprovals(t, approver, 1)

	// 参数不同必须重新询问。
	mustBash(t, permissioned, "touch two.txt")
	requests = requireApprovals(t, approver, 2)
	if !stringSlicesEqual(requests[1].SessionCommands[0].Argv, []string{"touch", "two.txt"}) {
		t.Fatalf("不同参数不应复用授权：%#v", requests[1].SessionCommands)
	}

	// 重定向不同必须重新询问；相同重定向再次命中。
	mustBash(t, permissioned, "touch one.txt >/dev/null")
	requests = requireApprovals(t, approver, 3)
	if !stringSlicesEqual(requests[2].SessionCommands[0].Argv, []string{"touch", "one.txt"}) ||
		!stringSlicesEqual(requests[2].SessionCommands[0].Redirects, []string{">/dev/null"}) {
		t.Fatalf("重定向应作为精确键的一部分：%#v", requests[2].SessionCommands)
	}
	mustBash(t, permissioned, "touch one.txt >/dev/null")
	requireApprovals(t, approver, 3)

	mustBash(t, permissioned, "touch one.txt 2>&1")
	requireApprovals(t, approver, 4)
	mustBash(t, permissioned, "touch one.txt 2>&1")
	requireApprovals(t, approver, 4)

	// 相同 argv 在不同工作目录必须重新询问。
	sub := filepath.Join(runtime.WorkDir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	mustBash(t, permissioned, "cd sub && touch one.txt")
	requests = requireApprovals(t, approver, 5)
	resolvedSub := mustEvalSymlinks(t, sub)
	grant = requests[4].SessionCommands[0]
	if grant.WorkDir != resolvedSub || !stringSlicesEqual(grant.Argv, []string{"touch", "one.txt"}) {
		t.Fatalf("不同工作目录应生成独立授权键：%#v", grant)
	}
	mustBash(t, permissioned, "cd sub && touch one.txt")
	requireApprovals(t, approver, 5)
}

func TestDefaultSessionPrefixMatchesArgsButNotCwd(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("系统缺少 make")
	}
	permissioned, runtime, _, approver, gate := sessionDefaultTest(t)
	makefile := "test:\n\t@printf 'root-ok\\n'\n"
	if err := os.WriteFile(filepath.Join(runtime.WorkDir, "Makefile"), []byte(makefile), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(runtime.WorkDir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "Makefile"), []byte(makefile), 0o600); err != nil {
		t.Fatal(err)
	}
	approver.setDecision(ApprovalDecision{Allow: true, Session: true})

	mustBash(t, permissioned, "make test")
	requests := requireApprovals(t, approver, 1)
	if len(requests[0].SessionCommands) != 1 || !stringSlicesEqual(requests[0].SessionCommands[0].Prefix, []string{"make", "test"}) {
		t.Fatalf("应提供 make test 前缀会话键：%#v", requests[0].SessionCommands)
	}
	if !gate.TrySession(requests[0]) {
		t.Fatal("前缀会话授权应命中")
	}

	// 同前缀、不同参数（Makefile 变量）继续命中。
	mustBash(t, permissioned, "make test EXTRA=1")
	requireApprovals(t, approver, 1)

	// 同前缀、不同工作目录必须重新询问。
	mustBash(t, permissioned, "cd sub && make test")
	requests = requireApprovals(t, approver, 2)
	grant := requests[1].SessionCommands[0]
	if grant.WorkDir != mustEvalSymlinks(t, sub) || !stringSlicesEqual(grant.Prefix, []string{"make", "test"}) {
		t.Fatalf("不同工作目录的前缀键异常：%#v", grant)
	}
	mustBash(t, permissioned, "cd sub && make test")
	requireApprovals(t, approver, 2)
}

func TestDefaultSessionOutsidePathsFileAndBash(t *testing.T) {
	permissioned, _, rules, approver, gate := sessionDefaultTest(t)
	approver.setDecision(ApprovalDecision{Allow: true, Session: true})

	outsideDir := t.TempDir()
	readFile := filepath.Join(outsideDir, "read.txt")
	if err := os.WriteFile(readFile, []byte("outside-read"), 0o600); err != nil {
		t.Fatal(err)
	}
	catFile := filepath.Join(outsideDir, "cat.txt")
	if err := os.WriteFile(catFile, []byte("outside-cat"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile := filepath.Join(outsideDir, "write.txt")
	resolvedRead := mustEvalSymlinks(t, readFile)
	resolvedCat := mustEvalSymlinks(t, catFile)

	result, err := executeDefault(t, permissioned, "read", map[string]any{"path": readFile})
	if err != nil || !strings.Contains(result.Content, "outside-read") {
		t.Fatalf("首次读取外部文件失败：%q %v", result.Content, err)
	}
	requests := requireApprovals(t, approver, 1)
	if !requests[0].CanSession() || len(requests[0].SessionPaths) != 1 || requests[0].SessionPaths[0].Path != resolvedRead {
		t.Fatalf("文件命令应提供会话路径：%#v", requests[0])
	}
	if !gate.Session().ContainsPath(resolvedRead) {
		t.Fatal("会话路径未保存")
	}
	result, err = executeDefault(t, permissioned, "read", map[string]any{"path": readFile})
	if err != nil || !strings.Contains(result.Content, "outside-read") {
		t.Fatalf("已授权路径读取失败：%q %v", result.Content, err)
	}
	requireApprovals(t, approver, 1)

	result, err = executeDefault(t, permissioned, "bash", map[string]any{"command": "cat " + catFile})
	if err != nil || !strings.Contains(result.Content, "outside-cat") {
		t.Fatalf("首次读取外部 bash 路径失败：%q %v", result.Content, err)
	}
	requests = requireApprovals(t, approver, 2)
	if !requests[1].CanSession() || len(requests[1].SessionPaths) != 1 || requests[1].SessionPaths[0].Path != resolvedCat {
		t.Fatalf("bash 查询应提供会话路径：%#v", requests[1])
	}
	result, err = executeDefault(t, permissioned, "bash", map[string]any{"command": "cat " + catFile})
	if err != nil || !strings.Contains(result.Content, "outside-cat") {
		t.Fatalf("已授权 bash 路径读取失败：%q %v", result.Content, err)
	}
	requireApprovals(t, approver, 2)

	result, err = executeDefault(t, permissioned, "write", map[string]any{"path": writeFile, "content": "written"})
	if err != nil || !strings.Contains(result.Content, "已写入") {
		t.Fatalf("首次写入外部文件失败：%q %v", result.Content, err)
	}
	requests = requireApprovals(t, approver, 3)
	if len(requests[2].SessionPaths) != 1 || requests[2].SessionPaths[0].Kind != PathKindFile {
		t.Fatalf("写入外部文件应提供文件会话路径：%#v", requests[2].SessionPaths)
	}
	result, err = executeDefault(t, permissioned, "write", map[string]any{"path": writeFile, "content": "written-again"})
	if err != nil || !strings.Contains(result.Content, "已写入") {
		t.Fatalf("已授权外部写入失败：%q %v", result.Content, err)
	}
	requireApprovals(t, approver, 3)

	commands, paths := gate.Session().Len()
	if commands != 0 || paths != 3 {
		t.Fatalf("会话授权数量异常：commands=%d paths=%d", commands, paths)
	}
	if cleared := gate.ClearSession(); cleared != 3 {
		t.Fatalf("清除数量异常：%d", cleared)
	}

	// 清除后同样的操作重新询问。
	if _, err := executeDefault(t, permissioned, "read", map[string]any{"path": readFile}); err != nil {
		t.Fatal(err)
	}
	requireApprovals(t, approver, 4)
	if _, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "cat " + catFile}); err != nil {
		t.Fatal(err)
	}
	requireApprovals(t, approver, 5)
	if _, err := executeDefault(t, permissioned, "write", map[string]any{"path": writeFile, "content": "after-clear"}); err != nil {
		t.Fatal(err)
	}
	requireApprovals(t, approver, 6)

	if _, err := os.Stat(rules.FilePath()); err == nil {
		t.Fatal("会话授权不应写入项目规则文件")
	}
}

func TestDefaultSessionDangerousAndMixedChainsAlwaysAsk(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("系统缺少 git")
	}
	permissioned, _, _, approver, gate := sessionDefaultTest(t)
	approver.setDecision(ApprovalDecision{Allow: true, Session: true})

	// 先给 git status 建立会话授权。非 Git 工作目录下退出码非零不影响审批。
	_, _ = executeDefault(t, permissioned, "bash", map[string]any{"command": "git status"})
	requests := requireApprovals(t, approver, 1)
	if len(requests[0].SessionCommands) != 1 || !stringSlicesEqual(requests[0].SessionCommands[0].Prefix, []string{"git", "status"}) {
		t.Fatalf("git status 应提供前缀会话键：%#v", requests[0].SessionCommands)
	}

	// 危险后缀每次都询问，且请求不带任何会话键。
	for i := 0; i < 2; i++ {
		_, _ = executeDefault(t, permissioned, "bash", map[string]any{"command": "git status --output=out.txt"})
		requests = requireApprovals(t, approver, 2+i)
		request := requests[len(requests)-1]
		if request.CanSession() || len(request.SessionCommands) != 0 || len(request.SessionPaths) != 0 || len(request.RecordPrefixes) != 0 {
			t.Fatalf("危险后缀不应提供记录或会话授权：%#v", request)
		}
	}

	// 混合链里只要有一段命中危险后缀，整条链都不提供会话授权；
	// 已有的 git status 授权也不能让整条链自动放行。
	for i := 0; i < 2; i++ {
		_, _ = executeDefault(t, permissioned, "bash", map[string]any{"command": "git status && git log --output=out.txt"})
		requests = requireApprovals(t, approver, 4+i)
		request := requests[len(requests)-1]
		if request.CanSession() || len(request.SessionCommands) != 0 || len(request.SessionPaths) != 0 {
			t.Fatalf("危险混合链不应提供会话授权：%#v", request)
		}
	}
	if commands, paths := gate.Session().Len(); commands != 1 || paths != 0 {
		t.Fatalf("危险命令不应新增授权：commands=%d paths=%d", commands, paths)
	}
}

func TestDefaultSessionRememberNever(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	runtime := testRuntime(t)
	skills := skill.Discover(runtime.WorkDir)
	memoryContext := memory.NewContext(runtime.WorkDir)
	if memoryContext.Store == nil || memoryContext.Rules == nil {
		t.Fatalf("长期记忆初始化失败：%s", memoryContext.Warning)
	}
	registry, err := NewDefaultRegistryWithMemory(runtime, skills, memoryContext.Store, memoryContext.Rules, "session-test")
	if err != nil {
		t.Fatal(err)
	}
	rules, err := NewProjectRules(runtime.WorkDir, runtime.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	permissioned, _, _, approver, gate := newSessionDefault(t, registry, runtime, rules)
	approver.setDecision(ApprovalDecision{Allow: true, Session: true})

	remember := map[string]any{
		"target": "project_memory", "type": "project", "name": "session-never",
		"description": "remember 不应建立会话授权", "body": "body",
	}
	_, _ = executeDefault(t, permissioned, "remember", remember)
	requests := requireApprovals(t, approver, 1)
	if requests[0].Tool != "remember" || requests[0].CanSession() || len(requests[0].SessionCommands) != 0 || len(requests[0].SessionPaths) != 0 {
		t.Fatalf("remember 不应提供会话授权：%#v", requests[0])
	}
	if commands, paths := gate.Session().Len(); commands != 0 || paths != 0 {
		t.Fatalf("remember 不应保存会话授权：%d %d", commands, paths)
	}

	_, _ = executeDefault(t, permissioned, "remember", remember)
	requireApprovals(t, approver, 2)
}

func TestReadOnlyIgnoresSessionGrants(t *testing.T) {
	readOnly, runtime := readOnlyTest(t)
	gate := NewApprovalGate()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("TOP-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved := mustEvalSymlinks(t, outside)
	if _, _, err := gate.Session().Store(
		[]CommandGrant{{WorkDir: runtime.WorkDir, Argv: []string{"cat", outside}}},
		[]PathRule{{Path: resolved, Kind: PathKindFile}},
	); err != nil {
		t.Fatal(err)
	}

	result, err := executeReadOnly(t, readOnly, "read", map[string]any{"path": outside})
	requireToolKind(t, err, KindPermissionDenied)
	if strings.Contains(result.Content, "TOP-SECRET") {
		t.Fatalf("只读模式不应因会话授权读取外部文件：%q", result.Content)
	}
	result, err = executeReadOnly(t, readOnly, "bash", map[string]any{"command": "cat " + outside})
	requireToolKind(t, err, KindPermissionDenied)
	if strings.Contains(result.Content, "TOP-SECRET") {
		t.Fatalf("只读模式不应因会话授权执行外部 cat：%q", result.Content)
	}
	if commands, paths := gate.Session().Len(); commands != 1 || paths != 1 {
		t.Fatalf("测试前置会话授权被意外修改：%d %d", commands, paths)
	}
}
