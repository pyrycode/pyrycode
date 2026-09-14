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
    WindowTokens int // observed window for that entry's model, defaultWindowTokens on a miss, or 0 if UsedTokens disproved it
}

// Read scans the claude transcript at path and reports current context-window
// usage, sizing the window from windows when the latest usage-bearing entry
// names a model windows has an entry for (#2107).
func Read(path string, windows map[string]int) (Usage, error)
```

`Usage` deliberately carries no model field. The id that decided `WindowTokens` has no route
out of `Read` — two ints leave, nothing else. Adding one "for diagnostics" would reopen two
channels closed by design: the id is claude-authored, unsanitized text that must not reach a
log line (#833's posture) or an argv token (`ModelWindows`'s own doc names that risk), and
`Usage` has never had a path to either.

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

`WindowTokens` is `defaultWindowTokens` when nothing better is known — a guess,
not a fact. A 1M-context session exists and was measured live on 2026-09-04
(latest usage-bearing entry summing to 223075, 111% of this constant), so this
fallback alone was not enough to keep the gauge honest.

**The window and the used count arrive on different channels, and #2107 joins
them by model id.** The used count comes off the transcript; the window comes
off claude's stdout `result` line, decoded by #2101 and retained per session
by \#2106 (see [the fourth retention application](streamsup-package-retaining-the-decoded-model-list-for-the-session.md#retaining-the-per-model-context-windows-the-fourth-application-2106)).
`Read`'s `windows` parameter is that retained report, reduced to a plain
`map[string]int`. The join key is `message.model` on the transcript's latest
usage-bearing entry — read via [`jsonl.Event.Model`](jsonl-reader.md) — matched
against `windows` first by **exact, verbatim string comparison**: no
lowercasing, no alias expansion, no date stripping. Measured across 30
committed captures, two `modelUsage` entries do not imply two models (26 of 30
are an alias pair for one model, identical window under either spelling) and
no rule relating a dated to an undated spelling survives the data — so "the
largest", "the first", and "the only one" are each wrong on some real
capture, and only exact match is safe. **An empty model id is a miss on
either side of the join and never matched**: an assistant entry with no
`message.model` decodes to `""`, and #2101 keeps a `modelUsage` entry keyed
`""` (with a positive window) reachable in the retained report, sorted first
by `ModelID`. `Read` never performs the lookup when the transcript-side key
is `""`, so that reachable `""` entry is structurally unreachable from this
join — the rule is enforced once, not spelled on both sides.

**#2118 — an exact miss falls back to one trailing variant group, and only
one.** claude keys a 1M-context model's `modelUsage` entry with a bracketed
suffix (`claude-opus-5[1m]`) while the transcript names the model without it
(`claude-opus-5`), so the exact join missed on every turn of every 1M session,
fell back to `defaultWindowTokens`, and #2100's own contradiction check then
zeroed the reading — a session that should show a real fraction of 1,000,000
instead drew a blank gauge, which is what the exact-match paragraph above did
not anticipate. After an exact match misses, `Read` now looks for windows keys
that are the transcript's id plus exactly one trailing `[...]` group
(`variantBase`, mirroring the grammar `internal/relay`'s `validModel` (#1838)
machine-checks for the same shape, without sharing the function — that one
guards an argv/turn-text sink in a package this one cannot import). If
exactly one such key carries a positive window, that window is reported; zero
or two-or-more is still a miss. The exact-match evidence above is why the
tolerance stops at one bracket group: lowercasing, date-stripping and alias
expansion are still refused, only the observed suffix shape is now bridged.
Requiring a *unique* variant match (not "the first" or "the largest") is what
makes the answer independent of Go's randomised map iteration, on top of
guarding against a key that claims a base it doesn't exactly spell.
**Non-positive entries are dropped before they are counted, not after**: a
`modelUsage` entry `Read` already treats as absent by contract (see Error
handling below) must not be able to manufacture an ambiguity that collapses a
reading which has exactly one correct answer.

**#2100 — `Read` stops reporting a window its own data disproves — and this
only holds if resolution runs before the check.** `Read` first resolves
`WindowTokens` (from `windows`, falling back to `defaultWindowTokens` on a
miss), *then* applies #2100's comparison: a used count above the resolved
window is proof the resolved value is wrong, and `Read` reports `WindowTokens`
0 rather than asserting a window it can see is false. Reversing the two steps
changes the answer: a 1M session summing to 223075 reports `1000000` when
resolved first, `0` when checked first (223075 exceeds the *default* 200000
before the real window is even looked up). `UsedTokens` still carries the true
sum in both orderings; only the denominator differs. Equality is not a
contradiction (a session exactly at its window is full, not evidence of a
wrong belief) and keeps its window. This is deliberately not an error — see
Error handling below.

**`Read`'s reading was cross-checked against claude's own, live, on 2026-09-09 (#2287).**
A `get_context_usage` control-request capture read the same session both ways: `Read`
reported 21978 tokens against a 200000 window (10.99%), claude's own reply reported 21929
and a top-level 11%. The two agree to within the tokens the transcript gained in the gap
between claude's reply and `Read`'s later scan — no divergence to design around, and no
change to `Read` followed from it. See
[e2e-realclaude-context-usage-capture-test-go.md](e2e-realclaude-context-usage-capture-test-go.md).

**The gap #2107 deliberately leaves open.** A window learned from the stream
is known only from the session's first completed turn onward, and again only
from the first completed turn after a daemon restart — `windows` answers a
miss in both gaps, and `Read` falls back to `defaultWindowTokens` exactly as
it did before this join existed. No complaint has been observed about that
sub-200K restart gap, and where the used count already exceeds the default
in that gap, #2100's check still collapses the reading to 0 rather than a
clamped lie. Persisting the observed window with the session record (closing
the gap) is deferred; no successor ticket exists yet.

**The resolver lives in `cmd/pyry`, one file, following `resolveBoundModelList`'s
precedent minus its conversation hop** (see
[the model-list resolver](sessions-package-key-types-runner-interface-runnerfactory.md)):
type-assert `Session.Runner()` to the `ModelWindows() (modelWindowReport, bool)`
method #2106 added, comma-ok as the only filter, and build a fresh
`map[string]int` per call so `Read`'s "never retained" contract holds. Both
`cmd/pyry` seams that call `contextwindow.Read` — `bootstrapSnapshotUsage`
(`screen_snapshot`) and the by-id closure in `snapshotUsageFor`
(`session_settings`) — take the resolver as a parameter; a `nil` resolver
degrades to today's default-window reading rather than collapsing the seam to
nil, the same "degrading one integer must not make a resolvable session
unresolvable" rule `runConfigFor` already applies to its `usage` half.

**Naming trap: a `cmd/pyry` file cannot be named `session_model_windows.go`.**
Go applies an implicit `GOOS=windows` build constraint to any file whose name
ends `_windows.go` — the plan named the resolver's file that, and the package
failed to build on darwin/linux because `sessionModelWindows` was undefined
everywhere except a Windows target this project doesn't support. The exported
symbol keeps the name; only the file (and its `_test.go` twin) is renamed —
here, `session_model_window_lookup.go`. Worth checking on any future
`cmd/pyry` file whose natural name would end in `_windows`, `_linux`,
`_darwin`, `_test`, or one of Go's other implicit build-constraint suffixes.

**#2423 — the transcript *folder* is resolved per session, the same way the
window is per model.** Before #2423, the by-id reader behind
`session_settings` (`snapshotUsageFor`) read every session's transcript out of
one fixed folder — the daemon's own working directory
(`resolveClaudeSessionsDir`), decided once at start-up. A conversation created
with a `cwd` spawns claude somewhere else (`Pool.buildSession` prefers the
spawn directory over the template workdir), so claude writes that session's
transcript under the projects folder named for the directory it actually
resolved. The stat missed, `Read` collapsed to its fresh-session report, and
every `session_settings` reply for such a conversation showed `0` used tokens
against the default window — the desktop's "Context: 0%" for a chat already
43% into its window.

`snapshotUsageFor` now takes a folder resolver keyed by session id (`dirFor`)
in place of the fixed `dir`. `sessionTranscriptDir`
(`cmd/pyry/session_transcript_dir.go`) answers it exactly like
`sessionModelWindows` above — `Pool.Lookup` on the id, then a type assertion
off `Session.Runner()` — but for a `ClaudeSessionsDir() string` method instead
of `ModelWindows`. That method (on `streamRunner`) hands back the *stored*
string `mapStreamsupConfig` already computed for that same runner's own spawn
probe, never a fresh derivation, so the reader and the probe read one field of
one struct and cannot name different folders for one session — including
under case canonicalisation or a symlink (`agentrun.ResolveWorkdir` applies
both and the confining validators upstream of it do not, so a second,
independent derivation from a differently-spelled workdir is exactly the
shape that drifts). `fixedTranscriptDir(dir)` adapts one folder into the same
resolver shape for a caller that genuinely has only one.

The build-time nil rule reads the same way it does for `windows`: a `nil`
folder resolver is decided on the resolver's *presence*, not on what it
answers, so an unwired daemon's `session_settings` seam still collapses to
the zero-reporting nil shape rather than becoming a working reader against
the default window (AC 5). Per call, `""` is the folder resolver's only
refusal — an unknown session id, a runner without the method, and a runner
whose own derivation degraded all answer it, and `snapshotUsageFor` reads an
empty folder as "nothing to stat," the same path a genuinely-absent
transcript already takes. `sessionModelWindows` and `sessionTranscriptDir`
therefore share one latent gap, recorded rather than fixed: `Pool.Lookup("")`
can return `(nil, nil)` for the evicted-bootstrap or zero-value-map state
`Pool.DefaultSettings` documents, and the comma-ok type assertion on a nil
`Session` would panic rather than refuse. Not reachable today — both
resolvers are only ever called with the non-empty id `resolveBoundRunSettings`
produces — so no defence was added; if either resolver is touched again, one
shared nil-check closes it in both rather than one.

**`bootstrapSnapshotUsage` (the `screen_snapshot` seam) deliberately keeps its
fixed folder.** The bootstrap session's working directory *is* the daemon's
own trusted workdir, so it has no workspace to miss, and it is the one place
left where the reader's folder (`filepath.Abs`) and the spawn probe's
(`agentrun.ResolveWorkdir`, which also canonicalises case and resolves
symlinks) could in principle still disagree — a divergence #1655 measured as
absent in practice. #2423 chose not to touch a shipped reading to close a gap
with no observed instance; routing this seam through `sessionTranscriptDir`
needs a ticket that owns the reading it would change.

**No unit table over the reader can prove this class of fix.** The defect was
*which folder crossed the seam*, not what the reader does with one — a fixed
folder and a per-session one behave identically once each is handed a
directory, so both pass the same reader-level tests. The only test that
discriminates plants two transcripts under two different workspace folders,
drives a real daemon through two `create_conversation`s carrying distinct
`cwd`s, and asserts each conversation's `session_settings` reports its own
count (`TestRelayV2_StreamSessionSettingsReadsEachWorkspacesOwnTranscript`
in `internal/e2e`) — it was run red against the pre-fix wiring before being
accepted green.

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

`windows == nil` is legal and answers a miss on every lookup — "nothing
observed" needs no second spelling. `path == ""` keeps `defaultWindowTokens`
regardless of `windows`: no transcript means no model, so there is nothing to
join on and the map is never consulted.

## Concurrency

None. `Read` is a stateless open→scan→close call — no goroutines, channels,
or shared state. Safe to call concurrently; each call opens its own file and
`jsonl.Reader`, and now additionally reads — never writes or retains — a
caller-supplied `windows` map. A shared map handed in by two concurrent
callers is safe for the same reason: `Read` never mutates it and never hangs
it off the package.

## Related

- [jsonl-reader.md](jsonl-reader.md) — `internal/agentrun/jsonl`, the decode
  this package reuses (`Event.Usage *UsageBlock`, and since #2107
  `Event.Model` — the join key).
- [sessions-package.md](sessions-package.md) — `ResolveTranscript` /
  `newProbePreferredTranscriptResolver` (#838), the probe-preferred resolver
  precedent #857 followed via a *different*, cmd/pyry-local instance
  (`resolveOwnBootstrapJSONL`) rather than this seam directly — see above.
- [streamsup-package-retaining-the-decoded-model-list-for-the-session.md](streamsup-package-retaining-the-decoded-model-list-for-the-session.md) —
  #2106's `sessionModelWindowHold`, the retained report `Read`'s `windows`
  parameter is built from.
- [sessions-package-key-types-runner-interface-runnerfactory.md](sessions-package-key-types-runner-interface-runnerfactory.md) —
  `resolveBoundModelList`, the resolver shape #2107's `sessionModelWindows`
  copies minus the conversation hop, and the same shape #2423's
  `sessionTranscriptDir` copies for the folder half.
- [847](../codebase/847.md) / [848](../codebase/848.md) — the settings-leaf /
  settings-wire split this ticket mirrors.
- [857](../codebase/857.md) — wires `Read` onto `screen_snapshot` via the
  optional `SnapshotUsage` seam on `V2SessionConfig`.
