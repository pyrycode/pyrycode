# Codex deprecation and guardian warning banners

## Files read

- `internal/codexsup/translate.go` → `Translator.Translate`, `warningBanner`, method lists, `truncate`, `undecodable`: reuse the existing prose-only warning contract and bounds.
- `internal/codexsup/translate_warning_test.go` → `TestTranslateWarnings`, `TestTranslateWarningBounds`, `TestTranslateMalformedWarnings`, `TestTranslateWarningsPreserveTurn`, `TestWarningMethodLanes`: extend both warning shapes and replace the guardian unknown-method expectation.
- `internal/codexsup/serverrequest_test.go` → `loadSchema`, `jsonSchema.validate`: validate successful examples against the pinned schema.
- `internal/codexsup/codex_app_server_protocol.schemas.json` → `GuardianWarningNotification`, `DeprecationNoticeNotification`: guardian examples require `threadId`; deprecation details are nullable and optional.
- `docs/knowledge/features/codexsup-package.md` § The translator's three-list partition: pointer-valued prose distinguishes valid empty strings from missing/null fields; targeting stays inert.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification.md`: focused race checks, contract comments and regression proof.

## Change

Move `guardianWarning` and `deprecationNotice` exclusively into `mappedMethods`
and dispatch them through `warningBanner`. Guardian uses the existing `message`
branch; deprecation uses the existing `summary`/`details` branch. Both emit one
warning banner with `StopsTurn: false`. Preserve empty required strings, append
only nonempty details with a newline, and retain the UTF-8-safe 4096-byte text
cap and `Truncated`. Missing/null/wrong-type prose, wrong-type details, malformed
JSON and non-object params use the existing bounded undecodable fallback.
No payload logging, routing or filesystem action is added; target metadata is
not decoded. No exported types, consumers or lifecycle state change.

Overlaps: #3583, #3584 and #3585 touch `translate.go` in independent mappings;
keep this change local to warning dispatch, classification and the helper guard.

Sizing: one deliverable, approximately 130 written lines including plan and
tests, zero new exported types, zero consumer updates, two acceptance criteria,
and no new state-machine rejection branches; all five limits remain satisfied.

## Testing strategy

Extend the existing warning tables for both methods, preserving old cases.
Schema-check successful examples (including guardian `threadId`), exercise empty
prose and optional details, exact/over-limit and multibyte bounds, malformed
params and bounded raw fallback. Extend repeated warning/control comparisons
to prove targeting cannot change translator state or completed-turn output.
Assert both methods occupy only `mappedMethods` and unknown methods still surface.
Run tests before implementation to establish the regression, then run affected
package tests with `-race`, `go vet ./...` and `go build ./cmd/pyry` on Linux.
The verifier owns the complete suite.

## Documentation handoff

- Pending documentation stage: update `docs/knowledge/features/codexsup-package.md`, **The translator's three-list partition**, with both mappings and text/fallback rules.

## Security review

**Verdict:** PASS

- Trust boundaries: `warningBanner` accepts untrusted subprocess JSON, decodes only prose into pointer strings, and produces inert display events. Keep metadata omitted from decoding.
- Tokens, errors and logs: no credentials or log calls are introduced; payloads remain solely in bounded display events, including the existing undecodable fallback.
- File operations, subprocesses and network: no new I/O or execution; this change only translates notifications already received by the client.
- Cryptography: no cryptographic operations or keys are involved.
- Concurrency: no goroutines, locks or shared-state writes are introduced.
- Threat model: banner prose remains untrusted under `docs/protocol-mobile.md` § Security model; it never controls routing, filesystem access or turn termination. The existing UTF-8-safe caps apply to both output paths.

**Findings:** No MUST FIX or SHOULD FIX findings.
**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-17
