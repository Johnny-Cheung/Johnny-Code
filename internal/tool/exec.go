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
type argvOutcome struct {
	result   Result
	exitCode int
	started  bool
	err      error
}

func executeArgvOutcome(ctx context.Context, runtime Runtime, spec restrictedCommand, timeout time.Duration, env []string, successExitCodes map[int]bool) argvOutcome {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := contextError(ctx); err != nil {
		return argvOutcome{exitCode: -1, err: err}
	}
	if spec.program == "" {
		err := invalidArguments("命令不能为空", nil)
		return argvOutcome{exitCode: -1, err: err}
	}
	if timeout <= 0 {
		timeout = runtime.CommandTimeout
	}
	if _, err := exec.LookPath(spec.program); err != nil {
		err = executionError(fmt.Sprintf("命令不可用：%s", spec.program), err)
		return argvOutcome{exitCode: -1, err: err}
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
	started := true
	runErr := cmd.Run()
	_ = killProcess(cmd)
	if runErr != nil {
		if parentErr := ctx.Err(); parentErr != nil {
			return argvOutcome{result: Result{}, exitCode: -1, started: started, err: parentErr}
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Success() {
			return argvOutcome{result: Result{Content: formatBashOutput(stdout.String(), stderr.String(), "")}, exitCode: 0, started: started}
		}
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
			content := formatBashOutput(stdout.String(), stderr.String(), "\n[命令执行超时]")
			err := executionError(fmt.Sprintf("命令执行超时（%s）", timeout), context.DeadlineExceeded)
			return argvOutcome{result: Result{Content: content, IsError: true}, exitCode: -1, started: started, err: err}
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			code := exitErr.ExitCode()
			content := formatBashOutput(stdout.String(), stderr.String(), fmt.Sprintf("\n[exit code %d]", code))
			if successExitCodes[code] {
				return argvOutcome{result: Result{Content: formatBashOutput(stdout.String(), stderr.String(), "")}, exitCode: code, started: started}
			}
			err := executionError(fmt.Sprintf("命令退出码 %d", code), exitErr)
			return argvOutcome{result: Result{Content: content, IsError: true}, exitCode: code, started: started, err: err}
		}
		err := executionError("命令启动或执行失败", runErr)
		return argvOutcome{result: Result{Content: formatBashOutput(stdout.String(), stderr.String(), ""), IsError: true}, exitCode: -1, started: started, err: err}
	}
	if parentErr := ctx.Err(); parentErr != nil {
		return argvOutcome{result: Result{}, exitCode: -1, started: started, err: parentErr}
	}
	return argvOutcome{result: Result{Content: formatBashOutput(stdout.String(), stderr.String(), "")}, exitCode: 0, started: started}
}

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
