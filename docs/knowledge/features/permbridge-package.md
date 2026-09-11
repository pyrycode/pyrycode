# `internal/permbridge` — pending tool-approval registry (Streamrunner Interactive permission bridge)

The daemon-side registry that lets a synchronous permission prompt from a non-YOLO headless `claude` be parked, surfaced for a human decision, and answered (or safely defaulted to deny) **without hanging the turn**. First slice (#1103, split from #1079) of the Streamrunner Interactive permission bridge; the T1 protocol spike (#1075) proved the mechanism this package backs: `claude` spawned with `--permission-prompt-tool mcp__<server>__<tool>` synchronously calls a registered MCP tool and blocks on its allow/deny JSON for the full duration of the pending approval (the spike measured an 11.5 s turn behind an 8 s-delayed approval). claude's own deny path does **not** hang the turn, so a lost caller or an elapsed deadline must resolve to deny — fail-closed / default-deny is the security-critical core of the whole mechanism.

This package shipped the registry **primitive only**, unwired and unit-tested in isolation (mirroring how `internal/modalbridge` shipped ahead of its own consumers). Consumers:

- **#1104 (landed)** — the control-socket `mcp.approve` verb (`internal/control`) forwards claude's request into `Register` and serializes the `Await`ed verdict back to the `pyry mcp-approve` subcommand. See [control-plane.md § Approve](control-plane.md#approve-mcpapprove-verb--forward-to-permbridge-block-default-deny-1104) and [codebase/1104.md](../codebase/1104.md). It was the first production caller of `Register`/`Await`; until #1080 landed there was no resolver, so every requested approval timed out to deny.
- **#1105 (landed)** — the `pyry mcp-approve` stdio subcommand forwards claude's `tools/call approve` through #1104's verb into this registry. See [pyry-mcp-approve-command.md](pyry-mcp-approve-command.md).
- **#1106 (landed)** — spawn-arg injection + mcp-config generation (`cmd/pyry/mcp_config.go`) that points a non-YOLO claude spawn at the #1105 subcommand in the first place (`--permission-prompt-tool mcp__pyry_approve__approve --mcp-config <path>`). Doesn't call the registry directly; completes the enforce-vs-skip switch that makes #1104/#1105 reachable at all. See [codebase/1106.md](../codebase/1106.md).
- **#1080 (landed)** — the `modal_shown ↔ modal_answer` wiring: `cmd/pyry`'s `streamApprovalBridge` calls `Lookup`/`Resolve` from a client's decision, threading the *same* `*Registry` instance #1104 created at the `cmd/pyry` composition root (`runSupervisor`), not a second one. See [v2-session-manager.md § Stream-json approval bridge](v2-session-manager.md#stream-json-approval-bridge--the-verdict-arm-1080) and [codebase/1080.md](../codebase/1080.md).
- **#1932 (landed)** — wires `SetAnswerable`: `cmd/pyry/relay.go`'s `startRelayV2` installs `bridge.ApprovalAnswerable` as the registry's `AnswerableFunc`, so #1931's conditional bound (below) is live in production rather than shipped-dormant. See § Conditional bound and [control-plane.md § Approve](control-plane.md#approve-mcpapprove-verb--forward-to-permbridge-block-default-deny-1104).
- **#2343 (landed)** — adds an opt-in stdio consumer for interactive daemon
  children. `stdioPermissionHandler` registers each `can_use_tool` ask in this
  same registry and returns the correlated verdict to the child that originated
  it; the MCP consumer remains the default rollback transport. See
  [config-package.md](config-package.md#stdio_permission_prompt--interactive-permission-transport-2343).

Spec: [`specs/architecture/1103-permbridge-registry.md`](../../specs/architecture/1103-permbridge-registry.md). Ticket record: [codebase/1103.md](../codebase/1103.md).

**The timer's bound was narrowed from unconditional to conditional.** `Register`'s original deadline denied unconditionally: waiting past the window was itself treated as the unsafe state. It wasn't — a non-YOLO headless `claude` blocks on the verdict for the whole park, so the tool cannot run while parked, and interactive `claude` has no deadline at all (the Agent SDK's permission callback "can stay pending indefinitely"). `Register`'s timer callback now routes through an unexported `expire`, which consults an injected `AnswerableFunc` and re-arms the same window when someone can still answer, spending the deadline only on an approval nobody can answer. #1931 shipped this unwired, exactly like the original primitive shipped in #1103; **#1932 wired it** — `cmd/pyry/relay.go`'s `startRelayV2` installs `cmd/pyry`'s `streamApprovalBridge.ApprovalAnswerable(approvalID string) bool` as the report (same signature, same id space — claude's `tool_use_id` — so the method value assigns with no adapter), in the outer `w.approvals != nil` branch rather than the inner `w.busy != nil` guard that gates the two turn-busy-tracker assignments: `ApprovalAnswerable` reads the bridge's own modal correlation and the broadcaster, never the turn-busy tracker, so gating it on `w.busy` would have silently disabled the extension in PTY mode for no reason. No nil guard or wrapper closure is installed either — `bridge` and its `bcast` are non-nil by construction at that single wiring site, discharging `AnswerableFunc`'s no-panic obligation structurally; a defensive `if bridge == nil` wrapper would be unreachable code that turns a future genuine nil into "nobody can answer", which is the exact silent-universal-deny failure mode the seam's contract forbids. A nil `w.approvals` (relay disabled, or v1/foreground) means `SetAnswerable` is never called, so the window stays the hard unconditional deadline it always was. Spec: [`specs/architecture/1931-answerable-approval-window-extension.md`](../../specs/architecture/1931-answerable-approval-window-extension.md) — see especially § "Why a non-positive window is never extended" and § "Why at most one `expire` is ever in flight per entry" — and [`specs/architecture/1932-wire-approval-liveness-report.md`](../../specs/architecture/1932-wire-approval-liveness-report.md) for the wiring site.

## Why `internal/permbridge` is self-contained

Unlike [`internal/modalbridge`](modalbridge-package.md), which must document a relay-import hazard (`internal/relay` imports it, so it must not import `internal/relay` back), `internal/permbridge` imports **nothing** from `internal/` at all — only stdlib (`encoding/json`, `errors`, `sync`, `time`). No import cycle is possible with any future consumer. The package doc states this invariant directly rather than documenting a hazard to avoid.

It is also **log-free** — a pure data structure with no logger field, so it cannot leak a request's tool input, an id, or a deny message into logs. Content-free decision logging is the consumer's job, matching how `acp_permission.go` logs only discriminants.

## Domain types (wire contract, envelope deferred to #1104)

```go
// Request is the tool-approval request claude sends (T1 spike contract).
type Request struct {
    ToolName           string          `json:"tool_name"`
    Input              json.RawMessage `json:"input"` // opaque, preserved verbatim
    ToolUseID          string          `json:"tool_use_id"`
    DecisionReason     json.RawMessage `json:"decision_reason,omitempty"`
    DecisionReasonType string          `json:"decision_reason_type,omitempty"`
    BlockedPath        string          `json:"blocked_path,omitempty"`
    Description        string          `json:"description,omitempty"`
    DefaultToNo        bool            `json:"default_to_no,omitempty"`
}

// Verdict is the allow/deny decision claude accepts.
type Verdict struct {
    Behavior     string          `json:"behavior"`               // BehaviorAllow | BehaviorDeny
    UpdatedInput json.RawMessage `json:"updatedInput,omitempty"` // allow only
    Message      string          `json:"message,omitempty"`      // deny only
}

const (
    BehaviorAllow = "allow"
    BehaviorDeny  = "deny"
)
func Allow(updatedInput json.RawMessage) Verdict // {BehaviorAllow, updatedInput, ""}
func Deny(message string) Verdict                // {BehaviorDeny, nil, message}
```

`Input`/`UpdatedInput` are `json.RawMessage` so an arbitrary tool-input object round-trips byte-verbatim — the #1080 stream verdict arm (`streamApprovalBridge.ResolveStream`) always echoes the parked `Input` back as `UpdatedInput` unmodified on allow; the primitive only carries the bytes. The five optional ask-context fields are independent Claude-authored display values (#2346): the stdio adapter copies each only from its corresponding `can_use_tool` field, never from `Input`, while the approval MCP producer leaves all five at zero values. They do not participate in the verdict or timeout. `omitempty` gives the two disjoint verdict wire shapes claude expects. `Allow`/`Deny` constructors make call sites correct-by-construction — no stringly-typed `"allow"`/`"deny"` at the resolver. `reasonTimeout` (unexported, a fixed string, never host-derived content) is the deny message the timer path uses.

## Registry surface

```go
type Registry struct { /* leaf sync.Mutex + map[string]*pending */ }
func New() *Registry

func (r *Registry) Register(id string, req Request, timeout time.Duration) (*Pending, error)
func (r *Registry) Resolve(id string, v Verdict) bool
func (r *Registry) Lookup(id string) (Request, bool)
func (r *Registry) SetAnswerable(ask AnswerableFunc)

type Pending struct { /* ch <-chan Verdict */ }
func (p *Pending) Await() Verdict

// AnswerableFunc reports whether the approval parked under id still has somebody
// able to answer it. Consulted only when a window elapses; a true reading buys
// exactly one more window and is then re-asked.
type AnswerableFunc func(id string) bool
```

- **`Register`** parks `req` under `id`, arms the fail-closed deadline (`time.AfterFunc(timeout, …)`), and returns the caller's handle. Rejects an empty `id` or a duplicate live `id` with `ErrDuplicateID` (`errors.Is`-matchable; caller default-denies). The timer callback routes through an unexported `expire`, not straight to a deny — see § Conditional bound below.
- **`Resolve`** satisfies the pending request `id` with `v`; returns `true` if it resolved a live entry, `false` if the id is unknown or already resolved — a safe no-op.
- **`Lookup`** returns the parked `Request` for `id` without resolving it — the read seam `streamApprovalBridge.ResolveStream` (#1080) uses to build `Allow(req.Input)`. Unknown id → `(Request{}, false)`, also a safe no-op.
- **`SetAnswerable`** (#1931) installs the liveness report, or clears it with `nil` — a setter rather than a `New` option, because the daemon composition root builds the registry long before the thing that can answer the question exists. It does not validate its argument: `nil` is a meaningful value ("nobody can answer"), not an error. Written under `Registry.mu`; the expiry path reads it under the same lock, so last write wins and calling it concurrently with live entries is safe.
- **`Pending.Await`** blocks until resolved and returns the verdict. With no `AnswerableFunc` installed (the default, and the state this ships in) it is guaranteed to return within the `Register` timeout, exactly as before. With one installed, it returns within one window of the first reading that says nobody can answer — an approval whose answerer stays reachable and simply never decides stays parked indefinitely, by design (see § Conditional bound).

Exported surface: 4 types (`Request`, `Verdict`, `Registry`, `Pending`), one func type (`AnswerableFunc`), funcs `New`/`Allow`/`Deny`, sentinel `ErrDuplicateID`.

## Conditional bound: `expire` and `AnswerableFunc` (#1931)

The registry no longer treats the timer as an unconditional deadline. `Register`'s `time.AfterFunc` now fires `expire(id, timeout)` instead of resolving straight to deny:

1. Under `mu`: is `id` still live, and what is `r.answerable`? Release `mu`.
2. Entry absent → return (already resolved by a winning `Resolve`; the report is never consulted for a dead entry — the common negative must not pay a cross-goroutine round-trip to reach an answer it cannot change).
3. No report installed, or a non-positive window → `resolve(id, Deny(reasonTimeout))`.
4. Call `ask(id)` with **no lock held** — in production this is a blocking round-trip onto the relay manager's `Run` goroutine, so `mu` (a leaf lock) is never held across it.
5. `false` → `resolve(id, Deny(reasonTimeout))`.
6. `true` → under `mu`, re-check `id` is *still* present, then `p.timer.Reset(window)` (the same `timer`, not a fresh `AfterFunc` — `resolve` reads `p.timer` outside `mu` to call `Stop()`, so the field is written once in `Register` and never reassigned).

Each positive reading buys exactly one more window and is re-asked at the next expiry — a single stale "yes" can never buy an unbounded wait, because the report is a level the consumer is expected to re-read, not an edge latched once. The re-arm happens strictly *after* `ask` returns, so at most one `expire` is ever in flight per entry regardless of how slow the report is — a slow report stretches the window rather than racing itself via `Timer.Reset`'s documented "may run concurrently with the prior callback" hazard. `resolve`'s `delete`-under-`mu` is untouched by any of this: the extension path writes no verdict at all, so it remains the sole arbiter of who satisfies `p.ch`.

**A dishonest report defeats the bound.** A report that always answers `true` converts the fail-closed deadline into an unbounded park, including for a lost caller that never `Await`s. `AnswerableFunc`'s contract is therefore explicit: **answer false once nobody is waiting on this approval.** `expire` deliberately does not `recover()` a panic from `ask` — a nil-bridge method value panicking must kill the daemon, not read as "nobody can answer," which is the failure mode a swallowed panic would produce.

## The one-shot: `resolve` — the security core

Both `Resolve` and the timer callback funnel through one unexported method:

```
resolve(id, v):
    mu.Lock()
    p, ok := pending[id]
    if !ok { mu.Unlock(); return false }   // lost the race / unknown / already resolved
    delete(pending, id)                    // ← the one-shot arbiter: delete-under-lock
    mu.Unlock()
    p.timer.Stop()                         // idempotent; harmless from the timer's own call
    p.ch <- v                              // buffered(1) + provably single writer ⇒ never blocks
    return true
```

`delete(pending, id)` under `mu` is the **sole arbiter**. Exactly one of {`Resolve`, timer} finds the entry present and becomes the single writer of `p.ch`; the other finds it gone and is a no-op. Because `ch` is buffered(1) with a provably-single writer, the send never blocks, so doing it after unlocking is safe. `Register` holds `mu` across the whole insert (timer arm + map write), so even a 0-duration timer that fires instantly blocks in `resolve` on `mu.Lock()` until the entry is fully installed — no fire-before-install race, and the lock establishes the happens-before for the timer's later `Stop()`.

This mirrors `cmd/pyry/acp_permission.go`'s existing default-deny idiom (every non-`selected` path routes deny, a one-shot arbiter resolves at most once) but arbitrates via **delete-under-mutex** instead of an `atomic.Bool` CompareAndSwap, because the map already needs the lock.

## Concurrency model

Four goroutine roles touch the registry: (1) the caller/verb goroutine — `Register` then `Await`, one per parked request; (2) the resolver goroutine (`streamApprovalBridge.ResolveStream`/`retire`, #1080, calling `Resolve` from the relay `Run` goroutine and the control-server handler goroutine respectively); (3) the per-entry timer goroutine (`time.AfterFunc`, `Stop()`ped when `Resolve` wins so it never fires in the resolved-in-time case); (4) a concurrent `Lookup` reader. All shared state is `Registry.pending`, guarded by the **leaf** `Registry.mu` — held only around O(1) map ops, never nested with another lock, never held across the channel send. Each `pending.ch` is buffered(1), written exactly once by the one-shot winner, read at most once by `Await`.

Consumer-side lifecycle tracking needs the same per-registration identity. The
stdio adapter's `stdioPermissionHandler` may accept a new request after the
registry has resolved and removed an older request with the same external
`tool_use_id`, while the older waiter is still unwinding. If that waiter deletes
a side-map entry by id alone, it can erase the newer request and make child-exit
cleanup miss it. `stdioPermissionHandler.await` therefore deletes the tracked
entry only when its stored `*permbridge.Pending` is the same registration it
awaited; the external correlation id alone is not an ownership token.

No daemon-lifecycle goroutine exists in this primitive — shutdown (a bulk "deny all pending on daemon stop" drain) is deferred; every entry is self-bounded by its own `timeout` window, so with no `AnswerableFunc` installed no entry outlives its deadline regardless of process shutdown timing, and with one installed no entry outlives a window in which the report reads unanswerable.

## Fail-closed / default-deny — the security-critical invariant

Allow is reachable by **exactly one path**: an explicit `Resolve(id, Allow(...))` that wins the delete-under-lock. Every other terminal path yields deny or a safe no-op:

| Failure mode | Behavior |
|---|---|
| Window elapses, no `AnswerableFunc` installed (default) | Timer wins the one-shot → `Await` returns `Deny(reasonTimeout)`; entry retired. Byte-identical to pre-#1931 behavior. |
| Window elapses, report installed and answers false (including nil report) | Same as above — deny with the same fixed message, no new wire shape. |
| Window elapses, report installed and answers true | Entry survives; timer re-armed for one more window; report re-asked at the next expiry. Not a terminal path — listed here because it is the one case that is *not* deny. |
| Resolve arrives **after** the deadline (or after a false reading) | Timer already deleted the entry → `Resolve` finds it gone → `false`, no-op. The already-delivered deny is never flipped to allow. |
| Lost caller (never `Await`s) | Timer still fires, retires the entry, sends deny into the unread buffer (GC'd). No leaked pending entry. |
| Unknown / already-resolved id on `Resolve`/`Lookup` | `false` / `(Request{}, false)`. No panic, no goroutine leak. |
| Empty or duplicate `id` on `Register` | `(nil, ErrDuplicateID)`; caller default-denies; existing entry unaffected. |
| Non-positive `timeout` | `AfterFunc` fires ≈immediately → deny. Not a security hole — fail-closed either way. |

**Since #1932, the production fail-closed bound is `idleTimeout + window`, not `window` alone.** The relay↔binary leg carries no per-connection disconnect frame (`docs/protocol-mobile.md` § close code 4408) — a phone that drops or backgrounds is only detectable by inbound-frame silence, caught by `internal/relay`'s 15-minute `idleTimeout` sweep, not a WS close. `ApprovalAnswerable`'s connected half stays `true` for a vanished phone until that sweep deletes its session, so an approval whose only answerer physically disappeared without the daemon observing it can stay parked for up to ~15 minutes plus one further window before denying. This is not unbounded — the idle sweep is unconditional and the report is re-asked every window — and nothing executes while an approval is parked, so the widened bound extends how long a decision waits, not how long anything runs unsupervised.

The deterministic proof is a 300-iteration allow-vs-timeout race test asserting internal consistency under `-race` on every round (belt-and-suspenders: the guarantee is structural — a map delete under a mutex — and the test is deterministic code, not another stochastic check).

**Known NIT (not fixed, contract documented instead):** the `AfterFunc` closure captures `id`, not the specific `*pending`, and re-looks-it-up in `resolve`. A stale timer that fires just as its entry is being re-`Register`ed under a **reused** id could, in a tight window, resolve the *new* entry instead of a no-op. This is fail-closed-only (can only ever deliver `Deny(reasonTimeout)`, never allow) and unreachable with claude's unique `tool_use_id`s, so code review (PR #1111) left it as-is. **Contract for future consumers:** an id must not be re-`Register`ed until its predecessor's timer has drained. If #1104 ever recycles ids, capture the `*pending` in the closure and identity-check it under `mu` before deleting.

## Trust boundary — who may call `Resolve`, and who may report liveness

The registry **trusts its caller**. `id` is an opaque correlation key (a tool-use id), **not a capability or credential** — it need not be unguessable at this layer. Authenticating that a `Resolve(id, allow)` really came from a human decision over the control socket, rather than a forger, is the **control-socket verb's** boundary (#1104's socket auth), not this primitive's. If a future consumer exposes `Resolve` on an unauthenticated surface, the unguessability requirement moves with it.

Since #1931, the registry trusts a second thing: the *honesty* of whatever `AnswerableFunc` `SetAnswerable` installs. It is a behavioural input, not data — a process-internal Go value the composition root chooses, never anything a network peer supplies — but a report that always answers `true` converts the fail-closed deadline into an unbounded park (see § Conditional bound). This is unreachable by any actor outside the daemon's own composition root, so it is not a hole; it is a contract obligation on whoever wires the seam.

## Testing

`internal/permbridge/permbridge_test.go`, same-package, stdlib `testing`, `go test -race`. Local helpers: `awaitWithin` (bounds `Await`), `registryLen`/`waitRetired` (poll internal state without a fixed sleep). Scenarios: allow path, deny path, timeout→deny, late-resolve-after-timeout (no flip), the 300-iteration allow-vs-timeout race, unknown-id no-op, duplicate/empty id, lost-caller self-clean (no leaked entry), `Lookup` returns the parked request, and marshal-shape golden checks (allow has no `message` key, deny has no `updatedInput` key). No fake clock anywhere — short real durations plus polling, throughout.

**#1931's extension scenarios, and the shape a race test needs to actually race.** `TestRegistry_ExtendedAllowVsTimeoutRace` extends the 300-iteration race with a scripted, call-counted `AnswerableFunc`; it initially shipped with the racing `Resolve` firing before the 1 ms timer on every iteration (`extensions=0`, the report never asked), which is a duplicate of the unextended race test wearing new fixtures rather than a test of the extension path. A race test that measures zero occurrences of the branch it exists to cover has not raced anything — the fix was a per-iteration jitter so `Resolve` lands inside the extension windows on some iterations, which also made the ask-count assertion (not the empty-buffer check alone) load-bearing evidence that the extension actually ran. The other new scenarios: a report that flips from true to false is re-asked and denied without further extension once it turns false (pins "re-asked, not settled once"); a report is never consulted off the expiry path (unknown/duplicate/already-resolved ids); the report runs with `mu` released (a re-entrant `Lookup` from inside the report would self-deadlock a regression); a report slower than its window produces exactly one expiry, not two; and a non-positive window is never extended even with an always-true report (`TestRegistry_NonPositiveWindowIsNeverExtended` — the guard this pins is reachable in production, since `approvalTimeout` passes an unclamped `PYRY_APPROVAL_TIMEOUT` straight through).

## Related

- [modalbridge-package.md](modalbridge-package.md) — the outstanding-registry pattern this package mirrors (leaf mutex, opaque-id-keyed map, one-shot `Resolve`), plus the relay-import-cycle hazard permbridge does *not* have to document.
- [permission-protocol-spike.md](permission-protocol-spike.md) — the #383 `--permission-prompt-tool stdio` protocol spike; the T1 spike (#1075) that directly motivates this registry's request/verdict contract is a later, undocumented-as-of-this-writing rerun against a registered MCP tool (fixture `fixture-p4-approval-contract.json`, not in-repo) — see the ticket body for its reproduced findings.
- [codebase/1103.md](../codebase/1103.md) — ticket record: implementation notes, patterns established, lessons learned (including the allow-vs-timeout test-code race lesson).
- [control-plane.md § Approve](control-plane.md#approve-mcpapprove-verb--forward-to-permbridge-block-default-deny-1104) and [v2-session-manager.md § Stream-json approval bridge](v2-session-manager.md#stream-json-approval-bridge--the-verdict-arm-1080) — where #1932 installs `SetAnswerable` and where the shared registry, surfacer and approvals-window plumbing they describe already lived.
