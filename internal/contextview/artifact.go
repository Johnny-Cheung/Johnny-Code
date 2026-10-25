package contextview

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
)

// ArtifactStore 保存本轮会话的大工具输出。它不是会话记忆，也不是恢复来源。
type ArtifactStore struct {
	workDir   string
	sessionID string
	root      string
	mu        sync.Mutex
}

func NewArtifactStore(workDir, sessionID string) *ArtifactStore {
	store := &ArtifactStore{workDir: strings.TrimSpace(workDir), sessionID: sanitizeSessionID(sessionID)}
	if store.workDir != "" && store.sessionID != "" {
		store.root = filepath.Join(store.workDir, ".johnny", "artifacts", store.sessionID)
	}
	return store
}

func (s *ArtifactStore) Enabled() bool {
	return s != nil && s.root != ""
}

func (s *ArtifactStore) Dir() string {
	if s == nil {
		return ""
	}
	return s.root
}

// Save 写入完整工具输出并返回相对工作目录的路径。
func (s *ArtifactStore) Save(turn, call int, tool, content string) (string, error) {
	if !s.Enabled() {
		return "", errors.New("工具输出存档未启用")
	}
	path := filepath.Join(s.root, fmt.Sprintf("turn-%d", turn), fmt.Sprintf("call-%d-%s.log", call, sanitizeName(tool)))
	relative, err := filepath.Rel(s.workDir, path)
	if err != nil {
		return "", err
	}
	clean := Sanitize(content)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := file.WriteString(clean); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return filepath.ToSlash(relative), nil
}

// Close 删除当前会话的存档目录；异常退出时可能留下目录，但不会影响会话。
func (s *ArtifactStore) Close() error {
	if !s.Enabled() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.RemoveAll(s.root)
}

// Sanitize 清理终端控制序列和非法 UTF-8，避免再次读取时污染模型上下文。
func Sanitize(content string) string {
	content = ansi.Strip(content)
	return strings.ToValidUTF8(content, "�")
}

func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "tool"
	}
	var builder strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteByte('-')
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return "tool"
	}
	return result
}

func sanitizeSessionID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteByte('-')
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return "session"
	}
	return result
}
