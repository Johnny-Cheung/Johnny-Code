
package skills

import (
	"strings"
)

type SkillMeta struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	WhenToUse    string   `yaml:"when_to_use"`
	Tags         []string `yaml:"tags"`
	AllowedTools []string `yaml:"allowed_tools"`
	
	
	Mode string `yaml:"mode"`
	
	Model string `yaml:"model"`
	
	
	Context string `yaml:"context"`
	
	
	
	ForkContext string `yaml:"fork_context"`
}



func (m SkillMeta) IsFork() bool {
	return m.Mode == "fork" || m.Context == "fork"
}

type Skill struct {
	Meta       SkillMeta
	PromptBody string
	SourceDir  string
	
	
	
	
	IsDirectory bool
	
	
	BodyLoaded bool
}







func (s *Skill) Render(args string) string {
	body := s.renderBody(args)
	if s.Meta.IsFork() {
		return s.renderForkDirective(body)
	}
	return body
}

func (s *Skill) renderBody(args string) string {
	body := s.PromptBody
	if strings.Contains(body, "$ARGUMENTS") {
		return strings.ReplaceAll(body, "$ARGUMENTS", args)
	}
	if strings.TrimSpace(args) == "" {
		return body
	}
	return body + "\n\n## User Request\n\n" + args
}




func (s *Skill) renderForkDirective(body string) string {
	var sb strings.Builder
	sb.WriteString("Run the skill `")
	sb.WriteString(s.Meta.Name)
	sb.WriteString("` in a forked sub-agent by calling the Agent tool with:\n")
	sb.WriteString("- subagent_type: general-purpose (or another agent if more appropriate)\n")
	if len(s.Meta.AllowedTools) > 0 {
		sb.WriteString("- restrict the sub-agent to these tools: ")
		sb.WriteString(strings.Join(s.Meta.AllowedTools, ", "))
		sb.WriteString("\n")
	}
	sb.WriteString("- prompt (pass verbatim to the sub-agent):\n\n")
	sb.WriteString(body)
	sb.WriteString("\n\nReport back only the sub-agent's final summary; do not perform the skill's steps yourself.")
	return sb.String()
}

