package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const projectRulesVersion = 1

var (
	errProjectRulesUntrusted = errors.New("项目权限规则尚未受信任")
	errInvalidProjectRules   = errors.New("项目权限规则格式无效")
)

type PathKind string

const (
	PathKindFile PathKind = "file"
	PathKindDir  PathKind = "dir"
)

// PathRule 记录用户批准的项目路径。目录规则覆盖子项，文件规则只覆盖
// 该文件本身。路径在加载和新增时都解析为真实路径。
type PathRule struct {
	Path string   `json:"path"`
	Kind PathKind `json:"kind"`
}

type projectRulesFile struct {
	Version         int        `json:"version"`
	CommandPrefixes [][]string `json:"command_prefixes"`
	Paths           []PathRule `json:"paths"`
}

// ProjectRules 保存当前项目的动态命令前缀和路径规则。用户级白名单和
// 黑名单不在这里，也不会写入项目文件。
type ProjectRules struct {
	mu              sync.RWMutex
	projectRoot     string
	workDir         string
	filePath        string
	commandPrefixes [][]string
	paths           []PathRule
	trusted         bool
	onSave          func(hash string) error
}

func projectRulesPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".johnny", "permissions.local.json")
}

// FindProjectRoot 从工作目录向上寻找 Git 根目录；没有 Git 时使用工作目录。
func FindProjectRoot(workDir string) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		return "", errors.New("工作目录不能为空")
	}
	absolute, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	for current := resolved; ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return resolved, nil
		}
	}
}

// NewProjectRules 创建一个空的可写规则集，供没有规则文件或文件无效的
// 项目使用。
func NewProjectRules(projectRoot, workDir string) (*ProjectRules, error) {
	projectRoot, err := canonicalDirectory(projectRoot)
	if err != nil {
		return nil, err
	}
	workDir, err = canonicalDirectory(workDir)
	if err != nil {
		return nil, err
	}
	return newProjectRules(projectRoot, workDir), nil
}

// LoadProjectRules 读取项目规则文件，但不决定是否信任。exists 为 false 时
// 返回一个可写入的空规则集；文件存在时 trusted 为 false，调用方确认后再
// 设置 trusted。
func LoadProjectRules(projectRoot, workDir string) (rules *ProjectRules, hash string, exists bool, err error) {
	projectRoot, err = canonicalDirectory(projectRoot)
	if err != nil {
		return nil, "", false, err
	}
	workDir, err = canonicalDirectory(workDir)
	if err != nil {
		return nil, "", false, err
	}
	path := projectRulesPath(projectRoot)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return newProjectRules(projectRoot, workDir), "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	if len(data) > 1<<20 {
		return nil, "", false, fmt.Errorf("%w：文件过大", errInvalidProjectRules)
	}
	hash = contentHash(data)
	var file projectRulesFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, hash, true, fmt.Errorf("%w：%v", errInvalidProjectRules, err)
	}
	if file.Version != projectRulesVersion {
		return nil, hash, true, fmt.Errorf("%w：不支持版本 %d", errInvalidProjectRules, file.Version)
	}
	rules = newProjectRules(projectRoot, workDir)
	rules.trusted = false
	for _, prefix := range file.CommandPrefixes {
		prefix = append([]string(nil), prefix...)
		if !validRecordablePrefix(prefix) {
			return nil, hash, true, fmt.Errorf("%w：命令前缀 %q 不可记录", errInvalidProjectRules, strings.Join(prefix, " "))
		}
		rules.commandPrefixes = append(rules.commandPrefixes, prefix)
	}
	for _, rule := range file.Paths {
		canonical, err := canonicalPathRule(rule.Path, rule.Kind)
		if err != nil {
			return nil, hash, true, fmt.Errorf("%w：%v", errInvalidProjectRules, err)
		}
		rules.paths = append(rules.paths, canonical)
	}
	rules.normalize()
	return rules, hash, true, nil
}

func newProjectRules(projectRoot, workDir string) *ProjectRules {
	return &ProjectRules{
		projectRoot: projectRoot,
		workDir:     workDir,
		filePath:    projectRulesPath(projectRoot),
		trusted:     true,
	}
}

func (r *ProjectRules) SetSaveHook(hook func(hash string) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onSave = hook
}

func (r *ProjectRules) Trusted() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.trusted
}

func (r *ProjectRules) SetTrusted(trusted bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trusted = trusted
}

func (r *ProjectRules) CanRecord() bool {
	return r != nil && r.Trusted()
}

func (r *ProjectRules) ProjectRoot() string {
	if r == nil {
		return ""
	}
	return r.projectRoot
}

func (r *ProjectRules) FilePath() string {
	if r == nil {
		return ""
	}
	return r.filePath
}

func (r *ProjectRules) WorkDir() string {
	if r == nil {
		return ""
	}
	return r.workDir
}

func (r *ProjectRules) ContainsPath(path string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if pathWithin(r.workDir, path) {
		return true
	}
	if !r.trusted {
		return false
	}
	for _, rule := range r.paths {
		switch rule.Kind {
		case PathKindDir:
			if pathWithin(rule.Path, path) {
				return true
			}
		case PathKindFile:
			if path == rule.Path {
				return true
			}
		}
	}
	return false
}

func (r *ProjectRules) ContainsPrefix(tokens []string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.trusted {
		return false
	}
	for _, prefix := range r.commandPrefixes {
		if prefixMatches(prefix, tokens) {
			return true
		}
	}
	return false
}

func (r *ProjectRules) CommandPrefixes() [][]string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return clonePrefixes(r.commandPrefixes)
}

func (r *ProjectRules) PathsRules() []PathRule {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]PathRule(nil), r.paths...)
}

func (r *ProjectRules) AddRules(prefixes [][]string, paths []PathRule) error {
	if r == nil {
		return errors.New("项目规则不能为空")
	}
	validatedPrefixes := make([][]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		prefix = append([]string(nil), prefix...)
		if !validRecordablePrefix(prefix) {
			return fmt.Errorf("命令前缀不可记录：%s", strings.Join(prefix, " "))
		}
		validatedPrefixes = append(validatedPrefixes, prefix)
	}
	validatedPaths := make([]PathRule, 0, len(paths))
	for _, path := range paths {
		rule, err := canonicalPathRule(path.Path, path.Kind)
		if err != nil {
			return err
		}
		validatedPaths = append(validatedPaths, rule)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.trusted {
		return errProjectRulesUntrusted
	}
	oldPrefixes, oldPaths := r.snapshotLocked()
	for _, prefix := range validatedPrefixes {
		found := false
		for _, existing := range r.commandPrefixes {
			if stringSlicesEqual(existing, prefix) {
				found = true
				break
			}
		}
		if !found {
			r.commandPrefixes = append(r.commandPrefixes, prefix)
		}
	}
	for _, rule := range validatedPaths {
		found := false
		for _, existing := range r.paths {
			if existing.Path == rule.Path && existing.Kind == rule.Kind {
				found = true
				break
			}
		}
		if !found {
			r.paths = append(r.paths, rule)
		}
	}
	r.normalizeLocked()
	if err := r.saveLocked(); err != nil {
		r.restoreLocked(oldPrefixes, oldPaths)
		return err
	}
	return nil
}

func (r *ProjectRules) AddPrefix(prefix []string) error {
	if r == nil {
		return errors.New("项目规则不能为空")
	}
	prefix = append([]string(nil), prefix...)
	if !validRecordablePrefix(prefix) {
		return fmt.Errorf("命令前缀不可记录：%s", strings.Join(prefix, " "))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.trusted {
		return errProjectRulesUntrusted
	}
	for _, existing := range r.commandPrefixes {
		if stringSlicesEqual(existing, prefix) {
			return nil
		}
	}
	oldPrefixes, oldPaths := r.snapshotLocked()
	r.commandPrefixes = append(r.commandPrefixes, prefix)
	r.normalizeLocked()
	if err := r.saveLocked(); err != nil {
		r.restoreLocked(oldPrefixes, oldPaths)
		return err
	}
	return nil
}

func (r *ProjectRules) AddPath(path string, kind PathKind) error {
	if r == nil {
		return errors.New("项目规则不能为空")
	}
	rule, err := canonicalPathRule(path, kind)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.trusted {
		return errProjectRulesUntrusted
	}
	for _, existing := range r.paths {
		if existing.Path == rule.Path && existing.Kind == rule.Kind {
			return nil
		}
	}
	oldPrefixes, oldPaths := r.snapshotLocked()
	r.paths = append(r.paths, rule)
	r.normalizeLocked()
	if err := r.saveLocked(); err != nil {
		r.restoreLocked(oldPrefixes, oldPaths)
		return err
	}
	return nil
}

func (r *ProjectRules) RemovePrefix(index int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.trusted {
		return errProjectRulesUntrusted
	}
	if index < 1 || index > len(r.commandPrefixes) {
		return fmt.Errorf("命令前缀序号超出范围：%d", index)
	}
	oldPrefixes, oldPaths := r.snapshotLocked()
	r.commandPrefixes = append(r.commandPrefixes[:index-1], r.commandPrefixes[index:]...)
	if err := r.saveLocked(); err != nil {
		r.restoreLocked(oldPrefixes, oldPaths)
		return err
	}
	return nil
}

func (r *ProjectRules) RemovePath(index int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.trusted {
		return errProjectRulesUntrusted
	}
	if index < 1 || index > len(r.paths) {
		return fmt.Errorf("路径序号超出范围：%d", index)
	}
	oldPrefixes, oldPaths := r.snapshotLocked()
	r.paths = append(r.paths[:index-1], r.paths[index:]...)
	if err := r.saveLocked(); err != nil {
		r.restoreLocked(oldPrefixes, oldPaths)
		return err
	}
	return nil
}

func (r *ProjectRules) Clear() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.trusted {
		return errProjectRulesUntrusted
	}
	oldPrefixes, oldPaths := r.snapshotLocked()
	r.commandPrefixes = nil
	r.paths = nil
	if err := r.saveLocked(); err != nil {
		r.restoreLocked(oldPrefixes, oldPaths)
		return err
	}
	return nil
}

func (r *ProjectRules) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(r.filePath), 0o700); err != nil {
		return err
	}
	file := projectRulesFile{
		Version:         projectRulesVersion,
		CommandPrefixes: clonePrefixes(r.commandPrefixes),
		Paths:           append([]PathRule(nil), r.paths...),
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if r.onSave != nil {
		if err := r.onSave(contentHash(data)); err != nil {
			return fmt.Errorf("更新项目规则信任记录失败：%w", err)
		}
	}
	return atomicWriteFile(r.filePath, data, 0o600)
}

func (r *ProjectRules) snapshotLocked() ([][]string, []PathRule) {
	return clonePrefixes(r.commandPrefixes), append([]PathRule(nil), r.paths...)
}

func (r *ProjectRules) restoreLocked(prefixes [][]string, paths []PathRule) {
	r.commandPrefixes = prefixes
	r.paths = paths
}

func (r *ProjectRules) normalize() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.normalizeLocked()
}

func (r *ProjectRules) normalizeLocked() {
	sort.Slice(r.commandPrefixes, func(i, j int) bool {
		return strings.Join(r.commandPrefixes[i], "\x00") < strings.Join(r.commandPrefixes[j], "\x00")
	})
	sort.Slice(r.paths, func(i, j int) bool {
		if r.paths[i].Path == r.paths[j].Path {
			return r.paths[i].Kind < r.paths[j].Kind
		}
		return r.paths[i].Path < r.paths[j].Path
	})
}

func clonePrefixes(prefixes [][]string) [][]string {
	if prefixes == nil {
		return nil
	}
	cloned := make([][]string, len(prefixes))
	for i, prefix := range prefixes {
		cloned[i] = append([]string(nil), prefix...)
	}
	return cloned
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func canonicalDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("目录不能为空")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("路径不是目录")
	}
	return resolved, nil
}

func canonicalPathRule(path string, kind PathKind) (PathRule, error) {
	if strings.TrimSpace(path) == "" || strings.IndexByte(path, 0) >= 0 {
		return PathRule{}, errors.New("路径不能为空或包含无效字符")
	}
	if !filepath.IsAbs(path) {
		return PathRule{}, errors.New("项目路径规则必须是绝对路径")
	}
	// 保留原始字节：以空格结尾的路径是合法目标。解析时先展开符号链接
	// 再应用 ".."，因此记录下来的规则就是内核实际访问的目标。
	resolved, err := resolveExistingAncestors(path)
	if err != nil {
		return PathRule{}, err
	}
	info, statErr := os.Stat(resolved)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return PathRule{}, statErr
	}
	if kind == "" {
		if statErr == nil && info.IsDir() {
			kind = PathKindDir
		} else {
			kind = PathKindFile
		}
	}
	switch kind {
	case PathKindDir:
		if statErr == nil && !info.IsDir() {
			return PathRule{}, errors.New("目录规则指向的不是目录")
		}
	case PathKindFile:
		if statErr == nil && info.IsDir() {
			return PathRule{}, errors.New("文件规则指向的是目录")
		}
	case "":
		return PathRule{}, errors.New("路径规则缺少类型")
	default:
		return PathRule{}, fmt.Errorf("未知路径规则类型：%s", kind)
	}
	return PathRule{Path: resolved, Kind: kind}, nil
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
