# ADR 025 — Mobile as a full remote head for an interactive Claude Code session

## Status

**Accepted** for the load-bearing decisions (drive model, wire versioning, remote-permission model), operator-confirmed across the 2026-06-06 / 2026-06-07 design sessions. **Implementation is phased.** Phase 1 (foundation) is in flight; Phases 2 and 3 are tracked as epics that decompose into XS/S children when their prior gate is met on live claude. Extends ADR 024 (Noise_IK mobile E2E); does not change the transport or the `noise_msg` frame.

## Context

### The vision

The Pyrycode mobile app should become a real remote head for a live, interactive Claude Code session — not a chat client that shows final answers. From the phone, a person drives their own running session over the relay with the full interactive experience:

- assistant output, streamed as it is produced;
- a thinking indicator that reflects when claude is working;
- every tool use claude makes (shell command, file read/edit), shown as it happens;
- modals, dialogs, and permission prompts, surfaced to the phone, answerable from the phone, with the answer routed back into the session;
- the queued-message backlog (messages typed while claude is busy).

The end state is "more or less full use of a normal Claude Code session, from the phone, through the relay."

### The drive-model forcing function

The drive model is the **interactive terminal, parsed by tui-driver** — NOT claude's structured `stream-json` / headless (`claude -p`) path. This is decided and not reopened.

The reason is cost. The headless path is expected to flip from Max-subscription-covered to pay-per-token metering around June 2026. An always-on personal session on the metered path is not economical. The interactive terminal session is covered by the Max subscription. So the daemon must keep hosting claude in a real terminal, and tui-driver must turn that terminal into the structured events the phone needs. Keeping the terminal also preserves `pyry attach`, the local bridge into the same running session.

Consequence: tui-driver is the single home for all claude screen knowledge. The substrate seal already enforces this — no claude screen literal may appear in pyrycode, and `cmd/substrate-guard` fails the build if one does. This work extends tui-driver from "deliver a prompt and confirm it committed" to "expose a structured event stream parsed from the live screen," without reopening a parse-able raw seam in pyrycode.

### Where things stand — building blocks and the gap

- The daemon hosts claude through `internal/supervisor` (a PTY via `creack/pty`). This is **separate** from `internal/agentrun/ptyrunner` (the `pyry agent-run` runner, already migrated to tui-driver). The supervisor does NOT use tui-driver today: raw `pty.Start` + `io.Copy` + a raw `WriteUserTurn`.
- `Supervisor.WriteUserTurn` is fire-and-forget: if no child is attached it drops the turn and returns success — no ready-gate, no commit-confirm, no recovery. The conversation cursor (`currentConvID`) and its survival across restarts are good and must be kept.
- tui-driver v1.0.1 (sealed) already has everything the structured stream needs: a `Session` (Spawn, DeliverPrompt, Snapshot, QuietFor, typed keystrokes, RecordTo), a unified `Session.Events()` stream emitting idle / thinking / modal-shown / modal-hidden / mcp-failure / network-failure plus per-entry JSONL events and end-of-turn, and JSONL/modal parsers: `AssistantText`, `IsEndTurn`, `DetectModalClass`, `ParseAskUserQuestion`, `ParsePicker`, `ParseMcpStatus`, `Render`, `Answer` / `AcceptTrust` / `Navigate` / `SendEsc`. The v1.0.0 seal removed the raw seams (`Buffer`, `PTY`, `SpawnOpts.Mirror`), so a streaming-output surface must be added deliberately rather than reusing a raw seam.
- The coarse round-trip foundation already shipped (closed 2026-06-06): the binary↔relay handshake retirement (#569 → #581 / #582 / #583), the concurrency-safe v2 session push surface (#571), and the v2 assistant-turn bridge that fans **finished** assistant turns to phones as `message` envelopes (#572 → #588 conn-ID snapshot + #589 bridge). mobile #336 (session boundaries) and #337 (live streaming) were re-pointed onto #589.
- The v2 (Noise) session manager `V2SessionManager` (`internal/relay/v2session.go`) now has a push surface (#571's `Push` + #588's conn-ID snapshot). The Run goroutine remains the single owner of each session's `send` CipherState; a push funnels onto that goroutine the way `manualRekey` already does.
- The `permission_protocol_v2.1.158.json` file named in the original prompt does not exist. The real artifacts are the 2.1.143 fixture set plus `docs/knowledge/features/permission-protocol-spike.md`. Its key finding: `--permission-prompt-tool stdio` produced NO structured permission event and `--allowed-tools` was advisory under the headless argv. That is the headless path we are NOT using. In the interactive terminal, a permission prompt renders as a real modal (`ModalClassPermission`, anchor "Do you want to proceed") that tui-driver already detects and can answer. The null finding corroborates the drive-model decision: the screen-modal flow is the viable path.

### Key architectural insight — the brittleness split

Two distinct sources feed the event stream, with very different reliability:

- **JSONL-sourced (robust):** assistant text, tool-use, tool-result, end-of-turn. claude writes these as structured JSON to the session file; tui-driver tails it. No screen scraping. A schema change is rarer and louder (JSON parse fails visibly).
- **Screen-sourced (brittle):** thinking/idle spinner, modal class and content. These depend on anchor strings ("Do you want to proceed", spinner glyphs) that claude can change on a UI update (the #124 spinner history).

This split drives the safe-degradation strategy below and means tool-use and text streaming are NOT at the mercy of screen parsing — only thinking-state and modal detection are.

## Decision

1. **Drive model = interactive terminal hosted by the daemon, parsed by tui-driver.** Not headless `stream-json`. Cost rationale above. tui-driver stays the single home for screen knowledge; `cmd/substrate-guard` stays green.

2. **Wire versioning: v2-additive + capability negotiation.** The Noise transport and the `noise_msg` frame are unchanged. The interactive events are additive application message types. The phone advertises an `interactive` capability in `hello`; the binary emits interactive events only to phones that advertised it. Old and new interoperate — no re-pair, no hard cutover. This matches the v2 spec's own rule that additive optional envelope types stay within a major version. (The "v3" label in the spec referred to permission *scoping* / tiered authority, which stays deferred; what we add is permission *surfacing and answering*.)

3. **Remote-permission model: per-device opt-in + deny-on-timeout.** Answering a permission/trust prompt is a capability granted at pair time (`pyry pair --allow-remote-permissions`, default OFF). A non-permitted phone can see prompts but cannot grant them. An unanswered prompt auto-DENIES on timeout, never auto-grants. Each answer is bound to a one-time modal nonce (idempotent, replay-safe). Destructive classes need an explicit second confirm tap on the phone.

4. **Phasing with explicit gates** (below). Each gate is checked on live claude before the next phase decomposes.

## Architecture

```
        claude (real terminal, Max-sub)                      pyry attach
                 │  PTY bytes + session JSONL              (local terminal head)
                 ▼                                                  ▲
        ┌──────────────────────────────────────┐   raw mirror bytes │
        │  tui-driver  (ALL screen knowledge)   │───────────────────┘
        │  Session: Spawn / DeliverPrompt       │
        │  Events(): idle·thinking·modal·       │  (substrate seal: no claude
        │            jsonl·tool·text·end·stall   │   literal ever leaves here)
        │  Answer / Navigate / SendEsc          │
        └──────────────────────────────────────┘
                 │ typed Event stream + DeliverPrompt + sealed attach mirror
                 ▼
        ┌──────────────────────────────────────┐
        │  daemon: internal/supervisor          │  hosts claude via tui-driver
        │   + conversation cursor (kept)        │  Session across the restart loop
        │  cmd/pyry: event→envelope bridge      │  maps Events()→wire envelopes
        │  internal/relay: V2SessionManager     │  + push surface (#571) per phone
        │   + modal control loop (Phase 3)      │  + queue, permission gate
        └──────────────────────────────────────┘
                 │ noise_msg (AEAD-sealed application envelopes)
                 ▼
        ┌──────────────┐   ciphertext + routing only   ┌──────────────┐
        │ content-blind │ ◄────────────────────────────►│    phone     │
        │     relay     │      (sees nothing inside)     │ render + act │
        └──────────────┘                                 └──────────────┘
        two-way loop: phone → send_message / modal_answer / dequeue / interrupt
                      binary → turn_state / assistant_delta / tool_* / modal_shown
```

The phone never receives raw screen bytes; it receives typed wire events derived from tui-driver's `Events()`. Raw bytes flow ONLY to the local `pyry attach` head, through a sealed tui-driver mirror that carries opaque bytes the consumer must not parse — so the substrate guard stays green.

## The event model

`Session.Events()` already emits the kinds the phone needs. The bridge maps them to wire envelopes:

- **Thinking / idle** (screen-sourced, brittle): `EventKindPtyThinking` / `Idle` → `turn_state`.
- **Assistant text** (JSONL-sourced, robust): `AssistantText` → coalesced `assistant_delta` (per JSONL message or ~250 ms, not per token).
- **Tool use / result** (JSONL-sourced, robust): new `ParseToolUse` / `ParseToolResult` helpers → `tool_use` / `tool_result`.
- **End of turn** (JSONL-sourced): `IsEndTurn` → `turn_end`.
- **Modal shown / dismissed** (screen-sourced, brittle): `DetectModalClass` + `Render` → `modal_shown` / `modal_dismissed`.
- **Stall** (safe-degrade): not-idle + PTY quiet + no JSONL progress → `stall_detected`.

## Wire-protocol extension (v2-additive, capability-negotiated)

Transport unchanged: every frame is a `noise_msg` carrying the existing `{id, type, ts, payload, in_reply_to}` application envelope, AEAD-sealed. The 65519-byte envelope cap and the per-session monotonic `id` (resets after rekey) are unchanged. New application types only.

**Capability negotiation.** `hello.payload.capabilities: ["interactive"]` from the phone; `hello_ack.payload.capabilities: [...]` from the binary. An old phone gets the Phase-1 coarse `message` envelopes; an `interactive` phone gets the structured stream.

**Binary → phone (events):**
- `turn_state` — `{conversation_id, state: "thinking"|"responding"|"idle"}`.
- `assistant_delta` — `{conversation_id, turn_id, seq, text}`. Incremental text, coalesced.
- `tool_use` — `{conversation_id, turn_id, tool_use_id, name, input_summary}`.
- `tool_result` — `{conversation_id, turn_id, tool_use_id, is_error, result_summary}`.
- `turn_end` — `{conversation_id, turn_id}`.
- `modal_shown` — `{conversation_id, modal_id, class, title, prompt, options:[{id,label,description}], default_option_id, multi_select}`. `modal_id` is a one-time nonce.
- `modal_dismissed` — `{conversation_id, modal_id, reason: "answered"|"timeout"|"local"|"cancelled"}`.
- `queue_state` — `{conversation_id, queued:[{queued_msg_id, text, ts}]}`.
- `stall_detected` — `{conversation_id}`.

**Phone → binary (control):**
- `send_message` — unchanged. Queued by the daemon when claude is busy.
- `modal_answer` — `{conversation_id, modal_id, option_id|option_ids[], answer_token}`. Bound to the current `modal_id`; a stale id is rejected so the phone re-syncs; `answer_token` (phone UUID) makes re-apply a no-op.
- `modal_cancel` — `{conversation_id, modal_id, answer_token}`. ESC / deny.
- `dequeue_message` — `{conversation_id, queued_msg_id}`.
- `interrupt` — `{conversation_id}`. Maps to `Session.SendEsc`.

**Backpressure / replay.** The per-session push queue is bounded and event-class-aware: `assistant_delta` is coalescable and drop-oldest under pressure (the phone backfills the full turn on reconnect); control events (`modal_shown`, `turn_end`, `tool_*`) never drop. On mid-turn reconnect the phone sends `hello` with `last_event_id`; the binary replays from a bounded per-conversation event ring, or emits a resync marker and the phone re-fetches via the existing `backfill_since`.

The authoritative shapes land in `docs/protocol-mobile.md` per implementing ticket (matching how #569 amended the spec), so the spec never drifts ahead of the code.

## Security model — remote permission granting (default-safe)

1. **Per-device opt-in.** `pyry pair --allow-remote-permissions` marks a device permitted to answer permission/trust/destructive modals; default OFF. A non-permitted phone receives `modal_shown` but its `modal_answer` for a gated class is rejected with an error envelope (server-side enforcement).
2. **Deny-on-timeout.** An unanswered prompt is answered with the SAFE default (deny / ESC) after a bounded window. Never auto-grant.
3. **Idempotent + fresh.** `modal_answer` is bound to the `modal_id` nonce the binary minted when it surfaced the modal. A stale id (the modal already changed) is rejected; `answer_token` makes a duplicate a no-op. This defeats reorder/replay: a captured answer cannot grant a different later modal.
4. **First-answer-wins across two heads.** If the local `pyry attach` terminal answers a modal, the binary emits `modal_dismissed{local}`; the phone's pending answer becomes stale and is rejected.
5. **Destructive needs explicit confirm (phone UX).** Write/execute/delete classes require a second confirm tap; single-tap grant is reserved for benign classes.
6. **Audit.** Each remote answer is logged locally (device id, class, decision, time); never on the wire beyond the answer itself. Keys/tokens never logged.

This is permission *answering*, default-safe. Tiered permission *scoping* (a phone with less authority than the desk) stays the deferred v3 concern.

## Safe degradation when a screen parser breaks

- **Thinking spinner breaks:** the JSONL deltas still flow, so the phone infers "responding" from delta arrival; degrades to "no explicit spinner," not silence.
- **Modal anchor breaks (the dangerous case):** the turn hangs waiting for input that was never detected. `QuietFor` + watchdog detect a stall (PTY quiet while not-idle and no JSONL progress) and emit `stall_detected`; the phone is told the session may be waiting and can open the live view. Not a silent hang.
- **Version pinning:** tui-driver's `make e2e` against a new claude version is the canary; the version-drift discipline already enforces live verification. A `parser_uncertain` / stall signal is the in-band degrade marker.

## Rationale

### Why the terminal, not headless stream-json

Cost. The headless path is expected to meter pay-per-token around June 2026; an always-on personal session on it is uneconomical, while the interactive session is Max-sub covered. The terminal model also preserves `pyry attach` and keeps a single home (tui-driver) for screen knowledge. The permission spike's null finding (no structured permission event under headless argv) independently confirms the screen-modal flow is the viable path.

### Why v2-additive + capability negotiation, not a v3 hard cutover

ADR 024 chose a hard cutover for E2E because v1 had no install base. That does not apply here: there are paired v2 phones in the field, and the interactive events are purely additive. Capability negotiation lets old and new phones share one binary with no re-pair and no dual-wire-shape confidentiality compromise. The spec already allows additive optional envelope types within a major version.

### Why per-device opt-in + deny-on-timeout

Remote permission granting is a real risk surface: a remote user could grant a destructive permission, or a network reorder could replay an answer onto a later modal. Default-OFF per-device opt-in keeps an ordinary paired phone unable to grant. Deny-on-timeout ensures the failure mode of a dropped/late answer is "claude was denied," never "claude was silently granted." The one-time nonce + answer token make answers idempotent and replay-safe. Destructive-confirm and first-answer-wins close the remaining UX and two-heads races.

## Alternatives considered

### A. Headless `stream-json` as the drive model

Gives a clean structured stream with no screen parsing. Rejected on cost: the headless path is expected to meter around June 2026, and it loses `pyry attach`. The whole point of this ADR is to stay on the Max-sub-covered interactive session.

### B. v3 hard cutover (re-pair everyone) for the new events

Symmetric with ADR 024. Rejected: there is a paired install base now, the events are additive, and a hard cutover buys nothing the capability flag does not.

### C. Auto-grant on timeout (or no per-device gate)

Rejected outright: it makes the dangerous failure mode (silent grant of a destructive permission) the default. Default-safe means deny-on-timeout and default-OFF.

### D. Permission *scoping* (tiered authority) now

A phone with strictly less authority than the desk head. Useful, but a larger design (authority tiers, per-class policy). Deferred to v3. This ADR delivers permission *surfacing and answering* with a binary per-device gate, which is the smaller, default-safe step.

### E. Phone parses raw screen bytes itself

Rejected: it would either leak claude screen literals into the phone/pyrycode (breaking the substrate seal) or duplicate tui-driver's parsing on the phone. tui-driver stays the single home; the phone renders typed events.

## Consequences

- **The supervisor migrates onto tui-driver.** The daemon's raw `pty.Start` + `io.Copy` host loop and fire-and-forget `WriteUserTurn` are replaced by a tui-driver `Session` + `DeliverPrompt`. This is the load-bearing change and the main risk (open risk below). The conversation cursor and restart survival are preserved.
- **A new sealed mirror surface on tui-driver** carries opaque raw bytes to the local attach head only, so `pyry attach` keeps working without reopening a parse-able seam. The phone never sees raw bytes.
- **The protocol grows ~13 additive application types** behind a capability flag; the transport and frame are untouched, so ADR 024's confidentiality property is preserved.
- **Remote permission answering becomes possible**, default-safe, behind a per-device pair flag. Tiered scoping stays a v3 concern.
- **Screen-modal detection is a brittle seam.** `stall_detected` is the safety net, not a fix; a claude UI change can still degrade the permission UX to "open the live view."
- **The spec follows the code.** `docs/protocol-mobile.md` is amended per implementing ticket, never ahead of it.

## Phasing, gates, and ticket map

**Phase 1 — foundation: reliable pipe + coarse round-trip.**
The coarse finished-turn round-trip already shipped (#569 / #571 / #572 + splits #581 / #582 / #583 / #588 / #589, closed 2026-06-06). The remaining Phase 1 work is the supervisor → tui-driver migration + reliable delivery + two-heads attach:
- tui-driver **#136** (T1) — sealed local-attach mirror surface (opaque-bytes-no-parse).
- pyrycode **#593** (T2) — supervisor hosts claude via `tuidriver.Spawn`; Session owned across the restart loop. *blocked-by #136.*
- pyrycode **#594** (T3) — `WriteUserTurn` via `Session.DeliverPrompt` (ready-gate + commit-confirm + recovery). *blocked-by #593.*
- pyrycode **#595** (T4) — rewire `pyry attach` onto the mirror; phone + local terminal coexist. *blocked-by #136, #593.*

Gate: a paired phone sends a message to the live daemon over the encrypted channel and sees claude's reply stream back, reliably, and `pyry attach` works concurrently. `make check` (pyrycode) + `make e2e` (tui-driver) green.

**Phase 2 — structured streaming.** Epic pyrycode **#596**, *blocked-by #594*. Decomposes into: tui-driver tool-use/tool-result JSONL helpers + stall signal; pyrycode interactive event types + capability negotiation; the event-stream bridge (replaces #589's coarse fan-out) + delta coalescing; backpressure/droppable-delta policy; mid-turn reconnect replay; mobile thinking indicator + tool-use timeline + reconnect client. Re-point mobile #336 / #337 onto the event-stream-bridge child.
Gate: the phone renders thinking + tool-use + incremental text from the structured stream; a screen-parser break degrades to streamed text, never silence.

**Phase 3 — interactive: modals, permissions, queue.** Epic pyrycode **#597**, *blocked-by #596*. Decomposes into: tui-driver permission-modal parse + serializer + safe modal-answer; pyrycode modal/permission wire types + nonce/idempotency + modal control loop; the remote-permission security model (`--allow-remote-permissions`, deny-on-timeout, audit; security-sensitive); queued-message backlog (daemon queue + wire types); two-heads ownership; `interrupt` / `SendEsc`; mobile render+answer+queue+interrupt.
Gate: a live permission prompt surfaces + is answered from the phone (gated, deny-on-timeout, idempotent) and routes back as the correct keystroke; queued messages drain; two heads coexist with first-answer-wins.

## Open risks

- The supervisor → tui-driver migration (#593) is the load-bearing change; if the attach mirror surface (#136) cannot be made seal-clean, the whole approach needs a rethink. Mitigation: #136 carries opaque bytes only, no parsing in pyrycode.
- Screen-modal detection is the brittle seam; `stall_detected` is the safety net, not a fix.
- Per-session push backpressure under a slow relay must never block the daemon dispatch goroutine; the droppable-delta policy is the guard and needs a real load test.

## Verification

- **Per ticket:** test-first (RED → GREEN). pyrycode gate = `make check` (vet + race + staticcheck + `cmd/substrate-guard`). tui-driver gate = `make e2e` (+ `make check`). No GitHub CI. The substrate guard must stay green on every pyrycode change.
- **Phase 1 end-to-end:** with `PYRY_MOBILE_V2=1`, run the pairing pre-flight, pair a device, send a message, confirm the reply streams back, and confirm `pyry attach` works at the same time. The v2 e2e oracle to extend is `internal/e2e/relay_v2_daemon_test.go`.
- **Live claude is the only honest oracle** (it self-updates); each phase's gate is checked on live claude, not just fixtures.

## Related

- ADR 024 — [`024-noise-ik-mobile-e2e.md`](024-noise-ik-mobile-e2e.md). This ADR extends it; transport and frame unchanged.
- Spec: [`docs/protocol-mobile.md`](../../protocol-mobile.md) — amended per implementing ticket.
- Permission spike: [`docs/knowledge/features/permission-protocol-spike.md`](../features/permission-protocol-spike.md) — the headless null finding.
- Shipped foundation: #569 / #571 / #572 and splits #581 / #582 / #583 / #588 / #589 (all closed 2026-06-06).
- Phase 1 tickets: tui-driver #136; pyrycode #593, #594, #595. Phase 2 epic #596; Phase 3 epic #597.
- Substrate seal: `cmd/substrate-guard`; tui-driver v1.0.1 sealed surface.
