package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/filipekdick/go-harness-whatsmeow/internal/store"
)

func testRegistry() *Registry {
	r := NewRegistry(nil)
	r.Register(&Tool{
		Name:        "check_stock",
		Description: "read-only",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"product_query": map[string]any{"type": "string"},
			},
			"required": []string{"product_query"},
		},
		Roles: []store.Role{store.RoleCustomer, store.RoleEmployee},
		Handler: func(ctx context.Context, env *Env, p map[string]any) (string, error) {
			return "ok:" + p["product_query"].(string), nil
		},
	})
	r.Register(&Tool{
		Name:        "update_stock",
		Description: "write",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"product_id": map[string]any{"type": "integer"},
				"absolute":   map[string]any{"type": "integer"},
			},
			"required": []string{"product_id"},
		},
		Roles: []store.Role{store.RoleEmployee},
		Handler: func(ctx context.Context, env *Env, p map[string]any) (string, error) {
			return "written", nil
		},
	})
	return r
}

func TestRoleGatingExcludesWriteToolsFromCustomerPayload(t *testing.T) {
	r := testRegistry()
	defs := r.DefsFor(store.RoleCustomer)
	for _, d := range defs {
		if d.Name == "update_stock" {
			t.Fatal("write tool leaked into customer tool definitions")
		}
	}
	if len(defs) != 1 || defs[0].Name != "check_stock" {
		t.Fatalf("unexpected customer defs: %+v", defs)
	}
	if len(r.DefsFor(store.RoleEmployee)) != 2 {
		t.Fatal("employee should see both tools")
	}
}

func TestExecuteBlocksRoleViolation(t *testing.T) {
	r := testRegistry()
	env := &Env{EffectiveRole: store.RoleCustomer}
	out, isErr := r.Execute(context.Background(), env, "update_stock",
		json.RawMessage(`{"product_id": 1}`))
	if !isErr || !strings.Contains(out, "not available") {
		t.Fatalf("expected role block, got %q (isErr=%v)", out, isErr)
	}
}

func TestExecuteValidatesParams(t *testing.T) {
	r := testRegistry()
	env := &Env{EffectiveRole: store.RoleEmployee}

	cases := []struct {
		name  string
		tool  string
		input string
		want  string
	}{
		{"missing required", "check_stock", `{}`, "missing required"},
		{"unknown param", "check_stock", `{"product_query":"x","bogus":1}`, "unknown parameter"},
		{"wrong type", "check_stock", `{"product_query": 5}`, "must be a string"},
		{"non-integer", "update_stock", `{"product_id": 1.5}`, "must be an integer"},
		{"unknown tool", "drop_tables", `{}`, "unknown tool"},
	}
	for _, tc := range cases {
		out, isErr := r.Execute(context.Background(), env, tc.tool, json.RawMessage(tc.input))
		if !isErr || !strings.Contains(out, tc.want) {
			t.Errorf("%s: got %q (isErr=%v), want substring %q", tc.name, out, isErr, tc.want)
		}
	}

	out, isErr := r.Execute(context.Background(), env, "check_stock",
		json.RawMessage(`{"product_query":"coke"}`))
	if isErr || out != "ok:coke" {
		t.Fatalf("valid call failed: %q (isErr=%v)", out, isErr)
	}
}

func TestExecuteRecoversFromPanic(t *testing.T) {
	r := NewRegistry(nil)
	r.Register(&Tool{
		Name:        "boom",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Roles:       []store.Role{store.RoleEmployee},
		Handler: func(ctx context.Context, env *Env, p map[string]any) (string, error) {
			panic("kaboom")
		},
	})
	env := &Env{EffectiveRole: store.RoleEmployee}
	out, isErr := r.Execute(context.Background(), env, "boom", json.RawMessage(`{}`))
	if !isErr || !strings.Contains(out, "internal error") {
		t.Fatalf("panic not recovered: %q (isErr=%v)", out, isErr)
	}
}
