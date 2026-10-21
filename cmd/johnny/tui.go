package main

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tui"
	"github.com/charmbracelet/x/term"
)

func useTUI(input io.Reader, output io.Writer) bool {
	in, inOK := input.(*os.File)
	out, outOK := output.(*os.File)
	return inOK && outOK && term.IsTerminal(in.Fd()) && term.IsTerminal(out.Fd())
}

func runTerminal(ctx context.Context, conversation *agent.Conversation, input io.Reader, output, stderr io.Writer, logs *sessionLogger, modelName, workDir string) int {
	// 运行中的日志提示进入 UI 队列，避免直接写 stderr 破坏正在重绘的输入区。
	// Run 等待所有任务退出后才返回，此时关闭日志的警告可回到普通 stderr。
	defer func() { logs.stderr = stderr }()
	err := tui.Run(ctx, tui.Options{
		Input: input, Output: output, Model: modelName, WorkDir: workDir, Version: version,
		RunTurn: func(turnCtx context.Context, prompt string, emit func(tui.Event) error, notice func(string)) (agent.Result, error) {
			logs.stderr = noticeWriter{notice: notice}
			return conversation.Run(turnCtx, prompt, func(event agent.Event) error {
				logs.record(event)
				if displayEvent, ok := terminalEvent(event); ok {
					return emit(displayEvent)
				}
				return nil
			})
		},
	})
	if err != nil {
		return reportError(stderr, "终端界面失败：", err)
	}
	return 0
}

type noticeWriter struct{ notice func(string) }

func (w noticeWriter) Write(data []byte) (int, error) {
	w.notice(string(data))
	return len(data), nil
}

func terminalEvent(event agent.Event) (tui.Event, bool) {
	converted := tui.Event{Kind: event.Type}
	switch event.Type {
	case agent.EventResponse:
		converted.Final = event.Response != nil && event.Response.StopReason == model.StopEndTurn
		return converted, true
	case agent.EventRequestStart, agent.EventEnd:
		return converted, true
	case agent.EventModel:
		if event.ModelEvent == nil || event.ModelEvent.Type != model.EventBlockDelta || event.ModelEvent.Block.Type != model.BlockText || event.ModelEvent.Delta == "" {
			return converted, false
		}
		converted.Text = event.ModelEvent.Delta
	case agent.EventToolStart:
		if event.Call != nil {
			converted.ID = event.Call.ID
			converted.Name = event.Call.Name
			key, truncated := limitBytes(toolKey(event.Call), previewMaxKeyBytes)
			if truncated {
				key += "…（参数已截断）"
			}
			converted.Key = key
		}
	case agent.EventToolEnd, agent.EventToolSkipped:
		if event.Call != nil {
			converted.ID = event.Call.ID
			converted.Name = event.Call.Name
		}
		if event.ToolResult != nil {
			converted.Failed = event.ToolResult.IsError
			detail, truncated := limitPreview(event.ToolResult.Content)
			if !converted.Failed && event.Call != nil {
				switch event.Call.Name {
				case "read":
					detail = "已读取文件内容"
					truncated = false
				case "write", "edit":
					detail, _, _ = strings.Cut(detail, "\n")
					truncated = false
				}
			}
			if truncated {
				detail += "\n…（预览已截断）"
			}
			converted.Text = detail
		}
	default:
		return converted, false
	}
	return converted, true
}
