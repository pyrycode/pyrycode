# Spec #1109 — stream-json `RunnerFactory`: construct & inject `*streamsup.Runner` at both pool sites

**Ticket:** pyrycode/pyrycode#1109 · **Size:** S · **Security-sensitive:** no

The first `streamsup.New` caller in the tree. Deliver a `sessions.RunnerFactory` that maps an
incoming `supervisor.Config` → `streamsup.Config`, constructs a `*streamsup.Runner`, and wraps it in
the existing `cmd/pyry` `streamRunner{}` adapter (#1097). It becomes injectable on
`sessions.Config.RunnerFactory` so a stream-json daemon gets a live, pool-supervised runner at both
construction sites. **This ticket does not wire the factory into production** — the `interactive_runner`
selection that picks it is #1081. Everything downstream (turn-event draining #1098, interrupt/new-session
#1099/#1100, snapshot-offline #1101) builds on the runner this delivers.

## Files to read first

- `cmd/pyry/streamsup_runner.go` (whole file, 55 lines) — the `streamRunner{ r *streamsup.Runner }`
  adapter (#1097) you extend. Its doc comment (lines 20–22) is the stale `#1081's scope` comment you must
  correct. `mapStreamState` here is the sibling pattern for a small pure mapper.
- `internal/sessions/runner.go:38-46` — `RunnerFactory` type: **`func(cfg supervisor.Config) (Runner, error)`**.
  Your factory must have exactly this signature so `cfg.RunnerFactory = streamRunnerFactory` type-checks.
  Lines 25–31 are the `Runner` interface `streamRunner` already satisfies.
- `internal/streamsup/runner.go:60-113` — `streamsup.Config`: the target shape. Note the field name
  **`Args`** (not `ClaudeArgs`), and `Stdout`/`Stderr`/`Env` (leave all nil).
- `internal/streamsup/runner.go:158-196` — `streamsup.New`: validates `ClaudeBin`/`WorkDir`/`SessionID`
  non-empty, `exec.LookPath`s the binary, `agentrun.ResolveWorkdir`s the dir. **These are the loud-failure
  paths the factory must propagate.**
- `internal/streamsup/runner.go:476-498` — `buildArgs`: proves streamsup **re-injects** `--session-id <id>`
  (first spawn) / `--resume <id>` (respawn) from `Config.SessionID`. This is *why* the incoming argv's id
  flags must be stripped — otherwise double-injection.
- `internal/sessions/pool.go:444-517` — `Pool.New` bootstrap site: builds `supervisor.Config` with
  `SessionID: string(bootstrapID)` (#1108, line 462) and `ClaudeArgs: bootstrapArgs` that **do not** carry
  `--session-id` (resolved via the PTY-only `ResolveSessionID` closure). Strip is a **no-op** here.
- `internal/sessions/pool.go:1275-1330` — `Pool.buildSession` per-session site: `SessionID: string(id)`
  (#1108, line 1310) **and** `ClaudeArgs: args` where `args` bakes `--session-id <id>` (line 1291). Strip is
  **required** here.
- `internal/supervisor/supervisor.go:92-185` — `supervisor.Config`: the incoming shape. Confirm it has **no**
  `Stderr`/`Env` field (so those map to nil) and which fields have no streamsup analogue (below).
- `cmd/pyry/streamsup_runner_test.go` — existing `TestMapStreamState`; the same package/style your new tests
  join. `t.Parallel()`, table-driven, stdlib only.
- `docs/knowledge/features/streamsup-package.md` § "Out of scope (follow-on slices)" — confirms this is the
  `#1081` factory arm being pulled forward into #1109; § "`buildArgs`" for the id-flag inversion.

## Context

`sessions.Config.RunnerFactory` (`func(cfg supervisor.Config) (sessions.Runner, error)`; nil ⇒
`supervisor.New`, the byte-identical PTY rollback default — #1077) is the injection seam. #1097 landed the
`streamRunner` adapter proving `*streamsup.Runner` satisfies `sessions.Runner`, but deferred the *factory
that constructs it*. `streamsup.New` has **zero callers tree-wide** — this ticket is the first.

The construction-safe id both sites read is delivered by #1108 (merged): the pool sets
`supervisor.Config.SessionID` unconditionally at both sites — `string(bootstrapID)` in `Pool.New`,
`string(id)` in `Pool.buildSession`. This ticket delivers **only** the constructed + injected runner: not
turn-event draining, not the config toggle, not control-verb routing.

## Design

All new code lives in **`cmd/pyry/streamsup_runner.go`** (modify the existing file — the factory belongs
next to the adapter it produces, and the comment you must fix is already there). Three unexported additions,
plus one comment correction. **No new files. No exported symbols** (#1081 consumes these from the same
`package main`).

### 1. The factory — `streamRunnerFactory`

```go
func streamRunnerFactory(cfg supervisor.Config) (sessions.Runner, error)
```

- Signature is **exactly** `sessions.RunnerFactory`. Body: `r, err := streamsup.New(mapStreamsupConfig(cfg))`;
  on error return `(nil, fmt.Errorf("cmd/pyry: stream runner: %w", err))`; on success return
  `(streamRunner{r: r}, nil)`.
- **No PTY fallback.** On a `streamsup.New` error the factory returns a **nil** `sessions.Runner` and the
  wrapped error — it must never call `supervisor.New`. The error surfaces through the pool's existing
  `"sessions: bootstrap supervisor: %w"` (New) / `"sessions: create supervisor: %w"` (buildSession) wraps.
- Assign-compatible with the seam: `sessions.Config{RunnerFactory: streamRunnerFactory}` type-checks because
  a func value is assignable to the named func type `sessions.RunnerFactory`. That assignment is #1081's job,
  not this ticket's — see § Out of scope.

### 2. The pure mapper — `mapStreamsupConfig`

```go
func mapStreamsupConfig(cfg supervisor.Config) streamsup.Config
```

Pure, no side effects, fully inspectable — **this is the primary tested surface** (all fields are readable on
the returned struct, so the no-double-inject invariant is asserted directly rather than through the opaque
`*streamsup.Runner`). Field mapping:

| `supervisor.Config` field | `streamsup.Config` field | Note |
|---|---|---|
| `ClaudeBin` | `ClaudeBin` | copy |
| `WorkDir` | `WorkDir` | copy |
| `SessionID` | `SessionID` | copy — #1108 guarantees non-empty at both pool sites |
| `ClaudeArgs` | `Args` | **`stripSessionIDFlags(cfg.ClaudeArgs)`** — note the field rename |
| `Logger` | `Logger` | copy (nil → streamsup defaults to `slog.Default()`) |
| `BackoffInitial` | `BackoffInitial` | copy |
| `BackoffMax` | `BackoffMax` | copy |
| `BackoffReset` | `BackoffReset` | copy |
| — | `Stdout` | **leave nil** (turnevent sink is #1098; unconsumed child stdout → `/dev/null`) |
| — | `Stderr` | leave nil (no `supervisor.Config` analogue) |
| — | `Env` | leave nil (no `supervisor.Config` analogue) |

**Fields on `supervisor.Config` deliberately NOT mapped** (PTY-path concerns with no streamsup analogue —
do not try to map them): `ResumeLast`, `ResolveSessionID`, `Bridge`, `ValidateConversation`,
`ResolveTranscript`, `RecordDir`, `helperEnv`. `streamsup` owns its own id-flag inversion (`buildArgs`), has
no PTY bridge, no transcript binding, and no `.cast` recorder.

### 3. The strip helper — `stripSessionIDFlags`

```go
func stripSessionIDFlags(args []string) []string
```

Pure. Returns a **new** slice (never mutates `args` — the caller's `ClaudeArgs` is aliased into the pool's
`spawnBase`). Removes every `--session-id` / `--resume` occurrence together with its value. Handle both
argv forms:

- **Two-token form** `--session-id`, `<value>` / `--resume`, `<value>` — the form the codebase actually
  produces (`Pool.buildSession` line 1291, `streamsup.buildArgs`). Drop the flag **and** the next token.
- **Joined form** `--session-id=<value>` / `--resume=<value>` — drop the single token. Defensive: an operator
  template could carry this; cheap to handle.
- **Dangling flag** (`--session-id` with no following token, e.g. last element) — drop the lone flag, don't
  index out of bounds.

**Why strip:** `streamsup.buildArgs` re-injects `--session-id <id>` (first spawn) / `--resume <id>`
(respawn) from `Config.SessionID` itself. Leaving the pool's baked `--session-id <id>` in `Args` would
double-inject (`--session-id X … --session-id X`, or the contradictory `--session-id X … --resume X` on
respawn). The strip is **required** at the per-session site and a **harmless no-op** at the bootstrap site
(bootstrap `ClaudeArgs` carry no id flag). One helper, applied uniformly at both — no site-specific branch.

### Data flow

```
sessions.New(cfg{RunnerFactory: streamRunnerFactory})              [#1081 sets this; test injects it]
  └─ Pool.New builds supervisor.Config{SessionID: bootstrapID, ClaudeArgs: [...no id flag...]}
       └─ newRunner(supCfg) == streamRunnerFactory(supCfg)
            └─ mapStreamsupConfig  →  streamsup.Config{SessionID: bootstrapID, Args: strip(ClaudeArgs)}
                 └─ streamsup.New  →  *streamsup.Runner
                      └─ streamRunner{r}  (satisfies sessions.Runner)

Pool.buildSession builds supervisor.Config{SessionID: id, ClaudeArgs: [--session-id id, --settings p, ...]}
  └─ same chain; strip removes "--session-id id" so buildArgs re-injects exactly one id flag.
```

## Concurrency model

None introduced. The factory is a synchronous construction-time call made by `sessions.New` /
`Pool.buildSession` on the constructing goroutine, before any supervise goroutine exists. `mapStreamsupConfig`
and `stripSessionIDFlags` are pure. The constructed `*streamsup.Runner`'s own three-leaf-mutex model is
#1097's and is untouched here.

## Error handling

One failure mode: `streamsup.New` returns an error (empty `SessionID` — won't occur at pool sites per #1108;
missing/absent binary via `exec.LookPath`; non-existent `WorkDir` via `agentrun.ResolveWorkdir`). The factory
wraps it (`fmt.Errorf("cmd/pyry: stream runner: %w", err)`) and returns `(nil, err)`. It **never** substitutes
a PTY `supervisor.New` — that would silently diverge the bootstrap (the primary interactive session `pyry
attach` drives) from the operator's stated stream-json intent. Propagation is verified by test (below).

## Testing strategy

Extend `cmd/pyry/streamsup_runner_test.go` (same `package main`, `t.Parallel()`, table-driven, stdlib only).
Scenarios as bullets — the developer writes the assertions in the project idiom:

- **`stripSessionIDFlags` table test.** Inputs → expected:
  - `nil` / `[]` → empty (no panic).
  - `[--model m]` (no id flags) → `[--model m]` unchanged.
  - `[--model m, --session-id, ID, --settings, p]` → `[--model m, --settings, p]`.
  - `[--resume, ID]` → `[]`.
  - `[--session-id=ID, --model, m]` → `[--model, m]`.
  - `[--resume=ID]` → `[]`.
  - `[--model, m, --session-id]` (dangling, no value) → `[--model, m]`.
  - Assert the input slice is **not mutated** (pass a copy, compare original to a saved copy).
- **`mapStreamsupConfig` — bootstrap shape.** Input mirrors `Pool.New`: `SessionID: "boot-uuid"`,
  `ClaudeArgs: [--settings, p]` (no id flag), plus `ClaudeBin`/`WorkDir`/`Logger`/backoff set. Assert every
  mapped field copies through, `SessionID == "boot-uuid"`, `Args == [--settings, p]` (strip is a no-op),
  and `Stdout == nil`.
- **`mapStreamsupConfig` — per-session shape (no double-inject).** Input mirrors `Pool.buildSession`:
  `SessionID: "sess-uuid"`, `ClaudeArgs: [--session-id, sess-uuid, --settings, p]`. Assert `Args` contains
  **no** `--session-id` and no `--resume` and no residual bare id token, `SessionID == "sess-uuid"` preserved.
  This is the AC-2 no-double-inject proof, asserted on the observable `.Args`.
- **`streamRunnerFactory` construction — both shapes.** Point `ClaudeBin` at a resolvable binary
  (`os.Args[0]` is an absolute path, so `exec.LookPath` accepts it — the simplest resolvable binary) and
  `WorkDir` at `t.TempDir()`. Drive the factory with (a) the bootstrap-shaped and (b) the per-session-shaped
  `supervisor.Config`. Assert `(runner, nil)`, type-assert `runner.(streamRunner)` (same package), and inner
  `.r != nil`. Covers AC-1 + AC-2's "constructs successfully at both sites."
- **`streamRunnerFactory` error propagation — no PTY fallback.** Set `ClaudeBin` to a guaranteed-absent name
  (e.g. `"pyry-nonexistent-binary-xyz"`), valid `SessionID`/`WorkDir`. Assert the returned error is non-nil
  **and** the returned `sessions.Runner` is nil. This catches a regression to `supervisor.New` fallback:
  `supervisor.New` does not `LookPath` at construction, so a fallback would return `(non-nil, nil)` and fail
  this assertion. Covers AC-3.
- **AC-4 (nil factory byte-identical): no new test.** Satisfied structurally — this ticket does **not** modify
  the two production `sessions.New` call sites (`cmd/pyry/main.go:753`, `cmd/pyry/acp.go:67`), so production
  `RunnerFactory` stays nil and the PTY path is byte-identical. The nil-path normalization is already covered
  by #1077's internal/sessions tests. **Do not touch those two call sites** (that wiring is #1081).

## Comment correction (required by the ticket)

In `cmd/pyry/streamsup_runner.go`, the type doc comment currently ends (lines 20–22):

> *The factory arm that constructs a streamRunner from a supervisor.Config is #1081's scope; this ticket
> delivers only the adapter and the compile-time proof that it satisfies the interface.*

Replace with a statement that the factory now lives in this file (delivered by #1109), e.g.: *"The factory
that constructs a streamRunner from a supervisor.Config — `streamRunnerFactory` below — is delivered by
#1109; the `interactive_runner` selection that injects it on `sessions.Config.RunnerFactory` is #1081."*

## Out of scope (do not do here)

- **Wiring the factory into production** — the `interactive_runner` config toggle / selection at
  `cmd/pyry/main.go:753` and `cmd/pyry/acp.go:67` is **#1081**. Leave both `sessions.New` call sites untouched.
- Draining turnevents into `interactiveTurnEmitterV2` / setting `Config.Stdout` — **#1098**.
- Interrupt / new-session routing — **#1099 / #1100**. Snapshot-offline — **#1101**.
- Any change to `internal/streamsup` or `internal/sessions` — this is a `cmd/pyry`-only slice. `streamsup.New`
  and the pool's #1108 seams are consumed as-is.

## Acceptance criteria (from the ticket)

1. A stream-json `RunnerFactory` (`streamRunnerFactory`) constructs a `*streamsup.Runner` adapted via
   `streamRunner`, injectable on `sessions.Config.RunnerFactory` — the first `streamsup.New` caller.
2. Constructs successfully at both the bootstrap (`Pool.New`) and per-session (`Pool.buildSession`) shapes,
   each reading the non-empty #1108 `SessionID`; a per-session `ClaudeArgs` carrying baked `--session-id <id>`
   does not double-inject — proven by the mapper + construction tests.
3. No silent PTY fallback for the bootstrap: a `streamsup.New` failure surfaces as an error (nil runner),
   proven by the error-propagation test.
4. With a nil `RunnerFactory`, daemon startup is byte-identical to today (rollback guarantee) — satisfied by
   leaving both production `sessions.New` sites untouched.

## Open questions

None. The seam (#1077), the adapter (#1097), and the id field (#1108) are all merged; the mapping is
mechanical and the strip is a small pure function. If `exec.LookPath` on `os.Args[0]` proves awkward in CI,
any always-present binary (`"true"`, or the resolved `go` toolchain path) works for the construction test —
`streamsup.New` only needs a *resolvable* path, not a real claude.
