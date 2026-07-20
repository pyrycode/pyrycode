# `internal/permbridge` — pending tool-approval registry (Streamrunner Interactive permission bridge)

The daemon-side registry that lets a synchronous permission prompt from a non-YOLO headless `claude` be parked, surfaced for a human decision, and answered (or safely defaulted to deny) **without hanging the turn**. First slice (#1103, split from #1079) of the Streamrunner Interactive permission bridge; the T1 protocol spike (#1075) proved the mechanism this package backs: `claude` spawned with `--permission-prompt-tool mcp__<server>__<tool>` synchronously calls a registered MCP tool and blocks on its allow/deny JSON for the full duration of the pending approval (the spike measured an 11.5 s turn behind an 8 s-delayed approval). claude's own deny path does **not** hang the turn, so a lost caller or an elapsed deadline must resolve to deny — fail-closed / default-deny is the security-critical core of the whole mechanism.

This package ships the registry **primitive only**, unwired and unit-tested in isolation (mirroring how `internal/modalbridge` shipped ahead of its own consumers). Consumers are siblings:

- the control-socket verb that forwards claude's request into `Register` and serializes the `Await`ed verdict back (#1104, not yet landed)
- the `pyry mcp-approve` stdio subcommand + spawn-arg injection (#1105/#1106, not yet landed)
- the `modal_shown ↔ modal_answer` wiring that calls `Resolve` from a human decision (#1080, not yet landed)

Spec: [`specs/architecture/1103-permbridge-registry.md`](../../specs/architecture/1103-permbridge-registry.md). Ticket record: [codebase/1103.md](../codebase/1103.md).

## Why `internal/permbridge` is self-contained

Unlike [`internal/modalbridge`](modalbridge-package.md), which must document a relay-import hazard (`internal/relay` imports it, so it must not import `internal/relay` back), `internal/permbridge` imports **nothing** from `internal/` at all — only stdlib (`encoding/json`, `errors`, `sync`, `time`). No import cycle is possible with any future consumer. The package doc states this invariant directly rather than documenting a hazard to avoid.

It is also **log-free** — a pure data structure with no logger field, so it cannot leak a request's tool input, an id, or a deny message into logs. Content-free decision logging is the consumer's job, matching how `acp_permission.go` logs only discriminants.

## Domain types (wire contract, envelope deferred to #1104)

```go
// Request is the tool-approval request claude sends (T1 spike contract).
type Request struct {
    ToolName  string          `json:"tool_name"`
    Input     json.RawMessage `json:"input"`      // opaque, preserved verbatim
    ToolUseID string          `json:"tool_use_id"`
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

`Input`/`UpdatedInput` are `json.RawMessage` so an arbitrary tool-input object round-trips byte-verbatim — an allow typically echoes the request's `Input` back as `UpdatedInput` (whether an allow ever *modifies* it is a #1080 resolver decision; the primitive only carries the bytes). `omitempty` gives the two disjoint wire shapes claude expects. `Allow`/`Deny` constructors make call sites correct-by-construction — no stringly-typed `"allow"`/`"deny"` at the resolver. `reasonTimeout` (unexported, a fixed string, never host-derived content) is the deny message the timer path uses.

## Registry surface

```go
type Registry struct { /* leaf sync.Mutex + map[string]*pending */ }
func New() *Registry

func (r *Registry) Register(id string, req Request, timeout time.Duration) (*Pending, error)
func (r *Registry) Resolve(id string, v Verdict) bool
func (r *Registry) Lookup(id string) (Request, bool)

type Pending struct { /* ch <-chan Verdict */ }
func (p *Pending) Await() Verdict
```

- **`Register`** parks `req` under `id`, arms the fail-closed deadline (`time.AfterFunc(timeout, …)`), and returns the caller's handle. Rejects an empty `id` or a duplicate live `id` with `ErrDuplicateID` (`errors.Is`-matchable; caller default-denies).
- **`Resolve`** satisfies the pending request `id` with `v`; returns `true` if it resolved a live entry, `false` if the id is unknown or already resolved — a safe no-op.
- **`Lookup`** returns the parked `Request` for `id` without resolving it — the read seam #1080 uses to build `Allow(req.Input)`. Unknown id → `(Request{}, false)`, also a safe no-op.
- **`Pending.Await`** blocks until resolved and returns the verdict; guaranteed to return within the `Register` timeout because the registry-owned timer always delivers a deny if nothing else resolves the entry first.

Exported surface: 4 types (`Request`, `Verdict`, `Registry`, `Pending`), funcs `New`/`Allow`/`Deny`, sentinel `ErrDuplicateID`.

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

Four goroutine roles touch the registry: (1) the caller/verb goroutine — `Register` then `Await`, one per parked request; (2) the resolver goroutine (#1080's `modal_answer` handler calling `Resolve`); (3) the per-entry timer goroutine (`time.AfterFunc`, `Stop()`ped when `Resolve` wins so it never fires in the resolved-in-time case); (4) a concurrent `Lookup` reader. All shared state is `Registry.pending`, guarded by the **leaf** `Registry.mu` — held only around O(1) map ops, never nested with another lock, never held across the channel send. Each `pending.ch` is buffered(1), written exactly once by the one-shot winner, read at most once by `Await`.

No daemon-lifecycle goroutine exists in this primitive — shutdown (a bulk "deny all pending on daemon stop" drain) is deferred; every entry is self-bounded by its own `timeout`, so no entry outlives its deadline regardless of process shutdown timing.

## Fail-closed / default-deny — the security-critical invariant

Allow is reachable by **exactly one path**: an explicit `Resolve(id, Allow(...))` that wins the delete-under-lock. Every other terminal path yields deny or a safe no-op:

| Failure mode | Behavior |
|---|---|
| Deadline elapses before resolve | Timer wins the one-shot → `Await` returns `Deny(reasonTimeout)`; entry retired. |
| Resolve arrives **after** the deadline | Timer already deleted the entry → `Resolve` finds it gone → `false`, no-op. The already-delivered deny is never flipped to allow. |
| Lost caller (never `Await`s) | Timer still fires, retires the entry, sends deny into the unread buffer (GC'd). No leaked pending entry. |
| Unknown / already-resolved id on `Resolve`/`Lookup` | `false` / `(Request{}, false)`. No panic, no goroutine leak. |
| Empty or duplicate `id` on `Register` | `(nil, ErrDuplicateID)`; caller default-denies; existing entry unaffected. |
| Non-positive `timeout` | `AfterFunc` fires ≈immediately → deny. Not a security hole — fail-closed either way. |

The deterministic proof is a 300-iteration allow-vs-timeout race test asserting internal consistency under `-race` on every round (belt-and-suspenders: the guarantee is structural — a map delete under a mutex — and the test is deterministic code, not another stochastic check).

**Known NIT (not fixed, contract documented instead):** the `AfterFunc` closure captures `id`, not the specific `*pending`, and re-looks-it-up in `resolve`. A stale timer that fires just as its entry is being re-`Register`ed under a **reused** id could, in a tight window, resolve the *new* entry instead of a no-op. This is fail-closed-only (can only ever deliver `Deny(reasonTimeout)`, never allow) and unreachable with claude's unique `tool_use_id`s, so code review (PR #1111) left it as-is. **Contract for future consumers:** an id must not be re-`Register`ed until its predecessor's timer has drained. If #1104 ever recycles ids, capture the `*pending` in the closure and identity-check it under `mu` before deleting.

## Trust boundary — who may call `Resolve`

The registry **trusts its caller**. `id` is an opaque correlation key (a tool-use id), **not a capability or credential** — it need not be unguessable at this layer. Authenticating that a `Resolve(id, allow)` really came from a human decision over the control socket, rather than a forger, is the **control-socket verb's** boundary (#1104's socket auth), not this primitive's. If a future consumer exposes `Resolve` on an unauthenticated surface, the unguessability requirement moves with it.

## Testing

`internal/permbridge/permbridge_test.go`, same-package, stdlib `testing`, `go test -race`. Local helpers: `awaitWithin` (bounds `Await`), `registryLen`/`waitRetired` (poll internal state without a fixed sleep). Scenarios: allow path, deny path, timeout→deny, late-resolve-after-timeout (no flip), the 300-iteration allow-vs-timeout race, unknown-id no-op, duplicate/empty id, lost-caller self-clean (no leaked entry), `Lookup` returns the parked request, and marshal-shape golden checks (allow has no `message` key, deny has no `updatedInput` key).

## Related

- [modalbridge-package.md](modalbridge-package.md) — the outstanding-registry pattern this package mirrors (leaf mutex, opaque-id-keyed map, one-shot `Resolve`), plus the relay-import-cycle hazard permbridge does *not* have to document.
- [permission-protocol-spike.md](permission-protocol-spike.md) — the #383 `--permission-prompt-tool stdio` protocol spike; the T1 spike (#1075) that directly motivates this registry's request/verdict contract is a later, undocumented-as-of-this-writing rerun against a registered MCP tool (fixture `fixture-p4-approval-contract.json`, not in-repo) — see the ticket body for its reproduced findings.
- [codebase/1103.md](../codebase/1103.md) — ticket record: implementation notes, patterns established, lessons learned (including the allow-vs-timeout test-code race lesson).
