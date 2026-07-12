package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// WAChannel is one WhatsApp line: a (company, CUSTOMER|EMPLOYEE) pair linked
// to at most one WhatsMeow device.
type WAChannel struct {
	ID        int64
	CompanyID int64
	Channel   Channel
	DeviceJID *string // nil until QR pairing has completed
}

// ListWAChannels returns every active channel of every active company.
// This is infrastructure bootstrap (the SessionManager owns all lines), not
// tenant data access, so it is intentionally not company-scoped.
func (s *Store) ListWAChannels(ctx context.Context) ([]WAChannel, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT wc.id, wc.company_id, wc.channel, wc.wa_device_jid
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

func (s *Store) GetWAChannel(ctx context.Context, companyID int64, channel Channel) (*WAChannel, error) {
	ch := &WAChannel{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, company_id, channel, wa_device_jid FROM wa_channels
		 WHERE company_id = $1 AND channel = $2 AND is_active`,
		companyID, channel).
		Scan(&ch.ID, &ch.CompanyID, &ch.Channel, &ch.DeviceJID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ch, nil
}

func (s *Store) SetWAChannelDevice(ctx context.Context, companyID int64, channel Channel, deviceJID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE wa_channels SET wa_device_jid = $3
		 WHERE company_id = $1 AND channel = $2 AND is_active`,
		companyID, channel, deviceJID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("no active wa_channel row for this company/channel")
	}
	return nil
}

// CreateCompany bootstraps a tenant: the company row plus its two channel
// rows (unpaired). Returns the new company id.
func (s *Store) CreateCompany(ctx context.Context, name, systemPrompt string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var id int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO companies (name, system_prompt) VALUES ($1, $2) RETURNING id`,
		name, systemPrompt).Scan(&id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO wa_channels (company_id, channel) VALUES ($1, 'CUSTOMER'), ($1, 'EMPLOYEE')`,
		id); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

// UpsertEmployee registers (or re-activates) an employee number for a
// company. Used by the add-employee CLI subcommand — employees are NEVER
// created implicitly from inbound messages.
func (s *Store) UpsertEmployee(ctx context.Context, companyID int64, phone, displayName string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (company_id, phone, role, display_name)
		 VALUES ($1, $2, 'EMPLOYEE', NULLIF($3, ''))
		 ON CONFLICT (company_id, phone) DO UPDATE
		   SET role = 'EMPLOYEE',
		       display_name = COALESCE(NULLIF($3, ''), users.display_name),
		       is_active = TRUE, archived_at = NULL, updated_at = now()
		 RETURNING id`,
		companyID, phone, displayName).Scan(&id)
	return id, err
}
