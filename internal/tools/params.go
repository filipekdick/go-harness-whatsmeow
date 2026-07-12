package tools

import (
	"fmt"
	"strings"

	"github.com/filipekdick/go-harness-whatsmeow/internal/store"
)

// Typed accessors for validated tool params. The registry has already
// checked presence of required fields and JSON types against the schema, so
// these are lookups, not validators.

func strParam(p map[string]any, key string) string {
	s, _ := p[key].(string)
	return strings.TrimSpace(s)
}

func floatParam(p map[string]any, key string) (float64, bool) {
	f, ok := p[key].(float64)
	return f, ok
}

func intParam(p map[string]any, key string) (int64, bool) {
	f, ok := p[key].(float64)
	if !ok {
		return 0, false
	}
	return int64(f), true
}

func strSliceParam(p map[string]any, key string) []string {
	arr, ok := p[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

func limitParam(p map[string]any, key string, def, max int) int {
	n, ok := intParam(p, key)
	if !ok || n <= 0 {
		return def
	}
	if int(n) > max {
		return max
	}
	return int(n)
}

func has(p map[string]any, key string) bool {
	_, ok := p[key]
	return ok
}

// --- shared formatting -------------------------------------------------

func fmtPrice(v float64) string { return fmt.Sprintf("%.2f", v) }

func formatProduct(b *strings.Builder, p store.Product, withID bool) {
	if withID {
		fmt.Fprintf(b, "#%d ", p.ID)
	}
	fmt.Fprintf(b, "%s — price %s, stock %d", p.Name, fmtPrice(p.Price), p.Stock)
	if p.Category != "" {
		fmt.Fprintf(b, ", category: %s", p.Category)
	}
	if len(p.Tags) > 0 {
		fmt.Fprintf(b, ", tags: %s", strings.Join(p.Tags, ", "))
	}
	if p.Description != "" {
		fmt.Fprintf(b, "\n   %s", truncate(p.Description, 200))
	}
	b.WriteString("\n")
}

func formatService(b *strings.Builder, v store.Service, withID bool) {
	if withID {
		fmt.Fprintf(b, "#%d ", v.ID)
	}
	fmt.Fprintf(b, "%s — price %s", v.Name, fmtPrice(v.Price))
	if v.Category != "" {
		fmt.Fprintf(b, ", category: %s", v.Category)
	}
	if len(v.Tags) > 0 {
		fmt.Fprintf(b, ", tags: %s", strings.Join(v.Tags, ", "))
	}
	if v.Description != "" {
		fmt.Fprintf(b, "\n   %s", truncate(v.Description, 200))
	}
	b.WriteString("\n")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Schema shorthand helpers keep tool definitions readable.

func obj(props map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func num(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc}
}

func integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func strArray(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}
