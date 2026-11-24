package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

type fakeClassifier struct {
	mu     sync.Mutex
	calls  []string
	result Classification
	err    error
}

func (f *fakeClassifier) Classify(ctx context.Context, command, workDir string) (Classification, error) {
	f.mu.Lock()
	f.calls = append(f.calls, command)
	result, err := f.result, f.err
	f.mu.Unlock()
	if err != nil {
		return Classification{}, err
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return Classification{}, err
		}
	}
	return result, nil
}

func (f *fakeClassifier) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func executeJEV(t *testing.T, tools *JEV, name string, arguments any) (Result, error) {
	t.Helper()
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	return tools.Execute(context.Background(), model.ToolCall{ID: "call-" + name, Name: name, Arguments: raw})
}

func newJEVTest(t *testing.T, classification Classification, classifyErr error) (*JEV, Runtime, *fakeApprover, *fakeClassifier, *[]ApprovalAudit) {
	t.Helper()
	base, runtime, _, approver := defaultTest(t)
	classifier := &fakeClassifier{result: classification, err: classifyErr}
	tools, err := NewJEV(base, classifier, nil)
	if err != nil {
		t.Fatal(err)
	}
	audits := &[]ApprovalAudit{}
	base.gate.SetAudit(func(audit ApprovalAudit) {
		*audits = append(*audits, audit)
	})
	return tools, runtime, approver, classifier, audits
}

func TestJEVAllowExecutesWithoutApproval(t *testing.T) {
	tools, _, approver, classifier, audits := newJEVTest(t, Classification{
		Decision: ClassificationAllow, Reason: "read_only", Model: "jev-1.14.0",
		Probabilities: map[string]float64{"allow": 0.99, "ask": 0.01},
	}, nil)
	result, err := executeJEV(t, tools, "bash", map[string]any{"command": "echo JEV-OK"})
	if err != nil || !strings.Contains(result.Content, "JEV-OK") {
		t.Fatalf("result = %q, err = %v", result.Content, err)
	}
	if calls := classifier.snapshot(); len(calls) != 1 || calls[0] != "echo JEV-OK" {
		t.Fatalf("classifier calls = %#v", calls)
	}
	if requests := approver.snapshot(); len(requests) != 0 {
		t.Fatalf("允许不应进入审批：%#v", requests)
	}
	if len(*audits) != 1 {
		t.Fatalf("audits = %#v", *audits)
	}
	audit := (*audits)[0]
	if audit.Source != "jev" || audit.Classifier != "jev" || audit.ClassifiedDecision != "allow" || audit.Decision != "allow" {
		t.Fatalf("audit = %+v", audit)
	}
}

func TestJEVAllowUsesFullShell(t *testing.T) {
	tools, _, _, _, _ := newJEVTest(t, Classification{Decision: ClassificationAllow}, nil)
	result, err := executeJEV(t, tools, "bash", map[string]any{"command": "printf 'a\\nb\\n' | wc -l"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "2") {
		t.Fatalf("完整 shell 管道输出异常：%q", result.Content)
	}
}

func TestJEVAskUsesApproval(t *testing.T) {
	tools, _, approver, _, audits := newJEVTest(t, Classification{
		Decision: ClassificationAsk, Reason: "network", Model: "jev-1.14.0",
		Probabilities: map[string]float64{"allow": 0.2, "ask": 0.7, "deny": 0.1},
	}, nil)
	approver.setDecision(ApprovalDecision{Allow: true})
	result, err := executeJEV(t, tools, "bash", map[string]any{"command": "echo ASK-OK"})
	if err != nil || !strings.Contains(result.Content, "ASK-OK") {
		t.Fatalf("result = %q, err = %v", result.Content, err)
	}
	requests := approver.snapshot()
	if len(requests) != 1 {
		t.Fatalf("审批请求数 = %d", len(requests))
	}
	request := requests[0]
	if request.Classifier != "jev" || request.ClassifiedDecision != "ask" || request.ClassifiedReason != "network" {
		t.Fatalf("审批请求缺少分类信息：%+v", request)
	}
	if request.CanSession() || request.CanRecord() {
		t.Fatal("JEV 模式不应提供会话或项目记录选项")
	}
	if !strings.Contains(request.Detail, "ask") || !strings.Contains(request.Detail, "0.700") {
		t.Fatalf("审批详情缺少分类结果：%q", request.Detail)
	}
	if len(*audits) != 1 || (*audits)[0].Source != "approval" || (*audits)[0].ClassifiedDecision != "ask" {
		t.Fatalf("audits = %#v", *audits)
	}
}

func TestJEVAskDenied(t *testing.T) {
	tools, runtime, _, _, _ := newJEVTest(t, Classification{Decision: ClassificationAsk}, nil)
	result, err := executeJEV(t, tools, "bash", map[string]any{"command": "touch should-not-exist"})
	if err == nil || !strings.Contains(result.Content, "权限审批未通过") {
		t.Fatalf("result = %q, err = %v", result.Content, err)
	}
	if _, statErr := os.Stat(filepath.Join(runtime.WorkDir, "should-not-exist")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("审批拒绝后命令仍被执行")
	}
}

func TestJEVDenyDoesNotExecute(t *testing.T) {
	tools, runtime, approver, _, audits := newJEVTest(t, Classification{
		Decision: ClassificationDeny, Reason: "destructive", Model: "jev-1.14.0",
	}, nil)
	result, err := executeJEV(t, tools, "bash", map[string]any{"command": "touch denied.txt"})
	if err == nil || !strings.Contains(result.Content, "JEV 拒绝执行") {
		t.Fatalf("result = %q, err = %v", result.Content, err)
	}
	var toolErr *Error
	if !errors.As(err, &toolErr) || toolErr.Kind != KindPermissionDenied {
		t.Fatalf("err = %#v", err)
	}
	if _, statErr := os.Stat(filepath.Join(runtime.WorkDir, "denied.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("JEV 拒绝后命令仍被执行")
	}
	if len(approver.snapshot()) != 0 {
		t.Fatal("JEV 拒绝不应进入审批")
	}
	if len(*audits) != 1 || (*audits)[0].Source != "jev" || (*audits)[0].ClassifiedDecision != "deny" || (*audits)[0].Decision != "deny" {
		t.Fatalf("audits = %#v", *audits)
	}
}

func TestJEVFallbackUsesDefaultPolicy(t *testing.T) {
	base, runtime, _, approver := defaultTest(t)
	classifier := &fakeClassifier{err: errors.New("relay down")}
	tools, err := NewJEV(base, classifier, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executeJEV(t, tools, "bash", map[string]any{"command": "pwd"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, runtime.WorkDir) {
		t.Fatalf("降级后 pwd 输出异常：%q", result.Content)
	}
	if len(approver.snapshot()) != 0 {
		t.Fatal("固定查询在降级后不应进入审批")
	}
	if calls := classifier.snapshot(); len(calls) != 1 {
		t.Fatalf("classifier calls = %#v", calls)
	}
}

func TestJEVCircuitBreaker(t *testing.T) {
	base, _, _, _ := defaultTest(t)
	classifier := &fakeClassifier{err: errors.New("relay down")}
	var fallbacks int
	tools, err := NewJEV(base, classifier, func(error) { fallbacks++ })
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := executeJEV(t, tools, "bash", map[string]any{"command": "pwd"}); err != nil {
			t.Fatal(err)
		}
	}
	if calls := classifier.snapshot(); len(calls) != jevFailureLimit {
		t.Fatalf("连续失败后仍调用 JEV：%#v", calls)
	}
	if fallbacks != 4 {
		t.Fatalf("fallbacks = %d", fallbacks)
	}
}

func TestJEVCanceledContextDoesNotFallback(t *testing.T) {
	base, runtime, _, _ := defaultTest(t)
	classifier := &fakeClassifier{result: Classification{Decision: ClassificationAllow}}
	tools, err := NewJEV(base, classifier, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw, _ := json.Marshal(map[string]any{"command": "touch canceled.txt"})
	_, err = tools.Execute(ctx, model.ToolCall{ID: "call-cancel", Name: "bash", Arguments: raw})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(runtime.WorkDir, "canceled.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("取消后命令仍被执行")
	}
}

func TestJEVNonBashDelegatesToDefault(t *testing.T) {
	tools, runtime, _, classifier, _ := newJEVTest(t, Classification{Decision: ClassificationAllow}, nil)
	if _, err := executeJEV(t, tools, "write", map[string]any{"path": "inside.txt", "content": "hello"}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(runtime.WorkDir, "inside.txt"))
	if err != nil || string(content) != "hello" {
		t.Fatalf("content = %q, err = %v", content, err)
	}
	if calls := classifier.snapshot(); len(calls) != 0 {
		t.Fatalf("非 bash 工具不应调用 JEV：%#v", calls)
	}
	definitions := tools.Definitions()
	for _, definition := range definitions {
		if definition.Name == "bash" {
			if !strings.Contains(definition.Description, "JEV 模式") {
				t.Fatalf("bash 描述未更新：%q", definition.Description)
			}
			return
		}
	}
	t.Fatal("缺少 bash 定义")
}
