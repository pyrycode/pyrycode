# #2054 — answer a `request_attachment` with the stored file or a coded reject

**Ticket:** [#2054](https://github.com/pyrycode/pyrycode/issues/2054) (child of #1746, grandchild of #1684)
**Size:** `s`, over one line of the table (see § Size) — depth-capped, `needs-human:sizing` already applied.
**Labels:** `enhancement`, `size:s`, `security-sensitive`, `needs-human:sizing`

This is the join. `TypeRequestAttachment` (#2052) is declared with no handler,
`StreamAttachment` (#2053) turns a resolved host path into a conforming chunk
stream with no caller, and `attachments.ResolvePath` (#2037) maps a
`(conversation_id, attachment_id)` pair to a path. This slice wires the three
into an answered verb: a client asks, and is either served the file or refused
with one merged code.

## Size

Six boundaries, re-counted against this written plan:

| Limit | Boundary | This plan |
|---|---|---|
| Production source files created or modified | ≤ 5 | **4** — `v2session_attachment_request.go` (new), `v2session.go`, `v2session_seams.go`, `cmd/pyry/relay.go` |
| Total written work | ≤ 800 | **~1300 (over)** |
| New exported types or interfaces | ≤ 5 | **0** — the resolver seam is a func field on the existing `V2SessionConfig` |
| Consumer call sites needing simultaneous update | ≤ 10 | **3** — the two `appFrameJob` literals in `dispatchAppFrame` and the `job.attachment` arm in `appFrameWorker` |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches | ≤ 10 | **7** — six reject causes collapsing to one code, plus the abort code |

Only the line count trips, and it is stated rather than hidden. **The split-depth
gate closes the question**: `parent 1746 grandparent 1684`, verified by the
`gh api graphql` parent-chain query, so this is already a grandchild and does not
get split again. Per the depth-cap rule the outcome is *build it and mark it*,
not *stop*; `needs-human:sizing` is already on the ticket from refinement, so no
label change is owed. The refiner's own analogues size it honestly — #1897 landed
436 spec + 1248 implementation lines across 9 files as one ticket for the same
handler shape one leg earlier, and #2053, this ticket's immediate blocker, landed
1218 lines across 4 files.

**No overlap** (§ A2, run 2026-09-03): `git fetch --prune` then a sweep of every
`origin/feature/<n>` branch's diff against `origin/main`. One branch names a file
this plan touches — `origin/feature/449`, `internal/relay/v2session.go` — and
issue #449 has been CLOSED since 2026-05-17, so that branch is a stale leftover
rather than in-flight work. Nothing else intersects.

## Files read

- `internal/relay/v2attachmentstream.go` → `StreamAttachment`, `attachmentEnvelopes`,
  `strippedPathError`, `ReassembleAttachment` — the stream this handler drives, its
  stated precondition, and the error identities the two-code partition rests on.
- `internal/relay/v2session_attachment.go` → `handleAttachmentChunk`, `attachmentReply`,
  `attachmentReplyError`, `attachmentReject`, `attachmentRejectFor` — the sibling handler
  whose nil-seam guard, reject-not-tolerate decode, never-log posture and reply emitters
  this one reuses verbatim rather than re-deriving.
- `internal/relay/v2session_replay.go` → `handleRequestSnapshot` — the in-package precedent
  for the **ordering** (membership gate first, before anything else). Its reject *code* is
  deliberately not copied.
- `internal/relay/v2session.go` → `dispatchAppFrame`, `enqueueAppFrame`, `appFrameJob`,
  `appFrameWorker`, `forwardToRun`, `Push` — the routing tag to widen, and Push's closed
  error contract (`ErrConnNotFound`, or `ctx.Err()` only when already cancelled at entry),
  which is what makes the abort partition sound.
- `internal/relay/v2session_seams.go` → `V2SessionConfig.KnownConversation`,
  `V2SessionConfig.AttachmentIntake` — the membership seam this verb validates against and
  the doc-block idiom the new resolver field follows.
- `internal/attachments/storage.go` → `ResolvePath` — its precondition ("a caller that gets
  it wrong defeats every check below"), its creates-nothing shape, its full-path-equality
  containment, and its logging obligation on the returned path.
- `internal/protocol/attachments.go` → `RequestAttachmentPayload` — the declaring contract:
  every field an unverified claim, the zero value decoding silently to two empty strings,
  `filepath.Join(dir, "", "")` addressing the conversation directory root, and both ids
  loggable only after shape validation.
- `internal/protocol/codes.go` → `CodeAttachmentNotFound`, `CodeAttachmentStreamAborted`,
  `TypeRequestAttachment` — the merged-code disclosure argument, the retryability split,
  and the guard-filing obligation this slice discharges.
- `cmd/pyry/relay.go` → `attachmentResolve`, the `KnownConversation` closure, the
  `V2SessionConfig` literal — the wiring point; both closures already exist in one scope.
- `cmd/pyry/relay_guard_test.go` → `inboundTypes`, `excludedTypes`,
  `TestEveryInboundV2TypeHasHandler` — the entry that must move, and why leaving it fails
  Assertion #2.
- `internal/e2e/relay_v2_attachment_upload_test.go` → `TestRelayV2_AttachmentUploadMultiChunk`,
  `attachmentFixture`, `nextAttachmentEnvelope`, `assertNoAttachmentLeakInLogs` — the
  harness precedent and the multi-chunk fixture this run reuses.
- `internal/relay/v2session_attachment_test.go` → `startAttachmentConn`, `soleReply`,
  `decodeErrorPayload` — the unit rig shape (real handshake, real sealed frame, real worker).
- `docs/protocol-mobile.md` § Attachments (`request_attachment`, "Three answers") and
  § Error codes rows for both codes — the published contract this handler implements.
- `docs/knowledge/features/v2-session-manager-state-machine-outbound-attachment-stream-streamattachm.md`
  — #2053's lessons, in particular that the returned error is rebuilt around the *stripped*
  cause so `errors.Is` still works and a host path cannot leak through a caller that logs it.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-attachment-chunk-attachmentintake-seam.md`
  — #1897's lessons: the loggable field set, the three-way-answer trap, and (from #1898) that
  an e2e never-log scan needs three renderings per `[]byte` field to be non-vacuous.

## Context

The retrieval leg's user story — reopen a conversation on another device and the
file you sent is still reachable — has been unreachable in code since the upload
leg landed. #2052 published the verb, #2053 built the stream, and neither has a
caller: a client that sends `request_attachment` today gets nothing at all (the
type is intercepted by no case, so `dispatch.Route` answers `protocol.unsupported`).

The whole security property is one sentence from `ResolvePath`'s doc block: the
conversation id must be one *the caller has already validated*, because that
function cannot check it and "a caller that gets it wrong defeats every check
below". This handler is that caller. Skip the check and the frame reads "name any
conversation, get its files" — turning an identifier the protocol repeatedly calls
**not a capability** into exactly that.

No ADR is warranted. Every decision here is an application of contracts already
published in `docs/protocol-mobile.md` § Attachments and already argued in the
declaring doc blocks; nothing new is being decided at the architecture level.

## Design

### The seam: `V2SessionConfig.AttachmentResolve`

A new optional func field, mirroring `KnownConversation`'s primitive-in/primitive-out
shape so `internal/relay` gains no import:

```go
AttachmentResolve func(conversationID, attachmentID string) (path string, ok bool)
```

Production assigns `cmd/pyry/relay.go`'s existing `attachmentResolve` literal —
which already has exactly this signature for `handlers.SendMessage` — so the
wiring is one field, not a new adapter. Its doc block must state what the closure
does *not* do, because that is the whole reason this handler exists: it wraps
`ResolvePath`, discards its error (the one scope that ever holds it, for the
documented log-injection reason), and discharges **no registry check whatsoever**.

Nil ⇒ the frame is **inert but consumed**, exactly as a nil `AttachmentIntake`
leaves `attachment_chunk`: no decode of remote-authored bytes, no reply, and no
fall-through to `dispatch.Route`'s unknown-type answer. That keeps every
non-production construction site (unit tests, the fake-daemon harness,
foreground/v1) compiling and behaving unchanged.

### Routing: widen the tag, do not re-probe

`appFrameJob.attachment bool` becomes a typed kind:

```go
type appFrameKind uint8
const (
    appFrameRoute appFrameKind = iota  // v1 application frame → dispatch.Route
    appFrameAttachmentChunk            // inbound upload (#1897)
    appFrameAttachmentRequest          // inbound retrieval (#2054)
)
```

Three call sites move: the two `appFrameJob` literals in `dispatchAppFrame` and
the `job.attachment` arm in `appFrameWorker`. A bool pair was rejected — the two
kinds are mutually exclusive by construction and a pair can express a state that
is not. The `appFrameJob` doc block's insistence that the routing decision live in
exactly one place is preserved: `dispatchAppFrame`'s case selector stays the
registry `TestEveryInboundV2TypeHasHandler` reads, and the worker still branches
on the tag rather than re-deriving the type.

`request_attachment` runs on the worker rather than inline on `Run` for the same
reason `attachment_chunk` does: `StreamAttachment` reads a file of up to the
per-upload bound (16 MiB), hashes it, and base64-marshals N envelopes. That is
precisely the work #1491 had to move off `Run`.

### `handleRequestAttachment` — order is the design

New file `internal/relay/v2session_attachment_request.go`. Signature mirrors its
sibling — it takes the plaintext, not a probed envelope, because the worker
receives bytes:

`func (m *V2SessionManager) handleRequestAttachment(ctx context.Context, s *V2Session, plaintext []byte)`

Seven steps, and the order carries the security property:

1. **Nil `AttachmentResolve` ⇒ inert.** Return before decoding anything. An
   unwired daemon performs zero parsing of remote-authored bytes.
2. **Envelope decode.** Unreachable (`dispatchAppFrame` already decoded these
   bytes to match the type); log and return, never echo the error. Copied from
   `handleAttachmentChunk`.
3. **Payload decode failure ⇒ `attachment.not_found`, rejected not tolerated.**
   `handleRequestSnapshot` tolerates a decode failure because its zero value fails
   the next gate anyway; `RequestAttachmentPayload`'s own doc block names the
   opposite obligation ("a decode failure is a rejected frame, never an
   empty-but-successful request — discharging it is #2054's"). Nothing about the
   failure is echoed or logged: `encoding/json` quotes offending input into its
   error string and those bytes are remote-authored.
4. **Membership gate, before either id becomes a path component.**
   `m.cfg.KnownConversation == nil || !m.cfg.KnownConversation(payload.ConversationID)`
   ⇒ `attachment.not_found`. This is `handleRequestSnapshot`'s ordering copied and
   its *code* deliberately not copied: that handler answers
   `conversation.not_found`, and a distinguishable answer here would rebuild the
   path-existence oracle `CodeAttachmentNotFound`'s merge exists to prevent. A nil
   seam rejecting everything is the fail-safe reading, matching its sole existing
   reader.
5. **Empty attachment id ⇒ `attachment.not_found`.** Four lines discharging the
   published obligation that "an absent or empty id resolves nothing", against the
   specific documented hazard: `filepath.Join(dir, "", "")` is `dir`. It is
   deliberately **not** a second copy of `ResolvePath`'s canonical-shape check —
   the non-canonical-but-non-empty id stays that function's to refuse — so the two
   do not fork. It is also what makes the empty-id acceptance criterion provable
   against a test double rather than against the production closure's internals.
6. **Resolve.** `path, ok := m.cfg.AttachmentResolve(convID, attID)`; `!ok` ⇒
   `attachment.not_found`. One sentinel behind that bool covers the unknown id,
   the non-canonical id and the id resolving outside the conversation's directory
   alike — by construction the handler cannot branch on what it must not
   distinguish.
7. **Stream.** `m.StreamAttachment(ctx, s.connID, attID, path, env.ID)`.
   `nil` ⇒ done: the chunks *are* the answer and there is no completion frame.
   Non-nil ⇒ § Error handling's two-code partition.

The reply on the success path is the stream itself, emitted through `Push` from
this goroutine — documented safe from any goroutine, and the reason a file of any
size cannot overrun the 8-slot handler-reply buffer. Both reject paths go through
the sibling's `attachmentReplyError` → `attachmentReply` → `forwardToRun`, since
`s.send` is the single-owner Noise send state and only `Run` may seal.

### Rejects

Two `attachmentReject` values in the new file, reusing the sibling's struct so a
code cannot be paired with the wrong retryable flag:

| value | code | message (static) | retryable |
|---|---|---|---|
| `rejectAttachmentNotFound` | `protocol.CodeAttachmentNotFound` | `"attachment not found"` | `false` |
| `rejectStreamAborted` | `protocol.CodeAttachmentStreamAborted` | `"attachment stream aborted"` | `true` |

Retryability is read off § Error codes rather than chosen here: `not_found` is
published `no`, `stream_aborted` `yes, after a backoff`. Both messages are fixed
constants; no value derived from an error, an id or a path ever reaches either.

### `cmd/pyry/relay.go`

One field added to the `V2SessionConfig` literal, `AttachmentResolve:
attachmentResolve`, beside the existing `KnownConversation` and
`AttachmentIntake`. The comment records that this handler is the validating
caller `ResolvePath`'s precondition names, and that the closure's own comment
(written for `handlers.SendMessage`, whose validation is `SessionRouter.Route`)
now describes two callers with two different validators.

`SessionRouter.Route` is deliberately **not** reused here: it lives in a package
the v2 manager does not have, and it additionally refuses a known conversation
with no bound session — precisely the reopened conversation this user story is
about. `KnownConversation`'s doc block records that distinction; for retrieval,
addressable is the correct reading.

## Concurrency model

No new goroutine. The handler runs on the conn's existing `appFrameWorker`,
strictly FIFO, one frame fully handled before the next — so a large retrieval
delays that conn's subsequent frames and no other conn's, which is the property
#965 bought and #1897 relied on.

Three goroutines are involved and each touches a disjoint set:

- **`Run`** decodes and tags the frame (`dispatchAppFrame`), seals every outbound
  frame (`forwardAppReply` for the rejects, `drainOnce` → `forwardEnvelope` for
  the chunks). Sole owner of `s.send`.
- **the worker** decodes the payload, consults both seams, reads the file and
  enqueues via `Push`. Touches `m.queues` under `pushMu` only.
- **the seams' own implementations** — `KnownConversation` takes the conversations
  registry's mutex; `ResolvePath` holds no state and no lock.

Shutdown: the worker exits on `s.done` or `runCtx`, unchanged. A reject in flight
when the session tears down is abandoned by `forwardToRun` returning false; queued
chunks are dropped by `forwardEnvelope`'s `V2StateOpen` re-check. Nothing new to
leak.

## Error handling

**The partition is on the error's identity, because identity is all
`StreamAttachment` returns** — its signature carries no count of what it
enqueued, so the handler cannot ask "how many chunks went out".

`StreamAttachment` has exactly three error origins:

| origin | when | answer |
|---|---|---|
| `os.ReadFile` failed | before any envelope exists | `attachment.not_found` |
| `attachmentEnvelopes` marshal failed | before any envelope is pushed | `attachment.not_found` |
| `Push` failed | emission has begun (or is at index 0) | `attachment.stream_aborted` |

The classifier names the **abort** set and defaults to `not_found`:

```go
func attachmentStreamAborted(err error) bool  // ErrConnNotFound | context.Canceled | context.DeadlineExceeded
```

That direction is deliberate and is the design decision most worth stating.
`Push`'s doc block closes its own error set — `ErrConnNotFound` when no queue
exists, `ctx.Err()` only when ctx is already cancelled at entry, and a
drop/overflow is *not* an error — so the abort set is small and enumerable, while
the pre-emission set is open (any `errno` from a read, plus a defensive marshal
failure). Defaulting the open set to `not_found` means a *new* pre-emission
failure mode is classified correctly without an edit; defaulting the other way
would claim emission began when it had not. Any future post-emission failure must
be added to the abort arm, and that obligation goes in the function's doc block.

Two consequences designed against rather than discovered, both from the ticket:

- **The abort frame is usually undeliverable in production.** `Push` fails only
  when the conn's queue is gone, and by then `forwardToRun` is also gone; the
  branch is best-effort. Its proof therefore belongs in a unit test that removes
  the queue while the session stays open, not in the e2e run.
- **A `Push` failure at index 0 is answered `stream_aborted`** where `not_found`
  would also have been defensible. That is the correct trade: `stream_aborted` is
  the retryable code and a torn-down conn is a retryable condition.

A stream that simply stops because the session died gets **no frame at all** — the
client's timeout covers it and the protocol offers nothing for it.

**Never-log rule**, inherited from `AttachmentChunkPayload`'s SECURITY block,
§ Attachments, and `ResolvePath`'s logging obligation. Loggable: conn id, the
attachment id *after* the empty check, chunk counts, the reject code. Never
logged, replied or interpolated: the file's bytes, the filename, the digest, the
resolved host path, the requested conversation id (a client-supplied string whose
shape this handler does not fully validate), and the error out of any seam or out
of `StreamAttachment`. Terminal outcomes at Info; the inert arm at Debug.

## Testing strategy

**RED first.** Every unit test below is written and run against the unwired tree
before the handler exists — the guard test reddens the moment the
`dispatchAppFrame` case lands, which is itself the first RED.

Unit, `internal/relay/v2session_attachment_request_test.go`, on the
`startAttachmentConn` rig (real handshake, real AEAD-sealed frame, real worker) so
interception is proven through `dispatchAppFrame` rather than by calling the
handler directly. A stub resolver returns a path under `t.TempDir()`:

- the happy path — one multi-chunk file streams, every frame is
  `TypeAttachmentChunk` correlated to the request's envelope id, and
  `ReassembleAttachment` recovers the bytes exactly (the oracle #2053 exported for
  this assertion);
- a table over the six no-bytes causes — undecodable payload, empty
  `conversation_id`, empty `attachment_id`, unknown conversation, resolver miss,
  and a resolved path whose file was removed — each answered with exactly one
  `TypeError`, code `attachment.not_found`, `retryable:false`, the static message,
  correlated, **and no attachment frame**;
- the membership gate fires *before* the resolver: a foreign conversation id leaves
  the resolver stub with **zero calls** — the assertion that "performs no filesystem
  lookup built from the client's ids" is about, and the one a reordering kills;
- nil `AttachmentResolve` ⇒ inert: no reply at all, no
  `protocol.unsupported` (proving consumed-not-dropped), and the resolver never
  reached;
- nil `KnownConversation` with a resolver wired ⇒ `attachment.not_found`, never
  served;
- the abort branch: the conn's push queue is removed from `m.queues` while the
  session stays open, so `Push` answers `ErrConnNotFound` deterministically and the
  reply path still works. Expect one `TypeError`, `attachment.stream_aborted`,
  `retryable:true`, correlated, and zero attachment frames. This is the one test
  whose fixture deviates from production (there, the queue disappears *with* the
  session) and the deviation is what isolates the branch;
- a banned-string log scan over the captured buffer — filename, digest, content
  bytes and host path absent — with a positive pin on the attachment id so the
  negatives cannot green against an empty buffer. Per #1898's lesson the content
  bytes are checked in both renderings a `[]byte` takes (base64 and slog's
  bracketed decimal), and the fixture's byte range is chosen so the base64 needle
  is alignment-valid.

Guard, `cmd/pyry/relay_guard_test.go`: `"TypeRequestAttachment"` moves from
`excludedTypes` to `inboundTypes` as `"switch-intercepted"`, with the comment
recording the move the way `TypeAttachmentChunk`'s did in #1897. Assertion #2
fails a wired type left behind, so this is mandatory rather than tidy-up.

E2E, `internal/e2e/relay_v2_attachment_retrieval_test.go` (build tag `e2e`, so it
is inside the standard gate), following
`TestRelayV2_AttachmentUploadMultiChunk`'s shape — pair, seed a bound
conversation, fake relay, fake phone, real daemon, no credentials. The stored file
is **written directly onto the host** under the conversation's attachment
directory rather than uploaded, because the upload leg is #1898's subject and
routing a turn first would cost ~20s of deadline to prove nothing this run
asserts. It reuses `attachmentFixture`, so the file is multi-chunk by
construction. Two asks, one run:

- a request for the stored attachment ⇒ the chunks arrive, `ReassembleAttachment`
  recovers the file byte for byte;
- a request naming an attachment that is not there ⇒ one `TypeError`,
  `attachment.not_found`, correlated to *that* request;
- plus `assertNoAttachmentLeakInLogs` over the daemon's captured stderr.

Verification is touched-scope: `go test -race ./internal/relay/... ./internal/e2e/... ./cmd/pyry/...`,
`go vet ./...`, `go build ./cmd/pyry`. The full-module race suite is the verifier's gate.

## Open questions

1. **Does the e2e run need the daemon to have routed a turn first?** The upload
   run does, because `attachments.Intake` resolves its conversation off the
   follow-active cursor. Retrieval reads the conversation off the *frame* and
   validates it against the registry, so it should not — to be confirmed by the
   run itself, and recorded here if wrong.
2. **Is `m.queues` reachable for deletion from an in-package test without a data
   race under `-race`?** It is guarded by `pushMu`, which a same-package test can
   take. If it proves awkward, the fallback is calling the handler directly with a
   session whose conn was never opened, which tests the same partition with a
   weaker fixture.
3. **Does anything else construct `appFrameJob` outside `dispatchAppFrame`?** The
   sweep found two literals and one reader, all in `v2session.go`. Confirm no test
   file names the `attachment` field before renaming it.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. There is exactly one boundary and it is a
  single function: `handleRequestAttachment` decodes `plaintext` into
  `protocol.RequestAttachmentPayload`, whose declaring doc block states that every
  field is an unverified claim. `conversation_id` becomes *addressable* (never
  "trusted") at the `KnownConversation` gate and nowhere else; `attachment_id`
  never becomes trusted at all — it is handed to `ResolvePath`, which validates its
  canonical shape and proves containment by full-path equality. The value passed
  onward is the **client's own string**, not one the registry returned, so the two
  checks could in principle disagree; they cannot here, because
  `conversations.Registry.Get` compares ids with `==` and `conversations.ValidID`
  requires the lowercase-UUIDv4 shape, so the gate and the path build read the same
  bytes. Downstream signalling is by ordering rather than by type, which is why
  § Design states the order as load-bearing and the unit suite pins the
  membership-gate-before-resolver claim with a zero-call assertion.

- **[Tokens, secrets, credentials]** Not applicable by design, and the design is
  published rather than assumed: `docs/protocol-mobile.md` § Attachments states
  that authorization is **pairing**, enforced structurally at the Noise IK
  handshake, that "there is no per-verb authorization gate on either leg, and none
  is invented here", and `RequestAttachmentPayload`'s block repeats it for this
  frame. This slice mints no token, reads none, and adds no per-verb gate. The
  attachment id is explicitly **not** a capability and must not be treated as a
  bearer secret; what bounds a paired-but-hostile client is confinement.

- **[File operations]** Two findings, neither exploitable as designed.
  *Path traversal:* the only path join in this slice's reach is inside
  `ResolvePath`, and it is reached **only** after the membership gate — the
  ordering that discharges that function's stated precondition. Its defence is
  `conversations.ValidID` on both ids plus full-path **equality** (strictly
  stronger than a containment test; it refuses a conversation directory symlinked
  at a sibling), and only regular files are eligible.
  *Non-regular file / FIFO wedge:* `StreamAttachment`'s doc block records that
  `os.ReadFile` on a FIFO blocks indefinitely, wedging the calling goroutine — here
  the conn's `appFrameWorker`, which would silently stall every subsequent frame on
  that conn. Unreachable through `ResolvePath` (regular files only). **SHOULD FIX:**
  the `AttachmentResolve` seam's doc block must state that the only sanctioned
  implementation is a `ResolvePath` wrapper and name both consequences of supplying
  a path from anywhere else — no containment guarantee, and a wedged worker. No
  runtime guard is added: it would be a second, weaker copy of a check that already
  exists, which is exactly what #2053 declined for the same reason.
  *Creation:* nothing is created on any path, including every reject —
  `ResolvePath` performs no `MkdirAll`, which is why an `attachment.not_found`
  cannot leave an empty directory behind for each traversal probe it refuses.
  *TOCTOU:* the check-then-use window between `ResolvePath`'s directory read and
  `StreamAttachment`'s open is real and accepted on `EnsureDir`'s published bound —
  exploiting it needs write access inside the daemon's own `0o700` state directory,
  and anyone holding that can already rewrite `devices.json`.

- **[Subprocess / external command execution]** Not applicable — this slice
  executes nothing and passes no value to `exec.Command`. Neither id becomes an
  argv element, an environment variable, or prompt content; unlike
  `send_message`'s `attachment_ids` (#2038), nothing here reaches `claude`.

- **[Cryptographic primitives]** No findings, and one property worth stating
  because getting it wrong is silent. The digest on every chunk is `crypto/sha256`
  over the stored bytes, computed by `attachmentEnvelopes` and published as
  integrity, not authenticity. **No new sealing site is introduced**: the rejects
  go through `attachmentReplyError` → `attachmentReply` → `forwardToRun`, so `Run`
  seals them under the single-owner `s.send`, and the chunks go through `Push`,
  documented safe from any goroutine because it touches only `m.queues`. A reply
  emitted directly from the worker would be a concurrent `Encrypt` on the send
  CipherState and therefore a **nonce reuse** — the specific reason the sibling
  handler owns its own error helper instead of calling `settingsReplyError`.

- **[Network & I/O]** One finding, recorded rather than fixed. *Input:* the
  request payload carries no count and no length field, so there is nothing to
  allocate from a claim; the encrypted-frame cap is the only inbound byte limit and
  it already applies. *Output:* per-frame size is #2053's, proven in two fabrics
  against `maxNoisePayloadBytes`. *Amplification:* this verb is amplification-shaped
  — a ~100-byte request yields up to the 16 MiB per-upload bound — and the bounds
  on it are existing and deterministic rather than new. Per conn, the
  `appFrameWorker` is strictly FIFO, so retrieval concurrency is **1 per conn**;
  `appFrameQueueDepth` bounds queued requests and tears the conn down at 4421 on
  overflow; and `pushQueueByteCeiling` (32 MiB) bounds enqueued outbound bytes,
  latching and tearing the session down past it. The honest consequence: **two
  back-to-back maximum-size retrievals on one conn can trip that ceiling**, because
  the worker enqueues the second transfer before `Run` has drained the first, and
  the outcome is a teardown of the requesting conn — self-inflicted, per-conn, and
  bounded by the mechanism #1505 built for exactly this. **No new limit is minted
  here:** § Attachments states that any limit on concurrent retrievals is
  receiver-configured, unpublished and learned by being rejected, so inventing one
  would be new protocol surface in a slice that owns none, and there is no observed
  failure to justify it.

- **[Error messages, logs, telemetry]** No findings; the merge is the control.
  All six no-bytes causes take **one** `attachmentReject` value — one code, one
  static message, one retryable flag — so an unknown conversation and an unknown
  attachment are byte-identical on the wire, which is what stops the verb from
  becoming a path-existence oracle. No message is derived from an error, an id or a
  path. The never-log set is inherited whole and the resolved path is the sharpest
  edge: `ResolvePath`'s leaf is a *sanitised client filename*, so the path is no
  more loggable than the raw name; `StreamAttachment` already rebuilds its error
  around the stripped cause so a caller logging it verbatim cannot leak the path,
  and this handler additionally never logs that error at all. The requested
  conversation id is not logged either — this handler validates its *membership*,
  not its *shape*, and § Attachments makes shape validation the precondition for
  logging a client string.
  *Residual:* the two rejects differ in **timing** — an unknown conversation is
  refused before any filesystem access, an unknown attachment after a directory
  read. Accepted, and the reason it does not matter is structural rather than
  statistical: the peer is paired, and a paired peer can already enumerate this
  daemon's conversations outright with `list_conversations`, so the conversation
  half is not a secret from it. The half that must stay opaque is which *paths*
  exist, and that half is answered identically in code, message and retryability.

- **[Concurrency]** No findings. No goroutine is spawned, so none can leak; the
  handler runs on the existing per-conn worker whose exit conditions (`s.done`,
  `runCtx`) are unchanged. No lock is taken by this slice — `KnownConversation`
  takes the conversations registry's own mutex and `ResolvePath` holds no state —
  so no ordering is introduced and none can invert. A reject racing teardown is
  abandoned by `forwardToRun`; queued chunks racing teardown are dropped by
  `forwardEnvelope`'s `V2StateOpen` re-check, which is the gate that keeps a
  buffered push from reaching a de-authenticated peer. The one new correctness risk
  is the `appFrameJob` tag widening: a mis-tagged frame would route an upload to the
  retrieval handler or the reverse. It is not exploitable (both handlers reject
  what they cannot decode) and it is caught by #1897's existing suite plus this
  ticket's, which drive real sealed frames through `dispatchAppFrame` rather than
  calling handlers directly.

- **[Threat model alignment]** § Security model's threat 1 (claude-authored or
  client-authored content becoming prompt content) **does not land on this frame** —
  nothing here reaches `claude`, the opposite of `send_message`'s `attachment_ids`.
  The threat that does land is a paired-but-hostile device probing for files, and
  the answer is the one the section publishes: confinement to the named
  conversation's own directory plus the daemon's validation of that name, never the
  secrecy of an id. Out of scope and named: per-device authorization for retrieval
  (there is no per-verb gate on any leg of this family, by published decision), and
  a receiver-configured concurrent-retrieval limit (unpublished by design; whoever
  first needs one owns it).

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-03
