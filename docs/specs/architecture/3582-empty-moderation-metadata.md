# Ignore empty Codex moderation metadata

## Files read

- `internal/codexsup/translate.go` → `Translate`, method lists, `unrecognized`, `undecodable`, `truncate`: dispatch and bounded fallback contracts.
- `internal/codexsup/translate_test.go` → partition and hand-built schema tests: preserve classification and validate the new fixture.
- `internal/codexsup/translate_warning_test.go` → warning regressions: table-driven fallback and UTF-8 bound patterns.
- `internal/codexsup/serverrequest_test.go` → `jsonSchema.validate`: fixture validation accepts opaque metadata.
- `internal/codexsup/codex_app_server_protocol.schemas.json` → `TurnModerationMetadataNotification`: metadata accepts any JSON value.
- `docs/knowledge/features/codexsup-package.md` → “The translator's three-list partition”: known noise must not hide unknown methods; preserve bounded fallbacks.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification.md`: focused tests and schema evidence.

## Change

Move `turn/moderationMetadata` from `unrecognizedMethods` to `mappedMethods` and add a stateless handler in `translate.go`. Decode object params with metadata as `json.RawMessage`, rejecting missing, malformed or non-object params through `undecodable`. Suppress only absent/null metadata and empty objects/arrays, allowing JSON whitespace. Preserve every other opaque value through the existing `UnrecognizedCodexMethod` fallback with the original params; do not decode numbers into floating point or rebuild the raw JSON. No turn state, concurrency, identity, callers or exported types change. Remote feature branches checked: no overlap with the planned files.

Sizing: one behavior, two acceptance criteria, approximately 200 total written lines including tests and this plan; zero exported types and consumer updates; no state machine. All builder limits remain satisfied.

## Testing strategy

Write table-driven regressions first and observe failure for empty metadata and invalid params. Cover whitespace, all JSON scalar types, containers holding null, malformed/absent/non-object params, unchanged raw text, byte-limit boundaries and UTF-8 truncation in both fallback lanes. Assert the method occupies only the mapped list and unknown methods still surface. Replay a nonempty hand-built notification and run `TestHandBuiltFramesMatchSchema` and `TestMethodListsPartitionServerNotifications`. Run the affected `internal/codexsup` package with race detection through test admission, then `go vet ./...` and `go build ./cmd/pyry` on macOS. Full-module acceptance belongs to the verifier.

## Documentation handoff

- Pending documentation stage: in `docs/knowledge/features/codexsup-package.md`, under **The translator's three-list partition**, state the empty-metadata rule and fallbacks: object params with absent/null metadata or empty object/array emit nothing; every other metadata value surfaces bounded original params as `UnrecognizedCodexMethod`; malformed, absent or non-object params surface bounded `UnrecognizedUndecodable`.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `Translate` treats subprocess JSON as untrusted. The new handler validates the whole object before checking emptiness; false, zero, empty strings and containers containing null must remain visible.
- Tokens, secrets, credentials: no credential handling. Original params retain the existing bounded display-text fallback; no new payload logging.
- File operations and subprocesses: the handler performs neither; the fixture contains synthetic identifiers only.
- Cryptography: no key, nonce or cryptographic operation changes.
- Network and I/O: no new I/O. Existing ACP framing limits input; `unrecognized` caps output at `maxUnrecognizedRaw` with UTF-8-safe truncation.
- Errors, logs, telemetry: `undecodable` preserves the existing bounded diagnostic event and adds no logs or telemetry.
- Concurrency: the helper holds no state and starts no goroutines; repeated notifications are independent.
- Threat model: metadata remains untrusted display text with no routing, filesystem, command execution or approval meaning. No security work is deferred.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-17
