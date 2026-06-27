// 来源：公众号@小林coding
// 后端八股网站：xiaolincoding.com
// Agent网站：xiaolinnote.com
// 简历模版：jianli.xiaolinnote.com

package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

const maxTimeout = 600

type BashTool struct{}

func (t *BashTool) Name() string            { return "Bash" }

func (t *BashTool) Description() string     { return BashDescription }

func (t *BashTool) Category() ToolCategory  { return CategoryCommand }

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

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	var stdout, stderr bytes.Buffer
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

	return ToolResult{Output: sb.String(), IsError: isError}
}
