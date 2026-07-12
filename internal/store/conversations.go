package store

import (
	"context"
	"encoding/json"
	"time"
)

type Conversation struct {
	ID          int64
	CompanyID   int64
	UserID      int64
	Channel     Channel
	ChatJID     string
	Summary     string
	SummaryUpto int64 // messages.id watermark: rows <= this are covered by Summary
	EscalatedAt *time.Time
}

type StoredMessage struct {
	ID      int64
	Kind    string // user | assistant | tool_result | system_note
	Content []byte // JSONB: []llm.ContentBlock
}

func (s *Store) GetOrCreateConversation(ctx context.Context, companyID, userID int64, channel Channel, chatJID string) (*Conversation, error) {
	c := &Conversation{}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO conversations (company_id, user_id, channel, wa_chat_jid)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (company_id, user_id, channel) DO UPDATE
		   SET last_activity_at = now(), wa_chat_jid = EXCLUDED.wa_chat_jid
		 RETURNING id, company_id, user_id, channel, wa_chat_jid,
		           summary, summary_upto_message_id, escalated_at`,
		companyID, userID, channel, chatJID).
		Scan(&c.ID, &c.CompanyID, &c.UserID, &c.Channel, &c.ChatJID,
			&c.Summary, &c.SummaryUpto, &c.EscalatedAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// AppendMessage persists one message row. content is marshaled to JSONB;
// pass a []llm.ContentBlock (or anything JSON-serializable).
func (s *Store) AppendMessage(ctx context.Context, companyID, convID int64, kind string, content any, waMessageID *string) (int64, error) {
	raw, err := json.Marshal(content)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.pool.QueryRow(ctx,
		`INSERT INTO messages (conversation_id, company_id, kind, content, wa_message_id)
		 SELECT $2, $1, $3, $4, $5
		 WHERE EXISTS (SELECT 1 FROM conversations WHERE id = $2 AND company_id = $1)
		 RETURNING id`,
		companyID, convID, kind, raw, waMessageID).Scan(&id)
	return id, err
}

// RecentMessages returns up to `limit` of the newest messages with
// id > afterID, in ascending id order.
func (s *Store) RecentMessages(ctx context.Context, companyID, convID, afterID int64, limit int) ([]StoredMessage, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, kind, content FROM (
		    SELECT id, kind, content FROM messages
		    WHERE company_id = $1 AND conversation_id = $2 AND id > $3
		      AND kind <> 'system_note'
		    ORDER BY id DESC LIMIT $4
		 ) sub ORDER BY id ASC`,
		companyID, convID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredMessage
	for rows.Next() {
		var m StoredMessage
		if err := rows.Scan(&m.ID, &m.Kind, &m.Content); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// OldestMessagesAfter returns up to `limit` of the oldest messages with
// id > afterID, ascending — the slice that summarization folds away.
func (s *Store) OldestMessagesAfter(ctx context.Context, companyID, convID, afterID int64, limit int) ([]StoredMessage, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, kind, content FROM messages
		 WHERE company_id = $1 AND conversation_id = $2 AND id > $3
		   AND kind <> 'system_note'
		 ORDER BY id ASC LIMIT $4`,
		companyID, convID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredMessage
	for rows.Next() {
		var m StoredMessage
		if err := rows.Scan(&m.ID, &m.Kind, &m.Content); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) CountMessagesAfter(ctx context.Context, companyID, convID, afterID int64) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM messages
		 WHERE company_id = $1 AND conversation_id = $2 AND id > $3
		   AND kind <> 'system_note'`,
		companyID, convID, afterID).Scan(&n)
	return n, err
}

func (s *Store) UpdateSummary(ctx context.Context, companyID, convID int64, summary string, uptoID int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE conversations SET summary = $3, summary_upto_message_id = $4
		 WHERE company_id = $1 AND id = $2`,
		companyID, convID, summary, uptoID)
	return err
}

func (s *Store) TouchConversation(ctx context.Context, companyID, convID int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE conversations SET last_activity_at = now()
		 WHERE company_id = $1 AND id = $2`, companyID, convID)
	return err
}

func (s *Store) SetEscalated(ctx context.Context, companyID, convID int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE conversations SET escalated_at = now()
		 WHERE company_id = $1 AND id = $2 AND escalated_at IS NULL`,
		companyID, convID)
	return err
}
