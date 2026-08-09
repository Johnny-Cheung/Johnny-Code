
package llm

import (
	"context"
	"fmt"

	"johnnycode/internal/config"
	"johnnycode/internal/conversation"
)

type Client interface {
	Stream(ctx context.Context, conv *conversation.Manager, tools []map[string]any) (<-chan StreamEvent, <-chan error)
}

type MaxTokensSetter interface {
	SetMaxOutputTokens(tokens int)
}

func NewClient(cfg *config.ProviderConfig, systemPrompt string) (Client, error) {
	switch cfg.Protocol {
	case "anthropic":
		return newAnthropicClient(cfg, systemPrompt)
	case "openai":
		return newOpenAIClient(cfg, systemPrompt)
	case "openai-compat":
		return newOpenAICompatClient(cfg, systemPrompt)
	default:
		return nil, fmt.Errorf("unknown protocol: %s", cfg.Protocol)
	}
}



type contextWindowFetcher interface {
	FetchModelContextWindow(ctx context.Context) int
}












func ResolveContextWindow(ctx context.Context, cfg *config.ProviderConfig) {
	
	
	if cfg == nil || cfg.ContextWindow > 0 {
		return
	}
	if cfg.Protocol != "anthropic" {
		return
	}

	client, err := NewClient(cfg, "")
	if err != nil {
		return
	}
	fetcher, ok := client.(contextWindowFetcher)
	if !ok {
		return
	}
	if window := fetcher.FetchModelContextWindow(ctx); window > 0 {
		cfg.SetFetchedContextWindow(window)
	}
}

