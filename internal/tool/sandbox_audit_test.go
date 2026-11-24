package tool

import (
	"context"
	"os/exec"
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
