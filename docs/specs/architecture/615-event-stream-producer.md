# Spec #615 — Event-stream bridge (producer): drain `Session.Events()` into the internal turn-event model

**Part of EPIC #596 (Phase 2 structured streaming).** Anchor: [ADR 025](../../knowledge/decisions/025-mobile-remote-head-interactive-session.md) § "The event model".

This is the **producer half** of the structured-event bridge: drain the supervised claude session's unified tui-driver `Events()` stream and map each event into the neutral internal turn-event model (`internal/turnevent`, #606). The **consumer half** — mapping the internal model to v2 wire envelopes (#607) and the capability-gated fan-out to phones — is #616 and consumes this producer's output. This slice ships the producer **with unit tests, deliberately unwired to a live fan-out** (the standard "introduce the producer + tests; wire the consumer in the next slice" pattern).

Blocker #619 (tui-driver **v1.3.0** bump) is **merged** — `go.mod` is at `v1.3.0`, so `ParseToolUse` / `ParseToolResult` and the `Tracker`-param `Session.Events` signature are available. Build against them; do not reimplement the parsers.

**Not `security-sensitive`** (confirmed against the ticket label). The producer reads the supervised claude session's structured events — trusted local input — and maps them to an internal model. No untrusted-party input, no dispatch to phones, no trust decision, no capability enforcement. **Skip the architect security-review step.**

---

## Files to read first

Read these before writing code; line ranges point at the exact contract to extract.

- `internal/supervisor/supervisor.go:267-289` — `ScreenSnapshot` is the **exact capture pattern** the new `Session()` accessor mirrors (lock `sessMu`, read `s.sess`, unlock, nil-check). Copy its shape.
- `internal/supervisor/supervisor.go:120-155` — the `Supervisor` struct: `sessMu` + `sess *tuidriver.Session` + `sessReadyCh`. `Session()` reads `sess`; the producer's subscriber uses `WaitForPTY`.
- `internal/supervisor/supervisor.go:306-346` — `setSession` / `WaitForPTY`: the readiness choreography. `WaitForPTY(ctx)` blocks until the **next** hosted session is live — the producer's subscriber calls it before each subscription.
- `internal/turnevent/event.go` — the output model: `Event` (sealed sum type), `TextChunk`, `ThoughtChunk`, `ToolStart`, `ToolUpdate`, `TurnEnd`, `Location`. **Map INTO these verbatim; do not introduce a parallel set.**
- `internal/turnevent/taxonomy.go` — `ToolKind` / `ToolStatus` / `TurnEndReason` enums + `Valid()`. The producer fills `ToolStart.Kind`, `ToolUpdate.Status`, `TurnEnd.Reason` from these.
- `internal/turnevent/content.go` — `ToolContent` sum type; `TextContent` is what a `ToolUpdate` carries here. `nil` content is legal (status-only).
- `<modcache>/github.com/pyrycode/tui-driver@v1.3.0/pkg/tuidriver/events.go:15-170` — `EventKind` (11 variants), the `Event` struct (`Kind` / `Source` / `Time` / `Modal` / `Entry`), and `Session.Events(ctx, jsonlPath, startOffset, tr *Tracker)`. **Confirm the exact `EventKind` variant names against this file** before writing the type switch (the ticket lists 11; this is the source of truth).
- `<modcache>/.../tuidriver/jsonl.go:80-135,297-337` — `JSONLEntry` / `EntryMessage` / `ContentBlock` shapes, `AssistantText` (text-block extractor the producer reuses), `IsEndTurn`. Note `JSONLEntry.RawLine` (the verbatim line bytes `ParseToolUse`/`ParseToolResult` take).
- `<modcache>/.../tuidriver/tool_use.go` — `ParseToolUse(snap []byte) *ToolUse` → `{ID, Name, Input map[string]any}`. Returns nil unless the line is an `assistant` envelope with a `tool_use` block.
- `<modcache>/.../tuidriver/tool_result.go` — `ParseToolResult(snap []byte) *ToolResult` → `{ToolUseID, IsError, Content any}`. Gates on `user` envelope with a `tool_result` block.
- `<modcache>/.../tuidriver/tracker.go:17-81` — `NewTracker(TrackerOpts{})` + `DefaultPTYQuietLimit`. `Session.Events` requires a **non-nil** `*Tracker` (nil panics); a default tracker suffices here (it only drives the dropped stall arm).
- `<modcache>/.../tuidriver/jsonl.go:35-78` — `SessionJSONLPath` / `WaitForSessionJSONL`; the subscriber uses `WaitForSessionJSONL` to gate on the file existing before `Events`.
- `internal/sessions/reconcile.go:50-89` — `mostRecentJSONL` + `DefaultClaudeSessionsDir`. **Reference only** — the production JSONL resolver belongs to #616, NOT this slice (see § "Open question: which JSONL"). Read it to understand the resolution the resolver will perform.
- `cmd/substrate-guard/main.go:50-72` — the banned-literal list. The mapper's string literals (`"thinking"`, `"text"`, `"assistant"`, `"user"`, tool names) are **not** on it; the guard scans test files too, so **fixtures must avoid the banned tokens** (spinner/idle glyphs, `"Pasted text"`, `\x1b[`, …).
- `docs/knowledge/codebase/606.md` and `607.md` — how the internal model (#606) and the wire types (#607) were built; the conventions this producer feeds.

`<modcache>` = `$(go list -m -f '{{.Dir}}' github.com/pyrycode/tui-driver)` (currently `~/go/pkg/mod/github.com/pyrycode/tui-driver@v1.3.0`).

---

## Context

ADR 025 § Phase 2 introduces structured streaming: a phone advertising the `interactive` capability should receive typed events (thinking, tool use, assistant text, turn boundaries) instead of the coarse finished-turn `message` fan-out (#589). The neutral internal model (#606) is the decoupling layer between tui-driver's event vocabulary and the v2 mobile wire vocabulary (#607). This ticket fills that model from tui-driver; #616 drains the model out to the wire.

**How tool activity arrives.** `Session.Events()` merges PTY-state edges and JSONL entries into one unified stream. There is **no `tool-use` / `tool-result` event kind** — tool activity rides `EventKindJsonlEntry` payloads (`Event.Entry`, a `tuidriver.JSONLEntry`). `tuidriver.ParseToolUse` extracts a `tool_use` block (assistant envelope) → `ToolStart`; `tuidriver.ParseToolResult` extracts a `tool_result` block (user envelope) → `ToolUpdate`. The producer runs these extractors over each `JsonlEntry` payload; no new tui-driver stream work is needed.

**The brittleness split (ADR 025 § Key architectural insight).** JSONL-sourced events (assistant text, tool use/result, end-of-turn) are robust; PTY-state events (idle/thinking spinner, modals, banners, stall) are screen-scraped and brittle. This producer maps the **robust** JSONL-sourced kinds and **drops** every PTY-state kind — not because the screen signals are worthless, but because the internal model (#606) has **no type** for them (no idle/modal/stall variant; that is deliberate, see #606). Thinking-state, modals, and stall surfacing are later #596/#597 children.

---

## Design

### Package layout

Two new production files in a new package `internal/turnbridge`, plus one additive accessor on the supervisor:

```
internal/supervisor/supervisor.go   (mod)  + Session() accessor (~8 LOC)
internal/turnbridge/producer.go     (new)  Producer lifecycle: drain + re-subscribe; the live Subscriber
internal/turnbridge/mapper.go       (new)  mapEvent + pure helpers (the type switch)
internal/turnbridge/producer_test.go (new) drain / re-subscribe / nil-OnEvent tests
internal/turnbridge/mapper_test.go   (new) table-driven event→model + drop tests
```

`internal/turnbridge` imports `tuidriver` + `internal/turnevent` (+ stdlib). It does **not** import `internal/supervisor` (it defines the consumer-side `SessionHost` interface) and does **not** import `internal/sessions` (the JSONL resolver is injected). Dependency direction stays clean: `cmd/pyry → internal/turnbridge → {tuidriver, turnevent}`, with `*supervisor.Supervisor` satisfying `turnbridge.SessionHost` structurally.

Package name: `turnbridge` — it bridges tui-driver events into the `turnevent` model. (Reads naturally beside `turnevent`.)

### AC 1 — the `Session()` accessor (supervisor.go)

A nil-safe getter mirroring `ScreenSnapshot`'s capture. **Additive** — no existing signature changes, no consumer cascade.

```go
// Session returns the currently-hosted tui-driver Session, or nil when no
// claude child is attached (between restarts, mid-spawn, or idle-evicted).
// Safe for concurrent use; captures the pointer under sessMu. The producer
// (internal/turnbridge) subscribes to the returned session's Events() stream.
func (s *Supervisor) Session() *tuidriver.Session {
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	return s.sess
}
```

**Seal note:** returning `*tuidriver.Session` does not breach the substrate seal. The supervisor already holds and uses this pointer; the producer only calls `sess.Events()` (typed events) — never `MirrorOutput()`/`Snapshot()` (raw bytes) — so no claude-screen literal enters pyrycode. `cmd/substrate-guard` stays green.

### AC 2 + AC 4 — the producer lifecycle (producer.go)

The producer is split into a **testable core** (drain + re-subscribe loop, driven by an injected `Subscriber`) and **live glue** (the production `Subscriber` that calls `Session.Events`). The split is what makes AC 2's lifecycle guarantees unit-testable without a live claude.

```go
// Subscriber yields a live session's tui-driver event stream. The returned
// channel closes when that session ends (supervisor restart) or ctx is done.
// Returns a non-nil error ONLY on ctx cancellation; transient resolution
// failures are retried internally, so the channel-or-ctx-done contract holds.
type Subscriber func(ctx context.Context) (<-chan tuidriver.Event, error)

// SessionHost is the supervisor seam the production Subscriber drives.
// *supervisor.Supervisor satisfies it.
type SessionHost interface {
	Session() *tuidriver.Session
	WaitForPTY(ctx context.Context) error
}

type Config struct {
	Subscribe Subscriber             // required
	OnEvent   func(turnevent.Event)  // nil ⇒ no-op beyond draining (AC 4)
	Logger    *slog.Logger           // nil ⇒ slog.Default()
}

type Producer struct { /* cfg + log */ }

func New(cfg Config) (*Producer, error)            // err if Subscribe == nil
func (p *Producer) Run(ctx context.Context) error  // outer re-subscribe loop
```

**`Run` (outer loop) — behaviour contract:**
- Loop: `Subscribe(ctx)` → `drain(ctx, ch)` → repeat. `Subscribe` blocks until a live stream exists, so the loop is naturally paced (no busy-spin).
- Returns `ctx.Err()` when ctx is cancelled (the only error `Subscribe` returns per its contract).
- Re-subscribes after `drain` returns on channel close — this is "no leaked goroutine across a session restart": the prior session's tui-driver merge goroutine already closed its channel (its per-session ctx was cancelled, see the Subscriber below).

**`drain` (inner loop) — behaviour contract:** a `select` over `ctx.Done()` and the event channel.
- `ctx.Done()` → return.
- channel closed → return.
- event received → if `OnEvent == nil`, `continue` (drains the source, does nothing else — AC 4's "no-op beyond draining"); else `mapEvent(ev)` → on `ok` call `OnEvent(te)`, on `!ok` `log.Debug("turnbridge: dropping unrepresentable event", "kind", ev.Kind)` (AC 3's "dropped … logged at debug").

The drain loop satisfies **both** AC 2 (clean exit on ctx-cancel AND channel-close) and the per-event half of AC 5, and is exercised directly by `producer_test.go` with synthetic channels.

**Output is a callback, not a channel.** AC 4 says "channel or callback … with no consumer attached the producer is a no-op beyond draining." A single `OnEvent` callback (nil-allowed) models "no consumer = no-op beyond draining" precisely, and pushes backpressure / queueing / drop-policy to #616 — which is where ADR 025 § Backpressure puts it. #616 supplies a callback that enqueues into its per-phone push queues. One downstream bridge, so the callback is set once at construction (no dynamic re-attach, no concurrency on the field).

### AC 2 — the live `Subscriber` (producer.go)

`NewSessionSubscriber` builds the production `Subscriber`. It owns the per-session ctx that makes the Events channel close when the session ends — the linchpin of "no leaked goroutine across a session restart."

```go
// NewSessionSubscriber builds the production Subscriber. resolve yields which
// session JSONL to tail and from what offset (supplied by #616 wiring — see
// "Open question" below). tr is required by Session.Events (drives only the
// dropped stall arm); pass tuidriver.NewTracker(tuidriver.TrackerOpts{}).
func NewSessionSubscriber(
	host SessionHost,
	resolve func(ctx context.Context) (path string, startOffset int64, err error),
	tr *tuidriver.Tracker,
	log *slog.Logger,
) Subscriber
```

Returned closure, per call:
1. `host.WaitForPTY(ctx)` — block until a session is live; return `ctx.Err()` on cancel.
2. `sess := host.Session()`; if `nil` (torn down between WaitForPTY and capture), retry from step 1 — `WaitForPTY` will block for the next session, so no spin.
3. `path, off, err := resolve(ctx)` then `tuidriver.WaitForSessionJSONL(ctx, path)` — if `ctx`-cancelled, return the error; on any other transient error, `log.Warn` and retry after a bounded delay (`select { case <-ctx.Done(): return …; case <-time.After(subscribeRetryDelay): }`, a small package const, e.g. 500ms) to cap the spin.
4. `sessCtx, cancel := context.WithCancel(ctx)`; `go func() { sess.Wait(); cancel() }()` — when the supervised process exits (restart/idle-evict), `Wait` returns → `cancel` → the Events merge loop's `ctx.Done()` fires → it closes the channel → `drain` returns → `Run` re-subscribes.
5. `ch, err := sess.Events(sessCtx, path, off, tr)`; on error `cancel()` + retry (same ctx-vs-transient split as step 3); on success return `ch`.

**Why the Wait-watcher is necessary and leak-free.** The Events merge loop exits only on its ctx or when its internal `TailJSONL` channel closes — but `TailJSONL` tails a file that **persists on disk after claude exits** (EOF → wait → retry), so it never closes on process exit alone. The per-session ctx (cancelled by `sess.Wait()`) is therefore the only thing that closes the stream on a restart. `sess.Wait()` is documented safe to call from multiple goroutines concurrently with the supervisor's own `Wait`/`Close` (tuidriver `session.go:403-417`), so the watcher does not interfere with `runOnce`. The watcher exits when `Wait` returns; the supervisor's `runOnce` guarantees `sess.Close()` on every iteration exit (including root-ctx cancel, shared with the producer), so the watcher always unblocks — no leak.

This live glue is exercised by #616's wiring and the v2 e2e oracle, **not** by `turnbridge`'s unit tests (a real `*tuidriver.Session` can only be `Spawn`ed). That is the intended scope line: this slice unit-tests the drain + re-subscribe core (via a fake `Subscriber`) and the pure mapper; the live subscription is verified downstream.

### AC 3 + AC 5 — the mapper (mapper.go)

A pure function — no logger, no I/O — so the drain owns the debug-log for drops and the mapper stays trivially table-testable.

```go
// mapEvent maps one tui-driver Event to a neutral turnevent.Event. ok is false
// for events the internal model has no representation for — the caller drops +
// debug-logs those. Pure; safe on a zero-value Event.
func mapEvent(ev tuidriver.Event) (turnevent.Event, bool)
```

**Mapping rules** (confirm `EventKind` names against `events.go` first):

| `ev.Kind` | sub-condition (on `e := ev.Entry`) | result |
|---|---|---|
| `EventKindJsonlEntry`, `e.Type=="assistant"` | `ParseToolUse(e.RawLine) != nil` | `ToolStart{…}`, true |
| ″ | else `AssistantText(e) != ""` | `TextChunk{MessageID: e.Message.ID, Text: …}`, true |
| ″ | else `thinkingText(e) != ""` | `ThoughtChunk{MessageID: e.Message.ID, Text: …}`, true |
| ″ | else | drop |
| `EventKindJsonlEntry`, `e.Type=="user"` | `ParseToolResult(e.RawLine) != nil` | `ToolUpdate{…}`, true |
| ″ | else | drop |
| `EventKindJsonlEntry`, other `e.Type` | — | drop |
| `EventKindJsonlEndOfTurn` | — | `TurnEnd{Reason: TurnEndReasonEndTurn}`, true |
| `EventKindPty*` / `EventKindStallDetected` / `EventKindUnknown` | — | drop |

In claude's streaming JSONL each line carries one content block, so the assistant-branch sub-conditions are mutually exclusive in practice; the priority order (tool_use → text → thinking) is defensive. The branches are split on `e.Type` first so `ParseToolUse`/`ParseToolResult` (which re-parse `RawLine` and gate on envelope type) run only where they can match.

**Field mapping:**
- `ToolStart{ToolCallID: tu.ID, Title: tu.Name, Kind: toolKind(tu.Name), RawInput: rawInput(tu.Input), Locations: nil}`. `Locations` deferred (deriving touched files from tool input is #616/refinement territory).
- `ToolUpdate{ToolCallID: tr.ToolUseID, Status: <Failed if tr.IsError else Completed>, Content: <TextContent{toolResultText(tr.Content)} if non-empty, else nil>}`. A `tool_result` marks the call finished → `ToolStatusCompleted`/`ToolStatusFailed`; `nil` content for an empty result is the legal status-only update.
- `TextChunk` / `ThoughtChunk`: `MessageID = e.Message.ID`. Safe — both branches are reached only after a non-empty text/thinking extraction, which implies `e.Message != nil` (the extractors return "" on a nil message).
- `TurnEnd`: `EventKindJsonlEndOfTurn` fires only when `IsEndTurn` held (assistant + `stop_reason=="end_turn"` + non-empty text), so the reason is always `TurnEndReasonEndTurn`. Other stop reasons (`max_tokens`, `refusal`, …) are not distinguishable from this event kind in v1.3.0; map to `end_turn` and note it.

**Note on the end-of-turn pair:** the merge loop emits `EventKindJsonlEntry` then `EventKindJsonlEndOfTurn` for the same entry (`events.go:243-252`). The producer therefore emits a `TextChunk` immediately followed by a `TurnEnd` for the final assistant line — the correct ordering (content, then boundary).

**Pure helpers** (signatures + one-line behaviour; the developer writes bodies in-idiom):
- `thinkingText(e tuidriver.JSONLEntry) string` — guard `e.Message == nil`; concat `e.Message.Content` blocks where `Type == "thinking"`, reading `block.Raw["thinking"].(string)`. Mirrors `AssistantText`'s shape exactly (which reads `"text"` from `type=="text"` blocks). **Verify the `"thinking"` field name** against a real fixture (see Open questions).
- `toolResultText(content any) string` — `tr.Content` is a `string | []any | nil` union (see `tool_result.go`): a `string` returns itself; a `[]any` joins the `"text"` field of each `{"type":"text",…}` block; anything else → `""`.
- `toolKind(name string) turnevent.ToolKind` — small best-effort switch on common claude tool names → ACP kind, default `ToolKindOther`. e.g. `Read→read`, `Edit`/`Write→edit`, `Bash→execute`, `Grep`/`Glob→search`, `WebFetch→fetch`, `Task→think`. Best-effort and intentionally minimal; refinement is downstream.
- `rawInput(in map[string]any) json.RawMessage` — `nil`/empty → `nil`; else `json.Marshal(in)` (drop the error, fall back to `nil`). Re-marshal sorts keys; acceptable because `RawInput` is opaque pass-through the consumer never key-orders against.

---

## Concurrency model

- **One producer goroutine** (`Producer.Run`, started by #616 under the daemon root ctx). It calls `Subscribe` (which calls `WaitForPTY`, blocking) then `drain` (blocking on the channel), in a loop. Single owner of the `OnEvent` callback invocation — `OnEvent` runs on this goroutine, so #616's callback must not block it indefinitely (its own queue handles backpressure).
- **One short-lived Wait-watcher goroutine per subscription** (`go func(){ sess.Wait(); cancel() }()`), inside the live Subscriber. Exits when the session closes. No leak (see § live Subscriber).
- **tui-driver spawns its own merge + tail goroutines** inside `Session.Events`, governed by the per-session ctx; they close the channel and exit when that ctx is cancelled.
- **Shutdown sequence:** root ctx cancel → `WaitForPTY`/`drain`/Events-merge all observe `ctx.Done()` → channels close → `Run` returns `ctx.Err()`; the supervisor independently `Close`s its session, unblocking any in-flight Wait-watcher.
- Locking: the only shared state is the supervisor's `sessMu` (already leaf-only), read by `Session()`. The producer adds no locks.

---

## Error handling

- `New` returns an error if `Subscribe` is nil (programmer error; the one constructor guard).
- `Subscribe`'s contract: returns a non-nil error **only** on ctx cancellation. Transient failures (JSONL not yet present, `Events` open error, momentary nil session) are retried internally with a bounded delay — they never surface to `Run`, keeping the outer loop trivial and spin-free.
- `mapEvent` never errors — unrepresentable events return `(nil, false)` and are dropped + debug-logged by the drain. This follows #606's "primitive exposes `Valid()`, refusal-mapping is the consumer's job" posture: the producer drops what the model can't hold; it does not invent error envelopes.
- Malformed JSONL never reaches the mapper — `TailJSONL` silently drops unparseable lines upstream (`jsonl.go:200-210`).

---

## Testing strategy

stdlib `testing` only, table-driven, `t.Parallel()` where safe, `go test -race`. No live claude.

**`mapper_test.go` (AC 5 — the heart):** a table of `tuidriver.Event` inputs → expected `(turnevent.Event, ok)`:
- assistant line with a `text` block → `TextChunk{MessageID, Text}`.
- assistant line with a `thinking` block → `ThoughtChunk{MessageID, Text}`.
- assistant line with a `tool_use` block → `ToolStart` (assert `ToolCallID`, `Title`, `Kind`, `RawInput` is valid JSON of the input) — the `ParseToolUse` path.
- user line with a `tool_result` block, `is_error:false` → `ToolUpdate{Status: completed, Content: TextContent}`; and `is_error:true` → `Status: failed` — the `ParseToolResult` path.
- `EventKindJsonlEndOfTurn` → `TurnEnd{end_turn}`.
- **drop cases:** every `EventKindPty*` (idle, thinking, modal shown/hidden, mcp/network failure shown/hidden), `EventKindStallDetected`, `EventKindUnknown`, and a JSONL entry with no representable content (e.g. `type:"system"`, or an assistant line carrying only a `usage` block) → `ok == false`.
- helper unit cases: `toolResultText` over string / `[]any` text-blocks / nil; `toolKind` over a few known names + an unknown (→ `other`).
- Build synthetic events via a small fixture builder that sets `Entry.Type`, `Entry.Message`, and **`Entry.RawLine`** (the parsers read `RawLine`; for synthetic entries the test must populate it — it is nil by default per the `JSONLEntry` contract). **Fixtures must not contain substrate-guard-banned tokens** (no spinner/idle glyphs, no `\x1b[`, no `"Pasted text"`); plain JSON text is fine.

**`producer_test.go` (AC 2 + AC 4):**
- **drain on channel close:** feed a buffered channel with N synthetic events, close it, run `drain`, assert it returns and `OnEvent` saw the mapped events (drops excluded).
- **drain on ctx cancel:** start `drain` on an open channel, cancel ctx, assert it returns promptly.
- **re-subscribe across restart:** a fake `Subscriber` returns `ch1`, then `ch2`, then blocks until ctx-done and returns `ctx.Err()`; close `ch1`; assert `Run` calls `Subscribe` again and drains `ch2`; cancel ctx; assert `Run` returns `ctx.Err()` and `Subscribe` was called the expected number of times (the "no leak across restart" observable).
- **nil OnEvent is a no-op beyond draining:** with `OnEvent == nil`, feed events + close; assert `Run`/`drain` drain the channel and return without panicking.
- A fake `Subscriber` (a function closing over scripted channels) is the only double needed — no `*tuidriver.Session`.

**Gate:** `make check` (vet + `-race` + staticcheck + `cmd/substrate-guard`) green. The substrate guard must pass on the new files **and their fixtures**.

---

## Open question — which JSONL, and surviving rotation (architect decision)

The ticket flags this as a real design call. **Decision: the producer does not own JSONL-path resolution or live-`/clear`-rotation survival. It accepts an injected resolver and re-subscribes per session via the supervisor's restart signal.** Rationale:

1. **Supervisor restart is handled by this slice.** Each newly-hosted session ends the prior one (`sess.Wait()` returns) → per-session ctx cancels → Events channel closes → `Run` re-subscribes → `WaitForPTY` blocks for the new session → `resolve` runs again. This is exactly AC 2's "no leaked goroutine across a session restart," and it is unit-tested via the fake `Subscriber`.

2. **Live `/clear` rotation is out of scope and deferred to #616.** A `/clear` rotates claude's on-disk session UUID **without** restarting the supervised process, so the Events channel does **not** close and the producer keeps tailing the now-silent old JSONL. Detecting this needs the rotation watcher (`internal/sessions/rotation`) or an fsnotify watch — pool-layer machinery the producer must not import. The producer already exposes the re-subscription hook (its per-session lifecycle); #616, where the pool + rotation watcher are in scope, supplies the trigger (e.g. cancel-and-re-resolve on a `RotateID`-style signal). This slice must **not** pull `internal/sessions`/rotation in.

3. **The production resolver belongs to #616.** Because the supervisor spawns with `--continue` (no `--session-id`), there is no stable UUID at spawn time — exactly why `deliverViaSession` leaves `JSONLPath` empty (`supervisor.go:234-239`). The pool, however, tracks the current bootstrap UUID via reconcile + the rotation watcher (`Pool.Default().ID()`, kept fresh). #616's resolver can therefore be `tuidriver.SessionJSONLPath(home, workDir, pool.Default().ID())`, re-evaluated per subscription so a rotation picks up the new file. The resolver **should return the current file size as `startOffset`** so a (re)subscription streams only new events instead of replaying the whole conversation. None of this is #615 work — the resolver is a `func(ctx)(string,int64,error)` parameter here; #615 ships the mechanism, #616 supplies the policy.

---

## Open questions (for implementation)

- **Thinking content-block field name.** `thinkingText` reads `block.Raw["thinking"]` from `type=="thinking"` blocks (the Anthropic extended-thinking shape `{"type":"thinking","thinking":"…","signature":"…"}`). tui-driver has no thinking-text helper to lean on (`AssistantText` covers only `"text"`). **Confirm the `"thinking"` field name against a real claude JSONL line** (or tui-driver's `docs/knowledge/architecture/jsonl-layout.md`) before finalizing; the structure otherwise mirrors `AssistantText` exactly. If the field differs, only `thinkingText` changes.
- **`subscribeRetryDelay` value.** A small package const (≈500ms) caps the spin on a persistent post-`WaitForPTY` resolution error. Tune if it proves chatty; it only fires on the abnormal path.

---

## Out of scope (siblings)

- v2 wire-envelope mapping, the per-phone push, capability intersection/enforcement, and the capability-gated fan-out — **#616** (the consumer; carries `security-sensitive`).
- The production JSONL resolver + live-`/clear`-rotation re-subscription trigger — **#616** wiring (see Open question).
- `screen_snapshot` is already shipped on the supervisor (`ScreenSnapshot`, #618); modals / queue / stall surfacing / thinking-state events — other #596/#597 children.
- The `pyry acp` adapter / ACP `stopReason` return — **#600**.
- Do **not** write `docs/knowledge/codebase/615.md` — the documentation phase owns it.

---

## Scope check (S confirmed)

- **Production source files:** 3 — `internal/supervisor/supervisor.go` (mod, ~8 LOC), `internal/turnbridge/producer.go` (new), `internal/turnbridge/mapper.go` (new). Under the ≥5 split gate.
- **New exported types:** 4 — `Producer`, `Config`, `Subscriber`, `SessionHost`. Under the >5 red line. (`New`, `Run`, `NewSessionSubscriber` are functions/methods; the JSONL resolver is an unnamed inline func param.)
- **Consumer call sites needing simultaneous update:** 0 — `Session()` is additive; the package is greenfield and unwired. No edit fan-out.
- **State-machine reject branches:** the producer has ~2 (ctx vs channel-close) + the mapper's drops (no per-branch log fan-out — one debug call in the drain). Well under 10.
- **Total LOC** (production + helpers + tests): ≈ 470 — under the ~600 total red line.
- **ACs:** 5, tightly coupled (accessor → drain → map → output → tests) — one cohesive slice.
- **File overlap:** the only existing file touched (`internal/supervisor/supervisor.go`) is not modified by any in-flight feature branch (checked at architect time). The new package cannot overlap.
