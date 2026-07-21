package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/filipekdick/go-harness-whatsmeow/internal/store"
)

var readRoles = []store.Role{store.RoleCustomer, store.RoleEmployee}

// RegisterReadOnlyTools installs the tools shared by customers and employees.
// No handler in this set can mutate business data.
func RegisterReadOnlyTools(registry *Registry) {
	registry.Register(&Tool{
		Name:        "search_catalog",
		Description: "Search active products and services in this company's catalog by name, description, category, or tag. Use this before requesting a product ID.",
		InputSchema: objectSchema(map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Words describing the product or service to find.",
			},
			"kind": map[string]any{
				"type":        "string",
				"enum":        []any{"all", "product", "service"},
				"description": "Optional catalog item type; defaults to all.",
			},
			"category": map[string]any{
				"type":        "string",
				"description": "Optional exact category filter, case-insensitive.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     20,
				"description": "Maximum number of results; defaults to 10 and is capped at 20.",
			},
		}, []string{"query"}),
		Roles:   readRoles,
		Handler: searchCatalog,
	})

	registry.Register(&Tool{
		Name:        "get_product",
		Description: "Get the current details, price, and stock of one active product by its catalog ID.",
		InputSchema: objectSchema(map[string]any{
			"product_id": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "Product ID returned by search_catalog.",
			},
		}, []string{"product_id"}),
		Roles:   readRoles,
		Handler: getProduct,
	})

	registry.Register(&Tool{
		Name:        "check_stock",
		Description: "Check the current stock quantity of one active product by its catalog ID.",
		InputSchema: objectSchema(map[string]any{
			"product_id": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "Product ID returned by search_catalog.",
			},
		}, []string{"product_id"}),
		Roles:   readRoles,
		Handler: checkStock,
	})

	registry.Register(&Tool{
		Name:        "get_order_status",
		Description: "Get an order's current status and items. Customers may only access their own orders; employees may access any order in the same company.",
		InputSchema: objectSchema(map[string]any{
			"order_id": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "Numeric order ID.",
			},
		}, []string{"order_id"}),
		Roles:   readRoles,
		Handler: getOrderStatus,
	})

	registry.Register(&Tool{
		Name:        "get_business_rule",
		Description: "Get one active company rule or FAQ entry by its exact key, such as hours, address, delivery_policy, or faq:returns.",
		InputSchema: objectSchema(map[string]any{
			"key": map[string]any{
				"type":        "string",
				"description": "Exact business rule key.",
			},
		}, []string{"key"}),
		Roles:   readRoles,
		Handler: getBusinessRule,
	})
}

func searchCatalog(ctx context.Context, env *Env, params map[string]any) (string, error) {
	st, err := readStore(env)
	if err != nil {
		return "", err
	}
	query := strings.TrimSpace(params["query"].(string))
	if query == "" {
		return "", errors.New("query must not be empty")
	}
	search := store.CatalogSearch{Query: query}
	if kind, ok := params["kind"].(string); ok {
		search.Kind = kind
	}
	if category, ok := params["category"].(string); ok {
		search.Category = category
	}
	if limit, ok := params["limit"].(float64); ok {
		search.Limit = int(limit)
	}
	items, err := st.SearchCatalog(ctx, env.CompanyID, search)
	if err != nil {
		return "", err
	}
	return jsonResult(struct {
		Items []store.CatalogItem `json:"items"`
	}{Items: items})
}

func getProduct(ctx context.Context, env *Env, params map[string]any) (string, error) {
	st, err := readStore(env)
	if err != nil {
		return "", err
	}
	productID := integerParam(params, "product_id")
	product, err := st.GetProduct(ctx, env.CompanyID, productID)
	if err != nil {
		return "", err
	}
	if product == nil {
		return "", fmt.Errorf("product %d not found", productID)
	}
	return jsonResult(product)
}

func checkStock(ctx context.Context, env *Env, params map[string]any) (string, error) {
	st, err := readStore(env)
	if err != nil {
		return "", err
	}
	productID := integerParam(params, "product_id")
	stock, err := st.GetProductStock(ctx, env.CompanyID, productID)
	if err != nil {
		return "", err
	}
	if stock == nil {
		return "", fmt.Errorf("product %d not found", productID)
	}
	return jsonResult(stock)
}

func getOrderStatus(ctx context.Context, env *Env, params map[string]any) (string, error) {
	st, err := readStore(env)
	if err != nil {
		return "", err
	}
	orderID := integerParam(params, "order_id")

	var order *store.Order
	if env.EffectiveRole == store.RoleEmployee {
		order, err = st.GetOrder(ctx, env.CompanyID, orderID)
	} else {
		if env.User == nil || env.User.ID == 0 {
			return "", errors.New("customer identity is required to access an order")
		}
		order, err = st.GetOrderForCustomer(ctx, env.CompanyID, orderID, env.User.ID)
	}
	if err != nil {
		return "", err
	}
	if order == nil {
		return "", fmt.Errorf("order %d not found", orderID)
	}
	return jsonResult(order)
}

func getBusinessRule(ctx context.Context, env *Env, params map[string]any) (string, error) {
	st, err := readStore(env)
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(params["key"].(string))
	if key == "" {
		return "", errors.New("key must not be empty")
	}
	rule, err := st.GetBusinessRule(ctx, env.CompanyID, key)
	if err != nil {
		return "", err
	}
	if rule == nil {
		return "", fmt.Errorf("business rule %q not found", key)
	}
	return jsonResult(rule)
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func readStore(env *Env) (ReadStore, error) {
	if env == nil || env.Store == nil {
		return nil, errors.New("tool store is not configured")
	}
	return env.Store, nil
}

func integerParam(params map[string]any, name string) int64 {
	return int64(params[name].(float64))
}

func jsonResult(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode tool result: %w", err)
	}
	return string(raw), nil
}
