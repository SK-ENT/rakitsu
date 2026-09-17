#!/bin/sh
# Rakitsu installer / uninstaller
# Install:   curl -fsSL https://raw.githubusercontent.com/SK-ENT/rakitsu/main/install.sh | sh
# Uninstall: curl -fsSL https://raw.githubusercontent.com/SK-ENT/rakitsu/main/install.sh | sh -s -- --uninstall
set -e

REPO="SK-ENT/rakitsu"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

# Uninstall mode
if [ "$1" = "--uninstall" ] || [ "$1" = "uninstall" ]; then
  CONFIRMED=0
  for arg in "$@"; do
    case "$arg" in
      --yes|-y) CONFIRMED=1 ;;
    esac
  done
  echo "This will remove:"
  echo "  ${INSTALL_DIR}/rakitsu"
  echo "  ${HOME}/.rakitsu (all sessions, configs, and memory — cannot be undone)"
  if [ "$CONFIRMED" -eq 0 ]; then
    # Piped through `curl | sh`, stdin is the script itself, not a terminal
    # — read the confirmation from /dev/tty directly instead. The device
    # can exist but still fail to open (no controlling terminal at all,
    # e.g. cron/CI) — `read` as an `if` condition doesn't trigger `set -e`
    # on failure, so a failed open falls through to the --yes requirement
    # below instead of killing the script with a raw error.
    if [ -r /dev/tty ] && { printf "Continue? [y/N] " >/dev/tty; read -r REPLY < /dev/tty; } 2>/dev/null; then
      case "$REPLY" in
        y|Y|yes|YES) ;;
        *) echo "Aborted."; exit 1 ;;
      esac
    else
      echo ""
      echo "No interactive terminal available — re-run with --yes to confirm:"
      echo "  curl -fsSL https://raw.githubusercontent.com/SK-ENT/rakitsu/main/install.sh | sh -s -- --uninstall --yes"
      exit 1
    fi
  fi
  echo "Uninstalling Rakitsu..."
  rm -f "$INSTALL_DIR/rakitsu"
  rm -rf "$HOME/.rakitsu"
  echo "Removed $INSTALL_DIR/rakitsu"
  echo "Removed ~/.rakitsu (sessions, configs)"
  echo "Done."
  exit 0
fi

# Detect OS
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  linux)  OS="linux" ;;
  darwin) OS="darwin" ;;
  *)      echo "Unsupported OS: $OS"; exit 1 ;;
esac

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)             echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

BINARY="rakitsu-${OS}-${ARCH}"
echo "Detected: ${OS}/${ARCH}"

# Get latest release tag.
# Uses /releases (plural) and picks the first entry instead of /releases/latest,
# because /releases/latest skips prereleases and early alpha/beta tags would be invisible.
# VERSION env var overrides auto-detection (e.g. VERSION=v0.1.0-alpha.3 curl ... | sh).
if [ -n "$VERSION" ]; then
  LATEST="$VERSION"
else
  LATEST=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases" | grep '"tag_name"' | head -1 | sed -E 's/.*"([^"]+)".*/\1/')
fi
if [ -z "$LATEST" ]; then
  echo "Could not determine latest release. Check https://github.com/${REPO}/releases"
  exit 1
fi
echo "Latest release: ${LATEST}"

# Download into a private temp dir (not predictable /tmp paths — avoids a
# symlink/race target) and clean it up on exit no matter how the script ends.
umask 077
TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/rakitsu.XXXXXX")
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM
BINARY_FILE="$TMP_DIR/rakitsu"
CHECKSUM_FILE="$TMP_DIR/checksums.txt"

URL="https://github.com/${REPO}/releases/download/${LATEST}/${BINARY}"
echo "Downloading ${URL}..."
curl -fsSL -o "$BINARY_FILE" "$URL"

# Verify checksum against the release's checksums.txt.
CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${LATEST}/checksums.txt"
echo "Verifying checksum..."
curl -fsSL -o "$CHECKSUM_FILE" "$CHECKSUMS_URL"
EXPECTED=$(awk -v binary="$BINARY" '$2 == binary || $2 == ("*" binary) { print $1; exit }' "$CHECKSUM_FILE")
if [ -z "$EXPECTED" ]; then
  echo "Could not find a checksum for ${BINARY} in checksums.txt — aborting."
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL=$(sha256sum "$BINARY_FILE" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL=$(shasum -a 256 "$BINARY_FILE" | awk '{print $1}')
else
  echo "No sha256sum or shasum found — cannot verify checksum, aborting."
  exit 1
fi
if [ "$EXPECTED" != "$ACTUAL" ]; then
  echo "Checksum mismatch for ${BINARY}!"
  echo "  expected: $EXPECTED"
  echo "  actual:   $ACTUAL"
  exit 1
fi
echo "Checksum OK."

# Install. chmod 755 explicitly (not just +x) — the umask set above for the
# temp download dir would otherwise leave this owner-only, which is wrong
# for a globally installed CLI binary. Same for the directory itself: a
# freshly-created $INSTALL_DIR inherits the umask too (harmless for the
# $HOME/.local/bin default, real for a shared path like /opt/rakitsu/bin
# under sudo — `|| true` because chmod legitimately fails, and should be
# ignored, when INSTALL_DIR already exists and is owned by someone else,
# e.g. the always-present /usr/local/bin).
mkdir -p "$INSTALL_DIR"
chmod 755 "$INSTALL_DIR" 2>/dev/null || true
# Copy-then-rename instead of a direct mv: $TMP_DIR and $INSTALL_DIR can be
# on different filesystems, which silently degrades `mv` to copy+unlink —
# an install interrupted mid-copy would then leave a truncated binary where
# a working one used to be. Renaming within $INSTALL_DIR is always atomic.
cp "$BINARY_FILE" "$INSTALL_DIR/rakitsu.new"
chmod 755 "$INSTALL_DIR/rakitsu.new"
mv "$INSTALL_DIR/rakitsu.new" "$INSTALL_DIR/rakitsu"
echo "Installed to ${INSTALL_DIR}/rakitsu"

# macOS-only: release binaries are ad-hoc signed (Go's default), not
# notarized with a real Apple Developer ID — Gatekeeper can silently kill
# a freshly downloaded one on first run with no dialog, just a bare
# "killed" from the shell. Not every macOS setup hits this, but there's no
# way to know in advance, so the note is unconditional on this OS rather
# than only printed after a failure this script has no way to detect.
if [ "$OS" = "darwin" ]; then
  echo ""
  echo "macOS note: if running 'rakitsu' gets silently killed, that's"
  echo "Gatekeeper blocking an unsigned/unnotarized binary, not a broken"
  echo "install. Try:"
  echo "  xattr -cr ${INSTALL_DIR}/rakitsu"
  echo "If it's still killed after that:"
  echo "  sudo spctl --add ${INSTALL_DIR}/rakitsu"
  echo "or System Settings -> Privacy & Security -> scroll down to the"
  echo "blocked-item notice for rakitsu -> Open Anyway."
fi

# Check PATH (colon-delimited exact match, not a substring search)
case ":${PATH:-}:" in
  *:"$INSTALL_DIR":*)
    ;;
  *)
    echo ""
    echo "Add to your PATH:"
    echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
    echo ""
    echo "Then restart your shell or run the export command above."
    ;;
esac

echo ""
echo "Run 'rakitsu quickstart' to get started!"
echo "Or 'rakitsu serve' to open the web UI."
