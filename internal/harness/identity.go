package harness

import (
	"context"
	"fmt"

	"github.com/filipekdick/go-harness-whatsmeow/internal/store"
)

type identity struct {
	user          *store.User
	effectiveRole store.Role
}

// resolveIdentity maps (receiving channel, sender phone) to a user and an
// effective role. Security rules, enforced in code:
//
//   - Role authority is the users table, never the line texted or the prompt.
//   - CUSTOMER line: unknown numbers are auto-registered as CUSTOMER of the
//     company that owns the line. The effective role on this line is ALWAYS
//     CUSTOMER — a registered employee texting the customer line gets the
//     customer toolset (defense in depth).
//   - EMPLOYEE line: only pre-registered active EMPLOYEE users are served;
//     everyone else gets a fixed refusal and no LLM call at all.
//
// Returns a non-empty refusal string when the sender must be turned away.
func (h *Harness) resolveIdentity(ctx context.Context, msg Inbound) (*identity, string, error) {
	switch msg.Channel {
	case store.ChannelCustomer:
		user, err := h.store.EnsureCustomer(ctx, msg.CompanyID, msg.SenderPhone, msg.SenderName)
		if err != nil {
			return nil, "", fmt.Errorf("ensure customer: %w", err)
		}
		return &identity{user: user, effectiveRole: store.RoleCustomer}, "", nil

	case store.ChannelEmployee:
		user, err := h.store.UserByPhone(ctx, msg.CompanyID, msg.SenderPhone)
		if err != nil {
			return nil, "", fmt.Errorf("lookup user: %w", err)
		}
		if user == nil || user.Role != store.RoleEmployee {
			h.log.Warn("unregistered number on employee line",
				"company", msg.CompanyID, "phone", msg.SenderPhone)
			return nil, replyUnknown, nil
		}
		return &identity{user: user, effectiveRole: store.RoleEmployee}, "", nil

	default:
		return nil, "", fmt.Errorf("unknown channel %q", msg.Channel)
	}
}
