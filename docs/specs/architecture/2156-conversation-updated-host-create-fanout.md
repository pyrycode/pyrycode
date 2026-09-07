# 2156 — a conversation created on the host reaches every connected client

## Files read

- `cmd/pyry/attachment_offer_v2.go` → `attachmentOfferEmitterV2`, `newAttachmentOfferEmitterV2`, `announce` — the emitter body this ticket copies: marshal once, one timestamp per announcement, the `!c.Interactive` gate, a per-emitter envelope-id counter under a leaf mutex, return on `ctx.Err()`, log-and-continue otherwise. Its header states the contract AC-3 repeats.
- `cmd/pyry/attachment_offer_v2_test.go` → `TestAttachmentOfferEmitterV2_Announce_GatesOnInteractive`, `…_SharedTimestampMonotonicIDs`, `…_TornDownConnDoesNotStopTheFanOut`, `…_TeardownReturnsEarly` — the four cases the emitter's own contract needs, and the `fakeInteractiveBcast` / `recordedPush` doubles they drive (declared in `interactive_turn_v2_test.go`, same package, reused verbatim).
- `cmd/pyry/channel.go` → `channelCreator` — the call site. Holds `reg`, so the read-back is in reach; its refusal branches all return before the success path where the push belongs.
- `cmd/pyry/attach_file.go` → `fileAttacher` — the parameter-list precedent: `announce` is a bare func, may be nil, is called LAST on the success path only, and its result is deliberately not consulted.
- `cmd/pyry/relay.go` → `startRelay`, `startRelayV2` — where the bare func is built (`newAttachmentOfferEmitterV2(mgr, ctx, logger).announce`, beside `mgr` and outside the `w.approvals != nil` block) and returned; and the no-URL early return that makes it nil.
- `cmd/pyry/main.go` → `runSupervisor`'s composition root — the `startRelay` destructure and the `ctrl.SetChannelCreator(channelCreator(...))` wiring, in the between-`NewServer`-and-`Serve` window.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveBroadcaster` — the two-method consumer-declared fan-out surface (`ActiveConns` + `Push`); `*relay.V2SessionManager` satisfies it.
- `internal/relay/handlers/promote_conversation.go` → `PromoteConversation` — the `reg.Get` read-back feeding a `protocol.ConversationUpdatedPayload` literal, field for field. The payload build here is a copy of it.
- `internal/protocol/conversations_write.go` → `ConversationUpdatedPayload` — six fields, `Name` a pointer, and a header recording that the type deliberately omits `system_prompt` because a projection lacking a field cannot leak it.
- `internal/protocol/conversations_read.go` → `ConversationSummary` — carries `Cwd` too. Load-bearing for the security review: every paired conn can already fetch this row's workspace path with `list_conversations`.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes`, `TestEveryInboundV2TypeHasHandler` — the map entry AC-4 names, and the three assertions that consume it. Assertion #3 keys on membership only; the map's *value* is a free-form review label no assertion parses.
- `docs/protocol-mobile.md` § v2 type table (the `conversation_updated` row) — today an empty Notes cell; the sentences at § Setting a conversation's system prompt are explicitly not this ticket's to correct.
- `docs/knowledge/features/control-plane.md` § Channel: new verb — #2155's lessons. The one that changes this build: `channelCreator`'s guards are deliberately doubled against seams that fail open, and its refusal messages are static constants because `SetChannelCreator` forwards the error text to the wire verbatim. Nothing this ticket adds may return a new error from the creator.

## Context

`pyry channel new` (#2155) writes a promoted conversation row on the host. No connected client learns of it until its next `list_conversations`, and nothing prompts one — desktop re-lists on connect and on any `conversation_updated` it receives (pyrycode-desktop#273, #275), so a pushed `conversation_updated` carrying the new row is exactly the trigger it already honours.

No verb in the conversation family fans out today: all four producers of `conversation_updated` answer their requester through `c.Reply`. A host-side create has no requester on the wire, so a push is the only route to anyone.

This is #2166 one frame over — a host-side control verb produced something and no client was told — and that ticket's shape is copied rather than re-derived: a small emitter over the relay's interactive broadcaster, returned from `startRelay` as a bare func that is nil when the relay is off, handed to a control-server dependency's constructor.

No ADR is warranted. The design decides nothing new; it applies an existing precedent to a second producer. What *would* deserve one — whether the four wire verbs should fan out the same way — is out of scope here and named as such below.

## Design

### The emitter — `cmd/pyry/conversation_update_v2.go` (new)

```go
type conversationUpdateEmitterV2 struct {
    bcast  interactiveBroadcaster
    ctx    context.Context
    logger *slog.Logger
    mu     sync.Mutex
    nextID uint64
}

func newConversationUpdateEmitterV2(bcast interactiveBroadcaster, ctx context.Context, logger *slog.Logger) *conversationUpdateEmitterV2

func (e *conversationUpdateEmitterV2) announce(p protocol.ConversationUpdatedPayload)
```

`announce` is `attachmentOfferEmitterV2.announce`'s loop with a different payload type and a different `Type` constant: marshal once before the loop; one `time.Now().UTC()` for the whole fan-out; skip `!c.Interactive` (the #607 capability gate); bump `nextID` under `mu` (a leaf lock held around the counter and nothing else — never across `ActiveConns` or a `Push`, so there is no lock order against the manager's own); `Push`; `return` on `ctx.Err()`; log at Debug and continue on any other push error. It returns nothing, and that is the contract rather than an omission — a failed announcement must not turn a successful create into a refusal.

The mutex is load-bearing for the same reason it is on the offer emitter and not on the emitters that sit on the relay's single `Run` goroutine: `control.Server.Serve` accepts each conn onto its own goroutine, so two concurrent `channel.new` calls announce concurrently.

**Why the seam carries `protocol.ConversationUpdatedPayload` and not the row.** Two alternatives were weighed:

- *Pass `conversations.Conversation`* and let the emitter project. Rejected: the emitter would then hold `CurrentSessionID` and `SessionHistory`, neither of which may reach the wire, and the projection would move away from the read-back that justifies it. The payload type's own header records the principle — a projection that lacks a field cannot leak it — and building the projection at the read-back keeps "the frame carries the stored row" checkable in one place.
- *Pass the six values as primitives*, the way `announce(conversationID, attachmentID, filename)` does. Rejected: six positional parameters including two adjacent bools is a transposition waiting to happen, and the payload struct is already the exact wire shape.

`internal/protocol` is not a relay type, so this does not breach the rule `fileAttacher`'s header states (the value crossing out of the relay leg takes on no `internal/relay` type). `cmd/pyry` already imports the package.

### The call site — `channelCreator`

Parameter list grows by one, in `fileAttacher`'s position (before `log`):

```go
func channelCreator(
    reg *conversations.Registry,
    mint func(label, spawnDir string) (string, error),
    registryPath string,
    announce func(protocol.ConversationUpdatedPayload),
    log *slog.Logger,
) func(cwd, name string) (string, error)
```

On the success path only — after the eager `reg.Save` and after the `channel_new.created` log line, immediately before the `return`:

1. Skip entirely when `announce == nil` (no relay leg): no registry read, nothing.
2. `got, ok := reg.Get(id)`. A miss means the row was deleted between `Create` and here; log a Warn and skip the push. `promote_conversation`'s read-back treats the same miss as truthful current state.
3. Build `protocol.ConversationUpdatedPayload` from `got` field for field, then `announce(payload)`.

The read-back is what AC-1 asks for, and it is not ceremony: it is the difference between announcing what was stored and announcing what the creator believes it stored.

**No new error is returned on any branch.** Every refusal path already returns before this block, and the block itself returns nothing — which keeps `channelNewVerdict`'s output, and therefore `pyry channel new`'s stdout/stderr/exit code, byte-identical to #2155 in every case (AC-3).

### Wiring — `relay.go` and `main.go`

`startRelayV2` and `startRelay` each gain a fourth func return, `announceConversation func(protocol.ConversationUpdatedPayload)`, threaded exactly as `announce` is: built over `mgr` beside the offer emitter (after `mgr`, before `mgr.Run`'s goroutine starts, and outside the `w.approvals != nil` block — it needs `mgr` and nothing else), returned unchanged through `startRelay`, and nil from the no-URL early return. Two call sites, one each.

`main.go` destructures the extra value and passes it into `channelCreator` at the existing `ctrl.SetChannelCreator` line.

### Documentation and the guard — AC-4

- `docs/protocol-mobile.md`: the `conversation_updated` row in the v2 type table gets a Notes cell recording that the frame has two producers — a reply correlated by `in_reply_to` on the four wire verbs, and an unsolicited push carrying no `in_reply_to` after a host-side `pyry channel new` — and that a client MUST accept the uncorrelated form. The sentences at § Setting a conversation's system prompt claiming the record "is broadcast to every phone on the server-id" are left untouched: they are rationale for a different verb's payload shape, they are not made true by this ticket, and correcting them means re-deciding that rationale.
- `cmd/pyry/relay_guard_test.go`: `"TypeConversationUpdated"` moves out of the anonymous `"reply"` block — whose header defines the label as *correlated to a request via `in_reply_to`*, which is no longer the whole truth — into its own commented entry labelled `"reply+push"`, the map's first dual classification. The label is a free-form review string: Assertion #3 keys on membership in exactly one map, and nothing parses the value. The comment carries why the distinction is load-bearing, borrowing the `"TypeAttachmentOffered"` entry's reasoning — nothing solicits this producer's frame, so there is no request envelope for `in_reply_to` to name. **Nothing goes red if this is skipped**, which is why it is an acceptance criterion.

## Concurrency model

No goroutine is spawned, so there is none to leak. `announce` runs synchronously on the `control.Server` handler goroutine servicing `channel.new` — the shape `fileAttacher`'s announcement already runs in — and is bounded there: `ActiveConns` is a snapshot taken under the manager's own lock, and `Push` enqueues without blocking, so a wedged phone cannot hold the verb open.

Locks: `mu` is a leaf, held around the counter bump alone. `reg.Get` takes the registry's own lock, and is called before `announce` rather than inside the fan-out loop, so no registry lock is ever held across a `Push`.

Teardown: `ctx` is the daemon context captured at construction. Once cancelled, `ActiveConns` answers empty and a racing `Push` returns its error, so a late announcement fans out to nobody rather than blocking shutdown.

Registry consistency: `Create` → `Save` → `Get` is three lock acquisitions, not one transaction. A concurrent delete between them yields a `Get` miss, handled above; any other concurrent write is reflected verbatim in the read-back, which is the truthful current state — `promote_conversation`'s documented posture for the identical sequence.

## Error handling

| Failure | Behaviour |
|---|---|
| No relay leg (no URL configured) | `announce` is nil; the create succeeds silently, no registry read. |
| Row deleted between `Create` and the read-back | Warn log; no push; the create still returns the id. |
| Payload marshal failure | Defensive only (a closed struct of two strings, two bools, a `*string` and a `time.Time`). Warn log naming the event and the conversation id; no push; return. |
| A conn torn down mid-fan-out | `Push` answers `relay.ErrConnNotFound`; logged at Debug and skipped; the remaining conns still receive their copies. |
| Daemon teardown mid-fan-out | `ctx.Err() != nil` ⇒ return immediately; no error line, because a daemon going away is not a dropped push. |

No branch can fail the create: `announce` returns nothing and the creator ignores it structurally rather than by discipline.

**Logging discipline.** No log line on any branch carries `Cwd` or `Name`. Both are host filesystem strings — the operator's project directory and its base name — and the daemon's existing `channel_new.created` line already limits itself to the two ids. The fields logged are `conversation_id`, `conn_id`, `env_id` and the transport error, the same set the offer emitter logs.

## Testing strategy

**`cmd/pyry/conversation_update_v2_test.go` (new)** — drives the emitter against the existing `fakeInteractiveBcast`:

- Table over conn sets: one interactive; one non-interactive (receives nothing); a mixed set (only the interactive members, in snapshot order); no conns at all. Each recorded push decodes as `ConversationUpdatedPayload` and every field matches what was announced.
- The frame is `conversation_updated`. The assertion message names AC-2's reason — a `conversation_created` would make desktop navigate into the thread and steal the client's screen on a host-side create.
- One timestamp shared across a fan-out; envelope ids strictly increasing across announcements. Both are loop properties, invisible from a single-conn case.
- A conn answering `relay.ErrConnNotFound` is logged and skipped, and the conn after it still gets its push.
- A cancelled ctx returns early — at most one push recorded, and no push-error line.
- No log line carries the announced `Cwd` or `Name`, asserted on the success, dropped-push, ctx-error and unexpected-error branches. The fixture path and name are distinctive strings so the assertion cannot pass vacuously.

**`cmd/pyry/channel_test.go` (extended)** — the seam, not the loop:

- A successful create announces exactly once, and the announced payload's `Cwd` is the **resolved realpath** and `Name` the base name derived from it. This is the read-back assertion: a payload assembled from the request would carry the un-resolved directory, which differs on macOS where `t.TempDir()` sits under a symlinked `/var`. That divergence is what makes the test non-vacuous.
- A nil `announce` creates successfully — the no-relay daemon.
- Each refusal path (empty cwd, rejected spawn dir, failed mint) announces nothing.
- The existing assertions on returned id, stored row and on-disk state stay green with the hook installed, which is the "the push cannot fail the create" half at this layer.

**Verification gate:** `go test -race ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`. The full-module race suite is the verifier's.

## Open questions

1. **How does a one-label map record a type that is both a reply and a push?** The ticket leaves it to the builder. Resolved in the plan above: a `"reply+push"` value plus a comment, since no assertion parses the value and the alternative — leaving it in the `"reply"` block — is the false sentence AC-4 exists to correct.
2. **Does the seam carry the row or the payload?** Resolved in § Design in favour of the payload, with both rejected alternatives recorded.
3. **Should the emitter be reusable by the four wire verbs?** Not decided here. The type is shaped so it *could* be, but wiring it into `rename` / `archive` / `promote` / `change_workspace` is explicitly out of scope, and doing it would change four handlers' delivery semantics — a decision, not a refactor.

## Out of scope

- Mobile's handling of an unsolicited `conversation_updated`. If it drops the frame it sees the row on its next list, which is today's behaviour.
- Whether the four wire producers should fan out the same way.
- The § Setting a conversation's system prompt paragraphs claiming the record is broadcast to every phone. Still not true of the daemon after this ticket; correcting them means re-deciding a different verb's payload rationale and belongs in its own ticket.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — and the read-back is the boundary, not a nicety.** The design turns a unicast reply into a broadcast, so the question is whether the audience or the content widens. Neither does. *Audience:* the frame reaches only conns that `ActiveConns` reports and that carry `Interactive` — conns that completed the Noise_IK handshake against a device in the registry, the same set `list_conversations` answers. *Content:* `ConversationUpdatedPayload`'s six fields are a strict subset of `ConversationSummary`'s seven, and any such conn can fetch that record for every row at any time. The push therefore delivers sooner a value the recipient could already pull; it discloses no field it could not. The one place a genuine widening could enter is the payload's source: announcing the caller's raw `cwd` would put an unconfined, CLI-authored string on the wire, since `resolveSpawnDir`'s confinement output is what `channelCreator` stores and *not* what it was handed. Building the payload from `reg.Get` rather than from the request is what keeps the announced path the confined, symlink-resolved one — that is why AC-1 is written the way it is, and it must not be "simplified" later into a literal built from the creator's own in-flight values.
- **[Tokens, secrets, credentials] No findings.** Nothing here creates, reads, stores, rotates or revokes a credential. The Noise session keys that seal the frame stay inside `V2SessionManager.Push`. The emitter does mint an identifier — `nextID` — and it is deliberately *not* security-relevant: an envelope id is an ordering key, addresses nothing and authorizes nothing, which is why a plain counter is correct here where an attachment id (an address a client dereferences) is `crypto/rand`. The conversation id in the payload was minted by `conversations.NewID()` in #2155 and is merely echoed.
- **[File operations] No findings — a path-shaped string is not a path operation.** `Cwd` crosses this code as data only: nothing added here opens it, stats it, joins it, or uses any part of it as a path component. The creator's own filesystem work (`resolveSpawnDir`, `reg.Save`) is untouched and completes before the announce block is reached. No file is created, so no mode question arises.
- **[Subprocess / external command execution] No findings.** No `exec.Command`, no shell. The session mint runs earlier in `channelCreator` and is not modified, so no value this ticket handles reaches `claude`'s argv or environment.
- **[Cryptographic primitives] No findings.** No primitive is selected or configured. Sealing remains `V2SessionManager.Push`'s. No RNG is used at all on this path — not `crypto/rand`, and specifically not `math/rand`.
- **[Network & I/O] OUT OF SCOPE — the application-envelope cap (`maxAppEnvelopeBytes`, 65519) is not checked for this frame, and is not this ticket's to add.** A `ConversationUpdatedPayload` is bounded in practice by a UUID, two bools, a timestamp, an OS-bounded `$HOME`-confined path, and `Name`. `Name` has no length bound anywhere in the repo: neither `internal/conversations` nor any wire handler validates it, so `rename_conversation` can already set an arbitrarily long one and `list_conversations` already returns it to every conn. This ticket inherits that property rather than creating it, and inherits it identically to the four existing `conversation_updated` producers. The failure mode is also contained: an oversized frame is refused at `Push`, which the fan-out logs and skips, and which cannot fail the create. Filing a name bound is a repo-wide change across the write verbs and belongs in its own ticket. No inbound read is added, so no cap, deadline or slow-loris question applies.
- **[Error messages, logs, telemetry] No findings, and one trap named.** No branch logs `Cwd` or `Name` — both are host filesystem strings, and `channel_new.created` already restricts itself to the two ids. The logged set is `conversation_id`, `conn_id`, `env_id` and the transport error. The trap is on the *return* side rather than the log side: `SetChannelCreator` forwards the creator's error text to the wire verbatim, which is why every refusal in `channelCreator` is a static constant. The obvious-looking implementation of the read-back miss — returning an error — would breach that contract by putting a new, non-constant message on that path. The design returns nothing from the announce block on every branch, which is also what AC-3 requires.
- **[Concurrency] No findings.** `mu` is a leaf lock, held around the `nextID` bump alone and never across `ActiveConns` or `Push`, so it cannot nest inside the manager's own lock and there is no lock order to reason about. It is load-bearing rather than copied: `control.Server.Serve` accepts each conn onto its own goroutine, so two concurrent `channel.new` calls announce concurrently — without it that is a data race on the counter and duplicate envelope ids on a value clients order by. `reg.Get` is called before the fan-out, so no registry lock is ever held across a `Push`. `Create` → `Save` → `Get` is three acquisitions and not atomic; the only reachable interleaving is a concurrent delete, which yields a `Get` miss the design handles by skipping the push. No goroutine is spawned, so none can leak; a cancelled daemon ctx makes `ActiveConns` answer empty and returns the loop early.
- **[Threat model alignment] No findings against `docs/protocol-mobile.md` § Security model.** *Relay operator MITM (threat 3):* the payload rides inside the Noise-sealed `noise_msg`, so the relay sees one more opaque frame. It learns that a frame exists on this server-id and when — a traffic-analysis signal the model already accepts for every existing push, and one this frame makes no worse. *Server-id race (threat 2):* an attacker who claims a server-id cannot complete Noise_IK without the binary's static private key, so it never becomes a session, never appears in `ActiveConns`, and is never a fan-out target. The `!c.Interactive` gate is a capability filter applied on top of an already-authenticated conn, not the authentication itself — which is why the gate alone is sufficient here. *Prompt injection (threat 1):* not applicable; no value on this path becomes `claude` input.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-07
