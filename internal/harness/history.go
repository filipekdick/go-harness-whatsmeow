package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/filipekdick/go-harness-whatsmeow/internal/llm"
	"github.com/filipekdick/go-harness-whatsmeow/internal/store"
)

// buildSystem assembles the per-request system prompt:
// company prompt + hard security preamble + sender context + rolling summary.
// The security lines are informational for the model; real enforcement is
// role-gated tool definitions and company_id-scoped SQL.
func (h *Harness) buildSystem(company *store.Company, ident *identity, conv *store.Conversation) string {
	var b strings.Builder
	b.WriteString(company.SystemPrompt)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "You are the WhatsApp assistant of %q. You only have data for this one business; never speculate about other businesses or other customers.\n", company.Name)
	name := ident.user.DisplayName
	if name == "" {
		name = "unknown"
	}
	fmt.Fprintf(&b, "You are talking to a %s (name: %s, phone: %s). Reply in the sender's language, keep answers WhatsApp-short.\n",
		strings.ToLower(string(ident.effectiveRole)), name, ident.user.Phone)
	if ident.effectiveRole == store.RoleEmployee {
		b.WriteString("Write operations are two-phase: a write tool only PREPARES a change and returns a pending-write preview; nothing is committed until the employee explicitly confirms and you call confirm_write. Always relay the preview and wait for their answer in a later message.\n")
	}
	if conv.Summary != "" {
		b.WriteString("\nSummary of the earlier part of this conversation:\n")
		b.WriteString(conv.Summary)
		b.WriteString("\n")
	}
	return b.String()
}

// buildHistory loads the message tail after the summary watermark and maps it
// to API messages. Kinds map as: user -> user, assistant -> assistant,
// tool_result -> user (tool_result blocks).
func (h *Harness) buildHistory(ctx context.Context, conv *store.Conversation) ([]llm.Message, error) {
	rows, err := h.store.RecentMessages(ctx, conv.CompanyID, conv.ID, conv.SummaryUpto, h.cfg.HistoryTailLimit)
	if err != nil {
		return nil, err
	}
	// The API requires the first message to be a plain user turn; a tail cut
	// mid-tool-exchange would start with assistant or tool_result rows.
	start := 0
	for start < len(rows) && rows[start].Kind != "user" {
		start++
	}
	rows = rows[start:]

	var out []llm.Message
	for _, m := range rows {
		var content []llm.ContentBlock
		if err := json.Unmarshal(m.Content, &content); err != nil {
			return nil, fmt.Errorf("corrupt message %d: %w", m.ID, err)
		}
		role := "user"
		if m.Kind == "assistant" {
			role = "assistant"
		}
		out = append(out, llm.Message{Role: role, Content: content})
	}
	return out, nil
}

const summarizePrompt = `You maintain a rolling summary of a WhatsApp conversation between a business assistant and a contact.
Merge the EXISTING SUMMARY (may be empty) with the NEW MESSAGES into one updated summary.
Keep: open requests, decisions, confirmed orders/changes, preferences, names and quantities.
Drop: pleasantries and resolved back-and-forth. Answer with the summary text only, at most 300 words, in the language of the conversation.`

// maybeSummarize folds the oldest part of the tail into the rolling summary
// once the tail exceeds cfg.SummarizeAfter rows. Best effort: failures are
// logged and retried implicitly on a later message.
func (h *Harness) maybeSummarize(ctx context.Context, log *slog.Logger, conv *store.Conversation) {
	count, err := h.store.CountMessagesAfter(ctx, conv.CompanyID, conv.ID, conv.SummaryUpto)
	if err != nil || count <= h.cfg.SummarizeAfter {
		return
	}
	rows, err := h.store.OldestMessagesAfter(ctx, conv.CompanyID, conv.ID, conv.SummaryUpto, count-h.cfg.HistoryTailLimit)
	if err != nil || len(rows) == 0 {
		return
	}

	var transcript strings.Builder
	for _, m := range rows {
		var content []llm.ContentBlock
		if err := json.Unmarshal(m.Content, &content); err != nil {
			continue
		}
		for _, b := range content {
			switch b.Type {
			case "text":
				fmt.Fprintf(&transcript, "[%s] %s\n", m.Kind, b.Text)
			case "tool_use":
				fmt.Fprintf(&transcript, "[tool call] %s %s\n", b.Name, string(b.Input))
			case "tool_result":
				fmt.Fprintf(&transcript, "[tool result] %s\n", b.Content)
			}
		}
	}

	resp, err := h.llm.Complete(ctx, &llm.Request{
		System: summarizePrompt,
		Messages: []llm.Message{{Role: "user", Content: []llm.ContentBlock{llm.TextBlock(
			"EXISTING SUMMARY:\n" + conv.Summary + "\n\nNEW MESSAGES:\n" + transcript.String(),
		)}}},
		MaxTokens: 2048,
	})
	if err != nil {
		log.Warn("summarization failed", "err", err)
		return
	}
	newUpto := rows[len(rows)-1].ID
	if err := h.store.UpdateSummary(ctx, conv.CompanyID, conv.ID, resp.Text(), newUpto); err != nil {
		log.Warn("summary update failed", "err", err)
		return
	}
	log.Info("conversation summarized", "conv", conv.ID, "folded_rows", len(rows), "new_watermark", newUpto)
}
