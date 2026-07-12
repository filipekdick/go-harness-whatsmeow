package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

type Service struct {
	ID          int64
	CompanyID   int64
	Name        string
	Description string
	Category    string
	Tags        []string
	Price       float64
}

const serviceCols = `id, company_id, name, description, category, tags, price`

func (s *Store) SearchServices(ctx context.Context, companyID int64, f CatalogFilter) ([]Service, error) {
	conds := []string{"company_id = $1", "is_active"}
	args := []any{companyID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.Category != "" {
		conds = append(conds, "category ILIKE "+arg("%"+f.Category+"%"))
	}
	if f.MaxPrice > 0 {
		conds = append(conds, "price <= "+arg(f.MaxPrice))
	}
	if len(f.Tags) > 0 {
		conds = append(conds, "tags && "+arg(f.Tags))
	}
	if f.Keywords != "" {
		ph := arg("%" + f.Keywords + "%")
		conds = append(conds, "(name ILIKE "+ph+" OR description ILIKE "+ph+")")
	}
	if f.Limit <= 0 {
		f.Limit = 15
	}
	sql := `SELECT ` + serviceCols + ` FROM services WHERE ` + strings.Join(conds, " AND ") +
		` ORDER BY category, name LIMIT ` + arg(f.Limit)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Service
	for rows.Next() {
		var v Service
		if err := rows.Scan(&v.ID, &v.CompanyID, &v.Name, &v.Description, &v.Category, &v.Tags, &v.Price); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetService(ctx context.Context, companyID, id int64) (*Service, error) {
	v := &Service{}
	err := s.pool.QueryRow(ctx,
		`SELECT `+serviceCols+` FROM services
		 WHERE company_id = $1 AND id = $2 AND is_active`, companyID, id).
		Scan(&v.ID, &v.CompanyID, &v.Name, &v.Description, &v.Category, &v.Tags, &v.Price)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

func (s *Store) GetServiceForUpdate(ctx context.Context, tx pgx.Tx, companyID, id int64) (*Service, error) {
	v := &Service{}
	err := tx.QueryRow(ctx,
		`SELECT `+serviceCols+` FROM services
		 WHERE company_id = $1 AND id = $2 AND is_active FOR UPDATE`, companyID, id).
		Scan(&v.ID, &v.CompanyID, &v.Name, &v.Description, &v.Category, &v.Tags, &v.Price)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

func (s *Store) InsertServiceTx(ctx context.Context, tx pgx.Tx, v *Service) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx,
		`INSERT INTO services (company_id, name, description, category, tags, price)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		v.CompanyID, v.Name, v.Description, v.Category, v.Tags, v.Price).Scan(&id)
	return id, err
}

func (s *Store) SaveServiceTx(ctx context.Context, tx pgx.Tx, v *Service) error {
	tag, err := tx.Exec(ctx,
		`UPDATE services SET name = $3, description = $4, category = $5, tags = $6,
		        price = $7, updated_at = now()
		 WHERE company_id = $1 AND id = $2 AND is_active`,
		v.CompanyID, v.ID, v.Name, v.Description, v.Category, v.Tags, v.Price)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("service no longer exists")
	}
	return nil
}

func (s *Store) ArchiveServiceTx(ctx context.Context, tx pgx.Tx, companyID, id int64) error {
	tag, err := tx.Exec(ctx,
		`UPDATE services SET is_active = FALSE, archived_at = now(), updated_at = now()
		 WHERE company_id = $1 AND id = $2 AND is_active`, companyID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("service no longer exists")
	}
	return nil
}
