package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

type Bash struct {
	runtime Runtime
}

type bashArguments struct {
	Command        string `json:"command"`
	TimeoutSeconds *int   `json:"timeout_seconds"`
}

func NewBash(runtime Runtime) *Bash {
	return &Bash{runtime: normalizeRuntime(runtime)}
}

func (b *Bash) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name:        "bash",
		Description: "在工作目录中执行一条 shell 命令，并返回标准输出、错误输出和退出状态。",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"要执行的 shell 命令"},"timeout_seconds":{"type":"integer","minimum":1,"maximum":600,"description":"超时秒数，默认 120"}},"required":["command"]}`),
	}
}

func (b *Bash) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var args bashArguments
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, invalidArguments("bash 参数格式无效", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return Result{}, invalidArguments("bash 的 command 不能为空", nil)
	}
	timeout := b.runtime.CommandTimeout
	if args.TimeoutSeconds != nil {
		if *args.TimeoutSeconds < 1 || *args.TimeoutSeconds > 600 {
			return Result{}, invalidArguments("bash 的 timeout_seconds 必须在 1 到 600 之间", nil)
		}
		timeout = time.Duration(*args.TimeoutSeconds) * time.Second
	}
	shell := "bash"
	if _, err := exec.LookPath(shell); err != nil {
		shell = "sh"
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, shell, "-c", args.Command)
	cmd.Dir = b.runtime.WorkDir
	cmd.Env = b.runtime.Env
	cmd.WaitDelay = time.Second
	configureProcess(cmd)
	cmd.Cancel = func() error { return killProcess(cmd) }
	stdout := &limitedWriter{limit: b.runtime.MaxOutputBytes}
	stderr := &limitedWriter{limit: b.runtime.MaxOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	runErr := cmd.Run()
	// 无论成败，都清理本次调用仍存活的后台进程，避免进程泄漏。
	// 正常命令此时进程组已结束，清理为空操作。
	_ = killProcess(cmd)
	if runErr != nil {
		if parentErr := ctx.Err(); parentErr != nil {
			return Result{}, parentErr
		}
		// shell 已经成功退出时，命令本身已经完成。剩余等待只是后台子进程
		// 占用输出管道（例如 `cmd &`），此时按成功返回已收集的输出。
		if cmd.ProcessState != nil && cmd.ProcessState.Success() {
			return Result{Content: formatBashOutput(stdout.String(), stderr.String(), "")}, nil
		}
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
			content := formatBashOutput(stdout.String(), stderr.String(), "\n[命令执行超时]")
			return Result{Content: content, IsError: true}, executionError(fmt.Sprintf("命令执行超时（%s）", timeout), context.DeadlineExceeded)
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			code := exitErr.ExitCode()
			content := formatBashOutput(stdout.String(), stderr.String(), fmt.Sprintf("\n[exit code %d]", code))
			return Result{Content: content, IsError: true}, executionError(fmt.Sprintf("命令退出码 %d", code), exitErr)
		}
		return Result{Content: formatBashOutput(stdout.String(), stderr.String(), ""), IsError: true}, executionError("bash 启动或执行失败", runErr)
	}
	if parentErr := ctx.Err(); parentErr != nil {
		return Result{}, parentErr
	}
	return Result{Content: formatBashOutput(stdout.String(), stderr.String(), "")}, nil
}

func formatBashOutput(stdout, stderr, suffix string) string {
	var builder strings.Builder
	builder.WriteString(stdout)
	if stderr != "" {
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString("[stderr]\n")
		builder.WriteString(stderr)
	}
	builder.WriteString(suffix)
	return builder.String()
}
