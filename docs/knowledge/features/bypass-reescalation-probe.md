# In-band bypass re-escalation probe (#2060)

## TL;DR

A child launched **with** `--dangerously-skip-permissions`, downgraded in-band to `default`,
then asked back into `bypassPermissions` **in the same child**: **RE-ESCALATION APPLIED** at
claude 2.1.239. This is the first committed capture of the case #1595 never covered — #1686's
2026-08-21 hand observation against 2.1.220 reproduces, nineteen releases on. Together with
\#1595's `enable` negative (still refused, byte-identical message, reproduced in the same run),
the mechanism is now settled: **claude gates the escalation on the launch argv, not on the
session's current mode.** A child launched without the flag can never reach bypass in-band; a
child launched with it may leave and re-enter as often as it likes.

This measures **claude**, not pyry. It adds no production writer and changes no daemon
behaviour — `permissionModeAllowed` (`internal/streamsup/envelope.go`) still refuses
`bypassPermissions` by non-membership, backing `SetPermissionMode`'s contract
(`internal/streamsup/runner.go`). Whether anything is built on this answer is #1686's decision.

## claude version measured

**2.1.239**, 2026-09-03, macOS, `-race`. Test
`internal/e2e/realclaude/bypass_reescalation_probe_test.go`
(`TestRealClaude_BypassReescalation_Probe`); fixtures
`internal/e2e/realclaude/testdata/bypass_reescalation_v2.1.239_<arm>.json`. Four children, 56 s,
model `claude-haiku-4-5`.

## The drive sequence — three turns, not two

Every arm here drives **three** turns, where #1595 and #2041 drive two. That is forced, not
stylistic: the `reescalate` arm's post-re-escalation read is a turn 3, and #1595's
index-symmetry rule (a measurement's read and its control must sit at the same turn index)
means the controls need a turn 3 too, or the comparison folds in a turn-index confound. Because
`reescalate` reads at turn 3 and `enable` (the #1595 negative, re-measured here for
comparability) still reads at turn 2, the two directions are classified at **different**
indices — so the no-discrimination check runs per direction at its own index, not once up front
the way #1595's does. A single up-front check would have answered for the wrong turn.

| arm | launch flag | control 1 (after turn 1) | control 2 (after turn 2) | read |
|---|---|---|---|---|
| `reescalate` | `--dangerously-skip-permissions` | → `default` | → `bypassPermissions` | turn 3 |
| `enable` | *(none)* | → `bypassPermissions` | *(none)* | turn 2 |
| `control_default` | *(none)* | *(none)* | *(none)* | 2 and 3 |
| `control_bypass` | `--dangerously-skip-permissions` | *(none)* | *(none)* | 2 and 3 |

Turns 1 and 2 reuse #1595's prompts verbatim for comparability with the 2.1.220 captures; turn 3
reads `/usr` — read-only package-manager content, deliberately not `$HOME`, the workdir or
`/etc`, because every turn's stdout is committed to a public repo.

## The downgrade gate — why a re-escalation verdict can be vacuous without it

A child that never actually left bypass behaves like the bypass control either way, so "turn 3
matches `control_bypass`" is true whether or not the downgrade ever landed — the check the AC
names would pass green on a no-op. `reescalateDowngrade` (in the probe file) closes that gap:
primarily off the init read (`init_permission_modes[0]` must read `bypassPermissions`,
`[1]` — the line the turn *after* the downgrade emits — must read `default`), falling back to a
purely behavioural comparison against both turn-2 controls when fewer than two init lines were
seen. Where neither confirms, the run reports `RE-ESCALATION NOT MEASURED` with the reason,
which is a recorded outcome — the test still passes.

Live evidence turned out stronger than the gate requires: the `reescalate` arm's turn 2 came
back **gated** between two ungated turns (`bypassPermissions → default → bypassPermissions` in
its init line, in step), so the downgrade is confirmed behaviourally at every stage, not only by
the init echo. A two-request arm that fired both requests back-to-back with no turn between them
would have produced the identical final-turn verdict with nothing behind it — the turn between
the two control requests is what makes the middle state observable at all.

## Verdict per direction

| direction | result | judged against | matched |
|---|---|---|---|
| `reescalate` (turn 3) | **RE-ESCALATION APPLIED** | `control_bypass` | exactly |
| `enable` (turn 2) | **ESCALATION FAILED** | `control_default` | exactly |

`enable`'s refusal is byte-identical to #1595's 2.1.220 capture:
`Cannot set permission mode to bypassPermissions because the session was not launched with
--dangerously-skip-permissions`. Both `control_response`s on `reescalate` came back
`subtype:"success"`, echoing `default` then `bypassPermissions`, correlated by `request_id` — but
per #1595's rule the echo never enters the verdict; the behavioural read agreed with it
independently. `onSetPermissionMode callback not registered` did not appear on any arm, matching
both prior measurements.

## The `SecondRequest` capture field — a nil pointer, not four flat `omitempty` fields

`setModeFixtureRecord` gained `SecondRequest *setModeSecondRequest` rather than four
individually-optional fields. `control_response_id_matched` has a meaningful `false` (a reply
that correlated to nothing) — a flat `bool` with `json:"...,omitempty"` would spell that `false`
by absence, the identical trap #2041's `supports_auto_mode` recorded. A nil pointer says "no
second request was sent"; a non-nil one always carries all four fields, so the false-by-absence
case cannot occur.

## What the rig gained, additively

- `setModeArm.secondTargetMode` — empty means "send no second control request"; both of #1595's
  and #2041's existing keyed-literal construction sites get this behaviour for free.
- `setModeChildConfig.promptThree` — empty means "stop after turn 2", unchanged for every
  existing arm.
- `runSetModeChild` — one guarded block after turn 2's wait: writes the second control request
  when `secondTargetMode` is set (waiting on a `controlResponseCount` baseline+1, not an
  absolute 2, so an unsolicited reply can't satisfy the wait), then drives turn 3.

Only two call sites construct `setModeArm` (`setModeArms` and #2041's inline literal) and both
use keyed fields, so neither changed.

## Reproduce

```bash
export CLAUDE_CODE_OAUTH_TOKEN=...   # or ANTHROPIC_API_KEY
go test -tags e2e_realclaude -race -count=1 -v \
  -run TestRealClaude_BypassReescalation ./internal/e2e/realclaude/
```

~56 s, four children. Deliberately not prefixed `TestRealClaude_SetPermissionMode` or
`TestRealClaude_InBandModeSwitch` — both are documented reproduce filters for measurements that
budgeted their own children, and sharing a prefix would sweep four more into a run budgeted for
one. Deterministic half (no credentials, no subprocess):

```bash
go test -tags e2e_realclaude -race -count=1 -run TestBypassReescalation ./internal/e2e/realclaude/
```

## Related

- [`bypass-approval-argv-probe.md`](bypass-approval-argv-probe.md) — #2061, which builds on this
  ticket's mechanism finding to measure the same escalation on the argv production actually
  spawns (the four approval flags alongside bypass): the approval bridge comes back after the
  in-band downgrade, and no race window was observed in three consecutive spawns.
- [`set-permission-mode-inband-probe.md`](set-permission-mode-inband-probe.md) — #1595, whose
  `enable` negative and index-symmetry rule this measurement re-measures and extends.
- [`permission-mode-switch-inband-probe.md`](permission-mode-switch-inband-probe.md) — #2041,
  source of `setModeChildConfig`, the model-discovery pattern and the `omitempty`-false-by-absence
  trap this ticket's `SecondRequest` field avoids the same way.
- [`e2e-realclaude-interactive-stream-inband-model-test-go.md`](e2e-realclaude-interactive-stream-inband-model-test-go.md)
  — file-level entry for `bypass_reescalation_probe_test.go`.
- `docs/specs/architecture/2060-bypass-reescalation-probe.md` — full design, downgrade-gate
  derivation and security review.
