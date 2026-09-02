# In-band permission-mode switch probe (#2041)

## TL;DR

All four modes `#1595` left unmeasured — `acceptEdits`, `dontAsk`, `plan`, `auto` — switch on a
running child in-band at claude 2.1.239, extending #1595's `default`/`bypassPermissions` pair.
`auto` is refused **per model**, never per mechanism: a model that doesn't publish
`supportsAutoMode: true` refuses the switch with a verbatim message while the switch mechanism
itself is identical on both models.

## claude version measured

**2.1.239**, 2026-09-02, macOS, `-race`. Test
`internal/e2e/realclaude/permission_mode_switch_probe_test.go`
(`TestRealClaude_InBandModeSwitch_Probe`); fixtures
`internal/e2e/realclaude/testdata/permission_mode_switch_v2.1.239_<arm>.json`.

## Verdict per arm

| arm | model | `control_response` | `init.permissionMode` after |
|---|---|---|---|
| `acceptEdits` | sonnet (auto-capable, chosen live) | `success` | `acceptEdits` |
| `dontAsk` | sonnet | `success` | `dontAsk` |
| `plan` | sonnet | `success` | `plan` |
| `auto` | sonnet | `success` | `auto` |
| `auto_unsupported` | claude-haiku-4-5 (auto-incapable, chosen live) | `error` | unchanged |

The `auto_unsupported` refusal, verbatim: `Cannot set permission mode to auto: auto mode
unavailable for this model`. All five requests are correlated by `request_id`; the ack plus the
next turn's `system`/`init` line is the read, mirroring #1595's boundary of not measuring
behaviour (no control arms — the read here is direct).

## The model is chosen from the run's own list, never a table

A throwaway discovery child (`--model claude-haiku-4-5`, `--max-turns 1`, no probe turns) sends
one `initialize` control request before any arm runs, and the auto-capable / auto-incapable
models are picked from a preference list intersected against what that run actually published —
not a fixed constant.

This is not defensive ceremony; it closes a real, previously-latent gap. #1595's reusable
`runSetModeChild` hardcoded `setModeModel = "claude-haiku-4-5"`, and in the committed 2.1.239
capture `claude-haiku-4-5` is one of only two rows publishing no `supportsAutoMode` at all.
Reused unchanged, the `auto` arm would have measured a refusal on a model that never supported
auto and reported "auto no longer switches in-band" — a false finding, passing green.

The live list also drifted from the repo's own committed capture at the *same* binary version:
`claude-fable-5-1[1m]` published live vs. `claude-fable-5[1m]` in
`testdata/initialize_control_v2.1.239.json`. Nothing depended on the exact name — `sonnet` was
selected first — but it is the concrete evidence for the design choice: a preference-list entry
naming a model that no longer exists costs nothing, because the fallback takes the first
qualifying row in arrival order.

`supportsAutoMode: false` is spelled by **absence**, not the literal — no row in the 2.1.239
list carries `false`. A capture field with `omitempty` would silently reproduce that trap; this
probe's `supports_auto_mode` / `key_present` fields are written unconditionally.

## Flag injection, not shell injection

Reading a model `value` out of one child's stdout and passing it as `--model <value>` to the
*next* child's argv is the one place an untrusted string crosses into another subprocess's
command line. No shell is involved (`exec.CommandContext` takes an argument slice, so quoting
doesn't apply) — but a value beginning with `-` is parsed as a **flag** by claude's own CLI
parser: `--model --dangerously-skip-permissions` would parse as a valueless `--model` followed
by a bypass flag, silently launching the child bypassed and inverting the posture every arm's
finding is judged against. `modeSwitchModelValueOK` is the single gate every candidate passes
before selection (non-empty, ≤64 bytes, no leading `-`, restricted charset) — the pattern worth
reusing anywhere a probe round-trips a claude-published value back into an argv.

## Reproduce

```bash
export CLAUDE_CODE_OAUTH_TOKEN=...   # or ANTHROPIC_API_KEY
go test -tags e2e_realclaude -race -count=1 -run TestRealClaude_InBandModeSwitch_Probe ./internal/e2e/realclaude/
```

~67 s, six children (one discovery plus five arms). The probe passes on every recorded outcome;
a refusal is a finding, not a failure.

Deterministic half, no credentials or subprocess:

```bash
go test -tags e2e_realclaude -race -count=1 -run 'TestModeSwitch' ./internal/e2e/realclaude/
```

Note the different prefix (`TestModeSwitch*`, not `TestRealClaude_*`): #1595's own documented
reproduce command filters on `-run TestRealClaude_SetPermissionMode`, and these deterministic
tests are named outside that family on purpose so a reader copying that command doesn't
accidentally sweep five more live children into a run budgeted for four.

## Related

- [`set-permission-mode-inband-probe.md`](set-permission-mode-inband-probe.md) — #1595's
  `default`/`bypassPermissions` pair this ticket extends; same argv shape, same recorder and
  drive sequence.
- [`e2e-realclaude-interactive-stream-inband-model-test-go.md`](e2e-realclaude-interactive-stream-inband-model-test-go.md)
  — the file-level entry for `permission_mode_switch_probe_test.go`, with the namer-sanitisation
  and scrub-ordering lessons this measurement also surfaced.
