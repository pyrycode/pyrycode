# #1898 — e2e: upload a multi-chunk attachment against the fake daemon

## Files read

- `internal/e2e/relay_v2_stream_send_test.go` → `TestRelayV2_StreamSendMessageDrainsTurn` — the whole pair → seed → start → interactive handshake → routed turn setup, copied wholesale as this run's precondition.
- `internal/e2e/relay_v2_debug_bundle_test.go` → `testV2DebugBundleStreamDecode`, `testV2DebugBundleErrorNoLeak` — the classify-in-a-loop-with-a-`default:`-arm shape for a chunked transfer over an open v2 conn, and the deadline-poll over `Harness.Stderr` this plan reuses as an ordering barrier.
- `internal/e2e/harness.go` → `shortHome`, `StartStreamInteractiveWithRelay`, `seedBootstrapRegistry`, `seedBoundConversation`, `readPersistedServerID`, `mustJSON` — the harness surface. `seedBoundConversation` fixes the instance directory as `<home>/.pyry/test`, which is what makes the on-disk assertion addressable.
- `internal/e2e/relay_v2_daemon_test.go` → `decryptInnerEnvelope`, `waitBinaryHello`; `internal/e2e/relay_v2_handshake_test.go` → `readInnerFrame`, `sendNoiseMsg`; `internal/e2e/handshake_interactive_helpers_test.go` → `driveHandshakeToOpenDaemonInteractive` — the wire primitives.
- `internal/relay/handlers/send_message.go` → `SendMessage` — routes THEN acks, so an observed ack is proof the cursor is stamped. This is why no turn drain is needed.
- `cmd/pyry/main.go` → `sessionRouter.Route` (stamps `activeConversation` on the successful-route path only), `activeConversation.CurrentConversation`, `resolveInstanceDirPath` (`<HOME>/.pyry/<name>`).
- `cmd/pyry/relay.go` → the `attachments.NewIntake` call — wired unconditionally over the follow-active cursor, so no env gate or config toggle is needed beyond what the stream-interactive harness already sets.
- `internal/relay/v2session_attachment.go` → `handleAttachmentChunk` (three-way answer; `chunk.accepted` at Debug, `chunk.refused` and `attachment.stored` at Info), `attachmentRejectFor`.
- `internal/relay/v2session.go` → `dispatchAppFrame`'s `TypeAttachmentChunk` arm — not capability-gated, tags the frame and hands it to `appFrameWorker`.
- `internal/attachments/intake.go` → `Intake.Receive` — the fork admits on FIRST TO ARRIVE, never on `index == 0`; the conversation is resolved once per COMPLETING chunk.
- `internal/attachments/admission.go` → `CheckDeclaration` — `total_chunks == max(1, ceil(size / protocol.MaxAttachmentChunkBytes))` as an equality; `internal/attachments/storage.go` → `Store` (dot-prefixed temp pattern, so a stored directory holds exactly one entry), `EnsureDir` (the `conversations/<conv>/attachments/<att>` layout).
- `internal/protocol/attachments.go` → `MaxAttachmentChunkBytes` (45000 raw, pre-base64), `AttachmentChunkPayload`, `AttachmentStoredPayload`.
- `internal/conversations/id.go` → `ValidID` — 36 chars, lowercase hex, `-` at 8/13/18/23, `4` at 14, one of `89ab` at 19. The attachment id fixture must satisfy it or the transfer is refused only on its completing chunk.
- `docs/protocol-mobile.md` § Attachments, "Chunking (the sender's obligation)" and "Reassembly & integrity (the receiver's rules)" — the published stride, and the deliberate weakening from `debug_bundle_chunk`'s strict `seq` to any-order arrival.
- `docs/knowledge/features/e2e-harness.md`, `docs/knowledge/features/attachments-package.md` — the package overviews for the two areas this touches.

## Context

The upload leg landed as a chain of independently unit-tested slices: accumulation and integrity (#1741), admission bounds, filename sanitisation, directory resolution, the byte write (#1782), the in-flight registry, the intake driver (#1896), the success reply's wire type (#1895), and the v2 dispatch arm plus production wiring (#1897). Nothing runs the whole chain in one process.

The failure this slice is built to catch is the quiet one: every layer accepts a chunk stream, the phone is told `attachment_stored`, and no bytes reach the host. A reply and a file are two observations and only the second is evidence, so this run makes both in one process against a real daemon.

`internal/e2e` has no attachment coverage today (`git grep -i attachment -- internal/e2e` is empty), so this is also the first fake-daemon exercise of the seam.

No ADR is warranted: this slice adds no production code and settles no design question the family has not already settled.

## Design

**One new file, `internal/e2e/relay_v2_attachment_upload_test.go`, behind the existing `//go:build e2e` tag. No production file changes.** The `e2e` tag is part of `make check`, which discharges AC-4 by construction.

One test function, `TestRelayV2_AttachmentUploadMultiChunk`, with no subtests: the four acceptance criteria are four observations of ONE transfer, and splitting them would need four transfers.

### Sequence

1. `shortHome`, then `RunBareIn(… "pair" …)` and `decodePairPayload` — the pairing token and the server static pubkey.
2. `seedBoundConversation(t, home, knownConvID, initialUUID)`, after the pair (which creates `<home>/.pyry/test`) and before the daemon starts (the registry is loaded once at startup). `seedBootstrapRegistry` is called by the harness starter, so it is not repeated here.
3. `fakerelay.New`, then `StartStreamInteractiveWithRelay(t, home, initialUUID, fr.URL()+"/v2/server")`. Chosen over `StartInWithEnv` for two properties it already carries: it seeds the bootstrap registry at `initialUUID`, and it passes `-pyry-verbose`, which puts the Debug-level `chunk.accepted` record on the captured stderr this design uses as an ordering barrier.
4. `readPersistedServerID`, `waitBinaryHello`, `fakephone.Dial`, `driveHandshakeToOpenDaemonInteractive` — an open, interactive v2 conn.
5. **Stamp the cursor.** One `send_message` for `knownConvID`; read frames in arrival order until the `TypeAck` correlated to its envelope id. **The ack is the whole precondition** — `SendMessage` calls `sessionRouter.Route` (which stamps) and only then enqueues and acks, so an ack proves the cursor is non-empty. The turn is deliberately NOT drained: the drain is `TestRelayV2_StreamSendMessageDrainsTurn`'s subject, costs ~20s of deadline, and proves nothing this ticket asserts.
6. **Upload, in reverse index order** (see below): chunk `index 1`, barrier, chunk `index 0`.
7. Classify frames until `TypeAttachmentStored`; assert AC-1.
8. Read the stored directory; assert AC-2.
9. Quiet window + a log-side negative; assert AC-3.

### Fixture arithmetic

`size` is `protocol.MaxAttachmentChunkBytes + 1234` — **strictly greater** than the bound, which is the input condition AC-1 states. Equality would give `max(1, ceil(45000/45000)) == 1` and a silently single-chunk run, so `totalChunks` is COMPUTED from the constant with the receiver's own `max(1, ceil(…))` form and guarded by an explicit `if totalChunks < 2 { t.Fatalf }` rather than hardcoded to `2`. A future move of the constant then reddens loudly instead of quietly proving one chunk.

Chunk 0 carries exactly `MaxAttachmentChunkBytes` raw bytes and chunk 1 the remainder — the published stride, which `CheckDeclaration` enforces as an equality on the derived count.

Bytes are position-dependent (`byte(i*7+3)`, `testV2DebugBundleStreamDecode`'s fill) so any reorder or off-by-one in reassembly changes the concatenation. A constant fill would make AC-2 pass against a receiver that appended in arrival order.

`sha256` is lowercase hex of the WHOLE file (`hex.EncodeToString` is already lowercase), repeated identically on both chunks, as is every other declaration field.

`attachmentID` is a canonical lowercase UUIDv4 satisfying `conversations.ValidID` — version nibble `4` at index 14, variant in `89ab` at 19. Nothing checks the shape at admission, so a non-canonical id transmits every byte and is refused only at `EnsureDir` on the completing chunk.

`filename` is plain ASCII within `SanitizeFilename`'s allowlist, so it survives unchanged and the stored name is predictable.

### Reverse index order — the one non-obvious decision

Chunks are sent **index 1 first, then index 0**, and this is load-bearing twice at zero extra cost:

- It separates "the chunk that COMPLETED the transfer" from "the chunk with the highest index". Sent in order, those two readings name the same frame and AC-1's `in_reply_to` clause cannot distinguish a correct implementation from one that correlates to the last index. Reversed, the reply must carry index 0's envelope id.
- It makes AC-2's "concatenated in index order" non-vacuous: arrival order and index order differ, so a receiver that appended rather than addressing by index produces different bytes.

`Intake.Receive`'s fork admits on FIRST TO ARRIVE and explicitly never on `index == 0`, and § Attachments publishes any-order arrival, so a leading index 1 is a conforming client rather than an edge case.

### The barrier between the two chunks

After the non-completing chunk, poll `Harness.Stderr` until it contains `event=v2.attachment.chunk.accepted`. `handleAttachmentChunk` writes that record on the accepted-but-incomplete arm and then returns, so once it lands no reply for that frame can still be in flight — which is what turns AC-3's "answered with no frame at all" into an assertion rather than a race. The completing chunk goes only after the barrier.

The alternative — send both, then check nothing extra arrived — cannot tell "answered nothing" from "answered late".

### Frame classification

Every daemon→phone frame advances the receive cipher exactly once, so all of them are decrypted in arrival order in a single loop on the test goroutine, classified by `Type`, with a `default:` arm tolerating the turn's own pushes (`turn_state`, `assistant_delta`) — `testV2DebugBundleStreamDecode`'s shape. A `TypeError` at any point in the upload phase is an immediate `t.Fatalf` naming the decoded `ErrorPayload.Code`, since the only errors reachable here are `attachment.*` rejects and the code is the diagnostic.

### On-disk assertion (AC-2)

`filepath.Join(home, ".pyry", "test", "conversations", knownConvID, "attachments", attachmentID)`, read with `os.ReadDir`. Assert exactly one entry — `Store`'s temp pattern is dot-prefixed and `SanitizeFilename` never returns a leading `.`, so a second entry means a leaked temp file — then that the entry's name equals the sent filename, then that `os.ReadFile` of it equals the fixture bytes.

The path is built textually from `home` while `EnsureDir` returns the symlink-resolved form; `os.ReadFile` follows symlinks, so the two agree on macOS where `/var` resolves to `/private/var`.

### Exactly one stored (AC-3)

Two independent observations:

- **Wire.** After the stored reply, drain for a bounded quiet window; any further `TypeAttachmentStored` or `TypeError` fails. Turn pushes are ignored. `fakephone.ErrReceiveTimeout` ends the window.
- **Log.** The captured stderr must contain no `event=v2.attachment.chunk.refused`. Non-vacuous by construction: the same buffer already yielded `chunk.accepted` at the barrier, so it is provably live rather than empty.

## Concurrency model

The test spawns no goroutine. The phone conn is read serially on the test goroutine — the single-reader discipline every v2 spec follows, and a requirement rather than a style choice, since `noise.CipherState` nonces are sequential and a second reader desyncs them.

Daemon-side concurrency is not this test's to arrange but is what it depends on: the two chunks are routed to one `appFrameWorker`, strictly FIFO, which is what discharges `Intake.Receive`'s single-feeder precondition. The barrier makes the test's own ordering independent of that anyway.

Teardown is `t.Cleanup` in reverse-registration order — phone close, harness stop, fakerelay close, temp home removal — all inherited from the harness helpers.

## Error handling

Every failure is a `t.Fatalf` or `t.Errorf` carrying the observation that failed:

- No ack within its deadline → the cursor was never stamped, so the upload would be refused with `attachment.storage_failed` after every byte. Named explicitly in the message, because that is the trap the ticket's technical notes call out.
- A `TypeError` during upload → fail naming the decoded `Code`; `attachment.storage_failed` points at the cursor, `attachment.invalid_chunk` at the fixture arithmetic, `attachment.integrity_failed` at the digest.
- The barrier's timeout → the daemon never logged `chunk.accepted`, so the first chunk was neither accepted nor refused; likely inert intake or a frame that never reached the worker.
- Missing stored reply within its deadline → the completing chunk did not complete the transfer.
- Directory missing / wrong entry count / byte mismatch → the reply said stored and the host disagrees, which is the exact failure this slice exists to catch.

Deadlines are seconds-scale (3s reads, ~10s for the ack and the stored reply, ~1s quiet window). No test-visible error is recovered from; the run fails.

## Testing strategy

The test IS the deliverable, so the strategy is what makes it non-vacuous:

- **Mutation check, run once by hand via `go test -overlay`, not committed:** nil the `AttachmentIntake` field at its assignment in `cmd/pyry/relay.go` and confirm the run reddens at the barrier. That is the seam the whole family joins at, and a run that greens with it dead is asserting nothing.
- **Reverse order** kills a receiver that appends rather than addressing by index, and kills a reply correlated to the highest index.
- **Position-dependent fill** kills a reassembly that is correct in length but wrong in placement.
- **Derived `totalChunks` with a `< 2` guard** kills the single-chunk degeneration at the bound.
- **The barrier plus the quiet window** kills a per-chunk reply, in both directions: one too early and one too many.
- **Directory entry count** kills a leaked temp file, which a bytes-equal-on-the-named-file assertion alone would miss.

No table: there is one transfer with one shape, and a table over one row is noise.

## Open questions

1. Does the un-drained turn's fakeclaude traffic ever interleave a frame between the completing chunk and its reply? The classification loop tolerates it by design, so the answer only affects whether the `default:` arm is exercised. **Resolve by observing the run.**
2. Is ~1s a long enough quiet window to be meaningful without being flaky? **Resolve by running the test; if a second stored were emitted it would be emitted by the same handler in the same millisecond, so the window bounds scheduling latency rather than a real delay.**

Both are settled during Phase B and any design change they force is recorded in a `## Revisions` entry.

## Security review

**Verdict:** PASS

This ticket adds no production code, so no category can find an exploitable design here in the ordinary sense. The adversarial question that IS live: this run is the first and only place the assembled upload leg meets a REAL `attachments.Intake` — where `EnsureDir` and `Store` build host paths and wrap them into errors — so where the plan declines to observe a published security obligation it could observe for free, that is a finding, not a scope boundary.

**Findings:**

- [Trust boundaries] **No finding, with a stated limit.** The one boundary is remote-authored chunk bytes entering the daemon, and it is explicit and singular: `dispatchAppFrame`'s `TypeAttachmentChunk` arm → `handleAttachmentChunk` → `Intake.Receive`. The plan's on-disk assertion pins the destination structurally — the path is built from `knownConvID`, the conversation the run ROUTED to, which the frame never names — so a regression that let a frame steer its destination has no field to steer with. THE LIMIT: with one seeded conversation, "landed under the routed conversation" and "landed under the only conversation" are the same observation. A second conversation would pin attribution, but confinement here is a property of `AttachmentChunkPayload` having no `conversation_id` at all, so a second conversation would test the cursor's correctness — a different subject, and `boundSessionIDForActive`'s. Not added.

- [Tokens, secrets, credentials] **SHOULD FIX — do not dump the daemon buffer into a failure message.** `testV2DebugBundleErrorNoLeak` interpolates the whole of `Harness.Stderr` into its `t.Fatalf`, and this harness runs the daemon at `slog.LevelDebug` via `-pyry-verbose`, so that buffer is chattier here than at any Info-level spec. A test failure would publish every Debug record — pairing token material included, if any subsystem ever logs it — into CI output. Phase B reports the MISSING EVENT NAME on a barrier timeout and never the buffer contents. The test mints no credential of its own; the pairing token and static keypair are handled exactly as every sibling spec handles them, and nothing durable is written outside the per-test temp home.

- [File operations] **SHOULD FIX — assert the stored file's mode.** The read path is safe (every component is a test-chosen constant under a private temp home; nothing user-supplied reaches a path), but the plan reads the bytes and ignores the mode. `Store` writes 0600 and `EnsureDir` creates 0700, and this run is the only place either is observed as produced by the real daemon under the real umask — the unit tier's `TestStore_*` runs in-process. One `Stat().Mode().Perm()` check on the stored file pins a genuine privacy property (a user's private file bytes on a multi-user host) for three lines. Added in Phase B. TOCTOU is not applicable: the test reads once, after the transfer is complete and the daemon is quiet.

- [Subprocess execution] **No finding.** The daemon and `fakeclaude` are spawned through the harness with fixed argv. No fixture value — filename, attachment id, digest, bytes — reaches an `exec.Command` argument on any path; they ride the encrypted wire exclusively. No `sh -c`, and the child environment is the harness's explicit `envSet`.

- [Cryptographic primitives] **No finding.** The Noise_IK handshake is the production one via `driveHandshakeToOpenDaemonInteractive`; the digest is `crypto/sha256` rendered lowercase by `hex.EncodeToString`, the canonical form § Attachments mandates for exact-equality comparison. The fixture fill is deterministic rather than `math/rand`, which is required rather than tolerated — the reorder assertion needs reproducible bytes, and this is not security-relevant randomness. Nonce discipline is discharged by the single-reader rule stated under Concurrency. One decision worth naming: the digest is computed over the SAME buffer the chunks are sliced from, so declaration and payload share provenance and a generator bug cannot produce a self-consistent green.

- [Network & I/O] **No finding.** Every cap in play is the daemon's and is exercised rather than asserted: `maxUploadBytes` (16 MiB, far above this fixture), `maxInFlightUploads`, and the 65519-byte application-envelope cap — which the 45000-byte chunk constant is derived to clear (45000 raw → 60000 base64 plus metadata), so no chunk of this fixture can trip `message.too_long`. Every read in the test carries a deadline, so no assertion can hang.

- [Error messages, logs, telemetry] **SHOULD FIX — add a never-log scan over the captured daemon log.** This is the pass's strongest finding. `protocol.AttachmentChunkPayload`'s SECURITY block and § Attachments ban logging the bytes, the filename and the declared digest, and `handleAttachmentChunk`'s doc extends the ban to the host path because `EnsureDir`'s and `Store`'s refusals wrap one. The unit tier's `TestV2Session_AttachmentChunk_CarriesNoBannedStrings` checks this against a FAKE intake that builds no path; nothing checks it against the real one. The run already holds the buffer and already scans it for the barrier, so the scan costs a handful of lines. Phase B adds: a positive pin that the attachment id DOES appear (the id is loggable by the declaring contract, and the pin is what stops the negatives greening against an empty buffer), and negatives for the filename, the declared digest, and the bytes. THE BYTES NEED MORE THAN ONE NEEDLE — a `[]byte` renders as base64 through `encoding/json` and as a decimal slice through `slog`, and neither matches a raw-ASCII search — so the fixture embeds a distinctive ASCII sentinel inside chunk 0's bytes and the scan carries both that sentinel and the base64 rendering of the run it sits in. The position-dependent fill is preserved everywhere else, so the reorder assertion is unaffected. Residual, stated rather than papered over: a leak rendered in some third form neither needle spells would still pass.

- [Concurrency] **No finding.** The test spawns no goroutine and reads the conn serially on the test goroutine, which is a correctness requirement (sequential Noise nonces) and not a style choice. Polling `Harness.Stderr` from the test goroutine while the harness's copier writes it is race-free: the field is a `*safeBuffer`, mutex-guarded, and the suite runs under `-race` in `make check`. Daemon-side, the two chunks are serialised by the one `appFrameWorker` per session, which is what discharges `Intake.Receive`'s single-feeder precondition — and the run does not depend on it anyway, because the barrier orders the two sends from outside.

- [Threat model alignment] **Scope stated, not a finding.** § Security model places a paired device inside the user's trust domain, so the live threat is a HOSTILE PAIRED DEVICE and the defences are the receiver's declaration cross-check, both resource bounds, the id-shape validation and the containment check. This run exercises NONE of the refusals: every one is unit-covered (`TestV2Session_AttachmentChunk_SentinelsMapToWireCodes` for the mapping, `internal/attachments`' own suites for the checks), and duplicating them over the wire would be a second ticket's worth of transfers. An end-to-end refusal run — a traversal-shaped attachment id, an oversized declaration, a digest mismatch — is genuinely uncovered at this tier and is OUT OF SCOPE here; it belongs to a follow-up rather than to this slice, whose four acceptance criteria are all about the success path.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02
