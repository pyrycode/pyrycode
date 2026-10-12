# Bounded isolated permission observation

## Files read
- `internal/permbridge/permbridge.go`: `Register`, `expire`, `resolve`, `Pending.Await`; delete-under-lock is the verdict arbiter.
- `internal/permbridge/permbridge_test.go`: `TestRegistry_StaleExpiryCannotResolveReusedIDGeneration`, `TestRegistry_ExtendedAllowVsTimeoutRace`, `TestRegistry_NonPositiveWindowIsNeverExtended` establish identity and answerability policy.
- `docs/knowledge/features/permbridge-package.md`: conditional window, exact pending identity and log-free stdlib dependency direction must survive.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification.md`: behavioral assertions and counted evidence.

## Context
Provide registry evidence for #3225 and #3226 without claiming a Mobile cause or delivery evidence. One deliverable: bounded registry observation plus its submission contract. No overlapping feature branch changes permbridge. Estimate: 750 written lines, four exported diagnostic types, zero migrated consumer call sites, four acceptance criteria, no new approval rejection branches. No decision record required.

## Design
`permission_observe` is the supported diagnostic build tag. Ordinary builds compile inert private hooks; existing constructors and methods stay intact. Diagnostic builds expose `ObservationRecorder`, `ObservationIdentity`, `ObservationRecord`, `ObservationSnapshot`, `NewObservationRecorder`, `Registry.Observe`, and `Pending.ObservationIdentity`.

A recorder starts a fresh process-local run epoch; `Reset` advances the observer epoch without reusing aliases or generation numbers. `End` closes coverage. Attach before isolated activity. Attaching with pending requests marks incomplete coverage. Closed, reset, open or lossy snapshots cannot prove absence.

`Submit(stage, reason, identity)` admits a finite vocabulary for sibling hooks. `Identity(conversation, session, request, modal, connection)` returns kind-prefixed opaque aliases, stable within the recorder run, with explicit `unknown` for missing identity. Session is never inferred. Only bounded SHA-256 identity keys are privately retained, never raw identifiers or hashes in snapshots. Lifetime generations are minted at accepted registration and retained on the actual pending instance and public handle. Rejections carry no generation. Terminal reason comes from the expiry path or verdict behavior, never verdict message.

Limits: 1024 records, 512 retained alias keys of 33 bytes (16896 total key bytes), 1024 input bytes per identifier, counters/epochs/generation at 9007199254740991. Loss flags name record capacity, identity capacity/oversize/exhaustion, counter exhaustion, contention, reset truncation, incomplete coverage, closed submission and invalid vocabulary. Counters saturate; exhausted identities become unknown, never collide. Monotonic elapsed nanoseconds are relative to recorder creation, with no wall-clock timestamps. Snapshots copy slices and maps; serialization is caller-owned.

## Concurrency model
No observer goroutines, callbacks, channels, serialization or I/O. Registry hooks execute before releasing its existing mutex, with a single recorder `TryLock`; they never acquire a blocking observer lock. Snapshot/lifecycle operations may lock the recorder but never the registry. Contention uses a sticky atomic loss flag. Retained accepted registration therefore precedes its terminal. Lost registration suppresses terminal identity rather than fabricating a generation.

## State transitions and identity reuse
| Event | Race test |
|---|---|
| allow/deny/timeout winner, duplicate and empty rejection, immediate expiry | `TestObservationRegistry` |
| reused ID with stale expiry; repeated generations | `TestObservationReuseReset` |
| reset while pending, terminal retains original identity | `TestObservationReuseReset` |
| stalled recorder, saturation and capacity loss | `TestObservationBounds` |
| attach/end coverage, typed aliases, detached hostile snapshot | `TestObservationContract` |

## Error handling
Approval errors, verdict bytes and answerability remain unchanged. Diagnostic failures only mark incomplete evidence; a full recorder stops retaining records rather than overwriting registrations. The timer closure binds its actual pending instance so even a delayed callback starting after ID reuse cannot observe a replacement. No observer error includes caller content.

## Testing strategy
Write diagnostic tests first and observe failure before implementation. Run counted `go test -race -json -tags permission_observe ./internal/permbridge/...`, ordinary package race tests, `go vet ./...`, diagnostic vet and `go build ./cmd/pyry`. Preserve checked serialized content-free snapshot and named executed/passed/failed/skipped counts in PR. No live Claude run. Full-module suite belongs to verifier.

## Open questions
None; sibling relay hooks and daemon activation/file output are deliberately delegated to #3225/#3226.

## Documentation handoff
Pending documentation stage: add **Isolated permission observation** in `docs/knowledge/features/permbridge-package.md`, describing supported build/activation, lifetime/epoch identity, numerical limits/loss semantics and unchanged approval arbitration. State snapshots prove registry events only, not modal emission or receipt.

## Security review
**Verdict:** PASS
- Trust boundaries: `Submit` rejects unknown stage/reason vocabulary; `Identity` bounds each input before hashing and emits only kind-prefixed generated aliases.
- Tokens/secrets: aliases are diagnostic correlations, not authorization tokens; neither hashes nor raw inputs enter snapshots. Request/verdict content is excluded structurally.
- File operations/subprocesses/network/I/O: recorder performs none; daemon capture/output is OUT OF SCOPE, owned by #3226.
- Cryptography: stdlib SHA-256 only for private equality keys, with identity kind in the key; not a credential or authentication primitive.
- Errors/logs/telemetry: no logger or free-text record field. Invalid verdict behaviors use a fixed unknown reason.
- Concurrency: one nonblocking recorder lock attempt on approval boundaries; snapshots cannot hold the registry lock. MUST FIX addressed in design: terminal identity belongs to pending, never a later ID lookup.
- Threat model: bounded adversarial identities and full/stalled recorders cannot delay approval or alter authorization. Mobile receipt/authentication is OUT OF SCOPE (#3225/#3226).
**Reviewer:** builder self-review. **Date:** 2026-10-12.

## Revisions
- 2026-10-12: count both 33-byte private equality keys and 8-byte alias numbers in retained identity payload: 41 bytes each, 20992 bytes total, excluding fixed Go map overhead. The alias strings are generated for records/accessors; none is retained in the alias table. This tightens the byte accounting without changing the identity contract.
