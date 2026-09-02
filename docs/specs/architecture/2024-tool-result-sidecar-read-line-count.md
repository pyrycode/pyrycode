# #2024 — decode the tool-result sidecar and send a read row's line count

Split from #1794. Sibling: #2025 (the remaining four sidecar shapes, which add arms to the
dispatch this slice builds and explicitly add no new wire field, no new event field and no
new plumbing).

## Files read

- `internal/streamsup/parser.go` → `streamLine` — the line-level segmentation struct; its
  doc and `systemTaskStartedLine`'s state the boundary this design must not cross.
- `internal/streamsup/parser.go` → `systemTaskUpdatedLine` — the `json.RawMessage` +
  second-stage-decode precedent, including why `map[string]any` is the wrong target (key
  order normalised, every number rounded through `float64`).
- `internal/streamsup/parser.go` → `consumeLine` — the `user` arm is the call site that has
  to change; its `rate_limit_event` arm calls `emitRateLimit(line)`, the in-file precedent
  for handing an emitter the raw line bytes.
- `internal/streamsup/parser.go` → `emitUser`, `decodeBlock`, `toolResultContent`,
  `toolResultText` — the existing user-block loop, its emission order, and the
  Unrecognized surfacing this design must leave untouched.
- `internal/streamsup/parser.go` → `streamMessage`, `streamBlock` — `Content` is
  `[]json.RawMessage` at message level and `any` at block level; the block count pre-pass
  is sized against that.
- `internal/turnevent/event.go` → `ToolUpdate` — the event that gains the composed string.
- `internal/turnbridge/outbound.go` → `maxResultSummaryRunes`, `resultSummary`, and the
  `turnevent.ToolUpdate` arm of the mapper — the bounding precedent for a claude-derived
  string on this exact payload, and the one production construct site of the payload.
  Its doc measures the worst case at 61363 B of the 65519-byte v2 application-envelope
  cap (93.7%, ~4.2 KB of headroom) and states the failure mode: exceeding the cap does not
  truncate a frame, it **loses** it.
- `internal/turnbridge/outbound_test.go` → `TestToolResultPayload_FitV2EnvelopeCap` — the
  test AC5 names by name; it drives the real `resultSummary` so the constant is what the
  measurement stands on.
- `internal/protocol/interactive.go` → the file's package doc and `ToolResultPayload`.
  **The file doc states a hard convention: "No field carries omitempty: every field is
  always present on the wire so the testdata fixtures pin the full shape and boundary
  values like seq:0 and is_error:false do not silently vanish."** This overrides the
  ticket body's "one new optional string" read as an `omitempty` tag — see Open questions.
- `internal/protocol/interactive_test.go` → `roundTripEnvelope`,
  `TestToolResultPayload_RoundTrip` — the round trip is byte-exact against
  `internal/protocol/testdata/tool_result.json`, so the fixture moves with the struct.
- `internal/e2e/realclaude/testdata/tool_result_sidecar_v2.1.239.json` — #2023's committed
  stdout capture. The two retained sidecar-bearing `user` lines are the observed bytes
  behind the read arm and the non-read-object arm.
- `docs/knowledge/features/protocol-package-drift-detectors.md` — checked and found not to
  apply: every detector there keys on `Type*` constants, and this slice adds none.
- `docs/knowledge/features/streamsup-package.md`, `turnevent-package.md` — package roles
  and the "content blocks are held as `json.RawMessage`" convention this design extends to
  the line's sidecar.

## Context

A read's tool row today says only what `resultSummary` derives from the block's own
`content`. Claude already writes the structured outcome of every call in a **sidecar**: a
top-level field of the stdout line, a sibling of `type` and `message`. The daemon has never
read it, because `streamLine` carries `Type`/`Subtype`/`Message` only.

This slice lands the whole path — sidecar to wire — for the read shape, plus the
fail-closed default that makes every other shape correct by construction until #2025 fills
them in. Reads are partial 56.5% of the time across 93227 measured calls, which is why the
row names both halves: a bare count would hide that only a slice of a much longer file was
seen.

**The field is spelled `tool_use_result` on stdout.** The transcript spells it
`toolUseResult`; `internal/streamsup` parses claude's stdout, not the transcript, and there
the same payload is snake_case. #2023's first run searched only the camelCase name and
reported `sidecar-absent` — literally true, materially the inverse. The re-run recorded a
spelling census of `{"tool_use_result": 2}` against zero camelCase on claude 2.1.239.
**The rename stops at the envelope: every key inside stays camelCase** (`file.filePath`,
`file.numLines`, `file.startLine`, `file.totalLines`).

No ADR is warranted. This is one decode arm and one wire field inside an established
pattern, not a decision that needs a record of its own.

### Size — the overage is stated, not split away

The refiner estimated ~890 lines against the 800-line size-S boundary and declined to
split. Re-checked against this written plan, the other five boundaries hold: 4 production
files, 0 new exported types or interfaces, 1 production construct site apiece for both
changed types (verified — `internal/streamsup/parser.go` for `turnevent.ToolUpdate`,
`internal/turnbridge/outbound.go` for `protocol.ToolResultPayload`; every other site is a
keyed test literal covered by Go's zero value), 5 acceptance criteria, 8 reject branches.

Only the line count trips, and it trips on the **floor** rule rather than despite it: a
`protocol` field with no producer is a name minted for one caller, and a `turnevent` field
nothing maps is the same thing from the other end. Neither half is checkable alone. The
floor wins over the ceiling, the overage is ~90 lines, and this ticket builds as one.

## Design

### Layer 1 — carriage off the line (`internal/streamsup/parser.go`)

`streamLine` does not change. A second decode target holds the sidecar, exactly as
`systemTaskUpdatedLine` does for a patch:

```go
type userToolResultLine struct {
    ToolUseResult json.RawMessage `json:"tool_use_result"`
}
```

`json.RawMessage` accepts **any** valid JSON value — object, string, number, null — so the
one captured teardown sidecar that is the bare string `Error: Exit code 1` lands here
without failing anything. `emitSlashCommandList`'s objection to a second `json.Unmarshal`
("it would add a second undecodable outcome to classify") does not transfer: `consumeLine`
has already decoded this line once, and a `RawMessage` target adds no new failure.

`consumeLine`'s `user` arm changes to hand `emitUser` the raw line bytes alongside the
message — `emitRateLimit(line)`'s shape. The second decode happens inside `emitUser`, and a
decode error there yields no count and emits nothing: **no `Unrecognized`, ever.** A
sidecar the daemon does not understand costs a missing count, never a wrong one and never a
second unrecognized outcome (AC3).

### Layer 2 — the read shape (`internal/streamsup/parser.go`)

```go
type toolResultSidecar struct{ File *sidecarFile `json:"file"` }

type sidecarFile struct {
    NumLines   *int64 `json:"numLines"`
    TotalLines *int64 `json:"totalLines"`
}
```

Three properties, each load-bearing:

- **Pointers distinguish an absent key from a present zero.** A sidecar carrying only one
  of the two keys is not a read and sends nothing; `0`/`0` is present-and-zero and takes
  its own arm.
- **`int64`, not `float64` or `any`.** A count that does not decode as a fixed-width whole
  number — `4.5`, `"4"`, a value past `int64` — fails the decode and sends nothing. That
  is what makes the composed string's length *structural* rather than a second cap to keep
  correct.
- **`file.content` is never declared.** Absent from the decode target is a stronger
  guarantee than any test sweep: a field that does not exist cannot leak (the reasoning
  `systemTaskUpdatedLine`'s doc states for `uuid`/`session_id`). This is the confinement
  clause of AC1, and `file.content` is the entire contents of the file claude read.

Keying is on `file.numLines` **and** `file.totalLines`, both of them, and nothing else.
Not on the sidecar's `type`: a read sidecar carries `type: "text"` and a write sidecar
carries `type: "create"`, so `type` identifies nothing here.

**Composer** — `readLineCount(sidecar json.RawMessage) string`, contract: returns the row
text, or `""` for every shape that is not a read. Reject arms, all sending `""`:

| Case | Why |
| --- | --- |
| sidecar absent / zero-length | nothing to decode |
| sidecar is not a JSON object (the bare string `Error: Exit code 1`) | struct decode fails |
| object with no `file` key, or `file` is not an object | not a read |
| only one of `numLines` / `totalLines` present | one without the other is not a read |
| either count negative | not a non-negative whole number |
| either count not a fixed-width whole number | decode fails |
| both counts zero | an empty file's read has nothing worth saying |

Accept arms: counts equal and not both zero → `"265 lines"`; counts differing →
`"110 of 1676 lines"`, returned before total. **No relational validation** — a returned
count larger than its total is rendered as given, and a zero returned count against a
non-zero total still sends `0 of 1676 lines`, which says something true. Inventing a
validation rule for a shape with few observations is what #1380 declined to do and what
`systemTaskUpdatedLine.Patch` reasons about in this file.

**No pluralisation.** `1 lines` is the form. #1794 measured the shortest value at 7
characters, which is `4 lines`, so the family's own measurement is of the unpluralised
form; a singular arm would be a rule the ticket's table does not carry.

### Layer 3 — attribution (`internal/streamsup/parser.go`)

One line carries one sidecar; a `user` message may carry many `tool_result` blocks. The two
meet at `emitUser`, which is the only place holding both. **More than one `tool_result`
block → no count on any of them** (AC4) — the sidecar cannot be attributed to a particular
block, and a count on the wrong row is worse than no count. The capture's block histogram
is `{"0": 1, "1": 2}`, so this is a fail-closed decision about an unobserved case rather
than a measured shape.

Order of operations inside `emitUser`, chosen so the existing loop's emission order and its
`Unrecognized` surfacing are untouched:

1. Compose the count from the line's sidecar. Almost always `""` — about 95% of calls are
   tools with no meaningful count.
2. **Only if it is non-empty**, run a block-count pre-pass over `msg.Content` decoding each
   block into a `struct{ Type string }` and counting `tool_result`. Non-empty is the gate,
   so the 95% path pays nothing and a read pays one tiny decode per block. A block that
   fails even that decode is not counted; the main loop still surfaces it as
   `Unrecognized`.
3. If the count is not exactly 1, drop the composed string.
4. The existing loop runs unchanged, attaching the string to the one `ToolUpdate`.

### Layer 4 — event and wire

- `internal/turnevent/event.go` → `ToolUpdate` gains `ResultDetail string`. Named for what
  it is rather than for reads, because #2025's four shapes ride this same field.
- `internal/turnbridge/outbound.go` → the `turnevent.ToolUpdate` arm maps it straight
  through. **No cap here.** The bound is applied at CONSTRUCTION in `readLineCount`, which
  is where every cap in `streamsup` is applied; `maxResultSummaryRunes` exists because
  `toolResultContent` returns claude's text verbatim with no bound of its own, and that is
  precisely not this field's situation.
- `internal/protocol/interactive.go` → `ToolResultPayload` gains
  `ResultDetail string \`json:"result_detail"\``, **no `omitempty`** (see Open questions).
- `internal/protocol/testdata/tool_result.json` gains `"result_detail":""` so the byte-exact
  `roundTripEnvelope` keeps pinning the full shape, and so a client author reading the
  fixture sees the new key.

### The bound, by construction

Both counts are non-negative `int64`, so each formats to at most 19 digits. The longest
composable string is `19 + len(" of ") + 19 + len(" lines")` = **48 bytes**, all of them
digits, spaces and ASCII letters — none of which `encoding/json` escapes, so 48 bytes is
the wire cost too, plus 18 bytes for `,"result_detail":""`. Against the ~4.2 KB of
headroom `maxResultSummaryRunes`' doc measures, that is under 1.6%. A hostile or absurd
line count cannot grow the frame, because the arithmetic is over `int64`'s range and not
over claude's input length.

## Concurrency model

None added. `readLineCount` and the block-count pre-pass are pure functions over bytes the
caller already owns, called synchronously on the same os/exec forwarder goroutine that
already runs `consumeLine` → `emitUser` → `emit`. No goroutine, no lock, no shared state,
no shutdown path to add.

## Error handling

Every failure in this slice is the same failure: **send no count**. There is exactly one
error-signalling mechanism (the empty string) and no error value crosses a layer boundary.
Concretely:

- A malformed or unexpected sidecar never fails the line. `consumeLine` has already
  decoded the line; the second decode's error is swallowed by `readLineCount`.
- No `Unrecognized` is emitted on any sidecar path (AC3). A sidecar shape the daemon does
  not recognise is not news the way a fourth assistant block type is — #2025 exists to add
  the shapes, and an `Unrecognized` per shell call would be per-turn noise on the phone.
- No log line carries sidecar content. If a drop is logged at all it is content-free, in
  the shape `emitUser`'s existing harness-nudge drop uses (site and type only).

## Testing strategy

`internal/streamsup/parser_test.go` — table-driven, in the existing `consumeLine` table's
shape (one raw line in, an expected `[]turnevent.Event` out), so each case is a real line
through the real parser rather than a direct call to the composer.

**Fixture provenance.** Bytes come from committed captures, not from the ticket's table:

- read, counts equal (4/4) and the non-read shell object: the two retained sidecar-bearing
  `user` lines of `internal/e2e/realclaude/testdata/tool_result_sidecar_v2.1.239.json`,
  observed on **stdout**, claude 2.1.239 — the surface this decoder reads. Inlined in the
  test with a citation to that artifact, per the repo's practice; not read across the
  package boundary at test time.
- read, counts differing: the `file` object's bytes from `internal/agentrun/jsonl`'s
  `clean.jsonl` (40 of 256) and `no_end_turn.jsonl` (50 of 236). **Transcript** captures —
  the `file` object only, wrapped in a stdout-shaped line with the snake_case envelope key,
  because a transcript line lifted whole carries the wrong envelope key.
- non-object: the bare string `Error: Exit code 1` from `probeToolResultTeardownAbort`.
  **Transcript** capture; the sidecar's bare-string value only.

Scenarios (bullets, not bodies):

- **AC1 carriage** — the captured stdout read line yields a `ToolUpdate` carrying
  `4 lines`. The proof that the segmentation struct did not widen is structural: a
  reflection assertion that `streamLine`'s field set is exactly `Type`/`Subtype`/`Message`.
- **AC1 confinement** — a read line whose `file.content` carries a distinctive marker
  string: assert the marker appears nowhere in the emitted event (marshalled whole) and
  nowhere in the marshalled `protocol.ToolResultPayload` the bridge builds from it. The
  marker must be one that would survive a substring search — not the empty string.
- **AC2 accept/reject** — equal-and-non-zero → short form; differing → both, returned
  first; both zero → none; `numLines` alone → none; `totalLines` alone → none; `4.5` →
  none; `"4"` → none; a value past `int64` → none; negative → none.
- **AC3 fail-closed** — absent sidecar; the bare-string sidecar (its own named case);
  the captured shell object. Each asserts the exact event list, which is how "no second
  unrecognized outcome" is proven: an extra `Unrecognized` would make the list differ.
- **AC4 multi-block** — a `user` line with two `tool_result` blocks and a read sidecar
  emits two `ToolUpdate`s, neither carrying a count. Plus a one-block control on the same
  sidecar that does carry it, so the case is not vacuously green.
- **AC5 wire** — a payload JSON with no `result_detail` key decodes without error to `""`
  (absence is a value, not an error); `TestToolResultPayload_RoundTrip` keeps its
  byte-exact round trip against the updated fixture; and
  `TestToolResultPayload_FitV2EnvelopeCap` is extended to carry the field at its
  constructed worst case (48 bytes) alongside the capped `ResultSummary`, so the envelope
  guarantee is measured with the new field present.
- **Bound** — a unit assertion that `readLineCount` at `int64`'s extremes returns at most
  48 bytes, so the number the cap test carries is the producer's own and not a guess.

`internal/turnbridge/outbound_test.go` — the existing mapper table gains a case proving
`ResultDetail` maps through.

Gate: `go test -race` on `internal/streamsup`, `internal/turnevent`, `internal/turnbridge`,
`internal/protocol`; `go vet ./...`; `go build ./cmd/pyry`.

## Open questions

1. **`omitempty` or not on the wire field?** Resolved before the plan commit, against the
   ticket's phrasing. The ticket says "one new optional string"; `internal/protocol/interactive.go`'s
   file doc says "No field carries omitempty: every field is always present on the wire so
   the testdata fixtures pin the full shape". The declaring file's own convention wins over
   the analogy, so the field ships **without** `omitempty` and AC5's "optional" is
   honoured in the protocol sense that actually binds: absence decodes to `""` without
   error for a client built before this lands, and `""` is the value meaning "no count".
   The alternative would also have been invisible to the fixture, since an empty
   `omitempty` field emits no key and the round trip would stay green either way — the
   convention is the only thing that decides it.
2. **Field name.** No name was minted by #1794 or #2025 (checked; #2025 explicitly adds no
   new wire field, so this name has to serve all five shapes). `result_detail` /
   `ResultDetail` is chosen over a read-specific name for that reason.
3. **Does a block that fails its own decode suppress the count?** No — it is not counted as
   a `tool_result`, so a line with one valid read block and one garbage block still sends
   the count. Revisit only if a capture ever shows garbage blocks sharing a line with a
   `tool_result`; today that shape is unobserved and the conservative alternative would
   suppress counts on lines that are plainly attributable.

## Security review

**Verdict:** PASS

The asset this ticket puts at risk is named plainly: the read sidecar's `file.content` is
**the entire contents of the file claude read**, and `file.filePath` is an absolute path
that discloses the operator's home directory and project layout. Both arrive in the same
bytes this slice starts decoding, and the consumer at the far end is a mobile client across
a relay. Everything below is measured against that.

**Findings:**

- **[Trust boundaries] No findings — but the boundary is stronger than the neighbouring
  field's and must be documented as such.** `readLineCount` is the single, named crossing
  from untrusted stdout bytes to daemon state; nothing else in the design parses the
  sidecar. The property that makes it safe is that **no claude-supplied byte reaches the
  output at all**: the returned string is `strconv.FormatInt` over two `int64`s plus the
  literals `" of "` and `" lines"`, so its alphabet is `[0-9]`, space and ASCII letters.
  There is no injection surface — no JSON escape, no control byte, no ANSI sequence, no
  path fragment — and this is categorically unlike its payload sibling `ResultSummary`,
  which is claude's text carried verbatim under a rune cap. SHOULD FIX in Phase B: say so
  at both `ResultDetail` declarations, because a reader who assumes the two fields share a
  provenance will reason wrongly about both.

- **[Error messages, logs, telemetry] MUST-NOT, satisfied by construction — and this is
  the finding the pass exists to have found.** `emitUnrecognized` does not merely log: it
  puts the offending bytes on the wire as `turnevent.Unrecognized.Raw`, cut by
  `truncateRaw` to `maxUnrecognizedRaw` and shipped to the phone. A design that surfaced an
  unrecognised **sidecar** that way would exfiltrate the first `maxUnrecognizedRaw` bytes
  of every file claude reads, straight into the event ring and onto the relay. The
  fail-closed default is therefore a confinement control, not only an AC3 nicety: **no
  sidecar path may emit an `Unrecognized`, ever.** AC3's "no second unrecognized outcome"
  is the assertion that enforces it, and the exact-event-list tests are what keep it
  enforced.

- **[Tokens, secrets, credentials] No findings on tokens; the secret-shaped data is
  handled structurally.** `file.content`, `file.filePath`, and the shell shape's `stdout` /
  `stderr` are never declared in `toolResultSidecar` or `sidecarFile`, so they cannot be
  read into daemon state at all — the guarantee `systemTaskUpdatedLine`'s doc states for
  `uuid`/`session_id`, and stronger than any test sweep. SHOULD FIX in Phase B: no new log
  call may take `line`, the sidecar, or any part of either. `readLineCount` is given no
  logger precisely so it cannot; `emitUser` does hold `p.log`, and the only permitted shape
  there is the content-free site-and-type form its existing harness-nudge drop uses.

- **[Network & I/O] No findings, with one pre-existing bound named rather than inherited
  silently.** `Parser.Write`'s `defaultMaxParseBuf` caps the **partial** remainder only; a
  complete `'\n'`-delimited line is handed to `consumeLine` whole at any size. That is
  pre-existing and out of scope here (§ Scope Discipline). What this slice adds against it
  is bounded: `json.Unmarshal` into `json.RawMessage` **copies**, so the sidecar copy is
  transient, proportional to a line the parser already holds in full, and freed when
  `emitUser` returns. Critically, the composed string is **freshly formatted digits and
  retains no sub-slice of the sidecar**, so it cannot pin the decode's allocation the way a
  `s[:n]` "cap" would. Outbound size is bounded by construction at 48 bytes — see § The
  bound — against the ~4.2 KB of envelope headroom, and
  `TestToolResultPayload_FitV2EnvelopeCap` measures it with the field present.

- **[File operations] Not applicable, by a decision rather than by absence.** No path is
  opened, canonicalised, joined or `Stat`ed anywhere in this slice. `file.filePath` is
  present in the observed sidecar and is deliberately not decoded: mapping it would put an
  operator's absolute path onto the wire for no gain the row needs.

- **[Subprocess / external command execution] Not applicable.** The sidecar is *output* of
  an already-spawned child. No value decoded here flows back into any argv, environment,
  or stdin write; nothing in this slice execs.

- **[Cryptographic primitives] Not applicable.** No randomness, no key material, and no
  comparison of an attacker-controlled value against a secret. The two equality tests are
  `int64` comparisons of claude's own counts against each other, with no security meaning.

- **[Concurrency] Not applicable, stated rather than assumed.** No goroutine, no lock, no
  shared mutable state, no shutdown path. `readLineCount` and the block-count pre-pass are
  pure functions over bytes the caller already owns, running synchronously on the same
  os/exec forwarder goroutine that already runs `consumeLine`.

- **[Threat model alignment] No findings.** `docs/protocol-mobile.md` § Security model
  treats an authenticated phone as the recipient; the exposure this ticket could have added
  is unintended daemon→phone disclosure of local file contents, addressed by the two
  structural controls above (undeclared fields, no `Unrecognized`). The unbounded
  complete-line allocation is named as pre-existing and belongs to whoever caps
  `consumeLine`'s input, not to this slice.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02

## Revisions

### 2026-09-02 — implementation

**Correction to the security review's [Network & I/O] finding.** The committed text said a
complete `'\n'`-delimited line "is handed to `consumeLine` whole at any size". That is
wrong as stated: `Parser.Write` caps its accumulator at `defaultMaxParseBuf` (4 MiB), so
while the check runs *after* the complete-line loop — meaning one line can reach roughly
4 MiB plus a single `Write` chunk — a line is bounded, not unbounded. The finding's
conclusion is unchanged and slightly stronger: the transient sidecar copy this slice adds
is proportional to an allocation that is already bounded. Nothing in the design moved; the
claim was over-stated and is corrected here rather than quietly left in place.

**The design shipped as planned.** No interface changed shape between the plan and the
code, and all three Open questions resolved as written: no `omitempty` (the declaring
file's convention over the ticket's phrasing), `result_detail` / `ResultDetail` as the
name, and a block that fails its own decode does not suppress the count.

**Measurements the plan predicted, now taken.**

- `TestToolResultPayload_FitV2EnvelopeCap` measures **61430 B, 93.8%** of the 65519-byte
  cap, up from 61363 B / 93.7%. That is +67 B — the field's 48-byte worst case plus 19 B
  of key and punctuation — matching § The bound's arithmetic exactly.
- The producer's worst case is 48 bytes, asserted against `maxResultDetailBytes` by
  `TestReadLineCount_BoundedByConstruction` rather than assumed by the cap test.

**Non-vacuity, established by mutation rather than by argument.** Three mutants were run
under `go test -overlay` (no worktree writes) and each was caught:

- envelope key `tool_use_result` → `toolUseResult`: **6 subtests red.** This is the mutant
  that matters — it is precisely the dead-code decoder #2023 was run to prevent, and it
  confirms the stdout-captured fixtures pin the spelling rather than agreeing with it.
- `file.filePath` folded into the composed string: the confinement test caught the leak by
  marker, which is the security control of AC1 proven live rather than argued from the
  undeclared field set.
- multi-block guard forced off: the two-block case went red while the one-block control
  stayed green, so AC4 is not passing merely because nothing composes a count.
