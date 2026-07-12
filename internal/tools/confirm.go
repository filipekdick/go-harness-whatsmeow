package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/filipekdick/go-harness-whatsmeow/internal/store"
)

// registerConfirmTools adds confirm_write / cancel_write — the ONLY code
// path that commits an employee write. All guards here are enforced in code;
// none rely on the model behaving.
func registerConfirmTools(r *Registry) {
	employee := []store.Role{store.RoleEmployee}

	r.Register(&Tool{
		Name: "confirm_write",
		Description: "Commit a pending write after the employee explicitly confirmed it. " +
			"Only call this when the employee's LATEST message clearly approves the previewed change " +
			"(e.g. \"yes\", \"confirm\", \"go ahead\"). Never call it in the same turn that created the pending write.",
		InputSchema: obj(map[string]any{
			"pending_write_id": str("The UUID returned when the write was prepared"),
		}, "pending_write_id"),
		Roles:   employee,
		Handler: confirmWrite,
	})

	r.Register(&Tool{
		Name: "cancel_write",
		Description: "Discard a pending write. Call this when the employee declines the previewed " +
			"change or asks for something different instead.",
		InputSchema: obj(map[string]any{
			"pending_write_id": str("The UUID returned when the write was prepared"),
		}, "pending_write_id"),
		Roles:   employee,
		Handler: cancelWrite,
	})
}

func confirmWrite(ctx context.Context, env *Env, p map[string]any) (string, error) {
	pw, problem, err := loadPending(ctx, env, strParam(p, "pending_write_id"))
	if err != nil {
		return "", err
	}
	if problem != "" {
		return "", fmt.Errorf("%s", problem)
	}

	// Expiry is swept lazily, at confirm time.
	if time.Now().After(pw.ExpiresAt) {
		_, _ = env.Store.ResolvePendingWrite(ctx, env.Store.Pool(), env.CompanyID, pw.ID, "EXPIRED")
		return "", fmt.Errorf("this pending write expired; prepare the change again and re-confirm")
	}

	// The confirmation must come from a LATER inbound message than the one
	// that created the pending write. This makes prepare-and-confirm inside
	// a single turn impossible, even if the model chains the two calls.
	if pw.CreatedInMessageID >= env.InboundMessageID {
		return "", fmt.Errorf("confirmation must come from the employee in a new message; " +
			"show them the preview and wait for their reply")
	}

	apply, ok := applyFuncs[pw.ToolName]
	if !ok {
		return "", fmt.Errorf("pending write has unknown tool %q", pw.ToolName)
	}
	var params map[string]any
	if err := json.Unmarshal(pw.Params, &params); err != nil {
		return "", fmt.Errorf("pending write has corrupt parameters")
	}

	tx, err := env.Store.Pool().Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("could not start transaction")
	}
	defer tx.Rollback(ctx)

	// Claim the pending row first; the status='PENDING' guard makes a
	// concurrent double-confirm a no-op.
	claimed, err := env.Store.ResolvePendingWrite(ctx, tx, env.CompanyID, pw.ID, "CONFIRMED")
	if err != nil {
		return "", err
	}
	if !claimed {
		return "", fmt.Errorf("this pending write was already resolved")
	}

	res, err := apply(ctx, tx, env, params)
	if err != nil {
		return "", err // rollback: the pending row stays PENDING
	}

	// The audit row commits atomically with the write it records.
	if err := store.InsertAudit(ctx, tx, store.AuditEntry{
		CompanyID:      env.CompanyID,
		ActorUserID:    &env.User.ID,
		ActorPhone:     env.User.Phone,
		Action:         pw.ToolName,
		EntityType:     res.entityType,
		EntityID:       res.entityID,
		Before:         res.before,
		After:          res.after,
		PendingWriteID: &pw.ID,
	}); err != nil {
		return "", fmt.Errorf("audit insert failed: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit failed: %w", err)
	}
	return "COMMITTED: " + res.message, nil
}

func cancelWrite(ctx context.Context, env *Env, p map[string]any) (string, error) {
	pw, problem, err := loadPending(ctx, env, strParam(p, "pending_write_id"))
	if err != nil {
		return "", err
	}
	if problem != "" {
		return "", fmt.Errorf("%s", problem)
	}
	done, err := env.Store.ResolvePendingWrite(ctx, env.Store.Pool(), env.CompanyID, pw.ID, "CANCELLED")
	if err != nil {
		return "", err
	}
	if !done {
		return "", fmt.Errorf("this pending write was already resolved")
	}
	return "cancelled; nothing was changed", nil
}

// loadPending fetches and authorizes a pending write for the current caller.
// Returns a user-facing problem string for expected failure modes.
func loadPending(ctx context.Context, env *Env, id string) (*store.PendingWrite, string, error) {
	if id == "" {
		return nil, openPendingHint(ctx, env, "no pending_write_id given"), nil
	}
	pw, err := env.Store.GetPendingWrite(ctx, env.CompanyID, id)
	if err != nil {
		return nil, "", err
	}
	// company_id was part of the lookup, so a foreign company's UUID simply
	// does not exist here.
	if pw == nil {
		return nil, openPendingHint(ctx, env, fmt.Sprintf("no pending write %q in this company", id)), nil
	}
	if pw.ConversationID != env.ConversationID {
		return nil, "that pending write belongs to a different conversation", nil
	}
	if pw.RequestedBy != env.User.ID {
		return nil, "that pending write was prepared by a different employee; only the requester can resolve it", nil
	}
	switch pw.Status {
	case "PENDING":
		return pw, "", nil
	case "CONFIRMED":
		return nil, "that pending write was already committed", nil
	case "CANCELLED":
		return nil, "that pending write was already cancelled", nil
	default: // EXPIRED
		return nil, "that pending write expired; prepare the change again", nil
	}
}

func openPendingHint(ctx context.Context, env *Env, prefix string) string {
	open, err := env.Store.OpenPendingWrites(ctx, env.CompanyID, env.ConversationID)
	if err != nil || len(open) == 0 {
		return prefix + "; there are no open pending writes in this conversation"
	}
	var b strings.Builder
	b.WriteString(prefix + "; open pending writes in this conversation:\n")
	for _, pw := range open {
		fmt.Fprintf(&b, "  %s (%s): %s\n", pw.ID, pw.ToolName, firstLine(pw.Preview))
	}
	return b.String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
