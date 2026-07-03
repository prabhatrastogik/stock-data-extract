CREATE TABLE IF NOT EXISTS options_extraction_state (
    underlying  TEXT NOT NULL,
    interval    TEXT NOT NULL,
    last_date   TEXT NOT NULL,
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (underlying, interval)
);
