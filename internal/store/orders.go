package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type Order struct {
	ID         int64
	CustomerID int64
	Status     string
	Total      float64
	Notes      string
	CreatedAt  time.Time
}

type OrderItem struct {
	ProductName string
	Quantity    int
	UnitPrice   float64
}

// GetOrder returns an order with its items, or (nil, nil, nil) when it does
// not exist in this company. Customer visibility (own orders only) is
// enforced by the tool layer via CustomerID.
func (s *Store) GetOrder(ctx context.Context, companyID, orderID int64) (*Order, []OrderItem, error) {
	o := &Order{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, customer_id, status, total, notes, created_at FROM orders
		 WHERE company_id = $1 AND id = $2 AND is_active`, companyID, orderID).
		Scan(&o.ID, &o.CustomerID, &o.Status, &o.Total, &o.Notes, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}

	rows, err := s.pool.Query(ctx,
		`SELECT COALESCE(p.name, 'item #' || oi.product_id), oi.quantity, oi.unit_price
		 FROM order_items oi
		 LEFT JOIN products p ON p.id = oi.product_id AND p.company_id = oi.company_id
		 WHERE oi.company_id = $1 AND oi.order_id = $2
		 ORDER BY oi.id`, companyID, orderID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var items []OrderItem
	for rows.Next() {
		var it OrderItem
		if err := rows.Scan(&it.ProductName, &it.Quantity, &it.UnitPrice); err != nil {
			return nil, nil, err
		}
		items = append(items, it)
	}
	return o, items, rows.Err()
}
