#!/usr/bin/env bash
# deploy/system-setup.sh — create OS users, groups, directories, and install
# systemd service files for appx plus the Pi agent backend.
#
# Must be run as root. Safe to run multiple times (idempotent).
#
# What this script does:
#   1. Reads APPX_DATA from /etc/appx/appx.env (falls back to /var/lib/appx)
#   2. Creates appx and appx-agent users with login shells (/bin/bash)
#      — appx user's home dir is set to the data directory
#   3. Creates a shared "projects" group for project directory access
#   4. Sets up directories with correct ownership and permissions
#   5. Copies systemd service files and enables them
#
# What this script does NOT do:
#   - Install Go, Node, Pi, agent-server, or Claude binaries (use tools-install.sh)
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
if [ -f /etc/appx/appx.env ]; then
  # shellcheck source=/dev/null
  _APPX_DATA=$(grep '^APPX_DATA=' /etc/appx/appx.env | cut -d= -f2- || true)
  if [ -n "$_APPX_DATA" ]; then
    DATA_DIR="${_APPX_DATA%/}"
  fi
fi
echo "data directory: $DATA_DIR"

# Container mode (Stage 3): appx creates/supervises the agent-server OUTER
# container itself; there is no host appx-agent user or agent-server.service.
# Host mode (the default until container mode has soaked in prod) keeps them.
APPX_AGENT_CONTAINER="false"
if [ -f /etc/appx/appx.env ]; then
  _CM=$(grep '^APPX_AGENT_CONTAINER=' /etc/appx/appx.env | cut -d= -f2- || true)
  case "$(echo "${_CM}" | tr '[:upper:]' '[:lower:]')" in
    1|true|yes|on) APPX_AGENT_CONTAINER="true" ;;
  esac
fi
echo "agent backend: pi ($([ "$APPX_AGENT_CONTAINER" = true ] && echo 'container mode' || echo 'host mode'))"

# ---------------------------------------------------------------------------
# OS users and groups
# ---------------------------------------------------------------------------

# Shared group — appx and appx-agent get read/write access to project directories.
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

# appx-agent user — runs the Pi agent-server process (HOST MODE ONLY). In
# container mode agent-server runs as uid 1000 *inside* the outer container, so
# no host user is needed.
if [ "$APPX_AGENT_CONTAINER" = "true" ]; then
  echo "container mode: skipping appx-agent user (agent-server runs inside the outer container)"
else
  if ! getent group appx-agent >/dev/null 2>&1; then
    groupadd --system appx-agent
    echo "created group: appx-agent"
  fi
  if ! id -u appx-agent >/dev/null 2>&1; then
    useradd --system --create-home --shell /bin/bash --home-dir /home/appx-agent \
      --gid appx-agent --groups projects appx-agent
    echo "created user: appx-agent"
  else
    usermod --shell /bin/bash --home /home/appx-agent --append --groups projects appx-agent || true
    echo "user appx-agent already exists (updated shell, home, and groups)"
  fi
fi

# ---------------------------------------------------------------------------
# Directories
# ---------------------------------------------------------------------------

# Data dir: appx owns it. Accessible for traversal by the agent user so it can
# reach the projects/ subdirectory inside it.
install -d -o appx -g appx -m 755 "$DATA_DIR"
echo "directory ready: $DATA_DIR (appx:appx 755)"

# Internals subdir: DB, TLS certs, password — appx-only, no access for others.
install -d -o appx -g appx -m 700 "$DATA_DIR/.appx-internals"
echo "directory ready: $DATA_DIR/.appx-internals (appx:appx 700)"

# Projects subdir: shared workspace for appx and appx-agent.
# Setgid ensures new files inherit the projects group.
install -d -o appx -g projects -m 2770 "$DATA_DIR/projects"
echo "directory ready: $DATA_DIR/projects (appx:projects 2770)"

# /home/appx-agent: private Pi agent workspace (HOST MODE ONLY).
if [ "$APPX_AGENT_CONTAINER" != "true" ]; then
  install -d -o appx-agent -g appx-agent -m 700 /home/appx-agent
  echo "directory ready: /home/appx-agent (appx-agent:appx-agent 700)"
  if [ ! -d /home/appx-agent/.pi ] && [ -d /home/opencode/.pi ]; then
    cp -a /home/opencode/.pi /home/appx-agent/.pi
    chown -R appx-agent:appx-agent /home/appx-agent/.pi
    chmod 700 /home/appx-agent/.pi /home/appx-agent/.pi/agent 2>/dev/null || true
    echo "migrated existing Pi agent data to /home/appx-agent/.pi"
  fi

  # Pi agent config/auth/cache dir. Pi is project-local for prompts, skills, and
  # extensions, but auth/models/settings that should not live in project repos go
  # under the agent user's private home directory.
  PI_AGENT_DIR="/home/appx-agent/.pi/agent"
  install -d -o appx-agent -g appx-agent -m 700 "$PI_AGENT_DIR"
  install -d -o appx-agent -g appx-agent -m 700 "$PI_AGENT_DIR/npm"
  install -d -o appx-agent -g appx-agent -m 700 "$PI_AGENT_DIR/git"
  echo "directory ready: $PI_AGENT_DIR (appx-agent:appx-agent 700)"
fi

# ---------------------------------------------------------------------------
# Container mode: docker access + the security profile appx references
# ---------------------------------------------------------------------------
# Decision (docker invocation privilege): the INDUSTRY-STANDARD secure option is
# rootless docker or host podman for the appx user — docker *group* membership is
# root-equivalent (you can `docker run -v /:/host` and own the box). We therefore
# PREFER rootless. If the appx user already has a working rootless docker/podman,
# we leave it alone. Otherwise we fall back to the docker group with a loud
# warning, because the alternative is a non-functional deploy. Document the
# trade-off in your threat model; the long-term target is rootless.
if [ "$APPX_AGENT_CONTAINER" = "true" ]; then
  # The tailored seccomp profile is the security boundary; appx references it by
  # absolute path at `docker run` time. Install it where APPX_AGENT_SECCOMP points.
  install -d -m 755 /etc/appx
  if [ -f "$SCRIPT_DIR/builder-container/seccomp-builder.json" ]; then
    install -m 644 "$SCRIPT_DIR/builder-container/seccomp-builder.json" /etc/appx/seccomp-builder.json
    echo "installed seccomp profile → /etc/appx/seccomp-builder.json"
  else
    echo "WARNING: seccomp-builder.json not found in $SCRIPT_DIR/builder-container/ — set APPX_AGENT_SECCOMP manually"
  fi

  if ! command -v docker >/dev/null 2>&1 && ! command -v podman >/dev/null 2>&1; then
    echo "WARNING: neither docker nor podman is installed. Install one before starting appx in container mode."
    echo "         (Ubuntu: 'apt-get install -y docker.io' for rootful docker, or set up rootless docker / podman.)"
  fi

  # If the appx user can't already drive a container runtime, fall back to the
  # docker group (root-equivalent — see decision above).
  if command -v docker >/dev/null 2>&1; then
    if su -s /bin/sh appx -c 'docker info' >/dev/null 2>&1; then
      echo "appx user can already invoke docker (rootless or pre-configured) — leaving as-is"
    elif getent group docker >/dev/null 2>&1; then
      usermod --append --groups docker appx || true
      echo "WARNING: added appx to the 'docker' group (root-equivalent). Prefer rootless docker/podman in hardened deployments."
    fi
  fi
fi

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

systemctl disable --now opencode 2>/dev/null || true
rm -f /etc/systemd/system/opencode.service

if [ "$APPX_AGENT_CONTAINER" = "true" ]; then
  # Container mode: appx supervises the outer container itself — no host
  # agent-server.service. Disable a stale one if a prior host-mode install left it.
  if systemctl list-unit-files 2>/dev/null | grep -q '^agent-server.service'; then
    systemctl disable --now agent-server 2>/dev/null || true
    rm -f /etc/systemd/system/agent-server.service
    echo "container mode: removed host agent-server.service (appx manages the outer container)"
  fi
  systemctl daemon-reload
  echo "systemd reloaded"
  systemctl enable appx
  echo "services enabled: appx (agent-server runs inside the appx-managed container)"
else
  if ! systemctl is-active --quiet agent-server 2>/dev/null; then
    pkill -u appx-agent -f '(^|/)agent-server( |$)|agent-server/dist/server\.js' 2>/dev/null || true
    if id -u opencode >/dev/null 2>&1; then
      pkill -u opencode -f '(^|/)agent-server( |$)|agent-server/dist/server\.js' 2>/dev/null || true
    fi
  fi
  sed "s|__APPX_PROJECTS_DIR__|$DATA_DIR/projects|g" \
    "$SCRIPT_DIR/agent-server.service" > /etc/systemd/system/agent-server.service
  echo "copied appx.service and agent-server.service"

  systemctl daemon-reload
  echo "systemd reloaded"

  systemctl enable appx agent-server
  echo "services enabled: appx, agent-server"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

echo ""
echo "System setup complete."
