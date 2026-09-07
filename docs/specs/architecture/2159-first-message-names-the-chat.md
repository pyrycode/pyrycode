# #2159 — a new chat names itself from its first message

A `send_message` accepted for a conversation whose stored `Name` is nil derives a
short title from the message text, stores it, and pushes the updated row to every
interactive-capable client through #2156's existing `conversation_updated`
emitter.

## Files read

- `internal/relay/handlers/send_message.go` → `SendMessage`, `Enqueuer`,
  `AttachmentResolver`, `composeAttachmentPrompt` — the hook point is between the
  non-zero `EnqueueDelivery` and `replyAck`; the composed prompt is what claude
  gets and is explicitly *not* what the title is cut from.
- `internal/relay/handlers/rename_conversation.go` → `ConversationRenamer`,
  `RenameConversation` — the template for the write: a consumer-side interface
  `*conversations.Registry` satisfies structurally, one locked `Update` that both
  mutates and snapshots the reply, a title captured in a local, the eager
  best-effort `Save`, and the `WorkspaceLabel`-outside-the-callback rule its
  interface doc records.
- `internal/relay/handlers/rename_workspace.go` → `WorkspaceAnnouncer`,
  `RenameWorkspace` — the shape of a push seam declared as a bare func type in
  the handler package, returning nothing so a fan-out can never fail the
  operation, and nil-valid so every pre-existing test keeps compiling.
- `internal/relay/handlers/list_conversations.go` → `workspaceLabelFor`,
  `workspaceLabelReader` — the one projection rule for the nullable
  `workspace_label` field: presence comes from the accessor's second return, never
  from a `label != ""` compare.
- `internal/relay/handlers/promote_conversation.go` → `PromoteConversation` — the
  alternative read-back shape (`Update` then `Get`), rejected below.
- `internal/conversations/registry.go` → `Update` — `fn` runs under `r.mu` and
  MUST NOT call any `Registry` method, and MUST NOT retain the `*Conversation`.
- `cmd/pyry/conversation_update_v2.go` → `conversationUpdateEmitterV2`,
  `announce` — the emitter this ticket reuses: interactive gate, per-emitter
  envelope counter, torn-down-conn tolerance, no `in_reply_to`. Its `announce`
  takes the payload and nothing else, so there is no requester to exclude.
- `cmd/pyry/relay.go` → `startRelayV2` — the handler table is a field of the
  literal passed to `NewV2SessionManager`, so no emitter exists at the
  `TypeSendMessage` entry; `announceConversation` is a *named return* of this
  function, assigned after `mgr` and before `mgr.Run`.
- `internal/protocol/conversations_write.go` → `ConversationUpdatedPayload` — the
  record's six fields, `workspace_label` nullable but never omitted.
- `docs/knowledge/features/conversations-registry-crud.md` § `WorkspaceLabel` —
  #2210's two producers each hit the `Update`-callback deadlock; a security review
  classed it as a client-triggerable denial of service, not just a bug, because a
  goroutine parked holding `r.mu` stalls every registry consumer.
- `docs/knowledge/features/relay-package.md` § handlers — the sub-package imports
  no `internal/sessions` / `internal/msgqueue` / `internal/relay`; every seam is a
  consumer-declared interface. This ticket adds the sixth.
- `docs/protocol-mobile.md` § v2 type table, `conversation_updated` row — the
  dual-producer prose #2156 wrote, which this ticket extends.

## Context

Desktop renders a nil name as `Untitled`, so every chat started from the desktop
sits in the sidebar as an anonymous row until someone renames it by hand. The
mobile conversations design settled server-side auto-naming on 2026-05-08 and the
daemon never built it: `CreateConversation` passes a nil name straight through and
`SendMessage` never touches the conversations registry at all.

The client side needs nothing — desktop re-lists on any unsolicited
`conversation_updated`, and its "Save as channel" dialog seeds from the row's
displayed title, so the design's default-suggestion rule falls out of the stored
name.

This does not deserve an ADR. It implements a decision already recorded in
pyrycode-mobile's `347-remote-create-discussion.md`, and it introduces no new
mechanism: the emitter, the interactive gate, the registry write shape and the
label projection all exist and are reused verbatim.

### Sizing note — two boundary lines are exceeded, deliberately

Measured against the one-ticket boundary, two lines trip and four hold. Split
depth is clean (no parent, no grandparent), so a split was available and was
rejected on the merits below rather than blocked by the depth gate.

- **Consumer call sites: 14, ceiling 10.** Widening `SendMessage` touches 1
  production consumer (`cmd/pyry/relay.go`) and 13 in-package constructions in
  `send_message_test.go`.
- **Total written work: ~910 lines, ceiling 800.** ~500 of code, tests and doc
  rows — the refiner's estimate, and unchanged by this design — plus a 408-line
  plan, of which ~90 lines are the `security-sensitive` label's mandated review
  section.

Not split, because **the floor rule wins over the ceiling** and every available
seam fails the floor:

- *Signature widening / use of the new parameters.* The first child has no
  deliverable anything outside the pair consumes, and the package would not
  compile between them, so neither half could be verified on its own. #2038
  widened this same signature the same way inside one ticket.
- *Derivation / everything else.* `deriveConversationName`'s only consumer is its
  sibling — the textbook one-consumer slice the floor rule names.
- *Store the name / push the frame.* The only floor-legal cut, and it makes
  things worse on every axis the ceiling protects: each child re-pays the whole
  14-site cascade, so the tripped line is paid twice rather than halved; and each
  child needs its own mandated security review, so the tripped line count grows
  rather than shrinks. It also strands the user story — a stored name nobody is
  told about is half of what the operator asked for.

Four lines hold with room: 3 production files (ceiling 5), 2 new exported types
(ceiling 5), 4 acceptance criteria (ceiling 5), no state machine and so no reject
branches. The 13 test edits are a single trailing-argument insertion each, in the
file this ticket is already extending.

## Design

Two new symbols in a new file, `internal/relay/handlers/autoname.go`, plus a
widened `SendMessage` and one wiring line.

### The derivation — `deriveConversationName(text string) (string, bool)`

Pure, total, no I/O. Contract:

1. Normalise: collapse every run of whitespace (newlines included) to one space
   and trim. `strings.Fields` + `strings.Join` is exactly this rule.
2. An empty normalisation yields `("", false)` — the attachment-only message. The
   caller writes nothing and pushes nothing.
3. Within `maxAutoNameRunes` (40) runes: the normalisation is the title, verbatim,
   with no ellipsis. Exactly 40 runes is the inclusive boundary.
4. Over it: take whole words while the accumulation stays within 40 runes, then
   append `…`. If the first word alone exceeds 40 runes, cut *that word* at 40
   runes and append `…`.

The ellipsis is appended **after** the 40-rune cut, so a truncated title is 41
runes on the wire and the 40-rune bound describes the retained text. It is
appended only when something was dropped, which is why case 3 is a separate arm
rather than a formatting rule that happens to agree.

Rune-counted throughout (`utf8.RuneCountInString`, and a `[]rune` slice for the
long-word cut) — a byte cut could split a multi-byte rune and put U+FFFD in a
display name. `internal/turnbridge`'s `truncate` is rune-safe but has no
word-boundary logic and lives in another package; this is written fresh beside
its consumer rather than exported across a boundary for one caller.

A first message that is a slash command names the chat after the command
(`/compact`). Accepted, no special case: the operator can see what the chat is.

### The write — `autoNameConversation`

An unexported step in the same file, called by `SendMessage` after a non-zero
`EnqueueDelivery` and before `replyAck`. It consumes two new exported seams:

```go
type ConversationAutoNamer interface {
	Update(id conversations.ConversationID, fn func(*conversations.Conversation)) bool
	Save(path string) error
	WorkspaceLabel(cwd string) (string, bool)
}

type ConversationAnnouncer func(p protocol.ConversationUpdatedPayload)
```

`ConversationAutoNamer` is method-set-identical to `ConversationRenamer` and is
still declared separately, matching the package's one-narrow-interface-per-handler
convention (`ConversationPromoter`, `ConversationArchiver` and `ConversationDeleter`
already overlap each other): the doc comment carries this consumer's own
"writes only over a nil name" contract, which the rename interface must not claim.
`*conversations.Registry` satisfies it structurally, no adapter.

`ConversationAnnouncer` mirrors `WorkspaceAnnouncer` in shape and in contract —
returns nothing, nil is valid and means "no fan-out" — but takes **no
`excludeConnID`**. The sender is pushed too, and that is the point: its reply is
an `ack` carrying no record, so excluding it would leave the one client that
caused the change as the only one that does not learn of it. This is also why the
type matches `conversationUpdateEmitterV2.announce`'s signature exactly, with no
adapting closure.

Order of operations, each step load-bearing:

1. `reg == nil` → return. Fail-closed for a caller with no registry leg, and the
   shape all 13 pre-existing tests take.
2. Derive. `!ok` → return, having touched nothing.
3. One `reg.Update`. Inside the callback: if `cv.Name != nil`, return without
   mutating — this is the whole of the never-overwrite rule, and it is inside the
   lock so a concurrent `rename_conversation` cannot land between the check and
   the write. Otherwise set `cv.Name = &title` (a local; never the callback's
   `*Conversation`, which a later `Create` may reallocate), capture `cv.Cwd`, and
   snapshot the `ConversationUpdatedPayload` from the row's own fields.
4. `WorkspaceLabel` is read **after `Update` returns**, keyed by the cwd captured
   in step 3. Not in the callback: `Update` holds `r.mu` for the callback's
   duration and `WorkspaceLabel` takes the same non-reentrant lock, so a read
   there deadlocks the daemon on an ordinary message.
5. Eager best-effort `Save`, exactly as `RenameConversation` treats its own.
6. Announce, if non-nil.

The snapshot-inside-the-callback shape is chosen over `PromoteConversation`'s
`Update`-then-`Get` read-back: one lock acquisition instead of two, no
vanished-between-calls arm to specify, and the pushed record is then provably the
row as it stood at the instant the name landed. `last_used_at` is carried through
from the row unchanged — auto-naming is a metadata write, not a "use", the same
call `rename_conversation` makes.

### The widened signature

```go
func SendMessage(router SessionRouter, queue Enqueuer, resolve AttachmentResolver,
	reg ConversationAutoNamer, registryPath string, announce ConversationAnnouncer,
	logger *slog.Logger) dispatch.Handler
```

Positional, with `logger` still last — the shape #2038 and #2092 already widened
this signature into. The derivation reads `p.Text`, never
`composeAttachmentPrompt`'s output: the composed prompt names on-host paths, and a
title cut from it would put a host filesystem path in a display name pushed to
every paired client.

### Wiring

At the `protocol.TypeSendMessage` entry of `startRelayV2`'s handler table, with
`w.convReg` and `resolveConversationsRegistryPath(w.instanceName)`. The announcer
is a nil-guarded closure over `announceConversation` — this function's own **named
return**, which #2156 assigns from `newConversationUpdateEmitterV2(mgr, ctx, logger).announce`
after `mgr` exists and before `mgr.Run` starts. So there is no new hook variable
and no second emitter: #2209's ordering problem is already solved here by a
variable that is in scope at the table. The nil guard covers the window between
the table literal and that assignment — unreachable in practice, since no frame
can dispatch until `mgr.Run` starts, but a hook read from a dispatch goroutine is
not a place to rely on it.

## Concurrency model

No goroutines are added. Everything runs synchronously on the `appFrameWorker`
goroutine already servicing the frame, and every blocking step is bounded there:
`Update` takes a leaf mutex around an in-memory scan, `Save` is one small file
write, and `announce` snapshots the conns under the manager's lock and enqueues
without blocking.

Two orderings carry correctness:

- **The nil check and the write are one locked mutation.** A concurrent
  `rename_conversation` for the same id either lands entirely before (and the
  callback sees a non-nil name and declines) or entirely after (and overwrites the
  derived title, which is the operator's explicit intent winning). There is no
  interleaving in which both write.
- **`WorkspaceLabel` is never called from inside the callback.** `r.mu` is
  non-reentrant; a read there parks the goroutine forever holding the registry
  lock, stalling every other registry consumer — a denial of service reachable by
  one ordinary `send_message` frame.

`announce` is called after `Update` and `Save` return, so no registry lock is held
across the fan-out and there is no lock order between `r.mu` and the manager's
`pushMu` to reason about.

## Error handling

| Failure | Behaviour |
|---|---|
| Text normalises to empty | No write, no push, no log line. Not an error — an attachment-only message is a real message. |
| `Update` miss (row deleted between enqueue and here) | Return silently. Nothing to name; the message is already queued and the drain owns it. |
| Row already named | Return silently. The common steady-state path — every message after the first. |
| `Save` fails | Logged at Error with the derived name's own event and an `err` field. Non-fatal: the name is live in memory and durability is best-effort, as it is for create/rename/promote/archive. |
| `announce` nil, or a conn torn down mid-push | Nothing. The seam returns nothing by contract, and the emitter already tolerates a dead conn and logs it at Debug. |
| Any of the above | The `ack` is unchanged and is sent regardless — auto-naming is a side effect of an accepted message, never a reason to refuse one. |

A rejected send — unknown conversation, unbound session, over-bound attachment
list, unresolvable attachment, full backlog — returns before the hook point, so it
writes no name by construction rather than by a guard.

### Logging

One new event, `send_message.autonamed`, carrying `event`, `conn_id` and
`conversation_id`. `conn_id` is daemon-minted and every other branch of this
handler carries it; nothing else is added.

The `Save` failure is logged at Error under that **same** event name with an
`err` field, rather than as a second `…persist_failed` event. That deviates from
the five sibling registry writers deliberately, to keep AC 4's "one new event"
literally true: the event is "this conversation was auto-named", and the level
plus the `err` field say whether the write also reached disk. `err` is a
filesystem error naming the daemon's own registry path — the one field on that
line safe to log.

Never logged, on any branch: `payload.Text` (unchanged from before this ticket)
and the derived title, which is a prefix of that text and therefore the same
untrusted user content. The pushed record carries the title because that is the
whole point of the frame; the log record has no such need.

## Testing strategy

`internal/relay/handlers/autoname_test.go` — a table test over
`deriveConversationName`, covering every case AC 1 enumerates plus the arms that
distinguish a correct implementation from a plausible one:

- empty string; whitespace-only; a tab/newline-only string
- embedded newlines collapsed to single spaces, and leading/trailing trimmed
- a run of several spaces collapsed to one
- exactly 40 runes → verbatim, **no ellipsis** (the arm that catches an
  off-by-one that always truncates)
- 41 runes over several words → cut at a word boundary, ellipsis present
- one word longer than 40 runes → cut mid-word at exactly 40 runes + ellipsis
- a first word of exactly 40 runes followed by more → the word alone + ellipsis
- multi-byte runes (CJK, an emoji) → counted as runes, never split mid-rune;
  a 40-rune multi-byte string survives whole
- a slash command → named after the command, no special case

`internal/relay/handlers/send_message_test.go` — new tests beside the existing 13:

- an accepted send on a nil-name row stores the derived title, persists it (read
  back off disk), and announces exactly once with the stored name, the row's
  `workspace_label`, an unchanged `last_used_at`, and no `in_reply_to`
- a labelled workspace's row pushes the label; an unlabelled one pushes `null`
- a row that already has a name is not renamed and announces nothing, on a
  message that would derive a different title
- a rejected send writes no name and announces nothing — a table over the
  backlog-full, unknown-conversation and too-many-attachments arms
- an attachment-only message (empty text, one attachment) writes no name and
  announces nothing
- a message with both text and attachments names the chat from the text alone —
  the assertion that the derivation does not read the composed prompt
- the `ack` is still sent, and unchanged, with a nil registry and with a nil
  announcer

The 13 existing constructions take `nil, "", nil` for the new parameters, which is
the no-registry-leg shape and leaves their assertions untouched.

Gate: `go test -race ./internal/relay/handlers/... ./cmd/pyry/...`, `go vet ./...`,
`go build ./cmd/pyry`.

## Docs

`docs/protocol-mobile.md`'s `conversation_updated` row names auto-naming as a
second unsolicited producer beside the host-side create. `relay_guard_test.go`'s
`excludedTypes` prose enumerates that type's producers and calls the host-side
create its only push; the sentence is updated so it does not go stale silently —
no assertion moves either way.

## Open questions

- **Does the push need to exclude the sender's conn?** Resolved at design time:
  no. `conversationUpdateEmitterV2.announce` takes no exclusion, and the sender's
  `ack` carries no record, so excluding it would starve the one client that
  caused the change.
- **Snapshot-in-callback or `Get` read-back?** Resolved at design time in favour
  of the snapshot; see § Design for the three reasons.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No finding, and the reason is not that the value is
  trusted. `payload.Text` is remote-authored by a paired phone, and this ticket
  moves a prefix of it across a boundary it did not previously cross: into
  persisted registry state and onto a frame broadcast to every paired client.
  That is not a *new* class of value in that field — `RenameConversation` already
  stores an arbitrary remote string as `Conversation.Name` verbatim with only an
  empty-check, and with **no length bound at all**. The derived title is bounded
  at 41 runes, so this path admits strictly less than the field already admits.
  UTF-8 validity is inherited rather than checked: `encoding/json` substitutes
  U+FFFD for invalid bytes and unpaired surrogates decoding into a Go string, so
  `p.Text` is always valid UTF-8 by the time `deriveConversationName` slices it —
  which is what makes the `[]rune` cut safe rather than merely conventional.
- **[Trust boundaries — control characters]** No finding, but the property is an
  *absent sink*, not a filter, so record it. `strings.Fields` splits on
  `unicode.IsSpace`, which does not include ESC (U+001B), so an ANSI escape
  sequence in a first message survives into the stored title. Checked every sink:
  the title is never logged (AC 4), never written to a terminal by the daemon —
  no CLI verb prints `Conversation.Name`, `pyry channel new` prints the bare id —
  and reaches only JSON on disk and JSON on the wire, where a client renders it as
  UI text and not as terminal output. A future CLI verb that lists conversation
  names to a TTY re-opens this for `rename_conversation` and this path alike, and
  is where a sanitiser would belong; it does not belong here, where it would
  filter one of two writers of the same field.
- **[Tokens, secrets, credentials]** N/A by design — this path handles no
  credential material, mints nothing, and reads no key. The registry rows it
  touches carry ids, a cwd, flags and a display name.
- **[File operations]** No finding. The one filesystem operation is
  `reg.Save(registryPath)`, whose path is daemon-derived from
  `resolveConversationsRegistryPath(w.instanceName)`; no client byte reaches it.
  The derived title is file *content*, never a path component, never joined,
  stat-ed or opened. Atomicity of the write is `Registry.Save`'s pre-existing
  property and is unchanged.
- **[Subprocess / external command execution]** No finding. The title never
  reaches an argv or an environment variable. `p.Text` itself already reaches the
  supervised child's stdin through the drain's `WriteUserTurn`, unchanged by this
  ticket, and the title is derived from it rather than added to that path.
- **[Cryptographic primitives]** N/A — no randomness, no comparison against a
  secret, no key material on this path.
- **[Network & I/O — push amplification]** No finding, and this is the category
  that most deserved the check. One inbound `send_message` fans out one frame per
  interactive conn, which is an amplification shape. It is bounded by the
  never-overwrite rule to **at most once per conversation, for the lifetime of
  that conversation**: the second message finds a non-nil `Name` inside the
  locked callback and announces nothing, so a client cannot loop the fan-out by
  sending repeatedly. Amplifying past that requires creating N conversations,
  which `create_conversation` already costs a minted claude session each. Frame
  size is bounded too: one row, whose title is at most 41 runes ≈ 164 bytes at
  4 bytes/rune — three orders of magnitude under the 65519-byte application
  envelope cap.
- **[Errors, logs, telemetry]** No finding. `payload.Text` remains never-logged
  on every branch, and the derived title is never logged **because it is a prefix
  of that text** — the same untrusted user content under a different name, which
  is the specific mistake this category exists to catch. The single new event
  carries `event`, `conn_id` and `conversation_id`, all daemon-minted or opaque
  ids already logged by every sibling branch. The one `err` field is a filesystem
  error naming the daemon's own registry path.
- **[Concurrency]** No finding, and two hazards were actively designed against.
  (a) `WorkspaceLabel` is read **after** `Update` returns, keyed by a cwd captured
  in the callback. Reading it inside would take `r.mu` re-entrantly and park the
  goroutine forever holding the registry lock — every registry consumer stalled,
  a denial of service reachable by one ordinary `send_message` frame, which is how
  `docs/knowledge/features/conversations-registry-crud.md` classes the same
  mistake #2210's two producers made. (b) The nil-check and the write are one
  locked mutation, so a concurrent `rename_conversation` cannot interleave between
  them; whichever verb reaches the lock second sees the other's result, and an
  explicit rename arriving second wins, which is the intended precedence. No
  goroutine is spawned, so there is no lifecycle to leak. No lock is held across
  the fan-out, so `r.mu` and the manager's `pushMu` never nest and there is no
  lock order to state.
- **[Concurrency — unrelated-write interaction]** No finding. `Registry.Promote`
  refuses `ErrPromotionNameInUse` when another **promoted** row holds the same
  name; auto-naming writes only to rows it finds unnamed, which are by
  construction not yet promoted, and the check skips unpromoted rows. So an
  auto-named title can never make a later promotion fail.
- **[Threat model alignment]** Addressed. Reachability is the authenticated,
  paired Noise session — the frame is decrypted under the session's receive state
  before dispatch, the same gate `send_message` already sits behind, so no new
  inbound gate is added (matching `set_system_prompt` and `rename_conversation`).
  The outbound push is interactive-capability-gated inside the emitter (#607).
  The audience question is settled the way #2209 and #2210 settled it: every
  recipient is inside the same paired-session trust boundary as the sender, and
  each could already read the stored name off its next `list_conversations`, so
  the push changes *when* a client learns the title, not *whether* it may. The
  on-disk exposure is likewise strictly smaller than what already exists — the
  full message text is in claude's JSONL transcript on the same host.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-08

## Revisions

### 2026-09-08 — the auto-naming step moved to AFTER the ack

**Driven by:** the verification gate's regression report on PR #2221 — the
stream-json e2e specs `TestRelayV2_StreamModalPermissionRoundTrip` (three of four
subtests) and `TestRelayV2_StreamNewSessionRotatesAndRestartsFresh` passed on the
merge-base and failed on the branch.

**What was wrong.** § Design placed the step "after a non-zero `EnqueueDelivery`
and before `replyAck`", following the ticket's Technical Notes, and § Concurrency
argued the step was bounded on the `appFrameWorker` goroutine. Bounded it is —
about 4 ms, dominated by `Save`'s fsync — but bounded is not free, and the
argument missed what that window is racing. `EnqueueDelivery` hands the turn to
the drain, which runs on **its own goroutine** and can deliver it, get the child
to raise a permission approval, and have that approval broadcast as `modal_shown`
while the handler is still fsyncing. The sender then receives `modal_shown`
*before* the `ack` it is waiting for.

That is a real reordering of the wire, not a test artefact: nothing has ever
ordered a daemon push against a reply, and both specs record their phone's
receive order. Instrumenting the failing subtest showed the phone receiving
`queue_state, modal_shown, queue_state, conversation_updated, ack` — the
ack-await loop consumed the run's only `modal_shown` and the later wait for one
timed out. The daemon itself was healthy throughout: a SIGQUIT dump of the stuck
process showed the approval correctly parked in `permbridge.Pending.Await` with
no goroutine blocked on the registry.

**The new contract.** `replyAck` is called first and its error is held in a
local; `autoNameConversation` runs after it; the held error is returned. Nothing
else about the step moves — same goroutine, same synchronous call, same order of
operations inside it, same reject branches returning before it.

The reasoning that makes this the right placement rather than a workaround:
acceptance is established by the enqueue, so by the time the ack is due, the
message is queued and the drain owns it. Everything after that is a side effect
on registry metadata, and the sender's round-trip has no reason to wait on an
fsync or on a fan-out aimed at *other* clients.

**What it costs.** Nothing in the acceptance criteria. AC 2 keys naming on "a
`send_message` is accepted (`EnqueueDelivery` returned non-zero)", which is
unchanged; AC 3's "the `ack` is unchanged and is sent whether or not the push
succeeded" is satisfied more strongly than before, since the ack now precedes the
push outright. One behaviour genuinely changes: a conversation is now named even
when the ack's own write fails. That is deliberate — a conn whose ack failed is
on its way out, and the message it queued still runs, so its conversation still
deserves its name.

**Also revised by this entry:** § Concurrency's claim that "every blocking step
is bounded there" was true but not sufficient, and § Error handling's row for the
ack now reads "the ack precedes all of this" rather than "the ack is sent
regardless". The `## Security review` verdict is unaffected — no finding in it
depends on the step's position relative to the ack, and moving it neither widens
the trust boundary nor changes what reaches disk, a log or the wire.
