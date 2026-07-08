# Spec #847 — `Pool.DefaultSettings()` accessor + model/effort/yolo fields on `screen_snapshot`

**Size:** S · **Not** `security-sensitive` (read-only reflection of existing, non-secret session config). Split A of #835; ships **unwired** — B (#848) wires both leaves into the snapshot handler.

## Files to read first

- `internal/sessions/pool.go:885-892` — `Pool.Default()`: the exact accessor pattern to mirror (RLock, resolve `p.sessions[p.bootstrap]` fresh, no `lcMu`). New accessor is this plus a value read and an existence bool.
- `internal/sessions/pool.go:585-612` — `Pool.UpdateSettings`: documents that `sess.settings` is guarded by **`p.mu`** (not `lcMu`); the only other reader is `saveLocked`. Confirms reading `settings` under `RLock` is race-free.
- `internal/sessions/pool.go:511-528` — `RotateID` flips `p.bootstrap = newID` under the write lock. Resolving `p.sessions[p.bootstrap]` fresh each call is what makes the accessor rotation-safe (AC-1).
- `internal/sessions/session.go:61-73` — `SessionSettings` struct (`Model`, `Effort`, `YOLO`); a pure value type (no pointers/slices) → returning it by value is a clean snapshot. Zero value = inherit template / permissions enforced.
- `internal/sessions/pool_update_settings_test.go:21-69` — `helperPoolWithSettings` + `diskSettings` test helpers. `helperPoolWithSettings(t, regPath, settings)` warm-starts a pool whose bootstrap carries the given settings — the exact fixture for the AC-2 test (see `pool.Default().ID()` usage at line 95).
- `internal/protocol/snapshot.go:36-50` — `ScreenSnapshotPayload` struct + the file's documented "no field carries omitempty / every field always present" invariant the three new fields must honour.
- `internal/protocol/snapshot_test.go:33-113` — `TestScreenSnapshotPayload_RoundTrip` (extend for the 3 fields) and `TestSnapshotPayloads_EmptyConversationID` (the boundary-pin style to copy for the zero-value assertions).
- `internal/protocol/interactive_test.go:14-28` — `roundTripEnvelope`: it compares `canonical(out)` vs `canonical(raw)`.
- `internal/protocol/envelope_test.go:11-18` — **`canonical` is `json.Compact` — it does NOT sort keys.** Therefore fixture payload field order must exactly match struct declaration order. This is the single sharpest constraint in the ticket (see Design § Fixture).
- `internal/protocol/testdata/screen_snapshot.json` — the one-line fixture to regenerate.
- `docs/protocol-mobile.md:617-625` — the `screen_snapshot` field table (3 rows today) to extend with 3 rows.

## Context

The desktop Status sheet (pyrycode-desktop#156) renders the current model / reasoning-effort / YOLO setting before offering to change them. Those values live as `SessionSettings{Model, Effort, YOLO}` on the bootstrap session (private field, shipped #833) and need to reach the paired client via the existing `screen_snapshot` reply. #835 was split at the architect gate (5 prod files) into two children; **this ticket ships the two zero-dependency leaves** that a later ticket (#848) consumes:

1. a locked, rotation-safe accessor that surfaces the bootstrap session's persisted settings across the `internal/sessions` package boundary (the private `settings` field is unreachable otherwise — `SessionInfo`/`List()` does not carry it);
2. three always-present fields on the `screen_snapshot` wire payload.

Nothing reads either leaf at runtime after this ticket. The existing snapshot handler (`internal/relay/v2session.go:1719`, a **keyed** struct literal) keeps compiling untouched and simply serializes the three new fields at their zero values — wire-valid, no live desktop consumer yet. #848 populates them.

## Design

### 1. `Pool.DefaultSettings()` accessor — `internal/sessions/pool.go`

Add one exported method, placed adjacent to `Default()`:

```go
func (p *Pool) DefaultSettings() (SessionSettings, bool)
```

**Behaviour (contract):**

- Take `p.mu.RLock()` (defer RUnlock) — same lock discipline as `Default()`.
- Resolve the bootstrap **fresh**: `sess := p.sessions[p.bootstrap]`. Do not cache a `*Session`; fresh resolution is what keeps it correct across a session-id rotation (`RotateID` updates `p.bootstrap` under the write lock). — **AC-1 rotation clause.**
- If `sess == nil` (no bootstrap to read from — the embedded evicted-bootstrap host case; also the zero-value `&Pool{}` map-miss), return `(SessionSettings{}, false)`. The bool lets a consumer fall back to defaults. — **AC-1 existence clause.**
- Otherwise return `(sess.settings, true)`. `SessionSettings` is a value type, so the return is a snapshot copy — no aliasing of the pool's live field. — **AC-1 read clause.**

**Locking note for the doc comment** (mirror `Default()`'s + `UpdateSettings()`'s phrasing): reads `sess.settings` under `p.mu` (RLock); does **not** take `Session.lcMu` — `settings` is a `p.mu`-guarded field (the writer `UpdateSettings` holds `p.mu` write; the other reader `saveLocked` holds `p.mu`), so there is no torn read. State in the comment that it resolves `p.bootstrap` fresh so it stays correct across a rotation, and that the bool reports whether a bootstrap session exists.

Why the accessor is unavoidable (record in the comment, one line): `settings` is a private field read only under `Pool.mu`; a consumer outside `internal/sessions` cannot reach it without this accessor.

**Do not** add a conversation-keyed variant. The snapshot source is the bootstrap session (the `Snapshotter` renders the bootstrap, conversation-agnostic); settings source == snapshot source by construction, which is what makes #848's AC hold. Keying would invent a seam neither side has.

### 2. Wire fields — `internal/protocol/snapshot.go`

Add three fields to `ScreenSnapshotPayload`, **after** `TS`, **no `omitempty`**:

```go
Model  string `json:"model"`
Effort string `json:"effort"`
YOLO   bool   `json:"yolo"`
```

Extend the struct's doc comment to state the semantics a consumer relies on:
- `Model` / `Effort` — the bootstrap session's per-session override; **empty string = inherited daemon default (no override)**.
- `YOLO` — bypass-permissions (`--dangerously-skip-permissions`) on/off; **`false` = permissions enforced** (the fail-safe default).

All three are always present on the wire (honouring the file's existing no-omitempty invariant), so an empty `model`/`effort` is distinguishable from unset and `yolo:false` is explicit rather than dropped.

**Field order is load-bearing.** `roundTripEnvelope` compares `json.Compact`ed bytes, which preserves key order; Go marshals struct fields in declaration order. Appending the three fields after `TS` fixes the wire order to `conversation_id, text, ts, model, effort, yolo` — and the fixture (below) must list them in exactly that order.

### 3. Fixture — `internal/protocol/testdata/screen_snapshot.json`

Regenerate the single-line fixture so the `payload` object carries the three new fields with representative **non-default** values (a concrete model + effort, `yolo: true`) in declaration order, e.g. `…,"ts":"2026-05-08T10:33:14Z","model":"opus","effort":"high","yolo":true`. Leave the existing `conversation_id`/`text`/`ts` and the envelope-level fields unchanged.

Safest regeneration method: marshal a representative `ScreenSnapshotPayload` in a throwaway snippet (or by hand) and confirm the payload substring matches struct declaration order — a mis-ordered fixture fails `roundTripEnvelope`'s byte compare, which is the intended tripwire.

## Concurrency model

No new goroutines, channels, or locks. The accessor is a single `p.mu.RLock()`-guarded read that composes with the existing reader/writer discipline (`List`/`saveLocked`/`ResolveID` share the read lock; `UpdateSettings`/`Rename`/`RotateID` serialise on the write lock). The wire change is pure struct vocabulary — no runtime.

## Error handling

The accessor cannot error. The "no bootstrap" case is expressed as the `bool == false` return (not an error), so a consumer branches on existence and falls back to defaults — matching the ticket's stated contract and `Default()`'s nil-return convention.

## Testing strategy

**`internal/sessions` — accessor (AC-2), one new test file `pool_default_settings_test.go`:**
- **Known settings round-trip (AC-2):** `helperPoolWithSettings(t, regPath, SessionSettings{Model:"opus", Effort:"high", YOLO:true})` → call `pool.DefaultSettings()` → assert `got == want` (value equality; `SessionSettings` is comparable) **and** the existence bool is `true`.
- **No-bootstrap existence bool:** call `DefaultSettings()` on a zero-value `&Pool{}` (in-package; the `RWMutex` zero value is usable and `p.sessions[p.bootstrap]` is a nil-map miss) → assert `(SessionSettings{}, false)`. Pins the fallback contract the consumer relies on.
- Both cases table-driven if the developer prefers; `t.Parallel()` where the temp-dir case allows.

**`internal/protocol` — wire (AC-3/4/5), extend `snapshot_test.go`:**
- **Round-trip (AC-5):** in `TestScreenSnapshotPayload_RoundTrip`, after regenerating the fixture, assert `payload.Model`/`Effort`/`YOLO` equal the fixture's non-default values, then let the existing `roundTripEnvelope(...)` call pin the full canonical shape (AC-4 fixture ↔ struct order).
- **Zero-value boundary (AC-5):** marshal `ScreenSnapshotPayload{}` and assert the output contains `"model":""`, `"effort":""`, and `"yolo":false` — none dropped. Mirror the existing `TestSnapshotPayloads_EmptyConversationID` `bytes.Contains` style (add a `screen_snapshot` sub-case there or a sibling test).

Run `go test -race ./internal/sessions/... ./internal/protocol/...` and `go vet ./...`.

## Docs (developer deliverable — AC-6)

`docs/protocol-mobile.md` § `screen_snapshot` table (line ~621): add three rows —
- `model` · string · Per-session model override; empty = inherited daemon default.
- `effort` · string · Per-session reasoning-effort override; empty = inherited daemon default.
- `yolo` · bool · Bypass-permissions (`--dangerously-skip-permissions`); `false` = permissions enforced.

`protocol-mobile.md` is the wire contract and part of this feat commit (precedent: #740, #831), **not** a doc-phase evergreen doc. Do **not** create or edit `docs/knowledge/**` — that is the documentation phase's job.

## Open questions

None. Signature, field names, ordering, and the split boundary are all pinned by #835's split decision and the existing `Default()`/`UpdateSettings()` patterns.
