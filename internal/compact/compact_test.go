package compact

import (
	"context"
	"strings"
	"testing"

	"johnnycode/internal/conversation"
	"johnnycode/internal/llm"
	"johnnycode/internal/session"
)





type stubSummaryClient struct {
	summary      string
	lastPrompt   string
	streamCalled bool
}

func (c *stubSummaryClient) Stream(ctx context.Context, conv *conversation.Manager, tools []map[string]any) (<-chan llm.StreamEvent, <-chan error) {
	c.streamCalled = true
	if msgs := conv.GetMessages(); len(msgs) > 0 {
		c.lastPrompt = msgs[len(msgs)-1].Content
	}
	ch := make(chan llm.StreamEvent, 4)
	errCh := make(chan error, 1)
	ch <- llm.TextDelta{Text: "<summary>" + c.summary + "</summary>"}
	ch <- llm.StreamEnd{StopReason: "end_turn"}
	close(ch)
	errCh <- nil
	close(errCh)
	return ch, errCh
}




type failThenSucceedSummaryClient struct {
	failures int 
	summary  string
	prompts  []string
}

func (c *failThenSucceedSummaryClient) Stream(ctx context.Context, conv *conversation.Manager, tools []map[string]any) (<-chan llm.StreamEvent, <-chan error) {
	ch := make(chan llm.StreamEvent, 4)
	errCh := make(chan error, 1)
	if msgs := conv.GetMessages(); len(msgs) > 0 {
		c.prompts = append(c.prompts, msgs[len(msgs)-1].Content)
	}
	if c.failures > 0 {
		c.failures--
		errCh <- &llm.ContextTooLongError{Message: "prompt is too long"}
		close(errCh)
		close(ch)
		return ch, errCh
	}
	ch <- llm.TextDelta{Text: "<summary>" + c.summary + "</summary>"}
	ch <- llm.StreamEnd{StopReason: "end_turn"}
	close(ch)
	errCh <- nil
	close(errCh)
	return ch, errCh
}






func TestAutoCompactRetriesWithSmallerPrefix(t *testing.T) {
	conv := conversation.NewManager()
	
	for i := 0; i < 6; i++ {
		conv.AddUserMessage("OLD-PREFIX " + bigMsg(3000))
		conv.AddAssistantMessage("OLD-REPLY " + bigMsg(3000))
	}
	recent := []string{"RECENT-A unique-marker-A", "RECENT-B unique-marker-B"}
	conv.AddUserMessage(recent[0])
	conv.AddAssistantMessage(recent[1])

	client := &failThenSucceedSummaryClient{failures: 1, summary: "RETRY-SUMMARY"}
	msg, err := autoCompact(context.Background(), conv, client, "", "", 200000, nil, nil)
	if err != nil {
		t.Fatalf("autoCompact error: %v", err)
	}
	if msg == "" {
		t.Fatalf("expected a compaction message after successful retry, got empty")
	}
	if len(client.prompts) != 2 {
		t.Fatalf("expected exactly 2 summarizer calls (1 failed + 1 retry), got %d", len(client.prompts))
	}
	if len(client.prompts[1]) >= len(client.prompts[0]) {
		t.Errorf("retry prompt (%d chars) must be strictly shorter than the first attempt (%d chars) — the retry must shrink the summarized range",
			len(client.prompts[1]), len(client.prompts[0]))
	}
	
	
	var sawSummary bool
	for _, m := range conv.GetMessages() {
		if strings.Contains(m.Content, "RETRY-SUMMARY") {
			sawSummary = true
		}
	}
	if !sawSummary {
		t.Errorf("retry summary not present after compaction")
	}
	for _, r := range recent {
		if !containsMsg(conv.GetMessages(), r) {
			t.Errorf("recent message %q not preserved verbatim after retry compaction", r)
		}
	}
}




func TestAutoCompactAllRetriesFailKeepsRecentTail(t *testing.T) {
	conv := conversation.NewManager()
	for i := 0; i < 6; i++ {
		conv.AddUserMessage("OLD-PREFIX " + bigMsg(3000))
		conv.AddAssistantMessage("OLD-REPLY " + bigMsg(3000))
	}
	recent := []string{"RECENT-A unique-marker-A", "RECENT-B unique-marker-B"}
	conv.AddUserMessage(recent[0])
	conv.AddAssistantMessage(recent[1])

	client := &failThenSucceedSummaryClient{failures: 3, summary: "SHOULD-NOT-APPEAR"}
	msg, err := autoCompact(context.Background(), conv, client, "", "", 200000, nil, nil)
	if err != nil {
		t.Fatalf("autoCompact error: %v", err)
	}
	if msg == "" {
		t.Fatalf("expected a compaction message even when all retries fail, got empty")
	}
	if len(client.prompts) != 3 {
		t.Fatalf("expected exactly 3 summarizer calls (all failing), got %d", len(client.prompts))
	}
	var sawNotice, sawSummary bool
	for _, m := range conv.GetMessages() {
		if strings.Contains(m.Content, "已丢弃") {
			sawNotice = true
		}
		if strings.Contains(m.Content, "SHOULD-NOT-APPEAR") {
			sawSummary = true
		}
	}
	if !sawNotice {
		t.Errorf("drop notice not present after all retries failed")
	}
	if sawSummary {
		t.Errorf("failed summarizer output must never appear in the conversation")
	}
	for _, r := range recent {
		if !containsMsg(conv.GetMessages(), r) {
			t.Errorf("recent message %q not preserved verbatim after failed compaction", r)
		}
	}
}





func TestFormatCompactSummary(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "both blocks present",
			in:   "<analysis>scratch thoughts</analysis>\n<summary>final text</summary>",
			want: "final text",
		},
		{
			name: "summary block unterminated",
			in:   "<analysis>scratch</analysis>\n<summary>tail with no close tag",
			want: "tail with no close tag",
		},
		{
			name: "only analysis block — drop it",
			in:   "prefix <analysis>scratch</analysis> suffix",
			want: "prefix  suffix",
		},
		{
			name: "neither block — return raw",
			in:   "plain text response",
			want: "plain text response",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatCompactSummary(tc.in)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}


func TestEstimateTokensZeroAndPopulated(t *testing.T) {
	if got := EstimateTokens(nil); got != 0 {
		t.Errorf("empty input should be 0 tokens, got %d", got)
	}
	conv := conversation.NewManager()
	conv.AddUserMessage(strings.Repeat("x", 700))
	got := EstimateTokens(conv.GetMessages())
	if got < 150 || got > 250 {
		t.Errorf("700-char message should estimate ~200 tokens, got %d", got)
	}
}




func TestBaselineFromUsage(t *testing.T) {
	u := llm.UsageInfo{
		InputTokens:         100,
		OutputTokens:        40,
		CacheReadTokens:     5000,
		CacheCreationTokens: 200,
	}
	if got, want := BaselineFromUsage(u), 5340; got != want {
		t.Errorf("BaselineFromUsage = %d, want %d", got, want)
	}
	
	
	if got := BaselineFromUsage(llm.UsageInfo{}); got != 0 {
		t.Errorf("empty usage baseline = %d, want 0", got)
	}
}



func TestComputeUsedTokensColdStartFallback(t *testing.T) {
	conv := conversation.NewManager()
	conv.AddUserMessage(strings.Repeat("x", 700))
	conv.AddAssistantMessage(strings.Repeat("y", 700))
	msgs := conv.GetMessages()

	got := ComputeUsedTokens(msgs, UsageAnchor{}) 
	want := EstimateTokens(msgs)
	if got != want {
		t.Errorf("cold-start ComputeUsedTokens = %d, want full estimate %d", got, want)
	}
}





func TestComputeUsedTokensWithAnchorIncremental(t *testing.T) {
	conv := conversation.NewManager()
	
	
	conv.AddUserMessage(strings.Repeat("x", 7000))
	conv.AddAssistantMessage(strings.Repeat("y", 7000))
	conv.AddUserMessage(strings.Repeat("z", 7000))
	anchorCount := conv.Len()
	
	conv.AddAssistantMessage(strings.Repeat("w", 350))
	msgs := conv.GetMessages()

	const baseline = 1500 
	anchor := UsageAnchor{BaselineTokens: baseline, AnchorCount: anchorCount, HasUsage: true}

	got := ComputeUsedTokens(msgs, anchor)
	wantIncrement := EstimateTokens(msgs[anchorCount:])
	if got != baseline+wantIncrement {
		t.Errorf("anchored ComputeUsedTokens = %d, want baseline+increment %d", got, baseline+wantIncrement)
	}
	
	
	if full := EstimateTokens(msgs); got >= full {
		t.Errorf("anchored result %d should be below full estimate %d", got, full)
	}
}




func TestComputeUsedTokensStaleAnchorClamp(t *testing.T) {
	conv := conversation.NewManager()
	conv.AddUserMessage("hi")
	msgs := conv.GetMessages()

	anchor := UsageAnchor{BaselineTokens: 9999, AnchorCount: 50, HasUsage: true}
	got := ComputeUsedTokens(msgs, anchor)
	if want := EstimateTokens(msgs); got != want {
		t.Errorf("stale-anchor ComputeUsedTokens = %d, want full estimate %d", got, want)
	}
}




func bigMsg(tokens int) string {
	return strings.Repeat("x", tokens*4)
}


func containsMsg(msgs []conversation.Message, want string) bool {
	for _, m := range msgs {
		if m.Content == want {
			return true
		}
	}
	return false
}





func TestAutoCompactKeepsRecentVerbatim(t *testing.T) {
	conv := conversation.NewManager()
	
	for i := 0; i < 6; i++ {
		conv.AddUserMessage("OLD-PREFIX " + bigMsg(3000))
		conv.AddAssistantMessage("OLD-REPLY " + bigMsg(3000))
	}
	
	recent := []string{"RECENT-A unique-marker-A", "RECENT-B unique-marker-B"}
	conv.AddUserMessage(recent[0])
	conv.AddAssistantMessage(recent[1])

	client := &stubSummaryClient{summary: "THE SUMMARY"}
	msg, err := autoCompact(context.Background(), conv, client, "", "", 200000, nil, nil)
	if err != nil {
		t.Fatalf("autoCompact error: %v", err)
	}
	if msg == "" {
		t.Fatalf("expected a compaction message, got empty (degraded to no-op)")
	}
	out := conv.GetMessages()

	
	var sawSummary bool
	for _, m := range out {
		if strings.Contains(m.Content, "THE SUMMARY") {
			sawSummary = true
		}
	}
	if !sawSummary {
		t.Errorf("summary not present after compaction")
	}
	
	for _, r := range recent {
		if !containsMsg(out, r) {
			t.Errorf("recent message %q not preserved verbatim after compaction; messages=%v", r, msgContents(out))
		}
	}
}





func TestAutoCompactPersistsBoundary(t *testing.T) {
	conv := conversation.NewManager()
	for i := 0; i < 6; i++ {
		conv.AddUserMessage("OLD-PREFIX " + bigMsg(3000))
		conv.AddAssistantMessage("OLD-REPLY " + bigMsg(3000))
	}
	conv.AddUserMessage("RECENT-TAIL-USER unique-marker-A")
	conv.AddAssistantMessage("RECENT-TAIL-ASSISTANT unique-marker-B")

	workDir := t.TempDir()
	sid := "compact-roundtrip"
	client := &stubSummaryClient{summary: "PERSISTED-SUMMARY"}

	msg, err := autoCompact(context.Background(), conv, client, workDir, sid, 200000, nil, nil)
	if err != nil {
		t.Fatalf("autoCompact error: %v", err)
	}
	if msg == "" {
		t.Fatalf("expected a compaction message, got empty (degraded to no-op)")
	}

	
	
	msgs := session.LoadSession(workDir, sid)
	boundary, after, ok := session.FindLastCompactBoundary(msgs)
	if !ok {
		t.Fatalf("expected a compact_boundary record to be persisted")
	}
	if boundary.Summary != "PERSISTED-SUMMARY" {
		t.Fatalf("persisted summary mismatch: got %q", boundary.Summary)
	}
	if len(after) != 0 {
		t.Fatalf("no messages should follow a freshly written boundary, got %d", len(after))
	}
	
	var sawTailUser, sawTailAssistant bool
	for _, k := range boundary.Keep {
		if k.Content == "RECENT-TAIL-USER unique-marker-A" {
			sawTailUser = true
		}
		if k.Content == "RECENT-TAIL-ASSISTANT unique-marker-B" {
			sawTailAssistant = true
		}
	}
	if !sawTailUser || !sawTailAssistant {
		t.Fatalf("kept tail not inlined into boundary: %+v", boundary.Keep)
	}
	
	
	
	
	
	rebuilt := conv.GetMessages()
	tail := rebuilt[len(rebuilt)-len(boundary.Keep):]
	for i, k := range boundary.Keep {
		if tail[i].Role != k.Role || tail[i].Content != k.Content {
			t.Fatalf("boundary keep[%d]=%+v does not match in-memory tail %+v", i, k, tail[i])
		}
	}
}



func TestAutoCompactNoSessionNoBoundary(t *testing.T) {
	conv := conversation.NewManager()
	for i := 0; i < 6; i++ {
		conv.AddUserMessage("OLD-PREFIX " + bigMsg(3000))
		conv.AddAssistantMessage("OLD-REPLY " + bigMsg(3000))
	}
	conv.AddUserMessage("RECENT-A")
	conv.AddAssistantMessage("RECENT-B")

	workDir := t.TempDir()
	client := &stubSummaryClient{summary: "S"}
	
	if _, err := autoCompact(context.Background(), conv, client, workDir, "", 200000, nil, nil); err != nil {
		t.Fatalf("autoCompact error: %v", err)
	}
	
	msgs := session.LoadSession(workDir, "anything")
	if len(msgs) != 0 {
		t.Fatalf("expected no session log written when sessionID empty, got %d", len(msgs))
	}
}



func TestAutoCompactSummaryOnlyCoversPrefix(t *testing.T) {
	conv := conversation.NewManager()
	for i := 0; i < 6; i++ {
		conv.AddUserMessage("PREFIX-ONLY-CONTENT " + bigMsg(3000))
		conv.AddAssistantMessage("PREFIX-ONLY-REPLY " + bigMsg(3000))
	}
	conv.AddUserMessage("TAIL-ONLY-MARKER")
	conv.AddAssistantMessage("TAIL-ONLY-REPLY")

	client := &stubSummaryClient{summary: "S"}
	if _, err := autoCompact(context.Background(), conv, client, "", "", 200000, nil, nil); err != nil {
		t.Fatalf("autoCompact error: %v", err)
	}
	if !client.streamCalled {
		t.Fatalf("summarizer was never called")
	}
	if strings.Contains(client.lastPrompt, "TAIL-ONLY-MARKER") {
		t.Errorf("summary prompt must not include the kept tail, but it did")
	}
	if !strings.Contains(client.lastPrompt, "PREFIX-ONLY-CONTENT") {
		t.Errorf("summary prompt must include the summarized prefix, but it did not")
	}
}




func TestComputeKeepStartIndexDoesNotSplitToolPair(t *testing.T) {
	conv := conversation.NewManager()
	
	for i := 0; i < 8; i++ {
		conv.AddUserMessage(bigMsg(3000))
		conv.AddAssistantMessage(bigMsg(3000))
	}
	
	
	conv.AddToolUseMessage("calling tool", "tu-1", "ReadFile", map[string]any{"path": "/x"})
	conv.AddToolResultMessage("tu-1", bigMsg(9000), false)
	msgs := conv.GetMessages()

	keepStart := computeKeepStartIndex(msgs)
	if keepStart <= 0 || keepStart >= len(msgs) {
		t.Fatalf("keepStart=%d out of expected range (0, %d)", keepStart, len(msgs))
	}
	
	
	if hasToolResults(msgs[keepStart]) {
		t.Fatalf("keepStart landed on a tool_result message (orphaned); keepStart=%d", keepStart)
	}
	
	
	keep := msgs[keepStart:]
	openUses := map[string]bool{}
	for _, m := range keep {
		for _, tu := range m.ToolUses {
			openUses[tu.ToolUseID] = true
		}
		for _, tr := range m.ToolResults {
			if !openUses[tr.ToolUseID] {
				t.Errorf("tool_result %s in kept tail has no matching tool_use in tail (pair split)", tr.ToolUseID)
			}
		}
	}
}




func TestAutoCompactDegradesWhenTooFewMessages(t *testing.T) {
	conv := conversation.NewManager()
	conv.AddUserMessage("just one")
	conv.AddAssistantMessage("two")
	before := conv.GetMessages()

	client := &stubSummaryClient{summary: "S"}
	msg, err := autoCompact(context.Background(), conv, client, "", "", 200000, nil, nil)
	if err != nil {
		t.Fatalf("autoCompact error: %v", err)
	}
	if msg != "" {
		t.Errorf("expected no-op (empty message) for too-few messages, got %q", msg)
	}
	if client.streamCalled {
		t.Errorf("summarizer should not be called when degrading to no-op")
	}
	after := conv.GetMessages()
	if len(before) != len(after) {
		t.Errorf("conversation changed during no-op: before=%d after=%d", len(before), len(after))
	}
	for i := range before {
		if before[i].Content != after[i].Content {
			t.Errorf("message %d mutated during no-op", i)
		}
	}
}

func msgContents(msgs []conversation.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		c := m.Content
		if len(c) > 40 {
			c = c[:40] + "..."
		}
		out[i] = c
	}
	return out
}
