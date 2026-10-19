package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

type Edit struct {
	runtime Runtime
}

type editArguments struct {
	Path       *string `json:"path"`
	OldString  *string `json:"old_string"`
	NewString  *string `json:"new_string"`
	ReplaceAll *bool   `json:"replace_all"`
}

func NewEdit(runtime Runtime) *Edit {
	return &Edit{runtime: normalizeRuntime(runtime)}
}

func (e *Edit) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name:        "edit",
		Description: "对已有文件进行精确文本替换。",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"文件路径"},"old_string":{"type":"string","description":"要匹配的原文本"},"new_string":{"type":"string","description":"替换后的文本"},"replace_all":{"type":"boolean","description":"是否替换所有匹配项，默认只允许唯一匹配"}},"required":["path","old_string","new_string"]}`),
	}
}

func (e *Edit) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var args editArguments
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, invalidArguments("edit 参数格式无效", err)
	}
	if args.Path == nil || strings.TrimSpace(*args.Path) == "" {
		return Result{}, invalidArguments("edit 缺少 path 参数", nil)
	}
	if args.OldString == nil || *args.OldString == "" {
		return Result{}, invalidArguments("edit 的 old_string 不能为空", nil)
	}
	if args.NewString == nil {
		return Result{}, invalidArguments("edit 缺少 new_string 参数", nil)
	}
	if *args.OldString == *args.NewString {
		return Result{}, invalidArguments("edit 的新旧文本不能相同", nil)
	}
	path, err := resolvePath(e.runtime, *args.Path)
	if err != nil {
		return Result{}, invalidArguments("edit 路径无效", err)
	}
	info, err := regularFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{}, executionError("edit 文件不存在，创建新文件请使用 write", err)
		}
		if errors.Is(err, errNotRegularFile) {
			return Result{}, executionError("edit 目标不是普通文件", err)
		}
		return Result{}, executionError("edit 无法打开目标文件", err)
	}
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return Result{}, executionError("edit 读取目标文件失败", err)
	}
	count := strings.Count(string(content), *args.OldString)
	if count == 0 {
		return Result{}, executionError("edit 未找到匹配文本", nil)
	}
	replaceAll := args.ReplaceAll != nil && *args.ReplaceAll
	if count > 1 && !replaceAll {
		return Result{}, executionError(fmt.Sprintf("edit 匹配到 %d 处，请补充上下文或使用 replace_all", count), nil)
	}
	updated := strings.Replace(string(content), *args.OldString, *args.NewString, 1)
	if replaceAll {
		updated = strings.ReplaceAll(string(content), *args.OldString, *args.NewString)
	}
	if err := contextError(ctx); err != nil {
		return Result{}, err
	}
	target, err := resolveWriteTarget(path)
	if err != nil {
		return Result{}, executionError("edit 无法解析目标文件", err)
	}
	if err := atomicWriteFile(target, []byte(updated), info.Mode()); err != nil {
		return Result{}, executionError("edit 写入目标文件失败", err)
	}
	return Result{Content: fmt.Sprintf("已更新 %s（替换 %d 处）", *args.Path, count)}, nil
}
