package compact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"johnnycode/internal/conversation"
	"johnnycode/internal/llm"
	"johnnycode/internal/session"
)

const (
	summaryOutputReserve = 20000

	autoCompactSafetyMargin = 13000

	manualCompactSafetyMargin = 3000
)

const (
	keepRecentTokens = 10000

	minKeepMessages = 5

	keepMaxTokens = 40000
)

func computeCompactThreshold(contextWindow, maxOutput int, manual bool) int {
	reserve := summaryOutputReserve
	if maxOutput > 0 && maxOutput < reserve {
		reserve = maxOutput
	}
	effectiveWindow := contextWindow - reserve
	margin := autoCompactSafetyMargin
	if manual {
		margin = manualCompactSafetyMargin
	}
	return effectiveWindow - margin
}

const MaxConsecutiveAutoCompactFailures = 3

type AutoCompactTrackingState struct {
	ConsecutiveFailures int
}

const summarySystemPrompt = `Your task is to create a detailed summary of the conversation so far, paying close attention to the user's explicit requests and your previous actions.
This summary should be thorough in capturing technical details, code patterns, and architectural decisions that would be essential for continuing development work without losing context.

Before providing your final summary, wrap your analysis in <analysis> tags to organize your thoughts and ensure you've covered all necessary points. In your analysis process:

1. Chronologically analyze each message and section of the conversation. For each section thoroughly identify:
   - The user's explicit requests and intents
   - Your approach to addressing the user's requests
   - Key decisions, technical concepts and code patterns
   - Specific details like file names, code snippets, function signatures, file edits
   - Errors that you ran into and how you fixed them
   - Specific user feedback received, especially corrections
2. Double-check for technical accuracy and completeness.

After your analysis, output your final summary wrapped in <summary> tags. The summary MUST preserve:
- All file paths that were read, modified, or created
- Key decisions and their rationale
- The current task/goal and overall progress
- Any pending work or next steps
- Error states and their resolution
- Important code snippets or patterns discussed

Output structure:

<analysis>
[Your thought process, ensuring all points are covered thoroughly and accurately]
</analysis>

<summary>
[The final compact summary that retains all actionable context]
</summary>`

func EstimateTokens(messages []conversation.Message) int {
	total := 0
	for _, m := range messages {
		total += int(float64(len(m.Content))/3.5) + 4
		for _, tu := range m.ToolUses {
			argsJSON, _ := json.Marshal(tu.Arguments)
			total += 50 + int(float64(len(argsJSON))/3.5)
		}
		for _, tr := range m.ToolResults {
			total += int(float64(len(tr.Content))/3.5) + 10
		}
		for _, tb := range m.ThinkingBlocks {
			total += int(float64(len(tb.Thinking)) / 3.5)
		}
	}
	return total
}

type UsageAnchor struct {
	BaselineTokens int
	AnchorCount    int
	HasUsage       bool
}

func BaselineFromUsage(u llm.UsageInfo) int {
	return u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens + u.OutputTokens
}

func ComputeUsedTokens(messages []conversation.Message, anchor UsageAnchor) int {
	if !anchor.HasUsage {
		return EstimateTokens(messages)
	}

	if anchor.AnchorCount < 0 || anchor.AnchorCount > len(messages) {
		return EstimateTokens(messages)
	}
	return anchor.BaselineTokens + EstimateTokens(messages[anchor.AnchorCount:])
}

func ManageContext(
	ctx context.Context,
	conv *conversation.Manager,
	client llm.Client,
	workDir string,
	sessionID string,
	contextWindow int,
	maxOutput int,
	tracking *AutoCompactTrackingState,
	recovery *RecoveryState,
	toolSchemas []map[string]any,
	anchor UsageAnchor,
) (string, error) {
	tokens := ComputeUsedTokens(conv.GetMessages(), anchor)

	if tokens < computeCompactThreshold(contextWindow, maxOutput, false) {
		return "", nil
	}

	if tokens >= computeCompactThreshold(contextWindow, maxOutput, true) {
		return ForceCompact(ctx, conv, client, workDir, sessionID, contextWindow, recovery, toolSchemas)
	}

	if tracking != nil && tracking.ConsecutiveFailures >= MaxConsecutiveAutoCompactFailures {
		return "", nil
	}

	msg, err := autoCompact(ctx, conv, client, workDir, sessionID, contextWindow, recovery, toolSchemas)
	if err != nil {
		if tracking != nil {
			tracking.ConsecutiveFailures++
		}
		return "", err
	}
	if tracking != nil {
		tracking.ConsecutiveFailures = 0
	}
	return msg, nil
}

func ForceCompact(
	ctx context.Context,
	conv *conversation.Manager,
	client llm.Client,
	workDir string,
	sessionID string,
	contextWindow int,
	recovery *RecoveryState,
	toolSchemas []map[string]any,
) (string, error) {
	return autoCompact(ctx, conv, client, workDir, sessionID, contextWindow, recovery, toolSchemas)
}

func hasToolResults(m conversation.Message) bool {
	return len(m.ToolResults) > 0
}

func hasToolUses(m conversation.Message) bool {
	return len(m.ToolUses) > 0
}

func snapKeepStartAcrossToolPairs(messages []conversation.Message, keepStart int) int {
	for keepStart > 0 && hasToolResults(messages[keepStart]) {
		prev := keepStart - 1
		if hasToolUses(messages[prev]) {
			keepStart = prev
			continue
		}
		break
	}
	return keepStart
}

func computeKeepStartIndex(messages []conversation.Message) int {
	n := len(messages)
	if n == 0 {
		return 0
	}

	keptTokens := 0
	keptCount := 0
	keepStart := n
	for i := n - 1; i >= 0; i-- {
		msgTokens := EstimateTokens(messages[i : i+1])

		if keptCount > 0 && keptTokens+msgTokens > keepMaxTokens {
			break
		}
		keptTokens += msgTokens
		keptCount++
		keepStart = i

		if keptTokens >= keepRecentTokens || keptCount >= minKeepMessages {
			break
		}
	}

	return snapKeepStartAcrossToolPairs(messages, keepStart)
}

func autoCompact(
	ctx context.Context,
	conv *conversation.Manager,
	client llm.Client,
	workDir string,
	sessionID string,
	contextWindow int,
	recovery *RecoveryState,
	toolSchemas []map[string]any,
) (string, error) {
	messages := conv.GetMessages()
	beforeTokens := EstimateTokens(messages)

	keepStart := computeKeepStartIndex(messages)
	if keepStart <= 0 {

		return "", nil
	}

	const maxSummaryRetries = 2
	var (
		finalSummary string
		summaryErr   error
	)

	for retry := 0; retry <= maxSummaryRetries; retry++ {
		if retry > 0 {

			newStart := snapKeepStartAcrossToolPairs(messages, keepStart/2)
			if newStart == 0 {
				break
			}
			keepStart = newStart
		}

		prefix := messages[:keepStart]

		var sb strings.Builder
		for _, m := range prefix {
			sb.WriteString(fmt.Sprintf("[%s]: %s\n", m.Role, m.Content))
			for _, tu := range m.ToolUses {
				sb.WriteString(fmt.Sprintf("[tool_use %s]: %s\n", tu.ToolName, tu.ToolUseID))
			}
			for _, tr := range m.ToolResults {
				content := tr.Content
				if len(content) > 500 {
					content = content[:500] + "..."
				}
				sb.WriteString(fmt.Sprintf("[tool_result]: %s\n", content))
			}
		}

		summaryConv := conversation.NewManager()
		summaryConv.AddUserMessage(summarySystemPrompt + "\n\n" + sb.String())

		events, errs := client.Stream(ctx, summaryConv, nil)
		var summary strings.Builder
		for ev := range events {
			if td, ok := ev.(llm.TextDelta); ok {
				summary.WriteString(td.Text)
			}
		}
		select {
		case err := <-errs:
			summaryErr = err
		default:
			summaryErr = nil
		}

		if summaryErr == nil {
			finalSummary = formatCompactSummary(summary.String())
			break
		}

		var ctxErr *llm.ContextTooLongError
		if !errors.As(summaryErr, &ctxErr) {
			return "", summaryErr
		}
	}

	if finalSummary == "" {

		keepStart = len(messages)
		for keepStart > 0 && len(messages)-keepStart < minKeepMessages {
			keepStart--
		}
		keepStart = snapKeepStartAcrossToolPairs(messages, keepStart)
		finalSummary = "（早期对话上下文因超出窗口限制已丢弃，未生成摘要。）"
	}

	keep := messages[keepStart:]

	if sessionID != "" && workDir != "" {
		keepRecords := make([]session.KeepMessage, 0, len(keep))
		for _, m := range keep {
			keepRecords = append(keepRecords, session.KeepMessage{Role: m.Role, Content: m.Content})
		}
		session.SaveCompactBoundary(workDir, sessionID, finalSummary, keepRecords)
	}

	content := "本次会话延续自之前的对话，因上下文空间不足进行了压缩。以下是早期对话的摘要：\n\n" + finalSummary
	if len(keep) > 0 {
		content += "\n\n近期消息已原样保留。"
	}
	if sessionID != "" && workDir != "" {
		content += fmt.Sprintf("\n\n如果你需要压缩前的具体细节（代码片段、报错信息等），请用 ReadFile 读取完整会话记录：%s", session.SessionFilePath(workDir, sessionID))
	}
	if attachment := BuildRecoveryAttachment(recovery, toolSchemas); attachment != "" {
		content += "\n\n---\n\n" + attachment
	}

	compacted := conversation.NewManager()
	compacted.AddUserMessage(content)
	compacted.AppendMessages(keep)

	*conv = *compacted
	afterTokens := EstimateTokens(conv.GetMessages())
	return fmt.Sprintf("Compacted: %d → %d estimated tokens", beforeTokens, afterTokens), nil
}

func formatCompactSummary(raw string) string {
	if start := strings.Index(raw, "<summary>"); start >= 0 {
		body := raw[start+len("<summary>"):]
		if end := strings.Index(body, "</summary>"); end >= 0 {
			return strings.TrimSpace(body[:end])
		}
		return strings.TrimSpace(body)
	}

	if start := strings.Index(raw, "<analysis>"); start >= 0 {
		if end := strings.Index(raw, "</analysis>"); end > start {
			return strings.TrimSpace(raw[:start] + raw[end+len("</analysis>"):])
		}
	}
	return strings.TrimSpace(raw)
}
