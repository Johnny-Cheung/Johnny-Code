
package worktree

import (
	"context"
	"os"
	"regexp"
	"strings"
	"time"
)



var ephemeralWorktreePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^agent-a[0-9a-f]{7}$`),
	regexp.MustCompile(`^wf_[0-9a-f]{8}-[0-9a-f]{3}-\d+$`),
	regexp.MustCompile(`^wf-\d+$`),
	regexp.MustCompile(`^bridge-[A-Za-z0-9_]+(-[A-Za-z0-9_]+)*$`),
	regexp.MustCompile(`^job-[a-zA-Z0-9._-]{1,55}-[0-9a-f]{8}$`),
}

func isEphemeralSlug(slug string) bool {
	for _, p := range ephemeralWorktreePatterns {
		if p.MatchString(slug) {
			return true
		}
	}
	return false
}






func CleanupStaleAgentWorktrees(ctx context.Context, cutoffDate time.Time) int {
	cwd, _ := os.Getwd()
	gitRoot := FindCanonicalGitRoot(cwd)
	if gitRoot == "" {
		return 0
	}

	dir := WorktreesDir(gitRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}

	currentPath := ""
	if s := GetCurrentWorktreeSession(); s != nil {
		currentPath = s.WorktreePath
	}

	removed := 0
	for _, entry := range entries {
		slug := entry.Name()

		
		if !isEphemeralSlug(slug) {
			continue
		}

		worktreePath := WorktreePathFor(gitRoot, slug)
		if currentPath == worktreePath {
			continue
		}

		
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoffDate) {
			continue
		}

		
		
		statusOut, _, statusCode := runGit(ctx, worktreePath,
			"--no-optional-locks", "status", "--porcelain", "-uno")
		if statusCode != 0 || strings.TrimSpace(statusOut) != "" {
			continue
		}

		unpushedOut, _, unpushedCode := runGit(ctx, worktreePath,
			"rev-list", "--max-count=1", "HEAD", "--not", "--remotes")
		if unpushedCode != 0 || strings.TrimSpace(unpushedOut) != "" {
			continue
		}

		if RemoveAgentWorktree(ctx, worktreePath, WorktreeBranchName(slug), gitRoot) {
			removed++
		}
	}

	if removed > 0 {
		runGit(ctx, gitRoot, "worktree", "prune")
	}
	return removed
}



func StartCleanupLoop(ctx context.Context) {
	interval := GetStaleCleanupInterval()
	if interval <= 0 {
		return
	}
	cutoffHours := GetStaleCutoffHours()

	go func() {
		ticker := time.NewTicker(time.Duration(interval) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cutoff := time.Now().Add(-time.Duration(cutoffHours) * time.Hour)
				CleanupStaleAgentWorktrees(ctx, cutoff)
			}
		}
	}()
}

