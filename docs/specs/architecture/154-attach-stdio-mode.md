# Spec — `pyry attach --stdio` (no-PTY byte forwarding for SDK consumers)

**Ticket:** [#154](https://github.com/pyrycode/pyrycode/issues/154)
**Phase:** 1.3a (extends Phase 1 multi-session pool with non-PTY attach for SDK consumers)
**Size:** S

## Files to read first

The developer's turn-1 reading list. Pull from these to avoid re-discovering the
attach surface from scratch.

- `internal/control/attach_client.go` (full, 233 lines) — the PTY-mode reference
  implementation. The new function is a deliberate **subset** of `Attach`: same
  dial-and-handshake prologue, no `term.MakeRaw`, no `startWinsizeWatcher`, no
  `copyWithEscape`. Mirror its godoc tone for `AttachStdio`'s.
- `internal/control/server.go:599-687` (`handleAttach`) — confirms server side
  needs **no changes**. The zero-cols/zero-rows handshake path
  (`server.go:651`) already skips the resize seam, and `sess.Attach(conn, conn)`
  treats the conn as opaque `io.ReadWriter`.
- `internal/control/protocol.go:116-137` (`AttachPayload`) — `Cols`, `Rows`,
  `SessionID` all carry `omitempty`. Empty-`SessionID` byte-shape is already
  pinned by `TestAttach_EmptySessionIDOmittedOnWire`; the same omitempty rule
  carries `Cols=0`/`Rows=0` off the wire.
- `cmd/pyry/main.go:454-488` (`runAttach`) — the call site to extend.
  `parseClientFlags` strips only `-pyry-*` flags; attach-specific flags need a
  fresh `flag.FlagSet` (precedent: `runSessionsNew`).
- `cmd/pyry/main.go` (grep for `flag.NewFlagSet` in
  `runSessionsNew`/`runSessionsRm`) — the established sub-flag parsing pattern.
- `internal/control/attach_test.go:226-285` (`TestServer_AttachHandshakeAndStream`)
  and `:746-865` (`TestAttach_ClientSendsSessionID`,
  `TestAttach_EmptySessionIDOmittedOnWire`) — the test pattern to extend
  (fakeAttachProvider, server-with-fake-resolver, raw-bytes round trip).
- `internal/control/attach_client.go:74-100` (the synchronous-stop /
  `defer stopWinsize()` block) — concrete shape of the goroutine-lifecycle
  guarantees the new function must replicate (no goroutine outlives the call).

## Context

### Problem

Claudian (Obsidian plugin built on `@anthropic-ai/claude-agent-sdk`) spawns
claude via the SDK's process-pipe shape:

```
claude --input-format stream-json --output-format stream-json
       --session-id <uuid> ...
```

— with the parent process exchanging line-delimited JSON over the child's
stdin/stdout pipes. **No PTY.** No raw mode. No SIGWINCH. The transport is
just two byte streams and an EOF.

To make Claudian use pyry-supervised sessions, the SDK needs to invoke a
binary that speaks the same protocol over the same pipes. Today's
`pyry attach <id>` allocates a PTY on the client side
(`term.MakeRaw(stdinFd)`, `pty.GetsizeFull`, SIGWINCH watcher, escape-key
state machine). For a stream-json client, all of that is dead weight at
best and active interference at worst — `term.MakeRaw` mutates a tty that
isn't there, and `copyWithEscape`'s `Ctrl-B d` detection silently mangles
the byte stream if a JSON message ever contains `0x02` followed by `'d'`.

### Solution shape

Expose a no-PTY mode of the same operation as a new flag on the existing
verb: `pyry attach --stdio <session-id>`. The CLI dispatches to a new
client-side function `AttachStdio` that:

- speaks the same wire protocol (`VerbAttach`, `AttachPayload`),
- sends `Cols=0, Rows=0` so the server's existing zero-sentinel skips the
  PTY resize (no semantic change server-side),
- bridges `os.Stdin → conn` and `conn → os.Stdout` via straight `io.Copy`
  — no raw mode, no SIGWINCH, no escape detection,
- exits cleanly when stdin EOFs **without terminating the session**
  (consistent with lazy-eviction semantics: detach ≠ destroy).

The supervisor still runs claude under a PTY — that's a separate concern
on the daemon side, unaffected by this ticket.

### Decision: flag, not new verb

Architect call (the ticket explicitly invites this): **flag on
`pyry attach`, not a separate verb.** The verb is the resource operation
("attach to session N"); raw-bytes-vs-PTY is a transport variant of the
same binding. Same `VerbAttach`, same `Pool.ResolveID`, same `Activate`,
same `Bridge.Attach` server-side. A separate verb would duplicate every
one of those without adding addressable behaviour. Same logic gRPC and
HTTP use ("upgrade" headers, content-type switches): the resource is what
the verb names; the encoding is metadata.

## Design

### Surface added

One new exported function in `internal/control`:

```go
// AttachStdio is the no-PTY counterpart to Attach. It performs the same
// VerbAttach handshake, then bridges in→conn and conn→out as raw bytes —
// no raw mode, no SIGWINCH watcher, no escape-key detection. Intended
// for SDK consumers (stream-json, tooling) that exchange line-delimited
// payloads over their own stdin/stdout pipes and need pyry to be a
// transparent byte conduit.
//
// sessionID resolution rules match Attach (full UUID, unique prefix, or
// "" → bootstrap; resolved server-side via Pool.ResolveID).
//
// EOF on `in` ends the attach cleanly; the session itself stays alive
// (lazy-eviction semantics — detach ≠ destroy). Server-initiated close
// (daemon stop, session evicted from under us) returns nil. Any other
// I/O error propagates.
//
// AttachStdio does not touch any tty. It is safe to call when stdin
// is a pipe, a file, /dev/null, or absent — there is no IsTerminal
// branch.
func AttachStdio(ctx context.Context, socketPath, sessionID string, in io.Reader, out io.Writer) error
```

One new flag on `pyry attach` (parsed via `flag.FlagSet`, the
`runSessionsNew` precedent):

```
pyry attach [-pyry-socket=...] [-pyry-name=...] [--stdio] [<id>]
```

`--stdio` is opt-in. Without it, behaviour is byte-identical to today's
PTY-mode attach. With it, `runAttach` calls `control.AttachStdio` instead
of `control.Attach`, and suppresses the human-affordance stderr lines
("pyry: attached. Press Ctrl-B d to detach.", "pyry: detached.") — those
are noise to a programmatic parent.

### Wire-level: nothing changes

Server-side `handleAttach` (server.go:599-687) is byte-identical-input to
today's PTY-mode attach client:

- Handshake `Cols=0, Rows=0` → omitempty drops the fields, server's
  `payload.Cols > 0 && payload.Rows > 0` guard skips the resize call.
  No new wire field, no new branch.
- `sess.Attach(conn, conn)` is already PTY-agnostic — the supervisor
  Bridge takes opaque `io.Reader`/`io.Writer` and pipes bytes through.
- Error paths (foreground-mode, `ErrAttachUnavailable`, busy bridge, etc.)
  surface via the same `Response.Error` mapping. `AttachStdio`'s error
  shape mirrors `Attach`'s: dial errors, handshake encode/decode errors,
  ack carries `Error`, copy errors.

### Client-side concurrency model

Two goroutines, deterministic shutdown — same shape as `Attach`'s output
copy / input copy split, minus the SIGWINCH watcher.

```
caller                      AttachStdio                   server
------                      -----------                   ------
                            dial(ctx, socketPath)
                            json encode VerbAttach
                                       ─── handshake ───>
                                       <─── ack (OK) ────
                            ┌─ goroutine: io.Copy(out, conn)        # server → caller stdout
                            │
                            └─ main:    io.Copy(conn, in)           # caller stdin → server
                                          (returns when in EOF)
                            close(conn)         # forces output goroutine's Read to return
                            <-doneCh            # join output goroutine
                            return nil
```

Lifecycle invariants:

1. **No goroutine outlives the call.** Same load-bearing guarantee as
   `Attach`'s `defer stopWinsize()` block (attach_client.go:80-81). The
   output goroutine is joined via a `done chan struct{}` closed inside
   the goroutine's defer; the function does not return until the join
   completes.
2. **Closing `conn` is the cross-goroutine signal.** When the input
   copy returns (caller's stdin EOF or read error), the function
   closes `conn`. That unblocks the output goroutine's `Read` with
   `net.ErrClosed`/EOF; `io.Copy` returns; the goroutine signals done.
   No channels-with-cancel needed — the conn is the rendezvous point.
3. **EOF on `in` returns nil.** The clean-detach contract. Translation
   table:
   - `in` EOF → close conn → return nil
   - server hangup (conn EOF on output side) → output goroutine
     returns; input copy then errors on `Write` (`net.ErrClosed` /
     `io.ErrClosedPipe`); `writerErr`-style coercion returns nil.
   - other read/write error → propagated wrapped (`fmt.Errorf("attach
     stdio: …: %w", err)`).
4. **`ctx` cancels dial only.** Once the conn is established the attach
   is driven by I/O; `ctx` is captured for `dial(ctx, socketPath)` and
   its cancellation is not plumbed through to the I/O loop. This
   matches `Attach`'s shape — `ctx` there is also dial-only.

### CLI wiring

`runAttach` grows an attach-specific flag-set (the `runSessionsNew`
pattern). Before:

```go
func runAttach(args []string) error {
    socketPath, rest, err := parseClientFlags("pyry attach", args)
    if err != nil { return err }
    sessionID, err := attachSelectorFromArgs(rest)
    // … geometry read, control.Attach call …
}
```

After:

```go
func runAttach(args []string) error {
    socketPath, rest, err := parseClientFlags("pyry attach", args)
    if err != nil { return err }

    fs := flag.NewFlagSet("pyry attach", flag.ContinueOnError)
    fs.SetOutput(io.Discard) // top-level main prints usage on error
    stdio := fs.Bool("stdio", false, "no-PTY byte forwarding for SDK consumers")
    if err := fs.Parse(rest); err != nil {
        // surface via existing usage-line/exit-2 path
    }
    sessionID, err := attachSelectorFromArgs(fs.Args())
    if err != nil { /* same as today */ }

    if *stdio {
        if err := control.AttachStdio(context.Background(), socketPath, sessionID, os.Stdin, os.Stdout); err != nil {
            return fmt.Errorf("attach: %w", err)
        }
        return nil
    }

    // existing PTY-mode path: print "pyry: attached…", read geometry,
    // call control.Attach, print "pyry: detached." — UNCHANGED.
}
```

`attachSelectorFromArgs`'s shape is preserved: it operates on whatever
positionals remain after sub-flag parsing. No change to its signature
or its error contract.

### Files touched

| File | Lines | Why |
|---|---|---|
| `internal/control/attach_stdio_client.go` (NEW) | ~55 prod | `AttachStdio` function + helper. Isolated from PTY-mode for clarity. |
| `internal/control/attach_stdio_client_test.go` (NEW) | ~150 test | Table-driven coverage (see Testing strategy). |
| `cmd/pyry/main.go` | ~15 prod | `--stdio` flag, dispatch, suppress affordance stderr. |
| `cmd/pyry/main.go` (usage strings, lines 1050+, 1082+) | ~3 | One-line addition to the attach usage example. |

Total: ~70 lines production. 0 new exported types. 1 new exported
function. 1 consumer call site. No edit fan-out.

## Concurrency model

(Recap, named per the design above.)

- **Goroutines:** 1 (output: `conn → out`).
- **Channels:** 1 internal `done chan struct{}` for join.
- **Synchronization:** the conn itself; closing it from the input
  goroutine is the wake-up for the output goroutine's blocked `Read`.
- **Shutdown sequence:** input EOF/err → `conn.Close()` → output
  goroutine's `Read` returns → `io.Copy` returns → goroutine `defer`
  closes `done` → main `<-done` joins → `AttachStdio` returns.
- **Deadlocks ruled out:** the only blocking primitive on the input
  side is `io.Copy(conn, in)` — `in` is the caller's stream, which
  the caller is responsible for terminating (closing stdin in the
  Claudian case is what ends the supervised process). On the output
  side, `io.Copy(out, conn)` blocks on `conn.Read` until the conn is
  closed, which the input side does on exit.

### Compared to `Attach`'s concurrency

| Concern | `Attach` (PTY mode) | `AttachStdio` |
|---|---|---|
| Output goroutine | `io.Copy(os.Stdout, conn)` (fire-and-forget; let conn.Close drain it) | Same, but joined via done-chan before return |
| Input loop | `copyWithEscape(conn, os.Stdin)` (custom 1-byte loop for `Ctrl-B d`) | `io.Copy(conn, in)` (straight) |
| SIGWINCH | `startWinsizeWatcher(...)` + synchronous stop in defer | None |
| `term.MakeRaw` | yes, restored in defer | None |
| Goroutine count | 2 (output + winsize watcher) | 1 (output only) |
| Joining | `stopWinsize()` synchronous; output drained by `conn.Close()` defer | done-chan join |

The output-goroutine-join is a small upgrade over `Attach`'s
fire-and-forget. `Attach` can afford fire-and-forget because the
restoration of terminal state in `defer term.Restore` doesn't depend on
the output goroutine completing. `AttachStdio` has no terminal state to
restore, but joining gives the caller a deterministic "all server bytes
flushed to `out`" guarantee — useful for SDK consumers that read
`stdout` until the spawned process closes the pipe.

## Error handling

| Failure | Behaviour |
|---|---|
| `dial` error (socket missing, perm, etc.) | Propagate wrapped: `fmt.Errorf("dial: %w", err)`. Same shape as `Attach`. |
| Handshake encode error | `fmt.Errorf("send handshake: %w", err)`. |
| Handshake decode error | `fmt.Errorf("read ack: %w", err)`. |
| Ack carries `Error` (`resp.Error != ""`) | `errors.New(resp.Error)`. Foreground-mode case (`"attach: no attach provider configured…"`) flows through verbatim — no special handling needed. |
| Ack OK=false with no Error | `errors.New("control: attach ack missing")` (parity with `Attach`). |
| `in` EOF (clean detach) | Return `nil`. |
| `in` read error other than EOF | Close conn, join output, return wrapped error. |
| `conn` write error during input copy | Coerce `net.ErrClosed`/`io.ErrClosedPipe` to nil (server hung up); propagate others. Reuse the existing `writerErr` helper from `attach_client.go`. |
| `conn` read error during output copy | Goroutine returns; input loop notices via subsequent `Write` failure. No special handling — the input side decides the function's return value. |
| `ctx` cancelled mid-attach | Not plumbed past `dial`. Caller controls via closing `in`. |

The contract: `AttachStdio` returns `nil` on any clean shutdown path
(stdin EOF, server hung up); a non-nil error means a real I/O failure
the caller should surface.

## Testing strategy

All tests in `internal/control/attach_stdio_client_test.go`. Drive both
sides via `io.Pipe` / `bytes.Buffer` — no real PTY, no real terminal.
Existing fakes (`fakeAttachProvider`, `sessionResolverWith`,
`shortTempDir`) are reused as-is.

### Table-driven cases (`TestAttachStdio_ByteForwarding`)

| Case | Input from `in` | Server returns | Expected at `out` | Expected return |
|---|---|---|---|---|
| plain bytes pass through | `"hello world"` | (nothing) | (nothing) | `nil` (after stdin EOF) |
| stream-json line round-trip | `"{\"type\":\"user\",\"content\":\"hi\"}\n"` | (echo provider) | same line | `nil` |
| escape-key sequence is NOT consumed | `"abc\x02d\x02defg"` (would detach in PTY mode) | (nothing) | (nothing — bytes go to server, server is the sink) | `nil` |
| binary-safe (NUL bytes) | `"\x00\x01\x02\xff"` | (nothing) | (nothing) | `nil` |

Mechanics: `fakeAttachProvider` reads from `in` and accumulates into a
mutex-protected buffer (existing pattern, attach_test.go:177-218). The
test asserts `provider.received()` matches the input bytes verbatim,
proving the client side did not filter, escape, or transform.

### Server-driven output (`TestAttachStdio_ServerToClientStream`)

A custom test provider writes a known payload to `out` (the conn) on
attach, closes its done-channel, and the test reads `out` (an
`io.Pipe` reader passed as `AttachStdio`'s `out`). Asserts byte-equality.

### EOF / disconnect (`TestAttachStdio_EOFReturnsNil`)

`in` is a `bytes.Reader` over a small payload — when exhausted it
returns `io.EOF`. `AttachStdio` should return `nil`. Asserts:
- The goroutine has joined (no leak — done-chan is closed before
  return; verifiable via `runtime.NumGoroutine()` delta with a small
  tolerance, or by structural inspection of the function returning).
- The conn is closed (the test's accept loop sees EOF on its side).

### Server hangup (`TestAttachStdio_ServerHangupReturnsNil`)

Server accepts, sends the ack, then immediately closes the conn. The
output `io.Copy` returns; `AttachStdio` should return `nil` (clean,
not a propagated error). Mirrors `writerErr`'s contract.

### Pre-handshake error (`TestAttachStdio_AckErrorPropagates`)

Server returns `Response{Error: "attach: no attach provider configured (daemon may be in foreground mode)"}` (the foreground-mode wire string). `AttachStdio` should return an error whose `.Error()` is exactly that string — proves the byte-identical foreground-mode contract is preserved for stdio clients too.

### SessionID flows through (`TestAttachStdio_SessionIDOnWire`)

Same shape as `TestAttach_ClientSendsSessionID` (attach_test.go:746-821).
Cases: empty, full UUID, prefix, whitespace. Asserts the server-side
captured `Request.Attach.SessionID` matches the input verbatim. Also
asserts `Request.Attach.Cols == 0 && Request.Attach.Rows == 0` — the
"no geometry" promise.

### Geometry omitted on the wire (`TestAttachStdio_NoGeometryOnWire`)

Companion to `TestAttach_EmptySessionIDOmittedOnWire`. Captures raw
bytes off the conn before decoding and asserts `"cols"` and `"rows"`
do not appear. Pins the omitempty contract — a future change that
sets `Cols: 0` explicitly via a struct literal would still pass an
`encoded.Cols == 0` check but break the wire-bytes promise.

### CLI flag wiring (`TestAttach_StdioFlagDispatch`, `cmd/pyry/main_test.go`)

If `attachSelectorFromArgs` already has unit tests, mirror their
shape for the new flag-parsing path — assert that
`["--stdio", "abc-123"]` peels to `(stdio=true, selector="abc-123")`
and `["--stdio"]` peels to `(stdio=true, selector="")`. Existing
attach E2E coverage (test_attach_pty in #125 et al.) is unchanged
because `--stdio` is opt-in and the default path is byte-identical.

### Out of scope here (covered by #161, #162)

- E2E process-level harness driving `pyry attach --stdio` against a
  real daemon — #161.
- `lsof`/`/proc/<pid>/fd` assertion that the attach client has no PTY
  fds open — #162.

The unit tests above land the `go test -race ./internal/control/...`
clean AC. E2E waits for the dependent tickets.

## Open questions

None blocking. Notes for the developer:

- **Flag parsing on `pyry attach` after `parseClientFlags`.**
  `parseClientFlags` returns the post-`-pyry-*` remainder. Today
  `attachSelectorFromArgs` consumes that directly. The change is to
  insert one `flag.FlagSet.Parse` between them — same surgical shape
  `runSessionsNew` uses. If the existing unit tests for
  `attachSelectorFromArgs` cover behaviour the developer wants to
  preserve, route the tests through the new flag-set parser too.
- **Should `AttachStdio` accept `os.Stdin`/`os.Stdout` directly, or
  take `io.Reader`/`io.Writer` parameters?** Spec is the latter — easier
  to test (no `os.Stdin` dance), and the call site in `main.go` passes
  `os.Stdin`/`os.Stdout` explicitly. No production-code seam, but it
  removes a test scaffolding concern. This is the same pattern
  `copyWithEscape` exposes (it takes `io.Reader`/`io.Writer`, not the
  `Attach` function's `os.Stdin`).
- **Should the `--stdio` flag also gate suppression of pyry's own
  stderr noise from `runAttach`?** Yes — see "CLI wiring". The
  "pyry: attached…" / "pyry: detached." lines are TTY affordances
  for human users; SDK consumers either don't see pyry's stderr at
  all or see it as a noise channel. Suppressing them in `--stdio`
  is a one-line `if !stdio { fmt.Fprintln(os.Stderr, …) }` guard.
  No CLAUDE.md or doc updates needed — the flag's behaviour is
  self-documenting.
- **Reusable helpers from `attach_client.go`.** `dial` and `writerErr`
  are package-private and reusable. The developer should reuse them
  rather than duplicating; the spec assumes they will.
