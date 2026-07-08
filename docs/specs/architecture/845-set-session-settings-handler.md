# Spec — #845: handle the `set_session_settings` v2 verb — validate, persist, reply

**Size:** S (3 production files — `internal/relay/v2session.go`, `cmd/pyry/relay.go`, `cmd/pyry/main.go`; 1 new exported interface + 1 new exported struct + 1 new exported sentinel; ~5 reply branches; ~150 production LOC + ~300 test LOC).

**Label:** `security-sensitive`. This ticket adds the **untrusted remote write path** for per-session model / effort / YOLO. #833 (the storage primitive) explicitly deferred untrusted-value validation to the wire-owning ticket: *"#826b introduces the untrusted (wire) set-path and MUST validate model/effort there."* #826b became #841, split into #844 (wire vocab, merged) + **#845 (this handler)**. The security-review pass (§ Security review) is part of this spec.

## Context

Desktop (pyrycode-desktop#156) and a forthcoming mobile client need one shared daemon message to change a session's per-session settings. #844 landed the wire vocabulary on `main`: the message types `TypeSetSessionSettings` / `TypeSessionSettingsUpdated`, the payload structs `SetSessionSettingsPayload{SessionID; Model, Effort *string; YOLO *bool}` / `SessionSettingsUpdatedPayload{SessionID}`, and `CodeSessionNotFound`. #840 landed the persistence seam `sessions.Pool.UpdateSettings(id, SettingsUpdate) error`. #833 landed the spawn-argv application (`claudeSettingsArgs`).

This ticket is the **daemon-side handler**. It intercepts `set_session_settings` in the v2 manager before `dispatch.Route`, gates on the negotiated `interactive` capability, validates the untrusted model/effort at the wire boundary, persists via the injected settings seam, and replies with a deterministic success or failure. The relay forwards v2 frames opaquely, so this is entirely daemon-side. The change takes effect on the session's **next spawn** (via #833's `claudeSettingsArgs`); making a *running* session pick it up immediately is the sibling live-restart ticket #842 — out of scope here.

## Files to read first

- `internal/relay/v2session.go:1511-1548` — `dispatchAppFrame` control switch. Add the new `case protocol.TypeSetSessionSettings` here, before `dispatch.Route`, mirroring the other interception cases.
- `internal/relay/v2session.go:1648-1747` — `handleRequestSnapshot` + `snapshotReplyError`: the **reply-owing** handler precedent (validate → `forwardEnvelope` success/error, all on the Run goroutine). This is the closest shape to copy. Note the `msgSnapshot*` static-constant discipline.
- `internal/relay/v2session.go:1779-1851` — `handleDebugBundleRequest` + `debugBundleReplyError`: the nil-seam → deterministic-"unavailable"-reply pattern, and the "never echo the assembly error text" discipline.
- `internal/relay/v2session.go:2019-2143` — `handleInterrupt` / `handleNewSession` / `handleDequeueMessage`: the `if !s.interactive { return }` capability gate (the authz boundary) and the tolerant-decode / never-echo posture. Note these are fire-and-forget (no reply); this verb differs — it always replies on the interactive path.
- `internal/relay/v2session.go:433-509` — the consumer-side seam interfaces (`Interrupter`, `SessionStarter`, `QueueRemover`, `ModalResolver`). Add `SettingsUpdater` + `SettingsUpdate` + `ErrSessionUnknown` beside them, same doc-comment idiom (relay imports neither `internal/sessions` nor `cmd/pyry`).
- `internal/relay/v2session.go:522-628` — `V2SessionConfig`. Add the optional `SettingsUpdater` field with the nil-behaviour doc, mirroring `QueueRemover` / `DebugBundler`.
- `internal/relay/v2session.go:2786-2821` — `forwardEnvelope`: the single seal-and-forward path both the success and error replies use (`V2StateOpen`-gated; the Run goroutine).
- `internal/protocol/settings.go` — `SetSessionSettingsPayload` (decode target) and `SessionSettingsUpdatedPayload` (success reply body). The pointer-presence contract lives in this file's doc comment; do not re-derive it.
- `internal/protocol/codes.go:7-34` — the error-code constants. Reuse `CodeSessionNotFound`, `CodeProtocolMalformed`, `CodeServerBinaryOffline`; **do not add a new code** (wire vocab is #844's closed domain).
- `internal/sessions/pool.go:571-612` — `Pool.UpdateSettings`: merges present fields, one atomic `saveLocked`, rolls back in-memory on save failure, returns `ErrSessionNotFound` for an unknown id. This is what the cmd/pyry adapter wraps.
- `internal/sessions/session.go:75-109` — `SettingsUpdate` (the seam's presence contract to mirror) and `claudeSettingsArgs` (**the argv surface the model/effort validator defends** — `--model <value>` / `--effort <value>` as separate argv tokens).
- `cmd/pyry/relay.go:91-110, 277-365` — `startRelay` / `startRelayV2` signatures + the `V2SessionConfig{…}` construction. Thread the new seam param through both and wire it into the config.
- `cmd/pyry/main.go:738-757, 853` — `pool` construction and the single `startRelay(...)` call site. `pool` is already in scope (passed as `transitionObserverSink`); construct the settings adapter here (like `sessionMinter{pool}` on the same line).
- `cmd/pyry/main.go:898-1009` — `poolResolver` / `sessionMinter` / `sessionRouter`: the co-located `*sessions.Pool` → consumer-interface adapter idiom. Add `settingsUpdaterAdapter` here.
- Tests to mirror: `internal/relay/v2session_dequeue_test.go` (interactive-gated inbound verb; capability negative path) and `internal/relay/v2session_debugbundle_test.go` (reply decryption). Harness: `driveToOpenCaps` (open a conn with a chosen capability set), `sealAppFrame` (seal an inbound control frame), `v2Recorder` + `openSession` (decrypt outbound replies) — all in `internal/relay/v2session_test.go:718-855`.
- `docs/protocol-mobile.md:802-845` — the `set_session_settings` / `session_settings_updated` wire contract (presence semantics, `session_id` addressing key, `in_reply_to` correlation, example frames). Line 859 pins `session.not_found` as *"Returned by the handler (#845)."*

## Design

### 1. Interception point (`internal/relay/v2session.go`)

Add one case to the `dispatchAppFrame` switch (before `dispatch.Route`), mirroring `TypeRequestSnapshot`:

```go
case protocol.TypeSetSessionSettings:
    m.handleSetSessionSettings(ctx, s, probeEnv)
    return
```

Handler signature (matches `handleRequestSnapshot` — needs `ctx` for `forwardEnvelope`):

```go
func (m *V2SessionManager) handleSetSessionSettings(ctx context.Context, s *V2Session, env protocol.Envelope)
```

### 2. Consumer-side seam (`internal/relay`, beside `Interrupter` / `SessionStarter`)

`internal/relay` must import neither `internal/sessions` nor `cmd/pyry`, so the seam speaks relay-local vocabulary:

```go
// SettingsUpdate is the presence contract: a nil field leaves the stored
// value untouched; a non-nil field sets it (including *"" / *false). Mirrors
// sessions.SettingsUpdate 1:1 without importing internal/sessions.
type SettingsUpdate struct {
    Model  *string
    Effort *string
    YOLO   *bool
}

// SettingsUpdater persists a per-session settings change; the cmd/pyry adapter
// wraps *sessions.Pool.UpdateSettings. ErrSessionUnknown ⇒ session.not_found.
type SettingsUpdater interface {
    UpdateSettings(sessionID string, update SettingsUpdate) error
}

var ErrSessionUnknown = errors.New("relay: session unknown")
```

Config field on `V2SessionConfig` (optional, nil-behaviour documented like `DebugBundler`):

```go
// SettingsUpdater persists an inbound set_session_settings change (#845).
// Optional: nil ⇒ the verb replies "unavailable" deterministically, never a
// silent drop (foreground / unwired). Production wires a *sessions.Pool adapter.
SettingsUpdater SettingsUpdater
```

`SettingsUpdate` (three untyped pointers) carries no secret and needs no `SECURITY:` note; the field doc just states the nil-behaviour. Reuse the existing `errors` import.

### 3. Handler control flow (ordered; the order is load-bearing)

The interactive path **always replies** (success or failure); the non-interactive path is fully inert. Steps:

1. **Capability gate (authz boundary).** `if !s.interactive { return }` — inert: no decode, no seam call, **no reply** (AC #6). A non-interactive conn must not even learn whether a session exists. Matches `handleInterrupt` exactly; a bare one-line check, **not** a reusable inbound-gate abstraction (CODING-STYLE: resist over-DRY).
2. **Decode.** `json.Unmarshal(env.Payload, &p)` into `protocol.SetSessionSettingsPayload`. Unlike the fire-and-forget verbs (which tolerate decode failure silently), this verb owes a reply, so a decode error ⇒ `settingsReplyError(…, CodeProtocolMalformed, msgSettingsMalformed, false)` and return (AC #4: malformed → failure reply, persists nothing). **Never echo the decode error or any payload byte** — `encoding/json` quotes attacker bytes into its error string.
3. **Validate untrusted values (AC #5, before any persistence).**
   - `if p.Model != nil && !validModel(*p.Model)` ⇒ malformed reply, return.
   - `if p.Effort != nil && !validEffort(*p.Effort)` ⇒ malformed reply, return.
   - `YOLO` needs no value validation — `*bool` has only two inhabitants; a malformed `yolo` (e.g. `"yolo":"yes"`) already failed the step-2 type-decode into the malformed branch, so bypass can never be inferred from a bad value (AC #4).
4. **Nil-seam guard.** `if m.cfg.SettingsUpdater == nil` ⇒ `settingsReplyError(…, CodeServerBinaryOffline, msgSettingsUnavailable, true)`, return. Never a silent drop.
5. **Persist.** Build `SettingsUpdate{Model: p.Model, Effort: p.Effort, YOLO: p.YOLO}` (post-validation, the payload pointers pass straight through — same presence contract). Call `err := m.cfg.SettingsUpdater.UpdateSettings(p.SessionID, update)`:
   - `err == nil` ⇒ **success reply** (below) + one content-free info log; return.
   - `errors.Is(err, ErrSessionUnknown)` ⇒ `settingsReplyError(…, CodeSessionNotFound, msgSettingsNotFound, false)`, return.
   - else (persist/disk failure) ⇒ log the failure **event** only (never `err.Error()` — it can quote a path), then `settingsReplyError(…, CodeServerBinaryOffline, msgSettingsUnavailable, true)`, return.

**Atomicity (AC #1).** Validation (step 3) rejects before the seam is touched, so an invalid request persists nothing. `Pool.UpdateSettings` merges all present fields under one `saveLocked` with in-memory rollback on failure, so a partial write is impossible — any combination of the three fields applies atomically or not at all.

### 4. Success reply (built inline, mirroring `handleRequestSnapshot`)

Marshal `protocol.SessionSettingsUpdatedPayload{SessionID: p.SessionID}` into an `Envelope{ID: 1, Type: protocol.TypeSessionSettingsUpdated, TS: time.Now().UTC(), Payload: …, InReplyTo: &env.ID}` and `m.forwardEnvelope(ctx, s.connID, reply)`. Echoing `p.SessionID` is safe: on the `nil`-error path the id matched a real session key exactly (so it is a confirmed-real, non-secret routing id in a typed struct field — not an error-string interpolation), and #844's `SessionSettingsUpdatedPayload` is defined precisely to carry it back. A `forwardEnvelope` error is logged at debug and dropped (the package's outbound-drop posture), never echoing content.

### 5. Failure-reply helper + static messages

Add a `settingsReplyError(ctx, s, inReplyTo uint64, code, message string, retryable bool)` mirroring `snapshotReplyError` / `debugBundleReplyError` byte-for-byte in shape (marshal `protocol.ErrorPayload` → `Envelope{Type: TypeError, InReplyTo}` → `forwardEnvelope`). A third near-identical copy is the established package posture (each reply-owing handler owns its helper; see the `snapshot`/`debug bundle` pair) — do **not** extract a shared helper (over-DRY). Static message constants (never attacker-influenced):

```go
const (
    msgSettingsMalformed   = "malformed set_session_settings request"
    msgSettingsNotFound    = "unknown session_id"
    msgSettingsUnavailable = "session settings unavailable"
)
```

### 6. Validators (`internal/relay`)

Define both locally — the ticket forbids assuming a sibling provides them.

- `validEffort(e string) bool` — `e == "" || e ∈ {low, medium, high, xhigh, max}`. The closed set matches `cmd/pyry/agent_run.go`'s `validEfforts` (the established `--effort` enum) but is defined relay-local (relay can't import cmd/pyry). `""` is the legitimate "clear to template default" value (`claudeSettingsArgs` emits no `--effort` for it).
- `validModel(m string) bool` — **shape check, not an allowlist** (model names churn; a fixed allowlist would reject new models and force a code edit per launch). Accept `m == ""` (clear) OR: length 1..64, first byte alphanumeric, every byte in `[A-Za-z0-9._-]`. This is the argv-injection defense — see § Security review, threat 2.

### 7. cmd/pyry wiring (adapter + param threading)

**Adapter** in `cmd/pyry/main.go`, beside `sessionMinter` / `poolResolver`:

```go
type settingsUpdaterAdapter struct{ p *sessions.Pool }

func (a settingsUpdaterAdapter) UpdateSettings(id string, u relay.SettingsUpdate) error {
    err := a.p.UpdateSettings(sessions.SessionID(id),
        sessions.SettingsUpdate{Model: u.Model, Effort: u.Effort, YOLO: u.YOLO})
    if errors.Is(err, sessions.ErrSessionNotFound) {
        return relay.ErrSessionUnknown
    }
    return err
}
```

This is the **only** place `sessions.ErrSessionNotFound → relay.ErrSessionUnknown` mapping lives (the project convention: sentinel-to-wire mapping at the consumer call site, not in the primitive).

**Param threading** (edit fan-out is minimal — one caller each):
- `startRelayV2` (`cmd/pyry/relay.go:277`): add a `settings relay.SettingsUpdater` param; wire `SettingsUpdater: settings` into the `V2SessionConfig{…}`.
- `startRelay` (`cmd/pyry/relay.go:91`): add the same param; pass it through on the `startRelayV2(...)` call (line 153).
- `main.go:853`: pass `settingsUpdaterAdapter{pool}` as the new arg. `pool` is already in scope on that line.

## Concurrency model

No new goroutine and no new lock. `handleSetSessionSettings` runs on the manager's **single Run dispatch goroutine**, like every other `dispatchAppFrame` handler: `s.interactive` is read lock-free (single-owner invariant), and `forwardEnvelope` reads `m.sessions` on that same goroutine. The seam call crosses into `internal/sessions`, where `Pool.UpdateSettings` takes `Pool.mu` (the pool's own independent lock) synchronously and returns before the handler continues — it satisfies the `V2SessionConfig.Handlers` "handlers must be synchronous, no long-lived goroutines" contract. `UpdateSettings` performs one small atomic registry write on the Run goroutine; this briefly blocks the Run loop, consistent with the existing synchronous-handler posture (`handleDebugBundleRequest` does heavier I/O the same way). The registry is a handful of entries and the write is temp-file + `rename` (sub-millisecond), so no offload is warranted — and offloading would break the reply-correlation-on-Run pattern and the single-owner invariant.

## Error handling

Every interactive request yields exactly one reply; nothing is silently dropped (AC #3). Failure-mode → reply mapping:

| Condition | Reply type | Code | Retryable | Persists? |
|---|---|---|---|---|
| Success | `session_settings_updated` | — | — | yes (atomic) |
| Non-interactive conn | *(none — inert)* | — | — | no |
| Malformed payload / bad model / bad effort | `error` | `protocol.malformed` | no | no |
| Unknown `session_id` (seam → `ErrSessionUnknown`) | `error` | `session.not_found` | no | no |
| Nil seam / persist (disk) failure | `error` | `server.binary_offline` | yes | no |

Reusing `server.binary_offline` for "settings seam unavailable / persist failed" follows the `handleDebugBundleRequest` precedent (it overloads the same code for "unavailable") and keeps this ticket out of #844's closed wire-vocabulary. All three failure messages are fixed constants; no `err.Error()`, no raw frame byte, and no model/effort value ever reaches the wire or a log.

**Logging discipline (never-echo).** Success: one `Info` with `event=v2.settings.updated`, `conn_id`, `session_id` only (`session_id` is a non-secret routing id, already a standard log field). Persist failure: one `Warn` with `event=v2.settings.persist_err`, `conn_id`, `session_id` — never the wrapped `err`. Model / effort / YOLO **values** are never logged at any level (they include attacker-controlled bytes on the malformed path, and #833 established keeping them out of logs). The capability-gate and malformed branches log nothing that includes payload bytes.

## Testing strategy

Table-driven, mirroring `v2session_dequeue_test.go` (capability gate) and `v2session_debugbundle_test.go` (reply decryption). A fake `SettingsUpdater` test double records the `(sessionID, SettingsUpdate)` it receives and returns a configurable error. Drive a conn to open with `driveToOpenCaps` (interactive vs non-interactive), seal an inbound frame with `sealAppFrame`, and decrypt outbound frames via `v2Recorder` / `openSession`.

Scenarios (inputs → expected):
- interactive, all three fields present & valid → seam called once with all three non-nil; exactly one outbound reply of type `session_settings_updated`, `in_reply_to == req.ID`, payload `session_id` echoes the request.
- interactive, only `effort` present (valid) → seam receives `Model==nil, YOLO==nil, Effort!=nil`; success reply.
- interactive, only `yolo:true` → seam receives `*YOLO==true`; success reply.
- interactive, `model:""` and `effort:""` present (the "clear" values) → validators accept; seam receives non-nil `*""`; success reply.
- **non-interactive** → seam **not** called; **zero** outbound frames (AC #6).
- nil `SettingsUpdater` seam → seam absent; one `error` reply, `server.binary_offline`, retryable.
- seam returns `ErrSessionUnknown` → one `error` reply, `session.not_found`, not retryable.
- seam returns an arbitrary persist error whose text is a recognizable sentinel → one `error` reply; assert the reply message **equals** `msgSettingsUnavailable` and does **not** contain the seam-error text (never-echo).
- malformed payload — `{"yolo":"nope"}` (type mismatch) and a truncated-JSON body → seam **not** called; one `error` reply, `protocol.malformed`; assert the message equals `msgSettingsMalformed` and contains none of the request bytes.
- invalid model — leading dash `--dangerously-skip-permissions`, embedded space, embedded control byte, and a 65-char string → each: seam **not** called; malformed reply.
- invalid effort — `ultra` → seam **not** called; malformed reply.
- unit table for `validModel` / `validEffort` (accepted: `""`, `sonnet`, `claude-opus-4-8`, `opus.plan`, `low`..`max`; rejected: leading `-`, space, control char, over-length, `ultra`).

Run `go test -race ./internal/relay/... ./cmd/pyry/...` and `go vet ./...`.

## Security review

*(Mandatory: ticket is `security-sensitive`. Pass performed on this spec before commit. Verdict below.)*

**Trust boundaries.** This ticket opens a **new untrusted remote write surface**: a paired phone's `set_session_settings` frame flows `phone → relay (opaque) → V2SessionManager.dispatchAppFrame → handleSetSessionSettings`. The frame is already AEAD-authenticated (Noise_IK) and the peer is token-validated at handshake, but the *payload contents* (`session_id`, `model`, `effort`, `yolo`) are attacker-controlled. Two authorization layers gate the write: (a) the negotiated `interactive` capability (`s.interactive`), and (b) the session addressing is by-id through the pool, which scopes the mutation to exactly the named session. Downstream, validated values flow: payload → `relay.SettingsUpdate` → `sessions.SettingsUpdate` → `SessionSettings` → argv tokens → `exec.CommandContext` (no shell).

**Assets & threats walked:**

1. **YOLO silently enabling `--dangerously-skip-permissions` (primary asset).** Deterministic, code-enforced fail-safe:
   - The `*bool` presence contract (#844/#840): absent `yolo` ⇒ `nil` ⇒ `UpdateSettings` leaves the stored value untouched — an absent field can never enable bypass (AC #4).
   - A malformed `yolo` value fails the step-2 payload type-decode → malformed reply, persists nothing. No partial-parse path can reach `YOLO=*true`.
   - Only an explicit `"yolo":true` sets `*true`; the whole flow is a single atomic `saveLocked`. **Belt-and-suspenders is different fabric:** the safe default is the pointer-nil semantics (code), and the corruption guard is `json.Unmarshal` strictness (code) — no stochastic component.
2. **argv injection via `model` / `effort` (the deferred #833 threat, now owned here).** Values reach claude as **separate argv tokens** (`claudeSettingsArgs`: `--model <value>`, `--effort <value>`) under `exec.CommandContext` with no shell — so there is no shell-metachar / word-splitting surface. The residual risk is a leading-dash value (`--model --foo`) confusing claude's **own** flag parser into reading a flag where a value was expected. `validModel` closes it: rejecting a leading `-` (first byte must be alphanumeric) and constraining the charset to `[A-Za-z0-9._-]` with a 64-byte cap means no crafted value can pose as a flag, and `validEffort`'s closed enum admits no free-form value at all. Validation runs **before** persistence (AC #5), so a rejected value is never stored and never spawned.
3. **Information disclosure / attacker-byte reflection.** The never-echo discipline of `handleRequestSnapshot` / `handleDequeueMessage` is applied verbatim: the three failure replies carry only fixed message constants; no `json` decode-error string, wrapped `err`, raw frame byte, or model/effort value ever reaches the wire or a log. The success reply echoes only the client's own confirmed-real `session_id` in a typed struct field (not an error interpolation). Logs carry content-free discriminants (`event`, `conn_id`, `session_id`) only.
4. **Authorization / capability bypass.** A non-interactive conn's request is fully inert — no decode, no seam call, **no reply** (AC #6). Because it does not even reply, an unauthorized peer cannot use the verb as a session-existence oracle. The gate is a deterministic one-line `if !s.interactive` on the single Run goroutine, matching the three sibling inbound control verbs.
5. **Cross-session / addressing abuse.** `session_id` is the addressing key; `UpdateSettings` does a direct map lookup and returns `ErrSessionNotFound` for any id not present (including `""` and any well-formed-but-unknown UUID), yielding a deterministic `session.not_found` reply. A hostile id can only ever name a session the daemon actually hosts or miss entirely — it cannot touch another daemon's or an out-of-band resource.
6. **Atomicity / torn write.** `Pool.UpdateSettings` is one atomic temp-file+rename with in-memory rollback on save failure; a rejected or failed request leaves the registry byte-and-mtime unchanged. No partial-apply is observable across a daemon restart.

**Decision criteria:** the two security-critical properties — bypass fail-safe-OFF on absence/corruption, and no untrusted value reaching the claude argv un-validated — are each enforced by deterministic code (pointer-nil semantics; shape/enum validators before the seam). The authz gate and never-echo discipline reuse the established, reviewed sibling patterns. No finding requires a spec revision. **Verdict: PASS.**

## Open questions

- **`session_id` rotation on `/clear` (from the ticket).** A `/clear` issues a new id via the `session_transition` marker; a client must track the current id to address the right session. No handler change is needed — an addressing a stale id simply returns `session.not_found`, which is the correct deterministic outcome. Documented here so the developer does not add id-tracking logic.
- **Dedicated `server.unavailable` code.** This spec reuses `server.binary_offline` for the nil-seam / persist-failure cases to avoid touching #844's closed wire vocabulary. If a future ticket wants to distinguish "settings seam down" from "no live claude," it can add a `server.unavailable` code in the protocol package — a wire-vocab change, not a handler change. Not resolved here; no code depends on the distinction.
- **Relay-side `session_id` shape pre-validation.** Not added: `internal/relay` can't import `sessions.ValidID`, and the seam already rejects unknown/empty ids deterministically. A future shape-check would only change the *code* on a malformed id (`protocol.malformed` vs `session.not_found`), not the leak posture — both are safe. Deferred.

## Out of scope

- Live-restart so a *running* session picks up the change immediately (#842).
- Any new wire message type, payload struct, or error code (#844's closed domain).
- Reloading non-bootstrap sessions on daemon restart (pre-existing #833 limitation).
- The `docs/knowledge/codebase/845.md` note — owned by the documentation phase, written post-merge from this spec + the merged diff. Not a developer deliverable.
