# ADR 011: Client-side UUID-prefix resolution for `sessions.*` CLI verbs

## Status

Accepted (ticket #99, Phase 1.1d-B2). Second consumer landed in #93 (Phase 1.1c-B2b, `pyry sessions rename` prefix lift) — confirms the helper shape; lift to `internal/sessions` still defers to the third caller (Phase 1.1e `attach`).

## Context

`pyry sessions rm <id>` is the first CLI verb in Phase 1.1 to accept a UUID **or unique prefix** for the `<id>` argument. The same shape will recur in Phase 1.1c (`pyry sessions rename <id> ...`) and Phase 1.1e's `pyry attach <id>` refactor (#49). Prefix resolution can live in one of three places:

1. **Server-side, behind the existing `sessions.rm` verb.** Extend the wire to accept a prefix; map the ambiguous case to a new `ErrCodeAmbiguous` and ship the match list over JSON for the CLI to render.
2. **Server-side, behind a new `sessions.resolve` verb.** Expose `Pool.ResolveID` over the wire; CLI calls `resolve` then `rm`.
3. **Client-side, via `control.SessionsList` (#87) + filtering in the CLI.** Two RTTs per invocation (list + rm) instead of one.

`Pool.ResolveID` already exists internally with documented resolution order (exact match wins; otherwise `strings.HasPrefix` scan; zero/one/many → `ErrSessionNotFound` / canonical id / `ambiguousError`). So the question isn't "do we own this logic?" — it's "where does the resolution boundary sit relative to the wire?"

## Decision

**Resolve client-side via `control.SessionsList`.** The CLI handler:

1. Calls `control.SessionsList(ctx, sock)` to enumerate every session.
2. Walks the list once for an exact match (`s.ID == arg`), returning the ID on hit.
3. Walks again with `strings.HasPrefix(s.ID, arg)` to collect prefix matches.
4. Zero matches → `sessions.ErrSessionNotFound`. One → canonical UUID. Multiple → an unexported `errAmbiguousPrefix` sentinel wrapping the AC-prescribed multi-line `<uuid> <label>` list (ID-sorted; bootstrap with empty label renders as `bootstrap`).

The same helper (`resolveSessionIDViaList`) is the template for #63 (`rename`) and #49 (`attach`). Per the #99 ticket body, **do not extract a shared helper now**; defer that lift-out until the third caller (Phase 1.1e) arrives.

## Rationale

### Why not extend `sessions.rm` to accept a prefix (option 1)

Extending the wire surface means:

- A new `ErrorCode` value (`ErrCodeAmbiguous` joining `ErrCodeSessionNotFound` / `ErrCodeCannotRemoveBootstrap`).
- Deciding how to ship the list of matches over JSON for the CLI to render. The cleanest shape is a typed `Response.Ambiguous []SessionInfo` field, which means `protocol.go` either re-imports `internal/sessions` (breaks the import-free invariant; see ADR-adjacent lessons) or duplicates `SessionInfo`.
- The same change has to ship for `rename` and `attach`. Three verbs × wire change.

The CLI already needs `sessions.list` for `pyry sessions list` (#88). Reusing it costs one extra RTT per `rm` invocation and zero new wire surface.

### Why not a new `sessions.resolve` verb (option 2)

Same wire-extension cost as option 1, plus introduces a "verb that exists only to support another verb" smell. If a future surface really needs server-side prefix resolution (e.g. an HTTP gateway that wants to be stateless), revisit then.

### Why two RTTs is acceptable

`Pool.List` returns an in-memory snapshot (RLock + per-session brief `lcMu`). The wire response carries one `SessionInfo` per session — fields are bounded (UUID + label + state + last-active + bootstrap bit). At Phase 1.1's expected scale (handful to low hundreds of sessions), the JSON payload is sub-kilobyte and the round-trip is sub-millisecond on a Unix socket.

The benefit is a stable wire surface. The same `resolveSessionIDViaList` satisfies every future `<id>`-taking verb without a wire change each time. If profiling ever shows the second RTT as load-bearing, the lift to server-side is a localised refactor with no client-visible contract change.

### Why mirror `Pool.ResolveID`'s exact-then-prefix order

A user typing a full canonical UUID expects the verb to work even though every UUID is also a prefix of itself. Skipping the exact-match short-circuit and going straight to `HasPrefix` would still match correctly for full UUIDs (no two UUIDs share a 36-char prefix), but `Pool.ResolveID`'s exact-first order is documented contract. Mirroring it client-side means the CLI's behaviour matches the reference if we ever do shift to server-side resolution.

### Why each AC-prescribed message bypasses `main`'s `pyry: ` prefix

`main` prepends `pyry: ` to any error returned from `run()`. The AC for `sessions rm` specifies three message texts verbatim **without** that prefix:

- `<uuid> <label>` per match line (ambiguous prefix)
- `no session with id "<arg>"` (unknown UUID/prefix)
- `cannot remove bootstrap session` (bootstrap rejection)

The handler emits these via `fmt.Fprintln(os.Stderr, ...)` + `os.Exit(1)`, identical in shape to `runAttach`'s exit-2 usage path. `os.Exit` skips the deferred `cancel()` of the 30s timeout context, but the only resource involved is a process-local timer reaped on exit; no socket / file / goroutine outlives the call.

Any other error (e.g. dial failure on a stopped daemon) flows through `fmt.Errorf("sessions rm: %w", err)` → `pyry: sessions rm: …`, matching `runStop` / `runStatus`.

## Consequences

### Positive

- **Wire surface stays stable.** `sessions.rm` accepts canonical UUIDs only; the wire contract is unchanged from #98. Every future `<id>`-taking verb gets the same client-side prefix support without server work.
- **Output formatting lives where the operator sees it.** The AC#3 `<uuid> <label>` format is a CLI concern, not a wire concern. Server-side resolution would have forced the wire to ship match metadata in a CLI-friendly order; client-side keeps presentation cleanly separated.
- **Mirroring `Pool.ResolveID` keeps client and server behaviour interchangeable.** A future migration to server-side resolution is a localised refactor.

### Negative

- **Two RTTs per `rm` invocation.** Sub-millisecond on a Unix socket; not load-bearing at Phase 1.1 scale.
- **TOCTOU race window.** `SessionsList` returns canonical UUID `X`; another caller runs `pyry sessions rm X` before our `SessionsRm` lands; the wire returns `ErrSessionNotFound`. The CLI surfaces the **operator's typed `<id>`** (possibly a prefix), not the canonical UUID — preserves debugging context. No retry; let the operator re-list.
- **Ambiguous-output format duplicated between `Pool.ambiguousError` and `resolveSessionIDViaList`.** The server-side renderer uses `<uuid> (<label>)` (parens); the client uses `<uuid> <label>` (space) per AC#3. The duplication is intentional — same data, different consumers. If the wire ever returns ambiguity, the server should adopt the CLI's space form for consistency.

### Neutral

- **No shared helper extraction yet.** Phase 1.1c-B2b (`rename`, #93) is now the second caller; Phase 1.1e (`attach`) will be the third. The third-caller architect makes the lift-out call, per the #99 ticket body. Caller #2's surgical insertion (one resolver call + one ambiguous-prefix branch in the existing error switch — the rename handler has no bootstrap-rejection branch, but is otherwise line-for-line `runSessionsRm`'s shape) confirmed the helper's contract scales as-is to a second consumer with zero modification; the lift's case strengthens with each additional consumer but the trigger remains "third caller".

## Alternatives considered

- **Add `ErrCodeAmbiguous` + ship match list in `Response`.** Rejected — wire-surface growth for a CLI-presentation concern.
- **New `sessions.resolve` verb.** Rejected — "verb that exists only to support another verb" anti-pattern, plus same wire-surface cost.
- **Inline the resolution into `runSessionsRm` without a helper.** Rejected — three callers in two phases; unit-testability of the resolution rules is worth the small named function.

## References

- [`features/control-plane.md` § `runSessionsRm` handler (1.1d-B2)](../features/control-plane.md#runsessionsrm-handler-11d-b2) — implementation walkthrough.
- [ADR 010](010-sessions-cli-sub-router.md) — sub-router shape, the host this verb plugs into.
- `internal/sessions/pool.go` — `Pool.ResolveID` reference implementation; the resolution-order contract this CLI mirrors.
- `docs/specs/architecture/99-cli-sessions-rm.md` — full architect's spec.
