package contextview

import (
	"unicode/utf8"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

// EstimateText 使用保守估算：ASCII 约 4 字节/token，非 ASCII 约 1 字符/token。
// 不实现精确 tokenizer；实际用量会通过 ObserveUsage 校准。
func EstimateText(text string) int {
	if text == "" {
		return 0
	}
	units := 0
	for _, r := range text {
		if r < utf8.RuneSelf {
			units++
		} else {
			units += 4
		}
	}
	return (units + 3) / 4
}

// EstimateMessages 估算消息列表，包含少量角色和块结构开销。
func EstimateMessages(messages []model.Message) int {
	total := 0
	for _, message := range messages {
		total += 4
		for _, block := range message.Content {
			total += 4
			total += EstimateText(block.Text)
			if block.ToolCall != nil {
				total += EstimateText(string(block.ToolCall.Arguments)) + 8
			}
			if block.ToolResult != nil {
				total += EstimateText(block.ToolResult.Content) + 4
			}
			if block.Replay != nil {
				total += EstimateText(string(block.Replay.Data)) + 8
			}
		}
	}
	return total
}

// EstimateTools 估算工具定义。工具顺序和 Schema 在会话内保持不变。
func EstimateTools(tools []model.ToolDefinition) int {
	total := 0
	for _, tool := range tools {
		total += EstimateText(tool.Name) + EstimateText(tool.Description) + EstimateText(string(tool.InputSchema)) + 8
	}
	return total
}

// EstimateRequest 估算 system、工具定义和消息的输入占用。
func EstimateRequest(system string, tools []model.ToolDefinition, messages []model.Message) int {
	return EstimateText(system) + EstimateTools(tools) + EstimateMessages(messages)
}
