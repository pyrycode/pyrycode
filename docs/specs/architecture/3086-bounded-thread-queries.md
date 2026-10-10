# Bounded thread queries (#3086)

## Files read

- `internal/thread/store.go` → `run`, `publish`, `Snapshot`, `Unload`, `Shutdown`: immutable publications and worker identity/lifetime guards.
- `internal/thread/fold.go` → `Item`, `Items`: public membership, complete content and creation-ID order.
- `internal/thread/sends.go` → `sendFact`: acceptance IDs acquire delivery order; claimed delivery rows disappear.
- `internal/thread/observations.go` → `copyItems`, `difference`: detached ownership and existing change contracts.
- `internal/thread/observations_store.go` → `Changes`: nonusable states and consumer cancellation.
- `internal/thread/store_test.go`, `sends_test.go`, `child_test.go`: committed-history fixtures and gated readers.
- `internal/history/log.go` → `readSegment`, `MaxEntryID`: actual read counters and safe version-plus-one bound.
- `docs/knowledge/features/thread-package.md` § Cache and epochs and `thread-package-background-store.md` § Isolation and lifecycle: public rows cannot seed resumed folding; preparation/copying stays outside the global lock.
- `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: count work and force cancellation/retirement paths.
- `docs/knowledge/decisions/042-daemon-built-thread.md` § Update messages: continuous ranges, active extras and parent closure.
- `CODING-STYLE.md`; `docs/protocol-mobile.md` § Security model: storage errors stay independent of transport; authorization belongs to callers.

## Context

Long conversations need bounded backend reads without cloning a full baseline or scanning history. This implements one bounded selection contract with page and newest-window entry points. #3087 consumes the result contract for catch-up; #2963 owns handlers and capability activation. No new decision record is needed. No overlapping feature branch touched the proposed files at planning time.

## Design

Add `internal/thread/queries.go` and tests, with local integration in `store.go`.
Export `QueryResult` with `State SnapshotState`, `Items []Item`, `Epoch string`, `Version uint64`, `LowerOrder uint64`, `UpperOrder uint64`, `Continuation uint64`, `OlderExists bool`, and `Err error`.
`Store.HistoryPage(ctx, id, upperOrder, limit) (QueryResult, error)` selects below the exclusive upper bound. `Store.NewestWindow(ctx, id, limit) (QueryResult, error)` uses consumed `Version + 1` and adds all active public items. The caller must already be authorized for `id`.
Both reject nonpositive limits with `ErrInvalidLimit` and clamp to exported `MaxQueryItems = 256`. Validation checks context and canonical ID without I/O. Nonusable results carry only state and its existing generic error, with all bounds/continuation/epoch/version/items zero.

Build an immutable `queryIndex` from the successful publication's `Fold.Items`: permanent-ID lookup, ordered rows sorted by `(Order, ID)`, and active rows. Build outside `Store.mu`; install alongside the snapshot under its existing identity/lifecycle checks. No fold/cache format or folding-rule change.
Binary search locates the first ordered row at/above the upper bound; select at most the limit immediately below it. Include all rows tied at the selected lower order to preserve a continuous range. Lower and continuation equal that order; older-exists means an index row lies strictly below it. With no eligible ordered rows, lower/continuation are zero and older-exists false. Upper remains the requested bound on usable results, including empty ones.
Add active rows only for newest windows, then walk public parents through the lookup, stopping at already included IDs. Unordered rows occur only as active or closure extras. Deduplicate by permanent ID. Return rows in deterministic selection/closure traversal order (no ordering guarantee for consumers); clone full content per returned row. Extras neither consume the limit nor change bounds. Request work is O(log N + selected range + active rows + unique parents + returned content bytes), with no full-publication copy. Preparation is O(N log N) separately on publication/recovery.

## Concurrency model

No new goroutines. Existing per-conversation workers build indexes and publish them. Requests capture one index pointer and scalar metadata under the global lock, then seek/select/copy outside it, checking cancellation during work. Captured publications remain immutable and valid for in-flight readers; failure/retirement clears worker references. Consumer cancellation never cancels the worker or performs recovery. No I/O, joining or per-item work occurs under the global lock.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Recovery, clean reopen and unload/reload rebuild index and retain compatible epoch | `TestStoreQueriesBounded` |
| Queue acceptance, delivery placement and delivery-row suppression | `TestStoreQueriesUpdates` |
| Active work settles; unresolved children become public with ancestors | `TestStoreQueriesUpdates` |
| Rebuilding, failure, retry, unload retirement and shutdown withdraw index | `TestStoreQueriesLifecycle` |
| Request cancellation before/during selection leaves worker and other conversations progressing | `TestStoreQueriesLifecycle` |
| Shared/multi-level parents and repeated requests deduplicate without identity mutation | `TestQuerySelection` |

## Error handling

Context errors are returned directly, with no partial result. Invalid IDs return `history.ErrInvalidID`; invalid limits return `ErrInvalidLimit`. Loaded rebuilding/unavailable states follow Snapshot; unloaded, retiring and closed stores return not-loaded. Queries never start or wait for workers. Existing generic failure and recovery contracts remain intact.

## Testing strategy

Write tests first and observe the missing API failure. Table-driven selection tests cover ties, hidden/dropped rows, unordered closure, multi-level/shared parents, exact/clamped/invalid limits and empty/oldest bounds. Store tests compare complete returned fields with committed fold baselines across live updates, detached mutation and lifecycle transitions. A 36,000-entry real history fixture measures actual segment reads separately from preparation and counts actual index accesses per query; allocation limits reject full Snapshot/Observe clones. Repeat early/middle/newest pages and windows after clean reopen and unload/reload. Gated readers make request-only read measurements deterministic. Run `go test -race ./internal/thread ./internal/history`, `go vet ./...`, and `go build ./cmd/pyry` (output binary outside the worktree). The dispatcher owns the standard full hermetic gate.

## Open questions

None. Sizing rechecked: approximately 720 total written lines including this plan, one new exported type, zero existing consumer updates, four acceptance criteria and fewer than ten reject/state branches. Within all builder limits.

## Documentation handoff

Pending documentation stage: `docs/knowledge/features/thread-package-background-store.md`, new “Bounded queries” section: describe page/newest-window inputs, limit validation/clamping, detached result epoch/version, inclusive lower/exclusive upper bounds, empty ranges, next-page continuation and authoritative older-exists. State that unordered/closure extras do not alter bounds, newest windows include active public items, and recovery/publication preparation cost is separate from bounded request cost. Distinguish these reads from full baselines and live change ranges.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `HistoryPage`/`NewestWindow` retain caller authorization; validate canonical IDs and read only committed public `Fold.Items`. Private unresolved or claimed rows never enter the index.
- [Tokens/secrets/cryptography] No token/key operations or new randomness. Existing epochs remain random recovery identifiers; full saved content stays inert data for authorized callers.
- [File operations] Queries perform no file operations. Publication reuses existing contained checkpoint writes and permissions without changing paths or recovery formats.
- [Subprocesses] No subprocess execution or interpreted user data.
- [Network/I/O] Positive limits clamp to 256 ordered rows; active/parent extras intentionally remain complete. Context checks bound cancellation latency between seeks and row copies. Socket/wire size enforcement is OUT OF SCOPE, owned by #2963.
- [Errors/logs/telemetry] Fixed storage sentinels and state only; no new logs or source-error/content/path exposure.
- [Concurrency] Atomic snapshot/index installation under `Store.mu`, immutable references, identity/retirement checks, and outside-lock selection prevent torn metadata and writer starvation. Existing workers keep their cancellation/join paths.
- [Threat model] No capability or relay change. Authenticated connection authorization, wire framing and reply completion remain OUT OF SCOPE for #2963.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-10

## Revisions

- 2026-10-10: Find the first tied lower-order row with a second binary seek, preserving the range contract while bounding work before cancellable copying. Extend `TestStoreQueriesRetirementIsolation` to hold a consumer during publication and retirement; verify its captured epoch/version remains coherent while same-conversation writers and another worker progress. The 36,000-entry fixture now measures both read bytes and segment opens with active queued/nested extras after fresh history-store reopen.
- 2026-10-10, verifier finding 1: `Fold.resolveChildren` and `Fold.recordShown` repeatedly walk accumulated items during replay, with child visibility rebuilding after every fact when nested children already exist. Move the bounded-query fixture's nested active work and queued acceptance to orders 35001–35004, still outside the newest 20-item window; retain all 36,000 committed entries, distributed messages, read/visit/allocation assertions and all recovery phases. Log replay/publication duration separately from request measurements. No fold or query contract changes.
- 2026-10-10, verifier finding 2: Read `cmd/pyry/agent_switch_frames_test.go` → `gatedRelaySwitch`/`TestRelayAgentSwitchEncryptedFrames`, `conversation_agent_switch.go` → `Switch`, `internal/relay/v2session_switchagent.go` → `handleSwitchAgent`/`handleSwitchAgentDone`, and `docs/knowledge/features/conversation-session-binding.md` § Agent switching. Outcome frames precede `Switch` returning and releasing reset exclusion; a temporary Go overlay delaying that release by 100 ms reproduced the reported `missing switch frame`. Add adapter-completion synchronization before the test sends another request or changes configuration. Verify the delayed-return reproduction, named race regressions and a fresh standard hermetic gate; production behavior remains unchanged.
