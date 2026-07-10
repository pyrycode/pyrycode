# Spec #879 — Reconcile-on-connect contract (docs)

**Ticket:** pyrycode/pyrycode#879 · **Size:** XS · **Labels:** `documentation`, `enhancement` · **Not** `security-sensitive`.

Docs-only. Zero production/test source files. Writes the *single written rule* that the
already-merged reconcile-on-connect mechanism (#877 modal, #878 queue, #829/#903/#904 e2e)
implements, so every client author builds against one contract instead of inferring
control-state reconciliation from daemon behaviour.

## Context

The #874/#875 hold-and-reflush-within-grace vs. resync-after split was drawn by the
transport, not the problem. #877 and #878 (both merged) implement the shared mechanism:
on any (re)connection a v2 session that reaches `V2StateOpen` with the `interactive`
capability is brought to **current control truth** — the still-outstanding `modal_shown`
(same `modal_id`) is unicast, and the current per-conversation `queue_state` is unicast for
every non-empty backlog. This ticket writes that rule into the protocol docs and records
the supersession in ADR 025.

The core insight to document: **the axis of difference is data type, not outage length.**
Bulk transcript content reconnects via the cursor/ring path (`last_seen_ts` bulk backfill;
`last_event_id` #647 ring replay with `resync` fallback) — *replay of past events*. Control
state reconnects via a cheap current-state snapshot — *match-and-replace by stable id,
applied idempotently on every connect, replaying no past control events*. These are
complements, not alternatives; the #647 machinery stays exactly as-is.

## Files to read first

- `docs/protocol-mobile.md:850-852` — `## Backfill semantics` (the stub: "Unchanged from v1.
  All backfill frames ride inside `noise_msg`."). **Primary edit site** — expand into the
  two-mode reconnect rule (AC1–AC4). The existing v1-backfill line is preserved as the
  bulk-transcript sub-part.
- `docs/protocol-mobile.md:580-603` — `#### Reconnect replay & resync (consumer, #647)`. The
  bulk-transcript event-ring path. **Read to cross-link, do NOT modify** — the new contract
  is its control-state complement (Technical Notes).
- `docs/protocol-mobile.md:630-675` — `### Modal (v2)` intro + `modal_shown`/`modal_dismissed`
  tables + the **Security & validation contract** note (L675). The nonce + `answer_token` +
  first-answer-wins semantics AC3 references already live here — add a *pointer*, don't
  restate them. Edit site for AC1 (Modal points to the rule).
- `docs/protocol-mobile.md:677-692` — `### Queue (v2)` intro + `queue_state` table +
  **Emission (#722)** note. **L692 is now stale**: "a phone that reconnects after missing a
  `queue_state` sees the current backlog only on the next change" contradicts #878's
  connect-time reconcile. Edit site for AC1 (Queue consistent with the rule).
- `docs/protocol-mobile.md:401-403` — `### Reconnect` (backoff/jitter timing, under Connection
  lifecycle). Add a one-line cross-reference to `## Reconnect / Backfill semantics`; do not
  move the application-layer contract into this timing subsection.
- `docs/protocol-mobile.md:405-451` — Application message types table; `modal_shown` (L438) /
  `queue_state` (L442) already say "not part of the reconnect-replay ring." Read for
  vocabulary consistency; no edit required beyond the sections above.
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md:130` —
  `**Backpressure / replay.**` paragraph. **Edit site for AC5.** Contains the exact wording
  to supersede for control events: "the binary replays from a bounded per-conversation event
  ring." Read L134-145 (Security model 1–4) — the nonce/deny-on-timeout/first-answer-wins
  invariants AC3 codifies are already stated there; the note references, does not restate.
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md:484-486` (in
  protocol-mobile.md) — the `> **Superseded as a requirement — 2026-06-22 (ADR 025
  amendment).**` blockquote is the **house style** for a dated superseding note. Mirror it.
- `docs/knowledge/codebase/877.md`, `docs/knowledge/codebase/878.md` — the two mechanisms
  being documented. Read for the precise contract (unicast, same `modal_id`, `queue_state`
  snapshot-shaped so re-send is idempotent, `EventID` nil ⇒ never in the ring). Cite the
  tickets; do not restate the implementation.
- `docs/knowledge/features/v2-session-manager.md` — anchors
  `#connect-time-modal-reconcile-877--outstandingmodals-seam--reconcilemodals` and
  `#connect-time-queue-reconcile-878--outstandingqueues-seam--reconcilequeues` for optional
  deeper cross-links from the protocol doc.

## Design — what to write, where

Four edit sites. One canonical statement (Backfill semantics), two consistency pointers
(Modal, Queue), one dated supersession note (ADR 025), plus a one-line cross-ref from
`### Reconnect`. No new files. No restatement of mechanism internals — reference #877/#878/#829.

### Edit site 1 (canonical) — `## Backfill semantics` → `## Reconnect / Backfill semantics`

Retitle `## Backfill semantics` (L850) to `## Reconnect / Backfill semantics` (matches AC1's
`§ Reconnect / Backfill semantics`; no internal anchor link depends on the old title — the
`#backfill-semantics` anchor is unreferenced across `docs/`, verified by grep). Expand the
stub into a section stating **two reconciliation modes keyed on data type**:

- **Preserve** the existing v1 line as the bulk-transcript-backfill paragraph.
- **Mode A — bulk transcript content → cursor backfill.** `last_seen_ts` bulk history
  (`backfill_since`/`message_chunk`/`backfill_done`, unchanged from v1) and the #647
  mid-turn `last_event_id` event-ring replay, with the `resync` marker as the
  snapshot-fallback when a cursor ages off the bounded ring. This is *replay of past events*.
  Link to `#### Reconnect replay & resync (consumer, #647)`.
- **Mode B — control state → current-state snapshot (the new rule).** State, as the contract
  all clients build against:
  - **(AC1)** On any (re)connection the daemon brings the client to **current control truth** —
    the still-outstanding modal (#877) and the current queued backlog (#878) — and **replays
    no past control events.**
  - **(AC2) Match-and-replace by stable id, applied idempotently on every connect.** Keyed on
    stable identity: `modal_id` for modals; `conversation_id` plus per-message
    `queued_msg_id` for queue backlog. Spell out the three sub-rules:
    - a re-delivered `modal_shown` for a **known** id updates in place — **never double-shows**;
    - an id the client has **already resolved** is a **no-op**;
    - control state **not re-asserted after a fresh handshake is gone** (reset-on-reconnect):
      each reconnect is a fresh Noise_IK handshake; the client resets control state and rebuilds
      it from whatever the daemon re-asserts, so anything resolved-while-disconnected simply
      does not reappear.
  - **(AC4) The axis of difference is data type, not outage length.** One sentence contrasting
    Mode A (bulk → cursor backfill + snapshot fallback) with Mode B (control → always a cheap
    current-state snapshot). Explicitly: the choice of mechanism is *what kind of data*, never
    *how long the client was away*.
  - Cross-reference #877/#878 for the mechanism and #829 for the exactly-once e2e guarantee;
    do not restate their internals.

### Edit site 2 (pointer) — `### Modal (v2)` intro (L632) + Security & validation contract (L675)

Add to the Modal section a short note (or extend the intro) making it **point to** the
Backfill rule and carrying **AC3** (answer semantics unchanged under re-delivery):

- On (re)connect the daemon re-asserts the outstanding modal by **unicasting `modal_shown`
  with the original `modal_id`** (#877) — this is match-and-replace by stable id (see
  `## Reconnect / Backfill semantics`); a re-sent `modal_shown` for a known id updates in
  place and never double-shows.
- **(AC3)** Answer semantics are **unchanged under re-delivery**: the one-time modal `modal_id`
  nonce plus the client-minted `answer_token` idempotency key keep a prompt answerable
  **exactly once**, re-sent or not; **deny-on-timeout is armed once, at raise time, and is
  never re-armed by a re-send.** These invariants already live in the **Security & validation
  contract** (L675) and ADR 025 § Security model (1–4) — **reference them, do not restate.**
  The one net-new fact is the "unchanged under re-delivery / never re-armed by a re-send"
  framing tying them to reconcile-on-connect.

### Edit site 3 (consistency fix) — `### Queue (v2)`, L692

L692 currently reads "It is **not** part of the reconnect-replay ring; a phone that reconnects
after missing a `queue_state` sees the current backlog only on the next change." The first
clause stays true; the second is now **contradicted by #878**. Rewrite to be consistent with
the rule:

- Keep: `queue_state` is **not** part of the #647 reconnect-replay ring (`EventID` nil).
- Replace the stale clause with: on (re)connect the daemon reconciles current queue truth by
  **unicasting a `queue_state` snapshot for every non-empty backlog** (#878) — a current-state
  snapshot, not ring replay. Because `queue_state` is already snapshot-shaped full state
  (`queued: []` clears a view), the re-send is idempotent by construction. Point to
  `## Reconnect / Backfill semantics`.
- The **Emission (#722)** note (L690) — "pushes on change" — stays; #878 is the *additional*
  connect-time trigger, not a replacement. Make the two read as complementary (change-driven
  push + connect-time reconcile).

### Edit site 4 (supersession note) — ADR 025 `**Backpressure / replay.**`, L130

Add a **dated superseding blockquote** immediately under the `**Backpressure / replay.**`
paragraph, in the house style of protocol-mobile.md's `> **Superseded as a requirement —
2026-06-22 (ADR 025 amendment).**` note. It must (AC5):

- Record that **control events reconcile by current-state snapshot on connect, not event-ring
  replay** — the outstanding modal (#877) and queue backlog (#878) are re-asserted as current
  state, idempotently, on every connect.
- **Explicitly supersede** the L130 wording "the binary replays from a bounded per-conversation
  event ring" **for control events specifically**.
- State that the **cursor/ring path (`last_event_id`, #647) remains for bulk transcript
  content** (`assistant_delta` and the turn-event stream). The control-never-drops promise
  (#874/#875) is unchanged.
- Point to `docs/protocol-mobile.md` § Reconnect / Backfill semantics as the authoritative
  wire contract.
- Use today's date (2026-07-10) in the note header.

### Edit site 5 (one-liner) — `### Reconnect`, L403

Append one sentence: this subsection covers reconnect *timing* (backoff/jitter); the
*application-layer* reconcile-on-connect contract (what state the daemon re-asserts) lives in
§ Reconnect / Backfill semantics. Keeps the timing subsection uncluttered while satisfying
AC1's "§ Reconnect / Backfill semantics" heading reference from the reconnect entry point.

## AC → edit-site map

| AC | Where | What must be true |
|---|---|---|
| AC1 (reconcile-on-connect rule; Modal/Queue point to it) | Site 1 (canonical) + Sites 2, 3 (pointers) + Site 5 (xref) | The rule is stated once; Modal and Queue reference/are consistent with it. |
| AC2 (match-and-replace by stable id; reset-on-reconnect) | Site 1 | Three sub-rules: known-id updates-in-place/never-double-shows, resolved-id no-op, not-re-asserted-is-gone. |
| AC3 (answer semantics unchanged under re-delivery) | Site 2 | nonce + `answer_token` = answerable exactly once; deny-on-timeout armed once at raise time, never re-armed. |
| AC4 (axis = data type, not outage length) | Site 1 | Mode A (bulk → cursor backfill + snapshot fallback) vs Mode B (control → current-state snapshot). |
| AC5 (ADR 025 supersession note) | Site 4 | Dated blockquote superseding the ring-replay wording for control events; ring path stays for bulk transcript. |

## Style & consistency constraints

- **Do not restate the mechanism.** #877/#878 own the implementation; #829/#903/#904 own the
  e2e proof; #874/#875 own the within-grace hold. Reference by ticket number, state the
  *contract*, not the *runtime* (Technical Notes).
- **Do not touch** `#### Reconnect replay & resync (consumer, #647)` or the `hello.last_event_id`
  docs — the bulk-transcript path stays as-is; the new rule is its complement.
- Reuse existing vocabulary already in the doc: "current truth", "match-and-replace",
  "reset-on-reconnect", "snapshot-shaped full state", `EventID` nil ⇒ "never in the ring".
- ADR note is **append/amend** (dated superseding blockquote), never a rewrite of the original
  L130 paragraph — the original stays readable as the superseded prior wording.
- Keep the security invariants (nonce, `answer_token`, deny-on-timeout, first-answer-wins)
  **by reference** to their existing homes (protocol § Security & validation contract, ADR §
  Security model). This ticket codifies *reconciliation*, not the security primitives — which
  is why it is **not** `security-sensitive` (docs writing a security contract carry no new
  design surface).

## Testing / verification strategy

Docs-only — no Go build/test surface. Verification is:

1. **Internal-link integrity.** Every anchor this spec adds or references resolves. Confirm the
   retitled `## Reconnect / Backfill semantics` renders and that the Modal/Queue/Reconnect
   cross-links point to it (GitHub slug: `#reconnect--backfill-semantics`). No pre-existing
   link targeted `#backfill-semantics` (verified), so retitling breaks nothing.
2. **Consistency sweep.** Grep `docs/protocol-mobile.md` for "only on the next change" and any
   claim that control state is *not* re-asserted on connect — none should survive that
   contradict the new rule.
3. **QMD re-index.** After editing, run `qmd update && qmd embed` (CLAUDE.md § Documentation)
   so the new contract is searchable.
4. **AC readback.** Re-read each AC against the five edit sites using the map above; every AC
   bullet maps to a concrete sentence in a specific section.

## Open questions

- **Retitle vs. keep `## Backfill semantics` + add a sibling `## Reconnect` section.** Spec
  chooses *retitle-and-expand* (one home, matches AC1's slash-title, no anchor breakage). If
  the developer finds a cleaner fit as two sibling sections, that is acceptable provided AC1's
  "§ Reconnect / Backfill semantics" is satisfied and Mode A/Mode B stay in one place so the
  data-type axis (AC4) reads as a single contrast. Recommendation: retitle.
- **Depth of the v2-session-manager.md cross-links.** Optional; the ticket ACs are satisfied by
  citing #877/#878. Add the feature-doc anchors only if they aid a client author — do not make
  the protocol doc depend on internal-feature-doc structure.
