# Attach: stdio mode (1.3a)

`pyry attach --stdio [<id>]` is the no-PTY counterpart of the default attach mode, intended for SDK consumers (Claudian / `@anthropic-ai/claude-agent-sdk`) that exchange line-delimited JSON over their own stdin/stdout pipes. The flag is opt-in; without it, behaviour is byte-identical to the pre-#154 PTY-mode surface. See [ADR 012](../decisions/012-attach-stdio-flag-vs-verb.md) for why this is a flag rather than a separate verb.

### Server side: zero changes

The handshake is byte-identical-input to today's PTY-mode attach client. `AttachStdio` sends `Cols=0, Rows=0`; `omitempty` keeps both fields off the wire entirely (byte-indistinguishable from a v0.5.x client that didn't know the fields), and the existing `payload.Cols > 0 && payload.Rows > 0` guard in `handleAttach`'s [Handshake Geometry block](#attach-handshake-geometry-136) skips the resize seam. `sess.Attach(conn, conn)` is already PTY-agnostic — the supervisor `Bridge` takes opaque `io.Reader`/`io.Writer`. No new verb, no new field, no new branch.

### `control.AttachStdio` — client function

```go
func AttachStdio(ctx context.Context, socketPath, sessionID string, in io.Reader, out io.Writer) error
```

Sibling to `control.Attach` rather than a shared core. The dial-and-handshake prologue is ~10 lines; the diverging tail (raw mode + SIGWINCH + `copyWithEscape` vs. plain `io.Copy`) is large enough that a shared core would be a sea of `if stdio { … }` branches. Two functions, one wire protocol.

```
caller                      AttachStdio                   server
------                      -----------                   ------
                            dial(ctx, socketPath)
                            json encode VerbAttach
                                       ─── handshake ───>
                                       <─── ack (OK) ────
                            ┌─ goroutine: io.Copy(out, conn)        # server → caller
                            │
                            └─ main:    io.Copy(conn, in)           # caller → server
                                          (returns when in EOF)
                            close(conn)         # forces output goroutine's Read to return
                            <-doneCh            # join output goroutine
                            return writerErr(copyErr)
```

Lifecycle invariants:

- **No goroutine outlives the call.** The output goroutine is joined via a `done chan struct{}` closed inside its `defer`. Small upgrade over `Attach`'s fire-and-forget output goroutine — `Attach` can afford fire-and-forget because `defer term.Restore` doesn't depend on the goroutine completing; `AttachStdio` has no terminal state to restore but joining gives the caller a deterministic "all server bytes flushed to `out`" guarantee.
- **Closing `conn` is the cross-goroutine wake-up.** When the input copy returns, the function closes the conn; that unblocks the output goroutine's `Read` with `net.ErrClosed`/EOF; `io.Copy` returns; the goroutine signals done. No channels-with-cancel needed — the conn is the rendezvous.
- **EOF on `in` returns nil.** The clean-detach contract (matches lazy-eviction semantics: detach ≠ destroy). Server-side close after ack also returns nil — the existing `writerErr` helper coerces `net.ErrClosed` / `io.ErrClosedPipe` on the input side's last write.
- **`ctx` scopes the dial only.** Once the conn is established, the attach is driven by I/O on `in`/`out`/`conn`. Caller cancels by closing `in`. Same shape as `Attach`'s `ctx` use.

Error encoding mirrors `Attach`: dial errors propagate wrapped (`fmt.Errorf("dial: %w", err)`), handshake encode/decode errors propagate wrapped, `Response.Error` from the ack flows through verbatim (the foreground-mode wire string is byte-identical to PTY-mode — pinned by `TestAttachStdio_AckErrorPropagates`), `OK=false` with empty `Error` returns `"control: attach ack missing"`.

### CLI surface

```
pyry attach [flags] [--stdio] [<id>]
```

`runAttach` grows a `parseAttachArgs(args) (sessionID, stdio, err)` helper — extracted so the parsing rules are unit-testable without dialling the socket. The helper opens a fresh `flag.NewFlagSet("pyry attach", flag.ContinueOnError)` for `--stdio`, then routes the post-flag positionals through the existing `attachSelectorFromArgs`. Mirrors the [`runSessionsNew` precedent](#sessions-cli-router-11a-b2).

Flag-ordering rule: **`--stdio` must precede any positional.** Go's `flag` package stops at the first non-flag arg, so `pyry attach <id> --stdio` is rejected as `errTooManyAttachArgs`. Same convention as `-pyry-*` globals before sub-verbs (see [ADR 010](../decisions/010-sessions-cli-sub-router.md)).

Under `--stdio`, the affordance stderr lines (`"pyry: attached. Press Ctrl-B d to detach."`, `"pyry: detached."`) are suppressed — they are noise on a programmatic parent's stderr channel. One-line `if !stdio { … }` guard around each print.

### Tests

All new tests in `internal/control/attach_stdio_client_test.go`. Drive both sides via `io.Pipe` / `bytes.Buffer` — no real PTY, no real terminal. Existing fakes (`fakeAttachProvider`, `sessionResolverWith`, `shortTempDir`) reused as-is.

| Test | Asserts |
|---|---|
| `TestAttachStdio_ByteForwarding` (table) | Plain bytes, stream-json line, `Ctrl-B d` sequence (NOT consumed), NUL/high bytes, empty input — all flow verbatim through `provider.received()` |
| `TestAttachStdio_ServerToClientStream` | Server-side writes appear at `out` byte-for-byte |
| `TestAttachStdio_EOFReturnsNil` | EOF on `in` → nil return; output goroutine joined |
| `TestAttachStdio_ServerHangupReturnsNil` | Server closes after ack → nil return (writerErr contract) |
| `TestAttachStdio_AckErrorPropagates` | Foreground-mode wire string flows verbatim |
| `TestAttachStdio_AckMissingOK` | `OK=false, Error=""` → `"control: attach ack missing"` |
| `TestAttachStdio_DialError` | Missing socket surfaces as wrapped dial error |
| `TestAttachStdio_SessionIDOnWire` (table) | Empty / full UUID / prefix / whitespace flow through `Attach.SessionID` verbatim; `Cols/Rows` are zero |
| `TestAttachStdio_NoGeometryOnWire` | Raw bytes off the conn contain neither `"cols"` nor `"rows"` nor `sessionID` (omitempty pinning) |
| `TestAttachStdio_InReadErrorPropagates` | Non-EOF read error on `in` propagates wrapped (distinguishes "stdin EOFed" from "pipe broke") |
| `TestParseAttachArgs` (in `cmd/pyry/args_test.go`) | Flag-parsing rules: `--stdio` alone, `-stdio` (single dash), `--stdio <id>`, `<id> --stdio` (rejected), too many positionals |

E2E coverage: the process-level harness driving `pyry attach --stdio` against a real daemon landed in #161 (`internal/e2e/attach_stdio.go` — `startStdioAttach` + `StdioAttachClient`; see [e2e-harness.md § Stdio-Attach Harness Pattern](e2e-harness.md#stdio-attach-harness-pattern-attach_stdiogo-attach_stdio_testgo-161)). #167 fixed the `parseClientFlags` rejection; removing the skip uncovered a pre-existing harness bug — `spawnAttachableDaemon` wires the Go test binary directly as `claude`, so `Pool.Create`'s appended `--session-id <uuid>` reaches the test framework's `flag.Parse()` and is rejected. `auto_attach.go` already works around this with a shell-wrapper (`echoClaudeScript`); the stdio harness needs the same. Skip rotated to #257; `TestE2E_AttachStdio_BytesRoundTrip` and `TestE2E_AttachStdio_NoPTYInProcessTree` (#162) both lift in one commit when that ticket lands.
