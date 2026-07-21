# Spec #1101 — `request_snapshot` degrades to `msgSnapshotOffline` on the stream-json path

**Size:** XS (confirmed — PO sized XS). One production file (`cmd/pyry/relay.go`), one test file. Zero new exported types. One call-site changed. No `internal/relay` change.

## Files to read first

- `internal/relay/v2session_replay.go:50-75` — `handleRequestSnapshot`: the `KnownConversation` gate (line 59), then the nil-`Snapshotter` arm (66-69) and the `!live` arm (71-75). **This is the control flow the stream path routes into. NO CHANGE here** — the arm already exists and already fires after the gate.
- `internal/relay/v2session_seams.go:25-32` — `ScreenSnapshotter` interface (`ScreenSnapshot() (text string, live bool)`), exported, consumed by the manager.
- `internal/relay/v2session_seams.go:266-272` — `Snapshotter ScreenSnapshotter` field doc: *"Optional: when nil, request_snapshot yields a server.binary_offline error reply — the snapshot feature is simply unavailable, not a crash."* This is the contract the fix relies on.
- `cmd/pyry/relay.go:463-496` — the `V2SessionConfig` literal; the `Snapshotter: w.sup` line (492) is the one line to change, and the `KnownConversation` closure (493-496) is the gate wiring (unchanged).
- `cmd/pyry/main.go:941` — `sup: bootstrap.Supervisor()` — the origin of `w.sup`.
- `internal/sessions/session.go:245-258` — `Session.Supervisor()` type-asserts the stored runner to `*supervisor.Supervisor` and **returns nil for a stream-json runner** (the typed-nil source); `Session.Runner()` is total for both runner types. This nil-on-stream-path behaviour is the mechanism, established by #1077.
- `internal/supervisor/supervisor.go:550-558` — `ScreenSnapshot()` dereferences `s.sessMu` on the first line; **called on a nil receiver it panics** (why a typed-nil-in-interface, not just a nil pointer, is the bug).
- `internal/relay/v2session_test.go:3789-3842` — existing cases `"foreign conversation rejected"` (uses a **live** snapshotter, still returns `CodeConversationNotFound` → proves gate-first) and `"nil Snapshotter reports offline"` (→ `CodeServerBinaryOffline`, retryable). **The end-to-end offline degrade AND the security-critical gate ordering are already pinned here.** Reuse `fakeSnapshotter` if any relay-level assertion is wanted (it is not — see Testing).

## Context

The stream-json runner (`*streamsup.Runner`, constructed and pool-supervised as of #1109) structurally has no PTY/screen: no tui-driver seal, no transcript tailing. When the daemon's bootstrap session is stream-json-backed and a phone sends `request_snapshot`, there is nothing to render, so the request must degrade to the existing offline reply.

The relay handler already does the right thing when handed a nil `Snapshotter`: it returns `msgSnapshotOffline` / `CodeServerBinaryOffline`, **after** the `KnownConversation` gate. The gap is purely in the wiring: `cmd/pyry/relay.go:492` assigns `w.sup` (a `*supervisor.Supervisor`) directly to the `Snapshotter ScreenSnapshotter` field, and on the stream path `w.sup` is a **typed nil** (`Session.Supervisor()` returns `nil` when the runner is not a `*supervisor.Supervisor`, per #1077). A typed-nil pointer assigned to an interface produces a **non-nil interface value** — so `m.cfg.Snapshotter == nil` is false, the handler skips its offline arm, and calls `ScreenSnapshot()` on a nil `*supervisor.Supervisor` receiver → panic at `s.sessMu.Lock()`. AC #1's *"cleanly, with no error or panic"* is exactly the closing of this trap.

Today `sessions.New` in `main.go:753` is constructed with no `RunnerFactory`, so production is always PTY and `w.sup` is never nil — the panic is latent. This ticket is the preparatory guard so that when the stream-json toggle (the config selector, sibling to the #1078 split) lands, `request_snapshot` degrades cleanly instead of panicking. The guard is a no-op on the PTY path (non-nil `w.sup` → non-nil interface, byte-identical wiring).

## Design

### The one change: convert typed-nil `*supervisor.Supervisor` → genuine nil `ScreenSnapshotter`

Add a small unexported helper in `cmd/pyry/relay.go` (next to `startRelayV2`) and use it at the `Snapshotter` field:

```go
// screenSnapshotterOrNil returns a genuine nil ScreenSnapshotter when sup is a
// nil *supervisor.Supervisor — the stream-json bootstrap path, where
// Session.Supervisor() returns nil (#1077). Assigning the typed-nil pointer
// straight to the interface field would leave a non-nil interface holding a nil
// pointer, so handleRequestSnapshot would skip its nil-Snapshotter arm and call
// ScreenSnapshot on a nil receiver → panic. Routing to nil instead lands the
// request in the handler's existing post-gate offline arm (#1101).
func screenSnapshotterOrNil(sup *supervisor.Supervisor) relay.ScreenSnapshotter
```

Behaviour: `return nil` when `sup == nil`; otherwise `return sup`. Signature + the one-line contract above is the whole thing — a ~4-line body.

At `cmd/pyry/relay.go:492`, change:

```
Snapshotter: w.sup,                        // before
Snapshotter: screenSnapshotterOrNil(w.sup),// after
```

That is the entire production change. `w.sup` stays typed `*supervisor.Supervisor` everywhere else it is used (it is needed as the concrete type by `SessionStarter`, the modal resolver, and `State()`), so only the Snapshotter assignment is touched.

### Why no `internal/relay` change

`handleRequestSnapshot` already:

1. runs the `KnownConversation` gate first (v2session_replay.go:59) — a foreign/unknown/empty/malformed `conversation_id` returns `CodeConversationNotFound` and returns;
2. then, for a known conversation, takes the offline arm when `Snapshotter == nil` (line 66) — returns `CodeServerBinaryOffline`, retryable.

Feeding a genuine nil into that seam lands the stream path in arm (2), strictly after gate (1). No new message, no new code, no new degrade path — exactly the ticket's mandate.

### Data flow (stream path, after the fix)

```
phone → sealed request_snapshot → handleRequestSnapshot (app-frame worker goroutine)
  ├─ KnownConversation(convID)?  ── no ─→ CodeConversationNotfound reply, return   (gate, unchanged)
  └─ yes → Snapshotter == nil?   ── yes ─→ msgSnapshotOffline / CodeServerBinaryOffline reply, return
```

On the PTY path the second branch is `no` (non-nil supervisor) and the handler renders the live screen exactly as today.

## Concurrency model

None introduced. `screenSnapshotterOrNil` is a pure function — no state, no goroutine, no lock. It is called once, on the single-threaded daemon-startup path inside `startRelayV2`. The handler continues to run on the per-conn app-frame worker goroutine (v2session_seams.go:250-266) and its reply still seals through the existing `forwardEnvelope` path on the Run goroutine — unchanged.

## Error handling

The "failure" this path handles — no live screen — is precisely the offline reply, and it is already implemented. The fix removes a crash (panic) failure mode and replaces it with the existing deterministic reply. No new failure modes are introduced: the helper cannot fail (it returns one of two interface values). The offline reply carries only the static `msgSnapshotOffline` constant and `CodeServerBinaryOffline` — no attacker-controlled bytes, no conversation_id, no screen text (unchanged from today's nil-Snapshotter arm).

## Testing strategy

**New — `cmd/pyry` helper unit test** (table-driven, stdlib `testing`):

- `screenSnapshotterOrNil(nil)` returns an interface that satisfies `got == nil` (the genuine-nil property — this is the assertion that would have caught the typed-nil trap; a direct `Snapshotter: w.sup` assignment would fail it).
- `screenSnapshotterOrNil(nonNilSup)` returns a non-nil interface (`got != nil`). A zero-value `&supervisor.Supervisor{}` is sufficient as the non-nil pointer — the test only inspects interface-nil-ness, never calls `ScreenSnapshot`; an empty composite literal is legal cross-package (no unexported fields are set).

**Reused — existing `internal/relay` coverage** proves the end-to-end behaviour the ACs describe, with no new relay test needed:

- AC #1 (degrade cleanly): `v2session_test.go:3835` `"nil Snapshotter reports offline"` → `CodeServerBinaryOffline`, retryable, exactly one reply, no panic.
- AC #2 (gate unchanged): `v2session_test.go:3790` `"foreign conversation rejected"` uses a **live** snapshotter yet still returns `CodeConversationNotFound` — pinning that the gate runs before any Snapshotter branch; plus `"nil KnownConversation rejects all"` (3799), `"empty conversation_id rejected"` (3808), `"malformed payload rejected"` (3817).

The composition — cmd/pyry hands a genuine nil on the stream path (new helper test) ∘ relay routes nil into the post-gate offline arm (existing tests) — covers both ACs without duplicating the relay-level table in `cmd/pyry`.

`go build ./...`, `go vet ./...`, `go test -race ./cmd/pyry/... ./internal/relay/...` must stay green.

## Open questions

- **Sibling seams with the same trap are deliberately out of scope.** `SessionStarter: w.sup` (relay.go:550) and the modal resolver (`newModalResolverV2(modalReg, w.sup, …)`, relay.go:423) assign the same typed-nil `w.sup` and would panic on the stream path if a `new_session` / modal-resolve frame arrives. Those verbs are separate slices of the #1078 stream-json integration and may degrade differently; this ticket touches only the `Snapshotter` seam (Scope Discipline). Not a bug to fix here — flagged so the narrow scope reads as intentional. The `snapshotUsage` closure (relay.go:441, `w.sup.State()`) is safe: it is only invoked in the render path (v2session_replay.go:93), which the offline arm returns before reaching, so it is never called on the stream path.
- **Helper naming/placement** — `screenSnapshotterOrNil` in `cmd/pyry/relay.go` beside `startRelayV2` matches the file's existing wiring-helper convention (`resolveWorkspaceDir`, `relay4409Threshold`). A developer may prefer a different name; the contract (nil pointer → nil interface) is what matters.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No finding. The untrusted input is the phone's `request_snapshot` envelope, specifically `payload.ConversationID` (network → process). The boundary is `handleRequestSnapshot`'s `KnownConversation` gate at `v2session_replay.go:59` — **unchanged** by this ticket. The fix introduces no new parse of untrusted input and no new boundary; it only decides, at wiring time, whether the `Snapshotter` seam is a genuine nil. The stream path routes into the **existing post-gate** arm (line 66), so the gate still runs first. Pinned by the existing `"foreign conversation rejected"` test (`v2session_test.go:3790`), which uses a live snapshotter yet returns `CodeConversationNotFound`.
- **[Error messages, logs, telemetry — the oracle concern]** No finding, and this is the crux of the `security-sensitive` label. The offline reply carries only the static `msgSnapshotOffline` constant + `CodeServerBinaryOffline`; it never echoes the conversation_id, the decode error, or any screen text (identical to today's nil-Snapshotter arm), and adds no log call. The distinction the design must protect: a **foreign** conversation_id must return not-found, never offline — otherwise a remote caller learns a foreign conversation exists (an enumeration oracle). Because a foreign id is rejected by the gate at line 60 **before** the Snapshotter nil-check is ever reached, the offline reply is only observable for a conversation the phone is already authorised to enumerate via `list_conversations`. The offline-vs-not-found split therefore leaks only "this **known** conversation currently has no live screen," which is not sensitive. Short-circuiting the offline reply ahead of the gate — the anti-pattern the ticket names — is explicitly **not** done: the fix moves the gate nowhere and adds no pre-gate branch.
- **[Concurrency]** No finding. `screenSnapshotterOrNil` is a pure function (no state, no lock, no goroutine), called once on the single-threaded startup path. No new lock ordering, no new goroutine lifecycle, no shutdown interaction. The handler's existing single-owner reply discipline (seal on the Run goroutine) is untouched.
- **[Tokens/secrets]** Not applicable. The fix moves no secret material; on the stream path the seam is nil, so nothing is rendered. No token, key, credential, or nonce is created, compared, or logged.
- **[File operations]** Not applicable. No filesystem path is constructed or read on this path. The `snapshotUsage` transcript read (`w.sup.State()` at relay.go:441) is never reached because the offline arm returns before `v2session_replay.go:93`.
- **[Subprocess / external command]** Not applicable. No `exec`, no argv, no environment handling.
- **[Cryptographic primitives]** Not applicable. No RNG, no comparison, no key derivation. The reply seals through the existing `forwardEnvelope` Noise path, unchanged.
- **[Network & I/O]** No finding. The degrade emits exactly one bounded `TypeError` envelope and returns; it adds no new `Read`, no unbounded allocation, and no new size cap is needed (the inbound envelope is already size-bounded by the dispatch layer).
- **[Threat model alignment]** The relevant threat — the conversation-existence enumeration oracle — is addressed by preserving gate-before-degrade ordering (routing into the existing post-gate arm). **Out of scope, named:** the `SessionStarter` (`new_session`) and modal-resolver seams carry the same typed-nil-in-interface trap on the stream path and are handled by sibling slices of the #1078 stream-json integration, not here.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-21
