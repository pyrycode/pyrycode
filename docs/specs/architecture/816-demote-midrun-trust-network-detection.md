# #816 — agent-run: demote mid-run trust/network detection from fatal to logged

## Files to read first

- `internal/agentrun/ptyrunner/runner.go:518-546` — the `for ev := range ch` event
  loop. The two cases to change are `EventKindPtyModalShown`+`ModalClassTrustFolder`
  (521-526) and `EventKindPtyNetworkFailureShown` (532-535). The `EventKindPtyMcpFailureShown`
  case (527-531) is the **template** — `Warn` + fall-through, no return.
- `internal/agentrun/ptyrunner/runner.go:396-406` — the startup/idle `ready.TrustModal`
  / `ready.NetworkFailure` aborts. **These stay fatal — do not touch.**
- `internal/agentrun/ptyrunner/runner.go:200-221` — `Run`'s return-value contract doc.
  The bullet at 203-207 currently claims the sentinels also surface "on a mid-run
  EventKindPty{…} transition." That clause is now stale and must be corrected.
- `internal/agentrun/streamjson/emitter.go:302-323` — `SetTerminalDetail` semantics:
  idempotent (first non-empty wins), and **only surfaced when the resolved
  ExitReason is `ExitReasonError`**. A clean completion or a max_turns stop ignores
  it. This is why keeping the `SetTerminalDetail` calls on the demoted path is safe.
- `internal/agentrun/ptyrunner/watchdog.go:30-38` — the watchdog also calls
  `SetTerminalDetail("watchdog: …")`. Because the detail is first-wins, a mid-run
  trust/network detection that fired earlier keeps its `trust_modal_detected` /
  `network_failure_detected` label on a subsequent wedge trailer.
- `internal/agentrun/ptyrunner/runner_test.go:275-330` — `TestRun_MidRun_ModalAndBannerDetection`,
  the only test to flip (currently asserts the two sentinels return).
- `internal/agentrun/ptyrunner/runner_test.go:585-593` — the short-watchdog-opts
  pattern (`WatchdogTick` + `WatchdogTrackerOpts`) to mirror in the flipped test.
- `internal/agentrun/ptyrunner/runner_test.go:211-273, 876-909` — the startup tests
  (`TestRun_TrustModalDetected`, `TestRun_NetworkFailureDetected`,
  `TestRun_McpFailureNonFatal`, `TestRun_RecordOn_ErrTagged`). They use the `"trust"` /
  `"network_failure"` (startup) fixtures — **leave green, do not change.**
- `internal/agentrun/ptyrunner/helper_test.go:63-74, 208-252` — the `mid_trust` /
  `mid_network_failure` fixtures. They write the anchor after the prompt lands and
  then wedge (no end-of-turn). **No fixture change needed** — this is exactly the
  shape the demoted path exercises.
- Prior art: `docs/specs/architecture/513-ptyrunner-events-unified-stream.md` — the
  spec that *introduced* mid-run modal/banner monitoring. This ticket reverses only
  its fatal-on-mid-run policy, not the unified-stream mechanism.

## Context

`ptyrunner.Run`'s event loop treats a **mid-run** trust-folder modal or network-failure
detection as fatal — it returns `ErrTrustModalDetected` / `ErrNetworkFailure` the instant
the event fires, killing an otherwise-progressing run. During the 2026-07-07 tui-driver
detection review, every mid-run fire of these two detectors in the 1136-cast corpus was a
**content forgery** (an agent rendering a ticket body or detector source that quotes the
anchor string on the grid). The detector-level forgery fixes shipped in tui-driver v1.8.0
(#219-#227) and are live via pyry v1.8.1: trust now requires the dialog option-row shape,
network is re-anchored on the live `Unable to connect to API` line and bottom-region scoped.
The forgery justification for the mid-run abort is therefore gone; this ticket removes the
remaining blast radius so even a *genuine* mid-run blip does not kill a live run.

The two transient cases are already covered without a hard kill: claude self-retries a
network failure on its own backoff, and the PTY-quiet watchdog remains the true fatal net
for a genuinely wedged run. A false kill on a single mid-stream match is strictly worse than
a slightly later watchdog stop. The startup/idle aborts stay fatal — a trust/network failure
*before* the run begins means it genuinely cannot start.

Not security-sensitive: this *removes* a forgery-triggerable fatal on a local PTY boundary
(security-net-positive), adds no new surface or parsing, and depends on hardening that already
shipped in a separate repo. No new design surface for a spec-stage sec review to audit.

## Design

Single production file: `internal/agentrun/ptyrunner/runner.go`. No new types, no new
functions, no signature changes. Zero consumers of the sentinels exist outside this package
(verified: `grep` for `ErrTrustModalDetected` / `ErrNetworkFailure` in non-test production
code returns only `runner.go`), so there is no call-site cascade.

### Change 1 — demote the two mid-run cases (runner.go:521-535)

Both cases converge to the exact shape the `EventKindPtyMcpFailureShown` case already uses:
`logger.Warn(...)` then fall through the `switch` (no `return`). **Retain** the existing
`emitter.SetTerminalDetail("trust_modal_detected")` / `SetTerminalDetail("network_failure_detected")`
calls.

- `EventKindPtyModalShown` + `ModalClassTrustFolder`: `Warn` + `SetTerminalDetail("trust_modal_detected")`,
  then continue consuming events. Remove `return ErrTrustModalDetected`.
- `EventKindPtyNetworkFailureShown`: `Warn` + `SetTerminalDetail("network_failure_detected")`,
  then continue. Remove `return ErrNetworkFailure`.

**Why keep `SetTerminalDetail`.** The AC requires the Warn to carry "the existing terminal
detail." `SetTerminalDetail` is idempotent-first-wins and is surfaced *only* when the run
resolves to `ExitReasonError` (emitter.go:302-323). So:
  - If the run continues to a clean end-of-turn (`ExitReasonCompletion`), the detail is
    ignored — the success trailer is unaffected. No pollution.
  - If the run later genuinely wedges (no end-of-turn → `ExitReasonError`, e.g. stuck on an
    un-answerable prompt until the watchdog fires), the trailer's `terminal_reason` reads
    `trust_modal_detected` / `network_failure_detected` — *more* diagnostic than the generic
    `watchdog: …` the watchdog would otherwise write, because first-wins lets the earlier
    detection label win.

This is the belt-and-suspenders payoff of the demotion: the run gets a chance to progress,
and if it can't, the wedge trailer still names the mid-run condition that preceded it.

Consider collapsing the three mid-run cases' rationale into one adjacent comment: all three
(trust, mcp, network) are now `Warn` + continue; the real fatal net is the PTY-quiet
watchdog + the dispatcher wall-clock cap; a real network outage renders claude's own
`API Error:` and self-retries. Keep it concise — match the existing comment density.

### Change 2 — correct the stale contract doc (runner.go:203-207)

The `Run` doc bullet currently reads (paraphrased): "ErrTrustModalDetected / ErrMcpFailureBanner
/ ErrNetworkFailure on the post-idle one-shot detection OR on a mid-run EventKindPty{…}
transition." Rewrite so the sentinels are documented as returned **only** from the startup/idle
`WaitReady` classification. State that mid-run trust/network/mcp detections are logged (`Warn`)
and consumed — the run continues — and that a mid-run detection's terminal detail still lands
on the wedge trailer if the run subsequently fails to reach end-of-turn. `ErrMcpFailureBanner`
was already never returned; drop it from the "returned" list entirely.

The sentinel `var` docs (runner.go:65-77) are already idle-scoped ("renders at idle",
"unreachable at idle") and stay accurate — no change. `ErrTrustModalDetected` and
`ErrNetworkFailure` remain live sentinels (still returned from the startup path), so no
dead-code issue.

## Concurrency model

Unchanged. The event loop still runs inline in `Run`'s goroutine; the watchdog goroutine is
untouched. The only cross-goroutine interaction introduced is the first-wins race on
`SetTerminalDetail` between a mid-run detection (sets it during event processing) and the
watchdog (would set `watchdog: …`, becomes a no-op if the detection already set a detail).
`SetTerminalDetail` is mutex-guarded and idempotent, so the race is well-defined — the earlier
writer wins, which is the mid-run detection by design.

## Error handling / failure modes

- **Mid-run trust/network that then completes** — run reaches end-of-turn, `ExitReasonCompletion`,
  clean `success` trailer, detail ignored. This is the case the demotion unblocks (previously a
  hard kill).
- **Mid-run trust/network that then wedges** — no end-of-turn; the PTY-quiet watchdog fires,
  cancels `runCtx`, loop ends, `Run` returns `nil` (watchdog-fired-wedge collapse per the
  existing contract), `ExitReasonError`, trailer `terminal_reason` = the mid-run detail. The
  run still ultimately fails — but via the true safety net, later, with a descriptive reason,
  never via a fatal short-circuit on the first frame that matched.
- **Startup trust/network** — unchanged: `ErrTrustModalDetected` / `ErrNetworkFailure` returned,
  recording tagged `-err`.

## Testing strategy

### Flip `TestRun_MidRun_ModalAndBannerDetection` (runner_test.go:275-330)

The `mid_trust` / `mid_network_failure` fixtures need no change (they already emit the event
then wedge with no end-of-turn). Rework the table test:

- Drop the `want error` / `substring` fields (no sentinel is returned now).
- Assert `Run` returns `nil` — equivalently, `!errors.Is(err, ErrTrustModalDetected)` and
  `!errors.Is(err, ErrNetworkFailure)`. This is the core "does NOT end the run" contract.
- **Keep** the `wantReason` assertion: `parseTrailer(...).TerminalReason` equals
  `"trust_modal_detected"` / `"network_failure_detected"`. This verifies the demoted path still
  recorded the terminal detail (the "with the existing terminal detail" half of the AC) and
  that it survived onto the wedge trailer.
- Optionally strengthen: assert the trailer is the wedge shape (`is_error == true`) to document
  that the run still fails via the watchdog, just not via the fatal sentinel. (`trailer` in
  runner_test.go:88-96 already exposes `IsError`.)

**Timing / determinism.** With the base `helperRunCfg`, watchdog limits default to 30s, so the
demoted run would wait out the 10s ctx before ending — slow. Set short watchdog opts on the
test config, mirroring runner_test.go:585-593, but with a limit comfortably larger than
tui-driver's 50 ms event-poll so the mid-run detection's `SetTerminalDetail` reliably wins the
first-wins race before the watchdog cancels:

- `cfg.WatchdogTick = 50 * time.Millisecond`
- `cfg.WatchdogTrackerOpts = tuidriver.TrackerOpts{PTYQuietLimit: 1 * time.Second, SpinnerFreezeLimit: 1 * time.Second}`

Ordering invariant to preserve: the mid-run event is emitted ~one 50 ms poll after the anchor
appears; the watchdog fires `PTYQuietLimit` after the anchor (the last PTY activity). A 1 s
limit gives a ~950 ms margin for the detection to set `terminal_reason` before the watchdog
wins — far safer than the 200 ms the jsonl tests use, while still ending each subtest in ~1 s.
Keep the outer 10 s ctx as a safety net.

### Startup tests stay green (do not modify)

`TestRun_TrustModalDetected`, `TestRun_NetworkFailureDetected`, `TestRun_McpFailureNonFatal`
(runner_test.go:211-273) and `TestRun_RecordOn_ErrTagged` (876-909) all drive the startup
(`"trust"` / `"network_failure"` / `"mcp_failure"`) fixtures and assert the startup contract —
unaffected by this change.

### Gate

`make check` (vet + race + staticcheck + substrate-guard). **No `gofmt` target** — do not
reformat. `helper_test.go` is substrate-guard-allowlisted (holds fake-claude anchors) and is
already gofmt-dirty at HEAD; you are not editing it, but if you touch it, match its existing
style rather than reformatting.

## Open questions

- None blocking. One judgment call left to implementation: whether to also capture the logger
  and assert the exact `Warn` line was emitted. Not required — the `terminal_reason` trailer
  assertion already proves the demoted branch ran (`SetTerminalDetail` and `Warn` are adjacent
  in the same case). Adding a log-capture assertion is optional belt-and-suspenders, not
  mandated by the AC.
