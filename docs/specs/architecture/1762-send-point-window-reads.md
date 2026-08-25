# #1762 — fill the `initialize` capture's send-point window reads from the recorded lines

**Ticket:** [#1762](https://github.com/pyrycode/pyrycode/issues/1762) · size `s` · `security-sensitive`
**Split from:** #1715 · sibling: #1763 (commits one arm's artifact)

## Files to read first

Symbol names, not line numbers — resolve each with `codegraph_search` / `codegraph_node`.

- `internal/e2e/realclaude/initialize_control_record_test.go` → `initControlResultTrailer` — the exact struct the reader emits. Extract: the two field names mirror claude's own spellings (`num_turns`, `total_cost_usd`), and `TotalCostUSDPresent` is populated **from raw bytes and never derived from `TotalCostUSD`**. Its doc names this ticket as where that mutant first becomes reachable.
- same file → `initControlFixtureRecord` — the `SendPointIndex` / `AfterSendPointSystemInitCount` / `AfterSendPointResultTrailers` paragraph group. Extract the four standing constraints: one anchor, window is `stdout_events[anchor:]`, both edges legitimate with no presence flag, and no count gets a field of its own. **This is also the paragraph this slice retargets** (see § Design, step 4).
- same file → `initControlTrailerFields` — the hand-written per-field listing for the nested trailer type. Extract: it exists because `reflect.TypeOf(initControlFixtureRecord{}).NumField()` cannot see a nested type. This slice adds no field to either type, so this listing is **unchanged**; read it to confirm that, not to edit it.
- `internal/e2e/realclaude/initialize_control_probe_test.go` → `initControlSummarize` and `initControlSummary` — the shape precedent this slice copies: a package-private struct-returning pure read over recorded raw bytes, deciding presence on the bytes rather than on a decoded value, skipping a line it cannot decode rather than aborting.
- same file → `TestInitControlSummarize_ReadsAllThreePlacements` — the offline table-test idiom: `t.Parallel` on parent and subtest, `reflect.DeepEqual` against a `want` struct, one `t.Errorf` that says what the field feeds downstream.
- same file → `runInitControlChild` — the fill site. Extract three positions: the `writeLine("control request", …)` call, the post-join `lines := rec.snapshotLines()`, and the `initControlFixtureRecord` literal where the three fields get assigned.
- same file → `TestInitControlProbedArm_IsExactlyOneDeclaredNonEmptyArm` and `TestInitControlScanApplied_RecordsAnArmedNothingClassForAnAbsentPath` — the two precedents for an offline test living in the exec-ing file, and (in the second one's doc) the stated reason a test belongs in a **banned** file when it can.
- `internal/e2e/realclaude/set_permission_mode_probe_test.go` → `setModeRecorder`, its `add` method, and `snapshotLines` — the `"type":"system"` + `"subtype":"init"` switch that **is** the count's definition, the fact that a non-JSON line is retained as a JSON *string*, and the only accessor the anchor can be read through.
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans` and `TestFinOfflineFilesReachNoExecHelper` — the map is keyed by **file name**; the entry to copy whole is `initialize_control_record_test.go`'s seventeen names; the matcher handles a bare `*ast.Ident` and a dotted selector as separate rules and is deliberately built without `parser.ParseComments`.
- `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` — **read by hand only, never from the test.** Twelve lines; `system`/`init` at index 0; five `system`/`thinking_tokens`; the single `result` at index 9 carrying `num_turns: 1` and `total_cost_usd: 0.0219814`; `control_response` at 10; `system`/`background_tasks_changed` at 11. It predates #1723 and carries none of the three keys.
- `docs/knowledge/features/e2e-realclaude.md` § `initialize_control_record_test.go` (#1701, extended #1722, #1723, #1731) — the field group's history, including the "until #1715 fills them … an unpopulated field" claim this slice makes stale. Read-only; the documentation phase owns this file.
- `CODING-STYLE.md` — the documented ignored-error idiom, `_ = ptmx.Close() // best-effort cleanup, child already exited`. The per-field decode below is the same shape and needs the same comment.

## Context

#1723 added three fields to `initControlFixtureRecord` and deliberately left them unpopulated. A live re-run today writes `send_point_index: 0` and leaves the other two at their zero values, and the record's own doc says that reading is an **unpopulated field**, not a `before_first_turn` capture. This slice fills all three for the one send point the driver has: the control line written after a completed turn.

The read is a pure function over the lines the recorder already holds. It adds no arm, changes no signature, drives no extra child, and touches no production file.

**Today's arm produces an empty window, and that is the design constraint that shapes everything below.** Read off the committed capture: twelve lines, the single `result` at index 9, so the anchor is 10 and the window is `control_response` plus one `system`/`background_tasks_changed` — no `init` line, no trailer. A live re-run therefore writes `0` and an empty list and cannot distinguish a correct empty window from a reader that measured nothing. **The offline test carries the entire proof; the live run carries none of it.** That asymmetry is why the test's coverage list is prescriptive here rather than left to taste, and why the non-nil-empty distinction gets its own callout.

No ADR is warranted — this implements a contract three tickets already fixed in prose.

## Design

Four edits across four `_test.go` files. No production source file is created or modified.

### 1. New file: `internal/e2e/realclaude/initialize_control_window_test.go`

**Placement decision.** The ticket leaves the choice open between a new file and placing the reader beside `initControlSummarize` in `initialize_control_probe_test.go`. **Take the new file.** `finOfflineExecBans` is keyed by file name; the probe file execs and can never carry an entry, so a reader and test placed there rest on review alone, while a new file makes AC 1's "reaches no child, no filesystem and no environment" and AC 3's "PASS, not SKIP" mechanically enforced by an AST check that cannot answer itself out of its own header. That is deterministic fabric behind a stochastic claim, which is the reason to pay for the entry. The probe file is also already 963 lines.

**Header budget.** Aim at ~40–50 lines, not the record file's ~70. Everything specific to this slice belongs in the reader's own doc; the header states what the file is, that it performs no I/O in either direction, that its entry in `finOfflineExecBans` is the enforcement, and the `-run` invocation.

**The window type.** A package-private struct mirroring `initControlSummary`:

```go
type initControlWindow struct {
	systemInitCount int
	resultTrailers  []initControlResultTrailer
}
```

Unexported fields are correct and match the precedent — `reflect.DeepEqual` sees unexported fields within the package, which is what makes the table test's `want` comparison work.

**The reader.**

```go
// initControlReadWindow reads lines[anchor:] and returns the window's
// `system`/`init` count and one trailer per `result` line, in arrival order.
func initControlReadWindow(lines []json.RawMessage, anchor int) initControlWindow
```

Behaviour, per line in `lines[anchor:]`:

1. Decode into `map[string]json.RawMessage`. **A decode failure means the line is not a JSON object — skip it and continue.** That is the shape `setModeRecorder.add` stores for non-JSON child output (a JSON *string*), and skipping it must not abort the read. A JSON `null` line decodes into a nil map without error and then falls through steps 2–4 producing nothing; no extra guard is needed for it, and none should be added.
2. Read `type` per-field. `"system"` with `subtype` read per-field as `"init"` increments the count. The subtype half is load-bearing, not decorative: the committed capture carries six `system` lines that are not `init`, so a count keyed on `type` alone reads 7 over that run where 1 is right.
3. `"result"` appends exactly one trailer, **unconditionally** — whatever the state of its other fields. `len(resultTrailers)` **is** the window's `result` count and nothing else records it, so a dropped line silently changes a committed number and surfaces nowhere.
4. Trailer fields: `NumTurns` from `num_turns`, `TotalCostUSD` from `total_cost_usd`, `TotalCostUSDPresent` from whether the **key exists in the decoded map** — never from whether the value decoded, and never from `TotalCostUSD != 0`.

**Every field read is per-field, and one helper makes that structural rather than repeated four times:**

```go
// initControlWindowField decodes obj[key] into dst, reporting whether the key
// was PRESENT — independent of whether the decode succeeded.
func initControlWindowField(obj map[string]json.RawMessage, key string, dst any) bool
```

It looks the key up, returns false when absent, otherwise runs `_ = json.Unmarshal(raw, dst)` with the `CODING-STYLE.md` comment saying a per-field failure deliberately leaves `dst` at its zero value, and returns true. This is the security obligation #1723's review handed forward, discharged in one place: `stdout_events` is child output, so a `result` line whose `total_cost_usd` is a string must not abort the read, and presence must be read from the bytes.

**Decided here rather than left open: a non-numeric cost reads present with a zero value.** `"total_cost_usd": "0.02"` yields `{TotalCostUSDPresent: true, TotalCostUSD: 0}`. The flag's stated purpose is to separate "the trailer carried no cost key" from "the trailer carried one" — a string cost carried one. Reading it as absent would collapse exactly the two facts the flag exists to keep apart, and would additionally make an unreadable cost indistinguishable from a missing one.

**The empty window returns a NON-NIL empty slice.** Initialise `resultTrailers := []initControlResultTrailer{}` before the loop; do **not** declare it with `var`. A committed `[]` says the window was measured and `null` says the field was never filled, and that whole distinction lives in the marshalled bytes. This is one line of implementation and it is the one AC 1 clause a `var` declaration silently inverts.

**No bounds guard on `anchor`, deliberately.** The contract is `0 <= anchor <= len(lines)`, both edges legitimate. It holds structurally at the one call site: `r.lines` is append-only, so `len(rec.snapshotLines())` read before the write cannot exceed the length of the post-join snapshot. A clamp would add a return site no test row reaches — an unreachable arm that makes a totality claim false — so state the precondition in the doc and add no branch. AC 3's two anchor rows cover both legitimate edges.

### 2. `internal/e2e/realclaude/offline_exec_ban_test.go` — one entry

Add `"initialize_control_window_test.go"` carrying `initialize_control_record_test.go`'s **seventeen names copied whole**: `resolveClaudeBin`, `WithWorktreeAuthenticated`, `WithWorktree`, `probeClaudeVersion`, `captureClaudeVersion`, `os.Getenv`, `os.Environ`, `os.LookupEnv`, `packageDir`, `setModeFixturePath`, `writeSetModeFixture`, `writeFixture`, `filepath.Glob`, `os.ReadFile`, `os.WriteFile`, `os.Create`, `os.ReadDir`.

The doc comment above the entry says why that set and not #1702's narrower twelve: like #1696's and #1701's files, this one performs **no I/O in either direction** — it builds line literals and asserts on a returned struct. The first five keep a SKIP out (`resolveClaudeBin` and `WithWorktreeAuthenticated` skip *inside* the test body, after `=== RUN` prints, and a skip exits 0, which reads as a pass under `make e2e-realclaude`). The three environment readers are the credential guard. The `packageDir` group is the trio plus `writeFixture` because the check is an AST identifier match, so a file calling a wrapper reaches `packageDir` transitively while never naming it. The `os` read/write group plus `filepath.Glob` close the relative-path hazard the ticket names explicitly: `go test` runs in the package source directory, so a relative `os.ReadFile("testdata/initialize_control_v2.1.239.json")` reaches the committed capture without naming any wrapper — and that read is precisely the shortcut a developer building this table is tempted by.

`t.TempDir` is absent for #1661's reason rather than #1651's: this file writes nothing and needs no directory. `os.Open` is deliberately declined — no sibling entry carries it, the family's five `os` names are the established set, and a ban name needs a hazard behind it (the #1732 entry declines `os.TempDir` on exactly that ground).

### 3. `internal/e2e/realclaude/initialize_control_probe_test.go` — the fill site

Three small edits inside `runInitControlChild`, no signature change:

- **The anchor**, read immediately before `writeLine("control request", controlLine)` and after the turn-completion wait, into a local `sendPointIndex := len(rec.snapshotLines())`. `setModeRecorder` has no cheaper accessor, and adding one would change a type the `set_permission_mode` family shares.
- **The window read**, after the existing post-join `lines := rec.snapshotLines()` and its zero-lines fatal: `window := initControlReadWindow(lines, sendPointIndex)`. One anchor, one slice expression — so the two reads cannot disagree about which window they measured, and no line is counted both before and inside it.
- **Three assignments** in the record literal, in the field group's declaration order: `SendPointIndex: sendPointIndex`, `AfterSendPointSystemInitCount: window.systemInitCount`, `AfterSendPointResultTrailers: window.resultTrailers`.

**Do not compute the count as a difference of two `snapshotInitModes()` calls.** The ticket calls this out and it is a race: the two snapshots take the lock separately, so a line landing between them is counted on the wrong side of the anchor and the window under-reports by one. Computing from the window is what makes AC 4's last clause hold.

**The residual, stated because a reviewer will look for it:** a line claude writes between the anchor read and the write lands *inside* the window although it preceded the request. That is the safe direction and it is the one AC 4 asks for — nothing written *in response to* the request can fall before the anchor. Reading the anchor after the write would invert that.

A comment at the anchor site should say why it sits there rather than beside the record literal.

### 4. `internal/e2e/realclaude/initialize_control_record_test.go` — one stale paragraph

Exactly one paragraph of `initControlFixtureRecord`'s doc goes stale the moment this slice lands: the one beginning *"All three are POPULATED, never computed here"*, whose next sentence claims *"Until #1715 fills them a live re-run leaves all three at their zero values."* That is false after this ticket.

Retarget that paragraph and nothing else in the file:

- Keep "All three are POPULATED, never computed here" — still true; the reader lives in another file and the record computes nothing.
- Replace the #1715 clause with: #1762 fills all three at the fill site from `initControlReadWindow`; the committed `initialize_control_v2.1.239.json` predates the fields entirely and carries **none of the three keys**, which is why an artifact from before this slice is still not readable as a `before_first_turn` capture.
- Keep the "no presence flag" resolution and its reasoning intact.

This is a bounded comment edit. Do not touch `initControlResultTrailer`'s doc, `initControlTrailerFields`, or any test in that file — no field is added to either type, so the record's `NumField` assertion and the trailer's hand-written listing are both unaffected.

## Concurrency model

No goroutine is added, started or joined. The reader is a pure function over a slice the caller already owns.

The only concurrency in play is the existing one: `setModeRecorder`'s mutex guards appends from the single stdout reader goroutine against reads from the test goroutine. This slice takes that lock exactly once more, through `snapshotLines` at the anchor, and holds nothing across the write. The window read runs after `<-readerDone`, over a snapshot copy, so it races with nothing.

The rejected alternative is where the concurrency bug would have been: two `snapshotInitModes()` calls take the lock separately, and a line landing between them is attributed to the wrong side of the anchor.

## Error handling

There is no error return and there must not be one. The reader's contract is total over its input:

| Input shape | Behaviour |
|---|---|
| Line is not a JSON object (a JSON string, array, number) | Skip; no entry; read continues |
| Line is JSON `null` | Decodes to a nil map; no `type`; no entry |
| Object with no `type`, or a non-string `type` | Skip |
| `system` with a non-`init` subtype, or an unreadable subtype | Not counted |
| `result` with an unreadable `num_turns` | One entry, `NumTurns` zero |
| `result` with a non-numeric `total_cost_usd` | One entry, present **true**, value zero |
| `result` with no `total_cost_usd` key | One entry, present **false**, value zero |
| Empty window (`anchor == len(lines)`) | Zero count, non-nil empty slice |

A per-field decode failure is absorbed at the field, never at the line, and never at the window. That is the security obligation from #1723's review: `stdout_events` is child output and hostile-shaped input must not cost the capture.

## Testing strategy

One offline table test in the new file, `t.Parallel` on parent and subtest, `reflect.DeepEqual(got, tc.want)` with one `t.Errorf` naming what these fields feed. Rows are driven from **raw line literals** — never by marshalling `initControlResultTrailer` or `initControlFixtureRecord`, which structurally cannot produce a line missing a key, which is the whole subject of two rows.

Required rows, each with the mutant it exists for:

- **cost present and zero** — `{"type":"result","num_turns":1,"total_cost_usd":0}` → one trailer, present **true**, value 0. With the row below, the sole red for a reader deriving the flag from `TotalCostUSD != 0`.
- **no cost key** — `{"type":"result","num_turns":2}` → one trailer, present **false**. The other half of that pair.
- **cost is not a number** — `{"type":"result","num_turns":3,"total_cost_usd":"0.02"}` → one trailer, present **true**, value 0. Sole red for a reader that aborts the line (or the window) on a per-field decode error: such a reader drops the entry and the window's `result` count reads 0 where 1 is right.
- **a `system` line with another subtype** — a `thinking_tokens` line followed by an `init` line → count 1, not 2. Sole red for a count keyed on `type` alone.
- **a non-object line** — the recorder's non-JSON shape (a bare JSON string) **followed by a real `result` line** → the trailer still arrives. The following line is what makes the row discriminate: without it, "skipped" and "aborted the whole window" produce the same answer.
- **two `result` lines in one window** — distinguishable on **both** `num_turns` and cost, so a reader that reversed, deduplicated or overwrote is red. `reflect.DeepEqual` over the slice pins arrival order.
- **anchor `0`** and **anchor `== len(lines)`**, sharing **one** line list containing at least one `system`/`init` and at least one `result`. Suggested list, four entries: `system`/`init`, `result` A, `system`/`thinking_tokens`, `result` B. Anchor 0 → count 1 and both trailers; anchor 4 → count 0 and a non-nil empty slice.

  Say in the row's comment what each half does, because the pair reads as redundant otherwise: **the `len` row is the sole red for a reader that ignores the anchor and reads the whole slice**, and **the `0` row is what proves the `len` row's emptiness came from the anchor rather than from a line list that had nothing to find.** An anchor of 0 cannot catch the ignore-the-anchor mutant on its own — `lines[0:]` is the whole slice — which is exactly why both rows are required and why they must share one list.

**The trap that silently inverts two rows:** `reflect.DeepEqual` treats `nil` and `[]initControlResultTrailer{}` as different, and that is the *only* instrument for AC 1's non-nil-empty clause. A `want` written as `initControlWindow{systemInitCount: 1}` carries a **nil** slice, so it passes for a reader returning nil and fails for the correct one. Both empty-window rows must spell the field out as `resultTrailers: []initControlResultTrailer{}`.

**Verification.** `make check` never compiles this package, and the suite exits 0 both on a build failure and on a full credentials skip, so the exit code proves nothing:

```
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestInitControlReadWindow|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/
```

Read the count of tests that actually executed. Both must report **PASS, not SKIP**, on a machine with no claude and no credentials. The ban subtest is in the filter because the new `finOfflineExecBans` entry is only enforced when it runs.

**Do not commit an artifact from a live run.** A live re-run mints `initialize_control_v2_1_239_after_completed_turn.json` through the per-arm namer and overwrites nothing, but committing one arm's artifact is #1763's AC — and that ticket additionally requires `initialize_control_v2.1.239.json` byte-identical afterwards. Adding an assertion to `TestRealClaude_InitializeControl_Capture` is throwaway work: it asserts nothing about these three fields today and #1763 deletes it.

## Open questions

- **Should `initControlReadWindow` eventually move beside the arms table when #1715's remaining slices land three arms?** Not this slice's call. The function is package-private and the three arms all read the same window shape, so a move costs one rename and no contract change. Leave it where the ban entry is.
- **`before_first_turn`'s anchor is `0` and is indistinguishable in the bytes from a pre-#1723 artifact.** That is accepted by the record's doc and is not reopened here; the retargeted paragraph in step 4 is what tells a reader which artifacts predate the fields.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. `stdout_events` is child output and therefore untrusted, and this slice adds exactly one new reader over it: `initControlReadWindow`. The boundary is explicit and single — one function, one file, and every field crossing it is read through `initControlWindowField`, which absorbs a decode failure at the field. The trusted side is `initControlResultTrailer`, whose Go types (`int`, `float64`, `bool`) cannot carry an unparsed child string forward. `setModeRecorder.add` remains the only other consumer of the same bytes and is unchanged. **SHOULD FIX:** the reader's doc must state that its input is untrusted child output, since the type `[]json.RawMessage` does not say so on its own and the next author will read the doc, not this spec.
- **[Tokens, secrets, credentials]** No findings. The reader mints, stores, compares and logs nothing. It emits no diagnostic at all — no `t.Logf`, no error — so it cannot print a value it read. That matters concretely here: `runInitControlChild`'s doc carries a standing "never `%+v` the record" rule because that would move child output into a salvaged run log, and this slice adds no log site to the fill path. The new file's `finOfflineExecBans` entry bans `os.Getenv`, `os.Environ` and `os.LookupEnv` by AST name, which is the credential guard for a process environment carrying `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY`.
- **[File operations]** No findings, and the ban entry is the enforcement rather than a claim. The reader takes a slice and opens nothing. The named hazard is the test, not the reader: `go test` runs in the package source directory, so a relative `os.ReadFile("testdata/…")` reaches the committed captures — the ticket says to check the fixture by hand and never from the test, and the entry's `os.ReadFile`/`os.WriteFile`/`os.Create`/`os.ReadDir`/`filepath.Glob`/`packageDir`-group names make that mechanical. No path is built from child output anywhere in this slice; no file is created, so no mode, symlink or atomic-write question arises.
- **[Subprocess / external command execution]** Not applicable by design, and enforced rather than asserted: the new file names no `exec` helper and its entry bans `resolveClaudeBin`, `probeClaudeVersion`, `captureClaudeVersion`, `WithWorktree` and `WithWorktreeAuthenticated`. The fill-site edit passes no new value to `exec.CommandContext` — `argv` is untouched, and `sendPointIndex` is an `int` local that never leaves the record.
- **[Cryptographic primitives]** Not applicable — no randomness, no hashing, no comparison against a secret. The one comparison the reader performs is `type`/`subtype` against two literals, neither of which is secret.
- **[Network & I/O]** No new input path, and the size question is answered upstream rather than skipped: the reader consumes lines `setModeRecorder` already holds, and per-line length is already bounded by `setModeScanMax` on the `bufio.Scanner`, with an over-long line surfacing through `ScannerError`. **SHOULD FIX (accepted, not gated):** the reader itself imposes no bound on the *number* of trailers it returns. That is deliberate and consistent — `initControlFixtureRecord`'s doc records that the verbatim-bytes fields are uncapped by design because capping structured evidence destroys the artifact, and the trailer count is bounded by the `result` lines one budgeted child produced. A cap here would be a new bound in a second place with no observed failure behind it.
- **[Error messages, logs, telemetry]** No findings. The reader returns no error and emits no message, so it has no channel through which to leak. The test's `t.Errorf` prints `got` and `want`, both of which are this file's own synthetic literals — never a live capture, since the ban entry keeps the file away from `testdata/`. The fill-site edit adds no field to the existing `t.Logf`, which deliberately carries only non-child-output values.
- **[Concurrency]** No MUST FIX, and this is the category with a real near-miss. The rejected `snapshotInitModes()` difference is a genuine TOCTOU: two lock acquisitions with an unsynchronised gap, a line landing in the gap attributed to the wrong side of the anchor. The design forbids it and § Design step 3 says why. What ships takes the lock once via `snapshotLines` at the anchor, holds nothing across the stdin write, and reads the window after `<-readerDone` over a snapshot copy — a single lock, no ordering question, no goroutine added, nothing to leak. The remaining gap between the anchor read and the write is bounded and resolves in the safe direction: a line arriving in it is counted *inside* the window, so nothing written in response to the request can fall before the anchor.
- **[Threat model alignment]** No relay and no CLI surface is touched — this slice is entirely inside a test package behind the `e2e_realclaude` build tag, and `make check` never compiles it. The one threat that is genuinely in scope is the one #1723's security review named and handed forward: hostile-shaped child output on a `result` line. AC 1 and AC 2 are that obligation, and § Error handling's table is where each shape's outcome is pinned. The artifact-poisoning threat this family's redaction and deny-scan work addresses is out of scope here and unchanged — this slice writes no new string-bearing field, so `redactInitControlRecord`'s standing "every string-bearing field must be visited" instruction is not engaged.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-25
