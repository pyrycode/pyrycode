# Spec #848 — Populate `screen_snapshot` with the session's model / effort / YOLO

**Size:** S (comfortably; ~30 production LOC across 3 files, ~90 test LOC, no edit fan-out).
**Security-sensitive:** No — read-only reflection of an existing, non-secret control to an already-paired, AEAD-authenticated client that already receives the full rendered screen text under the same seal. No inbound verb, no authz decision, no mutation, no input parsing. (Contrast the write path #841/#845, `security-sensitive` because it accepts untrusted input to mutate the YOLO control.)

This is split **B** of #835. Split **A** (#847, merged as PR #850) shipped the two zero-dependency leaves this ticket consumes:
- `Pool.DefaultSettings() (SessionSettings, bool)` — a locked accessor for the bootstrap session's persisted settings (`internal/sessions/pool.go:937`).
- Three always-present `model` / `effort` / `yolo` fields on `ScreenSnapshotPayload` (`internal/protocol/snapshot.go:55`).

Both leaves are currently **unwired**: `handleRequestSnapshot` marshals the payload with the three fields at their zero values. This ticket wires the read path so the reply reports the running session's actual settings.

---

## Files to read first

- `internal/relay/v2session.go:1692-1757` — `handleRequestSnapshot`. The single edit site for the handler change: the `protocol.ScreenSnapshotPayload{…}` literal at :1719 is where the three new fields get populated. Note the branch order (KnownConversation reject → nil-Snapshotter offline → `!live` offline → marshal-and-forward); settings are read only on the success path, after the `live` check.
- `internal/relay/v2session.go:544-669` — `V2SessionConfig`. The new optional seam field goes here, alongside `Snapshotter` (:614) and `KnownConversation` (:621). Read the `DebugBundler func() (archive []byte, err error)` field (:660) — the primitive-typed-closure precedent this seam copies.
- `internal/relay/v2session.go:779-810` — `NewV2SessionManager`. The new seam is **optional** (like `Snapshotter`/`DebugBundler`) — do **not** add a validation branch here; nil is a supported value.
- `internal/sessions/pool.go:937-945` — `Pool.DefaultSettings()`. The accessor the cmd/pyry closure wraps. Returns `(SessionSettings{}, false)` when no bootstrap exists; both fields of the false case collapse to the wire defaults.
- `internal/sessions/session.go:70-74` — `SessionSettings{Model, Effort string; YOLO bool}`. The value type whose three fields map 1:1 onto the wire fields. Zero value = inherited daemon default (empty Model/Effort) + permissions enforced (YOLO off).
- `internal/protocol/snapshot.go:55-62` — `ScreenSnapshotPayload`. The three target fields (`Model`, `Effort`, `YOLO`), no `omitempty`, already on the wire since #847.
- `cmd/pyry/main.go:838-854` — the `debugBundler` closure + the `startRelay(…)` call. Build the new settings closure here (same spot, same shape) and add it as the final `startRelay` argument.
- `cmd/pyry/main.go:933-952` — `settingsUpdaterAdapter` (#845, the write-path adapter). Context for why relay speaks primitive/relay-local types and never imports `internal/sessions`. This ticket's read seam is even simpler — a bare closure, no adapter type needed.
- `cmd/pyry/relay.go:91-111` + `:154` — `startRelay` signature and the single `startRelayV2(…)` call site. Add the new parameter to both.
- `cmd/pyry/relay.go:278-372` — `startRelayV2` signature and the `V2SessionConfig{…}` literal. Wire the closure into the new seam field beside `Snapshotter` (:331) / `SettingsUpdater` (:371).
- `cmd/pyry/session_transition_v2.go:20-27` — `transitionObserverSink` doc comment. The canonical statement of the "`relay.go` must not import `internal/sessions`" discipline this ticket preserves.
- `internal/relay/v2session_test.go:3639-3830` — `fakeSnapshotter` double + `TestV2Session_OpenState_RequestSnapshot` table. The existing relay-level snapshot test the AC-4 assertions extend. Reuse `driveToOpen` / `v2Recorder` / `sealAppFrame` / `decryptAppFrame` verbatim.

---

## Context

The desktop Status sheet (pyrycode-desktop#156) shows the current model / reasoning-effort / permissions posture **before** offering to change it (the change path is the v2 `set_session_settings` verb, #841/#845). The persisted values live on the session (`SessionSettings`, #833). The `screen_snapshot` reply is the read channel the desktop already receives in response to `request_snapshot`; #847 added the wire fields but left them zero. This ticket makes the snapshot handler report the running session's actual settings.

Read-only. No new inbound verb, no relay-protocol change (the relay forwards v2 frames opaquely — `docs/protocol-mobile.md`).

---

## Design

Three additive changes, no signature breaks beyond the two threaded call sites.

### 1. New optional read seam on `V2SessionConfig`

Add one field, a primitive-typed closure, beside `Snapshotter` / `KnownConversation`:

```go
// SnapshotSettings reports the current model / effort / YOLO for the session
// whose screen the Snapshotter renders (the bootstrap), so handleRequestSnapshot
// can populate the screen_snapshot reply's settings fields. Optional: nil ⇒ the
// handler reports the effective defaults (empty model/effort, yolo:false) —
// preserving today's zero-value behaviour. Primitive-typed (three scalars) so
// internal/relay imports neither internal/sessions nor its SessionSettings type;
// production wires a closure over *sessions.Pool.DefaultSettings.
SnapshotSettings func() (model, effort string, yolo bool)
```

**Why a primitive-typed closure, not a relay-local struct.** `DebugBundler func() (archive []byte, err error)` (#813) is the exact precedent: relay declares a bare closure over the value it needs, and `cmd/pyry` builds it. A struct (mirroring `SettingsUpdate`, #845) is unnecessary here — the read has three scalar results and no presence semantics (nil-vs-set is expressed by the closure field being nil, not by pointer fields). Three named return values keep the call site self-documenting.

**Why no `ok` return.** `DefaultSettings` returns `(SessionSettings, bool)` where `false` means "no bootstrap session." Both branches of that bool map to the same wire output — the no-bootstrap case reports defaults, exactly like the all-defaults case (AC-2). The closure collapses `false` to `("", "", false)` internally, so the seam needs no `ok`.

**Do not add a validation branch to `NewV2SessionManager`.** The seam is optional, matching `Snapshotter` / `DebugBundler` / `SettingsUpdater`. A nil value is legal and means "report defaults."

### 2. Populate the three fields in `handleRequestSnapshot`

On the success path only (after the `live == true` check, before the `json.Marshal`), read the seam with a nil-guard and pass the three scalars into the payload literal at `v2session.go:1719`:

- nil seam ⇒ `model, effort, yolo` stay `"", "", false` (defaults; AC-2, and byte-identical to today's zero-value behaviour).
- non-nil seam ⇒ call it once, assign the three results.

The `ScreenSnapshotPayload{…}` literal gains `Model: model, Effort: effort, YOLO: yolo` beside `Text` / `TS`. No change to any reject branch, the error-reply path, or the forward path.

Behavioural contract (asserted by the extended table test, § Testing):
- Success reply carries the seam's supplied model/effort/YOLO.
- nil seam (or seam yielding defaults) ⇒ `model:"", effort:"", yolo:false`, all three present on the wire (never omitted — #847 dropped `omitempty`).

### 3. Wire the closure in `cmd/pyry`, keeping `relay.go` sessions-free

In `cmd/pyry/main.go` (beside the `debugBundler` closure, ~:849), build:

```go
snapshotSettings := func() (model, effort string, yolo bool) {
    s, ok := pool.DefaultSettings()
    if !ok {
        return "", "", false
    }
    return s.Model, s.Effort, s.YOLO
}
```

Thread `snapshotSettings` as a new trailing parameter through `startRelay` → `startRelayV2` (both signatures gain `snapshotSettings func() (model, effort string, yolo bool)`), and assign it to `V2SessionConfig.SnapshotSettings` in the config literal at `relay.go:312`.

**This closure — not `relay.go` — is where the `internal/sessions` dependency lives.** `cmd/pyry/main.go` already imports `internal/sessions`; `relay.go` deliberately does not (see `transitionObserverSink` doc comment). The closure decodes `SessionSettings` into three primitives at the composition root, so the value crossing into `relay.go` and `internal/relay` is a bare `func() (string, string, bool)`. Same discipline as `debugBundler` (#813) and `settingsUpdaterAdapter` (#845).

**Foreground / v1 path unchanged.** `startRelayV2` is only reached on the v2 leg; the v1 dispatch path (`relay.go:165-223`) builds no `V2SessionConfig` and is untouched. In a hypothetical foreground build the closure still reads a real pool, so no nil case arises there — but the seam's nil-tolerance covers any future call site that omits it.

### Design invariant behind AC-3 (settings-source == snapshot-source == bootstrap)

`Snapshotter` (`*supervisor.Supervisor`) renders the **bootstrap** child's screen and is conversation-agnostic. `Pool.DefaultSettings` reads that **same** bootstrap session's persisted settings. So the reported model/effort/YOLO and the rendered screen describe the one session, and both agree with the flags that session was launched with (`--model` / `--effort` / `--dangerously-skip-permissions`, applied from `SessionSettings` at spawn, #833) — by construction, not by a runtime cross-check.

**Do not pre-carve a conversation-keyed settings seam.** The snapshot is single-session today. If a future ticket makes it multi-session, `Snapshotter` and `SnapshotSettings` get keyed together; inventing a keyed settings seam now would add a parameter neither the snapshot source nor `DefaultSettings` can honour.

---

## Concurrency model

No new goroutines, no new locks. `handleRequestSnapshot` runs on the manager's single `Run` dispatch goroutine (the loop is the lock for `s.send` / `s.state`). The `SnapshotSettings` closure is invoked **synchronously** on that goroutine and returns before the marshal — identical to how `KnownConversation` invokes `convReg.Get(...)` inline in the same handler.

The closure calls `Pool.DefaultSettings()`, which takes `p.mu.RLock()` — a lock wholly internal to `internal/sessions`, never held by the relay, so there is no lock-ordering interaction with the manager. `DefaultSettings` is safe from any goroutine and is consistent under a concurrent `UpdateSettings` (writer holds `p.mu` write; reader holds RLock — no torn read) and under a concurrent #842 live-restart (which persists via `UpdateSettings` before restarting, so the read observes either the pre- or post-update value atomically). A snapshot request racing a settings change reports whichever value won the lock — acceptable: the snapshot is a point-in-time picture.

---

## Error handling

There is no new error path.
- nil seam ⇒ defaults (`"", "", false`). Not an error — a supported unwired configuration.
- The closure has no error return; `DefaultSettings` has none (its `ok=false` collapses to defaults inside the closure).
- The handler's existing reject/offline/marshal-failure branches are untouched — settings are read only after `live == true`, so a settings read never gates or alters an error reply.

---

## Testing strategy

Extend the existing relay-level table `TestV2Session_OpenState_RequestSnapshot` (`v2session_test.go:3655`) — do **not** write a new harness; reuse `driveToOpen` / `v2Recorder` / `sealAppFrame` / `decryptAppFrame`.

- Add a `settings func() (model, effort string, yolo bool)` field to the table struct and pass it into the `V2SessionConfig` literal at :3755 as `SnapshotSettings: tt.settings`.
- Add expected-value columns (`wantModel`, `wantEffort`, `wantYOLO`) checked in the `TypeScreenSnapshot` case beside the existing `Text` assertion (:3795), decoding the three fields off the already-unmarshaled `protocol.ScreenSnapshotPayload`.
- New / adjusted rows (AC-4):
  - **injected non-default settings** — `settings` returns e.g. `("opus", "high", true)`, live snapshotter ⇒ reply carries `model:"opus", effort:"high", yolo:true`.
  - **all-defaults via nil seam** — `settings: nil`, live snapshotter ⇒ reply carries `model:"", effort:"", yolo:false` (the explicitly-required all-defaults case). This doubles as the byte-compatibility check that a nil seam preserves today's zero-value output.
  - **all-defaults via seam returning defaults** — `settings` returns `("", "", false)` ⇒ same wire output as the nil case (proves the closure's no-bootstrap collapse is indistinguishable from unset).
- The existing error/offline rows need no settings column — set their `settings` to nil; those branches never read it. Confirm they still pass unchanged (the settings read is downstream of every reject).

No new `cmd/pyry` test. The `snapshotSettings` closure is trivial glue over `DefaultSettings` (which #847 unit-tested) and is exercised transitively via the daemon path — the same posture as the untested-in-cmd/pyry `debugBundler` (#813) and `settingsUpdaterAdapter` (#845) closures.

Run: `go test -race ./internal/relay/... ./internal/protocol/... ./internal/sessions/...` and `go vet ./...`.

---

## Acceptance criteria (from the ticket, mapped to this design)

1. The `screen_snapshot` reply reports the current model / effort / YOLO for the session, read from the live session's persisted settings — via `SnapshotSettings` → `Pool.DefaultSettings`, not hard-coded or zero. *(Design §2/§3)*
2. A never-configured session reports the effective default the client can interpret — inherited model/effort as empty strings, YOLO as `false` — never an omitted field. *(nil-seam + default collapse; #847's no-`omitempty` fields carry the explicit zeros)*
3. The reported values match what #833 persisted and the flags the session was launched with. Holds **by construction**: settings-source == snapshot-source == bootstrap session. *(Design invariant section — documented, not runtime-checked)*
4. A relay-level test drives `request_snapshot` and asserts the pushed `screen_snapshot` carries the settings the injected read seam supplied, including the all-defaults case (empty model/effort, `yolo:false`). *(Testing section)*

---

## Open questions

- **Seam name.** `SnapshotSettings` is chosen to parallel `Snapshotter` (both describe the same bootstrap session and would be keyed together in a future multi-session snapshot). If the developer finds `SessionSettings` clearer at the call site, either is acceptable — but avoid a bare `Settings` (collides conceptually with the `SettingsUpdater` write seam). Not blocking.
- **None affecting correctness.** The three deliverables are additive, the seam is optional, and no consumer outside the two threaded call sites and the one config literal changes.
