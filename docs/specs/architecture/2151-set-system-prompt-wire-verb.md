# #2151 — `set_system_prompt`: a client sets and clears a conversation's system prompt

A phone → binary write verb, keyed by conversation, that stores or clears the durable
per-conversation system prompt through the validating registry door #2149 built, persists it
eagerly, and replies with the reused `conversation_updated` record. It touches no session state:
#2150 already installed the read at the pool's spawn funnel, so a registry write is the whole
mechanism.

## Files read

Production surface:

- `internal/relay/handlers/archive_conversation.go` → `ArchiveConversation`, `ConversationArchiver` —
  the **structural** template. Its registry op (`SetArchived`) returns no record either, so it
  already solves this ticket's shape: single-field mutator → follow-up `Get` for the reply snapshot
  → eager `Save` → `conversation_updated`. Its comment on the `Get` miss documents the only
  observable interleaving (a concurrent `delete_conversation`) and the not_found resolution.
- `internal/relay/handlers/change_workspace.go` → `ChangeWorkspace` — the **discipline** template
  the ticket names: static reject messages, the two logging divergences (never the decode error,
  never the offending value), best-effort `Save`, `replyError(..., retryable=false)`. What is *not*
  inherited: `WorkspaceResolver`, `ErrWorkspaceRejected`, the `cmd/pyry` adapter — validation lives
  in the registry here, so there is no path to confine.
- `internal/conversations/registry.go` → `SetSystemPrompt`, `MaxSystemPromptBytes`,
  `ErrSystemPromptTooLong`, `ErrSystemPromptInvalidUTF8`, `ErrConversationNotFound`, `Get`, `Save` —
  the validating door. Takes the tri-state as `*string`, returns **error only**, never the record,
  and never calls `Save`. Validation is value-first (length, then UTF-8) then identity; on any
  refusal the registry is untouched.
- `internal/conversations/conversation.go` → `Conversation` — the record whose `SystemPrompt` field
  is the one this verb writes, and whose other fields the "exactly one field changes" criterion
  pins.
- `internal/protocol/conversations_write.go` → `ChangeWorkspacePayload`, `ConversationUpdatedPayload`
  — the payload neighbourhood, and the reply record that deliberately gains **no** prompt field.
  Its own doc calls the frame broadcast to all phones on this server-id.
- `internal/protocol/codes.go` → `TypeChangeWorkspace`, `CodeProtocolMalformed`,
  `CodeConversationNotFound` — the constant home and the two error codes this verb maps to.
- `internal/protocol/envelope.go` → `inboundAppTypeSet` — the type set an inbound verb must join or
  the dispatcher refuses the frame.
- `cmd/pyry/relay.go` → `startRelayV2`'s `Handlers` map — the single wiring site (confirmed: no
  conversation verb is registered in `startRelay`).
- `internal/relay/handlers/register_push_token.go` → `replyError` — the shared package helper that
  emits a `protocol.error` frame with a code, a message and a retryable flag.

Test surface:

- `internal/relay/handlers/change_workspace_test.go` → `newChangeWSConn`, `changeWSRequest`,
  `assertChangeWSEnvelopeShape` — the per-verb conn/request/shape helper trio each handler test
  defines for itself.
- `internal/relay/handlers/delete_conversation_test.go` → `findLogRecord` — the package-shared
  helper that parses a `slog.NewJSONHandler` capture and returns one record by `event`.
- `internal/protocol/compat_test.go` → `TestIsKnownAppType`'s `allTypes`, and the two `all` lists in
  the partition tests. **One holds a hardcoded `23`** that must move to `24`; the other count is
  derived. A new type that misses any of the three reddens the package.
- `cmd/pyry/relay_guard_test.go` → `inboundTypes` — every inbound verb must be classified here
  (`"map-dispatched"`) or the guard's coverage tie fails.
- `internal/sessions/pool_system_prompt_test.go` → `TestPool_ConversationPrompt_TriState` — pins the
  exact tri-state semantics (`nil` and `""` both spawn the constant only; text appends) that this
  verb's stored value feeds. It is the contract the reply-side of this ticket must not disturb.

Knowledge:

- `docs/knowledge/features/conversations-registry.md` § `SetSystemPrompt (#2149)` — three things
  that shape this handler: (1) the single `*string` spans all three states *deliberately*, because
  a `prompt string` signature would push the clear path outside the validated door; (2) validation
  is length-then-UTF-8, so a value that is both over-length and invalid UTF-8 returns
  `ErrSystemPromptTooLong` — pinned by a registry test, so the wire code for that input is
  determined, not a coin flip; (3) the sentinels are **static and returned naked** — no refusal
  path interpolates the value, its length, or the id.
- `docs/knowledge/features/protocol-package-types-conversations-write-payloads.md` — the payload
  neighbourhood's conventions: pointer fields carry no `omitempty` so `null` round-trips
  byte-equivalently, and a new verb gets its own payload type rather than reusing a sibling's
  (semantic coupling / false dependency).
- `docs/protocol-mobile.md` § *Application message types* — verbs are documented as one table row;
  § *Application-envelope size cap* (65519 bytes) is what `MaxSystemPromptBytes` was sized against.

## Context

#2149 added the durable `Conversation.SystemPrompt` field and its validating setter. #2150 wired the
read: `Pool.refreshSystemPrompt`, called from `Pool.Activate`, re-reads the stored prompt out of the
registry on every first spawn and every re-activate, and returns early on a session already in
`stateActive`. Both halves exist; neither is reachable from a client. This slice is the write verb,
and with it an operator can give a channel its own voice without editing a file on the host.

Two consequences of #2150 having landed first, and they are what make this slice small:

- **The handler touches no session state at all.** The value's route to a running child is the
  registry, re-read at the pool's own spawn funnel. Nothing here needs a pool, a runner, or an argv
  recomposition — and `Runner.SetSpawnArgs`, which an earlier revision of the ticket named, would be
  actively wrong: it is what `Pool.UpdateSettings` pairs with a *restart*.
- **"Leaves a running session alone" is a structural property, not a behaviour to implement.** The
  handler cannot disturb a live child because it holds no seam through which it could. The design
  keeps it that way by declaring a three-method consumer interface over the conversations registry
  and nothing else.

No ADR is warranted. The verb adopts two existing patterns wholesale (`archive_conversation`'s
mutator-then-`Get` shape, `change_workspace`'s untrusted-input logging discipline) and introduces no
new architectural concept. The one decision worth recording beyond this spec — that a
conversation-keyed write verb is the right shape for conversation state, as against the
session-keyed `set_session_settings` — is already stated in the ticket body and in
`conversations-registry.md`.

### Size

Over the size-S table's 800-line line by roughly 19% (~950 lines: ~170 handler, ~420 test, ~40
across the four registration sites and their test lists, ~20 docs, ~300 spec), stated rather than
hidden. Every other line of the table is clear: **5** production source files (`codes.go`,
`envelope.go`, `conversations_write.go`, the new `set_system_prompt.go`, `relay.go`), **2** new
exported types, **0** consumer call sites needing simultaneous update (the verb is purely additive —
no existing symbol changes signature), **4** reject branches, **5** acceptance criteria.

No split is available, and the sizing guide's floor is why. A payload-type slice has exactly one
consumer, the handler. A docs-only tail is the cut the #1720 split is on record for regretting.
Set-and-clear is one deliverable, not two: `SetSystemPrompt` spans both through a single `*string`,
and cutting there would ship a verb that can write a prompt nobody can remove. When the floor and
the ceiling disagree the floor wins — the ceiling protects a budget, the floor protects
verifiability.

### File-overlap check

`git fetch origin --prune` then a per-branch diff against `origin/main` across every
`origin/feature/<n>` branch. One nominal hit: `feature/449` touches `internal/protocol/codes.go`.
It is not in-flight work — issue #449 closed 2026-05-17 and its branch's tip commit is from that
date. No blocker set, no rework routing.

## Design

### 1. Wire type — `internal/protocol/codes.go`

`TypeSetSystemPrompt = "set_system_prompt"`, added beside `TypeChangeWorkspace` in the conversation
write-verb group, with the group's usual doc comment: phone → binary `dispatch.Route` write verb,
an `inboundAppTypeSet` member rather than a v2 control frame, replying with the reused
`conversation_updated` record so there is no new reply type. The string is pinned by the ticket
because it is a cross-repo contract (pyrycode-desktop#1078 sends it; #2152's read half is named
against it).

### 2. Payload — `internal/protocol/conversations_write.go`

```go
type SetSystemPromptPayload struct {
	ConversationID string  `json:"conversation_id"`
	SystemPrompt   *string `json:"system_prompt"`
}
```

The pointer is the whole point: it is what makes "clear" expressible distinctly from "set it to the
empty string", which a `string` field could not do. Three states, matching
`Registry.SetSystemPrompt`'s argument exactly:

| On the wire | Decodes to | Stored state |
|---|---|---|
| `"system_prompt": null` (or the key absent) | `nil` | cleared — the row returns to spawning exactly as it does today |
| `"system_prompt": ""` | non-nil `""` | explicitly empty |
| `"system_prompt": "<text>"` | non-nil text | stored verbatim, after validation |

No `omitempty`, matching every sibling pointer field in the file: with it, a nil pointer would drop
the key entirely and break byte-equivalent round-trip. `null` and an absent key are indistinguishable
after decode and both mean *clear* — stated in the type's doc comment so no reader has to rediscover
it.

Deliberately **not** a reuse of `ChangeWorkspacePayload` or `ArchiveConversationPayload`, per the
file's established rationale (semantic coupling / false dependency).

`ConversationUpdatedPayload` gains **no** field. Its six fields stay as they are, so the reply
confirms the write without carrying the value. Two reasons: the record's own doc describes the frame
as broadcast to all phones on this server-id, so hanging up to 8192 bytes of operator text on it
would widen the audience for a value only the requester asked about; and reading the prompt back is
#2152, which deliberately does not echo the text either. A projection type that omits a field is a
structural leak barrier — the reply *cannot* carry the prompt, rather than merely not doing so
today.

### 3. Envelope registration — `internal/protocol/envelope.go`

`TypeSetSystemPrompt: true` in `inboundAppTypeSet`. Its three mirrors in
`internal/protocol/compat_test.go` gain the constant, and the hardcoded list-length literal in the
partition test moves from 23 to 24.

### 4. Handler — `internal/relay/handlers/set_system_prompt.go` (new)

Consumer-declared interface, three methods, mirroring `ConversationArchiver`'s shape exactly.
`*conversations.Registry` satisfies it structurally; no adapter:

```go
type ConversationSystemPromptSetter interface {
	SetSystemPrompt(id conversations.ConversationID, prompt *string) error
	Get(id conversations.ConversationID) (conversations.Conversation, bool)
	Save(path string) error
}
```

That this interface names no session, pool or runner surface **is** the design's answer to "leaves a
running session alone": the handler holds no seam through which a restart, a rotation or an argv
recomposition could be reached.

```go
func SetSystemPrompt(reg ConversationSystemPromptSetter, registryPath string, logger *slog.Logger) dispatch.Handler
```

Flow, mirroring `ArchiveConversation` step for step:

1. **Decode.** `json.Unmarshal` into `SetSystemPromptPayload`. Failure → `protocol.malformed`,
   non-retryable, fixed message.
2. **Write through the validating door.** `reg.SetSystemPrompt(id, p.SystemPrompt)`. This one call
   carries the length bound, the UTF-8 check and the existence check; the handler re-implements
   none of them. Non-nil error → map to a reply (§ *Error handling*) and return. On every refusal
   the registry is untouched, so nothing is persisted on any reject path — a property of the door,
   not of the handler, which is why the handler must not do any partial work before this call.
3. **Snapshot for the reply.** `reg.Get(id)`. A miss can only mean a concurrent
   `delete_conversation` landed between the two separately-locked ops; resolve it as not_found,
   exactly as `ArchiveConversation` does and for its stated reason — the write committed but the
   record is gone, so `conversation_updated` has no source and not_found is truthful for the row's
   current state.
4. **Eager persist.** `reg.Save(registryPath)`, best-effort: a failure is logged at Error and the
   handler continues, matching create / rename / delete / archive. The in-memory value is already
   live, and it is the in-memory registry that #2150's spawn-funnel read consults.
5. **Reply.** Marshal a `ConversationUpdatedPayload` built **from the stored record `cv`**, never
   from the request — `ID: string(cv.ID)` — and `c.Reply(ctx, env, protocol.TypeConversationUpdated,
   …)`.

`LastUsedAt` is not bumped: setting a prompt is a metadata edit, the same call the sibling verbs
make.

### 5. Wiring — `cmd/pyry/relay.go`

One entry in `startRelayV2`'s `Handlers` map, beside `TypeChangeWorkspace`:

```go
protocol.TypeSetSystemPrompt: handlers.SetSystemPrompt(w.convReg, resolveConversationsRegistryPath(w.instanceName), logger),
```

One site, not two — `startRelay` registers no conversation verb. `cmd/pyry/relay_guard_test.go`'s
`inboundTypes` gains `"TypeSetSystemPrompt": "map-dispatched"`.

### 6. Documentation — `docs/protocol-mobile.md`

A row in the § *Application message types* table, plus a short subsection covering the tri-state
payload, the reused reply, and the timing caveat the criterion asks for in as many words: **the new
value takes effect at the conversation's next session start, not before** — an operator who edits
the prompt and keeps typing sees no change until then. Surfacing that cost rather than hiding it is
Juhana's ruling; telling the operator *in the moment* is #2152's job and the client's
(pyrycode-desktop#1078).

## Concurrency model

No goroutines are spawned; the handler runs synchronously on the dispatcher's frame-handling path
and returns. Nothing to shut down.

Two separately-locked registry operations (`SetSystemPrompt`, then `Get`) with a gap between them,
inherited knowingly from `ArchiveConversation`. Each is internally atomic — `SetSystemPrompt`'s scan
and mutation are one critical section under `r.mu`, so there is no check-then-mutate TOCTOU *within*
the write. The gap admits exactly one interleaving with an observable effect, a concurrent
`delete_conversation` removing the row, which surfaces as `ok == false` and is handled as not_found.
A concurrent second `set_system_prompt` is last-writer-wins on a single field, which is the correct
semantics for an idempotent setter.

Folding both into one `Update` closure would close the gap but is rejected: it would route the write
around the validating door, which is the entire point of #2149's design (and `Update` is documented
as deliberately unvalidated). The lock is never held across `Save` or across the reply, so a slow
disk cannot stall another conversation's write.

`Get` returns a shallow copy that **shares** the stored `SystemPrompt` pointer with the registry.
That would be a live aliasing hazard if the reply projected the field — it does not, and cannot,
because `ConversationUpdatedPayload` has no such field.

## Error handling

Four reject branches, all non-retryable (`retryable=false` on `replyError`): re-issuing the same
frame fails identically, because none of them has a transient mode — no spawn, no mint, no
filesystem operation.

| Branch | Trigger | Code | Static message |
|---|---|---|---|
| malformed | payload will not JSON-decode | `protocol.malformed` | `malformed set_system_prompt payload` |
| too long | `> MaxSystemPromptBytes` (8192, inclusive bound) | `protocol.malformed` | `system prompt exceeds the maximum length` |
| invalid UTF-8 | `!utf8.ValidString` | `protocol.malformed` | `system prompt is not valid UTF-8` |
| not found | id matches no row, or the row vanished before the reply snapshot | `conversation.not_found` | `conversation not found` |

Sentinel mapping uses `errors.Is` on the three exported registry errors, with a **default arm that
fails closed**: an unrecognised non-nil error maps to `protocol.malformed` with the malformed
message. A future fourth sentinel therefore degrades to a clean non-retryable refusal rather than
falling through to a spurious success or a nil-record reply.

Note a determined ordering rather than a coin flip: a value that is both over-length and invalid
UTF-8 returns `ErrSystemPromptTooLong`, because the registry checks length first (an O(1) gate ahead
of a full scan of hostile input) and pins that order with its own test.

`Save` failure is the one non-refusal error: logged at Error, the reply still succeeds. Durability
is best-effort here exactly as it is for every sibling verb; the in-memory value is what the next
spawn reads.

### Logging discipline — the load-bearing divergence

**No log line on any branch carries a supplied byte.** Only `conn_id` (daemon-minted) and a
per-branch `event` string appear. This is stricter than *both* templates:
`change_workspace` and `archive_conversation` each log `conversation_id` as a structured field on
their non-malformed branches; this verb does not, on any branch, success included. The criterion
names the conversation id among the supplied bytes that must not reach the daemon log, and this is a
`security-sensitive` verb carrying untrusted operator text from a network-paired party.

Concretely, never logged: the prompt, its length, the conversation id, the decode error (Go's
`json.Unmarshal` errors can embed offending input bytes), and the registry sentinel. The sentinels
are static and safe to log, but a distinct `event` per branch carries the same diagnostic signal
with no per-sentinel judgment call, so the branch name is the discriminator instead of an error
string. Events: `set_system_prompt.malformed`, `.too_long`, `.invalid_utf8`, `.not_found`,
`.persist_failed`, `.applied`.

The single exception is `persist_failed`, which logs `err` — a filesystem error naming the registry
path, daemon-authored, the same exception every sibling verb makes.

On the wire, the only field derived from the request is the reply's `id`, and it is sourced from the
**stored record** (`cv.ID`), reached only after the id matched an existing row. It is registry-held
state that happens to equal what was supplied, not the supplied bytes echoed; `conversation_updated`
requires the field, and every sibling verb has the same posture. No reject path sends it — all four
reply with a fixed static string.

## Testing strategy

`internal/relay/handlers/set_system_prompt_test.go`, following the package's per-verb helper trio
(a fresh `dispatch.NewTestConn` + recv, a seeded temp-dir-backed registry, an envelope-shape
assertion) and reusing the package-shared `findLogRecord`.

The prompt and conversation-id fixtures are deliberately distinctive, unique strings that collide
with no other value in the test — a leak assertion that searches for a common substring proves
nothing.

- **Set — persists, replies, and disturbs nothing else.** Reply is `conversation_updated`,
  `in_reply_to`-correlated, `id` equal to the target. The reply's raw JSON contains **no**
  `system_prompt` key and not one byte of the prompt. The in-memory row holds the value; a fresh
  `conversations.Load` from disk holds it too (the eager `Save` landed). `LastUsedAt` is not bumped.
  Every other field of every row is compared against a full pre-call `List()` snapshot — the
  criterion's "exactly one field of exactly the conversation the frame names changes".
- **Tri-state, table-driven.** `null` → `nil`; `""` → non-nil empty; text → text; and a *clear after
  a set* returns the row to `nil`. Asserted on the stored `*string` (nil vs non-nil vs pointee), the
  only representation that can tell the three apart — and the exact discrimination
  `Pool.conversationPrompt` makes at the spawn funnel.
- **Boundary.** Exactly `MaxSystemPromptBytes` is accepted (inclusive); one byte more is refused.
  Both rows, so an off-by-one in either direction reddens.
- **Rejects, table-driven over all four branches.** Each asserts the reply is a `protocol.error`
  with the expected code, the exact static message and `retryable == false`; that the full `List()`
  snapshot is byte-identical to the pre-call one; and that the on-disk registry file is unchanged.
  A pre-seeded prompt on the target row makes "nothing is persisted" observable rather than
  vacuously true against an empty starting state.
- **No supplied byte in the log.** Every path — success and all four rejects — run against a
  `slog.NewJSONHandler` capture. Each asserts the branch's expected `event` record **exists** and
  carries `conn_id` *first* (so the leak assertion cannot pass against an empty buffer, which would
  be the vacuous way to be green), then that the whole captured buffer contains neither the prompt,
  nor the conversation id, nor the decode-error text.
- **Touches no session surface.** A recording fake implementing
  `ConversationSystemPromptSetter` asserts the exact call sequence on the success path —
  `SetSystemPrompt`, `Get`, `Save`, and nothing else — and that a reject path calls
  `SetSystemPrompt` alone. This is the checkable half of "no restart, no rotation, no argv
  recomposition, no interruption of an in-flight turn": the handler's whole interaction with the
  daemon is those three registry calls. The structural half is the constructor signature, which
  names no session surface to reach.
- **Registration.** The four registration sites are self-enforcing: `compat_test.go`'s three lists
  and its length literal, and `relay_guard_test.go`'s `inboundTypes` coverage tie. Adding the
  constant without them is red by construction, which is the intended proof.

Verification gate (§ B2): `go test -race` on `./internal/relay/handlers/...`,
`./internal/protocol/...`, `./internal/conversations/...` and `./cmd/pyry/...`, plus `go vet ./...`
and `go build ./cmd/pyry`. No live-claude gate: every criterion is provable at the handler and
registry level against the existing fakes, #2150 already carries the live proof that a stored prompt
reaches a real child, and #1029 set the precedent of filing `change_workspace`'s live gate as its
own ticket rather than inside the verb.

## Open questions

1. **Does the `docs/protocol-mobile.md` verb documentation want a dedicated subsection, or does the
   table row suffice?** Every sibling conversation verb is a table row only, but the criterion asks
   for the reply *and* the next-session-start timing, which does not fit a table cell. Resolve at
   the doc edit: a row plus the shortest subsection that states both.
2. **Should the reply's `id` be considered an echo of a supplied byte?** Resolved in this plan (§
   *Logging discipline*) — sourced from the stored record, sent only after the id matched an
   existing row, and contractually required by `conversation_updated`. Recorded here because it is
   the kind of blanket "nothing is echoed" claim that is worth stating a scope for rather than
   assuming, and because the security review re-examines it independently below.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. Exactly two untrusted fields cross the boundary, and one call
  is the gate for both: `Registry.SetSystemPrompt` validates the value (byte bound, then UTF-8) and
  resolves the identity (exact-match registry key, no path or argv construction). The handler adds
  no second opinion and does no work before that call, which is what makes "nothing is persisted on
  a reject" a property of the door rather than of handler discipline. Downstream, the stored value
  reaches a claude child only through `composeSystemPrompt`, which appends the operator's bytes
  *after* the daemon constant and has **no branch returning the operator's text alone** — so the
  constant cannot be displaced by any value this verb can store.
- **[Tokens, secrets, credentials]** No findings — no token, key or credential is read, minted or
  stored. The prompt is operator-authored plaintext at rest in `~/.pyry/conversations.json`, the
  same class as the conversation names and cwds already there, and `Registry.Save` already writes it
  at mode 0600 under a 0700 parent.
- **[File operations]** No findings, and the reason is structural rather than careful: this verb
  stores no path, which is precisely why it inherits none of #823's confinement half. The one
  filesystem operation is `reg.Save(registryPath)`, and `registryPath` is daemon-computed by
  `resolveConversationsRegistryPath` at wiring time — never supplied, never derived from the frame.
  `Registry.Save` is temp-file + chmod 0600 + fsync + rename, so an interrupted persist of a
  now-larger registry cannot leave a partial file; the rename is the commit point.
- **[Subprocess / external command execution]** No findings — the stored value never becomes argv.
  #2149 pinned that deliberately ("the prompt reaches claude through a file, never as a command-line
  value") and #2150 delivers it via `--append-system-prompt-file`. This verb changes only the bytes
  that file is composed from, so no shell, no `exec.Command` argument and no environment variable is
  reachable from the payload.
- **[Cryptographic primitives]** No findings — none introduced. Reachability is the authenticated,
  paired Noise session: `dispatchAppFrame` decrypts a frame under the session's receive state before
  dispatch, so only the paired phone reaches this handler. That is the gate every conversation write
  verb already has and it needs no new code; the interactive capability is **not** an inbound gate
  (`V2Session.interactive` is read only in the outbound `ActiveConns` fan-out), which is why #823
  carried no such criterion either.
- **[Network & I/O]** No findings. The value is bounded twice — the v2 application envelope at 65519
  bytes caps the frame before decode, `MaxSystemPromptBytes` at 8192 caps the field — and the length
  check is the O(1) first gate, so hostile input is refused before anything scans it. **OUT OF
  SCOPE:** there is no per-conversation write rate limit, matching every sibling conversation verb
  and behind authentication; if that is ever wrong it is wrong for the whole family, not for this
  verb.
- **[Error messages, logs, telemetry]** Four items, one of them the ticket's load-bearing
  requirement:
  - No findings on the log itself: no branch logs a supplied byte — not the prompt, not its length,
    not the conversation id, not the decode error, not the registry sentinel. Only `conn_id`
    (daemon-minted) and a per-branch `event`, with `persist_failed`'s `err` (a filesystem error
    naming the daemon's own registry path) as the single sibling-standard exception.
  - **SHOULD FIX (Phase B):** the leak test must assert the branch's expected `event` record
    **exists** before asserting the buffer contains no supplied byte. Absence-only is green against
    an empty buffer, which is the vacuous way to pass a leak test.
  - No findings on containment, and this was verified rather than assumed: `SystemPrompt` appears in
    **no** `internal/protocol` type — not `ConversationUpdatedPayload`, not `ConversationSummary` —
    and in no non-test file under `internal/relay` or `cmd/pyry`. After this ticket the prompt's only
    wire surface is inbound; a projection type that lacks the field cannot leak it. **OUT OF SCOPE:**
    #2152 opens the first outbound path and must re-derive this rather than inherit it.
  - No findings on the existence oracle: `conversation.not_found` does distinguish a real id from a
    fabricated one, but only for a party already authenticated to enumerate every conversation via
    `list_conversations`. Because the registry validates value before identity, an over-length or
    invalid-UTF-8 probe is refused *without the registry being consulted at all*, so a malformed
    value cannot be used to probe existence.
  - Noted trade-off, deliberate: dropping `conversation_id` from every log line also drops the audit
    trail for *which* conversation's standing instructions changed. The `set_system_prompt.applied`
    record still says that one did and over which `conn_id`. The criterion is explicit and is
    followed; recorded so the cost is visible rather than discovered later.
- **[Concurrency]** No findings, one gap named. `SetSystemPrompt` and `Get` are separately locked;
  the only interleaving with an observable effect is a concurrent `delete_conversation`, resolved as
  not_found for `ArchiveConversation`'s stated reason. Locks are never nested and never held across
  `Save` or across the reply, so `Save`'s documented `saveMu`→`r.mu` order is untouched — the handler
  holds neither when it calls. `Get` returns a shallow copy sharing the stored `SystemPrompt`
  pointer, which would be a live aliasing hazard if the reply projected the field; it does not, and
  structurally cannot. No goroutine is spawned, so there is nothing to leak.
- **[Concurrency — fail-open]** **SHOULD FIX (Phase B):** the sentinel mapping needs a default arm.
  Matching only the three known errors and falling through on anything else would send an unhandled
  refusal down the `Get`-and-reply path, replying `conversation_updated` for a write that never
  happened — a spurious success, the worst shape available. The default arm maps any unrecognised
  non-nil error to a non-retryable `protocol.malformed` and returns.
- **[Threat model alignment]** `docs/protocol-mobile.md` § *Security model* threat #1, prompt
  injection (`severity: high`, `mitigation: partial`), is the one this verb touches, and it touches
  it in a new way worth naming plainly. Until now, changing what a claude child is told required host
  access; this verb makes it remotely settable. The gate is the authenticated paired session — the
  same gate that already permits `send_message`, i.e. arbitrary user-role input to a claude holding
  full tool permissions — so there is **no privilege escalation**: a party who can already drive
  turns gains nothing qualitatively new by also setting standing text. The genuinely new property is
  **durability and invisibility**: unlike a turn, a system prompt survives daemon restarts, applies
  to every future session of that conversation including ones started later from the host CLI, and
  appears in no message history. A one-time compromise of a paired device therefore leaves something
  behind. **OUT OF SCOPE**, with owners already named: the mitigation is making the value readable so
  an operator can see what is set — #2152 (the read half) and pyrycode-desktop#1078 (surfacing it).
  Threats #2–#5 are transport- and process-level and are unchanged by an application verb.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-07
