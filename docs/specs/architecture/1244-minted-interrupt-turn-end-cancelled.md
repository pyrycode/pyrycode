# #1244 — Assert `turn_end{cancelled}` in the minted per-conversation PTY interrupt oracle

**Size:** S (confirmed, not overridden). 1 production file (`internal/e2e/internal/fakeclaude/main.go`),
3 test files, 0 new files, 0 new exported symbols, ~110 LOC of written work including comments.
Edit fan-out: the renamed const has 2 code references and 2 comment references — far under the
10-call-site red line.

## Files to read first

| Path | What to extract |
|---|---|
| `internal/e2e/relay_v2_perconv_interrupt_test.go` (whole file, 519 lines) | The oracle being extended. `endTurnNeedle:41-47`, the stale StopReason paragraph `:91-97`, the AC1 turn_end wait `:416-443`, the Phase-5 transcript pair `:445-473`. |
| `internal/e2e/internal/fakeclaude/main.go:495-502` | `interruptEndTurnLine` — the const to replace, and its doc-comment's claim about "exactly what turnbridge's mapper requires". |
| `internal/e2e/internal/fakeclaude/main.go:725-734` | The ESC handler's own path: `escPending.Swap(false)` → `appendTurnEnd(f)` under the one-shot `escEnded` gate, on the main goroutine. **This is the injection path AC1 requires; it is already correct — do not touch the kicker's trigger path.** |
| `internal/e2e/internal/fakeclaude/main.go:1037-1049` | `appendTurnEnd` — the writer. Function name and body shape stay; only the const it writes changes. |
| `internal/e2e/internal/fakeclaude/main.go:94-104` | The `PYRY_FAKE_CLAUDE_ESC_ENDS_TURN` env-var doc block, which names the old shape ("assistant end_turn line") and goes stale. |
| `internal/agentrun/jsonl/testdata/no_end_turn.jsonl:53` | **The base line.** claude 2.1.128's recorded interruption entry — the only one in the repo. 12 top-level keys, no `permissionMode`. Read the whole line; the key set is the point. |
| `internal/turnbridge/mapper.go:79-101` | `mapEntry`'s `case "user"`: the `ParseToolResult` branch precedes and returns, then `isInterruptMarker` → `TurnEnd{Cancelled}` at `:95`. The mutation site for AC5. |
| `internal/turnbridge/mapper.go:103-166` | `interruptMarkerSentinel`, `isInterruptMarker`, `userAuthored` (`_, ok := e.Raw["permissionMode"]`), `userText`. The discriminator the staged line must satisfy. |
| `internal/turnbridge/outbound.go:87-92` | `TurnEnd` → `protocol.TurnEndPayload{StopReason: string(e.Reason)}` — why `TurnEndReasonCancelled` surfaces as the literal `"cancelled"` on the wire. No mapping table in between. |
| `cmd/pyry/interactive_turn_v2.go:208-217` | **`case turnevent.TurnEnd: if !e.inTurn { drop }`.** The new reachability dependency — see § The coupling this change introduces. |
| `cmd/pyry/interactive_turn_v2.go:263-306` | `startTurnIfNeeded` / `endTurn` — the only two sites that open and close a turn. Read them to confirm nothing closes a turn on inactivity. |
| `internal/e2e/relay_v2_interrupt_test.go:55-61` | The #794 sibling's StopReason paragraph — the false-statement-to-be that AC4 covers. Its assertion block at `:280-321` asserts presence + ConversationID only. |
| `internal/e2e/relay_v2_perconv_turn_end_test.go:52-60` | A third comment reference to `interruptEndTurnLine` by name; it does **not** set the ESC knob. |
| `docs/knowledge/codebase/1243.md` § "Fixture provenance has exactly two arms" | The verbatim-vs-derived labelling discipline this spec reuses for the fake's const. |
| `docs/specs/architecture/1191-minted-perconv-interrupt-oracle.md:590-609` | S-2, the finding this ticket discharges. Quote-worthy for the re-derivation's framing. |

Not needed, listed so you don't go looking: `cmd/substrate-guard/main.go` — the banned-pattern list
(`cmd/substrate-guard/main.go:56-73`) holds screen literals and CSI bytes only. `[Request interrupted
by user` is transcript prose, not substrate; production already carries the identical string at
`internal/turnbridge/mapper.go:108` outside the allowlist and the gate is green.

## Context

`TestRelayV2_PerConversationInterruptStopsRunningTurn` deliberately does not assert the stop reason,
and says why at `:91-97`: `EventKindJsonlEndOfTurn` was the mapper's only `turn_end` source, so a
tui-driver-hosted turn could not report an interrupt-stop distinctly. #1243 (merged 2026-07-30,
PR #1246) added the second source at `mapper.go:95`. That paragraph is now false.

The assertion cannot simply be added, because the fake stages an interrupt that **looks like a clean
completion on disk**: `interruptEndTurnLine` is an assistant entry carrying `stop_reason:"end_turn"`,
so it drives `mapper.go:30` and reports `end_turn`. Asserting `cancelled` against it would be red for
the wrong reason. The fake must stage the entry claude actually records.

What this buys over #1243's unit table: #1243 needed a bespoke fixture builder (`entryFromLine`)
precisely because the ordinary `entry()` helper leaves `Raw` nil, and the authorship gate reads
`e.Raw["permissionMode"]`. `Raw` population is tui-driver's `TailJSONL` contract, not a struct
invariant. This oracle is the first thing to run the marker through the real tailer
(`tuidriver.parseEntry`, `jsonl.go:436`, which sets `Raw` from the full parsed line), the real
producer, and the real emitter, out onto the wire. That integration span is the coverage.

## Design

### 1. The fake stages claude's recorded interruption entry

Replace the const at `internal/e2e/internal/fakeclaude/main.go:502`. Keep `appendTurnEnd`'s name,
signature, body shape, call site, and the one-shot `escEnded` gate — the *effect* (this write ends
the turn) is unchanged; only the bytes change.

```go
// interruptMarkerLine is the claude-format JSONL line appendTurnEnd writes …
// DERIVED from internal/agentrun/jsonl/testdata/no_end_turn.jsonl:53 (claude 2.1.128).
const interruptMarkerLine = `{…}` + "\n"
```

**Provenance discipline, inherited from #1243** (`docs/knowledge/codebase/1243.md` § "Fixture
provenance has exactly two arms, no third"): this const is **arm (b), derived** — a named base line
with a named substituted span — and its doc comment must say so in those words. It must never be
presented as captured.

- **Preserved verbatim from the base:** the full top-level key set and its order, and the values of
  `isSidechain`, `type`, `message` (`{"role":"user","content":[{"type":"text","text":"[Request
  interrupted by user]"}]}`), `userType`, `entrypoint`, `version`.
- **Substituted span, named in the comment:** the session-identity values — `parentUuid`, `promptId`,
  `uuid`, `sessionId`, `timestamp`, `cwd`, `gitBranch` — replaced with canned values that preserve
  each field's *shape* (a UUID-shaped id stays UUID-shaped, an RFC3339 timestamp stays RFC3339, an
  absolute path stays an absolute path). Nothing on this path reads any of them; the base line's real
  values name a developer worktree and a real session, and carrying those into a fake would be
  meaningless noise.
- **`permissionMode` is absent, and that absence is load-bearing.** `isInterruptMarker` requires
  `!userAuthored(e)`, and `userAuthored` is the *presence* of that top-level key
  (`mapper.go:163-166`). One extra key and the line reads as user-authored, is held out, produces no
  `turn_end` at all, and the run dies 15 s later at the pre-existing "never received a turn_end"
  fatal with nothing pointing at the one extra key.

**Why the full key set and not a minimal `{"type":"user","message":{…}}`.** The minimal form passes
the discriminator, and that is exactly the problem. `Raw` is built by `parseEntry` from the whole
line; a 2-key line makes `permissionMode`'s absence an absence among 2 keys, when production sees an
absence among 12. #1243 built `entryFromLine` to escape precisely that weakness in `entry()`. A
minimal line would re-introduce it at the integration tier and quietly shrink the span this ticket
exists to cover.

**Injection path (AC1).** `appendTurnEnd` is already reached only from `main.go:731` — the ESC
handler's own arm, on the main goroutine, gated on `escPending` and the one-shot `escEnded`. The
mid-turn kicker's trigger path (`JSONL_TRIGGER_DIR` → `emitStructuredJSONLIfTriggered`) is a
different function and is not touched by this ticket. AC1's "never the mid-turn kicker's trigger
path" is satisfied by construction; do not add a staging hook to the trigger path.

**Stale name references.** Grep `interruptEndTurnLine` before finishing. Three sites carry it by
name: the const itself, the env-var doc block at `main.go:94-104` (which also describes the old
"assistant end_turn line" shape), and a comment at `internal/e2e/relay_v2_perconv_turn_end_test.go:57`
("deliberately NOT the fake's own `interruptEndTurnLine`"). That last sentence stays true under the
rename — only the identifier changes.

### 2. The oracle asserts the reason

In `internal/e2e/relay_v2_perconv_interrupt_test.go`:

- **Delete `:91-97`.** Removed, not amended around — the limitation it describes no longer exists.
- **Assert `end.StopReason == "cancelled"`** alongside the existing `end.ConversationID` check
  (`:438-442`). Compare against the literal string, not `turnevent.TurnEndReasonCancelled`: this is
  the wire assertion, `protocol.TurnEndPayload.StopReason` is a plain `string` by decision
  (`docs/knowledge/features/protocol-package.md:740`), and the e2e tier asserts wire bytes. The
  failure message must name what a wrong value means: `end_turn` here says the marker reached the
  wire path but `mapper.go:95` did not classify it — a live regression of #1243, not a staging fault.
- **Re-point `endTurnNeedle`** (`:41-47`) at `[Request interrupted by user` and rename it to match
  (e.g. `interruptMarkerNeedle`). Update its doc comment's `main.go:502` citation.

**Why that needle and not something structural.** It is the exact string `mapper.go:108` keys on, so
needle-drift and mapper-drift cannot diverge: if the fake's line ever stops carrying it, the mapper
stops matching, no `turn_end` arrives, and the run dies loudly *in the same run* — the absence check
at `:470` can no longer go silently vacuous, because the condition that would empty it is itself a
hard red. It is also strictly more robust than the old `"stop_reason":"end_turn"` needle, which lives
in JSON *key* position and would miss on a whitespace change while `IsEndTurn` still held; this one
lives inside a string value and appears verbatim in the bytes regardless of formatting.

Yes, this needles claude's prose. That is deliberate and honest here: the string is not decoration,
it is the discriminator production depends on, and the sibling `relay_v2_perconv_turn_end_test.go`'s
"never claude's rendered words" is that test's own substrate-cleanliness discipline about *screen*
text, not a repo-wide gate. The substrate-guard's banned list contains no prose of this kind.

### 3. The re-derivation (AC3)

Replace the deleted `:91-97` with a paragraph that derives the invariant over a closed, countable
set. Required content — the developer writes the prose, in the file's voice:

- `turnevent.TurnEnd{` is constructed at exactly **three** sites in the repo. Two are reachable from
  a PTY session: `internal/turnbridge/mapper.go:30` (`EventKindJsonlEndOfTurn`) and `:95` (the
  interruption marker). `internal/streamsup/parser.go:267` is the stream-json path, which a PTY
  session never enters.
- **`mapper.go:30` is now unreachable in this test.** `tuidriver.IsEndTurn` requires assistant +
  `stop_reason=="end_turn"` + non-empty text. The minted transcript's line set is closed and
  countable: `{}` from `openSession`, `{}` per delivered turn from `appendTurnGrowth`, the kicker's
  `perConvMidTurnLine` (assistant text, **no** `stop_reason`), and the marker (`type:"user"`). None
  satisfies `IsEndTurn`.
- **`mapper.go:95` is reachable only from the ESC handler's line.** It needs `type:"user"`, no
  top-level `permissionMode`, and text prefixed `[Request interrupted by user`. Of the four line
  kinds above, only the marker is a `user` entry at all — the fake never writes the delivered prompt
  into the transcript.
- Therefore the invariant *holds in the same shape, by the mirror argument*: the design has exactly
  one reachable `turn_end` source, and it is the ESC handler's own write. `turn_end` for the minted
  conversation ⟺ the minted child's ESC handler fired.

This discharges S-2 of `docs/specs/architecture/1191-minted-perconv-interrupt-oracle.md:590-609`,
which flagged that #1229's descendants inherit this file and move the ground the causality claim
stands on. Cite it.

### 4. The coupling this change introduces — must be in the file

`cmd/pyry/interactive_turn_v2.go:208-213` drops a `TurnEnd` when `!e.inTurn`, with a debug log
(`interactive_turn.turn_end_no_turn`) and nothing on the wire.

Today the fake's canned line produces **two** events — `TextChunk("[interrupted]")` and `TurnEnd` —
so it is self-sufficient: its own `TextChunk` runs `startTurnIfNeeded` and opens a turn, and the
`TurnEnd` then lands regardless of what came before. The marker line produces **one** event. It
cannot open its own turn.

So Phase 3's kicker stops being merely a vacuous-pass guard ("the turn was genuinely running") and
becomes a **precondition for the new assertion to be reachable at all**. The dependency holds today:
`endTurn()` has exactly two call sites (`:159`, the follow-active conversation switch — impossible
here, one conversation, cursor stamped once; and `:217`, the `TurnEnd` arm itself), and nothing
closes a turn on inactivity, so `inTurn` stays true from the kicker's first `TextChunk` until the
interrupt. State the dependency and its two-call-site basis in the Phase 3 comment. It is the kind of
coupling that is invisible from the fake's side, which is how #1191's S-2 arose in the first place.

**Consequence for diagnosability.** With the marker as the only source, the AC1 fatal at `:422-425`
("the ESC never reached the minted child") is no longer the only reading of a timeout — a dropped
`TurnEnd`, or a marker held out by the authorship gate, produce the identical symptom. Widen that
message to name the discriminator: the minted transcript at `<sessionsDir>/<mintedID>.jsonl` either
contains the marker (the ESC arrived; the failure is downstream of the fake — check the daemon log
for `interactive_turn.turn_end_no_turn`) or does not (the ESC never arrived). Phase 5 would answer
this, but it sits after the fatal and never runs.

### 5. The shared knob's other consumer (AC4)

`PYRY_FAKE_CLAUDE_ESC_ENDS_TURN` is set by exactly two tests: this one and
`TestRelayV2_InterruptStopsRunningTurn` (`relay_v2_interrupt_test.go:126`), the #794 bootstrap
capstone. (`relay_v2_perconv_turn_end_test.go` names the const in a comment but does not set the
knob.)

**Decision: change the shared line; do not add a second knob.**

- The current canned line is *wrong about claude*. Real claude does not write an assistant `end_turn`
  entry when a turn is interrupted — it writes the marker. Correcting the fake improves fidelity for
  every consumer; a second knob would leave the fake with two mutually-exclusive "what an interrupt
  looks like" modes, one of them known-false, with no consumer wanting the false arm.
- Under a shared change the sibling **stays green**: it asserts a `turn_end` arrives and carries the
  right `ConversationID` (`:280-321`), and never reads `StopReason`. The marker produces exactly one
  `turn_end` where the old line produced one. Its turn is opened by its own mid-turn kicker
  (`:212`), identical in shape to this test's, so the `inTurn` dependency in § 4 is satisfied there
  the same way.
- It does **not** stay truthful: `:55-61` states as fact that the turn_end carries
  `StopReason=="end_turn"`, not `"cancelled"`, attributing it to a tui-driver limitation. Rewrite
  that paragraph: the reason is now `cancelled` (via `mapper.go:95`, #1243), and the test still
  deliberately does not assert it because it proves *causality*, not stop-reason semantics — that
  assertion is #1244's, one tier up, on the minted path. Leaving it would be exactly the staleness
  this ticket exists to delete from its own file, in a file nothing would flag.
- The note at `:283-285` ("StopReason is NOT asserted (see the doc comment)") stays accurate as a
  statement of what the test does; only the paragraph it points at needs the rewrite.

**Out of the developer's scope:** `docs/knowledge/features/fakeclaude-binary.md:381` and `:393`
document the old line and repeat the `end_turn`-not-`cancelled` claim. Those are the documentation
phase's to update from the merged diff — do not edit them. Flagged here so the documentation run
knows to look.

## Concurrency model

No new goroutines, no new channels, no shutdown-sequence change. The fake's
reader-signals-main-goroutine-writes discipline is untouched: the stdin reader still only sets
`escPending` (`main.go:1204`), the main poll loop still performs the sole write to `f`
(`main.go:731-734`), preserving the single-writer-of-`f` invariant. The one-shot `escEnded` gate
still makes a second ESC inert.

Test-side: the Phase 3 kicker goroutine and its `stopKick`/`sync.Once` teardown are unchanged. The
Phase 5 bounded ESC-count poll is unchanged.

## Error handling

No new failure modes in production or in the fake — `appendTurnEnd` keeps its best-effort
write-then-`Sync`, errors silenced, asserted downstream. The failure modes that change are the
test's *diagnostics*, covered in § 4 (the AC1 fatal message) and § 2 (the StopReason failure
message). Both are message-only edits; no new branches.

## Testing strategy

No new test functions. The work is in one existing test plus the fake.

**Run to green:**

```
go test -tags e2e -race -run 'TestRelayV2_PerConversationInterruptStopsRunningTurn|TestRelayV2_InterruptStopsRunningTurn|TestRelayV2_PerConversationTurnEnd' -v ./internal/e2e/
```

All three must pass — the two ESC-knob consumers and the third file whose comment references the
renamed const. Then `make check` (`go vet`, `staticcheck`, `substrate-guard`, unit tests).

**Non-vacuity, in one run (AC4).** The minted-transcript positive at `:458` finds the needle and the
bootstrap-transcript negative at `:470` does not, in the same run — the positive is what proves the
negative is not passing for free. Both now key on `[Request interrupted by user`, which the ESC
handler demonstrably emits (the wire `turn_end{cancelled}` in the same run is independent evidence
the mapper matched it). The bare-ESC count over the shared stdin log still reads 0 at Phase 4 and
exactly 1 at Phase 5.

**Mutation proof (AC5). Both runs pasted into the PR body.**

- *The mutation that earns AC2:* flip `internal/turnbridge/mapper.go:95` from
  `turnevent.TurnEndReasonCancelled` to `turnevent.TurnEndReasonEndTurn` — one token. A `turn_end`
  still arrives (the marker still matches, the event still flows, only the reason changes), so the
  run reaches the new assertion, and **that assertion** fails on the reason. Restore; green. This is
  the mutation that discriminates.
- *The weaker adjacent mutation, optional:* removing the `isInterruptMarker` arm outright deletes the
  only `turn_end` source in the new design, so the run dies ~15 s earlier at the pre-existing "never
  received a turn_end" fatal. That proves the staging is load-bearing, not that the reason assertion
  discriminates. Show it if useful; it does not substitute for the flip.

Do not attempt to prove RED by reverting the fake to the old canned line — that stages a *different*
interrupt shape and the failure would be about staging, not about the reason.

## Open questions

None blocking. Two judgement calls the developer owns:

- **Canned values in the derived const.** Any shape-preserving values are fine; the doc comment must
  name the substituted span either way. Suggest an obviously-synthetic `cwd` (e.g. `/tmp/fake-claude`)
  so nobody mistakes the line for a capture.
- **Needle constant name.** `interruptMarkerNeedle` reads well next to `perConvMidTurnMarker`; the
  name is not load-bearing, the comment is.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] SHOULD FIX — the staged line crosses the same boundary attacker-controlled
  text does, and the spec must not let the developer widen it.** The new const enters the daemon by
  the identical route as real transcript content: fake writes → `TailJSONL` → `parseEntry` → `Raw` →
  `mapEntry`. That is the point of the ticket. The hazard is in what the developer might add *around*
  it. Two guards, both already in the spec, must survive review: (a) the line carries **no**
  `tool_result` block — `ParseToolResult` precedes the marker check (`mapper.go:80-86`) and returns,
  so any entry carrying one becomes a `ToolUpdate` and never reaches the prose matcher; a "more
  realistic" line with a rejection `tool_result` bolted on would silently stop producing a
  `turn_end`, and the ticket strikes it explicitly. (b) The line carries **no** `permissionMode` —
  adding it inverts the authorship gate to "user-authored", also producing no `turn_end`. Both
  failures present identically (a 15 s timeout at the pre-existing fatal) which is why § 4's widened
  fatal message matters. Neither is exploitable; both are foot-guns that cost a developer the rest of
  the turn budget.
- **[Trust boundaries] No findings on the production boundary itself.** Zero production lines change.
  `internal/turnbridge` and `cmd/pyry` are read-only in this ticket except for the AC5 mutation,
  which is reverted before commit — the developer must verify `git diff internal/turnbridge/` is
  empty at hand-off, since the mutation edits a production file. **Reviewer: check this explicitly.**
- **[Error messages, logs, telemetry] No findings.** The widened AC1 fatal adds only paths already in
  the message (`sessionsDir`, `mintedID`) and a daemon log-event *name*
  (`interactive_turn.turn_end_no_turn`) — no payload, no screen bytes, no `peerStatic`, no stdin log
  dump. The new StopReason failure message prints two fixed reason strings. `t.Logf` additions, if
  any, carry ids and the fixed reason only. The fake's write path adds no logging.
- **[File operations] No findings.** No new file is created and no path is constructed from any new
  input. `appendTurnEnd` writes to the already-open `*os.File` the fake owns, mode and lifecycle
  unchanged; the test's paths are the same `filepath.Join(sessionsDir, …)` joins over
  harness-authored components.
- **[Subprocess / external command execution] No findings.** The knob set passed to
  `StartRotationWithRelay` is unchanged — no new environment variable, no change to what the daemon
  inherits or scrubs. The decision in § 5 to reuse `PYRY_FAKE_CLAUDE_ESC_ENDS_TURN` rather than add a
  second knob is what keeps this category empty.
- **[Concurrency] No findings.** No new goroutine, no new shared state. The single-writer-of-`f`
  invariant is preserved by not moving the write: the reader still only sets `escPending`, the main
  loop still performs the append. The `escEnded` one-shot still bounds the write to once per child.
  The new `inTurn` dependency (§ 4) is a *test-side ordering* property, not a data race — it is
  established by a happens-before chain the test already enforces (a non-idle `turn_state` observed
  on the wire before the interrupt is sent) and closed by the two-call-site argument for `endTurn()`.
- **[Threat model alignment] No findings, and one property is strengthened.**
  `docs/protocol-mobile.md` threat #1 — a client ending its own turn by quoting the interruption
  marker in a prompt — is guarded by `userAuthored` (`mapper.go:163`), and this ticket does not touch
  it. It is worth naming what this test does *not* cover: it drives the marker down the
  claude-authored path only. The forged-prompt direction is #1243's unit table
  (`mapper_test.go`), which is where it belongs — an e2e that sent a marker-quoting prompt over the
  wire would be a different oracle. **OUT OF SCOPE**, covered upstream, no follow-up ticket needed.
- **[Tokens, secrets, credentials] Not applicable, by construction.** The one adjacent question is
  the derived const's provenance: the base line at `no_end_turn.jsonl:53` carries a real developer
  worktree path, a real `sessionId`, and a real `gitBranch`. The spec's substituted-span rule (§ 1)
  replaces all of them with canned shape-preserving values, so the fake carries no copied identity
  data. That is a hygiene reason for the rule on top of the honesty reason.
- **[Cryptographic primitives] Not applicable.** No randomness, no comparison against a secret, no
  key material. The const is fixed bytes; the Noise handshake path is untouched.
- **[Network & I/O] Not applicable.** No socket, no HTTP surface, no size cap to set. The line is
  ~600 bytes of fixed content written to a local file; `TailJSONL`'s existing line-length handling is
  unchanged and unexercised by a line of this size.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-31
