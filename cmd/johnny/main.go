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
	version          = "0.5.0"
	commit           = "dev"
	configFileBase64 string
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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
	maxTurns := flags.Int("max-turns", agent.DefaultMaxTurns, "单次任务最多模型请求次数")
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

	prompt := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if len(flags.Args()) == 0 {
		fmt.Fprint(stderr, "请输入任务：")
		prompt, err = readPrompt(ctx, input)
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(stderr)
			return 0
		}
		if err != nil {
			return reportError(stderr, "读取输入失败：", err)
		}
	}
	if prompt == "" {
		fmt.Fprintln(stderr, "任务不能为空。")
		return 1
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

	logs := openSessionLogger(session.Metadata{WorkDir: runtime.WorkDir, API: cfg.API, Model: cfg.Model}, stderr)
	defer logs.close()

	display := newRenderer(output, stderr)
	emit := func(event agent.Event) error {
		// 记录失败只降级日志功能，不阻塞任务，也不掩盖输出错误。
		logs.record(event)
		return display.Handle(event)
	}
	result, runErr := agent.Run(ctx, agent.Options{
		Client:          client,
		Tools:           registry,
		Model:           cfg.Model,
		System:          systemPrompt(runtime.WorkDir),
		MaxTurns:        *maxTurns,
		RequestTimeout:  agent.DefaultRequestTimeout,
		MaxOutputTokens: 4096,
	}, prompt, emit)
	if err := display.finish(); runErr == nil && err != nil {
		runErr = err
	}
	return reportResult(stderr, result, runErr)
}

func systemPrompt(workDir string) string {
	return fmt.Sprintf(`你是 Johnny-Code，一个在用户本地工作目录中执行任务的编程助手。
当前工作目录：%s

请使用可用工具实际读取文件、修改代码或执行命令来完成任务，需要了解项目时先查看实际文件。
只有在对应工具成功返回后，才能声称已经完成该操作；工具失败时根据错误继续处理或说明阻塞原因。
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
	warned   bool
}

func openSessionLogger(metadata session.Metadata, stderr io.Writer) *sessionLogger {
	recorder, err := session.New(metadata)
	if err != nil {
		logger := &sessionLogger{stderr: stderr}
		logger.warn("创建", err)
		return logger
	}
	return newSessionLogger(recorder, stderr)
}

func newSessionLogger(recorder eventRecorder, stderr io.Writer) *sessionLogger {
	logger := &sessionLogger{recorder: recorder, stderr: stderr}
	if path := recorder.Path(); path != "" {
		fmt.Fprintf(stderr, "会话记录：%s\n", escapeTerminal(path))
	}
	return logger
}

func (l *sessionLogger) record(event agent.Event) {
	if l == nil || l.recorder == nil {
		return
	}
	if err := l.recorder.Record(event); err != nil {
		l.warn("写入", err)
		// 首写失败后停止继续写：关闭已写入的文件（不删除已有记录），
		// 后续事件直接跳过，避免每个事件重复失败、拖慢任务。
		_ = l.recorder.Close()
		l.recorder = nil
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
  johnny "任务"     自动执行工具并完成任务后退出
  johnny            输入一个任务，自动执行工具并完成任务后退出

选项：
  -h, --help        显示帮助
  --version         显示版本和提交号
  --model 名称      临时覆盖模型名（选项放在任务之前）
  --api 协议        anthropic-messages、openai-responses、openai-chat-completions
  --max-turns 次数  单次任务最多模型请求次数（默认 %d，必须是正整数）

配置文件：%s
Ctrl+C 取消当前任务，等待输入时 Ctrl+D 退出。
`, agent.DefaultMaxTurns, path)
}

func readPrompt(ctx context.Context, input io.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	type result struct {
		text string
		err  error
	}
	ready := make(chan result, 1)
	go func() {
		text, err := bufio.NewReader(input).ReadString('\n')
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
