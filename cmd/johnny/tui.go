package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/agent"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/skill"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tui"
	"github.com/charmbracelet/x/term"
)

func useTUI(input io.Reader, output io.Writer) bool {
	in, inOK := input.(*os.File)
	out, outOK := output.(*os.File)
	return inOK && outOK && term.IsTerminal(in.Fd()) && term.IsTerminal(out.Fd())
}

func runTerminal(ctx context.Context, conversation *agent.Conversation, input io.Reader, output, stderr io.Writer, logs *sessionLogger, modelName, workDir, permissionLabel string, gate *tool.ApprovalGate, localCommand func(string) (localCommandResult, bool), compact func(context.Context, func(agent.Event) error) (string, error), skills *skill.Store) int {
	var tuiLocalCommand func(string) (tui.LocalCommandResult, bool)
	if localCommand != nil {
		tuiLocalCommand = func(line string) (tui.LocalCommandResult, bool) {
			result, handled := localCommand(line)
			return tui.LocalCommandResult{Output: result.Output, Prompt: result.Prompt, Compact: result.Compact}, handled
		}
	}
	var tuiSlashCommands func() []tui.SlashCommand
	if skills != nil {
		tuiSlashCommands = func() []tui.SlashCommand { return slashCommands(skills) }
	}
	var tuiCompact func(context.Context, func(string)) (string, error)
	if compact != nil {
		tuiCompact = func(runCtx context.Context, notice func(string)) (string, error) {
			logs.stderr = noticeWriter{notice: notice}
			return compact(runCtx, func(event agent.Event) error {
				switch event.Type {
				case agent.EventNotice:
					if event.Notice != "" {
						notice(event.Notice)
					}
				case agent.EventSummary:
					if event.Summary != nil {
						notice(fmt.Sprintf("上下文摘要已更新：估算 %d → %d token；输入 /context 查看摘要。", event.Summary.Before, event.Summary.After))
					}
				}
				return nil
			})
		}
	}
	// 运行中的日志提示进入 UI 队列，避免直接写 stderr 破坏正在重绘的输入区。
	// Run 等待所有任务退出后才返回，此时关闭日志的警告可回到普通 stderr。
	defer func() { logs.stderr = stderr }()
	err := tui.Run(ctx, tui.Options{
		Input: input, Output: output, Model: modelName, WorkDir: workDir, Version: version, Mode: permissionLabel,
		Gate: gate, LocalCommand: tuiLocalCommand, SlashCommands: tuiSlashCommands, RunCompact: tuiCompact,
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

// slashCommands 返回斜杠菜单项目：先列 Skill，再列内置命令。
// 与内置命令同名的 Skill 不作为独立命令展示，但仍可用 /skill <name>。
func slashCommands(skills *skill.Store) []tui.SlashCommand {
	reserved := map[string]bool{
		"skills": true, "skill": true, "permissions": true, "context": true, "compact": true, "memory": true, "remember": true, "exit": true,
	}
	commands := make([]tui.SlashCommand, 0, 7)
	for _, item := range skills.Skills() {
		if reserved[item.Name] {
			continue
		}
		commands = append(commands, tui.SlashCommand{
			Command:     item.Name,
			Description: item.Description,
			Source:      item.Source.Label(),
		})
	}
	return append(commands,
		tui.SlashCommand{Command: "skills", Description: "查看已加载、被覆盖和未加载的 Skill", Source: "内置", Builtin: true},
		tui.SlashCommand{Command: "skills reload", Description: "重新扫描项目级和用户级目录", Source: "内置", Builtin: true},
		tui.SlashCommand{Command: "skill", Description: "显式使用 Skill：/skill <name> [参数]", Source: "内置", Builtin: true},
		tui.SlashCommand{Command: "permissions", Description: "查看固定权限名单和项目规则", Source: "内置", Builtin: true},
		tui.SlashCommand{Command: "context", Description: "查看上下文估算、预算、摘要和存档", Source: "内置", Builtin: true},
		tui.SlashCommand{Command: "compact", Description: "手动压缩较早的已完成轮次", Source: "内置", Builtin: true},
		tui.SlashCommand{Command: "memory", Description: "查看用户级和项目级记忆索引", Source: "内置", Builtin: true},
		tui.SlashCommand{Command: "remember", Description: "让模型起草或更新项目 AGENTS，确认后写入", Source: "内置", Builtin: true},
		tui.SlashCommand{Command: "exit", Description: "退出当前会话", Source: "内置", Builtin: true},
	)
}

func terminalEvent(event agent.Event) (tui.Event, bool) {
	converted := tui.Event{Kind: event.Type}
	switch event.Type {
	case agent.EventResponse:
		converted.Final = event.Response != nil && event.Response.StopReason == model.StopEndTurn
		return converted, true
	case agent.EventRequestStart, agent.EventEnd:
		return converted, true
	case agent.EventNotice:
		converted.Text = event.Notice
		return converted, true
	case agent.EventSummary:
		if event.Summary != nil {
			converted.Kind = agent.EventNotice
			converted.Text = fmt.Sprintf("上下文摘要已更新：估算 %d → %d token；输入 /context 查看摘要。", event.Summary.Before, event.Summary.After)
			return converted, true
		}
		return converted, false
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
			if event.Artifact != "" {
				detail += "\n[存档] " + event.Artifact
			}
			converted.Text = detail
		}
	default:
		return converted, false
	}
	return converted, true
}
