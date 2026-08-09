package agent

import (
	"context"
	"sync"

	"johnnycode/internal/llm"
	"johnnycode/internal/permissions"
	"johnnycode/internal/tools"
)

type StreamingExecutor struct {
	registry *tools.Registry
	checker  *permissions.Checker
	eventCh  chan AgentEvent

	mu      sync.Mutex
	pending []pendingTool
	wg      sync.WaitGroup
}

type pendingTool struct {
	call   llm.ToolCallComplete
	result toolExecResult
}

func NewStreamingExecutor(registry *tools.Registry, checker *permissions.Checker, eventCh chan AgentEvent) *StreamingExecutor {
	return &StreamingExecutor{
		registry: registry,
		checker:  checker,
		eventCh:  eventCh,
	}
}

func (se *StreamingExecutor) Submit(ctx context.Context, agent *Agent, tc llm.ToolCallComplete) {
	se.mu.Lock()
	idx := len(se.pending)
	se.pending = append(se.pending, pendingTool{call: tc})
	se.mu.Unlock()

	se.wg.Add(1)
	go func() {
		defer se.wg.Done()
		result := agent.executeSingleTool(ctx, se.eventCh, tc)

		se.mu.Lock()
		se.pending[idx].result = result
		se.mu.Unlock()
	}()
}

func (se *StreamingExecutor) CollectResults() []toolExecResult {
	se.wg.Wait()

	se.mu.Lock()
	defer se.mu.Unlock()

	results := make([]toolExecResult, len(se.pending))
	for i, p := range se.pending {
		results[i] = p.result
	}
	return results
}
