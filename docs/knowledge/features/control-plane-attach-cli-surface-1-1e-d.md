# Attach: CLI Surface (1.1e-D)

The Phase 1.1e end-to-end multi-session attach surface lands in `cmd/pyry/main.go` and `internal/control/attach_client.go`. Three changes, all dumb passthrough — the CLI does **not** parse, validate, or interpret the session selector. It is a string passed straight from `os.Args` to `AttachPayload.SessionID`. All resolution happens server-side in `Pool.ResolveID`.

### `parseClientFlags` returns positionals (and passes verb-specific flags through)

`parseClientFlags` (the shared helper for `status` / `stop` / `logs` / `attach` / `sessions`) surfaces `fs.Args()` plus everything the verb's own parser needs to see:

```go
func parseClientFlags(name string, args []string) (socketPath string, rest []string, err error)
```

A `splitClientFlags` helper (sibling of `splitArgs`, `cmd/pyry/main.go`) walks args left-to-right and peels recognised `-pyry-name` / `-pyry-socket` tokens off the front; everything else (verb-specific flags like `--stdio`, `--create-if-missing`; positionals; `--`) flows verbatim into `rest`. The internal `flag.FlagSet` only ever sees the extracted `pyryArgs`, so unknown-verb-flag errors land at the verb's own parser instead of the shared one (#167).

`splitClientFlags` stops at the first non-pyry-* token: pyry globals must precede sub-verb flags. Both `-pyry-socket=/tmp/x` and `-pyry-socket /tmp/x` (space-separated) forms are supported, as are `-` and `--` dash prefixes (`parseFlagSyntax` is reused). Trailing `-pyry-name` with no value is left in `pyryArgs` so the downstream `flag.FlagSet` produces the same error as before.

`runAttach` and `runSessions` consume `rest` via verb-specific parsers (`parseAttachArgs`, `parseSessionsNewArgs`, …) which already reject malformed input. `runStatus` / `runLogs` / `runStop` take no positionals; each adds an inline `len(rest) > 0` post-check — `fmt.Errorf("<verb>: unexpected arguments: %s", strings.Join(rest, " "))` — so unknown flags surface as errors instead of being silently swallowed (the regression risk introduced by the pass-through).

`TestRunAttachArgPath` (`cmd/pyry/args_test.go`) drives the full `runAttach` arg-parse composition (`parseClientFlags` → `parseAttachArgs`) so a regression of #167 fails CI without depending on the e2e harness — the gap that let the original bug ship.

### `runAttach` — optional positional after flags

```go
func runAttach(args []string) error {
    socketPath, rest, err := parseClientFlags("pyry attach", args)
    if err != nil { return err }
    sessionID, err := attachSelectorFromArgs(rest)
    if err != nil {
        fmt.Fprintln(os.Stderr, "pyry attach: too many arguments")
        fmt.Fprintln(os.Stderr, "usage: pyry attach [flags] [<id>]")
        os.Exit(2)
    }
    // ...unchanged: cols/rows from term.GetSize, control.Attach, ...
}
```

`attachSelectorFromArgs(rest []string) (string, error)` is the unit-testable seam. Empty rest → `""` (server resolves to bootstrap). One arg → that arg, verbatim. More than one → `errTooManyAttachArgs`.

Two design points:

- **`os.Exit(2)` for "too many arguments", not a returned error.** The convention in `main.go` is that `runFoo` returning an error becomes "exit 1 with the message printed by `main`" — semantically a runtime failure. Usage errors (the user typed the command wrong) are exit 2 by POSIX convention, visually distinct, and consistent with the existing `os.Exit(2)` at the top of `main` for argument-shape errors. The error/exit split is checkable: error return ⇒ runtime, `os.Exit(2)` ⇒ user typed wrong.
- **No client-side trimming or validation of `<id>`.** Whitespace-only, malformed UUID, mixed case — all pass through verbatim. `pyry attach " "` reaches the server's `Pool.ResolveID` which responds `ErrSessionNotFound`. The CLI's job is transport, not lint. UUID parsing and prefix logic in `cmd/pyry` or `internal/control/attach_client.go` is a regression — reviewers should `grep -rn 'HasPrefix\|uuid.Parse'` and reject any match.

### `control.Attach` — extended signature

```go
func Attach(ctx context.Context, socketPath string, cols, rows int, sessionID string) error
```

Extended in place rather than adding a sibling — there is exactly one external caller (`cmd/pyry/main.go runAttach`) and the function already builds the `AttachPayload`. The new arg flows into `AttachPayload.SessionID`; empty marshals to `{"cols":80,"rows":24}` under the `omitempty` tag (1.1e-C's load-bearing decision), preserving v0.5.x byte-identical wire output. Pinned by the `TestAttach_WireBackCompat_EmptySessionID` regression test from #101.

### Help text

```
  pyry attach [flags] [<id>]                     attach local terminal to daemon
                                                  (Ctrl-B d to detach; <id>
                                                  selects a session — full
                                                  UUID or unique prefix; omit
                                                  for the bootstrap session)
```

Terse, matches the surrounding block.

### Error propagation (no new code)

All four error classes already produce the correct behaviour with the changes above:

| Class | Path |
|---|---|
| Daemon not running | `dial` fails → `control.Attach` returns error → `runAttach` wraps `attach: %w` → exit 1 |
| Unknown id / ambiguous prefix | `handleAttach` (1.1e-C) encodes `Response.Error="attach: …"` → client returns `errors.New(resp.Error)` → wrapped `attach: %w` → exit 1 |
| `ErrBridgeBusy` | `Session.Attach` returns it → server encodes as `Response.Error` → same client path |
| Extra positionals | usage to stderr, `os.Exit(2)` |

The bridge-never-opened invariant is enforced server-side (1.1e-C). Nothing in the CLI can violate it: client sends one Request, reads one Response; if `resp.Error` is set, the client returns before raw mode and `io.Copy` is never started.

The doubled `attach: attach: …` wrapping (server prefixes once, client wraps again) is a known minor wart in the Phase 0 surface that the AC explicitly preserves — "messages match the existing surface, no rewording." Don't fix here.

### Tests

Two test files. Stdlib `testing` only.

- **`cmd/pyry/args_test.go` — `TestParseClientFlags_ReturnsRest`**: pins the seam — empty args, recognised flags only, single positional after flags, two positionals (passed through as a 2-len slice; "too many" is `runAttach`'s decision, not the parser's).
- **`internal/control/attach_test.go`** — extended with `TestAttach_PassesSessionID_OnWire` cases (no-arg → `""`, full-UUID, unique-prefix all reach the server with the expected `AttachPayload.SessionID`).

Resolver and bridge error paths are **not** re-tested through the CLI shell. `internal/control/attach_resolve_test.go` (1.1e-C) covers them exhaustively against the wire — the wire is the contract. Re-testing through the CLI wrapper would duplicate ground for no incremental confidence.
