# #92 — `pyry sessions rename` CLI verb (full UUID)

Phase 1.1c-B2a. Consumes the `sessions.rename` wire and `Renamer`
seam landed by #90 (`control.SessionsRename` client wrapper, typed
`ErrCodeSessionNotFound` propagation), and plugs into the
`runSessions` router landed by #76. Sibling slice #99 (`pyry
sessions rm`) just landed and established the conventions this
ticket follows verbatim:

- `flag.NewFlagSet` per sub-verb, even when the verb has no flags
  today (extensibility + consistent help output).
- Sentinel-wrapped usage errors → `os.Exit(2)` at the handler
  boundary, message printed without `pyry:` prefix.
- Sentinel-wrapped runtime errors that need AC-prescribed plain
  messages → `os.Exit(1)` from the handler, no `pyry:` prefix.
- Other errors → `fmt.Errorf("sessions <verb>: %w", err)` flow
  through main's top-level printer with the `pyry:` prefix.

The novel bits this slice adds are small:

1. One `case "rename":` arm in `runSessions`.
2. One new `parseSessionsRenameArgs` helper (two positionals
   instead of one).
3. One new `runSessionsRename` handler (no prefix resolution, no
   policy translation, single typed-error mapping).
4. One word added to `sessionsVerbList`, the top-level
   reserved-verb comment, and `printHelp`'s sessions line.
5. Tests: extend `cmd/pyry/sessions_test.go` with a parser table
   + a router-dispatch test, plus a new
   `internal/e2e/sessions_rename_test.go`.

**No prefix resolution in this slice.** AC#4 explicitly forbids it
— the `<id>` argument is forwarded as-is to `control.SessionsRename`,
and non-UUID input falls through to the server's
`ErrSessionNotFound` mapping. The follow-up ergonomic slice (a
sibling of #99's prefix-resolution work) lifts
`resolveSessionIDViaList` into a shared helper at that point.

## Files to read first

- `cmd/pyry/main.go:485-527` — `sessionsVerbList` constant,
  `errSessionsUsage`, and the `runSessions` switch. The
  `case "rename":` arm slots in alongside `case "rm":`; the
  constant grows from `"new, rm"` to `"new, rm, rename"` in the
  same edit.
- `cmd/pyry/main.go:566-724` — `errSessionsRmUsage`,
  `parseSessionsRmArgs`, `runSessionsRm`. The complete precedent
  this ticket mirrors: usage-sentinel pattern, FlagSet
  configuration, handler exit-code policy, typed-error sentinel
  matching, and `fmt.Errorf("sessions rm: %w", err)` final wrap.
  Read end-to-end before writing the rename equivalents — the
  shape is intentionally identical except where called out below.
- `cmd/pyry/main.go:529-564` — `parseSessionsNewArgs` /
  `runSessionsNew`. The simpler precedent (no usage sentinel, no
  exit-code branching) shows the minimum FlagSet shape; useful
  to compare against `parseSessionsRmArgs` when deciding what
  rename inherits from each.
- `cmd/pyry/main.go:21` — top-level reserved-verb comment block
  line `pyry sessions <verb>  Multi-session management (verbs:
  new, rm)`. One word edit (`new, rm` → `new, rm, rename`).
- `cmd/pyry/main.go:861-862` — `printHelp` line
  `pyry sessions <verb> [flags]                   manage sessions
  on a running daemon (verbs: new, rm)`. Same one-word edit.
- `cmd/pyry/sessions_test.go:70-90` — `TestRunSessions_RmDispatch`.
  Template for `TestRunSessions_RenameDispatch`: pass a bogus
  socket and assert the dial failure is reached (proves the
  switch dispatched to the new arm rather than the
  `default → unknown verb` branch).
- `cmd/pyry/sessions_test.go:92-143` — `TestParseSessionsRmArgs`
  table-driven test. The `TestParseSessionsRenameArgs` table
  follows the same shape — `{name, args, wantID, wantLabel,
  wantUsage, wantErr}` (drop `wantPolicy`, add `wantLabel`).
- `internal/control/client.go:156-186` — `SessionsRename(ctx,
  socketPath, id, newLabel) error`. The wire wrapper this CLI
  handler calls. `ErrCodeSessionNotFound` →
  `sessions.ErrSessionNotFound`; other server errors propagate
  as `errors.New(resp.Error)`.
- `internal/sessions/pool.go:393-429` — `Pool.Rename` contract.
  Empty `newLabel` is valid ("clear the label"); no-op rename
  (newLabel == current) returns nil. The CLI doesn't need to
  distinguish either case from a normal success — both surface
  as `OK: true` on the wire.
- `internal/sessions/pool.go:31-32` — `ErrSessionNotFound`
  sentinel definition. Only typed sentinel `Pool.Rename`
  emits; no `ErrCannotRemoveBootstrap` analogue (renaming
  the bootstrap is allowed — confirmed by `Pool.Rename`'s
  contract: it has no bootstrap special-case).
- `internal/e2e/sessions_rm_test.go:1-61, 241-351` — template
  for the e2e file. `TestSessionsRm_E2E_Success_Default`
  (happy path against a freshly-minted session),
  `TestSessionsRm_E2E_UnknownUUID` (typed-error mapping),
  `TestSessionsRm_E2E_NoDaemon` (dial failure), and the
  `findSession` helper at the top all carry over.
- `docs/specs/architecture/99-cli-sessions-rm.md` § "Handler
  (runSessionsRm)" and § "Error handling" — the full
  precedent for exit-code policy and message formatting. The
  rationale paragraphs in that spec apply identically here
  and are not re-litigated below.
- `docs/specs/architecture/90-control-sessions-rename.md`
  § "Error handling" — confirms `Pool.Rename`'s only typed
  sentinel is `ErrSessionNotFound` (no bootstrap-rejection
  analogue), and that empty `NewLabel` is forwarded
  unchanged through the wire.

## Context

`sessions.rename` (the wire verb and `Pool.Rename` seam) shipped in
#90; the `runSessions` router shipped in #76. This ticket is the
operator-facing CLI consumer. One operator types `pyry sessions
rename <uuid> <new-label>`, the CLI dials the daemon's control
socket, asks it to update the named session's label, and exits 0
on success.

Three operational shapes the CLI handler must satisfy:

1. **Argument parsing.** `flag.NewFlagSet` per sub-verb (even
   without flags today — for symmetry with `new` and `rm`, and
   so future flags slot in mechanically). Two positional args:
   `<id>` and `<new-label>`. Wrong arity exits 2 with a usage
   line. The empty string is a **valid value** for
   `<new-label>` (it clears the on-disk label per `Pool.Rename`'s
   contract) — the arity check counts positionals, not non-empty
   ones.
2. **Wire call.** Single `control.SessionsRename(ctx, sock, id,
   newLabel)`. `<id>` is forwarded as-is — no prefix resolution
   in this slice.
3. **Error mapping.** `errors.Is` against
   `sessions.ErrSessionNotFound` (the only typed sentinel
   `Pool.Rename` emits, propagated via #90's
   `ErrCodeSessionNotFound`). The matched case writes the
   AC-prescribed `no session with id "<id>"` message to stderr
   without the `pyry:` prefix and exits 1. All other errors flow
   through `fmt.Errorf("sessions rename: %w", err)` for main's
   top-level `pyry: <err>` print.

This ticket scope is **CLI-layer only**. No new wire surface, no
seam work, no e2e harness changes. The mechanics of the rename
(label update, registry persist) are entirely behind the wire.

## Design

### Top-level dispatch (cmd/pyry/main.go)

Three near-mechanical edits inside `runSessions` and its
neighbourhood:

```go
// Updated constant — verb list grows by one. #76's spec calls out
// that 1.1b/c/d/e each append one verb here in the same edit that
// adds the case. This is the 1.1c-B2a increment.
const sessionsVerbList = "new, rm, rename"

// In runSessions's switch, after `case "rm":`:
case "rename":
    return runSessionsRename(socketPath, subArgs)
```

Plus the top-level reserved-verb comment block at line 21:

```go
//	pyry sessions <verb>  Multi-session management (verbs: new, rm, rename)
```

The router's signature, the `parseClientFlags` peel, and the
sub-arg dispatch shape are unchanged. Adding `case "rename":` is
the entire router-level diff.

### Argument parsing (parseSessionsRenameArgs)

```go
// errSessionsRenameUsage marks every parse-time failure of
// `pyry sessions rename` as a usage error. runSessionsRename matches
// via errors.Is and exits 2 with the wrapped message printed
// verbatim (no `pyry:` prefix). One sentinel covers arity, future
// mutually-exclusive flag guards, and any other handler-side usage
// rule — the wire-call path is reached only on parse-success, so
// runSessionsRename doesn't need to discriminate further. Mirrors
// errSessionsRmUsage's shape exactly.
var errSessionsRenameUsage = errors.New("usage")

// parseSessionsRenameArgs parses `<id> <new-label>`. Returns
// (id, newLabel, err). Both positionals are required; the empty
// string IS a valid value for <new-label> (Pool.Rename treats it
// as "clear the on-disk label" per #62), so the arity check counts
// positionals (must be exactly 2) rather than testing for non-empty
// strings.
//
// Mirrors parseSessionsRmArgs's shape: extracted from
// runSessionsRename so flag-parsing rules are unit-testable
// without dialling the control socket. Every error returned wraps
// errSessionsRenameUsage so runSessionsRename can map the whole
// class to exit 2 with a single errors.Is check.
//
// No flags today — the FlagSet exists for symmetry with `new` and
// `rm` and so a future `--force` (or whatever) slots in
// mechanically without restructuring the handler.
func parseSessionsRenameArgs(args []string) (id, newLabel string, err error) {
    fs := flag.NewFlagSet("pyry sessions rename", flag.ContinueOnError)
    fs.SetOutput(os.Stderr)
    if err := fs.Parse(args); err != nil {
        return "", "", fmt.Errorf("%w: %v", errSessionsRenameUsage, err)
    }
    if fs.NArg() != 2 {
        return "", "", fmt.Errorf("%w: expected <id> <new-label>, got %d positional args", errSessionsRenameUsage, fs.NArg())
    }
    return fs.Arg(0), fs.Arg(1), nil
}
```

**Why exactly 2 positionals, not "at least 2".** The verb's
contract is single-id, single-label. Three or more positionals
is a mistake (e.g. an unquoted multi-word label —
`pyry sessions rename <uuid> hello world` would silently
discard `world` if we accepted ≥2). Rejecting with a usage error
forces the operator to quote multi-word labels:
`pyry sessions rename <uuid> "hello world"`. Same shape `git
config` and `kubectl label` use.

**Why not split `<new-label>` from a `--name`-style flag.** Two
reasons:

1. **Symmetry with `git mv` / `kubectl rename`.** Operators expect
   `<old> <new>` for two-arg mutations.
2. **`--name` is the create-time label flag on `pyry sessions new`
   (#76).** Reusing it on `rename` would conflate the
   verb-distinguishing semantic of `new` ("set this label on a
   freshly-minted session") with `rename`'s "replacement value"
   semantic. Distinct shapes per verb avoid that collision.

The trade-off (positionals vs. flag) costs the operator one extra
quote pair when the new label has whitespace; gains a clean
verb-by-verb argument shape. Same trade-off `kubectl rename` made.

**Empty `<new-label>` is accepted.** The arity check counts
positional tokens (`fs.NArg() == 2`), not non-empty values, so
`pyry sessions rename <uuid> ""` parses as `(uuid, "")` and
forwards through. Pool.Rename treats `""` as "clear the label"
per #62. AC#1 explicitly requires this — "no separate `--clear`
flag."

### Handler (runSessionsRename)

```go
// runSessionsRename implements `pyry sessions rename <id> <new-label>`:
// dial the daemon's control socket and ask it to update the named
// session's human-friendly label. <id> is forwarded verbatim — no
// prefix resolution in this slice (follow-up ergonomic slice).
//
// Exit codes match the rest of cmd/pyry:
//
//	0 — rename succeeded.
//	1 — runtime error (unknown id, server-side error, or
//	    no-daemon dial failure).
//	2 — usage error (parse failure or wrong arity). Mirrors
//	    runSessionsRm's exit-2 policy.
//
// The AC-prescribed unknown-id message is printed to stderr
// without the `pyry:` outer-error prefix; other errors flow
// through `fmt.Errorf("sessions rename: %w", err)`, which main's
// top-level error printer prepends with `pyry: `.
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

    if err := control.SessionsRename(ctx, socketPath, id, newLabel); err != nil {
        if errors.Is(err, sessions.ErrSessionNotFound) {
            fmt.Fprintf(os.Stderr, "no session with id %q\n", id)
            os.Exit(1)
        }
        return fmt.Errorf("sessions rename: %w", err)
    }
    return nil
}
```

**Why `os.Exit(1)` from the unknown-id branch rather than returning
an error.** Same rationale as #99 (`runSessionsRm`): main's
top-level printer prepends `pyry: ` and exits 1 on any non-nil
return from `run()`. AC#2 prescribes the message text without a
`pyry:` prefix. `fmt.Fprintln(os.Stderr, ...)` + `os.Exit(1)`
matches `runSessionsRm`'s precedent and `runAttach`'s exit-2
shape. Tests capture stderr + observe the exit code rather than a
returned error.

**Why no bootstrap-rejection branch.** Unlike `Pool.Remove`,
`Pool.Rename` has no bootstrap special-case — renaming the
bootstrap session is allowed (and useful: an operator may want
to rename the bootstrap to e.g. "primary" once they're juggling
multiple). #90's wire surface only propagates one
`ErrCode` for rename: `ErrCodeSessionNotFound`. No additional
sentinel-match branch needed. (Confirmed by reading `Pool.Rename`
at `internal/sessions/pool.go:412-429` — it returns only
`ErrSessionNotFound` and saveLocked errors; no
`ErrCannotRenameBootstrap`.)

**Why no prefix-resolution call.** AC#4: "<id> accepts the full
canonical UUID only in this slice — non-UUID input falls through
to the underlying server's not-found mapping." A user typing a
prefix gets a clean `no session with id "<prefix>"` error from
the typed-sentinel branch (the server doesn't find the prefix
verbatim in the registry; returns `ErrSessionNotFound`; client
maps to the AC message). Adding `resolveSessionIDViaList` here
would expand scope into the follow-up slice's territory.

**Why `os.Exit` is safe under deferred cleanup.** `runSessionsRename`'s
only deferred work is `cancel()`. On `os.Exit`, deferred functions
don't run — but `cancel()`'s only effect is releasing the
context's timer, a process-local resource the kernel reaps on
exit. No socket, no file, no goroutine outlives the call. Safe.
(Same rationale as #99.)

**Why surface `<id>` (the operator's input), not a normalised
form.** Today `<id>` is required to be the full canonical UUID, so
"input" and "canonical" are the same string — no ambiguity. When
the follow-up slice adds prefix resolution, that handler will
echo the operator's input on the unknown-id path (matches #99's
`runSessionsRm` race-window comment). Using `id` here keeps the
two handlers' message formats identical for the future merge.

### Help text update (printHelp)

One word in the `pyry sessions <verb>` block (cmd/pyry/main.go:861-862):

```
  pyry sessions <verb> [flags]                   manage sessions on a running
                                                  daemon (verbs: new, rm, rename)
```

Single-word edit (`new, rm` → `new, rm, rename`). Matches the
1.1b/c/d/e convention #76 and #99 established.

### Data flow

```
 Operator                                CLI (this ticket)                          Daemon (#90)
 ────────                                ─────────────────                          ────────────
 pyry sessions rename <uuid> "alpha"
   │
   ▼
 main → run() → runSessions(args)
   │
   peel global flags (parseClientFlags)
   │
   dispatch on "rename" → runSessionsRename(socketPath, ["<uuid>", "alpha"])
   │
   parseSessionsRenameArgs → ("<uuid>", "alpha", nil)
   │
   control.SessionsRename(ctx, sock, "<uuid>", "alpha") ───────────────►
   │                                                                       handleSessionsRename
   │                                                                         Pool.Rename(<uuid>, "alpha")
   │                                                                         → nil
   │     ◄────────────────────────────────────────────────────────────
   │     {ok: true}
   │
   return nil → main → exit 0


 Empty-label clear path:
 pyry sessions rename <uuid> ""
   │
   parseSessionsRenameArgs → ("<uuid>", "", nil)   ← arity counts positionals, not non-empty
   │
   control.SessionsRename(ctx, sock, "<uuid>", "") ───────────────────►
   │     wire payload: {sessions:{id:"<uuid>"}}     ← NewLabel elided via omitempty
   │                                                                       handleSessionsRename
   │                                                                         Pool.Rename(<uuid>, "") → nil
   │     ◄────────────────────────────────────────────────────────────
   │     {ok: true}
   │
   return nil → main → exit 0


 Error path (unknown id):
   │
   control.SessionsRename(...) ───────────────────────────────────────►
   │                                                                       Pool.Rename → ErrSessionNotFound
   │                                                                       resp.ErrorCode = "session_not_found"
   │     ◄────────────────────────────────────────────────────────────
   │     ErrorCode → sessions.ErrSessionNotFound (typed sentinel)
   │
   runSessionsRename → errors.Is(err, sessions.ErrSessionNotFound) →
                      Fprintf(stderr, "no session with id %q\n", id) →
                      os.Exit(1)


 Error path (no daemon):
   │
   control.SessionsRename(...) → request() → dial() → ENOENT
   │
   runSessionsRename → not ErrSessionNotFound →
                      return fmt.Errorf("sessions rename: %w", err) →
                      main prints `pyry: sessions rename: dial /path/sock: connect: ...`,
                      exit 1   (matches `pyry status` / `pyry stop` shape)
```

### Concurrency

No new goroutines, mutexes, or channels.

- `runSessionsRename` is sequential: parse → wire call → return.
- The 30s `context.WithTimeout` bounds the single wire call.
  `Pool.Rename` is bounded by `Pool.mu` + `saveLocked` (sub-
  second in normal operation); 30s is generous and matches the
  per-verb ceiling in `runSessionsNew` / `runSessionsRm`.
- One subtle interleave: a concurrent `sessions.rm` for the same
  id could race this rename. Both go through `Pool.mu`; whichever
  acquires first wins. Rename-after-Remove → `ErrSessionNotFound`
  (handler emits the AC message, exit 1). Remove-after-Rename →
  the rename succeeded, the subsequent rm sees the renamed entry.
  Both outcomes are well-defined and surface cleanly. No CLI-side
  coordination needed.

### Error handling

End-to-end error catalogue, mapping each AC failure to its
implementation path:

| Failure | Source | CLI mapping | Exit | stderr message |
|---|---|---|---|---|
| Wrong arity (≠2 positionals) | `parseSessionsRenameArgs` | `errSessionsRenameUsage` | 2 | `pyry sessions rename: usage: expected <id> <new-label>, got N positional args` |
| `flag.Parse` failure (unknown flag) | `parseSessionsRenameArgs` | `errSessionsRenameUsage` (wraps the bare `flag` error) | 2 | `pyry sessions rename: usage: <flag-package message>` plus `flag`'s own line |
| Daemon not running | `control.SessionsRename` dial fail | bubble through `fmt.Errorf` wrap | 1 | `pyry: sessions rename: ... dial socket: ...` (matches `pyry status` / `pyry stop`) |
| Unknown UUID | `control.SessionsRename` returns `sessions.ErrSessionNotFound` (typed via wire `ErrCodeSessionNotFound`) | sentinel match | 1 | `no session with id "<original-input>"` |
| Other server error (registry persist failure, missing-id guard, ...) | `control.SessionsRename` returns `errors.New(resp.Error)` | bubble through `fmt.Errorf` wrap | 1 | `pyry: sessions rename: <verbatim server message>` |

The AC-prescribed unknown-id message has **no** `pyry:` prefix;
the table's "bubble through" rows do, because main prepends
`pyry: ` when it prints a returned error.

The "no stack traces on user-facing errors" AC item is satisfied
trivially — none of the paths panic or `runtime.Stack`.

## Testing strategy

Two test files: extend `cmd/pyry/sessions_test.go` (unit) and add
`internal/e2e/sessions_rename_test.go` (e2e). Stdlib `testing`
only.

### Unit tests — `cmd/pyry/sessions_test.go` (extended)

Add `TestParseSessionsRenameArgs`, structured as a table mirroring
`TestParseSessionsRmArgs`.

| name | args | wantID | wantLabel | wantUsage | wantErr (substring) |
|---|---|---|---|---|---|
| no args | `nil` | `""` | `""` | true | `expected <id> <new-label>` |
| only `<id>` | `["abc"]` | `""` | `""` | true | `expected <id> <new-label>` |
| `<id> <label>` | `["abc", "alpha"]` | `"abc"` | `"alpha"` | false | `""` |
| `<id> ""` (clear) | `["abc", ""]` | `"abc"` | `""` | false | `""` |
| `<id> <label> extra` | `["abc", "alpha", "extra"]` | `""` | `""` | true | `expected <id> <new-label>` |
| label with spaces (single token, quoted by shell) | `["abc", "hello world"]` | `"abc"` | `"hello world"` | false | `""` |
| unknown flag | `["--unknown", "abc", "alpha"]` | `""` | `""` | true | `flag provided but not defined` |

The "wantUsage" rows assert `errors.Is(err,
errSessionsRenameUsage)` plus the message-fragment match — keeps
the sentinel chain observable from tests.

The empty-label-clear row pins AC#1: "`pyry sessions rename
<full-uuid> ""` is accepted ... No separate `--clear` flag." If
arity ever switched to "non-empty values" by mistake, this row
would fail.

The label-with-spaces row pins the "exactly 2 positionals" rule
under shell quoting. (`os.Args` unquotes shell-quoted strings
before our flag package sees them; the FlagSet observes a single
token containing a space.)

### Unit tests — `cmd/pyry/sessions_test.go` (extended, dispatch)

Add `TestRunSessions_RenameDispatch`: parallel to
`TestRunSessions_RmDispatch`. Verifies the router dispatches
`sessions rename` to `runSessionsRename` (without dialling — passes
a bogus socket and asserts the resulting error mentions the
`sessions rename:` wrap rather than the `unknown verb` router
error).

```go
// TestRunSessions_RenameDispatch pins AC#4's router wiring: the
// `case "rename":` arm exists and routes to runSessionsRename.
// Verified by passing a deliberately-bogus socket path and
// observing the resulting error path is the dial failure (wrapped
// as "sessions rename:"), not the help-style "unknown verb"
// router error.
func TestRunSessions_RenameDispatch(t *testing.T) {
    t.Setenv("PYRY_NAME", "")
    bogusSock := filepath.Join(t.TempDir(), "no-such.sock")

    err := runSessions([]string{"-pyry-socket", bogusSock, "rename", "abc", "alpha"})
    if err == nil {
        t.Fatal("expected error, got nil")
    }
    msg := err.Error()
    if strings.Contains(msg, "unknown verb") {
        t.Errorf("router did not dispatch rename: %v", err)
    }
    if !strings.Contains(msg, "sessions rename:") {
        t.Errorf("error %q missing %q wrap fragment", msg, "sessions rename:")
    }
}
```

### E2E tests — `internal/e2e/sessions_rename_test.go` (new file)

Build tag `//go:build e2e` (matches `sessions_new_test.go` and
`sessions_rm_test.go`). Reuses
`StartIn` / `Run` / `RunBare` / `newRegistryHome` / `readRegistry` /
`writeSleepClaude` / `waitForBootstrap` / `findSession` /
`mustReadFile` from the existing harness — no new helpers.

Each test follows the same shape: spin up `StartIn` with a
sleep-based `claudeBin`, drive `control.SessionsNew(...)` to mint
a session in the registry, then exercise `pyry sessions rename`.
Post-conditions check `readRegistry` for the expected label
delta. AC items are pinned 1:1 (test name → AC sub-bullet):

1. **`TestSessionsRename_E2E_Success`** — AC#1 happy path. Mint
   a session via `control.SessionsNew(..., "before")`,
   `pyry sessions rename <full-uuid> "after"`, exit 0. Poll
   `readRegistry` (matches the rm tests' fs-visibility pattern,
   2s deadline); assert the entry's `Label` flips from `"before"`
   to `"after"`.

2. **`TestSessionsRename_E2E_EmptyLabelClear`** — AC#1 empty-label
   clear. Mint with label `"to-clear"`,
   `pyry sessions rename <uuid> ""`, exit 0, poll registry,
   assert `Label == ""`. The `Run` invocation passes `""` as a
   distinct argv element: `h.Run(t, "sessions", "rename", id, "")`.

3. **`TestSessionsRename_E2E_UnknownUUID`** — AC#2 typed-error
   mapping. Bring up a daemon, run
   `pyry sessions rename 00000000-0000-4000-8000-000000000000
   anything`. Exit non-zero, stderr contains `no session with id`
   and the offending UUID, registry unchanged. Mirrors
   `TestSessionsRm_E2E_UnknownUUID`.

4. **`TestSessionsRename_E2E_NoDaemon`** — AC#2 dial-failure
   path. `RunBare(t, "sessions", "-pyry-socket="+bogusSock,
   "rename", "<some-uuid>", "alpha")`. Exit non-zero, stderr
   non-empty, no `panic` / `goroutine ` / `runtime/` markers.
   Mirrors `TestSessionsRm_E2E_NoDaemon` byte for byte except
   for the verb-name and arg shape.

5. **`TestSessionsRename_E2E_WrongArity`** — AC#3 wrong-arity
   exit-2 path. Bring up a daemon, run `pyry sessions rename
   <some-uuid>` (only one positional). Exit code **2**, stderr
   contains `expected <id> <new-label>`, registry unchanged.
   Pins AC#3 the same way `TestSessionsRm_E2E_FlagsExclusive`
   pinned rm's exit-2 case.

`go test -race ./...` and `go vet ./...` complete the AC#5
checklist.

### What's out of scope for tests

- **No prefix-resolution test.** AC#4 explicitly says full UUID
  only in this slice. The follow-up ergonomic slice covers prefix.
- **No no-op rename test (newLabel == current label → nil, no
  save).** `Pool.Rename`'s contract pins the no-save behaviour
  (`internal/sessions` tests from #62); the CLI sees the same
  `OK: true` either way and there is no observable difference at
  the CLI layer. Pinning at the source, not duplicating here.
- **No bootstrap-renaming test.** The CLI's only typed-error
  branch is `ErrSessionNotFound`; there is no
  bootstrap-rejection sentinel for rename. A
  successful-bootstrap-rename test would just mirror
  `TestSessionsRename_E2E_Success` against the bootstrap UUID
  — no new code path exercised. Defer until a future ticket
  surfaces a reason to special-case the bootstrap (none
  expected).
- **No wire-bytes-on-the-wire assertion at the CLI layer.**
  #90's tests pin the wire shape (`sessions_rename_test.go` in
  `internal/control` covers omitempty on `NewLabel`, payload
  decoding, etc.). Re-asserting through the CLI adds nothing.

## Open questions

1. **Should the CLI add a `--clear` flag as a more discoverable
   way to clear a label?** AC#1 explicitly says "no separate
   `--clear` flag" — `pyry sessions rename <uuid> ""` is the
   contract. Defer; revisit only on operator feedback.

2. **Should `<id>` accept a UUID prefix today (folding the
   follow-up slice into this one)?** No — AC#4 forbids it and
   the sibling #99 slice's prefix-resolution helper
   (`resolveSessionIDViaList`) is the natural lift point when
   the third caller arrives. Per the project's "Don't design
   for hypothetical future requirements" rule, hold the line.

3. **Should rename success print the renamed session's UUID to
   stdout (mirroring `pyry sessions new`'s UUID print)?** No —
   the operator already typed the UUID; echoing it back adds
   noise. Silent success matches `pyry stop`'s shape and is what
   AC#1 specifies (exit 0 with no stdout requirement). Defer
   unless operator feedback says otherwise.
