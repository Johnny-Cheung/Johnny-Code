package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type restrictedPipeline struct {
	producer restrictedCommand
	consumer restrictedCommand
}

func splitReadonlyPipeline(command string) ([]string, bool) {
	parts := make([]string, 0, 2)
	start := 0
	inSingle, inDouble, escaped := false, false, false
	seen := false
	for i := 0; i < len(command); i++ {
		r := command[i]
		if escaped {
			escaped = false
			continue
		}
		if inSingle {
			if r == '\'' {
				inSingle = false
			}
			continue
		}
		if inDouble {
			switch r {
			case '\\':
				escaped = true
			case '"':
				inDouble = false
			}
			continue
		}
		switch r {
		case '\'', '"':
			if r == '\'' {
				inSingle = true
			} else {
				inDouble = true
			}
		case '\\':
			escaped = true
		case '|':
			seen = true
			if i+1 < len(command) && command[i+1] == '|' {
				return nil, true
			}
			part := strings.TrimSpace(command[start:i])
			if part == "" || len(parts) > 0 {
				return nil, true
			}
			parts = append(parts, part)
			start = i + 1
			i++
		case '&', ';', '<', '>', '$', '`', '(', ')', '\n', '\r':
			if seen {
				return nil, true
			}
		}
	}
	if !seen {
		return nil, false
	}
	if inSingle || inDouble || escaped {
		return nil, true
	}
	part := strings.TrimSpace(command[start:])
	if part == "" {
		return nil, true
	}
	parts = append(parts, part)
	return parts, true
}

func buildRestrictedPipeline(command string, scope queryPathResolver) (restrictedPipeline, error) {
	parts, claimed := splitReadonlyPipeline(command)
	if !claimed {
		return restrictedPipeline{}, permissionDenied("只读模式未识别为管道命令", nil)
	}
	if len(parts) != 2 {
		return restrictedPipeline{}, permissionDenied("只读模式只支持两段管道", nil)
	}
	producerTokens, err := tokenizeRestricted(parts[0])
	if err != nil || unsupportedChainExpansion(parts[0]) {
		return restrictedPipeline{}, permissionDenied("只读模式的管道生产者格式无效", err)
	}
	consumerTokens, err := tokenizeRestricted(parts[1])
	if err != nil || unsupportedChainExpansion(parts[1]) {
		return restrictedPipeline{}, permissionDenied("只读模式的管道消费者格式无效", err)
	}
	producer, err := buildRestrictedCommandTokens(producerTokens, scope, false)
	if err != nil {
		return restrictedPipeline{}, err
	}
	consumer, err := buildRestrictedStdinCommand(consumerTokens)
	if err != nil {
		return restrictedPipeline{}, err
	}
	return restrictedPipeline{producer: producer, consumer: consumer}, nil
}

func buildRestrictedStdinCommand(tokens []string) (restrictedCommand, error) {
	if len(tokens) == 0 {
		return restrictedCommand{}, invalidArguments("管道消费者不能为空", nil)
	}
	switch tokens[0] {
	case "head":
		flags, paths, err := parseNFlags(tokens, false)
		if err != nil {
			return restrictedCommand{}, err
		}
		if len(paths) != 0 {
			return restrictedCommand{}, permissionDenied("管道消费者 head 不接受路径", nil)
		}
		return restrictedCommand{program: "head", args: flags, readsStdin: true}, nil
	case "tail":
		flags, paths, err := parseNFlags(tokens, true)
		if err != nil {
			return restrictedCommand{}, err
		}
		if len(paths) != 0 {
			return restrictedCommand{}, permissionDenied("管道消费者 tail 不接受路径", nil)
		}
		return restrictedCommand{program: "tail", args: flags, readsStdin: true}, nil
	case "wc":
		flags, paths, err := splitShortOptions(tokens, "lwcmL")
		if err != nil {
			return restrictedCommand{}, err
		}
		if len(paths) != 0 {
			return restrictedCommand{}, permissionDenied("管道消费者 wc 不接受路径", nil)
		}
		return restrictedCommand{program: "wc", args: flags, readsStdin: true}, nil
	case "grep":
		flags := make([]string, 0, 2)
		index := 1
		for index < len(tokens) {
			token := tokens[index]
			if token == "--" {
				index++
				break
			}
			if strings.HasPrefix(token, "-") && token != "-" {
				if strings.HasPrefix(token, "--") {
					return restrictedCommand{}, permissionDenied(fmt.Sprintf("管道消费者 grep 不支持选项 %q", token), nil)
				}
				for _, option := range token[1:] {
					if !strings.ContainsRune("niEF", option) {
						return restrictedCommand{}, permissionDenied(fmt.Sprintf("管道消费者 grep 不支持选项 %q", token), nil)
					}
				}
				flags = append(flags, token)
				index++
				continue
			}
			break
		}
		if index >= len(tokens) || tokens[index] == "" {
			return restrictedCommand{}, invalidArguments("管道消费者 grep 缺少搜索模式", nil)
		}
		pattern := tokens[index]
		index++
		if index != len(tokens) {
			return restrictedCommand{}, permissionDenied("管道消费者 grep 不接受路径", nil)
		}
		args := append(flags, "--", pattern)
		return restrictedCommand{program: "grep", args: args, readsStdin: true}, nil
	default:
		return restrictedCommand{}, permissionDenied(fmt.Sprintf("只读模式不允许作为管道消费者执行 %q", tokens[0]), nil)
	}
}

func executeRestrictedPipeline(ctx context.Context, runtime Runtime, pipeline restrictedPipeline, timeoutSeconds int) (Result, error) {
	if _, err := exec.LookPath(pipeline.producer.program); err != nil {
		return Result{}, executionError(fmt.Sprintf("命令不可用：%s", pipeline.producer.program), err)
	}
	if _, err := exec.LookPath(pipeline.consumer.program); err != nil {
		return Result{}, executionError(fmt.Sprintf("命令不可用：%s", pipeline.consumer.program), err)
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = int(runtime.CommandTimeout.Seconds())
	}
	return executeRestrictedPipelineWithTimeout(ctx, runtime, pipeline, time.Duration(timeoutSeconds)*time.Second)
}

func executeRestrictedPipelineWithTimeout(ctx context.Context, runtime Runtime, pipeline restrictedPipeline, timeout time.Duration) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	reader, writer, err := os.Pipe()
	if err != nil {
		return Result{}, executionError("创建管道失败", err)
	}
	defer reader.Close()
	defer writer.Close()

	producer := exec.CommandContext(commandCtx, pipeline.producer.program, pipeline.producer.args...)
	consumer := exec.CommandContext(commandCtx, pipeline.consumer.program, pipeline.consumer.args...)
	producer.Dir, consumer.Dir = runtime.WorkDir, runtime.WorkDir
	producer.Env, consumer.Env = restrictedEnvironment(runtime.Env), restrictedEnvironment(runtime.Env)
	producer.Stdout = writer
	consumer.Stdin = reader
	producerStderr := &limitedWriter{limit: runtime.MaxOutputBytes}
	consumerStderr := &limitedWriter{limit: runtime.MaxOutputBytes}
	consumerStdout := &limitedWriter{limit: runtime.MaxOutputBytes}
	producer.Stderr = producerStderr
	consumer.Stdout = consumerStdout
	consumer.Stderr = consumerStderr
	for _, command := range []*exec.Cmd{producer, consumer} {
		command.WaitDelay = time.Second
		configureProcess(command)
		command.Cancel = func() error { return killProcess(command) }
	}
	if err := producer.Start(); err != nil {
		return Result{}, executionError("启动管道生产者失败", err)
	}
	if err := consumer.Start(); err != nil {
		_ = killProcess(producer)
		_ = producer.Wait()
		return Result{}, executionError("启动管道消费者失败", err)
	}
	_ = writer.Close()
	_ = reader.Close()
	producerErr := producer.Wait()
	consumerErr := consumer.Wait()
	_ = killProcess(producer)
	_ = killProcess(consumer)

	stdout := consumerStdout.String()
	stderr := strings.TrimSpace(strings.TrimSpace(producerStderr.String()) + "\n" + strings.TrimSpace(consumerStderr.String()))
	content := formatBashOutput(stdout, stderr, "")
	if parentErr := ctx.Err(); parentErr != nil {
		return Result{Content: content, IsError: true}, parentErr
	}
	if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
		return Result{Content: content + "\n[命令执行超时]", IsError: true}, executionError(fmt.Sprintf("命令执行超时（%s）", timeout), context.DeadlineExceeded)
	}
	producerCode := processExitCode(producerErr)
	consumerCode := processExitCode(consumerErr)
	consumerOK := consumerErr == nil || (pipeline.consumer.program == "grep" && consumerCode == 1)
	producerOK := producerErr == nil || ((pipeline.producer.program == "grep" || pipeline.producer.program == "diff") && producerCode == 1) || producerCode == 141
	if !consumerOK {
		return Result{Content: content + fmt.Sprintf("\n[管道消费者 exit code %d]", consumerCode), IsError: true}, executionError(fmt.Sprintf("管道消费者退出码 %d", consumerCode), consumerErr)
	}
	if !producerOK {
		return Result{Content: content + fmt.Sprintf("\n[管道生产者 exit code %d]", producerCode), IsError: true}, executionError(fmt.Sprintf("管道生产者退出码 %d", producerCode), producerErr)
	}
	if pipeline.consumer.program == "grep" && strings.TrimSpace(stdout) == "" {
		content = "（无匹配）"
	}
	return Result{Content: content}, nil
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
