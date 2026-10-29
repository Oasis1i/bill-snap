-- bill-snap bills table.
-- schema.sql and internal/store/schema.sql must stay identical.
-- Startup embeds internal/store/schema.sql.
-- CREATE TABLE IF NOT EXISTS does not add columns to a table that already
-- exists. internal/store adds order_no, paid_at_defaulted, items, source,
-- and request_id when they are missing, then applies the indexes below.

CREATE TABLE IF NOT EXISTS bills (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    amount_cents       INTEGER NOT NULL,
    currency           TEXT    NOT NULL,
    merchant           TEXT    NOT NULL DEFAULT '',
    category           TEXT    NOT NULL DEFAULT '',
    paid_at            TEXT    NOT NULL,
    note               TEXT    NOT NULL DEFAULT '',
    image_path         TEXT    NOT NULL DEFAULT '',
    content_hash       TEXT    NOT NULL,
    dedup_key          TEXT    NOT NULL,
    raw_model_json     TEXT    NOT NULL DEFAULT '',
    order_no           TEXT,
    paid_at_defaulted  INTEGER NOT NULL DEFAULT 0,
    created_at         TEXT    NOT NULL,
    updated_at         TEXT    NOT NULL,
    items              TEXT    NOT NULL DEFAULT '',
    source             TEXT    NOT NULL DEFAULT 'screenshot',
    request_id         TEXT
);

-- One row per payment. See README for how dedup_key is built.
CREATE UNIQUE INDEX IF NOT EXISTS idx_bills_dedup_key ON bills(dedup_key);

-- Same screenshot bytes always map to one row.
CREATE UNIQUE INDEX IF NOT EXISTS idx_bills_content_hash ON bills(content_hash);

-- Same normalized order or transaction number is one payment.
-- NULL means the screenshot had no number. Those rows are not unique here.
CREATE UNIQUE INDEX IF NOT EXISTS idx_bills_order_no ON bills(order_no) WHERE order_no IS NOT NULL;

-- Manual add_bill idempotency key. NULL is not unique, so screenshot rows
-- and manual rows without a key can share an empty request_id.
CREATE UNIQUE INDEX IF NOT EXISTS idx_bills_request_id ON bills(request_id) WHERE request_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_bills_paid_at ON bills(paid_at);
CREATE INDEX IF NOT EXISTS idx_bills_category ON bills(category);

-- Append-only record of add_bill attempts. Not exposed through MCP.
CREATE TABLE IF NOT EXISTS audit_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    at         TEXT NOT NULL,
    token_kind TEXT NOT NULL,
    tool       TEXT NOT NULL,
    args_json  TEXT NOT NULL,
    result     TEXT NOT NULL,
    bill_id    INTEGER
);
