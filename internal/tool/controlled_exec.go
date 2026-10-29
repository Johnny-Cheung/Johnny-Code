package tool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// controlledSegment 是已经通过权限预检、可以直接 exec 的命令段。
type controlledSegment struct {
	program          string
	args             []string
	dir              string
	env              []string
	redirects        []bashRedirect
	successExitCodes map[int]bool
}

type controlledPipeline struct {
	operator string
	segments []controlledSegment
	// virtual 表示仅包含虚拟 cd 的流水线：不启动进程，按成功处理。
	virtual bool
}

type controlledChain struct {
	pipelines []controlledPipeline
}

type pipelineOutcome struct {
	result   Result
	exitCode int
	err      error
	fatal    bool
}

// syncLimitedWriter 是 limitedWriter 的并发安全版本。同一个捕获目标可能同时
// 接收 stdout 和 stderr（2>&1），os/exec 会从两个复制协程并发调用 Write。
type syncLimitedWriter struct {
	mu        sync.Mutex
	limit     int
	buffer    bytes.Buffer
	truncated bool
}

func (w *syncLimitedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.limit <= 0 {
		return len(data), nil
	}
	remaining := w.limit - w.buffer.Len()
	if remaining > 0 {
		if len(data) <= remaining {
			_, _ = w.buffer.Write(data)
		} else {
			_, _ = w.buffer.Write(data[:remaining])
			w.truncated = true
		}
	} else if len(data) > 0 {
		w.truncated = true
	}
	return len(data), nil
}

func (w *syncLimitedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	content := w.buffer.String()
	if w.truncated {
		content += "\n[输出已截断：超过限制，已省略后续内容]"
	}
	return content
}

type boundedOutput struct {
	limit     int
	builder   strings.Builder
	truncated bool
}

func (b *boundedOutput) append(content string) {
	if content == "" || b.truncated {
		return
	}
	if b.limit <= 0 || b.builder.Len()+len(content) <= b.limit {
		b.builder.WriteString(content)
		return
	}
	remaining := b.limit - b.builder.Len()
	if remaining > 0 {
		b.builder.WriteString(content[:remaining])
	}
	b.truncated = true
}

func (b *boundedOutput) String() string {
	content := b.builder.String()
	if b.truncated {
		content += "\n[输出已截断：超过限制，已省略后续内容]"
	}
	return content
}

// executeControlledChain 直接执行已经预检过的命令链。整条链共享一个超时，
// 管道内各段并发启动；每条流水线按最后一段的原始退出码决定 &&/||/; 是否
// 继续执行，不启用 pipefail。
func executeControlledChain(ctx context.Context, runtime Runtime, chain controlledChain, timeout time.Duration) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	if len(chain.pipelines) == 0 {
		return Result{}, invalidArguments("命令不能为空", nil)
	}
	if timeout <= 0 {
		timeout = runtime.CommandTimeout
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	output := &boundedOutput{limit: runtime.MaxOutputBytes}
	lastExit := 0
	var lastResult Result
	var lastErr error
	for index, pipeline := range chain.pipelines {
		if index > 0 && !pipelineRuns(pipeline.operator, lastExit) {
			continue
		}
		if err := chainContextError(ctx, commandCtx); err != nil {
			return Result{Content: output.String(), IsError: true}, err
		}
		outcome := executeControlledPipeline(ctx, commandCtx, runtime, pipeline)
		output.append(outcome.result.Content)
		lastExit, lastResult, lastErr = outcome.exitCode, outcome.result, outcome.err
		if outcome.fatal {
			return Result{Content: output.String(), IsError: true}, outcome.err
		}
	}
	if lastResult.IsError {
		return Result{Content: output.String(), IsError: true}, lastErr
	}
	return Result{Content: output.String()}, nil
}

func pipelineRuns(operator string, lastExit int) bool {
	switch operator {
	case "&&":
		return lastExit == 0
	case "||":
		return lastExit != 0
	default:
		return true
	}
}

// chainContextError 区分外部取消和整条链的超时。
func chainContextError(parent, command context.Context) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if errors.Is(command.Err(), context.DeadlineExceeded) {
		return executionError("命令链执行超时", context.DeadlineExceeded)
	}
	if err := command.Err(); err != nil {
		return err
	}
	return nil
}

func executeControlledPipeline(parentCtx, commandCtx context.Context, runtime Runtime, pipeline controlledPipeline) pipelineOutcome {
	if pipeline.virtual && len(pipeline.segments) == 0 {
		return pipelineOutcome{result: Result{}, exitCode: 0}
	}
	if len(pipeline.segments) == 0 {
		return pipelineOutcome{result: Result{Content: "命令不能为空", IsError: true}, exitCode: -1, err: invalidArguments("命令不能为空", nil), fatal: true}
	}
	resolvedPrograms := make([]string, len(pipeline.segments))
	for index, segment := range pipeline.segments {
		if segment.program == "" {
			return pipelineOutcome{result: Result{Content: "命令不能为空", IsError: true}, exitCode: -1, err: invalidArguments("命令不能为空", nil), fatal: true}
		}
		resolved, err := resolveProgramPath(segment.program, segment.dir, runtime.WorkDir)
		if err != nil {
			message := fmt.Sprintf("命令不可用：%s", segment.program)
			return pipelineOutcome{
				result:   Result{Content: message, IsError: true},
				exitCode: 127,
				err:      executionError(message, err),
			}
		}
		resolvedPrograms[index] = resolved
	}

	count := len(pipeline.segments)
	readers := make([]*os.File, 0, count-1)
	writers := make([]*os.File, 0, count-1)
	closePipes := func() {
		for _, file := range readers {
			_ = file.Close()
		}
		for _, file := range writers {
			_ = file.Close()
		}
	}
	defer closePipes()
	for index := 0; index < count-1; index++ {
		reader, writer, err := os.Pipe()
		if err != nil {
			return pipelineOutcome{result: Result{Content: "创建管道失败", IsError: true}, exitCode: -1, err: executionError("创建管道失败", err), fatal: true}
		}
		readers = append(readers, reader)
		writers = append(writers, writer)
	}

	lastStdout := &syncLimitedWriter{limit: runtime.MaxOutputBytes}
	stderrWriters := make([]*syncLimitedWriter, count)
	commands := make([]*exec.Cmd, count)
	cancels := make([]context.CancelFunc, count)
	for index, segment := range pipeline.segments {
		segmentCtx, segmentCancel := context.WithCancel(commandCtx)
		cancels[index] = segmentCancel
		command := exec.CommandContext(segmentCtx, segment.program, segment.args...)
		command.Path = resolvedPrograms[index]
		command.Dir = segment.dir
		if command.Dir == "" {
			command.Dir = runtime.WorkDir
		}
		command.Env = segment.env
		if command.Env == nil {
			// 防御性兜底：绝不把宿主完整环境意外传给子进程。
			command.Env = restrictedEnvironment(nil)
		}
		command.WaitDelay = time.Second
		configureProcess(command)
		command.Cancel = func() error { return killProcess(command) }

		var stdout io.Writer = lastStdout
		if index < count-1 {
			stdout = writers[index]
		}
		stderr := &syncLimitedWriter{limit: runtime.MaxOutputBytes}
		stderrWriters[index] = stderr
		var stderrWriter io.Writer = stderr
		for _, redirect := range segment.redirects {
			switch redirect.target {
			case "/dev/null":
				if redirect.fd == 1 {
					stdout = io.Discard
				} else {
					stderrWriter = io.Discard
				}
			case "&1":
				if redirect.fd == 2 {
					stderrWriter = stdout
				}
			}
		}
		command.Stdout = stdout
		command.Stderr = stderrWriter
		if index > 0 {
			command.Stdin = readers[index-1]
		}
		commands[index] = command
	}
	defer func() {
		for _, cancel := range cancels {
			if cancel != nil {
				cancel()
			}
		}
	}()

	for index, command := range commands {
		if err := command.Start(); err != nil {
			// 清理已经启动的进程组和管道，避免后台进程泄漏。
			for j := 0; j < index; j++ {
				cancels[j]()
			}
			closePipes()
			for j := 0; j < index; j++ {
				_ = killProcess(commands[j])
				_ = commands[j].Wait()
			}
			if parentErr := parentCtx.Err(); parentErr != nil {
				return pipelineOutcome{result: Result{Content: "命令已取消", IsError: true}, exitCode: -1, err: parentErr, fatal: true}
			}
			if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
				return pipelineOutcome{
					result:   Result{Content: "[命令执行超时]", IsError: true},
					exitCode: -1,
					err:      executionError("命令链执行超时", context.DeadlineExceeded),
					fatal:    true,
				}
			}
			message := fmt.Sprintf("启动命令失败：%s", pipeline.segments[index].program)
			return pipelineOutcome{result: Result{Content: message, IsError: true}, exitCode: -1, err: executionError(message, err), fatal: true}
		}
	}
	closePipes()

	type waitResult struct {
		index int
		err   error
	}
	results := make(chan waitResult, count)
	for index := range commands {
		go func(index int) {
			results <- waitResult{index: index, err: commands[index].Wait()}
		}(index)
	}
	finished := make([]bool, count)
	waitErrors := make([]error, count)
	for completed := 0; completed < count; completed++ {
		result := <-results
		finished[result.index] = true
		waitErrors[result.index] = result.err
		// 下游提前退出时立即取消上游的进程组，避免生产者继续写满管道。
		for upstream := 0; upstream < result.index; upstream++ {
			if !finished[upstream] {
				cancels[upstream]()
			}
		}
	}
	for _, command := range commands {
		_ = killProcess(command)
	}

	lastIndex := count - 1
	lastErr := normalizedWaitError(commands[lastIndex], waitErrors[lastIndex])
	lastExit := processExitCode(lastErr)
	rawOutput := pipelineOutput(lastStdout, stderrWriters)

	if parentErr := parentCtx.Err(); parentErr != nil {
		return pipelineOutcome{result: Result{Content: rawOutput, IsError: true}, exitCode: -1, err: parentErr, fatal: true}
	}
	if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
		content := rawOutput
		if content != "" {
			content += "\n[命令执行超时]"
		} else {
			content = "[命令执行超时]"
		}
		return pipelineOutcome{
			result:   Result{Content: content, IsError: true},
			exitCode: -1,
			err:      executionError("命令链执行超时", context.DeadlineExceeded),
			fatal:    true,
		}
	}
	return classifyPipelineOutcome(pipeline.segments[lastIndex], lastErr, lastExit, rawOutput)
}

// pipelineOutput 收集最后一段 stdout（以及被 2>&1 合并进去的 stderr）和
// 所有未被重定向丢弃的 stderr，不附加退出码后缀。
func pipelineOutput(lastStdout *syncLimitedWriter, stderrWriters []*syncLimitedWriter) string {
	var stderr strings.Builder
	for _, writer := range stderrWriters {
		if writer == nil {
			continue
		}
		part := strings.TrimSpace(writer.String())
		if part == "" {
			continue
		}
		if stderr.Len() > 0 {
			stderr.WriteByte('\n')
		}
		stderr.WriteString(part)
	}
	return formatBashOutput(lastStdout.String(), stderr.String(), "")
}

func classifyPipelineOutcome(segment controlledSegment, lastErr error, lastExit int, rawOutput string) pipelineOutcome {
	if lastErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(lastErr, &exitErr) {
			message := fmt.Sprintf("命令执行失败：%s", segment.program)
			return pipelineOutcome{
				result:   Result{Content: rawOutput, IsError: true},
				exitCode: -1,
				err:      executionError(message, lastErr),
				fatal:    true,
			}
		}
	}
	success := pipelineLastSuccess(segment, lastExit)
	content := rawOutput
	if !success {
		content = strings.TrimSuffix(rawOutput, "\n") + fmt.Sprintf("\n[exit code %d]", lastExit)
	}
	if success && lastExit == 1 && strings.TrimSpace(rawOutput) == "" {
		if segment.program == "grep" || segment.program == "rg" {
			content = "（无匹配）"
		}
	}
	if !success {
		var err error = lastErr
		if err == nil {
			err = executionError(fmt.Sprintf("命令退出码 %d", lastExit), nil)
		} else {
			err = executionError(fmt.Sprintf("命令退出码 %d", lastExit), lastErr)
		}
		return pipelineOutcome{
			result:   Result{Content: content, IsError: true},
			exitCode: lastExit,
			err:      err,
		}
	}
	return pipelineOutcome{result: Result{Content: content}, exitCode: lastExit}
}

func pipelineLastSuccess(segment controlledSegment, exitCode int) bool {
	if exitCode == 0 {
		return true
	}
	return segment.successExitCodes[exitCode]
}

// normalizedWaitError 把 WaitDelay 在进程已经成功退出后返回的错误视为成功。
func normalizedWaitError(command *exec.Cmd, err error) error {
	if err != nil && command.ProcessState != nil && command.ProcessState.Success() {
		return nil
	}
	return err
}

func processExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func querySuccessExitCodes(program string) map[int]bool {
	switch program {
	case "grep", "diff", "rg":
		return map[int]bool{1: true}
	default:
		return nil
	}
}

func directControlledSegment(tokens []string, redirects []bashRedirect, dir string, env []string) controlledSegment {
	return controlledSegment{
		program:   tokens[0],
		args:      append([]string(nil), tokens[1:]...),
		dir:       dir,
		env:       env,
		redirects: append([]bashRedirect(nil), redirects...),
	}
}

func queryControlledSegment(spec restrictedCommand, redirects []bashRedirect, dir string, env []string) controlledSegment {
	return controlledSegment{
		program:          spec.program,
		args:             append([]string(nil), spec.args...),
		dir:              dir,
		env:              env,
		redirects:        append([]bashRedirect(nil), redirects...),
		successExitCodes: querySuccessExitCodes(spec.program),
	}
}

// resolveProgramPath 在虚拟 cwd 下解析命令路径。包含路径分隔符的程序按
// 命令段目录解析；裸程序名仍走 PATH 查找。解析遵循内核语义：符号链接
// 先展开，再应用 ".."，避免执行与权限分类看到的目标不一致。
func resolveProgramPath(program, dir, fallbackDir string) (string, error) {
	if !strings.ContainsRune(program, filepath.Separator) {
		return exec.LookPath(program)
	}
	if strings.IndexByte(program, 0) >= 0 {
		return "", errors.New("路径包含无效字符")
	}
	base := dir
	if base == "" {
		base = fallbackDir
	}
	resolved, err := resolvePathFromBase(base, program)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", errors.New("不是可执行文件")
	}
	return resolved, nil
}
