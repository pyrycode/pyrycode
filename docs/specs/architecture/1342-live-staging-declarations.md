# 1342 — Probe rig: the run's FIFO name, hold prompt, staged command literal and env delta

**Ticket:** https://github.com/pyrycode/pyrycode/issues/1342
**Size:** S (confirmed — see § Size check)
**File:** new `internal/e2e/realclaude/finding_live_staging_test.go`, `//go:build e2e_realclaude`
**Prefix:** `finLiveStage*`

---

## Files to read first

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/finding_staging_gate_test.go:299-309` | The identity arm: `IssuedCommand != StagedCommand \|\| StagedCommand == ""` → `stage-command-not-staged`. This is the byte equality every declaration here exists to satisfy. |
| `internal/e2e/realclaude/finding_staging_gate_test.go:368-375` | `finOutcomeHoldCommand` — the `sh -c … ; exit 0` **stand-in**, and the recorded reason a fixture must not carry an operator filesystem path. Contrast target; never reuse as the staged literal. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:160-172` | `probeSystemPrompt` (reuse verbatim) and `probePrompt` — the backticked delimiter form this ticket follows. Note the sentence period falls **outside** the backticks. |
| `internal/e2e/realclaude/background_trigger_probe_test.go:142-148` | `probeFIFOName = "probe-hold"`, `probeHeldCommandName = "cat"`. The second is spliced into this rig's command literal. |
| `internal/e2e/realclaude/background_reach_probe_test.go:1089-1113` | `reachRunnerPathFromEnv` — reads the ambient `os.Getenv("PYRY_USE_STREAMJSON")` **first** (`:1103`), delta overrides after. Returns the **full** reading `"ptyrunner (interactive TUI, the agent-run default)"`. |
| `internal/e2e/realclaude/background_reach_probe_test.go:108-131` | `reachFIFOName` + the recorded both-directions collision reason (`:112-114`); `reachEnvDelta` (`:130`), the settled #1223 trigger. |
| `internal/e2e/realclaude/finding_run_record_test.go:256-272` | `finRecordRunnerLabel` — truncates the reading at `" ("`. Compare through this, never the raw return against `"ptyrunner"`. |
| `internal/e2e/realclaude/finding_run_record_test.go:432-439` | `finRecordEnvDelta()` — `PYRY_USE_STREAMJSON=0`, the func form, and the "operator's shell" reason for naming it explicitly. |
| `internal/e2e/realclaude/finding_run_record_test.go:818-827` | The nearest precedent for the runner assertion. It argues ambient-independence in its failure message and **never sets the ambient**. Do not copy as-is — AC3 requires the hostile ambient. |
| `internal/e2e/realclaude/finding_staging_fill_test.go:556-562` | The lengths-only failure-message form for captured-class strings. Copy this shape. |
| `internal/e2e/realclaude/finding_staging_fill_test.go:80-100` | The no-JSON-tags rule at `finTranscriptBashCall` / `finTranscriptReading`, and its reason. |
| `internal/e2e/realclaude/finding_staging_fill_test.go:252-264` | `finTranscriptFill` — the **second** byte-equality (`call.Command == staged`) that silently zeroes `TriggerFired` on a mismatched literal. |
| `internal/e2e/realclaude/finding_live_pin_test.go:1-40` | The pure-file header idiom: what the file does, what it execs (nothing), the `go test -run` line, and the symbol-list discipline in place of an `exec.` grep. |
| `internal/e2e/realclaude/finding_live_pin_test.go:216-234` | `finLivePinFIFOPath` — the synthetic-const fixture form and the splice-the-constant idiom (`finLivePinClaudeCommand` splices `tdnClaudeNeedle`). |
| `internal/e2e/realclaude/interactive_background_idle_probe_test.go:445-459` | `bgIdlePrompt` — the abutting-period form **not** to follow, and its `run=%d` cache-buster (also not adopted). `bgIdleFIFOName` is at `:153`. |
| `internal/e2e/realclaude/teardown_liveness_probe_test.go:146-166` | `tdnFIFOName` (`:150`), `tdnEnvDelta` (`:166`). |
| `internal/e2e/realclaude/trail_run_rig_test.go:103` | `trailRigFIFOName = "trail-rig-subject"`. |
| `internal/e2e/realclaude/finding_stage_held_group_test.go:148` | `finStageFIFOName = "fin-stage-held-subject"`. |
| `internal/e2e/realclaude/fifo_reader_liveness_test.go:99` | `fifoLiveFIFOName = "liveness-hold"`. |
| `internal/e2e/realclaude/fixtures_test.go:348-354` | `TestMain` branches only on `GO_TEST_HELPER_PROCESS`. Nothing gates this package — a regression here is red on a credential-free machine. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:206` | `trailDetail` and its `reachMaxCommandBytes` cap. Cited only to say this file formats **no** `Detail`. |

---

## Size check

**Verdict: S, no split.** Additive single new test file; **zero** existing symbols change, so the edit fan-out is zero — the eight FIFO constants and the four shipped helpers are *read* by the new test, never modified.

| Red line | This ticket |
|---|---|
| > 3 new files | 1 (the test file) + this spec |
| > ~600 LOC total written | ~400–480 projected (see below) |
| > 5 new exported types/interfaces | 0 — every declaration is file-local and lowercase; **no type is declared at all** |
| > 10 consumer call sites | 0 |
| > 5 acceptance criteria | 4 |
| ≥ 10 reject branches | 0 — no state machine; the file ships four declarations and three traps |

LOC projection, sized against the nearest analogue commits in this family rather than bottom-up (bottom-up runs ~40 % low here; doc density is ~1:1):

- #1338 `finding_live_pin_test.go` — 514 lines, one new file, pure function + synthetic ps-table fixture.
- #1304 `finding_staging_fill_test.go` — 602 lines, one new file, fill machinery + a wide case table.
- #1302 — 414 lines; #1313 — 211 lines.

This ticket carries **less machinery** than either 500+ analogue: no function under test beyond two string formatters, no fixture table to build, no record to assemble, no case table over a value space. Bottom-up: header ~90, declarations ~70, extractor ~25, three traps ~230 ⇒ ~415; with the family's doc density ⇒ ~480 ceiling. Under the 600 line.

Production-source-file count for the § Commit self-check: **0** (`*_test.go` is excluded).

**File-overlap check** (`git fetch origin --prune` + `git diff --name-only origin/main...origin/feature/N` over every remote feature branch, run at `26d83b7`): two branches touch `internal/e2e/realclaude/` — `origin/feature/1260` (`dropped_line_capture_test.go`, `testdata/dropped_lines_v2.1.220.json`) and `origin/feature/363` (`fixtures.go`). Neither touches `finding_live_staging_test.go`, and this ticket creates a new file and modifies none. **No overlap; no block set.**

---

## Context

`pyry agent-run` is probed live by staging one turn in which claude is asked to `cat` a FIFO that never closes. `finOutcomeStagingGate` (`finding_staging_gate_test.go:262`) decides whether that staging happened, and its identity arm is byte equality over two opaque strings:

```go
if s.IssuedCommand != s.StagedCommand || s.StagedCommand == "" {  // :299
```

`IssuedCommand` is the model's verbatim `input.command`; `StagedCommand` is whatever the rig says it staged. If the rig declares its internal shell form — or anything with a stray trailing `.`, a nonce that drifted, or a `;` the system prompt told the model not to emit — then **every correctly-staged run reports `stage-command-not-staged`**, the rig looks correct, and one live claude turn is spent per attempt. The same equality is load-bearing one layer down at `finding_staging_fill_test.go:260`, where a mismatch also silently zeroes `TriggerFired` — a second failure arm from the same defect.

This ticket ships the four declarations that equality turns on, plus an offline trap on each. It ships **no live run**: no `pyry` spawn, no real claude, no `ps` exec, no FIFO, no record assembly. Its tests are pure string and environment readings, which is exactly what makes a regression here red under `make e2e-realclaude` on a credential-free machine instead of red on a burned turn.

---

## Design

### The file

`internal/e2e/realclaude/finding_live_staging_test.go`, `//go:build e2e_realclaude`, `package realclaude`.

**Import set is the whole no-exec argument.** The file imports `fmt`, `strings`, `testing` — and nothing else. An `exec.` grep reads clean here by construction and therefore proves nothing (the lesson `finding_live_pin_test.go:38-40` already records), so the check is the import block plus a named symbol list. Lead the file with a header comment in the `finding_live_pin_test.go:1-40` idiom, carrying:

- what the file declares and that it reaches no verdict and takes no measurement;
- the `go test -race -tags e2e_realclaude -run '^TestFinLiveStage' -v ./internal/e2e/realclaude/` line;
- the byte-equality argument above, naming `finding_staging_gate_test.go:299` and `finding_staging_fill_test.go:260` as the two consumers;
- the forbidden-symbol list below, each with its reason.

**Forbidden symbols in this file** (each execs, reads the filesystem, or reads the operator's environment *inside a helper*, so none of them shows up in an `exec.`/`os.` grep of this file):

| Symbol | Why it is barred |
|---|---|
| `spawnProbePyry`, `holdProbeFIFO` | spawn pyry / create a real FIFO — this is #1340's job, explicitly out of scope |
| `probeProcessSnapshot`, `pinScanArgv`, `tdnScan` | each execs `ps` internally |
| `WithWorktree`, `WithWorktreeAuthenticated` | filesystem setup and credentials; nothing here needs either |
| `t.TempDir()` | yields an operator filesystem path — barred by AC1's synthetic-path rule, reason at `finding_staging_gate_test.go:370-375` |
| `os.Getenv`, `os.Environ`, `os.Setenv` | the *only* environment read is the one **inside** `reachRunnerPathFromEnv` (`background_reach_probe_test.go:1103`), and the only write is the single `t.Setenv` AC3's hostile ambient needs |
| `ps -E`, `ps -Eww`, any `ps` at all | those flags dump `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` |

**No type is declared.** The no-JSON-tags rule (`finding_staging_fill_test.go:92-93`) is therefore satisfied structurally rather than by inspection — there is no struct here to tag. State that in the header, and state that a later edit adding one inherits `finTranscriptReading`'s rule, because the prompt and the command are captured strings on a live run.

### Declarations

Five, all file-local, all `finLiveStage*`. Signatures and one-line contracts:

```go
const finLiveStageFIFOName = "fin-live-stage-hold"
const finLiveStageSystemPrompt = probeSystemPrompt   // identifier reference, never a re-typed copy
func finLiveStageCommand(fifoPath string) string     // probeHeldCommandName + " " + fifoPath
func finLiveStagePrompt(fifoPath string) string      // probePrompt's form, the whole command inside the backticks
func finLiveStageEnvDelta() []string                 // {"BASH_DEFAULT_TIMEOUT_MS=5000", "PYRY_USE_STREAMJSON=0"}
```

**`finLiveStageFIFOName = "fin-live-stage-hold"`.** Derived at `26d83b7` from `rg -n 'FIFOName *=|FIFOPath *=' internal/e2e/realclaude/` — eight taken names, and this candidate is substring-disjoint from all eight in both directions. Note the near miss the wider search was added for: `finLivePinFIFOPath` (`finding_live_pin_test.go:225`) is a `FIFOPath` const, so the older `FIFOName`-only recipe misses it. **Re-run the two-alternative recipe at your own HEAD** before committing — a blocker landing between refinement and implementation is how the seven-name list went stale.

**`finLiveStageCommand`** splices `probeHeldCommandName` (`background_trigger_probe_test.go:147`) rather than re-typing `"cat"`. Reason: #1340's liveness read calls `probeHasCommand(…, probeHeldCommandName)` (`:533`), so a rig that staged `cat` while the liveness check looked for something else would be a silent drift; the splice makes a rename a build break. The command is **bare** — `cat <path>` — not `sh -c … ; exit 0`. `finOutcomeHoldCommand` (`finding_staging_gate_test.go:375`) is a gate fixture and is not to be reused here.

**`finLiveStagePrompt`** follows `probePrompt`'s delimited form (`background_trigger_probe_test.go:170-172`), with one change: the **whole** command is interpolated inside the backticks rather than only the path, so the prompt and the staged literal come from one source instead of being written twice. Rendered output is byte-identical in shape to `probePrompt`'s — verb inside the backticks, sentence period outside them. Deliberately **not** `bgIdlePrompt`'s form (`interactive_background_idle_probe_test.go:456-458`), where the period abuts the path the model must reproduce byte-for-byte and no delimiter exists to extract on.

**No cache-buster.** `probePrompt` carries none and fired 7/7 live in #1223; no staleness has been observed on this path. If #1340 ever needs one it goes **outside** the backticks — everything inside them is the byte-for-byte staged literal, and AC1's extraction trap is what catches a nonce that drifts inside.

**`finLiveStageSystemPrompt`** is an alias, not a second prompt. The consuming driver hands `spawnProbePyry` both a prompt path and a system path (`background_trigger_probe_test.go:621`); declaring only the user half here would leave #1340 to invent the other. Defined as `= probeSystemPrompt` so the bytes are identical by construction — which is why it carries no trap: an assertion that it equals its own definition would prove nothing, and saying so is more honest than shipping one.

**`finLiveStageEnvDelta` is a func, not a var**, following `finRecordEnvDelta` (`finding_run_record_test.go:437`): a package-level `[]string` is mutable by every test in the package, and this one is read by two downstream tickets. Its two entries and their sources:

| Entry | Why | Shipped sibling |
|---|---|---|
| `BASH_DEFAULT_TIMEOUT_MS=5000` | the settled #1223 trigger, set on the `pyry agent-run` process; both runners hand claude pyry's environment verbatim, so no production change and no forwarding list | `reachEnvDelta` (`background_reach_probe_test.go:130`), `tdnEnvDelta` (`teardown_liveness_probe_test.go:166`) |
| `PYRY_USE_STREAMJSON=0` | named **explicitly** rather than left unset, because `reachRunnerPathFromEnv` reads the ambient `os.Getenv` first (`:1103`) and only then lets the delta override — an empty delta makes the downstream runner reading a reading of the *operator's shell* | `finRecordEnvDelta()` (`finding_run_record_test.go:437-439`) |

This is the first delta that must name both; neither sibling can simply be reused. **Declare the two entries as this file's own literals** rather than concatenating the two shipped deltas: composing them would make this rig's environment silently follow two other tickets' edits. The *test* asserts containment against both shipped identifiers instead, so an edit to either goes red here loudly rather than changing what this rig sets.

### Fixture

```go
const finLiveStageFixtureFIFOPath = "/tmp/pyry-fin-live-stage/" + finLiveStageFIFOName
```

Synthetic, spliced from the const so a rename tracks. It is passed to the two formatters and to nothing else — never opened, stat'd, canonicalised, joined or executed — so path traversal, TOCTOU and symlink following are structurally inapplicable here rather than merely unaddressed. It must not come from `t.TempDir()` or `os.Getenv`: either would put an operator filesystem path into a test file for nothing, the recorded reason at `finding_staging_gate_test.go:370-375` and the same choice `finLivePinFIFOPath` (`finding_live_pin_test.go:218-225`) made.

### Test-side extractor

```go
func finLiveStageCommandFromPrompt(prompt string) (string, bool)
```

Returns the text between the prompt's **only two** backticks; `ok` is false unless exactly two are present. It is the second, independent reading AC1 requires — the producing side is a `fmt.Sprintf`, the recovering side is a delimiter scan, so the two do not share an implementation. A hand-copied second literal in the test would prove nothing and is the thing this helper exists to replace.

### Data flow

```
finLiveStageFIFOName ──┐
                       ├─> #1340: filepath.Join(t.TempDir(), name) ──> fifoPath
                       │                                                  │
                       │                    ┌─────────────────────────────┴──────────────┐
                       │                    v                                            v
                       │        finLiveStageCommand(fifoPath)          finLiveStagePrompt(fifoPath)
                       │                    │                                            │
                       │                    │                              claude emits input.command
                       │                    │                                            │
                       │                    v                                            v
                       │        finOutcomeStaging.StagedCommand  ==(bytes)==  .IssuedCommand
                       │                                   finding_staging_gate_test.go:299
                       │
                       └─> must not collide, either direction, with the eight shipped FIFO names

finLiveStageEnvDelta() ──> #1340 appends to the pyry agent-run env
                       └─> reachRunnerPathFromEnv ──> finRecordRunnerLabel ──> "ptyrunner"
```

---

## Testing strategy

Three traps, one per behavioural AC. Scenarios, not code — the developer writes them in the package's idiom.

### `TestFinLiveStagePromptStagesTheDeclaredCommand` (AC1)

Drives `finLiveStageFixtureFIFOPath` through both formatters.

- **Extraction round-trip.** `finLiveStageCommandFromPrompt(finLiveStagePrompt(p))` returns `ok` and a string equal to `finLiveStageCommand(p)`. Failure message reports **lengths only**, following `finding_staging_fill_test.go:558-562` — both operands are captured-string class on a live run, and the habit is what keeps a later row that passes a real path safe. The synthetic path is a const a reader can look up, so a length-only message stays diagnosable here.
- **The extractor is not a pass-through.** The recovered string is strictly shorter than the prompt and unequal to it. Without this, an extractor that returned its whole argument would satisfy the round-trip whenever the two happened to agree.
- **Exactly one backticked span.** `ok` is false on a prompt with zero, one, or three backticks — assert the declared prompt's backtick count is exactly 2, and assert `ok` is false for a hand-built mutant with a third. This is what makes the delimiter unambiguous rather than merely conventional.
- **The command carries no backtick.** Otherwise the delimiter is ambiguous and the extraction above is accidental.
- **The command is delimited, not punctuated.** It does not end with `.` — the property that separates `probePrompt`'s form from `bgIdlePrompt`'s, where a trailing `.` abuts text the model must reproduce byte-for-byte.
- **It derives from the run's path.** The command contains `finLiveStageFixtureFIFOPath`, so the two sides are produced from one source.
- **It is a bare command.** No `;`, no `&&`, no `|`, and no `sh -c` prefix — `probeSystemPrompt` tells the model *"do NOT chain commands with && or ;"*, so a staged literal carrying one asks the model to break the instruction whose verbatim echo the byte equality compares. Reference `finOutcomeHoldCommand` in the comment as the shape being avoided; do **not** assert `!=` against it, which is trivially true and proves nothing.

### `TestFinLiveStageFIFONameIsDisjointFromEveryShippedName` (AC2)

A table of the eight shipped names, **referenced by identifier**: `probeFIFOName`, `reachFIFOName`, `tdnFIFOName`, `fifoLiveFIFOName`, `trailRigFIFOName`, `finStageFIFOName`, `bgIdleFIFOName`, `finLivePinFIFOPath`. All eight are file-local consts in this package under the same `e2e_realclaude` build tag, so the reference compiles and a rename breaks the build. A table that re-typed the eight string values would be the hand-copied list this criterion exists to replace. (The row *labels* are strings for the failure message only; the compared values come from the identifiers.)

- **Both directions, per row.** `strings.Contains(finLiveStageFIFOName, shipped)` is false **and** `strings.Contains(shipped, finLiveStageFIFOName)` is false. The rule and its reason are shipped at `background_reach_probe_test.go:112-114`: a concurrently running sibling probe's `cat` must never satisfy this run's content match.
- **The table is exactly 8 rows.** Assert the length so a row deleted during an edit is red rather than silently narrowing the sweep.
- **`finLivePinFIFOPath` is compared as shipped** — it is a path (`/tmp/pyry-fin-live-pin/live-pin-hold`), not a bare name, and a name disjoint from the whole path is disjoint from its basename. No basename splitting.
- **Do not add an empty-name guard.** `strings.Contains(s, "")` is true, so an accidentally empty `finLiveStageFIFOName` fails the second direction on every row — the safe direction. A guard that special-cased it would silence exactly the case that should shout.
- **State the limit in the comment, do not overclaim.** This traps a collision at the time of writing and any later edit to one of the eight. It cannot catch a **ninth** name added elsewhere afterwards, which is why the `rg -n 'FIFOName *=|FIFOPath *='` re-derivation stays in the loop. Note also that `live-pin-hold` is synthetic — #1338's file spawns nothing and creates no FIFO (`finding_live_pin_test.go:10-13`) — so it cannot collide in a live process table; it is in the taken set for the textual rule alone.

### `TestFinLiveStageEnvDeltaNamesTheRunner` (AC3)

Two parts. The first is the one AC3 calls out as the difference between a real assertion and a vacuous one.

**Under a hostile ambient.** `t.Setenv("PYRY_USE_STREAMJSON", "1")` — the single environment write this file makes.

- **Control first: prove the ambient is hostile.** With a `nil` delta, `finRecordRunnerLabel(reachRunnerPathFromEnv(nil))` is `"streamrunner"`. This is what makes the next row non-vacuous *by construction* rather than by argument: it shows the ambient actually reached the helper, so a green claim below is the delta's doing. No test in this package `t.Setenv`s `PYRY_USE_STREAMJSON` (the `PYRY_USE_STREAMJSON=1` at `background_trigger_probe_test.go:255` is a spawned row's *child* env), so wherever the variable is unset the claim passes identically with an **empty** delta — which is why the nearest precedent, `finding_run_record_test.go:819-827`, does not prove what its failure message argues. Do not copy it as-is. That the variable is not always unset is the point: `background_reach_probe_test.go:1092-1094` records it exported in an operator's shell on 2026-07-25, where it silently invalidated a #1223 gate.
- **The claim.** With `finLiveStageEnvDelta()`, `finRecordRunnerLabel(reachRunnerPathFromEnv(…))` is `"ptyrunner"`.
- **Compare like with like.** `reachRunnerPathFromEnv` returns the full reading `"ptyrunner (interactive TUI, the agent-run default)"` (`:1112`); the bare label comes from the shipped `finRecordRunnerLabel` (`finding_run_record_test.go:267-272`), which truncates at `" ("`. Assert through that helper or against the full reading — comparing the raw return to `"ptyrunner"` is red against a correct implementation, and growing a second truncator here is duplicate machinery.

**Composition.** Asserted against the shipped identifiers, not against re-typed strings:

- Every element of `reachEnvDelta` appears in `finLiveStageEnvDelta()` — the settled #1223 trigger, by identifier, so a retune of #1230's delta goes red here pointing at the shared assumption rather than drifting apart silently.
- Every element of `finRecordEnvDelta()` appears in it — the explicit `PYRY_USE_STREAMJSON` naming, by identifier.
- The delta has **exactly two** entries. This is the "and nothing else" clause, and it has a named failure mode rather than being tidiness: `CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1` added here would *suppress* the very trigger the rig depends on — measured in #1223 as `Exit code 143 / Command timed out after 5s`, `is_error: true`, command dead — and the run would report `stage-trigger-did-not-fire`, one more burned turn.

### AC4 — the captured-bytes channels

Structural, not a test, and this file says so rather than shipping a vacuous sweep. The family's `…CarriesNoCapturedBytes` tests exist where a record is **built**; nothing here builds one, so there is no census to plant a needle in. What the file states at itself instead:

- It writes no artifact, logs nothing, and interpolates neither the prompt nor the command into any `Detail` — it formats no `Detail` at all, so `trailDetail`'s cap (`trailer_admissibility_test.go:206`) never applies.
- Test failures **compare** the two captured strings and report **lengths**, per `finding_staging_fill_test.go:558-563`.
- It declares no type, so the no-JSON-tags rule (`finding_staging_fill_test.go:92-93`) has nothing to apply to; a later edit that adds one inherits it.
- No `ps` in any form, and no environment read beyond the delta and the single `t.Setenv` — the import set (`fmt`, `strings`, `testing`) and the forbidden-symbol table above are the check.

### Running

Not env-gated, and must not become so — nothing here needs a Claude login. `TestMain` (`fixtures_test.go:348-354`) branches only on `GO_TEST_HELPER_PROCESS` and otherwise runs `m.Run()`, so these tests run under `make e2e-realclaude` on a credential-free machine. Verified at `26d83b7`. That is why the ticket carries no `needs-real-claude` label, matching every sibling instrument ticket (#1302, #1304, #1313, #1316, #1320, #1324, #1326).

```
go test -race -tags e2e_realclaude -run '^TestFinLiveStage' -v ./internal/e2e/realclaude/
```

Note that `go vet ./...` and `staticcheck ./...` in `make check` run **without** `-tags e2e_realclaude`, so neither analyses this file. `make e2e-realclaude` is the gate.

---

## Concurrency model

None. The file spawns no goroutine, opens no channel, takes no lock, reads no clock and touches no filesystem. Every declaration is a pure function of its argument or a constant. `t.Setenv` is process-global, which is why the AC3 test must not be marked `t.Parallel()` — the Go runtime already refuses that combination, and the point is worth a line in the comment so nobody adds it back.

## Error handling

There is no failure mode to recover from at runtime: the declarations cannot fail. The only error surface is `finLiveStageCommandFromPrompt`'s `ok`, which is false when the prompt does not carry exactly one backticked span. It fails toward the safe direction — a caller that ignored `ok` and used the zero string would compare `""` against the staged command and reach `finOutcomeCommandNotStaged` via the `|| StagedCommand == ""` guard (`finding_staging_gate_test.go:299`), which is a failure arm, not the pass-through.

## Open questions

- **The `probeHeldCommandName` splice couples this rig's verb to #1223's.** If that const were ever retuned (say to add a flag), this rig's prompt changes with it and AC1's trap stays green because both sides derive from one source. That is the intended direction — the alternative is that the staged literal and #1340's `probeHasCommand` liveness read drift apart silently — but it is a coupling, and worth a sentence in the comment so the next reader sees it was chosen rather than inherited.
- **The eight-name table's row labels are re-typed strings.** They feed the failure message only; the compared values come from the identifiers. A renamed const leaves a stale label but never a wrong comparison. Left as cosmetic debt rather than solved with reflection.
- **`finLiveStageSystemPrompt` has no consumer until #1340.** Go does not flag unused package-level consts, and neither vet nor staticcheck runs under this build tag, so it will not trip `make check`. Flagged here so code-review does not read it as dead code.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. This file declares no boundary and crosses none: it takes no network input, reads no file, parses no subprocess output, and decodes nothing. The one value that *will* be untrusted on a live run — claude's verbatim `input.command` — is read by `finTranscriptFill` (`finding_staging_fill_test.go:252`) in #1304's file and compared as opaque bytes by `finOutcomeStagingGate` (`:299`), both already shipped and both out of this ticket's scope. What this ticket contributes to that boundary is the *trusted* operand only. The gate's no-parse contract (`finding_staging_gate_test.go:152-157`) is what keeps that comparison from becoming a filesystem read or a spawn, and this spec adds no pressure on it.
- **[Tokens, secrets, credentials]** No findings, and one prohibition is load-bearing rather than incidental. `ps -E` / `ps -Eww` dump `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` from any process the operator can see; the design bars `ps` in every form, and the forbidden-symbol table names the three helpers (`probeProcessSnapshot`, `pinScanArgv`, `tdnScan`) that exec it *internally* — which is the finding, since a reviewer grepping this file for `exec.` or `"ps"` would see nothing and wrongly conclude the rule holds. The import set (`fmt`, `strings`, `testing`) is the deterministic check behind the advisory list. No token is generated, stored, compared or logged here.
- **[File operations]** No findings — by construction, and the construction is the point. `finLiveStageFixtureFIFOPath` is a compile-time const splice, never `t.TempDir()` and never `os.Getenv`, and it is passed only to two `fmt.Sprintf` formatters. It is never opened, stat'd, canonicalised, `filepath.Join`ed or executed, so path traversal, TOCTOU and symlink following are structurally inapplicable rather than merely unaddressed — the same posture and the same wording `finLivePinFIFOPath` (`finding_live_pin_test.go:218-224`) already carries. No file is created, so no mode question arises.
- **[Subprocess / external command execution]** No findings *in this file*, and one deliberate choice worth naming. The checklist's "`sh -c` is almost always wrong" applies with unusual directness here: AC1 forbids the staged literal from taking `sh -c … ; exit 0` shape, and the reason is stronger than style. The rig stages a **bare** `cat <path>`, so nothing shell-interprets the path on the staged side — where `finOutcomeHoldCommand`'s stand-in shape would put a `;` into a string the model is asked to reproduce byte-for-byte, against a system prompt that forbids chaining. The `;`/`&&`/`|`/`sh -c` assertions in AC1's trap are the enforcement. This file itself execs nothing; #1340 owns the spawn.
- **[Cryptographic primitives]** Not applicable, as a design decision rather than an omission: nothing here is randomised. The spec explicitly declines `bgIdlePrompt`'s `run=%d` cache-buster, so there is no nonce to get wrong — no `math/rand` in a security-relevant position, and no temptation to "upgrade" a wall-clock cache-buster to `crypto/rand` as though it were a token (the confusion `interactive_background_idle_probe_test.go:452-454` had to write a warning against). If #1340 later needs one, this spec routes it *outside* the backticks and AC1's extraction trap catches it drifting inside.
- **[Network & I/O]** Not applicable. No socket, no listener, no HTTP server, no read from any descriptor. The one size-cap question in the neighbourhood — `trailDetail`'s `reachMaxCommandBytes` truncation — does not arise, because this file formats no `Detail`; that is stated in the design rather than left implicit, since a truncation that silently ate a planted needle is a known way to make a leak test pass against a leaking implementation (`finding_staging_gate_test.go:284-290`).
- **[Error messages, logs, telemetry]** No findings, and this is the category where the spec does real work. Both the prompt and the command are captured-string class on a live run — the command embeds a `t.TempDir()` path — so every failure message compares them and reports **lengths only**, per `finding_staging_fill_test.go:558-563`. The file logs nothing and writes no artifact. Residual: this file's own tests only ever pass the synthetic const, so a leak here would leak `/tmp/pyry-fin-live-stage/…` and nothing more; the lengths-only rule is enforced anyway so that a later row passing a real path inherits a safe shape rather than a shape that happened to be harmless.
- **[Concurrency]** No findings. No goroutine, no lock, no shared mutable state — with one real hazard named and closed: `t.Setenv` mutates process-global state, so the AC3 test must not be `t.Parallel()`. Go's runtime already refuses that pairing, which makes this deterministic rather than advisory, and the spec calls it out so nobody adds the call back. `finLiveStageEnvDelta` is a func returning a fresh slice rather than a package-level `var` precisely so no test can mutate what another reads — the `finRecordEnvDelta` precedent (`finding_run_record_test.go:437`).
- **[Threat model alignment]** The relevant threat is the one this whole probe family is built around and it is stated in the ticket: artifacts from these rigs get pasted into public issues, so anything a rig captures is a disclosure channel. This ticket's contribution to that model is negative-space — it declares strings and opens no channel the shipped records keep shut (AC4). Two threats are explicitly **out of scope** and named with owners: the live spawn, the FIFO and its cleanups belong to #1340; the record assembly and its artifact belong to #1343 and #1337, where the capture-and-publish path actually exists and where a census test guards it.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-05
