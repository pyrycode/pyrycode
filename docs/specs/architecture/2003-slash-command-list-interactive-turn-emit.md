# #2003 — emit the mapped `slash_command_list` frame on the interactive turn lane

## Files read

- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.Handle`, `emitMapped`, `emit`, `eventKind` — the lane this slice extends. `Handle`'s `turnevent.ModelList` arm is the template; `eventKind`'s `ModelAnnounced` and `SlashCommandList` arms hold the claims AC#4 corrects, in two *different* arms.
- `cmd/pyry/interactive_turn_v2_test.go` → `emitterSlashCommandListFixture`, `emitterSlashCommandListSentinels`, `slashCommandListDropLogger`, `assertSlashCommandListKindLeaksNothing`, `TestInteractiveTurnEmitterV2_SlashCommandListIsNotPublished` (the tripwire this slice reddens), `TestInteractiveTurnEmitterV2_NoAppOutputLogLeak` (the existing push-error rig that owns the emit-path log sweep), and the four `ModelList` tests that are this slice's shape.
- `internal/turnbridge/outbound.go` → `MapEvent`'s `turnevent.SlashCommandList` arm — the mapping this slice routes to. It never refuses the variant (a zero-value list maps), and it is itself the second cutter: `maxSlashCommandListBytes` (#2002) takes a tail-cut prefix and *adds* to `DroppedCommands`.
- `internal/protocol/interactive.go` → `SlashCommandListPayload` (+ its `MarshalJSON` nil→`[]`), `SlashCommand` (+ its `MarshalJSON`, which normalises `Aliases` and deliberately **exempts** `TruncatedFields`) — the wire shape the fan-out test decodes.
- `internal/turnevent/event.go` → `SlashCommandList`, `SlashCommand` — the carried-never-mutated contract the arm must honour.
- `cmd/pyry/stream_turn_drain.go` → `streamTurnSink.sinkFor` (sink-full drop) and `startStreamTurnDrainV2` (not-active-session drop) — the two drop sites that stay live for this variant after this slice, named in the enumeration AC#4 rewrites.
- `docs/knowledge/features/turnbridge-package.md` § `SlashCommandList` row + the lessons below it — carries two traps this slice must not walk into: a `reflect.DeepEqual`-shaped no-mutation test **can never fail** when source and destination element types differ (`turnevent.SlashCommand` vs `protocol.SlashCommand`), and the frame cut marshals the *wire* row, not the turnevent one.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md` → the single-writer invariant this arm inherits: only the drain goroutine calls `Handle`.

## Context

`interactiveTurnEmitterV2.Handle` has a case for every `turnevent.Event` implementation the stream parser produces except `SlashCommandList`. Both prerequisites landed: `MapEvent` maps the variant onto `protocol.SlashCommandListPayload` (#2001) and the mapped frame is bounded to the v2 application-envelope cap (#2002). `internal/streamsup` has produced the variant since #1877, with a second rung added by #1891 — so today every decoded list falls into `Handle`'s default and is logged by kind. This slice adds the missing case, which is the only thing between a decoded list and a connected client's Actions menu.

No ADR is warranted: this adds no new contract. The transport question (push, not request/reply) is settled by the `model_list` sibling under #1849 and already encoded in `cmd/pyry/relay_guard_test.go`'s classification of `TypeSlashCommandList` as `"push"`.

## Design

**One production change, in `cmd/pyry/interactive_turn_v2.go`.**

1. **A new `case turnevent.SlashCommandList:` in `Handle`**, placed immediately after the `turnevent.ModelList` case and before `default`. Body is the status-peer shape, two statements: `e.flushDelta(ctx)` then `e.emitMapped(ctx, convID, ev)`. No `startTurnIfNeeded`, no `transitionTo`, no `endTurn` — `inTurn`, `turnID`, `seq` and `currentState` are untouched, which is the answer `TestTurnMarkFor_TotalOverEveryVariant` already pins as `turnMarkNone`.

   **A separate arm rather than merging into `ModelList`'s**, which needs justifying because the two variants come closer to sharing a reason than any other pair in this switch — both are properties of the CHILD reported once per `initialize` exchange, and `MapEvent`'s own arm says so. The switch's stated rule is to merge arms that share a REASON, and merging would satisfy the letter of it. It is still the wrong call, because the two arms' bodies argue about exactly the things that differ: what sits ABOVE the fan-in drop (`sessionModelHold` retains the menu; there is no analogue for commands until #2004), what the reliable fallback path is (#1846 for the menu, #2005 for the commands), and the trust origin of the carried strings (claude-authored vs workspace-authored, which is what makes the log posture stricter here). A merged arm would have to state both sets side by side and would lose the per-variant argument, which is the same basis on which the switch already keeps `ThinkingProgress` and `RateLimited` apart.

   The event is **passed through untouched** and no field of it is read in the arm. `MapEvent` copies slice headers and #2005 will read the mapped payload on a relay-leg goroutine, so a sort, an in-place dedupe or an append into a slice the event owns would be a data race, not merely a wrong menu.

2. **Two `eventKind` comment corrections, in two different arms** (AC#4):
   - The **`ModelAnnounced`** arm holds the count. `Handle` gains coverage of a 17th variant, so *"an arm for 16 of `turnevent.Event`'s 18 implementations, and the two without are …"* becomes 17 of 18 with `PermissionRequest` the only one left — a singular, so the sentence's grammar moves with it. The clause that follows (*"Landing the mapping did not shrink that count of two — a `turnbridge.MapEvent` arm is not a `Handle` case…"*) is discharged and goes. So does the qualifier ahead of the count (*"That is a claim about `ModelAnnounced` ALONE and does not generalise to the production producer"*): after this slice the generalisation holds, and `interactive_turn.unknown` is a live call site for nothing the production producer emits.
   - The **`SlashCommandList`** arm holds the rest. *"Unlike the four arms above, NO `Handle` case claims this variant"* inverts to the `ModelList` arm's own wording. The drop-site enumeration is re-derived: `interactive_turn.unknown` stops being live; the no-cursor drop (which returns before the type switch), `sinkFor`'s sink-full droppable drop and `startStreamTurnDrainV2`'s not-active-session drop stay. `emitMapped`'s unmapped drop stays **unreachable for a new reason** — something routes to it now, and `MapEvent` never refuses this variant — which is a strictly stronger statement than the one it replaces. The closing *"A PRODUCTION PRODUCER NOW EMITS THE VARIANT … Each is dropped and logged by kind"* paragraph is part of the same sweep: the production producer's output now reaches the wire on the happy path and three drop sites on the unhappy ones.

   Deliberately **not** touched: the equivalent false comments in `internal/streamsup/parser.go`, `internal/turnevent/event.go`, `internal/protocol/interactive.go` and its test, and `docs/protocol-mobile.md`. The sibling frame paid for that sweep as #1861/#1862 after #1849 shipped; the doc is #2010's.

**No change to `emitMapped`, `emit`, `eventKind`'s returned values, or any type.** The capability gate stays single, in `emit` — writing a second one in the arm is how a single gate stops being single.

## Concurrency model

Unchanged. `Handle` runs only on the producer's single Run goroutine (the drain goroutine, per the streamsup drain doc), so the arm reads and writes nothing that needs guarding; it spawns no goroutine and owns no queue, so there is no new shutdown path. `emit`'s per-conn fan-out and the `eventring.Ring` append are untouched.

**Two queues, two answers**, both inherited rather than introduced. Downstream at `pushQueue` this is not a droppable delta (the droppable set there is `assistant_delta` only). Upstream at the fan-in it is: `turnMarkFor` answers `turnMarkNone`, so `sinkFor` classes the event droppable and may refuse it at `droppableCap`. That loss point, the bootstrap child's unaddressable report and the rotation gap are the sibling's published best-effort losses, repaired by the connect-time snapshot in a later slice of this family — explicitly not fixed here.

## Error handling

The arm introduces no failure mode of its own; it has no branch. The existing paths carry every failure:

- **No cursor** → `Handle` returns before the type switch and Debug-logs `interactive_turn.no_cursor` with `kind` only. Unchanged.
- **Unmappable** → `emitMapped`'s `ok == false` branch. Unreachable for this variant, because `MapEvent` maps even a zero-value `SlashCommandList`; it stays as defence in depth.
- **Marshal failure** → `emit`'s `interactive_turn.marshal_err`, which logs no payload and no `err.Error()`.
- **Push failure** → `emit`'s `interactive_turn.push_err`, which logs the transport sentinel and no content.

Every string this variant carries is workspace-authored — lower trust than claude's own — so no value derived from the event reaches a log record at any level, on the emit path or any drop path. That is enforced by test, not by convention (below).

## Testing strategy

All in `cmd/pyry/interactive_turn_v2_test.go`, driven by the existing `emitterSlashCommandListFixture` (two entries, every field distinguishable, second `TruncatedFields` nil).

- **AC#1 — `TestInteractiveTurnEmitterV2_SlashCommandListFansOutToEveryInteractiveConn`.** This is the positive twin that *replaces* `…SlashCommandListIsNotPublished` in place, which is the tripwire #1854 planted for exactly this slice. Three conns (two interactive, one not): each interactive conn receives exactly one envelope of `protocol.TypeSlashCommandList`, the non-interactive conn receives none. Decode the payload on one conn and assert `ConversationID` and a per-field `want` built from the fixture's own rows — every field of every entry holds a distinct sentinel, so a `Name`/`Description` swap or an entry-index swap is red, and row two's nil `TruncatedFields` must survive the round trip as nil.

  **`DroppedCommands` is deliberately not asserted.** #2002's byte cut composes with streamsup's entry cut, so the wire count is the event's plus whatever the frame bound dropped; `internal/turnbridge` already pins that composition and re-pinning it here is out of scope.

- **AC#2 — `TestInteractiveTurnEmitterV2_SlashCommandListNoLifecycleMutation`.** Bare event before any turn emits exactly one frame — no `turn_state`, no `turn_end` — and `inTurn`/`turnID`/`currentState` are asserted directly; then a `TextChunk` is driven past it to prove a fresh turn still opens.

- **AC#2 — `TestInteractiveTurnEmitterV2_SlashCommandListMidTurnDoesNotDisturbOpenTurn`.** Mid-turn interleave: buffered text keeps its wire position ahead of the frame, and the load-bearing half is the event driven PAST it — an `endTurn` on an already-open turn only shows its damage on the next event, so the second delta's `turn_id` and unbroken `seq` are what bite.

- **AC#3 — the emit-path log sweep goes in the existing rig, not a new one.** `assertSlashCommandListKindLeaksNothing` fatals on an empty log and requires `kind=slash_command_list` present, so it is a *drop-site* helper and cannot be aimed at the emit path, where there is no drop record. Extend `TestInteractiveTurnEmitterV2_NoAppOutputLogLeak` instead — its event table (a live cursor plus a conn whose `Push` always fails, so the log-heavy `push_err` branch fires per envelope) gains `emitterSlashCommandListFixture`, and its `leakable` slice gains `emitterSlashCommandListSentinels()...` plus the entry count's decimal spelling. The `logs == ""` Fatal is the positive control proving the event traversed the log-heavy path. Paired with a payload-presence half on the same rig: log-absence alone is passed by an arm that silently drops the event, so the presence check is what discriminates.

- **AC#3, drop paths — already covered, kept.** `TestInteractiveTurnEmitterV2_SlashCommandListEventKindNamesTheVariant` stays as-is on the empty-cursor rig, since the no-cursor drop returns before the type switch and remains reachable. Its doc comment's claim that the default arm is a live site (*"see the live-cursor test below"*) goes false and is corrected, as is `assertSlashCommandListKindLeaksNothing`'s "the two drop-site tests", which becomes one on this lane. The `stream_turn_drain.go` drops route through the same `eventKind`, whose per-value negatives that helper already owns.

- **Not carried: a `…NotSynthesizedWithoutAnEvent` twin.** The sibling has one (#1849 AC#4) and it is not vacuous — `MapEvent` maps a zero-value list, so an emitter synthesising a frame at a turn boundary would put an empty menu on the wire. #2003's four ACs do not ask for it and the ticket's estimate is the contract, so it is left to whichever slice needs it. Naming it here rather than silently omitting it.

**Gate:** `go test -race ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`. RED first: the three new/converted tests must fail against the un-armed `Handle` before the arm lands.

## Open questions

1. **Merge the arm with `ModelList`'s or keep it separate?** Resolved in Design above, before implementation: separate, because the two arms' bodies argue about the things that differ (retention above the fan-in, reliable fallback path, trust origin of the strings). Recorded here because the switch's own merge rule points the other way and a reader will ask.
2. **Does `assertSlashCommandListKindLeaksNothing` survive with one caller?** Yes — kept, with its doc corrected. Inlining it would churn the surviving drop-site test for no gain, and the helper is the right shape for the drop sites in `stream_turn_drain.go` a later slice may pin.
3. **Whether the `emitMapped` unmapped-drop clause should simply be deleted from the enumeration.** No: it stays, with its reachability re-argued. Deleting it would lose the fact that the branch is now *routed to and still never taken*, which is a stronger statement than "nothing routes here".

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings, and the boundary is explicit rather than scattered. The untrusted→bounded crossing happened upstream in `internal/streamsup`'s decode, which caps every content dimension (`maxSlashCommandName`, `maxSlashCommandArgumentHint`, `maxSlashCommandDescription`, `maxSlashCommandAlias`, `maxSlashCommandAliasCount`, `maxSlashCommandListEntries`); the frame-byte dimension is bounded in `MapEvent` by `maxSlashCommandListBytes`. This slice adds no parsing and no boundary — it routes an already-decoded, already-bounded value to an already-audited fan-out. The design decision that makes this category thin is the arm reading **no field** of the event. Downstream trust is signalled by type: `protocol.SlashCommandListPayload` is the bounded-but-**not-sanitized** form, and both types' SECURITY paragraphs assign the render boundary to the client.
- **[Tokens, secrets, credentials]** Not applicable by design: the arm mints nothing, stores nothing and reads no credential. The one identifier in play is `conversation_id`, supplied by `MapEvent` from the cursor `Handle` already read; envelope ids come from `emit`'s existing per-conn `nextID`, and turn ids from `conversations.NewID` (`crypto/rand`) which this arm never calls, since it opens no turn.
- **[File operations]** Not applicable: no path is constructed, opened, stat'd or written anywhere on this path. `stream_turn_drain.go` resolves no `<uuid>.jsonl` and this arm adds no filesystem contact.
- **[Subprocess / external command execution]** No findings, and this is the category where a workspace-authored string would do real damage. A slash command's `Name` is not an identifier — `__remote-workflow` is in the committed capture — and nothing in this slice passes any field to `exec.Command`, `filepath.Join`, `filepath.Match` or a regexp. The sink this slice adds is exactly one: a field of a `protocol.SlashCommandListPayload` on the wire. Publishing a name does not make it invokable; the frame declares no inbound verb.
- **[Cryptographic primitives]** Not applicable: no randomness, no comparison against a secret, no key material. Sealing is `V2SessionManager.Push`'s, unchanged and out of this diff.
- **[Network & I/O]** No findings — and this is the category that would fail without #2002. The single unbounded-in-principle input is the `commands` array, whose serialised size is a function of the workspace rather than of the daemon; `maxSlashCommandListBytes` bounds it below the 65519-byte v2 application-envelope cap with a measured reserve, enforced by `TestSlashCommandListPayload_FitV2EnvelopeCap`. No count cap could have closed that gap. Resource exhaustion downstream: the frame is not a droppable delta at `pushQueue`, so a burst holds queue slots — bounded on the producer's side by one `initialize` exchange per child, which is claude's cadence rather than anything network-reachable. Adding a second, differently-shaped filter in the arm is the hazard four neighbouring arms each name.
- **[Error messages, logs, telemetry]** SHOULD FIX, and this is the finding that shapes Phase B. The strings are workspace-authored — a lower-trust origin than claude's own — so the MUST-NOT-log set is every `Name`, `ArgumentHint`, `Description`, alias and `TruncatedFields` entry, plus the entry count and `DroppedCommands`, at every level including DEBUG. The MUST-log set is the content-free discriminants only: `event`, `kind` (the variant name alone), `conversation_id`, `turn_id`, `env_id`, `conn_id`, and `Push`'s transport sentinel. The arm itself writes no log line, which is the structural half; the enforcement half is that the emit path was never swept before, only the drop paths. Phase B discharges it by extending `TestInteractiveTurnEmitterV2_NoAppOutputLogLeak` as the Testing strategy specifies — the verifier should check that sweep landed on the emit path and not merely that the drop-site helper still passes.
- **[Concurrency]** No findings. The arm takes no lock, so there is no ordering to document; it mutates no shared state, so there is no check-then-mutate window; it spawns no goroutine, so nothing can leak. The one real hazard is aliasing rather than locking: `MapEvent` copies slice headers, so the payload shares `Aliases` and `TruncatedFields` backing arrays with the event, and #2005 will read the mapped payload on a relay-leg goroutine. The design decision that closes it is the arm reading no field of the event and performing no sort, dedupe, filter or append — a rule this plan states so the implementation cannot quietly drop it.
- **[Threat model alignment]** No findings. `docs/protocol-mobile.md` § Security model governs, and this frame is push-only binary→phone on the sealed v2 transport, gated on the interactive capability in `emit` — one gate, in one place. Best-effort delivery is the acknowledged gap: the bootstrap child's report is lost unconditionally, the fan-in may refuse the frame under load, and a session rotation delivers no fresh inventory. All three are named out of scope here and are repaired by this family's connect-time snapshot slice (#2005), which is also why no retention exists on this side yet (#2004).

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02

## Revisions

### 2026-09-02 — the entry-count needle does not go in the emit-path rig

**What changed.** The Testing strategy above says `TestInteractiveTurnEmitterV2_NoAppOutputLogLeak`'s `leakable` slice gains `emitterSlashCommandListSentinels()...` **plus the entry count's decimal spelling**. Only the sentinels were added; the count needle was not.

**What drove it.** Implementation, not a review finding. That rig keeps slog's own time attr, and every record it captures is a `push_err` carrying `env_id` — a small monotonic integer that runs 1..N across the event table. A whole-log `strings.Contains` for a one- or two-digit count is therefore TRUE against a *correct* implementation, which is the vacuous-needle failure with its polarity inverted: the assertion would be red for a reason that has nothing to do with a leak. The sibling's own discharge of this criterion (#1849) added sentinels only, for the same structural reason.

**The new contract.** AC#3's entry-count half is asserted where the record is digit-free: `assertSlashCommandListKindLeaksNothing`, whose logger drops the time attr and whose drop records carry no `env_id`. It already carries `strconv.Itoa(len(emitterSlashCommandListFixture.Commands))` and needed no change. The emit-path rig carries a comment naming why the needle is absent, so a later reader does not "complete" the sweep and turn the test red. The security review's [Error messages, logs, telemetry] SHOULD FIX is discharged by the sentinel sweep on the emit path plus that count needle on the drop path — no log record on either path carries a workspace-authored string, and the count negative holds on the only path that can express it.
