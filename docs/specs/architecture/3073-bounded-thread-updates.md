# Bounded thread update encoding (#3073)

## Files read

- `internal/protocol/thread.go` → `ThreadItem` and the three update DTOs: preserve all fields and raw JSON replacement semantics.
- `internal/protocol/envelope.go` → `Envelope`: account for all optional stamp fields, raw session metadata and timestamp width.
- `docs/knowledge/INDEX.md` and `docs/knowledge/features/protocol-package.md`: leaf package, content-free errors, no production activation.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: re-marshal decoded envelopes, assert omission separately and measure escaped raw JSON.
- `docs/knowledge/decisions/042-daemon-built-thread.md` § Update messages: 65519-byte limit and catch-up on revision mismatch.
- `docs/protocol-mobile.md` § Security model: untrusted display content stays inside authenticated encryption; encoding grants no authority.
- `CODING-STYLE.md`: pure table-driven protocol tests, stdlib only, errors as values.

## Context

Folded items exceed individual source-event caps. Add a detached, pure codec for later live publication (#3077) and full-item replies (#2963), without emitting anything now. Existing DTOs and legacy limits remain unchanged. The generic serialized-payload continuation contract deserves documentation alongside ADR 042; no separate decision record is required.

## Design

`EncodeThreadUpdate(Envelope, any) ([][]byte, error)` accepts exactly the three landed update value types. It selects the corresponding envelope type and replaces the supplied payload. A fitting logical update produces its ordinary envelope. Oversized updates produce `ThreadUpdatePart` payloads under the original type, with repeated conversation, epoch, version, item ID, base revision (for change/append) and target revision.

`ThreadContinuation` identifies the logical update with a SHA-256 digest of type plus serialized logical payload, zero-based part index, byte offset, total serialized payload bytes and final flag. `ThreadUpdatePart.Data` is a JSON string containing a UTF-8 fragment of that logical payload. First and continuation parts use the same representation; index zero starts assembly. This handles oversized metadata, unknown content and arbitrary patch values without field-specific truncation. Concatenation followed by JSON decoding preserves original values, omission/clears, identity, kind and placement. Later full-item replies can use the same added-payload assembly representation.

Continuation presence MUST be checked before decoding an ordinary DTO. These are assembly fragments, not ordinary item changes or text suffixes. Consumers buffer without applying fields, rev or version; accept only the next index and exact byte offset for the same type, digest and repeated metadata. The final flag completes only at the declared total length with a matching digest and decoded routing/revision metadata. Change/append application then requires the currently completed item's `base_rev`; append applies only to message text. Any gap, reordering, metadata disagreement, corruption or precondition mismatch discards assembly and requires catch-up. Replaying completed sequences never appends twice: the original base revision no longer matches. Full-item additions/catch-up replace by their original item identity and revision, not fragment indices.

Measure serialized envelopes, including double escaping of fragment strings. Reserve maximum uint64 widths for ID and all three optional numeric stamps, a maximum-width valid RFC3339Nano timestamp and both optional boolean flags. Preserve and measure supplied `SessionID` exactly. Callers may stamp those reserved fixed-width fields after splitting; they MUST supply all variable session metadata before encoding and MUST NOT enlarge it or alter payload/type afterwards. The relay's external `RoutingEnvelope` is outside the encrypted application-frame cap.

Measure empty-part overhead with maximum progress-number widths and the longer final=false representation. Binary search the largest string fragment fitting the remaining measured budget, backing down to a UTF-8 boundary. Publish results only after the entire sequence succeeds. Malformed JSON, invalid UTF-8 and invalid envelope metadata return content-free sentinels. Metadata that leaves no room for the next fragment returns a distinct size sentinel with nil output.

Sizing re-check: approximately 250 production + 360 test/helper + 85 plan lines, under 800 total; two exported structs, zero consumer updates, four criteria, fewer than ten codec rejection branches. Current remote feature branches #2873/#2882 do not overlap protocol files. New files isolate this codec from other protocol additions.

## Concurrency model

No goroutines, I/O, logging or shared mutable state. Inputs are read only for the duration of the call; callers must not mutate them concurrently. Returned serialized envelopes own their bytes.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Multiple parts of one logical update; only final ordered assembly applies | `TestThreadUpdateAssembly` |
| Missing middle then final / out-of-order continuation | `TestThreadUpdateAssemblyRejects` |
| Wrong base revision or repeated completed append/change | `TestThreadUpdateAssemblyRejects` |
| Same item updated again using a new revision | `TestThreadUpdateAssembly` |
| Routing/epoch reuse or corruption cannot join unrelated updates | `TestThreadUpdateAssemblyRejects` |
| Detached input used repeatedly, including maps and raw JSON | `TestThreadUpdateRoundTrip` |

## Error handling

`ErrInvalidThreadUpdate` covers unsupported DTOs, malformed JSON/UTF-8 and invalid timestamp/session JSON. `ErrThreadUpdateMetadataTooLarge` signals insufficient repeated metadata budget. No errors contain source JSON, field values or host paths; all failures return nil sequences. Receiver resource limits and timeouts are owned by later app reducers; the codec operates on already detached daemon data, and performs work proportional to its serialized size.

## Testing strategy

Write tests first and observe failure before implementation. Table-driven round trips cover fitting updates, large assistant/user text, tool/agent accumulated values, unknown nested content, oversized summaries and every other variable item field. Compare entire decoded logical DTOs/JSON using number-preserving decoding; inspect patches for omitted keys and explicit empty/false/zero/null replacement values. Verify input bytes/maps stay unchanged and every returned envelope is independently valid JSON.

Boundary tests derive sizes from the actual cap: exactly fitting ordinary envelopes and one byte beyond, escaped routing/session/item/raw JSON fields, maximum numeric/progress widths, timestamp/optional stamp growth and oversized repeated metadata. Record measured overhead. A test-only receiver enforces the documented assembly contract and applies ordinary replacement/append semantics atomically, proving incomplete and rejected assemblies never advance completed revision/version. Run `go test -race ./internal/protocol/...`, `go vet ./...`, `go build ./cmd/pyry`. Full-module tests belong to the verifier.

## Open questions

None. Fragment JSON strings deliberately trade additional escaping overhead for a simple lossless, kind-independent representation.

## Documentation handoff

Pending documentation stage: update `docs/protocol-mobile.md`, “Daemon thread updates (v2, declarations only)” → “Full thread item” and the three update sections. Document measured byte budgets, envelope overhead, first-part/continuation metadata and completion rules, revision application, missing/out-of-order-part detection and catch-up repair, including explicit failure when routing metadata cannot fit. Preserve ordinary append/patch meanings and mark emission pending until daemon activation in both catalog rows and section text.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] SHOULD FIX: `EncodeThreadUpdate` must reject invalid UTF-8 before JSON encoding can replace bytes, and suppress raw marshal errors. The codec accepts detached DTOs, not authenticated/authorized requests.
- [Tokens] No credential generation/storage. All strings and raw values can contain secrets; output remains application plaintext for the later encrypted sender, never logs/errors.
- [File operations / Subprocesses] No files, paths, processes or environment access.
- [Cryptography] SHA-256 is only assembly identity/integrity, not authentication; Noise remains the transport boundary.
- [Network and I/O] SHOULD FIX: measure all envelope fields and continuation double escaping, reserve later numeric/timestamp/boolean stamps, and return no partial result on size failure. No socket operations.
- [Errors/logs/telemetry] Two fixed sentinels only; no logger or telemetry dependency.
- [Concurrency] Local buffers only, immutable-input contract; no asynchronous work to shut down.
- [Threat model] Inert content is never interpreted. OUT OF SCOPE: authenticated publication in #3077, reply/page limits in #2963, client assembly memory/time limits and render sanitization in later app reducers.

**Reviewer:** builder (self-review)
**Date:** 2026-10-10
