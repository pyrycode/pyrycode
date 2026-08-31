# Sessions: CLI Router (1.1a-B2)

`pyry sessions <verb>` is the operator-facing surface for the `sessions.*` namespace. The router lives in `cmd/pyry/main.go` (`runSessions`) and dispatches `new` (#76) and `rm` (#99) today; 1.1b/c plug in as one switch case + one `runSessions<Verb>` helper each. The structural invariant — "one line per future verb" — is what the architect's choice of dispatch shape buys.

### Top-level dispatch

`run()`'s top-level switch gains a single case:

```go
case "sessions":
    return runSessions(os.Args[2:])
```

Adding 1.1b/c/d/e never touches this site again — the sub-router owns the rest.

### Sub-router

```go
const sessionsVerbList = "new, rm, rename, list" // appended by future verbs in lockstep with the switch

func runSessions(args []string) error {
    socketPath, rest, err := parseClientFlags("pyry sessions", args)
    if err != nil { return err }
    if len(rest) == 0 {
        return errSessionsUsage("missing subcommand")
    }
    sub, subArgs := rest[0], rest[1:]
    switch sub {
    case "new":
        return runSessionsNew(socketPath, subArgs)
    case "rm":
        return runSessionsRm(socketPath, subArgs)
    case "rename":
        return runSessionsRename(socketPath, subArgs)
    default:
        return errSessionsUsage(fmt.Sprintf("unknown verb %q", sub))
    }
}
```

Two design points lock the shape for the follow-on tickets:

- **The sub-router takes the *parsed* `socketPath`, not raw args.** `-pyry-socket` / `-pyry-name` are parsed exactly once by the existing `parseClientFlags` helper, before sub-verb dispatch. The convention "global pyry flags before the sub-verb" is enforced structurally — a `-pyry-name` placed *after* `new` reaches `runSessionsNew`'s own `flag.NewFlagSet` and produces "flag provided but not defined", not silent shadowing. Mirrors the top-level `splitArgs` convention ("pyry flags must come before claude args"). Pinned by `TestRunSessions_GlobalFlagAfterSubcommand_FailsCleanly` in `cmd/pyry/sessions_test.go`.
- **Constant `sessionsVerbList` over a derived list (map keys / reflection on the switch).** With one verb today and four 1-line additions in 1.1b/c/d/e, the duplication is one token per verb in two places (switch case + constant). A `map[string]func` would derive the list from `range m` but force a sort and pay an iteration cost that only amortizes at 3+ verbs. Dead-simple beats indirection here.

Unknown-verb (`pyry sessions list` before #61 lands) and missing-verb (`pyry sessions`) both surface through `errSessionsUsage` as `sessions: <detail>\nverbs: <list>`, exit 1. The router never falls through to the "forward unknown args to claude" path — the top-level switch returns from `runSessions` before `runSupervisor` is reached, so the verb namespace is closed structurally.

### `runSessionsNew` handler

```go
func runSessionsNew(socketPath string, args []string) error {
    label, err := parseSessionsNewArgs(args)
    if err != nil { return fmt.Errorf("sessions new: %w", err) }

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    id, err := control.SessionsNew(ctx, socketPath, label)
    if err != nil { return fmt.Errorf("sessions new: %w", err) }
    fmt.Println(id)
    return nil
}
```

`parseSessionsNewArgs(args) (label string, err error)` is the unit-testable seam — flag-parse + arity check, no network. Mirrors `attachSelectorFromArgs`'s split (1.1e-D); keeps `cmd/pyry/sessions_test.go` table-driven over flag forms (`--name`, `-name`, `--name=`, `--name=glued`, extra positional, unknown flag) without dialling the socket.

Three boundary rules pinned by AC:

- **Stdout is exactly `<uuid>\n`** — `fmt.Println(id)` writes the canonical 36-character UUIDv4 with a single trailing newline, no surrounding text. Pinned by `TestSessionsNew_E2E_Labelled` / `_Unlabelled` against `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\n$`.
- **Empty `--name` / no `--name` are equivalent** — both produce `label=""`, which `Pool.Create` stores verbatim as a no-label registry entry. The synthetic `"bootstrap"` substitution in `Pool.List` does *not* apply because `Bootstrap=false`; non-bootstrap empty-label sessions stay empty-labelled.
- **30s timeout mirrors the server-side ceiling.** `handleSessionsNew` uses `context.WithTimeout(..., sessionOpTimeout)` (30s) for `Pool.Create`; the client matches so neither side hangs the other. Lower would race the claude-spawn path (2-15s typical); higher gains nothing operationally — a stuck `Pool.Create` at 30s is a bug to surface, not paper over. Until #865, the server's conn write deadline stayed at the 5s handshake value across this whole 30s window, so an op that ran past 5s delivered EOF to this client instead of the UUID — see [Handshake Deadline](#handshake-deadline-per-conn-timeout-and-the-session-verb-extend-865).

### `runSessionsRm` handler (1.1d-B2)

```go
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
            fmt.Fprintln(os.Stderr, err.Error()); os.Exit(1)
        case errors.Is(err, sessions.ErrSessionNotFound):
            fmt.Fprintf(os.Stderr, "no session with id %q\n", id); os.Exit(1)
        }
        return fmt.Errorf("sessions rm: %w", err)
    }

    if err := control.SessionsRm(ctx, socketPath, canonical, policy); err != nil {
        switch {
        case errors.Is(err, sessions.ErrCannotRemoveBootstrap):
            fmt.Fprintln(os.Stderr, "cannot remove bootstrap session"); os.Exit(1)
        case errors.Is(err, sessions.ErrSessionNotFound):
            fmt.Fprintf(os.Stderr, "no session with id %q\n", id); os.Exit(1)
        }
        return fmt.Errorf("sessions rm: %w", err)
    }
    return nil
}
```

Three design points anchor this handler:

- **Client-side prefix resolution via `control.SessionsList`.** `resolveSessionIDViaList` enumerates the wire-level snapshot, prefers an exact match, then falls back to a `strings.HasPrefix` scan. Zero hits → `sessions.ErrSessionNotFound`; one → canonical UUID; many → `errAmbiguousPrefix` carrying the AC-prescribed multi-line `<uuid> <label>` list (sorted by ID asc, bootstrap with empty label rendered as `bootstrap`). Mirrors `Pool.ResolveID`'s order so client-side and server-side resolution agree byte-for-byte. Cost: two RTTs per `rm` instead of one. See [ADR 011](../decisions/011-cli-prefix-resolution.md).
- **Three AC-prescribed messages bypass `main`'s outer error printer.** Ambiguous prefix, unknown UUID, and bootstrap rejection emit plain text via `fmt.Fprintln(os.Stderr, ...)` + `os.Exit(1)`, **without** the `pyry:` prefix `main` prepends to returned errors. Other errors (e.g. dial failure on a stopped daemon) flow through `fmt.Errorf("sessions rm: %w", err)` → `pyry: sessions rm: …`. The `runAttach` precedent uses the same shape for its exit-2 path. `os.Exit` skips the deferred `cancel()`; the only resource involved is a process-local timer reaped on exit.
- **One `errSessionsRmUsage` sentinel covers every parse-time failure.** `parseSessionsRmArgs` wraps mutually-exclusive flags, wrong arity, and `flag.Parse` errors all in the same sentinel. `runSessionsRm` matches with `errors.Is` once, exits 2, and prints `pyry sessions rm: <wrapped-msg>`. Every error path out of the parser is, definitionally, a usage error — discrimination would buy nothing.

A single TOCTOU race exists: `SessionsList` returns the canonical UUID, then another caller removes the session before our `SessionsRm` lands. The wire returns `ErrSessionNotFound` from the second step; the CLI surfaces the **operator's typed `<id>`** (possibly a prefix), not the canonical UUID — preserves debugging context. No retry; let the operator re-list.

`parseSessionsRmArgs(args) (id string, policy control.JSONLPolicy, err error)` is the unit-testable seam — flag-parse + arity + mutual-exclusion guard, no network. Empty policy on the wire normalises to `JSONLPolicyLeave` server-side.

### `runSessionsRename` handler (1.1c-B2a → -B2b)

```go
func runSessionsRename(socketPath string, args []string) error {
    id, newLabel, err := parseSessionsRenameArgs(args)
    if err != nil {
        if errors.Is(err, errSessionsRenameUsage) {
            fmt.Fprintln(os.Stderr, "pyry sessions rename:", err)
        }
        os.Exit(2)
    }

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    canonical, err := resolveSessionIDViaList(ctx, socketPath, id)
    if err != nil {
        switch {
        case errors.Is(err, errAmbiguousPrefix):
            fmt.Fprintln(os.Stderr, err.Error()); os.Exit(1)
        case errors.Is(err, sessions.ErrSessionNotFound):
            fmt.Fprintf(os.Stderr, "no session with id %q\n", id); os.Exit(1)
        }
        return fmt.Errorf("sessions rename: %w", err)
    }

    if err := control.SessionsRename(ctx, socketPath, canonical, newLabel); err != nil {
        if errors.Is(err, sessions.ErrSessionNotFound) {
            // Race window: resolver returned a canonical UUID,
            // then another caller removed the session before our
            // wire call landed. Echo the operator's typed <id>.
            fmt.Fprintf(os.Stderr, "no session with id %q\n", id)
            os.Exit(1)
        }
        return fmt.Errorf("sessions rename: %w", err)
    }
    return nil
}
```

Five design points anchor this handler:

- **Client-side prefix resolution mirrors `runSessionsRm`.** `<id>` may be the full canonical UUID **or any unique prefix**. `resolveSessionIDViaList` is the same helper #99 introduced for `rm`; rename is its second caller. Exact match wins outright (the full-UUID form continues to work unchanged), otherwise a `strings.HasPrefix` scan over the `sessions.list` snapshot resolves to canonical UUID / `errAmbiguousPrefix` / `sessions.ErrSessionNotFound`. See [ADR 011](../decisions/011-cli-prefix-resolution.md). Per the ticket body and ADR 011, the helper is **not** lifted into `internal/sessions` yet — that defers to Phase 1.1e (`attach`), the third caller.
- **Two typed-sentinel branches → identical stderr.** Both the resolver-side `ErrSessionNotFound` (prefix matched nothing in the snapshot) and the wire-side one (resolver matched, but the session was removed between resolver enumeration and rename — same TOCTOU window as `runSessionsRm`) collapse to `no session with id "<input>"`. Operator reaction is identical, so distinguishing them buys nothing. The echoed string is the operator's typed `<id>`, not the resolver's canonical — matches `runSessionsRm` and what the operator can correlate against shell history. Renaming the bootstrap is allowed (`Pool.Rename` has no bootstrap-rejection per #62), so there is **no `errCannotRemoveBootstrap` branch** in this handler — that was rm-specific.
- **Single 30s `ctx` covers both wire calls (list + rename).** Sequential calls on the same socket; splitting into two contexts adds nothing — if the resolver burns 25s for some pathological reason, the rename deserves to be cut short on the same budget. Same shape as `runSessionsRm`.
- **Empty `<new-label>` is a valid value, not an arity hole.** `parseSessionsRenameArgs` checks `fs.NArg() == 2`, not "two non-empty values" — so `pyry sessions rename <uuid> ""` parses as `(uuid, "")` and forwards through. `Pool.Rename` treats `""` as "clear the on-disk label" per #62. AC#1 explicitly requires this; no separate `--clear` flag.
- **Exactly 2 positionals (not "≥2"); two-arg shape over `--name` flag.** Three or more is rejected as a usage error so multi-word labels must be quoted: `pyry sessions rename <uuid> "hello world"`. Same shape as `git config` / `kubectl label`; prevents silent token loss like `pyry sessions rename <uuid> hello world` discarding `world`. `<old> <new>` mirrors `git mv` / `kubectl rename`; reusing `--name` (the create-time label flag on `pyry sessions new`) would conflate two distinct semantics across verbs.

`parseSessionsRenameArgs(args) (id, newLabel string, err error)` is the unit-testable seam — flag-parse + arity, no network. The FlagSet exists for symmetry with `new` and `rm` (no flags today). The parser does not validate empty `<id>` — an empty first positional is technically parseable as `pyry sessions rename "" "label"`, would reach the resolver, and produce `errAmbiguousPrefix` against every session in the pool (because `strings.HasPrefix(_, "")` matches everything). That is a defensible failure shape (clean exit-1 error, not a panic) and not worth a parser-side guard.

### Sub-verb flag parsing — own FlagSet per verb

Each sub-verb's flag parser is its own `flag.NewFlagSet("pyry sessions <verb>", flag.ContinueOnError)`. Mirrors `runInstallService`'s precedent. Phase 1.1c's `--new-name` and 1.1d's `--archive`/`--purge` will reuse the same shape — no namespace-specific options accumulate on the top-level FlagSet, no name collision possible across sub-verbs.

### Error propagation

| Scenario | Operator-visible message | Source |
|---|---|---|
| `pyry sessions` (no verb) | `pyry: sessions: missing subcommand\nverbs: new, rm, rename, list` (exit 1) | `errSessionsUsage` |
| `pyry sessions bogus` (unknown verb) | `pyry: sessions: unknown verb "bogus"\nverbs: new, rm, rename, list` (exit 1) | `errSessionsUsage` |
| `pyry sessions new` against stopped daemon | `pyry: sessions new: dial …: connect: no such file or directory` (exit 1) | `request()` → `dial()` wrap |
| `pyry sessions new --name foo bar` | `pyry: sessions new: unexpected positional "bar"` (exit 1) | `parseSessionsNewArgs` arity check |
| Server-side `Pool.Create` failure | `pyry: sessions new: sessions: create supervisor: <claude err>` (exit 1) | `Response.Error` propagated by `SessionsNew` |
| Server-side activation failure (id valid, lifecycle goroutine respawns later) | `pyry: sessions new: <activate err>` (exit 1). **Registry entry remains** — operator can `pyry attach <uuid>` once 1.1e ships. | per AC#4 |
| `pyry sessions rename <uuid>` (only one positional) | `pyry sessions rename: usage: expected <id> <new-label>, got 1 positional args` (exit 2) | `parseSessionsRenameArgs` arity check |
| `pyry sessions rename <ambiguous-prefix> alpha` | `ambiguous session id prefix:\n<uuid>  <label>\n<uuid>  <label>...` (exit 1, no `pyry:` prefix) — no `sessions.rename` wire call made | `errors.Is(err, errAmbiguousPrefix)` |
| `pyry sessions rename <unknown-prefix-or-uuid> alpha` | `no session with id "<input>"` (exit 1, no `pyry:` prefix) | resolver-side `errors.Is(err, sessions.ErrSessionNotFound)` |
| `pyry sessions rename <prefix>` (race: resolved then removed before rename lands) | `no session with id "<input>"` (exit 1, no `pyry:` prefix) — operator's typed `<id>`, not the canonical | wire-side `errors.Is(err, sessions.ErrSessionNotFound)` |
| `pyry sessions rename` against stopped daemon | `pyry: sessions rename: dial …: connect: no such file or directory` (exit 1) | `request()` → `dial()` wrap |

Activation failure is **not** distinguishable from a generic error at the wire boundary (the wire has no separate "id valid despite error" channel). The registry entry persists because `Pool.Create` saves before the activation step, so AC#4 is satisfied server-side.

### Help text

```
  pyry sessions <verb> [flags]                   manage sessions on a running
                                                  daemon (verbs: new, rm, rename, list)
```

Phase 1.1b/c/d/e each append one verb to the parenthesised list, in lockstep with `sessionsVerbList`.

### Tests

- **`cmd/pyry/sessions_test.go`** (unit, ~100 LOC) — pins router shape and flag-parse / arity rules in isolation. `TestRunSessions_NoSubcommand`, `TestRunSessions_UnknownVerb`, `TestRunSessions_GlobalFlagAfterSubcommand_FailsCleanly`, and table-driven `TestParseSessionsNewArgs` over `--name` / `-name` / `--name=` / extra positional / unknown flag.
- **`internal/e2e/sessions_new_test.go`** (e2e, build tag `e2e`, ~165 LOC) — daemon-up against the `writeSleepClaude` stand-in (#116): `TestSessionsNew_E2E_Labelled` / `_Unlabelled` (stdout regex + registry post-condition: ID present, `Label`, `Bootstrap=false`); `TestSessionsNew_E2E_UnknownVerb` (registry session count unchanged before/after — AC#3); `TestSessionsNew_E2E_NoDaemon` (`RunBare` against bogus socket; non-zero exit, non-empty stderr, no `panic`/`goroutine`/`runtime/` substrings — AC#2).
- **`cmd/pyry/sessions_test.go`** (extended for #99) — `TestParseSessionsRmArgs` table over no-args, only-flags, id-only, `--archive`, `--purge`, both flags (mutually exclusive), trailing positional, flag-after-positional (Go `flag` halts at first non-flag), unknown flag. `TestRunSessions_RmDispatch` pins the router wiring against a bogus socket (asserts the failure is a dial error, not the "unknown verb" router diagnostic).
- **`internal/e2e/sessions_rm_test.go`** (e2e, build tag `e2e`, ~290 LOC) — nine tests covering: happy path on full UUID and unique prefix, `--archive` and `--purge` plumbing, ambiguous-prefix rendering (mints sessions until pigeonhole-collision on first hex char — bound 17 over 16 hex digits), unknown UUID, bootstrap rejection, mutually-exclusive flags (exit 2), no-daemon dial failure (clean error, no panic).
- **`cmd/pyry/sessions_test.go`** (extended for #92) — `TestParseSessionsRenameArgs` table over no-args, only-`<id>`, `<id> <label>`, `<id> ""` (empty-label clear is valid), trailing positional (rejected — multi-word labels must be quoted), label with embedded space (single token — passes), unknown flag. `TestRunSessions_RenameDispatch` pins the router wiring against a bogus socket (asserts the error wraps `sessions rename:`, not the `unknown verb` router diagnostic).
- **`internal/e2e/sessions_rename_test.go`** (e2e, build tag `e2e`) — seven tests after the #93 prefix-resolution lift: happy-path rename on full canonical UUID (label flips `before` → `after`); **prefix-success** (`TestSessionsRename_E2E_Success_Prefix`: rename via the first 8 hex chars of the canonical UUID — pins the resolver's canonical-UUID forwarding to the wire end-to-end, since a wrong forward would surface as the wire's `ErrSessionNotFound`); **ambiguous-prefix** (`TestSessionsRename_E2E_AmbiguousPrefix`: mints sessions until pigeonhole-collision on first hex char — bound 17 over 16 digits — runs `pyry sessions rename <shared-char> should-not-apply`, asserts non-zero exit, both matched ids+labels in stderr, **and both sessions' on-disk labels unchanged**: load-bearing AC#2 check that the resolver bails before the wire mutation); empty-label clear (full UUID); unknown UUID (resolver-side `ErrSessionNotFound` mapping); no-daemon dial failure; wrong-arity (one positional, exit 2). The race-window wire-side `ErrSessionNotFound` branch is covered transitively by the unknown-UUID test (which now traverses the same `errors.Is` arm) — pinning the race deterministically would require harness goroutine plumbing for marginal gain.

`Pool.Create` failure surfacing (typed sentinels, nil-Sessioner branch, etc.) is **not** re-tested through the CLI shell — `internal/control/sessions_new_test.go` (#75) covers it exhaustively against the wire. The CLI is `fmt.Errorf("sessions new: %w", err)` over a wire client we trust.

See [ADR 010](../decisions/010-sessions-cli-sub-router.md) for why the sub-router takes a parsed `socketPath` rather than raw args, and why `sessionsVerbList` is a constant rather than derived.
