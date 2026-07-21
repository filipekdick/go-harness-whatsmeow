package store

import "context"

// WAChannel is one WhatsApp line of a company — a linked (or to-be-linked)
// WhatsMeow device.
type WAChannel struct {
	ID        int64
	CompanyID int64
	Channel   Channel
	DeviceJID string // empty until QR pairing completes
}

// ListActiveChannels returns every active line across all companies. This is
// the one intentionally cross-tenant query in the package: the session
// manager needs the full roster at startup to run one client per line.
func (s *Store) ListActiveChannels(ctx context.Context) ([]WAChannel, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT wc.id, wc.company_id, wc.channel, COALESCE(wc.wa_device_jid, '')
		 FROM wa_channels wc
		 JOIN companies c ON c.id = wc.company_id AND c.is_active
		 WHERE wc.is_active
		 ORDER BY wc.company_id, wc.channel`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WAChannel
	for rows.Next() {
		var ch WAChannel
		if err := rows.Scan(&ch.ID, &ch.CompanyID, &ch.Channel, &ch.DeviceJID); err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// SetChannelDeviceJID records the linked device JID after a successful QR
// pairing, so restarts reuse the session instead of pairing again.
func (s *Store) SetChannelDeviceJID(ctx context.Context, companyID int64, channel Channel, jid string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE wa_channels SET wa_device_jid = $3
		 WHERE company_id = $1 AND channel = $2 AND is_active`,
		companyID, channel, jid)
	return err
}
