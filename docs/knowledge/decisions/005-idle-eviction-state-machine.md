# ADR 005: Idle eviction as a per-session two-state machine

**Status:** Accepted (2026-05-02, ticket [#40](https://github.com/pyrycode/pyrycode/issues/40))
**Phase:** 1.2c-A
**Supersedes:** —

## Context

Phase 2.0's first-message lazy bind will let Discord channels mint sessions automatically. Without eviction, pyry's RAM footprint scales with total session count rather than active conversation count. We need a primitive that lets idle claudes exit (~zero RAM) and respawn on demand. The JSONL on disk is the source of truth for session identity, so an evicted claude can be respawned with `claude --session-id <uuid>` and continue the prior conversation.

The design space had three independent questions:

1. **Where does the lifecycle goroutine live?** Per-session, in `Pool`, or external scheduler.
2. **What counts as activity?** Bytes through the bridge, attach state, or last message timestamp.
3. **Does Lookup or Attach implicitly wake an evicted session, or is wake-up an explicit verb?**

## Decision

1. **Lifecycle goroutine lives in `Session.Run`**, rewritten from a one-line supervisor delegate into a state-machine loop over `runActive` / `runEvicted`. Each `*Session` owns its own goroutine.
2. **Activity is "at least one client is currently attached" (`attached > 0`).** While attached, the idle timer's eviction is deferred (poll-with-grace: re-arm on fire). On detach, the timer runs out the configured window and evicts.
3. **Wake-up is an explicit `Session.Activate(ctx)` call.** `Lookup` does not wake. The control plane invokes `Activate` before `Attach` in `handleAttach`.

State-machine shape: two states (`active` / `evicted`) with a single backing `lcMu` mutex, an `activeCh` close-on-active broadcast, and a buffered(1) `activateCh` signal channel. Lock order: `Pool.mu → Session.lcMu`. Persistence is the `Pool.persist` seam called by `Session.transitionTo` after each state change.

## Rationale

### Per-session goroutine over central scheduler

A central scheduler (`time.AfterFunc` map keyed by `SessionID`, or one big tick loop) collapses N timers into 1, but it forces every state mutation to coordinate with the scheduler. Per-session keeps the state machine local: the goroutine that owns the supervisor lifecycle also owns the timer that decides when to stop it. Testing one session's behaviour doesn't require constructing a Pool. Phase 1.1's N-session fan-out drops in as one `g.Go(sess.Run)` per entry — same shape `errgroup` already supports for the bootstrap+watcher pair (1.2b-B).

The N-goroutines cost is a Go select per session, which is cheap. Alternatives that share a single timer lose the ability to express "while attached, defer eviction" as a local rule.

### Activity = attach state, not bytes-through-bridge

Tying activity to attach state captures the user intent (someone is using this session) without per-byte counting through the bridge. Bytes-through-bridge would require either reader plumbing through `*supervisor.Bridge` (cross-package surgery for one feature) or a sampling timer (the same tick problem we're trying to solve). The bridge already knows when it's bound and unbound — that's the cheap signal.

Today the only way to interact with a session is `pyry attach`, so attach state is a perfect proxy. When Phase 2.0's router lands, "router is delivering messages to this session" becomes another form of activity; the same `attached++` / `attached--` shape extends additively.

`last_active_at` IS still bumped on every state transition — that's the proxy the upcoming LRU concurrent-active cap (1.2c-B) consumes for victim selection.

### Activate as an explicit verb, not implicit on Lookup

Implicit wake on Lookup tangles two concerns: "tell me which session this id resolves to" and "make sure that session is live before I touch it." `pyry status` resolves a session via Lookup but should *not* wake an evicted one — the operator asking about state shouldn't pay 2-15s of respawn latency just for a status read. With Lookup-wakes-on-resolve, every read-side caller would need a "but please don't wake" knob.

Explicit `Activate` keeps the contract sharp: callers who genuinely need the session running call Activate. `handleAttach` does (the bridge would block forever otherwise). `handleStatus` doesn't (it reports `PhaseStopped`, which is faithful). `Pool.Activate(ctx, id)` is the thin wrapper for symmetry — the future router gets a single entry point without depending on the concrete `*Session` type.

### Why M-sized in one ticket, not split

Two seams were considered for splitting:

- **Primitive (Activate + state machine) vs. policy (idle timer + persistence).** The primitive without the timer is dead code nobody calls; the timer without `Activate` is a one-way trip into evicted-and-stuck; the AC requires `lifecycle_state` persisted. Each child slice would need placeholder consumers the next ticket immediately rewrites.
- **In-memory state machine vs. persistence.** Persistence is a 30-line addendum; doesn't stand alone.

The work is genuinely one state machine with one persist seam and one external trigger. ~140-160 production lines, ~5 files, supervisor unchanged, existing test infra reused. M with the full surface in one ticket gives the dev one design to hold rather than two halves to coordinate.

## Consequences

### Positive

- Phase 2.0's first-message lazy bind can land without an eviction-shaped hole in the design.
- LRU concurrent-active cap (1.2c-B) consumes `Activate` and `lifecycle_state` directly — no new primitives needed.
- The N-session fan-out shape lands incrementally via `errgroup` — same wrapper 1.2b-B introduced.
- Operators get an escape hatch (`-pyry-idle-timeout 0`) and a sensible production default (15m).
- `lifecycle_state: omitempty` keeps the registry's idempotent-reload byte-stability property intact for the dominant active case.

### Negative / risks

- **Real eviction may overshoot configured timeout by up to one window** because of poll-with-grace. Acceptable; documented as part of the latency story.
- **SIGKILL during JSONL write could truncate the final line.** Claude's JSONL is line-delimited and readers skip incomplete entries on resume, so this is a theoretical-not-observed concern. A graceful supervisor stop path is tracked as an open question for a follow-up if observed in practice.
- **`pyry status` on an evicted session reports `PhaseStopped`** with no lifecycle hint. Faithful but uninformative — could add a session-level lifecycle field to the status payload in a separate ticket; wire-format change so out of #40.
- **`pyry attach` UX during respawn is silent** for the 2-15s window. Polish, not in scope for #40.

### Neutral

- `Pool.Run` is **not** restructured for #40. Multi-session fan-out remains Phase 1.1's job. The new state machine lives entirely inside `Session.Run`.

## Alternatives considered

| Alternative | Why rejected |
|---|---|
| Central `time.AfterFunc` scheduler in `Pool` | Couples every state mutation with the scheduler; blocks per-session local reasoning; testing requires Pool construction. |
| Bytes-through-bridge as activity signal | Requires reader plumbing through `*supervisor.Bridge` or a sampling timer; the bridge already knows attach state for free. |
| Implicit wake on `Pool.Lookup` | Tangles read-side concerns; status reads would pay respawn latency; needs a "don't wake" knob on every read-side caller. |
| Split into primitive + policy tickets | Each child slice incoherent on its own; placeholder consumers churned by the next ticket. |
| Single-state struct + boolean instead of enum | Enum gives a `String()` for the on-disk encoding and one place to grow `stateDraining` / `stateZombie` if Phase 2 needs them. |

## References

- Ticket: [#40](https://github.com/pyrycode/pyrycode/issues/40)
- Spec: [`docs/specs/architecture/40-idle-eviction-lazy-respawn.md`](../../specs/architecture/40-idle-eviction-lazy-respawn.md)
- Feature doc: [`features/idle-eviction.md`](../features/idle-eviction.md)
- Locked phase design: [`docs/multi-session.md`](../../multi-session.md), [`docs/plan.md`](../../plan.md)
- Sibling: [ADR 003](003-session-addressable-runtime.md) (sessions package shape this builds on)
