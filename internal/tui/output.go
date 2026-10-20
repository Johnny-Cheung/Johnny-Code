package tui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// safeText 保留可读的换行，将外部控制字符显示为文本；分片 ESC 也不能变成终端指令。
func safeText(text string) string {
	var out strings.Builder
	for _, r := range text {
		switch r {
		case '\n':
			out.WriteRune(r)
		case '\t':
			out.WriteString("    ")
		default:
			if unicode.IsControl(r) {
				fmt.Fprintf(&out, "\\u%04x", r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	return out.String()
}

func wrap(text string, width int) string {
	return ansi.Hardwrap(text, max(1, width), true)
}

// Bubble Tea 的 renderer 不返回写错误；包装输出以捕获首次失败并取消会话。
type checkedOutput struct {
	io.Writer
	mu     sync.Mutex
	err    error
	cancel func()
}

func (w *checkedOutput) Fd() uintptr {
	if file, ok := w.Writer.(interface{ Fd() uintptr }); ok {
		return file.Fd()
	}
	return ^uintptr(0)
}

// term.File 同时要求读、写和关闭方法；保留完整接口才能让库识别窗口尺寸。
func (w *checkedOutput) Read(data []byte) (int, error) {
	if reader, ok := w.Writer.(io.Reader); ok {
		return reader.Read(data)
	}
	return 0, io.ErrClosedPipe
}

func (w *checkedOutput) Close() error {
	if closer, ok := w.Writer.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (w *checkedOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.Writer.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = fmt.Errorf("输出终端界面失败：%w", err)
		w.cancel()
		return 0, w.err
	}
	return n, nil
}

func (w *checkedOutput) failure() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Bubble Tea 将输入 EOF 当成读线程结束，默认仍保留 UI；终端断开时需同时
// 停止会话，避免界面已无法接收取消键而工具仍在后台运行。
type checkedReader struct {
	io.Reader
	stop func(error)
}

func watchInput(reader io.Reader, stop func(error)) io.Reader {
	watched := &checkedReader{Reader: reader, stop: stop}
	if _, ok := reader.(interface {
		io.ReadWriteCloser
		Fd() uintptr
	}); ok {
		return &checkedInput{checkedReader: watched}
	}
	// 非文件输入不能暴露虚假 Fd，否则 Linux cancelreader 会对 -1 注册 epoll。
	return watched
}

type checkedInput struct{ *checkedReader }

func (r *checkedInput) Fd() uintptr {
	if file, ok := r.Reader.(interface{ Fd() uintptr }); ok {
		return file.Fd()
	}
	return ^uintptr(0)
}

func (r *checkedReader) Read(data []byte) (int, error) {
	n, err := r.Reader.Read(data)
	if err != nil {
		r.stop(err)
	}
	return n, err
}

// 保留 cancelreader.File 接口，让库可以通过文件描述符中断阻塞读取。
func (r *checkedInput) Write(data []byte) (int, error) {
	if writer, ok := r.Reader.(io.Writer); ok {
		return writer.Write(data)
	}
	return 0, io.ErrClosedPipe
}

func (r *checkedInput) Close() error {
	if closer, ok := r.Reader.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (r *checkedInput) Name() string {
	if file, ok := r.Reader.(interface{ Name() string }); ok {
		return file.Name()
	}
	return "johnny-input"
}
