package contextview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

func userMessage(text string) model.Message {
	return model.Message{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.BlockText, Text: text}}}
}

func assistantMessage(text string) model.Message {
	return model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{Type: model.BlockText, Text: text}}}
}

func TestEstimateTextAndBudget(t *testing.T) {
	if got := EstimateText(""); got != 0 {
		t.Fatalf("空文本估算 = %d", got)
	}
	if got := EstimateText("abcdefgh"); got != 2 {
		t.Fatalf("ASCII 估算 = %d", got)
	}
	if got := EstimateText("中文"); got != 2 {
		t.Fatalf("中文估算 = %d", got)
	}
	budget := NewBudget(1_000_000, 4096)
	if budget.Safety != 50_000 || budget.Usable != 945_904 {
		t.Fatalf("预算计算错误：%+v", budget)
	}
	if budget.TurnTrigger() != 851_313 || budget.RequestTrigger() != 898_608 {
		t.Fatalf("触发线错误：%d %d", budget.TurnTrigger(), budget.RequestTrigger())
	}
}

func TestBashPreviewSavesArtifactAndKeepsHeadTail(t *testing.T) {
	dir := t.TempDir()
	store := NewArtifactStore(dir, "session-test")
	var lines []string
	for i := 0; i < 1200; i++ {
		lines = append(lines, strings.Repeat("x", 80))
	}
	lines[0] = "FIRST-LINE"
	lines[len(lines)-1] = "LAST-LINE"
	content := strings.Join(lines, "\n")
	preview := BuildPreview("bash", content, json.RawMessage(`{"command":"demo"}`), store, 2, 3, DefaultPreviewBytes)
	if !preview.Truncated || preview.Artifact == "" || preview.Warning != "" {
		t.Fatalf("预览或存档状态错误：%+v", preview)
	}
	if preview.Artifact != ".johnny/artifacts/session-test/turn-2/call-3-bash.log" {
		t.Fatalf("存档路径错误：%s", preview.Artifact)
	}
	if !strings.Contains(preview.Content, "FIRST-LINE") || !strings.Contains(preview.Content, "LAST-LINE") {
		t.Fatal("bash 预览应保留开头和结尾")
	}
	if !strings.Contains(preview.Content, "完整输出："+preview.Artifact) {
		t.Fatalf("预览缺少存档路径：%s", preview.Content[max(0, len(preview.Content)-500):])
	}
	path := filepath.Join(dir, filepath.FromSlash(preview.Artifact))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Fatal("存档内容不完整")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("存档文件权限 = %o", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("存档目录权限 = %o", dirInfo.Mode().Perm())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("关闭后应删除会话存档目录")
	}
}

func TestReadPreviewGivesNextOffset(t *testing.T) {
	var lines []string
	for i := 1; i <= 200; i++ {
		lines = append(lines, "line "+strings.Repeat("y", 20))
	}
	preview := BuildPreview("read", strings.Join(lines, "\n"), json.RawMessage(`{"path":"a.txt","offset":10}`), nil, 1, 1, 512)
	if !preview.Truncated || preview.NextOffset <= 10 {
		t.Fatalf("read 预览错误：%+v", preview)
	}
	if !strings.Contains(preview.Content, "offset="+strconv.Itoa(preview.NextOffset)) {
		t.Fatalf("read 预览缺少续读 offset：%s", preview.Content)
	}
}

func TestTurnCompactionOnlyCompactsLargeResults(t *testing.T) {
	manager := NewManager(Config{Budget: NewBudget(1_000_000, 4096)})
	manager.BeginTurn(1, userMessage("task"))
	manager.AppendAssistant(model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
		Type:     model.BlockToolCall,
		ToolCall: &model.ToolCall{ID: "call-1", Name: "bash", Arguments: json.RawMessage(`{"command":"demo"}`)},
	}}})
	result := model.ToolResult{ToolCallID: "call-1", Content: strings.Repeat("z", 40_000)}
	manager.AppendToolResult(1, model.ToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"demo"}`)}, result)
	info := manager.FinishTurn()
	if !info.Compacted || info.Saved <= 0 {
		t.Fatalf("大工具结果应轮级压缩：%+v", info)
	}
	messages := manager.Messages()
	if len(messages) != 3 {
		t.Fatalf("消息数量错误：%d", len(messages))
	}
	compacted := messages[2].Content[0].ToolResult
	if compacted == nil || compacted.ToolCallID != "call-1" || !strings.HasPrefix(compacted.Content, "[工具结果已压缩]") {
		t.Fatalf("压缩结果未保持配对：%+v", compacted)
	}
	if !strings.Contains(compacted.Content, "工具：bash") || !strings.Contains(compacted.Content, "原始大小：40000") {
		t.Fatalf("压缩记录信息不足：%s", compacted.Content)
	}

	small := NewManager(Config{Budget: NewBudget(1_000_000, 4096)})
	small.BeginTurn(1, userMessage("task"))
	small.AppendAssistant(model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
		Type:     model.BlockToolCall,
		ToolCall: &model.ToolCall{ID: "call-2", Name: "read", Arguments: json.RawMessage(`{"path":"a.txt"}`)},
	}}})
	small.AppendToolResult(1, model.ToolCall{Name: "read", Arguments: json.RawMessage(`{"path":"a.txt"}`)}, model.ToolResult{ToolCallID: "call-2", Content: "small"})
	if info := small.FinishTurn(); info.Compacted {
		t.Fatalf("小结果不应压缩：%+v", info)
	}
}

func TestTurnPreviewBudgetLimitsLaterResults(t *testing.T) {
	manager := NewManager(Config{Budget: NewBudget(1_000_000, 4096)})
	manager.BeginTurn(1, userMessage("task"))
	for i := 0; i < 6; i++ {
		manager.AppendToolResult(1, model.ToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"demo"}`)}, model.ToolResult{
			ToolCallID: strconv.Itoa(i),
			Content:    strings.Repeat("q", 40_000),
		})
	}
	messages := manager.Messages()
	last := messages[len(messages)-1].Content[0].ToolResult.Content
	if len(last) > 12_000 {
		t.Fatalf("轮内预览预算用尽后应使用小预览：%d", len(last))
	}
	if !strings.Contains(last, "8 KiB") {
		t.Fatalf("小预览标记错误：%s", last[:min(200, len(last))])
	}
}

func TestSummaryPrefixKeepsRecentTurns(t *testing.T) {
	manager := NewManager(Config{Budget: NewBudget(1_000_000, 4096)})
	for turn := 1; turn <= 8; turn++ {
		manager.BeginTurn(turn, userMessage("user-"+strconv.Itoa(turn)))
		manager.AppendAssistant(assistantMessage("answer-" + strconv.Itoa(turn)))
		manager.FinishTurn()
	}
	if !manager.ApplySummary("摘要A", 5) {
		t.Fatal("应替换旧轮次")
	}
	messages := manager.Messages()
	if len(messages) != 11 {
		t.Fatalf("保留消息数量错误：%d", len(messages))
	}
	if messages[0].Role != model.RoleUser || !strings.Contains(messages[0].Content[0].Text, "摘要A") {
		t.Fatalf("摘要消息错误：%+v", messages[0])
	}
	stats := manager.Stats("", nil)
	if stats.Turns != 5 || stats.SummaryTurns != 3 || stats.CoveredThrough != 3 {
		t.Fatalf("摘要统计错误：%+v", stats)
	}
	if input, _ := manager.BuildSummaryInput(5); len(input) != 0 {
		t.Fatalf("刚压缩后不应再有旧轮次：%+v", input)
	}

	manager.BeginTurn(9, userMessage("user-9"))
	manager.AppendAssistant(assistantMessage("answer-9"))
	manager.FinishTurn()
	input, previous := manager.BuildSummaryInput(5)
	if len(input) != 2 || previous != "摘要A" {
		t.Fatalf("摘要输入错误：%d %q", len(input), previous)
	}
	if !strings.Contains(input[0].Content[0].Text, "user-4") {
		t.Fatalf("应摘要最早的保留轮次：%+v", input)
	}
}

func TestDeterministicTrimCanDropAllTurns(t *testing.T) {
	manager := NewManager(Config{Budget: NewBudget(1_000_000, 4096)})
	for turn := 1; turn <= 3; turn++ {
		manager.BeginTurn(turn, userMessage("user-"+strconv.Itoa(turn)))
		manager.AppendAssistant(assistantMessage("answer-" + strconv.Itoa(turn)))
		manager.FinishTurn()
	}
	if !manager.DeterministicTrim(0, "测试") {
		t.Fatal("应执行确定性裁剪")
	}
	messages := manager.Messages()
	if len(messages) != 1 || manager.Stats("", nil).Turns != 0 {
		t.Fatalf("裁剪后应只保留说明：%d %+v", len(messages), manager.Stats("", nil))
	}
	manager.BeginTurn(4, userMessage("after-trim"))
	if got := manager.Messages(); len(got) != 2 || got[1].Role != model.RoleUser {
		t.Fatalf("裁剪后无法继续：%+v", got)
	}
}

func TestObserveUsageCalibratesEstimate(t *testing.T) {
	manager := NewManager(Config{Budget: NewBudget(1_000_000, 4096)})
	manager.BeginTurn(1, userMessage("hello"))
	raw := manager.RawEstimate("", nil)
	usage := model.Usage{InputTokens: int64Ptr(int64(raw * 2))}
	manager.ObserveUsage(raw, usage)
	if manager.Stats("", nil).Calibration <= 1 {
		t.Fatalf("校准未生效：%+v", manager.Stats("", nil))
	}
	if manager.Estimate("", nil) <= raw {
		t.Fatal("估算应随校准上调")
	}
	for i := 0; i < 30; i++ {
		raw = manager.RawEstimate("", nil)
		actual := int64(raw * 4)
		manager.ObserveUsage(raw, model.Usage{InputTokens: &actual})
	}
	if got := manager.Estimate("", nil); got < raw*3 || got > raw*5 {
		t.Fatalf("校准应收敛到真实比例附近：raw=%d estimate=%d cal=%.3f", raw, got, manager.Stats("", nil).Calibration)
	}
}

func TestThrashingProtection(t *testing.T) {
	manager := NewManager(Config{Budget: NewBudget(1_000_000, 4096)})
	for turn := 1; turn <= 4; turn++ {
		manager.BeginTurn(turn, userMessage("u"))
		manager.AppendAssistant(assistantMessage("a"))
		manager.FinishTurn()
		if turn == 1 {
			manager.RecordSummaryResult(true, 100)
		}
	}
	for i := 0; i < 2; i++ {
		if !manager.NoteAutoTrigger() {
			t.Fatalf("第 %d 次不应熔断", i+1)
		}
	}
	if manager.NoteAutoTrigger() {
		t.Fatal("快速回填三次后应熔断")
	}
	if !manager.AutoDisabled() {
		t.Fatal("熔断状态未记录")
	}
}

func int64Ptr(value int64) *int64 { return &value }
