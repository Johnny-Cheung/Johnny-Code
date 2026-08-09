package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)






type Manager struct {
	projectRoot string
	userMemDir  string 
	memDir      string 
}




func NewManager(projectRoot string) *Manager {
	abs, err := filepath.Abs(projectRoot)
	if err != nil {
		abs = projectRoot
	}
	return &Manager{
		projectRoot: abs,
		userMemDir:  GetUserAutoMemPath(),
		memDir:      GetAutoMemPath(abs),
	}
}



func (m *Manager) Dir() string {
	return m.memDir
}


func (m *Manager) UserDir() string {
	return m.userMemDir
}


func (m *Manager) EntrypointPath() string {
	return filepath.Join(m.memDir, AutoMemEntrypointName)
}


func (m *Manager) UserEntrypointPath() string {
	if m.userMemDir == "" {
		return ""
	}
	return filepath.Join(m.userMemDir, AutoMemEntrypointName)
}




func (m *Manager) BuildSystemReminder() string {
	if m.memDir == "" && m.userMemDir == "" {
		return ""
	}
	if m.userMemDir != "" {
		_ = EnsureMemoryDirExists(m.userMemDir)
	}
	if m.memDir != "" {
		_ = EnsureMemoryDirExists(m.memDir)
	}
	return BuildMemoryPrompt(autoMemDisplayName, m.userMemDir, m.memDir)
}


type MemoryFile struct {
	Path        string
	Name        string
	Description string
	Type        MemoryType
}




func (m *Manager) GetMemories() []string {
	files := m.LoadAll()
	out := make([]string, 0, len(files))
	for _, f := range files {
		typeTag := string(f.Type)
		if typeTag == "" {
			typeTag = "?"
		}
		desc := f.Description
		if desc == "" {
			desc = filepath.Base(f.Path)
		}
		out = append(out, fmt.Sprintf("[%s] %s — %s", typeTag, f.Name, desc))
	}
	return out
}




func (m *Manager) LoadAll() []MemoryFile {
	out := loadDir(m.userMemDir)
	out = append(out, loadDir(m.memDir)...)
	return out
}

func loadDir(dir string) []MemoryFile {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	var out []MemoryFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == AutoMemEntrypointName || !strings.HasSuffix(name, ".md") {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		mf := parseFrontmatter(string(data))
		mf.Path = path
		if mf.Name == "" {
			mf.Name = strings.TrimSuffix(name, ".md")
		}
		out = append(out, mf)
	}
	return out
}



func (m *Manager) Clear() {
	clearDir(m.userMemDir)
	clearDir(m.memDir)
}

func clearDir(dir string) {
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}

var frontmatterRe = regexp.MustCompile(`(?s)\A---\s*\n(.*?)\n---\s*\n`)





func parseFrontmatter(content string) MemoryFile {
	var mf MemoryFile
	m := frontmatterRe.FindStringSubmatch(content)
	if m == nil {
		return mf
	}
	for _, line := range strings.Split(m[1], "\n") {
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])
		val = strings.Trim(val, `"'`)
		switch key {
		case "name":
			mf.Name = val
		case "description":
			mf.Description = val
		case "type":
			if t, ok := ParseMemoryType(val); ok {
				mf.Type = t
			}
		}
	}
	return mf
}
