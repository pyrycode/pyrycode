# Spec — #840: persist a partial per-session model/effort/YOLO change (Pool settings-update seam)

**Size:** S (PO-sized S; architect confirms — 2 production files, 1 new exported type, one ~20-line method mirroring `Pool.Rename`, one reject branch, ~40 production LOC + ~200 test LOC. Additive: adding a method to `*Pool` cannot break the structural `Sessioner` interface, so zero consumer fan-out. No red line tripped.)

**Label:** `security-sensitive` — the update can carry YOLO (`--dangerously-skip-permissions`). The mandatory security-review pass is appended at the end (§ Security review).

## Context

#833 shipped per-session `Model` / `Effort` / `YOLO` settings: they live on `Session.settings` (`SessionSettings`, `internal/sessions/session.go`), persist through `saveLocked` into `~/.pyry/<name>/sessions.json`, and are applied to the claude spawn argv by `claudeSettingsArgs` at both spawn sites (`New` for the bootstrap session, `buildSession` for minted). But #833 left the field **immutable post-construction** — set once at session creation, never changed. Its own doc comment anticipates this ticket: *"a future wire setter that mutates it must take `Pool.mu` (write) and re-persist, exactly as `Pool.Rename` does for label."*

This ticket adds exactly that persistence primitive: **a `Pool` method that merges a partial model/effort/YOLO change into an identified session's stored settings and re-persists.** It is the single seam the client-facing v2 settings verb (#841, split from the same parent) calls. It is **not** client-facing — validating untrusted values is #841's job (the wire boundary). Making a *running* session pick the change up immediately is a separate sibling (#842); here the new values reach claude through #833's already-shipped spawn path on the session's **next** spawn (for the bootstrap session, the next daemon restart — the reload path #833 proved).

`Pool.Rename` is the concrete precedent — same shape, same locking, same sentinel — with a partial merge over `SessionSettings` substituted for the single label swap.

## Files to read first

- `internal/sessions/pool.go:552-566` — **`Pool.Rename`**: the exact template. Lock `Pool.mu` (write) → lookup → no-op short-circuit → save previous → mutate → `saveLocked` → roll back on save error → return. **What to extract:** copy this control flow verbatim, substituting a `SessionSettings` merge for the label swap.
- `internal/sessions/pool.go:33` — **`ErrSessionNotFound`** sentinel. **What to extract:** reuse it for the unknown-id branch (AC4); do NOT introduce a new error type.
- `internal/sessions/session.go:61-95` — **`SessionSettings`** value type + **`claudeSettingsArgs`** helper. **What to extract:** the three fields to merge over; `SessionSettings` is a comparable struct (string/string/bool), so a `==` no-op check works. `claudeSettingsArgs` is the downstream consumer that turns the persisted settings into argv (relevant only for AC5's round-trip proof — no change here).
- `internal/sessions/session.go:106-120` — **`Session.settings`** field + its "immutable post-New … #826b's setter must revisit synchronization" doc comment. **What to extract:** `settings` is a **`Pool.mu`-guarded** field (same discipline as `label`, NOT `lcMu`-guarded). This comment (and the type-level comment at 61-69) become stale this ticket — see § Design step 3.
- `internal/sessions/pool.go:1204-1245` — **`saveLocked`**: serializes `s.settings.{Model,Effort,YOLO}` into `registryEntry` under the held `Pool.mu`. The inline comment at line 1223 ("`s.settings` is immutable post-New") becomes stale. **What to extract:** the persist path the setter reuses (no change to `saveLocked`'s body); the comment to correct.
- `internal/sessions/pool_rename_test.go:14-90` — **`TestPool_Rename_RoundTrip` / `TestPool_Rename_EmptyClears`** + `helperPoolPersistent` (`pool_test.go:149`). **What to extract:** the unit-test idiom for a `Pool.mu`-guarded persisted setter — `helperPoolPersistent` → mutate via the method → `loadRegistry` → assert on-disk. Mirror this for the merge / unknown-id / no-op / persistence cases.
- `internal/sessions/pool_settings_test.go:124-157` — **`TestPool_BootstrapWarmStart_AppliesSettingsToArgv`** + the `helperPoolArgvRecorder` / `runPoolInBackground` / `waitArgv` recorder harness (top of file, lines 17-122). **What to extract:** the end-to-end spawn-argv idiom for AC5 — a template child that records its own appended argv.
- `internal/sessions/pool_settings_test.go:265-314` — **`TestPool_BootstrapSettings_SurviveNewPersistReload`**. **What to extract:** the persist→`loadRegistry`→assert pattern; AC5's persistence half plus the "second `New` reads it back" restart proof reuse this exactly.
- `docs/lessons.md:79` — **§ "Lock order with callback into the host"**: the documented `Pool.mu → Session.lcMu` order. **What to extract:** `saveLocked` re-acquires each session's `lcMu` internally; the setter must NOT hold any `lcMu` across `saveLocked` (it holds none — `settings` is `Pool.mu`-guarded). Same as `Rename`.
- `docs/knowledge/decisions/030-plain-bool-failsafe-persisted-flag.md` — the plain-`bool` YOLO fail-safe rationale from #833. **What to extract:** the persisted-layer fail-safe (absence/corruption → OFF) this ticket must not weaken; the presence contract here is the setter-layer complement (§ Security review).

## Design

Two moving parts, both in `internal/sessions`. No new file for production code — the type goes beside `SessionSettings` in `session.go`, the method beside `Rename` in `pool.go`.

### 1. Presence-distinguishing update type — `SettingsUpdate` (`session.go`, next to `SessionSettings`)

The AC requires changing *only the fields the caller marks present*, and this presence contract is **shared with #841** (which decodes the wire payload into it). The Go idiom for a partial/patch struct is a pointer per field: `nil` = "omitted, leave stored value"; non-nil = "set to this value" — which makes an empty-string model/effort and a `false` YOLO distinguishable from "omitted" (the AC's exact requirement).

New **exported** value type:

```go
// SettingsUpdate is a partial change to a session's SessionSettings. A nil
// field means "leave the stored value untouched"; a non-nil field sets that
// value (including "" for Model/Effort and false for YOLO — distinguishable
// from omitted). It is the presence contract shared with the v2 settings
// verb (#841), which decodes the wire payload into it.
type SettingsUpdate struct {
    Model  *string
    Effort *string
    YOLO   *bool
}
```

- `*bool` for YOLO is the security-relevant choice: an omitted `YOLO` (`nil`) can never flip bypass on (AC3), and it is distinct from an explicit `YOLO: &false` (turn bypass off).
- No JSON tags needed here — #840 never (de)serializes this type; #841 owns wire decoding.

### 2. The setter — `Pool.UpdateSettings` (`pool.go`, beside `Rename` at ~552)

```go
// UpdateSettings merges the fields marked present in update into the stored
// settings of session id and re-persists, taking Pool.mu (write) exactly as
// Rename does for label. Unmarked (nil) fields keep their stored value; an
// absent YOLO can never enable bypass (AC3). Returns ErrSessionNotFound for an
// unknown id (no entry is created). The new values reach claude via #833's
// spawn path on the session's next spawn.
func (p *Pool) UpdateSettings(id SessionID, update SettingsUpdate) error
```

Body — the `Rename` control flow with a merge substituted (contract, not implementation; ~18 lines):

1. `p.mu.Lock(); defer p.mu.Unlock()`.
2. `sess, ok := p.sessions[id]`; `if !ok { return ErrSessionNotFound }`.
3. Build `merged := sess.settings` (copy of current), then overlay each present field: `if update.Model != nil { merged.Model = *update.Model }`, same for `Effort`, `YOLO`.
4. No-op short-circuit (mirrors `Rename`'s `sess.label == newLabel`): `if merged == sess.settings { return nil }` — `SessionSettings` is comparable, so an update that changes nothing writes nothing to disk (byte-stable, no spurious registry churn).
5. `prev := sess.settings; sess.settings = merged`.
6. `if err := p.saveLocked(); err != nil { sess.settings = prev; return err }` — roll the field back on persist failure, return the persist error as-is (exactly as `Rename` does; `saveRegistryLocked` already wraps).
7. `return nil`.

`saveLocked` is unchanged — it already reads `s.settings` under the held `Pool.mu` and serializes all three fields (`omitempty` keeps the default on-disk shape byte-stable). No `registry.go` change: #833 already added the fields and the lenient decode.

### 3. Doc-comment corrections (accuracy, not behaviour)

After this ticket `settings` is no longer immutable. Three stale comments must be corrected to say it is mutated by `Pool.UpdateSettings` under `Pool.mu` (write), read under `Pool.mu` — same discipline as `label`/`Rename`:

- `session.go:61-69` — the `SessionSettings` type comment ("Immutable post-construction in this ticket … A future wire setter (#826b) …"). Replace with: mutated by `Pool.UpdateSettings` (#840) under `Pool.mu`, read under `Pool.mu`, exactly as `Pool.Rename` mutates `label`.
- `session.go:115-120` — the `Session.settings` field comment ("Immutable post-New this ticket … #826b's setter must revisit synchronization"). Same correction.
- `pool.go:1223` — the `saveLocked` inline comment ("`s.settings` is immutable post-New, read under the held `Pool.mu`"). Drop "immutable post-New"; keep the "read under the held `Pool.mu` (same discipline as `s.label`, NOT `lcMu`)" part — that is still exactly true and now load-bearing for `UpdateSettings`.

These are the only edits outside the type + method.

## Data flow

```
#841 wire verb (validated)              this ticket (#840)                 #833 (shipped)
  SettingsUpdate{Model,Effort,YOLO?} ─> Pool.UpdateSettings(id, update)
                                            │ p.mu.Lock()
                                            │ sess = p.sessions[id]  (miss → ErrSessionNotFound)
                                            │ merged = overlay(present fields) over sess.settings
                                            │ sess.settings = merged
                                            │ saveLocked ──> registryEntry ──> sessions.json (0600)
                                            ▼
                            (next spawn of this session)
  bootstrap: next daemon restart ─> New warm-start reads entry.{Model,Effort,YOLO}
                                     ─> claudeSettingsArgs ─> claude ... --model X --effort Y [--dangerously-skip-permissions]
  minted:    next (re)spawn ─> buildSession applies the same helper
```

## Concurrency model

No new goroutines, no new locks, no lock-order change — byte-for-byte the `Rename` concurrency shape.

- `settings` is a **`Pool.mu`-guarded** field (like `label`), not `lcMu`-guarded. `UpdateSettings` holds `Pool.mu` (write) across the merge + `saveLocked` and **never touches any `Session.lcMu`**.
- `saveLocked` internally acquires each session's `lcMu` briefly (to read `lcState`/`lastActiveAt`) — that is the documented `Pool.mu → Session.lcMu` order (`docs/lessons.md:79`), already correct and unchanged.
- All registry saves serialize on `Pool.mu`; the Pool is the sole registry writer holding authoritative in-memory state, so `Pool.mu` serialization alone prevents a concurrent daemon save from clobbering the change (AC2) — **no reload-before-save**, unlike the `internal/devices` push-token path where a second writer forces read-modify-write. (This distinction is the standing single-writer rule; see the codebase notes on registry writers.)
- The change survives a daemon restart because `saveLocked` commits it to disk and #833's warm-start path reloads it (AC2/AC5).

## Error handling

- **Unknown id → `ErrSessionNotFound`** (AC4): the same sentinel `Rename`/`Remove`/`Lookup` return. No silent no-op, no new entry — the map miss returns before any mutation or save.
- **`saveLocked` failure**: roll `sess.settings` back to `prev` and return the persist error unwrapped (mirrors `Rename`). In-memory state is left consistent with disk (both = the pre-update value).
- **No-op update** (all present fields equal stored, or all fields nil): return `nil` without a save (AC-consistent — nothing changed).
- **YOLO absent (`update.YOLO == nil`)**: `merged.YOLO` stays at `sess.settings.YOLO`; bypass cannot be flipped on by omission (AC3). This is deterministic code (the `nil` check), not a stochastic guard.

## Testing strategy

Same-package (`package sessions`), stdlib `testing`, table-driven, `-race`. New file `pool_update_settings_test.go`. Reuse `helperPoolPersistent` (unit assertions) and the `helperPoolArgvRecorder` / `runPoolInBackground` / `waitArgv` recorder harness (AC5 spawn proof). Scenarios (write as bullet cases, not full function bodies):

- **Partial merge, per field** (AC1): starting from known settings, an update with only `Model` present changes `Model` and leaves `Effort`/`YOLO` at their stored values on disk; likewise `Effort`-only and `YOLO`-only. Assert via `loadRegistry` → `pickBootstrap`.
- **Empty-string is a real value, not "omitted"** (AC1): update `Model: ptr("")` over a non-empty stored `Model` clears it to `""` on disk (distinguishes present-empty from absent).
- **YOLO absent leaves stored value; can never flip on** (AC3): with stored `YOLO=false`, an update with `YOLO=nil` (but Model/Effort present) leaves `YOLO=false` on disk. With stored `YOLO=true`, `YOLO=nil` leaves it `true` (absence touches nothing either direction).
- **YOLO present flips both ways** (AC3): `YOLO: ptr(true)` over stored `false` persists `true`; `YOLO: ptr(false)` over stored `true` persists `false`.
- **Unknown id** (AC4): `UpdateSettings(NewID-not-in-pool, any)` returns `ErrSessionNotFound` (assert `errors.Is`); the registry file is unchanged (no new entry, byte-identical to before).
- **No-op update writes nothing** (byte-stability): update whose present fields all equal stored values returns `nil`; on-disk bytes unchanged. (Optional but cheap; mirrors `Rename`'s empty-clear discipline.)
- **Concurrent updates serialize** (AC2, `-race`): N goroutines call `UpdateSettings` on the same bootstrap id; final on-disk state equals one of the updates, no torn/interleaved settings, race detector clean.
- **Round-trip reaches the spawn argv** (AC5): with the recorder harness, `New` (cold) → `UpdateSettings(Default().ID(), {Model,Effort,YOLO=true})` → assert on-disk via `loadRegistry` → construct a **second** `Pool` from the same `RegistryPath` (a simulated daemon restart) → `runPoolInBackground` → `waitArgv` shows `--model … --effort … --dangerously-skip-permissions`. This exercises setter → `saveLocked` → disk → `New` warm-start → `claudeSettingsArgs` → argv, i.e. the "bootstrap daemon-restart round-trip #833 already exercises."

## Open questions

- **Method name.** `UpdateSettings` pairs with the partial-merge semantics (vs `SetSettings`, which reads as full replacement). If #841's author prefers a different spelling at the wire seam, the rename is trivial and local. Not resolved here — no code outside `internal/sessions` depends on it yet.
- **#842 (live apply) has no dependency on this method's shape.** It rebuilds/re-spawns the running supervisor after (or around) an `UpdateSettings` call; keeping the setter free of any supervisor interaction (pure in-memory mutate + persist, like `Rename`) leaves #842 free to choose evict→rebuild vs in-place. Flagged so #842 does not assume this ticket touched the running process.

## Out of scope

- Any wire message / verb, and any validation of untrusted model/effort values — #841 (the wire boundary that crosses untrusted → trusted).
- Making a **running** session pick up the change without a respawn — #842.
- Wiring `UpdateSettings` into the control-plane `Sessioner` interface (`internal/control/server.go:99`) or any other consumer — #841 adds it to its own consumer-defined interface; #840 ships the `*Pool` method only.
- Re-materialising non-bootstrap sessions on daemon restart (pre-existing limitation from #833 — minted sessions round-trip at the registry-serialization layer only).
- The `docs/knowledge/codebase/840.md` note — owned by the documentation phase, written post-merge. Not a developer deliverable.

## Security review

*(Mandatory: ticket is `security-sensitive`. Adversarial self-review of this spec, default-FAIL until each category is walked. Verdict below.)*

**Trust boundaries.** This ticket adds **no new untrusted-input boundary.** `SettingsUpdate` arrives from #841 already validated (the wire verb owns the untrusted → trusted crossing) or from same-package tests. The only persisted surface is `sessions.json` (mode 0600, dir 0700 — `saveRegistryLocked`, unchanged), operator-local. Downstream, the merged `SessionSettings` reaches claude as distinct `exec.CommandContext` argv tokens (no shell) via #833's already-reviewed `claudeSettingsArgs`. **Finding:** the spec must NOT let a future reader assume #840 validated model/effort — it did not; that is #841's job. Documented explicitly in § Out of scope and the handoff note. No MUST-FIX.

**Tokens, secrets, credentials.** N/A — the update carries model name, effort string, and a bypass bool. None are secrets. YOLO is the security-relevant flag, handled under Concurrency + category below. No finding.

**File operations.** No new path handling; reuse of `saveLocked` → `saveRegistryLocked`'s existing atomic temp-file→fsync→rename recipe (#833/ADR path). A SIGKILL mid-write leaves the pre- or post-image, never a torn `"yolo":tr`. No path is caller-derived. No finding.

**Subprocess / external command execution.** No new exec. The persisted values reach claude only on a *later* spawn through #833's reviewed argv path (no shell; each value a separate token). A model/effort string shaped like a flag is a single non-splittable argv element claude rejects, not an injected flag. No finding.

**Cryptographic primitives.** N/A — no randomness, no crypto in this ticket. No finding.

**Network & I/O.** N/A — no sockets, no readers, no size-cap surface added. #841 owns the wire read and its caps. No finding.

**Error messages, logs, telemetry.** `ErrSessionNotFound` is a generic sentinel (no id echoed in the sentinel itself). No new log line required; if the developer adds a debug log, it must log the session id and which fields changed, **never** widen to log a "bypass enabled" decision as anything an attacker could trigger silently — but no logging is prescribed here. Model/effort/YOLO are not secrets. No MUST-FIX; SHOULD note: keep any added log at the session-id level, not payload dumps.

**Concurrency.** Byte-for-byte the `Rename` shape: single `Pool.mu` (write) held across merge + `saveLocked`; no `lcMu` taken by the setter; documented `Pool.mu → Session.lcMu` order preserved (`saveLocked`'s internal `lcMu` re-acquire is sequential, not nested with any lock the setter holds). All saves serialize on `Pool.mu`; the Pool is the sole registry writer, so no reload-before-save and no clobber (AC2). Rollback-on-save-error keeps memory consistent with disk. No goroutine spawned. No finding.

**Threat model alignment — the one security-critical property: YOLO fail-safe-OFF.** The asset is `--dangerously-skip-permissions` never being enabled by absence, omission, or confusion. Three deterministic layers, no stochastic component (belt-and-suspenders, different fabric):

1. **Setter layer (this ticket):** YOLO is `*bool`. `update.YOLO == nil` (omitted) → `merged.YOLO = sess.settings.YOLO` (untouched); it is impossible for an omitted field to flip bypass on (AC3). Only an explicit non-nil `*bool` changes it. The `nil` check is the fail-safe — code, not convention.
2. **Persisted layer (#833, unchanged):** `YOLO` is a plain `bool` whose zero value is OFF; a missing `yolo` key decodes to `false`; a malformed `yolo` fails the whole `loadRegistry` parse (fail-closed, `New` refuses to start). See ADR 030.
3. **Argv layer (#833, unchanged):** `claudeSettingsArgs` emits the bypass flag only for `YOLO == true` and never emits a "disable bypass" flag — absence of the flag is what enforces permissions.

An operator turning YOLO **on** through this setter is the intended behaviour; the *authorization* to do so (per-device gate, authenticated action) is enforced upstream at #841's wire boundary — named here as out of scope with the handoff, not silently assumed.

**Findings summary:**

- [Trust boundaries] No MUST-FIX — #840 adds no untrusted boundary; the "#840 did not validate model/effort" assumption is documented for #841 (§ Out of scope + Open questions).
- [File operations] No findings — reuses #833's atomic-write `saveLocked` path; no caller-derived paths.
- [Subprocess] No findings — no new exec; values reach claude via #833's reviewed no-shell argv path.
- [Error messages / logs] SHOULD — no logging prescribed; if the developer adds one, keep it session-id level, never a payload dump. Not gating.
- [Concurrency] No findings — identical to `Pool.Rename`; single-writer `Pool.mu`, documented lock order, rollback-on-error.
- [Threat model — YOLO fail-safe-OFF] No findings — three deterministic layers (setter `*bool` nil-check + persisted plain-bool + argv helper) make absence/omission/corruption unable to enable bypass; enabling YOLO requires an explicit non-nil `true`, authorized upstream at #841.

**Verdict:** PASS.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-08
