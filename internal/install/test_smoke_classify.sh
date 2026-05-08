#!/usr/bin/env bash
#
# Unit test for install.sh's classify_status helper. Sources install.sh
# (avoiding its main()) and feeds canned `pyry status` outputs to verify
# the three classification buckets: healthy / sentinel / dial-fail.
#
# Run: bash internal/install/test_smoke_classify.sh

set -uo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(cd "${SCRIPT_DIR}/../.." && pwd)
INSTALL_SH="${REPO_ROOT}/install.sh"

# Source classify_status without executing install.sh's main(). We do this by
# extracting the helper definitions to a temp file via awk.
helpers=$(mktemp)
trap 'rm -f "$helpers"' EXIT

awk '/^# ---------- post-install smoke check/{p=1} /^# ---------- main/{p=0} p' \
  "$INSTALL_SH" > "$helpers"

# install.sh references $REPO inside the sentinel message; supply it.
REPO="pyrycode/pyrycode"
# shellcheck disable=SC1090
source "$helpers"

failures=0

check() {
  local name="$1" expected_rc="$2" actual_rc="$3"
  if [ "$expected_rc" = "$actual_rc" ]; then
    echo "PASS: ${name}"
  else
    echo "FAIL: ${name} — expected rc=${expected_rc}, got rc=${actual_rc}"
    failures=$((failures + 1))
  fi
}

# Case 1: healthy — non-zero status output, no sentinel, status_rc=0.
healthy_out='Phase:         running
Child PID:     12345
Restart count: 0
Started at:    2026-05-08T10:00:00Z
Uptime:        42s'
set +e
classify_status "$healthy_out" 0 >/dev/null 2>&1
rc=$?
set -e
check "healthy" 0 "$rc"

# Case 2: sentinel — status_rc=0 but Uptime line is the MaxInt64 sentinel.
sentinel_out='Phase:         starting
Restart count: 0
Started at:    0001-01-01T00:00:00Z
Uptime:        2562047h47m16.854775807s'
set +e
classify_status "$sentinel_out" 0 >/dev/null 2>&1
rc=$?
set -e
check "sentinel" 2 "$rc"

# Case 3: dial-fail — status_rc non-zero (e.g. control socket missing).
dial_fail_out='status: dial unix /Users/x/.pyry/pyry.sock: connect: no such file or directory'
set +e
classify_status "$dial_fail_out" 1 >/dev/null 2>&1
rc=$?
set -e
check "dial-fail" 3 "$rc"

# Case 4: sentinel formatting variant — verify the regex tolerates the
# variable-width column padding produced by runStatus's printf alignment.
sentinel_tight='Uptime: 2562047h47m16.854775807s'
set +e
classify_status "$sentinel_tight" 0 >/dev/null 2>&1
rc=$?
set -e
check "sentinel (tight column)" 2 "$rc"

# Case 5: a near-miss uptime that should NOT be classified as sentinel.
near_miss='Phase:         running
Started at:    2026-05-08T10:00:00Z
Uptime:        2562047h47m16.854775806s'
set +e
classify_status "$near_miss" 0 >/dev/null 2>&1
rc=$?
set -e
check "near-miss uptime is healthy" 0 "$rc"

if [ "$failures" -gt 0 ]; then
  echo
  echo "${failures} test(s) failed"
  exit 1
fi
echo
echo "all tests passed"
