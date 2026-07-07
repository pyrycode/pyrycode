# Spec #812 — Stream a large bundle to a paired client in frame-cap-respecting chunks

**Ticket:** [#812](https://github.com/pyrycode/pyrycode/issues/812) — split from #803.
**Size:** S (confirmed; 3 production files, ~450 LOC total incl. tests).
**Security-sensitive:** Yes (label present). Self-review pass is at the end of this spec — **Verdict: PASS**.

## Context

The debug bundle assembled by #811 (`debugbundle.Assemble` → `[]byte`) is content-bearing and routinely multiple megabytes (a real `.cast` recording alone exceeds one frame). Two hard walls in the v2 relay stop it from riding a normal reply:

1. **The AEAD frame cap.** A `noise_msg` inner frame's decoded `data` (the ciphertext) is capped at `maxNoisePayloadBytes = 65535` (`internal/relay/v2session.go:57`), enforced on decode at `decodeInnerFrameV2` (`:977`). A multi-MB blob cannot be one sealed frame.
2. **The synchronous handler-reply channel.** `handlerOutboundBuf = 8` (`:246`) and the per-frame `dispatch.Conn` outbound channel is drained *after* the handler returns — more than 8 sends from a handler deadlock. So a bundle cannot be emitted frame-by-frame through the handler path.

This ticket delivers **the streaming primitive**: move an arbitrarily-large `[]byte` to one addressed, open, authenticated v2 conn as **ordered, cap-respecting chunks ending in a completion marker**, riding the manager's own asynchronous send path (`Push` → `drainOnce` → `forwardEnvelope`) — the same path every other unsolicited daemon→client frame uses (#571 snapshot, #632 emitter, #656 resync). It ships **unwired**: driven only by tests here. The request verb that triggers it for the debug bundle is sibling **#813**.

It is independently testable: assert the emitted, decrypted chunks reassemble to exactly the input, and that every emitted frame's ciphertext stays under the cap.

## Files to read first

- `internal/relay/v2session.go:2358-2417` — **`Push(ctx, connID, env)`**: the mandated send path. Enqueues onto the per-conn `pushQueue`, returns immediately, never touches `s.send`/`m.sessions`/`Outbound`; safe from any goroutine (incl. a handler on the Run goroutine — its cap-1 `drainCh` send is non-blocking). `StreamBundle` loops over this. Returns `ErrConnNotFound` when the conn isn't open. **Extract: the exact contract `StreamBundle` inherits.**
- `internal/relay/v2session.go:2419-2502` — **`drainOnce`**: pops one buffered envelope per Run pass, FIFO per conn, seals via `forwardEnvelope`. Proves chunks leave in enqueue order.
- `internal/relay/v2session.go:2581-2634` — **`forwardEnvelope`**: the single seal-and-forward path. `V2StateOpen` gate (keeps output from an un-authenticated/torn-down peer), `s.send.Encrypt(json.Marshal(env))`, wrap `noise_msg`. Note `env.EventID != nil` is the replay-dedup guard — bundle envelopes leave `EventID` nil, so the guard is inert for them. **Extract: plaintext = `json.Marshal(env)`; ciphertext = plaintext + 16-byte AEAD tag — this drives the chunk-size derivation.**
- `internal/relay/v2session.go:144-238` — **`pushQueue` drop policy**: `droppable` is `Type == TypeAssistantDelta` *only*. Bundle chunks are control-class → **never dropped**; admitted past cap via soft-overflow. This is why AC#2 (all chunks delivered, in order) holds through the transport.
- `internal/relay/v2session.go:53-57` + `:955-982` — `maxNoisePayloadBytes = 65535` and the inbound `decodeInnerFrameV2` cap it's enforced against. The outbound side must keep each sealed ciphertext ≤ this, or the phone rejects the frame at 4421.
- `internal/protocol/codes.go:99-160` — the v2-only `const (...)` block pattern (rekey/snapshot/resync/session_transition). **Add a new `debug_bundle_*` block here in the same shape** (with the "MUST NOT be added to v1TypeSet" doc).
- `internal/protocol/messaging.go:67-81` — `MessageChunkPayload` / `BackfillDonePayload` — the payload-struct shape to mirror for the two new payloads.
- `internal/protocol/envelope.go:19-40` + `:111-135` — `Envelope` fields (`ID/Type/TS/Payload/EventID`); `v1TypeSet` — the new types **must NOT** be added here.
- `internal/protocol/compat_test.go:104-179` — `v2OnlyTypes` map + `TestTypeConstants_V1V2Partition` `all` list + `TestIsV1Compatible` "-rejected" cases. **The two new types must be registered in all three** (test-file edits), else the drift detector fails the build.
- `internal/relay/v2session_test.go:698-798` + `:831-860` — `driveToOpen` → `openSession{mgr, rec, initRecv}` (phone's `initRecv` decrypts binary→phone frames), plus `decryptAppFrame`. The integration test's harness.
- `internal/relay/v2session_test.go:169-231` — `waitForEnvelopes`, `decodeNoiseMsg` (extracts ciphertext from a captured `noise_msg` — used for the cap assertion), `v2Recorder.snapshot()`.
- `internal/relay/v2session_test.go:2923` — `TestV2Session_Push_InterleavedWithReply_DecryptsUnderRace` — the closest existing "Push then decrypt phone-side" test; model the integration test on it.
- `internal/protocol/messaging_test.go:212-270` + `testdata/message_chunk.json` — the golden round-trip pattern (`readFixture`) for the new payload structs.

## Design

Two packages, three production files.

### 1. Wire vocabulary — `internal/protocol`

**Decision (the ticket leaves this to the architect): add bundle-specific type strings with a byte-generic payload shape.** Rationale: exactly one planned consumer (#813 debug-bundle verb); Simplicity-First. The *payload shape* (`{seq, data}` + `{total}`) is byte-generic, so a second byte-stream consumer would rename a string, not redesign the frame. Reusing `message_chunk`/`backfill_done` is rejected by the ticket (they carry `[]MessagePayload`).

`internal/protocol/codes.go` — new v2-only const block (mirror the resync/session_transition blocks; include the "MUST NOT be added to v1TypeSet; drift detector partitions in compat_test.go" doc):

```go
const (
    TypeDebugBundleChunk = "debug_bundle_chunk" // binary → phone, outbound v2 bundle chunk
    TypeDebugBundleDone  = "debug_bundle_done"  // binary → phone, outbound v2 bundle completion marker
)
```

`internal/protocol/messaging.go` — two payload structs (mirror `MessageChunkPayload`/`BackfillDonePayload`):

- `DebugBundleChunkPayload{ Seq int json:"seq"; Data []byte json:"data" }` — `Data []byte` auto-encodes as base64 std via `encoding/json`; the phone base64-decodes. `Seq` is 0-based, contiguous, ascending.
- `DebugBundleDonePayload{ Total int json:"total" }` — the exact chunk count; the receiver uses it to detect a truncated stream.

### 2. Streaming primitive — `internal/relay/v2bundlestream.go` (new file)

One new file holds the whole byte-stream concept: chunk-size const, chunker, `StreamBundle`, and the reassembly reference. It depends only on `internal/protocol` + `maxNoisePayloadBytes` (already in-package).

**Chunk-size constant** — the raw bundle bytes per chunk, chosen with headroom below the frame cap:

```go
// bundleChunkBytes bounds the raw bundle bytes per debug_bundle_chunk so the
// sealed noise_msg ciphertext stays under maxNoisePayloadBytes: base64 expands
// the payload 4/3, the JSON Envelope wrapper adds ~200 B, and the AEAD tag adds
// 16 B. 48000 → ~64200 B envJSON → ~64216 B ciphertext, ~1.3 KB under 65535.
// The invariant is ENFORCED by TestStreamBundle_EveryFrameWithinCap; if that
// ever fails, LOWER this constant — never raise it.
const bundleChunkBytes = 48000
```

The const is the human choice; the cap test is the deterministic enforcement — belt (conservative const) and suspenders (test), different fabric.

**Chunker** — pure, no manager:

- `bundleEnvelopes(blob []byte) ([]protocol.Envelope, error)` — splits `blob` into `ceil(len/bundleChunkBytes)` pieces; one `TypeDebugBundleChunk` envelope per piece with ascending `Seq` from 0; then one trailing `TypeDebugBundleDone` envelope with `Total` = chunk count. `EventID` left nil on every envelope (so `forwardEnvelope`'s dedup guard is inert). `ID` is non-load-bearing (set to `Seq`/`Total` for debuggability; the receiver keys on Type+Seq+Total, never ID). `TS = time.Now().UTC()` (matches `emitResync`/`emitRekeyRequest`). Returns the (practically-unreachable) `json.Marshal` error wrapped.
  - Empty blob → 0 chunks + `done{total:0}` (valid, round-trips to empty).

**Stream method** — rides `Push`:

- `func (m *V2SessionManager) StreamBundle(ctx context.Context, connID string, blob []byte) error` — builds envelopes via `bundleEnvelopes`, then `m.Push(ctx, connID, env)` for each in order; returns the first `Push` error (stops — a not-open conn means no point continuing). On success returns after **enqueue**, not after delivery (delivery is async on Run; failures are logged at debug by the drain path, matching the package's fire-and-forget posture). One content-free debug log line max (`event, conn_id, chunks, bytes` — counts only, never `Data`/blob).
  - Callable from any goroutine, including a future handler on the Run goroutine: `Push` never uses the handler-reply channel and never blocks, so this sidesteps both Context walls. This is the whole point of the ticket.

**Reassembly reference** — exported, pure; the receiver-contract spec + test oracle (required by AC#2 + the "fail cleanly" correctness note — you cannot test clean-failure without it):

- `func ReassembleBundle(frames []protocol.Envelope) ([]byte, error)` — walks `frames` in arrival order:
  - `TypeDebugBundleChunk`: decode payload; require `Seq == next` (0-based contiguous ascending) else error; append `Data`; `next++`.
  - `TypeDebugBundleDone`: decode payload; require `Total == next` (else truncated/count-mismatch error); return the concatenation. Stop.
  - any other Type: **skip** (tolerates interleaved `assistant_delta` etc. — the real phone filters by type).
  - frames exhausted with no done marker → error ("incomplete: missing completion marker").
  - On **any** failure return `(nil, err)` — never partial/corrupt bytes.

### Why `Push`, not a direct seal

`forwardEnvelope` reads `m.sessions`/`s.send` and MUST run on the Run goroutine. `Push` is the off-Run-safe front door to that path; the ticket explicitly says "ride the manager's own send path, the way other unsolicited daemon→client frames are sent." `StreamBundle`→`Push`→(Run)`drainOnce`→`forwardEnvelope` reuses all existing sealing, ordering, and the `V2StateOpen` gate with zero new concurrency surface.

## Concurrency model

- **Producer side (`StreamBundle`):** off-Run (tests here; #813 verb handler later). Each `Push` takes only the `pushMu` leaf lock, enqueues, and does a non-blocking `drainCh` signal — never blocks, never touches cipher state. Enqueues N+1 envelopes and returns.
- **Consumer side (Run goroutine):** `drainOnce` pops one envelope per select pass, FIFO per conn, sealing each under `s.send` — so chunks leave in enqueue order with a monotonic Noise send-nonce, and other conns / inbound frames / wakes are still serviced between chunks (no monopolization; same one-per-pass property as `drainReplayOnce`).
- **Ordering guarantee:** per-conn FIFO (`pushQueue`) + sequential seal (`forwardEnvelope` on one goroutine) ⇒ chunks arrive in order; the done marker, enqueued last, seals last. `assistant_delta`s may interleave in the queue but preserve relative order and are tolerated by `ReassembleBundle`'s type-skip.
- **No drop:** bundle chunks are control-class (`droppable == false`), so the drop policy never evicts them; under cap pressure they soft-overflow. All N+1 frames are delivered.

## Error handling

- **Conn not open / torn down:** `Push` returns `ErrConnNotFound`; `StreamBundle` returns it. A conn that closes *mid-stream* has its buffered chunks dropped by `forwardEnvelope`'s `V2StateOpen` gate at drain time — never sealed for an un-authenticated peer.
- **Seal/marshal failure at drain:** logged at debug by `drainOnce`, frame dropped (existing posture; realistically unreachable under correct flynn/noise). No payload bytes in the log.
- **Reassembly (receiver contract):** out-of-order seq, gap, duplicate, `Total` mismatch, missing marker, malformed payload → clean `(nil, err)`. The AEAD already guarantees per-frame content integrity (ChaChaPoly), so intra-chunk corruption is impossible on the wire; `Seq`+`Total` add deterministic gap/reorder/truncation detection — the two nets are different fabric (crypto vs. structural).

## Testing strategy

`internal/relay/v2bundlestream_test.go` (new):

- **`ReassembleBundle` table (pure, no manager):** happy round-trip; out-of-order (swap two chunk seqs) → err; gap (drop a middle chunk) → err; duplicate seq → err; `Total` ≠ chunk count → err; missing done marker → err; truncated (chunks, no marker) → err; interleaved `TypeMessage` frame between chunks → still reassembles; empty-blob round-trip. Assert failing cases return `nil` bytes.
- **`bundleEnvelopes` round-trip (pure):** for sizes {0, 1, `bundleChunkBytes`, `bundleChunkBytes+1`, several chunks} assert `ReassembleBundle(bundleEnvelopes(blob)) == blob` (`bytes.Equal`), every chunk payload `len(Data) <= bundleChunkBytes`, seqs are 0..n-1, last envelope is `TypeDebugBundleDone` with `Total == n`.
- **`TestStreamBundle_MultiChunk` (integration, AC#1/#2):** `driveToOpen`; `StreamBundle` a random blob > 3×`bundleChunkBytes`; collect the recorded stream frames (`rec.snapshot()`, skipping the handshake `noise_resp`); decrypt each under `initRecv`; assert reassembly == blob and the last inner envelope is `TypeDebugBundleDone{Total: n}`.
- **`TestStreamBundle_EveryFrameWithinCap` (AC#1, the deterministic net):** same setup; for every stream frame assert `len(decodeNoiseMsg(frame)) <= maxNoisePayloadBytes`.
- **`TestStreamBundle_SingleFrame` (AC#3):** blob < `bundleChunkBytes` → exactly 1 chunk + 1 done marker; reassembles; marker present.
- **`TestStreamBundle_NoBytesLogged` (AC#4):** put a recognizable byte pattern in the blob, run `StreamBundle` under a capturing `slog.Handler`, assert no emitted record's message or attr values contain the blob bytes or their base64.
- **`TestStreamBundle_ConnNotOpen`:** `StreamBundle` to an unknown conn → `ErrConnNotFound`, no frames emitted.

`internal/protocol` (mirror the existing golden pattern):

- `TestDebugBundleChunkPayload_RoundTrip` / `TestDebugBundleDonePayload_RoundTrip` against `testdata/debug_bundle_chunk.json` / `debug_bundle_done.json`.
- **Extend `compat_test.go`:** add both types to `v2OnlyTypes`, to `TestTypeConstants_V1V2Partition`'s `all` list, and to `TestIsV1Compatible`'s "-rejected" cases. Do **not** add to `v1TypeSet`.

Run `go test -race ./internal/relay/... ./internal/protocol/...` and `staticcheck ./...`.

## Open questions

- **Chunk-size tuning.** 48000 is conservative (~1.3 KB headroom). If #813's real bundles show the cap test is comfortable, it could rise toward ~48900, but there's no evidence-based reason to — leave it until a measured need appears.
- **Large-blob buffering (bounded, deferred).** A multi-MB blob buffers N+1 control-class envelopes in the `pushQueue` (soft-overflow past the 256 cap, since chunks never drop) — memory bound is `blob size × ~1.33` (base64), transient, released as Run drains. Acceptable: debug bundles are operator-initiated, infrequent, and already fully in RAM (#811 assembles in-memory). Chunk-level flow control is out of scope with no observed need.
- **Who may trigger a stream** (the DoS-trigger surface) and any per-request authz is **#813's** concern — this primitive is unwired and faithfully streams whatever blob + conn it's handed.

---

## Security review (self-review — label `security-sensitive`)

**Reviewer:** architect (self-review; `agents/architect/security-review.md` not synced into this worktree — inline pass using the standard adversarial categories, per the #707/#777/#702 precedent).
**Date:** 2026-07-07
**Scope:** a new outbound streaming primitive that seals content bytes as AEAD frames on the internet-exposed mobile surface. Adversarial pass:

- **Trust boundaries / untrusted input.** `StreamBundle(blob)` streams a **daemon-produced** blob (the #811 assembler output), not attacker input. `connID` addresses an already-open, token-validated conn; `forwardEnvelope`'s `V2StateOpen` gate is applied to **every** chunk at drain time, so a conn that closed or de-authed mid-stream never receives a sealed chunk. `ReassembleBundle` parses frames, but it has **no daemon-inbound caller** — it is the receiver-contract reference + test oracle, run in production only phone-side (out of repo). **No new attacker-reachable input path.** No new inbound parsing of phone bytes.
- **Nonce / seal single-writer (the catastrophic risk).** Every chunk seals via `Push` → `drainOnce` → `forwardEnvelope`, all on the single Run goroutine, reading `s.send` at execution time. Each envelope is enqueued once and popped once (`q.items[1:]`), forwarded once — **no double-seal, no nonce reuse**. A re-key swap between two chunk forwards composes (whole-old-key or whole-new-key, never torn), exactly as for the existing push stream. `StreamBundle` itself never touches `s.send`. **Invariant preserved, not weakened.**
- **Frame cap (AC#1).** A chunk whose sealed ciphertext exceeded 65535 would be rejected phone-side (4421) and break the stream. `bundleChunkBytes` is derived with ~1.3 KB headroom and the invariant is enforced by a deterministic per-frame cap test — not left to the constant alone.
- **Secret / content hygiene (AC#4).** `StreamBundle` logs only content-free counts (`conn_id, chunks, bytes`). `Push`/`drainOnce`/`forwardEnvelope` already never log payload, ciphertext, or key bytes. A dedicated test asserts the blob bytes never appear in any emitted log record. **No new secret-leaking field.**
- **Reassembly integrity (correctness note).** `ReassembleBundle` returns `(nil, err)` on out-of-order/gap/duplicate/truncation/count-mismatch/missing-marker — **never** partial or corrupted output. AEAD guarantees per-frame integrity; `Seq`+`Total` add structural gap/reorder detection. Two independent nets, different fabric.
- **Resource exhaustion / DoS.** A large blob buffers N+1 control-class envelopes (never dropped) — bounded by the caller-supplied, finite blob size (operator-initiated, infrequent, already in RAM). No phone-driven amplification exists **in this ticket**: the primitive is unwired, so no remote input can trigger a stream. The request→stream trigger surface and its authz are #813's responsibility — flagged here so it isn't lost, not a FAIL for this scope.
- **Fail-closed.** Not-open conn → `ErrConnNotFound`, no seal. Session closed mid-stream → `V2StateOpen` gate drops buffered chunks at drain, never sealed for an un-authenticated peer. Reassembly defaults to error, never best-effort bytes.

**Verdict: PASS.** No FAIL findings. The primitive is a Run-goroutine-local reuse of the existing sealed push path; it preserves every cryptographic invariant, adds no attacker-reachable input path, leaks no content, and its only DoS surface (who may trigger a stream) is deferred to the wiring ticket #813 per Evidence-Based Fix Selection.
