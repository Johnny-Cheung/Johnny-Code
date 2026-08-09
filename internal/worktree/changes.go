
package worktree

import (
	"context"
	"strconv"
	"strings"
)


type ChangeSummary struct {
	ChangedFiles int
	Commits      int
}



func HasWorktreeChanges(ctx context.Context, worktreePath, headCommit string) bool {
	stdout, _, code := runGit(ctx, worktreePath, "status", "--porcelain")
	if code != 0 {
		return true 
	}
	if strings.TrimSpace(stdout) != "" {
		return true
	}

	stdout, _, code = runGit(ctx, worktreePath, "rev-list", "--count", headCommit+"..HEAD")
	if code != 0 {
		return true 
	}
	n, err := strconv.Atoi(strings.TrimSpace(stdout))
	if err != nil {
		return true 
	}
	return n > 0
}




func CountWorktreeChanges(ctx context.Context, worktreePath, originalHeadCommit string) *ChangeSummary {
	stdout, _, code := runGit(ctx, worktreePath, "status", "--porcelain")
	if code != 0 {
		return nil 
	}
	changedFiles := 0
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) != "" {
			changedFiles++
		}
	}

	if originalHeadCommit == "" {
		
		return nil
	}

	stdout, _, code = runGit(ctx, worktreePath, "rev-list", "--count", originalHeadCommit+"..HEAD")
	if code != 0 {
		return nil 
	}
	commits, err := strconv.Atoi(strings.TrimSpace(stdout))
	if err != nil {
		return nil
	}

	return &ChangeSummary{
		ChangedFiles: changedFiles,
		Commits:      commits,
	}
}

