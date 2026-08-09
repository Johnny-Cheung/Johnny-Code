package toolresult

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"johnnycode/internal/conversation"
)

const (
	SingleResultLimit = 50000

	MessageAggregateLimit = 200000

	OldResultSnipChars = 2000

	KeepRecentTurns = 10

	SpillSubdir = ".johnnycode/tool_results"
)

const (
	persistedTagPrefix = "[Result of "
	snippedTagPrefix   = "[Stale output snipped:"
)

func isAlreadyReplaced(s string) bool {
	return strings.HasPrefix(s, persistedTagPrefix) || strings.HasPrefix(s, snippedTagPrefix)
}

func Apply(
	conv *conversation.Manager,
	workDir string,
	state *ContentReplacementState,
) (*conversation.Manager, []Record, error) {
	return apply(conv, workDir, state, nil)
}

// ApplyWithFresh gives newly-created tool results one pass through the model
// before the normal spill policy is applied. This keeps the first response
// complete when it fits, while the next turn still gets the 50k/200k budget
// protections. Results are intentionally not frozen in state during this
// pass; the following Apply call can therefore spill them if necessary.
func ApplyWithFresh(
	conv *conversation.Manager,
	workDir string,
	state *ContentReplacementState,
	freshIDs map[string]struct{},
) (*conversation.Manager, []Record, error) {
	return apply(conv, workDir, state, freshIDs)
}

func apply(
	conv *conversation.Manager,
	workDir string,
	state *ContentReplacementState,
	freshIDs map[string]struct{},
) (*conversation.Manager, []Record, error) {
	messages := conv.GetMessages()
	if len(messages) == 0 {
		return conv, nil, nil
	}

	spillDir := filepath.Join(workDir, SpillSubdir)
	absSpillDir, _ := filepath.Abs(spillDir)
	toolUseByID := buildToolUseIndex(messages)

	var records []Record
	newMessages := make([]conversation.Message, len(messages))

	for i, msg := range messages {
		if len(msg.ToolResults) == 0 {
			newMessages[i] = msg
			continue
		}

		decisions := make(map[string]string, len(msg.ToolResults))
		var fresh []conversation.ToolResultBlock

		for _, tr := range msg.ToolResults {
			if rep, ok := state.Replacements[tr.ToolUseID]; ok {

				decisions[tr.ToolUseID] = rep
				continue
			}
			if _, ok := state.SeenIDs[tr.ToolUseID]; ok {

				decisions[tr.ToolUseID] = tr.Content
				continue
			}
			if isAlreadyReplaced(tr.Content) {

				state.SeenIDs[tr.ToolUseID] = struct{}{}
				state.Replacements[tr.ToolUseID] = tr.Content
				decisions[tr.ToolUseID] = tr.Content
				records = append(records, Record{
					Kind:        "tool-result",
					ToolUseID:   tr.ToolUseID,
					Replacement: tr.Content,
				})
				continue
			}
			fresh = append(fresh, tr)
		}

		persistedByP1 := make(map[string]struct{})
		for _, tr := range fresh {
			_, preserveFresh := freshIDs[tr.ToolUseID]
			if len(tr.Content) <= SingleResultLimit || preserveFresh {
				continue
			}
			if isSpillReadback(toolUseByID[tr.ToolUseID], absSpillDir) {

				state.SeenIDs[tr.ToolUseID] = struct{}{}
				decisions[tr.ToolUseID] = tr.Content
				persistedByP1[tr.ToolUseID] = struct{}{}
				continue
			}
			path, err := writeSpill(spillDir, tr.ToolUseID, tr.Content)
			if err != nil {

				state.SeenIDs[tr.ToolUseID] = struct{}{}
				decisions[tr.ToolUseID] = tr.Content
				persistedByP1[tr.ToolUseID] = struct{}{}
				continue
			}
			preview := buildSpillPreview(len(tr.Content), path)
			decisions[tr.ToolUseID] = preview
			state.SeenIDs[tr.ToolUseID] = struct{}{}
			state.Replacements[tr.ToolUseID] = preview
			records = append(records, Record{
				Kind:        "tool-result",
				ToolUseID:   tr.ToolUseID,
				Replacement: preview,
			})
			persistedByP1[tr.ToolUseID] = struct{}{}
		}

		var remaining []conversation.ToolResultBlock
		for _, tr := range fresh {
			if _, done := persistedByP1[tr.ToolUseID]; done {
				continue
			}
			remaining = append(remaining, tr)
		}

		total := 0
		for _, c := range decisions {
			total += len(c)
		}
		for _, tr := range remaining {
			total += len(tr.Content)
		}

		if total > MessageAggregateLimit && len(remaining) > 0 {
			sorted := append([]conversation.ToolResultBlock(nil), remaining...)
			sort.SliceStable(sorted, func(a, b int) bool {
				return len(sorted[a].Content) > len(sorted[b].Content)
			})
			for _, tr := range sorted {
				if total <= MessageAggregateLimit {
					break
				}
				if isSpillReadback(toolUseByID[tr.ToolUseID], absSpillDir) {
					state.SeenIDs[tr.ToolUseID] = struct{}{}
					decisions[tr.ToolUseID] = tr.Content
					continue
				}
				path, err := writeSpill(spillDir, tr.ToolUseID, tr.Content)
				if err != nil {
					state.SeenIDs[tr.ToolUseID] = struct{}{}
					decisions[tr.ToolUseID] = tr.Content
					continue
				}
				preview := buildSpillPreview(len(tr.Content), path)
				decisions[tr.ToolUseID] = preview
				state.SeenIDs[tr.ToolUseID] = struct{}{}
				state.Replacements[tr.ToolUseID] = preview
				records = append(records, Record{
					Kind:        "tool-result",
					ToolUseID:   tr.ToolUseID,
					Replacement: preview,
				})
				total -= len(tr.Content) - len(preview)
			}
		}

		for _, tr := range fresh {
			if _, decided := decisions[tr.ToolUseID]; decided {
				continue
			}
			if _, preserveFresh := freshIDs[tr.ToolUseID]; preserveFresh {
				// Leave this ID out of SeenIDs so the next turn can apply the
				// ordinary spill policy to the same raw result.
				decisions[tr.ToolUseID] = tr.Content
				continue
			}
			state.SeenIDs[tr.ToolUseID] = struct{}{}
			decisions[tr.ToolUseID] = tr.Content
		}

		newResults := make([]conversation.ToolResultBlock, len(msg.ToolResults))
		for k, tr := range msg.ToolResults {
			newResults[k] = conversation.ToolResultBlock{
				ToolUseID: tr.ToolUseID,
				Content:   decisions[tr.ToolUseID],
				IsError:   tr.IsError,
			}
		}
		newMsg := msg
		newMsg.ToolResults = newResults
		newMessages[i] = newMsg
	}

	newMessages = snipStale(newMessages)

	return buildManager(newMessages), records, nil
}

func buildSpillPreview(originalSize int, path string) string {
	return fmt.Sprintf(
		"[Result of %d chars saved to %s — read with ReadFile if needed]",
		originalSize, path,
	)
}

func snipStale(messages []conversation.Message) []conversation.Message {
	totalTurns := 0
	for _, m := range messages {
		if m.Role == "assistant" && len(m.ToolUses) == 0 {
			totalTurns++
		}
	}
	if totalTurns <= KeepRecentTurns {
		return messages
	}
	out := make([]conversation.Message, len(messages))
	turnsSeen := 0
	oldBoundary := totalTurns - KeepRecentTurns
	for i, m := range messages {
		if m.Role == "assistant" && len(m.ToolUses) == 0 {
			turnsSeen++
		}
		if turnsSeen > oldBoundary || len(m.ToolResults) == 0 {
			out[i] = m
			continue
		}
		var newResults []conversation.ToolResultBlock
		changed := false
		for _, tr := range m.ToolResults {
			if isAlreadyReplaced(tr.Content) || len(tr.Content) <= OldResultSnipChars {
				newResults = append(newResults, tr)
				continue
			}
			newResults = append(newResults, conversation.ToolResultBlock{
				ToolUseID: tr.ToolUseID,
				Content:   fmt.Sprintf("[Stale output snipped: %d chars]", len(tr.Content)),
				IsError:   tr.IsError,
			})
			changed = true
		}
		if changed {
			nm := m
			nm.ToolResults = newResults
			out[i] = nm
		} else {
			out[i] = m
		}
	}
	return out
}

func buildToolUseIndex(messages []conversation.Message) map[string]conversation.ToolUseBlock {
	idx := make(map[string]conversation.ToolUseBlock, len(messages))
	for _, m := range messages {
		for _, tu := range m.ToolUses {
			idx[tu.ToolUseID] = tu
		}
	}
	return idx
}

func isSpillReadback(tu conversation.ToolUseBlock, absSpillDir string) bool {
	if tu.ToolName != "ReadFile" || absSpillDir == "" {
		return false
	}
	raw, _ := tu.Arguments["file_path"].(string)
	if raw == "" {
		return false
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return false
	}
	return strings.HasPrefix(abs, absSpillDir)
}

func writeSpill(dir, toolUseID, content string) (string, error) {
	if toolUseID == "" {
		return "", fmt.Errorf("empty tool_use_id")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, toolUseID)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return path, nil
		}
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return "", err
	}
	return path, nil
}

func buildManager(messages []conversation.Message) *conversation.Manager {
	out := conversation.NewManager()
	for _, m := range messages {
		switch {
		case len(m.ToolUses) > 0:
			out.AddAssistantFull(m.Content, m.ThinkingBlocks, m.ToolUses)
		case len(m.ToolResults) > 0:
			out.AddToolResultsMessage(m.ToolResults)
		case m.Role == "user":
			out.AddUserMessage(m.Content)
		case m.Role == "assistant":
			out.AddAssistantMessage(m.Content)
		}
	}
	return out
}
