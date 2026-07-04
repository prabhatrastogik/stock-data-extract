#!/usr/bin/env bash
set -euo pipefail

# Load .env if present
if [ -f .env ]; then
  set -a
  # shellcheck source=.env
  source .env
  set +a
fi

: "${SQLITE_PATH:=./data/stock.db}"
: "${LITESTREAM_BACKUP_PATH:=sqlite-backup/stock.db}"

CMD="${1:-run}"

# token-refresh needs no DB or R2 — run directly
if [ "$CMD" = "token-refresh" ]; then
  go build -o stock-data-extract ./cmd/
  exec ./stock-data-extract token-refresh
fi

# Build
echo "[build] compiling..."
go build -o stock-data-extract ./cmd/

# Create data directory
mkdir -p "$(dirname "$SQLITE_PATH")"

if ! command -v litestream &>/dev/null; then
  echo "[warn] litestream not found — SQLite will NOT be backed up to R2"
  echo "       Install (Apple Silicon): curl -L https://github.com/benbjohnson/litestream/releases/download/v0.3.13/litestream-v0.3.13-darwin-arm64.zip -o /tmp/ls.zip && unzip /tmp/ls.zip -d /tmp/ && sudo mv /tmp/litestream /usr/local/bin/"
  echo "       Install (Intel Mac):     curl -L https://github.com/benbjohnson/litestream/releases/download/v0.3.13/litestream-v0.3.13-darwin-amd64.zip -o /tmp/ls.zip && unzip /tmp/ls.zip -d /tmp/ && sudo mv /tmp/litestream /usr/local/bin/"
  echo "       See run-local.md for full instructions."
  exec ./stock-data-extract "$@"
fi

# Restore latest DB from R2 (source of truth) before every run
echo "[litestream] restoring from R2 (if backup exists)..."
litestream restore -if-replica-exists -force -config litestream.yml "$SQLITE_PATH"

# Start litestream replication in background
echo "[litestream] starting continuous replication to R2..."
litestream replicate -config litestream.yml &
LITESTREAM_PID=$!

# Kill litestream when the main process exits (any reason)
cleanup() {
  echo "[litestream] shutting down replication..."
  kill "$LITESTREAM_PID" 2>/dev/null || true
  wait "$LITESTREAM_PID" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

echo "[run] ./stock-data-extract $*"
./stock-data-extract "$@"
