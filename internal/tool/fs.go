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
	if _, err := io.Copy(file, bytes.NewReader(data)); err != nil {
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
