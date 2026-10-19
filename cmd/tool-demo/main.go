// Command tool-demo 是 v0.4 工具层的独立验证入口。
// 它不调用模型，也不属于 johnny 主命令；用于手动执行 bash、read、edit、write。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
	"github.com/Johnny-Cheung/Johnny-Code/internal/tool"
)

func main() {
	if len(os.Args) == 1 {
		runSample()
		return
	}
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, `用法：
  go run ./cmd/tool-demo                         运行完整示例（临时目录）
  go run ./cmd/tool-demo <工具名> '<JSON 参数>'  在当前目录执行单个工具，例如：
  go run ./cmd/tool-demo bash '{"command":"pwd"}'
  go run ./cmd/tool-demo write '{"path":"demo.txt","content":"hello"}'
  go run ./cmd/tool-demo read '{"path":"demo.txt"}'
  go run ./cmd/tool-demo edit '{"path":"demo.txt","old_string":"hello","new_string":"world"}'`)
		os.Exit(2)
	}
	work, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	registry, err := openRegistry(work)
	if err != nil {
		fail(err)
	}
	result, execErr := registry.Execute(context.Background(), model.ToolCall{
		ID:        "manual-1",
		Name:      os.Args[1],
		Arguments: json.RawMessage(os.Args[2]),
	})
	printResult("手动执行 "+os.Args[1], result, execErr)
	if execErr != nil {
		os.Exit(1)
	}
}

func runSample() {
	work, err := os.MkdirTemp("", "johnny-tool-demo-")
	if err != nil {
		fail(err)
	}
	defer os.RemoveAll(work)
	fmt.Printf("示例工作目录：%s\n（演示结束后自动删除）\n", work)
	registry, err := openRegistry(work)
	if err != nil {
		fail(err)
	}
	for _, definition := range registry.Definitions() {
		fmt.Printf("已注册工具：%-6s %s\n", definition.Name, definition.Description)
	}
	steps := []struct {
		label string
		name  string
		args  map[string]any
	}{
		{"bash：准备目录", "bash", map[string]any{"command": "mkdir -p src && echo 目录已创建"}},
		{"write：创建文件", "write", map[string]any{"path": "src/main.go", "content": "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"}},
		{"read：读取文件", "read", map[string]any{"path": "src/main.go"}},
		{"edit：精确替换", "edit", map[string]any{"path": "src/main.go", "old_string": "println", "new_string": "fmt.Println"}},
		{"read：读取指定行", "read", map[string]any{"path": "src/main.go", "offset": 3, "limit": 3}},
		{"bash：搜索", "bash", map[string]any{"command": "grep -n func src/main.go"}},
		{"edit：匹配失败", "edit", map[string]any{"path": "src/main.go", "old_string": "不存在的文本", "new_string": "x"}},
		{"bash：非零退出", "bash", map[string]any{"command": "echo 出错了 >&2; exit 3"}},
		{"read：文件不存在", "read", map[string]any{"path": "src/missing.go"}},
	}
	for i, step := range steps {
		args, err := json.Marshal(step.args)
		if err != nil {
			fail(err)
		}
		result, execErr := registry.Execute(context.Background(), model.ToolCall{
			ID:        fmt.Sprintf("sample-%d", i+1),
			Name:      step.name,
			Arguments: args,
		})
		printResult(step.label, result, execErr)
	}
}

func openRegistry(work string) (*tool.Registry, error) {
	runtime, err := tool.NewRuntime(work)
	if err != nil {
		return nil, err
	}
	return tool.NewDefaultRegistry(runtime)
}

func printResult(label string, result tool.Result, err error) {
	converted := tool.ToModelResult("call", result, err)
	fmt.Printf("\n=== %s ===\nIsError：%v\n", label, converted.IsError)
	if converted.Content == "" {
		fmt.Println("输出：（空）")
	} else {
		for _, line := range strings.Split(strings.TrimRight(converted.Content, "\n"), "\n") {
			fmt.Printf("  %s\n", line)
		}
	}
	if err != nil {
		fmt.Printf("错误：%v\n", err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "执行失败：", err)
	os.Exit(1)
}
