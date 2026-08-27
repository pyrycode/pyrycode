# #1764 — compare the `initialize` measurement arms against the no-request control at the same turn index

**Size:** S (PO's `size:s` confirmed — re-checked against this spec in § Scope check.)
**Shape:** test-only, offline, additive. One new file in `internal/e2e/realclaude/`, one `finOfflineExecBans` entry, three one-sentence prose corrections. No production file changes.

## Files to read first

Symbols, not lines — resolve each with `codegraph_search` / `codegraph_node`.

- `internal/e2e/realclaude/initialize_control_record_test.go` → `initControlFixtureRecord`, `initControlResultTrailer`. The decode target and the trailer fields. Read the record's doc comment for the "no parallel struct, no new fields" contract; this slice decodes through it and adds nothing to it.
- `internal/e2e/realclaude/initialize_control_window_test.go` → `initControlReadWindow`, `initControlWindowField`, and the file header. `initControlWindowField` is **reused verbatim** by this slice's reducer — it is the package's untrusted-bytes field decoder. The header is also the shape to copy for this file's own header (offline claim → ban-entry pointer → `go test -tags` recipe).
- `internal/e2e/realclaude/set_permission_mode_probe_test.go` → `probeOutcome`, `setModeProbeOutcome`, `setModeTurnWindows`, `setModeOutcomeAt`, `setModeFieldMatches`. The shape to borrow. **Borrow the shape, not the field set, and not `setModeOutcomeAt`'s return type** — § Design says why for both.
- `internal/e2e/realclaude/initialize_control_names_test.go` → `initControlArms`, `initControlArm`, `initControlArmFixtureName`, `initControlFixtureName`; the file header's "swept by three globs" paragraph; and the token table inside `TestInitControlFixtureName_AvoidsCommittedFamiliesAndStaysContained`. The arm table is the single source of truth for the arm set and for which arm is the control. The header paragraph is correction 1. The token table is why the new glob must **not** join that test's family-glob subtests (§ Design, "Do not add the glob to the collision tables").
- `internal/e2e/realclaude/initialize_control_probe_test.go` → `TestRealClaude_InitializeControl_SendPointArms`, `runInitControlChild`. The live writer that mints these fixtures; its write-set check carries correction 3 in a `t.Errorf` string.
- `internal/e2e/realclaude/permission_protocol_regression_test.go` → `TestRealClaude_PermissionProtocol_RegressionFixtures`, `assertRegressionFixture`. The package's glob-over-committed-fixtures idiom: relative glob including the `testdata/` prefix, loud fatal on zero matches, per-file subtests, `t.Errorf` rather than `t.Fatalf` inside per-finding checks so one run reports every problem at once.
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans`, `TestFinOfflineFilesReachNoExecHelper`. The table to extend, and the matcher's rule: a bare `*ast.Ident` **or** a dotted selector, comments not parsed.
- `internal/e2e/realclaude/testdata/initialize_control_v2.1.239_before_first_turn.json`, `…_after_completed_turn.json`, `…_control_no_request.json` — the three inputs. Read `arm`, `turn_boundaries`, `send_point_index`, `after_send_point_system_init_count`, `after_send_point_result_trailers` before designing against § Design's table. **Do not dump `stdout_events` into a run log** (§ Security review).
- `docs/knowledge/features/e2e-realclaude.md` § `initialize_control_window_test.go` (#1762) and § `initialize_control_names_test.go` — the family's accumulated lessons: the non-nil-empty contract, the `type`+`subtype` keying of the init count, and what a mutation table in this family actually proves. Read-only; the documentation phase owns this file.

## Context

#1763 committed three arm captures of the `initialize` control-request measurement: two arms that send the request at different send points, and one that sends nothing. #1762 built the send-point window reader. Nothing yet compares the arms.

The question is "does asking perturb the live session", and it cannot be answered from one arm. `system`/`init` is re-emitted per turn by every arm including the control, so a re-emitted init is the baseline shape and not a signal. Only a delta against the no-request control means anything, and only at the same turn index.

This slice settles offline over the committed fixtures: no tokens, no credentials, no claude binary, and it stays re-runnable long after the live run is gone.

**No ADR.** This adds a comparison over existing committed artifacts and one family glob; it decides nothing cross-cutting.

**Note for the documentation phase (not a deliverable here):** `docs/knowledge/features/e2e-realclaude.md` states, in its `initialize_control_names_test.go` entry, that `testdata/initialize_control_v*` is addressed by no glob. This slice makes the arm-carrying half of that family glob-addressed. The overview needs that folded in; the developer must not touch it.

### What the fixtures actually say

Measured on `7813fced`'s bytes. Turn windows are derived by slicing `stdout_events` at `turn_boundaries` — turn 0 is `lines[0:b0+1]`, turn 1 is `lines[b0+1:b1+1]`.

| arm | `send_point_index` | `turn_boundaries` | turn 0 window | turn 1 window | `control_response` in turn 0 / 1 | `system`/`init` per turn | `result` per turn |
|---|---|---|---|---|---|---|---|
| `before_first_turn` | 0 | `[10, 18]` | 0..10 | 11..18 | **1** / 0 | 1 / 1 | success, `is_error:false`, `num_turns:1` |
| `after_completed_turn` | 10 | `[9, 19]` | 0..9 | 10..19 | 0 / **1** | 1 / 1 | same |
| `control_no_request` | 13 | `[12, 25]` | 0..12 | 13..25 | 0 / 0 | 1 / 1 | same |

The `control_response` column is the only structural discriminator, and it is fixed by each arm's own send point, so it survives re-capture. Everything `probeOutcome` carries is constant across all three arms at both indices — the turns are tool-free — which is why porting its field set literally would give a comparison no mutation can redden. `thinking_tokens` counts differ (4/3, 4/3, 7/8) and are model variance: never a compared field.

### The liveness clause has a trap in it

`400db2d1`'s 401 capture — the state this ticket's precondition paragraph was written against — recorded, on **every** `result` line:

```
subtype: "success", is_error: true, num_turns: 1, total_cost_usd: 0,
duration_api_ms: 0, terminal_reason: "api_error"
```

`subtype` read **"success"** on a turn that never reached the model, and `num_turns` read 1. A liveness assertion keyed on `subtype` — the obvious choice, and the one `probeOutcome` would suggest — is **vacuous against the exact capture AC 5 exists to reject**. The healthy bytes read `is_error: false` and `terminal_reason: "completed"`. Assert those two; report `subtype`.

## Design

One new file: `internal/e2e/realclaude/initialize_control_compare_test.go`, behind the package's `e2e_realclaude` build tag.

### Discovery — glob, not exact name

```go
const initControlArmFixtureGlob = "testdata/initialize_control_v*_*.json"
```

The `testdata/` prefix is included, matching `fixtureGlob` and `dropcapFixtureGlob` (and unlike `setModeFamilyGlob`, which names base names) — `go test` runs in the package source directory, so a relative glob resolves under it with no `packageDir` call.

**Why the glob and not `initControlArmFixtureName` by exact name.** Exact-name addressing needs a version token, and this file may not learn one from a live claude (`captureClaudeVersion` is banned by its ban entry) — so it would have to hard-code `"2.1.239"`, and a re-capture at a new version would rename all three files and demand a code edit. Worse, it would make AC 4's clause about an undeclared `arm` structurally unreachable: a file addressed by exact name for a declared arm can never *have* an undeclared one. The glob discovers the version and gives the undeclared-arm check something to reject.

**The `_` is the exclusion, and it is structural.** `filepath.Match` needs a literal `_` after the version segment, and `initialize_control_v2.1.239.json` (#1688's one-arm capture, which carries no `arm`, no `send_point_index` and an unredacted `argv[0]`) has none. It is not read. Nothing mints unarmed names any more, so that one legacy file is the whole exclusion.

`initControlDiscoverArms(t *testing.T) (version string, byArm map[string]*initControlFixtureRecord)` performs, in order, accumulating problems into a `[]string` and ending with one `t.Fatalf` listing all of them (the `assertRegressionFixture` idiom — one run reports every problem):

1. `filepath.Glob` the constant. Zero matches ⇒ fatal, naming the glob. A deleted fixture set must be loud.
2. `os.ReadFile` + `json.Unmarshal` each match into an `initControlFixtureRecord`. A decode failure names the file.
3. `rec.Arm` must be non-empty and must be one of `initControlArms`' `id`s. Anything else is a problem naming the file and the arm — never a fourth row.
4. `filepath.Base(path)` must equal `initControlArmFixtureName(rec.ClaudeVersion, rec.Arm)`. This binds the name to the content in one comparison, so the version grouping below cannot be computed off a name whose record says something else. **String equality only — never `filepath.Join` with a value read out of a fixture** (§ Security review).
5. Group by `rec.ClaudeVersion`. **Exactly one group, or fail**, naming every version present.
6. Within that group, each of `initControlArms`' ids appears exactly once.

**Which version to compare, when several are present: none — the run fails.** Stated at the site. The alternative, "compare the newest", was declined: these tokens have no total order without a semver parser, and a wrong order silently compares the stale set — the one outcome worse than a red. Both captures to date landed on `2.1.239` and the filename is derived from the version, so a re-capture at the same version overwrites in place and never reaches this branch; a re-capture at a new version leaves a stale set whose deletion is the correct fix, and the failure message says so.

**The control arm comes from the table, never from a literal.** It is the unique row of `initControlArms` with `sendsRequest == false`. Exactly one such row, or fail. A second spelling of `"control_no_request"` in this file is the duplication `initControlArms`' own doc forbids.

### The per-turn read

```go
type initControlTurnRead struct {
	ControlResponses     int    // `control_response` lines inside THIS turn's window — the discriminator
	SystemInitCount      int    // `system` lines whose subtype is `init`, keyed on BOTH
	ResultObserved       bool   // false ⇒ the window never closed on a `result`
	ResultSubtype        string
	ResultIsError        bool
	ResultTerminalReason string
	ResultNumTurns       int
}
```

Seven fields, **every one of them compared**. That is the whole discipline `probeOutcome`'s doc states, applied to a different field set for the reason § Context gives: `probeOutcome`'s fields are constant across these arms, so porting them literally yields a comparison no mutant can redden. `ControlResponses` is what makes a wrong-index, wrong-arm or wrong-window slice observable rather than silently agreeing.

**Cost and `thinking_tokens` are deliberately not fields here.** Cost differs at the fourth decimal between arms for reasons that are not perturbation; a compared cost field would report "differs" on every row and turn the verdict into noise. Cost is reported by the side-by-side window row below, where it is labelled rather than subtracted. Do not "complete" this struct with either.

Functions (signature + one-line behaviour; the developer writes the bodies):

- `initControlReadTurn(window []json.RawMessage, closed bool) initControlTurnRead` — reduces one turn window. Decodes each line into `map[string]json.RawMessage` and reads every field through `initControlWindowField`, exactly as `initControlReadWindow` does: a line that is not a JSON object is skipped and the read continues; a field that will not decode leaves its destination at the zero value and the line still lands. Total over hostile bytes; returns no error and must never grow one.
- `initControlTurnReads(lines []json.RawMessage, boundaries []int) []initControlTurnRead` — slices at the recorded boundaries and reduces each window, mirroring `setModeTurnWindows` including its `if b < start || b >= len(lines) { continue }` guard and its trailing-unclosed-window rule (`ResultObserved` false). **The guard is load-bearing, not stylistic:** `turn_boundaries` is a number read off disk, and an out-of-range or non-monotonic value without it panics the package's whole test binary.
- `initControlTurnReadAt(reads []initControlTurnRead, i int) (initControlTurnRead, bool)` — **returns `ok`; it does not return a zero value.** This is the one place this slice deliberately diverges from `setModeOutcomeAt`, whose out-of-range zero value compares equal to another zero value, so two arms that both ran short report as "the arms do not differ". Hazard (a) of the ticket's three; the `bool` is what closes it, and the assertion in § The instrument is what consumes it.

### The comparison and its report

`initControlTurnRows(got, control initControlTurnRead) (rows []string, differing []string)` — one row per struct field, each reading `name=<got> vs control=<control> → agrees` or `→ differs`, plus the names of the differing fields.

"The arms do not differ" is reported as an outcome, never as a missing result: the per-(arm, index) verdict line reads either `agrees with control_no_request on every field` or `differs on [control_responses]`. Do **not** port `setModeFieldMatches`' three-way "both controls agree — not a discriminator" lean; that construct exists because that probe has two controls and this one has a single control, against which every field either agrees or differs.

**Row totality is mechanical, not remembered.** The test asserts `len(rows) == reflect.TypeOf(initControlTurnRead{}).NumField()`, the idiom `initControlTrailerFields`' own check already uses in this family. A field added to the struct and forgotten in the row builder would otherwise go silently uncompared.

### The side-by-side window row (AC 3)

`initControlSendPointTurn(boundaries []int, sendPoint int) int` — the turn index the send point falls in: the count of boundaries strictly less than `sendPoint`. Pure counting; it slices nothing, so no bounds hazard. Verified against the fixtures: `before_first_turn` → 0, both others → 1. A send point past the last boundary yields an index past the last turn, and the label below stays total over it.

One row per arm, in `initControlArms`' declared order, reporting `send_point_index`, the turn that index falls in, `after_send_point_system_init_count`, `len(after_send_point_result_trailers)`, and each trailer's `num_turns` and cost — read off the record, not recomputed. When an arm's send-point turn differs from the control's, the row says so in words: it opens in a different turn from the control's, spans a different stretch of the session, and its numbers are therefore not a delta. `before_first_turn` opens at index 0, so its window is the whole session (`init` 2, two trailers) where both other arms open after a completed first turn (`init` 1, one trailer). That is structural and survives any re-capture; it is exactly why AC 1's compare-at-the-same-turn-index is the apples-to-apples read and this row is labelled rather than subtracted.

### The instrument — what is asserted

`TestInitControlArms_CompareMeasurementArmsAgainstTheControlAtTheSameTurnIndex(t *testing.T)`, `t.Parallel()`.

The verdict is **reported**. What is **asserted**:

1. Everything `initControlDiscoverArms` checks: all three declared arms present exactly once for the one compared version, each decoding through `initControlFixtureRecord`, each name bound to its record.
2. `N`, the compared index range, is `max(len(reads))` across the three arms, and `N >= 1`. Zero means no arm produced a single turn read, and it is named rather than reported as agreement.
3. Every arm has a **real** read (`ok == true` from `initControlTurnReadAt`) at every index `0..N-1`. An arm that ran short is named; it does not zero-fill into agreement. This closes hazards (a) and (b).
4. Every compared turn of every arm is **live**: `ResultObserved && !ResultIsError && ResultTerminalReason == "completed"`. Keyed on `is_error` and `terminal_reason`, **never on `subtype`** — see § Context. This closes hazard (c): a set of arms whose turns never reached the model agrees with itself by construction.

The test reaches no child, no environment, no claude binary and no credentials, and reports PASS — not SKIP — on a machine that has neither. A locally regenerated capture that went the way `400db2d1` did makes this test red, and that is the liveness clause earning its keep: do not make discovery tolerant of it and do not prefer the committed bytes over what is on disk.

### Do not add the glob to the collision tables

`initControlArmFixtureGlob` must **not** be appended to the family-glob subtests inside `TestInitControlFixtureName_AvoidsCommittedFamiliesAndStaysContained` or `TestInitControlArmFixtureName_AvoidsCommittedNamesStaysDistinctAndContained`. Those assert that no minted name matches a *foreign* family's glob. Every arm name matches this glob by design, so the arm namer's subtest would assert something false — and the one-arm namer's subtest carries the token `2_1_220`, which mints `initialize_control_v2_1_220.json` and **does** match `v*_*`. Both would go red against correct code. The three corrections below are prose only.

### The offline ban entry

`finOfflineExecBans` is keyed by filename, so a new file with no entry produces no subtest and no failure. Add one for `initialize_control_compare_test.go`, derived from `initialize_control_window_test.go`'s seventeen names by dropping three and adding one — fifteen names:

- **Dropped:** `packageDir`, `filepath.Glob`, `os.ReadFile`. This is the first offline file in this package with a legitimate reason to read a committed fixture, so the group that keeps its siblings away from the real `testdata/` collapses in the read direction. The entry's comment must say that, because every sibling entry's comment says the opposite and a reader will otherwise take this one for an oversight.
- **Kept:** `os.ReadDir` — the listing route this file does *not* take. `filepath.Glob` and `os.ReadDir` are two ways to the same listing, and this package's precedent is that a ban name wants a hazard behind it rather than that an unused one has to go.
- **Kept:** `os.WriteFile`, `os.Create`, `writeFixture`, `writeSetModeFixture`, `setModeFixturePath`. The relative-path hazard the siblings close is in the **write** direction, and this file reads and must never write.
- **Added:** `writeInitControlFixture` — this family's own writer, and the one a developer in this file is most likely to reach for.
- **Unchanged from the sibling:** `resolveClaudeBin`, `WithWorktreeAuthenticated`, `WithWorktree`, `probeClaudeVersion`, `captureClaudeVersion` keep a SKIP out; `os.Getenv`, `os.Environ`, `os.LookupEnv` are the credential guard. `captureClaudeVersion` matters here specifically: it is the shortcut that would replace § Discovery's version grouping with a live `claude --version`, destroying the offline property.

### The three prose corrections

Adding a glob for this family makes three shipped claims false. None of them is itself asserted, so nothing reddens — which is why they are listed rather than left to a test.

1. `initialize_control_names_test.go`, the file header's "testdata/ is swept by three globs owned by three different probes" paragraph. Note that #1764 adds a fourth, that it is this family's **own**, and that the collision argument the paragraph makes is unchanged — the three foreign heads are still the ones a minted name must avoid.
2. `initialize_control_names_test.go`, the comment inside the "no minted name collides with the committed one-arm capture" subtest of `TestInitControlArmFixtureName_AvoidsCommittedNamesStaysDistinctAndContained`, which reads "the initialize_control_v family is matched by no glob in this package — it is addressed only by exact name". Narrow it to the foreign families this subtest sweeps, and name `initControlArmFixtureGlob` as the one that matches these names on purpose. The subtest's own reasoning — that a *pattern* check cannot stand in for the string equality it performs — is unaffected and must survive the edit.
3. `initialize_control_probe_test.go`, the `t.Errorf` string in the write-set check of `TestRealClaude_InitializeControl_SendPointArms` reading "no glob in this package matches the initialize_control_v family". After this slice, a path the namer did not mint is evidence #1764's discovery either misses it entirely or reads it under the wrong version. Same length, corrected claim.

The sweep that found all three, for re-running after the edits (ripgrep via the Grep tool — `git grep -E` silently drops `\b`):

```
rg -n 'no glob|matched by no|three globs|swept by three|three different probes' internal/e2e/realclaude/
```

`initialize_control_names_test.go`'s "The three patterns are NOT matched against the same string" paragraph and `inband_bypass_revoke_names_test.go`'s references are scoped to their own files' anchoring tables and stay true; leave both alone.

## Concurrency model

None to speak of, and that is a design property rather than an omission. No goroutines, no channels, no subprocess, no shared mutable state. `t.Parallel()` on the test; every helper is a pure function over locally-decoded values. `initControlArms` is **ranged only** — never appended to, never reassigned — per its own read-only contract, which other files in this package range from `t.Parallel()` tests.

## Error handling

| Failure mode | Handling |
|---|---|
| Glob matches nothing | `t.Fatalf` naming the glob. A deleted fixture set must be loud. |
| A match will not read or decode | Problem entry naming the file; the run fails rather than comparing two arms. |
| `arm` empty or undeclared | Problem entry naming the file and the value. Never a fourth row. |
| Base name ≠ `initControlArmFixtureName(rec.ClaudeVersion, rec.Arm)` | Problem entry naming both. |
| More than one `claude_version` group | Fatal naming every version, and saying the stale set should be deleted or this test taught which to compare. |
| A declared arm missing, or present twice | Problem entry naming the arm. |
| `initControlArms` has no unique `sendsRequest == false` row | Fatal. The comparison has no control. |
| A line in `stdout_events` that is not a JSON object | Skipped by the reducer; the lines after it still land. A bad line costs its own entry and nothing more. |
| A field with a hostile shape (`"num_turns": "1"`) | `initControlWindowField` leaves the destination at its zero value; the line still lands. No error grows out of the reducer. |
| A `turn_boundaries` entry out of range or non-monotonic | Skipped by `initControlTurnReads`' guard. Without it, a slice-out-of-range panic takes the package's test binary down. |
| An arm with no read at a compared index | Named in the failure — never zero-filled into agreement. |
| A compared turn whose `result` is an API error | Named in the failure. `is_error` and `terminal_reason`, never `subtype`. |

Every problem that can coexist with others is accumulated and reported together, so one run names every broken fixture rather than the first.

## Testing strategy

The test **is** the deliverable; there is no second test of it. Its own instrument assertions (§ The instrument) are what keep it from being a decoration, and `ControlResponses` is what keeps them from being satisfiable by a wrong slice.

No synthetic row table for the reducer. #1762 needed one because its live arm's window was empty and discriminated nothing; here all three committed arms carry a discriminating `ControlResponses` value at a known index, so the fixtures themselves are the table.

The pre-merge run — this package is behind `e2e_realclaude`, `make check` never compiles it, and the suite exits 0 both on a build failure and on a full credentials skip:

```
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestInitControlArms_CompareMeasurementArmsAgainstTheControl|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/
```

Both must report PASS — not SKIP, not "no tests to run". **Read the count of tests that executed, never the exit code.** After the three prose corrections, also compile-check the package (`go vet -tags e2e_realclaude ./internal/e2e/realclaude/`) and run the two touched name tests:

```
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestInitControlFixtureName_|TestInitControlArmFixtureName_' \
  ./internal/e2e/realclaude/
```

Both must stay green — correction 2 is inside one of them, and § "Do not add the glob to the collision tables" is what keeps them that way.

Worth doing once, since it is the claim the whole file rests on: check that discovery excludes `initialize_control_v2.1.239.json` and admits exactly the three arm files, by reading the failure the test prints when a fourth row appears — temporarily point the glob at `testdata/initialize_control_v*.json` in a scratch run and confirm the undeclared-arm branch fires on the legacy file rather than producing a row of zeros. Revert before committing.

## Scope check

Re-applied to this written spec:

| Boundary | Limit | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **0** — every file is `*_test.go` |
| Total written work | ≤ 400 lines | ~330–380 (new file ~290–330 incl. doc comments, ban entry ~30, corrections ~8) |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — additive; three prose edits touch no call site |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches in a state machine | ≤ 10 | n/a — no state machine |

Nearest analogue: #1762's `564a4a3b`, re-derived — 322 insertions / 5 deletions across four files, shipped `size:s`, its new file 261 lines of which ~150 were a synthetic row table this slice does not need. **Budget the diff at that commit's size.** If the new file runs past ~330 lines, the doc comments are the place to compress; the checks in § The instrument are not.

## Open questions

- **Report cost per compared turn as well as in the window row?** The spec says no — a compared cost field makes every verdict read "differs" for reasons that are not perturbation. If a reader later wants per-turn cost visible, it belongs in the reported line as a labelled annotation beside the read, never as a field of `initControlTurnRead`. Decide against adding it unless a concrete question needs it.
- **`thinking_tokens` per turn (4/3, 4/3, 7/8).** Reportable, never comparable — it flips run to run. Left out entirely; add it only as a labelled annotation if a reader asks.
- **The multi-version branch is unreachable today.** Both captures landed on `2.1.239` and the writer derives the filename from the version, so it can only fire after a version change. It is asserted rather than assumed because the alternative is a silent comparison of a stale set against a fresh one.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** MUST FIX, addressed in the spec. `stdout_events` and `turn_boundaries` are child output committed to disk — untrusted bytes read back into a trusted reduction. The boundary is explicit and single: every field crosses through `initControlWindowField`, absorbing failure at the **field**, never at the line or the window, and `initControlTurnReads` carries `setModeTurnWindows`' range guard on every boundary before slicing. Without that guard an out-of-range `turn_boundaries` entry panics the package's test binary; with it, a hostile line costs its own entry and nothing after it. A second finding in the same category: deciding "this capture is trustworthy" from `result.subtype` is exploitable by the *observed* failure — `400db2d1` recorded `subtype: "success"` on turns that returned 401 — so the liveness assertion is keyed on `is_error` and `terminal_reason`, and § Context states the measured bytes rather than asserting the field is safe.
- **[Tokens, secrets, credentials]** No findings. The file reaches no environment: `os.Getenv`, `os.Environ` and `os.LookupEnv` are in its `finOfflineExecBans` entry, and `TestFinOfflineFilesReachNoExecHelper` enforces it over the AST rather than over a comment. The process environment here carries `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY`; nothing in this slice can read either. No token is generated, stored, compared or logged.
- **[File operations]** No findings. Read-only. The glob pattern is a compile-time constant with a literal head, `filepath.Glob` returns paths inside `testdata/`, and **no value read out of a fixture is ever joined into a path** — the name↔record binding in `initControlDiscoverArms` is a string comparison against `initControlArmFixtureName`'s output, deliberately not a `filepath.Join`. That closes the traversal shape a fixture-supplied `arm` could otherwise reach (`initControlArmFixtureName` slugs both inputs, but the design does not rely on that here). No writes at all: `os.WriteFile`, `os.Create`, `writeFixture`, `writeSetModeFixture`, `setModeFixturePath` and `writeInitControlFixture` are banned, so no mode, no TOCTOU and no atomic-write question arises. No `os.Stat`-then-open: the file reads whatever the glob returned and reports a read failure.
- **[Subprocess / external command execution]** No findings, closed mechanically. `resolveClaudeBin`, `probeClaudeVersion`, `captureClaudeVersion`, `WithWorktree` and `WithWorktreeAuthenticated` are all banned for this file; it spawns nothing and inherits nothing.
- **[Cryptographic primitives]** Not applicable — no randomness, no hashing, no comparison against a secret. The design decision that makes it so: discovery is deterministic over a constant glob, with no sampling, no shuffling and no time-dependence.
- **[Network & I/O]** Not applicable to the network half. On the I/O half: input size is bounded by what is committed under `testdata/`, the reducer is linear in `len(stdout_events)` and allocates one read per turn window, and the boundary guard makes a hostile `turn_boundaries` of any length O(1) per skipped entry. `initControlFixtureRecord` is uncapped by design (its own doc states why capping structured evidence destroys the artifact); this slice adds no new uncapped surface.
- **[Error messages, logs, telemetry]** MUST FIX, addressed in the spec. The fixtures carry redacted-but-real child output, and a run log is a durable artifact. The design prints **only reduced reads** — counts, booleans, subtypes, terminal reasons, arm ids, relative file names and the recorded window numbers. It never prints an entry of `stdout_events`, `ControlRequestSent`, `ControlResponses`, `StderrCapture`, `Argv` or `Prompts`. AC 4's exclusion is doing double duty here: #1688's legacy one-arm capture predates #1733's redaction pass and carries the operator's home directory verbatim in `argv[0]`, and the glob's `_` keeps it out of the read set entirely. Paths reported in failures are the relative glob results, which carry no home directory.
- **[Concurrency]** No findings. No goroutines, no channels, no locks, so no ordering, no TOCTOU on shared state and no leak. `t.Parallel()` is safe because every helper is pure over locally-decoded values, and `initControlArms` is ranged read-only per its own contract — a mutation there would race in a way `-race` catches only when runs happen to overlap, which is why this file must not append to it.
- **[Threat model alignment]** Out of scope, named rather than skipped: this slice touches no relay, no wire format and no daemon surface, so `docs/protocol-mobile.md` § Security model has nothing applicable. The one live-side threat adjacent to it — a credentialled `make e2e-realclaude` rewriting these fixtures in place, since nothing in `.gitignore` covers this family — is deliberately **not** mitigated here: the liveness assertion going red on a bad local capture is the designed behaviour, and making discovery prefer the committed bytes would destroy it. Anyone who wants an ignore rule for this family should file it separately.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-25
