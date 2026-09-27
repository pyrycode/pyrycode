# #2671 — Codex clarifying questions through the question bridge

## Files read

- `cmd/pyry/codex_home.go` → `codexHomeConfig`, `prepareCodexHome` — the daemon-owned Codex configuration baseline and its atomic, permission-preserving rewrite.
- `cmd/pyry/codex_runner.go` → `codexRunner.runOnce`, `codexApprovals.handle`, `codexApprovals.await`, `codexApprovals.observe`, `codexApprovals.declineAll`, `codexApprovalRequest` — the sole production adapter for Codex server requests and every terminal path that resolves a parked request.
- `cmd/pyry/modal_resolve_v2.go` → `streamApprovalBridge.Surface`, `streamApprovalBridge.AnswerQuestion`, `streamApprovalBridge.RefuseQuestion`, `answerVerdict`, `streamApprovalBridge.retireQuestion` — the existing question surface, answer validation, one-shot dismissal, and refusal semantics that Codex will reuse unchanged.
- `internal/questionbridge/questionbridge.go` → `Parse`, `ToolName` — the 16 KiB fail-closed input boundary and the shared 1–4 question / 2–4 option shape.
- `internal/questionbridge/registry.go` → `Registry.Record`, `Registry.Resolve` — the daemon-minted batch correlation and exactly-once resolution used by `streamApprovalBridge`.
- `internal/codexsup/serverrequest.go` → `ServerRequest`, `ServerRequest.Respond`, `ServerRequest.Decline` — the deferred JSON-RPC response capability and existing default-decline behavior.
- `internal/codexsup/codex_app_server_protocol.schemas.json` → `ToolRequestUserInputParams`, `ToolRequestUserInputQuestion`, `ToolRequestUserInputResponse` — the pinned Codex request and answer shapes.
- `internal/e2e/internal/fakecodex/main.go` → `turn.run`, `turn.approval`, `server.logTurn` — the offline app-server marker and durable response log patterns to extend.
- `cmd/pyry/codex_approval_test.go` → `newApprovalCodexRunner`, the approval lifecycle tests — the fake-backed runner harness and terminal-path assertions this feature mirrors.
- `cmd/pyry/codex_runner_test.go` → `TestPrepareCodexHome` — the existing posture-preservation assertion for the daemon-owned config.
- `docs/knowledge/features/codexsup-package.md` → “Deny-by-default” and “Production wiring” — the current server-request partition, runner lifecycle, and content-free logging constraints.
- `docs/knowledge/features/development-verification.md` → “Prove that tests distinguish the change” and “Protocol boundaries” — tests must exercise each eligibility predicate and compare complete emitted payloads.
- `docs/knowledge/decisions/038-codex-daemon-owned-home-read-only-default-decline.md` → “Superseded in part” — the default-decline baseline already has one partial supersession and must gain another in the documentation stage.

## Context

Codex 0.156.1 asks clarifying questions with the server-initiated `item/tool/requestUserInput` method. The runner currently sends that method down `ServerRequest.Decline`, and the daemon-owned config does not enable the tool in Codex's default collaboration mode. The existing Claude question bridge already owns the client protocol, device answer gate, parked batch registry, and exactly-once dismissal semantics. This change adds Codex as a producer and response consumer without changing that bridge or the mobile protocol.

No new ADR is needed. ADR 038 already records the deny-by-default decision and its partial supersession mechanism; the documentation stage will amend that record for this newly eligible method.

Size re-check: three production files (`codex_home.go`, `codex_runner.go`, and the fake Codex test binary), two test files, this plan, about 550–650 written lines, no exported type, no signature cascade, five acceptance criteria, and at most nine request-rejection predicates. No in-flight feature branch overlaps these files.

## Design

### Daemon-owned configuration

`codexHomeConfig` gains a `[features]` table with `default_mode_request_user_input = true`. The existing top-level `approval_policy`, `sandbox_mode`, and `approvals_reviewer` values stay byte-for-byte unchanged. `TestPrepareCodexHome` asserts all four values after an overwrite.

### Request adaptation and correlation

`codexApprovals.handle` first classifies `item/tool/requestUserInput` with a new pure parser. It accepts only a schema-shaped batch with 1–4 questions where:

- every required string and boolean field is present;
- question IDs are pairwise unique and question texts are pairwise unique;
- `isOther` is true and `isSecret` is false;
- every question has 2–4 schema-shaped options.

The parser converts the accepted questions, in order, into the existing `AskUserQuestion` input shape: header, question, options, and `multiSelect: false`. It then calls `questionbridge.Parse` with `questionbridge.ToolName`. That call remains the single displayed-input size boundary and rejects an adapted input over 16 KiB or outside the shared question/option bounds. Unknown Codex params remain ignored, matching the shared parser's forward-compatible posture.

The resulting `permbridge.Request` uses the adapted input and `AskUserQuestion` tool name, so `streamApprovalBridge.Surface` takes its existing question arm and emits exactly one batch. A `codexApproval` for this request retains an ordered slice of `{id, text}` pairs. This runner-local metadata never enters the wire or a log; it exists only until `await` writes or suppresses the Codex response.

Malformed or ineligible question requests return no adapted request and continue through `ServerRequest.Decline`, exactly like every unsupported server request today. Command and file-change approval classification remains unchanged.

### Answer translation

The shared bridge continues to validate the device answer against its parked batch. For a valid single-select answer it resolves `permbridge` with updated input whose `answers` object is keyed by question text. `codexApprovals.await` detects the retained Codex mapping and translates that trusted bridge result into Codex's response shape:

- one `answers` member per retained Codex question;
- each member is keyed by the original Codex ID;
- each value is `{ "answers": [value] }`, where `value` is the selected label or free text produced by `answerVerdict`.

The translator requires an allow verdict, exactly one string value for every retained text, and no missing mapping. Any deny, empty, malformed, or incomplete verdict produces `{"answers":{}}`; it can never manufacture a partial response. Approval requests keep their existing `{"decision": ...}` response path.

### Terminal behavior

The lifecycle stays under the existing two one-shots. `streamApprovalBridge.AnswerQuestion` or `RefuseQuestion` consumes the question registry; `codexApprovals.await` consumes the permission verdict, writes at most one Codex response, and invokes the retire closure. Window expiry, refusal, interrupt, teardown, or process exit therefore return an empty answers object and let `retireQuestion` emit the one remaining dismissal. A successful answer emits its dismissal in `AnswerQuestion`, so `retireQuestion` observes an already-consumed batch and emits no duplicate.

`serverRequest/resolved` continues to set `withdrawn` before resolving the parked registry entry. For a Codex question this clears and dismisses the batch through the same retire closure, while `await` suppresses the response entirely.

### Fake Codex

The fake gains a question marker that sends a multi-question `item/tool/requestUserInput` request, waits like its approval marker, records the received response in `FAKECODEX_TURN_LOG`, and then completes the turn. A withdrawal variant sends `serverRequest/resolved` before an answer, using the existing late-response detector. The fixture values are deliberately distinct across IDs, texts, headers, option labels, and descriptions so field swaps cannot pass.

## Concurrency model

No new goroutine kind or lock is introduced. `handle` parses and parks on the Codex read loop, then starts the existing one-`await`-goroutine-per-request path. The immutable ID/text slice belongs to that parked request. `codexApprovals.mu` remains a leaf and is not held while parsing, surfacing, waiting, resolving, or responding. Every existing terminal path resolves the registry entry, so every `await` goroutine exits. The permission and question registries remain the independent one-shot arbiters for response and dismissal races.

## Error handling

- Missing fields, wrong types, duplicate IDs/text, ineligible flags, option-count violations, adapted-input overflow, JSON failures, RNG failure, and registry failure all fail closed; no question or permission frame is emitted for a malformed/ineligible Codex request.
- A response-translation mismatch returns an empty answers object rather than a partial answer.
- A dead Codex process can make `Respond` fail; as in the existing approval path, the write error is ignored after local cleanup because there is no live peer to recover.
- No error or log message contains a question, header, option label, option description, or answer value.

## Testing strategy

- Extend `TestPrepareCodexHome` to pin the feature flag beside the three unchanged posture values.
- Pure table tests cover the valid adaptation and each independent rejection predicate, including duplicate IDs, duplicate texts, both flag violations, missing required fields, option under/overflow, malformed JSON, and a displayed batch computed past the shared size cap.
- A fake-backed bridge test wires the real `streamApprovalBridge`, asserts one `question_shown` carrying the complete ordered Codex batch with `multi_select: false`, answers with one offered label and one free-text value, verifies the fake's ID-keyed response log, and asserts exactly one `question_dismissed`.
- Table-driven fake-backed terminal tests cover refusal, window expiry, interrupt, teardown, and process exit returning empty answers and one dismissal. A withdrawal test covers dismissal with no response and no late write.
- Existing command/file-change approval tests remain green, proving the sibling response shape did not regress.
- Builder gate: `go test -race ./cmd/pyry/... ./internal/e2e/internal/fakecodex/...`, `go vet ./...`, and `go build ./cmd/pyry`.
- Dispatcher-owned live gate remains pending under `needs-real-claude`: GPT-6 Luna at low effort first, with GPT-6 Sol fallback only if Luna does not call the tool.

## Open questions

None.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md` § Question — state that a batch may originate from Claude `AskUserQuestion` or Codex `item/tool/requestUserInput`.
- `docs/knowledge/decisions/038-codex-daemon-owned-home-read-only-default-decline.md` § “Superseded in part” — eligible Codex user-input requests now reach the operator; invalid and unsupported server requests remain default-declined.
- `docs/knowledge/features/codexsup-package.md` — record `default_mode_request_user_input`, request/answer translation, terminal behavior, and the untrusted-content/logging boundary.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No finding — `codexQuestionRequest` is the single subprocess-to-daemon boundary: it validates required shape and eligibility, constructs a fresh shared-bridge input, and retains only ordered ID/text correlation. The client answer is independently validated against the daemon's parked batch by `answerVerdict`.
- [Trust boundaries] SHOULD FIX (included in the design) — duplicate Codex IDs would overwrite response members and duplicate texts would collapse the shared bridge's text-keyed verdict. Reject both before parking; never attempt last-write-wins recovery.
- [Tokens, secrets, credentials] No finding — the change creates no credential. Existing question batch and approval correlation IDs remain `crypto/rand` nonces; Codex IDs are untrusted opaque map keys and never authorization tokens.
- [File operations] No finding — the only file change is the existing atomic `prepareCodexHome` rewrite at `0700`/`0600`; the feature flag adds no path or new file operation.
- [Subprocess / external command execution] No finding — question strings are decoded data, never argv or shell input. Enabling the feature exposes only the already-handled server request; it does not loosen the existing approval or sandbox posture.
- [Cryptographic primitives] No finding — no primitive changes; existing registries continue to use `crypto/rand`.
- [Network & I/O] SHOULD FIX (included in the design) — the adapted displayed input must pass `questionbridge.Parse`, preserving its 16 KiB whole-batch rejection and 1–4/2–4 cardinality bounds. No new unbounded control frame is introduced.
- [Error messages, logs, telemetry] No finding — parsing, correlation, response translation, and terminal cleanup add no log call. Question text, headers, labels, descriptions, and answer values therefore have no logging sink; only the fake's explicitly test-owned turn log records a response.
- [Concurrency] No finding — immutable mapping state is owned by one parked request; `codexApprovals.mu` remains a leaf; the existing registry one-shots arbitrate answer/terminal races; every parked request retains the same bounded shutdown path.
- [Threat model alignment] No finding — the existing `QuestionResolver` device-eligibility gate, daemon-minted batch nonce, and conversation-scoped broadcast are reused unchanged. OUT OF SCOPE: changing mobile wire types or broadening which devices may answer; neither is needed for this producer.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-27
