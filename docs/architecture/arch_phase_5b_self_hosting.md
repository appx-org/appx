# Phase 5b Architecture Reference: Self-Hosting and Deployment

**Branch:** de-docker-refactor  
**Date:** 2026-04-14  
**Status:** Complete

---

## Table of Contents

1. [Overview](#1-overview)
2. [System Map](#2-system-map)
   - [File Inventory](#file-inventory)
   - [Directory Layout on the Server](#directory-layout-on-the-server)
   - [OS Users and Groups](#os-users-and-groups)
   - [Systemd Services](#systemd-services)
3. [Bootstrap: Day-1 Setup](#3-bootstrap-day-1-setup)
   - [Configuration Prompt](#configuration-prompt)
   - [Pipeline](#pipeline)
4. [System Setup](#4-system-setup)
   - [User and Group Creation](#user-and-group-creation)
   - [Directory Hierarchy](#directory-hierarchy)
   - [OpenCode Model Config](#opencode-model-config)
   - [Service File Installation](#service-file-installation)
5. [Tools Installation](#5-tools-installation)
   - [Go](#go)
   - [Node.js](#nodejs)
   - [OpenCode](#opencode)
   - [Claude Code](#claude-code)
   - [uv](#uv)
   - [System-Wide PATH Strategy](#system-wide-path-strategy)
6. [Deploy: Day-N Updates](#6-deploy-day-n-updates)
7. [Verification Suite](#7-verification-suite)
8. [Security Model](#8-security-model)
   - [Permission Matrix](#permission-matrix)
   - [Isolation Boundaries](#isolation-boundaries)
   - [Binary Protection](#binary-protection)
   - [Egress Proxy Integration](#egress-proxy-integration)
9. [Configuration Reference](#9-configuration-reference)
10. [Pitfalls and Design Decisions](#10-pitfalls-and-design-decisions)

---

## 1. Overview

### The Problem

Appx requires a controlled server environment: two isolated OS users, a specific directory hierarchy with precise permissions, several tools installed system-wide, two systemd services, and a configuration file. Setting this up manually is error-prone and undocumented.

### The Design

A set of composable shell scripts in `deploy/` automate the entire server lifecycle:

```
Day 1 (fresh server):
  bootstrap.sh
    ├── interactive config prompt → /etc/appx/appx.env
    ├── system-setup.sh  (users, dirs, service files)
    ├── tools-install.sh (Go, Node, OpenCode, Claude, uv)
    ├── task build       (frontend + Go binary)
    ├── install binary   → /usr/local/bin/appx
    ├── restart services (opencode, appx)
    └── verify-installation.sh (40+ assertions)

Day N (code update):
  task server:deploy
    ├── git pull
    ├── task build
    ├── install binary
    ├── tools-install.sh (upgrade if versions changed)
    ├── system-setup.sh  (sync users/dirs/service files)
    ├── restart services
    └── verify-installation.sh
```

All scripts are idempotent. Running them on an already-configured server is a no-op for anything already at the desired state.

### Key Decisions

**Composable scripts, not a single monolith.** `system-setup.sh` and `tools-install.sh` can be run independently. Bootstrap orchestrates them for day-1; `task server:deploy` orchestrates them for day-N. This avoids the "ran the full installer to change one thing" problem.

**Version pinning via files.** OpenCode's version is pinned in `deploy/opencode-version` (a plain text file). Go's version is read from `go.mod`. Node is pinned to a major version (24) in the script. This keeps version decisions visible in git history and reviewable in PRs.

**Interactive config only on first run.** `bootstrap.sh` prompts for IP, data directory, and port once, writes `/etc/appx/appx.env`, and never prompts again. All subsequent runs (deploy, system-setup, tools-install) read from the env file silently.

**Everything in /usr/local/bin.** Regardless of where each tool's installer puts its binary (nvm puts node in a deeply nested path, OpenCode goes to `~/.opencode/bin/`, uv to `~/.local/bin/`), every script symlinks or copies the result to `/usr/local/bin/`. This ensures both OS users and root can run all tools without sourcing profile scripts.

---

## 2. System Map

### File Inventory

| File | Purpose | When to run |
|------|---------|-------------|
| `deploy/bootstrap.sh` | Full day-1 setup: config, users, tools, build, start, verify | First server setup |
| `deploy/system-setup.sh` | Create/update OS users, groups, directories, service files | After infra changes |
| `deploy/tools-install.sh` | Install/update Go, Node, OpenCode, Claude Code, uv | After tool version changes |
| `deploy/verify-installation.sh` | 40+ assertions on users, dirs, perms, isolation, tools, runtime | After any change |
| `deploy/appx.service` | systemd unit for the appx server | Installed by system-setup |
| `deploy/opencode.service` | systemd unit for OpenCode server | Installed by system-setup |
| `deploy/opencode.json` | Default OpenCode config (pins model to Anthropic BYOK) | Installed by system-setup |
| `deploy/opencode-version` | Pinned OpenCode version (e.g. `v1.4.3`) | Edited manually before deploy |

### Directory Layout on the Server

```
/etc/appx/
  appx.env                      # Server config (IP, data dir, port, optional domain+CF token)
                                # Mode 600, root:root

/usr/local/bin/
  appx                          # Appx binary (mode 750, root:appx)
  opencode                      # OpenCode binary (mode 755)
  go, node, npm, npx            # Symlinks to installed versions
  claude, uv                    # Agent tools

/srv/appx/                      # Git repo (source code, build tools)

{APPX_DATA}/                    # Data dir (default: /var/lib/appx)
  .appx-internals/              # DB, TLS certs, secrets (mode 700, appx:appx)
  projects/                     # Shared project workspace (mode 2770, appx:projects)

/home/opencode/                 # OpenCode home dir (mode 700, opencode:opencode)
  .config/opencode/
    opencode.json               # Model config (pins to anthropic/ provider)
```

### OS Users and Groups

```
appx      (system user)
  ├── Home: {APPX_DATA}
  ├── Shell: /bin/bash
  ├── Groups: appx, projects
  └── Runs: appx server process

opencode  (system user)
  ├── Home: /home/opencode
  ├── Shell: /bin/bash
  ├── Groups: opencode, projects
  └── Runs: opencode serve process

projects  (system group)
  └── Shared group for {APPX_DATA}/projects/ directory
      Both users read/write; setgid ensures new files inherit the group
```

### Systemd Services

**appx.service** — the appx server:

- Runs as `User=appx`, `Group=appx`
- `AmbientCapabilities=CAP_NET_BIND_SERVICE` — bind port 443 without root
- `UMask=0007` — new files are group-writable (so opencode can access project files)
- `EnvironmentFile=/etc/appx/appx.env` — server config injected as env vars
- `After=opencode.service` — starts after OpenCode
- `Restart=on-failure`, 5s delay

**opencode.service** — the OpenCode agent backend:

- Runs as `User=opencode`, `Group=opencode`
- `Environment=HOME=/home/opencode` — explicit home dir
- `Environment=HTTPS_PROXY=http://127.0.0.1:9080` — routes agent HTTPS traffic through appx's egress proxy
- `Environment=NO_PROXY=localhost,127.0.0.1` — internal traffic bypasses the proxy
- `WorkingDirectory={APPX_DATA}/projects` — rewritten by system-setup.sh from appx.env
- `Before=appx.service` — starts before appx
- `Restart=always`, 5s delay, burst limit 5/60s
- `ExecStart=/usr/local/bin/opencode serve --hostname 127.0.0.1 --port 4096`

**Service ordering:** OpenCode starts first (`Before=appx.service`). There is a brief window where the egress proxy (`HTTPS_PROXY`) is not yet available, but this is harmless — OpenCode makes no outbound HTTPS calls during its own startup. Agent HTTPS calls only happen after a user sends a message, by which time appx is running.

---

## 3. Bootstrap: Day-1 Setup

`deploy/bootstrap.sh` is the single entry point for setting up a fresh server. It orchestrates all other scripts in sequence.

### Configuration Prompt

On first run (no `/etc/appx/appx.env` exists), bootstrap prompts for three values:

| Setting | Default | Purpose |
|---------|---------|---------|
| `APPX_HOST` | Auto-detected public IP | Added to TLS cert SANs and router |
| `APPX_DATA` | `/var/lib/appx` | Data directory for DB, certs, projects |
| `APPX_PORT` | `443` | Listen port (must be open in firewall) |

The env file also supports optional settings for Let's Encrypt:

| Setting | Purpose |
|---------|---------|
| `APPX_DOMAIN` | Domain for Let's Encrypt via Cloudflare DNS-01 |
| `CLOUDFLARE_API_TOKEN` | Cloudflare API token for DNS-01 challenge |

The env file is written to `/etc/appx/appx.env` with mode `600` (root-only read). On subsequent runs, bootstrap skips the prompt and reads the existing config.

### Pipeline

Bootstrap runs seven steps in sequence. Each step is labeled in a trap handler so failures print exactly where they stopped:

1. **Config** — prompt or read `/etc/appx/appx.env`
2. **System setup** — delegates to `system-setup.sh`
3. **Tools install** — delegates to `tools-install.sh`
4. **Build** — runs `task build` (or falls back to manual npm+go build, or uses pre-built binary)
5. **Install binary** — `install -m 750 -o root -g appx ./appx /usr/local/bin/appx`
6. **Restart services** — `systemctl restart opencode appx`
7. **Verify** — delegates to `verify-installation.sh`

If any step fails, the trap prints "FAILED at: \<step-name\>" and the script exits non-zero.

---

## 4. System Setup

`deploy/system-setup.sh` creates the OS-level infrastructure. It reads `APPX_DATA` from `/etc/appx/appx.env` (falls back to `/var/lib/appx`) and is fully idempotent.

### User and Group Creation

The `projects` group is created first as a system group. Both OS users are added to it.

**appx user:** Created with `--system --shell /bin/bash --home-dir $DATA_DIR --groups projects`. The home directory is set to the data directory so terminal sessions started from the appx UI land in the right place. On re-run, the script updates shell, groups, and home dir if they've drifted.

**opencode user:** Created with `--system --shell /bin/bash --home-dir /home/opencode --groups projects`. On re-run, shell and groups are updated.

### Directory Hierarchy

```
install -d -o appx    -g appx     -m 755  $DATA_DIR               # Traversable by all
install -d -o appx    -g appx     -m 700  $DATA_DIR/.appx-internals  # Appx-only secrets
install -d -o appx    -g projects -m 2770 $DATA_DIR/projects      # Shared workspace (setgid)
install -d -o opencode -g opencode -m 700  /home/opencode          # OpenCode-only home
```

The `2770` mode on `projects/` sets the setgid bit. This means every file or directory created inside inherits the `projects` group, regardless of which user created it. Combined with `UMask=0007` on appx.service, both users get read/write access to all project files.

The data directory itself is `755` (world-traversable) because the opencode user needs to reach `projects/` inside it. The sensitive `.appx-internals/` subdirectory is `700` — only the appx user can enter.

### OpenCode Model Config

`system-setup.sh` copies `deploy/opencode.json` to `/home/opencode/.config/opencode/opencode.json` on first run only (does not overwrite existing config). This file pins the default model to the Anthropic BYOK provider:

```json
{
  "model": "anthropic/claude-sonnet-4-6"
}
```

This ensures API calls go directly to `api.anthropic.com` using the injected API key, rather than routing through OpenCode's zen proxy (which requires a separate OpenCode account key).

### Service File Installation

`appx.service` is copied as-is. `opencode.service` needs the `WorkingDirectory` rewritten to the actual data directory path (systemd doesn't expand env vars in `WorkingDirectory`), so `sed` substitutes the resolved path before copying:

```bash
sed "s|WorkingDirectory=.*|WorkingDirectory=$DATA_DIR/projects|" \
  "$SCRIPT_DIR/opencode.service" > /etc/systemd/system/opencode.service
```

After copying, `systemctl daemon-reload` and `systemctl enable appx opencode`.

---

## 5. Tools Installation

`deploy/tools-install.sh` installs build and runtime tools system-wide. Every tool ends up in `/usr/local/bin/` regardless of where its installer places the binary. Supports amd64 and arm64.

### Go

- **Version source:** `go.mod` (falls back to `1.24.2`)
- **Install method:** Official tarball to `/usr/local/go/`, symlinked to `/usr/local/bin/go`
- **Skip condition:** Already installed (any version — no auto-upgrade)
- **Purpose:** Build tool for compiling appx

### Node.js

- **Version:** Major 24 (pinned in script)
- **Install method:** nvm to `/usr/local/nvm` (system-wide), then `nvm install 24`. Binaries (`node`, `npm`, `npx`) symlinked from the nvm version directory to `/usr/local/bin/`
- **Skip condition:** `/usr/local/bin/node` major version already matches 24
- **Purpose:** Frontend build + runtime for agents (npm packages, Claude Code)
- **Why nvm:** nvm handles the platform detection and download. `PROFILE=/dev/null` prevents it from modifying any shell rc file.

### OpenCode

- **Version source:** `deploy/opencode-version` (e.g. `v1.4.3`)
- **Install method:** Official installer (`curl | bash`), then copy/symlink to `/usr/local/bin/`. If a version pin exists and the current version doesn't match, runs `opencode upgrade <version>` and re-copies.
- **Skip condition:** Already installed at the pinned version
- **Purpose:** AI agent backend — the core process appx proxies to
- **Version pinning:** Edit `deploy/opencode-version` and run `task server:deploy`. The file is tracked in git, so version changes are visible in commit history.

### Claude Code

- **Install method:** `npm install -g @anthropic-ai/claude-code`
- **Skip condition:** Already installed
- **Purpose:** Claude CLI for terminal use by agents
- **Updates:** `sudo npm update -g @anthropic-ai/claude-code` (no service restart needed)

### uv

- **Install method:** Official installer (`curl | sh`), then copy from `~/.local/bin/uv` to `/usr/local/bin/uv`
- **Skip condition:** Already in `/usr/local/bin/`
- **Purpose:** Python version and package management for agents
- **Updates:** `uv self update`

### System-Wide PATH Strategy

Each tool's official installer puts its binary in a different location:

| Tool | Installer location | System location |
|------|-------------------|-----------------|
| Go | `/usr/local/go/bin/go` | `/usr/local/bin/go` (symlink) |
| Node | `/usr/local/nvm/versions/node/v24.x.x/bin/node` | `/usr/local/bin/node` (symlink) |
| OpenCode | `~/.opencode/bin/opencode` | `/usr/local/bin/opencode` (copy) |
| Claude | npm global bin | `/usr/local/bin/claude` (symlink if needed) |
| uv | `~/.local/bin/uv` | `/usr/local/bin/uv` (copy) |

By normalizing everything to `/usr/local/bin/`, no user needs to source nvm, modify PATH, or know where each tool's installer puts things. Both OS users and root get access through the standard system PATH.

---

## 6. Deploy: Day-N Updates

After pushing new code, `task server:deploy` handles the full update:

```bash
git pull                                    # Pull latest code
task build                                  # Build frontend + Go binary
sudo install -m 750 -o root -g appx \
  ./appx /usr/local/bin/appx               # Install binary with correct perms
sudo ./deploy/tools-install.sh              # Upgrade tools if versions changed
sudo ./deploy/system-setup.sh              # Sync users/dirs/service files
sudo systemctl restart opencode appx        # Restart both services
sudo ./deploy/verify-installation.sh        # Run full verification
```

The key property: this is the same set of scripts bootstrap uses, just orchestrated differently. If the OpenCode version in `deploy/opencode-version` hasn't changed, `tools-install.sh` is a fast no-op for that tool. If no users/dirs need updating, `system-setup.sh` prints "already exists" for each item and moves on.

### Updating individual components

| What changed | Command |
|-------------|---------|
| Appx code | `task server:deploy` |
| OpenCode version | Edit `deploy/opencode-version`, then `task server:deploy` |
| Claude Code | `sudo npm install -g @anthropic-ai/claude-code` |
| OS users/permissions | `sudo ./deploy/system-setup.sh` |
| Service files | `sudo ./deploy/system-setup.sh && sudo systemctl restart opencode appx` |

---

## 7. Verification Suite

`deploy/verify-installation.sh` runs 40+ assertions organized into 8 test categories. It exits 0 only if every assertion passes.

### Test categories

**1. Users and groups** — appx and opencode users exist, both are in the `projects` group, both have `/bin/bash` shells, appx home dir matches the data directory.

**2. Directories and permissions** — binary exists with correct ownership (`root:appx 750`), data dir is `appx:appx 755`, internals is `appx:appx 700`, projects is `appx:projects 2770`, opencode home is `opencode:opencode 700`, opencode config pins the anthropic model.

**3. Isolation: opencode user** — opencode *cannot* list internals dir, read the DB, write to internals, or execute the appx binary. Opencode *can* list projects and create files there.

**4. Isolation: appx user** — appx *can* list internals and create files in projects. Appx *cannot* read opencode's home or overwrite its own binary.

**5. Setgid** — a file created by the appx user in `projects/` inherits the `projects` group (not the user's primary group).

**6. Service files** — env file exists with correct perms (`root:root 600`), both service files exist in `/etc/systemd/system/`, both are enabled, ExecStart points to `/usr/local/bin/`, correct User= directives.

**7. Tools** — go, task, node (major 24), opencode (matching pinned version), and uv are all present in `/usr/local/bin/`. Claude is checked but reported as INFO (optional).

**8. Runtime** — if services are running: opencode responds on `:4096/health`, each service's PID is owned by the correct OS user.

The `expect_ok`, `expect_deny`, and `expect_eq` helpers produce uniform `PASS`/`FAIL` output and increment counters for the final summary.

---

## 8. Security Model

### Permission Matrix

| Resource | appx user | opencode user | root |
|----------|-----------|---------------|------|
| `{data}/.appx-internals/` (DB, TLS, secrets) | read/write | **denied** | read/write |
| `{data}/projects/` (shared workspace) | read/write | read/write | read/write |
| `/usr/local/bin/appx` (binary) | execute | **denied** | read/write |
| `/home/opencode/` | **denied** | read/write | read/write |
| `/etc/appx/appx.env` (config) | **denied** | **denied** | read/write |

### Isolation Boundaries

The security goal: OpenCode (and any agent code it runs) cannot access appx's database, TLS private keys, session tokens, or binary. This is enforced by standard Unix file permissions:

- `.appx-internals/` is mode `700` owned by `appx:appx` — only the appx user (UID) can enter
- The appx binary is mode `750` owned by `root:appx` — opencode can't execute it (not in the appx group)
- `/etc/appx/appx.env` is mode `600` owned by `root:root` — neither service user can read it directly (appx reads it via systemd's `EnvironmentFile` directive, which runs as root during service start)

### Binary Protection

The appx binary has an unusual ownership model: `root:appx` with mode `750`. This means:

- **root** can read/write/execute (owner)
- **appx group** can read and execute (group) — the appx service needs to run it
- **everyone else** has no access — opencode can't even read the binary
- **appx user can't overwrite it** — the file is owned by root, and the appx user doesn't have write permission

This prevents a compromised agent (running as opencode) from replacing the appx binary or even inspecting it.

### Egress Proxy Integration

The opencode.service file sets `HTTPS_PROXY=http://127.0.0.1:9080`, routing all agent HTTPS traffic through appx's Go CONNECT proxy. `NO_PROXY=localhost,127.0.0.1` keeps internal traffic (OpenCode-to-appx API calls) direct. This is the cooperative layer of the two-layer egress design described in the Phase 5 architecture doc; the iptables hard backstop is deferred to Phase 6.

---

## 9. Configuration Reference

All server configuration lives in `/etc/appx/appx.env`:

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `APPX_HOST` | Yes | Auto-detected IP | Server IP or hostname, added to TLS cert SANs |
| `APPX_DATA` | Yes | `/var/lib/appx` | Data directory for DB, TLS certs, projects |
| `APPX_PORT` | Yes | `443` | Listen port (must be open in firewall) |
| `APPX_DOMAIN` | No | — | Domain for Let's Encrypt via Cloudflare DNS-01 |
| `CLOUDFLARE_API_TOKEN` | No | — | Cloudflare API token for DNS-01 challenge |

To change config: `sudo nano /etc/appx/appx.env && sudo systemctl restart appx`.

The env file is read by:
- `appx.service` via `EnvironmentFile=` (appx reads `APPX_DATA`, `APPX_HOST`, `APPX_PORT`, `APPX_DOMAIN`, `CLOUDFLARE_API_TOKEN` as env vars at runtime)
- `system-setup.sh` (reads `APPX_DATA` to set directory paths and rewrite `WorkingDirectory` in the opencode service file)
- `tools-install.sh` (does not read it — tools are version-pinned, not config-dependent)
- `verify-installation.sh` (reads `APPX_DATA` to verify the correct directories)

---

## 10. Pitfalls and Design Decisions

**systemd can't expand env vars in WorkingDirectory.** The `opencode.service` template has `WorkingDirectory=/var/lib/appx/projects` as a default, but `system-setup.sh` rewrites this line with `sed` using the resolved `APPX_DATA` value before copying the file to `/etc/systemd/system/`. If you edit `opencode.service` in the repo, remember the `WorkingDirectory` line gets replaced at install time.

**nvm sources are session-scoped.** nvm doesn't add itself to PATH permanently when `PROFILE=/dev/null` is set. The script exports `NVM_DIR` and sources `nvm.sh` for the current session only, then symlinks the resulting binaries to `/usr/local/bin/`. If you need to use nvm interactively on the server, source it manually: `. /usr/local/nvm/nvm.sh`.

**OpenCode installer location varies.** The official `curl | bash` installer puts the binary in `~/.opencode/bin/opencode` relative to the running user (root during tools-install). The script checks both `/root/.opencode/bin/` and `/home/opencode/.opencode/bin/` and copies whichever it finds to `/usr/local/bin/`. After an `opencode upgrade`, the binary may be replaced in the original location, so the script re-copies after each upgrade.

**Go is install-once.** Unlike OpenCode, Go is not upgraded automatically. If `go` is already on PATH, tools-install skips it entirely. To upgrade Go, remove `/usr/local/go/` and re-run tools-install. This is intentional — Go is a build tool, not a runtime dependency, and version mismatches between the build toolchain and `go.mod` are caught at compile time.

**Claude Code is optional.** The verification suite reports Claude Code as `INFO` rather than `FAIL` if absent. It requires Node.js and is installed via npm. Agents can function without it (OpenCode is the primary agent backend), but having it available gives agents the `claude` CLI in terminal sessions.

**First-run password.** On first start, appx generates a random password and writes it to `{APPX_DATA}/.appx-internals/initial_password`. Bootstrap's final output reminds the user to log in and set their API key. The password file should be deleted after the user saves their password.

**UMask=0007 on appx.service.** This ensures files and directories created by the appx process (e.g. new project directories under `projects/`) have group-write permission. Without it, new project dirs would be `rwxr-xr-x` (default umask 022) and the opencode user wouldn't be able to write to them despite being in the `projects` group.

**Appx user home = data dir.** The appx user's home directory is set to the data directory (not a traditional `/home/appx`). This is intentional — terminal sessions opened through the appx UI inherit the user's home, and landing in the data directory is more useful than an empty home.
