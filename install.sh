#!/usr/bin/env sh
# clawtop installer. Downloads the latest release binaries for the current
# OS/arch and drops them into ~/.local/bin.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/leonardorifeli/clawtop/main/install.sh | sh
#   curl -fsSL .../install.sh | sh -s -- daemon        # daemon binary only
#   curl -fsSL .../install.sh | sh -s -- viewer        # TUI binary only
#   curl -fsSL .../install.sh | sh -s -- both          # default
#
# Honors $CLAWTOP_VERSION (defaults to latest) and $CLAWTOP_BIN_DIR
# (defaults to ~/.local/bin).

set -eu

REPO="leonardorifeli/clawtop"
WHAT="${1:-both}"
BIN_DIR="${CLAWTOP_BIN_DIR:-$HOME/.local/bin}"
VERSION="${CLAWTOP_VERSION:-}"

die() { printf '%s\n' "error: $*" >&2; exit 1; }
info() { printf '==> %s\n' "$*"; }

case "$WHAT" in
  daemon|viewer|both) ;;
  *) die "unknown target '$WHAT' (expected: daemon, viewer, both)" ;;
esac

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
  linux|darwin) ;;
  *) die "unsupported OS: $OS" ;;
esac

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported arch: $ARCH" ;;
esac

# Resolve latest version if not pinned.
if [ -z "$VERSION" ]; then
  info "Looking up latest release of $REPO..."
  VERSION="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -n1)"
  [ -n "$VERSION" ] || die "could not determine latest version"
fi

TARBALL="clawtop_${VERSION}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/v${VERSION}/$TARBALL"

mkdir -p "$BIN_DIR"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

info "Downloading $TARBALL..."
curl -fsSL "$URL" -o "$TMP/clawtop.tar.gz" || die "download failed: $URL"
tar -xzf "$TMP/clawtop.tar.gz" -C "$TMP"

install_bin() {
  src="$TMP/$1"
  dst="$BIN_DIR/$1"
  [ -f "$src" ] || die "missing binary in archive: $1"
  install -m 0755 "$src" "$dst"
  info "Installed $dst"
}

case "$WHAT" in
  daemon) install_bin clawtopd ;;
  viewer) install_bin clawtop ;;
  both)   install_bin clawtopd; install_bin clawtop ;;
esac

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) info "Note: $BIN_DIR is not on \$PATH. Add it to your shell rc." ;;
esac

cat <<EOF

Next steps:
  - Daemon hosts: see https://github.com/$REPO/blob/main/deploy/INSTALL.md#2-daemon
  - Viewer host:  see https://github.com/$REPO/blob/main/deploy/INSTALL.md#3-viewer

EOF
