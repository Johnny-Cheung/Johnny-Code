package compact

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)





const (
	RecoveryFileLimit      = 5
	RecoveryTokensPerFile  = 5_000
	RecoverySkillsBudget   = 25_000
	RecoveryTokensPerSkill = 5_000
	recoveryCharsPerToken  = 3.5
)




type FileReadRecord struct {
	Path      string
	Content   string
	Timestamp time.Time
}




type SkillInvocationRecord struct {
	Name      string
	Body      string
	Timestamp time.Time
}





type RecoveryState struct {
	mu     sync.Mutex
	files  map[string]FileReadRecord
	skills map[string]SkillInvocationRecord
}


func NewRecoveryState() *RecoveryState {
	return &RecoveryState{
		files:  map[string]FileReadRecord{},
		skills: map[string]SkillInvocationRecord{},
	}
}



func (s *RecoveryState) RecordFileRead(path, content string) {
	if s == nil || path == "" {
		return
	}
	s.mu.Lock()
	s.files[path] = FileReadRecord{Path: path, Content: content, Timestamp: time.Now()}
	s.mu.Unlock()
}



func (s *RecoveryState) RecordSkillInvocation(name, body string) {
	if s == nil || name == "" {
		return
	}
	s.mu.Lock()
	s.skills[name] = SkillInvocationRecord{Name: name, Body: body, Timestamp: time.Now()}
	s.mu.Unlock()
}


func (s *RecoveryState) snapshotFiles(limit int) []FileReadRecord {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]FileReadRecord, 0, len(s.files))
	for _, r := range s.files {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.After(out[j].Timestamp) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}


func (s *RecoveryState) snapshotSkills() []SkillInvocationRecord {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SkillInvocationRecord, 0, len(s.skills))
	for _, r := range s.skills {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.After(out[j].Timestamp) })
	return out
}






func BuildRecoveryAttachment(state *RecoveryState, toolSchemas []map[string]any) string {
	var sb strings.Builder

	if files := state.snapshotFiles(RecoveryFileLimit); len(files) > 0 {
		sb.WriteString("## Recently read files\n\n")
		sb.WriteString("These snapshots are what the file-reading tool last returned. Re-open with the tool if you need the current bytes.\n\n")
		for _, f := range files {
			content := truncateByTokens(f.Content, RecoveryTokensPerFile)
			ts := f.Timestamp.UTC().Format("2006-01-02T15:04:05Z")
			fmt.Fprintf(&sb, "### %s  (read %s)\n\n", f.Path, ts)
			sb.WriteString("```\n")
			sb.WriteString(content)
			if !strings.HasSuffix(content, "\n") {
				sb.WriteByte('\n')
			}
			sb.WriteString("```\n\n")
		}
	}

	if skills := state.snapshotSkills(); len(skills) > 0 {
		var section strings.Builder
		section.WriteString("## Active skills\n\n")
		section.WriteString("These skills were invoked earlier in the session. Continue to follow each SOP when its triggering condition applies.\n\n")
		used := 0
		emitted := false
		for _, sk := range skills {
			body := truncateByTokens(sk.Body, RecoveryTokensPerSkill)
			tokens := approxTokens(body) + approxTokens(sk.Name) + 8
			if used+tokens > RecoverySkillsBudget {
				break
			}
			used += tokens
			fmt.Fprintf(&section, "### %s\n\n%s\n\n", sk.Name, body)
			emitted = true
		}
		if emitted {
			sb.WriteString(section.String())
		}
	}

	if len(toolSchemas) > 0 {
		sb.WriteString("## Available tools\n\nYou still have access to the following tools — call them directly when the task needs one:\n\n")
		for _, t := range toolSchemas {
			name, _ := t["name"].(string)
			if name == "" {
				continue
			}
			desc, _ := t["description"].(string)
			desc = firstLine(desc)
			if desc != "" {
				fmt.Fprintf(&sb, "- %s — %s\n", name, desc)
			} else {
				fmt.Fprintf(&sb, "- %s\n", name)
			}
		}
		sb.WriteString("\n")
	}

	if sb.Len() == 0 {
		return ""
	}

	sb.WriteString("## Note\n\nEverything above the divider is reconstructed context. For exact code, error strings, or user-typed text, re-read the source rather than guess from the summary.\n")
	return sb.String()
}



func approxTokens(s string) int {
	if s == "" {
		return 0
	}
	return int(float64(len(s)) / recoveryCharsPerToken)
}




func truncateByTokens(s string, tokenBudget int) string {
	if tokenBudget <= 0 || s == "" {
		return s
	}
	if approxTokens(s) <= tokenBudget {
		return s
	}
	maxChars := int(float64(tokenBudget) * recoveryCharsPerToken)
	if maxChars <= 0 || maxChars >= len(s) {
		return s
	}
	return s[:maxChars] + "\n… (content truncated)"
}



func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
