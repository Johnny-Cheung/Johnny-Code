package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
)

const (
	IndexFileName = "MEMORY.md"

	UserIndexTokens     = 1000
	ProjectIndexTokens  = 2000
	MaxEntriesPerScope  = 256
	WarnEntriesPerScope = 200
	SearchLimit         = 10
)

type WriteAction string

const (
	ActionCreated WriteAction = "created"
	ActionUpdated WriteAction = "updated"
)

// ErrNotFound 表示指定作用域下没有该记忆。
var ErrNotFound = errors.New("记忆不存在")

type WriteResult struct {
	Action WriteAction
	Item   Item
	Path   string
}

// Store 管理用户级和项目级记忆文件及其派生索引。
type Store struct {
	HomeDir string
	WorkDir string
	Project ProjectInfo
	Warning string
}

func NewStore(workDir string) (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("无法确定用户目录：%w", err)
	}
	return NewStoreAt(home, workDir)
}

func NewStoreAt(home, workDir string) (*Store, error) {
	home = strings.TrimSpace(home)
	workDir = strings.TrimSpace(workDir)
	if home == "" {
		return nil, errors.New("用户目录不能为空")
	}
	if workDir == "" {
		return nil, errors.New("工作目录不能为空")
	}
	info := DiscoverProject(workDir)
	return &Store{HomeDir: home, WorkDir: workDir, Project: info}, nil
}

func (s *Store) UserDir() string {
	if s == nil {
		return ""
	}
	return filepath.Join(s.HomeDir, ".johnny", "memory")
}

func (s *Store) ProjectDir() string {
	if s == nil || strings.TrimSpace(s.Project.ID) == "" {
		return ""
	}
	return filepath.Join(s.HomeDir, ".johnny", "projects", s.Project.ID, "memory")
}

func (s *Store) Dir(scope Scope) (string, error) {
	switch scope {
	case ScopeUser:
		return s.UserDir(), nil
	case ScopeProject:
		dir := s.ProjectDir()
		if dir == "" {
			return "", errors.New("项目记忆目录不可用")
		}
		return dir, nil
	default:
		return "", fmt.Errorf("未知记忆作用域：%q", scope)
	}
}

func (s *Store) IndexPath(scope Scope) (string, error) {
	dir, err := s.Dir(scope)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, IndexFileName), nil
}

func (s *Store) ItemPath(scope Scope, name string) (string, error) {
	if !itemNamePattern.MatchString(name) {
		return "", fmt.Errorf("记忆名称无效：%q", name)
	}
	dir, err := s.Dir(scope)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".md"), nil
}

// List 返回指定作用域的记忆；scope 为空时返回两层。
func (s *Store) List(scope Scope) ([]Item, []string, error) {
	scopes := []Scope{ScopeUser, ScopeProject}
	if scope != "" {
		scopes = []Scope{scope}
	}
	var items []Item
	var warnings []string
	for _, current := range scopes {
		loaded, currentWarnings, err := s.loadItems(current)
		if err != nil {
			return nil, warnings, err
		}
		items = append(items, loaded...)
		warnings = append(warnings, currentWarnings...)
	}
	sortItems(items)
	return items, warnings, nil
}

// Search 在名称、描述和正文中做大小写不敏感的子串搜索。
func (s *Store) Search(query string, scope Scope, limit int) ([]Item, []string, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, nil, errors.New("搜索关键词不能为空")
	}
	items, warnings, err := s.List(scope)
	if err != nil {
		return nil, warnings, err
	}
	var matches []Item
	for _, item := range items {
		if strings.Contains(strings.ToLower(item.Name), query) ||
			strings.Contains(strings.ToLower(item.Description), query) ||
			strings.Contains(strings.ToLower(item.Body), query) {
			matches = append(matches, item)
		}
	}
	sortItems(matches)
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, warnings, nil
}

func (s *Store) Read(scope Scope, name string) (Item, error) {
	path, err := s.ItemPath(scope, name)
	if err != nil {
		return Item{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Item{}, fmt.Errorf("%w：%s/%s", ErrNotFound, scope, name)
		}
		return Item{}, err
	}
	item, err := ParseItem(path, data)
	if err != nil {
		return Item{}, err
	}
	info, _ := os.Stat(path)
	fillItemTimes(&item, info)
	item.Scope = scope
	item.Project = s.projectFor(scope)
	if item.Name != name {
		return Item{}, fmt.Errorf("记忆文件名与 name 不一致：%s != %s", path, item.Name)
	}
	return item, nil
}

// Write 新建或更新一条记忆，并在同一把锁内重建索引。
func (s *Store) Write(ctx context.Context, item Item) (WriteResult, error) {
	if item.Scope == "" {
		item.Scope = ScopeProject
	}
	item.Project = s.projectFor(item.Scope)
	if item.Confidence == "" {
		item.Confidence = ConfidenceMedium
	}
	if err := item.Validate(); err != nil {
		return WriteResult{}, err
	}
	dir, err := s.Dir(item.Scope)
	if err != nil {
		return WriteResult{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return WriteResult{}, err
	}
	path, err := s.ItemPath(item.Scope, item.Name)
	if err != nil {
		return WriteResult{}, err
	}
	action := ActionCreated
	err = withFileLock(ctx, dir, ".lock", func() error {
		now := time.Now().UTC()
		existing, readErr := s.Read(item.Scope, item.Name)
		switch {
		case readErr == nil:
			action = ActionUpdated
			item.Created = existing.Created
			if item.Created.IsZero() {
				item.Created = now
			}
		case errors.Is(readErr, ErrNotFound):
			// 新建。
		default:
			return readErr
		}
		if item.Created.IsZero() {
			item.Created = now
		}
		item.Updated = now
		item.Path = path
		data, err := item.Markdown()
		if err != nil {
			return err
		}
		if err := writeFileAtomic(path, data, 0o600); err != nil {
			return err
		}
		return s.rebuildIndexLocked(item.Scope)
	})
	if err != nil {
		return WriteResult{}, err
	}
	_ = RecordProject(s.HomeDir, s.Project)
	return WriteResult{Action: action, Item: item, Path: path}, nil
}

func (s *Store) Delete(ctx context.Context, scope Scope, name string) (string, error) {
	dir, err := s.Dir(scope)
	if err != nil {
		return "", err
	}
	path, err := s.ItemPath(scope, name)
	if err != nil {
		return "", err
	}
	err = withFileLock(ctx, dir, ".lock", func() error {
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("%w：%s/%s", ErrNotFound, scope, name)
			}
			return err
		}
		return s.rebuildIndexLocked(scope)
	})
	if err != nil {
		return "", err
	}
	_ = RecordProject(s.HomeDir, s.Project)
	return path, nil
}

// IndexText 返回注入固定前缀的两个索引区块。索引从文件实时构建，并按 token 预算截断。
func (s *Store) IndexText() (string, string) {
	if s == nil {
		return "", ""
	}
	var sections []string
	var warnings []string
	if s.Warning != "" {
		warnings = append(warnings, s.Warning)
	}
	for _, scope := range []Scope{ScopeUser, ScopeProject} {
		items, currentWarnings, err := s.loadItems(scope)
		warnings = append(warnings, currentWarnings...)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s记忆索引读取失败：%s", scopeLabel(scope), err))
			continue
		}
		if len(items) == 0 {
			continue
		}
		budget := UserIndexTokens
		if scope == ScopeProject {
			budget = ProjectIndexTokens
		}
		section, truncated := formatIndexSection(scope, items, budget)
		if truncated {
			warnings = append(warnings, fmt.Sprintf("%s记忆索引超过 %d token，已截断。", scopeLabel(scope), budget))
		}
		sections = append(sections, section)
	}
	return strings.Join(sections, "\n\n"), strings.Join(warnings, "；")
}

func (s *Store) loadItems(scope Scope) ([]Item, []string, error) {
	dir, err := s.Dir(scope)
	if err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var items []Item
	var warnings []string
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == IndexFileName || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s：%s", entry.Name(), err))
			continue
		}
		item, err := ParseItem(path, data)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s：%s", entry.Name(), err))
			continue
		}
		info, _ := entry.Info()
		fillItemTimes(&item, info)
		item.Scope = scope
		item.Project = s.projectFor(scope)
		items = append(items, item)
	}
	sortItems(items)
	return items, warnings, nil
}

func (s *Store) rebuildIndexLocked(scope Scope) error {
	items, _, err := s.loadItems(scope)
	if err != nil {
		return err
	}
	path, err := s.IndexPath(scope)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(formatFullIndex(scope, items)), 0o600)
}

func (s *Store) projectFor(scope Scope) string {
	if scope == ScopeProject {
		return s.Project.ID
	}
	return ""
}

func fillItemTimes(item *Item, info os.FileInfo) {
	if item == nil {
		return
	}
	if item.Created.IsZero() {
		if info != nil {
			item.Created = info.ModTime().UTC()
		} else {
			item.Created = time.Now().UTC()
		}
	}
	if item.Updated.IsZero() {
		item.Updated = item.Created
	}
}

func sortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if left.Scope != right.Scope {
			// 项目知识优先于跨项目偏好，便于项目任务先看到更具体的内容。
			return left.Scope == ScopeProject
		}
		if left.Confidence != right.Confidence {
			return confidenceRank(left.Confidence) > confidenceRank(right.Confidence)
		}
		if !left.Updated.Equal(right.Updated) {
			return left.Updated.After(right.Updated)
		}
		return left.Name < right.Name
	})
}

func confidenceRank(confidence Confidence) int {
	switch confidence {
	case ConfidenceHigh:
		return 3
	case ConfidenceMedium:
		return 2
	case ConfidenceLow:
		return 1
	default:
		return 0
	}
}

func formatFullIndex(scope Scope, items []Item) string {
	var builder strings.Builder
	builder.WriteString("# ")
	builder.WriteString(indexTitle(scope))
	builder.WriteString("\n")
	for _, item := range items {
		builder.WriteString(indexLine(item))
		builder.WriteByte('\n')
	}
	return builder.String()
}

func formatIndexSection(scope Scope, items []Item, budget int) (string, bool) {
	var builder strings.Builder
	builder.WriteString("## ")
	builder.WriteString(scopeLabel(scope))
	builder.WriteString("记忆索引\n")
	used := contextview.EstimateText(builder.String())
	written := 0
	for _, item := range items {
		line := indexLine(item)
		cost := contextview.EstimateText(line)
		if used+cost > budget {
			break
		}
		builder.WriteString(line)
		builder.WriteByte('\n')
		used += cost
		written++
	}
	if written < len(items) {
		fmt.Fprintf(&builder, "另有 %d 条，使用 memory search 查找。\n", len(items)-written)
		return strings.TrimRight(builder.String(), "\n"), true
	}
	return strings.TrimRight(builder.String(), "\n"), false
}

func indexLine(item Item) string {
	description := OneLine(RuneLimit(item.Description, 80))
	return fmt.Sprintf("- `%s` [%s, %s, %s] — %s", item.Name, item.Type, item.Confidence, item.Updated.Format("2006-01-02"), description)
}

func indexTitle(scope Scope) string {
	if scope == ScopeProject {
		return "Project Memory"
	}
	return "User Memory"
}

func scopeLabel(scope Scope) string {
	if scope == ScopeProject {
		return "项目"
	}
	return "用户"
}
