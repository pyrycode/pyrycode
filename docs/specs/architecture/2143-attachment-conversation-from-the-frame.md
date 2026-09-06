# #2143 — File an upload under the conversation the client names

Ticket: [#2143](https://github.com/pyrycode/pyrycode/issues/2143) (`bug`, `size:s`,
`security-sensitive`, `needs-real-claude`). Parent #2098; siblings #2144 (e2e flow
proofs) and #2146 (refusing a mid-transfer conversation switch). Blocks #2083 and
[pyrycode-desktop#1076](https://github.com/pyrycode/pyrycode-desktop/issues/1076).

## Files read

- `internal/attachments/intake.go` → `Intake`, `NewIntake`, `Receive`,
  `resolveConversation`, `ErrNoConversation` — the whole surface this ticket
  reshapes; its doc blocks carry the structural property being retired.
- `internal/attachments/storage.go` → `EnsureDir`, `ResolvePath`, `ErrInvalidID` —
  `EnsureDir` runs `conversations.ValidID` on BOTH components before touching the
  filesystem, which is the remaining fail-closed backstop; `ResolvePath`'s
  precondition block holds a claim that this ticket falsifies.
- `internal/attachments/intake_test.go` → `newTestIntake`, `fixedConversation`,
  `completeChunk`, `boundUploadChunk`, `assertNoBannedStrings` — 14 of the 17
  simultaneous `Receive` edits live here; `fixedConversation` disappears with the
  resolver.
- `internal/attachments/registry_test.go` → `packageSentinels` — the
  sentinel-distinctness set `ErrNoConversation` must leave.
- `internal/relay/v2session_seams.go` → `AttachmentIntake`, `V2SessionConfig`'s
  `KnownConversation` and `HistoryPage` — `HistoryPage` is the exact
  caller's-precondition wording to copy, and `KnownConversation` is the seam the
  gate runs through. Both are already on `m.cfg`, so no new seam is minted.
- `internal/relay/v2session_attachment.go` → `handleAttachmentChunk`,
  `attachmentRejectFor`, `rejectInvalidChunk`, `attachmentReplyError` — the
  handler that gains the gate and the sentinel map that loses an arm.
- `internal/relay/v2session_attachment_request.go` → `handleRequestAttachment`,
  `rejectAttachmentRequest` — the ordering and the daemon-authored-reason shape to
  copy. Its steps 3-5 are gate → empty-id → resolve, two reasons behind one code.
- `internal/relay/v2session_attachment_test.go` → `fakeAttachmentIntake`,
  `attachmentCall`, the sentinel→code table — the relay-side double and the row
  that goes with the sentinel.
- `cmd/pyry/relay.go` → the `attachments.NewIntake` call and the
  `KnownConversation` closure over `w.convReg.Get` — the cursor resolver is
  deleted here and the closure it is replaced by is already wired on the same
  config.
- `internal/protocol/attachments.go`, `internal/protocol/codes.go` →
  `AttachmentChunkPayload.ConversationID`, `MaxAttachmentIDBytes`,
  `RequestAttachmentPayload` — five "inert until #2143" claims that this ticket
  makes false. Not in the ticket's site list; found by grepping `2143` repo-wide.
- `internal/e2e/relay_v2_attachment_upload_test.go`,
  `internal/e2e/realclaude/interactive_stream_attachment_read_test.go` — the two
  existing upload tests, both of which route a turn first purely to stamp the
  cursor.
- `docs/protocol-mobile.md` § Attachments and § Error codes — the published
  `attachment.invalid_chunk` row, and the three places that say the daemon does
  not act on the field yet.
- `docs/knowledge/features/attachments-package-intake-driver.md` § "The
  conversation is resolved once, on the completing chunk only" — records that
  `Registry` has nowhere to stash a per-transfer conversation id, which is why the
  destination arrives as a `Receive` PARAMETER rather than at admission. Read-only;
  the documentation phase owns it.

## Context

The attachment intake files a completing upload under the daemon's follow-active
cursor, and only `sessionRouter.Route` stamps that cursor. Two operator-visible
failures share the one cause: an attachment added before a conversation's first
message can never be stored (empty cursor → `ErrNoConversation` →
`attachment.storage_failed`, the host blamed for a precondition the wire could not
express), and *send in A, send in B, return to A and attach* files the bytes under
B, where the next `send_message` naming that id is refused whole with
`attachment.not_found`.

#2142 landed the wire half: `attachment_chunk` carries `conversation_id`, and both
the type's own block and the protocol doc say the field is inert until this ticket
acts on it. This ticket is the daemon honouring it.

**No ADR is warranted.** The security argument this reverses was already reversed
and published by #2142 — `AttachmentChunkPayload`'s block and
`docs/protocol-mobile.md`'s 2026-09-06 changelog entry carry the reasoning in
full. This ticket implements a decision already recorded, so a new
`docs/knowledge/decisions/` record would duplicate rather than decide.

### Size: over the boundary, built anyway under the floor rule

Counted against this written plan:

| Boundary | Limit | This ticket |
|---|---|---|
| Production source files | ≤ 5 | **7** (4 of them comment-only) |
| Total written work | ≤ 800 | **~1400** |
| New exported types/interfaces | ≤ 5 | 0 |
| Consumer call sites, simultaneous | ≤ 10 | **17** |
| Acceptance criteria | ≤ 5 | 5 |
| Reject branches | ≤ 10 | 2 new |

Three lines are over, and the ticket says so itself rather than leaving it to be
re-derived. **It still does not split**, and the reason is the floor rule rather
than a re-count of the edits:

- The only seam a cut could fall on is the `Receive` signature. Slice A (thread the
  conversation through, no gate) would land a daemon that files bytes under an
  unvalidated client-asserted id — `EnsureDir`'s `conversations.ValidID` still
  blocks traversal, but a paired client could create directories under
  conversations the daemon does not host. That is a security regression sitting on
  `main` between A and B, on a `security-sensitive` ticket.
- Slice B first (gate a field nothing reads) is a no-op whose only consumer is
  slice A. That is the floor rule's exact shape: a slice consumed by exactly one
  sibling in the same family is part of that sibling.
- Any other cut mints a throwaway resolver seam in `internal/relay` for the next
  slice to delete.

The separable deliverables are already cut out — #2146 owns refusing a mid-transfer
switch (it needs the registry to record a destination at admission), #2144 owns the
e2e flow proofs. A Go signature change cannot be split from its call sites: 14 of
the 17 are one-line argument additions in one file. The overage is stated here and
absorbed deliberately, per the floor-beats-ceiling rule.

The 7th and 6th production files are `internal/protocol/attachments.go` and
`internal/protocol/codes.go`, which the ticket's Technical Notes do not list. They
hold five "the field is inert until #2143" claims. Comment-only edits, but leaving
them would ship a tree asserting the opposite of what it does.

## Design

### The destination becomes a per-transfer parameter

`Intake` loses its `conversation` field, its `resolveConversation` method and
`ErrNoConversation`. `NewIntake` loses its second argument. `Receive` gains one:

```go
func NewIntake(instanceDir string) *Intake
func (i *Intake) Receive(connID, conversationID string, chunk protocol.AttachmentChunkPayload) (attachmentID string, stored bool, err error)
```

`conversationID` crosses the seam as a **`string`**, not a
`conversations.ConversationID`. `internal/relay` does not import
`internal/conversations` — only `internal/relay/handlers` does — and
`KnownConversation` is primitive-typed on purpose to keep it that way. Converting
to the typed id is `internal/attachments`' side of the seam, at the `EnsureDir`
call inside `Receive`.

**Precondition, and it is the caller's**, stated in the same words `HistoryPage`
already uses: `conversationID` MUST already have passed `KnownConversation`,
because it becomes a path component below this seam. `Receive` re-validates
nothing; `EnsureDir`'s `conversations.ValidID` check on both components is the
fail-closed backstop, not the gate.

**`EnsureDir`'s shape check moves from redundant to load-bearing.** Today the
conversation id it validates came from the daemon's own cursor, so
`conversations.ValidID` there was belt-only. From this ticket on, the value is
client-authored, and that check is the deterministic barrier between a remote
string and a path component. It runs before `filepath.Abs` and before `MkdirAll`,
so a traversal-shaped id creates nothing — but it is now the thing standing between
the two, and the test suite must say so rather than assume it.

It is read on the **completing chunk only**, unchanged from today: `Registry` has
nowhere to stash a per-transfer id, and adding one is #2146's. So the accepted
consequence that survives is the mid-transfer switch — a phone that switches
conversations mid-upload files under the NEW one, now registry-validated rather
than taken from the cursor. The consequence that **goes away** is the other one: an
upload naming an unusable conversation is refused on its FIRST chunk, at the relay
gate, rather than after every byte has crossed the wire.

### `ErrNoConversation` is deleted, not re-scoped

AC 1 asks for a surviving-producer check. There is none: `resolveConversation` was
its only producer, and with a caller-validated parameter no path can reach "the
daemon resolved nothing". A caller that passes `""` anyway reaches
`EnsureDir("")` → `ErrInvalidID` → `attachment.storage_failed` through the existing
`default:` arm. That arm **stays** — it is the catch-all for a sentinel the switch
has not learned about, and it never was `ErrNoConversation`'s arm.

### The gate: `handleAttachmentChunk`, before the intake, on every chunk

Two new steps between the payload decode and `Receive`, copying
`handleRequestAttachment`'s ordering and its two-reasons-behind-one-code shape:

1. `chunk.ConversationID == ""` → refuse, reason `"conversation id is absent or
   empty"`.
2. `m.cfg.KnownConversation == nil || !m.cfg.KnownConversation(chunk.ConversationID)`
   → refuse, reason `"conversation is not one this daemon hosts"`.

The empty check runs **first** even though `KnownConversation` would answer false
for `""` anyway: it separates "absent" from "unknown" in the daemon's own log
without depending on a registry lookup's behaviour for the empty string, and a nil
seam is fail-closed either way. This is the same two-arm shape
`handleRequestAttachment` uses (conversation gate, then empty attachment id), with
both arms landing on the one field this handler validates.

`KnownConversation` is a **pure membership check**, which is the point: a session
router additionally refuses a known conversation with no bound session — precisely
the never-messaged conversation this ticket exists for. A known-but-unbound
conversation is accepted here.

Because the gate sits in the handler and the handler runs per frame, validation
happens on **every chunk**, so a transfer naming an unusable conversation dies on
its first one.

### One wire code, two log reasons

Both arms answer the already-published **`attachment.invalid_chunk`**
(`rejectInvalidChunk`), reusing the existing value. No code is minted.

- Not `attachment.storage_failed`: its published row says a *verified* attachment
  could not be written to the host. Nothing is verified at chunk 1, and answering
  it would keep the upload leg blaming the host for a client-side claim.
- `invalid_chunk`'s published row is retryable **no**, and that is the correct
  advice: the same frames reproduce the refusal, and the repair is to name a
  conversation the daemon hosts. Its published class — an `attachment_chunk`'s
  framing claims are inconsistent or out of range — is where a conversation the
  daemon cannot place the chunk against belongs.
- Empty, unknown and foreign are **one answer on the wire**, so the upload leg does
  not become the conversation-existence oracle the retrieval leg deliberately
  closes. The daemon's own log separates them by a daemon-authored `reason`
  constant chosen at the call site.

A `rejectAttachmentChunk` helper (mirroring `rejectAttachmentRequest`) records and
answers in one call so the log line and the wire frame cannot drift into two edits.
**There is no fallback to the cursor on any arm** — the protocol has one rule.

Logging: the conversation id is **not logged on either new arm**, matching
`handleRequestAttachment`, which never logs it on any arm — it has not passed the
gate, and § Attachments makes shape validation the precondition for logging a
client-supplied string. The attachment id, index and total are logged, which is
what this handler already does and what its header's bound (slog's `TextHandler`
escapes control bytes) already covers. The filename, the digest and the bytes are
never logged on any arm; there is no format string interpolating them.

### Wiring

`cmd/pyry/relay.go` drops the resolver closure over `w.active.CurrentConversation`
and calls `attachments.NewIntake(resolveInstanceDirPath(w.instanceName))`. The
cursor keeps its five other readers (the modal resolver, the PTY emitter, the
approval bridge, the replay source, `boundSessionIDForActive`), so nothing else
moves. `KnownConversation` is already wired on the same `V2SessionConfig`.

## Concurrency model

Unchanged. `Receive` still runs on the conn's `appFrameWorker`, one per session,
strictly FIFO, which is what discharges its single-feeder precondition. The new
gate runs on that same worker, before the seam. `KnownConversation` takes the
conversations registry's own mutex and linear-scans; it is called once per chunk
rather than once per request, which is a bounded read on a slice the daemon
already scans on every `request_snapshot`. No lock is held across it, no goroutine
is spawned, and `Intake` still holds no lock of its own — removing a field only
shrinks it.

## Error handling

| Condition | Sentinel / arm | Wire code | Retryable |
|---|---|---|---|
| `conversation_id` empty | gate, before the seam | `attachment.invalid_chunk` | no |
| `conversation_id` unknown or foreign | gate, before the seam | `attachment.invalid_chunk` | no |
| `KnownConversation` unwired | gate, fail-closed | `attachment.invalid_chunk` | no |
| Caller passes `""` past the gate | `ErrInvalidID` from `EnsureDir` | `attachment.storage_failed` | yes |
| Everything else | unchanged | unchanged | unchanged |

`attachmentRejectFor` loses only its `ErrNoConversation` mention; the `default:`
arm and every other row stay exactly as they are.

## Testing strategy

RED first, in the order the compiler allows. A Go signature change does not
compile until its call sites move, so the honest RED is staged: the relay gate
tests are written and watched failing for the right reason (the chunk reaches the
intake unfiltered) before the gate exists, and the intake destination test is
watched failing before `Receive` honours its parameter.

**`internal/attachments/intake_test.go`**

- `newTestIntake(t)` loses the resolver argument; `fixedConversation` is deleted.
- `TestIntake_CompletingChunk_StoresUnderTheResolvedConversation` becomes
  `TestIntake_CompletingChunk_StoresUnderTheConversationTheCallerNamed`: **one**
  intake, two `Receive` calls naming `convA` and `convB`, each landing in its own
  directory. That is strictly stronger than the two-intakes-two-resolvers shape it
  replaces — it kills a build that captured the first conversation it saw.
- `TestIntake_NoConversation_RefusesTheCompletingChunk` and
  `TestIntake_ErrNoConversationIsDistinct` are deleted with the sentinel.
- Two new rows in `TestIntake_Refusals_ComeBackAsTheirOwnSentinel`: the empty
  conversation id, and a **traversal-shaped** one, both answering `ErrInvalidID`
  from `EnsureDir`. That is the backstop pinned at the layer where the value is now
  client-authored, so a future relaxation of the shape check reddens here rather
  than shipping.
- `registry_test.go`'s `packageSentinels` drops `ErrNoConversation`.

**`internal/relay/v2session_attachment_test.go`**

- `fakeAttachmentIntake.Receive` gains the parameter and `attachmentCall` records
  it, so the pinning assertion is "the conversation the frame named is what
  reached the seam", not merely "the seam was called".
- New table over the gate: empty id, unknown id, and a nil `KnownConversation` —
  each answers `attachment.invalid_chunk` with `retryable:false`, and the intake
  records **zero** calls. The zero-calls assertion is what makes it a gate rather
  than a post-hoc refusal.
- One row asserting an accepted chunk reaches `Receive` with the named
  conversation, so the gate is not merely refusing everything.
- The `ErrNoConversation` row leaves the sentinel→code table; existing fixtures
  gain a `ConversationID` and the manager config gains a `KnownConversation`.

**e2e (kept green, not extended — new flow coverage is #2144's)**

- `internal/e2e/relay_v2_attachment_upload_test.go`: chunks carry
  `ConversationID: knownConvID`; the `send_message` precondition and its ack loop
  go. `seedBoundConversation` **stays** — that is what makes the conversation
  *known*, which the gate requires; the cursor stamp is what goes.
- `internal/e2e/realclaude/interactive_stream_attachment_read_test.go`: the chunk
  literal is keyed, so it gains one line; the stamp turn and its drain go. Both
  chunk literals being keyed is why the live suite would keep **compiling** while
  failing at runtime on an empty conversation id — `make check` cannot see that
  package (`e2e_realclaude` build tag), so the edit is made deliberately rather
  than discovered by the gate. Judged by executed-test count, never exit code.

**Doc blocks rewritten, not deleted** (the retired structural property restated as
the new one): `Intake`'s type doc and the `conversation` field's argument (moved to
the parameter), `NewIntake`'s accepted-consequences paragraph, `Receive`'s "NO
CONVERSATION IS READ FROM THE FRAME ON ANY PATH" block, `ResolvePath`'s
precondition block in `storage.go`, the `AttachmentIntake.Receive` seam doc,
`cmd/pyry/relay.go`'s intake comment, the five `internal/protocol` "inert until
#2143" claims, and `docs/protocol-mobile.md` § Attachments (plus one new dated
changelog entry; the existing dated entries stay untouched).

## Open questions

1. **Does dropping the realclaude stamp turn cost a sentinel?** That turn's drain
   carries the package's two standing alarms (`unrecognized_message`,
   `rate_limited`). Resolve during Phase B by confirming the turn that follows the
   upload drains through the same helper; if it does not, keep an alarm-bearing
   drain rather than the whole stamp turn.
2. **Does `attachment.invalid_chunk` or a re-scoped sentinel read better to the
   verifier?** Settled above on the published rows, but re-check that no existing
   test asserts `invalid_chunk` is reachable only from framing claims.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] SHOULD FIX.** The boundary is explicit and single —
  `handleAttachmentChunk`, two arms, before the seam — but `Receive`'s
  `conversationID` is a bare `string` whose membership precondition is enforced by
  documentation, not by the type system. #2083 adds a second caller. Rejected
  alternative: a validated newtype minted in `internal/attachments`. It would add
  an exported type, and `internal/relay` cannot validate membership itself either
  (it holds only the `KnownConversation` closure), so the newtype would be
  constructible from an unvalidated string anyway — a type-shaped assertion of the
  same doc-level claim. The precedent settles it: `HistoryPage` states exactly this
  precondition for exactly this reason, `history.Store.Page` below it does the
  same, and the whole point of the primitive typing is keeping
  `internal/conversations` out of `internal/relay`. Mitigate in Phase B by pinning
  the gate behaviourally: the relay table asserts **zero** intake calls on every
  refusal arm, so a build that removes or reorders the gate reddens rather than
  silently filing bytes.
- **[File operations] SHOULD FIX.** The conversation id becomes a path component
  and, from this ticket on, is client-authored. `EnsureDir` runs
  `conversations.ValidID` on it *before* `filepath.Abs` and `MkdirAll`, so
  `../../../etc` creates nothing and answers `ErrInvalidID` — but that check was
  belt-only while the id came from the cursor and is load-bearing now. Phase B adds
  a traversal-shaped row to `TestIntake_Refusals_ComeBackAsTheirOwnSentinel`
  (recorded in Testing strategy). Modes (`0o700`), the `EvalSymlinks` anchor and
  containment check, and `Store`'s temp-plus-rename are all unchanged by this
  ticket.
- **[File operations — TOCTOU] No findings.** A conversation can be deleted between
  the gate and `EnsureDir`, filing bytes under a directory whose conversation is
  gone. Not a privilege boundary — it was hosted when the gate ran — and it is the
  same window `handleRequestAttachment` and `handleRequestHistory` already accept
  between `KnownConversation` and their own resolvers. Closing it would need the
  registry held across filesystem I/O, which is the lock-across-I/O shape `Receive`
  exists to avoid.
- **[Network & I/O] No findings.** `MaxAttachmentIDBytes` is a producer-side
  contract with no validator, so an inbound `conversation_id` is bounded only by
  the AEAD envelope cap — roughly 64 KB, not 64 bytes. It reaches nothing that
  allocates from it: `KnownConversation` is a length-first string comparison
  against 36-byte registry entries, and the gate refuses before `Admit`, before any
  path join and before any buffer is sized. Net resource effect is an
  **improvement**: a transfer naming an unusable conversation now consumes no
  upload slot and transmits no bytes, where today it consumed a slot and every byte
  before being refused.
- **[Error messages, logs, telemetry] No findings.** One static message per code,
  no error value interpolated on any arm, and the two new `reason` strings are
  daemon-authored literals at the call site — never derived from the frame. The
  conversation id is not logged on either refusal arm (it has not passed the gate);
  the filename, digest and bytes are logged nowhere, and there is no format string
  carrying them. `rejectAttachmentChunk` omits an empty attachment id rather than
  logging it empty, copying `rejectAttachmentRequest`. Log volume: the gate fires
  per chunk, so a client ignoring its reject gets one record per chunk — the same
  shape every existing per-chunk refusal arm already has, so no new flood vector.
- **[Concurrency] No findings.** `KnownConversation` takes the conversations
  registry's mutex on the `appFrameWorker` goroutine with **no lock held** by the
  caller, and `Receive` takes the upload `Registry`'s mutex only after the gate has
  returned, so the two are never nested and no lock ordering is introduced.
  Single-feeder is unchanged (one worker per session, FIFO). No goroutine is
  spawned; `Intake` loses a field and gains no state.
- **[Tokens, secrets, credentials] Not applicable.** No credential is read,
  written, compared or logged. `attachment_id` is published as
  not-a-capability and this ticket does not change what it grants.
- **[Subprocess execution] Not applicable.** Nothing is exec'd on any path this
  ticket touches; no value reaches an `exec.Command` argument or an environment.
- **[Cryptographic primitives] Not applicable.** No RNG, no key, no nonce. The
  digest comparison in `Deliver` is an unchanged integrity check over
  already-authenticated bytes, not a secret comparison, so constant time is not the
  relevant property.
- **[Threat model alignment] OUT OF SCOPE — #2146.** `docs/protocol-mobile.md`
  § Attachments publishes "naming a conversation is not authorization" and locates
  the safety in confinement: registry validation plus `EnsureDir` refusing an
  escaping directory. This ticket implements exactly that for the upload leg. The
  threat it deliberately does not close is the **mid-transfer switch** — chunk 0
  naming A, the completing chunk naming B. Both ids are registry-validated, the
  registry key is `(connID, attachmentID)` so only one conn's own transfer is
  involved, and the client could have uploaded to B directly; so it is a misfile a
  client can inflict on itself, with no privilege gain. #2146 closes it by
  recording the destination at admission.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-06
