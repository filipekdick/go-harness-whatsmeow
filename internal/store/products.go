package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

type Product struct {
	ID          int64    `json:"id"`
	CompanyID   int64    `json:"company_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Tags        []string `json:"tags"`
	Price       string   `json:"price"`
	Stock       int      `json:"stock"`
}

type ProductStock struct {
	ProductID int64  `json:"product_id"`
	Name      string `json:"name"`
	Stock     int    `json:"stock"`
}

// GetProduct returns an active product in the company, or (nil, nil) when it
// does not exist. The company filter is mandatory even though product IDs are
// globally unique, preserving tenant isolation by construction.
func (s *Store) GetProduct(ctx context.Context, companyID, productID int64) (*Product, error) {
	product := &Product{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, company_id, name, description, category, tags, price::text, stock
		 FROM products
		 WHERE company_id = $1 AND id = $2 AND is_active`,
		companyID, productID).Scan(
		&product.ID, &product.CompanyID, &product.Name, &product.Description,
		&product.Category, &product.Tags, &product.Price, &product.Stock)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return product, nil
}

// GetProductStock returns the current stock for one active product, or
// (nil, nil) when it is not visible in the company.
func (s *Store) GetProductStock(ctx context.Context, companyID, productID int64) (*ProductStock, error) {
	stock := &ProductStock{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, stock
		 FROM products
		 WHERE company_id = $1 AND id = $2 AND is_active`,
		companyID, productID).Scan(&stock.ProductID, &stock.Name, &stock.Stock)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return stock, nil
}
