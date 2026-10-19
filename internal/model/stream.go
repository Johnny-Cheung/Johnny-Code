package model

import (
	"encoding/json"
	"fmt"
)

type collector struct {
	response Response
	emit     func(Event) error
	ended    map[int]bool
}

func newCollector(source Source, emit func(Event) error) *collector {
	return &collector{response: Response{Message: Message{Role: RoleAssistant, Source: &source}}, emit: emit, ended: make(map[int]bool)}
}

func cloneBlock(b ContentBlock) ContentBlock {
	if b.ToolCall != nil {
		v := *b.ToolCall
		v.Arguments = append(json.RawMessage(nil), v.Arguments...)
		b.ToolCall = &v
	}
	if b.ToolResult != nil {
		v := *b.ToolResult
		b.ToolResult = &v
	}
	if b.Replay != nil {
		v := *b.Replay
		v.Data = append(json.RawMessage(nil), v.Data...)
		b.Replay = &v
	}
	return b
}

func (c *collector) send(event Event) error {
	if c.emit == nil {
		return nil
	}
	event.Block = cloneBlock(event.Block)
	if err := c.emit(event); err != nil {
		return fmt.Errorf("处理模型事件失败：%w", err)
	}
	return nil
}

func (c *collector) start(block ContentBlock) (int, error) {
	block = cloneBlock(block)
	text := block.Text
	block.Text = ""
	var args string
	if block.ToolCall != nil {
		args = string(block.ToolCall.Arguments)
		block.ToolCall.Arguments = nil
	}
	index := len(c.response.Message.Content)
	c.response.Message.Content = append(c.response.Message.Content, block)
	if err := c.send(Event{Type: EventBlockStart, BlockIndex: index, Block: block}); err != nil {
		return index, err
	}
	if text != "" {
		if err := c.delta(index, text); err != nil {
			return index, err
		}
	}
	if args != "" && args != "{}" {
		if err := c.delta(index, args); err != nil {
			return index, err
		}
	}
	return index, nil
}

func (c *collector) delta(index int, text string) error {
	if index < 0 || index >= len(c.response.Message.Content) || c.ended[index] {
		return protocolError("流式内容块顺序无效")
	}
	block := &c.response.Message.Content[index]
	switch block.Type {
	case BlockText, BlockThinking:
		block.Text += text
	case BlockToolCall:
		if block.ToolCall == nil {
			return protocolError("流式工具调用缺少元数据")
		}
		block.ToolCall.Arguments = append(block.ToolCall.Arguments, text...)
	default:
		return protocolError("不支持的流式内容块")
	}
	if text == "" {
		return nil
	}
	return c.send(Event{Type: EventBlockDelta, BlockIndex: index, Block: ContentBlock{Type: block.Type}, Delta: text})
}

func (c *collector) end(index int) error {
	if index < 0 || index >= len(c.response.Message.Content) || c.ended[index] {
		return protocolError("流式内容块结束顺序无效")
	}
	block := &c.response.Message.Content[index]
	if block.Type == BlockToolCall {
		if block.ToolCall == nil || block.ToolCall.ID == "" || block.ToolCall.Name == "" {
			return protocolError("模型返回了不完整的工具调用")
		}
		if len(block.ToolCall.Arguments) == 0 {
			block.ToolCall.Arguments = json.RawMessage(`{}`)
		}
		if !jsonObject(block.ToolCall.Arguments) {
			return protocolError("模型返回的工具参数不是有效的 JSON 对象")
		}
	}
	c.ended[index] = true
	return c.send(Event{Type: EventBlockEnd, BlockIndex: index, Block: *block})
}

func (c *collector) complete(reason StopReason) error {
	if reason == "" {
		return protocolError("模型未返回可识别的结束原因")
	}
	for i, b := range c.response.Message.Content {
		if !c.ended[i] {
			return protocolError("流式连接在内容块完成前结束")
		}
		if b.Type == BlockToolCall && reason != StopToolUse {
			return protocolError("工具调用与模型结束状态不一致")
		}
	}
	if reason == StopToolUse {
		found := false
		for _, b := range c.response.Message.Content {
			found = found || b.Type == BlockToolCall
		}
		if !found {
			return protocolError("模型请求工具，但没有返回完整工具调用")
		}
	}
	c.response.StopReason = reason
	return nil
}

func protocolError(message string) error { return &Error{Kind: ErrProtocol, Message: message} }

func jsonObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

func int64ptr(n int64) *int64 { return &n }
