# Bypass launch argv vs. production's approval flags (#2061)

## TL;DR

\#1686 wants every claude child launched with `--dangerously-skip-permissions` and downgraded
in-band immediately. Measured at claude 2.1.239 on the argv #1686 would actually ship —
production's four approval flags (`--permission-prompt-tool`, `--mcp-config`,
`--strict-mcp-config`, `--permission-mode default`) plus the bypass flag — **both open questions
came back "yes," so neither closes #1686 as answered-no**:

- **Q2 — APPROVAL GATE INTACT.** The combined argv launches; the child comes up in
  `bypassPermissions` with `mcp_servers` reporting the approval server connected even while in
  bypass. Turn 1, still in bypass, reached the stub approval socket zero times and ran ungated.
  After the in-band downgrade to `default`, turn 2 reached the socket once and was gated. The
  daemon's approval bridge comes back — #1686's design does not silently remove it.
- **Q3 — RACE WINDOW CLOSED**, on all three consecutive spawns, with no disagreement. Every
  child that wrote the downgrade before turn 1 drew its ack before turn 1 was written, and all
  three were gated on that turn.

This measures **claude**, not pyry, and at the time it landed changed no daemon
behaviour. #2066 built on this and #2060 together: `permissionModeAllowed`
(`internal/streamsup/envelope.go`) now admits `bypassPermissions`, and the escalation
routes in-band on the argv this probe measured — see
[`Pool.UpdateSettings`](sessions-package-key-types-pool-updatesettings.md).

## claude version measured

**2.1.239**, 2026-09-03, macOS, `-race`. Test
`internal/e2e/realclaude/bypass_approval_argv_probe_test.go`
(`TestRealClaude_BypassApprovalArgv_Probe`); fixtures
`internal/e2e/realclaude/testdata/bypass_approval_argv_v2.1.239_<arm>.json`. Five children, 52 s,
model `claude-haiku-4-5`.

## Why no earlier capture answered this

[`set-permission-mode-inband-probe.md`](set-permission-mode-inband-probe.md) (#1595) and
[`bypass-reescalation-probe.md`](bypass-reescalation-probe.md) (#2060) both deliberately spawned
a *bare* argv — no `--permission-prompt-tool`, no `--mcp-config` — because the #383 spike
concluded `--permission-prompt-tool stdio` short-circuits enforcement. Production's non-yolo
interactive spawn carries four flags neither probe had, and the reason no capture had ever
combined them with bypass is structural, not an oversight: `withApprovalArgs`
(`cmd/pyry/streamsup_runner.go`) returns early on `--dangerously-skip-permissions`, so production
itself never composes the two. #1686 would be the first thing that does.

## The five arms

| arm | launch flags | control request | position | primary read |
|---|---|---|---|---|
| `bypass_then_default` | bypass + the four approval flags | `default` | after turn 1 | turn 2 (Q2) |
| `approval_only` | the four approval flags | (none) | — | turn 2 (Q2 control); turn 1 (Q3 reference) |
| `prewrite_1..3` | bypass + the four approval flags | `default` | **before turn 1** | turn 1 (Q3) |

Every arm drives two turns on `setModePromptOne`/`setModePromptTwo`, keeping #1595's
index-symmetry rule intact: Q2 is classified at turn 2 on the arm and its control alike, Q3 at
turn 1 across all three references. The three `prewrite_*` arms differ from each other in name
alone — that is the point, since Q3's subject is whether the outcome is stable across spawns
rather than a one-run coincidence.

## Q2's verdict is taken on the socket, not on the behaviour

A bypass-launched child whose bridge never came back executes its tool ungated; a correctly
bridged child whose approval was *allowed* also executes ungated. Both read identically on every
field `setModeFieldMatches` compares — so a verdict taken on that comparison would report the
gate intact in exactly the case where it is gone.

The fix is structural rather than a comment: `bypassArgvGateVerdict(armApprovals,
controlApprovals int)` takes **no** `probeOutcome` parameter at all. The behavioural comparison
is still logged beside the verdict as corroboration, but the function that decides Q2 cannot see
it — the constraint is enforced by the signature, not by discipline. This is the general shape
worth reusing anywhere a measurement has two causes (bridge gone vs. bridge allowed) that produce
identical downstream behaviour: give the verdict function only the input that discriminates them,
even when a richer input is sitting right next to it.

A useful side effect: the arm's own turn 1 (bypass, zero approvals, ungated, but `mcp_servers`
reporting the server *connected*) is what separates "registered" from "consulted" — a connected
server that receives no request is not the same as a working gate, and this run demonstrates
both readings on the same child without needing a sixth arm.

## Q3's read: observe the moment, don't infer it from ordering

"Was the downgrade acked *before* turn 1" is a question about a moment, and a record written only
at the end of the run cannot answer it after the fact. The first-cut approach — deriving the
answer from stdout ordering (a `control_response` line appearing before the first `system/init`
line) — would have rested on an unstated assumption about when claude emits `system/init` relative
to a turn, and the run showed that assumption backwards: **the init line arrives *after* the
downgrade lands**, not before. All three `prewrite_*` children reported `default` at *their own
first* init line, meaning the downgrade landed before claude published its opening posture at
all — a stronger result than "before the tool ran," and one an ordering heuristic keyed on
`system/init` would have gotten wrong in the direction that looks safe.

The fix: `setModeChildConfig.mark func(stage string, controlResponses int)` is called by the
driver at each stage boundary with the control-response count *the driver itself already knows*,
so "acked before turn 1" is a read of driver state at the moment that matters, not an inference
from a log ordering that turned out to be backwards from what a first guess would predict.

## What is committed, and the redactor's honest limit

The mcp-config path and the stub socket path are run-local temp paths, matching two of
`dropcapFixedNeedles`' fixed deny classes (`/var/folders/`, `/private/var/folders/`); a fresh
random path per run would also make the argv-focused captures undiffable. `bypassArgvRedactor`
replaces both with fixed placeholders in the recorded argv and stderr capture before either is
written.

**A per-run redactor can only cover the paths *you* put on the argv.** claude echoes its own
working directory into `system/init`'s `cwd`, and that line is retained verbatim inside
`stdout_events` — so every capture in this package that pins HOME to a temp dir already carries a
`/private/var/folders/` path regardless of this redactor (12 occurrences in #2060's `enable`
fixture, 4 in #1595's `revoke` fixture). Rewriting a verbatim recording to hide that would defeat
the reason `stdout_events` is retained verbatim in the first place. The honest fix was to scope
the redactor to the argv and the stderr capture and say in the header what it does not cover,
rather than chase every place a subprocess might echo a path back.

The approval log records the tool *name* (capped via `truncateString`) and the arrival order,
never the tool input — that is model-composed bytes, and `writeSetModeFixture` runs no deny-scan.

## A footgun found while building the redactor

`strings.ReplaceAll(s, "", x)` does not no-op on an empty needle — it interleaves `x` between
every rune of `s`. A redactor built from a possibly-absent path (e.g. before the mcp-config
temp-dir is known) silently corrupts every string it touches instead of leaving it alone. Any
substitution built from a value that might be the zero string needs a guard and a test row for
the empty case; this is a Go gotcha, not a project-specific one, but this package now has the
fixture to prove it.

## What the rig gained, additively

Four knobs on the existing carriers (`setModeArm`, `setModeChildConfig`), all inert at the zero
value, following `secondTargetMode`/`promptThree`'s precedent from #2060:

- `setModeArm.extraLaunchArgs []string` — appended to the argv after the `launchYOLO` branch.
- `setModeArm.requestBeforeFirstTurn bool` — moves the *first* control request ahead of turn 1
  (#2060 moved the *second* request's position; this moves the first's).
- `setModeChildConfig.mark func(stage string, controlResponses int)` — see Q3 above.
- `setModeChildConfig.approval func() *bypassArgvApproval` — the per-arm socket observation,
  read once after the child has run.

All three existing construction sites (`setModeArms`, `permission_mode_switch_probe_test.go`,
`bypass_reescalation_probe_test.go`) use keyed fields and leave every new knob zero-valued, so
none of them changed.

## Reproduce

```bash
export CLAUDE_CODE_OAUTH_TOKEN=...   # or ANTHROPIC_API_KEY
go test -tags e2e_realclaude -race -count=1 -v \
  -run TestRealClaude_BypassApprovalArgv ./internal/e2e/realclaude/
```

~52 s, five children. Deterministic half (no credentials, no subprocess):

```bash
go test -tags e2e_realclaude -race -count=1 -run TestBypassArgv ./internal/e2e/realclaude/
```

## Related

- [`bypass-reescalation-probe.md`](bypass-reescalation-probe.md) — #2060, whose mechanism finding
  (claude gates escalation on launch argv, not current mode) this ticket builds on and whose
  `bypass_then_default`/`approval_only` shape and inline-arm-construction pattern this one reuses.
- [`set-permission-mode-inband-probe.md`](set-permission-mode-inband-probe.md) — #1595, source of
  the index-symmetry rule and `setModeFieldMatches`, the comparison this document's Q2 section
  explains why it cannot carry the verdict alone.
- [`permission-mode-switch-inband-probe.md`](permission-mode-switch-inband-probe.md) — #2041,
  source of `setModeChildConfig` and the `omitempty`-false-by-absence trap the rig avoids the same
  way for its nested capture fields.
- `docs/specs/architecture/2061-bypass-approval-argv-probe.md` — full design, security review and
  the resolved open questions.
