# #2488 — close a `noise_msg` on an unknown conn with retryable 4410

## Files read

- `internal/relay/v2session.go` → the close-code `const` block (`StatusIdleTimeout`,
  `StatusProtocolMismatch`, `StatusHandshakeFailure`, `StatusQueueOverflow`) — the doc-comment
  shape the new constant copies, and the block's append-at-the-end ordering convention
  (4413 was appended after 4426 despite being numerically lower).
- `internal/relay/v2session.go` → `handleFrame` — creates a bare `V2Session` in
  `V2StateAwaitingInit` for any conn id the manager does not hold, before dispatching on
  the inner frame type. This is why a legitimate client whose session the daemon dropped
  lands on the arm this ticket changes.
- `internal/relay/v2session.go` → `closeWith` — deletes the session from `m.sessions` and
  its queue from `m.queues`, then emits one routing envelope carrying `CloseCode` verbatim.
  It accepts any `websocket.StatusCode`; nothing in this repo gates the value.
- `internal/relay/v2session_handshake.go` → `handleNoiseMsg` — the three-arm switch. Only
  the `V2StateAwaitingInit` arm changes; `V2StateHandshakeComplete` (4401 + sealed
  `auth.invalid_token`) and `V2StateOpen` (4421 on AEAD failure) are untouched.
- `internal/relay/v2session_test.go` → `TestV2Session_OutOfStateRejections` — asserts one
  shared close code for all six cases today, so the changed case needs a per-case expectation.
- `internal/relay/v2session_test.go` → `setIdleTimeout`, `driveToOpen`, `waitForConnClose`,
  `sealAppFrame`, `wrapInnerFrame`, `handshakeConnToOpen` — the helpers each leg of the new
  test uses. `sealAppFrame` seals under the initiator's `CipherState` and addresses
  `v2TestConnID`, which is exactly "a client that still holds cipher state for a session the
  daemon dropped".
- `internal/relay/v2session_test.go` → `TestV2Session_SweptThenReconnect_ReHandshakes` — the
  nearest existing shape (sweep at 4408, then act on the same conn id); the new test is its
  sibling with a `noise_msg` instead of a `noise_init`.
- `docs/knowledge/features/v2-session-manager-state-machine-transition-table.md` — the
  `awaitingInit` column's two `noise_msg` rows read close(4421); they become close(4410) in
  the documentation stage, not here.
- `docs/protocol-mobile.md` § Error codes — the `4408` and `4413` rows and paragraphs whose
  "lands on a session the daemon no longer has" promise this change makes true. Read only;
  the documentation stage owns the edit.

## Context

`handleFrame` creates a fresh `V2Session` in `V2StateAwaitingInit` for any conn id the
manager does not hold. When the first frame on that conn is a `noise_msg`, the
`V2StateAwaitingInit` arm of `handleNoiseMsg` closes at `StatusProtocolMismatch` (4421).
Clients treat 4421 as a permanent protocol mismatch and stop re-dialling — the desktop shows
"Pairing error - Re-pair" until it is restarted.

A legitimate client reaches that arm whenever it still holds cipher state for a session the
daemon has dropped. Because `closeWith` deletes the conn from `m.sessions`, all three causes
reduce to the same thing — a conn id the manager no longer holds: a daemon restart (observed
twice on pyrybox on 2026-09-15), an idle sweep at 4408, and the push-queue ceiling at 4413.
`docs/protocol-mobile.md` § Error codes already promises for both 4408 and 4413 that the
phone "learns on its next inbound frame, which lands on a session the daemon no longer has"
and that "recovery is a fresh Noise handshake"; the code contradicts that promise.

No ADR is warranted — this adds one close code to an existing family, under the conventions
ADR-recorded for 4408 and 4413.

## Change

Add `StatusSessionGone websocket.StatusCode = 4410` to the close-code `const` block in
`internal/relay/v2session.go`, appended after `StatusQueueOverflow` (the block is ordered by
addition, not numerically). Its doc comment follows the shape of `StatusIdleTimeout` and
`StatusQueueOverflow`: what fires it, the 44xx←HTTP echo (410 Gone, as 4408←408 and
4413←413), why 4408 is *not* reused — "your session idled out" is not what a restart or an
overflow means, the same mislabel `StatusQueueOverflow` already records rejecting — and the
wire-spec pointer to the new `docs/protocol-mobile.md` § Error codes row.

In `internal/relay/v2session_handshake.go`, the `V2StateAwaitingInit` arm of `handleNoiseMsg`
closes with `StatusSessionGone` instead of `StatusProtocolMismatch`, in both the
`close_code` log field and the `closeWith` call. The `v2.state.reject` event name, the
`noise_msg_before_handshake` reason and the field set are unchanged; the close stays
close-only (`frame` nil — there are no CipherStates, so nothing can be sealed). The arm's
inline comment and the clause in the `handleNoiseMsg` doc comment are retuned to say why the
code is retryable.

Nothing else moves. The other two arms keep 4401 and 4421, `handleFrame`'s own three reject
paths (malformed inner frame, `noise_resp` from a phone, unknown type) keep 4421, and no
other package references this arm — the only in-repo consumers of 4421 are the open-state
AEAD-failure tests and unrelated comments.

## Testing strategy

- `TestV2Session_OutOfStateRejections` gains a `wantCode uint16` field per case. Five cases
  carry `uint16(StatusProtocolMismatch)`; `noise_msg_before_handshake` carries
  `uint16(StatusSessionGone)`. `uint16` rather than `websocket.StatusCode` keeps the test
  file's import list unchanged and matches `RoutingEnvelope.CloseCode`'s type.
- New in-package test `TestV2Session_SweptThenNoiseMsg_4410_ThenReHandshakes` covering
  AC-3 end to end against one running manager: `setIdleTimeout` to a sub-second value,
  `driveToOpen` on `v2TestConnID`, wait for the 4408 sweep via `waitForConnClose`, send a
  frame sealed under the initiator's still-live send `CipherState` with `sealAppFrame`,
  assert a 4410 close with `Frame == nil` via `waitForConnClose`, then
  `handshakeConnToOpen` on a second conn id to prove the manager still serves a fresh
  handshake. In-package because `idleTimeout` is a package var only an in-package test can
  shrink; not `t.Parallel`, matching the other `setIdleTimeout` tests.
- Unchanged and expected to pass as-is: `TestV2Session_RekeyResponder_OldKeyFrameAfterSwap_4421`,
  `TestV2Session_OpenState_TamperedNoiseMsg_4421`,
  `TestV2Session_Gating_NoiseMsgInHandshakeComplete_4401`.
- Gate: `go test -race ./internal/relay/...`, `go vet ./...`, `go build ./cmd/pyry`.

## Documentation handoff

Pending — owned by the documentation stage, not this PR.

- `docs/protocol-mobile.md` § Error codes: a `4410` row between the `4409` and `4413` rows,
  marked v2-new and sent by the binary (forwarded by the relay), plus a paragraph in the
  style of the neighbouring `4408` and `4413` paragraphs stating when it fires and that
  recovery is a fresh Noise handshake. Point the `4408` and `4413` paragraphs' "lands on a
  session the daemon no longer has" sentences at it. The § binary obligations bullet that
  closes a missing first `noise_init` at `4421` is about the ten-second timeout and is
  unchanged — worth a clause so the two do not read as contradicting.
- `docs/knowledge/features/v2-session-manager-state-machine-transition-table.md`: the
  `awaitingInit` column's two `noise_msg` rows read close(4421) and become close(4410).

## Open questions

- Where in the `const` block the new code belongs. Resolved before the plan commit: appended
  after `StatusQueueOverflow`, following the block's by-addition ordering (4413 already sits
  after 4426) rather than numeric order.
- Whether an e2e (`internal/e2e`) leg is owed as well. Resolved: no. No e2e test exercises
  this arm today (`internal/e2e/relay_v2_handshake_test.go` covers the open-state tampered
  frame at 4421 only), the acceptance criteria place the proof in `internal/relay`, and the
  4408 leg the new test depends on is reachable only by shrinking a package var.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — the changed arm is upstream of every trust boundary. It
  is reached only when the manager holds no `V2Session` for the conn id, so there are no
  CipherStates, `handleNoiseMsg` reads nothing out of `inner.Data` on this path, and no
  device token is consulted. The change is a single literal in a close envelope; the
  boundary itself (`handleNoiseInit` → paired-device lookup → `V2StateOpen`) is untouched.
- [Tokens, secrets, credentials] No findings — no token is read, compared or logged on this
  arm, and the log field set is unchanged (`event`, `conn_id`, `close_code`, `reason`). The
  close carries `frame` nil, so no key material is exercised and no send-nonce is burned.
- [File operations] Not applicable — the change touches no filesystem path.
- [Subprocess / external command execution] Not applicable — no process is spawned.
- [Cryptographic primitives] No findings — the arm runs before CipherStates exist, so no
  primitive, nonce or comparison is involved. Notably it must stay that way: sealing an
  error envelope here would be impossible (no keys), which is why the close stays close-only.
- [Network & I/O] Finding, no fix required — making the close retryable turns a client that
  reaches this arm into one that re-dials instead of stopping, so a hostile or buggy peer can
  drive a reconnect loop. The cost per iteration is bounded: `handleFrame` allocates only a
  bare `V2Session` struct (the push queue and the app-frame worker are created in
  `handleNoiseInit`, which this path never reaches), and `closeWith` deletes the entry in the
  same Run-goroutine turn, so nothing accumulates in `m.sessions` or `m.queues`. The dial
  rate is bounded outside this repo by the relay's per-IP rate limit, which the ticket names
  as the intended backstop. Judged acceptable: the alternative — keeping a permanent-failure
  code so misbehaving peers stop — is exactly the behaviour that breaks legitimate clients.
- [Error messages, logs, telemetry] No findings — one `Warn` with an unchanged field set and
  no frame content; the `reason` string is a fixed literal, not remote input.
- [Concurrency] No findings — the arm runs on the manager's Run goroutine, as it already did;
  no lock is taken, no goroutine is spawned, no timer is armed, and no shared state beyond
  the `m.sessions` delete `closeWith` already performed on this path.
- [Threat model alignment] Finding, accepted — the change makes the close code distinguish
  "conn id the manager holds no session for" (now 4410) from "open session whose AEAD check
  failed" (still 4421), where both previously read 4421. That is only an oracle to a party
  that can inject a frame on an arbitrary conn id, which on the binary's single multiplexed
  WebSocket means the relay itself or whoever has claimed the server-id there. The relay
  assigns conn ids and already observes every `noise_resp` and close it routes, so it learns
  nothing it did not already hold; `docs/protocol-mobile.md` § Security model treats the
  relay as untrusted for confidentiality and relies on Noise_IK, which this change does not
  weaken. Out of scope and named as such: 4409/server-id claim hardening at the relay
  (relay-side, not this repo) and the mobile app confirming 4410 is outside its own fatal set
  (the ticket routes that to the mobile repo).

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-16
