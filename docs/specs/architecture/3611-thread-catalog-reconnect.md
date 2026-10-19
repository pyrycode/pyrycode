# Thread catalog recovery on reconnect

## Files read
- `cmd/pyry/daemon_live_relay.go`: `daemonRelaySnapshot`, `installDaemonLiveRelay`, `daemonLiveCurrent` own supplied reconciliation and late eligibility.
- `cmd/pyry/session_model_list.go`: `modelListResultFor` preserves bound/bootstrap/saved precedence and canonical inventory.
- `cmd/pyry/daemon_live_inventory.go`: `cachedModelVocabularySource` preserves original producer evidence.
- `cmd/pyry/daemon_control_live.go`: `liveInventoryResult`, `daemonLiveOperation.complete` separate target ordering from retention authority.
- `cmd/pyry/daemon_live_state.go`: `liveConversation`, `advanceLocked`, `remove` own monotonic generations and retained revisions.
- `internal/relay/v2session_livestate.go`: `forwardLiveState`, `drainLiveOnce` enforce recipient access, projection, per-generation clears and late freshness checks.
- `cmd/pyry/daemon_model_catalog_test.go`, `daemon_inventory_ingress_test.go`, `daemon_live_relay_test.go`, `agent_switch_frames_test.go`: authenticated wire, parser/hold and finite snapshot fixtures.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md`: borrowed evidence must remain detached; fresh resolution and captured stale delivery are different operations.
- `docs/knowledge/features/relay-package.md`, `v2-session-manager.md`, `v2-session-manager-state-machine-connect-time-model-list-reconcile-retain.md`, `protocol-package.md`, `development-verification.md`: supplied versus legacy delivery and raw protocol assertions.
- `docs/protocol-mobile.md` Security model: authenticated encrypted delivery and replay threats.

## Context
Thread handshakes bypass legacy catalog reconciliation, leaving fresh targets without their own inventory unable to recover the daemon vocabulary. This restores that one observable reconnect behavior using the merged request contract. No decision record is needed. No overlapping feature branches touch the planned production files.

## Design
Keep the existing retained cursor for non-catalog families. When registry and catalog provider are installed, compose a finite supplied snapshot from that cursor and a captured list of registered target IDs. Skip retained model catalogs in this composed cursor and resolve each target once, lazily, through `modelListReading`. Resolve canonical rows with multi-agent vocabulary available; existing recipient projection and access gates decide delivery. Unavailable vocabulary contributes no reading. Never invoke legacy reconciliation for thread peers.

Track the latest successfully completed reply-only catalog revision in each live conversation, without keeping its payload or granting session admission. `daemonLiveCurrent` checks catalog readings against the greater of this revision and the retained catalog revision. Failed reservations do not supersede accepted readings. Generation changes reset this counter; removal discards it. Other reading families keep their existing checks. The wire producer tag remains the resolver's original omitted/null/string evidence.

## Concurrency model
No new goroutine. Relay Run owns each finite cursor and per-recipient delivery. Catalog resolution uses the existing `offerMu` boundary, then owner mutex; no source lock crosses delivery. The accepted reply-only revision updates under the owner mutex after generation/reservation validation. Existing cancellation and publisher shutdown remain responsible for their workers.

## State transitions and identity reuse
| Event | Race-enabled proof |
| --- | --- |
| Fresh connection and reconnect before any turn, basic/multi projection | `TestDaemonLiveRelayCatalogReconnect` |
| Persisted unresolved vocabulary replaced by parser/hold source within one generation | `TestDaemonLiveRelayCatalogReconnect` |
| Held catalog overtaken by a newer catalog, a reused-session transition, or removal/recreation | `TestDaemonLiveRelayCatalogSnapshotOvertaken` |
| Re-resolution after a target clear versus delivery of captured old evidence | `TestDaemonLiveRelayCatalogSnapshotOvertaken` |
| Colliding target/session identities and borrowed inventory isolation | `TestDaemonLiveRelayCatalogReconnect`, existing `TestDaemonLiveRelayRequestedCatalog` |
| A refused reservation supplies no accepted replacement | `TestDaemonLiveRelayCatalogReservedSnapshot` |

## Error handling
Provider refusal skips that target and completes the finite handshake snapshot. Existing validation rejects malformed or oversized supplied envelopes. Stale snapshots are silently withheld under the existing late delivery check; no fallback bypasses supplied-state gates.

## Testing strategy
Write the reconnect wire regression first and run it against unchanged production code. Reuse real catalog resolver, authenticated Noise harness and parser/hold ingress. Assert decoded full payloads and raw producer presence; clear before fresh; no request/ring/history identifiers; every accessible target including no-turn targets; borrowed retention isolation. Exercise held stale snapshots with controlled overtaking, capability/access gates, no vocabulary, and retained non-thread reconciliation. Preserve existing request and other-family tests. Run affected tests under race on Linux, `go vet ./...`, and build the binary. Dispatcher owns complete gates; the other OS is outside verification scope.

## Open questions
None. Estimated total written work 650 lines, zero exported types, one installation consumer update, three acceptance criteria and fewer than ten rejection branches. Scope is one behavior plus its necessary regression/security proof.

## Documentation handoff
Pending documentation stage: update `docs/protocol-mobile.md`, “Session-scoped live state (v2, supplied delivery contract)”, “model_list” / “Reconcile on (re)connect”, and “Reconnect / Backfill semantics”: publish thread catalog fallback coverage, source fidelity, clear/replacement and no-vocabulary behavior; distinguish supplied thread reconciliation from legacy reconcile and replay.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: `forwardLiveState` retains authenticated thread/interactive, known-target and agent-access gates. Catalog producer tags convey evidence only; only the catalog family accepts reply-only freshness. Tests must exercise access withholding with an accessible positive control.
- Tokens/secrets: no new credentials, credential access or payload logging; existing Noise pairing authenticates the integration fixture and production path.
- File operations: runtime change performs memory resolution only; saved vocabulary is loaded through the existing store, with no new paths or writes.
- Subprocesses: no new subprocess or assistant turn; tests feed the existing parser/hold directly.
- Cryptography: existing single-writer Noise encryption and nonce discipline are unchanged.
- Network/I/O: reuse `validateLiveState` envelope bounds and one-reading-per-drain delivery; snapshot enumeration is finite and never follows concurrent additions.
- Errors/logs/telemetry: refusals and stale captures emit no payload or source logging, and no new error surface is exposed.
- Concurrency: validate generation and reserved revision under the owner mutex before updating reply-only ordering. Clear and parked fresh delivery each pass existing freshness checks. Removal and reused session IDs cannot resurrect an old generation.
- Threat model: this change adds catalog delivery only to already authenticated recipients. Existing Noise protections against relay misrouting and ciphertext replay remain intact. No new input execution, credential or authorization surface is introduced.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-19

## Revisions
- 2026-10-19: Preserve the existing wire harness's `open` behavior while extracting `openCapabilities` for the non-interactive gate assertion. Add explicit null-producer and failed-reservation coverage. The revision-only borrowed bookkeeping needs local edits in three production files (`daemon_live_relay.go`, `daemon_live_state.go`, `daemon_control_live.go`); the forecast remains under 650 written lines with no new exported types or interfaces.
- 2026-10-19: Reconnect fixtures must retain the phone's static key across handshakes: pairing correctly rejects a replacement key under the same token. Store test-only keys in `switchWire` so reconnect exercises the authenticated snapshot path, with fresh connection ciphers and the original paired identity.
- 2026-10-19, verifier finding 1: Another peer's successful borrowed resolution can overtake a reconnect catalog parked behind its clear without publishing a replacement. Preserve target-wide accepted ordering and borrowed-retention isolation. In `internal/relay/v2session_livestate.go`, `drainLiveOnce` may re-resolve an overtaken unsolicited catalog once through the existing `ModelListReadingFor` provider before forwarding. Require an open thread/interactive recipient with current target access; accept only a catalog for the same conversation and captured target generation. Clear correlation on the fresh result; `forwardLiveState` still validates, projects and checks freshness. Refusal or a different generation leaves the captured reading rejected. This changes neither other reading families nor the finite target enumeration and adds no retry loop or goroutine.
- Repair proof: `TestDaemonLiveRelayCatalogConcurrentRequest` uses two authenticated peers, the real resolver and a transport hold immediately after the reconnecting phone's target clear. Process the other peer's request before resuming delivery, for unchanged and replaced bootstrap vocabulary and both recipient projections. Assert stale-capture rejection, one clear followed by fresh uncorrelated inventory, the correlated response on the other peer, and borrowed-retention isolation. Extend `TestDaemonLiveRelayCatalogSnapshotOvertaken` to distinguish fresh same-generation resolution from the captured stale reading; generation transitions and removal/recreation must still withhold it. Run affected daemon/relay race tests on Linux, vet and build; complete gates remain dispatcher-owned.
- Repair sizing/security review: This remains the same reconnect recovery invariant with its regression, estimated at 760 total written lines, no new exports or consumer signature changes, and the original three acceptance criteria. The relay refresh adds only a bounded memory resolution after capability/access checks; same-generation/target checks and ordinary validation/freshness checks prevent cross-target delivery and identity reuse. No new credentials, I/O, logging, cryptography, subprocesses or lock nesting. Security verdict remains PASS. `origin/feature/3628` also touches `drainLiveOnce` for hosted admission retirement, in a separate block; these local edits can merge independently.
