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
`ASSISTANT_TRIGGER` (#311) modes likewise extend the binary past pure
rotation; see [§ Configuration](#configuration--env).

| Step | Effect |
|---|---|
| Start | open `<dir>/<initialUUID>.jsonl` `O_WRONLY\|O_APPEND\|O_CREATE 0o600`, `WriteString("{}\n")`, `Sync()` |
| Idle | poll trigger file every 50ms |
| Trigger | `f.Close()` (OLD), mint `uuidV4()`, open `<dir>/<newU>.jsonl` (NEW), write+fsync, `os.Remove(trigger)`, set `rotated=true` |
| Idle (post-rotation) | poll continues; trigger reappearance ignored |
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
PYRY_FAKE_CLAUDE_TUI                when non-empty, emit the idle/thinking
                                    glyphs (#603; see § TUI mode)
```

No flags, no positional args — env is the entire configuration surface,
matching how the harness consumer configures the child via `cmd.Env`.
fakeclaude reads stdin only when `STDIN_LOG` or `TUI` is set; otherwise it
ignores stdin entirely.

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
TUI-off callers (`StartRotation`, the rotation tests, the two-phone-coarse
e2e) are unperturbed. Because TUI mode makes `main.go` carry the two
glyphs, the file is on the `cmd/substrate-guard` allowlist (#603),
mirroring the sanctioned `internal/agentrun/ptyrunner/helper_test.go`
exemption. Consumers must drain the spinner `message` envelope that the
assistant-turn emitter fans to the phone (it races the ack); see
[codebase/603.md](../codebase/603.md) for the drain pattern.

The initial UUID must satisfy
`internal/sessions/rotation/watcher.go:19`'s `uuidStemPattern`
(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).
The fresh UUID minted on rotation is generated by the inline `uuidV4()`
helper, which mirrors `internal/sessions/id.go` byte-for-byte (same
`crypto/rand` + version/variant fixup).

## Layout

```
internal/e2e/internal/fakeclaude/
  main.go        ~240 LOC, package main, no build tag (grew from the
                 #122 rotation core with the #311/#323/#603 optional modes)
  main_test.go   ~125 LOC, //go:build e2e
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
5. Poll (50ms gap, 3s deadline) until a fresh `<uuid>.jsonl` appears in
   the same directory whose stem matches `uuidStemPattern` and is not
   the initial UUID.
6. Assert the trigger file is gone.
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
  TUI mode: `docs/specs/architecture/603-fakeclaude-tui-idle-thinking-glyphs.md`
- TUI mode per-ticket notes: [codebase/603.md](../codebase/603.md) (glyph
  emission, the ack-pollution drain, the substrate-guard exemption)
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
