# 303 — `list_conversations` handler + dispatcher route

Slice off #297. Plugs the `list_conversations` verb into the per-conn dispatcher (#307). One handler, one registration call in `cmd/pyry`, one test file. Read-only; no registry mutation.

## Files to read first

- `internal/dispatch/dispatch.go:53-110` — `Handler` contract + `Conn.Reply` semantics (this handler emits exclusively via `Conn.Reply`; does NOT stamp `id` / `ts` / `in_reply_to` itself).
- `internal/dispatch/dispatch.go:236-249` — `Dispatcher.Register` contract (must be called before `Run`; duplicate registration panics).
- `internal/dispatch/dispatch_test.go:199-231` — `TestReply_InReplyToMatchesRequest`: this is the exact test seam shape this slice's tests reuse (push frame on `in`, read from `d.Outbound()`, decode and assert).
- `internal/conversations/registry.go:140-167` — `ListFilter` + `Registry.List` — the only registry method this handler calls. `List` returns a copy, takes the registry mutex internally.
- `internal/conversations/registry.go:72-83` — `Registry.Save`'s sort key (`LastUsedAt` asc, ties broken by `ID` asc). The handler MUST mirror this exact ordering before reply so byte-deterministic output matches the registry's serialized order.
- `internal/conversations/conversation.go:29-72` — `Conversation` struct. Note: there is no `LastMessageTS` field on `Conversation` today; the projection collapses it onto `LastUsedAt` (see "LastMessageTS source" below).
- `internal/protocol/conversations_read.go` — `ListConversationsPayload` (empty by spec), `ConversationsPayload`, `ConversationSummary` (declaration order is the wire order).
- `internal/protocol/codes.go:48-49` — `TypeListConversations` / `TypeConversations`.
- `cmd/pyry/relay.go:81-179` — `startRelay`: where `dispatch.New` is called. The new `d.Register(protocol.TypeListConversations, …)` site goes here, after `dispatch.New` and before the `go d.Run(ctx)` goroutine launches.
- `cmd/pyry/main.go:408-460` — `runSupervisor` already loads `convReg` (line 420) before calling `startRelay` (line 456). No load reorder needed — only thread the existing variable into `startRelay`.
- `internal/relay/handlers/register_push_token.go` — package layout reference (doc-comment style, `package handlers`). **DO NOT mirror the function signature** — it predates `dispatch.Handler`. The ticket body is explicit about this.
- `docs/PROJECT-MEMORY.md` § "Refusal-to-wire-code mapping is the consumer's job" — primitives return Go values; wire-code mapping happens at the dispatcher call site. The registry's `List` cannot fail, so no error envelope is wired in this slice.
- `docs/protocol-mobile.md:338-376` — wire example for `list_conversations` / `conversations`.

## Context

`internal/conversations/registry.go` is the on-disk truth for conversations. `internal/dispatch` (#307) is the per-conn handler-table demultiplexer. `internal/protocol/conversations_read.go` defines the wire payload shapes. All three exist; this ticket is the small load-bearing wire between them — decode the request, read the registry, project to wire types, reply.

The dispatcher today returns `protocol.unsupported` for every envelope type that has no handler registered (`dispatch.go:425-428`). Once this slice lands, `list_conversations` no longer falls through.

## Design

### Package and file

New file: `internal/relay/handlers/list_conversations.go` (same `package handlers` as `register_push_token.go`). One exported function returning a `dispatch.Handler`:

```go
// ListConversations returns a dispatch.Handler that answers a
// list_conversations request with a conversations envelope.
func ListConversations(reg ConversationLister) dispatch.Handler
```

### Registry interface (defined at the handler site)

A single-method interface declared in `list_conversations.go`:

```go
// ConversationLister is the minimal surface this handler consumes from
// the conversations registry. *conversations.Registry satisfies it
// structurally; no adapter required.
type ConversationLister interface {
    List(filter ...conversations.ListFilter) []conversations.Conversation
}
```

- Defined at the consumer, per CODING-STYLE.
- Variadic shape matches `Registry.List` exactly — the handler passes no filter, but the type must match for structural satisfaction.
- No export from `internal/conversations` is required; the type lives only here.

### Handler body — contract sketch

The handler is a closure over `reg`. Signature: `func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error`. Steps:

1. Call `reg.List()` (no filter — the wire spec lists every conversation regardless of `IsPromoted`).
2. Sort the returned slice in place: `LastUsedAt` ascending; if equal, `ID` ascending. Use `sort.SliceStable` so the impl matches `Registry.Save`'s ordering byte-for-byte (the registry uses `SliceStable` too).
3. Allocate `out := make([]protocol.ConversationSummary, 0, len(list))` and project each `Conversation` row (see "Projection rules" below).
4. Marshal `protocol.ConversationsPayload{Conversations: out}` to `json.RawMessage`.
5. Return `c.Reply(ctx, env, protocol.TypeConversations, payloadJSON)` — the dispatcher's helper sets `id` (per-conn counter), `ts` (`time.Now().UTC()`), and `in_reply_to: env.ID` automatically. The handler MUST NOT stamp those fields itself.

The handler does NOT unmarshal `env.Payload` into `ListConversationsPayload` — that payload is `struct{}` by spec, has no fields to inspect, and an empty/missing payload is the only legal shape. The dispatcher has already established that `env.Type == TypeListConversations` and `IsV1Compatible(env) == nil`; nothing more to validate.

The handler returns the error from `c.Reply` directly. The dispatcher logs handler errors at WARN; nothing else is wired today (per `dispatch.go:431-434`).

### Projection rules — `Conversation` → `ConversationSummary`

| `ConversationSummary` field | Source from `Conversation` |
|---|---|
| `ID` (string) | `string(c.ID)` |
| `Name` (`*string`) | `c.Name` — pointer copy. `nil` is meaningful (unnamed conversation → `"name": null` on the wire) and MUST be preserved; `omitempty` would break the round-trip, so the wire type already omits the tag. |
| `IsPromoted` (bool) | `c.IsPromoted` |
| `Cwd` (string) | `c.Cwd` |
| `LastMessageTS` (`time.Time`) | `c.LastUsedAt` — see "LastMessageTS source" below |
| `LastUsedAt` (`time.Time`) | `c.LastUsedAt` |

### `LastMessageTS` source

`Conversation` does not carry a separate "last message ts" today; the registry only tracks `LastUsedAt`. The protocol spec example (`docs/protocol-mobile.md:358-372`) shows both timestamps equal on each row, so projecting `LastMessageTS = c.LastUsedAt` is consistent with the published wire shape. This is a v1 simplification, not a bug — when a distinct `LastMessageTS` is introduced on the registry side (out of scope here), the projection updates in one place.

A short comment in `list_conversations.go` next to the projection MUST name this collapse so a future reader does not "fix" it by leaving `LastMessageTS` zero.

### Wiring in `cmd/pyry`

Two edits, both small and additive:

1. `cmd/pyry/relay.go`: extend `startRelay`'s signature with one trailing parameter — `convReg *conversations.Registry`. After `dispatch.New(...)` (currently at `:122-126`) and **before** the `go d.Run(ctx)` goroutine launch, add:

   ```go
   d.Register(protocol.TypeListConversations, handlers.ListConversations(convReg))
   ```

   Add `"github.com/pyrycode/pyrycode/internal/conversations"` and `"github.com/pyrycode/pyrycode/internal/relay/handlers"` to the import block.

   When `relayURL == ""` (relay disabled), `startRelay` still returns the no-op cleanup early; no dispatcher is constructed; the `convReg` parameter is accepted but unused on that path. That's intentional — uniform signature, no conditional plumbing.

2. `cmd/pyry/main.go`: `runSupervisor` already loads `convReg` at line 420 (current code) before calling `startRelay` at line 456. Update the single call site to pass `convReg`. No load reorder; no other change.

### Coordination with #313 (`send_message` handler)

Sibling ticket #313 (per `origin/feature/313`'s spec) also plans to extend `startRelay`'s signature with a trailing parameter (`handlers.TurnWriter`). Both this ticket and #313 are "append a parameter at the end" changes. The merge surface is one line in `runSupervisor`'s call to `startRelay` and one line in `startRelay`'s parameter list. Whichever ticket lands second rebases with a trivial textual merge — the two parameters are independent.

**Do not attempt to share scaffolding between #303 and #313 here.** Each ticket plugs its own handler; coupling them adds a coordination point that does not pay back.

## Concurrency

- The handler runs on the per-conn goroutine (one frame at a time per `ConnID`). No goroutines spawned by this slice.
- `Registry.List` takes the registry's mutex internally and returns a copy of the slice. The slice and its elements are safe to mutate (sort + project) without further locking.
- `c.Reply` → `c.Send` blocks on dispatcher outbound backpressure; that's the intended flow-control surface. Returns `ctx.Err()` on cancel; the handler propagates that error up to the dispatcher unchanged.

## Error handling

- Registry read cannot fail. `Registry.List` returns `[]Conversation` only; no error path. No wire-code mapping needed for this slice.
- `json.Marshal(ConversationsPayload)` cannot realistically fail — all fields are stdlib-marshalable (strings, bool, `time.Time`, `*string`). If it does (e.g. a future field type), the handler returns the wrapped error to the dispatcher, which logs at WARN. No error envelope is emitted to the phone because the request was structurally valid; emitting `protocol.malformed` for an internal marshal bug would be misleading.
- `c.Reply` errors (ctx cancel / outbound channel closed) are returned verbatim. The dispatcher's WARN log is sufficient — the conn is shutting down anyway.

## Testing strategy

New file: `internal/relay/handlers/list_conversations_test.go` (same `package handlers`).

Use the real dispatcher as the test seam (matches `dispatch_test.go`'s pattern). Construction shape per test:

- Build a `*conversations.Registry` directly (`&conversations.Registry{}` + `reg.Create(...)`) — no file I/O.
- `in := make(chan protocol.RoutingEnvelope, 1)`, `d := dispatch.New(dispatch.Config{Frames: in, Logger: testLogger(t)})`, `d.Register(protocol.TypeListConversations, ListConversations(reg))`, then run the dispatcher in a goroutine matching the pattern in `dispatch_test.go:53-67`.
- Push a `list_conversations` frame with a fixed `ID` on `in`; read from `d.Outbound()`; decode the inner envelope and payload; assert.

Scenarios (one test function each):

- **Empty registry** — empty `conversations` slice, `in_reply_to` matches request id, `Type == TypeConversations`. Asserts the empty list is serialized as an empty JSON array (not `null`); follows `make([]ConversationSummary, 0, ...)` rather than `var out []ConversationSummary`.
- **Single conversation** — one record with non-nil `Name`, non-zero `LastUsedAt`. Projection round-trips byte-equivalent to a hand-built expected `ConversationSummary` (timestamps compared via `time.Time.Equal`, per `docs/PROJECT-MEMORY.md` § "`time.Time` round-trip discipline").
- **Multiple conversations, deterministic order** — three records seeded in an order opposite to the expected sort. After the handler runs, the response's `Conversations` slice is in `LastUsedAt` ascending order; one tie on `LastUsedAt` resolves by `ID` ascending. The exact byte-equivalence of the encoded payload against a fixed expectation pins the ordering invariant.
- **`in_reply_to` correctness** — covered implicitly by the three above (each asserts it), but one focused test checks two consecutive requests on the same conn: each response's `in_reply_to` matches its own request id (not the previous one) and the outbound `id` field increments monotonically from the dispatcher's per-conn counter.

Test ordering is deterministic in all four scenarios — no `time.Sleep`, no `time.Now`-comparison, no goroutine race. Each test creates its own dispatcher and tears it down via `cancel()` per the existing `runDispatcher` helper pattern (copy locally; do not import from `internal/dispatch`'s test file).

No time-injection seam is introduced. The dispatcher stamps `ts := time.Now().UTC()` inside `Conn.Reply` and the tests verify the response by `time.Time.Equal`-ish checks (`!ts.IsZero()` is enough — the slice's contract is "valid timestamp"); the wire's `ts` is not byte-pinned in test goldens.

## Out of scope

- Permission filtering by `IsPromoted` (the ticket explicitly says "no filter"; the wire spec lists every conversation).
- Streaming / paginated responses (single envelope reply).
- `register_push_token`'s legacy parameter shape (left untouched; #313's spec also calls this out as out of scope for its slice).
- `LastMessageTS` as a distinct registry field — deferred until a real message-tracking ticket needs it.
- DoS resistance for huge registries (transport cap inherited from `internal/transport`; per-handler limits are explicitly out of scope per `internal/dispatch/dispatch.go:22-26`).
- Error envelopes — there is no failure mode that maps to a wire code on the happy path (per the registry's no-fail contract).

## Open questions

None. Every projection rule is fixed; every dependency lands at a stable seam; the test seam is the same one #307 already exercises.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The only data crossing into untrusted territory (binary → phone) is the conversation list. The phone is already authenticated by `FirstFrame` (#308) before this handler runs — by the time the dispatcher routes to `ListConversations`, the conn has cleared the gate. The handler reads from a trusted in-process registry only; no parsed-from-wire data is acted upon (`ListConversationsPayload` is `struct{}` by spec — the handler does not even unmarshal it).
- **[Tokens]** No findings. Handler touches no token material. Device-token hashes live in `internal/devices`; push tokens in `internal/devices.Device.PushToken`; conversation registry has none of either. The handler does not read either registry.
- **[File operations]** No findings. Read-only handler; no file I/O. Registry was loaded once at daemon startup (`cmd/pyry/main.go:420`); this handler holds a `*conversations.Registry` reference and calls `List` only.
- **[Subprocess]** No findings. No `exec.Command`, no shell, no subprocess interaction in the read path.
- **[Cryptography]** No findings. No RNG, no key material, no comparison of secrets.
- **[Network & I/O]** Reply size scales as O(N) in registry length. For a registry with thousands of conversations the response could be large (each row ~200 bytes JSON). The transport cap in `internal/transport`'s WS read path is the only ceiling today; the dispatcher and verb slices intentionally do not re-enforce per-handler caps (`dispatch.go:24-27`). SHOULD FIX in a separate ticket if real registries reach four-figure sizes — escalate then per "Evidence-Based Fix Selection". OUT OF SCOPE here.
- **[Error messages / logs]** The handler should not log `Cwd`, `Name`, or `ID` of conversation rows at INFO. A DEBUG-level "responded with N conversations" log keyed on `conn_id` is fine. No payload bytes in logs (matches the dispatcher's posture at `dispatch.go:31-34`). No error envelope is built, so no risk of leaking decode-error text on the wire.
- **[Concurrency]** `Registry.List` takes its own mutex and returns a copy; the handler mutates only its local slice. Two concurrent `list_conversations` requests (different conns) each get an independent snapshot. No lock ordering concern (only one lock involved, taken inside `List`).
- **[Threat model alignment]** Relevant threats from `docs/protocol-mobile.md` § Security model:
  - *Threat 1 (prompt injection)*: out of scope — this is a read-only handler; no `claude` input path.
  - *Threat 3 (relay MITM)*: out of scope — the relay sees ciphertext-or-plaintext per v1's `payload_encrypted: false`; no additional v1 surface added here. Conversation `Cwd` / `Name` / IDs reach the relay operator in v1 — same posture as every other binary→phone payload. The ticket does not change this.
  - *Threat 5 (impl bugs)*: addressed via `gosec` + `govulncheck` in CI; the handler has no `os/exec`, no path concat, no `unsafe`.
  - *Threat 7 (DoS)*: see "Network & I/O" finding above; deferred consistently with v1's overall posture.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-13
