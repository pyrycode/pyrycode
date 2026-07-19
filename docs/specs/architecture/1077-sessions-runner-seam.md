# Spec: `sessions.Runner` seam + `RunnerFactory` (#1077)

**Size:** S · **Not security-sensitive** (interface over methods that already exist on
`*supervisor.Supervisor`; introduces no new trust/permission boundary — an alternative
runner and its authz ride downstream in T4/T7). No `security-sensitive` label → no
security-review pass.

## Files to read first

- `internal/sessions/session.go:124-244` — the `Session` struct (`sup` field at `:133`), the five
  delegating methods (`State` `:223`, `WriteUserTurn` `:230`, `WaitForPTY` inside `Activate` `:344`,
  `Run`'s `s.sup.Run` at `:501`), and the **`Supervisor()` accessor at `:234-238`** (the one whose
  return type this spec deliberately keeps concrete).
- `internal/sessions/pool.go:53-137` — the `Config` struct (where `RunnerFactory` is added).
- `internal/sessions/pool.go:175-200` — the `Pool` struct (where the normalized `newRunner` field lands;
  mirror the `log` / `convReg` field style).
- `internal/sessions/pool.go:460-510` — **bootstrap construction site** (`supervisor.New` at `:480`),
  including the `bootstrapSup` local at `:462` (re-typed to `Runner`) and the `pidFn` closure that
  reads `bootstrapSup.State().ChildPID` at `:466-470`.
- `internal/sessions/pool.go:518` — the `&Pool{...}` literal in `Pool.New` (persist `newRunner` here).
- `internal/sessions/pool.go:1241-1292` — `buildSession` (Create path); **second construction site**
  (`supervisor.New` at `:1289`). Called by `CreateIn` (`:1181`) and `Pool.GetOrCreate`
  (`internal/sessions/get_or_create.go`).
- `internal/sessions/pool.go:685-694` — `UpdateSettings` reads `sup := sess.sup` then calls
  `sup.Restart(newArgs)` — confirms `Restart` is dispatched through the field, so it belongs on `Runner`.
- `internal/turnbridge/producer.go:31-36` — `SessionHost` interface (`Session()` + `WaitForPTY()`).
  Read to confirm it is **not** widened by this ticket (it keeps consuming the concrete supervisor via
  the concrete accessor).
- `cmd/pyry/acp.go:257-273` — `resolveCancelTarget` returns `*supervisor.Supervisor`; **stays concrete**.
  Read to confirm the accessor's concrete return keeps this and its caller (`:144-151`) compiling untouched.
- `cmd/pyry/relay.go:146-185` + `:419-602` — `relayWiring.sup *supervisor.Supervisor` and its downstream
  fan-out (`newModalResolverV2`, `startInteractiveTurnStreamV2`, `startInteractiveModalStreamV2`,
  `Snapshotter`/`Interrupter`/`SessionStarter`, and `e.sup.CurrentConversation()` at
  `interactive_turn_v2.go:141`). Read **only to confirm none of it is touched** — the concrete-accessor
  decision is what keeps this subtree out of scope.
- `internal/sessions/session_test.go:360-375` — existing test calls `sess.sup.WaitForPTY(...)`; the field
  re-type must keep it compiling (`WaitForPTY` is on `Runner`).
- `CODING-STYLE.md` § Interface Design — "accept interfaces, return structs; small interfaces; define at
  the consumer." The `Runner` seam is consumer-defined in `internal/sessions` and satisfied structurally
  by `*supervisor.Supervisor` with zero supervisor edits.

## Context

`internal/sessions` is welded to the concrete supervisor: `Session.sup` is a
`*supervisor.Supervisor` (`session.go:133`), built directly via `supervisor.New` at two sites
(`pool.go:480` bootstrap, `pool.go:1289` Create). The Streamrunner Interactive work (T4/T7) needs to
swap an alternative runner in behind the same lifecycle. This ticket extracts **only** the injection
seam: a narrow `sessions.Runner` interface (already satisfied by `*supervisor.Supervisor`) plus a
`RunnerFactory` on `Config`. When `RunnerFactory == nil` the factory defaults to `supervisor.New`, so
the concrete supervisor still flows through and today's PTY path is byte-identical — **that nil default
is the rollback guarantee.** No behaviour change; wiring an alternative runner is T4/T7.

## Design

### Central decision: keep `Supervisor()` concrete → minimal `Runner` interface

The ticket delegates the accessor shape to the architect ("return `Runner` vs. keep the concrete
return"). **Keep the concrete return** (`Supervisor() *supervisor.Supervisor`). Rationale:

- **It maximises "byte-identical."** All 7 external `Session.Supervisor()` touch-points (5 production +
  2 test) compile *unchanged*. The consumers keep receiving the same concrete `*supervisor.Supervisor`
  at runtime under the nil default.
- **It avoids premature consumer migration** (which the ticket forbids: *"Do NOT migrate any consumer
  onto an alternative runner in this ticket; that is T4/T7."*). Returning `Runner` would force
  `relayWiring.sup` (`relay.go:185`) and its entire downstream subtree —
  `startInteractiveTurnStreamV2`/`startInteractiveModalStreamV2` params, `newModalResolverV2`, and
  `e.sup.CurrentConversation()` — to re-type to `Runner`, dragging in `CurrentConversation` (a 13th
  method) and blowing past the 10-call-site refactor line. That is exactly the T4/T7 migration, not
  this slice.
- **It honours "no speculative methods."** Because consumers dispatch on the concrete type through the
  concrete accessor, the only methods dynamically dispatched *through the `Runner`-typed field* are the
  five `internal/sessions` itself calls. Adding `SendEsc`/`Answer`/`AcceptTrust`/`ScreenSnapshot`/
  `StartNewSession`/`Session`/`WorkDir` to `Runner` would be methods nothing routes through `Runner`
  this ticket — speculative. They are reached today (and until T4/T7) via the concrete accessor.

The AC's 12-method list is the discretion menu; the correct minimal shape under a concrete accessor is
the five methods below. A code reviewer verifies "strictly per what compiles" deterministically: a
sixth method on `Runner` is unused; a missing fifth fails to build.

### The interface (new file `internal/sessions/runner.go`)

`Runner` covers exactly the methods `internal/sessions` invokes on the runner field
(`session.go:223/230/344/501`, `pool.go:690-693`, `pool.go:466-470`):

| Method | Signature | Call site |
|---|---|---|
| `State` | `State() supervisor.State` | `session.go:223`, `pool.go:469` (`bootstrapSup.State()`) |
| `WriteUserTurn` | `WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error` | `session.go:231` |
| `WaitForPTY` | `WaitForPTY(ctx context.Context) error` | `session.go:344`, `session_test.go:371` |
| `Run` | `Run(ctx context.Context) error` | `session.go:501` |
| `Restart` | `Restart(args []string)` | `pool.go:693` |

- **Contract, not body:** the interface is these five signatures. `State()` returns
  `supervisor.State` — `sessions` already imports `supervisor`, so no new import.
- **Compile-time assertion** in `runner.go`: `var _ Runner = (*supervisor.Supervisor)(nil)`. This is
  AC-2's proof; it must build with **zero** diff under `internal/supervisor/`.

### The factory (new type in `runner.go`, field on `Config`)

```
type RunnerFactory func(cfg supervisor.Config) (Runner, error)   // signature only
```

- `Config` (`pool.go:53`) gains one field: `RunnerFactory RunnerFactory` — documented as "nil selects
  the default `supervisor.New`, keeping the PTY path byte-identical (the rollback guarantee)."
- **Normalize once in `Pool.New`:** derive a non-nil local `newRunner` at the top of `New`:
  `newRunner := cfg.RunnerFactory; if newRunner == nil { newRunner = func(c supervisor.Config)(Runner,error){ return supervisor.New(c) } }`.
  The default wrapper adapts `supervisor.New`'s `(*Supervisor, error)` to `(Runner, error)` — a plain
  return, since `*Supervisor` satisfies `Runner`.
- **Route both sites through it:**
  - Bootstrap (`pool.go:480`): `sup, err := newRunner(supCfg)`. `p` does not exist yet at this line
    (the `&Pool{}` literal is at `:518`), so use the local `newRunner`.
  - Persist on the Pool: add `newRunner RunnerFactory` to the `Pool` struct (`:175` region) and set it
    in the `&Pool{...}` literal at `:518` (`newRunner: newRunner`).
  - Create (`pool.go:1289`): `sup, err := p.newRunner(supCfg)`. `buildSession` is a `*Pool` method, so
    `p.newRunner` is in scope.

### Field + accessor changes (`session.go`)

- `Session.sup` (`:133`): `*supervisor.Supervisor` → `Runner`. The five delegating methods
  (`State`/`WriteUserTurn`/`WaitForPTY`/`Run`/`Restart`) call methods now on the interface — no body
  changes beyond the field type.
- `Supervisor()` accessor (`:234-238`): **return type unchanged** (`*supervisor.Supervisor`). Body
  becomes a type assertion that is *total under the nil-factory default*:
  - signature: `func (s *Session) Supervisor() *supervisor.Supervisor`
  - behaviour: return `s.sup` asserted to `*supervisor.Supervisor`, or `nil` when an alternative
    `Runner` has been injected. Document: unreachable under the nil default (rollback guarantee); T4/T7
    migrate these call sites onto `Runner` and retire the assertion. The comma-ok's `nil`-on-miss is the
    intended contract, not an ignored error.
- `bootstrapSup` local (`pool.go:462`): `var bootstrapSup *supervisor.Supervisor` → `var bootstrapSup Runner`.
  Its only use (`bootstrapSup.State().ChildPID`) is on `Runner`. The `newProbePreferredTranscriptResolver`
  wiring (gated on `ClaudeSessionsDir != ""`) is unchanged.

### Out of scope (do NOT touch)

`internal/supervisor/**` (zero diff — AC-2), `turnbridge.SessionHost`, `cmd/pyry` (`acp.go`,
`acp_turn_streams.go`, `main.go`, `relay.go`, `interactive_*_v2.go`), and the concrete return type of
`Supervisor()`. Any edit here means the design drifted into the T4/T7 consumer migration.

## Concurrency model

None introduced. `Runner` is an interface over the existing supervisor surface; the factory is invoked
synchronously at construction (in `Pool.New` under no new lock; in `buildSession` under the existing
discipline). `p.newRunner` is set once in `Pool.New` and read-only thereafter (same lifetime as
`p.log`) — no synchronization needed. The `sup` field's guarding is unchanged (it was already an
immutable-post-construction handle read by the lifecycle goroutine and by `UpdateSettings` under the
existing `p.mu`-then-release discipline at `pool.go:685-691`).

## Error handling

- Default wrapper propagates `supervisor.New`'s error verbatim; the existing wraps
  (`"sessions: bootstrap supervisor: %w"` at `:482`, `"sessions: create supervisor: %w"` at `:1291`)
  are unchanged — they now wrap the factory's error, which for the nil default *is* `supervisor.New`'s.
- A non-nil factory returning an error surfaces through the same wrapped paths — no new error class.
- `Supervisor()` returning `nil` for an injected alternative runner is a defined state, not an error;
  no production caller reaches it this ticket.

## Testing strategy

Existing suites (PTY/interactive, `pool_test.go`, `pool_cap_test.go`, `session_test.go`) must pass
**unchanged** — AC-3. The two `sup:` struct literals in `pool_test.go:893` / `pool_cap_test.go:78` hold
concrete supervisors that satisfy `Runner`; `session_test.go:371` calls `WaitForPTY` (on `Runner`).

New test (`internal/sessions/runner_test.go`, same package `sessions`, stdlib only, table-free is fine)
for AC-4 — prove the factory is threaded at **both** construction sites:

- Define a tiny `fakeRunner` satisfying `Runner` with no-op method bodies (`State` returns zero
  `supervisor.State`; `WriteUserTurn`/`WaitForPTY`/`Run` return `nil`; `Restart` no-op). It never spawns
  a PTY, so it is CI/non-TTY safe.
- Supply a counting `RunnerFactory` that increments per call and returns a fresh `fakeRunner`.
- **Bootstrap site:** construct the Pool via `New` with that factory (leave `ClaudeSessionsDir` empty to
  skip the transcript-resolver path; `RegistryPath` empty for no persistence). Assert the factory ran
  once and `supervisor.New` did not (the counter proves it).
- **Create site:** drive one create (`CreateIn` or `GetOrCreate` — pick the path that does not require a
  running `Pool.Run`, i.e. build the session without needing the lifecycle goroutine to progress; if the
  chosen path registers `g.Go(sess.Run)`, `fakeRunner.Run` returning `nil` keeps it inert). Assert the
  factory ran a second time.
- Assert the total invocation count is exactly 2 (bootstrap + one create), i.e. the seam is threaded at
  every site, not merely declared.
- Optionally assert `var _ Runner = (*supervisor.Supervisor)(nil)` — but that already lives in
  `runner.go` and gates compilation, so an explicit test is redundant. AC-2's `git diff` check
  (`git diff --stat internal/supervisor/` shows no entries) is a manual/CI verification, not a Go test.

Describe scenarios as above; write them in the project's testing idiom (stdlib `testing`, `t.Helper()`
on shared assertions, no testify).

## Acceptance criteria mapping

- **AC-1** (Runner covers only what compiles): the five-method interface in `runner.go`; concrete
  accessor keeps consumer dispatch off `Runner`, so no speculative methods.
- **AC-2** (`var _ Runner = (*supervisor.Supervisor)(nil)` + zero supervisor diff): assertion in
  `runner.go`; no `internal/supervisor/` edits.
- **AC-3** (nil factory byte-identical; existing tests unedited): default wrapper = `supervisor.New`;
  existing suites untouched.
- **AC-4** (non-nil factory observed at bootstrap **and** Create): `runner_test.go` counting-factory
  test above.

## Open questions

- **Create-path test entry point:** `CreateIn` vs `GetOrCreate` for the AC-4 second-site assertion.
  Both funnel through `buildSession` (`:1241`), so either observes the factory; the developer picks
  whichever needs the least lifecycle setup. Non-blocking — both satisfy the AC.
- **Test file placement:** `runner_test.go` is the natural home, but folding the AC-4 test into an
  existing `pool_test.go` construction test is equally valid if it reuses that file's Pool-construction
  helpers. Developer's call.
