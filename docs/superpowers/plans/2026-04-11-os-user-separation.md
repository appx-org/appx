# OS User Separation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run appx and opencode as separate OS users so coding agents cannot read, modify, or delete appx's binary, database, or TLS certs.

**Architecture:** Two system users (`appx`, `opencode`) with standard Unix file permission enforcement. `appx` owns `/opt/appx/` (binary) and `/var/lib/appx/` (data). `opencode` owns `/home/opencode/` (project workspace). Both run as independent systemd services with `User=` set. Cooperative egress enforcement added via `HTTPS_PROXY` on the opencode service — hard iptables enforcement is Phase 6.

**Tech Stack:** systemd, bash (setup script), Ubuntu Linux

---

## File Map

| File | Change |
|------|--------|
| `deploy/setup.sh` | **Create** — idempotent script: create users, dirs, set permissions, install opencode binary |
| `deploy/appx.service` | **Modify** — add `User=appx`, `Group=appx`, `AmbientCapabilities`, `Environment=HOME`, standard data path |
| `deploy/opencode.service` | **Modify** — add `User=opencode`, `Group=opencode`, `HTTPS_PROXY`, `NO_PROXY`, `HOME`, fix binary path |
| `docs/architecture/arch_phase_5.md` | **Modify** — update OS user separation description from "in production" to "implemented" |

---

### Task 1: Create deploy/setup.sh

**Files:**
- Create: `deploy/setup.sh`

This script creates the two OS users and their directories. It is idempotent — safe to re-run.

Directory layout enforced:
- `/opt/appx/` — `root:root 755` — binary lives here; not writable by the `appx` process itself (protects binary from a compromised appx)
- `/var/lib/appx/` — `appx:appx 700` — DB, TLS certs, config; invisible to `opencode` user
- `/home/opencode/` — `opencode:opencode 700` — project workspace; invisible to `appx` user

- [ ] **Step 1: Write setup.sh**

```bash
#!/usr/bin/env bash
# deploy/setup.sh — create OS users and directories for appx/opencode isolation.
#
# Must be run as root. Safe to run multiple times (idempotent).
#
# After running this script:
#   1. Copy the appx binary:  install -m 755 ./appx /opt/appx/appx
#   2. Copy service files:    cp deploy/*.service /etc/systemd/system/
#   3. Reload systemd:        systemctl daemon-reload
#   4. Restart services:      systemctl restart opencode appx

set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "error: must run as root" >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# OS users
# ---------------------------------------------------------------------------

if ! id -u appx >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin --home-dir /opt/appx appx
  echo "created user: appx"
else
  echo "user appx already exists"
fi

if ! id -u opencode >/dev/null 2>&1; then
  useradd --system --create-home --shell /usr/sbin/nologin --home-dir /home/opencode opencode
  echo "created user: opencode"
else
  echo "user opencode already exists"
fi

# ---------------------------------------------------------------------------
# Directories
# ---------------------------------------------------------------------------

# /opt/appx: binary directory — root-owned so neither service user can overwrite it
install -d -m 755 /opt/appx
echo "directory ready: /opt/appx (root:root 755)"

# /var/lib/appx: appx data (SQLite DB, TLS certs, config) — only appx user can read
install -d -o appx -g appx -m 700 /var/lib/appx
echo "directory ready: /var/lib/appx (appx:appx 700)"

# /home/opencode: project workspace — only opencode user can read
install -d -o opencode -g opencode -m 700 /home/opencode
echo "directory ready: /home/opencode (opencode:opencode 700)"

# ---------------------------------------------------------------------------
# OpenCode binary
# ---------------------------------------------------------------------------

if [ ! -x /usr/local/bin/opencode ]; then
  found=0
  for candidate in \
      /root/.opencode/bin/opencode \
      /home/opencode/.opencode/bin/opencode; do
    if [ -x "$candidate" ]; then
      install -m 755 "$candidate" /usr/local/bin/opencode
      echo "installed opencode → /usr/local/bin/opencode (from $candidate)"
      found=1
      break
    fi
  done
  if [ "$found" -eq 0 ]; then
    echo "WARNING: opencode binary not found in common locations."
    echo "  Install manually: install -m 755 /path/to/opencode /usr/local/bin/opencode"
  fi
else
  echo "opencode binary already at /usr/local/bin/opencode"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

echo ""
echo "Setup complete."
echo ""
echo "Next steps:"
echo "  1. Copy appx binary:  install -m 755 ./appx /opt/appx/appx"
echo "  2. Copy service files: cp deploy/*.service /etc/systemd/system/"
echo "  3. Reload systemd:    systemctl daemon-reload"
echo "  4. Restart services:  systemctl restart opencode appx"
```

- [ ] **Step 2: Make executable**

```bash
chmod +x deploy/setup.sh
```

- [ ] **Step 3: Commit**

```bash
git add deploy/setup.sh
git commit -m "deploy: add idempotent OS user/directory setup script"
```

---

### Task 2: Update deploy/appx.service

**Files:**
- Modify: `deploy/appx.service`

Changes from current:
- Add `User=appx` and `Group=appx` — run as non-root
- Add `AmbientCapabilities=CAP_NET_BIND_SERVICE` and `CapabilityBoundingSet=CAP_NET_BIND_SERVICE` — allows binding to port 443 without root; restricts to only this capability
- Add `Environment=HOME=/var/lib/appx` — fixes "HOME not set" error in non-root systemd services
- Change `-data` path from dev-specific `/mnt/appx-dev-1/appx-data` to standard `/var/lib/appx`
- Change `ExecStart` binary path from repo path to system install path `/opt/appx/appx`

- [ ] **Step 1: Replace deploy/appx.service with**

```ini
[Unit]
Description=Appx — Agentic Application Proxy
Documentation=https://github.com/neuromaxer/appx
After=network.target

[Service]
User=appx
Group=appx

# Allows binding to port 443 without running as root.
# Remove these two lines if running on a non-privileged port (8443+).
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

Environment=HOME=/var/lib/appx

# Adjust -data if your data directory is elsewhere.
ExecStart=/opt/appx/appx -data /var/lib/appx

Restart=on-failure
RestartSec=5

# The generated password is printed to stdout on first run only.
# View it with: journalctl -u appx -n 50
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 2: Commit**

```bash
git add deploy/appx.service
git commit -m "deploy: run appx as dedicated system user with CAP_NET_BIND_SERVICE"
```

---

### Task 3: Update deploy/opencode.service

**Files:**
- Modify: `deploy/opencode.service`

Changes from current:
- Add `User=opencode` and `Group=opencode` — run as non-root
- Add `Environment=HOME=/home/opencode` — fixes "HOME not set"; opencode stores its DB and config under `$HOME`
- Add `Environment=HTTPS_PROXY=http://127.0.0.1:9080` — routes all HTTPS traffic through appx's egress CONNECT proxy for allowlist enforcement and logging
- Add `Environment=NO_PROXY=localhost,127.0.0.1` — prevents internal HTTP traffic (opencode's own API at localhost:4096) from being routed through the proxy
- Change `ExecStart` from `/root/.opencode/bin/opencode` (inaccessible to the `opencode` user) to `/usr/local/bin/opencode` (world-executable system binary installed by setup.sh)

- [ ] **Step 1: Replace deploy/opencode.service with**

```ini
[Unit]
Description=OpenCode Server — AI agent backend for Appx
Documentation=https://github.com/anomalyco/opencode
After=network.target
Before=appx.service

[Service]
User=opencode
Group=opencode

Environment=HOME=/home/opencode

# Route all HTTPS traffic through appx's egress CONNECT proxy on 127.0.0.1:9080.
# This provides allowlist enforcement and request logging via the Egress UI.
# Hard enforcement (iptables UID rules blocking direct outbound) is added by the Phase 6 installer.
Environment=HTTPS_PROXY=http://127.0.0.1:9080
# Prevent internal traffic (localhost API calls) from going through the proxy.
Environment=NO_PROXY=localhost,127.0.0.1

# Binary installed to /usr/local/bin by deploy/setup.sh.
ExecStart=/usr/local/bin/opencode serve --hostname 127.0.0.1 --port 4096

WorkingDirectory=/home/opencode

Restart=on-failure
RestartSec=5

StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 2: Commit**

```bash
git add deploy/opencode.service
git commit -m "deploy: run opencode as dedicated system user, add HTTPS_PROXY egress routing"
```

---

### Task 4: Update arch docs

**Files:**
- Modify: `docs/architecture/arch_phase_5.md`

The doc currently says "In production, `appx` and `opencode` run as separate OS users under systemd." This was aspirational. Update it to describe the implemented setup.

- [ ] **Step 1: Find the OS user separation paragraph**

It is at approximately line 65 in `docs/architecture/arch_phase_5.md`:

> **OS user separation.** In production, `appx` and `opencode` run as separate OS users under systemd. Standard Unix permissions prevent the OpenCode process (and any agents it runs) from touching appx's binary, database, or TLS certs. Cross-project interference (one agent modifying another project's files) is accepted as low-risk for single-user servers with snapshot-based rollback.

Replace with:

> **OS user separation.** `appx` and `opencode` run as separate OS users under systemd. `appx` (system user) owns `/opt/appx/` (binary) and `/var/lib/appx/` (database, TLS certs, config — mode 700). `opencode` (system user) owns `/home/opencode/` (project workspace — mode 700). Standard Unix file permissions prevent the OpenCode process (and any agent it runs) from reading or modifying appx's internals. The `deploy/setup.sh` script creates both users and their directories. `AmbientCapabilities=CAP_NET_BIND_SERVICE` lets appx bind to port 443 without root. Cross-project interference (one agent modifying another project's files) is accepted as low-risk for single-user servers with snapshot-based rollback.

- [ ] **Step 2: Commit**

```bash
git add docs/architecture/arch_phase_5.md
git commit -m "docs: update arch_phase_5 to describe OS user separation as implemented"
```

---

### Task 5: Manual verification on server

No unit tests apply (these are OS-level deployment artifacts). Verify on the target server.

- [ ] **Step 1: Run setup.sh**

```bash
sudo bash deploy/setup.sh
```

Expected output: "Setup complete." with no warnings about missing opencode binary.

- [ ] **Step 2: Copy updated service files and binary**

```bash
sudo install -m 755 ./appx /opt/appx/appx
sudo cp deploy/appx.service /etc/systemd/system/appx.service
sudo cp deploy/opencode.service /etc/systemd/system/opencode.service
sudo systemctl daemon-reload
```

- [ ] **Step 3: Restart services**

```bash
sudo systemctl restart opencode
sudo systemctl restart appx
```

Check both are running:

```bash
systemctl status opencode appx
```

Expected: both `Active: active (running)`.

- [ ] **Step 4: Verify correct users**

```bash
ps aux | grep -E 'appx|opencode'
```

Expected: appx process shows `appx` in the USER column; opencode process shows `opencode`.

- [ ] **Step 5: Verify isolation — opencode cannot read appx data**

```bash
sudo -u opencode ls /var/lib/appx/
```

Expected: `Permission denied`

- [ ] **Step 6: Verify isolation — appx cannot read opencode workspace**

```bash
sudo -u appx ls /home/opencode/
```

Expected: `Permission denied`

- [ ] **Step 7: Verify egress proxy routing**

```bash
# Should succeed — goes through egress proxy
sudo -u opencode curl -x http://127.0.0.1:9080 https://api.anthropic.com/v1/models \
  -H "Authorization: Bearer test" -s -o /dev/null -w "%{http_code}"
```

Expected: `401` (request reached Anthropic, proxy allowed it through — 401 is the expected response for a dummy key). Check the Egress log in the appx UI to confirm the request was logged.

- [ ] **Step 8: Verify appx UI works end-to-end**

Open `https://localhost` (or configured port), log in, start a project, verify agent sessions work.

---

### Task 6: Run test suite

- [ ] **Step 1: Build**

```bash
task build
```

Expected: clean build, no errors.

- [ ] **Step 2: Run tests**

```bash
task test
```

Expected: all tests pass (no Go code changed, existing tests should be green).
