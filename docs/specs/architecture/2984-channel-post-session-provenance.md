# Channel post acceptance-time session provenance (#2984)

## Files read

- `cmd/pyry/channel.go` → `channelPoster`, `channelCreator`: resolve or create a daemon-owned conversation before acceptance.
- `cmd/pyry/channel_delivery.go` → `accept`, `persist`, `newChannelDelivery`, `deliver`: pending records, retry reconciliation and publication boundary.
- `cmd/pyry/main.go` → `runSupervisor`: same registry, pool and history store wired before delivery starts.
- `cmd/pyry/channel_delivery_test.go` → `testDelivery`, `testPostHistory`, `TestChannelDelivery_ReloadInterruptions`: reusable recovery and write-failure fixtures.
- `internal/conversations/registry.go` → `Get`, `Update`: current binding is a locked value read; the name-match row can be stale.
- `internal/sessions/pool.go` → `HarnessFor`: live/dormant agent lookup without revival or activation; unknown IDs return errors.
- `internal/sessions/registry.go` → `canonicalHarness`: a known legacy session entry is canonically Claude, unlike a missing session lookup.
- `internal/history/log.go` → `Metadata`, `SessionProvenance`, `AppendWithMetadata`: absent versus explicit none and valid agent/ID pairs.
- `docs/knowledge/features/history-package.md` § Producers (#2114, #2115): visibility is independent of provenance and legacy transport eligibility; reconciliation uses raw history.
- `docs/knowledge/features/sessions-package.md`, `sessions-package-key-types-pool.md`: dormant entries differ from live session lookup; no activation for attribution.
- `docs/knowledge/features/conversations-package.md`: session routing IDs and conversation IDs are distinct.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change / Protocol boundaries: verify failing assertions and inspect raw metadata versus payloads.
- `docs/knowledge/decisions/042-daemon-built-thread.md` § Sessions, agents, messages, read marks: daemon-owned provenance and legacy compatibility.
- `CODING-STYLE.md`: stdlib tests, contextual errors, persistence and concurrency conventions.

## Context

Channel post history lacks the acceptance-time binding. A deferred or recovered post must retain its original session attribution even after rebinding. This is one producer migration under ADR 042, with no new decision record required. No other fetched feature branch touches the planned files; #2981's common append seam is independent.

Sizing: approximately 450 written lines including plan and tests, zero new exported types/interfaces, one composition installation, four acceptance criteria and fewer than ten new reject/error branches. All limits remain within the builder ceiling.

## Design

Add a private acceptance-only provenance lookup to `channelDelivery`, installed by `runSupervisor` before publication. A helper beside `channelPoster` reads `Registry.Get` for the resolved conversation ID, rather than the name-match row. A known empty binding returns explicit `none`; a bound routing ID returns Claude/Codex provenance only on successful `HarnessFor`. Missing conversation, unavailable lookup, errors and unsupported agents return nil. No provenance failure refuses a new post.

`accept` copies the snapshot into an optional `session` field on `channelDeliveryPost` before persisting the candidate. Existing callback/factory signatures stay intact. Loading preserves nil and explicit none exactly, validates non-nil kind/ID combinations, and never calls the lookup. Both direct append sites combine the stored snapshot with their existing visibility classification. Reconciliation continues matching payloads and never rewrites already stored metadata or reannounces fully recorded posts. No payload or recipient changes.

## Concurrency model

No new goroutines. The callback is installed before delivery/handlers start and reads the registry then pool through their existing individual read locks. `accept` runs it under the existing delivery mutex, without retaining a registry lock while entering the pool. The snapshot is copied into delivery-owned memory. Recovery and delivery use only the persisted snapshot. Existing consumer cancellation and shutdown joins remain unchanged.

## Error handling

Provenance lookup failures degrade to unknown without logging identifying facts or adding refusal branches. Existing acceptance/persistence/history failures retain their contracts. Invalid persisted provenance returns the existing generic invalid-state load error; missing legacy fields remain valid. Partial writes retain pending work and publication waits for all remaining appends.

## Testing strategy

Table-driven acceptance tests cover Claude/Codex, explicit none, missing conversation/agent/provider, unsupported/empty agents, and auto-created channels; inspect raw pages from warm/reopened stores and visibility/watermarks. Use a real pool loaded with a dormant entry to prove no runner is constructed for it. Gate delivery, rebind, then drain to prove acceptance-time capture. Inject delta/completion write failures, restart or retry, and check deduplication, unchanged IDs/payloads and publication only after success. Recover literal legacy pending JSON and explicit none under a new binding without any lookup; preserve already stored untagged prefixes.

Run focused tests red first, then `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build -o /tmp/builder-2984/pyry ./cmd/pyry`. The dispatcher owns `make check`; no live Claude test is needed.

## Open questions

None.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/history-package.md`, “Producers (#2114, #2115)”, document acceptance-time capture, persistence through retry/recovery and shared provenance across chunks/completion. State that known-unbound posts use explicit `none`, whereas unavailable facts, older pending records and legacy history remain absent/unknown and are not inferred or rewritten.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] Provenance comes only from `Registry.Get` and `Pool.HarnessFor` after `channelPoster` resolves the conversation, never text, name, turn ID or client payload fields. Validate kind/ID consistency when loading pending records.
- [Tokens, secrets, credentials] No credential handling or new logging; routing IDs are private history metadata, not authorization.
- [File operations] Reuse `persist`'s private directory, 0600 temporary file, sync/close/rename and `newChannelDelivery`'s O_NOFOLLOW open. No new path inputs or history rewrites.
- [Subprocesses] `HarnessFor` reads live/dormant state; no activation, revival or execution is introduced.
- [Cryptography] No crypto changes; `newChannelPostTurnID` retains the existing crypto/rand-backed mint.
- [Network and I/O] Existing control text/name size limits and transport gates stay intact; no new wire fields or network operations.
- [Errors, logs, telemetry] Lookup errors are not exposed; invalid persisted state uses a content-free error. No session identifiers, post text or paths added to logs.
- [Concurrency] Separate registry/pool reads avoid nested registry-to-pool locks; delivery owns copied snapshots under its existing mutex. Atomic pending persistence and raw-history reconciliation retain restart recovery after partial writes.
- [Threat model] Existing local authenticated control and relay recipient boundaries remain authoritative. Metadata establishes attribution only; it grants no access and is absent from legacy payloads.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-08
