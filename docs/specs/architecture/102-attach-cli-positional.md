# Spec #102 — Phase 1.1e-D: `pyry attach <id>` CLI positional + help text

**Status:** Architecture · **Size:** S · **Slice of:** Phase 1.1e (multi-session attach)
**Depends on:** #101 (`AttachPayload.SessionID` + `handleAttach` via `Pool.ResolveID`)
**Closes:** Phase 1.1e end-to-end CLI surface

---

## Context

Phase 1.1e wires multi-session attach end-to-end. #66 shipped the resolver primitive, #101 shipped the wire field + server-side routing. This is the third and final slice: expose the resolver on the command line.

End-user surface after this slice:

- `pyry attach` — empty selector flows through; server resolves to bootstrap. Byte-identical to v0.5.x today.
- `pyry attach <full-uuid>` — full UUID flows verbatim through `AttachPayload.SessionID`; server `Pool.ResolveID` maps it to that session.
- `pyry attach <prefix>` — unique prefix flows verbatim; server resolves. Ambiguous prefix → typed error on stderr, exit non-zero, no bridge opened.

The CLI does **not** parse, validate, or interpret the selector. It is a string passed straight from `os.Args` to `AttachPayload.SessionID`. All resolution happens server-side. This invariant is the entire reason #66 + #101 exist as separate slices — keeping the CLI dumb means new resolution rules don't need a CLI release.

## Design

### 1. `runAttach` — accept optional positional after flags

`cmd/pyry/main.go:420-442` `runAttach` currently calls `parseClientFlags("pyry attach", args)` which discards everything after the recognised flags. The Phase 0 code path is:

```go
func runAttach(args []string) error {
    socketPath, err := parseClientFlags("pyry attach", args)
    if err != nil { return err }
    // ... cols/rows, control.Attach, ...
}
```

The shared helper `parseClientFlags` (line 354) returns only the resolved socket path. To get at the positional after `-pyry-*` flags, the helper must surface `fs.Args()`. Change the signature:

```go
func parseClientFlags(name string, args []string) (socketPath string, rest []string, err error)
```

`rest` is `fs.Args()` — the positionals after `-pyry-*` flag parsing. Callers that don't expect positionals (`runStatus`, `runLogs`, `runStop`) bind it to `_` and behave exactly as today (silent ignore of stray args). Only `runAttach` consumes `rest`. This keeps the change scoped: no new validation contract for sibling verbs.

This is a 3-call-site signature change (status, logs, stop), all trivial — `socketPath, _, err := parseClientFlags(...)`. No semantic shift for those verbs; out-of-scope per AC, intentional.

`runAttach` becomes:

```go
func runAttach(args []string) error {
    socketPath, rest, err := parseClientFlags("pyry attach", args)
    if err != nil { return err }

    var sessionID string
    switch len(rest) {
    case 0:
        // empty selector → server resolves to bootstrap.
    case 1:
        sessionID = rest[0]
    default:
        fmt.Fprintln(os.Stderr, "pyry attach: too many arguments\nusage: pyry attach [flags] [<id>]")
        os.Exit(2)
    }

    // ... unchanged: cols/rows from term.GetSize ...

    fmt.Fprintln(os.Stderr, "pyry: attached. Press Ctrl-B d to detach.")
    if err := control.Attach(context.Background(), socketPath, cols, rows, sessionID); err != nil {
        return fmt.Errorf("attach: %w", err)
    }
    fmt.Fprintln(os.Stderr, "\npyry: detached.")
    return nil
}
```

Two design points worth pinning:

- **`os.Exit(2)` for too-many-args, not a returned error.** The convention in this file is that `runFoo` returning a non-nil error becomes "exit 1 with the error printed by `main`" — semantically the same shell exit class as a successful-but-failed RPC. Usage errors (the user typed the command wrong) are exit 2 by POSIX convention and should be visually distinct. The existing CLI already uses `os.Exit(2)` for argument shape errors at the top of `main`; staying consistent.
- **No client-side trimming or validation of `<id>`.** Whitespace-only, case-mangled, malformed UUID — all pass through. If the user typed `pyry attach " "`, that's a string the server's `Pool.ResolveID` will see verbatim and respond `ErrSessionNotFound` to. The CLI's job is transport, not lint.

### 2. `control.Attach` — accept `sessionID` argument

`internal/control/attach_client.go:37` Phase 0 signature:

```go
func Attach(ctx context.Context, socketPath string, cols, rows int) error
```

Extend, not sibling — there is exactly one external caller (`cmd/pyry/main.go:437`) and the function already builds the `AttachPayload`. Adding a sibling `AttachWithID` would split the call surface for no benefit.

```go
func Attach(ctx context.Context, socketPath string, cols, rows int, sessionID string) error {
    // ... unchanged dial + Encode, except:
    if err := json.NewEncoder(conn).Encode(Request{
        Verb:   VerbAttach,
        Attach: &AttachPayload{Cols: cols, Rows: rows, SessionID: sessionID},
    }); err != nil { ... }
    // ... rest unchanged.
}
```

The doc comment grows one paragraph naming the new arg's shape (full UUID, unique prefix, or empty for bootstrap) and pointing at `Pool.ResolveID` for the resolution rules. Don't restate ResolveID's rules — link by name.

Empty `sessionID` produces `AttachPayload{Cols: cols, Rows: rows, SessionID: ""}`, which under the `omitempty` tag (#101's load-bearing decision) marshals to `{"cols":80,"rows":24}` — byte-identical to Phase 0. The wire-back-compat regression test #101 already added (`TestAttach_WireBackCompat_EmptySessionID`) continues to pin this.

### 3. Help text

`cmd/pyry/main.go:574` currently:

```
  pyry attach [flags]                            attach local terminal to daemon
                                                  (Ctrl-B d to detach)
```

Update:

```
  pyry attach [flags] [<id>]                     attach local terminal to daemon
                                                  (Ctrl-B d to detach; <id>
                                                  selects a session — full
                                                  UUID or unique prefix; omit
                                                  for the bootstrap session)
```

Match the surrounding terseness; no separate "Examples" addition for `<id>` — the existing example block (`pyry attach`) still reads correctly, and Phase 1.1e's `pyry sessions` examples land elsewhere.

### Error propagation (no new code)

All four error classes the AC enumerates already produce the right behaviour with the changes above:

| Class | Current path | After this slice |
|---|---|---|
| Daemon not running | `dial` fails → `control.Attach` returns error → `runAttach` wraps `attach: %w` → exit 1 | unchanged |
| Unknown id / ambiguous prefix | `handleAttach` (#101) encodes `Response.Error="attach: …"` → client decodes `resp.Error != ""` → `errors.New(resp.Error)` → `runAttach` wraps `attach: %w` → stderr, exit 1 | unchanged |
| `ErrBridgeBusy` (same session, second attach) | `Session.Attach` returns `supervisor.ErrBridgeBusy` → server encodes as `Response.Error` → same client path | unchanged |
| Extra positionals | n/a (no positional accepted) | new: stderr usage line, `os.Exit(2)` |

The bridge-never-opened invariant is enforced server-side already (#101's spec, "Bridge state invariant" section). Nothing in the CLI can violate it: the client sends one Request and reads one Response; if `resp.Error` is set, the client returns before entering raw mode and `io.Copy(os.Stdout, conn)` is never started.

## Concurrency model

No new goroutines, no new channels, no new locks. The CLI is a one-shot RPC client: send Request, read Response, optionally bridge. The bridge goroutine (`go func() { io.Copy(os.Stdout, conn) }()`) is only started after a non-error ack — same as today.

## Error handling

CLI-side error handling is unchanged from Phase 0 except for the new "too many positionals" branch:

- `parseClientFlags` errors (unknown flag, etc.) → propagated up as `error`, top of `main` prints and exits 1.
- `len(rest) > 1` → usage to stderr, `os.Exit(2)` directly. No error return — the user typed the command wrong, not a runtime failure.
- `control.Attach` returns any error → `runAttach` wraps as `fmt.Errorf("attach: %w", err)`. Existing surface; resolver / bridge errors come through verbatim because `handleAttach` already encoded them as `"attach: <err>"` server-side, so the doubled wrapping ("attach: attach: …") is a known minor wart in the Phase 0 surface that the AC explicitly preserves ("Messages match the existing surface — no rewording"). Don't fix here.

## Testing strategy

Two test files touched. Stdlib `testing` only; no helper-process pattern needed (the AC's reference to "TestHelperProcess coverage" is loose — `runAttach` is unit-testable in-process for argument parsing, and the resolver behaviours are already covered by `internal/control/attach_resolve_test.go` from #101).

### `cmd/pyry/args_test.go` — extend

Add `TestParseClientFlags_ReturnsRest`:

- `args=nil` → `rest=nil`.
- `args=["-pyry-name", "elli"]` → `rest=nil`.
- `args=["foo"]` → `rest=["foo"]`.
- `args=["-pyry-name", "elli", "abc-123"]` → `rest=["abc-123"]`.
- `args=["abc-123", "extra"]` → `rest=["abc-123", "extra"]`.

This is the seam — proving `parseClientFlags` surfaces positionals correctly is enough; the further behaviour (selector flow-through, exit-2 on too-many) is `runAttach`'s responsibility.

### `cmd/pyry/attach_test.go` — new file

Two reasons to keep this in a new file rather than extending `args_test.go`:

1. `runAttach` requires a live socket to exercise its happy path. The test file picks up a `net.Listen("unix", filepath.Join(t.TempDir(), "pyry.sock"))` fixture that has no place in `args_test.go`'s pure-function focus.
2. Future Phase 1.1f changes to the attach client (resize forwarding, session-switch escape sequence) will keep landing in this same file — naming it `attach_test.go` matches the implementation file naming convention.

Test cases:

- **`TestRunAttach_TooManyPositionals_ExitsTwoWithUsage`** — invoking the exit path requires intercepting `os.Exit`, which Go's testing model doesn't natively support without `osexit`-pattern indirection. Pragmatic choice: extract a tiny helper `attachUsageError(rest []string) error` (returns a sentinel) and have `runAttach` either return that error and let `main` produce exit code 2, OR keep the inline `os.Exit(2)` and test the helper's error-message construction in isolation. Recommended path: leave `os.Exit(2)` inline in `runAttach` (matches the file's convention for true usage errors), and instead test the `len(rest) > 1` detection by extracting:

  ```go
  // attachSelectorFromArgs returns the session selector string from the
  // post-flag remainder. Empty rest → "" (bootstrap). One arg → that arg.
  // More than one → ErrTooManyArgs.
  func attachSelectorFromArgs(rest []string) (string, error) { ... }
  ```

  `runAttach` calls this helper; on `ErrTooManyArgs` it prints the usage line and `os.Exit(2)`. The helper is straightforward to table-test:

  ```go
  cases := []struct{ in []string; want string; wantErr bool }{
      {nil, "", false},
      {[]string{}, "", false},
      {[]string{"abc"}, "abc", false},
      {[]string{"abc", "def"}, "", true},
      {[]string{"abc", "def", "ghi"}, "", true},
  }
  ```

- **`TestRunAttach_NoArgs_BootstrapSelector`** + **`TestRunAttach_OnePositional_PassesThrough`** — exercise the full `runAttach` path against a fake socket. Spin up a `net.Listener` in a temp dir, point `-pyry-socket` at it, accept one conn, decode the `Request`, assert `Request.Attach.SessionID == ""` (no-arg) or `== "<expected>"` (positional). Hand back `{"ok":true}` and immediately close to exit raw mode quickly. The bridge goroutine's `io.Copy` returns on close; no terminal needed because `term.IsTerminal(stdinFd)` returns false under `go test`, so the raw-mode branch is skipped.

  Keep these tests off the `t.Parallel()` ladder if they touch `os.Stdin` / `os.Stdout` (they read terminal geometry from `os.Stdout.Fd()`); test geometry codepath is non-TTY-friendly because `term.IsTerminal` returns false and `cols, rows` stay zero. That's fine — the assertion is on `SessionID`, not geometry.

- **Resolver and bridge error paths are NOT re-tested here.** They are covered exhaustively by `internal/control/attach_resolve_test.go` (#101) and `internal/control/attach_test.go` (Phase 0). Re-testing through the CLI shell would duplicate ground for no incremental confidence — the wire is the contract, not the CLI wrapper.

### `internal/control/attach_test.go` — extend

Existing `TestServer_AttachHandshakeAndStream` and friends already encode/decode `AttachPayload` round-trips. They'll pass unchanged after the `Attach` signature grows a fifth arg, because the test code constructs the `AttachPayload` directly — but the call sites that invoke `control.Attach()` (if any in this file — verify) need a new `""` literal in the new arg position. Sweep with `go vet`.

`go test -race ./...` and `go vet ./...` clean per AC.

## Open questions

None blocking.

- **`AttachSelectorFromArgs` exporting?** The helper is internal to `cmd/pyry`. Lowercase. No other package imports `cmd/pyry`.
- **Should `parseClientFlags`'s new `rest` return be propagated to `runStatus` / `runLogs` / `runStop` as a "reject extra args" check?** Out of scope per AC — AC d only mandates rejection for `pyry attach`. Opportunistic, not a precondition; defer.

## Out of scope (regression guards, not deliverables)

- No UUID parsing or prefix logic in `cmd/pyry` or `internal/control`. Reviewer should `grep -rn 'HasPrefix\|uuid.Parse' cmd/pyry/ internal/control/attach_client.go` and reject any match.
- No new flags. The selector is a positional, not `-pyry-session` or similar.
- No minimum prefix length enforcement at the CLI. Server-side enforcement is also deferred until ergonomics complaint (#102 scope locked this in).
- No session-switching from inside an attached session. Would need a mediated escape sequence (Phase 2.0+).
- No live SIGWINCH propagation through the attach. Detach and reattach to update geometry — Phase 0 caveat documented in `protocol.go:51-58` stays intact.
- No refactor of `runStatus` / `runLogs` / `runStop` to also reject extra positionals. Same-shape change, opportunistic, not required.
