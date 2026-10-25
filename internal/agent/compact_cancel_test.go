package agent

import (
	"context"
	"testing"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

func TestCompactCancellationDoesNotTrim(t *testing.T) {
	manager := contextview.NewManager(contextview.Config{Budget: contextview.NewBudget(32_768, 4096)})
	populateTurns(manager, 6, 10_000)
	tools := &testTools{}
	client := clientFunc(func(ctx context.Context, req model.Request, _ func(model.Event) error) (model.Response, error) {
		if req.System == summarySystemPrompt {
			<-ctx.Done()
			return model.Response{}, ctx.Err()
		}
		return finalResponse(), nil
	})
	conv := NewConversation(Options{Client: client, Tools: tools, Model: "model", ContextView: manager})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	text, err := conv.Compact(ctx, nil)
	if err == nil {
		t.Fatalf("取消的压缩应返回错误，实际返回 %q", text)
	}
	stats := manager.Stats("", nil)
	if stats.SummaryNote || stats.Summary != "" {
		t.Fatalf("canceled compact mutated view: %+v", stats)
	}
}
