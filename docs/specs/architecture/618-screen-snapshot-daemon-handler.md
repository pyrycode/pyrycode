# Spec — Screen-snapshot daemon handler (#618)

**Part of EPIC #596** (Phase 2 structured streaming). Consumes the snapshot wire
types from #617 (merged): `RequestSnapshotPayload` / `ScreenSnapshotPayload` +
`TypeRequestSnapshot` / `TypeScreenSnapshot`. ADR 025 § Safe degradation names
the screen snapshot the always-available, parser-independent live-view escape
hatch. This ticket wires the daemon side: intercept `request_snapshot`, render
the current claude screen to text inside the tui-driver seal, and push a
`screen_snapshot` back to the requesting connection.

**Size:** confirmed **S** (PO sized S; not overridden). 3 production files
touched, no new files, ~110 production LOC + tests, 1 new exported interface,
0 forced edits to existing call sites (the new config fields are optional —
see § Edit fan-out). No split.

---

## Files to read first

Generated from `codegraph_context` + the reads done during sizing; pruned to
what the implementer needs on turn 1.

- `internal/relay/v2session.go:998-1042` — `dispatchAppFrame`: the **interception
  point**. The `TypeRekeyRequest` probe is the exact pattern to extend; the
  reply loop shows the seal→wrap→`m.send` sequence.
- `internal/relay/v2session.go:1044-1083` — `handleRekeyRequest`: the **sibling
  intercepted-control handler** to mirror (signature shape, runs on the dispatch
  goroutine, no transport action vs. our reply).
- `internal/relay/v2session.go:1372-1412` — `handlePush`: the **single
  seal-and-forward implementation** to reuse. Call this *directly* from the
  handler (you are already on the dispatch goroutine).
- `internal/relay/v2session.go:1331-1370` — `Push` (public): the cross-goroutine
  funnel. **Do NOT call it from the dispatch goroutine** — it sends on `m.push`
  and waits for `Run`, which is busy in your handler → self-deadlock. Read this
  to understand *why* the handler uses `handlePush` instead.
- `internal/relay/v2session.go:264-316` — `V2SessionConfig`: where the two new
  **optional** fields go.
- `internal/relay/v2session.go:381-409` — `NewV2SessionManager`: confirm the new
  fields are **not** validated here (keeping them optional is what avoids the
  ~40-site test cascade).
- `internal/protocol/snapshot.go:32-50` — `RequestSnapshotPayload` /
  `ScreenSnapshotPayload` shapes (no `omitempty`; `TS` is `time.Time`, compare
  with `.Equal`).
- `internal/protocol/codes.go:18-24,121-124` — `CodeServerBinaryOffline`,
  `CodeConversationNotFound`, `TypeRequestSnapshot`, `TypeScreenSnapshot`
  (all already exist; no protocol change).
- `internal/supervisor/supervisor.go:199-221` — `WriteUserTurn`: the **`sessMu`
  capture pattern** (`s.sessMu.Lock(); sess := s.sess; s.sessMu.Unlock()`) that
  `ScreenSnapshot` mirrors, and the `sess == nil → ErrNoLiveSession` shape.
- `internal/supervisor/supervisor.go:282-301` — `setSession`: confirms `s.sess`
  is the live-or-nil hosted session; nil between iterations / when evicted.
- `internal/supervisor/winsize.go` — `resizeOnce` only fires when stdin **is** a
  TTY; a headless daemon's PTY therefore stays at tui-driver's 40×120 default,
  which is why `Render(snap, 0, 0)` is 1:1 (see § Open questions for the
  resized-foreground case).
- tui-driver `pkg/tuidriver/grid.go:36` — `Render(snap []byte, cols, rows int)
  string`; `0,0` → `DefaultGridCols`×`DefaultGridRows` (120×40). And
  `pkg/tuidriver/session.go:371` — `Session.Snapshot() []byte` (raw VT100 bytes;
  opaque, never inspected by pyrycode).
- `cmd/pyry/relay.go:260-321` — `startRelayV2`: where to wire `Snapshotter: sup`
  and the `KnownConversation` closure. `sup *supervisor.Supervisor` and
  `convReg *conversations.Registry` are already parameters; the file already
  imports both packages.
- `cmd/pyry/assistant_turn_v2.go:97-162` — `broadcast`: the **"never log the
  screen/PTY bytes"** discipline to mirror, and the `ActiveConnIDs`/`Push`
  usage the ticket says to reuse (we reuse the underlying `handlePush`).
- `internal/e2e/relay_v2_daemon_test.go:88-188` — `testV2DaemonListConversations
  RoundTrip`: the **e2e to clone**, plus `driveHandshakeToOpenDaemon`,
  `sendNoiseMsg`, `readInnerFrame`, `decryptInnerEnvelope` helpers.
- `internal/relay/v2session_test.go:98,707` — `startManager` / `driveToOpen`
  (+ `v2Recorder`): the unit-test harness to extend.
- `cmd/substrate-guard/main.go:32-72` — the banned-literal patterns; tests must
  not hardcode a claude screen glyph/anchor (assert text is *a string*, use a
  benign non-substrate sentinel in fakes).
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md`
  § "Safe degradation" + § "Security model" (line 141: read-only screen viewing
  is deliberately **outside** the per-device permission gate).

---

## Context

The phone needs an always-available "show me the literal screen" action that
survives any screen-parser break — the floor of ADR 025's degrade strategy.
The wire vocabulary landed in #617. This ticket adds the three missing pieces:
(1) a narrow supervisor seam that renders the live screen to text inside the
tui-driver seal, (2) the inbound interception of `request_snapshot` at the v2
control boundary, and (3) the push of `screen_snapshot` back to the requester.

**Why now:** #617 merged the types; the v2 push surface (`Push` / `handlePush`,
#571) and the conn snapshot (#588) already exist; the supervisor hosts claude
via a tui-driver `Session` (#593) whose `Snapshot`/`Render` are ready. Every
dependency is in `main`.

**Security-sensitive** (label present): this handler accepts an inbound frame
from a non-trusted party (a paired phone, over an internet-exposed relay) and
returns rendered screen content. The spec-stage security review is mandatory
and appended below.

---

## Design

Three seams, smallest-blast-radius first.

### 1. Supervisor render seam — `internal/supervisor/supervisor.go`

One new method. Mirrors `WriteUserTurn`'s `sessMu` capture; renders **inside the
seal** (the raw bytes from `sess.Snapshot()` are consumed by `tuidriver.Render`
in the same expression and never named in pyrycode):

```go
// ScreenSnapshot renders the current claude screen to plain text. live is
// false when no claude child is attached (between restarts / idle-evicted);
// text is "" then. Safe for concurrent use; non-blocking (a buffer read + an
// in-memory VT100 render — no I/O, no channel ops).
func (s *Supervisor) ScreenSnapshot() (text string, live bool)
```

Behaviour: capture `sess` under `sessMu`; `sess == nil` → `("", false)`;
else `tuidriver.Render(sess.Snapshot(), 0, 0), true`. `0,0` selects tui-driver's
120×40 default, matching the daemon PTY's allocation (§ Open questions covers
the resized-foreground edge). Render is total — no error path.

The invariant asserted by tests: with a live `*tuidriver.Session`, the method
returns `live == true` and a `string` (possibly empty); with no session, it
returns `("", false)`. The substrate seal holds because no claude screen literal
is named in pyrycode — `cmd/substrate-guard` stays green.

### 2. Relay interception + handler — `internal/relay/v2session.go`

**Consumer-declared interface** (keeps `internal/relay` zero-knowledge of
`internal/supervisor` / tui-driver — the relay still imports neither):

```go
// ScreenSnapshotter renders the daemon's live claude screen to plain text.
// *supervisor.Supervisor satisfies it; declared here per CODING-STYLE.
type ScreenSnapshotter interface {
	ScreenSnapshot() (text string, live bool)
}
```

**Two optional `V2SessionConfig` fields** (optional is load-bearing — see
§ Edit fan-out):

```go
// Snapshotter renders the live screen for request_snapshot. Optional: nil ⇒
// request_snapshot yields a server.binary_offline reply (feature unavailable).
Snapshotter ScreenSnapshotter
// KnownConversation reports whether a conversation_id is one this daemon hosts;
// an unknown id is rejected conversation.not_found (AC #4). Optional: nil ⇒
// every request_snapshot is rejected as not-found.
KnownConversation func(conversationID string) bool
```

`NewV2SessionManager` does **not** validate these (no panic / no error added) —
existing constructions that omit them keep today's behaviour.

**Discriminator** — extend the existing single-`if` probe in `dispatchAppFrame`
to a `switch` on `probeEnv.Type`, adding a `TypeRequestSnapshot` arm next to the
`TypeRekeyRequest` arm. One-line structural change; the JSON-decode-failure
fall-through to `dispatch.Route` is preserved.

**Handler** (runs on the single dispatch goroutine; intercepted before
`dispatch.Route`, exactly like `handleRekeyRequest`):

```go
// handleRequestSnapshot renders the current screen and pushes a screen_snapshot
// addressed to s, or a deterministic error envelope. Reuses handlePush for the
// seal+wrap+send (NOT the public Push — that would self-deadlock on the
// dispatch goroutine). SECURITY: the rendered text is NEVER logged.
func (m *V2SessionManager) handleRequestSnapshot(ctx context.Context, s *V2Session, env protocol.Envelope)
```

Control flow (each branch is deterministic and pushes exactly one reply):

1. Decode `RequestSnapshotPayload` from `env.Payload`. A decode failure is
   tolerated and leaves `ConversationID == ""`, which fails step 2 (collapses
   into the not-found reject). The decode error text is **not** echoed.
2. **AC #4** — if `m.cfg.KnownConversation == nil || !KnownConversation(conv)`
   → reply `error` (`CodeConversationNotFound`, retryable=false), return.
3. **AC #3** — if `m.cfg.Snapshotter == nil` → reply `error`
   (`CodeServerBinaryOffline`, retryable=true), return.
4. `text, live := m.cfg.Snapshotter.ScreenSnapshot()`; if `!live` → reply
   `error` (`CodeServerBinaryOffline`, retryable=true), return.
5. **Happy path** — build `ScreenSnapshotPayload{ConversationID: conv, Text:
   text, TS: time.Now().UTC()}`; wrap as `Envelope{Type: TypeScreenSnapshot,
   InReplyTo: &env.ID, …}`; deliver via `m.handlePush(ctx, s.connID, env)`.

**Reply helper** — a thin `func (m *V2SessionManager) snapshotReplyError(ctx,
s, inReplyTo uint64, code, msg string, retryable bool)` that marshals a
`protocol.ErrorPayload`, wraps a `Type: TypeError` envelope with
`InReplyTo: &inReplyTo`, and delivers via the same `m.handlePush`. Both the
success and error paths go through `handlePush` — the single existing
seal-and-forward path. No parallel send path is introduced.

Envelope `ID`: a fixed non-load-bearing value (v2 envelope IDs are sequence
hints; `InReplyTo` is the correlation key — see the `assistant_turn_v2.go`
`nextID` comment). Mirror `emitRekeyRequest`'s `ID: 1` posture; the phone
correlates on `InReplyTo`.

### 3. Production wiring — `cmd/pyry/relay.go`

In `startRelayV2`'s `V2SessionConfig` literal, add:

```go
Snapshotter:       sup, // *supervisor.Supervisor satisfies relay.ScreenSnapshotter
KnownConversation: func(id string) bool {
	_, ok := convReg.Get(conversations.ConversationID(id))
	return ok
},
```

`sup` and `convReg` are already parameters; both packages are already imported.
The closure mirrors the established `ValidateConversation` registry-membership
pattern (`internal/sessions/pool.go`), but returns a `bool` so the relay needs
no `conversations` import and no `errors.Is` coupling.

### Data flow

```
phone → noise_msg(request_snapshot{conversation_id})
   │ AEAD-decrypt (V2StateOpen) → dispatchAppFrame probe
   ▼
handleRequestSnapshot
   ├─ KnownConversation(conv)?  no → error(conversation.not_found) ─┐
   ├─ Snapshotter.ScreenSnapshot() → (text, live)                   │
   │     live? no → error(server.binary_offline) ──────────────────┤
   │     yes → screen_snapshot{conv, text, ts}                      │
   ▼                                                                │
m.handlePush(s.connID, env)  ── seal under s.send → noise_msg → m.send → relay → phone
                                                                    ▲
                              (all replies, success or error, share this path) ┘
```

---

## Concurrency model

- `handleRequestSnapshot` runs **only** on the manager's single `Run` dispatch
  goroutine (reached via `dispatchAppFrame`). No new goroutine, channel, or
  shutdown step.
- `ScreenSnapshot()` takes `supervisor.sessMu` (leaf lock, held only for a
  pointer read) then does a bounded in-memory render — no blocking I/O, no
  channel ops — so it cannot wedge the dispatch goroutine beyond a few ms.
- `KnownConversation` takes `conversations.Registry` RLock (leaf, bounded).
- Both are leaf locks in their own packages, never held across the relay's
  state; the manager has no mutex (the `Run` goroutine *is* the lock), so there
  is no lock-order interaction to document.
- Delivery uses `handlePush` **inline** on the dispatch goroutine (the same code
  `Run` invokes for an external `Push`). The public `Push` is explicitly *not*
  called here: it funnels onto `m.push` and blocks on `Run`, which is the
  goroutine currently executing the handler → guaranteed self-deadlock. This is
  the same inline-seal-and-send choreography the rekey and `dispatchAppFrame`
  reply paths already use.

---

## Error handling

| Condition | Reply | Code | Retryable |
|---|---|---|---|
| Malformed payload / empty `conversation_id` | `error` | `conversation.not_found` | false |
| Unknown / foreign `conversation_id` (AC #4) | `error` | `conversation.not_found` | false |
| `Snapshotter == nil` | `error` | `server.binary_offline` | true |
| No live session (`live == false`, AC #3) | `error` | `server.binary_offline` | true |
| Happy path | `screen_snapshot` | — | — |

- Never panics, hangs, or silently drops (AC #3): every branch pushes exactly
  one envelope and returns.
- The decode-error string is never echoed to the phone (it could reflect
  attacker-controlled bytes) — only a static message constant.
- A `handlePush` failure (unreachable here: the session is `V2StateOpen`) is
  logged at debug and the frame dropped, matching the package's outbound-drop
  posture. Not a crash.
- `ScreenSnapshot` has no error path; an empty screen renders `""` — a valid
  `screen_snapshot`, not an error.

---

## Testing strategy

`make check` (vet + race + staticcheck + **substrate-guard**) must stay green.

### Unit — extend `internal/relay/v2session_test.go`

Reuse `startManager` / `driveToOpen` + the existing seal/read helpers. New
`V2SessionConfig` fields are set **only** in these new tests (a fake
`ScreenSnapshotter` and a `func(string) bool` closure). Scenarios (each: drive
to open, send a sealed `request_snapshot`, assert the pushed reply):

- **Happy** — `KnownConversation→true`, fake snapshotter → `("rendered", true)`.
  Assert pushed envelope: `Type == screen_snapshot`, `InReplyTo == reqID`,
  payload `ConversationID == conv`, `Text == "rendered"`, `TS` recent (compare
  with `time.Time.Equal`-style, not `==`).
- **Foreign / unknown conv** — `KnownConversation→false` → `error`,
  `conversation.not_found`, `InReplyTo == reqID`.
- **No live session** — snapshotter → `("", false)` → `error`,
  `server.binary_offline`.
- **Nil seams (defensive)** — `Snapshotter == nil` (with `KnownConversation→true`)
  → `server.binary_offline`; `KnownConversation == nil` →
  `conversation.not_found`. No panic.
- **Malformed payload** — non-JSON / `{}` body → `conversation.not_found`.
- **No-log of screen text (security)** — fake returns a benign non-substrate
  sentinel (e.g. `"SENTINEL-SCREEN-XYZ"`); capture the manager's slog output to
  a buffer and assert the sentinel never appears in any log line.
- **Repeat** — two `request_snapshot`s in a row each produce a fresh
  `screen_snapshot` with a freshly stamped `TS`.

Write scenarios in the package's table-driven idiom; do **not** hardcode any
claude screen glyph/anchor (substrate-guard).

### E2E — extend `internal/e2e/relay_v2_daemon_test.go` (AC #5)

Add `testV2DaemonRequestSnapshotRoundTrip`, modelled on
`testV2DaemonListConversationsRoundTrip` and wired into the `TestRelayV2_Daemon`
`t.Run` list. The `sleep`-hosted child gives a **live** tui-driver session whose
buffer renders to (typically empty) text — `ScreenSnapshot()` returns
`("", true)` — so the render → `screen_snapshot` round-trip is exercised at the
real daemon seam without depending on `send_message` / `WaitReady`:

1. `pair`; seed `conversations.json` with a known `conversation_id` (the test
   already does this for list_conversations).
2. `StartInWithEnv(… PYRY_MOBILE_V2=1)`; `waitBinaryHello`; dial; handshake to
   open via `driveHandshakeToOpenDaemon`.
3. Send a sealed `request_snapshot{conversation_id: knownConvID}`.
4. Read + decrypt the reply; assert `Type == screen_snapshot`,
   `InReplyTo == reqID`, payload `ConversationID == knownConvID`, `Text` **is a
   string** (do not assert specific content — would couple to the child and risk
   the substrate guard), `TS` non-zero.
5. Second assertion, same session: `request_snapshot{conversation_id:
   "<foreign-uuid>"}` → `error` envelope, `conversation.not_found`,
   `InReplyTo` set (strengthens AC #4 at the daemon seam).

The substrate guard stays green: the supervisor chains `Snapshot()`→`Render()`
without naming raw bytes, and no test hardcodes a screen literal.

---

## Edit fan-out (sizing rationale)

`V2SessionConfig` is constructed at ~42 sites (1 production +
~40 in `v2session_test.go` + 1 in `relay_v2_handshake_test.go`). Making the two
new fields **optional** (no `NewV2SessionManager` validation) means **zero**
forced edits to those existing sites — they compile unchanged with the fields
nil. Only the new unit tests set them. This is what keeps the ticket inside the
10-call-site red line; a *required* field would have triggered a 40-site
cascade and forced a split.

Production source files modified: `internal/supervisor/supervisor.go`,
`internal/relay/v2session.go`, `cmd/pyry/relay.go` = **3** (< 5). New files: 0.
New exported types: 1 (`ScreenSnapshotter`). No protocol change (#617 already
added the types/constants and the `compat_test.go` partition).

---

## In-flight overlap determination

The §1.5 branch-overlap check flagged `origin/feature/449` touching
`internal/relay/v2session.go` (+ test). **Verified false positive:** #449 is
CLOSED (2026-05-17), has no open PR, and its deliverables
(`handleRekeyInit` / `handleRekeyRequest` / the `TypeRekeyRequest`
discriminator) are already in `main` and have since been extended by #453 / #571
/ #588 — `git diff --stat origin/main origin/feature/449` is 380 insertions /
2404 deletions, i.e. the branch is ~2400 lines *behind* main. `feature/449` is a
stale, already-merged branch that will never re-merge; blocking on a closed
ticket would strand #618 (a closed blocker cannot "land" to unblock). No block
set; spec written against current `main`.

---

## Open questions

- **Render dimensions on a resized foreground PTY.** `ScreenSnapshot` renders at
  tui-driver's 120×40 default. In headless/daemon mode (the relay path) the PTY
  stays at that default — `resizeOnce` only fires for a TTY stdin — so the render
  is 1:1. If a foreground operator resized via SIGWINCH, the live PTY differs and
  the rendered snapshot may word-wrap differently from what the operator sees.
  Acceptable for a degrade-floor view; threading the live size through would need
  the supervisor to track it (tui-driver exposes `Resize` but no size getter) and
  is deferred. The developer should render at `0,0` and not add size plumbing.
- **Per-conversation scoping.** This phase hosts a single bootstrap conversation;
  `KnownConversation` validates registry membership, so any registered
  `conversation_id` renders the one live screen. Precise per-conversation screen
  routing arrives with multi-conversation support (#596+). No new exposure: the
  rendered screen is the same content the phone already receives via the
  assistant-turn `message` fan-out.
- **Eager push on `stall_detected`.** ADR 025 flags "also push the snapshot on
  stall" as a Phase 2/3 sub-decision. Out of scope here — this ticket is
  on-demand (`request_snapshot`) only.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No finding (explicit decision). The only untrusted input
  is `conversation_id` (one string), arriving on a handshake-gated, token-validated
  `V2StateOpen` session. It is validated by `KnownConversation` *before* any render;
  the render itself consumes no attacker input (it renders the daemon's own screen).
  Returning screen content to the paired phone without a per-device gate is the
  operator-confirmed ADR 025 § Security model (line 141) decision — read-only screen
  viewing exposes nothing beyond the assistant-turn `message` stream the phone already
  receives. The conv_id is echoed back only when valid, only to the requester, sealed
  under the session key; the wire is JSON, so no injection/reflection vector.
- **[Tokens/secrets]** No finding. The handler generates/stores no token or key; it
  reuses the session's `s.send` CipherState through `handlePush`. `StaticPriv`,
  device tokens, and `peerStatic` are untouched.
- **[File operations]** No finding. No filesystem path is constructed from input.
  `conversations.Registry.Get` is an in-memory mutex-guarded lookup; `ScreenSnapshot`
  reads an in-memory tui-driver buffer. No path traversal / TOCTOU / file creation.
- **[Subprocess]** No finding. The handler executes nothing; it reads the buffer of
  the already-spawned claude session.
- **[Cryptographic primitives]** No finding. No new crypto. `conversation_id` is a
  non-secret routing identifier (the phone already holds it from `message`
  envelopes), so a variable-time membership lookup leaks nothing — no
  constant-time compare is warranted.
- **[Network & I/O]** SHOULD FIX / OUT OF SCOPE — request-flood DoS. Inbound is
  bounded by `maxNoisePayloadBytes` (65535) at `decodeInnerFrameV2`; output is
  bounded by the 120×40 grid (a few KB ≪ the 65519-byte envelope cap). An
  authenticated phone could loop `request_snapshot`, serialising v2 traffic behind
  per-render CPU on the single dispatch goroutine. Not gated: the peer is
  token-authenticated, each render is bounded (~ms), and this is parity with every
  other frame type (no per-frame rate limit exists in the v2 manager). No abuse
  observed; cross-cutting rate-limiting is deferred to a future ticket if/when seen.
- **[Error messages, logs, telemetry]** No finding (key control, specified + tested).
  The rendered screen text is sensitive and is NEVER logged — mirrors
  `assistant_turn_v2.go`'s chunk-bytes discipline. The handler logs only `conn_id`,
  `conversation_id` (a non-sensitive UUID, already logged elsewhere), and event
  names. Error replies carry a **static** message constant, never the decode-error
  text or the raw conv_id. A unit scenario pins the no-log invariant with a benign
  non-substrate sentinel. Code-review should grep the handler for any log field
  carrying the rendered `text`.
- **[Concurrency]** No finding. The handler runs only on the single `Run` dispatch
  goroutine; it takes `supervisor.sessMu` and `conversations.Registry` RLock
  sequentially (leaf locks in other packages, never nested), and spawns no goroutine.
  The TOCTOU window between `KnownConversation` and `ScreenSnapshot` is benign — a
  session that dies in the gap yields a deterministic `server.binary_offline`, not a
  crash or a stale render. The one real pitfall — calling the public `Push` on the
  dispatch goroutine self-deadlocks the whole manager — is forbidden by the spec
  (use `handlePush`) and flagged in three places; code-review should confirm no
  `m.Push(` appears inside `handleRequestSnapshot`.
- **[Threat model alignment]** No finding. Honors ADR 025 (snapshot outside the
  per-device permission gate; foreign/unknown `conversation_id` rejected; only
  token-validated `V2StateOpen` sessions reach the handler). OUT OF SCOPE —
  per-conversation screen isolation: in this single-bootstrap-conversation phase,
  `KnownConversation` validates *registry membership*, so any registered id renders
  the one live screen; this is correct now (single operator, single screen, no
  cross-tenant boundary) but the multi-conversation phase (#596+) MUST tighten the
  render to the named conversation's session. Recorded in § Open questions so the
  future implementer does not mistake registry-membership for per-conversation
  authorization.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-08
