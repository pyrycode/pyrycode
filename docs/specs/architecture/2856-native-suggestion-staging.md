# #2856 — repair native reply-suggestion live staging

## Files read

- `internal/e2e/realclaude/interactive_stream_reply_suggestion_test.go` → `installSuggestCLI`, native/fallback tests, `suggestWatch`: producer isolation and bounded set/clear proof.
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` → `startPerConversationHarnessSeeded`: authenticated fresh home and daemon lifecycle.
- `internal/streamsup/parser.go` → `emitPromptSuggestion`, `decodePromptSuggestion`: native source rejection contract.
- `cmd/pyry/reply_suggestion.go` → `turnEnded`, `suggest`, `accepted`: eligibility, native-first publication and clear semantics.
- `docs/knowledge/features/e2e-realclaude.md` § Test infrastructure: missing native output must fail; fallback cannot supply the native proof.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md` § Native reply suggestions after the result: post-result ordering and confirmed-delivery requirements.
- `docs/knowledge/features/development-verification.md` § Test execution and artifact survival: count execution and skips rather than relying on exit status.
- `CODING-STYLE.md`: stdlib tests, bounded subprocess lifecycle and no payload logging.
- Installed Claude 2.1.280 suggestion generator: `Vt` suppresses `allowed_warning` unless `CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION` is explicitly true; the CLI flag alone does not set that environment override.

## Context

The named test fails after six completed coding turns on both main and #2854.
Existing reports record no wire suggestion but cannot identify its source.
Claude's installed generation guard suggests a staging omission: the native
wrapper leaves the explicit enable environment unset. Confirm this using
redacted source observations before calling it the cause. No decision record is needed.

## Design

Keep all changes in the suggestion test infrastructure. The native wrapper
continues rejecting non-stream launches and forwards real Claude stdout unchanged.
Observe selected source metadata before forwarding: completed result counts,
nonempty native suggestion counts/lengths, and allowlisted rate-limit statuses.
Persist only these scalars in a private temporary evidence file, never payloads.
Report those observations after each bounded wait and require native source
evidence alongside the wire set. Source absence distinguishes generation failure;
source presence without a wire set directs investigation to parser/publication.

Set `CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=1` only for native persistent launches;
retain fallback's explicit disable on persistent launches. Do not widen six turns
or the 30-second window. Keep conversation filtering and explicit-null clear at
a higher revision after follow-up input. Do not change parser/product behavior
unless source evidence establishes a downstream defect; record any such change
in Revisions before expanding the implementation.

Overlap: #2854 has no edits to the target suggestion test file; no dependency.
Sizing: one deliverable, three acceptance criteria, approximately 350 written
lines including plan/tests; zero exported types, two helper consumers, fewer
than ten reject branches. All five limits remain within the builder ceiling.

## Concurrency model

Retain the single phone reader and harness cleanup. A stdout observer subprocess
exits at Claude stdout EOF or daemon process-group shutdown; the persistent
Claude process retains its launcher PID and inherited stdin. Evidence is written
before each observed line is forwarded, so completed wire observations can read
the corresponding evidence without polling. No new Go goroutines are needed.

## Error handling

Wrapper/evidence errors fail loudly without including source text. Missing native
output remains a failure with credentials present, never a skip. Standard binary
and credential guards remain unchanged. Unknown rate statuses are not retained.

## Testing strategy

First add hermetic wrapper checks using a local CLI stand-in: native non-stream
refusal, explicit enable overriding inherited disable, fallback stream disable,
and unchanged stdout with metadata-only evidence. Watch the enable assertion fail
before implementation. Run the named native test through the targeted live
launcher with source diagnostics before/after correcting staging. Run its
fallback sibling too; report actual executed/passed counts and skip reasons.
Run tagged scoped race tests, tagged vet, `go vet ./...`, and build `cmd/pyry`
to a scratch path. The dispatcher owns the full live suite and verifier gate.

## Open questions

- Does source observation confirm the `allowed_warning` suppression? Resolve
  using the targeted live run; carry the confirmed result in Revisions and PR.

## Documentation handoff

- Pending documentation stage: `docs/knowledge/features/e2e-realclaude.md`,
  “Test infrastructure”: update the native-suggestion staging paragraph with the
  confirmed failure cause and corrected setup. State that fallback remains
  blocked in the native proof and missing native output still fails with
  credentials present.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: Claude stdout remains untrusted; the observer forwards exact bytes and only projects fixed metadata. Native wire proof still excludes fallback and injected source frames.
- Tokens/secrets and logs: SHOULD FIX during build: evidence must never retain generated text, stderr, environment values or credentials; use only counters, lengths, booleans and allowlisted statuses. Tests plant sentinel text to verify exclusion.
- File operations: evidence and launcher live in test-owned private temporary directories; evidence is created at `0600`, launcher at `0700`. No caller-controlled paths or durable records.
- Subprocesses: fixed CLI executable and argv, no shell interpretation; preserve native rejection before launching. Observer exits on EOF or existing daemon group termination.
- Cryptography: existing Noise phone reader owns ordered receive nonces; no crypto changes.
- Network/I/O: retain bounded phone waits and existing harness transport size/deadline limits; no new network entry point.
- Concurrency: evidence is single-writer metadata flushed before stdout forwarding; retain phone reader cleanup and daemon shutdown.
- Threat model: test-only staging changes no relay authorization or production trust boundary. Authenticated setup remains owned by the normal harness and restricted live launcher.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-06
