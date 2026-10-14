# Codex warning notification banners (#3354)

## Files read

- `internal/codexsup/translate.go` → `Translator.Translate`, `reroute`, `truncate`, `undecodable`: mapping, inert banners, byte bounds and fallback contracts.
- `internal/codexsup/translate_test.go` → `TestMethodListsPartitionServerNotifications`, `TestTranslateUnrecognizedLanes`, `TestTranslateBounds`: schema partition and unknown-lane coverage to retain.
- `internal/codexsup/serverrequest_test.go` → `loadSchema`, `jsonSchema.validate`, `wireValue`: committed-schema validation conventions.
- `internal/codexsup/codex_app_server_protocol.schemas.json` → `v2/WarningNotification`, `v2/ConfigWarningNotification`, `TextRange`: required string prose and nullable optional metadata/details.
- `internal/turnevent/event_status.go` → `Banner`: display-only text with no turn identity or lifecycle effects.
- `docs/knowledge/features/codexsup-package.md` § The translator's three-list partition: every method occupies exactly one lane; unrecognized events must remain visible.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: regressions must fail against the old mapping.
- `CODING-STYLE.md` and `docs/protocol-mobile.md` § Security model: inert untrusted prose and no payload logging.

## Context

Mobile #2052 completed its replies but failed its parser-gap sentinel because known
`warning` and `configWarning` notifications still use the unknown-method lane.
Map these notices onto the existing banner contract without suppressing the sentinel.
This is one deliverable, estimated at about 300 written lines including plan/tests,
zero exported types, zero consumer updates, three acceptance criteria and fewer than
ten reject branches. The #2668 analogue changed the same translator boundary but
needed fewer malformed-input cases. No decision record is needed.
Fetched feature branches have no overlap with the proposed files.

## Design

Move both methods exclusively into `mappedMethods`. `Translator.Translate` dispatches
them to a stateless helper that decodes only their prose fields. Required strings
use pointers to distinguish missing/null from a valid empty string. `warning` uses
`message`; `configWarning` uses `summary`, appending nonempty nullable `details`
with a newline. Invalid JSON, non-object params, invalid required prose and invalid
details return `undecodable(params)`. Target/location metadata is neither decoded
nor used. Apply `truncate(text, maxBannerText)` to the combined prose and emit exactly
one `Banner{Level: "warning"}` with `Truncated` reflecting cutting and `StopsTurn`
false. Unknown methods retain their current fallback.

## Concurrency model

Translation stays synchronous on the existing notification caller. No goroutines,
locks, I/O or new shared state are introduced.

## State transitions and identity reuse

None: warning translation holds no lifecycle or identity state. Repeated warnings,
including targeting metadata, must not alter pending usage, reroute or model state;
`TestTranslateWarningsPreserveTurn` will compare completed-turn output against a
control translator under `go test -race`.

## Error handling

Reuse the bounded, UTF-8-safe `UnrecognizedUndecodable` fallback, including for
oversized malformed payloads. Never log payloads or add silent ignore paths.

## Testing strategy

Write credential-free table-driven regressions before implementation in
`internal/codexsup/translate_warning_test.go`. Validate valid cases against the
committed notification schemas: empty prose and optional fields absent/null/present;
exact-limit, over-limit and multibyte-cut prose for both methods, including combined
summary/details. Assert the complete single banner and unchanged turn output on
repetition. Exercise malformed JSON, all non-object JSON kinds, missing/null/wrong
prose types, wrong details types and bounded fallback. Pin mapped-only membership
and a genuinely unknown method alongside existing partition/unknown-lane tests.
Run `go test -race ./internal/codexsup/...`, `go vet ./...` and
`go build -o /tmp/builder-3354/pyry ./cmd/pyry`; disable live capture environment
inputs explicitly. Start no daemon/login and access no authentication.

## Open questions

None.

## Documentation handoff

Pending documentation stage: in `docs/knowledge/features/codexsup-package.md`, under
**The translator's three-list partition**, describe both warning mappings,
message/summary plus optional details, the 4096-byte UTF-8-safe cap and `Truncated`,
malformed-input fallback, inert targeting/location metadata, and unchanged turn lifecycle.

Pending Mobile dispatcher after merge: provide the daemon revision for Mobile
[#2052](https://github.com/pyrycode/pyrycode-mobile/issues/2052)'s fresh counted live
suite executing `interactiveTurn_switchAgentFromModelPick_confirmsAndReplies`,
preserving reply/history/identity/effort assertions and `UnrecognizedRowSentinel`.
This is separate from daemon acceptance and is not run by this builder.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The warning helper decodes untrusted subprocess prose into display-only `Banner` fields; missing/null required prose is rejected rather than silently accepted as empty.
- [Tokens, secrets, credentials] No findings. No credential operation is added; tests use synthetic data and explicitly disable live capture inputs.
- [File operations] No findings. `path` and `range` are omitted from decode targets; no filesystem operations use warning metadata.
- [Subprocesses] No findings. Warning translation starts no subprocess or shell and interprets no warning bytes as commands.
- [Cryptography] No findings. Existing encrypted banner transport is reused with no key or nonce changes.
- [Network and I/O] No findings. No transport changes; combined text is capped at 4096 bytes with `truncate`, malformed raw at `maxUnrecognizedRaw`.
- [Errors, logs, telemetry] No findings. No logging is added; fallback payloads remain bounded events rather than log fields.
- [Concurrency] No findings. The helper is stateless, and repeated warnings cannot mutate translator maps or the selected model.
- [Threat model] No findings. Under `docs/protocol-mobile.md` § Security model, warning prose remains untrusted display text, never routing, authorization or execution input. Existing client rendering and encrypted transport contracts are unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-14
