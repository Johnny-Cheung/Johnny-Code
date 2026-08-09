package skills

import (
	"embed"
	"path"
)

//go:embed builtins/*
var builtinsFS embed.FS

func LoadBuiltins() []*Skill {
	var result []*Skill
	entries, err := builtinsFS.ReadDir("builtins")
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		mdPath := path.Join("builtins", name, "SKILL.md")
		data, err := builtinsFS.ReadFile(mdPath)
		if err != nil {
			continue
		}
		skill, err := loadSkillFromBytes(name, data)
		if err != nil {
			continue
		}
		skill.IsDirectory = builtinHasToolJSON(name)
		result = append(result, skill)
	}
	return result
}

func builtinHasToolJSON(skillName string) bool {
	_, err := builtinsFS.ReadFile(path.Join("builtins", skillName, "tool.json"))
	return err == nil
}

func BuiltinToolJSON(skillName string) []byte {
	data, err := builtinsFS.ReadFile(path.Join("builtins", skillName, "tool.json"))
	if err != nil {
		return nil
	}
	return data
}
