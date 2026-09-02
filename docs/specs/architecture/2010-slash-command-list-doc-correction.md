# #2010 — Correct § `slash_command_list`'s published claim that nothing emits the frame

**Ticket:** [#2010](https://github.com/pyrycode/pyrycode/issues/2010) (`documentation`, `size:s`, no `security-sensitive`)
**Scope:** `docs/protocol-mobile.md` only. No production file, no test, no fixture.

## Files read

Documentation:

- `docs/protocol-mobile.md` → § Application message types' `slash_command_list` row, § `model_list`, § `slash_command_list`, § `question_shown`, § Attachments, § Reconnect / Backfill semantics, § Changelog — the file under edit; § `model_list` is the shape #1860 established for the same correction on the sibling and every rewritten sentence below is measured against it.
- `docs/specs/architecture/2003-slash-command-list-interactive-turn-emit.md` → its delivery-window and threat-model paragraphs — one of the three disagreeing loss-point enumerations the ticket sends me to re-derive.

Code walked for the delivery window (AC3) — the enumeration is **re-derived from these symbols**, not copied:

- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.Handle` (the no-cursor drop), its `turnevent.SlashCommandList` arm, `emitMapped`, `emit` — `emit` is where the frame is appended to the replay ring **before** the per-conn fan-out, which is what stamps it with an `event_id` and kills the section's "does not belong to the structured live-session stream" opening.
- `cmd/pyry/stream_turn_drain.go` → `streamTurnSink.sinkFor` (the droppable refusal at `droppableCap`) and `startStreamTurnDrainV2` (the not-active-session drop). The drain's gate resolves the active session **before** `Handle` runs, so on this frame's path it is the gate that actually fires.
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory` — binds `sink.sinkFor(cfg.SessionID)` **at runner construction**, which is the mechanism behind the rotation gap.
- `internal/sessions/transition.go` → `Pool.RotateForNewSession`, `Pool.notifyTransition`, `Pool.rebindConversation`; `internal/sessions/pool.go` → `Pool.rekeyLocked`; `internal/conversations/registry.go` → `Registry.RebindSession`. Together: a rotation re-keys the pool entry **in place** and rebinds the conversation, while the runner (and its sink tag) survives unchanged — so the reporting child's tag and the conversation's binding are no longer the same id.
- `cmd/pyry/relay.go` → `boundSessionIDForActive` — what `activeSession()` resolves to.
- `internal/relay/v2session.go` → `forwardEnvelope` — its `last_event_id` dedup, which the re-derivation **demotes**: see Design § 3.
- `internal/relay/v2session_slashreconcile.go` → `reconcileSlashCommandLists` — the connect-time unicast, its `interactive` + nil-seam gate, its empty-set early return, and the deliberately nil `EventID`. Its own doc block is the second disagreeing enumeration; its **fifth Mode B instance** self-count is the ordinal the ticket forbids publishing.
- `cmd/pyry/session_slash_command_list.go` → `resolveBoundSlashCommandList` (the refusal arms the snapshot's coverage gaps come from) and `retainedSlashCommandLists` (enumerate-all, archived conversations contribute, bootstrap contributes nothing).
- `internal/turnbridge/outbound.go` → `MapEvent`'s `SlashCommandList` arm and `maxSlashCommandListBytes` — `DroppedCommands: e.DroppedCommands + (len(e.Commands) - len(commands))` is the literal proof of "carried and added to, never recomputed" and of the tail cut.
- `internal/streamsup/parser.go` → `maxSlashCommandListEntries` (128, #1826) and `Parser.emitSlashCommandList` — the count's base, and the zero-length early return that makes "an empty list is never retained" true at the producer rather than by convention.

## Context

`docs/protocol-mobile.md` is the published contract clients build against. Several of its live statements about `slash_command_list` went false as #2001–#2009 landed, and the file records none of it. #1860 fixed exactly this for the sibling `model_list` and deliberately left the `slash_command_list` claims standing, because at that time the frame genuinely had no producer. That reason has expired.

Six live-prose sites are false or stale at HEAD:

1. § Application message types' row ends *"nothing emits it yet (#1720)"*.
2. § `slash_command_list`'s emission paragraph is titled **"Nothing emits this frame yet."** and names #1720 as the producer — a ticket that was split and no longer exists as work.
3. `dropped_commands` states its gap **twice** — *"Nothing counts it yet"* in the field table and *"Nothing counts it."* in the paragraph — and the paragraph forbids `len(commands) + dropped_commands`, which is the one arithmetic that now works.
4. § `question_shown`'s bounds table **quotes** the phrase *"Nothing counts it yet"* as the wording it deliberately copies — a quotation of text this ticket deletes, carrying no ticket number, so a number-grep sweep misses it.
5. § `question_shown`'s closing precedent sentence reads *"`slash_command_list` still awaits its own (#1726 declared, #1720 pending)"*.
6. § `slash_command_list`'s opening says the frame *"does not belong to the structured live-session stream"* — the sentence #1860 removed from § `model_list` for the reason it removed it: a client reads it as *carries no `event_id`*, and the live-lane emission stamps one.

Plus two absences: `slash_command_list` is missing from § Reconnect / Backfill semantics' Mode B list, and the section carries no reconcile-on-connect note where the other Mode B frames do.

**No ADR is warranted.** This publishes decisions already recorded by #2001–#2009; it makes none of its own.

## Design

No code. Seven edit sites in one file, plus one changelog entry. Every citation is by **ticket number or symbol name**, never by line.

### 1. The emission claim (AC1) — three sites

- **§ Application message types, the `slash_command_list` row.** Replace the trailing `nothing emits it yet (#1720)` with the emit statement, shaped after the `model_list` row directly above it: mapping #2001, frame bound #2002, producer #2003, proven end to end by #2008 and — for the connect-time snapshot — #2009. One clause names the best-effort live lane and one names the snapshot, so a reader of the summary table learns the frame has **two** delivery paths.
- **§ `slash_command_list`'s emission paragraph.** `**Nothing emits this frame yet.**` → `**This frame is emitted.**`, naming #1726 (type), #1727 (shape), #1718 (fixtures + section), #2001 (mapping), #2002 (frame bound), #2003 (producer), #2008 (proof). The declare-then-emit sequencing sentence stays and is updated from *"this is the fourth instance"* to record that this frame has now **completed** the sequence, matching how § `model_list` and § `question_shown` phrase their own completions.
- **§ `question_shown`'s closing precedent sentence.** `slash_command_list still awaits its own (#1726 declared, #1720 pending)` → completed, citing #1726 declared / #2001 mapped / #2003 emitted. This is the same live-cite-to-a-split-away-ticket repair the `2026-09-01` (#1974) changelog entry describes for its own four cites.

### 2. `dropped_commands` (AC2) — both statements, plus the § `question_shown` quotation

This is the correction most likely to change what a client does, so it gets § `model_list`'s prominence: an inverted field-table row and a **bolded lead sentence** on the paragraph.

- **Field-table row.** *"Nothing counts it yet — see below."* → the count is real, fed by two cuts, carried rather than recomputed.
- **The paragraph.** Full inversion. What it must state, all four verified above:
  - `len(commands) + dropped_commands` **is** the menu's true size after both cuts.
  - **Two cuts feed it, named.** The decode's entry cap `maxSlashCommandListEntries` (128, #1826) is the base; the frame-level serialised byte bound (#2002) **adds its own drop on top** rather than recomputing, which is `MapEvent`'s arm literally.
  - Both cut **from the tail**, so the entries a client receives are claude's first N in claude's own order.
  - **The divergence from the sibling that a client must not carry over.** § `model_list` may say a non-zero `dropped_models` always arrives beside exactly ten entries, because it has one cutter that cuts only the overflow. Here the byte bound can fire first, so a non-zero `dropped_commands` arrives beside **any** number of entries and a shorter list is **not** necessarily a complete one. This is the reachable-pairs check § `model_list`'s own "10 of 40" correction was made for; no `N of M` illustration is published here because no single pair is representative.
  - Neither number is a wire constant — a client must never hardcode either, treat a list of exactly 128 as a signal, or derive a cap from anything but this field.
- **§ `question_shown`'s bounds-table quotation.** The quoted phrase is being deleted, so the sentence is re-pointed rather than left dangling. The **precedent it is making survives** — a gap named as one beats a bound documented as if enforced — so the sentence keeps that principle, cites § Attachments (#1752), which the row directly above it already names as the rule's home, and records that § `slash_command_list` carried the phrasing until this ticket inverted it. The pointer to the `2026-08-27` changelog entry stays: that entry is history and is not touched.

### 3. The delivery window (AC3) — one new paragraph, re-derived

Modelled on § `model_list`'s delivery-window paragraph but with **its own enumeration**, because the walk disagrees with all three existing descriptions. What gets published:

- **What runs on a schedule is an ask, not a delivery**: one `initialize` exchange per claude child spawn. Nothing bounds the rate on either side.
- **Two delivery paths, and they differ on `event_id`.** The live-lane frame is appended to the per-conversation replay ring before the fan-out, so it **carries an `event_id`** and a reconnecting client with a cursor is replayed it (Mode A). The connect-time reconcile deliberately leaves `EventID` nil, so its frame is **never in the ring** and never replayed. That is the client-visible half of the opening-sentence fix in § 5.
- **The live lane is best-effort, at three loss points** (path order):
  1. **The producing session is not the active conversation's bound session.** `startStreamTurnDrainV2` drops every event tagged with any other session. Two cases land here. The **bootstrap child**, spawned at daemon startup before any conversation is routed, owns no conversation record, so nothing ever binds it and **its inventory is lost unconditionally** — a client attaching to an already-running daemon has not merely missed the frame, it was never sent one. And a **session rotation**: the rotation re-keys the pool entry in place and rebinds the conversation, while the runner and the session tag its parser sink was constructed with survive unchanged, so the reporting child's tag and the conversation's binding are no longer the same id and **a rotation delivers no fresh inventory** — even though it does spawn a child, and therefore a new ask.
  2. **The session is busy.** `turnMarkFor` answers `turnMarkNone`, so `sinkFor` classes the frame droppable and refuses it past `droppableCap` under load. **Missing it is not an error**: nothing is retried and no error frame reports a lost menu.
  3. **No interactive connection at that instant.** `emit` fans out to the connections open and interactive-granted right then; a client that connects a moment later gets nothing from this path.
- **What is explicitly *not* a loss point, and this is the correction to the two disagreeing enumerations.** `forwardEnvelope`'s `last_event_id` dedup drops only an envelope whose `event_id` the connection **already received in its reconnect replay**, and the replay watermark is clamped to the newest id retained at handshake, so a frame emitted afterwards always carries a higher id. It is inert for the reconciled frame too, for a second reason: `EventID` is nil there. Publishing it as a live-lane loss point would tell a client to expect a loss the code cannot produce.

### 4. The connect-time snapshot and its gaps (AC3, second half; AC4 second half)

A **Reconcile on (re)connect** paragraph, shaped like § `question_shown`'s so the family reads uniformly:

- On any (re)connection the daemon unicasts one `slash_command_list` per conversation whose bound session currently holds an inventory (#2006 landed the reconcile, #2007 wired the daemon-side enumeration; #2005 is the per-conversation resolver behind it). #2009 proves a client connecting *after* the child reported receives the retained list, driving no turn and sending nothing.
- **Match-and-replace by `conversation_id`**, idempotent on every connect; the frame is snapshot-shaped full state rather than a delta, so a re-send is safe.
- **Correlate by `conversation_id`, never by position** — the enumeration walks the registry in insertion order and that order is not a contract; every envelope in the burst carries the same non-load-bearing envelope id.
- **What the snapshot does not cover**, stated positively rather than as "the live-lane losses are fixed":
  - a conversation whose bound session is gone, or which is unbound, or whose session has reported nothing, is simply **absent** from the reconcile — those are `resolveBoundSlashCommandList`'s refusal arms, not an error;
  - an **empty list is never retained in the first place** (`emitSlashCommandList` returns early on a zero-length entry list, #1877), so "no frame for this conversation" and "claude offered nothing" are the same observation from a client's side;
  - a connection that handshakes while **nothing is retained receives no frame at all**, and the handshake still completes normally — #2009 pins that;
  - it is gated on the negotiated `interactive` capability, like the rest of the structured stream;
  - **archived conversations contribute** — the enumeration is unfiltered by design, and the client decides what to show.
- The actionable consequence, § `model_list`'s in a family that now has a repair: **never block a command menu on the live frame**; render without one and let the connect-time snapshot fill it in.

### 5. The section opening (AC1's sibling fix)

Delete *"and does not belong to the structured live-session stream"* and replace with § `model_list`'s corrected construction: not a `turnevent` variant and not one of the fifteen turn-stream events, **though it rides the same live interactive lane and carries an `event_id` like them** — plus the half `model_list` does not need, that the connect-time copy deliberately carries none. Everything else in the opening (the `control_response` arrival, neither-opens-nor-closes-a-turn, snapshot-not-delta, the identities/verbs split) is true and stays.

### 6. Mode B (AC4)

§ Reconnect / Backfill semantics' **Reconcile on connect** bullet gains `slash_command_list` (the current retained command menus, #2006/#2007) beside the modal, the queue backlog and the question batches; the **Match-and-replace by stable id** bullet gains `conversation_id` as this frame's key and the replaces-in-place clause.

**No ordinal is published.** `reconcileSlashCommandLists` calls itself the fifth Mode B instance; this document lists three and will list four, because the model-list reconcile never reached this file. "The fourth" would be true of the document and false of the daemon, so the document names the frames and counts nothing.

### 7. Changelog (AC5)

One new dated `2026-09-02` entry at the **top** of the list. Existing entries are **not edited** — #1860's own entry names which statements it left standing and why, and rewriting it would destroy that record. The new entry records: the emission correction and its slices; the `dropped_commands` inversion with both cuts named; the delivery window including the demoted dedup; the connect-time snapshot and its gaps; the Mode B addition without an ordinal; and the two § `question_shown` cites swept in the same pass.

It **names what it deliberately leaves standing**:

- § `model_list`'s *"no connect-time snapshot today"* and its *"deliberately absent from the Mode B list"* — both stale since #1863/#1867, confirmed and reported on #2010, belonging to the sibling family and untouched here. This is why the Mode B list now names four frames while the daemon runs five.
- The Go comments that carry the same false claims — the sibling paid for those separately as #1861/#1862, #2003 deliberately left this frame's standing, and no ticket owns them yet.
- § Attachments' sentence citing `model_list` and `slash_command_list` as declare-then-publish precedent, which is still true and is not swept.

## Concurrency model

Not applicable — no code changes, no goroutines, no shutdown path.

## Error handling

Not applicable — no code changes. The **documented** error/loss behaviour is Design § 3's three loss points and § 4's four coverage gaps, all of which are silent-by-design absences rather than error frames, and the prose says so explicitly so a client does not wait for one.

## Testing strategy

There is no behaviour to test — the ticket forbids behaviour, fixture and test changes, and every corrected sentence's evidence is a landed slice of #2001–#2009 plus the code walk recorded under **Files read**.

Verification is therefore the mechanical gates plus a claim-by-claim sweep:

1. `go vet ./...` and `go build ./cmd/pyry` — unchanged by construction; run to prove the tree is untouched.
2. `make cite-guard` — the citation gate. Every new citation is a ticket number or a symbol name; no `file.go:NNN`, no bare `:NNN`, no range.
3. `make docs-guard` — `docs/protocol-mobile.md` is not under `docs/knowledge/features/`, so the size cap does not bind it, but the false-heading rule does: no added line may begin with a ticket reference after a wrap.
4. A `grep` sweep for the deleted phrases (`Nothing counts it`, `Nothing emits this frame yet`, `does not belong to the structured live-session stream`, `#1720`) across the file, confirming that only **changelog** occurrences survive — history stays, live prose does not.

No `-race` package suite is named because no package is touched.

## Open questions

1. **Does the drain's gate or `Handle`'s cursor gate drop the bootstrap child's report?** Resolved during the walk: both would, and the drain's fires first — `boundSessionIDForActive` returns `false` when no conversation is current, so the event never reaches `Handle`. § `model_list`'s published text attributes the loss to the no-cursor drop. The outcome is identical, so the published sentence states the outcome (lost unconditionally) and attributes the gate to the active-session resolution, which is the one that actually runs on this path. No sibling text is edited to match; that is the sibling family's ticket.
2. **Is `forwardEnvelope`'s dedup a loss point?** Resolved: no, and § 3 publishes the reasoning. This is a deliberate departure from `reconcileSlashCommandLists`' own doc block, which counts it as one of three. The Go comment is not corrected here — the Go-comment sweep is out of scope and unticketed — so the doc and that comment will disagree until it is. Named in the PR body as a lesson.
3. **Does the producer ever emit an empty `commands` array?** Resolved: no — `emitSlashCommandList` returns early on a zero-length entry list. The field table's *"an empty `[]` is a positive statement"* is a **decoder** instruction and stays as written; the never-emitted fact is published in § 4 where it is actionable, not as a contradiction of that row.
4. **Should the Mode B addition mention that the daemon runs a fifth instance the document does not list?** Answered by the ticket: publish no ordinal. The gap is recorded in the changelog entry's leaves-standing clause instead, where it belongs to the sibling family's future ticket rather than to the Mode B contract a client reads.
