# #463 — `cmd/pyry`: `pyry rekey <conn_id>` operator verb

Slice B2 of the split of #460 (itself split from #451). Slice A (#459, merged) shipped the wire contract: `VerbRekey`, `control.Rekey(ctx, socketPath, connID)` client helper, `control.ErrConnNotFound` sentinel, the server-side dispatcher (`handleRekey`), and `Server.SetRekeyer`. Sibling B1 (#462, merged) ships `(*relay.V2SessionManager).Rekey` and the `emitRekeyRequest(reason)` refactor.

**This slice is the operator-facing CLI surface only.** A new top-level verb `pyry rekey <conn_id>` that dials the control socket and surfaces the result as a one-line stderr message + exit code. No relay changes. No `ctrl.SetRekeyer(v2mgr)` wire-up — the daemon does not yet construct a `*V2SessionManager` in `cmd/pyry/main.go`, so production-path `pyry rekey` returns the slice A guard `rekey: no rekeyer configured` until the missing daemon-wire-up ticket lands. Same "shipped ahead of cutover" precedent as `pair preflight`.

## Files to read first

Production code:

- `cmd/pyry/main.go:163-194` — top-level `run()` dispatch switch; `case "rekey":` is added here next to the existing case arms (`pair`, `update`, `agent-run`).
- `cmd/pyry/main.go:1279-1351` — `printHelp` body; one new line is added under the existing verb list (mirrors `pair revoke` placement).
- `cmd/pyry/pair.go:312-372` — `pairRevokeArgs` / `parsePairRevokeArgs` / `runPairRevoke`. **Canonical shape to mirror verbatim.** Sole-positional FlagSet, `fmt.Fprintln(os.Stderr, "pyry rekey:", err)` + usage line + `os.Exit(2)` for parse failures; `fmt.Fprintf(os.Stderr, "pyry rekey: ...\n", ...)` + `os.Exit(1)` for specific runtime errors; `return fmt.Errorf("rekey: %w", err)` for I/O / transport errors that flow through main's `pyry: ` prefix.
- `cmd/pyry/pair.go:396-405` — `preflightVerdict`: the precedent for "pure (exitCode, stderrLine) helper" testable seam. The new `rekeyVerdict` follows this idiom verbatim so the unit tests can drive the formatter without invoking `os.Exit`.
- `cmd/pyry/main.go:284-306` — `splitClientFlags`: pre-extractor for `-pyry-name` / `-pyry-socket` tokens that may appear before the positional. **Not used directly** — `parseClientFlags` is the right wrapper for `pyry rekey`; see § Parser below.
- `cmd/pyry/main.go:540-551` — `parseClientFlags`: the two-step "peel `-pyry-*` then verb-FlagSet" idiom every control-verb runner uses. `runRekey` calls this first to get `(socketPath, rest)`.
- `cmd/pyry/main.go:911-952` — `runSessionsRm`: the closest analog for "dial via client helper, branch on typed sentinel vs other error". The `errors.Is(err, sessions.ErrSessionNotFound)` → direct `fmt.Fprintf + os.Exit(1)` arm is the exact shape `runRekey`'s `errors.Is(err, control.ErrConnNotFound)` arm follows.
- `internal/control/client.go:260-278` — `control.Rekey`: the helper the verb invokes. Returns `nil` on success, `ErrConnNotFound` (reconstructed) on the typed-not-found path, `errors.New(resp.Error)` on every other server-side reject (including the slice A guards `rekey: no rekeyer configured` and `rekey: missing connID`), or a transport-shaped error (`dial`, `send request`, `decode`) on socket failures.
- `internal/control/server.go:27-35` — `control.ErrConnNotFound` sentinel. The verb's only branch-target on the typed path.

Tests / fixtures:

- `cmd/pyry/sessions_test.go:73-93` — `TestRunSessions_RmDispatch` is the canonical "bogus-socket → returned wrapped error contains verb prefix" pattern. **The transport-error test (AC4 bullet 1) mirrors this verbatim** with `runRekey` substituted for `runSessions`. Note: the path under test returns `fmt.Errorf("rekey: %w", err)` from `runRekey` itself; main's top-level printer prepends `pyry: ` in production but the test asserts on the returned `err.Error()` directly so it never reaches that printer.
- `cmd/pyry/pair_test.go:307-347` — `TestParsePairRevokeArgs`: table-driven parser-shape test. The new `TestParseRekeyArgs` follows this table format verbatim.
- `cmd/pyry/pair_test.go:412-456` — `TestRunPairRevoke_SaveFailure`: the wrapped-error-return path. Not directly mirrored (no on-disk state in `pyry rekey`), but cited for the "I/O error → returned wrapped error" idiom that the transport-error path inherits.
- `internal/control/rekey_test.go:55-85` — `startServerWithRekeyer`: the in-process-server fixture pattern. The cmd/pyry test file can NOT import this helper (it lives in package `control`'s `_test.go`), so § Test fixtures below specifies a minimal local re-implementation of the same shape.
- `internal/control/rekey_test.go:17-53` — `fakeRekeyer`: the trivial Rekeyer stub. The local re-implementation in `cmd/pyry/rekey_test.go` is structurally identical but defined in package `main` so it is in scope for the tests there.

Convention references:

- `docs/PROJECT-MEMORY.md:20` — "Refusal-to-wire-code mapping is the consumer's job." `pyry rekey` is the topmost consumer: it maps `control.ErrConnNotFound` → operator-readable `pyry rekey: conn_id "<value>" not found`, and everything else → `pyry rekey: <verbatim err.Error()>`.
- `docs/specs/architecture/459-control-rekey-wire.md` — slice A's spec; consult § "Client-side helper" for the contract `control.Rekey` exposes that this slice consumes.

## Context

The mobile protocol v2 (`docs/protocol-mobile.md` § Re-key, line 234) names `payload.reason = "manual"` as *"operator-triggered via `pyry rekey <conn_id>`"*. The verb is part of the v2 contract — slice A shipped the wire, slice B1 shipped the manager-side trigger, this slice ships the operator surface that ties them together.

End-to-end, when both this slice and the (separate, missing) daemon-wire-up ticket land:

```
operator shell                                          daemon (cmd/pyry)
─────────────                                          ─────────────────
pyry rekey conn-abc                                    control.Server
  → control.Rekey(ctx, sock, "conn-abc")                 → handle()
  → dial sock                                              → case VerbRekey: handleRekey
  → encode Request{Verb:"rekey",Rekey:{ConnID:…}}            → s.rekeyer.Rekey(ctx, "conn-abc")
                                                               → V2SessionManager.Rekey
                                                                 → enqueue on manualRekey ch
                                                                 → Run loop emits rekey_request
                                                                 → returns nil
                                                             → Response{OK: true}
  → decode → nil
  → exit 0
```

Until the daemon-wire-up ticket lands, `s.rekeyer` is `nil` in production, `handleRekey` replies `"rekey: no rekeyer configured"`, and the verb surfaces `pyry rekey: rekey: no rekeyer configured` to the operator. This is the documented production state.

The verb is **operator-facing only** — never exposed over the relay wire. Authentication is filesystem perms on the `0600`-mode control socket (slice A's security review covers this).

## Design

### 1. Verb dispatch (`cmd/pyry/main.go`)

One new case arm in the `run()` switch, alongside the existing top-level verbs:

```go
case "rekey":
    return runRekey(os.Args[2:])
```

Placement: between `"pair"` and `"install-service"` is the natural slot (operator-facing, mobile-protocol-adjacent). The exact line is the architect's call; the developer should match the surrounding alphabetisation conventions of the file.

One new line in `printHelp` under the existing verb list. Suggested text:

```
  pyry rekey <conn_id> [flags]                   trigger an immediate Noise re-key
                                                  on the named v2 conn (operator
                                                  rotation; control-socket only)
```

No new `pyryFlag*` map entries — `pyry rekey` is a top-level verb, not a `-pyry-*` flag, so the existing `splitArgs` / `splitClientFlags` routing already handles it.

### 2. New file: `cmd/pyry/rekey.go`

#### Argument shape

```go
type rekeyArgs struct {
    connID string // sole positional
}
```

No flags beyond the shared `-pyry-name` / `-pyry-socket` (handled by `parseClientFlags` upstream).

#### Parser

`parseRekeyArgs(args []string) (rekeyArgs, error)` — extracted so the parsing rules are unit-testable without dialling the control socket. **Mirrors `parsePairRevokeArgs` verbatim**:

- `flag.NewFlagSet("pyry rekey", flag.ContinueOnError)`, `SetOutput(io.Discard)`.
- No flags declared on this FlagSet (the shared client flags are already peeled off upstream by `parseClientFlags`).
- Arity rule:
  - 0 positionals → `errors.New("missing conn_id")`.
  - 1 positional → return `rekeyArgs{connID: fs.Arg(0)}`.
  - ≥2 positionals → `fmt.Errorf("unexpected positional %q", fs.Arg(1))`.
- `fs.Parse` error → propagate verbatim (caller wraps).

#### Verdict helper (testable seam)

```go
// rekeyVerdict returns (exitCode, stderrLine) for a control.Rekey result.
// exitCode == 0 means success; stderrLine is "". exitCode == 1 means
// failure; stderrLine is the one-line operator-readable message to print
// to stderr before os.Exit(1). Pure: deterministic on (connID, err).
//
// Mirrors preflightVerdict in cmd/pyry/pair.go — the same "extract the
// formatter so the unit test never has to intercept os.Exit" idiom.
func rekeyVerdict(connID string, err error) (exitCode int, stderrLine string)
```

Branches (one row per case):

| `err`                                              | `exitCode` | `stderrLine`                                                 |
| -------------------------------------------------- | ---------- | ------------------------------------------------------------ |
| `nil`                                              | `0`        | `""`                                                         |
| `errors.Is(err, control.ErrConnNotFound)` is true  | `1`        | `fmt.Sprintf("pyry rekey: conn_id %q not found", connID)`    |
| any other non-nil error                            | `1`        | `fmt.Sprintf("pyry rekey: %s", err.Error())`                 |

**Quoting note.** `%q` produces Go-syntax quoting (`"conn-abc"`, escaping any embedded quotes/backslashes/control bytes). This matches the AC text `conn_id "<value>" not found` and defends against operator-supplied conn-id strings with unusual content.

#### Runner

`runRekey(args []string) error` — composes the pieces above. **Mirrors `runPairRevoke` and `runSessionsRm`'s shapes** for control-socket verbs with a typed-sentinel branch.

Behaviour (one row per branch; not function-body pseudocode):

| Step                                                              | On success            | On failure                                                                                                                                                                                                                                                                                                                                                                                              |
| ----------------------------------------------------------------- | --------------------- | --- |
| `parseClientFlags("pyry rekey", args)` → `(socketPath, rest)`     | continue              | propagate parse error to caller (matches every other client-verb runner; main wraps with `pyry: `). |
| `parseRekeyArgs(rest)` → `parsed`                                 | continue              | `fmt.Fprintln(os.Stderr, "pyry rekey:", err)` + `fmt.Fprintln(os.Stderr, "usage: pyry rekey [-pyry-name=<instance>] [-pyry-socket=<path>] <conn_id>")` + `os.Exit(2)`. |
| `control.Rekey(ctx, socketPath, parsed.connID)`                   | return `nil`          | feed into `rekeyVerdict(parsed.connID, err)`; if `exitCode != 0`, `fmt.Fprintln(os.Stderr, stderrLine)` + `os.Exit(exitCode)`. *Special case:* the developer may choose to short-circuit transport-shaped errors by returning `fmt.Errorf("rekey: %w", err)` instead of os.Exit-ing — see § "Transport error path" below; both shapes satisfy AC4 bullet 1. |

Context timeout: `context.WithTimeout(context.Background(), 30*time.Second)` — matches `runSessionsRm`'s budget. The rekey trigger itself returns once the manager has enqueued, so 30s is a generous ceiling on the dial + write + decode round trip.

#### Transport error path

The AC's bullet 1 ("verb invoked against a bogus socket path → verb exits non-zero with a `pyry rekey: ` prefix on stderr") admits two implementation choices:

- **Option A — `os.Exit(1)` via `rekeyVerdict`.** Transport errors flow through the same branch as other non-typed errors; the verdict helper formats `pyry rekey: dial unix /no/such.sock: ...` and the runner prints + os.Exit-s. Stderr text: `pyry rekey: <transport err>`. Tested via subprocess (the existing `TestHelperProcess` pattern in `cmd/pyry/agent_run_test.go` is the precedent), OR by calling `rekeyVerdict(connID, errFromBogusSocket)` directly.
- **Option B — `return fmt.Errorf("rekey: %w", err)` (no os.Exit).** Transport errors return a wrapped error from `runRekey`; main's top-level printer produces `pyry: rekey: <transport err>`. **This is the shape `runSessionsRm` uses for transport-shaped errors**, and the shape `TestRunSessions_RmDispatch` (the AC-cited test pattern) verifies via `err.Error()` substring match.

**Recommendation: Option B.** It avoids the subprocess-test scaffolding for AC4 bullet 1, mirrors `runSessionsRm`'s existing shape directly, and the resulting stderr (`pyry: rekey: <err>`) still satisfies "verb exits non-zero with a `pyry rekey: ` substring prefix, not a `%v` dump" — the AC wording does not require the verb's prefix to *bypass* main's prefix on this path. The double-`rekey:` shape that *would* matter for ErrConnNotFound / no-rekeyer-reject (where the inner error's message ALSO starts with `rekey:`, producing `pyry: rekey: rekey: <…>`) is exactly why those paths use Option A's direct-print + os.Exit instead.

The decision matrix:

| Error class                                  | Runner action                                | Final stderr                                       |
| -------------------------------------------- | -------------------------------------------- | -------------------------------------------------- |
| Parse error                                  | `Fprintln` + usage + `os.Exit(2)`            | `pyry rekey: <parse err>` + usage line             |
| `errors.Is(err, control.ErrConnNotFound)`    | `Fprintln(stderrLine)` + `os.Exit(1)`        | `pyry rekey: conn_id "<value>" not found`          |
| Other server-side reject (no rekeyer, missing connID) | `Fprintln(stderrLine)` + `os.Exit(1)`        | `pyry rekey: rekey: no rekeyer configured` (or `pyry rekey: rekey: missing connID`) |
| Transport error (dial / encode / decode)     | `return fmt.Errorf("rekey: %w", err)`        | `pyry: rekey: <transport err>` (via main's prefix) |

The double-`rekey:` for the no-rekeyer-reject row is exactly what the AC pins: *"stderr matches `pyry rekey: rekey: no rekeyer configured` (the helper-returned message surfaced verbatim)"*.

### 3. Files-touched summary

| File                                  | Change                                                                                          |
| ------------------------------------- | ----------------------------------------------------------------------------------------------- |
| `cmd/pyry/main.go`                    | One `case "rekey":` arm in `run()`, one line in `printHelp`. ~3 LOC.                            |
| `cmd/pyry/rekey.go` (NEW)             | `rekeyArgs`, `parseRekeyArgs`, `rekeyVerdict`, `runRekey`. ~50-70 LOC.                          |
| `cmd/pyry/rekey_test.go` (NEW)        | Parser table-test, verdict table-test, three AC-mandated scenario tests + local test fixtures. ~120-160 LOC. |

**Self-check (architect post-spec gate):** Production source files prescribed = 2 (`cmd/pyry/main.go` modified, `cmd/pyry/rekey.go` created). Test files excluded per the rule. 2 < 5 — within the `size:s` boundary.

## Concurrency model

No new goroutines. The verb runs one-shot in the foreground operator shell: parse args → dial → encode request → decode response → exit. The control-socket round trip is sequential; the 30s context bounds the entire operation.

The asynchronous parts of the rekey (the actual Noise re-handshake on the responder side) run inside the daemon's `V2SessionManager` loop and are entirely out of scope for this slice — `pyry rekey` exits as soon as the manager acknowledges enqueue, NOT when the handshake completes. Slice B1's contract guarantees the manager returns synchronously once the request is on the manual-rekey channel.

## Error handling

Every error path produces a one-line stderr message starting with the verb-specific prefix `pyry rekey:` (or `pyry: rekey:` via main's outer prefix on the transport-error return path) — never a raw `%v` dump. See the decision matrix in § "Transport error path" above for the full mapping.

The runner does NOT log or capture the error beyond writing the stderr line; there is no on-disk audit trail of attempts. This matches every other control-socket verb's posture (stop, status, sessions rm, pair revoke). Audit logging belongs on the daemon side, where slice B1 already records the manual-rekey emit via the existing `rekeyTrigger` slog instrumentation.

## Testing strategy

New file: `cmd/pyry/rekey_test.go`, package `main`. Stdlib only, table-driven where useful.

### Test fixtures

Local to `cmd/pyry/rekey_test.go` (the in-package `internal/control` test helpers — `fakeRekeyer`, `fakeResolver`, `startServerWithRekeyer`, `shortTempDir` — are package-private to `control` and cannot be imported across package boundaries):

- **`fakeRekeyer`** — minimal `control.Rekeyer` stub. Single `returnErr` field; `Rekey(ctx, connID) error` returns it. Mutex/recording optional (the cmd/pyry tests do not need to inspect call shapes — the wire-shape pin lives in `internal/control/rekey_test.go`).
- **`rekeyTestResolver`** — minimal `control.SessionResolver` stub. `NewServer` panics on a nil resolver; the stub satisfies the type but its `Lookup` / `ResolveID` return `errors.New("not used")` because `VerbRekey` never touches the resolver path.
- **`startControlServerWithRekeyer(t *testing.T, r control.Rekeyer) (sockPath string, stop func())`** — local re-implementation of the in-package helper. Picks a `t.TempDir()`-rooted socket path, constructs the server via `control.NewServer(sock, rekeyTestResolver{}, nil, nil, nil, nil)`, calls `srv.SetRekeyer(r)`, calls `srv.Listen()`, spawns `srv.Serve(ctx)` on a goroutine, returns the socket path + a stop closure that cancels and joins with a 2s timeout. ~25 LOC.

### Test cases

Each is one top-level `Test*` (or a `t.Run` within a table-driven parent). All run with `t.Parallel()` unless they touch process-global env (`t.Setenv("PYRY_NAME", "")`, which serialises).

1. **`TestParseRekeyArgs`** — table-driven parser-shape test mirroring `TestParsePairRevokeArgs`. Cases: happy path (`["conn-abc"]` → `connID: "conn-abc"`), missing positional → `"missing conn_id"`, extra positional → `"unexpected positional"`, unknown flag → `"flag provided but not defined"`. No flag declarations on the FlagSet means any `--foo` is unknown.

2. **`TestRekeyVerdict`** — table-driven verdict formatter test. Cases:
   - `(connID="conn-abc", err=nil)` → `(0, "")`.
   - `(connID="conn-abc", err=control.ErrConnNotFound)` → `(1, "pyry rekey: conn_id \"conn-abc\" not found")`.
   - `(connID="conn-abc", err=fmt.Errorf("wrapped: %w", control.ErrConnNotFound))` → `(1, "pyry rekey: conn_id \"conn-abc\" not found")`. **Pins `errors.Is`, not `==`, on the wrap path.**
   - `(connID="conn-xyz", err=errors.New("rekey: no rekeyer configured"))` → `(1, "pyry rekey: rekey: no rekeyer configured")`. **Pins the double-`rekey:` shape from AC4 bullet 3.**
   - `(connID="conn-xyz", err=errors.New("dial unix /tmp/x.sock: connect: connection refused"))` → `(1, "pyry rekey: dial unix /tmp/x.sock: connect: connection refused")`. Transport-shaped untyped error.
   - Edge case: connID with embedded quote (`conn"id`) → `(1, "pyry rekey: conn_id \"conn\\\"id\" not found")`. Pins `%q` escaping.

3. **`TestRunRekey_BogusSocket_ReturnsWrappedError`** — AC4 bullet 1. **Mirrors `TestRunSessions_RmDispatch` at `cmd/pyry/sessions_test.go:73-93` verbatim**:
   ```
   t.Setenv("PYRY_NAME", "")
   bogusSock := filepath.Join(t.TempDir(), "no-such.sock")
   err := runRekey([]string{"-pyry-socket", bogusSock, "some-conn-id"})
   ```
   Assert: `err != nil`, `err.Error()` contains `"rekey:"`, `err.Error()` does NOT contain raw `"%!"` (no `%v`-formatting accident), `err.Error()` does NOT contain the verbatim string `"pyry rekey:"` (the verb prefix is added by main's outer printer in production, not by the runner's returned error).

4. **`TestRunRekey_UnknownConnID_ExitsOnePrintsTypedMessage`** — AC4 bullet 2. Stand up an in-process `control.Server` via `startControlServerWithRekeyer` with `&fakeRekeyer{returnErr: control.ErrConnNotFound}`. Call `control.Rekey(ctx, sock, "missing-conn")` directly — assert the returned error satisfies `errors.Is(err, control.ErrConnNotFound)`. Feed the result into `rekeyVerdict("missing-conn", err)`. Assert `(exitCode, stderrLine) == (1, "pyry rekey: conn_id \"missing-conn\" not found")`.
   - **Why not call `runRekey` directly?** Because the typed-not-found path ends in `os.Exit(1)`, which kills the test process. The verdict helper is the unit-testable seam. The wire round-trip (payload encoding, `Response.ErrorCode = ErrCodeConnNotFound`, client-side sentinel reconstruction) IS exercised — `control.Rekey` is the real client helper, the in-process `control.Server` is the real server, and the error fed into `rekeyVerdict` is the real reconstructed sentinel.

5. **`TestRunRekey_NoRekeyerConfigured_ExitsOnePrintsVerbatimReject`** — AC4 bullet 3. Stand up the in-process `control.Server` BUT do NOT call `SetRekeyer` (the test helper above always sets one; this test uses an inlined variant that skips `srv.SetRekeyer(r)`, OR passes `nil` to it — both clear the field). Call `control.Rekey(ctx, sock, "any-conn")` directly. Assert the returned error has `err.Error() == "rekey: no rekeyer configured"` and `errors.Is(err, control.ErrConnNotFound)` is false. Feed into `rekeyVerdict("any-conn", err)`. Assert `(exitCode, stderrLine) == (1, "pyry rekey: rekey: no rekeyer configured")`.

The three scenario tests together pin:
- AC4 bullet 1: transport failure surfaces as a returned wrapped error with the `rekey:` substring (test 3).
- AC4 bullet 2: unknown conn-id round-trips as `control.ErrConnNotFound` and is formatted as `pyry rekey: conn_id "<value>" not found` (test 4 + the corresponding row in `TestRekeyVerdict`).
- AC4 bullet 3: the no-rekeyer guard surfaces verbatim, with the double-`rekey:` shape (test 5 + the corresponding row in `TestRekeyVerdict`).

### What is intentionally NOT tested here

- **The wire shape (`"verb":"rekey"`, `"rekey":{"connID":"..."}` envelope).** Pinned by `internal/control/rekey_test.go:TestRekey_PassesConnIDOnWire`. Re-asserting in cmd/pyry would be redundant.
- **`Response.ErrorCode` → sentinel reconstruction.** Pinned by `internal/control/rekey_test.go:TestServer_Rekey_ErrConnNotFound` + `_Wrapped`. Re-asserting in cmd/pyry would be redundant.
- **`os.Exit(1)` actually firing.** Verified by inspection — the verdict-helper seam makes the formatting + exit code unit-testable without intercepting `os.Exit`. The full os.Exit path would require a `TestHelperProcess`-style subprocess test (e.g. `cmd/pyry/agent_run_test.go`'s pattern), which adds ~40 LOC of harness for one assertion. Out of scope; if the dispatch ever regresses, the `runRekey` body is < 30 LOC and reviewing the diff is faster than a subprocess assertion.
- **`pyry rekey --help` / usage line text.** No AC bullet pins the exact usage string; matching `pair revoke`'s shape is sufficient.

## Open questions

- **Verb placement in `run()`'s switch.** The `pair`–`install-service` corridor is the natural slot; the developer chooses the exact line. Cosmetic, not load-bearing.
- **Help-line wording.** The draft above is a starting point; the developer may tighten or match other lines' phrasing more closely. The AC has no specific wording requirement.

## Out of scope

- `(*V2SessionManager).Rekey` and `emitRekeyRequest(reason)` (shipped in #462).
- The 1-hour scheduled rekey timer + emit path (shipped in #450).
- The responder-side handshake re-run + `CipherState` swap (separate slice).
- `pyry rekey --all`, confirmation prompts, dry-run flags (explicit non-goal per the ticket's Out of scope).
- Production wire-up of `*V2SessionManager` in `cmd/pyry/main.go` (`ctrl.SetRekeyer(v2mgr)` registration) — separate missing ticket; this slice ships ahead of cutover, matching the `pair preflight` precedent.

## Security review

**Verdict:** PASS

**Mindset shift performed.** Re-read the spec as an attacker who controls (a) the operator-supplied `<conn_id>` argument, (b) the local shell environment, and (c) any code that can race the operator on the control socket. The verb's surface is the argument string + the socket round trip; that is the threat surface this review walks.

**Findings:**

- **[Trust boundaries]** No new external trust boundary. The control socket is `0600`-perms-locked, owner-only (verified in slice A's review at `internal/control/server.go:277-281`). The verb runs in the operator's own UID/GID; an attacker who can invoke `pyry rekey` can also invoke `pyry stop`, `pyry sessions rm`, and every other socket verb. The bar is not lowered. The `<conn_id>` positional is operator-supplied; it is forwarded verbatim to slice B1's `*V2SessionManager.Rekey`, which is responsible for validating against its open-conn map (slice B1's review covered this).
- **[Tokens, secrets, credentials]** Not in scope. The verb mints, transmits, stores, and logs no tokens. The control-socket request payload (`{"verb":"rekey","rekey":{"connID":"<value>"}}`) carries no secret material. The Noise re-handshake that the trigger initiates runs entirely on the daemon side; its symmetric keys never cross this wire.
- **[File operations]** No filesystem operations introduced. The verb does not read or write `~/.pyry/...` state files. The only file-system contact is `parseClientFlags` → `resolveSocketPath` → an `os.UserHomeDir()` lookup, which is the same code path every other client verb runs.
- **[Subprocess / external command execution]** None. The verb does not spawn `claude` or any other process. No `exec.Command`, no shell interpolation, no `os/exec` import.
- **[Cryptographic primitives]** Not in scope for this slice. All cryptographic work (Noise handshake, AEAD seal of the `rekey_request` envelope, cipher-state swap) lives behind the `Rekeyer.Rekey` contract — i.e. inside `*V2SessionManager` in `internal/relay`. Slice B1's security review (PASS, dated 2026-05-15) covers it. This slice is a thin RPC client.
- **[Network & I/O]** The verb dials one Unix-domain socket per invocation, sends one JSON line, reads one JSON line, closes. No retry loop, no keep-alive, no unbounded read. The 30s `context.WithTimeout` bounds the entire round trip. `json.Decoder` enforces the existing single-Decode call ceiling that every other client verb on this socket relies on. The DoS surface is unchanged from slice A's analysis (a malicious local caller can re-invoke at most one round trip per process spawn; this is irrelevant when the same caller already has `pyry stop` available).
- **[Error messages, logs, telemetry]** Stderr messages are deliberately compact: `pyry rekey: conn_id %q not found` (where `%q` Go-quotes the operator's input — defends against shell-injection-by-clever-printing-of-control-bytes), and `pyry rekey: <verbatim err.Error()>` for non-typed errors. The verbatim path could in principle leak server-side state if the daemon's `Response.Error` text grows to include internal pointers / paths / secrets — but the slice A and B1 reviews already covered this: the daemon's reject messages are `"rekey: no rekeyer configured"`, `"rekey: missing connID"`, `"relay: conn not found"`, and slice B1's `ErrSessionNotOpen` variant. None carry sensitive content. The verb does not invoke `slog`; there is no operator-facing audit log file written by this slice. Audit logging on the daemon side (slice B1's `rekeyTrigger` slog instrumentation) covers the durable record.
- **[Concurrency]** No new shared state introduced in this slice. The verb is single-threaded foreground code: the only goroutine boundary is `control.Rekey`'s internal `request()` helper, which is the same machinery every other client verb uses (`SessionsRm`, `SessionsRename`, etc.). No new mutexes, no new channels, no shutdown ordering question.
- **[Threat model alignment]** Per slice A's threat-model analysis: an attacker who can invoke `pyry rekey` already has every other socket verb available (filesystem perms are the only authentication). The verb is a force-the-key-rotation primitive — denying the operator key rotation is the relevant adversary, not enabling it. The verb does NOT weaken the threat model; it strengthens it by giving the operator a deterministic way to rotate keys after a suspected exposure (the explicit `payload.reason = "manual"` audit-trail value in `docs/protocol-mobile.md` § Re-key).
- **[Input validation at the parser boundary]** The verb's only operator-supplied input is `<conn_id>`. It is:
  - Required (parser rejects 0 positionals with `missing conn_id`).
  - Single (parser rejects ≥2 positionals).
  - Not parsed further — `<conn_id>` is opaque to the verb. Conn-id format validation is the daemon's responsibility (slice B1's `*V2SessionManager.Rekey` performs the lookup). This matches PROJECT-MEMORY § "Caller-supplied id validation at the primitive boundary, not the verb handler" — the primitive is `V2SessionManager`, not the CLI.
  - Stderr-quoted via `%q` on the not-found path. An operator-supplied `<conn_id>` containing newlines, ANSI escapes, or other terminal-control bytes is rendered as Go-quoted literal text. This defends a paranoid operator who is piping pyry's stderr into another shell against a hypothetical "the conn-id from logs contained an ANSI escape" attack.

**No findings warrant a spec revision.** Verdict: PASS.

**Reviewer:** architect (self-review per security-review.md mandate; ticket carries `security-sensitive` label).
**Date:** 2026-05-17.
