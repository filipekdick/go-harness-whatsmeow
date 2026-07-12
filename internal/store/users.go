package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

type Company struct {
	ID           int64
	Name         string
	SystemPrompt string
}

type User struct {
	ID          int64
	CompanyID   int64
	Phone       string
	Role        Role
	DisplayName string
}

func (s *Store) GetCompany(ctx context.Context, companyID int64) (*Company, error) {
	c := &Company{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, system_prompt FROM companies
		 WHERE id = $1 AND is_active`, companyID).
		Scan(&c.ID, &c.Name, &c.SystemPrompt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// UserByPhone returns the active user for this phone within the company,
// or (nil, nil) when none exists.
func (s *Store) UserByPhone(ctx context.Context, companyID int64, phone string) (*User, error) {
	u := &User{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, company_id, phone, role, COALESCE(display_name, '')
		 FROM users
		 WHERE company_id = $1 AND phone = $2 AND is_active`,
		companyID, phone).
		Scan(&u.ID, &u.CompanyID, &u.Phone, &u.Role, &u.DisplayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// EnsureCustomer returns the existing user for this phone or creates a new
// CUSTOMER row. It never changes the role of an existing user — an employee
// texting the customer line stays an EMPLOYEE in the database (the harness
// decides the effective role from the channel).
func (s *Store) EnsureCustomer(ctx context.Context, companyID int64, phone, displayName string) (*User, error) {
	// Insert-if-absent without ever updating an existing row.
	_, err := s.pool.Exec(ctx,
		`INSERT INTO users (company_id, phone, role, display_name)
		 VALUES ($1, $2, 'CUSTOMER', NULLIF($3, ''))
		 ON CONFLICT (company_id, phone) DO NOTHING`,
		companyID, phone, displayName)
	if err != nil {
		return nil, err
	}
	u, err := s.UserByPhone(ctx, companyID, phone)
	if err != nil {
		return nil, err
	}
	if u == nil {
		// Row exists but is_active = FALSE (blocked/archived user).
		return nil, errors.New("user exists but is inactive")
	}
	return u, nil
}
