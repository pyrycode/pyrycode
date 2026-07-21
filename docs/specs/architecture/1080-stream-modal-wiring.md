# Spec: Modal wiring for stream — permbridge ⇄ modalbridge round-trip (#1080)

Wire the stream-json permission bridge (`permbridge`, #1103) to the existing
client-facing modal machinery (`modalbridge`, #716/#717) so a parked approval
surfaces as the **same** `modal_shown` clients already answer, and a client's
`modal_answer` resolves claude's blocked approval tool — keyed by `modalID`.

**Size: S.** ~215 production LOC across **4 production files** (all changes
additive or single-branch); trivial edit fan-out (the two signatures that change
have exactly one caller each). Security-sensitive: an untrusted `modal_answer`
drives an allow/deny on a permission gate — see `## Security review` at the end.

---

## Files to read first

The developer's turn-1 data load. Read these before writing any code.

- `internal/permbridge/permbridge.go:121-176` — `Register` / `Lookup` / `Resolve`
  contract + the fail-closed one-shot (`resolve`). `Lookup(id)→Request` (read),
  `Resolve(id, Verdict)→bool` (one-shot consume). `Allow(req.Input)` echoes the
  parked input **byte-verbatim** (`json.RawMessage`). The registry-owned timer
  already denies on timeout — this ticket does **not** touch permbridge.
- `internal/modalbridge/modal.go:116-196` — `PermissionRequestForClass`,
  `Record(req, wireClass, convID)→(payload, err)` (mints `modal_id`, stamps the
  Outstanding), `Lookup`, `Resolve` (one-shot consume), `Snapshot`. The bridge
  reuses `PermissionRequestForClass(tuidriver.ModalClassPermission, prompt)` to
  build the exact 4-option / reject-once-default permission payload — this is how
  AC-1 byte-compatibility is guaranteed **by construction**.
- `cmd/pyry/modal_resolve_v2.go:200-291` — `ResolveAnswer`: the security-critical
  gated answer arm (Lookup → **gate** → classify → consume → actuate → audit).
  The stream arm slots in at the actuate step. `:311-338` `classifyAnswer`
  (optionID → outcome), `:270-291` the `AuthorizeRemotePermission` decision.
- `internal/control/server.go:865-908` — `handleApprove`: parks in permbridge,
  starts `watchApproveConn` (disconnect/shutdown deny), blocks on
  `pending.Await()`. The surfacer seam hooks in right after `Register`; the
  `defer retire()` backstop hangs off the post-`Await` return. `:333-338`
  `SetApprovalRegistry` — the setter shape to mirror for `SetApprovalSurfacer`.
  `:921-937` `watchApproveConn` (the two-cancellation-source watcher, unchanged).
- `cmd/pyry/interactive_modal_v2.go:98-144` — `handleModalShown` (Record → arm →
  broadcast) and `:227-251` `broadcastInteractive` (the ActiveConns/Push fan-out
  the bridge mirrors, with a mutex-guarded counter of its own). **Do not** call
  `ArmModalTimeout` for the stream modal (see § Concurrency: single timeout
  authority).
- `internal/relay/v2session_modal.go:194-231` — `handleModalAnswer` (calls
  `ResolveAnswer`, broadcasts on `ok=true`) and `broadcastModalDismissed`
  (`:251-`). The manager already broadcasts the answer-path dismissal from
  `ResolveAnswer`'s return — no manager change.
- `cmd/pyry/main.go:914-970` — composition: `approvals := permbridge.New()`
  (:921), `startRelay(relayWiring{…})` (:923), `ctrl := control.NewServer` +
  `SetApprovalRegistry` (:966-970). The 3 wiring edits land here.
- `cmd/pyry/relay.go:260-304` (`startRelay`) and `:414-442` (`startRelayV2` where
  `modalReg`, `modalResolver`, and `mgr` are constructed) — the bridge is
  constructed here and its `Surface` returned outward.
- `internal/devices/auth.go:60,89` — `MayAnswerRemotePermission()` (the gate) and
  `AuthorizeRemotePermission(d, outcome)→bool` (the fail-closed allow decision).
- Test doubles to reuse: `cmd/pyry/interactive_turn_v2_test.go:71`
  (`fakeInteractiveBcast`), `cmd/pyry/modal_resolve_v2_test.go:25`
  (`fakeKeystroker`), `internal/control/approve_test.go:1-40`
  (`startServerWithApprovalRegistry` — its doc already anticipates #1080).

---

## Context

Non-YOLO stream-json claude is spawned with `--permission-prompt-tool`; on a
blocked tool it Calls the MCP approve tool. The `pyry mcp-approve` subprocess
forwards that over the control socket → `handleApprove` parks it in `permbridge`
and blocks on `pending.Await()`. **Today nothing resolves it to allow** — every
request times out to deny (#1104 doc: "until #1080 lands there's no resolver").

This ticket closes the loop: the parked request surfaces as the same permission
`modal_shown` clients already render, and the client's `modal_answer` resolves
the parked completer to allow/deny. It **takes desktop#483's permission gate
green**.

The two primitives live in **two composition scopes** that never meet today:

| Registry | Created in | Consumed by | Keyed by |
|---|---|---|---|
| `permbridge.Registry` (`approvals`) | `main.go:921` | control server (`handleApprove`) | claude's `tool_use_id` |
| `modalbridge.Registry` (`modalReg`) | `relay.go:432` (startRelayV2) | modal emitter + `modalResolverV2` | minted `modal_id` nonce |

The design's core is a small cmd/pyry **bridge** that owns the `modal_id ⇄
tool_use_id` correlation and joins the two scopes.

---

## Design

### New component: `streamApprovalBridge` (in `cmd/pyry/modal_resolve_v2.go`)

Co-located with `modalResolverV2` (the resolver's stream arm delegates to it, and
the surfacer produces the modals the resolver resolves — one "stream-approval
modal" concern). Co-location keeps the change at **4 production files**; a
separate file would make it 5 and trip the split gate for no edit-budget gain.

It holds the correlation and both directions of the join:

```go
type streamApprovalBridge struct {
    perm       *permbridge.Registry      // claude-facing completer store (#1103)
    modal      *modalbridge.Registry     // client-facing modal store (#716)
    bcast      interactiveBroadcaster    // *relay.V2SessionManager (ActiveConns/Push)
    activeConv func() string             // follow-active convID scoping (#1065)
    ctx        context.Context           // daemon ctx captured at construction, for broadcasts
    logger     *slog.Logger

    mu      sync.Mutex
    byModal map[string]string            // modalID → toolUseID
    nextID  uint64                       // per-bridge envelope counter (mu-guarded)
}
```

Three methods (contracts, not bodies):

- `Surface(req permbridge.Request) (retire func())` — the control-server seam.
  Build a permission `PermissionRequest` (via
  `modalbridge.PermissionRequestForClass(tuidriver.ModalClassPermission,
  prompt)`), `modal.Record(req, classPermission, activeConv())` → payload+modalID,
  store `byModal[modalID] = req.ToolUseID`, broadcast `modal_shown`, and return a
  `retire` closure bound to that modalID. On `Record` RNG failure: log the
  content-free `rand_err`, store nothing, broadcast nothing, and return a no-op
  retire — claude then times out to deny via permbridge (fail-closed degrade,
  mirroring `handleModalShown`). Runs on the control-server handler goroutine.

- `retire()` (the returned closure) — the guaranteed cleanup + dismissal backstop
  for **every** terminal path, in two steps whose concerns are separate:
  1. **Unconditionally** `delete(byModal, modalID)` under `mu`. This is the sole
     correlation deleter and it runs on *every* `Await` return (answer, timeout,
     disconnect, shutdown), so the map never leaks — including the answer path,
     where step 2 no-ops. (Delete of an absent key is a safe no-op, so ordering
     against a racing delete is irrelevant.)
  2. `out, ok := modal.Resolve(modalID)`; if `!ok` a `modal_answer` already
     consumed it → **no dismissal** (no second broadcast); if `ok`, write one
     content-free audit record (fail-closed deny) and broadcast one
     `modal_dismissed`. The `modalbridge` one-shot `Resolve` is the **single
     arbiter** of the *broadcast* (not the correlation delete): exactly one of
     {`ResolveAnswer`, `retire`} broadcasts.

  Runs on the control-server handler goroutine after `Await`. **Rationale for the
  split:** the correlation delete and the dismissal broadcast have different
  arbiters. Gating the delete behind the modalbridge one-shot (as a naive "delete
  only when I win" would) leaks the map on the answer path — `ResolveStream`
  doesn't delete, and `retire`'s `Resolve` misses. Keeping the delete
  unconditional and separate is the fix.

- `ResolveStream(modalID string, allow bool, denyReason string) (handled bool)` —
  the resolver's stream arm. `toolUseID, ok := byModal[modalID]` (read under `mu`,
  release before any registry call); if `!ok` return `false` (caller routes the
  keystroke arm). If `allow`: `req, ok2 := perm.Lookup(toolUseID)`; if `ok2`,
  `perm.Resolve(toolUseID, permbridge.Allow(req.Input))` (echoes input verbatim —
  AC-2); if `!ok2` permbridge already resolved (raced timeout) — nothing to do.
  Else `perm.Resolve(toolUseID, permbridge.Deny(denyReason))`. Return `true`. Does
  **not** delete the correlation — `retire` is the sole (unconditional) deleter,
  and the modalbridge consume in `ResolveAnswer` already gates against a second
  `ResolveStream` for the same modalID. Runs on the relay Run goroutine.

`denyReason` is a fixed content-free constant (e.g. `reasonRemoteDeny =
"permission denied"`) — never host-derived (leaks nothing back to claude).

**SECURITY (bridge log discipline).** The bridge **never** logs `req.Input`, the
`tool_name`, the modal prompt/title, or the deny message beyond the fixed
constant. Permitted content-free fields mirror the control server's existing
approval log (`internal/control/server.go:906`): `event`, `modal_id`, the
`tool_use_id` correlation key (not a credential — `permbridge` doc), the wire
`class`, an allow/deny behavior discriminant, and transport sentinels. This is a
stated contract, enforced by the no-body-leak test below — not merely a test
artefact.

### `modalResolverV2.ResolveAnswer` — add the stream dispatch arm

Add an optional nil-default field (the established #1014 seam pattern — **not** a
constructor param, so the 18 `newModalResolverV2` call sites are untouched):

```go
// streamApprovalResolver: resolve a stream-json approval by modalID.
// handled=false ⇒ modalID is not a stream approval → route the keystroke arm.
type streamApprovalResolver interface {
    ResolveStream(modalID string, allow bool, denyReason string) (handled bool)
}
// on modalResolverV2:
streamApprovals streamApprovalResolver // nil ⇒ no stream approvals (foreground/v1/pre-#1080)
```

The only behavioural change in `ResolveAnswer`, at the actuate step (after
`reg.Resolve(modalID)` commits the modalbridge idempotency): compute the allow
decision once and dispatch —

- `allow := devices.AuthorizeRemotePermission(dev, outcome)` (reuse the existing
  decision that today only feeds the audit; it now feeds actuation too).
- `if r.streamApprovals != nil && r.streamApprovals.ResolveStream(modalID, allow,
  reasonRemoteDeny) { /* stream: permbridge resolved, no keystroke */ } else {
  r.routeAnswerKeystroke(verb, choice) /* tui arm, unchanged */ }`.
- Audit + return dismissal `{Outcome: optionID, Source: remote}` — unchanged. The
  gate (`MayAnswerRemotePermission`), classify, and consume steps are untouched
  and run **before** any permbridge contact.

Stream modals are always permission-class, so the existing trust-class emit
(`out.Class == classTrust`) never fires for them.

### `internal/control/server.go` — the surfacer seam

Minimal, optional, nil-degrades-cleanly (the `SetApprovalRegistry` shape):

- Field: `approvalSurfacer func(permbridge.Request) func()` (guarded by `s.mu`).
- Setter: `SetApprovalSurfacer(surface func(permbridge.Request) func())`.
- In `handleApprove`, after a successful `Register` and `SetDeadline(zero)`, hoist
  the request into a variable, then:
  `retire := func(){}; if surface := <snapshot under s.mu>; surface != nil {
  retire = surface(req) }; defer retire()`. The duplicate-`Register` early return
  is **before** this, so it never surfaces or retires. `defer retire()` fires on
  the post-`Await` return, covering allow / deny / timeout / disconnect / shutdown
  uniformly.

`permbridge.Request` already crosses into `internal/control` (server.go builds it
for `Register`), so the callback signature adds **no new import**.

### Wiring (relay.go + main.go)

- `relayWiring` gains `approvals *permbridge.Registry`.
- `startRelayV2` constructs the bridge over `(approvals, modalReg, mgr,
  active.CurrentConversation, ctx, logger)`, sets `modalResolver.streamApprovals =
  bridge`, and **returns `bridge.Surface`** as a new return value.
- `startRelay` threads the new return outward (it has exactly one caller).
- `main.go`: pass `approvals: approvals` into `relayWiring`; capture the returned
  surfacer; after `ctrl.SetApprovalRegistry(...)`, call
  `ctrl.SetApprovalSurfacer(surface)`. `ctrl` is created after `startRelay`
  returns, so the return-value order is natural — no reordering.

### Data flow

```
PARK (outbound):
  claude → pyry mcp-approve → control sock → handleApprove
    reg.Register(toolUseID, req)                       [permbridge parks; timer armed]
    retire := surface(req)  ── streamApprovalBridge.Surface ──▶
        modal.Record(...) → modalID                    [modalbridge stores Outstanding]
        byModal[modalID] = toolUseID                    [correlation]
        broadcast modal_shown  ──────────────────────▶ interactive clients
    pending.Await()                                     [blocks]

ANSWER (inbound, happy path):
  client modal_answer → relay Run → ResolveAnswer
    modalbridge Lookup → GATE → classify → modalbridge Resolve(consume)
    allow := AuthorizeRemotePermission(dev, outcome)
    bridge.ResolveStream(modalID, allow) ──▶ perm.Resolve(toolUseID, Allow(req.Input)/Deny)
    return dismissal → manager broadcastModalDismissed ─▶ clients
  ── perm.Resolve unblocks Await ─▶ defer retire(): modalbridge already consumed → NO-OP

TIMEOUT / DISCONNECT (fail-closed):
  permbridge timer OR watchApproveConn → perm.Resolve(toolUseID, Deny) ─▶ claude denied
  ── Await unblocks ─▶ defer retire(): modalbridge still present → consume + broadcast
     modal_dismissed + audit + delete correlation ─────▶ clients (no stale modal; AC-3)
```

---

## Concurrency model

- **Goroutines.** `Surface`/`retire` run on the control-server handler goroutine
  (one per approve request; **concurrent** across requests). `ResolveStream` runs
  on the relay manager's single Run goroutine (via `ResolveAnswer`). No new
  long-lived goroutine is spawned.
- **Locks.** The bridge's `sync.Mutex` guards `byModal` + `nextID` only — a leaf
  lock held around O(1) map ops and the counter bump, **never** across
  `modal.Record`, `perm.Lookup`/`perm.Resolve`, `modal.Resolve`, or a `Push`.
  Concretely: `Surface` calls `Record` *before* taking `mu` to store the
  correlation, and releases `mu` before broadcasting; `ResolveStream` reads
  `byModal` under `mu` and releases before any registry call. So there is no
  lock nesting (bridge.mu → registry.mu never occurs) and no deadlock order to
  reason about. `permbridge.Registry` and `modalbridge.Registry` each carry their
  own leaf mutex (unchanged).
- **Bounded state.** `byModal` holds one entry per *concurrently-parked* approval;
  `retire`'s unconditional delete (runs on every `Await` return) and permbridge's
  own timeout (`mcpApprovalTimeout`, 2 min) together bound it — no entry outlives
  its approval. (An unbounded *count* of concurrent parked approvals is a
  permbridge/mcp-approve concern, not this ticket's — see § Security review.)
- **Single dismissal arbiter.** `modalbridge.Resolve` is one-shot under its mutex;
  it decides who broadcasts `modal_dismissed` — `ResolveAnswer` on the answer path
  (retire then no-ops), `retire` on the timeout/disconnect path. This mirrors the
  existing local-vs-remote race arbitration in `handleModalHidden`.
- **Single timeout authority.** `permbridge`'s registry-owned timer is the sole
  timeout mechanism; the stream modal deliberately does **not** call
  `ArmModalTimeout`. Two timers would drift, and `ResolveTimeout` routes an ESC
  keystroke — which on the stream path has no on-screen modal and could inject a
  stray key into the **wrong** (bootstrap) supervisor. The `retire`-after-`Await`
  backstop provides the client dismissal instead, promptly (AC-3's disconnect path
  needs immediate retirement, not a 2-minute timer).
- **Off-Run broadcast is safe.** `ActiveConns` funnels onto the Run goroutine via
  `m.snapshot` (request/response — safe to call from another goroutine); `Push` is
  non-blocking and Run-safe (`m.queues` under `pushMu`). This is exactly how the
  emitters broadcast from the producer goroutine. The bridge broadcasts from the
  control handler goroutine the same way, with its own mu-guarded envelope counter.

---

## Error handling

| Failure | Behaviour |
|---|---|
| `Record` RNG failure | Drop the modal (no `modal_shown`, no correlation); no-op retire; claude times out to deny (fail-closed). |
| Broadcast `Push` error | Tolerated/`Debug`-logged with the transport sentinel; torn-down conn re-syncs on reconnect (`modalbridge.Snapshot` re-emits the still-outstanding stream modal). |
| Unknown / stale `modalID` on answer | `modalbridge.Lookup` misses in `ResolveAnswer` → `(zero,false)` no-op (AC-2). A modalID absent from `byModal` (non-stream) → `ResolveStream` false → keystroke arm. |
| Ungated / nil device | `MayAnswerRemotePermission()` fails **before** classify/consume/permbridge — audited `denied_unauthorized`, no permbridge contact. |
| `perm.Lookup` miss on allow (raced timeout) | Skip `Allow` — permbridge already resolved to deny; `ResolveStream` still returns true. Fail-closed (claude denied). |
| Duplicate `Register` id | `handleApprove` denies before surfacing; no modal, no retire. |

---

## Testing strategy

Bounded and fake-driven — reuse `fakeInteractiveBcast`, `fakeKeystroker`, real
`permbridge.Registry` / `modalbridge.Registry` (both stdlib leaves — no fakes),
and `startServerWithApprovalRegistry`. **No full network client harness**; the
AC-4 round-trip is exercised at the component level. Target ~5–7 focused,
table-driven cases per surface — not an exhaustive matrix.

- **Bridge** (`fakeInteractiveBcast` + real registries):
  - `Surface` records the Outstanding, stores the correlation, and broadcasts one
    `modal_shown` with the 4 permission options + reject-once default (AC-1).
  - `ResolveStream(allow=true)` resolves permbridge with `Allow` whose
    `UpdatedInput` **equals the parked `Input` byte-for-byte** (AC-2).
  - `ResolveStream(allow=false)` resolves permbridge with `Deny`.
  - `ResolveStream` on an unknown modalID → `false`, no permbridge mutation.
  - `retire` on an unconsumed modal → broadcasts one `modal_dismissed` + deletes
    the correlation; `retire` after `ResolveStream`/consume → **no** second
    dismissal (single-arbiter, AC-3).
  - **No correlation leak:** after the answer path (`ResolveStream` then `retire`)
    **and** after the timeout path (`retire` alone), `byModal` is empty. Assert on
    the bridge's map length (add a tiny test-only size accessor if needed) — this
    is the regression guard for the unconditional-delete fix.
- **`ResolveAnswer` stream arm** (`fakeKeystroker` + bridge with fakes):
  - stream allow → permbridge `Allow`, **no** keystroke; stream deny → permbridge
    `Deny`, no keystroke.
  - non-stream modalID → keystroke routed (existing tui behaviour preserved).
  - ungated / nil device → denied before any permbridge/keystroke contact (gate
    precedence).
  - no-body-leak: assert logs carry no `Input` / prompt / option bytes.
- **Control seam** (`startServerWithApprovalRegistry` + a fake surfacer): a parked
  approve invokes the surfacer once; the returned `retire` is invoked once on the
  terminal (resolve the registry to drive `Await`); nil surfacer → pre-#1080
  behaviour (times out to deny).
- **Round-trip (AC-4)**, component-level: wire bridge+resolver with fakes, drive
  `request → Surface (capture modal_shown) → ResolveAnswer(modal_answer) →` assert
  the permbridge verdict, for an **allow** answer and a **deny** answer.

Run `go test -race ./...` — the concurrent Surface/retire (control goroutine) vs
ResolveStream (Run goroutine) touching `byModal` must be race-clean.

---

## Open questions

1. **Prompt body.** The spec stamps the stream modal's prompt from `tool_name`
   (bounded by `boundPrompt`, non-secret — it names the tool being approved).
   Surfacing `tool_input` (e.g. the bash command) is more useful for the human
   decision but may carry sensitive content; deferred. Empty prompt is also
   viable (the ACP sibling `acpPermissionProxy` uses `""`). Recommend `tool_name`;
   revisit input-surfacing in a follow-up. Does not affect AC-1 (options + default
   are class-fixed) or the round-trip.
2. **convID scoping.** `Surface` stamps `active.CurrentConversation()`. Confirm the
   active cursor points at the stream runner's conversation when the approval
   parks (the follow-active assumption the rest of the modal machinery makes). An
   empty convID is a safe degrade (unscoped broadcast; security-critical fields are
   convID-independent).
3. **Envelope.ID for control frames.** The bridge mirrors the emitter's per-conn
   monotonic counter. If `Envelope.ID` proves non-load-bearing for control
   (non-replay-ring) frames, the mu-guarded counter could be dropped — verify
   against the client's control-frame handling before simplifying.

---

## Security review

**Verdict:** PASS

The threat this ticket exists to bound: an **untrusted `modal_answer` from a
network peer drives an allow/deny on a permission gate**, and claude's blocked
tool is resolved by that verdict. Walked adversarially against the spec above;
one MUST FIX was found and fixed inline before this verdict.

**Findings:**

- [Trust boundaries] No findings. Untrusted→trusted crossing is the single,
  explicit `modalResolverV2.ResolveAnswer` path: `MayAnswerRemotePermission()`
  gate → `classifyAnswer` membership → modalbridge consume →
  `AuthorizeRemotePermission` — all four conjunctive checks run **before** any
  `permbridge` contact, and every other path defaults to deny. The
  `modalID→toolUseID` correlation is minted daemon-side at `Surface` and is never
  exposed to the client, so a device answering `modalID X` can only allow the tool
  `X` was minted for — no client-supplied value selects *which* approval resolves.
- [Trust boundaries — inherited] Any *gated* device may answer any outstanding
  modal (not scoped to the device that will receive claude's result). This is the
  established #702/#717 authorization model (the gate **is** the authorization),
  unchanged and not widened by this ticket. Named, not a new finding.
- [Tokens/secrets] No findings. `modalID` is a 122-bit `crypto/rand` nonce
  (modalbridge, unchanged); `answerToken` is decoded-but-unused server-side and
  never logged (inherited `ResolveAnswer` contract); `tool_use_id` is a
  correlation key held daemon-internal, never on the wire to clients. No secret is
  persisted (the bridge is in-memory only).
- [Tokens/secrets — MUST FIX, **fixed**] The original `retire` deleted the
  `byModal` correlation only inside the `modal.Resolve` `ok` branch, but on the
  answer path that branch misses (ResolveAnswer already consumed the modal) and
  `ResolveStream` doesn't delete either — so every *answered* stream approval
  leaked its map entry forever (unbounded growth / resource exhaustion). Fixed:
  `retire` now deletes the correlation **unconditionally** on every `Await`
  return, with the modalbridge one-shot gating only the dismissal broadcast. A
  no-correlation-leak test (both answer and timeout paths) is the regression
  guard.
- [File operations] N/A — the design touches no filesystem; all state is
  in-memory (`byModal` map + the two registries).
- [Subprocess execution] N/A for this ticket. The Allow verdict echoes claude's
  own parked `Input` **verbatim** back to claude — it is a yes/no gate, not an
  input source; this code neither spawns a process nor constructs an executable
  argument. (The mcp-approve subprocess and the claude spawn are upstream/#1103,
  unchanged.)
- [Cryptographic primitives] No findings. No new crypto. The only
  attacker-controlled value compared against daemon state is `modalID` (a 122-bit
  nonce map-lookup); a constant-time compare is not required — brute-forcing a
  122-bit nonce is infeasible and a map-lookup timing side-channel yields
  negligible advantage (inherited from #717's model).
- [Network & I/O] No findings after the map-leak fix. `modal_answer` size is
  bounded by the transport AEAD frame cap; the modal prompt is bounded by
  `modalbridge.boundPrompt` (4096 B); `optionID` is length-bounded before
  logging. `byModal` is bounded by the concurrent-parked-approval count, each
  entry bounded by `mcpApprovalTimeout`.
- [Error messages / logs] SHOULD FIX, **addressed inline.** Made the bridge's log
  discipline an explicit contract (never log `Input` / `tool_name` / prompt /
  deny message; content-free discriminants only), backed by the no-body-leak
  test, rather than leaving it implied.
- [Concurrency] No findings. The bridge's `sync.Mutex` is a leaf held only around
  O(1) map/counter ops, never nested with a registry mutex or across a broadcast
  (documented lock ordering in § Concurrency). The modalbridge one-shot arbitrates
  the single dismissal; permbridge's one-shot arbitrates claude's single verdict.
  No new goroutine is spawned, so no goroutine leak. Shutdown: daemon ctx cancel
  short-circuits broadcasts; `watchApproveConn` denies parked entries; `retire`
  cleans up on the resulting `Await` return.
- [Threat model alignment] Aligned with ADR 025 § Security model ("the only path
  to allow is a fully-authorized valid answer; deny is the default"). The
  fail-closed defaults — ungated device, unknown/stale modalID, timeout,
  disconnect, shutdown, RNG failure — all deny.
- [Resource exhaustion — OUT OF SCOPE] An unbounded *count* of concurrently-parked
  approvals (a flood of distinct `tool_use_id`s → many permbridge/modalbridge
  entries + modal_shown broadcasts) is a `permbridge` / `pyry mcp-approve`
  registry-cap concern, not introduced or widened here. Deferred to whoever owns
  the parked-request admission cap (permbridge #1103 lineage).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-21
