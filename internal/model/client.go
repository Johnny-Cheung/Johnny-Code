package model

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/config"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func Stream(ctx context.Context, cfg config.Config, prompt string, output io.Writer) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	client := anthropic.NewClient(
		option.WithAPIKey(""),
		option.WithHeaderDel("X-Api-Key"),
		option.WithAuthToken(cfg.APIKey),
		option.WithBaseURL(cfg.BaseURL),
		option.WithMaxRetries(0),
	)
	stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(cfg.Model),
		MaxTokens: 4096,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	defer func() { _ = stream.Close() }()

	wrote := false
	needsNewline := false
	write := func(text string) error {
		if text == "" {
			return nil
		}
		if _, err := io.WriteString(output, text); err != nil {
			return fmt.Errorf("输出回复失败：%w", err)
		}
		wrote = true
		needsNewline = !strings.HasSuffix(text, "\n")
		return nil
	}
	defer func() {
		if needsNewline {
			if _, writeErr := fmt.Fprintln(output); err == nil && writeErr != nil {
				err = fmt.Errorf("输出回复失败：%w", writeErr)
			}
		}
	}()

	var stopReason anthropic.StopReason
	stopped := false
	for stream.Next() {
		switch event := stream.Current().AsAny().(type) {
		case anthropic.ContentBlockStartEvent:
			if block, ok := event.ContentBlock.AsAny().(anthropic.TextBlock); ok {
				if err := write(block.Text); err != nil {
					return err
				}
			}
		case anthropic.ContentBlockDeltaEvent:
			if delta, ok := event.Delta.AsAny().(anthropic.TextDelta); ok {
				if err := write(delta.Text); err != nil {
					return err
				}
			}
		case anthropic.MessageDeltaEvent:
			stopReason = event.Delta.StopReason
		case anthropic.MessageStopEvent:
			stopped = true
		}
		if stopped {
			break
		}
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := stream.Err(); err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			if apiErr.StatusCode == 200 {
				return errors.New("模型服务在流中返回错误，请检查 Key、额度、模型名或中转站状态")
			}
			return fmt.Errorf("模型服务返回 HTTP %d，请检查 Key、URL、模型名或服务状态", apiErr.StatusCode)
		}
		return errors.New("模型请求或流式读取失败，请检查中转站地址、网络、代理及 API 协议")
	}
	if !stopped || stopReason == "" {
		return errors.New("流式连接在模型完成回复前结束，请重试或检查中转站")
	}
	if stopReason != "end_turn" && stopReason != "stop_sequence" {
		return fmt.Errorf("模型未正常完成文本回复（stop_reason=%s），已显示的内容可能不完整", stopReason)
	}
	if !wrote {
		return errors.New("模型没有返回文本内容，请检查模型名称及中转站兼容性")
	}

	return nil
}
