# #1304 — Fill the staging record from the run's own transcript

Ticket: https://github.com/pyrycode/pyrycode/issues/1304 · Size: **S** · Split from #1279.

Everything here is offline: a JSONL file the test writes under a `t.TempDir()` HOME, no live claude,
no credentials, no daemon, no turn, no `t.Skip`.

---

## Files to read first

Read these before writing anything. Each line says what to extract; do not read the whole file.

| Path | Extract |
|---|---|
| `internal/e2e/realclaude/finding_staging_gate_test.go:138-186` | `finOutcomeStaging`'s eight fields, which three are transcript-side, and the NO-json-tags rule the new carrier type must mirror and cite. |
| `internal/e2e/realclaude/finding_staging_gate_test.go:246-320` | Arm order (identity before behaviour) and arms 1–3 — the three verdicts every row in this ticket lands on, plus the `\|\| StagedCommand == ""` clause. |
| `internal/e2e/realclaude/finding_staging_gate_test.go:621-647` | `TestFinOutcomeStagingGate`'s table + `reached` coverage-map idiom. The new table copies this shape. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:759-816` | Both shipped waiters. `probeWaitForBashToolUse` returns the **first** Bash `tool_use` (the defect); `probeWaitForToolResult` matches a **given** id (why the id is the load-bearing return). |
| `internal/e2e/realclaude/background_trigger_probe_test.go:818-852` | `probeToolUseInput` — the two-stage projection that yields `input` for a **named** block id. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:116-134` | `probeToolUseDeadline` (30 s), `probeToolResultDeadline` (45 s), `probePollInterval` (200 ms). The live values the fill's deadline parameters take; the rows pass a short one instead. |
| `internal/e2e/realclaude/background_reach_probe_test.go:425-452` | #1230's caller-side content guard — the shape this ticket generalises, and the comment saying why the shared rig is not edited. |
| `internal/e2e/realclaude/background_reach_probe_test.go:1039-1087` | `reachBackgroundHandle` (the `timedOutAfterMs` projection) and `reachToolUseCommand` (`input.command`). |
| `internal/e2e/realclaude/background_reach_probe_test.go:1402-1446` | `TestReachBackgroundHandle`'s fixture lines — the exact `toolUseResult`-as-sibling-of-`message` shape the new result-line builder must emit. |
| `internal/e2e/realclaude/tool_loop_test.go:152-183` | `contentBlock` (no `input` member) and `parseContentBlocks` (blocks returned **in order**). |
| `internal/e2e/realclaude/fixtures.go:56-64`, `:145-167` | `WithWorktree` (pins `HOME` to `t.TempDir()`), `ReadJSONL` (parses, `t.Fatalf`s on a bad line). |
| `internal/e2e/realclaude/fixtures_test.go:21`, `:253-273`, `:553-571` | `testSessionID`, the fixture-line idiom, and `writeFixtureLines` — the shipped writer at the session's own path. |
| `internal/e2e/realclaude/prompt_fidelity_test.go:78-84` | `jsonlPathFor` — how both waiters resolve the path. |
| `docs/knowledge/codebase/1223.md:81-95` | The two backgrounding paths; `timedOutAfterMs` is the only separator, and `run_in_background: true` only *corroborates* — the reason it must not become a second trigger signal. |
| `docs/knowledge/codebase/1223.md:160-172` | The shipped-unfixed first-match SHOULD FIX, in the reviewer's own words. |

---

## Context

`finOutcomeStagingGate` (`finding_staging_gate_test.go:262`) already decides all seven staging
outcomes, proven offline from synthetic inputs. Nothing yet **fills** its input from a run's
transcript, and the fill is where the reading goes wrong in two distinct ways:

1. **Value-side.** `probeWaitForBashToolUse` returns the first Bash `tool_use` regardless of
   `input.command`. A decoy Bash call the model makes first captures the whole measurement.
2. **Key-side.** The trigger reading comes from `probeWaitForToolResult(<id>)`. A fill that guards
   the *command* but keeps the first call's *id* has moved the same defect one step downstream —
   the record then says "the staged command's trigger fired" about the decoy's `tool_result`.

So the scan's unit is the **pair** (`tool_use` id, command), never a bare command.

This ticket fills exactly three of the record's eight fields — `BashIssued`, `IssuedCommand`,
`TriggerFired`. The other five (`StagedCommand`, `RendezvousDone`, `PinScanErrored`,
`PinMatchCount`, `PinWantCount`) are not readable from a transcript and stay the caller's.

---

## Design

### One new file

`internal/e2e/realclaude/finding_staging_fill_test.go` — `//go:build e2e_realclaude`, package
`realclaude`. Nothing else in the repo is modified.

The new file is required rather than stylistic: `finding_staging_gate_test.go:143` claims
`finOutcomeStaging` is "the one record in this file with NO json tags". A second tagless carrier
added to *that* file falsifies that sentence by the adding hand.

Give the file a header comment in the family's shape (see `finding_staging_gate_test.go:1-45`):
what it reads, what it never does (no exec, no live claude, no credentials, no `t.Skip`), and the
`-run '^TestFinTranscript'` invocation.

### Types

```go
// One Bash tool_use as the ordered scan found it. The id and the command travel
// TOGETHER because the trigger reading is keyed off the id.
type finTranscriptBashCall struct{ ToolUseID, Command string }

// The three transcript-side readings, and only those three.
type finTranscriptReading struct {
	BashIssued    bool
	IssuedCommand string
	TriggerFired  bool
}
```

Both types carry **no json tags**, and `finTranscriptReading`'s doc comment must state the rule and
cite `finding_staging_gate_test.go:141-157` rather than restate its argument. `Command` and
`IssuedCommand` are verbatim model output; a tag is the first step toward publishing them.

`finTranscriptReading`'s three fields are AC1's second sentence *as a type*: the composition returns
a value from which the other five staging fields are unreachable, so "neither reads nor invents
them" is structural rather than asserted. Do not widen it to a `finOutcomeStaging`, and do not add a
merge method — the caller assigns the three fields at the call site, in the open.

### Functions

```go
// Every Bash tool_use in the transcript, in file order, id paired with command.
func finTranscriptScanBash(t *testing.T, workdir, sessionID string) []finTranscriptBashCall

// THE CONTENT GUARD. Pure over its input: the call whose command is exactly
// staged, else the first call. ok is false only for an empty slice.
func finTranscriptSelect(calls []finTranscriptBashCall, staged string) (finTranscriptBashCall, bool)

// Waits for a Bash call to exist (shipped waiter), then for the staged one to
// appear, to one shared deadline. Returns the selection and whether any call exists.
func finTranscriptSelectBash(t *testing.T, workdir, sessionID, staged string,
	timeout time.Duration) (finTranscriptBashCall, bool)

// TriggerFired for one tool_use id: timedOutAfterMs present on its tool_result.
func finTranscriptTriggerFired(t *testing.T, workdir, sessionID, toolUseID string,
	timeout time.Duration) bool

// The composition. Fills exactly the three transcript-side fields.
func finTranscriptFill(t *testing.T, workdir, sessionID, staged string,
	toolUseTimeout, resultTimeout time.Duration) finTranscriptReading
```

**`finTranscriptScanBash`** — `ReadJSONL` → entries with `Kind == "assistant"` in order →
`parseContentBlocks` (skip a parse error, as the shipped waiters do) → blocks in order where
`Type == "tool_use" && Name == "Bash" && ID != ""` → command via `probeToolUseInput(e.Raw, b.ID)`
then `reachToolUseCommand(<first return>)`. Append the pair. No trimming, unquoting, splitting,
shell-lexing or path resolution anywhere on the path — the bytes `encoding/json` decoded are the
bytes returned. Two `tool_use` blocks on one assistant line are two entries in the slice, in block
order.

**`finTranscriptSelect`** — return the first element whose `Command == staged` (byte equality, the
same rule the gate's identity arm applies); otherwise `calls[0]`; `ok == false` iff `len(calls) == 0`.
Three lines of logic and no `t`, which is what makes AC2's mutation surgical and lets the guard be
proven without touching a file.

**`finTranscriptSelectBash`** — body under 20 lines:

1. `deadline := time.Now().Add(timeout)`.
2. `if id, _ := probeWaitForBashToolUse(t, workdir, sessionID, time.Until(deadline)); id == ""` →
   return the zero call and `false`. **Bind the id inside the `if` statement so it is out of scope
   below.** That scoping is the key-side guard made structural: the first-match id is not merely
   unused downstream, it is unreferenceable there. Do not hoist it to a function-level variable.
3. Poll to the same deadline: scan, select; return as soon as the selection's command equals
   `staged`; on deadline expiry return the current selection (the first call) and `true`; otherwise
   `time.Sleep(probePollInterval)`.

Step 3's loop is six lines beyond a single scan and it earns them: without it the answer depends on
flush **order** rather than on content, since `probeWaitForBashToolUse` returns as soon as *any*
Bash `tool_use` lands and the staged call's envelope "lags the subprocess by a couple of seconds".
A decoy that flushes first would then produce `stage-command-not-staged` on a run that staged
correctly — the same wrong verdict the first-match defect produces, arrived at by a different route.
One shared deadline keeps the whole selection bounded by `timeout`, not by `2 × timeout`. Offline
the file is complete before the call, so the loop returns on its first pass.

**`finTranscriptTriggerFired`** — `probeWaitForToolResult(...)`; `nil` → `false`; otherwise
`reachBackgroundHandle(raw)` and return `timedOut != ""`. Two deliberate omissions, both stated in
the doc comment:

- The handle-present boolean is **not** conjoined. AC4 says the reading is from `timedOutAfterMs`
  alone; a conjunction makes it depend on a second field.
- `tool_use.input.run_in_background` is **not** consulted. It corroborates the model-set path
  (`1223.md:87-88`) — the exact path this probe must exclude — so admitting it counts a run that
  never exercised the expiry lever.

Presence, not value, is the signal: `reachBackgroundHandle` hands back the number as a string, so
`"5000"` and a hypothetical `"0"` both read as fired. That matches the shipped projection's own
contract; #1223 measured `5000` on the expiry path and absence otherwise.

**`finTranscriptFill`** — under 15 lines:

1. `call, issued := finTranscriptSelectBash(...)`; `if !issued` → return the zero
   `finTranscriptReading` (`BashIssued` false, `IssuedCommand` "", `TriggerFired` false).
2. `r := finTranscriptReading{BashIssued: true, IssuedCommand: call.Command}`.
3. `if call.Command == staged` → `r.TriggerFired = finTranscriptTriggerFired(t, …, call.ToolUseID, resultTimeout)`.
4. Return `r`.

Step 3's condition is the same string comparison the gate performs, in a second place, for a
different question — and both must stay. The gate's decides the **verdict**; this one decides what
the fill is **entitled to claim**. `TriggerFired` is a claim about the staged command (that is why
the gate ranks identity above it); reading it off a call that is not the staged command would put a
behavioural claim about the wrong process into the record, and the gate would never consult it
anyway because arm 2 returns first. `false` is the zero value the record's own comment names as the
safe direction. Live, it also drops a pointless 45 s wait on a run that has already lost.

### Data flow

```
writeFixtureLines(t, workdir, testSessionID, lines...)      ← the test writes the transcript
        │                                                     at tuidriver.SessionJSONLPath(<HOME>…)
        ▼
finTranscriptFill
        ├─ finTranscriptSelectBash
        │        ├─ probeWaitForBashToolUse ........ was ANY Bash call issued  → BashIssued
        │        └─ loop { finTranscriptScanBash → finTranscriptSelect }
        │                 ReadJSONL → parseContentBlocks → probeToolUseInput
        │                           → reachToolUseCommand ... (id, command) pair
        │                                                    → IssuedCommand (+ the id)
        └─ finTranscriptTriggerFired (only when the command matched)
                 probeWaitForToolResult(<selected id>) → reachBackgroundHandle
                                                       → TriggerFired

caller: finOutcomeStaging{ StagedCommand, RendezvousDone, PinScanErrored,
                           PinMatchCount, PinWantCount }  ← the five the fill never touches
        + the three above  →  finOutcomeStagingGate  →  finOutcomeResult
```

### Concurrency model

None. The composition is sequential and blocking on the calling goroutine; both shipped waiters
sleep-poll at `probePollInterval`. No goroutine is spawned, no channel is used, no `t.Cleanup` is
registered. The only timing surface is the two deadlines, which is why they are parameters rather
than the live constants: a live caller passes `probeToolUseDeadline` / `probeToolResultDeadline`,
and the rows pass milliseconds. Worst-case live latency is `toolUseTimeout + resultTimeout`,
bounded and sequential.

### Error handling / failure modes

| Condition | Behaviour | Why |
|---|---|---|
| No Bash `tool_use` before the deadline | `BashIssued` false, `IssuedCommand` "", `TriggerFired` false → gate: `stage-no-bash-call` | The two readings stay separate; this is the only path that reports "no call was issued". |
| Bash calls exist, none matches `staged` | `BashIssued` **true**, `IssuedCommand` = first call's command → gate: `stage-command-not-staged` | AC3's anti-collapse rule: "no call matched" is never reported as "no call was issued". |
| A Bash block whose `input` fails to decode | Counts as a call; its command reads `""` | `probeToolUseInput` returns `(nil,false,"")` on a bad envelope and `reachToolUseCommand(nil)` returns `""`. `""` never equals a non-empty staged command, so the block is a fallback candidate but never a match. Honest: a call *was* issued, and what it was is unreadable. |
| No matching `tool_result` before the deadline | `TriggerFired` false → gate: `stage-trigger-did-not-fire` | `probeWaitForToolResult` returns `nil` on expiry; that *is* the did-not-fire signal (`background_trigger_probe_test.go:790-793`). |
| `tool_result` with a handle but no `timedOutAfterMs` | `TriggerFired` false | The model-set path. Not the expiry lever this probe measures. |
| Caller passes `staged == ""` | Not special-cased | The gate's `\|\| StagedCommand == ""` clause already answers `stage-command-not-staged`, and it documents itself as load-bearing. A second copy of that rule in the fill is drift waiting to happen. The only cost is one wasted result wait on a run that has already lost. |
| Malformed JSONL line | `ReadJSONL` `t.Fatalf`s with the path | Shipped behaviour. Offline every fixture line is machine-built valid JSON, so this fires only on a test-authoring error, loudly. |

The fill itself calls no `t.Fatalf` / `t.Error` and never fails a test: like the gate, an instrument
reading is a datum, not a reason to abort.

### What this code may not call

The no-exec purity clause is enforced by a **symbol list with reasons**, not by grepping for
`exec.` — every route off the offline path in this package goes through a helper that execs
internally, so an `exec.` grep reads clean either way.

Forbidden here: `pinScanArgv` / `probeProcessSnapshot` / `reachScanArgv` (run `ps`), `tdnScan`
(process-table read), `holdProbeFIFO` (opens and holds a FIFO), `BuildPyry` / `RunPyryAgentRun` /
`RunPyry*` (build and run pyry), `WithWorktreeAuthenticated` (reads operator credentials and
`t.Skip`s without them), and anything resolving or executing either command string.

Permitted, all pure over bytes or over a file the test wrote: `WithWorktree`, `writeFixtureLines`,
`ReadJSONL`, `jsonlPathFor`, `parseContentBlocks`, `probeWaitForBashToolUse`,
`probeWaitForToolResult`, `probeToolUseInput`, `reachToolUseCommand`, `reachBackgroundHandle`,
`finOutcomeStagingGate`.

---

## Testing strategy

Two tests, both offline, both on the PASS side of the tagged run (neither skips).

### Fixture builders

```go
func finTranscriptBashLine(t *testing.T, toolUseID, command string, runInBackground bool) string
func finTranscriptResultLine(t *testing.T, toolUseID, backgroundTaskID, timedOutAfterMs string) string
func finTranscriptStagedCaller(staged string) finOutcomeStaging
```

- Both line builders **marshal with `encoding/json`**, never string concatenation. A command
  carrying surrounding whitespace, embedded double quotes and a backslash must reach the file
  byte-exactly; a hand-escaped literal that is subtly wrong would make AC5's round-trip row prove
  nothing while still passing.
- `finTranscriptBashLine` emits `{"type":"assistant","message":{"content":[{"type":"tool_use",
  "id":…,"name":"Bash","input":{"command":…}}]}}`, adding `"run_in_background":true` only when
  asked. A row needing two `tool_use` blocks on one line joins two builder calls' content blocks —
  give the builder a variadic or slice form if that reads better; the shape, not the signature, is
  the requirement.
- `finTranscriptResultLine` emits the user line with `toolUseResult` as a **sibling of `message`**
  (`background_reach_probe_test.go:1412-1421`), omitting `timedOutAfterMs` when the argument is `""`.
- `finTranscriptStagedCaller` returns a `finOutcomeStaging` with the five caller-side fields at
  their staged values (`StagedCommand: staged`, `RendezvousDone: true`, `PinScanErrored: false`,
  `PinMatchCount: 1`, `PinWantCount: 1`), so the only thing that differs between a pass-through row
  and a failure row is what the fill read.
- `finTranscriptTestDeadline = 10 * time.Millisecond`. `probePollInterval` is 200 ms, so a waiter
  that finds nothing costs one sleep instead of the live 30 s / 45 s.

**Assertion-message rule (leak class).** No failure message in this file may interpolate
`IssuedCommand`, `StagedCommand` or a `finTranscriptBashCall.Command`. Name the row, the expected
and actual **outcome value**, and the `tool_use` id. AC5's byte-equality row is the single
exception and must compare with `!=` and report only *that* the strings differ plus their lengths —
the same discipline `finOutcomeResult.Detail` is held to one tier up.

### `TestFinTranscriptSelect` — the guard, pure

Table over `[]finTranscriptBashCall` and a staged string. Rows as scenarios:

- empty slice → `ok == false` (and the zero call).
- one call, command ≠ staged → returns that call, `ok == true`. (The fallback: a call *was* issued.)
- decoy first, staged second → returns the **second**, id and command together. This is the row
  AC2's mutation kills.
- staged first, decoy second → returns the first; proves the guard is content-keyed, not
  position-keyed in the other direction.
- two calls, neither matching → returns the **first**, so the fallback is deterministic.

### `TestFinTranscriptFill` — end to end through a real JSONL

Per row: `workdir := WithWorktree(t)`; `writeFixtureLines(t, workdir, testSessionID, tc.lines...)`;
`r := finTranscriptFill(t, workdir, testSessionID, tc.staged, finTranscriptTestDeadline,
finTranscriptTestDeadline)`; assign `r`'s three fields onto `finTranscriptStagedCaller(tc.staged)`;
assert `finOutcomeStagingGate(s).Value == tc.want`.

| # | Transcript | Expected | What it kills |
|---|---|---|---|
| 1 | Decoy Bash call (line 1) + its `tool_result` **without** `timedOutAfterMs`; staged Bash call (line 2) + its `tool_result` **with** `timedOutAfterMs: 5000` | `stage-ready-to-classify` | Value-side first-match (→ `stage-command-not-staged`) **and** key-side first-match (→ `stage-trigger-did-not-fire`). AC2's primary row. |
| 2 | One decoy Bash call only, with a `tool_result` | `stage-command-not-staged` | Collapse of the two readings. Assert `r.BashIssued == true` **on the reading**, not only the gate value — a fill that reported "no call issued" here would make a false statement about the run. |
| 3 | One assistant text-only line + one user line, no `tool_use` at all | `stage-no-bash-call` | The other half of AC3. Assert `r.IssuedCommand == ""`. Hits `probeWaitForBashToolUse`'s deadline (one 200 ms poll). |
| 4 | Decoy and staged as **two `tool_use` blocks on one assistant line**; decoy's `tool_result` carries `timedOutAfterMs: 5000`; staged call's carries the handle **without** it; the staged `tool_use.input` carries `run_in_background: true` | `stage-trigger-did-not-fire` | Key-side first-match (would read `5000` → `stage-ready-to-classify`) **and** a fill admitting `run_in_background` as a second trigger signal (same red). Also proves the scan is ordered *within* a line. AC4. |
| 5 | One Bash call issuing a staged command with **leading and trailing spaces, embedded double quotes and a backslash**; `tool_result` with `timedOutAfterMs` | `stage-ready-to-classify` **plus** `r.IssuedCommand == tc.staged` byte-for-byte | Any normalisation in the fill. AC5. |
| 6 | Staged Bash call, **no `tool_result` at all** | `stage-trigger-did-not-fire` | The `probeWaitForToolResult` → `nil` path, and that the result deadline is actually threaded (one 200 ms poll, not 45 s). |

**Coverage sweep**, in `TestFinOutcomeStagingGate:641-646`'s shape: collect the outcome values the
rows produced and assert all four reachable ones were reached (`stage-no-bash-call`,
`stage-command-not-staged`, `stage-trigger-did-not-fire`, `stage-ready-to-classify`) **and** that
none of the three caller-side outcomes (`stage-rendezvous-incomplete`, `stage-pin-scan-errored`,
`stage-pin-count-unexpected`) appeared. Since every row's caller-side fields come from
`finTranscriptStagedCaller` unchanged, a caller-side outcome could only mean the fill wrote a field
it does not own — AC1's first clause, as a test.

### The two mutations — run them, record the red

Both run without touching the worktree, via a mutated copy plus a Go overlay:

```bash
go test -race -tags e2e_realclaude -overlay=<abs-path-to-overlay.json> \
  -run '^TestFinTranscriptFill$/<row>' ./internal/e2e/realclaude/
```

- **M1 (AC2) — drop the content guard.** In `finTranscriptSelect`, delete the match loop and return
  `calls[0]`. Expect row 1 RED with `stage-command-not-staged`, and `TestFinTranscriptSelect`'s
  "decoy first, staged second" row RED. Grade each under its own `-run`.
- **M2 (AC5) — normalise.** Wrap the command projection in `finTranscriptScanBash` with
  `strings.TrimSpace`. Expect row 5 RED with `stage-command-not-staged`.

Paste the failing output (row name + got/want outcome values — **not** the command bytes) into the
PR description. "The mutation is run and its red recorded, not argued."

### Verification commands

```bash
go vet -tags e2e_realclaude ./internal/e2e/realclaude/...
go test -race -tags e2e_realclaude -run '^TestFinTranscript' -v ./internal/e2e/realclaude/
go test -race -tags e2e_realclaude ./internal/e2e/realclaude/...   # expect a PASS/SKIP split
```

`make check` / `make build` never compile `e2e_realclaude`-tagged files, so a green from either is
vacuous for this diff. The rows added here are all on the PASS side — none of them skips.

---

## Scope note

- New files: **1** (`internal/e2e/realclaude/finding_staging_fill_test.go`).
- Modified production source files: **0**. No existing symbol changes signature; no consumer call
  site moves. `codegraph_impact` fan-out is nil because everything is additive.
- New exported types/interfaces: **0**. New unexported symbols: 2 types, 5 functions, 3 fixture
  helpers, 1 constant, 2 tests.
- Projected total written lines (code + the family's ~1:1 comment density + tests): **~560**, one
  file. Inside the 600-line boundary but not by much — if the header comment and per-row rationale
  push past it, cut row 6 (the only row not named by an AC) rather than thinning the comments,
  which are load-bearing in this family.
- Identifier prefix `finTranscript*` re-verified 0 occurrences repo-wide against a known-taken
  control (`trail[A-Z]`, 1422 occurrences in `internal/`).
- Branch-overlap check (2026-08-05, `git fetch origin --prune` then every `origin/feature/N`):
  `origin/feature/1260` touches `dropped_line_capture_test.go` + testdata; `origin/feature/363`
  touches `fixtures.go`. This ticket creates one new file and modifies neither. No block set.

## Open questions

- **`timedOutAfterMs: 0`.** Presence-only is specified above, matching `reachBackgroundHandle`'s
  own contract; #1223 measured `5000` or absence, never `0`. If a live run ever produces `0`, that
  is a finding for the live ticket — not a defence to pre-build here.
- **Fallback command choice.** When nothing matches, the fill reports the *first* call's command
  rather than `""`. `""` would make the decoy row indistinguishable at the reading level from a
  block whose `input` failed to decode. Neither string is ever published, so only the gate's verdict
  travels; the live ticket may revisit if it wants the reading for diagnostics.
- **Live wiring** (which caller builds the five caller-side fields, and where the fill is invoked
  relative to the rendezvous) is the live ticket's. The byte-equality constraint travels outward:
  the live rig's staged literal must be exactly the string it asks the model to emit. Nothing here
  softens the comparison to make that easier.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No new boundary. The transcript file is the untrusted side (verbatim model
  output); the boundary is the already-shipped projection set — `parseContentBlocks`,
  `probeToolUseInput` + `reachToolUseCommand`, `reachBackgroundHandle` — and this spec adds no
  second decoder, no regex over the raw line, and no `json.Unmarshal` of its own. The trusted-side
  type is `finTranscriptReading`, whose one string field is documented as verbatim model output and
  carries no json tags; the type's three-field shape is what stops the untrusted string from
  travelling further than the gate.
- **[Tokens, secrets, credentials]** No findings, by construction: the fill runs under
  `WithWorktree`, whose `t.TempDir()` HOME contains no `~/.claude.json` and no credential.
  `WithWorktreeAuthenticated` is on the forbidden-symbol list — it reads operator credentials and
  `t.Skip`s without them, which would also break the "no `t.Skip`" contract.
- **[File operations]** Paths are not caller-composed: the transcript path comes from
  `tuidriver.SessionJSONLPath(<HOME>, workdir, sessionID)` with the shipped `testSessionID`
  constant, under a `t.TempDir()` HOME, so no user-controlled segment reaches a path join and there
  is no traversal surface. `writeFixtureLines` creates the dir at `0o700` and the file at `0o600`.
  **OUT OF SCOPE:** `probeWaitForBashToolUse` does `os.Stat` then `ReadJSONL` — a check-then-use
  gap on a path the rig owns. Under a per-test `t.TempDir()` with a single writer there is no
  adversary; closing it means editing the shared rig, which #1230 deliberately declined to do
  (`background_reach_probe_test.go:433-438`). If it is ever closed, it belongs to a rig ticket, not
  to a caller-side fill.
- **[Subprocess / external command execution]** No findings, and enforced as a symbol list rather
  than an `exec.` grep — see § *What this code may not call*. Every route off the offline path in
  this package execs *inside* a helper, so a grep for `exec.` reads clean whether or not the code is
  clean. The gate's no-exec clause is preserved in the strong form: neither command string is
  resolved, lexed, stat'd or executed; the only operation on either is `==`.
- **[Cryptographic primitives]** Not applicable, with the reason: no randomness is generated and
  no value is compared against a secret. The one comparison is model output vs a rig literal, and
  constant-time comparison would be noise — the result is published as an outcome value, not as an
  oracle, and neither operand is a credential.
- **[Network & I/O]** No sockets, no listeners, no timeouts to configure. Input size: `ReadJSONL`
  materialises the whole transcript per poll, which is shipped behaviour that
  `probeWaitForBashToolUse` already performs on every 200 ms tick; the fixtures are a handful of
  lines and a live transcript is bounded by claude's own writing. The selection loop adds one scan
  per tick to a loop that already did one — same order, no new exhaustion surface.
- **[Error messages, logs, telemetry]** **SHOULD FIX, addressed in this spec.** The live leak path
  here is a test failure message: `t.Errorf("got %q, want %q", r.IssuedCommand, tc.staged)` prints
  model bytes and an operator filesystem path into CI output, which is the same class
  `finOutcomeResult.Detail` is capped and needle-tested against one tier up. § *Testing strategy*
  therefore pins the rule — failure messages name the row, the outcome values and the `tool_use`
  id, never the command bytes; AC5's byte-equality row reports difference plus lengths only. The
  fill's own code emits no logs and calls no `t.Fatalf`.
- **[Concurrency]** No findings. No goroutine is spawned, so none can leak; no lock is taken, so no
  ordering exists to get wrong; no `t.Cleanup` is registered, so nothing runs after the test body.
  Both waiters poll on the calling goroutine and are bounded by caller-supplied deadlines —
  `finTranscriptSelectBash` shares one deadline across the shipped wait and the selection loop
  precisely so the composition cannot exceed `timeout` by waiting twice.
- **[Threat model alignment]** The threat this family actually models is a captured string reaching
  a public GitHub issue (`finding_staging_gate_test.go:141-157`): the staged command embeds a
  `t.TempDir()` path and an `exec.LookPath` result, and the issued one is unreviewed model output.
  This spec addresses it at four points — the three-field return type, no json tags on either new
  type, no `Detail` interpolation (gate-side, already pinned by
  `TestFinOutcomeResultCarriesNoCapturedBytes`), and the assertion-message rule above. Out of scope
  and named: the shared rig's first-match defect itself stays unfixed in
  `probeWaitForBashToolUse` — this ticket guards caller-side, as #1230 did.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-05
