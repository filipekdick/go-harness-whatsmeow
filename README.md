# go-harness-whatsmeow

A multi-tenant WhatsApp customer-service and inventory-management harness in
Go: a plain, hand-controlled LLM tool-calling loop (no agent framework) over
[WhatsMeow](https://github.com/tulir/whatsmeow) and Postgres, built to run as
a single process inside Termux on an Android phone.

Each company gets **two WhatsApp lines** — one for customers, one for
employees. Customers get read-only tools (stock, prices, catalog search with
recommendations, business info, order status, human escalation). Employees
additionally get write tools (products, services, stock, business rules) that
commit **only** through a two-phase confirmation: the tool prepares a preview,
the employee approves in a later message, and `confirm_write` commits the
change together with its audit-log row in one transaction.

## Layout

| Path | What |
|---|---|
| `cmd/harness` | CLI: `run`, `pair`, `add-company`, `add-employee` |
| `internal/harness` | the tool loop, identity/role resolution, workers, summarization |
| `internal/tools` | tool registry (role gating, validation) + all tool implementations |
| `internal/llm` | provider-neutral client interface; Anthropic implementation |
| `internal/store` | all SQL, every query scoped by `company_id` |
| `internal/wa` | WhatsMeow SessionManager, one client per (company, line) |
| `internal/db` | pgx pool + embedded migrations |
| `scripts/` | Termux:Boot launcher + supervisor with wakelock |
| `docs/DESIGN.md` | architecture and security model |
| `docs/DEPLOY.md` | step-by-step Termux deployment |

## Security model (short version)

- Tenant isolation is **structural**: only `internal/store` builds SQL and
  every query filters by `company_id`. The prompt is never trusted for it.
- Role authority is the `users` table. Employee-only tools are physically
  absent from the API payload for customers, and re-checked at execution.
- Unknown numbers on the employee line are refused without an LLM call;
  employees are only ever registered via the CLI.
- No hard deletes anywhere; every committed write lands in `audit_log`
  with before/after snapshots, in the same transaction.

See `docs/DESIGN.md` for the full picture and `docs/DEPLOY.md` to run it.
