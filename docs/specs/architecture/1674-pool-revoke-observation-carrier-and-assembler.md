# #1674 — the Pool-revoke observation carrier and its pure assembler

One arm's live readings, lifted into a value, and one pure function that turns that
value into the eighteen-field `poolRevokeFixtureRecord` #1675 commits. Everything
this ticket ships settles offline: no child, no turn, no fixture write, no
`testdata/` in either direction.

## Files read

- `internal/e2e/realclaude/inband_bypass_revoke_fixture_test.go` → `poolRevokeFixtureRecord`
  (the eighteen fields, their tags, and the doc constraint that `ChildOutputCapture`
  absorbs free text while `ControlResponses`/`StdoutLines`/`NotDeliveredLogs` stay
  uncapped), `poolRevokeFullRecord` (the intended shape of every field, especially
  `StdoutLines`' two-element literal), `fixtureFieldNonZero` (the kind-aware zero
  judgement this ticket's sweep reuses), `writePoolRevokeFixture` and
  `capFixtureCapture` (named so the assembler re-applies neither).
- `internal/e2e/realclaude/inband_bypass_revoke_arms_test.go` → `poolRevokeArm`,
  `poolRevokeArms`. The three rows and their `name`/`launchYOLO`/`takesSettingsUpdate`;
  READ-ONLY, ranged over from parallel tests elsewhere. Only `revoke` carries
  `takesSettingsUpdate: true`, and `control_default` carries `launchYOLO: false` —
  both facts decide which arm AC 4's row must use.
- `internal/e2e/realclaude/inband_bypass_revoke_harness_test.go` → `poolRevokeHarness`,
  `newPoolRevokeHarness`. The handles a live driver holds; the carrier is the
  *readings taken from* one of these, never a second copy of it. This file builds no
  harness.
- `internal/e2e/realclaude/set_permission_mode_probe_test.go` → `probeOutcome` (the
  per-turn behavioural read), `setModeRecorder`'s `add` (the JSON-quoting of a
  non-JSON run — the whole reason `StdoutLines` needs a conversion rather than an
  assignment) and the four `snapshot*` accessors with their return types.
- `internal/e2e/realclaude/interactive_stream_inband_bypass_revoke_test.go` →
  `revokeTap` (promotes the accessors), `newRevokeLogRecorder` (the `spawns` and
  `notDelivered` closures the carrier holds readings from).
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans` and
  `TestFinOfflineFilesReachNoExecHelper`. The entry for
  `inband_bypass_revoke_names_test.go` is this ticket's base (fourteen names); the
  entry for `initialize_control_names_test.go` states the reasons for the two
  additions.
- `internal/e2e/realclaude/permission_protocol_spike_test.go` → `stderrFixtureCap`,
  the byte cap AC 3's "the assembler applies no cap of its own" row is measured
  against.
- `docs/knowledge/features/e2e-realclaude-inband-bypass-revoke-arms-test-go.md` — the
  family overview. Carries #1651's lesson that a nested assertion is never the sole
  red for a mutant that trips its guard, which is why the zero sweep and the mapping
  table below are separate flat checks rather than one nested walk.

## Context

#1675 drives three arms through one identical two-turn probe and commits one
artifact per arm. #1662 already shipped the artifact's type and its writer; what is
missing is the step between a live drive and that record — and inside a live driver
that step is unverifiable. The run costs 8–14 minutes, and *some* zeros in a
committed artifact are honest measurements (a control's `TakesSettingsUpdate`, an
untripped deadline, an empty not-delivered slice), so a human reading three JSON
files cannot tell a field nothing filled from a field measured as zero.

Lifted out as a pure function over a value, that judgement becomes an offline table
running in milliseconds. This is the same seam #1651, #1661, #1662 and #1673 used:
settle offline everything that does not need a live child.

The carrier also makes one requirement structural rather than advisory: the two
control arms must read their pid pair at the same two points at which `revoke` takes
its settings update. A named field pair on the carrier is a thing a driver must
fill; a sentence in the driver is a thing someone has to honour. AC 4's row is what
gives that teeth — it asserts the pid pair is *filled* on a control arm, so a driver
that skipped the reads produces a record this test calls wrong.

No ADR is warranted: this adds no decision the family has not already made.

## Design

One new file, `internal/e2e/realclaude/inband_bypass_revoke_assemble_test.go`, same
package and same `//go:build e2e_realclaude` tag as the family. Zero production
files.

### The carrier

`poolRevokeObservation` — a struct of unexported fields holding exactly what one
arm's drive observes, and nothing derived:

| Carrier field | Type | Feeds |
|---|---|---|
| `arm` | `poolRevokeArm` | `Arm`, `LaunchYOLO`, `TakesSettingsUpdate` |
| `claudeVersionRaw`, `claudeVersion` | `string` | the two version fields |
| `argv` | `[]string` | `Argv` |
| `prompts` | `[]string` | `Prompts` |
| `spawnCount` | `int` | `SpawnCount` |
| `pidBefore`, `pidAfter` | `int` | `PIDBefore`, `PIDAfter` |
| `controlResponses` | `[]json.RawMessage` | `ControlResponses` |
| `initModes` | `[]string` | `InitPermissionModes` |
| `stdoutLines` | `[]json.RawMessage` | `StdoutLines` — **converted**, see below |
| `turnBoundaries` | `[]int` | `TurnBoundaries` |
| `probeOutcomes` | `[]probeOutcome` | `ProbeOutcomes` |
| `notDeliveredLogs` | `[]string` | `NotDeliveredLogs` |
| `deadlineTripped` | `bool` | `DeadlineTripped` |
| `childOutputCapture` | `string` | `ChildOutputCapture` |

Seventeen carrier fields onto eighteen record fields; the arm row expands to three.
The types are the *source's* types, not the record's — `stdoutLines` is
`[]json.RawMessage` because that is what `snapshotLines` returns, and making the
carrier hold the record's `[]string` would move the conversion back into the live
driver where nothing can test it.

The carrier holds no `env` field and must never hold one, for the record type's own
reason: the credential reaches the child through the environment while the argv
carries none.

Its doc comment carries one constraint for #1675: every slice on it must be a
*snapshot* — the tap's `snapshot*` accessors and the log recorder's `notDelivered`
closure each return a fresh copy under their own mutex. A carrier built from a
recorder's live fields would be marshalled while the child is still writing, which
is a data race no test here can see.

`pidBefore`/`pidAfter` carry the same-two-points requirement in their doc comment.
No field records which point that was — the requirement is that the two controls
read at the *revoke* arm's two points, which only #1675 can honour.

### The assembler

```go
func assemblePoolRevokeFixture(obs poolRevokeObservation) poolRevokeFixtureRecord
```

No `*testing.T`, no I/O, no mutation of `obs` or of package state. It returns a
value rather than a pointer, so #1675 writes
`rec := assemblePoolRevokeFixture(obs); writePoolRevokeFixture(t, dir, &rec)`.

Body: one struct literal, field per field, plus the one conversion below. It applies
no cap — `writePoolRevokeFixture` caps `ChildOutputCapture` through
`capFixtureCapture`, and a second cap here would silently bound the structured
evidence the artifact exists to carry.

It copies no slice: the carrier's slices already come from the tap's `snapshot*`
accessors, which each return a fresh copy, so the record shares backing arrays with
a carrier that is itself a snapshot. Sharing is not mutation, and the no-mutation
row below pins that the assembler does not sort or truncate in place.

### The stdout conversion

`poolRevokeStdoutLines([]json.RawMessage) []string`, the only non-assignment in the
mapping and the reason it is a named function.

`setModeRecorder`'s `add` retains a valid JSON line verbatim and retains a *non-JSON*
run by `json.Marshal`-ing it as a JSON string. So the tap's slice mixes two encodings,
and the record's `[]string` wants the bytes claude actually emitted in both cases:

- a raw message that decodes as a JSON string → the decoded string (the quoting the
  tap added is removed)
- anything else → `string(raw)`, verbatim

which is exactly the two-element shape `poolRevokeFullRecord`'s own `StdoutLines`
literal already shows. The ambiguity to state rather than hide: a line claude emitted
as a bare JSON string literal is valid JSON, is retained verbatim by the tap, and is
unquoted here. claude's stream-json emits objects, so it cannot arise; the tap keeps
no per-line non-JSON flag, so nothing could distinguish it if it did; and the
alternative — carrying the tap's quoting into the artifact — damages every genuinely
non-JSON line, which is the case that actually occurs.

### The zero sweep

`poolRevokeZeroFields(rec poolRevokeFixtureRecord) []string` reflects over the record
*type*, judges each field with #1662's `fixtureFieldNonZero`, and returns the JSON
tags of the zero-valued ones in declaration order. A nineteenth field added to the
record later is swept with no edit here — which is the point, and the precise failure
#1662's doc warns about, since a field nothing fills reads back as a zero in a
*committed* file.

#1662's own round trip is a hand-written eighteen-row list and is correct for what it
asserts: a round trip names both sides of each row. It is not the model for the
sweep. It *is* the model for the mapping table below, for the same reason.

Every record field is exported, so `Field(i).Interface()` is safe. The helper is
reported by tag rather than Go field name because the tag is what a reader of the
committed artifact sees.

## Testing strategy

Three tests, all `TestPoolRevoke*` so the ticket's `-run` filter reaches them.

**`TestPoolRevokeAssemble_FillsEveryFieldFromItsOwnObservation`** (AC 1, AC 2), over
`poolRevokeFullObservation()` — every observable distinct and non-zero, its `arm`
looked up by name from the real `poolRevokeArms`:

- the type-derived sweep returns no zero fields;
- a hand-written mapping table of `{tag, want, got}` rows asserts each observable
  landed in the field that names it — this is what catches a swapped pid pair,
  `InitPermissionModes` fed from `StdoutLines`, and crossed version fields;
- a distinctness pass over the same rows' `want` values (skipping bools, on #1662's
  ground) keeps the mapping table non-vacuous: two same-typed fields carrying the
  same value could satisfy each other's row;
- this carrier's `childOutputCapture` is SHORT, well under `stderrFixtureCap`. The
  mapping table's failure message prints want-vs-got, so an over-cap capture here
  would dump 8 KiB into the run log on any unrelated red. The over-cap case belongs
  to AC 3's test, whose messages report lengths only;
- no-mutation: a second, independently constructed carrier compares
  `reflect.DeepEqual`-equal to the one the assembler was handed. Constructed twice
  rather than shallow-copied on purpose — a shallow copy shares backing arrays, so an
  in-place sort would mutate both and the row would pass.

**`TestPoolRevokeAssemble_RoutesFreeTextToChildOutputCaptureAndConvertsStdoutLines`**
(AC 3), one carrier whose free-text capture and structured stdout lines are mutually
distinguishable by a marker substring the stdout lines do not contain:

- no element of `StdoutLines` contains the marker; `ChildOutputCapture` equals the
  carrier's capture byte for byte;
- the conversion rows: a valid-JSON line arrives verbatim, and a line the tap retained
  by JSON-quoting a non-JSON run arrives as the bytes claude emitted — no added
  quoting in the committed artifact;
- no cap: the capture is longer than `stderrFixtureCap` and survives at full length,
  and `ControlResponses`, `StdoutLines` and `NotDeliveredLogs` keep both their element
  counts and their longest element's length. Failure messages report lengths and at
  most a short prefix, never the capture itself — printing it would move 8 KiB out of
  a bounded field and into an unbounded run log.

**`TestPoolRevokeAssemble_ControlArmLeavesOnlyItsHonestZeros`** (AC 4), a two-row
table over control-arm carriers:

- row one, the healthy control: no settings update, no failed delivery, no tripped
  deadline. The sweep returns exactly `takes_settings_update`, `not_delivered_logs`
  and `deadline_tripped`; every other field is filled. **The arm is `control_bypass`,
  not `control_default`** — `control_default` is seeded `launchYOLO: false`, which
  makes `LaunchYOLO` a *fourth* honest zero and AC 4's exact claim false of it. That
  is a property of the arm table, so the row takes its arm from `poolRevokeArms` by
  name rather than from a synthetic literal.
- row two, a control whose deadline tripped and whose turn never closed — a
  legitimate observation. The sweep returns exactly `takes_settings_update` and
  `not_delivered_logs`.

Row two exists for one reason, stated so nobody deletes it as redundant: the record's
three bools are all `true` in the full carrier, so that test cannot discriminate them
pairwise. Row one separates `LaunchYOLO` from the other two; row two is the only
place `TakesSettingsUpdate` and `DeadlineTripped` differ, and without it an assembler
that crossed those two sources is green everywhere.

This test is also the sweep's own vacuity control: it is the only place the sweep is
asserted to *report* a zero, so a broken reflection walk that returns nothing cannot
leave AC 2 green.

## Offline enforcement

One entry appended to `finOfflineExecBans`, keyed
`"inband_bypass_revoke_assemble_test.go"`: #1661's fourteen-name entry for
`inband_bypass_revoke_names_test.go` copied whole — the four skip-keepers,
`os.Getenv`/`os.Environ`, and the `packageDir`/wrapper/`filepath.Glob`/`os`
read-write group that fences the file off from the committed `testdata/` — plus
`captureClaudeVersion` and `os.LookupEnv` for the reasons
`initialize_control_names_test.go`'s entry states: this file's carrier holds exactly
the version pair `captureClaudeVersion` returns, which makes it the one exec a
developer here reaches for, and `os.LookupEnv` is the two-value form of `os.Getenv`
over the same credential-bearing environment. Sixteen names.

`t.TempDir` stays absent for #1661's reason: this file writes nothing and needs no
directory. `fixtureFieldNonZero` is pure and banned nowhere; this file calls it.

`writePoolRevokeFixture` is deliberately NOT added, and a reader diffing this entry
against `initialize_control_compare_test.go`'s — which does ban its family's writer —
should know it was considered. AC 5 fixes this entry's composition at fourteen plus
two, and the hazard that ban closes elsewhere is absent here: that writer takes its
directory as a parameter and resolves no `packageDir`, so it reaches the committed
`testdata/` only through a relative path this file has no reason to name, and the
group above already bans every route by which such a path could be built. The file
not naming the writer is this ticket's AC 1 ("the only place #1675 assembles that
record" stops at the filled record) and is a review property here rather than an AST
one.

## Error handling

There are no error paths. The assembler and both helpers are total functions over
their inputs: no `error` return, no `t.Fatalf`, no panic reachable — the reflection
walk touches only exported fields of a concrete struct type, and the conversion's
`json.Unmarshal` failure IS the "not a JSON string" branch rather than an error.

Test-side failures follow the family: `t.Errorf` where the run can continue and
report every offending field, `t.Fatalf` only where a later assertion would be
meaningless. Every message names the consequence for #1675's committed artifact, not
just want-vs-got.

## Concurrency model

None. No goroutine, no lock, no shared state: the carrier is a value, the assembler
is pure, and the three tests take `t.Parallel()` with no fixture between them. The
carrier is *constructed from* readings the tap's mutex-guarded accessors already
copied out; nothing here touches the tap.

## Open questions

1. Should the assembler defensively clone the carrier's slices? Resolved in the
   design above: no. The carrier's slices are already snapshots, cloning would be
   noise, and the no-mutation row pins what actually matters.
2. Does the pid pair need distinct carrier names encoding "the revoke arm's two
   points"? Resolved: no. The requirement is documented on the field pair and enforced
   by AC 4's row asserting both are filled on a control; a longer name would state the
   same thing less precisely.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No MUST FIX, and the boundary is the subject rather than a side
  effect. Child stdout, child free-text output and the daemon's own log records are
  untrusted bytes that #1675 *commits to the repository*, and the assembler is the
  single explicit crossing — one pure function, one struct literal, one conversion.
  The conversion is safe against structure injection: `StdoutLines` is `[]string` and
  `encoding/json` escapes on write, so a child emitting `","x":"` cannot add a key to
  the artifact. Downstream knows what it holds because the record type says so in its
  own doc comment.
- [Tokens, secrets, credentials] No MUST FIX. Nothing here mints, stores or compares a
  secret; the standing hazard in this family is a credential reaching a committed
  file, and this ticket sits on three of its guards. First, no `env` field on the
  carrier or the record — the credential travels in the child's environment while the
  argv carries none, which is what makes recording argv safe. Second, free text routes
  to `ChildOutputCapture`, the field `writePoolRevokeFixture` caps through
  `capFixtureCapture`; routing it into the uncapped `StdoutLines` would commit an
  unbounded auth-failure message, and AC 3's test is that guard. Third, the file reads
  no environment — mechanically, via the `os.Getenv`/`os.Environ`/`os.LookupEnv` names
  in its `finOfflineExecBans` entry. `NotDeliveredLogs` was checked rather than
  assumed: `revokeLogHandler` retains `deliverSettingsInBand`'s record, which logs the
  constant field name and not the rejected value, so it carries no settings value to
  leak.
- [File operations] Not applicable by construction, and enforced rather than asserted:
  the assembler performs no I/O, builds no path and names no file. The
  `packageDir`/`setModeFixturePath`/`writeSetModeFixture`/`filepath.Glob` plus
  `os.ReadFile`/`os.WriteFile`/`os.Create`/`os.ReadDir` group in the ban entry closes
  the relative-path route to the committed `testdata/` — `go test` runs in the package
  source directory, so a relative write reaches it while naming no wrapper.
  `writePoolRevokeFixture` is the one remaining route and is discussed under Offline
  enforcement above; this file does not name it.
- [Subprocess / external command execution] Not applicable, and stated because the
  carrier holds an argv: `Argv` is an observation only. The assembler copies it and
  interprets nothing, no `exec` package is imported, and the four skip-keeper names in
  the ban entry (`resolveClaudeBin`, `WithWorktree`, `WithWorktreeAuthenticated`,
  `probeClaudeVersion`) plus `captureClaudeVersion` keep the file from acquiring a
  route to one.
- [Cryptographic primitives] Not applicable — no randomness and no comparison against
  a secret. The file's only comparisons are `reflect.DeepEqual` and substring checks
  over its own literals.
- [Network & I/O] Not applicable — no socket, no reader, no server. On input size:
  the assembler deliberately applies no cap, and that is inherited rather than new.
  `ChildOutputCapture` is bounded by the writer; `ControlResponses`, `StdoutLines` and
  `NotDeliveredLogs` are uncapped by #1662's explicit constraint, because capping the
  structured evidence would destroy the artifact, and a single adversarial stdout line
  is still bounded by `revokeTap`'s `maxPartial`.
- [Error messages, logs, telemetry] SHOULD FIX, and it is the finding this pass is
  most for. A `t.Errorf` that prints want-vs-got over a field holding child output
  moves up to 8 KiB out of a bounded field and into an unbounded run log — the exact
  thing the cap exists to prevent, and `writePoolRevokeFixture`'s own doc records the
  same rule for its `t.Fatalf`. Phase B follows two concrete rules, now written into
  the Testing strategy above: the full carrier's capture is SHORT so the mapping
  table's value-printing message is harmless, and AC 3's over-cap rows report lengths
  and at most a short prefix, never the capture.
- [Concurrency] SHOULD FIX, addressed in the Design above. Nothing in this ticket runs
  concurrently — the assembler is a pure function over a value and the three tests
  share no state — but the carrier is the seam where #1675 could get it wrong by
  filling it from a recorder's live fields while the child still writes. The carrier's
  doc comment now requires every slice on it to be a snapshot, which the tap's
  `snapshot*` accessors and the log recorder's `notDelivered` closure already return
  under their own mutexes.
- [Threat model alignment] The relevant threat is this family's own — a committed
  artifact that leaks a credential — and it is addressed under Tokens above.
  `docs/protocol-mobile.md` § Security model is OUT OF SCOPE: no relay, no transport
  and no production code is touched, and the live run that could expose a real
  credential is #1675's.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-03
