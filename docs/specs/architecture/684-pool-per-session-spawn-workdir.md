# Spec #684 — Per-session spawn workdir on the Pool (default falls back to shared workdir)

**Ticket:** [#684](https://github.com/pyrycode/pyrycode/issues/684) · **Size:** S (XS-scale) · **Labels:** `size:s` (not `security-sensitive` — opaque-path primitive, no trust boundary; trust/canonicalisation lives in #685)

**Split from #681.** This is the pool-level *primitive*: the surface + default-fallback behaviour, with no caller yet supplying a non-default value. The consumer that supplies a validated, trusted directory is #685 (`blocked-by #684`).

---

## Files to read first

- `internal/sessions/pool.go:947-1002` — `buildSession(id, label)`, the **single spawn seam**. Line 957 sets `supervisor.Config{ WorkDir: tpl.WorkDir }` for every session. This is the only place that needs the conditional `spawnDir → WorkDir` logic.
- `internal/sessions/pool.go:900-937` — `Pool.Create(ctx, label)`. Its body (mint id → build → persist → supervise → activate) moves into `CreateIn`; `Create` becomes a one-line delegator.
- `internal/sessions/get_or_create.go:44-104` — `Pool.GetOrCreate(ctx, id, label)`. Same treatment: body moves into `GetOrCreateIn`; `GetOrCreate` delegates with `""`. Note the take-path label-drop at `:62` — `spawnDir` is dropped on the take path the same way.
- `internal/supervisor/supervisor.go:85-87` + `:635-640` and `internal/supervisor/spawn.go:22` + `:40-41` — `Config.WorkDir` is read as `cmd.Dir` on **every** (re)spawn. This is why a per-session workdir survives child respawns with **no new `Session` field** — the supervisor already retains it. `WorkDir == ""` means inherit (today's bootstrap behaviour is unaffected).
- `internal/sessions/pool_create_test.go:18-81` — `helperPoolCreate` + `runPoolInBackground`. Reuse the harness shape. The fake-claude pattern (`/bin/sh -c "exec sleep 3600" --`, tolerates the appended `--session-id <uuid>`) and `ChildPID > 0` spawn-polling are the building blocks for the new test (see Testing strategy for the cwd-recorder variant).
- `cmd/pyry/main.go:666-669` — `sessionMinter.Create` calls `m.p.Create(ctx, label)`. Must compile unchanged (it will — the delegator preserves the signature). This is the AC-3 regression anchor.
- `internal/e2e/harness.go:201` — `StartIn(t, home, ...)`, the existing **`XxxIn` naming precedent** ("the same operation, but in a given directory") that `CreateIn`/`GetOrCreateIn` mirror.
- `internal/e2e/workdir_trust_test.go` — context only (#670 prior art for spawning in a trusted dir at the e2e level). This ticket is unit-level; do not extend the e2e suite.

---

## Context

Every session the pool spawns today runs in the single shared `tpl.WorkDir` (the daemon's trusted bootstrap workdir). `buildSession` hard-wires `supervisor.Config{ WorkDir: tpl.WorkDir }` (pool.go:957) and neither `Pool.Create` nor `Pool.GetOrCreate` accepts a per-session workdir.

A forthcoming slice (#685) needs each per-conversation session to spawn in its conversation's own directory. That requires the pool to *carry* a per-session spawn workdir. This ticket adds only that primitive — the surface and its default-fallback behaviour — with no caller supplying a non-default value yet.

**This is pure plumbing.** The pool treats the supplied directory as **opaque**: no validation, no canonicalisation, no trust handling, no `os.Stat`. It receives a pre-resolved path and spawns there. Trust / canonicalisation / `$HOME`-containment is #685's job, which is why this slice is deliberately **not** `security-sensitive` — no untrusted input reaches it, and its only caller after it still passes the default.

---

## Design

### Mechanism: `XxxIn` sibling methods (not functional options, not a new parameter)

Three candidate mechanisms were on the table (per the ticket's Technical Notes): a variadic functional option, an extra `Create` parameter, or threading a field through `buildSession`.

**Chosen: sibling methods `CreateIn` / `GetOrCreateIn`, with `Create` / `GetOrCreate` as thin delegators.** Rationale:

- The codebase has **zero** functional-options precedent (`grep "Option func(" internal/ cmd/` → empty). Introducing the options framework (an exported `CreateOption` type + `WithSpawnDir` constructor + an internal opts struct + a fold loop in two methods) for a *single* optional string violates "Simplicity first. Don't add abstraction layers it doesn't need yet" (CLAUDE.md).
- The codebase **does** have the `XxxIn` idiom: `StartIn(t, home, ...)` in `internal/e2e/harness.go:201` — literally "start, but in a given home dir." `CreateIn(ctx, label, spawnDir)` = "create, but spawn in `spawnDir`" is the exact analog. "Respect existing patterns" (CLAUDE.md) points here.
- An extra parameter on `Create` itself would break `m.p.Create(ctx, label)` and every test call site — disqualified by AC-3.
- Sibling methods keep the public `Create` / `GetOrCreate` signatures byte-identical, so the entire AC-3 call-site set (`sessionMinter`, the `sessions.new` verb, all current tests) compiles and behaves unchanged with zero churn.

Both public entry points get the capability (not just one) because the Technical Notes defer the `Create`-vs-`GetOrCreate` choice to #685: "makes it available to whichever public entry point the consumer slice ends up using." Exposing both means #685 picks freely without reopening this seam. The marginal cost is one extra ~3-line delegator.

### Surface (contracts only — no bodies)

```go
// internal/sessions/pool.go

// CreateIn is Create with an explicit per-session spawn working directory.
// spawnDir == "" spawns in the shared template workdir (tpl.WorkDir),
// byte-identical to Create. A non-empty spawnDir is used verbatim and is
// NOT validated, canonicalised, or trust-checked by the pool — callers
// supply a pre-resolved path (see #685).
func (p *Pool) CreateIn(ctx context.Context, label, spawnDir string) (SessionID, error)

// Create spawns in the shared template workdir. Unchanged signature.
func (p *Pool) Create(ctx context.Context, label string) (SessionID, error) // => CreateIn(ctx, label, "")
```

```go
// internal/sessions/get_or_create.go

// GetOrCreateIn is GetOrCreate with an explicit per-session spawn workdir,
// applied only on the *create* path. On the take path (session already
// registered) spawnDir is ignored — the existing session keeps its own
// workdir, mirroring how the take path already drops the caller's label
// (get_or_create.go:62).
func (p *Pool) GetOrCreateIn(ctx context.Context, id SessionID, label, spawnDir string) (SessionID, error)

// GetOrCreate. Unchanged signature.
func (p *Pool) GetOrCreate(ctx context.Context, id SessionID, label string) (SessionID, error) // => GetOrCreateIn(ctx, id, label, "")
```

```go
// internal/sessions/pool.go — the only behavioural change, at the spawn seam:

// buildSession gains a spawnDir param. supervisor.Config.WorkDir resolves to
// spawnDir when non-empty, else tpl.WorkDir (today's value).
func (p *Pool) buildSession(id SessionID, label, spawnDir string) (*Session, error)
//   workDir := tpl.WorkDir; if spawnDir != "" { workDir = spawnDir }
//   supCfg := supervisor.Config{ ..., WorkDir: workDir, ... }
```

### Implementation shape (move-the-body, don't duplicate)

- `Create`'s current body (pool.go:900-937) becomes the body of `CreateIn`, with the one change that its `buildSession(id, label)` call becomes `buildSession(id, label, spawnDir)`. `Create` shrinks to `return p.CreateIn(ctx, label, "")`. **No logic is duplicated** — the persist/supervise/activate sequence lives in exactly one place.
- `GetOrCreate`'s current body (get_or_create.go:44-104) becomes the body of `GetOrCreateIn`, with `buildSession(id, label)` → `buildSession(id, label, spawnDir)`. `GetOrCreate` shrinks to `return p.GetOrCreateIn(ctx, id, label, "")`.
- `buildSession`'s two internal callers (`CreateIn`, `GetOrCreateIn`) are the only call sites — both updated in this change. No fan-out (`grep buildSession` → exactly these two call sites plus comments).

### What is explicitly NOT touched

- **The bootstrap session.** It is constructed directly in `New` (pool.go:379), not via `buildSession`. It keeps `WorkDir: cfg.Bootstrap.WorkDir`. Unchanged — AC-2's "byte-for-byte identical for every existing caller" includes the bootstrap.
- **No new `Session` field.** The workdir lives only in `supervisor.Config`, which the supervisor reads on every (re)spawn. Crash-respawns reuse it automatically.
- **No registry persistence.** See Out of scope.

### Data flow

```
CreateIn(ctx, label, spawnDir)        GetOrCreateIn(ctx, id, label, spawnDir)
        │                                     │ (create path only)
        └──────────────┬──────────────────────┘
                       ▼
        buildSession(id, label, spawnDir)
                       │
        workDir = spawnDir != "" ? spawnDir : tpl.WorkDir
                       ▼
        supervisor.Config{ WorkDir: workDir, ... } ──► supervisor.New
                       ▼
        (every spawn) cmd.Dir = cfg.WorkDir ──► claude child runs in workDir
```

---

## Concurrency model

No change. `CreateIn`/`GetOrCreateIn` inherit the exact locking sequence of `Create`/`GetOrCreate` (register + persist under `p.mu`, then `supervise`/`Activate` off-lock; the cap path's `capMu` serialisation is untouched). `buildSession` still touches no `Pool` state and is still safe to call before taking `p.mu`. The new `spawnDir` is a pure value passed through call frames — no shared state, no new goroutine, no new channel.

---

## Error handling

**No new error modes in the pool.**

- `spawnDir == ""` → identical to today (default fallback). Zero behavioural delta.
- A non-empty `spawnDir` is passed verbatim to `supervisor.Config.WorkDir`. The pool does **not** `os.Stat` or otherwise validate it. If the directory does not exist or is not accessible, the failure surfaces at **spawn time** via the supervisor's existing path (`cmd.Start` returns a chdir error → backoff/restart loop), exactly as any other spawn failure. This is intentional: the pool stays opaque; the consumer (#685) is responsible for supplying a validated, existing, trusted directory.
- `supervisor.New` does not reject a `WorkDir` value, so construction is unaffected.

---

## Testing strategy

New tests in a focused file (`internal/sessions/pool_spawndir_test.go`) so existing test files stay untouched. Reuse `runPoolInBackground` from `pool_create_test.go`. The challenge is *observing* the child's cwd without adding a production accessor — solve it behaviourally with a **cwd-recorder fake claude**.

**Recorder recipe (keeps it behavioural, no production surface added):**
Configure the template with `ClaudeBin: "/bin/sh"` and `ClaudeArgs: ["-c", `pwd > "cwd-$2.txt"; exec sleep 3600`, "--"]`. Because `buildSession` appends `--session-id <uuid>` as the trailing args, inside `sh -c` the uuid lands at `$2`. Each spawned child writes a uniquely-named marker (`cwd-<its-uuid>.txt`) **into its own cwd** via a relative path — so the marker's *location* is proof of the spawn directory, and the per-uuid name prevents collisions with the bootstrap's marker. Assert by polling for the marker file's existence at the expected path (existence is robust against macOS `/tmp`→`/private/tmp` symlink rewriting; reading + comparing `pwd` content is optional and prone to that pitfall).

Scenarios (described, not pre-written):

- **`CreateIn` with explicit dir spawns there** — pass a fresh `t.TempDir()` (distinct from `tpl.WorkDir`) as `spawnDir`; poll until `<spawnDir>/cwd-<id>.txt` exists. Confirms AC-1.
- **`Create` (no dir) spawns in the template workdir** — set `tpl.WorkDir` to a dedicated `t.TempDir()`; create a session with plain `Create`; poll until `<tpl.WorkDir>/cwd-<id>.txt` exists (the per-uuid name distinguishes it from the bootstrap's marker in the same dir). Confirms AC-2 + AC-4's default leg.
- **`GetOrCreateIn` with explicit dir spawns there** — caller-supplied canonical UUID + `spawnDir = t.TempDir()`; poll until `<spawnDir>/cwd-<id>.txt` exists. Confirms the `GetOrCreate` seam threads identically.
- **(optional, cheap) `GetOrCreateIn` take-path ignores spawnDir** — pre-register a session, then `GetOrCreateIn` the same id with a different `spawnDir`; assert no marker appears in the second dir (the existing child keeps its workdir). Mirrors the documented label-drop. Include only if it stays within the S budget; the docstring contract is the primary guarantee.

Existing `pool_create_test.go` / `pool_get_or_create_test.go` already cover `Create`/`GetOrCreate` (no-dir) unchanged behaviour, satisfying AC-3 without new tests there.

Run `go test -race ./internal/sessions/...` and `go vet ./...`.

---

## Acceptance criteria mapping

| AC | Covered by |
|----|------------|
| Surface accepts optional per-session workdir; a session created with one spawns there | `CreateIn`/`GetOrCreateIn` + `buildSession` conditional; "explicit dir spawns there" tests |
| No workdir → spawns in shared template workdir, byte-for-byte as today | `Create`/`GetOrCreate` delegate with `""` → `buildSession` uses `tpl.WorkDir` unchanged; "default leg" test |
| Existing callers (`sessionMinter`, `GetOrCreate`, `sessions.new`, tests) compile + behave unchanged | Signatures of `Create`/`GetOrCreate` unchanged; delegators pass `""`; existing tests untouched |
| Unit test: explicit-dir spawns at dir; no-dir spawns at template workdir | `pool_spawndir_test.go` scenarios above |

---

## Out of scope

- **Validation / canonicalisation / trust / `$HOME`-containment** — #685 (which carries `security-sensitive`).
- **Registry persistence of the per-session workdir.** No session-reload path reconstructs live supervisors via `buildSession` today (`grep` for load/reload/recover on `*Pool` → none), so a custom spawn dir is a spawn-time input only and is not written to `sessions.json`. If a later slice needs the workdir to survive a daemon process restart, that is its own ticket.
- **Wiring an actual non-default caller** — #685/#686.

---

## Open questions

- None blocking. The `GetOrCreateIn` take-path-ignores-`spawnDir` semantics are settled by mirroring the existing label-drop; flagged here only so the developer documents it in the method's docstring.
