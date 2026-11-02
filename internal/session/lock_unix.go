//go:build unix

package session

import (
	"errors"
	"os"
	"syscall"
)

// Lock 是会话日志上的排他锁。它阻止两个 Johnny 进程同时恢复并追加同一份记录。
type Lock struct {
	file *os.File
}

func lockExclusive(path string) (*Lock, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, ErrSessionInUse
		}
		return nil, err
	}
	return &Lock{file: file}, nil
}

// Close 释放锁。重复调用是安全的。
func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	return errors.Join(unlockErr, closeErr)
}

// sessionLocked 只探测是否有其他进程持有排他锁，不保留锁。
func sessionLocked(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return err == syscall.EWOULDBLOCK || err == syscall.EAGAIN
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return false
}
