package contextview

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

// Config 创建一个会话级上下文管理器。
type Config struct {
	Budget       Budget
	WorkDir      string
	SessionID    string
	PreviewBytes int
}

// Manager 维护请求视图。完整历史不在本包中，调用方仍保留所有消息。
type Manager struct {
	budget       Budget
	previewBytes int
	artifacts    *ArtifactStore

	request []model.Message
	turns   []Turn
	current int
	inTurn  bool
	callSeq int

	summary        string
	summaryTurns   int
	summaryCovered int
	summaryIsNote  bool

	calibration      float64
	autoDisabled     bool
	summaryFailures  int
	lastSummaryTurn  int
	lastSummaryAfter int
	fastRefills      int
	lastInputTokens  int
	cacheReadTokens  int
	cacheWriteTokens int
	hasUsage         bool
}

// Turn 是请求视图中的一个用户任务及其消息范围。
type Turn struct {
	UserTurn       int
	Start          int
	End            int
	Results        []ResultRef
	PreviewBytes   int
	PreviewLimited bool
	Compacted      bool
	Evaluated      bool
}

// ResultRef 指向请求视图中的一条工具结果。
type ResultRef struct {
	Message        int
	Block          int
	Tool           string
	Path           string
	Artifact       string
	OriginalBytes  int
	OriginalTokens int
	IsError        bool
	Compacted      bool
}

// CompactionInfo 描述一次轮级压缩的结果。
type CompactionInfo struct {
	Before    int
	After     int
	Saved     int
	Compacted bool
}

// Stats 用于 /context 和诊断。
type Stats struct {
	Budget         Budget
	RawInput       int
	Input          int
	SystemTokens   int
	ToolTokens     int
	MessageTokens  int
	Turns          int
	SummaryTurns   int
	CoveredThrough int
	Summary        string
	SummaryNote    bool
	AutoDisabled   bool
	Artifacts      string
	Calibration    float64
	HasUsage       bool
	LastInput      int
	CacheRead      int
	CacheWrite     int
}

func NewManager(cfg Config) *Manager {
	if !cfg.Budget.Valid() {
		cfg.Budget = NewBudget(DefaultWindow, DefaultOutputTokens)
	}
	if cfg.PreviewBytes <= 0 {
		cfg.PreviewBytes = DefaultPreviewBytes
	}
	return &Manager{
		budget:       cfg.Budget,
		previewBytes: cfg.PreviewBytes,
		artifacts:    NewArtifactStore(cfg.WorkDir, cfg.SessionID),
		current:      -1,
		calibration:  1,
	}
}

func (m *Manager) Budget() Budget { return m.budget }

func (m *Manager) PreviewBytes() int { return m.previewBytes }

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	return m.artifacts.Close()
}

func (m *Manager) BeginTurn(userTurn int, user model.Message) {
	if m == nil {
		return
	}
	m.turns = append(m.turns, Turn{UserTurn: userTurn, Start: len(m.request), End: len(m.request)})
	m.current = len(m.turns) - 1
	m.inTurn = true
	m.callSeq = 0
	m.request = append(m.request, cloneMessage(user))
	m.turns[m.current].End = len(m.request)
}

func (m *Manager) AppendAssistant(message model.Message) {
	if m == nil {
		return
	}
	m.request = append(m.request, m.governAssistant(message))
	if m.current >= 0 {
		m.turns[m.current].End = len(m.request)
	}
}

// AppendToolResult 按预览规则追加工具结果，并记录轮级压缩所需信息。
func (m *Manager) AppendToolResult(userTurn int, call model.ToolCall, result model.ToolResult) Preview {
	if m == nil {
		return Preview{Content: result.Content, OriginalBytes: len(result.Content)}
	}
	m.callSeq++
	limit := m.previewBytes
	if m.current >= 0 && m.turns[m.current].PreviewBytes >= TurnPreviewBudget {
		limit = min(limit, LimitedPreviewBytes)
		m.turns[m.current].PreviewLimited = true
	}
	preview := BuildPreview(call.Name, result.Content, call.Arguments, m.artifacts, userTurn, m.callSeq, limit)
	governed := model.ToolResult{ToolCallID: result.ToolCallID, Content: preview.Content, IsError: result.IsError}
	m.request = append(m.request, model.Message{
		Role:    model.RoleTool,
		Content: []model.ContentBlock{{Type: model.BlockToolResult, ToolResult: &governed}},
	})
	if m.current >= 0 {
		turn := &m.turns[m.current]
		turn.End = len(m.request)
		turn.PreviewBytes += len(preview.Content)
		turn.Results = append(turn.Results, ResultRef{
			Message:        len(m.request) - 1,
			Block:          0,
			Tool:           call.Name,
			Path:           toolPath(call),
			Artifact:       preview.Artifact,
			OriginalBytes:  len(result.Content),
			OriginalTokens: EstimateText(result.Content),
			IsError:        result.IsError,
		})
	}
	return preview
}

// FinishTurn 评估并只压缩刚结束的这一轮。更早轮次保持原样。
func (m *Manager) FinishTurn() CompactionInfo {
	if m == nil {
		return CompactionInfo{}
	}
	defer func() { m.inTurn = false }()
	if m.current < 0 || m.current >= len(m.turns) {
		return CompactionInfo{}
	}
	return m.compactTurn(m.current)
}

func (m *Manager) compactTurn(index int) CompactionInfo {
	turn := &m.turns[index]
	if turn.Evaluated {
		return CompactionInfo{}
	}
	turn.Evaluated = true
	before, after := 0, 0
	replacements := 0
	for _, ref := range turn.Results {
		content, ok := m.resultContent(ref)
		if !ok {
			continue
		}
		before += EstimateText(content)
		record := compactRecord(ref, content, CompactPreviewBytes)
		compactTokens := EstimateText(record)
		if compactTokens < EstimateText(content) && compactTokens > 0 {
			after += compactTokens
			replacements++
		} else {
			after += EstimateText(content)
		}
	}
	saved := before - after
	if replacements == 0 || saved < CompactMinSavedTokens || saved*2 < before {
		return CompactionInfo{Before: before, After: before}
	}
	for i := range turn.Results {
		ref := &turn.Results[i]
		content, ok := m.resultContent(*ref)
		if !ok {
			continue
		}
		record := compactRecord(*ref, content, CompactPreviewBytes)
		if EstimateText(record) < EstimateText(content) {
			m.replaceResult(*ref, record)
			ref.Compacted = true
		}
	}
	turn.Compacted = true
	return CompactionInfo{Before: before, After: after, Saved: saved, Compacted: true}
}

func (m *Manager) resultContent(ref ResultRef) (string, bool) {
	if ref.Message < 0 || ref.Message >= len(m.request) {
		return "", false
	}
	message := m.request[ref.Message]
	if ref.Block < 0 || ref.Block >= len(message.Content) {
		return "", false
	}
	result := message.Content[ref.Block].ToolResult
	if result == nil {
		return "", false
	}
	return result.Content, true
}

func (m *Manager) replaceResult(ref ResultRef, content string) {
	if ref.Message < 0 || ref.Message >= len(m.request) {
		return
	}
	message := &m.request[ref.Message]
	if ref.Block < 0 || ref.Block >= len(message.Content) {
		return
	}
	if message.Content[ref.Block].ToolResult == nil {
		return
	}
	message.Content[ref.Block].ToolResult.Content = content
}

// Messages 返回请求视图的浅拷贝。调用方不得修改其中的内容。
func (m *Manager) Messages() []model.Message {
	if m == nil {
		return nil
	}
	return append([]model.Message(nil), m.request...)
}

// Estimate 返回校准后的输入 token 估算。
func (m *Manager) Estimate(system string, tools []model.ToolDefinition) int {
	return m.calibrate(m.RawEstimate(system, tools))
}

func (m *Manager) RawEstimate(system string, tools []model.ToolDefinition) int {
	if m == nil {
		return 0
	}
	return EstimateRequest(system, tools, m.request)
}

func (m *Manager) NeedsTurnCompaction(system string, tools []model.ToolDefinition) bool {
	return m != nil && m.Estimate(system, tools) >= m.budget.TurnTrigger()
}

func (m *Manager) NeedsRequestCompaction(system string, tools []model.ToolDefinition) bool {
	return m != nil && m.Estimate(system, tools) >= m.budget.RequestTrigger()
}

func (m *Manager) InputTokens(system string, tools []model.ToolDefinition) int {
	return m.Estimate(system, tools)
}

func (m *Manager) Stats(system string, tools []model.ToolDefinition) Stats {
	if m == nil {
		return Stats{}
	}
	raw := m.RawEstimate(system, tools)
	return Stats{
		Budget:         m.budget,
		RawInput:       raw,
		Input:          m.calibrate(raw),
		SystemTokens:   EstimateText(system),
		ToolTokens:     EstimateTools(tools),
		MessageTokens:  EstimateMessages(m.request),
		Turns:          len(m.turns),
		SummaryTurns:   m.summaryTurns,
		CoveredThrough: m.summaryCovered,
		Summary:        m.summary,
		SummaryNote:    m.summaryIsNote,
		AutoDisabled:   m.autoDisabled,
		Artifacts:      m.artifacts.Dir(),
		Calibration:    m.calibration,
		HasUsage:       m.hasUsage,
		LastInput:      m.lastInputTokens,
		CacheRead:      m.cacheReadTokens,
		CacheWrite:     m.cacheWriteTokens,
	}
}

// BuildSummaryInput 返回需要摘要的旧消息和上一次摘要。
func (m *Manager) BuildSummaryInput(retain int) ([]model.Message, string) {
	if m == nil {
		return nil, ""
	}
	completed := len(m.turns)
	if m.inTurn && completed > 0 {
		completed--
	}
	if retain < 0 {
		retain = 0
	}
	keepStart := completed - retain
	if keepStart < 0 || keepStart > len(m.turns) {
		return nil, m.summary
	}
	oldStart := 1
	if m.summary == "" {
		oldStart = 0
	}
	if oldStart > len(m.request) {
		return nil, m.summary
	}
	end := len(m.request)
	if keepStart < len(m.turns) {
		end = m.turns[keepStart].Start
	}
	if end <= oldStart {
		return nil, m.summary
	}
	return cloneMessages(m.request[oldStart:end]), m.summary
}

// ApplySummary 用一条合成 user 消息替换旧轮次，只保留最新摘要。
func (m *Manager) ApplySummary(text string, retain int) bool {
	return m.replacePrefix(text, retain, false)
}

// DeterministicTrim 在摘要失败时降级为确定性裁剪，并保留已有摘要。
func (m *Manager) DeterministicTrim(retain int, reason string) bool {
	if m == nil {
		return false
	}
	note := fmt.Sprintf("[上下文已裁剪]\n因%s，已省略较早的对话。完整记录仍在本地会话诊断日志中。", strings.TrimSpace(reason))
	if m.summary != "" {
		note += "\n\n已有摘要：\n" + m.summary
	}
	return m.replacePrefix(note, retain, true)
}

func (m *Manager) replacePrefix(text string, retain int, note bool) bool {
	if m == nil || strings.TrimSpace(text) == "" {
		return false
	}
	completed := len(m.turns)
	if m.inTurn && completed > 0 {
		completed--
	}
	if retain < 0 {
		retain = 0
	}
	keepStart := completed - retain
	if keepStart <= 0 || keepStart > len(m.turns) {
		return false
	}
	oldStart := len(m.request)
	if keepStart < len(m.turns) {
		oldStart = m.turns[keepStart].Start
	}
	if oldStart <= 0 || oldStart > len(m.request) {
		return false
	}
	covered := m.turns[keepStart-1].UserTurn
	kept := cloneMessages(m.request[oldStart:])
	summaryMessage := model.Message{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.BlockText, Text: text}}}
	m.request = append([]model.Message{summaryMessage}, kept...)
	delta := 1 - oldStart
	m.turns = m.turns[keepStart:]
	if m.inTurn {
		m.current -= keepStart
	} else {
		m.current = len(m.turns) - 1
	}
	for i := range m.turns {
		m.turns[i].Start += delta
		m.turns[i].End += delta
		for j := range m.turns[i].Results {
			m.turns[i].Results[j].Message += delta
		}
	}
	m.summary = text
	m.summaryIsNote = note
	m.summaryTurns += keepStart
	m.summaryCovered = covered
	return true
}

// EmergencyCompactCurrent 在摘要和裁剪后仍超限时，对全部可见工具结果使用最小预览。
func (m *Manager) EmergencyCompactCurrent() int {
	if m == nil {
		return 0
	}
	saved := 0
	for i := range m.turns {
		turn := &m.turns[i]
		for j := range turn.Results {
			ref := &turn.Results[j]
			content, ok := m.resultContent(*ref)
			if !ok || strings.HasPrefix(content, "[工具结果已压缩]") {
				continue
			}
			before := EstimateText(content)
			record := compactRecord(*ref, content, EmergencyBytes)
			after := EstimateText(record)
			if after < before {
				m.replaceResult(*ref, record)
				ref.Compacted = true
				saved += before - after
			}
		}
	}
	return saved
}

func (m *Manager) SummaryText() string {
	if m == nil {
		return ""
	}
	return m.summary
}

func (m *Manager) SummaryIsNote() bool { return m != nil && m.summaryIsNote }

func (m *Manager) SummaryCovered() int {
	if m == nil {
		return 0
	}
	return m.summaryCovered
}

func (m *Manager) CurrentUserTurn() int {
	if m == nil || len(m.turns) == 0 {
		return 0
	}
	return m.turns[len(m.turns)-1].UserTurn
}

// ObserveUsage 用服务返回的实际输入 token 校准后续估算，并保留最近缓存指标。
func (m *Manager) ObserveUsage(estimate int, usage model.Usage) {
	if m == nil {
		return
	}
	if usage.InputTokens != nil {
		m.lastInputTokens = int(*usage.InputTokens)
		m.hasUsage = true
	}
	if usage.CacheReadTokens != nil {
		m.cacheReadTokens = int(*usage.CacheReadTokens)
	}
	if usage.CacheWriteTokens != nil {
		m.cacheWriteTokens = int(*usage.CacheWriteTokens)
	}
	if estimate <= 0 || usage.InputTokens == nil || *usage.InputTokens <= 0 {
		return
	}
	ratio := float64(*usage.InputTokens) / float64(estimate)
	if ratio < 0.1 {
		ratio = 0.1
	}
	if ratio > 10 {
		ratio = 10
	}
	calibration := 0.6*m.calibration + 0.4*ratio
	if calibration < 0.25 {
		calibration = 0.25
	}
	if calibration > 4 {
		calibration = 4
	}
	m.calibration = calibration
}

func (m *Manager) calibrate(raw int) int {
	if raw <= 0 || m == nil {
		return raw
	}
	value := int(float64(raw) * m.calibration)
	if value < 1 {
		return 1
	}
	return value
}

func (m *Manager) CanAutoCompact() bool {
	return m != nil && !m.autoDisabled
}

// NoteAutoTrigger 检查压缩后快速回填，并返回是否仍允许自动压缩。
func (m *Manager) NoteAutoTrigger() bool {
	if m == nil || m.autoDisabled {
		return false
	}
	turn := m.CurrentUserTurn()
	if m.lastSummaryTurn > 0 && turn-m.lastSummaryTurn <= 3 {
		m.fastRefills++
		if m.fastRefills >= 3 {
			m.autoDisabled = true
			return false
		}
	} else {
		m.fastRefills = 0
	}
	return true
}

func (m *Manager) RecordSummaryResult(success bool, after int) {
	if m == nil {
		return
	}
	if success {
		m.summaryFailures = 0
		m.lastSummaryTurn = m.CurrentUserTurn()
		m.lastSummaryAfter = after
		return
	}
	m.summaryFailures++
	if m.summaryFailures >= 3 {
		m.autoDisabled = true
	}
}

func (m *Manager) DisableAuto() {
	if m != nil {
		m.autoDisabled = true
	}
}

func (m *Manager) AutoDisabled() bool { return m != nil && m.autoDisabled }

func (m *Manager) governAssistant(message model.Message) model.Message {
	cloned := cloneMessage(message)
	for i := range cloned.Content {
		call := cloned.Content[i].ToolCall
		if call == nil || len(call.Arguments) <= m.previewBytes {
			continue
		}
		cloned.Content[i].ToolCall = &model.ToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: summarizeArguments(call),
		}
	}
	return cloned
}

func summarizeArguments(call *model.ToolCall) json.RawMessage {
	value := map[string]any{
		"_context_note": "工具调用参数过大，请求视图已省略完整内容；工具已按原始参数执行。",
		"tool":          call.Name,
		"bytes":         len(call.Arguments),
	}
	if path := argumentString(call.Arguments, "path"); path != "" {
		value["path"] = path
	}
	if name := argumentString(call.Arguments, "name"); name != "" {
		value["name"] = name
	}
	if command := argumentString(call.Arguments, "command"); command != "" {
		value["command_prefix"] = cutHeadAtLine(command, 512)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{"_context_note":"工具调用参数过大，已省略"}`)
	}
	return encoded
}

func compactRecord(ref ResultRef, content string, previewBytes int) string {
	if strings.HasPrefix(content, "[工具结果已压缩]") {
		return content
	}
	status := "成功"
	if ref.IsError {
		status = "失败"
	}
	preview := cutHeadAtLine(content, previewBytes)
	var builder strings.Builder
	builder.WriteString("[工具结果已压缩]\n")
	fmt.Fprintf(&builder, "工具：%s\n", ref.Tool)
	fmt.Fprintf(&builder, "状态：%s\n", status)
	fmt.Fprintf(&builder, "原始大小：%d 字节\n", ref.OriginalBytes)
	if ref.Artifact != "" {
		fmt.Fprintf(&builder, "存档：%s\n", ref.Artifact)
	}
	if ref.Path != "" {
		fmt.Fprintf(&builder, "来源：%s\n", ref.Path)
	}
	builder.WriteString("预览：\n")
	builder.WriteString(preview)
	if previewBytes > 0 && len(content) > previewBytes {
		builder.WriteString("\n…（其余内容已省略）")
	}
	return builder.String()
}

func toolPath(call model.ToolCall) string {
	for _, key := range []string{"path", "name"} {
		if value := argumentString(call.Arguments, key); value != "" {
			return value
		}
	}
	return ""
}

func cloneMessages(messages []model.Message) []model.Message {
	cloned := make([]model.Message, len(messages))
	for i, message := range messages {
		cloned[i] = cloneMessage(message)
	}
	return cloned
}

func cloneMessage(message model.Message) model.Message {
	cloned := model.Message{Role: message.Role, Source: message.Source}
	if message.Content == nil {
		return cloned
	}
	cloned.Content = make([]model.ContentBlock, len(message.Content))
	for i, block := range message.Content {
		cloned.Content[i] = block
		if block.ToolCall != nil {
			call := *block.ToolCall
			call.Arguments = append(json.RawMessage(nil), block.ToolCall.Arguments...)
			cloned.Content[i].ToolCall = &call
		}
		if block.ToolResult != nil {
			result := *block.ToolResult
			cloned.Content[i].ToolResult = &result
		}
	}
	return cloned
}
