# `internal/contextwindow` — context-window usage reader

Reports the active claude session's context-window occupancy from its
resolved transcript, so a consumer (the `screen_snapshot` handler, and any
future push path) can surface a "context window used" gauge (mobile Status
sheet: "73% used (146K of 200K tokens)") without each consumer re-parsing the
JSONL (#856).

It is a pure leaf: one exported function, no dependency on `internal/sessions`.
**Shipped unwired** (0 non-test callers, correct for a primitive) — the wire
child **#857** obtains the transcript path from the [probe-preferred
resolver](sessions-package.md) (`newProbePreferredTranscriptResolver` /
`supervisor.Config.ResolveTranscript`) and carries `Read`'s output on
`screen_snapshot`. Mirrors the #847→#848 settings-on-snapshot split: this
ticket ships the leaf, #857 wires it.

Not `security-sensitive` — read-only reflection of non-secret runtime state
from an already-trusted, already-confined local transcript path (no new
inbound parsing, no authz decision, no mutation, no secret).

- Spec: [`856-context-window-usage-reader.md`](../../specs/architecture/856-context-window-usage-reader.md)
- Ticket record: [codebase/856.md](../codebase/856.md) (patterns + lessons)

## Files

```
internal/contextwindow/
├── usage.go       Usage struct + Read
├── usage_test.go  AC1–AC4 (latest-turn sum, window default, compaction reset, empty/no-usage) + open-error
└── testdata/      *.jsonl fixtures (latest_turn, compaction_reset, no_usage)
```

One file, one dependency: `internal/agentrun/jsonl`.

## Public API

```go
// Usage is the current context-window occupancy derived from a transcript's
// latest usage-bearing assistant entry.
type Usage struct {
    UsedTokens   int // input+cache_read+cache_creation+output on the latest usage entry
    WindowTokens int // the context-window size (defaultWindowTokens today)
}

// Read scans the claude transcript at path and reports current context-window
// usage.
func Read(path string) (Usage, error)
```

## Algorithm — last-usage-wins

`Read` opens `path` with `jsonl.NewReader` (reusing [`internal/agentrun/jsonl`](jsonl-reader.md)
rather than re-implementing a scanner — inherits its pointer-nil
"usage absent vs all-zeros" distinction, malformed-line log-skip, 16 MiB
per-line cap, and `server_tool_use`-key tolerance for free) and loops `Next()`
to `io.EOF`, tracking the **last** `Event` whose `Usage != nil` — overwriting
any earlier one. Entries without a usage block (transitional thinking-block
assistant lines, user/system/tool lines) do **not** clear the remembered
value.

`UsedTokens` is that last entry's `InputTokens + CacheReadInputTokens +
CacheCreationInputTokens + OutputTokens` — the *current* context size, not a
running total across turns.

**This is the whole of the compaction behaviour.** After an auto-compaction,
claude's next turn records a smaller `input_tokens` (the shrunk context).
Because `Read` always reports the latest usage-bearing entry — no `max()`, no
running total — a post-compaction read naturally returns the smaller, current
figure. Compaction surfaces as a reset for free, with no dedicated marker.

## Context-window size — single documented default

```go
const defaultWindowTokens = 200_000
```

`WindowTokens` is always `defaultWindowTokens`. Every current Claude model
(opus/sonnet/haiku) shares a 200K window, so the reported number *is* the
active model's window for every known model and the documented default for an
unknown/absent one. **Deliberately no per-model map** and no `message.model`
extraction — every entry would map to the same 200K value today (a defense
for a divergence that hasn't been observed). See Open questions below for the
seam.

## Error handling — three-way split

- **`path == ""`** → `Usage{0, defaultWindowTokens}, nil` **without opening
  anything**. This is the transcript resolver's `("", 0, nil)` "no transcript
  resolved yet" signal — not an error.
- **Transcript scanned, no usage entry found** (fresh session before the
  first turn completes) → `Usage{0, defaultWindowTokens}, nil`.
- **`os.Open` failure or a non-`io.EOF` error from `jsonl.Reader.Next()`**
  (`ErrLineTooLarge` or an underlying read error) → wrapped error
  (`contextwindow: open/scan transcript: %w`), zero `Usage`.

The two "nothing to report" cases are deliberately **not** errors, kept
distinct from genuine I/O failure — a consumer can tell "fresh session" from
"couldn't read". A raced-away file (`fs.ErrNotExist` on a path that existed
moments earlier) surfaces here as an error; whether a caller maps that to
"no usage yet" is that caller's call (#857).

## Concurrency

None. `Read` is a stateless open→scan→close call — no goroutines, channels,
or shared state. Safe to call concurrently; each call opens its own file and
`jsonl.Reader`. No logger is injected; the only logging inside the call is
`jsonl`'s malformed-line warning (defaults to `slog.Default()`, content-safe
by construction).

## Related

- [jsonl-reader.md](jsonl-reader.md) — `internal/agentrun/jsonl`, the decode
  this package reuses (`Event.Usage *UsageBlock`).
- [sessions-package.md](sessions-package.md) — the probe-preferred transcript
  resolver (#838) that will supply `Read`'s `path` argument.
- [847](../codebase/847.md) / [848](../codebase/848.md) — the settings-leaf /
  settings-wire split this ticket mirrors.
- #857 (blocked on this ticket) — wires `Read` onto `screen_snapshot`.
