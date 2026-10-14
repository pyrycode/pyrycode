# #3357 — Retry history proof

## Files read

- `cmd/pyry/queued_send_history_test.go` → `TestQueuedSendHistoryLifecycle`, `testQueuedSendRetryWriter`: repaired retry fixture and retained delivery/drop assertions.
- `cmd/pyry/queued_legacy_receipts_test.go` → `testQueuedReceiptIdentity`: repeated drains preserve durable identity and single publication.
- `cmd/pyry/queued_send_history.go` → `accepted`, `delivered`, `terminal`; `cmd/pyry/operator_message_history.go` → `operatorMessageHistory`: acceptance/outcome joins and operator-message placement.
- `internal/msgqueue/queue.go` → `New`, `drain`: default two-minute give-up window; synchronous warning output precedes its elapsed-time check.
- `docs/knowledge/features/msgqueue-package-give-up.md` § Testing: a shared 1 ms deadline can expire during warning output before another attempt.
- `docs/knowledge/features/history-package-producers.md` § Accepted sends and linked outcomes: hidden drops retain acceptance links and source without raising raw unread watermarks.
- `CODING-STYLE.md`; `docs/knowledge/features/development-verification.md` § Verify inherited premises: reuse the merged dependency's proof.

## Change

Reuse #3324 / PR #3326's test-only repair at `9cdd7fad475c0cbee68ed4aeb581006e150074cf`, now on main at `a920b90cc6bfd2f1fb8461e9f85ce6f19934afa6`. Only `give_up` has a 1 ms deadline. The retry arm fails attempt one, delays synchronous warning output by 5 ms and succeeds on attempt two under the default budget. Its three history entries, acceptance/delivery links, exactly-one operator push and repeated receipt identity/publication assertions remain intact. The separate give-up arm retains the hidden `send_dropped`, `give_up` reason, acceptance link, terminal source and raw/legacy watermark assertions. No remaining code gap was found; this branch records the reuse and verification without duplicating the repair or changing production behavior. No feature branch overlaps these paths. One deliverable; approximately 30 written lines, zero new exports, consumer migrations or reject branches, two acceptance criteria.

## Testing strategy

Run `TestQueuedSendHistoryLifecycle` repeatedly with the race detector, then the complete `cmd/pyry` race package, `go vet ./...` and the daemon build. Existing assertions cover successful retry and independent give-up; no new stimulus or assertion is required.

Upstream full-gate evidence: the final [verifier review of PR #3326](https://github.com/pyrycode/pyrycode/pull/3326#issuecomment-6171911494) reports dispatcher `make check` and `make build` passed on `c13e2533537f1e050e3827090aa939c95666b15e`. This is credited upstream evidence, not execution by this builder, and newer main changes prevent treating it as a full-gate pass on this branch. The dispatcher/verifier must run the full `make check` on the pushed candidate and record its tested commit and result; scoped checks do not replace that acceptance criterion.
