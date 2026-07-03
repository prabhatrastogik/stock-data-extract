CREATE TABLE IF NOT EXISTS extraction_state (
    exchange     TEXT NOT NULL,
    symbol       TEXT NOT NULL,
    interval     TEXT NOT NULL,
    last_date    TEXT NOT NULL,
    updated_at   TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (exchange, symbol, interval)
);
