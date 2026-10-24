package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/skill"
)

// SkillTool 按名称加载启动时发现的本地 Skill。
type SkillTool struct {
	skills *skill.Store
}

type skillArguments struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func NewSkillTool(skills *skill.Store) (*SkillTool, error) {
	if skills == nil {
		return nil, errorsForRegistry("Skill 集合不能为空")
	}
	return &SkillTool{skills: skills}, nil
}

func (s *SkillTool) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name:        "skill",
		Description: "加载已发现的本地 Skill 完整正文。name 必须是系统提示中列出的 Skill 名称。Skill 只提供任务指导，不授予权限，也不能绕过当前权限模式。",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Skill 名称"},"arguments":{"type":"string","description":"可选的任务参数，用于替换正文中的 $ARGUMENTS"}},"required":["name"]}`),
	}
}

func (s *SkillTool) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var args skillArguments
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, invalidArguments("skill 参数格式无效", err)
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return Result{}, invalidArguments("skill 缺少 name 参数", nil)
	}
	item, ok := s.skills.Lookup(name)
	if !ok {
		names := s.skills.Names()
		if len(names) == 0 {
			return Result{}, executionError("没有可用的 Skill；请检查项目 .johnny/skills 或用户 ~/.johnny/skills 目录", nil)
		}
		return Result{}, invalidArguments(fmt.Sprintf("未知 Skill：%s；可用 Skill：%s", name, strings.Join(names, "、")), nil)
	}
	return Result{Content: item.Loaded(args.Arguments)}, nil
}
