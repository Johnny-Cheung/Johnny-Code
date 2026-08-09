package toolresult

import (
	"reflect"
	"strings"
	"testing"

	"johnnycode/internal/conversation"
)

func oneToolResultMsg(results ...conversation.ToolResultBlock) *conversation.Manager {
	conv := conversation.NewManager()
	conv.AddToolResultsMessage(results)
	return conv
}

func TestApplyDoesNotMutateConv(t *testing.T) {
	big := strings.Repeat("x", SingleResultLimit+100)
	conv := oneToolResultMsg(conversation.ToolResultBlock{ToolUseID: "t1", Content: big})
	state := New()

	origSnapshot := conv.GetMessages()
	origContent := conv.GetMessages()[0].ToolResults[0].Content

	apiConv, _, err := Apply(conv, t.TempDir(), state)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if apiConv == conv {
		t.Fatal("Apply returned the same *Manager — Design B requires a new one")
	}

	if got := conv.GetMessages()[0].ToolResults[0].Content; got != origContent {
		t.Fatalf("original conv mutated: got=%q want=%q", got[:50], origContent[:50])
	}
	if !reflect.DeepEqual(conv.GetMessages(), origSnapshot) {
		t.Fatal("original conv history shape mutated")
	}

	apiContent := apiConv.GetMessages()[0].ToolResults[0].Content
	if !strings.HasPrefix(apiContent, "[Result of ") {
		t.Fatalf("apiConv tool_result not replaced: %q", apiContent)
	}
}

func TestFirstCallFreezesUnreplaced(t *testing.T) {
	small := strings.Repeat("y", 100)
	conv := oneToolResultMsg(conversation.ToolResultBlock{ToolUseID: "t1", Content: small})
	state := New()

	_, records, err := Apply(conv, t.TempDir(), state)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if _, ok := state.SeenIDs["t1"]; !ok {
		t.Fatal("t1 not added to SeenIDs after first call")
	}
	if _, ok := state.Replacements["t1"]; ok {
		t.Fatal("t1 should not be in Replacements when under budget")
	}
	if len(records) != 0 {
		t.Fatalf("expected no records, got %d", len(records))
	}
}

func TestReplacementByteIdentical(t *testing.T) {
	big := strings.Repeat("z", SingleResultLimit+200)
	conv := oneToolResultMsg(conversation.ToolResultBlock{ToolUseID: "t_big", Content: big})
	state := New()
	dir := t.TempDir()

	api1, recs1, err1 := Apply(conv, dir, state)
	if err1 != nil {
		t.Fatalf("Apply 1: %v", err1)
	}
	api2, recs2, err2 := Apply(conv, dir, state)
	if err2 != nil {
		t.Fatalf("Apply 2: %v", err2)
	}

	c1 := api1.GetMessages()[0].ToolResults[0].Content
	c2 := api2.GetMessages()[0].ToolResults[0].Content
	if c1 != c2 {
		t.Fatalf("byte mismatch between calls:\n  first: %q\n second: %q", c1, c2)
	}
	if len(recs1) != 1 {
		t.Fatalf("first call should record 1 replacement, got %d", len(recs1))
	}
	if len(recs2) != 0 {
		t.Fatalf("second call should record 0 (pure re-apply), got %d", len(recs2))
	}
	if state.Replacements["t_big"] != c1 {
		t.Fatal("state.Replacements out of sync with api_conv content")
	}
}

func TestApplyWithFreshDefersSingleResultSpill(t *testing.T) {
	big := strings.Repeat("z", SingleResultLimit+200)
	conv := oneToolResultMsg(conversation.ToolResultBlock{ToolUseID: "fresh", Content: big})
	state := New()
	dir := t.TempDir()

	first, firstRecords, err := ApplyWithFresh(conv, dir, state, map[string]struct{}{"fresh": {}})
	if err != nil {
		t.Fatalf("fresh Apply: %v", err)
	}
	if len(firstRecords) != 0 {
		t.Fatalf("fresh pass unexpectedly spilled result: %d records", len(firstRecords))
	}
	if got := first.GetMessages()[0].ToolResults[0].Content; got != big {
		t.Fatal("fresh pass did not preserve the complete result")
	}
	if _, ok := state.SeenIDs["fresh"]; ok {
		t.Fatal("fresh result was frozen before its first model pass")
	}

	second, secondRecords, err := Apply(conv, dir, state)
	if err != nil {
		t.Fatalf("normal Apply: %v", err)
	}
	if len(secondRecords) != 1 {
		t.Fatalf("normal pass records = %d, want 1", len(secondRecords))
	}
	if got := second.GetMessages()[0].ToolResults[0].Content; !strings.HasPrefix(got, "[Result of ") {
		t.Fatalf("normal pass did not spill result: %q", got)
	}
}

func TestFrozenNeverReplaced(t *testing.T) {

	const quarter = MessageAggregateLimit / 4
	first := conversation.ToolResultBlock{ToolUseID: "t1", Content: strings.Repeat("a", quarter)}
	conv := oneToolResultMsg(first)
	state := New()
	dir := t.TempDir()

	if _, _, err := Apply(conv, dir, state); err != nil {
		t.Fatalf("turn 1 Apply: %v", err)
	}
	if _, ok := state.Replacements["t1"]; ok {
		t.Fatal("t1 should not be replaced after turn 1")
	}

	huge := conversation.ToolResultBlock{
		ToolUseID: "t2",
		Content:   strings.Repeat("b", quarter*3+200),
	}
	convT2 := oneToolResultMsg(first, huge)

	api, _, err := Apply(convT2, dir, state)
	if err != nil {
		t.Fatalf("turn 2 Apply: %v", err)
	}

	var t1Got string
	for _, tr := range api.GetMessages()[0].ToolResults {
		if tr.ToolUseID == "t1" {
			t1Got = tr.Content
		}
	}
	if t1Got != first.Content {
		t.Fatalf("t1 unexpectedly replaced: %q (want raw)", t1Got[:50])
	}
	if _, ok := state.Replacements["t1"]; ok {
		t.Fatal("t1 was inserted into Replacements despite being frozen")
	}
}

func TestAggregateOnlyPicksFresh(t *testing.T) {

	bigUnder := SingleResultLimit - 1
	var rs []conversation.ToolResultBlock
	for _, id := range []string{"t1", "t2", "t3", "t4", "t5"} {
		rs = append(rs, conversation.ToolResultBlock{ToolUseID: id, Content: strings.Repeat("a", bigUnder)})
	}
	conv := conversation.NewManager()
	conv.AddToolResultsMessage(rs)
	state := New()

	api, recs, err := Apply(conv, t.TempDir(), state)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	total := 0
	for _, tr := range api.GetMessages()[0].ToolResults {
		total += len(tr.Content)
	}
	if total > MessageAggregateLimit {
		t.Fatalf("api_conv aggregate %d still over limit %d", total, MessageAggregateLimit)
	}
	if len(recs) < 1 {
		t.Fatal("expected at least one replacement record")
	}
	for _, id := range []string{"t1", "t2", "t3", "t4", "t5"} {
		if _, ok := state.SeenIDs[id]; !ok {
			t.Fatalf("%s not in SeenIDs", id)
		}
	}
}

func TestReconstructFromRecords(t *testing.T) {
	msgs := []conversation.Message{
		{
			Role: "user",
			ToolResults: []conversation.ToolResultBlock{
				{ToolUseID: "t1", Content: "raw"},
				{ToolUseID: "t2", Content: "raw"},
			},
		},
	}
	records := []Record{
		{Kind: "tool-result", ToolUseID: "t1", Replacement: "t1_preview"},
	}

	state := Reconstruct(msgs, records, nil)
	if _, ok := state.SeenIDs["t1"]; !ok {
		t.Fatal("t1 missing from SeenIDs")
	}
	if _, ok := state.SeenIDs["t2"]; !ok {
		t.Fatal("t2 missing from SeenIDs")
	}
	if state.Replacements["t1"] != "t1_preview" {
		t.Fatalf("t1 replacement wrong: %q", state.Replacements["t1"])
	}
	if _, ok := state.Replacements["t2"]; ok {
		t.Fatal("t2 should remain frozen-unreplaced after reconstruct")
	}
}

func TestReconstructWithInheritedParent(t *testing.T) {
	msgs := []conversation.Message{
		{
			Role: "user",
			ToolResults: []conversation.ToolResultBlock{
				{ToolUseID: "t_parent", Content: "raw"},
				{ToolUseID: "t_child", Content: "raw"},
			},
		},
	}
	records := []Record{
		{Kind: "tool-result", ToolUseID: "t_child", Replacement: "child_preview"},
	}
	inherited := map[string]string{"t_parent": "parent_preview"}

	state := Reconstruct(msgs, records, inherited)
	if state.Replacements["t_child"] != "child_preview" {
		t.Fatalf("t_child not from records: %q", state.Replacements["t_child"])
	}
	if state.Replacements["t_parent"] != "parent_preview" {
		t.Fatalf("t_parent not gap-filled from inherited: %q", state.Replacements["t_parent"])
	}
}
