# Spec #1191 — A minted per-conversation PTY interrupt oracle

**Ticket:** [#1191](https://github.com/pyrycode/pyrycode/issues/1191) · **Size:** S · **Labels:** `bug`, `size:s`, `security-sensitive`, `needs-real-claude`

One new hermetic e2e file. It drives an interrupt against a **minted** (non-bootstrap)
per-conversation PTY session and asserts the turn stops — the fake-tier coverage gap
the ticket names, now reachable because #1195 landed the transcript substrate.

**Expected production diff: zero.** The routing is very likely already correct at this
tier (see § Predicted outcome); if the test goes red on `main`, § AC3 decision
procedure bounds what the developer does about it.

---

## Files to read first

Turn-1 data load. Read these before writing any code; every decision below cites one.

| Path | Extract |
|---|---|
| `internal/e2e/relay_v2_perconv_turn_end_test.go` (whole file, 340 lines) | **The skeleton. Copy it.** #1195's minted-PTY test: `shortHome` → `RunBareIn pair` → `decodePairPayload` → `claudeSessionsDir` + pre-create `<initialUUID>.jsonl` → `trigDir` → `fakerelay.New` → `StartRotationWithRelay` + the two #1195 knobs → `readPersistedServerID` / `waitBinaryHello` → `fakephone.Dial` → `driveHandshakeToOpenDaemonInteractive` → local `sealSend` / `nextEnv` closures → all-null `create_conversation` → `boundSessionID` → `send_message` → drain. Lines 62–249 transfer almost verbatim. |
| `internal/e2e/relay_v2_interrupt_test.go` (whole file, 363 lines) | **The behavioural sibling** (bootstrap PTY interrupt, #794). Lift: the mid-turn re-drop kicker (`:205-219`), the ordered `t.Fatal` guards (`:221-259`, `:285-321`), the sealed `interrupt` envelope (`:265-277`), the StopReason-fidelity note (`:56-61`), and `hasBareESC` (`:352-362`). Its doc comment is the voice to match. |
| `internal/e2e/relay_v2_stream_interrupt_test.go:19-66` | The stream sibling's header — **why minting is what makes the test exercise #1121's routing at all** (`:47-56`), and the PTY-vs-stream StopReason asymmetry stated from the other side (`:64-66`). Do not copy its `StopReason == "cancelled"` assertion. |
| `internal/e2e/internal/fakeclaude/main.go:94-118` | The `PYRY_FAKE_CLAUDE_ESC_ENDS_TURN` doc block: raw mode, bare-ESC scan, **one-shot**, coexists with `PYRY_FAKE_CLAUDE_TUI`. |
| `internal/e2e/internal/fakeclaude/main.go:494-501` | `interruptEndTurnLine` — the canned `stop_reason:"end_turn"` line the ESC handler appends. **`package main` in an `internal/` dir: not importable. The e2e needles the wire-shape fragment, never this const.** |
| `internal/e2e/internal/fakeclaude/main.go:717-726`, `:1022-1041` | The poll-loop ESC branch (`escEndsTurn && !escEnded && escPending.Swap(false)`) and `appendTurnGrowth` / `appendTurnEnd`. **`appendTurnGrowth` writes `{}\n`** — inert to the mapper, which is why a delivered turn cannot fabricate a turn_end. |
| `internal/e2e/internal/fakeclaude/main.go:575-588` | Knob 1's wiring: `argvSessionID(os.Args[1:])` overrides `initU`, so the minted child's `f` **is** `<sharedDir>/<mintedID>.jsonl`. This is what makes `appendTurnEnd` land where the daemon tails. |
| `internal/e2e/internal/fakeclaude/main.go:1186-1200` | `startStdinReader`: any stdin bytes set `turnPending`; a `containsBareESC` read additionally sets `escPending`. Confirms a bracketed-paste prompt (`0x1b 0x5b …`) contributes no bare ESC. |
| `internal/turnbridge/mapper.go:21-31`, `:61-78` | `EventKindJsonlEndOfTurn → TurnEnd{end_turn}` (the only turn_end source, always `end_turn`), and `assistant` + non-empty text → `TextChunk` (what the mid-turn line becomes). |
| `cmd/pyry/main.go:1301-1311` | `resolveBoundRunner` + the `conv.CurrentSessionID == ""` guard. **Read the doc comment. This guard is a hard no-touch (AC4, #678).** |
| `cmd/pyry/main.go:1281-1290`, `:1361-1400` | `interruptRunner`'s arm switch and `activeInterrupter.SendEsc`'s four records (`no_active_conv`, `no_bound_runner`, `no_actuator`, `dispatched`). These are the diagnosis vocabulary for a red run and for AC5. |
| `cmd/pyry/main.go:995-1002` | Production wiring: `currentConv: active.CurrentConversation`, `resolveRunner` over `resolveBoundRunner(convReg, pool, …)`. The composition Phase 0 proves end-to-end. |
| `cmd/pyry/main.go:1601-1625` | `activeConversation.set` is called **only from `sessionRouter.Route`'s success path**; `CurrentConversation()` is `""` before any route. This is why Phase 0 must precede the first `send_message` — and why it lands on the `no_active_conv` arm. |
| `cmd/pyry/interrupt_routing_test.go:135-185`, `:292-311` | `TestResolveBoundRunner`/"empty CurrentSessionID is inert, never the bootstrap runner" and `TestActiveInterrupter`/"unbound/dangling resolution is inert". **Pre-existing AC4 arm-level coverage — cite by name in the PR, do not duplicate.** |
| `internal/supervisor/modal.go:69-108` | `SendEsc` → `sendModalKey`: `ErrNoLiveSession` (wrapped) when `s.sess == nil`, otherwise one tui-driver `Session.SendEsc()`. The `keystroke_err` failure mode. |
| `internal/sessions/pool.go:1379`, `:1414-1418` (`p.newRunner(supCfg)`), `internal/sessions/session.go:250-258` | A minted PTY session's `Runner()` is the `*supervisor.Supervisor` — it has `SendEsc`, so `interruptRunner` takes `armSendEsc`. The premise the whole test rests on. |
| `internal/e2e/per_conversation_eviction_test.go:380-395` | `boundSessionID(t, convPath, convID) string` — reads `current_session_id` off `conversations.json` with a 2 s poll. Reuse verbatim (`convPath = <home>/.pyry/test/conversations.json`). |
| `internal/e2e/harness.go:323-366` | `StartRotationWithRelay` — the four `PYRY_FAKE_CLAUDE_*` envs it always sets, `extraEnv` appended verbatim, and the **single shared** `PYRY_FAKE_CLAUDE_STDIN_LOG` path (`:336`) every child appends to. |
| `docs/knowledge/codebase/929.md` (whole file) | **Mandatory.** The 500 ms subscribe-at-EOF race: a single-shot append lands below the tailed range. The re-drop kicker is the established fix, and the mid-turn kicker here is that fix. |
| `docs/specs/architecture/1195-minted-perconv-pty-transcript-substrate.md` § Design, § Why the bootstrap child is unaffected | Why both knobs are safe to set daemon-wide, and why the bootstrap child's stem stays `<initialUUID>` by construction. |
| `CODING-STYLE.md` | stdlib-only tests, `gofmt`, table-driven where the shape fits. |

---

## Context

The interrupt route is `handleInterrupt` (`internal/relay/v2session_modal.go:477`) →
`relay.Interrupter` → `activeInterrupter.SendEsc` (`cmd/pyry/main.go:1361`) →
`resolveBoundRunner` (`:1301`) → `interruptRunner` (`:1281`) →
`(*supervisor.Supervisor).SendEsc` (`internal/supervisor/modal.go:69`) → a lone `0x1b`
on the child's PTY.

Every green fake-tier interrupt test on the PTY tier drives the **bootstrap** session
(`TestRelayV2_InterruptStopsRunningTurn`). The minted per-conversation path — the code
#1121 introduced, and the path the live 2026-07-24 failure ran on — has none. It could
not have one before now: a minted PTY session emitted no turn lifecycle at all, because
`fakeclaude` wrote `<sharedDir>/<INITIAL_UUID>.jsonl` while the daemon tailed
`<sharedDir>/<mintedID>.jsonl`. #1195 closed that with two default-off knobs, and
`TestRelayV2_PerConversationTurnEnd` is the proof it works. This ticket rides it.

Two properties of the landed substrate make this test small:

1. **The minted child's transcript is the file the daemon tails.** With
   `PYRY_FAKE_CLAUDE_SESSION_ID_FROM_ARGV=1`, `openSession` opens
   `<sharedDir>/<mintedID>.jsonl`, so `appendTurnEnd` — the ESC handler's write —
   lands directly in the tailed range.
2. **The mid-turn line can be aimed at one child.**
   `PYRY_FAKE_CLAUDE_JSONL_TRIGGER_DIR` keys the injection path on the child's own
   stem, so `<trigDir>/<mintedID>.jsonl.trig` is claimable by the minted child alone.
   The shared `PYRY_FAKE_CLAUDE_JSONL_TRIGGER` would have been a coin flip between
   the two children.

### Predicted outcome, and why the spec says so up front

This test is expected to be **GREEN on unmodified `main`**. The premises are all
verified above: a minted PTY session's `Runner()` is `*supervisor.Supervisor`, so
`interruptRunner` takes `armSendEsc`; the cursor is stamped by the `send_message` that
precedes the interrupt; and #1195's test already proves the minted supervisor has a
live tui-driver session (its `send_message` was acked, which requires
`deliverViaSession`). A green here is a **real result** — it exonerates the daemon-side
routing and localizes the live failure to claude's own PTY-side Esc handling, which the
fake defines by fiat and cannot model. Write that conclusion into the PR body; do not
treat it as a failure to reproduce (the ticket's Technical Notes say this in as many
words).

---

## Design

**One new file: `internal/e2e/relay_v2_perconv_interrupt_test.go`**
(`//go:build e2e`, `package e2e`), holding
`TestRelayV2_PerConversationInterruptStopsRunningTurn` and one local helper. No
production change, no edit to any existing file.

### Phase structure

| Phase | What happens | What it buys |
|---|---|---|
| **Setup** | `relay_v2_perconv_turn_end_test.go:62-140` verbatim, plus `PYRY_FAKE_CLAUDE_ESC_ENDS_TURN=1` in `extraEnv` and a captured `stdinLog` path | Both children get an ESC→end_turn handler and share one stdin log |
| **0 (AC4)** | Seal an `interrupt` **before any `send_message`**, while `CurrentConversation()` is still `""` | The production wiring is inert with nothing active |
| **1** | All-null `create_conversation` → `convID`; `boundSessionID` → `mintedID` | The minted topology (#1121's routing is only exercised when the target is non-bootstrap) |
| **2** | `send_message(convID)` → await ack | Stamps the cursor onto `convID`; opens the minted subscription |
| **3 (AC2 pre-guard)** | Re-drop an assistant-**text** line into `<trigDir>/<mintedID>.jsonl.trig`; drain to a non-idle `turn_state` scoped to `convID`; stop the kicker | The turn is genuinely running before the interrupt |
| **4 (AC1)** | Snapshot the bare-ESC count; seal the `interrupt`; drain to `turn_end` with `ConversationID == convID` | The interrupt stopped the minted conversation's turn |
| **5 (AC2 + AC4 teeth)** | Assert the on-disk transcript pair and the bare-ESC count delta | Which *child* received the ESC, and that nothing else ever did |

### Data flow

```
phone ──interrupt(#1) ─────────▶ handleInterrupt (interactive ✓)
                                   activeInterrupter.SendEsc
                                     currentConv() == ""  →  INERT, zero bytes written
                                     (record: v2.interrupt.no_active_conv)

phone ──create_conversation───▶ Pool.CreateIn → spawn fakeclaude  argv: … --session-id M …
                                   knob 1 ⇒ child writes <sharedDir>/M.jsonl
phone ──send_message(conv)────▶ sessionRouter.Route → active.set(conv)
                                   resolveTarget → tails <sharedDir>/M.jsonl

test ──(250 ms ticker)────────▶ <trigDir>/M.jsonl.trig = assistant TEXT line
   minted child appends to M.jsonl → TextChunk → turn_state{responding, conv}   ── AC2 pre-guard
test stops the kicker

phone ──interrupt(#2) ────────▶ activeInterrupter.SendEsc
                                   currentConv() == conv → resolveBoundRunner → M's
                                   *supervisor.Supervisor → armSendEsc → lone 0x1b
   minted child: containsBareESC → escPending → appendTurnEnd(f)
                                → <sharedDir>/M.jsonl gains stop_reason:"end_turn"
                                → EventKindJsonlEndOfTurn → turn_end{conv}       ── AC1

disk: M.jsonl HAS "stop_reason":"end_turn";  <initialUUID>.jsonl HAS NOT          ── AC2
log:  exactly one bare ESC in the whole shared stdin log                          ── AC4
```

### The three oracles, and what each one alone would fail to prove

This is the heart of the design. Each oracle is weak in a way the next one covers.

1. **The wire `turn_end{ConversationID: convID}`** proves the daemon reported a turn
   ending for that conversation. It does **not** prove which transcript the event came
   out of: `ConversationID` is stamped from the **active cursor**, not from the file
   (the same MF-2 hazard #1195's spec records). Alone it would also pass in a world
   where the daemon tailed the wrong file while the cursor happened to point at the
   minted conversation.

2. **The on-disk transcript pair** closes that. `PYRY_FAKE_CLAUDE_ESC_ENDS_TURN=1` is
   set on the *daemon*, so **both** children carry an ESC→end_turn handler — which is
   what turns the bootstrap transcript from a blind spot into a detector. A mis-routed
   ESC (the pre-#1121 `Interrupter: w.sup` behaviour) would append the canned end-turn
   line to `<sharedDir>/<initialUUID>.jsonl`, and the negative assertion catches it
   exactly. This is the answer to the ticket's "the stdin log is not a discriminator":
   the *transcripts* are, because they are per-child where the log is not.

3. **The bare-ESC count over the shared stdin log** is the AC4 oracle. It is useless
   for attribution (one file, every child appends — the ticket says so) but **sound for
   global absence**: a count of zero means no child received an ESC, whoever they are.
   Phase 0 asserts zero, Phase 5 asserts one. The Phase-5 positive is what proves the
   Phase-0 negative was not vacuous — the oracle demonstrably sees ESCs when there are
   any — so no settle-timer is needed anywhere.

Structural causality (the ticket's required attribution shape, since the PTY tier
cannot assert a stop reason) is what ties them together: **in this test the bare ESC is
the only thing that can produce an end-of-turn line in the minted transcript.** The
kicker only ever drops an assistant-text line with no `stop_reason`, and
`appendTurnGrowth` writes `{}\n`. So a `turn_end` for the minted conversation ⟺ the
minted child's ESC handler fired.

### Contracts

```go
// countBareESC returns how many bare ESCs — 0x1b bytes not immediately followed by
// 0x5b ('[') — appear in b. Every bracketed-paste marker is 0x1b 0x5b, so each bare
// 0x1b is one supervisor.SendEsc actuation. The counting form of hasBareESC.
func countBareESC(b []byte) int
```

`hasBareESC` (`relay_v2_interrupt_test.go:352`) is deliberately **left alone** rather
than re-expressed over `countBareESC`. The ~10 duplicated lines cost less than editing
#794's live capstone: touching that file adds merge surface against every in-flight
branch and puts a shipped exit-gate test at risk for a cosmetic win. Say so in
`countBareESC`'s doc comment so a reviewer reads it as a decision, not an oversight.

### Constants (file-local)

- `perConvMidTurnMarker` — a distinctive test-authored string, e.g.
  `"e2e-1191:mid-turn"`. Test-authored, so it is inert substrate under #603.
- `perConvMidTurnLine` — `{"type":"assistant","message":{"id":"m-1191","content":[{"type":"text","text":"<marker>"}]}}` + `"\n"`.
  **No `stop_reason` field.** That absence is a load-bearing invariant, not a
  formatting detail — comment it at the const.
- `endTurnNeedle` — the literal `"stop_reason":"end_turn"`. This is the wire-shape
  fragment of `fakeclaude`'s `interruptEndTurnLine`, needled rather than imported
  (`package main` under `internal/` is unimportable). Comment it as protocol shape,
  and point at `internal/e2e/internal/fakeclaude/main.go:501` as the line that must
  keep containing it.

### Env passed to `StartRotationWithRelay`

`PYRY_MOBILE_V2=1`, `PYRY_FAKE_CLAUDE_TUI=1`,
`PYRY_FAKE_CLAUDE_SESSION_ID_FROM_ARGV=1`,
`PYRY_FAKE_CLAUDE_JSONL_TRIGGER_DIR=<trigDir>`,
`PYRY_FAKE_CLAUDE_ESC_ENDS_TURN=1`.

TUI mode and Esc-ends-turn coexist by design — they touch different bytes
(`fakeclaude/main.go:112-116`). Use a never-created rotation trigger path, as both
precedent tests do. Create `<trigDir>` and pre-create `<sharedDir>/<initialUUID>.jsonl`
**before** the daemon starts.

### AC → evidence map

| AC | Where it is proved |
|---|---|
| AC1 (minted, not bootstrap) | Phases 1–4. Minted topology per the stream sibling's argument (`relay_v2_stream_interrupt_test.go:47-56`): had the target been bootstrap-bound, a correct route and the pre-#1121 bug would be indistinguishable. |
| AC2 (non-vacuous, structural) | Phase 3's ordered pre-guard + Phase 5's transcript pair + the "only the ESC can end a turn here" invariant. **No `StopReason` assertion** — see below. |
| AC3 (behaviour on `main` stated) | PR body. § AC3 decision procedure. |
| AC4 (unresolvable interrupt inert) | **Pair.** Arm-level: the pre-existing `TestResolveBoundRunner`/"empty CurrentSessionID is inert, never the bootstrap runner" and `TestActiveInterrupter`/"unbound/dangling resolution is inert" — cite both by name, add nothing. Wiring-level (**new**): Phase 0 + Phase 5's count. See § AC4, precisely. |
| AC5 (live PTY scenario) | Operator-run (`needs-real-claude`). Not a developer deliverable — see § AC5. |

### Do not assert `StopReason`

`EventKindJsonlEndOfTurn` fires only after `IsEndTurn` held, so the mapper always
reports `end_turn` (`internal/turnbridge/mapper.go:25-30`). The stream sibling can
assert `"cancelled"`; copying that here produces a false red on a tui-driver
limitation. The bootstrap sibling records this deliberately at `:56-61` — match its
wording. Asserting `StopReason == "end_turn"` (as #1195's test does) is harmless but
adds nothing: on this tier it is the only value the mapper can produce.

### AC4, precisely

AC4's literal subject is *"a conversation with no resolvable bound session"* — the
`no_bound_runner` arm. That arm is **already** proved, deterministically, against a
real `*sessions.Pool`, including the precondition that `Pool.Lookup("")` returns the
bootstrap session. Nothing this ticket adds would strengthen it, and duplicating it in
an e2e would be strictly worse evidence.

What was **not** proved anywhere is that the production composition routes through that
resolution at all rather than to the bootstrap supervisor. Phase 0 closes that: on a
production-wired daemon, in a state where nothing resolves, **zero bytes reach any
child**. Under the pre-#1121 `Interrupter: w.sup` wiring the count would be 1, and the
test would be red. Phase 0 exercises the `no_active_conv` guard rather than
`no_bound_runner`, because that is the unresolvable state reachable over the wire — the
cursor is only ever stamped on `sessionRouter.Route`'s success path
(`cmd/pyry/main.go:1601-1616`), so a conversation cannot become active without a
resolvable binding. Both guards return `(nil, false)` into the same inert path; the
property under test — *inert, and never the bootstrap* — is identical.

State this mapping in the PR body. A reviewer checking AC4 against its literal wording
should not have to re-derive it.

### AC5

Operator-run under the `needs-real-claude` label already on the ticket. The developer's
deliverable for AC5 is one paragraph in the PR body telling the operator what to look
for, now that #1192/#1193 put records on every arm of this route:

- `v2.interrupt.dispatched` with `arm=send_esc` and `conversation_id=<the minted
  conversation>` ⇒ the daemon dispatched correctly and the failure is downstream, in
  claude's own Esc handling. That is the finding that decides the ticket's open fork
  (fix vs. retire the PTY interrupt path).
- `v2.interrupt.no_bound_runner` / `no_actuator` / `no_active_conv` ⇒ a daemon-side
  routing defect after all, which this fake-tier test did not model.
- No `v2.interrupt.*` record at all on a wired daemon ⇒ the frame never arrived
  (`internal/relay/v2session_modal.go:468-476` states the invariant; note its "wired"
  caveat — the `v2.interrupt.inert` arm records at Debug).

### Rejected alternatives

| Alternative | Why not |
|---|---|
| **Assert `StopReason == "cancelled"`** (copy the stream sibling) | False red on a tui-driver limitation, not a defect. The asymmetry is the stream path's distinguishing rigour; the ticket forbids importing it. |
| **Use the shared `PYRY_FAKE_CLAUDE_JSONL_TRIGGER` for the mid-turn line** | Both children poll one path; whichever claims first appends to *its own* transcript. A coin flip, and the wrong outcome silently satisfies nothing (#1195 spec § Knob 2). |
| **Use the stdin log to prove *which* child got the ESC** | One shared path, every child appends (`harness.go:336`). Sound only for global absence/count, which is how this design uses it. The ticket calls this out explicitly. |
| **Reach the `no_bound_runner` arm over the wire via `delete_conversation`** | Plausible (delete → `convReg.Get` fails while the cursor still points there), but it buys a guard-level distinction that is already unit-proved, at the cost of depending on delete's session-teardown and cursor semantics. Named here so the option is not lost if code-review wants it. |
| **Drive the turn end from a trigger-dir injection** (like #1195) | Destroys the whole test. The injected line would be a second source of an end-of-turn, and the interrupt's causality would be unprovable. The kicker's line must never carry `stop_reason`. |
| **Refactor `hasBareESC` to call `countBareESC`** | Edits #794's shipped live capstone for a 10-line dedup. See § Contracts. |
| **Add `docs/knowledge/codebase/1191.md` as a deliverable** | Owned by the documentation phase, written from the merged diff. Not a developer AC. |

---

## Concurrency model

No production concurrency change. Inside the test:

- **One kicker goroutine** (Phase 3), `250 ms` ticker, stopped via `sync.Once` +
  `t.Cleanup`, exactly as `relay_v2_interrupt_test.go:206-219` and
  `relay_v2_perconv_turn_end_test.go:259-272`. Stopped *before* the interrupt is sent.
- **One receive path.** A single `recvCS` drains the whole test in capture order via
  the `nextEnv` closure — the receive nonce must stay in sequence, so never read frames
  on two paths. Copy #1195's closure verbatim.
- **Cross-process.** The test writes the trigger while the fake `os.Rename`s it; the
  claim is atomic, so a consume only ever operates on the inode it claimed. A torn read
  self-heals on the next re-drop.
- **The stdin log** is written by the fake (append + `Sync` per read,
  `fakeclaude/main.go:1233-1239`) and read by the test. Both count reads happen after a
  wire event that happens-after the write, so no polling is needed; if the Phase-5
  count reads 0 while the `turn_end` arrived, that is a real cross-process visibility
  lag — bound it with the same short poll `relay_v2_interrupt_test.go:331-339` uses,
  not with a bare sleep.

The kicker being stopped before the interrupt is symmetry with the sibling, not a
correctness requirement: the kicker's line carries no `stop_reason`, so re-drops cannot
fabricate a `turn_end` even if it kept running.

---

## Error handling

| Failure | Expected behaviour / what it means |
|---|---|
| Phase 0 count `!= 0` | An interrupt with nothing active actuated a child. **Do not weaken the assertion.** Localize the ESC first: if it is genuinely tui-driver's own delivery rather than the interrupt route, convert AC4 to the delta form (`after == before+1`) and document the measured baseline in the test. If it is the interrupt route, that is an AC4 regression and the finding of the ticket. |
| Phase 3 never sees a non-idle `turn_state` | The minted subscription never opened, or the kicker is aimed wrong. Check `<trigDir>/<mintedID>.jsonl.trig` and that `<sharedDir>/<mintedID>.jsonl` exists and grows. This is #1195 territory, not #1191 — `TestRelayV2_PerConversationTurnEnd` should be green; if it is not, fix nothing here. |
| Phase 4 never sees a `turn_end` | The reproduction. § AC3 decision procedure. |
| `turn_end` arrives with the wrong `ConversationID` | A cursor/stamping defect, distinct from the routing one. Report it; do not fold a second fix into this ticket. |
| Phase 5: needle present in the **bootstrap** transcript | The ESC reached the bootstrap child — the #678/#1121 mis-route, reproduced. The strongest possible red. |
| Phase 5: needle absent from the **minted** transcript while the wire `turn_end` arrived | The turn_end came from somewhere other than the ESC handler. Treat as a test defect (something else injected an end-of-turn) before treating it as production. |

Every `t.Fatal` message must name the phase, the invariant, and the concrete path or id
involved — the failure messages in both sibling tests are the standard to match. They
are the only diagnosis a CI-only reader gets.

---

## Testing strategy

One test function. Written as scenarios, in order; the developer writes the Go.

**Setup** — copy `relay_v2_perconv_turn_end_test.go:62-140`, changing only: a distinct
`initialUUID` const, the `extraEnv` list (add `PYRY_FAKE_CLAUDE_ESC_ENDS_TURN=1`), and
keeping the `stdinLog` path in a variable (the #1195 test discards it).

**Phase 0 — AC4 wiring negative.** Immediately after
`driveHandshakeToOpenDaemonInteractive`, before any other app frame: seal an
`interrupt` envelope (no payload, its own request id). Send it. Assert nothing here —
the count is read in Phase 4, after the long natural settle that Phases 1–3 provide.
Comment that the deferred read is deliberate: it removes the timing assumption a
settle-timer would smuggle in.

**Phase 1 — mint.** All-null `create_conversation`; drain to
`protocol.TypeConversationCreated`, failing loudly on `protocol.TypeError`; capture
`convID`. Then `mintedID := boundSessionID(t, filepath.Join(home, ".pyry", "test",
"conversations.json"), convID)`. `t.Logf` both ids.

**Phase 2 — cursor move.** `send_message(convID, "e2e-1191:go\n")`; drain to the ack
matching the request id. Plain-ASCII prompt with no ESC, so its bracketed paste
contributes no bare ESC — state that in a comment, it is what keeps the Phase-4 count
meaningful.

**Phase 3 — AC2 pre-guard.** Start the kicker writing `perConvMidTurnLine` to
`<trigDir>/<mintedID>.jsonl.trig` every 250 ms. Drain until a
`protocol.TypeTurnState` whose `ConversationID == convID` and whose `State != "idle"`.
Fail with the vacuous-pass wording (`relay_v2_interrupt_test.go:226-227` is the model):
without a running turn, "an interrupt stopped it" means nothing. Stop the kicker.

**Phase 4 — AC1 + the count snapshot.**
- Read the stdin log; `before := countBareESC(...)`. Assert `before == 0`, with a
  message naming AC4 and Phase 0's interrupt as the thing that must have actuated
  nothing.
- Seal and send the second `interrupt`.
- Drain until `protocol.TypeTurnEnd`; decode `protocol.TurnEndPayload`; assert
  `ConversationID == convID`. Deadline ~15 s, matching the siblings. The timeout
  message must say that the ESC handler is the only source of a turn_end here, so a
  timeout means the ESC did not reach the minted child.

**Phase 5 — AC2 on-disk + AC4 teeth.**
- `<sharedDir>/<mintedID>.jsonl` **contains** `endTurnNeedle`.
- `<sharedDir>/<initialUUID>.jsonl` **does not** contain `endTurnNeedle` — the
  mis-route detector.
- The two paths differ (guards the assertion pair against collapsing to one file, as
  `relay_v2_perconv_turn_end_test.go:337-339` does).
- Re-read the stdin log; assert `countBareESC(...) == 1`, i.e. exactly the one ESC this
  test intended. Message: a higher count means an interrupt actuated a child it should
  not have.

**No unit test is added.** `countBareESC` is exercised by the test it serves, and its
sibling `hasBareESC` ships untested for the same reason. Adding a table for a 6-line
byte scan would be ceremony.

### RED-before check

Unlike #1195 there is no known red state to stage: this test is expected green on
`main` (§ Predicted outcome). What the developer **must** do instead is confirm the
test is not green for the wrong reason — that it would fail if the interrupt were
mis-routed. Do that with one throwaway mutation, run, revert, and record in the PR:

> Temporarily change `activeInterrupter.SendEsc` to actuate the bootstrap runner
> instead of the resolved one (the pre-#1121 behaviour), run the new test, confirm it
> goes red at Phase 4 or Phase 5, revert.

That is the non-vacuity evidence a reviewer needs, and it costs one edit-run-revert
cycle. **Revert it — the mutation must not appear in the diff.**

### Suite verification

- `go test -race -tags e2e -run 'TestRelayV2_(PerConversationInterrupt|PerConversationTurnEnd|InterruptStopsRunningTurn|StreamInterruptStopsRunningTurn)' ./internal/e2e/...`
  — the new test plus the three siblings whose substrate or route it shares.
- `go test -race ./...` and the full `-tags e2e` e2e run.
- `go vet ./...`, `staticcheck ./...`, `gofmt`.
- Run the #603 substrate guard (`go test ./cmd/substrate-guard/...`). The new file
  carries a claude-format JSONL line; precedent is
  `relay_v2_perconv_turn_end_test.go:59` and `relay_two_phone_structured_test.go:263`,
  both unallowlisted, because the guard targets rendered claude prose and TUI glyphs,
  not inert JSONL with a test-authored marker. If it does flag, add an allowlist entry
  for the new file — do not weaken the line.

Note for whoever runs this: `ok … 1.1s` on an e2e package can mean every test
**skipped**. Confirm the new test actually ran.

---

## AC3 decision procedure (only if the test is RED on `main`)

The ticket puts the production fix in scope *if the test reproduces the live failure*.
It is in scope; it is not unbounded. Diagnose first — the arm records make this cheap.
Run the daemon with `-pyry-verbose` and read which `v2.interrupt.*` record the
interrupt produced:

| Record | Diagnosis | Shape of the localized fix |
|---|---|---|
| `no_active_conv` | The `send_message` did not stamp the cursor for a minted conversation | In `sessionRouter.Route`'s success path |
| `no_bound_runner` | `convReg.Get(convID).CurrentSessionID` empty or the pool lookup failed at interrupt time | In the binding's persistence/timing. **Never** by relaxing the `CurrentSessionID == ""` guard or adding a bootstrap fallback |
| `no_actuator` | The minted `Runner()` is not `*supervisor.Supervisor` | In `interruptRunner`'s switch or the runner factory |
| `dispatched` + `keystroke_err` | `ErrNoLiveSession`: the minted supervisor has no live tui-driver session | In the minted spawn's session attach |
| `dispatched` (clean) but no `turn_end` | The ESC landed; the transcript/subscription did not carry the end | #1195 substrate territory — check `<sharedDir>/<mintedID>.jsonl` on disk first |

Hard constraints on any fix:

- **The `conv.CurrentSessionID == ""` guard in `resolveBoundRunner` does not move.** It
  is the #678 cross-conversation isolation boundary; `Pool.Lookup("")` returns the
  bootstrap session. No fallback, no relaxation, no "just for the unbound case". The
  ticket forbids it and so does this spec.
- **No bootstrap fallback anywhere on the route.** `armNone` is inert on purpose — no
  actuation beats wrong actuation (`cmd/pyry/main.go:1276-1280`).
- If the fix would touch more than **two** production files or exceed ~**80**
  production lines, stop. Write the diagnosis and the proposed fix into the PR body and
  leave the (red, skipped or `t.Skip`-free but clearly-failing) test out of the merge
  path per whatever the reviewer decides. Expanding this ticket into an unscoped
  production change is the failure mode, not the fix.

---

## Security review (label: `security-sensitive`)

`agents/architect/security-review.md` is not present in this worktree (no `agents/`
tree anywhere in the repo — same finding as #1195's spec). Pass run over the categories
the architect instructions name: trust boundaries, input validation, false-green risk,
log/output hygiene, blast radius.

**Verdict: PASS.** One MUST FIX folded into the design (MF-1, § AC3 decision
procedure), one SHOULD (S-1, § Design), one verification note.

### Trust boundaries crossed

| Boundary | Data | Provenance | Guard |
|---|---|---|---|
| phone → `handleInterrupt` | an `interrupt` envelope, **no payload** | Remote, but Noise-sealed and device-authenticated | The interactive capability gate (`v2session_modal.go:480`), unchanged |
| active cursor → `resolveBoundRunner` | conversation id | Stamped only on `sessionRouter.Route`'s success path | `conv.CurrentSessionID == ""` (#678) — **no-touch** |
| resolved runner → child PTY | one `0x1b` byte | Fixed in code; nothing remote reaches the byte stream | n/a — the payload is a constant |
| test → fake's transcript | one assistant-text JSONL line | Test-authored const in a `//go:build e2e` file | n/a |
| env → trigger dir | a path | Harness/test-authored | n/a |

No new production surface. The only remote input on the whole route is the *existence*
of a zero-payload frame; there is no attacker-controlled value anywhere in the new code.
Restating that as a finding rather than an absence: the interrupt route's authorization
**is** the interactive capability, and this ticket neither widens nor re-checks it.

**MF-1 — the tempting shortcut, pre-forbidden.** If the test does go red on the
`no_bound_runner` arm, the one-line "fix" that makes it green is to drop or soften the
`conv.CurrentSessionID == ""` guard, or to fall back to the bootstrap runner when
resolution fails. Either is the #678 cross-conversation isolation break: an unbound
conversation's interrupt would actuate the shared bootstrap claude, and `armNone`'s
inertness exists to prevent exactly that. The ticket names this; § AC3 carries it as a
hard constraint; `interruptRunner`'s doc comment already pins it. A reviewer should
check `git diff cmd/pyry/main.go` for any change to lines 1276–1311 and fail the PR on
one.

**S-1 — false-green, the category that matters here.** The wire `turn_end` alone is a
weak oracle: `ConversationID` is stamped from the active cursor, not from the transcript
the event came out of, so it would also pass while the daemon tailed the wrong file.
The design's answer is the on-disk transcript pair — deterministic `strings.Contains`
over bytes, a genuinely different fabric from the wire assertion — plus the
`ESC_ENDS_TURN` knob being set daemon-wide so the bootstrap transcript acts as a
mis-route *detector* rather than a blind spot. Both are load-bearing; neither is polish.
Dropping either during implementation re-opens the false-green.

A second false-green shape, also handled: the Phase-0 negative. A settle-timer negative
("we waited 2 s and saw no ESC") is vacuous if the oracle simply cannot see ESCs. The
design defers the count read until Phase 4 and pairs it with Phase 5's `== 1`, so the
same oracle demonstrably reports a real ESC in the same run.

### Considered and dismissed

- **Log/output hygiene.** The test adds no production record. `t.Logf` carries only
  ids (conversation, session, turn) and test-authored markers — never the fake's stdin
  bytes, never rendered screen text, never `peerStatic`. The one thing a developer might
  reach for while debugging is dumping the stdin log on failure; the *existing* sibling
  already does this (`relay_v2_interrupt_test.go:342`) and it is safe here for the same
  reason — the only bytes the test ever sends are a plain-ASCII prompt and a lone `0x1b`.
  Asserted, not assumed: keep it that way if the prompt is changed.
- **Widening any resolver so the test fits.** `resolveBoundSessionJSONL` /
  `resolveTarget` / `perConversationSessionsDir` are all out of scope and were the #854
  confidentiality regression when relaxed. The fake moves to meet the daemon, never the
  reverse. Nothing in this design touches them.
- **The shared stdin log as an information leak.** It already exists and already
  receives every child's stdin under `PYRY_FAKE_CLAUDE_STDIN_LOG`; this test adds a
  count read, no new write and no new path. Not applicable.
- **Blast radius.** Expected production diff: zero. A test-only file under
  `//go:build e2e` cannot affect a shipped binary. If AC3 forces a production change,
  MF-1 plus the two-file / ~80-line ceiling bound it.
- **`cmd/substrate-guard` (#603).** The new file carries a claude-format JSONL line
  with a test-authored marker; direct precedent is `relay_v2_perconv_turn_end_test.go:59`,
  unallowlisted and green. **Verification note:** run the guard after adding the file
  and confirm.

---

## Open questions

1. **Is the pre-interrupt bare-ESC baseline really zero?** Reasoned, not measured: the
   only production `SendEsc` call sites are this interrupt route, the modal
   cancel/deny/trust-exit verbs (`cmd/pyry/modal_resolve_v2.go`), and the ACP paths —
   none of which fire in this test (no modal trigger, no trust trigger, no ACP), and a
   bracketed-paste prompt is `0x1b 0x5b …`, which `countBareESC` excludes by
   construction. If Phase 4's `before == 0` fails, § Error handling has the procedure.
   Resolve by observation on the first run.
2. **Does a minted PTY session emit `turn_state` as well as `turn_end`?** The mapper
   turns the assistant-text line into a `TextChunk` and the emitter turns that into
   `turn_state{responding}` — proven on the bootstrap transcript by
   `relay_v2_interrupt_test.go:221-259`, and the producer/emitter are shared. #1195's
   test only ever observed `turn_end`, so this is the one step with no exact minted
   precedent. If Phase 3 times out while `<sharedDir>/<mintedID>.jsonl` is visibly
   growing, the pre-guard can fall back to draining for a `TypeAssistantDelta` scoped to
   `convID` — same evidence (the turn is producing output), different envelope. Prefer
   `turn_state`; the fallback is not a weakening.
3. **Should `no_bound_runner` get an e2e of its own?** Named in § Rejected alternatives
   with its route (`delete_conversation`). Deferred, not lost — the arm is unit-proved
   and the wiring is covered by Phase 0.

---

## Scope

| File | Kind | Est. |
|---|---|---|
| `internal/e2e/relay_v2_perconv_interrupt_test.go` | new, e2e test | ~340 lines incl. a ~50-line doc comment and `countBareESC` |

**One new file. Zero production source files. Zero new exported types. Zero consumer
call sites** — nothing existing is edited, so there is no cascade. Total written work
~340 lines plus the PR body. Size `s` confirmed against the red lines: 1 new file
(≤3), ~340 lines (≤600), 0 exported types (≤5), 0 call sites (≤10), 0 reject branches
(≤10), 4 developer-facing AC (≤5; AC5 is operator-run).

**Not in scope:** any change to `resolveBoundRunner`'s isolation guard,
`interruptRunner`'s inert default, or any resolver in
`cmd/pyry/interactive_turn_stream_v2.go`; the `stream-json` interrupt path (it works,
production is cut over to it); `docs/knowledge/codebase/1191.md` (documentation phase);
any edit to `relay_v2_interrupt_test.go`; the live AC5 run (operator).
