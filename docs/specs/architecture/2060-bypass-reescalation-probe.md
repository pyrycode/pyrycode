# #2060 — measure the in-band bypass RE-escalation at 2.1.239 on the bare argv

## Files read

- `internal/e2e/realclaude/set_permission_mode_probe_test.go` → `setModeArm`, `setModeChildConfig`,
  `runSetModeChild`, `setModeFixtureRecord`, `writeSetModeFixture`, `setModeRecorder`,
  `setModeControlLine`, `setModeWaitFor`, `setModeTurnWindows`, `setModeOutcomeAt`, `probeOutcome`,
  `setModeFieldMatches`, `setModeNoDiscrimination`, `setModeResponseIDMatches`,
  `setModeAdversarialVersionTokens` — the rig this ticket rides, and the two things it does not
  yet carry: a second control request and a third turn. Its header also states the
  index-symmetry rule that forces every arm here to drive the same number of turns.
- `internal/e2e/realclaude/permission_mode_switch_probe_test.go` → `modeSwitchFamilyPrefix`,
  `modeSwitchNameToken`, `modeSwitchFixtureName`, `modeSwitchNamePattern`, `modeSwitchAnchor`,
  `TestModeSwitch_FixtureNamesAvoidRegressionGlobs` — #2041's landed precedent for building arms
  inline from an own names list, keeping an own fixture family, and asserting glob safety. Its
  pattern/anchor pair is REUSED here rather than copied a fourth time.
- `internal/e2e/realclaude/inband_bypass_revoke_names_test.go` → `setModeFamilyGlob`,
  `poolRevokeNamePattern`, `anchorFixtureName` — the family this must not join, and the table
  shape `modeSwitchNamePattern` was shaped after.
- The other committed families a capture must avoid: `permission_protocol_regression_test.go` →
  `fixtureGlob` and `expectedPermissionModeFromFilename` (the sweep that reads a filename's
  trailing token as an expected `init.permissionMode`); `dropped_line_capture_test.go` →
  `dropcapFixtureGlob`; `initialize_control_compare_test.go` → `initControlArmFixtureGlob`;
  `ask_user_question_reader_test.go` → `askQuestionFixtureGlob`.
- Shared helpers: `permission_protocol_spike_test.go` → `captureClaudeVersion`, `versionSlug`,
  `packageDir`, `truncateString`, `stderrFixtureCap`; `resilience_test.go` → `resolveClaudeBin`;
  `fixtures.go` → `WithWorktreeAuthenticated`; `initialize_control_probe_test.go` →
  `initControlScrubbed`, the credential guard `runSetModeChild` already calls.
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans` — read to confirm no
  entry needs adding. See "What is deliberately not touched".
- `internal/streamsup/runner.go` → `SetPermissionMode`, and `internal/streamsup/envelope.go` →
  `permissionModeAllowed`, `WritePermissionMode` — the two anchors the ticket names.
  `permissionModeAllowed` refuses `bypassPermissions` by NON-MEMBERSHIP, and its doc block calls
  #1595 "a claude-version fact rather than a guarantee — which is why it is the backstop here and
  this allow-list is the defence". That sentence is why this measurement changes no production code.
- `docs/knowledge/features/set-permission-mode-inband-probe.md` — #1595's finding doc: it
  documents `-run TestRealClaude_SetPermissionMode` as the reproduce command (hence a different
  test-name prefix here), and its per-arm table gives the init echo `revoke` →
  `[bypassPermissions, default]` that the downgrade gate reads at index 1.
- `docs/knowledge/features/permission-mode-switch-inband-probe.md`, `CODING-STYLE.md`,
  `docs/PROJECT-MEMORY.md` — package conventions.

## Sizing

Re-counted against this written plan: **~865 lines of total written work** — this plan (~305), a
new measurement file (~420), and an additive widening of the shared rig (~140) — against the
size-S ceiling of 800. **The line boundary is exceeded by ~65 and the ticket is built anyway**,
because split depth is closed: `gh api graphql` reports parent #2059, grandparent #1686, so
§ A1's depth cap fires and the ticket carries `needs-human:sizing` rather than a split proposal.

Every other boundary is clear. **Zero production source files** — everything lands in `*_test.go`
behind the `e2e_realclaude` build tag, plus this plan. No new exported types (the package is
internal; every symbol added is unexported). **Zero** call sites needing simultaneous update:
both widenings are new fields at keyed-literal construction sites, so `setModeArms`, #2041's
inline literal and both `runSetModeChild` callers are untouched. Five acceptance criteria. No
state machine.

The overage is entirely this plan: the ticket estimated ~200 for it and the mandatory
`## Security review` section put it at ~305. The code halves land on the ticket's own numbers.
Reusing `modeSwitchNamePattern`, `modeSwitchAnchor`, `modeSwitchNameToken` and
`setModeAdversarialVersionTokens` rather than re-authoring them is what keeps the measurement
file at ~420 — a fourth copy of that table would be ~60 lines asserting what the third already does.

The floor rule also binds independently, and it outranks the ceiling. There is one deliverable
here — a committed live measurement of the re-escalation at 2.1.239, with verdicts — and the rig
widening has exactly one consumer, this measurement. A slice carrying only the widening could not
be verified without its sibling, so it is not a ticket.

## Context

#1686 wants every claude child launched with `--dangerously-skip-permissions` and downgraded
in-band at once, so no posture change needs a respawn. It rests entirely on one behavioural
observation: a child launched **with** the flag, dropped to `default`, then asked back into
`bypassPermissions` **in the same child**, accepts both. That observation was taken by hand on
2026-08-21 against 2.1.220 and **has no committed capture**. It is not #1595's: #1595 measured
`revoke` (with the flag, down to `default` — APPLIED) and `enable` (without the flag, straight
up — FAILED, refused in words naming the launch argv). No fixture in this repo records a
two-request child, and `setModeArm` carries a single `targetMode`, so there is no 2.1.220
re-escalation baseline to diff against. This is the first committed measurement.

It measures and records. No production writer, no daemon behaviour change — the boundary #1595
and #2041 both held. A FAILED verdict here closes #1686 as answered-no and leaves
`permissionModeAllowed`'s non-membership refusal exactly as it stands.

This work warrants no ADR: it adds no decision, only evidence for one #1686 will make.

## Design

### The drive sequence — four arms, three turns each

| arm | launch flag | control 1 (after turn 1) | control 2 (after turn 2) | classified at |
|---|---|---|---|---|
| `reescalate` | `--dangerously-skip-permissions` | `default` | `bypassPermissions` | **turn 3** |
| `enable` | *(none)* | `bypassPermissions` | *(none)* | **turn 2** |
| `control_default` | *(none)* | *(none)* | *(none)* | turns 2 and 3 |
| `control_bypass` | `--dangerously-skip-permissions` | *(none)* | *(none)* | turns 2 and 3 |

All four drive **three** turns. This is forced, not stylistic: `set_permission_mode_probe_test.go`'s
header states that a measurement arm's post-change read and its control must sit at the same
turn index or the verdict folds in a turn-index confound. `reescalate`'s post-re-escalation read
is a turn 3, so its controls must have one. `enable` keeps #1595's shape and is read at turn 2,
against the same controls' turn 2 — which is what makes AC 3's "the probe still discriminates"
directly comparable to the committed 2.1.220 result. Each direction therefore names its own read
index, and the no-discrimination check (AC 5) is evaluated **per direction at that index**
rather than once up front as #1595 does, because two indices can discriminate independently.

Prompts: three Bash probes of the `ls -la <dir>` shape #1595 uses, differing from each other so
turn 3 is a fresh request rather than one claude answers with "I already did that". Behaviour is
the verdict here (AC 1), so the turns cannot be tool-free the way #2041's are. Turns 1 and 2
reuse #1595's `/` and `/tmp` verbatim, for comparability; turn 3 reads `/usr` — read-only,
package-manager content, and deliberately not `$HOME`, the workdir or `/etc`, because every
turn's stdout is committed to a public repo.

`maxTurns` is `"12"` on this measurement's config — #1595's `"8"` is 4× headroom over two Bash
probes, and this drives three. It is set in `setModeChildConfig`, never on the shared constant.

### The rig widening — additive, two fields and one block

- `setModeArm` gains **`secondTargetMode string`**. Empty means "send no second control request",
  which is what both existing keyed construction sites get for free.
- `setModeChildConfig` gains **`promptThree string`**. Empty means "stop after turn 2" — the
  existing behaviour, unchanged for #1595's four arms and #2041's five.
- `runSetModeChild` gains one block after turn 2's wait, guarded on `cfg.promptThree != ""`:
  optionally write the second control request (guarded on `arm.secondTargetMode != ""`, waiting
  on a `controlResponseCount` **baseline+1**, not an absolute 2, so an unsolicited reply cannot
  satisfy the wait), then drive turn 3. `Prompts` records three entries when it ran.
- `setModeFixtureRecord` gains **`SecondRequest *setModeSecondRequest`** with
  `json:"second_control_request,omitempty"` — a nil pointer, not four flat `omitempty` fields.
  Its `control_response_id_matched` bool has a meaningful FALSE (a reply correlating to nothing),
  and an `omitempty` bool spells false by absence — the trap #2041's `supports_auto_mode` records.
  A nil pointer says "no second request was sent"; a present one carries all four fields.

The second request's id is `set-permission-mode-<arm>-2` — fixed per arm and diffable, the same
argument `runSetModeChild` already makes for the first.

### The verdict, and the downgrade gate (AC 2)

Verdicts are behavioural whole-value comparisons of `probeOutcome`, never the echoed
`control_response` (AC 1). A new direction type carries `readTurn` alongside #1595's
applied/failed control pair and verdict strings; `setModeFieldMatches` renders the per-field
breakdown so an INCONCLUSIVE stays readable.

The downgrade gate is the new machinery, and it exists because a child that never left bypass
behaves like the bypass control either way, making a re-escalation verdict on it vacuous. One
predicate answers `(landed bool, reason string)` from the arm's record and the two turn-2
controls, in this order:

1. **Launch check.** `init_permission_modes[0]` must read `bypassPermissions`. If it does not,
   the arm never started in the posture the whole measurement assumes.
2. **The init read.** With at least two init lines, `init_permission_modes[1]` — the line the
   turn AFTER the downgrade emits, which is the read #1595 recorded as `[bypassPermissions,
   default]` — must read `default`.
3. **Behavioural fallback**, only when fewer than two init lines were seen: the arm's turn 2 must
   equal `control_default`'s turn 2 while the two controls differ at that index.

Anything else reports **`RE-ESCALATION NOT MEASURED`** with the reason naming which read failed
and what it said. That is a recorded outcome, so the test still passes (AC 5); a broken
instrument — no child, no stdout — still `t.Fatalf`s inside `runSetModeChild`.

An absent `control_response` is logged, never `t.Errorf`'d. #2041 errors there because its read
IS the ack; here AC 1 forbids the echo from entering the verdict, so its absence does not blind
the instrument.

### The fixture family

`bypass_reescalation_v<versionSlug>_<armToken>.json`, minted by a namer whose prefix is a
literal no input can reach, arm token sanitised through `modeSwitchNameToken` (which preserves
case, unlike `versionSlug`). It is written through **`writeSetModeFixture`** with the namer
passed as its `fixturePath` parameter — never a sibling writer, for the reason that function's
own doc gives: a second route to `packageDir` that every `finOfflineExecBans` entry silently
fails to cover.

### What is deliberately not touched

`finOfflineExecBans` gets no entry. Its keys are the files whose headers claim offline purity;
this measurement adds no such file (the deterministic name test lives in the same file as the
live probe, as #2041's does), and no listed file can reach the new namer. #2041's
`modeSwitchFixturePath` is likewise absent from those lists. Adding a defence for a failure mode
nobody has observed is out of scope here.

## Concurrency model

Unchanged from the rig: one child at a time, one reader goroutine per child closing over
`readerDone` so none outlives its child, `setModeRecorder` mutex-guarded because the reader
appends while the test goroutine polls. The third turn adds no goroutine and no shared state —
it is two more writes on the stdin the same goroutine already owns. No `t.Parallel` on the live
test; the deterministic name test is parallel and touches no process.

## Error handling

Every non-instrument outcome is recorded, not repaired: a turn that never closed
(`result_observed` false), an absent or non-correlating `control_response` on either request, a
missing init line, a stdin write error, a tripped context deadline, a non-zero exit, a scanner
error. `t.Fatalf` is reserved for a broken instrument, and `initControlScrubbed` runs before any
stderr can reach a failure message.

One known bound, recorded rather than widened: `setModeChildBudget` is 4 minutes per child and
three `setModeTurnBudget` waits can sum past it on a fully stalling arm. That is already the
documented behaviour — the deadline trips, `context_deadline_tripped` lands in the fixture, the
run does not hang. Observed per-turn time at this argv is seconds, so raising a shared constant
for an unobserved failure is not warranted.

## Testing strategy

- **The live probe** — one test, four arms, run under `-tags e2e_realclaude -race -count=1 -v`
  with a `-run` prefix that is neither `TestRealClaude_SetPermissionMode` nor
  `TestRealClaude_InBandModeSwitch`, so it cannot sweep either sibling's children into a run
  that budgeted four. Verdicts are logged; the test passes on every recorded outcome. Its
  captures are `git add`ed in the same commit — a run that spends tokens and lands nothing does
  not satisfy this ticket.
- **The deterministic half** — no subprocess, no credentials: every name the namer can mint, over
  `setModeAdversarialVersionTokens` plus a token per family and a set of hostile arm tokens
  (`a/b`, `..`, `/abs`, `""`), matches none of the six committed families' globs; every pattern
  can still match a synthetic control of its own shape (or the negatives are vacuous); every
  minted path stays a plain component directly inside `testdata/`; the four arms mint four
  distinct names.
- **The finding block** — after the live run, each verdict lands in the new file's header doc
  comment naming claude 2.1.239 and the run date, in the shape
  `set_permission_mode_probe_test.go`'s header uses (AC 4).
- `go vet -tags e2e_realclaude ./internal/e2e/realclaude/` is run explicitly: `make check` never
  compiles this package, so a green standard gate says nothing about it.

## Open questions

1. **Does claude emit exactly one `system/init` line per turn on a three-turn child?** #1595
   observed one per turn across two turns. If a three-turn child emits a different count, the
   index-1 init read is wrong and the gate falls through to its behavioural fallback. Resolve by
   reading `init_permission_modes` in the run's own log; record the resolution under `## Revisions`
   if the gate's ordering has to change.
2. **Does the re-escalation request produce a `control_response` at all, and with which
   subtype?** Recorded either way; it never enters the verdict.
3. **Is `"12"` enough turn headroom for three Bash probes with a possible retry after a denial?**
   An `error_max_turns` result lands in the fixture plainly. Raise and rerun if it fires.

## Revisions

### 2026-09-03 — the live run resolved all three open questions; the design did not change

The measurement ran clean at 2.1.239 (four children, 56 s, exit 0 on every arm, no tripped
deadline). Recorded here because a plan whose Open Questions are left standing cannot be diffed
against the code that answered them.

1. **One `system/init` line per turn on a three-turn child — confirmed.** Every arm emitted
   exactly three, so the gate took its primary INIT path and the behavioural fallback was not
   exercised live. The fallback stays in, unit-tested by
   `TestReescalateDowngrade_RefusesAVacuousReEscalationVerdict`: it exists for the run where the
   init read is missing, and dropping it because one healthy run did not need it is how a probe
   stops discriminating.
2. **The re-escalation does produce a `control_response` — `subtype:"success"` echoing
   `bypassPermissions`, correlated by `request_id`.** It did not enter the verdict, per AC 1;
   the behavioural read agreed with it independently.
3. **`"12"` was ample** — no arm approached it and no `error_max_turns` fired.

One result worth naming beyond the questions: the `reescalate` arm's turn 2 came back **gated**
between two ungated turns, with its init line reporting `bypassPermissions → default →
bypassPermissions` in step. The downgrade gate is therefore confirmed by measurement rather than
only by construction — the child demonstrably left bypass before the re-escalation was asked for,
which is exactly what AC 2 exists to establish.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings, and the design decision is the reason. There is exactly one
  untrusted→trusted crossing — claude's stdout into `setModeRecorder.add` — and everything this
  measurement derives from it (`probeOutcome` scalars, `init_permission_modes`, verbatim
  `control_response` lines) is only **compared to package constants, logged, or written to the
  fixture**. Critically, **no value read from a child's stdout becomes another child's argv**:
  every model, prompt, mode, arm name and request id here is a package constant, so the hazard
  `modeSwitchModelValueOK` exists for at #2041 has no counterpart in this ticket. The one
  subprocess-derived value that does reach a filesystem path is the version token from
  `captureClaudeVersion`, which passes through `versionSlug` and is covered by the containment
  and glob assertions the deterministic test runs over `setModeAdversarialVersionTokens`
  (`..`, `../..`, `permission_protocol`, a 64-char run, and the empty string included).
- **[Tokens, secrets, credentials]** No findings. The credential reaches the child through the
  environment (`WithWorktreeAuthenticated`) and never through argv; `setModeFixtureRecord` has no
  `env` field by design; `initControlScrubbed` runs before any stderr byte can reach a failure
  message and `stderrFixtureCap` bounds what lands in the file. Two decisions keep it that way
  here: this file logs only derived scalars and the verbatim `control_response` lines #1595
  already commits — **never `StderrCapture` and never raw stdout** — and it drives **no
  `initialize` request**, so the `account`-bearing reply `runModeSwitchDiscovery` fences off is
  never asked for. The four fields `SecondRequest` adds carry a package constant, a
  package-derived literal, a line marshalled from both, and a bool.
- **[File operations]** SHOULD FIX, resolved in the plan above. Every turn's stdout is committed
  to a public repo, so the third prompt's directory is a disclosure decision, not a cosmetic one:
  the plan now names `/usr` — read-only, package-manager content — and rules out `$HOME`, the
  workdir and `/etc`. Turns 1 and 2 keep #1595's `/` and `/tmp` for comparability with the
  committed 2.1.220 captures. Otherwise no findings: the write is `writeSetModeFixture`'s
  temp-file-plus-rename at `0o644` under a `0o755` `testdata/`, so an interrupted run cannot
  leave a half-written fixture for a later `git add`; there is no stat-then-open, hence no TOCTOU;
  and the path is a plain component inside the repo's own `testdata/`, asserted for every name
  the namer can mint including hostile arm tokens (`a/b`, `..`, `/abs`, `""`).
- **[Subprocess execution]** No findings, one accepted and bounded property stated. `exec.CommandContext`
  takes an arg slice — no shell, no `sh -c` — and every element is a constant. The property worth
  naming: two arms launch claude with `--dangerously-skip-permissions` and run Bash unsandboxed on
  the operator's machine, and on `reescalate` turn 3 may run in bypass if the escalation lands.
  That is #1595's existing exposure, not new, and it is bounded four ways — constant read-only
  `ls` prompts, `--max-turns 12`, a fresh EMPTY non-git workdir under the pinned `$HOME` so claude
  loads minimal project context, and `setModeChildBudget`'s hard kill. The measurement cannot be
  taken without it: a bypass-launched child IS the subject.
- **[Cryptographic primitives]** Not applicable, by design rather than omission. The only
  identifiers minted are `request_id` correlation tokens, fixed per arm on purpose — the same
  reason `(*Runner).Interrupt` mints from a monotonic counter rather than a CSPRNG. They correlate
  a reply on a pipe this process owns; a random one would rewrite every committed capture on each
  run. No randomness here is security-relevant.
- **[Network & I/O]** OUT OF SCOPE, named. No network. Reads are capped: `setModeScanMax` bounds
  one line and an over-cap line lands in `scanner_error`, so truncation is never silent; the
  second-control wait is bounded by `setModeControlBudget` (baseline+1, never an unbounded wait).
  What has no cap is `setModeRecorder.lines` — total retained stdout, which this ticket grows by
  roughly half by adding a third turn. It is bounded in practice by `--max-turns` and the child
  deadline, and capping it means changing a shared recorder for a failure nobody has observed
  across #1595's and #2041's nine committed captures. Deferred; it belongs to whoever next
  measures a longer child, not here.
- **[Error messages, logs, telemetry]** SHOULD FIX. The `RE-ESCALATION NOT MEASURED` reason quotes
  what the init read actually said, which is a subprocess-supplied string of no bounded length.
  Pass it through `truncateString` before it reaches `t.Logf`, so a pathological
  `init.permissionMode` cannot dump into a run log that gets salvaged. No injection surface
  otherwise — it reaches a log, never argv or a path.
- **[Concurrency]** No findings. One reader goroutine per child, closed over `readerDone`, exiting
  on the EOF that follows either the stdin close or the context kill; the third turn adds no
  goroutine and no shared state, only two more writes on the stdin the test goroutine already
  owns. `setModeRecorder`'s counters are mutex-guarded, and the new
  `baseline := controlResponseCount()` then wait-for-`baseline+1` is check-then-wait on a
  monotonically increasing counter, so a concurrent increment can only satisfy the wait early,
  never lose it. Shutdown order is unchanged: close stdin, `cmd.Wait()`, `<-readerDone`.
- **[Threat model alignment]** No findings, and this is the category that matters most for this
  ticket. The daemon's structural fail-safe against an in-band escalation is
  `permissionModeAllowed`'s refusal of `bypassPermissions` by NON-MEMBERSHIP, backing
  `SetPermissionMode`'s contract. **This ticket changes no production code and adds no production
  writer**, so that fail-safe is untouched whatever the verdict says. The finding block must state
  that explicitly: a measurement that claude ACCEPTS a re-escalation is not the daemon acquiring
  the ability to ask for one, and #1686 — not this ticket — decides whether anything is built on
  it. The escalation literal appearing in this file's arm table is confined to a `*_test.go` behind
  the `e2e_realclaude` tag, exactly as `setModeArms` already carries it; production source still
  never contains it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-03
</content>
</invoke>
