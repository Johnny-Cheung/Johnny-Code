package agents

import (
	"strings"

	"johnnycode/internal/tools"
)

var AllAgentDisallowedTools = map[string]bool{
	"TaskOutput":      true,
	"ExitPlanMode":    true,
	"EnterPlanMode":   true,
	"Agent":           true,
	"AskUserQuestion": true,
	"TaskStop":        true,
	"Workflow":        true,
}

var AsyncAgentAllowedTools = map[string]bool{
	"ReadFile":        true,
	"WebSearch":       true,
	"TodoWrite":       true,
	"Grep":            true,
	"WebFetch":        true,
	"Glob":            true,
	"Bash":            true,
	"EditFile":        true,
	"WriteFile":       true,
	"NotebookEdit":    true,
	"Skill":           true,
	"LoadSkill":       true,
	"SyntheticOutput": true,
	"ToolSearch":      true,
	"EnterWorktree":   true,
	"ExitWorktree":    true,
}

var InProcessTeammateAllowedTools = map[string]bool{
	"TaskCreate":  true,
	"TaskGet":     true,
	"TaskList":    true,
	"TaskUpdate":  true,
	"SendMessage": true,
	"CronCreate":  true,
	"CronDelete":  true,
	"CronList":    true,
}

func IsMCPTool(name string) bool {
	return strings.HasPrefix(name, "mcp__")
}

func FilterToolsForAgent(reg *tools.Registry, allowedTools, disallowedTools []string, isAsync bool) *tools.Registry {
	return FilterToolsForAgentEx(reg, allowedTools, disallowedTools, isAsync, false)
}

func FilterToolsForAgentEx(reg *tools.Registry, allowedTools, disallowedTools []string, isAsync, isInProcessTeammate bool) *tools.Registry {
	disallowed := make(map[string]bool, len(disallowedTools))
	for _, name := range disallowedTools {
		disallowed[name] = true
	}

	allowed := make(map[string]bool, len(allowedTools))
	hasWhitelist := len(allowedTools) > 0 && !(len(allowedTools) == 1 && allowedTools[0] == "*")
	for _, name := range allowedTools {
		allowed[name] = true
	}

	filtered := tools.NewRegistry()
	for _, t := range reg.ListTools() {
		name := t.Name()

		if IsMCPTool(name) {
			filtered.Register(t)
			continue
		}

		if AllAgentDisallowedTools[name] {
			continue
		}

		if isAsync && !AsyncAgentAllowedTools[name] {
			if isInProcessTeammate {

				if name == "Agent" || InProcessTeammateAllowedTools[name] {
					filtered.Register(t)
					continue
				}
			}
			continue
		}

		if disallowed[name] {
			continue
		}

		if hasWhitelist && !allowed[name] {
			continue
		}

		filtered.Register(t)
	}
	return filtered
}
