# #1845 — prove the model list reaches a connected client end to end

**Size:** s (test-only; 0 production source files, one new `*_test.go` under `internal/e2e`)

## Files to read first

Read these before writing anything. This list is the turn-1 data load — the design below assumes you have it.

- `internal/e2e/relay_v2_stream_rate_limit_test.go` → `driveRateLimitTurn` — **the template.** Copy its pair → seed → start daemon → fake relay → dial → interactive handshake → `sealSend` → `nextEnv` sequence verbatim up to the point this spec diverges. Also copy its two local closures (`sealSend`, `nextEnv`) rather than inventing a shared helper: seven sibling specs each declare their own pair, and that duplication is the established idiom here.
- `internal/e2e/stream_absent_transcript_respawn_test.go` → `killChild`, `waitForRunnerStatus`, `statusOrFatal` — the kill/respawn primitives. Same package, same `e2e` build tag, so call them directly. Note what its own assertions measured: a kill→respawn under `-race` lands near a second, and `RestartCount` settles at exactly 1.
- `internal/e2e/harness.go` → `StartStreamInteractiveWithRelay`, `seedBoundConversation`, `seedBootstrapRegistry`, `shortHome`, `readPersistedServerID` — what the harness seeds for you and what it does not. Read `seedBoundConversation`'s doc for the bootstrap-id/bound-id equality invariant.
- `internal/e2e/internal/fakephone/fakephone.go` → `ReceiveBytes` — **read the doc comment, it changes the design.** coder/websocket closes the underlying connection when the read context is cancelled, so a client that has once timed out cannot be reused. A poll loop of short-timeout receives is therefore not available; one deadline-bounded loop is.
- `internal/e2e/internal/fakeclaude/main.go` → `runStreamJSON`, `writeInitializeAck`, `initializeModels` — the canned two-entry answer and the fact that it is answered in both modes and under no rider. `initializeModels` is the expected-value source; transcribe its two rows as literals in the test (the fake is a separate main package, same discipline as the bogus needles in `relay_v2_stream_unrecognized_test.go`).
- `cmd/pyry/interactive_turn_v2.go` → `(*interactiveTurnEmitterV2).Handle`, specifically its `case turnevent.ModelList:` arm — the layer this e2e adds over the unit tests, and AC3's mutation point.
- `cmd/pyry/stream_turn_drain.go` → `startStreamTurnDrainV2` — the active-session gate the whole sequencing argument turns on.
- `cmd/pyry/relay.go` → `boundSessionIDForActive` — what the gate compares, and why it fails closed before the first route.
- `internal/streamsup/runner.go` → `Config.RequestInitializeOnSpawn` and the ask inside `runOnce` — one ask per spawn, fired right after `cmd.Start`, error absorbed at Debug.
- `internal/protocol/interactive.go` → `ModelListPayload`, `ModelOption`, and both `MarshalJSON` methods — the wire shape and, load-bearing for AC2, which of the two list fields is normalised and which is not.
- `docs/knowledge/features/e2e-harness.md` § "Why the bootstrap pool id and the bound conversation id must be equal" — the one invariant a new caller of this harness gets wrong, and its symptom (an unexplained timeout, not a clean seed-time failure).
- `docs/knowledge/features/fakeclaude-binary.md` § on the `initialize` answer — provenance of the canned rows and what the fake deliberately omits.

## Context

Four tickets built the model-list path in layers: #1839 asks each spawned child to `initialize`, #1840 retains the decoded list on the session (`sessionModelHold`), #1848 maps `turnevent.ModelList` onto the wire shape (`turnbridge.MapEvent`), #1849 emits it on the live interactive turn lane (`(*interactiveTurnEmitterV2).Handle`). Each is proved at its own seam against its own doubles.

Nothing proves the chain across process boundaries. The failure class this ticket covers — a sink wired to the wrong hold, a capability gate that never opens, an envelope dropped before it is sealed — leaves every unit test green. This adds the hermetic tier: a real `pyry` supervising a real child, a real client connection, no credentials, no network, inside `make check`.

No new harness scaffolding. The fake trio already carries everything: `writeInitializeAck` answers the ask unconditionally, `initializeModels` supplies the two contrasting entry shapes, and the respawn primitives exist.

**No ADR.** This adds a test at an existing tier against existing infrastructure; it decides nothing.

## Design

### The sequencing problem, and the route that solves it

The live lane emits once per child spawn to whatever interactive connections exist at that instant. There is no backfill for a client that connects later (#1846 owns that). The daemon spawns its bootstrap child eagerly at startup, so by the time a test can pair, dial and handshake, the ask, the reply and the emit have all happened. **A test that starts a daemon, connects a phone, drives a turn and waits for a `model_list` waits forever.**

So the test must make a child spawn *while the client is connected*, and only one of the two candidate routes delivers:

- **Kill and respawn (this design's route).** The respawned child keeps the session id, so its events still match the drain's gate: `startStreamTurnDrainV2` compares `env.sessionID` — the runner's construction-time tag — against `boundSessionIDForActive`, which resolves the active conversation's `CurrentSessionID`. Same id on both sides, before and after the kill.
- **A `new_session` rotation (rejected).** `relay_v2_stream_new_session_test.go`'s own header records that after a rotation the runner's sink tag stays on the outgoing id while the conversation rebinds to the new one, so *every* event the fresh child produces is dropped at that gate. That is why that test asserts the post-rotation child's stdin rather than a phone-side frame. Building a `model_list` observation on it would be asserting into a lane that is dropping.

Either route needs the gate to have something to compare: `activeConversation.set` is called only from `sessionRouter.Route`'s success path, so **at least one turn must be driven from the connected client before the kill.** Before that first route the cursor is `""`, `boundSessionIDForActive` returns `("", false)`, and the drain fails closed on every event — which is also why the bootstrap child's own `model_list` can never reach the phone by accident.

### Wire path under test

```
respawned fakeclaude
  └─ reads the initialize control_request the daemon wrote right after cmd.Start
  └─ writeInitializeAck → one control_response line on stdout
       └─ streamsup.Parser.emitModelList         → turnevent.ModelList          [#1839]
            └─ sessionModelHold.Sink (retain, then forward unchanged)           [#1840]
                 └─ streamTurnSink.sinkFor → fan-in channel (droppable class)
                      └─ startStreamTurnDrainV2 → active-session gate
                           └─ (*interactiveTurnEmitterV2).Handle, ModelList arm  [#1849]
                                └─ turnbridge.MapEvent, ModelList arm            [#1848]
                                     └─ emit() → interactive capability filter
                                          └─ sealed InnerFrameV2 → fakerelay → fakephone
```

The two arms marked #1848/#1849 are precisely what this e2e adds over the existing unit tests.

### Test file

One new file, `internal/e2e/relay_v2_stream_model_list_test.go`, `//go:build e2e`, package `e2e`. One test function, one helper, a const block. No production file is touched.

**Structure** — the helper does transport and collection, the test does the assertions, the split `driveRateLimitTurn` established:

- A `modelListObservation` struct carrying: the first `protocol.ModelListPayload` seen after the kill plus a `found bool`; a count of `model_list` frames seen *during* the pre-kill turn; the two pre-kill milestones (`sawEcho`, `sawTurnEnd`); the two child PIDs.
- A helper — name it for what it drives, e.g. `driveModelListRespawn` — returning that struct. It `t.Fatalf`s only on transport and decode faults (an error envelope, a payload that will not decode, a receive error that is not the deadline), exactly as `driveRateLimitTurn` does. On the deadline it returns with `found` false and `t.Logf`s the counts, leaving diagnosis to the caller's assertions.
- One test function calling it and asserting AC1 + AC2. No `t.Parallel()` — these spawn daemons; none of the siblings parallelise.

**Helper sequence** (order matters at two points, both called out):

1. `home := shortHome(t)`; `RunBareIn(t, home, "pair", …)`; `decodePairPayload`; decode the server static pubkey.
2. `seedBoundConversation(t, home, <convID>, <bootstrapUUID>)`. **Must come after the `pair` run and before the daemon start.** `seedBoundConversation` writes into `<home>/.pyry/test/` without creating it — the `pair` run is what creates that directory — and the daemon loads `conversations.json` once at startup.
3. `fakerelay.New`; `StartStreamInteractiveWithRelay(t, home, <bootstrapUUID>, fr.URL()+"/v2/server")` with **no extra env** — no rider is needed, the initialize answer is unconditional. The `<bootstrapUUID>` here and the one in step 2 MUST be the same string; a mismatch drops every event at the gate and presents as an unexplained timeout.
4. `readPersistedServerID`; `waitBinaryHello`; `fakephone.Dial`; `driveHandshakeToOpenDaemonInteractive`. The interactive handshake is load-bearing, not incidental: `emit()` filters on the interactive capability, so a non-interactive handshake yields zero frames and a vacuous test.
5. Declare `sealSend` and `nextEnv` as local closures, copied from `driveRateLimitTurn`. `nextEnv` MUST decrypt every `noise_msg` in receive order and filter after decrypting — the receive nonce is sequential and a pre-decrypt filter desyncs the `CipherState`.
6. `first := waitForRunnerStatus(t, h, 20*time.Second, "first child running", …Phase == "running" && ChildPID != 0)`.
7. `sealSend` one `send_message`; loop `nextEnv` until `turn_end`, recording `sawEcho` (an `assistant_delta` containing the echoed needle), `sawTurnEnd`, and any `model_list` seen in this window.
8. `killChild(t, first.ChildPID)`; `waitForRunnerStatus(…, RestartCount >= 1 && ChildPID != 0 && ChildPID != first.ChildPID)`. The phone is not read during this window; frames the daemon sends meanwhile buffer on the socket.
9. Loop `nextEnv` against a fresh deadline until the first `model_list` arrives or the deadline expires. Record it and return.

**Deadlines.** One deadline per collection window, `time.Now().Add(30 * time.Second)`, and `nextEnv` computes each receive timeout as `time.Until(deadline)` — never a fixed short timeout in a poll loop. A timed-out `fakephone.Client` cannot be reused, so the first timeout must be terminal for that window.

**Constants.** Distinct UUIDs from the sibling specs (`#1845`-flavoured), a distinctive echo needle, one `send_message` request id. The two expected model rows go in as literals transcribed from `initializeModels`; do not import the fake's package.

## Concurrency model

No new goroutines. Everything runs on the test goroutine, sequentially: control-plane polls and phone receives never interleave, which is what lets step 8 poll `control.Status` while `model_list` buffers unread on the websocket.

Three daemon-side goroutines matter to the reasoning and none are the test's to manage:

- `streamsup.Runner`'s supervise loop, which respawns after the SIGKILL on its 500ms→1s→2s ladder and fires `RequestInitialize` once per spawn.
- The per-child stdout forwarder feeding `streamsup.Parser` → `sessionModelHold.Sink` → the fan-in. A respawn mints a new forwarder against the same hold; `sessionModelHold`'s mutex exists because the old and new can briefly overlap during teardown.
- The drain goroutine started by `startStreamTurnDrainV2`, single-reader over the fan-in.

Teardown is the harness's: `StartStreamInteractiveWithRelay` registers `h.teardown`, and `t.Cleanup` closes the phone and the fake relay. Register the phone and relay cleanups in the helper as `driveRateLimitTurn` does.

## Error handling

| Failure | Response |
|---|---|
| `pair` exits non-zero, pubkey will not decode | `t.Fatalf` with stdout/stderr |
| Dial or handshake fails | `t.Fatalf` |
| A received envelope is `protocol.TypeError` | `t.Fatalf` with the raw payload — an error envelope means the daemon refused something and every later assertion is noise |
| A `model_list` payload will not decode | `t.Fatalf` — a decode failure at this layer is the defect, not a miss |
| Receive error that is not `fakephone.ErrReceiveTimeout` | `t.Fatalf` |
| Either collection deadline expires | return with what was collected; `t.Logf` the counts and the two PIDs; the caller's assertions produce the failure |
| `waitForRunnerStatus` budget expires | its own `t.Fatalf`, which already attaches the last status and the daemon stderr tail |

Failure messages carry the diagnostic that separates the three ways this can be red, because they are genuinely hard to tell apart from a bare "want 1, got 0":

- **The pre-kill turn never completed** (`sawEcho`/`sawTurnEnd` false) — assert these FIRST and fatally. Name the bootstrap-id/bound-id mismatch as the first suspect, as the rate-limit tests do; it is the most likely new-caller error and its symptom is exactly this.
- **The respawn never happened** — `waitForRunnerStatus` in step 8 fatals with the daemon's stderr tail.
- **The respawn happened and no frame arrived** — the AC1 failure proper. Say so explicitly, and name the two suspects in order: the emitter arm (#1849) or the mapper arm (#1848) not putting the frame on the wire, and — second, per the ticket's own note — the upstream droppable refusal, since `turnMarkFor` answers `turnMarkNone` for `ModelList` and `sinkFor` can refuse it at `droppableCap`. A quiet hermetic run should never approach that cap, so a flaky red there is a signal about the sink, not about this test.

## Testing strategy

### What the one test asserts

Milestones first and fatally — a missing frame read off a run where nothing completed proves nothing:

1. `sawEcho` and `sawTurnEnd` from the pre-kill turn.
2. The respawn produced a different `ChildPID` (already fatal inside `waitForRunnerStatus`'s predicate, so no second assertion needed).

**AC1** — a `model_list` arrived after the kill, and it carries both entries in claude's order:

- `found` is true.
- `ConversationID` equals the test's conversation id. This is the *bridge's* contribution: the parser's event carries no conversation identity, so this proves the mapper supplied it rather than leaving it empty.
- `len(Models) == 2`, `Models[0].Value == "sonnet"`, `Models[1].Value == "haiku"` — order is claude's own and `MapEvent` is documented not to sort, so this is a live claim.
- `DroppedModels == 0` — two entries against `maxModelListEntries`, so nothing was cut.

**AC2** — the two contrasting shapes survive the round trip:

- Row `sonnet`: `ResolvedModel == "claude-sonnet-5"`, `DisplayName == "Sonnet"`, `EffortLevels` equal to the five levels in claude's order (`low, medium, high, xhigh, max` — measured non-alphabetical, so compare as an ordered slice), `SupportsAutoMode == true`.
- Row `haiku`: `ResolvedModel == "claude-haiku-4-5-20251001"`, `DisplayName == "Haiku"`, `len(EffortLevels) == 0`, `SupportsAutoMode == false`. This row is the load-bearing one — claude omits both keys entirely, and the collapse of absent / `null` / `[]` onto one reading (#1828) is what this proves end to end.
- One extra assertion on the haiku row, worth its line: `EffortLevels != nil`. `json.Unmarshal` yields a nil slice for `null` and an empty non-nil slice for `[]`, so this is what distinguishes them — and it pins `ModelOption.MarshalJSON`'s normalisation through a daemon to a phone, where the unit tests pin it at the byte level. `TruncatedFields` is deliberately NOT normalised the same way, so assert `nil` on both rows: nothing was cut, and the absence must reach the wire as `null`.

### What the test must NOT assert

**Do not assert that zero `model_list` frames arrived during the pre-kill turn.** Record the count and put it in the failure messages, but make no claim on it. The bootstrap child's list cannot reach the phone today (the cursor is empty when it is emitted), but #1846 is open and its whole job is supplying the menu to a client that connects late. A zero-assertion here would go red the day #1846 lands, for a reason that has nothing to do with what this test proves.

**Do not assert an exact total count of `model_list` frames.** There is no terminator bounding the post-kill window the way `turn_end` bounds a turn, so "exactly one" would be a claim about timing rather than about behaviour. First-arrival plus content is the honest shape.

**Nothing may be asserted from a log.** The #833 posture keeps model / effort / YOLO values out of logs at every level, so a log-scraping assertion would assert the opposite of the intended behaviour. Every claim above reads a decrypted payload.

### Running it

`internal/e2e` is behind the `e2e` build tag. A bare `go test ./internal/e2e/` compiles and runs **zero** tests and exits 0. Use the Makefile target's flags:

```
go test -tags e2e -race -count=1 -run 'TestRelayV2_StreamModelList' ./internal/e2e/...
```

Report the count of tests that actually executed (`=== RUN` lines), never the exit code.

### AC3 — the mutation, which you must actually run

Not a phrasing. Run it, record the result in the PR body:

1. Delete the `case turnevent.ModelList:` arm from `(*interactiveTurnEmitterV2).Handle` in `cmd/pyry/interactive_turn_v2.go`.
2. Run the command above. The test MUST be red on AC1 — with the arm gone the event falls to that switch's `default`, which logs `interactive_turn.unknown` and produces no frame. A clean single-point mutant.
3. Restore the arm; confirm green.

Run it without writing to the worktree via `go test -overlay=<abs-path>/overlay.json`, pointing the overlay at a mutated copy of `interactive_turn_v2.go`.

`MapEvent`'s `turnevent.ModelList` arm in `internal/turnbridge/outbound.go` is the equally valid second point if you want a second data point; one is sufficient for the AC.

## Open questions

- **Post-kill wall clock.** The measured kill→respawn under `-race` is near a second, and the initialize round trip adds a child stdin read plus one stdout line. 30s is generous by two orders of magnitude; if the whole test measures slow enough to matter against `make check`'s budget, say so in the PR rather than trimming the deadline — a tight deadline on this path buys flakiness, not speed.
- **A second `model_list` in the post-kill window.** Should never happen (one respawn, one ask, one reply) and the design deliberately does not assert against it. If one is observed while developing, that is a finding about the respawn path worth reporting on the ticket, not something to assert away.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The boundary this test exercises is subprocess-stdout → parent state, and it is explicit and singular: `streamsup.Parser.emitModelList` decodes from the **top-level** line bytes only, never a nested field, which is what stops a tool result whose text is literally a control response from forging an inventory. The test changes nothing about that boundary — it drives the fake through the same `writeInitializeAck` path every other fake-daemon suite already sees. Worth stating because it is the category a reader would expect to be the finding: the strings this test asserts on (`Value`, `ResolvedModel`, `DisplayName`, the effort levels) are claude-authored text that crossed that boundary, and the test treats them as inert comparison values only — no `exec`, no path, no template, no shell.
- **[Tokens, secrets, credentials]** No findings. The test handles one pair token, obtained from `RunBareIn`'s `pair` output via `decodePairPayload` and passed to `fakephone.Dial` / `driveHandshakeToOpenDaemonInteractive` — the identical handling as the six sibling specs, no new storage, no new lifetime, and nothing written outside the `t.TempDir`-backed `shortHome`. The failure messages this spec prescribes carry counts, PIDs, model values and a daemon stderr tail; none carries the token or the static keys. The design decision that makes this safe rather than lucky: the helper never formats `payloadA` into any message.
- **[File operations]** No findings. Three writes, all inside `shortHome(t)`: `seedBoundConversation` and `seedBootstrapRegistry` (harness-owned, `0600`, fixed literal paths, no caller-controlled component) and the config write in `writeStreamInteractiveConfig` (`0700` dir, `0600` file). No path in this test is composed from a value that crossed a trust boundary. The claude sessions dir is deliberately unset — stream mode opens no transcript.
- **[Subprocess / external command execution]** Concrete scenario walked, no finding. The test spawns two processes: `pyry` (via `spawnWith`, fixed argv, `-pyry-*` flags placed before the `--` separator so none reaches the child) and, indirectly, `fakeclaude`. **No value the test asserts on is ever passed as an argument to either.** The one place claude-authored bytes could reach a sink is the daemon's own `set_session_settings` path, gated by `internal/relay`'s `validModel` / `validEffort`; this test does not send one, and the direction hazard `ModelOption`'s doc names (an effort level claude publishes that `validEffort`'s closed enum would refuse inbound) is out of scope here — it belongs to whatever ticket first sends a value back.
- **[Cryptographic primitives]** No findings. The test uses the Noise handshake through `driveHandshakeToOpenDaemonInteractive` and the established `sealSend` / `decryptInnerEnvelope` pair. One real hazard exists and the design addresses it explicitly rather than by luck: the receive nonce is sequential, so `nextEnv` must decrypt every `noise_msg` in receive order and filter *after* decrypting — a pre-decrypt filter desyncs the `CipherState` and every later decrypt fails with a misleading error. Called out in § Design step 5 because a developer writing a "skip frames I don't care about" loop would introduce it naturally. `PYRY_ALLOW_INSECURE_RELAY=1` is set by the harness for the hermetic loopback and is the tier's existing posture, not this ticket's choice.
- **[Network & I/O]** No findings, one deliberate design decision. All I/O is loopback against `fakerelay`; no listener, no TLS config and no size cap is introduced by this test. The cap that governs what it observes is upstream and already decided: `maxModelListEntries` (10) bounds the entry count and `turnevent.ModelList.DroppedModels` carries what was cut — which is why AC1 asserts `DroppedModels == 0` rather than ignoring the field. The read-deadline discipline is the design decision worth naming: one deadline per collection window with `time.Until(deadline)` per receive, because `fakephone.Client` cannot be reused after a timeout, so a short-timeout poll loop would leave the test asserting against a dead connection instead of a missing frame.
- **[Error messages, logs, telemetry]** SHOULD FIX, and the spec already carries the fix — restating it here because it is the one place this test could actively *undermine* a security posture rather than merely fail to test one. The #833 posture keeps model / effort / YOLO values out of logs at every level, and `sessionModelHold` enforces it by construction (no `*slog.Logger` field, constructor takes none). A log-scraping assertion — grepping the daemon's stderr for `"claude-sonnet-5"` — would pass only against a tree that had broken that posture, i.e. it would pin the defect as the requirement. § Testing strategy forbids it explicitly; code review should check that no assertion reads `h.Stderr` for a model value. Using `stderrTail` in a *failure* message is fine and is what the siblings do: it reports what the daemon logged, it does not require anything to be there.
- **[Concurrency]** No findings. The test adds no goroutine and takes no lock. The one shared-state hazard in the path under test is named and owned elsewhere: `turnbridge.MapEvent`'s `ModelList` arm copies slice **headers**, so the payload shares backing arrays with the event `sessionModelHold` retained, and a sort, in-place dedupe or append would be a data race across the parser's forwarder and the relay leg. The test must not mutate any slice it receives — it decodes into its own `ModelListPayload` from wire bytes, so it holds no reference into the daemon's memory at all, which is what makes this structural rather than a promise. `-race` is on in the Makefile flags this spec prescribes.
- **[Threat model alignment]** No findings. The relevant threat from `docs/protocol-mobile.md` § Security model is unauthorised access to interactive events, and the design depends on the gate rather than bypassing it: the interactive handshake is required for any frame to arrive, and § Design step 4 states that a non-interactive handshake yields zero frames and a vacuous test. The complementary property — that a client which was *not* connected when the daemon obtained the list gets nothing on this lane — is real and deliberately **not asserted**, because #1846 is the ticket that changes it; see § Testing strategy for why an assertion there would be a maintenance trap rather than a proof.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-27
