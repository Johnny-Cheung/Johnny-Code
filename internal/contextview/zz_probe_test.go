package contextview

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

func probeUser(text string) model.Message {
	return model.Message{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.BlockText, Text: text}}}
}

func probeAssistant(text string) model.Message {
	return model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{Type: model.BlockText, Text: text}}}
}

func TestProbeRolesAfterSummary(t *testing.T) {
	manager := NewManager(Config{Budget: NewBudget(1_000_000, 4096)})
	for turn := 1; turn <= 8; turn++ {
		manager.BeginTurn(turn, probeUser("user-"+strconv.Itoa(turn)))
		manager.AppendAssistant(probeAssistant("answer-" + strconv.Itoa(turn)))
		manager.FinishTurn()
	}
	if !manager.ApplySummary("摘要A", 5) {
		t.Fatal("summary failed")
	}
	manager.BeginTurn(9, probeUser("user-9"))
	for i, message := range manager.Messages() {
		t.Logf("msg[%d] role=%s first=%q", i, message.Role, message.Content[0].Text)
	}
	// retain=0 case
	m2 := NewManager(Config{Budget: NewBudget(1_000_000, 4096)})
	for turn := 1; turn <= 6; turn++ {
		m2.BeginTurn(turn, probeUser("u"+strconv.Itoa(turn)))
		m2.AppendAssistant(probeAssistant("a" + strconv.Itoa(turn)))
		m2.FinishTurn()
	}
	m2.BeginTurn(7, probeUser("u7"))
	if !m2.DeterministicTrim(0, "probe") {
		t.Fatal("trim failed")
	}
	t.Log("--- trim retain=0 ---")
	for i, message := range m2.Messages() {
		t.Logf("msg[%d] role=%s first=%q", i, message.Role, message.Content[0].Text)
	}
}

func TestProbeCalibration(t *testing.T) {
	manager := NewManager(Config{Budget: NewBudget(1_000_000, 4096)})
	for i := 0; i < 60; i++ {
		manager.BeginTurn(i+1, probeUser("hello world task"))
		manager.AppendAssistant(probeAssistant("some answer text here"))
		manager.FinishTurn()
		raw := manager.RawEstimate("", nil)
		// 真实用量恒为原始估算的 2 倍
		usage := model.Usage{InputTokens: int64Ptr(int64(raw * 2))}
		manager.ObserveUsage(manager.Estimate("", nil), usage)
	}
	t.Logf("calibration after 60 observations with true ratio 2.0: %.4f", manager.Stats("", nil).Calibration)
	fmt.Println("CALIB", manager.Stats("", nil).Calibration)
}

func TestProbeSummaryRequestPairing(t *testing.T) {
	// 在工具调用后立刻结束的轮次，检查 view 消息序列
	manager := NewManager(Config{Budget: NewBudget(1_000_000, 4096)})
	manager.BeginTurn(1, probeUser("task"))
	manager.AppendAssistant(model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{Type: model.BlockToolCall, ToolCall: &model.ToolCall{ID: "c1", Name: "bash", Arguments: []byte(`{"command":"x"}`)}}}})
	manager.AppendToolResult(1, model.ToolCall{Name: "bash", Arguments: []byte(`{"command":"x"}`)}, model.ToolResult{ToolCallID: "c1", Content: "out"})
	manager.FinishTurn()
	manager.BeginTurn(2, probeUser("task2"))
	input, previous := manager.BuildSummaryInput(0)
	t.Logf("input=%d previous=%q", len(input), previous)
	for i, message := range input {
		t.Logf("in[%d] role=%s", i, message.Role)
	}
}
