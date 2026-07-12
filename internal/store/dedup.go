package store

import "context"

// FirstTimeProcessing records the WhatsApp message ID and reports whether
// this is the first time we've seen it. WhatsApp can redeliver; callers must
// drop the message when this returns false.
func (s *Store) FirstTimeProcessing(ctx context.Context, companyID int64, waMessageID string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO processed_messages (company_id, wa_message_id)
		 VALUES ($1, $2)
		 ON CONFLICT DO NOTHING`,
		companyID, waMessageID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
