package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type PendingWrite struct {
	ID                 string // UUID
	CompanyID          int64
	ConversationID     int64
	RequestedBy        int64
	ToolName           string
	Params             []byte // validated tool params, verbatim JSON
	Preview            string
	Status             string
	CreatedInMessageID int64
	ExpiresAt          time.Time
}

func (s *Store) CreatePendingWrite(ctx context.Context, companyID, convID, requestedBy int64,
	toolName string, params map[string]any, preview string, createdInMsgID int64, ttl time.Duration) (string, error) {

	raw, err := json.Marshal(params)
	if err != nil {
		return "", err
	}
	var id string
	err = s.pool.QueryRow(ctx,
		`INSERT INTO pending_writes
		   (company_id, conversation_id, requested_by, tool_name, params, preview,
		    created_in_message_id, expires_at)
		 SELECT $1, $2, $3, $4, $5, $6, $7, now() + $8
		 WHERE EXISTS (SELECT 1 FROM conversations WHERE id = $2 AND company_id = $1)
		 RETURNING id`,
		companyID, convID, requestedBy, toolName, raw, preview, createdInMsgID, ttl).Scan(&id)
	return id, err
}

func (s *Store) GetPendingWrite(ctx context.Context, companyID int64, id string) (*PendingWrite, error) {
	pw := &PendingWrite{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, company_id, conversation_id, requested_by, tool_name, params, preview,
		        status, created_in_message_id, expires_at
		 FROM pending_writes WHERE company_id = $1 AND id = $2`, companyID, id).
		Scan(&pw.ID, &pw.CompanyID, &pw.ConversationID, &pw.RequestedBy, &pw.ToolName,
			&pw.Params, &pw.Preview, &pw.Status, &pw.CreatedInMessageID, &pw.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return pw, nil
}

// OpenPendingWrites lists still-pending writes of one conversation, used to
// help the model recover when it confirms with a wrong/missing ID.
func (s *Store) OpenPendingWrites(ctx context.Context, companyID, convID int64) ([]PendingWrite, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, tool_name, preview, expires_at FROM pending_writes
		 WHERE company_id = $1 AND conversation_id = $2 AND status = 'PENDING'
		 ORDER BY created_at`, companyID, convID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingWrite
	for rows.Next() {
		var pw PendingWrite
		if err := rows.Scan(&pw.ID, &pw.ToolName, &pw.Preview, &pw.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, pw)
	}
	return out, rows.Err()
}

// ResolvePendingWrite flips PENDING -> status. The WHERE status = 'PENDING'
// guard doubles as a mutex: a second concurrent confirm affects 0 rows.
// Runs on any Querier so confirms can join the write's transaction.
func (s *Store) ResolvePendingWrite(ctx context.Context, q Querier, companyID int64, id, status string) (bool, error) {
	tag, err := q.Exec(ctx,
		`UPDATE pending_writes SET status = $3, resolved_at = now()
		 WHERE company_id = $1 AND id = $2 AND status = 'PENDING'`,
		companyID, id, status)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
