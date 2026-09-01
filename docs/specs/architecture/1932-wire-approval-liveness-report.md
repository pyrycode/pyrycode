# #1932 — wire the daemon's approval-liveness report into permbridge's timer

Slice of #1912. Consumes #1931 (`Registry.SetAnswerable`) and #1915
(`streamApprovalBridge.ApprovalAnswerable`), both of which shipped unwired.

**Size: S.** Three production source files (`cmd/pyry/relay.go`,
`internal/control/server.go`, `internal/e2e/internal/fakeclaude/main.go`), one
new statement of behaviour-changing code, ~310 lines of total written work, no
new exported types, one consumer call site.

---

## Files to read first

| Path | Symbol | What to extract |
|---|---|---|
| `internal/permbridge/permbridge.go` | `expire`, `SetAnswerable`, `AnswerableFunc` | The seam's contract: called with **no registry lock held**, from the `time.AfterFunc` goroutine, **must not panic**, read as a level and re-asked every window. `expire` re-arms *the same* window on a positive reading. |
| `cmd/pyry/relay.go` | `startRelayV2` | The `w.approvals != nil` branch — the one wiring site. Note its inner `w.busy != nil` guard: `bridge.toolCallInFlight` and `w.approvalParked.set(...)` live there; this ticket's assignment does **not**. |
| `cmd/pyry/modal_resolve_v2.go` | `ApprovalAnswerable` | The report itself. Its doc states the two load-bearing halves, the parked-first ordering, and **"NEVER CALL THIS FROM THE RELAY `Run` GOROUTINE"**. Read all of it before wiring. |
| `cmd/pyry/modal_resolve_v2.go` | `retire`, `broadcast` | `retire` writes the `audit.Log` record (`outcome=denied_timeout`, `source=timeout`, `modal_id=…`) that the reworked e2e gates on, and broadcasts `modal_dismissed` only to conns live at that moment. |
| `internal/control/server.go` | `handleApprove`, `watchApproveConn`, `SetApprovalRegistry` | The five doc claims that go false (§ Doc-claim rewrites). Also: `handleApprove` logs `control: approval resolved` with `tool_use_id` + `behavior` — content-free, keep it that way. |
| `internal/e2e/relay_v2_stream_modal_test.go` | `TestRelayV2_StreamModalPermissionRoundTrip` | The case table, the `nextEnv` single-reader discipline, and the drain loop's needle / forbidden-needle assertions. |
| `internal/e2e/handshake_interactive_helpers_test.go` | `buildHelloEarlyInteractive`, `driveHandshakeToOpenDaemonInteractive` | The handshake helpers. **Nine specs call the drive helper — add a sibling, do not widen its signature.** |
| `internal/e2e/relay_v2_rekey_test.go` | `waitForLog` | The existing `*safeBuffer` log-polling helper, same package. `h.Stderr` is a `*safeBuffer`. |
| `internal/relay/v2session_replay.go` | `replayMissed` | Replay runs only when the hello carried `last_event_id`, and only for `cursor()`'s conversation. |
| `internal/eventring/ring.go` | `After` | `After(conv, 0)` on a non-evicted ring returns the **whole** retained tail with `gap == false`. This is why the reconnecting phone needs no event-id bookkeeping. |
| `cmd/pyry/interactive_turn_v2.go` | `emit` | `ring.Append` happens **before** the `ActiveConns` fan-out, so events are retained even when no phone is connected — the property the reconnect-replay witness rests on. |
| `internal/relay/v2session.go` | `dispatchFrame`'s `default:` arm, `idleTimeout` | An unknown inner-frame type on an open session ⇒ `closeWith(StatusProtocolMismatch)`, deleting the session on the Run goroutine. `idleTimeout` is 15 minutes and has no env seam. Both matter — see § The disconnect problem. |
| `internal/e2e/internal/fakeclaude/main.go` | `approveDialTimeout`, `dialApproval` | The 30 s margin comment to update, and the three needle constants. |
| `docs/protocol-mobile.md` § "Error codes", close-code row **4408** | — | *"The relay↔binary leg is a single multiplexed WebSocket with **no per-connection disconnect frame**, so a phone that drops or backgrounds is only detectable by inbound-frame silence."* This sentence is the reason the obvious e2e design does not work. |
| `docs/knowledge/features/permbridge-package.md` § "Conditional bound", § "Trust boundary" | — | The injected report's honesty is a documented trust input; a report that always answers `true` removes the fail-closed bound. |

---

## Context

`internal/permbridge`'s registry parks every approval under a fail-closed
deadline. #1931 replaced the unconditional timer-deny with `expire`, which
consults an injected `AnswerableFunc` and re-arms the same window on a positive
reading — then shipped it with nothing calling `SetAnswerable`, so today every
approval still denies on the window.

#1915 landed the report the seam was built for. `ApprovalAnswerable` is the
conjunction of "still parked on a person" and "at least one interactive-capable
client is connected", keyed on claude's `tool_use_id` — which is also the
registry id, because the control server registers each approval under
`Request.ToolUseID`. Same signature as `AnswerableFunc`, same id space, no
adapter.

This is the slice where behaviour changes on a running daemon: a permission
prompt keeps waiting while somebody can still answer it, instead of denying
itself while its human is walking to their desk.

Waiting is not the unsafe state. A non-YOLO headless `claude` blocks on the
verdict for the whole park, so the tool cannot execute while parked, and
interactive `claude` has no deadline at all. What the deadline bought was turn
termination, and #1911 has taken that over properly.

**This design deserves no ADR.** It is one assignment discharging a contract
#1931 wrote down and #1915 built for. Everything decided here belongs in
`docs/knowledge/features/permbridge-package.md` and
`docs/knowledge/features/control-plane.md`; the documentation phase owns both.
The one thing it should carry forward is § The fail-closed bound is now
`idleTimeout + window`, which is a genuinely new system property.

---

## Design

### 1. The wiring — one statement, in the outer branch

In `startRelayV2`, inside `if w.approvals != nil`, alongside
`modalResolver.streamApprovals = bridge` and `surface = bridge.Surface`:

```go
w.approvals.SetAnswerable(bridge.ApprovalAnswerable)
```

Three things the accompanying comment must record, because each is a decision a
reader will otherwise re-litigate:

**Why the outer branch and not the inner `w.busy != nil` guard.** The two
assignments under that guard are there because a method value on a nil
`*turnBusyTracker` is a non-nil func that panics on first call.
`ApprovalAnswerable` reads the bridge's own modal correlation and the
broadcaster — never the turn-busy tracker — so gating it on `w.busy` would
silently disable the extension in PTY mode for no reason at all.

**Why no nil guard and no wrapper closure.** The guarding duty `AnswerableFunc`
assigns to the injection site is discharged **structurally** here, not with a
runtime branch: `bridge` was just constructed on the line above and is
non-nil, and its `bcast` is `mgr`, also non-nil. This mirrors
`ApprovalAnswerable`'s own decision to carry no `b.bcast` nil guard. Do **not**
add a defensive `func(id string) bool { if bridge == nil { … } }` wrapper —
it would be unreachable code that makes a future genuine nil read as "nobody
can answer", which is the exact failure mode the seam's doc forbids.

**Why the registry outliving the relay leg is safe.** The registry is minted at
the composition root and handed to the control server; `startRelay` returns
early when `relayURL == ""`, so `SetAnswerable` is never called and the window
stays the hard deadline it has always been (AC-2's relay-disabled arm — no code
needed). At daemon teardown `ActiveConns` returns nil once the ctx is cancelled
or `Run` has exited, so the report answers `false` and every parked approval
denies within one window. The installed method value is safe to call for the
whole life of the registry, which is what the seam demands.

No new late-bound holder is needed — unlike #1919's `approvalParkedReport`, both
operands are already in scope at this one site.

### 2. Doc-claim rewrites in `internal/control/server.go`

Five written claims go false. Rewrite them where they stand (#1909-style) —
this is not comment tidying, it is the same edit as the code.

| Site | The claim that dies | What replaces it |
|---|---|---|
| the `approvals` / `approvalTimeout` field doc | "the registry's own timer denies after it" | the window is a **re-check interval**: the registry denies on it only while nobody can answer, and re-arms it otherwise |
| `SetApprovalRegistry` doc | "timeout bounds every pending approval … so a wait never outlives timeout" | timeout bounds a wait **nobody can answer**; with a report installed a wait ends within one window of the first reading that says so |
| `handleApprove`'s deadline-clearing comment | "the wait is bounded by permbridge's registry-owned timer" | the conn deliberately carries no deadline; the registry-owned timer bounds the wait only once the report reads unanswerable — and #1929 removed the client's own bound for the same reason |
| the inline `// guaranteed to return within timeout` on the `Await` call | outright false | name the real bound: returns within one window of the first unanswerable reading |
| `watchApproveConn` doc | "does not park a pending entry for the full approval timeout" | it maps caller disconnect and daemon shutdown into a deny — the two cancellations `Await` cannot observe — which matters *more* now that the elapsed-time bound is conditional |

**Keep stated, because it stays true and is the security core:** "Every terminal
path other than an explicit resolver `Allow` yields deny." Extension is not a
terminal path.

`cmd/pyry/main.go` (`approvalTimeout`, `mcpApprovalTimeout`) and
`internal/relay/v2session_modal.go` state the old bound too. **Those are
#1933's. Do not touch them.**

### 3. `fakeclaude`'s `approveDialTimeout` comment

The comment explains that the 30 s margin sits above the daemon window so a
no-answer deny is provably the daemon's timer and not a fakeclaude
self-timeout. That holds now only when nobody can answer the approval. Name
that condition; do not change the constant.

---

## The disconnect problem — read this before writing the e2e

The obvious rework ("the phone closes, so the daemon stops counting it as an
answerer") **does not work**, and the reason is a written protocol contract
rather than a fake-tier gap. `docs/protocol-mobile.md`'s close-code 4408 row:

> The relay↔binary leg is a single multiplexed WebSocket with **no
> per-connection disconnect frame**, so a phone that drops or backgrounds is
> only detectable by inbound-frame silence.

Confirmed in code, three ways:

- `fakerelay`'s `handlePhone` deletes the phone from its own map and sends the
  binary nothing.
- The manager's `Run` treats `<-m.cfg.Reconnect` as a drain wake only — it does
  **not** tear sessions down, so even force-closing the binary leg leaves stale
  sessions in `m.sessions`.
- The only sweep is `idleTimeout`, a package `var` of **15 minutes** with no env
  seam.

So a closed phone stays in `ActiveConns` for up to 15 minutes — far past
`approveDialTimeout` (30 s). Any design that waits for a WS close to register
will hang and land the forbidden `approve-error` needle.

**The one prompt, deterministic, phone-driven way a session leaves
`ActiveConns`** is a daemon-initiated close: `dispatchFrame`'s `default:` arm
rejects an unknown inner-frame type with `closeWith(StatusProtocolMismatch)`,
deleting the session on the Run goroutine and logging
`v2.state.reject` / `reason=unknown_inner_type`. The reworked case uses that,
and says so in a comment — the case is honest that it *ends* the session rather
than pretending a WS close was observed.

### The fail-closed bound is now `idleTimeout + window`

Stating the consequence plainly, because it is the real security delta and it is
larger than the ticket body assumes: since a vanished phone is invisible for up
to `idleTimeout`, an approval whose only answerer physically disappeared can stay
parked for up to **15 minutes plus one window** before denying. It cannot park
forever — the idle sweep is unconditional and the report is re-asked every
window — and nothing executes while it is parked. It is nonetheless the honest
bound and belongs in the package overview the documentation phase writes.

---

## Concurrency model

No new goroutines. One new cross-goroutine call path:

```
permbridge time.AfterFunc goroutine
  └─ Registry.expire            (reads r.answerable under mu, RELEASES mu)
       └─ bridge.ApprovalAnswerable
            ├─ b.mu  (leaf) — scan byModal for the tool_use_id, release
            └─ bcast.ActiveConns(b.ctx)  ── blocking round-trip ──▶ relay Run goroutine
```

Four properties to preserve, none of which needs new code — they need the
assignment to stay exactly where § 1 puts it:

- **The timer goroutine is not `Run`.** `ApprovalAnswerable` deadlocks the
  manager if called from `Run`; `time.AfterFunc` spawns a fresh goroutine per
  firing, so this path is safe. It is a property of *where the call sits*, not a
  licence to consult the report elsewhere.
- **No lock is held across the round-trip.** `expire` releases `Registry.mu`
  before `ask`; `ApprovalAnswerable` releases `b.mu` before `ActiveConns`. Two
  leaf locks, neither nested, neither held across a hand-off.
- **At most one `expire` in flight per entry.** `expire` re-arms strictly after
  `ask` returns, so a slow report stretches the window rather than racing itself.
- **Polling cost.** One `ActiveConns` round-trip per parked approval per window.
  At the production default (10 minutes) that is nothing; at the e2e's 2 s it is
  one hand-off every two seconds for one approval.

---

## Error handling / failure modes

| Mode | Behaviour | Where it is decided |
|---|---|---|
| Relay disabled (`relayURL == ""`) | `SetAnswerable` never called ⇒ nil report ⇒ deny on the window, existing fixed message | `startRelay`'s early return — no new code |
| Foreground / v1 (`w.approvals == nil`) | Branch not entered ⇒ nil report ⇒ deny | unchanged |
| Report installed, no interactive conn | `ApprovalAnswerable` false ⇒ deny with `reasonTimeout` | `expire` |
| Approval never surfaced (`Surface`'s `modal.Record` failed) | parked half false ⇒ deny, without paying the round-trip | `ApprovalAnswerable`'s parked-first ordering |
| Caller (`pyry mcp-approve`) disconnects | `watchApproveConn` denies — unchanged, and now the *only* bound on a caller that vanished while an answerer is present | `watchApproveConn` |
| Daemon shutdown | `watchApproveConn` denies via `closedCh`; independently `ActiveConns` returns nil after `Run` exits | both |
| Unknown / duplicate id | `Register` rejects with `ErrDuplicateID` ⇒ handler default-denies; report never consulted | `handleApprove` |
| `Run` wedged, `ActiveConns` never answers | `expire` blocks in `ask`; the entry stays parked, holding no lock. Fail-*open* in the "does not deny" sense, but nothing executes and #1911 terminates the turn | accepted; noted in § Security review |

Exactly one caller still writes each verdict: `resolve`'s delete-under-`mu`
is untouched by this ticket, and the extension path writes no verdict at all.

**Logging:** add none. `handleApprove`'s existing `control: approval resolved`
line already carries only `tool_use_id` + `behavior`. Do not log the report's
reading, the tool name, the tool input, or a deny message (AC-5).

---

## Testing strategy

### Unit

None added in `cmd/pyry`. `startRelayV2` is composition wiring behind a live
relay connection with no existing unit harness; standing one up to observe a
single assignment would be a larger change than the ticket. The two halves are
already unit-pinned: `permbridge`'s extension scenarios (#1931) and
`ApprovalAnswerable`'s own tests (#1915). What is genuinely unpinned — that the
two are *connected* — is only observable end to end, which is what the e2e
below is for. Say this in the PR rather than leaving it implicit.

### e2e — `TestRelayV2_StreamModalPermissionRoundTrip` (`internal/e2e`, `e2e` tag)

Keep `allow` and `deny` exactly as they are. Add one case, rework one.

**New case `extended` — AC-1, and the mandated RED for this ticket.**

- `PYRY_APPROVAL_TIMEOUT=2s`; one interactive, `--allow-remote-permissions`
  phone stays connected throughout.
- After `modal_shown` lands, wait past **two** windows (~5 s) before sending
  `modal_answer{allow_once}`. Add an `answerDelay time.Duration` column to the
  case table; the existing cases leave it zero.
- Expect needle `approve-allow`; forbid `approve-deny` and `approve-error`;
  expect `modal_dismissed{allow_once, remote}`.
- **Why it is the RED:** with `SetAnswerable` unwired the approval denies at
  t=2 s, the fake reflects `approve-deny`, and the forbidden-needle assertion
  fires. It goes green only when the wiring is live. Confirm this by reverting
  the one-line assignment locally before you call the ticket done.
- 5 s sits well inside `approveDialTimeout` (30 s) and inside the existing 25 s
  drain deadline.

**Reworked case `timeout` — AC-3.** Same table row (`answer: ""`,
`approvalTimeout: "2s"`, needle `approve-deny`, forbidden `approve-allow` /
`approve-error`), different body after `modal_shown`. Add a per-case
discriminant (e.g. `loseAnswerer bool`) rather than branching on `tc.name`.

Scenario, in order — every step gated on a positive observation, no sleeps:

1. `modal_shown` observed as today. The approval is parked **and** answerable —
   this is the state AC-1 protects, and it is the "was answerable at the window"
   half of AC-3.
2. Phone A ends its session: send one inner frame of an unknown `type` (not a
   `noise_msg`), which `dispatchFrame`'s `default:` arm rejects. Then close the
   WS. Gate on the daemon's own `v2.state.reject` log line so the next step
   cannot start while the session is still open.
3. Wait for `retire`'s audit record in `h.Stderr` — a **single line** carrying
   both `outcome=denied_timeout` and `modal_id=<shown.ModalID>`. This is the
   deny proof, tied to this case's modal rather than to any audit record.
4. Re-dial as a fresh interactive conn advertising `last_event_id: 0`, and drain
   the replay: require `approve-deny`, forbid `approve-allow` and
   `approve-error`, and `t.Fatalf` on a `protocol.TypeResync` marker naming ring
   eviction, so a future retention change fails legibly instead of confusingly.
   **The second conn needs its own `sendCS` / `recvCS` pair** — v2 has no session
   resumption, so the reconnect is a fresh Noise_IK handshake and carrying the
   first conn's cipher states across desyncs the AEAD nonce into a decrypt
   failure that reads as a daemon bug. `nextEnv` closes over `recvCS`, so it
   needs re-binding to the new state rather than reuse.
5. Keep the existing `tool_use` join assertion — the frame is replayed too,
   since `writeAssistantToolUse` runs before the approve dial.

Three things this shape buys, worth writing into the test's doc comment:

- **The re-extension hazard is structurally excluded.** Step 4 is gated on step
  3, so the returning conn cannot be an answerer for an approval that is already
  resolved. The ticket flags reconnecting before the window boundary as
  load-bearing; gating on the deny is what makes it so.
- **`modal_dismissed` is genuinely unobservable here** — it is a bridge
  broadcast, not a ring event, and it went out while nobody was connected. Its
  vocabulary is not lost, it moves to step 3's audit line, which carries the
  same `denied_timeout` / `timeout` pair.
- **`approve-error` discrimination survives**, which the audit line alone could
  not give: a masked client-side failure means the fake's `control.Approve`
  errored, `watchApproveConn` denied, and `retire` wrote a byte-identical audit
  record. Only the needle separates the two, which is why step 4 exists at all.

**Two new helpers, both additive.**

- In `handshake_interactive_helpers_test.go`: a sibling of
  `buildHelloEarlyInteractive` taking a `*uint64` `LastEventID`, and a sibling
  of `driveHandshakeToOpenDaemonInteractive` that uses it. Have the existing
  pair delegate with `nil`. **Do not widen the existing signatures** — nine
  specs call the drive helper and this ticket must not fan out into them.
- In the modal spec file (not the shared helper file — one caller): a
  line-scanning counterpart to `waitForLog` that waits for a single `h.Stderr`
  line containing *all* of several substrings. `waitForLog`'s whole-buffer
  `strings.Contains` would pass on two unrelated lines.

**What this suite does not cover, and why that is right.** AC-2's arms are
already pinned: "no report installed ⇒ deny on the window, byte-identical" is
`permbridge`'s own unit test from #1931, and the relay-disabled /
foreground paths are the existing v1 and foreground specs, which this ticket
leaves untouched because `startRelay` returns before the wiring site.

### Gates

`make check` covers the fake-tier e2e. The ticket carries `needs-real-claude`
because it changes when a real permission prompt denies on the live
`--permission-prompt-tool` path: run `make e2e-realclaude` and **read the count
of `=== RUN` lines**, not the exit code. The live permission suite always
answers and already asserts `Source == "remote"` specifically, so it should be
unaffected — this change only makes that attribution harder to fake. **Do not
add a live test that sits out the ten-minute default window.**

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No MUST FIX — one explicit boundary, one named
  consequence.** The report is a process-internal Go value the composition root
  chooses, never anything a network peer supplies, so the seam itself accepts no
  untrusted input. The id crossing it is `Request.ToolUseID`, already a
  daemon-side correlation key rather than a credential; `ApprovalAnswerable`'s
  membership scan carries no id-specific branch, so unknown, never-surfaced,
  already-resolved and empty ids all reach one `false` through the same path and
  the report stays no existence oracle. **The enforcement is the signature**
  (`func(string) bool`): any later widening to `(bool, error)`, or to anything
  returning a modal id, a conn id or a count, reintroduces the oracle silently.
  **Named consequence:** a *paired, authenticated* device that stays connected
  and never answers now holds an approval parked for as long as it stays
  connected. That is the requested behaviour, not a hole — an unauthenticated
  peer cannot do it, because `ActiveConns` excludes sessions still handshaking
  or token-unvalidated, and nothing executes while parked.
- **[Tokens, secrets, credentials] Not applicable, by design.** No token,
  secret, key or credential is created, stored, compared, logged or rotated
  anywhere in this design. `RoutingEnvelope.Token`'s MUST-NOT-log rule is
  untouched because no logging is added at all. The e2e's reuse of
  `payload.Token` across the reconnect is a test fixture on a fakerelay, not a
  production lifecycle change.
- **[File operations] Not applicable, by design.** No path is constructed,
  canonicalised, opened, created or written. `permbridge` is an in-memory
  registry; the wiring writes one field.
- **[Subprocess execution] Not applicable, by design.** No `exec.Command`, no
  new spawn argument, no environment change. `fakeclaude`'s edit is a comment;
  its `approveDialTimeout` constant stays 30 s.
- **[Cryptographic primitives] SHOULD FIX — a developer trap in the reworked
  e2e, not in production.** No production crypto changes. But the reconnect in
  the `timeout` case is a **fresh Noise_IK handshake** — v2 has no session
  resumption (`docs/protocol-mobile.md` § "Client requirements"). The developer
  must mint and use a **new `sendCS` / `recvCS` pair** for the second conn and
  must not carry the first conn's cipher states or nonce counters across; reusing
  them desyncs the AEAD nonce and produces a decrypt failure that looks like a
  daemon bug. Code review should check this specifically.
- **[Network & I/O] SHOULD FIX — note, do not gate.** Longer parks mean the
  registry's `pending` map, its per-entry timer, its buffered channel, the
  blocked `handleApprove` goroutine, its `watchApproveConn` goroutine and the
  open Unix-socket conn all live longer: previously bounded by `timeout`
  (10 min), now by `idleTimeout + timeout` (≈25 min worst case).
  `permbridge.Register` has no concurrent-approval cap. This is **not remotely
  reachable** — approvals are minted only by `claude` through `pyry mcp-approve`
  over the local control socket, one per gated call, and anyone holding that
  socket already holds the daemon. No cap is added here; if one is ever wanted
  it belongs in `permbridge`, not at this wiring site. Also noted: one
  `ActiveConns` round-trip per parked approval per window is new load on the
  `Run` goroutine — negligible at the 10-minute production default.
- **[Error messages, logs, telemetry] No findings.** No log line is added
  (AC-5); `permbridge` stays log-free. The existing `control: approval resolved`
  line carries `tool_use_id` + `behavior` only, and `retire`'s audit record
  carries opaque ids plus fixed vocabulary. The reworked e2e asserts on that
  **existing** vocabulary rather than requiring anything new to be logged, so
  the test does not pull content into the log to make itself observable.
  Explicitly forbidden: a "report says answerable" debug line carrying the tool
  name, the tool input, or a deny message.
- **[Concurrency] No MUST FIX — one accepted DoS shape, one accepted
  invariant.** Lock order: two leaf locks (`Registry.mu`, `streamApprovalBridge.mu`),
  neither nested with the other, neither held across the `ActiveConns` hand-off
  or the channel send. TOCTOU: `expire`'s check-release-ask sequence is
  non-atomic by design and made safe by its re-check under `mu` before
  `timer.Reset`, so no timer is ever re-armed for an entry a concurrent
  `Resolve` deleted — inherited from #1931, unchanged here. Goroutine lifecycle:
  none added; `time.AfterFunc` spawns one per firing which exits when `expire`
  returns. Shutdown: `ActiveConns` returns nil once the daemon ctx is cancelled
  or `Run` has exited, so teardown reads as "nobody can answer" — fail-closed —
  and `watchApproveConn`'s `closedCh` arm denies independently.
  **Accepted DoS shape:** a wedged `Run` goroutine blocks `ask` and leaves the
  approval parked indefinitely. Not new exposure — `ActiveConns` is already
  consulted on every `Surface` and `retire` — and it holds no registry lock, so
  `Register` / `Resolve` / `Lookup` stay live for every other approval. The
  ordering constraint that would *create* a deadlock (calling the report from
  `Run` itself) is excluded because the caller is the timer goroutine.
  **Accepted invariant:** the seam has no `recover()` by deliberate design — a
  panicking report must kill the daemon rather than read as "every approval
  silently denies" — and the no-panic obligation is discharged *structurally*
  (`bridge` and its `bcast` are non-nil by construction at the single wiring
  site), not by an assertion. A future edit that moves the assignment above
  `newStreamApprovalBridge`, or reorders `startRelayV2` so `mgr` is built later,
  turns it into a nil-method-value panic on the first expiring approval. The
  mitigation is § 1's comment naming the invariant, **not** a runtime guard: a
  guard would convert a loud crash into a silent universal deny, which is
  strictly worse.
- **[Threat model alignment] No MUST FIX — one named weakening.** Against
  `docs/protocol-mobile.md` § Security model: no new wire message, no new
  handshake path, no change to the capability gate — the report reuses the same
  `CapabilityInteractive` gate broadcast already applies, so a conn that could
  never receive a `modal_shown` is never counted as an answerer. The fail-closed
  core is intact: allow stays reachable by exactly one path, an explicit
  `Resolve(id, Allow(...))` winning `resolve`'s delete-under-`mu`; the extension
  path writes no verdict, so it cannot flip a deny, double-resolve, or displace
  `resolve` as the sole arbiter. `AnswerableFunc`'s documented hazard — a report
  pinned `true` — is structurally unreachable: `ApprovalAnswerable` is a
  conjunction whose parked half goes false the moment `retire` deletes the
  correlation and whose connected half goes false when `Run` exits.
  **Named weakening:** the fail-closed window is no longer `timeout`. It is
  `timeout` while unanswerable, and up to `idleTimeout + timeout` (≈15 minutes
  plus one window) when the only answerer vanished without the daemon observing
  it, because the relay↔binary leg carries no per-connection disconnect frame
  (§ The disconnect problem). Accepted: nothing executes while parked, interactive
  `claude` has no bound at all, turn termination is #1911's job, and the bound is
  finite because the idle sweep is unconditional and the report is re-asked every
  window. It must be written down — § The fail-closed bound names it for the
  documentation phase to fold into
  `docs/knowledge/features/permbridge-package.md`.
- **[Test-only seams] No findings.** No new production env seam.
  `PYRY_APPROVAL_TIMEOUT` is pre-existing and only shortens the window, which
  after this ticket shortens the *re-check interval* — the daemon asks more
  often, the safe direction. `last_event_id: 0` is documented input `Ring.After`
  classifies as the fresh-consumer case; it reads only the daemon's own retained
  events for the daemon-resolved conversation (`replayCursor`), never one the
  phone names.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-09-01

---

## Open questions

1. **Does the unknown-inner-type close land promptly enough?** The design says
   the daemon deletes the session on the Run goroutine as soon as it dispatches
   the frame. Verify with the `v2.state.reject` log gate before building the
   rest of step 4 — it is step 2's whole premise. If it does not fire, stop and
   report rather than substituting a sleep: a sleep-based "the phone is probably
   gone by now" turns AC-3's proof into a flake.
2. **Does the re-dial need a distinct device label?** `fakephone.Dial`'s last
   argument is the `x-pyrycode-device-name` header; the pairing token is what
   authenticates. Reusing `"phone-a"` should be fine — if fakerelay collides on
   it, pass `"phone-a2"`. Not worth investigating in advance.
3. **Ring retention.** Step 4's `resync` assertion exists so this fails legibly
   if the turn produces more events than `MaxEventsPerConversation`. Do not
   pre-emptively tune retention; let the assertion tell you.
