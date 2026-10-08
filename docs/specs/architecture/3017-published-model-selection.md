# Published model selection (#3017)

## Files read

- `cmd/pyry/pool_adapters.go` → `UpdateSettings`, `effortLevelsFor`, `Capabilities`, `followFamily`: membership, effort and canonical execution boundaries; mint and switch retain their contracts.
- `cmd/pyry/session_model_list.go` → `modelListFor`, `agentModelVocabulary`, `retainedModelVocabulary`: published rows and bound/bootstrap/saved source priority.
- `cmd/pyry/session_router.go` → `resolveBoundRunSettings`, `settingsOf`, `runSettingsPool`: live/dormant settings and confirmed posture, with no bootstrap settings fallback.
- `cmd/pyry/relay.go` → `runConfigFor`, `startRelayV2`; `cmd/pyry/main.go` → `runSupervisor`: wire reply and production composition.
- `internal/modelfamily/modelfamily.go` → `Alias`, `Reduce`: variants are distinct; alias wins, else newest pinned row.
- `cmd/pyry/model_vocabulary_store.go` → `Load`: saved vocabularies are reduced before publication.
- `cmd/pyry/session_model_list_test.go`, `dormant_settings_write_test.go`, `agent_switch_frames_test.go`: existing real pools, model holds and encrypted phone doubles.
- `internal/relay/v2session_settings.go` → `handleSetSessionSettings`: shape validation before mutation and session-ID-only acknowledgement.
- `docs/knowledge/features/sessions-package.md`; `sessions-package-key-types-sessionsettings-claudesettingsargs.md`: stored and executed settings follow families, so acceptance alone cannot fix readback.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: inspect authenticated reply bytes and explicit fields.
- `CODING-STYLE.md`; `docs/protocol-mobile.md` § Security model: contract comments and remote input boundary.

## Context

The published pinned-only Fable variant is rejected after conversion to a family
alias absent from the menu. Canonical storage also cannot identify that pinned
menu row on readback. One deliverable: offered family/variant selection agrees
across publication, acceptance, effort capabilities and readback, while execution
continues to follow the family. No new decision record is needed.

## Design

Add `cmd/pyry/session_model_selection.go` with a private menu-identity helper:
given harness, retained vocabulary and model, return the full offered value with
the same canonical family/variant, skipping truncated values. Codex matches
exact identifiers. An empty model remains empty; a miss retains the canonical
value so existing membership error classification and effort fallback survive.
The vocabulary already holds one row per family/variant; do not invent a row or
use `ResolvedModel` as its identity.

`settingsUpdaterAdapter.UpdateSettings` resolves the requested model against that
snapshot before validation; it canonicalizes only after model/effort checks and
before the pool write. `effortLevelsFor` resolves stored aliases through the same
helper, covering effort-only writes and capability reads.

Add `runSettingsFor(registry, pool, saved)` as the production closure. First use
`resolveBoundRunSettings`, then resolve its model with that session's harness and
the same vocabulary sources. Preserve all other fields and refusal behavior.
Wire this closure in `runSupervisor`; `runConfigFor` forwards its result unchanged.
Keep `sessionMinter.Create` and `conversationAgentSwitcher.Switch` unchanged.
Overlap: #3014 edits a separate startup block in `main.go`; no dependency.

Sizing after plan: approximately 600 total written lines, zero new exported types,
three existing production consumer updates, four acceptance criteria, no new
state machine or error branches. All five builder limits remain satisfied.

## Concurrency model

No new goroutines or retained selection state. Existing registry, pool and model
hold reads take their locks sequentially. The settings write uses existing atomic
pool persistence; vocabulary changes affect the next read rather than a cache.

## State transitions and identity reuse

| Event | Race-enabled regression |
| --- | --- |
| Repeated selection and repeated reads of X with unchanged vocabulary | `TestPublishedModelSelection_SettingsRoundTrip` |
| Reopening X through a second phone connection | `TestPublishedModelSelection_SettingsRoundTrip` |
| Dormant bound session update and read without materialization | `TestPublishedModelSelection_SettingsRoundTrip` |
| Selecting X preserves Y's model and effort | `TestPublishedModelSelection_SettingsRoundTrip` |
| Vocabulary replacement changes offered identity, preserving family execution | `TestPublishedModelSelection_VocabularyRefresh` |

## Error handling

Unknown session precedes vocabulary access. Missing/incomplete vocabulary keeps
existing unavailable classification. An unoffered family/variant remains rejected;
truncated values cannot prove membership. Every rejected frame preserves every
requested field. Empty reset and Codex identifiers keep existing behavior.

## Testing strategy

Write the encrypted round-trip regression before implementation, using reduced
saved vocabulary and existing pool/phone doubles. Cover pinned-only and
alias-present menus, newest-row/alias preference, live and dormant X, repeated
reads, reopen, acknowledgement bytes, canonical storage and sibling isolation.
Table-test selection misses/truncation/Codex and check model-specific effort
acceptance, refusals and capabilities. Watch pinned-only fail on the original
adapter. Run `go test -race ./cmd/pyry/...`, `go vet ./...`, and
`go build -o /tmp/builder-3017/pyry ./cmd/pyry`. The verifier owns the full gate.

## Open questions

None. Readback identity follows the current offered row; it does not pin execution.

## Documentation handoff

Pending for documentation stage:
- `docs/protocol-mobile.md` §§ `model_list`, `session_settings`: alias is preferred,
  otherwise newest pinned row represents the family/variant; a selectable published
  value round-trips through settings readback while execution follows the family.
- `docs/protocol-mobile.md` § `session_settings_updated`: keep the session-ID-only acknowledgement.
- `docs/knowledge/features/sessions-package-key-types-sessionsettings-claudesettingsargs.md`,
  paragraphs “What is stored is the family too” and “The --model value is always a
  family alias”: distinguish canonical stored/execution settings from published-row readback identity.

## Security review

**Verdict:** PASS

**Findings:**
- [Trust boundaries] Selection uses only uncut offered values; `validModel` still
  validates remote shape before the adapter and unknown-session checks precede vocabulary.
- [Tokens, secrets, credentials] No credential reads or changes; existing Noise handshake and device authorization gate remain upstream.
- [File operations] No production file operation added; existing pool persistence remains atomic with rollback, using server-owned session IDs.
- [Subprocesses] Only `followFamily` output reaches storage/execution; argv remains tokenized and variant groups keep `Alias`'s narrow syntax.
- [Cryptography] No primitive, key, nonce or comparison changes.
- [Network and I/O] Reuses existing encrypted-frame decoder and bounds; no new transport or reader. Menu iteration remains bounded by the retained vocabulary.
- [Errors, logs, telemetry] Existing fixed sentinels preserved; no model, effort or caller ID logging added.
- [Concurrency] No new lock nesting, goroutines or mutable selection cache; atomic pool writes preserve whole-frame refusal.
- [Threat model] Paired interactive authorization and AEAD remain upstream; this change alters model identity only and adds no prompt, credential or network authority.

**Reviewer:** builder (self-review)
**Date:** 2026-10-08
