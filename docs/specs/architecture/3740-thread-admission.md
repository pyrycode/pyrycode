# Require thread before app session binding

## Files read

- `internal/relay/v2session_handshake.go` → `handleNoiseInit`, `checkClientVersion`: authentication, binding, ack construction and persistence ordering.
- `internal/relay/v2session_test.go` → `startManager`, `driveToOpen`, `driveToOpenCaps`, hello builders: shared supported fixtures and real Noise transport.
- `internal/relay/v2session_min_version_test.go` → `runHelloFrom`, `assertVersionRejected`: correlated encrypted refusal assertions.
- `internal/relay/v2session_static_key_test.go` → static-key tests: atomic binding and persistence regressions.
- `internal/relay/v2session_thread_test.go` → negotiation, summaries and delivery: obsolete positive non-thread cases must become supported cases or admission refusals.
- `internal/e2e/relay_v2_handshake_test.go` → shared handshake helpers: fake-daemon supported admission.
- `internal/e2e/realclaude/harness_daemon_test.go` → `driveHandshake`: shared live connection setup.
- `internal/e2e/liverelay/liverelay_test.go` → `driveHandshake`, `buildHello`: real-relay connection setup.
- `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md`: grants remain value-specific, rekey retains them, rejected ack grants no session authority.
- `docs/knowledge/features/e2e-harness.md`, `e2e-realclaude.md`, `development-verification.md`: consume encrypted frames in order, catch up named conversations, compilation is not live proof.
- `docs/knowledge/decisions/042-daemon-built-thread.md`: thread is the supported conversation-content route; final compatibility wording belongs to the parent.

## Context

Authenticated/version-accepted apps currently bind and open without a usable thread route. Require negotiated thread before any admission side effects. One independently useful behavior includes the fixture migrations needed to retain runnable gates. Estimated 1000–1300 written lines, zero exported APIs, four acceptance criteria and two new refusal branches. The verification floor retains fixtures with their consumer despite the 800-line ceiling; this is already a grandchild (#3714 → #3720 → #3740), recorded with needs-human:sizing. No further split is permitted. No new decision record is needed.

Overlap: #3240 adds a push-queue observer later in `handleNoiseInit`; #3296 adds catch-up coverage. Neither supplies an admission prerequisite or restructures the admission block. Keep edits local.

## Design

`negotiateCapabilities` remains the sole grant authority. After token/static-key validation and existing version checks, derive a local thread refusal: no advertisement means `client.update_required`/4412; advertised but ungranted means `protocol.unsupported`/4421. Gate both binding and workspace construction on the complete verdict. Preserve the binding race's atomic decision and generic authentication rejection.

Complete Noise response construction on refusals, then send `noise_resp` with the correlated ack before exactly one routing envelope containing the correlated sealed nonretryable error plus close. No workspace metadata, session worker, redemption or client-version persistence may precede admission. Keep LastSeenAt's ordinary validation bookkeeping. Log closed-set reasons only.

Shared positive fixtures advertise and genuinely negotiate thread with a ready backend seam; explicit negative setup preserves missing/false readiness and absent advertisements. Optional interactive, expansion, compact and multi-agent flags retain value-specific membership checks. Replace obsolete legacy-positive content expectations with thread-supported assertions. Shared fake/live/real-relay helpers perform named-conversation catch-up before expecting live content, reusing existing scenario receivers rather than translating legacy content.

## Concurrency model

No new goroutines or shared state. Admission stays on the manager Run goroutine; registry binding remains atomic under its existing lock. Existing cancellation and worker teardown paths remain intact.

## State transitions and identity reuse

| Event | Race-detector coverage |
| --- | --- |
| Authenticated hello rejected before open/persistence | `TestV2Session_ThreadAdmission` |
| Unknown/expired token or different static key with any thread advertisement/readiness | `TestV2Session_ThreadAdmissionAuthPrecedence` |
| Existing version rejection precedes thread rejection | `TestV2Session_MinVersion_Enforced`, `TestV2Session_ThreadAdmissionVersionPrecedence` |
| Concurrent installs race to bind one token | `TestV2Session_StaticKey_BindRace` (existing static-key family) |
| Repeated handshake/rekey preserves optional grants | `TestV2Session_ThreadNegotiation`, existing rekey and static-key families |
| Reconnect/catch-up reestablishes a named content route | migrated affected fake-daemon scenarios; live execution pending dispatcher |

## Error handling

Authentication errors retain indistinguishable `auth.invalid_token`/4401. Existing version payloads remain unchanged. Thread errors contain no remote-authored hello values. Reuse existing error sealing and combined error/close handling, including close-only fallback if sealing fails. No compatibility switch or fallback admission.

## Testing strategy

Write real Noise_IK refusal tests first and observe them fail before implementation. Assert ordering, exact codes/correlation, omitted workspace root, no open session/dispatch/push route, untouched pairing admission fields in memory and disk, LastSeenAt bookkeeping and private logs. Preserve supported negotiation/rekey and minimum-version/static-key assertions while migrating required positive fixtures. Run affected package/scenario checks under race on Linux and compile changed e2e, e2e_realclaude and e2e_liverelay suites. Run go vet and build. Retain commands and counted outcomes in the PR. Full configured acceptance and live-Claude execution belong to dispatcher; macOS is outside this ticket's scope.

## Open questions

None. Fixture breadth will be determined by affected-package failures; do not remove supported scenario assertions.

## Documentation handoff

Pending documentation stage:
- `docs/protocol-mobile.md` sections **Handshake**, **hello (v2-specific note)**, **Capability negotiation (v2)**, **Compatibility**, **Appendix: example flow — first pairing + first message (v2)**: require negotiated thread after authentication/version checks and before binding, redemption or workspace metadata; state missing-thread 4412 versus advertised-but-ungrantable 4421, authentication privacy precedence and optional capabilities.
- Reconcile `docs/knowledge/features/v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md` and its **Thread delivery and summary projection** topic with this admission rule.
- Final compatibility-retirement/ADR wording remains with #3720's integrated documentation handoff.

## Security review

**Verdict:** PASS

- [Trust boundaries] `handleNoiseInit` admits only a token/static-key-authenticated, version-accepted peer with a granted thread capability; unknown strings cannot satisfy membership.
- [Tokens] Preserve generic authentication refusal precedence and Validate LastSeenAt updates. No token, token hash, static key or hello text enters new logs/errors.
- [File operations] No new paths or writes. Rejected admissions reach none of `recordStaticKey`, `recordRedemption`, `recordClientVersion`; tests compare disk and memory admission fields.
- [Subprocesses] No production subprocess changes; existing harness teardown owns test children.
- [Cryptography] Reuse Noise_IK response and existing seal functions; response precedes encrypted error so the initiator can decrypt. No nonce/key changes.
- [Network and I/O] No new socket or framing boundary; existing bounded Noise frame decoding and send path remain.
- [Errors/logs] SHOULD FIX during build: pin privacy with marker-bearing hello values and verify fixed rejection reason logs; preserve version payloads and generic authentication bytes.
- [Concurrency] Atomic `BindStaticKey` remains after all earlier verdicts and before workspace construction. No new lock/goroutine; race tests cover binding and rekey.
- [Threat model] Relay remains untrusted and sees encrypted application data. OUT OF SCOPE: removing remaining legacy production branches belongs to #3741 and final integrated retirement to #3720.

**Reviewer:** builder (self-review)
**Date:** 2026-10-23

## Revisions

2026-10-23: The affected package run showed retained modal, question, model, slash,
turn-phase and suggestion fixtures still supplied only legacy live-state seams.
Their fixture adapters now supply the same records through `ThreadLiveState`,
assert generated clears and retain payload, unicast, revision and optional-gate
checks. Generic transport tests use supported workspace updates; obsolete
non-thread positives become admission refusals. Droppable legacy deltas remain
covered by pure queue tests; the supported nonblocking transport scenario now
proves FIFO delivery of control frames without drops. Shared fake/live helpers
complete named catch-up through the existing scenario query reader. Total written
work remains approximately 1200 lines under the recorded verification-floor and
grandchild exception. The existing binding-race test's exact name is
`TestV2Session_StaticKey_RaceAcceptsAtMostOne`.
