package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

type BusinessRule struct {
	Key   string
	Value string
}

// MatchBusinessRules returns active rules whose key or value matches the
// topic (case-insensitive substring). Empty topic returns everything.
func (s *Store) MatchBusinessRules(ctx context.Context, companyID int64, topic string) ([]BusinessRule, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT key, value FROM business_rules
		 WHERE company_id = $1 AND is_active
		   AND ($2 = '' OR key ILIKE '%' || $2 || '%' OR value ILIKE '%' || $2 || '%')
		 ORDER BY key`,
		companyID, topic)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BusinessRule
	for rows.Next() {
		var r BusinessRule
		if err := rows.Scan(&r.Key, &r.Value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ListBusinessRuleKeys(ctx context.Context, companyID int64) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT key FROM business_rules WHERE company_id = $1 AND is_active ORDER BY key`,
		companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) GetBusinessRule(ctx context.Context, companyID int64, key string) (*BusinessRule, error) {
	r := &BusinessRule{}
	err := s.pool.QueryRow(ctx,
		`SELECT key, value FROM business_rules
		 WHERE company_id = $1 AND key = $2 AND is_active`, companyID, key).
		Scan(&r.Key, &r.Value)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Store) UpsertBusinessRuleTx(ctx context.Context, q Querier, companyID int64, key, value string) error {
	_, err := q.Exec(ctx,
		`INSERT INTO business_rules (company_id, key, value)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (company_id, key) DO UPDATE
		   SET value = EXCLUDED.value, is_active = TRUE, archived_at = NULL, updated_at = now()`,
		companyID, key, value)
	return err
}
