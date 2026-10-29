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
	"sync"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/config"
	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
	"github.com/Johnny-Cheung/Johnny-Code/internal/memory"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/session"
	"github.com/Johnny-Cheung/Johnny-Code/internal/skill"
	"github.com/Johnny-Cheung/Johnny-Code/internal/subagent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

const (
	previewMaxLines    = 20
	previewMaxBytes    = 4 << 10
	previewMaxKeyBytes = 256
)

var (
	version          = "0.13.0"
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
	return runWithEntryPolicy(ctx, args, input, output, stderr, true)
}

// runWithEntryPolicy 是 CLI 内部实现。requireTUI 为 true 时只允许终端中的
// 无参数 TUI 交互；测试用 false 覆盖旧的参数任务和管道路径。
func runWithEntryPolicy(ctx context.Context, args []string, input io.Reader, output, stderr io.Writer, requireTUI bool) int {
	flags := flag.NewFlagSet("johnny", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var help, showVersion bool
	flags.BoolVar(&help, "help", false, "显示帮助")
	flags.BoolVar(&help, "h", false, "显示帮助")
	flags.BoolVar(&showVersion, "version", false, "显示版本")
	api := flags.String("api", "", "临时选择 API 协议")
	modelName := flags.String("model", "", "临时选择模型")
	maxTurns := flags.Int("max-turns", agent.DefaultMaxTurns, "每轮任务最多模型请求次数")
	readOnlyFlag := flags.Bool("read", false, "只读模式")
	skipFlag := flags.Bool("skip", false, "放行模式：跳过逐次审批")
	permissionFlag := flags.String("permission-mode", "", "权限模式长写法：default、read-only 或 allow")
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
	permissionSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "permission-mode" {
			permissionSet = true
		}
	})
	mode, err := resolvePermissionMode(*permissionFlag, permissionSet, *readOnlyFlag, *skipFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if requireTUI {
		if len(flags.Args()) > 0 {
			fmt.Fprintln(stderr, "Johnny-Code 只支持在终端直接运行 johnny 进入 TUI 交互模式，不接受参数任务。")
			return 1
		}
		if !useTUI(input, output) {
			fmt.Fprintln(stderr, "Johnny-Code 只支持在终端直接运行 johnny 进入 TUI 交互模式；管道和重定向不支持。")
			return 1
		}
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
	contextCfg, err := contextConfigFromEnv(os.LookupEnv)
	if err != nil {
		return reportError(stderr, "配置错误：", err)
	}
	sessionID, err := session.NewID()
	if err != nil {
		return reportError(stderr, "会话初始化失败：", err)
	}

	workDir, err := os.Getwd()
	if err != nil {
		return reportError(stderr, "工具初始化失败：", fmt.Errorf("无法获取当前工作目录：%w", err))
	}
	runtime, err := tool.NewRuntime(workDir)
	if err != nil {
		return reportError(stderr, "工具初始化失败：", err)
	}
	skills := skill.Discover(runtime.WorkDir)
	printSkillNotice(stderr, skills)
	memoryContext := memory.NewContext(runtime.WorkDir)
	var registry *tool.Registry
	if memoryContext.Store != nil && memoryContext.Rules != nil {
		registry, err = tool.NewDefaultRegistryWithMemory(runtime, skills, memoryContext.Store, memoryContext.Rules, sessionID)
	} else {
		registry, err = tool.NewDefaultRegistryWithSkill(runtime, skills)
	}
	if err != nil {
		return reportError(stderr, "工具初始化失败：", err)
	}
	var rules *tool.ProjectRules
	var gate *tool.ApprovalGate
	if mode == permissionDefault {
		rules, err = loadProjectPermissions(runtime.WorkDir, interactive && interactiveReader(input), input, stderr)
		if err != nil {
			return reportError(stderr, "项目权限规则初始化失败：", err)
		}
		gate = tool.NewApprovalGate()
	}
	client, err := model.NewClient(model.Options{API: model.API(cfg.API), APIKey: cfg.APIKey, BaseURL: cfg.BaseURL})
	if err != nil {
		return reportError(stderr, "配置错误：", err)
	}
	var memoryReview agent.MemoryReviewFunc
	if mode != permissionReadOnly && !strings.EqualFold(os.Getenv("JOHNNY_MEMORY_AUTO"), "off") &&
		memoryContext.Store != nil && memoryContext.Rules != nil && memoryContext.Agents != nil {
		reviewer := memory.NewReviewer(client, cfg.Model, sessionID, memoryContext.Store, memoryContext.Rules, memoryContext.Agents, mode.label())
		memoryReview = reviewer.Review
	}

	view := contextview.NewManager(contextview.Config{
		Budget:    contextview.NewBudget(contextCfg.Window, contextCfg.OutputTokens),
		WorkDir:   runtime.WorkDir,
		SessionID: sessionID,
	})
	defer func() {
		if err := view.Close(); err != nil {
			fmt.Fprintf(stderr, "警告：清理工具输出存档失败：%s\n", escapeTerminal(err.Error()))
		}
	}()

	// 日志按需创建：只有出现第一条有效输入并开始执行时才落盘。
	logs := openSessionLogger(session.Metadata{ID: sessionID, WorkDir: runtime.WorkDir, API: cfg.API, Model: cfg.Model, PermissionMode: string(mode)}, stderr)
	defer logs.close()
	if memoryContext.Store != nil {
		memoryContext.Store.Observer = func(event memory.WriteEvent) {
			logs.recordMemory(session.MemoryRecord{
				Scope: event.Scope, Action: event.Action, Name: event.Name,
				Path: event.Path, Source: event.Source, Rule: event.Scope == "user_agents",
			})
		}
	}
	if memoryContext.Rules != nil {
		memoryContext.Rules.Observer = func(event memory.WriteEvent) {
			logs.recordMemory(session.MemoryRecord{
				Scope: event.Scope, Action: event.Action, Name: event.Name,
				Path: event.Path, Source: event.Source, Rule: true,
			})
		}
	}
	if gate != nil {
		gate.SetAudit(logs.recordPermission)
	}

	// task 工具只在 TUI 入口注册：子任务的状态、审批和唤醒都依赖终端界面。
	tuiMode := interactive && useTUI(input, output)
	toolRegistry := registry
	var manager *subagent.Manager
	subagentHub := &subagentEvents{}
	if tuiMode {
		manager, err = subagent.NewManager(subagent.Config{
			WorkDir: runtime.WorkDir, SessionID: sessionID, InteractionMode: mode.label(),
			Client: client, Model: cfg.Model, MaxOutputTokens: contextCfg.OutputTokens,
			Budget:  contextview.NewBudget(contextCfg.Window, contextCfg.OutputTokens),
			Toolbox: subagent.Toolbox{Runtime: runtime, Registry: registry, Mode: subagent.Mode(mode), Rules: rules, Gate: gate},
			Pinned:  memoryContext.Load,
			System: func(info subagent.Info) string {
				return childSystemPrompt(runtime.WorkDir, mode, info, skills)
			},
			OpenLog: openChildLog(logs, session.Metadata{ID: sessionID, WorkDir: runtime.WorkDir, API: cfg.API, Model: cfg.Model, PermissionMode: string(mode)}),
			OnEvent: func(event subagent.Event) {
				if event.Type == subagent.EventStarted || event.Type == subagent.EventEnded {
					logs.recordSubagent(subagentRecord(event))
				}
				if record, ok := workflowRecord(event); ok {
					logs.recordWorkflow(record)
				}
				subagentHub.publish(event)
			},
		})
		if err != nil {
			return reportError(stderr, "子任务初始化失败：", err)
		}
		defer manager.Close()
		taskTool, err := tool.NewTaskTool(runtime, manager)
		if err != nil {
			return reportError(stderr, "子任务初始化失败：", err)
		}
		workflowTool, err := tool.NewWorkflowTool(runtime, manager)
		if err != nil {
			return reportError(stderr, "子任务初始化失败：", err)
		}
		if toolRegistry, err = registry.With(taskTool, workflowTool); err != nil {
			return reportError(stderr, "子任务初始化失败：", err)
		}
	}
	var tools agent.Tools = toolRegistry
	switch mode {
	case permissionReadOnly:
		readOnly, err := tool.NewReadOnly(toolRegistry, runtime)
		if err != nil {
			return reportError(stderr, "只读模式初始化失败：", err)
		}
		tools = readOnly
	case permissionDefault:
		permissioned, err := tool.NewDefault(toolRegistry, runtime, rules, gate)
		if err != nil {
			return reportError(stderr, "默认模式初始化失败：", err)
		}
		tools = permissioned
	}
	if manager != nil {
		if tools, err = manager.GuardMain(tools); err != nil {
			return reportError(stderr, "子任务初始化失败：", err)
		}
	}
	buildSystem := func() string { return mainSystemPrompt(runtime.WorkDir, mode, skills, manager != nil) }

	display := newRenderer(output, stderr)
	conversation := agent.NewConversation(agent.Options{
		Client:          client,
		Tools:           tools,
		Model:           cfg.Model,
		System:          buildSystem(),
		InteractionMode: mode.label(),
		MaxTurns:        *maxTurns,
		RequestTimeout:  agent.DefaultRequestTimeout,
		MaxOutputTokens: contextCfg.OutputTokens,
		ContextView:     view,
		Pinned:          memoryContext.Load,
		MemoryReview:    memoryReview,
	})
	if manager != nil {
		manager.SetParent(conversation, tools)
	}
	compact := func(ctx context.Context, emit func(agent.Event) error) (string, error) {
		return conversation.Compact(ctx, func(event agent.Event) error {
			logs.record(event)
			if emit != nil {
				return emit(event)
			}
			switch event.Type {
			case agent.EventNotice:
				if event.Notice != "" {
					fmt.Fprintln(stderr, "[上下文] "+event.Notice)
				}
			case agent.EventSummary:
				if event.Summary != nil {
					fmt.Fprintf(stderr, "[上下文] 摘要已更新：估算 %d → %d token。\n", event.Summary.Before, event.Summary.After)
				}
			}
			return nil
		})
	}
	contextCommands := &contextController{
		stats: func() string { return contextStatsText(conversation.ContextStats()) },
		compact: func(ctx context.Context) (string, error) {
			return compact(ctx, nil)
		},
	}
	memoryCommands := &memoryController{store: memoryContext.Store, writable: mode != permissionReadOnly}
	localHandler := newLocalCommandHandlerWithMemory(rules, skills, func() error {
		if err := skills.Reload(); err != nil {
			return err
		}
		conversation.SetSystemPrompt(buildSystem())
		return nil
	}, contextCommands, memoryCommands)
	if gate != nil {
		localHandler.session = func() *tool.SessionGrants { return gate.Session() }
	}
	localCommand := localHandler.Handle

	if !interactive {
		printPermissionNotice(stderr, mode)
		return runTask(ctx, conversation, prompt, logs, display, stderr)
	}
	if useTUI(input, output) {
		var children *terminalSubagents
		if manager != nil {
			children = &terminalSubagents{manager: manager, conversation: conversation, hub: subagentHub}
		}
		return runTerminal(ctx, conversation, input, output, stderr, logs, cfg.Model, runtime.WorkDir, mode.label(), gate, localCommand, compact, skills, children)
	}
	printPermissionNotice(stderr, mode)
	return runInteractive(ctx, conversation, input, logs, display, stderr, localCommand, contextCommands.compact)
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
func runInteractive(ctx context.Context, conversation *agent.Conversation, input io.Reader, logs *sessionLogger, display *renderer, stderr io.Writer, localCommand func(string) (localCommandResult, bool), compact func(context.Context) (string, error)) int {
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
		prompt := line
		if localCommand != nil {
			if result, handled := localCommand(line); handled {
				if result.Compact {
					if compact == nil {
						fmt.Fprintln(stderr, "当前会话未启用上下文治理。")
						continue
					}
					text, compactErr := compact(ctx)
					if compactErr != nil {
						if ctx.Err() != nil {
							return reportError(stderr, "任务失败：", compactErr)
						}
						fmt.Fprintln(stderr, "上下文压缩失败：", compactErr)
						continue
					}
					if text != "" {
						fmt.Fprintln(stderr, text)
					}
					continue
				}
				if result.Prompt == "" {
					if result.Output != "" {
						fmt.Fprintln(stderr, result.Output)
					}
					continue
				}
				prompt = result.Prompt
			}
		}

		result, runErr, displayErr := executeTurn(ctx, conversation, prompt, logs, display)
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

func printPermissionNotice(output io.Writer, mode permissionMode) {
	switch mode {
	case permissionReadOnly:
		fmt.Fprintln(output, "权限模式：只读（仅允许 read 和受限查询；不是系统沙箱）。")
	case permissionDefault:
		fmt.Fprintln(output, "权限模式：默认（路径白名单内自动；其他操作需要审批；当前非 TUI 入口的审批会被拒绝）。")
	case permissionAllow:
		fmt.Fprintln(output, "权限模式：放行（工具直接执行，不逐次审批）。")
	}
}

func systemPrompt(workDir string, mode permissionMode, skills *skill.Store) string {
	base := systemPromptBase(workDir, mode)
	if section := skills.Prompt(); section != "" {
		base += "\n\n" + section
	}
	return base
}

func systemPromptBase(workDir string, mode permissionMode) string {
	if mode == permissionReadOnly {
		return fmt.Sprintf(`你是 Johnny-Code，一个在用户本地工作目录中执行任务的编程助手。
当前工作目录：%s
当前权限模式：只读。

你只能读取当前工作目录内的文件，并使用受限查询命令（echo、pwd、ls、find、grep、cat、head、tail、wc、stat、du、diff、basename、dirname、realpath、rg、sed、sort、uniq、tree、file）了解项目；每个程序只开放已校验的查询参数，sort 大输入可能使用系统临时文件，这不是操作系统沙箱。
请使用相对路径或工作目录内的绝对路径；不要尝试 write、edit，也不要执行任意 shell、构建或测试命令。查询时可以使用固定查询命令组成的多段管道、&&/||/; 链、虚拟 cd 和 2>&1、/dev/null 尾部重定向，例如 find . -name '*.go' | head -n 20；不要使用未引用的通配符、变量、命令替换或其他重定向。
如果任务需要修改文件或执行未开放的命令，请清楚说明原因，并建议用户重新启动 johnny 进入默认模式，由审批决定；放行模式需要用户显式使用 --skip。
只有在工具成功返回后，才能声称完成了操作；工具失败时根据错误继续处理或说明阻塞原因。
展示目录树等依赖对齐的纯文本时，请使用 text 类型的围栏代码块包裹，避免被 Markdown 重排。
完成任务后，用简洁的中文总结结果。`, workDir)
	}
	if mode == permissionDefault {
		return fmt.Sprintf(`你是 Johnny-Code，一个在用户本地工作目录中执行任务的编程助手。
当前工作目录：%s
当前权限模式：默认。

路径白名单内的 read、write、edit 会自动执行；用户级只读查询白名单和已记录的项目命令前缀会自动执行。可以使用受控的 cd、&&、||、;、多段固定查询管道和 2>&1、/dev/null 尾部重定向，系统会先检查全部分段再按每条流水线最后一段的原始退出码执行；未引用的通配符、变量、命令替换、循环和其他重定向需要完整 shell 审批。含 || 的命令链不提供项目永久记录，但可按符合条件的命令段创建本会话授权；危险后缀和 remember 每次询问。
Bash 已经在当前工作目录执行，优先使用相对路径或绝对路径，不要为了切换目录拼接复杂 shell 命令。为了减少不必要的审批：给通配符加引号或改用 find -name；一条命令不超过 8 段，把长探索拆成多次调用；避免命令替换和 for 循环。需要审批时，等待工具结果；如果用户拒绝，不要重复同一未授权操作，改用已授权的方案或说明阻塞原因。
不要尝试绕过权限层，也不要把审批提示当作已授权。
只有在工具成功返回后，才能声称完成了操作；工具失败时根据错误继续处理或说明阻塞原因。
展示目录树等依赖对齐的纯文本时，请使用 text 类型的围栏代码块包裹，避免被 Markdown 重排。
完成任务后，用简洁的中文总结结果。`, workDir)
	}
	return fmt.Sprintf(`你是 Johnny-Code，一个在用户本地工作目录中执行任务的编程助手。
当前工作目录：%s

请使用可用工具实际读取文件、修改代码或执行命令来完成任务，需要了解项目时先查看实际文件。
只有在对应工具成功返回后，才能声称已经完成该操作；工具失败时根据错误继续处理或说明阻塞原因。
展示目录树等依赖对齐的纯文本时，请使用 text 类型的围栏代码块包裹，避免被 Markdown 重排。
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
	case agent.EventNotice:
		if err := r.endLine(); err != nil {
			return err
		}
		return writeProgress(r.stderr, "[上下文] "+event.Notice+"\n")
	case agent.EventSummary:
		if event.Summary == nil {
			return nil
		}
		if err := r.endLine(); err != nil {
			return err
		}
		return writeProgress(r.stderr, fmt.Sprintf("[上下文] 摘要已更新：估算 %d → %d token；输入 /context 查看摘要。\n", event.Summary.Before, event.Summary.After))
	case agent.EventResponse, agent.EventToolStart, agent.EventToolEnd:
		if err := r.endLine(); err != nil {
			return err
		}
		switch event.Type {
		case agent.EventToolStart:
			return r.displayToolStart(event.Call)
		case agent.EventToolEnd:
			return r.displayToolEnd(event.ToolResult, event.Artifact)
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

func (r *renderer) displayToolEnd(result *model.ToolResult, artifact string) error {
	if result == nil {
		return writeProgress(r.stderr, "[完成]\n")
	}
	status := "成功"
	if result.IsError {
		status = "失败"
	}
	preview := toolPreview(result.Content)
	if artifact != "" {
		if preview != "" {
			preview += "\n"
		}
		preview += "存档：" + escapeTerminal(artifact)
	}
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
		Path        string `json:"path"`
		Command     string `json:"command"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Kind        string `json:"kind"`
		Action      string `json:"action"`
		Title       string `json:"title"`
		Step        int    `json:"step"`
		Tasks       []any  `json:"tasks"`
		Reason      string `json:"reason"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ""
	}
	if call.Name == "workflow" {
		switch args.Action {
		case "start":
			return "start：" + args.Title
		case "run":
			return fmt.Sprintf("run：第 %d 步（%d 个任务）", args.Step, len(args.Tasks))
		case "end":
			return "end：" + args.Reason
		}
		return args.Action
	}
	if args.Command != "" {
		return args.Command
	}
	if args.Path != "" {
		return args.Path
	}
	if call.Name == "task" && args.Description != "" {
		return args.Description + "（" + args.Kind + "）"
	}
	return args.Name
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
	RecordPermission(session.PermissionRecord) error
	Close() error
}

type sessionLogger struct {
	mu       sync.Mutex
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

func (l *sessionLogger) ensureLocked() {
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
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ensureLocked()
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

func (l *sessionLogger) recordPermission(audit tool.ApprovalAudit) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ensureLocked()
	if l.recorder == nil {
		return
	}
	record := session.PermissionRecord{
		ToolCallID: audit.ToolCallID,
		Tool:       audit.Tool,
		Target:     audit.Target,
		Decision:   audit.Decision,
		Source:     audit.Source,
		Reason:     audit.Reason,
		// 请求为什么需要审批，和用户的决策原因分开记录。
		RequestReason: audit.RequestReason,
		RequestDetail: audit.RequestDetail,
		Prefixes:      clonePermissionPrefixes(audit.Prefixes),
		Origin:        audit.Origin.ID,
	}
	for _, path := range audit.Paths {
		record.Paths = append(record.Paths, session.PermissionPath{Path: path.Path, Kind: string(path.Kind)})
	}
	for _, grant := range audit.SessionCommands {
		record.SessionCommands = append(record.SessionCommands, session.PermissionCommandGrant{
			CWD:       grant.WorkDir,
			Prefix:    append([]string(nil), grant.Prefix...),
			Argv:      append([]string(nil), grant.Argv...),
			Redirects: append([]string(nil), grant.Redirects...),
		})
	}
	for _, path := range audit.SessionPaths {
		record.SessionPaths = append(record.SessionPaths, session.PermissionPath{Path: path.Path, Kind: string(path.Kind)})
	}
	if err := l.recorder.RecordPermission(record); err != nil {
		l.warn("写入权限记录", err)
		_ = l.recorder.Close()
		l.recorder = nil
		l.disabled = true
	}
}

func (l *sessionLogger) recordMemory(record session.MemoryRecord) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ensureLocked()
	if l.recorder == nil {
		return
	}
	recorder, ok := l.recorder.(interface {
		RecordMemory(session.MemoryRecord) error
	})
	if !ok {
		return
	}
	if err := recorder.RecordMemory(record); err != nil {
		l.warn("写入记忆记录", err)
		_ = l.recorder.Close()
		l.recorder = nil
		l.disabled = true
	}
}

func clonePermissionPrefixes(prefixes [][]string) [][]string {
	if len(prefixes) == 0 {
		return nil
	}
	cloned := make([][]string, len(prefixes))
	for i, prefix := range prefixes {
		cloned[i] = append([]string(nil), prefix...)
	}
	return cloned
}

// setStderr 切换警告输出。界面运行期间警告进入界面队列；子任务的后台 goroutine
// 也可能同时写警告，因此需要加锁。
func (l *sessionLogger) setStderr(w io.Writer) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.stderr = w
	l.mu.Unlock()
}

func (l *sessionLogger) close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.recorder == nil {
		return
	}
	recorder := l.recorder
	l.recorder = nil
	// 关闭后不再重建同一会话文件：迟到的记录只丢弃，不打印误导性的创建失败警告。
	l.disabled = true
	if err := recorder.Close(); err != nil {
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
  johnny            在终端进入 TUI 交互模式，默认逐次审批，直到 /exit 或 EOF
  johnny --read     只读模式
  johnny --skip     放行模式，跳过逐次审批

选项：
  -h, --help        显示帮助
  --version         显示版本和提交号
  --model 名称      临时覆盖模型名（选项放在 johnny 之后）
  --api 协议        anthropic-messages、openai-responses、openai-chat-completions
  --read            只读模式：拒绝写入，bash 只允许固定查询
  --skip            放行模式：工具直接执行，不逐次审批
  --permission-mode 模式  等价的长写法：default、read-only 或 allow；与 --read/--skip 同时使用时必须一致
  --max-turns 次数  每轮用户输入最多模型请求次数（默认 %d，必须是正整数）

交互模式：
  只支持在终端直接运行 johnny 进入 TUI；参数任务、管道和重定向会被拒绝。
  每轮输入一个任务；空行不发送请求；输入完整的 /exit 或按 Ctrl+D（EOF）退出。
  /permissions 查看固定权限名单、项目规则和本会话授权；支持 remove-command、remove-path、clear、clear-session。
  /skills 查看已加载 Skill；/skills reload 重新扫描；/skill <name> [参数] 显式使用。
  /context 查看上下文估算、预算、摘要、缓存指标和工具输出存档。
  /compact 手动压缩较早的已完成轮次；自动压缩在接近窗口上限时触发。
  /memory 查看用户级和项目级 MEMORY.md 索引。
  /remember [内容] 让模型起草或更新项目 AGENTS，确认后写入。
  Enter 发送，Alt+Enter 换行，多行粘贴后手动发送，↑↓ 浏览本次输入历史。
  Ctrl+C 取消本轮并等待清理后继续输入；空闲时 Ctrl+C 清空输入。
  TUI 审批提供允许本次、拒绝本次、本会话内允许、批准并记录；默认焦点在拒绝。
  本会话内允许只在当前 johnny 进程内有效，主 Agent、子任务和 workflow 任务共享；等待审批时 Ctrl+C 取消本轮。
  每轮独立计算模型请求次数；单轮失败会说明原因并继续等待输入。
上下文预算：
  JOHNNY_CONTEXT_WINDOW 模型上下文窗口，默认 1000000
  JOHNNY_MAX_OUTPUT_TOKENS 每次请求输出预留，默认 4096
  JOHNNY_MEMORY_AUTO=off 关闭轮末自动记忆审查
  用户级 ~/.johnny/AGENTS.md 和项目根 AGENTS.md 在会话启动和压缩后加载
  单次工具结果模型可见预览最多 32 KiB；bash 大输出保存到 .johnny/artifacts/<会话ID>/
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
