# #2255 — capture claude's four unmapped operator-facing `system` lines

One live-claude evidence capture, one committed fixture, no production change.
`internal/e2e/realclaude/operator_system_lines_capture_test.go` drives three
triggers on one child, watches a fourth subtype that has no trigger, and writes
`internal/e2e/realclaude/testdata/operator_system_lines_v2.1.259.json`.

## Files read

- `internal/e2e/realclaude/task_notification_capture_test.go` → `TestRealClaude_TaskNotificationCapture`,
  `tncapSeedRecord`, `fixtureWorthy`, `tncapSeal`, `tncapWriteRecord`, `tncapStageFixture`,
  `tncapAwaitSubtype`, `TestTncapRigAuthoredProseCarriesNoDenyNeedle` — the closest
  precedent in every dimension this ticket needs: fixture-absence gate, seed-record split,
  seal-before-every-write, `git add` staging (AC 3), and the offline net that catches a
  rig-authored deny needle before a live turn is spent.
- `internal/e2e/realclaude/compaction_capture_test.go` → `TestRealClaude_CompactionCapture`,
  `ccapAwaitCompactTurn`, `ccapCollect`, `ccapCensus` — the pattern for awaiting a turn that
  may never produce a `result`, which both the blocked prompt and `/cost` need.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapRecorder`, `dropcapRedactor`,
  `dropcapScanner`, `dropcapMakeEntry`, `dropcapWaitForChild`, `newDropcapArgvHandler`,
  `dropcapFixedNeedles`, `dropcapEncodingJSONString` — every reused primitive.
- `internal/e2e/realclaude/tool_progress_capture_test.go` → `tpcapCensus` — the content-free
  census of everything else on the wire, called rather than re-derived.
- `internal/e2e/realclaude/harness_streamparse_test.go` → `parseOne`; `internal/e2e/realclaude/fixtures.go`
  → `WithWorktreeAuthenticated`, `realHome`; `internal/e2e/realclaude/resilience_test.go` →
  `resolveClaudeBin`; `internal/e2e/realclaude/background_trigger_probe_test.go` →
  `probeClaudeVersion`.
- `internal/streamsup/parser.go` → `emitSystemSubtype` (no arm for any of the four),
  `ignoredLineTypes` and its `consumeLine` branch (the silent drop), `streamLine`,
  `streamMessage` (`Content []json.RawMessage`), `consumePermissionDeniedLine`,
  `dropHarnessProseLine` — the four together decide the decode oracle in § Design.
- `internal/streamsup/compaction_capture_test.go` → `compactionCapturePath` — the read-side
  gate shape #2256–#2259 each build for themselves. Out of scope here.
- `internal/sessions/settings.go` → `writeMCPSettings`, `mcpSettingsFile` — precedent for a
  settings file's content and its 0600 atomic write, and nothing more: that file is the
  production per-session path, this one is rig scratch under a per-test temp `$HOME`.
- `internal/turnevent/event.go` → `Unrecognized`, `UnrecognizedUndecodable`.
- `docs/knowledge/features/e2e-realclaude.md`, `docs/knowledge/features/e2e-realclaude-task-notification-capture-test-go.md`,
  `docs/knowledge/features/e2e-realclaude-compaction-capture-test-go.md` — suite conventions:
  no `-race`, no `t.Parallel()` on the live test, offline siblings parallel.

## Context

Four `system` subtypes carry operator-facing text and are dropped today.
`emitSystemSubtype` has no arm for `informational`, `local_command_output`,
`notification` or `commands_changed`, and its `default` returning false lands the
line in `consumeLine`'s `ignoredLineTypes` branch, whose Debug drop is silent. A
`system` line cannot reach the surfaced unrecognized tier at all, so nothing today
would report one arriving. Their field sets are known only from the headless docs
and the Agent SDK type declarations; no committed fixture in this repo has ever held
one (verified: a `git grep` for the four names across every committed `*.json` and
`*.jsonl` returns only the word "informational" inside Go prose in
`internal/agentrun/jsonl/testdata/clean.jsonl`, and `dropped_lines_v2.1.220.json`'s
census lists none of them). #2256–#2259 cannot follow `systemTaskStartedLine`'s rule
— the field set is exactly what the committed capture shows and nothing invented —
without these bytes.

No ADR. This adds no decision that outlives the ticket: it is one more member of the
capture family the package overviews already describe.

### Sizing — the ceiling is exceeded deliberately, and the floor is why

The one-ticket boundary trips on exactly one line: total written work, estimated at
~1350 (about 620 of live rig and helpers, about 650 of offline guards, about 80 of
this plan's own edits). Every other line holds: 0 production source files, 0 new
exported types, 0 consumer call sites, 5 acceptance criteria, and the outcome
machine has three arms with four per-subtype verdicts under them.

The ticket argues the split and I re-derived it rather than deferring to it. Cut per
subtype, each child's fixture has exactly one consumer among #2257/#2258/#2259 —
below the floor the sizing guide puts under a slice — while whole it has three, which
is the shared-infrastructure trigger. The floor wins over the ceiling: a one-consumer
slice cannot be verified on its own, and no resume leg fixes that, where a budget miss
costs one leg. The fixed cost also dominates: the record type, its writer, the
redaction wiring, the seal and the session driver run most of the way to the boundary
before any trigger exists, and the reader docblock in
`internal/streamsup/compaction_capture_test.go` forbids generalising the read side, so
each family writes its own by design. Nearest analogue #2247 landed 1753 lines of
realclaude test against a single trigger; this ticket's three triggers ride one
session driver, one record and one seal.

## Design

### File and naming

One new file, `internal/e2e/realclaude/operator_system_lines_capture_test.go`, under
`//go:build e2e_realclaude`. Every file-local identifier takes the `oslcap` prefix —
siblings add files to this package concurrently and a branch-overlap check does not
catch a same-package identifier collision. `opcap` is deliberately NOT the prefix: it
is a substring of `dropcap`, so it would make every identifier in the family
un-greppable.

The fixture is `testdata/operator_system_lines_v2.1.259.json`, the version spliced
into the path from `oslcapFixtureVersion` rather than repeated, so the filename cannot
drift from the release the record vouches for. `2.1.259` is what `claude --version`
reports on this machine and what `compaction_v2.1.259.json` and
`tool_progress_v2.1.259.json` already pin.

### The gate is the fixture's absence

`os.Stat(oslcapFixturePath)` at the top of the live test, skipping when present, with
`PYRY_PROBE_OPERATOR_SYSTEM_LINES=1` as a FORCE and never as the gate — AC 2. `make
e2e-realclaude` sets no `PYRY_PROBE_*` variable, so an env gate would skip on the env
check before the credential check and the live gate would pass having captured
nothing. That is CLAUDE.md § Testing's #1763 failure, and it is also this repo's
measured one: an env-gated evidence probe makes the live gate vacuous.

### The four subtypes, their triggers and their witnesses

| subtype | trigger | witness that the trigger fired |
|---|---|---|
| `informational` | a `--settings` file whose `UserPromptSubmit` hook exits 2 on a marked prompt | the hook script appends `blocked`/`passed` to a rig witness file; the rig reads the ordered verdicts back |
| `local_command_output` | a `/cost` user turn | `cost` is present in the `system/init` line's `slash_commands` inventory, the turn was written, and the session produced lines after the send |
| `commands_changed` | `<workdir>/.claude/commands/oslcap-probe.md` written mid-session | the file exists on disk while the child is live, AND a later `/oslcap-probe` turn's assistant text carries the command body's rig-authored token |
| `notification` | none exists | none. Watched across the whole session, and the record says exactly that rather than naming a trigger that does not exist |

The `commands_changed` witness is two-level on purpose. The file write is rig-side and
establishes only that the rig did its part; a slash command the session honours is
claude-side and establishes that the inventory was actually re-read mid-session, which
is the thing an absence claim needs. It costs one haiku turn and only matters in the
miss case, where a rig-side-only witness would let "claude sends no `commands_changed`"
stand on a change claude never saw.

That witness is recorded as a BOOLEAN and nothing else. The rig searches the turn's
assistant lines for the command body's token and stores `custom_command_honoured`; the
assistant text itself never enters the record. Recording model prose the design does not
need would widen what a committed public artefact carries, which is the same rule the
hook script follows in declining to write its payload.

`notification`'s record carries `trigger_fired: false` with the reason "no trigger
exists to fire", so its absence is inconclusive by construction and #2259 reads that
rather than a false absence claim.

### Session shape

One child, spawned through `streamsup.New` with `Stdout` set to a `dropcapRecorder` —
the slot production gives the parser, so "upstream of the parser" is wiring rather than
an argument. Args are the YOLO interactive shape every capture in this family uses,
plus the `--settings` pair, which reaches claude through `streamsup.Config.Args` the way
`ccapArgs` does. `oslcapArgs` is therefore a function of the settings path, not a package
var.

Phases, in order, each recorded as an `oslcapPhase` with its own line-index window so a
frame can name the phase it arrived in:

1. **block** — the marked prompt. The hook blocks it. Awaited by
   `oslcapAwaitQuietTurn`, not by a `result`: a blocked prompt may never produce one.
2. **liveness** — a plain prompt asking for one word, awaited on `result`. This is
   AC 1's "later turns on the same child still run", and it is the discriminator that
   separates "the inventory was never re-read" from "the block killed the child".
3. **inventory** — write `.claude/commands/oslcap-probe.md`, then watch a quiet window
   for an unsolicited `commands_changed` push.
4. **command** — `/oslcap-probe`, awaited by `oslcapAwaitQuietTurn`. Its assistant text
   is the claude-side inventory witness.
5. **cost** — `/cost`, awaited by `oslcapAwaitQuietTurn`.
6. **settle** — a short final window, so a late `notification` is not cut off by the
   test returning.

A slash command sent as ordinary message text is honoured on this input path; #2138
drives a literal `/clear` through this runner. That precedent carries its own hazard in
its header, which is why phases 1, 4 and 5 do not block on a `result`.

Worst-case wall clock is about 7.5 minutes, inside the envelope
`TestRealClaude_CompactionCapture` already spends.

Every path the rig writes is rooted in the per-test temp `$HOME` from
`WithWorktreeAuthenticated` or in the `os.MkdirTemp` artifact dir, and every segment is a
compile-time constant of this file — no value claude produces is ever joined into a path.
Modes: workdir 0700, `.claude/commands` 0700, the command file 0600, the rig dir 0700,
settings 0600, hook script 0700, witness 0600, record 0600, fixture 0600.

**One ordering fails silently and has to be stated.** `newDropcapScanner` reads
`CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` via `os.Getenv` AS DENY NEEDLES, and
`WithWorktreeAuthenticated` is what re-pins them into this process. Building the scanner
first yields an empty needle, which `dropcapScanner.scan` reports as not-applied — skipped,
never fatal — so the credential net would be off while every message read green. The
scanner is built after the auth helper, and `credential_scan_skipped` ships in the record
as the after-the-fact evidence that it was on.

### The hook rig

Three rig-authored files under `<temp $HOME>/oslcap-rig/`, deliberately outside the
workdir so the workdir's only non-empty content is the one command file:

- `settings.json` at 0600 — `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"<script>"}]}]}}`.
- `hook.sh` at 0700 — reads the payload on stdin, appends one verdict word to the
  witness file, and on a payload containing the marker prints the rig-authored block
  reason to stderr and exits 2. The witness path and the marker are baked in at write
  time rather than read from the environment, so the script's behaviour does not depend
  on what claude passes down to it — and, more to the point, the script reads NO
  environment variable at all, so the credential claude inherits cannot reach the
  witness file through it.
- `hook-witness.log` at 0600, created by the rig before the child starts rather than by
  the script's first append, so its mode does not depend on the child's umask.

The script deliberately does NOT record the payload. The payload carries the prompt,
the cwd and the session id, and the record needs none of it: the question is whether the
hook ran and whether it blocked.

`oslcapHookScript(witnessPath, marker, reason string) string` is a pure function so the
offline suite can run the real script under `sh` against synthetic payloads and prove
the trigger works before a live turn is spent. It runs as `sh <path>`, never `sh -c`, so
nothing is shell-interpreted from a string.

**The three interpolated values are single-quoted in the generated script, and the
generator refuses any value containing a single quote.** A hook is arbitrary code
execution as the operator by design — `writeMCPSettings`'s doc makes exactly that point
about a file pyry hands claude as `--settings` — so the one place this file builds shell
source has to enforce its quoting rather than rely on today's inputs happening to be
metacharacter-free. Two of the three are compile-time constants; the third is a path
derived from `TMPDIR`, which comes from the environment. The refusal is a `t.Fatalf`
before any child is spawned.

### Does the line decode into `streamLine`? — AC 5, answered by the shipped parser

No mirror of `streamLine` is declared anywhere in this file. For a `system` line the
shipped parser's own verdict is exact, and `parseOne` reads it:

- `parseOne` returns an `Unrecognized` whose `Site` is `UnrecognizedUndecodable` ⇒ the
  line FAILED `streamLine`'s decode. The only two gates inside `consumeLine`'s decode-
  failure branch are `dropHarnessProseLine`, which requires type `user`, and
  `consumePermissionDeniedLine`, which requires subtype `permission_denied` — neither can
  claim a line of the four, so the fall-through to `emitUnrecognized` is unconditional.
- `parseOne` returns no events at all ⇒ the line DECODED and `emitSystemSubtype` had no
  arm for it, so the `ignoredLineTypes` branch dropped it silently.

`system/permission_denied` is the shape that motivates the question: it carries `message`
as a string where `streamLine` declares `*streamMessage`, so `encoding/json` fails the
whole line. Whether each of the four has that shape decides whether its mapping needs
the same separate gate. `oslcapMessageJSONType` answers the second half of AC 5 by
reading the raw line's `message` value and reporting `absent`, `null`, `string`,
`number`, `boolean`, `object` or `array`.

### Types

- `oslcapFrame` — one captured line of one of the four subtypes: the `dropcapMakeEntry`
  payload half (so the base64 arm for invalid UTF-8 is shared rather than re-derived),
  plus `phase`, `decodes_into_stream_line`, `decode_verdict`, `message_json_type`,
  `events_emitted` and the line's top-level `keys`.
- `oslcapSubtype` — AC 4's answer for one of the four, emitted for all four whether or
  not observed: `subtype`, `observed`, `line_count`, `frame_indices`, `trigger`,
  `trigger_witness`, `trigger_fired`, `conclusive`, `note`.
- `oslcapPhase` — `name`, `prompt`, `sent`, `first_line_index`, `line_count`,
  `terminated_on`, `seconds`.
- `oslcapRecord` — the rig-authored half from `oslcapSeedRecord`, plus the measured
  fields: the phases, the four subtype records, the trigger witnesses
  (`hook_verdicts`, `hook_blocked_count`, `cost_in_slash_command_inventory`,
  `cost_turn_assistant_lines`, `command_file_bytes`, `custom_command_honoured`,
  `child_alive_after_block`), `tpcapCensus`'s content-free census of every other line,
  the frames, the redaction table, `credential_scan_applied`/`_skipped`, the settings
  and hook contents as written (redacted), `fixture_staged`, the limitations and the cap
  accounting.

Outcomes are `fired` / `did-not-fire` / `instrument-broken`, the family's three.

### What this capture specifically can carry

The redaction rationale is inherited whole from `dropcapRedactionRationale` — a fresh
workdir under a per-test temp `$HOME`, rig-authored prompts, `os.Environ()` never read
into the record, the declared substitution table over every string, and `dropcapScanner`
as a fail-closed deny-scan. What it must ALSO state, rather than leave to be inferred by
whoever decides to paste this record into a public issue, is what THESE four surfaces can
hold that no sibling capture's does:

- `/cost`'s output is a usage and spend report. On a session of four haiku turns under a
  fresh `$HOME` the numbers describe the rig, but the surface is the operator's account
  and the line may name a plan or a window. It is KEPT, because a redacted local-command
  output would not be evidence of the shape #2257 has to map.
- `informational` is hook feedback. On the happy path it echoes the rig's own block
  reason. On a failing path it can echo the shell's error naming the hook script's
  path, which is why the temp-`$HOME` class and the path sweep both cover it.
- `commands_changed` is a full slash-command inventory — the operator-derived class
  `dropcapRedactionRationale` already names for `system/init`, in a second dress.

### Promotion, and what a partial capture does

`fixtureWorthy` refuses unless: the outcome is `fired`; `claude_version`'s leading token
matches `oslcapFixtureVersion`; every frame is `json-string` encoded, because a base64
frame carries no readable payload for the readers this fixture exists to feed; every one
of the three TRIGGERED subtypes is conclusive, meaning observed, or unobserved with its
trigger witnessed as fired; and `oslcapUnredactedPathFields` reports nothing.
`notification` is exempt from the conclusiveness rule — it has no trigger, so it can
never be conclusive, and requiring it would refuse every capture.

`oslcapUnredactedPathFields` is the third redaction mechanism and the only one this
ticket adds, modelled on `tncapUnredactedPathFields`. It walks each frame's payload AFTER
redaction and reports the JSON field name of any string value that still reads as an
absolute host path — a leading slash with at least two segments. It exists because the
deny-scan's fixed needles cover four known roots and the dynamic ones cover this run's own
paths, and neither sees a path under some other root: #2251 found exactly that, claude's
own `/tmp/cc-socks/<pid>.sock` echoed into `system/init`, which is why
`dropcapClassMessagingSocket` is added by the late adder rather than the constructor. Two
of this ticket's four subtypes are prose surfaces where such a value is plausible — a hook
whose script fails prints an error naming its path, and a local command prints whatever it
prints. The refusal names the FIELD and deliberately not the value.

A subtype that is unobserved with a trigger that did NOT fire is a rig failure, not
evidence, and a fixture recording it as "unobserved" would be the exact confusion this
ticket exists to prevent. So the run refuses promotion, logs the artifact-dir record
that holds the evidence, and fails — naming which trigger did not fire and what to read.
The gate stays armed for the next run, which is the point: a partial fixture on disk
would disarm the fixture-absence gate and freeze the miss in place.

### Where the bytes land — AC 3

Three destinations, and only the first survives every environment:

1. The artifact dir, an `os.MkdirTemp` OUTSIDE the worktree, written from a
   `t.Cleanup` registered before anything that can fail. This copy survives the
   dispatcher's `git worktree remove --force`.
2. `oslcapFixturePath` in-repo, written only when `fixtureWorthy` passes, from the same
   sealed bytes — so the run that produced them is the run that lands them.
3. `git add -- <fixture>` after the write, best-effort and never fatal, with its outcome
   recorded in the record. Fixed argv, no shell, `--` ahead of a compile-time constant,
   nothing claude emitted reaching it. `tncapStageFixture` is the precedent verbatim.

Staging into a worktree the dispatcher discards buys nothing, and the record says so
rather than leaving a later reader to assume it worked. The record is rewritten after
staging so `fixture_staged` is in the artifact-dir copy.

## Concurrency model

One goroutine runs `runner.Run(ctx)`; a `t.Cleanup` cancels it and waits, erroring if it
does not return. `dropcapRecorder` is mutex-guarded because `os/exec` drives `Stdout`
from its own copier goroutine while the test goroutine polls `snapshot()`. Every wait is
a poll over `snapshot()` rather than a second signal channel: `dropcapRecorder` exposes
only `resultSeen`, closed once via `sync.Once`, and teaching it more would fork a helper
every probe in this package shares. All redaction runs on the test goroutine. No FIFO,
no `tpcapHoldFIFO` — nothing here holds anything open, and #2089 spent two repair legs on
that helper.

## Error handling

Nothing claude does is fatal. A rig failure sets `instrument-broken` and returns, and
the cleanup still writes the record: `streamsup.New` failing, no live child within the
spawn wait, a `WriteTurn` error, a settings-rig write failure. Only two things are fatal:
a deny-scan hit, which writes nothing at all and names the class and never the value; and
a capture that reaches the end inconclusive, which fails after the record is on disk.

`oslcapSeal` marshals and scans the exact bytes about to be written, and is called before
EVERY write rather than once — `fixture_stage_detail` carries `git`'s combined output,
and `git` prints repository paths on failure, in nobody's substitution table. #2247's
header records what sealing once cost.

## Testing strategy

The live test asserts nothing about claude. The offline suite is the part `make check`
runs, and it is where the rig is proved before a live turn is spent:

- `TestOslcapDecodeVerdictMatchesTheShippedParser` — the AC 5 oracle, table-driven over
  hand-written `system/informational` lines: `message` object with an array `content`
  decodes; `message` as a string, a number, or an object whose `content` is a string
  does not; `message` absent or null decodes. Non-vacuous by construction — both verdicts
  appear, and a mutant that always answers "decodes" fails half the rows.
- `TestOslcapMessageJSONTypeReadsTheRawLine` — every JSON type plus absent, plus an
  undecodable line.
- `TestOslcapHookScriptBlocksOnlyTheMarkedPrompt` — runs the generated script under `sh`
  against a marked and an unmarked synthetic payload; asserts exit 2 with the reason on
  stderr for the first, exit 0 for the second, and the witness file holding both verdicts
  in order. This is the deterministic net under a stochastic live turn.
- `TestOslcapSettingsFileIsTheShapeClaudeReads` — decodes the written settings file and
  pins the hook wiring and both file modes.
- `TestOslcapFixtureWorthyRefusesEveryBadCapture` — one row per refusing arm, plus the
  promoting rows that matter: an unobserved subtype whose trigger fired is promoted, and
  `notification` unobserved never blocks promotion.
- `TestOslcapSubtypeVerdictSeparatesRigFailureFromFinding` — the per-subtype note must
  read as a finding about claude only when the trigger fired.
- `TestOslcapAwaitQuietTurnDoesNotDependOnAResult` — the three exits, against a real
  `dropcapRecorder` fed by hand, in milliseconds.
- `TestOslcapPhaseAttributionUsesTheLineWindows` — a frame's phase comes from the index
  windows, including a line that arrives before the first send.
- `TestOslcapUnredactedPathFieldsNamesTheFieldNotTheValue` — a frame whose payload keeps
  an absolute path is reported by field name, a redacted one is not, and the report never
  carries the value.
- `TestOslcapHookScriptRefusesAnUnquotableValue` — the generator fatals on a value
  carrying a single quote, and the script it does produce quotes all three.
- `TestOslcapRigAuthoredProseCarriesNoDenyNeedle` — #2247's lesson: scan the seed record,
  every verdict arm, the settings JSON and the hook script against `dropcapFixedNeedles`,
  with a non-vacuity row that appends a needle and requires the scan to redden.

Gate for this ticket: `go test ./internal/e2e/realclaude/...` (the offline half compiles
and runs only under the tag, so also `go vet -tags e2e_realclaude ./internal/e2e/realclaude/`
and `go test -tags e2e_realclaude -run '^TestOslcap' ./internal/e2e/realclaude/`, which
buys compile-plus-execute coverage `make check` never has), plus `go vet ./...` and
`go build ./cmd/pyry`. The live capture runs in the dispatcher's `make e2e-realclaude`
gate, which is the only place with credentials; the ticket earns `needs-real-claude`.

## Open questions

Resolved in Phase B, each with a `## Revisions` entry if it moves the design:

1. Whether a `UserPromptSubmit` hook runs at all under `--dangerously-skip-permissions`.
   Unmeasured, and the hook witness is what makes the answer legible either way.
2. Whether claude re-reads `.claude/commands/` mid-session. The custom-command turn is
   the witness; a miss makes `commands_changed` inconclusive rather than absent.
3. Whether a `/cost` turn closes with a `result` at all. `oslcapAwaitQuietTurn` does not
   depend on it, and `terminated_on` records which arm fired.
4. Whether adding `--settings` to the spawn perturbs anything else on this surface. The
   record carries the observed argv from the runner's own log and the settings content as
   written, so the delta is stated rather than assumed.

## Security review

**Verdict:** PASS (after two MUST FIX items were folded into the design above; the
first pass FAILED on them)

**Findings:**

- [Trust boundaries] MUST FIX, now addressed — the custom-command witness originally
  read claude's assistant text and the plan did not say what it stored. It stores the
  boolean `custom_command_honoured` and never the text; see § The four subtypes. The
  design's other boundaries are the family's and are explicit: claude stdout enters at
  `dropcapRecorder`, every string leaving for the record goes through `dropcapRedactor`,
  and `oslcapSeal` deny-scans the exact bytes before each write. `git`'s combined output
  and the observed argv (which now carries the `--settings` path) are the two values that
  enter the record AFTER the first marshal, which is why the seal runs before every write
  rather than once.
- [Tokens] SHOULD FIX, stated in § Session shape — `newDropcapScanner` must be built
  AFTER `WithWorktreeAuthenticated` or its credential needles are empty and the net is
  silently off while every message reads green. No token is generated, stored or
  compared here. The nonce is `time.Now().UnixNano()` and is not security-relevant: it is
  a redaction target and a prompt discriminator, so distinguishability is the property,
  not unpredictability. The hook script reads no environment variable, so the credential
  claude inherits cannot reach the witness file through it.
- [File operations] SHOULD FIX, stated in § Session shape — every created path's mode is
  now named. No path concatenates a claude-produced value; every segment is a
  compile-time constant under the per-test temp `$HOME` or the artifact dir. Two accepted
  deviations, named rather than left unstated: the fixture is `os.Stat`-ed then later
  `os.WriteFile`-n, a check-then-use whose swap window needs write access to the checkout
  — strictly more capability than anything this test grants — and it is written without
  temp-plus-rename, unlike the registry recipe, because it is a once-written test artefact
  whose partial state fails a reader loudly rather than being reloaded by a daemon.
- [Subprocess] MUST FIX, now addressed — `oslcapHookScript` builds shell source by
  interpolation, and a hook is arbitrary code execution as the operator by design. It now
  single-quotes all three interpolated values and fatals on any containing a single
  quote, so the invariant is enforced rather than resting on today's inputs. The script is
  invoked as `sh <path>`, never `sh -c`. `git add` uses fixed argv, no shell, and `--`
  ahead of a compile-time constant. The `--dangerously-skip-permissions` blast radius
  changes this ticket only in that the workdir gains one rig-authored command file, and
  the settings file is passed to THIS child alone — it is not the operator's
  `~/.claude/settings.json`, so it cannot outlive the temp `$HOME` or reach another
  session.
- [Cryptographic primitives] No findings, and not vacuously — the design performs no
  key derivation, no comparison against a secret and no randomness with a security
  purpose, so there is nothing to get wrong. The one random-looking value is the nonce
  above.
- [Network & I/O] No findings — no listener, no socket, no TLS. The one unbounded input
  is claude's stdout, capped by `dropcapMaxPartial` and `dropcapMaxCaptureBytes`, which
  drop whole lines and count them rather than truncating. The hook payload arrives
  unbounded on the script's stdin; the failure mode is a slow script, not a leak, since
  nothing derived from it is written.
- [Error messages, logs] SHOULD FIX, addressed in § Promotion — `oslcapUnredactedPathFields`
  refuses promotion on an absolute host path surviving redaction, naming the field and never
  the value. The fail-closed deny-scan message names the CLASS only, and the final
  inconclusive fatal prints counts, censuses and subtype names only. `informational` and
  `local_command_output` are the two prose surfaces where an unforeseen host path is
  plausible, which is what motivates the sweep.
- [Concurrency] No findings — one spawned goroutine, `runner.Run(ctx)`, exiting on the
  cleanup's cancel and waited with a timeout that errors on a hang. No second goroutine:
  #1260's rendezvous stamper and #2089's FIFO holder are both deliberately not inherited.
  One lock, inside `dropcapRecorder`, so there is no ordering to document. The record is
  mutated only from the test goroutine.
- [Threat model] Addressed in § What this capture specifically can carry — the relevant
  model for a capture ticket is the publication boundary claude stdout → record →
  committed fixture → public issue, and the section names what these four surfaces can
  hold that no sibling's does, rather than inheriting a rationale written for other bytes.
- OUT OF SCOPE — narrowing the YOLO spawn shape. Every capture in this family uses it and a
  differing shape would be a confound in the evidence; it stays named in
  `spawn_shape_delta`. Also out of scope: mapping any of the four subtypes, which is
  #2256, #2257, #2258 and #2259, and the read-side gates they each build.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-09

## Revisions

### 2026-09-09 — the local slash command is chosen from the session's own inventory

**What changed.** The plan named `/cost` as a fixed trigger. The implementation
chooses the command at runtime from the session's `system/init` slash-command
inventory, preferring `/cost` and falling back through `usage`, `context`, `status`.
`oslcapPickLocalCommand` is the picker, `oslcapLocalCommandCandidates` the ordered
list, and `TestOslcapPickLocalCommandPrefersTheTicketsCommand` pins both the
preference and that no candidate mutates session state.

**What drove it.** Measured, not anticipated. The init line committed in
`dropped_lines_v2.1.220.json` carries a 46-command inventory that does NOT include
`cost`, while it does include `usage`, `context`, `extra-usage` and `usage-credits`.
A hardcoded `/cost` would have driven a turn claude answers as prose, recorded
`local_command_output` as unobserved against a trigger that could not fire, and spent
a whole live gate to learn a name. The ticket's command stays first in the list, so a
session that has it drives it.

**What the record gains.** `local_command_chosen`, `local_command_candidates` and
`local_command_in_inventory` replace the plan's single `cost_in_slash_command_inventory`
boolean, and the turn measurements are renamed to `local_command_turn_lines` and
`local_command_turn_assistant_lines`. When no candidate is in the inventory the rig
still drives the ticket's own command and records `local_command_in_inventory: false`,
which makes that subtype inconclusive rather than silently skipped — the phase is
`local-command` rather than `cost` for the same reason.

**AC 1 is unaffected in substance.** It asks for a `/cost` user turn; the rig sends
one whenever the session knows the command, and records which command it sent and why
when it does not.

### 2026-09-09 — two smaller departures

- The hook witness is re-read and its verdicts recounted after every phase, not only
  after the blocked turn. Whether a slash-command turn fires a `UserPromptSubmit` hook
  at all is itself unmeasured, and the full ordered list is the only place that shows.
- `oslcapFrame`'s `events_emitted` doc was corrected before it shipped: the plan's
  reading ("non-zero means the drop set moved") is true only of a line that DECODES.
  On one that does not, the single event is the `Unrecognized` the decode failure
  produced — the same event `decode_verdict` is read from. The two fields are to be
  read together.

### Size, actual

The file landed at about 2430 lines against the plan's ~1350 estimate, on 84 offline
subtests. The overshoot is comment density and table rows rather than new design: the
nearest analogue, #2247, is 1753 lines for a single trigger, and this ticket drives
three and sweeps a fourth. Recorded here rather than smoothed over, because the sizing
guide's ceiling is calibrated from actuals.

### Open questions

All four remain open by design — each is a question only a live claude can answer, and
the record is built to answer them rather than to assume them. Question 1 (does a
`UserPromptSubmit` hook run under `--dangerously-skip-permissions`) is answered by
`hook_invocations` and `hook_blocked_count`; question 2 (is `.claude/commands` re-read
mid-session) by `custom_command_honoured`; question 3 (does a local-command turn close)
by the phase's `terminated_on`; question 4 (does `--settings` perturb the surface) by
`spawn_shape` beside `settings_content`.

### 2026-09-09 (rework) — the commands_changed witness is read from the wire, and a measured-impossible trigger is a finding

The first live lap ran the whole session green on everything except one row.
`make e2e-realclaude` executed 1126 tests, and
`TestRealClaude_OperatorSystemLinesCapture` failed by design: `commands_changed` had
neither a captured line nor a witnessed trigger, so the run refused to publish a rig
failure as an absence and the fixture was not promoted. Three of the four subtypes had
already answered.

**What the lap measured.** `informational` was OBSERVED — one verbatim line, decoding
into `streamLine`, `message` absent, keys `content`, `level`, `prevent_continuation`,
`session_id`, `subtype`, `type`, `uuid`. That also answers open question 1: a
`UserPromptSubmit` hook does run under `--dangerously-skip-permissions`, two
invocations with verdicts `[blocked passed]`, and the child answered the next unmarked
turn. `local_command_output` was unobserved with its trigger witnessed as fired, which
is a valid finding for #2257. `notification` was unobserved with no trigger, as
designed.

**The defect, and it is the rig's.** `oslcapInitSlashCommands` returned the FIRST
`system/init` line's inventory and every caller read it, including the
`commands_changed` witness. So `probe_command_in_init_inventory` asked what the session
knew BEFORE the rig wrote anything — false by construction however claude behaves — and
the only remaining witness was `custom_command_honoured`, a stochastic read of model
prose that came back false. The record's own census shows what was thrown away:
`system/init: 4` across four turns. Claude re-broadcasts the full slash-command
inventory on EVERY turn, so whether a mid-session file write reached the inventory was
sitting on the wire in two post-write init lines that the rig never read.

**What changed.** `oslcapInitInventories` reads every init line in a half-open window;
`oslcapInitSlashCommands` is now the first-line case of it, which is still correct for
the local-command witness because that one does ask about the pre-write state. The rig
snapshots a pre-write baseline before touching the workdir and, after the two turns
that follow, compares every post-write init inventory against it. `TriggerFired` for
`commands_changed` now turns on a wire measurement — the probe present in a post-write
inventory, or the inventory differing from the baseline at all — with
`custom_command_honoured` kept as a second and independent fabric rather than the only
one. `oslcapSameInventory` is order-insensitive so a reordering cannot manufacture the
witness.

**The design change under it.** A trigger that did not fire was one verdict and is now
two. The rig failing to perform a trigger says nothing about claude and must refuse
promotion. The rig performing it correctly and MEASURING that the mechanism has no
effect is a finding of exactly the kind this ticket commissions, and refusing
promotion for it parks the capture forever on a mechanism that will never work while
taking down the three subtypes that did answer. `oslcapSubtype.TriggerCouldNotFire`
carries the second, `finish` counts it as conclusive, and its note places it in the
same standing as `notification` — no known trigger — reached by measurement rather than
by assumption. The guard against that becoming a loophole is a three-part condition:
the rig wrote the file, claude re-broadcast the inventory at least once afterwards so a
measurement EXISTS, and that measurement says the write did not reach it. Zero
post-write init lines is still INCONCLUSIVE and still refuses promotion.

**AC 4 is served more directly, not less.** It asks for "an unobserved record naming
the trigger attempted and the witness that that trigger fired". The witness is now the
session's own re-broadcast inventory rather than an inference from whether a later turn
was honoured, and AC 1's "a slash-command inventory that changes mid-session" is
measured as the words say rather than proxied.

**The three verifier NITs are fixed in the same commit**, since all three sit in the
code this rework touches. The inventory phase records `Sent: false` — nothing was
written to the child there, the trigger was a file appearing on disk. `oslcapAwaitInit`
waits on an init line's presence rather than a non-empty inventory, and is called after
a turn has been sent rather than at spawn, where it could only ever time out its whole
budget and then record init as unobserved beside a 50-command inventory read from the
same session — which is what the lap recorded. `oslcapCollect` states the equivalence
between the two index spaces its phase attribution compares, and names
`lines_dropped_over_cap` as the field that tells a reader when it no longer holds.

**Tests.** Three new offline tests and two extended ones, 103 subtests from 84, none
skipped: `TestOslcapInitInventoriesReadsEveryInitLine` (with the non-vacuity row a
first-line-only helper still passes), `TestOslcapSameInventoryIgnoresOrder`,
`TestOslcapCommandsChangedVerdictNeedsAPostWriteMeasurement` (six rows over
`oslcapSubtypeRecords`, including the no-measurement row that must stay inconclusive),
plus the measured-impossible arm in the subtype-verdict table and both a promoting and
a still-refusing row in `TestOslcapFixtureWorthyRefusesEveryBadCapture`.

**Open question 2 is now answerable either way.** Whether claude re-reads
`.claude/commands` mid-session is read from `probe_in_post_write_inventory` beside
`inventory_changed_mid_session`, and a false pair is a measured answer rather than a
rig failure. Questions 1, 3 and 4 are answered by the first lap's record.
