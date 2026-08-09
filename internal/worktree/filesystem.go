









package worktree

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)




var safeRefName = regexp.MustCompile(`^[a-zA-Z0-9/._+@-]+$`)



func IsSafeRefName(name string) bool {
	if name == "" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") {
		return false
	}
	if strings.Contains(name, "..") {
		return false
	}
	
	for _, seg := range strings.Split(name, "/") {
		if seg == "." || seg == "" {
			return false
		}
	}
	return safeRefName.MatchString(name)
}

var sha1Pattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)



func IsValidGitSha(s string) bool {
	return sha1Pattern.MatchString(s) || sha256Pattern.MatchString(s)
}







func ResolveGitDir(root string) (string, error) {
	gitPath := filepath.Join(root, ".git")
	st, err := os.Stat(gitPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	if !st.IsDir() {
		
		
		raw, err := os.ReadFile(gitPath)
		if err != nil {
			return "", err
		}
		content := strings.TrimSpace(string(raw))
		if !strings.HasPrefix(content, "gitdir:") {
			return "", nil
		}
		rel := strings.TrimSpace(strings.TrimPrefix(content, "gitdir:"))
		
		if filepath.IsAbs(rel) {
			return rel, nil
		}
		return filepath.Clean(filepath.Join(root, rel)), nil
	}
	return gitPath, nil
}




func GetCommonDir(gitDir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	content := strings.TrimSpace(string(raw))
	if filepath.IsAbs(content) {
		return content, nil
	}
	return filepath.Clean(filepath.Join(gitDir, content)), nil
}


type gitHead struct {
	
	branch string
	
	sha string
}









func readGitHead(gitDir string) (*gitHead, error) {
	raw, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	content := strings.TrimSpace(string(raw))
	if strings.HasPrefix(content, "ref:") {
		ref := strings.TrimSpace(strings.TrimPrefix(content, "ref:"))
		if strings.HasPrefix(ref, "refs/heads/") {
			name := strings.TrimPrefix(ref, "refs/heads/")
			if !IsSafeRefName(name) {
				return nil, nil
			}
			return &gitHead{branch: name}, nil
		}
		
		if !IsSafeRefName(ref) {
			return nil, nil
		}
		sha, err := ResolveRef(gitDir, ref)
		if err != nil {
			return nil, err
		}
		return &gitHead{sha: sha}, nil
	}
	
	
	if !IsValidGitSha(content) {
		return nil, nil
	}
	return &gitHead{sha: content}, nil
}






func ResolveRef(gitDir, ref string) (string, error) {
	sha, err := resolveRefInDir(gitDir, ref)
	if err != nil {
		return "", err
	}
	if sha != "" {
		return sha, nil
	}
	commonDir, err := GetCommonDir(gitDir)
	if err != nil {
		return "", err
	}
	if commonDir != "" && commonDir != gitDir {
		return resolveRefInDir(commonDir, ref)
	}
	return "", nil
}


func resolveRefInDir(dir, ref string) (string, error) {
	
	raw, err := os.ReadFile(filepath.Join(dir, ref))
	if err == nil {
		content := strings.TrimSpace(string(raw))
		if strings.HasPrefix(content, "ref:") {
			target := strings.TrimSpace(strings.TrimPrefix(content, "ref:"))
			if !IsSafeRefName(target) {
				return "", nil
			}
			
			
			return ResolveRef(dir, target)
		}
		if !IsValidGitSha(content) {
			return "", nil
		}
		return content, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}

	
	packed, err := os.ReadFile(filepath.Join(dir, "packed-refs"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	for _, line := range strings.Split(string(packed), "\n") {
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		spaceIdx := strings.IndexByte(line, ' ')
		if spaceIdx == -1 {
			continue
		}
		if line[spaceIdx+1:] == ref {
			sha := line[:spaceIdx]
			if !IsValidGitSha(sha) {
				return "", nil
			}
			return sha, nil
		}
	}
	return "", nil
}




func ReadRawSymref(gitDir, refPath, branchPrefix string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(gitDir, refPath))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	content := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(content, "ref:") {
		return "", nil
	}
	target := strings.TrimSpace(strings.TrimPrefix(content, "ref:"))
	if !strings.HasPrefix(target, branchPrefix) {
		return "", nil
	}
	name := strings.TrimPrefix(target, branchPrefix)
	if !IsSafeRefName(name) {
		return "", nil
	}
	return name, nil
}






func GetDefaultBranch(repoRoot string) (string, error) {
	gitDir, err := ResolveGitDir(repoRoot)
	if err != nil {
		return "main", err
	}
	if gitDir == "" {
		return "main", nil
	}
	
	commonDir, err := GetCommonDir(gitDir)
	if err != nil {
		return "main", err
	}
	if commonDir == "" {
		commonDir = gitDir
	}
	branch, err := ReadRawSymref(commonDir, "refs/remotes/origin/HEAD", "refs/remotes/origin/")
	if err != nil {
		return "main", err
	}
	if branch != "" {
		return branch, nil
	}
	for _, candidate := range []string{"main", "master"} {
		sha, err := ResolveRef(commonDir, "refs/remotes/origin/"+candidate)
		if err != nil {
			return "main", err
		}
		if sha != "" {
			return candidate, nil
		}
	}
	return "main", nil
}




func GetCurrentBranch(repoRoot string) (string, error) {
	gitDir, err := ResolveGitDir(repoRoot)
	if err != nil || gitDir == "" {
		return "", err
	}
	head, err := readGitHead(gitDir)
	if err != nil || head == nil {
		return "", err
	}
	return head.branch, nil
}








func ReadWorktreeHeadSha(worktreePath string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(worktreePath, ".git"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	ptr := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(ptr, "gitdir:") {
		return "", nil
	}
	rel := strings.TrimSpace(strings.TrimPrefix(ptr, "gitdir:"))
	var gitDir string
	if filepath.IsAbs(rel) {
		gitDir = rel
	} else {
		gitDir = filepath.Clean(filepath.Join(worktreePath, rel))
	}
	head, err := readGitHead(gitDir)
	if err != nil {
		return "", err
	}
	if head == nil {
		return "", nil
	}
	if head.branch != "" {
		return ResolveRef(gitDir, "refs/heads/"+head.branch)
	}
	return head.sha, nil
}
