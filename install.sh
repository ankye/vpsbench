#!/bin/sh
# vpsbench one-click installer / 一键安装脚本
# Usage: curl -fsSL https://raw.githubusercontent.com/ankye/vpsbench/main/install.sh | sh
# Detects OS & architecture, downloads the matching binary from GitHub Releases.

set -e

REPO="ankye/vpsbench"
VERSION="${VPSBENCH_VERSION:-latest}"

say() { printf '%s\n' "$*"; }

# ---- detect OS ----
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  linux)  OS=linux ;;
  darwin) OS=darwin ;;
  *)
    say "✗ unsupported OS: $(uname -s) (linux/darwin only)"
    exit 1
    ;;
esac

# ---- detect arch ----
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64)   ARCH=amd64 ;;
  aarch64|arm64)  ARCH=arm64 ;;
  i386|i686)      ARCH=386 ;;
  armv6l|armv7l|arm) ARCH=arm ;;
  *)
    say "✗ unsupported architecture: $ARCH"
    exit 1
    ;;
esac

ASSET="vpsbench-${OS}-${ARCH}"
if [ "$OS" = "darwin" ] && [ "$ARCH" = "arm64" ]; then
  # macOS arm64 binary also runs natively on Apple Silicon (built as arm64)
  :
fi

say "► detected: ${OS}/${ARCH}  →  ${ASSET}"

# ---- prefer versioned URL, resolve 'latest' via redirect ----
BASE="https://github.com/${REPO}/releases"
if [ "$VERSION" = "latest" ]; then
  URL="${BASE}/latest/download/${ASSET}"
  SUM_URL="${BASE}/latest/download/SHA256SUMS"
else
  URL="${BASE}/download/${VERSION}/${ASSET}"
  SUM_URL="${BASE}/download/${VERSION}/SHA256SUMS"
fi

# ---- downloader ----
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

if command -v curl >/dev/null 2>&1; then
  FETCH="curl -fsSL"
elif command -v wget >/dev/null 2>&1; then
  FETCH="wget -qO-"
else
  say "✗ need curl or wget"
  exit 1
fi

say "► downloading $URL"
$FETCH "$URL" -o "$TMP/$ASSET" 2>/dev/null || curl -fsSL "$URL" -o "$TMP/$ASSET" || wget -q -O "$TMP/$ASSET" "$URL"

# ---- checksum (best effort) ----
if $FETCH "$SUM_URL" -o "$TMP/SHA256SUMS" 2>/dev/null; then
  want=$(grep " $ASSET\$" "$TMP/SHA256SUMS" 2>/dev/null | awk '{print $1}')
  if [ -n "$want" ]; then
    if command -v sha256sum >/dev/null 2>&1; then
      got=$(sha256sum "$TMP/$ASSET" | awk '{print $1}')
    elif command -v shasum >/dev/null 2>&1; then
      got=$(shasum -a 256 "$TMP/$ASSET" | awk '{print $1}')
    else
      got=""
    fi
    if [ -n "$got" ]; then
      if [ "$got" = "$want" ]; then
        say "► checksum ok"
      else
        say "✗ checksum mismatch! want=$want got=$got"
        exit 1
      fi
    fi
  fi
fi

chmod +x "$TMP/$ASSET"

# ---- pick install dir ----
if [ -n "$INSTALL_DIR" ]; then
  DEST="$INSTALL_DIR"
elif [ -w /usr/local/bin ] 2>/dev/null; then
  DEST="/usr/local/bin"
else
  DEST="$HOME/.local/bin"
  mkdir -p "$DEST"
fi

BIN="$DEST/vpsbench"
if [ "$OS" = "darwin" ] && [ "$DEST" = "/usr/local/bin" ] && [ ! -w /usr/local/bin ]; then
  DEST="$HOME/.local/bin"; mkdir -p "$DEST"; BIN="$DEST/vpsbench"
fi

mv "$TMP/$ASSET" "$BIN"

say "► installed: $BIN"
case "$BIN" in
  /usr/local/bin/*) ;;
  *)
    case ":$PATH:" in
      *":$DEST:"*) ;;
      *) say "  NOTE: add $DEST to PATH:  export PATH=\"\$PATH:$DEST\"" ;;
    esac
    ;;
esac

"$BIN" help >/dev/null 2>&1 || true
VER=$("$BIN" cpu -h 2>/dev/null | head -1 || true)
say ""
say "✓ done! try:"
say "    vpsbench server -addr :8300 -token YOUR_SECRET     # on the VPS"
say "    vpsbench all   http://<vps-ip>:8300 -lang zh       # from anywhere"
