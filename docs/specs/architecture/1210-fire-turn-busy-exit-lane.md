# 1210 — Fire the turn-busy exit lane from the runner's child exit

**Ticket:** [#1210](https://github.com/pyrycode/pyrycode/issues/1210) · **Size:** S · **Labels:** `bug`, `security-sensitive`
**Baseline:** every citation below re-verified at `3ef6f81` (= `main` after #1209 / PR #1211 merged).

---

## Files to read first

`codegraph_context` was run first for this ticket and came back off-target: the task string's `Config`
matched the `Config` symbol in six `internal/agentrun/*` packages and returned nothing from `cmd/pyry`
or `internal/streamsup`. The index's `Config` is too overloaded for this query to discriminate. The list
below is therefore from direct reads, each line range re-verified at `3ef6f81`.

| Path | What to extract |
|---|---|
| `cmd/pyry/streamsup_runner.go:70-112` | `newStreamRunnerFactory` — the doc block you extend and the closure the one new line joins. `:105` is the sibling install whose `cfg.SessionID` binding the new line must reuse **verbatim**. |
| `cmd/pyry/stream_turn_drain.go:102-143` | `exitFor` — the producer you install. Its `func()` type, the non-blocking select, and the `Warn` drop diagnostic (`stream_turn.exit_sink_full`) that T2 keys on. `:106` is a correction site; `:110-113` is **not** — see § Comment corrections. |
| `cmd/pyry/stream_turn_drain.go:196-217` | The drain's exit arm — the consumer end of the lane. Shows the clear runs inline on the drain goroutine, ahead of the active-session gate. |
| `cmd/pyry/stream_turn_busy.go:27-54` | The `KNOWN GAP` block (`:31`, `:33` correction sites) and the two paragraphs under it that stay untouched (the UNREACHABLE rotation edge, and SECURITY). |
| `cmd/pyry/stream_turn_busy.go:154-224` | `clearForSession` — its two-caller doc (`:162` correction site), the nil-receiver no-op, and the idempotence AC4's "panics on nothing" leans on. |
| `cmd/pyry/stream_turn_busy.go:233-252` | `setBusy` — the unchanged-membership early return that makes the overlapping-teardown case a no-op. |
| `internal/streamsup/runner.go:110-143` | `Config.OnChildExit` — the full contract (`:113` correction site). The clauses that bear on this slice are quoted in § Design. |
| `internal/streamsup/runner.go:456-489` | `Run`'s loop and the fire site at `:487-489`. This position is the whole of AC2's structural argument. |
| `internal/streamsup/runner.go:548-604` | `spawnAndWait` — `cmd.Stdout = r.cfg.Stdout` (`:551`) and `cmd.Wait()` (`:596`): why every event of the dead child is already pushed by the fire. |
| `internal/streamsup/parser.go:137-165` | `consumeLine` — the exact JSON the fake claude must print to open a turn (`"type":"assistant"`), and that a `result` line is the only thing that yields `TurnEnd`. |
| `internal/streamsup/parser.go:186-207, 228-235` | `emitAssistant` (one event per content block) and `emit` (sink called **synchronously**, no Parser goroutine). |
| `cmd/pyry/relay.go:726-747` | The `:743-744` correction site in context — what stays true (still a local; still no delivery reader) vs. the one clause that goes false. |
| `cmd/pyry/stream_turn_busy_test.go:495-631` | `exitLaneDrain` + the three #1209 exit tests — the background-conversation fixture, the assert-`Busy`-before-`WaitIdle` discipline, and the `waitDropKind` barrier vocabulary the new tests reuse. |
| `cmd/pyry/streamsup_runner_test.go:275-340` | `TestStreamRunnerFactory_Construct` / `_ErrorPropagation` — the established pattern for driving the factory from a test. |
| `cmd/pyry/acp_test.go:148-166` | `fakeClaudeScript` — the shell-script fake-claude precedent, including why a `TestHelperProcess` re-exec is the wrong shape here (the Go test binary rejects the `--session-id <uuid>` flag). |
| `cmd/pyry/acp_test.go:27`, `cmd/pyry/interactive_turn_v2_test.go:45` | `testLogger(w io.Writer)` and `discardLogger()`. |
| `docs/knowledge/codebase/1206.md:59-70` | The `State()`-from-callback NIT that AC4's third clause exists to keep from becoming a MUST FIX. |

---

## Context

The per-conversation turn-busy tracker closes an open turn on two feeds today: `observe` on a parsed
`TurnEnd`, and `clearForSession` on a pool teardown transition (#1202). A child that **crashes mid-turn**
is invisible to both — the resumed child emits no `result` line for the abandoned turn, and the pool entry
is untouched. That is the `KNOWN GAP` at `stream_turn_busy.go:27-35`.

#1209 built the closing half of the lane: the fan-in envelope gained an `exit` discriminant, the drain
gained an arm that calls `clearForSession(env.sessionID)` inline, and the per-runner producer closure
`(*streamTurnSink).exitFor` shipped ready to install. **This slice supplies only the binding** — one
assignment in `newStreamRunnerFactory` — plus the six forward-reference comments that go false the moment
it lands.

After this slice no reachable sequence leaves a conversation reported busy forever: `TurnEnd` closes the
normal turn, the pool transition closes a rotated or evicted one, and the child exit closes a crashed one.

**Ships unwired for delivery.** No delivery path consults the signal; no v2 frame changes.

---

## Scope check

Production `.go` files carrying modified content — excluding tests, `*.md`, and this spec: **5.**

| File | Change |
|---|---|
| `cmd/pyry/streamsup_runner.go` | one assignment + doc extension |
| `cmd/pyry/stream_turn_busy.go` | comment only (two sites) |
| `cmd/pyry/stream_turn_drain.go` | comment only |
| `cmd/pyry/relay.go` | comment only |
| `internal/streamsup/runner.go` | comment only |

**The §4 ≥5-modified-production-files gate trips, at exactly 5.** Recording the count and the ruling
rather than massaging either.

Every one of §1's six red lines passes with margin: 0 new production files (1 new test file); ~230 LOC
total written work (1 production line, ~25 comment lines, ~200 test lines); 0 new exported types; 1 new
call site; exactly 5 acceptance criteria; 0 new reject branches.

I have twice ruled that comment-register files count toward this gate — on #1203 (split → #1206 + #1207)
and #1207 (split → #1209 + #1210). **Both of those splits had a real seam. This one does not.** The only
remaining seam is behaviour-now / comments-later, and #1207's own split ruling rejected exactly that
seam: the behaviour child merges leaving *"nothing in production fires it"* sitting above code that fires
it — the inverted-comment defect this ticket exists to remove. The comments cannot land first either,
since the assignment is what falsifies them. A split here buys two extra pipeline cycles, a comment-only
child, and a window on `main` where five comments are false.

So: committing at 5, flagged rather than routed. Note that PO's *"only one is substantially edited"*
framing in the ticket body answers §1's **new**-files line, a different rule — the same mis-aim ruled on
for #1207. The count that matters is 5 and it is stated as 5.

---

## Design

### The change

One assignment inside `newStreamRunnerFactory`'s returned closure
(`cmd/pyry/streamsup_runner.go:102-111`), placed directly under the existing `scfg.Stdout` install:

```go
scfg.OnChildExit = sink.exitFor(cfg.SessionID)
```

Three properties, none of them incidental:

- **Same `cfg.SessionID` value as `:105`.** The two lanes must carry identical session tags or the drain's
  exit arm clears a conversation other than the one whose events it is ordered behind. Binding both from
  the one `cfg.SessionID` in the same three lines is the AC3 evidence — read as a pair, not derived.
- **Adjacency to `:105` is load-bearing, not cosmetic.** Anywhere before `streamsup.New(scfg)` is
  functionally equivalent (`New` copies the Config), so the placement is chosen for the reader.
- **`exitFor` is installed, not re-derived.** AC4 is satisfied by *which closure is installed*. A
  hand-rolled `func() { ... }` that re-writes the non-blocking send fails AC4 even if it behaves
  identically, because it duplicates the drop-diagnostic contract `exitFor` owns.

No signature changes. `newStreamRunnerFactory` already receives `sink *streamTurnSink` (`:101`) — the very
type `exitFor` hangs off — so nothing needs threading and `cmd/pyry/main.go` is untouched.

### Why AC2 holds structurally, in both directions

No runtime ordering check is added. The guarantee comes from four facts about the runner:

1. `cmd.Stdout = r.cfg.Stdout` (`runner.go:551`) — the Parser is a plain `io.Writer`, so os/exec runs a
   copier goroutine over the child's stdout.
2. `Parser.emit` calls its sink **synchronously** (`parser.go:231-233`) — no internal Parser goroutine and
   no internal buffer, so every mapped event is pushed onto the fan-in from that copier goroutine.
3. `cmd.Wait()` (`runner.go:596`) joins that copier before `spawnAndWait` returns. **By the fire, every
   event child N produced is already pushed** — pushed, not necessarily drained.

   This one needs more than the call site, because `spawnAndWait` sets `cmd.WaitDelay = killGrace`
   (`:568`, 5s) and a bounded wait plausibly returns *without* joining. Verified against the runtime:
   `exec.(*Cmd).awaitGoroutines` still blocks on `<-c.goroutineErr` **after** force-closing the pipes on
   the `WaitDelay` path (`$GOROOT/src/os/exec/exec.go`, the `case <-timer.C:` arm), so `Wait` never
   returns while a copier is live. The join is unconditional. What the timeout path *can* do is discard
   bytes not yet read — that loses events outright rather than reordering them, and a lost opener leaves
   the conversation idle, so the clear degrades to a no-op. Neither outcome inverts the ordering.
4. The fire site (`runner.go:487-489`) sits above the backoff wait and above the next iteration's
   `spawnAndWait`, so child N+1's copier does not exist yet. Note this is **program order, not timing**:
   the send completes before any statement that could reach the next `cmd.Start` runs, on that same `Run`
   goroutine. The immediate-restart path (`drainRestart()` → `continue`, which skips backoff entirely)
   therefore cannot invert it either — the guarantee does not rest on the backoff delay.

The fan-in therefore carries `[child N events] → [exit] → [child N+1 events]`, and the drain is a single
FIFO reader. **Not overtaken** (direction one) and **does not overshoot the respawn** (direction two) both
fall out of that, with no check in either direction.

The spec asserts this as *structural*; the developer must not add a guard, a sequence number, or a
timestamp comparison to "make it safe."

### What the seam's contract already gives us

From `Config.OnChildExit` (`internal/streamsup/runner.go:110-143`) — quoted because each clause maps to an
AC and the developer should not have to re-derive them:

- **Takes no arguments**, so the session id cannot come from the runner. It must be closure-captured — and
  that is not merely convenient. A runner-supplied id would be the rotating internal spawn id
  (`nextSpawnID`, `runner.go:461-470`), which `RestartFresh` rotates while the Parser's sink tag stays
  fixed at runner construction. Capturing `cfg.SessionID` is the *only correct* key. **Widening
  `OnChildExit`'s signature is a wrong turn, not a shortcut.**
- **Must not block** — it runs synchronously on the `Run` goroutine and stalls the restart ladder until it
  returns. `exitFor`'s non-blocking select satisfies this. A direct synchronous `clearForSession` call
  does not (it takes the tracker's lock and broadcasts) and is independently ruled out by AC2's ordering.
- **Must not panic** — there is no `recover` at the fire site, so a panic takes the supervise loop down.
- **Must not consult `Runner.State()`** — at the fire position `State()` still reports `PhaseRunning` with
  a dead PID, and the fire precedes `drainRestart()`. `exitFor` reads nothing from the runner; AC4's third
  clause exists to keep it that way (`docs/knowledge/codebase/1206.md:59-70`).
- **Fires once per completed supervision iteration, not once per live child** — including when the spawn
  failed during setup and no claude process ever launched. So the clear will sometimes fire with no turn
  ever open. That is a no-op through `setBusy`'s unchanged-membership early return
  (`stream_turn_busy.go:237-240`), which is the reason the contract names an idempotent clear as the safe
  consumer.
- **Fires on every exit path** — crash, deliberate restart, shutdown alike.

### Comment corrections

Six comments across five files. They are **load-bearing**: each currently asserts something this
assignment makes false, and four of them tell a future reader that the seam has no consumer.

Three greps find them, each run against the count it predicts (all three verified at `3ef6f81`):

| Grep | Predicted | Sites |
|---|---|---|
| `grep -rn '#1203' --include='*.go' .` | 3 lines / 2 files | `stream_turn_busy.go:31`, `:33`; `relay.go:743` |
| `grep -rn '#1207' --include='*.go' .` | 1 line / 1 file | `internal/streamsup/runner.go:113` |
| `grep -rn '#1210' --include='*.go' .` | 2 lines / 2 files | `stream_turn_drain.go:106`; `stream_turn_busy.go:162` |

The third grep is not optional. Both of its hits were introduced by #1209 *after* this ticket was filed
and are invisible to the other two greps because they name **this ticket's own number**. A developer who
runs only the first two sees them come back clean and ships two inverted comments in the exact files this
slice's next reader lands in first.

`#1203` and `#1207` are **closed split parents** — those four numbers are already stale. Replace them with
`#1210`; do not renumber to another closed ticket. The two `#1210` references keep their number and get
their **claim** corrected: the number is right, the tense is wrong.

What each correction must end up asserting (prose is the developer's; the claim is the contract):

1. **`stream_turn_busy.go:27-35` (the `KNOWN GAP` block).** The gap is **closed**. Restate as three feeds:
   `TurnEnd` via `observe`; pool teardown via `clearForSession` (#1202); child exit via the drain's exit
   arm (#1209 lane, #1210 producer). The *"must land before any consumer reads this signal"* precondition
   is now **satisfied** — restate it as satisfied, do not delete it. A later reader needs to see that the
   never-deliverable-conversation hazard was identified and closed, not that it was never considered.
   The two paragraphs below (`:37-47` UNREACHABLE rotation edge, `:49-54` SECURITY) stay **byte-identical**
   — both are still true.
2. **`stream_turn_busy.go:162`.** *"That lane exists but nothing in production fires it until #1210
   supplies a producer"* → the lane is fired in production; cite `newStreamRunnerFactory`
   (`cmd/pyry/streamsup_runner.go`) as the installer. The surrounding two-caller enumeration stays.
3. **`relay.go:743-744`.** Only the trailing *"Until then #1203 is the last clear that must land"* clause
   is false. The sentence before it — *"The consumer slice that consults it for delivery promotes it to a
   field, with the reader in the same diff"* — is **still true and must survive**: `busy` is still a
   local, and no delivery path reads it. Do not promote it to a `relayWiring` field here; that is #1199.
4. **`stream_turn_drain.go:106-108`.** *"Nothing in production installs it yet — #1210 is the wiring
   slice; this slice's caller is the unit test"* → both halves go false; it is installed by
   `newStreamRunnerFactory`, and production is now a caller.
   **Name the installer, not the seam symbol.** The paragraph immediately below (`:110-113`) explains that
   the seam is referred to by *location* rather than by symbol, and it stays unedited (it is a historical
   rationale, not a claim this slice falsifies). Writing `OnChildExit` into `:106` would leave those two
   paragraphs contradicting each other inside one doc comment. Naming `newStreamRunnerFactory` — a
   different symbol — corrects the claim and keeps the block coherent, at zero cost.
5. **`internal/streamsup/runner.go:113`.** *"nil on every production construction path today — this is the
   unwired seam, #1207 wires the first consumer"* → **inverts to non-nil on every production construction
   path**, not to "nil on the PTY path". `newStreamRunnerFactory` is the sole tree-wide caller of
   `streamsup.New` (verified: one hit), and in PTY mode no `streamsup.Runner` is constructed at all
   (`selectInteractiveRunner`, `main.go:668-673`, returns a nil factory). The field stays documented as
   **Optional** — it is nil-checked at the fire site and left nil by tests that omit it. Describe the
   consumer generically (a turn-busy clear in `cmd/pyry`); `internal/streamsup` must not name `cmd/pyry`
   types, per its own package-doc import discipline.

**One comment that looks stale and is not:** `stream_turn_drain.go:110-113`. Historical rationale for
#1209's own unfiredness gate. Leave it alone. Relatedly — naming `OnChildExit` in production code does not
break *this* slice's gate, which is the `.Busy(` / `.WaitIdle(` grep (AC5): the exit path reaches
`clearForSession` through the drain, not through either method, so that grep stays at 0.

The `#1203` references under `docs/knowledge/` (`features/streamsup-package.md`, `INDEX.md`,
`codebase/{1201,1202,1206,1209}.md`) are **documentation-phase territory — do not touch them.**

---

## Concurrency model

No new goroutine, no new lock, no change to any lock order.

| Goroutine | Role in this slice |
|---|---|
| runner `Run` | Calls the installed `exitFor` closure synchronously at `runner.go:487-489`, between `spawnAndWait` returning and the shutdown/backoff branch. One non-blocking channel send; no lock taken; returns immediately. |
| os/exec stdout copier (per child) | Writes into the Parser, whose `emit` pushes onto the same fan-in synchronously. Joined by `cmd.Wait` **before** the exit fire — the ordering premise. |
| drain (`startStreamTurnDrainV2`) | Sole reader. On an `exit` envelope calls `clearForSession` inline, before `observe`, before the active-session gate, before `Handle`. |

The exit send and the event sends target the same buffered channel, so the runtime's channel FIFO is the
only ordering mechanism needed. `clearForSession` and `observe` are serialised **by construction** on the
drain (one envelope at a time), not by `t.mu`; the tracker's own mutex covers the concurrent teardown
caller.

**Overlapping teardown is benign and needs no coordination.** A `/clear` rotation or an eviction tears the
child down *and* fires a pool transition, so both this clear and #1202's can run for one event. `setBusy`
returns early on unchanged membership — no mutation, no broadcast — so the second clear neither double-wakes
`WaitIdle` waiters nor mutates the map. Same for the spawn-setup-failure fire, where no turn was ever open.

---

## Error handling

| Failure mode | Behaviour | Where |
|---|---|---|
| Fan-in channel full at exit time | Drop the newest, log `Warn` `stream_turn.exit_sink_full` with `session_id` only. Degraded: that conversation stays busy until its next `TurnEnd` or teardown. | `exitFor`, `stream_turn_drain.go:129-143` (shipped by #1209; not re-litigated here) |
| Session resolves to no conversation | `clearForSession` Debug-skips and returns. Reachable on the spawn-setup-failure fire before any binding, and for an evicted id. Fail-closed: a wildcard clear would report a live turn on another conversation idle. | `stream_turn_busy.go:206-221` |
| Tracker is nil (PTY mode, or a drain built without one) | `clearForSession` is a nil-receiver no-op. | `stream_turn_busy.go:201-203` |
| Callback panics | Would take the supervise loop down — there is no `recover` at the fire site. Not reachable: `exitFor` is a channel send plus a `slog` call on a logger `newStreamTurnSink` guarantees non-nil (`:71-73`). | — |
| Callback blocks | Would stall the restart ladder. Not reachable: non-blocking `select` with a `default`. | — |

No new error value, no new sentinel, no error returned anywhere on this path — `OnChildExit` is `func()`
and stays so.

---

## Testing strategy

Tier: **`cmd/pyry`**, driving the real factory, a real `*streamsup.Runner`, a real sink, a real drain and a
real tracker against a fake-claude shell script. No live claude is required — the crash path is fully
reachable with a fake child — so this is **not** `needs-real-claude`.

### Shared fake-claude fixture

A shell script (precedent: `fakeClaudeScript`, `acp_test.go:148-166` — a shell wrapper, not a
`TestHelperProcess` re-exec, because the Go test binary rejects the `--session-id <uuid>` the pool appends):

- **First spawn:** print one `{"type":"assistant","message":{"id":…,"content":[{"type":"text","text":…}]}}`
  line, then exit 0.
- **Every later spawn:** print nothing and `exec sleep 3600`.
- A marker file in `t.TempDir()` selects the branch; argv is ignored.

The two-branch shape is not incidental. The runner respawns after the exit, and a second assistant line
would **re-open** the turn, racing every assertion against the backoff ladder. One turn, one exit, then
silence.

### T1 — end-to-end clear through the production wiring (AC1, AC2-positive, AC3)

- Real `newStreamTurnSink(0, discardLogger())`; real tracker over
  `stubBusyResolve(map[string]string{"sess-a": testConvID})`; real emitter; real
  `startStreamTurnDrainV2` with a `dropWatcher` logger.
- **`activeSession` returns `"sess-b"`, not `"sess-a"`.** Conversation A — the one under test — is a
  **background** conversation, which is both the common crash case and the fixture that makes the exit
  arm's placement discriminating: a clear delivered after the active-session gate could not produce the
  observed result. Same reasoning as `exitLaneDrain` (`stream_turn_busy_test.go:495-505`); reuse it.
- Build the factory over that sink, invoke it with `supervisor.Config{ClaudeBin: <script>, WorkDir:
  t.TempDir(), SessionID: "sess-a"}`, run the returned runner's `Run(ctx)` on a goroutine.
- Barrier for the opener: `waitDropKind(t, drops, "text_chunk")` — the drain's not-active drop, logged
  after `observe` on the same goroutine. **Do not poll `Busy` in a loop.**
- Then assert `busy.Busy(testConvID)` is **true**. Not decoration: `WaitIdle` returns nil immediately on an
  already-idle conversation, so without this the test passes with a completely dead exit lane (the #1209
  trap, recorded at `stream_turn_busy_test.go:540-546`).
- Assert `busy.WaitIdle(deadlineCtx, testConvID)` returns nil, and `Busy` is false after.
- Cancel ctx and join `Run` in cleanup (cancel-then-join; joining first deadlocks).
- **AC1's two silent channels are asserted by construction**, and the test must say so: no `result` line is
  ever printed (so no `TurnEnd` is parseable — `parser.go:152-157`), and no transition observer or
  `sessions.Pool` is constructed anywhere in the test. The observed clear can only have come from the exit
  lane.
- **AC3's discriminant is the resolve map.** It maps only `"sess-a"`. A clear keyed on anything other than
  the construction-time session id — a rotated internal spawn id, say — resolves to nothing,
  `clearForSession` returns early, and `WaitIdle` deadlines. The key is asserted, not assumed.

### T2 — the installed callback is `exitFor` (AC4)

The factory returns `sessions.Runner`, an interface over an unexported `*streamsup.Runner`, so the
installed callback **cannot be read back**. The discriminant has to be behavioural, and `exitFor` has one
no hand-rolled equivalent would reproduce: its drop branch logs `Warn` with `event=stream_turn.exit_sink_full`
and `session_id` — and nothing else.

- `newStreamTurnSink(1, testLogger(&buf))`; start **no** drain, so nothing consumes the channel.
- Run the same fake claude through the same factory. The one assistant line takes the single slot
  (`Parser.emit` is synchronous, so this is ordered before the exit, deterministically); the exit finds the
  channel full and takes the drop branch.
- Assert the captured record: level `Warn`, `event=stream_turn.exit_sink_full`, `session_id` equal to the
  runner's `cfg.SessionID`, and **no content-bearing field**.
- This also covers AC4's *"blocks on nothing"*: a blocking send would wedge `Run` on the full channel and
  the test would hit its deadline instead.
- AC4's *"reads no `Runner.State()`"* and *"panics on nothing"* are code-shape criteria verified at review,
  not by a test — the whole point of AC4 is that installing `exitFor` satisfies them by construction.

### AC5 — gates, not new test functions

- Run each of the three greps **before** editing, against its predicted count (3/2 files, 1/1, 2/2). Then
  after: `#1203` and `#1207` should be **gone** from `*.go`; the two `#1210` mentions remain, with corrected
  claims.
- `grep -rn '\.Busy(\|\.WaitIdle(' cmd/pyry/ internal/ --include='*.go' | grep -v '_test.go'` → still **0**.
  (`WaitIdle` in the new test file is fine — the gate excludes tests.)
- `TestTurnBusyTracker_ImportsStayMinimal` (`stream_turn_busy_test.go:417`) stays green: this slice adds no
  import to `stream_turn_busy.go`, whose edits are comment-only.
- The five `internal/e2e/relay_v2_stream_*_test.go` files (interrupt, modal, new_session, queue_drain, send)
  pass unchanged.
- **Run the `e2e` target explicitly.** `check: vet test staticcheck substrate-guard e2e` (`Makefile:41`)
  aborts silently at the first failing target and skips its suffix, so a green `make check` is not evidence
  that `e2e` ran.
- `go test -race ./...` throughout; a `-v` run on `cmd/pyry` to confirm the new tests actually ran rather
  than skipped (a package-level `ok … 0.9s` hides skips).

---

## Open questions

None blocking.

- Whether the exit-drop deserves a counter/metric rather than a `Warn` was settled by #1209 (Warn, so it is
  visible at the daemon's default `LevelInfo`). Not reopened here.
- Promoting `busy` from a `relay.go` local to a `relayWiring` field belongs to **#1199**, the delivery
  consumer, together with the reader — as the `relay.go:738-743` comment already says. Explicitly out of
  scope; leave the local alone.

---

## Security review

Ticket is `security-sensitive`. The mandated `agents/architect/security-review.md` is **not present in
this worktree** (same gap spec #487 recorded); this pass walks the standard adversarial categories inline,
matching the format the sibling #1209 spec used.

**Mindset: assume the child, the respawn timing, and the wiring are adversarial, and find the path to a
spurious clear.** A spurious clear is the dangerous direction — under #1199 it would release a mid-turn
send into a *running* conversation. A missed clear only withholds delivery.

**Verdict:** PASS

**Findings:**

- **[The exploitable direction] No findings. Route (c), which #1209 explicitly deferred to this slice, is
  closed — and closed more strongly than that review anticipated.** #1209's review walked three routes to a
  spurious clear, foreclosed (a) wrong session id and (b) a clear overtaking a later opener, and assigned
  (c) — *"an exit arriving late relative to the respawned child's events"* — to this ticket, since it
  depends on the fire site's position in `Run`. Walked here: the fire (`:487-489`) is a **synchronous**
  channel send that completes before any statement capable of reaching the next iteration's `cmd.Start`
  (`:581`) executes, on the same `Run` goroutine. That is program order, not a timing margin, so the
  backoff-skipping immediate-restart path (`drainRestart()` → `continue`) cannot invert it either. #1209's
  own framing — and the ticket body's — leans on *"the fire sits above the backoff wait"*, which reads as
  a timing argument and would be a weaker guarantee; the program-order reading is the correct one and is
  what § Design now states.

- **[The exploitable direction] No findings on the reverse ordering, and the one plausible refutation was
  chased down.** Direction one (the clear must not be overtaken by events the dead child already pushed)
  rests on `cmd.Wait` joining the stdout copier. That is not free: `spawnAndWait` sets
  `cmd.WaitDelay = killGrace` (`:568`), and the whole point of a bounded wait is to stop waiting — which
  would let a late event from child N land *behind* the exit and wedge the conversation with a turn that
  can never end. Verified against the runtime rather than assumed: `exec.(*Cmd).awaitGoroutines` executes
  `_ = <-c.goroutineErr` **after** `closeDescriptors` on the `case <-timer.C:` arm, so `Wait` blocks for
  the copier's return on the timeout path too. The join is unconditional. The timeout path discards
  *unread* bytes, which loses events rather than reordering them; a lost opener leaves the conversation
  idle and the clear becomes a no-op. This matters concretely here because the code at `:557-567` documents
  claude leaving detached grandchildren that can hold the inherited stdout write end open — i.e. the
  `WaitDelay` path is a real path on this runner, not a theoretical one.

- **[Trust boundaries] No findings — the seam's signature is the enforcement.** `OnChildExit` is `func()`.
  Structurally, nothing the child emits — on its stdout, on the v2 wire, or in its exit status — can name
  or influence the conversation being cleared, because the callback receives no value at all. The only key
  is `cfg.SessionID`, closure-captured at factory time from the pool's own id, and resolved
  session→conversation daemon-side by the injected closure. `stream_turn_busy.go`'s SECURITY note (*"the
  key is never taken from the wire"*) holds for this feed. **Widening `OnChildExit`'s signature is
  therefore the security regression to watch for**, which is why § Design and the ticket's Not-in-scope
  list both forbid it; AC3 is the guard.

- **[Trust boundaries] No findings on the rotation edge, re-derived rather than inherited.**
  `RestartFresh` rotates the runner's *internal* spawn id while `cfg.SessionID` — and so both `sinkFor`'s
  and `exitFor`'s tag — stays fixed at construction (`stream_turn_busy.go:37-44`). A post-rotation exit
  therefore clears under the original id. That still resolves to the correct conversation because
  `conversationForSession` matches through `SessionHistory`, and #1202 established that every id reachable
  that way belongs to the same runner. No cross-conversation clear. This is also the positive reason the
  key must be closure-captured: a runner-supplied id would be the rotating one.

- **[Concurrency] No findings. One new producer goroutine, no new lock, no new lock edge.** The `Run`
  goroutine now pushes onto a channel it never previously touched, joining that runner's per-child stdout
  copier as a second producer; with N runners the daemon-singleton sink has 2N producers and one reader.
  All are plain buffered-channel sends, safe for concurrent use. `exitFor` takes no mutex and reads only
  `s.ch` and `s.logger`, both write-once at construction. The consumer end is #1209's, unchanged:
  `clearForSession` resolves outside `t.mu`, so no conversations-registry-inside-tracker nesting is
  introduced.

- **[Concurrency] No findings on blocking or on killing the supervise loop.** AC4's three clauses are
  satisfied by *what is installed*, not by new code: the non-blocking `select` with a `default` cannot
  stall the restart ladder; there is no panic path (a channel send plus a `slog` call on a logger
  `newStreamTurnSink` guarantees non-nil at `:71-73`, and production passes a real one at `main.go:672`);
  and `exitFor` reads nothing from the runner, which keeps the `State()`-at-the-fire-site hazard
  (`docs/knowledge/codebase/1206.md:59-70`) a documentation NIT rather than the MUST FIX it becomes for a
  callback that reads `State()`. A hand-rolled callback is rejected for exactly this reason: it would have
  to re-establish all three properties by hand.

- **[Resource exhaustion / DoS] No findings; #1209's rate bound is now live and still holds.** #1209
  derived the bound and noted it belonged to the wiring slice in practice. Confirmed live: only `Run`
  invokes the callback, once per supervision iteration, and a child-driven crash loop is rate-limited by
  the backoff ladder (500ms initial → 30s max). A hostile child cannot reach the deliberate-restart path
  that skips backoff — `Restart` is daemon-driven. Worst case is one channel send and at most one `Warn`
  line per ≥500ms per runner: neither a log-flood nor a channel-starvation vector (an exit that cannot fit
  is itself the thing dropped, never a blocking producer).

- **[Error messages, logs, telemetry] No findings. This slice adds no log statement; it changes
  reachability, not content.** Every diagnostic on the path is #1209's, and the comment corrections emit
  nothing. What changes is that `stream_turn.exit_sink_full` (`Warn`, `event` + `session_id`, no `kind`, no
  conversation id, no event content) becomes **reachable in production for the first time**, putting a
  session id in the default-`LevelInfo` log on this path. #1209's review called that consistent with
  established practice; that claim was re-checked here rather than inherited, and all four cited sites
  still log `session_id` at `Warn` at `3ef6f81` — `internal/sessions/transition.go:84` and `:129`,
  `internal/sessions/pool.go:690`, `internal/relay/v2session_settings.go:111`. Holds.

- **[Residual risk, named not dismissed] A dropped exit re-opens the wedge this ticket closes.** If the
  fan-in is full (256 queued envelopes, i.e. a stalled drain) the exit is dropped and the conversation
  stays busy until its next `TurnEnd` or teardown. That is the pre-existing failure mode, so this slice
  strictly narrows it rather than widening it, and the `Warn` is visible at default level precisely so the
  degradation is not silent. Not re-litigated — it is #1209's shipped design decision.

- **[Approval-gate invariant] No findings, checked because this slice edits the function that owns it.**
  `newStreamRunnerFactory` is the sole stream-path spawn-construction site and the only tree-wide caller of
  `streamsup.New`, so `withApprovalArgs` (`:104`) covers every stream spawn including per-conversation
  runners. The new assignment goes *after* `:104`/`:105` and before `New`, touches neither `scfg.Args` nor
  the yolo branch, and adds no spawn argument. The invariant is unaffected.

- **[Input validation]** Not applicable: the callback takes no input, and the slice parses nothing.

- **[File operations]** Not applicable: no path is constructed, opened, stat'd or written. The four
  comment-only files gain no import — `TestTurnBusyTracker_ImportsStayMinimal` mechanically enforces this
  for `stream_turn_busy.go`.

- **[Subprocess / external command execution]** Not applicable beyond the approval-gate check above: no
  new `exec.Command`, no argument construction, no environment handling. The fake-claude script is test-only
  and lives in `t.TempDir()`.

- **[Tokens, secrets, credentials]** Not applicable: no credential, no token, no persisted state, no new
  in-memory state of any kind.

- **[Cryptographic primitives]** Not applicable: no randomness, no secret comparison, no hashing.

- **[Threat model alignment] No findings.** The existence-oracle posture (`stream_turn_busy.go:20-25`,
  #1101) survives: the exit lane only ever *removes* a key, so it cannot widen `Busy` into a "does
  conversation X exist" oracle. `Busy`'s signature is untouched, and AC5's `.Busy(` / `.WaitIdle(` grep
  keeps it unread by any delivery path — verified at **0** at `3ef6f81` and required to stay 0.

**Reviewer:** architect (self-review; `agents/architect/security-review.md` absent, categories walked inline)
**Date:** 2026-07-25
