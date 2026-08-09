
package worktree

import (
	"fmt"
	"regexp"
	"strings"
)


const MaxWorktreeSlugLength = 64


var validWorktreeSlugSegment = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)












func ValidateWorktreeSlug(slug string) error {
	if len(slug) > MaxWorktreeSlugLength {
		return fmt.Errorf(
			"Invalid worktree name: must be %d characters or fewer (got %d)",
			MaxWorktreeSlugLength, len(slug),
		)
	}
	
	
	
	for _, segment := range strings.Split(slug, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf(
				`Invalid worktree name %q: must not contain "." or ".." path segments`,
				slug,
			)
		}
		if !validWorktreeSlugSegment.MatchString(segment) {
			return fmt.Errorf(
				`Invalid worktree name %q: each "/"-separated segment must be non-empty and contain only letters, digits, dots, underscores, and dashes`,
				slug,
			)
		}
	}
	return nil
}












func FlattenSlug(slug string) string {
	return strings.ReplaceAll(slug, "/", "+")
}



func WorktreeBranchName(slug string) string {
	return "worktree-" + FlattenSlug(slug)
}

