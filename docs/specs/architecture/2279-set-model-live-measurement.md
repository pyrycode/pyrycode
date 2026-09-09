# #2279 — measure the `set_model` control request live at claude 2.1.259

## Files read

- `internal/e2e/realclaude/set_permission_mode_probe_test.go` → `runSetModeChild`, `setModeChildConfig`,
  `setModeArm`, `setModeRecorder`, `setModeFixtureRecord`, `writeSetModeFixture`, `setModeControlLine` —
  the rig this ticket rides. It is already direct-exec (`exec.CommandContext`, owned `StdinPipe` /
  `StdoutPipe`, `bufio.Scanner`), which is the property the ticket's Technical Notes actually require.
- `internal/e2e/realclaude/effort_init_capture_test.go` → `effortInitPass`, `effortInitSummarise`,
  `effortInitPromotable`, `TestRealClaude_EffortInitCapture` — #2251, the most recent capture that rides
  the rig rather than copying it. Its fixture-absence gate, its `fillRecord`/`screenFixture` pair and its
  no-`omitempty` observation block are the shape this ticket reproduces on a different axis.
- `internal/e2e/realclaude/permission_mode_switch_probe_test.go` → `runModeSwitchDiscovery`,
  `modeSwitchAutoRows`, `modeSwitchModelValueOK`, `modeSwitchNameToken` — #2041. It already asks a live
  child for its published model list over an `initialize` control request, spending no assistant tokens,
  and it argues why a model must be selected from the run's own list rather than from a table.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `newDropcapRedactor`, `newDropcapScanner`,
  `dropcapFixedNeedles` — the redaction table and the deny-scan the ticket's "redact at the capture site"
  note points at.
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans` — the deterministic guard whose
  entries name `writeSetModeFixture` as the single fenced route to `packageDir`. It is why this ticket
  adds no sibling fixture writer.
- `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` → the committed `models` array. It
  publishes `value` and `resolvedModel` per row, which is what makes an alias distinguishable from a
  genuinely different model.
- `internal/e2e/realclaude/testdata/set_permission_mode_v2.1.220_revoke.json`,
  `testdata/permission_mode_switch_v2.1.239_acceptEdits.json` → the `system`/`init` lines. Both carry a
  `model` key holding a RESOLVED id (`claude-haiku-4-5`, `claude-sonnet-5`), never the alias sent.
- `docs/knowledge/features/e2e-realclaude.md` and the per-file overviews for the three probes above → the
  package's standing rules on captures, gates and skip-versus-pass.
- `CODING-STYLE.md` § Comments — Citing Other Code → symbol citations only, no line numbers.

## Context

The daemon changes a live child's model by writing `/model <name>` on stdin as an ordinary user turn, from
`Pool.deliverSettingsInBand`. Replacing that with a `control_request` of subtype `set_model` is the goal of
the family this ticket opens, and every sibling rests on one unverified premise: that claude accepts the
subtype at all. `git grep set_model` over this repo returns nothing — no encoder, no fixture, no fake-claude
arm. The premise's only source is a published type definition, and this repo has already been burned by one:
the same source promised a reasoning-effort field on `system/init` that #2251 then measured as absent.

This ticket changes no production code. It writes the line, records what comes back, and commits the
captures. A refusal of the subtype is a successful outcome, and the finding then belongs in the PR body.

**Why no ADR.** This is a measurement, not a decision. The design choice worth recording — whether the
daemon replaces the in-band `/model` turn with a control frame — belongs to #2280 and rests on what this
run observes.

**Sizing overage, stated rather than hidden.** The one-ticket boundary caps total written work at 800
lines; this plan prescribes roughly 1100 (a ~700-line probe, ~40 lines across two shared-rig widenings, and
this spec). Every other line of the boundary holds: zero production source files, zero new exported types,
two consumer call sites, five acceptance criteria. The refiner's own estimate was ~1500 and riding the
shared rig rather than copying it is what brings it down; the remainder is not divisible, because the rig
is one fixed cost shared by five arms and a per-arm child would have exactly one consumer. The floor rule
beats the ceiling, so this builds as one ticket.

## Design

### What it rides, and why not a copy

The ticket's Technical Notes say to copy `set_permission_mode_probe_test.go` and warn off
`inbandTapRecorder`. The warning is the load-bearing half: a supervisor tap can only carry lines the
production encoder knows how to write, and there is no `set_model` encoder. `runSetModeChild` is not a
supervisor tap — it builds its own argv, owns both pipes and splits lines with `bufio.Scanner` — so riding
it satisfies the constraint the note exists to enforce, and #2251 already established riding as this
package's pattern.

Riding also buys the fourth acceptance criterion outright. `setModeFixtureRecord` already carries
`stdout_events`, `non_json_line_count`, `stderr_capture`, `exit_code`, `wait_error`, `stdin_write_errors`,
`control_request_sent`, `control_request_id` and `control_response_request_id_matched`. A copy would
re-derive all nine and a second `packageDir` route besides — one that eleven `finOfflineExecBans` entries
name `writeSetModeFixture` precisely to fence off, and that a sibling writer would silently reopen.

### The one knob the rig is missing

`sendFirstRequest` inside `runSetModeChild` mints its line through `setModeControlLine`, which hardcodes
subtype `set_permission_mode` and a `mode` field. One additive, nil-defaulted field on
`setModeChildConfig` closes that:

```go
// controlLine, when non-nil, mints the control request line instead of
// setModeControlLine. nil on every caller before #2279, so every committed
// capture's bytes are unchanged.
controlLine func(requestID, target string) ([]byte, error)
```

That generalises `setModeArm.targetMode` from "a permission mode" to an opaque **target token** whose wire
meaning belongs to the minter. `targetMode == ""` keeps its existing meaning — send no request at all —
which is what the two #1595 control arms rely on.

### The three reset spellings

The reset arms are requests that ARE sent, so none of them may use the empty target token. The wire shapes
differ in whether the `model` key exists, so one struct expresses all three:

```go
type setModelRequestInner struct {
    Subtype string          `json:"subtype"`           // "set_model"
    Model   json.RawMessage `json:"model,omitempty"`   // nil ⇒ key absent
}
```

`omitempty` on a `json.RawMessage` omits a zero-length value, so nil mints `{"subtype":"set_model"}`,
`[]byte("null")` mints an explicit `"model":null`, and `json.Marshal(value)` mints a properly escaped
string. Structured marshalling throughout, never concatenation — `marshalInterruptEnvelope`'s
one-physical-line invariant, which the appended `'\n'` then relies on.

Two arms need a target token that is not a model name. They get reserved sentinels, and the minter maps
them; every other arm's token is the literal string that goes on the wire. A sentinel that collided with a
published model value would be silently misencoded, so the arm builder refuses one and an offline test
pins the refusal.

### The five arms

Each is one child, launched pinned on `--model claude-haiku-4-5` (the ticket's requirement, and #1595's
argv), driving two tool-free turns with the control request between them. The read is the ack plus turn 2's
`system/init` model — #2041's shape, not #1595's behavioural comparison, because a model change has a
direct observable and needs no control arms.

| arm | target token | wire `model` |
|---|---|---|
| `accept` | a discovered value resolving elsewhere than the launch model | that string |
| `refuse` | a shape-valid id no published row carries | that string |
| `reset_omitted` | sentinel | key absent |
| `reset_null` | sentinel | `null` |
| `reset_default` | `default` | `"default"` |

### Selecting the accept arm's model

`runModeSwitchDiscovery` already spawns one turn-free child, writes an `initialize` request and returns the
published rows — but `modeSwitchAutoRow` drops `resolvedModel`, and without it an alias cannot be told from
a genuinely different model. Launching on `claude-haiku-4-5` and requesting `haiku` would report the same
resolved id whether the change applied or not, which is the confound the ticket's "launch on a pinned
model" note exists to prevent.

So `runModeSwitchDiscovery` gains a second return value — the DERIVED `value → resolvedModel` mapping, not
the raw replies. Returning the raw would put a payload carrying `account` and `pid` one `%v` away from a log
line, and that discipline is stated in `runModeSwitchDiscovery`'s own doc; deriving inside the function that
already holds the discipline makes it structural rather than remembered. Only the mapping crosses out.

Selection: the first row whose `resolvedModel` differs from the launch model's, whose `value` passes
`modeSwitchModelValueOK`, and which is not a sentinel; preferring `sonnet`, `default`, `opus` and falling
back to arrival order. No qualifying row is a finding about claude's model list, reported and recorded
rather than fatal.

`modeSwitchModelValueOK` is reused here, and its rationale does NOT carry over intact — say so at the call
site, or a reader inherits a wrong one. #2041 applies it because its selected value reaches `--model`, where
a leading `-` parses as a flag. Nothing discovered reaches argv here: the launch model is a package
constant, and the selected value goes into a JSON-marshalled control request body. What the check buys on
this path is its LENGTH bound, which keeps a pathological published value from inflating a committed
capture, plus a narrow charset that keeps a path- or credential-shaped value off the wire.

### Two bounds on what claude controls

The observation block holds two lists whose length claude decides: the model-list pairs and each init
line's key set. Both are capped, in `effortInitKeyCap`'s shape and for its reason — a cap makes a
pathological line visible as a truncated list rather than as an unbounded record — and a truncation sets
its own flag so it is never silent.

### What the capture records

A new nested pointer on `setModeFixtureRecord`, `SetModelCapture *setModelObservation`, exactly as #2251
added `EffortCapture`. No `omitempty` on any inner field: an absent `model` key on an init line and an
empty one are different findings, and an omitted false would spell the first by absence.

The block carries, per arm: the spelling name, the model string sent verbatim (or the fact that no key was
sent), the launch model, the resolved model the selector expected, every `system/init` line's full sorted
key set with its `model` value and a `model_present` flag, the model list pairs the run observed, and the
deny-scan's per-class `credential_scan_applied` report.

### The redaction table has to cover the argv, or nothing lands

`dropcapDenyUsers` — the literal `/Users/` — is a FIXED deny needle, and `resolveClaudeBin` returns an
absolute path that on this operator's machine reads `/Users/<name>/.local/bin/claude`. Ten committed
captures in this directory already carry it, because their families predate the deny-scan and never ran
one. A screen that scanned this record without a rule for it would refuse EVERY arm, write nothing, and
burn five live children to land no capture at all.

So the redaction table gets two classes beyond `newDropcapRedactor`'s own `tempHome` / `artifactDir` /
`workdir` trio: the resolved claude binary path, and `realHome`. The binary path is substituted with a
named placeholder rather than dropped, so the recorded argv still reads as an argv and stays diffable
against the sibling families' captures. This is a REDACTION OF THE OPERATOR'S MACHINE, not of a credential:
`initControlScrubbed` remains the credential guard and runs first, unconditionally.

The offline redaction test asserts both directions on the same bytes, so a rule that stopped substituting
would redden rather than quietly re-arm the refusal.

### Fixture family

`set_model_v<versionSlug>_<arm>.json` under `testdata/`, minted the way `effortInitFixtureName` mints its
family — a literal prefix no input can reach, the version through `versionSlug`, the arm through
`modeSwitchNameToken` so no token can land a file outside `testdata/`. The name shares no literal head with
`set_permission_mode_v`, `permission_mode_switch_v`, `permission_protocol_v`, `dropped_lines_v`,
`initialize_control_v`, `bypass_*_v`, `compaction_v` or `effort_init_v`; an offline test asserts it against
each rather than leaving it a convention.

### Gate

The gate is the FIXTURE FAMILY'S ABSENCE, never a `PYRY_PROBE_*` variable. `make e2e-realclaude` sets no
custom variable, so an env-gated probe skips on the env check before the credential check and the live gate
passes vacuously — #2089's and #1763's failure. The test runs whenever any of the five captures is missing,
and a separate force variable exists only for a deliberate re-capture.

## Concurrency model

Inherited whole from `runSetModeChild` and unchanged: one child at a time, one reader goroutine per child
over `stdoutPipe`, closed over a `readerDone` channel so none outlives its child. `setModeRecorder` is
mutex-guarded because the reader appends while the test goroutine polls. The arms run sequentially with no
`t.Parallel` — `WithWorktreeAuthenticated` reaches `t.Setenv`, and all five arms share one pinned `$HOME`.

The discovery child has the same shape and is joined before the first arm starts.

`fillRecord` and `screenFixture` both fire from the test goroutine after the child has exited, so the
redactor's unsynchronised counters need no lock.

## Error handling

The test PASSES on every recorded outcome. A refusal of the subtype, a `control_response` that never
arrives, a reply whose `request_id` correlates to nothing, an init line with no `model` key — each is the
measurement, recorded and logged. Only an instrument that measured nothing fails:

- zero stdout lines on any arm — `runSetModeChild`'s own fatal
- no `control_response` and no `system/init` line at all, which means the child never launched
- a deny-scan hit, which writes NOTHING: not the fixture, not the artifact copy
- a record taken at a claude release other than the one this family is pinned at
- fewer than two init lines on an arm, which cannot answer the before/after read

The last two are `setModelPromotable`'s rules, in `effortInitPromotable`'s shape. A refusal blocks the
in-repo write alone; the artifact copy outside the worktree is written regardless, so a run that measured
the wrong release is still diagnosable.

Stderr goes through `initControlScrubbed` before it can reach any failure message, and is capped at
`stderrFixtureCap` in the record. Ordering is load-bearing in one place: `WithWorktreeAuthenticated` must
precede `newDropcapScanner`, because the scanner reads the two credential variables as deny needles and a
scanner built first takes an empty needle that `scan` reports as not-applied — silently.

## Testing strategy

The live capture is one test, `TestRealClaude_SetModelProbe`, gated on the family's absence. Everything
below it runs with no claude, no credentials and no subprocess:

- the three reset spellings mint the three intended wire shapes, and the key is ABSENT rather than empty on
  the omitted arm — asserted on the marshalled bytes, since that is the distinction the arm exists to draw
- the accept arm's minted line carries the model as a properly escaped JSON string, and a value containing
  a quote or a backslash does not break the one-physical-line invariant
- the selector refuses an alias resolving to the launch model, refuses a value `modeSwitchModelValueOK`
  rejects, refuses a sentinel, and reports the no-qualifying-row case as a finding
- the init summariser separates an absent `model` key from a present-but-empty one, over hand-built lines
  including a decoy `system` line of another subtype and a retained non-JSON line
- the fixture namer stays out of every other family's glob and resolves to a plain component inside
  `testdata/`, over the adversarial version tokens `setModeAdversarialVersionTokens` already carries
- `setModelPromotable` refuses another release and a record with fewer than two init lines
- the redaction pass removes what the deny-scan looks for, asserted in BOTH directions on the same bytes —
  a scan of the un-redacted bytes proves the needle runs, since a needle skipped as too short is reported
  as not-applied rather than failing, and a one-direction test would pass with the net off

Gate for the change as a whole: `go test -race ./internal/e2e/realclaude/...` plus a `go vet -tags
e2e_realclaude` and a `-run` over the offline tests, since the standard gate never compiles this package.

## Open questions

1. **Does claude 2.1.259 accept the subtype at all?** The whole point. Resolved by the run.
2. **Which reset spelling does claude honour?** #2280's encoder emits a plain string if `default` works and
   needs explicit-null support otherwise. Resolved by the three reset arms.
3. **Does the init line report the alias sent or a resolved id?** Every committed capture shows a resolved
   id for a launch flag; whether a control request behaves the same is unmeasured. The capture keeps both
   strings rather than a boolean, so the answer survives whichever way it falls.
4. **Does a refusal carry usable text?** #2281 can report a rejected model change to a client only if it
   does. #2041's `auto` refusal is the precedent to diff against.
5. **Whether the reset arms need a third turn.** A reset may only be observable after a turn that follows
   the one reading it. Two turns is the design; if turn 2's init line proves unreadable for a reset,
   `setModeChildConfig.promptThree` already exists and the fix is one field, recorded as a revision.

## Security review

**Verdict:** PASS (revised — the first pass returned FAIL on two MUST FIX findings, both now designed out
above and re-walked from the top)

**Findings:**

- [Trust boundaries] **MUST FIX, fixed.** The design has one boundary that matters: a subprocess's stdout
  becomes a file committed to a public repo. It was scattered across two crossings, and the second was the
  hole. `runModeSwitchDiscovery`'s doc records that its `initialize` reply carries `account` and `pid` and
  that the raw is neither recorded nor logged — and the first draft widened it to RETURN that raw to a
  caller holding none of that discipline, one `%v` away from a `t.Logf`. Fixed by returning the derived
  `value → resolvedModel` mapping instead, computed inside the function that already documents the rule.
  The remaining crossing is explicit and single: `setModelPass.screen`, which sees the marshalled record and
  is the only thing that writes.
- [Tokens, secrets, credentials] **MUST FIX, fixed.** `dropcapDenyUsers` is the fixed literal `/Users/`, and
  `resolveClaudeBin` returns an absolute path under it on this operator's machine — ten committed captures
  in `testdata/` already carry that string, from families predating the deny-scan. The first draft ran the
  scan over a record whose `argv[0]` is exactly that path, so the screen would have refused every arm and
  landed nothing after five live children. Fixed by adding the resolved binary path and `realHome` to the
  redaction table as named placeholders. Credential handling itself is inherited and sound:
  `initControlScrubbed` fatals before any bytes are written, `stderrFixtureCap` bounds the capture, the
  record has no `env` field, and `WithWorktreeAuthenticated` is ordered before `newDropcapScanner` so the
  two credential needles are non-empty — a scanner built first takes an empty needle and reports it as
  not-applied, silently.
- [File operations] No findings. The fixture name is minted through `versionSlug` and `modeSwitchNameToken`,
  which map every path metacharacter, and an offline test asserts the result is a plain component inside
  `testdata/` over `setModeAdversarialVersionTokens` — `..` and `../..` included. `writeSetModeFixture`
  writes through a temp file and a rename, so an interrupted run leaves no half-written capture. The
  fixture is 0644 because it is committed evidence; the out-of-worktree artifact copy is 0600. The gate's
  `os.Stat`-then-run is check-then-use, and is not a TOCTOU: the only writer is the operator running the
  gate.
- [Subprocess / external command execution] No findings, and the reason is categorical rather than a check.
  NOTHING discovered from a subprocess reaches an argv here: the launch model is a package constant and the
  selected value goes into a JSON-marshalled request body. That is what makes #2041's flag-injection hazard
  inapplicable, and the plan says so at the call site rather than letting `modeSwitchModelValueOK` carry a
  rationale that no longer holds. No `sh -c`; `exec.CommandContext` takes an arg slice. The environment is
  inherited deliberately, because the credential has to reach the child.
- [Cryptographic primitives] Not applicable, by design rather than by omission. The request ids are
  correlation tokens minted from the arm name, fixed per arm so the captures stay diffable — the same
  reasoning `(*Runner).Interrupt` and `initControlRequestID` state. Nothing is compared against a secret.
  One hazard is real and inherited: the redactor's nonce must be a genuine `time.Now().UnixNano()`, since a
  zero installs a one-byte `"0"` substitution rule that rewrites every zero digit in the record.
- [Network & I/O] No findings. No sockets and no HTTP. The line reader is capped at `setModeScanMax`, and
  an over-long line lands in `scanner_error` so truncation is never silent. The two lists claude controls
  the length of — the model-list pairs and each init line's key set — are capped with a truncation flag,
  in `effortInitKeyCap`'s shape.
- [Error messages, logs, telemetry] **SHOULD FIX** — the artifact copy must be written from the SAME
  screened bytes the fixture is written from, never from the pre-screen record. `effortInitPass.screen` is
  the pattern; a copy taken before the redaction would put an un-redacted record in a temp directory that
  outlives the run. Phase B obligation, and the verifier should check it landed. Otherwise sound: the
  deny-scan failure names CLASSES and never the value, which is the whole point of the scan, and stderr
  reaches no failure message before `initControlScrubbed` has run.
- [Concurrency] No findings. One child at a time, one reader goroutine per child closed over `readerDone`,
  a mutex-guarded recorder, and no `t.Parallel` on the live arms because `WithWorktreeAuthenticated`
  reaches `t.Setenv`. The arms write a shared map from sequential subtests. `fillRecord` and
  `screenFixture` both fire from the test goroutine after the child exited, so the redactor's counters need
  no lock.
- [Threat model alignment] The applicable threat is not in the mobile protocol spec — nothing here touches
  the relay. It is repo exfiltration: committing an operator's home path, session id, socket path or
  credential into a public repo, which is the ticket's own "redact at the capture site, not afterwards"
  note. The deny-scan plus the redaction table is the mechanism, and the offline test asserts both
  directions on the same bytes so a rule that stopped substituting reddens rather than quietly re-arming a
  refusal.
- [Threat model alignment] **OUT OF SCOPE** — the ten committed captures that already carry `/Users/` are
  not this ticket's to redact. Retro-redacting another family's evidence would rewrite what those runs
  observed, and the correct move is a ticket of its own. Naming it here so the deliberate inaction is
  findable.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-09
