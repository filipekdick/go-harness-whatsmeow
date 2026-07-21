package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenRouterCompleteText(t *testing.T) {
	var received struct {
		Model               string              `json:"model"`
		Messages            []openRouterMessage `json:"messages"`
		MaxCompletionTokens int                 `json:"max_completion_tokens"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("HTTP-Referer"); got != "https://example.test" {
			t.Errorf("HTTP-Referer = %q", got)
		}
		if got := r.Header.Get("X-Title"); got != "Harness" {
			t.Errorf("X-Title = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Olá!"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	client := NewOpenRouterClient("test-key", "openai/test-model", 1, "https://example.test", "Harness")
	client.baseURL = server.URL + "/api/v1/chat/completions"
	client.httpClient = server.Client()

	response, err := client.Complete(context.Background(), &Request{
		System:    "Responda em português.",
		Messages:  []Message{{Role: "user", Content: []ContentBlock{TextBlock("Oi")}}},
		MaxTokens: 321,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if received.Model != "openai/test-model" {
		t.Errorf("model = %q", received.Model)
	}
	if received.MaxCompletionTokens != 321 {
		t.Errorf("max_completion_tokens = %d", received.MaxCompletionTokens)
	}
	if len(received.Messages) != 2 || received.Messages[0].Role != "system" || received.Messages[1].Role != "user" {
		t.Fatalf("messages = %#v", received.Messages)
	}
	if received.Messages[0].Content == nil || *received.Messages[0].Content != "Responda em português." {
		t.Errorf("system content = %#v", received.Messages[0].Content)
	}
	if received.Messages[1].Content == nil || *received.Messages[1].Content != "Oi" {
		t.Errorf("user content = %#v", received.Messages[1].Content)
	}
	if response.StopReason != "end_turn" {
		t.Errorf("StopReason = %q", response.StopReason)
	}
	if got := response.Text(); got != "Olá!" {
		t.Errorf("Text() = %q", got)
	}
}

func TestOpenRouterCompleteToolCall(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"city": map[string]any{"type": "string"},
		},
		"required": []any{"city"},
	}
	var received openRouterRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{
				"message":{
					"content":"Vou consultar.",
					"tool_calls":[{
						"id":"call_1",
						"type":"function",
						"function":{"name":"weather","arguments":"{\"city\":\"Recife\"}"}
					}]
				},
				"finish_reason":"stop"
			}]
		}`))
	}))
	defer server.Close()

	client := NewOpenRouterClient("key", "model", 1, "", "")
	client.baseURL = server.URL
	client.httpClient = server.Client()
	response, err := client.Complete(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: []ContentBlock{TextBlock("Como está o tempo?")}}},
		Tools: []ToolDef{{
			Name:        "weather",
			Description: "Consulta o tempo",
			InputSchema: schema,
		}},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if received.ToolChoice != "auto" {
		t.Errorf("tool_choice = %q", received.ToolChoice)
	}
	if len(received.Tools) != 1 {
		t.Fatalf("tools = %#v", received.Tools)
	}
	if !reflect.DeepEqual(received.Tools[0].Function.Parameters, schema) {
		t.Errorf("parameters = %#v, want %#v", received.Tools[0].Function.Parameters, schema)
	}
	if response.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, want tool_use", response.StopReason)
	}
	if len(response.Content) != 2 || response.Content[0].Type != "text" || response.Content[1].Type != "tool_use" {
		t.Fatalf("Content = %#v", response.Content)
	}
	toolUse := response.Content[1]
	if toolUse.ID != "call_1" || toolUse.Name != "weather" || string(toolUse.Input) != `{"city":"Recife"}` {
		t.Errorf("tool use = %#v", toolUse)
	}
}

func TestOpenRouterReplaysToolResultAndReasoningDetails(t *testing.T) {
	reasoning := json.RawMessage(`[{"type":"reasoning.encrypted","data":"opaque"}]`)
	var received openRouterRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{
				"message":{
					"content":null,
					"reasoning_details":[{"type":"reasoning.encrypted","data":"next"}]
				},
				"finish_reason":"stop"
			}]
		}`))
	}))
	defer server.Close()

	client := NewOpenRouterClient("key", "model", 1, "", "")
	client.baseURL = server.URL
	client.httpClient = server.Client()
	response, err := client.Complete(context.Background(), &Request{
		Messages: []Message{
			{
				Role: "assistant",
				Content: []ContentBlock{
					{Type: "provider_metadata", ProviderMetadata: reasoning},
					{Type: "tool_use", ID: "call_7", Name: "lookup", Input: json.RawMessage(`{"id":7}`)},
				},
			},
			{
				Role: "user",
				Content: []ContentBlock{{
					Type:      "tool_result",
					ToolUseID: "call_7",
					ToolName:  "lookup",
					Content:   `{"result":"ok"}`,
				}},
			},
		},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if len(received.Messages) != 2 {
		t.Fatalf("messages = %#v", received.Messages)
	}
	assistant := received.Messages[0]
	if assistant.Role != "assistant" || !reflect.DeepEqual(assistant.ReasoningDetails, reasoning) {
		t.Errorf("assistant = %#v", assistant)
	}
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Function.Name != "lookup" {
		t.Fatalf("assistant tool calls = %#v", assistant.ToolCalls)
	}
	if got := string(assistant.ToolCalls[0].Function.Arguments); got != `{"id":7}` {
		t.Errorf("arguments = %q", got)
	}
	toolResult := received.Messages[1]
	if toolResult.Role != "tool" || toolResult.ToolCallID != "call_7" || toolResult.Name != "lookup" {
		t.Errorf("tool result = %#v", toolResult)
	}
	if toolResult.Content == nil || *toolResult.Content != `{"result":"ok"}` {
		t.Errorf("tool result content = %#v", toolResult.Content)
	}

	if response.StopReason != "end_turn" || len(response.Content) != 1 {
		t.Fatalf("response = %#v", response)
	}
	metadata := response.Content[0]
	if metadata.Type != "provider_metadata" || string(metadata.ProviderMetadata) != `[{"type":"reasoning.encrypted","data":"next"}]` {
		t.Errorf("metadata = %#v", metadata)
	}
}

func TestOpenRouterRetries429(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited","code":429}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	client := NewOpenRouterClient("key", "model", 2, "", "")
	client.baseURL = server.URL
	client.httpClient = server.Client()
	client.backoff = func(int) time.Duration {
		t.Fatal("backoff used despite Retry-After")
		return 0
	}

	response, err := client.Complete(context.Background(), &Request{MaxTokens: 10})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if attempts.Load() != 2 {
		t.Errorf("attempts = %d, want 2", attempts.Load())
	}
	if response.Text() != "ok" {
		t.Errorf("response text = %q", response.Text())
	}
}

func TestOpenRouterDoesNotRetry400(t *testing.T) {
	const apiKey = "secret-key-must-not-leak"
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key secret-key-must-not-leak","code":400}}`))
	}))
	defer server.Close()

	client := NewOpenRouterClient(apiKey, "model", 3, "", "")
	client.baseURL = server.URL
	client.httpClient = server.Client()
	client.backoff = func(int) time.Duration {
		t.Fatal("backoff called for HTTP 400")
		return 0
	}

	_, err := client.Complete(context.Background(), &Request{MaxTokens: 10})
	if err == nil {
		t.Fatal("Complete returned nil error")
	}
	if attempts.Load() != 1 {
		t.Errorf("attempts = %d, want 1", attempts.Load())
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Errorf("error leaks API key: %v", err)
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "invalid key") {
		t.Errorf("error is not informative: %v", err)
	}
}

func TestOpenRouterRetryWaitIsCancelable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"retry"}}`))
	}))
	defer server.Close()

	backoffStarted := make(chan struct{})
	client := NewOpenRouterClient("key", "model", 3, "", "")
	client.baseURL = server.URL
	client.httpClient = server.Client()
	client.backoff = func(int) time.Duration {
		close(backoffStarted)
		return time.Hour
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.Complete(ctx, &Request{MaxTokens: 10})
		result <- err
	}()

	select {
	case <-backoffStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("retry backoff did not start")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Complete did not stop after context cancellation")
	}
}

func TestOpenRouterTreatsErrorsInSuccessfulHTTPResponse(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "top-level", body: `{"error":{"message":"provider failed","code":500}}`},
		{name: "choice", body: `{"choices":[{"error":{"message":"generation failed"},"message":{},"finish_reason":"stop"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			client := NewOpenRouterClient("key", "model", 3, "", "")
			client.baseURL = server.URL
			client.httpClient = server.Client()
			_, err := client.Complete(context.Background(), &Request{MaxTokens: 10})
			if err == nil || !strings.Contains(err.Error(), "failed") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
