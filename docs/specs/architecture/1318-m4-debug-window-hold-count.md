# #1318 — Raise the stream harness daemon to Debug and count the pending/hold marker in M4's window

**Ticket:** [#1318](https://github.com/pyrycode/pyrycode/issues/1318) · **Size:** S (confirmed, not downgraded) · **Labels:** `bug`, `security-sensitive`

## Files to read first

Read these before writing anything. Line ranges are the part that matters; the one-liner is what to extract.

| File | What to extract |
|---|---|
| `internal/e2e/relay_v2_stream_new_session_test.go:461-481` | M4's `t.Fatalf` — the record whose header and "how to read that window" prose this ticket changes. Note the argument order: `daemonLogWindow(...)` is the `%s` before the final `log=%q`. |
| `internal/e2e/relay_v2_stream_new_session_test.go:504-521` | `daemonLogBudget` + `msgqueueRetryWarn` — the constant whose sizing arithmetic goes stale, and the precedent block the new marker constant copies (duplicate-verbatim rationale + the "0 beside a visibly-populated excerpt" drift guard). |
| `internal/e2e/relay_v2_stream_new_session_test.go:523-571` | `daemonLogWindow` — the four arms and the header `fmt.Sprintf` the new count joins. Note the header is built AFTER the two sentinel arms return. |
| `internal/e2e/relay_v2_stream_new_session_test.go:573-687` | `TestDaemonLogWindow` — the table you extend. Note the `notWant` lists on the sentinel arms; they are the boundary pin the new count must inherit. |
| `internal/e2e/harness.go:368-442` | `StartStreamInteractiveWithRelay` — the `extraFlags` literal at `:419-422` is the single edit site. Signature stays as-is. |
| `internal/e2e/harness.go:621-671` | `spawnWith` — argv assembly: pyry flags, then `extraFlags`, then `--`, then claude args. Confirms `-pyry-verbose` lands on pyry's side of the separator. |
| `cmd/pyry/main.go:685-746` | `-pyry-verbose` → `slog.LevelDebug` → the stderr `TextHandler`. Read far enough to see the flag's ONLY effect is the level (the `logRing` tee is unconditional). |
| `internal/msgqueue/queue.go:577-611` | The two drain arms side by side: the `Debug` hold line at `:589` (the new marker, copy it verbatim) and the `Warn` retry line at `:607`. Read the comment at `:583-586` for why `Debug` is deliberate. |
| `cmd/pyry/main.go:923-932` | `msgqueue.New`'s `Pending:` closure — `errors.Is(err, supervisor.ErrTrustModalPending)`. This is the single gate on the hold arm and the load-bearing fact behind the prose correction below. |
| `cmd/pyry/main.go:1625-1653` | `newInboundDeliver` — the four steps and which of them can error. Confirms nothing on this path produces `ErrTrustModalPending`. |
| `internal/streamsup/runner.go:272-285` | `(*streamsup.Runner).WriteUserTurn` — returns `ErrNoLiveChild` / `turncommit.ErrDropped` / nil. The stream path's whole error vocabulary. |
| `internal/supervisor/supervisor.go:440-455` | `ErrTrustModalPending`'s only production producer — the PTY delivery gate. Not on the stream path. |
| `cmd/pyry/stream_turn_busy.go:354-396` | `waitIdleForDelivery` → `WaitIdle` — logs nothing at any level. One half of AC-5's residue. |
| `cmd/pyry/stream_turn_drain.go:220-234` | The active-session gate's `Debug` drop. This is the Debug record the AC-1 guard relies on, and the drop the test header at `:62-72` already documents. |
| `cmd/pyry/main.go:1524`, `:1552`; `internal/msgqueue/queue.go:69`, `:85` | The four timing constants the prose names: 30 s, 15 m, 1 s, 2 m. All four verified current — do not re-derive, do not change. |

## Context

M4's failure record gained a daemon-log window at `183de2f` (#1296). Its first live capture (2026-08-04, during QA of PR #1317) rendered `<empty: the daemon logged nothing between ack #2 and expiry>`. #1298 had pre-assigned that emptiness a meaning — "silence ⟹ the park hypothesis leads" — but M4's own prose immediately contradicts it: at the daemon's default `LevelInfo` several states are silent, and the record cannot tell them apart. The headline discriminator is not a discriminator.

The fix is on the harness side, not in production. `internal/msgqueue/queue.go:589` logs the pending/hold retry at `Debug` deliberately ("a long legitimate wait must not spam the operator log"), and `pyry` already ships `-pyry-verbose` to raise the stderr handler from `LevelInfo` to `LevelDebug`. So: pass the flag in the harness, teach the renderer to count the marker that becomes visible, and correct the prose.

**Out of scope, explicitly:** diagnosing, reproducing or fixing the stall. That is #1298, which owns the cause and names its own deterministic harness. This ticket fixes the instrument only. Do not wrap the test in a re-run, do not add a skip, do not make the flake green — the file's own comments at `:406-422` record why (#1168 / PR #1169: a SKIP exited 0 and read as a pass).

### One correction the ticket does not carry

The ticket inherits M4's existing claim that three states are silent at Info, the third being "RETRYING at that same 1s cadence under the pending/hold branch". **That third state cannot occur on this harness**, and the chain is short enough to state in full:

- `msgqueue`'s hold arm fires only when `q.pending != nil && q.pending(err)` (`internal/msgqueue/queue.go:587`).
- `cmd/pyry` wires exactly one classifier: `Pending: func(err error) bool { return errors.Is(err, supervisor.ErrTrustModalPending) }` (`cmd/pyry/main.go:932`).
- `ErrTrustModalPending` has one production producer: the PTY delivery gate in `internal/supervisor/supervisor.go:455`.
- This harness sets `interactive_runner:"stream-json"`, so the bound session's runner is a `*streamsup.Runner`, whose `WriteUserTurn` returns only `ErrNoLiveChild`, `turncommit.ErrDropped` or nil (`internal/streamsup/runner.go:283-285`), and whose `Activate` path never reaches the trust gate.

So on this harness the hold arm is **structurally unreachable**, and `pending/holds: 0` is the expected reading rather than an empirical one. That does not make the count pointless — it makes it a *contract* check, the same posture `TestDaemonLogWindow`'s empty-window arm already takes ("a CONTRACT check, not a manufactured failing scenario", `:573-580`). It converts a claim into a measurement: a non-zero count on this harness means the delivery path changed under the test, which is worth knowing on sight.

The prose the developer writes must carry this. Writing "the pair now decides between the retry arm and the hold arm" would replace one over-claim with another — the same shape as #1296's own replacement prose being wrong on two of its three numbers.

**Verification step for the developer (do this, don't take it on trust):** `grep -rn ErrTrustModalPending internal/ cmd/ --glob '*.go'` and confirm the only non-test producer is `internal/supervisor/supervisor.go:455`. If a second producer has appeared, the correction above is stale and the prose must change accordingly.

## Design

Two files. No new exported symbols, no signature changes, zero consumer call-site edits.

### 1. `internal/e2e/harness.go` — raise the level

`StartStreamInteractiveWithRelay` appends `-pyry-verbose` to its `extraFlags` literal (`:419-422`), unconditionally, for all six callers:

```
extraFlags: []string{
    "-pyry-workdir=" + home,
    "-pyry-relay=" + relayURL,
    "-pyry-verbose",
}
```

Extend the doc comment with a paragraph covering: why (M4's window needs the `Debug` marker to be a measurement rather than a suppression), the blast-radius decision, and the flag's exact effect.

**Why unconditional rather than a parameter or a `…Verbose` sibling.** A bool parameter costs six call-site edits for no gain and leaves two harness variants; a reader of a captured daemon log would then have to know which variant produced it before the log means anything. One level across every stream spec keeps the instrument singular.

**Blast radius — verified, not assumed:**

- Callers are exactly six, all in `internal/e2e` under tag `e2e`: `relay_v2_stream_{modal,queue_drain,unrecognized,interrupt,send,new_session}_test.go`. The signature is untouched, so none of them changes.
- `internal/e2e/realclaude/interactive_stream_liveness_test.go` does **not** call this constructor — it transcribes only the `config.json` seam and says so at `:152`. Different build tag (`e2e_realclaude`); `harness.go` is `e2e || e2e_install`. The publishable-run-record machinery is therefore untouched.
- No `internal/e2e` test asserts on the daemon's captured stderr *content*. Every `.Stderr` hit in the sibling stream specs is `RunResult.Stderr` from a `pyry pair` CLI invocation, not `h.Stderr`. The one place `h.Stderr` is dumped wholesale is `waitForReady`'s "pyry exited before ready" error (`harness.go:726`) — chattier, same content classes.
- `-pyry-verbose`'s only effect is the handler level (`cmd/pyry/main.go:735-746`). No behavioural branch keys off it.
- `teeHandler.Enabled` delegates to the stderr handler (`internal/control/logs.go:112-113`), so `Debug` records now also enter the 200-entry `logRing` and `pyry logs`' history gets shorter in these six specs. No affected test reads it: `TestLogs_E2E` (`internal/e2e/cli_verbs_test.go:80`) uses `Start(t)` and asserts only a non-empty stdout.

### 2. `internal/e2e/relay_v2_stream_new_session_test.go` — count, guard, correct

#### 2a. The marker constant

A file-level const beside `msgqueueRetryWarn`, copied verbatim from `internal/msgqueue/queue.go:589`:

```go
// msgqueueHoldDebug is the drain's legitimate-hold retry line, verbatim from
// internal/msgqueue/queue.go (the q.log.Debug in the pending branch — Debug, not
// Warn, deliberately: "a long legitimate wait must not spam the operator log").
const msgqueueHoldDebug = "msgqueue: delivery held (awaiting external decision), will retry"
```

Name it `msgqueueHoldDebug`, not `msgqueuePendingHold`: `msgqueueRetryWarn` encodes its level and the level is exactly what makes this pair's readings differ, so encoding it keeps the dependence legible at every use site.

Duplicated rather than exported from `msgqueue` — the rationale block on `msgqueueRetryWarn` (`:513-521`) applies unchanged and should be referenced, not re-argued at length: exporting a log message turns operator-facing prose into an API, and the drift guard is a count of `0` printed beside an excerpt that visibly contains the lines.

Substring matching survives `slog`'s `TextHandler` framing. The handler renders `msg="msgqueue: delivery held (awaiting external decision), will retry"` — the literal contains no quote or backslash, so it is escaped to itself and sits inside the quotes verbatim. This is the same mechanism `msgqueueRetryWarn` already relies on.

#### 2b. The header

`daemonLogWindow`'s header gains a second count, scoped to `window` exactly as the first is:

```
daemon log, ack #2 → expiry: %d bytes, retry Warns: %d (matching %q), pending/holds: %d (matching %q)
```

Both sentinel arms (`<unmeasurable:`, `<empty:`) return before the header is built, so neither count can leak past the boundary. The elision arm restates the header unchanged, so it needs no edit.

#### 2c. `daemonLogBudget` — keep the number, fix the doc

Leave `8 << 10`. Its doc's arithmetic ("~20 retry Warns at ~250 B ≈ 5 KB, so the modelled failure renders COMPLETE") was computed against an `Info`-level window and no longer holds — replace that clause with: the level is now `Debug`, completeness is no longer modelled, and the head+tail elision arm plus the header's report of the window's TOTAL size is the honest fallback. The existing "a tuning knob, not a contract" sentence stays and is now the operative one.

Moving the number is deliberately **not** done: no over-budget window has been observed, and sizing a bound against an imagined chatty run is a defence against a failure that has not happened. The doc must say the arithmetic is retired rather than leave it standing, so a future reader does not read stale numbers as current — the elision arm already handles the case honestly if it arrives.

#### 2d. The AC-1 instrument guard

Placed immediately after M4's success `t.Logf` (`:483`) and before M5. Not a new milestone — do not renumber M5 or touch the header diagram at `:39-48`.

Contract: poll `h.Stderr.Bytes()` for a `Debug`-level record, bounded by a short deadline (5 s at 50 ms is ample and sits well inside the test's existing budget); `t.Fatalf` naming both readings of an absence. The matched literal is `slog`'s `TextHandler` level field, verified against a live handler:

```go
// daemonDebugLevel is slog's TextHandler level field for a Debug record
// ("time=… level=DEBUG msg=…"). The harness starts pyry with -pyry-verbose, so
// its absence from the whole capture means the level flip is broken.
const daemonDebugLevel = "level=DEBUG"
```

**Why here, and why this record is deterministic.** After M2's rotation the runner's sink tag stays `initialUUID` while the conversation rebinds to `post.ID`, so every event the fresh child produces for turn #2 is dropped at the drain's active-session gate with `logger.Debug("relay: stream-turn drop; not active session", …)` (`cmd/pyry/stream_turn_drain.go:229`). That drop is the divergence this test's own header documents at `:62-72` — the reason M4 asserts stdin rather than a phone-side delta. So on any run where M4 goes green, turn #2's echo must produce at least one such record. The bounded poll exists because the drop is asynchronous (child stdout → parser → sink → drain goroutine) and M4's poll exits on the stdin log, which the child writes to *before* it emits its echo.

**Why a guard that only runs on green is the right shape.** The instrument matters on a red M4, and the guard never runs there. That is the point: it is a regression guard, not a diagnostic. Every green CI run keeps the flip honest so the rare red run's record can be trusted. Say this in the comment — otherwise the placement reads as an oversight.

The failure message must name both readings, because they are different defects: either the harness's `-pyry-verbose` no longer reaches the level (`harness.go`, or `cmd/pyry/main.go`'s flag), or the drain stopped dropping post-rotation events (which would mean the header's divergence analysis is stale). In both cases M4's `pending/holds` count silently degrades from a measurement to a suppression, which is exactly the ambiguity this ticket closes.

#### 2e. The "how to read that window" prose

Replace the paragraph at `:467-476`. It must carry four things:

1. **The pair's mapping.** `retry Warns > 0` ⟹ the error-return arm: `DeliverFunc` returned a non-nil error the `Pending` classifier did not match, the head stayed queued and was re-attempted every `defaultRetryInterval` (1 s); no give-up is possible inside this 20 s window (`defaultGiveUpAfter` is 2 m). `pending/holds > 0` ⟹ the hold arm: delivery declined with a `Pending`-classified error, the give-up streak reset, same head retried at the same 1 s cadence. Both non-zero ⟹ the drain moved between arms inside the window.
2. **The unreachability of the hold arm on this harness**, with the four-step chain from § Context compressed to one sentence plus the two file references (`cmd/pyry/main.go:932`, `internal/streamsup/runner.go:283`). So `pending/holds: 0` is the expected reading, and a non-zero one is a finding about the delivery path, not about the stall.
3. **No longer assert the hold state is invisible** (AC-4). It is visible at `Debug`; the harness now starts pyry *with* `-pyry-verbose`, so the existing parenthetical "(this harness starts pyry without `-pyry-verbose`)" is false and must go.
4. **The residue (AC-5).** Both counts at `0` narrows the field to exactly two states, and *no log level makes either visible* — they emit nothing at any level: parked in `Activate`, bounded by `inboundActivateTimeout` (30 s), which `newInboundDeliver` (`cmd/pyry/main.go:1631-1633`) does not log around; and parked in the mid-turn hold, bounded by `streamTurnHoldTimeout` (15 m), whose whole chain (`waitIdleForDelivery` → `WaitIdle`, `cmd/pyry/stream_turn_busy.go:389`, `:332`) is silent. The record must state this rather than let a reader infer a decision from a narrowed field.

Check every number against the constants row of the reading list before committing. #1296's replacement prose shipped wrong on two of three numbers and this ticket exists partly to clean up after that.

### AC → change map

| AC | Change |
|---|---|
| 1 — daemon logs at `Debug`, proven by an observed `Debug` line | § 1 (`harness.go`) + § 2d (the guard) |
| 2 — header reports the pending/hold count beside the retry-Warn count, window-scoped | § 2a + § 2b |
| 3 — non-vacuous, provable offline in `TestDaemonLogWindow` | § Testing strategy, cases 1–3 |
| 4 — prose maps the pair; no longer asserts the hold state is invisible | § 2e, items 1–3 |
| 5 — prose states what stays undecidable even at `Debug` | § 2e, item 4 |

## Concurrency model

Nothing new is spawned, and no synchronisation is added.

- `daemonLogWindow` stays pure over `(snapshot, since, budget)` — no `*testing.T`, no I/O, no clock. That purity is load-bearing at the M4 call site: the renderer runs while `t.Fatalf`'s arguments are being built, where a fatal-firing helper would pre-empt the message. Adding a second `bytes.Count` does not touch it. **Do not** reach for `h.Stderr` inside the renderer to self-report the level — that would couple a pure function to live state and break the property the file spends a paragraph defending.
- `h.Stderr` is a `safeBuffer` written by `os/exec`'s stderr-copy goroutine and read from the test goroutine; the AC-1 guard's poll uses `h.Stderr.Bytes()`, the same accessor M4 already uses at `:400` and `:480`. No new race surface.
- The window boundary stays approximate for the reason `:396-399` already gives (the copy goroutine is asynchronous). At `Debug` the window is chattier but the cadence being measured is still 1 Hz against 20 s, so the boundary's slack cannot change any reading. Leave that paragraph as it is.

## Error handling

- **Marker drift** (the `msgqueue` message changes upstream): the count reads `0` beside an excerpt that visibly contains the lines. Same one-look discrepancy `msgqueueRetryWarn` accepts; no code-level guard, deliberately.
- **The level flip breaks**: caught by the § 2d guard on the next green run.
- **A `Debug`-chatty window exceeds the budget**: the existing elision arm keeps head and tail and reports the window's TOTAL size, not the kept size. Already correct; the doc correction in § 2c is what makes that the stated fallback.
- **The sentinel arms**: unchanged. `daemonLogWindow` still never returns `""`, and neither count may appear on the `<unmeasurable:` or `<empty:` arms — pinned by the extended `notWant` lists.

## Testing strategy

Extend `TestDaemonLogWindow` (`:581`). Bullet-pointed scenarios; write them in the file's existing table idiom (substring `want` / `notWant`, computed numbers, no whole-output golden).

1. **A window carrying the marker reports a non-zero count** — snapshot with two `msgqueueHoldDebug` lines and no retry Warns, `since: 0`; want `pending/holds: 2` and `retry Warns: 0`. (AC-3, the non-zero half.)
2. **A window without it reports `0`** — extend the existing "window under budget renders verbatim" case (`:625`) with `pending/holds: 0` in `want`. Its snapshot already carries a retry Warn and no hold line, so it pins both counts against one input. (AC-3, the zero half.)
3. **The pending/hold count is scoped to the window, not the whole buffer** — mirror the existing retry-Warn scoping case (`:652`): three marker lines with `since` past the first; want `pending/holds: 2`. Do not fold this into case 1 — the boundary is a separate property from the count.
4. **Neither count leaks past a sentinel arm** — add `"pending/holds"` to the `notWant` lists on the `<empty:` (`:606`) and both `<unmeasurable:` (`:614`, `:622`) cases, beside the `"retry Warns"` entry already there.
5. **A window with both markers reports both** — one retry Warn and one hold line; want `retry Warns: 1` and `pending/holds: 1`. Pins that the two counts are independent rather than one matching the other's substring.
6. **Existing cases stay green unedited** — in particular the over-budget case's `budget: 74` keep-arithmetic is unaffected by a longer header; verify rather than assume, since the header is prepended to the same string the assertions slice.

Run: `go test -tags e2e -race -count=1 -run 'TestDaemonLogWindow' ./internal/e2e/...` for the offline arms, then `make e2e` for the live pass. Report M4's outcome honestly — a red `TestRelayV2_StreamNewSessionRotatesAndRestartsFresh` is the correct, expected state until #1298 lands, and its record should now render the count pair. If it comes up green, the AC-1 guard is what proves the flip; capture one `level=DEBUG` line in the PR body as AC-1's observation.

## Open questions

1. **Does the live `Debug` window fit in 8 KB?** Unmeasured — no `Debug` capture of this window exists yet. The design deliberately does not pre-emptively raise the budget. If the developer's live run renders elided, say so in the PR; moving the constant is then an evidence-backed follow-up, and the doc already licenses it ("a tuning knob, not a contract").
2. **Should the other five stream specs surface their own daemon-log window?** Out of scope. They now capture `Debug` output but none renders it. If a second spec grows a dead-window record, `daemonLogWindow` is the obvious thing to lift out of this file — not before.
3. **Nothing in this repo pins that `-pyry-verbose` still maps to `LevelDebug`.** The § 2d guard catches it indirectly and only through the e2e tag. A unit-level pin in `cmd/pyry` would be the direct fix; deferred as an unobserved failure mode.

## Security review

**Verdict:** PASS

The design's one security-relevant move is that a `Debug`-level daemon log now enters `h.Stderr`, and M4 renders a bounded window of it into a `t.Fatalf` that lands in CI output. The review below is centred on that.

**Findings:**

- **[Error messages, logs, telemetry]** No findings — the newly-exposed record class is content-free by existing, enforced design. Every `Debug` site reachable from this harness was read: `stream_turn.not_active` and `stream_turn.busy_unresolved`/`clear_unresolved` carry a discriminant plus a session id under explicit "SECURITY: content-free" notes (`cmd/pyry/stream_turn_drain.go:228`, `stream_turn_busy.go:189`, `:260-263`); the three `*.marshal_err` drops deliberately withhold both the payload and `err.Error()`, each with a comment saying why (`interactive_turn_v2.go:361-363`, `queue_state_v2.go:128-133`, `session_transition_v2.go:145-147`); `streamsup: unrecognized payload` logs site, type and byte count only (`internal/streamsup/parser.go:287-295`); and the `msgqueue` hold line itself is `conversation_id` + `queued_msg_id` + `queued_at` with a standing "NEVER log head.text (untrusted phone content)" rule (`internal/msgqueue/queue.go:584-592`). No phone-supplied text reaches any of them.
- **[Error messages, logs, telemetry — residual]** SHOULD FIX, already satisfied by the design as written: `Debug` records carry `session_id` and `conversation_id` more often than `Info` records do, and `session_transition_v2.go:38-47` classes a conversation id as a routing key treated as sensitive. In this harness both ids are synthetic constants declared in the test file (`11111111-…`, `22222222-…`) under an isolated `shortHome(t)`, so no real identifier can reach the record. Code-review should confirm the developer did not add a *new* logging site; this ticket adds none.
- **[Trust boundaries]** No findings — no boundary moves. The one new datum crossing into the record is `bytes.Count` over the daemon's own log, i.e. an integer derived from bytes the harness already captured and already renders. The renderer stays pure over `(snapshot, since, budget)`; nothing downstream holds anything it did not hold before.
- **[Subprocess / external command execution]** Named and dismissed on the specifics: the one argv change is the fixed literal `-pyry-verbose`, appended to `extraFlags` before `spawnWith`'s `--` separator (`internal/e2e/harness.go:639-655`), so it is parsed by pyry and never forwarded to `claude`. No caller-controlled value enters the argv, no `sh -c`, and the child env is untouched. `-pyry-verbose` is recognised in `pyryFlagBools` (`cmd/pyry/main.go:252`) and its only effect is the handler level (`:735-746`) — no behavioural branch keys off it, so no code path changes shape under test.
- **[Network & I/O — resource exhaustion]** Stated non-finding: `safeBuffer` is unbounded in memory and now accumulates `Debug` records for the life of each of six e2e tests. The buffer was already unbounded at `Info`; the daemon is quiet during these specs, they run for tens of seconds, and `daemonLogBudget` bounds what is *rendered*, which is the part that reaches CI output. No growth problem has been observed, so a cap here would be a defence against an unobserved failure. Worth a glance at code-review if a stream spec's memory use visibly changes.
- **[File operations]** Not applicable, and the reason is structural rather than incidental: this ticket creates, opens and writes no file. The only paths in play (`stdinLog`, `regPath`, `config.json`) are pre-existing and untouched.
- **[Tokens, secrets, credentials]** Not applicable — no token, key or credential is created, stored, compared or logged. The pairing token in this test (`payloadA.Token`) is generated and consumed by the existing flow at `:104-143` and never reaches the daemon's log; no `Debug` site on the reachable set logs a token, a header or a static key. Confirmed by reading the field list of every reachable `Debug` call, not by grepping for the word.
- **[Cryptographic primitives]** Not applicable — no randomness, no comparison, no primitive selection. The Noise handshake and its `CipherState` sequencing are untouched; the guard's added poll reads a byte buffer and performs no receive, so the receive-nonce ordering the file depends on (`:158-185`) is unaffected.
- **[Concurrency]** No findings — no goroutine is spawned, no lock is taken, no shared state is added. The guard's poll uses the same `h.Stderr.Bytes()` accessor the test already calls twice; `daemonLogWindow` stays pure, preserving the property that keeps it callable inside `t.Fatalf`'s argument list.
- **[Threat model alignment]** The relevant surface in `docs/protocol-mobile.md` § Security model is the untrusted-phone-content boundary, and it is not crossed: the record's new bytes are daemon-authored log lines whose content discipline is already reviewed and pinned upstream, and the existing `log=%q` argument at `:481` — which prints the *entire* stdin log including the user turn text, unbounded — remains the larger and less disciplined half of the same message. This ticket adds the smaller, bounded, content-free half. Tightening `log=%q` is out of scope and belongs to whichever ticket next revisits M4's record shape.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-05
