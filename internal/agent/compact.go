package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

// ContextStats 返回当前请求视图的估算占用，供 /context 使用。
func (c *Conversation) ContextStats() contextview.Stats {
	if c == nil || c.view == nil {
		return contextview.Stats{}
	}
	var definitions []model.ToolDefinition
	if c.options.Tools != nil {
		definitions = c.options.Tools.Definitions()
	}
	return c.view.Stats(c.options.System, definitions)
}

// Compact 手动压缩已完成的历史。它不受自动压缩的 thrashing 熔断影响。
func (c *Conversation) Compact(ctx context.Context, emit func(Event) error) (string, error) {
	if c == nil || c.view == nil {
		return "", errors.New("会话不能为空")
	}
	opts := c.options
	if opts.Client == nil || opts.Tools == nil || strings.TrimSpace(opts.Model) == "" {
		return "", errors.New("模型客户端、工具和模型名必须提供")
	}
	if opts.MaxOutputTokens == 0 {
		opts.MaxOutputTokens = 4096
	}
	if opts.RequestTimeout == 0 {
		opts.RequestTimeout = DefaultRequestTimeout
	}
	definitions := opts.Tools.Definitions()
	before := c.view.Estimate(opts.System, definitions)
	if input, _ := c.view.BuildSummaryInput(contextview.DefaultRetainTurns); len(input) == 0 {
		all, _ := c.view.BuildSummaryInput(0)
		if contextview.EstimateMessages(all) < contextview.CompactMinSavedTokens && !c.view.NeedsRequestCompaction(opts.System, definitions) {
			return "没有足够大的已完成历史可压缩。", nil
		}
	}
	notify := func(event Event) error {
		event.UserTurn = c.userTurn
		if emit == nil {
			return nil
		}
		return emit(event)
	}
	if err := c.compact(ctx, opts, definitions, contextview.DefaultRetainTurns, true, notify); err != nil {
		return "", err
	}
	after := c.view.Estimate(opts.System, definitions)
	if after >= before {
		return "没有可压缩的历史。", nil
	}
	if c.view.SummaryIsNote() {
		return fmt.Sprintf("上下文已裁剪：估算 %d → %d token（摘要失败或仍超限，已使用确定性裁剪）。", before, after), nil
	}
	return fmt.Sprintf("上下文已压缩：估算 %d → %d token。", before, after), nil
}

// compact 执行一次极限态压缩。auto 为 false 时检查 thrashing 熔断；force 允许手动
// 压缩不受阈值影响。
func (c *Conversation) compact(ctx context.Context, opts Options, definitions []model.ToolDefinition, retain int, force bool, notify func(Event) error) error {
	if c == nil || c.view == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	emit := func(event Event) error {
		if notify == nil {
			return nil
		}
		return notify(event)
	}
	before := c.view.Estimate(opts.System, definitions)
	if !force {
		if !c.view.CanAutoCompact() {
			return c.fallbackCompaction(ctx, opts, definitions, "自动压缩已熔断", emit)
		}
		if !c.view.NoteAutoTrigger() {
			return c.fallbackCompaction(ctx, opts, definitions, "压缩后上下文快速回填，已停止自动压缩", emit)
		}
	}
	_ = emit(Event{Type: EventNotice, Notice: fmt.Sprintf("正在生成上下文摘要（估算 %d token）…", before)})
	reason := "没有可摘要的旧轮次"
	for attempt := 0; attempt < 2; attempt++ {
		input, previous := c.view.BuildSummaryInput(retain)
		if len(input) == 0 {
			if retain != 0 {
				retain = 0
				continue
			}
			break
		}
		attemptBefore := c.view.Estimate(opts.System, definitions)
		summary, err := c.summarize(ctx, opts, input, previous)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			c.view.RecordSummaryResult(false, 0)
			_ = emit(Event{Type: EventNotice, Notice: "上下文摘要失败，降级为确定性裁剪：" + err.Error()})
			reason = "摘要失败"
			break
		}
		if !c.view.ApplySummary(summary, retain) {
			break
		}
		after := c.view.Estimate(opts.System, definitions)
		c.view.RecordSummaryResult(true, after)
		_ = emit(Event{Type: EventSummary, Summary: &SummaryInfo{
			Text:           summary,
			Before:         attemptBefore,
			After:          after,
			CoveredThrough: c.view.SummaryCovered(),
		}})
		if !c.view.NeedsRequestCompaction(opts.System, definitions) {
			return nil
		}
		retain = 0
	}
	return c.fallbackCompaction(ctx, opts, definitions, reason, emit)
}

func (c *Conversation) fallbackCompaction(ctx context.Context, opts Options, definitions []model.ToolDefinition, reason string, emit func(Event) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.view.DeterministicTrim(0, reason) {
		_ = emit(Event{Type: EventNotice, Notice: "已使用确定性裁剪保留最近轮次；完整记录仍在本地会话日志中。"})
	}
	if c.view.NeedsRequestCompaction(opts.System, definitions) {
		c.view.EmergencyCompactCurrent()
	}
	if c.view.NeedsRequestCompaction(opts.System, definitions) {
		estimate := c.view.Estimate(opts.System, definitions)
		return fmt.Errorf("%w：估算 %d token，可用输入预算 %d token", ErrContextExceeded, estimate, c.view.Budget().Usable)
	}
	return nil
}

func (c *Conversation) summarize(ctx context.Context, opts Options, messages []model.Message, previous string) (string, error) {
	timeout := opts.RequestTimeout
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	maxOutput := opts.MaxOutputTokens
	if maxOutput <= 0 || maxOutput > 4096 {
		maxOutput = 4096
	}
	requestMessages := append([]model.Message(nil), messages...)
	requestMessages = append(requestMessages, model.Message{
		Role:    model.RoleUser,
		Content: []model.ContentBlock{{Type: model.BlockText, Text: summaryInstruction(previous, opts.InteractionMode)}},
	})
	summaryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	response, err := opts.Client.Stream(summaryCtx, model.Request{
		Model:           opts.Model,
		System:          summarySystemPrompt,
		Messages:        requestMessages,
		MaxOutputTokens: maxOutput,
	}, nil)
	if err != nil {
		return "", err
	}
	if response.StopReason != model.StopEndTurn {
		return "", fmt.Errorf("摘要请求未正常结束（%s）", response.StopReason)
	}
	calls, err := responseCalls(response.Message, map[string]bool{})
	if err != nil {
		return "", err
	}
	if len(calls) > 0 {
		return "", errors.New("摘要请求返回了工具调用")
	}
	text := assistantText(response.Message)
	return extractSummary(text)
}

func assistantText(message model.Message) string {
	var builder strings.Builder
	for _, block := range message.Content {
		if block.Type != model.BlockText || block.Text == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(block.Text)
	}
	return builder.String()
}

func extractSummary(text string) (string, error) {
	lower := strings.ToLower(text)
	start := strings.LastIndex(lower, "<summary>")
	if start >= 0 {
		end := strings.LastIndex(lower, "</summary>")
		if end <= start {
			return "", errors.New("摘要缺少 </summary> 结束标记")
		}
		summary := strings.TrimSpace(text[start+len("<summary>") : end])
		if summary == "" {
			return "", errors.New("摘要为空")
		}
		return summary, nil
	}
	if strings.Contains(lower, "<analysis>") {
		return "", errors.New("摘要只包含 analysis，缺少 summary")
	}
	return "", errors.New("摘要缺少 <summary> 标记")
}

const summarySystemPrompt = `你是 Johnny-Code 的上下文压缩器。你的任务是把较早的 coding agent 对话压缩成一份可继续工作的摘要。
你不能调用工具，也不能要求执行命令。只输出文本。
先输出 <analysis> 进行整理，再输出 <summary>。运行时只保留 summary，analysis 会被丢弃。
摘要目标 2～4k token，最多 4096 token。优先保留长期约束、禁止事项、已确认决策、文件路径、错误、待办和当前工作状态。
长期约束和用户明确要求的边界只增不减，尽量保留原文。`

func summaryInstruction(previous, interactionMode string) string {
	if strings.TrimSpace(previous) == "" {
		previous = "（无）"
	}
	if strings.TrimSpace(interactionMode) == "" {
		interactionMode = "未提供"
	}
	return fmt.Sprintf(`请根据上面的对话历史生成压缩摘要。当前交互模式：%s。上一次摘要如下：

<previous_summary>
%s
</previous_summary>

<summary> 开头先单独写出“当前交互模式 / 等待用户决定”字段：说明当前正在等待用户确认什么、下一步由谁决定；如果没有，写“无”。随后写“历史摘要”小节，保留上一次摘要中仍然有效的约束、决策和待办，避免摘要套摘要丢失信息。最后包含以下九段：
1. 用户请求、意图与长期约束
2. 关键概念、方案与已确认决策
3. 文件、代码与产物（路径、修改目的、关键片段）
4. 错误、失败与修复
5. 问题解决、验证与当前状态
6. 用户消息原文（安全/约束类保留原文，普通消息保留关键句）
7. 待办事项
8. 摘要截止点的工作状态
9. 与最近轮次的衔接：哪些内容被摘要，最近保留轮次可能需要什么上下文

只输出 <analysis>...</analysis> 和 <summary>...</summary>，不要输出其他内容。`, interactionMode, previous)
}
