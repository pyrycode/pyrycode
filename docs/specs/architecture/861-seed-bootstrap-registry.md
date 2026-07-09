# Spec #861 — Centralize e2e bootstrap-id seeding behind `seedBootstrapRegistry`

**Ticket:** [#861](https://github.com/pyrycode/pyrycode/issues/861) · size **S** · test-infra only, **no production behavior change**
**Prep for:** [#839](https://github.com/pyrycode/pyrycode/issues/839) (deterministic bootstrap `--session-id`, which removes adopt-by-mtime)

---

## Files to read first

- `internal/e2e/harness.go:362-381` — `seedBoundConversation`: the **exact idiom the new helper mirrors** (raw-JSON `os.WriteFile` into `<home>/.pyry/test/`, fixed timestamp literal). Same file, same build tag.
- `internal/e2e/harness.go:253-360` — `StartRotation` + `StartRotationWithRelay`: the two shared constructors that set `PYRY_FAKE_CLAUDE_INITIAL_UUID` and receive `home`+`initialUUID`. Seed call goes here.
- `internal/e2e/harness.go:1` — build tag is `//go:build e2e || e2e_install`. **Load-bearing constraint** (see § Build-tag hazard).
- `internal/e2e/restart_test.go:13-49,117-126` — `registryFile`/`registryEntry`/`writeRegistry`/`newRegistryHome`: the canonical warm-seed-`sessions.json` precedent. Note its build tag is `//go:build e2e` **only** — these types are **not** available to `harness.go` under `e2e_install`.
- `internal/sessions/pool.go:329-384` — `Pool.New` warm-start branch: `pickBootstrap(reg)` → `bootstrapID = entry.ID`. This is what a seeded registry drives.
- `internal/sessions/reconcile.go:255-277` — `reconcileBootstrapOnNew`: the adopt-by-mtime path that **stays in production**. The `if mostRecent == current { return nil }` guard is why seeding makes it a confirming no-op.
- `internal/sessions/session.go:457-470` — `runActive` arms `time.NewTimer(idleTimeout)` fresh; eviction timing does **not** read the persisted `last_active_at`. This is why a fixed-literal seed timestamp is inert (no startup-eviction hazard in the idle=2s tests).
- `internal/e2e/rotation_test.go:41-90` — representative migrated body; its `post.LastActiveAt.After(pre.LastActiveAt)` assertion is the one timestamp-sensitive check (satisfied by a fixed-past seed value).
- `internal/e2e/per_conversation_eviction_test.go:234-270` — `startPerConvHarness` local harness (sets `INITIAL_UUID` at :261).
- `internal/e2e/respawn_after_eviction_test.go:246-270` — `startEvictionHarness` local harness (sets `INITIAL_UUID` at :268).
- `internal/e2e/relay_v2_dequeue_test.go:48-87` — the **one** inline site: `/bin/sleep` child + inline `<initialUUID>.jsonl` pre-create + `StartInWithEnv`. No shared constructor, so it needs an explicit seed call.

---

## Context

Roughly 15 e2e tests establish the daemon's bootstrap session id by (a) pre-creating `<initialUUID>.jsonl` in the daemon's computed sessions dir and (b) relying on `reconcileBootstrapOnNew` (adopt-by-mtime) to rotate the freshly-minted cold bootstrap id to `initialUUID` at startup. #839 removes adopt-by-mtime and spawns the bootstrap with a deterministic `--session-id`, which would break all of them at once — interleaved with the behavior change and a red-suite triage loop, that is what timed out the developer stage on 2026-07-08.

This ticket does the mechanical prep with **zero production change**: introduce one warm-start helper that seeds the bootstrap id explicitly, and route every dependent test through it. The suite stays green throughout because seeding converges on exactly the same post-startup state adopt-by-mtime produces today. When #839 later deletes `reconcileBootstrapOnNew`, these tests keep passing because their id no longer comes from the scan.

**Key discovery that shrinks this ticket:** every dependent test reaches adopt-by-mtime through one of **four harness constructors** that already own `home` + `initialUUID`, plus **one** inline `StartInWithEnv` site. Seeding inside the constructors migrates ~13 test files without touching their bodies. This is the explicit ticket goal ("#839 touches a handful of call sites rather than ~15 scattered test files").

---

## Design

### The helper (harness.go)

```go
// seedBootstrapRegistry writes sessions.json for the "test" instance with a
// single bootstrap entry whose id is bootstrapUUID, so Pool.New warm-starts the
// bootstrap session at that id WITHOUT depending on the startup adopt-by-mtime
// scan (reconcileBootstrapOnNew). Mirrors seedBoundConversation: raw JSON,
// fixed timestamp, os.WriteFile into <home>/.pyry/test/.
func seedBootstrapRegistry(t *testing.T, home, bootstrapUUID string)
```

Behavior contract (the JSON *is* the contract — keep it byte-faithful to `registryFile`):

- `os.MkdirAll(<home>/.pyry/test, 0o700)` first — the seed runs **before** the daemon (which would otherwise create the dir), and not every caller pre-creates it.
- Write `<home>/.pyry/test/sessions.json`, mode `0o600`, with exactly:
  ```json
  {"version":1,"sessions":[{"id":"<bootstrapUUID>","label":"","created_at":"2026-01-01T00:00:00Z","last_active_at":"2026-01-01T00:00:00Z","bootstrap":true,"lifecycle_state":"active"}]}
  ```
- Fatal (`t.Fatalf`) on mkdir/write error, matching `seedBoundConversation`.

Rationale for each choice:
- **Raw JSON string, not `writeRegistry`/`registryFile`.** See § Build-tag hazard — those types are `e2e`-only and `harness.go` also compiles under `e2e_install`.
- **Fixed past timestamp `2026-01-01T00:00:00Z`.** Inert for eviction timing (`runActive` uses a fresh fixed-duration timer, session.go:468-470), and makes rotation_test's `post.After(pre)` robust (pre = fixed past, post = real rotation time). Mirrors `seedBoundConversation`'s literal.
- **`lifecycle_state:"active"`** is ignored for the bootstrap by `Pool.New` (pool.go:365-375 forces `stateActive`); included only for on-disk shape fidelity with a running bootstrap. Harmless.

### Seed insertion points (5 call sites)

| # | Location | File (build tag) | Change |
|---|----------|------------------|--------|
| 1 | `StartRotation` — before `spawnWith` | `harness.go` (`e2e‖e2e_install`) | add `seedBootstrapRegistry(t, home, initialUUID)` |
| 2 | `StartRotationWithRelay` — before `spawnWith` | `harness.go` | same |
| 3 | `startPerConvHarness` — before its `spawnWith` | `per_conversation_eviction_test.go` (`e2e`) | same |
| 4 | `startEvictionHarness` — before its `spawnWith` | `respawn_after_eviction_test.go` (`e2e`) | same |
| 5 | inline, immediately before `StartInWithEnv(...)` at :83 | `relay_v2_dequeue_test.go` (`e2e`) | same (`home`, `initialUUID` are in scope) |

Constructors 1–4 cover: `rotation_test`, `fakeclaude_test` (via 1); all 8 `relay_*`/`relay_v2_*` growth-confirm tests (via 2); the 2 per-conversation-eviction tests (via 3); the respawn test (via 4). Site 5 covers `relay_v2_dequeue` (the only test that reaches adopt-by-mtime without a shared rotation constructor).

Place the seed **before** the `spawnWith`/`StartInWithEnv` that launches the daemon — `Pool.New` must read the registry at startup. Position relative to the `sessionsDir` `MkdirAll` is irrelevant (the seed writes under `.pyry/test`, not the sessions dir).

### What does NOT change

- **`reconcileBootstrapOnNew`, `mostRecentJSONL`, `Pool`, `cmd/pyry` — untouched.** Adopt-by-mtime still runs in production exactly as before. `mostRecentJSONL` also backs the #838 growth-confirm resolver; out of scope.
- **The `<initialUUID>.jsonl` pre-creates stay.** In the growth-confirm relay tests they are the delivery-confirm baseline (#668/#838) and are still required. In the reconcile-only tests (rotation, per-conv, dequeue) they become a redundant confirming no-op — leave them. This keeps the diff a single uniform addition (one seed call per site) and avoids any startup-timing surprise. **Do not** delete jsonl pre-creates as part of this ticket (scope discipline; not uniform since relay tests must keep theirs).
- **Test bodies of the ~13 constructor-routed files** — untouched.

---

## Correctness invariant (why the suite stays green in the pre-#839 tree)

For every migrated site, after `seedBootstrapRegistry(home, initialUUID)`:

1. `Pool.New` loads the registry; `pickBootstrap` returns the seeded entry; `bootstrapID = initialUUID` — warm start (pool.go:356-364).
2. `reconcileBootstrapOnNew` still runs. `mostRecent := mostRecentJSONL(sessionsDir)` is either:
   - `initialUUID` (test pre-created `<initialUUID>.jsonl`) → `mostRecent == current` → `return nil`; **or**
   - `""` (no jsonl yet) → `return nil`.
   Either way it does **not** rotate. Bootstrap id stays `initialUUID`.
3. Net post-startup state (`bootstrap id == initialUUID`) is identical to today's cold-start-then-reconcile path — only the mechanism differs. No external observer sees a difference (reconcile runs synchronously inside `Pool.New`, before the control socket listens).

**Precondition the developer must confirm per migrated site:** the daemon's computed sessions dir never contains a `<uuid>.jsonl` whose stem differs from `initialUUID` at startup — otherwise reconcile would rotate away from the seed. True today: the only pre-created stem is `initialUUID`, and fakeclaude writes only `<initialUUID>.jsonl` (keyed off `PYRY_FAKE_CLAUDE_INITIAL_UUID`). No migrated test introduces a foreign stem.

**Idle-eviction non-interaction:** the seeded `last_active_at` is inert because `runActive` arms `time.NewTimer(idleTimeout)` fresh on entering the active state (session.go:468-470); the eviction decision never computes `now − last_active_at`. So the fixed-past literal cannot trigger startup eviction in the idle=2s tests (`per_conversation_eviction`, `respawn_after_eviction`), and their PID-capture/eviction timing is unchanged.

---

## Build-tag hazard (must-read for the developer)

`harness.go` compiles under **`e2e || e2e_install`**. `registryFile`, `registryEntry`, and `writeRegistry` are defined in `restart_test.go` under **`e2e` only**. If `seedBootstrapRegistry` (in `harness.go`) referenced those types, the `e2e_install` build would fail with undefined symbols.

→ The helper **must** write a raw JSON string via `os.WriteFile`, exactly like its sibling `seedBoundConversation` in the same file. Do not import or construct `registryFile`.

Verify after the change: `go build -tags e2e_install ./internal/e2e/...` compiles (in addition to the `e2e` suite run).

---

## Testing strategy

No new test functions — this is test-infra. The proof is the existing suite passing before and after.

- **Before:** run the full `-tags e2e` suite on `main` to record the green baseline (and note any known flakes — see the project's known-test-flakes memory; realclaude SIGTERM / wssclient `-race` re-run isolated). `make check` for vet/staticcheck/unit.
- **After each edit family**, re-run `go test -tags e2e ./internal/e2e/...`. The high-signal files: `rotation_test` (bootstrap id + `post.After(pre)`), `per_conversation_eviction` + `respawn_after_eviction` (idle=2s timing), the `relay_v2_*` growth-confirm tests, and `relay_v2_dequeue` (the inline site).
- **Build both tags:** `go build -tags e2e ./internal/e2e/...` **and** `go build -tags e2e_install ./internal/e2e/...` (the build-tag hazard gate).
- **No test skipped or disabled** (AC4). If a migrated test goes red, it is a real interaction bug in the seed — diagnose against the Correctness invariant (almost certainly a foreign-stem or ordering violation), do not paper over it.

Acceptance mapping:
- AC1 (single helper, one place) → `seedBootstrapRegistry` in `harness.go`.
- AC2 (every dependent test migrated; none relies on the mtime scan for its id) → the id is established by the seed at all 5 sites; reconcile is a verified no-op (Correctness invariant §2).
- AC3 (no production change; reconcile + `mostRecentJSONL` stay) → zero edits outside `internal/e2e/`.
- AC4 (`make check` + `-tags e2e` green before & after, nothing skipped) → testing strategy above.

---

## Open questions

- **`fakeclaude_test.go`** routes through `StartRotation` and so is seeded automatically. It is a test *of* the fakeclaude binary's jsonl output, orthogonal to the bootstrap id — confirm on the suite run that seeding leaves its assertions meaningful (expected: yes, no change). No action anticipated.
- **Redundant jsonl pre-creates** in the reconcile-only tests (rotation, per-conv, dequeue) are left in place deliberately. A follow-up (naturally #839, which removes reconcile) may prune them; doing so here would break diff uniformity and risk startup timing. Out of scope.
