# `system/thinking_tokens` → `turnevent.ThinkingProgress` is rate-bounded, not 1:1 (#1385)
**`system/thinking_tokens` → `turnevent.ThinkingProgress` is rate-bounded, not 1:1 (#1385).** claude's
`estimated_tokens_delta` is accumulated in a new unexported `Parser.thinkingSinceEmit int` field and an
event is emitted once the accumulated delta crosses `minThinkingTokensPerEvent` (64) — most lines
consume silently. The bound keys on the per-line **delta**, not the cumulative `estimated_tokens`
counter, because that counter restarts near zero at every inference-request boundary within one turn: a
high-water-mark rule over the cumulative counter goes silent for a whole burst (the committed capture's
burst 2 never exceeds burst 1's peak), while a delta-accumulator only ever grows and so is immune by
construction. The crossing test is written subtracted (`d >= bound - acc`), never additive (`acc +=
d; if acc >= bound`) — the additive form overflows on an extreme `estimated_tokens_delta` and silently
kills the turn's liveness signal for the rest of the turn; subtracted, both operands stay in `[1, bound]`
by the invariant `acc ∈ [0, bound-1]`, so the failure is unrepresentable. `thinkingSinceEmit` resets
unconditionally on `consumeLine`'s `result` arm (both result subtypes) — the parser's one turn boundary.
See [codebase/1385.md](../codebase/1385.md).

**`rate_limit_event` → `turnevent.RateLimited` is the fifth mapping, the first that is not a `system`
subtype, and its substance is a gate rather than the field copy (#1404).** claude emits this line **once
per run** whatever the state of the usage-limit window — `status` reads `"allowed"` in all three captures
on record (three claude versions), i.e. every run that produced one hit no limit at all — so a 1:1
mapping would put one "you are rate limited" event on every healthy turn. `status` is the discriminator,
with three reachable readings, all decided and stated at `emitRateLimit`: (1) `status ==
benignRateLimitStatus` ("allowed") → silence; (2) `status` non-empty and not the benign value → one
`RateLimited`, because the failure direction is the safe one — an unrecognised status surfaces and a human
looks, rather than a real limit vanishing; (3) `rate_limit_info` absent, present-but-empty, or the line
fails to decode → silence, **not** emit — the naive "emit unless status is allowed" reading is rejected,
because an absent container answered with "emit" turns a container rename into a per-run noise row on
every healthy run forever (the worse failure), whereas answering it with silence produces a false negative
on a condition that has never fired once in three captures. The cost is named rather than hidden: a
container rename is undetected by any automatic test, and the only backstop is the live drop census (the
same one that surfaced this payload in the first place, #1260) still recording the dropped line's shape.
`RateLimited{Status, LimitType, ResetsAt int64, TruncatedFields}` — `Status`/`LimitType` bounded by
`maxRateLimitField` (256, ~28× the observed 7–9 byte values — wide because the value set beyond the one
benign status is unmeasured); `ResetsAt` is claude's unix-seconds number passed through **unbounded and
unvalidated in both directions** (not `time.Time` — converting would invent a claim the bytes do not
make). No rate bound: once-per-run is measured, not enforced, so an adversarial or buggy claude emitting
many non-benign lines produces many events (an accepted, named exposure, not a mechanised one, per
evidence-based fix selection). The variant is a **report, never a control input** — nothing in the daemon
may key a behaviour on it. `session_id`/`uuid` are absent from the decode target itself, as with the
background-task family; so are the payload's four `overage*` keys, two of which are measured
version-variable across claude 2.1.158/2.1.199/2.1.220. The drop site logs one message
(`rateLimitDropMsg`) with a `reason` drawn from a closed keyword set — `Status` never reaches a log, since
it is the one claude-authored value on this line and the field a drop site is most tempted to explain
itself with. `cmd/pyry/interactive_turn_v2.go`'s `eventKind` gained a fifth mapped arm
(`turnevent.RateLimited → "rate_limited"`, name only); `acpbridge.MapUpdate` and `stream_turn_busy.go`'s
opener whitelist correctly drop it through their existing `default:` arms — the ACP/desktop lane is
deliberately untouched (#1262) and a usage-limit report opens no turn. `turnbridge.MapEvent` no longer
drops it: the wire type landed in #1405 and the mapping arm in #1410, so the variant now reaches an
interactive v2 mobile client instead of falling to `default:`. See [codebase/1404.md](../codebase/1404.md),
[codebase/1405.md](../codebase/1405.md), [codebase/1410.md](../codebase/1410.md).

**`system/init` → `turnevent.ModelAnnounced` is the sixth mapping, a fifth `system` subtype, and the
first that reports what the daemon itself ASKED FOR versus what claude actually RAN (#1600).** The
daemon's two existing `model` payloads (`protocol.ScreenSnapshotPayload`, `protocol.SessionSettingsPayload`)
both mean the per-session override — `""` means "inherited default, no override" — so in the ordinary
case the daemon publishes an empty string while claude has named a concrete model on every turn.
`init` fires once per **turn**, not once per session (#1582 measured three in one session, because a
`/model` turn emits its own `init` and that one still reports the OLD model — a consumer that latches
the first announcement shows a stale value; documented as a hazard on the variant, not mechanised).
`systemInitLine{ Model string }` decodes exactly one of the captured line's 22 keys — `cwd` (the
operator's filesystem path) and `session_id` (claude's session identity, not the daemon's conversation
identity) are the two omissions named specifically, absent from the decode target itself rather than
merely unlogged. `Model` is claude's identifier **verbatim** — no lowercasing, alias expansion,
date-stamping, family mapping, or list lookup — bounded at construction by `maxModelField` (256, ~10×
the observed maximum across three spawn shapes: claude dates a bare family alias it's given but passes
an already-specific one through unchanged, so the value is not reliably dated and need not appear in any
published model list). An absent, empty, or undecodable `model` is consumed with no event and (absent/empty)
no log at all — the empty case diverges from the task handlers' emit-with-empty-field rule, following
`emitRateLimit`'s `case ""` suppress-on-empty precedent instead, because the model *is* the whole payload
here. Only the undecodable path logs, and only the subtype keyword — never `err`. CORRECTED 2026-08-31
(#1878): this used to justify that with "`encoding/json` quotes offending input into its error text."
Measured false, by mutation, on the sibling undecodable arm in `emitModelList`: a type-mismatch error
carries only the Go type path (`json: cannot unmarshal number into Go struct field ... of type
[]streamsup.modelOptionLine`), never the field's bytes — `encoding/json` quotes input only in narrow
cases (one character in a `SyntaxError`, the literal in a numeric overflow), never a string field's
contents, and `Model` is a string field. `err` still stays out of the log, but for the narrower reason:
those two quoting cases are real, just rarer than the blanket claim implied, and `Model` is single-field
here so there's no sibling value a partial decode could leave populated to leak instead (contrast
`commands`, below, where a decode failure on `models` still leaves `Commands` populated).
`cmd/pyry/interactive_turn_v2.go`'s `eventKind` gained a sixth mapped arm
(`ModelAnnounced → "model_announced"`, name only), and #1638 gave it a `Handle` arm so it no longer lands
in `Handle`'s `default`. CORRECTED 2026-08-19 (#1616): this used to say the protocol type and the
`turnbridge.MapEvent` case would land together "in a later ticket" — they didn't. #1616 declared
`protocol.TypeModelAnnounced` / `protocol.ModelAnnouncedPayload` alone, the same declare-then-emit split
`rate_limited` used (#1405 ahead of #1410); `turnbridge.MapEvent` no longer drops the variant, because the
mapping arm landed in sibling #1638, so it now reaches an interactive v2 mobile client instead of falling
to `default:`. See [codebase/1600.md](../codebase/1600.md).

**Kind-log capture tests in `interactive_turn_v2_test.go` must strip slog's own `time=` attribute before
asserting bare numeric needles.** `TestInteractiveTurnEmitterV2_ThinkingProgressEventKindNamesTheVariant`'s
negatives were `strings.Contains` over the whole captured `slog.TextHandler` record, which writes `time=`
first; the `ThinkingProgress` readings' numeric needles (`184`/`37`, for `EstimatedTokens`/
`EstimatedTokensDelta`) can match the timestamp's own digits instead of a real leak — measured at ≈5% of
runs (not the ~2% first estimated from a single `-count=N` burst, which structurally can't observe the
two 1-in-60 minute/second collision terms), and 100% of runs whose log instant lands in minute or second
`:37` (#1758). The `RateLimited` (#1410) and `ModelAnnounced` (#1600) kind-log tests already carried a
`ReplaceAttr` dropping `slog.TimeKey` for this exact reason; #1758 applied the same closure to the
`ThinkingProgress` test and added an explicit `time=`-absence assertion so a future removal of the
`ReplaceAttr` is red on every run instead of on the unlucky ~1-in-20. Only three of the file's six
capture-logger construction sites need this — the three whose readings loop asserts bare numeric needles;
the other three assert alphabetic sentinels that cannot collide with a timestamp and are deliberately left
without it.

Every claude-derived field is truncated **at construction**, mirroring `maxUnrecognizedRaw`'s
cap-at-construction precedent, with each cut named in `TruncatedFields`. The two scalar events share
`maxTaskFieldID` (256) / `maxTaskDescription` (4096) / `maxTaskPatch` (4096). `BackgroundTaskRoster`
needed a second, genuinely new dimension: the `tasks` array's **length** is claude's to choose, so a
per-entry text cap alone leaves the event's total size unbounded. It is bounded in both dimensions —
`maxTaskRosterEntries` (8) on the entry count, with overflow reported as `DroppedTasks` on the event
rather than a per-entry field, and `maxTaskFieldID`/`maxTaskRosterDescription` (512 — a smaller budget
than the scalar `Description`'s 4096, because this is the one field in the family whose budget is
multiplied by a count claude chooses) on each entry's text, reported per entry in that entry's
`TruncatedFields`. That forced a qualification of the family's stated single-worst-case doctrine
(`maxTaskPatch`'s comment: one number a reader can hold) — an aggregate variant cannot share a scalar
variant's worst case unless its cardinality is 1, so the doctrine now reads one worst case **per shape**:
the scalar pair ≤ ~4.9 KB, the roster ≤ 8 KiB (12.5% of the 65519-byte v2 application-envelope cap),
rather than forced to fit or silently abandoned.

claude's `session_id` and `uuid` are deliberately not carried by any of the four — absent from the
decode targets themselves (a field never declared cannot leak), enforced further by a reflection sweep
in each mapping test. No terminal/finish event is ever synthesized for a background task: the parser
holds no roster and no per-task memory, and a task's disappearance from a later roster — the only
available finish signal — has never been observed, so `BackgroundTaskRoster` reports the snapshot and
nothing more. (`thinkingSinceEmit`, #1385's token counter, doesn't change this refusal — it remembers no
task and no roster, only a count reset at the turn boundary.) Field mapping and cap arithmetic for the
three text-bearing events are built from the same committed capture (#1260), never a hand-built line; the
two bounds `BackgroundTaskRoster` needs are proven by lines synthesized to exceed them, since the
capture's single 212-byte, one-entry roster is far under either cap. `thinking_tokens`' bound is proven
the same way — the capture's bursts (126–197 tokens of delta) prove the bound's *ceiling*, but crossing
it repeatedly needs synthesized lines. See [codebase/1380.md](../codebase/1380.md),
[codebase/1382.md](../codebase/1382.md), [codebase/1381.md](../codebase/1381.md),
[codebase/1385.md](../codebase/1385.md).
