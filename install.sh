#!/usr/bin/env bash
#
# Hoot-Lb one-click installer.
#
# Usage:
#   bash install.sh
#   curl -fsSL https://raw.githubusercontent.com/Hoot-Code/Hoot-Lb/main/install.sh | bash
#
# Environment overrides:
#   HOOT_LB_INSTALL_DIR  Install directory (default: $HOME/.local/bin)
#   HOOT_LB_REPO         Git repo URL (default: https://github.com/Hoot-Code/Hoot-Lb.git)
#   HOOT_LB_BRANCH       Branch or tag to build (default: main)
#   HOOT_LB_NO_START     Set to 1 to skip auto-starting the binary after install.
#   HOOT_LB_ADMIN_TOKEN  Admin dashboard token (auto-generated if not set).
#   HOOT_LB_VERSION      Explicit version to download (e.g. "0.1.0"). If unset,
#                         the latest release is used.

set -euo pipefail

REPO="${HOOT_LB_REPO:-https://github.com/Hoot-Code/Hoot-Lb.git}"
BRANCH="${HOOT_LB_BRANCH:-main}"
BIN_NAME="hoot-lb"
INSTALL_DIR="${HOOT_LB_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${HOOT_LB_VERSION:-}"

CLEANUP_DIRS=()
CLEANUP_FILES=()

err() { printf 'error: %s\n' "$1" >&2; exit 1; }
info() { printf '==> %s\n' "$1"; }

do_cleanup() {
  [ ${#CLEANUP_DIRS[@]} -gt 0 ] && rm -rf "${CLEANUP_DIRS[@]}"
  [ ${#CLEANUP_FILES[@]} -gt 0 ] && rm -f "${CLEANUP_FILES[@]}"
  return 0
}
trap do_cleanup EXIT

mkdir -p "$INSTALL_DIR"

# --- Detect platform ---

OS="$(uname -s)"
ARCH="$(uname -m)"

case "$OS" in
  Linux)  OS_NAME="linux" ;;
  Darwin) OS_NAME="darwin" ;;
  *)      err "unsupported OS: $OS" ;;
esac

case "$ARCH" in
  x86_64|amd64)  ARCH_NAME="amd64" ;;
  aarch64|arm64) ARCH_NAME="arm64" ;;
  *)             err "unsupported architecture: $ARCH" ;;
esac

# --- Attempt download from GitHub Releases ---

download_release() {
  local tag="$1"
  local asset="${BIN_NAME}_${OS_NAME}_${ARCH_NAME}.tar.gz"
  local url="https://github.com/Hoot-Code/Hoot-Lb/releases/download/v${tag}/${asset}"

  info "attempting download from $url"
  local tmpdir
  tmpdir="$(mktemp -d)"
  CLEANUP_DIRS+=("$tmpdir")

  if curl -fsSL -o "${tmpdir}/${asset}" "$url" 2>/dev/null; then
    tar xzf "${tmpdir}/${asset}" -C "$tmpdir"
    # The binary may be at the top level or inside a subdirectory.
    local found=""
    found="$(find "$tmpdir" -maxdepth 2 -name "$BIN_NAME" -type f | head -1)"
    if [ -n "$found" ]; then
      mv "$found" "${INSTALL_DIR}/${BIN_NAME}"
      chmod +x "${INSTALL_DIR}/${BIN_NAME}"
      info "downloaded v${tag} release binary"
      return 0
    fi
  fi
  return 1
}

DOWNLOADED=0

if [ -n "$VERSION" ]; then
  if download_release "$VERSION"; then
    DOWNLOADED=1
  else
    info "download of v${VERSION} failed, falling back to build-from-source"
  fi
else
  # Try to find the latest release tag via GitHub API.
  LATEST=""
  if command -v curl >/dev/null 2>&1; then
    LATEST=$(curl -fsSL "https://api.github.com/repos/Hoot-Code/Hoot-Lb/releases/latest" 2>/dev/null \
      | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"v\?\([^"]*\)".*/\1/' || true)
  fi
  if [ -n "$LATEST" ]; then
    if download_release "$LATEST"; then
      DOWNLOADED=1
    else
      info "download of latest release (v${LATEST}) failed, falling back to build-from-source"
    fi
  else
    info "no release found, falling back to build-from-source"
  fi
fi

# --- Fallback: build from source ---

if [ "$DOWNLOADED" -eq 0 ]; then
  command -v go >/dev/null 2>&1 || err "go is required for build-from-source but not found (https://go.dev/dl/)"

  SCRIPT_DIR=""
  if [ -n "${BASH_SOURCE[0]:-}" ]; then
    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  fi
  if [ -n "$SCRIPT_DIR" ] && [ -f "$SCRIPT_DIR/go.mod" ] && [ -d "$SCRIPT_DIR/cmd/lb" ]; then
    SRC_DIR="$SCRIPT_DIR"
    info "building from local source ($SRC_DIR)"
  else
    SRC_DIR="$(mktemp -d)"
    CLEANUP_DIRS+=("$SRC_DIR")
    command -v git >/dev/null 2>&1 || err "git is required but not found"
    info "cloning $REPO ($BRANCH)"
    if ! git clone --depth 1 --branch "$BRANCH" "$REPO" "$SRC_DIR"; then
      err "git clone failed"
    fi
  fi

  info "building $BIN_NAME"
  if ! (cd "$SRC_DIR" && go build -o "$INSTALL_DIR/$BIN_NAME" ./cmd/lb/); then
    err "go build failed"
  fi
fi

chmod +x "$INSTALL_DIR/$BIN_NAME"

if ! command -v "$BIN_NAME" >/dev/null 2>&1; then
  info "installed, but $INSTALL_DIR is not on your PATH yet."
  info "add this to your shell profile:  export PATH=\"$INSTALL_DIR:\$PATH\""
fi

info "done. installed version:"
"$INSTALL_DIR/$BIN_NAME" -version || true

# --- Auto-start (skip with HOOT_LB_NO_START=1) ---

if [ "${HOOT_LB_NO_START:-0}" = "1" ]; then
  exit 0
fi

# Find the example config — it may be in the local source dir or
# the cloned repo dir.
CONFIG=""
if [ -n "${SRC_DIR:-}" ] && [ -f "$SRC_DIR/examples/config.yaml" ]; then
  CONFIG="$SRC_DIR/examples/config.yaml"
elif [ -f "$SCRIPT_DIR/examples/config.yaml" ]; then
  CONFIG="$SCRIPT_DIR/examples/config.yaml"
fi

if [ -z "$CONFIG" ]; then
  info "example config not found, skipping auto-start."
  exit 0
fi

# --- Generate admin token if not set ---

TOKEN="${HOOT_LB_ADMIN_TOKEN:-}"
if [ -z "$TOKEN" ]; then
  if command -v openssl >/dev/null 2>&1; then
    TOKEN=$(openssl rand -hex 32)
  else
    TOKEN=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
  fi
  info "generated admin token"
fi
export HOOT_LB_ADMIN_TOKEN="$TOKEN"

# --- Extract listener addresses and admin address from the example config ---

LISTENER_ADDRESSES=$(
  awk '
    /^listeners:/ { in_listeners=1; next }
    in_listeners && /^[^ ]/ { in_listeners=0 }
    in_listeners && /^[[:space:]]+address:/ {
      gsub(/^[[:space:]]+address:[[:space:]]*"?/, "")
      gsub(/"?[[:space:]]*$/, "")
      print
    }
  ' "$CONFIG"
)

ADMIN_ADDRESS=$(
  awk '
    /^global:/ { in_global=1; next }
    in_global && /^[^ ]/ { in_global=0 }
    in_global && /admin:/ { in_admin=1; next }
    in_admin && /^[^ ]/ { in_admin=0 }
    in_admin && /^[[:space:]]+address:/ {
      gsub(/^[[:space:]]+address:[[:space:]]*"?/, "")
      gsub(/"?[[:space:]]*$/, "")
      print
      exit
    }
  ' "$CONFIG"
)

if [ -z "$LISTENER_ADDRESSES" ]; then
  info "no listener addresses found in example config, skipping auto-start."
  exit 0
fi

# --- Check for port conflicts ---

PORT_CONFLICT=0
check_port() {
  local addr="$1"
  local port="${addr##*:}"
  if command -v nc >/dev/null 2>&1; then
    if nc -z 127.0.0.1 "$port" 2>/dev/null; then
      return 1
    fi
  elif command -v lsof >/dev/null 2>&1; then
    if lsof -i ":$port" -sTCP:LISTEN >/dev/null 2>&1; then
      return 1
    fi
  elif (echo >/dev/tcp/127.0.0.1/"$port") 2>/dev/null; then
    return 1
  fi
  return 0
}

while IFS= read -r addr; do
  if ! check_port "$addr"; then
    PORT_CONFLICT=1
    info "port ${addr##*:} (from listener $addr) is already in use — skipping auto-start."
    break
  fi
done <<< "$LISTENER_ADDRESSES"

if [ "$PORT_CONFLICT" -eq 0 ] && [ -n "$ADMIN_ADDRESS" ]; then
  if ! check_port "$ADMIN_ADDRESS"; then
    PORT_CONFLICT=1
    info "port ${ADMIN_ADDRESS##*:} (admin $ADMIN_ADDRESS) is already in use — skipping auto-start."
  fi
fi

if [ "$PORT_CONFLICT" -eq 1 ]; then
  exit 0
fi

# The example config references cert.pem/key.pem for TLS listeners.
# Generate self-signed certs in the working directory (where the binary
# runs) so relative paths in the config resolve correctly.
CERT_PEM="./cert.pem"
KEY_PEM="./key.pem"
if [ ! -f "$CERT_PEM" ] || [ ! -f "$KEY_PEM" ]; then
  if command -v openssl >/dev/null 2>&1; then
    info "generating self-signed TLS certs for example config"
    openssl req -x509 -newkey rsa:2048 -keyout "$KEY_PEM" -out "$CERT_PEM" \
      -days 1 -nodes -subj '/CN=localhost' 2>/dev/null
    CLEANUP_FILES+=("$CERT_PEM" "$KEY_PEM")
  else
    info "openssl not found, skipping auto-start (TLS certs missing)."
    exit 0
  fi
fi

LOG_FILE="/tmp/hoot-lb.log"
nohup "$INSTALL_DIR/$BIN_NAME" -config "$CONFIG" >"$LOG_FILE" 2>&1 &
LB_PID=$!

# Brief pause to let the process either start or fail.
sleep 1

if ! kill -0 "$LB_PID" 2>/dev/null; then
  info "hoot-lb failed to start. Log output:"
  cat "$LOG_FILE"
  exit 0
fi

info "hoot-lb started successfully."
echo ""
echo "  PID:            $LB_PID"
echo "  Log file:       $LOG_FILE"
echo "  Proxy listeners:"
while IFS= read -r addr; do
  echo "    $addr"
done <<< "$LISTENER_ADDRESSES"
echo ""
if [ -n "$ADMIN_ADDRESS" ]; then
  echo "  Dashboard:      http://$ADMIN_ADDRESS"
  echo "  Admin token:    $TOKEN"
  echo ""
fi
echo "  Stop:           kill $LB_PID"
echo "  Tail log:       tail -f $LOG_FILE"
echo ""
echo "  NOTE: The example config's backends are placeholder addresses"
echo "  (10.0.0.x etc.) that won't resolve to real servers. Proxied"
echo "  requests may fail. The dashboard works independently of backend"
echo "  connectivity."
