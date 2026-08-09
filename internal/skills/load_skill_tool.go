package skills

import (
	"context"
	"fmt"

	"johnnycode/internal/tools"
)












type LoadSkillTool struct {
	Catalog *Catalog
	Host    SkillHost
}

func (t *LoadSkillTool) Name() string                 { return "LoadSkill" }

func (t *LoadSkillTool) Category() tools.ToolCategory { return tools.CategoryRead }

func (t *LoadSkillTool) IsSystemTool() bool           { return true }

func (t *LoadSkillTool) Description() string {
	return "Activate a Skill by name. The Skill's SOP gets pinned to the environment " +
		"context so it's visible at the top of every subsequent turn, and any tools the " +
		"Skill declares get registered in the current session. Call this when the user's " +
		"request matches one of the available Skills listed in the system prompt. Pass " +
		"the Skill name without a leading slash."
}

func (t *LoadSkillTool) Schema() map[string]any {
	return map[string]any{
		"name":        t.Name(),
		"description": t.Description(),
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "The Skill name to activate (e.g. \"commit\", \"backend-interview\").",
				},
			},
			"required": []string{"name"},
		},
	}
}

func (t *LoadSkillTool) Execute(_ context.Context, args map[string]any) tools.ToolResult {
	name, _ := args["name"].(string)
	if name == "" {
		return tools.ToolResult{Output: "name is required", IsError: true}
	}
	if t.Catalog == nil || t.Host == nil {
		return tools.ToolResult{Output: "LoadSkill not wired (Catalog or Host nil)", IsError: true}
	}
	skill, err := t.Catalog.GetFull(name)
	if err != nil && skill == nil {
		return tools.ToolResult{Output: fmt.Sprintf("unknown skill: %s", name), IsError: true}
	}
	if skill.PromptBody == "" {
		return tools.ToolResult{Output: fmt.Sprintf("skill %q has empty body — cannot activate", name), IsError: true}
	}

	t.Host.ActivateSkill(skill.Meta.Name, skill.PromptBody)

	registered := 0
	if skill.IsDirectory {
		if n, regErr := RegisterDirectoryTools(skill, t.Host.ToolRegistry()); regErr == nil {
			registered = n
		}
	}

	return tools.ToolResult{
		Output: fmt.Sprintf(
			"Skill %s activated. SOP pinned to env. %d specialized tools registered.",
			skill.Meta.Name, registered,
		),
	}
}
