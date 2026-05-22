#!/usr/bin/env bash
# deploy/verify-installation.sh — full system verification after bootstrap.
#
# Tests that users, groups, directories, permissions, isolation, tools,
# service files, and runtime are all correctly configured.
#
# Must be run as root. Exits 0 if all tests pass, 1 otherwise.
#
# Usage: sudo ./deploy/verify.sh

set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "error: must run as root" >&2
  exit 1
fi

PASS=0
FAIL=0

# Read data directory from env file, fall back to default.
DATA_DIR="/var/lib/appx"
ENV_FILE="/etc/appx/appx.env"
if [ -f "$ENV_FILE" ]; then
  _APPX_DATA=$(grep '^APPX_DATA=' "$ENV_FILE" | cut -d= -f2- || true)
  if [ -n "$_APPX_DATA" ]; then
    DATA_DIR="${_APPX_DATA%/}"
  fi
fi
echo "data directory: $DATA_DIR"
echo ""

# expect_ok: command should succeed
expect_ok() {
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then
    echo "  PASS  $desc"
    PASS=$((PASS + 1))
  else
    echo "  FAIL  $desc"
    FAIL=$((FAIL + 1))
  fi
}

# expect_deny: command should fail (permission denied, not found, etc.)
expect_deny() {
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then
    echo "  FAIL  $desc (should have been denied)"
    FAIL=$((FAIL + 1))
  else
    echo "  PASS  $desc"
    PASS=$((PASS + 1))
  fi
}

# expect_eq: two values should match
expect_eq() {
  local desc="$1" actual="$2" expected="$3"
  if [ "$actual" = "$expected" ]; then
    echo "  PASS  $desc"
    PASS=$((PASS + 1))
  else
    echo "  FAIL  $desc (got: $actual, expected: $expected)"
    FAIL=$((FAIL + 1))
  fi
}

# ---------------------------------------------------------------------------
echo "=== 1. Users and groups ==="
# ---------------------------------------------------------------------------

expect_ok   "appx user exists"                id appx
expect_ok   "opencode user exists"            id opencode
expect_ok   "projects group exists"           getent group projects
if id -nG appx | grep -qw projects >/dev/null 2>&1; then
  echo "  PASS  appx is in projects group"; PASS=$((PASS + 1))
else
  echo "  FAIL  appx is in projects group"; FAIL=$((FAIL + 1))
fi
if id -nG opencode | grep -qw projects >/dev/null 2>&1; then
  echo "  PASS  opencode is in projects group"; PASS=$((PASS + 1))
else
  echo "  FAIL  opencode is in projects group"; FAIL=$((FAIL + 1))
fi

expect_eq "appx shell is /bin/bash" \
  "$(getent passwd appx | cut -d: -f7)" "/bin/bash"
expect_eq "opencode shell is /bin/bash" \
  "$(getent passwd opencode | cut -d: -f7)" "/bin/bash"
expect_eq "appx home dir is data dir" \
  "$(getent passwd appx | cut -d: -f6)" "$DATA_DIR"

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

expect_ok "opencode home exists"     test -d /home/opencode
expect_eq "opencode home is opencode:opencode 700" \
  "$(stat -c '%U:%G %a' /home/opencode 2>/dev/null)" "opencode:opencode 700"
expect_ok "opencode config sets anthropic model" \
  grep -q '"anthropic/' /home/opencode/.config/opencode/opencode.json
expect_ok "opencode AGENTS.md exists" \
  test -f /home/opencode/.config/opencode/AGENTS.md
expect_ok "pi agent dir exists"     test -d /home/opencode/.pi/agent
expect_eq "pi agent dir is opencode:opencode 700" \
  "$(stat -c '%U:%G %a' /home/opencode/.pi/agent 2>/dev/null)" "opencode:opencode 700"

# ---------------------------------------------------------------------------
echo ""
echo "=== 3. Isolation: opencode user ==="
# ---------------------------------------------------------------------------

expect_deny "opencode cannot list internals dir"    su -s /bin/bash opencode -c "ls $DATA_DIR/.appx-internals/"
expect_deny "opencode cannot read DB file"          su -s /bin/bash opencode -c "cat $DATA_DIR/.appx-internals/appx.db"
expect_deny "opencode cannot write to internals"    su -s /bin/bash opencode -c "touch $DATA_DIR/.appx-internals/hack"
expect_deny "opencode cannot execute appx binary"   su -s /bin/bash opencode -c "/usr/local/bin/appx --version"
expect_ok   "opencode can list projects"            su -s /bin/bash opencode -c "ls $DATA_DIR/projects/"
expect_ok   "opencode can create file in projects"  su -s /bin/bash opencode -c "touch $DATA_DIR/projects/.verify-oc && rm $DATA_DIR/projects/.verify-oc"

# ---------------------------------------------------------------------------
echo ""
echo "=== 4. Isolation: appx user ==="
# ---------------------------------------------------------------------------

expect_ok   "appx can list internals dir"           su -s /bin/bash appx -c "ls $DATA_DIR/.appx-internals/"
expect_ok   "appx can create file in projects"      su -s /bin/bash appx -c "touch $DATA_DIR/projects/.verify-ax && rm $DATA_DIR/projects/.verify-ax"
expect_deny "appx cannot read opencode home"        su -s /bin/bash appx -c "ls /home/opencode/"
expect_deny "appx cannot overwrite its own binary"  su -s /bin/bash appx -c "cp /usr/local/bin/appx /usr/local/bin/appx.bak"

# ---------------------------------------------------------------------------
echo ""
echo "=== 5. Setgid on projects directory ==="
# ---------------------------------------------------------------------------

# Files created by either user should inherit the projects group.
su -s /bin/bash appx -c "touch $DATA_DIR/projects/.verify-gid" 2>/dev/null
FGROUP=$(stat -c '%G' "$DATA_DIR/projects/.verify-gid" 2>/dev/null || echo "MISSING")
su -s /bin/bash appx -c "rm $DATA_DIR/projects/.verify-gid" 2>/dev/null
expect_eq "new files inherit projects group" "$FGROUP" "projects"

# ---------------------------------------------------------------------------
echo ""
echo "=== 6. Service files ==="
# ---------------------------------------------------------------------------

expect_ok "env file exists"              test -f /etc/appx/appx.env
expect_eq "env file is root:root 600" \
  "$(stat -c '%U:%G %a' /etc/appx/appx.env 2>/dev/null)" "root:root 600"
expect_ok "appx.service exists"          test -f /etc/systemd/system/appx.service
expect_ok "opencode.service exists"      test -f /etc/systemd/system/opencode.service
expect_ok "appx service enabled"         systemctl is-enabled appx
expect_ok "opencode service enabled"     systemctl is-enabled opencode
expect_ok "opencode ExecStart is /usr/local/bin" \
  grep -q "ExecStart=/usr/local/bin/opencode" /etc/systemd/system/opencode.service
expect_ok "appx ExecStart is /usr/local/bin" \
  grep -q "ExecStart=/usr/local/bin/appx" /etc/systemd/system/appx.service
expect_ok "appx runs as appx user" \
  grep -q "User=appx" /etc/systemd/system/appx.service
expect_ok "opencode runs as opencode user" \
  grep -q "User=opencode" /etc/systemd/system/opencode.service

# ---------------------------------------------------------------------------
echo ""
echo "=== 7. Tools ==="
# ---------------------------------------------------------------------------

expect_ok "go binary available"              command -v go
expect_ok "task binary available"            command -v task
expect_ok "node binary in /usr/local/bin"    test -x /usr/local/bin/node
EXPECTED_NODE_MAJOR="24"
ACTUAL_NODE_MAJOR=$(/usr/local/bin/node --version 2>/dev/null | sed 's/^v//' | cut -d. -f1 || echo "0")
expect_eq "node major version is $EXPECTED_NODE_MAJOR" \
  "$ACTUAL_NODE_MAJOR" "$EXPECTED_NODE_MAJOR"
expect_ok "opencode binary in /usr/local/bin" test -x /usr/local/bin/opencode
expect_ok "pi binary in /usr/local/bin"       test -x /usr/local/bin/pi
expect_ok "uv binary in /usr/local/bin"       test -x /usr/local/bin/uv

EXPECTED_OC_VERSION=""
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
if [ -f "$SCRIPT_DIR/opencode-version" ]; then
  EXPECTED_OC_VERSION=$(cat "$SCRIPT_DIR/opencode-version" | tr -d '[:space:]' | sed 's/^v//')
fi
if [ -n "$EXPECTED_OC_VERSION" ]; then
  ACTUAL_OC_VERSION=$(/usr/local/bin/opencode --version 2>/dev/null || echo "unknown")
  expect_eq "opencode version matches deploy/opencode-version" \
    "$ACTUAL_OC_VERSION" "$EXPECTED_OC_VERSION"
fi

EXPECTED_PI_VERSION=""
if [ -f "$SCRIPT_DIR/pi-version" ]; then
  EXPECTED_PI_VERSION=$(cat "$SCRIPT_DIR/pi-version" | tr -d '[:space:]')
fi
if [ -n "$EXPECTED_PI_VERSION" ]; then
  ACTUAL_PI_VERSION=$(/usr/local/bin/pi --version 2>/dev/null || echo "unknown")
  expect_eq "pi version matches deploy/pi-version" \
    "$ACTUAL_PI_VERSION" "$EXPECTED_PI_VERSION"
fi

# Claude is optional (requires Node.js) — report status without failing.
if [ -x /usr/local/bin/claude ]; then
  echo "  INFO  claude installed: $(/usr/local/bin/claude --version 2>/dev/null || echo 'unknown')"
else
  echo "  INFO  claude not installed (optional — requires Node.js)"
fi

# ---------------------------------------------------------------------------
echo ""
echo "=== 8. Runtime (if services are running) ==="
# ---------------------------------------------------------------------------

if systemctl is-active --quiet opencode 2>/dev/null; then
  expect_ok "opencode is running"    systemctl is-active opencode
  expect_ok "opencode responds on :4096" \
    curl -sf --max-time 3 http://127.0.0.1:4096/health
  # Verify it's actually running as the opencode user.
  OC_PID=$(systemctl show opencode --property=MainPID --value 2>/dev/null)
  if [ -n "$OC_PID" ] && [ "$OC_PID" != "0" ]; then
    OC_USER=$(ps -o user= -p "$OC_PID" 2>/dev/null || echo "unknown")
    expect_eq "opencode process runs as opencode user" "$OC_USER" "opencode"
  fi
else
  echo "  SKIP  opencode not running (start with: systemctl start opencode)"
fi

if systemctl is-active --quiet appx 2>/dev/null; then
  expect_ok "appx is running"        systemctl is-active appx
  # Verify it's actually running as the appx user.
  AX_PID=$(systemctl show appx --property=MainPID --value 2>/dev/null)
  if [ -n "$AX_PID" ] && [ "$AX_PID" != "0" ]; then
    AX_USER=$(ps -o user= -p "$AX_PID" 2>/dev/null || echo "unknown")
    expect_eq "appx process runs as appx user" "$AX_USER" "appx"
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
