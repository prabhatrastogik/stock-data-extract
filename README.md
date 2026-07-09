# stock-data-extract

A self-hosted Go binary that extracts historical OHLC data from the Zerodha Kite Connect API and stores it as Parquet files on Cloudflare R2. Designed for the Indian equity market (NSE/NFO).

---

## What it does

- **Backfill**: Fetches 20 years of daily candles and 3–5 years of 15-min candles for configured NSE equity stocks, NFO futures contracts, and options underlyings.
- **Daily incremental**: A cron job runs each morning (8:00 AM IST) to fetch the previous trading day's candles and append them to the existing Parquet files.
- **Automated login**: Uses a headless Chrome browser to automate the Zerodha TOTP login flow. No human interaction required for token refresh.
- **Resumable**: Every extraction is checkpointed in SQLite. A crash or restart picks up exactly where it left off.

Data is stored as Parquet files on Cloudflare R2. SQLite (on the local filesystem, replicated to R2 via Litestream) stores instrument metadata and extraction progress.

---

## Data scope

| Asset class | Exchange | Intervals | History |
|---|---|---|---|
| Equity stocks (F&O universe, ~211 symbols) | NSE | daily, 15-min | Daily: 2004–present; 15-min: 2020–present |
| NSE indices (NIFTY 50, NIFTY BANK) | NSE | daily, 15-min | Daily: 2004–present; 15-min: 2020–present |
| Futures contracts (all active) | NFO | daily | Daily: 2004–present |
| Options (index + configured stocks) | NFO | daily | Daily: 2008–present |

Symbol universe is controlled by `config.yaml`:
- Set `fno_only: false` to extract all ~2000 NSE EQ symbols instead of just F&O-eligible stocks.
- Set `include_indices: false` to skip index instruments.
- Use the `indices:` list to restrict which specific indices are fetched (empty = all).
- Set `disabled: true` on any asset block to skip it entirely in scheduled runs and catch-up backfills.

Options are stored in a **by-underlying model**: all strikes and expiries for one underlying (e.g. NIFTY) live in a single Parquet file per year/month, rather than one file per contract. See `DATA.md` for the full data reference.

---

## R2 storage layout

```
stock-data/
├── NSE/EQ/{SYMBOL}/day/{YYYY}.parquet
├── NSE/EQ/{SYMBOL}/15min/{YYYY-MM}.parquet
├── NFO/FUT/{SYMBOL}/day/{YYYY}.parquet
├── NFO/FUT/{SYMBOL}/15min/{YYYY-MM}.parquet
├── NFO/OPT/{UNDERLYING}/day/{YYYY}.parquet
├── NFO/OPT/{UNDERLYING}/15min/{YYYY-MM}.parquet
├── instruments/snapshots/{YYYY-MM-DD}.parquet
└── sqlite-backup/                           ← managed by Litestream
```

Timestamps inside every Parquet file are stored as **Unix microseconds UTC** (`int64`). Daily files are partitioned by year; 15-min files are partitioned by month.

NSE index tradingsymbols that contain spaces (e.g. `"NIFTY 50"`) are normalised to hyphens (`"NIFTY-50"`) in all R2 keys and SQLite checkpoints.

---

## First-run checklist

Follow this sequence the very first time you set up the system:

1. **Create a Cloudflare R2 bucket** and generate an API token with `Object Read & Write` permission on that bucket.

2. **Get a Kite Connect developer account** at [developers.kite.trade](https://developers.kite.trade). Create an app and note the `api_key` and `api_secret`. Set the redirect URL to `https://kite.zerodha.com` (or any URL you control — `token-refresh` captures the redirect automatically).

3. **Copy `.env.example` to `.env`** and fill in all required values. Leave `SQLITE_PATH=./data/stock.db` for local runs (Docker overrides this automatically).

4. **Install Litestream** (required for R2 sync — not in Homebrew):
   ```bash
   # Apple Silicon (arm64)
   curl -L https://github.com/benbjohnson/litestream/releases/download/v0.3.13/litestream-v0.3.13-darwin-arm64.zip \
     -o /tmp/litestream.zip && unzip /tmp/litestream.zip -d /tmp/ && sudo mv /tmp/litestream /usr/local/bin/
   ```

5. **Get an access token:**
   ```bash
   ./run-local.sh token-refresh
   ```
   Copy the printed token into your `.env` as `KITE_ACCESS_TOKEN=...`.
   (If you have `KITE_USER_ID`/`KITE_PASSWORD`/`KITE_TOTP_SECRET` set, the app auto-refreshes on each run — you can skip this step once those are in `.env`.)

6. **Run the initial backfill.** With default config (~213 symbols + futures + options), each command takes minutes to hours. Equity daily from 2004 across all F&O stocks is the longest. Use tmux to keep it running:
   ```bash
   ./run-local.sh backfill --type equity --interval day
   ./run-local.sh backfill --type equity --interval 15min
   ./run-local.sh backfill --type futures --interval day
   ./run-local.sh backfill --type options --interval day
   ```
   Run these **sequentially** — parallel runs would create multiple Litestream processes writing to the same R2 path. Each command is resumable.

7. **Start the scheduler** to begin nightly incremental updates:
   ```bash
   ./run-local.sh
   ```
   Or run it via Docker (see [Deployment](#deployment)).

---

## Quick start

### Prerequisites

- Go 1.26+
- Chrome or Chromium installed (for automated login)
- A Zerodha Kite Connect developer account and API subscription
- A Cloudflare R2 bucket

### Environment variables

```bash
# Kite Connect
export KITE_API_KEY=...
export KITE_API_SECRET=...

# Automated login — needed for token-refresh and nightly auto-refresh.
# If these are set, KITE_ACCESS_TOKEN is optional: the app refreshes itself
# on startup when the token is expired or missing.
export KITE_USER_ID=...          # your Zerodha user ID (e.g. AB1234)
export KITE_PASSWORD=...
export KITE_TOTP_SECRET=...      # base32 secret from your 2FA setup page

# Optional — only needed if auto-refresh credentials above are not set.
# export KITE_ACCESS_TOKEN=...

# Cloudflare R2
export R2_ACCOUNT_ID=...
export R2_ACCESS_KEY_ID=...
export R2_SECRET_ACCESS_KEY=...
export R2_BUCKET=stock-data   # single source of truth — also read directly by Litestream

# Optional
export CONFIG_PATH=./config.yaml
export SQLITE_PATH=./data/stock.db   # local dev default; Docker overrides to /data/stock.db

# Token refresh debug — opens a visible Chrome window instead of headless
# export KITE_LOGIN_DEBUG=1
```

### Build

```bash
CGO_ENABLED=0 go build -o stock-data-extract ./cmd/
```

The binary has **zero CGo dependencies** — it uses pure-Go SQLite (`modernc.org/sqlite`) and pure-Go Parquet (`parquet-go/parquet-go`).

### Get an access token

```bash
./stock-data-extract token-refresh
```

This opens a headless browser, logs into Zerodha using your credentials + TOTP, and prints the new access token. Set it as `KITE_ACCESS_TOKEN` and re-run as needed (tokens expire at 6 AM IST daily).

### Run one incremental sync manually

```bash
./stock-data-extract incremental
```

Runs a single incremental pass immediately (same logic as the cron job): fetches today's instruments snapshot, then extracts yesterday's candles for all non-disabled asset types. Useful for triggering a sync on demand or debugging without waiting for the cron schedule.

### Run the nightly scheduler

```bash
./stock-data-extract run
```

Starts the cron scheduler. Two jobs run:
- **Incremental** (Tue–Sat 8:00 AM IST): fetches the previous trading day for all non-disabled asset types.
- **Catch-up backfill** (Sat 10:00 AM IST): runs after the incremental and fills any gaps from binary downtime during the week, for all non-disabled asset types.

### Run a historical backfill

```bash
# Equity daily (F&O universe + configured indices, ~213 symbols)
./stock-data-extract backfill --type equity --interval day

# Equity 15-min (2020–present)
./stock-data-extract backfill --type equity --interval 15min

# Futures daily
./stock-data-extract backfill --type futures --interval day

# Options daily for configured underlyings
./stock-data-extract backfill --type options --interval day
```

Backfills are **resumable**. Kill and restart at any point — it reads `extraction_state` from SQLite and continues from where it left off. The `disabled` flag in config has no effect on manual `backfill` commands — it only controls scheduled runs.

---

## Configuration (`config.yaml`)

```yaml
extraction:
  equity:
    exchanges: ["NSE"]
    intervals: ["day", "15min"]
    backfill_from:
      day: "2004-01-01"
      15min: "2020-01-01"
    fno_only: true        # only F&O-eligible stocks (~211 symbols); false = all ~2000 NSE EQ
    include_indices: true # also include NSE index instruments
    indices:              # specific indices to fetch (omit or leave empty for all)
      - "NIFTY 50"
      - "NIFTY BANK"
    # disabled: true      # uncomment to skip equity in scheduled runs

  futures:
    disabled: true        # skip futures in scheduled incremental and catch-up backfill
    exchanges: ["NFO"]
    intervals: ["day"]    # add "15min" to enable 15-min futures
    backfill_from:
      day: "2004-01-01"
      15min: "2020-01-01"

  options:
    exchanges: ["NFO"]
    underlyings: [NIFTY, BANKNIFTY, ETERNAL, RELIANCE, TCS, INFY, HDFCBANK, ICICIBANK]
    intervals: ["day"]    # add "15min" to enable 15-min options
    backfill_from:
      day: "2008-01-01"
      15min: "2020-01-01"
    # disabled: true      # uncomment to skip options in scheduled runs

kite:
  rate_limit_rps: 3        # Kite historical API limit (per API key)
  chunk_days:
    day: 365               # max days per API call for daily interval
    15min: 60              # max days per API call for 15-min interval

cron:
  schedule: "30 2 * * 2-6"         # 8:00 AM IST = 02:30 UTC, Tue–Sat
  backfill_schedule: "30 4 * * 6"  # 10:00 AM IST = 04:30 UTC, Sat catch-up

# R2 bucket and SQLite path are not in config.yaml — set via env vars:
#   R2_BUCKET=stock-data             (also read by Litestream)
#   SQLITE_PATH=./data/stock.db      (local dev; Docker overrides to /data/stock.db)
```

---

## Package reference

### `cmd/`

**`main.go`** — Binary entrypoint. Parses the subcommand and dispatches. `token-refresh` is handled immediately before any initialisation (no config, SQLite, or R2 needed). All other commands load config, create the SQLite directory, open the database, and initialise R2 and the Kite provider before dispatching.

- `run`: starts the cron scheduler and blocks until SIGTERM/SIGINT.
- `incremental`: runs a single incremental pass immediately — same logic as the cron job. Useful for on-demand syncs and debugging.
- `backfill --type <equity|futures|options> --interval <day|15min>`: runs a full historical backfill for the given asset class and interval. The `disabled` flag in config has no effect here.
- `token-refresh`: runs the automated browser login and prints the new access token.

---

### `internal/config/`

**`config.go`** — Loads `config.yaml` from disk (path from `CONFIG_PATH` env or default `./config.yaml`). Populates `SQLitePath` from `SQLITE_PATH` env var (default `./data/stock.db`) and `R2Bucket` from `R2_BUCKET` env var — these are env-only so that Litestream and `start.sh` share the same single source of truth. Validates required fields. Returns a typed `Config` struct used throughout the binary.

Key types: `Config`, `ExtractionConfig`, `AssetConfig`, `OptionsConfig`, `KiteConfig`, `CronConfig`.

`AssetConfig` fields of note:
- `Disabled bool` — when true, skips this asset type in incremental runs and the scheduled Saturday backfill. Has no effect on manual `backfill` commands.
- `FnOOnly bool` — restricts NSE equity to stocks with active NFO futures contracts.
- `IncludeIndices bool` — appends NSE index instruments (identified by `segment = "INDICES"` in Kite's data).
- `Indices []string` — if non-empty, restricts included indices to this list. Accepts either the raw Kite form (`"NIFTY 50"`) or the hyphenated form (`"NIFTY-50"`).

---

### `internal/provider/`

**`provider.go`** — Defines the `Provider` interface that all data sources must implement. This is the only abstraction layer between the extractor and the broker API. Swapping to a different broker (e.g. ICICI, Dhan) means implementing this interface and changing the wiring in `main.go` — nothing else changes.

```go
type Provider interface {
    Instruments(ctx context.Context, exchange string) ([]Instrument, error)
    Historical(ctx context.Context, token string, interval string,
               from, to time.Time, continuous bool, oi bool) ([]Candle, error)
}
```

`Candle` and `Instrument` are the canonical types used everywhere else in the system.

---

### `internal/provider/kite/`

The Zerodha Kite Connect implementation of the `Provider` interface.

**`kite.go`** — `KiteProvider` struct and its `Historical` method. Wraps `gokiteconnect/v4`. Key behaviours:
- Maps internal interval names to Kite's naming: `"15min"` → `"15minute"`.
- Automatic retry on HTTP 429 rate-limit errors: up to 3 attempts with exponential backoff (1s, 2s).
- `ValidateToken` method calls `GetUserProfile` as a cheap token validity check before each nightly run.
- `SetAccessToken` allows the incremental extractor to hot-swap a fresh token after auto-refresh without restarting the binary.

**`instruments.go`** — `KiteProvider.Instruments` method. Fetches the full instrument dump for an exchange via `GetInstrumentsByExchange`, maps it from Kite's CSV-parsed struct to the canonical `provider.Instrument` type. Derivative expiry dates are parsed from Kite's time format; equity instruments get a zero `Expiry`.

**`ratelimit.go`** — Token-bucket rate limiter wrapping `golang.org/x/time/rate`. All Kite API calls call `limiter.Wait(ctx)` before executing, ensuring the 3 req/sec limit is never exceeded.

**`login.go`** — Automated browser login using `chromedp` (headless Chrome via DevTools Protocol). Flow:
1. Navigate to the Kite login URL (`kc.GetLoginURL()`).
2. Fill user ID and password.
3. Wait for TOTP screen; generate the 6-digit code from `KITE_TOTP_SECRET` using `pquerna/otp`.
4. Submit TOTP; poll the browser location until the redirect URL contains `request_token=`.
5. Parse `request_token` from the redirect URL, call `kc.GenerateSession` to exchange it for an `access_token`.

> **Note on selectors**: Kite's login page reuses `input[id="userid"]` across all steps. The TOTP field is identified by `input[type="number"]`. If login breaks after a Kite UI update, open DevTools on `https://kite.zerodha.com`, run `document.querySelectorAll('input').forEach(i => console.log(i.id, i.type, i.maxLength))` on each step, and update the selectors in `login.go`. Set `KITE_LOGIN_DEBUG=1` to open a visible browser window.

---

### `internal/storage/r2/`

**`client.go`** — Thin wrapper around `aws-sdk-go-v2/service/s3` configured to point at Cloudflare R2's S3-compatible endpoint (`https://{account_id}.r2.cloudflarestorage.com`). Uses path-style addressing (`UsePathStyle: true`) as required by R2.

Three methods:
- `Upload(ctx, key, data, contentType)` — `PutObject`
- `Download(ctx, key) ([]byte, error)` — `GetObject`; returns `(nil, nil)` on `NoSuchKey` so callers can distinguish "file doesn't exist yet" from errors.
- `Exists(ctx, key) (bool, error)` — `HeadObject`; available for callers that need an existence check without downloading the body.

---

### `internal/storage/parquet/`

**`schema.go`** — Three Parquet-tagged structs that define the on-disk data model:

| Struct | Used for | Dedup key |
|---|---|---|
| `CandleRecord` | Equity and futures OHLCV+OI | `Timestamp` (Unix µs UTC) |
| `OptionCandleRecord` | Options OHLCV+OI with expiry/strike/type | `(Timestamp, Expiry, Strike, OptionType)` |
| `InstrumentRecord` | Daily instrument snapshot | — (full overwrite) |

**`keys.go`** — All R2 key construction is centralised here. Seven functions (`EquityDayKey`, `Equity15MinKey`, `FuturesDayKey`, `Futures15MinKey`, `OptionsDayKey`, `Options15MinKey`, `InstrumentsSnapshotKey`) produce the canonical R2 paths. No other file constructs R2 keys directly.

**`writer.go`** — Read-modify-write pattern for Parquet files:
1. Download the existing file from R2 (returns empty if the file doesn't exist yet).
2. Merge incoming records with existing ones; deduplicate by the struct's dedup key.
3. Sort ascending by `Timestamp`.
4. Serialise and upload back to R2.

`AppendCandles` handles `CandleRecord`, `AppendOptionCandles` handles `OptionCandleRecord`. `WriteInstruments` is a plain overwrite (no merge) used for the daily instruments snapshot.

**`reader.go`** — Download and in-memory filter. `ReadCandles` returns rows within a `[from, to]` time range. `ReadOptionCandles` additionally supports filtering by a specific expiry date. Both return an empty slice (not an error) when the R2 key doesn't exist.

---

### `internal/storage/sqlite/`

SQLite is used for two things: instrument metadata (for token lookups during extraction) and extraction progress state (so backfills are resumable).

**`db.go`** — Opens the database using `modernc.org/sqlite` (pure Go, no CGo). Sets three PRAGMAs on every open:
- `journal_mode=WAL` — enables concurrent reads alongside writes.
- `wal_autocheckpoint=0` — disables SQLite's built-in WAL auto-checkpoint so Litestream can manage checkpointing itself. Without this, SQLite can move WAL frames to the main DB mid-snapshot, producing non-sequential page numbers in the R2 backup and causing restore failures.
- `busy_timeout=5000` — prevents lock errors when Litestream holds a read lock during snapshot.

Runs versioned SQL migrations embedded via `//go:embed migrations/*.sql`. Migrations are idempotent — safe to run on every startup.

**Migrations** (`migrations/001–005.sql`):
- `001_instruments.sql` — `instruments` table: one row per instrument per snapshot date.
- `002_extraction_state.sql` — `extraction_state` table: tracks the last successfully extracted date per `(exchange, symbol, interval)` triple.
- `003_options_extraction_state.sql` — `options_extraction_state` table: same for options, keyed by `(underlying, interval)`.
- `004_schema_migrations.sql` — `schema_migrations` table definition.
- `005_exchange_token.sql` — adds `exchange_token` column to `instruments`.

**`state.go`** — CRUD for extraction state:
- `GetLastDate / SetLastDate` — for equity and futures (keyed by exchange + symbol + interval).
- `GetOptionsLastDate / SetOptionsLastDate` — for options (keyed by underlying + interval).

All dates are stored as `"YYYY-MM-DD"` strings in SQLite and converted to/from `time.Time` in Go.

**`instruments.go`** — CRUD for instrument records:
- `UpsertInstruments(date, records)` — bulk upsert for a full day's snapshot.
- `LatestInstruments(exchange, instrumentType)` — fetches by `instrument_type`. Used for FUT, CE, PE lookups.
- `FnOEquityInstruments(equityExchange, futuresExchanges)` — returns NSE EQ instruments whose `tradingsymbol` appears as the `name` field of a NFO FUT contract. This is the SQL backing `fno_only: true`. Excludes `segment = "INDICES"` to prevent index instruments leaking into the F&O list.
- `LatestInstrumentsBySegment(exchange, segment)` — filters by the `segment` column rather than `instrument_type`. Required for NSE index instruments, which Kite stores with `instrument_type = "EQ"` and `segment = "INDICES"` — not `instrument_type = "INDICES"`.
- `SearchInstruments(query, limit)` — fuzzy LIKE search by `tradingsymbol` or `name`.
- `OptionExpiries(underlying)` — returns distinct sorted expiry dates for an underlying.

---

### `internal/extractor/`

**`shared.go`** — Package-level utilities shared by all three extractors:
- `AutoRefreshConfig` — credentials for automated browser login, used by both backfiller and incremental extractor.
- `LastTradingDay(today)` — returns yesterday, skipping back to Friday on weekends. Exported so `cmd/` and `scheduler/` can use it without duplicating logic.
- `isAuthError(err)` — detects Kite access token errors by string matching. Auth errors are non-retriable and abort the entire run.
- `equityKey`, `futuresKey` — canonical R2 key builders for a given symbol and time.
- `candlesToRecords`, `instrumentsToParquet` — type conversion helpers.
- `sanitizeSymbol(s)` — replaces spaces with hyphens. NSE index tradingsymbols contain spaces (`"NIFTY 50"` → `"NIFTY-50"`); this normalisation is applied at load time so all downstream code uses safe keys.
- `equityInstrumentsFromDB(db, cfg, exchange)` — central function that both backfiller and incremental extractor call to get the equity instrument list. Applies `fno_only` (calls `FnOEquityInstruments`) and `include_indices` (calls `LatestInstrumentsBySegment("INDICES")`), then filters indices against the `indices` list if non-empty. Applies `sanitizeSymbol` to every returned tradingsymbol.

**`backfill.go`** — Full historical backfill. Entry point is `Backfiller.Run(ctx, BackfillConfig)`. Validates and auto-refreshes the access token at startup. Dispatches to equity, futures, or options sub-routines based on `BackfillConfig.Type`.

When `fno_only: true`, `runEquity` prefetches NFO instruments into SQLite before the equity loop, even if only `--type equity` was requested — because the F&O cross-reference query needs NFO FUT data in the DB.

Core logic for equity and futures:
1. Load instruments from SQLite (fetches from Kite API and upserts if SQLite is empty).
2. For each symbol: read `last_date` from `extraction_state` to determine resume point.
3. Split the date range into chunks of `config.kite.chunk_days` days.
4. Further split each chunk by year (daily) or month (15-min) so each `AppendCandles` call targets exactly one Parquet file.
5. Fetch candles, write to R2, update `extraction_state`. Checkpoint is only advanced if every sub-chunk succeeded — a partial failure re-fetches the chunk on restart (writes are idempotent).

**`options.go`** — Options-specific extraction. All CE and PE contracts for an underlying are matched by `TradingSymbol` prefix (e.g. all symbols starting with `NIFTY`). Records across all contracts are accumulated per Parquet key and written in one `AppendOptionCandles` call per key. Checkpoint is per-underlying, not per-contract.

**`incremental.go`** — Nightly incremental run. `IncrementalExtractor.Run`:
1. Validates and auto-refreshes the access token.
2. Fetches today's instruments snapshot from Kite, upserts to SQLite, writes the snapshot Parquet to R2.
3. Runs equity, futures, and options extraction for yesterday — each step is skipped if the corresponding asset config has `disabled: true`.

**`scheduler/cron.go`** — Wraps `robfig/cron/v3`. Two cron jobs:
- **Incremental** (`cron.schedule`): runs `IncrementalExtractor`; respects `disabled` flags.
- **Catch-up backfill** (`cron.backfill_schedule`): runs `Backfiller` across all non-disabled asset types and intervals to fill gaps from downtime during the week.

`Stop()` waits for any currently-running job to finish before returning, ensuring clean shutdown.

---

## Key design decisions

**Zero CGo.** The binary is built with `CGO_ENABLED=0`. This allows simple Docker cross-compilation and avoids C toolchain dependencies.

**Read-modify-write for Parquet.** There is no streaming append to Parquet files. Each write downloads the existing file, merges and deduplicates in memory, and re-uploads. This keeps each file self-contained and makes the system robust to partial failures — but it means large files are expensive to update. Mitigated by using yearly partitioning for daily data and monthly partitioning for 15-min data.

**Instruments are ephemeral for derivatives.** Kite recycles instrument tokens after each expiry cycle. A futures token from last month is not valid today. The daily instruments snapshot is the authoritative token source. Tokens are never hardcoded.

**Options backfill is limited to currently-listed contracts.** Expired contracts from before Kite's lookback window cannot be retrieved. The options Parquet files will only contain data from when you started running the extractor.

**NSE index instrument quirk.** Kite stores NSE index instruments (NIFTY 50, NIFTY BANK, etc.) with `instrument_type = "EQ"` and `segment = "INDICES"` — not `instrument_type = "INDICES"`. All index queries filter by `segment`, not `instrument_type`. Index tradingsymbols contain spaces and are normalised to hyphens in all R2 keys and SQLite checkpoints.

**Litestream for SQLite durability.** SQLite lives on a local persistent volume. Litestream streams WAL changes to R2 continuously (`sync-interval: 1m`). On every start, `start.sh` and `run-local.sh` unconditionally restore SQLite from R2 before launching the binary. In production (`start.sh`), Litestream runs as PID 1 with the Go binary as a managed subprocess (`litestream replicate -exec`). This ensures Docker's SIGTERM reaches Litestream first, which gracefully stops the binary and then flushes WAL segments to R2 before exiting. SQLite's built-in WAL auto-checkpoint is disabled (`PRAGMA wal_autocheckpoint=0`) so Litestream has sole control over checkpointing, preventing snapshot corruption.

**Do not run parallel backfills.** Each `run-local.sh` invocation starts its own Litestream replication process writing to the same R2 path. Multiple concurrent writers corrupt the Litestream backup. Run equity, futures, and options backfills sequentially — each is resumable so there is no cost to stopping between them.

---

## Token expiry

Kite access tokens expire at **6:00 AM IST daily**. If `KITE_USER_ID` + `KITE_PASSWORD` + `KITE_TOTP_SECRET` are set, both the incremental extractor and the backfill command auto-refresh the token at startup — no intervention needed.

If you are **not** using auto-refresh:
```bash
./stock-data-extract token-refresh
# prints: export KITE_ACCESS_TOKEN=<new_token>
```
Update `KITE_ACCESS_TOKEN` in your `.env` or deployment secrets, then restart the binary.

---

## Reading the data

See `DATA.md` for the full data reference, including schema tables, R2 key patterns, timestamp semantics, and Python/DuckDB code examples.

**Quick DuckDB example:**
```sql
SET s3_endpoint = '<account_id>.r2.cloudflarestorage.com';
SET s3_access_key_id = '...';
SET s3_secret_access_key = '...';
SET s3_region = 'auto';

-- RELIANCE daily candles, all years
SELECT
    make_timestamptz(timestamp, 'UTC') AS ts,
    open, high, low, close, volume
FROM read_parquet('s3://stock-data/NSE/EQ/RELIANCE/day/*.parquet')
ORDER BY ts;

-- NIFTY 50 index daily
SELECT * FROM read_parquet('s3://stock-data/NSE/EQ/NIFTY-50/day/*.parquet');

-- NIFTY options — all strikes/expiries for 2024
SELECT * FROM read_parquet('s3://stock-data/NFO/OPT/NIFTY/day/2024.parquet')
WHERE expiry = '2024-12-26' AND strike = 25000 AND option_type = 'CE';
```

**Timestamp field:** stored as Unix microseconds UTC (`int64`). In Python: `pd.to_datetime(df['timestamp'], unit='us', utc=True).dt.tz_convert('Asia/Kolkata')`.

---

## Running tests

```bash
CGO_ENABLED=0 go test ./...
```

Tests cover:
- `extractor` — Pure function tests for `chunkDateRange`, `splitByYear/Month`, `LastTradingDay`, `filterByUnderlying`, `sanitizeSymbol`, `equityInstrumentsFromDB` (F&O filter, index inclusion, index allowlist). Integration tests for `Backfiller` (stores candles, checkpoint not advanced on error, resume from checkpoint, skip when complete, fetch from provider when DB empty) and `OptionsExtractor`.
- `scheduler` — Cron expression validation, start/stop lifecycle, double-Stop safety.
- `provider/kite` — Integration tests against `httptest.Server`: `ValidateToken`, `Instruments`, `Historical` (success, retry, rate-limit, non-rate-limit errors, interval mapping, OI/continuous params, invalid token).
- `storage/parquet` — R2 key generation correctness for all 7 key types.
- `storage/sqlite` — Migration idempotency, extraction state round-trips, not-found behaviour, `FnOEquityInstruments` (F&O cross-reference, index exclusion, empty futures exchanges), `LatestInstrumentsBySegment` (returns only segment-matched instruments).

---

## Deployment

The binary is platform-agnostic. Any Linux host (EC2, VPS, Fly.io, bare metal) works as long as it has:
- A persistent disk/volume for SQLite
- Docker or a Go toolchain to build the binary
- Chrome/Chromium installed if using automated token refresh

### Local development (without Docker)

`run-local.sh` is a convenience wrapper. It:
1. Loads `.env` automatically
2. Rebuilds the binary before each run
3. Restores the latest SQLite from R2 (R2 is the source of truth)
4. Runs Litestream replication alongside the binary
5. Kills Litestream cleanly when the binary exits (SIGTERM + wait)

```bash
# Run the scheduler
./run-local.sh

# Run a backfill
./run-local.sh backfill --type equity --interval day

# Refresh the Kite access token (skips Litestream entirely)
./run-local.sh token-refresh
```

If Litestream is not installed, the script falls back to running the binary directly with a warning.

---

### Docker Compose (recommended for single-host deployments)

```bash
# Build and start
docker compose up -d

# Run a one-off backfill inside the running container
docker compose exec stock-data-extract /app/stock-data-extract backfill --type equity --interval day

# Tail logs
docker compose logs -f
```

`docker-compose.yml` mounts a named volume at `/data` for SQLite and **hardcodes `SQLITE_PATH=/data/stock.db`** in the `environment:` block — this overrides whatever is in `.env` for Docker. Do not change `SQLITE_PATH` in `.env` to `/data/stock.db`; that would break local dev runs. The `stop_grace_period` is set to 5 minutes to allow a running incremental job to finish before the container is force-killed.

### Docker (manual)

```bash
docker build -t stock-data-extract .

docker run -d \
  --env-file .env \
  -e SQLITE_PATH=/data/stock.db \
  -v /mnt/data:/data \
  --stop-timeout 300 \
  stock-data-extract
```

`start.sh` handles startup: restore SQLite from R2 → run `litestream replicate -exec /app/stock-data-extract run` (Litestream as PID 1, binary as managed subprocess). On `docker stop`, SIGTERM hits Litestream first, which stops the binary gracefully and then flushes WAL segments to R2.

### Environment variables required at runtime

```bash
KITE_API_KEY=...
KITE_API_SECRET=...
KITE_USER_ID=...
KITE_PASSWORD=...
KITE_TOTP_SECRET=...
R2_ACCOUNT_ID=...
R2_ACCESS_KEY_ID=...
R2_SECRET_ACCESS_KEY=...
R2_BUCKET=stock-data
# SQLITE_PATH is set automatically by Docker Compose; set manually for docker run
# KITE_ACCESS_TOKEN is optional — the app auto-refreshes via browser login
```
