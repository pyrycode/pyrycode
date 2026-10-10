# Native reply suggestions after the result (#2831)

`HandleFor` attributes `turnevent.PromptSuggestion` to the producing session's
conversation, just like other drained events, even when another conversation
is active. Its arm opens no turn and emits no `turn_state`. Eligibility lives
in `replySuggestions`, outside `convTurnState`: the emitter usually releases
that state before the post-result suggestion arrives. A set requires
`TurnEndReasonEndTurn`, `Outcome == "success"`, no error, nonblank delivered
user text and nonblank final main-agent `TextChunk` text
(`ParentToolCallID == ""`), with no invalidation. Delivered user text must also
be confirmed. Accepted native text is carried verbatim and never logged.

**Retention precedes delayed publication.** With the daemon live attachment,
`publishLocked` offers the current set or explicit-null suggestion to
`daemonLiveState` before marking the conversation dirty or waking the publisher.
`takeDirty` and recipient enumeration may coalesce or delay legacy delivery;
they cannot delay retention, and failed delivery does not undo it. Native payload
session fields and retained metadata use the stream's capture.
`startFallbackLocked` fixes conversation, source and generation before launching
inference and carries them through completion. Reading the current binding at
publication would misattribute a predecessor result after rotation; the captured
daemon generation rejects it even when the session ID is reused. See
[control and on-demand readings](streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md#control-and-on-demand-readings)
for shared ordering and the pending #3077 relay integration.

**Final-exchange selection and bounded retention (#2832).** `noteAssistantText`
assembles chunks of the final main-agent message by `MessageID`; a new message
resets both retained text and nonblank eligibility. Earlier assistant messages
and subagent chunks cannot supply missing final prose. `noteDelivered` retains
only the producing message's client-safe `QueuedMessage.Text`, never composed
stdin, full history or attachment-file contents. `replyExchangePrefix` retains
and sends at most the first 8192 UTF-8 bytes per side, backing up to a complete
code point. Invalid UTF-8 has no retained prefix. A clipped prefix is cloned:
a short Go substring otherwise keeps the original exchange's entire backing
allocation alive despite passing length assertions.

**Full-text eligibility is separate from retained prefixes.** Nonblank prose
after 8192 whitespace bytes still qualifies either side, even when it arrives
in later chunks after retention fills. Keep only the eligibility boolean beyond
the prefix; inference still receives the bounded prefix. Checking `TrimSpace`
on retained text alone would suppress even valid native output. Conversely,
an earlier nonblank message cannot make a blank final message eligible.
`TestReplySuggestionEligibilityBeyondPrefix` covers native and fallback sources,
later chunks, blank final messages and subagent-only prose.

**Delivery order cannot identify the producing message.** `OnDelivered` can
arrive after both `TurnEnd` and the native suggestion. `trackDelivery` wraps
the existing `turncommit` gate; after the gate accepts, `beginWrite` records
`DeliveryMessage`'s queued message ID before stdin is written. `noteDelivered`
credits only that ID's safe `QueuedMessage.Text`, excluding composed attachment
host paths. A valid suggestion missing only this confirmation waits in
`pending`. The matching late confirmation can release it, but never resets
invalidation. A spontaneous turn has no producing message ID and cannot borrow
text from the next CLI or channel delivery.

`waitReplyNative` gives native output two seconds from `TurnEnd`. Publication
within that window makes zero fallback calls. Once the window expires,
`startFallbackLocked` may launch one asynchronous attempt only if the producing
message has confirmed delivery and the turn remains eligible with no pending
native text. Matching confirmation after expiry can enable that attempt
immediately; it does not restart the window, credit another message or reset
invalidation. Eligible native output arriving while fallback is pending wins
unchanged and cancels inference. Whichever source publishes first closes the
turn to further suggestions. Turn completion and ordinary delivery never wait
for the timer or model call, and a failed attempt is not retried.

The queue may begin writing before `EnqueueSent` returns to its adapter.
`accepted` retains the greatest accepted ID even for an unseen conversation;
`beginWrite` preserves invalidation by any newer accepted ID. A delayed accept
for the producing ID does not invalidate its own turn. Arrival-order credit,
resetting invalidation on confirmation, and counting outstanding accepts all
miss this distinction. Tests must assert after the following confirmation:
`TestReplySuggestions_OvertakenTurn` covers both accept-before-first-content
interleavings, `TestReplySuggestions_UnrelatedDelivery` rejects borrowed text,
and `TestReplySuggestions_DeliveryGate` exercises success, failure and dropped
gates through the real queue context. A failed write invalidates only its
matching identity; a refused commit gate registers no identity.

New turn activity and a new queued write cancel the native wait or inference,
drop prior pending text and clear a held suggestion. Accepted queued sends,
accepted send-now, successful `/clear`,
reset/eviction transitions and `closeForConversation` on exit or teardown also
invalidate the producing turn. Refused sends or resets preserve state. A clear
advances the conversation's revision only when published text was held; other
conversations remain untouched. The null stays in `current()` for reconnects,
attributed to the producing session. Explicit deletion and idle sweeping cancel
and forget published and unpublished entries through
`dropRingOnConversationDelete`, composing `replySuggestions.forget` with
`Ring.Drop` in the registry's single removal observer. Reconnect pruning alone
would leave deleted conversations' waits and calls running. Tests must use
the production callback via `Registry.Delete` and `Sweep`, then release a late
result; calling `forget` directly misses absent wiring.
`TestReplyFallbackRegistryRemovalCancellation` exercises both phases and
preserves another conversation's ring and suggestion. See
[the removal observer contract](conversations-registry-crud.md#setondeletefn-funcid-conversationid-1502).

**One isolated Haiku attempt.** `replyFallback.run` uses the daemon's configured
Claude binary and `claudeAccount.provider()` with the `haiku` alias, no
more-expensive model fallback and one fresh print-mode turn with
`--output-format stream-json --verbose`. Fixed system
instructions ask for a short next reply; the two bounded exchange sides are
JSON-encoded stdin, never argv or shell text. The private temporary cwd,
empty setting sources, safe mode and explicit discovery exclusions disable
tools, skills, MCP, hooks, approval dialogs, session persistence, session
prompts, CLAUDE.md, auto-memory and attachment expansion. No workspace or
history is read. Bare mode is unsuitable here because it also skips installed
subscription OAuth/keychain login; isolate context while preserving existing
authentication rather than requiring an API key.

A configured provider is re-read inside the deadline and replaces ambient
OAuth/API credentials; refusal prevents the launch rather than selecting a
different account. `replyFallbackBudget` bounds the attempt at 30 seconds,
including credential lookup, startup, inference, completion and termination.
`replyFallbackDeadline` starts a 29.8-second context before lookup, reserving
200 ms for the existing 100 ms cancellation Wait grace and pipe cleanup. Group
cancellation sends `SIGKILL`; `cmd.WaitDelay` is 100 ms, so escaped descendants
cannot hold the caller open. Earlier parent cancellation or a parent deadline
remains authoritative. Lookup is selected against cancellation even if a reader
ignores its context. Lifecycle `own_deadline_elapsed` uses the same attempt
deadline. The shared `streamrunner.Run` would add a
five-second termination grace and treat parent cancellation as nil, so this
private helper owns the stricter bound. Missing binary/model/authentication,
unsupported isolation flags, child failure, cancellation or timeout produces
no publication and no retry. See [account source cancellation](claude-account-source.md#a-subprocess-bound-has-to-be-enforced-by-returning-on-ctxdone-not-by-waiting-on-the-child).

`replyFallbackStream` incrementally retains at most 4096 bytes per envelope,
discarding oversized frames through newline. Only complete UTF-8 JSON
`type=result` envelopes passing `decodeReplyFallback` supply a reply; malformed,
unknown or oversized frames cannot consume a later result's allowance.
Whole-stream receipt saturates at 4097 independently of the 1–4096-byte
`result_bytes` bound: treating saturation as oversized result output would
reject valid results after ordinary progress. Oversized results are never
truncated. A complete final envelope without newline is examined only after
Wait receipt. Successful result text is trimmed, then `validReplyFallback` requires
nonblank single-line valid UTF-8, no remaining control characters or U+2028/
U+2029 separators, at most 240 Unicode code points and 1024 UTF-8 bytes.
Oversized or otherwise invalid output is rejected rather than truncated.
Result receipt alone is insufficient: `replyFallback.run` requires successful
Wait receipt and an unexpired context before returning validated text. A killed
or unsuccessfully completed process cannot publish a reply, even if its result
was valid before cancellation. Exchange text, generated text, raw stdout and raw
errors are never logged;
stderr has the destination-specific exception below. `TestReplyFallbackProcess` and
`TestReplyFallbackProcessCancellation` exercise isolation, fresh account reads,
refusal, output validation and process termination; `TestReplyFallbackLifecycle`
covers final-message selection, native priority, late delivery and stale results.

A fixture that checks only valid result receipt misses completion crossing the
deadline. `TestReplyFallbackTimingBudget` separates these timings: one child
emits its result immediately and delays successful exit for 11 seconds; another
first emits its result after 11 seconds. Both failed at about 9.81 seconds under
the old deadline and return valid replies under the correction.
`TestReplyFallbackLookupBudget` proves lookup consumes the same budget without
launching a child. Increasing only the phone wait cannot repair a production
deadline that kills the helper before successful completion. See
[the correction and regression evidence](../../specs/architecture/2923-fallback-timing.md#offline-results).

The daemon reader recognizes decoded `system/init`, `system/api_retry` and
`result` progress. One writer mutex serializes recognition with the first
`cmd.Cancel` snapshot before its signal, through both the context watcher and
explicit cancellation. Last event, age, init/source and retry count are frozen;
later input and repeated cancellation affect neither contents nor applicability.
Current observations may advance separately. No recognized event means `none`,
with unknown age; absent/invalid evidence remains unknown. Retries count decoded
retry events only, including observed zero, never inferred from
`attempt`/`max_retries`. Reported source is exactly `none`,
`ANTHROPIC_API_KEY` or `unknown`; missing, invalid or other categories become
unknown. Init/source prove neither authentication nor readiness.

`replyFallbackOutput` requires `cmd.Wait` receipt before process-state and
completion-dependent output/exit/Wait predicates, even after group cancellation.
The caller may return before reaping; independently synchronized progress may
remain known while completion stays unknown. Retain the actual cancellation-arm
Wait error; a nil placeholder without receipt cannot imply success.
`decodeReplyFallback` shares production null/duplicate-field semantics with
diagnostics. Wrapper and daemon readers are independent: spawn/read/acknowledged
forward prefixes do not establish daemon receipt or child completion. Consumers
require typed, applicable, consistent metadata; contradictory event/init/retry
observations and their age become unknown while independent fields stay known.
A decoded result without wire publication proves neither forwarding loss nor
publication eligibility; see
[the snapshot and consumer contract](e2e-realclaude-test-infrastructure.md#test-infrastructure).

Child readiness cannot guarantee Wait receipt within the cancellation grace
under scheduling delay. `TestReplyFallbackCancellationEvidence` waits for a
private readiness pipe after fixture input/environment validation, then forces
received and withheld Wait outcomes for both parent cancellation and parent
deadline. In received cases, the private `waitGrace` seam releases held Wait on
grace entry and keeps timeout unavailable until receipt; withheld cases retain
the real 100 ms timer and hold Wait beyond bounded return. Production leaves
the seam nil, preserving the 29.8-second context and 100 ms grace. Each started
attempt must emit exactly one `reply_fallback.lifecycle` snapshot before deferred
context cleanup. Received completion records the observed exit; withheld
completion requires `wait_completed=false`, `exit_observed=false` and
`output_observed=false`, with completion-dependent fields, including
`result_bytes`, unknown. Both return an empty reply with `errReplyFallback` and
request group termination. See [custom deadline-context propagation](development-verification.md#prove-that-tests-distinguish-the-change).

`TestReplyFallbackProcessEvidence` retains success, nonzero exit, privacy and
actual timer-driven deadlines. Its own-deadline case holds Wait through the
29.8-second deadline and return before 30 seconds, proving the default grace
without assuming prompt reaping. Return-time evidence stays immutable: release held
work and separately join eventual completion before inspecting `ProcessState`
or asserting child termination. Cleanup cancels, releases gates, closes
readiness descriptors and joins run, Wait and readiness workers even after a
fatal assertion.

Cleanup must establish successful Start independently of Wait-worker scheduling.
After joining the run worker, `testStartReplyFallback` checks the command's nonnil
`Process`, then joins both Wait start and completion. A nonblocking worker-start
check can mistake an unscheduled worker for failed Start and leak it on failure.
`TestReplyFallbackAttemptCleanupDelayedWait` withholds that start signal through
bounded return and releases it only once cleanup begins the join;
`TestReplyFallbackAttemptCleanupFailedStart` preserves the no-worker/no-lifecycle
path for actual Start failure.

Every started helper logs its available stderr tail on success, failed exit or
bounded cancellation return. `replyFallbackStderrTail` keeps the last 1024
bytes, trims trailing CR/LF, then keeps the last five newline-delimited lines.
These raw bytes may contain credentials or other sensitive values. Only the
primary local daemon handler receives them: `replyFallbackDaemonOnly` follows
`daemonLogOnly.MarshalText` so the text handler quotes/escapes the attribute as
needed, and `LogDaemonOnly` makes `control.SlogTee` substitute the fixed
`(daemon log only)` in the ring. No tail, excerpt, hash or text-derived category
belongs in `pyry logs`, phone/debug bundles, reports, specs or knowledge docs.
See [the log destination contract](control-plane.md#keeping-a-value-out-of-the-log-ring-logdaemononly-2723).

Pipe setup failure preserves launch/return behavior with `stderr_observed=false`
and unknown reader predicates; no content or raw setup error is logged. Available
empty capture differs from unavailable capture and from a nonempty local tail.
`stderr_reader_done` means the reader was joined, not that it reached EOF:
`stderr_eof` is independently observed, and forced close, read failure or
unfinished drainage produces `stderr_partial=true`, even with an empty tail.
Wait receipt alone cannot establish EOF; EOF cannot establish child completion
or a complete error report. Unobserved Wait leaves exit/completion unknown.

An exec-owned stderr copier would let a descendant holding the pipe delay Wait
and alter classification. `captureReplyFallbackStderr` instead gives the child
a private file pipe. After normal Wait, `finish` allows at most 100 ms of drainage
within the attempt context; cancellation adds no drain grace after its 100-ms
Wait grace, especially not streamsup's 250 ms. Cleanup closes and joins every
started reader, including when Wait remains unobserved; failed Start closes both
parent descriptors. This joined return-time stderr snapshot is distinct from
the immutable stdout progress frozen before the first cancellation signal.

The [#3024 counted batch](../../specs/architecture/3024-fallback-stream-observation.md#six-run-results)
retains E/P/F/S **6/2/4/0**, with runs **1, 2, 4 and 6 FAIL**. Run 1 froze a
valid result before deadline cancellation/unsuccessful Wait without publication;
runs 2/4/6 froze init-only progress and observed zero retries without a decoded
result. The [operator disposition](https://github.com/pyrycode/pyrycode/issues/3024#issuecomment-6077408952)
accepts this as observation evidence for [#2923](https://github.com/pyrycode/pyrycode/issues/2923).
Earlier #2882 **6/4/2/0** (runs 2/5 FAIL), #2873 **6/5/1/0** (run 4 FAIL), and
PR #2896 **6/3/3/0** (runs 1/3/6 FAIL) remain failed historical observations;
see the
[retained historical spec](https://github.com/pyrycode/pyrycode/blob/25d778fbd0e54753bd82a6d4eca9a097c996f6f4/docs/specs/architecture/2882-fallback-stdout-evidence.md).
The correction is established by deterministic regressions, separately from
[the #2923 declared live batch](../../specs/architecture/2923-fallback-timing.md#six-run-results):
**6/6/0/0** on unchanged commit `1918d756`, without retries or replacements.
All six observed successful Wait and wire set/explicit-null clear revisions
1/2; runs 1 and 5 received valid results after the old cutoff. This is counted
live proof/non-reproduction under the corrected budget. Observation alone does
not fix a deadline, and these passes do not change historical failures or prove
how long the previously killed init-only calls would have taken.

The owner has one leaf mutex, released before registry reads, queue gates,
writes, credential lookup, inference or pushes. Reset cancels pending work and
advances a generation; fallback publication rechecks conversation identity,
generation, eligibility and current session attribution. A late result cannot
restore a clear, recreate deleted state, affect another conversation or overwrite
a newer turn/session. Daemon shutdown cancels and joins all owned timer and
inference workers. Its publisher snapshots dirty conversations' current state
and fans out only to interactive connections; bursts may coalesce to the
latest revision. The relay's revision guard prevents an overtaken snapshot
from restoring old text. These frames carry no `EventID` and enter neither
history nor replay. `main` mints the stream-only owner beside msgqueue;
`startRelayV2` binds the session resolver, wires `ReplySuggestions` beside
`RunningTurnPhases`, and starts a publisher joined during cleanup.
See [the wire contract](../../protocol-mobile.md#reply_suggestion) and
[connect-time reconciliation](v2-session-manager-state-machine-connect-time-reply-suggestion-reconcile.md).

Persistent stream `buildArgs` requests `--prompt-suggestions` on create and
resume spawns unless `promptSuggestionsDisabled` finds the last effective
`CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION` entry equal to `false`. Claude honours
its own `promptSuggestionEnabled: false` settings disable. Missing native
output can now use the fallback independently of those native controls; see
[the live-test staging and reader lessons](e2e-realclaude-test-infrastructure.md#test-infrastructure).
