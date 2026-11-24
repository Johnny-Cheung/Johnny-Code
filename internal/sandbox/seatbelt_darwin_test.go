//go:build darwin

package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildProfileWriteAndNetworkRules(t *testing.T) {
	work := realPath(t.TempDir())
	session := realPath(t.TempDir())
	profile := buildProfile(work, session, false)
	for _, want := range []string{
		"(allow default)",
		"(deny file-write*)",
		"(deny network*)",
		"(subpath " + sbplQuote(realPath(work)) + ")",
		"(subpath " + sbplQuote(realPath(session)) + ")",
		"(subpath " + sbplQuote(filepath.Join(realPath(work), ".johnny")) + ")",
	} {
		if !strings.Contains(profile, want) {
			t.Fatalf("profile 缺少 %q：\n%s", want, profile)
		}
	}
	networkProfile := buildProfile(work, session, true)
	if strings.Contains(networkProfile, "(deny network*)") {
		t.Fatalf("网络开启时不应包含 deny network：\n%s", networkProfile)
	}
}

func TestSeatbeltDeniesWritesOutsideWorkspace(t *testing.T) {
	if err := Available(); err != nil {
		t.Skipf("sandbox-exec 不可用：%v", err)
	}
	work := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.txt")

	runner, err := NewSeatbelt(Options{WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()

	run := func(script string) (string, error) {
		t.Helper()
		command := runner.CommandContext(context.Background(), "/bin/sh", "-c", script)
		command.Dir = work
		command.Env = append(runner.Environment(nil), "OUTSIDE="+outsideFile)
		output, runErr := command.CombinedOutput()
		return string(output), runErr
	}

	if output, err := run("echo ok > inside.txt"); err != nil {
		t.Fatalf("工作区内写入应成功：%v\n%s", err, output)
	}
	if data, err := os.ReadFile(filepath.Join(work, "inside.txt")); err != nil || strings.TrimSpace(string(data)) != "ok" {
		t.Fatalf("工作区文件内容不正确：%q %v", data, err)
	}

	output, err := run(`echo no > "$OUTSIDE"`)
	if err == nil {
		t.Fatalf("工作区外写入应被拒绝：%s", output)
	}
	if _, statErr := os.Stat(outsideFile); !os.IsNotExist(statErr) {
		t.Fatalf("沙箱外文件不应存在：%v", statErr)
	}
}

func TestSeatbeltAllowsGitInitAndProtectsJohnnyConfig(t *testing.T) {
	if err := Available(); err != nil {
		t.Skipf("sandbox-exec 不可用：%v", err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 不可用")
	}
	work := t.TempDir()
	runner, err := NewSeatbelt(Options{WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()

	run := func(script string) (string, error) {
		t.Helper()
		command := runner.CommandContext(context.Background(), "/bin/sh", "-c", script)
		command.Dir = work
		command.Env = runner.Environment(nil)
		output, runErr := command.CombinedOutput()
		return string(output), runErr
	}
	if output, err := run("git init"); err != nil {
		t.Fatalf("git init 应成功：%v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(work, ".git", "hooks")); err != nil {
		t.Fatalf("git init 应创建 hooks 目录：%v", err)
	}
	if output, err := run("mkdir -p .johnny && touch .johnny/test"); err == nil {
		t.Fatalf("项目 .johnny 应禁止 bash 写入：%s", output)
	}
}

func TestSeatbeltDeniesNetworkWhenDisabled(t *testing.T) {
	if err := Available(); err != nil {
		t.Skipf("sandbox-exec 不可用：%v", err)
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl 不可用")
	}
	work := t.TempDir()
	runner, err := NewSeatbelt(Options{WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()

	command := runner.CommandContext(context.Background(), "curl", "--max-time", "3", "-sS", "https://example.com")
	command.Dir = work
	command.Env = runner.Environment(nil)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("默认应禁止网络：%s", output)
	}
}

func TestSeatbeltEnvironmentUsesSessionTemp(t *testing.T) {
	if err := Available(); err != nil {
		t.Skipf("sandbox-exec 不可用：%v", err)
	}
	runner, err := NewSeatbelt(Options{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	seatbelt, ok := runner.(*Seatbelt)
	if !ok {
		t.Fatalf("类型不正确：%T", runner)
	}

	env := runner.Environment(nil)
	var tmpdir string
	for _, entry := range env {
		if strings.HasPrefix(entry, "TMPDIR=") {
			tmpdir = strings.TrimPrefix(entry, "TMPDIR=")
		}
	}
	if tmpdir == "" {
		t.Fatalf("环境变量缺少 TMPDIR：%v", env)
	}
	if filepath.Dir(tmpdir) != seatbelt.dir {
		t.Fatalf("TMPDIR 不在沙箱目录内：%s", tmpdir)
	}
}

func TestSeatbeltDescribe(t *testing.T) {
	if err := Available(); err != nil {
		t.Skipf("sandbox-exec 不可用：%v", err)
	}
	runner, err := NewSeatbelt(Options{WorkDir: t.TempDir(), Network: true})
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	if got := runner.Describe(); !strings.Contains(got, "网络开启") {
		t.Fatalf("Describe 应说明网络开启：%s", got)
	}
}

func TestSeatbeltCommandExitCode(t *testing.T) {
	if err := Available(); err != nil {
		t.Skipf("sandbox-exec 不可用：%v", err)
	}
	runner, err := NewSeatbelt(Options{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()

	command := runner.CommandContext(context.Background(), "/bin/sh", "-c", "exit 7")
	command.Dir = t.TempDir()
	command.Env = runner.Environment(nil)
	err = command.Run()
	if err == nil {
		t.Fatal("非零退出码应返回错误")
	}
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 7 {
		t.Fatalf("退出码应透传：%v", err)
	}
}

func TestSeatbeltProfileQuote(t *testing.T) {
	got := sbplQuote(`/tmp/a"b\c`)
	if got != `"/tmp/a\"b\\c"` {
		t.Fatalf("SBPL 转义不正确：%s", got)
	}
}
