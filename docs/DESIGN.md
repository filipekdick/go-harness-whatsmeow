# Design — multi-tenant WhatsApp harness (Go / WhatsMeow / pgx / Anthropic)

Single Go process running in Termux. No agent framework: a plain,
hand-controlled tool-calling loop over the Anthropic Messages API (behind an
interface). Postgres is the source of truth for tenancy, roles, catalog,
history, and audit.

## Topology

Each company operates **two WhatsApp lines**, each a separately linked
WhatsMeow device:

```
company ──┬── CUSTOMER line  (customers text this number)
          └── EMPLOYEE line  (employees text this number)
```

The `SessionManager` holds one WhatsMeow client per row of `wa_channels`,
keyed by `(company_id, channel)`. The receiving line therefore identifies the
tenant *and* the intended audience — but it is never the role authority:

- **Role authority is the `users` table only.** EMPLOYEE tools are exposed iff
  the sender resolves to an active `users` row with `role = 'EMPLOYEE'` **and**
  the message arrived on that company's EMPLOYEE line (defense in depth — a
  registered employee texting the customer line gets the customer toolset).
- Unknown number on the CUSTOMER line → auto-created as CUSTOMER of the
  company owning that line.
- Unknown number on the EMPLOYEE line → politely refused, logged. Never
  promoted to EMPLOYEE implicitly.

## File / folder structure

```
.
├── cmd/
│   └── harness/main.go        # wiring only: config → db → llm → sessions → run
├── internal/
│   ├── config/config.go       # env-based config (DB URL, API key, model, caps)
│   ├── db/
│   │   ├── db.go              # pgx pool + migration runner
│   │   └── migrations/        # embedded SQL, applied at startup
│   ├── store/                 # ALL SQL lives here; every function's first
│   │   │                      # args are (ctx, companyID) — tenancy by construction
│   │   ├── users.go products.go services.go orders.go rules.go
│   │   ├── conversations.go   # history load/save, rolling summary watermark
│   │   ├── pending.go         # pending_writes lifecycle
│   │   ├── audit.go dedup.go escalations.go
│   ├── llm/
│   │   ├── client.go          # llm.Client interface + neutral message/tool types
│   │   └── anthropic.go       # official SDK impl; retries (≤3 attempts,
│   │                          # exp backoff on timeout/429/5xx) via SDK config
│   ├── harness/
│   │   ├── harness.go         # the tool loop (≤6 iterations → auto-escalate)
│   │   ├── identity.go        # sender phone + receiving channel → (company, role)
│   │   ├── history.go         # summary + last-N-turns context builder
│   │   └── workers.go         # per-conversation serial workers, bounded pool
│   ├── tools/
│   │   ├── registry.go        # definitions, role gating, param schema validation
│   │   ├── customer.go        # read-only tools
│   │   ├── employee.go        # write tools (prepare phase only)
│   │   └── confirm.go         # confirm_write / cancel_write (commit phase)
│   └── wa/
│       ├── manager.go         # SessionManager keyed by (company_id, channel)
│       └── handler.go         # inbound events → dedup → worker dispatch
├── scripts/
│   ├── start.sh               # wakelock + supervise loop
│   └── termux-boot.sh         # → ~/.termux/boot/
└── docs/DESIGN.md
```

Tenancy rule made structural: nothing outside `internal/store` builds SQL, and
every store function requires `companyID` as a leading parameter and includes
`company_id = $1` in its WHERE/INSERT. The LLM never influences scoping.

## Message flow

```
WhatsMeow event
  → dedup (processed_messages, ON CONFLICT DO NOTHING)
  → identity: (receiving channel, sender phone) → company_id, user, role
  → enqueue on that conversation's serial worker
  → harness loop:
      history = rolling summary + last N messages
      tools   = registry.ForRole(role)          // CUSTOMER never sees write tools
      for i := 0; i < 6; i++ {
          resp = llm.Call(system, history, tools)   // retry w/ backoff inside
          if text  → send via the SAME channel it arrived on; persist; done
          if tool_use → validate name+role+params → execute (company-scoped)
                        → append tool_result (errors become results, never panics)
      }
      // 6 iterations exhausted:
      escalate_to_human(auto) + apologetic reply
```

## Schema

See `internal/db/migrations/0001_init.sql`. Summary:

| table | purpose | notes |
|---|---|---|
| `companies` | tenants + per-company system prompt | soft delete |
| `wa_channels` | (company, CUSTOMER\|EMPLOYEE) → linked device JID | one WhatsMeow session per row |
| `users` | phone → (company, role) | `UNIQUE(company_id, phone)`; role authority |
| `products`, `services` | catalog, rich attributes (category, tags[], description) | GIN indexes for search_catalog |
| `orders`, `order_items` | order status lookups | item rows snapshot unit_price |
| `business_rules` | key/value per company (hours, policies, FAQ) | `UNIQUE(company_id, key)` |
| `conversations` | one per (company, user, channel); holds rolling summary + watermark | |
| `messages` | lossless API-shaped JSONB blocks incl. tool_use/tool_result | full history kept forever |
| `processed_messages` | WhatsApp redelivery dedup | PK (company_id, wa_message_id) |
| `pending_writes` | phase 1 of employee writes | UUID id, 10-min TTL, turn watermark |
| `escalations` | escalate_to_human queue for humans | |
| `audit_log` | every committed write: actor, before/after JSONB | written in the same tx as the write |

WhatsMeow's own `whatsmeow_*` tables (device keys, sessions) live in the same
database via its Postgres sqlstore.

**Context bounding:** prompt = company system prompt + `conversations.summary`
(covers messages ≤ watermark) + all messages after the watermark, capped at
the last N=30 blocks. When the tail exceeds ~50 blocks, a background
summarization call folds the oldest ones into `summary` and advances
`summary_upto_message_id`. Full history always remains in `messages`; only
what is *sent to the API* is bounded.

## Employee write confirmation — two-phase via `pending_writes`

Write tools physically cannot commit. There is no code path from
`update_stock` to an UPDATE statement:

1. **Prepare.** The model calls e.g. `update_stock(product_id=7, absolute=30)`.
   The executor validates params, loads the current row (company-scoped),
   computes a before/after preview, inserts a `pending_writes` row
   (`status=PENDING`, `expires_at = now()+10min`, `created_in_message_id` =
   the current inbound message), and returns a tool_result like:
   > `PENDING a1b2…: "Coke 350ml" stock 42 → 30. Ask the employee to confirm or cancel.`
   The model relays this to the employee. Turn ends. Nothing changed in the DB.

2. **Confirm.** The employee replies ("yes", "confirm"). The model calls
   `confirm_write(pending_write_id)`. The harness enforces **in code**:
   - row exists, `status = 'PENDING'`, not expired;
   - same `company_id`, same `conversation_id`, same requesting employee;
   - `created_in_message_id` is *older than the current inbound message* —
     so the model cannot prepare and confirm inside one turn, even if it
     tries to chain the two tool calls.
   Then, in a single transaction: execute the write, insert `audit_log`
   (before/after snapshots, `pending_write_id` link), mark row `CONFIRMED`.

3. **Cancel / expire.** `cancel_write(id)` → `CANCELLED`. Expired rows are
   swept to `EXPIRED` lazily on the next confirm attempt.

Why this over a `confirm: true` flag on each tool: a flag is one hallucinated
`true` away from a silent commit. Here the commit path only accepts a UUID
that the *server* minted in a previous turn, and the pending row doubles as an
audit artifact for writes that were proposed but never confirmed.

`archive_product` / `archive_service` follow the same flow and only ever set
`is_active = FALSE, archived_at = now()`.

## Robustness (day one)

- LLM calls: ≤3 attempts, exponential backoff (1s/2s/4s + jitter) on
  timeouts, 429, 5xx. Non-retryable 4xx fails fast.
- Tool loop hard cap 6 → auto-escalation, never an infinite loop.
- Every tool error (not found, bad params, query failure) is returned to the
  model as an error tool_result string; the process never crashes on tool
  execution.
- Tool validation before touching the DB: name exists in the registry, is
  allowed for the resolved role, params validate against the tool's schema.
- Dedup via `processed_messages` insert-or-skip, done before any processing.
- Concurrency: a bounded worker pool where each conversation maps to one
  serial queue (hash of conversation key → worker), so a slow conversation
  in company A never blocks company B, but a single conversation is always
  processed in order.
