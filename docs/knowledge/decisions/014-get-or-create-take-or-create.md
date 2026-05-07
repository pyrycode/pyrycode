# ADR 014: `Pool.GetOrCreate` is take-or-create, not insert-or-error

## Status

Accepted (ticket #155, Phase 1.3b).

## Context

Phase 1.3b adds `pyry attach --create-if-missing <uuid>` so SDK consumers (Claudian / `@anthropic-ai/claude-agent-sdk`) can issue one attach call instead of two (`pyry sessions new --id <uuid>` then `pyry attach <uuid>`). The SDK already mints a UUIDv4 per chat upstream; pyry must accept that UUID even when it has never seen it before.

The handler's natural shape is "ensure session X exists, then attach." This needs a new Pool primitive — `Pool.Create` mints its own UUID via `NewID()` and cannot accept a caller-supplied id. The ticket invited two shapes:

1. **`CreateWithID(ctx, id, label)`** — insert-or-error. Returns `ErrIDInUse` when the id is already registered.
2. **`GetOrCreate(ctx, id, label)`** — take-or-create. Returns the canonical id whether the session existed or was just created.

Either shape implements AC #4 ("concurrent same-UUID create produces one entry"), but only with very different handler ergonomics.

## Decision

**`GetOrCreate(ctx context.Context, id SessionID, label string) (SessionID, error)`** — take-or-create. The handler treats "exists" and "fresh" identically from the call site onward: same `Pool.Lookup`, same `sess.Activate`, same `sess.Attach`.

Validation of the id-shape lives at the Pool boundary (`ValidID` next to `NewID` in `id.go`). Empty / malformed strings return `ErrInvalidSessionID`.

The "exists" branch is a constant-time map lookup under `Pool.mu` and returns without activating the session. The "create" branch holds `Pool.mu` across **register + persist + skip-set prime + supervise** (the lifecycle goroutine schedule) — see *Atomic registration* below.

## Rationale

### Why take-or-create over insert-or-error

An insert-or-error primitive would force the handler to:

```go
id, err := p.CreateWithID(ctx, sessionID, "")
if errors.Is(err, ErrIDInUse) {
    id, err = p.ResolveID(sessionID)
    if err != nil { ... }
}
sess, err := p.Lookup(id)
```

That re-introduces the TOCTOU window we are trying to close: between the failed `CreateWithID` and the follow-up `ResolveID`, a `Pool.Remove` could land and the `Lookup` would fail. Two SDK chats opening simultaneously would race through that window with no atomicity guarantee.

`GetOrCreate` collapses both branches into one Pool call. The atomicity is enforced inside `Pool.mu` — any concurrent same-id caller observes the registered entry and returns the canonical id with no error.

### Why validate UUIDv4 shape at the Pool boundary

`SessionID` is a `string` newtype with no validation. With caller-supplied ids flowing through the wire, "a session with id `b`" becomes accidentally constructible — `pyry attach --create-if-missing b` would otherwise create a session keyed by the literal string `"b"`. The registry, the file-system layout (`<encoded-cwd>/<uuid>.jsonl`), and `pyry sessions list`'s renderer were never designed for non-canonical ids.

`ValidID` (also new in this ticket, lives next to `NewID`) checks 36 chars, dashes at positions 8/13/18/23, hex elsewhere, version-4 nibble at position 14, RFC 4122 variant at position 19. The version + variant checks are belt-and-suspenders — SDK-produced UUIDs are uuidv4 by construction, but a future contributor mistakenly passing a v3/v5 id gets a clean error.

Validation lives at the Pool boundary (not the handler) so all future callers — direct Go consumers, test harnesses, future verbs — pick it up adapter-free.

### Why `--create-if-missing` skips `ResolveID`'s prefix logic

Without `--create-if-missing`, today's attach uses `ResolveID`, which accepts a unique prefix as a convenience for human users. With `--create-if-missing`, `handleAttach` skips `ResolveID` entirely and passes the literal `payload.SessionID` to `GetOrCreate`. Reasoning:

- The flag's intended caller is the SDK, which always passes a full UUID. Prefix-resolution is a human affordance not relevant to that path.
- A "prefix that doesn't match" being interpreted as a fresh UUID to register would create sessions with non-canonical ids.

`GetOrCreate`'s validator catches a non-UUID input at the Pool boundary; the handler surfaces the typed error verbatim.

### Why label is silently dropped on the take path

When `GetOrCreate` short-circuits to an existing entry, the caller's `label` argument is silently ignored. Today's only caller (`handleAttach`) passes `""`, so it doesn't matter. If a future caller wants take-or-create-with-label-update, that's a separate primitive (`Rename` after `GetOrCreate`); the silent-drop is documented in `GetOrCreate`'s docstring rather than smuggled in here.

## Atomic registration — the load-bearing change

`Pool.Create` releases `p.mu` between the registry-persist step and the `supervise` step. That gap is benign for `Create` because the id is freshly minted via `NewID` — no second goroutine ever sees the entry before `supervise` schedules the lifecycle goroutine.

`GetOrCreate` cannot afford that gap. A concurrent `GetOrCreate(sameID)` could observe the entry under `Pool.mu` after the winner's persist, return the session reference, and call `Session.Activate` — but the winner's lifecycle goroutine has not been scheduled yet. The buffered `activateCh` send completes, the unbuffered `activeCh` is never closed, and `Activate` blocks until ctx times out. The race detector cannot catch it; the failure mode is "30s hangs at attach time on the loser."

The fix is to hold `p.mu` across **all five** of:

1. duplicate-id short-circuit (`if existing, ok := p.sessions[id]; ok`)
2. registry-map insert
3. `saveLocked()`
4. `registerAllocatedUUIDLocked(id)` — rotation watcher skip-set prime
5. `g.Go(func() error { return sess.Run(gctx) })` — lifecycle goroutine schedule

`g.Go` is non-blocking: the goroutine it spawns parks on `activateCh` / `runCtx.Done()` before doing any pool work. Holding `p.mu` across `g.Go` is therefore safe (no lock-order violations, no deadlock risk).

`Activate(ctx)` happens **after** `p.mu` is released — `Activate` has its own (`capMu`, `lcMu`) discipline; holding `p.mu` across it would deadlock, the same constraint `Pool.Remove` already encodes.

## Helper extraction

Two private helpers shipped alongside `GetOrCreate`:

- **`buildSession(id, label) (*Session, error)`** — constructs the per-session supervisor + Session. Touches no Pool state. Shared verbatim between `Pool.Create` and `Pool.GetOrCreate`.
- **`registerAllocatedUUIDLocked(id)`** — the lock-held variant of `RegisterAllocatedUUID`. Caller MUST hold `p.mu` (write). Used by `GetOrCreate` to prime the skip-set inside the same critical section as the registry insert.

`Pool.Create`'s body shrinks; behaviour is unchanged.

## Consequences

### Positive

- **One Pool call per attach.** `handleAttach` switches between `ResolveID` (no flag) and `GetOrCreate` (flag) at the front; the rest of the function is unchanged.
- **Concurrent same-UUID safety.** AC #4 falls out of holding `p.mu` across the schedule. Two SDK chats opened simultaneously produce one registry entry, one supervised child, one rotation skip-set entry.
- **Validation centralised.** `ValidID` is the single source of truth for "what shape is a SessionID." Future verbs accepting caller-supplied ids reuse it.
- **Sibling not subtype.** `Pool.Create` (UUID-minted) and `Pool.GetOrCreate` (caller-supplied) sit beside each other and share `buildSession`. Refusing to merge them keeps each call site's contract explicit at the type level — a caller cannot accidentally pass `""` for the id of `Create`.

### Negative

- **`Pool.mu` held across `g.Go`.** A new edge in the lock-order analysis. Documented inline; `g.Go`'s non-blocking semantics make it safe, but reviewers must keep this in mind for any future `Pool.mu`-held block.
- **`ValidID` adds 30 LOC.** Stdlib-only character-class checks. The maintenance cost is trivial, but it's surface that wasn't there before.

### Neutral

- **`GetOrCreator` is a 1-method interface embedded in `Sessioner`.** Mirrors the `Remover` / `Renamer` / `Lister` pattern. `*sessions.Pool` satisfies it structurally; tests fake the interface directly.
- **`AttachPayload.CreateIfMissing` is `omitempty`.** Byte-identical wire output for clients that don't set it (same v0.5.x rollover guarantee that pins `SessionID`'s tag).

## Alternatives considered

- **`CreateWithID` (insert-or-error)** — rejected for the TOCTOU reason above.
- **Validate UUID shape at the handler layer** — rejected; pushes a new concern into every future caller of the primitive (test harnesses, channel-driven auto-mint).
- **Skip `g.Go`-under-`p.mu`, keep the existing post-unlock supervise call from `Pool.Create`** — rejected; the ~30s hang failure mode (ctx timeout in `Activate` on the loser) is too long a way to walk for a race that is nearly impossible to catch in CI.
- **Apply `--create-if-missing` semantics through `ResolveID`'s prefix path** — rejected; would let typos register as new sessions with non-canonical ids.

## References

- [`features/sessions-package.md` § Pool.GetOrCreate (1.3b)](../features/sessions-package.md#poolgetorcreate-13b) — implementation walkthrough.
- [`features/control-plane.md` § Attach: --create-if-missing (1.3b)](../features/control-plane.md#attach---create-if-missing-13b) — handler branch + wire field.
- [ADR 012](012-attach-stdio-flag-vs-verb.md) — sibling Phase 1.3a flag (`--stdio`).
- [ADR 013](013-evict-activate-persist-ordering.md) — surfaced by `TestPool_GetOrCreate_PersistsPostDetach` from this ticket.
- [`docs/specs/architecture/155-attach-create-if-missing.md`](../../specs/architecture/155-attach-create-if-missing.md) — full architect's spec.
- Issue [#155](https://github.com/pyrycode/pyrycode/issues/155).
