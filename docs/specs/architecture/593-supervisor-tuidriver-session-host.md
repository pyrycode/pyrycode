# #593 — Supervisor hosts claude via `tuidriver.Spawn`; `Session` owned across the restart loop

Phase 5 / Phase 1 task **T2** of [ADR 025](../../knowledge/decisions/025-mobile-remote-head-interactive-session.md). Migrate `internal/supervisor` from a raw `pty.Start` + `io.Copy` host loop to hosting claude through a tui-driver `Session`. **Behaviour-preserving** hosting swap: the conversation cursor, restart survival, live terminal resize, and every raw-byte consumer must keep working unchanged. Reliable turn delivery (#594), attach rewire + two-heads (#595), and structured streaming (#596) build on the supervisor-owned `Session` this ticket introduces — but none of that is pulled forward here.

## Files to read first

Code (use codegraph for symbols; the module-cache paths are read via `Read`):

- `internal/supervisor/supervisor.go:106-243` — `Supervisor` struct + the cursor/readiness machinery you re-key: `convMu`/`currentConvID`, `ptmxMu`/`ptmx *os.File`/`ptmxReadyCh`, `WriteUserTurn`, `setPTY`, `WaitForPTY`, `CurrentConversation`. The `ptmx *os.File` field and its helpers become a `*tuidriver.Session`.
- `internal/supervisor/supervisor.go:354-481` — `runOnce`, the host loop. Two branches (bridge mode at 371-414, foreground at 416-480). This is the bulk of the rewrite.
- `internal/supervisor/bridge.go:49-71` + `261-290` — `Bridge`'s `ptyMu`/`ptmx *os.File`, `SetPTY`, `Resize`. The `*os.File` resize coupling is replaced by a `resizer` delegate. **`Bridge.Write` / `SetOutputObserver` / `Attach` (153-259) are NOT touched** — they stay the raw-byte fan-out hub; only their byte feed changes.
- `internal/supervisor/winsize.go` (whole, 57 lines) — `watchWindowSize` + `resizeOnce`. Change the resize target from `pty.Setsize(ptmx,…)` to `sess.Resize(…)`; keep reading the *operator's own* terminal size via `pty.GetsizeFull(os.Stdin)`.
- `internal/agentrun/ptyrunner/runner.go:288-294` + `364-380` — the in-repo precedent for `Spawn` + `defer sess.Close()` + cmd construction. **Note the deliberate divergence below:** this ticket does NOT call `EnsureClaudeEnv` (ptyrunner does).
- tui-driver module cache (`go list -m -f '{{.Dir}}' github.com/pyrycode/tui-driver` → append `/pkg/tuidriver/…`):
  - `session.go:49-235` — `SpawnOpts.MirrorOutput`, `Spawn`, the reader goroutine (drop-newest mirror, sole sender/closer of `mirrorOut`), `Session` fields. **The PTY `*os.File` is private — there is no accessor.** This is *why* `Bridge.SetPTY(*os.File)` cannot survive.
  - `session.go:382-450` — `MirrorOutput() <-chan []byte` (buffer 256, closed by the reader on exit/Close), `Wait()` (blocks until exited **and** reader drained), `Close()` (SIGTERM→grace→SIGKILL, close PTY, join reader; idempotent).
  - `keys.go:77-94` — `AttachInput([]byte) error`, the production raw-input seam `WriteUserTurn` now writes through (replaces `ptmx.Write`).
  - `pty.go:120-155` — `StartPTY` (40×120 default size) and `Session.Resize(rows, cols uint16) error` (the resize delegate; callable the instant `Spawn` returns).
- `internal/supervisor/supervisor_test.go:142-254` — `TestHelperProcess` + `helperConfig`. The fake-child harness your new/updated tests reuse; `stdin_to_file` mode (179-198) is how the WriteUserTurn happy path verifies bytes reach claude's stdin.
- `internal/supervisor/supervisor_test.go:455-735` — the `WriteUserTurn_*` and `WaitForPTY_*` sets. The `WaitForPTY_*` tests call `sup.setPTY(f)` with `/dev/null`; these become `setSession(...)` call-site swaps.
- `internal/supervisor/bridge_test.go:240-302` — `TestBridge_Resize*`. Currently open a real `pty.Open` and assert `pty.Getsize`; they move to a `fakeResizer` double.
- `internal/sessions/session.go:109-226` — `WriteUserTurn` / `Resize` / `Activate` (→ `WaitForPTY`) delegations. **Confirm these keep compiling unchanged** — they are the proof that the public method signatures must not move.
- `docs/lessons.md` § *"`SetPTY(nil)` must run BEFORE `EndIteration`"* — the EBADF ordering rule. Preserved here as **`SetResizer(nil)` before `sess.Close()`** (see Concurrency).
- [ADR 007](../../knowledge/decisions/007-bridge-iteration-boundaries.md) (iteration boundaries) + [ADR 008](../../knowledge/decisions/008-bridge-resize-seam.md) (why `Resize` lives on `*Bridge`). The resize seam stays on `*Bridge`; only its target type changes.

Do **not** touch: `internal/supervisor/spawn.go` + `spawn_test.go`. `SpawnPTY`/`SpawnConfig` is a standalone helper with **zero production callers** (test-only, verified via codegraph + grep) — it is not part of the host loop, so AC #1 does not reach it. Leaving it untouched keeps the diff to the host loop.

## Context

Today the supervisor hosts claude with `pty.Start(cmd)` + two `io.Copy` pumps + a raw `WriteUserTurn` that calls `ptmx.Write`. The PTY master `*os.File` is held in three places: the supervisor (`ptmx`, for `WriteUserTurn`), the `Bridge` (`ptmx`, for `Resize`), and `winsize.go` (for SIGWINCH `Setsize`).

tui-driver's `Session` (v1.2.0, pinned) seals the PTY file privately and exposes exactly the seams this swap needs: `Spawn(cmd, SpawnOpts{MirrorOutput:true})` to host, `MirrorOutput()` for the raw-output stream, `AttachInput()` for raw input, `Resize()` for live geometry, and `Wait()`/`Close()` for the lifecycle. The two upstream preconditions (mirror surface #136 → v1.1.0; resize seam #138 → v1.2.0) are released and pinned.

The migration's load-bearing insight: **`Bridge.Write` already fans raw output bytes to *both* surviving consumers** — the `pyry attach` client (`b.output`) and the assistant-turn bridge (`b.outputObserver`, consumed by `cmd/pyry/assistant_turn.go` + `assistant_turn_v2.go`). So AC #4 is satisfied by *changing only the byte feed*: replace `io.Copy(bridge, ptmx)` with a loop forwarding `sess.MirrorOutput()` into `bridge.Write`. Neither consumer, nor `Bridge.Write`/`SetOutputObserver`, changes.

## Design

Three production files change: `supervisor.go`, `bridge.go`, `winsize.go`. No new files, no new exported types (the one new interface is unexported), no cross-package edits.

### 1. Supervisor: re-key the held PTY to a `*Session`

In the `Supervisor` struct (`supervisor.go:107-131`), replace the per-iteration PTY handle with the per-iteration session handle. Field renames (honest names; the readiness choreography is byte-identical):

| Before | After |
|---|---|
| `ptmxMu sync.Mutex` | `sessMu sync.Mutex` |
| `ptmx *os.File` | `sess *tuidriver.Session` |
| `ptmxReadyCh chan struct{}` | `sessReadyCh chan struct{}` |
| `setPTY(f *os.File)` | `setSession(sess *tuidriver.Session)` |

`New` initialises `sessReadyCh` (rename only). The readiness-channel choreography in `setSession` is **unchanged logic** — close on non-nil register, freshen (re-make) on nil clear; `WaitForPTY` still captures the channel under `sessMu` then awaits it unlocked.

**Public method names and signatures do NOT move** — `WaitForPTY`, `WriteUserTurn`, `CurrentConversation` keep their names (consumers in `internal/sessions`, `internal/relay/handlers`, `cmd/pyry` are untouched). Update only the doc-comments to say "Session is live" rather than "PTY bound."

`WriteUserTurn` changes one line: the write seam. Contract is otherwise identical (validate → stamp cursor under `convMu` → write or drop-when-none → wrap PTY errors with the `"supervisor: write user turn:"` prefix):

```
// was: if s.ptmx == nil { return nil }; _, err := s.ptmx.Write(payload)
// now: if s.sess == nil { return nil }; err := s.sess.AttachInput(payload)
```

`AttachInput` is the sanctioned production raw-input path and is `writeRaw` → `pty.Write` internally — byte-for-byte the old behaviour. The `DeliverPrompt` rewrite (bracketed paste + commit-confirm) is **#594; do not pull it forward.**

### 2. `runOnce`: host through the `Session` in both modes

Common preamble (replaces `pty.Start` at 361-369):

```
cmd := exec.CommandContext(ctx, s.cfg.ClaudeBin, args...)   // unchanged
cmd.Dir  = …                                                 // unchanged
cmd.Env  = append(os.Environ(), s.cfg.helperEnv...)          // unchanged — see "Env" below
sess, err := tuidriver.Spawn(cmd, tuidriver.SpawnOpts{MirrorOutput: true})
if err != nil { return fmt.Errorf("spawn: %w", err) }
if onSpawn != nil && cmd.Process != nil { onSpawn(cmd.Process.Pid) }  // cmd.Process set by Spawn
```

`MirrorOutput: true` is **required in both modes** — it is the only output path now (the sealed Session has no `*os.File` to `io.Copy` from). Foreground forwards the stream to `os.Stdout`; bridge mode forwards it into `bridge.Write`.

**Env:** keep `cmd.Env = append(os.Environ(), helperEnv...)` exactly. **Do NOT call `EnsureClaudeEnv`** — it force-overrides `TERM=xterm-256color`, which would change claude's TUI rendering versus today (foreground inherits the operator's real `TERM`). `EnsureClaudeEnv` exists for downstream *screen parsing*, which #593 does not do (that is #596). This is a behaviour-preservation decision; recorded in Open Questions for #596 to revisit.

**Two helpers** make the pumps read like the old `io.Copy` calls:

- An unexported `sessionWriter struct{ sess *tuidriver.Session }` implementing `io.Writer`, whose `Write(p)` calls `sess.AttachInput(p)` and returns `len(p), nil` on success (so `io.Copy` keeps draining). ~5 lines. This lets the input pump stay `io.Copy(sessionWriter{sess}, src)`, mirroring the old `io.Copy(ptmx, src)`.
- The output pump is a `for chunk := range sess.MirrorOutput() { dst.Write(chunk) }` loop in a goroutine, joined via a `done` channel (see Concurrency). It replaces `io.Copy(dst, ptmx)`.

**Bridge mode** (rewrite of 371-414). Structure preserved; substitutions only:

| Step | Before | After |
|---|---|---|
| register | `Bridge.SetPTY(ptmx)`; `setPTY(ptmx)` | `Bridge.SetResizer(sess)`; `setSession(sess)` |
| input pump | `io.Copy(ptmx, bridge)` | `io.Copy(sessionWriter{sess}, bridge)` |
| output pump | `io.Copy(bridge, ptmx)` | range `sess.MirrorOutput()` → `bridge.Write` |
| wait | `cmd.Wait()` | `sess.Wait()` |
| teardown | `setPTY(nil)`; `ptmx.Close()`; `Bridge.SetPTY(nil)`; `EndIteration()` | `setSession(nil)`; `Bridge.SetResizer(nil)`; `EndIteration()`; `sess.Close()` |

`BeginIteration()` still brackets the start. Ordering of teardown is load-bearing (Concurrency).

**Foreground mode** (rewrite of 416-480). Unchanged: raw-mode setup (`term.MakeRaw`), `openTTYInput`/`stdinFallback`, the `/dev/tty` close-to-unblock pattern. Substitutions:

| Step | Before | After |
|---|---|---|
| register | `setPTY(ptmx)` | `setSession(sess)` |
| resize watcher | `s.watchWindowSize(ptmx)` | `s.watchWindowSize(sess)` |
| input pump | `io.Copy(ptmx, input)` | `io.Copy(sessionWriter{sess}, input)` |
| output pump | `io.Copy(os.Stdout, ptmx)` | range `sess.MirrorOutput()` → `os.Stdout` |
| wait | `cmd.Wait()` | `sess.Wait()` |
| teardown | `setPTY(nil)`; `ptmx.Close()`; `input.Close()` | `setSession(nil)`; `input.Close()`; `sess.Close()` |

Both modes `return waitErr` (the `sess.Wait()` result) so `Run`'s switch + backoff classify exits exactly as today. Ctx-cancel still flows through `exec.CommandContext` (default kill) → `sess.Wait()` returns → loop's backoff `select` returns `ctx.Err()`. **Do not add `cmd.Cancel`** — that would change shutdown from the current SIGKILL-on-cancel to SIGTERM (and pull in ptyrunner's reaping concern). `GracefulShutdown` semantics are preserved.

### 3. `Bridge`: `*os.File` resize coupling → `resizer` delegate

Define the consumer-side interface in `bridge.go` (small, single-method, satisfied by `*tuidriver.Session`):

```
type resizer interface { Resize(rows, cols uint16) error }
```

Changes:
- Field `ptmx *os.File` → `rs resizer` (keep the `ptyMu` leaf-lock and its name/doc; it still guards the per-iteration registration).
- `SetPTY(f *os.File)` → `SetResizer(r resizer)`. Register/clear under `ptyMu`, same shape. Callers must pass an **untyped `nil`** to clear (runOnce does: `SetResizer(sess)` / `SetResizer(nil)`); document the typed-nil footgun.
- `Resize(rows, cols)`: `if b.rs == nil { return nil }; return b.rs.Resize(rows, cols)`. The silent-nil-no-op contract (between iterations / foreground) is preserved; the delegate already wraps its own error (`tuidriver: resize …`), so return it directly. The rows-then-cols order and the wire-side cols↔rows swap at `handleAttach` are unchanged.
- Drop the `os` and `github.com/creack/pty` imports from `bridge.go`.

`Session.Resize` returns an error on a closed FD instead of operating on a dangling `*os.File` — so the old EBADF race (lessons.md) is *structurally* defused; the ordering rule is still kept for clarity (Concurrency).

### 4. `winsize.go`: SIGWINCH → `sess.Resize`

- `watchWindowSize(ptmx *os.File) func()` → `watchWindowSize(sess *tuidriver.Session) func()`.
- `resizeOnce(ptmx *os.File)` → `resizeOnce(sess *tuidriver.Session)`: keep `term.IsTerminal(os.Stdin)` guard and the GC-finalizer-safe `pty.GetsizeFull(os.Stdin)` read of the *operator's* terminal (that reads the supervisor's own stdin, not the child — legitimate, unrelated to "hosting"); then `_ = sess.Resize(size.Rows, size.Cols)` instead of `pty.Setsize(ptmx, size)`. Errors stay ignored (best-effort), so a resize racing `sess.Close()` is harmless.
- Keep the `pty` (for `GetsizeFull`) and `term` imports.

The foreground prime (`resizeOnce(sess)` at watcher start) is what satisfies AC #2's "real terminal size, not the fixed 40×120 spawn default": `StartPTY` opens at 40×120, the prime immediately resizes to the operator's terminal. Bridge mode reaches real geometry when a client attaches and drives `Bridge.Resize`.

### What stays the same (zero cascade)

`Bridge.Write`/`SetOutputObserver`/`Attach`; `Supervisor.WriteUserTurn`/`WaitForPTY`/`CurrentConversation` signatures; `sessions.Session.{WriteUserTurn,Resize,Activate}`; `control/server.go` resize calls; `cmd/pyry/assistant_turn*.go`; `relay/handlers/send_message.go`. `codegraph_impact SetPTY` confirms callers are `runOnce`-only; the rename is package-internal.

## Concurrency model

Per `runOnce` iteration the goroutines are: tui-driver's two (the `cmd.Wait` observer and the PTY reader, both internal to `Spawn`) plus our input pump and output pump. **Every one is joined before `runOnce` returns** — this is the invariant `TestSupervisor_Foreground_NoStdinReaderLeak` guards.

- **Reader never blocks on the mirror.** `MirrorOutput` is buffer-256 drop-newest; the reader `select`s with a `default` drop. So even a stalled output pump cannot wedge the reader or `Close`. The mirror is best-effort; a dropped chunk heals on claude's next repaint.
- **Output pump** ends when `MirrorOutput()` closes. The reader closes it (LIFO `defer`) when the PTY read errors — on natural child exit *or* on `sess.Close()` closing the PTY. By the time `sess.Wait()` returns (`<-exited` then `<-readerDone`), the channel is already closed, so the pump is draining/done; join it via a `done` channel with the existing `goroutineDrainTimeout` safety net.
- **Input pump** ends when its source closes: bridge mode via `EndIteration()` → `bridge.Read` returns EOF; foreground via `input.Close()`. Join with the same timeout.
- **tui-driver's goroutines** join inside `sess.Close()` (`<-readerDone`) and the `cmd.Wait` observer returns once the process is reaped.

**Teardown ordering (load-bearing, preserves lessons.md):** clear the registrations *before* the PTY closes. Bridge: `setSession(nil)` → `Bridge.SetResizer(nil)` → `EndIteration()` → `sess.Close()`. Foreground: `setSession(nil)` → `input.Close()` → `sess.Close()` (the deferred `stopResize()` may run after; a SIGWINCH-driven `sess.Resize` on a closed session returns an ignored error, no dangling-FD risk). Clearing first guarantees a racing `WriteUserTurn`/`Resize` sees `nil` and drops/no-ops rather than touching a closing session.

Lock discipline unchanged: `convMu`, `sessMu` (was `ptmxMu`), and `ptyMu` stay leaf-only and mutually un-nested.

## Error handling

- `Spawn` failure → `fmt.Errorf("spawn: %w", err)` (was `"pty start: %w"`); `runOnce` returns it, `Run` logs + backs off (unchanged classification).
- `WriteUserTurn`: validator error verbatim; no session → drop + `nil`; `AttachInput` error → `"supervisor: write user turn: %w"` (unchanged contract; `AttachInput` surfaces the closed-file error after `Close`).
- `Bridge.Resize`: no resizer → `nil`; else the delegate's wrapped error. Control plane still swallows resize errors (no attach failure).
- `resizeOnce`: all errors ignored (best-effort), as today.
- `sess.Close()` result is ignored in the loop (`_ =`); `waitErr` from `sess.Wait()` is the authoritative exit value for backoff/logging.

## Testing strategy

Test-first (RED → GREEN). Reuse `TestHelperProcess`/`helperConfig`; stdlib `testing` only. Changes are contained to `supervisor_test.go` and `bridge_test.go` (two files).

**Existing tests that must stay green unchanged** (behaviour-level, public API only): `TestSupervisor_RestartsAfterCrash`, `ChildExitsCleanly`, `GracefulShutdown`, `Foreground_NoStdinReaderLeak`, the `WriteUserTurn_*` set (HappyPath spawns a real session and polls the `stdin_to_file` helper — exercises `AttachInput` end-to-end; the cursor-only tests use the no-session drop path).

**Call-site swaps (mechanical):**
- `WaitForPTY_*` (4 tests): `sup.setPTY(f)` + `/dev/null` open → `sup.setSession(&tuidriver.Session{})` and `sup.setSession(nil)`. A zero-value `&tuidriver.Session{}` is a valid non-nil pointer; `setSession` only stores it + drives the readiness channel and **never dereferences it** — so this is safe *only* for readiness tests. **Never** set a dummy session then call `WriteUserTurn` (would deref a nil PTY → panic).

**Rewrites:**
- `TestBridge_Resize*` (3 tests): replace the real-`pty.Open` + `pty.Getsize` assertions with a `fakeResizer` double recording `(rows, cols)` and a settable error. Assert: `SetResizer(fr)` + `Resize(40,100)` forwards `(40,100)`; `Resize` with no resizer returns `nil`; `Resize` after `SetResizer(nil)` returns `nil` (silent no-op). The "geometry actually reaches the PTY" path is already covered by tui-driver's own `Session.Resize`/`pty` tests.

**New coverage (behaviour the existing tests don't pin on the Session-hosted path):**
- Output mirror reaches a bridge consumer: spawn a helper that writes a known marker to stdout, run the supervisor in bridge mode with an attached buffer (or an `outputObserver`), assert the marker arrives — proves the `MirrorOutput → bridge.Write` feed and the fan-out to both surfaces. Skip cleanly where `pty.Open`/TTY is unavailable (match the existing resize-test skip pattern).
- (Optional, if cheap) a foreground/bridge resize that drives `sess.Resize` via the registered resizer, asserting delegation end-to-end through `Bridge.Resize`.

Gate: `make check` (vet + race + staticcheck + `cmd/substrate-guard` — no claude TUI screen literal is added; the mirror bytes are opaque and never inspected) and `go test -race ./internal/supervisor/...`.

## Open questions

- **`EnsureClaudeEnv`/`TERM` in service mode.** #593 deliberately preserves the daemon's inherited `TERM` (no override) to stay behaviour-preserving. #596 (screen parsing) may need `TERM=xterm-256color` for reliable glyph detection; that is #596's call, made when parsing lands. Flagged so it isn't silently assumed here.
- **`spawn.go`'s `SpawnPTY`.** Test-only, no production callers; left untouched. If a future ticket migrates `pyry agent-run`'s one-shot path or removes dead code, it can retire `SpawnPTY` then — out of scope here.
- **`sess.Close()` SIGTERM grace vs. the backoff loop.** `Close` adds a SIGTERM→grace step on the already-exited child each iteration (a no-op when the child is dead, which is the normal case after `sess.Wait()`). No measurable cost expected; confirm `RestartsAfterCrash` timing stays within its window under `-race`.

## Size note (why S, not the [A]/[B] split)

The ticket pre-identified an [A] core-host-loop / [B] bridge-mirror split to use *if* the real surface exceeds ~100 production LOC or cascades across >5 test fixtures. It does not:

- **3 production files**, ~80–100 LOC net (runOnce substitutions + field renames + a 5-line `sessionWriter` + a 3-line `resizer` interface + the `Bridge`/`winsize` delegate swaps). Under the ≥5-file / ~600-total-LOC red lines.
- **0 new exported types.** `resizer` is unexported.
- **Zero cross-package cascade.** `SetPTY`→`SetResizer` and `setPTY`→`setSession` are package-internal (`codegraph_impact` + grep confirm `runOnce`-only callers). All public signatures consumed by `internal/sessions`, `internal/control`, `internal/relay/handlers`, `cmd/pyry` are preserved.
- **Test cascade contained to 2 files** — 4 mechanical `setSession` swaps + 3 `Bridge.Resize` rewrites + 1–2 new behaviour tests. Not a cross-file fixture cascade.

A single, coherent, behaviour-preserving swap inside one package. Splitting would create an artificial seam (the foreground and bridge branches share the same `Spawn`/`Session`/teardown machinery; separating them duplicates the host-loop scaffolding across two tickets).
