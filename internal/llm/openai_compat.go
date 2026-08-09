package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"johnnycode/internal/config"
	"johnnycode/internal/conversation"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared"
)

const openaiCompatStreamIdleTimeout = 5 * time.Minute

type openaiCompatClient struct {
	client       openai.Client
	model        string
	systemPrompt string
}

func newOpenAICompatClient(cfg *config.ProviderConfig, systemPrompt string) (*openaiCompatClient, error) {
	apiKey := cfg.ResolveAPIKey()
	if apiKey == "" {
		return nil, &AuthenticationError{
			Message: "OpenAI-compatible API key not found. Set it in .johnnycode/config.yaml or via OPENAI_API_KEY env var.",
		}
	}

	client := openai.NewClient(
		option.WithAPIKey(apiKey),
		option.WithBaseURL(cfg.BaseURL),
	)

	return &openaiCompatClient{
		client:       client,
		model:        cfg.Model,
		systemPrompt: systemPrompt,
	}, nil
}

func (c *openaiCompatClient) Stream(ctx context.Context, conv *conversation.Manager, toolSchemas []map[string]any) (<-chan StreamEvent, <-chan error) {
	events := make(chan StreamEvent, 64)
	errs := make(chan error, 1)

	messages := buildChatCompletionMessages(c.systemPrompt, conv.GetMessages())

	var tools []openai.ChatCompletionToolParam
	for _, s := range toolSchemas {
		name, _ := s["name"].(string)
		desc, _ := s["description"].(string)
		params, _ := s["parameters"].(map[string]any)
		tools = append(tools, openai.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        name,
				Description: param.NewOpt(desc),
				Parameters:  shared.FunctionParameters(params),
				Strict:      param.NewOpt(false),
			},
		})
	}

	go func() {
		defer close(events)
		defer close(errs)

		reqParams := openai.ChatCompletionNewParams{
			Model:    c.model,
			Messages: messages,
			StreamOptions: openai.ChatCompletionStreamOptionsParam{
				IncludeUsage: param.NewOpt(true),
			},
		}
		if len(tools) > 0 {
			reqParams.Tools = tools
		}

		stream := c.client.Chat.Completions.NewStreaming(ctx, reqParams)
		defer stream.Close()

		
		
		
		
		type toolCallAccum struct {
			id       string
			name     string
			argsJSON string
		}
		toolCalls := make(map[int64]*toolCallAccum)

		
		
		type sseResult struct {
			hasNext bool
		}
		nextCh := make(chan sseResult, 1)

		readNext := func() {
			nextCh <- sseResult{hasNext: stream.Next()}
		}

		idle := time.NewTimer(openaiCompatStreamIdleTimeout)
		defer idle.Stop()

		go readNext()
		for {
			var res sseResult
			select {
			case <-ctx.Done():
				errs <- &NetworkError{Message: fmt.Sprintf("context cancelled: %v", ctx.Err())}
				return
			case <-idle.C:
				errs <- &NetworkError{Message: fmt.Sprintf("stream idle timeout: no SSE events for %s", openaiCompatStreamIdleTimeout)}
				return
			case res = <-nextCh:
			}

			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(openaiCompatStreamIdleTimeout)

			if !res.hasNext {
				break
			}

			chunk := stream.Current()

			
			if chunk.JSON.Usage.Valid() && chunk.Usage.PromptTokens != 0 {
				cached := int(chunk.Usage.PromptTokensDetails.CachedTokens)
				
				
				input := int(chunk.Usage.PromptTokens) - cached
				if input < 0 {
					input = 0
				}
				events <- StreamEnd{
					StopReason: "end_turn",
					Usage: UsageInfo{
						InputTokens:     input,
						OutputTokens:    int(chunk.Usage.CompletionTokens),
						CacheReadTokens: cached,
					},
				}
				go readNext()
				continue
			}

			if len(chunk.Choices) == 0 {
				go readNext()
				continue
			}

			choice := chunk.Choices[0]
			delta := choice.Delta

			
			if delta.Content != "" {
				events <- TextDelta{Text: delta.Content}
			}

			
			for _, tc := range delta.ToolCalls {
				acc, exists := toolCalls[tc.Index]
				if !exists {
					acc = &toolCallAccum{}
					toolCalls[tc.Index] = acc
				}

				
				if tc.ID != "" {
					acc.id = tc.ID
				}
				if tc.Function.Name != "" {
					acc.name = tc.Function.Name
					events <- ToolCallStart{ToolName: acc.name, ToolID: acc.id}
				}

				
				if tc.Function.Arguments != "" {
					acc.argsJSON += tc.Function.Arguments
					events <- ToolCallDelta{Text: tc.Function.Arguments}
				}
			}

			
			if choice.FinishReason == "tool_calls" || choice.FinishReason == "stop" {
				
				for _, acc := range toolCalls {
					var args map[string]any
					if acc.argsJSON != "" {
						json.Unmarshal([]byte(acc.argsJSON), &args)
					}
					if args == nil {
						args = map[string]any{}
					}
					events <- ToolCallComplete{
						ToolID:    acc.id,
						ToolName:  acc.name,
						Arguments: args,
					}
				}
				
				toolCalls = make(map[int64]*toolCallAccum)

				
				
				
				if choice.FinishReason == "stop" && !chunk.JSON.Usage.Valid() {
					events <- StreamEnd{StopReason: "end_turn", Usage: UsageInfo{}}
				}
			}

			go readNext()
		}

		if err := stream.Err(); err != nil {
			errs <- classifyOpenAIError(err)
		}
	}()

	return events, errs
}




func buildChatCompletionMessages(systemPrompt string, messages []conversation.Message) []openai.ChatCompletionMessageParamUnion {
	var result []openai.ChatCompletionMessageParamUnion

	
	if systemPrompt != "" {
		result = append(result, openai.SystemMessage(systemPrompt))
	}

	for _, m := range messages {
		if m.Role == "assistant" {
			

			if len(m.ToolUses) > 0 {
				
				assistant := openai.ChatCompletionAssistantMessageParam{}
				if m.Content != "" {
					assistant.Content.OfString = param.NewOpt(m.Content)
				}
				for _, tu := range m.ToolUses {
					argsJSON, _ := json.Marshal(tu.Arguments)
					assistant.ToolCalls = append(assistant.ToolCalls, openai.ChatCompletionMessageToolCallParam{
						ID: tu.ToolUseID,
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      tu.ToolName,
							Arguments: string(argsJSON),
						},
					})
				}
				result = append(result, openai.ChatCompletionMessageParamUnion{OfAssistant: &assistant})
			} else if m.Content != "" {
				result = append(result, openai.AssistantMessage(m.Content))
			}
		} else if len(m.ToolResults) > 0 {
			
			for _, tr := range m.ToolResults {
				result = append(result, openai.ToolMessage(tr.Content, tr.ToolUseID))
			}
		} else {
			
			result = append(result, openai.UserMessage(m.Content))
		}
	}

	return result
}
