package tool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultBashChainRunsInsideWorkdirWithoutApproval(t *testing.T) {
	permissioned, runtime, _, approver := defaultTest(t)
	sub := filepath.Join(runtime.WorkDir, "sub")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	result, err := executeDefault(t, permissioned, "bash", map[string]any{
		"command": "cd sub && pwd && echo chain-ok",
	})
	if err != nil || !strings.Contains(result.Content, sub) || !strings.Contains(result.Content, "chain-ok") {
		t.Fatalf("命令链执行失败：%q %v", result.Content, err)
	}
	if requests := approver.snapshot(); len(requests) != 0 {
		t.Fatalf("工作目录内安全命令链不应审批：%#v", requests)
	}
}

func TestDefaultBashChainRecordsPrefixAndRunsAgain(t *testing.T) {
	permissioned, runtime, rules, approver := defaultTest(t)
	sub := filepath.Join(runtime.WorkDir, "sub")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "Makefile"), []byte("test:\n\t@printf 'chain-make-ok\\n'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	approver.setDecision(ApprovalDecision{Allow: true, Record: true})
	result, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "cd sub && make test"})
	if err != nil || !strings.Contains(result.Content, "chain-make-ok") {
		t.Fatalf("首次命令链执行失败：%q %v", result.Content, err)
	}
	requests := approver.snapshot()
	if len(requests) != 1 || len(requests[0].RecordPrefixes) != 1 || !stringSlicesEqual(requests[0].RecordPrefixes[0], []string{"make", "test"}) {
		t.Fatalf("命令链记录候选异常：%#v", requests)
	}
	if !rules.ContainsPrefix([]string{"make", "test"}) {
		t.Fatal("命令前缀未记录")
	}
	before := len(requests)
	result, err = executeDefault(t, permissioned, "bash", map[string]any{"command": "cd sub && make test"})
	if err != nil || !strings.Contains(result.Content, "chain-make-ok") {
		t.Fatalf("重复命令链执行失败：%q %v", result.Content, err)
	}
	if len(approver.snapshot()) != before {
		t.Fatal("已记录命令链不应再次审批")
	}
}

func TestDefaultBashChainOutsideCdRecordsDirectory(t *testing.T) {
	permissioned, _, rules, approver := defaultTest(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	approver.setDecision(ApprovalDecision{Allow: true, Record: true})
	result, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "cd " + outside + " && pwd"})
	if err != nil || !strings.Contains(result.Content, outside) {
		t.Fatalf("范围外 cd 命令链执行失败：%q %v", result.Content, err)
	}
	if !rules.ContainsPath(mustEvalSymlinks(t, outside)) {
		t.Fatal("范围外 cd 目录未记录")
	}
}

func TestDefaultBashChainDeniesBeforeStarting(t *testing.T) {
	permissioned, runtime, _, approver := defaultTest(t)
	sub := filepath.Join(runtime.WorkDir, "sub")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "Makefile"), []byte("test:\n\t@touch should-not-run.txt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "cd sub && make test"})
	requireToolKind(t, err, KindPermissionDenied)
	if _, statErr := os.Stat(filepath.Join(sub, "should-not-run.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("拒绝命令链后不应执行 Makefile：%v", statErr)
	}
	if len(approver.snapshot()) != 1 {
		t.Fatal("命令链应只产生一次审批")
	}
}

func TestDefaultBashChainLaterDangerousSuffixCannotRecord(t *testing.T) {
	permissioned, _, _, approver := defaultTest(t)
	_, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "git status && git log --output=history.txt"})
	requireToolKind(t, err, KindPermissionDenied)
	requests := approver.snapshot()
	if len(requests) != 1 || len(requests[0].RecordPrefixes) != 0 || len(requests[0].RecordPaths) != 0 {
		t.Fatalf("后续危险分段不应提供记录：%#v", requests)
	}
}

func TestDefaultBashSafePipelineRunsWithoutApproval(t *testing.T) {
	permissioned, runtime, _, approver := defaultTest(t)
	if err := os.WriteFile(filepath.Join(runtime.WorkDir, "file.txt"), []byte("hello\nworld\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "echo chain | head -n 1"})
	if err != nil || !strings.Contains(result.Content, "chain") {
		t.Fatalf("安全管道执行失败：%q %v", result.Content, err)
	}
	if requests := approver.snapshot(); len(requests) != 0 {
		t.Fatalf("固定查询管道不应审批：%#v", requests)
	}
	result, err = executeDefault(t, permissioned, "bash", map[string]any{"command": "cat file.txt | grep -n world | wc -l"})
	if err != nil || !strings.Contains(result.Content, "1") {
		t.Fatalf("多段固定查询管道失败：%q %v", result.Content, err)
	}
}

func TestDefaultBashUnknownShellSyntaxUsesSingleShellApproval(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("系统缺少 bash")
	}
	permissioned, runtime, _, approver := defaultTest(t)
	approver.setDecision(ApprovalDecision{Allow: true, Record: true})
	result, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "echo $(pwd)"})
	if err != nil || !strings.Contains(result.Content, runtime.WorkDir) {
		t.Fatalf("完整 shell 回退执行失败：%q %v", result.Content, err)
	}
	requests := approver.snapshot()
	if len(requests) != 1 {
		t.Fatalf("应只产生一次审批：%#v", requests)
	}
	if len(requests[0].RecordPrefixes) != 0 || len(requests[0].RecordPaths) != 0 ||
		len(requests[0].SessionCommands) != 0 || len(requests[0].SessionPaths) != 0 {
		t.Fatalf("完整 shell 审批不应提供记录或会话授权：%#v", requests[0])
	}
	if !strings.Contains(requests[0].Reason, "命令替换") {
		t.Fatalf("审批原因应说明具体语法：%q", requests[0].Reason)
	}
}

func TestDefaultBashUnknownProgramRunsDirectAfterApproval(t *testing.T) {
	permissioned, _, _, approver := defaultTest(t)
	approver.setDecision(ApprovalDecision{Allow: true})
	_, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "definitely-not-a-real-command"})
	requireToolKind(t, err, KindExecution)
	requests := approver.snapshot()
	if len(requests) != 1 || len(requests[0].RecordPrefixes) != 0 {
		t.Fatalf("未知程序审批请求异常：%#v", requests)
	}
	if !strings.Contains(err.Error(), "命令不可用") {
		t.Fatalf("未知程序应直接查找 argv 而不是回退 shell：%v", err)
	}
}

func TestDefaultBashRecordedPrefixProducerInPipeline(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("系统缺少 make")
	}
	permissioned, runtime, rules, approver := defaultTest(t)
	makefile := "test:\n\t@printf 'pipeline-make-ok\\n'\n"
	if err := os.WriteFile(filepath.Join(runtime.WorkDir, "Makefile"), []byte(makefile), 0600); err != nil {
		t.Fatal(err)
	}
	approver.setDecision(ApprovalDecision{Allow: true, Record: true})
	result, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "make test | tail -20"})
	if err != nil || !strings.Contains(result.Content, "pipeline-make-ok") {
		t.Fatalf("管道中首次执行 make 失败：%q %v", result.Content, err)
	}
	requests := approver.snapshot()
	if len(requests) != 1 || len(requests[0].RecordPrefixes) != 1 || !stringSlicesEqual(requests[0].RecordPrefixes[0], []string{"make", "test"}) {
		t.Fatalf("管道生产者前缀未提供记录：%#v", requests)
	}
	if !rules.ContainsPrefix([]string{"make", "test"}) {
		t.Fatal("管道生产者前缀未写入")
	}
	before := len(approver.snapshot())
	result, err = executeDefault(t, permissioned, "bash", map[string]any{"command": "make test | tail -20"})
	if err != nil || !strings.Contains(result.Content, "pipeline-make-ok") {
		t.Fatalf("已记录生产者管道执行失败：%q %v", result.Content, err)
	}
	if len(approver.snapshot()) != before {
		t.Fatal("已记录管道不应再次审批")
	}
}

func TestDefaultBashDangerousPipelineSegmentCannotRecord(t *testing.T) {
	permissioned, _, _, approver := defaultTest(t)
	_, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "git status | git log --output=history.txt"})
	requireToolKind(t, err, KindPermissionDenied)
	requests := approver.snapshot()
	if len(requests) != 1 {
		t.Fatalf("应只产生一次审批：%#v", requests)
	}
	if len(requests[0].RecordPrefixes) != 0 || len(requests[0].RecordPaths) != 0 {
		t.Fatalf("危险管道分段不应提供记录：%#v", requests[0])
	}
	if !strings.Contains(requests[0].Reason, "危险后缀") {
		t.Fatalf("审批原因未说明危险后缀：%q", requests[0].Reason)
	}
}

func TestDefaultBashPipelineDenialHasNoSideEffects(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("系统缺少 make")
	}
	permissioned, runtime, _, approver := defaultTest(t)
	if err := os.WriteFile(filepath.Join(runtime.WorkDir, "Makefile"), []byte("test:\n\t@touch pipeline-should-not-run.txt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "make test | cat"})
	requireToolKind(t, err, KindPermissionDenied)
	if _, statErr := os.Stat(filepath.Join(runtime.WorkDir, "pipeline-should-not-run.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("预检拒绝后仍执行了管道生产者：%v", statErr)
	}
	if len(approver.snapshot()) != 1 {
		t.Fatal("管道应只产生一次审批")
	}
}

func TestDefaultBashFixedQueryRedirectsRunWithoutApproval(t *testing.T) {
	permissioned, runtime, _, approver := defaultTest(t)
	if err := os.WriteFile(filepath.Join(runtime.WorkDir, "file.txt"), []byte("one\ntwo\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "ls >/dev/null"})
	if err != nil || result.Content != "" {
		t.Fatalf(">/dev/null 应丢弃输出：%q %v", result.Content, err)
	}
	result, err = executeDefault(t, permissioned, "bash", map[string]any{"command": "cat file.txt 2>/dev/null | wc -l"})
	if err != nil || strings.TrimSpace(result.Content) != "2" {
		t.Fatalf("2>/dev/null 管道失败：%q %v", result.Content, err)
	}
	if requests := approver.snapshot(); len(requests) != 0 {
		t.Fatalf("固定查询重定向不应审批：%#v", requests)
	}
}

func TestDefaultBashPipePrecedenceAndSimultaneousFlows(t *testing.T) {
	permissioned, runtime, _, approver := defaultTest(t)
	if err := os.WriteFile(filepath.Join(runtime.WorkDir, "status.txt"), []byte("present\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := executeDefault(t, permissioned, "bash", map[string]any{
		"command": "grep absent status.txt || echo fallback | grep fallback",
	})
	if err != nil || !strings.Contains(result.Content, "fallback") {
		t.Fatalf("管道优先级执行错误：%q %v", result.Content, err)
	}
	result, err = executeDefault(t, permissioned, "bash", map[string]any{
		"command": "cat status.txt | grep present; echo after-semicolon",
	})
	if err != nil || !strings.Contains(result.Content, "present") || !strings.Contains(result.Content, "after-semicolon") {
		t.Fatalf("管道与 ; 组合执行错误：%q %v", result.Content, err)
	}
	if requests := approver.snapshot(); len(requests) != 0 {
		t.Fatalf("固定查询链不应审批：%#v", requests)
	}
}

func TestDefaultBashGlobIsRejectedWithoutApproval(t *testing.T) {
	permissioned, _, _, approver := defaultTest(t)
	_, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "ls *.go"})
	requireToolKind(t, err, KindPermissionDenied)
	if requests := approver.snapshot(); len(requests) != 0 {
		t.Fatalf("通配符不应发起审批：%#v", requests)
	}
	for _, want := range []string{"通配符", "find -name", "不展开", "--skip"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("通配符拒绝缺少 %q：%v", want, err)
		}
	}
}

func TestDefaultBashMissingPathRunsWithoutApproval(t *testing.T) {
	permissioned, _, _, approver := defaultTest(t)
	result, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "cat missing.txt"})
	if err == nil || !strings.Contains(result.Content, "missing.txt") {
		t.Fatalf("不存在的路径应由程序报告：%q %v", result.Content, err)
	}
	requireToolKind(t, err, KindExecution)
	if requests := approver.snapshot(); len(requests) != 0 {
		t.Fatalf("工作目录内的不存在路径不应审批：%#v", requests)
	}
}

func TestDefaultBashRedirectOrderWithFixedQueries(t *testing.T) {
	permissioned, runtime, _, approver := defaultTest(t)
	if err := os.WriteFile(filepath.Join(runtime.WorkDir, "file.txt"), []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// grep 对目录写一行 stderr，退出码 2；作为上游被忽略。
	result, err := executeDefault(t, permissioned, "bash", map[string]any{
		"command": "grep hello . 2>&1 >/dev/null | wc -l",
	})
	if err != nil || strings.TrimSpace(result.Content) != "1" {
		t.Fatalf("2>&1 先复制再丢弃 stdout 应把 stderr 送入管道：%q %v", result.Content, err)
	}
	result, err = executeDefault(t, permissioned, "bash", map[string]any{
		"command": "grep hello . >/dev/null 2>&1 | wc -l",
	})
	if err != nil || strings.TrimSpace(result.Content) != "0" {
		t.Fatalf("先丢弃 stdout 再 2>&1 应同时丢弃 stderr：%q %v", result.Content, err)
	}
	if requests := approver.snapshot(); len(requests) != 0 {
		t.Fatalf("固定查询重定向不应审批：%#v", requests)
	}
}
