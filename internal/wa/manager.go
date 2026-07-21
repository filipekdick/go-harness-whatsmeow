// Package wa is the WhatsMeow session layer: one client per wa_channels row,
// QR pairing on first run, inbound text → harness, outbound text ← harness.
//
// MVP scope: direct chats and plain text only. Groups, media, reactions and
// edits are ignored.
package wa

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"go.mau.fi/whatsmeow"
	wstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/filipekdick/go-harness-whatsmeow/internal/harness"
	"github.com/filipekdick/go-harness-whatsmeow/internal/store"
	"go.mau.fi/whatsmeow/proto/waE2E"

	"github.com/mdp/qrterminal/v3"
)

// Enqueuer receives attributed inbound messages. Implemented by *harness.Harness.
type Enqueuer interface {
	Enqueue(harness.Inbound) bool
}

type sessionKey struct {
	companyID int64
	channel   store.Channel
}

// Manager holds one WhatsMeow client per active wa_channels row and
// implements harness.Sender for outbound replies.
type Manager struct {
	store     *store.Store
	container *sqlstore.Container
	log       *slog.Logger
	enq       Enqueuer

	mu       sync.RWMutex
	sessions map[sessionKey]*whatsmeow.Client
}

// NewManager opens the WhatsMeow device store in the same Postgres database
// (whatsmeow_* tables, created/upgraded automatically).
func NewManager(ctx context.Context, databaseURL string, st *store.Store, log *slog.Logger) (*Manager, error) {
	if log == nil {
		log = slog.Default()
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open whatsmeow db: %w", err)
	}
	container := sqlstore.NewWithDB(db, "postgres", waLog.Stdout("wadb", "WARN", false))
	if err := container.Upgrade(ctx); err != nil {
		return nil, fmt.Errorf("upgrade whatsmeow schema: %w", err)
	}
	return &Manager{
		store:     st,
		container: container,
		log:       log,
		sessions:  map[sessionKey]*whatsmeow.Client{},
	}, nil
}

// SetEnqueuer breaks the construction cycle: the harness needs the manager as
// Sender, the manager needs the harness to enqueue. Call before Start.
func (m *Manager) SetEnqueuer(e Enqueuer) { m.enq = e }

// Start connects one client per active line, sequentially, so that QR codes
// for unpaired lines never interleave on the terminal.
func (m *Manager) Start(ctx context.Context) error {
	if m.enq == nil {
		return fmt.Errorf("wa: SetEnqueuer must be called before Start")
	}
	channels, err := m.store.ListActiveChannels(ctx)
	if err != nil {
		return fmt.Errorf("list wa channels: %w", err)
	}
	if len(channels) == 0 {
		return fmt.Errorf("no active rows in wa_channels; insert a company and its lines first")
	}
	for _, ch := range channels {
		if err := m.connect(ctx, ch); err != nil {
			return fmt.Errorf("connect company %d %s line: %w", ch.CompanyID, ch.Channel, err)
		}
		m.log.Info("line connected", "company", ch.CompanyID, "channel", ch.Channel)
	}
	return nil
}

// Stop disconnects every client.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.sessions {
		c.Disconnect()
	}
}

// SendText implements harness.Sender.
func (m *Manager) SendText(ctx context.Context, companyID int64, channel store.Channel, chatJID, text string) error {
	m.mu.RLock()
	client := m.sessions[sessionKey{companyID, channel}]
	m.mu.RUnlock()
	if client == nil {
		return fmt.Errorf("no session for company %d channel %s", companyID, channel)
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("parse chat JID %q: %w", chatJID, err)
	}
	_, err = client.SendMessage(ctx, jid, &waE2E.Message{Conversation: proto.String(text)})
	return err
}

func (m *Manager) connect(ctx context.Context, ch store.WAChannel) error {
	var device *wstore.Device
	if ch.DeviceJID != "" {
		jid, err := types.ParseJID(ch.DeviceJID)
		if err != nil {
			return fmt.Errorf("bad stored device JID %q: %w", ch.DeviceJID, err)
		}
		device, err = m.container.GetDevice(ctx, jid)
		if err != nil {
			return err
		}
		if device == nil {
			m.log.Warn("stored device not found in whatsmeow store; re-pairing",
				"company", ch.CompanyID, "channel", ch.Channel, "jid", ch.DeviceJID)
		}
	}
	if device == nil {
		device = m.container.NewDevice()
	}

	client := whatsmeow.NewClient(device, waLog.Stdout("wa", "WARN", false))
	client.AddEventHandler(m.eventHandler(ch))

	if client.Store.ID == nil {
		if err := m.pair(ctx, ch, client); err != nil {
			return err
		}
	} else if err := client.Connect(); err != nil {
		return err
	}

	m.mu.Lock()
	m.sessions[sessionKey{ch.CompanyID, ch.Channel}] = client
	m.mu.Unlock()
	return nil
}

// pair runs the interactive QR flow on the terminal and persists the linked
// device JID into wa_channels on success.
func (m *Manager) pair(ctx context.Context, ch store.WAChannel, client *whatsmeow.Client) error {
	qrChan, err := client.GetQRChannel(ctx)
	if err != nil {
		return err
	}
	if err := client.Connect(); err != nil {
		return err
	}
	fmt.Printf("\n=== Pairing: company %d, %s line ===\n", ch.CompanyID, ch.Channel)
	fmt.Println("On the phone for THIS line: WhatsApp > Linked devices > Link a device")
	for item := range qrChan {
		switch item.Event {
		case "code":
			qrterminal.GenerateHalfBlock(item.Code, qrterminal.L, os.Stdout)
		case "success":
			// Loop ends: the channel closes right after a terminal event.
		default:
			return fmt.Errorf("pairing failed: %s (%v)", item.Event, item.Error)
		}
	}
	if client.Store.ID == nil {
		return fmt.Errorf("pairing did not complete")
	}
	jid := client.Store.ID.String()
	if err := m.store.SetChannelDeviceJID(ctx, ch.CompanyID, ch.Channel, jid); err != nil {
		return fmt.Errorf("persist device JID: %w", err)
	}
	fmt.Printf("=== Paired as %s ===\n\n", jid)
	return nil
}

func (m *Manager) eventHandler(ch store.WAChannel) whatsmeow.EventHandler {
	return func(evt any) {
		switch e := evt.(type) {
		case *events.Message:
			m.onMessage(ch, e)
		case *events.LoggedOut:
			m.log.Error("device logged out; delete wa_device_jid and restart to re-pair",
				"company", ch.CompanyID, "channel", ch.Channel, "reason", e.Reason)
		}
	}
}

func (m *Manager) onMessage(ch store.WAChannel, evt *events.Message) {
	info := evt.Info
	if info.IsFromMe || info.IsGroup {
		return
	}
	// Direct chats only: phone-number or LID addressed.
	if info.Chat.Server != types.DefaultUserServer && info.Chat.Server != types.HiddenUserServer {
		return
	}
	text := extractText(evt.Message)
	if text == "" {
		return
	}
	phone := senderPhone(info)
	if phone == "" {
		m.log.Warn("cannot resolve sender phone number, dropping",
			"company", ch.CompanyID, "sender", info.Sender.String())
		return
	}
	m.enq.Enqueue(harness.Inbound{
		CompanyID:   ch.CompanyID,
		Channel:     ch.Channel,
		SenderPhone: phone,
		SenderName:  info.PushName,
		ChatJID:     info.Chat.String(),
		WAMessageID: string(info.ID),
		Text:        text,
	})
}

// senderPhone returns the sender's E.164 digits (no '+'). With LID
// addressing the phone number lives in SenderAlt.
func senderPhone(info types.MessageInfo) string {
	if info.Sender.Server == types.DefaultUserServer {
		return info.Sender.User
	}
	if info.SenderAlt.Server == types.DefaultUserServer {
		return info.SenderAlt.User
	}
	return ""
}

func extractText(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if t := msg.GetConversation(); t != "" {
		return t
	}
	if et := msg.GetExtendedTextMessage(); et != nil {
		return et.GetText()
	}
	return ""
}
