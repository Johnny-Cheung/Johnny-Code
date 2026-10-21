package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/config"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/session"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

const (
	previewMaxLines    = 20
	previewMaxBytes    = 4 << 10
	previewMaxKeyBytes = 256
)

var (
	version          = "0.6.0"
	commit           = "dev"
	configFileBase64 string
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-ctx.Done()
		// 恢复默认信号处理，让再次 Ctrl+C 可以强制退出。
		stop()
	}()
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, input io.Reader, output, stderr io.Writer) int {
	flags := flag.NewFlagSet("johnny", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var help, showVersion bool
	flags.BoolVar(&help, "help", false, "显示帮助")
	flags.BoolVar(&help, "h", false, "显示帮助")
	flags.BoolVar(&showVersion, "version", false, "显示版本")
	api := flags.String("api", "", "临时选择 API 协议")
	modelName := flags.String("model", "", "临时选择模型")
	maxTurns := flags.Int("max-turns", agent.DefaultMaxTurns, "每轮任务最多模型请求次数")
	flags.Usage = func() { printHelp(stderr) }
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if help {
		printHelp(output)
		return 0
	}
	if showVersion {
		fmt.Fprintf(output, "johnny v%s (%s)\n", version, commit)
		return 0
	}
	if *maxTurns < 1 {
		fmt.Fprintln(stderr, "--max-turns 必须是正整数。")
		return 1
	}

	interactive := len(flags.Args()) == 0
	prompt := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if !interactive && prompt == "" {
		fmt.Fprintln(stderr, "任务不能为空。")
		return 1
	}

	path, err := configPath()
	if err != nil {
		return reportError(stderr, "配置错误：", err)
	}
	var overrides config.Overrides
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "api":
			overrides.API = api
		case "model":
			overrides.Model = modelName
		}
	})
	cfg, err := config.LoadWithOverrides(path, overrides)
	if err != nil {
		return reportError(stderr, "配置错误：", err)
	}

	workDir, err := os.Getwd()
	if err != nil {
		return reportError(stderr, "工具初始化失败：", fmt.Errorf("无法获取当前工作目录：%w", err))
	}
	runtime, err := tool.NewRuntime(workDir)
	if err != nil {
		return reportError(stderr, "工具初始化失败：", err)
	}
	registry, err := tool.NewDefaultRegistry(runtime)
	if err != nil {
		return reportError(stderr, "工具初始化失败：", err)
	}
	client, err := model.NewClient(model.Options{API: model.API(cfg.API), APIKey: cfg.APIKey, BaseURL: cfg.BaseURL})
	if err != nil {
		return reportError(stderr, "配置错误：", err)
	}

	// 日志按需创建：只有出现第一条有效输入并开始执行时才落盘。
	logs := openSessionLogger(session.Metadata{WorkDir: runtime.WorkDir, API: cfg.API, Model: cfg.Model}, stderr)
	defer logs.close()

	display := newRenderer(output, stderr)
	conversation := agent.NewConversation(agent.Options{
		Client:          client,
		Tools:           registry,
		Model:           cfg.Model,
		System:          systemPrompt(runtime.WorkDir),
		MaxTurns:        *maxTurns,
		RequestTimeout:  agent.DefaultRequestTimeout,
		MaxOutputTokens: 4096,
	})

	if !interactive {
		return runTask(ctx, conversation, prompt, logs, display, stderr)
	}
	if useTUI(input, output) {
		return runTerminal(ctx, conversation, input, output, stderr, logs, cfg.Model, runtime.WorkDir)
	}
	return runInteractive(ctx, conversation, input, logs, display, stderr)
}

// runTask 执行参数模式下的唯一任务；无论成功失败都直接结束进程。
func runTask(ctx context.Context, conversation *agent.Conversation, prompt string, logs *sessionLogger, display *renderer, stderr io.Writer) int {
	result, runErr, displayErr := executeTurn(ctx, conversation, prompt, logs, display)
	if displayErr != nil {
		return reportError(stderr, "任务失败：", displayErr)
	}
	return reportResult(stderr, result, runErr)
}

// runInteractive 反复读取用户输入并在同一会话历史上执行。
// 输入 /exit、EOF 或父 context 取消时结束；单轮模型失败只报告并继续等待。
func runInteractive(ctx context.Context, conversation *agent.Conversation, input io.Reader, logs *sessionLogger, display *renderer, stderr io.Writer) int {
	reader := bufio.NewReader(input)
	for {
		if err := writeProgress(stderr, "请输入任务："); err != nil {
			return reportError(stderr, "输出输入提示失败：", err)
		}
		line, err := readPrompt(ctx, reader)
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(stderr)
			return 0
		}
		if err != nil {
			return reportError(stderr, "读取输入失败：", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "/exit" {
			return 0
		}

		result, runErr, displayErr := executeTurn(ctx, conversation, line, logs, display)
		if displayErr != nil {
			return reportError(stderr, "任务失败：", displayErr)
		}
		if errors.Is(runErr, context.Canceled) || ctx.Err() != nil {
			if runErr == nil {
				runErr = ctx.Err()
			}
			return reportResult(stderr, result, runErr)
		}
		if runErr != nil {
			// 本轮失败不终止会话；历史由 Conversation 保留，继续等待输入。
			reportResult(stderr, result, runErr)
		}
	}
}

// executeTurn 运行一轮用户输入，并把终端输出错误与模型/工具错误分开返回。
// 日志写入失败只降级日志功能，不影响本轮。
func executeTurn(ctx context.Context, conversation *agent.Conversation, prompt string, logs *sessionLogger, display *renderer) (agent.Result, error, error) {
	var displayErr error
	emit := func(event agent.Event) error {
		logs.record(event)
		if err := display.Handle(event); err != nil {
			if displayErr == nil {
				displayErr = err
			}
			return err
		}
		return nil
	}
	result, runErr := conversation.Run(ctx, prompt, emit)
	if err := display.finish(); err != nil && displayErr == nil {
		displayErr = err
	}
	return result, runErr, displayErr
}

func systemPrompt(workDir string) string {
	return fmt.Sprintf(`你是 Johnny-Code，一个在用户本地工作目录中执行任务的编程助手。
当前工作目录：%s

请使用可用工具实际读取文件、修改代码或执行命令来完成任务，需要了解项目时先查看实际文件。
只有在对应工具成功返回后，才能声称已经完成该操作；工具失败时根据错误继续处理或说明阻塞原因。
展示目录树、表格等依赖对齐的文本时，请使用 text 类型的围栏代码块包裹，避免被 Markdown 重排。
完成任务后，用简洁的中文总结结果。`, workDir)
}

type renderer struct {
	output       io.Writer
	stderr       io.Writer
	needsNewline bool
}

func newRenderer(output, stderr io.Writer) *renderer {
	return &renderer{output: output, stderr: stderr}
}

// Handle 按顺序渲染 agent 事件。stdout 只写正文 delta；工具进展和等待写入 stderr。
// 写 stdout 失败时返回错误，agent 会停止后续调度，避免继续执行工具产生副作用。
func (r *renderer) Handle(event agent.Event) error {
	switch event.Type {
	case agent.EventRequestStart:
		if err := r.endLine(); err != nil {
			return err
		}
		message := "正在等待模型回复…\n"
		if event.Turn > 1 {
			message = fmt.Sprintf("正在等待模型回复…（第 %d 次请求）\n", event.Turn)
		}
		return writeProgress(r.stderr, message)
	case agent.EventModel:
		return r.displayModel(event.ModelEvent)
	case agent.EventResponse, agent.EventToolStart, agent.EventToolEnd:
		if err := r.endLine(); err != nil {
			return err
		}
		switch event.Type {
		case agent.EventToolStart:
			return r.displayToolStart(event.Call)
		case agent.EventToolEnd:
			return r.displayToolEnd(event.ToolResult)
		}
	case agent.EventEnd:
		return r.endLine()
	}
	return nil
}

// finish 确保即使 agent 异常结束、没有发出响应结束事件，也不会把正文和后续输出粘在同一行。
func (r *renderer) finish() error {
	return r.endLine()
}

func (r *renderer) displayModel(event *model.Event) error {
	if event == nil || event.Type != model.EventBlockDelta || event.Block.Type != model.BlockText || event.Delta == "" {
		return nil
	}
	return r.write(event.Delta)
}

func (r *renderer) write(text string) error {
	n, err := io.WriteString(r.output, text)
	if n > len(text) {
		n = len(text)
	}
	if n > 0 {
		r.needsNewline = text[n-1] != '\n'
	}
	if err == nil && n != len(text) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("输出回复失败：%w", err)
	}
	return nil
}

func (r *renderer) endLine() error {
	if !r.needsNewline {
		return nil
	}
	return r.write("\n")
}

func (r *renderer) displayToolStart(call *model.ToolCall) error {
	if call == nil {
		return nil
	}
	name := escapeTerminal(call.Name)
	key, truncated := limitBytes(toolKey(call), previewMaxKeyBytes)
	key = escapeTerminal(key)
	if truncated {
		key += "…（参数已截断）"
	}
	if key == "" {
		return writeProgress(r.stderr, fmt.Sprintf("[%s]\n", name))
	}
	return writeProgress(r.stderr, fmt.Sprintf("[%s] %s\n", name, key))
}

func (r *renderer) displayToolEnd(result *model.ToolResult) error {
	if result == nil {
		return writeProgress(r.stderr, "[完成]\n")
	}
	status := "成功"
	if result.IsError {
		status = "失败"
	}
	preview := toolPreview(result.Content)
	if preview == "" {
		return writeProgress(r.stderr, fmt.Sprintf("[%s]\n", status))
	}
	return writeProgress(r.stderr, fmt.Sprintf("[%s] %s\n", status, preview))
}

func writeProgress(w io.Writer, text string) error {
	n, err := io.WriteString(w, text)
	if n > len(text) {
		n = len(text)
	}
	if err == nil && n != len(text) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("输出任务进度失败：%w", err)
	}
	return nil
}

func toolKey(call *model.ToolCall) string {
	if call == nil {
		return ""
	}
	var args struct {
		Path    string `json:"path"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ""
	}
	if args.Command != "" {
		return args.Command
	}
	return args.Path
}

// toolPreview 只影响终端显示，不改变回传模型的工具结果。
func toolPreview(content string) string {
	limited, truncated := limitPreview(content)
	preview := escapeTerminal(limited)
	if truncated {
		preview += "\n…（预览已截断）"
	}
	return preview
}

func limitPreview(text string) (string, bool) {
	text, truncated := limitBytes(text, previewMaxBytes)
	lines := 0
	end := 0
	for lines < previewMaxLines {
		index := strings.IndexByte(text[end:], '\n')
		if index < 0 {
			break
		}
		end += index + 1
		lines++
	}
	if lines == previewMaxLines && end < len(text) {
		text = text[:end]
		truncated = true
	}
	return text, truncated
}

func limitBytes(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	text = text[:limit]
	// 只在字节上限截断时回退到完整 UTF-8 边界，不因内容中已有的
	// 非法字节而清空整个预览。
	for len(text) > 0 {
		r, size := utf8.DecodeLastRuneInString(text)
		if r != utf8.RuneError || size > 1 {
			break
		}
		text = text[:len(text)-1]
	}
	return text, true
}

// escapeTerminal 将控制字符转为可见转义，避免工具参数或输出注入终端。
func escapeTerminal(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))
	for _, r := range text {
		switch r {
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			if unicode.IsControl(r) {
				if r <= 0xff {
					fmt.Fprintf(&builder, `\x%02x`, r)
				} else {
					fmt.Fprintf(&builder, `\u%04x`, r)
				}
				continue
			}
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

type eventRecorder interface {
	Path() string
	Record(agent.Event) error
	Close() error
}

type sessionLogger struct {
	recorder eventRecorder
	stderr   io.Writer
	metadata *session.Metadata
	warned   bool
	disabled bool
}

// openSessionLogger 延迟到第一条事件时才创建文件，单纯 /exit 或 EOF 不落盘。
func openSessionLogger(metadata session.Metadata, stderr io.Writer) *sessionLogger {
	return &sessionLogger{stderr: stderr, metadata: &metadata}
}

func newSessionLogger(recorder eventRecorder, stderr io.Writer) *sessionLogger {
	logger := &sessionLogger{recorder: recorder, stderr: stderr}
	if path := recorder.Path(); path != "" {
		fmt.Fprintf(stderr, "会话记录：%s\n", escapeTerminal(path))
	}
	return logger
}

func (l *sessionLogger) ensure() {
	if l == nil || l.recorder != nil || l.metadata == nil || l.disabled {
		return
	}
	recorder, err := session.New(*l.metadata)
	if err != nil {
		// 创建失败也只降级日志功能；禁用以避免每次事件重复失败。
		l.disabled = true
		l.warn("创建", err)
		return
	}
	l.recorder = recorder
	if path := recorder.Path(); path != "" {
		fmt.Fprintf(l.stderr, "会话记录：%s\n", escapeTerminal(path))
	}
}

func (l *sessionLogger) record(event agent.Event) {
	if l == nil {
		return
	}
	l.ensure()
	if l.recorder == nil {
		return
	}
	if err := l.recorder.Record(event); err != nil {
		l.warn("写入", err)
		// 首写失败后停止继续写：关闭已写入的文件（不删除已有记录），
		// 后续事件直接跳过，避免每个事件重复失败、拖慢任务。
		_ = l.recorder.Close()
		l.recorder = nil
		l.disabled = true
	}
}

func (l *sessionLogger) close() {
	if l == nil || l.recorder == nil {
		return
	}
	if err := l.recorder.Close(); err != nil {
		l.warn("关闭", err)
	}
}

// warn 只输出一次，记录功能失败后仍继续执行原任务。
func (l *sessionLogger) warn(action string, err error) {
	if l.warned {
		return
	}
	l.warned = true
	fmt.Fprintf(l.stderr, "警告：会话记录%s失败：%s；已降级继续任务。\n", action, escapeTerminal(err.Error()))
}

func reportResult(output io.Writer, result agent.Result, err error) int {
	if err != nil {
		return reportError(output, "任务失败：", err)
	}
	switch result.Status {
	case agent.StatusCompleted:
		return 0
	case agent.StatusCanceled:
		fmt.Fprintln(output, "请求已取消。")
		return 130
	case agent.StatusMaxTurns:
		return reportError(output, "任务失败：", agent.ErrMaxTurns)
	case agent.StatusMaxTokens:
		return reportError(output, "任务失败：", agent.ErrMaxTokens)
	case agent.StatusRefusal:
		return reportError(output, "任务失败：", agent.ErrRefusal)
	case agent.StatusRequestTimeout:
		return reportError(output, "任务失败：", context.DeadlineExceeded)
	default:
		return reportError(output, "任务失败：", fmt.Errorf("任务未正常完成（状态：%s）", result.Status))
	}
}

func configPath() (string, error) {
	path := ".env"
	if configFileBase64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(configFileBase64)
		if err != nil || len(decoded) == 0 {
			return "", errors.New("构建中的配置文件路径无效，请重新安装 johnny")
		}
		path = string(decoded)
	}
	return filepath.Abs(path)
}

func printHelp(output io.Writer) {
	path, err := configPath()
	if err != nil {
		path = "构建路径无效，请重新安装 johnny"
	}
	fmt.Fprintf(output, `Johnny-Code

用法：
  johnny "任务"     执行一个任务，完成后退出，不读取标准输入
  johnny            进入多轮交互，保留本进程历史，直到 /exit 或 EOF

选项：
  -h, --help        显示帮助
  --version         显示版本和提交号
  --model 名称      临时覆盖模型名（选项放在任务之前）
  --api 协议        anthropic-messages、openai-responses、openai-chat-completions
  --max-turns 次数  每轮用户输入最多模型请求次数（默认 %d，必须是正整数）

交互模式：
  每轮输入一个任务；空行不发送请求；输入完整的 /exit 或按 Ctrl+D（EOF）退出。
  终端中启用 TUI：Enter 发送，Alt+Enter 换行，多行粘贴后手动发送，↑↓ 浏览本次输入历史。
  TUI 中 Ctrl+C 取消本轮并等待清理后继续输入；空闲时 Ctrl+C 清空输入。
  每轮独立计算模型请求次数；单轮失败会说明原因并继续等待输入。
  管道或重定向使用普通文本；参数任务和普通文本模式中 Ctrl+C 取消并退出（退出码 130）。
配置文件：%s
`, agent.DefaultMaxTurns, path)
}

func readPrompt(ctx context.Context, input io.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// 交互模式传入同一个 *bufio.Reader，避免每次新建 Reader 预读后丢弃后续行。
	reader, ok := input.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(input)
	}
	type result struct {
		text string
		err  error
	}
	ready := make(chan result, 1)
	go func() {
		text, err := reader.ReadString('\n')
		ready <- result{text, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case res := <-ready:
		text := strings.TrimSpace(res.text)
		if errors.Is(res.err, io.EOF) && text != "" {
			return text, nil
		}
		return text, res.err
	}
}

func reportError(output io.Writer, prefix string, err error) int {
	switch {
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(output, "请求已取消。")
		return 130
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintln(output, "请求超时（单次最多等待 5 分钟），请检查服务状态或重试。")
	default:
		fmt.Fprintln(output, prefix, err)
	}
	return 1
}
