# Retired raw conversation-history payloads

`request_history` / `history_page` were removed in #3715. The endpoint-only
`internal/protocol/history.go`, `RequestHistoryPayload`, protocol `HistoryEntry`,
`HistoryPagePayload`, both type constants and their positive paging fixtures/tests
no longer exist. They belong in neither the v2 partition tables nor the relay
handler inventory. The older `backfill_since` / `message_chunk` / `backfill_done`
content-recovery vocabulary was removed in #967 and remains retired.

Use [Thread query messages](../../protocol-mobile.md#thread-query-messages) for
bounded newest-window, older-page and catch-up recovery, as defined by
[ADR 042](../decisions/042-daemon-built-thread.md). Raw history is not a fallback
for clients lacking thread negotiation.

## Rejection and error ownership

An authenticated well-formed literal `request_history` reaches `dispatch.Route`
as an unknown type. The correlated `error` payload is exactly
`code: "protocol.unknown_type"`, `message: "unknown envelope type"`,
`retryable: false`. No endpoint payload decode, membership lookup or page read
occurs. Malformed envelopes retain generic rejection and `payload_encrypted`
still takes precedence over type recognition in `IsKnownAppType`.
`TestV2Session_RetiredHistoryQuery` checks those boundaries through authenticated
Noise dispatch, repeated requests, an independent peer and request-data canaries.

The endpoint-only `history.invalid_request`, `history.invalid_page_size` and
`history.invalid_cursor` codes were removed. `CodeHistoryUnavailable` remains:
list/read handlers still need the shared retryable `history.unavailable` refusal.
Removing a wire endpoint does not imply that every similarly named error is
endpoint-exclusive; check production consumers before deleting it.

## Source records and content trust

[`internal/history`](history-package.md)'s `Entry`, storage paging/walk APIs and
`appendConversationHistory` remain. They supply raw facts to
[`internal/thread`](thread-package.md); the removed protocol `HistoryEntry` was
a different type. Legacy live/replay publication, receipt projection and read
tracking remain separate behaviors.

Stored payload bytes retain their original trust class: operator-authored input
and Claude-authored output do not become trusted because the daemon persisted
or replayed them. A content-bearing nested payload needs the same sanitisation
as its live counterpart; attachment-request trust arguments cannot be copied
onto content recovery. Thread folding is the rendering boundary, not a reason
to regard stored text as trusted.
