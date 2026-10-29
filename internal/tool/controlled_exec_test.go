package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func controlledSh(t *testing.T, script string, redirects ...bashRedirect) controlledSegment {
	t.Helper()
	skipWithoutCommand(t, "sh")
	return controlledSegment{
		program:   "sh",
		args:      []string{"-c", script},
		env:       restrictedEnvironment(nil),
		redirects: redirects,
	}
}

func controlledProgram(t *testing.T, program string, args []string, redirects ...bashRedirect) controlledSegment {
	t.Helper()
	skipWithoutCommand(t, program)
	return controlledSegment{
		program:   program,
		args:      args,
		env:       restrictedEnvironment(nil),
		redirects: redirects,
	}
}

func runControlled(t *testing.T, runtime Runtime, pipelines ...controlledPipeline) (Result, error) {
	t.Helper()
	return executeControlledChain(context.Background(), runtime, controlledChain{pipelines: pipelines}, 5*time.Second)
}

func TestControlledPipelineRedirectOrder(t *testing.T) {
	runtime := testRuntime(t)
	result, err := runControlled(t, runtime, controlledPipeline{segments: []controlledSegment{
		controlledSh(t, "printf out; printf err >&2", bashRedirect{fd: 1, target: "/dev/null"}, bashRedirect{fd: 2, target: "&1"}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "" {
		t.Fatalf("重定向到 /dev/null 后不应有输出：%q", result.Content)
	}

	// 2>&1 复制当时的 stdout，因此先复制再丢弃 stdout 会保留 stderr。
	result, err = runControlled(t, runtime, controlledPipeline{segments: []controlledSegment{
		controlledSh(t, "printf out; printf err >&2", bashRedirect{fd: 2, target: "&1"}, bashRedirect{fd: 1, target: "/dev/null"}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Content, "out") || !strings.Contains(result.Content, "err") {
		t.Fatalf("重定向顺序语义错误：%q", result.Content)
	}
}

func TestControlledPipelineStderrJoinedIntoPipe(t *testing.T) {
	runtime := testRuntime(t)
	result, err := runControlled(t, runtime,
		controlledPipeline{segments: []controlledSegment{
			controlledSh(t, "echo err >&2", bashRedirect{fd: 2, target: "&1"}),
			controlledProgram(t, "wc", []string{"-l"}),
		}},
	)
	if err != nil || strings.TrimSpace(result.Content) != "1" {
		t.Fatalf("stderr 未合并进管道：%q %v", result.Content, err)
	}
}

func TestControlledPipelineRedirectOrderWithPipe(t *testing.T) {
	runtime := testRuntime(t)
	result, err := runControlled(t, runtime, controlledPipeline{segments: []controlledSegment{
		controlledSh(t, "echo out; echo err >&2", bashRedirect{fd: 2, target: "&1"}, bashRedirect{fd: 1, target: "/dev/null"}),
		controlledProgram(t, "wc", []string{"-l"}),
	}})
	if err != nil || strings.TrimSpace(result.Content) != "1" {
		t.Fatalf("管道中 2>&1 再丢弃 stdout 应只传 stderr：%q %v", result.Content, err)
	}
}

func TestControlledPipelineUpstreamExitIgnored(t *testing.T) {
	runtime := testRuntime(t)
	result, err := runControlled(t, runtime, controlledPipeline{segments: []controlledSegment{
		controlledSh(t, "exit 3"),
		controlledProgram(t, "cat", nil),
	}})
	if err != nil || result.IsError || result.Content != "" {
		t.Fatalf("上游失败且下游成功时不应报错：%#v %v", result, err)
	}
}

func TestControlledChainUsesLastExitForControlOperators(t *testing.T) {
	runtime := testRuntime(t)
	result, err := runControlled(t, runtime,
		controlledPipeline{segments: []controlledSegment{controlledSh(t, "exit 3")}},
		controlledPipeline{operator: "&&", segments: []controlledSegment{controlledProgram(t, "printf", []string{"%s", "skipped"})}},
		controlledPipeline{operator: "||", segments: []controlledSegment{controlledProgram(t, "printf", []string{"%s", "fallback"})}},
		controlledPipeline{operator: ";", segments: []controlledSegment{controlledProgram(t, "printf", []string{"%s", "after"})}},
	)
	if err != nil || !strings.Contains(result.Content, "fallback") || strings.Contains(result.Content, "skipped") || !strings.Contains(result.Content, "after") {
		t.Fatalf("&&/||/; 控制错误：%q %v", result.Content, err)
	}
}

func TestControlledPipelineLastFailureIsErrorButRawExitControls(t *testing.T) {
	runtime := testRuntime(t)
	result, err := executeControlledChain(context.Background(), runtime, controlledChain{pipelines: []controlledPipeline{
		{segments: []controlledSegment{controlledSh(t, "exit 3")}},
		{operator: "||", segments: []controlledSegment{controlledProgram(t, "printf", []string{"%s", "fallback"})}},
	}}, 5*time.Second)
	if err != nil || !strings.Contains(result.Content, "fallback") {
		t.Fatalf("|| 未按原始退出码执行：%q %v", result.Content, err)
	}

	_, err = executeControlledChain(context.Background(), runtime, controlledChain{pipelines: []controlledPipeline{
		{segments: []controlledSegment{controlledSh(t, "exit 3")}},
	}}, 5*time.Second)
	requireToolKind(t, err, KindExecution)
}

func TestControlledGrepCodeOneIsResultButRawExitControls(t *testing.T) {
	runtime := testRuntime(t)
	file := filepath.Join(runtime.WorkDir, "file.txt")
	if err := os.WriteFile(file, []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	grep := controlledProgram(t, "grep", []string{"--", "absent", file})
	grep.successExitCodes = map[int]bool{1: true}
	result, err := runControlled(t, runtime, controlledPipeline{segments: []controlledSegment{grep}})
	if err != nil || result.IsError || !strings.Contains(result.Content, "无匹配") {
		t.Fatalf("grep 1 应是非错误结果：%#v %v", result, err)
	}
	result, err = runControlled(t, runtime,
		controlledPipeline{segments: []controlledSegment{grep}},
		controlledPipeline{operator: "||", segments: []controlledSegment{controlledProgram(t, "printf", []string{"%s", "fallback"})}},
	)
	if err != nil || !strings.Contains(result.Content, "fallback") {
		t.Fatalf("grep 原始退出码 1 未驱动 ||：%q %v", result.Content, err)
	}
}

func TestControlledPipelineEarlyExitKillsUpstream(t *testing.T) {
	skipWithoutCommand(t, "yes")
	skipWithoutCommand(t, "head")
	runtime := testRuntime(t)
	start := time.Now()
	result, err := executeControlledChain(context.Background(), runtime, controlledChain{pipelines: []controlledPipeline{
		{segments: []controlledSegment{
			controlledProgram(t, "yes", nil),
			controlledProgram(t, "head", []string{"-n", "1"}),
		}},
	}}, 5*time.Second)
	if err != nil || strings.TrimSpace(result.Content) != "y" {
		t.Fatalf("下游提前退出结果错误：%q %v", result.Content, err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("下游提前退出后上游未被及时清理：%s", elapsed)
	}
}

func TestControlledPipelineBackgroundProcessCleanup(t *testing.T) {
	skipWithoutCommand(t, "sh")
	runtime := testRuntime(t)
	start := time.Now()
	result, err := executeControlledChain(context.Background(), runtime, controlledChain{pipelines: []controlledPipeline{
		{segments: []controlledSegment{controlledSh(t, "sleep 5 & echo ok")}},
	}}, 4*time.Second)
	if err != nil || !strings.Contains(result.Content, "ok") {
		t.Fatalf("后台进程清理失败：%q %v", result.Content, err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("后台进程让命令等待过久：%s", elapsed)
	}
}

func TestControlledPipelineTimeoutAndCancellation(t *testing.T) {
	skipWithoutCommand(t, "sh")
	runtime := testRuntime(t)
	start := time.Now()
	_, err := executeControlledChain(context.Background(), runtime, controlledChain{pipelines: []controlledPipeline{
		{segments: []controlledSegment{controlledSh(t, "sleep 5")}},
	}}, 100*time.Millisecond)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("超时应返回 DeadlineExceeded：%v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("超时未及时终止：%s", elapsed)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err = executeControlledChain(ctx, runtime, controlledChain{pipelines: []controlledPipeline{
		{segments: []controlledSegment{controlledSh(t, "sleep 5")}},
	}}, 5*time.Second)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应返回 context.Canceled：%v", err)
	}
}

func TestControlledPipelineMissingProgramDoesNotFallback(t *testing.T) {
	runtime := testRuntime(t)
	_, err := executeControlledChain(context.Background(), runtime, controlledChain{pipelines: []controlledPipeline{
		{segments: []controlledSegment{{program: "definitely-not-a-real-command", env: restrictedEnvironment(nil)}}},
	}}, 5*time.Second)
	requireToolKind(t, err, KindExecution)
	if !strings.Contains(err.Error(), "命令不可用") {
		t.Fatalf("缺失程序错误异常：%v", err)
	}
}

func TestControlledPipelineOutputLimit(t *testing.T) {
	skipWithoutCommand(t, "printf")
	runtime := testRuntime(t)
	runtime.MaxOutputBytes = 8
	result, err := runControlled(t, runtime, controlledPipeline{segments: []controlledSegment{
		controlledProgram(t, "printf", []string{"%s", "123456789"}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "12345678") || !strings.Contains(result.Content, "输出已截断") {
		t.Fatalf("输出限制未生效：%q", result.Content)
	}
}

func TestControlledPipelineVirtualCdPipeline(t *testing.T) {
	runtime := testRuntime(t)
	result, err := runControlled(t, runtime,
		controlledPipeline{virtual: true},
		controlledPipeline{operator: "&&", segments: []controlledSegment{controlledProgram(t, "printf", []string{"%s", "after-cd"})}},
	)
	if err != nil || result.Content != "after-cd" {
		t.Fatalf("虚拟 cd 流水线错误：%q %v", result.Content, err)
	}
}
