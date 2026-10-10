# Thread delivery negotiation and conversation readings

## Files read
- `internal/relay/v2session_handshake.go` → `negotiateCapabilities`, `handleNoiseInit`: authenticated capability admission.
- `internal/relay/v2session_seams.go` → `V2SessionConfig`: additive provider seams; production leaves readiness absent.
- `internal/relay/v2session.go` → `V2Session`: Run-owned capability state.
- `internal/relay/v2session_conns.go` → `handleActiveConns`: authenticated snapshots.
- `internal/relay/v2session_push.go` → `forwardEnvelope`: shared live/replay/reconciliation sealing boundary.
- `internal/relay/v2session_appframe.go` → `forwardAppReply`: handler-reply sealing boundary.
- `internal/relay/v2session_agentgate.go` → `withheldFromConn`, `agentTaggedForConn`: conversation authorization and copy-on-send projection.
- `internal/relay/v2session_replay.go` → `drainReplayOnce`: nil suppression advances replay and releases live buffering.
- `internal/relay/handlers/list_conversations.go` → `ListConversationsWithAgents`: existing list and agent restrictions remain intact.
- `internal/protocol/thread_encoding.go` → `EncodeThreadUpdate`: supplied session identity cannot grow; scalar stamps have reserved room.
- `internal/relay/v2session_test.go`, `v2session_agentgate_test.go`, `v2session_multiagent_test.go`: authenticated encrypted-wire and rekey helpers.
- `docs/knowledge/features/relay-package.md`, `relay-package-handlers.md`, `v2-session-manager.md`: owning package contracts.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: assert decrypted bytes and field absence directly.
- `docs/knowledge/features/protocol-package.md`, `docs/knowledge/decisions/042-daemon-built-thread.md` § Compatibility and Sessions, agents, messages, read marks: thread replaces content while preserving live state.

## Context
Provide the per-connection relay contract for supplied thread updates and authoritative readings. Retained state, live-state reconciliation and production publication/activation belong to #3076/#3082/#3077. No new decision record is needed. Remote feature-branch inspection found no overlap with the touched forwarding files.

## Design
Add optional readiness and authoritative reading functions to `V2SessionConfig`. Readiness attests installation of live publication, watermark lookup and session-state reconciliation; nil/false never grants thread. Negotiate it additively, collapse advertisements, and record `s.thread` only on successful authenticated admission. Expose `ActiveConn.Thread`; rekey retains the admission decision. Thread never implies interactive or multi_agent.

At both sealing boundaries, reject thread update kinds unless the connection is open, thread-capable, interactive and authorized for the payload's conversation under the existing Codex gate. Correlation cannot bypass this item gate. Preserve supplied logical payloads and continuation order without growing SessionID. Ordinary correlated replies keep their existing exemptions.

For thread connections suppress only unsolicited replaced content: assistant deltas, tools/results/denials, turn ends, session transitions, user-message pushes, task lifecycle/rosters, queue state, banners/runtime notices, compaction boundaries, refusals and unrecognized output. Keep transient tool/task progress and other live readings. The same forwarding gate covers connect reconciliation and replay; suppression returns nil so replay advances normally.

Project conversation_updated and every conversations row using fresh JSON bytes, preserving all other fields and their presence. Thread readings come only from the authoritative provider: usable zero is numeric zero, unavailable removes even supplied stale readings. Legacy projection strips last_shown_version and envelope session_id/session_state_cleared, and omits explicitly cleared new-session frames altogether. Avoid reserializing legacy replies when no projection is needed. No handler signature or read-mark storage changes.

Sizing: one negotiated delivery contract, approximately 730 total written lines (250 production, 400 tests, 80 plan), zero exported types, zero mandatory consumer migrations, five acceptance criteria and fewer than ten new reject branches. Rechecked against this written plan before commit.

## Concurrency model
All connection decisions and projection run on the existing manager Run goroutine. Providers are construction-time callbacks and must supply concurrency-safe, promptly available readings. No new goroutines, channels, locks or mutable shared payloads.

## State transitions and identity reuse
| Event | Coverage under go test -race |
| --- | --- |
| Admission, duplicate/spoofed capabilities, failed authentication | `TestV2Session_ThreadNegotiation` |
| Rekey on the same connection | `TestV2Session_ThreadNegotiation` |
| Reconnect replay suppression followed by permitted and buffered live frames | `TestV2Session_ThreadReplay` |
| Repeated updates/continuations under the same item identity | `TestV2Session_ThreadDelivery` |

## Error handling
Unavailable readings omit metadata, never infer a watermark. Malformed supplied JSON retains existing error handling; item authorization fails closed if no conversation can be identified. Suppressed frames spend no nonce and do not abort replay. Projection errors never expose plaintext logs.

## Testing strategy
Write hermetic encrypted-wire tests first. Pin legacy literal bytes and thread field presence for push and handler replies, differing conversation readings, numeric zero and unavailable/stale metadata. Cover negotiation/readiness/rekey and denied correlated items; preserve progress/request replies. Exercise reconnect drain and worst-escaping codec continuations within MaxThreadEnvelopeBytes. Run focused race tests, then go test -race ./internal/relay/..., go vet ./..., go build -o /tmp/builder-3081/pyry ./cmd/pyry. Full-module suite belongs to verifier.

## Open questions
None. Provider installation and session-tagged reconciliation are explicitly later-stage product tickets.

## Documentation handoff
Pending documentation stage: in `docs/protocol-mobile.md`, “Capability negotiation (v2)”, “conversations”, the `conversation_updated` description and “Daemon thread updates”, document readiness/per-connection gates, replaced-content filtering, legacy omissions and authoritative numeric-zero/unavailable summary rules. Describe unread as `last_shown_version > read_up_to`, preserving existing field meanings/presence. State production providers/activation remain pending #3077 and session-state reconciliation is a separate prerequisite (#3082/#3076); do not claim shipped daemon emission.

## Security review
**Verdict:** PASS
**Findings:**
- Trust boundaries: the authenticated open-state boundary precedes both forwarding gates; item authorization applies even to correlated updates and continuations. Missing conversation identity fails closed for item traffic.
- Tokens: no new credentials, retention or credential logging; readiness is daemon-supplied, not a client assertion.
- File operations and subprocesses: none added; no supplied payload reaches a filesystem or execution sink.
- Cryptography: filter before existing Noise encryption; withheld frames consume no nonce, verified by decrypting the next permitted frame. No new primitives or keys.
- Network/I/O: reuse existing inbound limits; preserve codec payloads and SessionID so reserved scalar stamps keep outgoing updates within MaxThreadEnvelopeBytes.
- Errors/logs: no payload logging; unavailable readings omit metadata rather than disclose incomplete state.
- Concurrency: Run owns connection flags; projection allocates copies and cannot mutate shared replay/push payloads. Existing shutdown paths remain sufficient.
- Threat model: preserve Noise confidentiality/authentication against relay MITM and misrouting; thread grants no execution or agent access. Prompt injection and relay metadata privacy remain the existing protocol's residual risks. Production activation is OUT OF SCOPE (#3077).
**Reviewer:** builder (self-review)
**Date:** 2026-10-10

## Revisions
2026-10-10: Preserve conversation fields by editing individual JSON fields rather than round-tripping an incomplete summary DTO in `agentTaggedForConn`. This keeps supplied binding/watermark fields and avoids adding absent fields. The authority and capability contracts are unchanged. `TestV2Session_ThreadContinuations` covers repeated updates/continuations for all three kinds; `TestV2Session_ThreadAuthenticationFailure` covers rejected admission. A mutation run removing forwarding projection/filtering failed the legacy byte, authoritative-zero and delivery assertions as intended.
