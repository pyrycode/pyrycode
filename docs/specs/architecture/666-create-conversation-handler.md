# Spec — `create_conversation` handler on the v2 phone-protocol dispatch (#666)

## Files to read first

- `internal/relay/handlers/list_conversations.go:14-59` — the closest analog: the minimal consumer-declared interface (`ConversationLister`), the read-then-`c.Reply(...)` shape, and the `protocol.Type*` reply discipline. The new handler mirrors this structure exactly.
- `internal/relay/handlers/send_message.go:31-83` — the malformed-payload reject pattern: a *static* user-facing message (never echo decoded bytes), `replyError(ctx, c, env, protocol.CodeProtocolMalformed, msg, false)`, and the `logger.Warn("…malformed payload", "event", …, "conn_id", …)` field convention. Reuse verbatim.
- `internal/relay/handlers/register_push_token.go:106-124` — `replyAck` / `replyError` helpers (package-private, already available to the new file). The new handler uses `replyError`; success goes through `c.Reply` directly (it returns a typed payload, not an ack).
- `internal/protocol/conversations_write.go:5-33` — `CreateConversationPayload` (request: all three fields `*`-nullable → server fills defaults) and `ConversationCreatedPayload` (reply: `ID`, `IsPromoted`, `Cwd`, `Name *string`, `LastUsedAt`). Field semantics + nullability rationale.
- `internal/protocol/testdata/create_conversation.json` + `conversation_created.json` — the exact wire shapes. Note the request fixture is `{"is_promoted":false,"name":null,"cwd":null}` (the common "scratch discussion" path) and the reply carries `in_reply_to`. The handler must produce a reply byte-compatible with `conversation_created.json`'s field set.
- `internal/conversations/registry.go:118-137` — `Registry.Create(Conversation)` (caller owns id/uniqueness; in-memory append) and `Registry.Get(id)` (the validity check `send_message` already performs via the pool's `ValidateConversation`). `Registry.Save(path)` at `:72-116` — atomic temp+rename, snapshot-under-lock, mode `0600`/`0700`.
- `internal/conversations/id.go:10-19` — `conversations.NewID()` (UUIDv4 via `crypto/rand`; errors only on rng failure).
- `internal/conversations/sweep_loop.go:14-63` — the *only* current persistence writer: `RunSweepLoop` Saves **lazily** (only on a non-zero archive tick). This is why a created row is otherwise non-durable until the next archive — the basis for this spec's eager-Save decision.
- `cmd/pyry/relay.go:154-163` (v1 `d.Register(...)` block) and `:296-300` (v2 `Handlers:` map) — the two registration sites. Both already register `ListConversations` / `RegisterPushToken` / `SendMessage`; this ticket adds one line to each.
- `cmd/pyry/relay.go:88-100` (`startRelay` signature) + `:269-282` (`startRelayV2` signature) + `cmd/pyry/main.go:489` (the single `startRelay` call site) — where the new `defaultCwd` argument threads through.
- `cmd/pyry/main.go:402` (`pyry-workdir` flag, default `""`) + `:118-130` (`resolveClaudeSessionsDir`, the `filepath.Abs(workdir)` + getwd-fallback idiom to mirror) — source for the default cwd.
- `internal/relay/handlers/send_message_test.go:64-86` (`newSendMsgConn` → `dispatch.NewTestConn`) and `register_push_token_test.go:39-58` (`newTestConn`) — the **light** handler-test idiom: build a `*dispatch.Conn` directly via `dispatch.NewTestConn(connID, out, dev)`, invoke the handler closure, read the outbound envelope off the channel. Prefer this over `list_conversations_test.go`'s full-dispatcher loop.

## Context

The mobile rung-3 interactive-stream e2e (pyrycode-mobile#421) was driven live on 2026-06-18 and fails at discussion creation: tapping "New discussion" sends a `create_conversation` frame over the Noise v2 channel, but `cmd/pyry/relay.go` registers only `list_conversations` / `register_push_token` / `send_message` on both dispatch paths. With no handler, `dispatch.Route` returns `protocol.unsupported` ("no handler registered"); the mobile `createDiscussion` awaits `conversation_created`, receives the error, throws, and the app never navigates into the thread.

The wire contract is already in the tree (`internal/protocol`: types, payloads, round-trip tests, fixtures). What is missing is purely the daemon handler and its registration on both paths.

This ticket carries `security-sensitive`: it is an inbound handler on the internet-exposed v2 Noise dispatch that accepts a remote frame from a paired (but untrusted-by-default) mobile client and creates persistent server state, including a caller-influenced `cwd`. The security review pass is at the end of this spec.

## Design

### New file: `internal/relay/handlers/create_conversation.go`

A single `dispatch.Handler`-returning constructor, structurally a sibling of `ListConversations` / `SendMessage`.

**Consumer-declared interface** (minimal surface, mirrors `ConversationLister`):

```go
// ConversationCreator is the write surface this handler consumes.
// *conversations.Registry satisfies it structurally.
type ConversationCreator interface {
    Create(c conversations.Conversation)
    Save(path string) error
}
```

**Constructor signature:**

```go
func CreateConversation(reg ConversationCreator, registryPath, defaultCwd string, logger *slog.Logger) dispatch.Handler
```

- `reg` — the conversations registry (production: the same `*conversations.Registry` passed to `ListConversations`).
- `registryPath` — the on-disk `conversations.json` path, for the eager Save (see Persistence below). Computed in `startRelay`/`startRelayV2` via the existing `resolveConversationsRegistryPath(instanceName)` (no new param needed for the path).
- `defaultCwd` — the absolute cwd to record when the payload's `cwd` is `null`. Threaded in from `main.go` (see Wiring).
- `logger` — daemon slog logger.

**Handler behavior** (the returned closure; contract, not code):

1. Decode `protocol.CreateConversationPayload` from `env.Payload`. On decode error → `logger.Warn` with `event=create_conversation.malformed` + `conn_id`, then `replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgCreateConversationMalformed, false)`. The decode-error text is **never** echoed (could reflect attacker bytes); a static `const msgCreateConversationMalformed = "malformed create_conversation payload"` is the only user-facing string — identical discipline to `send_message.go:31-35`.
2. Resolve the three nullable fields to effective values:
   - `promoted := p.IsPromoted != nil && *p.IsPromoted` (default `false`).
   - `cwd := defaultCwd; if p.Cwd != nil { cwd = *p.Cwd }`.
   - `name := p.Name` (pointer passthrough; `nil` stays `nil` — an unnamed scratch discussion).
3. Mint `id, err := conversations.NewID()`. On error (rng failure — effectively unreachable on Linux/macOS `crypto/rand`): `logger.Error` with `event=create_conversation.id_failed`, then `replyError(…, protocol.CodeServerBinaryOffline, msgCreateConversationServerError, true)` (retryable; the only retryable server-side code today, matching `send_message`'s catch-all). Do **not** return a bare error — that yields no reply and hangs the phone's `await`.
4. `now := time.Now().UTC()`. Build `conversations.Conversation{ID: id, Name: name, Cwd: cwd, IsPromoted: promoted, LastUsedAt: now}` and `reg.Create(conv)`.
5. **Eager best-effort persist:** `if err := reg.Save(registryPath); err != nil { logger.Error("create_conversation: persist failed", "event", "create_conversation.persist_failed", "err", err) }`. Save failure is **non-fatal** — the row is live in-memory and immediately usable; durability is best-effort, exactly as `RunSweepLoop` treats its Save (sweep_loop.go:26-30). Do not fail the reply.
6. Marshal `protocol.ConversationCreatedPayload{ID: string(id), IsPromoted: promoted, Cwd: cwd, Name: name, LastUsedAt: now}` and `return c.Reply(ctx, env, protocol.TypeConversationCreated, payloadJSON)`. `Conn.Reply` (dispatch.go:153) stamps `ID` (per-conn monotonic), `TS`, and `InReplyTo=env.ID`.
7. `logger.Info` ack with `event=create_conversation.created` + `conn_id` + `conversation_id`. The `name` and `cwd` are operator-owned metadata; logging `conversation_id` matches the `send_message.ack` convention. Do not log `name`/`cwd` payload values at Info (low value; keep the log line lean) — `conversation_id` is the correlator.

### Key decision — eager persistence (divergence from the lazy sweep)

`Registry.Create` is in-memory only; the sweep loop Saves **only on a non-zero archive tick**. A freshly created conversation is therefore non-durable — on the next `pyry start` (daemon restart, which a *crash-recovery supervisor* does routinely), `list_conversations` would return a registry missing every conversation created since the last archive. For a product whose defining promise is surviving restarts, a create path that silently drops user-created state on restart is a self-contradiction.

The handler therefore Saves eagerly after Create. This **matches the established precedent**: `pyry pair` Saves the device registry immediately after `Add` (`cmd/pyry/pair.go:210,367`) — both are user-initiated genesis-of-state events. The sweep loop's laziness is correct for *its* job (a background bulk archive, tolerant of an hour's delay); it is the wrong model for an interactive create. Save is best-effort/non-fatal so a transient disk error never blocks a discussion from being created (the row works in-memory regardless).

### Key decision — no session spawn at create time (no split)

Confirmed per the ticket's evidence: `send_message` writes to the bootstrap session via `TurnWriter.WriteUserTurn`; conversation validity is just `convReg.Get` (the pool's `ValidateConversation` closure, `internal/sessions/pool.go`). A `Registry.Create`d row validates immediately, so a created conversation accepts a `send_message` on the same id with no claude session spawned at create time — lazy bind on first turn already works. **No session-binding split is needed.** AC#3's "immediately accepts a send_message" is satisfied by the in-memory Create alone.

### Key decision — `conversation_updated` broadcast is out of scope

The AC asks only for the `in_reply_to` reply to the creator. Broadcasting `conversation_updated` to the operator's other phones (a `ConversationUpdatedPayload` exists) is deferred — not required to unblock pyrycode-mobile#421, and adds a fan-out surface this ticket does not need. Named as out of scope; a future ticket picks it up if multi-phone create-sync is wanted.

### Wiring (`cmd/pyry/relay.go` + `cmd/pyry/main.go`)

- **Register on both paths.** v1 block (`relay.go:160-162`): add
  `d.Register(protocol.TypeCreateConversation, handlers.CreateConversation(convReg, resolveConversationsRegistryPath(instanceName), defaultCwd, logger))`.
  v2 `Handlers` map (`relay.go:297-299`): add
  `protocol.TypeCreateConversation: handlers.CreateConversation(convReg, resolveConversationsRegistryPath(instanceName), defaultCwd, logger),`.
  The handler is the same `dispatch.Handler` type on both paths; the v2 manager runs it through the identical `dispatch.Conn`/`Reply` machinery (proven by `ListConversations` working on both paths today).
- **Thread `defaultCwd`.** Add `defaultCwd string` to `startRelay` and `startRelayV2` signatures; `startRelay` forwards it to `startRelayV2`. Single `startRelay` call site is `main.go:489` (confirmed via `codegraph_impact startRelay` — only `runSupervisor` calls it).
- **Compute `defaultCwd` once in `main.go`** (just before the `startRelay` call), mirroring `resolveClaudeSessionsDir`'s resolution: the absolute form of `*workdir`, or `os.Getwd()` when `*workdir == ""`. A small `resolveDefaultCwd(workdir string) string` helper in `cmd/pyry` (returns an absolute path; falls back to the raw value on `Getwd` error) keeps the resolution testable and the call site one line. Record it as the conversation's recorded cwd so the row's `Cwd` matches where the bootstrap session actually runs.

## Concurrency model

No new goroutines. The handler runs synchronously on the dispatcher's per-conn goroutine (the same context every handler runs in). `Registry.Create` / `Get` / `Save` are all mutex-guarded and safe for concurrent use.

The eager `Save` can race the sweep loop's `Save` on the same path: each snapshots the registry under `r.mu`, writes a private temp file, and `os.Rename`s into place (atomic commit). Both snapshots include the freshly-appended row (it was `Create`d before either Save snapshots), so no row is lost — the only contention is byte-level last-writer-wins on the final file, both versions containing the new row. No new lock ordering is introduced (the handler never holds two locks).

## Error handling

| Failure mode | Reply | Retryable | Logged |
|---|---|---|---|
| Payload JSON decode fails | `protocol.malformed`, static message | false | `Warn`, `event=create_conversation.malformed`, no payload bytes |
| `conversations.NewID()` rng failure (≈unreachable) | `server.binary_offline`, static message | true | `Error`, `event=create_conversation.id_failed` |
| `reg.Save` fails | *(none — success reply still sent)* | — | `Error`, `event=create_conversation.persist_failed` |
| Reply marshal fails | bare error to dispatcher (no reply path) | — | dispatcher logs |
| Happy path | `conversation_created` + payload | — | `Info`, `event=create_conversation.created`, `conversation_id` |

There is no `conversation.not_found` path here — create is the genesis, not a lookup.

## Testing strategy

New file `internal/relay/handlers/create_conversation_test.go`, same-package, table-driven where natural, stdlib only, using the light `dispatch.NewTestConn` idiom (a real `*conversations.Registry` + a `t.TempDir()` registry path — no mocks). Scenarios (bullet, not pre-written code):

- **Happy path, all-null payload** (mirrors `create_conversation.json`): `{"is_promoted":false,"name":null,"cwd":null}` → assert (a) reply `Type == conversation_created`, (b) `InReplyTo` points to the request id, (c) `Conn.NextID` monotonicity (first reply id == 1 on a fresh conn), (d) reply payload `Cwd == defaultCwd`, `IsPromoted == false`, `Name == nil`, `LastUsedAt` non-zero, `ID` is `conversations.ValidID`-shaped, (e) the registry now has exactly one row whose `ID` equals the reply's `ID` (via `reg.Get`), with matching `Cwd`/`IsPromoted`/`Name`.
- **Explicit non-null fields**: `is_promoted=true`, `name="proj"`, `cwd="/work/proj"` → reply and stored row carry those exact values (cwd is **not** overridden by `defaultCwd`); compare `LastUsedAt` via `time.Time.Equal`, never `==`.
- **Eager persistence**: after the happy-path call, `conversations.Load(registryPath)` returns a registry containing the created row (proves the on-disk Save happened, i.e. survives a daemon restart). Asserts the eager-Save decision, not just in-memory state.
- **Malformed payload**: `env.Payload = []byte("{")` → reply `Type == error`, decoded `ErrorPayload.Code == protocol.CodeProtocolMalformed`, `Retryable == false`, message is the static `msgCreateConversationMalformed` (asserts the decode-error text is not echoed), and the registry is left empty (no partial row).
- **`send_message` accepts the new id** (AC#3, unit-level): after creating a conversation, `convReg.Get(ConversationID(replyID))` returns `(_, true)` — the same predicate the pool's `ValidateConversation` uses. This proves the created row is immediately valid for a follow-up `send_message` without spinning the supervisor.

`go test -race ./internal/relay/handlers/...` must pass. The existing `internal/protocol` round-trip tests already cover the wire shapes; do not duplicate them.

AC#3's full end-to-end leg (real phone / pyrycode-mobile#421 rung-3) is **operator-verified**, not a Go test — it exercises the live Noise v2 channel, the mobile client, and navigation. The unit test above proves the daemon-side contract; the operator confirms the round trip.

## Open questions

- **`server.binary_offline` for an rng failure** is a slight semantic stretch (it is the only retryable server code today). The case is effectively unreachable on `crypto/rand`; if a developer prefers, a generic retryable wording is acceptable. Not worth a new error code.
- **Mobile-supplied `cwd` shape.** pyrycode-mobile#347's `createDiscussion` sends `cwd: null` for scratch discussions (per the fixture). If a future mobile build sends a real path, the trust-boundary note in the security review governs how the daemon should treat it. No code action this ticket.

## Security review

**Verdict:** PASS

**Findings:**

- **[1 Trust boundaries]** The single untrusted→trusted crossing is the `json.Unmarshal` of `env.Payload` into `CreateConversationPayload`, inside the per-conn goroutine — the same boundary `ListConversations`/`SendMessage` already cross (cf. #307's review: "phone-supplied data stays inside `protocol.Envelope`"). Decoded fields are typed and bounded: `is_promoted` (bool), `name` (string, stored + echoed only), `cwd` (string — see below). The created `id` is server-minted via `conversations.NewID()` (never phone-supplied), so a phone cannot choose or collide an id, cannot overwrite an existing row (`Create` appends; it does not upsert), and cannot forge a reply correlator (`in_reply_to` is the dispatcher-stamped `env.ID`, an opaque counter). No finding.
- **[3 File operations / path handling]** The phone-influenced `cwd` is the flagged surface. **It is inert today:** a grep of `internal/` + `cmd/` for `conversations.Conversation.Cwd` consumers shows the only reader is `list_conversations.go:43` (echo back to the operator's own phones). **No code path spawns a process, `chdir`s, or joins a filesystem path using `conversation.Cwd`** — `send_message` writes to the bootstrap session, which carries its *own* `WorkDir`; the conversation's `Cwd` never reaches `exec.Command`/`cmd.Dir`. So no path-traversal, TOCTOU, or symlink surface is reachable from this handler in the current tree: the value is stored metadata and echoed back to authenticated, operator-owned devices (paired via Noise_IK + device token), not crossed to any third party. Storing it verbatim (vs. validating/canonicalising now) is the evidence-based call — there is no observed exploit and no consumer to exploit it. **Deferred enforcement (SHOULD FIX, future):** any future ticket that spawns a per-conversation claude session at `conversation.Cwd` MUST canonicalise + boundary-check the path before use (absolute, cleaned, confined) — exactly the discipline #594's spec applied when it declined to source a phone-influenced path into `SessionJSONLPath`. The handler carries a code comment stating the cwd is inert-stored-metadata and the spawn-consumer owns validation. The registry `Save` itself writes to a server-owned path (`resolveConversationsRegistryPath`, never phone-supplied) at mode `0600`/`0700` via atomic temp+rename (registry.go:72-116) — no traversal on the write side.
- **[2 Tokens, secrets, credentials]** N/A — the handler touches no tokens/secrets. The Noise static key and device tokens are handled upstream in the v2 manager / first-frame gate; nothing secret enters or leaves this handler.
- **[4 Subprocess]** N/A — no `exec.Command`, no `sh -c`, no new subprocess. The created row binds no process (lazy bind on first turn, via the existing bootstrap path).
- **[5 Cryptographic primitives]** `conversations.NewID()` uses `crypto/rand` (id.go:12) — correct RNG for an identifier that must be unguessable/non-colliding. No hand-rolled crypto, no comparison of secrets.
- **[6 Network & I/O]** Input-size cap inherited from the transport (1 MiB WS read ceiling, `internal/transport`) and the v2 frame decode — this handler adds no unbounded read. The created `Conversation` is O(1) state; no per-request resource amplification (one append + one bounded atomic file write).
- **[7 Error messages, logs, telemetry]** Decode-error text is **never** echoed to the wire (static `msgCreateConversationMalformed` only) and never logged with payload bytes — the established `send_message` discipline. Logged fields are `event`, `conn_id`, and (on success) the server-minted `conversation_id`; no `name`/`cwd` payload values at Info, no tokens, no full frames. No finding.
- **[8 Concurrency]** No new goroutine; the handler is synchronous on the per-conn goroutine. `Registry.Create`/`Get`/`Save` are mutex-guarded. The eager-Save vs sweep-Save race is byte-level last-writer-wins with both snapshots containing the new row (no lost write); shutdown mid-Save leaves the prior file intact (rename is the commit point) — at worst the new row is absent on next start, which is the same non-durability the lazy model already tolerated. No lock-ordering finding (single lock).
- **[9 Threat model alignment]** Aligns with `docs/protocol-mobile.md` § Security model: the frame arrives already authenticated + decrypted by the v2 Noise manager (the handler is post-gate); the handler creates only operator-scoped state echoed only to the operator's own devices. Out of scope (named): `conversation_updated` multi-phone broadcast; per-conversation session-spawn cwd confinement (the future SHOULD FIX above).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-18
