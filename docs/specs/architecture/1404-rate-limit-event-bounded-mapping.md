# #1404 — Map `rate_limit_event` to a bounded `turnevent.RateLimited`

Fifth mapping in the family #1380 opened, and the **first that is not a `system` subtype**. It moves
`rate_limit_event` off `ignoredLineTypes` and gives it its own arm in `consumeLine`'s main switch,
alongside `assistant` / `user` / `result`. Carrying it to a client is #1405 and is not in scope.

The substance of this ticket is not the mapping — that part is routine — it is the **gate**. claude
emits this line once per run whatever the rate-limit state, so a 1:1 mapping produces one event on
every healthy run. Two rungs of that gate are decided below and both are stated at the code.

---

## Files to read first

Turn-1 data load. Read these before writing anything; they answer every structural question this
spec relies on.

**The mapping's shape (the house pattern this one copies):**

- `internal/streamsup/parser.go:696-732` — `emitBackgroundTaskStarted`. The canonical emitter: local
  `json.Unmarshal(line, ...)` on the **top-level bytes**, the `bound()` closure feeding
  `TruncatedFields` in call order, undecodable → content-free `Debug` + consumed. Copy this shape.
- `internal/streamsup/parser.go:1022-1031` — `truncateField(s, limit) (string, bool)`. The one cut
  helper; note the UTF-8 scrub it performs and the `json.RawMessage` exception that does **not**
  apply here (both new fields are plain strings).
- `internal/streamsup/parser.go:47-64` — `maxTaskFieldID`'s doc. The "multiple of the observation +
  envelope arithmetic" comment shape the new cap must follow.
- `internal/streamsup/parser.go:102-105` — the "separate constant even though it currently equals X"
  paragraph, verbatim reusable for why the new cap is not `maxTaskFieldID`.
- `internal/turnevent/event.go:106-160` — `BackgroundTaskStarted` + `BackgroundTaskUpdated` docs.
  The variant-doc shape: why the name is the daemon's, the deliberate `session_id`/`uuid` drops, the
  "bounded by the producer AT CONSTRUCTION" sentence, the `TruncatedFields` nil-not-empty contract.
- `internal/turnevent/event.go:234-268` — `BackgroundTaskRoster`'s doc, specifically the paragraph on
  why claude's `…_changed` trigger-name was rejected for a payload-shaped name. The new variant's
  name rests on the same argument (§ Naming).

**The sites this ticket changes:**

- `internal/streamsup/parser.go:211-285` — `ignoredLineTypes` and its doc. `:227` (census row, stays
  true), `:256` and `:259` (both go false), and the literal at `:282-285`.
- `internal/streamsup/parser.go:572-643` — `consumeLine`. The main switch is where the new arm goes;
  `:606-638` is the drop branch, `:610-612` the `sl.Type == "system"` guard whose stated reason
  evaporates, `:617` the drop comment.
- `internal/turnevent/event.go:403-436` — `Unrecognized`'s doc. `:408-411` goes false; `:414` is a
  historical quote and stays.
- `cmd/pyry/stream_turn_busy.go:130-146` — `observe`'s whitelist rationale. Goes false **and**
  carries a stale cite (`parser.go:158-163`; the list is at `:282-285`).
- `cmd/pyry/interactive_turn_v2.go:455-504` — `eventKind`. Needs a fifth mapped arm; read
  `ThinkingProgress`'s arm at `:491-500` for exactly why the arm exists despite this file's own
  `default`.
- `internal/streamsup/watchdog.go:143-171` — the stall tracker's own switch. `:169` names
  `rate_limit_event` and **stays true**: this tracker decodes only the top-level `type` and matches
  no case for it. Do not touch.

**The realclaude mirror (build tag `e2e_realclaude`):**

- `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:180-204` — `parserIgnoredTypes`; the
  entry at `:203` and its comment at `:198-202` go.
- `…:1167-1207` — `parserIgnoredTypeFixtures`; the `rate_limit_event` entry at `:1204-1206` **must go
  too**. See § The fifth mirror obligation.
- `…:1250-1302` — `TestParserIgnoredTypesMatchesStreamsupParser`, including the orphan-fixture loop at
  `:1288-1292` that forces the above.
- `…:86-102` — `expectedStreamRunnerOnly`; the `rate_limit_event` entry at `:88` **stays**, comment
  unchanged.
- `…:352-363` — `shapeFilterDrops`. Note the order: the one-sided tables are consulted **before**
  `parserDropsShape`, which is why the sequence comparison is unaffected.
- `…:1054-1124` — `TestExtractShapes_FiltersParserIgnoredTypes`. Stays green unchanged; `:1061`'s
  comment stays true. Do not "fix" it.
- `internal/e2e/realclaude/dropped_line_capture_test.go:1053-1074` — `dropcapClassify`. The row's
  verdict is **derived from the shipped parser**, not declared.
- `…:1498-1560` — `TestDropcapClassification`; the `rate_limit_event` row at `:1519-1522`.
- `internal/e2e/realclaude/interactive_stream_unrecognized_test.go:5-42` — `:9` goes false, `:34`
  (line inventory) stays true.

**The evidence:**

- `internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json:56-65` — the capture record. Note
  `payload_encoding: "json-string"` and **no `subtype` key**.
- `internal/streamsup/capture_test.go:36-112` — the capture reader. It is hard-scoped to
  `rec.Type != "system"` and therefore **cannot read this line today**; § The capture reader says what
  to do.
- `internal/streamsup/parser_test.go:550-571` — `taskStartedCapCheat`. The cap-fixture-as-literal
  doctrine the new cap test must follow.
- `internal/streamsup/parser_test.go:655-690` — the `session_id`/`uuid` leak sweep to mirror.

---

## Context

`rate_limit_event` is claude's once-per-run report of the usage-limit window. Today it is one of the
two members of `ignoredLineTypes` and dies in a `Debug` line the production daemon never prints. This
slice makes the daemon understand it. #1405 carries it to a client.

### Correcting the ticket body's evidence — read this before designing against it

Two numbers in the ticket body are wrong. The **design does not change**, but the spec must not repeat
them and neither should any doc comment the developer writes.

- The body says **four captures**. There are **three**, across three claude versions:
  `dropped_lines_v2.1.220.json`, `permission_protocol_v2.1.158.json`,
  `permission_protocol_v2.1.199.json`. The body's fourth cite, `permission_protocol_v2.1.220.json:198`,
  **names a file that does not exist** — the only 2.1.220 capture is `dropped_lines_*`, already cited
  as the first. Verified by grepping `rate_limit` across all of `internal/e2e/realclaude/testdata/`.
- The body says **four keys are present in every capture** (`status`, `resetsAt`, `rateLimitType`,
  `isUsingOverage`). There are **five** — `overageStatus` is present in all three as well
  (`"allowed"` in 2.1.158, `"rejected"` in 2.1.199 and 2.1.220).

The measured picture, in full:

| key | 2.1.158 | 2.1.199 | 2.1.220 |
|---|---|---|---|
| `status` | `"allowed"` | `"allowed"` | `"allowed"` |
| `rateLimitType` | `"five_hour"` | `"five_hour"` | `"five_hour"` |
| `resetsAt` | 1783073400 | 1783622400 | 1785699000 |
| `overageStatus` | `"allowed"` | `"rejected"` | `"rejected"` |
| `isUsingOverage` | `false` | `false` | `false` |
| `overageResetsAt` | 1783063200 | — | — |
| `overageDisabledReason` | — | `"org_level_disabled"` | `"org_level_disabled"` |

Union: 7 keys. Always present: 5. Version-variable: the two overage-detail keys. The body's headline
conclusions all hold — container key `rate_limit_info` stable, `status == "allowed"` in every capture,
`rateLimitType` and `resetsAt` present and meaningful everywhere.

---

## Design

### The variant — `turnevent.RateLimited`

```go
type RateLimited struct {
    Status          string
    LimitType       string
    ResetsAt        int64
    TruncatedFields []string
}
```

Declared after `ThinkingProgress` (`event.go:356`); marker added to the block at `:447-459` in the
same position.

**Naming.** `RateLimited`, not `RateLimitEvent` (claude's message name) and not `RateLimitStatus`.
The argument is `BackgroundTaskRoster`'s, applied one step further: a name ending in `Status` invites a
consumer to read the variant as a periodic report of the rate-limit window, which is exactly what the
gate below exists to prevent it from being. This variant fires **only when a limit is in force**; the
name states that condition, and it sits with the family's other condition-named variants (`Stall`,
`Compacting`, `ApiRetry`).

**Fields.**

- `Status` — claude's own `rate_limit_info.status`, verbatim and bounded. It is on the event because
  it is the only field that says *why* the event fired, and its value set beyond `"allowed"` is
  **unmeasured**: no capture of a limit in force exists. Putting the raw string on the event is how the
  set gets measured the first time a real limit fires, without the daemon inventing an enum it has no
  evidence for. Empty is unreachable by construction — the gate does not emit on an empty status.
- `LimitType` — claude's `rateLimitType` (`"five_hour"` in all three captures), bounded. The name is
  translated: `RateLimitType` inside a type called `RateLimited` stutters, and the daemon's snake_case
  name for `TruncatedFields` purposes is `"limit_type"`. A plain string, not a closed enum, for
  `BackgroundTaskStarted.TaskType`'s reason.
- `ResetsAt` — claude's `resetsAt`, **unix seconds as `int64`**, 0 when claude did not report it. Not
  `time.Time`: converting invents a claim the bytes do not make (that the number is a valid instant),
  creates a second absent-value question (`time.Time{}` vs 0), and drags in
  PROJECT-MEMORY's `time.Time`-round-trip discipline for a field that is only ever a number on a
  wire. Document that it is **claude's** number, not the daemon's clock, and that it is unvalidated in
  **both** directions: a consumer must not assume it is in the future, and must not assume it is in a
  sane range at all (negative, zero, and year-40000 values are all representable and none is rejected
  here, because rejecting one would be a validation rule with no captured negative case behind it).
  `int64` rather than `int` because a unix timestamp is a 64-bit quantity by nature; a value too large
  for `int64` fails the whole-line decode and takes the undecodable arm, exactly as
  `systemTaskUpdatedLine.TaskID` does for a numeric task id.
- `TruncatedFields` — the house contract, nil when nothing was cut. Unlike `ThinkingProgress` this
  variant **does** carry one: two of its three payload fields are claude-authored strings that can be
  cut, so a report of the cut is not a bound that does not exist.

**Deliberately absent, and say so in the doc:**

- `session_id` and `uuid` — the family's two standing drops (#1380), absent from the decode target
  itself, which is stronger than the test's reflection sweep.
- The four overage keys. `overageResetsAt` and `overageDisabledReason` are **measured
  version-variable** (table above), and `overageStatus` / `isUsingOverage` are org-policy detail that
  nothing in the daemon or in the user story reads. Declaring version-variable keys in a decode target
  is the "inventing field structure" the family's targets each refuse. Name this as a deliberate drop
  so #1405 does not re-litigate it.

**It is a REPORT, never a control input, and the doc must say so.** Nothing in the daemon may key a
behaviour on this variant — no backoff, no throttle, no retry, no turn suspension, no reconnect delay.
Every field on it is claude-authored text or a claude-authored integer crossing the subprocess trust
boundary, and today the only thing downstream of it is display (#1405). That is what keeps a wrong or
hostile `status` value costing at most one misleading row rather than a resource action the daemon
takes on itself. A future slice that wants the daemon to *act* on a rate limit is re-opening the trust
analysis, not extending this one — write that sentence into the variant's doc so it is met by whoever
tries.

### The gate — two rungs, both decided here

`status` is the discriminator. It has three reachable readings and each gets an explicit answer.

| rung | condition | verdict |
|---|---|---|
| 1 | `rate_limit_info` decoded, `status == "allowed"` | **silence** |
| 2 | `rate_limit_info` decoded, `status` non-empty and not `"allowed"` | **emit one `RateLimited`** |
| 3 | `rate_limit_info` absent, empty, or the line will not decode into the shape | **silence** |

**Rung 2 — confirming the ticket's recommended posture.** Emit for anything that is not the one
measured-benign value. The failure direction is the safe one: an unrecognised status surfaces and a
human looks, rather than a real limit vanishing. It matches the doctrine already written into
`ignoredLineTypes` ("too wide and a real new message type stays invisible"). Confirmed, not overturned.

**Rung 3 — overturning the naive reading, and this is the decision the ticket asks for.** "Emit unless
status is `allowed`" answers an absent container with *emit*, because an absent object decodes to an
empty status. That is rejected, for three reasons:

1. **The event would be a claim with no evidence behind it.** An absent container yields
   `RateLimited{Status:"", LimitType:"", ResetsAt:0}` — it names no limit and no reset time, so it
   cannot serve AC1's purpose. Emitting it is the daemon reporting a rate limit it never observed, the
   same inference `BackgroundTaskRoster`'s doc refuses to make about a task finishing.
2. **Its failure mode is the per-run noise row, which is the worse of the two.** If a future claude
   renames or drops the container, rung-3-emits produces one content-free "you are rate limited" event
   on **every healthy run, forever** — visible to users, indistinguishable from a real limit, and
   actionable in the wrong direction. Rung-3-silent produces a false *negative* on a feature that has
   never fired once in three captures. The `ignoredLineTypes` doctrine ranks a per-turn noise row as
   the outcome to avoid, and this is that row.
3. **It is the package's own precedent for an absent field.** `emitBackgroundTaskStarted`'s doc:
   "absence is claude's to choose, there is no captured negative case, so the field lands empty rather
   than inventing a validation rule." Landing empty on the *gate input* means the gate reads "no report
   was made", and no report is silence.

The cost is real and must be stated at the code: **a container rename goes undetected by any automatic
test.** The available detector is the live drop census — under rung 3 a renamed-container line still
gets dropped, so it still lands in `dropped_line_capture_test.go`'s per-type census with its payload,
which is exactly how #1260 discovered this payload in the first place. A *mapped* line does not appear
there, so the census cleanly separates "claude reported benign" from "claude changed shape". Say that
in the comment; it is the honest answer to "how would we find out".

**Byte-exact match on the benign value, and its failure direction stated rather than assumed.**
`benignRateLimitStatus = "allowed"` is compared with `==` — no fold, no trim, no prefix — which is the
tolerance three observations earn (`harnessNoOutputNudge`'s rule). Unlike that constant, drift here is
**not** the safe direction: a capitalisation or rename of the benign value makes the event fire once
per run on healthy runs. That is accepted deliberately, because the alternative is worse: a folded or
prefix match that swallowed a genuinely new benign-ish value would suppress a real limit in silence,
trading a loud wrong for a quiet one. A loud wrong is one constant edit to fix. Write that trade into
the constant's doc — do not write "the failure direction is safe", which is true of
`harnessNoOutputNudge` and false here.

### Where the arm goes

`ignoredLineTypes` loses `rate_limit_event` and becomes `{"system": true}`. The type gains its own case
in `consumeLine`'s main switch:

```go
case "rate_limit_event":
    p.emitRateLimit(line)
```

Two consequences worth noting in the comment:

- `emitUnrecognized` stays unreachable for this type **by matching**, not by list membership — which
  is a stronger guarantee than the one it had.
- `emitRateLimit` needs **no bool return**, unlike the `emitSystemSubtype` family: the case arm
  consumes the line by matching, so there is no "did you handle it?" to report back.

**The `sl.Type == "system"` guard at `:610-612`: keep it, re-justify it.** With one member left on the
list the guard is trivially true, and deleting it would be the simpler edit. Keep it: the property it
enforces — only `system` lines reach `emitSystemSubtype` — is what makes `emitUnrecognized`
structurally unreachable from any system line, and a one-member list is a transient state, not the
design. What must change is the *stated reason*, which currently names `rate_limit_event` as the
member the guard exists for. Re-state it as: the guard scopes the subtype match to `system` by
construction, so a future list member carrying a colliding subtype name cannot reach the wrong emitter.

### `emitRateLimit` contract

`func (p *Parser) emitRateLimit(line []byte)` — decodes the **top-level bytes** into a new unexported
target and emits at most one `turnevent.RateLimited`. Never emits an `Unrecognized`. Returns nothing.

Decode target, mirroring the family's one-struct-per-shape convention:

```go
type rateLimitEventLine struct {
    Info rateLimitInfo `json:"rate_limit_info"`
}

type rateLimitInfo struct {
    Status    string `json:"status"`
    LimitType string `json:"rateLimitType"`
    ResetsAt  int64  `json:"resetsAt"`
}
```

The nested shape is the family's first, and its doc should say why the container is a plain struct
rather than a `*rateLimitInfo`: absence and a present-but-empty object are treated **identically**
(both are rung 3), so a pointer would buy a distinction nothing acts on — the same argument
`systemThinkingTokensLine` makes for `int` over `*int`.

The decode input is `line`, the top-level bytes, never a nested field. `streamLine`'s doc states the
property this preserves (control shapes read from the top level only, nested content never re-scanned)
and it matters here for the same reason it does on `task_started`: decoding from anywhere else would
make a rate-limit report forgeable out of claude's own tool output.

Behaviour, in order:

1. `json.Unmarshal(line, &rl)` fails → content-free `Debug`, no event. Not an `Unrecognized`.
2. `rl.Info.Status == benignRateLimitStatus` → content-free `Debug`, no event.
3. `rl.Info.Status == ""` → content-free `Debug`, no event.
4. Otherwise → bound `Status` then `LimitType` (in that order — `TruncatedFields` is ordered by the
   `bound()` calls, in sequential statements not a composite literal, for `emitBackgroundTaskStarted`'s
   reason), pass `ResetsAt` through unbounded, emit.

**Logging.** One message, `"streamsup: dropping rate_limit_event"`, with a content-free `reason`
attribute drawn from three unexported constants (`undecodable` / `benign` / `no_rate_limit_info`).
Daemon-authored keywords, never claude's values — `Status` in particular must not reach a log, which
is the one field a drop-site is tempted to explain itself with. A single message with a closed reason
set beats three messages: it mirrors `dropcapReason*`'s shape in the realclaude package and gives the
tests one string to filter on.

### Bounding

**One new cap constant, `maxRateLimitField = 256`**, covering `Status` and `LimitType`. Precedent:
`maxTaskFieldID` serves three fields of the same class (short, enum-ish or opaque, claude-chosen). Its
doc must carry:

- **The multiple.** Observed values are 7–9 bytes (`"allowed"`, `"rejected"`, `"five_hour"`), so 256 is
  ~28× the observation — a wider multiple than `maxTaskFieldID`'s 9×, and deliberately so: the value
  set beyond the benign one is unmeasured, so there is no distribution to reason about and the binding
  constraint comes from the envelope side.
- **The envelope arithmetic**, in `maxUnrecognizedRaw`'s style: worst case one `RateLimited` carries
  256 + 256 = **512 bytes** of claude-derived text, 0.8% of the v2 application-envelope cap of 65519
  bytes (`docs/protocol-mobile.md` § Application-envelope size cap). An order of magnitude below the
  scalar background-task pair's 7.4% / 6.6%. Escaping is mild for `maxUnrecognizedRaw`'s reason.
- **Why it is a separate constant** even though it equals `maxTaskFieldID`: `maxTaskPatch`'s paragraph
  applies verbatim — they bound different fields for different reasons, and folding them would make a
  future change to the task-id budget silently move this one.

`ResetsAt` gets no cap and contributes no envelope term: an `int64` cannot grow. State that explicitly
rather than leaving its absence to be read as an oversight, the way `ThinkingProgress` does.

No rate bound. The line fires once per run (census `rate_limit_event == 1`), and under the gate above
a healthy run emits **zero** — so there is nothing to bound in frequency. `minThinkingTokensPerEvent`'s
whole reason for existing is absent here; say so in one sentence so a reader does not go looking.

The question a rate bound would answer was asked and deliberately declined: once-per-run is **measured,
not enforced**, so a claude that emitted thousands of non-benign `rate_limit_event` lines would produce
thousands of events, none of them a droppable delta (the droppable set is `assistant_delta` only,
#610), holding queue slots. That is the same accepted cost the three background-task variants already
carry, bounded by the same existing backpressure, and #1385 earned its rate bound from a **measured**
~10/turn rather than from a hypothetical. Per evidence-based fix selection: name the exposure, ship no
mechanism. The amplification from input to retained bytes is separately fine and worth stating —
`rateLimitEventLine` holds three scalars and no array, so a 4 MiB line (`defaultMaxParseBuf`,
`parser.go:19`) yields at most 512 bytes of retained text plus one integer.

### The fifth mirror obligation — not named in the ticket

AC4 names two realclaude edits. There is a **third**, and it fails the build-tagged suite if missed.

`TestParserIgnoredTypesMatchesStreamsupParser` ends with an orphan check
(`ptyrunner_byte_equivalence_test.go:1288-1292`): every key in `parserIgnoredTypeFixtures` must have a
matching `parserIgnoredTypes` member. Removing the `parserIgnoredTypes` entry at `:203` therefore
orphans the fixture entry at `:1204-1206`, which must be removed in the same edit.

**Do not relocate the fixture line elsewhere.** Its purpose was to exercise the mirror's claim against
the shipped parser; the mirror no longer makes a claim about this type, so the fixture has no claim to
exercise. The line's *verdict* is preserved by `TestDropcapClassification`'s row, which asserts the same
bytes against the same parser.

**Relocate the fact the deleted comment carried.** `:198-202` explains that `rate_limit_event` sits in
both `parserIgnoredTypes` and `expectedStreamRunnerOnly.Events`, that the two are independently true,
and that `shapeFilterDrops` consults the one-sided tables first so the double listing does not change
the filter. After this ticket the second half is the **only** thing keeping the type out of the sequence
comparison — the belt is gone and only the braces remain. Move that sentence to
`expectedStreamRunnerOnly.Events`'s entry (or to `shapeFilterDrops`), so a future "cleanup" of that
entry meets the reason it must not happen. No new test: this is a comment obligation, and no failure
has been observed.

**What does not change, and must not be "fixed":**

- `expectedStreamRunnerOnly.Events["rate_limit_event"]` (`:88`) and its comment — about ptyrunner's
  structural inability to emit an API-stream event. Independently true, unaffected.
- `shapeFilterDrops("rate_limit_event", "")` still returns **true**, via the one-sided table consulted
  at `:356` before `parserDropsShape`. The live sequence comparison is unaffected.
- `TestExtractShapes_FiltersParserIgnoredTypes` stays green with no edit, and its `:1061` comment stays
  true. A green filter here is **not** evidence the `parserIgnoredTypes` edit was unnecessary.
- `dropped_line_capture_test.go:1374` — a line-splitting fixture, not a claim about the parser.

### `dropcapClassify`'s row — the verdict is already right; only the prose moves

`dropcapClassify` (`:1055-1074`) calls the shipped parser and returns `("", false)` the moment it emits
anything. Under rung 3, `{"type":"rate_limit_event"}` emits nothing, falls to the `default` arm, and
still classifies as `dropcapReasonIgnoredType` with `wantDrop: true`. **Both fields at `:1519-1522`
stay as they are.** What changes is the `why:` string at `:1521` — "the second `ignoredLineTypes`
member" is false the moment the list shrinks. It must be replaced with the rung-3 statement: the line
carries no decodable `rate_limit_info`, so the shipped parser makes no rate-limit claim for it.

The `dropcapReasonIgnoredType` constant name becomes slightly loose for this row (the type is no longer
ignored; this *instance* is). That is acceptable and should not be renamed — the reason is derived by
`dropcapClassify`'s `default` arm from real parser behaviour, renaming it would touch every other row,
and the `why:` string is where the nuance belongs.

### The capture reader

AC2(a) requires the captured line byte-exact as a regression test. `capturedSystemLines`
(`capture_test.go:56`) filters on `rec.Type != "system"` and **cannot** return it — the capture record
has `type: "rate_limit_event"` and no `subtype` key at all.

Generalise the existing reader on **type**, keeping `capturePath` a package constant with no path
parameter:

- `capturedLines(t, typ, subtype string) [][]byte` — becomes the one reader, holding the `is_capture`
  and `payload_encoding` provenance checks and the zero-matches `t.Fatalf` unchanged.
- `capturedSystemLines(t, subtype)` — a one-line wrapper over it, so all 12 existing call sites are
  untouched.
- A singular exactly-one wrapper for this ticket's use, over `capturedLines(t, "rate_limit_event", "")`.

Do **not** add a second reader with its own copy of the provenance checks; the existing doc at `:39-47`
forbids exactly that, and its warning about generalisation is scoped to the *path* parameter, which
stays fixed. Amend that doc: it currently says "the package's one capture reader" of a function that is
about to become a wrapper, and it is scoped to system subtypes throughout.

**The amended doc must carry the path-parameter prohibition forward verbatim, and say that this
generalisation is the type axis only.** The original sentence is the guard against a reader that reads
any file: "a plural reader is exactly the shape someone later generalizes into 'read any capture file',
and that generalization is what would put an unchecked file behind these assertions." Widening the
reader by one axis is precisely the moment that sentence is most likely to be dropped as no longer
applying, and precisely the moment it applies most — `capturePath` stays a package constant and
`capturedLines` takes **no path parameter**.

### Downstream — nothing to wire, and one arm that is not wiring

No protocol type, no payload struct, no bridge route. `turnbridge.MapEvent` and `acpbridge.MapUpdate`
each drop the variant through their existing `default:` arms, exactly as they did for
`ThinkingProgress` between #1385 and #1386. `cmd/pyry/interactive_turn_v2.go`'s handler switch and
`stream_turn_busy.go`'s opener whitelist both land in `default`, and for the busy tracker that is the
**correct** answer, not an omission — see § Comment corrections.

The one arm that is not wiring: **`eventKind` (`cmd/pyry/interactive_turn_v2.go:457`) gains a
`case turnevent.RateLimited: return "rate_limited"`.** Every variant in the package has one, and
without it the four `eventKind` call sites (`acp_turn_stream.go`, `stream_turn_busy.go`,
`stream_turn_drain.go`, this file) log `"unknown"` for a variant the daemon recognises — the exact
reason `ThinkingProgress`'s arm exists (`:491-500`). The arm returns the variant **name only**; neither
`Status` nor `LimitType` is claude-derived content that may reach a log.

---

## Concurrency model

Unchanged. No new goroutine, no new channel, no new lock, and — unlike #1385 — **no new parser field
and no cross-line state**. `emitRateLimit` is a pure function of one line, like the three
background-task emitters and unlike `emitThinkingProgress`. `Parser`'s doc statement about its single
accumulator (`thinkingSinceEmit`, reset on the `result` arm) is untouched and must not be amended.

The single-writer invariant (`os/exec` drives `Write` from one goroutine) already covers everything
here, and this mapping adds nothing that depends on it.

---

## Error handling

| failure | response |
|---|---|
| Line does not decode into `rateLimitEventLine` | content-free `Debug` (`reason=undecodable`), no event, line consumed. **Never** an `Unrecognized` — see below. |
| `rate_limit_info` absent, or present but empty | content-free `Debug` (`reason=no_rate_limit_info`), no event. Rung 3. |
| `rate_limit_info` present but not a JSON object | whole-line decode fails → the undecodable arm above. Same visible outcome as rung 3, different logged reason. |
| `status == "allowed"` | content-free `Debug` (`reason=benign`), no event. Rung 1. |
| `rateLimitType` or `resetsAt` absent on an emitting line | field lands empty / 0 and the event **still fires**. The status is the report; absence of detail is claude's to choose and is not a validation rule. |
| Either bounded string over `maxRateLimitField` | cut by `truncateField`, name appended to `TruncatedFields`. |

**Why an undecodable payload is not an `Unrecognized`.** The family's standing answer
(`emitBackgroundTaskStarted`'s doc) is that surfacing a malformed line of a type we already know is
worth less than the structural guarantee that this type never reaches the unrecognized lane. Here the
guarantee is stronger than it was — the type is claimed by a case arm rather than by list membership —
and breaking it would put a bad payload in front of the live zero-unrecognized gate
(`interactive_stream_liveness_test.go:253`) for a line the daemon does in fact recognise.

---

## Testing strategy

Bullet scenarios; the developer writes them in the package's table-driven idiom. Untagged unless
stated.

**AC2 rung 1 — the captured line, and it is a regression test, not an example.**

- Feed the byte-exact capture (via the new reader) to a fresh parser: **zero events**.
- Assert specifically that no `turnevent.Unrecognized` of any site was emitted — a blanket
  "len(got) == 0" already covers it, but the ticket asks for both halves to be visible in the failure
  message.
- **Control, and it is load-bearing:** the same test must include an emitting line, or the
  zero-event assertion passes vacuously against a mis-wired sink. Reuse `TestDropcapClassification`'s
  framing for why.

**AC2 rung 2 — a non-benign status emits exactly one event.**

- Build the line from a `rateLimitLineFixture(status, limitType string, resetsAt int64)` helper whose
  key names are **exactly the capture's** (`rate_limit_info`, `status`, `rateLimitType`, `resetsAt`) —
  mirroring `taskStartedLineFixture`. Invent no field structure.
- One `turnevent.RateLimited` with `Status` carrying the supplied value verbatim, `LimitType`
  `"five_hour"`, `ResetsAt` the capture's integer, `TruncatedFields` nil.
- A second row with a *different* non-benign value, so the assertion pins "anything but the benign
  value" rather than one hardcoded alternative.
- A row with `status` differing from the benign value only in case (`"Allowed"`) → **emits**. This is
  the byte-exact match made visible, and it is the row that documents the accepted failure direction.

**AC2 rung 3 — the absent-container class. All silent, none `Unrecognized`.**

- `{"type":"rate_limit_event"}` — no container.
- `{"type":"rate_limit_event","rate_limit":{"status":"allowed"}}` — wrong container key (the shape at
  `parser_test.go:541`).
- `{"type":"rate_limit_event","retry_after":10}` — unrelated payload (the shape at
  `parser_test.go:151`).
- `{"type":"rate_limit_event","rate_limit_info":{}}` — container present, status absent.
- `{"type":"rate_limit_event","rate_limit_info":"nope"}` — container present but not an object; the
  whole-line decode fails and the undecodable arm consumes it.

Each row asserts silence **and** the logged `reason`, using the `logRecorder` helper. Asserting the
reason is what separates "silent for the right rung" from "silent by accident" — three rungs that all
produce zero events are otherwise indistinguishable from one another and from a dead arm.

**The drop site is logged content-free, and this needs its own assertion — asserting the `reason` does
not cover it.** An implementation that sets `reason` correctly *and* also logs
`"status", rl.Info.Status` passes every bullet above. Mirror
`TestParser_HarnessNudgeDropIsLoggedContentFree` (`parser_test.go:2601-2632`) and the family's
`dropMsg` sweeps (`:1333`, `:1936`, `:2508`): drive a line whose `status` and `rateLimitType` are
distinctive fixture values, then sweep **every** record's message and every attribute value for those
strings and fail if either appears. `Status` is the field this drop site is most tempted to explain
itself with, and it is the one value on this line that a future claude could make arbitrarily long or
arbitrarily revealing.

Run the same sweep on the **emit** path, not only the drop rungs: a rung-2 line must produce the event
and log nothing carrying its field values.

**AC2 case (c) also requires the decision to be stated where it lives.** The three in-tree fixtures
(`parser_test.go:151`, `:541`, `dropped_line_capture_test.go:1519-1522`) all keep their `want: nil` /
`wantDrop: true` verdicts, because rung 3 is silence. Each must gain a comment naming the rung and its
reason — a row that is green for a newly *chosen* reason and says nothing is exactly the "harmless
until the type is mapped" state the ticket is closing.

**Caps.**

- Fixture literal `256`, declared alongside `taskStartedCapCheat`'s block and **not** derived from
  `maxRateLimitField` — same doctrine, same reason: a fixture built from the constant it validates
  follows a halved constant green.
- `Status` over the cap → cut to exactly the cap, `TruncatedFields == ["status"]`.
- `LimitType` over the cap → `TruncatedFields == ["limit_type"]`.
- Both over → `["status", "limit_type"]`, in that order — the order is the contract, not an accident.
- Nothing over → `TruncatedFields` nil, **not** an empty non-nil slice.

**Leak sweep.** Mirror `parser_test.go:655-690`: reflect over the emitted event's fields and assert
neither the capture's `session_id` nor its `uuid` appears in any of them. The capture's `session_id` is
redacted to `$SESSION_ID`, so pin against the value read out of the capture, not a literal — and fail
loudly if the capture carries no `uuid`, or the sweep is vacuous (the pattern at `:1292-1294`).

**AC3 — the list pin.** `TestParser_IgnoredLineTypesIsTheMeasuredSet` (`:2637`) updates to
`map[string]bool{"system": true}`. Untagged: this is the first thing that goes red on every
developer's machine.

**AC4 — build-tagged, `make e2e-realclaude`.** No new test. The three edits (both `parserIgnoredTypes`
and `parserIgnoredTypeFixtures` entries removed; the `why:` string corrected) are verified by the
existing `TestParserIgnoredTypesMatchesStreamsupParser` and `TestDropcapClassification`. Run
`go vet -tags e2e_realclaude ./internal/e2e/realclaude/` and the offline members of that package
(`TestDropcapClassification`, `TestExtractShapes_*`, `TestParserIgnoredTypes*`,
`TestShapeFilterKeepsThinkingTokensOutOfTheSequence` all need no claude) before declaring AC4 met; the
live suite is the dispatcher's to run.

**Mutation check before opening the PR** (`go test -overlay`, no worktree writes). Each of these must
go RED at a named row, and each row must be the sole RED for its mutant:

| mutant | must go RED at |
|---|---|
| `benignRateLimitStatus` changed to another value | rung-1 captured-line silence |
| rung 3 flipped to emit | all four rung-3 rows |
| `maxRateLimitField` halved | the cap rows (literal fixture) |
| `bound()` call order swapped | the both-fields-cut `TruncatedFields` order row |
| `ResetsAt` not copied onto the event | rung-2 emit row |
| `rate_limit_event` restored to `ignoredLineTypes` | AC3's pin **and** rung 2 |
| `parserIgnoredTypes` entry restored | `TestParserIgnoredTypesMatchesStreamsupParser` (tagged) |
| the drop site given a `"status", rl.Info.Status` attr | the content-free-log sweep |

---

## Comment corrections — count per site

AC5's obligation is per-site. Eight go false, six stay true, and one is deleted along with its entry.
Editing a true one into a lie is the same failure as missing a false one.

**Goes false — must be corrected (8):**

| site | why it goes false |
|---|---|
| `parser.go:256` | "plus `rate_limit_event` whole" under "Still dropped in silence" — no longer whole. |
| `parser.go:259` | "The list itself is UNCHANGED" — it shrinks to one member. |
| `parser.go:610-612` | the guard's stated reason names `rate_limit_event` as the member it exists for. Re-justify (§ Where the arm goes), do not delete the guard. |
| `parser.go:617` | "system … and `rate_limit_event`: tolerated and dropped, silently". |
| `turnevent/event.go:408-411` | "`rate_limit_event` and every UNMAPPED system subtype stay silent". |
| `cmd/pyry/stream_turn_busy.go:137` | claims the parser drops it — **and** carries a stale cite (`parser.go:158-163`; the list is at `:282-285`). Fix both. See the note below. |
| `interactive_stream_unrecognized_test.go:9` | "`rate_limit_event` whole". |
| `dropped_line_capture_test.go:1521` | the `why:` string, "the second `ignoredLineTypes` member". |

**Stays true — must NOT be touched (6):**

| site | why it stays true |
|---|---|
| `parser.go:227` | the census row "`rate_limit_event` — once per run" is a statement about claude, not about the parser. Per parent #1264 it gains a **pointer** to the removal so the list and its rationale do not disagree; the census line itself is unedited. |
| `streamsup/watchdog.go:169` | the stall tracker decodes only the top-level `type` and matches no case for this one, so "activity only" is unchanged. Verified against `watchdog.go:143-171`. |
| `turnevent/event.go:414` | a historical quote of what the comment used to say. |
| `interactive_stream_unrecognized_test.go:34` | a line inventory of what claude emits on a tool turn. This change does not alter what claude emits. |
| `ptyrunner_byte_equivalence_test.go:88` | ptyrunner's structural inability to emit an API-stream event. Independent of whether the parser maps it. |
| `ptyrunner_byte_equivalence_test.go:1061` | "the once-per-run `rate_limit_event`" in a fixture describing claude's stream. |

**Deleted with its entry (1):** `ptyrunner_byte_equivalence_test.go:198-202`. Relocate the
`shapeFilterDrops`-consults-the-one-sided-tables-first fact rather than losing it — § The fifth mirror
obligation.

**The `stream_turn_busy.go` correction has a specific shape.** That comment argues the opener set is a
whitelist because `rate_limit_event` "is exactly the line that becomes an ApiRetry the day someone
wires it — a blacklist would wedge a conversation on it." **This ticket is that day, and the design is
vindicated:** `RateLimited` lands in `observe`'s `default` and is a no-op for the busy tracker with no
code change, which is the correct answer — a usage limit is orthogonal to turn lifecycle and opening a
turn on one would wedge the conversation exactly as opening one on an `Unrecognized` would. Rewrite the
sentence in the **past tense as a discharged prediction**, not as a still-pending one, and fix the cite
to `parser.go:282-285` while you are in the line.

---

## Sizing — S, and the evidence, because the projection exceeds the generic red line

Stated openly so code review can audit it.

**Shape class.** One claude line-type → one bounded daemon variant + one parser arm + caps + tests +
the mirror and prose consequences. The four shipped members of that class all merged as `size:s`, none
salvaged, none recorded a `max_turns`:

| ticket | feat commit | insertions |
|---|---|---|
| #1380 `task_started` | `da7cda6` | 743 / 4 files |
| #1381 `background_tasks_changed` | `0b04794` | 922 / 4 files |
| #1385 `thinking_tokens` | `85b5c2f` + `8d48744` | 1116 / 6 files |

#1404 sits inside that band: fewer novel mechanisms than #1385 (no accumulator, no parser field, no
fourth mirror table, no rate bound) and more prose work (8 corrections vs 5, plus a nested decode
target and a `TruncatedFields` surface #1385 did not need). Projection ~850–1000 total lines.

**That exceeds the generic ~600-line red line, and the override is a measurement rather than a
re-count.** The line-count red line is a proxy for the ~50-turn budget. In `internal/streamsup` +
`internal/turnevent` the doc-comment density runs ~1:1 with code, so lines do not track edits: the
direct count for this ticket is **~25 edits + ~15 verification turns**, and the four measured
instances of exactly this shape in exactly these files completed within budget. Where the direct
measurement of the thing the proxy proxies is available, it governs.

**The other red lines are clear, not close:**

- Production files: **4** — `turnevent/event.go`, `streamsup/parser.go`, `cmd/pyry/stream_turn_busy.go`,
  `cmd/pyry/interactive_turn_v2.go`. Under 5.
- New exported types: **1** (`turnevent.RateLimited`).
- New files: **0**.
- Reject/error branches: **3** (undecodable / benign / no-container). Nowhere near 10.
- Consumer sites needing simultaneous update: **5**, enumerated — `ignoredLineTypes`, its pin test,
  `parserIgnoredTypes`, `parserIgnoredTypeFixtures`, the dropcap row. Under 10, and additive: no
  signature changes, no interface renames, no test-fixture cascade.
- Acceptance criteria: **5**.

**There is no valid seam.** Both candidate splits ship a broken or dead intermediate state:

- *mapping ‖ mirror + prose* — the mirror edits cannot precede the mapping (the prose becomes false in
  the other direction, the #1380 rule) and cannot follow it without leaving
  `TestParserIgnoredTypesMatchesStreamsupParser` and `TestDropcapClassification` RED on the branch for
  a whole merge window.
- *declare the variant ‖ map it* — the #1393/#1394 pair shape, which existed because three events and a
  whole wire surface were in play. Here it ships a `turnevent.RateLimited` with no producer as dead
  code, and the caps would separate from the payload they bound — "a construction-time security bound
  never splits from the payload it bounds" (#1380).

A ticket with no valid seam is sized honestly and given the tightest possible reading list, which is
what § Files to read first is for.

**File-overlap check: clean.** `git fetch origin --prune` then a diff of every `origin/feature/N`
branch against `main` — no in-flight branch touches `internal/streamsup/`, `internal/turnevent/`,
`cmd/pyry/stream_turn_busy.go`, `cmd/pyry/interactive_turn_v2.go`, or any of the three realclaude test
files. `origin/feature/363` touches `internal/e2e/realclaude/fixtures.go`, a different file. No block
set.

---

## Open questions

**1. The benign-value drift has no automatic detector, and it cannot get one in this slice.** The live
zero-unrecognized assertion lives in `drainForCompletedTurn`
(`interactive_stream_liveness_test.go:185`), which reads decrypted **wire envelopes**
(`protocol.Envelope`), not `turnevent.Event`s. `RateLimited` has no wire type until #1405, so a
"healthy turn emits zero `RateLimited`" sentinel is not expressible here. **Route to #1405:** when the
wire type lands, add that assertion beside the zero-unrecognized one in the same shared drain, and
every stream spec becomes a sentinel for benign-value drift for free. Do not attempt it in this slice.

**2. The `status` value set beyond `"allowed"` stays unmeasured.** No capture of a limit in force
exists. Rung 2 is designed for that openly rather than around it, and `RateLimited.Status` carrying
claude's raw string is what will supply the measurement the first time a real limit fires. A follow-up
that promotes the set to a closed enum should wait for a second observed value, not for a plausible
list — the `harnessNoOutputNudge` rule.

**3. Whether `resetsAt` is ever absent on an emitting line.** All three captures carry it, but all
three are benign. The design lands 0 and emits anyway; if a real limited run turns out to omit it, that
is data for #1405's rendering decision, not a reason to withhold the event.

**4. The overage keys.** Deliberately dropped (§ The variant). If a consumer ever needs
"overage available / disabled", that is its own slice with its own measurement — the two detail keys
are version-variable across the three captures we have, which is precisely the shape that should not be
declared on speculation.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** MUST FIX → **fixed in this spec.** The design has one explicit boundary —
  `emitRateLimit` decoding the **top-level** line bytes into `rateLimitEventLine`, no nested re-scan —
  which is what stops a tool result whose text is literally `{"type":"rate_limit_event",…}` from
  forging a usage-limit report out of claude's own output (`streamLine`'s doc, `parser.go:422`;
  `emitAssistant`/`emitUser` decode blocks and never re-enter `consumeLine`). What the first draft
  left unstated was the *downstream* half: nothing establishes that `RateLimited` may not become a
  control input. Every field is claude-authored across that boundary, and a variant that any future
  slice keys a backoff, throttle, retry, or turn suspension on turns a wrong `status` value from one
  misleading row into a resource action the daemon takes on itself. § The variant now requires that
  refusal in the variant's own doc.
- **[Trust boundaries]** SHOULD FIX → **fixed.** `ResetsAt` was documented as "must not assume it is
  in the future". That is half the contract: the value is unvalidated in both directions, and negative,
  zero, and absurd-future values are all representable and none is rejected (rejecting one would be a
  validation rule with no captured negative case). A consumer formatting it as a date without a range
  check is the realistic bug. Stated at the field.
- **[Tokens, secrets, credentials]** No findings. No token, key, or credential is created, read, or
  compared. The captured line's two identity-ish keys — `session_id` (claude's session identity, **not**
  the daemon's conversation identity) and `uuid` — are absent from the decode target itself, which is
  a stronger guarantee than a reflection sweep because an undeclared field cannot leak; the sweep is
  required anyway (§ Testing strategy, mirroring `parser_test.go:655-690`) and must fail loudly rather
  than vacuously if the capture carries neither value.
- **[File operations]** No findings, and one prohibition preserved rather than assumed. The only file
  touched is the committed capture, read-only, in tests, at the fixed `capturePath` package constant.
  This spec widens the reader by one axis (type), and the existing doc's warning — that a plural reader
  is the shape someone generalises into "read any capture file", which is what would put an unchecked
  file behind these assertions — is exactly the sentence most likely to be dropped as no-longer-applying
  at the moment it applies most. § The capture reader now requires it carried forward verbatim, with
  `capturedLines` taking **no path parameter**. No path concatenation, no TOCTOU, no created file, no
  symlink follow.
- **[Subprocess / external command execution]** No findings, by a decision rather than by absence.
  Nothing here execs. The category is live in this package because `BackgroundTask.Description` carries
  a literal command line for claude's `local_bash` task type and its doc carries an explicit
  "safe to RENDER as text, never to execute or re-shell" warning. Neither new field is of that class —
  `Status` and `LimitType` are short enum-ish values (`"allowed"`, `"five_hour"`) — so the warning is
  deliberately **not** copied over; repeating it on every claude-derived string would devalue it where
  it is load-bearing.
- **[Cryptographic primitives]** No findings. No RNG, no hashing, no key material. The one comparison
  the design adds is `status == benignRateLimitStatus`; `crypto/subtle.ConstantTimeCompare` is **not**
  applicable because neither operand is a secret — the benign value is a public constant and the input
  is subprocess stdout, so a timing side channel reveals nothing an attacker supplying the input does
  not already know.
- **[Network & I/O]** No findings; the bound is stated with its arithmetic rather than asserted.
  Input is capped upstream at 4 MiB by `defaultMaxParseBuf` (`parser.go:19`) before the decoder sees
  the line. `rateLimitEventLine` holds three scalars and no array, so amplification from input to
  retained bytes is linear and near zero — a 4 MiB line yields at most 512 bytes of retained text plus
  one integer, versus the roster variant's array case that needed a cardinality cap. Worst case one
  event is 512 bytes, 0.8% of the 65519-byte v2 application-envelope cap
  (`docs/protocol-mobile.md:304`), an order of magnitude under the scalar background-task pair. The
  envelope arithmetic is computed here for a payload that does not yet ride an envelope — that is the
  correct order (bounded at construction, before the wire, so #1405 inherits a bound rather than adding
  one), not premature.
- **[Network & I/O]** OUT OF SCOPE, named rather than silent. Once-per-run is **measured, not
  enforced**: a claude emitting many non-benign `rate_limit_event` lines would produce many events,
  none of them a droppable delta (#610), holding queue slots. Identical to the exposure the three
  background-task variants already carry, bounded by the same existing backpressure, and #1385 earned
  its rate bound from a measured ~10/turn rather than a hypothetical. Per evidence-based fix selection:
  exposure named in § Bounding, no mechanism shipped. Revisit only on an observed rate.
- **[Error messages, logs, telemetry]** MUST FIX → **fixed in this spec.** The drop site logs one
  message with a `reason` drawn from a closed set of three daemon-authored keywords, and no claude
  value. The hole was in the *tests*: the first draft asked each rung to assert its logged `reason`,
  and an implementation that sets `reason` correctly **and also** logs `"status", rl.Info.Status`
  passes every one of those assertions. § Testing strategy now requires a content-free sweep over every
  record's message and attributes, on the emit path as well as the three drop rungs, mirroring
  `TestParser_HarnessNudgeDropIsLoggedContentFree` (`parser_test.go:2601-2632`), plus a mutation row
  that must go RED. `Status` is the field a drop site is most tempted to explain itself with and the
  one value on this line a future claude could make arbitrarily long. This event surfaces no error to
  an external caller and adds no telemetry.
- **[Concurrency]** No findings. The mapping adds no goroutine, no channel, no lock, and — unlike
  #1385 — no parser field and no cross-line state: `emitRateLimit` is a pure function of one line.
  `Parser`'s single accumulator (`thinkingSinceEmit`, reset on the `result` arm) is untouched, so no
  second boundary is created for one piece of state to keep agreeing. The single-writer invariant
  (`os/exec` drives `Write` from one goroutine) is inherited unchanged and nothing new depends on it.
  There is no partial state to recover: a process signalled mid-line loses the line, exactly as today.
- **[Threat model alignment]** No findings. `docs/threat-model.md` does not exist in this repo; the
  governing document is `docs/protocol-mobile.md` § Security model, whose one clause relevant to this
  slice is the application-envelope size cap (`:304`), satisfied at construction with an order of
  magnitude of headroom. Nothing crosses the wire in this slice — the mobile shape and the turnbridge
  route are #1405 — and this spec's Open Question 1 hands that ticket the one control this slice cannot
  express: a live "healthy turn emits zero `RateLimited`" sentinel beside the zero-unrecognized
  assertion in `drainForCompletedTurn`, which reads wire envelopes and therefore cannot see a variant
  with no wire type.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-09
