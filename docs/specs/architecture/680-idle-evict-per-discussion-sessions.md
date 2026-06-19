# Spec #680 — Idle-evict per-discussion claude sessions

**Ticket:** #680 (split from #672) · **Size:** S · **Labels:** `security-sensitive`

## TL;DR for the developer

**No production code changes.** The mechanism is already wired end-to-end (#677 mint
path + #678 routing path + the pre-existing `internal/sessions` idle-evict/cap machinery).
This ticket is **binary-boundary test coverage only**: prove that a *phone-created*
conversation's bound session participates in idle eviction, reactivates on the next
`send_message`, obeys the concurrent-active cap, and stays session-scoped (no cross-bleed).

Deliverable: one new e2e file `internal/e2e/per_conversation_eviction_test.go`
(build tag `e2e`) with **two** tests + a few small local helpers. Nothing under
`internal/`, `cmd/`, or any production `.go` file changes.

If you find yourself editing a production file to make an AC pass, **stop and re-read
§ "Why there is no production change"** — the wiring you think is missing is already
present, and the failure is almost certainly a test-harness issue, not a product gap.

## Files to read first

- `internal/sessions/pool.go:947-1002` — `buildSession`. The `--session-id <uuid>` spawn
  point used by `Pool.Create`. **Note `idleTimeout := tpl.IdleTimeout; if 0 { = p.idleTimeoutDefault }`** —
  a Create-minted session inherits the `-pyry-idle-timeout` flag automatically. This is
  why no wiring is needed for AC#1.
- `internal/sessions/pool.go:900-937` — `Pool.Create`. The mint path `sessionMinter.Create`
  calls. Persists in `stateEvicted`, supervises, then `Pool.Activate`s.
- `internal/sessions/pool.go:1145-1212` — `Pool.Activate` + `pickLRUVictim`. The
  cap-enforcing spawn entry. `boundSession.Activate` funnels through here (AC#3). Bootstrap
  is **not** excluded from the victim set — it can be cap-evicted at runtime.
- `cmd/pyry/main.go:641-720` — `sessionMinter`, `errNoBoundSession`, `sessionRouter`,
  `boundSession`. The three adapters #680 exercises. `boundSession.Activate` → `Pool.Activate(ctx, id)`
  (the comment explicitly names "#680 relies on" this).
- `cmd/pyry/main.go:558-559` — `IdleTimeout: *idleTimeout, ActiveCap: *activeCap` flow into
  `sessions.Config`. The flags reach per-conversation sessions.
- `cmd/pyry/relay.go:163-166` — v1 dispatcher registers `TypeCreateConversation` **and**
  `TypeSendMessage`. The e2e fakephone (v1) can drive both.
- `internal/relay/handlers/send_message.go:96-200` — routing → `Activate` → `WriteUserTurn`
  → `replyAck`. The ack is the AC#2 wire signal.
- `internal/supervisor/supervisor.go:275-309` — `deliverViaSession`. **The nil-resolver
  branch (`ResolveTranscript == nil`, which is every per-conversation session) still gates
  the ack on `sess.WaitReady` + `DeliverPrompt`.** This is why the reactivation+ack test
  needs fakeclaude in TUI mode (`PYRY_FAKE_CLAUDE_TUI=1`), not `sleep`.
- `internal/e2e/respawn_after_eviction_test.go` — **the template.** `startEvictionHarness`
  (pair + relay + fakeclaude-TUI + `-pyry-idle-timeout`), the hello→send_message→drain-to-ack
  loop, `waitForChildPID`, `containsAll`. Reuse `startEvictionHarness` verbatim and copy the
  phone-dial/hello block.
- `internal/e2e/cap_test.go` — `waitForBootstrap`, `waitForSessionState`, `assertActive`,
  `sleepClaudeScript`/`writeSleepClaude`, `newRegistryHome`. The registry-state assertion
  vocabulary for AC#1/#3/#4. **`waitForSessionState(t, regPath, id, "evicted"|"active", timeout)`
  is the workhorse — it takes a session UUID, exactly what you'll extract from
  conversations.json.**
- `internal/protocol/conversations_write.go` — `CreateConversationPayload` (3 nilable fields;
  send all-null to take server defaults) / `ConversationCreatedPayload` (`.ID` is the
  server-minted conversation UUID).
- `internal/protocol/codes.go:50-51` — `TypeCreateConversation` / `TypeConversationCreated`.
- `internal/conversations/conversation.go:44-49` — `CurrentSessionID string json:"current_session_id,omitempty"`.
  The conversations.json field you read to get a conversation's bound session UUID.
- `docs/knowledge/features/conversation-session-binding.md` § "Routing" + "Two load-bearing
  invariants" — the AC#4 trust boundary (phone supplies only the lookup key; routing target
  is server-stored; empty binding rejected before any `Lookup`).
- `docs/knowledge/features/idle-eviction.md` § "Activity definition" + § "Eviction cause
  record" — the cap policy ignores `attached`; the cap-eviction path emits **no** log line
  (out of scope here — see § Non-goals).

## Context

EPIC #672 gives each phone discussion its own dedicated claude session.
- **#677** (create path) eagerly mints + binds a session at `create_conversation` time via
  `sessionMinter.Create` → `Pool.Create`, recorded on `Conversation.CurrentSessionID`.
- **#678** (routing path) makes `send_message` resolve that bound session via
  `sessionRouter.Route` → `boundSession` and Activate-then-write against it, instead of the
  shared bootstrap. `boundSession.Activate` deliberately funnels through the cap-enforcing
  `Pool.Activate` so a per-conversation session is a full `ActiveCap` citizen.

`idle-eviction.md` calls this "Phase 2.0: first-message lazy bind makes eviction
load-bearing — RAM scales with active conversations, not total sessions." Until
per-conversation sessions actually routed through `Activate` (#678), eviction wasn't
load-bearing because the only session was the bootstrap. This ticket closes the phase by
**proving** per-conversation sessions participate: they idle-evict, reactivate on the next
turn, obey the cap, and never bleed across discussions.

## Why there is no production change

Walk each AC against the live code; every path already exists:

| AC | Already-wired path | Evidence |
|---|---|---|
| #1 idle-evict | `Pool.Create`→`buildSession` sets `idleTimeout` from `tpl.IdleTimeout` else `p.idleTimeoutDefault` (=`Config.IdleTimeout`=`-pyry-idle-timeout`). The per-active-period idle timer arms exactly as for the bootstrap. | `pool.go:979-982`; flag at `main.go:558` |
| #2 reactivate on send | #678 already routes `send_message` through `boundSession.Activate`→`Pool.Activate(boundID)` before `WriteUserTurn`. An evicted bound session respawns `claude --session-id <same-uuid>`. | `send_message.go:140-166`; `main.go:706-719` |
| #3 cap | `boundSession.Activate`→`Pool.Activate` enforces `ActiveCap` with LRU victim selection. | `pool.go:1145-1212`; flag at `main.go:559` |
| #4 session-scoped | Each conversation binds a **distinct** server-minted session UUID; `sessionRouter.Route` reads the server-stored `CurrentSessionID` (never phone-writable) and rejects an empty binding before any `Lookup`, so a turn never falls through to the bootstrap. | `conversation-session-binding.md` § "Two load-bearing invariants" |

The `internal/sessions` primitive is **already unit + e2e tested** for Create-minted
sessions: `pool_cap_test.go` (cap-evict LRU), `session_test.go` (idle-evict + respawn via
`Activate`), `pool_create_test.go` (Create), and the binary-boundary `cap_test.go` /
`idle_test.go`. What none of those exercise is the **phone path** — the `sessionMinter` /
`sessionRouter` / `boundSession` adapters in `cmd/pyry`. Existing eviction e2e drives either
the bootstrap or `control.SessionsNew`-minted sessions, never a `create_conversation`-minted
bound session. That adapter-level gap is the only thing #680 adds, and it is closed with
tests, not code.

## Design

### Test surface

A single new file, `internal/e2e/per_conversation_eviction_test.go`, build tag `e2e`,
package `e2e`. It composes existing package-level helpers (`startEvictionHarness`,
`waitForSessionState`, `assertActive`, `waitForBootstrap`, `newRegistryHome`, fakephone
dial, the protocol envelope builders) and adds three small local helpers:

- `createConversationViaPhone(t, phone, nextID) -> convID string` — build a
  `TypeCreateConversation` envelope (all-null `CreateConversationPayload`), `Send`, drain to
  the `TypeConversationCreated` reply (skip racing `message`/spinner envelopes the same way
  `respawn_after_eviction_test.go` drains to the ack), unmarshal `ConversationCreatedPayload`,
  return `.ID`. Contract: returns only after the daemon has minted + bound + persisted the
  session (the reply is sent after `reg.Save`). ~30 lines.
- `boundSessionID(t, convPath, convID) -> string` — read conversations.json, find the row
  with `id == convID`, return its `current_session_id`. Fail if empty (the bind must be
  populated post-reply). Mirror `conv_sweep_test.go`'s `convPath`/`mustReadFile` access.
  Define a minimal local struct decode (`{"conversations":[{"id","current_session_id"}]}`)
  or reuse `conversations` package types if importing them stays test-clean. ~20 lines.
- `startPerConvHarness(t, home, sessionsDir, initialUUID, relayURL string, extraFlags ...string) *Harness`
  — a generalization of `startEvictionHarness` that takes arbitrary `-pyry-*` flags (so one
  test can pass `-pyry-idle-timeout=2s` and the other `-pyry-active-cap=2`). Model it on
  `startEvictionHarness` (same `spawnWith` + fakeclaude-TUI env + `waitForReady`). Keep it
  local to this file. ~40 lines. *(If you prefer, call `startEvictionHarness` directly for
  the idle test and write only the cap variant — either is fine; do not edit
  `respawn_after_eviction_test.go`.)*

### Why fakeclaude-TUI, and what the harness can and cannot observe

- **Minting** a per-conversation session (`create_conversation`) needs the spawned child to
  reach PTY-ready (`Pool.Activate` waits on `WaitForPTY`). Both `sleep` and fakeclaude bind a
  PTY, so minting works with either.
- **The reactivation ack** (AC#2) is gated on `sess.WaitReady` + `DeliverPrompt` even on the
  nil-resolver path (`supervisor.go:284-296`). Only a TUI-emitting child reaches idle, so the
  send_message test **must** use fakeclaude with `PYRY_FAKE_CLAUDE_TUI=1`. Use it for both
  tests for uniformity.
- **fakeclaude derives its JSONL stem from `PYRY_FAKE_CLAUDE_INITIAL_UUID` (env), not from
  the `--session-id` argv** (`fakeclaude/main.go:118-119`). Every supervised child therefore
  shares one JSONL file. Consequence: the e2e proves **routing/lifecycle scoping** (which
  session's *registry entry* transitions, which session *reactivates*), not **per-file JSONL
  content scoping**. That is the faithful and sufficient observable — see § Testing strategy
  and § Open questions. The content-recall half of AC#2/#4 ("prior conversation intact",
  "turns land in its own JSONL" *as content*) is realclaude's domain and is already covered
  by the existing #677/#678 realclaude round-trip e2e; do **not** build a new realclaude
  test (the two-phone realclaude harness does not exist — see #603 / the structured-stream
  harness gap).

### Data flow exercised

```
fakephone ──create_conversation──> v1 dispatcher ──> handlers.CreateConversation
                                                       └─> sessionMinter.Create ─> Pool.Create
                                                            └─> buildSession (arms idle timer)
                                                            └─> Pool.Activate (cap-aware spawn)
   <──conversation_created (carries server-minted conv id; bind persisted)──┘

(idle window elapses, no attach) ──> Session idle timer ──> transitionTo(evicted)
                                                              └─> registry lifecycle_state="evicted"

fakephone ──send_message(convID)──> handlers.SendMessage
                                      └─> sessionRouter.Route(convID) ─> boundSession
                                           └─> boundSession.Activate ─> Pool.Activate(boundID)  (respawn, cap-aware)
                                           └─> WriteUserTurn ─> WaitReady+DeliverPrompt ─> replyAck
   <──ack──┘   (registry lifecycle_state back to "active"; same session UUID)
```

## Concurrency model

No new concurrency. The tests observe the existing per-session lifecycle goroutine, the
idle timer, and `Pool.Activate`'s `capMu`-serialized cap path. Multiple fakeclaude children
run concurrently (bootstrap + N per-conversation) — already exercised by `cap_test.go`
(bootstrap + α/β/γ). Each child emits its TUI glyph on its own PTY; the shared JSONL is
opened append-only by each and is never read on the nil-resolver path, so the collision is
benign.

## Error handling (test robustness / flake avoidance)

- **Drain non-ack envelopes.** After `send_message`, a respawned fakeclaude re-seeds its idle
  glyph and emits a thinking-spinner `message` envelope that races the ack. Copy the
  drain-to-ack loop from `respawn_after_eviction_test.go:202-220` (skip any non-`TypeAck`
  envelope until the 15s respawn bound).
- **Poll the registry, don't sleep-then-read.** Use `waitForSessionState(... "evicted" ...)`
  with a 5s budget for the idle transition and `... "active" ...` for the reactivation.
  Use `assertActive`/an `assertEvicted` one-shot for "X must be true at this exact moment"
  (bystander checks).
- **Idle-timeout margin.** Use `-pyry-idle-timeout=2s` (matches `cap_test.go` /
  `respawn_after_eviction_test.go`). For the idle test, do **not** require a previously-active
  bystander to *stay* active across a long reactivation — assert the bystander stays
  **evicted** after the target reactivates (no re-arm race). See Test A scenario.
- **Distinguishable LRU stamps.** In the cap test, space the three `create_conversation`
  calls by ~50ms (as `cap_test.go` does) so `lastActiveAt` ordering is deterministic for
  `pickLRUVictim`.
- **Sessions dir alignment.** The daemon computes `<HOME>/.claude/projects/encode(workdir)`
  with `-pyry-workdir=home`; pre-create that dir and pass it as `PYRY_FAKE_CLAUDE_SESSIONS_DIR`
  + a pre-created `<INITIAL_UUID>.jsonl`, exactly as `respawn_after_eviction_test.go:63-78`.

## Testing strategy

Two tests. Each AC is mapped to a concrete observable. Scenarios are described as
input→expected; write them in the project's table/imperative e2e idiom (no full bodies here).

### Test A — `TestE2E_PerConversation_IdleEvictsAndReactivates` (AC#1, AC#2, AC#4 no-bleed)

Harness: fakeclaude-TUI, `-pyry-idle-timeout=2s`, relay wired, uncapped.

1. `pyry pair`; dial fakephone (v1); hello/hello_ack. *(copy from respawn test)*
2. `createConversationViaPhone` twice → `convA`, `convB`. Read `boundA`, `boundB` from
   conversations.json. Assert `boundA != boundB` and both non-empty (distinct dedicated
   sessions — AC#4 binding distinctness).
3. **AC#1:** `waitForSessionState(boundA, "evicted", 5s)` and `waitForSessionState(boundB,
   "evicted", 5s)`. The registry `lifecycle_state=="evicted"` is the AC's own stated proof;
   it is written by `transitionTo(stateEvicted)` only *after* the supervisor stops the child,
   so it faithfully witnesses "claude process exited, RAM freed."
4. **AC#2:** `send_message(convA, "<marker>")`; drain to `TypeAck` within 15s; assert
   `ack.InReplyTo == reqID`. Then `waitForSessionState(boundA, "active", 3s)` — `boundA`
   reactivated (same UUID ⇒ respawned `--session-id boundA`, resumed from its own JSONL,
   structurally). 
5. **AC#4 (no cross-bleed):** immediately after A's ack, `assertEvicted(boundB)` — A's
   reactivation did **not** touch B. (B has no reason to wake; the assertion window is
   well under the 2s idle re-arm.) This is the "churn in one discussion leaves another's
   session untouched" guarantee, and "A's turn landed in A's session, not B's."
6. *(Optional strengthening, only if stable in CI: `send_message(convB)` → ack → `boundB`
   active, asserting the routing is bidirectional. Skip if it introduces an idle-re-arm
   race on `boundA`.)*

### Test B — `TestE2E_PerConversation_CapEvictsCrossDiscussion` (AC#3, AC#4 cap-bystander)

Harness: fakeclaude-TUI, `-pyry-active-cap=2`, relay wired, **no** idle timeout (so the only
transitions are cap-driven and the test is deterministic).

1. `pyry pair`; dial; hello. `bootstrapID := waitForBootstrap(...)`.
2. `createConversationViaPhone` → `convA`/`boundA` (active count: bootstrap+A = 2; no evict).
   Sleep ~50ms.
3. `createConversationViaPhone` → `convB`/`boundB` (activating B = 3 > cap → cap-evicts LRU
   peer = bootstrap). Sleep ~50ms.
   - **AC#3:** `waitForSessionState(bootstrapID, "evicted", 3s)`; `assertActive(boundA)`;
     `assertActive(boundB)`.
4. `createConversationViaPhone` → `convC`/`boundC` (activating C = 3 > cap → cap-evicts LRU
   peer = `boundA`, a **per-conversation** session — the security-sensitive cross-conversation
   eviction).
   - **AC#3:** `waitForSessionState(boundA, "evicted", 3s)`; `assertActive(boundB)`;
     `assertActive(boundC)`. The active count never exceeds 2 at any checkpoint.
   - **AC#4 (bystander untouched):** `boundB` stayed `active` across C's creation — only the
     deliberate LRU victim (`boundA`) transitioned. Each conversation's `current_session_id`
     remains its own distinct UUID (re-read conversations.json; assert `boundA/B/C` pairwise
     distinct and unchanged from step 2-4 capture).

> Why creates (not send_messages) drive the cap test: `create_conversation` is itself a
> spawning + `Pool.Activate` operation, so a sequence of creates is the cleanest way to push
> past the cap and observe LRU victim selection. A send-driven variant would add the
> WaitReady/ack dance for no extra coverage of the cap policy.

### Out of band

`go test -race -tags e2e ./internal/e2e/ -run TestE2E_PerConversation` must pass. Follow the
existing e2e flake-hygiene (poll-with-deadline, no bare sleeps for state, drain non-ack
envelopes).

## Security review (label: `security-sensitive`)

This ticket adds **no production code**, so it introduces **no new attack surface, no new
trust boundary, and no new dispatch policy.** The label is correct nonetheless: #680 makes
phone-driven per-conversation sessions full cap citizens in practice, so the
**cross-conversation eviction interaction** becomes observable, and the review's job is to
(a) confirm the existing boundaries that keep it safe and (b) ensure the new tests *pin*
those boundaries rather than paper over them.

**Mindset:** assume a hostile authenticated phone trying to (1) read or disturb another
discussion's session, (2) point a turn at a session it shouldn't, or (3) exhaust host
resources. Walk each surface against file:line.

### Trust boundaries (confirmed, not introduced)

- **Routing target is server-stored, never phone-writable.** The phone supplies only the
  `ConversationID` lookup key and `Text`; `sessionRouter.Route` reads `CurrentSessionID` from
  the server registry row (`main.go:681-697`, `send_message.go:91-95`). A phone cannot
  redirect a turn at an arbitrary session — it can only address a conversation whose
  server-minted id it already holds.
- **Empty binding is rejected before any `Lookup`.** `Route` returns `errNoBoundSession`
  (→ `server.binary_offline`, retryable) when `CurrentSessionID == ""`, *before*
  `Pool.Lookup` — because `Pool.Lookup("")` returns the **bootstrap**. Without this guard an
  unbound conversation would silently route a phone turn into the shared bootstrap claude
  (the confused-deputy / isolation break AC#4 forbids). **Test A step 5 + Test B's
  distinct-UUID assertions pin that turns reach exactly the bound session and no other.**
- **`Cwd` is structurally excluded from the spawn path.** Per-conversation sessions spawn in
  the daemon's already-trust-marked shared workdir (`buildSession` uses `tpl.WorkDir`); the
  phone-influenced `conversation.Cwd` is never a spawn input. #680 does not touch this, and
  the distinct-per-conversation-workdir follow-up remains deferred. No new path for `Cwd` to
  reach `exec.Command`.

### Cross-conversation eviction (the AC#4 surface flagged by PO)

The cap policy force-evicts the LRU active peer **regardless of `attached`** (idle-eviction.md
§ Activity definition). With phone-driven sessions participating, remote create/send activity
in discussion C *can* evict discussion A's active session (Test B step 4). This is **by
design and safe**:
- Eviction preserves the on-disk JSONL (`idle-eviction.md` — "evicted is a state, not
  removal"); the victim re-activates on its next `send_message` with its conversation intact.
- Eviction acts on exactly the victim's own `--session-id`/JSONL; the registry transition is
  per-UUID. **Test B asserts only the LRU victim transitions and the bystander is untouched**
  — directly exercising the no-bleed guarantee.
- It is a confidentiality-preserving operation: no session reads another's JSONL; the victim's
  process simply stops. No data crosses the boundary.

### Resource exhaustion (pre-existing, documented, not regressed)

Eager binding makes `create_conversation` process-spawning; an authenticated phone spamming
creates can pressure host processes/memory. The in-architecture bound is exactly the
`ActiveCap` this ticket proves works (it LRU-evicts under pressure rather than growing
unbounded). A dedicated per-operator create quota/rate-limit is a separate, already-named
#672-family follow-up (`conversation-session-binding.md` § "Process-exhaustion"). #680
neither introduces nor worsens this; it demonstrates the existing mitigation. **No new
defense is prescribed — no failure observed** (evidence-based fix selection).

### Data exposure

`payload.Text` is never logged (`send_message.go:88-90`); `conversation_id`/`message_id` are
opaque phone-supplied ids logged only on ack/reject. #680's tests log nothing sensitive
(they assert on registry state + ack envelopes). No change.

### Verdict: **PASS**

No new surface; the load-bearing isolation invariants (#678's two guards, the `Cwd`
exclusion, per-UUID eviction) are pre-existing and are now **pinned by the new tests**. The
cross-conversation cap eviction is by-design, confidentiality-preserving, and bounded by the
mechanism #680 verifies. No code-level defense is warranted on unobserved failure modes.

## Open questions / non-goals

- **Per-JSONL content scoping is not asserted at the e2e layer** (fakeclaude's single
  env-UUID JSONL). The lifecycle/routing scoping the ACs hinge on *is* asserted (per-UUID
  registry transitions + correct reactivation target). The content-recall semantics ("prior
  conversation intact (resumed)", "turns land in its own JSONL" as content) are realclaude's
  domain and already covered by the existing #677/#678 realclaude round-trip e2e. **Do not
  build a new realclaude/two-phone test** — that harness does not exist (#603).
- **The cap-eviction path emits no `session.*_eviction` WARN log line** (only the idle path
  does — `idle-eviction.md` § Eviction cause record: "when that path next gets touched,
  mirror this pattern"). Adding a `session.cap_eviction` log line is **out of scope** here:
  no AC requires it and no failure has been observed that needs the log to diagnose. Leave
  the sibling-line TODO for whoever next touches the cap path with a product reason.
- **Bootstrap cap-eviction at runtime** (Test B step 3 evicts the bootstrap) is consistent
  with the design — the bootstrap warm-start carve-out applies only at `Pool.New` load time
  (ADR 016), not to runtime cap pressure. If a future ticket wants the cap to spare the
  bootstrap, that's a policy change, not a bug here.
