package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"johnnycode/internal/compact"
	"johnnycode/internal/conversation"
	"johnnycode/internal/filehistory"
	"johnnycode/internal/hooks"
	"johnnycode/internal/llm"
	"johnnycode/internal/permissions"
	"johnnycode/internal/planfile"
	"johnnycode/internal/prompt"
	"johnnycode/internal/session"
	"johnnycode/internal/toolresult"
	"johnnycode/internal/tools"
)

const (
	maxTokensCeiling          = 64000
	maxOutputTokensRecoveries = 3
)

type Agent struct {
	Client        llm.Client
	Registry      *tools.Registry
	Protocol      string
	WorkDir       string
	MaxIterations int
	ContextWindow int

	MaxOutputTokens int
	Checker         *permissions.Checker
	Hooks           *hooks.Engine

	SessionID      string
	NotificationFn func() []string

	Instructions   string
	MemoryContent  string
	ToolNameFilter func(name string) bool

	OnLoopComplete  func(conv *conversation.Manager)
	FileHistory     *filehistory.History
	compactTracking compact.AutoCompactTrackingState

	ReplacementState *toolresult.ContentReplacementState

	RecoveryState      *compact.RecoveryState
	freshToolResultIDs map[string]struct{}

	activeSkills map[string]string
}

func (a *Agent) ActivateSkill(name, body string) {
	if a.activeSkills == nil {
		a.activeSkills = make(map[string]string)
	}
	a.activeSkills[name] = body
}

func (a *Agent) ClearActiveSkills() {
	a.activeSkills = nil
}

func (a *Agent) GetActiveSkills() map[string]string {
	out := make(map[string]string, len(a.activeSkills))
	for k, v := range a.activeSkills {
		out[k] = v
	}
	return out
}

func (a *Agent) SetToolFilter(allow func(name string) bool) {
	a.ToolNameFilter = allow
}

func (a *Agent) ToolRegistry() *tools.Registry {
	return a.Registry
}

func New(client llm.Client, registry *tools.Registry, protocol string) *Agent {
	wd, _ := os.Getwd()
	return &Agent{
		Client:             client,
		Registry:           registry,
		Protocol:           protocol,
		WorkDir:            wd,
		MaxIterations:      0,
		ContextWindow:      200000,
		ReplacementState:   toolresult.New(),
		RecoveryState:      compact.NewRecoveryState(),
		freshToolResultIDs: make(map[string]struct{}),
	}
}

func (a *Agent) SetSessionID(id string) { a.SessionID = id }

func (a *Agent) currentToolSchemas() []map[string]any {
	schemas := a.Registry.GetAllSchemas(a.Protocol)
	if a.ToolNameFilter == nil {
		return schemas
	}
	allow := func(name string) bool {
		if t := a.Registry.Get(name); t != nil && tools.IsSystemTool(t) {
			return true
		}
		return a.ToolNameFilter(name)
	}
	return filterSchemasByName(schemas, allow)
}

func (a *Agent) Run(ctx context.Context, conv *conversation.Manager) <-chan AgentEvent {
	ch := make(chan AgentEvent, 32)

	go func() {
		defer close(ch)
		defer a.emitHook(hooks.EventSessionEnd, "", nil)

		a.emitHook(hooks.EventSessionStart, "", nil)

		conv.InjectLongTermMemory(a.Instructions, a.MemoryContent)

		var totalInput, totalOutput int
		consecutiveUnknown := 0
		maxTokensEscalated := false
		outputRecoveries := 0

		var usageAnchor compact.UsageAnchor

		for iteration := 1; ; iteration++ {
			if a.MaxIterations > 0 && iteration > a.MaxIterations {
				ch <- ErrorEvent{Message: fmt.Sprintf("Agent reached maximum iterations (%d)", a.MaxIterations)}
				return
			}

			if ctx.Err() != nil {
				return
			}

			toolSchemas := a.currentToolSchemas()

			if msg, err := compact.ManageContext(ctx, conv, a.Client, a.WorkDir, a.SessionID, a.ContextWindow, a.MaxOutputTokens, &a.compactTracking, a.RecoveryState, toolSchemas, usageAnchor); err == nil && msg != "" {
				ch <- CompactEvent{Message: msg}

				usageAnchor = compact.UsageAnchor{}
			}

			if a.Checker != nil && a.Checker.Mode == permissions.ModePlan {
				planPath := planfile.GetOrCreatePlanPath(a.WorkDir)

				a.Checker.PlanFilePath = planPath
				planExists := planfile.PlanExists(a.WorkDir)
				reminder := prompt.BuildPlanModeReminder(planPath, planExists, iteration)
				conv.AddSystemReminder(reminder)
			}

			if a.NotificationFn != nil {
				for _, note := range a.NotificationFn() {
					conv.AddSystemReminder(note)
				}
			}

			a.emitHook(hooks.EventTurnStart, "", nil)

			if reminder := buildActiveSkillsReminder(a.activeSkills); reminder != "" {
				conv.AddSystemReminder(reminder)
			}

			if deferredTools := a.Registry.GetDeferredToolInfos(); len(deferredTools) > 0 {
				reminder := buildDeferredToolsReminder(deferredTools)
				conv.AddSystemReminder(reminder)
			}

			a.emitHook(hooks.EventPreSend, "", nil)

			apiConv, newRecords, _ := toolresult.ApplyWithFresh(conv, a.WorkDir, a.ReplacementState, a.freshToolResultIDs)
			// A fresh result is exempt from spilling for one API request only.
			// The underlying conversation remains raw, so the next iteration can
			// spill it if it still exceeds the normal result budget.
			a.freshToolResultIDs = make(map[string]struct{})
			if len(newRecords) > 0 {

				if a.SessionID != "" {
					_ = toolresult.AppendRecords(session.SessionRecordsDir(a.WorkDir, a.SessionID), newRecords)
				}
			}

			events, errs := a.Client.Stream(ctx, apiConv, toolSchemas)

			var text string
			var toolCalls []llm.ToolCallComplete
			var thinkingBlocks []conversation.ThinkingBlock
			var stopReason string
			var usage llm.UsageInfo

			executor := NewStreamingExecutor(a.Registry, a.Checker, ch)

			for ev := range events {
				switch e := ev.(type) {
				case llm.ThinkingDelta:
					ch <- ThinkingText{Text: e.Text}
				case llm.ThinkingComplete:
					thinkingBlocks = append(thinkingBlocks, conversation.ThinkingBlock{
						Thinking:  e.Thinking,
						Signature: e.Signature,
					})
				case llm.TextDelta:
					text += e.Text
					ch <- StreamText{Text: e.Text}
				case llm.ToolCallStart:
					ch <- ToolUseEvent{ToolID: e.ToolID, ToolName: e.ToolName}
				case llm.ToolCallDelta:

				case llm.ToolCallComplete:
					toolCalls = append(toolCalls, e)
					ch <- ToolUseEvent{
						ToolID:   e.ToolID,
						ToolName: e.ToolName,
						Args:     e.Arguments,
					}

					executor.Submit(ctx, a, e)
				case llm.StreamEnd:
					stopReason = e.StopReason
					usage = e.Usage
				}
			}
			a.emitHook(hooks.EventPostReceive, text, nil)

			select {
			case err := <-errs:
				if err != nil {
					if retry, compacted := a.handleStreamError(ctx, ch, conv, err); retry {
						if compacted {

							usageAnchor = compact.UsageAnchor{}
						}
						continue
					}
					ch <- ErrorEvent{Message: err.Error()}
					return
				}
			default:
			}

			totalInput += usage.InputTokens
			totalOutput += usage.OutputTokens
			ch <- UsageEvent{InputTokens: totalInput, OutputTokens: totalOutput}

			anchorBaseline := compact.BaselineFromUsage(usage)
			anchorAfterAssistant := func() {
				if anchorBaseline > 0 {
					usageAnchor = compact.UsageAnchor{
						BaselineTokens: anchorBaseline,
						AnchorCount:    conv.Len(),
						HasUsage:       true,
					}
				}
			}

			if stopReason == "max_tokens" {
				if !maxTokensEscalated {

					if setter, ok := a.Client.(llm.MaxTokensSetter); ok {
						setter.SetMaxOutputTokens(maxTokensCeiling)
						maxTokensEscalated = true
					}
					if text != "" {
						conv.AddAssistantFull(text, thinkingBlocks, nil)
						anchorAfterAssistant()
						conv.AddUserMessage("Output token limit hit. Resume directly from where you stopped. Do not apologize or repeat previous content. Pick up mid-thought if needed.")
					}
					ch <- RetryEvent{Reason: "max_tokens escalation", Wait: 0}
					continue
				} else if outputRecoveries < maxOutputTokensRecoveries {

					outputRecoveries++
					conv.AddAssistantFull(text, thinkingBlocks, nil)
					anchorAfterAssistant()
					conv.AddUserMessage("Output token limit hit. Resume directly from where you stopped. Break remaining work into smaller pieces.")
					ch <- RetryEvent{Reason: fmt.Sprintf("max_tokens recovery %d/%d", outputRecoveries, maxOutputTokensRecoveries), Wait: 0}
					continue
				}

			} else {

				outputRecoveries = 0
			}

			if len(toolCalls) == 0 {
				conv.AddAssistantFull(text, thinkingBlocks, nil)
				if a.FileHistory != nil {
					summary := text
					if len(summary) > 60 {
						summary = summary[:60] + "..."
					}
					a.FileHistory.MakeSnapshot(conv.Len(), summary)
				}
				ch <- LoopComplete{TotalTurns: iteration}
				if a.OnLoopComplete != nil {
					go a.OnLoopComplete(conv)
				}
				return
			}

			var toolUses []conversation.ToolUseBlock
			for _, tc := range toolCalls {
				toolUses = append(toolUses, conversation.ToolUseBlock{
					ToolUseID: tc.ToolID,
					ToolName:  tc.ToolName,
					Arguments: tc.Arguments,
				})
			}
			conv.AddAssistantFull(text, thinkingBlocks, toolUses)

			anchorAfterAssistant()

			results := executor.CollectResults()

			var toolResults []conversation.ToolResultBlock
			for _, r := range results {
				if r.isUnknown {
					consecutiveUnknown++
				} else {
					consecutiveUnknown = 0
				}

				ch <- ToolResultEvent{
					ToolID:   r.toolID,
					ToolName: r.toolName,
					Output:   r.output,
					IsError:  r.isError,
					Elapsed:  r.elapsed,
				}

				truncated := tools.TruncateOutput(r.output, tools.MaxOutputCharsForContext(a.ContextWindow))
				toolResults = append(toolResults, conversation.ToolResultBlock{
					ToolUseID: r.toolID,
					Content:   truncated,
					IsError:   r.isError,
				})
			}

			if consecutiveUnknown >= 3 {
				ch <- ErrorEvent{Message: "Too many consecutive unknown tool calls"}
				return
			}

			exitPlanCalled := false
			for _, tc := range toolCalls {
				if tc.ToolName == "ExitPlanMode" {
					exitPlanCalled = true
					break
				}
			}
			conv.AddToolResultsMessage(toolResults)
			for _, tr := range toolResults {
				a.freshToolResultIDs[tr.ToolUseID] = struct{}{}
			}
			if exitPlanCalled {
				ch <- TurnComplete{Turn: iteration}
				ch <- LoopComplete{TotalTurns: iteration}
				return
			}
			ch <- TurnComplete{Turn: iteration}
			a.emitHook(hooks.EventTurnEnd, "", nil)
		}
	}()

	return ch
}

func buildDeferredToolsReminder(infos []tools.DeferredToolInfo) string {
	var sb strings.Builder
	sb.WriteString("The following deferred tools are available via ToolSearch. Their full input schemas are NOT loaded yet. Use ToolSearch with query \"select:<name>[,<name>...]\" to load a tool before calling it. The name and description below are available for tool discovery:\n")
	for _, info := range infos {
		sb.WriteString("\n- ")
		sb.WriteString(info.Name)
		sb.WriteString(": ")
		sb.WriteString(info.Description)
	}
	return sb.String()
}

func (a *Agent) emitHook(event hooks.EventName, message string, args map[string]any) {
	if a.Hooks == nil {
		return
	}
	a.Hooks.RunHooks(hooks.HookContext{
		EventName: event,
		ToolArgs:  args,
		Message:   message,
	})
}

func buildActiveSkillsReminder(active map[string]string) string {
	if len(active) == 0 {
		return ""
	}
	names := make([]string, 0, len(active))
	for name := range active {
		names = append(names, name)
	}
	sort.Strings(names)
	var sb strings.Builder
	sb.WriteString("# Active Skills\n\nThe following Skill SOPs are pinned to the environment context. Follow each SOP when its triggering condition applies.\n\n")
	for _, name := range names {
		body := active[name]
		sb.WriteString("## Active Skill: ")
		sb.WriteString(name)
		sb.WriteString("\n\n")
		sb.WriteString(body)
		sb.WriteString("\n\n")
	}
	return sb.String()
}

func filterSchemasByName(schemas []map[string]any, allow func(name string) bool) []map[string]any {
	out := make([]map[string]any, 0, len(schemas))
	for _, s := range schemas {
		name, _ := s["name"].(string)
		if allow(name) {
			out = append(out, s)
		}
	}
	return out
}

func (a *Agent) handleStreamError(ctx context.Context, ch chan AgentEvent, conv *conversation.Manager, err error) (retry, compacted bool) {
	var ctxErr *llm.ContextTooLongError
	if errors.As(err, &ctxErr) {
		msg, compactErr := compact.ForceCompact(ctx, conv, a.Client, a.WorkDir, a.SessionID, a.ContextWindow, a.RecoveryState, a.currentToolSchemas())
		if compactErr == nil && msg != "" {
			ch <- CompactEvent{Message: "Auto-compacted due to context length: " + msg}
			return true, true
		}
		return false, false
	}

	var rlErr *llm.RateLimitError
	if errors.As(err, &rlErr) {
		wait := parseRetryAfter(rlErr.RetryAfter)
		ch <- RetryEvent{Reason: "rate limited", Wait: wait}
		select {
		case <-time.After(wait):
			return true, false
		case <-ctx.Done():
			return false, false
		}
	}

	return false, false
}

func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 5 * time.Second
	}
	if secs, err := strconv.Atoi(header); err == nil {
		return time.Duration(secs) * time.Second
	}
	return 5 * time.Second
}

type toolExecResult struct {
	toolID    string
	toolName  string
	output    string
	isError   bool
	elapsed   time.Duration
	isUnknown bool
}

func extractFilePath(args map[string]any) string {
	for _, key := range []string{"file_path", "path", "pattern", "target"} {
		if v, ok := args[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func (a *Agent) executeSingleTool(ctx context.Context, eventCh chan AgentEvent, tc llm.ToolCallComplete) toolExecResult {
	tool := a.Registry.Get(tc.ToolName)
	start := time.Now()

	if tool == nil {
		return toolExecResult{
			toolID:    tc.ToolID,
			toolName:  tc.ToolName,
			output:    fmt.Sprintf("Error: unknown tool '%s'", tc.ToolName),
			isError:   true,
			elapsed:   time.Since(start),
			isUnknown: true,
		}
	}

	if a.Checker != nil {
		decision := a.Checker.Check(tool, tc.Arguments)
		if decision.Effect == permissions.Deny {
			return toolExecResult{
				toolID:   tc.ToolID,
				toolName: tc.ToolName,
				output:   fmt.Sprintf("Permission denied: %s", decision.Reason),
				isError:  true,
				elapsed:  time.Since(start),
			}
		}
		if decision.Effect == permissions.Ask {
			respCh := make(chan PermissionResponse, 1)
			desc := permissions.ExtractContent(tc.ToolName, tc.Arguments)
			if desc == "" {
				desc = formatToolArgs(tc.Arguments)
			}
			eventCh <- PermissionRequestEvent{
				ToolName:   tc.ToolName,
				Desc:       desc,
				ResponseCh: respCh,
			}
			resp := <-respCh
			if resp == PermDeny {
				return toolExecResult{
					toolID:   tc.ToolID,
					toolName: tc.ToolName,
					output:   "Permission denied by user",
					isError:  true,
					elapsed:  time.Since(start),
				}
			}
			if resp == PermAllowAlways {
				content := permissions.ExtractContent(tc.ToolName, tc.Arguments)

				a.Checker.RuleEngine.AddSessionRule(permissions.Rule{
					ToolName: tc.ToolName,
					Pattern:  content,
					Effect:   permissions.RuleAllow,
				})
			}
		}
	}

	if a.Hooks != nil {
		hctx := hooks.HookContext{
			EventName: hooks.EventPreToolUse,
			ToolName:  tc.ToolName,
			ToolArgs:  tc.Arguments,
			FilePath:  extractFilePath(tc.Arguments),
		}
		if rejected, msg := a.Hooks.RunPreToolHooks(hctx); rejected {
			return toolExecResult{
				toolID:   tc.ToolID,
				toolName: tc.ToolName,
				output:   "Blocked by hook: " + msg,
				isError:  true,
				elapsed:  time.Since(start),
			}
		}
	}

	result := tool.Execute(ctx, tc.Arguments)

	if !result.IsError && tc.ToolName == "ReadFile" {
		if p, _ := tc.Arguments["file_path"].(string); p != "" {
			if data, err := os.ReadFile(p); err == nil {
				a.RecoveryState.RecordFileRead(p, string(data))
			}
		}
	}

	if a.Hooks != nil {
		a.Hooks.RunHooks(hooks.HookContext{
			EventName: hooks.EventPostToolUse,
			ToolName:  tc.ToolName,
			ToolArgs:  tc.Arguments,
			FilePath:  extractFilePath(tc.Arguments),
			Message:   result.Output,
		})
	}

	return toolExecResult{
		toolID:   tc.ToolID,
		toolName: tc.ToolName,
		output:   result.Output,
		isError:  result.IsError,
		elapsed:  time.Since(start),
	}
}

func formatToolArgs(args map[string]any) string {
	var parts []string
	for k, v := range args {
		s := fmt.Sprintf("%v", v)
		if len(s) > 80 {
			s = s[:80] + "…"
		}
		parts = append(parts, fmt.Sprintf("%s: %s", k, s))
	}
	return strings.Join(parts, ", ")
}
