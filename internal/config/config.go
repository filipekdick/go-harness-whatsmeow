// Package config loads all runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	ProviderAnthropic  = "anthropic"
	ProviderOpenRouter = "openrouter"
)

type Config struct {
	// Postgres connection string, e.g. postgres://user@localhost:5432/harness
	DatabaseURL string

	// LLM
	LLMProvider string
	Model       string
	MaxTokens   int
	// Total attempts per API call (1 initial + retries).
	LLMMaxAttempts int

	AnthropicAPIKey string

	OpenRouterAPIKey  string
	OpenRouterSiteURL string
	OpenRouterAppName string

	// Harness behavior
	MaxToolIterations int           // hard cap on the tool loop before auto-escalation
	HistoryTailLimit  int           // how many recent message rows are sent to the API
	SummarizeAfter    int           // fold history into the rolling summary past this many rows
	PendingWriteTTL   time.Duration // employee write confirmations expire after this

	// Concurrency
	WorkerCount     int
	WorkerQueueSize int
}

func Load() (*Config, error) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_PROVIDER")))
	if provider == "" {
		// Preserve existing Anthropic-only installations while making OpenRouter
		// the zero-wiring option when its key is configured.
		if os.Getenv("OPENROUTER_API_KEY") != "" {
			provider = ProviderOpenRouter
		} else {
			provider = ProviderAnthropic
		}
	}

	model := strings.TrimSpace(os.Getenv("LLM_MODEL"))
	if model == "" && provider == ProviderAnthropic {
		model = "claude-opus-4-8"
	}

	cfg := &Config{
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		LLMProvider:       provider,
		Model:             model,
		MaxTokens:         envInt("LLM_MAX_TOKENS", 8192),
		LLMMaxAttempts:    envInt("LLM_MAX_ATTEMPTS", 3),
		AnthropicAPIKey:   os.Getenv("ANTHROPIC_API_KEY"),
		OpenRouterAPIKey:  os.Getenv("OPENROUTER_API_KEY"),
		OpenRouterSiteURL: os.Getenv("OPENROUTER_SITE_URL"),
		OpenRouterAppName: envStr("OPENROUTER_APP_NAME", "go-harness-whatsmeow"),
		MaxToolIterations: envInt("MAX_TOOL_ITERATIONS", 6),
		HistoryTailLimit:  envInt("HISTORY_TAIL_LIMIT", 30),
		SummarizeAfter:    envInt("SUMMARIZE_AFTER", 50),
		PendingWriteTTL:   time.Duration(envInt("PENDING_WRITE_TTL_MINUTES", 10)) * time.Minute,
		WorkerCount:       envInt("WORKER_COUNT", 8),
		WorkerQueueSize:   envInt("WORKER_QUEUE_SIZE", 256),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("LLM_MODEL is required for provider %q", cfg.LLMProvider)
	}
	switch cfg.LLMProvider {
	case ProviderOpenRouter:
		if cfg.OpenRouterAPIKey == "" {
			return nil, fmt.Errorf("OPENROUTER_API_KEY is required when LLM_PROVIDER=openrouter")
		}
	case ProviderAnthropic:
		if cfg.AnthropicAPIKey == "" {
			return nil, fmt.Errorf("ANTHROPIC_API_KEY is required when LLM_PROVIDER=anthropic")
		}
	default:
		return nil, fmt.Errorf("unsupported LLM_PROVIDER %q (supported: openrouter, anthropic)", cfg.LLMProvider)
	}
	if cfg.LLMMaxAttempts < 1 || cfg.LLMMaxAttempts > 3 {
		return nil, fmt.Errorf("LLM_MAX_ATTEMPTS must be between 1 and 3")
	}
	if cfg.MaxTokens < 1 {
		return nil, fmt.Errorf("LLM_MAX_TOKENS must be greater than zero")
	}
	if cfg.SummarizeAfter <= cfg.HistoryTailLimit {
		return nil, fmt.Errorf("SUMMARIZE_AFTER (%d) must be greater than HISTORY_TAIL_LIMIT (%d)",
			cfg.SummarizeAfter, cfg.HistoryTailLimit)
	}
	return cfg, nil
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
