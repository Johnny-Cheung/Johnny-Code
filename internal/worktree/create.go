
package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)



func WorktreesDir(repoRoot string) string {
	return filepath.Join(repoRoot, ".johnnycode", "worktrees")
}



func WorktreePathFor(repoRoot, slug string) string {
	return filepath.Join(WorktreesDir(repoRoot), FlattenSlug(slug))
}




type CreateResult struct {
	WorktreePath   string
	WorktreeBranch string
	HeadCommit     string
	BaseBranch     string 
	Existed        bool
}














func getOrCreateWorktree(ctx context.Context, repoRoot, slug string) (*CreateResult, error) {
	worktreePath := WorktreePathFor(repoRoot, slug)
	worktreeBranch := WorktreeBranchName(slug)

	
	existingHead, err := ReadWorktreeHeadSha(worktreePath)
	if err != nil {
		return nil, fmt.Errorf("read worktree HEAD: %w", err)
	}
	if existingHead != "" {
		return &CreateResult{
			WorktreePath:   worktreePath,
			WorktreeBranch: worktreeBranch,
			HeadCommit:     existingHead,
			Existed:        true,
		}, nil
	}

	if err := os.MkdirAll(WorktreesDir(repoRoot), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir worktrees dir: %w", err)
	}

	
	
	
	defaultBranch, err := GetDefaultBranch(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("get default branch: %w", err)
	}
	gitDir, err := ResolveGitDir(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve git dir: %w", err)
	}
	var baseBranch, baseSha string
	if gitDir != "" {
		baseSha, _ = ResolveRef(gitDir, "refs/remotes/origin/"+defaultBranch)
	}
	if baseSha != "" {
		baseBranch = "origin/" + defaultBranch
	} else {
		
		
		
		_, _, fetchCode := runGit(ctx, repoRoot, "fetch", "origin", defaultBranch)
		if fetchCode == 0 {
			baseBranch = "origin/" + defaultBranch
		} else {
			baseBranch = "HEAD"
		}
		
		
		stdout, _, shaCode := runGit(ctx, repoRoot, "rev-parse", baseBranch)
		if shaCode != 0 {
			return nil, fmt.Errorf(`failed to resolve base branch %q: git rev-parse failed`, baseBranch)
		}
		baseSha = trimNewline(stdout)
	}

	
	
	_, stderr, code := runGit(ctx, repoRoot, "worktree", "add", "-B", worktreeBranch, worktreePath, baseBranch)
	if code != 0 {
		return nil, fmt.Errorf("failed to create worktree: %s", stderr)
	}

	return &CreateResult{
		WorktreePath:   worktreePath,
		WorktreeBranch: worktreeBranch,
		HeadCommit:     baseSha,
		BaseBranch:     baseBranch,
		Existed:        false,
	}, nil
}



func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
