package tool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxSymlinkExpansions 限制单次解析中展开的符号链接数量，避免链接环
// 无限循环。内核同样会在达到上限后返回 ELOOP。
const maxSymlinkExpansions = 64

// resolveExistingAncestors 把绝对路径解析为当前文件系统中的真实目标。
//
// 它与 filepath.EvalSymlinks(filepath.Clean(path)) 的关键区别是严格按内核
// 的解析顺序处理路径：遇到符号链接先展开，再继续应用后续的 ".."。因此
// 当 /repo/wl 指向 /outside/002 时，/repo/wl/../002/file 会解析到
// /outside/002/file，而不是词法清理后的 /repo/002/file。
//
// 目标尾部可以不存在：不存在的组件会保留在已解析的祖先之后，供尚未创建
// 的文件使用；即使尾部缺失，前面已经存在的符号链接和 ".." 仍按真实语义
// 展开。target 必须是绝对路径，调用方不得预先 filepath.Clean 或
// TrimSpace：以空格结尾的目录名是合法路径，预先清理会指向另一个目标。
func resolveExistingAncestors(target string) (string, error) {
	if strings.IndexByte(target, 0) >= 0 {
		return "", errors.New("路径包含无效字符")
	}
	if !filepath.IsAbs(target) {
		return "", errors.New("路径必须是绝对路径")
	}
	volume := filepath.VolumeName(target)
	resolved := volume + string(filepath.Separator)
	pending := splitPathComponents(strings.TrimPrefix(target, volume))
	expansions := 0
	for len(pending) > 0 {
		component := pending[0]
		pending = pending[1:]
		switch component {
		case "", ".":
			continue
		case "..":
			// 对已经展开的真实目录应用 ".."，与内核的父目录语义一致。
			resolved = filepath.Dir(resolved)
			continue
		}
		candidate := joinPathComponent(resolved, component)
		info, err := os.Lstat(candidate)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// 不存在的组件无法继续展开，但保留其位置后继续处理后缀：
				// 后面的 ".." 仍应作用于已解析的祖先，后面已经存在的
				// 组件也仍可继续解析。
				resolved = candidate
				continue
			}
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = candidate
			continue
		}
		expansions++
		if expansions > maxSymlinkExpansions {
			return "", fmt.Errorf("符号链接层级过深：%s", candidate)
		}
		link, err := os.Readlink(candidate)
		if err != nil {
			return "", err
		}
		if link == "" {
			return "", fmt.Errorf("符号链接目标为空：%s", candidate)
		}
		if filepath.IsAbs(link) {
			linkVolume := filepath.VolumeName(link)
			resolved = linkVolume + string(filepath.Separator)
			link = strings.TrimPrefix(link, linkVolume)
		}
		// 相对链接以链接所在目录为基准，即当前 resolved。
		pending = append(splitPathComponents(link), pending...)
	}
	return filepath.Clean(resolved), nil
}

// resolvePathFromBase 解析相对于 base 的 path，或直接解析绝对 path。
// 相对拼接只加一个分隔符，不做 filepath.Join/Clean，因此符号链接之后的
// ".." 仍由 resolveExistingAncestors 按内核语义处理。
func resolvePathFromBase(base, path string) (string, error) {
	if path == "" {
		return "", errors.New("路径不能为空")
	}
	if filepath.IsAbs(path) {
		return resolveExistingAncestors(path)
	}
	if base == "" {
		return "", errors.New("路径基准不能为空")
	}
	if !filepath.IsAbs(base) {
		return "", errors.New("路径基准必须是绝对路径")
	}
	return resolveExistingAncestors(joinPathComponent(base, path))
}

func splitPathComponents(path string) []string {
	return strings.Split(path, string(filepath.Separator))
}

func joinPathComponent(dir, component string) string {
	if strings.HasSuffix(dir, string(filepath.Separator)) {
		return dir + component
	}
	return dir + string(filepath.Separator) + component
}
