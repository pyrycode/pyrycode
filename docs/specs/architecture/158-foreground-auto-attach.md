# #158 — Foreground pyry auto-attaches when daemon hosts `--session-id`

Phase 1.3c-2. Single-file change to `cmd/pyry/main.go`. No new packages,
no new exported control surface, no wire change. Builds on:

- **#154 (1.3a)** — `control.AttachStdio` (`internal/control/attach_stdio_client.go`),
  the no-PTY bridge function we dispatch into.
- **#155 (1.3b)** — `AttachStdio`'s `createIfMissing bool` param. We
  pass `false` here (existence has already been confirmed via
  `sessions.has-id` — the take-or-create branch is for SDK callers
  that want one-call onboarding, which is a different shape).
- **#157 (1.3c-1)** — `control.SessionsHasID(ctx, socket, id) (bool, error)`,
  the cheap one-bit existence query the auto-detect calls before
  committing to attach.

This ticket plugs those primitives together at the entry point so
Claudian and any other "claude-binary-path" consumer that passes
`--session-id <uuid>` Just Works against a running pyry daemon —
without a wrapper script, without a socket-path dance, and without
any per-tool integration.

The closest precedent for the "scan claude args at startup" shape is
**`splitArgs` (`main.go:204-237`)**: a small, pure, table-driven
walk over the raw `os.Args` tail. The auto-attach path is a similar
shape — a tiny string scan, then a stat, then at most one wire call,
then dispatch. No goroutines, no new abstractions, no test doubles
beyond what `args_test.go` already uses.

Consumer-side (Claudian, the SDK) concerns are out of scope; e2e
coverage of the dispatch is split into siblings #163 (happy path) and
#164 (fallback scenarios) per the ticket body.

## Files to read first

The developer's turn-1 reading list. Pull from these to avoid
re-discovering the entry-point flow.

- `cmd/pyry/main.go:139-171` (`main` + `run`) — the top-level
  subcommand dispatch. Auto-attach is **not** added here. The verbs
  branch is unchanged; auto-attach plugs in below, inside
  `runSupervisor`, *after* `run()` has decided this invocation is the
  foreground-binary branch (no `attach` / `status` / `stop` / `logs`
  / `sessions` / `install-service` / `version` / `help` match).
- `cmd/pyry/main.go:204-237` (`splitArgs`) — the canonical "walk the
  args, separate pyry's flags from claude's" routine. The new
  `extractSessionID` helper scans `claudeArgs` (not `pyryArgs`)
  because `--session-id` is a claude flag, never a pyry flag.
- `cmd/pyry/main.go:255-348` (`runSupervisor`) — the function the
  auto-attach hook splices into. Splice point is right after
  `socketPath` / `registryPath` / `claudeSessionsDir` are resolved
  (line ~272), **before** the logger / ring buffer / Bridge / Pool
  side effects. Everything below the splice is unchanged.
- `cmd/pyry/main.go:64-69` (`defaultName`) and `:75-84`
  (`resolveSocketPath`) — the daemon-name resolution chain
  (`-pyry-name` flag → `PYRY_NAME` env → `DefaultName`). The
  auto-attach must reuse this verbatim — the AC's "use the same
  resolution chain" is load-bearing for "point Claudian at pyry"
  with `PYRY_NAME` set.
- `cmd/pyry/main.go:494-530` (`runAttach`) — the reference
  implementation for the dispatch tail. Auto-attach's stdio branch
  must produce **byte-identical** behaviour to
  `pyry attach --stdio <uuid>`: same `control.AttachStdio` call, same
  `os.Stdin`/`os.Stdout` wiring, same `context.Background()`, same
  `createIfMissing=false`, same error wrap (`fmt.Errorf("attach: %w", err)`),
  no human-affordance stderr lines (the `--stdio` mode already
  suppresses them).
- `internal/control/attach_stdio_client.go` (full, ~85 lines) —
  `AttachStdio`'s contract: dial scoped to `ctx`; once dialed, EOF on
  `in` returns `nil` (clean detach), server hangup returns `nil`,
  other I/O errors propagate. Output goroutine is joined before
  return. **No tty side effects** — safe to call when `os.Stdin` is a
  pipe, regular file, `/dev/null`, or absent.
- `internal/control/client.go:219-246` (`SessionsHasID`) — the wire
  client. Returns `(true, nil)` if registered, `(false, nil)` if
  well-formed-but-absent, and `(false, error)` for empty/malformed
  input or transport failure. The auto-attach treats every non-`true`
  outcome (false, transport error, malformed-id error) as
  "fall through to supervisor" — the AC is a strict "true → attach,
  everything else → spawn supervised."
- `internal/sessions/id.go:34-69` (`ValidID`) — the canonical UUIDv4
  validator. The auto-attach **does not** call this — `SessionsHasID`
  validates server-side and returns an error for malformed input,
  which we coerce to "fall through". A client-side check would
  duplicate the seam without saving any work; the probe is already
  cheap.
- `cmd/pyry/args_test.go:10-66` (`TestParseClientFlags`) — the
  pattern for unit-testing arg-shape helpers via `t.Setenv`. The new
  `extractSessionID` and `tryAutoAttach` tests slot alongside (or
  into) this file.
- `docs/specs/architecture/154-attach-stdio-mode.md` § "Wire-level:
  nothing changes" — confirms the server side is byte-identical-input
  for stdio attach. Auto-attach inherits that property: the daemon
  cannot tell whether the connecting client is `pyry attach --stdio`
  or `pyry --session-id <uuid>` falling into the auto-attach branch.
- `docs/specs/architecture/157-control-sessions-has-id.md` § "Read
  consistency" — the has-id read can race against a concurrent
  rm/evict, but the auto-attach treats the answer as a decision input,
  not a contract. A session removed between the probe and the attach
  surfaces as `Response.Error: "sessions: session not found"` from
  `AttachStdio`, which propagates as a normal attach failure (exit 1
  with `pyry: attach: sessions: session not found`). Not a special
  case — same shape as `pyry attach --stdio <stale-uuid>` produces.

## Context

Today's foreground pyry always spawns a supervised claude. Phase 1
shipped multi-session pools (#27, #28, #29, etc.); Phase 1.3a added
`--stdio` byte-forwarding (#154); Phase 1.3b added `--create-if-missing`
(#155); Phase 1.3c-1 added the cheap `sessions.has-id` query (#157).
Every primitive is now in place. The missing piece is the startup-time
decision about which mode to use.

Concretely, Claudian's "claude binary path" UI setting can be set to
`~/.local/bin/pyry`. Today, doing so causes Claudian to spawn a fresh
supervised claude per chat — bypassing the running daemon entirely.
The fix is to teach pyry's foreground-binary entry to detect "the
caller passed `--session-id <uuid>` AND a daemon already hosts that
UUID" and dispatch to the in-daemon session via stdio attach instead
of spawning a competing supervisor.

The detection must be:

1. **Conservative.** Default behaviour is unchanged: no `--session-id`
   in the args → no probe, no socket stat, supervised spawn. Existing
   pyry users see no behavioural difference.
2. **Fast in the no-daemon case.** ENOENT on the socket must
   short-circuit without dialling, without a network timeout, without
   any blocking call beyond the `os.Stat` itself. AC: <50ms.
3. **Permissive on every probe failure mode except "registered=true".**
   Stale socket, unresponsive daemon, malformed UUID, registry says
   absent — all of these fall through to supervised spawn. The
   "attach" branch is the *exception*, not the default.
4. **Identical to `pyry attach --stdio` on the dispatch tail.** Same
   wire, same exit codes, same stdin/stdout wiring, same error
   propagation. The auto-attach branch must be a pure rewriting of
   the entry path, with no semantic divergence below the dispatch.

## Design

### Surface added

Two new private helpers in `cmd/pyry/main.go`:

```go
// extractSessionID scans args for --session-id <value>, --session-id=<value>,
// -session-id <value>, or -session-id=<value> (claude accepts both single-
// and double-dash forms; we accept both for symmetry). Returns "" if not
// present or if the flag is the last arg with no value. The returned
// string is opaque to extractSessionID — UUID validation lives at the
// daemon (sessions.has-id rejects malformed input server-side).
//
// Pure function over a string slice. No environment, no syscalls.
func extractSessionID(args []string) string

// tryAutoAttach is the foreground-binary auto-attach gate. Called from
// runSupervisor after pyry-flag parsing but before any supervisor-mode
// side effect (logger setup, ring buffer, Bridge, Pool init).
//
// Returns (handled, err):
//
//   - (false, nil) — fall through to the existing supervisor path. This
//     is the outcome for: no --session-id in claudeArgs, PYRY_NO_AUTO_ATTACH=1
//     in the env, socket file absent (ENOENT), daemon non-responsive
//     (dial / has-id error), or has-id returns false.
//
//   - (true, err) — auto-attach was committed (the daemon hosts the
//     UUID); err is the result of control.AttachStdio. nil err means a
//     clean EOF detach (caller closed stdin); a non-nil err is a
//     wire/transport failure or a server-side rejection from the
//     attach handshake. The caller (runSupervisor) returns this verbatim;
//     main wraps with "pyry: " and exits 1 on err.
//
// The probe budget is a single os.Stat (microseconds) plus, if the
// socket exists, one short-deadline SessionsHasID round trip (default
// 1s ctx timeout — has-id is an O(1) registry read, no claude spawn).
// AC#3 (<50ms in the no-daemon case) is satisfied by structurally
// returning at the os.Stat ENOENT branch before any dial.
func tryAutoAttach(socketPath string, claudeArgs []string) (handled bool, err error)
```

One existing function gets a single `if` block:

```go
// runSupervisor (excerpt — splice point at ~line 272, after socketPath
// is resolved and before the logger / Bridge / Pool side effects):
//
//   socketPath := resolveSocketPath(*socketFlag, *name)
//   registryPath := resolveRegistryPath(*name)
//   claudeSessionsDir := resolveClaudeSessionsDir(*workdir)
//
//   // Phase 1.3c-2: foreground binary auto-attaches when the daemon
//   // hosts the requested session-id. Conservative — falls through on
//   // every failure mode except "definitely registered".
//   if handled, err := tryAutoAttach(socketPath, claudeArgs); handled {
//       return err
//   }
//
//   level := slog.LevelInfo
//   ...
```

No other production-code edits.

### Why a private helper, not a new exported function

The AC is "the foreground binary changes its startup decision". The
seam is a startup-path branch, not a reusable primitive. Exposing
`tryAutoAttach` from a package would invite re-use that has no
caller. Per CLAUDE.md "Stdlib over dependencies, abstractions over
speculation" — keep the helper private to `cmd/pyry`, unit-test via
`args_test.go`'s established pattern, ship.

### Why scan `claudeArgs`, not raw `os.Args`

`splitArgs` already does the work of separating pyry's flag namespace
(`-pyry-*`) from claude's. `--session-id` is unambiguously a claude
flag — there is no pyry flag with that name and there never will be
(pyry's namespace is `-pyry-*`). Scanning `claudeArgs` is correct by
construction: any `--session-id` is destined for the claude
subprocess, which is exactly the same UUID we want to query for.

Scanning raw `os.Args` would conflate the two namespaces and force
the helper to re-implement `splitArgs`'s rules. Re-using the existing
split is one line of plumbing in `runSupervisor` (`extractSessionID(claudeArgs)`)
and zero re-implementation.

### Auto-attach flow

```
runSupervisor(args)
  ├─ splitArgs(args) → pyryArgs, claudeArgs
  ├─ parse pyry flags (existing FlagSet)
  ├─ socketPath := resolveSocketPath(*socketFlag, *name)
  │
  ├─ tryAutoAttach(socketPath, claudeArgs):
  │   ├─ if os.Getenv("PYRY_NO_AUTO_ATTACH") == "1" → (false, nil)
  │   ├─ id := extractSessionID(claudeArgs)
  │   │   if id == "" → (false, nil)
  │   ├─ if _, err := os.Stat(socketPath); errors.Is(err, fs.ErrNotExist)
  │   │   → (false, nil)        ← AC#3 fast path: <50ms
  │   ├─ ctx, cancel := context.WithTimeout(Background, 1s); defer cancel
  │   ├─ has, err := control.SessionsHasID(ctx, socketPath, id)
  │   │   if err != nil       → (false, nil)  // daemon down, unresponsive,
  │   │                                       //   or malformed UUID; treat
  │   │                                       //   as "fall through"
  │   │   if !has             → (false, nil)  // well-formed but absent
  │   ├─ // commit to attach
  │   │   err := control.AttachStdio(Background, socketPath, id,
  │   │                              os.Stdin, os.Stdout, false)
  │   └─ return (true, fmt.Errorf("attach: %w", err))  // err may be nil
  │
  └─ if !handled: existing supervisor path (logger, Bridge, Pool, Run)
```

A few things worth noting:

- **The `os.Stat` check is the load-bearing fast path.** ENOENT on
  the socket is the common case for "no daemon running, user invoked
  pyry as a normal claude wrapper". Stat returns in microseconds
  (kernel cache hit on the parent dir is the worst case); the
  `errors.Is(err, fs.ErrNotExist)` branch returns before any dial,
  any context allocation, any goroutine spawn.

- **A 1-second `context.WithTimeout` on `SessionsHasID`** bounds the
  "socket exists, daemon is hung / mid-shutdown / left over after a
  crash" case. has-id is server-side an O(1) map read under
  `Pool.mu` RLock — 1s is generous by orders of magnitude, and it
  matches the conservative spirit of the AC ("daemon non-responsive"
  is one of the explicit fall-through cases).

- **Every `SessionsHasID` failure mode collapses to fall-through.**
  The function's contract is `(bool, error)`. We treat both `(_, err)`
  and `(false, nil)` as "do not auto-attach". A pedantic
  alternative would be to log a debug line on transport errors so
  operators can diagnose "I expected auto-attach to fire but didn't" —
  but the foreground binary has no logger configured at this point
  in startup (the logger is set up *after* the splice point), and
  introducing one early creates a new ordering hazard for a tiny
  affordance. Skipped.

- **`AttachStdio` is called with `createIfMissing=false`.** We
  already proved existence via has-id; passing `true` would be
  redundant on the take branch and semantically wrong (we'd be
  claiming "create this if missing" when we know it's not missing,
  and the SDK take-or-create branch has a different intended
  caller — Claudian's per-chat onboarding flow, not this
  daemon-handoff path).

- **The `context.Background()` for `AttachStdio`** matches
  `runAttach`'s call. `AttachStdio`'s ctx scopes the dial only;
  once dialed, the bridge runs until EOF on stdin or a wire error.
  Cancellation by the caller (Ctrl-C in the parent shell) flows
  through SIGINT → kernel writes EINTR to the active syscall →
  `io.Copy` returns an error → `AttachStdio` returns. No special
  signal handling needed.

### `extractSessionID` — exact shape

Claude's flag conventions accept both single- and double-dash forms
for long flags (Go's `flag` package accepts both forms via
`-name`/`--name`). We mirror that:

```go
func extractSessionID(args []string) string {
    for i := 0; i < len(args); i++ {
        a := args[i]
        switch {
        case a == "--session-id" || a == "-session-id":
            if i+1 < len(args) {
                return args[i+1]
            }
            return ""
        case strings.HasPrefix(a, "--session-id="):
            return strings.TrimPrefix(a, "--session-id=")
        case strings.HasPrefix(a, "-session-id="):
            return strings.TrimPrefix(a, "-session-id=")
        }
    }
    return ""
}
```

Notes:

- **No `flag.FlagSet` used.** A FlagSet would parse the entire
  claudeArgs slice and reject any flag it didn't know — but
  claudeArgs by construction contains arbitrary claude flags. We
  want to *peek* for one specific name without disturbing the rest.
- **No UUID validation here.** That's the daemon's job
  (`SessionsHasID` rejects malformed input via `Response.Error`).
  Doing the check client-side would duplicate the seam without
  saving any work — the probe is one stat plus one short wire call.
- **`--session-id` with no value returns `""`.** Treated as
  "session-id absent" → fall through. This matches what claude
  itself would do on receiving the malformed flag (error and exit) —
  but that's claude's problem, not pyry's. Our job is to decide
  whether to attach, not to validate claude's argv.
- **First match wins.** `--session-id A --session-id B` returns A.
  Claude's flag library would also surface only one of these; we
  don't try to be smarter than claude.

### `PYRY_NO_AUTO_ATTACH` escape hatch

Per AC#5: `PYRY_NO_AUTO_ATTACH=1` in the environment forces the
legacy supervised-spawn path. Implementation: the very first check
inside `tryAutoAttach`, before `extractSessionID`, before `os.Stat`,
before anything else. No flag — env var only, matches the
`PYRY_NAME` precedent.

```go
if os.Getenv("PYRY_NO_AUTO_ATTACH") == "1" {
    return false, nil
}
```

Strict equality with `"1"`, not "any non-empty value" — matches the
common Go convention (`GODEBUG`, `GOTRACEBACK`) and avoids surprising
the operator who exports `PYRY_NO_AUTO_ATTACH=` (empty) from a stale
shell config and then can't figure out why auto-attach isn't firing.

The intended consumers are:

- Tests (e2e tests in #163/#164 that need to assert "supervised spawn
  fired" without first having to confirm the daemon is down).
- Operators debugging auto-attach behaviour (set, re-run, observe
  the supervised path).
- A safety valve for any future bug we don't yet know about.

### Why no dispatch in `run()` (the top-level switch)

A tempting alternative: handle auto-attach as a preamble in `run()`
before the verb-switch. Rejected because:

- The verb-switch dispatches on `os.Args[1]`, which is one of
  `version` / `status` / `stop` / `logs` / `attach` / `sessions` /
  `install-service` / `help` / `-v` / `--version` / `-h` / `--help`.
  None of those values can ever be `--session-id`. The auto-attach
  path is structurally orthogonal to those verbs.
- `runSupervisor` is the unique sink for "no recognised verb". The
  one place we want to splice is the one place that runs.
- Splicing in `run()` would force the auto-attach to do its own
  splitArgs / flag parsing for socket resolution. Splicing in
  `runSupervisor` reuses the existing parse for free.

The cost of the splice point being inside `runSupervisor` is one
extra stack frame on the auto-attach path (negligible) and the
visual coupling that "supervisor mode" is named more broadly than
its post-splice contents. Worth it for the simplicity gain.

### Concurrency

- **No new goroutines** beyond what `AttachStdio` already spawns
  internally (one output-copy goroutine, joined before return —
  see #154's spec).
- **Strictly sequential probe.** stat → dial → encode → decode → branch.
  No fan-out, no select, no shared state.
- **No new lock-order edges.** The daemon-side handler runs on the
  per-conn goroutine and takes `Pool.mu` RLock only (#157's
  contract). The client-side path holds no locks.
- **Signal handling is unchanged.** When we fall through, the
  existing `signal.NotifyContext(..., SIGINT, SIGTERM)` setup
  applies (started a few lines below the splice). When we attach,
  the kernel delivers the signal to pyry's pid; `io.Copy(conn, in)`
  returns with an error; `AttachStdio` propagates; `tryAutoAttach`
  returns; `runSupervisor` returns; `main` wraps and exits. No
  zombie goroutines, no orphaned conn.

### Error handling

| Failure mode | Outcome |
|---|---|
| `PYRY_NO_AUTO_ATTACH=1` | `(false, nil)` → supervised spawn. |
| No `--session-id` in `claudeArgs` | `(false, nil)` → supervised spawn. AC#2 path. |
| `os.Stat(socket)` returns ENOENT | `(false, nil)` → supervised spawn. AC#3 fast path (<50ms). |
| `os.Stat(socket)` returns other error (EPERM, EACCES, …) | `(false, nil)` → supervised spawn. Same posture: probe failure → fall through, never crash. |
| Dial timeout / refused connection | `(false, nil)` (via `SessionsHasID` error). |
| `SessionsHasID` returns `(_, err)` for any reason | `(false, nil)`. Coerce all transport / validation errors to fall-through. |
| `SessionsHasID` returns `(false, nil)` (UUID absent) | `(false, nil)` → supervised spawn. AC#1 fall-through path. |
| `SessionsHasID` returns `(true, nil)` (UUID present) | Commit to attach. |
| `AttachStdio` returns `nil` (clean EOF detach) | `(true, nil)` → exit 0 (parity with `pyry attach --stdio`). |
| `AttachStdio` returns transport / handshake error | `(true, fmt.Errorf("attach: %w", err))` → main wraps, exits 1. |

The "everything that isn't a true success falls through" posture is
the load-bearing simplicity. There is exactly one way to take the
attach branch (has-id said yes); every other path leads back to the
existing supervised-spawn flow with zero side effects.

### Affordance: no stderr noise, no human-readable lines

`pyry attach --stdio` already suppresses the `"pyry: attached. Press
Ctrl-B d to detach."` and `"pyry: detached."` lines (see #154's
"Affordance stderr lines suppressed under `--stdio`"). The
auto-attach inherits that — it calls `control.AttachStdio` directly,
not `runAttach`. The dispatched path is silent on stderr, matching
what an SDK consumer expects from a transparent byte conduit.

The case for *not* logging "auto-attached to existing daemon
session": the foreground caller is by construction a programmatic
parent (Claudian or similar). Any unsolicited stderr line is a
foreign substring its log scraper may or may not handle. If we ever
want a debug knob, `PYRY_VERBOSE=1` could opt-in — out of scope here.

## Testing strategy

Two new files:

- **`cmd/pyry/auto_attach_test.go`** (~200 LOC) — unit tests for
  `extractSessionID` and `tryAutoAttach`. Co-located with
  `args_test.go`'s shape: `t.Setenv` for env-var control, `t.TempDir`
  for socket paths, no real daemon spawn (the helper is structured
  so every code path *except* the attach commit can be exercised
  without one).

- **No e2e harness extension.** AC explicitly defers e2e to siblings
  #163 (happy path) and #164 (fallback). The unit-level coverage is
  sufficient for this ticket.

| Test | What it asserts |
|---|---|
| `TestExtractSessionID/space_separated_double_dash` | `["--session-id", "abc"]` → `"abc"`. |
| `TestExtractSessionID/space_separated_single_dash` | `["-session-id", "abc"]` → `"abc"`. |
| `TestExtractSessionID/glued_double_dash` | `["--session-id=abc"]` → `"abc"`. |
| `TestExtractSessionID/glued_single_dash` | `["-session-id=abc"]` → `"abc"`. |
| `TestExtractSessionID/absent` | `["--model", "sonnet"]` → `""`. |
| `TestExtractSessionID/no_args` | `[]` → `""`. |
| `TestExtractSessionID/last_arg_no_value` | `["--session-id"]` → `""`. |
| `TestExtractSessionID/empty_value` | `["--session-id", ""]` → `""` (treated as absent). Pinned shape so the call site doesn't need a follow-up `if id != ""` guard — `tryAutoAttach`'s "id == empty → fall through" branch covers it once. |
| `TestExtractSessionID/empty_glued_value` | `["--session-id="]` → `""`. |
| `TestExtractSessionID/preserves_value_with_dashes` | `["--session-id", "abc-def-123"]` → `"abc-def-123"` (we don't parse the value — has-id rejects malformed UUIDs server-side). |
| `TestExtractSessionID/first_match_wins` | `["--session-id", "A", "--session-id", "B"]` → `"A"`. |
| `TestExtractSessionID/embedded_in_args` | `["--model", "sonnet", "--session-id", "abc", "-p", "hi"]` → `"abc"`. |
| `TestTryAutoAttach_NoSessionID` | claudeArgs without `--session-id` → `(false, nil)`, no `os.Stat` call. (Test via passing a `socketPath` that does *not* exist; if the helper called Stat we'd still get fall-through, but a wall-clock timer asserts <1ms — the path bails before Stat.) |
| `TestTryAutoAttach_EnvOptOut` | `PYRY_NO_AUTO_ATTACH=1` set → `(false, nil)` even with a valid `--session-id` and a (real) socket path that would otherwise be probed. Pinned via `t.Setenv`. |
| `TestTryAutoAttach_EnvOptOutNonOne` | `PYRY_NO_AUTO_ATTACH=true` → still probes (strict-`"1"` semantics). Documents the convention. |
| `TestTryAutoAttach_SocketAbsent_FastPath` | `socketPath` points to a non-existent file in `t.TempDir()`. claudeArgs contains `--session-id <uuid>`. Asserts `(false, nil)` AND `time.Since(start) < 50*time.Millisecond` (AC#3). On a developer laptop this runs in well under 1ms — the 50ms ceiling is the AC contract, not the expected steady-state. |
| `TestTryAutoAttach_SocketStatOtherError` | Optional / platform-conditional: a directory at `socketPath` with no read perms (chmod 000). Asserts `(false, nil)` — non-ENOENT errors also fall through. Skip on Windows (out of scope). Best-effort; if `chmod 000` doesn't reliably produce EACCES on the test runner, document and skip. |
| `TestTryAutoAttach_DaemonUnresponsive` | Listen on the socket path with `net.Listen("unix", path)` but never accept (socket exists but no `handle` goroutine). Auto-attach should dial, hit the 1s ctx timeout, return `(false, nil)`. Wall-clock budget for the test: assert returns within ~1.5s. Demonstrates "daemon non-responsive → fall through" per AC#1. |
| `TestTryAutoAttach_HasIDFalse` | Spin a tiny in-process control server (reuse `startServerWithSessioner` from `internal/control` test helpers — already exposes `sessions.has-id`) backed by a `fakeResolver` with no sessions. claudeArgs contains a valid-looking UUID; helper returns `(false, nil)`. Pins the AC#1 "UUID unknown → fall through" branch. |
| `TestTryAutoAttach_HasIDInvalid` | Same shape, but pass a malformed `--session-id` value (non-UUID). Server returns `Response{Error: "sessions.has-id: invalid uuid"}` → `SessionsHasID` returns an error → helper returns `(false, nil)`. Pins "malformed UUID → fall through". |

Key plumbing notes for the developer:

- **Reusing `internal/control`'s test helpers from `cmd/pyry`** —
  `startServerWithSessioner` lives in `internal/control` test files
  (lower-case package access). Two viable paths:
  - **(Preferred)** Stand up the test server directly in
    `auto_attach_test.go` using only public `internal/control` API:
    `net.Listen("unix", path)` + a tiny `accept` loop that decodes
    `Request` and writes back canned `Response`s. ~30 LOC of
    test scaffolding for the two has-id tests.
  - Move `startServerWithSessioner` into a new
    `internal/control/testserver` test-helper package. Heavier;
    only worth it if a future ticket needs the same shape from
    `cmd/pyry`. Defer.
- **The `--session-id` value in tests** uses canonical
  UUIDv4 strings like `"11111111-2222-4333-8444-555555555555"` (note
  v4 nibble at position 14, RFC 4122 variant at position 19).
  `internal/control/sessions_has_id_test.go` already uses this shape.
- **The `tryAutoAttach` helper's signature** does not take an
  `io.Reader`/`io.Writer` for stdin/stdout — it pulls `os.Stdin` /
  `os.Stdout` directly. That's correct for production, awkward for
  tests of the *attach* branch. The unit tests **do not** exercise
  the AttachStdio commit — that's e2e (#163). Tests stop at the
  has-id decision and rely on `internal/control/attach_stdio_client_test.go`
  (#154) for AttachStdio's own contract.

## Open questions

None. The design is mechanical: one helper to peek the args, one
helper to gate the probe, one `if` block to splice them in. Every
branch falls through on every failure mode that isn't "definitely
registered." The escape hatch is one env var. The probe budget is
one stat plus one short-deadline wire call. Every primitive consumed
(`SessionsHasID`, `AttachStdio`, `defaultName`, `resolveSocketPath`,
`splitArgs`) is already shipped and tested.

If the developer hits "I want to log when auto-attach fires for
debugging," resist — see "Affordance" above. Add it via
`PYRY_VERBOSE=1` in a separate ticket if a real operator asks.

## Out of scope

- WS/remote attach (Phase 3 + 5). Auto-attach is local-Unix-socket
  only.
- Multi-daemon-name discovery (always single resolved name via the
  existing chain).
- A `--no-auto-attach` flag on pyry itself. The env var covers the
  "operator wants to bypass" case; a flag would have to thread
  through `splitArgs` / the FlagSet without colliding with claude's
  flag namespace, which is more change for the same semantic.
- E2E tests of the full auto-attach flow → tracked in **#163**
  (happy path: pyry process spawns, dispatches, bytes round-trip)
  and **#164** (fallback: socket missing, daemon down, UUID
  unknown, env opt-out — every fall-through arm).
- Logging from the auto-attach path. The foreground binary has no
  logger at the splice point.
- Telemetry / counter for "auto-attach fired" — not on any
  observability surface today.

## Documentation

Update `docs/knowledge/features/control-plane.md`:

- Add a new subsection "Foreground binary auto-attach (1.3c-2)"
  alongside the existing "Attach: stdio mode (1.3a)" / "Attach:
  --create-if-missing (1.3b)" / "Sessions: has-id seam (1.3c-1)"
  entries. Document:
  - The startup-time gate: `extractSessionID(claudeArgs)` →
    `os.Stat(socket)` → `SessionsHasID(ctx, socket, id)` →
    `AttachStdio` if `true`, else fall through.
  - The `PYRY_NO_AUTO_ATTACH=1` escape (strict equality with `"1"`).
  - The "every probe failure mode falls through" posture.
  - The <50ms ENOENT fast path.
  - The reuse of `defaultName` / `resolveSocketPath` for the
    daemon-name resolution chain — same surface as every existing
    pyry client subcommand.

Update `docs/PROJECT-MEMORY.md` after the developer ticket lands —
add a "Phase 1.3c-2 (#158)" entry under the foreground / startup
work, mirroring the 1.3a / 1.3b / 1.3c-1 entries' shape (helper
names, splice point, fall-through rules, test coverage).

After editing, run `qmd update && qmd embed` (per CLAUDE.md).

## Production / test diff sizing

| Surface | LOC est. | File(s) |
|---|---|---|
| `extractSessionID` | ~20 | `cmd/pyry/main.go` |
| `tryAutoAttach` | ~50 | `cmd/pyry/main.go` |
| `runSupervisor` splice (the `if handled, err := …; handled { return err }` block + comment) | ~5 | `cmd/pyry/main.go` |
| **Production total** | **~75** | **1 file** |
| Unit tests for both helpers | ~200 | `cmd/pyry/auto_attach_test.go` (new) |
| `docs/knowledge/features/control-plane.md` § "Foreground binary auto-attach (1.3c-2)" | ~30 | `docs/knowledge/features/control-plane.md` |

Comfortably within the S envelope (≤100 production LOC, 1 production
file, 0 new exported types). No consumer cascades, no cross-package
coordination, no fan-out edits.
