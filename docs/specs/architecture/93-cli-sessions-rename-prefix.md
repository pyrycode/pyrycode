# #93 — `pyry sessions rename` UUID-prefix resolution

Phase 1.1c-B2b. Wires the existing `resolveSessionIDViaList`
helper (landed by #99 alongside `pyry sessions rm`) into the
`runSessionsRename` handler (landed by #92). Two-line surgical
insertion at the top of the wire-call path, plus a parallel
ambiguous-prefix branch in the existing error switch.

The novel surface this slice adds is small:

1. One call to the existing `resolveSessionIDViaList` helper
   inside `runSessionsRename`.
2. One new `errors.Is(err, errAmbiguousPrefix)` branch in the
   handler's error mapping (parallel to `runSessionsRm`'s
   identical branch).
3. Two new e2e test functions extending
   `internal/e2e/sessions_rename_test.go`.

**No new helper, no new sentinel, no new exported type, no wire
work.** `resolveSessionIDViaList` and `errAmbiguousPrefix` both
already exist in `cmd/pyry/main.go` because the sibling rm slice
(#99) shipped first. This ticket is the second consumer; per the
ticket body the helper stays in `cmd/pyry` and gets opportunistically
lifted into `internal/sessions` only when the third caller (1.1e
`attach`) arrives.

## Files to read first

- `cmd/pyry/main.go:626-671` — `resolveSessionIDViaList` helper.
  Read end-to-end: input contract (`arg != ""` precondition,
  empty rejected at parse time), exact-match-first resolution
  order, the three return shapes (canonical id + nil,
  `sessions.ErrSessionNotFound`, `errAmbiguousPrefix` wrapping a
  sorted multi-line `<uuid> <label>` body). The helper consumes
  `control.SessionsList` and is the only data path the resolver
  uses — no direct `Pool.List`.
- `cmd/pyry/main.go:581-588` — `errAmbiguousPrefix` sentinel
  declaration. Already used by `runSessionsRm`; this ticket adds
  the second `errors.Is` branch that matches it.
- `cmd/pyry/main.go:690-731` — `runSessionsRm` handler. The
  precedent for the exact insertion shape: parse → resolve →
  switch on resolver errors (ambiguous + not-found) → wire call →
  switch on wire errors (typed sentinels including the race-window
  not-found). Mirror this verbatim for rename, dropping the
  bootstrap-rejection branch (rename has no bootstrap special-case).
- `cmd/pyry/main.go:760-796` — `runSessionsRename` (current full-UUID
  shape from #92). The function this ticket modifies. The doc
  comment block at lines 760-775 needs the "no prefix resolution
  in this slice" line removed and replaced with a one-line note
  that `<id>` is now resolver-fed.
- `cmd/pyry/main.go:21` — top-level reserved-verb comment.
  Unchanged — verb name is the same; no help-text edit.
- `cmd/pyry/main.go:1040` — `printHelp` line. Unchanged for the
  same reason.
- `internal/e2e/sessions_rename_test.go` (whole file, 163 lines)
  — current rename e2e. Two new tests append at end:
  `TestSessionsRename_E2E_Success_Prefix` and
  `TestSessionsRename_E2E_AmbiguousPrefix`.
- `internal/e2e/sessions_rm_test.go:63-96` —
  `TestSessionsRm_E2E_Success_Prefix`. Verbatim template for the
  rename success-prefix test (swap `rm` → `rename` and add the
  new-label positional + post-condition on `entry.Label`).
- `internal/e2e/sessions_rm_test.go:166-237` —
  `TestSessionsRm_E2E_AmbiguousPrefix`. Verbatim template for
  rename's ambiguous-prefix test, including the pigeonhole-bound
  collision-mining loop. The post-condition swaps from "both
  sessions still in registry" to "both sessions' labels unchanged
  in registry" — the resolver bails before any wire mutation,
  so the labels do not flip.
- `cmd/pyry/sessions_test.go:148-167` — `TestRunSessions_RenameDispatch`
  (already present from #92). Unchanged. The new prefix logic
  lives below the dispatch — the dispatch test continues to
  exercise its own assertion (router routes to handler).
- `docs/specs/architecture/99-cli-sessions-rm.md` § "Handler
  (runSessionsRm) — prefix resolution" and § "Error handling".
  The full precedent for resolver-error mapping; rename inherits
  the shape verbatim.
- `docs/specs/architecture/92-cli-sessions-rename.md` § "Open
  questions" #2 — confirms the deferred work this ticket lifts.

## Context

`pyry sessions rename <id> <new-label>` (delivered by #92) accepts
only the canonical UUID for `<id>`. Operators copy-pasting from
`pyry sessions list` typically work with the first 8 hex chars;
forcing them to type the full UUID is friction without payoff.

This slice closes that gap. The underlying resolver
(`resolveSessionIDViaList`) was already built for `pyry sessions rm`
in #99 — same prefix → canonical-UUID translation, same
exact-match-first order (`Pool.ResolveID` mirror), same
`errAmbiguousPrefix` multi-line stderr format. This ticket is
strictly the second consumer of that helper. AC#1 explicitly says
"the resolver enumerates the snapshot returned by the
`sessions.list` client wrapper from #87 and filters by
`strings.HasPrefix(uuid, arg)`" — that's exactly what the existing
helper does.

Three operational shapes the modified handler must satisfy:

1. **Unique prefix → canonical id → wire call.** The resolver
   returns the canonical UUID; that UUID is what flows into
   `control.SessionsRename` (not the operator-supplied prefix).
   The full-UUID form continues to work because exact-match wins
   outright in the resolver.
2. **Ambiguous prefix → bail before the wire.** No
   `control.SessionsRename` call is made. Stderr carries the
   resolver's pre-formatted multi-line list (sorted by id),
   exit 1.
3. **Unknown prefix-or-UUID → bail with the AC-prescribed
   not-found message.** Resolver returns `sessions.ErrSessionNotFound`;
   the handler maps that to the existing `no session with id "<input>"`
   stderr line, exit 1. The operator-typed string is echoed (matches
   #99's race-window comment).

Two not-found code paths exist post-change — the resolver-side
one (prefix doesn't match anything in the snapshot) and the
wire-side one (resolver matched, but the session was removed
between resolver enumeration and rename wire-call). Both
collapse to the same message and exit code, so the handler can
share one stderr-print line. The race window is the same as
#99's; surface the operator's input verbatim in either case.

Scope is **CLI-layer only**. No wire surface, no helper extraction,
no `internal/sessions` work, no documentation churn beyond a
one-line comment edit on `runSessionsRename`.

## Design

### Handler (runSessionsRename) — single insertion + branch

The current handler at `cmd/pyry/main.go:776-796` becomes:

```go
// runSessionsRename implements `pyry sessions rename <id> <new-label>`:
// resolve the (possibly-prefix) <id> via sessions.list, dial the
// daemon's control socket, ask it to update the named session's
// human-friendly label.
//
// Exit codes match the rest of cmd/pyry:
//
//	0 — rename succeeded.
//	1 — runtime error (ambiguous prefix, unknown id, server-side
//	    error, or no-daemon dial failure).
//	2 — usage error (parse failure or wrong arity).
//
// AC-prescribed messages (ambiguous, unknown) are printed to stderr
// without the `pyry:` outer-error prefix; other errors flow through
// `fmt.Errorf("sessions rename: %w", err)`, which main's top-level
// error printer prepends with `pyry: `.
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
            fmt.Fprintln(os.Stderr, err.Error())
            os.Exit(1)
        case errors.Is(err, sessions.ErrSessionNotFound):
            fmt.Fprintf(os.Stderr, "no session with id %q\n", id)
            os.Exit(1)
        }
        return fmt.Errorf("sessions rename: %w", err)
    }

    if err := control.SessionsRename(ctx, socketPath, canonical, newLabel); err != nil {
        if errors.Is(err, sessions.ErrSessionNotFound) {
            // Race window: resolver returned the canonical UUID,
            // then another caller removed it before our wire call
            // landed. Surface the operator's original <id> — the
            // string they typed.
            fmt.Fprintf(os.Stderr, "no session with id %q\n", id)
            os.Exit(1)
        }
        return fmt.Errorf("sessions rename: %w", err)
    }
    return nil
}
```

Diff against the current handler is two surgical edits:

1. **Insert** the `resolveSessionIDViaList` block + ambiguous /
   not-found switch above the wire call. Mirrors `runSessionsRm`
   line-for-line minus the `errCannotRemoveBootstrap` branch.
2. **Substitute** `canonical` for `id` in the `control.SessionsRename`
   call argument. The wire receives the canonical UUID; the operator
   echo continues to use the typed string `id` in stderr messages.

The doc comment block loses the "<id> is forwarded verbatim — no
prefix resolution in this slice" line and the AC-prescribed-messages
sentence picks up "ambiguous, " in front of "unknown".

**Why the same `ctx` for resolve and rename.** Both calls are
sequential and the 30s budget is generous (each is sub-second in
normal operation). Splitting into two contexts adds nothing — if
the resolver burns 25s for some pathological reason, the rename
deserves to be cut short on the same budget rather than getting a
fresh 30s. Same shape `runSessionsRm` uses.

**Why no new sentinel for the race-window unknown-id case.** Both
paths (resolver not-found, wire not-found) collapse to the same
message + exit code. Distinguishing them buys nothing operationally
— the operator's reaction is identical: "the id I typed is no longer
there". Keeping the two `Fprintf` lines verbatim-identical means a
reader sees the relationship at a glance.

**Why echo the operator's typed `id`, not the resolver's canonical.**
On the ambiguous branch there's no canonical to echo (multiple
matches). On the not-found branches there's also no canonical.
On the race-window not-found branch, echoing the canonical would
surprise the operator (they typed `abc1234`, the message says
`abc12345-...`); echoing `id` matches the rm precedent and what
the operator can correlate against their shell history.

### Argument parsing — unchanged

`parseSessionsRenameArgs` is untouched. The arity rule (exactly 2
positionals), the empty-`<new-label>`-is-valid rule, and the
single-sentinel error wrapping all carry over verbatim. Prefix
resolution happens at the handler layer, after parse-success — the
parser just hands the raw `id` string to the resolver.

The parser already rejects empty `<id>` indirectly: `fs.NArg() == 2`
demands two positional tokens; an empty first token is technically
parseable but the operator would have to type `pyry sessions rename
"" "new-label"`. The resolver's contract documents `arg != ""` as a
precondition; if an empty `id` ever reaches it, the resolver enters
the `strings.HasPrefix("...", "")` path and matches **everything**
in the snapshot, producing an ambiguous-prefix error (or a
not-found if the snapshot is unexpectedly empty). Both shapes are
defensible — they surface as a clean exit-1 error rather than a
panic — but the canonical answer is "operators don't type empty
ids," consistent with #99's choice to leave this unguarded at the
parser. Not tightening here.

### Top-level dispatch / verb list / printHelp

All unchanged. The verb name `rename` is the same; the help text
already lists it (added in #92). No edits to `cmd/pyry/main.go:21`
or `:1040`.

### Data flow

```
 Operator                              CLI (this ticket)                                  Daemon
 ────────                              ─────────────────                                  ──────
 pyry sessions rename <prefix> alpha
   │
   ▼
 main → run() → runSessions(args)
   │
   peel global flags
   │
   dispatch on "rename" → runSessionsRename(socketPath, ["<prefix>", "alpha"])
   │
   parseSessionsRenameArgs → ("<prefix>", "alpha", nil)
   │
   resolveSessionIDViaList(ctx, sock, "<prefix>") ───────────────────►
   │     control.SessionsList → snapshot of every session
   │     ◄────────────────────────────────────────────────────────────
   │     exact-match? no. HasPrefix scan → 1 match
   │     return ("<canonical-uuid>", nil)
   │
   control.SessionsRename(ctx, sock, "<canonical-uuid>", "alpha") ───►
   │                                                                   handleSessionsRename
   │                                                                     Pool.Rename(<canonical>, "alpha") → nil
   │     ◄────────────────────────────────────────────────────────────
   │     {ok: true}
   │
   return nil → main → exit 0


 Ambiguous-prefix path:
   │
   resolveSessionIDViaList(ctx, sock, "1") ──────────────────────────►
   │     SessionsList → snapshot
   │     HasPrefix scan → 2+ matches
   │     return ("", errAmbiguousPrefix wrapping "<uuid>  <label>\n<uuid>  <label>")
   │
   runSessionsRename → errors.Is(err, errAmbiguousPrefix) →
                       Fprintln(stderr, err.Error()) →   ← multi-line list, no `pyry:` prefix
                       os.Exit(1)
   ↑
   No control.SessionsRename call made.


 Unknown-prefix path:
   │
   resolveSessionIDViaList → no exact, no HasPrefix match
   │     return ("", sessions.ErrSessionNotFound)
   │
   runSessionsRename → errors.Is(err, sessions.ErrSessionNotFound) →
                       Fprintf(stderr, "no session with id %q\n", id) →
                       os.Exit(1)


 Race-window unknown path:
   │
   resolveSessionIDViaList → returns canonical UUID
   │
   control.SessionsRename(ctx, sock, canonical, label) ──────────────►
   │                                                                   Pool.Rename → ErrSessionNotFound
   │                                                                   (rm landed between resolve and rename)
   │     ◄────────────────────────────────────────────────────────────
   │     ErrCodeSessionNotFound → sessions.ErrSessionNotFound
   │
   runSessionsRename → wire-side errors.Is(err, sessions.ErrSessionNotFound) →
                       Fprintf(stderr, "no session with id %q\n", id) →   ← echoes operator input
                       os.Exit(1)
```

### Concurrency

No new goroutines, mutexes, or channels.

- `runSessionsRename` is sequential: parse → resolve → wire call → return.
- The single 30s `ctx` covers both wire calls (list + rename).
- Resolver and rename are not atomic — the race window between
  them is the source of the wire-side `ErrSessionNotFound`
  branch. Both are bounded by `Pool.mu` on the daemon side; the
  rename either lands against the canonical UUID we resolved or
  hits the not-found sentinel after a concurrent rm. No CLI-side
  coordination needed.
- A concurrent rename for the same id (two operators typing
  `pyry sessions rename` in the same second) is serialised through
  `Pool.mu` on the daemon. Last writer wins; both CLIs see
  `OK: true`. No surprise.

### Error handling

Updated end-to-end catalogue (changes from #92's table marked **NEW**
or **CHANGED**):

| Failure | Source | CLI mapping | Exit | stderr message |
|---|---|---|---|---|
| Wrong arity (≠2 positionals) | `parseSessionsRenameArgs` | `errSessionsRenameUsage` | 2 | `pyry sessions rename: usage: expected <id> <new-label>, got N positional args` |
| `flag.Parse` failure | `parseSessionsRenameArgs` | `errSessionsRenameUsage` | 2 | `pyry sessions rename: usage: <flag-package message>` |
| Daemon not running (resolver step) | `control.SessionsList` dial fail | bubble through `fmt.Errorf` wrap | 1 | `pyry: sessions rename: ... dial socket: ...` |
| **NEW** Ambiguous prefix | `resolveSessionIDViaList` returns `errAmbiguousPrefix` | sentinel match | 1 | `ambiguous session id prefix:\n<uuid>  <label>\n<uuid>  <label>...` |
| **CHANGED** Unknown prefix-or-UUID (resolver) | `resolveSessionIDViaList` returns `sessions.ErrSessionNotFound` | sentinel match | 1 | `no session with id "<original-input>"` |
| **CHANGED** Unknown UUID (race: resolved then removed) | `control.SessionsRename` returns `sessions.ErrSessionNotFound` | sentinel match | 1 | `no session with id "<original-input>"` |
| Daemon not running (wire-rename step) | `control.SessionsRename` dial fail | bubble through `fmt.Errorf` wrap | 1 | `pyry: sessions rename: ... dial socket: ...` |
| Other server error (registry persist failure, etc.) | `control.SessionsRename` returns `errors.New(resp.Error)` | bubble through `fmt.Errorf` wrap | 1 | `pyry: sessions rename: <verbatim server message>` |

Note the resolver-step daemon-down path is identical to the
existing wire-rename-step path: both are dial failures that
bubble through `fmt.Errorf("sessions rename: %w", err)`. The
single-`ctx` design means timing differences are invisible to the
operator.

The "no stack traces on user-facing errors" property is preserved
(no `panic`, no `runtime.Stack`, no goroutine dumps on any
documented failure mode).

## Testing strategy

Append two e2e tests to the existing
`internal/e2e/sessions_rename_test.go`. No unit-test edits — the
parser is untouched (`TestParseSessionsRenameArgs` still passes
verbatim) and `TestRunSessions_RenameDispatch` still pins the
router wiring (the resolver step happens after dispatch, doesn't
change the dispatch-test's "did the case fire?" assertion).

### E2E tests — `internal/e2e/sessions_rename_test.go` (append)

Build tag `//go:build e2e` (already present at top of file).
Reuses `StartIn` / `Run` / `newRegistryHome` / `readRegistry` /
`writeSleepClaude` / `findSession` / `mustReadFile` from the
existing harness — no new helpers.

| Test | What it asserts | Mirrors |
|---|---|---|
| **NEW** `TestSessionsRename_E2E_Success_Prefix` | Mint `before`-labelled session, run `pyry sessions rename <first-8-chars> after`, exit 0, registry entry's label flips to `after`. Pins AC#1 prefix branch. | `TestSessionsRm_E2E_Success_Prefix` |
| **NEW** `TestSessionsRename_E2E_AmbiguousPrefix` | Mint sessions until two share the same first hex char (pigeonhole bound: ≤17 mints), run `pyry sessions rename <shared-char> any-label`, exit non-zero, stderr lists both matched ids and labels, **both sessions' on-disk labels unchanged**. Pins AC#2. | `TestSessionsRm_E2E_AmbiguousPrefix` |

Existing tests cover the rest of the AC:

- AC#1 full-UUID continues to work → already covered by
  `TestSessionsRename_E2E_Success` (uses the full canonical
  UUID; the resolver's exact-match-first branch returns it
  outright).
- AC#1 empty-label clear (with full UUID) → already covered by
  `TestSessionsRename_E2E_EmptyLabelClear`. Optionally widen to
  use a prefix as well; not required by AC.
- AC#3 unknown prefix-or-UUID → already covered by
  `TestSessionsRename_E2E_UnknownUUID`. The current
  `00000000-...` UUID exercises the resolver's
  `sessions.ErrSessionNotFound` path now (no exact match, no
  prefix match in a freshly-bootstrapped registry); same stderr
  fragment, same exit code. The test name retains `_UnknownUUID`
  because the input is a syntactically-valid UUID; the assertion
  is unchanged.
- AC#4 dial-failure / wrong-arity / `go test -race` / `go vet` →
  already covered by `TestSessionsRename_E2E_NoDaemon` /
  `TestSessionsRename_E2E_WrongArity` and the project's CI
  config.

#### `TestSessionsRename_E2E_Success_Prefix` shape

```go
func TestSessionsRename_E2E_Success_Prefix(t *testing.T) {
    home, regPath := newRegistryHome(t)
    claudeBin := writeSleepClaude(t, home)
    h := StartIn(t, home, "-pyry-claude="+claudeBin)

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    id, err := control.SessionsNew(ctx, h.SocketPath, "before")
    if err != nil {
        t.Fatalf("sessions.new: %v", err)
    }
    prefix := id[:8]

    r := h.Run(t, "sessions", "rename", prefix, "after")
    if r.ExitCode != 0 {
        t.Fatalf("pyry sessions rename %q after exit=%d\nstdout:\n%s\nstderr:\n%s",
            prefix, r.ExitCode, r.Stdout, r.Stderr)
    }

    deadline := time.Now().Add(2 * time.Second)
    for time.Now().Before(deadline) {
        reg := readRegistry(t, regPath)
        if entry, ok := findSession(reg, id); ok && entry.Label == "after" {
            return
        }
        time.Sleep(50 * time.Millisecond)
    }
    t.Fatalf("session %s label did not become %q within 2s\nfile:\n%s",
        id, "after", mustReadFile(t, regPath))
}
```

Direct adaptation of `TestSessionsRename_E2E_Success`: insert
the `prefix := id[:8]` line and substitute it in the `Run` call.

#### `TestSessionsRename_E2E_AmbiguousPrefix` shape

Mirrors `TestSessionsRm_E2E_AmbiguousPrefix` line-for-line with
two changes:

1. The `Run` invocation gains a new-label positional:
   `h.Run(t, "sessions", "rename", prefix, "should-not-apply")`.
2. The post-condition shifts from "both sessions still in
   registry" (rm checks **presence**) to "both sessions' labels
   unchanged" (rename's resolver bails before any wire call, so
   the labels never flip). Use `findSession` + `entry.Label ==
   m.label` for each collided entry. Both sessions remain present
   too (presence assertion is free; keep it for symmetry with rm).

Sketch:

```go
func TestSessionsRename_E2E_AmbiguousPrefix(t *testing.T) {
    home, regPath := newRegistryHome(t)
    claudeBin := writeSleepClaude(t, home)
    h := StartIn(t, home, "-pyry-claude="+claudeBin)

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    const maxMints = 17 // pigeonhole over 16 hex first-char bins

    type minted struct {
        id    string
        label string
    }
    byFirstChar := make(map[byte]minted, 16)
    prefix := ""
    var collided [2]minted
    for i := 0; i < maxMints && prefix == ""; i++ {
        label := fmt.Sprintf("amb-%d", i)
        id, err := control.SessionsNew(ctx, h.SocketPath, label)
        if err != nil {
            t.Fatalf("sessions.new amb-%d: %v", i, err)
        }
        first := id[0]
        if other, ok := byFirstChar[first]; ok {
            prefix = string(first)
            collided = [2]minted{other, {id: id, label: label}}
            break
        }
        byFirstChar[first] = minted{id: id, label: label}
    }
    if prefix == "" {
        t.Fatalf("no first-char collision after %d mints — UUID generation broken?", maxMints)
    }

    r := h.Run(t, "sessions", "rename", prefix, "should-not-apply")
    if r.ExitCode == 0 {
        t.Fatalf("pyry sessions rename %q unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s",
            prefix, r.Stdout, r.Stderr)
    }
    for _, m := range collided {
        if !bytes.Contains(r.Stderr, []byte(m.id)) {
            t.Errorf("stderr missing matched id %q:\n%s", m.id, r.Stderr)
        }
        if !bytes.Contains(r.Stderr, []byte(m.label)) {
            t.Errorf("stderr missing matched label %q:\n%s", m.label, r.Stderr)
        }
    }

    reg := readRegistry(t, regPath)
    for _, m := range collided {
        entry, ok := findSession(reg, m.id)
        if !ok {
            t.Errorf("session %s missing after ambiguous rename\nfile:\n%s",
                m.id, mustReadFile(t, regPath))
            continue
        }
        if entry.Label != m.label {
            t.Errorf("session %s label = %q, want unchanged %q",
                m.id, entry.Label, m.label)
        }
    }
}
```

The "label unchanged" assertion is the load-bearing AC#2 check
("No call to `sessions.rename` is made"). If a future regression
moves the resolver below the wire call by mistake, the labels
would flip to `should-not-apply` and this assertion would fail.

The new-label positional is intentionally `"should-not-apply"`
rather than something like `"x"` — a reader scanning failures
sees the label in stderr and immediately knows the assertion
was about the no-mutation invariant.

### What's out of scope for tests

- **No race-window unit test for the wire-side `ErrSessionNotFound`
  branch.** The race is a real code path, but constructing it
  reliably in e2e (resolve → external rm → rename, all within the
  same `ctx`) requires either harness goroutine plumbing (out of
  scope) or stat-busy timing tricks (flaky). The branch is a
  one-line mirror of the rm precedent and the typed-sentinel wire
  mapping is exercised end-to-end by `TestSessionsRename_E2E_UnknownUUID`
  (which now traverses the same `errors.Is` arm). Pinning the
  race specifically would buy little.
- **No prefix-resolution unit test in `cmd/pyry/sessions_test.go`.**
  The resolver itself is unit-tested in #99's coverage; this slice
  only adds a call site. The dispatch test (`TestRunSessions_RenameDispatch`)
  still pins the router wiring — that's the unit-level
  responsibility of `cmd/pyry/sessions_test.go`. Adding a
  resolver-call-site assertion here would duplicate the e2e
  coverage that already exercises the full path.
- **No empty-prefix-with-prefix-resolution test.** Empty `<id>`
  is rejected at parse time (arity check requires 2 positional
  tokens; `pyry sessions rename "" alpha` parses as
  `("", "alpha")` and would reach the resolver, where
  `strings.HasPrefix(_, "")` matches everything and the operator
  sees an ambiguous-prefix error against every session in the
  pool). That's a defensible-but-pointless input; testing it
  would test what is essentially "what does the resolver do when
  given `""`" — already covered by #99's resolver tests.
- **No "lifting `resolveSessionIDViaList` into `internal/sessions`"
  work.** Out of scope per the ticket body's "do that
  opportunistically when the third caller arrives" rule. This is
  caller #2; #99 is caller #1; 1.1e `attach` will be #3.

`go test -race ./...` and `go vet ./...` complete the AC#4
checklist.

## Open questions

1. **Should the success-prefix test also check that the resolver
   returned the canonical UUID (i.e. that the wire-side rename
   used the canonical, not the prefix)?** Implicitly yes — if
   the wire call went out with `prefix` instead of the canonical,
   the daemon would return `ErrSessionNotFound` and the test
   would see exit 1 + the not-found stderr fragment. So the
   exit-0 + label-flip assertion already pins the canonical-resolution
   contract end-to-end. No separate assertion needed.

2. **Should the ambiguous-prefix test assert `_, _ := control.SessionsList`
   was called exactly once?** No — that's an implementation-detail
   assertion below the e2e abstraction. The behaviour-level claim
   ("no labels flip on ambiguous prefix") covers what AC#2
   actually requires.

3. **Should we deprecate the "<id> is forwarded verbatim" comment
   in #92's spec?** Already done in the doc-comment edit above.
   No standalone follow-up needed.
