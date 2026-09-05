-- Webhook inbox (ATC-306): durable acceptance of authenticated webhook
-- deliveries, and the minimal receipts that make redelivery idempotent.
-- A row is born 'pending' with its payload and is acknowledged to the
-- sender only once committed. Processing may run more than once — the
-- consuming Integration owns duplicate-action prevention. Completion
-- clears the payload and leaves the receipt, retained for a bounded window
-- so an authenticated redelivery finds it instead of creating a new row.
-- +goose Up
CREATE TABLE webhook_deliveries (
    -- ATC-minted identity; stable across restarts and retries.
    id TEXT PRIMARY KEY,
    integration_id TEXT NOT NULL,
    -- Route path the delivery arrived on; processing dispatches by it.
    route TEXT NOT NULL,
    -- The Integration's protocol delivery identity. Deduplication is
    -- scoped to the Integration: the same identity under two
    -- Integrations is two deliveries.
    delivery_id TEXT NOT NULL,
    -- The delivery data the Integration chose to preserve for processing;
    -- NULL once processed.
    payload BLOB,
    state TEXT NOT NULL CHECK (state IN ('pending', 'done')),
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT NOT NULL,
    -- The most recent processing failure, for status; never request data.
    last_error TEXT,
    accepted_at TEXT NOT NULL,
    completed_at TEXT,
    UNIQUE (integration_id, delivery_id)
) STRICT;
CREATE INDEX webhook_deliveries_due ON webhook_deliveries (state, next_attempt_at);
