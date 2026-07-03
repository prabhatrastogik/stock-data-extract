CREATE TABLE IF NOT EXISTS instruments (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    snapshot_date     TEXT NOT NULL,
    instrument_token  INTEGER NOT NULL,
    exchange          TEXT NOT NULL,
    tradingsymbol     TEXT NOT NULL,
    name              TEXT NOT NULL DEFAULT '',
    instrument_type   TEXT NOT NULL,
    segment           TEXT NOT NULL DEFAULT '',
    expiry            TEXT NOT NULL DEFAULT '',
    strike            REAL NOT NULL DEFAULT 0,
    lot_size          INTEGER NOT NULL DEFAULT 1,
    tick_size         REAL NOT NULL DEFAULT 0.05,
    UNIQUE(snapshot_date, instrument_token)
);

CREATE INDEX IF NOT EXISTS idx_instruments_date_exchange_type
    ON instruments(snapshot_date, exchange, instrument_type);

CREATE INDEX IF NOT EXISTS idx_instruments_date_name
    ON instruments(snapshot_date, name);
