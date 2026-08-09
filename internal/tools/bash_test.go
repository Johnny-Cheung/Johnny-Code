package tools

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestCappedBufferBoundsCommandOutput(t *testing.T) {
	var output cappedBuffer
	output.limit = 10
	if n, err := output.Write([]byte(strings.Repeat("x", 100))); err != nil || n != 100 {
		t.Fatalf("capped Write = (%d, %v), want (100, nil)", n, err)
	}
	if output.Len() != 10 {
		t.Fatalf("capped buffer length = %d, want 10", output.Len())
	}
}

func TestShellCommandUsesAvailableWindowsShell(t *testing.T) {
	cmd := shellCommand(context.Background(), "echo shell-ok")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("shell command failed: %v", err)
	}
	if !strings.Contains(string(out), "shell-ok") {
		t.Fatalf("shell output = %q", out)
	}
}

func TestBashToolRunsNativeShellCommand(t *testing.T) {
	command := "echo shell-ok"
	if runtime.GOOS == "windows" {
		command = `cmd.exe /d /s /c "echo shell-ok"`
	}
	result := (&BashTool{}).Execute(context.Background(), map[string]any{
		"command": command,
		"timeout": 2,
	})
	if result.IsError {
		t.Fatalf("Bash returned error: %s", result.Output)
	}
	if !strings.Contains(result.Output, "shell-ok") {
		t.Fatalf("Bash output = %q", result.Output)
	}
}
