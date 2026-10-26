-- bill-snap bills table.
-- schema.sql and internal/store/schema.sql must stay identical.
-- Startup embeds internal/store/schema.sql.

CREATE TABLE IF NOT EXISTS bills (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    amount_cents    INTEGER NOT NULL,
    currency        TEXT    NOT NULL,
    merchant        TEXT    NOT NULL DEFAULT '',
    category        TEXT    NOT NULL DEFAULT '',
    paid_at         TEXT    NOT NULL,
    note            TEXT    NOT NULL DEFAULT '',
    image_path      TEXT    NOT NULL DEFAULT '',
    content_hash    TEXT    NOT NULL,
    dedup_key       TEXT    NOT NULL,
    raw_model_json  TEXT    NOT NULL DEFAULT '',
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL
);

-- One row per payment. See README for how dedup_key is built.
CREATE UNIQUE INDEX IF NOT EXISTS idx_bills_dedup_key ON bills(dedup_key);

-- Same screenshot bytes always map to one row.
CREATE UNIQUE INDEX IF NOT EXISTS idx_bills_content_hash ON bills(content_hash);

CREATE INDEX IF NOT EXISTS idx_bills_paid_at ON bills(paid_at);
CREATE INDEX IF NOT EXISTS idx_bills_category ON bills(category);
