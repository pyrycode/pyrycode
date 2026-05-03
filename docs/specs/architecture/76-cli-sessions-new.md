# #76 — `pyry sessions new` CLI router + verb

Phase 1.1a-B2. Consumes the `sessions.new` wire and `Sessioner` seam landed
by #75 (and the `pool` parameter already threaded through `NewServer` at
`cmd/pyry/main.go:314`). This ticket adds the operator-facing `pyry sessions
<verb>` router and its first verb, `pyry sessions new [--name LABEL]`.

The router shape is the load-bearing decision: 1.1b (`list`), 1.1c
(`rename`), 1.1d (`rm`), and 1.1e (`attach <id>`) all plug in here, each as
a one-line addition. The architect's job is to lock the shape now so the
four follow-on tickets are mechanical.

## Files to read first

- `cmd/pyry/main.go:134-164` — `run()`: the top-level verb switch where
  `case "sessions":` slots in alongside `status` / `stop` / `logs` /
  `attach` / `install-service`.
- `cmd/pyry/main.go:309-318` — wiring already done by #75:
  `control.NewServer(socketPath, poolResolver{pool}, logRing, cancel,
  logger, pool)` passes the pool as `Sessioner`. No `runSupervisor`
  changes for this ticket.
- `cmd/pyry/main.go:362-371` — `parseClientFlags`: shared
  `-pyry-socket` / `-pyry-name` parser. The new sub-router reuses it
  verbatim — the same surface every other client verb already drives.
- `cmd/pyry/main.go:485-500` — `runStop`: the simplest one-shot control
  verb. Error wrapping (`fmt.Errorf("stop: %w", err)`), success print
  shape, and post-call exit are the templates `runSessionsNew` mirrors.
- `cmd/pyry/main.go:374-403` — `runStatus`: shows the
  `parseClientFlags → control.X → error wrap` pattern in full; same
  shape as the new handler.
- `cmd/pyry/main.go:507-597` — `runInstallService`: the precedent for a
  verb with **its own** internal `flag.NewFlagSet`. Mirror its
  `fs := flag.NewFlagSet("pyry install-service", flag.ContinueOnError)`
  + `fs.String(...)` + `fs.Parse(...)` shape for `runSessionsNew`'s
  `--name`. Phase 1.1c's `rename` reuses the same pattern for
  `--new-name`, etc.
- `cmd/pyry/main.go:599-650` — `printHelp`: add the `sessions <verb>`
  group. The 1.1b/c/d/e tickets each add one line here.
- `cmd/pyry/args_test.go:107-143` — `TestAttachSelectorFromArgs`:
  table-driven test over a small arity helper. The new
  `sessionsSubcommand` extractor follows the same shape; this test is
  the template for `TestSessionsSubcommand`.
- `internal/control/client.go:96-121` — `SessionsNew(ctx, sock, label)
  (string, error)`: the wire client this CLI handler wraps. Same
  one-shot pattern as `Status` / `Stop`.
- `internal/e2e/cli_verbs_test.go:17-47` — `TestStop_E2E`: daemon-up
  e2e pattern (`Start(t)` + `h.Run(t, verb, args...)` + assert exit /
  stdout). Template for `TestSessionsNew_E2E_Labelled` /
  `_Unlabelled` / `_UnknownVerb`.
- `internal/e2e/cli_verbs_test.go:49-73` — `TestStatus_E2E_Stopped`:
  no-daemon e2e pattern (`RunBare(t, verb, "-pyry-socket=" + bogusSock)`
  + assert non-zero exit + clean stderr). Template for
  `TestSessionsNew_E2E_NoDaemon`.
- `internal/e2e/harness.go:465-513` — `Harness.Run` / `runVerb`:
  shows that the harness injects `-pyry-socket=` between
  `os.Args[1]` (verb) and the rest. For `pyry sessions new --name foo`,
  the call site `h.Run(t, "sessions", "new", "--name", "foo")` produces
  `pyry sessions -pyry-socket=<sock> new --name foo`. The router must
  parse the global flags before the sub-verb is reached — this is the
  binding constraint on the dispatch shape (see Design § Sub-router
  argument shape).
- `internal/e2e/restart_test.go:13-48,117-148` — `registryEntry` /
  `registryFile` on-disk shapes plus `newRegistryHome` /
  `readRegistry` / `mustReadFile` helpers. AC#1 ("on-disk registry
  contains an entry with the printed UUID, the supplied label, `bootstrap:
  false`") asserts via `readRegistry`. Same package (`e2e`,
  build-tag `e2e`), so the new test reuses these directly without
  re-declaring them.
- `internal/e2e/cap_test.go:60-96` — concrete consumer of `readRegistry`;
  shows the byID-map + post-condition assertion pattern. The new
  e2e tests follow the same shape.
- `internal/sessions/pool.go:803-882` — `Pool.Create(ctx, label)
  (SessionID, error)`. Confirms `bootstrap: false` is set by Create
  (registry post-condition for AC#1) and that empty `label` becomes a
  no-label entry.
- `docs/knowledge/features/control-plane.md` § "Sessions: creation
  seam (1.1a-B1)" — the wire conventions and `Response.Error` shape
  the CLI handler decodes. Confirms that
  `errors.New("sessions.new: no sessioner configured")` is the only
  prefixed error from the server (always nil-Sessioner case is gone
  for this ticket since the wiring in main.go:314 passes the real
  pool).

## Context

Today the top-level CLI dispatches verbs via a flat `switch` on
`os.Args[1]`. Phase 1.1 introduces a verb family: `sessions new`,
`sessions list`, `sessions rename`, `sessions rm`, plus the `sessions`-
adjacent `attach <id>` refactor (#49). Each is a wire-level verb the
operator drives via `pyry`. Two design questions need locking now:

1. **Where does the `sessions <verb>` dispatch live?** Top-level switch
   gains a `case "sessions":` that hands off to a sub-router. Adding
   1.1b/c/d/e is one switch case each in the sub-router. No follow-on
   ticket touches the top-level switch.
2. **How are sub-verb flags parsed?** Each sub-verb gets its own
   `flag.NewFlagSet`. Mirrors `runInstallService`'s precedent; gives
   1.1c (`rename --new-name LABEL`) and 1.1e (`attach <id>`) clean
   per-verb flag surfaces without the top-level FlagSet growing
   namespace-specific options.

This ticket lands the router with one verb (`new`). The four follow-on
tickets each add (a) a `case "<verb>":` in the sub-router, (b) a
`runSessions<Verb>` handler, and (c) one line in `printHelp`. The
"one line per future verb" invariant the issue calls out is satisfied
by structure, not by discipline.

## Design

### Top-level dispatch (cmd/pyry/main.go)

Add a single case to `run()`'s switch (line 142-160):

```go
case "sessions":
    return runSessions(os.Args[2:])
```

That's the entire top-level change. `runSessions` owns the rest.

### Sub-router (cmd/pyry/main.go)

```go
// runSessions implements `pyry sessions <verb>`: peel the global pyry
// flags via parseClientFlags, then dispatch on the first positional.
//
// Convention (matches the top-level CLI: "pyry flags must come before
// claude args"): -pyry-socket / -pyry-name must precede the sub-verb.
// Sub-verb flags (e.g. --name on `new`) come after.
//
// New verbs in this family (1.1b list, 1.1c rename, 1.1d rm, 1.1e
// attach) each add one switch case + one runSessions<Verb> helper.
func runSessions(args []string) error {
    socketPath, rest, err := parseClientFlags("pyry sessions", args)
    if err != nil {
        return err
    }
    if len(rest) == 0 {
        return errSessionsUsage(`missing subcommand`)
    }
    sub, subArgs := rest[0], rest[1:]
    switch sub {
    case "new":
        return runSessionsNew(socketPath, subArgs)
    default:
        return errSessionsUsage(fmt.Sprintf("unknown verb %q", sub))
    }
}

// errSessionsUsage formats a help-style error listing the implemented
// verbs. Phase 1.1b/c/d/e each append one verb to sessionsVerbList.
func errSessionsUsage(detail string) error {
    return fmt.Errorf("sessions: %s\nverbs: %s", detail, sessionsVerbList)
}

// sessionsVerbList is the displayed verb list in usage errors. Update
// in lockstep with the switch above.
const sessionsVerbList = "new"
```

**Why a constant `sessionsVerbList` instead of deriving from the switch
or a map.** A `map[string]func` derives the list from `range m` but
needs sorting, and the iteration cost only pays off once the list is
long enough that the duplication hurts (3+ verbs). With one verb today
and four 1-line additions in 1.1b/c/d/e, the duplication is one token
per verb in two places (switch case + constant). Dead-simple, beats
indirection. The 1.1b ticket extends the constant in the same edit
that adds the case — no new pattern, no map.

**Why the sub-router takes the parsed `socketPath`, not raw args.** Two
reasons: (a) it lets the top-level `-pyry-socket` and `-pyry-name`
flags be parsed exactly once, by the existing helper, with the
existing "sub-verb flag is unknown" error message style; (b) it
forces the convention "pyry global flags before sub-verb" structurally,
matching the existing top-level convention (`splitArgs`) — instead of
allowing each sub-verb's own FlagSet to silently absorb a second
`-pyry-name` and produce surprising semantics. The cost is a one-line
constraint in the spec ("globals before sub-verb"); the win is one
canonical parse path for every future `sessions.*` verb.

### `runSessionsNew` handler (cmd/pyry/main.go)

```go
// runSessionsNew implements `pyry sessions new [--name LABEL]`:
// dial the daemon's control socket, ask it to mint a session,
// print the UUID. Empty label maps to a no-label session per AC.
func runSessionsNew(socketPath string, args []string) error {
    fs := flag.NewFlagSet("pyry sessions new", flag.ContinueOnError)
    label := fs.String("name", "", "human-friendly label for the new session")
    if err := fs.Parse(args); err != nil {
        return err
    }
    if fs.NArg() > 0 {
        return fmt.Errorf("sessions new: unexpected positional %q", fs.Arg(0))
    }

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    id, err := control.SessionsNew(ctx, socketPath, *label)
    if err != nil {
        return fmt.Errorf("sessions new: %w", err)
    }
    fmt.Println(id)
    return nil
}
```

**Stdout shape.** `fmt.Println(id)` writes `<uuid>\n` exactly — no
prefix, no trailing whitespace beyond the newline `Println` adds. Pins
AC#1 ("single 36-character canonical UUIDv4 on stdout (no surrounding
text, trailing newline only)").

**Timeout choice.** 30s mirrors the server-side ceiling locked by
`handleSessionsNew` (server.go: `context.WithTimeout(..., 30s)`). The
client's deadline armed via `request()`'s `conn.SetDeadline` then
matches the server's — neither side goes hung. Going lower would race
the server's claude-spawn path (2-15s typical); going higher gains
nothing operationally (a stuck Pool.Create at 30s is a real bug to
surface, not paper over).

**Error wrapping.** `fmt.Errorf("sessions new: %w", err)` makes
`main.go`'s top-level `fmt.Fprintln(os.Stderr, "pyry:", err)` produce
`pyry: sessions new: <wire-error>`. For the no-daemon case, the
wire-error is `dial /path/to/sock: connect: <reason>` (from
`request()` → `dial()`); the operator sees
`pyry: sessions new: dial /path/sock: connect: no such file or directory`
— same shape as `pyry status` on a stopped daemon today.

**Why no per-positional-arity helper like `attachSelectorFromArgs`.**
The arity rule for `new` is trivial — zero positionals — and inlines
in two lines. `runAttach` extracts the helper because the
selector-from-args rule has three cases (zero / one / many) and is
re-used by tests for the helper's own contract. For `new`, the logic
is `if NArg() > 0: error`; introducing a helper would be ceremony
without a unit-test target worth carving out.

### `printHelp` update (cmd/pyry/main.go:599-650)

Add one usage line under the existing `pyry attach` line:

```
  pyry sessions <verb> [flags]                   manage sessions on a running
                                                  daemon (verbs: new)
```

Matches the indentation pattern of the other multi-line entries. The
verb list is duplicated from `sessionsVerbList`; 1.1b/c/d/e update
both in lockstep (one extra word per ticket).

### Top-level switch comment (main.go:13-22)

The reserved-verb comment block lists every CLI verb. Add one line:

```
//	pyry sessions <verb> Multi-session management (verbs: new)
```

### Sub-router argument shape — concrete walkthrough

The harness's `h.Run(t, "sessions", "new", "--name", "foo")` produces
the argv:

```
pyry sessions -pyry-socket=<sock> new --name foo
```

(Confirm: `harness.go:488` — `full := append([]string{verb,
"-pyry-socket=" + socket}, args...)`.)

`run()` switch matches `os.Args[1] == "sessions"` and calls
`runSessions(os.Args[2:])`, i.e. `["-pyry-socket=<sock>", "new",
"--name", "foo"]`.

`parseClientFlags("pyry sessions", args)`:
- The internal `flag.FlagSet` knows `-pyry-name` and `-pyry-socket`.
- Parses `-pyry-socket=<sock>`, sets `socketFlag`.
- Stops at `"new"` (Go's `flag.Parse` halts at first non-flag token).
- Returns `socketPath=<sock>`, `rest=["new", "--name", "foo"]`.

Sub-router:
- `sub = "new"`, `subArgs = ["--name", "foo"]`.
- Dispatch to `runSessionsNew(socketPath, subArgs)`.

`runSessionsNew`:
- Internal FlagSet parses `--name=foo`, sets `*label = "foo"`.
- `fs.NArg() == 0` → OK.
- Calls `control.SessionsNew(ctx, socketPath, "foo")`.

Operator-facing convention: `-pyry-socket` / `-pyry-name` must come
**before** the sub-verb. After it, they're treated as unknown by the
sub-verb's FlagSet and produce a clean error. This matches the
top-level `splitArgs` convention (lines 184-196) — same spirit, same
wording for the failure mode.

### Data flow

```
operator                         pyry CLI                       daemon
────────                         ────────                       ──────
$ pyry sessions new --name x
                                run()
                                  os.Args[1] == "sessions"
                                  runSessions(os.Args[2:])
                                    parseClientFlags → socketPath
                                    sub="new"
                                    runSessionsNew(socket, ["--name","x"])
                                      flag.NewFlagSet, parse --name=x
                                      control.SessionsNew(ctx, sock, "x")
                                        dial unix sock
                                        encode {verb:"sessions.new",
                                                sessions:{label:"x"}}
                                          ─────────────────────►
                                                                handleSessionsNew
                                                                  pool.Create(ctx, "x")
                                                                    NewID, supervise, Activate
                                                                  encode {sessionsNew:
                                                                    {sessionID:"<uuid>"}}
                                          ◄─────────────────────
                                        decode → return uuid
                                      fmt.Println(uuid)
$ <uuid>\n                      exit 0
```

Error path: any non-zero `err` from `control.SessionsNew` (dial
failure, decode failure, server-reported `Response.Error`) becomes
`fmt.Errorf("sessions new: %w", err)`, which `main` prints as
`pyry: sessions new: <reason>` and exits 1.

### Concurrency

None. The CLI is one short-lived process per `pyry sessions new`
invocation — dial, encode, decode, exit. All concurrency lives on the
server side; this handler is a 4-line synchronous wrapper.

### Error handling

| Scenario | User-facing message | Source |
|---|---|---|
| `pyry sessions` with no sub-verb | `pyry: sessions: missing subcommand\nverbs: new` (exit 1) | `errSessionsUsage` |
| `pyry sessions list` (1.1b not landed) | `pyry: sessions: unknown verb "list"\nverbs: new` (exit 1) | `errSessionsUsage` |
| `pyry sessions new` against stopped daemon | `pyry: sessions new: dial /path/sock: connect: no such file or directory` (exit 1) | `request()` → `dial()` wrap |
| `pyry sessions new --name foo bar` (extra positional) | `pyry: sessions new: unexpected positional "bar"` (exit 1) | `runSessionsNew` arity check |
| Server-side `Pool.Create` failure (e.g. claude bin missing) | `pyry: sessions new: sessions: create supervisor: <claude err>` (exit 1) | `Response.Error` propagated by `SessionsNew` |
| Server-side activation failure (id valid, lifecycle goroutine respawns later) | `pyry: sessions new: <activate err>` (exit 1). **Registry entry remains** — operator can `pyry attach <uuid>` after 1.1e. | per AC#4; `Pool.Create`'s `(id, err)` contract |
| `--name` value parse error (impossible — string flag) | n/a | n/a |
| `-pyry-foo` (unknown global flag) | `flag provided but not defined: -pyry-foo` (exit 1) | `parseClientFlags` |

The activation-failure case is **not** distinguishable from a generic
error at the wire boundary (per #75's wire shape: `Response.Error`
carries the message; no separate "id valid despite error" channel).
The server sees the `(id, err)` and reports the error verbatim;
`Pool` has already persisted the registry entry, so the operator
view is "command failed; session lingers in registry until cleanup
or attach". This satisfies AC#4 ("registry entry remains on disk so
the operator can later `pyry attach <uuid>` to retry once that
ships") because the persistence is on the server side, before the
error returns.

### What stays out of scope

- **No changes to the wiring in `runSupervisor`.** #75 already passes
  `pool` as the `Sessioner` at `cmd/pyry/main.go:314`. No edits to
  that line.
- **No `--workdir` / `--cwd` flag for `sessions new`.** `Pool.Create`
  inherits the daemon's bootstrap workdir for now. Per-session
  workdir is Phase 2.x territory and out of scope for AC#1.
- **No retry / activation poll.** Activation failure is reported
  verbatim and returns. Future work (Phase 1.1e, `pyry attach`)
  exposes the retry path.

## Testing strategy

Two test files. Stdlib `testing` only, no testify, table-driven where
the input space is enumerated.

### Unit: `cmd/pyry/sessions_test.go` (new, ~80 LOC)

Same package as `main.go` (package `main`). Targets the helpers in
isolation — no daemon, no network.

1. **`TestRunSessions_NoSubcommand`** — call `runSessions(nil)`,
   assert error string contains `"missing subcommand"` and the verb
   list (`"new"`). Pins the empty-rest error path.

2. **`TestRunSessions_UnknownVerb`** — call `runSessions([]string{
   "list"})`, assert error contains `"unknown verb"` and `"list"` and
   the verb list. Pins AC#3 (does not fall through to claude — proven
   structurally because `runSessions` returns from `run()` before
   `runSupervisor` is reached, so a unit test on `runSessions` itself
   is sufficient).

3. **`TestRunSessions_GlobalFlagBeforeSubcommand`** — call with
   `[]string{"-pyry-socket=/tmp/x", "new"}`, assert no error from
   the parse stage (the `runSessionsNew` body errors later on the
   dial, but the parse itself succeeds and the sub-verb dispatch
   reaches "new"). Confirms the convention.

4. **`TestRunSessions_GlobalFlagAfterSubcommand_FailsCleanly`** —
   call with `[]string{"new", "-pyry-name", "elli"}`, assert error
   from `runSessionsNew`'s FlagSet (unknown flag `-pyry-name`).
   Documents and pins the convention.

5. **`TestRunSessionsNew_ArgParsing`** — table-driven over
   `runSessionsNew(socket="bogus", args=[...])`. Cases: empty args,
   `--name foo`, `--name=`, `--name foo extra` (extra positional
   error). Each case asserts on the FlagSet error vs. the
   "unexpected positional" error — both happen *before* the
   `control.SessionsNew` dial, which we don't execute (the dial will
   fail against `bogus`, but the test asserts on the *type* of
   error, distinguishing "flag parse" from "unexpected positional"
   from "dial failure"). The dial failure is exercised in the e2e
   test, not here — keeping the unit test free of network.

   Concretely: for the first three cases, the call returns the dial
   error (acceptable — the test asserts `errors.Is(err, ...)` is
   not the parse-time errors). For the extra-positional case, assert
   the error string contains `"unexpected positional"`.

   Or, cleaner: extract a helper `parseSessionsNewArgs(args)
   (label string, err error)` that does only the flag parse +
   arity check, and unit-test that. The handler becomes
   `label, err := parseSessionsNewArgs(args); ...; control.SessionsNew(...)`.

   **Decision:** extract the helper. Keeps unit tests
   network-free, mirrors `attachSelectorFromArgs`'s precedent.

6. **`TestParseSessionsNewArgs`** (with the helper above) — table
   over the four cases above, no network involved.

### E2E: `internal/e2e/sessions_new_test.go` (new, ~120 LOC)

Build-tag `//go:build e2e` (mirrors `cli_verbs_test.go`).

7. **`TestSessionsNew_E2E_Labelled`** — `Start(t)`,
   `h.Run(t, "sessions", "new", "--name", "feature-x")`,
   assert exit 0 and stdout matches `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]
   {4}-[0-9a-f]{4}-[0-9a-f]{12}\n$` (canonical UUID, exactly one
   trailing newline, no other text). Then `readRegistry(t,
   <home>/.pyry/test/sessions.json)`, find the entry whose `ID`
   matches the printed UUID, assert `Label == "feature-x"`,
   `Bootstrap == false`, `LifecycleState == "active"`. Pins AC#1
   (full).

8. **`TestSessionsNew_E2E_Unlabelled`** — same as above but
   `h.Run(t, "sessions", "new")` with no `--name`. Assert the new
   registry entry has `Label == ""`. Pins AC#1 empty-label semantics.

9. **`TestSessionsNew_E2E_UnknownVerb`** — `h.Run(t, "sessions",
   "list")`. Assert exit non-zero, stderr contains `"unknown verb"`
   and `"list"`, and the registry **does not** gain an entry (the
   command must not reach the daemon's pool for an unknown verb).
   Compare registry entry count before/after. Pins AC#3 ("does not
   fall through to the 'forward unknown args to claude' path").

10. **`TestSessionsNew_E2E_NoDaemon`** — `RunBare(t, "sessions",
    "new", "-pyry-socket=" + filepath.Join(t.TempDir(),
    "no-such.sock"))`. Assert exit non-zero, stderr non-empty, no
    panic / goroutine / runtime crash markers. Mirrors
    `TestStatus_E2E_Stopped`'s shape exactly. Pins AC#2.

### Race / vet

- `go test -race ./...` — handled by the existing CI invocation.
  No new race surface (the CLI is short-lived and synchronous).
- `go vet ./...` — clean. Pin AC#5.

### What's deliberately out of scope for tests

- **No test that exercises `Pool.Create` failure surfacing.** That's
  covered by `internal/control` tests in #75 (fake `Sessioner`
  returning errors). Re-running the same assertion through the CLI
  layer adds nothing — the CLI is `fmt.Errorf("sessions new: %w",
  err)` over a wire client we trust.
- **No simultaneous-creation race test.** `Pool.Create` concurrency
  is owned by the sessions package and exercised there.
- **No `--name` flag with embedded shell quoting / unicode test.**
  The flag value is a Go string; flag-package parsing is well-trodden.

## Open questions

1. **Should the `--name` flag also accept `-name`?** Go's `flag`
   package accepts both forms automatically (single-dash and
   double-dash). The issue body uses `--name` consistently; document
   `--name` in `printHelp` but the implementation supports both
   without extra work. No decision needed.

2. **Should `pyry sessions new` retry on transient dial failure?**
   No. `pyry status` / `pyry stop` / `pyry logs` don't retry;
   "no daemon" is a clean failure for the operator to act on. Phase
   2.x remote-access work may revisit if remote retry policy makes
   sense, but local-dial doesn't.

3. **Does the help-style error need to print full usage syntax?**
   The current shape (`sessions: unknown verb "list"\nverbs: new`)
   is terse. `git`'s style is similar (`git: 'foo' is not a git
   command`). If 1.1b operator feedback says "I want
   `pyry sessions --help`", add it then; not preemptively. Per
   project working principle "Don't design for hypothetical future
   requirements."
