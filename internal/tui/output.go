package tui

import (
	"fmt"
	"io"
	"strconv"
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

// Bubble Tea 的 renderer 不返回写错误。用只在内部生成的零宽标记关联一次
// 实际终端写入和同步观察者，工具开始行写入成功之后才允许工具执行。
// 标记在写入底层终端前移除，不会成为用户输出或终端指令。
const fencePrefix = "\x1b]777;johnny-fence="
const fenceEnd = "\x07"

type checkedOutput struct {
	io.Writer
	mu      sync.Mutex
	next    uint64
	pending map[uint64]chan error
	err     error
	cancel  func()
}

func (w *checkedOutput) Fd() uintptr {
	if file, ok := w.Writer.(interface{ Fd() uintptr }); ok {
		return file.Fd()
	}
	return ^uintptr(0)
}

func (w *checkedOutput) fence() (string, <-chan error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.next++
	ack := make(chan error, 1)
	if w.err != nil {
		ack <- w.err
		return "", ack
	}
	if w.pending == nil {
		w.pending = make(map[uint64]chan error)
	}
	w.pending[w.next] = ack
	return fencePrefix + strconv.FormatUint(w.next, 10) + fenceEnd, ack
}

func (w *checkedOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	text := string(data)
	var ids []uint64
	for {
		start := strings.Index(text, fencePrefix)
		if start < 0 {
			break
		}
		end := strings.Index(text[start:], fenceEnd)
		if end < 0 {
			break
		}
		end += start
		id, err := strconv.ParseUint(text[start+len(fencePrefix):end], 10, 64)
		if err == nil {
			ids = append(ids, id)
		}
		text = text[:start] + text[end+len(fenceEnd):]
	}
	n, err := io.WriteString(w.Writer, text)
	if err == nil && n != len(text) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = fmt.Errorf("输出终端界面失败：%w", err)
		for id, ack := range w.pending {
			ack <- w.err
			delete(w.pending, id)
		}
		w.cancel()
		return 0, w.err
	}
	for _, id := range ids {
		if ack, ok := w.pending[id]; ok {
			ack <- nil
			delete(w.pending, id)
		}
	}
	return len(data), nil
}

func (w *checkedOutput) failure() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}
