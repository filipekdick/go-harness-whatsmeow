package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type Order struct {
	ID         int64       `json:"id"`
	CompanyID  int64       `json:"company_id"`
	CustomerID int64       `json:"customer_id"`
	Status     string      `json:"status"`
	Total      string      `json:"total"`
	Notes      string      `json:"notes,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
	UpdatedAt  time.Time   `json:"updated_at"`
	Items      []OrderItem `json:"items"`
}

type OrderItem struct {
	ProductID   int64  `json:"product_id"`
	ProductName string `json:"product_name"`
	Quantity    int    `json:"quantity"`
	UnitPrice   string `json:"unit_price"`
}

// GetOrder returns an active order visible to the company. This is intended
// for employee reads; customer-facing callers must use GetOrderForCustomer.
func (s *Store) GetOrder(ctx context.Context, companyID, orderID int64) (*Order, error) {
	return s.getOrder(ctx, companyID, orderID, nil)
}

// GetOrderForCustomer adds customer ownership to tenant scoping. Returning
// (nil, nil) for another customer's order avoids leaking whether it exists.
func (s *Store) GetOrderForCustomer(ctx context.Context, companyID, orderID, customerID int64) (*Order, error) {
	return s.getOrder(ctx, companyID, orderID, &customerID)
}

func (s *Store) getOrder(ctx context.Context, companyID, orderID int64, customerID *int64) (*Order, error) {
	order := &Order{}
	var err error
	if customerID == nil {
		err = s.pool.QueryRow(ctx,
			`SELECT id, company_id, customer_id, status, total::text, notes, created_at, updated_at
			 FROM orders
			 WHERE company_id = $1 AND id = $2 AND is_active`,
			companyID, orderID).Scan(
			&order.ID, &order.CompanyID, &order.CustomerID, &order.Status,
			&order.Total, &order.Notes, &order.CreatedAt, &order.UpdatedAt)
	} else {
		err = s.pool.QueryRow(ctx,
			`SELECT id, company_id, customer_id, status, total::text, notes, created_at, updated_at
			 FROM orders
			 WHERE company_id = $1 AND id = $2 AND customer_id = $3 AND is_active`,
			companyID, orderID, *customerID).Scan(
			&order.ID, &order.CompanyID, &order.CustomerID, &order.Status,
			&order.Total, &order.Notes, &order.CreatedAt, &order.UpdatedAt)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx,
		`SELECT oi.product_id, p.name, oi.quantity, oi.unit_price::text
		 FROM order_items AS oi
		 JOIN orders AS o
		   ON o.id = oi.order_id AND o.company_id = oi.company_id
		 JOIN products AS p
		   ON p.id = oi.product_id AND p.company_id = oi.company_id
		 WHERE oi.company_id = $1 AND oi.order_id = $2
		 ORDER BY oi.id`,
		companyID, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	order.Items = make([]OrderItem, 0)
	for rows.Next() {
		var item OrderItem
		if err := rows.Scan(&item.ProductID, &item.ProductName, &item.Quantity, &item.UnitPrice); err != nil {
			return nil, err
		}
		order.Items = append(order.Items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return order, nil
}
