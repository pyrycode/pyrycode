# ADR 012: `pyry attach --stdio` is a flag, not a separate verb

## Status

Accepted (ticket #154, Phase 1.3a).

## Context

Phase 1.3a needed a no-PTY attach mode for SDK consumers — specifically Claudian (the Obsidian plugin built on `@anthropic-ai/claude-agent-sdk`), which spawns claude with `--input-format stream-json --output-format stream-json` and exchanges line-delimited JSON over stdin/stdout pipes. No PTY, no raw mode, no SIGWINCH.

Today's `pyry attach <id>` is heavy on the client side: `term.MakeRaw(stdinFd)`, `pty.GetsizeFull`, SIGWINCH watcher, `copyWithEscape`'s `Ctrl-B d` state machine. For a stream-json client, all of that is dead weight at best and active interference at worst — `term.MakeRaw` mutates a tty that isn't there, and `copyWithEscape`'s 1-byte escape detection silently mangles bytes any time a JSON message contains `0x02` followed by `'d'`.

A no-PTY mode is needed. The question is the surface shape: a new flag (`pyry attach --stdio <id>`) or a new verb (`pyry attach-stdio <id>` / `pyry stream <id>` / similar).

## Decision

**Flag, not new verb: `pyry attach [--stdio] [<id>]`.** The flag is opt-in. Without it, behaviour is byte-identical to the v1.1e PTY-mode attach. With it, the CLI dispatches to a new `control.AttachStdio` client that bridges raw bytes through the same wire protocol with no PTY-side machinery.

Server-side: **zero changes.** The same `VerbAttach` handshake; the client sends `Cols=0, Rows=0` so the existing `payload.Cols > 0 && payload.Rows > 0` guard skips the resize seam, and `omitempty` keeps the fields off the wire entirely (byte-identical to a v0.5.x client that doesn't know them).

## Rationale

The verb names the *resource operation* ("attach to session N"). PTY-vs-raw-bytes is a *transport variant* of the same binding:

- Same `VerbAttach`.
- Same `Pool.ResolveID` selector resolution.
- Same `Activate`-then-bind sequence.
- Same `Bridge.Attach` server-side.
- Same error envelope (`Response.Error`), same foreground-mode wire string, same `ErrBridgeBusy` shape.

A separate verb would duplicate every one of those without adding addressable behaviour. Same logic gRPC and HTTP use ("upgrade" headers, content-type switches): the verb names the resource; the encoding is metadata.

### Sub-flag parsing precedent

`runSessionsNew` already runs its own `flag.NewFlagSet("pyry sessions new", flag.ContinueOnError)` after `parseClientFlags` peels the global `-pyry-*` flags (see [ADR 010](010-sessions-cli-sub-router.md)). `runAttach` adopts the same shape: a fresh `flag.FlagSet` for `--stdio`, then `attachSelectorFromArgs` on the post-flag positionals. Helper `parseAttachArgs(args) (sessionID, stdio, err)` is the unit-testable seam — table-tests cover `["--stdio"]`, `["--stdio", "<id>"]`, `["<id>", "--stdio"]` (rejected: flags must precede positionals), and the too-many-positionals case without dialling the control socket.

### Why no PTY-detection auto-mode

An alternative was: keep the verb shape unchanged and have `pyry attach` auto-detect via `term.IsTerminal(stdinFd)` — TTY → PTY mode, non-TTY → stdio mode. Rejected:

- **Implicit mode switching is hostile.** A test suite running `pyry attach` under a captured stdin would silently produce stream-json behaviour; a script piping input to `pyry attach` would silently lose the escape-key surface. Surprising in both directions.
- **Stream-json clients want the explicit contract.** Claudian wants to assert "stdio mode" at the call site so a future host that happens to expose a tty (a debugger, a wrapped shell) doesn't change behaviour underneath it.
- **Operators may pipe input into PTY-mode attach intentionally.** A heredoc or `<<<` redirection through `pyry attach` reaches the supervised claude through `copyWithEscape` today. Auto-stdio-mode would break that path.

The flag is the explicit signal. `--stdio` is what an SDK passes; humans don't pass it; nothing changes for either group accidentally.

### Why suppress the affordance stderr lines under `--stdio`

`runAttach` prints `"pyry: attached. Press Ctrl-B d to detach."` on entry and `"pyry: detached."` on exit — affordances for human users. Under `--stdio` they are noise on a programmatic parent's stderr channel. The suppression is a one-line `if !stdio { fmt.Fprintln(os.Stderr, …) }` guard around each print. No flag, no env var — the mode is the signal.

## Consequences

### Positive

- **Zero server change.** `handleAttach` is byte-identical-input to today's PTY-mode attach client. The `Cols=0, Rows=0` handshake path was already in place from #136 (handshake geometry's zero-sentinel skip-resize rule); this ticket just becomes a second consumer.
- **CLI surface stays one verb.** `pyry attach` remains the only attach verb operators, scripts, and SDKs need to know. The transport variant is documented on the help line.
- **No new wire field.** `omitempty` carries `Cols=0` / `Rows=0` off the wire, so a stdio handshake is byte-indistinguishable from a v0.5.x client that didn't know the fields. Pinned by `TestAttachStdio_NoGeometryOnWire`.
- **PTY-mode path is byte-unchanged.** No flag → byte-identical to the pre-#154 surface. Operators won't see the new code unless they ask for it.

### Negative

- **Two attach client functions live side by side.** `Attach` (PTY-mode, `attach_client.go`) and `AttachStdio` (no-PTY, `attach_stdio_client.go`). The duplication is the dial-and-handshake prologue (~10 lines). The diverging tails (raw mode + escape-key + SIGWINCH watcher vs. plain `io.Copy`) are large enough that an attempted shared core would be a sea of `if stdio { … }` branches — keeping them as siblings is the simpler shape. If a third transport ever lands, factor at that point, not pre-emptively.
- **Adding a transport variant in the future means another flag, not a new function.** Acceptable; the existing flag-set parser is open to extension.

### Neutral

- **`AttachStdio` joins its output goroutine before returning; `Attach` does not.** `Attach` can fire-and-forget the output goroutine because `defer term.Restore` doesn't depend on the goroutine completing. `AttachStdio` has no terminal state to restore, but joins via a `done chan struct{}` so the caller gets a deterministic "all server bytes flushed to `out`" guarantee — useful for SDK consumers that read `out` until the spawned process closes the pipe. Small upgrade over `Attach`'s shape; not a regression for either path.

## Alternatives considered

- **Separate verb `pyry attach-stdio`** — rejected for the duplication reasons above.
- **Separate verb `pyry stream`** — rejected; would invent a new noun for an existing operation, and the operation isn't really "streaming" in any way that distinguishes it from PTY-mode attach (both stream bytes).
- **Auto-detect via `term.IsTerminal`** — rejected for the surprise-mode-switch reasons above.
- **A third wire verb `VerbAttachStdio`** — rejected; duplicates the entire attach handler with the same body (resolve, lookup, activate, bridge), and adds a wire constant for no addressable difference. The zero-sentinel geometry shape already routes through `handleAttach` cleanly.

## References

- [`features/control-plane.md` § Attach: stdio mode (1.3a)](../features/control-plane.md#attach-stdio-mode-13a) — implementation walkthrough.
- [ADR 010](010-sessions-cli-sub-router.md) — `flag.NewFlagSet` precedent for sub-flag parsing.
- `docs/specs/architecture/154-attach-stdio-mode.md` — full architect's spec.
- Issue [#154](https://github.com/pyrycode/pyrycode/issues/154); follow-on coverage: #161 (E2E harness), #162 (no-PTY-in-fd-table assertion).
