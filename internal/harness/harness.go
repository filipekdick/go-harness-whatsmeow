// Package harness implements the tool-calling loop: one inbound WhatsApp
// message in, one (or more) LLM round-trips with company- and role-scoped
// tools, one reply out.
package harness

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/filipekdick/go-harness-whatsmeow/internal/config"
	"github.com/filipekdick/go-harness-whatsmeow/internal/llm"
	"github.com/filipekdick/go-harness-whatsmeow/internal/store"
	"github.com/filipekdick/go-harness-whatsmeow/internal/tools"
)

// Sender delivers an outbound text on a company's line. Implemented by the
// WhatsMeow SessionManager (stage 3).
type Sender interface {
	SendText(ctx context.Context, companyID int64, channel store.Channel, chatJID, text string) error
}

// Inbound is one received WhatsApp message, already attributed to a company
// and channel by the session layer (each linked device belongs to exactly
// one (company, channel) pair).
type Inbound struct {
	CompanyID   int64
	Channel     store.Channel
	SenderPhone string // E.164 digits, no '+'
	SenderName  string // WhatsApp push name, best effort
	ChatJID     string
	WAMessageID string
	Text        string
}

// Reply texts sent when the model can't (or shouldn't) answer. Adjust to the
// language of your customer base; company-specific wording can later move to
// business_rules.
const (
	replyEscalated = "I wasn't able to resolve this automatically, so I've passed the conversation to a member of our team. They'll get back to you shortly."
	replyFailed    = "Sorry, something went wrong on our side. Please try again in a moment."
	replyUnknown   = "This number is reserved for registered staff. If you're a customer, please contact us on our main number."

	processTimeout = 5 * time.Minute
)

type Harness struct {
	store  *store.Store
	llm    llm.Client
	tools  *tools.Registry
	sender Sender
	cfg    *config.Config
	log    *slog.Logger
	pool   *workerPool
}

func New(st *store.Store, client llm.Client, reg *tools.Registry, sender Sender, cfg *config.Config, log *slog.Logger) *Harness {
	if log == nil {
		log = slog.Default()
	}
	h := &Harness{store: st, llm: client, tools: reg, sender: sender, cfg: cfg, log: log}
	h.pool = newWorkerPool(cfg.WorkerCount, cfg.WorkerQueueSize, h.process)
	return h
}

// Start launches the worker pool. Workers stop when ctx is canceled.
func (h *Harness) Start(ctx context.Context) { h.pool.start(ctx) }

// Enqueue hands an inbound message to its conversation's serial worker.
// Returns false when that worker's queue is full (message dropped + logged);
// WhatsApp users will simply resend.
func (h *Harness) Enqueue(msg Inbound) bool {
	ok := h.pool.dispatch(msg)
	if !ok {
		h.log.Error("worker queue full, dropping message",
			"company", msg.CompanyID, "chat", msg.ChatJID)
	}
	return ok
}

// process runs the full pipeline for one inbound message. It is invoked
// serially per conversation by the worker pool.
func (h *Harness) process(parent context.Context, msg Inbound) {
	ctx, cancel := context.WithTimeout(parent, processTimeout)
	defer cancel()

	log := h.log.With("company", msg.CompanyID, "channel", msg.Channel, "chat", msg.ChatJID)

	// 1. Deduplicate: WhatsApp redelivers.
	first, err := h.store.FirstTimeProcessing(ctx, msg.CompanyID, msg.WAMessageID)
	if err != nil {
		log.Error("dedup check failed", "err", err)
		return
	}
	if !first {
		log.Debug("duplicate message dropped", "wa_id", msg.WAMessageID)
		return
	}

	// 2. Resolve identity: phone + receiving channel -> user + effective role.
	ident, refusal, err := h.resolveIdentity(ctx, msg)
	if err != nil {
		log.Error("identity resolution failed", "err", err)
		h.reply(ctx, msg, replyFailed)
		return
	}
	if refusal != "" {
		h.reply(ctx, msg, refusal)
		return
	}
	log = log.With("user", ident.user.ID, "role", ident.effectiveRole)

	company, err := h.store.GetCompany(ctx, msg.CompanyID)
	if err != nil {
		log.Error("company lookup failed", "err", err)
		return
	}

	// 3. Conversation + persist the inbound message.
	conv, err := h.store.GetOrCreateConversation(ctx, msg.CompanyID, ident.user.ID, msg.Channel, msg.ChatJID)
	if err != nil {
		log.Error("conversation lookup failed", "err", err)
		h.reply(ctx, msg, replyFailed)
		return
	}
	inboundID, err := h.store.AppendMessage(ctx, msg.CompanyID, conv.ID, "user",
		[]llm.ContentBlock{llm.TextBlock(msg.Text)}, &msg.WAMessageID)
	if err != nil {
		log.Error("persist inbound failed", "err", err)
		h.reply(ctx, msg, replyFailed)
		return
	}

	// 4. Build the API payload: system + summary + recent history, and the
	// tool definitions for THIS role only.
	messages, err := h.buildHistory(ctx, conv)
	if err != nil {
		log.Error("history build failed", "err", err)
		h.reply(ctx, msg, replyFailed)
		return
	}
	system := h.buildSystem(company, ident, conv)
	defs := h.tools.DefsFor(ident.effectiveRole)

	env := &tools.Env{
		CompanyID:        msg.CompanyID,
		User:             ident.user,
		EffectiveRole:    ident.effectiveRole,
		Channel:          msg.Channel,
		ConversationID:   conv.ID,
		InboundMessageID: inboundID,
		Store:            h.store,
	}

	// 5. The tool loop.
	final, escalate := h.runToolLoop(ctx, log, env, conv, system, messages, defs)

	if escalate {
		h.autoEscalate(ctx, log, msg, conv, "tool loop exceeded "+
			fmt.Sprint(h.cfg.MaxToolIterations)+" iterations; last user message: "+msg.Text)
		return
	}
	if final == "" {
		final = replyFailed
	}
	h.reply(ctx, msg, final)

	if err := h.store.TouchConversation(ctx, msg.CompanyID, conv.ID); err != nil {
		log.Warn("touch conversation failed", "err", err)
	}
	// 6. Keep the context window bounded (best effort, after replying).
	h.maybeSummarize(ctx, log, conv)
}

// runToolLoop drives the LLM until it produces a final text answer or the
// iteration cap is hit. Returns (finalText, escalate).
func (h *Harness) runToolLoop(ctx context.Context, log *slog.Logger, env *tools.Env,
	conv *store.Conversation, system string, messages []llm.Message, defs []llm.ToolDef) (string, bool) {

	for i := 0; i < h.cfg.MaxToolIterations; i++ {
		resp, err := h.llm.Complete(ctx, &llm.Request{
			System:    system,
			Messages:  messages,
			Tools:     defs,
			MaxTokens: h.cfg.MaxTokens,
		})
		if err != nil {
			// Retries already happened inside the client.
			log.Error("llm call failed", "err", err, "iteration", i)
			return "", false
		}

		// Persist the assistant turn verbatim (including tool_use and
		// thinking blocks) so replay is lossless.
		if _, err := h.store.AppendMessage(ctx, env.CompanyID, conv.ID, "assistant", resp.Content, nil); err != nil {
			log.Error("persist assistant turn failed", "err", err)
			return "", false
		}
		messages = append(messages, llm.Message{Role: "assistant", Content: resp.Content})

		if resp.StopReason != "tool_use" {
			if resp.StopReason == "refusal" {
				log.Warn("model refused", "iteration", i)
				return replyFailed, false
			}
			return resp.Text(), false
		}

		// Execute every requested tool; all results go back in ONE user turn.
		var results []llm.ContentBlock
		for _, call := range resp.ToolUses() {
			out, isErr := h.tools.Execute(ctx, env, call.Name, call.Input)
			log.Info("tool executed", "tool", call.Name, "is_error", isErr, "iteration", i)
			results = append(results, llm.ContentBlock{
				Type: "tool_result", ToolUseID: call.ID, Content: out, IsError: isErr,
			})
		}
		if _, err := h.store.AppendMessage(ctx, env.CompanyID, conv.ID, "tool_result", results, nil); err != nil {
			log.Error("persist tool results failed", "err", err)
			return "", false
		}
		messages = append(messages, llm.Message{Role: "user", Content: results})
	}
	return "", true
}

func (h *Harness) autoEscalate(ctx context.Context, log *slog.Logger, msg Inbound, conv *store.Conversation, summary string) {
	if _, err := h.store.CreateEscalation(ctx, msg.CompanyID, conv.ID, summary); err != nil {
		log.Error("auto-escalation insert failed", "err", err)
	}
	if err := h.store.SetEscalated(ctx, msg.CompanyID, conv.ID); err != nil {
		log.Error("mark escalated failed", "err", err)
	}
	h.reply(ctx, msg, replyEscalated)
	if _, err := h.store.AppendMessage(ctx, msg.CompanyID, conv.ID, "system_note",
		[]llm.ContentBlock{llm.TextBlock("auto-escalated: " + summary)}, nil); err != nil {
		log.Warn("persist escalation note failed", "err", err)
	}
}

func (h *Harness) reply(ctx context.Context, msg Inbound, text string) {
	if err := h.sender.SendText(ctx, msg.CompanyID, msg.Channel, msg.ChatJID, text); err != nil {
		h.log.Error("send reply failed", "company", msg.CompanyID, "chat", msg.ChatJID, "err", err)
	}
}
