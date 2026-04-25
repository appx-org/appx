#!/usr/bin/env bash
# deploy/tools-install.sh — install build and runtime tools system-wide.
#
# Must be run as root. Safe to run multiple times (idempotent).
# Installs everything to /usr/local/bin so all users (appx, opencode) have access.
#
# Tools installed:
#   - Go         (version pinned to go.mod — build tool)
#   - Task        (taskfile.dev build runner — build tool)
#   - Node.js 24  (via nvm, pinned to major version — runtime + agents)
#   - OpenCode    (AI agent backend, version pinned to deploy/opencode-version)
#   - Claude Code (Claude CLI for terminal use — self-update: npm update -g @anthropic-ai/claude-code)
#   - uv          (Python version/package manager — self-update: uv self update)
#
# Supported platforms: Ubuntu/Debian (amd64, arm64).

set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "error: must run as root" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Detect architecture.
ARCH=$(dpkg --print-architecture 2>/dev/null || echo "amd64")
case "$ARCH" in
  amd64) GO_ARCH="amd64" ;;
  arm64) GO_ARCH="arm64" ;;
  *) echo "ERROR: unsupported architecture: $ARCH"; exit 1 ;;
esac

# ---------------------------------------------------------------------------
# Task (taskfile.dev build runner)
# ---------------------------------------------------------------------------

if command -v task >/dev/null 2>&1; then
  echo "task already installed: $(task --version 2>/dev/null)"
else
  echo "installing task..."
  curl -1sLf 'https://dl.cloudsmith.io/public/task/task/setup.deb.sh' | bash
  apt-get install -y task
  echo "task installed: $(task --version 2>/dev/null)"
fi

# ---------------------------------------------------------------------------
# Go
# ---------------------------------------------------------------------------

# Read required version from go.mod; fall back to a known-good default.
GO_VERSION="1.24.2"
if [ -f "$REPO_DIR/go.mod" ]; then
  _GO_MOD_VER=$(grep '^go ' "$REPO_DIR/go.mod" | awk '{print $2}')
  if [ -n "$_GO_MOD_VER" ]; then
    GO_VERSION="$_GO_MOD_VER"
  fi
fi

if command -v go >/dev/null 2>&1; then
  echo "go already installed: $(go version 2>/dev/null)"
else
  echo "installing Go ${GO_VERSION}..."
  TMP_GO=$(mktemp)
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz" -o "$TMP_GO"
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "$TMP_GO"
  rm -f "$TMP_GO"
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
  ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
  echo "go installed: $(go version 2>/dev/null)"
fi

# ---------------------------------------------------------------------------
# Node.js (via nvm, pinned to major version 24)
#
# nvm is installed to /usr/local/nvm (system-wide). Binaries are symlinked
# to /usr/local/bin so all users have access without sourcing nvm manually.
# ---------------------------------------------------------------------------

NODE_MAJOR=24
NVM_DIR="/usr/local/nvm"
NVM_VERSION="v0.40.1"

CURRENT_NODE_MAJOR=$(/usr/local/bin/node --version 2>/dev/null | sed 's/^v//' | cut -d. -f1 || echo "0")
if [ "$CURRENT_NODE_MAJOR" = "$NODE_MAJOR" ]; then
  echo "node $NODE_MAJOR already installed: $(/usr/local/bin/node --version)"
else
  echo "installing Node.js $NODE_MAJOR via nvm..."

  # Install nvm to system-wide location if not present.
  if [ ! -s "$NVM_DIR/nvm.sh" ]; then
    mkdir -p "$NVM_DIR"
    export NVM_DIR
    # PROFILE=/dev/null prevents nvm from modifying any shell profile file.
    curl -o- "https://raw.githubusercontent.com/nvm-sh/nvm/${NVM_VERSION}/install.sh" | \
      PROFILE=/dev/null bash
  fi

  # Source nvm for this script session.
  export NVM_DIR
  # shellcheck source=/dev/null
  . "$NVM_DIR/nvm.sh"

  # Install the pinned major version and symlink binaries system-wide.
  nvm install "$NODE_MAJOR"
  NODE_BIN_DIR="$(dirname "$(nvm which $NODE_MAJOR)")"
  for bin in node npm npx; do
    ln -sf "$NODE_BIN_DIR/$bin" "/usr/local/bin/$bin"
  done

  echo "node installed: $(/usr/local/bin/node --version)"
fi

# ---------------------------------------------------------------------------
# OpenCode (pinned to deploy/opencode-version)
# ---------------------------------------------------------------------------

OPENCODE_VERSION=""
if [ -f "$SCRIPT_DIR/opencode-version" ]; then
  OPENCODE_VERSION=$(cat "$SCRIPT_DIR/opencode-version" | tr -d '[:space:]')
fi

if [ -x /usr/local/bin/opencode ]; then
  echo "opencode already installed: $(/usr/local/bin/opencode --version 2>/dev/null || echo 'unknown')"
else
  echo "installing opencode..."
  curl -fsSL https://opencode.ai/install | bash
  echo "opencode installed"
fi

# Ensure opencode is in /usr/local/bin — the installer puts the binary in
# ~/.opencode/bin/ which may not be in PATH for all users.
if [ ! -x /usr/local/bin/opencode ]; then
  for candidate in \
      /root/.opencode/bin/opencode \
      /home/opencode/.opencode/bin/opencode; do
    if [ -x "$candidate" ]; then
      install -m 755 "$candidate" /usr/local/bin/opencode
      echo "copied opencode → /usr/local/bin/opencode"
      break
    fi
  done
fi

# Pin to specific version if specified — skip if already at the right version.
if [ -n "$OPENCODE_VERSION" ]; then
  CURRENT=$(/usr/local/bin/opencode --version 2>/dev/null || echo "unknown")
  WANT=$(echo "$OPENCODE_VERSION" | sed 's/^v//')
  if [ "$CURRENT" = "$WANT" ]; then
    echo "opencode already at $WANT"
  else
    echo "pinning opencode to $OPENCODE_VERSION..."
    /usr/local/bin/opencode upgrade "$OPENCODE_VERSION"
    echo "opencode pinned to $OPENCODE_VERSION"
    # Re-copy in case the upgrade replaced the binary in ~/.opencode/bin/.
    for candidate in \
        /root/.opencode/bin/opencode \
        /home/opencode/.opencode/bin/opencode; do
      if [ -x "$candidate" ]; then
        install -m 755 "$candidate" /usr/local/bin/opencode
        break
      fi
    done
  fi
fi

# ---------------------------------------------------------------------------
# Claude Code (self-update: sudo npm update -g @anthropic-ai/claude-code)
# ---------------------------------------------------------------------------

if command -v claude >/dev/null 2>&1; then
  echo "claude already installed: $(claude --version 2>/dev/null || echo 'unknown')"
else
  echo "installing claude..."
  npm install -g @anthropic-ai/claude-code
  echo "claude installed"
fi

# Ensure claude is in /usr/local/bin (npm -g usually puts it there already).
if command -v claude >/dev/null 2>&1; then
  CLAUDE_PATH=$(command -v claude)
  if [ "$CLAUDE_PATH" != "/usr/local/bin/claude" ] && [ ! -x /usr/local/bin/claude ]; then
    ln -sf "$CLAUDE_PATH" /usr/local/bin/claude
    echo "linked claude → /usr/local/bin/claude"
  fi
fi

# ---------------------------------------------------------------------------
# uv (self-update: uv self update)
# ---------------------------------------------------------------------------

if [ -x /usr/local/bin/uv ]; then
  echo "uv already installed: $(/usr/local/bin/uv --version 2>/dev/null || echo 'unknown')"
else
  echo "installing uv..."
  curl -LsSf https://astral.sh/uv/install.sh | sh
  # Installer puts it in ~/.local/bin/ — copy to system path.
  for candidate in \
      /root/.local/bin/uv \
      /home/opencode/.local/bin/uv; do
    if [ -x "$candidate" ]; then
      install -m 755 "$candidate" /usr/local/bin/uv
      echo "copied uv → /usr/local/bin/uv"
      break
    fi
  done
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

echo ""
echo "Tools install complete."
echo ""
echo "  task:     $(task --version 2>/dev/null || echo 'not found')"
echo "  go:       $(go version 2>/dev/null || echo 'not found')"
echo "  node:     $(/usr/local/bin/node --version 2>/dev/null || echo 'not found')"
echo "  uv:       $(/usr/local/bin/uv --version 2>/dev/null || echo 'not found')"
echo "  opencode: $(/usr/local/bin/opencode --version 2>/dev/null || echo 'not found')"
echo "  claude:   $(claude --version 2>/dev/null || echo 'not found')"
