# `internal/contextwindow` — context-window usage reader

Reports the active claude session's context-window occupancy from its
resolved transcript, so a consumer (the `screen_snapshot` handler, and any
future push path) can surface a "context window used" gauge (mobile Status
sheet: "73% used (146K of 200K tokens)") without each consumer re-parsing the
JSONL (#856).

It is a pure leaf: one exported function, no dependency on `internal/sessions`.
Shipped unwired at #856 (0 non-test callers, correct for a primitive); **wired
by #857**, which carries `Read`'s output on `screen_snapshot` via a new
optional `SnapshotUsage` seam on `internal/relay`'s `V2SessionConfig`. Mirrors
the #847→#848 settings-on-snapshot split.

**Resolver used: `resolveOwnBootstrapJSONL`, not `ResolveTranscript`.** This
package's own spec anticipated #857 would obtain the transcript path via
`internal/sessions`' [probe-preferred `ResolveTranscript`
seam](sessions-package.md) (`newProbePreferredTranscriptResolver` /
`supervisor.Config.ResolveTranscript`, the growth-confirm consumer). #857's
architect spec deliberately used a different resolver instead:
`resolveOwnBootstrapJSONL` (`cmd/pyry/interactive_turn_stream_v2.go`), the
turn-stream's own probe-preferred, cmd/pyry-local resolver, via a **second,
dedicated instance**. Reasoning: `ResolveTranscript` is a `*sessions.Pool`
seam and threading it into `relay.go` would re-import `internal/sessions`
into a file that discipline keeps sessions-free (see
[v2-session-manager.md § Inbound screen-snapshot handler](v2-session-manager.md)
and [codebase/857.md](../codebase/857.md) for the full rationale). Both
resolvers are probe-preferred and functionally equivalent for this purpose;
the choice is about where the `internal/sessions` dependency lives, not
about correctness.

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
    WindowTokens int // believed window (defaultWindowTokens), or 0 if UsedTokens disproved it
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

## Context-window size — a believed default, not an asserted fact

```go
const defaultWindowTokens = 200_000
```

`WindowTokens` is what this package *believes* a session's window is, absent
anything better to go on — it is a guess, not a fact. A 1M-context session
exists and was measured live on 2026-09-04 (latest usage-bearing entry summing
to 223075, 111% of this constant), so a session's real window is not knowable
from the transcript's usage blocks alone. **Deliberately no per-model map** and
no `message.model` extraction — sourcing the real window that way is #2101/#2102;
this constant stays as the fallback for a session whose window is not yet known.

**#2100 — `Read` stops reporting a window its own data disproves.** A used
count *above* the believed window is proof the belief is wrong, and `Read`
reports `WindowTokens` 0 in that case rather than asserting a window it can see
is false — the daemon needs no model knowledge to detect this, only the
comparison `UsedTokens > WindowTokens`. `UsedTokens` still carries the true
sum; only the denominator is withheld. Equality is not a contradiction (a
session exactly at its window is full, not evidence of a wrong belief) and
keeps its window. The comparison is against the field, not the constant, so it
stays correct once #2101 makes the believed window per-session instead of a
fixed default. This is deliberately not an error — see Error handling below.

## Error handling — four-way split

- **`path == ""`** → `Usage{0, defaultWindowTokens}, nil` **without opening
  anything**. This is the transcript resolver's `("", 0, nil)` "no transcript
  resolved yet" signal — not an error.
- **Transcript scanned, no usage entry found** (fresh session before the
  first turn completes) → `Usage{0, defaultWindowTokens}, nil`.
- **Transcript scanned, latest usage entry's sum exceeds the believed window**
  (#2100) → `Usage{sum, 0}, nil`. Deliberately **not** an error: a wrong belief
  is a fact about the data, not a read failure. Routing it through the error
  path would have collapsed it back onto the disproved window, since #857's
  recovery (`Read("")`, below) reports `defaultWindowTokens`.
- **`os.Open` failure or a non-`io.EOF` error from `jsonl.Reader.Next()`**
  (`ErrLineTooLarge` or an underlying read error) → wrapped error
  (`contextwindow: open/scan transcript: %w`), zero `Usage`.

The three "nothing wrong, just report it" cases above are deliberately **not**
errors, kept distinct from genuine I/O failure — a consumer can tell "fresh
session" and "disproved window" from "couldn't read". A raced-away file
(`fs.ErrNotExist` on a path that existed moments earlier) surfaces here as an
error; #857's closure maps that (and every other `Read` error) to `Read("")`'s
deterministic fresh-session report rather than surfacing it — the seam is
non-erroring by contract (plain ints, mirroring `SnapshotSettings`). That
recovery path discriminates on `err != nil`, never on the window value, so the
disproved-window arm above (which returns a nil error) cannot be routed into it.

## Concurrency

None. `Read` is a stateless open→scan→close call — no goroutines, channels,
or shared state. Safe to call concurrently; each call opens its own file and
`jsonl.Reader`. No logger is injected; the only logging inside the call is
`jsonl`'s malformed-line warning (defaults to `slog.Default()`, content-safe
by construction).

## Related

- [jsonl-reader.md](jsonl-reader.md) — `internal/agentrun/jsonl`, the decode
  this package reuses (`Event.Usage *UsageBlock`).
- [sessions-package.md](sessions-package.md) — `ResolveTranscript` /
  `newProbePreferredTranscriptResolver` (#838), the probe-preferred resolver
  precedent #857 followed via a *different*, cmd/pyry-local instance
  (`resolveOwnBootstrapJSONL`) rather than this seam directly — see above.
- [847](../codebase/847.md) / [848](../codebase/848.md) — the settings-leaf /
  settings-wire split this ticket mirrors.
- [857](../codebase/857.md) — wires `Read` onto `screen_snapshot` via the
  optional `SnapshotUsage` seam on `V2SessionConfig`.
