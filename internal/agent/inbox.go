package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

// MaxInjectBytes 是一次注入或唤醒最多合并的收件箱文本大小。
const MaxInjectBytes = 24 << 10

// InboxItem 是等待进入主对话的外部数据，例如子任务的最终报告。
// Source 只用于诊断记录，Text 由调用方负责格式化和限长。
type InboxItem struct {
	Source string
	Text   string
}

// Enqueue 线程安全地加入收件箱。内容在下一次模型请求前注入当前轮，
// 或在空闲时由 RunInbox 作为唤醒轮的起点。
func (c *Conversation) Enqueue(item InboxItem) {
	if c == nil || strings.TrimSpace(item.Text) == "" {
		return
	}
	c.inboxMu.Lock()
	c.inbox = append(c.inbox, item)
	c.inboxMu.Unlock()
}

// InboxLen 返回等待注入的条目数。
func (c *Conversation) InboxLen() int {
	if c == nil {
		return 0
	}
	c.inboxMu.Lock()
	defer c.inboxMu.Unlock()
	return len(c.inbox)
}

// takeInbox 按先进先出取出不超过 MaxInjectBytes 的条目，至少取一条。
func (c *Conversation) takeInbox() []InboxItem {
	c.inboxMu.Lock()
	defer c.inboxMu.Unlock()
	total, count := 0, 0
	for count < len(c.inbox) {
		size := len(c.inbox[count].Text)
		if count > 0 && total+size > MaxInjectBytes {
			break
		}
		total += size
		count++
	}
	if count == 0 {
		return nil
	}
	items := append([]InboxItem(nil), c.inbox[:count]...)
	c.inbox = append([]InboxItem(nil), c.inbox[count:]...)
	return items
}

func (c *Conversation) requeueInbox(items []InboxItem) {
	c.inboxMu.Lock()
	c.inbox = append(append([]InboxItem(nil), items...), c.inbox...)
	c.inboxMu.Unlock()
}

func inboxText(items []InboxItem) (string, []string) {
	texts := make([]string, 0, len(items))
	sources := make([]string, 0, len(items))
	for _, item := range items {
		texts = append(texts, item.Text)
		sources = append(sources, item.Source)
	}
	return strings.Join(texts, "\n\n"), sources
}

// injectInbox 在模型请求前把收件箱内容作为一条合成 user 消息追加到历史和请求视图。
// turn 是紧随其后的模型请求序号。调用点必须位于所有工具结果之后，不会插到一批工具调用中间。
func (c *Conversation) injectInbox(turn int, notify func(Event) error) error {
	items := c.takeInbox()
	if len(items) == 0 {
		return nil
	}
	text, sources := inboxText(items)
	message := model.Message{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.BlockText, Text: text}}}
	c.messages = append(c.messages, message)
	c.view.AppendUser(message)
	return notify(Event{Type: EventInject, Turn: turn, Message: &message, Sources: sources})
}

// RunInbox 在没有新用户输入时，以收件箱内容作为本轮起点（空闲唤醒）。
// 收件箱为空时返回错误且不产生新轮次。
func (c *Conversation) RunInbox(ctx context.Context, emit func(Event) error) (Result, error) {
	if c == nil {
		return Result{Status: StatusFailed}, errors.New("会话不能为空")
	}
	items := c.takeInbox()
	if len(items) == 0 {
		return Result{Status: StatusFailed}, errors.New("收件箱为空")
	}
	text, sources := inboxText(items)
	result, err := c.run(ctx, text, sources, emit)
	if result.UserTurn == 0 {
		c.requeueInbox(items)
	}
	return result, err
}
