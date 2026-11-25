package tool

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/sandbox"
)

type fakeSandbox struct {
	info sandbox.Info
}

func (f fakeSandbox) CommandContext(ctx context.Context, program string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, program, args...)
}

func (f fakeSandbox) Environment(base []string) []string { return base }
func (f fakeSandbox) Info() sandbox.Info                 { return f.info }
func (f fakeSandbox) Describe() string                   { return "fake" }
func (f fakeSandbox) Close() error                       { return nil }

func TestApplySandboxInfo(t *testing.T) {
	runner := fakeSandbox{info: sandbox.Info{Name: "seatbelt", Network: true}}
	runtime := Runtime{Sandbox: runner}
	request := ApprovalRequest{Tool: "bash"}
	applySandboxInfo(runtime, &request)
	if request.Sandbox != "seatbelt" || request.SandboxNetwork != "on" {
		t.Fatalf("bash 审计字段不正确：%+v", request)
	}

	request = ApprovalRequest{Tool: "write"}
	applySandboxInfo(runtime, &request)
	if request.Sandbox != "" || request.SandboxNetwork != "" {
		t.Fatalf("文件工具不应记录沙箱字段：%+v", request)
	}

	runtime.Sandbox = nil
	request = ApprovalRequest{Tool: "bash"}
	applySandboxInfo(runtime, &request)
	if request.Sandbox != "off" {
		t.Fatalf("关闭沙箱时应记录 off：%+v", request)
	}
}

func TestWithSandboxHint(t *testing.T) {
	runtime := Runtime{Sandbox: fakeSandbox{info: sandbox.Info{Name: "seatbelt", Network: true}}}
	result := withSandboxHint(runtime, Result{Content: "mkdir: /HNU: Operation not permitted", IsError: true})
	if !strings.Contains(result.Content, "[沙箱] Seatbelt") {
		t.Fatalf("EPERM 应追加沙箱提示：%s", result.Content)
	}

	offline := Runtime{Sandbox: fakeSandbox{info: sandbox.Info{Name: "seatbelt", Network: false}}}
	result = withSandboxHint(offline, Result{Content: "curl: (6) Could not resolve host: example.com", IsError: true})
	if !strings.Contains(result.Content, "网络已关闭") {
		t.Fatalf("断网错误应追加网络提示：%s", result.Content)
	}

	result = withSandboxHint(runtime, Result{Content: "curl: (6) Could not resolve host: example.com", IsError: true})
	if strings.Contains(result.Content, "[沙箱]") {
		t.Fatalf("网络开启时不应追加断网提示：%s", result.Content)
	}

	result = withSandboxHint(runtime, Result{Content: "mkdir: /HNU: Operation not permitted", IsError: false})
	if strings.Contains(result.Content, "[沙箱]") {
		t.Fatalf("成功结果不应追加提示：%s", result.Content)
	}

	result = withSandboxHint(Runtime{}, Result{Content: "mkdir: /HNU: Operation not permitted", IsError: true})
	if strings.Contains(result.Content, "[沙箱]") {
		t.Fatalf("无沙箱时不应追加提示：%s", result.Content)
	}
}
