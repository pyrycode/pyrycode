# Deduplicate the deliberate untrusted-project warning

## Files read

- `internal/codexsup/translate.go` → `Translator`, `Translate`, `warningBanner`, `truncate`, `undecodable`: validation, warning dispatch, bounded output and turn state.
- `internal/codexsup/translate_warning_test.go` → warning scenarios, bounds, malformed input, turn preservation and method lanes: extend existing coverage without replacing it.
- `cmd/pyry/codex_runner.go` → `codexRunner.runOnce`, `notify`: translator construction per spawn and serialized access; no runner changes.
- `internal/codexsup/capture_live_test.go` → `TestCaptureLive`: broad authenticated recapture requires an isolated signed-in home and rewrites unrelated fixtures.
- `docs/knowledge/features/codexsup-package.md` → “The translator's three-list partition” and capture guidance: required pointer prose and inert metadata must remain intact.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification-captures-and-live-evidence.md`: focused race checks and truthful live evidence.

## Context

The deliberately untrusted workspace repeatedly produces the same config warning.
Show its first banner per translator/spawn while retaining different notices.
This is one behavior plus its regression proof, not independently shippable slices.
Estimated written work: about 350 lines including this plan, zero exported types,
one internal call site, four acceptance criteria and two new decision branches.
No decision record is needed.

Branch overlap: #3583, #3584, #3593 and #3594 touch `translate.go`; #3594 also
extends warning tests. Their edits add mappings or warning families; this change
keeps the lists and existing decoding intact and does not depend on those additions.

A real Codex 0.159.2 app-server startup in an isolated temporary untrusted project
produced `configWarning.params.summary` containing the exact case-sensitive text
`is marked as untrusted in the effective configuration`; `details` was null.
No personal authentication was read or copied and no deployed config was changed.
The authenticated conversation and unstable-feature capture remain live-gate work
unless the isolated probe supplies them. Retain redacted excerpts in the PR.

## Design

Make `warningBanner` a translator method; its decoding contract stays unchanged.
After successful decoding, match only `configWarning` whose complete banner prose
(`summary`, plus newline and nonempty `details`) contains the exact fragment above.
Remember a single SHA-256 digest of that first matched, untruncated prose, with an
explicit unset state. Identical matches yield no events; distinct matches still
emit banners and never replace that digest. Hash before the existing banner cap.
State is fixed size; no payload text, path or growing cache is retained or logged.
The digest is an identity fingerprint, not an authentication or trust decision.

## Concurrency model

No new goroutines or locks. `Translator` remains caller-serialized and unsafe for
concurrent use; the runner already serializes notifications and model changes.

## State transitions and identity reuse

| Event | Behavior | Race-detector coverage |
| --- | --- | --- |
| First match, then identical repeats | Banner once, repeats empty | `TestTranslateUntrustedWarningLifetime` |
| Turn completion, next turn and model change | Remember first warning across turns; usage/reroute/model output preserved | `TestTranslateUntrustedWarningLifetime` |
| New translator / app-server respawn | Empty identity, banner again | `TestTranslateUntrustedWarningLifetime` |
| Different full prose, including beyond banner cap | Visible on every occurrence; first identity retained | `TestTranslateUntrustedWarningIdentity` |
| Malformed target before/after valid warning | Bounded undecodable each time, identity unchanged | `TestTranslateUntrustedWarningMalformed` |
| Nonmatches and other methods before/after first warning | Existing handling; no identity mutation | `TestTranslateUntrustedWarningNonmatches` |

## Error handling

Existing required-prose and optional-details validation runs before matching.
Malformed JSON, missing/null/wrong-type prose and wrong-type details retain
bounded `UnrecognizedUndecodable`, even after a valid warning. No new error path.
Target/location metadata stays inert; unknown methods retain their current lane.

## Testing strategy

Add the named scenarios first and observe failure before implementation. Exercise
both summary and details matching, exact case/fragment boundaries, metadata changes,
different fields producing the same banner prose, Unicode/banner truncation and
malformed target-shaped large payloads. Retain all existing tests and partitions.
Run the affected `internal/codexsup` package with race detection on Linux, then
`go vet ./...` and `go build ./cmd/pyry`; complete suites are dispatcher-owned.

## Open questions

No product questions. Dispatcher live acceptance must confirm the matcher against
the real warning in an authenticated Codex conversation and capture both startup
warnings' methods/fields/text and version. Use isolated pre-suppression settings for
the unstable-feature warning if #3598 lands; never alter deployed trust/config.
Inferred payloads, absent captures and skipped checks are not observed passes.

## Documentation handoff

Pending documentation stage: in `docs/knowledge/features/codexsup-package.md`,
under **The translator's three-list partition**, describe the exact warning/method/
field matcher, first-banner/identical-repeat-drop rule with distinct prose still
visible, translator/spawn lifetime and reset, deliberately untrusted workspace
and unchanged handling of other warnings. State whether method/text evidence was
observed live, distinguishing isolated startup evidence from authenticated proof.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `warningBanner` validates prose before identity changes; subprocess prose remains inert display text. A hostile distinct warning must remain visible.
- [Tokens, secrets, credentials] No credentials used by production changes; retain only a digest, never log payloads or local paths. Capture uses an isolated home without copying authentication.
- [File operations] No new production filesystem operations; location/path metadata remains ignored.
- [Subprocesses] No subprocess changes. `codexRunner.runOnce` still owns spawn/reset and shutdown.
- [Cryptography] Standard `crypto/sha256` fingerprints full prose; it is not a secret comparison or authorization check.
- [Network and I/O] No new I/O. Existing transport bounds and UTF-8-safe 4096-byte banner and 16-KiB undecodable caps remain intact.
- [Errors, logs, telemetry] No new logs; malformed input retains the existing bounded fallback and cannot poison deduplication.
- [Concurrency] No goroutines or lock order changes; caller serialization protects the one digest.
- [Threat model] Warning text has no trust/config/routing authority. Workspace remains deliberately untrusted; mobile rendering remains the existing inert-text boundary.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-17

## Revisions

- 2026-10-17: the isolated Codex 0.159.2 probe also started a thread and attempted
  a turn. It observed the unstable-feature warning on `warning.params.message`
  with `agent_message_board=true` and suppression unset. The trust warning was
  observed on `configWarning.params.summary` with null `details`. Both method
  assignments are now observed, not inferred. Authentication was absent and
  the turn failed on transport access, so final conversation/matcher acceptance
  remains pending for the dispatcher live gate. The implementation design is unchanged.
