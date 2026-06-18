# Spec #668 — Deterministic transcript-growth commit-confirm for supervised turn delivery

Fixes the mobile "first message after create_conversation never reaches claude" bug
(pyrycode-mobile#421 rung-3, last blocker) by replacing the supervised-bootstrap delivery
path's reliance on tui-driver's **stochastic chip heuristic** with a **deterministic
transcript-growth commit-confirm**. `WriteUserTurn` returns `nil` only when the resolved
claude transcript actually grows; otherwise it returns the existing `ErrTurnNotCommitted`
(a loud, retryable failure) instead of a false ack.

`security-sensitive` — this is the delivery path for untrusted, phone-originated turn
payloads (`send_message` → `WriteUserTurn` → live claude). Architect security review is at
the end of this spec; verdict **PASS**.

---

## Files to read first

- `internal/supervisor/supervisor.go:199-255` — `WriteUserTurn` + `deliverViaSession` + the
  `deliverFn` seam. **This is the file you rewrite.** Extract: the capture-then-unlock pattern,
  the current `deliverViaSession` body (becomes the nil-resolver fallback), the
  `ErrTurnNotCommitted` sentinel (reused, do not add a new one), and the long doc-comment at
  `:223-239` that explains *why* `JSONLPath` was left empty — you are superseding that comment.
- `internal/supervisor/supervisor.go:66-117` — `Config` struct. Add the new optional
  `ResolveTranscript` field here, modelled on `ValidateConversation` (`:106-112`): optional,
  nil-safe, doc'd as the production-vs-test seam.
- `internal/supervisor/supervisor.go:364-392` — `New`; note `deliverFn` is set here. No change,
  but the new resolver is read from `s.cfg`, not a separate set-once field.
- `internal/sessions/reconcile.go:57-86` — `mostRecentJSONL(dir) (SessionID, error)`. **Reuse
  this**; the new `newTranscriptResolver` wraps it + `os.Stat` for size. Note `jsonlExt`
  (same package) and the empty-dir contract (`"" , nil`, not an error).
- `internal/sessions/pool.go:351-369` — bootstrap `supCfg` construction (the `ValidateConversation`
  closure precedent). Wire `supCfg.ResolveTranscript` here, gated on `cfg.ClaudeSessionsDir != ""`.
  `cfg.ClaudeSessionsDir` is in scope (also stored as `p.claudeSessionsDir`, `:160`).
- `internal/sessions/pool.go:946-964` — `buildSession` (the per-`--session-id` path). **Do NOT
  wire the resolver here** — out of scope (see § Out of scope); leave nil.
- `cmd/pyry/interactive_turn_stream_v2.go:98-155` — `resolveLatestSessionJSONL(dir)`. **Reference
  only, do not call.** It is the sibling resolver the turn-bridge already uses (newest `*.jsonl`
  + size, same stem regex, same mtime+lexical tiebreak). Your `newTranscriptResolver` resolves the
  *same file by construction* (same dir, same algorithm) — that coherence is the point; it is why
  the turn-bridge can stream the very turn this path confirmed.
- `internal/relay/handlers/send_message.go:113-148` — the error switch. **No change needed**:
  `ErrTurnNotCommitted` already falls into `default` → `CodeServerBinaryOffline` retryable
  (`:148`). Read it to confirm the loud-failure → retryable-wire-reply mapping is already wired.
- `$(go list -m -f '{{.Dir}}' github.com/pyrycode/tui-driver)/pkg/tuidriver/deliver.go:94-171` —
  `DeliverPrompt` + the `deliverPrompt` loop. Extract the **exact false-ack mechanism**
  (`:155-163`: no commit signal + no `[Pasted text]` chip ⇒ `Committed=true`, "committed-but-slow")
  and that `JSONLPath` commit detection is `os.Stat` *appearance* (`:188-191`), not growth.
- `.../pkg/tuidriver/ready.go:42-54` — `WaitReady`/`Readiness`. Unchanged; we keep #594's
  "ignore the Readiness policy fields" stance.
- `docs/knowledge/codebase/594.md` — the direct predecessor. This spec is the JSONL-net upgrade
  #594 explicitly deferred (it "gates on `Committed` with no downstream net"; ptyrunner treats
  `Committed` as advisory *with* a JSONL/watchdog net — this path now gets that net).

---

## Context

**The user-visible failure.** A mobile user creates a discussion, types a one-line message; the
app acks but no assistant reply ever streams. The rung-3 e2e (`InteractiveStreamE2ETest.kt:79`)
times out at 90s on the streamed-reply assertion. Live-verified on Pixel_8 + real claude.

**Root cause (confirmed during this design).**

1. `create_conversation` is pure registry metadata — it never spawns or restarts claude
   (confirmed: `internal/relay/handlers/create_conversation.go`, codebase/666.md § "no session
   spawn at create time"). So the ~7.5s clean exit + `--continue` restart that follows is
   **claude's own black-box behaviour**, not a pyrycode-triggered restart (idle-eviction is
   default-disabled; no other code path stops the child here). **Design consequence: the
   supervisor cannot and should not try to prevent the restart — its whole reason to exist is to
   tolerate the child exiting at any time. The fix makes turn *delivery* robust to a restart
   racing the send, it does not chase the restart's cause.**

2. The `send_message` arrives while claude is mid-restart/resume. `WriteUserTurn` captures
   whatever `*tuidriver.Session` is live and delivers to it. `WaitReady` returns when claude
   first reaches idle — which during a `--continue` resume can be a *transient* idle, or the
   captured session is the dying pre-restart one. Either way the typed turn is lost.

3. **The actual bug is the false ack.** The prompt is short and single-line, so `DeliverPrompt`
   uses `TypePrompt` (no bracketed paste, so no `[Pasted text]` chip ever renders). When the turn
   doesn't commit, `deliverPrompt` sees *no commit signal* and *no chip* and concludes
   "committed-but-slow" → `DeliverResult.Committed = true` (tui-driver deliver.go:155-163). So
   `deliverViaSession` returns `nil`, the handler sends `TypeAck`, and **the phone has no reason
   to retry** — it was told the turn succeeded. The transcript is byte-identical before and after.

**Why the existing levers don't catch it.** tui-driver's optional `JSONLPath` commit signal is
*appearance*-based (`os.Stat` — "the file exists"). Under `--continue` the per-session JSONL
already exists from the prior turn, so passing `JSONLPath` would make `promptDidCommit` return
`true` instantly — actively worse. A **growth** check (size increased) is the only reliable
signal, and tui-driver v1.3.0 does not offer one. It must be done pyrycode-side.

**Scope boundary (from the ticket / PO).** The "chip-independent commit detection + in-driver
re-delivery" half lives in tui-driver (#124, a versioned dep). This ticket is the **pyrycode-side
daemon fix**: a deterministic commit-confirm that converts the false ack into a loud, retryable
failure. See § "AC #3 and the cross-repo boundary".

---

## Design

### One change in one sentence

`deliverViaSession` stops trusting `DeliverResult.Committed` on the supervised-bootstrap path;
instead it records the newest transcript's size *before* delivery and returns `nil` only after it
observes that transcript **grow** (size increased, or a `/clear`-rotated newer file appeared);
no growth within a bounded window → `ErrTurnNotCommitted`.

### New `Config` field (the contract)

```go
// ResolveTranscript, when non-nil, resolves the newest claude transcript for the
// supervised workdir: its absolute path and current byte size. ("", 0, nil) means
// no transcript exists yet (valid — a fresh session that has written no turn). A
// non-nil error means the dir is unreadable.
//
// When set, deliverViaSession uses transcript *growth* as a deterministic commit
// signal instead of trusting DeliverResult.Committed (tui-driver's chip heuristic,
// which false-acks short single-line turns lost to a --continue restart race).
// When nil, the #594 Committed-based behaviour is preserved (foreground mode, tests).
ResolveTranscript func(ctx context.Context) (path string, size int64, err error)
```

- Optional + nil-safe, exactly like `ValidateConversation`. No new exported type — it is a func field.
- Read from `s.cfg` inside `deliverViaSession` (no new set-once field; `deliverFn` already injects
  the whole method for tests).

### Rewritten `deliverViaSession`

Branch on the resolver:

- **`ResolveTranscript == nil`** → unchanged #594 body: `WaitReady` → `DeliverPrompt` →
  `!Committed ⇒ ErrTurnNotCommitted` → `nil`. (Foreground / unit-test ergonomics.)
- **`ResolveTranscript != nil`** → wire the real session methods into a pure helper
  `confirmViaTranscriptGrowth` and return its result.

`deliverViaSession` keeps `JSONLPath: ""` in the `DeliverOpts` (still no in-driver appearance
signal — we own the growth signal now). Supersede the `:223-239` doc-comment to explain the
growth confirm.

### The pure confirm helper (the testability seam)

Mirror #594's "seam one level above the screen" and tui-driver's own `deliverDeps` pattern so
every branch is unit-testable with **no live claude and no screen literal**:

```go
type deliverGrowthDeps struct {
    waitReady func(ctx context.Context) error                       // wraps Session.WaitReady
    deliver   func(ctx context.Context) (committed bool, err error) // wraps Session.DeliverPrompt
    resolve   func(ctx context.Context) (path string, size int64, err error) // Config.ResolveTranscript
    log       *slog.Logger
}

// confirmViaTranscriptGrowth: WaitReady → baseline → DeliverPrompt → poll for growth.
// Returns nil only on observed growth; ErrTurnNotCommitted on no-growth-within-window.
func confirmViaTranscriptGrowth(ctx context.Context, d deliverGrowthDeps) error
```

Behaviour contract (the body is ~35 lines — implement to this contract, do not pre-write it here):

1. `waitReady(ctx)` err → `return fmt.Errorf("wait ready: %w", err)` (preserves
   `errors.Is(..., context.DeadlineExceeded/Canceled)` through the chain, like #594).
2. `basePath, baseSize, berr := resolve(ctx)`. If `berr != nil`: we cannot establish a baseline →
   log `Warn` and **fall back to Committed**: `committed, err := deliver(ctx)`;
   `err` → return; `!committed` → `ErrTurnNotCommitted`; else `nil`. (Robustness: a transient dir
   read error must not manufacture a false-negative that drives a double-send.)
3. `committed, err := deliver(ctx)` → `err` → return (already prefixed by tui-driver). Ignore
   `committed` on the growth path (it is the unreliable heuristic we are replacing).
4. Poll every `transcriptConfirmPoll` until `transcriptConfirmTimeout` elapses or `ctx` is done:
   re-`resolve`; if it succeeds and `grew(basePath, baseSize, newPath, newSize)` → `return nil`.
5. `ctx` done during the poll → `return ctx.Err()` (matchable; lands in the handler's `default` →
   retryable). Timeout with no growth → `return ErrTurnNotCommitted`.

```go
// grew reports whether the newest transcript advanced past the pre-delivery baseline:
// a larger file (turn appended) or a different newest file (/clear rotation). A still-
// empty resolution (newPath == "") is not growth.
func grew(basePath string, baseSize int64, newPath string, newSize int64) bool {
    return newPath != "" && (newPath != basePath || newSize > baseSize)
}
```

Baseline is captured **after** `WaitReady` returns idle, so any JSONL writes claude makes *during*
resume are already in the baseline and cannot be mistaken for the user turn.

### Two new unexported constants (`internal/supervisor`)

```go
// transcriptConfirmTimeout bounds the post-delivery wait for the transcript to grow.
// Generous on purpose: claude appends the user turn to its JSONL at commit time, so a
// real commit shows growth within ~1-2s; the margin covers a slow --continue resume
// drain. A too-tight value risks a false negative (turn committed late) → phone retry
// → duplicate turn. Tuning knob, not a contract; bump if a slow-resume false negative
// is ever OBSERVED. Capped by the caller's ctx (handler's 30s sendMessageDeliverTimeout).
const transcriptConfirmTimeout = 10 * time.Second

// transcriptConfirmPoll is the growth poll interval — matches tui-driver promptCommitPoll.
const transcriptConfirmPoll = 150 * time.Millisecond
```

### The resolver (`internal/sessions`)

Add near `mostRecentJSONL`, **reusing it** (no third scanner):

```go
// newTranscriptResolver returns a Config.ResolveTranscript closure over dir: the newest
// <uuid>.jsonl plus its current size, ("",0,nil) when none exists yet. Reuses
// mostRecentJSONL so it selects the same file reconcile + the rotation watcher do.
func newTranscriptResolver(dir string) func(ctx context.Context) (string, int64, error)
```

Contract (≈12 lines): call `mostRecentJSONL(dir)`; ReadDir error → propagate; empty stem →
`("", 0, nil)`; else `path := filepath.Join(dir, string(id)+jsonlExt)`, `os.Stat` it,
return `(path, info.Size(), nil)` — a vanished-between-scan-and-stat file returns the stat error
(treated as "no baseline" / "no growth", never a panic). The `ctx` param is unused today
(satisfies the field signature; lets a future resolver honour cancellation).

### Wiring (`internal/sessions/pool.go`, bootstrap only)

At `:351-369`, after the `ValidateConversation` block:

```go
if cfg.ClaudeSessionsDir != "" {
    supCfg.ResolveTranscript = newTranscriptResolver(cfg.ClaudeSessionsDir)
}
```

Bootstrap-only by design — see § Out of scope for why `buildSession` (`:946`) stays nil.

### Data flow

```
send_message handler
   │  WriteUserTurn(deliverCtx=30s, convID, payload)         [unchanged #594 path]
   ▼
Supervisor.WriteUserTurn → capture *Session under sessMu, unlock → deliverFn
   ▼
deliverViaSession (ResolveTranscript != nil)
   │  WaitReady(ctx) ─ idle gate (unchanged)
   │  resolve(ctx) ─ baseline (newest *.jsonl path + size)   ◄── newTranscriptResolver
   │  DeliverPrompt(ctx) ─ types/pastes the turn (Committed ignored)
   │  poll resolve(ctx) every 150ms ≤10s ─ grew(base, new)?
   ▼
grew → nil (ack)        no growth in window → ErrTurnNotCommitted
                                            ▼
                        handler default arm → server.binary_offline, Retryable=true
```

---

## Concurrency model

No new goroutines. `confirmViaTranscriptGrowth` runs synchronously on the same handler/per-conn
goroutine `WriteUserTurn` already runs on (post-`sessMu`-release, on the captured `*Session`
pointer — the #594 capture-then-unlock contract is unchanged). The poll is a `time.Ticker` +
`time.Timer` + `ctx.Done()` `select`, fully bounded; both timer and ticker are `Stop`-ped on
return. No new locks, no new lock ordering. The resolver does read-only `os.ReadDir`/`os.Stat`
on a server-owned dir — no shared mutable state. A `setSession(nil)+Close()` racing the captured
pointer remains teardown-safe (#594): a torn-down session surfaces as a `WaitReady`/`DeliverPrompt`
error → loud failure, never a crash or false ack.

---

## Error handling

| Condition | Return | Wire result (handler, unchanged) |
|---|---|---|
| Growth observed | `nil` | `TypeAck` |
| No growth within `transcriptConfirmTimeout` | `ErrTurnNotCommitted` | `server.binary_offline`, retryable |
| `ctx` cancelled mid-poll | `ctx.Err()` (matchable) | `default` → retryable (or propagated if parent conn closing) |
| `WaitReady` ctx timeout (claude busy) | wrapped `context.DeadlineExceeded` | retryable |
| `DeliverPrompt` PTY write error | wrapped, returned | retryable |
| No live session at capture | `ErrNoLiveSession` (existing) | retryable |
| Baseline `resolve` error | fall back to `Committed` (then nil / `ErrTurnNotCommitted`) | ack / retryable |
| `ValidateConversation` refuses id | `ErrConversationNotFound` (existing) | `conversation.not_found`, non-retryable |

Every non-nil return keeps the stable `"supervisor: write user turn:"` prefix (applied by
`WriteUserTurn`, unchanged). **No new sentinel, no new wire code, no handler edit** — the design
deliberately reuses #594's `ErrTurnNotCommitted` → `default` → `binary_offline` mapping.

**At-least-once, not exactly-once.** A late-committing turn (growth appears *after* the window)
→ `ErrTurnNotCommitted` → phone retry → duplicate turn. This is the same at-least-once property
#594's deliver-timeout already has; the generous 10s window keeps it rare. Exactly-once delivery
(idempotency keys / in-driver re-delivery) is out of scope (tui-driver#124).

---

## Testing strategy

stdlib `testing`, table-driven, `-race`, `t.Parallel()`. Test files:
`internal/supervisor/supervisor_test.go`, `internal/sessions/reconcile_test.go`.

**`confirmViaTranscriptGrowth` (pure, fake `deliverGrowthDeps` — no live claude, no screen
literal).** Drive `resolve` with a counter closure returning scripted `(path,size)` per call:

- *Growth on first poll* → `nil`.
- *Growth after several polls* (baseline N times, then N+1) → `nil` (proves the poll loop waits).
- *Never grows* (constant size) → `errors.Is(err, ErrTurnNotCommitted)`. Use a tiny injected
  timeout/poll via the deps or package vars so the test isn't real-time-bound (10s is too slow
  for a unit test — make the timeout/poll injectable, e.g. fields on `deliverGrowthDeps` or
  unexported package vars the test overrides; pick whichever is cleaner and document it).
- *Rotation counts as growth* (baseline `old.jsonl`/5000, then `new.jsonl`/200) → `nil`.
- *Empty baseline then file appears* (`""`/0 → `a.jsonl`/120) → `nil`.
- *`waitReady` error* → wrapped, `errors.Is(context.DeadlineExceeded)` survives.
- *`deliver` error* → returned (wrapped by the fake), no panic.
- *Baseline `resolve` error + `deliver` committed=true* → `nil` (fallback path).
- *Baseline `resolve` error + `deliver` committed=false* → `ErrTurnNotCommitted` (fallback path).
- *`ctx` cancelled mid-poll* → non-nil, `errors.Is(context.Canceled)`.

**`newTranscriptResolver` (`reconcile_test.go`, reuse existing `t.TempDir` + `mostRecentJSONL`
fixtures).**

- Picks the newest `<uuid>.jsonl` and returns its real size.
- Empty dir → `("", 0, nil)` (not an error).
- Missing dir → propagates the ReadDir error.
- Ignores non-`.jsonl` / non-UUID-stem files (inherited from `mostRecentJSONL`; one assertion).

**Wiring regression (`internal/sessions`).** A bootstrap built with a non-empty
`ClaudeSessionsDir` has a non-nil `supCfg.ResolveTranscript`; with empty dir, nil. (Assert via an
existing pool construction test or a focused one — do not add a new exported accessor; if the
field isn't reachable from the test, assert behaviourally that growth-confirm engages.)

**Existing `internal/supervisor` tests.** The `deliverFn`-override branch tests
(`CommittedReturnsNil`, `NotCommittedFailsLoud`, `NoSessionFailsLoud`, `NotReadyFailsLoud`, …)
are **unaffected** — they override `deliverFn` and never reach `deliverViaSession`. The real-
machinery `RealDeliverNotReadyFailsLoud` still passes (WaitReady times out before the resolver is
consulted). Run them to confirm; do not rewrite.

**Gate.** `make check` green: `go vet`, `-race`, `staticcheck`, and **`cmd/substrate-guard`** —
the resolver reads file *sizes* (`os.Stat`) only, never JSONL *content*, so no claude-screen
literal enters pyrycode and the seal stays intact (the same property as `ScreenSnapshot`'s
render-inside-the-seal discipline). e2e `-tags e2e` suites are not in `make check`; #594's five
skipped fakeclaude `send_message` tests stay skipped (fakeclaude renders no idle screen — #603).

---

## AC #3 and the cross-repo boundary

AC #1 (transcript grows) and AC #2 (no false ack; loud retryable failure when the turn didn't
land) are **fully satisfied daemon-side by this spec**. AC #3 (the Kotlin e2e renders the reply)
is *verification* and **cross-repo-contingent**, exactly as the PO scoped it:

- This fix turns the silent false-ack into a `server.binary_offline` **retryable** reply. The
  e2e goes green when the **consumer retries** that retryable failure — a mobile-client behaviour
  (pyrycode-mobile), honouring the protocol's existing `Retryable` flag (set since #594). It is a
  separate repo; it cannot be a pyrycode child ticket.
- This is **not** the tui-driver#124 route-back case. The ticket says route back to PO only if the
  investigation finds *claude is genuinely ready when the prompt arrives yet the turn still fails*
  (⇒ only fix is in tui-driver's chip heuristic). The investigation found the opposite: claude is
  **not** genuinely ready (mid-`--continue` resume), so the deterministic-confirm fix is squarely
  in pyrycode's domain. We write the spec; we do not route back.

**Developer guidance:** AC #3 is **operator-verified, not a Go test** (mirrors #666's AC #3 —
live Noise v2 + real phone + navigation). Do **not** block the ticket on it and do **not** add an
e2e Go test for it. Your deliverables are AC #1 + AC #2 (the daemon-side change + unit tests). If
operator verification then shows the phone does **not** retry on `server.binary_offline`, that is
a mobile-side follow-up (the consumer must honour the retryable contract) — note it on the PR;
it does not reopen this daemon work.

---

## Out of scope (named)

- **In-driver re-delivery** (clear residue + re-send so the turn lands in a single daemon call) —
  tui-driver#124. Doing it pyrycode-side would nest a retry around `DeliverPrompt`'s own retry and
  reintroduce the #227 buffered-residue double-paste risk that genuinely needs claude input-line
  knowledge (tui-driver's domain). The deterministic confirm here makes the *failure* honest; the
  *single-call landing* is the driver's job.
- **The `buildSession` / `--session-id` path** (`pool.go:946`). Those sessions spawn with a pinned
  UUID and share the workdir's sessions dir, so "newest in dir" is ambiguous across peers; and no
  mobile turn routes there today (lazy-bind on the bootstrap, #666). They keep the nil-resolver
  `Committed` behaviour. A future ticket that routes turns to per-`--session-id` sessions should
  build the path from the pinned id (`SessionJSONLPath`) instead of scanning.
- **Reading transcript content** to confirm *which* turn landed. We confirm growth (size), not
  identity — keeps the substrate seal and adds no JSONL-parsing surface. The turn-bridge (#661)
  owns content resolution downstream.
- **Exactly-once delivery / idempotency.** At-least-once with phone retry is the accepted model
  (§ Error handling).
- **Preventing claude's ~7.5s restart.** Black-box claude behaviour; the supervisor tolerates it
  by design (§ Context).

---

## Open questions

1. **`transcriptConfirmTimeout = 10s`** is an evidence-light first value (no telemetry on real
   `--continue` resume-drain latency yet). It is a documented tuning knob; revisit if a slow-resume
   false negative (→ duplicate turn) is observed in operator verification.
2. **Injecting the unit-test clock.** The "never grows" / poll tests need the 10s timeout and
   150ms poll to be overridable so tests run in milliseconds. Developer's call between
   `deliverGrowthDeps` fields and test-overridable unexported package vars — pick the one that
   keeps production call sites clean; both are fine.

---

## Security review (required — `security-sensitive`)

This path delivers untrusted, phone-originated turn payloads. Walked adversarially:

- **Trust boundaries.** The phone controls only `payload` (the turn text) and `conversation_id`
  (validated by the existing `ValidateConversation` → `conversations.ErrConversationNotFound`,
  unchanged). The new code adds **no** phone-controlled input to any new sink. `payload` flows to
  `DeliverPrompt` exactly as in #594 — unchanged surface.
- **Path handling / traversal.** The transcript path is built server-side: `filepath.Join(dir,
  <stem>+jsonlExt)` where `dir = cfg.ClaudeSessionsDir` (server-resolved from the operator's
  workdir, never phone input) and `<stem>` is a UUID-regex-validated filename from
  `mostRecentJSONL` (no `..`, no separators admitted). No phone value reaches the filesystem.
- **TOCTOU.** A file may vanish between scan and `os.Stat` → the stat error is handled as
  "no baseline / no growth" (best-effort), never a panic or a misread. Read-only throughout; the
  daemon never writes, truncates, or deletes the transcript.
- **Substrate seal.** Only `os.Stat` **sizes** are read — never JSONL bytes — so no claude-screen
  literal enters pyrycode; `cmd/substrate-guard` stays green (gated in `make check`).
- **DoS / unbounded work.** The confirm poll is doubly bounded (`transcriptConfirmTimeout` and the
  handler's 30s `deliverCtx`); each tick is one `ReadDir`+`Stat` of a small dir. No unbounded loop,
  no growth in per-turn cost.
- **No false ack (the security-relevant correctness property).** The change strictly *tightens*
  the success contract: `nil` is now returned only on observed, deterministic transcript growth.
  It cannot make `WriteUserTurn` return `nil` in any case where #594 returned an error. A phone
  can no longer be told a turn landed when it did not.
- **Information disclosure.** Error strings carry only an `os` path/errno (e.g. "read claude
  sessions dir …"), never file contents; the stable `"supervisor: write user turn:"` wrap is
  unchanged.

**Verdict: PASS** — no MUST FIX. The fix is a net security improvement (eliminates a false ack on
an untrusted inbound path) and introduces no new attacker-controlled sink.
