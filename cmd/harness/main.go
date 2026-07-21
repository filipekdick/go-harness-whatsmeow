// Command harness is the single process: config → db → llm → tools →
// harness → WhatsMeow sessions. Ctrl-C / SIGTERM shuts everything down.
package main

import (
	"context"
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
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	llmClient := llm.NewAnthropicClient(cfg.AnthropicAPIKey, cfg.Model, cfg.LLMMaxAttempts)
	h := harness.New(st, llmClient, reg, mgr, cfg, log)
	mgr.SetEnqueuer(h)

	h.Start(ctx)
	if err := mgr.Start(ctx); err != nil {
		return err
	}
	log.Info("harness running", "model", cfg.Model, "workers", cfg.WorkerCount)

	<-ctx.Done()
	log.Info("shutting down")
	mgr.Stop()
	return nil
}
