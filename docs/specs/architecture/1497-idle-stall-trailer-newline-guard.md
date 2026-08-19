# #1497 — Newline-guard the synthetic `idle_stall` trailer

**Size:** XS (~22 production lines across two files; the rest is tests)
**Package:** `internal/agentrun/streamrunner`

## Files to read first

- `internal/agentrun/streamrunner/watchdog.go` → `streamParser` — the struct doc block enumerates the tracked state (`lastEvent`, `awaiting`, `sawResult`); this ticket adds a fourth field and the doc block must grow with it.
- `internal/agentrun/streamrunner/watchdog.go` → `Write` — forwards to `p.dst` FIRST, then feeds `b[:n]`. The `n` slice, not `b`, is what actually reached stdout; the guard keys off it.
- `internal/agentrun/streamrunner/watchdog.go` → `feed` — where the partial remainder is kept, and where it is dropped to `nil` past `maxBuf`. Extract: the drop happens *after* the bytes were forwarded, which is why `len(p.buf)` is not a sound signal.
- `internal/agentrun/streamrunner/watchdog.go` → `hasSeenResult` — the house shape for a mutex-guarded bool accessor (field `sawResult`, method `hasSeenResult`). The new accessor mirrors it exactly.
- `internal/agentrun/streamrunner/watchdog.go` → `writeIdleStallResult` — already appends its own trailing `'\n'`; only the leading separator is missing. This function's signature changes.
- `internal/agentrun/streamrunner/runner.go` → `Run` — the `wd.hasFired()` / `!parser.hasSeenResult()` block is the single call site. Extract also: the comment above it establishing that `cmd.Wait()` has returned, so `Run` is the sole writer to `cfg.Stdout` here.
- `internal/agentrun/streamrunner/watchdog_test.go` → `TestRun_IdleStall_AfterToolResult` — the test tightened for AC#2. Currently asserts with `strings.Contains`, which cannot see a stray blank line.
- `internal/agentrun/streamrunner/watchdog_test.go` → `TestStreamParser_OversizedLine` — the shape the new parser-level test copies (construct the parser directly, shrink `maxBuf`, feed an oversized blob). Stays green unmodified.
- `internal/agentrun/streamrunner/helper_test.go` → `TestStreamRunnerHelperProcess`, `blockUntilSigterm` — the fake-claude switch and the SIGTERM-handling block used by every stall mode. The new mode is one more `case`.
- `internal/agentrun/streamrunner/runner_test.go` → `helperRunCfg` — how a Run-level test wires `Config` to the helper process.
- `docs/knowledge/features/streamrunner-package.md` § "Fake claude helper modes" — the mode list the documentation phase will extend; read for the house description style, do not edit.

## Context

When the idle-stall watchdog kills claude, `Run` synthesises the `result` trailer itself. That trailer is the *only* signal the run produced anything: `Run` returns nil on this path by contract, and the dispatcher classifies `subtype: error_idle_stall` / `terminal_reason: idle_stall` as a transient runner-side error it auto-retries (unlike `max_turns` / `timeout`, which it deliberately never retries).

`writeIdleStallResult` appends its own trailing newline but no leading one. `Write` forwards every byte to `cfg.Stdout` *before* parsing, so bytes claude wrote without a closing newline are already on stdout when the trailer is composed. The trailer then lands on the *same line* as the partial, and the resulting line parses as nothing at all:

```
{"type":"assistant","message":{"role":"assistant","content":[{"type":"te{"type":"result","subtype":"error_idle_stall",...}
```

The dispatcher sees no result trailer, cannot apply the retry, and pyry exits 0 with no classifiable outcome. This is the failure the ticket measured at `main` 2e33ffe.

The state is reachable precisely where the watchdog fires: a connection wedged mid-chunk, or claude SIGKILLed by `WaitDelay` mid-write of a pipe write past the 4 KiB atomicity limit during the grace window. A partial also does not push the fire out — `lastEvent` advances only in `consumeLine`, which runs per *complete* line, so the idle clock keeps running across an unterminated write.

Two neighbouring designs are wrong, and the current suite catches neither:

- **Unconditional leading newline** — keeps the whole package green today, because nothing pins the terminated path against a stray blank line.
- **Guard keyed on `len(p.buf) > 0`** — wrong for a partial larger than `maxBuf`: `feed` drops such a remainder to `nil` *after* forwarding the bytes. `TestStreamParser_OversizedLine` already asserts the accumulator is empty in exactly that case.

The guard must therefore be keyed on **whether the last byte forwarded to stdout was a newline**, which is a property of the passthrough, not of the parse buffer.

## Design

Three changes, all inside `internal/agentrun/streamrunner`.

### 1. `streamParser` tracks whether a line is open

Add a fourth tracked field alongside `lastEvent` / `awaiting` / `sawResult`:

```go
lineOpen bool // true when the last byte forwarded to dst was not '\n'
```

Extend the `streamParser` struct doc comment's "Tracked state" list with an entry for it, matching the existing entries' tone: it records a property of the *passthrough*, not of the parse — deliberately independent of `buf`, which `feed` may drop.

Zero value `false` is correct for a fresh parser: nothing has been forwarded, so there is no open line and the trailer needs no separator. `newStreamParser` needs no change.

### 2. `feed` sets it from the last forwarded byte

Inside the existing critical section, before or after the line loop (order is irrelevant — the two derivations are independent):

```go
if len(b) > 0 {
    p.lineOpen = b[len(b)-1] != '\n'
}
```

The `len(b) > 0` guard is load-bearing for the semantics, not just for the index: an empty feed must leave the flag unchanged, not reset it. Add a short comment stating that this is derived from the bytes that *reached* `dst` (the caller passes `b[:n]`), which is why it stays correct across a short write and across a `maxBuf` drop.

### 3. `writeIdleStallResult` takes the separator decision as a parameter

```go
func writeIdleStallResult(w io.Writer, idle time.Duration, runStart time.Time, leadNewline bool) error
```

When `leadNewline` is true, the marshalled trailer is prefixed with a single `'\n'` before the existing trailing `'\n'` is appended — one `w.Write`, one error path, no new logging branch. Extend the function's doc comment with one sentence: the leading newline exists so the trailer starts its own line when claude's stream ended mid-line, and is suppressed otherwise so a newline-terminated stream gains no blank line.

Add the mutex-guarded accessor mirroring `hasSeenResult`:

```go
// hasOpenLine reports whether the last byte forwarded to stdout was not a
// newline — i.e. an unterminated partial line is already on stdout.
func (p *streamParser) hasOpenLine() bool
```

`Run`'s block becomes a one-argument change; no new statements:

```go
if err := writeIdleStallResult(cfg.Stdout, idle, runStart, parser.hasOpenLine()); err != nil {
```

Binding the separator to the trailer write (rather than writing it separately in `Run`) is deliberate: it makes it structurally impossible to emit a stray newline on the `hasSeenResult()` path, where no trailer is written and no separator is wanted.

### Data flow

```
claude stdout ──► streamParser.Write
                    │
                    ├─► p.dst.Write(b)  ──► cfg.Stdout      (verbatim, unchanged)
                    │        returns n
                    └─► p.feed(b[:n])
                             ├─ consume complete lines → lastEvent / awaiting / sawResult
                             └─ lineOpen = last byte of b[:n] != '\n'

watchdog fires ──► cancelChild ──► SIGTERM ──► cmd.Wait() returns
                                                   │
Run (sole writer now) ─► writeIdleStallResult(cfg.Stdout, …, parser.hasOpenLine())
                              └─► ["\n"] + trailer JSON + "\n"
```

## Concurrency model

No new goroutines. `lineOpen` is written in `feed` under `p.mu` (already held) and read in `hasOpenLine` under `p.mu`, matching `sawResult` / `hasSeenResult`.

At the read site there is in fact no concurrent writer — `cmd.Wait()` has returned, so the stdlib's stdout-forwarding goroutine is done, and `wd.wait()` has joined the watchdog. The mutex is taken anyway for consistency with the other accessors and so `-race` sees a clean happens-before regardless of how the parser is exercised from tests.

## Error handling

- The separator rides inside `writeIdleStallResult`'s single `w.Write`, so its failure mode is the existing one: `Run` logs `streamrunner: write idle_stall result failed` at Warn and returns nil. No new failure mode, no new log line.
- A short write in `Write` (`n < len(b)` with an error) leaves `lineOpen` describing exactly the bytes that reached `dst`, because `feed` receives `b[:n]`. This is the reason the guard is keyed on the forwarded prefix rather than on the argument.
- An oversized partial dropped by `feed` still leaves `lineOpen` true — the bytes were forwarded before the drop. This is the AC#3 property.

## Testing strategy

All four criteria land in the existing test files; no new files.

**New fake-claude mode `stall_partial_line`** (`helper_test.go`): write one unterminated line to stdout — no newline — then drain stdin to EOF, then `blockUntilSigterm()`. Nothing precedes the partial, so the parser stays in its initial `awaiting` state and the watchdog fires; a partial does not advance `lastEvent`. Document the mode in the switch's doc comment alongside the other `stall_*` modes. Hold the partial's bytes in a package-level test constant so the assertions compare against the exact bytes the helper wrote, not a re-typed copy.

**AC#1 — `TestRun_IdleStall_PartialLine_NewlineGuarded`** (`watchdog_test.go`), a Run-level test on the new mode with a sub-second `IdleTimeout`:

- `Run` returns nil.
- `stdout` begins with the partial constant verbatim and unmodified.
- The remainder after the partial is exactly one `'\n'` — assert the byte at the split point is `'\n'` and the one after it is not.
- What follows is a single line (no interior newline once the sole trailing newline is trimmed) that `json.Unmarshal`s into a struct with `terminal_reason` equal to `idle_stall`.
- Do **not** assert on the trailer's human-readable `result` string: a sub-second `IdleTimeout` renders as `for 0s` there (integer-seconds truncation).

Fails on `main` today: the remainder starts with `{`, and the unmarshal fails with `invalid character 't' after object key:value pair`.

**AC#2 — tighten `TestRun_IdleStall_AfterToolResult`** so it fails if the newline is written unconditionally. Replace the two `strings.Contains` checks with a line-level assertion over `stdout`: trim exactly one trailing newline, split on `'\n'`, and assert

- exactly four elements,
- no element is empty,
- element 2 is the tool-result `user` line (`"type":"user"`),
- element 3 `json.Unmarshal`s with `terminal_reason` `idle_stall`.

Under the unconditional-newline variant this yields five elements, one of them empty — the assertion fails twice over. This is the only test in the package modified for this ticket, which AC#4 explicitly sanctions.

**AC#3 — `TestStreamParser_OversizedPartial_LineStaysOpen`** (`watchdog_test.go`), white-box, in the shape of `TestStreamParser_OversizedLine` — construct the parser directly, shrink `maxBuf`, and assert:

- a fresh parser reports no open line;
- after feeding a newline-less blob longer than `maxBuf`, `hasOpenLine()` is true **and** `len(p.buf)` is zero (read under `p.mu`). The pair is the discriminator: the buf-keyed variant reports closed on exactly this input;
- after a subsequent newline-terminated write, `hasOpenLine()` is false again.

**AC#4 — regression surface.** `Write` is untouched, so passthrough is untouched. `TestRun_CleanExit_NoSyntheticResult_ByteExact`, `TestStreamParser_PassthroughByteExact` and `TestStreamParser_OversizedLine` must stay green with no edits. `TestRun_IdleStall_NoEvents` also stays unmodified and is a live check of the degenerate case: the helper writes nothing, so `lineOpen` is false and stdout is exactly the trailer line with no leading blank.

Gate: `make check` (the package runs under `-race`).

## Open questions

- **Helper-startup timing on the new mode.** The test relies on the helper writing its partial before the watchdog's SIGTERM lands (~`IdleTimeout` + one tick after `Run` starts). This flake shape is not new — `TestRun_IdleStall_AfterToolResult` already races helper startup against a 200 ms threshold and has been stable. Give the new test a slightly roomier threshold (≈300 ms) and have the helper write the partial *before* draining stdin, so the write does not wait on the parent's envelope round-trip. If it proves flaky in practice, raising the threshold is the fix, not restructuring the fixture.
- **`internal/streamsup` has a parallel watchdog** with the same trailer-synthesis shape. Whether it has the same defect is out of scope here; it needs its own measurement and its own ticket rather than a speculative parallel edit.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings, with one design constraint made explicit above. The boundary is claude's stdout crossing into pyry's parent state, and it stays where it already is: `streamParser.Write`. The new state is one bool derived from a single byte's *identity* (`== '\n'`), never from event content, so the package doc's promise that it inspects only the structural `type` field is preserved. The constraint that makes it sound: the byte must be taken from the forwarded prefix `b[:n]`, which is what `Write` passes to `feed` — deriving it from the caller's full `b` would mis-report "closed" when a short write truncates before a trailing newline the child sent but pyry never emitted. This is stated in § Design and § Error handling.
- **[Threat model — output-signal integrity]** This ticket *closes* a child-controlled signal-suppression hole rather than opening one. Before the fix, a wedged or hostile child could erase pyry's own out-of-band retry signal simply by ending its stream mid-line: its bytes spliced into the front of pyry's synthetic trailer and rendered the whole line unparseable, so the dispatcher saw no `idle_stall` and pyry exited 0. After the fix the trailer always begins a line of its own, and the child's bytes can only ever occupy the line before it. No new influence is granted: the child's only reachable effect on the new code path is whether one `'\n'` is emitted.
- **[Trust boundaries — trailer forgery]** OUT OF SCOPE, pre-existing. A child that controls stdout can already write a line that *looks* like a `result` trailer, and that line is forwarded verbatim by design — the dispatcher treats the runner's stdout as the run's output. This ticket neither widens nor narrows that, and notably cannot produce a duplicate trailer: when the child emitted its own `result`, `hasSeenResult()` is true, no synthetic trailer is written, and therefore no separator either.
- **[Network & I/O — resource exhaustion]** No findings. The new state is a single bool; no bytes are retained. This is deliberate and load-bearing: implementing the guard by holding on to the partial line would defeat the existing unbounded-buffer defence in `feed`, which drops a remainder past `maxBuf` precisely so a pathological newline-less stream cannot grow memory without bound. `maxBuf` and the drop are unchanged, and `TestStreamParser_OversizedLine` continues to pin them.
- **[Error messages, logs, telemetry]** No findings. The design adds no log call and no error string. The partial's bytes, its last byte, and the flag's value are never logged — the one existing Warn on this path (`write idle_stall result failed`) carries only `err`, which originates from the writer, not from stream content.
- **[Concurrency]** No findings. One lock, unchanged, no new lock and therefore no ordering question. `lineOpen` is written in `feed` and read in `hasOpenLine`, both under `p.mu`, mirroring `sawResult` / `hasSeenResult`. No goroutine is spawned, so no lifecycle or leak question arises. The read in `Run` is additionally sequenced after `cmd.Wait()` and `wd.wait()`, so no writer can be live; the mutex is taken regardless.
- **[Subprocess / external command execution]** No findings. Nothing in the spawn path changes: no argv change, no environment change, no change to `cmd.Cancel`, the descendant reap, or `WaitDelay`. The new fake-claude mode is a test fixture re-exec'ing the test binary through the existing `helperRunCfg` wiring, adding no new environment variable.
- **[Tokens, secrets, credentials]** Not applicable — no credential, token, or key is created, read, stored, or compared anywhere on this path.
- **[File operations]** Not applicable — the design opens, creates, and stats no file. `cfg.Stdout` is an `io.Writer` supplied by the caller; no path is constructed.
- **[Cryptographic primitives]** Not applicable — no randomness, hashing, key derivation, or secret comparison is involved; the only comparison introduced is a byte against the constant `'\n'`.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
