# #1868 — Prove the connect-time model list reaches a late-connecting client

**Size:** s (one new test file, zero production files). **Label:** `security-sensitive` — see § Security review.

## Files to read first

Symbol-anchored on purpose: `make cite-guard` runs in `make check`, scans `.go` files, and fails on any `//`-comment citation that resolves to a declaration — at any depth, ranges included, `internal/e2e` not exempt. Resolve each name with `codegraph_search` / `codegraph_node`.

- `internal/e2e/relay_v2_stream_model_list_test.go` → `driveModelListRespawn`, `TestRelayV2_StreamModelListReachesConnectedPhone`, and the `modelList*` const block + `modelListWantEffortLevels`. **The template.** Copy its shape (pair → seed → start → fake relay → dial → interactive handshake → sealed send → ordered decrypt loop) and its header discipline. Its constants are REUSED by the new file (same package) — read their doc comment for why the fake's table is transcribed as literals.
- `internal/e2e/relay_v2_stream_interrupt_test.go` → `TestRelayV2_StreamInterruptStopsRunningTurn`. **The mint half.** It starts the same harness, mints an all-null conversation over the wire inline, drives a `send_message` to it and observes frames for the minted conversation. Extract: the `create_conversation` → drain-to-`conversation_created` loop and the "No seedBoundConversation" reasoning.
- `internal/e2e/handshake_interactive_helpers_test.go` → `driveHandshakeToOpenDaemonInteractive`, `buildHelloEarlyInteractive`. What drives a conn to interactive-open, and that it consumes exactly the `noise_resp` inner frame and nothing after it.
- `internal/e2e/harness.go` → `StartStreamInteractiveWithRelay`, `writeStreamInteractiveConfig`, `seedBootstrapRegistry`, `shortHome`, `readPersistedServerID`, `mustJSON`. What the harness already does for you; note `seedBootstrapRegistry` runs inside the Start call, so the bootstrap UUID is the only seed this test needs.
- `internal/e2e/relay_v2_daemon_test.go` → `waitBinaryHello`, `decryptInnerEnvelope`; `internal/e2e/relay_v2_handshake_test.go` → `sendNoiseMsg`, `sendNoiseInit`; `internal/e2e/pair_test.go` → `decodePairPayload` and `TestPairRevoke_E2E`'s "removes one of two" subtest, which is the precedent that two `pyry pair` runs into one home produce two usable devices.
- `internal/relay/v2session_handshake.go` → the success tail of `handleNoiseInit` (where `s.interactive` is recorded, the push queue is created, then `reconcileModals` → `reconcileQueues` → `reconcileModelLists` fire). **This is the seam under test**, and the ordering — `noise_resp` sent BEFORE the reconcile pushes — is what makes the phone-side decrypt order deterministic.
- `internal/relay/v2session_modelreconcile.go` → `reconcileModelLists`. The two early returns (`!s.interactive`, nil `RetainedModelLists`); the nil one is AC 3's mutant target.
- `cmd/pyry/relay.go` → the `RetainedModelLists:` assignment inside `startRelayV2`, and the `retainedModelLists` field doc on the relay wiring struct. **AC 3's one-line mutant lives at that assignment.**
- `cmd/pyry/session_model_list.go` → `retainedModelLists`, `resolveBoundModelList`. Why the bootstrap session contributes NOTHING (it has no conversation record) and why a conversation must therefore be minted before anything can be reconciled.
- `cmd/pyry/session_model_hold.go` → `sessionModelHold.Sink`. **Load-bearing for this test's synchronisation**: the retention is written BEFORE the event is forwarded downstream.
- `internal/e2e/internal/fakeclaude/main.go` → `runStreamJSON`, `initializeModels`, `writeInitializeAck`, `writeStreamResponse`. The single-goroutine read-line/write-line loop that gives this test its happens-before, and the two canned rows AC 2 branches on.
- `docs/knowledge/features/e2e-harness.md`, `docs/knowledge/features/fakeclaude-binary.md` — the harness/fake conventions this test rides; read before adding anything new to either (it must not need to).

## Context

The model-list chain was built in layers — the ask on spawn (#1839), per-session retention (#1840), the wire mapping (#1848), the conversation-keyed resolver (#1857), the connect-time reconcile (#1863), and the enumeration that fills its seam (#1867). Each is proved at its own seam against its own doubles. Nothing proves the chain across process boundaries for the client the reconcile exists for: one that was **not** connected when the daemon obtained the list.

#1845 (`TestRelayV2_StreamModelListReachesConnectedPhone`) proves the LIVE lane and had to kill a child to see a frame at all, because that lane emits once per spawn to whoever is connected at that instant. This ticket is its inverse: the list must exist **first**, then the observing client connects, and the frame must arrive with that client neither driving a turn nor sending a message. **If the design ever needs a child to spawn while the observing client is connected, it has become a second copy of #1845 and proves nothing about #1867.**

No ADR is warranted: this adds a test to an established family and introduces no decision.

## Design

One new file, `internal/e2e/relay_v2_stream_model_list_reconcile_test.go`, `//go:build e2e`, package `e2e`. **No new harness scaffolding** — no change to `fakeclaude`, `fakerelay`, `fakephone` or `harness.go`, and no production change.

### The two-connection shape

Two paired devices, both paired before the daemon starts, both under `-pyry-name=test`:

- **The minter (phone A).** Dials, handshakes interactive, mints a conversation over the wire, drives one turn on it. It exists only to put a retained list into the daemon.
- **The observer (phone B).** Dials and handshakes interactive **after** all of that, then reads. It sends nothing but its handshake.

`fakerelay` keys phones by connID (`Server.phones`) and mints a fresh one per upgrade, and the daemon's V2 manager records `s.interactive` per session with no exclusivity, so two simultaneous interactive conns are supported by both sides. `TestPairRevoke_E2E`'s "removes one of two" subtest is the precedent for two `pyry pair` runs into one home.

### The sequence, and why each step is forced

1. `shortHome`, then `pyry pair --name=phone-a` and `pyry pair --name=phone-b` (both with `-pyry-name=test`). One `ServerStaticPubkey` serves both; each run yields its own token.
2. `fakerelay.New`, then `StartStreamInteractiveWithRelay(t, home, bootstrapUUID, fr.URL()+"/v2/server")`. **No extra env**: `runStreamJSON` answers the `initialize` control request unconditionally, under no rider. **No `seedBoundConversation`**: the conversation this test observes is minted over the wire, so its binding is created by `create_conversation`, not seeded — `TestRelayV2_StreamInterruptStopsRunningTurn`'s reasoning, inherited verbatim.
3. `readPersistedServerID` + `waitBinaryHello`, then dial phone A and `driveHandshakeToOpenDaemonInteractive`.
4. **Mint the conversation.** Sealed all-null `create_conversation`; drain to the matching `conversation_created` and keep the server-minted id. This is forced, not incidental: `retainedModelLists` enumerates the CONVERSATION registry, and the pool's bootstrap session has no conversation record — `CurrentSessionID` is written only at conversation mint, in `handlers.CreateConversation`. A daemon that has minted nothing retains nothing this path can see, so a test built on the bootstrap child alone waits forever for a frame that is correctly never sent. The mint is also a spawning `Pool.Activate`, so it produces the child whose `initialize` reply gets retained.
5. **Drive one turn on the minted conversation from phone A, and wait for `turn_end`.** This is the SYNCHRONISATION, and § Concurrency explains why it is exact rather than a sleep. The mint's reply is sent after the handler's registry save, which says nothing about whether the child's `initialize` ack has been parsed yet; without step 5 the observer can win the race and see nothing.
6. **Dial phone B and handshake it interactive.** `handleNoiseInit`'s success tail records `s.interactive`, creates the push queue, and calls `reconcileModelLists`, which reads the `RetainedModelLists` seam and pushes one `model_list` envelope per retained payload.
7. **Read on phone B until the first `model_list` or the deadline.** B sends nothing further. Nothing in the test may spawn a child after B connects.

### Structure

A driver returning an observation value, plus one test function holding every assertion — `driveModelListRespawn` / `modelListObservation`'s split, and for its stated reason: milestones that the caller asserts first and fatally, so a frame count read off a run where the turn never completed cannot masquerade as an AC failure.

```go
// lateModelListObservation is what one wire-minted conversation, one driven turn
// and one LATER interactive handshake put on the observing client.
type lateModelListObservation struct {
	convID        string // the server-minted id the frame must be stamped with
	sawEcho       bool   // milestone: the minted child replied
	sawTurnEnd    bool   // milestone: the turn closed ⇒ the initialize ack was parsed
	listsOnMinter int    // diagnostic only, never asserted (see below)
	list          protocol.ModelListPayload
	found         bool
}

func driveLateConnectModelList(t *testing.T) lateModelListObservation
```

The driver `t.Fatalf`s only on transport, seal and decode faults and on an `error` envelope — never on an acceptance criterion. On a collection deadline it returns what it has and `t.Logf`s the counts, leaving diagnosis to the caller.

**`listsOnMinter` is counted and logged, never asserted.** It should be zero — the live lane's emit for the minted child fires at spawn, when the active-conversation cursor is still empty, so `interactiveTurnEmitterV2.Handle`'s empty-conversation early return drops it. That is worth logging into the failure messages because it separates "the reconcile did not fire" from "the live lane fired instead", but it is not an acceptance criterion and an assertion on it would redden this test for a change in a lane it does not own.

### Two closures, one factory

Both conns need the same two things: seal-and-send, and decrypt-the-next-envelope. Write **one** factory taking `(*fakephone.Client, *noise.CipherState)` and returning the pair, then instantiate it per conn. Do not write `nextEnv` twice.

The decrypt loop is `driveModelListRespawn`'s, unchanged in its two load-bearing properties: one deadline per collection window with every receive bounded by what remains of it (a timed-out `fakephone.Client` cannot be reused, so the first timeout must be terminal for that window), and non-`noise_msg` inner frames skipped WITHOUT decrypting while every `noise_msg` is decrypted in arrival order (the receive nonce is a lockstep counter). `driveHandshakeToOpenDaemonInteractive` consumes exactly the `noise_resp` and nothing after it, so B's loop starts on a nonce that is in sequence.

### Constants

Reuse the sibling file's `modelListRichValue`, `modelListRichResolved`, `modelListRichDisplay`, `modelListLeanValue`, `modelListLeanResolved`, `modelListLeanDisplay` and `modelListWantEffortLevels` — same package, one transcription of the fake's canned table, one place to update when `initializeModels` changes. Name the owning file in a comment; if it is ever deleted the compile break is the signal you want, not a silently diverged second copy.

New constants this file owns: a bootstrap UUID distinct from #1845's, the user text and its echo needle, and the two envelope request ids. Prefix them so neither block shadows the other in package scope.

## Concurrency model

The test is one goroutine. It never reads two conns at once: phone A is driven to completion before phone B is dialled, so the two windows cannot interleave and no frame is lost to an unread socket. Whatever the daemon sends phone A during B's window buffers on A's socket and is never read — deliberate, and the same posture `driveModelListRespawn` takes across its kill window.

**The happens-before that replaces a sleep.** `runStreamJSON` is a single loop over `bufio.ReadString` that writes every reply on the same goroutine in the order it read the lines. The `initialize` control request is written to the child's stdin at spawn, before any user turn can be routed, so the ack line precedes the turn's echo and result lines on that child's stdout. On the daemon side those lines are parsed in order and `sessionModelHold.Sink` stores a `ModelList` **before** forwarding the event downstream. Therefore:

> `turn_end` observed on phone A ⇒ the ack line was parsed ⇒ the hold holds the list ⇒ `retainedModelLists` will enumerate it for the next handshake.

This is the causality `runStreamJSON`'s own comments claim for the bogus and rate-limit riders ("BEFORE the reply, so turn_end reaching a client implies the fed line has already been through the parser — no sleep, no poll, no ordering race to tune"), used here for the ack. **No `time.Sleep` and no retry loop belongs anywhere in this test.** If a poll appears in the implementation, the ordering argument above has been broken and the fix is to restore it, not to poll harder.

The turn's events reach phone A only because the drain's session-tag gate passes: `sessionRouter.Route`'s success path stamps the active conversation to the minted one, whose bound session is the minted session — the same id the minted runner was constructed with. Nothing rotates, so the two never diverge.

The daemon spawns nothing after phone B connects: both children are already up, and the test kills nothing.

## Error handling

Every failure of this test is a timeout somewhere, and the three timeouts are hard to tell apart from a bare "want 1, got 0". Each gets its own message naming its own suspect, `TestRelayV2_StreamModelListReachesConnectedPhone`'s discipline:

- **No `conversation_created`** → fatal in the driver: the minted stream-json session never came up. The mint is a spawning activate; suspect the child, not the seam.
- **No echo needle** → the minted child never replied. The turn did not run, so the ordering argument never engaged and nothing downstream can be diagnosed. Report the minted id.
- **Echo but no `turn_end`** → the turn never closed. Same consequence: the retention cannot be assumed, so the AC-1 result is uninterpretable. Assert both milestones before the frame, fatally.
- **Milestones met, no `model_list` on B** → **this is the AC-1 failure proper.** The message must name, in order: the `RetainedModelLists` assignment in `startRelayV2` being unset or nil (AC 3's exact mutant, and `reconcileModelLists` returns early on a nil seam); `retainedModelLists` enumerating nothing because the conversation resolved to no retained list; and B's interactive grant. It must ALSO say what the droppable-cap suspicion does NOT explain here: `turnMarkFor` answers `turnMarkNone` for this variant so `streamTurnSink.sinkFor` can refuse it at `droppableCap`, but the reconcile pushes straight onto the conn's queue via `Push` and never through that sink — so a miss on this path points at the seam, and a drop seen on the LIVE lane (the `listsOnMinter` diagnostic) points at the sink instead.
- **A `model_list` payload that will not decode** → fatal, not a miss. A decode failure at that layer IS the defect.
- **An `error` envelope in any window** → fatal, echoing the payload.

Budgets: 15s for the mint (it covers a child spawn), 30s for the turn, 20s for B's collection window. Generous on purpose — this is a hermetic run and a tight budget converts a slow CI box into a mystery red.

Nothing may be asserted from a log line. The #833 posture keeps model, effort and display values out of logs at every level, `sessionModelHold` and `retainedModelLists` both enforce it by having no logger at all, and a log-scraping assertion here would assert the opposite of the intended behaviour.

## Testing strategy

One test function, `TestRelayV2_StreamModelListReachesLateConnectingPhone`, over the driver above. Scenarios and claims, in assertion order:

- **Milestones, fatally, first.** Echo seen; `turn_end` seen. Each with the message above.
- **AC 1 — the frame arrives on the later conn.** A `model_list` reached phone B, which connected after the list existed and sent nothing but its handshake. First-arrival-plus-content is the honest shape: nothing terminates this window the way `turn_end` terminates a turn, so a total count would be a claim about timing rather than about behaviour.
- **The frame is stamped with the minted conversation.** `conversation_id` equals the id `conversation_created` returned. This is `retainedModelLists`' own contribution and is NOT inherited from #1845: the reconcile carries no turn context, so the id can only have come from the daemon's registry record via `resolveBoundModelList`. The bootstrap session contributes nothing (no conversation record), so exactly one payload is expected and the first arrival is it.
- **`dropped_models` is 0.** Two canned entries against the producer's entry cap, so nothing was cut. It also guards the resolver's explicit rule that the value rides through from the decode and is never recomputed from `len(Models)` and never zeroed.
- **AC 2 — two entries, claude's own order.** `len(Models) == 2`, fatal (the row assertions below index into it). `turnbridge.MapEvent` is documented not to sort, so order is asserted by position and `EffortLevels` is compared as an ORDERED slice via `slices.Equal`, never as a set.
- **AC 2, the rich row (index 0).** `value`, `resolved_model`, `display_name` match the canned sonnet row; `effort_levels` equals all five levels in claude's own order; `supports_auto_mode` is true; `truncated_fields` is nil.
- **AC 2, the lean row (index 1) — the load-bearing one.** `value`, `resolved_model`, `display_name` match the canned haiku row; `effort_levels` has length 0 **and is non-nil**; `supports_auto_mode` is false; `truncated_fields` is nil. The nil check is a live claim, not decoration: `json.Unmarshal` yields a nil slice for `null` and an empty non-nil slice for `[]`, so it is what pins `ModelOption.MarshalJSON`'s nil→`[]` normalisation through a daemon to a phone — and it pins it on the RECONCILE's marshal (`reconcileModelLists` marshals each payload itself), which is a different call site from the live lane's. `supports_auto_mode` false on an omitted key is the #1819 grant-on-silence property, likewise re-proved on this path.

The full row is asserted on both rows rather than trimming to AC 2's minimum. The two paths share `turnbridge.MapEvent` but not the marshal or the delivery, and the six extra comparisons cost nothing next to a second e2e run.

**Deliberately not asserted, with reasons** (so a reviewer need not re-derive them):

- **A count of `model_list` frames on B.** Requires draining the full window on the success path to prove a negative about a second frame; buys a timing claim, not a behaviour claim.
- **`listsOnMinter == 0`.** Logged, not asserted — see § Design.
- **Anything about the bootstrap session's own retained list.** It has no conversation record, so it is structurally absent from this path; `TestRetainedModelLists_*` in `cmd/pyry/session_model_list_test.go` owns that claim at its own seam.

### Running it

`internal/e2e` is behind the `e2e` build tag, so a bare `go test ./internal/e2e/` compiles and runs **zero** tests and exits 0. Use the Makefile's form and **read the count of tests that executed, not the exit code**:

```
go test -tags e2e -race -count=1 -run TestRelayV2_StreamModelListReachesLateConnectingPhone ./internal/e2e/
```

Then `make check` before you finish — the new test must be green inside the standard gate, and `cite-guard` runs there.

### AC 3 — the mutation run (mandatory, not a phrasing)

The third criterion is an experiment. Run it and report the output:

1. In `cmd/pyry/relay.go`, inside `startRelayV2`, change the `RetainedModelLists:` field of the V2 session config to `nil`.
2. Run the command above. The harness builds `pyry` with `go build` once per test process (`ensurePyryBuilt`), so a fresh run picks the mutant up — no cache to clear, and no `-overlay` needed since the daemon is a built binary rather than the test binary.
3. **Expect red**, and expect it to be the AC-1 failure with the milestones green: the mint and the turn are untouched, `reconcileModelLists` returns early on the nil seam, and no frame reaches B. A run that stays GREEN here means the frame B observed came from somewhere other than the reconcile and the test is asserting nothing about #1867 — stop and report that rather than committing.
4. `git checkout cmd/pyry/relay.go`, re-run, confirm green.

Quote the mutant's failure output in the PR body. The mutant must never be committed.

## Open questions

- **Does phone A's session idle out during B's window?** `handleNoiseInit` arms an idle sweep per conn and A sends nothing after its turn. If A is torn down mid-test it is harmless — B's conn, queue and reconcile are independent, and the test never reads A again. Resolve by observation: if A's teardown ever produces a fatal on B's path, the coupling is the finding.
- **Whether both `pyry pair` runs must precede the daemon start.** They must, for the reason `seedBoundConversation` documents about registry loading; the analogue pairs before starting and this test follows. If a second pair after start is ever needed, that is a different ticket.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The design adds no boundary and moves none. The one boundary in view — subprocess stdout → daemon state — is `streamsup`'s parser feeding `sessionModelHold.Sink`, and this ticket only observes its output from outside the process. The conversation id the test compares against is server-minted (`conversations.NewID` via `handlers.CreateConversation`) and reaches the test over the wire, so the test never supplies an id the daemon then trusts. `retainedModelLists`' own doc states the corollary this test must not undermine: every id it hands the resolver came out of the daemon's own registry, so the resolver's untrusted-id arm is unreachable from that entry point. Nothing in this spec introduces a test-controlled id into that path.
- **[Tokens, secrets, credentials]** No findings, one property worth naming. The test holds two live pair tokens (`payloadA.Token`, `payloadB.Token`) and a static public key. They are used only as `fakephone.Dial` arguments and as the hello early-data token, exactly as every sibling spec uses them. **They must not be echoed into any `t.Logf` or failure message** — the failure messages this spec prescribes carry conversation ids, model values, pids and frame counts, and none of them has a reason to carry a token. A second device is paired here for the first time in this family; that changes nothing about token handling, since `pyry pair` mints each token itself and the test only transports them. Tokens are hermetic to `shortHome`'s temp dir and die with it.
- **[File operations]** Not applicable by construction. The test creates no file and writes no path. `shortHome` owns the temp root, `StartStreamInteractiveWithRelay` performs every write (`writeStreamInteractiveConfig`, `seedBootstrapRegistry`) with modes those helpers already set, and this spec deliberately calls neither `seedBoundConversation` nor any new seeder. No user-controlled value is joined into a path anywhere in the design.
- **[Subprocess / external command execution]** No findings. The two subprocesses are the daemon and, transitively, `fakeclaude` — both spawned by `spawnWith` / `ensurePyryBuilt` with fixed argv, no shell, and the env set the harness already builds. **This spec adds no `extraEnv` and no `extraFlags`**, which is a deliberate narrowing: `runStreamJSON` answers the `initialize` request under no rider, so there is no reason to widen the child's environment for this test, and a rider added "to be safe" would be new attack surface in the fake for no proof.
- **[Cryptographic primitives]** No findings. The Noise handshake is `driveHandshakeToOpenDaemonInteractive`'s, unmodified; key generation is `ecdh.X25519().GenerateKey(rand.Reader)` — `crypto/rand`. **The one crypto-adjacent hazard this design must respect is nonce discipline, and it is a correctness hazard rather than a vulnerability**: each conn has its own `CipherState` pair, the receive nonce is a lockstep counter, and the decrypt loop must therefore decrypt every `noise_msg` in arrival order and skip non-`noise_msg` frames without decrypting. Two conns make this newly easy to get wrong — the failure mode of sharing one `CipherState` across both phones, or filtering before decrypting, is a desync that presents as a misleading decrypt error rather than as a clean failure. The per-conn closure factory in § Design exists partly to make the pairing structural.
- **[Network & I/O]** No findings. No listener, no server, no upgrade handling is added; `fakerelay` is unmodified and already caps and validates its own upgrades. Every receive in the design is bounded by a deadline computed from the window's remaining time — no unbounded read exists, and the "first timeout is terminal for the window" rule is inherited because `fakephone.Client`'s read cancellation closes the underlying connection.
- **[Error messages, logs, telemetry]** **SHOULD FIX, and the spec addresses it — flagging it because it is the one category where this ticket can actively regress a posture.** The #833 posture keeps model / effort / display values out of the daemon's logs at every level, enforced by construction: `sessionModelHold` and `retainedModelLists` have no logger field and must not grow one, and `reconcileModelLists`' marshal branch deliberately does not echo `err` because `encoding/json` would quote a model value into the record. The test is on the other side of that boundary — it holds the decoded payload legitimately — but two rules follow and are stated in § Error handling and § Testing strategy: **nothing may be asserted from a daemon log line** (that would assert the inverse of the intended behaviour and would go green only on a regression), and no failure message may print a pair token. Printing model values in a test failure is fine and expected; the values are canned fixtures in a temp home.
- **[Concurrency]** No findings. The test adds no goroutine and takes no lock. It reads the two conns strictly sequentially, so there is no concurrent-reader race on either `CipherState`. The daemon-side locks this path touches are already ordered and documented — the conversation registry's, the pool's `RLock` and the hold's leaf mutex, acquired sequentially and never nested (`resolveBoundModelList`), plus `pushMu` inside `Push` (`reconcileModelLists`) — and this ticket adds no edge to that order. Shutdown safety is the harness's: `t.Cleanup` closes both phones, stops the daemon and closes the fake relay; a partial state left by a mid-test kill dies with the temp home.
- **[Threat model alignment]** No findings, and one alignment worth recording. `docs/protocol-mobile.md` § Reconnect / Backfill binds control-state data classes to a connect-time snapshot rather than a cursor backfill, which is exactly what `reconcileModelLists` implements and what this test proves end to end. The capability gate is the security-relevant half: `reconcileModelLists` returns early for a non-interactive conn, so the menu is never unicast to a conn that did not negotiate the grant. **This test exercises only the granted arm.** The refused arm — a non-interactive conn receiving nothing while a sibling interactive conn receives one, so the zero means the gate rather than an empty seam — is pinned at the unit seam by `TestV2Session_ModelListReconcile_NoFrame`, and is deliberately OUT OF SCOPE here: adding a third conn to assert a negative over a full deadline would double this test's wall clock to re-prove a claim that already has a cheaper home. If an e2e proof of the refused arm is ever wanted, it is its own ticket.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-31
