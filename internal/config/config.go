// Package config loads all runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	// Postgres connection string, e.g. postgres://user@localhost:5432/harness
	DatabaseURL string

	// LLM
	AnthropicAPIKey string
	Model           string
	MaxTokens       int
	LLMMaxAttempts  int // total attempts per API call (1 initial + retries)

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
	cfg := &Config{
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		AnthropicAPIKey:   os.Getenv("ANTHROPIC_API_KEY"),
		Model:             envStr("LLM_MODEL", "claude-opus-4-8"),
		MaxTokens:         envInt("LLM_MAX_TOKENS", 8192),
		LLMMaxAttempts:    envInt("LLM_MAX_ATTEMPTS", 3),
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
	if cfg.AnthropicAPIKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is required")
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
