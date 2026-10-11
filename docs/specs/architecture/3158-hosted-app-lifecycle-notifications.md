# Committed hosted-app lifecycle notifications

## Files read

- `internal/relay/v2session_hosted.go` → `hostedEnqueue`, `hostedDeliver`, `retireHosted`: bounded ordered discovery/control lane and list cancellation.
- `internal/relay/v2session.go` → `Run`, `closeWith`, `teardown`: single owner, shutdown and generation retirement.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit`: negotiated authenticated generation installation.
- `internal/relay/v2session_hosted_test.go` → `testHostedPeer`: authenticated decrypted-wire fixtures and sender-ID assertions.
- `internal/relay/v2session_hosted_delivery_test.go` → `TestHostedDiscoveryOrderedDelivery`: controlled send blocking and independent-peer progress.
- `internal/apps/lifecycle.go` → `Change`, `Subscribe`: detached current-process committed records, ordered synchronous callbacks under registry mutex.
- `internal/protocol/hosted_apps.go` → `HostedAppRecord`, `AppRemovedPayload`: required nullable fields and tombstones.
- `docs/knowledge/features/apps-package.md` § Concurrency and persistence: callbacks cannot re-enter registry/unsubscribe; candidate manifests are not committed display metadata.
- `docs/knowledge/features/v2-session-manager-concurrency.md` § Hosted discovery and ordered delivery: cipher/ID ownership, cancellable adapter and read-capacity retention.
- `docs/knowledge/features/development-verification.md`: decrypted bytes and controlled completion barriers distinguish actual delivery from admission.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Capability and envelope vocabulary, Errors and lifecycle test obligations, Shared JSON examples: host authority, eligibility, omission and fresh discovery.
- `CODING-STYLE.md`: shutdown paths, contract comments and focused race tests.

## Context

Paired viewers need committed registration changes before or after listing apps. The discovery lane exists; daemon registry subscription ownership remains #3139. No production readiness change or decision record is needed. No overlapping feature branches touched the planned production files at planning time.

## Design

Expose `V2SessionManager.HostedAppsChanged(apps.Change)` as a registry-compatible callback. Reject foreign-host changes using immutable manager configuration. Map only committed record fields into `HostedAppRecord`, or removal identity/revision into `AppRemovedPayload`; share record projection with discovery pages. An explicit unsolicited output flag omits correlation while retaining discovery sender IDs and metadata omissions.

Install a bounded callback inbox for each negotiated authenticated connection at handshake, before its first list. A leaf manager mutex protects inbox membership, pending lifecycle outputs, aggregate reservations and overflow/retirement flags. Each inbox identifies its original session by pointer; callbacks never access mutable session or cipher state. Reserve one of 16 total output slots before callback admission or discovery/control enqueue, counting callback pending, Run queue and active send together. Release reservations at delivery completion or removal of a cancelled queued list page. Overflow clears queued callback work, cancels active delivery and signals Run to close that original generation. No callback worker, peer I/O, registry call or unsubscribe is allowed.

Run consumes a coalesced wake, transfers bounded pending outputs to the existing lane, checks current generation and paired authorization, and delivers in registry callback order. Closed inboxes admit nothing. Terminal close delivery also has a generation context cancelled when a replacement is installed; a delayed predecessor close cannot affect a reused connection ID. Shutdown disables admission and retires inboxes before Run returns. Closed generation entries may remain until replacement; they retain no envelopes and are removed on terminal completion when still current.

Sizing: one deliverable; approximately 720 written lines including plan, production and tests, no new exported types/interfaces, zero consumer migrations, four acceptance criteria and at most eight new rejection/retirement branches. Rechecked against this written plan: within all five limits.

## Concurrency model

Registry callbacks hold only the leaf inbox mutex, bounded by connected peers and 16 entries per peer, and signal without blocking. Run alone owns session/cipher fields and seals output. Existing one-active-send-per-connection workers honor cancellation through the sender adapter. Inbox delivery cancellation and generation close cancellation are context operations safe under the leaf mutex; it never spans registry, device lookup or I/O. No new goroutine is created by a callback. Existing registry unsubscribe remains daemon-owned.

## State transitions and identity reuse

| Event | Race-enabled test |
|---|---|
| Concurrent commits, candidate update and removal | `TestHostedLifecycleCommittedWire` |
| Rejected/no-op/failed persistence, foreign host and unnegotiated peer | `TestHostedLifecycleCommitSuppression`, `TestHostedLifecycleHostIsolation` |
| Slow-peer overflow while another peer progresses | `TestHostedLifecycleOverflow` |
| List cancellation with ongoing notifications | `TestHostedLifecycleListCancellation` |
| Disconnect/revocation/shutdown with stalled delivery | `TestHostedLifecycleRetirement` |
| Reconnect reusing connection/request IDs with delayed output/completion/close | `TestHostedLifecycleReconnect` |

## Error handling

Committed registry changes are trusted validated data, not caller-selectable network routing. Persistence failure and rejected/no-op mutations never invoke the consumer. Delivery failure, capacity exhaustion or revoked pairing retire the affected generation; fresh listing recovers durable state. Calls after shutdown are inert. Safe committed error code/message are copied without parser/process/I/O diagnostics.

## Testing strategy

Write lifecycle fake-session tests first and observe missing callback support fail. Reuse real registry and authenticated discovery fixtures; decrypt whole envelopes and assert monotonically increasing revisions, required nulls, omissions, no correlation and unique safe IDs. Controlled sender/read gates prove callback completion, other-peer progress and retirement before release; then verify no late output. Use separate registry roots and authenticated host managers sharing an app ID. No live Claude proof is required. Run `go test -race ./internal/relay/...`, `go vet ./...`, and `go build -o /tmp/builder-3158/pyry ./cmd/pyry`; verifier owns the full-module gate.

## Open questions

None. Subscription installation/unsubscribe and production capability activation remain explicitly outside this slice.

## Documentation handoff

Pending for documentation stage:
- `docs/protocol-mobile.md` § Hosted apps (v2): mark both lifecycle table rows and prose implemented; describe committed revision ordering, tombstones, paired-peer eligibility, overflow recovery and no replay.
- `docs/hosted-apps.md` § Manifest and identity: describe committed lifecycle discovery notifications and reconnect refresh.
- State daemon subscription wiring remains #3139 and production capability activation still requires supervision/resource integration.

## Security review

**Verdict:** PASS

**Findings:**
- Trust boundaries: `HostedAppsChanged` requires the manager host ID; handshake installs negotiated recipients and Run rechecks `hostedAuthorized` before sealing. Foreign changes disclose no records.
- Tokens/secrets: reuse paired authentication and revocation; callbacks and logs carry no token, payload or error diagnostic.
- Files: no new production file I/O; registry durable persistence remains the event source.
- Subprocesses: none; supervision belongs to later integration.
- Cryptography: existing Noise sealing and safe sender-ID allocation remain exclusively on Run; notifications bypass replay/history.
- Network/I/O: aggregate 16-slot reservation includes handoff and active delivery; cancellation-aware sender and existing timeout bound stalled peers.
- Errors/logs: project only committed safe code/message; content-free overflow/retirement diagnostics.
- Concurrency: generation pointers and cancellation contexts prevent old queued outputs, completion signals and delayed closes affecting replacements; no lock spans registry or network I/O.
- Threat model: content-blind relay receives existing Noise ciphertext; authenticated host-scoped peers only. OUT OF SCOPE: daemon subscription ownership #3139 and production activation/supervision/resource integration.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-11

## Revisions

- 2026-10-11: Generation identity is carried by the session's distinct inbox reference, rather than exposing a session pointer to callbacks. Retired inboxes are removed immediately. Terminal sends use a separate bounded-lifetime close context, cancelled at replacement session creation before its handshake sends anything; completion removes only its own context. This prevents a delayed close from reaching a replacement even during the new handshake.
- 2026-10-11: Clear removed queue entries as well as releasing reservations, so cancelled pages and completed outputs retain no stale envelope references in slice backing arrays.
