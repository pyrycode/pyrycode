# #1494 — `eventring.After`: an id beyond the ring's id space is a gap, not caught-up

**Size:** S (confirmed; PO sized S). Two production files (comment-heavy, one operator of real
logic), two test files, three live docs. No new files, no new exported types, three call sites
total across `After` / `NewestID` / `replayMissed`.

**Label:** `security-sensitive` — see § Security review at the end.

---

## Files to read first

Read by symbol, not by line. Every name below resolves with `codegraph_search` / `codegraph_node`.

| File | Symbol / section | What to extract |
|---|---|---|
| `internal/eventring/ring.go` | `Ring.After` | The three-outcome contract and its doc comment — this is the whole production change. |
| `internal/eventring/ring.go` | `Ring.NewestID` | Its doc comment claims to be the clamp source that stops an out-of-range id muting the live stream. That claim narrows here (see § AC #4). |
| `internal/eventring/ring.go` | `Ring.Append`, `convRing.evictOldest` | Why `c != nil ⇒ len(c.events) ≥ 1` — load-bearing for the reachability argument in § AC #4. |
| `internal/relay/v2session_replay.go` | `V2SessionManager.replayMissed` | The sole production consumer of `After`. Its doc comment and its clamp comment both assert the old classification. |
| `internal/relay/v2session_replay.go` | `V2SessionManager.emitResync` | What the gap branch emits: one `TypeResync` envelope, inline `{conversation_id}`, no `EventID`. |
| `internal/relay/v2session_handshake.go` | `handleNoiseInit` (the `helloPayload.LastEventID != nil` tail) | Where `replayMissed` is called from, and why "before any live frame" is structural rather than timing-dependent. Its comment already says "(or emit a resync marker)" — **no change needed here**. |
| `internal/eventring/ring_test.go` | `TestAfter_CaughtUp`, `TestAfter_GapWhenOldestFellOff`, `appendControl` | The row that must move; the gap-test shape to mirror; the fixture helper. |
| `internal/relay/v2session_replay_test.go` | `reconnectScenario`, `reconnectOpenLive`, `appendRingEvents`, `waitForEnvelopes` | The `wantEnvs` / `wantHandshake` counts every retarget below turns on. |
| `internal/relay/v2session_replay_test.go` | `TestV2Session_Reconnect_CaughtUp_NoReplay`, `TestV2Session_Reconnect_Gap_EmitsResync`, `TestV2Session_Reconnect_OutOfRangeLastEventID_LiveStreamDelivered`, `TestV2Session_Reconnect_ClearRotation_LiveStreamDelivered` | The four tests that change. `..._Gap_EmitsResync` is the template for the new test and does **not** itself change. |
| `internal/relay/v2session.go` | `V2Session.replayThrough`, `V2SessionManager.forwardEnvelope` | The guard that turns a watermark into a silent mute — the mechanism the whole ticket is about. |
| `docs/protocol-mobile.md` | § "Reconnect replay & resync (consumer, #647)" | The four-bullet branch table; the Caught-up bullet is stale. |
| `docs/knowledge/features/eventring-package.md` | § "`After` — the three-way replay contract"; the `NewestID` paragraph above it | The condition table and the clamp-purpose sentence. |
| `docs/knowledge/features/v2-session-manager.md` | § Reconnect replay (the `replayMissed` branch list, the `replayThrough` bullet, the intro paragraph's #663 clause, the test-inventory paragraph) | Four separate stale claims — see § Documentation for the full site table. |

---

## Context

`Ring.After` classifies `afterID >= latestID` as caught-up. That lumps two different inputs
together:

- `afterID == latestID` — the phone has everything. Genuinely caught up.
- `afterID > latestID` — the phone names an id **this daemon never issued**. Not caught up;
  the daemon has no idea what that id refers to.

The second case is what a post-restart reconnect looks like. The ring is purely in-memory
(`eventring`'s package doc says so explicitly, and names the gap signal as the restart
fallback), so a daemon restart resets every per-conversation counter to 1. A phone holding
`last_event_id=900` reconnects, another conn drives a turn appending ids 1–10, `After(C, 900)`
sees `latestID=10`, returns caught-up, and the phone gets nothing — no replay, no resync. The
phone then dedups durably on `event_id` (`docs/protocol-mobile.md` tells it to), so it silently
drops every live event until ids climb past 900. A muted stream that looks healthy from both ends.

The empty-ring branch of `After` already gets this right: an unknown conversation queried with
`afterID > 0` is a gap, on exactly the reasoning "the consumer references events this daemon
never had". The non-empty branch just doesn't apply the same reasoning.

**Why the #663 clamp does not fix this.** `replayMissed` already clamps `replayThrough` to
`min(afterID, NewestID)`, so the *daemon* forwards the live events. The mute is phone-side: only
a `resync` makes the phone discard its cursor and accept ids below what it last saw. Reaching the
gap branch is what emits one.

---

## Design

### The production change

One comparison in `Ring.After`, splitting the current single `>=` branch:

```go
latestID := c.nextID - 1
if afterID > latestID {
    return nil, true // an id this daemon never issued → gap → resync
}
if afterID == latestID {
    return nil, false // caught up
}
```

Nothing else in `After` moves. The oldest-fell-off-the-back gap check, the ascending scan, and
the unreachable-tail return all keep their current position and semantics.

This makes the non-empty branch consistent with the empty branch: both now classify "an id this
daemon never issued" as a gap.

### What changes downstream, mechanically

`replayMissed` is the only production caller. For `afterID > newest` it now takes the gap branch:
`emitResync` forwards one `TypeResync` marker and returns — **before** the `replayThrough` clamp
line. So on this input class `replayThrough` is never written at all and stays `0`, and
`forwardEnvelope`'s guard is inert for the conn. Live delivery is preserved by a different
mechanism than before (untouched watermark rather than clamped watermark), which is why AC #3
still holds and why AC #4 exists.

### Ordering — why AC #2's "before any live frame" is structural

`replayMissed` runs at the tail of `handleNoiseInit`'s success path, on the manager's single `Run`
goroutine, and `emitResync` forwards **inline** via `forwardEnvelope` — not through the buffered
push stream. `Run` has not returned to its select, so no `drainOnce` / `drainReplayOnce` pass can
have run. `replayQueue` also stays empty on this path, so the `drainOnce` replay gate is not
engaged and is not needed here. The resync is therefore the first AEAD-transport frame after
`noise_resp`, ahead of any live frame, by construction rather than by timing.

### Behaviour change that is intentional, and its cost

The resync class widens. A phone that previously got silence on an out-of-range
`last_event_id` now gets one marker and does a full reload of the named conversation. That is one
extra reload for a phone that was previously (wrongly) muted — strictly the better trade. The
change **never** produces a replay for an id the daemon did not issue; it only moves inputs from
the silent branch to the resync branch.

### Concurrency model

Unchanged. The edit is inside `After`, under the ring's existing `sync.Mutex`. No new goroutines,
no new locks, no lock-ordering surface. The `NewestID`-then-`After` read window in `replayMissed`
is pre-existing and stays exactly as it is — but it becomes the *only* remaining justification for
the clamp (§ AC #4).

### Error handling

No new failure modes. `After` returns no error and cannot fail; the gap branch's only new work is
`emitResync`, whose marshal-failure and forward-failure paths already exist and already log at
`Warn` / `Debug` without echoing payload bytes.

---

## AC #4 — the #663 clamp's coverage

The ticket asks the architect to decide this rather than leave it to be discovered. **Decision:
keep the clamp, retire the coverage claim, restate the two comments that describe it.** Do not
add a test seam.

### Reachability analysis (predicts the mutation outcome the developer must confirm)

After the change, `s.replayThrough = min(afterID, newest)` is reached only when `After` returned
`gap == false`. Enumerate every such input:

| Input class | `newest` | `min(afterID, newest)` | Mutant `= afterID` |
|---|---|---|---|
| Unknown conversation, `afterID == 0` | `0` | `0` | `0` — identical |
| Non-empty conv, `afterID == latestID` | `latestID` | `afterID` | identical |
| Non-empty conv, `afterID < latestID` (replay branch) | `latestID > afterID` | `afterID` | identical |

`c != nil ⇒ len(c.events) ≥ 1` (every `Append` appends exactly one event after evicting at most
one, so the slice never empties after creation), so there is no fourth class.

`newest` is read *before* `After`, so `newest ≤ latestID`. For `min` to bind we need
`newest < afterID ≤ latestID`, i.e. an `Append` landing **between the two reads**. Single-goroutine
tests cannot produce that, and there is no seam to inject it — `SetReplaySource` takes a concrete
`*eventring.Ring`, and widening it to an interface is a refactor this ticket does not justify.

**Predicted mutation outcome:** after the change, mutating `min(afterID, newest)` → `afterID`
reddens **nothing**. The developer must run it and record the actual result (below), not assume
this prediction.

### What the clamp still defends

The concurrent-append window, and it is a real one:

1. `newest := ring.NewestID(convID)` reads `5`.
2. The emitter appends event `6` for the conversation.
3. `ring.After(convID, 6)` sees `latestID == 6` → `6 == latestID` → caught-up, `gap == false`.
4. Without the clamp `replayThrough = 6`, and `forwardEnvelope` drops the live frame carrying
   event 6 — exactly one silently muted event. With the clamp, `min(6, 5) = 5` and event 6 flows.

Reachable in production via the `/clear` rotation shape: conv B at newest 5, phone advertises a
stale 6 from the rotated-away conv A, the emitter appends B's event 6 in the window. Narrow, but
not dead code, and the clamp direction is the safe one — it can only *lower* the watermark
(deliver more), never raise it.

### Consequence for the two `_LiveStreamDelivered` tests

`TestV2Session_Reconnect_OutOfRangeLastEventID_LiveStreamDelivered` and
`TestV2Session_Reconnect_ClearRotation_LiveStreamDelivered` keep asserting a true and valuable
property — an out-of-range / hostile `last_event_id` must not mute the live stream — but they no
longer pin the *clamp*, because their inputs now return before it. Keep both tests; rewrite their
doc comments so they say which mechanism now delivers the live stream (the gap branch leaving
`replayThrough` at `0`).

`..._ClearRotation_...`'s comment block needs particular care: it currently argues at length that
conv B **must be non-empty**, because an empty conv B would take the gap branch and "already
deliver live events even with the bug present". After this change the non-empty case takes the gap
branch too, so that paragraph is not merely stale — it now argues for a distinction that no longer
exists. Replace it, don't patch it.

### Where the mutation record goes

AC #4 asks for the outcome in `docs/knowledge/codebase/1494.md`. That file is owned by the
documentation phase, which writes it from this spec plus the merged diff; making it a developer
deliverable pushes a fixed-cost housekeeping write into the implementation turn budget. So:

- The **developer** fills in § Implementation record at the bottom of *this spec file* with the
  verbatim mutation outcome.
- The **documentation phase** lifts it into `docs/knowledge/codebase/1494.md`.

AC #4's substance — "resolved rather than silently dropped" — is satisfied by the record existing
and being explicit. Code-review should read it there, not in the knowledge doc.

Run the mutation without dirtying the worktree, per the house overlay technique:

```bash
# overlay.json maps v2session_replay.go to a copy under the scratchpad with the
# clamp line mutated to `s.replayThrough = afterID`
cd <worktree> && go test -overlay=<abs-path-to>/overlay.json ./internal/relay/ -run 'TestV2Session_Reconnect' -count=1
```

Record: the exact command, which tests reddened (expected: none), and one sentence naming the
surviving justification.

---

## Testing strategy

Scenarios, not code. Write them in the package's existing idiom (table-driven, stdlib `testing`,
no testify). Everything here is provable under `make check`; no `internal/e2e/realclaude` test
touches `last_event_id` or `eventring`.

### `internal/eventring/ring_test.go`

- **`TestAfter_CaughtUp` — retarget.** Drop the `afterID=99` row. Keep only `afterID=5` against a
  1..5 conversation, still expecting `(nil, false)`. Update the comment: caught-up is now
  *exactly at* the latest id, not at-or-beyond.
- **New: beyond-the-id-space is a gap.** Same 1..5 fixture. For `afterID` of `6`, `99`, and
  `math.MaxUint64`, expect `gap == true` and zero events. Reverting `>` to `>=` must redden this —
  that is AC #1's explicit requirement, so pick the message text so a failure reads as a
  classification failure, not a count failure. `ring_test.go` does not currently import `math`.
- **Unchanged, and must stay green:** `TestAfter_GapWhenOldestFellOff` (its `After("A", 6)`
  probe is `afterID == latestID`, still caught-up), `TestAppend_CapOne`, `TestAfter_ReplayReturnsEventsAfterID`,
  `TestAfter_UnknownConversation`, `TestAppend_AllControlHardBound`,
  `TestAfter_MiddleDeltaEvictionNoGap`, `TestNewestID`, `TestRing_ConcurrentAppendAfter`. Do not
  touch them. If any of these reddens, the change is wrong — not the test.

### `internal/relay/v2session_replay_test.go`

- **`TestV2Session_Reconnect_CaughtUp_NoReplay` — narrow to the true caught-up case.** Keep only
  the `at-newest` subcase (`last=5`, `wantEnvs=1`, zero forwarded frames). Remove `beyond-newest`
  and `hostile-max-uint64` from this table and rewrite the doc comment: it currently says "at or
  beyond the newest event", which is exactly the phrasing AC #5 bans.
- **New: `TestV2Session_Reconnect_BeyondNewest_EmitsResync`** — AC #2. Mirror
  `TestV2Session_Reconnect_Gap_EmitsResync`'s structure, with the two subcases that just moved
  (`last=99`, `last=math.MaxUint64`) against a full-cap ring holding ids 1..5. Each subcase:
  `reconnectScenario(..., wantEnvs=2)`; assert exactly one forwarded frame; assert
  `Type == protocol.TypeResync`, `EventID == nil`, and that the payload's `conversation_id` is
  `v2TestConvID`. State in the comment that this previously produced **zero** frames — that
  contrast is the AC.
- **`TestV2Session_Reconnect_OutOfRangeLastEventID_LiveStreamDelivered` — index shift only.**
  `reconnectOpenLive(..., wantHandshake)` goes `1 → 2` (noise_resp + resync);
  `waitForEnvelopes(t, rec, 2)` goes to `3`; the live frame moves from `envs[1]` to `envs[2]`.
  Assertions on the live frame's `EventID` and payload are unchanged. Rewrite the doc comment per
  § AC #4 — it currently explains delivery via the clamp; delivery is now via the untouched
  watermark. Both subcases keep their inputs.
- **`TestV2Session_Reconnect_ClearRotation_LiveStreamDelivered` — index shift only.**
  `reconnectOpenLive(..., 1)` → `2`; `waitForEnvelopes(t, rec, 3)` → `4`; `envs[i+1]` → `envs[i+2]`.
  Replace the "conv-B must be NON-EMPTY" comment block wholesale (§ AC #4).
- **Unchanged, and must stay green:** `TestV2Session_Reconnect_ReplaysMissedTail`,
  `TestV2Session_Reconnect_Gap_EmitsResync`, `TestV2Session_Reconnect_AbsentLastEventID_NoReplay`,
  `TestV2Session_Reconnect_ScopedToCursorConversation` (its cursor→B / ring-holds-A shape is the
  unknown-conversation gap branch, already `(nil, true)`),
  `TestV2Session_Reconnect_ReplayDisabled_NoReplay`, `TestV2Session_Reconnect_OtherConnsUnaffected`,
  `TestV2Session_Reconnect_SameConversation_DedupPreserved` (in-range `afterID=2` → replay branch),
  and the four #777 pacing tests.

### Gates

`make check` covers everything. Run `make cite-guard` before committing — do not write `file:NNN`
citations into any comment this ticket touches; name the symbol instead.

---

## Documentation

AC #5's four named sites, plus three the ticket's list does not name (found by sweeping the
claim's vocabulary — `at or beyond`, `afterID >=`, `caught-up`, `hostile` — across `*.go` and
`docs/`). All seven are **live** contract statements. Frozen build artifacts under
`docs/specs/architecture/` (646, 647, 663) and historical per-ticket records under
`docs/knowledge/codebase/` are **out of scope**: they record what those tickets did, and rewriting
them would falsify the history.

| # | File | Site | Stale claim → what it must say |
|---|---|---|---|
| 1 | `internal/eventring/ring.go` | `After` doc comment, the three-outcome bullet list | "Caught up — afterID >= the latest id assigned" → caught-up is `afterID == latestID`; add the beyond case to the gap bullet, on the same "an id this daemon never issued" reasoning the unknown-conversation sentence already uses. |
| 2 | `internal/eventring/ring.go` | `After`, the inline `// caught up` comment | Must not read as covering the beyond case. |
| 3 | `internal/eventring/ring.go` | `NewestID` doc comment | "so an untrusted remote `last_event_id` beyond this conversation's id space can never set the watermark above a real id" — that class no longer reaches the clamp. Restate as the `NewestID`/`After` read-window defence (§ AC #4). |
| 4 | `internal/relay/v2session_replay.go` | `replayMissed` doc comment | "a hostile-large id classifies as caught-up (zero work)" → classifies as gap → one resync marker, still bounded work. |
| 5 | `internal/relay/v2session_replay.go` | `replayMissed`, the clamp comment above `s.replayThrough = min(...)` | "a stale cross-/clear id or a hostile 2^64-1 would otherwise set the watermark above this conversation's id space" — those inputs now return at the gap branch. Restate per § AC #4. |
| 6 | `docs/protocol-mobile.md` | § "Reconnect replay & resync (consumer, #647)", the **Caught-up** bullet | "at or beyond the newest retained event" → *at* the newest retained event. Add the beyond case to the resync bullet (or give it its own), so the four bullets stay a total partition of the input space and the never-a-silent-gap promise on the following line is honest. |
| 7 | `docs/knowledge/features/eventring-package.md` | § "`After` — the three-way replay contract", the condition table | `afterID >= latestID` → `afterID == latestID`; the Gap row gains the `afterID > latestID` condition. The "Caught-up is checked first" sentence below stays true — keep it accurate about which of the two new branches is checked first. |
| 8 | `docs/knowledge/features/eventring-package.md` | the `NewestID` paragraph above that section | Same correction as site 3 — the "ruling out an untrusted `last_event_id` beyond the conversation's id space silently muting the live stream" claim now belongs to the gap branch, not the clamp. |
| 9 | `docs/knowledge/features/v2-session-manager.md` | § Reconnect replay, the `replayMissed` branch list (**caught-up** / **gap** bullets) | Caught-up bullet must state the `==` boundary; gap bullet gains the beyond case and notes `replayThrough` is left untouched there. |
| 10 | `docs/knowledge/features/v2-session-manager.md` | the `replayThrough` bullet | "the caught-up branch clamps it to `min(afterID, NewestID(convID))` (#663) — a remote `last_event_id` beyond the conversation's id space can never raise it above the newest retained id" → that input class never reaches the clamp now; the watermark simply stays `0`. |
| 11 | `docs/knowledge/features/v2-session-manager.md` | the intro paragraph's `#663` clause | "the caught-up watermark is clamped … so an out-of-range / hostile `last_event_id` cannot suppress the live stream" — one clause, must reflect that #1494 moved that input class to the resync branch. |
| 12 | `docs/knowledge/features/v2-session-manager.md` | the reconnect-replay test-inventory paragraph | Add the new `…_BeyondNewest_EmitsResync` test; adjust the `…_CaughtUp_NoReplay` description; note that the two `…_LiveStreamDelivered` tests now exercise the gap branch. |

Run `qmd update && qmd embed` after the doc edits.

**Not to be touched:** `docs/PROJECT-MEMORY.md`, `docs/lessons.md`, `docs/knowledge/INDEX.md`,
`docs/knowledge/codebase/*.md` (documentation phase owns 1494.md), and
`internal/relay/v2session_handshake.go`'s `handleNoiseInit` replay-hook comment, which already
reads correctly.

---

## Open questions

- **None blocking.** The one judgement call — whether to re-site the #663 clamp's coverage or
  retire the claim — is decided in § AC #4 (retire the claim, keep the code, restate the comments,
  add no test seam). If the developer's mutation run contradicts the prediction and something
  *does* redden, record which test and stop: that would mean the reachability table above is wrong
  and the spec needs revisiting, not the test.

---

## Implementation record

*(Developer fills this in. The documentation phase lifts it into `docs/knowledge/codebase/1494.md`.)*

**AC #4 — #663 clamp mutation re-run.**

- Command:

  ```bash
  # overlay maps internal/relay/v2session_replay.go to a scratchpad copy with
  # `s.replayThrough = min(afterID, newest)` mutated to `s.replayThrough = afterID`
  # (plus `_ = newest` after the NewestID read, or the mutant does not compile —
  # dropping the clamp is the only use of `newest`, and a build failure is not a
  # red test).
  cd <worktree> && go test -overlay=<abs-path>/overlayB.json ./internal/relay/ -count=1
  ```

- Tests reddened: **none.** `ok github.com/pyrycode/pyrycode/internal/relay` — the
  whole package, not just `-run TestV2Session_Reconnect`. This matches the spec's
  reachability prediction exactly.

  The result is a real delta, not a broken mutant. The same mutation was run against
  the **pre-#1494 tree** (an overlay mapping `ring.go`, `ring_test.go`,
  `v2session_replay.go`, `v2session_replay_test.go` to their `HEAD` content, with the
  clamp mutated identically) and reddened precisely the three rows AC #4 named:
  `TestV2Session_Reconnect_ClearRotation_LiveStreamDelivered` and both subcases of
  `TestV2Session_Reconnect_OutOfRangeLastEventID_LiveStreamDelivered`. Post-change,
  all three still pass **and** still assert live delivery — they now reach it through
  the gap branch (`replayThrough` never written, guard inert) instead of the clamp.

- Surviving justification for keeping `min(afterID, newest)`: the concurrent-`Append`
  window between the `NewestID` read and the `After` read. An `Append` landing there
  makes an `afterID` that was out of range at the first read equal to `latestID` — and
  so caught-up — at the second; without the clamp the watermark would be set to that
  `afterID` and `forwardEnvelope` would drop the live frame carrying the very event
  that was just appended. Not reachable single-goroutine and deliberately given no
  test seam (`SetReplaySource` takes a concrete `*eventring.Ring`; widening it to an
  interface is a refactor this ticket does not justify), so **the clamp is now
  defence-only for that race window**. Its direction is the safe one — it can only
  lower the watermark, never raise it. The `min(afterID, newest)` comment in
  `replayMissed` and the `NewestID` doc comment were both rewritten to say this
  instead of the retired "an out-of-range remote id can never raise the watermark"
  claim, which #1494 moved to the gap branch.

**AC #1 — classification-revert mutation (confirming the new gap test is load-bearing).**

- Command: same overlay technique, mapping `internal/eventring/ring.go` to a copy with
  the two new branches collapsed back to the pre-#1494 single
  `if afterID >= latestID { return nil, false }`.
- Reddened: `TestAfter_GapBeyondIDSpace` (all three rows — `6`, `99`,
  `math.MaxUint64`), plus `TestV2Session_Reconnect_BeyondNewest_EmitsResync` (both
  subcases), `…_OutOfRangeLastEventID_LiveStreamDelivered` (both subcases) and
  `…_ClearRotation_LiveStreamDelivered` in `internal/relay`.
- A second variant — flipping only the operator in the shipped two-branch shape
  (`>` → `>=`, leaving the now-unreachable `==` arm) — reddens the *other* side:
  `TestAfter_CaughtUp`, `TestAfter_GapWhenOldestFellOff`'s at-latest probe, and
  `TestV2Session_Reconnect_CaughtUp_NoReplay`. Both boundaries are pinned; neither
  arm is free.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — and the change narrows the boundary.** `last_event_id`
  crosses untrusted→trusted at one explicit point: the `*uint64` decode of
  `HelloClientPayload` inside `handleNoiseInit`, which runs *after* Noise IK authentication and
  the device-token check. Downstream it reaches exactly one function, `replayMissed`, which never
  lets the phone name a conversation (`cursor()` resolves it daemon-side) and bounds the work by
  `MaxEventsPerConversation`. The change *reduces* the untrusted value's reach: today
  `afterID > latestID` flows into `s.replayThrough` via `min(afterID, newest)`; after the change
  that class returns at the gap branch and the untrusted value is never written to per-conn state
  at all. `Ring.After` remains the single classification site.

- **[Network & I/O] SHOULD NOT FIX — amplification is 1:1 with an authenticated handshake.** A
  hostile paired phone can now elicit one `TypeResync` envelope per reconnect by advertising a
  large `last_event_id`, where it previously got zero frames. The marker is a single small control
  envelope carrying one conversation id, emitted once per completed Noise IK handshake on the
  conn that requested it — so the rate is bounded by the existing handshake path, not by anything
  this ticket adds, and there is no fan-out (`emitResync` addresses `s.connID` only). The resync
  instructs the phone to reload a conversation it is already authorised to read and could reload
  unprompted. No cap needed; no new limit is being deferred.

- **[Error messages, logs, telemetry] No findings.** The gap branch reuses `emitResync`, whose
  `Info` log carries `event`, `conn_id`, `conversation_id` — the same fields the pre-existing
  aged-out gap path already logs — and whose failure paths log at `Warn`/`Debug` without echoing
  payload or ciphertext bytes. The change adds no log site and no new field. One consequence worth
  naming: `v2.replay.resync` will fire more often (it is now the post-restart and stale-cursor
  path, not only the aged-out path), which is a volume change on an existing `Info` line, not a
  content change.

- **[Information disclosure / oracle] Finding, accepted.** The change makes `latestID` probeable.
  An authenticated phone can reconnect with varying `last_event_id` and binary-search the boundary
  between resync (`> latestID`) and silence (`== latestID`), learning the conversation's exact
  event count. Accepted: the probing conn is paired and authenticated, `latestID` counts events
  that same conn receives in full on its live stream, and the conversation id the marker names is
  already on every live envelope the conn gets. No privilege gain, no cross-conversation leak —
  `cursor()` is the enforcing symbol and it never consults phone input.

- **[Concurrency] No findings — one pre-existing window, now explicitly the clamp's only
  justification.** The edit is inside `Ring.After`, under the ring's existing `sync.Mutex`; no new
  locks, goroutines, or lock-ordering surface. The `NewestID`-then-`After` read window in
  `replayMissed` is untouched and its staleness direction stays safe (`newest` can only be low,
  which can only lower the watermark and deliver more). § AC #4 documents that this window is now
  the sole reachable case for the `min()` clamp, and keeps the clamp for it.

- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model treats `last_event_id`
  as untrusted remote input on an internet-exposed path; the never-a-silent-gap promise in
  § "Reconnect replay & resync" is the guarantee this ticket restores. Site 6 in § Documentation
  makes the branch table match that promise, closing the doc/promise contradiction the ticket
  filed against.

- **[Tokens/secrets], [File operations], [Subprocess], [Cryptographic primitives] — not
  applicable by design.** This ticket changes one integer comparison, four code comments, and
  three docs. It touches no credential, no filesystem path, no `exec.Command`, and no key, nonce,
  or RNG. The frames it causes to be emitted seal through the unmodified `forwardEnvelope` path
  under the session's existing Noise CipherState; the send-nonce sequence stays single-writer on
  `Run` because `emitResync` was already called from there.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
