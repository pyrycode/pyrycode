# Spec #679 — turn bridge resolves the conversation's own session transcript (follow-active producer)

**Size:** S. Four production `.go` files (`internal/turnbridge/producer.go`, `cmd/pyry/interactive_turn_stream_v2.go`, `cmd/pyry/main.go`, `cmd/pyry/relay.go`), ~110 production LOC + ~190 test LOC ≈ 300 total, 2 new exported types (`turnbridge.Target`, `turnbridge.TargetResolver`), 4 ACs. `security-sensitive` — this is the cross-conversation confidentiality property; the security-review pass is appended at the end.

**Sizing hedge resolution (from the ticket).** The PO flagged two risks: (1) a custom dual-cancel subscriber, and (2) replay-ring continuity across re-subscription. (1) is real and designed below within S — it is a parameterised generalisation of the *existing* `NewSessionSubscriber` body, not net-new machinery. (2) is a **non-issue**: the #647 replay ring is owned by the emitter, which is daemon-resident (built once in `startInteractiveTurnStreamV2`, lives for the daemon's life) and is appended on the producer's single Run goroutine regardless of which session is subscribed — the producer re-subscribing never touches the ring or `SetReplaySource`. So no split; ships as one ticket.

## Files to read first

- `cmd/pyry/interactive_turn_stream_v2.go:53-110` — `startInteractiveTurnStreamV2`. The subscriber is built at `:79-80` (`resolve := resolveLatestSessionJSONL(...)`, `sub := turnbridge.NewSessionSubscriber(sup, resolve, tr, logger)`). **This is the one production call site that changes** — it becomes a follow-active `NewTargetSubscriber(resolveTarget, tr, logger)`. The emitter (`:61`) and replay source (`:74`) already read the #687 `active` signal — **leave both unchanged** (AC4 protects #687's cursor work).
- `cmd/pyry/interactive_turn_stream_v2.go:112-214` — `resolveLatestSessionJSONL`: the recency resolver. **Keep it** — it becomes the `convID == ""` (no-route-yet / bootstrap) branch of `resolveTarget`, preserving AC4. Lines `:143-147` (single-Run-goroutine, no-mutex contract) and `:154-213` (cold-start vs warm-start offset discrimination via `resolvedOnce`/`sawEmpty`) are the offset policy the new by-id resolver mirrors. Line `:29` `jsonlStemPattern` is the UUID-stem validator the by-id resolver reuses.
- `internal/turnbridge/producer.go:140-223` — `NewSessionSubscriber`. **This body is the template for `NewTargetSubscriber`.** The retry loop, `WaitForPTY` gate (`:164`), `host.Session()` capture (`:169`), `resolve` + `WaitForSessionJSONL` gate (`:173-187`), `sess.Events` open (`:190`), and the `go { sess.Wait(); cancel() }` session-end watcher (`:207-210`) all carry over — generalised to a per-subscription `Target` + an added active-switch teardown.
- `internal/turnbridge/producer.go:26-36, 88-105` — `Subscriber` type + `SessionHost` interface (`Session()` + `WaitForPTY()`); `Producer.Run`'s re-subscribe loop (`drain` returns when the stream channel closes → `subscribe` again). The follow-active re-key works **entirely through this existing loop**: a switch closes the stream channel, Run re-subscribes, the fresh `resolveTarget` snapshots the now-active session. `Producer` is not modified.
- `cmd/pyry/main.go:720-757` — `activeConversation` (the #687 holder: `mu`+`id`, `set`, `CurrentConversation`). Gains a `changed chan struct{}` + a `watch()` method and a fire-on-*change* tweak to `set`. `:582-587` — `runSupervisor` where `pool`, `convReg`, `bootstrap`, `active` are all concretely in scope: build the `boundHost` lookup here.
- `cmd/pyry/main.go:681-697` — `sessionRouter.Route`: confirms `active.set` is stamped *after* `convReg.Get` + `pool.Lookup` both succeed, so an `active` id always named a conversation with a pool-resolvable bound session **at stamp time** (the resolver's not-found path is the rare deleted-mid-flight case).
- `internal/sessions/session.go:118-122` — `Session.Supervisor() *supervisor.Supervisor`: the bound session's supervisor (satisfies `turnbridge.SessionHost` structurally — `Session()` + `WaitForPTY()`). This is how a bound session becomes the subscription host **and** the PTY-state source (couplings 1 & 2 collapse: `host.Session()` returns the bound session's `*tuidriver.Session`, whose screen buffer feeds turn_state/thinking/stall).
- `internal/sessions/pool.go:685-696` — `Pool.Lookup(id) (*Session, error)`; **`Lookup("")` returns the bootstrap** (same hazard #678 guards). `:780` — `Pool.Default()` (bootstrap). `internal/conversations/registry.go:128-137` — `Registry.Get`; `internal/conversations/conversation.go:44-49` — `Conversation.CurrentSessionID` (empty ⇒ no bound session).
- `internal/supervisor/supervisor.go:489-508` — `WaitForPTY` blocks until the supervisor has a live `Session`, returns `ctx.Err()` on cancel. For an evicted bound session it blocks until the session is (re)activated — the natural sync point, and the reason the pre-stream waits must be **switch-abortable** (below).
- `cmd/pyry/relay.go:88-103, 273-289, 348-354` — `startRelay`/`startRelayV2`/`startInteractiveTurnStreamV2` signatures + call sites. All package `main`; threading is plain param-passing. `convReg`/`active`/`sup`/`claudeSessionsDir` already reach all three (#687/#678); add one `boundHost` param.
- `cmd/pyry/interactive_turn_stream_v2_test.go:271-420` — `scriptedSubscriber` + `TestInteractiveTurnStream_RotationReSubscribes`: the producer-level re-subscribe test harness the AC3 switch test extends.
- `internal/e2e/relay_two_phone_structured_test.go:136-158` — the structured-stream e2e + its explicit note on the **cold-start offset race** (why the test pre-creates the JSONL). Confirms the offset policy the by-id resolver inherits; informs the "Open questions" deferral of a two-conversation e2e.
- `docs/specs/architecture/687-active-conversation-cursor.md` — the signal this ticket consumes (what `active.set`/`CurrentConversation` already do, and why the cursor re-key is *not* re-implemented here). `docs/knowledge/codebase/678.md` — conversation→session routing.

## Context

The structured interactive turn stream tails a JSONL transcript and streams the assistant reply to the phone. #687 re-keyed the stream's **cursor** (the `conversation_id` it stamps + the #647 replay attribution) to the active-conversation signal, so the stream emits again and stamps the routed conversation's id. But the **producer still tails by recency**: it subscribes over the bootstrap supervisor (`turnbridge.NewSessionSubscriber(sup=bootstrap, …)`) and its resolver returns the newest `<uuid>.jsonl` in the shared sessions directory (`resolveLatestSessionJSONL`).

Since #678, each conversation's turn commits on its **own** bound session, which writes its **own** `<bound-session-id>.jsonl`. With a per-conversation session active alongside the bootstrap (or another conversation's session), "newest JSONL" no longer identifies the conversation being interacted with — a reply could be tailed from whichever session wrote most recently, **cross-streaming another discussion's output into the reply**. That is the cross-conversation confidentiality break this ticket closes.

Each bound session is spawned with `--session-id <uuid>` (`buildSession`), so its JSONL filename is deterministic. The fix is the **follow-active producer lifecycle**: re-key the producer's subscription host, JSONL resolver, and PTY-state source to the **bound session of the active conversation**, resolving the transcript **by the bound session id** (mtime-independent), and re-subscribe when the active conversation (or its session) changes.

**What this ticket does NOT touch** (AC4): #687's cursor readers (the emitter's `CurrentConversation()` read and `SetReplaySource`); and the inbound `WriteUserTurn` growth-confirm resolver (`supervisor.Config.ResolveTranscript`, `internal/sessions/reconcile.go newTranscriptResolver`, #668) — a separate concern on the inbound path.

**Out of scope** (deferred, per the ticket): concurrently delivering *distinct* per-conversation reply streams to *multiple* connections. The structured stream fans out to all interactive conns by capability with no connection→conversation binding for output. This ticket covers the **single active reply stream following the operator's current conversation**; multi-operator isolation needs a connection-level focus model (likely cross-repo) and is undesigned.

## Design

The whole re-key rides the producer's **existing** re-subscribe loop (`Producer.Run`: subscribe → drain → channel-closed → subscribe). Three pieces:

1. **A per-subscription `Target`** (turnbridge): generalise the subscriber so each (re)subscription resolves *which* host + *which* JSONL + *what tears it down*, instead of a fixed host + resolver captured once.
2. **A follow-active `resolveTarget`** (cmd/pyry): snapshot the active conversation, map it to the bound session's supervisor + a by-id resolver, or fall back to the bootstrap + recency resolver before any route.
3. **An active-conversation *switch* signal** (cmd/pyry): `activeConversation` fires a channel when the active id changes, which tears down the current subscription so `Run` re-subscribes onto the now-active session.

### 1. `turnbridge.Target` + `NewTargetSubscriber` (generalise the subscriber)

The existing `NewSessionSubscriber(host, resolve, tr, log)` bakes in one fixed `host` and one `resolve`. Replace it with a target-driven subscriber; the per-subscription target is supplied by a caller callback (where the sessions/conversations knowledge lives — turnbridge stays neutral, no new imports).

```go
// Target is what one (re)subscription needs, resolved fresh per subscription.
type Target struct {
    Host    SessionHost                                            // WaitForPTY + Session()
    Resolve func(ctx context.Context) (path string, startOffset int64, err error)
    // Switch, if non-nil, tears the subscription down when it fires so
    // Producer.Run re-subscribes onto the now-current Target (follow-active).
    // nil ⇒ session-end and ctx are the only teardown triggers.
    Switch  <-chan struct{}
}

// TargetResolver yields the current Target. Called once at the top of each
// (re)subscription. A non-nil error is retried (backoff) unless ctx is done.
type TargetResolver func(ctx context.Context) (Target, error)

func NewTargetSubscriber(resolve TargetResolver, tr *tuidriver.Tracker, log *slog.Logger) Subscriber
```

`NewSessionSubscriber` is **removed** — its sole production caller is rewritten to `NewTargetSubscriber`, it has no test of its own (producer tests drive a `scriptedSubscriber`), and its single-host behavior is exactly the `convID == ""` branch of the follow-active resolver (so AC4 is preserved by a tested branch, not a dead wrapper). The existing body's logic carries over wholesale into `NewTargetSubscriber`; the two additions are the **switch teardown** and the **switch-abortable pre-stream waits**.

**Behavior contract of `NewTargetSubscriber`'s returned `Subscriber` (per re-subscription iteration):**

1. `target, err := resolve(ctx)`. On error: if `ctx` is done, return `ctx.Err()`; else `sleepCtx(ctx, subscribeRetryDelay)` and retry (the existing transient-resolve backoff — also covers the rare "active names an unresolvable bound session" case; see Error handling).
2. Derive a per-subscription `subCtx, cancel := context.WithCancel(ctx)`. If `target.Switch != nil`, spawn a **switch watcher**: `select { case <-target.Switch: cancel(); case <-subCtx.Done(): }`. It cancels `subCtx` on a switch and exits on session-end/parent-cancel — no leak.
3. `target.Host.WaitForPTY(subCtx)` → on err: `cancel()`; if `ctx.Err() != nil` return it (parent cancel), else `continue` (a switch cancelled `subCtx` — re-snapshot the now-active session). `WaitForPTY` returns only nil or ctx-err, so this is the only discrimination needed here.
4. `sess := target.Host.Session()`; nil ⇒ `cancel(); continue` (torn down between wait and capture).
5. `path, off, err := target.Resolve(subCtx)`; then `tuidriver.WaitForSessionJSONL(subCtx, path)`. On err, discriminate **before** calling your own `cancel()`: `ctx.Err() != nil` → parent cancel, return; `subCtx.Err() != nil` → switch, `cancel(); continue`; else → transient (file not present yet), `cancel()`, backoff, retry.
6. `sess.Events(subCtx, path, off, tr)` — same three-way discrimination as (5).
7. On success: spawn the **session-end watcher** `go { _ = sess.Wait(); cancel() }` (unchanged from today), keep the existing offset diagnostic `log.Debug("turnbridge: subscribed to session jsonl", path, off, cold_start=off==0)`, and `return ch, nil`.

Both watchers call `cancel()` (context cancel is idempotent). The switch-abortable waits (steps 3, 5, 6) are the one new invariant beyond today's body and are **load-bearing for AC3**: without them the producer can block forever in `WaitForPTY` of a stale evicted session after the operator switches conversations.

### 2. `resolveTarget` — the follow-active target resolver (cmd/pyry, in `interactive_turn_stream_v2.go`)

Built inside `startInteractiveTurnStreamV2`, closing over `active`, `boundHost`, `sup` (bootstrap), and `claudeSessionsDir`. Contract (signature + behavior; ~22 LOC, developer writes the body):

- `convID, switchCh := active.watch()` — atomic snapshot of the current conversation id **and** the channel that fires when it next changes (so host + path + teardown all key off one snapshot, never disagree).
- `convID != ""`: `host, sessionID, ok := boundHost(convID)`.
  - `ok` → `Target{Host: host, Resolve: resolveBoundSessionJSONL(claudeSessionsDir, sessionID), Switch: switchCh}`.
  - `!ok` → **return an error** (subscriber backs off + retries). Do **not** fall back to the bootstrap under a non-empty cursor: the emitter stamps `convID`, so tailing the bootstrap there would cross-stream. Retry re-snapshots; a concurrent switch resolves the new conversation on the next call.
- `convID == ""` (no route yet) → `Target{Host: sup, Resolve: resolveLatestSessionJSONL(claudeSessionsDir), Switch: switchCh}`. This is the **AC4 bootstrap path, unchanged**; `Switch` is still set so the first route re-subscribes onto the routed bound session.

`boundHost` is the conv→session→supervisor lookup, built in `runSupervisor` where `pool`/`convReg` are concrete and threaded as one new param down `startRelay → startRelayV2 → startInteractiveTurnStreamV2`:

```go
type boundHostFunc func(convID string) (host *supervisor.Supervisor, sessionID string, ok bool)
// boundHost(convID): convReg.Get → CurrentSessionID (!"" guard) → pool.Lookup → sess.Supervisor().
// (nil, "", false) on unknown conversation, empty binding, or Lookup miss.
```

`startInteractiveTurnStreamV2` then builds the subscriber as `turnbridge.NewTargetSubscriber(resolveTarget, tr, logger)` in place of the old `NewSessionSubscriber(sup, resolve, …)`. `sup` stays a param (it is the bootstrap fallback host inside `resolveTarget`).

### 3. `resolveBoundSessionJSONL(dir, sessionID)` — the by-id resolver (cmd/pyry)

A sibling of `resolveLatestSessionJSONL` that targets a **fixed** transcript instead of scanning for the newest. Signature + behavior (the cold/warm offset logic mirrors the recency resolver `:154-213`; ~30 LOC):

- Validate `sessionID` against `jsonlStemPattern` (`:29`, the UUID-stem regex). Non-match → return a non-nil error and never construct a path. This is the **path-safety guard**: a clean UUID stem has no `/` or `.`, so `filepath.Join(dir, sessionID+".jsonl")` cannot escape `dir` (defense-in-depth — `sessionID` is already a server-minted UUID from the trusted registry).
- `path := filepath.Join(dir, sessionID + jsonlStreamExt)`. `os.Stat(path)`: not-exist → not-found error (subscriber gates + retries via `WaitForSessionJSONL`); exists → return `path` + offset.
- **Offset (mtime-independent — keys off the id, satisfies AC2):** same per-subscription cold/warm discrimination as the recency resolver — a fresh closure per subscription holds `resolvedOnce`/`sawEmpty`. First look absent → on appearance, offset `0` (cold start: a brand-new bound session whose whole file is the current turn). Present at first look → offset = size (warm resume / switch-back: tail from EOF, never replay the conversation's history to the phone). Because the path is fixed by id, the file mtime is **never** consulted — another session writing more recently cannot redirect the tail.

The recency vs by-id resolvers share the cold/warm offset rule; the developer may extract a small `func(path) (off int64, …)` helper if it reads cleaner, but duplication of ~10 LOC is acceptable (each resolver stays self-contained). Not required.

### 4. `activeConversation` gains a *change* signal (cmd/pyry, `main.go`)

Extend the #687 holder so a switch can tear down the subscription. Mechanism mirrors the supervisor's own `activeCh` replace-on-transition pattern:

```go
type activeConversation struct {
    mu      sync.Mutex
    id      string
    changed chan struct{} // closed+replaced when id changes to a DIFFERENT value; lazy-init
}
// set(id): if id != a.id → a.id = id; close(a.changed); a.changed = make(...). Same id ⇒ no fire.
// watch() (id string, changed <-chan struct{}): snapshot a.id + a.changed under mu.
// CurrentConversation(): unchanged (#687).
```

- **Fire on *change* only.** `set` already runs on every successful `Route` (#687); consecutive messages to the *same* conversation must not re-subscribe (the tail stays open and catches each turn continuously, as the bootstrap does today). Only a different id closes `changed`.
- **Lazy-init `changed`.** `main.go:586` and `session_router_test.go` construct `&activeConversation{}` (zero value); `set`/`watch` must `if a.changed == nil { a.changed = make(chan struct{}) }` under `mu` so the literal stays valid (no constructor churn). A `nil` `changed` returned by `watch` before any init is a never-firing channel — harmless.
- **Capture-then-wait race is benign.** If `set` fires `changed` between `watch()` returning and the switch watcher selecting, the closed channel makes the select fire immediately → re-subscribe → re-snapshot. No missed switch. A→B→A flapping converges to the current id (A); intermediate targets are simply skipped.

### Data flow (after)

```
send_message(convID) ─► Route ─► active.set(convID)
                                     │ (id changed?) ── yes ──► close `changed`
                                     ▼
   Producer.Run subscribe loop ◄──── switch watcher cancels subCtx ──► stream closes ──► re-subscribe
        │
        └─► resolveTarget: convID,switchCh := active.watch()
                 convID==""  ─► bootstrap host + resolveLatestSessionJSONL   (AC4)
                 convID!=""  ─► boundHost(convID) ─► sess.Supervisor() + resolveBoundSessionJSONL(dir, <sessionID>)
                                     │
                                     ▼
              WaitForPTY(bound sup) ─► host.Session() (bound screen → PTY-state) ─► Events(<sessionID>.jsonl)
                                     ▼
                          emitter.Handle (cursor = active, #687) ─► fan out to interactive conns
```

## Concurrency model

- **No change to `Producer`.** The re-key works through the existing `Run` re-subscribe loop. Drain still runs on the single Run goroutine; `OnEvent`/`OnFlush`/emitter state stay single-goroutine exactly as #633/#687 documented.
- **`activeConversation`** is the one synchronization point: `set` (writer, on the per-conn dispatch/Route goroutine) and `watch`/`CurrentConversation` (readers, on the producer Run goroutine + the emitter) are all under the leaf `mu`. `set`'s close+replace of `changed` is single-shot per channel instance (the prior channel is closed exactly once, then replaced) — the same discipline as `supervisor.transitionTo`. The mutex is never held across a call-out.
- **Two per-subscription watcher goroutines.** The session-end watcher (`sess.Wait`) is unchanged from today and bounded by session lifetime (it lingers after a switch-away until the switched-from session ends — idle-evict/restart eventually fires it; bounded by live-session count ≤ ActiveCap or #conversations). The switch watcher exits on `subCtx.Done()` and so never outlives its subscription. Both call the idempotent `cancel()`.
- **Switch-abortable waits** mean the producer never wedges on `WaitForPTY`/`WaitForSessionJSONL` of a stale session after a switch — it abandons the wait and re-snapshots the now-active target.

## Error handling

| Case | Where | Behavior |
|---|---|---|
| No route yet (`convID == ""`) | `resolveTarget` | Bootstrap host + recency resolver (AC4); emitter drops on empty cursor anyway |
| `active` names an unknown/unbound/deleted conversation (`boundHost` !ok) | `resolveTarget` | Return error → subscriber backs off (`subscribeRetryDelay`) and retries; **never** falls back to bootstrap under a non-empty cursor (no cross-stream). Rare: only after a conversation/session is removed mid-flight (`Route` proved both present at stamp time) |
| Bound JSONL not yet on disk (cold start) | `resolveBoundSessionJSONL` + `WaitForSessionJSONL` | Not-found → gate retries; on appearance, offset 0 so the first reply streams (the #671 cold-start rule, per-bound-session) |
| Malformed `sessionID` (not a UUID stem) | `resolveBoundSessionJSONL` | Error, no path constructed (path-safety guard) |
| Active conversation switches | switch watcher | Cancel `subCtx` → stream closes / pre-stream wait aborts → `Run` re-subscribes onto the new session |
| Bound session ends (idle-evict / restart) | session-end watcher | Cancel `subCtx` → re-subscribe; `active` id is stable across evict/activate, so re-subscribe lands on the same conversation and `WaitForPTY` blocks until the next message re-activates it (AC3) |
| Parent `ctx` cancelled | every wait | `Subscriber` returns `ctx.Err()` → `Producer.Run` returns → cleanup `<-done` unblocks |

No new error *types*. The unresolvable-bound retry can warn-spam every `subscribeRetryDelay` if `active` is parked on a deleted conversation with no further routing — identical in shape to the existing "no session jsonl found" retry-warn, and self-clears on the next route.

## Testing strategy

Unit + producer-level (stdlib `testing`, `t.Parallel()` where safe). Scenarios as bullets; developer writes them in the project idiom.

- **`resolveBoundSessionJSONL` (new `*_test.go`, or extend `interactive_turn_stream_v2_test.go`) — AC1, AC2, path-safety:**
  - Bound `<id>.jsonl` present, a *different* session's JSONL present **and newer** → resolver returns the bound `<id>.jsonl` (keys off id, **not** mtime). This is the deterministic confidentiality proof of AC2.
  - Bound file absent at first call → not-found error; appears after a not-found → offset 0 (cold). Present at first call → offset = size (warm). (Mirror the recency resolver's offset tests.)
  - `sessionID` that is not a UUID stem (e.g. `"../escape"`, `""`) → error, no path returned.
- **`activeConversation` change signal (extend the #687 holder test) — AC3 mechanism:**
  - Zero value → `watch()` id `""`; `CurrentConversation()` `""` (regression).
  - `set("A")` then `set("A")` → `changed` does **not** fire (same id). `set("A")` then `set("B")` → the channel from `watch()` after `set("A")` is closed.
  - Race probe (`-race`): one goroutine loops `set`, another loops `watch`/`CurrentConversation`; no race; a returned id is always a value that was set (or `""`).
- **Producer-level re-subscribe on switch (extend `TestInteractiveTurnStream_RotationReSubscribes` shape) — AC3:** drive `turnbridge.New` with a scripted `TargetResolver` returning a controllable `Switch` channel and two event streams; close `Switch` → assert `Run` tears down stream 1, re-subscribes (resolver re-invoked), and the second reply flows on stream 2. Assert the session-end path still re-subscribes (existing test stays green).
- **`NewTargetSubscriber` switch-abortable wait — AC3 robustness:** a `SessionHost` whose `WaitForPTY` blocks until ctx-cancel; fire `Switch` → assert the subscriber abandons the wait and re-invokes `resolveTarget` (does **not** return / stop the producer).
- **Wiring smoke:** `go build ./...`, `go vet ./...`, `go test -race ./cmd/pyry/... ./internal/turnbridge/...`. Confirm the existing structured-stream tests (`interactive_turn_stream_v2_test.go`) stay green — the `convID == ""` → recency path is the unchanged-bootstrap regression guard for AC4.

## Open questions

- **Two-conversation structured e2e (deferred).** A full e2e — two bound sessions writing two JSONLs concurrently, the other one newer, assert the phone receives *only* the bound conversation's reply — would prove AC2 end-to-end, but requires the daemon to spawn per-conversation sessions under fakeclaude with two live structured JSONLs, an extension of the single-conversation `relay_two_phone_structured_test.go` harness (and it inherits that harness's documented cold-start offset race, `:146-158`). The security-relevant decision — *which file is tailed* — is fully pinned by the deterministic `resolveBoundSessionJSONL` unit test (by id, mtime-independent) and the producer-level switch test. Recommend the two-conversation e2e as a follow-up harness ticket, not a blocker here. Flag to PO if e2e coverage is required before merge.
- **Unresolvable-bound retry-warn cadence.** If `active` parks on a deleted conversation with no further routing, the subscriber warn-retries every `subscribeRetryDelay`. Matches the existing "no session jsonl" cadence; tune only if it proves noisy in a live run (no observed failure → defer).
- **Switched-away session-end watcher lifetime.** The `sess.Wait` goroutine for a switched-from session lives until that session ends. Bounded (≤ live-session count). If a future uncapped-`ActiveCap` deployment with many conversations shows goroutine growth, add an explicit per-subscription `sess.Wait` cancel; not warranted now (no observed failure).

---

## Security review

**Verdict:** PASS

**Threat framing.** This slice changes *which transcript* the outbound reply stream tails. The adversarial question: **can a routed turn's reply be tailed from — or leak content of — a conversation other than the one the operator is interacting with?** That is the cross-conversation confidentiality property the `security-sensitive` label names. The phone supplies only a `ConversationID` (already validated upstream by `Route`, #678) and turn `Text`; it supplies neither the session id nor the transcript path — both are server-derived from the trusted registry/pool.

**Findings:**

- **[Trust boundaries]** No MUST FIX — the confidentiality property is *strengthened* by this slice and pinned by a deterministic test. Before: the producer tailed the newest JSONL in the shared dir, so a second session writing more recently could cross-stream its content into the active conversation's reply. After: the transcript is resolved **by the bound session id** (`resolveBoundSessionJSONL`), which is read from `Conversation.CurrentSessionID` (server-minted, never phone-writable) via `boundHost`, and the file mtime is never consulted — another session writing more recently **cannot** redirect the tail (AC2, unit-tested). The one path that could regress confidentiality — falling back to the bootstrap (or any other) transcript while the emitter stamps a non-empty `convID` — is explicitly forbidden: `resolveTarget` returns an *error* (retry) rather than a bootstrap `Target` when `convID != ""` is unresolvable, so the stream never tails one conversation's file under another's attribution. The bootstrap/recency path is reachable **only** when `convID == ""` (no route yet), where the emitter drops every event anyway (empty cursor, #687). **Residual transient (not regressed, #687-owned).** Between `active.set(B)` and the switch watcher draining A's stream, an already-in-flight A event could reach the emitter, which stamps it with the live cursor (now `B`) — a content-misattribution window inherent to #687's read-live-cursor model, not introduced here. This slice *narrows* it (the switch tears A's subscription down promptly; pre-#679 recency could tail A's file indefinitely under B's cursor), and in the single-operator switch case A is idle at switch time (its prior turn completed before the operator messaged B), so the window carries no A content. Eliminating it entirely would require binding the emitter's stamp to the subscription's `convID` rather than the live cursor — a change inside #687's fenced-off cursor work (AC4) and the deferred multi-operator isolation, out of scope here.
- **[File operations]** No MUST FIX. The only new path construction is `filepath.Join(dir, sessionID + ".jsonl")`. `sessionID` is validated against `jsonlStemPattern` (`^[0-9a-f]{8}-…-[0-9a-f]{12}$`) **before** the join — a matching stem contains no `/` or `.`, so traversal out of `dir` is impossible (defense-in-depth: `sessionID` is already a server-minted UUID from the trusted registry/pool, not phone input). `dir` is the daemon's already-computed `claudeSessionsDir` (the exact dir reconcile + the rotation watcher use), unchanged. No TOCTOU surface beyond the existing `Stat`→`Events` window the recency resolver already has (a vanished/raced file degrades to a retry, never a wrong-file read). No new write path; the producer is read-only over transcripts.
- **[Tokens / secrets / crypto]** N/A. No tokens, keys, credentials, or crypto primitives are created, stored, or compared. `sessionID`/`conversationID` are non-secret routing UUIDs already standard as log fields. The `crypto/rand` UUID minting (`conversations.NewID`, `sessions`) is unchanged and elsewhere.
- **[Subprocess / external command]** N/A. No `exec`, no argv/env construction, no signals. The producer never spawns or activates a session — it only *reads* (`WaitForPTY` blocks; `Events` tails). Activation remains the `send_message` handler's job through the cap-enforcing `Pool.Activate` (#678), so this slice adds no spawn-amplification surface.
- **[Network & I/O]** No findings. No new socket reads/writes or wire parsing; envelopes still fan out via the established `V2SessionManager.Push` + capability gate (#687, unchanged). The transport-size cap and the handler's two 30s budgets are untouched. The switch-abortable waits *remove* a potential hang (producer wedged on a stale session) rather than add one; the unresolvable-bound retry is bounded by `subscribeRetryDelay`.
- **[Error messages / logs / telemetry]** No findings. The carried-over `turnbridge: subscribed to session jsonl` diagnostic logs only the path (a server-minted UUID filename under the trusted dir), the offset, and the derived `cold_start` bool — never JSONL bytes (the substrate seal, preserved verbatim). The emitter's "never log application output" discipline (#687) is untouched — this slice changes which *file* is tailed, never whether content is logged. New warn lines (unresolvable-bound retry) carry only the non-secret conversation/session ids and a package-context error, matching the existing resolve-retry warn.
- **[Concurrency]** No MUST FIX (covered by AC3 + the `-race` holder test). The single new cross-goroutine state is `activeConversation.changed`, guarded by the existing leaf `mu` (never held across a call-out); its close+replace is single-shot per channel instance, mirroring `supervisor.transitionTo`'s proven pattern. The capture-then-wait race (`watch()` then select) is benign by construction (a closed channel fires the select immediately → re-subscribe; no missed or duplicated switch beyond a harmless re-snapshot). Both per-subscription watchers call the idempotent context `cancel()`; the switch watcher cannot leak (exits on `subCtx.Done()`), and the session-end watcher's lifetime is bounded by session lifetime (Open questions). No new lock ordering: the holder's lock is a leaf, and the producer's single-Run-goroutine invariant for the emitter is unchanged.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-18
