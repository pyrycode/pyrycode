# Spec — supervisor user-turn write surface + `conversation_id` cursor (#312)

## Files to read first

- `internal/supervisor/supervisor.go:54-105` — `Config` + `Supervisor` struct definitions; this slice adds one `Config` field, two `Supervisor` fields, and two exported methods.
- `internal/supervisor/supervisor.go:226-287` — `runOnce` service-mode path (`pty.Start` → `Bridge.SetPTY(ptmx)` → child wait → teardown). New `setPTY` calls bracket the same lifetime as the existing `Bridge.SetPTY` calls.
- `internal/supervisor/supervisor.go:288-349` — `runOnce` foreground-mode path. Same `setPTY`/`clearPTY` bracketing applies (the supervisor tracks ptmx in both modes; only the bridge case mirrors today's pattern).
- `internal/supervisor/bridge.go:242-271` — existing `SetPTY` / `Resize` pair on `Bridge`. The supervisor's new private `setPTY` mirrors this design (mutex-guarded fd registration); the new public `WriteUserTurn` writes directly to that fd, not through the bridge.
- `internal/conversations/registry.go:22-29` — `ErrConversationNotFound` sentinel and the sibling errors. The supervisor never imports this package; the sentinel travels through the validator closure verbatim.
- `internal/conversations/registry.go:128-137` — `(*Registry).Get(id) (Conversation, bool)`. The Pool-side validator wraps this: `if _, ok := r.Get(id); !ok { return conversations.ErrConversationNotFound }`.
- `internal/sessions/pool.go:344-358` — bootstrap supervisor `Config` construction in `New`. One new field assignment (`ValidateConversation`) plumbs the validator.
- `internal/sessions/pool.go:923-945` — `buildSession` (non-bootstrap supervisor construction). Same one-line addition.
- `internal/sessions/session.go:62-98` — `Session` struct (owns `*supervisor.Supervisor`). No edits required by this slice; downstream consumers (#310 handler, #311 bridge) will add accessor methods if they need to reach the new supervisor surface through `*Session`.
- `internal/supervisor/supervisor_test.go:1-90` — `helperConfig` pattern; tests for this slice extend the same fixture.
- `docs/PROJECT-MEMORY.md:20-24` — refusal-to-wire-code mapping convention. The supervisor returns `ErrConversationNotFound` verbatim; the relay handler in the sibling slice does the `errors.Is → CodeConversationNotFound` mapping.

## Context

`internal/supervisor.Supervisor` owns one claude child plus its PTY master. Today the PTY master fd lives entirely inside `runOnce` (and, in service mode, gets handed to the `Bridge` for the iteration's duration). Nothing outside the supervisor can drive an inbound write tagged with a `conversation_id`, and there is no readable cursor for "which conversation is currently active."

Two consumers need that primitive:

1. **`send_message` handler** (sibling slice, #310 successor): consumes an inbound user turn from the phone, looks up the target supervisor, and writes the payload. Validation refusals must surface as `conversations.ErrConversationNotFound` so the handler can map to `CodeConversationNotFound` per the project convention.
2. **Assistant-turn → `message` bridge** (#311): reads the cursor to stamp outbound `message` envelopes with the conversation the supervisor was tracking when claude produced the assistant turn.

This slice introduces the supervisor-side primitive in isolation. No relay wiring, no envelope plumbing, no `Session` accessor methods. The Pool gets one config-field pass-through so the bootstrap and Pool-minted supervisors carry a registry-backed validator into production.

Two design rules from existing conventions shape the design:

1. **Refusal-to-wire-code mapping at the call site** (`docs/PROJECT-MEMORY.md:20`): the supervisor returns the Go sentinel `conversations.ErrConversationNotFound`; the dispatcher/handler maps to `conversation.not_found` at the handler boundary in the sibling slice.
2. **Caller-supplied id validation at the primitive boundary, not the verb handler** (`docs/PROJECT-MEMORY.md:24`): the registry is the primitive that owns conversation existence. The supervisor delegates to it via a closure handed at construction — the supervisor never imports `internal/conversations`.

## Design

### Surface shape — method on `Supervisor`, validator via closure-at-construction

Public API on `*supervisor.Supervisor`:

- `func (s *Supervisor) WriteUserTurn(id string, payload []byte) error`
- `func (s *Supervisor) CurrentConversation() string`

Validator threading via `supervisor.Config`:

- `Config.ValidateConversation func(id string) error` (optional; when nil, `WriteUserTurn` skips validation — production wiring always supplies one; tests opt in per case).

Rationale for the two-shape split:

- **Method, not closure-into-the-consumer**: the cursor lives on the supervisor (per AC #2). A method keeps state + behaviour colocated. Consumers depend on a 2-method interface (in their own package; structural typing) when they want a test stub — that's a cheap two-line declaration on the handler side.
- **Validator as closure-at-construction, not direct registry import**: keeps `internal/supervisor` from depending on `internal/conversations`. The sentinel travels through the closure return value verbatim — the supervisor doesn't know what `ErrConversationNotFound` means, only that it propagates whatever the validator returns.
- **Not a channel**: validation needs a synchronous error return on the unknown-id path; a channel-based surface would require an additional reply path. The cursor's mutex is the only synchronization this primitive needs.

### Internal state added to `Supervisor`

Two independent mutex-guarded fields. Lock order: `convMu` and `ptmxMu` are leaf-only and never held simultaneously by `WriteUserTurn`; the cursor is updated and released before the PTY write acquires `ptmxMu`.

- `convMu sync.Mutex` + `currentConvID string` — the cursor. Zero value `""` means "no user turn has succeeded yet."
- `ptmxMu sync.Mutex` + `ptmx *os.File` — the PTY master fd registered for the current `runOnce` iteration. `nil` between iterations (during backoff) and before the first spawn.

These are distinct from the existing `Supervisor.mu` / `state` pair, which guards `State` snapshots. No new edges to existing locks.

### `WriteUserTurn` contract

Signature: `func (s *Supervisor) WriteUserTurn(id string, payload []byte) error`.

Behaviour:

1. If `s.cfg.ValidateConversation != nil`, call it with `id`. Return the result verbatim on non-nil — typically `conversations.ErrConversationNotFound`, but any sentinel the validator returns passes through unchanged. **No wrapping.**
2. Acquire `convMu`, set `currentConvID = id`, release `convMu`.
3. Acquire `ptmxMu`. If `ptmx == nil` (no active child — between iterations, during backoff, or before `Run` started), release and return `nil` — bytes are dropped silently. The cursor update from step 2 stands.
4. Otherwise, `_, err := s.ptmx.Write(payload)`; release `ptmxMu`; return `err` verbatim (wrapped only with `fmt.Errorf("supervisor: write user turn: %w", err)` if non-nil so callers see a stable prefix).

Notes on the cursor-vs-write ordering:

- The cursor is updated **before** the PTY write so a concurrent `CurrentConversation()` reader sees the new value even if the write blocks (PTY backpressure). #311's bridge will frequently read the cursor at the moment claude produces an assistant turn, which is necessarily after the write returned — but reading "before" the write means the cursor never lags behind an observed PTY effect.
- The cursor update is **not** rolled back on PTY write failure. A failed `os.File.Write` does not mean the cursor is wrong (the conversation is still the one the caller targeted); it means delivery failed, which is a separate concern. The sibling handler slice may choose to NOT emit `ack` on that path — that decision lives there.
- The validator-failure path **does not** mutate the cursor. This is the AC-load-bearing invariant for "unknown-id returns the sentinel" — the supervisor must look unchanged to subsequent observers.

### `CurrentConversation` contract

Signature: `func (s *Supervisor) CurrentConversation() string`.

Behaviour: acquire `convMu`, copy `currentConvID`, release, return the copy. Zero value `""` when no `WriteUserTurn` has succeeded yet. Safe for concurrent use; the read is a simple snapshot, not a subscription.

### PTY fd tracking — private `setPTY` plus `runOnce` edits

Add an unexported method `func (s *Supervisor) setPTY(f *os.File)` that locks `ptmxMu`, assigns `s.ptmx = f`, unlocks. Identical pattern to `Bridge.SetPTY`.

Edits to `runOnce`:

- **Service-mode branch** (currently calls `s.cfg.Bridge.SetPTY(ptmx)` / `s.cfg.Bridge.SetPTY(nil)`): add `s.setPTY(ptmx)` immediately after the `Bridge.SetPTY(ptmx)` call, and `s.setPTY(nil)` immediately before the `Bridge.SetPTY(nil)` call. Pair the two registrations so an out-of-band `WriteUserTurn` racing with iteration teardown sees `nil` rather than a just-closed fd.
- **Foreground-mode branch**: add `s.setPTY(ptmx)` after `pty.Start` succeeds, and `s.setPTY(nil)` immediately before `_ = ptmx.Close()` in the teardown sequence. Foreground mode has no `Bridge` so no `Bridge.SetPTY` calls exist to pair against; the supervisor's own `ptmx` registration is the only registration.

Both modes register and clear under the supervisor's `ptmxMu`. The bracketing matters: `setPTY(nil)` runs **before** the actual `Close` so a `WriteUserTurn` racing with iteration end sees `nil` and drops, rather than writing to a closed fd and getting `EBADF`.

### Config-field plumbing in Pool

Two trivial edits in `internal/sessions/pool.go`:

- `New` (bootstrap supervisor): when `cfg.ConversationsRegistry != nil`, set `supCfg.ValidateConversation = func(id string) error { if _, ok := cfg.ConversationsRegistry.Get(conversations.ConversationID(id)); !ok { return conversations.ErrConversationNotFound }; return nil }`. When `nil` (test default), leave it nil — `WriteUserTurn` skips validation, which is the intended test ergonomics.
- `buildSession` (non-bootstrap supervisors): same closure, reading from `p.convReg`. When `p.convReg` is nil, leave the field unset for symmetry with the bootstrap path.

The closure captures the registry by pointer, so any post-construction `Create` / `Update` / `Delete` on the registry is visible immediately to subsequent `WriteUserTurn` calls — no cache, no snapshot.

The Pool does NOT add any new method or accessor in this slice. Consumer-side mapping (handler-given `conversation_id` → which `*Session` → which `*supervisor.Supervisor`) is the sibling slice's concern, not this one.

## Concurrency model

Three independent mutexes participate in the new code path:

- `Supervisor.mu` — pre-existing; guards `state`. Untouched by this slice.
- `Supervisor.convMu` — new; guards `currentConvID`. Acquired by `WriteUserTurn` and `CurrentConversation`. Leaf-only.
- `Supervisor.ptmxMu` — new; guards `ptmx`. Acquired by `WriteUserTurn` and `setPTY`. Leaf-only.

Lock ordering: `WriteUserTurn` takes `convMu` first (briefly, to update the cursor), releases it, then takes `ptmxMu` (to write to PTY). The two are never nested; no deadlock potential.

`runOnce` calls `setPTY` from the main `Run` goroutine. `WriteUserTurn` calls happen from arbitrary handler goroutines. `ptmxMu` serializes the two. A `WriteUserTurn` arriving mid-teardown (after `setPTY(nil)` but before the actual `Close`) sees `nil` and drops.

The cursor is observable across child restarts: it persists in memory through backoff and respawn. This is intentional — claude's `--continue` reattaches the prior conversation, so the cursor remains valid across restarts. (#311 may revisit this if claude's behaviour diverges.)

## Error handling

- **Unknown `conversation_id`**: validator returns `conversations.ErrConversationNotFound`; `WriteUserTurn` returns it verbatim. `errors.Is(err, conversations.ErrConversationNotFound)` is the handler's branch in the sibling slice.
- **No active child** (PTY not registered): `WriteUserTurn` drops silently and returns `nil`. Documented in the method's doc comment. The choice mirrors `Bridge.Write`'s discard-on-unattached behaviour and avoids forcing every handler to special-case the backoff window. The cursor still updates on this path — see the cursor-vs-write note above.
- **PTY write failure** (e.g., `EIO` on a half-closed master): `WriteUserTurn` returns `fmt.Errorf("supervisor: write user turn: %w", err)`. The handler logs and decides whether to emit `ack` — outside this slice.

## Testing strategy

Same-package tests in `internal/supervisor/supervisor_test.go`. The fixture imports a local sentinel for the validator-failure case so the test does not depend on `internal/conversations`:

```go
var errTestConvNotFound = errors.New("test: conversation not found")
```

Required cases (bullet-pointed scenarios; the developer writes test bodies in the project's table-driven idiom):

- **Happy path** — Construct supervisor via `helperConfig` with a validator that always returns `nil`; `Run` in a goroutine; wait for `PhaseRunning`; call `WriteUserTurn("c-1", []byte("hello\n"))`; assert `err == nil` and `CurrentConversation() == "c-1"`. Assert the child observed `"hello\n"` on stdin (the existing `TestHelperProcess` pattern can echo stdin to a tempfile that the test reads after teardown — see `helperConfig` consumers for the established shape).
- **Cursor read-back across two writes** — Same setup; `WriteUserTurn("c-1", _)` then `WriteUserTurn("c-2", _)`; assert `CurrentConversation() == "c-2"`.
- **Unknown id returns the sentinel and does not mutate cursor** — Validator returns `errTestConvNotFound` for `id == "ghost"`, `nil` otherwise. `WriteUserTurn("c-1", _)` succeeds; then `WriteUserTurn("ghost", _)` returns an error such that `errors.Is(err, errTestConvNotFound)` is true; then `CurrentConversation()` still equals `"c-1"`.
- **Validator nil = skip** — Config with `ValidateConversation == nil`; `WriteUserTurn("anything", _)` succeeds (returns `nil` on PTY write); cursor reflects the id.
- **No-PTY drop** — Construct supervisor but never call `Run`; `WriteUserTurn("c-1", payload)` returns `nil`; `CurrentConversation()` returns `"c-1"` (cursor updates, write is dropped).
- **Cursor concurrency** — `t.Parallel`; goroutines A and B each call `WriteUserTurn` 100 times alternating between two ids; a third goroutine reads `CurrentConversation()` repeatedly. Assert `-race` passes and the final cursor is one of the two ids. (This guards against future refactors that drop the mutex.)

Tests run under `go test -race ./internal/supervisor/...`.

Pool-side wiring is exercised indirectly by the existing pool tests (the new closure must not break `New`). No new pool test is required in this slice — the validator's behaviour is unit-tested at the supervisor level via the local sentinel; the closure's `Registry.Get` mapping is one trivially-correct line. The first end-to-end exercise of the Pool-built validator lands in the sibling handler slice.

## Open questions

1. **Cursor on PTY write failure** — The spec keeps the cursor updated on PTY write failure. Alternative: roll back the cursor when the write fails. The chosen behaviour is "cursor reflects intent, not delivery"; the handler can re-evaluate after the sibling slice ships. If #311 finds this surprising, revisit.
2. **`Session`-level accessors** — This slice deliberately does NOT add `Session.WriteUserTurn` / `Session.CurrentConversation` delegates. The sibling handler slice owns conversation-id → `*Session` resolution and can decide whether to expose the supervisor directly or wrap it. Either way it's a one-line forward.
3. **Foreground-mode behaviour** — `setPTY` registration runs in both modes. Foreground-mode `WriteUserTurn` is functional but has no real consumer (the relay only runs in service mode). The shared code path keeps the implementation minimal; if foreground mode later needs different semantics, mode-gating can be added at the `WriteUserTurn` entry.
