package tools

import "github.com/filipekdick/go-harness-whatsmeow/internal/config"

// RegisterAll wires every concrete tool into the registry. Stage 4 fills
// this in with the customer (read-only) and employee (two-phase write)
// tool sets; until then the model runs with no tools.
func RegisterAll(r *Registry, cfg *config.Config) {
	_ = cfg
}
