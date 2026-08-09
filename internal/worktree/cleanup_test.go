
package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestIsEphemeralSlug(t *testing.T) {
	tests := []struct {
		slug     string
		expected bool
	}{
		{"agent-a1234567", true},
		{"agent-aabcdef0", true},
		{"wf_12345678-abc-1", true},
		{"wf_12345678-abc-42", true},
		{"wf-1", true},
		{"wf-99", true},
		{"bridge-abc", true},
		{"bridge-abc_def-ghi", true},
		{"job-mytemplate-12345678", true},
		
		{"my-feature", false},
		{"agent-too-long", false},
		{"agent-a123", false},     
		{"agent-aGGGGGGG", false}, 
		{"wf_short", false},
	}
	for _, tt := range tests {
		got := isEphemeralSlug(tt.slug)
		if got != tt.expected {
			t.Errorf("isEphemeralSlug(%q) = %v, want %v", tt.slug, got, tt.expected)
		}
	}
}

func TestCleanupStaleAgentWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	repo := t.TempDir()
	initTestRepo(t, repo)

	
	
	bare := t.TempDir()
	exec.Command("git", "init", "--bare", bare).Run()
	exec.Command("git", "-C", repo, "remote", "add", "origin", bare).Run()
	exec.Command("git", "-C", repo, "push", "origin", "master").Run()

	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)
	os.Chdir(repo)

	
	result, err := CreateAgentWorktree(context.Background(), "agent-aaaaaaaa")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	
	past := time.Now().Add(-25 * time.Hour)
	os.Chtimes(result.WorktreePath, past, past)

	
	cutoff := time.Now().Add(-24 * time.Hour)
	removed := CleanupStaleAgentWorktrees(context.Background(), cutoff)
	if removed != 1 {
		t.Fatalf("expected 1 removed, got %d", removed)
	}

	
	if _, err := os.Stat(result.WorktreePath); !os.IsNotExist(err) {
		t.Fatal("stale worktree should be removed")
	}
}

func TestCleanupStaleAgentWorktrees_SkipsUserNamed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	repo := t.TempDir()
	initTestRepo(t, repo)

	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)
	os.Chdir(repo)

	
	_, err := getOrCreateWorktree(context.Background(), repo, "my-feature")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	wtPath := WorktreePathFor(repo, "my-feature")

	
	past := time.Now().Add(-48 * time.Hour)
	os.Chtimes(wtPath, past, past)

	
	cutoff := time.Now().Add(-24 * time.Hour)
	removed := CleanupStaleAgentWorktrees(context.Background(), cutoff)
	if removed != 0 {
		t.Fatal("user-named worktree should not be cleaned up")
	}

	if _, err := os.Stat(wtPath); err != nil {
		t.Fatal("user-named worktree should still exist")
	}
}

func TestCleanupStaleAgentWorktrees_SkipsDirtyWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	repo := t.TempDir()
	initTestRepo(t, repo)

	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)
	os.Chdir(repo)

	result, err := CreateAgentWorktree(context.Background(), "agent-abbbbbbb")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	
	os.WriteFile(filepath.Join(result.WorktreePath, "dirty.txt"), []byte("dirty"), 0o644)
	exec.Command("git", "-C", result.WorktreePath, "add", ".").Run()

	
	past := time.Now().Add(-48 * time.Hour)
	os.Chtimes(result.WorktreePath, past, past)

	
	cutoff := time.Now().Add(-24 * time.Hour)
	removed := CleanupStaleAgentWorktrees(context.Background(), cutoff)
	if removed != 0 {
		t.Fatal("dirty worktree should not be cleaned up")
	}
}
