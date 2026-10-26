package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ProjectInfo 描述当前工作目录所属的稳定项目身份。
type ProjectInfo struct {
	ID     string
	Root   string
	Remote string
}

// DiscoverProject 使用 remote 优先、路径兜底生成 project-id。
func DiscoverProject(workDir string) ProjectInfo {
	root := ProjectRoot(workDir)
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	root = canonicalPath(root)
	info := ProjectInfo{Root: root}
	identity := ""
	if isGitRepo(root) {
		if remote := preferredRemote(root); remote != "" {
			info.Remote = normalizeRemote(remote)
		}
		if info.Remote != "" {
			identity = info.Remote
		} else if common := gitOutput(root, "rev-parse", "--git-common-dir"); common != "" {
			if !filepath.IsAbs(common) {
				common = filepath.Join(root, common)
			}
			identity = "git-common:" + canonicalPath(common)
		}
	}
	if identity == "" {
		identity = "path:" + root
	}
	slug := slugify(filepath.Base(root))
	sum := sha256.Sum256([]byte(identity))
	info.ID = slug + "-" + hex.EncodeToString(sum[:])[:16]
	return info
}

func isGitRepo(root string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), gitLookupTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--is-inside-work-tree").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func preferredRemote(root string) string {
	remotes := strings.Fields(gitOutput(root, "remote"))
	if len(remotes) == 0 {
		return ""
	}
	sort.Strings(remotes)
	name := remotes[0]
	for _, candidate := range remotes {
		if candidate == "origin" {
			name = candidate
			break
		}
	}
	return strings.TrimSpace(gitOutput(root, "remote", "get-url", name))
}

func gitOutput(root string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitLookupTimeout)
	defer cancel()
	full := append([]string{"-C", root}, args...)
	out, err := exec.CommandContext(ctx, "git", full...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// normalizeRemote 将常见 remote 形式统一为 host/path，去掉凭据和 .git。
func normalizeRemote(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "file://") {
		if parsed, err := url.Parse(raw); err == nil && parsed.Path != "" {
			return "path:" + canonicalPath(parsed.Path)
		}
	}
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		host := strings.ToLower(parsed.Hostname())
		path := strings.Trim(parsed.Path, "/")
		path = strings.TrimSuffix(path, ".git")
		if host == "" || path == "" {
			return ""
		}
		return host + "/" + path
	}
	if strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") || filepath.IsAbs(raw) || (!strings.Contains(raw, ":") && !strings.Contains(raw, "@")) {
		return "path:" + canonicalPath(raw)
	}
	hostPart, pathPart, ok := strings.Cut(raw, ":")
	if !ok {
		return ""
	}
	if at := strings.LastIndex(hostPart, "@"); at >= 0 {
		hostPart = hostPart[at+1:]
	}
	hostPart = strings.ToLower(strings.TrimSpace(hostPart))
	pathPart = strings.Trim(pathPart, "/")
	pathPart = strings.TrimSuffix(pathPart, ".git")
	if hostPart == "" || pathPart == "" {
		return ""
	}
	return hostPart + "/" + pathPart
}

func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && builder.Len() > 0 {
				builder.WriteByte('-')
				lastDash = true
			}
		}
		if builder.Len() >= 32 {
			break
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		return "project"
	}
	return slug
}

func canonicalPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

type projectIndex struct {
	Version  int                     `json:"version"`
	Projects map[string]projectEntry `json:"projects"`
}

type projectEntry struct {
	Roots   []string  `json:"roots"`
	Remote  string    `json:"remote,omitempty"`
	Updated time.Time `json:"updated"`
}

// RecordProject 更新 ~/.johnny/projects/index.json，便于排查和 /memory 展示。
func RecordProject(home string, info ProjectInfo) error {
	home = strings.TrimSpace(home)
	if home == "" {
		return nil
	}
	dir := filepath.Join(home, ".johnny", "projects")
	path := filepath.Join(dir, "index.json")
	ctx, cancel := context.WithTimeout(context.Background(), lockWaitTimeout)
	defer cancel()
	return withFileLock(ctx, dir, "index.lock", func() error {
		index := projectIndex{Version: 1, Projects: make(map[string]projectEntry)}
		if data, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(data, &index); err != nil {
				return err
			}
			if index.Projects == nil {
				index.Projects = make(map[string]projectEntry)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		entry := index.Projects[info.ID]
		entry.Remote = info.Remote
		entry.Updated = time.Now().UTC()
		entry.Roots = appendUnique(entry.Roots, info.Root)
		if len(entry.Roots) > 8 {
			entry.Roots = entry.Roots[len(entry.Roots)-8:]
		}
		index.Projects[info.ID] = entry
		data, err := json.MarshalIndent(index, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
		return writeFileAtomic(path, data, 0o600)
	})
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
