# Spec #1173 — realclaude/stream: multi-turn continuity against live claude

**Ticket:** [#1173](https://github.com/pyrycode/pyrycode/issues/1173) · **Size:** S ·
**Security-sensitive:** no (no `security-sensitive` label — plain conversational
continuity, no permission modal, no device gate; contrast #1154/#1175)

**One-line:** Add one real-claude e2e that drives ≥3 sequential turns on a single
held-open stream-json interactive session and proves the same live child retained
context across turns by planting a per-run token and asserting a later turn recalls it.

---

## Context

The interactive stream runner (`internal/streamsup`) exists to hold one live `claude`
child's stdin open across many turns with context intact. Today only two real-claude
e2es exercise it, both **single-turn**:

- `interactive_stream_liveness` (#1153) — one ungated turn.
- `interactive_stream_modal_resolution` (#1154) — one permission-gated turn.

Every multi-turn stream behaviour is covered only against a scripted fakeclaude
(`internal/e2e/relay_v2_stream_*`), which cannot catch the recurring fake-green/real-red
class (#949). The runner's whole reason to exist — cross-turn continuity on a held-open
child — has **no** real-claude coverage. This ticket adds exactly one behaviour:
multi-turn continuity on a single stream session. Part of #1083 (T9); sibling leaves are
#1174 (new-session rotation) and #1175 (permission DENY), out of scope here.

This is a **direct transcription of #1153** (`interactive_stream_liveness_test.go`) with
two deltas: (1) drive a loop of ≥3 turns instead of one, and (2) add a content-dependent
continuity assertion on a later turn. No production change — the stream runner already
exists and is green against fakeclaude.

---

## Files to read first

Everything is in package `realclaude` under the `e2e_realclaude` build tag; all helpers
below are reused **verbatim** (same package, same tag). The new file transcribes the
#1153 body and adds one drain helper.

- `internal/e2e/realclaude/interactive_stream_liveness_test.go` (whole file, ~255 lines)
  — **the transcription base.** Copy its `TestInteractiveStreamLiveness` setup body
  (lines 73–133: LookPath → auth → workdir → `writeStreamInteractiveConfig` → pair →
  seed×2 → fakerelay → `spawnBootstrapDaemon` → serverID → `waitBinaryHello` → Dial →
  `driveHandshakeInteractive`) almost line-for-line; replace the single `sealSendMessage`
  + `drainForCompletedTurn` (lines 138–141) with the turn loop. Also read
  `writeStreamInteractiveConfig` (155–165, the stream-json toggle) and
  `drainForCompletedTurn` (181–254) — the new capturing drain is its superset.
- `internal/e2e/realclaude/interactive_bootstrap_liveness_test.go` — home of every reused
  helper. Exact signatures to satisfy:
  - `sealSendMessage(t, phone, cs *noise.CipherState, id uint64, convID, msgID, text string)` (160) — **note `id` is `uint64`** (loop index needs `uint64(...)`).
  - `spawnBootstrapDaemon(t, home, workdir, claudeBin, relayURL string) *bootstrapDaemon` (383) — ungated spawn (`--dangerously-skip-permissions`); reuse this, NOT `spawnPermissionDaemon` (no tool use → no modal).
  - `seedBootstrapRegistry(t, home, bootstrapUUID)` (529), `seedBoundConversation(t, home, convID, boundSessionID, cwd)` (546), `driveHandshakeInteractive` (247), `runPyry` (481), `readPersistedServerID` (509), `decodePairPayload` (559), `waitBinaryHello` (575), `relayTestLogger` (584).
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go:85` — `const perTurnReplyBudget = 120 * time.Second`. Reuse per turn (this file already drives multiple sequential turns each with this budget — the established norm; `interactive_session_control_liveness_test.go` drives three).
- `internal/streamsup/roundtrip_test.go:21` — `TestParser_MultiTurnRoundTripZeroBleed` — the unit-level proof of the same held-open-stdin, reuse-across-turns invariant this e2e proves against real claude. Read to confirm the mental model (capture stdin once, no per-turn respawn); do **not** couple to it.
- `internal/protocol` — `AssistantDeltaPayload{ConversationID, Seq, Text}` and `TurnStatePayload{State, ConversationID}` are the two payloads the drain decodes (both already used in `drainForCompletedTurn`).

**UUID collision map (must not redeclare a name or reuse an in-flight literal).** Existing
package literals: `77777777`/`55555555` (#854), `99999999`/`66666666` (#1030),
`88888888` (#1153 bootstrap, also #997), `77777777`/`55555555` (#1154),
`aaaaaaaa`/`bbbbbbbb` (session-control), `11111111` (fixtures). In-flight on
`feature/1172`: `runningTurnBootstrapUUID`=`33333333`, `runningTurnConvID`=`44444444`,
plus a `drainForResponding` helper. This spec's fresh names/literals dodge all of them.

---

## Design

### New file

`internal/e2e/realclaude/interactive_stream_multiturn_continuity_test.go`

```go
//go:build e2e_realclaude
package realclaude
```

Imports: identical set to `interactive_stream_liveness_test.go` (context, encoding/base64,
encoding/json, errors, fmt, os, os/exec, path/filepath, strings, testing, time; plus
fakephone, fakerelay, noise, protocol).

### Fixed identifiers (fresh — see collision map)

```go
const (
    streamMultiTurnBootstrapUUID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc" // bootstrap POOL id (seedBootstrapRegistry)
    streamMultiTurnConvID        = "dddddddd-dddd-4ddd-8ddd-dddddddddddd" // driving conversation, bound to it
)
```

Distinct NAMES from every same-package const (compile-safety) and distinct LITERALS from
`feature/1172`'s `33333333`/`44444444` (hygiene). `4`/`8` version/variant nibbles match the
existing UUIDv4-shaped fixtures.

### Test: `TestInteractiveStreamMultiTurnContinuity`

Body = #1153's setup transcribed **verbatim** (no `t.Parallel`; skip when `claude`/creds
absent via `exec.LookPath` + `WithWorktreeAuthenticated`), seeding
`streamMultiTurnBootstrapUUID` / `streamMultiTurnConvID`, spawning via
`spawnBootstrapDaemon`, up to `initSend, initRecv := driveHandshakeInteractive(...)`.

Then, in place of #1153's single send:

- `nonce := time.Now().UnixNano()` (per-run, defeats cross-run caching — same pattern #1153 uses).
- `token := fmt.Sprintf("PYRY%X", nonce)` — a contiguous uppercase alphanumeric identifier
  (`%X` → uppercase hex). Opaque, no internal separators for claude to reformat.
- A **3-entry turn plan**, driven strictly sequentially over the one held-open session
  (`initSend`/`initRecv`, `streamMultiTurnConvID`) — each turn fully drains to
  `turn_state{idle}` **before** the next send, so no cross-turn frame bleed:

  | # | envelope id | msgID | prompt (intent) | drain |
  |---|-------------|-------|-----------------|-------|
  | 1 (plant)  | 2 | `m-1` | "Remember this exact identifier for the rest of our conversation: `<token>`. Reply with just the word ok." | non-empty delta → idle |
  | 2 (filler) | 3 | `m-2` | "Reply with a single short word. run=`<nonce>`" | non-empty delta → idle |
  | 3 (recall) | 4 | `m-3` | "What was the exact identifier I asked you to remember earlier? Reply with only that identifier and nothing else." | non-empty delta → idle, **capture text** |

  The **filler** turn (2) is load-bearing for AC #1: it proves the child served an
  intervening turn between plant and recall without respawn — a 2-turn plant→recall would
  not. Total = 3 turns ⇒ AC #1's "≥3 sequential turns on one held-open child".

- Each send: `sealSendMessage(t, phone, initSend, uint64(id), streamMultiTurnConvID, msgID, prompt)`.
- Each drain: `text := drainForCompletedTurnText(t, phone, initRecv, streamMultiTurnConvID, perTurnReplyBudget)`.
- After the **recall** turn only, assert continuity by content:
  `strings.Contains(strings.ToUpper(text), token)` must hold — else `t.Fatalf` naming the
  recall text and token. Case-insensitive (`ToUpper`) absorbs any hex case-folding by claude;
  `Contains` (substring, not equality) absorbs surrounding words. This is the observable
  that distinguishes "same child served all turns" from "unexpected respawn" (AC #3): a
  freshly respawned child has no memory of the planted token, so the reference can only
  appear if one live child retained context.

**No pid/process inspection.** AC #1's "no respawn" is proven by AC #3's content
continuity, not by inspecting the child pid (the harness does not expose it, and content
memory is a strictly stronger observable). Deliberate Simplicity-First choice.

### New drain helper: `drainForCompletedTurnText`

A **text-capturing superset** of `drainForCompletedTurn` (#1153). Contract:

```go
func drainForCompletedTurnText(t *testing.T, phone *fakephone.Client, cs *noise.CipherState,
    convID string, timeout time.Duration) string
```

Behaviour is **byte-identical** to `drainForCompletedTurn`'s milestone semantics — so AC #2
("the same full-turn drain the sibling liveness spec asserts, applied per turn") holds — with
exactly one addition: it **accumulates every** `assistant_delta.Text` for `convID` (not just
the first) into a `strings.Builder` and **returns the concatenation** when M2 fires. Rationale
for a new helper rather than editing the shared one: the recall assertion needs the full reply
text, `drainForCompletedTurn` exposes none, and editing it would touch two merged sibling call
sites (liveness:141, modal:117) and change asserted behaviour — outside "one new file, no
production change". The ~50-line duplication is self-contained test code.

Reuse the sibling's exact frame loop (do not reinvent it — read `drainForCompletedTurn`
181–254 and mirror it):

- Read `phone.ReceiveBytes(remaining)` in receive order; on `ErrReceiveTimeout` re-loop into
  the deadline branch; any other receive error → `t.Fatalf`.
- Decode `protocol.InnerFrameV2`; **skip non-`TypeNoiseMsg` frames without decrypting** (they
  do not advance the receive nonce — identical to the sibling; getting this wrong desyncs the
  CipherState).
- Decrypt in order, decode `protocol.Envelope`, switch on `env.Type`:
  - `TypeAssistantDelta`: decode `AssistantDeltaPayload`; if `ConversationID == convID` and
    `strings.TrimSpace(Text) != ""`, set `sawDelta = true` (M1, first non-empty) **and always
    append `Text` to the builder** for every matching delta (M1 and after).
  - `TypeTurnState`: ignore until `sawDelta` (the leading `responding` precedes the delta);
    once seen, on `State == "idle" && ConversationID == convID` → M2, `return builder.String()`.
- Deadline branch: if `!sawDelta`, `t.Fatalf` M1 (turn never drained end-to-end — most likely a
  UUID mismatch between the two seeds drops every event); else `t.Fatalf` M2 (delta but never
  idle — turn opened but never closed). Same milestone-specific messages as the sibling.

---

## Data flow

```
phone ──seal send_message(convID)──> daemon ──stdin(held open)──> claude child
phone <──noise_msg: assistant_delta*, turn_state{responding|idle}── daemon <── child stdout

Per turn i (strictly sequential):
  send(id=i+1, msg=m-i, prompt_i) ; drainForCompletedTurnText → text_i (waits to turn_state{idle})
  (only after idle_i does send i+1 leave)

Continuity: token planted in text of turn-1's prompt ; asserted present in text_3 (recall)
            ⇒ one live child accumulated conversation context across turns 1→3.
```

The two seeds gate the drain exactly as in #1153: the stream runner tags turnevents by its
construction-time bootstrap pool id (`seedBootstrapRegistry` → `streamMultiTurnBootstrapUUID`);
the drain gate forwards to the emitter only when the tag == `activeSession()`, which resolves
`streamMultiTurnConvID`'s binding via `seedBoundConversation`. A UUID mismatch drops every event
and hangs the drain → surfaces as the M1 deadline, never a silent pass.

---

## Concurrency model

No new goroutines. The test is a single sequential driver: one `send` then one blocking drain
per turn, three times. The daemon, fakerelay, and claude child run in their own
processes/goroutines (owned by `spawnBootstrapDaemon` and `fakerelay.New`, torn down by the
existing `t.Cleanup`s transcribed from #1153). Strict send→drain-to-idle→send ordering is what
guarantees no cross-turn frame interleaving.

---

## Error handling

- **claude / creds absent** → clean skip (exit 0): `exec.LookPath("claude")` +
  `WithWorktreeAuthenticated` skip, identical to the sibling stream specs (AC #4).
- **UUID seed mismatch / event never reaches emitter** → M1 deadline `t.Fatalf` with the
  cause named (never a silent green).
- **Turn opens but never closes** → M2 deadline `t.Fatalf`.
- **Continuity broken (recall omits the token)** → the `strings.Contains` assertion `t.Fatalf`s
  — this is the real regression the test exists to catch (an unexpected respawn or lost context).
- **CipherState desync** → surfaces as a decrypt error `t.Fatalf` in the drain (guarded by the
  skip-non-noise_msg-without-decrypting rule, mirrored from the sibling).

---

## Testing strategy

The file **is** the test; there is no unit layer beneath it (it is a standing real-claude
liveness gate under `e2e_realclaude`, wired into `make e2e-realclaude`/`make preship` by build
tag, no Makefile change — like #854/#1030/#1153/#1154). It is a liveness gate, not a
deterministic RED/GREEN oracle; the fake tier owns deterministic shape. Non-vacuity is
structural and load-bearing on both ends:

- **Liveness per turn** — each `drainForCompletedTurnText` requires a non-empty delta (M1) then
  terminal idle (M2); "no reply" fails M1, "reply but never closed" fails M2. Neither greens
  vacuously.
- **Continuity by content** — the recall assertion requires the *planted, per-run-unique* token
  to appear in the recall reply. A respawned/memory-less child cannot produce it, so the
  assertion cannot pass vacuously; the per-run nonce defeats cross-run caching giving a false
  green.

Manual verification (developer, once, against live claude):
`ANTHROPIC_API_KEY=… go test -tags e2e_realclaude -run TestInteractiveStreamMultiTurnContinuity -race ./internal/e2e/realclaude/ -timeout 600s`
→ passes green (3 turns, recall echoes the token). With no creds → `SKIP` (exit 0).

Regression sanity the developer should eyeball (not encode): temporarily forcing a respawn
between turns (or hard-coding a stale token in the assertion) must turn the test RED — confirms
the continuity assertion is the binding observable, not decoration.

---

## Scope check (self-audit before commit)

- Production source files (`*.go` excluding `*_test.go`) new/modified: **0** (< 5 gate ✓).
- New files: **1** (a `_test.go`) (≤ 3 ✓).
- Total written LOC: ~150 (test body ~55 + drain helper ~55 + consts/doc ~40) (≤ 600 ✓).
- New exported types/interfaces: **0** (all lowercase test-package identifiers) (≤ 5 ✓).
- Consumer call sites needing simultaneous update: **0** (brand-new file, no fan-out) (≤ 10 ✓).
- Error/reject branches: 3 drain `t.Fatalf`s + 1 continuity `t.Fatalf` (≤ 10 ✓).

No red line tripped. Ships as one S ticket.

---

## Open questions

- **Token reformatting robustness.** `strings.Contains(strings.ToUpper(text), token)` covers
  case-folding and surrounding prose. If real-run flakiness ever shows claude splitting the
  opaque token (e.g. inserting a space mid-token), the mitigation is to tighten the plant/recall
  wording or strip whitespace before comparison — **defer** until observed (Evidence-Based Fix
  Selection; do not pre-build the defense). The contiguous no-separator `PYRY%X` token shape is
  chosen precisely to make a split unlikely.
- **Turn count.** Spec drives exactly 3 (plant/filler/recall) — the minimum that satisfies
  "≥3 … with an intervening turn between plant and recall". If a later reviewer wants a longer
  hold, add filler entries to the turn plan; no structural change (the loop is uniform).
