package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

type fakeApprover struct {
	mu       sync.Mutex
	requests []ApprovalRequest
	decision ApprovalDecision
	err      error
}

func (f *fakeApprover) set(requests []ApprovalRequest, decision ApprovalDecision, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = requests
	f.decision = decision
	f.err = err
}

func (f *fakeApprover) snapshot() []ApprovalRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ApprovalRequest(nil), f.requests...)
}

func (f *fakeApprover) setDecision(decision ApprovalDecision) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.decision = decision
}

func (f *fakeApprover) setRequests(requests []ApprovalRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = requests
}

func defaultTest(t *testing.T) (*Default, Runtime, *ProjectRules, *fakeApprover) {
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
	return permissioned, runtime, rules, approver
}

func executeDefault(t *testing.T, permissioned *Default, name string, arguments any) (Result, error) {
	t.Helper()
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	return permissioned.Execute(context.Background(), model.ToolCall{ID: "call-" + name, Name: name, Arguments: raw})
}

func TestDefaultFileToolsInsideWorkdirAuto(t *testing.T) {
	permissioned, runtime, _, approver := defaultTest(t)
	if _, err := executeDefault(t, permissioned, "write", map[string]any{"path": "inside.txt", "content": "hello"}); err != nil {
		t.Fatal(err)
	}
	result, err := executeDefault(t, permissioned, "read", map[string]any{"path": "inside.txt"})
	if err != nil || !strings.Contains(result.Content, "hello") {
		t.Fatalf("工作目录内 read 失败：%q %v", result.Content, err)
	}
	if _, err := executeDefault(t, permissioned, "edit", map[string]any{"path": "inside.txt", "old_string": "hello", "new_string": "world"}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(runtime.WorkDir, "inside.txt"))
	if err != nil || string(content) != "world" {
		t.Fatalf("默认模式工作目录内编辑失败：%q %v", content, err)
	}
	if requests := approver.snapshot(); len(requests) != 0 {
		t.Fatalf("工作目录内操作不应审批：%#v", requests)
	}
}

func TestDefaultOutsidePathDeniesThenRecords(t *testing.T) {
	permissioned, _, rules, approver := defaultTest(t)
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	resolved := mustEvalSymlinks(t, outsideFile)

	result, err := executeDefault(t, permissioned, "read", map[string]any{"path": outsideFile})
	requireToolKind(t, err, KindPermissionDenied)
	if !result.IsError {
		t.Fatal("拒绝结果应标记错误")
	}
	requests := approver.snapshot()
	if len(requests) != 1 || len(requests[0].RecordPaths) != 1 || requests[0].RecordPaths[0].Path != resolved {
		t.Fatalf("路径审批请求异常：%#v", requests)
	}

	approver.setDecision(ApprovalDecision{Allow: true, Record: true})
	result, err = executeDefault(t, permissioned, "read", map[string]any{"path": outsideFile})
	if err != nil || !strings.Contains(result.Content, "outside") {
		t.Fatalf("批准并记录后 read 失败：%q %v", result.Content, err)
	}
	if !rules.ContainsPath(resolved) {
		t.Fatal("路径规则未写入")
	}
	if _, err := os.Stat(filepath.Join(rules.ProjectRoot(), ".johnny", "permissions.local.json")); err != nil {
		t.Fatalf("项目规则文件未保存：%v", err)
	}
	before := len(approver.snapshot())
	result, err = executeDefault(t, permissioned, "read", map[string]any{"path": outsideFile})
	if err != nil || !strings.Contains(result.Content, "outside") {
		t.Fatalf("已记录路径应自动读取：%q %v", result.Content, err)
	}
	if len(approver.snapshot()) != before {
		t.Fatal("已记录路径不应再次审批")
	}
}

func TestDefaultOutsideWriteRecord(t *testing.T) {
	permissioned, _, rules, approver := defaultTest(t)
	outsideDir := t.TempDir()
	target := filepath.Join(outsideDir, "new.txt")
	approver.setDecision(ApprovalDecision{Allow: true, Record: true})
	result, err := executeDefault(t, permissioned, "write", map[string]any{"path": target, "content": "new"})
	if err != nil || !strings.Contains(result.Content, "已写入") {
		t.Fatalf("批准并记录后 write 失败：%q %v", result.Content, err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "new" {
		t.Fatalf("write 未写入文件：%q %v", content, err)
	}
	if !rules.ContainsPath(mustEvalSymlinks(t, target)) {
		t.Fatal("新文件路径规则未写入")
	}
}

func TestDefaultBashWhitelistOutsidePathRecord(t *testing.T) {
	permissioned, _, rules, approver := defaultTest(t)
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	approver.setDecision(ApprovalDecision{Allow: true, Record: true})
	result, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "cat " + outsideFile})
	if err != nil || !strings.Contains(result.Content, "outside") {
		t.Fatalf("批准并记录后 cat 失败：%q %v", result.Content, err)
	}
	if !rules.ContainsPath(mustEvalSymlinks(t, outsideFile)) {
		t.Fatalf("文件路径规则未写入：%#v", rules.PathsRules())
	}
	before := len(approver.snapshot())
	if _, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "cat " + outsideFile}); err != nil {
		t.Fatal(err)
	}
	if len(approver.snapshot()) != before {
		t.Fatal("已记录路径不应再次审批")
	}
}

func TestDefaultBashProjectPrefixRecord(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("系统缺少 make")
	}
	permissioned, runtime, rules, approver := defaultTest(t)
	makefile := "test:\n\t@printf 'make-ok\\n'\n"
	if err := os.WriteFile(filepath.Join(runtime.WorkDir, "Makefile"), []byte(makefile), 0600); err != nil {
		t.Fatal(err)
	}
	approver.setDecision(ApprovalDecision{Allow: true, Record: true})
	result, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "make test"})
	if err != nil || !strings.Contains(result.Content, "make-ok") {
		t.Fatalf("批准并记录后 make 失败：%q %v", result.Content, err)
	}
	if !rules.ContainsPrefix([]string{"make", "test"}) {
		t.Fatal("项目命令前缀未写入")
	}
	before := len(approver.snapshot())
	result, err = executeDefault(t, permissioned, "bash", map[string]any{"command": "make test"})
	if err != nil || !strings.Contains(result.Content, "make-ok") {
		t.Fatalf("已记录前缀应自动执行：%q %v", result.Content, err)
	}
	if len(approver.snapshot()) != before {
		t.Fatal("已记录前缀不应再次审批")
	}
}

func TestDefaultDangerousSuffixAndShellSyntaxDoNotRecord(t *testing.T) {
	permissioned, runtime, _, approver := defaultTest(t)
	target := filepath.Join(runtime.WorkDir, "keep.txt")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		command string
		want    string
	}{
		{"git log --output=out.txt", "危险后缀"},
		{"rm keep.txt", "命令不在"},
		{"ls; rm keep.txt", "shell 语法"},
	}
	for _, c := range cases {
		approver.setRequests(nil)
		_, err := executeDefault(t, permissioned, "bash", map[string]any{"command": c.command})
		requireToolKind(t, err, KindPermissionDenied)
		requests := approver.snapshot()
		if len(requests) != 1 {
			t.Fatalf("%q 应产生一次审批：%#v", c.command, requests)
		}
		if len(requests[0].RecordPrefix) != 0 || len(requests[0].RecordPaths) != 0 {
			t.Fatalf("%q 不应提供记录：%#v", c.command, requests[0])
		}
		if !strings.Contains(requests[0].Reason, c.want) {
			t.Fatalf("%q 原因缺少 %q：%q", c.command, c.want, requests[0].Reason)
		}
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != "keep" {
		t.Fatalf("拒绝后文件被修改：%q %v", content, err)
	}
}

func TestDefaultSymlinkOutsideNeedsApproval(t *testing.T) {
	permissioned, runtime, _, approver := defaultTest(t)
	outsideBase := t.TempDir()
	outside := filepath.Join(outsideBase, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(runtime.WorkDir, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("无法创建符号链接：%v", err)
	}
	_, err := executeDefault(t, permissioned, "read", map[string]any{"path": "link.txt"})
	requireToolKind(t, err, KindPermissionDenied)
	if requests := approver.snapshot(); len(requests) != 1 || requests[0].RecordPaths[0].Path != mustEvalSymlinks(t, outside) {
		t.Fatalf("符号链接审批请求异常：%#v", requests)
	}
}

func TestDefaultProjectPrefixRequiresPathWhitelist(t *testing.T) {
	permissioned, _, rules, approver := defaultTest(t)
	if err := rules.AddPrefix([]string{"git", "log"}); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "git log " + outside})
	requireToolKind(t, err, KindPermissionDenied)
	requests := approver.snapshot()
	if len(requests) != 1 {
		t.Fatalf("应产生一次路径审批：%#v", requests)
	}
	if len(requests[0].RecordPrefix) != 0 || len(requests[0].RecordPaths) != 1 {
		t.Fatalf("已记录前缀的命令应只补记路径：%#v", requests[0])
	}
	if !strings.Contains(requests[0].Reason, "路径不在项目路径白名单") {
		t.Fatalf("路径审批原因异常：%q", requests[0].Reason)
	}
}

func TestDefaultProjectPrefixOffersPrefixAndPaths(t *testing.T) {
	permissioned, _, _, approver := defaultTest(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "git log " + outside})
	requireToolKind(t, err, KindPermissionDenied)
	requests := approver.snapshot()
	if len(requests) != 1 {
		t.Fatalf("应产生一次审批：%#v", requests)
	}
	if !stringSlicesEqual(requests[0].RecordPrefix, []string{"git", "log"}) || len(requests[0].RecordPaths) != 1 {
		t.Fatalf("未记录命令应同时提供前缀和路径：%#v", requests[0])
	}
	if !strings.Contains(requests[0].Reason, "项目前缀") || !strings.Contains(requests[0].Reason, "路径不在") {
		t.Fatalf("审批原因未说明前缀和路径：%q", requests[0].Reason)
	}
}

func TestCommandPathCandidates(t *testing.T) {
	cases := []struct {
		tokens []string
		want   []string
	}{
		{[]string{"go", "test", "-o", "/outside", "-coverprofile=/outside2", "./..."}, []string{"/outside", "/outside2", "./..."}},
		{[]string{"npm", "run", "build", "--", "/outside"}, []string{"/outside"}},
		{[]string{"make", "test", "VAR=/outside"}, []string{"/outside"}},
		{[]string{"git", "log", "https://example.com"}, nil},
		{[]string{"git", "log", "--oneline"}, nil},
	}
	for _, c := range cases {
		got := commandPathCandidates(c.tokens)
		if !stringSlicesEqual(got, c.want) {
			t.Fatalf("%v：got %v，want %v", c.tokens, got, c.want)
		}
	}
}

func TestPrefixExecutionWarning(t *testing.T) {
	for _, prefix := range [][]string{
		{"make", "test"},
		{"go", "test"},
		{"npm", "run", "build"},
		{"cargo", "run"},
	} {
		if prefixExecutionWarning(prefix) == "" {
			t.Fatalf("脚本前缀应提示内部路径限制：%v", prefix)
		}
	}
	for _, prefix := range [][]string{{"git", "log"}, {"go", "doc"}, {"npm", "test"}} {
		if prefixExecutionWarning(prefix) != "" && prefix[0] == "git" {
			t.Fatalf("只读 Git 前缀不应有脚本警告：%v", prefix)
		}
	}
}

func TestDefaultDefinitionsPreserveTools(t *testing.T) {
	permissioned, _, _, _ := defaultTest(t)
	definitions := permissioned.Definitions()
	if len(definitions) != 4 {
		t.Fatalf("工具数量错误：%d", len(definitions))
	}
	for _, definition := range definitions {
		if !strings.Contains(definition.Description, "默认模式") {
			t.Fatalf("%s 缺少默认模式说明", definition.Name)
		}
	}
}

func TestDefaultEnvironmentFiltersSecrets(t *testing.T) {
	base := []string{
		"PATH=/bin", "HOME=/home/user", "LANG=en_US.UTF-8",
		"JOHNNY_API_KEY=secret", "ANTHROPIC_AUTH_TOKEN=secret",
		"OPENAI_API_KEY=secret", "CLAUDE_CODE_TOKEN=secret",
		"AWS_SECRET_ACCESS_KEY=secret", "GITHUB_TOKEN=secret",
		"SSH_AUTH_SOCK=/tmp/agent",
	}
	env := defaultEnvironment(base)
	joined := strings.Join(env, "\n")
	for _, secret := range []string{"JOHNNY_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY", "CLAUDE_CODE_TOKEN", "AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN"} {
		if strings.Contains(joined, secret+"=") {
			t.Fatalf("默认环境不应包含 %s：%s", secret, joined)
		}
	}
	for _, keep := range []string{"PATH=/bin", "HOME=/home/user", "LANG=en_US.UTF-8", "SSH_AUTH_SOCK=/tmp/agent"} {
		if !strings.Contains(joined, keep) {
			t.Fatalf("默认环境应保留 %s：%s", keep, joined)
		}
	}
}

func TestApprovalGateAuditOnlyRecordsActualRecords(t *testing.T) {
	permissioned, _, _, approver := defaultTest(t)
	var audits []ApprovalAudit
	permissioned.gate.SetAudit(func(audit ApprovalAudit) { audits = append(audits, audit) })
	outsideDir := t.TempDir()
	target := filepath.Join(outsideDir, "target")
	if err := os.WriteFile(target, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	approver.setDecision(ApprovalDecision{Allow: true, Record: false})
	if _, err := executeDefault(t, permissioned, "read", map[string]any{"path": target}); err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || audits[0].Decision != "allow" || len(audits[0].Prefix) != 0 || len(audits[0].Paths) != 0 {
		t.Fatalf("一次性允许审计异常：%#v", audits)
	}
	approver.setDecision(ApprovalDecision{Allow: true, Record: true})
	if _, err := executeDefault(t, permissioned, "read", map[string]any{"path": target}); err != nil {
		t.Fatal(err)
	}
	if len(audits) != 2 || audits[1].Decision != "record" || len(audits[1].Paths) != 1 {
		t.Fatalf("记录审计异常：%#v", audits)
	}
}
