package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/skill"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

const permissionUsage = `用法：
  /permissions                     查看固定名单和项目规则
  /permissions remove-command 序号  删除一条项目命令前缀
  /permissions remove-path 序号     删除一条项目路径
  /permissions clear               清空项目规则`

const skillUsage = `用法：
  /skills                    查看已加载、被覆盖、未加载和未注入提示的 Skill
  /skills reload             重新扫描项目级和用户级目录
  /skill <name> [参数]       显式使用某个 Skill`

const contextUsage = `用法：
  /context                   查看上下文估算、预算、摘要和存档
  /compact                   手动压缩较早的已完成轮次`

// localCommandResult 表示本地命令的结果。Prompt 非空时作为本轮任务发送，
// Compact 为 true 时由调用方执行一次手动上下文压缩。
type localCommandResult struct {
	Output  string
	Prompt  string
	Compact bool
}

type contextController struct {
	stats   func() string
	compact func(context.Context) (string, error)
}

type localCommandHandler struct {
	rules   *tool.ProjectRules
	skills  *skill.Store
	reload  func() error
	context *contextController
}

func newLocalCommandHandler(rules *tool.ProjectRules, skills *skill.Store, reload func() error, controllers ...*contextController) *localCommandHandler {
	var contextController *contextController
	if len(controllers) > 0 {
		contextController = controllers[0]
	}
	return &localCommandHandler{rules: rules, skills: skills, reload: reload, context: contextController}
}

func (h *localCommandHandler) Handle(line string) (localCommandResult, bool) {
	if result, handled := handleSkillCommand(line, h.skills, h.reload); handled {
		return result, true
	}
	if result, handled := handleContextCommand(line, h.context); handled {
		return result, true
	}
	if text, handled := handleLocalCommand(line, h.rules); handled {
		return localCommandResult{Output: text}, true
	}
	if result, handled := handleDirectSkillCommand(line, h.skills); handled {
		return result, true
	}
	return localCommandResult{}, false
}

func handleContextCommand(line string, controller *contextController) (localCommandResult, bool) {
	line = strings.TrimSpace(line)
	switch {
	case line == "/context":
		if controller == nil || controller.stats == nil {
			return localCommandResult{Output: "当前会话未启用上下文治理。"}, true
		}
		return localCommandResult{Output: controller.stats()}, true
	case strings.HasPrefix(line, "/context "):
		return localCommandResult{Output: contextUsage}, true
	case line == "/compact":
		if controller == nil || controller.compact == nil {
			return localCommandResult{Output: "当前会话未启用上下文治理。"}, true
		}
		return localCommandResult{Compact: true}, true
	case strings.HasPrefix(line, "/compact "):
		return localCommandResult{Output: contextUsage}, true
	}
	return localCommandResult{}, false
}

// handleDirectSkillCommand 支持把每个 Skill 作为 /skill-name 独立命令使用。
// 内置命令由调用方先处理，因此同名 Skill 不会覆盖 /skills、/permissions 等。
func handleDirectSkillCommand(line string, skills *skill.Store) (localCommandResult, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") {
		return localCommandResult{}, false
	}
	rest := strings.TrimPrefix(line, "/")
	if rest == "" {
		return localCommandResult{}, false
	}
	name, arguments := rest, ""
	if index := strings.IndexAny(rest, " \t"); index >= 0 {
		name = rest[:index]
		arguments = strings.TrimSpace(rest[index+1:])
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "/\\") {
		return localCommandResult{}, false
	}
	item, ok := skills.Lookup(name)
	if !ok {
		return localCommandResult{}, false
	}
	prompt := "请使用下面的 Skill 完成任务。\n\n" + item.Loaded(arguments)
	return localCommandResult{Prompt: prompt}, true
}

func handleSkillCommand(line string, skills *skill.Store, reload func() error) (localCommandResult, bool) {
	line = strings.TrimSpace(line)
	switch {
	case line == "/skills":
		return localCommandResult{Output: skillSummary(skills)}, true
	case line == "/skills reload":
		if reload == nil {
			return localCommandResult{Output: "当前模式未加载 Skill。"}, true
		}
		if err := reload(); err != nil {
			return localCommandResult{Output: "Skill 重载失败：" + err.Error() + "；已保留原集合。"}, true
		}
		return localCommandResult{Output: "Skill 已重载。\n\n" + skillSummary(skills)}, true
	case strings.HasPrefix(line, "/skills "):
		return localCommandResult{Output: skillUsage}, true
	case line == "/skill":
		return localCommandResult{Output: skillUsage}, true
	case !strings.HasPrefix(line, "/skill "):
		return localCommandResult{}, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, "/skill"))
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return localCommandResult{Output: skillUsage}, true
	}
	name := fields[0]
	arguments := strings.TrimSpace(rest[len(name):])
	item, ok := skills.Lookup(name)
	if !ok {
		names := skills.Names()
		if len(names) == 0 {
			return localCommandResult{Output: "没有可用的 Skill；请检查项目 .johnny/skills 或用户 ~/.johnny/skills 目录。"}, true
		}
		return localCommandResult{Output: fmt.Sprintf("未知 Skill：%s；可用 Skill：%s", name, strings.Join(names, "、"))}, true
	}
	prompt := "请使用下面的 Skill 完成任务。\n\n" + item.Loaded(arguments)
	return localCommandResult{Prompt: prompt}, true
}

func skillSummary(skills *skill.Store) string {
	stats := skills.Stats()
	if stats.Loaded == 0 && stats.Problems == 0 && stats.Shadowed == 0 {
		project, user := skills.Dirs()
		return "未发现 Skill。\n项目级：" + emptyPath(project) + "\n用户级：" + emptyPath(user)
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "Skill：已加载 %d 个（项目 %d，用户 %d），注入系统提示 %d 个。\n",
		stats.Loaded, stats.Project, stats.User, stats.Injected)
	for _, item := range skills.Skills() {
		status := ""
		if !skills.Injected(item.Name) {
			status = "（未注入提示）"
		}
		fmt.Fprintf(&builder, "- %s [%s]%s：%s\n  %s\n", item.Name, item.Source.Label(), status, item.Description, item.Path)
	}
	if shadowed := skills.Shadowed(); len(shadowed) > 0 {
		builder.WriteString("\n被项目级覆盖：\n")
		for _, item := range shadowed {
			fmt.Fprintf(&builder, "- %s：%s\n", item.Name, item.Path)
		}
	}
	if omitted := skills.Omitted(); len(omitted) > 0 {
		builder.WriteString("\n未注入系统提示或未加载：\n")
		for _, item := range omitted {
			fmt.Fprintf(&builder, "- %s [%s]：%s\n", item.Name, item.Source.Label(), item.Reason)
		}
	}
	if problems := skills.Problems(); len(problems) > 0 {
		builder.WriteString("\n未加载：\n")
		for _, item := range problems {
			name := item.Name
			if name == "" {
				name = item.Path
			}
			fmt.Fprintf(&builder, "- %s [%s]：%s\n", name, item.Source.Label(), item.Reason)
		}
	}
	return strings.TrimRight(builder.String(), "\n")
}

func emptyPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return "未配置"
	}
	return path
}

// printSkillNotice 在启动时给出简短摘要；没有 Skill 或问题时保持安静。
func printSkillNotice(output io.Writer, skills *skill.Store) {
	stats := skills.Stats()
	if stats.Loaded == 0 && stats.Problems == 0 {
		return
	}
	if stats.Problems > 0 {
		fmt.Fprintf(output, "Skill：已加载 %d 个，未加载 %d 个；输入 /skills 查看。\n", stats.Loaded, stats.Problems)
		return
	}
	fmt.Fprintf(output, "Skill：已加载 %d 个（项目 %d，用户 %d）；输入 /skills 查看。\n", stats.Loaded, stats.Project, stats.User)
}

// handleLocalCommand 处理不进入模型的本地命令。第二个返回值表示是否已
// 处理；未处理的输入继续作为任务发送。
func handleLocalCommand(line string, rules *tool.ProjectRules) (string, bool) {
	line = strings.TrimSpace(line)
	if line != "/permissions" && !strings.HasPrefix(line, "/permissions ") {
		return "", false
	}
	fields := strings.Fields(line)
	if len(fields) == 1 {
		return tool.PermissionSummary(rules), true
	}
	if rules == nil {
		return "当前模式未加载项目规则；固定名单可用 /permissions 查看。", true
	}
	switch fields[1] {
	case "help":
		return permissionUsage, true
	case "clear":
		if len(fields) != 2 {
			return "用法：/permissions clear", true
		}
		if err := rules.Clear(); err != nil {
			return "清空项目规则失败：" + err.Error(), true
		}
		return "已清空项目规则。", true
	case "remove-command":
		if len(fields) != 3 {
			return "用法：/permissions remove-command 序号", true
		}
		index, err := parsePermissionIndex(fields[2])
		if err != nil {
			return err.Error(), true
		}
		if err := rules.RemovePrefix(index); err != nil {
			return "删除命令前缀失败：" + err.Error(), true
		}
		return fmt.Sprintf("已删除项目命令前缀 %d。", index), true
	case "remove-path":
		if len(fields) != 3 {
			return "用法：/permissions remove-path 序号", true
		}
		index, err := parsePermissionIndex(fields[2])
		if err != nil {
			return err.Error(), true
		}
		if err := rules.RemovePath(index); err != nil {
			return "删除路径失败：" + err.Error(), true
		}
		return fmt.Sprintf("已删除项目路径 %d。", index), true
	default:
		return permissionUsage, true
	}
}

func parsePermissionIndex(value string) (int, error) {
	index, err := strconv.Atoi(value)
	if err != nil || index < 1 {
		return 0, fmt.Errorf("序号必须是正整数：%s", value)
	}
	return index, nil
}
