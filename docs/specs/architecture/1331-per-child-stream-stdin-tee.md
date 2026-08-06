# #1331 — Attribute the stream stdin tee per child so M4 can name which child received the turn

**Size:** S (PO's `size:s` re-verified below). **Tier:** test-only — one test-tier binary
(`fakeclaude`), one e2e spec, one new pure unit test. No production package is touched.

## Files to read first

Turn-1 reading list. Each entry says what to extract; read the range, not the whole file.

| Path + range | What to extract |
|---|---|
| `internal/e2e/internal/fakeclaude/main.go:544-608` | `main()`'s stream branch. The tee at `:566-574` is the **single writer-side edit site**. The comment at `:557-565` carries the clause this ticket retires — "so the bootstrap child and the fresh post-rotation child both accumulate into one file". |
| `internal/e2e/internal/fakeclaude/main.go:1132-1170` | `argvSessionID(args)` + its stem-guard rationale (`:1146-1158`). The new writer-side helper wraps it; the guard is why an argv value may be spliced into a filename at all. |
| `internal/e2e/internal/fakeclaude/main.go:1-22` | Package header env table; the `PYRY_FAKE_CLAUDE_STDIN_LOG` entry at `:14-22` is the one whose stream-mode clause changes. |
| `internal/e2e/internal/fakeclaude/main.go:229-263` | `PYRY_FAKE_CLAUDE_SESSION_ID_FROM_ARGV`'s doc. Two things: the "argv is the one per-child channel that already carries the id" argument this design reuses, **and** that this var is INERT on the stream path — do not set it (see Constraints). |
| `internal/e2e/internal/fakeclaude/main.go:264-294` | `PYRY_FAKE_CLAUDE_JSONL_TRIGGER_DIR` (#1195). The **shipped precedent**: a shared path could not serve a multi-child tree, and "keying the path on the child's own stem makes ownership structural". This ticket is that same move applied to the stdin tee. |
| `internal/e2e/internal/fakeclaude/argv_session_id_test.go` (whole file, 90 lines) | The untagged pure table-test idiom (`// Intentionally UNTAGGED … so the standard go test gate exercises the parser`) the new writer-side test copies, and the stem-guard rows it must **not** duplicate. |
| `internal/e2e/relay_v2_stream_new_session_test.go:23-72` | File header: the milestone sketch (M4's line is `:47`) and the drain-divergence analysis (`:62-72`). Both say "the stdin log", singular. |
| `internal/e2e/relay_v2_stream_new_session_test.go:109-129` | The tee's env wiring. `:117-120` states the shared-file contract being replaced; `:121` is the `stdinLog` variable that becomes a stem. |
| `internal/e2e/relay_v2_stream_new_session_test.go:334-400` | M4's ack #2 wait, the ack-time byte-count probe (`:371-384`) and the daemon-log byte offset (`:386-400`). Extract the **same-instrument-at-both-ends** doctrine (`:376-379`) — it survives this change verbatim, re-pointed at a per-child file. |
| `internal/e2e/relay_v2_stream_new_session_test.go:402-494` | M4's poll and its whole failure record. Two binding rules live here: nothing on the failure path may call a `t.Fatal*`-ing helper (`:446-448`), and a broken instrument must render as a sentinel, never as a measurement (`:449-460`, `:424-426`). |
| `internal/e2e/relay_v2_stream_new_session_test.go:496-546` | The AC-1 instrument guard. `:507-511` explains why it only runs on a green M4 — the fact AC-4 turns on. `:538-544` is the message AC-4 amends. |
| `internal/e2e/relay_v2_stream_new_session_test.go:548-564` | M5: the non-vacuity guard (which today matches turn #1 — the **outgoing** child's bytes) and the `/clear` check. |
| `internal/e2e/relay_v2_stream_new_session_test.go:680-836` | `TestDaemonLogWindow`. The in-file table-test idiom the AC-3 fixture copies, including its "CONTRACT check, not a manufactured failing scenario" posture (`:680-688`, `:781-787`). **Untouched by this ticket** — `daemonLogWindow` and its test do not move. |
| `internal/streamsup/runner.go:782-804` | `buildArgs`: every spawn ends in `--session-id <id>` (first run) or `--resume <id>` (respawn). This is why the argv discriminator is **total** on this path, and why a crash-respawn of the same session appends to the same per-child file. |
| `internal/streamsup/runner.go:484-510` | `RestartFresh` re-arms first-run form, so the post-rotation spawn carries `--session-id <newID>` — the id M2 captures as `post.ID`. |
| `internal/e2e/harness.go:368-467` | `StartStreamInteractiveWithRelay`: `extraEnv` flows verbatim into the daemon's process env, inherited identically by both children. **Not modified by this ticket.** |
| `docs/knowledge/features/fakeclaude-binary.md:667-690` | § "Stream-path stdin tee" — the contract that goes stale. A **documentation-phase** follow-up, not a developer AC (see Out of scope). |
| `docs/knowledge/codebase/1330.md` | The sibling's scope note: the ~21 s M4 stall is **#1298, still open**, and not this ticket's. Read before interpreting any red run. |

## Context

M4 asserts "the fresh post-rotation child received the subsequent turn" by polling one
stdin log for `e2e-1137-user:two`. Both children inherit the same
`PYRY_FAKE_CLAUDE_STDIN_LOG` from the daemon's process env and both open it `O_APPEND`,
deliberately (`fakeclaude/main.go:562-565`). A needle in that file proves *some* child
received the turn; it cannot say which. When the outgoing child gets it, M4 goes green on
the wrong evidence and the run then surfaces as an AC-1 failure whose two named readings
are both wrong — a broken-instrument report about a working instrument.

The fix is the move #1195 already made for the JSONL trigger: stop sharing one path, key
the path on the child's own spawn id. The id is already on each child's argv
(`--session-id <id>` / `--resume <id>`), `argvSessionID` already parses it, and its stem
guard already refuses anything that could steer a `filepath.Join`.

This is the **evidence** half of the flake. The production race is #1330 (closed
2026-08-06, `dac5779`); this ticket does not fix a race, it makes the test able to see
one. The remaining M4 stall (#1298) is open and out of scope.

## Design

### 1. Attribution key: the session id, not the process

Each per-child log is keyed by the **session id that spawn was pinned to**, not by pid or
spawn ordinal. Consequences, both intended:

- A crash-respawn of the *same* session (`--resume <sameID>`, `buildArgs` `:803`) re-opens
  and appends to the same file. Correct: M4's claim is about the child *the daemon spawned
  with `--session-id post.ID`*, which is a session, not a process.
- The bootstrap child (`initialUUID`) and the post-rotation child (`post.ID`) get distinct
  files by construction, because `RestartFresh` rotates the id before the respawn.
- A *second* rotation (M2's loop re-sends `new_session` every ~250 ms and this file already
  anticipates double actuation, `:251-257`) produces a **third** file under a third id. M4
  then goes red — correctly — and the existing `regNowID` re-read (`:437-460`) plus the new
  per-child inventory in the failure record name it on sight.

`O_APPEND | O_CREATE | O_WRONLY, 0o600` and the per-write `syncWriter` are unchanged: append
is still load-bearing (same-session respawn), and the fsync is still what makes a sibling
process's `os.ReadFile` see bytes promptly on APFS.

### 2. Path shape

`PYRY_FAKE_CLAUDE_STDIN_LOG` keeps its name and is **re-interpreted in stream mode only**:
its value is a path *stem*, and the child appends `"." + <its session id>`.

```
stem:                 <tmp>/fakeclaude-stdin
bootstrap child:      <tmp>/fakeclaude-stdin.11111111-1111-4111-8111-111111111111
post-rotation child:  <tmp>/fakeclaude-stdin.<post.ID>
```

Decisions, with the alternative each one beat:

- **Same env var, mode-scoped meaning** (not a second `…_STREAM_STDIN_LOG`). The PTY tee
  (`startStdinReader`, `:1207`) reads the same const and is **not touched** — its 7+
  consumers keep a single file, because they never enter the stream branch (the two modes
  are mutually exclusive at `main()`'s first check). Two near-identically-named vars would
  invite setting the wrong one, and the failure of setting the wrong one is *silence* —
  the exact failure mode this ticket exists to kill. One knob, documented per mode, matches
  how every other fakeclaude rider is scoped.
- **Suffix on a stem** (not "the value is now a directory"). A directory would change the
  value's *kind* between modes and add a mkdir contract; a stem keeps it a path in the same
  directory and makes the bare `<stem>` file's absence meaningful (if it exists, the PTY
  path wrote it).
- **Per-child files** (not markers interleaved into one file). `io.TeeReader` writes exactly
  the bytes each `Read` returns, so a turn envelope can span read boundaries; an inline
  marker could split a needle across one, and both M4's and M5's checks are contiguous
  substring checks. Per-child files remove the hazard by construction — each file is written
  by exactly one child's single stdin-read loop, so every needle stays contiguous.
- **Unattributed fallback, not `fatalf`.** When argv carries no usable id the child writes
  `<stem>.unattributed`. Unreachable on today's spawn path (`buildArgs` always appends an id
  flag), so this is a *rendering* choice, not a defense: it keeps the child alive so the
  other milestones still produce evidence, and the anomaly shows up by name in M5's
  per-child inventory. It can never manufacture a green: M4 reads only `post.ID`'s file, and
  M5 spans every file including this one.

### 3. Writer side — `internal/e2e/internal/fakeclaude/main.go`

One new const and one new pure helper, placed beside `argvSessionID` (`:1132`):

```go
// unattributedStdinLogStem names the per-child stdin log of a stream child whose
// argv carried no usable session id. Unreachable via streamsup (buildArgs always
// appends --session-id/--resume); it exists so such a child is named, not silent.
const unattributedStdinLogStem = "unattributed"

// streamStdinLogPath returns the per-child path the stream tee appends to:
// stem + "." + this spawn's session id, or the unattributed stem when argv carries
// none. Pure over (stem, args); never reads the environment.
func streamStdinLogPath(stem string, args []string) string
```

`argvSessionID`'s stem guard is what makes the concatenation safe — a value carrying `/`,
`\` or `.` is refused there, so the appended component can never introduce a separator or a
traversal. Say that in the helper's doc; do not re-implement the guard.

The tee at `:568` opens `streamStdinLogPath(logPath, os.Args[1:])` instead of `logPath`.
Everything else in that block — the open flags, `syncWriter`, `fatalf` on open failure, the
`io.TeeReader` wrap, the hold/approve/bogus riders below it — is unchanged.

Two comments must move with the code, because both currently assert the retired contract:

- `:557-565` — the "so the bootstrap child and the fresh post-rotation child both accumulate
  into one file" clause is now false. Replace with the per-child rule + the #1195 precedent
  it mirrors + why the *session id* is the key (§1).
- `:14-22` — the header env entry's "In stream-json mode the same var tees the stream
  child's stdin to the file" becomes "…treats the value as a stem and tees each child's
  stdin to `<stem>.<its session id>`".

### 4. Reader side — `internal/e2e/relay_v2_stream_new_session_test.go`

Three helpers. **All three are fatal-free** — no `*testing.T`, no `t.Fatal*` — because M4's
failure record calls them while building its message, and the rule at `:446-448` is that a
fatal fired there means the message never prints.

```go
// childStdinLog mirrors fakeclaude's per-child derivation (stem + "." + sessionID).
// Duplicated rather than shared: fakeclaude is package main and unimportable — the
// same posture msgqueueRetryWarn takes. Drift fails LOUD (M4 red), never silent.
func childStdinLog(stem, sessionID string) string

// childReceivedTurn reports whether the child pinned to sessionID received needle,
// reading ONLY that child's log. Returns the bytes and the read error so an absent
// or unreadable file renders as itself and never as "the file was empty".
func childReceivedTurn(stem, sessionID string, needle []byte) (bool, []byte, error)

// childStdinLogs returns every per-child log under stem, keyed by session id.
// os.ReadDir + a `filepath.Base(stem)+"."` prefix filter, NOT filepath.Glob:
// a stem containing a glob metacharacter would make Glob's ErrBadPattern render
// as "no children", i.e. an instrument failure reported as a measurement.
// Per-file read errors are errors.Join'd into err while the other files still land
// in the map.
func childStdinLogs(stem string) (map[string][]byte, error)
```

**Call-site changes** (all inside the one test function):

- `:121` — `stdinLog` becomes `stdinLogStem`, basename `fakeclaude-stdin` (no `.log`; the
  per-child files carry the id where the extension used to be). The comment at `:117-120`
  states the new per-child contract.
- `:384` — the ack-time probe reads `childStdinLog(stem, post.ID)`. Keep the
  same-instrument-at-both-ends discipline verbatim (`os.ReadFile` + `len` at both ends, both
  on the *same per-child path*). Note in the comment that an ENOENT here is now
  *informative*, not noise: the fresh child opens its log before reading stdin, so an absent
  file at ack #2 means it had not started yet.
- `:427-436` — the poll calls `childReceivedTurn(stem, post.ID, needle)`.
- `:437-493` — the failure record. Rewrite three things and leave the rest byte-identical:
  1. The headline claim: the **post-rotation** child (`--session-id post.ID`) never received
     turn #2.
  2. The byte-count pair becomes a per-child inventory rendered from `childStdinLogs` —
     `id=<n> bytes`, sorted by id, with the post-rotation id and the outgoing id marked. The
     decisive new reading: *bytes in the outgoing child's log and none in the
     post-rotation child's ⟹ the turn went to the wrong child* (the #1330-class defect this
     ticket makes visible). Keep the read error rendered beside each count.
  3. Nothing else. The `regNowID` sentinel, the `daemonLogWindow` attachment, the
     retry-Warns / pending-holds reading paragraphs and their constants stay **verbatim** —
     they are about delivery, not attribution.
- `:537-545` — AC-1's message gains the third reading, worded as AC-4 mandates: *the fresh
  child received turn #2 but never echoed it, so the drain had no post-rotation event to
  drop*. It must **not** name "the fresh child never received the turn": M4 now excludes
  that before AC-1 can fire (`:507-511` — the guard runs only on a green M4). The existing
  two readings stay.
- `:553-563` — M5 iterates `childStdinLogs(stem)`. Non-vacuity: **at least one** child's
  bytes carry `echoNeedleOne` or `"type":"user"`, and an **empty map is a failure** (no
  child log at all ⟹ the tee never wired). `/clear` check: no child's bytes contain
  `/clear`, and the failure names the id whose file did. Both span every child by
  construction, which is what keeps a `/clear` typed at the *outgoing* child red (AC-5).
- `:23-72` — the header. M4's sketch line (`:47`) and the divergence analysis's "why M4
  asserts the fresh child's STDIN" (`:71-72`) both say "the stdin log", singular. One
  sentence each.

### 5. What this design does not change

`daemonLogWindow` + `TestDaemonLogWindow`, the three `msgqueue*` / `daemonDebugLevel`
constants, `harness.go`, the PTY tee (`startStdinReader`), `runStreamJSON`'s signature, and
the six sibling stream specs (none of which sets `PYRY_FAKE_CLAUDE_STDIN_LOG`). Verified:
of the 7 `StartStreamInteractiveWithRelay` callers only this test sets the var, and the two
other `PYRY_FAKE_CLAUDE_STDIN_LOG` setters (`respawn_after_eviction_test.go:251`,
`per_conversation_eviction_test.go:282`) set no `STREAM_JSON` and so stay on the PTY path.

## Concurrency model

No goroutines are added. The relevant properties:

- **One writer per file.** Each per-child log is written only by that child's `io.TeeReader`,
  driven by `runStreamJSON`'s single serial stdin read loop. Contiguity of a needle within a
  file therefore holds by construction — the byte-interleaving hazard that rules out a
  shared file with inline markers does not arise.
- **Cross-process visibility** is unchanged: `syncWriter` fsyncs per write, which is what
  makes the test's polling `os.ReadFile` observe bytes promptly.
- **Reader concurrency.** `childStdinLogs` reads a directory that children are appending to
  concurrently. A short read is possible and harmless: every check is "does this contain the
  needle", monotone under appends, and the poll re-reads. Do not add locking or retries.

## Error handling

| Failure | Behaviour | Why |
|---|---|---|
| Stream child's log open fails | `fatalf` (unchanged) | Same as today; a tee that cannot open is a harness misconfiguration. |
| Argv carries no usable session id | Write `<stem>.unattributed` | Unreachable via `buildArgs`; named rather than silent, and cannot satisfy M4. |
| Post-rotation child's log absent at ack #2 | Render the read error beside the count | The fresh child had not yet started; an absent file must never render as "0 bytes". |
| Post-rotation child's log absent at expiry | M4 red, error rendered | Correct outcome: no evidence the attributed child received anything. |
| `childStdinLogs` ReadDir fails | Return the error; M4/M5 render it | An instrument failure must not read as "no children". |
| Per-file read fails inside `childStdinLogs` | `errors.Join` into err, other files still returned | Partial evidence beats none, but the gap is stated. |
| Reader/writer derivations drift | M4 goes red | Loud, never silently green: the needle can only appear in a file some child actually wrote. |

## Testing strategy

Both new tests run under `make check` (`make e2e` → `go test -tags e2e ./internal/e2e/...`,
which covers `internal/e2e/internal/fakeclaude`; the writer-side test is untagged so
`make test` runs it too).

**`TestStreamStdinLogPath`** — new file `internal/e2e/internal/fakeclaude/stdin_log_path_test.go`,
untagged, table-driven, mirroring `argv_session_id_test.go`. Scenarios (inputs → expected
path suffix), not a re-test of `argvSessionID`'s 17 rows:

- `--session-id <uuid>` → `<stem>.<uuid>`
- `--resume <uuid>` (the same-session respawn form) → `<stem>.<uuid>` — pins §1's
  "respawns of one session share one file"
- both flags present → the **last** wins, matching the transcript stem
- no id flag → `<stem>.unattributed`
- a guard-rejected value (`../escape`) → `<stem>.unattributed`, **and** the returned path's
  `filepath.Dir` still equals the stem's dir — the traversal did not survive into the path
- a stem containing a `.` (a realistic `…/fakeclaude-stdin.log` stem) → the id is still the
  final component; documents that the stem's own dots are harmless

**`TestChildStdinLogAttribution`** — in `relay_v2_stream_new_session_test.go` beside
`TestDaemonLogWindow`. This is AC-3's proof. It builds fixture files with a **hand-written
literal path shape** (`filepath.Join(dir, "fakeclaude-stdin."+id)`), never by calling
`childStdinLog` — a fixture written through the helper would pass under any derivation and
prove nothing.

M4-predicate rows (`childReceivedTurn(stem, postID, needle)`):

- **The AC-3 row:** only `<stem>.<outgoingID>` exists and it contains the turn-#2 needle →
  `false`, with a non-nil read error for the absent post file. *This is the row that goes
  red under today's shared-file instrument and green under the new one.*
- Control (the row that proves the fixture shape is reachable at all, per #1295/#1330): the
  same fixture **plus** `<stem>.<postID>` carrying the needle → `true`.
- `<stem>.<postID>` exists but is empty → `false`, **nil** error — an empty file and an
  absent one are different readings.
- Both children's files carry the needle → `true` (a second delivery to the outgoing child
  does not un-prove the post-rotation one).

M5-predicate rows (over `childStdinLogs`):

- `/clear` in the **outgoing** child's file only → reported, naming that id (AC-5's explicit
  case).
- `/clear` in the post-rotation child's file only → reported, naming that id.
- neither → not reported.
- non-vacuity: only the outgoing child's file carries a user turn → non-vacuity **holds**
  (the guard spans every child, so turn #1's bytes still discharge it).
- non-vacuity: no files at all → non-vacuity **fails** (an empty map is not a pass).
- an unrelated file in the same directory (no `<base>.` prefix) → not collected.

**What the fixture proves and what it does not.** It proves the attribution property: a
needle in the outgoing child's evidence cannot satisfy the post-rotation child's assertion.
It does not prove the writer and reader agree on the path shape — nothing offline can, since
the fake is a separate binary. That agreement is proven by a live green M4 plus M5's
non-vacuity, and a disagreement is loud (M4 red), never silent. State this in the test's doc
comment rather than letting a reader over-read the fixture.

**Do not** try to prove AC-3 by re-running the live suite. #1330 closed the race, so the
wrong-child delivery is no longer producible on demand; and per `docs/knowledge/codebase/1330.md`
a red M4 on this branch may be #1298's still-open stall, while a green suite is not evidence
either (#1330's review got 38/38 green on a pristine tree where the cited baseline was 4-in-34
red). Do not weaken or re-scope M4 to make a red run go green.

## Acceptance criteria (developer deliverables)

1. The stream tee writes each child's stdin to `<stem>.<session id>`, derived from that
   spawn's own argv via `argvSessionID`'s stem guard; the PTY tee is unchanged.
2. M4 asserts turn #2 reached the child spawned with `--session-id post.ID`, reading only
   that child's log. A turn delivered only to `initialUUID`'s child fails M4.
3. `TestStreamStdinLogPath` and `TestChildStdinLogAttribution` land as specified, including
   the AC-3 red row and its control.
4. M4's failure record renders the per-child inventory and names the wrong-child reading;
   AC-1's message gains the third reading and names neither "never received".
5. M5's `/clear` check and its non-vacuity guard span every per-child log, and an empty
   inventory fails non-vacuity.
6. `make check` passes. If M4 goes red, check `docs/knowledge/codebase/1330.md` before
   assuming it is this change.

## Out of scope

- `docs/knowledge/features/fakeclaude-binary.md:667-690` § "Stream-path stdin tee" describes
  the retired shared-file contract and must be rewritten — **by the documentation phase**,
  from this spec plus the merged diff. Not a developer AC (the developer's worktree mutates
  code, tests and this spec only).
- #1298's ~21 s M4 stall, and #1330's production race (closed).
- `startStdinReader` (the PTY tee) and its 7+ consumers.

## Open questions

- **Sentinel spelling.** `unattributed` is chosen for legibility in the per-child inventory.
  A production session id could in principle collide with it; ids are UUIDs on every path
  that reaches here, so no guard is specified. If the developer prefers a spelling that
  cannot be a UUID at all, that is a free choice — record it in the const's doc.
- **Where the M5 predicate's `/clear` scan lives.** Specified as an inline loop over
  `childStdinLogs`' map. If writing the AC-5 fixture rows against an inline loop turns
  awkward, extracting a fourth fatal-free helper (`childrenWithClear(map) []string`) is
  sanctioned — it does not change any contract above.

## Scope check

Recorded so the numbers are auditable rather than asserted:

- New files: **1** (`fakeclaude/stdin_log_path_test.go`). Limit 3. ✓
- Production source files (non-test, primary-language) created or modified: **1**
  (`fakeclaude/main.go`). Limit 5. ✓
- Projected total written LOC: **~400** (fakeclaude ~55, its test ~50, the e2e file
  ~260-300 including two table tests and the message rewrites). Limit ~600. ✓ Calibrated
  against the nearest analogues in these same files rather than estimated: #1137
  (`ea9fd55`, both files, 434 insertions), #1296 (`183de2f`, one pure helper + a 10-row
  table test + a message rewrite, 240 insertions), #1318 (`dfeef55`, 206 insertions).
- Consumer call sites needing simultaneous update: **1** writer + **~6** readers, all inside
  one test function. Limit 10. ✓ No interface or signature changes, so no cascade.
- New exported types/interfaces: **0**. Limit 5. ✓
- Acceptance criteria: **5** on the ticket. Limit 5. ✓
- Error/reject branches in a state machine: **0** — no state machine; the error table above
  is 7 rows of rendering choices, 4 of which are existing behaviour re-pointed at a new path.
- File-overlap check (2026-08-06): `git fetch origin --prune` then a scan of every
  `origin/feature/<n>` branch against this design's file list — **no overlaps**, and no open
  PRs. No `blockedBy` set.
