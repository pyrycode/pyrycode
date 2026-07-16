# Spec #1038 — Re-home `list_conversations` + `recent_workspaces` tests off the v1 Dispatcher harness

**Ticket:** #1038 (split from #966) · **Size:** S · **Security-sensitive:** no

## Files to read first

- `internal/relay/handlers/create_conversation_test.go:44-73` — the **direct-call harness to mirror**: `newCreateConvConn` builds `dispatch.NewTestConn(id, out, nil)` + a buffered `out` channel + a `recv()` helper (2s timeout). This is the exact shape both migrated files adopt.
- `internal/relay/handlers/create_conversation_test.go:44-52` — `createConvFirstID = 1` note: on a fresh `NewTestConn`, `NextID` starts at 0 so the first reply lands at id=1 (no gate pre-advance). This is why AC#3 holds by construction.
- `internal/relay/handlers/list_conversations_test.go` — file to migrate (5 test funcs + 4 helpers). Note `TestListConversations_InReplyToAndIDMonotonic:245-273` sends **two frames on the same conn** — the crux case (see § Crux).
- `internal/relay/handlers/recent_workspaces_test.go` — file to migrate (7 test funcs). Its `runRecentWorkspaces:49-60` helper **calls `runListConvDispatcher` + `recvOutbound` defined in the sibling file** — both are deleted, so this helper must become self-contained (see § Cross-file coupling).
- `internal/dispatch/dispatch.go:95-163` — `NewTestConn`, `NextID`, `Reply`, `Send` contracts. `Reply` stamps `ID=NextID()`, `InReplyTo=&req.ID`, `TS=now`, then `Send`s a `RoutingEnvelope{ConnID, Frame}` onto the conn's `outbound` channel. The reply-production path is **unchanged** by this ticket.
- `internal/relay/handlers/list_conversations.go:27-59` / `recent_workspaces.go:44-76` — handler signatures: `ListConversations(reg) dispatch.Handler` / `RecentWorkspaces(reg) dispatch.Handler`, both invoked as `h(ctx, c, env)`. **Neither takes a logger** — the logger today is consumed by `dispatch.Config`, not the handler, so `testLogger(t)` drops out of both migrated files.

## Context

The Mobile Protocol v1 relay path was removed from production in #913 slice 1. `internal/dispatch` still carries the orphaned v1 `Dispatcher` (`New`/`Config`/`Run`/`Register`/`Outbound`) with zero production callers; a follow-on ticket (#966's removal slice) deletes it. These two handler tests are the last consumers of that harness *as a test scaffold*. Re-homing them onto the direct-call pattern every sibling handler test already uses (`create_conversation_test.go` et al.) removes the dependency and unblocks the deletion.

This is a **pure test-harness swap**. No production file changes. The handler code, the reply envelope it produces, and every assertion stay identical — only the plumbing that drives the handler and collects its reply changes.

## Design

### The invariant that makes this safe

In both the old and new harness, the reply is produced by the identical call chain: `handler(ctx, conn, env)` → `c.Reply(ctx, env, type, payload)` → `NextID()` + stamp + `c.Send`. The only thing that changes is **how the handler gets invoked** and **how the reply is read back**:

| Concern | Old (Dispatcher) | New (direct-call) |
|---|---|---|
| Build request | `protocol.Envelope` → `json.Marshal` → `RoutingEnvelope{ConnID, Frame}` | `protocol.Envelope` (no marshal, no wrap) |
| Drive handler | `in <- frame`; dispatcher demuxes, decodes, looks up by Type, calls handler | `h(context.Background(), c, req)` directly |
| Conn | dispatcher-created per ConnID | `c := dispatch.NewTestConn(connID, out, nil)` |
| Read reply | `<-d.Outbound()` | `<-out` (via a `recv()` helper) |
| Logger | `dispatch.Config{Logger: testLogger(t)}` | none (handler takes no logger) |

Because the reply-production path is byte-for-byte identical, **AC#2 (identical reply envelope: Type, ID, InReplyTo, TS, Payload) and AC#3 (first reply ID==1) are preserved structurally**, not by re-tuning. The developer does not re-derive expected bytes; the existing `want…` literals and `decode…Response` helpers carry over unchanged.

### Per-file transform

**`list_conversations_test.go`:**

- **Delete** `runListConvDispatcher` (the `go d.Run(ctx)` + cancel helper) and `recvOutbound(t, d)`.
- **Add** a `newListConvConn(t) (*dispatch.Conn, func() protocol.RoutingEnvelope)` helper mirroring `newCreateConvConn`: buffered `out` channel, `dispatch.NewTestConn(listConvConnID, out, nil)`, a `recv()` closure that reads one `RoutingEnvelope` off `out` with a 2s timeout.
- **Change** `makeListConversationsFrame(t, id) protocol.RoutingEnvelope` → return `protocol.Envelope` instead (drop the `json.Marshal` + `RoutingEnvelope` wrap; keep `ID/Type/TS/Payload:"{}"`). Rename to `makeListConversationsRequest` for clarity (optional but preferred — it no longer produces a frame).
- **Keep** `decodeConversationsResponse(t, out RoutingEnvelope)` as-is — it still receives the `RoutingEnvelope` read off `out` and unmarshals `.Frame`.
- Per test: replace the 3-line `d := dispatch.New(...)` / `d.Register(...)` / `stop := runListConvDispatcher(...)` / `defer stop()` block with `c, recv := newListConvConn(t)`; replace `in <- makeListConversationsFrame(t, N)` + `out := recvOutbound(t, d)` with `h := ListConversations(reg); if err := h(context.Background(), c, makeListConversationsRequest(t, N)); err != nil { t.Fatalf(...) }` then `out := recv()`.
- `testLogger(t)` reference disappears (handler takes no logger).

**`recent_workspaces_test.go`:**

- **Rewrite** `runRecentWorkspaces(t, reg, reqID)` to be **self-contained** (it currently borrows `runListConvDispatcher`/`recvOutbound` from the sibling file, both now deleted): build `out` + `dispatch.NewTestConn(recentWSConnID, out, nil)`, invoke `h := RecentWorkspaces(reg); h(context.Background(), c, makeRecentWorkspacesRequest(t, reqID))`, read one envelope off `out` (2s timeout), decode via `decodeRecentWorkspacesResponse`. Signature and return type (`(protocol.Envelope, protocol.RecentWorkspacesListPayload)`) stay identical, so all 7 call sites are untouched.
- **Change** `makeRecentWorkspacesFrame` → `makeRecentWorkspacesRequest` returning `protocol.Envelope` (same transform as the sibling).
- **Add `"context"` to the import block** — this file does not import it today (it relied on the sibling's helper); the direct call needs `context.Background()`. **Drop `testLogger(t)`.**

### Cross-file coupling (do not miss)

`runRecentWorkspaces` today calls `runListConvDispatcher(t, d)` and `recvOutbound(t, d)`, both defined in `list_conversations_test.go` and both **deleted** by this ticket. If the sibling file is migrated first, `recent_workspaces_test.go` will not compile until `runRecentWorkspaces` is rewritten. Migrate both files in the same change; do not leave a dangling reference to a deleted helper.

`testLogger` stays defined in `register_push_token_test.go` and is used by 9 other sibling test files — removing the two references here does not orphan it.

### Crux: `TestListConversations_InReplyToAndIDMonotonic`

This is the only test that asserts a **second** reply's `ID == 2`. In the old harness that worked because two frames carrying the same `ConnID` routed to the same dispatcher-owned `Conn`, so `NextID` advanced 1→2. In the direct-call path the developer must **reuse a single `*dispatch.Conn` across both handler invocations**:

- Build one conn: `c, recv := newListConvConn(t)`.
- Call `h(ctx, c, makeListConversationsRequest(t, 100))`; `out1 := recv()`; assert `InReplyTo==100`, `ID==1`.
- Call `h(ctx, c, makeListConversationsRequest(t, 200))` **on the same `c`**; `out2 := recv()`; assert `InReplyTo==200`, `ID==2`.

Reading `recv()` after each call (not batching both then reading) keeps the buffered-channel send from any risk of blocking and mirrors the test's existing sequential structure. A naive "fresh conn per call" rewrite would reset `NextID` and make the second reply id=1 — that is the one way to silently break AC#3, so it is called out here explicitly.

## Concurrency model

None. The old harness ran the dispatcher on a background goroutine (`go d.Run(ctx)`) and cancelled it on teardown; the direct-call path is fully synchronous on the test goroutine (`h(...)` returns after `c.Reply`'s `Send` completes). `Send` blocks only if the `out` channel is full — use a buffered `out` channel (mirror `newCreateConvConn`'s buffer of 4), which it never fills for these single-reply tests. Removing the goroutine + `context.WithCancel` teardown is a strict simplification; no new races.

## Error handling

No error paths change. Both handlers only fail on a JSON marshal error of their own payload (unreachable with the test fixtures). The tests already treat a non-nil `h(...)` return as `t.Fatalf` (mirror the sibling), replacing the old harness's implicit "dispatcher logged and dropped" behavior with an explicit assertion — a strengthening that is compatible with "no assertion weakened."

## Testing strategy

The migrated files **are** the tests. Verification:

- `go test -race ./internal/relay/handlers/...` passes (AC#4). All 12 test functions (5 + 7) retain their names, fixtures, and assertions.
- Scenario coverage preserved 1:1 — no case added, dropped, or weakened (AC#2):
  - list: empty registry → `{"conversations":[]}`; single conversation full field projection; archived-flag surfaced; deterministic ordering (LastUsedAt asc, ID tiebreak); InReplyTo + monotonic ID (the crux).
  - recent: empty → `{"workspaces":[]}`; single; dedupe-by-Cwd-keeping-max; ordered most-recent-first; tie-break by Path asc; archived-only workspace included; empty-Cwd skipped.
- `make check` green (AC#5): `gofmt`, `go vet`, `staticcheck`, `go test -race`. In particular, confirm no unused import survives (`context` added to recent_workspaces; verify `time`/`encoding/json` still referenced in both — they are, via `time.Date`/`json.Unmarshal`).
- Grep gate for AC#1: after the change, `grep -nE 'dispatch\.(New|Config|Dispatcher)|d\.(Run|Register|Outbound)' internal/relay/handlers/list_conversations_test.go internal/relay/handlers/recent_workspaces_test.go` returns nothing.

## Open questions

None. The sibling pattern (`create_conversation_test.go`) is a complete, in-package template; the transform is mechanical and the one non-obvious case (shared conn for the monotonic test) is pinned above.
