-- 0001_init.sql
-- Multi-tenant WhatsApp customer-service / inventory harness.
--
-- Conventions:
--   * Every tenant-owned table carries company_id; application code MUST
--     filter on it in every query. FKs exist for integrity, not isolation.
--   * Soft deletes only: is_active + archived_at. No DELETE statements ever.
--   * Timestamps are TIMESTAMPTZ; storage size is explicitly not a concern,
--     so we keep full JSONB payloads for auditability.
--   * WhatsMeow keeps its own device/session tables (whatsmeow_* prefix) in
--     this same database via its sqlstore; we only reference devices by JID.

BEGIN;

-- ---------------------------------------------------------------------------
-- Tenancy
-- ---------------------------------------------------------------------------

CREATE TABLE companies (
    id            BIGSERIAL PRIMARY KEY,
    name          TEXT        NOT NULL,
    -- Per-company system prompt sent on every LLM call.
    system_prompt TEXT        NOT NULL DEFAULT '',
    is_active     BOOLEAN     NOT NULL DEFAULT TRUE,
    archived_at   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Each company runs TWO WhatsApp lines: one customer-facing, one
-- employee-facing. Each line is one linked WhatsMeow device/session.
-- SessionManager loads one client per active row here.
CREATE TABLE wa_channels (
    id            BIGSERIAL PRIMARY KEY,
    company_id    BIGINT      NOT NULL REFERENCES companies(id),
    channel       TEXT        NOT NULL CHECK (channel IN ('CUSTOMER', 'EMPLOYEE')),
    -- JID of the linked device in the whatsmeow sqlstore (e.g. 5511999999999:12@s.whatsapp.net).
    -- NULL until the QR pairing for this line has been completed.
    wa_device_jid TEXT        UNIQUE,
    is_active     BOOLEAN     NOT NULL DEFAULT TRUE,
    archived_at   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (company_id, channel)
);

-- Phone -> (company, role). Role authority lives HERE, never in which line
-- the message arrived on. Unknown senders on the CUSTOMER line get an
-- implicit CUSTOMER identity (a row is auto-created); unknown senders on the
-- EMPLOYEE line are ignored/refused — they are never promoted to EMPLOYEE.
CREATE TABLE users (
    id           BIGSERIAL PRIMARY KEY,
    company_id   BIGINT      NOT NULL REFERENCES companies(id),
    phone        TEXT        NOT NULL,  -- E.164 digits, no '+', as WhatsApp JIDs use
    role         TEXT        NOT NULL CHECK (role IN ('CUSTOMER', 'EMPLOYEE')),
    display_name TEXT,
    is_active    BOOLEAN     NOT NULL DEFAULT TRUE,
    archived_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (company_id, phone)
);
CREATE INDEX users_lookup_idx ON users (phone, company_id) WHERE is_active;

-- ---------------------------------------------------------------------------
-- Catalog
-- ---------------------------------------------------------------------------

CREATE TABLE products (
    id          BIGSERIAL PRIMARY KEY,
    company_id  BIGINT        NOT NULL REFERENCES companies(id),
    name        TEXT          NOT NULL,
    description TEXT          NOT NULL DEFAULT '',
    category    TEXT          NOT NULL DEFAULT '',
    tags        TEXT[]        NOT NULL DEFAULT '{}',
    price       NUMERIC(12,2) NOT NULL CHECK (price >= 0),
    stock       INTEGER       NOT NULL DEFAULT 0 CHECK (stock >= 0),
    is_active   BOOLEAN       NOT NULL DEFAULT TRUE,
    archived_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ   NOT NULL DEFAULT now()
);
CREATE INDEX products_company_idx  ON products (company_id) WHERE is_active;
CREATE INDEX products_search_idx   ON products USING gin (to_tsvector('simple', name || ' ' || description));
CREATE INDEX products_tags_idx     ON products USING gin (tags);

CREATE TABLE services (
    id          BIGSERIAL PRIMARY KEY,
    company_id  BIGINT        NOT NULL REFERENCES companies(id),
    name        TEXT          NOT NULL,
    description TEXT          NOT NULL DEFAULT '',
    category    TEXT          NOT NULL DEFAULT '',
    tags        TEXT[]        NOT NULL DEFAULT '{}',
    price       NUMERIC(12,2) NOT NULL CHECK (price >= 0),
    is_active   BOOLEAN       NOT NULL DEFAULT TRUE,
    archived_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ   NOT NULL DEFAULT now()
);
CREATE INDEX services_company_idx ON services (company_id) WHERE is_active;

-- ---------------------------------------------------------------------------
-- Orders
-- ---------------------------------------------------------------------------

CREATE TABLE orders (
    id          BIGSERIAL PRIMARY KEY,
    company_id  BIGINT        NOT NULL REFERENCES companies(id),
    customer_id BIGINT        NOT NULL REFERENCES users(id),
    status      TEXT          NOT NULL DEFAULT 'PENDING'
                CHECK (status IN ('PENDING','CONFIRMED','PREPARING','SHIPPED','DELIVERED','CANCELLED')),
    total       NUMERIC(12,2) NOT NULL DEFAULT 0,
    notes       TEXT          NOT NULL DEFAULT '',
    is_active   BOOLEAN       NOT NULL DEFAULT TRUE,
    archived_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ   NOT NULL DEFAULT now()
);
CREATE INDEX orders_company_idx  ON orders (company_id, status);
CREATE INDEX orders_customer_idx ON orders (company_id, customer_id);

CREATE TABLE order_items (
    id         BIGSERIAL PRIMARY KEY,
    order_id   BIGINT        NOT NULL REFERENCES orders(id),
    company_id BIGINT        NOT NULL REFERENCES companies(id), -- denormalized on purpose: uniform scoping
    product_id BIGINT        NOT NULL REFERENCES products(id),
    quantity   INTEGER       NOT NULL CHECK (quantity > 0),
    unit_price NUMERIC(12,2) NOT NULL, -- price snapshot at order time
    created_at TIMESTAMPTZ   NOT NULL DEFAULT now()
);
CREATE INDEX order_items_order_idx ON order_items (order_id);

-- ---------------------------------------------------------------------------
-- Business rules (hours, address, delivery policy, FAQ, ...)
-- ---------------------------------------------------------------------------

CREATE TABLE business_rules (
    id         BIGSERIAL PRIMARY KEY,
    company_id BIGINT      NOT NULL REFERENCES companies(id),
    key        TEXT        NOT NULL,  -- e.g. 'hours', 'address', 'delivery_policy', 'faq:returns'
    value      TEXT        NOT NULL,
    is_active  BOOLEAN     NOT NULL DEFAULT TRUE,
    archived_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (company_id, key)
);

-- ---------------------------------------------------------------------------
-- Conversations & messages (full history, bounded context via rolling summary)
-- ---------------------------------------------------------------------------

CREATE TABLE conversations (
    id               BIGSERIAL PRIMARY KEY,
    company_id       BIGINT      NOT NULL REFERENCES companies(id),
    user_id          BIGINT      NOT NULL REFERENCES users(id),
    channel          TEXT        NOT NULL CHECK (channel IN ('CUSTOMER', 'EMPLOYEE')),
    wa_chat_jid      TEXT        NOT NULL,  -- the remote party's chat JID
    -- Rolling summary of turns OLDER than summary_upto_message_id. The prompt
    -- is built as: system + summary + last-N messages after that watermark.
    summary                TEXT   NOT NULL DEFAULT '',
    summary_upto_message_id BIGINT NOT NULL DEFAULT 0,
    escalated_at     TIMESTAMPTZ,           -- set by escalate_to_human; cleared by a human
    last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    is_active        BOOLEAN     NOT NULL DEFAULT TRUE,
    archived_at      TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (company_id, user_id, channel)
);
CREATE INDEX conversations_activity_idx ON conversations (company_id, last_activity_at DESC);

CREATE TABLE messages (
    id              BIGSERIAL PRIMARY KEY,
    conversation_id BIGINT      NOT NULL REFERENCES conversations(id),
    company_id      BIGINT      NOT NULL REFERENCES companies(id), -- denormalized for uniform scoping
    -- 'user' | 'assistant' | 'tool_use' | 'tool_result' | 'system_note'
    kind            TEXT        NOT NULL
                    CHECK (kind IN ('user','assistant','tool_use','tool_result','system_note')),
    -- Full API-shaped content block(s) as sent to / received from the LLM,
    -- so history replay is lossless (tool calls included).
    content         JSONB       NOT NULL,
    wa_message_id   TEXT,                  -- set for messages that came from / went to WhatsApp
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX messages_conv_idx ON messages (conversation_id, id);

-- WhatsApp redelivers; inbound messages are recorded here before processing.
-- Insert with ON CONFLICT DO NOTHING; if the row already existed, skip.
CREATE TABLE processed_messages (
    company_id    BIGINT      NOT NULL REFERENCES companies(id),
    wa_message_id TEXT        NOT NULL,
    processed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (company_id, wa_message_id)
);

-- ---------------------------------------------------------------------------
-- Two-phase employee writes
-- ---------------------------------------------------------------------------

-- Employee write tools NEVER commit directly. Phase 1 inserts a row here and
-- returns a human-readable preview; phase 2 (confirm_write tool, in a later
-- employee turn) executes the change transactionally. See docs/DESIGN.md.
CREATE TABLE pending_writes (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      BIGINT      NOT NULL REFERENCES companies(id),
    conversation_id BIGINT      NOT NULL REFERENCES conversations(id),
    requested_by    BIGINT      NOT NULL REFERENCES users(id),
    tool_name       TEXT        NOT NULL,   -- e.g. 'update_stock'
    params          JSONB       NOT NULL,   -- validated tool params, verbatim
    preview         TEXT        NOT NULL,   -- human-readable summary shown to the employee
    status          TEXT        NOT NULL DEFAULT 'PENDING'
                    CHECK (status IN ('PENDING','CONFIRMED','CANCELLED','EXPIRED')),
    -- Turn watermark: confirm is rejected unless it arrives in a LATER
    -- inbound employee message than the one that created this row.
    created_in_message_id BIGINT NOT NULL REFERENCES messages(id),
    expires_at      TIMESTAMPTZ NOT NULL,   -- created_at + 10 minutes
    resolved_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX pending_writes_open_idx ON pending_writes (conversation_id) WHERE status = 'PENDING';

-- ---------------------------------------------------------------------------
-- Escalations & audit
-- ---------------------------------------------------------------------------

CREATE TABLE escalations (
    id              BIGSERIAL PRIMARY KEY,
    company_id      BIGINT      NOT NULL REFERENCES companies(id),
    conversation_id BIGINT      NOT NULL REFERENCES conversations(id),
    summary         TEXT        NOT NULL,   -- model-written conversation summary
    status          TEXT        NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN','RESOLVED')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at     TIMESTAMPTZ
);
CREATE INDEX escalations_open_idx ON escalations (company_id) WHERE status = 'OPEN';

-- Every committed write lands here, in the same transaction as the write.
CREATE TABLE audit_log (
    id               BIGSERIAL PRIMARY KEY,
    company_id       BIGINT      NOT NULL REFERENCES companies(id),
    actor_user_id    BIGINT      REFERENCES users(id),
    actor_phone      TEXT        NOT NULL,
    action           TEXT        NOT NULL,  -- tool name or internal action
    entity_type      TEXT        NOT NULL,  -- 'product' | 'service' | 'business_rule' | ...
    entity_id        BIGINT,
    before           JSONB,                 -- full row snapshot before (NULL on create)
    after            JSONB,                 -- full row snapshot after  (NULL never; archives keep the row)
    pending_write_id UUID        REFERENCES pending_writes(id),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_company_idx ON audit_log (company_id, created_at DESC);
CREATE INDEX audit_log_entity_idx  ON audit_log (company_id, entity_type, entity_id);

COMMIT;
