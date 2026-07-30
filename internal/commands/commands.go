package commands

import (
	"fmt"
	"sort"
	"strings"
)

type CommandType string

const (
	TypeLocal   CommandType = "local"
	TypeLocalUI CommandType = "local-ui"
	TypePrompt  CommandType = "prompt"
	// TypeSkillFork is for skills declared with `mode: fork`. The handler
	// runs the skill in an isolated sub-agent (no main-loop touch) and
	// returns the final assistant text; the TUI dispatcher inserts that
	// text into the main chat as an assistant message instead of pumping
	// it through the regular Agent Loop.
	TypeSkillFork CommandType = "skill-fork"
)

type Context struct {
	Args            string
	MemoryList      func() []string
	MemoryClear     func()
	TokenCount      func() (input, output int)
	PermissionMode    func() string
	SetPermissionMode func(mode string) error
	ToolCount         func() int
	SessionInfo     func() string
	SkillList       func() []SkillInfo
	MCPInfo         func() string
	WorkDir         string
	Model           string
}

type SkillInfo struct {
	Name        string
	Description string
}

type Handler func(ctx *Context) string

type Command struct {
	Name        string
	Description string
	Aliases     []string
	Type        CommandType
	ArgPrompt   string
	Hidden      bool
	Handler     Handler
}

type Registry struct {
	commands map[string]*Command
	aliases  map[string]string
}

func NewRegistry() *Registry {
	return &Registry{
		commands: make(map[string]*Command),
		aliases:  make(map[string]string),
	}
}

func (r *Registry) Register(cmd *Command) {
	if _, exists := r.commands[cmd.Name]; exists {
		panic(fmt.Sprintf("commands: duplicate command name %q", cmd.Name))
	}
	if owner, exists := r.aliases[cmd.Name]; exists {
		panic(fmt.Sprintf("commands: command name %q collides with alias of %q", cmd.Name, owner))
	}
	for _, alias := range cmd.Aliases {
		if _, exists := r.commands[alias]; exists {
			panic(fmt.Sprintf("commands: alias %q for %q collides with existing command name", alias, cmd.Name))
		}
		if owner, exists := r.aliases[alias]; exists {
			panic(fmt.Sprintf("commands: alias %q for %q already registered by %q", alias, cmd.Name, owner))
		}
	}
	r.commands[cmd.Name] = cmd
	for _, alias := range cmd.Aliases {
		r.aliases[alias] = cmd.Name
	}
}

// HasConflict reports whether cmd's name or any of its aliases would collide
// with something already in the registry. Dynamic loaders (e.g. user
// commands from disk) should call this before Register to filter out
// conflicting entries, since Register panics on collision.
func (r *Registry) HasConflict(cmd *Command) bool {
	if r.Find(cmd.Name) != nil {
		return true
	}
	for _, alias := range cmd.Aliases {
		if r.Find(alias) != nil {
			return true
		}
	}
	return false
}

func (r *Registry) Find(name string) *Command {
	if cmd, ok := r.commands[name]; ok {
		return cmd
	}
	if canonical, ok := r.aliases[name]; ok {
		return r.commands[canonical]
	}
	return nil
}

func (r *Registry) ListCommands() []*Command {
	var cmds []*Command
	for _, cmd := range r.commands {
		if !cmd.Hidden {
			cmds = append(cmds, cmd)
		}
	}
	sort.Slice(cmds, func(i, j int) bool {
		return cmds[i].Name < cmds[j].Name
	})
	return cmds
}

func (r *Registry) Complete(prefix string) []string {
	var matches []string
	for name, cmd := range r.commands {
		if !cmd.Hidden && strings.HasPrefix(name, prefix) {
			matches = append(matches, name)
		}
	}
	for alias, canonical := range r.aliases {
		if cmd := r.commands[canonical]; cmd != nil && !cmd.Hidden && strings.HasPrefix(alias, prefix) {
			matches = append(matches, alias)
		}
	}
	sort.Strings(matches)
	return matches
}

func Parse(input string) (name string, args string) {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "/") {
		return "", ""
	}
	input = input[1:]
	parts := strings.SplitN(input, " ", 2)
	name = strings.ToLower(parts[0])
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}
	return
}

func CreateDefaultRegistry() *Registry {
	r := NewRegistry()

	r.Register(&Command{
		Name:        "help",
		Description: "显示可用命令",
		Aliases:     []string{"h", "?"},
		Type:        TypeLocal,
		Handler: func(ctx *Context) string {
			if ctx.Args != "" {
				cmd := r.Find(ctx.Args)
				if cmd == nil {
					return fmt.Sprintf("未知命令：%s", ctx.Args)
				}
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("/%s — %s\n", cmd.Name, cmd.Description))
				if len(cmd.Aliases) > 0 {
					sb.WriteString(fmt.Sprintf("  别名：%s\n", strings.Join(cmd.Aliases, ", ")))
				}
				if cmd.ArgPrompt != "" {
					sb.WriteString(fmt.Sprintf("  用法：%s\n", cmd.ArgPrompt))
				}
				return sb.String()
			}
			var sb strings.Builder
			sb.WriteString("可用命令：\n\n")
			for _, cmd := range r.ListCommands() {
				aliases := ""
				if len(cmd.Aliases) > 0 {
					aliases = ", /" + strings.Join(cmd.Aliases, ", /")
				}
				sb.WriteString(fmt.Sprintf("  /%s%s\n    %s\n", cmd.Name, aliases, cmd.Description))
			}
			sb.WriteString("\n输入 /help <命令> 查看详情。")
			return sb.String()
		},
	})

	r.Register(&Command{
		Name:        "mcp",
		Description: "显示 MCP 服务器状态",
		Type:        TypeLocal,
		Handler: func(ctx *Context) string {
			if ctx.MCPInfo == nil {
				return "未配置 MCP 服务器"
			}
			info := ctx.MCPInfo()
			if info == "" {
				return "没有已连接的 MCP 服务器"
			}
			return info
		},
	})

	r.Register(&Command{
		Name:        "clear",
		Description: "清除对话并重新开始",
		Type:        TypeLocalUI,
	})

	r.Register(&Command{
		Name:        "compact",
		Description: "压缩对话上下文",
		Aliases:     []string{"c"},
		Type:        TypeLocalUI,
	})

	r.Register(&Command{
		Name:        "status",
		Description: "显示当前状态",
		Aliases:     []string{"s"},
		Type:        TypeLocal,
		Handler: func(ctx *Context) string {
			var sb strings.Builder
			sb.WriteString("Johnny Code 状态\n")
			sb.WriteString("──────────────\n")
			sb.WriteString(fmt.Sprintf("  模式：     %s\n", ctx.PermissionMode()))
			input, output := ctx.TokenCount()
			sb.WriteString(fmt.Sprintf("  Token：    %d 入 / %d 出\n", input, output))
			sb.WriteString(fmt.Sprintf("  工具：     %d 已启用\n", ctx.ToolCount()))
			memories := ctx.MemoryList()
			sb.WriteString(fmt.Sprintf("  记忆：     %d 条\n", len(memories)))
			sb.WriteString(fmt.Sprintf("  模型：     %s\n", ctx.Model))
			sb.WriteString(fmt.Sprintf("  目录：     %s\n", ctx.WorkDir))
			return sb.String()
		},
	})

	r.Register(&Command{
		Name:        "memory",
		Description: "管理自动记忆",
		Type:        TypeLocal,
		Handler: func(ctx *Context) string {
			sub, subArgs := parseSubcommand(ctx.Args)

			switch sub {
			case "", "list":
				memories := ctx.MemoryList()
				if len(memories) == 0 {
					return "暂无存储的记忆。"
				}
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("记忆（%d 条）：\n\n", len(memories)))
				for i, mem := range memories {
					preview := mem
					if len(preview) > 80 {
						preview = preview[:80] + "…"
					}
					sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, preview))
				}
				return sb.String()

			case "clear":
				ctx.MemoryClear()
				return "所有自动记忆已清除。"

			default:
				_ = subArgs
				return "用法：/memory [list|clear]"
			}
		},
	})

	r.Register(&Command{
		Name:        "plan",
		Description: "切换到计划模式（只读）",
		Aliases:     []string{"p"},
		Type:        TypeLocalUI,
	})

	r.Register(&Command{
		Name:        "session",
		Description: "会话管理",
		Type:        TypeLocal,
		Handler: func(ctx *Context) string {
			sub, _ := parseSubcommand(ctx.Args)
			switch sub {
			case "", "info":
				return ctx.SessionInfo()
			case "list":
				return ctx.SessionInfo()
			default:
				return "用法：/session [list|info]"
			}
		},
	})

	r.Register(&Command{
		Name:        "permission",
		Description: "权限管理",
		Aliases:     []string{"perm"},
		Type:        TypeLocal,
		Handler: func(ctx *Context) string {
			sub, rest := parseSubcommand(ctx.Args)
			switch sub {
			case "", "info":
				return fmt.Sprintf("当前权限模式：%s", ctx.PermissionMode())
			case "mode":
				if rest == "" {
					return "用法：/permission mode <default|acceptEdits|plan|bypassPermissions>"
				}
				if ctx.SetPermissionMode == nil {
					return "此上下文中不支持切换权限模式。"
				}
				if err := ctx.SetPermissionMode(rest); err != nil {
					return err.Error()
				}
				return fmt.Sprintf("权限模式已设置为：%s", rest)
			default:
				return "用法：/permission [info|mode <模式>|rules]"
			}
		},
	})

	r.Register(&Command{
		Name:        "resume",
		Description: "恢复之前的会话",
		Aliases:     []string{"r"},
		Type:        TypeLocalUI,
	})

	r.Register(&Command{
		Name:        "rewind",
		Description: "将对话和文件回退到之前的检查点",
		Type:        TypeLocalUI,
	})

	r.Register(&Command{
		Name:        "skills",
		Description: "列出可用技能",
		Type:        TypeLocal,
		Handler: func(ctx *Context) string {
			skills := ctx.SkillList()
			if len(skills) == 0 {
				return "没有已安装的技能。\n\n将技能添加到 .johnnycode/skills/<技能名>/SKILL.md"
			}
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("可用技能（%d）：\n\n", len(skills)))
			for _, s := range skills {
				desc := s.Description
				if len(desc) > 100 {
					desc = desc[:100] + "…"
				}
				sb.WriteString(fmt.Sprintf("  /%s\n    %s\n\n", s.Name, desc))
			}
			sb.WriteString("输入 /<技能名> 来调用技能。")
			return sb.String()
		},
	})

	r.Register(&Command{
		Name:        "review",
		Description: "审查当前代码更改",
		Type:        TypePrompt,
		Handler: func(ctx *Context) string {
			prompt := "Please review the current git diff for code changes. Focus on:\n" +
				"1. Logic errors\n2. Security issues\n3. Performance problems\n4. Code style"
			if ctx.Args != "" {
				prompt += "\n\nAdditional focus: " + ctx.Args
			}
			return prompt
		},
	})

	return r
}

func parseSubcommand(args string) (sub string, rest string) {
	args = strings.TrimSpace(args)
	if args == "" {
		return "", ""
	}
	parts := strings.SplitN(args, " ", 2)
	sub = strings.ToLower(parts[0])
	if len(parts) > 1 {
		rest = strings.TrimSpace(parts[1])
	}
	return
}
