package wa

import (
	"fmt"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/filipekdick/go-harness-whatsmeow/internal/harness"
	"github.com/filipekdick/go-harness-whatsmeow/internal/store"
)

// eventHandler returns the per-client WhatsMeow event callback, bound to the
// (company, channel) that owns the client. The channel binding is what makes
// the receiving line a trustworthy tenant/audience signal.
func (m *SessionManager) eventHandler(companyID int64, channel store.Channel) func(any) {
	return func(evt any) {
		switch v := evt.(type) {
		case *events.Message:
			m.onMessage(companyID, channel, v)
		case *events.LoggedOut:
			m.log.Error("whatsapp line logged out — re-pair required",
				"company", companyID, "channel", channel, "reason", v.Reason)
		case *events.Disconnected:
			m.log.Warn("whatsapp line disconnected (auto-reconnect will retry)",
				"company", companyID, "channel", channel)
		case *events.Connected:
			m.log.Info("whatsapp line online", "company", companyID, "channel", channel)
		}
	}
}

func (m *SessionManager) onMessage(companyID int64, channel store.Channel, evt *events.Message) {
	info := evt.Info

	// Only serve direct 1:1 chats: no groups, broadcasts, newsletters or our
	// own outgoing messages (which also arrive as events).
	if info.IsFromMe || info.IsGroup || info.Chat.Server != types.DefaultUserServer {
		return
	}

	text := extractText(evt.Message)
	if text == "" {
		m.log.Debug("ignoring non-text message",
			"company", companyID, "channel", channel, "chat", info.Chat.String())
		return
	}

	phone := senderPhone(info)
	if phone == "" {
		m.log.Warn("could not resolve sender phone number; dropping message",
			"company", companyID, "channel", channel, "sender", info.Sender.String())
		return
	}

	if m.OnMessage == nil {
		m.log.Error("OnMessage not wired; dropping message")
		return
	}
	m.OnMessage(harness.Inbound{
		CompanyID:   companyID,
		Channel:     channel,
		SenderPhone: phone,
		SenderName:  info.PushName,
		ChatJID:     info.Chat.String(),
		// Scope the dedup key by chat: WhatsApp message IDs are only unique
		// per sender.
		WAMessageID: fmt.Sprintf("%s|%s", info.Chat.String(), info.ID),
		Text:        text,
	})
}

// senderPhone returns the sender's phone number (E.164 digits, no '+').
// WhatsApp increasingly uses anonymized @lid JIDs; prefer whichever of
// Sender/SenderAlt is a phone-number JID.
func senderPhone(info types.MessageInfo) string {
	if info.Sender.Server == types.DefaultUserServer {
		return info.Sender.User
	}
	if info.SenderAlt.Server == types.DefaultUserServer {
		return info.SenderAlt.User
	}
	return ""
}

// extractText pulls the text out of the supported message shapes: plain
// conversation, extended text (links/replies), and media captions. Unwraps
// ephemeral/view-once containers first.
func extractText(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	// Unwrap containers (disappearing messages etc.).
	for {
		switch {
		case msg.GetEphemeralMessage().GetMessage() != nil:
			msg = msg.GetEphemeralMessage().GetMessage()
		case msg.GetViewOnceMessage().GetMessage() != nil:
			msg = msg.GetViewOnceMessage().GetMessage()
		case msg.GetDocumentWithCaptionMessage().GetMessage() != nil:
			msg = msg.GetDocumentWithCaptionMessage().GetMessage()
		default:
			goto unwrapped
		}
	}
unwrapped:
	if t := msg.GetConversation(); t != "" {
		return t
	}
	if t := msg.GetExtendedTextMessage().GetText(); t != "" {
		return t
	}
	if t := msg.GetImageMessage().GetCaption(); t != "" {
		return t
	}
	if t := msg.GetVideoMessage().GetCaption(); t != "" {
		return t
	}
	if t := msg.GetDocumentMessage().GetCaption(); t != "" {
		return t
	}
	return ""
}
