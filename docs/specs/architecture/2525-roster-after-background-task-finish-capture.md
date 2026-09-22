# #2525 — Capture whether claude emits `background_tasks_changed` after a background task completes

## Files read

- `internal/e2e/realclaude/task_notification_capture_test.go` → `TestRealClaude_TaskNotificationCapture`,
  `tncapSeedRecord`, `tncapSeal`, `tncapWriteRecord`, `tncapAwaitSubtype`, `fixtureWorthy`,
  `stagingVerdict`, `tncapUnredactedPathFields`, `tncapStageFixture`, `tncapPinLiteral` — the rig this
  ticket reuses and the shape every part of the new probe mirrors. Its phase ordering, its
  cleanup-registration order and its fail-closed seal are copied as *structure*, not as code.
- `internal/e2e/realclaude/tool_progress_capture_test.go` → `tpcapHoldFIFO`, `tpcapCensus` — called, not
  forked. `tpcapHoldFIFO` is the FIFO hold whose `release` is exposed rather than pinned to cleanup, and
  it carries a measured BSD/XNU wakeup fix; `tpcapCensus` is the content-free whole-turn census AC3 asks
  for.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapRecorder` (`Write`, `consume`,
  `snapshot`, `resultSeen`), `dropcapCaptured`, `dropcapMakeEntry`, `dropcapEntry`, `dropcapRedactor`,
  `newDropcapScanner`, `dropcapFixedNeedles`, `dropcapScanner.scan`, `newDropcapArgvHandler`,
  `dropcapWaitForChild`, `dropcapSpawnWait`, `dropcapBashTimeoutEnv`, `dropcapEncodingJSONString`,
  `dropcapSpawnShapeDelta`, `dropcapDenyUsers`, `dropcapDenyHome` — the shared capture machinery, and
  the file header carrying this package's branch-hygiene rule (every file-local identifier takes the
  prefix of the file it was minted in).
- `internal/e2e/realclaude/interactive_background_idle_probe_test.go` → `bgIdlePrompt` — the rig-authored
  prompt that backgrounds `cat <fifo>`. Called, not re-derived.
- `internal/e2e/realclaude/applied_settings_test.go` → its `streamsup.Config` literal sets
  `RequestInitializeOnSpawn` per arm. The precedent for the *pre-first-turn* ask AC1 needs, which is the
  one production runs.
- `internal/e2e/realclaude/interactive_stream_inband_model_test.go` → its phase-2 body calls
  `RequestInitialize` on the live runner and states why it does that rather than setting the config
  field. The precedent for the *mid-session* ask.
- `internal/e2e/realclaude/initialize_control_probe_test.go` → the hand-written control_request and the
  doubly-nested `control_response` → `response` → `response` decode. Read for the reply's shape; this
  probe does **not** copy the hand-written request, because `RequestInitialize` already writes it.
- `internal/streamsup/task_notification_capture_test.go` → `taskNotificationCapturePath`,
  `taskNotificationCaptureVersion`, `taskNotificationPinnedKeys`, `taskNotificationReaderGate`,
  `capturedTaskNotificationLine` — the fourth standalone reader, and the model for the fifth. Its header
  states the no-generalisation discipline and `capturedLines`' docblock forbids a path parameter by name.
- `internal/streamsup/runner.go` → `Config.RequestInitializeOnSpawn`, `Runner.RequestInitialize`,
  `Config.Stdout`, `Config.ControlParser`, `streamsup.WriteTurn` — the two ask positions and the raw tap.
  `RequestInitialize` returns `ErrNoLiveChild` when `Stdin()` is nil, which is the error path Phase 6
  records rather than fatals on.
- `internal/streamsup/parser.go` → `systemTaskStartedLine`, `systemTaskUpdatedLine`,
  `systemBackgroundTasksLine`, `systemBackgroundTaskEntry`, `emitBackgroundTaskRoster` — read only, to
  learn the *shapes claude has been observed to send*. `task_updated`'s payload is `{task_id, patch}`
  with `patch` a raw value; at 2.1.220 that patch was `{"is_backgrounded":true}`. That is why terminal-
  status detection must look inside `patch` as well as at the top level.
- `internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json`,
  `task_notification_v2.1.259.json`, `initialize_control_v2.1.239*.json` — the four prior observations
  the ticket's Context enumerates, re-read at `43a52426` to confirm the roster payload shape
  (`{"type","subtype","tasks":[{task_id,task_type,description}],"uuid","session_id"}`) and that
  `task_notification` at 2.1.259 carried `status:"completed"` and `output_file:""`.
- `docs/knowledge/features/development-verification.md` § "Captures and live evidence" — the arming rule:
  a capture an operator-only switch alone can trigger is never reached by the gate's normal invocation.
- `docs/knowledge/features/e2e-realclaude.md`, `docs/knowledge/features/streamsup-package.md` — the two
  owning package overviews.

## Context

Two clients assume opposite things about one line. Desktop #1246's criterion ("a finished row clears from
the panel on the next roster frame that omits it") assumes claude sends a trailing
`system/background_tasks_changed`; desktop #1558 and mobile #677/#678 assume it does not, and clear on the
status instead. The daemon synthesises no finish and never diffs rosters, so the count only comes down if
claude sends a roster line that omits the finished task.

The repo's four prior observations do not settle it, and the ticket's second refinement pass found why:
the only roster lines anywhere in the repo follow a **mid-session** `initialize` control_response, while
the ask position production actually runs — `RequestInitializeOnSpawn`, one ask per spawned child, before
that child's first turn — has never been followed by a roster at all. A capture that staged only
production's ask would reproduce precisely the arm where nothing has ever appeared, and an absence
recorded there cannot tell "the roster only answers a control request" from "claude never sends it".

So the staging reads through three prompts in a deliberate order: a quiet window first, so an unprompted
roster cannot be mistaken for a prompted one; then a mid-session ask; then a whole further turn, because
claude may batch roster changes to a turn boundary.

This ticket changes no production code. `emitBackgroundTaskRoster` is read, not edited.

**No ADR is warranted.** This adds one evidence probe and one reader to an established family; it decides
nothing about a boundary. The durable finding is the record itself, and the sentence that goes into
`emitBackgroundTaskRoster`'s doc comment belongs to the handoff pass below, not to this run.

### Sizing — a deliberate ceiling breach, on the floor rule

The estimate is ~1200 lines against the 800 ceiling, and I am building it as one ticket rather than
splitting. The only split on offer is probe / reader. The reader's sole subject is this one fixture and
the fixture's sole consumer is that reader, so the floor rule merges them — split apart, the reader would
be built against bytes that do not exist either way, and neither half could be verified alone. **The floor
wins over the ceiling by rule.** #2247 is the same case, kept whole, and shipped. The ticket already
carries `needs-human:sizing` (applied by the refiner, twice re-derived); the overage is rig, not scope —
zero production files, zero new exported symbols, zero consumer call sites.

## Design

Two new files. No production file is created or modified.

### 1. `internal/e2e/realclaude/roster_after_finish_capture_test.go` (build tag `e2e_realclaude`)

File-local identifier prefix `rafcap`, per this package's branch-hygiene rule. A **sibling** of the
notification rig, not an edit to it: `tpcapHoldFIFO`, `tpcapCensus`, `bgIdlePrompt`, `dropcapRecorder`,
`dropcapRedactor`, `dropcapScanner`, `dropcapMakeEntry`, `dropcapWaitForChild` and
`newDropcapArgvHandler` are called.

**Gate (AC1).** Arms on `rafcapFixturePath`'s absence; `PYRY_PROBE_ROSTER_AFTER_FINISH_CAPTURE=1` forces a
re-capture over an existing fixture. The env var is never the gate — `make e2e-realclaude` sets no probe
variable, so an env-gated probe skips before the credential check and the gate passes vacuously.

**Spawn shape.** The interactive YOLO shape (`--model haiku --dangerously-skip-permissions`),
`BASH_DEFAULT_TIMEOUT_MS=5000` so the blocked `cat` leaves the foreground, a fresh empty non-git workdir
under a per-test temp `$HOME`, and — the delta from the rig — `streamsup.Config.RequestInitializeOnSpawn:
true`, which is production's per-spawn ask written before the child's first turn.

**Phases, in AC1's order.** Each is a bounded wait on the recorder's snapshot, never a fixed sleep; the
run stops as soon as the follow-on turn's `result` has been read.

| # | What it waits for | Bound | Ends at `result`? |
|---|---|---|---|
| 0 | a live child (`dropcapWaitForChild`), then the per-spawn ask's own `control_response` | `dropcapSpawnWait`, `rafcapInitializeWait` | n/a |
| 1 | claude's `cat` opening the FIFO (the rendezvous) | `rafcapRendezvousWait` | yes |
| 2 | `system/task_started` — the background task exists | `rafcapBackgroundWait` | yes |
| 3 | **release** the FIFO; `cat` reaches EOF and the task completes | — | — |
| 4 | the **terminal-status line**: the first `task_updated` or `task_notification` carrying a terminal status token | `rafcapTerminalWait` | **no** |
| 5 | the **quiet window**: `rafcapQuietWindow` spent past that line, unprompted | floor, ≥30 s | **no** |
| 6 | a `control_response` past the window, after one mid-session `RequestInitialize` | `rafcapInitializeWait` | **no** |
| 7 | the **follow-on turn**'s own `result` (result count ≥ 2) | `rafcapFollowOnTurnWait` | — |

Phases 4–7 must not end at `result`: a background task outlives the turn that started it, `resultSeen` is
closed via `sync.Once`, and treating it as terminal collapses the wait to one `snapshot()` — the exact
defect `tncapResultIsNotTheEnd` exists to name. Phase 5 is the one wait that is a **floor rather than a
deadline**: spending it is what licenses "nothing arrived unprompted".

**Phase attribution.** Each phase records a *mark* — the number of lines claude had emitted when the phase
ended, taken from the last captured line's `Index+1`. A roster line's phase is then decided by which two
marks its index falls between, which is what lets the verdict name *which of the three prompts preceded
it*. Contract:

```go
// rafcapPhaseFor names the prompt a line at this index followed, from the marks
// recorded at each phase boundary. Returns rafcapPhasePreTerminal for anything at or
// before the terminal-status line.
func (rec *rafcapRecord) rafcapPhaseFor(index int) string
```

**Timeline.** `dropcapRecorder` does not timestamp lines and teaching it to would fork a helper every
probe shares. Instead a file-local `rafcapTimeline` records, on every poll tick, the wall-clock at which
each newly-appeared index was *first observed*. Every phase is a poll loop, so coverage has no gaps; the
granularity is one tick (`rafcapPoll`) and the record's `limitations` says so. This is what turns
"offset" into seconds as well as lines — the difference between a roster arriving immediately and one
arriving thirty minutes later.

**The control traffic (AC2), and the one thing this record deliberately does not keep.** Roster lines are
ordered against `control_response` lines, and a roster is flagged when the immediately preceding captured
line is one. For each `control_response` the record keeps **only** its index, its `request_id` and its
reply subtype, decoded through the doubly-nested shape `initialize_control_probe_test.go` documents —
**never its payload**. AC2 needs the ordering, not the reply's contents, and the initialize reply is the
operator's local claude configuration inventory (model menu, tool and MCP server names, `cwd`,
`apiKeySource`). Dropping the payload removes that exposure class from a committed public artefact
entirely rather than redacting it. See § Security review.

**The record.** One `rafcapRecord`, seeded by `rafcapSeedRecord()` (compile-time constants only, so the
offline deny-scan net scans exactly the bytes the live run writes), with the measured fields filled in at
the call site. Its load-bearing groups:

- *Provenance (AC4):* `ticket`, `claude_version`, `captured_at`, `is_capture: true`, `model`,
  `spawn_shape`, `spawn_shape_delta`, `env_delta`, `workdir`, `prompts`, `redaction`,
  `redaction_rationale`, `credential_scan_applied`, `credential_scan_skipped`, `limitations`.
- *Staging:* `foreground_call_observed`, `background_task_observed`, `fifo_held_seconds`,
  `terminal_status_observed`, `quiet_window_seconds`, `mid_session_ask_sent`,
  `mid_session_ask_answered`, `follow_on_turn_observed`, `per_spawn_ask_answered`,
  `per_spawn_ask_followed_by_roster`, `turn_seconds`, `terminated_on`.
- *AC2:* `rosters[]` — every `background_tasks_changed` line seen after the task started, in order, each
  with `tasks` verbatim, `offset_lines`, `offset_seconds`, `after_control_response`,
  `preceding_line_type`, `results_before` (which places it against the turn's `result`),
  `lists_finished_task`, `phase`, and the redacted payload via `dropcapMakeEntry`. Plus `verdict` and
  `verdict_prompt`.
- *AC3:* `terminal_status_subtype`, `terminal_status_token`, `terminal_status_location`
  (`top-level` | `patch`), `status_candidates[]` (one entry per candidate subtype, fired or not — an
  omitted subtype and one that fired without a status look identical to a later reader and only one of
  those is a measurement), `line_type_census`, `tool_calls`, `tool_result_errors`, `undecoded_lines`.

**The verdict, from a closed set (AC2).** Four values, spelled as file-local constants:

- `task-did-not-complete` — no claim. The terminal-status line never arrived.
- `none-after-terminal-status` — zero rosters after it. *This ticket's most likely real answer.*
- `roster-still-lists-the-finished-task`
- `roster-omits-the-finished-task` — covers an empty `tasks` array.

Decided by the **last** post-terminal roster, because that is the state a client is left in;
`verdict_prompt` names that roster's phase. The full ordered list is in the record either way.
`lists_finished_task` joins on the `task_id` claude put on its own `task_started` line, which is known
whenever `background_task_observed` is true.

**Promotion (AC4).** `fixtureWorthy() (reason string, ok bool)` refuses, naming the reason:

1. the staging failed before a background task existed (`foreground_call_observed` /
   `background_task_observed` false);
2. `terminal_status_observed` false, or the verdict is `task-did-not-complete` — written as evidence,
   refused as the fixture;
3. the quiet window was not actually spent to its floor;
4. `follow_on_turn_observed` false — the follow-on turn is load-bearing, since an absence without it is
   only an absence *within* one turn;
5. `claude_version`'s leading token ≠ `rafcapFixtureVersion`;
6. any kept frame is not `json-string`-encoded (the reader reads only that);
7. `unredacted_path_fields` non-empty.

A `none-after-terminal-status` verdict **promotes**. That is the arm the whole design turns on: refusing
it would refuse this ticket's most likely real answer as vacuous. A failed *mid-session ask* does **not**
refuse either — the ask's failure is itself recorded, and discarding a live turn over it would throw away
the other two prompts' evidence.

`stagingVerdict()` names *which* of the ways a no-claim capture can happen actually happened, so a reader
of a red live gate can tell a rig failure from a finding about claude's surface.

**Writing (AC4).** `t.Cleanup` registered **first** in the body, so LIFO runs it **last** and a structural
`t.Fatalf` still leaves evidence on disk. It writes **unconditionally** to `os.MkdirTemp("",
rafcapArtifactPrefix)` — outside every worktree, the only copy that survives a run the dispatcher
discards — and additionally to `rafcapFixturePath` in-repo when worthy, followed by `git add`
(best-effort, never fatal). `rafcapSeal(scanner, rec)` marshals and deny-scans the **exact bytes about to
be written**, including the decoded bytes of every base64 payload, and is called before *every* write
because fields (git's combined output) enter the record between them.

### 2. `internal/streamsup/roster_after_finish_capture_test.go` (no build tag — inside `make check`)

**A fifth standalone reader, not a generalisation of the four.** Own package constants, the version pin
spliced into the path so the filename cannot drift from what the reader enforces, no path parameter, no
shared helper, every provenance check written out. The duplication is the mechanism: `capturedLines`'
docblock forbids growing any older reader a path parameter by name, because its `is_capture` assertion is
what stops a hand-built payload file being swapped in behind the provenance checks.

```go
const rosterAfterFinishCaptureVersion = "2.1.272"
const rosterAfterFinishCapturePath = "../e2e/realclaude/testdata/roster_after_finish_v" +
        rosterAfterFinishCaptureVersion + ".json"

// IS THE MEASUREMENT THIS TICKET COMMITS. Empty until the live gate has run.
var rosterAfterFinishPinnedVerdict = ""

// Pure, so all four quadrants are proved on every leg — including the one where the
// fixture is still absent and the reader itself can assert nothing.
func rosterAfterFinishReaderGate(fixtureExists, verdictPinned bool) (action, reason string)
```

Quadrants: absent + unpinned → the **one** legal skip; present + unpinned → fatal (bytes committed with
nothing transcribing what they measured); absent + pinned → fatal (a verdict with no bytes behind it);
both → run.

Once the gate says run, every failure is `t.Fatalf`, never a skip. The reader asserts `is_capture`, the
version pin, that `verdict` is in the closed set, that it equals `rosterAfterFinishPinnedVerdict` — and
that **the record does not disagree with itself**: a `none-after-terminal-status` verdict whose `rosters`
list holds a post-terminal entry fatals, as does a roster-bearing verdict with an empty list. A verdict
transcribed from a record that contradicts it is worse than no pin.

## Concurrency model

Unchanged from the rig. One goroutine runs `runner.Run(ctx)`; `os/exec` drives `dropcapRecorder.Write`
from its own copier goroutine while the test goroutine reads `snapshot()` — the mutex inside the recorder
is what makes that legal under `-race`. `tpcapHoldFIFO` starts one goroutine parked in `open(O_WRONLY)`;
its `release` closure is idempotent via `sync.Once` and has a measured BSD/XNU unpark path.

Shutdown, and the registration order that produces it (`t.Cleanup` is LIFO, so *earlier* registration
runs *later*):

1. the record write — registered first, runs last, so a `t.Fatalf` anywhere still leaves evidence;
2. the FIFO release — registered before the runner, so it runs **after** the runner's cancel: the
   descendant reap kills the backgrounded `cat`, and closing the last write end is the backstop if the
   reap missed;
3. `cancel()` + wait for `runDone` within `rafcapRunExitWait`, which errors rather than hanging.

No goroutine outlives the test: the run goroutine exits on cancel, the FIFO goroutine on release.

`rafcapTimeline` is touched only from the test goroutine (inside the poll loop), so it needs no lock —
stated at its declaration, because a helper that looks shareable and is not is how a race gets added later.

## Error handling

Every failure that is not a rig defect becomes **recorded data**, never a lost turn:

- **Staging failures** (no rendezvous, never backgrounded, no terminal status) set the outcome, are named
  by `stagingVerdict()`, refuse promotion, and still write the artifact record.
- **`RequestInitialize` returning `ErrNoLiveChild`** sets `mid_session_ask_sent: false` with the error
  redacted into the detail; the run continues to the follow-on turn.
- **A mid-session ask that is never answered** sets `mid_session_ask_answered: false` and is stated in
  `limitations`. Not a refusal — see § Design.
- **A deny-scan hit** is **fail-closed**: nothing is written, nothing after it is written, and the message
  names the offending **class** only, never the matched value. Printing it would be the exposure the scan
  exists to prevent.
- **A version mismatch** refuses promotion and logs the exact two-line repin instruction
  (`rafcapFixtureVersion` and `rosterAfterFinishCaptureVersion` together) with the observed version
  spliced in. The artifact-dir record still lands.
- **`git add` failing** is recorded, never fatal: the bytes are already in the working tree and in the
  artifact directory.
- **A frame that is not valid UTF-8** is recorded base64 with an empty payload by `dropcapMakeEntry` and
  refuses promotion, because the reader reads only `json-string`.

## Testing strategy

Everything below runs **offline, inside `make check`**. The live turn is the dispatcher's gate, after
verification.

In `internal/e2e/realclaude` (behind `e2e_realclaude`, compiled by `make preship`):

- `TestRafcapFixtureWorthyRefusesEveryBadCapture` — the table AC4 names. One arm per refusal above, plus
  the **promoting** arms that matter: a `none-after-terminal-status` verdict promotes; a capture whose
  mid-session ask went unanswered promotes; a bare version string with no `(Claude Code)` suffix promotes.
- `TestRafcapVerdictFromRosters` — the closed set, decided by the last post-terminal roster: none after →
  `none-after-terminal-status`; one still listing the finished id → `roster-still-lists…`; an empty array
  → `roster-omits…`; a pre-terminal roster only → still `none-after-terminal-status`; no terminal status
  at all → `task-did-not-complete`, and `verdict_prompt` empty on every no-roster arm.
- `TestRafcapPhaseForNamesThePromptThatPrecededIt` — indices either side of each recorded mark.
- `TestRafcapTerminalStatusFindsItInPatchAndAtTopLevel` — the 2.1.259 shape (top-level `status`), the
  2.1.220 `patch` shape, a `patch` that is a string rather than an object, a non-terminal token, and a
  line with no status at all.
- `TestRafcapControlResponsesKeepNoPayload` — the security decision, pinned as a property: a
  `control_response` whose bytes carry an operator-configuration string yields a record entry holding
  only index, request id and reply subtype, and the marshalled record does not contain those bytes.
- `TestRafcapUnredactedPathFieldsNamesTheFieldNotTheValue` — depth, arrays, a redacted path is not a
  finding, an undecodable payload says it could not be swept, and on every row the result must not
  contain the value.
- `TestRafcapAwaitOutlivesTheTurn` — against a recorder fed by hand: the post-terminal waits spend their
  deadline although `result` has closed, still find a line that lands inside them, and the result-count
  predicate distinguishes the follow-on turn's `result` from the first turn's.
- `TestRafcapQuietWindowMeetsItsFloor` and `TestRafcapBudgetOutlastsItsPhases` — arithmetic, offline:
  `rafcapQuietWindow >= rafcapQuietWindowFloor` (30 s, AC1), and `rafcapTurnBudget` exceeds the sum of
  every phase wait, so the final `select` can never find both arms ready and report at random how its own
  turn ended.
- `TestRafcapSealCatchesWhatEntersTheRecordAfterTheFirstScan` — git's index-lock path on both platforms'
  prefixes, and a base64 payload scanned **decoded**.
- `TestRafcapRigAuthoredProseCarriesNoDenyNeedle` — the net that would have saved #2247's first live lap:
  the seed record and every `stagingVerdict` arm carry no deny needle, plus a non-vacuity arm proving the
  net reddens when one is introduced.

In `internal/streamsup` (plain `make check`):

- `TestRosterAfterFinishReaderGateHasExactlyOneLegalSkip` — the four quadrants; non-vacuous on this leg,
  where the fixture does not yet exist.
- `TestRealClaudeRosterAfterFinishVerdictIsPinned` — skips on this leg by the gate, and is the assertion
  that reddens the moment bytes land without a transcribed verdict.

**RED first.** The reader's gate test and the probe's offline tables are written and watched fail before
the code under them exists.

## Documentation handoff

The ticket's **Handoff once the record lands** belongs to the pass that commits the bytes — the bytes
cannot exist during this run, because the gate that produces them runs after verification in a worktree it
discards. Both items are **pending**, and AC5's reader is what makes skipping them redden `make check`
rather than pass in silence:

1. **`internal/streamsup/parser.go` → `emitBackgroundTaskRoster`'s doc comment** — one sentence naming the
   claude version measured, **transcribed from the record's `verdict` and never from what the probe
   expected to see**, in the style of that doc's existing dated `CORRECTED` / `RE-SCOPED` entries.
2. **Cross-post the same finding on `pyrycode/pyrycode-desktop#1246` and `#1558`**, so the two client
   tickets stop assuming opposite things. If the verdict turns on the ask position, say so there: "the
   roster answers a mid-session control request only" is a different instruction to a client than "claude
   never sends it".

Neither is a documentation-stage edit under `docs/knowledge/`; both are named here so the pass that lands
the fixture has them in one place. This ticket's own run edits no shared doc.

## Open questions

1. **Which claude release the gate machine runs.** `rafcapFixtureVersion` and
   `rosterAfterFinishCaptureVersion` are pinned to `2.1.272` — the release the ticket's own live
   observation on 2026-09-22 was taken at. A gate on a newer release refuses promotion loudly and names
   the repin; the artifact record still lands, so nothing is lost but one turn. Resolved by design, not
   by guessing: the alternative (writing under the observed version) would leave the reader skipping while
   bytes sat committed, which is the silent state AC5 exists to make impossible.
2. **Whether `task_updated` or `task_notification` carries the terminal status at 2.1.272.** Unmeasured —
   that is AC3, and the probe watches both rather than assuming either. At 2.1.259 it was
   `task_notification` (`status:"completed"`, top level); the 2026-09-22 live observation reports
   `task_updated`. Both paths are staged and the answer is recorded.
3. **Whether the quiet window is long enough.** 30 s is AC1's floor, not a claim about claude's batching.
   If the record shows a roster arriving just past the window's end, the window was too short and the
   `limitations` field says what the window bounded. Stated as a limitation rather than guessed at.

Each is resolved in Phase B or recorded as a limitation; any that moves the design gets a `## Revisions`
entry here in the same commit as the code that departs.

## Security review

**Verdict:** PASS

The artefact this ticket produces is a **committed, publicly readable file built from a subprocess's
verbatim output**. That is the whole threat surface: every category below is read against "what could
reach `testdata/roster_after_finish_v*.json` that should not".

**Findings:**

- **[Trust boundaries] MUST FIX — found in this pass, plan revised before commit.** The one boundary is
  claude's stdout → a committed artefact, and it is explicit and three-layered: `dropcapRedactor.redact`
  (declared substitution table, applied to every string that enters the record), `dropcapScanner.scan`
  (fail-closed deny scan over the exact bytes about to be written), and `rafcapUnredactedPathFields` (a
  promotion refusal for a residual absolute path). The finding is that this probe introduces a payload
  class **no record in this family has carried**: the `initialize` control_response, which is the
  operator's local claude configuration inventory — model menu, tool and MCP server names, `cwd`,
  `apiKeySource`. Redacting it would be the weak fix, because the classes are open-ended and the
  substitution table cannot know them. **Resolution, now in § Design:** the record keeps only a
  control_response's index, `request_id` and reply subtype, and **never its payload**. AC2 needs the
  ordering, not the contents, so the exposure class is removed rather than filtered.
  `TestRafcapControlResponsesKeepNoPayload` pins it as a property of the record, not a habit of the
  author.

- **[Tokens, secrets, credentials] No findings, and one ordering that is load-bearing.** Nothing is
  minted. `newDropcapScanner` reads `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` via `os.Getenv`
  **as deny needles**, and `WithWorktreeAuthenticated` is what re-pins them into this process's
  environment — so the scanner **must** be constructed after it. Built first, the needle is empty,
  `scan` reports it as `notApplied` (skipped, not fatal), and the credential net is off while every
  message reads green. The order is asserted by construction in the test body and made visible after the
  fact by `credential_scan_applied` / `credential_scan_skipped` shipping inside the record itself.
  Separately, `env_delta` is a **fixed literal** and `os.Environ()` is never read into the record;
  harvesting it would put the operator's whole environment, tokens included, in a public file.

- **[File operations] No findings.** Both written paths are compile-time constants —
  `rafcapFixturePath` is repo-relative and nothing claude emits reaches any path. The record and the
  fixture are written `0600`, the workdir `0700`, the artifact directory by `os.MkdirTemp` (`0700`).
  The gate's `os.Stat` → later `os.WriteFile` is a check-then-use gap, and it is **not** a TOCTOU
  concern here: the path is a constant inside the operator's own checkout, and an actor who can plant a
  symlink there can already edit the test that reads it. Writes are non-atomic; an interrupted write
  leaves invalid JSON, which the reader's `t.Fatalf` on decode reports **loudly**, so the failure mode is
  a red gate rather than a fixture that quietly measures nothing.

- **[Subprocess / external command execution] No findings; two mitigations stated rather than assumed.**
  `git add -- <constant>` is fixed argv, no shell, with `--` ahead of a compile-time constant; nothing
  claude emitted reaches it. claude's own argv is `rafcapArgs` plus streamsup's fixed prefix, all
  constants, and the prompt reaches it on **stdin** as a turn envelope, not as an argument. The probe
  does run claude with `--dangerously-skip-permissions` and a real credential — so the model *could* in
  principle read the temp `$HOME` it is authenticated from and echo it to stdout. That is why the
  construction defences (fresh empty non-git workdir, rig-authored `cat <fifo>` prompt producing no
  output) are paired with a **fail-closed deny scan of different fabric**: construction is the belt,
  and a credential value appearing anywhere in the bytes refuses the whole write. Shutdown of the
  backgrounded `cat` has two independent paths (the runner's descendant reap, then the FIFO release as
  backstop), ordered by `t.Cleanup`'s LIFO.

- **[Cryptographic primitives] Not applicable, by design rather than by omission.** No randomness in this
  probe is security-relevant: `rafcapSessionID` is a fixed literal inside a per-test temp `$HOME` (not a
  secret, and a declared redaction class), and the prompt nonce is `time.Now().UnixNano()` used only to
  make one prompt distinguishable from the last run's — also a declared redaction class. Neither is a
  token, neither authenticates anything, so `crypto/rand` would buy nothing.

- **[Network & I/O] No findings.** No sockets. The one unbounded input is claude's stdout read into the
  parent's memory, and `dropcapRecorder` caps it twice: `maxPartial` (4 MiB, mirroring the parser's own
  `defaultMaxParseBuf`) on the accumulator, and `dropcapMaxCaptureBytes` (8 MiB) on the total, past which
  **whole lines** are dropped and counted rather than truncated — a truncated payload in an evidence
  record is a mutation nothing at the entry would state. Every phase is deadline-bounded,
  `rafcapTurnBudget` bounds the turn, `rafcapRunExitWait` bounds shutdown and errors rather than hanging.
  `TestRafcapBudgetOutlastsItsPhases` is why the budget cannot silently fall below the phase sum, which
  would leave the final `select` with both arms ready and the record misreporting how its own turn ended.

- **[Error messages, logs, telemetry] No findings, and three rules the implementation must not relax.**
  A deny-scan hit names the **class** only; `rafcapUnredactedPathFields` names the **field** (claude's
  vocabulary, which appears in nobody's data) and never the value, asserted on every row of its offline
  table; the refusal and diagnostic `t.Fatalf`/`t.Logf` carry counts, indices, censuses and field names
  only, never claude's bytes. Every error string that reaches a log or the record — `streamsup.New`,
  `RequestInitialize`, `git add`'s combined output — goes through `red.str`, because an unredacted
  `err.Error()` is the easy slip here and git in particular prints repository paths that are in nobody's
  substitution table. The rig's own prose (`redaction_rationale`, `limitations`, `stagingVerdict`) lands
  **inside** the record and is then scanned, so it names path prefixes by symbol (`dropcapFixedNeedles`)
  and never spells them out; `TestRafcapRigAuthoredProseCarriesNoDenyNeedle` is the deterministic net
  under that advisory rule, and it exists because #2247 followed the rule carefully and broke it anyway,
  losing a 360 s live turn inside a 1400 s gate lap.

- **[Concurrency] No findings; one constraint stated at its declaration.** One lock, the recorder's,
  taken and released inside its own methods with no nesting, and `snapshot()` returns a copy so the test
  goroutine never reads mid-mutation. Both goroutines have named exits (run goroutine on cancel, FIFO
  goroutine on release, which is `sync.Once`-idempotent). `rafcapTimeline` is deliberately **unsynchronised**
  and touched only from the test goroutine's poll loop; that constraint is written at its declaration,
  because a helper that looks shareable and is not is how a race gets added by the next ticket.

- **[Threat model alignment] Out of scope, named.** No wire format, no relay, no CLI surface and zero
  production files, so `docs/protocol-mobile.md` § Security model has nothing this change touches. The
  applicable model is the capture family's own: a committed artefact carries no operator-identifying data
  beyond what is deliberately kept **and named** in `redaction_rationale`. Explicitly **not** re-opened
  here: whether the classes the *existing* committed captures deliberately keep — `dropped_lines_v2.1.220.json`'s
  `system/init` configuration inventory in particular — should be narrowed. Those are other tickets'
  artefacts and other tickets' call; this ticket's record does not carry that class at all.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-22
