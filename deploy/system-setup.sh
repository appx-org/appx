#!/usr/bin/env bash
# deploy/system-setup.sh — create OS users, groups, directories, and install
# systemd service files for appx and opencode.
#
# Must be run as root. Safe to run multiple times (idempotent).
#
# What this script does:
#   1. Reads APPX_DATA from /etc/appx/appx.env (falls back to /var/lib/appx)
#   2. Creates appx and opencode users with login shells (/bin/bash)
#      — appx user's home dir is set to the data directory
#   3. Creates a shared "projects" group for project directory access
#   4. Sets up directories with correct ownership and permissions
#   5. Copies systemd service files and enables them
#
# What this script does NOT do:
#   - Install Go, Node, opencode, or claude binaries (use tools-install.sh)
#   - Copy the appx binary (handled by bootstrap.sh / server:deploy)

set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "error: must run as root" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# ---------------------------------------------------------------------------
# Data directory — read early so it can be used for the appx user's home dir.
# ---------------------------------------------------------------------------

DATA_DIR="/var/lib/appx"
APPX_AGENT_BACKEND="pi"
if [ -f /etc/appx/appx.env ]; then
  # shellcheck source=/dev/null
  _APPX_DATA=$(grep '^APPX_DATA=' /etc/appx/appx.env | cut -d= -f2- || true)
  if [ -n "$_APPX_DATA" ]; then
    DATA_DIR="${_APPX_DATA%/}"
  fi
  _APPX_AGENT_BACKEND=$(grep '^APPX_AGENT_BACKEND=' /etc/appx/appx.env | cut -d= -f2- || true)
  if [ -n "$_APPX_AGENT_BACKEND" ]; then
    APPX_AGENT_BACKEND="$_APPX_AGENT_BACKEND"
  fi
fi
echo "data directory: $DATA_DIR"
echo "agent backend: $APPX_AGENT_BACKEND"

# ---------------------------------------------------------------------------
# OS users and groups
# ---------------------------------------------------------------------------

# Shared group — both users get read/write access to project directories.
if ! getent group projects >/dev/null 2>&1; then
  groupadd --system projects
  echo "created group: projects"
else
  echo "group projects already exists"
fi

# appx user — runs the appx server process.
# Home dir is the data directory so that shell sessions started by the terminal
# feature land in the right place and have access to tools in PATH.
if ! id -u appx >/dev/null 2>&1; then
  useradd --system --create-home --shell /bin/bash --home-dir "$DATA_DIR" \
    --groups projects appx
  echo "created user: appx (home: $DATA_DIR)"
else
  # Ensure existing user has correct shell, group membership, and home dir.
  CURRENT_HOME=$(getent passwd appx | cut -d: -f6)
  usermod --shell /bin/bash --append --groups projects appx || true
  if [ "$CURRENT_HOME" != "$DATA_DIR" ]; then
    usermod --home "$DATA_DIR" appx
    echo "user appx already exists (updated home: $CURRENT_HOME → $DATA_DIR)"
  else
    echo "user appx already exists (updated shell and groups)"
  fi
fi

# opencode user — runs the opencode server process.
if ! id -u opencode >/dev/null 2>&1; then
  useradd --system --create-home --shell /bin/bash --home-dir /home/opencode \
    --groups projects opencode
  echo "created user: opencode"
else
  usermod --shell /bin/bash --append --groups projects opencode || true
  echo "user opencode already exists (updated shell and groups)"
fi

# ---------------------------------------------------------------------------
# Directories
# ---------------------------------------------------------------------------

# Data dir: appx owns it. Accessible for traversal by others (opencode needs
# to reach the projects/ subdirectory inside it).
install -d -o appx -g appx -m 755 "$DATA_DIR"
echo "directory ready: $DATA_DIR (appx:appx 755)"

# Internals subdir: DB, TLS certs, password — appx-only, no access for others.
install -d -o appx -g appx -m 700 "$DATA_DIR/.appx-internals"
echo "directory ready: $DATA_DIR/.appx-internals (appx:appx 700)"

# Projects subdir: shared workspace for appx and opencode.
# Setgid ensures new files inherit the projects group.
install -d -o appx -g projects -m 2770 "$DATA_DIR/projects"
echo "directory ready: $DATA_DIR/projects (appx:projects 2770)"

# /home/opencode: opencode workspace.
install -d -o opencode -g opencode -m 700 /home/opencode
echo "directory ready: /home/opencode (opencode:opencode 700)"

if [ "$APPX_AGENT_BACKEND" = "opencode" ]; then
  # OpenCode config: pin the default model to the Anthropic BYOK provider so
  # that API calls go directly to api.anthropic.com using the injected key,
  # rather than routing through the opencode.ai zen proxy (which requires a
  # separate OpenCode account key).
  OC_CONFIG_DIR="/home/opencode/.config/opencode"
  OC_CONFIG_FILE="$OC_CONFIG_DIR/opencode.json"
  install -d -o opencode -g opencode -m 700 "$OC_CONFIG_DIR"
  if [ ! -f "$OC_CONFIG_FILE" ]; then
    install -m 600 -o opencode -g opencode "$SCRIPT_DIR/opencode.json" "$OC_CONFIG_FILE"
    echo "wrote opencode config → $OC_CONFIG_FILE"
  else
    echo "opencode config already exists: $OC_CONFIG_FILE"
  fi

  # AGENTS.md: global rules for the OpenCode agent, including egress access
  # request instructions. Copied only on first setup — user customizations
  # are preserved on subsequent runs.
  OC_AGENTS_FILE="$OC_CONFIG_DIR/AGENTS.md"
  if [ ! -f "$OC_AGENTS_FILE" ]; then
    install -m 600 -o opencode -g opencode "$SCRIPT_DIR/AGENTS.md" "$OC_AGENTS_FILE"
    echo "wrote agents rules → $OC_AGENTS_FILE"
  else
    echo "agents rules already exist: $OC_AGENTS_FILE"
  fi
fi

# Pi agent config/auth/cache dir. Pi is project-local for prompts, skills, and
# extensions, but auth/models/settings that should not live in project repos go
# under the agent user's private home directory.
PI_AGENT_DIR="/home/opencode/.pi/agent"
install -d -o opencode -g opencode -m 700 "$PI_AGENT_DIR"
install -d -o opencode -g opencode -m 700 "$PI_AGENT_DIR/npm"
install -d -o opencode -g opencode -m 700 "$PI_AGENT_DIR/git"
echo "directory ready: $PI_AGENT_DIR (opencode:opencode 700)"

# ---------------------------------------------------------------------------
# Appx binary permissions (if binary already deployed)
# ---------------------------------------------------------------------------

if [ -f /usr/local/bin/appx ]; then
  chown root:appx /usr/local/bin/appx
  chmod 750 /usr/local/bin/appx
  echo "binary permissions: /usr/local/bin/appx (root:appx 750)"
fi

# ---------------------------------------------------------------------------
# Systemd service files
# ---------------------------------------------------------------------------

cp "$SCRIPT_DIR/appx.service" /etc/systemd/system/appx.service

if [ "$APPX_AGENT_BACKEND" = "opencode" ]; then
  # OpenCode needs WorkingDirectory set to the shared projects dir.
  # Since systemd can't expand env vars in WorkingDirectory, we substitute
  # the resolved path into the service file before installing it.
  sed "s|WorkingDirectory=.*|WorkingDirectory=$DATA_DIR/projects|" \
    "$SCRIPT_DIR/opencode.service" > /etc/systemd/system/opencode.service
  echo "copied service files to /etc/systemd/system/"
  echo "opencode WorkingDirectory → $DATA_DIR/projects"
else
  systemctl disable --now opencode 2>/dev/null || true
  rm -f /etc/systemd/system/opencode.service
  echo "copied appx.service; opencode.service disabled for $APPX_AGENT_BACKEND backend"
fi

systemctl daemon-reload
echo "systemd reloaded"

if [ "$APPX_AGENT_BACKEND" = "opencode" ]; then
  systemctl enable appx opencode
  echo "services enabled: appx, opencode"
else
  systemctl enable appx
  echo "services enabled: appx"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

echo ""
echo "System setup complete."
