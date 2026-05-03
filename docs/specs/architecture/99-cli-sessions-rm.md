# #99 — `pyry sessions rm` CLI router + verb + UUID-prefix resolution

Phase 1.1d-B2. Consumes the `sessions.rm` wire surface and `Remover` seam
landed by #98 (typed `JSONLPolicy` enum on `SessionsPayload`, typed-error
sentinels propagated via `Response.ErrorCode`, `control.SessionsRm` client
wrapper) and the `sessions.list` wire landed by #87 (read-only snapshot of
the in-memory pool, used here for client-side prefix resolution).

This ticket adds a single switch case to `runSessions` (#76's router) plus
the consumer-facing handler. The non-trivial design choice is **where
prefix resolution lives**: client-side via `control.SessionsList`, not a
new wire verb. The reasoning is laid out in the Design section.

This is the first CLI verb in Phase 1.1 to:

1. Take a UUID-or-prefix `<id>` argument (sibling #92 took full UUID
   only).
2. Emit AC-prescribed user-facing error messages (no `pyry: sessions rm:`
   prefix wrap) for the three named-error cases — ambiguous prefix,
   unknown UUID, bootstrap rejection.
3. Use `os.Exit(2)` for usage errors (the mutually-exclusive-flags
   guard) and `os.Exit(1)` for typed runtime errors, matching
   `runAttach`'s precedent.

## Files to read first

- `cmd/pyry/main.go:486-561` — `runSessions` router and `runSessionsNew`.
  The `case "rm":` slot, the `sessionsVerbList` constant ("new" → "new,
  rm"), and the new `runSessionsRm` helper all live here. `parseSessionsNewArgs`
  is the precedent shape for `parseSessionsRmArgs`.
- `cmd/pyry/main.go:429-484` — `errTooManyAttachArgs` + `attachSelectorFromArgs`
  + `runAttach`. The "extracted helper for arg-shape testing" pattern,
  `os.Exit(2)` on the usage-error path, and the printed-message-without-
  `pyry:`-prefix shape all carry over to `parseSessionsRmArgs` /
  `runSessionsRm`.
- `cmd/pyry/main.go:362-374` — `parseClientFlags`. The `runSessions`
  router has already peeled the `-pyry-socket` / `-pyry-name` flags
  before dispatching to `runSessionsRm`; the sub-handler receives only
  the post-flag remainder.
- `cmd/pyry/sessions_test.go:66-107` — `TestParseSessionsNewArgs`
  table-driven test. The new `TestParseSessionsRmArgs` follows the
  same shape (each row is `{name, args, wantID, wantPolicy, wantErr}`)
  and exercises every flag-parsing edge case without dialling a
  socket.
- `internal/control/client.go:96-153` — `SessionsNew` and `SessionsRm`
  wire wrappers. `SessionsRm`'s typed-error mapping (`ErrCodeSessionNotFound`
  → `sessions.ErrSessionNotFound`, `ErrCodeCannotRemoveBootstrap` →
  `sessions.ErrCannotRemoveBootstrap`) is the contract the CLI matches
  with `errors.Is`.
- `internal/control/client.go:188-217` — `SessionsList(ctx, sock)
  ([]SessionInfo, error)`. The wire endpoint the prefix resolver
  consumes. Error responses surface as `errors.New(resp.Error)`; a
  failed dial bubbles up as `dial socket: ...`. No typed-sentinel
  mapping here — `Pool.List` doesn't return sentinels.
- `internal/control/protocol.go:202-237` — `SessionsListPayload` /
  `SessionInfo` shape. `ID` (string), `Label`, `Bootstrap` (omitempty)
  — the three fields the resolver uses. `State` and `LastActive` are
  ignored at this layer.
- `internal/control/protocol.go:120-160` — `JSONLPolicy` wire enum
  (`JSONLPolicyLeave` / `JSONLPolicyArchive` / `JSONLPolicyPurge`) and
  `ErrorCode` enum (`ErrCodeSessionNotFound` /
  `ErrCodeCannotRemoveBootstrap`). The CLI passes the policy directly
  to `SessionsRm`; `""` (zero value) is normalised to `JSONLLeave`
  server-side.
- `internal/sessions/pool.go:31-49` — `ErrSessionNotFound`,
  `ErrCannotRemoveBootstrap`, `ErrAmbiguousSessionID` sentinel
  declarations. The CLI imports `sessions` for `errors.Is` matching;
  no type assertions, no message-string comparisons.
- `internal/sessions/pool.go:609-686` — `Pool.ResolveID` +
  `ambiguousError`. The reference implementation of UUID-or-prefix
  resolution. The CLI mirrors the resolution order (exact match
  first, then prefix scan) so server-side and client-side behaviour
  agree byte-for-byte. **Note**: the CLI's ambiguous-output format
  differs slightly — `<uuid> <label>` (space) vs.
  `ambiguousError`'s `<uuid> (<label>)` (parens). Per AC#3 the CLI
  uses the space form.
- `internal/e2e/sessions_new_test.go` (whole file) — template for
  the new e2e tests. `StartIn`/`Run`/`RunBare`, `newRegistryHome` /
  `readRegistry` / `waitForBootstrap` / `writeSleepClaude` helpers
  are reused verbatim.
- `internal/e2e/cli_verbs_test.go:49-73` — `TestStatus_E2E_Stopped`,
  the `RunBare(t, "sessions", "-pyry-socket="+bogus, "rm", ...)`
  pattern for the no-daemon e2e case.
- `docs/specs/architecture/76-cli-sessions-new.md` § "Sub-router
  argument shape" — establishes the convention `pyry [global-flags]
  sessions <verb> [verb-flags] [positionals]`. `runSessionsRm`
  inherits this verbatim.
- `docs/specs/architecture/98-control-sessions-rm.md` § "Wire
  surface (protocol.go)" and § "Client wrapper (client.go)" — the
  upstream surface this ticket consumes. Confirms typed-error
  propagation works end-to-end (`errors.Is(err,
  sessions.ErrSessionNotFound)` after JSON round-trip).

## Context

`pyry sessions new` (#76) lands the router; `sessions.rm` wire (#98) and
`sessions.list` wire (#87) land the seams. This ticket consumes both:
the operator types `pyry sessions rm <prefix>`, the CLI resolves the
prefix to a canonical UUID via one `sessions.list` call, then issues
one `sessions.rm` with the resolved UUID and the chosen JSONL policy.

Three operational shapes the CLI handler must satisfy:

1. **Argument parsing.** `[--archive|--purge] <id>` with arity-1
   positional. Mutually exclusive flags trip a usage error (exit 2).
   Wrong arity also exits 2. All parsing is testable without a
   socket — extracted into `parseSessionsRmArgs` mirroring
   `parseSessionsNewArgs`.
2. **Prefix resolution.** Client-side, against the `sessions.list`
   wire result. Mirrors `Pool.ResolveID`'s resolution order: empty
   `<id>` is rejected at parse time (arity check), exact UUID match
   wins over a prefix that would also match the same UUID, single
   prefix match returns the canonical UUID, multiple prefix matches
   render the AC#3 multi-line "ambiguous" error.
3. **Error mapping.** `errors.Is` against the two sentinels the wire
   propagates plus the local `errAmbiguousPrefix` sentinel. Each
   matched case writes the AC-prescribed plain-text message to
   stderr (no `pyry:` or `pyry sessions rm:` prefix) and exits 1.
   Other errors flow through the standard `fmt.Errorf("sessions rm:
   %w", err)` wrap, which `main` prefixes with `pyry: ` on its way
   to stderr — matches `runStop` / `runStatus`.

This ticket scope is **CLI-layer only**. No new wire surface, no
`Pool` work, no e2e harness changes. The mechanics of removal
(child termination, registry write, JSONL disposition) are entirely
behind the wire.

## Design

### Top-level dispatch (cmd/pyry/main.go)

Three changes to `runSessions`:

```go
// Updated constant — verb list grows by one. #76's spec calls out
// that 1.1b/c/d/e each append one verb here in the same edit that
// adds the case. This is the 1.1d-B2 increment.
const sessionsVerbList = "new, rm"

// In runSessions's switch:
switch sub {
case "new":
    return runSessionsNew(socketPath, subArgs)
case "rm":
    return runSessionsRm(socketPath, subArgs)
default:
    return errSessionsUsage(fmt.Sprintf("unknown verb %q", sub))
}
```

The router's signature, the `parseClientFlags` peel, and the
sub-arg dispatch shape are unchanged. Adding `case "rm":` is the
entire router-level diff.

### Argument parsing (parseSessionsRmArgs)

```go
// errSessionsRmFlagsExclusive marks the mutually-exclusive
// --archive / --purge guard. Mapped to os.Exit(2) at the
// runSessionsRm boundary. Sentinel exists so the parser stays
// testable (a returned error matched by errors.Is) and runSessionsRm
// owns the exit-2 policy decision.
var errSessionsRmFlagsExclusive = errors.New("--archive and --purge are mutually exclusive")

// parseSessionsRmArgs parses `[--archive|--purge] <id>`.
// Returns (id, policy, err); policy is the wire enum
// (control.JSONLPolicy) — empty when neither --archive nor --purge
// was set, which the server treats as JSONLPolicyLeave (the default).
//
// Mirrors parseSessionsNewArgs's shape: extracted from runSessionsRm
// so flag-parsing rules are unit-testable without dialling the
// control socket.
func parseSessionsRmArgs(args []string) (id string, policy control.JSONLPolicy, err error) {
    fs := flag.NewFlagSet("pyry sessions rm", flag.ContinueOnError)
    fs.SetOutput(os.Stderr)
    archive := fs.Bool("archive", false, "archive the on-disk JSONL transcript")
    purge := fs.Bool("purge", false, "delete the on-disk JSONL transcript (default: leave)")
    if err := fs.Parse(args); err != nil {
        return "", "", err
    }
    if *archive && *purge {
        return "", "", errSessionsRmFlagsExclusive
    }
    if fs.NArg() != 1 {
        return "", "", fmt.Errorf("expected <id>, got %d positional args", fs.NArg())
    }
    switch {
    case *archive:
        policy = control.JSONLPolicyArchive
    case *purge:
        policy = control.JSONLPolicyPurge
    default:
        // Empty policy — wire layer normalises to JSONLPolicyLeave.
        // Sending the explicit token would also work; "" keeps the
        // wire shape clean (omitempty drops the field) and matches
        // sessions.JSONLLeave's zero-value default.
        policy = ""
    }
    return fs.Arg(0), policy, nil
}
```

**Why an unexported sentinel for the mutual-exclusion error.**
`runSessionsRm` needs to distinguish *usage* errors (exit 2, no
`pyry:` prefix on the message) from *runtime* errors (exit 1,
wrapped via `fmt.Errorf("sessions rm: %w", err)`). The
`errTooManyAttachArgs` precedent (`cmd/pyry/main.go:433`) uses the
same shape: a package-private sentinel the caller matches with
`errors.Is`, exits 2, and prints a usage line. Wrong-arity also
exits 2; reusing the same exit-code policy applies to it via the
final `errors.Is(err, errSessionsRmFlagsExclusive) || strings.HasPrefix(err.Error(), "expected <id>")`
check — but the cleaner shape is to wrap the arity error in the
same sentinel family. See "Why one sentinel, not two" below.

**Why one sentinel, not two.** The arity error and the
mutual-exclusion error are both *usage* errors — they should both
exit 2 with no `pyry:` prefix. Using one shared `errSessionsRmUsage`
sentinel wraps both:

```go
var errSessionsRmUsage = errors.New("usage")  // marker only

// arity branch:
return "", "", fmt.Errorf("%w: expected <id>, got %d positional args", errSessionsRmUsage, fs.NArg())
// flags branch:
return "", "", fmt.Errorf("%w: --archive and --purge are mutually exclusive", errSessionsRmUsage)
```

`runSessionsRm` matches `errors.Is(err, errSessionsRmUsage)` → exit 2,
`fmt.Fprintln(os.Stderr, "pyry sessions rm:", err)`. The
`flag.Parse` failure (unknown flag, etc.) is also a usage error;
`flag` already prints to stderr, so we just exit 2 without an
extra Fprintln to avoid duplicate output. Match this with
`errors.Is(err, flag.ErrHelp)` (for `-h`) or by looking at `err`
not being any of our sentinels — the simplest shape is: any error
from `parseSessionsRmArgs` is a usage error; exit 2; `flag` already
printed if it wanted to.

**Decision: collapse to one sentinel + uniform exit-2 policy.**
The trade-off (one wrapped error vs two named sentinels) lands on
"one wrapper" because every error path out of `parseSessionsRmArgs`
is, definitionally, a usage error. `runSessionsRm` doesn't need to
discriminate further; the wire-call path is reached only on
parse-success.

```go
// In runSessionsRm:
id, policy, err := parseSessionsRmArgs(args)
if err != nil {
    if !errors.Is(err, flag.ErrHelp) {
        // flag.Parse already wrote its own diagnostic on parse
        // failures; only print our wrapped errors. errSessionsRmUsage
        // -wrapped errors carry the AC-prescribed text directly.
        if errors.Is(err, errSessionsRmUsage) {
            fmt.Fprintln(os.Stderr, "pyry sessions rm:", err)
        }
    }
    os.Exit(2)
}
```

This is intentionally identical in behaviour to `runAttach`'s
`errTooManyAttachArgs` branch.

### Prefix resolution (resolveSessionIDViaList)

```go
// errAmbiguousPrefix carries the formatted multi-line "ambiguous
// prefix" message. The unexported sentinel exists so runSessionsRm
// can branch with errors.Is rather than string-matching the message.
//
// The wrapped message is the AC#3 user-facing format: each match on
// its own line as `<uuid> <label>` (space-separated), sorted by
// SessionID ascending. Bootstrap entries with empty labels render as
// "<uuid> bootstrap" — mirrors Pool.ambiguousError's substitution.
var errAmbiguousPrefix = errors.New("ambiguous session id prefix")

// resolveSessionIDViaList resolves a user-supplied UUID-or-prefix to
// a canonical SessionID by listing every session via the wire and
// filtering client-side. Returns the canonical UUID on success.
//
// Resolution order (mirrors Pool.ResolveID):
//   1. Exact ID match wins outright (one map-equivalent pass).
//   2. Otherwise scan with strings.HasPrefix; one match → that ID.
//      Zero → sessions.ErrSessionNotFound. Multiple → errAmbiguousPrefix
//      with each match formatted as "<uuid> <label>" on its own line.
//
// Empty arg is rejected at parseSessionsRmArgs (arity check); this
// function may assume arg != "". The exact-vs-prefix order matters:
// a full UUID always wins, even if some prefix of it would also
// match the same row (a no-op on practical UUIDs but the order is
// part of Pool.ResolveID's documented contract; we mirror it for
// behavioural parity).
//
// Lift-out point: this is the first of two/three CLI callers that
// need prefix resolution (sibling: rename's prefix slice; future:
// attach refactor #49). Per the ticket body, do NOT extract a
// shared helper now. Phase 1.1e (#49)'s architect makes the call
// when the third caller arrives.
func resolveSessionIDViaList(ctx context.Context, socketPath, arg string) (string, error) {
    list, err := control.SessionsList(ctx, socketPath)
    if err != nil {
        return "", err
    }
    for _, s := range list {
        if s.ID == arg {
            return s.ID, nil
        }
    }
    var matches []control.SessionInfo
    for _, s := range list {
        if strings.HasPrefix(s.ID, arg) {
            matches = append(matches, s)
        }
    }
    switch len(matches) {
    case 0:
        return "", sessions.ErrSessionNotFound
    case 1:
        return matches[0].ID, nil
    default:
        sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
        var b strings.Builder
        for i, m := range matches {
            label := m.Label
            if m.Bootstrap && label == "" {
                label = "bootstrap"
            }
            if i > 0 {
                b.WriteByte('\n')
            }
            fmt.Fprintf(&b, "%s %s", m.ID, label)
        }
        return "", fmt.Errorf("%w:\n%s", errAmbiguousPrefix, b.String())
    }
}
```

**Why client-side resolution (not a server-side wire change).**
Three options were considered:

1. **Add an `ErrCodeAmbiguous` to the wire and have `sessions.rm`
   accept a prefix.** Server-side resolution; `Pool.ResolveID`
   already exists. **Rejected.** Extending the wire surface
   (a third `ErrorCode` value, plus deciding how to ship the list
   of matches over JSON for the CLI to render) is more change than
   adding a single switch case to `runSessions`. The CLI still
   needs `sessions.list` for `pyry sessions list` (#88) — the wire
   call already exists; reusing it costs one extra round-trip per
   `rm` invocation.
2. **Expose `Pool.ResolveID` over a new `sessions.resolve` wire
   verb.** Same complaint, plus introduces a "verb that exists only
   to support another verb" smell. **Rejected.**
3. **Resolve client-side via `control.SessionsList`.** Two wire
   round-trips per `rm` invocation (list + rm). **Chosen.** The
   list size is bounded by the number of sessions (small even at
   the upper end of Phase 1.1's expected scale — handful to low
   hundreds), the wire bandwidth is negligible, and the client
   gets full control over the AC#3 ambiguous-output format.

The cost is two RTTs vs one. The benefit is a stable wire surface
and future-proof CLI ergonomics (the same `resolveSessionIDViaList`
satisfies `attach`, `rename`-prefix-slice, and any future
`<id>`-taking verb without a wire change each time).

**Why mirror `Pool.ResolveID`'s order rather than just prefix-scan.**
A user running `pyry sessions rm <full-uuid>` expects the verb to
work even if the same UUID matches a prefix of itself (which it
does). Skipping the exact-match short-circuit and going straight
to `HasPrefix` would still match the same row exactly once for a
full UUID — but only because no two UUIDs share a 36-char prefix.
The exact-match short-circuit is documented contract on
`Pool.ResolveID`; mirroring it here means the CLI's behaviour
matches the `ResolveID` reference if we ever do shift to
server-side resolution.

**No `sort.Slice` import collision.** `cmd/pyry/main.go` does not
currently import `sort`. Adding it is one line in the import block.

### Handler (runSessionsRm)

```go
// runSessionsRm implements `pyry sessions rm [--archive|--purge] <id>`:
// resolve the (possibly-prefix) <id> via sessions.list, dial the
// daemon's control socket, ask it to terminate the named session,
// remove its registry entry, and apply the JSONL disposition policy.
//
// Exit codes (matches the rest of cmd/pyry):
//   0 — removal succeeded.
//   1 — runtime error: ambiguous prefix, unknown id, bootstrap
//       rejection, server-side error (evict failure, ...), or
//       no-daemon dial failure.
//   2 — usage error: parsing failure, mutually-exclusive flags,
//       wrong arity. (Identical to runAttach's exit-2 policy.)
//
// The three AC-prescribed messages (ambiguous, unknown, bootstrap)
// print to stderr WITHOUT the standard `pyry:` outer-error prefix.
// Other errors flow through `fmt.Errorf("sessions rm: %w", err)`,
// which main's top-level error printer wraps as `pyry: sessions rm:
// <err>` — matches runStop / runStatus.
func runSessionsRm(socketPath string, args []string) error {
    id, policy, err := parseSessionsRmArgs(args)
    if err != nil {
        if errors.Is(err, errSessionsRmUsage) {
            fmt.Fprintln(os.Stderr, "pyry sessions rm:", err)
        }
        os.Exit(2)
    }

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    canonical, err := resolveSessionIDViaList(ctx, socketPath, id)
    if err != nil {
        switch {
        case errors.Is(err, errAmbiguousPrefix):
            fmt.Fprintln(os.Stderr, err.Error())
            os.Exit(1)
        case errors.Is(err, sessions.ErrSessionNotFound):
            fmt.Fprintf(os.Stderr, "no session with id %q\n", id)
            os.Exit(1)
        }
        return fmt.Errorf("sessions rm: %w", err)
    }

    if err := control.SessionsRm(ctx, socketPath, canonical, policy); err != nil {
        switch {
        case errors.Is(err, sessions.ErrCannotRemoveBootstrap):
            fmt.Fprintln(os.Stderr, "cannot remove bootstrap session")
            os.Exit(1)
        case errors.Is(err, sessions.ErrSessionNotFound):
            // Pool.Remove can race with another concurrent removal:
            // we resolved the canonical UUID, then someone else
            // removed it before our SessionsRm call landed. Surface
            // the original (unresolved) <id> in the message — the
            // user typed it, so it's the meaningful identifier.
            fmt.Fprintf(os.Stderr, "no session with id %q\n", id)
            os.Exit(1)
        }
        return fmt.Errorf("sessions rm: %w", err)
    }
    return nil
}
```

**Why `os.Exit(1)` from the AC-message branches rather than returning
an error.** `main`'s top-level error printer prepends `pyry: ` and
exits 1 on any non-nil return from `run()`. The AC explicitly
specifies the three message texts without a `pyry:` prefix. The
options:

1. **Return a sentinel and have `main` strip `pyry:` selectively.**
   Rejected — adds top-level coupling.
2. **`fmt.Fprintln(os.Stderr, ...)` + `os.Exit(1)` from the
   handler.** Chosen. Same shape `runAttach` uses for its exit-2
   path. The trade-off: tests must capture stderr + observe the
   exit code rather than a returned error. The `TestHelperProcess`
   pattern already does both (e2e tests check `r.ExitCode` and
   `r.Stderr`).

**Why the `os.Exit` branches are still correct under deferred
cleanup.** `runSessionsRm`'s only deferred work is `cancel()`. On
`os.Exit`, deferred functions don't run — but `cancel()`'s only
effect is releasing the context's timer. The 30s timer is a process-
local resource that the kernel reaps on `os.Exit`; no socket, no
file, no goroutine outlives the call. Safe.

**Why concurrent-removal race emits the original `<id>`, not the
canonical UUID.** The race window is: client lists → resolves
prefix → calls `sessions.rm` → server returns `ErrSessionNotFound`
(another client removed it in between). The user typed `<id>`
(possibly a prefix); echoing the *prefix* in the error preserves
context: "the thing you asked to remove isn't here". Echoing the
*canonical UUID* would surface a string the user never typed.
Matches operator-debugging convention.

### Help text update (printHelp)

One line in the `pyry sessions <verb>` block (cmd/pyry/main.go:698):

```go
//   pyry sessions <verb> [flags]                   manage sessions on a running
//                                                  daemon (verbs: new, rm)
```

Single-character edit (`new` → `new, rm`). The verbs:- prefix and
the second-line continuation already exist; #76's spec called out
that 1.1b/c/d/e each add one comma-separated entry here. Phase 1.1d
adds `, rm`.

### Data flow

```
 Operator                           CLI (this ticket)                          Daemon (#87, #98)
 ────────                           ─────────────────                          ─────────────────
 pyry sessions rm <prefix>
   │
   ▼
 main → run() → runSessions(args)
   │
   peel global flags (parseClientFlags)
   │
   dispatch on "rm" → runSessionsRm(socketPath, ["--archive", "<prefix>"])
   │
   parseSessionsRmArgs → (<prefix>, JSONLPolicyArchive, nil)
   │
   resolveSessionIDViaList(ctx, sock, <prefix>)
   │     control.SessionsList(ctx, sock) ───────────────────────────►
   │                                                                     handleSessionsList
   │                                                                     → []SessionInfo (snapshot)
   │     ◄────────────────────────────────────────────────────────────
   │     filter HasPrefix → unique match → canonical UUID
   │
   control.SessionsRm(ctx, sock, canonical, JSONLPolicyArchive) ──────►
   │                                                                     handleSessionsRm
   │                                                                       Pool.Remove(...)
   │                                                                       → nil
   │     ◄────────────────────────────────────────────────────────────
   │     {ok: true}
   │
   return nil → main → exit 0


 Error path (ambiguous prefix):
   │
   resolveSessionIDViaList → multiple HasPrefix hits
   │     errAmbiguousPrefix wrapping sorted "<uuid> <label>" lines
   │
   runSessionsRm → errors.Is(err, errAmbiguousPrefix) →
                   Fprintln(stderr, err.Error()) → os.Exit(1)


 Error path (bootstrap):
   │
   resolveSessionIDViaList → bootstrap UUID matched (returns canonical)
   │
   control.SessionsRm(ctx, sock, bootstrapUUID, ...) ──────────────────►
   │                                                                     handleSessionsRm
   │                                                                       Pool.Remove(...)
   │                                                                       → ErrCannotRemoveBootstrap
   │                                                                       resp.ErrorCode = "cannot_remove_bootstrap"
   │     ◄────────────────────────────────────────────────────────────
   │     ErrorCode → sessions.ErrCannotRemoveBootstrap (typed sentinel)
   │
   runSessionsRm → errors.Is(err, sessions.ErrCannotRemoveBootstrap) →
                   Fprintln(stderr, "cannot remove bootstrap session") →
                   os.Exit(1)
```

### Concurrency

No new goroutines, mutexes, or channels.

- `runSessionsRm` is sequential: parse → list → resolve → rm → return.
- The 30s `context.WithTimeout` is shared across both wire calls.
  Bound by `Pool.Remove`'s SIGTERM→SIGKILL ladder (~5s) plus
  registry persist (sub-second), with `Pool.List` returning
  effectively immediately. 30s is generous.
- One subtle race (documented in the handler's "concurrent-removal"
  comment): list returns ID `X`, then another caller runs
  `sessions.rm X` before our `SessionsRm` lands. The wire returns
  `ErrSessionNotFound` (the registry entry is gone by the time the
  second `Pool.Remove` looks it up). The handler emits the
  AC-prescribed `no session with id "<original-prefix>"` message.
  No retry, no escalation — let the operator re-enumerate.

### Error handling

End-to-end error catalogue, mapping each AC failure to its
implementation path:

| Failure | Source | CLI mapping | Exit | stderr message |
|---|---|---|---|---|
| `--archive` + `--purge` set | `parseSessionsRmArgs` | `errSessionsRmUsage` | 2 | `pyry sessions rm: usage: --archive and --purge are mutually exclusive` |
| Wrong arity (0 or ≥2 positionals) | `parseSessionsRmArgs` | `errSessionsRmUsage` | 2 | `pyry sessions rm: usage: expected <id>, got N positional args` |
| `flag.Parse` failure (unknown flag) | `parseSessionsRmArgs` | bare `flag` error | 2 | (`flag` package writes its own diagnostic) |
| Daemon not running | `control.SessionsList` dial fail | bubble through `fmt.Errorf` wrap | 1 | `pyry: sessions rm: ...dial socket: ...` (matches `pyry status` / `pyry stop`) |
| Ambiguous prefix | `resolveSessionIDViaList` | `errAmbiguousPrefix` | 1 | `<err.Error()>` — the AC#3 multi-line "<uuid> <label>" list |
| Unknown UUID/prefix | `resolveSessionIDViaList` returns `ErrSessionNotFound` | sentinel match | 1 | `no session with id "<original-input>"` |
| Bootstrap rejection | `control.SessionsRm` returns `sessions.ErrCannotRemoveBootstrap` (typed via wire `ErrCodeCannotRemoveBootstrap`) | sentinel match | 1 | `cannot remove bootstrap session` |
| Race: removed between list and rm | `control.SessionsRm` returns `sessions.ErrSessionNotFound` (typed via wire) | sentinel match | 1 | `no session with id "<original-input>"` |
| Other server error (evict failure, etc.) | `control.SessionsRm` returns `errors.New(resp.Error)` | bubble through `fmt.Errorf` wrap | 1 | `pyry: sessions rm: <verbatim server message>` |

The AC-prescribed messages have **no** `pyry:` prefix; the table's
"bubble through" rows do, because `main` prepends `pyry: ` when it
prints a returned error.

The "no stack traces on user-facing errors" AC item is satisfied
trivially — none of the paths panic or `runtime.Stack`.

## Testing strategy

Two test files: one unit (`cmd/pyry/sessions_test.go`, extended) and
one e2e (`internal/e2e/sessions_rm_test.go`, new).

### Unit tests — `cmd/pyry/sessions_test.go` (extended)

Add `TestParseSessionsRmArgs`, structured as a table mirroring
`TestParseSessionsNewArgs`. Stdlib `testing` only.

Rows:

| name | args | wantID | wantPolicy | wantErr (substring) |
|---|---|---|---|---|
| no args | `nil` | `""` | `""` | `expected <id>` |
| only flags | `["--archive"]` | `""` | `""` | `expected <id>` |
| `<id>` only | `["abc"]` | `"abc"` | `""` (= leave) | `""` |
| `--archive <id>` | `["--archive", "abc"]` | `"abc"` | `JSONLPolicyArchive` | `""` |
| `--purge <id>` | `["--purge", "abc"]` | `"abc"` | `JSONLPolicyPurge` | `""` |
| `--archive --purge <id>` | `["--archive", "--purge", "abc"]` | `""` | `""` | `mutually exclusive` |
| `--purge --archive <id>` | `["--purge", "--archive", "abc"]` | `""` | `""` | `mutually exclusive` |
| `<id>` then extra positional | `["abc", "extra"]` | `""` | `""` | `expected <id>` |
| flag after positional | `["abc", "--archive"]` | `""` | `""` | `expected <id>` (`flag` halts at first non-flag) |
| unknown flag | `["--unknown", "abc"]` | `""` | `""` | `flag provided but not defined` |
| `--name=` glued (typo, not a known flag) | `["--name=elli", "abc"]` | `""` | `""` | `flag provided but not defined` |

The "mutually exclusive" rows assert `errors.Is(err,
errSessionsRmUsage)` plus the message-fragment match — so the
sentinel chain stays observable from tests.

### Unit tests — also in `cmd/pyry/sessions_test.go`

Add `TestRunSessions_RmDispatch`: parallel to the existing
`TestRunSessions_NoSubcommand` / `_UnknownVerb`. Verifies the
router dispatches `sessions rm` to `runSessionsRm` (without
dialling — passes a bogus socket and asserts the resulting error
mentions `dial` or socket-style failure rather than the
`sessions: missing subcommand` / `unknown verb` diagnostic).

```go
// TestRunSessions_RmDispatch pins AC#1's router wiring: the
// `case "rm":` arm exists and routes to runSessionsRm. Verified
// by passing a deliberately-bogus socket path and observing that
// the error path is the dial failure, not the help-style
// "unknown verb" router error.
func TestRunSessions_RmDispatch(t *testing.T) {
    t.Setenv("PYRY_NAME", "")
    bogusSock := filepath.Join(t.TempDir(), "no-such.sock")

    err := runSessions([]string{"-pyry-socket", bogusSock, "rm", "abc"})
    if err == nil {
        t.Fatal("expected error, got nil")
    }
    msg := err.Error()
    if strings.Contains(msg, "unknown verb") {
        t.Errorf("router did not dispatch rm: %v", err)
    }
}
```

### E2E tests — `internal/e2e/sessions_rm_test.go` (new file)

Build tag `e2e` (matches `sessions_new_test.go`). Reuses
`StartIn` / `Run` / `RunBare` / `newRegistryHome` / `readRegistry` /
`writeSleepClaude` / `waitForBootstrap` from the existing harness —
no new helpers.

Each test follows the same shape: spin up `StartIn` with a sleep-
based `claudeBin`, drive `pyry sessions new --name <label>` once or
twice to populate the registry, then exercise `pyry sessions rm`.
Post-conditions check `readRegistry` for the expected entry-count
delta. AC items are pinned 1:1 (test name → AC sub-bullet):

1. **`TestSessionsRm_E2E_Success_Default`** — AC#1 happy path.
   Create one session, `pyry sessions rm <full-uuid>`, exit 0,
   registry no longer contains the entry. (Default JSONL policy:
   leave-on-disk; tested at the wire layer in #98 — this test
   asserts the CLI delivers the "0 = success, registry-entry-gone"
   contract, not the JSONL filesystem state.)

2. **`TestSessionsRm_E2E_Success_Prefix`** — AC#1 prefix branch.
   Create one session, run `pyry sessions rm <first-8-chars>`,
   exit 0, registry-entry-gone. Asserts unique-prefix resolution
   succeeds.

3. **`TestSessionsRm_E2E_Success_Archive`** — AC#1 + AC#2
   `--archive` flag. Create + remove with `--archive`. Exit 0,
   registry-entry-gone. (Wire path tested in #98; this test
   asserts the flag plumbs through to a successful invocation.)

4. **`TestSessionsRm_E2E_Success_Purge`** — AC#1 + AC#2
   `--purge` flag. Symmetric to `_Archive`.

5. **`TestSessionsRm_E2E_AmbiguousPrefix`** — AC#3 ambiguous-prefix
   path. Setup uses a manual registry: create two sessions whose
   UUIDs collide on a chosen short prefix (mint two via
   `pyry sessions new`, find the longest common prefix, use it as
   the test argument). Exit non-zero, stderr contains the multi-
   line "<uuid> <label>" list, registry unchanged.

   **Probabilistic concern.** Random UUIDs collide on the first
   character ~6.25% of the time (1/16); on the first two ~0.4%.
   Testing with 5 sessions raises P(collision in first char) to
   ~46%. The pragmatic shape: mint sessions in a loop until two
   share a `>=1`-char prefix that isn't an exact UUID match,
   then test that prefix. With `t.Parallel()` disabled and a
   small bound on the loop (mint up to 10), collision is
   effectively certain. Document the loop bound in the test
   comment so a future flake (10 mints, no collision) is
   identifiable as "regenerate UUIDs" rather than a real bug.

   Alternative shape: don't mint random UUIDs — directly hand-edit
   `sessions.json` before `Start`. Reuses the `newRegistryHome` +
   `readRegistry` helpers; injects two entries with chosen UUIDs
   (e.g. `aaaaaaaa-...-001` and `aaaaaaaa-...-002`). Cleaner,
   deterministic. **Use this shape.**

6. **`TestSessionsRm_E2E_UnknownUUID`** — AC#3 unknown-UUID path.
   Run `pyry sessions rm 00000000-0000-4000-8000-000000000000`
   (canonical UUID format, definitely not in the registry).
   Exit non-zero, stderr contains `no session with id`, registry
   unchanged.

7. **`TestSessionsRm_E2E_BootstrapRejected`** — AC#3 bootstrap
   path. Wait for bootstrap (`waitForBootstrap`), read its UUID
   from the registry, run `pyry sessions rm <bootstrap-uuid>`.
   Exit non-zero, stderr is exactly
   `cannot remove bootstrap session\n`, registry still contains
   the bootstrap entry.

8. **`TestSessionsRm_E2E_FlagsExclusive`** — AC#2 mutually-
   exclusive path. Run `pyry sessions rm --archive --purge <uuid>`
   against any running daemon. Exit code **2**, stderr contains
   `mutually exclusive`, registry unchanged.

9. **`TestSessionsRm_E2E_NoDaemon`** — AC#3 dial-failure path.
   `RunBare(t, "sessions", "-pyry-socket="+bogusSock, "rm",
   "<some-uuid>")`. Exit non-zero, stderr non-empty, stderr does
   NOT contain `panic` / `goroutine ` / `runtime/`. Mirrors
   `TestSessionsNew_E2E_NoDaemon`.

`go test -race ./...` and `go vet ./...` complete the AC#4
checklist.

### What's out of scope for tests

- **No JSONL-filesystem assertions.** AC#1 only asserts the
  default policy "leaves the JSONL on disk" implicitly via the
  wire layer (#98 tests); the CLI test asserts only the
  registry-entry delta + exit code. Pushing JSONL-on-disk
  assertions into this layer would duplicate #98's coverage.
- **No partial-prefix-of-prefix exact-match test.** Conceptually
  testable (mint UUID `abcdef...`, run `pyry sessions rm abcdef`
  to confirm the exact-match short-circuit fires) but the
  behavioural difference between exact-match and unique-prefix is
  unobservable end-to-end (both produce the same exit/stderr/
  registry shape). Covered by mirroring `Pool.ResolveID`'s
  resolution order — pinned at the source, not duplicated here.
- **No client-side ambiguous-error-format byte-comparison test.**
  The unit-test layer doesn't reach `resolveSessionIDViaList`
  (it requires a live `sessions.list` server); the e2e layer
  asserts the message contains both UUIDs and both labels but
  doesn't pin exact byte format. Format details are spec-level
  documentation, not load-bearing wire contract.

## Documentation

`docs/knowledge/features/control-plane.md` already documents the
wire surface (#98). No knowledge-base update needed for this
ticket — `cmd/pyry`-level CLI verbs are already implicitly
covered via the help text in `printHelp`. If a future maintainer
needs a per-verb operator reference, that's a separate
documentation slice.

`docs/PROJECT-MEMORY.md` gets a one-paragraph update under Phase
1.1d noting the CLI verb is shipped (developer task; not part of
the architecture spec).

## Open questions

1. **Should the CLI re-resolve after `ErrSessionNotFound` from the
   `sessions.rm` call?** The race (list → another caller removes →
   our rm returns NotFound) is rare. Today: surface the original
   `<id>` and exit. A retry would mask a legitimate race; better
   to let the operator re-list. Defer.

2. **Should `--archive` / `--purge` be replaced with a single
   `--jsonl <leave|archive|purge>` enum flag?** The AC-locked
   shape is two bool flags, mutually exclusive. The enum form is
   easier to extend (a future `--jsonl compress` doesn't need a
   new flag). Defer to a later ergonomic pass; today's shape
   matches the issue body.

3. **Should the prefix resolver be lifted into a shared helper
   now?** The ticket body explicitly says **no**: wait for the
   third caller (Phase 1.1e #49). This spec respects that. If
   sibling #92's prefix-resolution slice lands before #49, that
   architect inherits the lift-out decision.

4. **Should the wire-layer typed-error catalogue be extended with
   `ErrCodeAmbiguousSessionID` for symmetry?** Today: no — prefix
   resolution is fully client-side, so the wire never sees the
   ambiguous case. If a future ticket moves resolution
   server-side (e.g. a `sessions.resolve` verb), that ticket
   adds the code at the same time.
