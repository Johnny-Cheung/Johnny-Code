package tool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

type echoTool struct {
	definition model.ToolDefinition
	called     bool
}

func (e *echoTool) Definition() model.ToolDefinition { return e.definition }

func (e *echoTool) Execute(_ context.Context, args json.RawMessage) (Result, error) {
	e.called = true
	return Result{Content: string(args)}, nil
}

func validDefinition(name string) model.ToolDefinition {
	return model.ToolDefinition{Name: name, Description: "测试工具", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func TestDefaultRegistryDefinitionsAreStable(t *testing.T) {
	registry, err := NewDefaultRegistry(testRuntime(t))
	if err != nil {
		t.Fatal(err)
	}
	definitions := registry.Definitions()
	if len(definitions) != 4 {
		t.Fatalf("默认工具数量错误：%d", len(definitions))
	}
	for i, want := range []string{"bash", "edit", "read", "write"} {
		if definitions[i].Name != want {
			t.Fatalf("工具排序错误：%v", definitions)
		}
	}
	definitions[0].Name = "changed"
	if registry.Definitions()[0].Name == "changed" {
		t.Fatal("Definitions 应返回副本")
	}
	if _, ok := registry.Get("read"); !ok {
		t.Fatal("已注册工具不可查找")
	}
	if _, ok := registry.Get("missing"); ok {
		t.Fatal("未注册工具不应查找到")
	}
}

func TestRegistryRejectsInvalidDefinitions(t *testing.T) {
	cases := []struct {
		name       string
		definition model.ToolDefinition
	}{
		{"空名称", validDefinition("")},
		{"大写名称", validDefinition("Read")},
		{"空描述", model.ToolDefinition{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		{"数组 schema", model.ToolDefinition{Name: "read", Description: "测试", InputSchema: json.RawMessage(`[]`)}},
		{"空 schema", model.ToolDefinition{Name: "read", Description: "测试", InputSchema: json.RawMessage(`null`)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewRegistry(&echoTool{definition: tc.definition}); err == nil {
				t.Fatal("非法定义应被拒绝")
			}
		})
	}
	first := &echoTool{definition: validDefinition("read")}
	second := &echoTool{definition: validDefinition("read")}
	if _, err := NewRegistry(first, second); err == nil {
		t.Fatal("重复名称应被拒绝")
	}
}

func TestRegistryExecuteValidatesAndDispatches(t *testing.T) {
	echo := &echoTool{definition: validDefinition("echo")}
	registry, err := NewRegistry(echo)
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Execute(context.Background(), model.ToolCall{Name: "echo"})
	if err != nil || result.Content != `{}` || !echo.called {
		t.Fatalf("空参数没有按对象执行：%+v %v", result, err)
	}
	if _, err := registry.Execute(context.Background(), model.ToolCall{Name: "missing", Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("未知工具应失败")
	} else {
		var toolErr *Error
		if !errors.As(err, &toolErr) || toolErr.Kind != KindInvalidArguments {
			t.Fatalf("未知工具错误分类错误：%v", err)
		}
	}
	if _, err := registry.Execute(context.Background(), model.ToolCall{Name: "echo", Arguments: json.RawMessage(`[]`)}); err == nil {
		t.Fatal("数组参数应被拒绝")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := registry.Execute(ctx, model.ToolCall{Name: "echo", Arguments: json.RawMessage(`{}`)}); !errors.Is(err, context.Canceled) {
		t.Fatalf("预取消没有直接返回：%v", err)
	}
}
