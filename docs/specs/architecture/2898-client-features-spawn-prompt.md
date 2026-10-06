# #2898 — include client feature self-reports in the spawn prompt

## Files read

- `internal/protocol/handshake.go` → `HelloClientPayload`: optional `ClientFeatures` already shipped by #2897.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit`: token-OK retention tail; never retain the hello/token.
- `internal/relay/v2session.go` → `V2Session`, `retainedClientField`, `ActiveConn`, `handleActiveConns`: authenticated snapshots and independent resource bounds.
- `cmd/pyry/main.go` → `runSupervisor`'s `setClientIdentity` closure: production relay-to-sessions mapping.
- `internal/sessions/systemprompt.go` → `admissibleClientField`, `admitClient`, `admittedClients`, `clientSection`, `writeComposedPrompt`: single prompt admission boundary and snapshot composition.
- `internal/sessions/session.go` → `Session.promptClients`: immutable admitted snapshot carried across rotation.
- `internal/relay/v2session_client_identity_test.go` → `openWithIdentity`: real authenticated IK handshake fixtures.
- `internal/relay/v2session_test.go` → `TestV2Session_RekeyResponder_HappyPath_RoundTripUnderNewKeys`: existing successful re-key sequence.
- `internal/sessions/systemprompt_client_test.go` → `TestClientSectionText_Pinned`: independently pinned lead and legacy absent-description behavior.
- `internal/sessions/pool_rotate_system_prompt_test.go` → rotation retention and never-resolve assertions.
- `internal/e2e/conversation_post_test.go` → `TestConversationPost_E2E_UserTurns`: child argv recorder.
- `internal/e2e/handshake_interactive_helpers_test.go`, `internal/e2e/relay_v2_stream_new_session_dormant_test.go`, `internal/e2e/harness.go`: authenticated fake phone, `noiseWire`, real-daemon fixtures.
- `docs/knowledge/features/sessions-package.md`, `docs/knowledge/features/sessions-package-key-types-writesystemprompt-systemprompttext.md` § Composition and resolution: retention and display bounds belong at separate boundaries; never resolve clients on relay Run during rotation.
- `docs/knowledge/features/relay-package.md`, `docs/knowledge/features/e2e-harness.md`, `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`: logging, hermetic fixture and test conventions.
- `docs/protocol-mobile.md` § Security model: paired-client prompt injection remains a partial mitigation; attribution does not authenticate the truth of a report.

## Context

An attached client can report its features, but the daemon currently drops that field before prompt composition. Include the report with its client's identity so Claude sees attributed text, never a daemon capability guarantee. Existing activation staleness is intentional: reconnect does not refresh an active prompt; rotation carries its admitted snapshot; eviction/reactivation resolves current clients. No decision record is needed.

One deliverable, estimated 650 written lines including plan, production comments and tests; zero new exported types/interfaces, one production mapping consumer, five acceptance criteria, no new state-machine reject branch. Rechecked against this plan: all builder limits hold. No overlapping feature branches were found after fetching origin.

## Design

- Add `clientFeatures` to `V2Session`, retained verbatim only after token authentication through `retainedClientField` with an inclusive 1024-byte bound. Over-bound reports become empty without failing authentication. Re-key leaves the field untouched. Copy only the string, never the hello or token; no device-registry field or log attribute is added.
- Add `ClientFeatures` to `ActiveConn` and `Features` to `ClientIdentity`; extend the installed mapping closure to copy it without validation.
- `admitClient` admits features independently with `admissibleClientField` and an inclusive 512-UTF-8-byte cap. Existing character rules apply: nonblank, valid UTF-8, no C0/C1/DEL or double quote. A refused description becomes empty; name/version behavior stays unchanged.
- `admittedClients` sorts by name, version, features, then compacts identical triples. Distinct reports remain distinct; the existing whole-section cap counts admitted triples.
- `clientSection` appends ` (self-reported features "<description>")` after optional version. The lead and `systemPromptText` constants stay byte-identical. Update affected comments to describe all three fields and bounded admitted retention.
- Reuse lifecycle logic without new resolver calls. Extend existing pool tests to inspect admitted features carried through `Session.promptClients` and rendered after rotation.

## Concurrency model

No new goroutines or scheduling. Relay retention and enumeration remain on Run; fields are immutable after authentication, including re-key. Resolver calls remain off relay Run and off pool locks. `promptClients` remains an immutable slice replaced/read under `Pool.mu`. Existing harness cleanup stops daemon, phone and relay.

## Error handling

Admission/retention refusal is silent and field-only; invalid names still drop the entire identity. Reports cannot fail a handshake or spawn. Existing prompt write failure preserves the prior atomic file and logs metadata without report bytes.

## Testing strategy

Write tests before production changes and observe their failure. Table-driven handshake retention covers empty, verbatim hostile text, 1024/1025-byte and multibyte boundaries, rejected authentication and successful re-key. Admission tests cover 512/513-byte boundaries, multibyte text, invalid UTF-8, every control range and independent refusal. Independent rendering pins cover optional version, stable triple order/dedup, differing reports, section cap and absence of client-authored lines.

Extend pool rotation tests for rendered/admitted features and a resolver that must be called only once. A local e2e fixture handshakes through fakephone/fakerelay into a real daemon, records the spawned child's argv and reads its named prompt file. Reconnect with a different report, verify unchanged active bytes, then evict/reactivate and verify fresh bytes. Assert daemon output and `devices.json` omit both reports. No new shared harness or live Claude run.

Checks: race tests for `internal/relay`, `internal/sessions`, `cmd/pyry`; targeted race e2e proof with `e2e` tag; `go vet ./...`; `go build ./cmd/pyry` with output outside the worktree. The verifier owns the full-module gate.

## Open questions

None. All report contracts and lifecycle semantics are specified by the issue and existing seams.

## Documentation handoff

- Pending documentation stage: `docs/protocol-mobile.md`, under `hello` (v2-specific note): replace #2897's pending-consumer note with shipped authenticated retention (1024-byte cap) and prompt behavior (512-byte cap, single-line UTF-8, no C0/C1/DEL or double quotes, field-only omission on refusal). Describe self-report attribution and activation-snapshot staleness, including rotation carrying the snapshot and eviction/reactivation refreshing it. State that no protocol-version bump or capability negotiation is required.
- Pending documentation stage: `docs/knowledge/features/sessions-package-key-types-writesystemprompt-systemprompttext.md`, under Composition and resolution: describe the third field, independent admission, triple ordering/deduplication and the existing activation/rotation snapshot behavior.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `admitClient` remains the one prompt boundary; single-line quoted attribution does not guarantee report truth or prevent semantic prompt injection from a paired operator.
- Tokens: only the bounded description string is retained after successful authentication, never a hello/token; existing token and static-key validation remain unchanged.
- File operations: no report is used in a path. Existing prompt files remain atomic writes at 0600; no new persistence is introduced.
- Subprocesses: report bytes go only into the appended prompt file, never shell text, executable name or argv values. Existing child shutdown and environment handling remain unchanged.
- Cryptography: no key, nonce or comparison behavior changes; re-key preserves authenticated metadata.
- Network and I/O: existing handshake envelope cap applies; the new 1024-byte retention and 512-byte admission bounds limit amplification. Existing transport deadlines and limits are unchanged.
- Errors/logs: SHOULD FIX during build: assert both distinct report sentinels are absent from daemon logs and persisted device registry in the production e2e proof; never format `ActiveConn` wholesale.
- Concurrency: no new goroutines; keep immutable relay fields and pool snapshot locking. Rotation must never invoke the resolver from relay dispatch.
- Threat model: attribution and structural admission address report framing under threat 1, without claiming semantic containment. Existing Noise/authentication mitigations for threats 2–6 are unchanged. Threat 7 connection/rate limiting remains deferred in the protocol's security model; this ticket bounds only new retained/prompt bytes.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-06
