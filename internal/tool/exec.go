package tool

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

func commandTimeout(runtime Runtime, timeoutSeconds *int) (time.Duration, error) {
	timeout := runtime.CommandTimeout
	if timeoutSeconds == nil {
		return timeout, nil
	}
	if *timeoutSeconds < 1 || *timeoutSeconds > 600 {
		return 0, invalidArguments("bash 的 timeout_seconds 必须在 1 到 600 之间", nil)
	}
	return time.Duration(*timeoutSeconds) * time.Second, nil
}

// executeArgv 直接启动程序，不经过 shell。restricted 查询和默认模式
// 自动放行的项目前缀命令共用这条路径。
func executeArgv(ctx context.Context, runtime Runtime, spec restrictedCommand, timeout time.Duration, env []string, successExitCodes map[int]bool) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	if spec.program == "" {
		return Result{}, invalidArguments("命令不能为空", nil)
	}
	if timeout <= 0 {
		timeout = runtime.CommandTimeout
	}
	if _, err := exec.LookPath(spec.program); err != nil {
		return Result{}, executionError(fmt.Sprintf("命令不可用：%s", spec.program), err)
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, spec.program, spec.args...)
	cmd.Dir = runtime.WorkDir
	cmd.Env = env
	cmd.WaitDelay = time.Second
	configureProcess(cmd)
	cmd.Cancel = func() error { return killProcess(cmd) }
	stdout := &limitedWriter{limit: runtime.MaxOutputBytes}
	stderr := &limitedWriter{limit: runtime.MaxOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	runErr := cmd.Run()
	_ = killProcess(cmd)
	if runErr != nil {
		if parentErr := ctx.Err(); parentErr != nil {
			return Result{}, parentErr
		}
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
			if successExitCodes[code] {
				return Result{Content: formatBashOutput(stdout.String(), stderr.String(), "")}, nil
			}
			content := formatBashOutput(stdout.String(), stderr.String(), fmt.Sprintf("\n[exit code %d]", code))
			return Result{Content: content, IsError: true}, executionError(fmt.Sprintf("命令退出码 %d", code), exitErr)
		}
		return Result{Content: formatBashOutput(stdout.String(), stderr.String(), ""), IsError: true}, executionError("命令启动或执行失败", runErr)
	}
	if parentErr := ctx.Err(); parentErr != nil {
		return Result{}, parentErr
	}
	return Result{Content: formatBashOutput(stdout.String(), stderr.String(), "")}, nil
}
