# Spec — install.sh post-install smoke check (#203)

## Files to read first

- `install.sh` (entire file, 139 lines) — the script you're extending. Today it ends after `info "next: see ...deployment.md"` (line 136); the new logic appends after that.
- `docs/deployment.md` § "Updating the binary" (Linux block ~lines 107–124, macOS block ~lines 202–222) — the canonical restart commands you mirror: `systemctl --user restart pyry` and `launchctl kickstart -k gui/$UID/dev.pyrycode.pyry`.
- `cmd/pyry/main.go:461-491` (`runStatus`) — exact stdout shape the smoke check greps. The lines printed are `Phase:`, `Child PID:`, `Restart count:`, `Last uptime:` (optional), `Next backoff:` (optional), `Started at:`, `Uptime:`. Each is `Key:` + spaces + value; key column width is fixed (printf `%-14s`-style alignment).
- `internal/control/dial.go:25-95` — `dial` already retries `ENOENT`/`ECONNREFUSED` for ~1.5s, and `runStatus` wraps the whole call in a 5s context deadline. **Do not add an extra sleep before invoking `pyry status`** — its built-in retry+timeout *is* the grace period named in AC #1.
- `internal/install/install.go:186` and `internal/install/install_test.go:130` — confirm the default launchd label is literally `dev.pyrycode.pyry` and the default systemd unit is `pyry.service` (i.e. `defaultName() == "pyry"`). The smoke check only handles the default name; `-pyry-name foo` deployments are out of scope (see § Out of scope).
- `docs/knowledge/features/install-e2e.md` § "Linux / systemd Specifics" → "Skip Discipline" — pattern to mirror for "is user-systemd usable?" detection (`systemctl --user is-system-running` returning `offline`/`unknown` means no D-Bus session, treat as "no service to restart").

## Context

**The bug being defended against (#202).** v0.10.1 introduced a startup deadlock: under non-TTY stdin (launchd, systemd, Claudian, wrapper scripts), the supervisor reaches `"pyrycode starting"` and brings the control server up — but never reaches `"spawning claude"`. `Started at` stays at `0001-01-01T00:00:00Z`; `Uptime` reports `time.Duration(math.MaxInt64).String()` = `2562047h47m16.854775807s`. `pyry status` works (control server is alive), but the values it prints are sentinels.

The user only noticed because Claudian + launchd both went silent. A post-install probe of `pyry status` would have caught it in seconds. #202 fixes the underlying hang; #203 (this ticket) adds the install-time detector so the next regression of this *class* surfaces immediately.

**Why this is a defensive smoke test, not a fix.** The fix is in #202. This check is cheap insurance against the same observable (sentinel `Uptime`) recurring from a different root cause. Per the pipeline-wide [Evidence-Based Fix Selection] principle: the failure mode has been observed (v0.10.1), so adding a deterministic post-install probe is justified. We are *not* speculating about other failure classes — only the one already proven to happen.

**Why install.sh, not Go code.** The probe runs at install time, before any pyry process the user controls. install.sh already has root-of-trust (the user just ran `curl | bash`), is the chokepoint every binary update flows through, and adding ~80 lines of bash here is dramatically simpler than a new Go subcommand or a launchd helper. The technical-notes section of the ticket explicitly asks for "Bash, grep, awk only — no new binary dependencies."

## Design

The new logic appends to `install.sh`'s `main()` after the existing "next: see deployment.md" line. It runs in five steps:

```
┌──────────────────────────────────────────────────────────────────┐
│ existing install.sh main() — download / verify / extract / drop │
└──────────────────────────────────────┬───────────────────────────┘
                                       │
              ┌────────────────────────▼────────────────────────┐
              │ smoke_check()                                   │
              │   1. detect service registration (per-platform) │
              │   2. if absent → print info, return 0           │
              │   3. restart service (per-platform)             │
              │   4. probe with `pyry status`                   │
              │   5. classify: healthy | sentinel | dial-fail   │
              └────────────────────────┬────────────────────────┘
                                       │
                                       ▼
                          exit 0  /  exit 2  /  exit 3
```

### Step 1 — Detect service registration

Per-platform "is the user-managed service registered for the default name?" check. The check must be **non-fatal and fast**: a missing service simply means "no smoke check applies", not an error.

**macOS (`os == "Darwin"`):**

```bash
service_present_darwin() {
  # gui/<uid>/dev.pyrycode.pyry — `print` exits 0 iff the label is registered.
  # The label is the install-service default; non-default -pyry-name
  # deployments are out of scope.
  local uid label
  uid=$(id -u)
  label="dev.pyrycode.pyry"
  launchctl print "gui/${uid}/${label}" >/dev/null 2>&1
}
```

**Linux (`os == "Linux"`):**

```bash
service_present_linux() {
  # Need both: systemctl --user must be usable, AND pyry.service must be loaded.
  command -v systemctl >/dev/null 2>&1 || return 1
  # is-system-running returns "offline" or "unknown" when there is no D-Bus
  # session for the invoking user (common on CI runners). Treat as "no service".
  local state
  state=$(systemctl --user is-system-running 2>/dev/null || true)
  case "$state" in
    offline|unknown|"") return 1 ;;
  esac
  systemctl --user list-unit-files pyry.service --no-legend 2>/dev/null \
    | grep -q '^pyry\.service'
}
```

Use `list-unit-files` (not `is-active`) so the service is detected whether or not it's currently running — we want to know "is this host one of the deployment targets?" not "is it up right now?".

### Step 2 — Skip path

If detection returns false, print:

```
==> no pyry service detected — skipping post-install smoke check
    (run `pyry install-service` to set one up; see docs/deployment.md)
```

…and return 0. This satisfies AC #5 ("skipped … when install.sh is run in a context that does not (re)start the service").

### Step 3 — Restart

```bash
restart_darwin() {
  launchctl kickstart -k "gui/$(id -u)/dev.pyrycode.pyry"
}

restart_linux() {
  systemctl --user restart pyry
}
```

Both commands return 0 on accept and non-zero on hard failure. If they fail, print the failure and exit 4 (distinct from sentinel/dial-fail — this means the *service manager* itself rejected the request, e.g. unit file references a missing binary path; that is a separate operator-actionable problem from a hung supervisor).

### Step 4 — Probe

Invoke the **freshly installed** binary's `status` (not whatever pyry happens to be on `$PATH`):

```bash
"${INSTALL_DIR}/pyry" status 2>&1
```

Capture stdout+stderr together into a variable, plus exit code. No manual `sleep` — `pyry status` already retries `ENOENT`/`ECONNREFUSED` for ~1.5s and times out at 5s total, which collectively *is* the "brief grace period (e.g. 5 seconds)" the AC names. Adding a `sleep 5` on top of this would double the wall-clock cost on the healthy path for no benefit.

Why the install-dir binary, not bare `pyry`: a fresh first-time install may not have `INSTALL_DIR` on `$PATH` yet (the existing PATH-advisory block warns about this). Calling `${INSTALL_DIR}/pyry` directly is robust.

### Step 5 — Classify

Three buckets, three exit codes:

| Outcome | Detection | Exit code | Message |
|---|---|---|---|
| **Healthy** | status exit 0, output does *not* match `^Uptime: *2562047h47m16\.854775807s$` | 0 | `==> supervisor running normally` plus the `Phase:` and `Started at:` lines (grep them out of the captured output for one-glance confirmation). |
| **Sentinel (the #202 class)** | status exit 0, output matches the sentinel `Uptime` line | 2 | `error: supervisor failed to start — Started at == 0001-01-01T00:00:00Z, Uptime == 2562047h47m16.854775807s sentinel detected. See https://github.com/pyrycode/pyrycode/issues/202 for diagnosis steps.` |
| **Dial fail** (control server didn't come up) | status exit non-zero | 3 | `error: supervisor restart did not bring up the control socket within 5s. The service manager accepted the restart but pyry's status endpoint is unreachable. Check service-manager logs (\`journalctl --user -u pyry\` / \`tail /tmp/pyry.{out,err}.log\`).` |
| **Restart fail** (service manager rejected) | restart command exit non-zero | 4 | `error: failed to restart pyry service via <systemctl|launchctl> — exit status N. See <command output>.` |

Detection of the sentinel uses `grep -E` against the canonical line:

```bash
if printf '%s\n' "$status_out" | grep -Eq '^Uptime: +2562047h47m16\.854775807s$'; then
  ...sentinel...
fi
```

The pattern is anchored, allows the variable-width column padding (`+` not single-space — `runStatus` uses printf alignment that may render as one or several spaces depending on the longest key), and matches the exact `time.Duration(math.MaxInt64).String()` form. This is the canonical sentinel string per #202's technical notes; we don't try to also parse `Started at: 0001-01-01T00:00:00Z` because two checks against the same observable add zero coverage and one risks false negatives if the format drifts.

### Concurrency model

None — install.sh is sequential bash. The only "concurrency" is whatever the service manager does after `restart`/`kickstart` returns (it accepts asynchronously and the supervisor starts on its own goroutines). `pyry status`'s built-in dial retry is the synchronization point.

### Error handling

`set -euo pipefail` is already on at the top of install.sh. The new helpers must not break that contract:

- The detection helpers (`service_present_*`) are explicitly called via `if`, so a non-zero return is consumed and does not abort the script.
- The restart commands are run unguarded; their non-zero exit *should* abort under `set -e`, but we want to convert that into a structured exit-4 with a clear message. Wrap the call:

  ```bash
  if ! restart_output=$(restart_darwin 2>&1); then
    err "failed to restart pyry service via launchctl: ${restart_output}"
  fi
  ```

  (`err` already exits 1; we want exit 4 for "restart fail" specifically — change `err` to accept an optional exit code, or define a new `fail_with_code()` helper. The simpler path is one new helper.)
- `pyry status`'s exit code must NOT abort the script — capture it explicitly:

  ```bash
  set +e
  status_out=$("${INSTALL_DIR}/pyry" status 2>&1)
  status_rc=$?
  set -e
  ```

### Skip-when-it-doesn't-apply (AC #5) coverage

The detection check from Step 1 covers all "no service" cases:
- Fresh install with no service ever set up → false → skip.
- CI/headless environments without D-Bus or GUI session → false → skip.
- Linux system without systemctl → false → skip.
- Operator running a `-pyry-name foo` non-default deployment → false (we only look for the default label/unit) → skip with the same informational message. Operators with custom names already understand they're off the well-trodden path; printing "no default-named service detected" is honest.

There is no separate `--no-smoke-check` flag. AC #5 says "skipped (or no-ops cleanly)"; the detection branch *is* the no-op path, and is reached automatically. Adding an explicit flag is YAGNI (no observed need; simplicity-first).

## Testing strategy

install.sh has no test harness today (verified: no `install*test*` files in the repo). For an XS bash change, the testing approach is:

1. **Manual round-trip on macOS** (the platform where #202 was observed):
   - On a host with a registered launchd service (default label), run `bash install.sh` (against a local checkout, with `PYRY_VERSION` pinned to a known-good release like v0.9.1).
   - Expect: `==> supervisor running normally Phase: running ...`, exit 0.
   - Repeat against v0.10.1 (the broken release): expect sentinel detection, exit 2.
   - Unload the launchd job (`launchctl bootout gui/$UID/dev.pyrycode.pyry`), run install.sh: expect skip message, exit 0.
2. **Manual round-trip on Linux** with a registered user-systemd unit, same three cases.
3. **Smoke-classification unit test (optional, dev's discretion):** the classifier (sentinel match vs. healthy vs. dial-fail) is a pure function of the captured `status_out` + `status_rc`. If the dev wants belt-and-suspenders, factor it into a `classify()` shell function and exercise it with three pre-canned outputs in a tiny `bats`/`shunit2`-free `test_classify.sh` (run with `bash test_classify.sh`). **Not required** — manual round-trip is sufficient evidence for an XS bash change against an already-existing observable.

E2E coverage in `internal/e2e/install_*_test.go` is **out of scope** — those tests cover `pyry install-service` round-trips, not `install.sh`. Extending them would balloon scope. If a future regression motivates automated install.sh coverage, that's a separate ticket.

## Open questions

1. **Should restart-fail also surface the underlying service-manager output?** Current design: yes (captured via `2>&1` and printed in the error message). Alternative: print only the exit code and tell the user to consult logs. Captured output is more actionable; keep it.

2. **Custom `-pyry-name` deployments — silent skip or informational mention?** Current design: skipped with the same generic message ("no pyry service detected"). The operator running a non-default name already knows the script is binary-only for them. If real operators ask for it later, add a `PYRY_NAME` env var to install.sh that scopes detection. Defer.

3. **Should the success path also restart on Linux when the unit is loaded but inactive (disabled)?** Edge case: unit file exists but the operator hasn't `enable --now`-d it. `systemctl --user restart` against an inactive unit will start it, which may not be what the operator wants on this install run. Mitigation: gate restart on `systemctl --user is-active --quiet pyry` succeeding — only restart if currently active. Same logic on macOS (gate on `launchctl print` reporting `state = running`, mirroring the install-e2e pattern). **Recommended: implement this gate.** It avoids unintentionally starting a service the operator deliberately disabled, and it cleanly maps to AC #5's "context that does not (re)start the service".

   Updated detection (Step 1):

   ```bash
   service_present_darwin() {
     launchctl print "gui/$(id -u)/dev.pyrycode.pyry" 2>/dev/null \
       | grep -q 'state = running'
   }
   service_present_linux() {
     # ...usability check as above...
     systemctl --user is-active --quiet pyry
   }
   ```

   Trade-off: an installed-but-not-yet-started service won't be restarted on first install — but on first install the operator's next step (per deployment.md) is `systemctl --user enable --now pyry` / `launchctl load`, which they run themselves. The smoke check is a regression detector for the **update** path, where the service is already running. Prefer this scoping.

## Out of scope

- Fixing #202's underlying hang (separate ticket, already merged).
- Custom `-pyry-name` deployment detection.
- Adding a `--no-smoke-check` / `--dry-run` flag (no observed need).
- Automated install.sh integration tests in `internal/e2e/`.
- Detection of failure classes other than the canonical sentinel `Uptime` (e.g. backoff loops, child-spawn errors). The ticket scope is the #202 observable specifically; broader liveness checks belong in `pyry status` itself or in a separate health-check ticket.

## Summary

~80 lines added to `install.sh`: two per-platform detection helpers, two restart helpers, one classifier, one router that calls them in order. Uses tools already required (`grep`, `printf`); adds no new binary dependencies. Honors `set -euo pipefail`. Skips cleanly on hosts without the default-named service. Detects the canonical sentinel via anchored grep against the line `pyry status` already prints. Distinguishes restart-fail (4), dial-fail (3), and sentinel (2) with separate exit codes for downstream automation.
