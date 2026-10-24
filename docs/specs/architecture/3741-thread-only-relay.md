# Thread-only relay delivery

## Files read

- `internal/relay/v2session_thread.go` → `threadWithheld`, `threadSummary`, `threadProjected`, `threadReply`: content gating and attribution projection.
- `internal/relay/v2session_livestate.go` → `liveWithheld`, `forwardLiveState`, `refreshLiveCatalog`: supplied identity, clears and revision watermarks.
- `internal/relay/v2session_catchup.go` → `admitThreadQuery`, `threadQueryGate`: complete queries, optional expansion and projection joins.
- `internal/relay/v2session_hosted.go` → `hostedDeliver`: final eligibility, authorization and cancellation before sealing.
- `internal/relay/v2session_conns.go` → `handleConnSnapshot`: publication recipient selection.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit`, `negotiateCapabilities`: mandatory admission order and supplied recovery.
- `internal/relay/v2session_thread_test.go`, `v2session_thread_delivery_test.go`, `v2session_catchup_test.go`: authenticated delivery and query fixtures.
- `internal/relay/v2session_livestate_test.go`, `v2session_livestate_hosted_test.go`: attribution, prompt eligibility, repeated generations and supplied bypass proofs.
- `internal/relay/v2session_compact_test.go`, `v2session_page_test.go`, `v2session_tool_body_test.go`, `v2session_children_test.go`: optional capabilities, join isolation and late rejection proofs to reuse.
- `internal/e2e/relay_v2_client_features_prompt_test.go` → `TestRelayV2_ClientFeaturesSpawnSnapshot`: cold first post, named catch-up, prompt ownership, reconnect and eviction.
- `docs/knowledge/features/thread-package.md`, `e2e-harness.md`, `development-verification-test-execution-and-artifact-survival.md`: demand loading, genuine wire evidence and preservation of failures.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md` and its `thread-delivery-and-summary-projection` topic: supplied replies require interactive even when correlated; ordered delivery needs `OutboundContext`, not only `Outbound`.
- `CODING-STYLE.md`, `docs/protocol-mobile.md` § Security model: lifecycle, source identity and trust boundary requirements.

## Context

Merged #3740 requires negotiated thread before a session opens. Compatibility delivery and recovery branches remain, while complete-thread content incorrectly inherits interactive supplied-state eligibility. This ticket removes that compatibility and repairs thread-only content together under its admitted split-depth exception. ADR 042 needs the documentation-stage update listed below.

Source inspected: `556f9e8277b75e2a1d86b4bc24edf28129201f43`. The two admitted results remain compatibility removal and complete-thread eligibility repair; no new behaviour is added. Estimate: about 600 written lines including this record, tests and deletions, six production files rather than the refiner's four because publication enumeration and handshake recovery also carry the contract. Zero exported types, no public API migration, four acceptance criteria, no new reject branches. Both sketch and final plan fit the five ceilings. Branch overlap check found no overlapping remote feature changes in the selected production files.

## Design

- Add an internal conversation eligibility predicate for complete-thread content: negotiated thread, nonempty known conversation, and the existing Codex/multi-agent restriction. Reuse it for content filtering, query admission and `ActiveThreadConns` enumeration.
- Keep `liveWithheld` as the interactive supplied-state gate composed with that predicate. Supplied prompts, clears, catalog refresh and state watermarks retain all existing eligibility and freshness checks.
- Complete catch-up/page admission and final hosted delivery no longer require interactive. Expansion still explicitly requires interactive plus expansion; compact negotiation retains its current prerequisites and complete/compact joins retain their current rules. Authentication, hosted-resource authorization, late eligibility and cancellation do not change.
- Remove non-thread content forwarding, tag stripping, summary fallback and supplied forwarding/reconnect fallback. Summaries always consult authoritative last-shown readings, preserving present zero, unavailable omission and unrelated raw fields. All admitted content retains session attribution and clear flags; replaced unsolicited raw content remains suppressed.
- Preserve #3740's exact refusal order: authentication/static-key privacy, existing version checks, omitted-thread 4412 or ungrantable-thread 4421, before binding, redemption, workspace disclosure or opening.

## Concurrency model

No new goroutines, locks or queues. Run retains session/crypto ownership; existing workers own cancelable provider work. Hosted delivery checks remain immediately before sealing. Existing teardown, queue bounds, query cancellation and rekey paths remain authoritative.

## State transitions and identity reuse

| Event | Race-detector proof |
| --- | --- |
| Cold first completed post, initial named catch-up and final marker | `TestRelayV2_ClientFeaturesSpawnSnapshot/thread-only` and `/thread-interactive` |
| Reconnect, eviction and reactivation with reused conversation/session | Both `TestRelayV2_ClientFeaturesSpawnSnapshot` cases retain prompt assertions |
| Complete catch-up, older page, join and later update without optional grants | `TestThreadCompleteCapabilities` |
| Back-to-back generations, stale revisions and repeated clears | Existing `TestLiveStateOrdering`, `TestLiveStateWire`, `TestThreadDeliverySuppliedLiveState` |
| Prompt eligibility and supplied correlated replies | Existing `TestLiveStateCapturedEligibility`, `TestLiveStateReplies`, `TestLiveStateGates` |
| Query/live race, cancellation, retirement and late authorization | Existing `TestThreadCatchUpLiveRace`, `TestThreadPageLiveRace`, `TestThreadDeliveryLateEligibility`, `TestThreadDeliveryCancellation` |
| Complete/compact projection replacement and expansion scope | Existing `TestThreadCompactJoins`, `TestThreadCompactLiveQueryRace`, tool-body/children admission tests |
| Rekey preserves negotiated flags | Existing `TestV2Session_ThreadNegotiation` |

## Error handling

Queries retain content-free `thread.query_failed` on invalid input, missing providers, unavailable state or authorization loss. Missing thread fails closed even in synthetic sessions. Existing delivery errors, bounded queue retirement and cancellation are unchanged. No legacy fallback is selected after refusal or missing optional grants.

## Testing strategy

Write the thread-only cold fixture and focused authenticated regressions first. Run the named cold case through `scripts/test-admission.py` before other tests and retain its expected failure. Then implement and run the same cold case, both cold-fixture capability cases, and focused relay race families for thread delivery/summaries/continuations, live state, catch-up/pages, expansion/compact, admission, controls, attachments and rekey. Record JSON run/pass/fail/skip counts and exact candidate revision in the PR. Compile the affected e2e and real-Claude tagged packages; run `go vet ./...` and build `cmd/pyry` with output outside the worktree. Complete configured offline and affected real-Claude permission/question/reconnect acceptance belong to the dispatcher and remain pending. Linux is this ticket's operating system; macOS is outside verification scope.

## Open questions

None. #3844 publication continuity is independent. Earlier failed captures and approval/operator holds remain unchanged; retrieved earlier evidence is retained under `/tmp/builder-3741/earlier-evidence`.

## Documentation handoff

Pending for the documentation stage, including the transferred #3720 final compatibility handoff and merged #3740/#3719 handoffs:

- `docs/knowledge/decisions/042-daemon-built-thread.md`, Status, Compatibility, decision 4 (Old event stream), Consequences and Migration order: record the end of daemon compatibility while retaining historical rationale.
- `docs/protocol-mobile.md`, Handshake, Capability negotiation (v2), Compatibility, Appendix: example flow — first pairing + first message (v2), Daemon thread updates (v2, supplied delivery contract), Session-scoped live state (v2, supplied delivery contract) and Message envelope: every admitted app session negotiates thread; content retains attribution; complete-thread queries/updates do not require interactive; supplied state, controls and optional expansion/compact keep their gates. Remove claims that current delivery strips tags for non-thread peers.
- `docs/knowledge/features/thread-package.md` and `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md`, with its linked thread-delivery-and-summary-projection topic: reconcile mandatory admission, independent optional capabilities and the removed compatibility branches.
- Preserve authentication/static-key privacy rejection, then version checks, then omitted-thread (`client.update_required`/4412) or advertised-but-ungrantable thread (`protocol.unsupported`/4421), before binding, redemption, workspace disclosure and opening. Preserve merged children's valid evidence and all pending full/live acceptance.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: complete content uses the known-conversation and Codex gate at admission, enumeration and final send. Supplied state independently requires interactive. SHOULD FIX: test both genuine Noise-negotiated capability sets and negative optional-capability requests so this separation cannot authorize controls or expansion.
- Tokens/credentials: authentication, static binding, revocation and `hostedAuthorized` remain in place. No new credentials, storage or log fields.
- File operations: production introduces no paths or writes. E2E wrappers and argv/prompt reads stay within each fixture's private temporary home.
- Subprocesses: no new production process or shell input. Existing fake-daemon cleanup owns spawned test children.
- Cryptography: no changes to Noise, nonces or rekey; suppression still occurs before sealing on Run.
- Network/I/O: retain envelope caps, bounded ordered lane, provider cancellation and transport deadlines. No new socket or HTTP boundary.
- Errors/logs: keep generic query errors and existing structured metadata; report text, tokens and payloads are not logged.
- Concurrency: no new locks or lifetimes; source generations/revisions and generated clears retain current ordering. Race tests cover reconnect, reused identity and late eligibility.
- Threat model: protocol Security model's prompt injection, relay MITM/routing, token misuse and replay boundaries remain enforced by existing validation and Noise. This work grants complete content only to already authenticated eligible thread clients. Existing deferred admission deployment, rate limits and hardware key storage remain with their existing owners; no deployment or migration is performed.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-24

## Revisions

- 2026-10-24: Affected-package validation at `4d696d5135ab621ee88daf2dd9bfaa95408122cc` exposed catch-up/page fixtures still treating omitted interactive as ineligible; migrate those negative cases to unavailable membership, with positive thread-only coverage in `TestThreadCompleteCapabilities`. A synthetic tool-body regression also showed that a blanket no-thread output drop suppressed its content-free refusal. Apply thread gates to content and supplied state while retaining error delivery. Remove the now-unused session argument from `threadProjected` and its two callers (one additional production file, `v2session_push.go`). No exported API or new lifecycle; total written work remains below 800 lines. Preserve all original failed runs.
- 2026-10-24: The focused admission rerun exposed the synthetic tool-body fixture's capability mutation inside `KnownConversation`. Check the thread flag after resolving membership so admission observes that mutation before provider work, retaining the existing fail-closed regression and its content-free refusal.
