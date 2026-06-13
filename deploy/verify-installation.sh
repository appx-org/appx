#!/usr/bin/env bash
# deploy/verify-installation.sh — full system verification after bootstrap.
#
# Deploy is CONTAINER MODE ONLY (Stage 4): appx runs as the `appx` systemd
# service and supervises the agent-server OUTER container. There is no host
# appx-agent user, no agent-server.service, and no host Pi/agent-server install.
# This script verifies users, directories, permissions, tools, the systemd unit,
# and (when running) the outer container's security boundary + secret wiring.
#
# Must be run as root. Exits 0 if all tests pass, 1 otherwise.
#
# Usage: sudo ./deploy/verify-installation.sh

set -uo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "error: must run as root" >&2
  exit 1
fi

PASS=0
FAIL=0

# Read data directory + container config from env file, fall back to defaults.
DATA_DIR="/var/lib/appx"
ENV_FILE="/etc/appx/appx.env"
SECRETS_FILE="/etc/appx/secrets.env"
CONTAINER_NAME="builder-outer"
if [ -f "$ENV_FILE" ]; then
  _APPX_DATA=$(grep '^APPX_DATA=' "$ENV_FILE" | cut -d= -f2- || true)
  [ -n "$_APPX_DATA" ] && DATA_DIR="${_APPX_DATA%/}"
  _CNAME=$(grep '^APPX_AGENT_CONTAINER_NAME=' "$ENV_FILE" | cut -d= -f2- || true)
  [ -n "$_CNAME" ] && CONTAINER_NAME="$_CNAME"
fi
echo "data directory: $DATA_DIR"
echo "agent backend: pi (container mode — outer container '$CONTAINER_NAME')"
echo ""

expect_ok() {
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then echo "  PASS  $desc"; PASS=$((PASS + 1))
  else echo "  FAIL  $desc"; FAIL=$((FAIL + 1)); fi
}

expect_deny() {
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then echo "  FAIL  $desc (should have been denied)"; FAIL=$((FAIL + 1))
  else echo "  PASS  $desc"; PASS=$((PASS + 1)); fi
}

expect_eq() {
  local desc="$1" actual="$2" expected="$3"
  if [ "$actual" = "$expected" ]; then echo "  PASS  $desc"; PASS=$((PASS + 1))
  else echo "  FAIL  $desc (got: $actual, expected: $expected)"; FAIL=$((FAIL + 1)); fi
}

# ---------------------------------------------------------------------------
echo "=== 1. Users and groups ==="
# ---------------------------------------------------------------------------

expect_ok   "appx user exists"                id appx
expect_ok   "projects group exists"           getent group projects
if id -nG appx | grep -qw projects; then
  echo "  PASS  appx is in projects group"; PASS=$((PASS + 1))
else
  echo "  FAIL  appx is in projects group"; FAIL=$((FAIL + 1))
fi
# Docker access (root-equivalent — decided for Stage 4; scoped down in Stage 5).
if id -nG appx | grep -qw docker; then
  echo "  PASS  appx is in docker group (can drive the daemon)"; PASS=$((PASS + 1))
else
  echo "  FAIL  appx is in docker group"; FAIL=$((FAIL + 1))
fi
expect_eq "appx shell is /bin/bash" \
  "$(getent passwd appx | cut -d: -f7)" "/bin/bash"
expect_eq "appx home dir is data dir" \
  "$(getent passwd appx | cut -d: -f6)" "$DATA_DIR"

# Host-mode artifacts must be gone.
expect_deny "no host appx-agent user (host mode removed)"  id appx-agent
expect_deny "no host agent-server.service"  test -f /etc/systemd/system/agent-server.service
expect_deny "no host /home/appx-agent dir"  test -d /home/appx-agent
expect_deny "no host pi binary"             test -x /usr/local/bin/pi
expect_deny "no host agent-server binary"   test -x /usr/local/bin/agent-server

# ---------------------------------------------------------------------------
echo ""
echo "=== 2. Directories and permissions ==="
# ---------------------------------------------------------------------------

expect_ok "appx binary exists"       test -f /usr/local/bin/appx
expect_eq "appx binary is root:appx 750" \
  "$(stat -c '%U:%G %a' /usr/local/bin/appx 2>/dev/null)" "root:appx 750"

expect_ok "data dir exists"          test -d "$DATA_DIR"
expect_eq "data dir is appx:appx 755" \
  "$(stat -c '%U:%G %a' "$DATA_DIR" 2>/dev/null)" "appx:appx 755"

expect_ok "internals dir exists"     test -d "$DATA_DIR/.appx-internals"
expect_eq "internals dir is appx:appx 700" \
  "$(stat -c '%U:%G %a' "$DATA_DIR/.appx-internals" 2>/dev/null)" "appx:appx 700"

expect_ok "projects dir exists"      test -d "$DATA_DIR/projects"
expect_eq "projects dir is appx:projects 2770" \
  "$(stat -c '%U:%G %a' "$DATA_DIR/projects" 2>/dev/null)" "appx:projects 2770"

expect_ok "seccomp profile installed" test -f /etc/appx/seccomp-builder.json

# ---------------------------------------------------------------------------
echo ""
echo "=== 3. Isolation: appx user ==="
# ---------------------------------------------------------------------------

expect_ok   "appx can list internals dir"           su -s /bin/bash appx -c "ls $DATA_DIR/.appx-internals/"
expect_ok   "appx can create file in projects"      su -s /bin/bash appx -c "touch $DATA_DIR/projects/.verify-ax && rm $DATA_DIR/projects/.verify-ax"
expect_deny "appx cannot overwrite its own binary"  su -s /bin/bash appx -c "cp /usr/local/bin/appx /usr/local/bin/appx.bak"

# ---------------------------------------------------------------------------
echo ""
echo "=== 4. Setgid on projects directory ==="
# ---------------------------------------------------------------------------

su -s /bin/bash appx -c "touch $DATA_DIR/projects/.verify-gid" 2>/dev/null
FGROUP=$(stat -c '%G' "$DATA_DIR/projects/.verify-gid" 2>/dev/null || echo "MISSING")
su -s /bin/bash appx -c "rm $DATA_DIR/projects/.verify-gid" 2>/dev/null
expect_eq "new files inherit projects group" "$FGROUP" "projects"

# ---------------------------------------------------------------------------
echo ""
echo "=== 5. Service files + secrets ==="
# ---------------------------------------------------------------------------

expect_ok "env file exists"              test -f "$ENV_FILE"
expect_eq "env file is root:root 600" \
  "$(stat -c '%U:%G %a' "$ENV_FILE" 2>/dev/null)" "root:root 600"
expect_ok "env file selects container mode" \
  grep -Eq '^APPX_AGENT_CONTAINER=(1|true|yes|on)$' "$ENV_FILE"
expect_ok "env file sets APPX_AGENT_IMAGE"   grep -q '^APPX_AGENT_IMAGE=' "$ENV_FILE"
expect_ok "env file sets APPX_AGENT_SECCOMP" grep -q '^APPX_AGENT_SECCOMP=' "$ENV_FILE"
expect_ok "appx.service exists"          test -f /etc/systemd/system/appx.service
expect_ok "appx service enabled"         systemctl is-enabled appx
expect_deny "legacy opencode.service absent" test -f /etc/systemd/system/opencode.service
expect_ok "appx.service orders after docker" \
  grep -q 'After=docker.service' /etc/systemd/system/appx.service
expect_ok "appx.service Wants docker" \
  grep -q 'Wants=docker.service' /etc/systemd/system/appx.service
expect_ok "appx.service is Type=simple" \
  grep -q 'Type=simple' /etc/systemd/system/appx.service
expect_ok "appx.service references optional secrets.env" \
  grep -q 'EnvironmentFile=-/etc/appx/secrets.env' /etc/systemd/system/appx.service
expect_ok "appx ExecStart is /usr/local/bin" \
  grep -q "ExecStart=/usr/local/bin/appx" /etc/systemd/system/appx.service
expect_ok "appx runs as appx user" \
  grep -q "User=appx" /etc/systemd/system/appx.service

# Secrets file (optional) must be root:root 0600 if present.
if [ -f "$SECRETS_FILE" ]; then
  expect_eq "secrets.env is root:root 600" \
    "$(stat -c '%U:%G %a' "$SECRETS_FILE" 2>/dev/null)" "root:root 600"
else
  echo "  INFO  no $SECRETS_FILE (provider creds may live in $ENV_FILE)"
fi

# ---------------------------------------------------------------------------
echo ""
echo "=== 6. Tools ==="
# ---------------------------------------------------------------------------

expect_ok "go binary available"              command -v go
expect_ok "task binary available"            command -v task
expect_ok "node binary in /usr/local/bin"    test -x /usr/local/bin/node
EXPECTED_NODE_MAJOR="24"
ACTUAL_NODE_MAJOR=$(/usr/local/bin/node --version 2>/dev/null | sed 's/^v//' | cut -d. -f1 || echo "0")
expect_eq "node major version is $EXPECTED_NODE_MAJOR" \
  "$ACTUAL_NODE_MAJOR" "$EXPECTED_NODE_MAJOR"
expect_deny "legacy opencode binary absent" test -x /usr/local/bin/opencode
expect_ok "docker available"                 command -v docker
expect_ok "uv binary in /usr/local/bin"      test -x /usr/local/bin/uv

if [ -x /usr/local/bin/claude ]; then
  echo "  INFO  claude installed: $(/usr/local/bin/claude --version 2>/dev/null || echo 'unknown')"
else
  echo "  INFO  claude not installed (optional — terminal feature)"
fi

# ---------------------------------------------------------------------------
echo ""
echo "=== 7. Outer image ==="
# ---------------------------------------------------------------------------

APPX_AGENT_IMAGE=$(grep '^APPX_AGENT_IMAGE=' "$ENV_FILE" 2>/dev/null | cut -d= -f2- || true)
APPX_AGENT_IMAGE="${APPX_AGENT_IMAGE:-builder-outer}"
expect_ok "outer image '$APPX_AGENT_IMAGE' present" \
  docker image inspect "$APPX_AGENT_IMAGE"

# ---------------------------------------------------------------------------
echo ""
echo "=== 8. Runtime (if appx is running) ==="
# ---------------------------------------------------------------------------

expect_deny "legacy opencode service inactive" systemctl is-active opencode

if systemctl is-active --quiet appx 2>/dev/null; then
  expect_ok "appx is running"        systemctl is-active appx
  AX_PID=$(systemctl show appx --property=MainPID --value 2>/dev/null)
  if [ -n "$AX_PID" ] && [ "$AX_PID" != "0" ]; then
    AX_USER=$(ps -o user= -p "$AX_PID" 2>/dev/null || echo "unknown")
    expect_eq "appx process runs as appx user" "$AX_USER" "appx"
  fi

  # Outer container: exists, running, healthy, with the proven security boundary.
  if docker inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
    expect_eq "outer container is running" \
      "$(docker inspect -f '{{.State.Running}}' "$CONTAINER_NAME" 2>/dev/null)" "true"
    expect_ok "agent-server inside the container responds on :4001" \
      curl -sf --max-time 5 http://127.0.0.1:4001/
    expect_eq "Privileged=false" \
      "$(docker inspect -f '{{.HostConfig.Privileged}}' "$CONTAINER_NAME" 2>/dev/null)" "false"
    expect_eq "CapAdd is empty" \
      "$(docker inspect -f '{{.HostConfig.CapAdd}}' "$CONTAINER_NAME" 2>/dev/null)" "[]"
    expect_deny "no no-new-privileges" \
      bash -c "docker inspect -f '{{.HostConfig.SecurityOpt}}' '$CONTAINER_NAME' | grep -q 'no-new-privileges'"
    expect_deny "no /dev/fuse device" \
      bash -c "docker inspect -f '{{.HostConfig.Devices}}' '$CONTAINER_NAME' | grep -q '/dev/fuse'"
    expect_eq "restart policy is unless-stopped" \
      "$(docker inspect -f '{{.HostConfig.RestartPolicy.Name}}' "$CONTAINER_NAME" 2>/dev/null)" "unless-stopped"
    expect_ok "publishes the API + app range (4001 + 10000-10199)" \
      bash -c "docker inspect -f '{{json .HostConfig.PortBindings}}' '$CONTAINER_NAME' | grep -q '4001/tcp' \
               && docker inspect -f '{{json .HostConfig.PortBindings}}' '$CONTAINER_NAME' | grep -q '10199/tcp'"
    expect_deny "publishes are loopback-only (never 0.0.0.0)" \
      bash -c "docker inspect -f '{{json .HostConfig.PortBindings}}' '$CONTAINER_NAME' | grep -q '\"0.0.0.0\"'"

    # Secret reachability: the forwarded provider key is present in the
    # container env (only when one was configured). Never print the value.
    if [ -s "$SECRETS_FILE" ] || grep -q '^ANTHROPIC_API_KEY=' "$ENV_FILE" 2>/dev/null; then
      expect_ok "provider secret reachable inside the container (ANTHROPIC_API_KEY set)" \
        bash -c "docker exec '$CONTAINER_NAME' printenv ANTHROPIC_API_KEY >/dev/null 2>&1"
    else
      echo "  INFO  no ANTHROPIC_API_KEY configured — skipping secret-reachability check"
    fi

    # Secrets must never land in the journal.
    expect_deny "no ANTHROPIC_API_KEY value leaked into journalctl -u appx" \
      bash -c "journalctl -u appx --no-pager 2>/dev/null | grep -qi 'sk-ant-'"
  else
    echo "  FAIL  outer container '$CONTAINER_NAME' not found while appx is active"; FAIL=$((FAIL + 1))
  fi
else
  echo "  SKIP  appx not running (start with: systemctl start appx)"
fi

# ---------------------------------------------------------------------------
echo ""
echo "==="
echo "Results: $PASS passed, $FAIL failed"
echo ""

if [ "$FAIL" -eq 0 ]; then
  echo "All tests pass. System is configured correctly."
  exit 0
else
  echo "Some tests failed. Review the output above."
  exit 1
fi
