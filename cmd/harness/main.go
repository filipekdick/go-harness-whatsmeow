// Command harness is the single-process WhatsApp customer-service and
// inventory harness.
//
//	harness run                                    start everything (default)
//	harness pair <company_id> <CUSTOMER|EMPLOYEE>  link a WhatsApp number (QR)
//	harness add-company <name>                     create a tenant + its 2 lines
//	harness add-employee <company_id> <phone> [display name]
//
// Configuration is environment-only; see internal/config.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
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
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
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

	cmd := "run"
	args := os.Args[1:]
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}

	switch cmd {
	case "run":
		return serve(ctx, log, cfg, st)

	case "pair":
		if len(args) != 2 {
			return fmt.Errorf("usage: harness pair <company_id> <CUSTOMER|EMPLOYEE>")
		}
		companyID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid company id %q", args[0])
		}
		channel, err := parseChannel(args[1])
		if err != nil {
			return err
		}
		manager, err := wa.NewSessionManager(ctx, cfg.DatabaseURL, st, log)
		if err != nil {
			return err
		}
		return manager.Pair(ctx, companyID, channel)

	case "add-company":
		if len(args) < 1 {
			return fmt.Errorf("usage: harness add-company <name>")
		}
		id, err := st.CreateCompany(ctx, strings.Join(args, " "), "")
		if err != nil {
			return err
		}
		fmt.Printf("company %d created with CUSTOMER and EMPLOYEE channels.\n", id)
		fmt.Println("next steps:")
		fmt.Printf("  1. set its system prompt:  UPDATE companies SET system_prompt = '...' WHERE id = %d;\n", id)
		fmt.Printf("  2. pair the lines:         harness pair %d CUSTOMER   /   harness pair %d EMPLOYEE\n", id, id)
		fmt.Printf("  3. register employees:     harness add-employee %d <phone> [name]\n", id)
		return nil

	case "add-employee":
		if len(args) < 2 {
			return fmt.Errorf("usage: harness add-employee <company_id> <phone> [display name]")
		}
		companyID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid company id %q", args[0])
		}
		phone := strings.TrimPrefix(args[1], "+")
		name := strings.Join(args[2:], " ")
		id, err := st.UpsertEmployee(ctx, companyID, phone, name)
		if err != nil {
			return err
		}
		fmt.Printf("employee user %d registered for company %d (phone %s)\n", id, companyID, phone)
		return nil

	default:
		return fmt.Errorf("unknown command %q (run | pair | add-company | add-employee)", cmd)
	}
}

func serve(ctx context.Context, log *slog.Logger, cfg *config.Config, st *store.Store) error {
	client := llm.NewAnthropicClient(cfg.AnthropicAPIKey, cfg.Model, cfg.LLMMaxAttempts)

	registry := tools.NewRegistry(log)
	tools.RegisterAll(registry, cfg)

	manager, err := wa.NewSessionManager(ctx, cfg.DatabaseURL, st, log)
	if err != nil {
		return err
	}
	defer manager.DisconnectAll()

	h := harness.New(st, client, registry, manager, cfg, log)
	manager.OnMessage = h.Enqueue

	h.Start(ctx)
	if err := manager.Connect(ctx); err != nil {
		return err
	}
	log.Info("harness running", "model", cfg.Model, "workers", cfg.WorkerCount)

	<-ctx.Done()
	log.Info("shutting down")
	return nil
}

func parseChannel(s string) (store.Channel, error) {
	switch strings.ToUpper(s) {
	case "CUSTOMER":
		return store.ChannelCustomer, nil
	case "EMPLOYEE":
		return store.ChannelEmployee, nil
	default:
		return "", fmt.Errorf("channel must be CUSTOMER or EMPLOYEE, got %q", s)
	}
}
