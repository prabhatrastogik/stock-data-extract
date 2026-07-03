# stock-data-extract

A self-hosted Go binary that extracts historical OHLC data from the Zerodha Kite Connect API and stores it as Parquet files on Cloudflare R2. Designed for the Indian equity market (NSE/NFO).

---

## What it does

- **Backfill**: Fetches 20 years of daily candles and 3–5 years of 15-min candles for all NSE equity stocks, all active NFO futures contracts, and a configurable set of options underlyings.
- **Daily incremental**: A cron job runs each morning (8:00 AM IST) to fetch the previous trading day's candles and append them to the existing Parquet files.
- **Automated login**: Uses a headless Chrome browser to automate the Zerodha TOTP login flow. No human interaction required for token refresh.
- **Resumable**: Every extraction is checkpointed in SQLite. A crash or restart picks up exactly where it left off.

Data is stored as Parquet files on Cloudflare R2. SQLite (on the local filesystem, replicated to R2 via Litestream) stores instrument metadata and extraction progress.

---

## Data scope

| Asset class | Exchange | Intervals | History |
|---|---|---|---|
| Equity stocks (~2000 symbols) | NSE | daily, 15-min | Daily: 2004–present; 15-min: 2020–present |
| Futures contracts (all active) | NFO | daily | Daily: 2004–present |
| Options (index + configured stocks) | NFO | daily | Daily: 2008–present |

> Add `"15min"` to `futures.intervals` / `options.intervals` in `config.yaml` to enable 15-min collection for those asset classes.

Options are stored in a **by-underlying model**: all strikes and expiries for one underlying (e.g. NIFTY) live in a single Parquet file per year/month, rather than one file per contract.

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

---

## First-run checklist

Follow this sequence the very first time you set up the system:

1. **Create a Cloudflare R2 bucket** and generate an API token with `Object Read & Write` permission on that bucket.

2. **Get a Kite Connect developer account** at [developers.kite.trade](https://developers.kite.trade). Create an app and note the `api_key` and `api_secret`. Set the redirect URL to `https://kite.zerodha.com` (or any URL you control — `token-refresh` captures the redirect automatically).

3. **Copy `.env.example` to `.env`** and fill in all required values. Set `SQLITE_PATH=./data/stock.db` for local runs.

4. **Build the binary:**
   ```bash
   CGO_ENABLED=0 go build -o stock-data-extract ./cmd/
   ```

5. **Get an access token:**
   ```bash
   source .env   # or: set -a && . .env && set +a
   ./stock-data-extract token-refresh
   ```
   Copy the printed token into your `.env` as `KITE_ACCESS_TOKEN=...`.  
   (If you have `KITE_USER_ID`/`KITE_PASSWORD`/`KITE_TOTP_SECRET` set, the app auto-refreshes the token on each run — you can skip this step entirely once those are in `.env`.)

6. **Run the initial backfill.** This is the longest step — equity daily across ~2000 symbols from 2004 takes many hours. Use tmux to keep it running:
   ```bash
   tmux new -s backfill
   ./stock-data-extract backfill --type equity --interval day
   # Ctrl-B D to detach; tmux attach -t backfill to reattach

   ./stock-data-extract backfill --type equity --interval 15min
   ./stock-data-extract backfill --type futures --interval day
   ./stock-data-extract backfill --type options --interval day
   ```
   Each command is resumable — kill and restart at any point. SQLite tracks the checkpoint per symbol.

7. **Start the scheduler** to begin nightly incremental updates:
   ```bash
   ./stock-data-extract run
   ```
   Or run it via Docker (see [Deployment](#deployment)).

---

## Quick start

### Prerequisites

- Go 1.23+
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
export SQLITE_PATH=./data/stock.db   # set to your persistent volume path in production (e.g. /mnt/data/stock.db)
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

### Run the nightly scheduler

```bash
./stock-data-extract run
```

Starts the cron scheduler. Two jobs run:
- **Incremental** (Tue–Sat 8:00 AM IST): fetches the previous trading day. Saturday's run picks up Friday's data.
- **Catch-up backfill** (Sat 10:00 AM IST): runs after the incremental and fills any gaps from binary downtime during the week.

### Run a historical backfill

```bash
# Equity daily (2004–present, ~2000 symbols — takes many hours)
./stock-data-extract backfill --type equity --interval day

# Equity 15-min (2020–present)
./stock-data-extract backfill --type equity --interval 15min

# Futures daily
./stock-data-extract backfill --type futures --interval day

# Options daily for configured underlyings
./stock-data-extract backfill --type options --interval day
```

Backfills are **resumable**. Kill and restart at any point — it reads `extraction_state` from SQLite and continues from where it left off.

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

  futures:
    exchanges: ["NFO"]
    intervals: ["day"]           # add "15min" to enable 15-min futures
    backfill_from:
      day: "2004-01-01"
      15min: "2020-01-01"

  options:
    exchanges: ["NFO"]
    underlyings: [NIFTY, BANKNIFTY, MIDCPNIFTY, FINNIFTY, RELIANCE, ...]
    intervals: ["day"]           # add "15min" to enable 15-min options
    backfill_from:
      day: "2008-01-01"
      15min: "2020-01-01"

kite:
  rate_limit_rps: 3        # Kite historical API limit
  chunk_days:
    day: 365               # max days per API call for daily interval
    15min: 60              # max days per API call for 15-min interval

cron:
  schedule: "30 2 * * 2-6"    # 8:00 AM IST = 02:30 UTC, Tue–Sat (Sat fetches Friday)
  backfill_schedule: "30 4 * * 6"  # 10:00 AM IST = 04:30 UTC, Sat catch-up

# R2 bucket and SQLite path are not in config.yaml — set via env vars:
#   R2_BUCKET=stock-data             (also read by Litestream)
#   SQLITE_PATH=/mnt/data/stock.db   (also read by Litestream and start.sh)
```

---

## Package reference

### `cmd/`

**`main.go`** — Binary entrypoint. Parses the subcommand (`run`, `backfill`, `token-refresh`), initialises all dependencies (config, SQLite, R2 client, Kite provider), and dispatches.

- `run`: starts the cron scheduler and blocks until SIGTERM/SIGINT.
- `backfill --type <equity|futures|options> --interval <day|15min>`: runs a full historical backfill for the given asset class and interval.
- `token-refresh`: runs the automated browser login and prints the new access token.

---

### `internal/config/`

**`config.go`** — Loads `config.yaml` from disk (path from `CONFIG_PATH` env or default `./config.yaml`). Populates `SQLitePath` from `SQLITE_PATH` env var (default `./data/stock.db`) and `R2Bucket` from `R2_BUCKET` env var — these are env-only so that Litestream and `start.sh` share the same single source of truth. Validates required fields. Returns a typed `Config` struct used throughout the binary.

Key types: `Config`, `ExtractionConfig`, `AssetConfig`, `OptionsConfig`, `KiteConfig`, `CronConfig`.

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

> **Note on selectors**: The CSS selectors for the Kite login form (`input[id="userid"]`, `input[id="totp"]`, etc.) target the current Kite UI. If login breaks after a Kite frontend update, inspect `https://kite.zerodha.com` in a browser and update the selectors in `login.go`.

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

**`db.go`** — Opens the database using `modernc.org/sqlite` (pure Go, no CGo). Enables WAL mode for safe concurrent reads. Runs versioned SQL migrations embedded via `//go:embed migrations/*.sql`. Migrations are idempotent — safe to run on every startup. Version tracking is in the `schema_migrations` table.

**Migrations** (`migrations/001–005.sql`):
- `001_instruments.sql` — `instruments` table: one row per instrument per snapshot date.
- `002_extraction_state.sql` — `extraction_state` table: tracks the last successfully extracted date per `(exchange, symbol, interval)` triple.
- `003_options_extraction_state.sql` — `options_extraction_state` table: same for options, keyed by `(underlying, interval)`.
- `004_schema_migrations.sql` — `schema_migrations` table definition. Note: `db.go` bootstraps this table inline before running any file-based migrations so version tracking is available from the first migration onwards; the file-based migration is therefore a safe no-op.
- `005_exchange_token.sql` — adds `exchange_token` column to `instruments` (required for the Parquet snapshot to store the value; the initial instruments table used a placeholder `0`).

**`state.go`** — CRUD for extraction state:
- `GetLastDate / SetLastDate` — for equity and futures (keyed by exchange + symbol + interval).
- `GetOptionsLastDate / SetOptionsLastDate` — for options (keyed by underlying + interval).

All dates are stored as `"YYYY-MM-DD"` strings in SQLite and converted to/from `time.Time` in Go.

**`instruments.go`** — CRUD for instrument records:
- `UpsertInstruments(date, records)` — bulk upsert for a full day's snapshot. Uses `INSERT ... ON CONFLICT DO UPDATE SET` to update in place without losing row identity.
- `LatestInstruments(exchange, instrumentType)` — fetches instruments from the most recent snapshot date. Used by extractors to get token lists.
- `SearchInstruments(query, limit)` — fuzzy search by `tradingsymbol` or `name` (LIKE pattern). For future API server use.
- `OptionExpiries(underlying)` — returns distinct sorted expiry dates for an underlying. For future API server use.

---

### `internal/extractor/`

**`backfill.go`** — Full historical backfill. Entry point is `Backfiller.Run(ctx, BackfillConfig)`. Dispatches to equity, futures, or options sub-routines based on `BackfillConfig.Type`.

Core logic for equity and futures:
1. Load instruments from SQLite (fetches from Kite API and upserts if SQLite is empty).
2. For each symbol: read `last_date` from `extraction_state` to determine resume point.
3. Split the date range into chunks of `config.kite.chunk_days` days (to stay within Kite's per-call limits).
4. Further split each chunk by year (daily) or month (15-min) so each `AppendCandles` call targets exactly one Parquet file.
5. Fetch candles, write to R2, update `extraction_state`. On ctx cancellation, stops cleanly at the next symbol boundary.

`chunkDateRange`, `splitByYear`, `splitByMonth` are the helper functions for date splitting.

**`options.go`** — Options-specific extraction. Options require a different approach because each expiry is a separate contract with its own instrument token. `OptionsExtractor.extractUnderlying`:
1. Loads all CE and PE contracts for the underlying from SQLite, filtered by `TradingSymbol` prefix (e.g., all symbols starting with `NIFTY`).
2. For each contract × each date chunk: fetches candles and accumulates `OptionCandleRecord` rows.
3. Groups records by their target Parquet key (by year for daily, by month for 15-min) and calls `AppendOptionCandles` once per key.

**`incremental.go`** — Nightly incremental run. `IncrementalExtractor.Run`:
1. Validates the access token via `GetUserProfile`. If expired and `AutoRefreshConfig` is set, calls `kite.AutoLogin` to get a fresh token.
2. Fetches today's instruments snapshot from Kite, upserts to SQLite, writes the snapshot Parquet to R2.
3. Runs equity and futures extraction for yesterday (skips weekends: Saturday→Friday, Sunday→Friday).
4. Runs options incremental for each configured underlying and interval.

`AutoRefreshConfig` carries the credentials needed for automated login. It is only populated if all three env vars (`KITE_USER_ID`, `KITE_PASSWORD`, `KITE_TOTP_SECRET`) are set.

---

### `internal/scheduler/`

**`cron.go`** — Wraps `robfig/cron/v3`. `Scheduler.Start()` registers two cron jobs from `config.yaml` and starts the scheduler. `Stop()` is called on SIGTERM for clean shutdown.

- **Incremental** (`cron.schedule`, default `"30 2 * * 2-6"` = Tue–Sat 8:00 AM IST): runs `IncrementalExtractor` to fetch the previous trading day.
- **Catch-up backfill** (`cron.backfill_schedule`, default `"30 4 * * 6"` = Sat 10:00 AM IST): runs `Backfiller` across all asset types and intervals to fill any gaps from downtime during the week.

---

## Key design decisions

**Zero CGo.** The binary is built with `CGO_ENABLED=0`. This allows simple Docker cross-compilation and avoids C toolchain dependencies. Both `modernc.org/sqlite` and `parquet-go/parquet-go` are pure Go.

**Read-modify-write for Parquet.** There is no streaming append to Parquet files. Each write downloads the existing file, merges and deduplicates in memory, and re-uploads. This keeps each file self-contained and makes the system robust to partial failures — but it means large files are expensive to update. Mitigated by using yearly partitioning for daily data and monthly partitioning for 15-min data.

**Instruments are ephemeral for derivatives.** Kite recycles instrument tokens after each expiry cycle. A futures token from last month is not valid today. The daily instruments snapshot is the authoritative token source. Tokens are never hardcoded.

**Options backfill is limited to currently-listed contracts.** Expired contracts from before Kite's lookback window (~3–5 years for 15-min, longer for daily) cannot be retrieved. The options Parquet files will only contain data from when you started running the extractor.

**Litestream for SQLite durability.** SQLite lives on a local persistent volume. Litestream streams WAL changes to R2 continuously (`sync-interval: 1m`). On a fresh deploy, `start.sh` restores the SQLite from R2 before starting the binary — so you can move between hosts without losing checkpoint state.

---

## Token expiry

Kite access tokens expire at **6:00 AM IST daily**. If the binary is running with `KITE_USER_ID` + `KITE_PASSWORD` + `KITE_TOTP_SECRET` in the environment, the incremental extractor auto-refreshes the token at startup of each run — no intervention needed.

If you are **not** using auto-refresh:
```bash
./stock-data-extract token-refresh
# prints: export KITE_ACCESS_TOKEN=<new_token>
```
Update `KITE_ACCESS_TOKEN` in your `.env` or deployment secrets, then restart the binary.

---

## Reading the data (consumer guide)

All data is stored as **Parquet files on R2**. Any tool that can read S3-compatible storage and Parquet works: DuckDB, Polars, PyArrow, Pandas, Spark.

**DuckDB example:**
```sql
-- Install the httpfs extension once: INSTALL httpfs; LOAD httpfs;
SET s3_endpoint = '<account_id>.r2.cloudflarestorage.com';
SET s3_access_key_id = '...';
SET s3_secret_access_key = '...';
SET s3_region = 'auto';

-- All RELIANCE daily candles
SELECT
    epoch_us(make_timestamptz(timestamp, 'UTC')) AS ts,
    open, high, low, close, volume
FROM read_parquet('s3://stock-data/NSE/EQ/RELIANCE/day/*.parquet')
ORDER BY ts;

-- NIFTY options for a specific expiry
SELECT *
FROM read_parquet('s3://stock-data/NFO/OPT/NIFTY/day/*.parquet')
WHERE expiry = '2024-12-26' AND strike = 25000 AND option_type = 'CE'
ORDER BY timestamp;
```

**Timestamp field:** stored as Unix microseconds UTC (`int64`). In Python: `pd.to_datetime(df['timestamp'], unit='us', utc=True)`.

**Column reference:**

| Field | Type | Description |
|---|---|---|
| `timestamp` | `int64` | Unix microseconds UTC |
| `open / high / low / close` | `float64` | Price in INR |
| `volume` | `int64` | Number of units traded |
| `oi` | `int64` | Open interest (0 for equity) |
| `expiry` | `string` | Options only: `YYYY-MM-DD` |
| `strike` | `float64` | Options only: strike price |
| `option_type` | `string` | Options only: `CE` or `PE` |

---

## Running tests

```bash
CGO_ENABLED=0 go test ./...
```

Tests cover:
- `extractor` — Pure function tests for `chunkDateRange`, `splitByYear/Month`, `lastTradingDay`, `filterByUnderlying`, `candlesToRecords`, `instrumentsToParquet`. Integration tests for `Backfiller` (stores candles, checkpoint not advanced on error, resume from checkpoint, skip when complete, fetch from provider when DB empty) and `OptionsExtractor` (underlying prefix filtering). All integration tests use an in-memory SQLite and a fake blob store — no network required.
- `scheduler` — Cron expression validation, start/stop lifecycle, double-Stop safety.
- `provider/kite` — Integration tests against an `httptest.Server`: `ValidateToken`, `Instruments`, `Historical` (success, retry logic, rate-limit exhaustion, non-rate-limit errors, interval mapping, OI/continuous params, invalid token). Also unit tests for `mapInterval` and `isRateLimitError`.
- `storage/parquet` — R2 key generation correctness for all 7 key types.
- `storage/sqlite` — Migration idempotency, extraction state round-trips, not-found behaviour.

---

## Deployment

The binary is platform-agnostic. Any Linux host (EC2, VPS, Fly.io, bare metal) works as long as it has:
- A persistent disk/volume for SQLite (set `SQLITE_PATH` to a path on it)
- Docker or a Go toolchain to build the binary
- Chrome/Chromium installed if using automated token refresh

### Docker Compose (recommended for local and single-host deployments)

```bash
# Build and start
docker compose up -d

# Run a backfill inside the running container
docker compose exec stock-data-extract /app/stock-data-extract backfill --type equity --interval day

# Tail logs
docker compose logs -f
```

`docker-compose.yml` mounts a named volume at `/data` for SQLite. Set `SQLITE_PATH=/data/stock.db` in your `.env`.

### Docker (manual)

```bash
docker build -t stock-data-extract .

docker run -d \
  --env-file .env \
  -v /mnt/data:/data \
  stock-data-extract
```

`start.sh` handles the startup sequence: restore SQLite from the R2 replica → start Litestream replication in background → start the binary.

The Litestream config path defaults to `./litestream.yml` (next to the binary). Override with `LITESTREAM_CONFIG` if needed.

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
SQLITE_PATH=/mnt/data/stock.db   # path on your persistent volume
# KITE_ACCESS_TOKEN is optional — the app auto-refreshes via browser login
```

### Running a backfill (use tmux for long runs)

```bash
docker exec -it <container> tmux new -s backfill
/app/stock-data-extract backfill --type equity --interval day
```
