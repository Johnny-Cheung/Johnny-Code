//go:build !unix

package session

import "os"

// Lock 在不支持 flock 的平台上是一个空操作。当前支持的目标平台是 macOS
// 和 Linux；Windows 尚未验证，恢复时不加锁。
type Lock struct{}

func lockExclusive(path string) (*Lock, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return &Lock{}, nil
}

func (l *Lock) Close() error { return nil }

func sessionLocked(path string) bool { return false }
