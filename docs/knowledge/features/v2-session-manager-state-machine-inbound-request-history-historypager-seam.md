# Inbound `request_history` (#2116) — `HistoryPager` seam

`request_history` is a v2 **control** envelope (phone → binary), intercepted
in `dispatchAppFrame`'s discriminator switch **before** `dispatch.Route`, and
routed to the conn's `appFrameWorker` rather than handled inline on `Run` —
the same reason [`request_attachment`](v2-session-manager-state-machine-inbound-request-attachment-attachmentresolve.md)
(#2054) is: answering it reads log segments off disk and marshals a page. It
joins [`internal/history`](history-package.md)'s `Store.Page` (#2112, unwired
until this ticket), the wire vocabulary (#2113) and the two log producers
(#2114, #2115) into an answered verb. `security-sensitive`. See
[`docs/specs/architecture/2116-history-page-handler.md`](../../specs/architecture/2116-history-page-handler.md).

Both a page and a reject leave by the **same** route, `forwardToRun` — unlike
`request_attachment`, there is no `Push` leg, since a page is one envelope
rather than a stream. That makes this handler's emission path strictly
simpler to keep single-owner-safe than #2054's two-route split.

## An outcome discriminant, not comma-ok, because the merge itself needs a seam

`AttachmentResolve`'s `(path string, ok bool)` shape was the obvious template,
and it doesn't fit here. That seam collapses several failure causes into one
bool *because none of them needs telling apart on the daemon side either*. This
one does: a cursor that fails to parse is `history.invalid_cursor`, but a
segment that has gone corrupt or an I/O error is the separately-coded, uniquely
retryable `history.unavailable` — the merge that the wire owes the client
(three cursor causes → one code) is not the same merge the *seam* needs to
preserve internally, because an operator still needs to tell "a client sent
garbage" from "this daemon's disk is failing" apart in the log. A comma-ok
seam here would erase that distinction before the handler ever saw it. The
seam instead carries a small outcome enum (`HistoryPageOK` / `HistoryPageBadCursor`
/ `HistoryPageUnavailable`) across the `internal/relay` ↔ `cmd/pyry` boundary,
with `HistoryPageOK` as the zero value on purpose: an adapter that forgets to
set an outcome reports success with zero entries — the terminal empty page,
which is inert, not a false refusal.

**The generalizable point:** a seam that merges several failure causes for the
*client's* benefit is not automatically the right shape for the *operator's*
diagnostic needs too — check both audiences before reusing a comma-ok
precedent from a neighbouring handler.

## A page's cursor names a position, so shortening by truncation silently skips entries

`Store.Page`'s cursor points just before the *oldest entry the log returned*.
The tempting one-liner for fitting a page inside the 65519-byte
application-envelope cap is to slice the returned entries down and keep the
same cursor — and that is wrong: the cursor still claims to name the position
before the (now-dropped) oldest entry, so the client's next ask silently jumps
over exactly what got truncated. The bug is invisible to any test whose pages
all fit, and to any single-page assertion — it only surfaces in a walk long
enough to force shortening, which is why
`TestV2Session_RequestHistory_ShortensAPageToFitTheEnvelopeCap` walks the whole
log under forced shortening and asserts every entry exactly once. The correct
fix is to **re-ask `Store.Page` at a strictly smaller limit** and let the log
answer honestly for that limit — never to slice a page already in hand. The
re-ask limit is derived by marshalling entries newest-first and taking the
largest prefix that fits, clamped so the limit strictly decreases every
iteration (termination is structural, not dependent on the attempt cap); an
attempt cap exists only to bound the concurrent-append case, where a fresh
append between two asks can make a computed fit miss once. A page shortened
this way never sets `AtStart` on that account — `AtStart` is the log's own
answer for the smaller limit, not something the handler synthesises.

## A client's `limit` reaching a downstream clamp unmodified is amplification, not delegation

The first draft passed the client's `limit` straight through to
`history.Store.Page`, reasoning that its own clamp (`MaxPageEntries = 4096`)
already owns the ceiling. Security review caught this: `MaxSegmentBytes`
bounds one stored entry line at 1 MiB, so an honoured `limit: 4096` can force
the daemon to open ~256 segments and hold up to ~256 MiB of decoded entries
per request — and every one of those entries is then thrown away by the byte
budget, since no page above roughly 1365 entries can ever serialise inside
65519 bytes regardless of what it holds. The fix is a **second, smaller
ceiling** (`maxHistoryPageEntries = 1024`) enforced one package up, derived
from the envelope cap rather than copied from the log's count ceiling. A
downstream package's own clamp existing is not evidence that passing an
unmodified client value through to it is safe — the two clamps can be
protecting against different things, sized for different reasons, and the
gap between them is exactly where a resource-amplification bug lives.

## The page/live dedup key: an id on either side doesn't survive contact with the producers

A client that just connected asks for the newest page and receives the live
stream at the same time; the two must meet with no gap and no duplicate. The
obvious candidates for the join key both fail on inspection: the durable log
`id` has no live-side counterpart at all (the live lane's `event_id` is
`eventring`'s per-process one, explicitly a different namespace per #2113,
and the `session_transition` producer skips the ring entirely so its live
envelope carries no `event_id` to compare); `turn_id` + `seq` covers only
turn-scoped payloads, and `turn_state` / `stall` / `session_transition` and
others carry neither. What survives is **(`type`, `ts`)** — sound only because
both #2114 producers hoist one `ts := time.Now().UTC()` per logical event
*above* the per-conn fan-out and hand the identical value to both
`appendConversationHistory` and every live envelope, a property of the
producers rather than of this handler. `session_transition` is the type that
proves the key actually needed to hold for a no-`turn_id` case, not just in
theory. **A dedup key chosen for a join between two lanes has to be checked
against every producer that writes either lane, not just the common case** —
the candidate that "obviously" carries identity (an id) is the one most likely
to turn out to be scoped to one lane only.

## A multi-page walk needs one sequential decrypt, never one that restarts from the top

The receive `CipherState`'s nonce advances in lockstep with the sender's, so
any test helper that re-decrypts frames from the beginning of the exchange to
find the one it wants will desynchronise the stream — every later frame then
fails authentication, and the failure reads like a crypto bug rather than a
test-harness one. The e2e's `awaitHistoryPage` reuses `nextAttachmentEnvelope`
(from the #2054 retrieval-leg tests) precisely because that helper decrypts
**the next envelope in arrival order** and classifies only after decrypting —
skipping a frame instead of consuming it is the same desynchronisation bug
from the other direction. Any future e2e that awaits a specific reply among
interleaved traffic on one Noise-encrypted conn should reuse this helper (or
its ordering discipline) rather than writing a fresh "scan for the one I
want" loop.

## Related

- [Inbound `request_attachment` (#2054)](v2-session-manager-state-machine-inbound-request-attachment-attachmentresolve.md) — the structural template (ordering, worker handoff, `forwardToRun`) and the comma-ok seam shape this ticket departed from.
- [`internal/history`](history-package.md) — `Store.Page`, the cursor's own refuse-one-sentinel discipline, and § Reader for the adapter that classifies its sentinels.
- [History request/reply payloads (#2113)](protocol-package-types-history-payloads.md) — the frozen wire shapes, `Limit`'s zero-means-daemon-chooses rule, and why termination must be `AtStart` and never an entry count.
- [Error codes](protocol-package-constants-codes-go-error-codes-21.md) — the four `history.*` codes minted here.
- [Concurrency](v2-session-manager-concurrency.md) — the `appFrameKind` widening this ticket's dispatch case reuses.
