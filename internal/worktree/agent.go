
package worktree

import (
	"context"
	"os"
	"time"
)


type AgentWorktreeResult struct {
	WorktreePath   string
	WorktreeBranch string
	HeadCommit     string
	GitRoot        string
}




func CreateAgentWorktree(ctx context.Context, slug string) (*AgentWorktreeResult, error) {
	if err := ValidateWorktreeSlug(slug); err != nil {
		return nil, err
	}

	cwd, _ := os.Getwd()
	gitRoot := FindCanonicalGitRoot(cwd)
	if gitRoot == "" {
		return nil, &worktreeError{msg: "cannot create agent worktree: not in a git repository"}
	}

	result, err := getOrCreateWorktree(ctx, gitRoot, slug)
	if err != nil {
		return nil, err
	}

	if !result.Existed {
		performPostCreationSetup(ctx, gitRoot, result.WorktreePath)
	} else {
		
		now := time.Now()
		_ = os.Chtimes(result.WorktreePath, now, now)
	}

	return &AgentWorktreeResult{
		WorktreePath:   result.WorktreePath,
		WorktreeBranch: result.WorktreeBranch,
		HeadCommit:     result.HeadCommit,
		GitRoot:        gitRoot,
	}, nil
}


func RemoveAgentWorktree(ctx context.Context, worktreePath, worktreeBranch, gitRoot string) bool {
	if gitRoot == "" {
		return false
	}

	_, _, code := runGit(ctx, gitRoot, "worktree", "remove", "--force", worktreePath)
	if code != 0 {
		return false
	}

	if worktreeBranch != "" {
		
		time.Sleep(100 * time.Millisecond)
		runGit(ctx, gitRoot, "branch", "-D", worktreeBranch)
	}
	return true
}

