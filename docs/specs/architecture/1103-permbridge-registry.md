# Spec #1103 — `internal/permbridge`: pending-approval registry with fail-closed default-deny completer

## Files to read first

- `internal/modalbridge/modal.go:89-196` — the registry pattern to mirror: leaf `sync.Mutex`, opaque-id-keyed map, `New`/`Record`/`Lookup`/`Resolve` shape, clone-on-write, one-shot `Resolve` (delete-under-lock). permbridge is the same shape **plus** a per-entry completer + timer.
- `internal/modalbridge/modal.go:1-14` — package-doc convention: state the relay-free / import-discipline invariant up front. permbridge imports **nothing** from `internal/` (self-contained), so its doc states that explicitly.
- `cmd/pyry/acp_permission.go:36-79` — the codebase's existing default-deny idiom: every non-`selected` path routes deny; a one-shot arbiter (`resolved atomic.Bool` CompareAndSwap) resolves at most once; the deadline (`timeout`) source. permbridge reuses the *semantics* (deny is the failure mode, one-shot resolution) but arbitrates via **delete-under-mutex** instead of an atomic bool, because the map already needs the lock.
- `cmd/pyry/acp_permission.go:157-208` — `runRoundTrip` / `route`: the "one goroutine wins the one-shot, routes exactly one outcome, the loser routes nothing" structure the timer-vs-Resolve race mirrors.
- `CODING-STYLE.md` §§ Error Handling, Concurrency, Testing — sentinel errors + `errors.Is`, channels-for-coordination/mutex-for-state, table-driven `-race` tests, stdlib only.
- `internal/modalbridge/modal_test.go:1-40` — same-package test idioms (small local helpers, no testify) to match.

The T1 approval contract (fixture `fixture-p4-approval-contract.json`, not in-repo) is fully reproduced in the ticket body — no need to hunt for it.

## Context

Part of the Streamrunner Interactive permission bridge (ex-T5). The #1075 spike proved that a non-YOLO headless claude spawned with `--permission-prompt-tool mcp__<server>__<tool>` **synchronously blocks** on the MCP tool's allow/deny JSON for the full duration of a pending approval (measured 11.5 s behind an 8 s-delayed approval). A pending request is therefore real owed-silence of *unbounded* length. claude's own deny path does **not** hang the turn, so a timeout or a lost caller must resolve to **deny** — fail-closed / default-deny is the security-critical core of the whole mechanism.

This ticket delivers **only** the registry primitive: an in-memory store of pending approval requests, each keyed by an id, backed by a completer the blocked caller waits on and a resolver satisfies, with a per-entry fail-closed timeout that resolves to deny. Its consumers are siblings:

- the control-socket verb that forwards claude's request into `Register` and serializes the `Await`ed verdict back (#1104),
- the `pyry mcp-approve` stdio subcommand and spawn-arg injection (#1105/#1106),
- the `modal_shown ↔ modal_answer` wiring that calls `Resolve` from a human decision (#1080).

None of them exist yet; this package ships **unwired** and unit-tested in isolation (mirroring how modalbridge shipped ahead of #717).

## Design

### Package boundary

New package `internal/permbridge`, one production file `permbridge.go`. **Self-contained**: imports only stdlib (`encoding/json`, `errors`, `fmt`, `sync`, `time`). It imports nothing from `internal/` — no cycle is possible, and (unlike modalbridge) there is no relay-import hazard to document because there is no internal import at all. The package doc states this invariant.

The registry is **log-free**: it is a pure data structure. All content-free decision logging is the consumer's job (the verb / `mcp-approve` subcommand), matching how modalbridge carries no logger.

### Domain types (marshal-friendly; wire envelope deferred to #1104)

These are the types the downstream verb and `mcp-approve` serialize. JSON tags match the T1 contract exactly. **No control-socket envelope here** — just the request/verdict payloads.

```go
// Request is the tool-approval request claude sends (T1 spike contract).
type Request struct {
    ToolName  string          `json:"tool_name"`
    Input     json.RawMessage `json:"input"`      // opaque tool input object, preserved verbatim
    ToolUseID string          `json:"tool_use_id"`
}

// Verdict is the allow/deny decision claude accepts.
type Verdict struct {
    Behavior     string          `json:"behavior"`               // BehaviorAllow | BehaviorDeny
    UpdatedInput json.RawMessage `json:"updatedInput,omitempty"` // allow only (echoes Input, possibly modified)
    Message      string          `json:"message,omitempty"`      // deny only (reason)
}
```

- `Input` / `UpdatedInput` are `json.RawMessage` so an arbitrary tool-input **object** round-trips byte-verbatim (an allow echoes the request's `Input` back as `UpdatedInput`, which is exactly why `Lookup` must return the `Request` — see below).
- `omitempty` gives the two disjoint wire shapes: allow → `{"behavior":"allow","updatedInput":{…}}` (nil `Message` omitted); deny → `{"behavior":"deny","message":"…"}` (nil `UpdatedInput` omitted — a nil `json.RawMessage` is empty and dropped).

Constructors + constants make call sites correct-by-construction (no stringly-typed `"allow"`/`"deny"` typos at the resolver):

```go
const (
    BehaviorAllow = "allow"
    BehaviorDeny  = "deny"
)
func Allow(updatedInput json.RawMessage) Verdict // {BehaviorAllow, updatedInput, ""}
func Deny(message string) Verdict                // {BehaviorDeny, nil, message}
```

`reasonTimeout` (unexported, fixed string e.g. `"approval request timed out"`) is the deny message the timer path uses — a constant, never host-derived content.

### Registry

```go
type Registry struct {
    mu      sync.Mutex
    pending map[string]*pending   // keyed by Request.ToolUseID (opaque correlation id)
}
func New() *Registry

// pending is one parked request. ch is buffered(1) and receives EXACTLY ONE
// verdict, written by whichever of {Resolve, timer} wins the delete-under-lock.
type pending struct {
    req   Request
    ch    chan Verdict
    timer *time.Timer
}

// Pending is the caller's handle: block on Await for the verdict.
type Pending struct { ch <-chan Verdict }
func (p *Pending) Await() Verdict   // returns <-p.ch
```

Exported surface (types): `Request`, `Verdict`, `Registry`, `Pending` — **4** (under the 5-type red line). Plus funcs `New`, `Allow`, `Deny`, methods `Register`/`Resolve`/`Lookup`/`Await`, sentinel `ErrDuplicateID`.

Method contracts (signatures + behavior, not bodies):

- **`Register(id string, req Request, timeout time.Duration) (*Pending, error)`** — parks `req` under `id`, arms the fail-closed deadline, returns the handle. Rejects an empty `id` and a duplicate live `id` with `ErrDuplicateID` (caller then default-denies). Arms `time.AfterFunc(timeout, …)` that calls the internal one-shot with `Deny(reasonTimeout)`. The timer is stored on the entry so a winning `Resolve` can stop it.
- **`Resolve(id string, v Verdict) bool`** — satisfies the pending request `id` with `v`; returns `true` if it resolved a live entry, `false` if the id is unknown or already resolved (safe no-op — AC-4). Delegates to the shared internal one-shot.
- **`Lookup(id string) (Request, bool)`** — returns the parked `Request` for `id` without resolving it (read seam #1080 uses to build `Allow(req.Input)`). Unknown id → `(Request{}, false)`, a safe no-op (AC-4).
- **`Pending.Await() Verdict`** — blocks until the entry is resolved; returns the verdict. Guaranteed to return within `timeout` because the registry-owned timer always delivers a verdict (deny) if nothing else does.

### The one-shot: `resolve` (unexported, the security core)

Both `Resolve` and the timer callback funnel through one internal method:

```
resolve(id, v):
    mu.Lock()
    p, ok := pending[id]
    if !ok { mu.Unlock(); return false }   // lost the race / unknown / already resolved
    delete(pending, id)                    // ← the one-shot arbiter: delete-under-lock
    mu.Unlock()
    p.timer.Stop()                         // idempotent; harmless from the timer's own call
    p.ch <- v                              // buffered(1) + single writer ⇒ never blocks
    return true
```

**`delete(pending, id)` under `mu` is the sole arbiter.** Exactly one of {`Resolve`, timer} finds the entry present and becomes the single writer of `p.ch`; the other finds it gone and is a no-op. Because `ch` is buffered(1) and there is provably at most one send, the send never blocks — so it is safe to do after unlocking, with a single reader (`Await`) or no reader at all.

`Register` holds `mu` across the whole insert (including `p.timer = time.AfterFunc(...)` and the map write), so even a 0-duration timer that fires instantly blocks in `resolve` on `mu.Lock()` until the entry is fully installed — no fire-before-install race. `p.timer` is written under `mu` in `Register` and read in `resolve` only after `resolve` has acquired (then released) `mu`, so the happens-before is established by the lock even though the `Stop()` call sits just outside the critical section.

## Concurrency model

Goroutines that touch the registry:

1. **Caller / verb goroutine** — `Register` then `Await` (blocks on `<-p.ch`). One per parked request.
2. **Resolver goroutine** — #1080's `modal_answer` handler calls `Resolve`. Distinct goroutine from the caller.
3. **Timer goroutine** — `time.AfterFunc` fires the deadline callback (`resolve(id, Deny(reasonTimeout))`) in its own goroutine at `timeout`; created per entry, `Stop()`ped when `Resolve` wins so it never fires (no goroutine spawned in the resolved-in-time case).
4. **Lookup reader** — the verb/resolver may `Lookup` concurrently (read-only, under `mu`).

Shared state: `Registry.pending` guarded by the **leaf** `Registry.mu` (held only around O(1) map ops; never nested with another lock; the channel send is outside it). Each `pending.ch` is buffered(1), written exactly once by the one-shot winner, read at most once by `Await`.

**Shutdown:** out of scope for this slice. Each entry is self-bounded by its `timeout` (fail-closed), so no entry outlives its deadline; a bulk "deny all pending on daemon stop" drain, if ever needed, is a consumer/wiring concern — see Open questions. There is no daemon-lifecycle goroutine in this primitive.

## Error handling / failure modes

Fail-closed is the invariant: **allow is reachable only via an explicit `Resolve(id, Allow(...))`; every other terminal path yields deny.**

| Failure mode | Behavior |
|---|---|
| Deadline elapses before resolve | Timer wins the one-shot → `Await` returns `Deny(reasonTimeout)`; entry retired (AC-3). |
| Resolve arrives **after** the deadline | Timer already deleted the entry → `Resolve` finds it gone → returns `false`, no-op. The already-delivered deny is never flipped to allow (AC-3, security-critical). |
| Lost caller (never `Await`s) | Timer still fires, retires the entry, sends deny to the buffer (GC'd unread). **No leaked pending entry** (AC-4). |
| Unknown / already-resolved id on `Resolve` / `Lookup` | Returns `false` / `(Request{}, false)`. No panic, no goroutine leak, no leaked entry (AC-4). |
| Empty or duplicate `id` on `Register` | `(nil, ErrDuplicateID)`; caller default-denies. Existing entry unaffected. |
| Non-positive `timeout` | AfterFunc fires ≈immediately → deny. Document; not a security hole (fail-closed). |

Sentinel `ErrDuplicateID` follows the codebase convention (`internal/*` returns Go sentinels; wire-code mapping is the consumer's job) — matchable with `errors.Is`.

## Testing strategy

Same-package `permbridge_test.go`, stdlib `testing`, `go test -race`, table-driven where natural. Small local helper `awaitWithin(t, p, d) Verdict` (select on `<-done`-via-goroutine vs `time.After`) to assert `Await` returns within a bound. Scenarios (inputs → expected), not full bodies:

- **Allow path** — `Register(id, req, long)`; in a goroutine `Resolve(id, Allow(req.Input))`; `Await` returns `Behavior==BehaviorAllow` with `UpdatedInput==req.Input`; `Resolve` returned `true`; `Lookup(id)` now `false` (retired).
- **Deny path** — same but `Resolve(id, Deny("nope"))`; `Await` returns deny with `Message=="nope"`; retired.
- **Timeout path** — `Register(id, req, 20ms)`, no resolver; `Await` returns `Deny(reasonTimeout)` within ~timeout; `Lookup(id)` `false`.
- **Late resolve after timeout** — short timeout; `Await` returns deny; *then* `Resolve(id, Allow(...))` returns `false`; the returned verdict is still deny (no flip). No panic.
- **Allow-vs-timeout race (security invariant)** — loop N iterations with a tiny timeout, launching `Resolve(id, Allow)` concurrently with the timer; assert internal consistency each round: `Await`'s verdict is allow **iff** `Resolve` returned `true`, deny **iff** `Resolve` returned `false` — never a second delivery, never allow-after-deny. Must pass under `-race`. This is the deterministic (belt-and-suspenders) proof of the one-shot.
- **Unknown id no-op** — `Resolve("nope", Allow(nil))` → `false`; `Lookup("nope")` → `false`; no panic; registry stays empty.
- **Duplicate / empty id** — second `Register(id, …)` → `errors.Is(err, ErrDuplicateID)`; empty id → rejected; the first entry still `Resolve`-able.
- **Lost-caller self-clean** — `Register(id, req, 20ms)`, never `Await`; after the deadline `Lookup(id)` → `false` and the registry length is 0 (timer retired it). No leaked entry / goroutine (`-race`, and no `t.Fatal` from a leaked timer).
- **Lookup returns parked request** — `Register(id, req)`; `Lookup(id)` returns `req` (ToolName/Input/ToolUseID match), `true`; still pending afterward.
- **Marshal shape (golden key presence)** — `json.Marshal(Allow(obj))` contains `behavior:"allow"` + `updatedInput`, **no** `message`; `json.Marshal(Deny("x"))` contains `behavior:"deny"` + `message`, **no** `updatedInput`.

## Open questions

- **Bulk shutdown drain.** Should the registry expose `Close()`/`DenyAll()` to resolve every pending entry to deny on daemon stop, rather than waiting out each timeout? Deferred: no observed need in this slice, each entry is timeout-bounded, and the daemon-lifecycle owner is the verb/wiring ticket. Add only when a consumer needs deterministic immediate drain (evidence-based).
- **Timeout source.** Per-`Register` `timeout` param (chosen) vs a registry-level default set in `New`. Per-call is more flexible and lets the verb pick the window; revisit if every caller passes the same constant.
- **`updatedInput` policy.** Whether an allow ever *modifies* `Input` (vs echoing verbatim) is a #1080 resolver decision; the primitive only carries the bytes. Noted so #1080 owns it.

---

## Security review

Ticket is `security-sensitive`. This pass walks the mandated categories against the spec above. **Mindset: assume the resolver, the caller, and the clock are adversarial and find the path to a spurious allow.**

### Trust boundaries

- **Untrusted input parked here:** `Request.Input` is claude-tool input, opaque bytes carried as `json.RawMessage` and never parsed, matched, or interpolated by this package — it is stored and echoed only. No injection surface inside permbridge (the wire envelope that frames it is #1104's boundary, reviewed there).
- **Who may call `Resolve`:** the registry **trusts its caller**. `id` is an opaque correlation key (a tool-use id), **not a capability or credential** — it need not be unguessable *at this layer*. Authenticating the resolver (that a `Resolve(id, allow)` really came from a human decision over the control socket, not a forger) is the **control-socket verb's** trust boundary (#1104 socket auth), not this primitive's. This is stated so the boundary is owned, not assumed away — restating the ticket's "id = identifier, not credential" as an explicit finding. If a future consumer exposes `Resolve` on an unauthenticated surface, the unguessability requirement moves with it; flagged for #1104/#1080.

### Fail-closed / default-deny (the security-critical invariant)

- **Allow is reachable by exactly one path:** an explicit `Resolve(id, Allow(...))` that wins the delete-under-lock. Enumerated non-allow terminal paths — deadline, lost caller, unknown id, duplicate/empty id, non-positive timeout — **all** yield deny or a `false` no-op. Deny is the failure mode, not a branch (mirrors `acp_permission.go`'s design).
- **No allow-after-deadline race (the invariant the ticket calls out).** The single arbiter is `delete(pending, id)` under `mu`. Once the timer's `resolve` deletes the entry and sends `Deny(reasonTimeout)`, any later `Resolve(id, Allow)` finds the entry absent and returns `false` — it **cannot** deliver a second verdict, because `ch` is written only by the goroutine that won the delete, and the delete happens exactly once. Conversely if `Resolve` wins, it `Stop()`s the timer; even a timer that already fired then finds the entry gone and is a no-op. There is no window in which both deliver. The **allow-vs-timeout race test under `-race`** is the deterministic safety net asserting this (belt-and-suspenders: the guarantee is structural — a map delete under a mutex — and the test is deterministic code, not another stochastic check).
- **Send safety.** `ch` is buffered(1) with a provably single writer, so the one send never blocks and never panics (no double-send on a would-be-closed channel — the channel is never closed; the value simply rides the buffer and is GC'd if unread).

### Resource / DoS

- **No unbounded growth:** every entry is retired within its `timeout` (timer always fires or is stopped by a resolve). A lost or malicious caller that registers and vanishes cannot leak entries — the timer self-cleans. A flood of `Register` calls is bounded by the consumer's own admission control (#1104), not this primitive; the registry adds no unbounded buffer (each `ch` is size 1, freed on retire).
- **No goroutine leak:** the only goroutine the primitive spawns is the per-entry `AfterFunc`, which is `Stop()`ped on resolve or runs once and exits. Verified by the lost-caller and unknown-id `-race` tests.

### Information disclosure / logging

- The registry is **log-free** — it holds no logger and emits nothing, so it cannot leak `Request.Input`, the `id`, or a deny `Message` into logs. Content-free decision logging is the consumer's responsibility (`acp_permission.go` is the template: log discriminants, never modal/prompt/option content). No disclosure surface here.

### Verdict: **PASS**

The default-deny invariant holds on every enumerated path; the one allow path requires an explicit trusted `Resolve`; the allow-after-deadline race is closed by a single delete-under-mutex arbiter and asserted deterministically under `-race`; no entry, goroutine, or content leaks. The one boundary this primitive does **not** own — authenticating the resolver — is explicitly delegated to the consumer ticket (#1104), consistent with the id being a correlation key rather than a credential.
