# Authenticated cancellable host-app discovery (#3157)

## Files read

- `internal/relay/v2session.go` → `dispatchAppFrame`, `Run`, `teardown`, `closeWith`, `send`: authenticated routing, sole crypto owner and retirement.
- `internal/relay/v2session_handshake.go` → `negotiateCapabilities`, `handleNoiseInit`: fail-closed admission and hello_ack ID 1.
- `internal/relay/v2session_seams.go` → `V2SessionConfig`: consumer-owned discovery/readiness/delivery seams; no daemon wiring.
- `internal/relay/v2session_appframe.go` → `forwardAppReply`, `sealErrorPayload`: existing errors use fixed IDs and synchronous delivery.
- `internal/relay/v2session_push.go` → `forwardEnvelopeSource`: ordering and transport-down gates.
- `internal/relay/v2session_test.go`, `v2session_modal_test.go`: existing paired registry, Noise, recorder and encrypted frame helpers.
- `internal/apps/registry.go` → `Registry.List`, `Record`: detached atomic committed snapshot, sorted IDs; mutex can block.
- `internal/protocol/hosted_apps.go`, `envelope.go`: required nullable keys and safe correlations.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Capability and envelope vocabulary, Errors and lifecycle test obligations, Shared JSON examples: normative wire rules.
- `docs/knowledge/features/apps-package.md`: committed fields must come from records, not candidate manifests; exact schema before decoding avoids aliases.
- `docs/knowledge/features/relay-package.md`, `v2-session-manager-concurrency.md`, `protocol-package.md`, `development-verification.md`: Run-owned ciphers, completion barriers, decrypted byte assertions.
- `CODING-STYLE.md`: stdlib tests, contract comments and bounded goroutines.

## Context

Paired clients need durable host-scoped discovery independently of channel capabilities. Registry/protocol dependencies are merged. Notifications, resources and production composition remain separate tickets. No fetched concurrent feature branch overlaps the selected relay files.

The refiner estimates ~1100 written lines, exceeding 800. Actual lineage #3121 → #3138 → #3157 and existing needs-human:sizing require building under the grandchild exception. Candidate split already recorded on the issue: authenticated validation/delivery admission, then paging/cancellation. Five acceptance criteria, at most two exported interfaces/types, no required existing consumer migration; reject branches are validation, unsupported, cursor invalid/changed, busy, capacity, auth, delivery and seal failure. Recounted plan preserves this exception.

## Design

Add consumer interface `HostedAppsDiscovery.List() ([]apps.Record, uint64)`, readiness callback attesting the entire registration/supervision/resource integration, and a context-aware hosted delivery callback on `V2SessionConfig`. All three dependencies are required for capability admission. The delivery callback must return on context cancellation/deadline and must not publish cancelled work; the old context-free callback cannot establish that contract. Production supplies none by default. Future daemon composition must install the cancellable adapter before attesting readiness.

Recognize all six declared types at `dispatchAppFrame`; validate exact canonical envelope/payload fields from a duplicate-free UTF-8 JSON tree before decoding individual known values. Unknown additive keys are ignored without struct alias matching. Only list/cancel are requests. Correlatable invalid input gets a static app.invalid_request; unusable/ambiguous IDs close through protocol rejection. Valid resource cancel gets protocol.unsupported.

Each connection owns an opaque random continuation token and its host/revision/last-ID/expiry state. Tokens are connection-owned, cannot select a host and are invalid after reconnect. Each continuation re-reads an atomic snapshot and checks revision before selecting up to 16 records. First page replaces a completed walk. Map committed record fields directly with required nulls.

Discovery maintains one pending page including its read and reply. Cancel invalidates the walk and queued page, but blocked read capacity remains occupied until the original reader exits. A per-connection ordered delivery queue holds at most 16 envelopes; one delivery executes at a time with deadline/cancellation. Crypto remains Run-owned. Queue entries are unsealed until selected; cancelled queued pages spend no nonce. Already sealed delivery is irreversible: cancelling it closes the connection rather than delivering a later frame across a nonce gap. All sends on a hosted-enabled connection use the same delivery lane. Failure/overflow closes only the affected connection. Sender IDs start after hello_ack and are assigned on Run.

## Concurrency model

Run owns mutable session, paging, queue and Noise state. One registry reader may be outstanding per connection; it returns detached results via a completion channel and never reads session state. Cancellation does not replace a blocked reader. The reader exits when List returns; its handoff escapes on session or Run shutdown. One context-aware delivery worker executes a bounded queue head, returns via the same completion mechanism, and exits on cancellation/deadline. No wait for these operations occurs on Run. Teardown retires queues and delivery contexts immediately; late outcomes are checked against original session and pending pointers. Shutdown retires discovery state even when Run ends via Frames closure.

## State transitions and identity reuse

| Event | Race-enabled coverage |
|---|---|
| completed page, continuation, fresh walk | `TestHostedDiscoveryPaging` |
| cursor expiry/revision drift/host exchange | `TestHostedDiscoveryCursors` |
| disconnect/reconnect with same connection ID | `TestHostedDiscoveryRetirement` |
| repeated cancel while List is blocked | `TestHostedDiscoveryBlockedRead` |
| duplicate/unknown/finished cancel and resource cancel | `TestHostedDiscoveryCancellation` |
| blocked delivery, overflow and delivery error | `TestHostedDiscoveryDelivery` |
| revocation and shutdown during read/delivery | `TestHostedDiscoveryRetirement` |

## Error handling

Only static contract errors with required code/message/retryable and optional retry_after_s=1 for app.busy. No payloads, cursor bytes, paths or credentials in logs. Revoked membership is checked on request and completion/delivery; unavailable transport closes before sealing. Queue overflow and sealed failure close without silently dropping controls.

## Testing strategy

Write encrypted fake-session tests first and observe failure. Reuse handshake/Noise/recorder helpers, inspect decrypted raw JSON for required nulls, absent metadata, fresh IDs and correlation. Use owned gates to prove control progress before blocked work is released, per-connection isolation and bounded repeated cancellation. Run focused tests then `go test -race ./internal/relay/...`, `go vet ./...`, and `go build -o /tmp/builder-3157/pyry ./cmd/pyry`. Full-module gate belongs to verifier; no live Claude acceptance.

## Open questions

None. A context-aware delivery dependency is necessary because cancellation cannot interrupt the existing context-free callback.

## Documentation handoff

Pending for documentation stage:

- `docs/protocol-mobile.md` § Hosted apps (v2): update table rows and prose for implemented list paging, 30-second cursor lifetime and connection validity, pending-page capacity, list cancellation, errors/correlation and paired-peer gating. Lifecycle producers remain pending #3158; production activation requires supervision/resource integration and cancellable transport wiring; resource-scoped cancellation remains #3123.
- `docs/hosted-apps.md` § Manifest and identity: describe atomic discovery revisions and host isolation.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: strict tree validation precedes typed canonical field parsing; paired membership is rechecked before data delivery. Unknown fields grant no authority.
- Tokens/secrets: continuation tokens use crypto/rand, remain only in connection memory, expire after 30 seconds, and never reach logs. Pairing credentials use existing Noise authentication.
- Files: no new file operations; List reads the existing committed registry snapshot.
- Subprocesses: none.
- Cryptography: existing Noise ciphers remain Run-owned; random opaque tokens use standard crypto/rand. No sealing before delivery capacity exists; failure after sealing closes.
- Network/I/O: reuse inbound encrypted-frame cap; bounded queue and delivery deadline; context-aware callback is mandatory for activated discovery.
- Errors/logs: static messages and minimal error DTO, no remote content in errors/logs.
- Concurrency: no mutex across List or delivery; Run exclusively owns state; blocked readers retain capacity and cannot multiply on cancellation.
- Threat model: host implicit in authenticated connection; cursors cannot route hosts, app data never enters history. OUT OF SCOPE: resource routing #3123, lifecycle notifications #3158 and daemon supervision/composition #3139/#3122.

**Reviewer:** builder (self-review)
**Date:** 2026-10-10
