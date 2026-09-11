# Spec — Ticket #2281: reject models absent from the published menu

## Files read

- `docs/knowledge/features/sessions-package-key-types-pool-updatesettings.md` → `Pool.UpdateSettings`, `deliverSettingsInBand` — defines atomic persistence-before-delivery, empty-model restart semantics, and the existing privacy-safe delivery failure event.
- `docs/knowledge/features/streamsup-package-retaining-the-decoded-model-list-for-the-session.md` → `sessionModelHold.ModelList` — establishes the retained list as the per-session authority and documents its deep-copy and concurrency contract.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-set-session-settings-settingsupd.md` → `handleSetSessionSettings` — records the relay validation order and fixed-message error posture.
- `docs/knowledge/features/development-verification.md` → “Protocol boundaries”, “Captures and live evidence” — requires wire-level negative-path proof and non-vacuous capture-backed evidence.
- `docs/knowledge/features/e2e-harness.md` → `StartStreamInteractiveWithRelay` — supplies the real daemon/fake Claude/fake relay/fake phone path for hermetic proof.
- `docs/knowledge/features/e2e-realclaude.md` → `startStreamModalResolutionHarness`, `drainForControlEvent` — supplies the authenticated live-child/client harness and current-menu request path.
- `docs/protocol-mobile.md` → `set_session_settings`, `model_list`, “Security model” — defines the paired-client trust domain, exact client-returned `ModelOption.Value`, and existing shape-validation boundary.
- `cmd/pyry/session_model_list.go` → `retainedModelVocabulary`, `sessionRetainedModelList` — the single source used to publish a bound session’s retained menu with daemon-wide bootstrap fallback.
- `cmd/pyry/main.go` → `settingsUpdaterAdapter.UpdateSettings` — the composition seam where retained cmd-side state can be checked before the sessions primitive mutates or delivers.
- `internal/relay/v2session_seams.go` → `SettingsUpdater`, `ErrSessionUnknown` — the dependency-preserving relay contract and precedent for adapter-returned outcome sentinels.
- `internal/relay/v2session_settings.go` → `handleSetSessionSettings`, `validModel` — the network shape gate and mapping from updater outcomes to correlated wire replies.
- `internal/sessions/pool.go` → `Pool.UpdateSettings`, `deliverSettingsInBand` — the atomic mutation and live `set_model` delivery that validation must precede.
- `cmd/pyry/session_model_list_test.go` → `newModelListTestPool`, `modelListPlan`, `sentinelModelList` — reusable real-pool fixtures for bound, fallback, absent, dropped, and truncated vocabulary states.
- `internal/relay/v2session_settings_test.go` → `TestV2Session_SetSessionSettings_ErrorReplies`, `fakeSettingsUpdater` — the relay-side sentinel-to-wire and privacy-proof surface.
- `internal/e2e/relay_v2_stream_model_list_test.go` → `driveModelListRespawn` — the existing model-menu phone path and fake-Claude menu provenance.
- `internal/e2e/realclaude/interactive_stream_question_answer_test.go` → `startStreamModalResolutionHarness`, `sealSendMessage`, `drainForAnnouncedModel` — the current client-visible settings and later-turn liveness pattern.
- `internal/e2e/realclaude/testdata/set_model_v2.1.259_accept.json` → `set_model_capture.published_models` — #2279’s committed current-version `Value` vocabulary used to choose and prove a valid-shape absent candidate without pinning a future offered model.

## Context

Claude 2.1.259 acknowledges an arbitrary well-shaped `set_model` string and adopts it, then fails the next application turn if the model is not servable. A successful control acknowledgement therefore cannot make `set_session_settings` safe. The daemon already retains the `initialize` model menu used for client publication, so the safe boundary is the cmd-side settings adapter: validate there before calling `Pool.UpdateSettings`, using the same retained value and preserving relay → cmd → sessions dependency direction.

This adds policy at an existing composition boundary and does not warrant an ADR. The documentation stage must update the public protocol contract after implementation.

### Size boundary

- Deliverables: one behavior — validate a requested non-empty model against the retained published menu before the existing atomic update path.
- Production source files: 4 (`cmd/pyry/main.go`, `internal/relay/v2session_seams.go`, `internal/relay/v2session_settings.go`, `internal/sessions/pool.go`).
- Total written work: estimated 650–750 lines including this plan, unit tests, hermetic e2e, and live-Claude proof.
- New exported types or interfaces: 0; two exported relay-local sentinel values extend an existing outcome vocabulary.
- Consumer call sites needing simultaneous update: 0; `SettingsUpdater.UpdateSettings` keeps its signature. Codegraph did not resolve the interface method, and the required source fallback found one production implementation plus the existing relay test double.
- Acceptance criteria: 5.
- Distinct new reject/error outcomes: 3 — offered, conclusively absent, and vocabulary incomplete/unavailable.

Ticket #2281 has parent #2203 and no grandparent. The remote branch comparison found no in-flight feature branch touching the prospective files.

## Design

### Composition-side membership decision

`settingsUpdaterAdapter.UpdateSettings` keeps the current `error` contract. For a nil model or explicit empty model it calls `Pool.UpdateSettings` unchanged. For a non-empty model it first confirms the session id exists, preserving `ErrSessionUnknown` precedence and preventing a hostile unknown id from probing bootstrap-menu completeness.

For a hosted session, it reads `retainedModelVocabulary(pool, sessionID)`, which already prefers the session’s list and falls back to the bootstrap list. The decision is ordered:

1. Any row whose `Value` exactly equals the requested model and whose `TruncatedFields` does not contain `"value"` is offered; proceed to `Pool.UpdateSettings` even if other rows were dropped or truncated.
2. If no such row matches and there is no retained list, no model rows, `DroppedModels > 0`, or any row reports a truncated `value`, return `relay.ErrModelVocabularyUnavailable`.
3. Otherwise the complete retained vocabulary proves absence; return `relay.ErrModelNotOffered`.

Only `Value` participates. `ResolvedModel`, `DisplayName`, aliases inferred outside the list, and normalization never do. The requested value and menu values are never embedded in an error or log.

### Relay outcome mapping

Add `ErrModelNotOffered` and `ErrModelVocabularyUnavailable` beside `ErrSessionUnknown` in `internal/relay`. `handleSetSessionSettings` maps them before its generic failure branch:

- `ErrModelNotOffered` → one non-retryable `protocol.malformed` with a new fixed message.
- `ErrModelVocabularyUnavailable` → one retryable `model_list.unavailable` using the existing fixed model-list-unavailable message.

Each branch records a distinct event name with only the confirmed session id and connection id. The handler does not log the sentinel text, payload, requested model, or retained rows. The existing generic persistence failure path remains separate.

### Delivery failure event

`deliverSettingsInBand` keeps its fire-and-forget contract and existing fields, but adds a static event key to the existing “not delivered” record. That makes a write failure distinguishable from the two validation refusals without adding any model-list or requested-model value.

### End-to-end evidence

The hermetic e2e starts the real daemon with fake Claude’s capture-derived initialize menu and a stdin tee, connects a fake phone, obtains the advertised `model_list`, and loads #2279’s committed 2.1.259 capture to prove the chosen well-shaped candidate is absent from the real published `Value` set and from the advertised retained subset. It sends one frame combining that model with effort/posture, asserts the correlated non-retryable malformed reply, verifies the stdin tee contains no `set_model`, and sends a later ordinary turn whose reply and `turn_end` prove the prior child remains usable.

The live-Claude test requests the current child’s model list through the phone, chooses a valid-shape candidate absent from every returned `Value`, sends `set_session_settings`, and asserts the correlated rejection. It then sends a small application turn and requires its normal completion and announced model to remain the launch/prior model. The builder does not run this live test; the dispatcher’s `needs-real-claude` gate provides the evidence.

## Concurrency model

No goroutine, channel, mutex, or shutdown path is added. Validation is synchronous on the relay dispatch goroutine. The adapter’s session-existence lookup, retained-menu snapshot, and `Pool.UpdateSettings` acquire their existing locks sequentially and never nest them. If the session disappears between validation and mutation, `Pool.UpdateSettings` still returns `ErrSessionNotFound`, mapped to `ErrSessionUnknown`; no mutation or delivery occurs.

The retained list can be replaced by a later child initialize response, but the deep-copied snapshot is the menu published at the validation instant. No acknowledgement wait or new asynchronous state is introduced.

## Error handling

- Unknown session: preserve `ErrSessionUnknown` and `session.not_found`, checked before vocabulary state.
- Nil or empty model: skip membership validation; nil leaves the model unchanged and empty retains restart-to-default behavior.
- Exact untruncated match: call the existing pool update with the whole frame, preserving atomic effort/posture application.
- Complete-menu absence: return the value-free unsupported sentinel before persistence or delivery.
- Missing, empty, dropped, or value-truncated menu: return the value-free temporary-unavailability sentinel before persistence or delivery.
- Persistence failure after successful validation: retain the existing rollback and generic retryable server-unavailable response.
- In-band write failure after successful persistence: retain fire-and-forget behavior and next-spawn argv recovery, with a distinct privacy-safe event key.

## Testing strategy

- RED in `cmd/pyry`: table-driven adapter tests cover exact alias and bracketed matches, explicit empty reset, complete absence, no vocabulary, dropped rows, truncated matching/nonmatching rows, bootstrap fallback, session-not-found precedence, and all-or-nothing model+effort/posture state.
- RED in `internal/relay`: extend error-reply coverage for both new sentinels, exact code/message/retryability, one correlated frame, distinct event keys, and absence of requested/list markers from logs.
- RED in `internal/sessions`: pin the new static delivery-failure event key while retaining the no-model-value assertion.
- Hermetic e2e: capture-backed absent candidate, phone-visible refusal, no fake-Claude `set_model`, unchanged posture/settings, and a successful later turn.
- Build-tagged live test: current-menu-derived absent candidate, phone-visible refusal, and successful later turn on the prior model. Do not execute locally.
- Required gate: `go test -race ./internal/relay/... ./internal/sessions/... ./internal/e2e/...`, `go vet ./...`, and `go build ./cmd/pyry`.

## Open questions

None. The ticket fixes comparison field, completeness rules, empty reset semantics, wire codes, retryability, and the no-acknowledgement scope.

## Documentation handoff

Pending for the documentation stage:

- Update `docs/protocol-mobile.md` under the `set_session_settings` behavior and error-code table to state the exact published-`Value` membership rule, the non-retryable unsupported-model response, the retryable incomplete-vocabulary response, and the unchanged empty-model reset behavior.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `validModel` remains the network shape boundary; `settingsUpdaterAdapter.UpdateSettings` is the single availability boundary and treats subprocess-authored retained rows as evidence only when the exact `Value` is present and untruncated.
- [Tokens, secrets, credentials] No findings — no token, credential, or secret lifecycle changes. Requested and published models are explicitly excluded from errors and logs.
- [File operations] No findings — no new file operation is added; the existing atomic registry write occurs only after validation and rolls back on failure.
- [Subprocess / external command execution] No findings — rejected models reach neither the future-spawn argv nor live `set_model`; accepted values retain the existing discrete argv-element and structured control-request handling. No shell is introduced.
- [Cryptographic primitives] No findings — no cryptographic primitive, key, nonce, or randomness changes.
- [Network & I/O] No findings — the existing 64-byte closed grammar and application-envelope cap remain in force; validation adds no socket read, listener, or unbounded allocation.
- [Error messages, logs, telemetry] No findings — both client messages are static and both validation logs contain only static event names plus confirmed routing identifiers. The delivery failure keeps a value-free error contract and gains a static event key.
- [Concurrency] No findings — all existing locks are acquired sequentially; no goroutine is added. Session removal in the read/update window fails closed at `Pool.UpdateSettings`.
- [Threat model alignment] No findings — the interactive capability gate and paired Noise transport remain unchanged, unknown session ids cannot probe menu completeness, and threat 1’s subprocess-authored values are compared as inert bounded strings and never logged or rendered on the rejection path.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-11

## Revisions

### 2026-09-11 — verifier rework: traverse the full captured menu

The verifier found that `TestRelayV2_StreamRejectsModelAbsentFromPublishedMenu`
only checked the default two-row fake menu for membership in #2279's six-row
capture, so four captured rows never traversed initialize retention or client
publication. The hermetic test now creates an exact `Value`/`ResolvedModel`
projection of every captured row, after asserting the capture is complete, and
passes its path through `PYRY_FAKE_CLAUDE_INITIALIZE_MODELS`. The test-only
`loadInitializeModels` override feeds that projection through
`writeInitializeAck`; the default canned menu remains unchanged for every other
test. The phone-visible menu must match all captured rows in order before the
absent-model rejection and later-turn assertions run.

This adds one touched production-language file under the test harness,
`internal/e2e/internal/fakeclaude/main.go`, bringing the ticket to five such
files and remaining within the one-ticket boundary. The override reads only a
test-supplied local fixture and introduces no production daemon file operation
or trust-boundary change, so the committed security-review verdict remains PASS.
