# #2247 — capture claude's `system/task_notification` line verbatim

## Files read

- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapRecorder`, `dropcapRedactor`,
  `dropcapScanner`, `dropcapMakeEntry`, `dropcapWaitForChild`, `newDropcapArgvHandler`,
  `dropcapCaptured`, `dropcapExpectedSubtypes`, `dropcapEncodingJSONString`, `dropcapBashTimeoutEnv`,
  `dropcapSpawnWait` — the shared rig this probe reuses whole, and the staging it extends. Its
  `dropcapExpectedSubtypes` already enumerates all four background-task subtypes.
- `internal/e2e/realclaude/tool_progress_capture_test.go` → `tpcapHoldFIFO`, `tpcapCensus`,
  `TestRealClaude_ToolProgressCapture`'s fixture-absence gate, `tpcapRecord.fixtureWorthy`,
  `tpcapRecord.stagingVerdict`, `tpcapWriteRecord` — the arming, promotion and rig-driven FIFO hold
  this probe copies in shape and calls in substance.
- `internal/streamsup/compaction_capture_test.go` → `compactionReaderGate`,
  `TestCompactionReaderGateHasExactlyOneLegalSkip`, `compactionPinnedShapes` — the four-quadrant
  sequencing gate AC5 asks for, verbatim in shape.
- `internal/streamsup/capture_test.go` → `capturePath`, `capturedLines` — the reader whose docblock
  forbids growing a path parameter, which is why AC5's reader is a fourth reader rather than a
  generalisation.
- `internal/streamsup/tool_progress_capture_test.go` → `capturedToolProgressLines`,
  `toolProgressCaptureVersion` — the version-pin discipline restated at each reader.
- `internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json` — the 2.1.220 record. Its census is
  `system/task_started:1`, `system/task_updated:1`, `system/background_tasks_changed:1`, and
  `expected_absent` is exactly `["task_notification"]`. Its three task payloads carry **neither**
  `ambient` nor `skip_transcript`, which is the prior AC2 measures against.
- `internal/e2e/realclaude/testdata/tool_progress_v2.1.259.json` — `line_type_census` counts
  `system/task_notification: 1` with no frame kept. Its staging (`env_delta`
  `BASH_DEFAULT_TIMEOUT_MS=180000`, a FIFO held 75 s then **released**) is the existence proof that a
  released FIFO is the lever.
- `internal/e2e/realclaude/interactive_background_idle_probe_test.go` → `bgIdlePrompt` — the
  backgrounding-neutral prompt #1260 uses; reused unchanged.
- `internal/e2e/realclaude/background_trigger_probe_test.go` → `holdProbeFIFO`,
  `probeFIFOReleaseDeadline`, `probeClaudeVersion` — the FIFO hold whose release is pinned to
  `t.Cleanup`, i.e. the one this probe must NOT use.
- `internal/e2e/realclaude/harness_streamparse_test.go` → `parseOne` — the shipped parser's verdict
  per line, recorded as data.
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans` — an opt-in per-file
  table. A file absent from it carries no ban, so the new probe's `exec.Command` is unconstrained by
  it; it is nonetheless argued for in the security review below.
- `internal/streamsup/parser.go` → `ignoredLineTypes`, `emitSystemSubtype` — the "Still dropped in
  silence" paragraph naming `task_notification` measured-absent here, and the single enumeration of
  the mapped set. Both are out of scope; #2245 rewrites the first.
- `docs/knowledge/features/streamsup-package-system-maps-per-subtype-since-2026-08-07.md` — the
  family's declare-only-what-the-capture-shows rule, and why a subtype's arm is added at one site.

## Context

`internal/streamsup` maps three background-task subtypes and declares, for each, exactly the keys its
one committed capture shows. `task_notification` is the fourth sibling and has no captured payload
anywhere in the repo, so #2245's mapping would have nothing to declare from. This ticket produces the
bytes.

Two secondary measurements ride along because they cost no extra staging: whether claude 2.1.259 sends
`ambient` and `skip_transcript` on the three companion subtypes (the 2.1.220 record shows neither), and
how the observed `task_notification` key set compares against the Agent SDK's documented
`SDKTaskNotificationMessage`. The SDK list is carried in the record as a **check-against**, never as a
field set to declare from.

No ADR is warranted: this adds no decision the family has not already made.

## Design

Two new files, no production change. `streamLine` and every drop behaviour are untouched.

### 1. `internal/e2e/realclaude/task_notification_capture_test.go` — the probe

Build tag `e2e_realclaude`. Every file-local identifier takes the `tncap` prefix, per the package's
branch-hygiene rule.

**The staging is #1260's plus one lever.** `dropped_lines_v2.1.220.json` proves that `bgIdlePrompt`
with `BASH_DEFAULT_TIMEOUT_MS=5000` makes claude run `cat $FIFO`, time the foreground call out at 5 s
and background it — firing `task_started`, `background_tasks_changed` and
`task_updated{is_backgrounded:true}`. It fires no `task_notification` because that record's
`holdProbeFIFO` pins its release to `t.Cleanup`, so the task never completes. This probe releases the
FIFO **mid-test**, which lets `cat` see EOF and the background task reach a terminal state.

**Three shared helpers are called, not forked**, and two of them carry a `tpcap` prefix. That prefix
marks the file an identifier was minted in, not private scope, and re-deriving either would be exactly
the fork the ticket forbids:

- `tpcapHoldFIFO` is `holdProbeFIFO` with the release exposed instead of pinned to cleanup — the one
  difference this probe needs. Its body encodes a measured BSD/XNU wakeup fix (a transient read-open
  does not unpark a writer blocked in `open(O_WRONLY)` there) that cost a hung live gate to find. Its
  failure messages name #2089 because the mechanism is that ticket's.
- `tpcapCensus` produces the whole-turn line-type census AC1 asks for, plus the tool-name and
  tool-error diagnostics that let a did-not-fire record say what claude did instead.
- `bgIdlePrompt` is reused verbatim. It says nothing about backgrounding, which is the measured axis.

**Gate.** The fixture's absence arms the probe; its existence disarms it.
`PYRY_PROBE_TASK_NOTIFICATION_CAPTURE=1` survives only as a force for re-capture at a new claude
release. An env-gated probe skips on the env check before the credential check, so `make
e2e-realclaude` would pass vacuously and the fixture would never land — CLAUDE.md § Testing's #1763
failure, and #2089's own gate comment argues the same break.

**Record.** `tncapRecord`, marshalled to `tncap-record.json` in an `os.MkdirTemp("")` artifact dir that
outlives the worktree, and promoted to `testdata/task_notification_v2.1.259.json` when
`fixtureWorthy`. Fields, beyond the provenance block every sibling carries (ticket, claude_version,
captured_at, is_capture, model, spawn_shape, spawn_shape_delta, env_delta, workdir, prompt, redaction,
redaction_rationale, credential_scan_applied/skipped, limitations, cap accounting):

| Field | AC | What it holds |
|---|---|---|
| `frames` | 1 | every captured `system` line of the four background-task subtypes, payload via `dropcapMakeEntry` |
| `line_type_census`, `tool_calls`, `tool_result_errors`, `undecoded_lines` | 1 | the whole turn, content-free |
| `notification_frame_count` | 1, 4 | the quarry's own count; the only non-vacuity key |
| `key_presence` | 2 | one entry per companion subtype: fired, line count, how many lines carried `ambient`, how many carried `skip_transcript` |
| `observed_notification_keys`, `documented_notification_keys`, and the two set differences | — | the SDK comparison, as a report |
| `foreground_call_observed`, `background_task_observed`, `held_seconds`, `turn_seconds`, `terminated_on` | 4 | the staging measurement behind `stagingVerdict` |
| `fixture_staged`, `fixture_stage_detail` | 3 | whether `git add` ran and what it said |

**Turn sequence.** Register the record-writing cleanup first so a later fatal still leaves evidence;
`t.Setenv` the bash timeout before the runner so the child inherits it; create the FIFO before the
runner so it exists when claude reads the prompt and so its release cleanup runs after the runner's
cancel. Then: wait for the rendezvous (claude's `cat` opening the FIFO) or the turn ending; poll
`recorder.snapshot()` for a `system/task_started` line, bounded; release the FIFO; poll for a
`system/task_notification` line, bounded; then wait for `result` or the turn budget.

Polling `snapshot()` rather than teaching `dropcapRecorder` a second signal channel is deliberate: the
recorder is shared with every probe in the package, and a per-subtype channel on it would be a fork by
another name.

**Contracts** (signatures only; bodies are Phase B):

- `tncapKeys(raw []byte) (keys []string, ambient, skipTranscript bool)` — decodes one line into
  `map[string]json.RawMessage` and returns its sorted top-level key set plus membership of the two AC2
  keys. Presence means the key exists in the object. The booleans are derivable from `keys`, which is
  what stops either being invented.
- `tncapCollect(t, lines []dropcapCaptured, red *dropcapRedactor) []tncapFrame` — filters on type
  `system` and the four subtypes, never on the parser's verdict; the verdict rides along per frame as
  `events_emitted`.
- `tncapKeyPresence(frames []tncapFrame) []tncapKeyPresence` — AC2's table, one entry per companion
  subtype **whether or not it fired**, so a did-not-fire is a recorded measurement rather than an
  omission.
- `(*tncapRecord).stagingVerdict() string` — names which of three ways a zero-notification capture
  happened: the command never ran, it ran but was never backgrounded, or a background task existed and
  the FIFO was released and still nothing came. Only the third is evidence about the surface.
- `tncapUnredactedPathFields(frames []tncapFrame) []string` — the security pass's MUST FIX. For every
  `task_notification` frame it decodes the payload and walks every string VALUE at any depth, and
  returns the **field names** of any value still beginning with `/` after redaction. Field names are
  claude's vocabulary and appear in nobody's data; values are never returned, printed or recorded.
  This is the layer for `output_file`, a documented host path and a field class no record in this
  family has carried. The declared table already covers the temp `$HOME`, `os.TempDir()`, the workdir
  and the operator's real home, and the deny-scan's fixed needles fail the whole write closed on
  `/Users/`, `/home/`, `/var/folders/` and `/private/var/folders/` — but a path under a prefix none of
  those know (a bare `/tmp/<uid>/…`, say) would pass both. A refusal costs one live turn; a promotion
  costs a public leak.
- `(*tncapRecord).fixtureWorthy() (reason string, ok bool)` — outcome is `fired`,
  `notification_frame_count > 0`, `claude_version` matches the pinned release, every frame is
  `json-string` encoded, and `unredacted_path_fields` is empty. **Keyed on the quarry alone**: a
  companion subtype that did not fire is AC2 data, never a refusal.
- `tncapStageFixture() (staged bool, detail string)` — `exec.Command("git", "add", "--", <fixture>)`,
  fixed argv, no shell, combined output redacted before it reaches the record. Best-effort and never
  fatal: git being unavailable must not throw away a good capture.

**Offline self-checks** (they run wherever the tag is set, with no claude and no credentials):

- `fixtureWorthy` refuses every bad capture — a table whose arms are: fired-and-good, bare version
  string, did-not-fire, instrument-broken, zero notification frames, a different claude release, an
  unreadable version, an empty version, a base64 frame the reader cannot decode. Plus the arm that
  matters most here: **a record whose companion subtypes never fired is still promoted.**
- `stagingVerdict` separates rig failure from finding — four arms, only the last reading as a surface
  finding.
- `tncapKeys` reports presence from the line's own bytes — a payload carrying both keys, one carrying
  neither, one carrying `ambient` only, one that does not decode.
- `tncapUnredactedPathFields` names the field and only the field — a frame whose `output_file` is a
  bare host path is refused naming `output_file` and nothing else; one whose `output_file` is already
  a `$TEMP_HOME/…` replacement is promoted; a nested path inside `usage` is caught at depth; a frame
  list holding only companion subtypes contributes nothing, so the sweep is scoped to the quarry.
- `tncapKeyPresence` records a did-not-fire — an empty frame list yields three entries, all
  `fired:false`, rather than an empty table.

### 2. `internal/streamsup/task_notification_capture_test.go` — the AC5 reader

No build tag, so it runs inside `make check`. It reads across by relative path into
`../e2e/realclaude/testdata/`, which is what keeps the measurement out from behind the opt-in gate.
It is a **fourth** reader: its own path constant, no path parameter, every provenance check written
out rather than borrowed. None of the four can decode another's record shape.

- `taskNotificationCaptureVersion = "2.1.259"`, spliced into `taskNotificationCapturePath` so the
  filename cannot drift from the version the reader enforces. The producing side refuses to write
  under a mismatched name, so a claude upgrade is a loud instruction to re-capture and repin.
- `taskNotificationPinnedKeys []string` — the measurement this ticket commits: the top-level key set
  the `task_notification` line arrived with. **It ships EMPTY**, because the fixture cannot exist until
  `make e2e-realclaude` runs on an authenticated machine, which happens after verification. Filling it
  is the commit that lands the fixture.
- `taskNotificationReaderGate(fixtureExists, pinFilled bool) (action, reason string)` — pure, so all
  four quadrants are provable on the leg where the reader itself can assert nothing. Skip only when
  neither exists. A pin whose bytes are gone and bytes with nothing pinning them both fatal.
- `TestRealClaudeTaskNotificationCaptureKeysArePinned` — gate, then `is_capture`, then the version
  token, then: for every frame whose subtype is `task_notification`, re-derive the key set from the
  frame's **own payload bytes** rather than from the record's `observed_notification_keys` label, and
  compare the union against the pin. Zero notification frames fatals. Re-deriving is what keeps this a
  measurement rather than an agreement with a label the probe wrote.
- `TestTaskNotificationReaderGateHasExactlyOneLegalSkip` — the four quadrants, offline, parallel.

## Concurrency model

Inherited from the two probes this copies. `dropcapRecorder` is mutex-guarded because `os/exec` drives
`Stdout` from its own copier goroutine while the test goroutine polls `snapshot()`. `tpcapHoldFIFO`
runs one goroutine parked in `open(O_WRONLY)`; its `release` is idempotent, bounded by
`probeFIFOReleaseDeadline`, registered in `t.Cleanup` as well as called mid-test, and reports the open
error rather than swallowing it. The runner goroutine exits on context cancel, awaited in a cleanup
registered after the FIFO's so it runs before the release. No new goroutine is introduced.

## Error handling

Nothing claude does is fatal except producing no `task_notification` payload, which AC4 requires to
redden. Three record outcomes: `fired`, `did-not-fire`, `instrument-broken`. The record is written by a
cleanup registered before anything can fail, so a structural fatal still leaves evidence on disk.

The deny-scan is fail-closed and inherited whole: on a hit **nothing** is written, not the record and
not the fixture, and the message names the class only. `git add` failing is recorded, never fatal.

## Testing strategy

`go test -race ./internal/streamsup/...` covers the AC5 reader and its offline quadrant table inside
`make check`. `go vet ./...` and `go build ./cmd/pyry` for the rest. The probe compiles only under
`-tags e2e_realclaude`; its offline self-checks run there with no credentials, and the live capture is
the dispatcher's gate to run — no agent session on this machine can sign claude in, so a local probe
skips at the credential check and proves nothing. Compilation of the tagged package is verified with
`go vet -tags e2e_realclaude ./internal/e2e/realclaude/`.

The reader skips on this branch, on its one legal quadrant. That is the state the gate exists to
sequence, not a hole.

## Open questions

1. **Does the released FIFO actually fire `task_notification` at 2.1.259?** The `tool_progress`
   record's census says the subtype reached this surface in a turn whose FIFO was held then released,
   which is the strongest available evidence, but that turn's timeout was 180 s rather than 5 s. If
   the live gate comes back with `background_task_observed:true` and zero notification frames,
   `stagingVerdict`'s third arm says so explicitly and the finding routes back rather than being
   papered over by loosening the promotion rule.
2. **Does `git add` survive the dispatcher's gate?** It stages into whatever worktree the gate runs
   from, and a detached one is discarded. The durable path is the artifact-dir record plus the
   reader's fatal-on-unpinned quadrant; `fixture_staged` records which happened. Resolved in Phase B
   by making the promotion log print the observed key list verbatim, so filling the pin needs no
   re-read of the file.

## Security review

**Verdict:** PASS (after one MUST FIX, folded into the Design above before this plan was committed)

**Findings:**

- **[Trust boundaries] MUST FIX — addressed in the plan above.** The boundary is claude's stdout →
  `dropcapRecorder` → `tncapRecord` → a committed public fixture → possibly a public issue comment,
  and it is the ticket's whole subject. The two inherited mechanisms are of genuinely different
  fabric: `dropcapRedactor`, a declared substitution table applied to every string entering the
  record, and `dropcapScanner`, a fail-closed deny-scan over the marshalled record that writes nothing
  on a hit. The first draft of this plan said both apply unchanged and stopped there, which
  under-covers the one thing the ticket flags: `output_file` is a documented path on the operator's
  host and a field class no record in this family has carried. The table covers the temp `$HOME`,
  `os.TempDir()`, the workdir and the operator's real home; the deny-scan's fixed needles fail the
  whole write closed on `/Users/`, `/home/`, `/var/folders/` and `/private/var/folders/`. A path under
  a prefix none of those know still passes both. `tncapUnredactedPathFields` is the added layer, in
  `fixtureWorthy` rather than in the shared scanner: extending `dropcapFixedNeedles` would change
  every sibling probe's verdict and could redden the already-committed captures that re-scan
  themselves offline.
- **[Tokens] No findings, and one ordering constraint restated because getting it wrong is silent.**
  Nothing is minted. `newDropcapScanner` reads `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` via
  `os.Getenv` **as deny needles only** — never stored in a field, never logged, never written.
  `WithWorktreeAuthenticated` is what re-pins them into this process's environment, so the scanner
  must be built after it; built first, the needle is empty, `scan` reports it as not applied, and the
  credential net is off while every message reads green. `credential_scan_skipped` ships in the record
  so an off arm stays visible after the fact.
- **[File operations] No findings.** Three writes, none on a caller- or claude-controlled path: the
  record at `0600` into an `os.MkdirTemp("")` directory, the fixture at `0600` at a compile-time
  constant relative path, and the FIFO at `0600` via `syscall.Mkfifo` under a workdir joined from a
  constant basename. No traversal surface, so no canonicalisation to get wrong and no `O_NOFOLLOW` to
  add. The `os.Stat`-then-write on the fixture is check-then-use, but the checked property is "has
  this capture already been taken", not a security property: losing the race costs one wasted live
  turn. The fixture write is not atomic; a partial file is caught by the reader's `is_capture` and
  JSON decode, and by human review of the commit, before it can pin anything.
- **[Subprocess] No findings.** Two spawns. `streamsup.New` runs claude through production's own path,
  unchanged. `exec.Command("git", "add", "--", <fixture>)` has a fixed argv and no `sh -c`; its single
  argument is a compile-time constant and `--` stops it being read as a flag, so nothing claude emits
  can reach it. Its combined output goes through `red.str` before entering the record, because git
  prints repository paths on failure. It runs only after `fixtureWorthy` passes, i.e. only after the
  deny-scan has already cleared the same bytes. `finOfflineExecBans` is an opt-in per-file table and
  this file is absent from it, correctly: this file's subject is a live claude turn.
- **[Cryptographic primitives] Not applicable, by a design decision worth naming rather than
  skipping.** The nonce is `time.Now().UnixNano()` and is a cache-buster, not security randomness —
  `bgIdlePrompt`'s doc says so and says it must not be "upgraded" to `crypto/rand`, which is the
  plausible-looking wrong change here. The session id is a fixed literal in a per-test temp `$HOME`.
- **[Network & I/O] No findings.** No sockets. The one unbounded input is claude's stdout, capped by
  `dropcapRecorder` at 4 MiB on the partial accumulator and 8 MiB in total, past which WHOLE lines are
  dropped and counted rather than truncated, with draining continuing past both because an unconsumed
  stdout would block claude. Every wait in the turn sequence is bounded: rendezvous, task-started
  poll, hold, notification poll, turn budget, run exit.
- **[Error messages and logs] No findings, and one deliberate allowance.** On a deny-scan hit the
  fatal names the class only and nothing is written; every `t.Logf` runs through `red.str`; the AC4
  fatal prints counts, indices and censuses; the new refusal prints field names. The allowance: the
  promotion log prints the observed `task_notification` key list verbatim, so the operator can fill
  `taskNotificationPinnedKeys` without re-reading the record. Keys are claude's vocabulary and appear
  in no operator's data — the same distinction `capturedStrings` draws when it sweeps values and
  excludes keys.
- **[Concurrency] No findings, and three `t.Cleanup` orderings are load-bearing.** LIFO means the
  record-writing cleanup must be registered first in the body so it runs last; the FIFO hold must be
  registered before the runner so its release runs after the runner's cancel; and the scanner must be
  built after the auth helper. Each wrong ordering fails silently — a green run with no evidence, a
  `cat` that never sees EOF, or a disabled credential net. One mutex, inside `dropcapRecorder`, never
  held across a call out. No new goroutine: the runner's exits on cancel and is awaited, and
  `tpcapHoldFIFO`'s is unparked by an idempotent, deadline-bounded `release`.
- **[Threat model] Not applicable, and the reason is the boundary.** Nothing crosses the noise
  transport or the control socket, so `docs/protocol-mobile.md` § Security model is not engaged. The
  one live threat is the one the ticket states and the first finding addresses: a public artefact
  carrying claude's stdout verbatim.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-09

## Revisions

### 2026-09-09 — rework leg 1, from the verifier's findings on PR #2294

Three changes to the design above, two of them load-bearing. Each names the finding that drove it.

**1. The quarry wait no longer treats `result` as terminal** (MUST FIX, `tncapAwaitSubtype`).

The Design's turn sequence said "poll for a `system/task_notification` line, bounded" and the
implementation reused the companion wait, which returns as soon as `recorder.resultSeen` closes. That
is right for a line the assistant emits inside its own turn and wrong for this ticket's quarry:
`task_notification` fires when a **background** task terminates, and a background task by construction
outlives the turn that started it. `resultSeen` is closed via `sync.Once`, so once fired the arm is
permanently ready and the whole `tncapNotificationWait` collapsed to a single `snapshot()`.

The plan should have caught this, and the evidence to catch it with was already in the Files read
list. `dropped_lines_v2.1.220.json` is this probe's own staging and it records `terminated_on:
"result"` with a process still holding the FIFO's read end at turn end — so on this exact staging
claude emits `result` while the backgrounded `cat` is still alive, and the quarry can only arrive
afterwards. The consequence was not a missed line but a **false verdict**: `stagingVerdict`'s third arm
would state that no line arrived "in the 90s that followed" when the 90 s were never spent, reporting
a rig failure as a finding about pyry's surface — the exact confusion that arm exists to prevent.

New contract: `tncapAwaitSubtype` takes a `resultEndsWait bool`, spelled at both call sites through
the named constants `tncapResultEndsWait` and `tncapResultIsNotTheEnd`. Phase 2 keeps the terminal
behaviour, because `task_started` cannot fire after the turn that would have backgrounded the call has
finished. Phase 4 runs to its own deadline.

**Budget, chosen rather than inherited.** The three phase waits now genuinely sum to 280 s, against a
`tncapTurnBudget` that was 4 minutes. Under the old number the phases could outlast the budget, and the
final `select` would then find both arms ready and pick `terminated_on` at random. The budget is
6 minutes, and `TestTncapBudgetOutlastsItsPhases` pins the inequality offline so the next edit to a
phase wait cannot quietly re-create the overlap. The final select also checks the already-closed
`resultSeen` non-blocking first, so the arm is chosen rather than raced.

**2. The deny-scan now covers the bytes that are actually written** (MUST FIX, `tncapWriteRecord`), and
the Security review's `[Subprocess]` finding above is wrong where it says otherwise.

That finding reasons that git's combined output "goes through `red.str` before entering the record" and
that it "runs only after `fixtureWorthy` passes, i.e. only after the deny-scan has already cleared the
same bytes." **The last clause is false, and it is the clause the design leaned on.** They are not the
same bytes: the scan cleared a blob marshalled before `FixtureStageDetail` existed, and the record was
then re-marshalled and that second blob was written to both the artifact record and the committed
fixture. `red.str` is no backstop here — `dropcapRedactor`'s declared table covers the temp `$HOME`,
the artifact dir, the workdir, the FIFO and the session id, and not the repository, so a `git add`
losing the index lock prints `/Users/<operator>/…/.git/index.lock` and the redactor passes it through
unchanged. Read the `[Subprocess]` finding as amended by this paragraph: the argv reasoning stands, the
"already cleared the same bytes" reasoning does not.

The design is now that **every blob written is deny-scanned as itself**. `tncapSeal` marshals the
record as it stands, scans that blob plus the decoded bytes of every base64 frame payload, and returns
the classes hit; `tncapWriteRecord`'s `seal` closure owns the fail-closed response, and every
`os.WriteFile` takes a `seal(...)` result. It is split out as a function rather than left inline so the
property can be asserted with no live turn and no write near the repo, which is what
`TestTncapSealCatchesWhatEntersTheRecordAfterTheFirstScan` does — including the index-lock string
verbatim. A mutant that scans a blob marshalled before the staging fields entered reddens it.

**Ordering, found while making the above true.** `tncapStageFixture` ran **before** the fixture was
written, so `git add` was staging a path that did not exist yet. That fails with "pathspec did not
match any files", and the fixture being absent is precisely the first-capture case this probe arms
itself for — so AC3 would have failed on every run that mattered. Staging now runs after the fixture
write. The consequence is that the two files deliberately differ by two fields: the **fixture** carries
a `fixture_stage_detail` saying where its staging outcome lives, because a file cannot truthfully
record whether it was staged before it existed, and the **artifact-dir record** is rewritten after
staging and is AC3's evidence. That rewrite is sealed again, which is what puts git's output through
the scan.

**3. `tncapFrame.SkipTranscriptResent` renamed to `SkipTranscriptPresent`** (SHOULD FIX). Every use
meant *present*; the JSON tag was already `skip_transcript_present`, so the record shape is unchanged.

**Unchanged by this leg:** the `## Security review` verdict stands as PASS with the `[Subprocess]`
amendment above folded in — the finding's conclusion (no injection surface, fixed argv, nothing claude
emits reaches it) survives; only its claim about scan coverage was wrong, and that gap is now closed in
code rather than in reasoning. `streamLine` is still untouched, no production file changed, and the
streamsup reader and its four-quadrant gate are exactly as designed, pin still honestly empty.

**Open question 1 is unaffected but better instrumented.** Whether a released FIFO fires
`task_notification` at 2.1.259 is still the live gate's to answer. The difference is that the answer is
now trustworthy: before this leg a zero-frame result could not distinguish "the surface does not emit
it" from "the rig never waited", and `stagingVerdict`'s third arm asserted the former either way.
