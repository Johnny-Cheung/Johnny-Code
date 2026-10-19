package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

type Write struct {
	runtime Runtime
}

type writeArguments struct {
	Path    *string `json:"path"`
	Content *string `json:"content"`
}

func NewWrite(runtime Runtime) *Write {
	return &Write{runtime: normalizeRuntime(runtime)}
}

func (w *Write) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name:        "write",
		Description: "创建文件或整体写入已有文件内容。",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"文件路径"},"content":{"type":"string","description":"要写入的完整文本内容"}},"required":["path","content"]}`),
	}
}

func (w *Write) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var args writeArguments
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, invalidArguments("write 参数格式无效", err)
	}
	if args.Path == nil || strings.TrimSpace(*args.Path) == "" {
		return Result{}, invalidArguments("write 缺少 path 参数", nil)
	}
	if args.Content == nil {
		return Result{}, invalidArguments("write 缺少 content 参数", nil)
	}
	path, err := resolvePath(w.runtime, *args.Path)
	if err != nil {
		return Result{}, invalidArguments("write 路径无效", err)
	}
	mode := os.FileMode(0644)
	if info, statErr := os.Stat(path); statErr == nil {
		if !info.Mode().IsRegular() {
			return Result{}, executionError("write 目标不是普通文件", nil)
		}
		mode = info.Mode()
	} else if !os.IsNotExist(statErr) {
		return Result{}, executionError("write 无法检查目标文件", statErr)
	} else {
		parent := filepath.Dir(path)
		if info, parentErr := os.Stat(parent); parentErr != nil || !info.IsDir() {
			if parentErr == nil {
				parentErr = fmt.Errorf("父路径不是目录")
			}
			return Result{}, executionError("write 父目录不存在，请先使用 bash 创建目录", parentErr)
		}
	}
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	target, err := resolveWriteTarget(path)
	if err != nil {
		return Result{}, executionError("write 无法解析目标路径", err)
	}
	if err := atomicWriteFile(target, []byte(*args.Content), mode); err != nil {
		return Result{}, executionError("write 写入文件失败", err)
	}
	return Result{Content: fmt.Sprintf("已写入 %s（%d 字节）", *args.Path, len(*args.Content))}, nil
}
