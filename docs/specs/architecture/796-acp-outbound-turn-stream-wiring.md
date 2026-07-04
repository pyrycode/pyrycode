# Spec — #796: wire the outbound turn-event stream for `pyry acp`

**Ticket:** feat(acp): wire the outbound turn-event stream (session/update notifications during a turn)
**Epic:** #600 — `pyry acp` as a thin adapter over the shared remote-head core.
**Size:** S — additive, within-package (`cmd/pyry`) wiring of pre-built primitives. No new package, no new exported type, 2 production files.
**Security-sensitive:** No. Outbound plumbing over the local trusted ACP stdio transport, reusing already-reviewed primitives (`MapUpdate` #769, `acpTurnStream` #750, `resolveBoundSessionJSONL` #679). Content-free logging is a *preservation* constraint for code review, not a new surface.

---

## Files to read first

Turn-1 reading list — read these before writing anything.

- `cmd/pyry/acp.go:74-145` — `serveACPWithPool` composition root. Where the stream manager is constructed, `attach`ed inside `register`, and joined on return. This file gains a param + ~6 wiring lines.
- `cmd/pyry/acp.go:28-72` — `runACP`. `trustedWorkdir` is computed here (`confineWorkdirToHome("")` → `trustMark`). Compute `claudeSessionsDir` next to it and pass it into `serveACPWithPool`.
- `cmd/pyry/acp.go:167-225` — `newSessionHandler` / `loadSessionHandler`. The two `streams.start(id)` call sites; note where each obtains the session id (new mints it via `pool.Create`; load decodes it from params).
- `cmd/pyry/acp_turn_stream.go` (whole, 118 lines) — the sink (#750). `newACPTurnStream(transport, sessionID, onTurnEnd, logger)` signature; `Handle`; the `onTurnEnd` **nil seam** #751 will flip; the content-free logging posture the wiring must not break.
- `cmd/pyry/interactive_turn_stream_v2.go:59-117` — `startInteractiveTurnStreamV2`, the mobile analogue to mirror. Your version is far thinner: no emitter, no replay ring, no flush/coalesce, no follow-active.
- `cmd/pyry/interactive_turn_stream_v2.go:283-361` — `resolveTarget` + `resolveBoundSessionJSONL`. Reuse `resolveBoundSessionJSONL(dir, sessionID)` verbatim; your `TargetResolver` is the degenerate fixed-target case of `resolveTarget` (one host, nil `Switch`).
- `internal/turnbridge/producer.go:29-63` — `Subscriber` / `SessionHost` / `Target` / `TargetResolver` contracts. `*supervisor.Supervisor` satisfies `SessionHost` structurally.
- `internal/turnbridge/producer.go:97-132, 167-318` — `New` / `Run` (re-subscribe loop) + `NewTargetSubscriber` (per-subscription ctx, session-end re-subscribe, retry backoff). This is what gives you "runs for the session's lifecycle, re-subscribes per turn, no leaked goroutine."
- `internal/acpbridge/outbound.go:110-182` — `MapUpdate`: the four emit-able variants and the two `ok==false` events (TurnEnd/Stall). You don't call this directly — the sink does — but it defines what frames a scripted turn produces.
- `cmd/pyry/interactive_turn_stream_v2_test.go:28-54, 550-586, 591-639, 699-729` — **reusable** package-`main` test fixtures: `streamEntry` / `jsonlStreamEvent` / `endOfTurnEvent`, `scriptedSubscriber`, `waitClosed`, and the `EventsReachHandle` / `TeardownUnblocks` patterns. Your AC1/AC4 test is the ACP twin of `TestInteractiveTurnStream_EventsReachHandle`.
- `cmd/pyry/acp_turn_stream_test.go` (whole) — `streamHarness`, `frames`, `assertNotification`, `discriminant`. Reuse `assertNotification` shape to assert `session/update` frames addressed to the session id.
- `cmd/pyry/acp_test.go:284-360` — `newFakeClaudePool` / `newACPHarness`. Backing for the manager-lifecycle test. Also the two `serveACPWithPool(ctx, pool, …)` call sites (`:205`, `:350`) you must update with the new param.
- `internal/sessions/reconcile.go:37-40` — `DefaultClaudeSessionsDir(workdir string) string` (returns `""` when `$HOME` unresolvable).
- `internal/sessions/pool.go:728-744` — `Pool.Lookup(id) (*Session, error)`. `Session.Supervisor()` returns the `*supervisor.Supervisor` that is your `SessionHost`.

---

## Context

The neutral, daemon-owned turn-event model (`internal/turnevent`) is already mapped OUT to ACP `session/update` payloads by two merged pieces: `acpbridge.MapUpdate` (#769, the pure value-to-value mapper) and `acpTurnStream` (#750, the stateless OnEvent sink that writes `session/update` notifications on the ACP transport). **`acpTurnStream` currently has zero non-test callers.** Nothing in the `pyry acp` composition root constructs a `turnbridge.Producer` to drive it, so no turn events ever reach the host.

This ticket closes exactly that gap: for a live `pyry acp` session, construct a `turnbridge.Producer` that tails the session's transcript and feeds `acpTurnStream`, so the intermediate `session/update` notifications (assistant text, reasoning, tool activity) flow as a turn progresses. It is the ACP analogue of the mobile leg's `startInteractiveTurnStreamV2`, but far thinner: **one host, one fixed session per stream, no capability fan-out, no replay ring, no follow-active cursor.**

**Hard cost invariant (unchanged).** The turn runs on the real interactive `claude` session driven through the terminal driver. This ticket only *tails and maps* that turn's existing event stream. It introduces no `claude` spawn and no `claude -p` / Agent SDK path.

**Scope boundary vs #751.** `TurnEnd` is the `stopReason` RETURN of `session/prompt`, not a `session/update` (ADR 027 divergence 1). Resolving the held call on `TurnEnd` is #751's job, supplied via `acpTurnStream`'s `onTurnEnd` seam. **This ticket leaves that seam nil** — `TurnEnd` is a logged debug no-op here — and keeps the `newACPTurnStream(...)` construction site accessible so #751 flips nil → the held-call resolver without restructuring.

---

## Design

### Package structure

Two production files in `cmd/pyry`:

1. **`cmd/pyry/acp_turn_streams.go` (new)** — `acpTurnStreams`, an unexported per-process manager that owns one `turnbridge.Producer` goroutine per addressable ACP session and joins them all at shutdown.
2. **`cmd/pyry/acp.go` (edit)** — `runACP` computes the claude-sessions dir and passes it down; `serveACPWithPool` constructs the manager, attaches the transport in `register`, threads it into the two session handlers, and joins it on return.

No new package (this is `cmd/pyry` composition-root wiring). No new exported type.

### The manager — `acpTurnStreams`

Contract sketch (signatures + behaviour; not implementation):

```go
// acpTurnStreams owns one turnbridge.Producer per addressable ACP session,
// each driving the acpTurnStream sink (#750) for exactly one fixed session id.
// One instance per `pyry acp` process. dir == "" disables streaming (no $HOME).
type acpTurnStreams struct {
    ctx       context.Context      // runCtx; parent of every producer goroutine
    pool      *sessions.Pool       // Lookup(id) -> Session -> Supervisor (the SessionHost)
    dir       string               // claude <id>.jsonl directory; "" => streaming disabled
    logger    *slog.Logger
    transport *acp.Transport       // set by attach before Serve accepts frames
    mu        sync.Mutex           // guards started
    started   map[string]struct{}  // idempotency: at most one producer per session id
    wg        sync.WaitGroup       // joins every producer goroutine
}

func newACPTurnStreams(ctx context.Context, pool *sessions.Pool, dir string, logger *slog.Logger) *acpTurnStreams
func (m *acpTurnStreams) attach(t *acp.Transport) // set transport inside register, before Serve
func (m *acpTurnStreams) start(id sessions.SessionID) // idempotent; spawn one producer for id
func (m *acpTurnStreams) wait() // join all producers; call after m.ctx is cancelled
```

- **`attach(t)`** — stores the transport. Called as the first line of the `register` closure, which the acp transport's "register before Serve" invariant guarantees runs before any handler dispatch. No lock needed for `transport` (set-once, happens-before every `start`).

- **`start(id)`** — behaviour:
  1. If `dir == ""` → return (streaming disabled; nothing to tail).
  2. Under `mu`: if `id` already in `started` → return (idempotent — `session/load` may be called repeatedly for the same id, and `session/new`+`session/load` of one id must not double-start). Else insert and proceed.
  3. `sess, err := pool.Lookup(id)` — on error (unknown id, unreachable on the create/activate-success path) log content-free at debug (`session_id` + `err.Error()` sentinel), un-insert from `started`, return.
  4. `host := sess.Supervisor()` — the `turnbridge.SessionHost`.
  5. Build the **fixed** `TargetResolver` (see below), the `Subscriber` via `turnbridge.NewTargetSubscriber(resolve, tuidriver.NewTracker(tuidriver.TrackerOpts{}), logger)`, the sink via `newACPTurnStream(m.transport, string(id), nil /* #751 seam */, logger)`, and the producer via `turnbridge.New(turnbridge.Config{Subscribe: sub, OnEvent: sink.Handle, Logger: logger})`.
  6. `m.wg.Add(1)`; spawn `go` running `prod.Run(m.ctx)`, `defer m.wg.Done()`. `Run` returns only `ctx.Err()` (per its contract) → debug-log and exit.

  A fresh `tuidriver.NewTracker` **per session** (not shared) mirrors the mobile path's one-tracker-per-stream and avoids cross-session parse-state races.

- **`wait()`** — `m.wg.Wait()`. Called after `cancel()` in `serveACPWithPool`, so every producer has already observed ctx cancellation.

### The fixed-target resolver

The mobile `resolveTarget` follows the active conversation (bootstrap-vs-bound, a live `Switch` channel). ACP has none of that: each stream serves ONE fixed session id. The `TargetResolver` is the degenerate constant case — inline it as a closure in `start`:

```
resolve := func(ctx) (turnbridge.Target, error) {
    return turnbridge.Target{
        Host:    host,                                   // this session's supervisor
        Resolve: resolveBoundSessionJSONL(dir, string(id)), // fresh per (re)subscription
        Switch:  nil,                                    // one fixed session; no re-key
    }, nil
}
```

- `resolveBoundSessionJSONL` is **reused verbatim** from `interactive_turn_stream_v2.go` (same package). Build it *fresh inside the closure* so its per-subscription cold/warm offset state (`resolvedOnce`/`sawEmpty`) resets on each re-subscription — same rationale the mobile `resolveTarget` documents.
- `Switch: nil` — `NewTargetSubscriber` treats a nil `Switch` as "session-end and parent-ctx cancel are the only teardown triggers" (the pre-#679 single-host behaviour). Correct: ACP never re-keys a stream onto a different session.
- Never a bootstrap fallback: the ACP bootstrap is evicted (#761); a stream only ever exists for a `Create`d/`Activate`d session, so `host` is always that session's own supervisor.

### The claude-sessions directory

All ACP sessions spawn in the daemon's own workdir (`trustedWorkdir`; no caller cwd reaches the spawn until #762+), so one fixed directory serves every stream:

```
claudeSessionsDir := sessions.DefaultClaudeSessionsDir(trustedWorkdir)  // in runACP
```

`trustedWorkdir` is the realpath returned by `trustMark(workdirReal)` (`confineWorkdirToHome` already `EvalSymlinks`ed it), and it is byte-identical to the cwd `claude` is launched in (`buildSession` sets `WorkDir = tpl.WorkDir = trustedWorkdir`). So `DefaultClaudeSessionsDir(trustedWorkdir)` names the exact directory `claude` writes `<session-id>.jsonl` into — coherent by construction.

**Do NOT set `sessions.Config.ClaudeSessionsDir` on the ACP pool.** That would enable the rotation watcher, which `RotateID`s a session on a `/clear` and would break the host-held session id addressing that `session/prompt` / `session/cancel` rely on. The stream's dir is computed independently and passed only to `serveACPWithPool`; the pool's behaviour stays byte-unchanged.

### Data flow

```
claude PTY + <session-id>.jsonl
        │  (tui-driver Session.Events tails JSONL + tracks PTY)
        ▼
NewTargetSubscriber(fixed Target: host=supervisor, resolveBoundSessionJSONL(dir,id), Switch=nil)
        │  <-chan tuidriver.Event
        ▼
turnbridge.Producer.Run  ──mapEvent──▶ OnEvent(turnevent.Event)
        │
        ▼
acpTurnStream.Handle  ──MapUpdate──▶  transport.Notify("session/update", {sessionId:id, update})
        │                                        (assistant→agent_message_chunk,
        │                                         thought→agent_thought_chunk,
        │                                         tool→tool_call / tool_call_update)
        ├─ TurnEnd  → onTurnEnd==nil → debug no-op (#751 flips this)
        └─ Stall    → stderr (Warn, content-free), never a notification
```

### Composition-root wiring (`serveACPWithPool`)

- Signature gains a trailing `claudeSessionsDir string` param.
- After `newPromptHolds`, construct `streams := newACPTurnStreams(runCtx, pool, claudeSessionsDir, logger)`.
- Inside `register`: `streams.attach(t)` as the first line; pass `streams` into `newSessionHandler` and `loadSessionHandler`.
- After `serveACP` returns: `cancel()` (already present) stops both `pool.Run` and every producer; then `streams.wait()` (join producers) before `<-poolErr` (join pool). Order is not load-bearing — both respond to the single `cancel()` — but joining consumers first reads cleanly.

### Handler edits

- `newSessionHandler(pool, streams)` — after a successful `pool.Create`, call `streams.start(id)` before returning the result.
- `loadSessionHandler(pool, streams)` — after a successful `pool.Activate`, call `streams.start(id)`. Idempotent: for a this-process id whose stream is already running, `start` is a no-op; the call keeps the invariant "every addressable session has a running stream" honest and future-proofs a persistence-backed load.

---

## Concurrency model

- **One producer goroutine per session**, parented on `runCtx`. Started on the read-loop goroutine (from `session/new` / `session/load` handlers, which dispatch serially inside `Serve`). Torn down by `cancel()` at process shutdown, joined by `wait()`.
- **`started` map + `wg`** are touched by `start` (read-loop-only) and read by `wait` (after the read loop ends). The `mu` guard is belt-and-suspenders against a future off-loop caller; the correctness argument today rests on read-loop-only access, exactly like `promptHolds.begin`.
- **Producer-internal goroutines** (per-subscription `Switch`/session-end watchers inside `NewTargetSubscriber`) are turnbridge's own; each is tied to a session and unblocks on the supervisor's guaranteed `sess.Close()` at pool teardown (`<-poolErr`). They are not counted by `wg` and need no handling here — the mobile leg relies on the same contract.
- **Re-subscribe per turn:** the producer runs for the session's *lifecycle*, not a single turn. `Producer.Run`'s outer loop re-subscribes after each `sess.Events` channel close (a claude restart / turn boundary), so a session that outlives a turn keeps streaming subsequent turns. This satisfies AC-2's "not a single turn — the subscriber re-subscribes per turn."
- **No frame interleaving hazard:** the sink writes only `session/update` notifications, and only when events arrive. `acp.Transport.Notify` serialises on the transport write mutex against handler replies, so producer notifications and request responses never corrupt each other on stdout.

---

## Error handling

- **`dir == ""`** (no `$HOME`): `start` is a no-op; the process serves without live streaming rather than failing. Log once is optional — `DefaultClaudeSessionsDir` only returns `""` when `$HOME` is unresolvable, which `confineWorkdirToHome` would already have failed on, so this is effectively unreachable in `runACP`.
- **`pool.Lookup` miss in `start`**: content-free debug log, un-mark, return. Unreachable on the create/activate-success path; defensive only.
- **`turnbridge.New` error**: only on a nil `Subscribe` (never, here). Fail soft — log and skip that session's stream rather than aborting `serveACPWithPool`.
- **JSONL not yet present** (cold start — claude defers transcript creation until first input): the `NewTargetSubscriber` retry loop backs off and re-resolves; `resolveBoundSessionJSONL` tails from offset 0 on the cold-start file so the in-flight reply is not skipped (the #671 rule, per bound session). No new handling needed.
- **Transport write failure** in the sink: already handled by #750 (content-free debug log; the turn is ending anyway). Unchanged.
- **Content-free logging (preservation constraint).** The wiring must introduce **no** log site that formats event payloads. Every new log call carries only the content-free event kind, the session id, and error sentinels — never assistant/thought text, tool titles/inputs/results, or JSONL bytes. This is the one property code review must re-verify; it is not a new security surface (confidentiality is inherited from the already-reviewed sink/producer).

---

## Testing strategy

Reuse the package-`main` fixtures from `interactive_turn_stream_v2_test.go` (`streamEntry`, `jsonlStreamEvent`, `endOfTurnEvent`, `scriptedSubscriber`, `waitClosed`) and the `assertNotification`/`discriminant` helpers from `acp_turn_stream_test.go`. New tests live in `cmd/pyry/acp_turn_streams_test.go`.

- **AC-1 / AC-4 — scripted turn → ordered `session/update` frames (the "e2e" twin of `TestInteractiveTurnStream_EventsReachHandle`).** Wire a `scriptedSubscriber` yielding one channel of hand-built events → `turnbridge.New(Config{Subscribe, OnEvent: newACPTurnStream(transport, id, nil, logger).Handle})` over a real `acp.Transport` whose writer is a buffer. Drive, in order: `assistant/thinking`, `assistant/text`, `assistant/tool_use`, then `endOfTurnEvent()`. Assert the buffer holds exactly the ordered frames — `agent_thought_chunk`, `agent_message_chunk`, `tool_call` — each a well-formed `session/update` notification (no `id`/`result`/`error`) addressed to `sessionId == id`, and that the terminal `endOfTurn` (→ `TurnEnd`, nil resolver) emits **no** frame. This exercises the real `mapEvent` + `MapUpdate` path, not just the sink.
- **AC-3 — TurnEnd (nil resolver) no-op; Stall → stderr.** The bullet above already proves the TurnEnd-nil no-op through the wired producer. Stall is an unchanged pass-through to the sink, already proven by `TestACPTurnStream_StallDropsToStderr` (#750); optionally re-drive `sink.Handle(turnevent.Stall{})` in the same harness to assert no frame + `acp_turn.stall` on stderr. No new Stall path is introduced, so no new production coverage is warranted beyond this.
- **AC-2 — lifecycle: producer runs then tears down cleanly, no leaked goroutine.** Using `newFakeClaudePool` + a running pool, `Create` a session, construct `acpTurnStreams` with a real transport-writer buffer and a tmp `dir`, `attach`, `start(id)` (the producer parks in `WaitForPTY`/resolve-retry — the fake claude writes no JSONL), then `cancel()` the manager ctx and assert `wait()` returns within a deadline. Proves the producer goroutine exits and is joined.
- **Idempotency.** Call `start(id)` twice for one id; assert only one producer goroutine is created (e.g. `wg` reaches zero after a single `Done`, or a `started`-size check) so a repeated `session/load` cannot spawn a duplicate stream.
- **Existing-test upkeep.** Update the two `serveACPWithPool(ctx, pool, …)` call sites (`acp_test.go:205`, `:350`) with the new param. Pass `""` there — those tests don't exercise streaming, and `""` disables it, keeping their behaviour byte-unchanged (no new goroutines, no timing).
- **No content leak.** Optional but cheap: mirror `TestInteractiveTurnStream_NoAppOutputLogLeak` — drive secrets through the scripted chain against a captured logger and assert none appear in the logs.
- **`make check` green** (AC-5): `go vet`, `staticcheck`, `go test -race`.

---

## Open questions / out of scope

- **`/clear` mid-session.** The fixed-id resolver tails `<session-id>.jsonl`; a `/clear` that rotates claude to a new UUID transcript would leave the stream on the stale file. This is accepted by the ticket ("one fixed session per stream, no `Switch`") and is why the rotation watcher stays disabled (it would break host-held id addressing). If ACP ever needs mid-session rotation follow, it is a separate ticket. Not built now (no observed failure).
- **Per-session teardown before process exit.** ACP sets no `IdleTimeout` and exposes no session-close verb, so a pool session never ends independently of process shutdown; "session ends" collapses to "process shuts down" here, which `cancel()` + `wait()` handle. A claude *process* restart within a session is already handled by the producer's re-subscribe loop. If a future `session/close` / idle-eviction path lands, `acpTurnStreams` would need a per-id cancel (a child ctx per producer). Deferred — evidence-based, no such path exists today.
- **#751 hand-off.** `start` builds the sink with `onTurnEnd: nil`. #751 will pass a `holds`-derived resolver (`func(reason string){ holds.end(string(id), reason) }`) at this exact construction site; the manager will need a reference to the `promptHolds`. Left as a one-line seam, not pre-built.
