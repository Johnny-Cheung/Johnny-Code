package model

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Johnny-Cheung/Johnny-Code/internal/config"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
)

func Stream(ctx context.Context, cfg config.Config, prompt string, output io.Writer) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	client := openai.NewClient(
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(cfg.BaseURL),
		option.WithMaxRetries(0),
	)
	stream := client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(cfg.Model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(prompt),
		},
	})
	defer func() { _ = stream.Close() }()

	wrote := false
	needsNewline := false
	defer func() {
		if needsNewline {
			if _, writeErr := fmt.Fprintln(output); err == nil && writeErr != nil {
				err = fmt.Errorf("输出回复失败：%w", writeErr)
			}
		}
	}()

	finishReason := ""
	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if text := choice.Delta.Content; text != "" {
			if _, err := io.WriteString(output, text); err != nil {
				return fmt.Errorf("输出回复失败：%w", err)
			}
			wrote = true
			needsNewline = !strings.HasSuffix(text, "\n")
		}
		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := stream.Err(); err != nil {
		var apiErr *openai.Error
		if errors.As(err, &apiErr) {
			return fmt.Errorf("模型服务返回 HTTP %d，请检查 Key、URL、模型名或服务状态", apiErr.StatusCode)
		}
		var streamErr *ssestream.StreamError
		if errors.As(err, &streamErr) {
			return errors.New("模型服务在流中返回错误，请检查 Key、额度、模型名或中转站状态")
		}
		return errors.New("模型请求或流式读取失败，请检查中转站地址、网络、代理及 API 协议")
	}
	if finishReason == "" {
		return errors.New("流式连接在模型完成回复前结束，请重试或检查中转站")
	}
	if finishReason != "stop" {
		return fmt.Errorf("模型未正常完成文本回复（finish_reason=%s），已显示的内容可能不完整", finishReason)
	}
	if !wrote {
		return errors.New("模型没有返回文本内容，请检查模型名称及中转站兼容性")
	}

	return nil
}
