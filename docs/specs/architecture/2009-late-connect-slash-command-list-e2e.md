# #2009 — Prove the retained slash-command list reaches a late-connecting client

**Size:** s (one new test file, zero production files). **Label:** `security-sensitive` — see § Security review.

## Files read

Symbol-anchored on purpose: `make cite-guard` runs in `make check`, scans `.go` files, and fails on any `//`-comment citation that resolves to a declaration — at any depth, ranges included, `internal/e2e` not exempt.

- `internal/e2e/relay_v2_stream_model_list_reconcile_test.go` → `driveLateConnectModelList`, `lateModelListObservation`, `sealedConnDriver`, `TestRelayV2_StreamModelListReachesLateConnectingPhone`. **The template**, and #1868's own conclusion: the two-conn sequencing, the happens-before that replaces a sleep, the milestones-as-values split. `sealedConnDriver` is package-level and is REUSED rather than transcribed.
- `internal/e2e/relay_v2_stream_slash_command_list_test.go` → `slashCommandListWantRows`, `TestRelayV2_StreamSlashCommandListReachesConnectedPhone`. **The payload half.** Its want-table is REUSED (same package, one transcription of the fake's canned rows), and its per-row assertion loop is the shape AC 2 copies — by index, field by field, no payload dump.
- `internal/e2e/internal/fakephone/fakephone.go` → `Client.ReceiveBytes`, `ErrReceiveTimeout`. **Load-bearing for AC 3 and it changed the design**: coder/websocket closes the underlying connection when the read context is cancelled, so a `Client` that times out cannot be reused. An absence window ends in a timeout by nature, so the conn that proves AC 3 cannot be the conn that mints — see § Design.
- `internal/e2e/handshake_interactive_helpers_test.go` → `driveHandshakeToOpenDaemonInteractive`. Drives a conn to interactive-open, consumes exactly the `noise_resp` inner frame and nothing after it, and **fatals unless `hello_ack` echoes the interactive grant** — which is the whole of AC 3's "the handshake still completes normally" half.
- `internal/e2e/harness.go` → `StartStreamInteractiveWithRelay`, `seedBootstrapRegistry`, `seedBoundConversation`, `shortHome`, `mustJSON`. `seedBootstrapRegistry` writes `sessions.json` only; nothing seeds `conversations.json` unless `seedBoundConversation` is called, and this test deliberately does not call it. That is what makes the daemon's registry empty at first handshake, which AC 3 rests on.
- `internal/relay/v2session_slashreconcile.go` → `reconcileSlashCommandLists`. The two early returns (`!s.interactive`, nil `RetainedSlashCommandLists`) — the nil one is AC 4's mutant target — and the `len(retained) == 0` return, which is AC 3's mechanism.
- `cmd/pyry/relay.go` → the `RetainedSlashCommandLists:` assignment inside `startRelayV2`. **AC 4's one-line mutant lives at that assignment.**
- `cmd/pyry/session_slash_command_list.go` → `retainedSlashCommandLists`, `resolveBoundSlashCommandList`. Why the bootstrap session contributes NOTHING (it has no conversation record, and `CurrentSessionID` is written only at mint by `handlers.CreateConversation`), hence why exactly one payload is expected after one mint.
- `internal/e2e/internal/fakeclaude/main.go` → `initializeCommands`, `writeInitializeAck`, `runStreamJSON`. The canned four-row fixture with its deliberate alias interleaving, and the single-goroutine read-line/write-line loop that gives this test its happens-before.
- `docs/knowledge/features/v2-session-manager-state-machine-connect-time-slash-command-list-reconcile-retain.md` — the #2006/#2007 package overview. Two things it holds that change how this ticket is built: the reconcile's frame is **never droppable** (`pushQueue.enqueue` marks only `TypeAssistantDelta`), so a missing frame here cannot be blamed on the cap; and the enumerate-all seam takes no argument, so cardinality on the wire is a claim about the registry rather than about the observing conn.
- `docs/specs/architecture/1868-late-connect-model-list-e2e.md` — the twin's spec, including the AC-3 mutation-run procedure this one inherits.

## Context

Seven tickets built the slash-command path in layers — mapping (#2001), frame bound (#2002), turn-lane emit (#2003), retention (#2004), resolver (#2005), connect-time reconcile (#2006), enumeration seam (#2007) — each proved at its own seam. #2008 shipped the first cross-process proof, and its header states its own boundary: it covers the **live lane** only, and it has to respawn a child to observe a frame at all.

This ticket is the inverse. The list must exist **first**, then the observing client connects, and the frame must arrive with that client neither driving a turn nor sending a message. **If the design ever needs a child to spawn while the observing client is connected, it has become a second copy of #2008 and proves nothing about the reconcile.**

The frame has two producers, which is what makes AC 4 load-bearing rather than ceremonial: #2003's turn-lane emit sends the same variant, so a test that accidentally observes a live-lane frame passes against a completely dead reconcile. The mutant is the only thing that separates them; a frame count on the *minting* conn is what keeps the two lanes distinguishable in the failure message.

No ADR is warranted: this adds a test to an established family and introduces no decision.

## Design

One new file, `internal/e2e/relay_v2_stream_slash_command_list_reconcile_test.go`, `//go:build e2e`, package `e2e`. **No new harness scaffolding** — no change to `fakeclaude`, `fakerelay`, `fakephone` or `harness.go`, and no production change.

### Three connections, and why the third one is forced

#1868 needs two conns. This ticket needs three, and the third is not a stylistic choice — it falls out of `Client.ReceiveBytes`' documented close semantics.

- **The empty observer (phone-empty).** Dials and handshakes interactive while the conversation registry is still empty, then drains a bounded window and counts. This is AC 3. Proving an absence means running the window to its deadline, and a `fakephone.Client` whose read context is cancelled has had its underlying connection closed — so this conn is **dead after its own window** and is never touched again. That is why it cannot also be the minter.
- **The minter (phone-a).** Dials next, handshakes interactive, mints a conversation over the wire and drives one turn on it. It exists only to put a retained list into the daemon.
- **The late observer (phone-b).** Dials and handshakes interactive **after** all of that, then reads. It sends nothing but its handshake — its seal-and-send closure is discarded on the floor so that is structural rather than a promise in a comment.

Three `pyry pair` runs into one home, all before the daemon starts (the daemon loads its registries once at startup). `TestPairRevoke_E2E`'s "removes one of two" subtest is the precedent that repeated pair runs yield independently usable devices; there is no device cap in the pairing path. One `ServerStaticPubkey` serves all three, each run yields its own token.

### The sequence, and why each step is forced

1. `shortHome`; three `pyry pair` runs under `-pyry-name=test`.
2. `fakerelay.New`, then `StartStreamInteractiveWithRelay(t, home, bootstrapUUID, fr.URL()+"/v2/server")`. **No extra env** (`runStreamJSON` answers the `initialize` control request under no rider) and **no `seedBoundConversation`** — the observed conversation is minted over the wire, and the empty registry at startup is what AC 3 rests on.
3. `readPersistedServerID` + `waitBinaryHello`.
4. **AC 3.** Dial phone-empty, `driveHandshakeToOpenDaemonInteractive` (which fatals unless the handshake completes with the interactive grant echoed in `hello_ack` — the "completes normally" half, by construction), then drain a 2s window counting `slash_command_list` frames. Expect zero: `retainedSlashCommandLists` enumerates an empty registry, so `reconcileSlashCommandLists` takes its `len(retained) == 0` return. The window is short on purpose — the reconcile's pushes happen synchronously on the handshake's success tail, before `noise_resp`'s successor frames, so a frame that is coming at all is milliseconds away rather than seconds.
5. **Mint the conversation** from phone-a: sealed all-null `create_conversation`, drain to `conversation_created`, keep the server-minted id. Forced: `retainedSlashCommandLists` enumerates the CONVERSATION registry and the bootstrap session has no record there, so a test built on the bootstrap child alone waits out its deadline for a frame that is correctly never sent. The mint is a spawning `Pool.Activate`, so it also produces the child whose `initialize` reply is retained.
6. **Drive one turn on the minted conversation from phone-a, and wait for `turn_end`.** This is the SYNCHRONISATION — see § Concurrency model. Without it phone-b can win the race and see nothing.
7. **Dial phone-b and handshake it interactive.** `handleNoiseInit`'s success tail records `s.interactive`, creates the push queue, and calls `reconcileSlashCommandLists`.
8. **Read on phone-b until the first `slash_command_list`**, then drain a short settle window counting any further ones. Nothing in the test may spawn a child after phone-b connects.

### Structure

A driver returning an observation value, plus one test function holding every assertion — `driveSlashCommandListRespawn` / `slashCommandListObservation`'s split, for its stated reason: milestones the caller asserts first and fatally, so a frame count read off a run where the turn never completed cannot masquerade as an AC failure.

```go
// lateSlashCommandListObservation is what one empty-registry handshake, one
// wire-minted conversation, one driven turn and one LATER interactive handshake
// put on three clients.
type lateSlashCommandListObservation struct {
	emptyConnLists  int    // AC 3: frames on a conn that handshook with nothing retained
	convID          string // the server-minted id the frame must be stamped with
	sawEcho         bool   // milestone: the minted child replied
	sawTurnEnd      bool   // milestone: the turn closed ⇒ the initialize ack was parsed
	listsOnMinter   int    // diagnostic only, never asserted (see below)
	list            protocol.SlashCommandListPayload
	found           bool
	extraOnObserver int // AC 2's cardinality half: further frames after the first
}

func driveLateConnectSlashCommandList(t *testing.T) lateSlashCommandListObservation
```

The driver `t.Fatalf`s only on transport, seal and decode faults, on an `error` envelope, and on the mint never completing — never on an acceptance criterion. On a collection deadline it returns what it has and `t.Logf`s the counts, leaving diagnosis to the caller.

**`listsOnMinter` is counted and logged, never asserted.** It should be zero — the live lane's emit for the minted child fires at spawn, when the active-conversation cursor is still empty, so `interactiveTurnEmitterV2.Handle`'s empty-conversation early return drops it. It is logged because it separates "the reconcile did not fire" from "the live lane fired instead", but an assertion on it would redden this test for a change in a lane it does not own.

**`extraOnObserver` IS asserted, and this is where this spec departs from its twin.** #1868 deliberately declined a count on the observing conn, reasoning that a total buys a timing claim rather than a behaviour one. That reasoning does not transfer, because the seam is enumerate-all: exactly one conversation exists, so `retainedSlashCommandLists` returns exactly one payload and `reconcileSlashCommandLists` pushes exactly one envelope. A second frame would mean the enumerator emitted a duplicate row, or the bootstrap session leaked into the registry enumeration — both real defects with no other home in this test. First arrival with a generous deadline, then a **short settle window** for the extras, is what makes the count an exact claim without paying the generous deadline on every run.

### Constants and reuse

Reuse `slashCommandListWantRows` (owned by the live-lane sibling) and `sealedConnDriver` (owned by the model-list reconcile twin) — same package, one transcription of each. Name the owning file in a comment; if either is ever deleted the compile break is the signal you want, not a silently diverged second copy.

New constants this file owns: a bootstrap UUID distinct from both siblings', the user text and its echo needle, and the two envelope request ids. All prefixed so no block shadows another in package scope.

## Concurrency model

The test is one goroutine and never reads two conns at once. Phone-empty's window closes before phone-a is dialled; phone-a is driven to completion before phone-b is dialled. Whatever the daemon sends phone-a during phone-b's window buffers unread on phone-a's socket — deliberate, and the posture the twin takes across its own second window.

**The happens-before that replaces a sleep.** `runStreamJSON` is a single loop that writes every reply on the goroutine that read the line, in order. The `initialize` control request is written to the child's stdin at spawn, before any user turn can be routed, so the ack line precedes the turn's echo and result lines on that child's stdout. The daemon parses them in order and the retention is written before the event is forwarded downstream. Therefore:

> `turn_end` observed on phone-a ⇒ the ack line was parsed ⇒ the hold holds the list ⇒ `retainedSlashCommandLists` will enumerate it for the next handshake.

**No `time.Sleep` and no retry loop belongs anywhere in this test.** The two bounded windows that do exist (AC 3's absence window, the observer's settle window) are not polls: each is a single drain to a deadline whose result is a count, and neither retries anything.

The daemon spawns nothing after phone-b connects: both children are already up, and the test kills nothing.

Each conn owns its own `CipherState` pair. The receive nonce is a lockstep counter, so every `noise_msg` must be decrypted in arrival order and non-`noise_msg` inner frames skipped WITHOUT decrypting — `sealedConnDriver`'s job, and pairing each phone with its own states at one call site is what makes that structural across three conns.

## Error handling

Every failure of this test is a timeout somewhere, and they are hard to tell apart from a bare "want 1, got 0". Each gets its own message naming its own suspect:

- **A `slash_command_list` on phone-empty** → AC 3 failed: something is unicasting a menu built from an empty registry. Report the count, never the payload.
- **No `conversation_created`** → fatal in the driver: the minted stream-json session never came up. Suspect the child, not the seam.
- **No echo needle / echo but no `turn_end`** → the turn did not complete, so the ordering argument never engaged and nothing downstream is diagnosable. Both asserted before the frame, fatally.
- **Milestones met, no `slash_command_list` on phone-b** → **the AC-1 failure proper.** The message names, in order: the `RetainedSlashCommandLists` assignment in `startRelayV2` being unset (AC 4's exact mutant — `reconcileSlashCommandLists` returns early on a nil seam); `retainedSlashCommandLists` enumerating nothing because the minted conversation resolved to no retained list; and phone-b's interactive grant. It must also say what the droppable-cap suspicion does NOT explain here: the reconcile pushes straight onto the conn's queue via `Push`, and `pushQueue.enqueue` marks only `TypeAssistantDelta` droppable — so a miss on this path points at the seam, while a live-lane drop would show as the `listsOnMinter` diagnostic instead.
- **A payload that will not decode** → fatal, not a miss. A decode failure at that layer IS the defect.
- **An `error` envelope in any window** → fatal.

Budgets: 2s for AC 3's absence window, 15s for the mint (it covers a child spawn), 30s for the turn, 20s for phone-b's first arrival, 2s for its settle window. Generous where a real wait is involved; short where the claim is an absence, because the pushes being proved absent are synchronous with a handshake that already completed.

**No workspace-authored string may appear in anything this test logs.** Command names, argument hints, descriptions and aliases are workspace-authored, a lower-trust origin than claude's own, and the #833 posture keeps them out of logs at every level; `reconcileSlashCommandLists` takes no logger on its success path and must not grow one. Deadline diagnostics log counts and ids only, and no failure path dumps a payload — each row reports its own field, which is #2008's restraint and the part worth copying, because `internal/e2e/realclaude` siblings copy this shape and there the same payload carries the operator's real workspace. Nothing may be asserted from a daemon log line either: that would assert the inverse of the intended behaviour and would go green only on a regression.

## Testing strategy

One test function, `TestRelayV2_StreamSlashCommandListReachesLateConnectingPhone`, over the driver above. Scenarios and claims, in assertion order:

- **AC 3, first.** `emptyConnLists == 0` on a conn that handshook interactive while the registry was empty. Its "handshake completes normally" half is discharged by `driveHandshakeToOpenDaemonInteractive` having returned at all — it fatals on a non-`noise_resp` inner frame, a `hello_ack` that will not decode, and a missing interactive grant. Asserted before the milestones because it is the only claim that is still interpretable if everything downstream fails.
- **Milestones, fatally.** Echo seen; `turn_end` seen.
- **AC 1 — the frame arrives on the later conn.** A `slash_command_list` reached phone-b, which connected after the list existed and sent nothing but its handshake.
- **AC 2, cardinality.** `extraOnObserver == 0`: exactly one frame, because exactly one conversation is retained. The predicate reddens if an extra arrives, which a first-arrival-only wait could not.
- **The frame is stamped with the minted conversation.** `conversation_id` equals the id `conversation_created` returned. This is `retainedSlashCommandLists`' own contribution and is NOT inherited from #2008: the reconcile carries no turn context, so the id can only have come from the daemon's registry record via `resolveBoundSlashCommandList`.
- **`dropped_commands` is 0**, and `len(Commands)` equals the canned row count (fatal — the row assertions index into it). Four entries against a 128-entry cap and a 64000-byte bound, so nothing was cut. A nothing-was-cut assertion, not a cap test.
- **AC 2, per row, BY INDEX** — `name`, `argument_hint`, `description`, `truncated_fields` nil, and the alias claim in two arms:
  - An alias-bearing row: `slices.Equal(got.Aliases, want.aliases)` — ordered, not a set, because the capture's alias order is non-alphabetical and `turnbridge.MapEvent` is documented not to sort. The two alias-bearing rows differ in both value and count, so neither can substitute for the other.
  - A row the fake omits the key for: `len(Aliases) == 0` **and** `Aliases != nil`. The length check is the "and to no other" half — a key-blind reading that sprayed every alias across every row passes every check above and reddens precisely here. The non-nil check is a live claim, not decoration: `json.Unmarshal` yields nil for `null` and an empty non-nil slice for `[]`, so it pins `SlashCommand.MarshalJSON`'s nil→`[]` normalisation through **the reconcile's own marshal**, a call site #2008 never exercises.

The full row is asserted rather than trimmed to AC 2's minimum: the two paths share `turnbridge.MapEvent` but not the marshal or the delivery, and the extra comparisons cost nothing next to a second e2e run.

**Deliberately not asserted, with reasons:**

- **`listsOnMinter == 0`.** Logged, not asserted — see § Design.
- **Anything about the bootstrap session's own retained list.** It has no conversation record, so it is structurally absent from this path; `TestRetainedSlashCommandLists_*` owns that claim at its own seam. AC 2's cardinality assertion is the closest this test comes, and it comes at it from the wire rather than from the enumerator.
- **The refused arm of the capability gate.** A non-interactive conn receiving nothing is pinned at the unit seam; adding a fourth conn to assert a negative over a full deadline would re-prove a claim that already has a cheaper home.

### Running it

`internal/e2e` is behind the `e2e` build tag, so a bare `go test ./internal/e2e/` compiles and runs **zero** tests and exits 0. **Read the count of tests that executed, not the exit code:**

```
go test -tags e2e -race -count=1 -run TestRelayV2_StreamSlashCommandListReachesLateConnectingPhone ./internal/e2e/
```

### AC 4 — the mutation run (mandatory, not a phrasing)

The fourth criterion is an experiment. Run it and report the output:

1. In `cmd/pyry/relay.go`, inside `startRelayV2`, remove the `RetainedSlashCommandLists:` assignment from the V2 session config (leaving the field at its nil zero value).
2. Run the command above. The harness builds `pyry` with `go build` once per test process, so a fresh run picks the mutant up — no cache to clear, and no `-overlay` needed since the daemon is a built binary rather than the test binary.
3. **Expect red**, and expect it to be the AC-1 failure with AC 3 and the milestones green: the mint and the turn are untouched, `reconcileSlashCommandLists` returns early on the nil seam, and no frame reaches phone-b. A run that stays GREEN here means the frame phone-b observed came from somewhere other than the reconcile — most likely the live lane, which emits the same variant — and the test is asserting nothing about #2006/#2007. Stop and report that rather than committing.
4. `git checkout cmd/pyry/relay.go`, re-run, confirm green.

Quote the mutant's failure output in the PR body. The mutant must never be committed.

## Open questions

- **Does the settle window ever see a legitimate second frame?** It should not: one conversation, one payload, one envelope, and nothing spawns a child after phone-b connects. If a second frame appears in practice, the finding is either a duplicate registry row or a live-lane emit reaching an idle conn, and both are defects worth the red — resolve by observation during Phase B, and record the resolution here if it changes the design.
- **Does phone-empty's dead socket perturb the later conns?** It should not — `handleNoiseInit` state is per conn and the daemon's teardown of a dropped conn touches no other queue. If phone-empty's teardown ever produces a fatal on phone-a's or phone-b's path, that coupling is the finding.

## Security review

**Verdict:** PASS (three SHOULD FIX items, each discharged in Phase B; no MUST FIX)

**Findings:**

- **[Trust boundaries]** **SHOULD FIX.** The design adds no boundary and moves none — the one in view is subprocess stdout → daemon state, and this test only observes its output from outside the process. But the strings it asserts are **workspace-authored**, a lower-trust origin than claude's own (a command defined in a repository was written by whoever wrote that repository), and the assertions demand they arrive **verbatim**, which is the documented production posture: `reconcileSlashCommandLists` forwards them unsanitised by design and the render boundary owing the sanitisation is the client's. A reader who finds a green test asserting exact strings could reasonably cite it as evidence that something sanitises. **The file header must state what this test is and is not evidence of** — faithful forwarding, not sanitisation — as #2008's does. The conversation id compared against is server-minted (`conversations.NewID` via `handlers.CreateConversation`) and arrives over the wire, so the test never supplies an id the daemon then trusts; `retainedSlashCommandLists`' untrusted-id arm stays unreachable from this entry point.
- **[Tokens, secrets, credentials]** No findings. The test holds **three** live pair tokens — one more than any spec in this family — plus a static public key. Each is used only as a `fakephone.Dial` argument and as the hello early-data token, exactly as every sibling uses them. The third device changes nothing about handling: `pyry pair` mints each token itself and the test only transports them, and all three die with `shortHome`'s temp dir. **No token may appear in any `t.Logf` or failure message**; the diagnostics this spec prescribes carry counts, conversation ids and phone names, none of which has a reason to carry one.
- **[File operations]** No findings, and one decision is what makes it so. The test creates no file and joins no user-controlled value into a path: `shortHome` owns the temp root and `StartStreamInteractiveWithRelay` performs every write. **`seedBoundConversation` must not be called**, and that is a security decision as well as a sequencing one. A seeded row binds a conversation to the *bootstrap* session, which would both make AC 3 vacuous (a non-empty registry at the first handshake) and hand a conversation the shared bootstrap child's menu — the precise shortcut `retainedSlashCommandLists`' double lookup exists to prevent, under the #678 guard where `Pool.Lookup("")` returns the bootstrap session.
- **[Subprocess / external command execution]** No findings. The two subprocesses are the daemon and, transitively, `fakeclaude`, both spawned with fixed argv, no shell, and the env set the harness already builds. **This spec adds no `extraEnv` and no `extraFlags`** — a deliberate narrowing: `runStreamJSON` answers the `initialize` request under no rider, so widening the child's environment would be new surface in the fake for no proof.
- **[Cryptographic primitives]** **SHOULD FIX.** The Noise handshake is `driveHandshakeToOpenDaemonInteractive`'s, unmodified; keygen is `crypto/rand`. The hazard is nonce discipline, and this ticket raises it from two conns to three: each conn has its own `CipherState` pair, the receive nonce is a lockstep counter, and sharing one state across phones — or filtering before decrypting — desyncs it and presents as a misleading decrypt error rather than a clean failure. `sealedConnDriver` pairs each phone with its own states at one call site, which is what keeps that structural. **New in this design and not present in the twin: phone-empty's client is DEAD once its window ends** (a cancelled read context closes the underlying connection), so its cipher states are unusable afterwards. Phase B must make that structural rather than a comment — **discard phone-empty's seal-and-send closure**, as phone-b's is discarded, so a later edit that tries to reuse the conn fails to compile instead of failing as a decrypt error that reads like a daemon bug.
- **[Network & I/O]** No findings. No listener, server or upgrade handling is added; `fakerelay` is unmodified. Every receive is bounded by a deadline computed from its window's remaining time — no unbounded read exists — and the "first timeout is terminal for the window" rule is inherited because a cancelled read closes the connection.
- **[Error messages, logs, telemetry]** **SHOULD FIX — the one category where this ticket can actively regress a posture.** The #833 posture keeps command names, argument hints, descriptions and aliases out of logs at every level, enforced by construction: `reconcileSlashCommandLists` takes no logger on its success path and must not grow one. The test sits on the other side of that boundary and holds the decoded payload legitimately, but three rules follow and are stated in § Error handling: nothing may be asserted from a daemon log line (that asserts the inverse of the intended behaviour and goes green only on a regression); no failure message may dump a payload, each row reporting its own field instead; and — **the specific trap this design introduces** — AC 3's window must report a **count** on failure and must never decode or print the unexpected frame. The natural spelling of that failure echoes the payload, which would dump a whole inventory from the one path in the test that has no legitimate reason to hold one. The restraint matters beyond the fake: `internal/e2e/realclaude` siblings copy this shape, and there the same payload carries the operator's real workspace.
- **[Concurrency]** No findings. The test adds no goroutine and takes no lock. It reads the three conns strictly sequentially — phone-empty's window closes before phone-a is dialled, phone-a completes before phone-b is dialled — so no `CipherState` has a concurrent reader. The daemon-side locks on this path (the conversation registry's, the pool's, the hold's leaf mutex, and `pushMu` inside `Push`) are already ordered and this ticket adds no edge. Shutdown safety is the harness's: `t.Cleanup` closes every phone, stops the daemon and closes the fake relay.
- **[Threat model alignment]** No findings, one alignment and one explicit non-claim. `docs/protocol-mobile.md` § Reconnect / Backfill binds control-state data classes to a connect-time snapshot rather than a cursor backfill, which is what `reconcileSlashCommandLists` implements and what this test proves across process boundaries. The capability gate is the security-relevant half and **this test exercises only the granted arm**; the refused arm is pinned at the unit seam and is out of scope here. **The non-claim needs stating because three paired devices make it easy to misread: this test is NOT evidence of per-device confinement.** Phone-b observes a conversation minted by a *different* device, and that is the enumerate-all seam working exactly as designed — `RetainedSlashCommandLists` takes no argument, so a paired interactive conn sees every retained list, including conversations bound to other workspaces. That is recorded as out of scope for the whole Mode B family and belongs to the umbrella (#829), not to this test. The header must not let a later reader mistake the three-device shape for a confinement proof.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02

## Revisions

_None yet._
