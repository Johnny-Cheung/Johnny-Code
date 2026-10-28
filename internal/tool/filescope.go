package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

// FileClaims 登记写入类子任务声明负责的路径。它是防误写的护栏：只约束
// write/edit，bash 不受限，因此不是安全边界。
type FileClaims struct {
	mu     sync.Mutex
	claims map[string]fileClaim
}

type fileClaim struct {
	label string
	paths []string
}

func NewFileClaims() *FileClaims {
	return &FileClaims{claims: make(map[string]fileClaim)}
}

// Claim 为 owner 登记规范绝对路径。与其他持有者的范围重叠时失败并说明冲突对象。
func (c *FileClaims) Claim(owner, label string, paths []string) error {
	if c == nil {
		return errors.New("文件范围登记簿不能为空")
	}
	if owner == "" || len(paths) == 0 {
		return errors.New("文件范围需要持有者和至少一个路径")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for other, existing := range c.claims {
		if other == owner {
			continue
		}
		for _, mine := range paths {
			for _, theirs := range existing.paths {
				if pathWithin(theirs, mine) || pathWithin(mine, theirs) {
					return fmt.Errorf("路径 %s 与子任务 %s（%s）负责的 %s 重叠，请等它结束或改用其他路径", mine, other, existing.label, theirs)
				}
			}
		}
	}
	c.claims[owner] = fileClaim{label: label, paths: append([]string(nil), paths...)}
	return nil
}

// OverlappingPaths 返回两组路径中第一对重叠的路径：相同、互相包含都算重叠。
// 批量启动任务前用它预检，好在启动任何任务之前给出明确的错误。
func OverlappingPaths(a, b []string) (string, string, bool) {
	for _, mine := range a {
		for _, theirs := range b {
			if pathWithin(theirs, mine) || pathWithin(mine, theirs) {
				return mine, theirs, true
			}
		}
	}
	return "", "", false
}

// Release 释放 owner 的全部范围。重复释放无副作用。
func (c *FileClaims) Release(owner string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.claims, owner)
	c.mu.Unlock()
}

// Owners 返回当前持有范围的子任务 ID，按字典序排列。
func (c *FileClaims) Owners() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	owners := make([]string, 0, len(c.claims))
	for owner := range c.claims {
		owners = append(owners, owner)
	}
	sort.Strings(owners)
	return owners
}

// check 判断 owner 能否写入 path。owner 为空表示主 Agent：不能写任何子任务的范围；
// 非空表示子任务：只能写自己的范围。
func (c *FileClaims) check(owner, path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if owner == "" {
		for other, existing := range c.claims {
			for _, claimed := range existing.paths {
				if pathWithin(claimed, path) {
					return fmt.Errorf("路径 %s 正由子任务 %s（%s）负责写入，请等它完成后再修改，或改用其他路径", path, other, existing.label)
				}
			}
		}
		return nil
	}
	mine, ok := c.claims[owner]
	if !ok {
		return fmt.Errorf("子任务没有声明写入范围，拒绝写入 %s", path)
	}
	for _, claimed := range mine.paths {
		if pathWithin(claimed, path) {
			return nil
		}
	}
	return fmt.Errorf("子任务只能修改声明的范围 %s，拒绝写入 %s", strings.Join(mine.paths, "、"), path)
}

// Executor 是 ScopeGuard 包装的工具集合，与 agent.Tools 的方法集一致。
type Executor interface {
	Definitions() []model.ToolDefinition
	Execute(context.Context, model.ToolCall) (Result, error)
}

// ScopeGuard 在 write/edit 前检查 FileClaims。它必须是最外层包装，
// 这样范围外的写入在审批前就被拒绝。
type ScopeGuard struct {
	inner   Executor
	claims  *FileClaims
	owner   string
	workDir string
}

func NewScopeGuard(inner Executor, claims *FileClaims, owner, workDir string) (*ScopeGuard, error) {
	if inner == nil || claims == nil {
		return nil, errors.New("范围守卫需要工具集合和登记簿")
	}
	absolute, err := filepath.Abs(strings.TrimSpace(workDir))
	if err != nil || strings.TrimSpace(workDir) == "" {
		return nil, errors.New("范围守卫需要工作目录")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("解析工作目录失败: %w", err)
	}
	return &ScopeGuard{inner: inner, claims: claims, owner: owner, workDir: resolved}, nil
}

func (g *ScopeGuard) Definitions() []model.ToolDefinition { return g.inner.Definitions() }

func (g *ScopeGuard) Execute(ctx context.Context, call model.ToolCall) (Result, error) {
	if call.Name != "write" && call.Name != "edit" {
		return g.inner.Execute(ctx, call)
	}
	arguments := call.Arguments
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	path, _, err := fileArgument(call.Name, arguments)
	if err != nil || strings.TrimSpace(path) == "" {
		return g.inner.Execute(ctx, call)
	}
	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(g.workDir, target)
	}
	resolved, err := resolveExistingAncestors(filepath.Clean(target))
	if err != nil {
		return g.inner.Execute(ctx, call)
	}
	if err := g.claims.check(g.owner, resolved); err != nil {
		return Result{Content: err.Error(), IsError: true}, permissionDenied(err.Error(), nil)
	}
	return g.inner.Execute(ctx, call)
}
