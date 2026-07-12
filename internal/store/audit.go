package store

import (
	"context"
	"encoding/json"
)

// AuditEntry records one committed write. Before/After are full row
// snapshots; Before is nil on creates.
type AuditEntry struct {
	CompanyID      int64
	ActorUserID    *int64
	ActorPhone     string
	Action         string // tool name or internal action
	EntityType     string // product | service | business_rule | order | ...
	EntityID       *int64
	Before         any
	After          any
	PendingWriteID *string // UUID
}

// InsertAudit writes an audit row. Pass the pgx.Tx of the write being
// recorded so the audit entry commits atomically with it.
func InsertAudit(ctx context.Context, q Querier, e AuditEntry) error {
	before, err := marshalNullable(e.Before)
	if err != nil {
		return err
	}
	after, err := marshalNullable(e.After)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx,
		`INSERT INTO audit_log
		   (company_id, actor_user_id, actor_phone, action, entity_type, entity_id,
		    before, after, pending_write_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		e.CompanyID, e.ActorUserID, e.ActorPhone, e.Action, e.EntityType, e.EntityID,
		before, after, e.PendingWriteID)
	return err
}

func marshalNullable(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}
