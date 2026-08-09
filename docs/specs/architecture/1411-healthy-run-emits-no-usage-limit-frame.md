# #1411 — Prove a healthy run emits no usage-limit frame, at both test tiers

**Size:** S (confirmed; PO's label stands — see § Size check)
**Labels:** `security-sensitive`, `needs-real-claude`
**Blocker:** #1410, merged 2026-08-09 (`f9ae5ab`). Both tiers are now reachable; neither assertion is vacuous.

---

## Files to read first

Generated from the codegraph query for this ticket plus the ticket's own citations. `codegraph_context` returned only `protocol.TypeRateLimited` (one node) — the work is almost entirely test and fake-harness code, which the index carries thinly — so the list below is the ticket's cites verified at `f9ae5ab` and expanded by direct reads.

| Path + lines | What to extract |
|---|---|
| `internal/e2e/relay_v2_stream_unrecognized_test.go:46-239` | **The template.** The whole drive shape: pair → seed → fakerelay → daemon → phone dial → interactive handshake → `sealSend` → drain-until-`turn_end`. Lift `sealSend` / `nextEnv` verbatim in spirit. Note its assertion is a POSITIVE (`len(unrecognized) != 2`); this ticket inverts that, which is why the control exists. |
| `internal/e2e/internal/fakeclaude/main.go:332-356` | The env-const block. `envStreamBogus` at `:352`; the new value-carrying const goes here, beside the value-carrying precedents (`envJSONLTriggerDir`, `envApproveSocketFile`, `envTrustTrigger`). |
| `internal/e2e/internal/fakeclaude/main.go:606-629` | `main()`'s stream branch — where the rider value is read (`:628`) and passed. The `envStreamBogus` comment at `:621-627` is the doc shape to mirror. |
| `internal/e2e/internal/fakeclaude/main.go:1455-1509` | `runStreamJSON` doc + signature + turn loop. The `emitBogus` hook at `:1481-1485` is exactly where the new hook goes. The doc's "-race clean by construction" claim must survive. |
| `internal/e2e/internal/fakeclaude/main.go:1579-1615` | `writeBogusLines` + its needle constants — the writer shape and the "constants not inline literals so the e2e asserts against the same strings" rationale. |
| `internal/e2e/internal/fakeclaude/main.go:1378-1382` | `streamSessionID = "fake-stream"` — the `session_id` the fed line stamps. |
| `internal/e2e/internal/fakeclaude/stream_detect_test.go:269-324` | The two bogus-rider unit tests (`_BogusRider`, `_BogusRiderOffIsByteIdentical`) — the exact pair to mirror. Also the file holding 9 of the 10 `runStreamJSON` call sites. |
| `internal/streamsup/parser.go:1156-1275` | `emitRateLimit` — the three-rung gate. Rung 1 (benign → silence) is AC-1's subject; rung 2 (any other non-empty status → one event) is AC-2's. |
| `internal/streamsup/parser.go:627-662` | `rateLimitEventLine` / `rateLimitInfo`. **The exact JSON keys the fake must write:** `rate_limit_info.status`, `.rateLimitType`, `.resetsAt`. Note `uuid`/`session_id`/the four overage keys are deliberately absent from the decode target. |
| `internal/streamsup/parser.go:258-295` | `benignRateLimitStatus = "allowed"` (`:279`) and the closed drop-reason set. Read the doc for why the match is byte-exact. |
| `internal/streamsup/parser.go:211-256` | `maxRateLimitField = 256` — the per-string cap, and why there is no rate bound. Tells you AC-2's fixture is nowhere near truncation. |
| `internal/turnbridge/outbound.go:211-252` | The mapper. Every field crosses verbatim; **a nil `TruncatedFields` is what puts `"truncated_fields":null` on the wire** — `RateLimitedPayload` deliberately has no nil→`[]` `MarshalJSON`. |
| `cmd/pyry/interactive_turn_v2.go:296-314` | The emitter arm: `flushDelta` then `emitMapped(ctx, convID, ev)`, no turn-lifecycle mutation. Confirms the frame is conversation-scoped and needs no open turn. |
| `internal/protocol/interactive.go:338-397` | `RateLimitedPayload` field names + JSON tags (`conversation_id`, `status`, `limit_type`, `resets_at`, `truncated_fields`), and the SECURITY paragraph. |
| `internal/e2e/realclaude/interactive_stream_liveness_test.go:167-288` | `drainForCompletedTurn`. The `TypeUnrecognizedMessage` arm at `:229-258` is the sentinel template — copy its "here is the legitimate cause, here is the bug" phrasing. Update the doc comment's "a THIRD thing" at `:174-176`. |
| `internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json:57-65` | The captured line. Its `rate_limit_info` object is what AC-1 means by "match the capture". `$SESSION_ID` and the `uuid` are placeholders — the fake supplies its own. |
| `internal/e2e/harness.go:409-467` | `StartStreamInteractiveWithRelay` — `extraEnv ...string` is how the rider reaches the child. |
| `internal/e2e/harness.go:469-488` | `seedBoundConversation` — the second half of the UUID double-seed. A mismatch with `seedBootstrapRegistry`'s id drops every event and hangs the drain for the full deadline. |
| `docs/knowledge/features/protocol-package.md:1219` | The single evergreen sentence AC-4 corrects. |

---

## Context

`emitRateLimit`'s three-rung gate has unit coverage at the parser. What it has never had is proof that its **silence** survives parser → `turnbridge.MapEvent` → the interactive v2 emitter → an encrypted frame on a connected client. #1410 closed the last gap in that chain (the mapper case), so both tiers below now assert something.

Two properties, two tiers that are not substitutes:

- **Hermetic** *feeds* the captured `status: "allowed"` bytes. Only a fake claude can make a chosen line arrive on demand. Runs on every `make check`.
- **Live** *observes* whatever a real claude sends, including a status nobody has captured. Runs in the opt-in pre-ship gate.

**The design problem this spec exists to solve:** AC-1 is a zero-assertion, and a zero does not prove its own input arrived. The reply's own milestones do not restore that proof — an assistant delta and a turn end show *the reply* flowed, not that *the fed line* reached `emitRateLimit`. The answer is a control that shares the feed path and differs from the silent case in exactly one byte-string: the status. Everything in § Design below is arranged so that "differs only in the status" is true by construction rather than by inspection.

---

## Size check

**Verdict: S. No split.** Recorded per red line, counted raw.

| Red line | Count | Trips? |
|---|---|---|
| > 3 new files | 1 (`internal/e2e/relay_v2_stream_rate_limit_test.go`) | no |
| > ~600 lines total written | ~380 projected (see below) | no |
| > 5 new exported types/interfaces | 0 | no |
| > 10 consumer call sites | **10** (`runStreamJSON`: `main.go:628` + `stream_detect_test.go:50, 88, 122, 147, 170, 196, 275, 312, 313`) | no — at the ceiling, not over |
| > 5 acceptance criteria | 4 | no |
| > ~10 reject branches in a state machine | 0 (no new state machine) | no |

Production source files (`*.go`, excluding `*_test.go`) the spec prescribes changes to: **1** (`internal/e2e/internal/fakeclaude/main.go`). The ≥5 gate does not trip.

Total-LOC projection — deliberately larger than the ticket's 285, because the ticket's figure omits the fake's own unit tests and the call-site cascade:

| Item | LOC |
|---|---|
| Fake: env const, `runStreamJSON` param + hook, `writeRateLimitEvent`, constants, doc comments | ~50 |
| Fake: two unit tests in `stream_detect_test.go` (mirroring the bogus-rider pair) | ~60 |
| Fake: 10 call-site edits (one trailing argument each) | ~10 |
| Hermetic e2e: shared drive helper + two tests + doc comments | ~230 |
| Live sentinel arm + `drainForCompletedTurn` doc-comment update | ~30 |
| Doc correction (one sentence) | ~2 |

The 10 call sites are counted raw and listed above so the developer does not hunt for them. They are **not** discounted as mechanical: each still costs a read-edit-verify cycle. Ten is the ceiling, not a pass — if the developer finds an eleventh (a call site added since `f9ae5ab`), that is a signal to stop and route back, not to absorb it.

**Edit fan-out note.** `codegraph_callers` was not used for `runStreamJSON`: it is an unexported function in a `main` package, and the recorded limitation (`codegraph_callers` returns same-package callers only) makes it exactly as informative as the grep here, where every caller *is* same-package. The grep is complete for this symbol.

**File-overlap check:** run at `f9ae5ab` against all 49 `origin/feature/*` branches (branch-based, not PR-based) for the five target paths. **Zero overlaps.** No `blockedBy` set.

---

## Design

Three independent deliverables plus one doc line. Nothing depends on anything else landing first, so they can be built in any order; the fake's rider is the natural first step because the hermetic e2e cannot run without it.

### 1. The fake's rate-limit rider (AC-1 + AC-2's feed)

**Env knob.** Add to the const block at `main.go:332-356`:

```go
envStreamRateLimit = "PYRY_FAKE_CLAUDE_STREAM_RATE_LIMIT"
```

**Value-carrying, not boolean.** The value *is* the `rate_limit_info.status` the rider writes. This is the load-bearing design choice: AC-1 and AC-2 differ only in this string, so one knob keeps both cases provably on one path. Unset or empty ⟹ rider off ⟹ output byte-identical to today.

A consequence to state rather than work around: rung 3 (absent / empty `rate_limit_info`) is **not reachable** through this knob, because empty means "off". Rung 3 already has parser-tier coverage (`parser_test.go:2754-2769`) and needs none here.

**Signature.** `runStreamJSON` grows a fifth parameter:

```go
func runStreamJSON(r io.Reader, w io.Writer, honorInterrupt, emitBogus bool, rateLimitStatus string)
```

Passed as a value, keeping the function a pure I/O seam — the same reason `emitBogus` is a value (`main.go:626-627`). Do **not** read the env inside `runStreamJSON`.

*Elegance check, recorded so it is not re-litigated at review:* five positional parameters with three riders is the smell that argues for a `streamRiders` struct. Rejected here — collapsing the existing two riders into a struct is a refactor of ten call sites for no behaviour, against Simplicity First. Revisit when a fourth rider arrives.

**Turn-loop hook.** Directly after the `emitBogus` block at `:1481-1485`, before the reply: when `rateLimitStatus != ""`, write one rate-limit line; return on write error, exactly as the bogus hook does.

Ordering is the whole ordering argument, inherited from the bogus rider: the rider writes **before** the reply, so by the time `turn_end` reaches the phone the line has necessarily been through the parser. No sleeps, no polling, no race to tune.

**Writer.**

```go
// writeRateLimitEvent writes one top-level rate_limit_event line carrying the
// captured rate_limit_info object with `status` substituted.
func writeRateLimitEvent(w io.Writer, status string) error
```

Behaviour: one `writeJSONLine` of the shape below; returns the first marshal/write error. Uses `map[string]any` like `writeBogusLines` (map keys marshal sorted, so output is deterministic).

The object is transcribed from `dropped_lines_v2.1.220.json:64` — **all six keys**, not the three the daemon reads. Carrying `overageStatus` / `overageDisabledReason` / `isUsingOverage` costs nothing and makes "match the capture" literally true, while also proving the decode ignores keys absent from `rateLimitInfo`:

```jsonc
{"type":"rate_limit_event",
 "rate_limit_info":{"status":<the knob's value>,"resetsAt":1785699000,
   "rateLimitType":"five_hour","overageStatus":"rejected",
   "overageDisabledReason":"org_level_disabled","isUsingOverage":false},
 "uuid":<the fake's own>,"session_id":"fake-stream"}
```

`uuid` and `session_id` are the envelope identifiers the ticket flags as placeholders in the capture: the fake supplies its own (`streamSessionID` for the session, a fixed synthetic UUID literal for `uuid`). Neither is in the parser's decode target, so neither can affect the gate.

**Constants**, beside the bogus needles at `:1607-1615` and for the same stated reason (the e2e asserts against the same strings the fake writes, across a `main`-package boundary): the captured `resetsAt` (`1785699000`) and `rateLimitType` (`"five_hour"`), plus the synthetic `uuid`.

**Fake unit tests** in `stream_detect_test.go`, mirroring the bogus pair:

- *Rider on* — table-driven over the two statuses the e2e uses (`"allowed"`, `"e2e-not-allowed"`). Per row: exactly 3 output lines; line 0 has `type == "rate_limit_event"`; its `rate_limit_info.status` equals the row's input verbatim; `rateLimitType`/`resetsAt` equal the captured constants; the echo and result lines are unchanged. Table-driven *is the point* — it pins "the two cases differ only in status" at the cheapest tier.
- *Rider off is byte-identical* — off vs on, tail after the prepended line matches the untouched two-line output.

**Call sites.** All ten listed in § Size check take a new trailing `""` except the two new tests.

### 2. The hermetic e2e (AC-1 + AC-2)

New file `internal/e2e/relay_v2_stream_rate_limit_test.go`, `//go:build e2e`, package `e2e`. One shared drive helper, two test functions.

Two daemon spawns are required — the knob's value differs per spawn — so the drive shape is factored rather than duplicated. Do not `t.Parallel()`; match the six sibling relay-v2 specs.

**Helper contract:**

```go
// driveRateLimitTurn spawns a stream-interactive daemon whose fakeclaude runs the
// rate-limit rider at riderStatus, drives one ordinary turn from a connected
// interactive v2 client, and returns what the turn put on the wire.
func driveRateLimitTurn(t *testing.T, riderStatus string) rateLimitObservation

type rateLimitObservation struct {
	rateLimited  []protocol.RateLimitedPayload
	unrecognized int
	sawEcho      bool
	sawTurnEnd   bool
}
```

Steps, all lifted from the template at `relay_v2_stream_unrecognized_test.go:63-186`:

1. `shortHome` → `pair` → `decodePairPayload` → decode the server static pubkey.
2. `seedBoundConversation(t, home, knownConvID, initialUUID)` — **the UUID double-seed.** `initialUUID` must be the same value passed to `StartStreamInteractiveWithRelay`; a mismatch drops every event at the drain gate and hangs for the full deadline.
3. `fakerelay.New(relayTestLogger())` + cleanup.
4. `StartStreamInteractiveWithRelay(t, home, initialUUID, fr.URL()+"/v2/server", "PYRY_FAKE_CLAUDE_STREAM_RATE_LIMIT="+riderStatus)`. **This env is the only thing distinguishing this spec from the plain send spec, and the only thing distinguishing the two tests from each other.**
5. `readPersistedServerID` → `waitBinaryHello` → `fakephone.Dial` (3 s) → `driveHandshakeToOpenDaemonInteractive`. **The interactive handshake is load-bearing, not incidental:** this stream is capability-gated — frames reach only a phone whose `interactive` capability was echoed in `hello_ack` (`docs/protocol-mobile.md:521-523`). A non-interactive handshake yields zero `rate_limited` frames in *both* tests, which would make test 1 vacuously green. Test 2 is what catches it.
6. Seal and send one `send_message` for `knownConvID`.
7. Drain until `turn_end` or a 30 s deadline, collecting: every `TypeRateLimited` payload, a count of `TypeUnrecognizedMessage`, whether an `assistant_delta` carried the echo needle, whether `turn_end` landed.
8. Return.

**The helper asserts none of the ACs.** It `t.Fatalf`s only on transport and decode faults — a `TypeError` envelope, a payload that will not decode, a receive error other than timeout. On deadline it returns with `sawTurnEnd` false. That is deliberate: it keeps both milestone flags *live values the tests assert*, which is what AC-1's "in a run whose positive milestones the same test asserts" requires. A helper that fataled on the deadline would make the milestone assertions dead.

**Nonce discipline — do not get this wrong.** The receive nonce is sequential. Every `noise_msg` must be decrypted in receive order; skipping one desyncs the `CipherState` and every later decrypt fails with a misleading error. Filter *after* decrypting, never before. Non-`noise_msg` inner frames do not advance the nonce and are skipped without decrypting (template `:132-134`).

**Test 1 — AC-1, the silence.** Drives with `"allowed"`.

Assertion order matters and is prescribed: milestones first (fatal), then the zero. A zero read off a run that never completed says nothing.

- `sawEcho` must be true — else the reply never arrived.
- `sawTurnEnd` must be true — else the turn never closed.
- `len(rateLimited)` must be 0.
- `unrecognized` must be 0 — the fed line must not land in the unrecognized lane. This pins, end-to-end for the first time, the structural guarantee `emitRateLimit`'s doc claims at `parser.go:1213-1220`, and it turns the ticket's "the test is not looking at the lane that did light up" failure into an immediate diagnosis rather than an inference.

The `"allowed"` literal is transcribed from the capture, **not** derived from `streamsup.benignRateLimitStatus` (which is unexported and must stay uncoupled). If that constant is ever renamed, this test *should* go red — that is the alarm, per the same fixture discipline recorded at `parser_test.go:2562`.

**Test 2 — AC-2, the arrival control.** Drives with `"e2e-not-allowed"`.

- Same two milestone assertions first.
- `len(rateLimited)` must be exactly 1.
- `unrecognized` must be 0.
- On that one frame: `ConversationID == knownConvID`; `Status` equals the fed status verbatim; `LimitType == "five_hour"`; `ResetsAt == 1785699000`; `TruncatedFields == nil`.

`TruncatedFields == nil` is a live assertion, not decoration: `null` decodes to nil, `[]` decodes to an empty non-nil slice, so it distinguishes the two — and `RateLimitedPayload` deliberately carries no nil→`[]` `MarshalJSON` precisely so that nothing-was-cut stays an absence on the wire (`outbound.go:234-240`). Unit tests pin this at the byte level; this is its first proof through a real daemon to a phone.

**The fixture status.** `"e2e-not-allowed"`: non-empty (clears rung 3), not the benign value (takes rung 2), distinctive enough that the assertion cannot pass on another frame's content, obviously synthetic so no reader mistakes it for a measured claude value, and 15 bytes — an order of magnitude under `maxRateLimitField`, so the truncation path is untouched.

**Doc comment on the pair.** State the control relationship where a reader meets it: without test 2, test 1's zero means "nothing was observed"; with it, the zero means "the line arrived and the gate chose silence". Name the three bugs the control kills — a misspelled env var at the test end, a malformed `"type"` that routes to the unrecognized lane, a line dropped before the parser.

### 3. The live sentinel (AC-3)

One new `case protocol.TypeRateLimited:` arm in `drainForCompletedTurn`, beside the `TypeUnrecognizedMessage` arm at `interactive_stream_liveness_test.go:229`. Zero call-site changes — all six live stream specs inherit it.

Contract: decode `protocol.RateLimitedPayload` (`t.Fatalf` on decode failure, matching the sibling), then `t.Fatalf` unconditionally. Terminal, like its sibling — it does not `continue`.

**No conversation filter**, matching the unrecognized arm. A usage limit is account-scoped; a frame bound to any conversation is still the alarm.

The message must carry both readings, legitimate cause first:

1. **A usage limit really is in force on this account right now.** Not a daemon bug. The window resets — re-run then.
2. **claude renamed or recapitalised the benign status.** `streamsup.benignRateLimitStatus` (`"allowed"`) has gone stale, so every healthy run now emits. One constant edit fixes it; confirm against a fresh drop-census capture first.

Print the five payload fields to let a reader tell those apart at a glance — `status` is the discriminator. Do **not** print a raw line; there isn't one here.

Update `drainForCompletedTurn`'s doc comment at `:174-176`: it now asserts a fourth thing, and the new negative deserves the same one-line framing the third got.

### 4. The evergreen correction (AC-4)

`docs/knowledge/features/protocol-package.md:1219`, final sentence of the **The rate-limited bridge (#1405's consumer surface)** bullet. Replace exactly:

> The live "healthy turn emits zero `RateLimited`" sentinel (#1404's Open Question 1) is **#1411**, still open.

with past tense naming both tiers, e.g.:

> Both proof tiers for the gate's silence — the hermetic e2e feeding the captured benign `rate_limit_event` plus its non-benign arrival control, and the live standing sentinel in `drainForCompletedTurn` (#1404's Open Question 1) — landed in **#1411**.

Nothing else in that bullet changes; the rest is already past tense as of #1410. This is the only evergreen site making the claim (swept at `f9ae5ab`); frozen specs and per-ticket `codebase/` records are historical and stay.

*Why this doc edit stays with the developer* — the standing rule keeps evergreen docs out of the implementation budget. It does not apply here: this is a one-sentence tense flip at a named line whose replacement text is pre-written above, correcting a claim **this ticket's own change falsifies**. It is the same shape #1410 handled in-branch (`ca2b07e`), not a knowledge-doc authoring task. `docs/knowledge/codebase/1411.md` is **not** a deliverable — documentation phase owns it after merge.

---

## Concurrency model

No new goroutine anywhere in this ticket.

- **Fake.** The rider is one extra `writeJSONLine` on the same single-threaded turn loop that already writes the bogus lines and the reply. `runStreamJSON`'s "no goroutines, no shared state, `-race` clean by construction" property (`main.go:1454-1455`) must survive verbatim — the new parameter is read-only and never escapes.
- **Hermetic e2e.** One receive loop per test, single-threaded, ordered decryption (see the nonce discipline above). Daemon, fakerelay and fakephone lifecycles are the template's, via `t.Cleanup`.
- **Live sentinel.** One more `case` in an existing loop.

**Ordering guarantee** (the only synchronisation this ticket relies on): the rider writes its line before the reply on the same stdout, in one goroutine, so `turn_end` arriving at the phone implies the fed line already traversed the parser. Nothing here waits on a sleep or a poll.

---

## Error handling

| Failure | Behaviour |
|---|---|
| Rider marshal/write error in the fake | Return the first error; the turn loop returns, exactly as the bogus rider does. The child exits; the test fails at the drain deadline. |
| `TypeError` envelope during the drain | `t.Fatalf` with the payload — the template's behaviour (`:167-168`). |
| Payload decode failure during the drain | `t.Fatalf` naming the frame type. |
| Receive timeout | Not fatal in the helper. The loop exits with the milestone flags as observed; the *test* reports which milestone was missing. |
| Live sentinel: undecodable `RateLimitedPayload` | `t.Fatalf`, matching the sibling arm. |

**Diagnostics are load-bearing here.** The ticket flags this as the highest-variance work in turns: each failed hermetic drain burns the full 30 s deadline, and the classic cause (a UUID mismatch between the two seeds) presents as an unexplained hang. So every drain-failure message must print the counts observed so far — `rateLimited`, `unrecognized`, `sawEcho` — the way the template does at `:163-165`, and the milestone messages must name the seed mismatch as the first suspect the way `drainForCompletedTurn` does at `:193-195`. The goal is that the first red run is diagnostic, not the third.

---

## Testing strategy

| Tier | Command | Covers |
|---|---|---|
| Unit (untagged) | `make check` | The fake's rider: shape, verbatim status, default-off byte-identity |
| Hermetic e2e (`e2e` tag) | `make check` | AC-1 + AC-2, every `make check` |
| Live (`e2e_realclaude` tag) | `make e2e-realclaude` | AC-3, opt-in pre-ship gate |

`make check` must be green before the PR. The live tier carries `needs-real-claude` because an exit code cannot distinguish a skipped live suite from a passing one — a human confirms the sentinel ran.

**Discrimination checks.** Run these as overlay mutations (`go test -overlay=<abs-path json>`, no worktree writes) and confirm each goes RED. A row that stays green means the assertion it names is not doing work:

| Mutation | Must go RED |
|---|---|
| Misspell the rider env var in the test only, so the rider never fires | Test 2 (0 frames, want 1) |
| Fed line's `"type"` → `"rate_limit_event_x"` | Test 2 (0 rate_limited) **and** both tests' `unrecognized == 0` |
| `benignRateLimitStatus` → `"allowedX"` in `parser.go` | Test 1 (1 frame, want 0) |
| Drop `status` from the fed `rate_limit_info` | Test 2 (rung 3 → silence) |
| Delete the `case turnevent.RateLimited` arm in `interactive_turn_v2.go` | Test 2 |
| Give `RateLimitedPayload` a nil→`[]` `MarshalJSON` | Test 2's `TruncatedFields == nil` |

The first three are the ones that matter: together they are the proof that the pair is non-vacuous in both directions.

---

## Deliberately not in scope

- **No production code.** Nothing under `internal/streamsup`, `internal/turnbridge`, `internal/protocol`, or `cmd/pyry` changes. The gate landed in #1404, the wire shape in #1405, the mapping in #1410.
- **`docs/protocol-mobile.md` is untouched.** Both "fourteen turn-stream events" literals (`:523`, `:980`) are correct — this ticket adds no frame and changes no count. Its dated changelog entry at `:1637` is a historical record and stays.
- **No fixture or testdata change.** The capture is read, never edited.
- **Truncation is not exercised.** AC-2's 15-byte fixture is far under `maxRateLimitField`; the truncation path keeps its existing unit coverage. `TruncatedFields == nil` asserts the *absence* encoding, not a truncation claim.
- **Rung 3 is unreachable through the rider** (empty means off) and stays parser-tier.
- **`docs/knowledge/codebase/1411.md`** — documentation phase, after merge.

---

## Open questions

1. **The non-benign fixture value.** `"e2e-not-allowed"` is prescribed. The alternative is `"rejected"`, which the capture carries as `overageStatus` — rejected here because a plausible-looking value reads as measured when the value set beyond `"allowed"` is explicitly unmeasured (`parser.go:263-268`). If code-review disagrees it is a one-constant edit; the property under test is identical.
2. **`t.Fatalf` vs `t.Errorf` in the live sentinel.** `Fatalf` is prescribed, matching the sibling at `:253`. If a limit is genuinely in force, the remaining live specs would burn tokens against a limited account anyway.
3. **Both riders set simultaneously** (bogus + rate-limit). Nothing forbids it, nothing needs it, no test does it. Left unconstrained rather than mechanised — an observed failure would be the trigger to add a guard, not this.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The only boundary touched is the existing one — fakeclaude's stdout → the daemon's parser — and no new parse site is added. The load-bearing property is that `emitRateLimit` decodes from `line`, the **top-level** bytes, never a nested field (`parser.go:1205-1210`); that is what stops a tool result whose text is literally a `rate_limit_event` from forging a usage-limit report. The rider writes at the top level only and deliberately does **not** exercise nested placement, so this ticket cannot establish nested decoding as tested-and-fine. Downstream, the frame is a REPORT: `RateLimitedPayload`'s doc (`interactive.go:386-390`) states a client MUST NOT branch security-relevant behaviour on `Status`. AC-2 asserts the value crosses verbatim — a *transport* claim only; nothing here licenses client-side branching on it.
- **[Tokens, secrets, credentials]** No findings. The hermetic test handles a pairing token and the server static pubkey through the unchanged existing helpers (`decodePairPayload`, `driveHandshakeToOpenDaemonInteractive`); no token is minted, persisted, rotated, or revoked by this ticket. Constraint carried into the spec: no failure message prints `payloadA.Token` or any handshake material — the template prints neither, and the new helper's failure messages print frame counts and milestone flags only.
- **[File operations]** No findings. The design creates no file at runtime and builds no path from the rider's value. `config.json` (`0600`) and `conversations.json` (`0600`) under `<home>/.pyry` (`0700`) are written by the existing `StartStreamInteractiveWithRelay` / `seedBoundConversation` helpers, reused unmodified. No `os.Stat`-then-open, no symlink traversal, no atomic-write obligation introduced.
- **[Subprocess / external command execution]** No findings, and one property worth naming: the rider is delivered by **environment**, never by argv. `StartStreamInteractiveWithRelay`'s `extraEnv` reaches the child through the daemon's inherited environment (the same channel `envStreamBogus` already uses), so the knob cannot perturb the spawn shape the sibling specs measure. No `sh -c`. No new `exec.Command` argument is built from any test-controlled string.
- **[Cryptographic primitives]** No new primitive, no new key, no new RNG use. One real hazard, promoted into § Design rather than left to discovery: the Noise receive nonce is sequential, so the drain must decrypt **every** `noise_msg` in receive order and filter afterwards. A helper that skipped frames before decrypting would desync the `CipherState` and fail every later decrypt with a misleading error — the failure would read as a crypto bug and is not one.
- **[Network & I/O]** No findings. No new socket, no new listener, no new cap. The frame rides the existing v2 application envelope whose 65519-byte cap is unchanged, and one `RateLimited` carries at most 512 bytes of claude-derived text by construction (`parser.go:226-232`), so the fed line cannot approach it. The hermetic drain is bounded at 30 s; the live sentinel inherits its spec's existing budget.
- **[Error messages, logs, telemetry]** SHOULD FIX — resolved in-spec, recorded so it is not "fixed" backwards later. `emitRateLimit`'s doc is emphatic that **nothing from the payload is logged on any path** (`parser.go:1222-1226`), `Status` above all. The live sentinel's `t.Fatalf` prints `Status` and `LimitType`. That is not a violation of that rule and must not be read as one: the prohibition governs the **daemon's** operational log, which reaches operators and the `pyry logs` ring, whereas the sentinel is a test failure in an opt-in, locally-run suite — and its sibling arm already prints an unrecognized line's entire raw JSON (`:258`), strictly more exposure. Both printed strings are capped at 256 bytes before reaching the wire. Constraints the spec therefore carries: the sentinel prints only the five payload fields, never a raw line; **no daemon-side logging is added anywhere in this ticket**; and nothing is written to disk, so the realclaude package's capture-redaction regime (`dropcapRedactor`, the credential deny-scan) does not apply and must not be half-copied.
- **[Concurrency]** No findings. No goroutine is spawned, so there is no lifecycle or leak to reason about; no lock is taken, so there is no ordering to document. The fake's turn loop and both drain loops are single-threaded. `t.Parallel()` is deliberately not used on the two hermetic tests, matching the six sibling relay-v2 specs. `runStreamJSON`'s "`-race` clean by construction" property is preserved because the new parameter is read-only and never escapes the loop.
- **[Threat model alignment]** No findings. `docs/protocol-mobile.md` § Security model's relevant threat is a hostile or compromised claude child forging frames; this ticket does not widen it. The new knob lives in `internal/e2e/internal/fakeclaude`, which is never built into `pyry` — **the daemon gains no new environment surface**, reads no new variable, and behaves identically whether or not `PYRY_FAKE_CLAUDE_STREAM_RATE_LIMIT` is set in its environment. The one threat this ticket touches only partially, named rather than silently deferred: a claude that renames the benign status value is detected by the **live** tier alone (the hermetic tier pins the measured bytes and would stay green through such a rename). That asymmetry is the ticket's own thesis for shipping both tiers, and the residual — a rename landing between pre-ship gates — remains covered by the live drop census (`parser.go:1197-1203`), not by this ticket.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-09
