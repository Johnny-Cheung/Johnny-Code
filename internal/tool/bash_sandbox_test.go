//go:build darwin

package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/sandbox"
)

func TestBashRunsInsideSeatbelt(t *testing.T) {
	if err := sandbox.Available(); err != nil {
		t.Skipf("sandbox-exec 不可用：%v", err)
	}
	work := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.txt")
	runner, err := sandbox.NewSeatbelt(sandbox.Options{WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()

	runtime := Runtime{WorkDir: work, Sandbox: runner}
	runtime.Env = append(runner.Environment(nil), "OUTSIDE="+outsideFile)
	bash := NewBash(runtime)

	allowed, err := json.Marshal(map[string]string{"command": "echo ok > inside.txt"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := bash.Execute(context.Background(), allowed)
	if err != nil || result.IsError {
		t.Fatalf("工作区内写入应成功：%v %+v", err, result)
	}
	data, err := os.ReadFile(filepath.Join(work, "inside.txt"))
	if err != nil || strings.TrimSpace(string(data)) != "ok" {
		t.Fatalf("工作区文件不正确：%q %v", data, err)
	}

	denied, err := json.Marshal(map[string]string{"command": `echo no > "$OUTSIDE"`})
	if err != nil {
		t.Fatal(err)
	}
	result, err = bash.Execute(context.Background(), denied)
	if err == nil && !result.IsError {
		t.Fatalf("工作区外写入应被拒绝：%+v", result)
	}
	if !strings.Contains(result.Content, "[沙箱]") {
		t.Fatalf("沙箱拒绝应带明确提示：%q", result.Content)
	}
	if _, statErr := os.Stat(outsideFile); !os.IsNotExist(statErr) {
		t.Fatalf("沙箱外文件不应存在：%v", statErr)
	}
}

func TestControlledChainRunsInsideSeatbelt(t *testing.T) {
	if err := sandbox.Available(); err != nil {
		t.Skipf("sandbox-exec 不可用：%v", err)
	}
	work := t.TempDir()
	runner, err := sandbox.NewSeatbelt(sandbox.Options{WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()

	runtime, err := NewRuntime(work)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Sandbox = runner
	runtime.Env = runner.Environment(nil)
	chain := controlledChain{pipelines: []controlledPipeline{{
		segments: []controlledSegment{{program: "pwd", dir: work, env: runtime.Env}},
	}}}
	result, err := executeControlledChain(context.Background(), runtime, chain, time.Minute)
	if err != nil || result.IsError {
		t.Fatalf("受控命令应成功：%v %+v", err, result)
	}
	if !strings.Contains(result.Content, work) {
		t.Fatalf("pwd 应输出工作目录：%q", result.Content)
	}
}

func TestBashSandboxCancellationKillsProcessGroup(t *testing.T) {
	if err := sandbox.Available(); err != nil {
		t.Skipf("sandbox-exec 不可用：%v", err)
	}
	work := t.TempDir()
	runner, err := sandbox.NewSeatbelt(sandbox.Options{WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	runtime, err := NewRuntime(work)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Sandbox = runner
	runtime.Env = runner.Environment(nil)
	bash := NewBash(runtime)

	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	time.AfterFunc(150*time.Millisecond, cancel)
	raw, _ := json.Marshal(map[string]string{"command": "sleep 30"})
	result, err := bash.Execute(ctx, raw)
	elapsed := time.Since(start)
	if err == nil && !result.IsError {
		t.Fatalf("取消后应返回错误：%+v", result)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("取消后进程组未及时退出：%s", elapsed)
	}
}
