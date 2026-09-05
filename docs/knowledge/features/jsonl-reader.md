# `internal/agentrun/jsonl` — JSONL line reader + deterministic end-of-turn detector

Pure stdlib reader over claude session JSONL output. Single concern: take an `io.Reader`, surface one `Event` per parsed JSONL line — every line kind, with verbatim bytes and any embedded `usage` block — and apply a deterministic end-of-turn rule on assistant entries. No filesystem watching, no path resolution, no goroutines. The fsnotify driver (#349) wraps it; the dashed-path encoding ([agentrun-package.md](agentrun-package.md), `EncodeProjectDir`) supplies the directory name.

## Public API

```go
package jsonl

type Event struct {
    StopReason string          // assistant only; "" on non-assistant
    TextChars  int             // assistant only; 0 on non-assistant
    EndOfTurn  bool             // assistant only; StopReason == "end_turn" AND TextChars > 0
    Raw        json.RawMessage // verbatim line bytes, trailing '\n' stripped (CRLF '\r' preserved)
    Kind       string           // whitelisted: "assistant"|"user"|"tool_use"|"tool_result"|"system"|"attachment"|""
    Usage      *UsageBlock      // non-nil only on assistant entries that carry a `usage` object
    Model      string           // assistant only; message.model verbatim, or "" if absent, non-assistant, or over 256 bytes (#2107)
}

type UsageBlock struct {
    InputTokens              int
    OutputTokens             int
    CacheCreationInputTokens int
    CacheReadInputTokens     int
}

type Config struct {
    Logger      *slog.Logger // optional; defaults to slog.Default
    StartOffset int64        // informational; caller must Seek src to this position before constructing
}

func NewReader(src io.Reader, cfg Config) *Reader
func (r *Reader) Next() (Event, error)        // io.EOF on drain; wrapped error on read failure; ErrLineTooLarge if a single buffered line exceeds 16 MiB
func (r *Reader) Offset() int64               // byte position of the next not-yet-consumed line; safe to persist as resume point
func (r *Reader) AssistantCount() int         // includes transitional empty-content end_turn entries

var ErrLineTooLarge = errors.New("jsonl: line exceeds maximum size")
```

Two types, one struct, one constructor, three methods. `Reader` is **not safe for concurrent use** — construct one per source.

### Event semantics across kinds (#353)

| Input line                                            | `Kind`         | `Raw` | `StopReason` | `TextChars` | `EndOfTurn` | `Usage`       |
| ----------------------------------------------------- | -------------- | ----- | ------------ | ----------- | ----------- | ------------- |
| assistant + `end_turn` + text                         | `"assistant"`  | yes   | `"end_turn"` | `>0`        | `true`      | nil or set    |
| assistant + `end_turn` + empty content (transitional) | `"assistant"`  | yes   | `"end_turn"` | `0`         | `false`     | nil or set    |
| assistant + `tool_use`                                | `"assistant"`  | yes   | `"tool_use"` | sum text    | `false`     | nil or set    |
| assistant carrying a `usage` object                   | `"assistant"`  | yes   | per-line     | per-line    | per-line    | **non-nil**   |
| assistant without `usage`                             | `"assistant"`  | yes   | per-line     | per-line    | per-line    | **nil**       |
| user / tool_use / tool_result / system / attachment   | the kind       | yes   | `""`         | `0`         | `false`     | `nil`         |
| `{"type":"summary",…}` or any other / missing `type`  | `""`           | yes   | `""`         | `0`         | `false`     | `nil`         |
| malformed JSON                                        | (not surfaced) | —     | —            | —           | —           | —             |

`Kind` is a closed whitelist. Anything not in the six recognised values — including a missing `type` field — maps to `""`; the verbatim bytes survive on `Raw` so downstream re-emitters can still forward unrecognised line shapes byte-equivalent to claude's output. New claude line kinds land in the unrecognised bucket until the whitelist is widened.

`Usage` is pointer-valued to distinguish "field absent" from "field present with all zeros". It is **never** non-nil on a non-assistant line, even if such a line carries a `usage`-shaped sub-object (defensive contract; not observed in practice).

### `Model` — a comparison key, not renderable text (#2107)

`Model` mirrors `message.model` verbatim on an assistant entry — the join key [`internal/contextwindow`](contextwindow-package.md) matches against a retained per-model window report. It is claude-authored, unsanitized text that may carry control characters or terminal escapes; nothing downstream may log it, render it, or pass it through to a subprocess argv.

`maxModelIDBytes = 256` bounds it, and the bound is derived rather than chosen: the retained report on the other side of the join is itself capped at 256 bytes per id (`streamsup.maxModelWindowID`), so anything longer is a guaranteed miss regardless. **Over-cap yields the empty string, never a truncated prefix.** A cut id could collide with a genuinely different, shorter model whose id happens to match that prefix — a false match — where an empty id is only ever a guaranteed miss. This is `jsonl`'s first capped decoded field; `StopReason` still rides uncapped, because it is compared against a closed set of literal values rather than an open one, and a comparison key crossing a trust boundary is the case that earns a cap.

No aliasing risk: `Model` is decoded through `encoding/json`'s normal struct-field path, which allocates a fresh string, unlike `strings.ToValidUTF8(s[:limit], "")`-style capping elsewhere in the codebase (see [contextwindow-package.md](contextwindow-package.md)'s and the streamsup retention doc's notes on that trap) — there is no `s[:limit]` slice here to alias the reader's line buffer.

**Adding `Model` widened what a malformed `message` object can abort, and that widening is accepted rather than engineered around.** Before #2107, `rawAssistantMessage` had no `model` field at all, so any value there — of any JSON type — was silently ignored by `encoding/json` as an unknown field. Now a `model` field of the wrong JSON type (a number, an object) fails the inner unmarshal, and the entry — usage block included — is logged-and-skipped like any other malformed line, per the "malformed JSON line" row above. A caller reading the current context size can therefore lose the *latest* usage entry to a shape claude has never actually emitted (all 742 measured entries in a real transcript carried `message.model` as a string). `StopReason` carries the identical risk on the same struct already; a `json.RawMessage` two-step to avoid it would be more code for a shape not observed in practice. Pinned by `TestReader_NonStringModelSkipsTheEntry` so the tradeoff is chosen, not stumbled into.

## End-of-turn rule

Phase A spike (#329) verified across 1151 real pyrycode session JSONLs. The rule is:

> An assistant entry is the real end-of-turn iff `message.stop_reason == "end_turn"` AND `sum(len(content[i].text)) > 0`.

Verification breakdown:
- 1088 clean runs (single `end_turn`, always last assistant entry) — rule fires correctly.
- 32 single-turn double-`end_turn` runs — first `end_turn` has 0 text (transitional, claude's thinking block resolving); second has 248–3252 chars of real response. Rule correctly skips the first, fires on the second.
- 30 no-`end_turn` runs (max_turns / interrupted) — rule never fires; consumer's `--max-turns` counter is the structural backstop (#349).
- Zero false-positives, zero false-negatives.

The previously-feared "premature termination" failure mode (an early empty `end_turn` collapsing the loop before the real response) is structurally impossible under this rule. Empty-content `end_turn` entries are surfaced as ordinary `Event`s with `EndOfTurn == false` and DO advance `AssistantCount`.

## Line framing

Single state-machine loop in `Next`:

1. Search the internal buffer for `'\n'`. If found, slice the line, **copy into a freshly-allocated `[]byte`** so the caller's `Event.Raw` cannot be mutated by subsequent `append` into the reader's backing array, advance `offset` past `len(line)+1`, decode, return (or skip on parse error).
2. If no `'\n'`, call `src.Read` into a 4 KiB scratch buffer, append to internal buffer, repeat.
3. EOF is **not sticky**: only surfaced when `Read` returns `(0, io.EOF)` AND the buffer holds no complete line. A later call to `Next` may pull more bytes from a growing source (the fsnotify-tail contract). Partial bytes are retained across calls.

Buffer cap: 16 MiB per single line (well above the ~80 KiB observed real maximum). Exceeding it returns `ErrLineTooLarge` — the stream is structurally broken; the consumer must abort.

**`Raw` aliasing trap.** A `lineCopy := r.buf[:i]` slice would share memory with the reader's growing buffer; a later `append(r.buf, ...)` can write into that same backing array and silently mutate `Event.Raw` after the caller has received it. The reader allocates a fresh slice and `copy`s into it before assigning to `Event.Raw`. Pinned by `TestReader_RawByteEquivalence` which calls `Next` again before inspecting earlier `Raw` slices.

### Why hand-rolled buffering and not `bufio.Scanner` / `bufio.Reader.ReadBytes`?

- `bufio.Scanner` defaults to a 64 KiB token cap (real lines reach ~80 KiB) and cannot distinguish "drained mid-line, more may come" from "stream ended" — both surface as `Scan() returning false`. The reader needs that distinction to wait for fsnotify versus surface a fatal error.
- `bufio.Reader.ReadBytes('\n')` returns partial bytes + `io.EOF` mid-line; re-feeding requires a custom `io.MultiReader` dance. The hand-rolled buffer is ~15 lines and removes the dance entirely.

If a future consumer owns a `*bufio.Reader` for other reasons, the line-framing logic shrinks. Today there is no such consumer — build the simplest thing.

## Two-pass JSON parsing

```go
type rawLine struct {
    Type    string          `json:"type"`
    Message json.RawMessage `json:"message"`
}
type rawAssistantMessage struct {
    StopReason string `json:"stop_reason"`
    Model      string `json:"model"`
    Content    []struct {
        Type string `json:"type"`
        Text string `json:"text"`
    } `json:"content"`
    Usage *struct {
        InputTokens              int `json:"input_tokens"`
        OutputTokens             int `json:"output_tokens"`
        CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
        CacheReadInputTokens     int `json:"cache_read_input_tokens"`
    } `json:"usage"`
}
```

The outer decode keeps `message` as `RawMessage` so non-assistant entries skip the inner cost (their `content` shapes differ — `user` lines have `content` as a string, not an array — single-pass decode would either fail or require pointer-to-`any`). Only when `Type == "assistant"` does the inner decode run. Unknown top-level fields (`parentUuid`, `requestId`, `timestamp`, `cwd`, etc.) are silently ignored by `encoding/json`.

`Usage` on `rawAssistantMessage` is **pointer-typed** so `encoding/json` leaves it `nil` when the field is absent. When non-nil after unmarshal, the loop builds an exported `UsageBlock` value and assigns its pointer to `Event.Usage`. The exported type is intentionally a value, not a pointer, so the `UsageBlock` itself is concrete; only the optionality of "present on this Event" is pointer-modelled.

`TextChars` is computed as `sum(len(c.Text) for c in raw.Message.Content)`. Content blocks of `type == "thinking"` or `type == "tool_use"` have `text` absent → `c.Text == ""` → contributes 0 naturally. No explicit type-filter needed; the math matches the spec's literal `sum(len(content[i].text))` wording.

## Trust boundary — do NOT log file contents

The package doc-comment carries the same `MUST NOT log file contents at any layer` stance as [agentrun-package.md](agentrun-package.md). Claude session JSONL may contain user prompts, file contents, or other operator-supplied material the operator has not opted into being logged. The reader logs only:

- Offsets (int64).
- Error message strings (the `json` package's parse error — not the source line bytes).
- Structured slog fields, never the raw line.

Malformed-line logging is **rate-limited**: the first occurrence + every 100th thereafter. Real claude does not emit malformed lines; a flood means catastrophic corruption (truncated mid-write, wrong-file pointed-at) and the operator needs the FIRST one with enough context, not 50,000 copies.

## Error classes

| Class | Surface |
|---|---|
| Source drained, partial bytes may be buffered | `io.EOF` (sentinel; check via `errors.Is`). The next `Next` call may yield an event if `src.Read` produces more bytes. |
| Source `Read` failed (file closed, IO error) | Wrapped: `fmt.Errorf("jsonl: read at offset %d: %w", offset+len(buf), err)`. |
| Malformed JSON line (outer or inner) | Logged at Warn (rate-limited), skipped. Does NOT surface as an Event. Does NOT advance `AssistantCount`. Does NOT terminate iteration. Claude is the source of truth; one broken line must not poison the stream. |
| Single line exceeded 16 MiB without `'\n'` | `ErrLineTooLarge` (exported sentinel). Consumer must abort. |

## Concurrency model

None. Single-goroutine. The fsnotify driver (#349) calls `Next` synchronously on its event loop. No `context.Context` parameter on `Next` — cancellation is structural: the consumer closes the underlying `*os.File`, the next `Read` returns an error, `Next` surfaces it. The fsnotify driver owns the context.

## Resume contract

`Config.StartOffset` is informational. The caller must `Seek` the underlying `*os.File` to that position before constructing the reader; the `Reader` uses the value only to make `Offset()` report absolute file positions for resume. After construction `Offset() == StartOffset` until the first complete line is consumed. Every successful `Next` (and every silently-skipped non-assistant or malformed line) advances `Offset` past the consumed line's trailing `'\n'`. `Offset` does NOT advance into a partial-line buffer — a half-written line keeps `Offset` at the line's start byte so the resume point always lands on a complete line.

## Fixtures

Tests under `internal/agentrun/jsonl/testdata/` are byte-identical copies of real pyrycode session JSONLs from `~/.claude/projects/-Users-...-Workspace-Projects--pyrycode-worktrees-*`:

- `clean.jsonl` — single `end_turn`, last assistant entry, fires once.
- `double_end_turn.jsonl` — transitional `end_turn` with 0 text + real `end_turn` with text; signal fires on the second only.
- `no_end_turn.jsonl` — `max_turns`-like run; signal never fires.

Original source paths recorded in the test file's header comment so a future operator can re-pull from the same shape if a fixture needs refresh. Files are pyrycode's own dev session output (operating on pyrycode itself); contents are pyrycode source / ticket text already public on GitHub. No secret-scrub required, but the discipline applies on any future fixture refresh.

## Consumers

- **Sibling #349** (JSONL fsnotify watcher) — wraps this reader with a `fsnotify.Watcher` over `~/.claude/projects/<EncodeProjectDir(workdir)>/`, calls `Next` synchronously on each Write event, persists `Offset()` as the resume point, surfaces `EndOfTurn` to the dispatcher's max-turn enforcement loop. After #353 the watcher's `OnEvent` fires for every line kind (not just assistant entries); it adds no filtering of its own beyond what the reader does.
- **Future stream-json emitter** (split from #335) — consumes `Event.Raw` to re-emit lines byte-equivalent to `claude -p --output-format stream-json`, aggregates `Event.Usage` across assistant entries to compose a result trailer. Not in tree yet; #353 only extends the reader contract.
- [`internal/contextwindow`](contextwindow-package.md) (#2107) — `Read` tracks `Event.Model` alongside `Event.Usage` on the same branch ("latest usage-bearing entry" keeps one definition), and joins it against a per-model window report by exact string match.

## Out of scope

- File open / close — consumer owns the `*os.File`.
- Fsnotify wiring — sibling #349.
- Path encoding (`~/.claude/projects/<dashed-cwd>/`) — sibling #347's [`EncodeProjectDir`](agentrun-package.md).
- Fuzz coverage — leave a `// TODO: fuzz NewReader` marker; sibling tickets can pick it up once `testing.F` patterns appear elsewhere in the repo.
- Cross-platform encoding audit — pyrycode targets darwin + linux only.

## Related

- [agentrun-package.md](agentrun-package.md) — pre-spawn primitives + the dashed-directory encoder this reader's consumer points at.
- [pyry-agent-run-command.md](pyry-agent-run-command.md) — the verb whose JSONL output this reader consumes.
- [jsonl-reconciliation.md](jsonl-reconciliation.md) — separate concern: startup-time `<uuid>.jsonl` scan for the registry. Different code path; this reader is per-turn streaming.
- [contextwindow-package.md](contextwindow-package.md) — the sole consumer of `Event.Model` (#2107), joining it against a per-model window report.
