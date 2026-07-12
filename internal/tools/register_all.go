package tools

import "github.com/filipekdick/go-harness-whatsmeow/internal/config"

// RegisterAll wires every concrete tool into the registry.
//
// Customer-facing (read-only, visible to CUSTOMER and EMPLOYEE):
//   check_stock, check_price, search_catalog, get_business_info,
//   check_order_status, escalate_to_human
//
// Employee-only:
//   list_products, list_services (reads)
//   create/update/archive product & service, update_stock,
//   set_business_rule (two-phase prepares)
//   confirm_write, cancel_write (the only commit path)
func RegisterAll(r *Registry, cfg *config.Config) {
	registerCustomerTools(r)
	registerEmployeeTools(r, cfg.PendingWriteTTL)
	registerConfirmTools(r)
}
