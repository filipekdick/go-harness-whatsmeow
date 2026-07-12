package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/filipekdick/go-harness-whatsmeow/internal/store"
)

// Employee write tools are two-phase. The tool handlers below only PREPARE:
// they validate, compute a human-readable preview against current data, and
// insert a pending_writes row. The commit happens exclusively in
// confirm_write (confirm.go), which dispatches to the applyFuncs table.
// There is no code path from a write tool handler to an UPDATE/INSERT.

type applyResult struct {
	message    string
	entityType string
	entityID   *int64
	before     any
	after      any
}

type applyFunc func(ctx context.Context, tx pgx.Tx, env *Env, params map[string]any) (*applyResult, error)

var applyFuncs = map[string]applyFunc{
	"create_product":    applyCreateProduct,
	"update_product":    applyUpdateProduct,
	"update_stock":      applyUpdateStock,
	"archive_product":   applyArchiveProduct,
	"create_service":    applyCreateService,
	"update_service":    applyUpdateService,
	"archive_service":   applyArchiveService,
	"set_business_rule": applySetBusinessRule,
}

func registerEmployeeTools(r *Registry, pendingTTL time.Duration) {
	employee := []store.Role{store.RoleEmployee}

	prepare := func(ctx context.Context, env *Env, toolName string, params map[string]any, preview string) (string, error) {
		id, err := env.Store.CreatePendingWrite(ctx, env.CompanyID, env.ConversationID,
			env.User.ID, toolName, params, preview, env.InboundMessageID, pendingTTL)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("PENDING WRITE %s (expires in %d minutes)\n%s\n"+
			"NOTHING has been saved yet. Show this preview to the employee and ask them to confirm. "+
			"Only after they answer yes in their next message, call confirm_write with pending_write_id=%q.",
			id, int(pendingTTL.Minutes()), preview, id), nil
	}

	// ---- read-only employee tools (no confirmation needed) ----------------

	r.Register(&Tool{
		Name: "list_products",
		Description: "List the company's products with their IDs, prices, stock and attributes. " +
			"Use this to find the product_id needed by update/archive tools.",
		InputSchema: obj(map[string]any{
			"category": str("Filter by category (substring match)"),
			"keywords": str("Free-text match on name and description"),
			"limit":    integer("Max rows (default 20, max 50)"),
		}),
		Roles: employee,
		Handler: func(ctx context.Context, env *Env, p map[string]any) (string, error) {
			products, err := env.Store.SearchProducts(ctx, env.CompanyID, store.CatalogFilter{
				Category: strParam(p, "category"),
				Keywords: strParam(p, "keywords"),
				Limit:    limitParam(p, "limit", 20, 50),
			})
			if err != nil {
				return "", err
			}
			if len(products) == 0 {
				return "no products found", nil
			}
			var b strings.Builder
			for _, prod := range products {
				formatProduct(&b, prod, true)
			}
			return b.String(), nil
		},
	})

	r.Register(&Tool{
		Name:        "list_services",
		Description: "List the company's services with their IDs, prices and attributes.",
		InputSchema: obj(map[string]any{
			"category": str("Filter by category (substring match)"),
			"keywords": str("Free-text match on name and description"),
			"limit":    integer("Max rows (default 20, max 50)"),
		}),
		Roles: employee,
		Handler: func(ctx context.Context, env *Env, p map[string]any) (string, error) {
			services, err := env.Store.SearchServices(ctx, env.CompanyID, store.CatalogFilter{
				Category: strParam(p, "category"),
				Keywords: strParam(p, "keywords"),
				Limit:    limitParam(p, "limit", 20, 50),
			})
			if err != nil {
				return "", err
			}
			if len(services) == 0 {
				return "no services found", nil
			}
			var b strings.Builder
			for _, svc := range services {
				formatService(&b, svc, true)
			}
			return b.String(), nil
		},
	})

	// ---- write tools (prepare phase only) ----------------------------------

	twoPhaseNote := " This only PREPARES the change and returns a preview; it commits after the employee confirms and you call confirm_write."

	r.Register(&Tool{
		Name:        "create_product",
		Description: "Add a new product to the catalog." + twoPhaseNote,
		InputSchema: obj(map[string]any{
			"name":        str("Product name"),
			"price":       num("Unit price, must be >= 0"),
			"stock":       integer("Initial stock, must be >= 0"),
			"description": str("Longer description shown to customers"),
			"category":    str("Category label"),
			"tags":        strArray("Search tags"),
		}, "name", "price", "stock"),
		Roles: employee,
		Handler: func(ctx context.Context, env *Env, p map[string]any) (string, error) {
			price, _ := floatParam(p, "price")
			stock, _ := intParam(p, "stock")
			if err := checkPriceStock(price, float64(stock)); err != nil {
				return "", err
			}
			preview := fmt.Sprintf("Create product %q: price %s, stock %d, category %q, tags [%s]\n%s",
				strParam(p, "name"), fmtPrice(price), stock, strParam(p, "category"),
				strings.Join(strSliceParam(p, "tags"), ", "), strParam(p, "description"))
			return prepare(ctx, env, "create_product", p, preview)
		},
	})

	r.Register(&Tool{
		Name:        "update_product",
		Description: "Change fields of an existing product (any subset of name, price, description, category, tags). Get the product_id from list_products first." + twoPhaseNote,
		InputSchema: obj(map[string]any{
			"product_id":  integer("ID of the product to change"),
			"name":        str("New name"),
			"price":       num("New price, must be >= 0"),
			"description": str("New description"),
			"category":    str("New category"),
			"tags":        strArray("New tags (replaces the old list)"),
		}, "product_id"),
		Roles: employee,
		Handler: func(ctx context.Context, env *Env, p map[string]any) (string, error) {
			id, _ := intParam(p, "product_id")
			prod, err := env.Store.GetProduct(ctx, env.CompanyID, id)
			if err != nil {
				return "", err
			}
			if prod == nil {
				return "", fmt.Errorf("product #%d not found", id)
			}
			var changes []string
			if has(p, "name") {
				changes = append(changes, fmt.Sprintf("name %q → %q", prod.Name, strParam(p, "name")))
			}
			if price, ok := floatParam(p, "price"); ok {
				if price < 0 {
					return "", fmt.Errorf("price must be >= 0")
				}
				changes = append(changes, fmt.Sprintf("price %s → %s", fmtPrice(prod.Price), fmtPrice(price)))
			}
			if has(p, "description") {
				changes = append(changes, fmt.Sprintf("description → %q", truncate(strParam(p, "description"), 120)))
			}
			if has(p, "category") {
				changes = append(changes, fmt.Sprintf("category %q → %q", prod.Category, strParam(p, "category")))
			}
			if has(p, "tags") {
				changes = append(changes, fmt.Sprintf("tags [%s] → [%s]",
					strings.Join(prod.Tags, ", "), strings.Join(strSliceParam(p, "tags"), ", ")))
			}
			if len(changes) == 0 {
				return "", fmt.Errorf("no fields to change were provided")
			}
			preview := fmt.Sprintf("Update product #%d %q:\n  %s", prod.ID, prod.Name, strings.Join(changes, "\n  "))
			return prepare(ctx, env, "update_product", p, preview)
		},
	})

	r.Register(&Tool{
		Name: "update_stock",
		Description: "Adjust the stock of a product, either by a relative delta (e.g. -3 after a sale, " +
			"+50 after a delivery) or to an absolute count (e.g. after a recount). Provide exactly one of delta/absolute." + twoPhaseNote,
		InputSchema: obj(map[string]any{
			"product_id": integer("ID of the product"),
			"delta":      integer("Relative change, may be negative"),
			"absolute":   integer("New absolute stock count, must be >= 0"),
		}, "product_id"),
		Roles: employee,
		Handler: func(ctx context.Context, env *Env, p map[string]any) (string, error) {
			id, _ := intParam(p, "product_id")
			hasDelta, hasAbs := has(p, "delta"), has(p, "absolute")
			if hasDelta == hasAbs {
				return "", fmt.Errorf("provide exactly one of delta or absolute")
			}
			prod, err := env.Store.GetProduct(ctx, env.CompanyID, id)
			if err != nil {
				return "", err
			}
			if prod == nil {
				return "", fmt.Errorf("product #%d not found", id)
			}
			newStock := int64(prod.Stock)
			if hasDelta {
				d, _ := intParam(p, "delta")
				newStock += d
			} else {
				newStock, _ = intParam(p, "absolute")
			}
			if newStock < 0 {
				return "", fmt.Errorf("stock cannot go below 0 (current stock of %q is %d)", prod.Name, prod.Stock)
			}
			preview := fmt.Sprintf("Product #%d %q: stock %d → %d", prod.ID, prod.Name, prod.Stock, newStock)
			return prepare(ctx, env, "update_stock", p, preview)
		},
	})

	r.Register(&Tool{
		Name: "archive_product",
		Description: "Archive (hide) a product so customers no longer see it. This is a soft delete; " +
			"data and history are kept." + twoPhaseNote,
		InputSchema: obj(map[string]any{
			"product_id": integer("ID of the product to archive"),
		}, "product_id"),
		Roles: employee,
		Handler: func(ctx context.Context, env *Env, p map[string]any) (string, error) {
			id, _ := intParam(p, "product_id")
			prod, err := env.Store.GetProduct(ctx, env.CompanyID, id)
			if err != nil {
				return "", err
			}
			if prod == nil {
				return "", fmt.Errorf("product #%d not found", id)
			}
			preview := fmt.Sprintf("Archive product #%d %q (price %s, stock %d). It disappears from the catalog but is not deleted.",
				prod.ID, prod.Name, fmtPrice(prod.Price), prod.Stock)
			return prepare(ctx, env, "archive_product", p, preview)
		},
	})

	r.Register(&Tool{
		Name:        "create_service",
		Description: "Add a new service to the catalog." + twoPhaseNote,
		InputSchema: obj(map[string]any{
			"name":        str("Service name"),
			"price":       num("Price, must be >= 0"),
			"description": str("Longer description shown to customers"),
			"category":    str("Category label"),
			"tags":        strArray("Search tags"),
		}, "name", "price"),
		Roles: employee,
		Handler: func(ctx context.Context, env *Env, p map[string]any) (string, error) {
			price, _ := floatParam(p, "price")
			if price < 0 {
				return "", fmt.Errorf("price must be >= 0")
			}
			preview := fmt.Sprintf("Create service %q: price %s, category %q, tags [%s]\n%s",
				strParam(p, "name"), fmtPrice(price), strParam(p, "category"),
				strings.Join(strSliceParam(p, "tags"), ", "), strParam(p, "description"))
			return prepare(ctx, env, "create_service", p, preview)
		},
	})

	r.Register(&Tool{
		Name:        "update_service",
		Description: "Change fields of an existing service (any subset of name, price, description, category, tags). Get the service_id from list_services first." + twoPhaseNote,
		InputSchema: obj(map[string]any{
			"service_id":  integer("ID of the service to change"),
			"name":        str("New name"),
			"price":       num("New price, must be >= 0"),
			"description": str("New description"),
			"category":    str("New category"),
			"tags":        strArray("New tags (replaces the old list)"),
		}, "service_id"),
		Roles: employee,
		Handler: func(ctx context.Context, env *Env, p map[string]any) (string, error) {
			id, _ := intParam(p, "service_id")
			svc, err := env.Store.GetService(ctx, env.CompanyID, id)
			if err != nil {
				return "", err
			}
			if svc == nil {
				return "", fmt.Errorf("service #%d not found", id)
			}
			var changes []string
			if has(p, "name") {
				changes = append(changes, fmt.Sprintf("name %q → %q", svc.Name, strParam(p, "name")))
			}
			if price, ok := floatParam(p, "price"); ok {
				if price < 0 {
					return "", fmt.Errorf("price must be >= 0")
				}
				changes = append(changes, fmt.Sprintf("price %s → %s", fmtPrice(svc.Price), fmtPrice(price)))
			}
			if has(p, "description") {
				changes = append(changes, fmt.Sprintf("description → %q", truncate(strParam(p, "description"), 120)))
			}
			if has(p, "category") {
				changes = append(changes, fmt.Sprintf("category %q → %q", svc.Category, strParam(p, "category")))
			}
			if has(p, "tags") {
				changes = append(changes, fmt.Sprintf("tags [%s] → [%s]",
					strings.Join(svc.Tags, ", "), strings.Join(strSliceParam(p, "tags"), ", ")))
			}
			if len(changes) == 0 {
				return "", fmt.Errorf("no fields to change were provided")
			}
			preview := fmt.Sprintf("Update service #%d %q:\n  %s", svc.ID, svc.Name, strings.Join(changes, "\n  "))
			return prepare(ctx, env, "update_service", p, preview)
		},
	})

	r.Register(&Tool{
		Name:        "archive_service",
		Description: "Archive (hide) a service. Soft delete; data and history are kept." + twoPhaseNote,
		InputSchema: obj(map[string]any{
			"service_id": integer("ID of the service to archive"),
		}, "service_id"),
		Roles: employee,
		Handler: func(ctx context.Context, env *Env, p map[string]any) (string, error) {
			id, _ := intParam(p, "service_id")
			svc, err := env.Store.GetService(ctx, env.CompanyID, id)
			if err != nil {
				return "", err
			}
			if svc == nil {
				return "", fmt.Errorf("service #%d not found", id)
			}
			preview := fmt.Sprintf("Archive service #%d %q (price %s).", svc.ID, svc.Name, fmtPrice(svc.Price))
			return prepare(ctx, env, "archive_service", p, preview)
		},
	})

	r.Register(&Tool{
		Name: "set_business_rule",
		Description: "Create or replace one piece of business information (opening hours, address, " +
			"delivery policy, FAQ entries...). Keys are short identifiers like \"hours\", " +
			"\"delivery_policy\" or \"faq:returns\"." + twoPhaseNote,
		InputSchema: obj(map[string]any{
			"key":   str("Short identifier of the rule"),
			"value": str("The full text customers should be told"),
		}, "key", "value"),
		Roles: employee,
		Handler: func(ctx context.Context, env *Env, p map[string]any) (string, error) {
			key := strParam(p, "key")
			if key == "" {
				return "", fmt.Errorf("key must not be empty")
			}
			old, err := env.Store.GetBusinessRule(ctx, env.CompanyID, key)
			if err != nil {
				return "", err
			}
			oldVal := "(not set)"
			if old != nil {
				oldVal = old.Value
			}
			preview := fmt.Sprintf("Business rule %q:\n  old: %s\n  new: %s", key, oldVal, strParam(p, "value"))
			return prepare(ctx, env, "set_business_rule", p, preview)
		},
	})
}

func checkPriceStock(price, stock float64) error {
	if price < 0 {
		return fmt.Errorf("price must be >= 0")
	}
	if stock < 0 {
		return fmt.Errorf("stock must be >= 0")
	}
	return nil
}

// ---- apply functions: run inside the confirm_write transaction ------------
// State may have changed between prepare and confirm, so every apply
// re-reads current rows (FOR UPDATE) and re-checks its invariants.

func applyCreateProduct(ctx context.Context, tx pgx.Tx, env *Env, p map[string]any) (*applyResult, error) {
	price, _ := floatParam(p, "price")
	stock, _ := intParam(p, "stock")
	if err := checkPriceStock(price, float64(stock)); err != nil {
		return nil, err
	}
	prod := &store.Product{
		CompanyID:   env.CompanyID,
		Name:        strParam(p, "name"),
		Description: strParam(p, "description"),
		Category:    strParam(p, "category"),
		Tags:        strSliceParam(p, "tags"),
		Price:       price,
		Stock:       int(stock),
	}
	id, err := env.Store.InsertProductTx(ctx, tx, prod)
	if err != nil {
		return nil, err
	}
	prod.ID = id
	return &applyResult{
		message:    fmt.Sprintf("product #%d %q created", id, prod.Name),
		entityType: "product", entityID: &id, after: prod,
	}, nil
}

func applyUpdateProduct(ctx context.Context, tx pgx.Tx, env *Env, p map[string]any) (*applyResult, error) {
	id, _ := intParam(p, "product_id")
	prod, err := env.Store.GetProductForUpdate(ctx, tx, env.CompanyID, id)
	if err != nil {
		return nil, err
	}
	if prod == nil {
		return nil, fmt.Errorf("product #%d no longer exists", id)
	}
	before := *prod
	if has(p, "name") {
		prod.Name = strParam(p, "name")
	}
	if price, ok := floatParam(p, "price"); ok {
		if price < 0 {
			return nil, fmt.Errorf("price must be >= 0")
		}
		prod.Price = price
	}
	if has(p, "description") {
		prod.Description = strParam(p, "description")
	}
	if has(p, "category") {
		prod.Category = strParam(p, "category")
	}
	if has(p, "tags") {
		prod.Tags = strSliceParam(p, "tags")
	}
	if err := env.Store.SaveProductTx(ctx, tx, prod); err != nil {
		return nil, err
	}
	return &applyResult{
		message:    fmt.Sprintf("product #%d %q updated", prod.ID, prod.Name),
		entityType: "product", entityID: &prod.ID, before: before, after: prod,
	}, nil
}

func applyUpdateStock(ctx context.Context, tx pgx.Tx, env *Env, p map[string]any) (*applyResult, error) {
	id, _ := intParam(p, "product_id")
	prod, err := env.Store.GetProductForUpdate(ctx, tx, env.CompanyID, id)
	if err != nil {
		return nil, err
	}
	if prod == nil {
		return nil, fmt.Errorf("product #%d no longer exists", id)
	}
	before := *prod
	newStock := int64(prod.Stock)
	if has(p, "delta") {
		d, _ := intParam(p, "delta")
		newStock += d
	} else {
		newStock, _ = intParam(p, "absolute")
	}
	if newStock < 0 {
		return nil, fmt.Errorf("stock would go below 0 (it is now %d — it may have changed since the preview)", prod.Stock)
	}
	prod.Stock = int(newStock)
	if err := env.Store.SaveProductTx(ctx, tx, prod); err != nil {
		return nil, err
	}
	return &applyResult{
		message:    fmt.Sprintf("stock of product #%d %q is now %d", prod.ID, prod.Name, prod.Stock),
		entityType: "product", entityID: &prod.ID, before: before, after: prod,
	}, nil
}

func applyArchiveProduct(ctx context.Context, tx pgx.Tx, env *Env, p map[string]any) (*applyResult, error) {
	id, _ := intParam(p, "product_id")
	prod, err := env.Store.GetProductForUpdate(ctx, tx, env.CompanyID, id)
	if err != nil {
		return nil, err
	}
	if prod == nil {
		return nil, fmt.Errorf("product #%d no longer exists", id)
	}
	if err := env.Store.ArchiveProductTx(ctx, tx, env.CompanyID, id); err != nil {
		return nil, err
	}
	return &applyResult{
		message:    fmt.Sprintf("product #%d %q archived", prod.ID, prod.Name),
		entityType: "product", entityID: &prod.ID, before: prod,
		after: map[string]any{"is_active": false},
	}, nil
}

func applyCreateService(ctx context.Context, tx pgx.Tx, env *Env, p map[string]any) (*applyResult, error) {
	price, _ := floatParam(p, "price")
	if price < 0 {
		return nil, fmt.Errorf("price must be >= 0")
	}
	svc := &store.Service{
		CompanyID:   env.CompanyID,
		Name:        strParam(p, "name"),
		Description: strParam(p, "description"),
		Category:    strParam(p, "category"),
		Tags:        strSliceParam(p, "tags"),
		Price:       price,
	}
	id, err := env.Store.InsertServiceTx(ctx, tx, svc)
	if err != nil {
		return nil, err
	}
	svc.ID = id
	return &applyResult{
		message:    fmt.Sprintf("service #%d %q created", id, svc.Name),
		entityType: "service", entityID: &id, after: svc,
	}, nil
}

func applyUpdateService(ctx context.Context, tx pgx.Tx, env *Env, p map[string]any) (*applyResult, error) {
	id, _ := intParam(p, "service_id")
	svc, err := env.Store.GetServiceForUpdate(ctx, tx, env.CompanyID, id)
	if err != nil {
		return nil, err
	}
	if svc == nil {
		return nil, fmt.Errorf("service #%d no longer exists", id)
	}
	before := *svc
	if has(p, "name") {
		svc.Name = strParam(p, "name")
	}
	if price, ok := floatParam(p, "price"); ok {
		if price < 0 {
			return nil, fmt.Errorf("price must be >= 0")
		}
		svc.Price = price
	}
	if has(p, "description") {
		svc.Description = strParam(p, "description")
	}
	if has(p, "category") {
		svc.Category = strParam(p, "category")
	}
	if has(p, "tags") {
		svc.Tags = strSliceParam(p, "tags")
	}
	if err := env.Store.SaveServiceTx(ctx, tx, svc); err != nil {
		return nil, err
	}
	return &applyResult{
		message:    fmt.Sprintf("service #%d %q updated", svc.ID, svc.Name),
		entityType: "service", entityID: &svc.ID, before: before, after: svc,
	}, nil
}

func applyArchiveService(ctx context.Context, tx pgx.Tx, env *Env, p map[string]any) (*applyResult, error) {
	id, _ := intParam(p, "service_id")
	svc, err := env.Store.GetServiceForUpdate(ctx, tx, env.CompanyID, id)
	if err != nil {
		return nil, err
	}
	if svc == nil {
		return nil, fmt.Errorf("service #%d no longer exists", id)
	}
	if err := env.Store.ArchiveServiceTx(ctx, tx, env.CompanyID, id); err != nil {
		return nil, err
	}
	return &applyResult{
		message:    fmt.Sprintf("service #%d %q archived", svc.ID, svc.Name),
		entityType: "service", entityID: &svc.ID, before: svc,
		after: map[string]any{"is_active": false},
	}, nil
}

func applySetBusinessRule(ctx context.Context, tx pgx.Tx, env *Env, p map[string]any) (*applyResult, error) {
	key := strParam(p, "key")
	old, err := env.Store.GetBusinessRule(ctx, env.CompanyID, key)
	if err != nil {
		return nil, err
	}
	if err := env.Store.UpsertBusinessRuleTx(ctx, tx, env.CompanyID, key, strParam(p, "value")); err != nil {
		return nil, err
	}
	var before any
	if old != nil {
		before = old
	}
	return &applyResult{
		message:    fmt.Sprintf("business rule %q saved", key),
		entityType: "business_rule", before: before,
		after: store.BusinessRule{Key: key, Value: strParam(p, "value")},
	}, nil
}
