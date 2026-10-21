# Raw history query retirement

The raw `request_history` endpoint is retired (#3715). `dispatchAppFrame` no
longer intercepts it, `appFrameWorker` has no raw-history job, and
`handleRequestHistory`, `serveHistoryPage`, `V2SessionConfig.HistoryPage`,
`HistoryPager` and the daemon's `newHistoryPager` adapter/wiring are removed.
There is no replacement raw pager or successful empty response.

## Dispatch and concurrency

An authenticated, well-formed literal request follows ordinary `dispatch.Route`
rejection: correlated `protocol.unknown_type`, static message
`unknown envelope type`, `retryable: false`. It consults no membership or history
provider. Generic malformed/encrypted-envelope rejection keeps its precedence.
`TestV2Session_RetiredHistoryQuery` proves repeated-request correlation, exact
payloads, no peer delivery and no request-canary disclosure through Noise.
Existing per-connection FIFO workers and Run-owned cipher sealing remain;
retirement adds no goroutine or lock.

## Recovery and source boundaries

Use [Thread query messages](../../protocol-mobile.md#thread-query-messages)
for newest windows, older pages and catch-up. The production thread providers
remain separate from the removed raw pager.
`TestDaemonThreadQueriesRecovery`, `TestThreadCatchUpRequestedScope` and
`TestThreadPageRequestedScope` retain authenticated recovery and named-scope
coverage. See [thread delivery and queries](v2-session-manager-state-machine-capability-negotiation-on-the-handshake-thread-delivery-and-summary-projection.md).

[`internal/history`](history-package.md) retains raw entries and storage APIs
for thread folding. `historyPageFailure` remains used by startup reconciliation;
`CodeHistoryUnavailable` remains used by list/read handlers. Legacy live/replay
publication, receipts and read tracking are independent of this retirement.

## Testing the retained behavior

Deleting endpoint tests can also delete shared protocol/pairing/session helpers.
Keep general helpers in independent test files (`nextAppReply` / `mustMarshal`
in `v2session_appframe_helpers_test.go`, `assertWireKeys` in `wire_keys_test.go`)
and compile affected tagged suites: ordinary unit selection cannot reveal a
helper missing only behind a build tag. Preserve producer, live/replay and
read-tracking assertions in mixed tests using source records or authoritative
thread queries, rather than recreating the removed endpoint.

Noise test readers must decrypt every next frame in arrival order, including
intervening replies. Restarting a scan or skipping ciphertext desynchronises
receive nonces. A timeout can close a fakephone connection; use a correlated
supported reply as an observation barrier when later live assertions still need
that connection. See [held-output observations](e2e-harness-stream-interactive-harness-pattern-startstreamin.md#observing-held-output-without-closing-the-phone-connection).
