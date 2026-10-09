#!/usr/bin/env bash
# deploy/teardown.sh — reverse everything created by bootstrap.sh /
# system-setup.sh: stop+remove the systemd services, then optionally remove the
# appx/appx-agent users, the projects group, data directories, the env file, and
# the installed binaries.
#
# Must be run as root. Safe to run multiple times (idempotent).
#
# Shared build/runtime tools (go, node, pi, claude, uv) are intentionally left
# in place — they are not appx-specific.
#
# Pass --purge-data to also delete every piece of appx state (DESTRUCTIVE and
# IRREVERSIBLE):
#   - the data directory (SQLite DB, TLS certs) and the legacy agent user's home
#   - /etc/appx/secrets.env (provider credentials)
#   - the outer container and BOTH of its named volumes: builder-workspace
#     (every project's files and Pi session transcripts) and
#     builder-podman-storage (inner app images and containers)
#
# The volumes are the part that is easy to miss: in container mode they, not the
# data directory, hold all project data, and nothing else in this script touches
# them. Without removing them, --purge-data leaves projects behind and a
# subsequent bootstrap silently inherits the old workspace.

set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "error: must run as root" >&2
  exit 1
fi

PURGE_DATA=0
for arg in "$@"; do
  case "$arg" in
    --purge-data) PURGE_DATA=1 ;;
    *) echo "unknown flag: $arg" >&2; exit 1 ;;
  esac
done

ENV_FILE="/etc/appx/appx.env"

# Resolve everything we need from the env file BEFORE deleting it. The container
# and volume names are overridable per deployment, so read them rather than
# assuming the defaults — otherwise --purge-data would silently skip a renamed
# container's volumes and leave all project data behind.
DATA_DIR="/var/lib/appx"
CONTAINER_NAME="builder-outer"
WORKSPACE_VOLUME="builder-workspace"
PODMAN_VOLUME="builder-podman-storage"
if [ -f "$ENV_FILE" ]; then
  _APPX_DATA=$(grep '^APPX_DATA=' "$ENV_FILE" | cut -d= -f2- || true)
  [ -n "$_APPX_DATA" ] && DATA_DIR="${_APPX_DATA%/}"
  _CNAME=$(grep '^APPX_AGENT_CONTAINER_NAME=' "$ENV_FILE" | cut -d= -f2- || true)
  [ -n "$_CNAME" ] && CONTAINER_NAME="$_CNAME"
  _WSVOL=$(grep '^APPX_AGENT_WORKSPACE_VOLUME=' "$ENV_FILE" | cut -d= -f2- || true)
  [ -n "$_WSVOL" ] && WORKSPACE_VOLUME="$_WSVOL"
  _PDVOL=$(grep '^APPX_AGENT_PODMAN_VOLUME=' "$ENV_FILE" | cut -d= -f2- || true)
  [ -n "$_PDVOL" ] && PODMAN_VOLUME="$_PDVOL"
fi

# ---------------------------------------------------------------------------
# 1. Systemd services
# ---------------------------------------------------------------------------

for svc in appx agent-server opencode; do
  if systemctl list-unit-files "$svc.service" >/dev/null 2>&1; then
    systemctl disable --now "$svc" 2>/dev/null || true
  fi
  rm -f "/etc/systemd/system/$svc.service"
done
systemctl daemon-reload
echo "removed services: appx, agent-server, opencode"

# Kill any stragglers that escaped systemd.
pkill -u appx-agent -f '(^|/)agent-server( |$)|agent-server/dist/server\.js' 2>/dev/null || true
pkill -u appx -f '(^|/)appx( |$)' 2>/dev/null || true

# ---------------------------------------------------------------------------
# 2. Outer agent container
# ---------------------------------------------------------------------------

# The container runs with `--restart unless-stopped`, so the Docker daemon keeps
# it (and the agent inside it) alive and brings it back on reboot even after the
# appx service is gone. Remove it unconditionally: it is a process, not data —
# its state lives in the named volumes, which survive here and are only deleted
# under --purge-data below. appx's EnsureRunning recreates it on next startup.
if command -v docker >/dev/null 2>&1; then
  if docker inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
    docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
    echo "removed outer container: $CONTAINER_NAME (volumes kept)"
  else
    echo "no outer container named $CONTAINER_NAME"
  fi
else
  echo "docker not installed — skipping outer container removal"
fi

# ---------------------------------------------------------------------------
# 3. Binaries
# ---------------------------------------------------------------------------

rm -f /usr/local/bin/appx /usr/local/bin/agent-server /usr/local/bin/opencode
echo "removed binaries: /usr/local/bin/{appx,agent-server,opencode}"

# ---------------------------------------------------------------------------
# 4. Users and groups
# ---------------------------------------------------------------------------

# userdel -r would remove home dirs; we manage data deletion explicitly below so
# the default (no --purge-data) leaves project data on disk.
for user in appx appx-agent; do
  if id -u "$user" >/dev/null 2>&1; then
    userdel "$user" 2>/dev/null || true
    echo "removed user: $user"
  fi
done

for grp in projects appx-agent; do
  if getent group "$grp" >/dev/null 2>&1; then
    groupdel "$grp" 2>/dev/null || true
    echo "removed group: $grp"
  fi
done

# ---------------------------------------------------------------------------
# 5. Config + data
# ---------------------------------------------------------------------------

rm -f "$ENV_FILE"
# The seccomp profile is extracted from the agent image by tools-install.sh, so
# it is a generated artifact — remove it too, or /etc/appx never goes away.
# secrets.env is deliberately NOT removed unless --purge-data: it holds
# credentials the operator may not have stored elsewhere.
rm -f /etc/appx/seccomp-builder.json
if [ "$PURGE_DATA" -eq 1 ]; then
  rm -f /etc/appx/secrets.env
fi
rmdir /etc/appx 2>/dev/null || true
echo "removed config: $ENV_FILE and /etc/appx/seccomp-builder.json"
if [ "$PURGE_DATA" -eq 0 ] && [ -f /etc/appx/secrets.env ]; then
  echo "kept /etc/appx/secrets.env (provider credentials; --purge-data removes it)"
fi

if [ "$PURGE_DATA" -eq 1 ]; then
  rm -rf "$DATA_DIR" /home/appx-agent
  echo "purged data: $DATA_DIR and /home/appx-agent"
else
  echo "kept data: $DATA_DIR and /home/appx-agent (re-run with --purge-data to delete)"
fi

# The named volumes, NOT the data directory, hold all project data in container
# mode: builder-workspace has every project's files and Pi session transcripts,
# builder-podman-storage has the inner app images. They are removable only now
# that the container referencing them is gone (section 2) — `docker volume rm`
# refuses while a container still uses it.
if [ "$PURGE_DATA" -eq 1 ]; then
  if command -v docker >/dev/null 2>&1; then
    for vol in "$WORKSPACE_VOLUME" "$PODMAN_VOLUME"; do
      if docker volume inspect "$vol" >/dev/null 2>&1; then
        if docker volume rm "$vol" >/dev/null 2>&1; then
          echo "purged volume: $vol"
        else
          # Almost always another container still mounting it.
          echo "WARNING: could not remove volume $vol — still in use?" >&2
          echo "         inspect with: docker ps -a --filter volume=$vol" >&2
        fi
      else
        echo "no volume named $vol"
      fi
    done
  else
    echo "WARNING: docker not installed — project data in the named volumes was NOT purged" >&2
  fi
else
  echo "kept volumes: $WORKSPACE_VOLUME and $PODMAN_VOLUME (ALL project files and"
  echo "  sessions live here, not in $DATA_DIR; --purge-data deletes them)"
fi

echo ""
echo "Teardown complete. Shared tools (go, node, pi, claude, uv) were left in place."
