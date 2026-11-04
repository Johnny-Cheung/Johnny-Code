package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

const (
	contextWindowEnv  = "JOHNNY_CONTEXT_WINDOW"
	maxOutputEnv      = "JOHNNY_MAX_OUTPUT_TOKENS"
	requestTimeoutEnv = "JOHNNY_REQUEST_TIMEOUT"
	commandTimeoutEnv = "JOHNNY_BASH_TIMEOUT"
)

type contextConfig struct {
	Window       int
	OutputTokens int
}

func contextConfigFromEnv(lookup func(string) (string, bool)) (contextConfig, error) {
	config := contextConfig{Window: contextview.DefaultWindow, OutputTokens: contextview.DefaultOutputTokens}
	if lookup == nil {
		return config, nil
	}
	if value, ok := lookup(contextWindowEnv); ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || parsed < 32768 {
			return config, fmt.Errorf("%s 必须是不小于 32768 的整数", contextWindowEnv)
		}
		config.Window = parsed
	}
	outputExplicit := false
	if value, ok := lookup(maxOutputEnv); ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || parsed < 1 {
			return config, fmt.Errorf("%s 必须是正整数", maxOutputEnv)
		}
		config.OutputTokens = parsed
		outputExplicit = true
	}
	if !outputExplicit {
		// 默认输出预留提高后，最小窗口 32768 只留下 8192 token 输入预算。
		// 未显式指定输出时按窗口的八分之一封顶，保持小窗口的自动摘要可用；
		// 显式设置 JOHNNY_MAX_OUTPUT_TOKENS 时尊重用户选择。
		if limit := config.Window / 8; limit > 0 && config.OutputTokens > limit {
			config.OutputTokens = limit
		}
	}
	if config.OutputTokens >= config.Window {
		return config, fmt.Errorf("%s 必须小于 %s", maxOutputEnv, contextWindowEnv)
	}
	return config, nil
}

func requestTimeoutFromEnv(lookup func(string) (string, bool)) (time.Duration, error) {
	timeout := agent.DefaultRequestTimeout
	if lookup == nil {
		return timeout, nil
	}
	value, ok := lookup(requestTimeoutEnv)
	if !ok {
		return timeout, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return timeout, fmt.Errorf("%s 必须是大于 0 的时长，例如 10m 或 90s", requestTimeoutEnv)
	}
	return parsed, nil
}

func commandTimeoutFromEnv(lookup func(string) (string, bool)) (time.Duration, error) {
	timeout := tool.DefaultCommandTimeout
	if lookup == nil {
		return timeout, nil
	}
	value, ok := lookup(commandTimeoutEnv)
	if !ok {
		return timeout, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed <= 0 || parsed > tool.MaxCommandTimeout {
		return timeout, fmt.Errorf("%s 必须是大于 0 且不超过 %s 的时长，例如 10m 或 90s", commandTimeoutEnv, tool.MaxCommandTimeout)
	}
	return parsed, nil
}

func contextStatsText(stats contextview.Stats) string {
	if stats.Budget.Window == 0 {
		return "当前会话未启用上下文治理。"
	}
	percent := 0.0
	if stats.Budget.Usable > 0 {
		percent = float64(stats.Input) * 100 / float64(stats.Budget.Usable)
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "上下文估算：%d / %d token（%.1f%% 可用输入预算）\n", stats.Input, stats.Budget.Usable, percent)
	fmt.Fprintf(&builder, "窗口 %d；输出预留 %d；安全余量 %d；可用输入 %d\n",
		stats.Budget.Window, stats.Budget.Output, stats.Budget.Safety, stats.Budget.Usable)
	fmt.Fprintf(&builder, "组成：系统 %d；工具定义 %d；消息 %d\n", stats.SystemTokens, stats.ToolTokens, stats.MessageTokens)
	if stats.HasUsage {
		fmt.Fprintf(&builder, "最近请求：输入 %d；缓存读 %d；缓存写 %d\n", stats.LastInput, stats.CacheRead, stats.CacheWrite)
	}
	fmt.Fprintf(&builder, "轮次：%d；已摘要 %d 轮", stats.Turns, stats.SummaryTurns)
	if stats.CoveredThrough > 0 {
		fmt.Fprintf(&builder, "（覆盖到第 %d 轮）", stats.CoveredThrough)
	}
	builder.WriteString("\n")
	if stats.AutoDisabled {
		builder.WriteString("自动压缩：已熔断（连续失败或快速回填）；仍可用 /compact 手动压缩。\n")
	}
	if stats.Artifacts != "" {
		fmt.Fprintf(&builder, "工具输出存档：%s\n", stats.Artifacts)
	}
	if stats.Summary != "" {
		label := "当前摘要"
		if stats.SummaryNote {
			label = "当前裁剪说明"
		}
		fmt.Fprintf(&builder, "\n%s：\n%s\n", label, stats.Summary)
	} else {
		builder.WriteString("\n当前摘要：无\n")
	}
	return strings.TrimRight(builder.String(), "\n")
}
