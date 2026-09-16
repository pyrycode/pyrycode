# #2456 — a client's `/clear` runs the reset path instead of reaching claude

## Files read

- `internal/relay/handlers/send_message.go` → `SendMessage` — the handler the seam goes
  into; its ordering contract (the 32-id bound before `Route`, `Route` before
  `resolveAttachments`) is what fixes where the intercept sits.
- `internal/relay/handlers/send_message.go` → `SessionRouter`, `Enqueuer`,
  `AttachmentResolver`, `ConversationToucher` — the house shape for a consumer-side seam
  in this package: declared here, narrow, nil-tolerant, documented at the interface.
- `internal/relay/handlers/send_message.go` → `touchConversation`, `autoNameConversation`
  (in `autoname.go`) — the two post-ack registry writes the intercept must sit ABOVE, and
  the precedent for "the ack goes out first, side effects follow".
- `internal/relay/handlers/send_message_test.go` → `stubSessionRouter`, `routeTo`,
  `fakeEnqueuer`, `newSendMsgConn`, `sendMsgRequest`, `assertSendMsgEnvelopeShape`,
  `sendMsgCapturingLogger` — the whole test rig this ticket's tests reuse; also the
  24 `SendMessage(` call sites the signature change touches.
- `internal/relay/v2session_seams.go` → `SessionStarter`, `LateSessionStarter`,
  `RotatedWithoutWorkspaceError` — the seam being given a second caller, and the reason
  the plain form is the right one here (see § Design).
- `internal/relay/v2session_modal.go` → `handleNewSession` — the sibling caller's
  posture: best-effort, Warn-logged, no rollback, no reply owed. Its arm-4 doc is what the
  intercept's error arm copies.
- `cmd/pyry/main.go` → `activeSessionStarter`, `start`, `StartNewSession`,
  `resetThenRotate`, `resolveSpawnDir` — the reset entry point. Every inert /
  already-resetting / no-live-child arm AC-4 names already lives in `start`, which is why
  this ticket adds no reset logic.
- `cmd/pyry/main.go` → `startFreshRunner`, `installSpawnDir` — the errors that can come
  back through the plain seam form, needed for the log-safety finding in § Security review.
- `cmd/pyry/relay.go` → the `protocol.TypeSendMessage` registration and the
  `activeSessionStarter` field on the wiring struct — the one production call site.
- `internal/e2e/internal/fakeclaude/main.go` → `envClearRotates`, `clearRotatePending`,
  `containsClearCommand`, `startStdinReader`, `rotateSession` — the mode AC-5 retires and
  the helper (`rotateSession`) that must survive because the file trigger shares it.
- `internal/e2e/internal/fakeclaude/clear_detect_test.go` → `TestContainsClearCommand` —
  the detector test AC-5 retires.
- `docs/knowledge/features/relay-package-handlers.md` § "`send_message` grows a sixth
  seam" and § "a seventh write" — the package overview's record of how the last two seams
  were threaded through this same handler.
- `docs/knowledge/features/fakeclaude-binary-clear-rotate-mode.md` — confirms the mode's
  only driver was the deleted terminal supervisor typing `/clear` into a PTY.

## Context

`/clear` typed in a client reaches claude as ordinary message text today and clears the
conversation in place. That leaves two "start over" verbs whose only difference is whether
the child process restarts, and the in-place one skips the wrap-up turn and the handoff
note entirely. Juhana decided on 2026-09-06 that claude's in-place clear is no longer
reachable from a client.

The reset itself is built and merged: #2477 shipped the wrap-up turn and the handoff-note
write, #2478 the `resetting` frames, and both hang off `activeSessionStarter.start`,
reached through `relay.SessionStarter`. **This ticket adds no reset logic.** It adds a
second caller of that seam: a fixed-literal match on inbound message text, a route into
the seam, and the tests that pin both.

No ADR is warranted. The decision this encodes — one reset verb, reachable two ways — is
recorded on the ticket and belongs in `docs/protocol-mobile.md`'s `send_message` row,
which the documentation stage owns.

## Design

### The seam

A new consumer-side interface in `handlers`, beside `SessionRouter` and `Enqueuer`:

```go
type ConversationResetter interface {
	StartNewSession(conversationID string) error
}
```

Named for the role this handler puts it to (the package's convention: `SessionRouter`,
`ConversationToucher`, `ConversationAnnouncer`) while keeping the method name of the sealed
surface — the same split `relay.SessionStarter`'s own doc argues for. `handlers` imports
neither `internal/relay` nor `internal/sessions`, so the interface is declared here and
`cmd/pyry` passes its existing `relay.SessionStarter`-typed value: an interface value is
assignable to any interface its method set covers, so this costs no adapter, exactly as
`ConversationToucher` does over the same registry value.

**The plain form, not `LateSessionStarter`.** #2443's `new_session.workspace_refused` reply
is the late form's entire reason, and it is correlated by `in_reply_to` against a
`new_session` frame. Answering a `send_message` with it would misreport which frame it
describes. A `/clear` whose rotation cannot re-enter the conversation's recorded workspace
therefore gets no error frame at all.

`SendMessage` gains the seam as its eighth parameter, after `announce` and before `logger`
— beside the other optional seams, ahead of the logger the package keeps last.

### The match

One fixed literal, in an unexported predicate:

```go
// isClearCommand reports whether text's first whitespace-delimited token is
// exactly "/clear".
func isClearCommand(text string) bool
```

Exact, case-sensitive, first token only, never a prefix and never a vocabulary.
`strings.TrimSpace` (which returns a subslice, so no copy), then `strings.HasPrefix`
against the literal, then a boundary check that the next rune — if any — is
`unicode.IsSpace`. Deliberately **not** `strings.Fields`: that allocates one string per
token, and a 1 MiB text (the transport's WS read ceiling) would make a remote client
buy ~100k allocations per message for a predicate that only ever needs the first token.

Accepts: `/clear`, `  /clear  `, `/clear and more`. Refuses: `/clearcache`, `/CLEAR`,
`/compact`, `/model`, `do /clear`.

### Where the intercept sits

Inside `SendMessage`, between the `router.Route` block and the `resolveAttachments` call.

- **After `Route`** so the conversation id is registry-validated and the follow-active
  cursor is stamped before anything acts on it. That is also what keeps AC-4's third row
  free: an unknown or unbound conversation is rejected by `Route`'s existing arms
  (`conversation.not_found` / `server.binary_offline`) before the match is ever consulted.
- **Before `resolveAttachments`** so no attachment id is resolved (AC-2) — no
  `attachment.not_found` can be answered for a `/clear`, and no directory read happens on
  its behalf.
- **Before `EnqueueDelivery`** so nothing is queued and no `queue_state` item is published.
  That last property is structural rather than asserted downstream: `queue_state` is
  emitted by the queue's own observer, so a message never enqueued produces no item.
- **Above `touchConversation` and `autoNameConversation`**, so an intercepted `/clear`
  never becomes the conversation's auto-name (AC-2) and stamps no `LastUsedAt`. The
  `LastUsedAt` decision is deliberate and matches the sibling route: a `new_session` frame
  driving the same reset stamps nothing either, and this ticket's promise is that the two
  give the same reset.

The 32-id attachment bound stays where it is, above `Route`: it is a pure frame-shape rule
and a `/clear` naming 33 attachment ids is still a non-conforming `send_message`.

### Order inside the intercept

1. Record the interception (Info; conn id, conversation id, message id — never the text).
2. `replyAck` — the ordinary ack, the one the client already expects.
3. `StartNewSession(p.ConversationID)`.
4. Return the ack's error.

**The ack goes first**, for the reason `touchConversation` already records for itself: the
wrap-up arm of the entry point hands off to its own goroutine, but every other arm runs
inline on the calling goroutine, so an inert arm's work would otherwise sit between the
frame and its ack.

A non-nil seam error is Warn-logged and tolerated — no reply is owed and there is nothing
to roll back, the posture `handleNewSession`'s arm 4 already takes for the same seam.

**A nil resetter still drops the message.** Fail-closed here means the `/clear` never
reaches claude, because that reachability is the exact thing the ticket removes; an
unwired seam must not quietly restore it. The drop is recorded at Debug.

### No interactive gate

`new_session` is intercepted by the v2 session manager and gated on the negotiated
`interactive` capability; `send_message` is a `dispatch.Route` handler and `dispatch.Conn`
carries no such flag. The intercepted `/clear` is **not** interactive-gated: pairing is the
authorization boundary, and `new_session` is already published as exempt from the
per-device permission gate (#702), so plumbing the flag in would be new machinery for no
authorization gain. A non-interactive client's `/clear` resets and simply does not see the
`resetting` frames it declined.

### AC-5: the fake's clear-rotate mode

Pure deletion from `internal/e2e/internal/fakeclaude`: the `envClearRotates` const, its
file-header doc block, the `clearRotates` local and its `startStdinReader` argument, the
`clearRotatePending` var, the detector `containsClearCommand`, the reader's accumulate arm
(`clearAcc` / `clearDetected`), the poll-loop rotate arm, and `clear_detect_test.go`.

`rotateSession` and the shared `rotated` gate **stay** — the file trigger uses both. The
other stdin-reader modes (esc-ends-turn, modal-clear-on-answer, trust-trigger, stdin
logging) are untouched; `startStdinReader` loses one bool parameter and its sole caller
updates. Nothing in the repo sets the env var: the mode has been stranded since #1348
deleted the terminal runner that typed `/clear` into a PTY.

## Concurrency model

No new goroutines. The intercept runs on the dispatch goroutine that already runs
`SendMessage`; `StartNewSession` is documented safe from any goroutine, and the one arm
that is slow (the wrap-up) already moves itself off the caller's goroutine inside
`resetThenRotate`. The reset's own already-in-progress guard (`conversationReset.begin`)
is the serialization point for two `/clear`s racing, and it is untouched.

## Error handling

| Condition | Answer |
|---|---|
| Unknown conversation | `conversation.not_found`, from `Route` — unchanged, before the match |
| Unbound conversation | `server.binary_offline`, retryable, from `Route` — unchanged |
| `/clear`, reset in progress | Seam returns nil having dropped it; client gets the ack |
| `/clear`, no live child | Seam's inert arm returns nil; client gets the ack |
| `/clear`, rotation failed | Seam error, Warn-logged, tolerated; client keeps the ack |
| `/clear`, workspace refused | `*RotatedWithoutWorkspaceError`, Warn-logged, no error frame |
| `/clear`, no resetter wired | Dropped, Debug-logged; client gets the ack |
| Any other text | Unchanged: resolve, enqueue, ack, touch, auto-name |

`payload.Text` is never logged on any of these, as on every existing branch.

## Testing strategy

New tests in `internal/relay/handlers/send_message_test.go`, over a `fakeResetter` double
recording the ids it was handed and returning an injectable error:

- **`/clear` is intercepted** — seam called once with the routed conversation id; nothing
  enqueued; the write surface never driven; the client still gets an `ack`. (AC-1)
- **Attachments are dropped** — a `/clear` naming ids whose resolver would refuse them is
  still intercepted: the resolver is never called, no `attachment.not_found`, and the
  conversation's stored `Name` stays nil. (AC-2)
- **Table: what is and is not the literal** — `/clear`, `  /clear  `, `/clear extra`
  intercept; `/compact`, `/model`, `/clearcache`, `/CLEAR`, `do /clear`, `""` enqueue
  byte-for-byte with the delivery equal to the text. (AC-3)
- **Rejects come first** — a `/clear` on an unknown conversation answers
  `conversation.not_found` and never reaches the seam; on an unbound one,
  `server.binary_offline`. (AC-4, third row)
- **Seam outcomes are tolerated** — a seam returning an error (the drop / inert / failed
  arms as seen from here) still leaves the client with an ack and nothing enqueued, and the
  record carries no message text. (AC-4, first two rows; their behaviour is `start`'s and
  is already pinned by #2477's arm tests in `cmd/pyry`.)
- **Nil resetter drops rather than delivers** — nothing enqueued, ack sent.
- **Ack precedes the reset** — a resetter that observes the outbound channel sees the ack
  already queued when it runs.

AC-5 needs no new test: it is a deletion, and the fake's remaining modes are covered by
`esc_detect_test.go` / `modal_detect_test.go` and the e2e suite, which must stay green.

Gate: `go test -race` on `./internal/relay/handlers/...`, `./internal/e2e/...` and
`./cmd/pyry/...`, plus `go vet ./...` and `go build ./cmd/pyry`.

## Sizing — one boundary exceeded, stated deliberately

Five of the six one-ticket numbers hold: 3 production files, ~720 lines of total written
work, 1 new exported type, 5 acceptance criteria, 1 new reject branch.

**Consumer call sites: 25, against a boundary of 10.** `SendMessage` gains a parameter, so
its one production call site and all 24 in `send_message_test.go` update.

This is not split, and the reason is the floor rule rather than a re-count of the edits.
The only split that addresses *this* boundary is "introduce the parameter" then "use it",
whose first child ships a parameter nothing reads and whose sole consumer is its own
sibling — the one-consumer shape the floor forbids, and the floor wins over the ceiling.
The other available axis (intercept vs. fake cleanup) separates two real deliverables but
leaves the whole cascade inside the first child, so it fixes nothing that tripped.

The overage is recorded rather than argued away. The nearest analogue is #2159, which
threaded three new seams through this same handler and paid the same cascade across the
same file at 920 lines.

## Documentation handoff

Owned by the documentation stage — **pending**, not done here. The builder edits no file
under `docs/` other than this plan.

1. `docs/protocol-mobile.md`, the `send_message` row in **§ Application message types**
   (the verb has no `####` section of its own): state the one intercepted literal — exact,
   case-sensitive, first token only; attachments dropped; no interactive gate; no
   `workspace_refused` reply; and the absent history row.
2. `docs/protocol-mobile.md`, the dated **`2026-09-15`, not yet live:** note following
   § `slash_command_list`'s SECURITY paragraph: resolve it to point here. Its closing
   sentence — "Until #2456 lands, the paragraph above remains accurate as written" — is
   what goes stale.
3. `docs/knowledge/features/fakeclaude-binary-clear-rotate-mode.md` is retired, and its
   bullet dropped from the map in `docs/knowledge/features/fakeclaude-binary.md`. Note
   that `docs/knowledge/features/fakeclaude-binary-what-it-does.md` also names the mode.

One client-visible consequence belongs in item 1 so nobody files it as a bug: a client
renders `/clear` as the operator's own row from its optimistic echo, but the daemon never
delivers it, so history will not hold it and a reload shows the session delimiter with no
row above it. Accepted.

## Open questions

1. **Does an intercepted `/clear` bump `LastUsedAt`?** Resolved in § Design: no, matching
   the `new_session` route into the same reset.
2. **Where does the new parameter go in the signature?** Resolved: eighth, after
   `announce`, before `logger`.
3. **Does `containsClearCommand` survive AC-5?** Resolved: no — the reader arm is its only
   caller, and an unused unexported function would fail staticcheck. `rotateSession` does
   survive; the file trigger shares it.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No exploitable finding; one capability difference, decided and
  bounded.** The new boundary is explicit and singular: `isClearCommand`, one predicate
  with one call site inside `SendMessage`, which decides whether untrusted text steers
  daemon control flow rather than merely transiting to claude. It grants a paired client
  no capability it lacks: `docs/protocol-mobile.md` § New session already publishes that
  `new_session` is exempt from the per-device permission gate (#702) and argues, verbatim,
  that this is "not a widening — a device could already route a `send_message` to any
  conversation to move the cursor there and then send a bare `new_session`". The one real
  difference is the `interactive` capability, which `handleNewSession` checks and this
  path does not. That is a capability negotiation (does this client want the live stream?)
  and not the authorization boundary — pairing is, and this verb is exempt from the gate
  that expresses it — so a non-interactive client gains no privilege, only a reset whose
  `resetting` frames it declined to receive. The intercept must NOT acquire an
  `interactive` check by accident in Phase B; the difference is stated in the
  Documentation handoff.
- **[Trust boundaries] No finding — the daemon never matches against text it authored.**
  `composeAttachmentPrompt` builds the daemon's own prompt block AFTER the intercept, so
  the only string the predicate ever sees is the client's own `p.Text`. There is no path
  by which a daemon-authored or claude-authored string re-enters this match.
- **[Tokens, secrets, credentials] Not applicable — no secret is created, stored,
  transmitted or compared on this path.** The literal compared against is a PUBLIC
  constant, which is also why `crypto/subtle.ConstantTimeCompare` is inapplicable: a
  timing signal on `/clear` discloses nothing not already published in the protocol doc.
- **[File operations] No finding — the containment is inherited, not re-implemented.**
  The intercept performs no file operation. The seam's does: `resolveSpawnDir` runs
  `MkdirAll` and writes `~/.claude.json`. Its confinement holds unchanged for this second
  caller because it lives INSIDE `start`, below every inert arm and below
  `conversationReset.begin`'s already-resetting guard — the placement that function's own
  doc calls load-bearing "so a repeated frame cannot drive MkdirAll". The directory is
  derived from the conversation row's recorded `Cwd` and re-confined to `$HOME` on every
  rotation; **no byte of the message text becomes a path or a path component**, because
  the text is discarded at the intercept and only `p.ConversationID` — already validated
  by `SessionRouter.Route` — crosses the seam.
- **[Subprocess execution] No finding; this ticket REMOVES a data flow.** Today `/clear`
  reaches the supervised child's stdin verbatim. After this change it does not: the
  intercept discards `p.Text` and it reaches no argv, no environment and no stdin. The
  successor child's session id is pool-minted and its directory is `resolveSpawnDir`'s
  confined output.
- **[Cryptographic primitives] Not applicable — no randomness, key, nonce or digest is
  introduced.**
- **[Network & I/O] One finding, fixed in the design: unbounded allocation on the
  match.** The predicate runs on EVERY `send_message`, and `p.Text` is capped only by the
  transport's 1 MiB WS read ceiling. A `strings.Fields` implementation would allocate one
  string header per token, so ~1 MiB of whitespace buys a remote client roughly 500k
  allocations per frame — a remotely-driven amplification on a hot path. § Design's
  `strings.TrimSpace` + `strings.HasPrefix` + boundary-rune form is allocation-free
  (`TrimSpace` returns a subslice) and is one linear scan strictly cheaper than the
  `json.Unmarshal` that already ran over the same bytes. **Phase B must not "simplify"
  this back to `strings.Fields`.**
- **[Error messages, logs, telemetry] No finding — no new disclosure channel, and one
  oracle surface removed.** The intercept adds no error reply at all: every seam outcome
  (dropped as already-resetting, inert, rotation failed, workspace refused, no seam wired)
  answers the SAME plain `ack`, so a client cannot discriminate daemon state from the
  reply. Dropping attachment resolution (AC-2) removes rather than adds a probe surface —
  no `attachment.not_found` can be derived from a `/clear`'s ids. In the records:
  `p.Text` is never logged, as on every existing branch; `conversation_id` is a validated
  registry key by the time it is recorded (stricter than `handleNewSession`, which must
  bound a raw client string); `message_id` is phone-supplied and unbounded but is already
  logged that way on the existing enqueue and backlog-full branches, so this introduces no
  new class of value. The seam error IS logged verbatim, and that is safe by construction:
  `RotatedWithoutWorkspaceError.Error()` returns a constant by design, `resolveSpawnDir`
  discards its confinement error at its own site and returns only a bool, and
  `installSpawnDir` swallows its own error and logs no path — so no phone-influenced
  workspace path can reach this record. `handleNewSession` already logs the same errors
  the same way.
- **[Concurrency] No finding; the goroutine position is better than the sibling
  caller's.** `dispatch.Route` — and therefore `SendMessage`, the intercept, and the
  inline `StartNewSession` after the ack — runs on the per-conn worker goroutine that
  `routeAppFrame` drives, NOT on the `V2SessionManager` Run goroutine. So
  `resolveSpawnDir`'s documented syscall blocking stalls only the addressed conn's own
  later frames, never Run and never another conversation — the consequence its doc records
  for `handleNewSession` does not transfer to this caller. That same structure is what
  makes the ack-first ordering real rather than nominal: `replyAck` pushes onto the
  conn's `outbound`, which `routeAppFrame`'s drain loop forwards to Run while the handler
  is still inside the seam call. No new goroutine, no new lock, no new shared state;
  concurrent `/clear`s on one conversation serialize at `conversationReset.begin`.
- **[Threat model alignment] One residual, considered and not new: remote token spend.**
  Each accepted `/clear` on a live conversation causes a wrap-up turn that spends real
  claude tokens, driven by a remote literal. `conversationReset.begin` bounds it to one
  in flight per conversation, but sequential `/clear`s buy sequential wrap-ups. This is
  not a new vector and not an amplification: sequential `new_session` frames do exactly
  the same thing on the same entry point (#2477's shipped behaviour), and the same client
  can spend far more by sending ordinary messages. No mitigation added here. Separately,
  `docs/protocol-mobile.md`'s stance that inbound message text is never a command
  vocabulary takes its one published exception, and the security requirement that follows
  is a constraint on this design and on every future edit to it: the match stays **one
  fixed, case-sensitive, first-position token** — never a prefix, never a table, never a
  case-insensitive compare. § Testing strategy's AC-3 table (`/compact`, `/model`,
  `/clearcache`, `/CLEAR`, `do /clear`) is what pins it against drift.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-16
