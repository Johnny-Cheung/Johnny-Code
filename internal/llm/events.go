package llm

type StreamEvent interface{ streamEvent() }

type TextDelta struct{ Text string }
type ThinkingDelta struct{ Text string }
type ThinkingComplete struct {
	Thinking  string
	Signature string
}
type ToolCallStart struct{ ToolName, ToolID string }
type ToolCallDelta struct{ Text string }
type ToolCallComplete struct {
	ToolID    string
	ToolName  string
	Arguments map[string]any
}
type UsageInfo struct {
	InputTokens  int
	OutputTokens int
	
	
	
	
	
	CacheReadTokens int
	
	
	
	CacheCreationTokens int
}

type StreamEnd struct {
	StopReason string
	Usage      UsageInfo
}

func (TextDelta) streamEvent()         {}

func (ThinkingDelta) streamEvent()     {}

func (ThinkingComplete) streamEvent()  {}
func (ToolCallStart) streamEvent()     {}
func (ToolCallDelta) streamEvent()     {}

func (ToolCallComplete) streamEvent()  {}

func (StreamEnd) streamEvent()         {}
