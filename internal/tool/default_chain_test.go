package tool

import (
	"os"
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

func TestDefaultBashChainUnsupportedPipeUsesLegacyApproval(t *testing.T) {
	permissioned, _, _, approver := defaultTest(t)
	_, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "echo chain | head"})
	requireToolKind(t, err, KindPermissionDenied)
	requests := approver.snapshot()
	if len(requests) != 1 || len(requests[0].RecordPrefixes) != 0 {
		t.Fatalf("管道命令应沿用不可记录审批：%#v", requests)
	}
	if !strings.Contains(requests[0].Reason, "shell 语法") {
		t.Fatalf("管道审批原因异常：%q", requests[0].Reason)
	}
}
