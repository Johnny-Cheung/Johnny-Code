package commands

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)



type CommandMeta struct {
	Description  string   `yaml:"description"`
	ArgumentHint string   `yaml:"argument-hint"`
	Aliases      []string `yaml:"aliases"`
}









func LoadDir(dir string) []*Command {
	if dir == "" {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil
	}

	var cmds []*Command
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}
		cmd := parseCommandFile(dir, path)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		return nil
	})
	return cmds
}





func LoadUserCommands(workDir string) []*Command {
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".johnnycode", "commands"))
	}
	dirs = append(dirs,
		filepath.Join(workDir, ".johnnycode", "commands"),
	)

	merged := map[string]*Command{}
	var order []string
	for _, d := range dirs {
		for _, cmd := range LoadDir(d) {
			if _, seen := merged[cmd.Name]; !seen {
				order = append(order, cmd.Name)
			}
			merged[cmd.Name] = cmd
		}
	}

	out := make([]*Command, 0, len(order))
	for _, name := range order {
		out = append(out, merged[name])
	}
	return out
}




func parseCommandFile(baseDir, path string) *Command {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	rel, err := filepath.Rel(baseDir, path)
	if err != nil {
		return nil
	}
	rel = strings.TrimSuffix(rel, ".md")
	parts := strings.Split(rel, string(filepath.Separator))
	for i, p := range parts {
		parts[i] = strings.ToLower(strings.ReplaceAll(p, " ", "-"))
	}
	name := strings.Join(parts, ":")
	if name == "" {
		return nil
	}

	meta, body := splitFrontmatter(string(data))
	body = strings.TrimSpace(body)
	if meta.Description == "" {
		meta.Description = firstNonHeaderLine(body)
	}

	return &Command{
		Name:        name,
		Description: meta.Description,
		Aliases:     meta.Aliases,
		Type:        TypePrompt,
		ArgPrompt:   meta.ArgumentHint,
		Handler:     promptHandler(body),
	}
}




func splitFrontmatter(content string) (CommandMeta, string) {
	var meta CommandMeta
	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return meta, content
	}
	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return meta, content
	}
	if err := yaml.Unmarshal([]byte(parts[1]), &meta); err != nil {
		return CommandMeta{}, content
	}
	return meta, parts[2]
}



func firstNonHeaderLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return ""
}



func promptHandler(body string) Handler {
	return func(ctx *Context) string {
		if strings.Contains(body, "$ARGUMENTS") {
			return strings.ReplaceAll(body, "$ARGUMENTS", ctx.Args)
		}
		if strings.TrimSpace(ctx.Args) == "" {
			return body
		}
		return body + "\n\n## User Request\n\n" + ctx.Args
	}
}
