package memory

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)


const MaxIncludeDepth = 5


type InstructionSource struct {
	Path    string
	Content string
}















func LoadInstructions(workDir string) string {
	sources := DiscoverInstructions(workDir)
	if len(sources) == 0 {
		return ""
	}
	var parts []string
	for _, s := range sources {
		label := s.Path
		if rel, err := filepath.Rel(workDir, s.Path); err == nil && !strings.HasPrefix(rel, "..") {
			label = rel
		}
		parts = append(parts, fmt.Sprintf("Contents of %s:\n\n%s", label, strings.TrimRight(s.Content, "\n")))
	}
	return strings.Join(parts, "\n\n---\n\n")
}



func DiscoverInstructions(workDir string) []InstructionSource {
	var sources []InstructionSource
	seen := map[string]bool{}

	if home, err := os.UserHomeDir(); err == nil {
		add(&sources, seen, filepath.Join(home, ".johnnycode", "JOHNNYCODE.md"))
		add(&sources, seen, filepath.Join(home, ".johnnycode", "AGENTS.md"))
	}
	for _, dir := range projectInstructionDirs(workDir) {
		add(&sources, seen, filepath.Join(dir, "JOHNNYCODE.md"))
		add(&sources, seen, filepath.Join(dir, "AGENTS.md"))
	}
	add(&sources, seen, filepath.Join(workDir, ".johnnycode", "INSTRUCTIONS.md"))
	add(&sources, seen, filepath.Join(workDir, "JOHNNYCODE.local.md"))
	return sources
}

func add(out *[]InstructionSource, seen map[string]bool, path string) {
	abs, err := filepath.Abs(path)
	if err != nil || seen[abs] {
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return
	}
	seen[abs] = true
	content := expandIncludes(string(data), filepath.Dir(abs), seen, 0)
	*out = append(*out, InstructionSource{Path: abs, Content: content})
}

func expandIncludes(content, baseDir string, seen map[string]bool, depth int) string {
	if depth > MaxIncludeDepth {
		return content
	}
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var out strings.Builder
	inCode := false
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCode = !inCode
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		}
		if !inCode {
			if path := parseInclude(trimmed); path != "" {
				resolved := resolveInclude(path, baseDir)
				if abs, err := filepath.Abs(resolved); err == nil && !seen[abs] {
					if data, err := os.ReadFile(abs); err == nil {
						seen[abs] = true
						out.WriteString(fmt.Sprintf("<!-- included from %s -->\n", path))
						out.WriteString(expandIncludes(string(data), filepath.Dir(abs), seen, depth+1))
						out.WriteByte('\n')
						continue
					}
				}
				
				
			}
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}




func parseInclude(trimmed string) string {
	if !strings.HasPrefix(trimmed, "@") || strings.HasPrefix(trimmed, "@@") {
		return ""
	}
	rest := strings.TrimPrefix(trimmed, "@")
	if rest == "" {
		return ""
	}
	if strings.ContainsAny(rest, " \t") {
		return ""
	}
	switch {
	case strings.HasPrefix(rest, "./"), strings.HasPrefix(rest, "../"),
		strings.HasPrefix(rest, "~/"), strings.HasPrefix(rest, "/"):
		return rest
	}
	return ""
}

func resolveInclude(p, baseDir string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
		return ""
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(baseDir, p)
}



func projectInstructionDirs(workDir string) []string {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return []string{workDir}
	}
	root := findGitRoot(abs)
	if root == "" {
		return []string{abs}
	}
	var dirs []string
	cur := abs
	for {
		dirs = append([]string{cur}, dirs...)
		if cur == root {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return dirs
}

func findGitRoot(start string) string {
	cur := start
	for {
		if info, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			_ = info
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
}
