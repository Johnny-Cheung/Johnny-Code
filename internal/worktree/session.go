package worktree

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)


type WorktreeSession struct {
	OriginalCwd        string `json:"original_cwd"`
	WorktreePath       string `json:"worktree_path"`
	WorktreeName       string `json:"worktree_name"`
	WorktreeBranch     string `json:"worktree_branch,omitempty"`
	OriginalBranch     string `json:"original_branch,omitempty"`
	OriginalHeadCommit string `json:"original_head_commit,omitempty"`
	SessionID          string `json:"session_id"`
	HookBased          bool   `json:"hook_based,omitempty"`
	CreationDurationMs int64  `json:"creation_duration_ms,omitempty"`
}


var (
	currentWorktreeSession *WorktreeSession
	sessionMu              sync.RWMutex
)


func GetCurrentWorktreeSession() *WorktreeSession {
	sessionMu.RLock()
	defer sessionMu.RUnlock()
	return currentWorktreeSession
}



func RestoreWorktreeSession(session *WorktreeSession) {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	currentWorktreeSession = session
}


func sessionFilePath(repoRoot string) string {
	return filepath.Join(repoRoot, ".johnnycode", "worktree_session.json")
}


func SaveWorktreeSession(repoRoot string, session *WorktreeSession) error {
	path := sessionFilePath(repoRoot)
	if session == nil {
		_ = os.Remove(path)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}



func LoadWorktreeSession(repoRoot string) (*WorktreeSession, error) {
	path := sessionFilePath(repoRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var session WorktreeSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	return &session, nil
}


func CreateWorktreeForSession(ctx context.Context, sessionID, slug, repoRoot string) (*WorktreeSession, error) {
	if err := ValidateWorktreeSlug(slug); err != nil {
		return nil, err
	}

	if repoRoot == "" {
		return nil, errorf("cannot create a worktree: not in a git repository")
	}

	originalCwd, _ := os.Getwd()
	originalBranch, _ := GetCurrentBranch(repoRoot)

	start := time.Now()
	result, err := getOrCreateWorktree(ctx, repoRoot, slug)
	if err != nil {
		return nil, err
	}

	var creationDurationMs int64
	if !result.Existed {
		performPostCreationSetup(ctx, repoRoot, result.WorktreePath)
		creationDurationMs = time.Since(start).Milliseconds()
	}

	session := &WorktreeSession{
		OriginalCwd:        originalCwd,
		WorktreePath:       result.WorktreePath,
		WorktreeName:       slug,
		WorktreeBranch:     result.WorktreeBranch,
		OriginalBranch:     originalBranch,
		OriginalHeadCommit: result.HeadCommit,
		SessionID:          sessionID,
		CreationDurationMs: creationDurationMs,
	}

	sessionMu.Lock()
	currentWorktreeSession = session
	sessionMu.Unlock()

	_ = SaveWorktreeSession(repoRoot, session)
	return session, nil
}


func KeepWorktree(repoRoot string) error {
	sessionMu.Lock()
	session := currentWorktreeSession
	currentWorktreeSession = nil
	sessionMu.Unlock()

	if session == nil {
		return nil
	}

	if err := os.Chdir(session.OriginalCwd); err != nil {
		return err
	}

	_ = SaveWorktreeSession(repoRoot, nil)
	return nil
}


func CleanupWorktree(ctx context.Context, repoRoot string) error {
	sessionMu.Lock()
	session := currentWorktreeSession
	currentWorktreeSession = nil
	sessionMu.Unlock()

	if session == nil {
		return nil
	}

	if err := os.Chdir(session.OriginalCwd); err != nil {
		return err
	}

	
	_, _, code := runGit(ctx, session.OriginalCwd,
		"worktree", "remove", "--force", session.WorktreePath)
	if code != 0 {
		
	}

	
	if session.WorktreeBranch != "" {
		
		time.Sleep(100 * time.Millisecond)
		runGit(ctx, session.OriginalCwd, "branch", "-D", session.WorktreeBranch)
	}

	_ = SaveWorktreeSession(repoRoot, nil)
	return nil
}

func errorf(format string, args ...any) error {
	return &worktreeError{msg: format}
}

type worktreeError struct {
	msg string
}

func (e *worktreeError) Error() string { return e.msg }
