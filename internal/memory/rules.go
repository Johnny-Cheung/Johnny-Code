package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
)

const (
	agentsAutoStart = "<!-- johnny:auto:start -->"
	agentsAutoEnd   = "<!-- johnny:auto:end -->"
	maxRuleTokens   = 200
	maxBackups      = 10
)

// Rules 维护用户级 AGENTS.md 的自动规则区。
type Rules struct {
	HomeDir  string
	Observer func(WriteEvent)
}

func NewRules(home string) *Rules {
	return &Rules{HomeDir: home}
}

func (r *Rules) Path() string {
	if r == nil {
		return ""
	}
	return filepath.Join(r.HomeDir, ".johnny", AgentsFileName)
}

func (r *Rules) BackupsDir() string {
	if r == nil {
		return ""
	}
	return filepath.Join(r.HomeDir, ".johnny", "backups", "agents")
}

// Add 在自动规则区追加一条规则。已存在的 ID 不会被覆盖。
func (r *Rules) Add(ctx context.Context, name, text string) (string, error) {
	return r.AddWithSource(ctx, name, text, "")
}

// AddWithSource 与 Add 相同，并记录写入来源。
func (r *Rules) AddWithSource(ctx context.Context, name, text, source string) (string, error) {
	if r == nil || strings.TrimSpace(r.HomeDir) == "" {
		return "", errors.New("用户目录不能为空")
	}
	name = strings.TrimSpace(name)
	text = OneLine(text)
	if !itemNamePattern.MatchString(name) {
		return "", fmt.Errorf("规则名称无效：%q", name)
	}
	if text == "" {
		return "", errors.New("规则内容不能为空")
	}
	if contextview.EstimateText(text) > maxRuleTokens {
		return "", fmt.Errorf("规则内容超过 %d token 上限", maxRuleTokens)
	}
	if containsSecret(text) {
		return "", errSecret
	}
	path := r.Path()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	err := withFileLock(ctx, dir, ".agents.lock", func() error {
		existing, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		content := string(existing)
		if len(existing) > 0 {
			if err := r.backup(content, name); err != nil {
				return err
			}
		}
		updated, err := addAutoRule(content, name, text)
		if err != nil {
			return err
		}
		return writeFileAtomic(path, []byte(updated), 0o600)
	})
	if err != nil {
		return "", err
	}
	if r.Observer != nil {
		r.Observer(WriteEvent{Scope: "user_agents", Action: "rule_added", Name: name, Path: path, Source: source})
	}
	return path, nil
}

func addAutoRule(content, name, text string) (string, error) {
	if strings.TrimSpace(content) == "" {
		content = defaultAgentsContent()
	}
	start := strings.Index(content, agentsAutoStart)
	if start < 0 {
		content = strings.TrimRight(content, "\n") + "\n\n## 自动规则\n\n" + agentsAutoStart + "\n" + agentsAutoEnd + "\n"
		start = strings.Index(content, agentsAutoStart)
	}
	endRelative := strings.Index(content[start:], agentsAutoEnd)
	if endRelative < 0 {
		return "", errors.New("用户 AGENTS.md 的自动规则区缺少结束标记")
	}
	end := start + endRelative
	section := content[start:end]
	if strings.Contains(section, "`"+name+"`") {
		return "", fmt.Errorf("用户规则已存在：%s", name)
	}
	line := fmt.Sprintf("- `%s` [%s] %s\n", name, time.Now().Format("2006-01-02"), text)
	updated := content[:end] + line + content[end:]
	return updated, nil
}

func defaultAgentsContent() string {
	return "# 用户规则\n\n## 手动规则\n\n## 自动规则\n\n" + agentsAutoStart + "\n" + agentsAutoEnd + "\n"
}

func (r *Rules) backup(content, name string) error {
	dir := r.BackupsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	timestamp := time.Now().Format("20060102T150405.000000000")
	path := filepath.Join(dir, timestamp+"-add-"+name+".md")
	if err := writeFileAtomic(path, []byte(content), 0o600); err != nil {
		return err
	}
	return pruneBackups(dir)
}

func pruneBackups(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		files = append(files, entry.Name())
	}
	sort.Strings(files)
	for len(files) > maxBackups {
		if err := os.Remove(filepath.Join(dir, files[0])); err != nil {
			return err
		}
		files = files[1:]
	}
	return nil
}
