
package skills

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"johnnycode/internal/tools"
)



type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}




type builtinToolFactory func(schema ToolSchema) tools.Tool







var builtinToolFactories = map[string]builtinToolFactory{}



func parseToolJSON(skill *Skill) ([]ToolSchema, error) {
	var data []byte
	var err error

	if skill.SourceDir != "" {
		path := filepath.Join(skill.SourceDir, "tool.json")
		data, err = os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("read tool.json: %w", err)
		}
	}
	if len(data) == 0 {
		data = BuiltinToolJSON(skill.Meta.Name)
	}
	if len(data) == 0 {
		return nil, nil
	}

	var schemas []ToolSchema
	if err := json.Unmarshal(data, &schemas); err != nil {
		return nil, fmt.Errorf("parse tool.json: %w", err)
	}
	return schemas, nil
}








func RegisterDirectoryTools(skill *Skill, registry *tools.Registry) (int, error) {
	schemas, err := parseToolJSON(skill)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, schema := range schemas {
		if registry.Get(schema.Name) != nil {
			continue
		}
		factory, ok := builtinToolFactories[schema.Name]
		if !ok {
			log.Printf("skills: tool.json declares %q but no compiled-in implementation; skipping", schema.Name)
			continue
		}
		registry.Register(factory(schema))
		count++
	}
	return count, nil
}

