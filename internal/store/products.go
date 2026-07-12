package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

type Product struct {
	ID          int64
	CompanyID   int64
	Name        string
	Description string
	Category    string
	Tags        []string
	Price       float64
	Stock       int
}

const productCols = `id, company_id, name, description, category, tags, price, stock`

func scanProduct(row pgx.Row) (*Product, error) {
	p := &Product{}
	err := row.Scan(&p.ID, &p.CompanyID, &p.Name, &p.Description, &p.Category, &p.Tags, &p.Price, &p.Stock)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// FindProducts is the fuzzy lookup behind check_stock / check_price: matches
// name, description or tags, active products only.
func (s *Store) FindProducts(ctx context.Context, companyID int64, query string, limit int) ([]Product, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+productCols+` FROM products
		 WHERE company_id = $1 AND is_active
		   AND (name ILIKE '%' || $2 || '%'
		        OR description ILIKE '%' || $2 || '%'
		        OR $2 ILIKE ANY(tags))
		 ORDER BY name LIMIT $3`,
		companyID, query, limit)
	if err != nil {
		return nil, err
	}
	return collectProducts(rows)
}

// CatalogFilter narrows SearchProducts. Zero values mean "no filter".
type CatalogFilter struct {
	Category string
	MaxPrice float64
	Tags     []string
	Keywords string
	Limit    int
}

func (s *Store) SearchProducts(ctx context.Context, companyID int64, f CatalogFilter) ([]Product, error) {
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
	sql := `SELECT ` + productCols + ` FROM products WHERE ` + strings.Join(conds, " AND ") +
		` ORDER BY category, name LIMIT ` + arg(f.Limit)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return collectProducts(rows)
}

// GetProduct returns an active product or (nil, nil).
func (s *Store) GetProduct(ctx context.Context, companyID, id int64) (*Product, error) {
	p, err := scanProduct(s.pool.QueryRow(ctx,
		`SELECT `+productCols+` FROM products
		 WHERE company_id = $1 AND id = $2 AND is_active`, companyID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// GetProductForUpdate locks the row inside the confirm transaction so the
// before/after snapshot in audit_log is exact.
func (s *Store) GetProductForUpdate(ctx context.Context, tx pgx.Tx, companyID, id int64) (*Product, error) {
	p, err := scanProduct(tx.QueryRow(ctx,
		`SELECT `+productCols+` FROM products
		 WHERE company_id = $1 AND id = $2 AND is_active FOR UPDATE`, companyID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func (s *Store) InsertProductTx(ctx context.Context, tx pgx.Tx, p *Product) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx,
		`INSERT INTO products (company_id, name, description, category, tags, price, stock)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		p.CompanyID, p.Name, p.Description, p.Category, p.Tags, p.Price, p.Stock).Scan(&id)
	return id, err
}

func (s *Store) SaveProductTx(ctx context.Context, tx pgx.Tx, p *Product) error {
	tag, err := tx.Exec(ctx,
		`UPDATE products SET name = $3, description = $4, category = $5, tags = $6,
		        price = $7, stock = $8, updated_at = now()
		 WHERE company_id = $1 AND id = $2 AND is_active`,
		p.CompanyID, p.ID, p.Name, p.Description, p.Category, p.Tags, p.Price, p.Stock)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("product no longer exists")
	}
	return nil
}

// ArchiveProductTx is the ONLY delete path: a soft delete.
func (s *Store) ArchiveProductTx(ctx context.Context, tx pgx.Tx, companyID, id int64) error {
	tag, err := tx.Exec(ctx,
		`UPDATE products SET is_active = FALSE, archived_at = now(), updated_at = now()
		 WHERE company_id = $1 AND id = $2 AND is_active`, companyID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("product no longer exists")
	}
	return nil
}

func collectProducts(rows pgx.Rows) ([]Product, error) {
	defer rows.Close()
	var out []Product
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.CompanyID, &p.Name, &p.Description, &p.Category,
			&p.Tags, &p.Price, &p.Stock); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
