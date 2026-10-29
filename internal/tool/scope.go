package tool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var errPathOutsideWorkspace = errors.New("路径超出工作目录")

// workspaceScope 把只读模式的路径限制在规范化后的工作目录内。工作目录
// 本身可以是符号链接；所有比较都使用解析后的真实路径。
type workspaceScope struct {
	root string
}

// multiPathScope 用于默认模式：相对路径仍以启动工作目录为基准，但判断
// 访问范围时同时检查工作目录、项目路径白名单和可选的会话路径授权。
// resolve 不因路径在范围外而失败，由调用方决定审批还是拒绝。
type multiPathScope struct {
	workDir string
	rules   *ProjectRules
	session *SessionGrants
}

func newMultiPathScope(workDir string, rules *ProjectRules) (*multiPathScope, error) {
	resolved, err := canonicalDirectory(workDir)
	if err != nil {
		return nil, fmt.Errorf("解析工作目录失败: %w", err)
	}
	return &multiPathScope{workDir: resolved, rules: rules}, nil
}

func (s *multiPathScope) resolve(path string) (string, error) {
	return s.resolveFrom(s.workDir, path)
}

func (s *multiPathScope) resolveFrom(base, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("路径不能为空")
	}
	if strings.IndexByte(path, 0) >= 0 {
		return "", errors.New("路径包含无效字符")
	}
	if strings.TrimSpace(base) == "" {
		return "", errors.New("路径基准不能为空")
	}
	// 不能先 TrimSpace 或 filepath.Clean：真实的目录/文件名可以以空格
	// 结尾，而符号链接之后的 ".." 必须按内核语义解析。
	return resolvePathFromBase(base, path)
}

func (s *multiPathScope) resolveExisting(path string) (string, error) {
	return s.resolveExistingFrom(s.workDir, path)
}

func (s *multiPathScope) resolveExistingFrom(base, path string) (string, error) {
	resolved, err := s.resolveFrom(base, path)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func (s *multiPathScope) contains(path string) bool {
	if s == nil {
		return false
	}
	if pathWithin(s.workDir, path) {
		return true
	}
	if s.rules != nil && s.rules.ContainsPath(path) {
		return true
	}
	return s.session != nil && s.session.ContainsPath(path)
}

// setSession 接入进程内会话路径授权。只在启动或授权库确定后调用；之后
// 授权库自身的读写由 SessionGrants 加锁。
func (s *multiPathScope) setSession(grants *SessionGrants) {
	if s != nil {
		s.session = grants
	}
}

func newWorkspaceScope(workDir string) (workspaceScope, error) {
	if strings.TrimSpace(workDir) == "" {
		return workspaceScope{}, errors.New("工作目录不能为空")
	}
	absolute, err := filepath.Abs(workDir)
	if err != nil {
		return workspaceScope{}, fmt.Errorf("解析工作目录失败: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return workspaceScope{}, fmt.Errorf("解析工作目录失败: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return workspaceScope{}, fmt.Errorf("读取工作目录失败: %w", err)
	}
	if !info.IsDir() {
		return workspaceScope{}, errors.New("工作目录不是目录")
	}
	return workspaceScope{root: resolved}, nil
}

// resolve 返回目标在当前文件系统中的规范绝对路径，但不要求目标存在。
// 不存在的尾部会拼接到已解析的最近祖先上，因此符号链接父目录不能把
// 尚未创建的文件带出工作目录。
func (s workspaceScope) resolve(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("路径不能为空")
	}
	if strings.IndexByte(path, 0) >= 0 {
		return "", errors.New("路径包含无效字符")
	}
	resolved, err := resolvePathFromBase(s.root, path)
	if err != nil {
		return "", err
	}
	if !s.contains(resolved) {
		return "", fmt.Errorf("%w: %s", errPathOutsideWorkspace, path)
	}
	return resolved, nil
}

// resolveExisting 要求目标存在；用于 ls/find/grep 这类查询命令。
func (s workspaceScope) resolveExisting(path string) (string, error) {
	resolved, err := s.resolve(path)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func (s workspaceScope) contains(path string) bool {
	relative, err := filepath.Rel(s.root, path)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
