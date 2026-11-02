package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

func TestRestoreConversationSendsSummaryAndRetainedHistory(t *testing.T) {
	var requests [][]model.Message
	client := clientFunc(func(ctx context.Context, req model.Request, emit func(model.Event) error) (model.Response, error) {
		requests = append(requests, append([]model.Message(nil), req.Messages...))
		return finalResponse(), nil
	})
	conversation, err := RestoreConversation(Options{
		Client:          client,
		Tools:           &testTools{},
		Model:           "test-model",
		System:          "system",
		MaxTurns:        5,
		MaxOutputTokens: 4096,
	}, RestoreState{
		Summary: &contextview.RestoreSummary{Text: "旧摘要", CoveredThrough: 1},
		Steps: []contextview.RestoreStep{
			{UserTurn: 2, Message: textMessage(model.RoleUser, "继续")},
			{UserTurn: 2, Message: textMessage(model.RoleAssistant, "好的")},
		},
		UserTurn: 2,
	})
	if err != nil {
		t.Fatalf("RestoreConversation() error = %v", err)
	}

	stats := conversation.ContextStats()
	if stats.SummaryTurns != 1 || stats.CoveredThrough != 1 || stats.Turns != 1 {
		t.Fatalf("stats = %+v", stats)
	}

	var startTurn int
	if _, err := conversation.Run(context.Background(), "下一步", func(event Event) error {
		if event.Type == EventStart {
			startTurn = event.UserTurn
		}
		return nil
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if startTurn != 3 {
		t.Fatalf("start user turn = %d, want 3", startTurn)
	}
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	messages := requests[0]
	if len(messages) != 4 {
		t.Fatalf("messages = %d, want 4: %+v", len(messages), messages)
	}
	if messages[0].Content[0].Text != "旧摘要" || messages[1].Content[0].Text != "继续" {
		t.Fatalf("restored prefix = %+v", messages[:2])
	}
	if messages[2].Content[0].Text != "好的" || !strings.Contains(messages[3].Content[0].Text, "下一步") {
		t.Fatalf("history = %+v", messages)
	}
}
