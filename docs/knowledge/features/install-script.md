# `install.sh` — release downloader + post-install smoke check

`install.sh` is the curl-piped installer (`curl -fsSL …/install.sh | bash`)
referenced from the README and from `docs/deployment.md`'s "Updating the
binary" sections. It downloads a published GitHub release, verifies the
SHA-256 against the release's `checksums.txt`, drops the binary at
`${PYRY_INSTALL_DIR:-$HOME/.local/bin}/pyry`, and — when a default-named
managed service is currently running — restarts it and probes for the
canonical "supervisor stuck at startup" sentinel before exiting (#203).

The script is bash-only (`set -euo pipefail`), uses `curl`, `tar`, `grep`,
`awk`, `sha256sum`/`shasum`, and the platform's service-manager CLI. No
new binary dependencies were introduced for the smoke check.

## Phases

```
detect OS / arch
    ↓
resolve VERSION (latest GitHub release if PYRY_VERSION unset)
    ↓
download tarball + checksums.txt
    ↓
verify_checksum
    ↓
extract pyry → install -m 0755 → ${INSTALL_DIR}
    ↓
PATH advisory + `pyry version`
    ↓
smoke_check  (#203 — only runs when a default-named service is active)
```

## Smoke check (#203)

Defends against the class of regression where the control server comes up
but `Supervisor.Run` never reaches `"spawning claude"` — the v0.10.1
non-TTY-stdin hang fixed in #202. The observable is the canonical
`time.Duration(math.MaxInt64).String()` rendered as
`Uptime: 2562047h47m16.854775807s` in `pyry status` output.

This is a defensive smoke test, not a fix. Per the pipeline's
[Evidence-Based Fix Selection] principle, the failure mode has been
observed (v0.10.1), so the cost of an ~80-line bash probe at the install
chokepoint is justified.

### Detection — does a service apply here?

Per-platform "is there a default-named, currently-active pyry service on
this host?" probe. Both arms gate on **running** state — not just "unit
file present" — so a deliberately-disabled unit isn't accidentally
started by the smoke check.

| Platform | Detection |
|---|---|
| `Darwin` (macOS) | `launchctl print "gui/$(id -u)/dev.pyrycode.pyry"` stdout greps for `state = running` |
| `Linux` | `systemctl --user is-system-running` must not return `offline`/`unknown`/empty (no D-Bus session ⇒ skip), then `systemctl --user is-active --quiet pyry` |

Anything that returns false (no service, custom `-pyry-name foo`
deployment, CI/headless host without D-Bus, operator deliberately
disabled the unit) drops cleanly into the skip branch:

```
==> no running pyry service detected — skipping post-install smoke check
    (run `pyry install-service` to set one up; see docs/deployment.md)
```

…and exits 0. This satisfies AC #5 ("skipped … when install.sh is run in
a context that does not (re)start the service").

### Restart

| Platform | Command |
|---|---|
| `Darwin` | `launchctl kickstart -k "gui/$(id -u)/dev.pyrycode.pyry"` |
| `Linux` | `systemctl --user restart pyry` |

The `2>&1`-captured output is preserved into a structured exit-4 error
message if the service manager rejects the request — that's a separate
operator-actionable failure (e.g. unit references a missing binary path)
distinct from a hung supervisor.

### Probe — `${INSTALL_DIR}/pyry status`

The freshly-installed binary is invoked directly (not bare `pyry`) so a
first-time install where `INSTALL_DIR` isn't yet on `$PATH` still
probes the right binary.

**No manual `sleep` before the probe.** `pyry status` already retries
`ENOENT`/`ECONNREFUSED` for ~1.5s via `internal/control.dialWithRetry`
(#199) and times out at 5s total via `runStatus`'s context deadline.
Those collectively *are* the "brief grace period" AC #1 names; layering
a `sleep 5` on top would double the wall-clock cost on the healthy path
for no benefit.

### Classification — `classify_status()`

Three buckets, four exit codes:

| Outcome | Detection | Exit | Operator-facing message |
|---|---|---|---|
| **Healthy** | `status` exit 0, output does not match the sentinel grep | 0 | `==> supervisor running normally` plus the `Phase:` and `Started at:` lines grepped from captured output |
| **Sentinel (#202 class)** | `status` exit 0, output matches `^Uptime: +2562047h47m16\.854775807s$` | 2 | `error: supervisor failed to start — Started at == 0001-01-01T00:00:00Z, Uptime == 2562047h47m16.854775807s sentinel detected. See https://github.com/pyrycode/pyrycode/issues/202 for diagnosis steps.` |
| **Dial fail** | `status` exit non-zero (control socket unreachable inside its own retry+timeout window) | 3 | `error: supervisor restart did not bring up the control socket within 5s. … check service-manager logs (\`journalctl --user -u pyry\` / \`tail /tmp/pyry.{out,err}.log\`).` |
| **Restart fail** | service-manager command exited non-zero | 4 | `error: failed to restart pyry service via <systemctl\|launchctl> — exit status N. <captured output>` |

The sentinel match is anchored against the canonical line `runStatus`
prints: `^Uptime: +2562047h47m16\.854775807s$`. The `+` (not single-space)
allows for the variable-width column padding `runStatus` uses when other
keys (e.g. `Last uptime:`) are wider. Detection is a single grep against
the documented zero-time tell from #202; we deliberately do NOT also
parse `Started at: 0001-01-01T00:00:00Z` — two checks against the same
observable add zero coverage and one risks false negatives if `runStatus`'s
format drifts.

### Distinct exit codes

The four-bucket exit-code split (0 / 2 / 3 / 4) matters for downstream
automation. Operators wrapping `install.sh` in their own deploy scripts
can branch:

- 0 — install succeeded, supervisor (if applicable) is healthy
- 2 — install succeeded *but* supervisor is in the #202-class hung state
- 3 — install succeeded, restart accepted, control socket never came up
- 4 — install succeeded, but the service manager rejected the restart

Sentinel and dial-fail are *different* failure modes per AC #4 — the
former means "the supervisor is running but stuck"; the latter means "no
supervisor visible at all." Surfacing them under the same code would
collapse the diagnostic distinction the install-time probe is supposed
to draw.

## Classifier unit test

`internal/install/test_smoke_classify.sh` is a bash-only unit test for
`classify_status`. It sources only the `# ---------- post-install smoke
check` to `# ---------- main` band of `install.sh` (extracted via `awk`)
to avoid running `main()`, then feeds three pre-canned `pyry status`
outputs and asserts return codes 0 / 2 / 3.

Run with:

```
bash internal/install/test_smoke_classify.sh
```

It is not wired into Go's test runner — it's an XS bash-only artefact for
the bash-only classifier. Manual round-trip against a real launchd /
systemd service on the platforms is the load-bearing coverage; this
script is the cheap regression net for the pure classification logic.

## Out of scope

- Custom `-pyry-name foo` deployment detection (operator already off the
  default path; falls through to skip).
- `--no-smoke-check` / `--dry-run` flags (no observed need; the detection
  branch IS the no-op path).
- Detection of failure classes other than the canonical `Uptime`
  sentinel (e.g. backoff loops). The ticket scope is the #202 observable
  specifically; broader liveness checks belong in `pyry status` itself.
- Automated `install.sh` integration tests in `internal/e2e/` — extending
  those would balloon scope. Manual round-trip on each platform is the
  agreed coverage for an XS bash change.

## Related

- [ADR 017 — install.sh post-install smoke check shape](../decisions/017-install-script-smoke-check.md)
- [ADR 016 — bootstrap ignores persisted lifecycle state](../decisions/016-bootstrap-ignores-persisted-lifecycle-state.md) — the #202 fix this probe defends
- [`docs/knowledge/features/install-e2e.md`](install-e2e.md) — `pyry install-service` round-trip tests (different surface: the in-binary unit-file generator, not the curl-piped release installer)
- [`docs/deployment.md`](../../deployment.md) — operator-facing service setup
