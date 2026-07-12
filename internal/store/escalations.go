package store

import "context"

func (s *Store) CreateEscalation(ctx context.Context, companyID, convID int64, summary string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO escalations (company_id, conversation_id, summary)
		 SELECT $1, $2, $3
		 WHERE EXISTS (SELECT 1 FROM conversations WHERE id = $2 AND company_id = $1)
		 RETURNING id`,
		companyID, convID, summary).Scan(&id)
	return id, err
}
