# VPS Deployment Guide

Deploy `stock-data-extract` on any Linux VPS (Ubuntu/Debian shown) without Docker. The binary is statically compiled with zero system dependencies — only Litestream and Chromium need to be installed on the host.

---

## Overview

```
/opt/stock-data-extract/
├── stock-data-extract   ← compiled binary
├── config.yaml
├── litestream.yml
├── start.sh
└── .env                 ← secrets (chmod 600)

/data/
└── stock.db             ← SQLite (replicated to R2 by Litestream)
```

A dedicated `stock` user runs the service. systemd manages the process, auto-restarts on crash, and starts it on boot.

---

## Step 1 — Build the Linux binary on your Mac

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o ./build/stock-data-extract ./cmd/
```

For ARM VPS (Hetzner CAX, Oracle Ampere, etc.):
```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o ./build/stock-data-extract ./cmd/
```

---

## Step 2 — Create the app user and directories on the VPS

```bash
# Run as root on the VPS
useradd --system --shell /usr/sbin/nologin --home /opt/stock-data-extract stock

mkdir -p /opt/stock-data-extract /data
chown stock:stock /opt/stock-data-extract /data
chmod 750 /opt/stock-data-extract /data
```

---

## Step 3 — Install Litestream on the VPS

```bash
# amd64
wget -q https://github.com/benbjohnson/litestream/releases/download/v0.3.13/litestream-v0.3.13-linux-amd64.tar.gz \
  && tar -xzf litestream-v0.3.13-linux-amd64.tar.gz -C /usr/local/bin/ \
  && rm litestream-v0.3.13-linux-amd64.tar.gz

# arm64
wget -q https://github.com/benbjohnson/litestream/releases/download/v0.3.13/litestream-v0.3.13-linux-arm64.tar.gz \
  && tar -xzf litestream-v0.3.13-linux-arm64.tar.gz -C /usr/local/bin/ \
  && rm litestream-v0.3.13-linux-arm64.tar.gz

litestream version   # verify
```

---

## Step 4 — Install Chrome on the VPS

Chrome is required for automated Kite token refresh.

> **Do NOT use `apt install chromium-browser` on Ubuntu 20.04+.** That package is a Snap stub. Snap's `snap-confine` requires the `cap_dac_override` kernel capability which is unavailable in most LXC/VPS containers — Chrome will fail to start with `snap-confine is packaged without necessary permissions`.

### amd64 — Google Chrome from Google's .deb

```bash
wget -q https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb
apt install -y ./google-chrome-stable_current_amd64.deb
rm google-chrome-stable_current_amd64.deb

# Verify
google-chrome-stable --version
```

### arm64 (Hetzner CAX, Oracle Ampere, etc.)

Google Chrome has no arm64 Linux build. Use Chromium from the Debian repository (a proper .deb, not Snap), or add the xtradeb PPA on Ubuntu:

```bash
# Debian (bullseye / bookworm) — native .deb
apt install -y chromium

# Ubuntu arm64 — non-Snap Chromium via xtradeb PPA
add-apt-repository ppa:xtradeb/apps
apt update && apt install -y chromium

# Verify
chromium --version
```

---

## Step 5 — Copy files to the VPS

```bash
# From your local machine
scp stock-data-extract config.yaml litestream.yml start.sh \
    user@your-vps:/opt/stock-data-extract/

# On the VPS
chmod +x /opt/stock-data-extract/start.sh
chown -R stock:stock /opt/stock-data-extract/
```

---

## Step 6 — Create the `.env` file on the VPS

```bash
cat > /opt/stock-data-extract/.env << 'EOF'
KITE_API_KEY=your_api_key
KITE_API_SECRET=your_api_secret
KITE_USER_ID=your_zerodha_user_id
KITE_PASSWORD=your_zerodha_password
KITE_TOTP_SECRET=your_totp_base32_secret

R2_ACCOUNT_ID=your_cloudflare_account_id
R2_ACCESS_KEY_ID=your_r2_access_key_id
R2_SECRET_ACCESS_KEY=your_r2_secret_access_key
R2_BUCKET=stock-data

SQLITE_PATH=/data/stock.db
LITESTREAM_BACKUP_PATH=sqlite-backup/stock.db
CONFIG_PATH=/opt/stock-data-extract/config.yaml
LITESTREAM_CONFIG=/opt/stock-data-extract/litestream.yml
EOF

# Restrict access — this file contains secrets
chmod 600 /opt/stock-data-extract/.env
chown stock:stock /opt/stock-data-extract/.env
```

---

## Step 7 — Create the systemd unit file

```bash
cat > /etc/systemd/system/stock-data-extract.service << 'EOF'
[Unit]
Description=stock-data-extract — Zerodha historical data pipeline
Documentation=https://github.com/prabhatrastogik/stock-data-extract
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=stock
Group=stock
WorkingDirectory=/opt/stock-data-extract

# Load all secrets from .env
EnvironmentFile=/opt/stock-data-extract/.env

# start.sh restores SQLite from R2, then runs:
#   litestream replicate -exec "/app/stock-data-extract run"
# Litestream becomes the main process (PID tracked by systemd).
# On stop, systemd sends SIGTERM to litestream, which gracefully
# stops the binary and flushes WAL segments to R2 before exiting.
ExecStart=/opt/stock-data-extract/start.sh

# Give a running incremental job (up to ~2 min at 3 RPS for 213 symbols)
# plus Litestream flush time before force-killing.
TimeoutStopSec=300

# Restart automatically on crash, but not on clean exit (e.g. token error
# that requires manual intervention).
Restart=on-failure
RestartSec=30

# Prevent credential leakage in core dumps
PrivateTmp=true
# NOTE: if Chrome is installed as a Snap package, remove NoNewPrivileges=true —
# snap-confine needs setuid to acquire cap_dac_override and this flag blocks it.
# The proper fix is to install Chrome as a non-Snap .deb (see Step 4).
NoNewPrivileges=true

# Log to journald (view with: journalctl -u stock-data-extract -f)
StandardOutput=journal
StandardError=journal
SyslogIdentifier=stock-data-extract

[Install]
WantedBy=multi-user.target
EOF
```

---

## Step 8 — Enable and start the service

```bash
systemctl daemon-reload
systemctl enable stock-data-extract    # start on boot
systemctl start stock-data-extract

# Check it came up cleanly
systemctl status stock-data-extract
journalctl -u stock-data-extract -f    # follow logs
```

Expected startup log:
```
Restoring SQLite from replica...
[incremental] ...
```

---

## Step 9 — Run the initial backfill

The service runs the **scheduler** (`run` command). For the initial backfill, run the binary directly as the `stock` user in a `tmux` session so it survives SSH disconnects:

```bash
# Stop the scheduler first to avoid two processes writing SQLite simultaneously
systemctl stop stock-data-extract

# Switch to the stock user and start tmux
su -s /bin/bash stock
cd /opt/stock-data-extract
tmux new -s backfill

# Source env vars (start.sh does this automatically, but we're running manually)
set -a && source .env && set +a

# Restore SQLite from R2 before starting (start.sh normally does this)
rm -f "$SQLITE_PATH" "${SQLITE_PATH}-shm" "${SQLITE_PATH}-wal"
litestream restore -if-replica-exists -config litestream.yml "$SQLITE_PATH"

# Start litestream replication in the background
litestream replicate -config litestream.yml &
LITESTREAM_PID=$!
trap "kill $LITESTREAM_PID; wait $LITESTREAM_PID" EXIT INT TERM

# Run backfills sequentially (do NOT run in parallel — see README)
./stock-data-extract backfill --type equity  --interval day
./stock-data-extract backfill --type equity  --interval 15min
./stock-data-extract backfill --type futures --interval day
./stock-data-extract backfill --type options --interval day

# Ctrl-B D to detach from tmux; reattach with: tmux attach -t backfill
```

Once all backfills are complete, restart the scheduler:
```bash
systemctl start stock-data-extract
```

---

## Managing the service

```bash
# Status
systemctl status stock-data-extract

# Logs (last 100 lines)
journalctl -u stock-data-extract -n 100

# Follow live logs
journalctl -u stock-data-extract -f

# Restart (e.g. after config change)
systemctl restart stock-data-extract

# Stop cleanly (waits up to 5 minutes for running job to finish)
systemctl stop stock-data-extract
```

### Run an incremental sync manually

To trigger a single incremental pass on demand (without waiting for the cron schedule):

```bash
su -s /bin/bash stock -c \
  "set -a && source /opt/stock-data-extract/.env && set +a && \
   /opt/stock-data-extract/stock-data-extract incremental"
```

This runs the same logic as the cron job: fetches today's instruments snapshot, then extracts yesterday's candles for all non-disabled asset types.

---

## Updating the binary

```bash
# On your local machine — build for the VPS architecture
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o stock-data-extract ./cmd/

# Copy new binary and any changed config
scp stock-data-extract config.yaml user@your-vps:/opt/stock-data-extract/

# On the VPS — restart the service
systemctl restart stock-data-extract
```

The restart triggers a fresh SQLite restore from R2, so the service always starts from the canonical state regardless of what happened in the previous run.

---

## Manual token refresh

If auto-refresh stops working (Kite UI change, TOTP secret rotated, etc.):

```bash
su -s /bin/bash stock -c \
  "set -a && source /opt/stock-data-extract/.env && set +a && \
   /opt/stock-data-extract/stock-data-extract token-refresh"
```

Copy the printed `KITE_ACCESS_TOKEN=...` into `/opt/stock-data-extract/.env`, then:
```bash
systemctl restart stock-data-extract
```

---

## Firewall

The service makes **outbound** connections only (Kite API, Cloudflare R2). No inbound ports need to be opened.

```bash
# UFW example — outbound is allowed by default
ufw allow out 443/tcp   # HTTPS to Kite and R2 (likely already allowed)
```
