# Bound continuation sizing work during daemon publication

## Files read
- `cmd/pyry/daemon_thread_relay_test.go` → `TestDaemonThreadPublication`: authenticated peers, complete revision reconstruction, continuation and access assertions.
- `cmd/pyry/daemon_live_relay_test.go` → `testDaemonRelayWire`: real manager, paired Noise peers and joined publisher cleanup.
- `cmd/pyry/agent_switch_frames_test.go` → `switchWire.open`, `read`, `await`: shared three-second frame deadline.
- `cmd/pyry/daemon_thread_relay.go` → `daemonThreadPublisher.publish`: encodes a whole logical update before enqueueing its first continuation.
- `cmd/pyry/daemon_live_relay.go` → `startDaemonLivePublisher`: independent current-reading publication and shutdown.
- `cmd/pyry/thread_shadow.go` → `follow`: existing 50 ms discovery/follow scheduling and joined workers.
- `internal/protocol/thread_encoding.go` → `threadEncodePayload`, `threadFragmentLength`: repeated escaping during binary-search sizing.
- `internal/protocol/thread_encoding_test.go` → `TestThreadUpdateRoundTrip`, `TestThreadUpdateBudgets`: round-trip assembly, UTF-8, escaping and maximum stamp budgets.
- `internal/relay/v2session_livestate.go` → `forwardLiveState`: current-reading and recipient gates.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit`: authenticated admission before publication.
- `docs/knowledge/features/thread-package.md` § Shadow lifecycle and evidence: 250 ms is scheduled waiting, excluding encoding/network time.
- `docs/knowledge/features/protocol-package-thread-messages.md`: measure fully escaped envelopes, preserve UTF-8 boundaries and unknown JSON.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: controlled pre-fix proof and producer/barrier ordering.
- `CODING-STYLE.md`: pure logic tables, race checks, symbol citations and local edits.

## Context
At baseline `126d7ca662a38fdd399cacd2ea09bcb12304c892`, 20 isolated race publication runs pass. Scratch phase diagnostics reproduce the missing-frame failure in eight concurrent copies of the existing fixture on one CPU: both peers receive version 11's preceding field change, then await the first escaping-heavy addition. The whole addition is encoded before enqueueing; binary-search fragment sizing serializes prefixes repeatedly. A five-run race CPU profile attributes substantial encoding work to `threadFragmentLength`. This controlled reproduction identifies a continuation encoding stall; the original historical log is unavailable, so it does not prove the historical invocation's exact phase.

One deliverable: remove repeated sizing work so the existing authenticated delivery regression completes under the controlled load without weakening its waits. No decision record is needed. Pool admission was repaired by #3304 and is verification-only here. Feature #3236 overlaps `installDaemonThreadRelay`, which this design does not edit.

## Design
Keep `threadFragmentLength(data []byte, room int) int` and the current encoding contract. Scan valid UTF-8 once up to the available escaped-byte budget, counting encoding/json's default string costs: quotes/backslashes and short control escapes cost two, other controls and HTML-sensitive ASCII cost six, U+2028/U+2029 cost six, and other runes cost their UTF-8 width. Stop before a rune would exceed room. Do not allocate or marshal candidate prefixes. No output DTO, metadata, hash, fragment capacity or stamp reservation changes.

Add independent oracle tests comparing returned boundaries with actual `json.Marshal` lengths across all escaping classes and tight rune budgets. Pin allocation-free sizing on the publication's large escaping pattern so the repeated serialization regression fails deterministically on the pre-fix implementation. Retain the authenticated test's full payload and all assertions; add phase diagnostics for handshake, each version and live readings.

## Concurrency model
Sizing is pure caller-local computation; no new goroutines, locks or lifecycle owners. Existing publisher, manager and shadow cancellation/join paths remain intact.

## State transitions and identity reuse
None: sizing introduces no lifecycle or identity state. The unchanged publication regression still covers existing revisions, transitions and continuation assembly for separate peers.

## Error handling
Existing invalid UTF-8/input and insufficient metadata-room errors remain in `threadEncodePayload`. A rune that cannot fit returns a zero fragment length and follows the existing metadata-room rejection. No new errors or logging of content.

## Testing strategy
First run allocation/escape regressions against the original sizing code and observe the allocation regression fail. Then run protocol race tests and the unchanged authenticated publication test under the same eight-fixture one-CPU comparison. Run the issue's exact count=20 race JSON commands for publication/apps ownership and shutdown admission; record per-test outcomes and commits. Run scoped cmd/protocol race checks, `go vet ./...`, `go build ./cmd/pyry`, then the explicitly required complete `make check`. Report reached tiers and independently tracked failures; no live Claude run.

## Open questions
- Does eliminating repeated candidate marshaling suffice for the controlled authenticated failure? Resolve with the same before/after load, and revise if evidence requires another local repair.

## Sizing
One deliverable; approximately 220 total written lines including plan, tests and production; zero new exported types/interfaces, zero consumer migrations, three acceptance criteria and no new reject branches. Below all five builder limits.

## Security review
**Verdict:** PASS

**Findings:**
- [Trust boundaries] No findings: `handleNoiseInit`, `liveWithheld` and existing thread gates retain authentication, membership and agent eligibility. Sizing consumes validated UTF-8 inside `threadEncodePayload` and does not trust content as instructions.
- [Tokens/secrets] No findings: fixture keys still use `crypto/rand`; no credential generation/storage changes and phase diagnostics contain only synthetic routing/version identifiers.
- [File operations] No findings: production sizing performs no I/O; tests use existing temporary history stores.
- [Subprocesses] No findings: no new subprocess path; the scratch controlled comparison runs only the existing test binary.
- [Cryptography] No findings: Noise sealing and continuation SHA-256 identity remain unchanged.
- [Network/I/O] No findings: `MaxThreadEnvelopeBytes` and maximum stamp reservations remain enforced; oracle and assembly tests prove escaping cannot undercount frame size.
- [Errors/logs] No findings: retain content-free encoding errors; do not log payloads, tokens, keys or host paths.
- [Concurrency] No findings: pure sizing owns no shared mutable state; existing cancellation and joined teardown remain intact.
- [Threat model] No findings: protocol § Security model's relay confidentiality and untrusted content boundaries remain enforced by Noise and inert JSON. This repair reduces computation before delivery without widening recipient access.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-12
