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
`end_turn` line so the Esc *causes* the turn to stop, likewise
extend the binary past pure rotation; see [§ Configuration](#configuration--env),
[§ JSONL-trigger mode](#jsonl-trigger-mode-642),
[§ Idle-trigger mode](#idle-trigger-mode-792), and
[§ Esc-ends-turn mode](#esc-ends-turn-mode-794). Whenever the stdin reader is
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
```

No flags, no positional args — env is the entire configuration surface,
matching how the harness consumer configures the child via `cmd.Env`.
fakeclaude reads stdin only when `STDIN_LOG`, `TUI`, `MODAL_TRIGGER`, or
`ESC_ENDS_TURN` is set; otherwise it ignores stdin entirely.

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
  main.go        ~415 LOC, package main, no build tag (grew from the #122
                 rotation core with the #311/#323/#603/#642/#792/#794 optional
                 modes and the #673 on-turn transcript growth)
  main_test.go   ~125 LOC, //go:build e2e
  modal_detect_test.go  untagged unit test for the modal-class detector
  esc_detect_test.go    ~60 LOC untagged unit test — TestContainsBareESC pins the
                        bare-ESC discriminator the #794 Esc-ends-turn mode relies on
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
  on-turn growth: `docs/specs/architecture/673-fakeclaude-transcript-growth.md`
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
