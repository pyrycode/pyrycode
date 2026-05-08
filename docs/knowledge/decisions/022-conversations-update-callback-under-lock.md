# ADR 022: `Registry.Update` runs the caller's callback under the registry lock

## Status

Accepted (#217).

## Context

`internal/conversations.Registry` needs a method to mutate a single record by id. `Conversation` carries reference-typed fields (`*string Name`, `[]string SessionHistory`); pass-by-value would force the caller to construct a full replacement struct on every change, defeating the point. `internal/devices.Registry` has no analogous method because device records are append-only after pairing — only `Add` and `Remove`. Conversations mutate (rename, promote, rotate the bound session, bump `LastUsedAt`), so this method is genuinely needed.

Two reasonable shapes for the method:

1. **Callback-under-lock.** `Update(id, fn func(*Conversation)) bool` — locate the entry, invoke `fn` with a pointer to the slice element while the registry lock is held, return `true`. On miss, return `false` and do not invoke `fn`.

2. **Snapshot-mutate-swap.** `Update(id, fn func(Conversation) Conversation) bool` — under the lock, snapshot the entry by value; release the lock; pass the snapshot to `fn`; reacquire the lock; locate the entry again; replace it with `fn`'s return value.

## Decision

Use callback-under-lock. AC literal signature is `Update(id, fn func(*Conversation))`; the body locates the entry, calls `fn(&r.conversations[i])` under `r.mu`, and returns. On miss, `fn` is never invoked.

The method's doc comment pins three caller obligations:

- `fn` MUST NOT call back into the registry (any `Registry` method would deadlock — `sync.Mutex` is non-reentrant).
- `fn` MUST NOT retain the `*Conversation` pointer past return (slice reallocation by a future `Create` would dangle the pointer).
- `fn` may read and mutate any field; the registry does not validate post-mutation state.

## Rationale

Snapshot-mutate-swap looks like the safer shape (caller never holds the lock, no deadlock risk) but loses correctness in two ways:

- **Lost writes.** Two concurrent `Update` calls on the same id both snapshot the same pre-state, both compute new states, both reacquire the lock, and the second swap silently overwrites the first. A CAS loop ("retry if the entry mutated since I snapshotted") would fix it but introduces failure modes (livelock under heavy contention; transient `Update` failures; a generation counter on every record). For a registry where the writer is a low-frequency operator action (rename, promote) and the reader is `Get`/`List`, the contention pattern doesn't justify the machinery.

- **Reference fields require a deeper contract.** `Conversation.SessionHistory` is `[]string`. With snapshot-mutate-swap, the snapshot's slice header points at the registry's backing array; the caller mutating the snapshot's slice could race a concurrent reader on the same backing array. Either the registry deep-copies on snapshot (extra allocation per `Update`) or the contract documents "do not mutate `SessionHistory` in place" (which the callback-under-lock shape also documents but never violates because the lock is held).

Callback-under-lock makes both problems disappear: the lock serializes mutations, and the in-place mutation never publishes a half-updated record to a concurrent reader. The deadlock risk is real but bounded by a single rule (`fn` does not call back into the registry); a code-review check is sufficient. Conversations are not high-contention; the lock-held duration is whatever the caller's `fn` body costs, which is dominated by trivial field assignments.

The callback-under-lock pattern also matches Go stdlib idioms — `sync.Map.Range`'s callback runs without holding any lock, but `database/sql.Tx`'s callback shape (transaction wrappers) does run under an implicit lock and documents the no-callback-back constraint identically.

`Update` returns `bool` (hit/miss), not `(Conversation, bool)` (post-mutation snapshot). The AC pins the bool-only signature; callers that need the post-state read it inside `fn` (the lock guarantees no concurrent mutation between read and any subsequent in-callback decision). Adding the post-mutation snapshot return is a non-breaking additive change if a future caller needs it.

## Consequences

- `Update` is the canonical mutation entry point for the conversations registry. New mutation surfaces (rename API, promote API, session rotation) all funnel through `Update` rather than acquiring `Registry.mu` from the outside.
- Callers must not call other `Registry` methods from within `fn`. Code review catches this; the doc comment is the primary defense.
- A future "transactional multi-record update" surface (rename two conversations atomically) cannot be expressed by composing `Update` calls — that ticket would need its own primitive. Phase 3 has no such requirement.
- The "no shared atomic-write helper across packages" stance from [the issue tech note] extends to the mutation helper: do not abstract `Update` into a generic `internal/registry` helper. The two registries (`devices` append-only, `conversations` mutable) have structurally different mutation surfaces; sharing would hide that divergence.
- If a future `Conversation` field starts being mutated outside the registry lock (e.g. an external goroutine appending to `SessionHistory`), the snapshot at `Save` becomes incorrect. Today's invariant — every mutation goes through `Update` and runs under `r.mu` — keeps the shallow-copy snapshot correct.

## Alternatives considered

- **Snapshot-mutate-swap.** Rejected — lost writes under concurrent same-id updates; reference-field aliasing forces deeper contract or extra allocations; CAS loop adds failure modes.
- **Expose `(*Registry).WithLock(fn func())` and re-entrant getters.** Rejected — every "while you're in there" caller becomes a load-bearing review item; deadlock risk diffuses across the codebase. Callback-under-lock keeps the discipline at one method.
- **Return `(Conversation, bool)` from `Update`** — defer; AC is bool-only and additive change later costs nothing.

## Related

- [`features/conversations-registry.md`](../features/conversations-registry.md) — the registry surface this decision shapes.
- [ADR 020](020-devices-registry-snapshot-then-write.md) — `devices.Registry.Save` snapshots under lock and writes outside; the conversations registry inherits the same pattern. `Update` is the analogous discipline on the mutation side.
- `docs/specs/architecture/217-conversations-registry-crud.md` — architect's spec, "Open questions" section, item 2 (post-mutation return value).
