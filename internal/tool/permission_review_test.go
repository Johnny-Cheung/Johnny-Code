package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingCommandStillAllowsFallbackAndContinue(t *testing.T) {
	for _, operator := range []string{"||", ";"} {
		t.Run(operator, func(t *testing.T) {
			runtime := testRuntime(t)
			result, err := runControlled(t, runtime,
				controlledPipeline{segments: []controlledSegment{{program: "johnny-unavailable-test-program"}}},
				controlledPipeline{operator: operator, segments: []controlledSegment{controlledProgram(t, "echo", []string{"after"})}},
			)
			if err != nil || result.IsError || !strings.Contains(result.Content, "after") {
				t.Fatalf("缺少程序后 %s 仍应继续：%+v %v", operator, result, err)
			}
		})
	}
}

func TestSessionGrantCannotAuthorizeChangingInterpreterInput(t *testing.T) {
	skipWithoutCommand(t, "sh")
	permissioned, runtime, _, approver := defaultTest(t)
	approver.setDecision(ApprovalDecision{Allow: true, Session: true})
	// 单独执行时 stdin 为空，这项精确 argv 授权不能转用到带上游输入的解释器。
	if _, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "sh -"}); err != nil {
		t.Fatal(err)
	}
	approver.setDecision(ApprovalDecision{Allow: false})
	_, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "echo 'touch pipe-pwned.txt' | sh -"})
	requireToolKind(t, err, KindPermissionDenied)
	if _, err := os.Stat(filepath.Join(runtime.WorkDir, "pipe-pwned.txt")); !os.IsNotExist(err) {
		t.Fatalf("已有 argv 授权不能批准新的管道输入：%v", err)
	}
	requests := approver.snapshot()
	if len(requests) != 2 || requests[1].CanSession() {
		t.Fatalf("非查询管道消费者必须重新审批且不能会话记住：%+v", requests)
	}
}

func TestUnknownPipeConsumerCannotCreateSessionGrant(t *testing.T) {
	skipWithoutCommand(t, "sh")
	permissioned, _, _, approver := defaultTest(t)
	approver.setDecision(ApprovalDecision{Allow: true, Session: true})
	for _, script := range []string{"echo benign", "echo another"} {
		if _, err := executeDefault(t, permissioned, "bash", map[string]any{"command": "echo '" + script + "' | sh -"}); err != nil {
			t.Fatal(err)
		}
	}
	requests := approver.snapshot()
	if len(requests) != 2 || requests[0].CanSession() || requests[1].CanSession() {
		t.Fatalf("管道脚本输入变化仍需逐次审批：%+v", requests)
	}
}

func TestDefaultKnownQueryApprovalIncludesValidationReason(t *testing.T) {
	permissioned, _, _, approver := defaultTest(t)
	_, _ = executeDefault(t, permissioned, "bash", map[string]any{"command": "grep -R foo ."})
	requests := approver.snapshot()
	if len(requests) != 1 || !strings.Contains(requests[0].Reason, "不支持选项") || !strings.Contains(requests[0].Reason, "-R") {
		t.Fatalf("已知查询形式未通过时应说明具体原因：%+v", requests)
	}
}
