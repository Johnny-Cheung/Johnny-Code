package tool

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type limitedWriter struct {
	limit     int
	buffer    bytes.Buffer
	truncated bool
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	if w.limit <= 0 {
		return len(data), nil
	}
	remaining := w.limit - w.buffer.Len()
	if remaining > 0 {
		if len(data) <= remaining {
			_, _ = w.buffer.Write(data)
		} else {
			_, _ = w.buffer.Write(data[:remaining])
			w.truncated = true
		}
	} else if len(data) > 0 {
		w.truncated = true
	}
	return len(data), nil
}

func (w *limitedWriter) String() string {
	content := w.buffer.String()
	if w.truncated {
		content += "\n[输出已截断：超过限制，已省略后续内容]"
	}
	return content
}

func resolvePath(runtime Runtime, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("路径不能为空")
	}
	if strings.IndexByte(path, 0) >= 0 {
		return "", errors.New("路径包含无效字符")
	}
	// 与权限分类使用同一套真实路径解析：先展开符号链接再应用 ".."，
	// 不预先词法清理。这样分类看到的路径就是 read/write/edit 实际
	// 打开的目标，以空格结尾的文件名也不会被误当成同名兄弟。
	if filepath.IsAbs(path) {
		return resolveExistingAncestors(path)
	}
	if runtime.WorkDir == "" {
		return "", errors.New("工作目录不能为空")
	}
	return resolvePathFromBase(runtime.WorkDir, path)
}

var errNotRegularFile = errors.New("目标不是普通文件")

func regularFile(path string) (fs.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return info, errNotRegularFile
	}
	return info, nil
}

// resolveWriteTarget 在写入前解析符号链接：目标是链接时写入其指向的真实文件，
// 保持链接本身不被替换。目标不存在时按原路径处理（用于新建文件）。
func resolveWriteTarget(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return path, nil
		}
		return "", err
	}
	return resolved, nil
}

func atomicWriteFile(path string, data []byte, mode fs.FileMode) (err error) {
	return atomicWrite(path, mode, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// atomicAppendFile 把 data 追加到 path 的副本，再用 rename 替换目标。
// 它不使用 O_APPEND 直接写原 inode：工作区内的硬链接可能指向外部文件，
// 就地追加会绕过路径检查；临时文件 + rename 只替换工作区的目录项。
// 同时，追加中途失败时原文件保持不变，不会留下半段内容。
func atomicAppendFile(path string, data []byte, mode fs.FileMode) (err error) {
	existing, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return atomicWriteFile(path, data, mode)
	}
	if err != nil {
		return err
	}
	defer func() { _ = existing.Close() }()
	return atomicWrite(path, mode, func(w io.Writer) error {
		if _, err := io.Copy(w, existing); err != nil {
			return err
		}
		_, err := w.Write(data)
		return err
	})
}

func atomicWrite(path string, mode fs.FileMode, fill func(io.Writer) error) (err error) {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".johnny-tmp-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(mode.Perm()); err != nil {
		_ = file.Close()
		return err
	}
	if err := fill(file); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	committed = true
	return nil
}
