# Spec — #839: Deterministic bootstrap `--session-id` (retire `--continue`, remove startup adopt-by-mtime)

**Size:** S (PO sized S; architect confirms S). 3 production files, 2 new exported symbols, **zero net-new e2e call sites** — the seed cascade already landed with #861 (verified on the merged tree; see § E2e alignment). See § Size note.

**Label:** `security-sensitive` → the architect security-review pass is at the end of this spec (verdict: PASS).

## Context

The interactive **bootstrap** claude is spawned with `--continue` (via `supervisor.Config.ResumeLast`, forwarded through the bootstrap `SessionConfig` at `pool.go:405`). `--continue` resumes the **most-recent** session in the working directory. When a second claude writes more recently into the shared `~/.claude/projects/<encoded-cwd>/` dir, a supervisor restart resumes the **wrong** conversation. The same root cause drives `reconcileBootstrapOnNew` (`pool.go:512`): with no deterministic id, on startup the daemon **adopts** a session id from the shared dir by mtime and can adopt a foreign transcript.

Fix: spawn the bootstrap with an explicit `--session-id <bootstrapID>` resolved from the daemon's own persisted id, on first start and every restart, and remove the startup adopt-by-mtime. The scaffolding exists: `bootstrapID` is already minted + persisted (`pool.go:348-384`); the `--session-id` spawn shape is already used by `buildSession` (`pool.go:1173`).

**Design tension (resolved).** `--continue` was chosen so a `/clear` inside claude (which rotates the on-disk id, #830/#831) is picked up on the next restart rather than reattaching to the orphaned pre-`/clear` session. A fixed `--session-id` must reconcile with `/clear` rotation. This falls out of **spawn-time** id resolution: the rotation watcher already fires `onRotate → RotateID`, which updates `p.bootstrap` **and persists it**; the supervisor already re-reads its argv each iteration (`liveArgs()`). Resolving `--session-id` from the *current* `p.bootstrap` at each spawn therefore reconciles `/clear` with **no** new watcher, **no** `onRotate` wiring, and **no** supervisor argv-swap seam.

## Design

### Seam choice: pull (spawn-time provider), not push (argv swap on rotate)

Two families reconcile the fixed id with `/clear`:

- **Push** — wire `onRotate` (for the bootstrap) to swap the supervisor's live argv when `RotateID` fires. Needs a new supervisor "swap-args-without-restart" method (`Restart` induces a child kill we must not trigger — the post-`/clear` child is happily running the rotated session) **plus** an `onRotate` branch. This is the surface the ticket's split-escape warns about.
- **Pull** *(chosen)* — the supervisor resolves `--session-id` from a provider function at each spawn; the provider reads the current `p.bootstrap`. `RotateID` updating `p.bootstrap` is then picked up automatically on the next spawn. No `onRotate` wiring, no watcher change, no argv-swap method.

Pull is strictly lower surface and reuses the two existing load-bearing seams (`liveArgs()` re-read + `RotateID`-persists-`p.bootstrap`).

### 1. `internal/supervisor/supervisor.go` — `ResolveSessionID` provider

Add one `Config` field (contract, not body):

```go
// ResolveSessionID, when non-nil, is called at the start of every spawn.
// A non-empty return appends "--session-id <id>" to the claude args and
// suppresses --continue (the two are mutually exclusive). Resolved fresh
// each spawn so a /clear id rotation is picked up on the next restart.
// Nil preserves the ResumeLast/--continue behaviour (per-caller sessions,
// foreground tests).
ResolveSessionID func() string
```

Extend `buildClaudeArgs` (keep it a pure, table-tested function) to take the resolved id:

- **Signature:** `buildClaudeArgs(claudeArgs []string, firstRun, continueLast bool, sessionID string) []string`.
- **Behavior:** when `sessionID != ""` → append `"--session-id", sessionID`; **do not** prepend `--continue`. When `sessionID == ""` → today's logic verbatim (prepend `--continue` iff `!firstRun && continueLast`). Never mutate the input slice (the existing aliasing guarantee holds).
- **Position:** append `--session-id` at the end of `claudeArgs`. Order is immaterial to claude (flags are order-independent); the buildSession path proves `--session-id` + settings coexist.

Call site (`Run`, was line 677):

```go
sessionID := ""
if s.cfg.ResolveSessionID != nil {
    sessionID = s.cfg.ResolveSessionID()
}
args := buildClaudeArgs(s.liveArgs(), firstRun, s.cfg.ResumeLast, sessionID)
```

The resolver is invoked **every iteration** (each spawn), reading the live id under the pool lock — this is the spawn-time resolution AC-4 rides.

`ResumeLast` and `buildClaudeArgs`'s `continueLast` param stay for the per-caller / foreground / test paths that still use `--continue`. Only the bootstrap opts into the provider.

### 2. `internal/sessions/pool.go` — wire the bootstrap provider + `BootstrapID()`

**Bootstrap wiring** (the `supCfg` block, ~402-448):
- Set `supCfg.ResumeLast = false` (the bootstrap no longer maps `ResumeLast` to a `--continue` spawn — AC-1). `cfg.Bootstrap.ResumeLast` becomes vestigial for the bootstrap; leave the field/flag in place (removing `-pyry-resume` is a separate cascade — see § Out of scope).
- Set `supCfg.ResolveSessionID = func() string { return string(p.BootstrapID()) }`.
- `p` is constructed **after** `supCfg` is built and after `supervisor.New`. Use the **late-bind** pattern already in this function for `bootstrapSup`: forward-declare `var p *Pool` before `supCfg`, assign it at the existing `&Pool{...}` literal (change `p :=` to `p =`). The resolver is only *called* at spawn time (`Run`), long after `p` is assigned — identical timing guarantee to `pidFn`.

**New accessor** — mirror `DefaultSettings` (RLock, resolve `p.bootstrap` fresh):

```go
// BootstrapID returns the pool's current bootstrap session id under p.mu.
// Resolves p.bootstrap fresh (mirroring Default/DefaultSettings) so it stays
// correct across a /clear rotation — RotateID flips p.bootstrap under the write
// lock. The bootstrap supervisor's ResolveSessionID provider reads THIS (a
// p.mu-guarded value), NOT Default().ID()/sess.id, keeping RotateID's
// "sess.id has no concurrent reader" invariant intact (§ Concurrency).
func (p *Pool) BootstrapID() SessionID
```

Reading `p.bootstrap` (not `sess.id`) is the load-bearing race-safety decision — see § Concurrency.

**Remove the adopt-by-mtime call** at `pool.go:512`:
```go
if err := reconcileBootstrapOnNew(p, cfg.ClaudeSessionsDir, cfg.Logger); err != nil { ... }
```
Delete the whole `if` block. This also removes one fatal-at-startup path (a shared-dir read error at construction no longer aborts startup) — a net simplification.

### 3. `internal/sessions/reconcile.go` — delete `reconcileBootstrapOnNew`

Delete `reconcileBootstrapOnNew` (247-277). **Keep** `mostRecentJSONL`, `encodeWorkdir`, `DefaultClaudeSessionsDir`, `newTranscriptResolver`, `newProbePreferredTranscriptResolver` (all still live — #838). Prune imports that become unused (`errors`, `io/fs`, and `log/slog` if nothing else in the file uses it — verify with `go build` / `staticcheck`).

### Data flow after the change

```
supervisor.Run (each spawn)
  └─ ResolveSessionID() ─(RLock)→ Pool.bootstrap ──> "--session-id <id>"   (no --continue)

/clear inside claude (same PID)
  └─ watcher CREATE <newID>.jsonl → probe matches child → RotateID(old,new)
        └─ p.bootstrap = new; saveLocked()          (child keeps running as `new`)

  … later supervisor restart (crash) …
  └─ ResolveSessionID() now returns `new` → "--session-id <new>"  (resumes post-/clear, AC-4)

whole-daemon restart
  └─ pickBootstrap(reg) → persisted `new` → p.bootstrap = new → "--session-id <new>"
```

## Watcher — no `RegisterAllocatedUUID` needed

When claude first creates `<bootstrapID>.jsonl`, the CREATE reaches `handleCreate`. Because `<bootstrapID>` is now a **current** session id (`p.bootstrap`), the guard at `watcher.go:165-169` (`ref.ID == stem` → return) suppresses any misfire — the watcher will not treat the bootstrap's own first file as a `/clear`. `RegisterAllocatedUUID(bootstrapID)` is available as belt-and-suspenders but is **not added**: the guard is already deterministic code covering this exact case (Evidence-Based Fix Selection — no defense for an unobserved, structurally-precluded failure).

## Concurrency model

- The provider runs on the supervisor's `Run` goroutine at each spawn and calls `Pool.BootstrapID()`, which takes `p.mu.RLock()`. `RotateID` writes `p.bootstrap` under `p.mu.Lock()`. The read of `p.bootstrap` is therefore fully synchronized — no torn read, `-race`-clean.
- The provider reads **`p.bootstrap`** (a `p.mu`-guarded `SessionID` value), deliberately **not** `p.Default().ID()` / `sess.id`. `RotateID` mutates `sess.id` *without* `Session.lcMu` under the documented invariant "no concurrent reader of `sess.id` exists." Routing the spawn-time read through `p.bootstrap` (not `sess.id`) **preserves** that invariant — no new `sess.id` reader is introduced.
- Eventual-consistency by design: if a `/clear` rotates `p.bootstrap` between a resolve and the next spawn, the *next* restart picks up the new id. There is no requirement (or attempt) to hot-swap the running child's id — the running child already rotated in-place.

## Error handling

- Provider returns the current bootstrap id, non-empty in all real paths (minted at `New`, or a `uuidStemPattern`-validated rotated stem). Defensive: the supervisor appends `--session-id` only when the resolved id is non-empty; an empty return (never expected) spawns without `--session-id` and without `--continue` (fresh session) rather than passing a malformed flag. No new error sentinel.
- Removing `reconcileBootstrapOnNew` removes its `fmt.Errorf("sessions: reconcile bootstrap: %w", err)` fatal-at-startup branch. One fewer startup failure mode.

## Testing strategy

### New unit tests — `internal/sessions` (new file, e.g. `pool_bootstrap_sessionid_test.go`)

Drive `Pool.New` / `RotateID` / `BootstrapID` directly (no live claude). Scenarios (bullets, not code):

- **AC-2/AC-3 — no foreign adoption.** Construct a pool with `ClaudeSessionsDir` pointing at a temp dir that contains a **newer** foreign `<foreign-uuid>.jsonl` (mtime after any daemon file), plus a warm-start registry pinning a known bootstrap id (or cold-start and capture the minted id). Assert `pool.BootstrapID()` equals the daemon's own id, **not** the foreign uuid — proving adopt-by-mtime is gone. (Pre-change, `reconcileBootstrapOnNew` would have rotated to the foreign uuid.)
- **AC-4 — `/clear` reconcile via the rotation seam.** Construct a pool; capture `id0 := pool.BootstrapID()`. Call `pool.RotateID(id0, id1)` (the exact seam the watcher drives). Assert `pool.BootstrapID() == id1` — i.e. the next spawn's `ResolveSessionID` resolves the rotated id, so the next `--session-id` is `id1`, not `id0`.
- **AC-5 — stable, not re-minted.** Assert `BootstrapID()` is invariant across repeated reads with no `RotateID`; and that a warm-start `New` (registry present) reuses the persisted id (no re-mint). It changes only via the AC-4 `RotateID` path.

**Do not** add an argv-introspection seam to the supervisor for these. The `ResolveSessionID` provider's *source* is `BootstrapID()`; asserting `BootstrapID()` here + the `buildClaudeArgs` argv-shape test below together cover "the spawned argv is `--session-id <bootstrapID>`."

### `internal/supervisor/args_test.go` — extend the `buildClaudeArgs` table

- `sessionID != ""` → output contains `--session-id <id>` and **no** `--continue`, even with `firstRun=false, continueLast=true` (session-id wins; mutual exclusion).
- `sessionID != ""` on `firstRun=true` → still `--session-id <id>` (deterministic from the first spawn — AC-1).
- `sessionID == ""` → every existing row unchanged (regression: `--continue` prepended iff `!firstRun && continueLast`).
- Input-slice-not-mutated assertion extended to the new param.

### e2e alignment (see § E2e) — run `go test -tags e2e ./internal/e2e/...`

## E2e alignment — already landed by #861 (verify, do NOT re-migrate)

Removing adopt-by-mtime would, on its own, break the ~11 e2e tests that relied on it: each pre-created `<initialUUID>.jsonl` (or set `PYRY_FAKE_CLAUDE_INITIAL_UUID`) so `reconcileBootstrapOnNew` adopted `initialUUID` as the bootstrap id, which `seedBoundConversation(..., initialUUID)` / `waitForBootstrapID(..., initialUUID)` then depended on. **#861 (merged) already absorbed this cascade** — it added `seedBootstrapRegistry` and wired it into every spawn path, warm-starting the daemon with `p.bootstrap == initialUUID` independent of adopt-by-mtime. The seeding this ticket needs is therefore **already in the tree**; the developer's e2e job is to *verify*, not to build.

Verified on the current (post-#861-merge) tree — do **not** re-create or re-wire any of this:

- `seedBootstrapRegistry(t, home, bootstrapUUID)` already exists at `internal/e2e/harness.go:399` (raw-JSON `sessions.json` writer, exactly the shape the old draft of this section described).
- `StartRotation` (`harness.go:273`) and `StartRotationWithRelay` (`harness.go:325`) call it internally before spawn — so every test spawning through them is seeded automatically: `rotation_test.go`, `fakeclaude_test.go`, `relay_roundtrip_test.go`, `relay_assistant_turn_test.go`, `relay_send_message_test.go`, `relay_two_phone_structured_test.go`, `relay_v2_queue_drain_test.go`, `relay_v2_interrupt_test.go`, `relay_v2_two_head_modal_test.go`, and — **correcting an earlier draft of this spec** — `relay_v2_modal_answer_test.go`, which spawns via `StartRotationWithRelay` and needs **no** separate seed call.
- Non-rotation spawns already carry an explicit seed: `respawn_after_eviction_test.go:255`, `relay_v2_dequeue_test.go:84`, `per_conversation_eviction_test.go:246` (`startPerConvHarness`).

Every `initialUUID` daemon-spawn site on the current tree already resolves to a seed. Grep `seedBootstrapRegistry(` / `initialUUID` to re-confirm before touching any test file. Existing inline `<initialUUID>.jsonl` pre-creations stay (now harmless — they match the warm-started id).

Why the seeding aligns: the daemon warm-starts with `p.bootstrap == initialUUID` and spawns fakeclaude with `--session-id initialUUID`; fakeclaude keys its jsonl off `PYRY_FAKE_CLAUDE_INITIAL_UUID == initialUUID` (env, unchanged), so the on-disk file and the daemon's id agree by construction, and the first-CREATE watcher guard (`watcher.go:165`) suppresses any misfire.

**Developer e2e task is therefore only:** after the production change (remove adopt-by-mtime), run `go test -tags e2e ./internal/e2e/...` and fix any residual assertion that assumed cold-start/adopt semantics rather than the seeded `initialUUID` (expected: none — the seed makes on-disk file and daemon id agree by construction — but the `-tags e2e` run is ground truth). Add a `seedBootstrapRegistry` call **only** if the run surfaces a spawn site the grep missed.

## Size note

- Production files (new/modified `*.go`, excluding tests): `supervisor.go`, `pool.go`, `reconcile.go` = **3** (< 5 gate).
- New exported symbols: `Config.ResolveSessionID` field, `Pool.BootstrapID` method = **2** (< 5).
- Edit fan-out: **one** production code call site removed (`pool.go:512`). The e2e seed cascade (~6 call sites) is **already in the tree from #861** — this ticket adds **zero** new e2e call sites; the developer removes adopt-by-mtime, adds the spawn-time provider + `BootstrapID()`, and runs `-tags e2e` to confirm the already-migrated suite tolerates the removal. Well under the 10-call-site red line. The ticket's proposed A/B split does **not** lower cost (child A still removes adopt-by-mtime; the `/clear` reconciliation adds **zero** extra production seams under the pull design). Confirmed S — comfortably at the small end (the test-fixture bulk that originally justified sizing here is done).

## Out of scope

- Removing `-pyry-resume` / `SessionConfig.ResumeLast` / `supervisor.Config.ResumeLast` wholesale. They stay for per-caller/foreground/test `--continue` paths; only the bootstrap opts out. A dead-flag cleanup is a separate ticket (touches `main.go` help text, `args_test.go`, and 3 e2e `-pyry-resume=false` sites).
- Resolver simplification (`newTranscriptResolver` / probe machinery resolving by-id now that the filename is known) — owned by #838; keep the existing resolvers working.
- `newProbePreferredTranscriptResolver`'s foreign-adoption behaviour (#838).

## Open questions

- **Empty-id defense placement.** The spec defends an empty provider return in the supervisor (skip `--session-id`). Equivalent to defending in the provider (never return ""). Supervisor-side is chosen so `buildClaudeArgs` stays the single arg-shape authority. Developer may move it if cleaner; behaviour must match.
- **e2e residual assertions.** Confidence is high that #861's already-in-tree `seedBootstrapRegistry` wiring is the whole e2e fix (no new seed calls needed), but the `-tags e2e` run is the ground truth; if a specific test asserts on the *cold-start* minted id (rather than `initialUUID`), adjust that test.

---

## Security review (label: `security-sensitive`)

`agents/architect/security-review.md` is not present in this worktree (as noted in specs #210/#487); the pass is run inline over the standard adversarial categories.

- **[1 Trust boundaries]** No MUST-FIX — **net improvement.** The change *removes* a trust-crossing: adopt-by-mtime read another process's `<uuid>.jsonl` filename from the **shared** `~/.claude/projects/<encoded-cwd>/` dir and adopted it as the daemon's own session id — a confused-deputy vector (a co-located second claude, or anything that can write a `<uuid>.jsonl` there, could steer which conversation the daemon resumes on restart). The new argv value comes only from the daemon's own `p.bootstrap` — minted (`NewID`, crypto/rand UUIDv4) or a `/clear`-rotated stem the watcher already validated. No foreign on-disk state reaches the spawn argv.
- **[2 Subprocess / argv injection]** No MUST-FIX. The `--session-id` value is always a canonical 36-char `uuidStemPattern` UUID: `NewID()` output, or a rotated stem gated by `uuidStemPattern.MatchString` at `watcher.go:150` before `RotateID`. No shell metacharacters and no `-`-leading token, so no flag-injection; spawn is `exec` (no shell), identical to the existing `buildSession --session-id` path.
- **[3 Input validation]** No findings. `p.bootstrap` is settable only by `pickBootstrap`/`NewID` (New) and `RotateID` (watcher, uuid-gated). No external/untrusted caller can set an arbitrary bootstrap id.
- **[4 Error messages / logs]** No findings. `Run`'s `"spawning claude", "args"` log now includes `--session-id <uuid>`; a session UUID is a disk filename, not a secret, and is already logged by the reconcile/rotation paths. No token/key/payload is logged.
- **[5 Concurrency / TOCTOU]** No MUST-FIX. Provider read of `p.bootstrap` is `p.mu.RLock`-guarded against `RotateID`'s `p.mu.Lock` write (`-race`-clean); the deliberate choice to read `p.bootstrap` (not `sess.id`) preserves `RotateID`'s no-concurrent-`sess.id`-reader invariant. The resolve→exec window is intended eventual-consistency (next restart picks up a mid-flight rotation), not a vulnerability.
- **[6 DoS / escalation]** No findings. No new unbounded loop, fd/dir handle, or privilege change; removing the construction-time shared-dir scan slightly *reduces* startup surface.
- **[7 Isolation / confused-deputy — the ticket's core]** No MUST-FIX — **the change is the mitigation.** A supervisor restart now deterministically resumes the daemon's own conversation, closing the #828-family bootstrap isolation gap.

**Verdict: PASS.** No MUST-FIX findings; the change is a net trust-boundary and isolation improvement with no new untrusted-input-to-argv path.
