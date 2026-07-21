// Command harness is the single process: config → db → llm → tools →
// harness → WhatsMeow sessions. Ctrl-C / SIGTERM shuts everything down.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/filipekdick/go-harness-whatsmeow/internal/config"
	"github.com/filipekdick/go-harness-whatsmeow/internal/db"
	"github.com/filipekdick/go-harness-whatsmeow/internal/harness"
	"github.com/filipekdick/go-harness-whatsmeow/internal/llm"
	"github.com/filipekdick/go-harness-whatsmeow/internal/store"
	"github.com/filipekdick/go-harness-whatsmeow/internal/tools"
	"github.com/filipekdick/go-harness-whatsmeow/internal/wa"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(log)
	if err := run(log, os.Args[1:]); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mode := "run"
	if len(args) > 1 {
		return fmt.Errorf("usage: harness [run|migrate]")
	}
	if len(args) == 1 {
		mode = args[0]
	}
	if mode == "migrate" {
		return migrateOnly(ctx, log)
	}
	if mode != "run" {
		return fmt.Errorf("unknown command %q; usage: harness [run|migrate]", mode)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	st := store.New(pool)

	reg := tools.NewRegistry(log)
	tools.RegisterReadOnlyTools(reg)

	mgr, err := wa.NewManager(ctx, cfg.DatabaseURL, st, log)
	if err != nil {
		return err
	}

	llmClient, err := buildLLMClient(cfg)
	if err != nil {
		return err
	}
	h := harness.New(st, llmClient, reg, mgr, cfg, log)
	mgr.SetEnqueuer(h)

	h.Start(ctx)
	if err := mgr.Start(ctx); err != nil {
		return err
	}
	log.Info("harness running", "provider", cfg.LLMProvider, "model", cfg.Model, "workers", cfg.WorkerCount)

	<-ctx.Done()
	log.Info("shutting down")
	mgr.Stop()
	return nil
}

func buildLLMClient(cfg *config.Config) (llm.Client, error) {
	switch cfg.LLMProvider {
	case config.ProviderOpenRouter:
		return llm.NewOpenRouterClient(
			cfg.OpenRouterAPIKey,
			cfg.Model,
			cfg.LLMMaxAttempts,
			cfg.OpenRouterSiteURL,
			cfg.OpenRouterAppName,
		), nil
	case config.ProviderAnthropic:
		return llm.NewAnthropicClient(cfg.AnthropicAPIKey, cfg.Model, cfg.LLMMaxAttempts), nil
	default:
		return nil, fmt.Errorf("unsupported LLM provider %q", cfg.LLMProvider)
	}
}

func migrateOnly(ctx context.Context, log *slog.Logger) error {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	pool, err := db.Connect(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	log.Info("database migrations applied")
	return nil
}
