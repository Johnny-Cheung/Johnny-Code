
package toolresult

import "johnnycode/internal/conversation"








func Reconstruct(
	messages []conversation.Message,
	records []Record,

	inheritedReplacements map[string]string,
) *ContentReplacementState {
	state := New()
	candidateIDs := make(map[string]struct{})
	for _, m := range messages {
		for _, tr := range m.ToolResults {
			candidateIDs[tr.ToolUseID] = struct{}{}
		}
	}
	for id := range candidateIDs {
		state.SeenIDs[id] = struct{}{}

	}
	for _, r := range records {
		if r.Kind != "tool-result" {
			continue
		}
		if _, ok := candidateIDs[r.ToolUseID]; ok {
			state.Replacements[r.ToolUseID] = r.Replacement
		}
	}
	for id, rep := range inheritedReplacements {
		if _, ok := candidateIDs[id]; !ok {
			continue
		}
		if _, exists := state.Replacements[id]; exists {
			continue
		}
		state.Replacements[id] = rep
	}
	return state
}

