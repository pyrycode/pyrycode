# #2144 — prove the three attachment-destination flows against the fake daemon

Test-only slice. **Zero production source files.** One new file under `internal/e2e/`
plus a final phase appended to the existing upload test.

## Files read

| Path | Symbols that matter | Why it matters |
|---|---|---|
| `internal/e2e/relay_v2_attachment_upload_test.go` | `TestRelayV2_AttachmentUploadMultiChunk`, `attachmentFixture`, `sendAttachmentChunk`, `nextAttachmentEnvelope`, `waitForDaemonEvent`, `assertNoAttachmentLeakInLogs` | The harness these ride beside. Its doc block is the current map: the never-messaged *upload* is already asserted here, its single-reader discipline is the rule every new drain must obey, and four of its five helpers are reused unchanged. |
| `internal/e2e/per_conversation_eviction_test.go` | `dialHelloPhone`, `createConversationViaPhone`, `drainForReply`, `startPerConvHarness` | The wire-minting machinery. `createConversationViaPhone` is how flows 2 and 3 get two conversations without a second seeder, since `seedBoundConversation` overwrites and cannot seed A and B. |
| `internal/e2e/harness.go` | `seedBoundConversation`, `seedBootstrapRegistry`, `StartStreamInteractiveWithRelay`, `shortHome`, `mustJSON` | The starter both new tests use, and the seeder whose "row must exist before the daemon starts" constraint is what a wire-minted conversation sidesteps. |
| `internal/relay/v2session_attachment.go` | `handleAttachmentChunk`, `rejectAttachmentChunk`, `attachmentRejectFor` | The gate under test. Its step-3 block states both refusal arms, their shared wire code, and that the discriminator is the daemon-authored `reason` — which is exactly why flow 3 pins the reason and not only the code. |
| `cmd/pyry/relay.go` | `KnownConversation` closure, `attachmentResolve` closure | `KnownConversation` is a live `w.convReg.Get`, so a wire-minted conversation passes the upload gate. `attachmentResolve` is what flow 1's ack proves ran. |
| `internal/relay/handlers/send_message.go` | `SendMessage`, `resolveAttachments` | Flow 1's observable. `Route` runs first (so the ack also witnesses the cursor stamp), then `resolveAttachments` refuses the whole message with `attachment.not_found` if any named id does not resolve under the conversation. |
| `internal/attachments/storage.go` | `EnsureDir`, `ResolvePath`, `ErrNotFound` | The on-disk layout every assertion addresses: `<instanceDir>/conversations/<conv>/attachments/<att>/<file>`. `ResolvePath` returns the first non-dot regular entry, which is what flow 1's ack depends on. |
| `internal/protocol/attachments.go` | `AttachmentChunkPayload`, `MaxAttachmentChunkBytes` (45000), its SECURITY block | The declaration contract (`total_chunks == max(1, ceil(size/bound))`, so a sub-bound fixture must declare exactly 1) and the never-log rule the new tests inherit. |
| `internal/conversations/id.go` | `ValidID` | 36 chars, lowercase hex, `4` at index 14, `8/9/a/b` at 19. Flow 3's unknown id must satisfy this so the test proves *membership* is the gate, not shape. |
| `docs/knowledge/features/v2-session-manager-state-machine-inbound-attachment-chunk-attachmentintake-seam.md` | § "The conversation gate runs before the seam, on every chunk" | Confirms the refusal lands on the first chunk before any byte is admitted — flow 3 therefore needs one chunk, not a transfer. |
| `docs/knowledge/features/e2e-harness-stream-interactive-harness-pattern-startstreamin.md` | § "Cursor-stamping only needs the ack, not the drain (#1898)" | **Changes how flow 2 is built.** `handlers.SendMessage` calls `Route` and only then enqueues and acks, so an observed ack already proves the cursor moved. Flow 2 stops at two acks instead of paying two ~20s full-turn drains. |

## Context

\#2142 published `conversation_id` on `attachment_chunk`; #2143 made the daemon
file an upload under the conversation the client names, validated against the
registry, instead of under the daemon-global follow-active cursor. Both ship
unit-level proof. What is missing is the operator-visible flow: a chunk over a
real Noise session, bytes on disk under a particular conversation, and a
following `send_message` that resolves the id.

Re-derived against `main` at `d8833c42`. #2143 removed the leading routed turn
from `TestRelayV2_AttachmentUploadMultiChunk`, so the never-messaged *upload* is
already green there. Three flows remain, one property: **the bytes land where the
client said.**

No ADR is warranted — this slice adds no design decision, it pins one that
#2143 already made.

## Design

### Flow 1 — the never-messaged conversation resolves (extends the existing test)

Appended as a final phase to `TestRelayV2_AttachmentUploadMultiChunk` rather than
rebuilt as a sibling. Its precondition *is* flow 1's precondition — a conversation
bound by `seedBoundConversation` but never routed, whose bytes are already stored
— and duplicating the whole multi-chunk transfer to reach it would cost ~120 lines
to observe one extra frame.

**Placement is load-bearing: the new phase runs AFTER `assertNoAttachmentLeakInLogs`.**
That helper snapshots `h.Stderr` at the moment it is called. A `send_message`
naming an attachment composes a prompt carrying the host path and the client
filename, and the run is at `slog.LevelDebug` via `-pyry-verbose`; keeping the
scan's snapshot strictly upstream of the send leaves the existing never-log claim
scoped to the phase that owns it instead of silently widening it to a path this
ticket does not audit.

Contract of the new phase:

- Send one `send_message` on the run's own envelope id, naming `knownConvID`,
  `attachment_ids: [attachmentID]`, and a short text.
- Read with the file's existing single reader `nextAttachmentEnvelope` — not
  `drainForReply`, which `t.Fatalf`s on a non-matching correlation and would
  report a bare timeout for the one outcome this phase must name. Loop until an
  `ack` correlated to that envelope id, and fail loudly on a `TypeError`,
  decoding `ErrorPayload.Code` so `attachment.not_found` is reported as itself.
- The **ack is the assertion**: `resolveAttachments` runs after `Route` and
  refuses the whole message if any id does not resolve under the named
  conversation, so an ack is only reachable when `ResolvePath` found the bytes
  under `knownConvID`. The existing hand-built path assertion cannot prove that —
  it addresses the directory itself, while the resolver is what a real client
  depends on.

### Flow 2 — the A-B-A misfile (new file, new test)

`TestRelayV2_AttachmentUploadNamesConversationNotCursor`.

Sequence: pair → `StartStreamInteractiveWithRelay` → `dialHelloPhone` →
`createConversationViaPhone` ×2 (A then B) → `send_message` on A, await ack →
`send_message` on B, await ack (cursor now on B) → one single-chunk
`attachment_chunk` naming **A** → `attachment_stored`.

- Two acks, not two full turn drains, per the harness overview's §
  "Cursor-stamping only needs the ack". The acks are ordered, so the cursor
  provably lands on B last.
- Wire-minted conversations, not a second seeder. `KnownConversation` reads
  `w.convReg` live, so a conversation created after startup passes the gate;
  `createConversationViaPhone` also binds a dedicated session eagerly, which is
  what makes `Route` succeed and the ack arrive.
- The reader loop must tolerate `assistant_delta` / `turn_state` pushes from the
  two enqueued turns; it switches on envelope type and reads on, obeying the
  single-reader nonce discipline `nextAttachmentEnvelope` documents.
- **Both halves are asserted, and the negative is made non-vacuous by construction.**
  The sweep runs **once over the whole `conversations/` directory**, not over B's
  subtree, and the claim is that it returns *exactly one* attachment file and that
  the one it returns sits under A — with its bytes equal to the sent bytes. A
  B-subtree sweep would answer empty just as readily against a typo'd path or a
  broken walker as against a correct daemon, and B's directory need not exist at
  all for the negative to be true; a whole-tree sweep whose single hit is the
  positive proof is self-live, covers "nothing under B" a fortiori, and also
  catches a misfile under any third conversation. The negative half is what makes
  the flow discriminating — an A-only check stays green against a destination that
  silently fell back to the cursor if the harness happened to leave the cursor on A.

### Flow 3 — an unknown conversation is refused and writes nothing (new file, new test)

`TestRelayV2_AttachmentUploadUnknownConversationRefused`.

Sequence: pair → `StartStreamInteractiveWithRelay` → `dialHelloPhone` → one
single-chunk `attachment_chunk` naming a well-formed UUIDv4 that was never
registered.

Three assertions, pinned together because the code alone is weak — it also answers
a payload decode failure, an absent `conversation_id`, the declaration arithmetic
and `ErrUnknownUpload`:

1. **Wire:** a `TypeError` whose `InReplyTo` is that chunk's envelope id and whose
   `ErrorPayload.Code` is `protocol.CodeAttachmentInvalidChunk`.
2. **Log:** a `v2.attachment.chunk.refused` record carrying
   `reason="conversation is not one this daemon hosts"` — the daemon-authored
   discriminator `rejectAttachmentChunk` documents — **and not** the sibling arm's
   `conversation id is absent or empty`. This is the arm-specific witness; it is a
   positive pin, so it also proves the log buffer is live and the sibling-absence
   check is non-vacuous.
3. **Disk:** a sweep of the whole instance directory finds no attachment file
   anywhere, not a check of one expected path. Nothing was written because nothing
   was ever admitted: the gate runs per frame ahead of the intake seam. **The sweep
   runs after the error frame has been observed, and the ordering is the guard** —
   the refusal decision and the frame are emitted by the same handler in that
   order, so a sweep placed before the frame could read the tree ahead of a write
   the daemon had not yet declined to make.

Additionally the unknown conversation id must not appear anywhere in the captured
log. `handleAttachmentChunk`'s SECURITY block states the conversation id is logged
on no arm — it has not passed the gate — and this is the only tier where that claim
runs against the real daemon's handler. Non-vacuous by construction: assertion 2's
positive pin already proves the buffer holds this refusal's record.

Flow 3 needs its own daemon instance, not a phase of flow 2: the instance-wide
sweep would find flow 2's legitimately stored file.

### New helpers (both in the new file)

- `singleChunkAttachment(t, marker) (file []byte, digest string)` — a sub-bound
  fixture (well under `protocol.MaxAttachmentChunkBytes`) with a position-dependent
  fill and an embedded ASCII marker, plus its lowercase-hex sha256 over the same
  buffer the payload carries. Guards that the fixture cuts to exactly one chunk
  under the receiver's own division-then-remainder arithmetic, so a future move of
  the constant reddens instead of silently making the declaration invalid.
- `attachmentFilesUnder(t, root) (attachmentFiles []string, filesWalked int)` —
  walks `root` with `filepath.WalkDir` (which does **not** follow symlinks, so a
  link out of the tree is reported, never traversed) and returns every regular file
  sitting beneath an `attachments` path component, skipping dot-prefixed entries,
  plus the total count of regular files the walk visited. `filesWalked` is the
  vacuity guard for a sweep with no positive hit: flow 3 asserts it is non-zero
  (the instance directory holds `sessions.json` and `devices.json`), so "no
  attachment file" is a claim about a directory that was actually walked rather
  than about a mistyped root. Paths are returned relative to `root`, so a failure
  message names the layout without printing a host prefix. One helper serves flow
  2's whole-tree sweep and flow 3's instance-wide sweep, so the two negatives
  cannot drift apart.
- `awaitAttachmentOutcome` — a small reader loop over `nextAttachmentEnvelope`
  returning the first `attachment_stored` or `error` envelope, skipping turn
  pushes. Shared by flows 2 and 3 so the "wrong outcome" failure message names the
  error code once.

`sendAttachmentChunk`, `nextAttachmentEnvelope` and `attachmentFixture` are reused
from the existing file unchanged; no helper there is edited.

## Concurrency model

No new goroutines. Every test drives the daemon from its own test goroutine and
reads the phone conn serially — the receive `CipherState` is a lockstep counter and
a second reader desynchronises the nonce into a decrypt failure that reads like a
daemon bug. Daemon lifetime is the harness's `t.Cleanup`. The only synchronisation
is deadline-bounded polling: `waitForDaemonEvent` for the log record, and bounded
receive deadlines on the wire.

## Error handling

Every failure path names the milestone rather than reporting a bare timeout, per
`nextAttachmentEnvelope`'s stated reason for answering instead of `Fatal`ing:

- Flow 1: an error frame is decoded and its code reported, so
  `attachment.not_found` (the resolver missed) is distinguishable from a timeout
  (nothing arrived).
- Flow 2: a refusal instead of `attachment_stored` is reported with its code; a
  file found under B is reported with its path relative to the instance dir.
- Flow 3: the missing log record is reported by event name only —
  `waitForDaemonEvent` never prints the buffer, because this harness runs at Debug
  and interpolating it would publish pairing material into CI output. New failure
  messages inherit that rule: no message in either new test prints captured daemon
  stderr, the fixture bytes, the digest, or a client filename.

## Testing strategy

This is the fake-daemon tier, inside `make check` (`-tags e2e`). Not
`internal/e2e/realclaude`: no flow here needs a live claude, so the ticket is
deliberately not `needs-real-claude`.

RED before GREEN, per flow, using the deterministic overlay technique (`go test
-overlay`, no worktree writes):

- Flow 1 — revert `resolveAttachments`' confinement so it resolves against a
  fixed wrong conversation: the ack must become `attachment.not_found`.
- Flow 2 — restore the pre-#2143 destination by making `handleAttachmentChunk`
  pass the daemon's cursor conversation to `Receive` instead of
  `chunk.ConversationID`: the bytes must land under B and both halves must redden.
- Flow 3 — remove the `KnownConversation` arm: the chunk must be admitted, no
  error frame arrives, and the sweep must find a file.

Verification gate (§ B2 scope): `go test -race -tags e2e ./internal/e2e/...`,
`go vet ./...`, `go build ./cmd/pyry`. The whole-module race suite is the
verifier's gate.

## Open questions

1. **Does the enqueued turn from flow 2's two acks produce enough wire traffic to
   starve the attachment reader's deadline?** Expected no — the drain gate forwards
   only events whose sink tag equals the active session, so A's turn is dropped
   outright and only B's reaches the phone. Resolve by measurement on the first
   green run; if the loop needs a longer deadline, record the number and why.
2. **Does `send_message` in flow 1 need the bootstrap child to be live?** Expected
   no — the ack is accept-into-backlog and `resolveAttachments` runs before
   `EnqueueDelivery`, so the resolver's verdict is reached whatever the child is
   doing. Resolve on the first green run.

Each is resolved in Phase B; anything that changes the design above is recorded in
a `## Revisions` entry.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings.** The inbound `attachment_chunk` boundary is
  this slice's subject, not something it moves: the boundary stays exactly where
  `handleAttachmentChunk`'s step-3 gate puts it, and every test is a pure observer
  over the real daemon binary — no production file is edited, no seam is faked,
  no gate is bypassed. The one place the design deliberately *relaxes* a boundary
  is the RED mutants in § Testing strategy; those run through `go test -overlay`
  in a throwaway process against a temporary `HOME` and are never written into the
  worktree, so no relaxed build can be committed.

- **[Tokens, secrets, credentials] SHOULD FIX — pin the no-stderr-dump rule at
  each new failure site.** Every test here holds the pair token and the server
  static pubkey, and runs the daemon at `slog.LevelDebug` via `-pyry-verbose`, so
  a failure message that interpolates `h.Stderr.String()` (or the package's
  `stderrTail`) would publish pairing material into CI output —
  `waitForDaemonEvent`'s doc block already bans exactly this and is the precedent
  to copy. Phase B: no new failure message in either test calls `stderrTail` or
  reads the captured buffer for interpolation; the missing event name is the
  diagnostic. No lifecycle concern otherwise — the tests mint no token, and each
  run's pairing material dies with its `shortHome`.

- **[File operations] No findings, with two decisions made explicit.**
  `attachmentFilesUnder` uses `filepath.WalkDir`, which does not follow symlinks,
  so a link pointing out of the instance tree is reported as an entry rather than
  traversed into the host filesystem — the sweep must not become a reader of
  arbitrary paths. No path in either test is built from remote-authored input:
  every component is a test constant or a server-minted UUID, so there is no
  traversal surface. TOCTOU is real and is answered by ordering rather than
  locking — flow 3's sweep runs strictly after the error frame, because the
  refusal decision and that frame come from the same handler in that order (see
  the flow-3 assertion 3 note). Permissions are unchanged: the tests assert modes,
  they create nothing under the instance directory.

- **[Subprocess execution] Not applicable, with the reason.** No new `exec`; the
  harness's `spawnWith` owns argv and env for both `pyry` and the fakeclaude
  child, and neither new test adds a flag or an environment variable. The only
  test-authored values reaching a child process are flow 2's `send_message` text
  and flow 1's prompt, both fixed ASCII, and neither goes through a shell.

- **[Cryptographic primitives] No findings, two properties load-bearing.** The
  fixture digest is `crypto/sha256` computed over **the same buffer the payload
  carries**, so the declaration and the bytes share provenance and a generator bug
  cannot manufacture a self-consistent green. The fill is deterministic
  (`byte(i*7+3)`) and not `math/rand`: a random fill would make a byte-comparison
  failure unreproducible. The nonce hazard is the one to respect — the receive
  `CipherState` is a lockstep counter, so every new drain loop must be the run's
  single reader and must decrypt every `noise_msg` in arrival order; a second
  reader desynchronises the nonce into a decrypt failure that reads like a daemon
  bug rather than like the test's own mistake.

- **[Network & I/O] No findings.** Every wire read is deadline-bounded and every
  log poll is `waitForDaemonEvent`'s bounded loop, so no assertion can hang past
  its own budget and convert a bounded failure into a package-level timeout kill.
  The per-upload byte bound and `MaxAttachmentChunkBytes` are not exercised — both
  new fixtures are sub-bound by construction and guard that they cut to exactly
  one chunk, so a future move of the constant reddens the guard instead of
  silently invalidating the declaration.

- **[Error messages, logs, telemetry] No findings; two are assertions rather than
  risks.** Flow 3 asserts the unknown conversation id appears nowhere in the
  captured log — `handleAttachmentChunk`'s SECURITY block states the conversation
  id is logged on no arm because it has not passed the gate, and this is the only
  tier where that claim runs against the real handler. Flow 1's placement decision
  keeps `assertNoAttachmentLeakInLogs`' snapshot strictly upstream of the
  `send_message`, so the existing never-log claim stays scoped to the phase that
  owns it rather than being silently widened over a composed prompt carrying a
  host path. Sweep failures report paths relative to the instance directory, so
  the layout is legible without a host prefix in CI output.

- **[Concurrency] No findings.** No new goroutines, so there is no lifecycle to
  leak; daemon lifetime is the harness's `t.Cleanup`. Neither new test calls
  `t.Parallel()` — deliberately, matching the family: two concurrent daemons plus
  their fakerelays and spawned children would contend for the active-session cap
  and make the cursor ordering flow 2 depends on non-deterministic. The
  check-then-mutate hazard in the design is flow 3's sweep, answered by ordering
  above.

- **[Threat model alignment] Partly OUT OF SCOPE, named.** The relevant threat in
  `docs/protocol-mobile.md` § Security model is a paired-but-hostile client
  steering bytes into a conversation it should not reach. `AttachmentChunkPayload`'s
  SECURITY block states the posture: naming a conversation is not authorization,
  and what bounds the client is confinement — registry validation *plus*
  `attachments.EnsureDir` refusing an escaping directory. Flow 3 is the end-to-end
  proof of the **registry-validation half only**. The `EnsureDir` containment half
  — a conversation or attachment id that is well-formed to the wire but escapes on
  the path join — is **out of scope here**: this ticket's acceptance criteria do
  not name it, it is unit-covered in `internal/attachments` against `EnsureDir`
  directly, and adding it would put a fourth flow in a three-flow ticket. A future
  ticket wanting the e2e half of confinement should say so explicitly rather than
  reading flow 3 as covering it.

**Fixed during this pass (was MUST FIX, revised before commit):** flow 2's
negative half originally swept conversation B's subtree for "no attachment file
under B". That check answers empty just as readily against a mistyped path, a
broken walker, or a B directory that legitimately never exists as it does against
a correct daemon — a negative with no liveness control, which is the vacuity shape
this repo has been bitten by before. The design now sweeps the whole
`conversations/` tree once and asserts the result is *exactly one* attachment
file, under A, with the sent bytes: the positive hit is the walker's own liveness
proof, "nothing under B" follows a fortiori, and a misfile under any third
conversation is caught too. `attachmentFilesUnder` additionally returns
`filesWalked` so flow 3's hitless instance-wide sweep carries the same guard.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-06

## Revisions

### 2026-09-06 — flow 1 became its own test; `fakephone` closes the conn on a receive timeout

**What changed.** § Design had flow 1 appended as a final phase to
`TestRelayV2_AttachmentUploadMultiChunk`. It is now its own test,
`TestRelayV2_AttachmentUploadOnNeverMessagedConversationResolves`, in the new file
beside the other two, driving its own single-chunk transfer into a
`seedBoundConversation` row that no turn is routed into.

**What drove it.** Measured, not reasoned: the first run failed at the send with
`fakephone write: failed to write msg: use of closed network connection`.
`fakephone.Client.ReceiveBytes` closes the conn when its deadline elapses, and
#1898's AC-3 quiet window ends by deliberately running that deadline out to prove
an absence — so by the time the appended phase ran, the conn was dead. No ordering
inside that test fixes it: proving the absence is what kills the conn, and flow 1
needs the conn alive afterwards.

**What the new contract is.** The rebuilt precondition is a conversation that is
*hosted and bound* (so the `KnownConversation` gate and `SessionRouter.Route` both
pass) and *never routed* (so the follow-active cursor is empty for the whole
transfer). That is what `seedBoundConversation` alone gives, and it is the same
precondition #1898 establishes — reproduced in a few lines rather than shared.
The ack is still the whole assertion.

**What it cost and what it bought.** The upload half is now driven twice across the
two files. That is the price of #1898's absence proof and is cheap: one single-chunk
transfer. It buys a real gain the appended phase did not have — flow 1 no longer
perturbs #1898's ordered observations at all, so its never-log scan keeps its
snapshot without a placement argument, and `relay_v2_attachment_upload_test.go`
takes only a doc-block pointer. The plan's flow-1 placement rationale (§ Design,
"Placement is load-bearing") is superseded by this entry.

### 2026-09-06 — Open questions resolved

1. **Wire traffic from flow 2's two routed turns does not starve the attachment
   reader.** Resolved green on the first run, as predicted: the shared
   `awaitOutcome` loop skips `assistant_delta` / `turn_state` and reads on, and no
   deadline needed lengthening. The 20s budget is slack for a loaded host, not a
   measured requirement.
2. **Flow 1's `send_message` does not need the bootstrap child live.** Resolved as
   predicted — `resolveAttachments` runs before `EnqueueDelivery`, so the ack
   arrives on accept-into-backlog whatever the child is doing.

### 2026-09-06 — RED proof: `-overlay` alone cannot mutate an e2e daemon

`go test -overlay` rebuilds only the *test* binary, and this tier drives `pyry` as a
separate process, so the first mutant run passed against unmutated production code.
The working shape is `go build -overlay=<mutant json> -o <bin> ./cmd/pyry` plus
`PYRY_E2E_BIN=<bin> go test`, with a control run against an unmutated pre-built
binary first — the methodology `docs/knowledge/features/e2e-harness-stream-interactive-harness-pattern-startstreamin.md`
already records for #1845. Results:

| Mutant | Flow 1 | Flow 2 | Flow 3 | #1898 |
|---|---|---|---|---|
| Control (unmutated, `PYRY_E2E_BIN`) | pass | pass | pass | pass |
| Destination no longer read from the chunk | **RED** (`attachment.not_found`) | **RED** (single hit at the wrong conversation) | pass | **RED** |
| `KnownConversation` arm never fires | pass | pass | **RED** (`attachment_stored` for an unhosted conversation) | pass |

Each mutant reddens exactly the flows that own the behaviour it breaks and no
others, so neither test is standing in for the other's proof.
