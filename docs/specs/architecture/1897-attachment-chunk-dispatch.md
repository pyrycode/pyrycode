# #1897 — answer an `attachment_chunk` on the v2 session

Connect the wire to the upload service: intercept `attachment_chunk` in
`dispatchAppFrame`, run the upload on the conn's `appFrameWorker` through the
`attachments.Intake` seam, answer `attachment_stored` or a dotted `attachment.*`
reject, and release the conn's uploads at teardown.

## Files read

Production surface:

- `internal/attachments/intake.go` → `Intake`, `NewIntake`, `Receive`,
  `ReleaseConn`, `ErrNoConversation` — the seam behind this slice. `Receive`'s
  three-way answer and its single-feeder precondition are the two contracts the
  design is built against; `ErrNoConversation`'s doc block names this ticket as
  the owner of its wire code.
- `internal/attachments/registry.go` → `ErrUnknownUpload` — the other code this
  slice picks. `storage.go` → `ErrInvalidID`, `ErrNotContained`, `ErrWriteFailed`
  (all three already committed to `attachment.storage_failed` by `ErrWriteFailed`'s
  doc), `EnsureDir`, `Store` — the two that wrap host paths into their refusals.
  `accumulator.go` → the six accumulator sentinels; `admission.go` →
  `ErrInvalidDeclaration`, `ErrUploadTooLarge`, `ErrTooManyUploads`.
- `internal/relay/v2session.go` → `dispatchAppFrame` (the interception point and
  its non-blocking enqueue), `appFrameWorker`, `routeAppFrame`, `forwardToRun`,
  `forwardAppReply` (the seal-on-Run boundary), `closeWith` (the per-conn cleanup
  cluster `ReleaseConn` joins), `V2Session.appFrames`.
- `internal/relay/v2session_question.go` → `handleQuestionAnswer` — #1984's
  handler, the shape this one copies: nil-seam inert-but-consumed, reject on
  decode failure, never echo the decode error.
- `internal/relay/v2session_seams.go` → `QuestionResolver`, `ModalResolver`,
  `V2SessionConfig` — where a seam and its config field are declared.
- `internal/relay/v2session_settings.go` → `settingsReplyError`;
  `v2session_debugbundle.go` → `debugBundleReplyError`, `msgDebugBundleUnavailable`
  — the static-message error-reply idiom, and the package's "each reply-owing
  handler owns its helper, do not extract a shared one" posture.
- `internal/protocol/attachments.go` → `AttachmentChunkPayload` (its SECURITY
  block is the declaring log contract), `AttachmentStoredPayload`;
  `internal/protocol/codes.go` → the seven `attachment.*` codes and
  `TypeAttachmentChunk` / `TypeAttachmentStored`.
- `cmd/pyry/relay.go` → `startRelayV2`'s `relay.V2SessionConfig` literal,
  `boundSessionIDForActive` (the precedent for an adapter over `activeConversation`);
  `cmd/pyry/main.go` → `activeConversation.CurrentConversation`,
  `resolveRegistryPath` / `resolveConversationsRegistryPath` (the `resolve*Path(name)`
  family the instance-directory resolver joins).
- `cmd/pyry/relay_guard_test.go` → `inboundTypes`, `excludedTypes`,
  `TestEveryInboundV2TypeHasHandler` — the guard whose `TypeAttachmentChunk` entry
  moves, and whose three `#1744` cites are stale.

Documentation that changed the design:

- `docs/protocol-mobile.md` § Error codes — the seven rows, their retryability
  and the static-message mandate on `attachment.storage_failed`; § Attachments —
  chunks may arrive in any order, and the digest is added to the log ban.
- `docs/knowledge/features/attachments-package-intake-driver.md` — the fork, the
  three answers, the once-per-completing-chunk conversation resolution.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-question-control-questionresolver-seam.md`
  — two lessons carried into the testing strategy: a decode-failure fixture must
  be a **type mismatch inside valid JSON** (a `json.RawMessage` payload cannot
  carry literally-malformed bytes through `json.Marshal`), and a "the seam was
  not called" assertion must bind on the **call count**, not on the value received.

## Context

`dispatchAppFrame` splits an open-state plaintext two ways and `attachment_chunk`
has no arm, so an uploaded chunk falls through to the v1 application dispatch
chain and is answered as an unhandled type. `attachments.Intake` (#1896) is the
upload service and has no caller outside its own package. This slice is the
join: the seam interface, the interception, the sentinel→code map, the production
conversation resolver, the daemon-side construction, and the teardown release.

No ADR is warranted. The two wire-code choices are recorded here and in the
handler's doc block; both fold into the already-published vocabulary, so
`docs/protocol-mobile.md` § Error codes stays true as written and this ticket
publishes no new client-visible contract.

**Sizing.** The ticket carries `needs-human:sizing`: honestly counted it is over
the 800-line ceiling and at the 5-file line, and #1897 is a grandchild
(#1684 → #1744 → #1897) so the split-depth gate bars a split. The refiner
recorded the A/B split it would have made. Re-checked against this written plan:
5 production files (`v2session_seams.go`, `v2session.go`, the new
`v2session_attachment.go`, `cmd/pyry/relay.go`, `cmd/pyry/main.go`), 1 new
exported type, 0 consumer call sites to update, 5 acceptance criteria, 6 reject
branches. Only the line count is over. Building per the depth gate.

**File-overlap check (§ A2).** `origin/feature/449` touches
`internal/relay/v2session.go`, but issue #449 is CLOSED, its branch is unmerged
and last touched 2026-05-17, and it has no PR — an abandoned branch, not in-flight
work. No blocker set; no other branch touches any file in this plan.

## Design

### The seam

`AttachmentIntake` joins the seven seams in `v2session_seams.go`, declared
consumer-side, with a `V2SessionConfig.AttachmentIntake` field:

```go
type AttachmentIntake interface {
	Receive(connID string, chunk protocol.AttachmentChunkPayload) (attachmentID string, stored bool, err error)
	ReleaseConn(connID string)
}
```

`*attachments.Intake` satisfies it as landed. The seam's own signatures name only
`internal/protocol`, so the seams file gains no import — but the **handler** file
imports `internal/attachments` for the sentinels, which is the shape #1751's spec
settled ("`internal/*` packages return Go sentinels and the dispatch site maps
them at the call site via `errors.Is`"), notwithstanding the "imports neither X
nor Y" phrasing the neighbouring seams carry.

Nil seam ⇒ **inert but consumed**: the case selector still intercepts, the handler
logs one content-free record and replies nothing. That is #1984's posture and it
is what keeps every non-production construction site (unit tests, the fake-daemon
e2e harness, foreground/v1) compiling unchanged.

### Where the work runs — one routing decision, made once

`dispatchAppFrame` gains a `case protocol.TypeAttachmentChunk:` that hands the
frame to the conn's worker through the same non-blocking enqueue the v1
fall-through uses, then returns. The completing chunk hashes and writes up to the
per-upload byte bound, which is exactly the work #1491 had to move off Run for
`handleDebugBundleRequest`; running it inline would repeat that mistake.

`V2Session.appFrames` changes element type from `[]byte` to a two-field
`appFrameJob{plaintext []byte; attachment bool}`, and `routeAppFrame` branches on
the tag. The alternative — re-probing the envelope type inside the worker — was
rejected: it puts the same routing decision in two places that can silently
disagree, and `TestEveryInboundV2TypeHasHandler` reads `dispatchAppFrame`'s case
selectors as *the* registry of those decisions, so a worker-side copy would let a
future edit delete the case while the code kept working. Four sites touch the
channel (the field, its creation in `handleNoiseInit`'s open tail, the enqueue,
the worker's receive); the overflow branch and its close-at-4421 posture are
unchanged.

### The handler

`handleAttachmentChunk(ctx, s, env)` in the new `internal/relay/v2session_attachment.go`,
running on the worker:

1. Nil-seam guard ⇒ one Debug record, return.
2. `json.Unmarshal` the payload. A decode failure is **rejected, not tolerated**,
   and nothing about it is echoed or logged — `AttachmentChunkPayload` nests
   `data`, `size` and `sha256`, so a tolerated failure could hand `Receive` a
   populated `attachment_id` with truncated bytes, which is the partial-population
   hazard #1984 documented. The reject answers `attachment.invalid_chunk`: a frame
   that does not decode is a frame whose framing claims cannot be read.
3. `Receive(s.connID, payload)`, then the three-way answer:
   - `err != nil` ⇒ map to a code and reply one `error` envelope, `InReplyTo` the
     chunk's envelope id.
   - `stored` ⇒ reply `attachment_stored` with `AttachmentStoredPayload{AttachmentID: id}`,
     `InReplyTo` the **completing** chunk's envelope id, using the id `Receive`
     returned rather than the decoded payload's.
   - otherwise (accepted, waiting for more) ⇒ **reply nothing**. This is what most
     chunks of a healthy upload get. `ErrIncomplete` never reaches here; `Receive`
     is the one place that interprets it, and a handler treating every non-nil
     error as a refusal would answer one reject per healthy chunk.

Replies reach the wire through `forwardToRun`, which parks the reply on
`m.appReply` for Run to seal under `s.send` — the single-owner-cipher invariant.
The handler never touches `s.send`.

### The sentinel → code map

One `errors.Is` arm per **code**, not per sentinel. Ten of the twelve reachable
sentinels were already committed by landed doc blocks; the two open ones are
decided here, and both **fold into the published vocabulary** rather than minting
a code — #1751 declared that vocabulary in one place precisely so the
implementations answering from it would not each invent a name, and a new code
would need a `codes.go` constant plus a published § Error codes row, which is a
protocol-publication slice rather than this one.

| Wire code | Sentinels |
|---|---|
| `attachment.invalid_chunk` | `ErrInvalidDeclaration`, `ErrTotalChunksMismatch`, `ErrIndexOutOfRange`, `ErrDuplicateIndex`, **`ErrUnknownUpload`** |
| `attachment.integrity_failed` | `ErrSizeMismatch`, `ErrDigestMismatch` |
| `attachment.too_large` | `ErrUploadTooLarge` |
| `attachment.too_many_uploads` | `ErrTooManyUploads` |
| `attachment.storage_failed` | `ErrInvalidID`, `ErrNotContained`, `ErrWriteFailed`, **`ErrNoConversation`**, and any error matching no arm |

**`ErrUnknownUpload` → `attachment.invalid_chunk`.** The sentinel's real meaning
is narrow: the pair's entry vanished between `Lookup` and `Deliver` — reaped for
idleness, or released by this conn's teardown. Chosen against *"your transfer
expired"*, per the refinement note: a client resuming at a non-zero index against
a restarted daemon is **admitted** into a fresh transfer under #1896's pinned
fork, not refused, so that case never reaches this arm. `invalid_chunk` already
carries the class "this chunk cannot be placed against a transfer"; this is the
sub-case where there is no transfer state left to place it against. Its published
`retryable: no` is the *correct* client advice here and not merely tolerable: a
naive resend of this one chunk would be admitted as a fresh transfer that can
never complete and idles out, so "rebuild the transfer, do not resend this frame"
is exactly right. One clause of the published row does not transfer — *"resending
the same frames reproduces it"* is false for an expired transfer, where resending
**all** frames would succeed — and that clause is a rationale in the row, not the
contract the row states; noted here rather than papered over.

**`ErrNoConversation` → `attachment.storage_failed`.** Every clause of the
published row is true of it: a *verified* attachment (`Deliver` returned assembled,
digest-checked bytes) *could not be written to the host* (there is no destination
to file it under), the message is *static* so the daemon's routing state stays off
the wire, and *retryable after a backoff* is right — it clears the moment the
daemon routes to a conversation, and a hot-loop re-upload of an already-transmitted
file is the exact behaviour that row forbids. It shares the code with `ErrInvalidID`
without collapsing what `ErrNoConversation`'s doc block argued for: that block
objects to routing an unrouted daemon through `EnsureDir("")` and having a client
told *its own id* was malformed. `attachment.storage_failed` says neither "your id
is malformed" nor "the daemon is unrouted" — it says the host could not file it,
back off — so the distinction the sentinel exists to preserve was never one the
wire carried.

**No default drop.** An error matching no arm is still answered, with
`attachment.storage_failed` — the honest generic for a daemon-side condition of
unknown cause, retryable and static.

Every reply carries a **static** per-code message constant. `err` is never
interpolated, replied or logged, on any arm: `EnsureDir` and `Store` wrap host
paths and the daemon's own conversation id into their refusals, and AC 5 bans a
host path from a log line as well as from the wire.

### Teardown

`closeWith` gains an `AttachmentIntake.ReleaseConn(s.connID)` call in the existing
per-conn cleanup cluster, beside the `close(s.done)` and the `m.queues` delete.
Nil-guarded. `ReleaseConn` is documented as carrying no single-feeder precondition
— it is remove-only under the registry's own lock and touches no accumulator — so
running it on the teardown goroutine while the worker may still be inside
`Receive` is the documented, intended shape.

### `cmd/pyry` wiring

- `resolveInstanceDirPath(name)` in `main.go`, joining the `resolve*Path(name)`
  family: `~/.pyry/<sanitized-name>`, with the same CWD-relative fallback when
  `$HOME` cannot be resolved. This is the parent `resolveRegistryPath` and
  `resolveConversationsRegistryPath` already build under.
- An adapter in `relay.go` over the existing cursor — not new state:
  `func() (conversations.ConversationID, bool)` returning
  `active.CurrentConversation()` typed, comma-ok false on `""`. The empty check is
  redundant with `Intake.resolveConversation`'s own and kept anyway: `false` at
  this seam must mean "no conversation", and a caller that returns `("", true)` is
  lying to a contract it does not own. `boundSessionIDForActive` is the precedent.
- `attachments.NewIntake(resolveInstanceDirPath(w.instanceName), adapter)`
  constructed in `startRelayV2` beside `modalResolver` / `questionResolver`, and
  assigned into the config literal. One per daemon, which is the ceiling's meaning.

### Guard test

`TypeAttachmentChunk` moves from `excludedTypes` to `inboundTypes` as
`"switch-intercepted"`, beside the twelve there. Its three `#1744` cites are
repaired to #1897 in the same edit — the in-a-file-already-edited rule, #1984's
third commit as precedent. The repo-wide `#1744` sweep is not this ticket's.

## Concurrency model

No new goroutine. The handler runs on the existing per-conn `appFrameWorker`,
which is spawned in `handleNoiseInit`'s open tail and exits on `s.done` (closeWith)
or `runCtx` (Run exit) — an unchanged lifecycle with an unchanged shutdown path.

`Receive`'s **single-feeder precondition** is discharged structurally: exactly one
`appFrameWorker` per session, the worker routes frames strictly FIFO with one
frame fully handled before the next is dequeued, and the registry key includes the
conn id. A second worker for a live conn id cannot exist — `handleNoiseInit` on an
already-open conn id is the *re-key* path, which reuses the session and its worker.
See § Security review for the conn-id-reuse case and its discharge.

`ReleaseConn` runs on Run concurrently with a worker's `Receive` for the same
conn. Safe by that method's own contract: remove-only under the registry's mutex,
no accumulator touched. A teardown landing between `Deliver` and `Store` still
files those bytes, which `Intake` documents as accepted rather than mitigated.

The seam field and `s.connID` are construction-time immutable, read lock-free off
Run exactly as `m.cfg.Handlers` and `s.device` already are in `routeAppFrame`.
Nothing here touches `s.send`, `s.recv`, session state or a timer.

## Error handling

- **Decode failure** ⇒ `attachment.invalid_chunk`, nothing echoed or logged about
  the failure itself.
- **Every `Receive` refusal** ⇒ one `error` envelope, correlated, with a static
  per-code message. Six arms; no path drops a refusal silently.
- **Marshal failure** on either reply ⇒ Warn (event + conn id + code) and drop,
  the package's established defensive branch for a closed struct of strings.
- **`forwardToRun` returning false** (session or manager tearing down) ⇒ return;
  the reply is abandoned exactly as `routeAppFrame` abandons its drain.
- **Reply dropped at the seal** by `forwardAppReply`'s `V2StateOpen` /
  `transportDown` gates ⇒ unchanged, out of this handler's hands.

## Testing strategy

Table-driven, stdlib only, in `internal/relay/v2session_attachment_test.go`,
against a `fakeAttachmentIntake` stub recording `(connID, chunk)` calls and
returning a scripted `(id, stored, err)`. Driving real bytes through
`attachments.Intake` would be testing #1896 again; the stub is what makes the
three-way answer and the map addressable one row at a time.

- **AC 1** — an `attachment_chunk` sealed onto an open session reaches the seam,
  and `TestEveryInboundV2TypeHasHandler` (already in `cmd/pyry`) passes with the
  entry moved. That the work runs off Run is pinned the way `#965`'s suite pins
  it: a seam whose `Receive` blocks does not stall a second conn's frame.
- **AC 2** — three rows over one fixture: completing chunk ⇒ exactly one
  `attachment_stored` naming the attachment with `InReplyTo` equal to *that*
  chunk's envelope id; accepted-incomplete ⇒ **zero** noise_msg for the conn;
  refusal ⇒ exactly one `error`. The incomplete row is the one that catches a
  handler reading `err != nil` the usual way.
- **AC 3** — one table row per sentinel, all twelve plus a
  `errors.New("unmapped")` row, asserting the dotted code on the decoded reply.
  The two chosen codes get their own named rows so the choice is pinned by name.
- **AC 4** — teardown with an upload in flight calls `ReleaseConn` exactly once
  with this conn's id; and a nil seam tears down without a panic.
- **AC 5** — the log-scan shape #1984 used: drive the stored, incomplete, and
  refusing paths through a captured buffer and assert no line contains the
  fixture's distinctive filename, digest, chunk bytes, or a host path fragment;
  and assert the wire `error` message equals the static constant.
- **Nil seam** — the frame is consumed (no unknown-type reply reaches the wire)
  and nothing is replied.
- **Decode failure** — a **type mismatch inside otherwise-valid JSON**
  (`"total_chunks": "not-a-number"`), not garbled bytes, which `json.RawMessage`
  cannot carry through `json.Marshal`. The binding assertion is `Receive` called
  **zero** times, with `attachment_id` populated ahead of the failing field.
- **v1 frames unchanged** — an ordinary app frame still routes through
  `dispatch.Route` after the channel's element type changed.

Verification is touched-scope: `go test -race ./internal/relay/... ./cmd/pyry/...
./internal/attachments/...`, `go vet ./...`, `go build ./cmd/pyry`.

## Open questions

1. **Does `routeAppFrame` need its own nil-seam short-circuit?** Resolved in
   design: no — the guard lives in `handleAttachmentChunk` so the interception is
   unconditional and the case selector stays load-bearing for the guard test.
2. **Static message wording per code.** Five short constants, resolved during
   implementation; the constraint (static, no host path, no id) is fixed here.
3. **Log level for the accepted-incomplete path.** Debug rather than Info —
   see § Security review, finding 7.

## Security review

**Verdict:** PASS

**Findings:**

- **[1 Trust boundaries]** No MUST FIX. The boundary is explicit and singular:
  `handleAttachmentChunk`'s `json.Unmarshal` into `protocol.AttachmentChunkPayload`
  is the only place remote bytes become typed values on this path, and everything
  downstream of it holds a typed payload that `attachments` re-validates on its
  own terms (`CheckDeclaration` at admission, `conversations.ValidID` inside
  `EnsureDir`, `SanitizeFilename` inside `Store`). The handler itself validates
  **nothing** and that is deliberate — a second validator here would be a second
  arbiter beside `Intake`, the mistake #1984 names. Worth stating because it reads
  like an omission: the handler passes `chunk.Filename`, `chunk.SHA256` and
  `chunk.Data` through untouched, and their entire defence is inside
  `internal/attachments`.
- **[3 File operations]** No MUST FIX, and the reason is structural rather than
  argued. No path in this slice builds a filesystem path. The one host path this
  ticket introduces is `resolveInstanceDirPath(name)` in `cmd/pyry`, whose input is
  the operator's own `-name` flag routed through the existing `sanitizeName`, not
  remote input. `attachment_chunk` carries no `conversation_id` and `Receive` takes
  no conversation parameter, so a client cannot steer bytes into another
  conversation's directory — the property `Intake`'s doc block calls structural,
  and this slice preserves it by wiring the resolver at **construction** time and
  never per frame. Permissions, symlink resolution, containment and the
  temp-file-plus-rename are all `EnsureDir`/`Store`'s and unchanged.
- **[6 Network & I/O]** No MUST FIX. Frame size is capped upstream by the v2
  envelope cap before AEAD; per-conn frame concurrency is capped by
  `appFrameQueueDepth` with a close-at-4421 overflow; daemon-wide upload capacity
  is capped by `maxInFlightUploads` inside the registry; per-upload bytes are
  capped by `ErrUploadTooLarge`. The one bound this slice adds is the teardown
  release — **without** `ReleaseConn` in `closeWith`, a phone that drops mid-upload
  holds daemon-wide capacity until `uploadIdleTimeout`, so a client that opens and
  drops conns can hold the whole budget indefinitely. That is AC 4, and it is a
  capacity-exhaustion fix rather than a tidy-up.
- **[7 Error messages, logs, telemetry]** MUST-FIX-class hazard, addressed **in
  this plan** rather than deferred: `Receive` returns errors that wrap host paths
  and the daemon's own conversation id (`EnsureDir`'s refusals, `Store`'s rename
  leg wrapping an `*os.LinkError` whose `Error()` prints the sanitised filename).
  The design therefore never interpolates, replies or **logs** `err` on any arm —
  the reply carries a static per-code constant and the log record carries the
  mapped code. AC 5's first clause bans a host path from a log line, so the
  stricter reading governs over "stay operator-side": the wrapped paths stay in the
  dropped error value and reach no sink. Loggable set, from the union of the
  declaring type's SECURITY block and § Attachments: conn id, attachment id, index,
  total. Banned: bytes, filename, digest, host path. The `internal/attachments` doc
  blocks asserting a stricter four-string ban that includes the id are wrong about
  the id — `attachment_stored` echoes it to the wire — and correcting them is
  explicitly out of this ticket's scope.
- **[7 Error messages, logs, telemetry]** SHOULD FIX — **log amplification on an
  unbounded `attachment_id`.** The id is client-chosen and its shape is checked
  only at `EnsureDir`, i.e. on the completing chunk; `MaxAttachmentIDBytes` is a
  ceiling for envelope arithmetic and is enforced nowhere. So a paired-but-hostile
  device can put tens of kilobytes of arbitrary bytes in `attachment_id` and have
  it logged on **every** chunk. Mitigation taken here: the per-chunk
  accepted-incomplete path logs at **Debug**, so only terminal outcomes (one per
  transfer, stored or rejected) reach Info. Not escalated beyond that: the same
  exposure already ships for `question_batch_id` (#1984, also unbounded, also
  attacker-authored, logged at Info), `slog`'s `TextHandler` escapes control bytes
  so a terminal-escape id cannot forge log structure, and the party is authenticated.
  Capping the logged id would be a house-wide rule, not this handler's to invent.
- **[8 Concurrency]** SHOULD FIX, discharged — **`Receive`'s single-feeder
  precondition under conn-id reuse.** Accumulators carry no mutex and are handed
  back off-lock, so two goroutines calling `Receive` for one conn id is a data
  race. Within a session it cannot happen: one `appFrameWorker` per session,
  strictly FIFO, and a `noise_init` on an already-open conn id is the re-key path
  that reuses that worker. The residual case is a relay that **reuses** a conn id
  for a new connection while the old session's worker is still inside `Receive`
  after `closeWith`. It is unreachable against the implementation this repo owns —
  `fakerelay` mints `c-<monotonic-seq>` — and against the real relay as
  `docs/protocol-mobile.md` documents it (`c-7f3a…`, a per-connection random). The
  manager already depends on that non-reuse elsewhere (`m.sessions` and `m.queues` are conn-id
  keyed, and re-key is distinguished from a fresh handshake by conn-id state). No
  defence added here: enforcing it would be a manager-wide identity change, not
  this seam's, and adding a lock inside `Intake` would contradict the precondition
  #1896 deliberately published. Recorded so the next reader finds the reasoning
  instead of re-deriving it.
- **[8 Concurrency]** No findings on the teardown race. `ReleaseConn` on Run
  concurrent with the worker's `Receive` is the documented exception to the
  precondition — remove-only, under the registry's own mutex, touching no
  accumulator. The worst outcome is a chunk answered `attachment.invalid_chunk`
  for a transfer torn down underneath it, on a conn whose reply
  `forwardAppReply`'s `V2StateOpen` gate then drops anyway.
- **[9 Threat model alignment]** No findings. § Security model's relevant threat
  is a **paired-but-hostile device**: authenticated, so every refusal must be a
  coded reply rather than a dropped frame (AC 3's no-default-drop), and every
  bound must hold per-conn as well as daemon-wide (AC 4). The disclosure posture
  of § Error codes is preserved — static messages, no host path, no filesystem
  error — and no new code is minted, so no client-visible contract changes.
- **[2 Tokens, secrets, credentials]** Not applicable: this path handles no token,
  key or credential. `s.device` is not read by this handler at all — unlike
  `handleQuestionAnswer`, there is no per-device decision to make, because upload
  capacity is bounded per conn and daemon-wide rather than per device.
- **[4 Subprocess execution]** Not applicable: no `exec`, no shell, no subprocess
  on this path.
- **[5 Cryptographic primitives]** Not applicable to new code. The digest compare
  is `accumulator.go`'s, unchanged; the AEAD seal stays on Run under `s.send` and
  this slice adds no encrypt call. The design's one crypto-adjacent obligation —
  never seal off Run — is met by routing every reply through `forwardToRun`.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02

## Revisions

### 2026-09-02 — implementation

**No design departure.** The three Open Questions resolved as the plan predicted,
so nothing above was rewritten:

1. No nil-seam short-circuit in `routeAppFrame` — the guard stayed in
   `handleAttachmentChunk`, so the case selector is unconditional.
2. Five static message constants, one per code, named `msgAttachment*`.
3. The accepted-but-incomplete path logs at Debug; only the terminal outcomes
   (stored, refused) reach Info.

One thing the plan under-specified and the code settled: the handler takes the
**plaintext**, not `dispatchAppFrame`'s already-probed `Envelope`, and decodes it
a second time. That keeps `appFrameJob` two fields wide, and the cost is the same
two-decode total a v1 application frame already pays (the probe, then
`dispatch.Route`'s).

**Mutation testing — four mutants, all red on their predicted test**, run under
`go test -overlay` so nothing was written to the worktree:

| Mutant | Predicted red | Result |
|---|---|---|
| `if !stored` never taken (answer the incomplete chunk) | `..._AcceptedChunk_AnswersNothing` | red, plus two over-determined |
| `ErrUnknownUpload` dropped from the `invalid_chunk` arm | `..._SentinelsMapToWireCodes` | sole red |
| `"err", err` added to the refuse record | `..._CarriesNoBannedStrings` | sole red |
| `ReleaseConn` call deleted from `closeWith` | `..._TeardownReleasesConn` | sole red |

The first is over-determined because the accepted path's Debug record is the
synchronisation knob two other tests wait on; its named test is still the one that
fails on the assertion the mutant targets.

**Measured size, against the `Estimate:` line's ~1150.** Total written work is
**1206** lines of code and tests (244 inserted across the six modified files, 339
in the new handler, 623 in its test), plus **436** lines of spec — **1642** all in.
That is 1.4× the estimate and 2.0× the 800-line ceiling. The overage is in tests
and doc comments rather than in logic: the handler's own executable body is about
90 lines. Recorded here as the evidence the ticket's `needs-human:sizing` comment
asked for — the run did **not** exhaust its budget, so by that comment's own
standard this is not yet grounds for tightening the ceiling.
