# Data Reference — stock-data-extract

This document describes every dataset produced by the extraction pipeline. It is written for downstream consumers: trading model builders, backtesting frameworks, and charting / analysis tools. Read it before touching the data.

---

## Storage overview

All OHLCV data lives in **Cloudflare R2** (S3-compatible object storage) as **Apache Parquet** files.

A **SQLite database** (replicated to R2 via Litestream) tracks extraction progress and holds the current instrument master. It is an operational store — not a primary data source for model building. Use the Parquet instrument snapshots for historical instrument metadata.

```
R2 bucket
├── NSE/EQ/<symbol>/day/<year>.parquet          # equity daily candles
├── NSE/EQ/<symbol>/15min/<YYYY-MM>.parquet     # equity 15-min candles
├── NFO/FUT/<symbol>/day/<year>.parquet         # futures daily candles
├── NFO/FUT/<symbol>/15min/<YYYY-MM>.parquet    # futures 15-min candles
├── NFO/OPT/<underlying>/day/<year>.parquet     # options daily candles (all strikes/expiries)
├── NFO/OPT/<underlying>/15min/<YYYY-MM>.parquet
└── instruments/snapshots/<YYYY-MM-DD>.parquet  # instrument master snapshot
```

---

## Timestamps

**All timestamps are UTC, stored as microseconds since the Unix epoch (`int64`).**

Kite Connect returns candle times in IST (UTC+5:30). The pipeline stores them as-is after converting to `time.Time` via the Go Kite client, which normalises to UTC internally.

- Daily candle timestamp: `YYYY-MM-DDT00:00:00Z` (midnight UTC = 5:30 AM IST of that trading day)
- 15-min candle timestamp: start of the 15-minute bar in UTC

To reconstruct IST: add 5 hours 30 minutes, or use `Asia/Kolkata` locale.

---

## Dataset 1 — NSE Equity candles

### Scope

| Config field | Value |
|---|---|
| `fno_only: true` | Only NSE stocks with active F&O contracts on NFO (~180 symbols) |
| `include_indices: true` | Also includes NSE index instruments (NIFTY 50, NIFTY BANK, etc.) |
| Daily backfill from | 2004-01-01 |
| 15-min backfill from | 2020-01-01 |

With both flags set (current default) the equity universe is approximately:
- ~180 F&O-eligible stocks (RELIANCE, TCS, HDFCBANK, ETERNAL, …)
- ~10–15 NSE index instruments (NIFTY 50, NIFTY BANK, NIFTY NEXT 50, INDIA VIX, …)

Set `fno_only: false` to extract all ~2000 NSE EQ symbols.

### R2 key pattern

```
NSE/EQ/<symbol>/<interval>/<partition>.parquet
```

| Component | Values | Example |
|---|---|---|
| `<symbol>` | NSE tradingsymbol; spaces replaced with hyphens | `RELIANCE`, `NIFTY-50`, `NIFTY-BANK` |
| `<interval>` | `day` or `15min` | `day` |
| `<partition>` | Year (`YYYY`) for daily; year-month (`YYYY-MM`) for 15-min | `2024`, `2024-01` |

Examples:
```
NSE/EQ/RELIANCE/day/2024.parquet
NSE/EQ/RELIANCE/15min/2024-01.parquet
NSE/EQ/NIFTY-50/day/2024.parquet
NSE/EQ/NIFTY-50/15min/2024-01.parquet
```

### Parquet schema — `CandleRecord`

| Column | Type | Unit / Notes |
|---|---|---|
| `timestamp` | `int64` | Microseconds since Unix epoch, UTC |
| `open` | `float64` | INR |
| `high` | `float64` | INR |
| `low` | `float64` | INR |
| `close` | `float64` | INR |
| `volume` | `int64` | Number of shares traded |
| `oi` | `int64` | Open interest — **always 0 for equity and index candles** (Kite does not report OI for non-derivative instruments) |

### Deduplication and sort order

Within each file, rows are deduplicated on `timestamp` (latest write wins) and sorted ascending by `timestamp`. The pipeline performs a read-modify-write on every append, so each file is always a clean, sorted, deduplicated series.

---

## Dataset 2 — NFO Futures candles

### Scope

| Config field | Value |
|---|---|
| Exchange | NFO |
| Universe | All active futures contracts in the Kite instrument snapshot at run time |
| Daily backfill from | 2004-01-01 |

Futures contracts are identified by `instrument_type = "FUT"` in the Kite instrument dump. This includes both index futures (NIFTY, BANKNIFTY, …) and stock futures (RELIANCE, TCS, …). Each contract (e.g., RELIANCE24JULFUT, RELIANCE24AUGFUT) is stored as a **separate symbol**.

### R2 key pattern

```
NFO/FUT/<tradingsymbol>/<interval>/<partition>.parquet
```

| Component | Values | Example |
|---|---|---|
| `<tradingsymbol>` | Full Kite tradingsymbol of the contract | `RELIANCE24JULFUT`, `NIFTY24JULFUT` |
| `<interval>` | `day` (15min not currently enabled) | `day` |
| `<partition>` | Year for daily; year-month for 15-min | `2024` |

Example:
```
NFO/FUT/RELIANCE24JULFUT/day/2024.parquet
NFO/FUT/NIFTY24JULFUT/day/2024.parquet
```

### Parquet schema — `CandleRecord`

Same schema as equity (above). For futures, `oi` is **populated** and represents the open interest in number of contracts at the end of each bar.

| Column | Type | Unit / Notes |
|---|---|---|
| `timestamp` | `int64` | Microseconds since Unix epoch, UTC |
| `open` | `float64` | INR |
| `high` | `float64` | INR |
| `low` | `float64` | INR |
| `close` | `float64` | INR |
| `volume` | `int64` | Number of contracts traded |
| `oi` | `int64` | Open interest in contracts (populated for futures) |

### Important: contract-level granularity

Each file contains candles for a **single expiry contract**, not a continuous front-month series. To build a continuous futures series you must stitch contracts yourself — typically by rolling at expiry or N days before expiry. Expiry dates are available in the instruments snapshot dataset.

---

## Dataset 3 — NFO Options candles

### Scope

Options data is organised **by underlying**, not by individual contract. All strikes and expiries for a given underlying are merged into one file per time partition. This makes it efficient to load all option chain data for a single underlying at once.

| Config field | Value |
|---|---|
| Underlyings | NIFTY, BANKNIFTY, MIDCPNIFTY, FINNIFTY, RELIANCE, TCS, INFY, HDFCBANK, ICICIBANK, WIPRO |
| Daily backfill from | 2008-01-01 |

Contracts are matched by `tradingsymbol` prefix: e.g., all tradingsymbols starting with `"NIFTY"` (CE and PE, all strikes, all expiries) are collected under the `NIFTY` underlying key.

### R2 key pattern

```
NFO/OPT/<underlying>/<interval>/<partition>.parquet
```

Examples:
```
NFO/OPT/NIFTY/day/2024.parquet
NFO/OPT/BANKNIFTY/day/2024.parquet
NFO/OPT/RELIANCE/day/2024.parquet
```

### Parquet schema — `OptionCandleRecord`

| Column | Type | Unit / Notes |
|---|---|---|
| `timestamp` | `int64` | Microseconds since Unix epoch, UTC |
| `expiry` | `string` | `"YYYY-MM-DD"` — expiry date of this contract |
| `strike` | `float64` | Strike price in INR |
| `option_type` | `string` | `"CE"` (call) or `"PE"` (put) |
| `open` | `float64` | INR |
| `high` | `float64` | INR |
| `low` | `float64` | INR |
| `close` | `float64` | INR |
| `volume` | `int64` | Contracts traded |
| `oi` | `int64` | Open interest in contracts |

### Deduplication key

Within each file, rows are deduplicated on the composite key `(timestamp, expiry, strike, option_type)`. Sort order: `timestamp` → `expiry` → `strike` → `option_type`.

### Querying a specific expiry

When reading options data, filter on `expiry` to isolate a single contract series. For example, to get the NIFTY weekly expiry for 2024-07-25:

```python
df = pd.read_parquet("NFO/OPT/NIFTY/day/2024.parquet")
df = df[df["expiry"] == "2024-07-25"]
```

### Checkpoint granularity

The extraction checkpoint for options tracks progress **per underlying per interval** (not per contract). If any contract fetch fails within a chunk, the checkpoint is not advanced and the chunk is re-fetched on the next run. Since Parquet writes are idempotent (dedup on write), re-fetching is safe.

---

## Dataset 4 — Instrument master snapshots

A daily snapshot of the full Kite instrument universe is written each time the extractor runs. This is a **full overwrite** (not append-merge) — each file represents the complete instrument list as of that date.

### R2 key pattern

```
instruments/snapshots/<YYYY-MM-DD>.parquet
```

Example:
```
instruments/snapshots/2024-07-04.parquet
```

### Parquet schema — `InstrumentRecord`

| Column | Type | Notes |
|---|---|---|
| `instrument_token` | `int64` | Kite's unique integer token; used to request historical data |
| `exchange_token` | `int64` | Exchange-level token (subset of instrument_token) |
| `tradingsymbol` | `string` | Symbol as shown on the exchange |
| `name` | `string` | For futures/options: underlying symbol (e.g. `"RELIANCE"`). For equity: company name or blank. **Do not use `name` to identify underlyings for equity instruments** — use `tradingsymbol`. |
| `exchange` | `string` | `"NSE"`, `"NFO"`, `"BSE"`, `"BFO"`, `"MCX"`, … |
| `instrument_type` | `string` | `"EQ"`, `"FUT"`, `"CE"`, `"PE"` (see note below) |
| `segment` | `string` | `"NSE"`, `"NFO-FUT"`, `"NFO-OPT"`, `"INDICES"`, … |
| `expiry` | `string` | `"YYYY-MM-DD"` for derivatives; `""` for equity and indices |
| `strike` | `float64` | Strike price for options; `0` for non-options |
| `lot_size` | `int64` | Contract lot size; `1` for equity |
| `tick_size` | `float64` | Minimum price movement in INR |

### Critical: NSE index instruments

Kite stores NSE index instruments (NIFTY 50, NIFTY BANK, INDIA VIX, etc.) with:
- `instrument_type = "EQ"` — **not** `"INDICES"`
- `segment = "INDICES"` — this is the correct field to identify them

Filtering by `instrument_type = "INDICES"` returns nothing for NSE indices. Always filter by `segment = "INDICES"` to identify index instruments.

### Using the snapshot for backtesting

Use the snapshot to resolve instrument tokens (needed to call Kite API), look up lot sizes for position sizing, and identify all strikes/expiries available on a given date. Always use the snapshot whose date is closest to (but not after) the date you are backtesting — the instrument universe changes over time.

---

## SQLite tracking database

The SQLite file (`$SQLITE_PATH`, default `./data/stock.db`) is replicated to R2 by Litestream. It is the operational store for extraction progress tracking and the live instrument cache. It is **not** the primary source for historical analysis — use Parquet for that.

### Tables

#### `instruments`

Mirrors the Parquet snapshot in queryable form. The latest snapshot is identified by `snapshot_date = (SELECT MAX(snapshot_date) FROM instruments)`.

```sql
CREATE TABLE instruments (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    snapshot_date     TEXT NOT NULL,          -- 'YYYY-MM-DD'
    instrument_token  INTEGER NOT NULL,
    exchange_token    INTEGER NOT NULL DEFAULT 0,
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
```

Indexes:
- `(snapshot_date, exchange, instrument_type)` — fast lookup by exchange and type
- `(snapshot_date, name)` — fast lookup of all contracts for a given underlying

#### `extraction_state`

Tracks the last successfully extracted date per symbol. Used to make backfill resumable.

```sql
CREATE TABLE extraction_state (
    exchange     TEXT NOT NULL,
    symbol       TEXT NOT NULL,   -- tradingsymbol (sanitized: spaces → hyphens)
    interval     TEXT NOT NULL,   -- 'day' or '15min'
    last_date    TEXT NOT NULL,   -- 'YYYY-MM-DD'
    updated_at   TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (exchange, symbol, interval)
);
```

#### `options_extraction_state`

Same purpose as `extraction_state` but keyed by underlying (not individual contract).

```sql
CREATE TABLE options_extraction_state (
    underlying  TEXT NOT NULL,   -- e.g. 'NIFTY', 'RELIANCE'
    interval    TEXT NOT NULL,
    last_date   TEXT NOT NULL,   -- 'YYYY-MM-DD'
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (underlying, interval)
);
```

---

## Coverage summary

| Dataset | R2 prefix | Interval | From | Universe |
|---|---|---|---|---|
| NSE Equity | `NSE/EQ/` | `day` | 2004-01-01 | ~180 F&O stocks + NSE indices |
| NSE Equity | `NSE/EQ/` | `15min` | 2020-01-01 | ~180 F&O stocks + NSE indices |
| NFO Futures | `NFO/FUT/` | `day` | 2004-01-01 | All active NFO FUT contracts |
| NFO Options | `NFO/OPT/` | `day` | 2008-01-01 | 10 configured underlyings |
| Instrument snapshots | `instruments/snapshots/` | daily file | First run date | Full Kite universe |

> Coverage starts from the backfill dates above but a symbol only has data from the date it started trading. Newly listed stocks and new contract series will have data from their listing date, not from the backfill start.

---

## Reading data — practical examples

### Python (pandas + pyarrow)

```python
import boto3
import pyarrow.parquet as papq
import io

# Configure R2 as S3-compatible endpoint
s3 = boto3.client(
    "s3",
    endpoint_url=f"https://{R2_ACCOUNT_ID}.r2.cloudflarestorage.com",
    aws_access_key_id=R2_ACCESS_KEY_ID,
    aws_secret_access_key=R2_SECRET_ACCESS_KEY,
)

def read_parquet(key: str):
    obj = s3.get_object(Bucket=BUCKET, Key=key)
    return papq.read_table(io.BytesIO(obj["Body"].read())).to_pandas()

# Equity daily for RELIANCE, full year 2024
df = read_parquet("NSE/EQ/RELIANCE/day/2024.parquet")
# Columns: timestamp (int64 µs UTC), open, high, low, close, volume, oi

# Convert timestamp to IST datetime
import pandas as pd
df["datetime"] = pd.to_datetime(df["timestamp"], unit="us", utc=True).dt.tz_convert("Asia/Kolkata")

# NIFTY index daily
nifty = read_parquet("NSE/EQ/NIFTY-50/day/2024.parquet")

# NIFTY options for 2024 — all strikes and expiries in one file
opts = read_parquet("NFO/OPT/NIFTY/day/2024.parquet")
# Filter to a specific expiry
weekly = opts[opts["expiry"] == "2024-07-25"]
# Filter to ATM call
atm_call = weekly[(weekly["strike"] == 24000.0) & (weekly["option_type"] == "CE")]
```

### Listing available files

```python
# List all equity symbols
paginator = s3.get_paginator("list_objects_v2")
symbols = set()
for page in paginator.paginate(Bucket=BUCKET, Prefix="NSE/EQ/", Delimiter="/"):
    for prefix in page.get("CommonPrefixes", []):
        symbol = prefix["Prefix"].split("/")[2]
        symbols.add(symbol)

# List all futures contracts
contracts = set()
for page in paginator.paginate(Bucket=BUCKET, Prefix="NFO/FUT/", Delimiter="/"):
    for prefix in page.get("CommonPrefixes", []):
        contracts.add(prefix["Prefix"].split("/")[2])
```

### Loading multiple partitions

```python
def load_equity_range(symbol: str, interval: str, start: str, end: str):
    """Load equity candles across multiple year (daily) or month (15min) partitions."""
    import pandas as pd
    from dateutil.relativedelta import relativedelta

    frames = []
    cursor = pd.Timestamp(start)
    end_ts = pd.Timestamp(end)

    while cursor <= end_ts:
        if interval == "day":
            key = f"NSE/EQ/{symbol}/day/{cursor.year}.parquet"
            cursor += relativedelta(years=1)
        else:
            key = f"NSE/EQ/{symbol}/15min/{cursor.strftime('%Y-%m')}.parquet"
            cursor += relativedelta(months=1)
        try:
            frames.append(read_parquet(key))
        except s3.exceptions.NoSuchKey:
            pass

    if not frames:
        return pd.DataFrame()

    df = pd.concat(frames).drop_duplicates("timestamp").sort_values("timestamp")
    # Trim to exact requested range (microseconds)
    from_us = int(pd.Timestamp(start).timestamp() * 1e6)
    to_us   = int(pd.Timestamp(end).replace(hour=23, minute=59, second=59).timestamp() * 1e6)
    return df[(df["timestamp"] >= from_us) & (df["timestamp"] <= to_us)].reset_index(drop=True)
```

---

## Design decisions relevant to consumers

### Partitioning strategy

- **Daily data → partitioned by year.** A single `2024.parquet` file contains every trading day of 2024 for one symbol. This keeps file count low while allowing efficient yearly loads.
- **15-min data → partitioned by month.** Each `YYYY-MM.parquet` file has ~22 trading days × 26 bars = ~572 rows. Monthly partitioning stays within Kite's API chunk limit (60-day windows) and gives convenient monthly reload granularity.

### Idempotent writes

Every append operation is a **read-modify-write**: the pipeline downloads the existing Parquet file, merges new rows (deduplicating on the natural key), re-sorts, and uploads. This means:

1. Re-running the pipeline over the same date range is safe — no duplicate rows.
2. An interrupted write does not corrupt existing data (R2 upload is atomic).
3. Backfill and incremental runs can safely write to the same files.

### Futures: individual contracts, not continuous series

Each futures file is for one named contract (e.g., `RELIANCE24JULFUT`). There is no pre-built continuous/rollover series. For strategy backtesting you will need to:
- Load all contracts for an underlying by listing `NFO/FUT/` keys that match a prefix (e.g., all keys starting with `RELIANCE`)
- Build a rollover series using expiry dates from the instruments snapshot

### Symbol sanitization

NSE index tradingsymbols in Kite contain spaces (`"NIFTY 50"`, `"NIFTY BANK"`). These are normalised to hyphens in all R2 keys and SQLite checkpoint keys:

| Kite tradingsymbol | R2 key uses |
|---|---|
| `NIFTY 50` | `NIFTY-50` |
| `NIFTY BANK` | `NIFTY-BANK` |
| `NIFTY NEXT 50` | `NIFTY-NEXT-50` |

Regular equity symbols (e.g., `RELIANCE`, `TCS`) contain no spaces and are stored as-is.

### OI field semantics

| Dataset | `oi` field |
|---|---|
| NSE Equity (stocks) | Always `0` — Kite does not provide OI for equity |
| NSE Index instruments | Always `0` |
| NFO Futures | Populated — open interest in number of contracts |
| NFO Options | Populated — open interest in number of contracts |
