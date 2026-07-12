package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/filipekdick/go-harness-whatsmeow/internal/store"
)

// registerCustomerTools adds the read-only tool set. These are visible to
// customers AND employees (an employee may legitimately check stock or
// prices while chatting on the employee line).
func registerCustomerTools(r *Registry) {
	bothRoles := []store.Role{store.RoleCustomer, store.RoleEmployee}

	r.Register(&Tool{
		Name: "check_stock",
		Description: "Check how many units of a product are in stock. " +
			"Use a short product name or keyword as the query.",
		InputSchema: obj(map[string]any{
			"product_query": str("Product name or keyword to look up"),
		}, "product_query"),
		Roles: bothRoles,
		Handler: func(ctx context.Context, env *Env, p map[string]any) (string, error) {
			products, err := env.Store.FindProducts(ctx, env.CompanyID, strParam(p, "product_query"), 5)
			if err != nil {
				return "", err
			}
			if len(products) == 0 {
				return fmt.Sprintf("no products match %q", strParam(p, "product_query")), nil
			}
			var b strings.Builder
			for _, prod := range products {
				fmt.Fprintf(&b, "%s: %d in stock (price %s)\n", prod.Name, prod.Stock, fmtPrice(prod.Price))
			}
			return b.String(), nil
		},
	})

	r.Register(&Tool{
		Name: "check_price",
		Description: "Check the price of a product. " +
			"Use a short product name or keyword as the query.",
		InputSchema: obj(map[string]any{
			"product_query": str("Product name or keyword to look up"),
		}, "product_query"),
		Roles: bothRoles,
		Handler: func(ctx context.Context, env *Env, p map[string]any) (string, error) {
			products, err := env.Store.FindProducts(ctx, env.CompanyID, strParam(p, "product_query"), 5)
			if err != nil {
				return "", err
			}
			if len(products) == 0 {
				return fmt.Sprintf("no products match %q", strParam(p, "product_query")), nil
			}
			var b strings.Builder
			for _, prod := range products {
				avail := "in stock"
				if prod.Stock <= 0 {
					avail = "OUT OF STOCK"
				}
				fmt.Fprintf(&b, "%s: price %s (%s)\n", prod.Name, fmtPrice(prod.Price), avail)
			}
			return b.String(), nil
		},
	})

	r.Register(&Tool{
		Name: "search_catalog",
		Description: "Search the catalog of products and services with optional filters. " +
			"Returns rich rows (name, price, stock, category, tags, description) so you can " +
			"compare options and make recommendations. All filters are optional; combine them freely.",
		InputSchema: obj(map[string]any{
			"category":  str("Filter by category (substring match)"),
			"max_price": num("Only items at or below this price"),
			"tags":      strArray("Only items having at least one of these tags"),
			"keywords":  str("Free-text match on name and description"),
		}),
		Roles: bothRoles,
		Handler: func(ctx context.Context, env *Env, p map[string]any) (string, error) {
			maxPrice, _ := floatParam(p, "max_price")
			filter := store.CatalogFilter{
				Category: strParam(p, "category"),
				MaxPrice: maxPrice,
				Tags:     strSliceParam(p, "tags"),
				Keywords: strParam(p, "keywords"),
				Limit:    15,
			}
			products, err := env.Store.SearchProducts(ctx, env.CompanyID, filter)
			if err != nil {
				return "", err
			}
			services, err := env.Store.SearchServices(ctx, env.CompanyID, filter)
			if err != nil {
				return "", err
			}
			if len(products) == 0 && len(services) == 0 {
				return "nothing in the catalog matches those filters", nil
			}
			var b strings.Builder
			if len(products) > 0 {
				b.WriteString("PRODUCTS:\n")
				for _, prod := range products {
					formatProduct(&b, prod, env.EffectiveRole == store.RoleEmployee)
				}
			}
			if len(services) > 0 {
				b.WriteString("SERVICES:\n")
				for _, svc := range services {
					formatService(&b, svc, env.EffectiveRole == store.RoleEmployee)
				}
			}
			return b.String(), nil
		},
	})

	r.Register(&Tool{
		Name: "get_business_info",
		Description: "Look up business information: opening hours, address, delivery policy, " +
			"payment methods, FAQ answers, etc. Pass a topic keyword (e.g. \"hours\", \"delivery\").",
		InputSchema: obj(map[string]any{
			"topic": str("Topic keyword; empty returns all stored info"),
		}),
		Roles: bothRoles,
		Handler: func(ctx context.Context, env *Env, p map[string]any) (string, error) {
			topic := strParam(p, "topic")
			rules, err := env.Store.MatchBusinessRules(ctx, env.CompanyID, topic)
			if err != nil {
				return "", err
			}
			if len(rules) == 0 {
				keys, err := env.Store.ListBusinessRuleKeys(ctx, env.CompanyID)
				if err != nil {
					return "", err
				}
				if len(keys) == 0 {
					return "no business information has been stored yet", nil
				}
				return fmt.Sprintf("nothing stored for topic %q; available topics: %s",
					topic, strings.Join(keys, ", ")), nil
			}
			var b strings.Builder
			for _, rule := range rules {
				fmt.Fprintf(&b, "%s: %s\n", rule.Key, rule.Value)
			}
			return b.String(), nil
		},
	})

	r.Register(&Tool{
		Name: "check_order_status",
		Description: "Look up the status of an order by its numeric order ID. " +
			"Customers can only see their own orders.",
		InputSchema: obj(map[string]any{
			"order_id": integer("The numeric order ID"),
		}, "order_id"),
		Roles: bothRoles,
		Handler: func(ctx context.Context, env *Env, p map[string]any) (string, error) {
			orderID, _ := intParam(p, "order_id")
			order, items, err := env.Store.GetOrder(ctx, env.CompanyID, orderID)
			if err != nil {
				return "", err
			}
			// A customer must never learn anything about someone else's
			// order — including whether it exists. Same message either way.
			if order == nil ||
				(env.EffectiveRole == store.RoleCustomer && order.CustomerID != env.User.ID) {
				return fmt.Sprintf("no order #%d found for this customer", orderID), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "order #%d — status %s, total %s, placed %s\n",
				order.ID, order.Status, fmtPrice(order.Total), order.CreatedAt.Format("2006-01-02"))
			for _, it := range items {
				fmt.Fprintf(&b, "  %dx %s @ %s\n", it.Quantity, it.ProductName, fmtPrice(it.UnitPrice))
			}
			if order.Notes != "" {
				fmt.Fprintf(&b, "  notes: %s\n", order.Notes)
			}
			return b.String(), nil
		},
	})

	r.Register(&Tool{
		Name: "escalate_to_human",
		Description: "Hand the conversation over to a human. Use when the customer asks for a " +
			"person, is upset, or you cannot resolve their request with the available tools. " +
			"Write a concise summary of the conversation and what the human needs to do.",
		InputSchema: obj(map[string]any{
			"conversation_summary": str("What happened and what the human should do next"),
		}, "conversation_summary"),
		Roles: bothRoles,
		Handler: func(ctx context.Context, env *Env, p map[string]any) (string, error) {
			id, err := env.Store.CreateEscalation(ctx, env.CompanyID, env.ConversationID,
				strParam(p, "conversation_summary"))
			if err != nil {
				return "", err
			}
			if err := env.Store.SetEscalated(ctx, env.CompanyID, env.ConversationID); err != nil {
				return "", err
			}
			return fmt.Sprintf("escalation #%d logged. Now tell the customer that a human "+
				"will take over this conversation shortly.", id), nil
		},
	})
}
