# #1826 — bound the decoded slash-command entry count and report the drops

## Files read

- `internal/streamsup/parser.go` → `maxSlashCommandName` — states THE PER-ENTRY TERM (1280 bytes) this
  slice multiplies, and its closing sentence names the entry count as the one missing factor.
- `internal/streamsup/parser.go` → `maxSlashCommandDescription` — owns the fraction table handed forward
  (6 entries at 1/2, 8 at 5/8) and the 6,711-byte retained measurement this derivation takes as its base.
- `internal/streamsup/parser.go` → `maxSlashCommandAliasCount` — the cross-reference to this ticket, and
  the ROLE argument (a cut command is a working command greyed out in the consumer's menu) plus the
  workspace-authored-growth argument that push past the family's cardinality convention.
- `internal/streamsup/parser.go` → `maxModelListEntries` — the structural precedent: NOT 8 (no cap firing
  on ordinary output), the power-of-two exception, and the record of a ceiling MOVING when no pair of caps
  the family's doctrine permits fits the inherited one.
- `internal/streamsup/parser.go` → `emitModelList` — the five-rung classification, the shared
  `commands := len(...)` above both rungs that read the array, and the model list's own count-bound block
  (`dropped`, tail truncation) that this slice copies one array over.
- `internal/streamsup/parser.go` → `emitSlashCommandList` — the construction site, its empty-list
  PRECONDITION, its IT LOGS NOTHING property, and the tail comment reserving this bound by number.
- `internal/streamsup/parser.go` → `logControlResponse` — the six-attribute record, and the paragraph
  arguing NO SEVENTH ATTRIBUTE from a premise (`no entry-count cap exists`) this slice falsifies.
- `internal/streamsup/parser.go` → `truncateField` — the `<=` boundary and the empty-replacement scrub the
  retained measurement is computed through.
- `internal/turnevent/event.go` → `ModelList`, `ModelList.DroppedModels` — the field doc shape to copy,
  including the "reports where the dimension is decided" argument.
- `internal/turnevent/event.go` → `SlashCommandList` — two paragraphs to reconcile: the split-four-ways
  attribution and NO DroppedCommands FIELD.
- `internal/protocol/interactive.go` → `SlashCommandListPayload` — "NOTHING COUNTS IT YET, and no entry cap
  is enforced anywhere", plus the 51/74 count measurement and the 14,277-byte serialisation.
- `internal/streamsup/parser_test.go` → `TestParser_ModelListEntryCountIsBounded` — the test shape to
  mirror, rows and record subtest alike.
- `internal/streamsup/parser_test.go` → `TestParser_SlashCommandFieldsAreCapped`,
  `TestParser_InitializeControlResponseRejectBranches` — the two comments naming this ticket, and the
  exact-map `wantAttrs` comparisons that every record-shape change reddens.
- `internal/streamsup/initialize_capture_test.go` → `capturedInitializePayload` — the hermetic reader that
  makes AC 4 need no live claude.
- `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` — the committed capture; every
  figure below is re-derived from it in this session rather than transcribed.
- `docs/knowledge/features/streamsup-package-producing-turnevent-slashcommandlist.md`,
  `docs/knowledge/features/turnevent-package.md` — both carry the retired "No entry-count cap and no
  `DroppedCommands`" figure. **Documentation phase's to correct, NOT this slice's.**

## Context

`emitSlashCommandList` bounds every text dimension of a slash-command entry and nothing about how many
entries it keeps. The array's length is claude's — really the workspace's — to choose, so the value's
total retained size is a function of a number the daemon does not control; the only bound below it is
`defaultMaxParseBuf`'s 4 MiB on the whole input line. `protocol.SlashCommandListPayload.DroppedCommands`
is declared and permanently 0, which reads as "nothing was dropped" rather than as "nobody counts".

This is `maxTaskRosterEntries`' doctrine's third application, after `maxModelListEntries` (#1812).

**Sizing, re-counted against this written plan.** Three production files and zero new exported types, both
inside the boundary. Two lines are over it and neither is missed: total written work is ~500 against the
400 guidance, and the simultaneous-update count is 13 (five `logControlResponse` call sites, two
`emitSlashCommandList` ones, six exact-map `wantAttrs` comparisons) against 10. **The split is barred at
depth** — #1826 → #1719 → #1683, confirmed by query this session — so per the depth-capped rule the ticket
is built whole rather than routed back. `needs-human:sizing` is already on it, carrying the refiner's
reasoning; no candidate seam survives its own rule anyway, a docs-only child having no owning pipeline
phase and a record-attribute child having exactly one consumer, its sibling.

No ADR is warranted: the decision is a constant's derivation and belongs in that constant's doc, which is
where this family has kept every sibling derivation.

## The derivation

This is the substance of the slice. Three constraints are in tension and one of them must be spent.

### Re-derived measurements (this session, from the committed capture)

| figure | value | how |
|---|---|---|
| entries | 51 | `commands` array length |
| retained text, all four fields, through `truncateField` | **6,711 B** | byte cut at each cap, then invalid-UTF-8 deleted |
| retained per entry | **131.59 B** | 6711 / 51 |
| uncut text | 11,668 B | matches `maxSlashCommandDescription`'s stated figure |
| raw `commands` array, compact | 14,277 B | matches `protocol.SlashCommandListPayload`'s stated figure |

The 6,711 and 11,668 figures reproduce the package's own; they are re-derived rather than trusted. The
worst-case per-entry term is **1280 B** and is NOT re-opened — it is settled at `maxSlashCommandName`.

### Why both handed-forward candidates are unavailable

`maxSlashCommandDescription`'s fraction table computes 6 entries (at 1/2 of 16 KiB) and 8 (at 5/8) against
the **1280-byte worst case**. Both cut a 51-entry menu by 43 or 45 entries — a cap firing on claude's
ordinary output, which `maxModelListEntries`' NOT 8 paragraph rejects by name and
`maxSlashCommandAliasCount`'s ROLE argument sharpens: a cut command is a working command greyed out in
pyrycode-desktop#694's type-ahead. They are not taken.

### The base moves from the worst case to the observation

Re-computed at the **retained** base (131.59 B/entry) against `maxUnrecognizedRaw`'s 16 KiB whole line:

| fraction | budget | entries |
|---|---|---|
| 1/2 | 8,192 | 62 |
| 5/8 | 10,240 | 77 |
| whole ceiling | 16,384 | 124 |

Changing the base is **not enough**. The second observation is 74 entries (claude 2.1.220, a different
working directory), retaining ~9,738 B — already 59% of the whole unknown-line cap before any cap fires.
1/2 cuts that observation by 12 entries; 5/8 clears it by 3, which is inside the noise of a count that is
workspace- and version-dependent by design. The whole ceiling is what the rule forbids approaching.

### So the ceiling gives, and this is `maxModelListEntries`' own situation verbatim

The family's power-of-two convention leaves exactly two candidates above the 74-entry observation: 64,
which cuts it, and 128, whose projected retained cost (16,844 B) is past the whole 16,384 ceiling. **No
constant this family's doctrine permits fits the inherited ceiling** — which is the sentence
`maxModelListEntries`' doc writes about itself, and it resolved it by moving the ceiling.

What is stated in place of a fraction is that **this shape has no ceiling it can meet**, and that is
argued rather than asserted:

- The worst-case product exceeds the 65,519-byte v2 application-envelope cap at any cap clearing 74
  (74 x 1280 = 94,720). The only count whose worst case fits the envelope is 51, the observation itself.
- `maxUnrecognizedRaw`'s rule is an ORDERING rule — a KNOWN event must not approach the bound on an
  UNKNOWN line — and it presupposes the known event is the cheaper thing. For this shape that
  presupposition is already false at claude's ordinary output, at 0.59 of the ceiling, and no count cap
  can change it.

So the worst-case-product convention is **spent** and the no-fire-on-ordinary-output rule is **kept**.
What the cap buys is stated honestly: the retained size stops being a function of a number the workspace
chooses and becomes a function of a daemon constant, and the drop is COUNTED. What bounds the worst case
is what already bounds it — `defaultMaxParseBuf`'s 4 MiB on the input line — and any FRAME-level bound is
#1720's, when a wire frame exists at all.

### The number: 128

Determined by three constraints with one survivor:

- **Above the larger observation.** 1.73x over 74, 2.51x over the capture's 51.
- **A power of two**, matching every constant in this family except `maxModelListEntries`.
- **Not gratuitous.** 256 is 3.5x the larger observation and projects 33,687 B retained, over twice the
  whole-line ceiling, for headroom neither observation asks for — `maxSlashCommandAliasCount`'s WHY NOT 16.

The multiple sits above the family's cardinality convention (1.67x, 1.6x) and below
`maxSlashCommandAliasCount`'s 4x, and it earns the thicker end for that constant's own two reasons: this
bounds a list a WORKSPACE author writes, whose growth direction nothing claude ships bounds, and a cut
entry costs a working command greyed out in the named consumer's menu.

## Design

### `internal/streamsup/parser.go`

- **New constant `maxSlashCommandListEntries = 128`**, declared after `maxSlashCommandAliasCount`. Named
  for the LIST, parallel to `maxModelListEntries`, so it cannot be confused with the per-entry
  `maxSlashCommandAliasCount` — that constant's own naming paragraph is the reason. Its doc carries the
  derivation above.
- **The cut runs ONCE, in `emitModelList`, at the existing `commands := len(...)` site** — above every
  rung, so there is no second cap for the two rungs to keep in step and the count is available to
  `logControlResponse`, which both rungs call BEFORE any emit. Contract:
  - the retained slice is `entries[:cap]` when over — truncation **from the tail**, claude's order
    preserved, no ranking invented;
  - `commandsDropped` is `len(entries) - cap`, else 0;
  - `commands` becomes the **emitted** count, mirroring `models`, where it was the decoded count.
- **The discriminant is preserved by construction.** The cap is `>= 1`, so emitted `== 0` iff decoded
  `== 0`; rung 3 (neither array) and rung 4 (commands-only) partition exactly as before. Rung 3 passes the
  literal `0` for both new-pair members, matching the existing literal-`0` convention on that rung.
- **`emitSlashCommandList` gains a `dropped int` parameter** and puts it on the emitted value. It does NOT
  apply the cap: the record is written before the call, so a count computed inside a void-returning
  emitter could not reach it. The emitter's empty-list precondition is untouched — when it returns early,
  `dropped` is 0 by construction.
- **`logControlResponse` gains a seventh attribute**, `commands_dropped`, as the LAST parameter and LAST
  attribute so the two orders stay one order. `commands`/`commands_dropped` mirror `models`/`dropped`.
  The NO SEVENTH ATTRIBUTE paragraph's premise is falsified by this slice and is rewritten, not deleted.
- **`emitSlashCommandList`'s tail comment** (NO ENTRY-COUNT CAP and no DroppedCommands) is replaced by the
  statement of what now happens there.

### `internal/turnevent/event.go`

- **`SlashCommandList.DroppedCommands int`**, after `Commands`. Doc mirrors `ModelList.DroppedModels`: how
  many entries the producer cut, 0 when none, true size recoverable as `len(Commands) + DroppedCommands`,
  and the count reports HERE rather than as a name in a top-level `TruncatedFields` for
  `BackgroundTaskRoster.DroppedTasks`' stated reason.
- The NO DroppedCommands FIELD paragraph is replaced by the field; the split-four-ways paragraph and the
  "one unbounded dimension left" paragraph move this slice from future to landed.

### `internal/protocol/interactive.go`

- `SlashCommandListPayload.DroppedCommands`'s doc: "NOTHING COUNTS IT YET, and no entry cap is enforced
  anywhere" is false as of this slice. The correction must keep the wire's position honest — the producer
  counts it, but nothing maps it onto this payload and no frame of this type is produced at all today
  (#1720 owns both), so a client still cannot read `len(Commands) + DroppedCommands` off a frame.
  **No struct field, tag or marshalling changes.**

## Concurrency model

None. `Parser.Write` is the single-goroutine line consumer; every symbol touched is on that call path. No
goroutine is started, no channel added, no lock taken.

## Error handling

No new failure mode. The cut is a slice expression on an already-decoded slice and cannot fail. Decode
errors keep their existing rung and their existing all-zero record; the undecodable arm still logs no
`err`, so no workspace-authored bytes reach a log.

## Testing strategy

Table-driven, stdlib only, mirroring `TestParser_ModelListEntryCountIsBounded` one array over.

- **`TestParser_SlashCommandEntryCountIsBounded`** — the central pin, synthesized lines (the capture is
  under the cap and proves the zero-drop path only):
  - one over the cap drops one; exactly at the cap carries every entry with 0 dropped (the `>` boundary);
    a large array reports how many were lost (a COUNT, not a flag); one under the cap is untouched;
  - tail truncation pinned **per entry by name**, so a head-truncating `entries[len-cap:]` reddens;
  - a record subtest on the model-list rung pinning all seven attributes by exact map equality;
  - a record subtest on the **commands-only** rung, where the model trio is all-zero and the new pair is
    not — the rung the existing doc names as the one where a parameter swap is visible. The two values
    must DIFFER from each other, or a swap between them is invisible.
- **AC 3, classification** — rows proving a capped list still lands on `controlResponseCommandsOnly` with
  its own keyword and still emits exactly one list; the empty and absent arrays keep the ack rung. These
  ride the test above rather than a second test, the discriminant being one branch.
- **AC 4, the capture has headroom** — extend the existing captured-decode assertions: all 51 entries
  survive, `DroppedCommands == 0`, plus an explicit **non-vacuity guard** that the capture's own length is
  strictly under the cap. Without that guard the equality would pass for a cap of 1 and prove nothing.
- **Record-shape fallout** — six exact-map `wantAttrs` comparisons in `parser_test.go` gain the seventh
  key. That they are exact maps is the coverage: any attribute added or renamed reddens all of them.
- The two `parser_test.go` comments naming this ticket as the owner of a bound that does not exist are
  corrected in the same commit as the code.

## Open questions

1. **Should `commands` stay the DECODED count and the new attribute carry the emitted one?** Resolved in
   the design above: the Technical Notes name `maxModelListEntries`' pair — `models` emitted, `dropped`
   cut — as the shape to copy, and copying it keeps one reading of both pairs on one record.
2. **Does anything outside `internal/streamsup` construct a `turnevent.SlashCommandList` positionally?**
   To confirm with `codegraph_callers` before the field lands; an unkeyed composite literal anywhere would
   break the build. Expected: none — the type is constructed at one site.
3. **Whether the capture's own arms differ in entry count.** All three responding arms are documented as
   byte-identical; if an arm differs, the headroom assertion is per arm and the guard catches it.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No finding, and the boundary MOVES in the safe direction. The untrusted-to-held
  crossing is claude's stdout → `json.Unmarshal` into `commandEntryLine` → construction in
  `emitSlashCommandList`. Placing the cut in `emitModelList`, above the emitter, means that emitter's
  `make([]turnevent.SlashCommand, 0, len(entries))` is now bounded by a daemon constant where it was
  previously bounded by the decoded count. **SHOULD FIX:** that allocation bound is a property of the
  cut's PLACEMENT, not of the emitter, so a later refactor moving the cut into the emitter's loop would
  lose it silently. State it at the cut site.

- **[Network & I/O — retained size]** **SHOULD FIX**, and it is the sharpest finding. After this slice the
  worst-case retained text is `128 * 1280 = 163,840` bytes, which is **2.5x the 65,519-byte v2
  application-envelope cap**. No count cap can fix that: the only count whose worst case fits the envelope
  is 51, the observation itself. Nothing produces a frame of this type today — `turnbridge.MapEvent`'s
  default drops the value and `interactiveTurnEmitterV2.Handle` has no case — so the exposure is that
  **#1720's author reads "the entry count is bounded" as "the frame fits"**. The constant's doc must state
  the worst-case product in bytes and name #1720 as the owner of the frame-level bound. The lower-the-cap
  alternative is barred by the ticket and by `maxSlashCommandAliasCount`'s ROLE argument; the
  lower-the-text-caps alternative is barred outright, those four constants being settled.

- **[Network & I/O — transient amplification]** **SHOULD FIX**, and the copyable cross-reference is WRONG
  for this struct. `maxTaskRosterEntries` states its amplification as "linear and near 1" (~55 bytes of
  input for a ~64-byte struct), and the sibling slash-command constants cite it. `commandEntryLine`'s
  densest legal entry is `{},` — three bytes of input, since every key is optional and absence is claude's
  to choose — for a 72-byte struct (three string headers plus one slice header). So the amplification is
  **~24x, not near 1**: a single `defaultMaxParseBuf`-sized 4 MiB line yields ~1.4M entries and ~100 MB of
  transient allocation before the cut discards all but 128. Bounded (one line at a time, reclaimed, no
  retention past `emitModelList`) and therefore not MUST FIX, but the figure must be COMPUTED in the new
  constant's doc rather than inherited from a paragraph written about a different struct.

- **[Concurrency / retention]** No finding, and the reason is worth recording because it looks like a
  violation of a rule this same file states. `boundAliases`' doc refuses to return `values[:n]` because
  its result IS retained by the emitted event. The entry-count cut MAY reslice — `entries[:cap]` shares
  the decoder's backing array — because its result is NOT retained by the event: `emitSlashCommandList`
  appends constructed values into a fresh allocation, and the resliced header dies with `emitModelList`.
  `maxModelListEntries`' own block reslices for exactly this reason. **SHOULD FIX:** say so at the site,
  or a reader who knows `boundAliases`' rule will read the reslice as a bug.

- **[Error messages, logs, telemetry]** No finding. The record's content-free rule is preserved by
  construction: `commands_dropped` is `len(slice) - const`, a daemon-computed integer carrying none of
  claude's bytes, which is the same footing that admits the four integers already there. The obligation
  this creates on the implementation is concrete — the value must never be derived from, or accompanied
  by, a dropped entry's name. No `err` is added to any arm; the undecodable arm still logs none, which is
  what keeps workspace-authored bytes out of the log when `encoding/json` quotes them into its error text.

- **[Subprocess / external command execution]** No finding, by design rather than by absence. Values on
  this path come FROM a subprocess and none returns to one; `turnevent.SlashCommandList`'s SECURITY
  paragraph already states that no field may reach a child as an argv element. The new value is an `int`
  computed from slice lengths and reaches only one `slog` attribute and one struct field.

- **[Tokens, secrets, credentials]** Not applicable, and the decision that makes it so is explicit: this
  path carries workspace-authored command names, hints, descriptions and aliases — never a credential —
  and `logControlResponse` admits integers only. #833's "model / effort / YOLO values are NEVER logged"
  posture is untouched.

- **[File operations]** Not applicable. No filesystem path is constructed, read or written by production
  code in this slice. The capture is read by a TEST helper, `capturedInitializePayload`, from a committed
  fixture at a path built from a constant arm name rather than from any input.

- **[Cryptographic primitives]** Not applicable. No randomness, no hashing, and no comparison against a
  secret; the one new comparison is `len(entries) > maxSlashCommandListEntries` on non-secret data, where
  constant-time behaviour is irrelevant.

- **[Threat model alignment]** The relevant threat is a hostile or compromised workspace publishing an
  inventory large enough to exhaust the daemon or to present a shortened menu as complete. Both halves are
  addressed: the count is bounded and the drop is counted, so a short menu says it is short. **OUT OF
  SCOPE and named:** the frame-level envelope bound and the client's render boundary belong to #1720.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02
