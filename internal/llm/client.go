// Package llm defines a provider-neutral interface to a tool-calling LLM.
// The harness and stores only ever see these types; swapping providers means
// writing one new implementation of Client.
package llm

import (
	"context"
	"encoding/json"
)

// ContentBlock is the neutral representation of one API content block.
// It is also the shape persisted in messages.content (JSONB), so history
// replay is lossless — including tool calls and thinking blocks.
type ContentBlock struct {
	Type string `json:"type"` // text | thinking | redacted_thinking | tool_use | tool_result

	// text / thinking
	Text string `json:"text,omitempty"`
	// thinking blocks must be echoed back with their signature intact
	Signature string `json:"signature,omitempty"`
	// redacted_thinking
	Data string `json:"data,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

func TextBlock(s string) ContentBlock { return ContentBlock{Type: "text", Text: s} }

type Message struct {
	Role    string         `json:"role"` // user | assistant
	Content []ContentBlock `json:"content"`
}

// ToolDef describes one tool exposed to the model. InputSchema is a plain
// JSON Schema object ({"type":"object","properties":{...},"required":[...]}).
type ToolDef struct {
	Name        string
	Description string
	InputSchema map[string]any
}

type Request struct {
	System    string
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int
}

type Response struct {
	Content    []ContentBlock
	StopReason string // end_turn | tool_use | max_tokens | refusal | ...
}

// Text concatenates all text blocks of the response.
func (r *Response) Text() string {
	out := ""
	for _, b := range r.Content {
		if b.Type == "text" {
			if out != "" {
				out += "\n"
			}
			out += b.Text
		}
	}
	return out
}

// ToolUses returns the tool_use blocks of the response, in order.
func (r *Response) ToolUses() []ContentBlock {
	var out []ContentBlock
	for _, b := range r.Content {
		if b.Type == "tool_use" {
			out = append(out, b)
		}
	}
	return out
}

type Client interface {
	Complete(ctx context.Context, req *Request) (*Response, error)
}
