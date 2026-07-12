// Package wa owns WhatsApp connectivity: one WhatsMeow client per active
// wa_channels row, i.e. per (company, CUSTOMER|EMPLOYEE) line. It implements
// harness.Sender for outbound text and feeds inbound messages into the
// harness via the OnMessage callback.
package wa

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow/proto/waE2E"

	"github.com/mdp/qrterminal/v3"

	"github.com/filipekdick/go-harness-whatsmeow/internal/harness"
	"github.com/filipekdick/go-harness-whatsmeow/internal/store"

	// database/sql driver for whatsmeow's sqlstore ("pgx" driver name).
	_ "github.com/jackc/pgx/v5/stdlib"
)

type channelKey struct {
	companyID int64
	channel   store.Channel
}

type SessionManager struct {
	container *sqlstore.Container
	st        *store.Store
	log       *slog.Logger
	waLog     waLog.Logger

	// OnMessage receives every accepted inbound text message. Set it before
	// calling Connect. It must not block (the harness enqueues internally).
	OnMessage func(harness.Inbound) bool

	mu      sync.RWMutex
	clients map[channelKey]*whatsmeow.Client
}

// NewSessionManager opens (and migrates) whatsmeow's own device store inside
// the same Postgres database used by the harness.
func NewSessionManager(ctx context.Context, databaseURL string, st *store.Store, log *slog.Logger) (*SessionManager, error) {
	if log == nil {
		log = slog.Default()
	}
	wlog := waLog.Stdout("whatsmeow", "WARN", false)
	container, err := sqlstore.New(ctx, "pgx", databaseURL, wlog)
	if err != nil {
		return nil, fmt.Errorf("open whatsmeow sqlstore: %w", err)
	}
	return &SessionManager{
		container: container,
		st:        st,
		log:       log,
		waLog:     wlog,
		clients:   map[channelKey]*whatsmeow.Client{},
	}, nil
}

// Connect brings up one client per paired channel. Channels without a linked
// device are skipped with a warning (pair them with the `pair` subcommand).
// WhatsMeow auto-reconnects dropped connections on its own.
func (m *SessionManager) Connect(ctx context.Context) error {
	channels, err := m.st.ListWAChannels(ctx)
	if err != nil {
		return fmt.Errorf("list channels: %w", err)
	}
	connected := 0
	for _, ch := range channels {
		log := m.log.With("company", ch.CompanyID, "channel", ch.Channel)
		if ch.DeviceJID == nil {
			log.Warn("channel has no linked device yet; run: harness pair <company_id> <channel>")
			continue
		}
		jid, err := types.ParseJID(*ch.DeviceJID)
		if err != nil {
			log.Error("stored device JID is invalid", "jid", *ch.DeviceJID, "err", err)
			continue
		}
		device, err := m.container.GetDevice(ctx, jid)
		if err != nil || device == nil {
			log.Error("device not found in whatsmeow store; re-pair this channel", "err", err)
			continue
		}
		client := whatsmeow.NewClient(device, m.waLog)
		client.AddEventHandler(m.eventHandler(ch.CompanyID, ch.Channel))
		if err := client.Connect(); err != nil {
			log.Error("connect failed", "err", err)
			continue
		}
		m.mu.Lock()
		m.clients[channelKey{ch.CompanyID, ch.Channel}] = client
		m.mu.Unlock()
		connected++
		log.Info("whatsapp line connected", "jid", jid.String())
	}
	if connected == 0 {
		return fmt.Errorf("no WhatsApp line could be connected (paired channels: run `harness pair` first)")
	}
	return nil
}

// Pair links a new WhatsApp device to a (company, channel) line by printing
// a QR code on the terminal. Scan it with the phone that owns that number
// (WhatsApp > Linked devices). Blocks until pairing succeeds or ctx expires.
func (m *SessionManager) Pair(ctx context.Context, companyID int64, channel store.Channel) error {
	ch, err := m.st.GetWAChannel(ctx, companyID, channel)
	if err != nil {
		return err
	}
	if ch == nil {
		return fmt.Errorf("no wa_channels row for company %d channel %s (run add-company first)", companyID, channel)
	}
	if ch.DeviceJID != nil {
		return fmt.Errorf("channel already paired to %s; clear wa_channels.wa_device_jid to re-pair", *ch.DeviceJID)
	}

	device := m.container.NewDevice()
	client := whatsmeow.NewClient(device, m.waLog)

	qrChan, err := client.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("qr channel: %w", err)
	}
	if err := client.Connect(); err != nil {
		return fmt.Errorf("connect for pairing: %w", err)
	}
	defer client.Disconnect()

	for evt := range qrChan {
		switch evt.Event {
		case "code":
			fmt.Printf("\nScan this QR with the %s-line phone of company %d:\n\n", channel, companyID)
			qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
		case "success":
			jid := client.Store.ID.String()
			if err := m.st.SetWAChannelDevice(ctx, companyID, channel, jid); err != nil {
				return fmt.Errorf("pairing succeeded but saving JID failed: %w", err)
			}
			fmt.Printf("\nPaired %s line of company %d as %s\n", channel, companyID, jid)
			// Stay connected briefly so key uploads and the initial sync
			// settle before we drop the connection.
			time.Sleep(10 * time.Second)
			return nil
		case "timeout":
			return fmt.Errorf("QR code timed out; run pair again")
		}
	}
	return fmt.Errorf("pairing did not complete")
}

// SendText implements harness.Sender. Replies always leave through the same
// (company, channel) line the message arrived on.
func (m *SessionManager) SendText(ctx context.Context, companyID int64, channel store.Channel, chatJID, text string) error {
	m.mu.RLock()
	client := m.clients[channelKey{companyID, channel}]
	m.mu.RUnlock()
	if client == nil {
		return fmt.Errorf("no connected client for company %d channel %s", companyID, channel)
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("parse chat jid %q: %w", chatJID, err)
	}
	_, err = client.SendMessage(ctx, jid, &waE2E.Message{Conversation: proto.String(text)})
	return err
}

// DisconnectAll cleanly drops every line (call on shutdown).
func (m *SessionManager) DisconnectAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, client := range m.clients {
		client.Disconnect()
		delete(m.clients, key)
	}
}
