package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

type Read struct {
	runtime Runtime
}

type readArguments struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset"`
	Limit  *int   `json:"limit"`
}

func NewRead(runtime Runtime) *Read {
	return &Read{runtime: normalizeRuntime(runtime)}
}

func (r *Read) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name:        "read",
		Description: "读取文本文件内容并返回带行号的结果。",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"文件路径"},"offset":{"type":"integer","minimum":1,"description":"起始行号，从 1 开始"},"limit":{"type":"integer","minimum":1,"description":"最多读取的行数"}},"required":["path"]}`),
	}
}

func (r *Read) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var args readArguments
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, invalidArguments("read 参数格式无效", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return Result{}, invalidArguments("read 缺少 path 参数", nil)
	}
	offset := 1
	if args.Offset != nil {
		if *args.Offset < 1 {
			return Result{}, invalidArguments("read 的 offset 必须大于等于 1", nil)
		}
		offset = *args.Offset
	}
	limit := 0
	if args.Limit != nil {
		if *args.Limit < 1 {
			return Result{}, invalidArguments("read 的 limit 必须大于等于 1", nil)
		}
		limit = *args.Limit
	}
	path, err := resolvePath(r.runtime, args.Path)
	if err != nil {
		return Result{}, invalidArguments("read 路径无效", err)
	}
	// 先确认是普通文件再打开，避免打开 FIFO 等特殊文件时阻塞。
	if _, err := regularFile(path); err != nil {
		return Result{}, executionError(readFileErrorMessage(err), err)
	}
	file, err := os.Open(path)
	if err != nil {
		return Result{}, executionError(readFileErrorMessage(err), err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	output := &limitedWriter{limit: r.runtime.MaxOutputBytes}
	lineNumber := 0
	returned := 0
	for {
		if err := contextError(ctx); err != nil {
			return Result{}, err
		}
		line, truncated, readErr := readBoundedLine(reader, r.runtime.MaxOutputBytes)
		if truncated {
			line = trimIncompleteRune(line)
		}
		if len(line) > 0 {
			lineNumber++
			if !utf8.ValidString(line) {
				return Result{}, executionError("read 目标不是有效的 UTF-8 文本", nil)
			}
			if lineNumber >= offset {
				formatted := fmt.Sprintf("%6d\t%s", lineNumber, line)
				if _, err := output.Write([]byte(formatted)); err != nil {
					return Result{}, executionError("read 输出失败", err)
				}
				returned++
				if limit > 0 && returned >= limit {
					break
				}
				if output.truncated {
					break
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return Result{}, executionError("read 读取文件失败", readErr)
		}
	}
	if lineNumber < offset && !(lineNumber == 0 && offset == 1) {
		return Result{}, executionError("read 起始行超出文件末尾", nil)
	}
	return Result{Content: output.String()}, nil
}

// readBoundedLine 读取一行，最多保留 limit 字节；超出部分被丢弃并标记截断。
// 丢弃时仍继续读取到行尾，保证行号与 offset 语义不受影响。
func readBoundedLine(reader *bufio.Reader, limit int) (string, bool, error) {
	var builder strings.Builder
	truncated := false
	for {
		chunk, err := reader.ReadSlice('\n')
		if !truncated {
			remaining := limit - builder.Len()
			if len(chunk) <= remaining {
				builder.Write(chunk)
			} else {
				builder.Write(chunk[:remaining])
				truncated = true
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return builder.String(), truncated, err
	}
}

// trimIncompleteRune 丢弃截断后可能残缺的末尾字节，避免把完整 UTF-8 文本误判为二进制。
func trimIncompleteRune(s string) string {
	for i := 0; i < utf8.UTFMax && len(s) > 0; i++ {
		if utf8.ValidString(s) {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

func readFileErrorMessage(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "read 文件不存在"
	}
	if errors.Is(err, errNotRegularFile) {
		return "read 目标不是普通文件"
	}
	return "read 无法打开文件"
}
