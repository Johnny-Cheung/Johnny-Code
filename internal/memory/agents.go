// Package memory loads and persists Johnny-Code long-term rules and memories.
package memory

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
)

const (
	// AgentsFileName 是用户级和项目级规则文件统一使用的文件名。
	AgentsFileName = "AGENTS.md"

	MaxUserAgentsLines     = 100
	MaxUserAgentsTokens    = 1000
	MaxProjectAgentsLines  = 200
	MaxProjectAgentsTokens = 2000

	agentsReadLimit  = 1 << 20
	gitLookupTimeout = 2 * time.Second
)

// Agents 加载用户级和项目级 AGENTS.md。WorkDir 为空时使用当前目录。
// HomeDir 主要用于测试；为空时使用 os.UserHomeDir。
type Agents struct {
	WorkDir string
	HomeDir string
}

func NewAgents(workDir string) *Agents {
	return &Agents{WorkDir: workDir}
}

// Load 返回可直接拼接进系统提示的规则区块，以及不阻断任务的警告。
func (a *Agents) Load() (string, string) {
	if a == nil {
		return "", ""
	}
	workDir := strings.TrimSpace(a.WorkDir)
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	if workDir == "" {
		return "", "无法确定工作目录，未加载 AGENTS.md。"
	}
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		absWork = workDir
	}
	projectRoot := ProjectRoot(absWork)

	home := strings.TrimSpace(a.HomeDir)
	if home == "" {
		home, err = os.UserHomeDir()
	}
	var sections []string
	var warnings []string
	if err != nil || strings.TrimSpace(home) == "" {
		warnings = append(warnings, "无法确定用户目录，跳过用户级 AGENTS.md。")
	} else if text, exists, warning := readAgentsFile(filepath.Join(home, ".johnny", AgentsFileName)); warning != "" {
		warnings = append(warnings, "用户级 AGENTS.md："+warning)
	} else if exists {
		limited, truncated := limitAgents(text, MaxUserAgentsLines, MaxUserAgentsTokens)
		if truncated {
			warnings = append(warnings, fmt.Sprintf("用户级 AGENTS.md 已截断到 %d 行或 %d token。", MaxUserAgentsLines, MaxUserAgentsTokens))
		}
		sections = append(sections, agentsSection("用户规则", filepath.Join(home, ".johnny", AgentsFileName), limited))
	}

	if text, exists, warning := readAgentsFile(filepath.Join(projectRoot, AgentsFileName)); warning != "" {
		warnings = append(warnings, "项目级 AGENTS.md："+warning)
	} else if exists {
		limited, truncated := limitAgents(text, MaxProjectAgentsLines, MaxProjectAgentsTokens)
		if truncated {
			warnings = append(warnings, fmt.Sprintf("项目级 AGENTS.md 已截断到 %d 行或 %d token。", MaxProjectAgentsLines, MaxProjectAgentsTokens))
		}
		sections = append(sections, agentsSection("项目规则", filepath.Join(projectRoot, AgentsFileName), limited))
	}

	return strings.Join(sections, "\n\n"), strings.Join(warnings, "；")
}

// ProjectRoot 返回 git 根目录；不在 git 仓库内时返回 workDir。
func ProjectRoot(workDir string) string {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return workDir
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitLookupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", workDir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err == nil {
		if root := strings.TrimSpace(string(out)); root != "" {
			return root
		}
	}
	return workDir
}

func agentsSection(title, path, text string) string {
	return fmt.Sprintf("## %s\n来源：%s\n\n%s", title, path, text)
}

// readAgentsFile 返回文件正文（已规范化换行和 UTF-8）、是否存在以及警告。
func readAgentsFile(path string) (string, bool, string) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, ""
		}
		return "", false, err.Error()
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", false, err.Error()
	}
	if info.IsDir() {
		return "", false, "路径是目录，不是文件"
	}
	var warning string
	limit := int64(agentsReadLimit)
	if info.Size() > agentsReadLimit {
		warning = fmt.Sprintf("文件超过 %d 字节，已只读取开头。", agentsReadLimit)
	} else {
		limit = info.Size()
	}
	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return "", false, err.Error()
	}
	text := strings.ToValidUTF8(string(data), "�")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return "", false, warning
	}
	return text, true, warning
}

// limitAgents 在行和保守 token 预算内截断文本，避免 AGENTS 长时间占用前缀。
func limitAgents(text string, maxLines, maxTokens int) (string, bool) {
	lines := strings.Split(text, "\n")
	var builder strings.Builder
	truncated := false
	for index, line := range lines {
		if index >= maxLines {
			truncated = true
			break
		}
		next := line + "\n"
		if contextview.EstimateText(builder.String()+next) > maxTokens {
			truncated = true
			break
		}
		builder.WriteString(next)
	}
	out := strings.TrimRight(builder.String(), "\n")
	if truncated {
		out += fmt.Sprintf("\n\n[AGENTS 已截断：超过 %d 行或 %d token 上限，请精简文件。]", maxLines, maxTokens)
	}
	return out, truncated
}
