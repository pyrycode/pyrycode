# Spec #756 — `pyry acp`: serve the `internal/acp` transport over stdio

**Ticket:** #756 (split from #746, sub-issue of epic #600, blocked by #755 — now merged)
**Size:** S — 1 new production file (`cmd/pyry/acp.go`) + a one-case dispatch edit to `cmd/pyry/main.go`; 0 new exported types; ~60 production LOC + ~140 test LOC. Kept at S (not downgraded to XS): the signal-driven blocked-read unblock, the clean-exit mapping, and the deterministic serve-loop test are the non-trivial core.
**Security-sensitive:** No (not labelled). Composition-root plumbing at a **local parent→child stdio** boundary: it registers no handlers, validates no frames itself (the transport owns framing/dispatch — #755, likewise unlabelled), and drives no claude. Re-evaluate the label on the later tickets that register real `session/*` handlers.

## Files to read first

Read these before writing code. Each line says what to extract.

- `internal/acp/acp.go:83-150` — `New(r, w, log)` + `Serve(ctx)` contract. **Extract:** `New` panics on nil `r`/`w`, `nil` log → `slog.Default()`; `Serve` returns `nil` at EOF, `ctx.Err()` when cancelled **between frames**, and a wrapped error (`fmt.Errorf("acp: serve: %w", …)`) on a structurally broken stream. The doc comment at :121-130 already names #756 as the owner of "unblock a real blocked stdin by closing the reader on shutdown" — this ticket delivers exactly that closer.
- `cmd/pyry/main.go:189-222` — `run()`'s `switch os.Args[1]` dispatch. **Extract:** the verb-routing shape; add `case "acp": return runACP(os.Args[2:])` beside `case "agent-run":`.
- `cmd/pyry/main.go:625-704` — `runSupervisor`'s prologue. **Extract:** the exact `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)` + `defer cancel()` idiom (:703-704) to mirror, and the `slog.NewTextHandler(os.Stderr, …)` construction (:689-692 builds a *teed* logger — this ticket uses the **plain, un-teed** form; see Design §2).
- `cmd/pyry/main.go:843-847` — `runSupervisor`'s clean-exit mapping (`if runErr != nil && !errors.Is(runErr, context.Canceled)`). **Extract:** the "treat `context.Canceled` as clean shutdown" precedent. **Caveat (load-bearing):** this ticket needs a *stronger* guard than `errors.Is(err, context.Canceled)` — see Error handling §. The forced-reader-close path returns a wrapped `os.ErrClosed`, not `context.Canceled`.
- `cmd/pyry/agent_run.go:225-271` — `runAgentRun`: the sibling verb-file pattern. **Extract:** signature `func runX(args []string) error`, the `signal.NotifyContext` setup (:251), and the `if errors.Is(err, context.Canceled) { return nil }` clean-exit tail (:264-270). `runACP` is a leaner cousin.
- `internal/supervisor/supervisor.go:798-823` (`openTTYInput` + `stdinFallback`) — **the decisive precedent for AC#4.** **Extract:** *why* closing a plain fd cannot interrupt a blocked in-kernel `Read`, and the O_NONBLOCK/poller mechanism that makes "close a side fd to drain a blocked reader" work. This is the exact trap the ticket's suggested "close `os.Stdin`" falls into.
- `docs/lessons.md:92` (#78) and `docs/lessons.md:243` — **read both before designing the closer.** :92: reading `os.Stdin` directly strands a goroutine on its `fdMutex` because `Close` can't wake a blocked non-pollable read. :243: "Sockets and pipes get [poller mediation] for free; character devices like /dev/tty do not" — and plain (blocking) fds cannot be interrupted by `Close`. These two lessons *are* the design of the closer.
- `cmd/pyry/main.go:1828+` (`printHelp`) and `main.go:13-27` (package doc verb list) — **Extract:** where to add the one-line `pyry acp` help entry and doc-comment verb line (AC#1 housekeeping; low cost).
- `docs/knowledge/features/acp-package.md` — the transport's shipped contract in prose (EOF→nil, over-long→wrapped error, diagnostics-isolation). Confirms what `Serve` guarantees so the subcommand only wires, never re-implements.

## Context

Epic #600 makes `pyry acp` speak the Agent Client Protocol (Zed-stewarded, JSON-RPC 2.0, line-delimited over stdio). The forked Claudian plugin spawns `pyry acp` as its agent subprocess and speaks JSON-RPC over the child's stdio pipes — **local, same-user, same-machine** (not a network or mobile peer). Transport mechanics (from Claudian's `AcpJsonRpcTransport.ts`): stdin = host→pyry, stdout = pyry→host, **stderr is captured by the host** (the host keeps its own ~8 KB diagnostics ring). That last fact is why this subcommand needs no ring buffer of its own.

#755 built the transport (`internal/acp`): framing, dispatch table, diagnostics discipline, and — explicitly — deferred "the closer that unblocks a blocked stdin read on shutdown" to **this ticket** (`acp.go:121-130`, and #755 spec Open questions). #756 is the **composition root**: register the verb, construct the transport over real stdio driven by a stderr-only logger, and serve until the host closes the pipe or a signal arrives. It registers **no** real handlers (later tickets do) and drives **no** claude. The inbound `session/*` method surface and claude driving are out of scope.

## Design

### 1. Dispatch + verb entry (`cmd/pyry/main.go` + new `cmd/pyry/acp.go`)

Add one case to `run()`'s switch (beside `agent-run`):

```go
case "acp":
    return runACP(os.Args[2:])
```

New sibling file `cmd/pyry/acp.go` holds `runACP` and the testable core `serveACP`.

`runACP` — the thin, non-testable shell (touches real process globals):

```go
// runACP serves the internal/acp transport over the process's real stdio,
// blocking until stdin reaches EOF or a signal cancels the context. Takes no
// flags and no positional arguments.
func runACP(args []string) error
```

Behaviour:
- **Arg rejection (AC#1):** `len(args) != 0` → return `fmt.Errorf("acp: unexpected arguments: %s", strings.Join(args, " "))` before any side effect. The subcommand takes no flags and no positionals. (Mirrors `runStatus`'s "unexpected arguments" guard, `main.go:1109`.)
- Build the signal context: `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)` + `defer cancel()` — identical to `runSupervisor` (`main.go:703`).
- Build a **plain stderr logger** (Design §2).
- `return serveACP(ctx, os.Stdin, os.Stdout, logger)`.

### 2. Logger — plain stderr, no ring buffer

```go
logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
```

Do **not** reuse `control.SlogTee` + `control.NewRingBuffer` (as `runSupervisor` does). The host owns the stderr ring for `pyry acp`; there is no `pyry logs` consumer for this subprocess. AC#5 requires only "diagnostics go to stderr, never stdout" — a bare `slog.NewTextHandler(os.Stderr, …)` satisfies it. This is the ticket's "simplify to fit" note taken at face value; adding a ring buffer here is dead weight.

`os.Stdout` is handed to `acp.New` as the transport's writer, so the transport's frames go to stdout and the logger's diagnostics go to stderr — the two streams are separated by construction (AC#5).

### 3. The serve core + the blocked-read closer (`serveACP` — AC#3, AC#4)

```go
// serveACP wires the acp transport over `stdin`/`stdout` with `logger` and
// blocks until stdin reaches EOF (returns nil) or ctx is cancelled by a signal
// (returns nil — a deliberate shutdown is a clean exit). A genuine stream break
// (over-long line / read error) while not shutting down returns a wrapped error.
// stdin is an io.Reader (not *os.File) so tests inject an in-memory reader.
func serveACP(ctx context.Context, stdin io.Reader, stdout io.Writer, logger *slog.Logger) error
```

Structural sketch (the goroutine choreography *is* the contract — kept under 20 lines):

```go
pr, pw := io.Pipe()
// closer (AC#4): on shutdown, unblock a Read blocked inside Serve's scanner.
go func() { <-ctx.Done(); _ = pr.CloseWithError(ctx.Err()) }()
// bridge: feed the pipe from real stdin; host EOF closes the pipe (AC#3).
go func() { _, _ = io.Copy(pw, stdin); _ = pw.Close() }()

t := acp.New(pr, stdout, logger) // registers NO handlers (AC#2)
err := t.Serve(ctx)
if ctx.Err() != nil {            // deliberate SIGINT/SIGTERM shutdown → exit 0
    return nil
}
if err != nil {                  // genuine stream break while running → error
    return fmt.Errorf("acp: %w", err)
}
return nil                        // EOF → exit 0
```

**Why an `io.Pipe` bridge instead of the ticket's suggested `os.Stdin.Close()`.** This is the load-bearing decision and a deliberate correction of the ticket's Technical Notes. `os.Stdin` is created by Go via `os.NewFile` with the non-pollable `kindNewFile`. A `Read` blocked on it parks *in the kernel*; `os.Stdin.Close()` from another goroutine cannot wake it — `Close` itself then blocks on `os.Stdin`'s `fdMutex` until the read returns, which on a quiet host never happens. This is the exact `#78` failure (`docs/lessons.md:92`, `:243`; `supervisor.go:753-757`). So closing `os.Stdin` **does not** satisfy AC#4 — it deadlocks it.

The `io.Pipe` bridge sidesteps the fd/poller machinery entirely: `Serve` reads the **in-memory** `PipeReader`, and `pr.CloseWithError(…)` synchronously unblocks any blocked `PipeReader.Read` (io.Pipe is a channel internally; a blocked reader observes the close immediately). So AC#4 is satisfied with zero syscall/nonblock/`/dev/fd` portability reasoning, and the closer (`pr.CloseWithError`) is trivially, deterministically correct.

**The residual bridge goroutine.** On the signal path, the `io.Copy(pw, stdin)` goroutine stays blocked on `os.Stdin.Read` and leaks. This is acceptable *here* and only here: `pyry acp` is a **one-shot subprocess exiting immediately** after `serveACP` returns (via `run()` → `main()` → process exit), so exactly one goroutine dies with the process. Contrast #78, where the identical leak was fatal because it recurred *per restart iteration* inside a long-lived supervisor, piling readers onto the shared `os.Stdin` `fdMutex` until the next `pty.Start` deadlocked. The no-leak `openTTYInput` machinery (dup/reopen + O_NONBLOCK + poller-mediated close) exists to defeat that *accumulation*; it buys nothing for a single terminal shutdown and would add fd-lifecycle and Go-version-pollability fragility to a composition-root verb. Simplicity-first (CLAUDE.md) says: accept the one-shot leak, document why. (If a future change makes `pyry acp` long-lived or re-entrant, revisit — the seam to swap in is a pollable reopened stdin behind the same `io.Reader` boundary `serveACP` already takes.)

**The `defer cancel()` drains the closer.** On the EOF path, `Serve` returns `nil` while the closer goroutine is still parked on `<-ctx.Done()`. When `runACP` returns, its `defer cancel()` fires → `ctx.Done()` closes → the closer wakes, calls `pr.CloseWithError` (a harmless no-op on the already-drained pipe), and exits. So the closer goroutine never outlives the call; only the stdin-bound bridge goroutine can (signal path), as above.

### Data flow

```
host stdin ──io.Copy──► pw ══io.Pipe══► pr ──► acp.Transport.Serve ──► host stdout (frames)
   (real *os.File)                                     │
                                                       └──► logger ──► stderr (diagnostics only)

SIGINT/SIGTERM ──► ctx.Done ──► pr.CloseWithError(ctx.Err()) ──► Serve's blocked Read unblocks ──► serveACP returns nil
host closes stdin ──► io.Copy EOF ──► pw.Close ──► Serve reads EOF ──► Serve returns nil ──► serveACP returns nil
```

## Concurrency model

- **Three goroutines during serve:** (1) the caller's goroutine blocked in `acp.Serve` (the transport's single read/dispatch loop — #755's model, unchanged); (2) the **bridge** (`io.Copy(pw, os.Stdin)`); (3) the **closer** (`<-ctx.Done()` → `pr.CloseWithError`). No shared mutable state between them beyond the `io.Pipe`, which is internally synchronized.
- **Shutdown sequence (signal):** signal → `signal.NotifyContext` cancels ctx → closer fires `pr.CloseWithError` → `Serve`'s `scanner.Scan()` returns false with the pipe-close error → `Serve` returns the wrapped error → `serveACP` sees `ctx.Err() != nil` → returns `nil` → `runACP` returns `nil` → `defer cancel()` (idempotent) → process exits 0.
- **Shutdown sequence (EOF):** host closes pipe → `io.Copy` returns → `pw.Close()` → `Serve` reads EOF → returns `nil` → `serveACP` sees `ctx.Err() == nil`, `err == nil` → returns `nil` → exits 0.
- **No `errgroup`, no join.** `serveACP` must **not** wait for the bridge goroutine — that would force it to block until a signal even on the EOF path (wrong), and the bridge is unjoinable on the signal path (blocked on `os.Stdin`). The goroutines are fire-and-forget, reaped by process exit / `defer cancel()`. This asymmetry is deliberate and documented inline.

## Error handling

The clean-exit mapping is the one subtlety, and it must be **`ctx.Err() != nil`**, not `errors.Is(err, context.Canceled)`:

| Exit condition | `Serve` returns | `ctx.Err()` | `serveACP` returns | Exit |
|---|---|---|---|---|
| Host closes stdin (EOF) | `nil` | `nil` | `nil` | 0 (AC#3) |
| Signal, cancelled **between** frames | `context.Canceled` | non-nil | `nil` | 0 (AC#4) |
| Signal, cancelled **during** a blocked read | wrapped `os.ErrClosed` (from forced `pr.CloseWithError`) | non-nil | `nil` | 0 (AC#4) |
| Over-long line / real read error, **not** shutting down | wrapped error | `nil` | `fmt.Errorf("acp: %w", err)` | 1 |

The third row is why `errors.Is(err, context.Canceled)` alone (the ticket's suggestion, and `runSupervisor`'s pattern) is **insufficient**: the forced-close path surfaces `os.ErrClosed`, not `context.Canceled`, so an `errors.Is(context.Canceled)` guard would misclassify a signal-driven shutdown as an error exit. Guarding on `ctx.Err() != nil` — "did *we* deliberately cancel?" — absorbs both the between-frames and the forced-close cases. It also correctly declines to swallow a genuine stream break, which only occurs with `ctx.Err() == nil`. (The pipe-close error passed to `CloseWithError` is `ctx.Err()`; the honest choice, though any non-nil value works since the `ctx.Err()` guard, not the error identity, drives the mapping.)

Diagnostics: all failures inside `Serve` are logged by the transport to the injected logger (stderr). `serveACP`/`runACP` add no stdout output on any path (AC#5). `acp.New` panics only on a nil reader/writer — both are always non-nil here (a live `io.Pipe` reader, `os.Stdout`), so that path is unreachable in production.

## Testing strategy

Same-package `cmd/pyry/acp_test.go`, stdlib `testing` only, table-driven where the shape allows, `-race`-clean. The `serveACP(ctx, io.Reader, io.Writer, *slog.Logger)` signature is what makes AC#3/AC#4 **deterministically testable without a TTY or real fds** — inject an in-memory reader, a `bytes.Buffer` writer, and a discard/`bytes.Buffer` slog sink. (CI runners have no terminal — the injected-reader design is required, not merely convenient.)

Scenarios (developer writes them in the project idiom):

- **AC#4 — blocked read unblocks promptly on cancel (the load-bearing test).** Inject a reader that blocks forever (a custom `blockingReader` whose `Read` waits on a channel, or the read end of an `os.Pipe()` with nothing written). Run `serveACP` in a goroutine; assert it has **not** returned yet; `cancel()` the ctx; assert `serveACP` returns `nil` within a short timeout (e.g. 2s via a `select` on a done-channel vs `time.After`). Fail if it hangs past the timeout — that is the #78 regression this ticket exists to prevent. Release the injected reader in `t.Cleanup` (close the channel / the `os.Pipe` write end) so the bridge goroutine drains and the test leaks nothing.
- **AC#3 — EOF returns nil.** Inject an already-EOF reader (`strings.NewReader("")` / `bytes.NewReader(nil)`); assert `serveACP` returns `nil` promptly with a non-cancelled ctx.
- **AC#3 — a few frames then EOF.** Inject `strings.NewReader` of one or two blank/whitespace lines (no handlers registered, so a real request would just 404; blank lines are skipped by the transport) followed by EOF; assert `nil` and that nothing spurious lands on the stdout buffer.
- **AC#5 — clean stream break maps to error, not exit-0.** Inject a reader delivering a single over-long line (> `maxLineBytes`) with a non-cancelled ctx; assert `serveACP` returns a non-nil error wrapping the transport's `acp: serve:` error, and that the diagnostics buffer (not the stdout buffer) carries the detail.
- **AC#1 — arg rejection.** `runACP([]string{"x"})` returns a non-nil error mentioning the unexpected argument, **without** blocking on stdin (the guard fires first). (`runACP(nil)` is not unit-tested directly — it blocks on real `os.Stdin`; its behaviour is covered transitively by the `serveACP` tests.)
- **Diagnostics isolation.** Assert the stdout `bytes.Buffer` is empty across the EOF and cancel scenarios (no handlers ⇒ no frames), and that only the stderr sink ever receives bytes.

`make check` (vet, race, staticcheck, substrate guard) must be green (AC#5). Substrate-guard is trivially green — `cmd/pyry/acp.go` names no claude-TUI substrate literals (it drives no claude), so no allowlist entry is needed.

## Open questions

- **e2e coverage (deferred, not required).** A full e2e — spawn a real `pyry acp`, close its stdin, assert exit 0; send SIGINT to a quiet one, assert exit 0 — would exercise the real `os.Stdin` path the unit tests stub. It is **out of scope**: it needs a new `acp` invocation mode in `internal/e2e/harness.go` (extra surface for an S ticket), and the unit tests already pin the load-bearing mechanism deterministically. If epic #600's later handler tickets add an e2e harness for `pyry acp`, fold a shutdown-exit-code assertion in then.
- **Pollable-stdin upgrade (deferred).** If `pyry acp` ever becomes long-lived or re-entrant, the one-shot bridge-goroutine leak stops being free; swap the `io.Pipe` bridge for a pollable reopened stdin (dup + `O_NONBLOCK` + poller-mediated `Close`, per `supervisor.openTTYInput`) behind the same `io.Reader` boundary `serveACP` already accepts. No wire-contract change. Deferred until observed need.
- **Help/doc-comment housekeeping (in scope, low cost).** Add a `pyry acp` line to `printHelp` (`main.go:1828+`) and the package-doc verb list (`main.go:13-27`), matching the `agent-run` entry's brevity: "serve the ACP JSON-RPC transport over stdio (spawned by an ACP host)".
