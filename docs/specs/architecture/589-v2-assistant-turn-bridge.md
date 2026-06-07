# Ticket #589 — Mobile v2: assistant-turn bridge (fan finished assistant turns to v2 phones)

Return half of every conversation on the encrypted (Noise) path. Today a phone's
`send_message` over v2 reaches claude and the assistant replies, but the reply
never returns — `send_message` yields only an `ack`. This slice wires the v2 leg
of the assistant-turn bridge: it taps the supervised child's PTY output, reads the
conversation cursor `send_message` stamped, builds the same `message` application
envelope the v1 bridge (#311) builds, enumerates the currently-open v2 sessions
(#588 `ActiveConnIDs`), and `Push`es (#571) the sealed envelope to each.

## Files to read first

- `cmd/pyry/assistant_turn.go` (whole file, ~205 LOC) — the **v1 emitter** this
  slice parallels: `assistantTurnEmitter` (queue + drain + `broadcast`), the
  `cursorReader` interface (**reused verbatim** by the v2 emitter — same package),
  `assistantTurnQueueSize`, `Enqueue` copy-and-drop-on-full discipline, and the
  per-branch log-field hygiene (PTY bytes never logged). The v2 emitter mirrors
  its shape; the only divergence is the fan-out tail.
- `cmd/pyry/relay.go:255-297` — `startRelayV2`: the function whose signature gains
  `sup`/`bridge` and whose body gains the bridge wiring + cleanup. Note the existing
  `mgr` handle (`*relay.V2SessionManager`) is the `v2Broadcaster`.
- `cmd/pyry/relay.go:88-208` — `startRelay`: the **single call site** of
  `startRelayV2` (line 141); it already holds `sup`/`bridge` (params line 96-97) and
  forwards them. Lines 162-208 show the v1 leg's bridge wiring + `legCleanup`
  ordering to mirror (observer cleared first; cleanup waits on the Run goroutine).
- `cmd/pyry/main.go:489` — the `startRelay(...)` call passing
  `bootstrap.Supervisor()` / `bootstrap.Bridge()`; and `main.go:514` `pool.Run(ctx)`
  + `defer relayCleanup()` — proof the cleanup runs **after** ctx-cancel, so a
  bridge cleanup that waits on the emitter's ctx-driven exit completes promptly.
- `internal/relay/v2session.go:1357` — `Push(ctx, connID, env) error` contract
  (seal-under-`s.send` + `noise_msg`; `ErrConnNotFound` for torn-down conn,
  `ErrSessionNotOpen` for un-authenticated, `ctx.Err()` on cancel; best-effort,
  never tears the session down).
- `internal/relay/v2session.go:1444` — `ActiveConnIDs(ctx) []string` contract
  (snapshot of `V2StateOpen` conn IDs; `nil` on ctx-cancel or Run-exited; unordered).
- `internal/protocol/messaging.go:13-30` — `MessagePayload` fields; `codes.go:45`
  `TypeMessage = "message"`. Envelope shape is identical to v1.
- `internal/e2e/relay_v2_daemon_test.go` (whole file) — AC#4 target. Reuse
  `driveHandshakeToOpenDaemon`, `waitBinaryHello`, the sealed-frame request/decrypt
  pattern (lines 134-167). The new test combines this with the assistant-trigger
  flow below.
- `internal/e2e/relay_assistant_turn_test.go` (whole file) — the **v1 e2e**: the
  `send_message`-stamps-cursor → write-trigger → loop-until-marker pattern the v2
  e2e copies, plus the "tolerate prelude chunks" loop.
- `internal/e2e/harness.go:318-360` — `StartRotationWithRelay`: forwards `extraEnv`
  and takes the relay URL, so the v2 e2e reuses it with `/v2/server` +
  `PYRY_MOBILE_V2=1` + `PYRY_FAKE_CLAUDE_ASSISTANT_TRIGGER=...`. **No harness change.**
- `docs/knowledge/codebase/311.md` — v1 bridge design (gate-eligibility race,
  defensive-log-fields-not-defensive-code, don't-close-the-channel rationale). The
  v2 emitter inherits all three lessons.
- `docs/knowledge/codebase/571.md` / `588.md` — `Push` / `ActiveConnIDs` surfaces;
  both name #589 as their consumer and document the `V2StateOpen` security gate
  (belt-and-suspenders: enumeration filter + `Push` gate, two deterministic checks).

## Context

`startAssistantTurnBridge` (v1, #311) is wired into the v1 dispatcher leg only
(`relay.go:168`). `startRelayV2` neither receives `sup`/`bridge` nor starts a
bridge. The two v2 transport primitives the bridge needs now exist:
`(*V2SessionManager).Push` (#571, addressable seal-and-deliver) and
`(*V2SessionManager).ActiveConnIDs` (#588, open-session snapshot). v2 is a hard
cutover (ADR 024) — exactly one leg consumes the frame stream, so the v2 bridge
owns the single `Bridge` output observer when `PYRY_MOBILE_V2=1`. There is no
mixed-mode path; the v1 and v2 observers never coexist.

Scope is **finished-message delivery only**. Live token streaming is out of scope
(pyrycode-mobile#337).

## Design

### Decision: parallel v2 emitter, v1 emitter untouched

The ticket leaves open "generalize the existing emitter vs. add a parallel v2
emitter — the 'v1 unchanged' AC constrains that choice." **Decision: add a parallel
v2 emitter in a new file `cmd/pyry/assistant_turn_v2.go`; do not touch
`assistant_turn.go` or `assistant_turn_test.go`.**

Rationale:
- **"v1 behaviour unchanged" (AC#5) is satisfied provably, not by argument.** Zero
  edits to any v1 file ⇒ zero risk of behavioural drift and zero churn in the v1
  unit tests (which assert `env.ID == 1` from `c.NextID()` — an assertion that would
  have to move into a sink abstraction under the generalize option).
- **Precedent.** The v2 manager already *mirrored* rather than *unified* the v1
  surface: `ActiveConnIDs`/`Push` are the v2 analogs of `ActiveConns`/`Conn.Send`,
  authored as parallel methods (#571, #588). The PROJECT-MEMORY convention
  ("Resist over-DRY on duplicated registry primitives") and #571's explicit
  "inline the seal sequence, don't extract" both favour bounded duplication over a
  forced shared abstraction with one v1 and one v2 caller.
- **Blast radius.** Generalizing touches 2 v1 files (emitter + its 3 unit tests) and
  introduces 2-3 sink types; the parallel emitter touches 0 v1 files and adds 1
  production file. Smaller, and the only shared logic (cursor read → payload
  marshal, ~25 LOC) is duplicated deliberately.

**Rejected — generalize via an `envelopeSink` interface.** Would keep one emitter
and abstract the fan-out tail behind `Broadcast(ctx, convID, msgID, payload)`, with
a `dispatchSink` (v1) and `v2Sink`. Cleaner on paper, but it changes
`newAssistantTurnEmitter`'s signature, forces a rewrite of the three v1 unit tests,
and moves v1's ID-allocation assertion into the sink — all friction against an AC
that exists precisely to keep v1 still. Revisit only if a third transport leg
appears (YAGNI today).

The only cross-file reuse is the **`cursorReader` interface** (defined in
`assistant_turn.go`, package `main`): the v2 emitter references it directly — free,
no edit to v1.

### The v2 emitter (`cmd/pyry/assistant_turn_v2.go`, new file)

Mirror the v1 emitter's structure. One unexported type, one consumer-side interface,
one wiring function. All unexported (package `main`); **zero new exported types**.

**Consumer interface** (small, defined at the consumer per CODING-STYLE):

```go
// v2Broadcaster is the minimal V2SessionManager surface the v2 emitter needs:
// the open-session snapshot (#588) and the per-conn sealed push (#571).
// *relay.V2SessionManager satisfies it.
type v2Broadcaster interface {
	ActiveConnIDs(ctx context.Context) []string
	Push(ctx context.Context, connID string, env protocol.Envelope) error
}
```

**Emitter struct** — fields: `sup cursorReader`, `bcast v2Broadcaster`,
`logger *slog.Logger`, `in chan []byte` (buffered `assistantTurnQueueSize`), and a
private `nextID uint64` envelope-ID counter (see § Envelope-ID policy). The counter
is a plain field, **not** atomic: it is read/written only on the single `Run`
goroutine (`broadcast` is called serially from `Run`).

**Methods** (contracts; bodies mirror v1 — do not re-derive):
- `Enqueue(p []byte)` — copy `p` (supervisor reuses its read buffer), non-blocking
  send onto `in`, drop-on-full with a WARN carrying only `chunk_len`. **Byte-identical
  to v1 `Enqueue`.** Invariant asserted by the unit "Enqueue copies" scenario.
- `Run(ctx)` — drain `in` until ctx-cancel or channel close; per chunk call
  `broadcast(ctx, chunk)`. Identical to v1 `Run`.
- `broadcast(ctx, chunk)` — the one divergent method:
  1. `convID := sup.CurrentConversation()`; empty → DEBUG drop (`assistant_turn.no_cursor`),
     return. (No `send_message` has stamped the cursor yet.)
  2. `msgID, err := conversations.NewID()`; err → WARN drop (`assistant_turn.rand_err`),
     return.
  3. Build `protocol.MessagePayload{ConversationID: convID, MessageID: string(msgID),
     Role: "assistant", Text: string(chunk)}`, `json.Marshal`; err → WARN drop
     (`assistant_turn.marshal_err`, **never** `err.Error()` — see #311 lesson), return.
  4. `for _, connID := range bcast.ActiveConnIDs(ctx)`: `e.nextID++`;
     `env := protocol.Envelope{ID: e.nextID, Type: protocol.TypeMessage,
     TS: time.Now().UTC(), Payload: payloadJSON}`; `bcast.Push(ctx, connID, env)`.
     On `Push` error: if `ctx.Err() != nil` return (teardown); else DEBUG log
     (`assistant_turn.push_err`) with `conn_id`/`conversation_id`/`message_id`/`err`
     and **continue** the loop (per-conn failure must not abort the turn for other
     conns — AC#2).

Steps 1-3 are the ~25 LOC genuinely duplicated from v1's `broadcast`; this is the
deliberate, precedented duplication. Step 4 is the v2-specific fan-out.

**Wiring function**:

```go
func startAssistantTurnBridgeV2(
	ctx context.Context,
	sup cursorReader,
	bridge *supervisor.Bridge,
	bcast v2Broadcaster,
	logger *slog.Logger,
) func()  // idempotent cleanup
```

Same shape as v1 `startAssistantTurnBridge`: construct the emitter, register
`emitter.Enqueue` via `bridge.SetOutputObserver`, spawn the `Run` goroutine, return
a cleanup that clears the observer (`SetOutputObserver(nil)`) and `<-done`. **Do
NOT close `in`** — same panic-window reasoning as #311 (a `Write` racing cleanup
could `Enqueue` after the observer is cleared; rely on ctx-cancel to drain `Run`).

> Note `sup` is typed `cursorReader` (the interface), not `*supervisor.Supervisor`,
> so the unit test can drive it without a real supervisor — same as v1.

### Wiring into `startRelayV2` (`cmd/pyry/relay.go`)

1. **Signature** — add two params (mirror v1's `startRelay` tail order):
   `func startRelayV2(ctx, logger, instanceName, conn, registry, serverID, convReg,
   sess, sup *supervisor.Supervisor, bridge *supervisor.Bridge) (drain func(), err error)`.
2. **Call site** (`relay.go:141`, inside the `if v2Enabled` branch of `startRelay`):
   append `, sup, bridge`. `startRelay` already has both in scope.
3. **Body** — after `mgr` is built and its `Run` goroutine spawned, gate on
   foreground mode and start the bridge:
   ```go
   var bridgeCleanup func()
   if bridge != nil {
       bridgeCleanup = startAssistantTurnBridgeV2(ctx, sup, bridge, mgr, logger)
   }
   ```
   `mgr` (`*relay.V2SessionManager`) is passed as the `v2Broadcaster`.
4. **Cleanup** — extend the returned `drain` to stop the observer before waiting on
   the manager:
   ```go
   return func() {
       if bridgeCleanup != nil {
           bridgeCleanup()   // clear observer + wait emitter Run (ctx already cancelled)
       }
       <-mgrDone
   }, nil
   ```

**Foreground gate (AC#3).** `bridge == nil` in foreground mode (`main.go:448-451`
only constructs a `Bridge` when stdin is not a terminal). The `if bridge != nil`
guard means the v2 bridge — like the v1 bridge — never starts in foreground; there
is no PTY-output observer surface there. Inbound `send_message` still works.

### Envelope-ID policy

v2 has no per-session outbound envelope counter (manager-internal frames —
`noise_resp`, solicited replies — use fixed/`Reply`-allocated IDs that the bridge
does not share). Per the #571 recommendation, the bridge uses a **caller-side
monotonic counter** (`e.nextID`, incremented once per envelope emitted). It is
**not** coordinated with the dispatch reply path, so a bridge `env.ID` may collide
with a solicited reply's `env.ID` on the same session. **This is acceptable**:
`MessagePayload.MessageID` (a fresh UUIDv4 per chunk) is the phone's dedup/ordering
key; `env.ID` is an envelope sequence hint, not load-bearing on v2. Escalate to a
manager-owned per-session counter only if the phone protocol later mandates
globally-monotonic envelope IDs per session (tracked as an open question, not built).

## Concurrency model

Identical topology to v1 (#311), with the manager funnel replacing the dispatcher:

- **Producer** — the supervisor's PTY-drain goroutine: `Bridge.Write` →
  `observer(p)` → `emitter.Enqueue(p)`. Copy + non-blocking send + drop-on-full;
  the PTY drain never blocks or wedges (the load-bearing `Bridge.Write` invariant).
- **Consumer** — one long-lived `emitter.Run` goroutine. `broadcast` is serial here,
  so `e.nextID` needs no atomic. It calls `bcast.ActiveConnIDs(ctx)` then
  `bcast.Push(ctx, id, env)` per id — both **funnel through the manager's single
  `Run` goroutine** (the package's single-writer idiom; no mutex). The unbuffered
  funnel makes the cross-goroutine call wait its turn rather than race the flynn/noise
  nonce counter.
- **Cursor read** — `sup.CurrentConversation()` is `convMu`-guarded (leaf-only,
  #312); taken once per chunk.
- **Fresh snapshot per chunk** — `ActiveConnIDs` is re-called on every `broadcast`,
  so a phone that opens its session between chunk N and N+1 is included in N+1's
  fan-out (AC#2 "connects mid-turn"). A phone whose session was torn down is absent
  from the snapshot, or, if it drops between snapshot and `Push`, `Push` returns
  `ErrConnNotFound` and the loop continues (AC#2 "dropped is skipped").
- **Teardown** — daemon ctx-cancel (the same ctx passed to `startRelayV2`) unblocks
  `Run`; cleanup runs post-cancel (`main.go` defers it after `pool.Run`). A
  `broadcast` in flight during teardown passes the cancelled ctx to
  `ActiveConnIDs`/`Push`, both of which have a `ctx.Done` escape arm (return
  `nil`/`ctx.Err()`), so the emitter never blocks on a winding-down manager. The
  caller closes `conn` before `drain`; clearing the observer inside `drain` (after
  the close) leaves a negligible window where a late chunk's `Push` returns an error
  that is debug-logged — safe, non-fatal, documented.

No new locks. The bridge's `mu` guards the observer slot; the supervisor's `convMu`
guards the cursor; the manager's `Run` goroutine owns `m.sessions`. None nest.

## Error handling

| Failure | Branch | Disposition |
|---|---|---|
| Cursor empty (no `send_message` yet) | `broadcast` step 1 | DEBUG drop, return. Normal pre-conversation state. |
| `conversations.NewID()` fails (crypto/rand) | step 2 | WARN drop, return. Defensive; never echo chunk. |
| `json.Marshal(payload)` fails | step 3 | WARN drop, return. `MessagePayload` is closed strings — unreachable in practice; **never** log `err.Error()` (it quotes input bytes). |
| `Push` → `ErrConnNotFound` (conn dropped mid-turn) | step 4 | DEBUG log, **continue** loop. AC#2. |
| `Push` → `ErrSessionNotOpen` (un-authenticated) | step 4 | DEBUG log, continue. Should not appear (snapshot filters to open), but the gate is the net. |
| `Push` → transport drop / ctx-cancel | step 4 | `ctx.Err() != nil` → return; else DEBUG, continue. |
| Queue full | `Enqueue` | WARN drop (`chunk_len` only), never block. |

A per-conn `Push` failure is **never** fatal to the turn — AC#2 is satisfied
structurally by the continue. No teardown is triggered by any bridge error (matches
`Push`'s best-effort, stay-open posture).

**SECURITY — PTY bytes never logged.** Every branch logs only `chunk_len`,
`conversation_id`, `message_id`, `conn_id`, `event`, and (where present) `err` from
`Push` (a transport/sentinel error, never chunk-derived). The chunk reaches the
phone via `MessagePayload.Text` verbatim. The marshal-error branch in particular
omits `err.Error()`. This is the same contract the v1 emitter documents.

## Testing strategy

### Unit — `cmd/pyry/assistant_turn_v2_test.go` (new, stdlib only, `-race`)

Drive the emitter against the `cursorReader` seam (reuse `stubCursor` shape from
`assistant_turn_test.go` — or a local copy; same package so either works) and a new
`stubV2Broadcaster` implementing `ActiveConnIDs`/`Push` that records pushed
`(connID, env)` pairs. Scenarios (bullet inputs → expected behaviour; the developer
writes the bodies):

- **Cursor empty → no push.** Empty cursor, enqueue a chunk; assert the stub records
  zero `Push` calls. (Mirrors v1 `DropsWhenCursorEmpty`.)
- **Fan-out to every open conn.** Stub `ActiveConnIDs` returns `{conn-a, conn-b}`,
  cursor set; enqueue one chunk; assert exactly two `Push` calls, one per id, each
  carrying `Type == TypeMessage`, `InReplyTo == nil`, decoded `MessagePayload` with
  the right `ConversationID`, `Role == "assistant"`, `Text == chunk`, and a
  `conversations.ValidID(MessageID)`. (AC#1, AC#2 fan-out.)
- **Per-conn `Push` failure does not abort the turn.** Stub returns `{a, b, c}`;
  `Push` to `b` returns `relay.ErrConnNotFound`; assert `a` and `c` still receive
  their envelopes (3 attempts, 1 error, 2 deliveries). (AC#2 "dropped is skipped".)
- **Envelope-ID counter increments.** Two chunks over two open conns; assert the four
  envelopes carry monotonically increasing `env.ID` (the caller-side counter), and
  each chunk's two conns get distinct UUID `MessageID`s. (Envelope-ID policy.)
- **Enqueue copies the chunk.** Enqueue a buffer, mutate it immediately; assert the
  pushed `Text` reflects the original bytes. (Mirrors v1 `EnqueueCopiesChunk`.)
- **ctx-cancel stops Run.** Cancel ctx; assert `Run` returns (the cleanup `<-done`
  unblocks). Keep this deterministic — no sleeps; gate on a `done` channel.

AC#2's "connects mid-turn" is covered by the fresh-snapshot semantics: a scenario
where `ActiveConnIDs` returns `{a}` on the first chunk and `{a, b}` on the second
asserts `b` receives only the second chunk's envelope.

### E2E — extend `internal/e2e/relay_v2_daemon_test.go` (AC#4)

Add a new top-level `TestRelayV2_AssistantTurn_BroadcastsMessageEnvelope` (the file
already groups v2 daemon coverage). It combines the v2 handshake/sealed-frame path
(`testV2DaemonListConversationsRoundTrip`) with the v1 assistant-trigger flow
(`TestRelay_AssistantTurn_BroadcastsMessageEnvelope`):

1. `pair` → token + responder static pubkey; seed `conversations.json` with a known
   conv id (same as the existing daemon subtest).
2. Start the daemon via `StartRotationWithRelay(t, home, sessionsDir, initialUUID,
   rotateTrigger, stdinLog, fr.URL()+"/v2/server", "PYRY_MOBILE_V2=1",
   "PYRY_FAKE_CLAUDE_ASSISTANT_TRIGGER="+asstTrigger)`. This spawns **fakeclaude**
   (so an assistant turn can be triggered) on the **v2** leg. No harness change —
   `StartRotationWithRelay` already forwards `extraEnv` and sets
   `PYRY_ALLOW_INSECURE_RELAY=1`.
3. `waitBinaryHello`; `fakephone.Dial`; `driveHandshakeToOpenDaemon` → `initSend`,
   `initRecv`.
4. Send a sealed `send_message` (`initSend.Encrypt` → `sendNoiseMsg`) for the known
   conv id; read the inner frame, `initRecv.Decrypt`, assert it decodes to an `ack`
   with `InReplyTo == reqID`. **This stamps `CurrentConversation()`.**
5. `os.WriteFile(asstTrigger, []byte(knownAssistantText), 0o600)` to make fakeclaude
   emit the scripted chunk on stdout.
6. **Loop-until-marker, decrypting in order.** Read each subsequent inner
   `noise_msg` frame, `initRecv.Decrypt` it (CipherState nonce is **sequential** —
   the phone must decrypt every binary→phone frame in capture order; it cannot skip),
   unmarshal the `Envelope`. If `Type != TypeMessage`, or it is a `message` whose
   `Text` lacks the marker (TUI prelude tolerance, as v1), keep going. On the marker
   `message`: assert `InReplyTo == nil`, `ConversationID == knownConvID`,
   `Role == "assistant"`, `conversations.ValidID(MessageID)`. (AC#1.)

A small local helper `decryptInnerMessage(t, inner, cs) protocol.Envelope`
(base64-decode `inner.Data` → `cs.Decrypt` → `json.Unmarshal`) keeps steps 4 and 6
DRY; optional, developer's call.

**Why one phone in the e2e.** Multi-phone / mid-turn-connect / dropped-conn-skip
(AC#2) are pinned deterministically and cheaply by the unit scenarios above; an
e2e with two live Noise phones would add handshake/teardown timing flakiness for no
additional coverage of the wiring. The e2e's distinct value is the real sealed
round-trip over the daemon boundary (AC#1, AC#4).

### Regression

- AC#5 (v1 unchanged): no v1 file is edited; the existing
  `assistant_turn_test.go` and `relay_assistant_turn_test.go` pass unmodified.
- `go test -race ./...`, `go vet ./...`, `go build ./cmd/pyry`, and the e2e tag
  (`go test -tags=e2e ./internal/e2e/...`) all green.

> **Real-claude e2e caveat (environmental).** As recorded on #571/#588, the sandbox
> cannot fetch the private `tui-driver` module, so `make e2e-realclaude` may not run
> here. The `e2e` (fakeclaude) suite is the in-CI oracle; flag any real-claude gap
> to the operator rather than routing `needs-rework:developer`.

## Security review (`security-sensitive`)

> The architect's `security-review.md` companion file is not present in this
> worktree; this pass was conducted from the documented categories (trust
> boundaries, information disclosure, authorization, concurrency-safety, DoS) and
> the package's established discipline — same disclaimer as the #588 spec.

**Trust boundaries.** The bridge consumes two trusted in-process inputs: the
supervised child's PTY bytes (already trusted — same bytes `pyry attach` streams to
the operator) and the manager's open-session snapshot. It produces a `message`
envelope sealed by `Push` under the session's send CipherState. No
phone-controlled / network-controlled input crosses into the bridge's control flow:
`conversation_id` comes from the supervisor cursor the *binary* stamped (not from
the chunk), `MessageID` is freshly minted by the binary, and `Text` is the child's
own output. There is no new inbound attack surface — the bridge is outbound-only.

**Authorization — server output reaches only authenticated peers.** This is the
load-bearing control and it is **inherited, not re-implemented**. `ActiveConnIDs`
returns only `V2StateOpen` conn IDs; a session reaches `V2StateOpen` only after its
token is validated in `handleNoiseInit`'s accept branch (#588 § Security review). A
`V2StateHandshakeComplete` session (CipherStates present, token unchecked) is
excluded. *Belt-and-suspenders, different fabric:* even if the snapshot filter
regressed, the bridge calls `Push(ctx, id, env)` per id, and `Push` independently
gates on `V2StateOpen` (`ErrSessionNotOpen` otherwise). Two deterministic,
independent code-level checks (enumeration filter + `handlePush` gate); neither is a
stochastic agent rule. The bridge adds **no** third gate of its own — adding one
would duplicate the primitive's contract; relying on the two existing deterministic
checks is correct.

**Information disclosure — the dominant risk for this slice.** PTY chunk bytes are
application plaintext (the assistant's reply) and **MUST NEVER** be logged at any
level. Every `broadcast` branch logs only `chunk_len`, `conversation_id`,
`message_id`, `conn_id`, `event`, and a `Push` `err` (a transport/sentinel error,
never chunk-derived). The marshal-error branch omits `err.Error()` specifically
because `encoding/json` echoes invalid input bytes into its error string (#311
lesson, carried forward). The error-handling table and § Error handling pin this for
every branch. The chunk reaches the phone only via `MessagePayload.Text`, sealed
under Noise — never in cleartext on any wire.

**Concurrency-safety.** The emitter's `broadcast` runs on a single goroutine
(`e.nextID` needs no atomic). All `m.sessions` access happens on the manager's `Run`
goroutine behind the `ActiveConnIDs`/`Push` funnels — no torn read, no nonce-counter
race (the unbuffered funnel serializes every `s.send.Encrypt`). The PTY-drain
producer is decoupled by a buffered drop-on-full queue, so a slow `Push` cannot wedge
the supervisor. ctx-cancel has an escape arm at every blocking call.

**DoS.** Fan-out is O(open sessions) per assistant chunk, all on trusted in-process
goroutines; no external party can amplify it (the trigger is the supervised child's
own output). The drop-on-full queue bounds memory under a chunk burst. The per-conn
`Push` is best-effort and never retries. Not a meaningful vector. Noted, not actioned.

**Foreground gate.** AC#3 (`bridge == nil` ⇒ no bridge) is a security-relevant
correctness property too: in foreground mode there is no attach/relay surface, and
the guard ensures no observer is installed and no envelope is minted.

**Verdict: PASS.** The one security-relevant authorization decision is inherited from
two independent deterministic checks (#571/#588), not newly invented here. The
dominant disclosure risk (PTY plaintext in logs) is closed by an explicit
log-field allowlist on every branch, matching the v1 contract. No phone-controlled
input enters the bridge's control flow; the concurrency model is safe by
construction. No revision required.

## Open questions

1. **Globally-monotonic envelope IDs per session.** The caller-side `nextID` counter
   can collide with the dispatch reply path's `env.ID` on the same session
   (acceptable today — `MessageID` is the phone's dedup key). If pyrycode-mobile
   later requires per-session monotonic `env.ID`, promote the counter into the
   manager (one counter per `V2Session`, allocated on the `Run` goroutine alongside
   `Reply`). Out of scope; not built.
2. **Real-claude assistant-turn parsing.** The bridge tees raw PTY bytes (ANSI
   escapes and all) into `Text`, exactly as v1. Parsing "what counts as a turn"
   against the real claude TUI is a separate future slice (inherited #311 deferral),
   shared by both legs.
3. **Per-conversation routing.** Like v1, the v2 bridge broadcasts to *every* open
   session — no `conversation_id → conn_id` subscription map. Pyrycode is
   single-paired-phone in practice; subscription semantics (pyrycode-mobile#336
   session boundaries) replace the broadcast call later without touching the
   supervisor or bridge sides.

## Acceptance criteria mapping

- **AC#1** (reply returns as decryptable `message` envelopes) — § Design fan-out +
  e2e steps 5-6.
- **AC#2** (multi-phone; mid-turn connect included; dropped skipped, non-fatal) —
  fresh-snapshot-per-chunk + per-conn `continue` on `Push` error; unit scenarios 2-4.
- **AC#3** (v2 leg only; not in foreground) — `startRelayV2` wiring + `bridge != nil`
  gate.
- **AC#4** (e2e round-trip extending `relay_v2_daemon_test.go`) — § E2E.
- **AC#5** (v1 unchanged) — parallel emitter, zero v1 edits; § Regression.

## Related

- `docs/knowledge/codebase/311.md` — v1 assistant-turn bridge (design parallel).
- `docs/knowledge/codebase/571.md` — `Push` surface (consumed here).
- `docs/knowledge/codebase/588.md` — `ActiveConnIDs` snapshot (consumed here).
- `docs/knowledge/features/v2-session-manager.md` — evergreen manager doc.
- `docs/knowledge/decisions/024-noise-ik-mobile-e2e.md` — ADR 024, v2 hard cutover.
