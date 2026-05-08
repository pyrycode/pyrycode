# ADR 017: `install.sh` post-install smoke check is bash + sentinel-grep, gates on running services, distinguishes four exit codes

## Status

Accepted (ticket #203).

## Context

#202 was a startup deadlock that left the supervisor in `Phase: starting`
forever under non-TTY stdin (launchd, systemd, piped wrapper). The control
server came up — `pyry status` answered — but `Started at` was still the
zero value and `Uptime` reported the
`time.Duration(math.MaxInt64).String()` sentinel
`2562047h47m16.854775807s`. The user only noticed because Claudian and
launchd both went silent; a post-install probe of `pyry status` would
have caught it in seconds.

#202 fixes the underlying hang. #203 (this decision) adds a defensive
install-time detector for the *observable*, so the next regression of
the same class surfaces at install time rather than silently hanging the
service.

Three design questions:

1. **Where does the detector live — `install.sh` or a new Go subcommand?**
2. **What triggers the probe — every install, or only when a managed service is in scope?**
3. **How are failure modes distinguished — single non-zero exit, or distinct codes?**

## Decision

**Detector lives in `install.sh`.** ~80 lines of bash appended after the
existing "next: see deployment.md" line. Helpers: `service_present_*` /
`restart_*` (per-platform), `classify_status` (pure), `smoke_check`
(orchestrator). No new binary dependencies; only `grep`, `awk`, `printf`,
`launchctl`/`systemctl`, and the freshly-installed `pyry status`.

**Probe gates on a currently-running default-named service.** macOS:
`launchctl print "gui/$(id -u)/dev.pyrycode.pyry"` + grep `state =
running`. Linux: `systemctl --user is-system-running` ≠
`offline`/`unknown`/`""`, then `systemctl --user is-active --quiet pyry`.
Anything else (no service ever installed, custom `-pyry-name`, CI without
D-Bus, deliberately-disabled unit) falls through to a skip-with-info
return.

**Four exit codes for four distinct failure modes.**

| Code | Outcome |
|---|---|
| 0 | Install OK; supervisor healthy (or no service in scope) |
| 2 | #202-class sentinel detected: `Uptime: 2562047h47m16.854775807s` |
| 3 | Dial fail: control socket never reachable inside `pyry status`'s 5s window |
| 4 | Restart fail: service manager rejected `kickstart`/`restart` |

**Sentinel detection is one anchored grep** against
`^Uptime: +2562047h47m16\.854775807s$` on `pyry status` output. We do
not also parse `Started at: 0001-01-01T00:00:00Z` — two checks against
the same observable add zero coverage.

**No manual `sleep` before the probe.** `pyry status` already retries
`ENOENT`/`ECONNREFUSED` for ~1.5s (`dialWithRetry`, #199) and times out
at 5s total. That window *is* the AC's "brief grace period."

## Rationale

### Why bash, not Go

The probe runs at install time, before any pyry process the user
controls. `install.sh` already has root-of-trust (the user just ran
`curl | bash`), is the chokepoint every binary update flows through, and
adding ~80 lines of bash here is dramatically simpler than a new Go
subcommand or a launchd helper. The ticket's technical-notes section
explicitly asks for "Bash, grep, awk only — no new binary dependencies."

A Go subcommand (`pyry install --smoke-check`) would add a new public
CLI surface, a new test seam, and a chicken-and-egg problem on
first-time installs (the freshly-extracted binary would need to know its
own service-management context). Bash sidesteps all of that.

### Why gate on currently-running services

The first iteration of the spec gated on "unit file present"
(`launchctl print` exits 0 / `systemctl --user list-unit-files` shows the
unit). The architect's open-question note recommended tightening to
"currently running" via `state = running` / `is-active --quiet`. Two
reasons:

1. **Don't unintentionally start a deliberately-disabled service.** An
   operator who ran `systemctl --user disable pyry` (or never enabled
   the unit on first install) doesn't want the installer kickstarting it
   for them on every release update. Gating on `is-active` means the
   smoke check restarts only services the operator has demonstrably
   chosen to keep running.
2. **The smoke check's job is regression detection on the update path.**
   A first-time install isn't where #202 manifested — kickstart-after-
   eviction was. `is-active` matches the population at risk.

The trade-off: an installed-but-not-yet-started service won't be
restarted on first install. But the operator's documented next step on
first install (per `deployment.md`) is `systemctl --user enable --now
pyry` / `launchctl bootstrap`, which they run themselves. The smoke
check does not — and should not — substitute for that.

### Why four exit codes, not one

AC #4 explicitly distinguishes "sentinel detected" from "control server
unreachable" — they are different failure modes pointing at different
root causes:

- **Sentinel** ⇒ supervisor is alive but stuck before `Supervisor.Run`
  (the #202 class). Action: read #202's diagnosis steps, check if
  another #202-shaped regression has crept in.
- **Dial fail** ⇒ supervisor isn't visible at all on the control socket.
  Action: check service-manager logs; the supervisor probably exited
  before binding the socket, or the service manager is itself broken.
- **Restart fail** ⇒ the service manager *rejected* the restart command.
  Action: validate the unit file (e.g. `pyry install-service` may need
  re-running after a `$HOME` move); check `journalctl`/`launchctl
  print` for unit errors.
- **Healthy** ⇒ done.

Surfacing all three under "exit 1, look at stderr" loses that
diagnostic. Distinct exit codes also let downstream deploy automation
branch — sentinel might page operators; restart-fail might re-run
`install-service`; dial-fail might surface logs without paging.

### Why one grep against `Uptime`, not also `Started at`

Both are derived from the same in-memory `State.StartedAt == time.Time{}`
condition; matching either is sufficient. Two checks against the same
observable are not belt-and-suspenders — they're correlated. Worse, if
`runStatus`'s format ever drifts (e.g. RFC3339 → human-readable for the
zero-time case), the second grep starts producing false negatives that
mask the sentinel. One anchored grep against the documented sentinel
string is the smaller surface.

### Why no extra `sleep` before the probe

The first draft of the design called for `sleep 5` after the restart, on
the "give the daemon time to start" intuition. Reading the actual code
(`internal/control/dial.go:25-95` + `runStatus`'s 5s context deadline)
showed `pyry status` already retries `ENOENT`/`ECONNREFUSED` for ~1.5s
and times out at 5s total — collectively, the "brief grace period" the
AC names. Adding a `sleep 5` on top would double the wall-clock cost on
the healthy path for no benefit, and would still need the same
classify-by-output logic afterwards.

The retry+timeout *is* the synchronization point. Bash leans on what's
already there; the script doesn't reimplement liveness logic.

## Consequences

- **The script grows by ~80 LOC.** Acceptable for an XS bash change at a
  high-leverage chokepoint.
- **Custom `-pyry-name` deployments are silently skipped.** The probe
  hardcodes `dev.pyrycode.pyry` / `pyry.service`. Renamed installs fall
  through to the no-service-detected message. If real operators ask for
  it later, add a `PYRY_NAME` env var.
- **The classification rule is reusable from a future Go path.** If we
  later move the smoke check into `pyry update` (`#203 + #190`-style
  composition), the four-bucket classification is the same — only the
  caller changes. Documenting the bucket boundaries in this ADR keeps
  that option open.
- **A future `pyry status --machine-readable` would let us drop the
  string-grep.** For now, string-matching the canonical sentinel is the
  cheaper path; a JSON `pyry status` output is unrelated work.

## Alternatives considered

1. **Probe inside the daemon (e.g. `Supervisor.Run` watchdog).** Doesn't
   help: the failure mode IS that `Supervisor.Run` was never reached.
   Self-watchdogs cannot diagnose their own non-execution.
2. **Probe in a launchd `KeepAlive`/systemd `Restart=on-failure` hook.**
   `pyry status` returning normally on a stuck supervisor means the
   service manager sees the process as healthy. The probe must run
   *outside* the service-managed process to observe the hang.
3. **Run the probe on every install regardless of service state.** Was
   the first iteration; rejected because it would auto-start
   deliberately-disabled units (see § "Why gate on currently-running").
4. **Fold the probe into `pyry update`.** `pyry update` is one update
   path; `install.sh` is another (curl-piped first-install + manual
   re-run). Putting the check at the install chokepoint covers both.
   `pyry update` already exec's the restart command (#190); a future
   composition can reuse this script's `classify_status` rule via a
   thin Go port.

## References

- Spec: `docs/specs/architecture/203-install-smoke-check.md`
- Implementation: `install.sh` (the `# ---------- post-install smoke
  check (#203)` band)
- Classifier unit test: `internal/install/test_smoke_classify.sh`
- Sibling fix: [ADR 016](016-bootstrap-ignores-persisted-lifecycle-state.md) — the underlying #202 hang the sentinel observed
- Underlying retry primitive: [features/control-plane.md § Client dial: transient-startup retry](../features/control-plane.md)
