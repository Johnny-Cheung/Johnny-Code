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

func scopeTest(t *testing.T) (*FileClaims, Runtime, string) {
	t.Helper()
	runtime := testRuntime(t)
	root, err := filepath.EvalSymlinks(runtime.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	return NewFileClaims(), runtime, root
}

func guardedTools(t *testing.T, claims *FileClaims, runtime Runtime, owner string) *ScopeGuard {
	t.Helper()
	registry, err := NewDefaultRegistry(runtime)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := NewScopeGuard(registry, claims, owner, runtime.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	return guard
}

func writeCall(path, content string) model.ToolCall {
	raw, _ := json.Marshal(map[string]string{"path": path, "content": content})
	return model.ToolCall{ID: "w", Name: "write", Arguments: raw}
}

func TestFileClaimsRejectOverlapAndReleaseFreesPaths(t *testing.T) {
	claims, _, root := scopeTest(t)
	if err := claims.Claim("aaaa", "后端", []string{filepath.Join(root, "pkg")}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "pkg"), filepath.Join(root, "pkg", "x.go"), root} {
		err := claims.Claim("bbbb", "前端", []string{path})
		if err == nil || !strings.Contains(err.Error(), "aaaa") || !strings.Contains(err.Error(), "后端") {
			t.Fatalf("重叠范围应说明冲突对象（%s）：%v", path, err)
		}
	}
	if err := claims.Claim("bbbb", "前端", []string{filepath.Join(root, "web")}); err != nil {
		t.Fatalf("不重叠应成功：%v", err)
	}
	if err := claims.Claim("aaaa", "后端", []string{filepath.Join(root, "pkg"), filepath.Join(root, "docs")}); err != nil {
		t.Fatalf("同一持有者重新登记不应与自己冲突：%v", err)
	}
	if owners := claims.Owners(); strings.Join(owners, ",") != "aaaa,bbbb" {
		t.Fatalf("持有者错误：%v", owners)
	}
	claims.Release("aaaa")
	claims.Release("aaaa")
	if err := claims.Claim("cccc", "新任务", []string{filepath.Join(root, "pkg")}); err != nil {
		t.Fatalf("释放后应可重新登记：%v", err)
	}
	if err := claims.Claim("", "x", []string{root}); err == nil {
		t.Fatal("缺少持有者应失败")
	}
}

func TestScopeGuardBlocksMainAgentOnClaimedPaths(t *testing.T) {
	claims, runtime, root := scopeTest(t)
	if err := claims.Claim("ab12", "重构 pkg", []string{filepath.Join(root, "pkg")}); err != nil {
		t.Fatal(err)
	}
	main := guardedTools(t, claims, runtime, "")
	for _, path := range []string{"pkg/a.go", filepath.Join(root, "pkg", "deep", "b.go")} {
		result, err := main.Execute(context.Background(), writeCall(path, "x"))
		var toolErr *Error
		if !errors.As(err, &toolErr) || toolErr.Kind != KindPermissionDenied || !result.IsError || !strings.Contains(result.Content, "ab12") || !strings.Contains(result.Content, "重构 pkg") {
			t.Fatalf("主 Agent 写被占用路径应被拒绝并说明占用者（%s）：%+v %v", path, result, err)
		}
	}
	if _, err := main.Execute(context.Background(), writeCall("other.txt", "ok")); err != nil {
		t.Fatalf("范围外写入应放行：%v", err)
	}
	edit := model.ToolCall{ID: "e", Name: "edit", Arguments: json.RawMessage(`{"path":"pkg/a.go","old_string":"a","new_string":"b"}`)}
	if _, err := main.Execute(context.Background(), edit); err == nil {
		t.Fatal("edit 同样应被范围拦截")
	}
	claims.Release("ab12")
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := main.Execute(context.Background(), writeCall("pkg/a.go", "x")); err != nil {
		t.Fatalf("子任务结束后应释放：%v", err)
	}
}

func TestScopeGuardLimitsChildToOwnPaths(t *testing.T) {
	claims, runtime, root := scopeTest(t)
	if err := claims.Claim("ab12", "写文档", []string{filepath.Join(root, "docs")}); err != nil {
		t.Fatal(err)
	}
	if err := claims.Claim("cd34", "写代码", []string{filepath.Join(root, "src")}); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"docs", "src"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	child := guardedTools(t, claims, runtime, "ab12")
	if _, err := child.Execute(context.Background(), writeCall("docs/a.md", "ok")); err != nil {
		t.Fatalf("自己的范围应放行：%v", err)
	}
	for _, path := range []string{"src/a.go", "elsewhere.txt", filepath.Join(t.TempDir(), "outside.txt"), "docs/../src/b.go"} {
		if _, err := child.Execute(context.Background(), writeCall(path, "x")); err == nil || !strings.Contains(err.Error(), "只能修改声明的范围") {
			t.Fatalf("范围外写入应被拒绝（%s）：%v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "src", "a.go")); err == nil {
		t.Fatal("被拒绝的写入不应创建文件")
	}
	reader := model.ToolCall{ID: "r", Name: "read", Arguments: json.RawMessage(`{"path":"docs/a.md"}`)}
	if _, err := child.Execute(context.Background(), reader); err != nil {
		t.Fatalf("read 不受范围限制：%v", err)
	}
	released := guardedTools(t, claims, runtime, "gone")
	if _, err := released.Execute(context.Background(), writeCall("docs/b.md", "x")); err == nil || !strings.Contains(err.Error(), "没有声明写入范围") {
		t.Fatalf("没有范围的持有者不能写：%v", err)
	}
}

func TestScopeGuardFollowsSymlinksOutOfClaim(t *testing.T) {
	claims, runtime, root := scopeTest(t)
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "pkg"), filepath.Join(root, "link")); err != nil {
		t.Skip("无法创建符号链接")
	}
	if err := claims.Claim("ab12", "重构", []string{filepath.Join(root, "pkg")}); err != nil {
		t.Fatal(err)
	}
	main := guardedTools(t, claims, runtime, "")
	if _, err := main.Execute(context.Background(), writeCall("link/a.go", "x")); err == nil {
		t.Fatal("通过符号链接写被占用目录也应被拒绝")
	}
}

func TestScopeGuardRequiresInputs(t *testing.T) {
	claims, runtime, _ := scopeTest(t)
	if _, err := NewScopeGuard(nil, claims, "", runtime.WorkDir); err == nil {
		t.Fatal("缺少工具集合应失败")
	}
	registry, _ := NewDefaultRegistry(runtime)
	if _, err := NewScopeGuard(registry, nil, "", runtime.WorkDir); err == nil {
		t.Fatal("缺少登记簿应失败")
	}
	if _, err := NewScopeGuard(registry, claims, "", ""); err == nil {
		t.Fatal("缺少工作目录应失败")
	}
}

func TestApprovalGateReportsSubagentWaiting(t *testing.T) {
	gate := NewApprovalGate()
	var mu sync.Mutex
	var events []string
	gate.SetWaitObserver(func(origin Origin, waiting bool) {
		mu.Lock()
		defer mu.Unlock()
		state := "end"
		if waiting {
			state = "begin"
		}
		events = append(events, origin.ID+":"+state)
	})
	gate.Set(func(_ context.Context, request ApprovalRequest) (ApprovalDecision, error) {
		mu.Lock()
		defer mu.Unlock()
		if !request.Origin.IsZero() && (len(events) != 1 || events[0] != "ab12:begin") {
			t.Errorf("审批函数运行时应已处于等待状态：%v", events)
		}
		return ApprovalDecision{Allow: true}, nil
	})
	if _, err := gate.Ask(context.Background(), ApprovalRequest{Origin: Origin{ID: "ab12"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Ask(context.Background(), ApprovalRequest{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(events, ",") != "ab12:begin,ab12:end" {
		t.Fatalf("只有子任务的审批才应通知：%v", events)
	}
}
