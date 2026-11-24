package main

import (
	"bytes"
	"os"
	"path/filepath"
	goos "runtime"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func TestPrepareSandboxDisabled(t *testing.T) {
	t.Setenv("JOHNNY_SANDBOX", "off")
	t.Setenv("JOHNNY_SANDBOX_NETWORK", "")
	work := t.TempDir()
	runtime, err := tool.NewRuntime(work)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	enabled, err := prepareSandbox(&runtime, false, nil, &output, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if enabled || runtime.Sandbox != nil {
		t.Fatal("JOHNNY_SANDBOX=off 时不应启用沙箱")
	}
	if !strings.Contains(output.String(), "沙箱已关闭") {
		t.Fatalf("应输出关闭警告：%s", output.String())
	}
}

func TestPrepareSandboxEnablesSeatbelt(t *testing.T) {
	if goos.GOOS != "darwin" {
		t.Skip("仅在 macOS 验证 Seatbelt")
	}
	t.Setenv("JOHNNY_SANDBOX", "")
	t.Setenv("JOHNNY_SANDBOX_NETWORK", "")
	runtime, err := tool.NewRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	enabled, err := prepareSandbox(&runtime, false, nil, &output, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled || runtime.Sandbox == nil {
		t.Fatal("macOS 应启用 Seatbelt 沙箱")
	}
	defer runtime.Sandbox.Close()
	if !strings.Contains(output.String(), "Seatbelt") {
		t.Fatalf("应输出沙箱状态：%s", output.String())
	}
	if !strings.Contains(runtime.Sandbox.Describe(), "网络开启") {
		t.Fatalf("默认应开启网络：%s", runtime.Sandbox.Describe())
	}
}

func TestPrepareSandboxNetworkOff(t *testing.T) {
	if goos.GOOS != "darwin" {
		t.Skip("仅在 macOS 验证 Seatbelt")
	}
	t.Setenv("JOHNNY_SANDBOX", "")
	t.Setenv("JOHNNY_SANDBOX_NETWORK", "off")
	runtime, err := tool.NewRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	enabled, err := prepareSandbox(&runtime, false, nil, &output, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled || runtime.Sandbox == nil {
		t.Fatal("macOS 应启用 Seatbelt 沙箱")
	}
	defer runtime.Sandbox.Close()
	if !strings.Contains(runtime.Sandbox.Describe(), "网络关闭") {
		t.Fatalf("JOHNNY_SANDBOX_NETWORK=off 应关闭网络：%s", runtime.Sandbox.Describe())
	}
}

func TestPrepareSandboxNonInteractiveReportsError(t *testing.T) {
	// 该测试只在没有 Seatbelt 的平台上有意义；darwin 由真实沙箱测试覆盖。
	if _, err := os.Stat(filepath.Join("/usr", "bin", "sandbox-exec")); err == nil {
		t.Skip("当前平台有 sandbox-exec")
	}
	t.Setenv("JOHNNY_SANDBOX", "")
	t.Setenv("JOHNNY_SANDBOX_NETWORK", "")
	runtime, err := tool.NewRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if _, err := prepareSandbox(&runtime, false, nil, &output, false, true); err == nil {
		t.Fatal("没有沙箱且非交互时应返回错误")
	}
}
