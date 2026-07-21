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
```

No flags, no positional args — env is the entire configuration surface,
matching how the harness consumer configures the child via `cmd.Env`.
fakeclaude reads stdin only when `STDIN_LOG`, `TUI`, `MODAL_TRIGGER`,
`ESC_ENDS_TURN`, `MODAL_CLEAR_ON_ANSWER`, `CLEAR_ROTATES`, or `STREAM_JSON` is set;
otherwise it ignores stdin entirely. `STREAM_JSON` never inspects `os.Args` either —
the daemon's injected `--input-format`/`--output-format`/`--verbose`/`--session-id`/
`--resume` flags are silently tolerated by construction, not parsed.

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
  `main.go:149`). The #792 consumer sets `PYRY_FAKE_CLAUDE_STDIN_LOG` (via
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
and appends one canned `end_turn` line to the live session JSONL, so the daemon's
structured-turn producer maps it to a `turn_end` — making the Esc the **cause** of
the turn ending. (Reusing #792's *file*-driven busy→idle flip would instead let an
"interrupt stopped the turn" assertion pass **vacuously** — the idle would come
from a file, not the Esc.)

When `PYRY_FAKE_CLAUDE_ESC_ENDS_TURN` is set:

| Moment | Action | Effect |
|---|---|---|
| startup | `enterRawMode()` (like modal mode) | a lone ESC with no line terminator reaches `read()` verbatim (canonical discipline would otherwise withhold it) |
| stdin read containing a bare ESC | `containsBareESC(buf)` → set `escPending` (signal only) | stdin reader flags the interrupt without touching `f` |
| main poll loop, `escPending.Swap(true)`, one-shot `escEnded` | `appendTurnEnd(f)`: write `interruptEndTurnLine` + `f.Sync()` | producer tails it → `EventKindJsonlEndOfTurn` → `TurnEnd` → `turn_end` to the phone |

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
- **`interruptEndTurnLine` is a fixed literal** —
  `{"type":"assistant","message":{"id":"interrupt-end","stop_reason":"end_turn",
  "content":[{"type":"text","text":"[interrupted]"}]}}` — the exact shape
  `turnbridge`'s mapper needs for `EventKindJsonlEndOfTurn` (assistant +
  `stop_reason=="end_turn"` + non-empty text). It is inert JSONL data, **not** a TUI
  substrate glyph, so the `cmd/substrate-guard` allowlist is unchanged.
- **One-shot.** The `escEnded` gate bounds the append to one end-of-turn line; a
  second ESC is inert — a re-interrupt of an already-ended turn is a no-op, matching
  claude.
- **Coexists with `PYRY_FAKE_CLAUDE_TUI`** (unlike idle-trigger's mutual exclusion).
  The two touch different bytes: TUI emits the startup `❯` + one spinner; the ESC
  detector scans for the bare ESC. The #794 capstone runs both ON. **When unset,
  byte-identical to today** — every existing caller is unperturbed.

The turn_end carries `StopReason == "end_turn"`, **not** `"cancelled"`: tui-driver
v1.3.0's `EventKindJsonlEndOfTurn` cannot distinguish an interrupt-stop from a
normal end (`turnbridge/mapper.go:25-28`), so the mode proves **causality** (the
`turn_end` exists only because the Esc was received), not stop-reason semantics.
See [codebase/794.md](../codebase/794.md) for the live interrupt capstone this mode
feeds (the structural-causality guard, the two ordered `t.Fatal`s, the two-oracle
belt-and-suspenders).

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
- **Default mode still ignores every non-`"user"` line**, including a
  `control_request` — responding only to user turns with `assistant`+`result(success)`.
  new_session / queue / modal remain out of scope for the fake. Interrupt is now
  handled by the `honorInterrupt` rider, below (#1136).
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
| `{"type":"control_request",…"subtype":"interrupt"}` | ignored | `writeInterruptedResult` — `result{subtype:"error_during_execution"}` |

`writeStreamResponse` was split so the assistant-echo half is independently
reusable: `writeAssistantEcho(w, msgID, text)` writes just the assistant line;
`writeStreamResponse` is now `writeAssistantEcho` + the `result{success}` line,
byte-identical to its pre-#1136 output. `interruptControlRequest(line []byte) bool`
decodes a minimal `{type, request.subtype}` mirror of
`streamsup.controlRequest`/`marshalInterruptEnvelope` (`internal/streamsup/envelope.go`)
— the exact shape the daemon writes to the child's stdin on a phone interrupt — and
returns `false` (line ignored) on anything that isn't
`type=="control_request" && request.subtype=="interrupt"`, preserving the same
per-line decode resilience as `userTurnText`.

The mode is **stateless**: it emits `result{error_during_execution}` on *every*
interrupt `control_request` it sees, with no in-flight-turn tracking. A caller that
drives exactly one interrupt per in-flight turn (the only shape #1136's e2e needs)
gets the right behaviour for free; nothing polices "interrupt with no turn open".

`error_during_execution` is not an arbitrary error subtype — `streamsup.Parser`'s
`resultTurnEndReason` (`internal/streamsup/parser.go`) maps that one subtype to
`turnevent.TurnEndReasonCancelled` and every other subtype to `end_turn`, so this is
the specific value that makes the daemon report the turn as interrupted rather than
merely errored. See [codebase/1136.md](../codebase/1136.md).

### Stream-path stdin tee (`PYRY_FAKE_CLAUDE_STDIN_LOG`, #1137)

`PYRY_FAKE_CLAUDE_STDIN_LOG` already existed for the PTY path (`startStdinReader`,
above). #1137 extends the *same* env var to the stream-json branch: when set, the
stream dispatch wraps `os.Stdin` in `io.TeeReader(os.Stdin, syncWriter{f})` before
calling `runStreamJSON`, so every byte the daemon writes to the child's stdin — user-
turn envelopes, interrupt `control_request`s — is appended to the log. `syncWriter`
is a tiny `io.Writer` (`Write` → `f.Write` → `f.Sync`) mirroring the PTY reader's
per-write `Sync`; the file is opened with the same flags
(`O_WRONLY|O_APPEND|O_CREATE, 0o600`) so a bootstrap child and a later fresh
post-rotation child (after a stream `new_session`) both accumulate into one log.

The tee lives at the `main()` call site, not inside `runStreamJSON` — the function
keeps its pure `(io.Reader, io.Writer, bool) ` I/O seam, so the #1140 unit test and
the #1136 interrupt rider are byte-identical whether or not this ticket's change
exists. With the env unset (every caller except #1137's own test) the branch is
exactly `runStreamJSON(os.Stdin, os.Stdout, honorInterrupt)` — unchanged.

This is the runtime oracle for proving a stream `new_session` never types `/clear`
to the child: on the stream path `new_session` is a process re-spawn
(`(*streamsup.Runner).RestartFresh`, #1124), never a keystroke, so the log should
contain a user-turn marker (non-vacuity) but never the substring `/clear`. See
[codebase/1137.md](../codebase/1137.md).

## On-turn transcript growth (#673)

#668 made the supervised-bootstrap delivery path confirm a turn by observing the
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
`logPath != "" || tui` (`main.go:129`), so the grow fires only there — the other e2e
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
  main.go        ~830 LOC, package main, no build tag (grew from the #122
                 rotation core with the #311/#323/#603/#642/#791/#792/#793/#794/#1004
                 optional modes, the #673 on-turn transcript growth, the #1140
                 stream-json mode, and the #1136 interrupt rider)
  main_test.go   ~125 LOC, //go:build e2e
  modal_detect_test.go  untagged unit test for the modal-class detector
  esc_detect_test.go    ~60 LOC untagged unit test — TestContainsBareESC pins the
                        bare-ESC discriminator the #794 Esc-ends-turn mode relies on
  clear_detect_test.go  untagged unit test — TestContainsClearCommand pins the
                        /clear discriminator the #1004 clear-rotate mode relies on
  stream_detect_test.go  untagged unit test — drives runStreamJSON against
                        in-memory buffers and the real streamsup.Parser (#1140)
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
  new_session rider: `docs/specs/architecture/1137-stream-new-session-rotation-e2e.md`
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
