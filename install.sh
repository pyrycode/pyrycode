#!/usr/bin/env bash
#
# pyry installer — downloads a pyrycode release from GitHub, verifies its
# SHA-256 checksum, and places the binary in your PATH.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/pyrycode/pyrycode/main/install.sh | bash
#
# Environment variables:
#   PYRY_VERSION       Pin a specific release (default: latest). Example: v0.5.0
#   PYRY_INSTALL_DIR   Where to drop the pyry binary (default: ~/.local/bin).
#

set -euo pipefail

REPO="pyrycode/pyrycode"
INSTALL_DIR="${PYRY_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${PYRY_VERSION:-}"

# ---------- helpers ----------

err() {
  echo "error: $*" >&2
  exit 1
}

info() {
  echo "==> $*"
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || err "missing required command: $1"
}

detect_os() {
  case "$(uname -s)" in
    Linux*)  echo "Linux"  ;;
    Darwin*) echo "Darwin" ;;
    *) err "unsupported OS: $(uname -s). pyry supports Linux and macOS only." ;;
  esac
}

detect_arch() {
  # Match goreleaser's naming: x86_64 / arm64.
  case "$(uname -m)" in
    x86_64|amd64)   echo "x86_64" ;;
    arm64|aarch64)  echo "arm64"  ;;
    *) err "unsupported arch: $(uname -m). pyry supports amd64 and arm64." ;;
  esac
}

latest_version() {
  curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name":' \
    | head -n1 \
    | cut -d'"' -f4
}

verify_checksum() {
  local file="$1" checksums="$2" expected actual
  expected=$(grep " $(basename "$file")\$" "$checksums" | awk '{print $1}')
  [ -n "$expected" ] || err "checksum for $(basename "$file") not found in checksums.txt"

  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$file" | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "$file" | awk '{print $1}')
  else
    err "neither sha256sum nor shasum found; cannot verify checksum"
  fi

  [ "$expected" = "$actual" ] || err "checksum mismatch for $(basename "$file") (expected $expected, got $actual)"
}

# ---------- main ----------

main() {
  require_cmd curl
  require_cmd tar

  local os arch tarball_name url_base tmpdir
  os=$(detect_os)
  arch=$(detect_arch)

  if [ -z "$VERSION" ]; then
    VERSION=$(latest_version)
    [ -n "$VERSION" ] || err "could not determine latest version (no published releases yet?). Set PYRY_VERSION=vX.Y.Z to pin."
  fi

  # goreleaser renders {{.Version}} without the leading 'v'.
  local version_no_v="${VERSION#v}"
  tarball_name="pyry_${version_no_v}_${os}_${arch}.tar.gz"
  url_base="https://github.com/${REPO}/releases/download/${VERSION}"

  info "installing pyry ${VERSION} (${os}/${arch}) to ${INSTALL_DIR}"

  tmpdir=$(mktemp -d)
  trap 'rm -rf "$tmpdir"' EXIT

  info "downloading ${tarball_name}"
  curl -fsSL -o "${tmpdir}/${tarball_name}"  "${url_base}/${tarball_name}"
  curl -fsSL -o "${tmpdir}/checksums.txt"    "${url_base}/checksums.txt"

  info "verifying checksum"
  verify_checksum "${tmpdir}/${tarball_name}" "${tmpdir}/checksums.txt"

  info "extracting"
  tar -xzf "${tmpdir}/${tarball_name}" -C "${tmpdir}" pyry

  mkdir -p "${INSTALL_DIR}"
  install -m 0755 "${tmpdir}/pyry" "${INSTALL_DIR}/pyry"

  info "installed: ${INSTALL_DIR}/pyry"

  # PATH advisory.
  case ":$PATH:" in
    *":${INSTALL_DIR}:"*) ;;
    *)
      echo
      echo "warning: ${INSTALL_DIR} is not on your PATH."
      echo "  Add it with one of:"
      echo "    echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.bashrc"
      echo "    echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.zshrc"
      echo
      ;;
  esac

  echo
  "${INSTALL_DIR}/pyry" version || true
  echo
  info "next: see https://github.com/${REPO}/blob/main/docs/deployment.md for service-mode setup"
}

main "$@"
