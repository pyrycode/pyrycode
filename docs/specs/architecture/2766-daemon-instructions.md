# #2766 — Durable daemon-wide conversation instructions

## Files read

- `internal/sessions/pool.go` → `New`, `buildSessionAs`, `dataDir`: constructor, conversation construction and daemon storage root.
- `internal/sessions/systemprompt.go` → `composeSystemPromptForOn`, `writeSystemPromptFile`, `writeComposedPrompt`, both refresh functions: composition, atomic private files, next-start refresh and rotation's no-resolve constraint.
- `internal/conversations/registry.go` → `MaxSystemPromptBytes`, `SetSystemPrompt`: inclusive byte bound and distinguishable UTF-8 validation precedent.
- `internal/sessions/pool_system_prompt_test.go`, `pool_rotate_system_prompt_test.go`, `systemprompt_handoff_test.go`, `systemprompt_readfolder_test.go`, `systemprompt_client_test.go`: fake runners, argv/file evidence and existing contributor tests.
- `docs/knowledge/features/sessions-package.md`: owning package map.
- `docs/knowledge/features/sessions-package-key-types-writesystemprompt-systemprompttext.md` § Composition and resolution and The mint-window trap this design exists to avoid: construction alone misses ordinary operator edits; preserve stable argv paths and complete-file fallback.
- `docs/knowledge/features/development-verification.md`: tests must distinguish missing lifecycle refresh from mere argv presence.
- `CODING-STYLE.md`: mutex discipline and tempfile/sync/close/rename persistence.

## Context

Operators need one durable rule shared by every conversation, independent of workspace and conversation registry. This slice adds the sessions primitive; sibling slices own relay access and client UI. Bootstrap's separate prompt lifecycle remains unchanged. No decision record is needed.

Sizing: one deliverable, four acceptance criteria, approximately 650–750 written lines including tests and this plan, zero new exported types/interfaces, four existing composition consumers to update, no new state machine. The estimate is consistent with #2149's persistence/validation analogue. No overlapping remote feature branches were present after fetch.

## Design

Add `daemoninstructions.go` with the exact default constant and `Pool.DaemonInstructions() string`, `Pool.DefaultDaemonInstructions() string`, and `Pool.SetDaemonInstructions(text string) error`. The write door and load validate UTF-8 and the inclusive `conversations.MaxSystemPromptBytes` bound, with separate sessions error sentinels; preserve all accepted bytes, including empty text.

Store `<Pool.dataDir()>/daemon-instructions.json`, independently of registry and transient prompt directories. A required `instructions` byte field uses JSON's base64 representation of `[]byte`: decoding cannot replace invalid UTF-8 or lone surrogate escapes with replacement runes. Missing/null fields and malformed data are errors. Bound reads before decoding; reject nonregular stores. Seed and atomically persist the default only on absence, including upgrades with existing sessions registries. Persistence-disabled pools seed memory only. Constructor errors propagate; no fallback to defaults on an existing invalid/unreadable store.

Use same-directory 0600 tempfile, sync, close and rename through the existing `writeSystemPromptFile` primitive, with a 0700 daemon directory. Publish memory only after successful persistence; validation and failed writes leave the previous value intact. Removal, shutdown and transient-file purge never own this file.

Extend `composeSystemPromptForOn` to take instructions separately and insert them verbatim immediately after `daemonPromptText`, followed by clients, handoff note, and operator text last. Empty instructions contribute no separator. Keep `composeSystemPrompt` and `composeSystemPromptFor` compatible by passing empty instructions. Construction and shared next-start refresh read the pool value; do not cache on sessions. Preserve the bootstrap skips, active-session guard, stable prompt path, rotation's carried clients and previous-complete-file fallback.

## Concurrency model

Add a dedicated instructions RWMutex to Pool. Getters take its read lock; setters hold its write lock across persistence and memory publication. Construction can hold `p.mu`, so the instructions door never acquires `p.mu`, avoiding recursive locking. No new goroutines; existing runner shutdown is unchanged.

## Error handling

Invalid UTF-8 and oversized input return distinct sentinel errors with no input bytes. Store errors identify the operation without decoder details or payloads. File errors contain only fixed paths and OS errors. Startup refuses unreadable/malformed stores and failed initial persistence. Runtime failed persistence leaves memory and disk unchanged; prompt refresh keeps its existing complete-file fallback.

## Testing strategy

Write tests first: independent pin of the default, fresh/upgrade seed, custom and explicit-empty reconstruction, reset, disabled persistence, private permissions, malformed/unreadable/oversized/invalid-byte stores and initial write failure. Exercise byte boundaries, multibyte UTF-8, verbatim whitespace, validation rollback, persistence rollback, concurrent read/write and absence of secret text in errors/logs.

Assert the five-contributor order directly and inspect the prompt file named by fake-runner argv after edits between mint and activation. Prove active children/files remain unchanged; latest text reaches inactive reactivation, revival and rotation with no client resolve, and separate conversations keep their operator text last. Verify setting survival through removal, rotation, shutdown and constructor purge. Existing contributor fixtures explicitly clear instructions where they isolate older contracts. Run `go test -race ./internal/sessions/...`, `go vet ./...`, and `go build ./cmd/pyry`; the verifier owns the full-module gate.

## Open questions

None. Relay wire vocabulary and UI are outside this slice.

## Documentation handoff

Pending for the documentation stage: update `docs/knowledge/features/sessions-package-key-types-writesystemprompt-systemprompttext.md`, under “Composition and resolution” and “The mint-window trap this design exists to avoid”: document the five-contributor order, the independent durable setting and its lifecycle, seed-only-when-absent behavior, explicit empty remaining empty on restart, the inclusive 8192-byte UTF-8 bound, and the default returned for reset. State that edits apply at the next composition/start, including rotation, and leave active children and the bootstrap alone.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `SetDaemonInstructions` and the loader validate original bytes before publication; instruction prose is operator-authorized, not client identity or child-authored notes.
- [Tokens/secrets] No credentials generated. Text can be private; durable and composed files remain 0600 and no payload is logged.
- [Files] Fixed filename prevents text-driven traversal. Atomic rename replaces destination symlinks; loader rejects symlinks and other nonregular files before opening. The daemon data directory is private; same-UID replacement during load remains within the existing local trust boundary.
- [Subprocesses] Only file contents change; no shell interpolation, argv expansion or child lifecycle changes.
- [Cryptography] No cryptographic operations or key reuse introduced.
- [Network/I/O] No network entry point added. Stored reads are bounded before decoding, preventing unbounded allocations; reject nonregular stores so a FIFO cannot stall startup.
- [Errors/logs] Return generic parse errors rather than decoder errors that might echo payloads; fixed-path filesystem errors and validation sentinels contain no instruction text.
- [Concurrency] Dedicated lock serializes durable publication without acquiring the pool lock. Interrupted temp writes leave the previous complete setting; no goroutines introduced.
- [Threat model] Relay authorization and wire validation are deferred to the sibling relay slice of #2746; this primitive validates every future caller's input independently.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-04
