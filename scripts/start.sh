#!/data/data/com.termux/files/usr/bin/bash
# Supervising launcher for the harness inside Termux.
#
# - grabs a wakelock so Android doesn't doze the process
# - starts local Postgres if it isn't running
# - runs the harness in a restart loop with backoff
# - keeps logs bounded with a simple size-based rotation
#
# Run it directly for foreground testing, or let scripts/termux-boot.sh
# launch it at boot. Configuration lives in ~/.config/harness/env.

set -u

APP_DIR="$HOME/harness"
LOG_DIR="$APP_DIR/logs"
ENV_FILE="$HOME/.config/harness/env"
PGDATA="$PREFIX/var/lib/postgresql"
HARNESS_BIN="${HARNESS_BIN:-$PREFIX/bin/harness}"
MAX_LOG_BYTES=$((20 * 1024 * 1024)) # rotate at 20 MB, keep one .old

mkdir -p "$LOG_DIR"
LOG="$LOG_DIR/harness.log"
PGLOG="$LOG_DIR/postgres.log"

log() { echo "[start.sh $(date '+%F %T')] $*" >>"$LOG"; }

rotate() {
    local f="$1"
    if [ -f "$f" ] && [ "$(stat -c%s "$f")" -gt "$MAX_LOG_BYTES" ]; then
        mv "$f" "$f.old"
    fi
}

# Keep the CPU available while we run. Termux shows a persistent
# notification for this; that is expected and wanted.
termux-wake-lock
trap 'termux-wake-unlock' EXIT

# Secrets and settings (DATABASE_URL, ANTHROPIC_API_KEY, ...).
if [ ! -f "$ENV_FILE" ]; then
    echo "missing $ENV_FILE — see docs/DEPLOY.md" >&2
    log "missing $ENV_FILE; aborting"
    exit 1
fi
set -a
. "$ENV_FILE"
set +a

# Bring up Postgres if needed. pg_ctl -w waits until it accepts connections.
if ! pg_isready -q 2>/dev/null; then
    log "starting postgres"
    pg_ctl -D "$PGDATA" -l "$PGLOG" -w start >>"$LOG" 2>&1 || {
        log "postgres failed to start; see $PGLOG"
        exit 1
    }
fi

log "supervisor started (harness: $HARNESS_BIN)"
backoff=2
while true; do
    rotate "$LOG"
    rotate "$PGLOG"
    started=$(date +%s)
    log "launching harness"
    "$HARNESS_BIN" run >>"$LOG" 2>&1
    code=$?
    ran=$(( $(date +%s) - started ))
    log "harness exited with code $code after ${ran}s"

    # A crash right after start usually means bad config or an unreachable
    # database — back off exponentially so we don't spin. A crash after a
    # healthy run restarts quickly.
    if [ "$ran" -ge 60 ]; then
        backoff=2
    else
        backoff=$((backoff * 2))
        [ "$backoff" -gt 300 ] && backoff=300
    fi
    log "restarting in ${backoff}s"
    sleep "$backoff"
done
