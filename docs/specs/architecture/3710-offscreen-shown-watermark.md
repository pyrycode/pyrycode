# Offscreen shown-watermark recovery

## Files read
- `cmd/pyry/daemon_thread_relay.go` → `installDaemonThreadRelay`, `startDaemonThreadPublisher`, `publishThread`: authoritative provider and relay publication ownership.
- `cmd/pyry/thread_shadow.go` → `follow`, `shadowQuiescent`: quiet retirement and raw-progress reactivation.
- `cmd/pyry/daemon_thread_demand.go` → `queryDaemonThread`: demand release after bounded capture/rendering.
- `internal/thread/store.go` → `Status`, `Unload`, `run`: retiring publications are withdrawn before joined persistence.
- `internal/thread/cache.go` → `UnloadedLastShownVersion`: no worker, known H and clean exact V = H evidence required.
- `internal/relay/v2session_thread.go` → `threadSummary`: projection removes unavailable metadata, including stale supplied fields.
- `cmd/pyry/conversation.go` → `conversationCreator`: fresh daemon conversation creation and stored summary fields.
- `cmd/pyry/interactive_turn_v2.go` → `HandleFor`, `emit`: actual assistant/completion/history producers.
- `cmd/pyry/daemon_thread_queries_test.go`, `daemon_live_relay_test.go`, `thread_shadow_test.go`: authenticated fixture, completion marker, joined cleanup and retirement barriers.
- `cmd/pyry/daemon_thread_idle_append_test.go` → `TestDaemonThreadIdleAppendKeepsConnections`: preserve exact item-update counts while allowing summary refreshes beside them.
- `cmd/pyry/daemon_thread_relay_test.go` → `TestDaemonThreadIdleCommit`: its held item-publication counter must not decode summary notifications as item additions.
- `docs/knowledge/features/thread-package.md`, `thread-package-cache-and-epochs.md` § Scalar certificates and unloaded metadata: unknown H and retiring workers must remain absent.
- `docs/knowledge/features/relay-package.md`: sealed per-connection projection and content-free diagnostics.
- `docs/knowledge/features/development-verification.md`: producer proof and controlled failing-before checks.
- `docs/protocol-mobile.md` § conversations, conversation_updated and Security model: shown watermark authority and existing authenticated audience.

## Context
An offscreen thread phone can receive completed items but read a conversation list during retirement, when neither loaded nor unloaded metadata is eligible. The producer does not announce restored eligibility after retirement, leaving that row stranded. The private failed trace establishes absent metadata; the controlled regression will establish this concrete producer interleaving without claiming its internal timing is known from the trace. No new decision record is needed.

One behavior slice: recover the authoritative offscreen summary after completion. Its regression and producer plumbing cannot ship independently. Forecast: 70 production, 390 test/helper and 90 plan lines, no exported types, no changed consumer signatures, four acceptance criteria and fewer than ten reject branches. No overlapping remote feature branch touches the planned production files at initial fetch.

## Design
After a successful quiet `Unload`, publish an ordinary `conversation_updated` refresh to eligible authenticated interactive thread peers. Read every summary field from the current registry row and workspace label. Reuse the thread publisher's context, peer selection, push and envelope counter. Keep the refresh local to this conversation and thread recipients; non-thread publication is unchanged.

The refresh supplies no invented watermark. `V2SessionManager.threadSummary` still invokes the installed provider at delivery. A retiring, stale, rebuilding, unavailable or removed conversation remains absent. If an append races retirement, the existing follower reloads the suffix and a later successful retirement refresh restores eligible metadata. Loaded reads remain memory-only; unloaded reads keep their current single-certificate and exact V = H checks.

## Concurrency model
The existing per-conversation follower initiates refresh after joined retirement, outside `quietMu` and the store lock. The helper snapshots the publisher under `threadShadow.mu`, registers with its wait group under publisher `mu`, then releases both before registry/relay work. Stop cancels and joins this work using the existing publisher lifecycle. No new goroutine or retained watermark is introduced.

## State transitions and identity reuse
| Event | Race-detector coverage |
| --- | --- |
| Fresh A/B creation, peer completion, explicit catch-up completion and demand release | `TestDaemonThreadOffscreenShownRecovery` |
| Quiet retirement while a later hidden fact is pending; temporary absence | `TestDaemonThreadOffscreenShownRecovery` |
| Joined retirement with stale V/H, reload under the same conversation ID, second retirement and restored summary | `TestDaemonThreadOffscreenShownRecovery` |
| Removed registry membership, authoritative zero, loaded/rebuilding/unavailable and invalid/cold certificate readings | existing `TestDaemonThreadLastShown*`, `TestUnloadedLastShownVersionGuards` and relay projection tests |
| Publisher cancellation during refresh, stopped publisher, removal and one failed peer | `TestDaemonThreadSummaryRefreshLifecycle` |

## Error handling
Retirement failure retains its existing diagnostic and emits no success refresh. Missing registry rows emit nothing. Marshal/push failures get content-free diagnostics and preserve live-only delivery behavior; reconnect listing remains the recovery path. No client read mark is changed.

## Testing strategy
Write the producer regression first and observe its missing-refresh failure. Create fresh A/B through the daemon constructor; use an authenticated diagnostic non-thread sender and negotiated thread phone, real queue delivery/history and assistant/completion producers. Complete A catch-up with its separate marker. Coordinate quiet retirement and a hidden tail-fold barrier so an immediate authenticated list omits unusable metadata. Release retirement and require a producer refresh with A shown > read, B not unread, unchanged read marks and unchanged other summary fields, without another catch-up/read/open. Record content-free readiness, demand, retirement and raw/certificate eligibility diagnostics. Also run existing daemon/store/relay watermark guards under managed Linux race admission, `go vet ./...` and `go build ./cmd/pyry`. Full suites and live acceptance belong to later gates.

## Open questions
None. Test evidence will distinguish this controlled producer cause from historical trace observations.

## Documentation handoff
Pending documentation stage:
- `docs/knowledge/features/thread-package-shadow-lifecycle-and-evidence.md`, **Shadow lifecycle and evidence**: document the demonstrated cause and summary recovery.
- `docs/knowledge/features/thread-package-cache-and-epochs.md`, **Scalar certificates and unloaded metadata**: describe the repaired loaded/retiring/unloaded authority boundary, preserving exact V = H eligibility, rejection rules and authoritative zero.
- Keep Mobile capture pending: Mobile #2246 must pass unchanged `InteractiveStreamE2ETest.interactiveTurn_attentionDot_followsARealTurn` against the repaired daemon through managed Linux/Android capture with counted results, revisions, APK/binary hashes and negotiated route/host/conversation/connection ownership receipts. Dispatcher owns complete suites and normal live gate; Core producer proof cannot replace Mobile capture.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: refresh uses registry membership and stored fields, then existing `ActiveThreadConns`/Push membership and agent gates and `threadSummary` authority checks. No client-supplied watermark is accepted.
- Tokens and cryptography: no credential/key generation, storage or lifecycle change; summary remains inside existing Noise-sealed application delivery.
- File operations: no added persistence or metadata read path; `UnloadedLastShownVersion` retains contained no-follow certificate reads and independently known H. Retiring workers and either V/H mismatch remain rejected.
- Subprocesses: no new subprocess invocation; tests substitute the child boundary while exercising real producers.
- Network/I/O: existing bounded push queues and authenticated peer selection remain; refresh is one existing summary envelope per eligible peer per successful retirement, with no new socket/read surface.
- Errors/logs: SHOULD FIX during build: diagnostics must identify only static failure reasons, never summary payloads, names, paths, text or arbitrary marshal/push error strings.
- Concurrency: preserve shadow-mutex then publisher-mutex order and register before stop can join; release locks before relay operations, honor both follower and publisher cancellation.
- Threat model: protocol threats 1–4 retain existing prompt, authentication and encryption boundaries; this change adds no verbs or audience. Existing relay DoS/metadata visibility and endpoint-compromise posture are unchanged.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-21

## Revisions
- 2026-10-21: Keep the refresh helper in `cmd/pyry/daemon_thread_summary.go` beside the existing publisher; `follow` calls it after releasing the retirement gate. The controlled regression demonstrates a retiring worker, then joined stale V/H evidence, then automatic suffix reload and clean retirement. The new notification leaves provider/projection guards untouched. Strengthen stored-field checks with a non-default mute, pin and workspace label, and compare serialized times with `Time.Equal`.
- Failing-before source: regression commit `21676cce` on plan `fe0787a0`, with no producer repair. Managed Linux race result: 1 executed, 0 passed, 1 failed, 0 skipped; missing `conversation_updated` after real recovery. Diagnostics establish registered=true, no query demand, retirement=true, known raw progress and rejected certificate, followed by joined consumed progress below H. Initial test-vet attempt executed zero tests due to two incorrect log format verbs; fixed before the behavioral baseline.
- 2026-10-21: `TestDaemonThreadDemandIdleReadKeepsConnection` exposed the query fixture's assumption that every received frame is correlated. Teach `testDaemonThreadReply` to tolerate only unsolicited `conversation_updated` beside queries, retaining envelope bounds and every correlated reply/completion assertion. The producer regression checks the notification itself. Add `TestDaemonThreadSummaryRefreshLifecycle` for stopped/removed/cancelled refreshes, peer filtering, fan-out after a failed push and content-free diagnostics.
- `TestDaemonThreadIdleAppendKeepsConnections` similarly counts thread item updates separately from unsolicited retirement summary metadata; its exact content/update-count and original-connection assertions remain intact.
- `TestDaemonThreadIdleCommit` keeps its held item-publication assertion separate from summary notifications, which carry no item version. These are three fixture compatibility findings from the initial affected daemon race run, not relaxed producer or watermark assertions.
- Retained Linux evidence: initial affected daemon selection `^(TestDaemonThread|TestThreadShadow|TestUnreadThreadHiddenTails)` produced 166 run events, 162 passed, 3 failed and 1 skipped. The failures were the three fixture assumptions named above. `TestDaemonThreadMetadataReadiness` skips its opt-in full delayed replay proof (`make test-slow`); its stalled-readiness sibling passed. This historical attempt remains failed.
- On production revision `b43656ba0a00bcd149f75ac5469bd5ab4a6ad054`, the explicit named rerun passed 69/69 Go test events (22 top-level tests), zero failed/skipped. Selected names: `TestDaemonThreadDemand{IdleReadKeepsConnection,Recovery,WireAfterIdle,Cancellation,ProtectsRendering,Teardown,CancelledDuringRetirement,Refusals}`, `TestDaemonThreadQueries{Readiness,Recovery,Reset,Bounded,Join}`, `TestDaemonThreadLastShown{IO,Certificates,LoadedZero,Lifecycle,Unavailable}`, `TestDaemonThreadIdleAppendKeepsConnections`, `TestDaemonThreadOffscreenShownRecovery`, `TestDaemonThreadSummaryRefreshLifecycle`, and `TestUnreadThreadHiddenTails` (braces denote the individually named tests). `go vet ./...` and daemon build passed. The only subsequent change is the third fixture compatibility guard and this evidence record; rerun that fixture with the producer/lifecycle proof before handoff.
