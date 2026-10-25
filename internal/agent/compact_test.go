package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func textMessage(role model.Role, text string) model.Message {
	return model.Message{Role: role, Content: []model.ContentBlock{{Type: model.BlockText, Text: text}}}
}

func toolCallMessage(id string) model.Message {
	return model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
		Type:     model.BlockToolCall,
		ToolCall: &model.ToolCall{ID: id, Name: "bash", Arguments: json.RawMessage(`{"command":"demo"}`)},
	}}}
}

func populateTurns(manager *contextview.Manager, count, resultBytes int) {
	for turn := 1; turn <= count; turn++ {
		id := fmt.Sprintf("call-%d", turn)
		manager.BeginTurn(turn, textMessage(model.RoleUser, fmt.Sprintf("task-%d", turn)))
		manager.AppendAssistant(toolCallMessage(id))
		manager.AppendToolResult(turn, model.ToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"demo"}`)}, model.ToolResult{
			ToolCallID: id,
			Content:    strings.Repeat("z", resultBytes),
		})
		manager.AppendAssistant(textMessage(model.RoleAssistant, fmt.Sprintf("done-%d", turn)))
		manager.FinishTurn()
	}
}

func TestLargeToolCallArgumentsAreGoverned(t *testing.T) {
	manager := contextview.NewManager(contextview.Config{
		Budget:       contextview.NewBudget(1_000_000, 4096),
		PreviewBytes: 1024,
	})
	bigArgs := json.RawMessage(`{"command":"` + strings.Repeat("x", 2000) + `"}`)
	var executed int
	tools := &testTools{execute: func(_ context.Context, call model.ToolCall) (tool.Result, error) {
		executed = len(call.Arguments)
		return tool.Result{Content: "ok"}, nil
	}}
	requests := 0
	var taskRequest model.Request
	var startArguments int
	client := clientFunc(func(_ context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
		requests++
		if requests == 1 {
			return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
				Type:     model.BlockToolCall,
				ToolCall: &model.ToolCall{ID: "one", Name: "bash", Arguments: bigArgs},
			}}}, StopReason: model.StopToolUse}, nil
		}
		taskRequest = req
		return finalResponse(), nil
	})
	_, err := NewConversation(Options{
		Client: client, Tools: tools, Model: "model", ContextView: manager,
	}).Run(context.Background(), "task", func(event Event) error {
		if event.Type == EventToolStart && event.Call != nil {
			startArguments = len(event.Call.Arguments)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if executed < 2000 || startArguments < 2000 {
		t.Fatalf("工具应按原始参数执行并记录：executed=%d event=%d", executed, startArguments)
	}
	if len(taskRequest.Messages) != 3 {
		t.Fatalf("请求消息数量错误：%d", len(taskRequest.Messages))
	}
	arguments := taskRequest.Messages[1].Content[0].ToolCall.Arguments
	if len(arguments) >= 2000 || !strings.Contains(string(arguments), "_context_note") || !json.Valid(arguments) {
		t.Fatalf("请求视图未压缩大工具参数：%s", arguments)
	}
}

func TestToolEndEventCarriesArtifact(t *testing.T) {
	dir := t.TempDir()
	manager := contextview.NewManager(contextview.Config{
		Budget:    contextview.NewBudget(1_000_000, 4096),
		WorkDir:   dir,
		SessionID: "artifact-test",
	})
	tools := &testTools{execute: func(context.Context, model.ToolCall) (tool.Result, error) {
		return tool.Result{Content: strings.Repeat("o", 40_000)}, nil
	}}
	requests := 0
	client := clientFunc(func(_ context.Context, _ model.Request, _ func(model.Event) error) (model.Response, error) {
		requests++
		if requests == 1 {
			return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
				Type:     model.BlockToolCall,
				ToolCall: &model.ToolCall{ID: "one", Name: "bash", Arguments: json.RawMessage(`{"command":"demo"}`)},
			}}}, StopReason: model.StopToolUse}, nil
		}
		return finalResponse(), nil
	})
	var artifact string
	_, err := NewConversation(Options{
		Client: client, Tools: tools, Model: "model", ContextView: manager,
	}).Run(context.Background(), "task", func(event Event) error {
		if event.Type == EventToolEnd {
			artifact = event.Artifact
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact == "" {
		t.Fatal("工具结束事件缺少存档路径")
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(artifact))); err != nil {
		t.Fatalf("存档文件不存在：%v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(artifact))); !os.IsNotExist(err) {
		t.Fatal("关闭后应删除存档")
	}
}

func TestExtractSummaryDiscardsAnalysis(t *testing.T) {
	summary, err := extractSummary("<analysis>内部整理</analysis>\n<summary>只保留这里</summary>")
	if err != nil || summary != "只保留这里" {
		t.Fatalf("摘要提取错误：%q %v", summary, err)
	}
	if _, err := extractSummary("<analysis>只有分析</analysis>"); err == nil {
		t.Fatal("只有 analysis 应报错")
	}
	if _, err := extractSummary("   "); err == nil {
		t.Fatal("空摘要应报错")
	}
	if _, err := extractSummary("普通回复"); err == nil {
		t.Fatal("缺少 summary 标记应报错")
	}
}

func TestRequestViewPreviewsLargeToolResult(t *testing.T) {
	manager := contextview.NewManager(contextview.Config{
		Budget:       contextview.NewBudget(1_000_000, 4096),
		PreviewBytes: 1024,
	})
	tools := &testTools{execute: func(context.Context, model.ToolCall) (tool.Result, error) {
		return tool.Result{Content: strings.Repeat("a", 10_000)}, nil
	}}
	requests := 0
	var second model.Request
	var fullResult int
	client := clientFunc(func(_ context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
		requests++
		if requests == 1 {
			return model.Response{Message: toolCallMessage("one"), StopReason: model.StopToolUse}, nil
		}
		second = req
		return finalResponse(), nil
	})
	_, err := NewConversation(Options{
		Client: client, Tools: tools, Model: "model", ContextView: manager,
	}).Run(context.Background(), "task", func(event Event) error {
		if event.Type == EventToolEnd && event.ToolResult != nil {
			fullResult = len(event.ToolResult.Content)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if fullResult != 10_000 {
		t.Fatalf("诊断事件应保留完整结果：%d", fullResult)
	}
	if len(second.Messages) != 3 {
		t.Fatalf("请求消息数量错误：%d", len(second.Messages))
	}
	content := second.Messages[2].Content[0].ToolResult.Content
	if len(content) > 1400 || !strings.Contains(content, "[上下文预览") {
		t.Fatalf("请求视图未使用预览：len=%d %s", len(content), content)
	}
}

func TestNextTurnUsesCompactedToolRecord(t *testing.T) {
	manager := contextview.NewManager(contextview.Config{Budget: contextview.NewBudget(1_000_000, 4096)})
	tools := &testTools{execute: func(context.Context, model.ToolCall) (tool.Result, error) {
		return tool.Result{Content: strings.Repeat("b", 40_000)}, nil
	}}
	requests := 0
	client := clientFunc(func(_ context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
		requests++
		switch requests {
		case 1:
			return model.Response{Message: toolCallMessage("one"), StopReason: model.StopToolUse}, nil
		case 2:
			return finalResponse(), nil
		case 3:
			if len(req.Messages) != 5 {
				t.Fatalf("第二轮请求消息数量错误：%d", len(req.Messages))
			}
			content := req.Messages[2].Content[0].ToolResult.Content
			if !strings.HasPrefix(content, "[工具结果已压缩]") || !strings.Contains(content, "工具：bash") {
				t.Fatalf("下一轮未使用轮级压缩记录：%s", content)
			}
			return finalResponse(), nil
		default:
			t.Fatal("不应有额外请求")
			return model.Response{}, nil
		}
	})
	conversation := NewConversation(Options{Client: client, Tools: tools, Model: "model", ContextView: manager})
	if _, err := conversation.Run(context.Background(), "task-1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.Run(context.Background(), "task-2", nil); err != nil {
		t.Fatal(err)
	}
}

func TestAutoSummaryAtTurnStart(t *testing.T) {
	manager := contextview.NewManager(contextview.Config{Budget: contextview.NewBudget(32_768, 4096)})
	populateTurns(manager, 6, 10_000)
	tools := &testTools{}
	var taskRequest model.Request
	var summaries int
	var summaryMode bool
	var summaryEvent *SummaryInfo
	client := clientFunc(func(_ context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
		if req.System == summarySystemPrompt {
			summaries++
			if len(req.Messages) > 0 {
				last := req.Messages[len(req.Messages)-1]
				summaryMode = strings.Contains(last.Content[0].Text, "放行")
			}
			return model.Response{
				Message:    textMessage(model.RoleAssistant, "<analysis>整理</analysis><summary>旧工作摘要</summary>"),
				StopReason: model.StopEndTurn,
			}, nil
		}
		taskRequest = req
		return finalResponse(), nil
	})
	_, err := NewConversation(Options{
		Client: client, Tools: tools, Model: "model", ContextView: manager, MaxTurns: 2, InteractionMode: "放行",
	}).Run(context.Background(), "new-task", func(event Event) error {
		if event.Type == EventSummary {
			summaryEvent = event.Summary
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if summaries == 0 {
		t.Fatal("未发起摘要请求")
	}
	if !summaryMode {
		t.Fatal("摘要提示词缺少当前交互模式")
	}
	if summaryEvent == nil || summaryEvent.Text != "旧工作摘要" {
		t.Fatalf("摘要事件错误：%+v", summaryEvent)
	}
	if len(taskRequest.Messages) < 2 {
		t.Fatalf("任务请求消息不足：%d", len(taskRequest.Messages))
	}
	first := taskRequest.Messages[0]
	if first.Role != model.RoleUser || !strings.Contains(first.Content[0].Text, "旧工作摘要") {
		t.Fatalf("任务请求未使用摘要前缀：%+v", first)
	}
	last := taskRequest.Messages[len(taskRequest.Messages)-1]
	if last.Role != model.RoleUser || last.Content[0].Text != "new-task" {
		t.Fatalf("当前轮消息错误：%+v", last)
	}
}

func TestSummaryFailureFallsBackToDeterministicTrim(t *testing.T) {
	manager := contextview.NewManager(contextview.Config{Budget: contextview.NewBudget(32_768, 4096)})
	populateTurns(manager, 6, 10_000)
	tools := &testTools{}
	var taskRequest model.Request
	client := clientFunc(func(_ context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
		if req.System == summarySystemPrompt {
			return model.Response{}, errors.New("摘要服务失败")
		}
		taskRequest = req
		return finalResponse(), nil
	})
	_, err := NewConversation(Options{
		Client: client, Tools: tools, Model: "model", ContextView: manager, MaxTurns: 2,
	}).Run(context.Background(), "new-task", nil)
	if err != nil {
		t.Fatal(err)
	}
	first := taskRequest.Messages[0]
	if !strings.Contains(first.Content[0].Text, "上下文已裁剪") {
		t.Fatalf("未降级为确定性裁剪：%+v", first)
	}
}

func TestContextExceededWhenCurrentTurnTooLarge(t *testing.T) {
	manager := contextview.NewManager(contextview.Config{Budget: contextview.NewBudget(32_768, 4096)})
	tools := &testTools{}
	requests := 0
	client := clientFunc(func(context.Context, model.Request, func(model.Event) error) (model.Response, error) {
		requests++
		return finalResponse(), nil
	})
	_, err := NewConversation(Options{
		Client: client, Tools: tools, Model: "model", ContextView: manager,
	}).Run(context.Background(), strings.Repeat("大", 60_000), nil)
	if !errors.Is(err, ErrContextExceeded) {
		t.Fatalf("应返回上下文超限：%v", err)
	}
	if requests != 0 {
		t.Fatal("超限时不应请求模型")
	}
}
