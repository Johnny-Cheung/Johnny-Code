package memory

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeRemote(t *testing.T) {
	cases := map[string]string{
		"git@github.com:Johnny-Cheung/Johnny-Code.git":       "github.com/Johnny-Cheung/Johnny-Code",
		"https://github.com/Johnny-Cheung/Johnny-Code.git":   "github.com/Johnny-Cheung/Johnny-Code",
		"ssh://git@github.com/Johnny-Cheung/Johnny-Code.git": "github.com/Johnny-Cheung/Johnny-Code",
		"https://user:token@github.com/owner/repo.git/":      "github.com/owner/repo",
		"git@gitlab.example.com:group/sub/repo.git":          "gitlab.example.com/group/sub/repo",
		"https://github.com/owner/repo":                      "github.com/owner/repo",
	}
	for input, want := range cases {
		if got := normalizeRemote(input); got != want {
			t.Fatalf("normalizeRemote(%q)=%q，期望 %q", input, got, want)
		}
	}
}

func TestSlugify(t *testing.T) {
	if got := slugify("Johnny Code!"); got != "johnny-code" {
		t.Fatalf("slugify=%q", got)
	}
	if got := slugify("中文项目"); got != "project" {
		t.Fatalf("非 ASCII 应回退：%q", got)
	}
	if got := slugify(strings.Repeat("a", 40)); len(got) > 32 {
		t.Fatalf("slug 过长：%q", got)
	}
}

func TestDiscoverProjectStable(t *testing.T) {
	dir := t.TempDir()
	first := DiscoverProject(dir)
	second := DiscoverProject(dir)
	if first.ID == "" || first.ID != second.ID {
		t.Fatalf("project-id 不稳定：%q %q", first.ID, second.ID)
	}
	if !strings.HasPrefix(first.ID, slugify(filepath.Base(dir))) && !strings.HasPrefix(first.ID, "project-") {
		t.Fatalf("project-id 前缀异常：%q", first.ID)
	}
}

func TestDiscoverProjectUsesRemoteAcrossSubdirs(t *testing.T) {
	root := t.TempDir()
	if err := exec.Command("git", "init", root).Run(); err != nil {
		t.Skipf("git 不可用：%v", err)
	}
	if err := exec.Command("git", "-C", root, "remote", "add", "origin", "git@github.com:owner/repo.git").Run(); err != nil {
		t.Skipf("git remote 不可用：%v", err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	rootInfo := DiscoverProject(root)
	subInfo := DiscoverProject(sub)
	if rootInfo.ID != subInfo.ID {
		t.Fatalf("同一 remote 的子目录应共享 project-id：%q != %q", rootInfo.ID, subInfo.ID)
	}
	if rootInfo.Remote != "github.com/owner/repo" {
		t.Fatalf("remote 归一化错误：%q", rootInfo.Remote)
	}
}

func TestRecordProject(t *testing.T) {
	home := t.TempDir()
	info := DiscoverProject(t.TempDir())
	if err := RecordProject(home, info); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".johnny", "projects", "index.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var index projectIndex
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	entry, ok := index.Projects[info.ID]
	if !ok || len(entry.Roots) == 0 || entry.Roots[0] != info.Root {
		t.Fatalf("项目索引错误：%+v", index)
	}
}
