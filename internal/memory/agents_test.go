package memory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAgentsLoadsUserThenProject(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	writeFile(t, filepath.Join(home, ".johnny", AgentsFileName), "# 用户规则\n\n- 回答使用中文。")
	writeFile(t, filepath.Join(work, AgentsFileName), "# 项目规则\n\n- 测试用 go test ./...")

	text, warning := (&Agents{WorkDir: work, HomeDir: home}).Load()
	if warning != "" {
		t.Fatalf("不应有警告：%s", warning)
	}
	userIndex := strings.Index(text, "## 用户规则")
	projectIndex := strings.Index(text, "## 项目规则")
	if userIndex < 0 || projectIndex < 0 || userIndex > projectIndex {
		t.Fatalf("规则顺序错误：\n%s", text)
	}
	for _, want := range []string{"回答使用中文", "测试用 go test", filepath.Join(home, ".johnny", AgentsFileName), filepath.Join(work, AgentsFileName)} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺少 %q：\n%s", want, text)
		}
	}
}

func TestAgentsMissingFilesReturnsEmpty(t *testing.T) {
	text, warning := (&Agents{WorkDir: t.TempDir(), HomeDir: t.TempDir()}).Load()
	if text != "" || warning != "" {
		t.Fatalf("空目录应返回空：text=%q warning=%q", text, warning)
	}
}

func TestAgentsTruncatesByLines(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	var user strings.Builder
	for i := 0; i < MaxUserAgentsLines+20; i++ {
		user.WriteString("user rule\n")
	}
	var project strings.Builder
	for i := 0; i < MaxProjectAgentsLines+20; i++ {
		project.WriteString("project rule\n")
	}
	writeFile(t, filepath.Join(home, ".johnny", AgentsFileName), user.String())
	writeFile(t, filepath.Join(work, AgentsFileName), project.String())

	text, warning := (&Agents{WorkDir: work, HomeDir: home}).Load()
	if !strings.Contains(warning, "用户级 AGENTS.md 已截断到") || !strings.Contains(warning, "项目级 AGENTS.md 已截断到") {
		t.Fatalf("警告中未看到截断：%s", warning)
	}
	if !strings.Contains(text, "[AGENTS 已截断") {
		t.Fatalf("缺少截断标记：\n%s", text)
	}
}

func TestLimitAgentsByTokens(t *testing.T) {
	text, truncated := limitAgents(strings.Repeat("中", 2000), 100, 100)
	if !truncated || !strings.Contains(text, "已截断") {
		t.Fatalf("应按 token 截断：%q", text)
	}
	if contextview.EstimateText(text) > 200 {
		t.Fatalf("截断后仍过大：%d", contextview.EstimateText(text))
	}
}

func TestProjectRootFallsBackToWorkDir(t *testing.T) {
	dir := t.TempDir()
	got := ProjectRoot(dir)
	want, _ := filepath.EvalSymlinks(dir)
	got, _ = filepath.EvalSymlinks(got)
	if got != want {
		t.Fatalf("非 git 目录应回退到工作目录：%q != %q", got, want)
	}
}

func TestProjectRootUsesGitRoot(t *testing.T) {
	root := t.TempDir()
	if err := exec.Command("git", "init", root).Run(); err != nil {
		t.Skipf("git 不可用：%v", err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got := ProjectRoot(sub)
	gotResolved, _ := filepath.EvalSymlinks(got)
	wantResolved, _ := filepath.EvalSymlinks(root)
	if gotResolved != wantResolved {
		t.Fatalf("git 子目录应返回根目录：%q != %q", gotResolved, wantResolved)
	}
}
