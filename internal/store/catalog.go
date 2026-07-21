package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	CatalogKindAll     = "all"
	CatalogKindProduct = "product"
	CatalogKindService = "service"

	defaultCatalogLimit = 10
	maxCatalogLimit     = 20
)

// CatalogSearch controls a tenant-scoped search across active products and
// services. Kind accepts "all", "product", or "service".
type CatalogSearch struct {
	Query    string
	Kind     string
	Category string
	Limit    int
}

// CatalogItem is the common read model returned by SearchCatalog. Stock is
// present only for products. Price is kept as a decimal string to avoid losing
// NUMERIC precision through a float conversion.
type CatalogItem struct {
	Kind        string   `json:"kind"`
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Tags        []string `json:"tags"`
	Price       string   `json:"price"`
	Stock       *int     `json:"stock,omitempty"`
}

func (s *Store) SearchCatalog(ctx context.Context, companyID int64, search CatalogSearch) ([]CatalogItem, error) {
	search.Query = strings.TrimSpace(search.Query)
	if search.Query == "" {
		return nil, errors.New("catalog query must not be empty")
	}
	search.Kind = strings.ToLower(strings.TrimSpace(search.Kind))
	if search.Kind == "" {
		search.Kind = CatalogKindAll
	}
	if search.Kind != CatalogKindAll && search.Kind != CatalogKindProduct && search.Kind != CatalogKindService {
		return nil, fmt.Errorf("invalid catalog kind %q", search.Kind)
	}
	search.Category = strings.TrimSpace(search.Category)
	if search.Limit <= 0 {
		search.Limit = defaultCatalogLimit
	}
	if search.Limit > maxCatalogLimit {
		search.Limit = maxCatalogLimit
	}

	rows, err := s.pool.Query(ctx,
		`SELECT kind, id, name, description, category, tags, price, stock
		 FROM (
		     SELECT 'product'::text AS kind, id, name, description, category, tags,
		            price::text AS price, stock
		     FROM products
		     WHERE company_id = $1 AND is_active
		       AND $3 IN ('all', 'product')
		       AND ($4 = '' OR lower(category) = lower($4))
		       AND (
		           to_tsvector('simple', name || ' ' || description) @@ websearch_to_tsquery('simple', $2)
		           OR strpos(lower(name), lower($2)) > 0
		           OR strpos(lower(description), lower($2)) > 0
		           OR strpos(lower(category), lower($2)) > 0
		           OR EXISTS (
		               SELECT 1 FROM unnest(tags) AS tag
		               WHERE strpos(lower(tag), lower($2)) > 0
		           )
		       )
		     UNION ALL
		     SELECT 'service'::text AS kind, id, name, description, category, tags,
		            price::text AS price, NULL::integer AS stock
		     FROM services
		     WHERE company_id = $1 AND is_active
		       AND $3 IN ('all', 'service')
		       AND ($4 = '' OR lower(category) = lower($4))
		       AND (
		           to_tsvector('simple', name || ' ' || description) @@ websearch_to_tsquery('simple', $2)
		           OR strpos(lower(name), lower($2)) > 0
		           OR strpos(lower(description), lower($2)) > 0
		           OR strpos(lower(category), lower($2)) > 0
		           OR EXISTS (
		               SELECT 1 FROM unnest(tags) AS tag
		               WHERE strpos(lower(tag), lower($2)) > 0
		           )
		       )
		 ) AS catalog
		 ORDER BY (lower(name) = lower($2)) DESC, name, kind, id
		 LIMIT $5`,
		companyID, search.Query, search.Kind, search.Category, search.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]CatalogItem, 0)
	for rows.Next() {
		var item CatalogItem
		if err := rows.Scan(&item.Kind, &item.ID, &item.Name, &item.Description,
			&item.Category, &item.Tags, &item.Price, &item.Stock); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
