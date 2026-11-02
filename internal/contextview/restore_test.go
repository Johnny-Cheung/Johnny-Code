package contextview

import (
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

func TestRestoreSeedsSummaryAndRetainedTurn(t *testing.T) {
	manager := NewManager(Config{Budget: NewBudget(1_000_000, 4096), WorkDir: t.TempDir(), SessionID: "restore"})
	defer manager.Close()

	call := model.ToolCall{ID: "call-1", Name: "read", Arguments: []byte(`{"path":"a.txt"}`)}
	result := model.ToolResult{ToolCallID: "call-1", Content: "文件内容"}
	manager.Restore(
		&RestoreSummary{Text: "旧摘要", CoveredThrough: 1},
		[]RestoreStep{
			{UserTurn: 2, Message: userMessage("第二轮")},
			{UserTurn: 2, Message: assistantMessage("回答二")},
			{UserTurn: 2, Message: model.Message{Role: model.RoleTool, Content: []model.ContentBlock{{
				Type: model.BlockToolResult, ToolResult: &result,
			}}}, Call: &call},
		},
	)

	messages := manager.Messages()
	if len(messages) != 4 {
		t.Fatalf("messages = %d, want summary + 3 messages", len(messages))
	}
	if messages[0].Role != model.RoleUser || messages[0].Content[0].Text != "旧摘要" {
		t.Fatalf("summary message = %+v", messages[0])
	}
	if messages[1].Content[0].Text != "第二轮" || messages[2].Content[0].Text != "回答二" {
		t.Fatalf("retained messages = %+v", messages[1:])
	}
	if message := messages[3]; message.Content[0].ToolResult == nil || message.Content[0].ToolResult.Content != "文件内容" {
		t.Fatalf("tool message = %+v", message)
	}

	stats := manager.Stats("", nil)
	if stats.Turns != 1 || stats.SummaryTurns != 1 || stats.CoveredThrough != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if manager.SummaryText() != "旧摘要" || manager.SummaryCovered() != 1 || manager.SummaryIsNote() {
		t.Fatalf("summary = %q covered %d note %t", manager.SummaryText(), manager.SummaryCovered(), manager.SummaryIsNote())
	}
	if manager.CurrentUserTurn() != 2 {
		t.Fatalf("current user turn = %d", manager.CurrentUserTurn())
	}
}

func TestRestoreKeepsInjectedMessagesInOneTurn(t *testing.T) {
	manager := NewManager(Config{Budget: NewBudget(1_000_000, 4096), WorkDir: t.TempDir(), SessionID: "restore-inject"})
	defer manager.Close()

	manager.Restore(nil, []RestoreStep{
		{UserTurn: 1, Message: userMessage("任务")},
		{UserTurn: 1, Message: userMessage("[子任务报告]")},
		{UserTurn: 1, Message: assistantMessage("回答")},
	})

	messages := manager.Messages()
	if len(messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(messages))
	}
	if messages[1].Content[0].Text != "[子任务报告]" {
		t.Fatalf("injected message = %+v", messages[1])
	}
	if stats := manager.Stats("", nil); stats.Turns != 1 {
		t.Fatalf("turns = %d, want 1", stats.Turns)
	}
}

func TestRestoreSummaryOnly(t *testing.T) {
	manager := NewManager(Config{Budget: NewBudget(1_000_000, 4096), WorkDir: t.TempDir(), SessionID: "restore-summary"})
	defer manager.Close()

	manager.Restore(&RestoreSummary{Text: "全部压缩后的摘要", CoveredThrough: 4}, nil)
	messages := manager.Messages()
	if len(messages) != 1 || messages[0].Content[0].Text != "全部压缩后的摘要" {
		t.Fatalf("messages = %+v", messages)
	}
	stats := manager.Stats("", nil)
	if stats.Turns != 0 || stats.SummaryTurns != 4 || stats.CoveredThrough != 4 {
		t.Fatalf("stats = %+v", stats)
	}
	if manager.CurrentUserTurn() != 0 {
		t.Fatalf("current user turn = %d", manager.CurrentUserTurn())
	}
}
