#!/usr/bin/env bash
#
# Dev-build installer for the operator's own daemon. Builds the checked-out
# tree with a `dev-<sha>` version stamp, swaps it into ~/.local/bin/pyry, keeps
# a rollback copy, and bounces the managed daemon so the new code is actually
# running.
#
# Usage (via the Makefile):
#   make install                 build HEAD, swap, restart the daemon
#   make install NO_RESTART=1    swap only; the daemon runs old code until bounced
#   make rollback                put pyry.prev back and restart
#
# Environment variables:
#   PYRY_INSTALL_DIR   Where the binary lives (default: ~/.local/bin).
#   NO_RESTART         Set to 1 to skip the daemon restart.
#
# Why this is a script and not a recipe. On 2026-09-02 a hand swap written as
# `cp new pyry` over the live file killed the daemon for five minutes. On Apple
# silicon, overwriting a binary in place while a process runs from it leaves a
# file the kernel refuses to exec (launchd reports OS_REASON_CODESIGNING). A
# second run of the same recipe then overwrote the rollback copy with the build
# it was meant to protect against. Both are ruled out here by construction:
# the new build lands on the path by rename, never by overwrite, and pyry.prev
# only rotates when the installed bytes actually change.
#
# This does not run any gate. `make preship` is the pre-swap gate and stays a
# separate, deliberate step.

set -euo pipefail

INSTALL_DIR="${PYRY_INSTALL_DIR:-$HOME/.local/bin}"
TARGET="$INSTALL_DIR/pyry"
PREV="$INSTALL_DIR/pyry.prev"
LAUNCHD_LABEL="dev.pyrycode.pyry"

# Hoisted so the EXIT trap can see it under set -u.
tmpfile=""
cleanup() { if [ -n "$tmpfile" ]; then rm -f "$tmpfile"; fi; }
trap cleanup EXIT

err()  { echo "error: $*" >&2; exit 1; }
info() { echo "==> $*"; }

repo_root() {
  cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd
}

# dev-<short sha>, plus -dirty when tracked files have uncommitted changes.
# Matches the stamps already in service (dev-663f18e, dev-c332453f).
version_stamp() {
  local sha dirty=""
  sha="$(git rev-parse --short HEAD)"
  if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
    dirty="-dirty"
  fi
  echo "dev-${sha}${dirty}"
}

# Place a file on $TARGET by rename. The running daemon keeps its old inode;
# the path gets a fresh one. This is the same move `go build -o` and
# `pyry update` make, and the only one that is safe while the daemon runs.
place() {
  local src="$1"
  mv -f "$src" "$TARGET"
}

# Copy the installed binary to a dated backup. Reading the running file is
# fine; only writing over it is not.
backup_installed() {
  local dated
  dated="$INSTALL_DIR/pyry.$(date +%Y%m%d-%H%M)"
  cp -p "$TARGET" "$dated"
  echo "$dated"
}

# --- daemon control -----------------------------------------------------

# Prints the argv that restarts the managed daemon, or nothing when no
# service is installed. Mirrors internal/update/restart.go.
restart_cmd() {
  if [ -f "$HOME/Library/LaunchAgents/$LAUNCHD_LABEL.plist" ]; then
    echo "launchctl kickstart -k gui/$(id -u)/$LAUNCHD_LABEL"
  elif [ -f "$HOME/.config/systemd/user/pyry.service" ]; then
    echo "systemctl --user restart pyry"
  fi
}

daemon_pid() {
  if command -v launchctl >/dev/null 2>&1; then
    launchctl print "gui/$(id -u)/$LAUNCHD_LABEL" 2>/dev/null \
      | awk '/^[[:space:]]*pid = /{print $3; exit}'
  elif command -v systemctl >/dev/null 2>&1; then
    systemctl --user show -p MainPID --value pyry 2>/dev/null
  fi
}

# Restart, then prove the new process is up: the pid must change and
# `pyry status` must answer. The socket takes a second or two to bind after
# a restart, so poll rather than fail on the first miss.
restart_daemon() {
  local cmd old_pid new_pid
  cmd="$(restart_cmd)"
  if [ -z "$cmd" ]; then
    info "no managed pyry service found; restart your daemon yourself"
    return 0
  fi

  old_pid="$(daemon_pid || true)"
  info "restarting the daemon: $cmd"
  $cmd

  for _ in $(seq 1 30); do
    sleep 0.5
    new_pid="$(daemon_pid || true)"
    if [ -n "$new_pid" ] && [ "$new_pid" != "$old_pid" ] \
       && "$TARGET" status >/dev/null 2>&1; then
      info "daemon is up on pid $new_pid (was ${old_pid:-none})"
      "$TARGET" status
      echo
      echo "    Relaunch the desktop app now. A daemon restart ends its"
      echo "    session for good; the app never re-handshakes on its own."
      return 0
    fi
  done

  echo "error: the daemon did not come back within 15s." >&2
  if command -v launchctl >/dev/null 2>&1; then
    launchctl print "gui/$(id -u)/$LAUNCHD_LABEL" 2>/dev/null \
      | grep -E "last exit|state = " >&2 || true
  fi
  echo "       Roll back with: make rollback" >&2
  exit 1
}

maybe_restart() {
  if [ "${NO_RESTART:-0}" = "1" ]; then
    info "NO_RESTART=1: binary swapped, daemon still runs the old code until bounced"
    local cmd
    cmd="$(restart_cmd)"
    [ -n "$cmd" ] && echo "    bounce it with: $cmd"
    return 0
  fi
  restart_daemon
}

# --- commands -----------------------------------------------------------

cmd_install() {
  local root version dated
  root="$(repo_root)"
  cd "$root"
  command -v go >/dev/null 2>&1 || err "go not found on PATH"
  mkdir -p "$INSTALL_DIR"

  version="$(version_stamp)"
  info "building $version"
  # Build into the install directory so the final rename stays on one
  # filesystem and is atomic.
  tmpfile="$INSTALL_DIR/.pyry.install.$$"
  go build -ldflags "-X main.Version=$version" -o "$tmpfile" ./cmd/pyry

  if [ -f "$TARGET" ] && cmp -s "$tmpfile" "$TARGET"; then
    info "installed binary already matches $version; nothing to swap"
    local cmd
    cmd="$(restart_cmd)"
    [ -n "$cmd" ] && echo "    to bounce the daemon anyway: $cmd"
    return 0
  fi

  if [ -f "$TARGET" ]; then
    dated="$(backup_installed)"
    cp -p "$dated" "$PREV"
    info "saved the running build as $(basename "$dated") and pyry.prev"
  fi

  place "$tmpfile"
  tmpfile=""
  info "installed $("$TARGET" --version) at $TARGET"

  maybe_restart
}

cmd_rollback() {
  [ -f "$PREV" ] || err "no rollback copy at $PREV"
  if [ -f "$TARGET" ] && cmp -s "$PREV" "$TARGET"; then
    info "installed binary already matches pyry.prev; nothing to swap"
    return 0
  fi
  local dated
  if [ -f "$TARGET" ]; then
    dated="$(backup_installed)"
    info "kept the build being rolled back as $(basename "$dated")"
  fi
  # Copy first, then rename, so pyry.prev survives and a second rollback is
  # a no-op rather than a loss.
  tmpfile="$INSTALL_DIR/.pyry.rollback.$$"
  cp -p "$PREV" "$tmpfile"
  place "$tmpfile"
  tmpfile=""
  info "rolled back to $("$TARGET" --version)"

  maybe_restart
}

case "${1:-}" in
  install)  cmd_install ;;
  rollback) cmd_rollback ;;
  *) err "usage: $0 install|rollback" ;;
esac
