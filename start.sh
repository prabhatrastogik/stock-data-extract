#!/bin/sh
set -e

: "${SQLITE_PATH:=/data/stock.db}"
DATA_DIR="$(dirname "$SQLITE_PATH")"
mkdir -p "$DATA_DIR"

# Path to litestream config — override via LITESTREAM_CONFIG env var.
# Defaults to the path the Dockerfile copies it to; set to ./litestream.yml for local runs.
LITESTREAM_CONFIG="${LITESTREAM_CONFIG:-./litestream.yml}"

# Restore latest SQLite from R2 (source of truth) before every start
echo "Restoring SQLite from replica..."
litestream restore -if-replica-exists -force -config "$LITESTREAM_CONFIG" "$SQLITE_PATH"

# Run litestream as PID 1 with the binary as a managed subprocess.
# This ensures litestream receives SIGTERM from Docker and can flush WAL segments
# to R2 before the container exits — avoiding snapshot corruption on shutdown.
exec litestream replicate -config "$LITESTREAM_CONFIG" -exec "/app/stock-data-extract run"
