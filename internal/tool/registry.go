package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/memory"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/skill"
)

var toolNamePattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

type Registry struct {
	tools map[string]Tool
	defs  []model.ToolDefinition
}

func NewRegistry(tools ...Tool) (*Registry, error) {
	registry := &Registry{tools: make(map[string]Tool), defs: make([]model.ToolDefinition, 0, len(tools))}
	for _, tool := range tools {
		if tool == nil {
			return nil, errorsForRegistry("工具实例不能为空")
		}
		definition := tool.Definition()
		if !toolNamePattern.MatchString(definition.Name) {
			return nil, errorsForRegistry(fmt.Sprintf("工具名称无效：%q", definition.Name))
		}
		if strings.TrimSpace(definition.Description) == "" {
			return nil, errorsForRegistry(fmt.Sprintf("工具描述不能为空：%s", definition.Name))
		}
		if !jsonObject(definition.InputSchema) {
			return nil, errorsForRegistry(fmt.Sprintf("工具输入 schema 必须是 JSON 对象：%s", definition.Name))
		}
		if _, exists := registry.tools[definition.Name]; exists {
			return nil, errorsForRegistry(fmt.Sprintf("工具名称重复：%s", definition.Name))
		}
		registry.tools[definition.Name] = tool
		definition.InputSchema = append(json.RawMessage(nil), definition.InputSchema...)
		registry.defs = append(registry.defs, definition)
	}
	sort.Slice(registry.defs, func(i, j int) bool { return registry.defs[i].Name < registry.defs[j].Name })
	return registry, nil
}

func errorsForRegistry(message string) error {
	return &Error{Kind: KindInvalidArguments, Message: message}
}

func NewDefaultRegistry(runtime Runtime) (*Registry, error) {
	runtime = normalizeRuntime(runtime)
	if strings.TrimSpace(runtime.WorkDir) == "" {
		return nil, errorsForRegistry("工具运行时工作目录不能为空")
	}
	return NewRegistry(
		NewBash(runtime),
		NewRead(runtime),
		NewEdit(runtime),
		NewWrite(runtime),
	)
}

// NewDefaultRegistryWithSkill 在四个内置工具之外注册 skill 工具。
func NewDefaultRegistryWithSkill(runtime Runtime, skills *skill.Store) (*Registry, error) {
	skillTool, err := NewSkillTool(skills)
	if err != nil {
		return nil, err
	}
	runtime = normalizeRuntime(runtime)
	if strings.TrimSpace(runtime.WorkDir) == "" {
		return nil, errorsForRegistry("工具运行时工作目录不能为空")
	}
	return NewRegistry(
		NewBash(runtime),
		NewRead(runtime),
		NewEdit(runtime),
		NewWrite(runtime),
		skillTool,
	)
}

// NewDefaultRegistryWithSkillAndMemory 在 skill 之外注册只读 memory 工具。
func NewDefaultRegistryWithSkillAndMemory(runtime Runtime, skills *skill.Store, store *memory.Store) (*Registry, error) {
	skillTool, err := NewSkillTool(skills)
	if err != nil {
		return nil, err
	}
	memoryTool, err := NewMemoryTool(store)
	if err != nil {
		return nil, err
	}
	runtime = normalizeRuntime(runtime)
	if strings.TrimSpace(runtime.WorkDir) == "" {
		return nil, errorsForRegistry("工具运行时工作目录不能为空")
	}
	return NewRegistry(
		NewBash(runtime),
		NewRead(runtime),
		NewEdit(runtime),
		NewWrite(runtime),
		skillTool,
		memoryTool,
	)
}

func (r *Registry) Get(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *Registry) Definitions() []model.ToolDefinition {
	if r == nil {
		return nil
	}
	definitions := make([]model.ToolDefinition, len(r.defs))
	for i, definition := range r.defs {
		definitions[i] = definition
		definitions[i].InputSchema = append(json.RawMessage(nil), definition.InputSchema...)
	}
	return definitions
}

func (r *Registry) Execute(ctx context.Context, call model.ToolCall) (Result, error) {
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	if r == nil {
		return Result{}, invalidArguments("工具注册表不能为空", nil)
	}
	tool, ok := r.tools[call.Name]
	if !ok {
		return Result{}, invalidArguments(fmt.Sprintf("未知工具：%s", call.Name), nil)
	}
	arguments := call.Arguments
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if !jsonObject(arguments) {
		return Result{}, invalidArguments("工具参数必须是 JSON 对象", nil)
	}
	return tool.Execute(ctx, arguments)
}
