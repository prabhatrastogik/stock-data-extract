#!/bin/sh
set -e

DATA_DIR="$(dirname "$SQLITE_PATH")"
mkdir -p "$DATA_DIR"

# Path to litestream config — override via LITESTREAM_CONFIG env var.
# Defaults to the path the Dockerfile copies it to; set to ./litestream.yml for local runs.
LITESTREAM_CONFIG="${LITESTREAM_CONFIG:-./litestream.yml}"

# Restore SQLite from replica if no local DB exists
if [ ! -f "$SQLITE_PATH" ]; then
    echo "Restoring SQLite from replica..."
    litestream restore -if-replica-exists -config "$LITESTREAM_CONFIG" "$SQLITE_PATH"
fi

# Start Litestream replication in background
litestream replicate -config "$LITESTREAM_CONFIG" &

# Start the application
exec /app/stock-data-extract run
