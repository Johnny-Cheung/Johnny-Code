package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const maxTimeout = 600

type BashTool struct{}

func (t *BashTool) Name() string { return "Bash" }

func (t *BashTool) Description() string { return BashDescription }

func (t *BashTool) Category() ToolCategory { return CategoryCommand }

func (t *BashTool) Schema() map[string]any {
	return map[string]any{
		"name":        t.Name(),
		"description": t.Description(),
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "Shell command to execute"},
				"timeout": map[string]any{"type": "integer", "description": "Timeout in seconds (max 600)", "default": 120},
			},
			"required": []string{"command"},
		},
	}
}

func (t *BashTool) Execute(ctx context.Context, args map[string]any) ToolResult {
	command, _ := args["command"].(string)
	if command == "" {
		return ToolResult{Output: "Error: command is required", IsError: true}
	}

	timeout := intArg(args, "timeout", 120)
	if timeout > maxTimeout {
		timeout = maxTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := shellCommand(ctx, command)
	stdout := cappedBuffer{limit: MaxOutputChars}
	stderr := cappedBuffer{limit: MaxOutputChars}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	if ctx.Err() == context.DeadlineExceeded {
		return ToolResult{Output: fmt.Sprintf("Error: command timed out after %ds", timeout), IsError: true}
	}

	exitCode := 0
	isError := false
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
			isError = exitCode != 0
		} else if ctx.Err() == nil {
			return ToolResult{Output: fmt.Sprintf("Error executing command: %s", err), IsError: true}
		}
	}

	var sb bytes.Buffer
	fmt.Fprintf(&sb, "$ %s\n", command)
	if stdout.Len() > 0 {
		sb.Write(stdout.Bytes())
		if stdout.Bytes()[stdout.Len()-1] != '\n' {
			sb.WriteByte('\n')
		}
	}
	if stderr.Len() > 0 {
		fmt.Fprintf(&sb, "STDERR: %s", stderr.String())
		if stderr.Bytes()[stderr.Len()-1] != '\n' {
			sb.WriteByte('\n')
		}
	}
	fmt.Fprintf(&sb, "(exit code %d)", exitCode)

	return ToolResult{Output: TruncateOutput(sb.String(), MaxOutputChars), IsError: isError}
}

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd.exe", "/d", "/s", "/c", normalizeWindowsCommand(command))
	}
	return exec.CommandContext(ctx, "bash", "-c", command)
}

func normalizeWindowsCommand(command string) string {
	trimmed := strings.TrimSpace(command)
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"cmd.exe /d /s /c", "cmd.exe /c", "cmd /c"} {
		if strings.HasPrefix(lower, prefix) {
			inner := strings.TrimSpace(trimmed[len(prefix):])
			if len(inner) >= 2 && inner[0] == '"' && inner[len(inner)-1] == '"' {
				inner = inner[1 : len(inner)-1]
			}
			return inner
		}
	}
	return trimmed
}

// cappedBuffer keeps command output bounded even before the Agent layer sees
// the ToolResult. Returning len(p) from Write lets the child process continue
// normally while preventing an unbounded bytes.Buffer allocation in Johnny
// Code itself.
type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.Buffer.Write(p[:remaining])
		} else {
			_, _ = b.Buffer.Write(p)
		}
	}
	return len(p), nil
}
