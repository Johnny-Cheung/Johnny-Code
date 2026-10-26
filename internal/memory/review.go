package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/contextview"
	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

const (
	defaultReviewTimeout   = 30 * time.Second
	defaultReviewAttempts  = 2
	defaultReviewBackoff   = 2 * time.Second
	defaultReviewQueue     = 3
	maxReviewInputTokens   = 8000
	maxReviewOutputTokens  = 2048
	maxReviewToolResult    = 1500
	maxReviewToolArguments = 500
)

var (
	errReviewQueueFull = errors.New("记忆审查队列已满")
	errReviewPaused    = errors.New("记忆审查已暂停")
)

// Reviewer 在轮末异步提取长期记忆。它只使用独立模型请求，不参与主对话工具循环。
type Reviewer struct {
	Client          model.Client
	Model           string
	SessionID       string
	Store           *Store
	Rules           *Rules
	Agents          *Agents
	InteractionMode string

	Timeout  time.Duration
	Attempts int
	Backoff  time.Duration

	runMu    sync.Mutex
	stateMu  sync.Mutex
	failures int
	paused   bool
	slots    chan struct{}
}

func NewReviewer(client model.Client, modelName, sessionID string, store *Store, rules *Rules, agents *Agents, interactionMode string) *Reviewer {
	return &Reviewer{
		Client:          client,
		Model:           modelName,
		SessionID:       sessionID,
		Store:           store,
		Rules:           rules,
		Agents:          agents,
		InteractionMode: interactionMode,
		Timeout:         defaultReviewTimeout,
		Attempts:        defaultReviewAttempts,
		Backoff:         defaultReviewBackoff,
		slots:           make(chan struct{}, defaultReviewQueue),
	}
}

func (r *Reviewer) Review(ctx context.Context, userTurn int, messages []model.Message) (string, error) {
	if r == nil || r.Client == nil || r.Store == nil || r.Rules == nil {
		return "", errors.New("记忆审查器未完整配置")
	}
	r.stateMu.Lock()
	paused := r.paused
	r.stateMu.Unlock()
	if paused {
		return "", errReviewPaused
	}
	if r.slots == nil {
		r.slots = make(chan struct{}, defaultReviewQueue)
	}
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	default:
		return "", errReviewQueueFull
	}
	r.runMu.Lock()
	defer r.runMu.Unlock()
	return r.review(ctx, userTurn, messages)
}

func (r *Reviewer) review(ctx context.Context, userTurn int, messages []model.Message) (string, error) {
	prompt, err := r.buildPrompt(userTurn, messages)
	if err != nil {
		r.recordFailure()
		return "", err
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = defaultReviewTimeout
	}
	attempts := r.Attempts
	if attempts <= 0 {
		attempts = defaultReviewAttempts
	}
	backoff := r.Backoff
	if backoff < 0 {
		backoff = defaultReviewBackoff
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if ctx == nil {
			ctx = context.Background()
		}
		attemptCtx, cancel := context.WithTimeout(ctx, timeout)
		requestText := prompt
		if attempt > 0 {
			requestText += "\n\n上一次输出不是有效 JSON。只输出一个 JSON 对象，不要 Markdown 代码围栏，不要解释。"
		}
		response, err := r.Client.Stream(attemptCtx, model.Request{
			Model:  r.Model,
			System: reviewSystemPrompt,
			Messages: []model.Message{{
				Role:    model.RoleUser,
				Content: []model.ContentBlock{{Type: model.BlockText, Text: requestText}},
			}},
			MaxOutputTokens: maxReviewOutputTokens,
		}, nil)
		cancel()
		if err != nil {
			lastErr = err
		} else if response.StopReason != model.StopEndTurn {
			lastErr = fmt.Errorf("记忆审查未正常结束：%s", response.StopReason)
		} else {
			text := messageText(response.Message)
			output, err := parseReviewOutput(text)
			if err == nil {
				notice, applyErr := r.apply(ctx, userTurn, output)
				if applyErr != nil {
					lastErr = applyErr
				} else {
					r.recordSuccess()
					return notice, nil
				}
			} else {
				lastErr = err
			}
		}
		if attempt+1 < attempts && backoff > 0 {
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				r.recordFailure()
				return "", ctx.Err()
			case <-timer.C:
			}
		}
	}
	r.recordFailure()
	return "", lastErr
}

func (r *Reviewer) buildPrompt(userTurn int, messages []model.Message) (string, error) {
	var builder strings.Builder
	mode := strings.TrimSpace(r.InteractionMode)
	if mode == "" {
		mode = "未提供"
	}
	fmt.Fprintf(&builder, "当前交互模式：%s\n当前用户轮次：%d\n\n", mode, userTurn)
	if r.Agents != nil {
		text, warning := r.Agents.Load()
		builder.WriteString("## 现有用户/项目 AGENTS\n")
		if strings.TrimSpace(text) == "" {
			builder.WriteString("（无）\n")
		} else {
			builder.WriteString(limitTextByTokens(text, 2500))
			builder.WriteByte('\n')
		}
		if warning != "" {
			builder.WriteString("警告：")
			builder.WriteString(warning)
			builder.WriteByte('\n')
		}
		builder.WriteByte('\n')
	}
	if r.Store != nil {
		text, warning := r.Store.IndexText()
		builder.WriteString("## 现有记忆索引\n")
		if strings.TrimSpace(text) == "" {
			builder.WriteString("（无）\n")
		} else {
			builder.WriteString(limitTextByTokens(text, 2500))
			builder.WriteByte('\n')
		}
		if warning != "" {
			builder.WriteString("警告：")
			builder.WriteString(warning)
			builder.WriteByte('\n')
		}
		builder.WriteByte('\n')
	}
	builder.WriteString("## 本轮对话\n")
	builder.WriteString(renderReviewMessages(messages))
	return limitTextByTokens(builder.String(), maxReviewInputTokens), nil
}

func (r *Reviewer) apply(ctx context.Context, userTurn int, output reviewOutput) (string, error) {
	written := 0
	rules := 0
	skipped := 0
	for _, candidate := range output.Memories {
		action := strings.ToLower(strings.TrimSpace(candidate.Action))
		if action == "noop" {
			continue
		}
		scope, ok := ParseScope(candidate.Scope)
		if !ok {
			skipped++
			continue
		}
		itemType := Type(strings.TrimSpace(candidate.Type))
		if itemType == "" {
			itemType = TypeFeedback
		}
		if !itemType.Valid() {
			skipped++
			continue
		}
		confidence := Confidence(strings.TrimSpace(candidate.Confidence))
		if confidence == "" {
			confidence = ConfidenceMedium
		}
		if !confidence.Valid() {
			skipped++
			continue
		}
		_, err := r.Store.Write(ctx, Item{
			Name:        candidate.Name,
			Description: candidate.Description,
			Type:        itemType,
			Scope:       scope,
			Confidence:  confidence,
			Body:        candidate.Body,
			Source:      fmt.Sprintf("%s:turn-%d", r.SessionID, userTurn),
		})
		if err != nil {
			skipped++
			continue
		}
		written++
	}
	for _, candidate := range output.UserAgents {
		action := strings.ToLower(strings.TrimSpace(candidate.Action))
		if action == "noop" {
			continue
		}
		if strings.TrimSpace(candidate.Evidence) == "" || strings.ToLower(strings.TrimSpace(candidate.Confidence)) != string(ConfidenceHigh) {
			skipped++
			continue
		}
		if _, err := r.Rules.AddWithSource(ctx, candidate.Name, candidate.Description, fmt.Sprintf("%s:turn-%d", r.SessionID, userTurn)); err != nil {
			skipped++
			continue
		}
		rules++
	}
	var parts []string
	if written > 0 {
		parts = append(parts, fmt.Sprintf("已自动记录 %d 条长期记忆", written))
	}
	if rules > 0 {
		parts = append(parts, fmt.Sprintf("已新增 %d 条用户规则", rules))
	}
	if len(output.ProjectAgentsSuggestions) > 0 {
		parts = append(parts, fmt.Sprintf("另有 %d 条项目 AGENTS 建议未写入", len(output.ProjectAgentsSuggestions)))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("跳过 %d 条无效候选", skipped))
	}
	if len(parts) == 0 {
		if written == 0 && rules == 0 && skipped > 0 {
			return "", errors.New("记忆审查候选全部无效")
		}
		return "", nil
	}
	return strings.Join(parts, "；") + "。", nil
}

func (r *Reviewer) recordFailure() {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.failures++
	if r.failures >= 3 {
		r.paused = true
	}
}

func (r *Reviewer) recordSuccess() {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.failures = 0
	r.paused = false
}

type reviewOutput struct {
	Memories                 []reviewMemory    `json:"memories"`
	UserAgents               []reviewUserAgent `json:"user_agents"`
	ProjectAgentsSuggestions []string          `json:"project_agents_suggestions"`
}

type reviewMemory struct {
	Action      string `json:"action"`
	Scope       string `json:"scope"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Confidence  string `json:"confidence"`
	Reason      string `json:"reason"`
}

type reviewUserAgent struct {
	Action      string `json:"action"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Evidence    string `json:"evidence"`
	Confidence  string `json:"confidence"`
}

func parseReviewOutput(text string) (reviewOutput, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return reviewOutput{}, errors.New("记忆审查响应缺少 JSON 对象")
	}
	var output reviewOutput
	if err := json.Unmarshal([]byte(text[start:end+1]), &output); err != nil {
		return reviewOutput{}, fmt.Errorf("记忆审查 JSON 无效：%w", err)
	}
	return output, nil
}

func messageText(message model.Message) string {
	var builder strings.Builder
	for _, block := range message.Content {
		if block.Type == model.BlockText && block.Text != "" {
			if builder.Len() > 0 {
				builder.WriteByte('\n')
			}
			builder.WriteString(block.Text)
		}
	}
	return builder.String()
}

func renderReviewMessages(messages []model.Message) string {
	var builder strings.Builder
	for _, message := range messages {
		fmt.Fprintf(&builder, "[%s]\n", message.Role)
		for _, block := range message.Content {
			switch block.Type {
			case model.BlockText:
				if text := strings.TrimSpace(block.Text); text != "" {
					builder.WriteString(limitTextByTokens(text, 1800))
					builder.WriteByte('\n')
				}
			case model.BlockToolCall:
				if block.ToolCall != nil {
					fmt.Fprintf(&builder, "[tool_call %s] %s\n", block.ToolCall.Name, limitRunes(string(block.ToolCall.Arguments), maxReviewToolArguments))
				}
			case model.BlockToolResult:
				if block.ToolResult != nil {
					status := "ok"
					if block.ToolResult.IsError {
						status = "error"
					}
					fmt.Fprintf(&builder, "[tool_result %s] %s\n", status, limitRunes(block.ToolResult.Content, maxReviewToolResult))
				}
			}
		}
		builder.WriteByte('\n')
	}
	return builder.String()
}

func limitRunes(text string, limit int) string {
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…（已截断）"
}

func limitTextByTokens(text string, maxTokens int) string {
	if maxTokens <= 0 || contextview.EstimateText(text) <= maxTokens {
		return text
	}
	runes := []rune(text)
	low, high := 0, len(runes)
	for low < high {
		mid := (low + high + 1) / 2
		if contextview.EstimateText(string(runes[:mid])) <= maxTokens {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return string(runes[:low]) + "\n…（输入已截断）"
}

const reviewSystemPrompt = `你是 Johnny-Code 的长期记忆审查器。你的任务是从本轮对话中提取值得跨会话保存的内容。
你不能调用工具，也不能要求执行命令。只输出一个 JSON 对象，不要 Markdown 代码围栏，不要解释。

原则：
- 只记录跨会话仍然成立、无法从代码/文档/git 直接推导、会改变未来行为的信息。
- 不记录临时任务状态、原始工具输出、完整日志、未验证猜测、密钥、Token、私钥或 .env 内容。
- 项目事实、已确认决策、命令、坑和环境怪癖写入 project scope。
- 稳定个人偏好、用户纠正和确认的方法写入 user scope。
- 允许什么都不写；宁可不写，也不要写错。
- 模型推断的偏好只写 memories，不写 user_agents。
- 只有用户明确说“以后所有项目……”“所有项目都……”或确认提升时，才输出 user_agents，并把用户原话放入 evidence。
- 项目 AGENTS 只能输出建议，绝不能当作已写入。

输出格式：
{"memories":[{"action":"create|update|noop","scope":"user|project","type":"preference|feedback|project|reference|environment","name":"kebab-case","description":"一行描述","body":"正文","confidence":"high|medium|low","reason":"依据"}],"user_agents":[{"action":"create|noop","name":"kebab-case","description":"一行规则","evidence":"用户原话","confidence":"high"}],"project_agents_suggestions":["建议文本"]}
没有内容时返回空数组。`
