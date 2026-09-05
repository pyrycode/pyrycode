# #2107 — Report the observed context window for the model that produced the used count

## Files read

- `internal/contextwindow/usage.go` → `Read`, `Usage`, `defaultWindowTokens` — the whole
  package; the join lands inside `Read` and the two stale doc references live here.
- `internal/contextwindow/usage_test.go` → `TestRead_Fixtures` — the table the join rows
  extend; its `wantWindow` is already per-row (#2100 made it so), which is exactly what a
  per-model window needs.
- `internal/contextwindow/testdata/*.jsonl` — **every committed fixture already carries
  `message.model`** (`latest_turn.jsonl` names `claude-opus-4-7`, `over_window.jsonl` names
  `claude-opus-5`). No fixture rewrite is needed for the basic join; one new fixture supplies
  the two-models-one-session case.
- `internal/agentrun/jsonl/reader.go` → `Event`, `rawAssistantMessage`, `Next`,
  `maxLineBytes` — the decode that must surface `message.model`, and the package-constant
  bound style to mirror.
- `cmd/pyry/snapshot_usage.go` → `snapshotUsageFor`, `bootstrapSnapshotUsage` — the sole
  production consumer of `contextwindow.Read`, and both of the two seams AC 1 names.
- `cmd/pyry/relay.go` → `relayWiring`, `startRelayV2`, `runConfigFor` — the two construction
  sites (`bootstrapSnapshotUsage` for `screen_snapshot`, `snapshotUsageFor` for
  `runConfigFor`/`session_settings`) and the struct a resolver must be threaded through.
  `runConfigFor`'s doc states the `usage == nil` rule this ticket must copy rather than
  invert.
- `cmd/pyry/main.go` → `resolveClaudeSessionsDir`, the `relayWiring` composite literal —
  where `runSettings` and `bootstrapIDFn` are built over `pool`; the new resolver joins them.
- `cmd/pyry/session_model_window_hold.go` → `modelWindowReport`, `sessionModelWindowHold`,
  `ModelWindows`, `cloneModelWindowReport` — #2106's retention: the report is a copy, sorted
  by `ModelID`, every `WindowTokens` above zero, and the bool is the only "never reported"
  spelling.
- `cmd/pyry/streamsup_runner.go` → `streamRunner.ModelWindows` — the accessor reached by
  type assertion off `Session.Runner`; states that the interface is deliberately un-widened.
- `cmd/pyry/session_model_list.go` → `resolveBoundModelList`, `retainedModelLists` — the
  pool-keyed resolver precedent: type assertion off `Runner()`, comma-ok as the only filter,
  no logger, errors discarded rather than wrapped. This ticket's resolver is that shape minus
  the conversation hop.
- `internal/streamsup/parser.go` → `maxModelWindowID`, `decodeModelWindows`,
  `maxModelWindowEntries` — the producer's 256-byte id bound and its "a cut id names no
  model" argument, which is what makes the transcript-side bound derivable rather than
  invented.
- `internal/e2e/internal/fakeclaude/main.go` → `outResult`, `writeStreamResponse`,
  `runStreamJSON`, `writeBackgroundTaskRoster`, `envStreamRoster` — the rider pattern
  (#2080) this ticket's `modelUsage` rider copies, including its parameter-ordering
  discipline.
- `internal/e2e/relay_v2_stream_run_config_test.go` → `TestRelayV2_StreamRequestSessionSettings`
  — the e2e template: pair → fakerelay → `StartStreamInteractiveWithRelay` → seed
  conversation → `request_session_settings`. Its existing assertion (`used_tokens == 0`,
  because the minted session has no transcript) is the state this ticket's e2e departs from.
- `internal/e2e/relay_v2_stream_background_task_roster_reconcile_test.go` — the
  drive-a-turn-and-wait-for-`turn_end` block, and the happens-before it names: a rider line
  written before the reply means `turn_end` on a client implies the hold already holds it.
- `internal/e2e/rotation_test.go` → `claudeSessionsDir`, `encodeWorkdir` — the in-package
  mirror of `sessions.DefaultClaudeSessionsDir` that lets a test compute the daemon's
  transcript directory without touching the test process's `$HOME`.
- `internal/e2e/harness.go` → `StartStreamInteractiveWithRelay`, `Harness` — `extraEnv` is
  the rider channel; `ClaudeSessionsDir` is deliberately unset in stream mode, which is why
  the e2e re-derives it.
- `internal/e2e/realclaude/testdata/permission_mode_switch_v2.1.239_plan.json` — read as
  DATA for the rider's canned `modelUsage`: the two-genuinely-different-models shape
  (`claude-haiku-4-5-20251001` at 200000 beside `claude-sonnet-5` at 1000000).
- `docs/knowledge/features/contextwindow-package.md` — the package overview. Its § "Context
  window size" currently states "**Deliberately no per-model map** and no `message.model`
  extraction"; that is precisely what this ticket reverses, and the documentation phase owns
  the edit.

## Context

`contextwindow.Read` reports a hardcoded 200K window for every session. A 1M-context session
measured live on 2026-09-04 summed to 223075 used, which against that constant is 111%.
Since #2100 the daemon reports `WindowTokens` 0 rather than a clamped lie — but it still
cannot report the true window.

The two halves arrive on different channels. The used count comes off the transcript file;
the window comes off claude's stdout `result` line, decoded by #2101 and retained per session
by #2106. **The join key is `message.model` on the same transcript entry the used count came
from** — and `internal/agentrun/jsonl` does not decode that field today, so the key is not
available.

Three facts from the ticket's measurement decide the join's shape and are not re-litigated
here: two `modelUsage` entries do not imply two models (26 of 30 committed captures are an
alias pair for one model); no rule relating a dated to an undated spelling survives the data;
and therefore the join is an exact, verbatim string match on the id, with a miss falling back
to the default.

**No ADR is warranted.** This is one join inside an existing seam, and every design rule it
rests on is already recorded — #2101's decode bound, #2106's retention contract, #2100's
contradiction check. There is no choice-between-alternatives here that outlives the ticket.

**Size.** The refiner sized this `s` at ~1000 lines / 5 production files and declared the
overage deliberate. Re-counted against this plan: **7 production files** (the five named,
plus `cmd/pyry/session_model_windows.go` and `internal/e2e/internal/fakeclaude/main.go`) and
~1000 lines of total written work. Both the file line and the line-count line of the size-S
table are exceeded. **The split gate is closed on depth** — `#2107 → #2102 → #2086`, verified
by the parent-chain query — so per the builder's § A1 this is built in place with
`needs-human:sizing` (already applied by the refiner) as the marker. The split I would
otherwise have made is recorded below for that review.

*The split I would have made, had depth allowed:* (A) the key — `Event.Model` on `jsonl` plus
`contextwindow.Read`'s window parameter and the join, both halves unwired; (B) the wiring —
the pool-keyed resolver, both `cmd/pyry` seams, and the fake-claude e2e. **The floor rule
argues against it even so:** (A)'s only consumer is (B), so (A) is not a ticket of its own,
and the floor beats the ceiling.

## Design

Four layers, bottom-up. Each has exactly one consumer above it.

### 1. `internal/agentrun/jsonl` — surface the join key

`rawAssistantMessage` grows a `Model string` field tagged `json:"model"`. `Event` grows:

```go
// Model mirrors message.model verbatim on an assistant entry, or "" when the
// entry has no model field, the value exceeds maxModelIDBytes, or the entry is
// not an assistant entry.
Model string
```

`Next`'s assistant arm sets it from `msg.Model` when `len(msg.Model) <= maxModelIDBytes`, and
to `""` otherwise.

```go
const maxModelIDBytes = 256
```

**The bound is derived, not chosen, and over-cap yields NO KEY rather than a cut one.** This
field's only purpose is to be compared against a key on the other side of a join, and that
side is bounded: `decodeModelWindows` drops any `modelUsage` entry whose key exceeds
`maxModelWindowID` (256), so a retained report can never carry a longer id. An id longer than
256 bytes is therefore a guaranteed miss, and truncating it to its 256-byte prefix would turn
a guaranteed miss into a possible **false match** against a genuinely different model whose id
is exactly that prefix. `maxModelWindowID`'s own doc makes the same argument from the other
side: a cut id names no model, and a consumer cannot tell a cut id from a model it has never
heard of.

**No slicing, therefore no aliasing.** The ticket warns that `streamsup`'s `truncateField`
returns `s[:limit]` unchanged for a valid-UTF-8 prefix, so a "capped" string pins the whole
pre-cap allocation. That trap is structurally unreachable here: this bound never produces a
prefix. The over-cap value is replaced with the empty string, and the under-cap value is the
string `encoding/json` allocated for the struct field.

`jsonl` caps no other decoded string field (`StopReason` rides uncapped), so this is a
departure the field's doc must justify rather than a house rule it inherits. The
justification is the paragraph above: this field is a comparison key, `StopReason` is a
compared-to-a-literal enum.

### 2. `internal/contextwindow` — the join and the ordering

```go
// Read scans the claude transcript at path and reports current context-window
// usage, sizing the window from windows when the latest usage-bearing entry
// names a model it has an entry for.
func Read(path string, windows map[string]int) (Usage, error)
```

`Read`'s scan loop captures the model **on the same branch** that captures the usage block, so
"latest usage-bearing entry" keeps exactly one definition:

```go
if ev.Usage != nil {
    last = ev.Usage
    lastModel = ev.Model
}
```

Resolution, then the check, in that order (AC 4):

1. `window := defaultWindowTokens`
2. if `lastModel != ""`, look up `windows[lastModel]`; on a comma-ok hit with a value above
   zero, `window = that value`
3. `usage.WindowTokens = window`
4. **then** #2100's contradiction check: `if usage.UsedTokens > usage.WindowTokens { usage.WindowTokens = 0 }`

Resolve first and the measured 1M session reports 1000000; check first and it reports 0.

**One guard closes both sides of the empty-id rule.** An assistant entry with no
`message.model` decodes to `""`; #2101 keeps a `modelUsage` entry keyed `""` when its window is
positive, and `modelWindowReport.Windows` sorts it first. The `lastModel != ""` guard means a
lookup for `""` never happens, so a `""` key in the map is unreachable and cannot match. The
rule is enforced once, on the transcript side, rather than spelled twice.

**The `> 0` guard is `Read`'s, not the resolver's.** `windows` is a caller-supplied map on an
exported function, so `Read` defends its own contract; the resolver relies on #2106's
documented invariant instead of restating it. A non-positive value is not a window and is
treated as absent.

**`windows` is read-only and never retained.** `Read` stays safe for concurrent use — it
opens its own file, hangs no shared state off the package, and a nil map is legal and misses
every lookup (which is exactly "nothing observed").

The `path == ""` early return keeps `defaultWindowTokens`: no transcript means no model, so
there is nothing to join on.

Doc corrections in the same file (comment-only): `defaultWindowTokens`' "Sourcing the real
window … is #2101/#2102" becomes this ticket, and `Usage.WindowTokens`' "the believed
context-window size (defaultWindowTokens today)" becomes the observed-or-default reading.

### 3. `cmd/pyry` — the pool-keyed resolver

New file `cmd/pyry/session_model_windows.go`, following the `session_*.go` convention and the
`resolveBoundModelList` precedent minus its conversation hop:

```go
// sessionModelWindows answers the context windows observed for a session id.
func sessionModelWindows(pool *sessions.Pool) func(sessionID string) map[string]int
```

Contract: `Lookup` → type-assert `Session.Runner()` to `interface{ ModelWindows() (modelWindowReport, bool) }`
→ call it → build a fresh `map[string]int` from `report.Windows`. Every refusal — unknown id,
runner without the method, nothing reported — answers **nil**, which `Read` reads as "nothing
observed" with no second spelling. `Dropped` is not consulted: a truncated report's surviving
entries are still true, and a miss already has a safe answer.

- **Isolation (AC 5)** is `Pool.Lookup`'s: the map is built from one session's retention and
  is a fresh allocation per call, so it is never shared between sessions or between callers.
- **The comma-ok is the only filter** — no `ModelID != ""` check and no `WindowTokens > 0`
  check here; both rules live in `Read`, per § 2.
- **SECURITY:** no logger field and no logger parameter, `Lookup`'s error discarded rather
  than wrapped, ids never returned on a refusal — the `resolveBoundModelList` posture
  inherited for its stated reason. Model ids are compared and nothing else; nothing this
  function touches reaches a wire surface.
- **Concurrency:** a synchronous read on the caller's goroutine. Two locks — the pool's and
  the hold's leaf mutex — acquired sequentially and never nested, adding no edge to the
  daemon's lock order.

### 4. `cmd/pyry` — both seams, and the wiring

`snapshot_usage.go` grows one parameter on each builder:

```go
func snapshotUsageFor(dir string, windows func(sessionID string) map[string]int) func(id string) (usedTokens, windowTokens int)
func bootstrapSnapshotUsage(dir string, bootstrapID func() string, windows func(sessionID string) map[string]int) func() (usedTokens, windowTokens int)
```

The by-id closure asks `windows(id)` for the same id it resolved the transcript by, then calls
`contextwindow.Read(path, observed)`. The error recovery stays `contextwindow.Read("", nil)` —
the same deterministic fresh-session report, unchanged.

**`windows == nil` must NOT collapse the seam to nil.** This is the spot where the
neighbouring shape is close enough to be pattern-matched wrong, and `runConfigFor`'s doc
already names the same trap for its own `usage` half: `dir == ""` collapses because with an
empty dir `StatByID` would join a relative path against the daemon's cwd, whereas a nil
window resolver yields a WORKING seam that reports the default window — today's behaviour
exactly. Degrading one integer must not make a resolvable session unresolvable.

`relayWiring` grows one field, built in `main.go` beside `runSettings` and `bootstrapIDFn`
because `startRelayV2` holds no pool reference:

```go
modelWindows func(sessionID string) map[string]int
```

`startRelayV2` threads it into **both** constructions — `bootstrapSnapshotUsage` (feeding
`screen_snapshot`) and the second `snapshotUsageFor` (feeding `runConfigFor`, which
`session_settings` reads). Wiring one and not the other makes the two surfaces disagree, which
is why AC 1 names both. `main.go` sets `modelWindows: sessionModelWindows(pool)`; foreground /
v1 leaves it nil, which § 4's rule turns into today's reading.

### 5. `internal/e2e/internal/fakeclaude` — the `modelUsage` rider

`outResult` grows a `ModelUsage map[string]outModelUsage` field tagged
`json:"modelUsage,omitempty"`, and
`writeStreamResponse` takes that map as a parameter. **nil ⟹ `omitempty` ⟹ the emitted result
line is byte-identical to today's**, so every existing e2e is unchanged by construction rather
than by inspection.

A new rider env `PYRY_FAKE_CLAUDE_STREAM_MODEL_WINDOWS`, non-empty ⟹ on (the
`envStreamWithholdMode` spelling, not `envStreamRoster`'s count — there is nothing here to
count), threaded to `runStreamJSON` as a trailing `bool` after `rosterTasks int`, so a
mis-slotted call-site edit stays a compile error the way that parameter list's doc demands.

The canned value is **the two-entry, two-genuinely-different-models shape read verbatim from
`permission_mode_switch_v2.1.239_plan.json`**: `claude-haiku-4-5-20251001` at `contextWindow`
200000 beside `claude-sonnet-5` at 1000000, with the sibling fields (`inputTokens`,
`costUSD`, `canonicalModel`, `provider`, …) carried across so the harness line is a faithful
copy rather than a minimal one. No live-claude run is needed and none is asked for.

## Concurrency model

No goroutines are spawned, none are joined, and no shutdown path changes.

`Read` remains safe for concurrent use: it opens its own file and reader, retains nothing, and
now additionally reads — never writes — a caller-supplied map. `sessionModelWindows` runs
synchronously on the caller's goroutine (a relay-leg goroutine, with no turn necessarily in
flight, which is exactly the case #2106's hold was built for). Its two locks are acquired
sequentially, never nested: `Pool`'s, released before `Session.Runner()` is asserted and
called, then `sessionModelWindowHold`'s leaf mutex inside `ModelWindows`. Do not restructure
so one is held across the other.

The `Lookup` → `ModelWindows` window is the benign TOCTOU the resolver family already
documents: a rotation landing inside it yields either the window the bound session reported a
moment ago or a miss, and both are correct readings.

The single writer of the retention is the parser's forwarder chain, serialised across
respawns; this ticket adds the reader that hold's mutex was introduced for.

## Error handling

| Failure | Reading |
|---|---|
| Transcript path unresolvable / not yet written / raced away | unchanged — `Read("", nil)`'s fresh-session report (zero used, default window) |
| Scan or open failure on a resolved path | unchanged — error discarded, collapsed to the fresh-session report; the path never surfaces |
| `Pool.Lookup` fails, runner lacks `ModelWindows`, nothing reported yet | nil map ⟹ default window ⟹ **today's reading**, including #2100's collapse to 0 when the used count exceeds it (AC 3) |
| Latest entry names a model the report has no entry for | same as above — a miss, not an error |
| Latest entry has no model (`""`) | miss; the lookup never happens |
| Retained entry keyed `""` | unreachable — nothing looks it up |
| `message.model` over 256 bytes | `Event.Model` is `""` ⟹ miss; never a truncated key |

Nothing on this path grows an error return, and nothing grows a log line.

## Testing strategy

**`internal/agentrun/jsonl` (`reader_test.go`)** — scenarios: an assistant entry with
`message.model` surfaces it verbatim; an assistant entry without one surfaces `""`; a
non-assistant entry surfaces `""`; a model id of exactly 256 bytes survives whole; one of 257
bytes surfaces `""` **and not a 256-byte prefix** (the assertion must pin the empty string, or
a truncating implementation passes).

**`internal/contextwindow` (`usage_test.go`)** — extend `TestRead_Fixtures` with a `windows`
column, plus rows for:
- the join: `latest_turn.jsonl` (names `claude-opus-4-7`) against a map giving it 1000000
  reports that window;
- the miss: the same fixture against a map naming only a *different* model reports
  `defaultWindowTokens`;
- **AC 2, the key** — a new fixture whose two usage-bearing entries name two different models
  at two window sizes, run twice against the same map with the entry order reversed, so the
  answer moves with which model the LATEST entry names. This is the row that kills "the
  largest", "the first" and "the only";
- **AC 2, the alias pair** — one map carrying both spellings of one model at the same window,
  asserted under either spelling;
- **AC 4, ordering** — `over_window.jsonl` (223075 used, names `claude-opus-5`) reports 1000000
  against a map giving that model 1000000, and 0 against a map giving it 200000. The second
  row is what fails if the check runs before the resolve;
- **the empty-id rule, both directions** — a fixture entry with no `message.model` against a
  map carrying a `""` key with a positive window reports the default, and a named entry
  against a `""`-keyed map likewise;
- `Read(path, nil)` on every existing fixture reproduces today's numbers exactly (the
  regression floor for AC 3).

**`cmd/pyry` (`snapshot_usage_test.go`)** — the seam threading: a by-id reader built with a
window func reports the observed window for the id it was asked about; built with `nil` it
reports the default (the must-not-collapse rule); `bootstrapSnapshotUsage` still returns a nil
seam for an empty dir or a nil id source, and a working one with a nil window func. Isolation:
a window func that answers for one id only leaves a sibling id on the default.

**`cmd/pyry` (`session_model_windows_test.go`, new)** — the resolver against a fake runner:
a runner holding a report yields the map; a runner without the method, an unknown id, and a
never-reported hold each yield nil. A fake pool is not available, so this drives
`sessions.Pool` the way the package's other resolver tests do.

**e2e (`internal/e2e/relay_v2_stream_model_window_test.go`, new)** — one test, the assertion
AC 1 names. Start the stream-interactive harness with the rider env; seed a conversation;
`request_session_settings` to learn its session id; write a transcript at
`claudeSessionsDir(home)/<sessionID>.jsonl` whose latest usage-bearing entry names
`claude-sonnet-5` and sums to **223075**; drive one turn and wait for `turn_end` (the rider
writes before the reply, so `turn_end` implies the hold already holds the report — the
happens-before #2080's e2e uses, no sleep); re-request and assert `used_tokens == 223075` and
`window_tokens == 1000000`.

The fixture is stronger than the assertion count suggests: the rider reports TWO models at two
sizes, so a 1M answer also disproves "the first", "the only" and "the largest-that-fits".

**Why the e2e proves `session_settings` and not `screen_snapshot`.** In stream mode
`handleRequestSnapshot` short-circuits to `server.binary_offline` — cmd/pyry routes the
typed-nil supervisor to a nil `Snapshotter` on purpose — so there is no screen to snapshot and
no reply to assert against, as `TestRelayV2_StreamRequestSessionSettings`' own doc records.
The `screen_snapshot` half is proven at its seam instead: `bootstrapSnapshotUsage` is unit
tested above, and the two seams are constructed side by side in `startRelayV2`, both fed the
same resolver.

**Not run by me:** the full-module race suite and `make check` are the verifier's gate. My gate
is `go test -race` on the touched packages plus `go vet ./...` and `go build ./cmd/pyry`.

## Open questions

1. **Does `writeStreamResponse` have callers beyond `runStreamJSON`'s default arm?**
   `writeVerdictResponse` builds its own `outResult` rather than calling it, and
   `writeInterruptedResult` likewise, so the parameter addition looks confined to one call
   site. Confirmed by `git grep` in Phase B; any additional caller passes nil and stays
   byte-identical.
2. **Does `cmd/pyry`'s test surface already have a fake runner exposing `ModelWindows`?**
   #2106 landed `sessionModelWindowHold`, which satisfies the assertion directly and may serve
   as the fake. If it does, the resolver test uses it rather than minting a second double.

Each is resolved in Phase B; a resolution that changes the design above lands as a
`## Revisions` entry in the same commit as the code.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** SHOULD FIX — two untrusted values cross into this design, and one of
  them crosses at a NEW boundary. `message.model` crosses file → memory at exactly one point,
  `jsonl.Next`'s assistant arm, where `maxModelIDBytes` is applied at construction so an
  oversized value never enters an `Event` (the `maxModelWindowID` doctrine, inherited). The
  `modelUsage` keys crossed subprocess-stdout → parent state at #2101 and are re-read here.
  The finding is on the *type signal*: `modelWindowReport.Windows` carries an explicit
  SECURITY paragraph naming the two obligations that ride the value, and a bare `Model string`
  on `jsonl.Event` would carry none — a later consumer could read it as ordinary decoded text.
  **Phase B: `Event.Model`'s doc must state that the value is claude-authored, unsanitized,
  may carry control characters or terminal escapes, and is a comparison key rather than
  renderable or executable text.**

- **[Tokens, secrets, credentials]** No findings, and not by absence of a checklist item: this
  design generates, stores, compares and rotates nothing secret. A model id grants no
  capability and is not consulted in any authorization decision. The one disclosure question —
  does reporting a true `window_tokens` tell a paired device something new — resolves to no:
  the same reply already carries the session's model on `SessionSettingsPayload.Model` via the
  `runSettings` half, so the model tier is not newly disclosed.

- **[File operations]** No findings — **this ticket writes no file and resolves no path.** The
  transcript path is still resolved by `transcript.StatByID`, whose stem validation runs
  before any join and which this ticket does not touch. The session id `sessionModelWindows`
  receives is joined into no path at all: it is a `Pool.Lookup` key. The model id reaches no
  path-shaped API — it is a `map[string]int` key, and map keys carry no path semantics. The
  pre-existing stat-then-open TOCTOU is unchanged and its documented recovery
  (`Read("", nil)`) still covers the raced-away case.

- **[Subprocess / external command execution]** No findings, and the closure is STRUCTURAL
  rather than a promise. `sessionModelWindowHold.ModelWindows`' doc warns that an id is not an
  argv token and that joining one back as `--model <id>` would be argument injection into the
  daemon's own child. The design closes that channel by narrowing: `Event.Model` is consumed
  only by `contextwindow.Read`, and `contextwindow.Usage` deliberately carries **no** model
  field, so the id has no route out of `Read` at all — two ints leave, nothing else.
  **Phase B: state that deliberate absence in `Usage`'s doc.** Adding a `Usage.Model` "for
  diagnostics" is the single edit that would reopen both this channel and the render channel
  below, and it must be visibly a decision rather than an omission. The fake-claude rider
  emits a canned constant under an env gate and composes no argv.

- **[Cryptographic primitives]** Not applicable, stated rather than skipped: no randomness, no
  hashing, no key material. The id comparison is a plain map lookup and MUST NOT be made
  constant-time — both operands are non-secret, and `crypto/subtle` here would be cargo-cult.

- **[Network & I/O]** No findings on bounds; every quantity this design introduces is capped.
  The per-entry text bound is `maxModelIDBytes` (256, derived from the producer's
  `maxModelWindowID`); the line bound is `jsonl`'s pre-existing `maxLineBytes`; the map's
  cardinality is `streamsup`'s `maxModelWindowEntries`, applied at construction upstream. The
  scan is O(1) in retained memory regardless of transcript length — `Read` keeps one
  `lastModel` string, overwritten per usage-bearing entry, never a per-entry collection. The
  pinning trap the ticket names is unreachable: this bound never produces a prefix, so there
  is no `s[:limit]` aliasing the decoder's buffer. Cost per request is one extra map
  allocation of at most `maxModelWindowEntries` entries, beside a file open and a full
  transcript scan that already dominate it — no new rate vector.

- **[Error messages, logs, telemetry]** SHOULD FIX, on a *decode-widening* consequence rather
  than a disclosure. Disclosure first: no new error return exists anywhere on this path,
  `sessionModelWindows` DISCARDS `Pool.Lookup`'s error rather than wrapping it (the
  `resolveBoundModelList` rule, whose reason is exactly that wrapping reflects a hostile id
  into whatever a caller builds from it), neither new function takes a logger and neither may
  grow one, and `jsonl.logMalformed` records an offset plus `err.Error()`, which for a type
  mismatch names the struct field path and not the offending value. The finding: adding
  `Model` to `rawAssistantMessage` widens what a malformed `message` object can abort. Today
  a non-string `model` is skipped as an unknown field; after this change it fails the
  unmarshal, so the entry is logged-and-skipped and **its usage block is lost**, and `Read`
  reports an older entry's used count. Consequence is degraded-toward-fresh-session, never a
  wrong-and-confident answer, and #2100's collapse still applies. It is accepted rather than
  engineered around — `StopReason` carries the identical risk on the same struct, and a
  `json.RawMessage` two-step for a shape absent from all 742 measured entries would be more
  code and an inconsistency. **Phase B: pin it with a test** so the behaviour is chosen rather
  than stumbled into.

- **[Concurrency]** No findings. Nothing is spawned, so nothing can leak; nothing is
  persisted, so no partial state survives a signal. There is no check-then-mutate — the whole
  path is a pure read. The two locks (`Pool`'s, then `sessionModelWindowHold`'s leaf mutex
  inside `ModelWindows`) are acquired SEQUENTIALLY and never nested, so no edge is added to the
  daemon's lock order; `Session.Runner()` is read outside the pool lock on
  `resolveBoundModelList`'s stated precedent that it is assigned once at construction and never
  reassigned. The `Lookup` → `ModelWindows` window is a benign TOCTOU: both outcomes are
  correct readings. No shared mutable state crosses into `Read` — the map is freshly allocated
  per call, never retained by `Read`, never handed to a second goroutine — and `Read`'s doc
  says so, so a later caller does not pass a shared map and then mutate it.

- **[Threat model alignment]** No findings. Against `docs/protocol-mobile.md` § Security
  model, the two relevant threats are a paired device learning more than it should and
  claude-authored text reaching a wire surface unsanitized. The first is addressed above. The
  second is closed by design: **no new wire field is added** — `window_tokens` is an existing
  integer on both `session_settings` and `screen_snapshot` and only its value changes — and
  the model id is deliberately not added to either payload. That discharges #2106's
  sanitization obligation by narrowing rather than by passing it on: no client inherits a
  render obligation from this ticket, because no client receives the text.

- **[Threat model alignment]** OUT OF SCOPE — persisting the observed window with the session
  record, which would close the post-daemon-restart gap where the report falls back to the
  default. The ticket defers it explicitly on the grounds that no complaint has been observed
  about the sub-200K restart gap and that #2100 already refuses to report a clamped lie above
  it. No successor ticket exists yet; it is named here so whoever files one finds the
  reasoning.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-05

## Revisions

### 2026-09-05 — implementation

**The resolver's file is `cmd/pyry/session_model_window_lookup.go`, not `session_model_windows.go`.** Go applies an implicit GOOS build constraint to any file whose name ends `_windows.go`, so the plan's name compiled the resolver on Windows only — `sessionModelWindows` was undefined everywhere else and the package did not build. The symbol keeps the plan's name; only the file is renamed, and its test with it (`_windows_test.go` carries the same constraint). Nothing about the design moves.

**Open question 1 resolved: `writeStreamResponse` has two callers**, `runStreamJSON`'s default arm and `TestWriteStreamResponse_Shape`. `writeVerdictResponse` and `writeInterruptedResult` build their own `outResult` and are untouched. The test passes nil and now also asserts the emitted line carries no `modelUsage` key at all, which is what makes "byte-identical when the rider is off" a checked property rather than a claim about `omitempty`.

**Open question 2 resolved: no reusable fake existed**, so `session_model_window_lookup_test.go` mints `modelWindowsRunner` / `modelWindowsPlan` / `newModelWindowsTestPool`, the `modelListRunner` family's shape. `sessionModelWindowHold` satisfies the assertion but is a *sink decorator* fed by the parser chain, not a `sessions.Runner`, so it cannot be handed to a `RunnerFactory`; the arm-by-id plan is needed for the same reason its precedent gives — `sessions.New` builds the bootstrap runner before the test can know that session's id.

**Two additions the plan did not name, both from the security review's SHOULD FIX items.** `jsonl`'s `TestReader_NonStringModelSkipsTheEntry` pins the widened-decode failure mode; `contextwindow.Usage` carries an explicit comment stating that it deliberately has no model field, and why adding one would reopen both the render and the argv channel.

**One test-design correction made during implementation.** The bound's own rows were first written as `maxModelIDBytes` and `maxModelIDBytes+1`, which moves fixture and expectation in lockstep and lets a mutant that retunes the constant survive green. They are literals 256 and 257, which is what pins the number to `streamsup`'s `maxModelWindowID`.

**The e2e was verified non-vacuous by mutant** rather than by inspection: with `startRelayV2` passing `nil` in place of `w.modelWindows` to the `session_settings` seam, `TestRelayV2_StreamSessionSettingsReportsTheObservedWindow` fails with `window_tokens = 0, want 1000000`. The mutant was reverted.
