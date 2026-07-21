// Package tools holds the tool registry: definitions, role gating, parameter
// validation, and panic-safe execution. Concrete tools (customer.go,
// employee.go) register themselves into a Registry at startup.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/filipekdick/go-harness-whatsmeow/internal/llm"
	"github.com/filipekdick/go-harness-whatsmeow/internal/store"
)

// ReadStore is the tenant-scoped data surface available to read-only tools.
// Keeping this interface in the consumer package makes handlers testable while
// *store.Store remains the production implementation.
type ReadStore interface {
	SearchCatalog(ctx context.Context, companyID int64, search store.CatalogSearch) ([]store.CatalogItem, error)
	GetProduct(ctx context.Context, companyID, productID int64) (*store.Product, error)
	GetProductStock(ctx context.Context, companyID, productID int64) (*store.ProductStock, error)
	GetOrder(ctx context.Context, companyID, orderID int64) (*store.Order, error)
	GetOrderForCustomer(ctx context.Context, companyID, orderID, customerID int64) (*store.Order, error)
	GetBusinessRule(ctx context.Context, companyID int64, key string) (*store.BusinessRule, error)
}

// Env is everything a tool handler is allowed to know. CompanyID scopes every
// query; handlers must pass it to every store call.
type Env struct {
	CompanyID        int64
	User             *store.User
	EffectiveRole    store.Role
	Channel          store.Channel
	ConversationID   int64
	InboundMessageID int64 // watermark for the two-phase write confirmation
	Store            ReadStore
}

type Handler func(ctx context.Context, env *Env, params map[string]any) (string, error)

type Tool struct {
	Name        string
	Description string
	// InputSchema is a JSON Schema object:
	// {"type":"object","properties":{...},"required":[...]}
	InputSchema map[string]any
	// Roles that may see and call this tool. A tool absent from this list for
	// the sender's role is never sent to the API at all.
	Roles   []store.Role
	Handler Handler
}

func (t *Tool) allowedFor(role store.Role) bool {
	for _, r := range t.Roles {
		if r == role {
			return true
		}
	}
	return false
}

type Registry struct {
	order []string
	tools map[string]*Tool
	log   *slog.Logger
}

func NewRegistry(log *slog.Logger) *Registry {
	if log == nil {
		log = slog.Default()
	}
	return &Registry{tools: map[string]*Tool{}, log: log}
}

func (r *Registry) Register(t *Tool) {
	if _, dup := r.tools[t.Name]; dup {
		panic("duplicate tool registered: " + t.Name)
	}
	r.tools[t.Name] = t
	r.order = append(r.order, t.Name)
}

// DefsFor returns the tool definitions visible to a role. This is the role
// gate: tools not allowed for the role are physically excluded from the API
// payload, not merely forbidden by prompt.
func (r *Registry) DefsFor(role store.Role) []llm.ToolDef {
	var defs []llm.ToolDef
	for _, name := range r.order {
		t := r.tools[name]
		if t.allowedFor(role) {
			defs = append(defs, llm.ToolDef{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.InputSchema,
			})
		}
	}
	return defs
}

// Execute validates and runs one tool call. It never panics and never
// returns a Go error to the caller: every failure becomes an error
// tool_result string for the model, so the loop always continues.
func (r *Registry) Execute(ctx context.Context, env *Env, name string, input json.RawMessage) (result string, isError bool) {
	defer func() {
		if p := recover(); p != nil {
			r.log.Error("tool handler panicked", "tool", name, "panic", p)
			result, isError = "internal error while executing the tool", true
		}
	}()

	t, ok := r.tools[name]
	if !ok {
		return fmt.Sprintf("unknown tool %q", name), true
	}
	if !t.allowedFor(env.EffectiveRole) {
		// Defense in depth: this tool was never in the payload for this role,
		// so reaching here means something is off — refuse and log loudly.
		r.log.Warn("tool call blocked by role gate",
			"tool", name, "role", env.EffectiveRole, "company", env.CompanyID)
		return fmt.Sprintf("tool %q is not available", name), true
	}

	params := map[string]any{}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &params); err != nil {
			return "invalid tool input: not a JSON object", true
		}
	}
	if err := validateParams(t.InputSchema, params); err != nil {
		return "invalid tool input: " + err.Error(), true
	}

	out, err := t.Handler(ctx, env, params)
	if err != nil {
		// Tool errors are data for the model ("product not found"), never a
		// process failure.
		return "error: " + err.Error(), true
	}
	return out, false
}

// validateParams checks params against a JSON Schema subset: required fields
// present, no unknown fields, primitive type checks, enum membership, numeric
// bounds, and arrays of strings. Enough to guarantee handlers see well-shaped input
// before any SQL runs.
func validateParams(schema map[string]any, params map[string]any) error {
	props, _ := schema["properties"].(map[string]any)

	if req, ok := schema["required"].([]string); ok {
		for _, name := range req {
			if _, present := params[name]; !present {
				return fmt.Errorf("missing required parameter %q", name)
			}
		}
	} else if reqAny, ok := schema["required"].([]any); ok {
		for _, r := range reqAny {
			name, _ := r.(string)
			if _, present := params[name]; !present {
				return fmt.Errorf("missing required parameter %q", name)
			}
		}
	}

	for name, value := range params {
		propAny, known := props[name]
		if !known {
			return fmt.Errorf("unknown parameter %q", name)
		}
		prop, _ := propAny.(map[string]any)
		if err := checkType(name, prop, value); err != nil {
			return err
		}
	}
	return nil
}

func checkType(name string, prop map[string]any, value any) error {
	if value == nil {
		return fmt.Errorf("parameter %q is null", name)
	}
	typ, _ := prop["type"].(string)
	switch typ {
	case "string":
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("parameter %q must be a string", name)
		}
		if enum, ok := prop["enum"].([]any); ok {
			for _, e := range enum {
				if e == s {
					return nil
				}
			}
			return fmt.Errorf("parameter %q must be one of %v", name, enum)
		}
	case "number":
		f, ok := value.(float64)
		if !ok {
			return fmt.Errorf("parameter %q must be a number", name)
		}
		if err := checkNumericBounds(name, prop, f); err != nil {
			return err
		}
	case "integer":
		f, ok := value.(float64)
		if !ok || f != float64(int64(f)) {
			return fmt.Errorf("parameter %q must be an integer", name)
		}
		if err := checkNumericBounds(name, prop, f); err != nil {
			return err
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("parameter %q must be a boolean", name)
		}
	case "array":
		arr, ok := value.([]any)
		if !ok {
			return fmt.Errorf("parameter %q must be an array", name)
		}
		if items, ok := prop["items"].(map[string]any); ok {
			itemType, _ := items["type"].(string)
			if itemType == "string" {
				for _, v := range arr {
					if _, ok := v.(string); !ok {
						return fmt.Errorf("parameter %q must be an array of strings", name)
					}
				}
			}
		}
	case "", "object":
		// No/opaque type declared: accept as-is.
	default:
		return fmt.Errorf("parameter %q has unsupported schema type %q", name, typ)
	}
	return nil
}

func checkNumericBounds(name string, prop map[string]any, value float64) error {
	if minimum, ok := schemaNumber(prop["minimum"]); ok && value < minimum {
		return fmt.Errorf("parameter %q must be at least %v", name, minimum)
	}
	if maximum, ok := schemaNumber(prop["maximum"]); ok && value > maximum {
		return fmt.Errorf("parameter %q must be at most %v", name, maximum)
	}
	return nil
}

func schemaNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case int32:
		return float64(number), true
	default:
		return 0, false
	}
}
