package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type BusinessRule struct {
	ID        int64     `json:"id"`
	CompanyID int64     `json:"company_id"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

// GetBusinessRule returns one active rule by its exact key, or (nil, nil)
// when the key is not defined for the company.
func (s *Store) GetBusinessRule(ctx context.Context, companyID int64, key string) (*BusinessRule, error) {
	rule := &BusinessRule{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, company_id, key, value, updated_at
		 FROM business_rules
		 WHERE company_id = $1 AND key = $2 AND is_active`,
		companyID, key).Scan(&rule.ID, &rule.CompanyID, &rule.Key, &rule.Value, &rule.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return rule, nil
}
