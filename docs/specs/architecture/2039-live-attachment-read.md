# #2039 — prove claude opens an attached file on the real-claude rung

One live test that closes the half of the 2026-05-16 decision the hermetic tier
cannot reach: not "the path reached claude's stdin" (#2038 proved that against a
scripted fake) but "claude, handed a path and told to read it, opened the file".

## Files read

- `internal/relay/handlers/send_message.go` → `composeAttachmentPrompt`,
  `resolveAttachments`, `SendMessage` — the composer under test, its empty-path
  identity case (which is what AC 3's mutant collapses the whole function to),
  and the ordering that makes `Route` run before any id resolves.
- `cmd/pyry/relay.go` → `attachmentResolve`, `attachmentIntake` — proof the live
  daemon wires a NON-NIL resolver and an intake over the same instance
  directory. A nil resolver fails closed, so an unwired seam would answer
  `attachment.not_found` and this test would red for a wiring reason.
  `attachmentIntake`'s conversation resolver reads `w.active.CurrentConversation()`
  — the follow-active cursor, which is why § Design has a cursor-stamp turn.
- `internal/e2e/relay_v2_attachment_upload_test.go` →
  `TestRelayV2_AttachmentUploadMultiChunk`, `attachmentFixture`,
  `sendAttachmentChunk` — #1898's upload driving. Re-derived, not imported: the
  `e2e` and `e2e_realclaude` build tags are disjoint. Its header carries the
  precondition this plan reuses ("the daemon must be on a conversation first").
- `internal/e2e/realclaude/harness_daemon_test.go` → `spawnBootstrapDaemon`,
  `driveHandshakeInteractive`, `seedBootstrapRegistry`, `seedBoundConversation`,
  `sealSendMessage`, `drainForAssistantReply` — the shared harness. The last two
  are the ones the ticket flags as insufficient; § Design says why neither is
  extended.
- `internal/e2e/realclaude/interactive_stream_liveness_test.go` →
  `writeStreamInteractiveConfig`, `drainForCompletedTurn` — the stream-json
  toggle and the two-milestone turn drain this file reuses verbatim for its
  cursor-stamp turn.
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` →
  `perConvHarness`, `sealEnvelope`, `drainForReply`, `perTurnReplyBudget` — the
  harness struct and the generic envelope sealer that removes any need to touch
  a shared helper.
- `internal/e2e/realclaude/interactive_stream_question_refusal_test.go` →
  `settleRefusedTurn`, `truncateString`, `questionTextLogCap` — the package's
  standing frame-loop shape and its rule for putting claude-authored bytes in a
  message.
- `internal/protocol/attachments.go` → `AttachmentChunkPayload`,
  `MaxAttachmentChunkBytes`, `AttachmentStoredPayload` — the wire shapes and the
  45000-byte stride.
- `internal/attachments/storage.go` → `EnsureDir`, `ResolvePath` — the on-host
  layout (`<instanceDir>/conversations/<conv>/attachments/<att>/<file>`) and the
  `conversations.ValidID` canonical-shape check both ids must pass.
- `docs/knowledge/features/attachments-package.md` § "Admission layer" — the
  lesson this plan acts on: a fixture sized at exactly `MaxAttachmentChunkBytes`
  is SINGLE-chunk, because `total_chunks == max(1, ceil(size/bound))` is an
  equality. #1898 derives the count from the constant rather than hardcoding it;
  this file does the same in the other direction.
- `docs/knowledge/features/e2e-realclaude.md` — the suite's placement contract:
  the `e2e_realclaude` build tag alone wires a file into `make e2e-realclaude`
  and `make preship`; no Makefile change (AC 2).

## Context

#2038 landed the composer. Its hermetic proof is that a `send_message` naming
`attachment_ids` reaches claude as the user's text followed by a daemon-authored
block naming each attachment's on-host path. That is one half of the 2026-05-16
decision — put the PATH in the prompt rather than inlining the bytes, because
inlining would duplicate them into the JSONL transcript the on-disk copy already
holds. The other half is a claim about a live model: that claude opens the path
with its ordinary Read tool. The fake claude in `internal/e2e` replays scripted
lines, so an assertion against it passes whether the path worked or not.

`internal/e2e/realclaude` is the only tier where the claim is testable. It runs a
real daemon against a real `claude`, and its interactive-stream tests already
drive a `fakephone.Client` over the encrypted v2 session.

No ADR is warranted: this ticket records no new decision, it supplies the missing
evidence for one taken on 2026-05-16 and already documented.

**Sizing overage, stated rather than hidden.** The refiner estimated ~850 lines
against a 800-line ceiling and declined to split, on the grounds that the only
available cut — the upload driving, separated from the assertion that rides it —
has exactly one consumer and the sizing floor forbids it. That reading is
correct and the floor wins over the ceiling. This plan lands under the estimate
anyway (one new file, no shared-helper edit, no production file touched: see
§ Design's "what this does not touch"). The depth gate was checked and is
informational only, since no split is proposed: `#2039 → #1745 → #1684`.

## Design

One new file, `internal/e2e/realclaude/interactive_stream_attachment_read_test.go`,
carrying one test, `TestInteractiveStreamAttachmentRead`, and the helpers below.

### What this does NOT touch

- **`sealSendMessage` is not extended.** It builds a `SendMessagePayload` with no
  `AttachmentIDs`, and the ticket leaves extending it open. It is not extended
  because it does not need to be: `sealEnvelope` already takes a whole
  `protocol.Envelope`, so a `send_message` naming attachments is one literal at
  the call site with no shared-file edit and no caller cascade.
- **`drainForAssistantReply` is not extended.** It returns on the first non-empty
  delta and discards the text, which is a liveness signal, not an accumulation.
  Widening it would change what fourteen callers assert. A local drain instead.
- **No production file changes.** Production source files created or modified: 0.

### Fixed identifiers

Three ids in the package's `<ticket>0000-…` convention, none of which collides
with an existing constant in the package: bootstrap pool
`20390000-0000-4000-8000-000000000001`, conversation `…0002`, attachment
`…0003`. All three satisfy `conversations.ValidID` (36 chars, `4` at index 14,
`8` at index 19), which both `EnsureDir` and `ResolvePath` check before either id
becomes a path component.

### The token

`crypto/rand` over 12 bytes, `hex.EncodeToString`d to 24 lowercase hex
characters, minted inside the test body. Never `math/rand`, never
`time.Now().UnixNano()`: AC 3 rests on the token being unguessable, and a
nanosecond timestamp is a value an assistant could in principle produce. 96 bits
is far past any guessing argument while staying short enough that a model echoes
it back without mangling.

The test writes it to a file under `t.TempDir()` — **never under `home` and never
under `workdir`**. That is load-bearing rather than tidy: claude's cwd is the
daemon workdir, and a token file sitting in it could be found by an ordinary
directory listing, which would green this test with the attachment path playing
no part at all.

Containment is `token + "\n"` and nothing else, filename
`pyrycode-2039-token.txt` — plain ASCII, inside `SanitizeFilename`'s
`[a-zA-Z0-9_.-]` allowlist, so it passes through unchanged.

### Sequence

1. **Harness** — `startAttachmentReadHarness(t) (*perConvHarness, string)`,
   modelled on the stream-liveness setup: `claude` on PATH or skip;
   `WithWorktreeAuthenticated` (skips cleanly with no credentials); workdir under
   the isolated HOME; `writeStreamInteractiveConfig` BEFORE the daemon spawns
   (the config is read once at startup); `pair`; `seedBootstrapRegistry` +
   `seedBoundConversation` (also read once at startup); `fakerelay`;
   `spawnBootstrapDaemon`, which passes `--dangerously-skip-permissions`, so a
   Read raises no modal and this file drives none; then dial and
   `driveHandshakeInteractive`.

2. **Cursor-stamp turn** — one ordinary `sealSendMessage` ("Reply with a single
   short word"), drained with `drainForCompletedTurn`. This is the precondition
   #1898's header names: `attachments.Intake` resolves the conversation once per
   COMPLETING chunk over the follow-active cursor, and before any route that
   cursor is empty, so the completing chunk answers `ErrNoConversation` and the
   dispatch arm maps it to `attachment.storage_failed` — a success-shaped stream
   that stores nothing. `handlers.SendMessage` stamps the cursor inside `Route`,
   on the successful-route path, before it enqueues.

   Drained to terminal idle rather than left running, unlike #1898's: with a real
   claude an undrained turn's frames interleave with everything after, and the
   accumulating drain in step 5 would then have to tell turn 1's deltas from turn
   2's. Draining costs one short turn and buys a wire that is provably quiet.
   `drainForCompletedTurn` also carries the package's two standing alarms
   (`unrecognized_message`, `rate_limited`) for free.

3. **Upload** — one `attachment_chunk` at index 0, `total_chunks` 1, `size` and
   `sha256` over the whole file, sealed with `sealEnvelope`. The chunk count is
   DERIVED from `protocol.MaxAttachmentChunkBytes` with the receiver's own
   division-then-remainder ceiling and guarded `!= 1`, never hardcoded. The
   package overview's admission lesson runs the other way here: a future shrink
   of the constant would make this fixture multi-chunk, and a hardcoded `1` would
   then send a declaration the receiver refuses, for a reason the failure message
   would not name.

   `attachment_chunk` carries no `conversation_id`, by design — the bytes land in
   the conversation the authenticated session is already on, which is why step 2
   exists and why nothing here can steer them elsewhere.

4. **Wait for `attachment_stored`** — a local drain, not `drainForReply`.
   `drainForReply` skips a `TypeError` silently and would report a bare timeout;
   the refusal CODE is the whole diagnostic here (`storage_failed` → the cursor,
   `invalid_chunk` → the declaration arithmetic, `integrity_failed` → the digest).
   The local drain returns on either the correlated `attachment_stored` or a
   `TypeError`, and fails naming the code.

   Then one deterministic precondition, different fabric from everything above:
   `os.ReadDir` on
   `<home>/.pyry/test/conversations/<convID>/attachments/<attachmentID>` must hold
   exactly one entry. Not an AC assertion — #1898 owns the bytes-on-host claim —
   but it separates "the file never landed" from "claude did not read it", which
   are the same red at step 5 otherwise. The DIRECTORY is named in any message,
   never the leaf: the leaf is a client filename, which § Attachments bans logging
   for a privacy reason sanitising does not lift.

5. **The message and the assertion** — `sealEnvelope` with a
   `protocol.SendMessagePayload` carrying `ConversationID`, `MessageID`, `Text`
   and `AttachmentIDs: []string{attachmentID}`. The text must ask for the
   contents: the daemon's block tells claude to read the files, it does not tell
   claude to say what it found.

   Before sending, the test asserts its own text does not contain the token —
   a two-line guard that keeps AC 1's "appears nowhere in the message text"
   honest against a later edit of the prompt rather than resting on review.

   Then `drainTurnText`: accumulate `assistant_delta` for the conversation until
   terminal `turn_state{idle}`, and assert the token is in the accumulation.

### Helpers (contracts, not bodies)

- `startAttachmentReadHarness(t *testing.T) (*perConvHarness, string)` — returns
  the handshaken harness and the conversation id.
- `mintAttachmentToken(t *testing.T) string` — 24 lowercase hex chars from
  `crypto/rand`; fails the test rather than falling back on a weaker source.
- `writeTokenFile(t *testing.T, token string) (path string, content []byte, digest string)`
  — writes under `t.TempDir()` at 0600, reads back, returns the lowercase-hex
  sha256 of what was read. Digest over the bytes that are actually sent, from the
  same buffer, so a fixture bug cannot make a self-consistent green.
- `uploadSingleChunk(t, h, envID uint64, attachmentID, filename string, file []byte, digest string)`
  — seals the one chunk after asserting the fixture is single-chunk under the
  published stride.
- `awaitAttachmentStored(t, h, envID uint64, attachmentID string, timeout)` —
  step 4's drain.
- `drainTurnText(t, h, convID string, timeout) string` — step 5's drain.
- `requireStoredFile(t, home, convID, attachmentID string)` — step 4's
  filesystem precondition.

Both drains follow the package's standing frame loop: read binary→phone frames in
receive order, decrypt EVERY `noise_msg` to keep the sequential receive nonce in
sync, skip a non-`noise_msg` control frame WITHOUT decrypting, and treat a
`TypeError` as a hard fail naming its code.

## Concurrency model

No goroutines. The three drains run strictly in sequence and are the ONLY reader
of the phone's receive stream — each decrypt advances the receive `CipherState`
exactly once, so a second concurrent reader desyncs the nonce into a decrypt
failure that reads like a daemon bug. The daemon's own goroutines are the
harness's concern and are torn down by `bootstrapDaemon.stop` under `t.Cleanup`,
SIGTERM then SIGKILL.

## Error handling

Every failure is a `t.Fatalf` naming the milestone that failed, not a bare
timeout, because on this tier a timeout is the default shape of every bug:

- upload refused → the `attachment.*` code;
- `attachment_stored` but no stored file → storage, not claude;
- `attachment.not_found` on the message → the resolver, the conversation
  binding, or an id that is not canonical — never claude;
- idle reached with no token → the measured outcome AC 3 exists to distinguish,
  reported with the accumulated reply BOUNDED by `truncateString` at
  `questionTextLogCap` and quoted with `%q`. Claude-authored bytes cross a trust
  boundary into a salvaged run log; nothing on that path strips terminal escapes.

If a live claude balks at a path outside its cwd, that measurement IS a finding
about the 2026-05-16 decision and gets recorded in this file's `## Revisions` and
in the test's header, the way this package's other probes record theirs — not
worked around silently.

## Testing strategy

The test IS the deliverable. Its own falsifiability is AC 3, and the mutation is
recorded in `## Revisions` after the run:

```
go test -overlay=<abs-path>/overlay.json -tags e2e_realclaude -count=1 -v \
  -run '^TestInteractiveStreamAttachmentRead$' ./internal/e2e/realclaude/
```

The overlay maps `internal/relay/handlers/send_message.go` to a copy whose
`composeAttachmentPrompt` returns `text` unconditionally — "composition disabled"
exactly, since the empty-path list is already that function's identity case. No
worktree write. Under it claude is handed no path, so the token can reach the
reply only by guessing or by some route other than the file, and the test must
red.

Verification gate for the branch: `go vet ./...`, `go build ./cmd/pyry`, and
`go test -tags e2e_realclaude -count=1 -run '^TestInteractiveStreamAttachmentRead$'
./internal/e2e/realclaude/`. `make check` never compiles this package.
**Read the count of `=== RUN` lines, never the exit code**: with no credentials
every test skips and exits 0, and when the package fails to build zero tests run
and it still exits 0 through a shell wrapper.

## Open questions

1. Does a live claude open an absolute path outside its cwd? The attachment
   lands under the daemon's instance directory, not under the workspace. Resolved
   by the first live run; recorded in `## Revisions` either way.
2. Does claude echo a 24-hex-char token byte-exact, or reformat it (case, spacing,
   chunking)? The assertion starts strict; if a run shows a case-folded echo the
   comparison folds case, with the reason recorded — a folded 96-bit match still
   proves the file was opened.
3. Is one cursor-stamp turn enough, or does the follow-active cursor need
   re-stamping between the turn and the chunk? Resolved by the first run; the
   `attachment.storage_failed` code names this cause specifically.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — two boundaries, both explicit. Claude's text
  reaches the daemon and then this test as `assistant_delta` payloads; it is
  untrusted and enters a message only through `truncateString` and `%q`, the
  rule `settleRefusedTurn`'s header states for the same reason. The token and the
  ids are test-authored and safe to print plain.
- [Tokens] No findings — `mintAttachmentToken` uses `crypto/rand`, 96 bits.
  Rejected explicitly: `time.Now().UnixNano()`, which the package uses for run
  nonces and which would be guessable in a way that weakens AC 3. Lifecycle is
  the test's own: minted per run, never stored outside `t.TempDir()`, removed by
  cleanup, never committed. It is a test nonce, not a credential — no rotation or
  revocation concept applies.
- [File operations] No findings, one scenario named. The vacuity hole here is a
  token file under `workdir`: claude's cwd is the daemon workdir, and a token
  sitting in it is findable by an ordinary listing, so the test would green with
  the attachment path playing no part. The design puts the file under
  `t.TempDir()`, a sibling of the isolated HOME. AC 3's mutant is the general
  answer to the whole category — under it no path reaches claude, so ANY
  surviving route for the token reddens the mutant run rather than passing
  silently. Modes: 0600 on the written file, matching what the daemon writes
  attachment bytes at. No traversal surface: both ids are compile-time constants
  that `conversations.ValidID` checks before either becomes a path component, and
  the test builds its one read path from `home` plus those constants.
- [Subprocess] No findings — the test adds no `exec.Command`. The daemon spawn is
  `spawnBootstrapDaemon` unchanged, including `--dangerously-skip-permissions`,
  which is what makes the Read raise no modal. The token travels as file content
  and as prompt text, never as an argv element and never through a shell.
- [Cryptographic primitives] No findings — `crypto/rand` for the token,
  `crypto/sha256` over the whole file for the declared digest, `encoding/hex`
  lowercase (the canonical form § Attachments compares for exact equality). The
  Noise `CipherState`s come from `driveHandshakeInteractive` unchanged; no key or
  nonce is reused, and the receive-nonce discipline is § Concurrency's subject.
- [Network & I/O] No findings — one chunk strictly under
  `MaxAttachmentChunkBytes`, with the count derived from the constant and guarded
  rather than hardcoded. Every drain is deadline-bounded; nothing here opens a
  listener or reads unbounded input.
- [Error messages, logs, telemetry] No findings, two obligations honoured. Any
  message naming the attachment location names the DIRECTORY, never the leaf —
  `ResolvePath`'s logging obligation, because the leaf is a client filename. And
  no failure path interpolates the daemon's captured stderr, which is #1898's
  `waitForDaemonEvent` lesson: this harness tees the daemon to `os.Stderr`
  already, and reprinting the buffer would publish every record it wrote into CI
  output. This test reads no daemon log at all.
- [Concurrency] No findings — no goroutines; three strictly sequential drains and
  exactly one reader of the receive stream. See § Concurrency model for why a
  second reader is a decrypt failure that reads like a daemon bug.
- [Threat model alignment] `docs/protocol-mobile.md` § Attachments, the
  confinement property: a client cannot steer bytes into another conversation
  because `attachment_chunk` carries no `conversation_id` to steer with. This run
  uses one conversation throughout and therefore exercises the property only
  positively. OUT OF SCOPE, and named as such: the cross-conversation REFUSAL arm
  stays with `send_message`'s hermetic coverage (`resolveAttachments`' one-refusal
  -fails-the-message rule) and #1898's on-host path assertion; no future ticket is
  opened for it, because both already hold it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-03
