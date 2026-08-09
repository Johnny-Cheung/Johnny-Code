package skills

import (
	"fmt"
	"os"
	"path/filepath"
)

type Catalog struct {
	skills  map[string]*Skill
	sources map[string]string
	workDir string
}

func NewCatalog() *Catalog {
	return &Catalog{
		skills:  make(map[string]*Skill),
		sources: make(map[string]string),
	}
}

func (c *Catalog) Register(s *Skill, source string) {
	c.skills[s.Meta.Name] = s
	c.sources[s.Meta.Name] = source
}

func (c *Catalog) Get(name string) *Skill {
	return c.skills[name]
}

func (c *Catalog) GetFull(name string) (*Skill, error) {
	skill, ok := c.skills[name]
	if !ok {
		return nil, fmt.Errorf("unknown skill: %s", name)
	}
	if skill.SourceDir == "" {

		return skill, nil
	}
	if err := loadSkillBody(skill); err != nil {

		if skill.PromptBody == "" {
			return nil, err
		}
		return skill, err
	}
	return skill, nil
}

func (c *Catalog) List() []SkillMeta {
	result := make([]SkillMeta, 0, len(c.skills))
	for _, s := range c.skills {
		result = append(result, s.Meta)
	}
	return result
}

func (c *Catalog) Source(name string) string {
	return c.sources[name]
}

func (c *Catalog) Reload(workDir string) {
	fresh := LoadCatalog(workDir)
	c.skills = fresh.skills
	c.sources = fresh.sources
	c.workDir = fresh.workDir
}

func LoadCatalog(workDir string) *Catalog {
	c := NewCatalog()
	c.workDir = workDir

	for _, s := range LoadBuiltins() {
		c.Register(s, "builtin")
	}

	if home, err := os.UserHomeDir(); err == nil {
		loadTierInto(c, filepath.Join(home, ".johnnycode", "skills"), "user")
	}

	loadTierInto(c, filepath.Join(workDir, ".johnnycode", "skills"), "project")

	return c
}

func LoadFromDirectory(dir string) (*Catalog, error) {
	c := NewCatalog()
	loadTierEager(c, dir, dir)
	return c, nil
}

func LoadSkills(workDir string) *Catalog {
	c := NewCatalog()
	c.workDir = workDir
	if home, err := os.UserHomeDir(); err == nil {
		loadTierEager(c, filepath.Join(home, ".johnnycode", "skills"), "user")
	}
	loadTierEager(c, filepath.Join(workDir, ".johnnycode", "skills"), "project")
	return c
}

func loadTierInto(c *Catalog, dir, source string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skill, err := parseFrontmatterOnly(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		c.Register(skill, source)
	}
}

func loadTierEager(c *Catalog, dir, source string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		skill, err := parseFrontmatterOnly(path)
		if err != nil {
			continue
		}
		_ = loadSkillBody(skill)
		c.Register(skill, source)
	}
}
