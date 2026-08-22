#!/usr/bin/env sh
#
# Single-shot installer for trond.
#
#   curl -fsSL https://raw.githubusercontent.com/tronprotocol/tron-deployment/master/scripts/install.sh | sh
#
# Optional environment variables:
#   TROND_VERSION   = explicit release tag (default: latest)
#   TROND_DEST      = install directory (default: /usr/local/bin or ~/.local/bin)
#
# We try /usr/local/bin first (system-wide install with sudo); if not
# writable we fall back to ~/.local/bin and warn the user to add it to
# PATH. No assumed sudo invocation — the user is in control.

set -eu

REPO="tronprotocol/tron-deployment"
VERSION="${TROND_VERSION:-}"
DEST="${TROND_DEST:-}"

# --- platform detection ---------------------------------------------------

uname_s=$(uname -s)
uname_m=$(uname -m)

case "$uname_s" in
    Linux*)  os="linux"  ;;
    Darwin*) os="darwin" ;;
    *)
        echo "trond: unsupported OS: $uname_s" >&2
        exit 1
        ;;
esac

case "$uname_m" in
    x86_64|amd64) arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *)
        echo "trond: unsupported arch: $uname_m" >&2
        exit 1
        ;;
esac

# --- resolve version ------------------------------------------------------

if [ -z "$VERSION" ]; then
    # /releases/latest returns only the newest NON-prerelease. goreleaser is
    # configured with `prerelease: auto`, so a tag like v0.1.0-alpha is filed
    # as a prerelease and this endpoint 404s even though releases exist —
    # hence the failure below names that case instead of blaming the network.
    # Installing a prerelease stays opt-in via TROND_VERSION; a `curl | sh`
    # should not pick one up silently.
    if ! VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
        | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1); then
        echo "trond: no stable release found (pre-releases are not installed by" >&2
        echo "       default). Check https://github.com/${REPO}/releases and set" >&2
        echo "       TROND_VERSION=<tag> to install a specific one." >&2
        exit 1
    fi
    if [ -z "$VERSION" ]; then
        echo "trond: no releases published yet; set TROND_VERSION to install a pre-release tag" >&2
        exit 1
    fi
fi

# Strip leading "v" for the tarball naming convention used by goreleaser.
ver_no_v=${VERSION#v}

# --- pick install dir -----------------------------------------------------

if [ -z "$DEST" ]; then
    if [ -w /usr/local/bin ]; then
        DEST=/usr/local/bin
    elif [ -d "$HOME/.local/bin" ] || mkdir -p "$HOME/.local/bin" 2>/dev/null; then
        DEST="$HOME/.local/bin"
    else
        echo "trond: cannot write to /usr/local/bin or ~/.local/bin; set TROND_DEST" >&2
        exit 1
    fi
fi

# --- download + extract + verify checksum --------------------------------

tarball="trond_${ver_no_v}_${os}_${arch}.tar.gz"
url="https://github.com/${REPO}/releases/download/${VERSION}/${tarball}"
checksums_url="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "trond: downloading ${url}"
curl -fsSL "$url" -o "${tmp}/${tarball}"

# Best-effort SHA256 verification — bail loudly if it doesn't match.
if curl -fsSL "$checksums_url" -o "${tmp}/checksums.txt" 2>/dev/null; then
    expected=$(awk -v f="$tarball" '$2==f {print $1}' "${tmp}/checksums.txt")
    if [ -n "$expected" ]; then
        if command -v sha256sum >/dev/null 2>&1; then
            actual=$(sha256sum "${tmp}/${tarball}" | awk '{print $1}')
        else
            actual=$(shasum -a 256 "${tmp}/${tarball}" | awk '{print $1}')
        fi
        if [ "$expected" != "$actual" ]; then
            echo "trond: SHA256 mismatch — refusing to install" >&2
            echo "  expected: $expected" >&2
            echo "  actual:   $actual" >&2
            exit 1
        fi
        echo "trond: SHA256 verified"
    fi
fi

tar -xzf "${tmp}/${tarball}" -C "${tmp}"
install_path="${DEST}/trond"

if [ -e "$install_path" ]; then
    rm -f "$install_path"
fi

mv "${tmp}/trond" "$install_path"
chmod 755 "$install_path"

echo
echo "trond ${VERSION} installed to $install_path"
"$install_path" version

# Helpful PATH hint if we fell back to user-local install.
case ":$PATH:" in
    *":$DEST:"*) ;;
    *)
        echo
        echo "Note: $DEST is not in PATH. Add this to your shell profile:"
        echo "  export PATH=\"\$PATH:$DEST\""
        ;;
esac
