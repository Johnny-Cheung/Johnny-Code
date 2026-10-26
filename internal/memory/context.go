package memory

import (
	"os"
	"strings"
)

// Context 组合 AGENTS 规则和两层记忆索引，供 Agent 在固定前缀中加载。
type Context struct {
	Agents  *Agents
	Store   *Store
	Rules   *Rules
	Warning string
}

func NewContext(workDir string) *Context {
	context := &Context{Agents: NewAgents(workDir)}
	home, err := os.UserHomeDir()
	if err != nil {
		context.Warning = "长期记忆初始化失败：" + err.Error()
		return context
	}
	context.Rules = NewRules(home)
	store, err := NewStoreAt(home, workDir)
	if err != nil {
		context.Warning = "长期记忆初始化失败：" + err.Error()
		return context
	}
	context.Store = store
	return context
}

// Load 返回固定注入文本和警告。写入路径不会调用它。
func (c *Context) Load() (string, string) {
	if c == nil {
		return "", ""
	}
	var sections []string
	var warnings []string
	if c.Warning != "" {
		warnings = append(warnings, c.Warning)
	}
	if c.Agents != nil {
		text, warning := c.Agents.Load()
		if text != "" {
			sections = append(sections, text)
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
	}
	if c.Store != nil {
		text, warning := c.Store.IndexText()
		if text != "" {
			sections = append(sections, text)
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
	}
	return strings.Join(sections, "\n\n"), strings.Join(warnings, "；")
}
