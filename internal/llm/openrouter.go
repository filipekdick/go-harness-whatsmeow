package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	openRouterChatCompletionsURL = "https://openrouter.ai/api/v1/chat/completions"
	maxOpenRouterResponseBody    = 4 << 20
	maxOpenRouterErrorDetail     = 2048
)

// OpenRouterClient implements Client using OpenRouter's Chat Completions API.
type OpenRouterClient struct {
	apiKey      string
	model       string
	maxAttempts int
	siteURL     string
	appName     string

	baseURL    string
	httpClient *http.Client
	backoff    func(attempt int) time.Duration
}

func NewOpenRouterClient(apiKey, model string, maxAttempts int, siteURL, appName string) *OpenRouterClient {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	return &OpenRouterClient{
		apiKey:      apiKey,
		model:       model,
		maxAttempts: maxAttempts,
		siteURL:     siteURL,
		appName:     appName,
		baseURL:     openRouterChatCompletionsURL,
		httpClient:  http.DefaultClient,
		backoff:     openRouterBackoff,
	}
}

func (c *OpenRouterClient) Complete(ctx context.Context, req *Request) (*Response, error) {
	if req == nil {
		return nil, errors.New("openrouter: nil request")
	}

	payload, err := c.requestPayload(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("openrouter: encode request: %w", err)
	}

	attempts := c.maxAttempts
	if attempts < 1 {
		attempts = 1
	}
	client := c.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	baseURL := c.baseURL
	if baseURL == "" {
		baseURL = openRouterChatCompletionsURL
	}

	for attempt := 1; attempt <= attempts; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("openrouter: create request: %w", err)
		}
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
		httpReq.Header.Set("Content-Type", "application/json")
		if c.siteURL != "" {
			httpReq.Header.Set("HTTP-Referer", c.siteURL)
		}
		if c.appName != "" {
			httpReq.Header.Set("X-Title", c.appName)
		}

		httpResp, err := client.Do(httpReq)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("openrouter: request canceled: %w", ctxErr)
			}
			if attempt == attempts {
				return nil, fmt.Errorf("openrouter: transport error after %d attempt(s): %w", attempt, err)
			}
			if err := c.waitForRetry(ctx, attempt, ""); err != nil {
				return nil, err
			}
			continue
		}

		responseBody, tooLarge, readErr := readOpenRouterBody(httpResp.Body)
		if readErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("openrouter: request canceled: %w", ctxErr)
			}
			if attempt == attempts {
				return nil, fmt.Errorf("openrouter: read response after %d attempt(s): %w", attempt, readErr)
			}
			if err := c.waitForRetry(ctx, attempt, ""); err != nil {
				return nil, err
			}
			continue
		}

		if isOpenRouterRetryableStatus(httpResp.StatusCode) {
			if attempt < attempts {
				if err := c.waitForRetry(ctx, attempt, httpResp.Header.Get("Retry-After")); err != nil {
					return nil, err
				}
				continue
			}
			return nil, c.httpStatusError(httpResp, responseBody, tooLarge, attempt)
		}
		if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
			return nil, c.httpStatusError(httpResp, responseBody, tooLarge, attempt)
		}
		if tooLarge {
			return nil, fmt.Errorf("openrouter: response body exceeds %d bytes", maxOpenRouterResponseBody)
		}

		return c.decodeResponse(responseBody)
	}

	return nil, errors.New("openrouter: request failed")
}

type openRouterRequest struct {
	Model               string              `json:"model"`
	Messages            []openRouterMessage `json:"messages"`
	Tools               []openRouterTool    `json:"tools,omitempty"`
	MaxCompletionTokens int                 `json:"max_completion_tokens"`
	ToolChoice          string              `json:"tool_choice,omitempty"`
}

type openRouterMessage struct {
	Role             string               `json:"role"`
	Content          *string              `json:"content,omitempty"`
	ToolCalls        []openRouterToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string               `json:"tool_call_id,omitempty"`
	Name             string               `json:"name,omitempty"`
	ReasoningDetails json.RawMessage      `json:"reasoning_details,omitempty"`
}

type openRouterTool struct {
	Type     string                 `json:"type"`
	Function openRouterToolFunction `json:"function"`
}

type openRouterToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type openRouterToolCall struct {
	ID       string                     `json:"id"`
	Type     string                     `json:"type"`
	Function openRouterToolCallFunction `json:"function"`
}

type openRouterToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (c *OpenRouterClient) requestPayload(req *Request) (openRouterRequest, error) {
	payload := openRouterRequest{
		Model:               c.model,
		MaxCompletionTokens: req.MaxTokens,
	}
	if req.System != "" {
		content := req.System
		payload.Messages = append(payload.Messages, openRouterMessage{Role: "system", Content: &content})
	}

	for messageIndex, message := range req.Messages {
		if message.Role == "assistant" {
			converted, err := openRouterAssistantMessage(message, messageIndex)
			if err != nil {
				return openRouterRequest{}, err
			}
			if converted != nil {
				payload.Messages = append(payload.Messages, *converted)
			}
			continue
		}

		var textParts []string
		flushText := func() {
			if len(textParts) == 0 {
				return
			}
			content := strings.Join(textParts, "\n")
			payload.Messages = append(payload.Messages, openRouterMessage{Role: "user", Content: &content})
			textParts = nil
		}
		for _, block := range message.Content {
			switch block.Type {
			case "text":
				textParts = append(textParts, block.Text)
			case "tool_result":
				flushText()
				content := block.Content
				payload.Messages = append(payload.Messages, openRouterMessage{
					Role:       "tool",
					Content:    &content,
					ToolCallID: block.ToolUseID,
					Name:       block.ToolName,
				})
			}
		}
		flushText()
	}

	for _, tool := range req.Tools {
		payload.Tools = append(payload.Tools, openRouterTool{
			Type: "function",
			Function: openRouterToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema,
			},
		})
	}
	if len(payload.Tools) > 0 {
		payload.ToolChoice = "auto"
	}
	return payload, nil
}

func openRouterAssistantMessage(message Message, messageIndex int) (*openRouterMessage, error) {
	converted := &openRouterMessage{Role: "assistant"}
	var textParts []string
	for blockIndex, block := range message.Content {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "tool_use":
			arguments := block.Input
			if len(arguments) == 0 {
				arguments = json.RawMessage(`{}`)
			}
			if !json.Valid(arguments) {
				return nil, fmt.Errorf("openrouter: message %d block %d has invalid tool input JSON", messageIndex, blockIndex)
			}
			converted.ToolCalls = append(converted.ToolCalls, openRouterToolCall{
				ID:   block.ID,
				Type: "function",
				Function: openRouterToolCallFunction{
					Name:      block.Name,
					Arguments: string(arguments),
				},
			})
		case "provider_metadata":
			if !hasOpenRouterJSONValue(block.ProviderMetadata) {
				continue
			}
			if !json.Valid(block.ProviderMetadata) {
				return nil, fmt.Errorf("openrouter: message %d block %d has invalid provider metadata JSON", messageIndex, blockIndex)
			}
			converted.ReasoningDetails = append(json.RawMessage(nil), block.ProviderMetadata...)
		}
	}
	if len(textParts) > 0 {
		content := strings.Join(textParts, "\n")
		converted.Content = &content
	}
	if converted.Content == nil && len(converted.ToolCalls) == 0 && len(converted.ReasoningDetails) == 0 {
		return nil, nil
	}
	return converted, nil
}

type openRouterResponse struct {
	Choices []openRouterChoice `json:"choices"`
	Error   json.RawMessage    `json:"error"`
}

type openRouterChoice struct {
	Message      openRouterResponseMessage `json:"message"`
	FinishReason string                    `json:"finish_reason"`
	Error        json.RawMessage           `json:"error"`
}

type openRouterResponseMessage struct {
	Content          *string                      `json:"content"`
	ToolCalls        []openRouterResponseToolCall `json:"tool_calls"`
	ReasoningDetails json.RawMessage              `json:"reasoning_details"`
}

type openRouterResponseToolCall struct {
	ID       string                             `json:"id"`
	Type     string                             `json:"type"`
	Function openRouterResponseToolCallFunction `json:"function"`
}

type openRouterResponseToolCallFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (c *OpenRouterClient) decodeResponse(body []byte) (*Response, error) {
	var apiResponse openRouterResponse
	if err := json.Unmarshal(body, &apiResponse); err != nil {
		return nil, fmt.Errorf("openrouter: decode response: %w", err)
	}
	if hasOpenRouterJSONValue(apiResponse.Error) {
		return nil, fmt.Errorf("openrouter: API error: %s", c.apiErrorDetail(apiResponse.Error))
	}
	if len(apiResponse.Choices) == 0 {
		return nil, errors.New("openrouter: response contains no choices")
	}

	choice := apiResponse.Choices[0]
	if hasOpenRouterJSONValue(choice.Error) {
		return nil, fmt.Errorf("openrouter: choice error: %s", c.apiErrorDetail(choice.Error))
	}

	response := &Response{}
	if choice.Message.Content != nil && *choice.Message.Content != "" {
		response.Content = append(response.Content, ContentBlock{Type: "text", Text: *choice.Message.Content})
	}
	for index, toolCall := range choice.Message.ToolCalls {
		input, err := openRouterToolArguments(toolCall.Function.Arguments)
		if err != nil {
			return nil, fmt.Errorf("openrouter: decode tool call %d arguments: %w", index, err)
		}
		response.Content = append(response.Content, ContentBlock{
			Type:  "tool_use",
			ID:    toolCall.ID,
			Name:  toolCall.Function.Name,
			Input: input,
		})
	}
	if hasOpenRouterJSONValue(choice.Message.ReasoningDetails) {
		if !json.Valid(choice.Message.ReasoningDetails) {
			return nil, errors.New("openrouter: response contains invalid reasoning_details JSON")
		}
		response.Content = append(response.Content, ContentBlock{
			Type:             "provider_metadata",
			ProviderMetadata: append(json.RawMessage(nil), choice.Message.ReasoningDetails...),
		})
	}

	if len(choice.Message.ToolCalls) > 0 {
		response.StopReason = "tool_use"
	} else {
		response.StopReason = normalizeOpenRouterStopReason(choice.FinishReason)
	}
	return response, nil
}

func openRouterToolArguments(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage(`{}`), nil
	}
	if trimmed[0] == '"' {
		var arguments string
		if err := json.Unmarshal(trimmed, &arguments); err != nil {
			return nil, err
		}
		if arguments == "" {
			return json.RawMessage(`{}`), nil
		}
		if !json.Valid([]byte(arguments)) {
			return nil, errors.New("arguments string is not valid JSON")
		}
		return json.RawMessage(arguments), nil
	}
	if !json.Valid(trimmed) {
		return nil, errors.New("arguments are not valid JSON")
	}
	return append(json.RawMessage(nil), trimmed...), nil
}

func normalizeOpenRouterStopReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "content_filter", "refusal":
		return "refusal"
	default:
		return reason
	}
}

func isOpenRouterRetryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

func (c *OpenRouterClient) waitForRetry(ctx context.Context, attempt int, retryAfter string) error {
	delay, ok := parseOpenRouterRetryAfter(retryAfter, time.Now())
	if !ok {
		backoff := c.backoff
		if backoff == nil {
			backoff = openRouterBackoff
		}
		delay = backoff(attempt)
	}
	if delay < 0 {
		delay = 0
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("openrouter: retry wait canceled: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

func openRouterBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	exponent := attempt - 1
	if exponent > 5 {
		exponent = 5
	}
	base := time.Second * time.Duration(1<<exponent)
	jitter := time.Duration(rand.Int63n(int64(base/2) + 1))
	return base + jitter
}

func parseOpenRouterRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	delay := when.Sub(now)
	if delay < 0 {
		delay = 0
	}
	return delay, true
}

func readOpenRouterBody(body io.ReadCloser) ([]byte, bool, error) {
	defer body.Close()
	contents, err := io.ReadAll(io.LimitReader(body, maxOpenRouterResponseBody+1))
	if err != nil {
		return nil, false, err
	}
	if len(contents) > maxOpenRouterResponseBody {
		return contents[:maxOpenRouterResponseBody], true, nil
	}
	return contents, false, nil
}

func (c *OpenRouterClient) httpStatusError(response *http.Response, body []byte, tooLarge bool, attempts int) error {
	detail := ""
	if !tooLarge {
		var apiResponse openRouterResponse
		if json.Unmarshal(body, &apiResponse) == nil && hasOpenRouterJSONValue(apiResponse.Error) {
			detail = c.apiErrorDetail(apiResponse.Error)
		}
	}
	if detail == "" {
		detail = c.redactErrorDetail(string(body))
	}
	if tooLarge {
		detail = fmt.Sprintf("response body exceeds %d bytes", maxOpenRouterResponseBody)
	}
	if detail == "" {
		detail = "empty response body"
	}
	return fmt.Errorf("openrouter: HTTP %s after %d attempt(s): %s", response.Status, attempts, detail)
}

func (c *OpenRouterClient) apiErrorDetail(raw json.RawMessage) string {
	var object struct {
		Message string          `json:"message"`
		Code    json.RawMessage `json:"code"`
	}
	if json.Unmarshal(raw, &object) == nil && object.Message != "" {
		message := c.redactErrorDetail(object.Message)
		if hasOpenRouterJSONValue(object.Code) {
			return fmt.Sprintf("code %s: %s", c.redactErrorDetail(string(object.Code)), message)
		}
		return message
	}
	var message string
	if json.Unmarshal(raw, &message) == nil {
		return c.redactErrorDetail(message)
	}
	return c.redactErrorDetail(string(raw))
}

func (c *OpenRouterClient) redactErrorDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if c.apiKey != "" {
		detail = strings.ReplaceAll(detail, c.apiKey, "[REDACTED]")
	}
	if len(detail) > maxOpenRouterErrorDetail {
		detail = detail[:maxOpenRouterErrorDetail] + "…"
	}
	return detail
}

func hasOpenRouterJSONValue(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}
