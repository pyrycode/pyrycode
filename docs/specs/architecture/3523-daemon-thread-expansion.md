# Install bounded daemon thread expansion

## Files read
- `cmd/pyry/daemon_thread_relay.go`: `installDaemonThreadRelay`, `publishThread` and `startDaemonThreadPublisher` own real Store installation and publication readiness.
- `cmd/pyry/daemon_thread_demand.go`: `queryThread` owns cancelable loading, demand counts and publication-boundary acquisition.
- `cmd/pyry/daemon_thread_queries_test.go` and `daemon_thread_demand_test.go`: authenticated wire harness and demand/retirement patterns.
- `cmd/pyry/thread_shadow.go`: `follow`, `remove` and `shutdown` serialize worker retirement and join readers.
- `internal/thread/tool_body.go` and `child_pages.go`: captured body kind, strict scope/cursor refusal, direct-child selection and detached results.
- `internal/thread/tool_body_test.go` and `child_pages_test.go`: existing selected-row/reference-lookup work-counter proofs, reused rather than duplicated.
- `internal/relay/v2session_children.go`, `v2session_tool_body.go`, `v2session_handshake.go`: authenticated expansion admission, encoding and additive capability gating.
- `internal/protocol/thread_expansion.go`: cursor mapping and expansion-only completion contract.
- `docs/knowledge/features/thread-package-shadow-lifecycle-and-evidence.md`: preserve metadata-only initial publication and existing idle checkpoint boundaries.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification.md`: bounded source checks supplement behavioral measurements; no new dependencies.

## Context
The bounded Store and authenticated relay implementations are merged but absent from production composition. Install both on the publisher's Store while retaining complete history/live delivery and merged recovery behavior. Compact publication (#3517), client adoption and new recovery behavior are outside this change. No decision record is needed.

## Design
Install `ThreadToolBody` and `ThreadChildren` alongside existing queries, clearing both on every installation attempt. Forward the exact conversation, context, epoch, revision and normalized child limit; translate the optional wire cursor field-for-field into `thread.ChildCursor`. Return each Store capture unchanged, including body kind.

Factor the current query-demand implementation into an unexported generic function returning the callback's result type; retain `queryThread` as the complete-query wrapper. All four reads use identical acquisition, publication-boundary, cancellation and release behavior. Bind admitted requests to the current publisher context when installed. Existing relay negotiation already requires both expansion callbacks plus ThreadReady and client capabilities; do not make complete-thread readiness depend on expansion callbacks.

No concurrent feature branch touched the planned production files at planning time. Sizing: approximately 730 written lines including this plan, 0 exported types/interfaces, 2 new consumers plus the retained complete wrapper, 4 acceptance criteria, no new reject state machine (one publisher cancellation binding).

## Concurrency model
Preserve quietMu → shadow mutex acquisition and existing Store worker ownership. Requests have independent contexts/counts and share only the worker and conversation coordinator. Publisher cancellation uses context.AfterFunc with cleanup; no additional long-lived goroutine or lifecycle owner. Never hold either shadow mutex while waiting for publication or rendering.

## State transitions and identity reuse
| Event | Race-enabled proof |
| --- | --- |
| Cold/idle reload with stable identity | `TestDaemonThreadExpansionDemand` and existing `TestDaemonThreadDemandRecovery` |
| Concurrent readers; cancel one | `TestDaemonThreadExpansionDemand` |
| Removal, shutdown or publisher stop during replay | `TestDaemonThreadExpansionTeardown` |
| Repeated pages, nested parent and byte-shortened continuation | `TestDaemonThreadExpansionWire` |
| History advance and epoch replacement | `TestDaemonThreadExpansionRefusals` |
| Independent client/conversation and complete client | `TestDaemonThreadExpansionWire` |

## Error handling
Preserve Store errors and cancellation unchanged for the relay's content-free correlated `thread.query_failed`. Missing/private items, stale epoch/revision/cursor and unavailable history never become partial success. Registry membership and existing tombstones prevent removed IDs reopening. Expansion completion remains relay-owned and cannot establish complete-thread joins.

## Testing strategy
Write installation/demand tests first and observe failure before wiring providers. Use synthetic durable history and the authenticated daemon harness for exact ordinary/agent content, assembled escaped bodies, direct ownership, paging and stale refusals. Measure fully stamped envelope bytes after preparation with increasing unopened descendants/body sizes, enforcing row and aggregate byte caps. Reuse Store work-counter tests and guard production adapters against Snapshot/Observe or duplicate selection. Run focused race tests with executed/passed/failed/skipped counts, touched-package race tests, go vet and go build on macOS. The dispatcher owns the normal full hermetic gate; live Claude and Linux checks are outside this run's scope.

## Open questions
None.

## Documentation handoff
- Pending documentation stage: `docs/protocol-mobile.md`, “Thread query messages” / “Bounded expansion reads” and the four expansion rows in “Application message types”: replace pending-installation wording with production readiness/authorization, cold/idle demand, cancellation, stale refusal and expansion-only completion. Preserve complete-thread coverage and identify compact delivery/client adoption as later work.
- Pending documentation stage: `docs/knowledge/features/thread-package-shadow-lifecycle-and-evidence.md`, “Shadow lifecycle and evidence” (linked from `thread-package.md`): describe installation on the real Store, shared demand ownership and installed authenticated bounded-read evidence.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: relay admission authenticates/authorizes the named conversation; Store APIs independently check public membership and captured scope. Cursor translation conveys no authorization.
- Tokens/secrets and cryptography: no credentials, key operations or crypto changes; existing Noise authentication/encryption remains authoritative.
- File operations: only existing Store/history reads through validated conversation IDs; no new paths or writes.
- Subprocesses: none added.
- Network/I/O: existing protocol envelope and row caps apply; body assembly alone can add bounded frames. Test encoded delivered bytes, including completion.
- Errors/logs: preserve generic relay refusals; no content or paths added to diagnostics.
- Concurrency: SHOULD FIX during build: the existing demand helper only follows caller/shadow cancellation during rendering; bind it to publisher retirement too, with a held replay test. Preserve independent shared-reader ownership.
- Threat model: reuse authenticated interactive/thread/expansion capability gates and known-conversation checks from the protocol security model. Compact client adoption remains #3517's follow-on work, outside this composition change.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-17

## Revisions
- 2026-10-17: publisher retirement now admits and joins query callbacks through the existing publisher wait group as well as canceling their contexts. This ensures stop returns only after demand release; `TestDaemonThreadExpansionTeardown` covers it. Cold/idle recovery and unavailable history have their dedicated `TestDaemonThreadExpansionIdleAndUnavailable` proof. Final sizing is approximately 710 added lines, with no new exported types or interfaces.
