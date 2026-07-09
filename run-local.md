# run-local.sh — Local Development Guide

`run-local.sh` is a convenience wrapper for running `stock-data-extract` locally. It handles building the binary, loading environment variables from `.env`, syncing SQLite with R2 via Litestream, and clean shutdown.

---

## Prerequisites

| Requirement | Notes |
|---|---|
| Go 1.26+ | `go build` is run on every invocation |
| `litestream` | Not in Homebrew — see install instructions below. If absent, SQLite is NOT backed up to R2 (a warning is printed) |
| Chrome or Chromium | Required only for `token-refresh`; probes `/Applications/Google Chrome.app` and `/Applications/Chromium.app` automatically |
| `.env` file | Copy from `.env.example` and fill in values |

---

## Usage

```sh
./run-local.sh [command] [flags]
```

If `command` is omitted it defaults to `run`.

---

### Installing Litestream (macOS)

Litestream is not available via Homebrew. Install from the GitHub release:

```sh
# Apple Silicon (arm64) — most Macs since 2020
curl -L https://github.com/benbjohnson/litestream/releases/download/v0.3.13/litestream-v0.3.13-darwin-arm64.zip \
  -o /tmp/litestream.zip
unzip /tmp/litestream.zip -d /tmp/
sudo mv /tmp/litestream /usr/local/bin/

# Intel (amd64)
curl -L https://github.com/benbjohnson/litestream/releases/download/v0.3.13/litestream-v0.3.13-darwin-amd64.zip \
  -o /tmp/litestream.zip
unzip /tmp/litestream.zip -d /tmp/
sudo mv /tmp/litestream /usr/local/bin/
```

Verify: `litestream version`

---

## Commands

### `run` (default)

Starts the cron scheduler. Blocks until you press Ctrl-C.

```sh
./run-local.sh
./run-local.sh run
```

What happens:
1. Builds the binary
2. Restores SQLite from R2 (latest backup)
3. Starts Litestream replication in the background
4. Runs the scheduler
5. On Ctrl-C: kills Litestream cleanly before exiting

Requires `cron.schedule` to be set in `config.yaml`. Example schedules:

| Schedule | Meaning |
|---|---|
| `"30 2 * * 2-6"` | 8:00 AM IST, Tue–Sat (default) |
| `"0 9 * * 1-5"` | 9:00 AM local time, Mon–Fri |
| `"* * * * *"` | Every minute (for testing) |

---

### `incremental`

Runs a single incremental pass immediately — same logic as the cron job. Fetches today's instruments snapshot, then extracts yesterday's candles for all non-disabled asset types. Exits when complete.

```sh
./run-local.sh incremental
```

Use this to trigger a sync on demand without waiting for the cron schedule, or to test that everything is wired up correctly.

---

### `backfill`

Runs a one-off historical backfill for a given asset type and interval. Exits when complete.

```sh
./run-local.sh backfill --type <type> --interval <interval>
```

**`--type`** (required)

| Value | Description |
|---|---|
| `equity` | NSE equity stocks — F&O universe + configured indices (~213 symbols with default config) |
| `futures` | All active NFO futures contracts |
| `options` | Index and stock options for configured underlyings |

> The `disabled` flag in `config.yaml` has **no effect** on manual `backfill` commands. It only controls the scheduled incremental run and the Saturday catch-up backfill.

**`--interval`** (optional, default: `day`)

| Value | Description | Chunk size | Partitioned by |
|---|---|---|---|
| `day` | Daily OHLCV candles | 365 days per API call | Year |
| `15min` | 15-minute OHLCV candles | 60 days per API call | Month |

Start dates are read from `config.yaml` under `extraction.<type>.backfill_from.<interval>`.

**Equity symbol filter** — controlled by fields in `config.yaml` under `extraction.equity`:

| Field | Default | Effect |
|---|---|---|
| `fno_only: true` | `false` | Restrict extraction to NSE stocks with active F&O contracts (~211 symbols instead of ~2000). Cross-references NSE EQ `tradingsymbol` against NFO FUT `name` field using the stored instruments snapshot. |
| `include_indices: true` | `false` | Also extract NSE index instruments. Kite stores these with `instrument_type="EQ"` and `segment="INDICES"` — not `instrument_type="INDICES"`. |
| `indices: ["NIFTY 50", "NIFTY BANK"]` | `[]` (all) | When `include_indices: true`, restrict to this specific list of indices. Accepts both `"NIFTY 50"` and `"NIFTY-50"` forms. Leave empty or omit to fetch all index instruments. |
| `disabled: true` | `false` | Skip this asset type in the scheduled incremental run and Saturday catch-up backfill. Manual `backfill` commands are unaffected. |

**Examples:**

```sh
# Equity daily from 2004 (longest — takes many hours for ~2000 symbols)
./run-local.sh backfill --type equity --interval day

# Equity 15-min from 2020
./run-local.sh backfill --type equity --interval 15min

# Futures daily
./run-local.sh backfill --type futures --interval day

# Options daily for configured underlyings
./run-local.sh backfill --type options --interval day
```

Backfill is **resumable** — kill it at any time and rerun the same command. SQLite tracks the checkpoint per symbol.

---

### `token-refresh`

Performs an automated Kite browser login and prints a fresh access token. Does **not** start Litestream or restore SQLite.

```sh
./run-local.sh token-refresh
```

Requires `KITE_USER_ID`, `KITE_PASSWORD`, and `KITE_TOTP_SECRET` to be set in `.env`.

On success, prints:
```
export KITE_ACCESS_TOKEN=<token>
```

Add that line to your `.env` (or just set the three auto-refresh env vars — the scheduler will refresh the token automatically on each run).

**Debug mode** — opens a visible Chrome window so you can watch the login flow:

```sh
KITE_LOGIN_DEBUG=1 ./run-local.sh token-refresh
```

On failure, a screenshot of the browser state is saved to `/tmp/kite-login-debug.png`.

---

## Environment variables

All variables are loaded from `.env` in the current directory. The table below covers every variable the script and binary read.

### Kite Connect (required)

| Variable | Example | Description |
|---|---|---|
| `KITE_API_KEY` | `abc123xyz` | API key from [developers.kite.trade](https://developers.kite.trade) |
| `KITE_API_SECRET` | `secret123` | API secret from the same dashboard |

### Kite auto-refresh (recommended)

If all three are set, the binary automatically refreshes the access token at startup of each incremental run **and** each backfill. `KITE_ACCESS_TOKEN` becomes optional.

> **Do not run multiple `./run-local.sh backfill` terminals in parallel.** Each invocation starts its own Litestream replication process writing to the same R2 path — multiple concurrent writers corrupt the backup. Run equity, futures, and options backfills sequentially.

| Variable | Example | Description |
|---|---|---|
| `KITE_USER_ID` | `AB1234` | Your Zerodha user ID |
| `KITE_PASSWORD` | `yourpassword` | Your Zerodha login password |
| `KITE_TOTP_SECRET` | `BASE32SECRET` | Base32 TOTP secret from your 2FA authenticator setup page (not the 6-digit code — the secret itself) |

### Kite access token (optional)

| Variable | Example | Description |
|---|---|---|
| `KITE_ACCESS_TOKEN` | `abcdef...` | Valid access token. Required only if auto-refresh credentials above are not set. Expires at 6:00 AM IST daily. |

### Cloudflare R2 (required)

| Variable | Example | Description |
|---|---|---|
| `R2_ACCOUNT_ID` | `abc123...` | Cloudflare account ID |
| `R2_ACCESS_KEY_ID` | `key...` | R2 API token access key |
| `R2_SECRET_ACCESS_KEY` | `secret...` | R2 API token secret |
| `R2_BUCKET` | `stock-data` | R2 bucket name — also read directly by Litestream |

### Storage paths

| Variable | Default | Description |
|---|---|---|
| `SQLITE_PATH` | `./data/stock.db` | Local SQLite path. For local runs use a relative path; for Docker use `/data/stock.db`. |
| `LITESTREAM_BACKUP_PATH` | `sqlite-backup/stock.db` | Path within the R2 bucket where Litestream stores the SQLite backup. Prefix with an environment name if multiple deployments share the same bucket (e.g. `prod/sqlite-backup/stock.db`). |

### Other

| Variable | Example | Description |
|---|---|---|
| `CONFIG_PATH` | `./config.yaml` | Path to `config.yaml`. Defaults to `config.yaml` in the current directory. |
| `KITE_LOGIN_DEBUG` | `1` | Set to `1` to open a visible Chrome window during `token-refresh`. |

---

## Startup sequence

For `run`, `incremental`, and `backfill`:

```
[build]       go build ./cmd/
[litestream]  restore from R2 → local SQLITE_PATH
[litestream]  replicate (background process)
[run]         ./stock-data-extract <command>
              (on exit) litestream killed cleanly
```

For `token-refresh`:

```
[build]       go build ./cmd/
[run]         ./stock-data-extract token-refresh
              (no Litestream, no config.yaml required)
```

---

## Litestream behaviour

- **Restore**: runs on every invocation, unconditionally. R2 is the source of truth — even if a local SQLite file exists, it is replaced with the latest R2 backup. This ensures you always start from the canonical state regardless of which machine last ran.
- **Replication**: syncs WAL changes to R2 every 60 seconds (`sync-interval: 1m` in `litestream.yml`). Up to 60 seconds of work can be lost if the process crashes without a clean shutdown.
- **Cleanup**: `trap cleanup EXIT INT TERM` ensures Litestream is killed when the script exits for any reason (Ctrl-C, `kill`, normal exit).
- **Missing litestream**: if `litestream` is not in PATH, the script prints a warning and runs the binary directly with no R2 sync.
