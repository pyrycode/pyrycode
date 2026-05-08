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

# Hoisted so the EXIT trap can see it (set -u would error otherwise once
# main() returns and tmpdir is out of scope).
tmpdir=""
cleanup() { [ -n "$tmpdir" ] && rm -rf "$tmpdir"; }
trap cleanup EXIT

# ---------- helpers ----------

err() {
  echo "error: $*" >&2
  exit 1
}

fail_with_code() {
  local code="$1"; shift
  echo "error: $*" >&2
  exit "$code"
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

# ---------- post-install smoke check (#203) ----------
#
# Defends against a class of regression where pyry's control server comes up
# but the supervisor never progresses past `Phase: starting` (see #202). The
# observable is `Uptime: 2562047h47m16.854775807s` — the canonical
# time.Duration(math.MaxInt64).String() sentinel printed when StartedAt is
# still the zero value.

# math.MaxInt64 nanoseconds rendered as time.Duration.String().
PYRY_UPTIME_SENTINEL="2562047h47m16.854775807s"

# Returns 0 if a default-named pyry service is currently active on this host.
# We only consider running services so we don't unintentionally start a unit
# the operator has deliberately disabled. Custom -pyry-name deployments are
# out of scope; they fall through to the skip path.
service_present_darwin() {
  launchctl print "gui/$(id -u)/dev.pyrycode.pyry" 2>/dev/null \
    | grep -q 'state = running'
}

service_present_linux() {
  command -v systemctl >/dev/null 2>&1 || return 1
  # is-system-running returns "offline"/"unknown" when the invoking user has
  # no D-Bus session (CI, ssh-without-lingering); treat as "no service".
  local state
  state=$(systemctl --user is-system-running 2>/dev/null || true)
  case "$state" in
    offline|unknown|"") return 1 ;;
  esac
  systemctl --user is-active --quiet pyry
}

restart_darwin() {
  launchctl kickstart -k "gui/$(id -u)/dev.pyrycode.pyry"
}

restart_linux() {
  systemctl --user restart pyry
}

# Classify `pyry status` output. Reads:
#   $1 — captured stdout+stderr from `pyry status`
#   $2 — exit code from `pyry status`
# Prints a human message and returns the appropriate exit code (0/2/3).
classify_status() {
  local status_out="$1" status_rc="$2"

  if [ "$status_rc" -ne 0 ]; then
    echo "error: supervisor restart did not bring up the control socket within 5s." >&2
    echo "  the service manager accepted the restart but pyry's status endpoint is unreachable." >&2
    echo "  check service-manager logs (\`journalctl --user -u pyry\` / \`tail /tmp/pyry.{out,err}.log\`)." >&2
    echo "  pyry status output:" >&2
    printf '%s\n' "$status_out" | sed 's/^/    /' >&2
    return 3
  fi

  if printf '%s\n' "$status_out" | grep -Eq "^Uptime: +${PYRY_UPTIME_SENTINEL}\$"; then
    echo "error: supervisor failed to start — Started at == 0001-01-01T00:00:00Z, Uptime == ${PYRY_UPTIME_SENTINEL} sentinel detected." >&2
    echo "  see https://github.com/${REPO}/issues/202 for diagnosis steps." >&2
    return 2
  fi

  info "supervisor running normally"
  printf '%s\n' "$status_out" | grep -E '^(Phase|Started at):' || true
  return 0
}

smoke_check() {
  local os="$1"

  local present_fn restart_fn manager
  case "$os" in
    Darwin) present_fn=service_present_darwin; restart_fn=restart_darwin; manager=launchctl ;;
    Linux)  present_fn=service_present_linux;  restart_fn=restart_linux;  manager=systemctl ;;
    *)      return 0 ;;
  esac

  if ! "$present_fn"; then
    info "no running pyry service detected — skipping post-install smoke check"
    echo "    (run \`pyry install-service\` to set one up; see docs/deployment.md)"
    return 0
  fi

  info "restarting pyry service via ${manager}"
  local restart_output restart_rc
  set +e
  restart_output=$("$restart_fn" 2>&1)
  restart_rc=$?
  set -e
  if [ "$restart_rc" -ne 0 ]; then
    fail_with_code 4 "failed to restart pyry service via ${manager} — exit status ${restart_rc}: ${restart_output}"
  fi

  info "probing pyry status"
  local status_out status_rc
  set +e
  status_out=$("${INSTALL_DIR}/pyry" status 2>&1)
  status_rc=$?
  set -e

  set +e
  classify_status "$status_out" "$status_rc"
  local class_rc=$?
  set -e
  if [ "$class_rc" -ne 0 ]; then
    exit "$class_rc"
  fi
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

  local os arch tarball_name url_base
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

  smoke_check "$os"
}

main "$@"
