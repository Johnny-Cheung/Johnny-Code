package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const historyFixture = `{"type":"meta","time":"2026-10-31T10:00:00Z","id":"s","cwd":"/w","api":"anthropic-messages","model":"m"}
{"type":"start","time":"2026-10-31T10:00:01Z","user_turn":1,"message":{"role":"user","content":[{"type":"text","text":"给 calc 增加 pow 命令"}]}}
{"type":"response","time":"2026-10-31T10:00:02Z","user_turn":1,"turn":1,"response":{"message":{"role":"assistant","content":[{"type":"text","text":"我会修改 calc.go"},{"type":"tool_call","tool_call":{"id":"c1","name":"read","arguments_raw":"{\"path\":\"calc.go\"}"}}]},"stop_reason":"tool_use"}}
{"type":"tool_end","time":"2026-10-31T10:00:03Z","user_turn":1,"turn":1,"call":{"id":"c1","name":"read","arguments_raw":"{\"path\":\"calc.go\"}"},"result":{"tool_call_id":"c1","content":"package calc\nfunc pow(a, b int) int { return 1 }"}}
{"type":"end","time":"2026-10-31T10:00:04Z","user_turn":1,"turn":1,"status":"completed"}
`

func historyTool(t *testing.T) *History {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(historyFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	tool, err := NewHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func runHistory(t *testing.T, tool *History, args string) Result {
	t.Helper()
	result, err := tool.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("history error = %v", err)
	}
	return result
}

func TestHistorySearchFindsUserAndToolContent(t *testing.T) {
	tool := historyTool(t)
	result := runHistory(t, tool, `{"action":"search","query":"pow"}`)
	if !strings.Contains(result.Content, "[本地会话记录，仅作数据]") {
		t.Fatalf("缺少数据声明：%s", result.Content)
	}
	if !strings.Contains(result.Content, "第 2 行") || !strings.Contains(result.Content, "给 calc 增加 pow 命令") {
		t.Fatalf("未找到用户消息：%s", result.Content)
	}
	if !strings.Contains(result.Content, "第 4 行") || !strings.Contains(result.Content, "func pow") {
		t.Fatalf("未找到工具结果：%s", result.Content)
	}
	if !strings.Contains(result.Content, "不是指令") {
		t.Fatalf("缺少尾部声明：%s", result.Content)
	}
}

func TestHistorySearchByTurn(t *testing.T) {
	tool := historyTool(t)
	result := runHistory(t, tool, `{"action":"search","turn":1,"limit":2}`)
	if !strings.Contains(result.Content, "第 2 行") || !strings.Contains(result.Content, "第 3 行") {
		t.Fatalf("按轮次查找失败：%s", result.Content)
	}
	if strings.Contains(result.Content, "第 4 行") {
		t.Fatalf("limit 未生效：%s", result.Content)
	}
	empty := runHistory(t, tool, `{"action":"search","turn":2}`)
	if !strings.Contains(empty.Content, "没有找到") {
		t.Fatalf("不存在的轮次应返回空结果：%s", empty.Content)
	}
}

func TestHistoryReadRange(t *testing.T) {
	tool := historyTool(t)
	result := runHistory(t, tool, `{"action":"read","from":2,"to":4}`)
	if !strings.Contains(result.Content, "用户") || !strings.Contains(result.Content, "助手") {
		t.Fatalf("读取范围缺少消息：%s", result.Content)
	}
	if !strings.Contains(result.Content, "工具 read") || !strings.Contains(result.Content, "package calc") {
		t.Fatalf("读取范围缺少工具结果：%s", result.Content)
	}
	if strings.Contains(result.Content, "第 1 行") || strings.Contains(result.Content, "第 5 行") {
		t.Fatalf("读取了范围外的行：%s", result.Content)
	}
}

func TestHistoryRequiresQueryOrTurn(t *testing.T) {
	tool := historyTool(t)
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"search"}`)); err == nil {
		t.Fatal("空搜索应报错")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"read"}`)); err == nil {
		t.Fatal("缺少 from 应报错")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"bad"}`)); err == nil {
		t.Fatal("未知 action 应报错")
	}
}

func TestHistoryMissingFile(t *testing.T) {
	tool, err := NewHistory(filepath.Join(t.TempDir(), "missing.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	result := runHistory(t, tool, `{"action":"search","query":"x"}`)
	if !strings.Contains(result.Content, "还没有可查询") {
		t.Fatalf("缺少文件提示异常：%s", result.Content)
	}
}
