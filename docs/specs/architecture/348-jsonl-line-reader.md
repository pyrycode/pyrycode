# 348 — `agent-run/jsonl`: line reader + deterministic end-of-turn detector

## Files to read first

- `internal/agentrun/trust.go:1-21` — package doc header and import block. The new subpackage is `internal/agentrun/jsonl/` — a sibling subdirectory, NOT a new top-level package. Mirror the "no logging of file contents at any layer" trust-boundary stance verbatim in the new package's doc comment.
- `internal/agentrun/drive.go:14-47` — `DriveConfig` shape: zero-value-defaults pattern (`if cfg.X == 0 { cfg.X = default }`) applied in the constructor body. Mirror.
- `internal/sessions/rotation/watcher.go:78-115` — `New(cfg Config) (*Watcher, error)` constructor shape: required-field validation, logger defaulting, struct return. The reader's constructor follows the same shape but `*Reader, error` is not warranted (no fallible setup); use `*Reader` directly. Reference for the Config-struct pattern only.
- `docs/lessons.md:52` — "Don't trust ticket bodies on filesystem layout — observe." Already applied by sibling #347. Cited here only to anchor the rule that the dashed-path concern is upstream of this ticket; this reader does not touch paths at all.
- `CODING-STYLE.md:46-86` — interface/testing conventions: small interfaces, `t.Parallel()`, table-driven, stdlib `testing` only, same-package tests.
- Real fixture files (NOT inside the worktree until copied into `testdata/`):
  - `~/.claude/projects/-Users-juhanailmoniemi-Workspace-Projects--pyrycode-worktrees-architect-15/6fc6d062-1972-4457-9bfd-6b47c7e77e11.jsonl` — clean single `end_turn`, 64 lines, 25 assistant entries. Use as `testdata/clean.jsonl`.
  - `~/.claude/projects/-Users-juhanailmoniemi-Workspace-Projects--pyrycode-worktrees-architect-83/054ce738-371c-4dac-81c7-b4f9993df20f.jsonl` — double `end_turn` (first transitional with 0 text, second with text), 18 lines, 6 assistant entries. Use as `testdata/double_end_turn.jsonl`. The exact file the ticket cites.
  - `~/.claude/projects/-Users-juhanailmoniemi-Workspace-Projects--pyrycode-worktrees-code-review-161/08ad9c51-b394-4720-9f4c-a16ea130834e.jsonl` — no `end_turn` (max_turns / interrupted), 53 lines, 34 assistant entries. Use as `testdata/no_end_turn.jsonl`. The exact file the ticket cites.

## Context

`pyry agent-run` (#338, already shipped) drives a single claude turn and exits. The next layer — sibling #349's fsnotify watcher — needs to react to claude's session JSONL output to detect end-of-turn and enforce a max-turn budget. Today there is no JSONL parser in the codebase.

This ticket is the **pure** reader. It owns three concerns and only three:

1. **Line framing.** Buffer partial-line bytes across reads so a JSONL line being mid-written never parses until its trailing `\n` arrives.
2. **Assistant-entry parsing.** Decode the minimum subset of each line needed to extract `type`, `message.stop_reason`, and the per-content text-character total. Skip non-assistant entries silently.
3. **End-of-turn detection + turn counting.** Apply the rule the Phase A spike (#329) discovered: signal fires iff `stop_reason == "end_turn"` AND `sum(len(content[i].text)) > 0`. Count every assistant entry (including transitional empty-content `end_turn` entries) toward the turn count.

What this reader explicitly does NOT do:

- Open the JSONL file. The consumer passes an `io.Reader` (typically an already-`Seek`-ed `*os.File`).
- Compute the watch directory (sibling #347's `EncodeProjectDir` does that).
- Drive any goroutine, fsnotify watcher, or filesystem polling loop (sibling #349 does that).
- Block on input. The reader returns `io.EOF` when its source is drained and waits for the caller to call `Next` again. The caller's fsnotify event drives the next call.

## Design

New subpackage at `internal/agentrun/jsonl/`. Single production file `reader.go` plus tests.

### Exported surface

```go
package jsonl

// Event is the parsed shape of a single assistant JSONL entry. Non-assistant
// entries are silently skipped by the Reader and never surface as Events.
type Event struct {
    // StopReason mirrors message.stop_reason verbatim ("end_turn",
    // "tool_use", "max_tokens", "stop_sequence", ""). Empty string when the
    // entry has no stop_reason field — a real and legitimate state for an
    // assistant entry mid-tool-call.
    StopReason string

    // TextChars is sum(len(content[i].text)) over every content block on the
    // entry. Content blocks without a "text" field (e.g. "thinking",
    // "tool_use") contribute 0 naturally.
    TextChars int

    // EndOfTurn is true iff StopReason == "end_turn" AND TextChars > 0.
    // This is the deterministic end-of-turn signal — fire once per Event
    // where this is true. Empty-content end_turn entries (transitional
    // thinking-block resolutions) have EndOfTurn == false even though their
    // StopReason is "end_turn".
    EndOfTurn bool
}

// Reader parses claude session JSONL output from an io.Reader, surfacing
// one assistant Event per call to Next.
//
// Not safe for concurrent use. Construct one Reader per source.
type Reader struct {
    // unexported fields: src, buf (pending partial-line bytes), offset,
    // assistantCount, log.
}

// Config configures Reader. Logger optional (defaults to slog.Default).
// StartOffset is informational: callers must Seek src to that position
// before constructing; the Reader uses StartOffset only to make Offset()
// report absolute file positions for resume.
type Config struct {
    Logger      *slog.Logger
    StartOffset int64
}

// NewReader returns a Reader that consumes src. Mirrors bufio.NewReader's
// naming. Does not read from src until the first Next call.
func NewReader(src io.Reader, cfg Config) *Reader

// Next returns the next assistant Event from src, advancing internal state.
//
// Returns io.EOF when src has signalled io.EOF AND no complete line is
// pending in the internal buffer. Partial bytes (a line without a trailing
// '\n') are retained across calls; the next call continues from where the
// previous left off, optionally after the underlying io.Reader has produced
// more bytes (the typical fsnotify-driven case).
//
// Returns any non-EOF read error from src wrapped as
// "jsonl: read at offset %d: %w". Malformed-JSON lines are logged at Warn
// and skipped — they do NOT terminate iteration and do NOT advance the
// assistant counter. (Claude is the source of truth; the reader is
// best-effort. A single broken line must not poison the stream.)
func (r *Reader) Next() (Event, error)

// Offset returns the byte position of the next not-yet-consumed line —
// safe to persist as the resume point. Equals Config.StartOffset before
// the first Next call. After every successful Next (and after every
// silently-skipped non-assistant line), advances past the consumed line's
// trailing '\n'. Does NOT advance into a partial-line buffer.
func (r *Reader) Offset() int64

// AssistantCount returns the number of assistant entries consumed so far,
// including transitional empty-content end_turn entries. Sibling #349 uses
// this for max-turn enforcement.
func (r *Reader) AssistantCount() int
```

That is the entire surface: one type, one struct, one constructor, three methods. No interface defined here — interfaces are defined where consumed (sibling #349 will introduce one if it needs to mock the reader; this package does not pre-define it).

### Internal line shape

The reader uses a single private struct for `json.Unmarshal` per line:

```go
type rawLine struct {
    Type    string `json:"type"`
    Message struct {
        StopReason string `json:"stop_reason"`
        Content    []struct {
            Type string `json:"type"` // unused by the rule but kept for clarity
            Text string `json:"text"`
        } `json:"content"`
    } `json:"message"`
}
```

`json.Unmarshal` ignores unknown fields by default — the reader doesn't see `parentUuid`, `requestId`, `timestamp`, `cwd`, etc. and that is correct; those fields exist on every line in real fixtures but are irrelevant to this reader's job.

`TextChars` is computed as `sum(len(c.Text) for c in raw.Message.Content)`. Content blocks of `type == "thinking"` have `text` absent → `c.Text == ""` → contributes 0 naturally. No explicit type-filter needed, and the math matches the ticket's literal `sum(len(content[i].text))` wording.

### Line framing algorithm

`Next` runs a single state-machine loop:

1. Search `r.buf` for the next `'\n'`. If found:
   - Slice off the line (without the `'\n'`). Advance `r.offset` past the line + `'\n'`.
   - Decode with `json.Unmarshal`. On parse error: log Warn with offset + first ~120 bytes redacted (see § Error handling), skip — DO NOT advance `assistantCount`, DO NOT return. Continue loop.
   - On `raw.Type != "assistant"`: skip silently. Continue loop.
   - On `raw.Type == "assistant"`: increment `assistantCount`, compute `TextChars`, compute `EndOfTurn`, return the Event.
2. If no `'\n'` in `r.buf`: call `r.src.Read` into a fixed scratch buffer (e.g. 4 KiB). Append the returned bytes to `r.buf`.
   - If `Read` returned `(n>0, nil)` or `(n>0, io.EOF)`: continue loop (we may now have a complete line).
   - If `Read` returned `(0, io.EOF)`: return `io.EOF`. The caller resumes later by calling `Next` again — `io.Reader.Read` may return new bytes on the next call (this is the standard tail-`-f`-on-`*os.File` contract).
   - If `Read` returned a non-EOF error: return it wrapped.

The internal `buf` is a `[]byte` slice that grows as partial lines come in and shrinks (via reslice) as complete lines are consumed. Cap any single buffered partial line at e.g. 16 MiB to prevent unbounded growth from a pathological writer — if exceeded, log Error and return a `*LineTooLargeError` (single sentinel error type sufficient; the consumer will surface as a hard failure since the JSONL stream is now structurally broken). Real claude lines max out at ~80 KiB in observed fixtures; 16 MiB is well above that.

### Why not `bufio.Scanner` or `bufio.Reader.ReadBytes('\n')`?

`bufio.Scanner` is tempting but has a default 64 KiB token cap (the largest real line in the surveyed fixtures is ~80 KiB; bumping the cap is required and easy to forget). More importantly, `Scanner` does not expose a "drained, no complete token yet, more may come" state distinguishable from "stream ended" — both surface as `Scan() returning false`. The reader needs to distinguish "wait for fsnotify" (drained mid-line) from a fatal read error.

`bufio.Reader.ReadBytes('\n')` is closer but on `io.EOF` mid-line returns the partial bytes + `io.EOF`. Re-feeding those bytes into the reader on the next call requires either a custom `io.MultiReader` dance or the reader maintains its own buffer anyway. The hand-rolled buffer is ~15 lines and removes the dance entirely.

### `bufio` is not banned — it's just the wrong shape here

If a future change makes the reader take a `*bufio.Reader` from the consumer (so the consumer owns buffering for other purposes too), the line-framing logic shrinks. Today there's no such consumer; build the simplest thing.

## Concurrency model

None. `Reader` is single-goroutine. The fsnotify driver (sibling #349) will own its own goroutine and call `Next` synchronously on the fsnotify event loop. Document on the type: "Not safe for concurrent use. Construct one Reader per source."

No `context.Context` parameter on `Next`. Cancellation is structural: the consumer closes the underlying `*os.File`, the next `Read` returns an error, `Next` surfaces it. The fsnotify driver owns the context.

## Error handling

Three error classes:

| Class | Surface |
|---|---|
| Source drained, partial bytes may be buffered, more may arrive | `io.EOF` from `Next` — sentinel, no wrapping. Use `errors.Is(err, io.EOF)`. |
| Source `Read` failed (file closed, IO error) | Wrapped: `fmt.Errorf("jsonl: read at offset %d: %w", r.offset, err)`. Caller decides whether to retry. |
| Malformed JSON line | Logged at Warn, skipped. `Next` continues the loop. Does NOT advance assistant count. |
| Single line exceeded 16 MiB buffered without `\n` | Wrapped via a sentinel `ErrLineTooLarge` exported from the package. Stream is structurally broken; consumer must abort. |

**Logging of malformed lines is bounded.** Log message: `"jsonl: skipping malformed line"` with structured fields `"offset"` (int64) and `"err"` (the json error). Do NOT log line contents — claude session JSONL may contain user-supplied prompt text, file contents, secrets the user pasted, etc. The trust-boundary stance from `internal/agentrun/trust.go:1-9` ("MUST NOT log file contents at any layer") applies here verbatim — copy that sentence into the new package's doc comment.

Rate-limit malformed-line logging: log only the first occurrence + every 100th thereafter (counter on the Reader). Real claude does not emit malformed lines under normal operation; a flood means something catastrophic (corrupted file, partial-truncation, wrong-file pointed-at), and the operator needs the FIRST one with enough context, not 50,000 of them.

## Testing strategy

Single test file `internal/agentrun/jsonl/reader_test.go`. Same-package tests (`package jsonl`). All tests `t.Parallel()` (the reader has no global state).

**Fixtures.** Copy the three real session JSONL files cited under "Files to read first" into `internal/agentrun/jsonl/testdata/`:

- `testdata/clean.jsonl` — single `end_turn` (1 fires)
- `testdata/double_end_turn.jsonl` — transitional `end_turn` with 0 text + real `end_turn` with text (1 fires, on the second one only)
- `testdata/no_end_turn.jsonl` — `max_turns`-like run with no `end_turn` (0 fires)

Each test reads its fixture via `os.Open` and feeds it through `NewReader`. Add a comment at the top of each fixture-test naming the original `~/.claude/projects/-Users-...` path it came from, per the ticket AC. No fixture modification — files are byte-identical to the source. Fixture sizes: ~256 KB, ~64 KB, ~256 KB respectively. These are within normal Go testdata budgets (`internal/protocol/testdata` already commits real-world payloads).

**Fixture content review — committing real session JSONLs.** These files are pyrycode's own dev/CI session output. The architect run that produced each fixture's conversation was operating on pyrycode itself, so the contained "user" and "assistant" text is pyrycode source code, ticket bodies, and architecture discussion — already public on GitHub. Spot-check before committing: open each fixture and scan for `~/.claude.json` snippets, API tokens, `AKIA*`/`ghp_*`/`sk-*` patterns, or `.env` content. If any are found, drop that fixture and pick a different file from the same pool (1088 clean / 32 double / 30 none runs exist). The reader's malformed-line redaction is independent of this — committing a clean fixture is the right discipline regardless.

**Test cases** (bullet form — developer writes test bodies in pyrycode's table-driven style):

- `TestReader_CleanSingleEndTurn` — feed `testdata/clean.jsonl` end-to-end via `io.Copy`-style draining; assert exactly one Event has `EndOfTurn == true`, that it's the LAST assistant Event yielded, that `AssistantCount() == 25` at termination, and that `Next` returns `io.EOF` once drained.
- `TestReader_DoubleEndTurn_FirstSkipped` — feed `testdata/double_end_turn.jsonl`; assert that the assistant events yielded include two with `StopReason == "end_turn"`, but exactly ONE has `EndOfTurn == true` (the second), and that the first end_turn event has `TextChars == 0 && EndOfTurn == false`. `AssistantCount() == 6` at termination.
- `TestReader_NoEndTurn_SignalNeverFires` — feed `testdata/no_end_turn.jsonl`; assert NO Event has `EndOfTurn == true`, `AssistantCount() == 34`, `Next` returns `io.EOF`.
- `TestReader_PartialLine_BuffersUntilNewline` — use a custom `io.Reader` (a tiny step-feeder type that returns bytes from a script of byte-chunks) that yields one complete assistant JSON line split across two reads: chunk 1 is everything up to but excluding the final `}\n`, chunk 2 is `}\n`. Assert: first `Next` after chunk 1 is delivered returns `io.EOF`; the second `Next` (after chunk 2) yields the Event; `Offset()` after chunk 1's drain equals the StartOffset (no advance into partial line); after the line completes it advances past the `\n`. The step-feeder is ~10 lines, lives in the test file, no new types exported.
- `TestReader_NonAssistantLinesSkipped` — feed a small synthetic in-memory stream containing one `user` line, one `system` line, one `summary` line, one assistant `end_turn` line (built with `strings.NewReader` and inline JSON literals). Assert only one Event yielded (the assistant), `Offset` advances past all four lines, `AssistantCount() == 1`.
- `TestReader_MalformedLineSkippedAndCounted` — feed a stream: one valid assistant line, one corrupt line (`{"type":"assistant","message":{`), one valid assistant line. Assert two Events yielded, `AssistantCount() == 2`, no error returned (the corrupt line is Warn-logged and skipped). Capture log via a `slog.Handler` that records records (a ~15-line test handler, single-file scope).
- `TestReader_ResumeFromOffset` — feed `testdata/clean.jsonl`, drain to completion, record `r.Offset()`. Then construct a second Reader over `strings.NewReader("")` with `StartOffset = r.Offset()`. Assert the second reader's `Offset()` equals the recorded offset and `Next` returns `io.EOF` immediately. Pins the resume contract: StartOffset is reflected by Offset() before any read.
- `TestReader_OffsetAdvancesPerLine` — synthetic two-line stream of known byte length; after first Next, assert `Offset() == len(line1)+1`; after second Next, assert `Offset() == total`.

Optional fuzz (NOT required for this ticket, but worth a one-line note in the test file as a `// TODO: fuzz NewReader against random byte streams` once stdlib `testing.F` patterns are used elsewhere in the repo — sibling tickets can pick it up).

## Open questions

None blocking. Two minor implementation choices left to the developer:

1. **Initial buffer capacity.** Start `r.buf` at e.g. `make([]byte, 0, 8192)` to avoid the first ~3 grows on typical lines. Fine to leave at zero-cap and let `append` size it — performance is irrelevant at typical claude write rates (~1 line/sec). Either is fine.
2. **Sentinel name.** `ErrLineTooLarge` matches stdlib (`bufio.ErrTooLong`); pick that or `ErrLineExceedsMax` — developer's call.

## Size self-check

Production source files this spec prescribes:

- `internal/agentrun/jsonl/reader.go` — new

That is **1** production source file. Test file (`reader_test.go`) and `testdata/*.jsonl` fixtures do not count. Production LOC estimate: ~130 lines (Event + Reader + Config structs ~25; NewReader ~10; Next loop ~50; Offset/AssistantCount accessors + parse helper ~20; doc comments + package doc ~25). Within the S budget. Zero existing call sites (greenfield package); edit fan-out is zero. Proceed to spec commit.
