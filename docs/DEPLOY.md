# Deployment — Termux on Android (no root)

Target device: Samsung Galaxy Z Fold 3, 12 GB RAM. Assumptions already in
place: the phantom process killer is disabled and Termux is excluded from
battery optimization. Remote admin happens over Tailscale.

## 1. Packages

```sh
pkg update
pkg install golang git postgresql openssh
```

## 2. Postgres (local, long-running)

```sh
initdb -D $PREFIX/var/lib/postgresql
```

Edit `$PREFIX/var/lib/postgresql/postgresql.conf` — settings that matter for
a long-running instance on a phone:

```conf
# Local-only. Remote psql goes through an SSH tunnel over Tailscale.
listen_addresses = '127.0.0.1'
max_connections = 25                 # harness pool (8) + whatsmeow + psql headroom

# 12 GB RAM device, but leave room for Android + WhatsApp + the harness.
shared_buffers = 256MB
work_mem = 16MB
maintenance_work_mem = 64MB

# Auditability over speed: keep synchronous_commit/fsync at their safe
# defaults (on). Phones lose power; never trade durability here.

# Fewer, larger checkpoints -> less background flash writes.
checkpoint_timeout = 15min
max_wal_size = 1GB

# Log to file so start.sh's rotation can manage it.
logging_collector = off              # start.sh passes -l for the log file
```

Create the database and user:

```sh
pg_ctl -D $PREFIX/var/lib/postgresql -l ~/harness/logs/postgres.log start
createdb harness
# Termux postgres trusts the local user by default; the DSN is simply:
#   postgres://localhost:5432/harness
```

Schema migrations run automatically every time the harness starts — no
manual step. WhatsMeow creates its own `whatsmeow_*` tables in the same
database on first run.

## 3. Build and install the harness

```sh
mkdir -p ~/harness/logs
git clone https://github.com/filipekdick/go-harness-whatsmeow ~/harness/repo
cd ~/harness/repo
go build -o $PREFIX/bin/harness ./cmd/harness
```

(Building on-device with Termux's Go toolchain is the simplest path and
avoids Android cross-compilation concerns. First build takes a few minutes;
later builds are incremental.)

## 4. Configuration

```sh
mkdir -p ~/.config/harness
cat > ~/.config/harness/env <<'EOF'
DATABASE_URL=postgres://localhost:5432/harness
ANTHROPIC_API_KEY=sk-ant-...
# Optional overrides (defaults shown):
# LLM_MODEL=claude-opus-4-8
# LLM_MAX_TOKENS=8192
# MAX_TOOL_ITERATIONS=6
# HISTORY_TAIL_LIMIT=30
# SUMMARIZE_AFTER=50
# PENDING_WRITE_TTL_MINUTES=10
# WORKER_COUNT=8
# WORKER_QUEUE_SIZE=256
EOF
chmod 600 ~/.config/harness/env
```

## 5. Tenant setup (per company)

```sh
set -a; . ~/.config/harness/env; set +a

harness add-company "Loja Exemplo"
#  -> prints the company id, e.g. 1

# System prompt (tone, language, business context) via psql:
psql harness -c "UPDATE companies SET system_prompt = 'Você é o atendente da Loja Exemplo...' WHERE id = 1;"

# Link the two WhatsApp numbers (scan the QR with each line's phone,
# WhatsApp > Linked devices):
harness pair 1 CUSTOMER
harness pair 1 EMPLOYEE

# Register employees — this is the ONLY way a number gains the EMPLOYEE
# role; inbound messages can never create one:
harness add-employee 1 5511999998888 "Maria"

# Business info the bot can quote:
psql harness -c "INSERT INTO business_rules (company_id, key, value) VALUES
  (1, 'hours',   'Seg-Sex 9h-18h, Sáb 9h-13h'),
  (1, 'address', 'Rua Exemplo 123, São Paulo');"
# (or let an employee do it over WhatsApp with set_business_rule)
```

Repeat for each additional company; the single process hosts all of them.

## 6. Run at boot (Termux:Boot + wakelock)

1. Install the **Termux:Boot** app (F-Droid, same signing key as your
   Termux) and open it once.
2. ```sh
   mkdir -p ~/.termux/boot
   cp ~/harness/repo/scripts/termux-boot.sh ~/.termux/boot/99-harness.sh
   chmod +x ~/.termux/boot/99-harness.sh ~/harness/repo/scripts/start.sh
   ```
3. Reboot to verify, or start it now in a Termux session:
   ```sh
   ~/harness/repo/scripts/start.sh &
   tail -f ~/harness/logs/harness.log
   ```

`start.sh` holds a wakelock (persistent Termux notification = working as
intended), starts Postgres if needed, and restarts the harness on crash
with exponential backoff. Logs rotate at 20 MB.

## 7. Remote admin over Tailscale

Install the Tailscale Android app and join your tailnet, then in Termux:

```sh
sshd            # listens on port 8022; add it to ~/.termux/boot if wanted
```

From your admin machine:

```sh
ssh -p 8022 <tailscale-ip>                       # shell / logs / psql
ssh -p 8022 -L 5432:127.0.0.1:5432 <tailscale-ip> # tunnel for GUI DB tools
```

Postgres itself never listens beyond 127.0.0.1 — the tunnel is the only
remote path to it.

## 8. Operations

| Task | How |
|---|---|
| Watch logs | `tail -f ~/harness/logs/harness.log` |
| Update the code | `cd ~/harness/repo && git pull && go build -o $PREFIX/bin/harness ./cmd/harness && pkill -f 'harness run'` (supervisor restarts it) |
| Stop everything | `pkill -f start.sh && pkill -f 'harness run'` |
| Re-pair a line | `psql harness -c "UPDATE wa_channels SET wa_device_jid = NULL WHERE company_id=1 AND channel='CUSTOMER'"` then `harness pair 1 CUSTOMER` |
| Review escalations | `psql harness -c "SELECT id, company_id, summary, created_at FROM escalations WHERE status='OPEN'"` |
| Audit trail | `psql harness -c "SELECT created_at, actor_phone, action, entity_type, entity_id FROM audit_log WHERE company_id=1 ORDER BY id DESC LIMIT 20"` |
| Nightly backup | `pkg install cronie; crontab -e` → `15 3 * * * pg_dump harness \| gzip > ~/harness/backups/harness-$(date +\%F).sql.gz` (and start `crond` from the boot script). Copy backups off-device over Tailscale. |

## 9. Known constraints

- **One process, all companies.** WhatsApp Web multi-device allows the
  harness to stay linked while each line's phone stays online as the
  primary device. If a line's phone is off for ~2 weeks, WhatsApp unlinks
  the device and that line needs re-pairing.
- **Android reboots**: Termux:Boot only fires after the device is unlocked
  once. With an unattended device, disable secure startup or accept that a
  reboot needs one manual unlock.
- **Time**: Postgres and the pending-write TTL rely on device time; keep
  automatic time sync on.
- **Message loss window**: if the process is down, WhatsApp queues messages
  and delivers them on reconnect; dedup makes redelivery safe.
