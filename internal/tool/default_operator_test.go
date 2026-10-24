package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultBashChainControlOperators(t *testing.T) {
	permissioned, _, _, approver := defaultTest(t)
	approver.setDecision(ApprovalDecision{Allow: true})
	result, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "false || echo fallback"})
	if err != nil || !strings.Contains(result.Content, "fallback") {
		t.Fatalf("|| 回退失败：%q %v", result.Content, err)
	}
	before := len(approver.snapshot())
	result, err = executeDefault(t, permissioned, "bash", map[string]any{"command": "true || echo skipped && echo after"})
	if err != nil || strings.Contains(result.Content, "skipped") || !strings.Contains(result.Content, "after") {
		t.Fatalf("控制运算符短路错误：%q %v", result.Content, err)
	}
	if len(approver.snapshot()) != before+1 {
		t.Fatalf("第二条命令链应产生一次审批：%#v", approver.snapshot())
	}
	result, err = executeDefault(t, permissioned, "bash", map[string]any{"command": "false; echo after-semicolon"})
	if err != nil || !strings.Contains(result.Content, "after-semicolon") {
		t.Fatalf("分号继续执行失败：%q %v", result.Content, err)
	}
}

func TestDefaultBashChainUsesRawQueryExitStatus(t *testing.T) {
	permissioned, runtime, _, approver := defaultTest(t)
	if err := os.WriteFile(filepath.Join(runtime.WorkDir, "status.txt"), []byte("present\n"), 0600); err != nil {
		t.Fatal(err)
	}
	approver.setDecision(ApprovalDecision{Allow: true})
	result, err := executeDefault(t, permissioned, "bash", map[string]any{
		"command": "grep absent status.txt && echo skipped || echo fallback",
	})
	if err != nil || strings.Contains(result.Content, "skipped") || !strings.Contains(result.Content, "fallback") {
		t.Fatalf("grep raw exit status 未驱动 ||：%q %v", result.Content, err)
	}
}

func TestDefaultBashChainOrDoesNotOfferRecording(t *testing.T) {
	permissioned, _, _, approver := defaultTest(t)
	approver.setDecision(ApprovalDecision{Allow: true, Record: true})
	_, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "make test || echo fallback"})
	if err != nil {
		// make may be unavailable or the target may fail; the approval decision is
		// still the behavior under test.
	}
	requests := approver.snapshot()
	if len(requests) != 1 {
		t.Fatalf("应产生一次审批：%#v", requests)
	}
	if len(requests[0].RecordPrefixes) != 0 || len(requests[0].RecordPaths) != 0 {
		t.Fatalf("含 || 的命令链不应提供记录：%#v", requests[0])
	}
}
