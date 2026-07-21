package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/filipekdick/go-harness-whatsmeow/internal/store"
)

type fakeReadStore struct {
	catalog []store.CatalogItem
	product *store.Product
	stock   *store.ProductStock
	order   *store.Order
	rule    *store.BusinessRule

	searchCompanyID   int64
	search            store.CatalogSearch
	productCompanyID  int64
	stockCompanyID    int64
	orderCompanyID    int64
	orderCustomerID   int64
	employeeOrderRead bool
	customerOrderRead bool
	ruleCompanyID     int64
	ruleKey           string
}

func (f *fakeReadStore) SearchCatalog(_ context.Context, companyID int64, search store.CatalogSearch) ([]store.CatalogItem, error) {
	f.searchCompanyID = companyID
	f.search = search
	return f.catalog, nil
}

func (f *fakeReadStore) GetProduct(_ context.Context, companyID, _ int64) (*store.Product, error) {
	f.productCompanyID = companyID
	return f.product, nil
}

func (f *fakeReadStore) GetProductStock(_ context.Context, companyID, _ int64) (*store.ProductStock, error) {
	f.stockCompanyID = companyID
	return f.stock, nil
}

func (f *fakeReadStore) GetOrder(_ context.Context, companyID, _ int64) (*store.Order, error) {
	f.orderCompanyID = companyID
	f.employeeOrderRead = true
	return f.order, nil
}

func (f *fakeReadStore) GetOrderForCustomer(_ context.Context, companyID, _ int64, customerID int64) (*store.Order, error) {
	f.orderCompanyID = companyID
	f.orderCustomerID = customerID
	f.customerOrderRead = true
	return f.order, nil
}

func (f *fakeReadStore) GetBusinessRule(_ context.Context, companyID int64, key string) (*store.BusinessRule, error) {
	f.ruleCompanyID = companyID
	f.ruleKey = key
	return f.rule, nil
}

func readOnlyRegistry() *Registry {
	registry := NewRegistry(nil)
	RegisterReadOnlyTools(registry)
	return registry
}

func TestRegisterReadOnlyToolsForBothRoles(t *testing.T) {
	registry := readOnlyRegistry()
	want := []string{
		"search_catalog",
		"get_product",
		"check_stock",
		"get_order_status",
		"get_business_rule",
	}

	for _, role := range []store.Role{store.RoleCustomer, store.RoleEmployee} {
		defs := registry.DefsFor(role)
		if len(defs) != len(want) {
			t.Fatalf("role %s: got %d definitions, want %d", role, len(defs), len(want))
		}
		for i, name := range want {
			if defs[i].Name != name {
				t.Errorf("role %s: definition %d is %q, want %q", role, i, defs[i].Name, name)
			}
		}
	}
}

func TestSearchCatalogUsesCompanyScopeAndFilters(t *testing.T) {
	fake := &fakeReadStore{catalog: []store.CatalogItem{{
		Kind: "product", ID: 9, Name: "Coke 350ml", Price: "5.50",
	}}}
	env := &Env{CompanyID: 42, EffectiveRole: store.RoleCustomer, Store: fake}

	out, isErr := readOnlyRegistry().Execute(context.Background(), env, "search_catalog",
		json.RawMessage(`{"query":" coke ","kind":"product","category":"drinks","limit":5}`))
	if isErr {
		t.Fatalf("search_catalog failed: %s", out)
	}
	if fake.searchCompanyID != 42 {
		t.Fatalf("store received company %d, want 42", fake.searchCompanyID)
	}
	if fake.search.Query != "coke" || fake.search.Kind != "product" || fake.search.Category != "drinks" || fake.search.Limit != 5 {
		t.Fatalf("unexpected search: %+v", fake.search)
	}
	if !strings.Contains(out, `"name":"Coke 350ml"`) || !strings.Contains(out, `"items"`) {
		t.Fatalf("unexpected JSON result: %s", out)
	}
}

func TestProductAndStockReadsUseCompanyScope(t *testing.T) {
	fake := &fakeReadStore{
		product: &store.Product{ID: 3, CompanyID: 7, Name: "Coffee", Price: "12.00", Stock: 4},
		stock:   &store.ProductStock{ProductID: 3, Name: "Coffee", Stock: 4},
	}
	env := &Env{CompanyID: 7, EffectiveRole: store.RoleCustomer, Store: fake}
	registry := readOnlyRegistry()

	out, isErr := registry.Execute(context.Background(), env, "get_product", json.RawMessage(`{"product_id":3}`))
	if isErr || !strings.Contains(out, `"name":"Coffee"`) {
		t.Fatalf("get_product: got %q (isErr=%v)", out, isErr)
	}
	out, isErr = registry.Execute(context.Background(), env, "check_stock", json.RawMessage(`{"product_id":3}`))
	if isErr || !strings.Contains(out, `"stock":4`) {
		t.Fatalf("check_stock: got %q (isErr=%v)", out, isErr)
	}
	if fake.productCompanyID != 7 || fake.stockCompanyID != 7 {
		t.Fatalf("company scope not propagated: product=%d stock=%d", fake.productCompanyID, fake.stockCompanyID)
	}
}

func TestGetOrderStatusScopesCustomersToTheirOwnUser(t *testing.T) {
	fake := &fakeReadStore{order: &store.Order{ID: 88, CompanyID: 12, CustomerID: 34, Status: "SHIPPED", Total: "20.00"}}
	env := &Env{
		CompanyID:     12,
		EffectiveRole: store.RoleCustomer,
		User:          &store.User{ID: 34, CompanyID: 12, Role: store.RoleCustomer},
		Store:         fake,
	}

	out, isErr := readOnlyRegistry().Execute(context.Background(), env, "get_order_status", json.RawMessage(`{"order_id":88}`))
	if isErr || !strings.Contains(out, `"status":"SHIPPED"`) {
		t.Fatalf("get_order_status: got %q (isErr=%v)", out, isErr)
	}
	if !fake.customerOrderRead || fake.employeeOrderRead {
		t.Fatalf("customer used wrong order query: customer=%v employee=%v", fake.customerOrderRead, fake.employeeOrderRead)
	}
	if fake.orderCompanyID != 12 || fake.orderCustomerID != 34 {
		t.Fatalf("unexpected order scope: company=%d customer=%d", fake.orderCompanyID, fake.orderCustomerID)
	}
}

func TestGetOrderStatusAllowsEmployeeWithinCompany(t *testing.T) {
	fake := &fakeReadStore{order: &store.Order{ID: 88, CompanyID: 12, CustomerID: 34, Status: "PREPARING", Total: "20.00"}}
	env := &Env{CompanyID: 12, EffectiveRole: store.RoleEmployee, Store: fake}

	out, isErr := readOnlyRegistry().Execute(context.Background(), env, "get_order_status", json.RawMessage(`{"order_id":88}`))
	if isErr || !strings.Contains(out, `"status":"PREPARING"`) {
		t.Fatalf("get_order_status: got %q (isErr=%v)", out, isErr)
	}
	if !fake.employeeOrderRead || fake.customerOrderRead {
		t.Fatalf("employee used wrong order query: customer=%v employee=%v", fake.customerOrderRead, fake.employeeOrderRead)
	}
	if fake.orderCompanyID != 12 {
		t.Fatalf("store received company %d, want 12", fake.orderCompanyID)
	}
}

func TestReadOnlyToolsReturnSafeNotFoundErrors(t *testing.T) {
	fake := &fakeReadStore{}
	env := &Env{CompanyID: 5, EffectiveRole: store.RoleCustomer, Store: fake}
	registry := readOnlyRegistry()

	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"get_product", `{"product_id":999}`, "product 999 not found"},
		{"check_stock", `{"product_id":999}`, "product 999 not found"},
		{"get_business_rule", `{"key":"hours"}`, `business rule "hours" not found`},
	} {
		out, isErr := registry.Execute(context.Background(), env, tc.name, json.RawMessage(tc.input))
		if !isErr || !strings.Contains(out, tc.want) {
			t.Errorf("%s: got %q (isErr=%v), want %q", tc.name, out, isErr, tc.want)
		}
	}
}

func TestReadOnlyToolsRejectOutOfRangeNumbersBeforeStoreAccess(t *testing.T) {
	fake := &fakeReadStore{}
	env := &Env{CompanyID: 5, EffectiveRole: store.RoleCustomer, Store: fake}
	registry := readOnlyRegistry()

	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"get_product", `{"product_id":0}`, "must be at least 1"},
		{"get_order_status", `{"order_id":-1}`, "must be at least 1"},
		{"search_catalog", `{"query":"coffee","limit":21}`, "must be at most 20"},
	} {
		out, isErr := registry.Execute(context.Background(), env, tc.name, json.RawMessage(tc.input))
		if !isErr || !strings.Contains(out, tc.want) {
			t.Errorf("%s: got %q (isErr=%v), want %q", tc.name, out, isErr, tc.want)
		}
	}
	if fake.productCompanyID != 0 || fake.orderCompanyID != 0 || fake.searchCompanyID != 0 {
		t.Fatal("a store method ran for invalid tool input")
	}
}

func TestGetBusinessRuleUsesCompanyScope(t *testing.T) {
	fake := &fakeReadStore{rule: &store.BusinessRule{ID: 1, CompanyID: 21, Key: "hours", Value: "Mon-Fri 9-18"}}
	env := &Env{CompanyID: 21, EffectiveRole: store.RoleCustomer, Store: fake}

	out, isErr := readOnlyRegistry().Execute(context.Background(), env, "get_business_rule", json.RawMessage(`{"key":" hours "}`))
	if isErr || !strings.Contains(out, `"value":"Mon-Fri 9-18"`) {
		t.Fatalf("get_business_rule: got %q (isErr=%v)", out, isErr)
	}
	if fake.ruleCompanyID != 21 || fake.ruleKey != "hours" {
		t.Fatalf("unexpected rule scope: company=%d key=%q", fake.ruleCompanyID, fake.ruleKey)
	}
}
