package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

type Service struct {
	ID          int64    `json:"id"`
	CompanyID   int64    `json:"company_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Tags        []string `json:"tags"`
	Price       string   `json:"price"`
}

// GetService returns an active service in the company, or (nil, nil) when it
// does not exist.
func (s *Store) GetService(ctx context.Context, companyID, serviceID int64) (*Service, error) {
	service := &Service{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, company_id, name, description, category, tags, price::text
		 FROM services
		 WHERE company_id = $1 AND id = $2 AND is_active`,
		companyID, serviceID).Scan(
		&service.ID, &service.CompanyID, &service.Name, &service.Description,
		&service.Category, &service.Tags, &service.Price)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return service, nil
}
