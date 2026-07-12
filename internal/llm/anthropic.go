package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// AnthropicClient implements Client against the Anthropic Messages API.
//
// Retry policy: the SDK retries connection errors, timeouts, 408/409/429 and
// 5xx responses with exponential backoff. maxAttempts=3 means 1 initial
// attempt + 2 retries, satisfying the "max 3 attempts" requirement without a
// hand-rolled retry loop.
type AnthropicClient struct {
	client anthropic.Client
	model  anthropic.Model
}

func NewAnthropicClient(apiKey, model string, maxAttempts int) *AnthropicClient {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	return &AnthropicClient{
		client: anthropic.NewClient(
			option.WithAPIKey(apiKey),
			option.WithMaxRetries(maxAttempts-1),
		),
		model: anthropic.Model(model),
	}
}

func (a *AnthropicClient) Complete(ctx context.Context, req *Request) (*Response, error) {
	params := anthropic.MessageNewParams{
		Model:     a.model,
		MaxTokens: int64(req.MaxTokens),
		Thinking: anthropic.ThinkingConfigParamUnion{
			OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{},
		},
		Messages: make([]anthropic.MessageParam, 0, len(req.Messages)),
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}
	for _, m := range req.Messages {
		params.Messages = append(params.Messages, toMessageParam(m))
	}
	for _, t := range req.Tools {
		params.Tools = append(params.Tools, toToolParam(t))
	}

	resp, err := a.client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}

	out := &Response{StopReason: string(resp.StopReason)}
	for _, block := range resp.Content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			out.Content = append(out.Content, ContentBlock{Type: "text", Text: v.Text})
		case anthropic.ThinkingBlock:
			out.Content = append(out.Content, ContentBlock{
				Type: "thinking", Text: v.Thinking, Signature: v.Signature,
			})
		case anthropic.RedactedThinkingBlock:
			out.Content = append(out.Content, ContentBlock{Type: "redacted_thinking", Data: v.Data})
		case anthropic.ToolUseBlock:
			out.Content = append(out.Content, ContentBlock{
				Type:  "tool_use",
				ID:    v.ID,
				Name:  v.Name,
				Input: json.RawMessage(v.JSON.Input.Raw()),
			})
		}
	}
	return out, nil
}

func toMessageParam(m Message) anthropic.MessageParam {
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.Content))
	for _, b := range m.Content {
		switch b.Type {
		case "text":
			blocks = append(blocks, anthropic.NewTextBlock(b.Text))
		case "thinking":
			blocks = append(blocks, anthropic.ContentBlockParamUnion{
				OfThinking: &anthropic.ThinkingBlockParam{Thinking: b.Text, Signature: b.Signature},
			})
		case "redacted_thinking":
			blocks = append(blocks, anthropic.ContentBlockParamUnion{
				OfRedactedThinking: &anthropic.RedactedThinkingBlockParam{Data: b.Data},
			})
		case "tool_use":
			var input any = map[string]any{}
			if len(b.Input) > 0 {
				input = json.RawMessage(b.Input)
			}
			blocks = append(blocks, anthropic.ContentBlockParamUnion{
				OfToolUse: &anthropic.ToolUseBlockParam{ID: b.ID, Name: b.Name, Input: input},
			})
		case "tool_result":
			blocks = append(blocks, anthropic.NewToolResultBlock(b.ToolUseID, b.Content, b.IsError))
		}
	}
	if m.Role == "assistant" {
		return anthropic.NewAssistantMessage(blocks...)
	}
	return anthropic.NewUserMessage(blocks...)
}

func toToolParam(t ToolDef) anthropic.ToolUnionParam {
	tool := anthropic.ToolParam{
		Name:        t.Name,
		Description: anthropic.String(t.Description),
		InputSchema: anthropic.ToolInputSchemaParam{},
	}
	if props, ok := t.InputSchema["properties"].(map[string]any); ok {
		tool.InputSchema.Properties = props
	}
	if req, ok := t.InputSchema["required"].([]string); ok {
		tool.InputSchema.Required = req
	} else if reqAny, ok := t.InputSchema["required"].([]any); ok {
		for _, r := range reqAny {
			if s, ok := r.(string); ok {
				tool.InputSchema.Required = append(tool.InputSchema.Required, s)
			}
		}
	}
	return anthropic.ToolUnionParam{OfTool: &tool}
}
