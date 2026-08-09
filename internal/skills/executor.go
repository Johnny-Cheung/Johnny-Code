package skills

import (
	"context"
	"fmt"

	"johnnycode/internal/conversation"
	"johnnycode/internal/tools"
)





type SkillHost interface {
	
	
	
	ActivateSkill(name, body string)
	
	
	SetToolFilter(allow func(name string) bool)
	
	
	
	
	ToolRegistry() *tools.Registry
}






type SkillForkHost interface {
	SkillHost
	
	
	
	
	RunSubAgent(ctx context.Context, body string, seed []conversation.Message, allowedTools []string, model string) (string, error)
	
	
	
	SnapshotParentMessages() []conversation.Message
}











func RunInline(_ context.Context, skill *Skill, args string, host SkillHost) (string, error) {
	if err := assertAllowedToolsExist(skill, host.ToolRegistry()); err != nil {
		return "", err
	}
	body := skill.renderBody(args)
	host.ActivateSkill(skill.Meta.Name, body)
	if len(skill.Meta.AllowedTools) > 0 {
		allowed := skillToolAllowSet(skill)
		host.SetToolFilter(func(name string) bool { return allowed[name] })
	} else {
		host.SetToolFilter(nil)
	}
	return body, nil
}












func RunFork(ctx context.Context, skill *Skill, args string, host SkillForkHost) (string, error) {
	if err := assertAllowedToolsExist(skill, host.ToolRegistry()); err != nil {
		return "", err
	}
	body := skill.renderBody(args)
	seed := buildForkSeed(skill.Meta.ForkContext, host.SnapshotParentMessages())
	return host.RunSubAgent(ctx, body, seed, skill.Meta.AllowedTools, skill.Meta.Model)
}






func assertAllowedToolsExist(skill *Skill, reg *tools.Registry) error {
	for _, name := range skill.Meta.AllowedTools {
		if reg.Get(name) == nil {
			return fmt.Errorf("skill %q declares allowed tool %q which is not registered", skill.Meta.Name, name)
		}
	}
	return nil
}





func skillToolAllowSet(skill *Skill) map[string]bool {
	out := make(map[string]bool, len(skill.Meta.AllowedTools))
	for _, name := range skill.Meta.AllowedTools {
		out[name] = true
	}
	return out
}









func buildForkSeed(mode string, parent []conversation.Message) []conversation.Message {
	switch mode {
	case "full":
		out := make([]conversation.Message, len(parent))
		copy(out, parent)
		return out
	case "recent":
		if len(parent) <= 5 {
			out := make([]conversation.Message, len(parent))
			copy(out, parent)
			return out
		}
		out := make([]conversation.Message, 5)
		copy(out, parent[len(parent)-5:])
		return out
	default:
		return nil
	}
}
