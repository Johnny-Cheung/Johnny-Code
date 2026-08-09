package filehistory

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxSnapshots = 100

type Backup struct {
	BackupPath string    `json:"backup_path"`
	Version    int       `json:"version"`
	Time       time.Time `json:"time"`
}

type Snapshot struct {
	MessageIndex int               `json:"message_index"`
	UserText     string            `json:"user_text"`
	Backups      map[string]Backup `json:"backups"`
	Timestamp    time.Time         `json:"timestamp"`
}

type History struct {
	mu           sync.Mutex
	sessionDir   string
	trackedFiles map[string]int 
	snapshots    []Snapshot
}

func New(baseDir, sessionID string) *History {
	dir := filepath.Join(baseDir, ".johnnycode", "file-history", sessionID)
	_ = os.MkdirAll(dir, 0o755)
	return &History{
		sessionDir:   dir,
		trackedFiles: make(map[string]int),
	}
}

func backupName(filePath string, version int) string {
	h := sha256.Sum256([]byte(filePath))
	return fmt.Sprintf("%x@v%d", h[:8], version)
}




func (h *History) TrackEdit(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	ver := h.trackedFiles[absPath]
	newVer := ver + 1

	data, err := os.ReadFile(absPath)
	if err == nil {
		bp := filepath.Join(h.sessionDir, backupName(absPath, newVer))
		_ = os.WriteFile(bp, data, 0o644)
	}
	
	

	h.trackedFiles[absPath] = newVer
}



func (h *History) MakeSnapshot(msgIndex int, userText string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	backups := make(map[string]Backup, len(h.trackedFiles))
	for path, ver := range h.trackedFiles {
		bp := filepath.Join(h.sessionDir, backupName(path, ver))
		if _, err := os.Stat(bp); err != nil {
			if data, readErr := os.ReadFile(path); readErr == nil {
				_ = os.WriteFile(bp, data, 0o644)
			}
		}
		backups[path] = Backup{BackupPath: bp, Version: ver, Time: time.Now()}
	}

	snap := Snapshot{
		MessageIndex: msgIndex,
		UserText:     userText,
		Backups:      backups,
		Timestamp:    time.Now(),
	}

	h.snapshots = append(h.snapshots, snap)
	if len(h.snapshots) > maxSnapshots {
		h.snapshots = h.snapshots[len(h.snapshots)-maxSnapshots:]
	}
}


func (h *History) GetSnapshots() []Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Snapshot, len(h.snapshots))
	copy(out, h.snapshots)
	return out
}



func (h *History) Rewind(snapshotIndex int) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if snapshotIndex < 0 || snapshotIndex >= len(h.snapshots) {
		return nil, fmt.Errorf("invalid snapshot index %d", snapshotIndex)
	}

	target := h.snapshots[snapshotIndex]
	var changed []string

	for path, backup := range target.Backups {
		backupData, err := os.ReadFile(backup.BackupPath)
		if err != nil {
			
			if _, statErr := os.Stat(path); statErr == nil {
				_ = os.Remove(path)
				changed = append(changed, path)
			}
			continue
		}

		currentData, _ := os.ReadFile(path)
		if string(currentData) != string(backupData) {
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			if writeErr := os.WriteFile(path, backupData, 0o644); writeErr == nil {
				changed = append(changed, path)
			}
		}
	}

	
	h.snapshots = h.snapshots[:snapshotIndex+1]

	
	for path, backup := range target.Backups {
		h.trackedFiles[path] = backup.Version
	}

	return changed, nil
}


func (h *History) HasSnapshots() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.snapshots) > 0
}


func (h *History) Save() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	data, err := json.MarshalIndent(h.snapshots, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(h.sessionDir, "snapshots.json"), data, 0o644)
}
