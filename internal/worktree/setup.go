package worktree

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
)



func performPostCreationSetup(ctx context.Context, repoRoot, worktreePath string) {
	
	copySettingsLocal(repoRoot, worktreePath)

	
	configureHooksPath(ctx, repoRoot, worktreePath)

	
	symlinkDirectories(repoRoot, worktreePath, getSymlinkDirectories())

	
	CopyWorktreeIncludeFiles(ctx, repoRoot, worktreePath)
}



func copySettingsLocal(repoRoot, worktreePath string) {
	relPath := filepath.Join(".johnnycode", "settings.local.json")
	src := filepath.Join(repoRoot, relPath)
	dst := filepath.Join(worktreePath, relPath)

	srcData, err := os.ReadFile(src)
	if err != nil {
		return 
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(dst, srcData, 0o644)
}



func configureHooksPath(ctx context.Context, repoRoot, worktreePath string) {
	candidates := []string{
		filepath.Join(repoRoot, ".husky"),
		filepath.Join(repoRoot, ".git", "hooks"),
	}
	var hooksPath string
	for _, c := range candidates {
		info, err := os.Stat(c)
		if err == nil && info.IsDir() {
			hooksPath = c
			break
		}
	}
	if hooksPath == "" {
		return
	}
	_, _, code := runGit(ctx, worktreePath, "config", "core.hooksPath", hooksPath)
	if code != 0 {
		
		return
	}
}



func symlinkDirectories(repoRoot, worktreePath string, dirs []string) {
	for _, dir := range dirs {
		if strings.Contains(dir, "..") {
			continue 
		}
		src := filepath.Join(repoRoot, dir)
		dst := filepath.Join(worktreePath, dir)
		
		_ = os.Symlink(src, dst)
	}
}



func getSymlinkDirectories() []string {
	return worktreeConfig.SymlinkDirectories
}



func CopyWorktreeIncludeFiles(ctx context.Context, repoRoot, worktreePath string) ([]string, error) {
	includeFile := filepath.Join(repoRoot, ".worktreeinclude")
	data, err := os.ReadFile(includeFile)
	if err != nil {
		return nil, nil 
	}

	var patterns []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	if len(patterns) == 0 {
		return nil, nil
	}

	
	stdout, _, code := runGit(ctx, repoRoot,
		"ls-files", "--others", "--ignored", "--exclude-standard", "--directory")
	if code != 0 || strings.TrimSpace(stdout) == "" {
		return nil, nil
	}

	entries := strings.Split(strings.TrimSpace(stdout), "\n")

	
	
	var toCopy []string
	for _, entry := range entries {
		if entry == "" {
			continue
		}
		
		if strings.HasSuffix(entry, "/") {
			continue
		}
		if matchesWorktreeInclude(entry, patterns) {
			toCopy = append(toCopy, entry)
		}
	}

	var copied []string
	for _, rel := range toCopy {
		src := filepath.Join(repoRoot, rel)
		dst := filepath.Join(worktreePath, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			continue
		}
		if err := copyFileContents(src, dst); err != nil {
			continue
		}
		copied = append(copied, rel)
	}
	return copied, nil
}



func matchesWorktreeInclude(path string, patterns []string) bool {
	base := filepath.Base(path)
	for _, p := range patterns {
		p = strings.TrimPrefix(p, "/")
		
		if p == path || p == base {
			return true
		}
		
		if matched, _ := filepath.Match(p, path); matched {
			return true
		}
		
		if matched, _ := filepath.Match(p, base); matched {
			return true
		}
		
		if strings.HasSuffix(p, "/") && strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

func copyFileContents(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}


var worktreeConfig = struct {
	SymlinkDirectories    []string
	StaleCleanupInterval  int 
	StaleCutoffHours      int 
}{
	StaleCutoffHours: 720,
}


func SetWorktreeConfig(symlinkDirs []string, cleanupIntervalSec, cutoffHours int) {
	worktreeConfig.SymlinkDirectories = symlinkDirs
	worktreeConfig.StaleCleanupInterval = cleanupIntervalSec
	if cutoffHours > 0 {
		worktreeConfig.StaleCutoffHours = cutoffHours
	}
}


func GetStaleCutoffHours() int {
	return worktreeConfig.StaleCutoffHours
}


func GetStaleCleanupInterval() int {
	return worktreeConfig.StaleCleanupInterval
}



func FindCanonicalGitRoot(startDir string) string {
	gitDir, err := ResolveGitDir(startDir)
	if err != nil || gitDir == "" {
		return ""
	}
	
	commonDir, err := GetCommonDir(gitDir)
	if err != nil || commonDir == "" {
		
		return filepath.Dir(gitDir)
	}
	
	return filepath.Dir(commonDir)
}

