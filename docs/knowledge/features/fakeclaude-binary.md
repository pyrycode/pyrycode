# Fake-Claude Test Binary

`internal/e2e/internal/fakeclaude` is a test-only Go binary that stands in
for the real `claude` CLI inside e2e tests. It opens a `<uuid>.jsonl` file
under a configured sessions directory, polls a trigger file, and on first
appearance closes the original fd, opens a fresh `<newUUIDv4>.jsonl` in
the same directory, removes the trigger, and idles forever. Subsequent
triggers are ignored.

Phase: ticket #122 ships the binary in isolation. Ticket #123 wires it
into the e2e harness (`Harness.StartRotation`, `Harness.ClaudeSessionsDir`,
`ensureFakeClaudeBuilt`) — see
[e2e-harness.md § Rotation Primitive](e2e-harness.md). The rotation-watcher
driver test that exercises pyry's watcher against the binary is the slice
after that.

## What It Does

The binary mimics exactly the externally observable behaviour
`internal/sessions/rotation`'s watcher cares about: a tracked PID has one
JSONL fd open at any moment, and on `/clear` the PID closes the old fd
and opens a new one in the same directory. It does **not** mimic claude's
stdin/stdout protocol, conversation content, or any other surface — the
rotation watcher only observes the fd table and the directory.

The one exception is the opt-in **TUI mode** (`PYRY_FAKE_CLAUDE_TUI`, #603):
it emits exactly two of claude's TUI substrate glyphs so tui-driver's
`IsIdle`/`IsThinking` detection — and #594's `WaitReady → DeliverPrompt →
commit` contract — can confirm a turn against it. See
[§ TUI mode](#tui-mode-603). The earlier optional `STDIN_LOG` (#323) and
`ASSISTANT_TRIGGER` (#311) modes, the **JSONL-trigger mode**
(`PYRY_FAKE_CLAUDE_JSONL_TRIGGER`, #642) that appends captured claude-format
turn events to the live session JSONL, the **idle-trigger mode**
(`PYRY_FAKE_CLAUDE_IDLE_TRIGGER`, #792) that opens a controllable *busy → free*
window by withholding the startup idle glyph until a trigger fires, and the
**Esc-ends-turn mode** (`PYRY_FAKE_CLAUDE_ESC_ENDS_TURN`, #794) that watches
stdin for the remote interrupt's bare ESC and, on finding it, appends one canned
`end_turn` line so the Esc *causes* the turn to stop, and the **modal-clear-on-answer
mode** (`PYRY_FAKE_CLAUDE_MODAL_CLEAR_ON_ANSWER`, #793) that **extends** modal mode
(`PYRY_FAKE_CLAUDE_MODAL_TRIGGER`, #791 — the permission-prompt raiser): after the modal
is shown it clears it on the first post-modal stdin byte (the local `pyry attach` head's
answer keystroke) so tui-driver fires `EventKindPtyModalHidden` and the daemon's #706
**local** first-answer-wins arm resolves it, and the **clear-rotate mode**
(`PYRY_FAKE_CLAUDE_CLEAR_ROTATES`, #1004) that watches stdin for the `/clear`
slash-command bytes (the keystroke `supervisor.StartNewSession` types on a phone's
`new_session` frame) and, on the first match, rotates the live session JSONL once —
mirroring real claude's `/clear`-starts-a-new-session behaviour so the #1004
new_session e2e can observe the rotation on disk, likewise
extend the binary past pure rotation; see [§ Configuration](#configuration--env),
[§ JSONL-trigger mode](#jsonl-trigger-mode-642),
[§ Idle-trigger mode](#idle-trigger-mode-792),
[§ Esc-ends-turn mode](#esc-ends-turn-mode-794),
[§ Modal-clear-on-answer mode](#modal-clear-on-answer-mode-793), and
[§ Clear-rotate mode](#clear-rotate-mode-1004). Whenever the stdin reader is
active (TUI or `STDIN_LOG`), a delivered turn also triggers **on-turn
transcript growth** (#673): the live session JSONL grows by one inert line so
the daemon's #668 transcript-growth commit-confirm observes growth and acks
(otherwise it times out with `ErrTurnNotCommitted`); see
[§ On-turn transcript growth](#on-turn-transcript-growth-673).

| Step | Effect |
|---|---|
| Start | open `<dir>/<initialUUID>.jsonl` `O_WRONLY\|O_APPEND\|O_CREATE 0o600`, `WriteString("{}\n")`, `Sync()` |
| Idle | poll trigger file every 50ms |
| Trigger | `f.Close()` (OLD), mint `uuidV4()`, open `<dir>/<newU>.jsonl` (NEW), write+fsync, `os.Remove(trigger)`, set `rotated=true` |
| Idle (post-rotation) | poll continues; trigger reappearance ignored |
| Delivered turn (stdin bytes, TUI/`STDIN_LOG` only) | reader sets `turnPending`; main loop appends `{}\n`+fsync to current `f` (growth-confirm signal, #673) |
| SIGTERM | Go runtime default-terminates; OS auto-closes the open fd |

Strict close-OLD-before-open-NEW is **load-bearing**. The downstream
rotation-watcher test relies on the platform probe (`/proc/<pid>/fd` on
Linux, `lsof` on macOS) seeing exactly one path on the PID's fd table at
the instant the watcher's CREATE-driven probe runs. If the binary held
both fds open across the rotation, the probe could match either path and
the watcher's exact-match gate (`watcher.go:167`) would race.

## Configuration — env

**Required** (a missing or empty value prints `fakeclaude: missing env
<NAME>` to stderr and exits 1):

```
PYRY_FAKE_CLAUDE_SESSIONS_DIR  directory that must already exist
PYRY_FAKE_CLAUDE_INITIAL_UUID  stem for the first <uuid>.jsonl
PYRY_FAKE_CLAUDE_TRIGGER       path watched for the rotation signal
```

**Optional** (each unset by default; together they layer behaviour on top
of the bare rotation primitive):

```
PYRY_FAKE_CLAUDE_STDIN_LOG          append every stdin byte to this file,
                                    fsynced per read (#323; lets a sibling
                                    test process observe the prompt)
PYRY_FAKE_CLAUDE_ASSISTANT_TRIGGER  path watched; on appearance, write the
                                    file's bytes to stdout as a scripted
                                    assistant chunk (#311)
PYRY_FAKE_CLAUDE_JSONL_TRIGGER      path watched; on appearance, append the
                                    file's bytes (capped) verbatim to the
                                    live <uuid>.jsonl, fsync, remove the
                                    trigger (#642; see § JSONL-trigger mode)
PYRY_FAKE_CLAUDE_TUI                when non-empty, emit the idle/thinking
                                    glyphs (#603; see § TUI mode)
PYRY_FAKE_CLAUDE_IDLE_TRIGGER       path watched; start BUSY (no startup idle
                                    glyph, never the spinner) until it appears,
                                    then emit the idle glyph ONCE and remove the
                                    trigger — a controllable busy→free window
                                    (#792; see § Idle-trigger mode). Mutually
                                    exclusive with PYRY_FAKE_CLAUDE_TUI.
PYRY_FAKE_CLAUDE_ESC_ENDS_TURN      when non-empty, enter raw mode and watch stdin
                                    for a bare ESC (the remote interrupt keystroke,
                                    supervisor.SendEsc's lone 0x1b); on the first
                                    one append a canned assistant end_turn line to
                                    the live JSONL so the Esc CAUSES the turn to
                                    stop (#794; see § Esc-ends-turn mode). A flag,
                                    not a path. Coexists with PYRY_FAKE_CLAUDE_TUI.
PYRY_FAKE_CLAUDE_MODAL_CLEAR_ON_ANSWER  when non-empty AND MODAL_TRIGGER is set (a
                                    no-op otherwise), clear the permission modal on
                                    the first post-modal stdin byte (the local pyry
                                    attach head's answer keystroke) — write a
                                    modal-clearing screen once so tui-driver's class
                                    transitions Permission->Unknown and the daemon's
                                    #706 local arm resolves it (#793; see § Modal-
                                    clear-on-answer mode). A flag, not a path. EXTENDS
                                    modal mode; byte-identical when unset.
PYRY_FAKE_CLAUDE_CLEAR_ROTATES      when non-empty, watch stdin for the "/clear"
                                    slash-command bytes (supervisor.StartNewSession's
                                    ClearInputLine + TypePrompt keystroke) and, on the
                                    first match, rotate the live session JSONL once —
                                    same close-old/open-new as the file trigger
                                    (#1004; see § Clear-rotate mode). A flag, not a
                                    path. Shares the `rotated` one-shot gate with the
                                    file trigger, so the two rotation sources are
                                    mutually exclusive in practice.
PYRY_FAKE_CLAUDE_STREAM_JSON        when non-empty, skip the PTY/TUI surface
                                    entirely and speak line-delimited stream-json
                                    instead (#1140; see § Stream-json mode). Checked
                                    FIRST in main(), above every other env var read —
                                    structurally mutually exclusive with every mode
                                    above (if both are set, stream wins) and binds no
                                    sessions dir / transcript.
PYRY_FAKE_CLAUDE_STREAM_HOLD        path to a trigger file; when set, the stream-mode
                                    child blocks BEFORE consuming any stdin until the
                                    path appears, then proceeds normally (#1138; see
                                    § Stream-path startup hold). A no-op outside stream
                                    mode. Default-off; unset ⟹ byte-identical.
PYRY_FAKE_CLAUDE_SESSION_ID_FROM_ARGV  when non-empty, take the INITIAL <uuid>.jsonl
                                    stem from this spawn's own argv — the value
                                    after the last --session-id/--resume
                                    (argvSessionID) — instead of
                                    PYRY_FAKE_CLAUDE_INITIAL_UUID (#1195; see
                                    § Argv-derived stem mode). No flag found, or a
                                    value that fails the filename-stem guard, falls
                                    back to the env var. A flag, not a path.
PYRY_FAKE_CLAUDE_JSONL_TRIGGER_DIR  directory watched in parallel with
                                    PYRY_FAKE_CLAUDE_JSONL_TRIGGER for
                                    <dir>/<this child's own initial stem>.jsonl.trig
                                    (#1195; see § Per-child JSONL trigger mode).
                                    Computed once from the same stem
                                    SESSION_ID_FROM_ARGV resolves, so the two are
                                    typically set together, but neither requires
                                    the other.
PYRY_FAKE_CLAUDE_REJECT_ABSENT_RESUME  directory; when set, a stream-mode spawn
                                    whose winning id flag is --resume against an
                                    id with no <dir>/<id>.jsonl is refused like
                                    real claude (stderr + exit 1) instead of
                                    served (#1631; see § Reject-absent-resume
                                    mode). Stream mode only — checked inside the
                                    STREAM_JSON branch. Default off; unset is
                                    byte-identical.
```

env is the entire configuration surface, matching how the harness consumer
configures the child via `cmd.Env`. fakeclaude reads stdin only when
`STDIN_LOG`, `TUI`, `MODAL_TRIGGER`, `ESC_ENDS_TURN`, `MODAL_CLEAR_ON_ANSWER`,
`CLEAR_ROTATES`, or `STREAM_JSON` is set; otherwise it ignores stdin entirely.
`STREAM_JSON` never inspects `os.Args` either — the daemon's injected
`--input-format`/`--output-format`/`--verbose`/`--session-id`/`--resume` flags
are silently tolerated by construction, not parsed.

`SESSION_ID_FROM_ARGV` (#1195, § Argv-derived stem mode below) is the one
exception to "no positional args": when set, it is the sole mode that reads
`os.Args`, and only to extract the value following `--session-id`/`--resume` —
never to reject an unrecognised flag. When unset (every caller before #1195),
`os.Args` is never read at all.

## TUI mode (#603)

When `PYRY_FAKE_CLAUDE_TUI` is non-empty, fakeclaude emits two of claude's
TUI substrate glyphs so the #594 `WaitReady → DeliverPrompt → commit`
delivery contract can confirm a turn against it (otherwise `WaitReady`
never reaches idle, the 30 s deliver timeout elapses, and `send_message`
replies `server.binary_offline` instead of `ack`):

| Moment | Action | Effect |
|---|---|---|
| startup | write `❯` (U+276F idle prompt) once to stdout | tui-driver `IsIdle` true → first `WaitReady` returns immediately |
| first stdin bytes | write `✻` (U+273B thinking spinner) once | `IsThinking` true → `DeliverPrompt` confirms a **fast** commit |

A *single* `❯` write suffices because tui-driver's `Snapshot()` is a
**rolling 4096-byte raw-byte window, not a grid emulator** — the glyph
persists until evicted; no continuous redraw is needed (each consumer flow
drives exactly one idle→thinking transition). All `os.Stdout` writes (both
glyphs + the assistant chunk) are serialised under one `sync.Mutex` so a
spinner write cannot interleave mid-chunk and corrupt a marker. fakeclaude
**never echoes stdin content to stdout** — it writes only the fixed glyph,
holding the trust boundary against reflecting phone-controlled prompt bytes
onto the observed PTY.

**When unset, fakeclaude is byte-identical to its pre-#603 behaviour**, so
TUI-off callers (`StartRotation`, the rotation tests) are unperturbed.
Because TUI mode makes `main.go` carry the two
glyphs, the file is on the `cmd/substrate-guard` allowlist (#603),
mirroring the sanctioned `internal/agentrun/ptyrunner/helper_test.go`
exemption. On the surviving **v1** coarse leg, consumers must drain the
spinner `message` envelope the v1 assistant-turn emitter fans to the phone
(it races the ack); see [codebase/603.md](../codebase/603.md) for the drain
pattern. (The v2 coarse emitter that also did this was removed in
[#699](../codebase/699.md), and its two-phone-coarse e2e was deleted.)

The initial UUID must satisfy
`internal/sessions/rotation/watcher.go:19`'s `uuidStemPattern`
(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).
The fresh UUID minted on rotation is generated by the inline `uuidV4()`
helper, which mirrors `internal/sessions/id.go` byte-for-byte (same
`crypto/rand` + version/variant fixup).

## JSONL-trigger mode (#642)

`PYRY_FAKE_CLAUDE_ASSISTANT_TRIGGER` (#311) feeds the *coarse* path by writing
to **stdout** (which the PTY bridge forwards as a `message` chunk).
`PYRY_FAKE_CLAUDE_JSONL_TRIGGER` (#642) is its **structured-path** sibling: it
appends captured **claude-format JSONL turn events** to the **live session
JSONL file** — the file the daemon's structured-turn producer
(`cmd/pyry/interactive_turn_stream_v2.go`) tails — so an `interactive`-granted
phone receives the real structured envelope stream (`turn_state` /
`assistant_delta` / `tool_use` / `turn_end`). It is the harness piece that made
the live structured-receive capstone exercisable (option (b): fakeclaude
replays a captured transcript, rather than fusing real-claude with the
Noise-phone suite).

`emitStructuredJSONLIfTriggered(f, path)` mirrors `emitAssistantIfTriggered`
but appends to the session `*os.File` instead of stdout:

| Step | Effect |
|---|---|
| trigger appears | `os.ReadFile(path)`, cap at `assistantMaxBytes` |
| **empty read** | **zero-byte read → `return` WITHOUT removing the trigger (#958); the next ~50ms poll retries once the producer's write lands** |
| append | `f.Write(data)` verbatim to the live `<uuid>.jsonl` (already `O_APPEND`) |
| flush | **`f.Sync()`** — load-bearing for cross-process tail visibility |
| consume | `os.Remove(path)` |

- **The trigger file's contents ARE the JSONL lines to append** — same
  "contents are the payload" shape as the assistant trigger. The test supplies
  complete `\n`-terminated claude-format objects (modeled on
  `internal/turnbridge/mapper_test.go`'s `entry(...)` oracle); tui-driver's
  `TailJSONL` reassembles them into `turnevent.Event`s.
- **`f.Sync()` is load-bearing.** The daemon's tail is a separate process and
  macOS APFS otherwise defers cross-process visibility (the same reason the
  stdin reader fsyncs per write). Without it the producer may never see the
  appended bytes.
- **A zero-byte read is the producer's `open(O_TRUNC)`-before-`write` window,
  not a payload — it must not remove the trigger (#958).** Every producer drop
  is `os.WriteFile` (`O_CREATE|O_TRUNC` then a single `write`), so the trigger
  exists-but-empty for a brief window after truncation. A poll landing there
  used to `os.Remove` the trigger anyway, unlinking it before the producer's
  content was ever visible and losing the structured test's `sync.Once`
  `dropFull` line — the intermittent full-suite-`-race` flake in
  `TestTwoPhoneStructured_InteractiveReceivesStream` /
  `TestRelayV2_InterruptStopsRunningTurn`. See [codebase/958.md](../codebase/958.md).
- **Errors are silenced** (read/write/remove) — a missing trigger is the steady
  state, and the e2e asserts the outcome downstream (the interactive phone
  receives the structured envelopes).
- **Only the main goroutine writes `f`**, so the append never races the stdin
  reader. No new glyphs are emitted, so the **substrate-guard allowlist is
  unchanged** — the appended bytes are JSON the test supplies, not TUI
  substrate.
- **When unset, behaviour is byte-identical to today** — every existing caller
  is unperturbed. Off by default, like the other optional modes.

Two harness preconditions make the appended events actually reach the producer
(both handled by the #642 test, not by fakeclaude):

1. **Sessions-dir alignment.** `resolveClaudeSessionsDir` has no env override —
   it always computes `<HOME>/.claude/projects/encode(workdir)`. The test points
   fakeclaude at that **same** computed dir so the producer tails exactly what
   fakeclaude writes (the `rotation_test.go` alignment pattern).
2. **Pre-create `<initialUUID>.jsonl` before the daemon starts.** The producer
   captures its tail offset at the first resolve; pre-creating the file makes
   that resolve succeed at startup, seconds before the post-ack append, so every
   appended line lands inside the tailed range (fixes a cold-start
   producer-subscribe race — see [codebase/642.md](../codebase/642.md)).

## Idle-trigger mode (#792)

TUI mode (#603) emits the idle glyph `❯` at **startup**, so claude is idle
immediately and can serve exactly one turn (the spinner then wedges "thinking").
`PYRY_FAKE_CLAUDE_IDLE_TRIGGER` (#792) **inverts the startup posture** to give a
test a controllable **busy → free** window: fakeclaude comes up **busy** and
frees only when the test drops the trigger — so a phone can pile up an inbound
backlog that then drains back-to-back. It is the harness piece that made the
**live** queued-backlog capstone exercisable (what #723's `/bin/sleep` child
could not do: never idle → never drains).

When `PYRY_FAKE_CLAUDE_IDLE_TRIGGER` is set:

| Moment | Action | Effect |
|---|---|---|
| startup | emit **no** idle glyph, **never** the spinner | tui-driver `IsIdle` stays false → supervisor `WaitReady` **blocks** (claude "busy") |
| trigger's **first** appearance | `emitIdleIfTriggered`: `writeStdout(❯)` once, `os.Remove(trigger)`, set one-shot `idled` | `IsIdle` true → `WaitReady` returns; claude stays idle thereafter |

- **The single `❯` write frees the whole backlog.** After the glyph, **nothing
  overwrites the bottom status region** — the delivered prompt is not echoed to
  stdout — so `WaitReady` returns immediately for every subsequent queued turn
  and they drain one after another with no further signal. This is why the mode
  is ~15 LOC: no spinner, no scroll/redraw emulation, no per-turn cycling. The
  load-bearing tui-driver fact (v1.6.0): `IsIdle` = `❯` present in the bottom
  region AND no spinner there — *withholding* `❯` is a busy gate, *emitting* it
  once is the release edge.
- **Gated by a one-shot `idled` bool** in the `main` poll loop, exactly like the
  `rotated` rotation gate — the glyph is emitted at most once. `emitIdleIfTriggered`
  runs **only on the main goroutine**, so it never races the stdin reader for
  `os.Stdout` (and in this mode `PYRY_FAKE_CLAUDE_TUI` is off, so the reader emits
  no spinner — the main goroutine is the sole stdout writer). Errors are silenced,
  mirroring the sibling `emit*` helpers.
- **Per-turn commit relies on `STDIN_LOG`, not on this mode.** Idle-trigger does
  **not** itself open the stdin reader (the gate is still `logPath != "" || tui`,
  `main.go:692`). The #792 consumer sets `PYRY_FAKE_CLAUDE_STDIN_LOG` (via
  `StartRotationWithRelay`), so the reader runs, `turnPending` fires, and the
  existing `appendTurnGrowth(f)` grows the JSONL that the supervisor's
  `confirmViaTranscriptGrowth` (#668/#673) observes as the commit. No spinner is
  needed — the relay bootstrap sets `ResolveTranscript`, so commit is growth-based.
- **Mutually exclusive with `PYRY_FAKE_CLAUDE_TUI`.** TUI mode's startup `❯` and
  stdin-spinner would defeat the busy window and wedge the second turn. A test
  sets one or the other, never both.
- **No new glyph, no allowlist change.** `❯` (`idleGlyph`) is already declared and
  the file is already on the `cmd/substrate-guard` allowlist; the mode only
  changes *when* the existing glyph is emitted. **When unset, byte-identical to
  today** — every existing caller is unperturbed.

See [codebase/792.md](../codebase/792.md) for the live queue-drain capstone this
mode feeds (the busy→free choreography, the empty-`queue_state` happens-after
fence, the ordered vacuous-pass guards).

## Esc-ends-turn mode (#794)

`PYRY_FAKE_CLAUDE_ESC_ENDS_TURN` makes the **remote interrupt keystroke itself**
end the running turn — the harness piece behind the live interrupt capstone
([codebase/794.md](../codebase/794.md)). The interrupt path routes a phone
`interrupt` frame → `handleInterrupt` → `supervisor.SendEsc()` → a **lone `0x1b`**
into the supervised child's stdin. In this mode fakeclaude detects that bare ESC
and appends claude's own interruption marker to the live session JSONL, so the
daemon's structured-turn producer maps it to a `turn_end{cancelled}` — making the
Esc the **cause** of the turn ending. (Reusing #792's *file*-driven busy→idle flip
would instead let an "interrupt stopped the turn" assertion pass **vacuously** —
the idle would come from a file, not the Esc.)

> Through #1244 (2026-07-31), this appended an assistant `stop_reason:"end_turn"`
> line (`interruptEndTurnLine`) — a shape that looked like a clean completion on
> disk and could only prove *causality*, never the stop reason, because
> `EventKindJsonlEndOfTurn` was the mapper's only `turn_end` source. #1243 added a
> second source — claude's own interruption marker (`turnbridge/mapper.go:95`) —
> and #1244 restaged this handler to write that marker instead. See
> [codebase/1244.md](../codebase/1244.md).

When `PYRY_FAKE_CLAUDE_ESC_ENDS_TURN` is set:

| Moment | Action | Effect |
|---|---|---|
| startup | `enterRawMode()` (like modal mode) | a lone ESC with no line terminator reaches `read()` verbatim (canonical discipline would otherwise withhold it) |
| stdin read containing a bare ESC | `containsBareESC(buf)` → set `escPending` (signal only) | stdin reader flags the interrupt without touching `f` |
| main poll loop, `escPending.Swap(true)`, one-shot `escEnded` | `appendTurnEnd(f)`: write `interruptMarkerLine` + `f.Sync()` | producer tails it → `isInterruptMarker` (`turnbridge/mapper.go:95`) → `TurnEnd{Cancelled}` → `turn_end{cancelled}` to the phone |

- **A bare ESC is unambiguously the interrupt.** The stdin stream carries exactly
  three ESC sources — bracketed-paste open (`ESC[200~`), close (`ESC[201~`), and
  the interrupt's lone `0x1b`. Both paste markers are `0x1b` immediately followed by
  `'['` (`0x5b`); the delivered prompt content between them is raw-ESC/C0-free
  (#749's paste-content guard), so it contributes no `0x1b`. `containsBareESC` rule:
  a `0x1b` at index `i` is bare iff `i == len(buf)-1` (last byte) **or**
  `buf[i+1] != 0x5b`. The last-byte arm is safe because tui-driver writes each paste
  marker as one `writeRaw` of the whole `ESC[200~…ESC[201~\r` unit (its `[` always
  follows in the same read), whereas `SendEsc` writes the lone `0x1b` standalone. A
  future multi-KiB prompt whose paste could split across reads on a marker's `0x1b`
  would need a one-byte cross-buffer carry — not built (this harness controls the
  prompt size). The discriminator is pinned by the untagged `esc_detect_test.go`
  (`TestContainsBareESC`).
- **Raw mode is load-bearing.** Unlike TUI mode, this mode enters raw mode: a lone
  ESC with no newline is withheld indefinitely by the default canonical line
  discipline, so without raw mode the detector would never see the interrupt (the
  `turn_end` would silently time out). The raw-mode gate is now
  `if modalTrig != "" || escEndsTurn { enterRawMode() }`.
- **Single-writer-of-`f` preserved.** The stdin reader only *signals*
  (`escPending atomic.Bool`, exactly like `turnPending`); the `appendTurnEnd(f)`
  write runs on the main poll goroutine. No mutex on `f`, no new writer goroutine.
- **`interruptMarkerLine` is a fixed literal, derived from a real capture** — the
  claude-format `type:"user"` interruption entry `mapEntry`'s `case "user"` maps to
  `turnevent.TurnEnd{Cancelled}` (`turnbridge/mapper.go:95`), byte-shaped after the
  repo's one recorded interruption entry
  (`internal/agentrun/jsonl/testdata/no_end_turn.jsonl:53`, claude 2.1.128): the
  full 13-key top-level set and its order preserved verbatim, only the
  session-identity fields (`parentUuid`, `promptId`, `uuid`, `timestamp`, `cwd`,
  `sessionId`, `gitBranch`) substituted with shape-preserving canned values. Two
  absences are load-bearing and silent if broken — no top-level `permissionMode`
  key (`userAuthored`'s presence check, `mapper.go:163-166`) and no `tool_result`
  block (`ParseToolResult` precedes the marker check and would intercept it,
  `mapper.go:80-86`) — either one makes the mapper produce no `turn_end` at all. It
  is inert JSONL data, **not** a TUI substrate glyph, so the `cmd/substrate-guard`
  allowlist is unchanged.
- **One-shot.** The `escEnded` gate bounds the append to one end-of-turn line; a
  second ESC is inert — a re-interrupt of an already-ended turn is a no-op, matching
  claude.
- **Coexists with `PYRY_FAKE_CLAUDE_TUI`** (unlike idle-trigger's mutual exclusion).
  The two touch different bytes: TUI emits the startup `❯` + one spinner; the ESC
  detector scans for the bare ESC. The #794 capstone runs both ON. **When unset,
  byte-identical to today** — every existing caller is unperturbed.

The turn_end carries `StopReason == "cancelled"` (since #1244; through #1244 it was
`"end_turn"`, because `EventKindJsonlEndOfTurn` was the mapper's only `turn_end`
source and could not distinguish an interrupt-stop from a normal end). The
bootstrap-tier consumer (`relay_v2_interrupt_test.go`, the #794 capstone) still does
not assert the reason — it proves **causality** (the `turn_end` exists only because
the Esc was received), by choice now rather than by impossibility, leaving the
reason assertion to the minted-tier oracle one level up
([codebase/1244.md](../codebase/1244.md)). See [codebase/794.md](../codebase/794.md)
for the live interrupt capstone this mode feeds (the structural-causality guard, the
two ordered `t.Fatal`s, the two-oracle belt-and-suspenders) — historical as of its
own ticket, so its `end_turn`-not-`cancelled` framing there is not rewritten here.

## Modal-clear-on-answer mode (#793)

Modal mode (`PYRY_FAKE_CLAUDE_MODAL_TRIGGER`, #791) raises a permission prompt
(`modalScreen`, the `"Do you want to proceed?"` anchor) and **never clears it** — enough
for #791, whose phone answers and whose daemon **remote** arm broadcasts the dismissal
regardless of claude's screen. But the #706 **local** first-answer-wins arm
(`handleModalHidden`) is `EventKindPtyModalHidden`-driven, and that event fires only on a
genuine `Permission→Unknown` screen transition. `PYRY_FAKE_CLAUDE_MODAL_CLEAR_ON_ANSWER`
(#793) closes that gap: after the modal is shown it clears it on the local head's answer
keystroke, making the keystroke the **cause** of the Hidden event — the harness piece
behind the live two-head first-answer-wins capstone
([codebase/793.md](../codebase/793.md)). (Reusing #791's fake unchanged would leave the
local arm dead; changing `MODAL_TRIGGER` itself to clear would race #791's remote arm on
the same `modal_id` — hence a **new** default-off env.)

When `PYRY_FAKE_CLAUDE_MODAL_CLEAR_ON_ANSWER` is set **and** `MODAL_TRIGGER` is set (it is
a no-op without modal mode):

| Moment | Action | Effect |
|---|---|---|
| stdin read (any bytes) | `clearPending.Store(true)` (signal only, never touches stdout or `f`) | mirrors `turnPending`/`escPending` — the reader flags, the main goroutine acts |
| main poll loop, `modalShown && !modalCleared && clearPending.Swap(false)` | `clearModalScreen()` → `writeStdout(modalClearScreen)` + fsync, set one-shot `modalCleared` | `DetectModalClass` Permission→Unknown → merge loop fires `EventKindPtyModalHidden` → #706 local arm `Resolve`s → `modal_dismissed{local}` |

- **The `modalShown` gate defers the clear to a post-modal keystroke.** `clearPending` is
  set on every read, but the main-loop short-circuit leaves it latched until the modal is
  up, then fires on the first post-modal byte — a pre-modal keystroke cannot clear early.
- **`modalClearScreen = strings.Repeat("\r\n", 16) + string(idleGlyph)`.** The 16 blank
  lines (`modalClearScrollRows`) scroll the `"Do you want to proceed?"` anchor above the
  bottom `permissionRegionRows` (12) window `DetectModalClass` scans (the fixed 40-row
  `DefaultPtyRows` grid — `DetectModalClass` always renders at 40×120 regardless of the
  PTY's live, possibly attach-resized, dimensions). The **trailing idle glyph is
  load-bearing, not decoration**: `trimGridText` drops trailing empty rows, so a
  newline-only clear screen collapses back and the anchor re-enters the detection window,
  still classifying as Permission — a non-empty final row pins the grid height.
- **Single-writer discipline preserved.** The stdin reader only signals (`clearPending
  atomic.Bool`); `clearModalScreen` runs on the main poll goroutine (modal mode emits no
  spinner, so the main goroutine is the sole stdout writer). No mutex on `f`, no new writer
  goroutine — the same shape as `turnPending`/`escPending`.
- **One-shot** via `modalCleared`; a second keystroke is inert.
- **No new glyph, no allowlist change.** `modalClearScreen` reuses the already-declared
  `idleGlyph` (`❯`), so the `cmd/substrate-guard` seal is untouched. **When unset,
  byte-identical to today** — #791 sets only `MODAL_TRIGGER` and its fake never clears, so
  its remote arm stays unaffected.

**De-risk the fixture harness-free.** `TestFakeClaude_ModalClearScreenReturnsToNonPermission`
(untagged, in `modal_detect_test.go`) asserts `DetectModalClass(modalScreen+modalClearScreen)
!= Permission` — feeding the concatenated bytes to a fresh detector reproduces the daemon's
sequential-write vt10x state deterministically, so it predicts the live `Permission→Unknown`
transition in milliseconds. A non-clearing fixture (too few newlines, or a newline-only
screen `trimGridText` collapses) fails here rather than timing out inside a slow live-daemon
run. The original `modalScreen == Permission` assertion (#791) is unchanged.

See [codebase/793.md](../codebase/793.md) for the live two-head first-answer-wins capstone
this mode feeds (the local `pyry attach` head bound before the modal is raised, the two
ordered observe-positives gating the loser-no-op, the `dismissed_local` live audit oracle).

## Clear-rotate mode (#1004)

`PYRY_FAKE_CLAUDE_CLEAR_ROTATES` makes the **phone-driven `new_session` keystroke
itself** rotate the session — the harness piece behind the `new_session` v2
control-verb e2e ([codebase/1004.md](../codebase/1004.md)). The `new_session` path
routes a phone frame → `handleNewSession` → `supervisor.StartNewSession()` →
`ClearInputLine` (Ctrl-U) + `TypePrompt("/clear")` + `"\r"` into the supervised
child's stdin. Real claude rotates its session UUID on `/clear`; in this mode
fakeclaude detects the `/clear` bytes and performs the same close-old/open-new
rotation the file trigger does, so the daemon's rotation watcher — and thus the
registry's on-disk bootstrap id — moves **because of** the `new_session` frame,
not vacuously. (Reusing the file trigger unchanged would leave `new_session`
untested — nothing in the harness would ever point the trigger at a real file for
this verb, since the whole point is proving the *keystroke* causes the rotation.)

When `PYRY_FAKE_CLAUDE_CLEAR_ROTATES` is set:

| Moment | Action | Effect |
|---|---|---|
| stdin read | reader accumulates bytes across reads, checks `containsClearCommand(buf)` | sets `clearRotatePending` (signal only) once `/clear` has fully arrived |
| main poll loop, `!rotated && clearRotatePending.Swap(false)` | `f = rotateSession(f, dir)`, set `rotated = true` | closes the old JSONL, opens a fresh `<uuid>.jsonl` — the rotation watcher follows it into the registry |

- **Detection is discipline-agnostic.** `containsClearCommand(buf) = bytes.Contains(buf,
  []byte("/clear"))` over the reader's accumulated buffer, so a `/clear` split
  byte-by-byte under a hypothetical raw-mode caller and the single `/clear\n` line
  the default **canonical** discipline delivers (this mode's actual case) both
  match. In this mode the only stdin fakeclaude ever receives is that one
  keystroke sequence, so a plain substring match cannot false-positive.
- **No raw mode needed.** Unlike Esc-ends-turn mode, this mode does **not** call
  `enterRawMode()`. `ClearInputLine`'s leading Ctrl-U (`0x15`) is consumed by the
  canonical line discipline as VKILL on an empty line (a no-op), and the trailing
  `\r` from `TypePrompt` commits the line, so a single read already carries the
  full `/clear\n` — no withheld-until-newline problem the way a lone ESC has.
- **Signal-only from the reader, shared `rotated` gate.** `clearRotatePending
  atomic.Bool` mirrors `escPending`/`turnPending` exactly — the reader never
  touches `f`; `rotateSession` runs only on the main poll goroutine. Reusing the
  existing `rotated` one-shot bool (rather than a fresh gate) means a test can
  never double-rotate, and the file-trigger and clear-rotate paths are mutually
  exclusive in practice — whichever fires first wins, the other becomes inert.
- **`rotateSession(f, dir) *os.File`** is a small helper extracted from the file
  trigger's inline close/open so both branches share one implementation
  (`f.Close()`; `return openSession(dir, uuidV4())`). The old-fd close is
  best-effort — every write is already fsynced by `openSession`/`appendTurn*`, so
  a failed close can't lose committed data.
- **No new glyph, no allowlist change.** The mode touches only stdin detection and
  the JSONL file rotation — no stdout writes, so `cmd/substrate-guard` is
  unaffected. **When unset, byte-identical to today** — every existing caller
  (`StartRotation`, the other e2e tests) is unperturbed.

Pinned by the untagged `containsClearCommand` table test in
`clear_detect_test.go`, mirroring `esc_detect_test.go`'s `TestContainsBareESC`.

See [codebase/1004.md](../codebase/1004.md) for the live `new_session` e2e this mode
feeds (the structural-causality guard — the file trigger points at a never-created
path so `/clear` is the only rotation source — and the bounded re-send loop that
makes the fire-and-forget verb deterministic against session-attach timing).

## Stream-json mode (#1140)

Every mode above models claude's **PTY/TUI** surface — a screen to read, a
`<uuid>.jsonl` transcript to grow. The daemon's `internal/streamsup` runner (the
`interactive_runner: "stream-json"` toggle, #1081, shipped) instead spawns claude
**headless over a pipe**: line-delimited stream-json envelopes on stdin, `assistant`/
`result` lines on stdout, no PTY and no transcript file at all. `PYRY_FAKE_CLAUDE_STREAM_JSON`
teaches fakeclaude that wire, so the stream path has a fake to drive it end-to-end —
the harness piece the sibling #1135 (blocked-by this ticket) rides for its first
`send_message` e2e spec.

When `PYRY_FAKE_CLAUDE_STREAM_JSON` is set, `main()`'s **very first** statement is:

```go
if os.Getenv(envStreamJSON) != "" {
    runStreamJSON(os.Stdin, os.Stdout, os.Getenv(envStreamInterrupt) != "")
    return
}
```

This one gate — checked above every `mustEnv(envSessionsDir/…)` call — makes three
properties fall out structurally rather than by a validation branch:

| Property | Why it holds |
|---|---|
| Byte-identical when unset | An unset env var falls straight through; nothing below the `if` changes |
| Mutually exclusive with every PTY/TUI mode | The `return` fires before any other mode's env var is even read; if both are set, stream wins and the other is inert |
| Binds no sessions dir / transcript | The `return` short-circuits before the three `mustEnv` calls and the JSONL open — stream mode never touches sessions-dir machinery |

`runStreamJSON(r io.Reader, w io.Writer, honorInterrupt bool)` is the testable
read→emit loop — the `io.Reader`/`io.Writer` seam (rather than hard-wiring
`os.Stdin`/`os.Stdout`) is what lets the unit test drive it against in-memory
buffers. `honorInterrupt` (default `false`, wired from `envStreamInterrupt`, #1136)
selects the interrupt rider — see § Interrupt mode below; this table describes the
default path:

| Step | Effect |
|---|---|
| Read one line | `bufio.NewReader(r).ReadString('\n')` — not `bufio.Scanner`, whose token cap would truncate a long prompt; also correctly processes a final non-newline-terminated read at EOF |
| Decode | `userTurnText(line)` — unmarshal into a local `inUserTurn` mirror of `streamsup.userTurn` (unexported there); a decode error or `Type != "user"` returns `("", false)` and the line is skipped (a `control_request` interrupt line, a blank line) |
| Respond | on a user line, mint `m<N>` (a local monotonic `int` counter, one per received turn) and call `writeStreamResponse(w, id, text)`, where `text` is the first `text` content block, verbatim |
| Stop | on `ReadString` error (EOF — the daemon closed stdin — or a read error) or the first write error (daemon's read end gone); both treated like teardown, never `os.Exit` mid-turn |

`writeStreamResponse(w io.Writer, msgID, text string) error` writes fakeclaude's
canned reply to one turn — one `assistant` line, one `result` line — each
`json.Marshal`-encoded from a local struct (`outAssistant`/`outResult`), never
string-concatenated, so the echoed `text` (caller-controlled bytes) is escaped and
each object lands as exactly one physical line regardless of content:

```
{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"<echoed prompt>"}]}}
{"type":"result","subtype":"success","session_id":"fake-stream"}
```

These are byte-compatible with `cmd/pyry/stream_turn_drain_test.go`'s
`assistantTextLine`/`resultLine` fixtures, so the daemon's real
`streamsup.Parser` (`internal/streamsup/parser.go`) maps them to
`turnevent.TextChunk{Text: <echoed prompt>}` then `turnevent.TurnEnd{Reason:
TurnEndReasonEndTurn}` — see [streamsup-package.md § Turn I/O](streamsup-package.md#turn-io--envelope-write--stdout-parser-1088).

- **`text` is echoed, not canned.** The response text is the inbound prompt itself
  — free downstream value for the #1135 `send_message` e2e, which can assert
  delta text == sent prompt, at the cost of one extra field-read on the
  already-decoded struct.
- **`session_id` is a fixed literal (`"fake-stream"`).** `Parser.streamLine` never
  decodes `session_id`, so the value is cosmetic; threading the daemon's injected
  `--session-id` through would force argv parsing the fake deliberately never does.
- **Wire shapes are hand-mirrored, not imported.** `internal/streamsup`'s envelope/
  parser types are unexported, and importing them would also pull a dependency into
  the fakeclaude **binary** — the same zero-dependency posture that keeps
  `interruptEndTurnLine` (Esc-ends-turn mode, above) a hand-written literal. Only the
  **test** links `internal/streamsup`/`internal/turnevent`; `main.go` stays
  stdlib-only.
- **Single goroutine, no raw mode.** `runStreamJSON` runs entirely on `main()`'s
  goroutine — no stdin-reader goroutine, no poll loop, none of the other modes'
  `atomic.Bool` signals are reached. Stream-json travels over a **pipe**, not a PTY,
  so canonical line discipline / CR mapping don't apply — `enterRawMode()` is never
  called in this mode.
- **Default mode still ignores every non-`"user"` line, with one exception.** An
  `initialize` `control_request` gets a canned answer regardless of mode or rider (#1692,
  see § Initialize control request answer below); every other `control_request` —
  including `interrupt` in default mode — is still dropped unlooked-at. new_session /
  queue / modal remain out of scope for the fake. Interrupt is handled by the
  `honorInterrupt` rider, below (#1136).
- **No new glyph, no allowlist change.** Stream mode emits pure JSON — no TUI
  substrate glyphs — so `cmd/substrate-guard`'s allowlist for this file is
  unaffected.

Pinned by `stream_detect_test.go` (untagged, package-level `go test`, no e2e build
tag): a single-turn round-trip and a multi-turn round-trip both feed
`runStreamJSON`'s output through the **real** `streamsup.Parser` (belt-and-suspenders,
different fabric — a shape bug the fake and a hand-written test-decoder would share
is still caught on the emit side) and assert the exact `TextChunk`/`TurnEnd` sequence;
a non-user-lines-ignored test asserts zero output bytes for a `control_request` /
blank / unparsable line; a distinct-message-ids test guards the per-turn counter; a
direct `writeStreamResponse` shape test checks the two line shapes without going
through the parser at all. See [codebase/1140.md](../codebase/1140.md).

### Interrupt mode (`honorInterrupt`, #1136)

`PYRY_FAKE_CLAUDE_STREAM_INTERRUPT` (default-off) is a rider on stream mode:
`envStreamJSON` still gates entry to `runStreamJSON`; `envStreamInterrupt` is read
once, at the same call site, and passed through as the `honorInterrupt bool` param
so the function itself stays a pure I/O seam with no env reads inside. It exists to
give the stream path a fake that can stay **mid-turn** long enough for a live
interrupt e2e (#1136) to land one — the default mode always answers a user turn
immediately, so there is no window to interrupt.

| Inbound line | `honorInterrupt == false` (default) | `honorInterrupt == true` |
|---|---|---|
| `{"type":"user",…}` | `writeAssistantEcho` + `result{success}` (unchanged) | `writeAssistantEcho` **only** — the result is withheld, so the turn stays in flight |
| `{"type":"control_request",…"subtype":"interrupt"}` | ignored | `writeInterruptAck` — `control_response{response:{subtype:"success",request_id:<echoed>}}` (#1500), **then** `writeInterruptedResult` — `result{subtype:"error_during_execution"}` |

`writeStreamResponse` was split so the assistant-echo half is independently
reusable: `writeAssistantEcho(w, msgID, text)` writes just the assistant line;
`writeStreamResponse` is now `writeAssistantEcho` + the `result{success}` line,
byte-identical to its pre-#1136 output. `interruptControlRequest(line []byte) (string, bool)`
decodes a minimal `{type, request_id, request.subtype}` mirror of
`streamsup.controlRequest`/`marshalInterruptEnvelope` (`internal/streamsup/envelope.go`)
— the exact shape the daemon writes to the child's stdin on a phone interrupt — and
returns `("", false)` (line ignored) on anything that isn't
`type=="control_request" && request.subtype=="interrupt"`, preserving the same
per-line decode resilience as `userTurnText`. The returned id is what the ack echoes;
it widened from a bare `bool` in #1500.

**The ack goes FIRST (#1500).** `writeInterruptAck` writes the `control_response`
real claude answers an interrupt with, before the interrupted `result` and on the same
writer — matching claude's own order (~40 ms ack, then the result) and, more usefully,
buying the e2e its causality on the rate-limit rider's terms: a `turn_end` reaching a
client implies the ack has already been through the parser, so
`TestRelayV2_StreamInterruptStopsRunningTurn`'s zero-`unrecognized_message` assertion
needs no sleep, no poll and no ordering race to tune. The envelope is transcribed from
the committed capture (`internal/e2e/realclaude/testdata/set_permission_mode_v2.1.220_revoke.json`,
verbatim in [`set-permission-mode-inband-probe.md`](set-permission-mode-inband-probe.md#the-control_response-received-verbatim)),
which nests `subtype` and `request_id` **under `response`** — the inverse of the
request side. Two honest limits: the capture is a `set_permission_mode` ack rather than
an interrupt one, so what it establishes is the control channel's *envelope*; and the
inner `response` payload is request-specific and unmeasured for `interrupt`, so the fake
invents none. `TestRunStreamJSON_InterruptAckRider` pins the two emitted lines and their
order — it is the arrival control for the e2e's zero, which cannot prove its own input
arrived.

The mode is **stateless**: it emits the ack + `result{error_during_execution}` pair on
*every* interrupt `control_request` it sees, with no in-flight-turn tracking. A caller that
drives exactly one interrupt per in-flight turn (the only shape #1136's e2e needs)
gets the right behaviour for free; nothing polices "interrupt with no turn open".

`error_during_execution` is not an arbitrary error subtype — `streamsup.Parser`'s
`resultTurnEndReason` (`internal/streamsup/parser.go`) maps that one subtype to
`turnevent.TurnEndReasonCancelled` and every other subtype to `end_turn`, so this is
the specific value that makes the daemon report the turn as interrupted rather than
merely errored. See [codebase/1136.md](../codebase/1136.md).

### Initialize control request answer (#1692)

The daemon is gaining the ability to ask its stream child to `initialize` (#1689) — the
request real claude answers with the session's model list and slash-command list. Unlike
every rider above, this answer is **unconditional, not env-gated**: `runStreamJSON`'s
dispatch matches an `initialize` `control_request` in an arm placed *beside*
`honorInterrupt`, evaluated before it, so the answer fires in both stream modes and needs
no new env var, no widened signature, no new `main()` call site. This is a deliberate
departure from this doc's "default-off, byte-identical when unset" house rule for riders:
nothing sends the request yet, so the unconditional answer changes no existing suite's
bytes today, and gating it behind a knob would leave the default path — the one every
future suite runs once #1689 lands — silently unanswered. At the time this answer
shipped, `streamsup.Parser`'s `control_response` case read nothing below the top-level
`type`, so the answer reached no client and changed no frame count anywhere; `emitModelList`
(`internal/streamsup/parser.go`) is what now decodes it — see
[streamsup-package.md](streamsup-package.md) for the settled per-field absent/empty/false
readings.

`controlRequestID(line []byte, subtype string) (string, bool)` is the shared decode
`interruptControlRequest` used to own alone, generalised by subtype (`subtypeInterrupt`
vs `subtypeInitialize`) rather than duplicated — the same
extract-the-shared-half-into-a-parameter move #1631 made for `argvSessionID` →
`argvIDFlag` in this file. `interruptControlRequest` is now a one-line wrapper and its
existing behaviour, and every existing interrupt-mode test, are unchanged by construction.

`writeInitializeAck` answers with the same inverted envelope `writeInterruptAck`
established (`subtype`/`request_id` nested *under* `response`), one level deeper still:
the initialize payload sits at `response.response`, carrying `models` only —
`writeInitializeAck`/`initializeModels` — never `commands`, `agents`, `account`, or the
rest of the real payload (#1683 is what extends this same answer with `commands`).

**Absent is not present-and-empty, and a map is what makes that literal.** The canned
list carries two entries transcribed verbatim from
`internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` — a `sonnet` entry
with the full eight-key shape (five effort levels, `supportsAutoMode: true`) and a
`haiku` entry with only four keys, `supportedEffortLevels`/`supportsAutoMode` **absent as
JSON keys**, not `false` or `[]`. `internal/protocol`'s `ModelOption.MarshalJSON` (#1704)
already decided that absent and empty both publish as `[]` on the *wire*; the
daemon-internal reading is settled too — `turnevent.ModelOption.EffortLevels` reads
absent, `null` and published `[]` as one reading, `nil` (#1828), and
`turnevent.ModelOption.SupportsAutoMode` reads absent, `null` and explicit `false` as one
reading, `false` (#1819). The collapse is the *decode*'s to perform (`emitModelList`'s
`boundEach` for the list, a plain `bool` needing no code at all for the flag — see
[streamsup-package.md](streamsup-package.md)), so the minimal entry's job is to keep
feeding that decode the one shape claude actually sends: a fake that emitted `[]`/`false`
for the minimal entry instead of omitting the keys would hand the decode an
already-collapsed input and the absent-key arm would go unexercised. Each canned entry is
a `map[string]any`, like `writeInterruptAck`/`writeRateLimitEvent`: a struct with
`omitempty` would conflate `false` with absent for exactly the field this ticket exists to
keep distinct.

**The `request_id` echo depends on going through `writeJSONLine`, not `fmt.Sprintf`.**
`requestID` is inbound bytes the daemon wrote to this child's stdin, reflected straight
back onto stdout, which the daemon's stream parser reads as line-delimited JSON.
`json.Marshal` escapes it, so a `request_id` carrying an embedded newline plus a forged
envelope lands as one escaped string inside one physical line; built by string
concatenation instead, that same value would split the output and fabricate an extra
stream line the parser would consume as real — the same property `writeAssistantEcho`
already depends on for the echoed prompt.

The mode-independence claim (fires in both `honorInterrupt` states) turned out to have a
two-sided mutation proof, and the sides are not symmetric with what you'd guess from the
placement rule alone: a mutant that gates the arm *inside* the `honorInterrupt` branch
reddens only the **default-mode** row (the arm never runs there); a mutant that reaches
the arm *only when `honorInterrupt` is false* reddens only the **interrupt-mode** row.
Both rows earn their place, each catching the opposite mis-gating — worth knowing before
trimming a two-row table that looks redundant from the code alone.

Pinned by the untagged `initialize_control_test.go` (`stream_detect_test.go`'s
no-build-tag discipline, so `make check` runs it in both the `test` and `e2e` targets):
both stream modes get the double-nested ack, both canned key sets are attested by
re-walking `internal/e2e/realclaude/testdata/initialize_control_v*.json` (never trusting
that capture's own `models_entry_fields` union summary, which cannot see per-entry
shape), and an unknown-subtype `control_request` is confirmed unchanged. The capture is
a live developer recording (`argv` carries a home path, the inner payload carries an
`account` object) — the test's failure messages print key **names** only, never a
decoded entry or file dump, to avoid republishing that into CI output on a red run.

### Stream-path stdin tee (`PYRY_FAKE_CLAUDE_STDIN_LOG`, #1137, per-child since #1331)

`PYRY_FAKE_CLAUDE_STDIN_LOG` already existed for the PTY path (`startStdinReader`,
above). #1137 extended the *same* env var to the stream-json branch: when set, the
stream dispatch wraps `os.Stdin` in `io.TeeReader(os.Stdin, syncWriter{f})` before
calling `runStreamJSON`, so every byte the daemon writes to the child's stdin — user-
turn envelopes, interrupt `control_request`s — is appended to the log. `syncWriter`
is a tiny `io.Writer` (`Write` → `f.Write` → `f.Sync`) mirroring the PTY reader's
per-write `Sync`.

**The value's meaning differs by mode.** On the PTY path the env value is still the
literal file every child appends to, unchanged. On the stream path (since #1331) the
value is a path *stem*, not a file: each child tees to `<stem>.<the session id its
own argv was pinned to>`, derived by the pure helper `streamStdinLogPath(stem, args)`
on top of `argvSessionID`'s existing stem guard (`main.go:1181`, rejects `/`, `\`,
`.`) — reused rather than re-implemented, which is what makes splicing the raw argv
value into a path safe. A child whose argv carries no usable id (unreachable via
`streamsup.buildArgs`, which always appends `--session-id`/`--resume`) falls back to
`<stem>.unattributed` — a rendering choice, not a defense.

\#1137 originally had the bootstrap child and a later fresh post-rotation child
(after a stream `new_session`) accumulate into one shared log, on the theory that a
needle proved the turn was received. #1331 retired that: the daemon's process env is
inherited identically by every child, so a needle in a shared file proved only that
*some* child received a turn, never which one — and when the *outgoing* child got it
instead of the fresh one, the test that reads this log went green on the wrong
evidence. Per-child files close that gap by construction: each is written by exactly
one child's single stdin-read loop, so a needle can only appear in the file the
child that actually received it wrote. Keying on the *session id* (not pid or spawn
ordinal) means a same-session crash-respawn (`--resume <sameID>`) still appends to
one file — the file identifies a session, not a process. Same move #1195 already
made for the per-child JSONL trigger path (below): stop sharing one path, key it on
the child's own argv stem.

`O_APPEND | O_CREATE | O_WRONLY, 0o600` and the per-write `Sync` are unchanged in
both modes — append is still load-bearing for same-session respawns, and the fsync
is still what makes a sibling process's `os.ReadFile` see bytes promptly.

The tee lives at the `main()` call site, not inside `runStreamJSON` — the function
keeps its pure `(io.Reader, io.Writer, bool) ` I/O seam, so the #1140 unit test and
the #1136 interrupt rider are byte-identical regardless of this tee's shape. With
the env unset (every caller except the one test that sets it) the branch is exactly
`runStreamJSON(os.Stdin, os.Stdout, honorInterrupt)` — unchanged.

This is the runtime oracle for proving a stream `new_session` never types `/clear`
to a child, and — since #1331 — for proving *which specific child* received a given
turn: on the stream path `new_session` is a process re-spawn
(`(*streamsup.Runner).RestartFresh`, #1124), never a keystroke, so a child's log
should contain a user-turn marker (non-vacuity) but never the substring `/clear`,
and the post-rotation child's own file — not any shared file — is what a "did the
fresh child receive turn N" assertion must read. See [codebase/1137.md](../codebase/1137.md)
and [codebase/1331.md](../codebase/1331.md).

### Stream-path startup hold (`PYRY_FAKE_CLAUDE_STREAM_HOLD`, #1138)

The stream path does not pace on commit: `streamsup.WriteTurn` writes the user-turn
envelope to the child's held-open stdin and returns on **write**, not on the child
reading it or replying — unlike the PTY path's `WaitReady`-gated delivery, there is
no way to hold a queue backlog open by making a *response* slow. `PYRY_FAKE_CLAUDE_STREAM_HOLD`
gives the stream path the same "come up busy, release on trigger" shape idle-trigger
mode (#792) gives the PTY path, but at **startup**, before any stdin is read at all —
so that queued turns buffer in the daemon-to-child pipe rather than needing a
per-turn hold that would coalesce turns at the emitter.

Placement is the call site in `main()`'s stream branch, immediately before
`runStreamJSON`, guarding a small poll helper:

```go
if hold := os.Getenv(envStreamHold); hold != "" {
    waitForTriggerFile(hold) // os.Stat poll, mirrors the emit*IfTriggered pattern
}
runStreamJSON(stdin, os.Stdout, os.Getenv(envStreamInterrupt) != "")
```

- **`waitForTriggerFile(path string)`** loops `os.Stat(path)` every `pollInterval`
  until it succeeds, then returns. It never removes the trigger — nothing re-reads
  it, unlike the rotation/idle triggers.
- **`runStreamJSON`'s signature and body are unchanged.** The hold sits above the
  call, the same discipline #1137's stdin tee uses ("the tee/hold lives at the call
  site so `runStreamJSON` keeps its pure I/O signature") — so `stream_detect_test.go`'s
  7 call sites and the #1136/#1137/#1140/#1141 stream specs are unaffected.
- **Composes with the #1137 tee.** The hold check runs after the tee is wrapped
  around `os.Stdin` (`stdin` may already be a `TeeReader`), so a test combining both
  envs still logs bytes written during and after the hold.
- **Why a startup hold and not an in-turn hold.** Holding a turn's *response* open
  (rather than the child's stdin-read loop) cannot create an observable backlog on
  this path — `WriteTurn` returns regardless of whether the child is reading — and
  holding multiple in-flight turns open at once would coalesce into one turn at the
  emitter (no `turn_end` between echoes). Parking the child before its read loop
  starts lets the daemon's pipe writes buffer normally (they always buffer; `WriteTurn`
  returns `nil` either way) and produces one clean `responding→delta→turn_end→idle`
  cycle per queued turn, in FIFO order, on release.
- **When unset, byte-identical to today.** No allowlist change (pure `os.Stat` poll,
  no stdout write).

See [codebase/1138.md](../codebase/1138.md) for the live queue-drain e2e this mode
feeds — the all-three-acks-before-release vacuity gate, and why submission order is
proven via the FIFO stdin-pipe chain rather than `queue_state` depth.

### Approve rider (`PYRY_FAKE_CLAUDE_STREAM_APPROVE`, #1139)

`PYRY_FAKE_CLAUDE_STREAM_APPROVE` (default-off) is a rider on stream mode, mutually
exclusive with the interrupt rider — a turn either does the approval dance or the
plain echo. Where every other mode/rider *reacts* to stdin, this one *originates* a
request: on each `{"type":"user",…}` turn it calls `control.Approve` — the same
client `pyry mcp-approve` calls — directly against the daemon's control socket,
blocking until the daemon answers allow/deny, then reflects the verdict into its
assistant echo instead of parroting the prompt.

```go
if os.Getenv(envStreamApprove) != "" {
    runStreamJSONApprove(stdin, os.Stdout, os.Getenv(envApproveSocketFile))
    return
}
runStreamJSON(stdin, os.Stdout, os.Getenv(envStreamInterrupt) != "")
```

`runStreamJSONApprove` duplicates `runStreamJSON`'s ~15-line read loop rather than
widening its signature — same discipline as the stdin tee and startup hold: the
tested seam (`runStreamJSON`) stays byte-identical for the send/interrupt/queue
siblings.

**Socket-in-a-file.** The daemon's control socket is a random per-spawn path
(`shortSocketPath`), unknown before spawn and not derivable from the child's env or
cwd. `PYRY_FAKE_CLAUDE_APPROVE_SOCKET_FILE` carries a *file path* instead (known
pre-spawn, chosen by the test); the test writes the real socket path into that file
after `StartStreamInteractiveWithRelay` returns, and `dialApproval` reads it lazily,
at dial time — mirroring the existing `PYRY_FAKE_CLAUDE_*_TRIGGER` file idiom.

**Verdict reflection oracle.** `dialApproval` maps the `control.Approve` outcome to
one of three needles `writeVerdictResponse` writes into the assistant text (which the
daemon's stream parser turns into an `assistant_delta` the client observes):

| Outcome | Needle |
|---|---|
| daemon verdict `allow` | `approve-allow` |
| daemon verdict `deny` | `approve-deny` |
| `control.Approve` error (unreachable socket, ctx expiry, unrecognised `Behavior`) | `approve-error` (fail-closed, mirrors `mcp_approve.go`'s error→deny) |

`approve-error` is tagged **distinctly** from `approve-deny` so an e2e can tell a
genuine daemon deny (the fail-closed proof) from a client-side failure — the two
must never be confused, since a masked client error could otherwise false-pass a
timeout assertion. `approveDialTimeout` (30s, fixed) is deliberately far above the
daemon's approval window in the e2e's timeout case (`PYRY_APPROVAL_TIMEOUT=2s`), so a
no-answer turn's deny is always the **daemon's** `permbridge` timer firing, never a
fake self-timeout that would mask the daemon's verdict.

This rider is what a live `interactive_runner:"stream-json"` claude would do if the
runner wired `--permission-prompt-tool`/`--mcp-config` into the child spawn — which
it currently does not (`internal/streamsup.buildArgs` omits that pair; only the
`pyry agent-run` batch verb wires it, see [pyry-mcp-approve-command.md](pyry-mcp-approve-command.md)).
So fakeclaude calls `control.Approve` directly rather than spawning its own
`pyry mcp-approve`, exercising the identical daemon-side surface
(`mcp.approve` → `permbridge` → `streamApprovalBridge` → `modal_shown` → answer →
verdict) without depending on that still-open wiring gap. See
[codebase/1139.md](../codebase/1139.md) for the live e2e this feeds and the
production follow-up this gap is tracked under.

## Reject-absent-resume mode (#1631)

`PYRY_FAKE_CLAUDE_REJECT_ABSENT_RESUME` (a directory, default off) makes stream-json
mode observe the one claude behaviour the never-established-session crash-loop
depends on: real claude refuses `--resume <id>` when `<id>.jsonl` doesn't exist,
exiting 1 with `No conversation found with session ID: <id>` on both streams
(measured live — see [session-transcript-and-resume-probe.md](session-transcript-and-resume-probe.md)
§ #1656). Before this knob the fake always served a turn regardless of which id
flag won, so the fake-daemon tier had no way to observe the loop
`internal/streamsup`'s `useCreateForm` exists to close (see
[streamsup-package.md](streamsup-package.md) § `useCreateForm`).

Checked first thing inside the `envStreamJSON` branch — above the stdin tee and the
startup hold — so a refused spawn consumes no stdin and never reaches the trigger
machinery other modes rely on; a child that got that far would be in a state real
claude never reaches.

The predicate needs "was the *winning* id flag `--resume`?", which the existing
`argvSessionID(args) (string, bool)` can't answer — it drops which flag matched.
`argvSessionID` is now a two-line wrapper over a new `argvIDFlag(args) (id string,
resume bool, ok bool)`, which holds the shared last-occurrence-wins parse and stem
guard. **One copy** of that guard: it is the security-relevant half of the parse
(the value reaches `filepath.Join`, now at three call sites instead of two — this
mode's own `os.Stat(filepath.Join(dir, id+".jsonl"))`), and a duplicated guard is
what drifts out of step with its twin. `argvSessionID`'s existing table
(`TestArgvSessionID`) is unmodified and is the extraction's regression check.

Empty ⟹ off ⟹ every existing fake-daemon test is byte-identical — no test sets
this knob, and the check is stream-mode-only by construction.

## Argv-derived stem mode (#1195)

Every mode above binds its transcript stem to `PYRY_FAKE_CLAUDE_INITIAL_UUID` — a
single **process-wide** value, so every child of one daemon opens the *same*
`<uuid>.jsonl`. That is fine for a bootstrap-only e2e, but for a **minted**
per-conversation session the daemon tails `<convDir>/<mintedSessionID>.jsonl`
(`resolveBoundSessionJSONL`), and the two stems can never agree — the
subscription never opens, and conversation-scoped turn lifecycle
(`turn_state`/`turn_end`) was observable on **zero** PTY-tier tests. The fix
is not a new env value carrying the id (that's still process-wide); it's
reading the id the daemon already pinned this **specific** spawn to, off argv:
`sessions.buildSession` bakes `--session-id <pool id>` into a minted child's
argv, and `supervisor.buildClaudeArgs` appends `--session-id`/`--resume
<id>` to every bootstrap spawn too (#1164 picks the flag based on whether the
transcript already exists).

When `PYRY_FAKE_CLAUDE_SESSION_ID_FROM_ARGV` is non-empty, `main()` resolves
the initial stem via `argvSessionID(os.Args[1:])` before calling `openSession`,
falling back to `PYRY_FAKE_CLAUDE_INITIAL_UUID` on no match:

```go
// argvSessionID returns the value after the LAST "--session-id" or "--resume"
// in args, and whether it is present and safe to use as a filename stem.
func argvSessionID(args []string) (string, bool)
```

- **Both flags, last occurrence wins.** `--session-id` (create) and `--resume`
  (warm reattach, #1164) name the same stem; `buildClaudeArgs` always appends
  the flag last, so the spawn-time value beats anything a template
  contributed. Two-token form only — neither call site emits `--flag=value`.
- **Stem guard (security).** The returned value reaches `filepath.Join` in
  `openSession` and in the per-child JSONL trigger path below, so an unguarded
  value could steer a write outside the sessions dir. `argvSessionID` refuses
  anything empty or containing `/`, `\`, or `.` and reports not-found instead —
  a **laxer** predicate than the daemon's `transcript.ValidStem` (a full UUID
  regex), so it is not literally that function's mirror, but the no-separator/
  no-dot rule is enough on its own to make `filepath.Join` incapable of
  escaping the sessions dir. See [codebase/1195.md](../codebase/1195.md) for
  why the doc comment originally overstated this as a "mirror".
- **The flag's presence is NOT a minted/bootstrap discriminator.** Since #839
  the bootstrap spawn is pinned too, so a design that keyed the stem on "argv
  carries a session id" unconditionally would re-point *every* existing
  bootstrap child's transcript. Inertness comes from the explicit env knob,
  like every other mode here — with both effectively pinned in the harness's
  seeded-bootstrap setup (`seedBootstrapRegistry` makes the bootstrap pool id
  *equal* `PYRY_FAKE_CLAUDE_INITIAL_UUID`), the knob resolves to the same
  value the env would have given for the bootstrap child, byte-identical by
  construction.
- **Fallback, never fatal.** No flag, or a value the guard rejects, falls back
  to `mustEnv(envInitialUUID)` exactly as today — a legacy unpinned spawn keeps
  working, and a malformed value degrades to today's behaviour instead of
  writing somewhere unexpected. `rotateSession` is untouched: a later `/clear`
  still mints a fresh random UUID.
- **When unset, byte-identical to today**, including for a child whose argv
  carries `--session-id`/`--resume` (every bootstrap spawn does) — `os.Args`
  is never read at all.

Pinned by the untagged `TestArgvSessionID` (`argv_session_id_test.go`, no
build tag, mirroring `esc_detect_test.go`): both flags, last-occurrence-wins
in both orders, the unhandled `=` form, and every stem-guard rejection
(`../escape`, `a/b`, `a\b`, `x.jsonl`, empty).

## Per-child JSONL trigger mode (#1195)

The existing `PYRY_FAKE_CLAUDE_JSONL_TRIGGER` is a single **shared** path, and
its claim protocol (a fixed `<path>.consuming` sidecar) is sound only because
one fakeclaude process runs one poll goroutine — with a bootstrap child and a
minted child both polling the *same* path, whichever claims first appends the
line to *its own* transcript, a coin flip. `PYRY_FAKE_CLAUDE_JSONL_TRIGGER_DIR`
gives each child its own trigger instead: when set, fakeclaude also watches
`<dir>/<its own initial stem>.jsonl.trig` and, on each appearance, appends the
file's contents verbatim to its live transcript — the same
`emitStructuredJSONLIfTriggered(f, path)` consume as the shared trigger
(§ JSONL-trigger mode), called with a different `path`.

- **Path computed once, before the poll loop**, from the same stem
  `openSession` used — so per-child isolation is exactly as good as
  `SESSION_ID_FROM_ARGV`'s stem divergence. The two are typically set
  together but neither requires the other (a future bootstrap-only test could
  use this knob alone).
- **Watched independently of `PYRY_FAKE_CLAUDE_JSONL_TRIGGER`** — a separate
  `if` block in the poll loop; neither disables the other, and a test may set
  both.
- **Not one-shot.** The consume re-fires on every re-drop — needed by the
  #929 subscription-offset kicker: the structured producer subscribes at EOF
  after a settle delay, so a single-shot drop can land below the tailed range
  and never be seen; re-dropping the same line on a ticker until the drain
  observes it is the established fix (see [codebase/929.md](../codebase/929.md)).
- **When unset, no watch and byte-identical to today.**

Both knobs together are what makes `TestRelayV2_PerConversationTurnEnd`
(`internal/e2e/relay_v2_perconv_turn_end_test.go`, #1195) possible: mint a
conversation over the wire, route a turn to move the active cursor, then
re-drop an `end_turn` line at the minted child's own trigger path until the
daemon's turn stream emits a `turn_end` carrying that conversation's id — the
first PTY-tier proof that conversation-scoped turn lifecycle exists at all.
The daemon-side resolver (`resolveBoundSessionJSONL`) is untouched; the fake
moves to meet the daemon, never the reverse. Deliberately **not** fused into
one "every turn ends" knob: #1191 (blocked on this ticket) needs
`SESSION_ID_FROM_ARGV` alone, without an unconditional end-of-turn line that
would fabricate the very event its interrupt oracle must attribute to the
interrupt. See [codebase/1195.md](../codebase/1195.md) for the full design and
the on-disk non-vacuity assertion (the minted transcript must contain the
test's marker text; the bootstrap transcript must not).

## On-turn transcript growth (#673)

\#668 made the supervised-bootstrap delivery path confirm a turn by observing the
resolved claude session JSONL **grow** past a pre-delivery baseline
(`confirmViaTranscriptGrowth`): `WriteUserTurn` returns `nil` only on growth, else
`ErrTurnNotCommitted` after a 10 s timeout. Real claude appends the turn at commit
time, so growth is guaranteed in production — but fakeclaude only wrote `{}\n` at
session open / rotation and **never grew on a delivered turn**, so every e2e test
that drives a turn timed out. #673 closes that fidelity gap: a delivered turn now
grows the live session JSONL by one inert line, the same on-disk signal a committed
turn produces.

A user turn reaches fakeclaude as **stdin bytes** (supervisor `DeliverPrompt` → PTY
write), observed in `startStdinReader`. The fix grows `f` **while preserving the
single-writer-of-`f` invariant** (only the main goroutine writes `f`):

| Site | Goroutine | Action |
|---|---|---|
| `startStdinReader`, on `n > 0` | stdin reader | `turnPending.Store(true)` — **signal only, never touches `f`** |
| `main()` poll loop, each cycle | main | `if turnPending.Swap(false) { appendTurnGrowth(f) }` — `f.WriteString("{}\n")` + `f.Sync()`, best-effort |

- **Signal across the boundary, write on the owner.** `turnPending atomic.Bool` is
  the only added shared state; `Store`/`Swap` need no lock. All four `f` writers
  (`openSession`, `emitStructuredJSONLIfTriggered`, `appendTurnGrowth`, the rotation
  re-open) stay on the main goroutine — **no mutex on `f`**. This is the general
  shape for any future cross-goroutine fakeclaude trigger.
- **`Swap(false)` per poll cycle** collapses chunked stdin into one append per
  ~50 ms cycle (far inside the 10 s confirm timeout, ahead of the 150 ms confirm
  poll) and re-arms for a later turn. The grow always targets the **current** `f`
  (post-rotate, if a rotation fired earlier in the same iteration).
- **The inert `{}\n` is invisible to every assertion except "the file grew".** It is
  the exact line `openSession` writes; the turnbridge mapper maps an empty/typeless
  line to `(nil, false)` (`mapper.go:72-73`), so the v2/structured producer tails it
  and emits no event. Growth-confirm checks **size only** — no glyph, no new
  substrate, **no allowlist change** (the seal is untouched).
- **Best-effort write.** A failed `Write`/`Sync` is silenced (mirrors
  `emitStructuredJSONLIfTriggered`); the e2e asserts the ack downstream. A
  persistently-failing write surfaces as the daemon's loud `ErrTurnNotCommitted`,
  never a false ack.
- **Race-free vs the baseline.** The supervisor captures the baseline *after*
  `WaitReady` and *before* `deliver`; stdin bytes (hence any grow) arrive only
  *after* `deliver`, so a grow always lands strictly past the baseline.

**Blast radius is exactly the TUI tests.** The stdin reader runs only when
`logPath != "" || tui` (`main.go:692`), so the grow fires only there — the other e2e
callers (`StartRotation`, the fakeclaude primitive, attach-stdio) set neither and are
unperturbed.

**Sessions-dir alignment is still required** for a test to observe the growth. The
daemon's resolver has no env override — it always scans `<HOME>/.claude/projects/
encode(workdir)` — so a test that drives a turn must point `sessionsDir` at that
**computed** dir and pre-create `<initialUUID>.jsonl` before startup (the same
alignment the JSONL-trigger mode needs, above). A `t.TempDir()` subdir is a dir the
daemon never scans; the misalignment is *silent* on the resolver side (an
exist-but-empty computed dir resolves to `("", 0, nil)`, **no WARN**) and surfaces
only as the 10 s `ErrTurnNotCommitted`. See [codebase/673.md](../codebase/673.md) for
the five tests aligned by #673.

## Layout

```
internal/e2e/internal/fakeclaude/
  main.go        ~990 LOC, package main, no build tag (grew from the #122
                 rotation core with the #311/#323/#603/#642/#791/#792/#793/#794/#1004
                 optional modes, the #673 on-turn transcript growth, the #1140
                 stream-json mode, the #1136 interrupt rider, the #1137 stdin tee,
                 the #1138 startup hold, and the #1195 argv-derived stem +
                 per-child JSONL trigger modes)
  main_test.go   ~125 LOC, //go:build e2e
  modal_detect_test.go  untagged unit test for the modal-class detector
  esc_detect_test.go    ~60 LOC untagged unit test — TestContainsBareESC pins the
                        bare-ESC discriminator the #794 Esc-ends-turn mode relies on
  clear_detect_test.go  untagged unit test — TestContainsClearCommand pins the
                        /clear discriminator the #1004 clear-rotate mode relies on
  stream_detect_test.go  untagged unit test — drives runStreamJSON against
                        in-memory buffers and the real streamsup.Parser (#1140)
  argv_session_id_test.go  untagged unit test — TestArgvSessionID pins the
                        last-occurrence-wins argv parse + stem guard the #1195
                        argv-derived stem mode relies on
```

The `internal/e2e/internal/` nesting visibility-fences the binary so
only e2e-package code can import it. Since it's `package main` that's
mostly moot, but the path also signals intent to readers.

The binary itself has **no** build tag. A tiny `package main` is
essentially free to compile under `./...`, and not gating it means the
test (and the next-ticket harness) can `go build` it without passing
`-tags`.

## Verification — `TestFakeClaude_OpensInitialAndRotatesOnTrigger`

Single test under `//go:build e2e`. Drives the binary end to end **without
the e2e harness, without pyry**:

1. `go build` the binary into `t.TempDir()`.
2. `exec.Command(binPath)` with the three env vars set.
3. Poll (50ms gap, 3s deadline) until the initial JSONL appears.
4. `os.WriteFile(trigger, nil, 0o600)`.
5. Poll (50ms gap, 3s deadline) until **both** post-conditions hold: a
   fresh `<uuid>.jsonl` whose stem matches `uuidStemPattern` (and isn't
   the initial UUID) has appeared **and** the trigger file has been
   removed. fakeclaude opens the rotated file *before* removing the
   trigger (§ *What It Does* table), so breaking on the JSONL alone races
   the still-present trigger — folding the trigger-gone check into the
   break condition closes that flake (#584, see
   [codebase/584.md](../codebase/584.md)).
6. Assert the trigger file is gone (now only a timeout diagnostic — the
   poll loop already waited for it, so this fires only on a genuine
   rotation regression, not the pre-#584 race).
7. `cmd.Process.Signal(SIGTERM)`; assert `WaitStatus.Signaled() &&
   Signal()==SIGTERM` within 3s, escalate to SIGKILL on grace expiry.

Hermetic: no network, no writes outside the test's tmp dir. A defensive
`t.Cleanup` SIGKILLs the binary if anything fails before the explicit
SIGTERM phase — without it, a leaked process would survive the test and
hold an fd in the now-deleted tmp dir.

### What the test does NOT verify

- **Strict close-OLD-before-open-NEW order.** Not directly observable —
  catching it would require attaching `lsof` mid-rotation, which races
  the 50ms poll. The order is enforced by code review of `main.go` (one
  `f.Close()` line, then one `openSession`); the consumer ticket's
  end-to-end driver against the real probe is what proves it works in
  anger.
- **Multiple rotations.** A second trigger is ignored by the `rotated`
  guard; not exercised by the test in this slice. If the harness
  consumer ever wants multiple rotations, that's a future spec change —
  not a "make it general now" exercise.
- **JSONL content beyond non-emptiness.** The bytes don't matter to the
  rotation watcher; only the file's existence on the PID's fd table.
- **Cross-platform fork.** `/proc` vs `lsof` is irrelevant here because
  no probe runs in this test — the test asserts directory state, not
  watcher state.

## Why no signal handler

Go's runtime default kills the process on SIGTERM with no goroutine
wind-down. The OS auto-closes the open fd on process death. The next
slice's harness consumer needs the binary to die when pyry SIGTERMs the
PTY child during teardown; default behaviour suffices.

## Why no `internal/sessions` import

The hand-rolled `uuidV4()` duplicates `sessions.NewID` (~8 lines).
Importing `sessions` from a test-only `package main` under
`internal/e2e/internal/` is technically allowed by Go's visibility rules
but pulls a chunk of production surface into a test binary for one
function. Inline copy is the simpler call.

## Error posture

Any failure (`os.OpenFile`, `Write`, `Sync`, missing env var,
`crypto/rand`) prints to stderr and `os.Exit(1)`. There is no recovery
path — a fake claude that can't open its sessions file is a bug in the
test setup, and exit-1 surfaces it loudly to whoever spawned the
binary. `os.Remove(trigger)` and `f.Close()` errors are deliberately
ignored: the OS will reclaim the fd, and a missing trigger file at
remove time would be a benign concurrent-removal race that doesn't
affect correctness.

## Related

- Spec: `docs/specs/architecture/122-fake-claude-test-binary.md`;
  TUI mode: `docs/specs/architecture/603-fakeclaude-tui-idle-thinking-glyphs.md`;
  JSONL-trigger mode: `docs/specs/architecture/642-structured-receive-two-phone-e2e-capstone.md`;
  idle-trigger mode: `docs/specs/architecture/792-queue-drain-two-phone-e2e-capstone.md`;
  Esc-ends-turn mode: `docs/specs/architecture/794-interrupt-stops-turn-two-phone-e2e-capstone.md`;
  modal-clear-on-answer mode: `docs/specs/architecture/793-two-head-first-answer-wins-e2e-capstone.md`;
  on-turn growth: `docs/specs/architecture/673-fakeclaude-transcript-growth.md`;
  clear-rotate mode: `docs/specs/architecture/1004-new-session-e2e.md`;
  stream-json mode: `docs/specs/architecture/1140-fakeclaude-stream-json-mode.md`;
  interrupt rider: `docs/specs/architecture/1136-stream-e2e-interrupt.md`;
  new_session rider: `docs/specs/architecture/1137-stream-new-session-rotation-e2e.md`;
  startup-hold rider: `docs/specs/architecture/1138-stream-e2e-queue-drain-in-order.md`;
  approve rider: `docs/specs/architecture/1139-stream-e2e-permission-round-trip.md`;
  argv-derived stem + per-child JSONL trigger modes:
  `docs/specs/architecture/1195-minted-perconv-pty-transcript-substrate.md`
- TUI mode per-ticket notes: [codebase/603.md](../codebase/603.md) (glyph
  emission, the ack-pollution drain, the substrate-guard exemption)
- JSONL-trigger per-ticket notes: [codebase/642.md](../codebase/642.md) (the
  structured-receive capstone it feeds, the sessions-dir alignment +
  pre-create-JSONL preconditions, the cold-start producer-subscribe race)
- On-turn growth per-ticket notes: [codebase/673.md](../codebase/673.md) (the
  cross-goroutine `turnPending` signal, the #668 commit-confirm it satisfies, the
  five `sessionsDir` alignments, the six broken tests)
- Idle-trigger per-ticket notes: [codebase/792.md](../codebase/792.md) (the live
  queue-drain capstone this mode feeds — the busy→free window, the empty-`queue_state`
  happens-after fence, and the ordered vacuous-pass guards)
- Esc-ends-turn per-ticket notes: [codebase/794.md](../codebase/794.md) (the live
  interrupt capstone this mode feeds — the Esc-drives-the-flip structural causality,
  the bare-ESC discriminator vs paste markers, the two-oracle belt-and-suspenders)
- Modal-clear-on-answer per-ticket notes: [codebase/793.md](../codebase/793.md) (the live
  two-head first-answer-wins capstone this mode feeds — the keystroke-is-the-cause
  structural causality, the local `pyry attach` head bound before the modal is raised, the
  two ordered observe-positives, the `dismissed_local` live audit oracle)
- Clear-rotate per-ticket notes: [codebase/1004.md](../codebase/1004.md) (the live
  `new_session` e2e this mode feeds — the never-created file-trigger structural-causality
  guard, and the bounded re-send loop that makes the fire-and-forget verb deterministic)
- Stream-json mode per-ticket notes: [codebase/1140.md](../codebase/1140.md) (the
  gate-above-mustEnv structural AC satisfier, the echo-the-prompt response, the
  different-fabric real-`streamsup.Parser` verification); feeds the sibling #1135
  harness + `send_message` e2e spec (blocked-by this ticket)
- Interrupt rider per-ticket notes: [codebase/1136.md](../codebase/1136.md) (the
  withheld-result in-flight-turn trick, the `error_during_execution` →
  `TurnEndReasonCancelled` mapping, the minted-conversation live routing-target
  proof it feeds)
- New_session rider per-ticket notes: [codebase/1137.md](../codebase/1137.md) (the
  stream-path stdin tee, the on-disk-rotation-implies-`RestartFresh` reasoning, the
  post-rotation drain divergence it confirms live and defers to #1133)
- Startup-hold rider per-ticket notes: [codebase/1138.md](../codebase/1138.md) (the
  live queue-drain-in-order e2e this mode feeds — why a msgqueue backlog isn't
  observable on the stream path, and the FIFO stdin-pipe chain proof instead)
- Argv-derived stem + per-child JSONL trigger per-ticket notes:
  [codebase/1195.md](../codebase/1195.md) (the minted per-conversation
  turn-lifecycle PTY e2e these modes feed, the stem guard vs the daemon's
  `transcript.ValidStem`, the on-disk non-vacuity assertion pair, why the two
  knobs stay separate for #1191)
- Substrate seal: `cmd/substrate-guard/main.go` allowlists this file
  alongside `internal/agentrun/ptyrunner/helper_test.go` (the two sanctioned
  fake-claude helpers that emit claude-TUI glyphs)
- Mirrors: `internal/sessions/id.go` (`NewID` UUIDv4 generator),
  `internal/sessions/rotation/watcher.go:17-19` (`uuidStemPattern`)
- Lessons: `docs/lessons.md § Claude session storage on disk` (the
  on-disk shape the binary mimics)
- Consumers: `Harness.StartRotation` + `ensureFakeClaudeBuilt` (#123,
  landed — wires the binary into the e2e harness as the supervised child;
  see [e2e-harness.md § Rotation Primitive](e2e-harness.md)).
  Forthcoming: rotation-watcher driver test (slice after #123 — runs
  pyry's watcher against the binary).
- Pattern: always-split "new package AND its first consumer" — the
  binary lands here without its harness consumer to keep the AC count
  inside the per-ticket budget. Same shape as the introduce-then-rewire
  slicing pattern (#28 → #29).
