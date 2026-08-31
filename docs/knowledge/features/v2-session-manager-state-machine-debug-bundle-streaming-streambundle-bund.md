# Debug-bundle streaming (#812) — `StreamBundle` + `bundleEnvelopes` + `ReassembleBundle`

`StreamBundle(ctx, connID, blob) error` moves an arbitrarily-large `[]byte` (the
[#811 debug-bundle assembler](debugbundle-package.md)'s output, routinely
multi-MB) to one addressed, open, authenticated conn as **ordered, cap-respecting
chunks ending in a completion marker**. It is the transport primitive the #813
request verb drives (wired in [§ Inbound debug-bundle request](#inbound-debug-bundle-request-813--request_debug_bundle--debugbundler--streambundle));
this slice shipped it **unwired**, test-driven only. New
file `internal/relay/v2bundlestream.go` holds the whole byte-stream concept —
the chunk-size const, the pure chunker, the method, and the reassembly reference
— depending only on `internal/protocol` + the in-package `maxNoisePayloadBytes`.
`security-sensitive` (content bytes sealed as AEAD frames on the mobile surface);
architect + code-review security passes both **PASS**. See
[`codebase/812.md`](../codebase/812.md).

**Two walls a bundle cannot ride a normal reply through, and why `Push` clears
both.** A `noise_msg`'s decoded ciphertext is capped at `maxNoisePayloadBytes =
65535` — a multi-MB blob is not one sealed frame — and the per-frame
handler-reply channel (`handlerOutboundBuf = 8`) still means one handler
invocation, however many replies it emits, occupies `Run`'s dispatch loop for
its whole duration (routing a multi-MB blob through it would stall every other
`conn_id` for the streaming duration — [#909](../codebase/909.md) fixed the
>8-reply *deadlock* on this channel, not this head-of-line cost). `StreamBundle` builds
N+1 envelopes with `bundleEnvelopes` and enqueues each via
[`Push`](#concurrency-safe-unsolicited-push-571--push-method--push-funnel) — the
manager's **asynchronous** `Push` → `drainOnce` → `forwardEnvelope` path, the same
path every unsolicited daemon → client frame uses (#571 push, #618 snapshot, #647
resync). `Push` never blocks and never touches `s.send`, so `StreamBundle` is
callable from any goroutine, **including a future #813 handler on the `Run`
goroutine** (its cap-1 `drainCh` signal is non-blocking) — sidestepping both walls
with zero new concurrency surface. It returns on **enqueue**, not delivery, and
returns the first `Push` error and stops (a not-open conn → `ErrConnNotFound`).

**`bundleEnvelopes(blob) ([]protocol.Envelope, error)`** (pure, no manager) splits
`blob` into `ceil(len/bundleChunkBytes)` `TypeDebugBundleChunk` envelopes with
ascending 0-based `Seq`, then one trailing `TypeDebugBundleDone{Total: n}`. An
**empty blob** → 0 chunks + `done{total:0}` (a valid stream reassembling to
empty). `EventID` is left **nil** on every envelope, so
[`forwardEnvelope`](#concurrency-safe-unsolicited-push-571--push-method--push-funnel)'s
reconnect-replay dedup guard is inert for bundle frames; `ID` is non-load-bearing
(set to `Seq`/`Total` for debuggability — the receiver keys on Type+Seq+Total).

**`bundleChunkBytes = 48000`** bounds the raw bundle bytes per chunk with ~1.3 KB
headroom below the frame cap (base64 ×4/3 + ~200 B `Envelope` JSON wrapper + 16 B
AEAD tag → ~64216 B ciphertext < 65535). **Belt-and-suspenders, different
fabric:** the conservative const is the belt; `TestStreamBundle_EveryFrameWithinCap`
(decrypts every emitted frame, asserts `len(ciphertext) <= maxNoisePayloadBytes`)
is the deterministic suspenders. The const's doc-comment pins the rule — **if the
cap test ever fails, LOWER the const, never raise it.**

**No drop, in order (AC#2).** Bundle frames are **control-class** (`Type !=
TypeAssistantDelta`), so the `pushQueue` drop policy never evicts them — under cap
pressure they soft-overflow. Per-conn FIFO + sequential seal on the single `Run`
goroutine ⇒ chunks arrive in enqueue order with a monotonic Noise send-nonce; the
`done` marker, enqueued last, seals last. An open session that closes mid-stream
has its still-buffered chunks dropped by `forwardEnvelope`'s `V2StateOpen` gate at
drain time — never sealed for an un-authenticated peer.

**`ReassembleBundle(frames) ([]byte, error)`** is the exported, pure
**receiver-contract reference and test oracle** — production use is phone-side
(out of repo); the daemon has **no inbound caller**. It walks `frames` in arrival
order: a `TypeDebugBundleChunk` must carry `Seq ==` the count of chunks already
seen (0-based contiguous ascending, else reorder/gap/duplicate is rejected); a
`TypeDebugBundleDone` must carry `Total ==` that count (else truncated /
count-mismatch is rejected); any other type is **skipped** (tolerates an
interleaved `assistant_delta`); frames exhausted with no marker is incomplete. On
**any** failure it returns `(nil, err)` — never partial or corrupt bytes. **Two
independent integrity nets, different fabric:** the AEAD (ChaChaPoly) guarantees
per-frame *content* integrity on the wire (intra-chunk corruption is impossible),
while `Seq` + `Total` add deterministic *structural* gap / reorder / truncation
detection. It exists in-repo because you cannot test "fail cleanly on a truncated
/ reordered stream" (the correctness note) without a reassembler to fail.

**Log discipline (AC#4).** `StreamBundle` logs one content-free debug line
(`event=v2.bundle.stream`, `conn_id`, `chunks`, `bytes` — counts only, never the
streamed bytes or their base64); `Push` / `drainOnce` / `forwardEnvelope` already
never log payload/ciphertext/key bytes. `TestStreamBundle_NoBytesLogged` seeds a
recognizable byte pattern, captures under a `LevelDebug` handler, first asserts
the `v2.bundle.stream` line **is** present (non-vacuous), then asserts the blob
bytes and their base64 appear in no record. The wire vocabulary
(`TypeDebugBundleChunk` / `TypeDebugBundleDone` + the two payloads) lives in
[`internal/protocol`](protocol-package.md#debug-bundle-streaming-payloads-812) and
[protocol-mobile.md § Debug bundle](../../protocol-mobile.md#debug-bundle-v2).
