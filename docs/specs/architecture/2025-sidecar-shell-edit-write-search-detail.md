# #2025 — result counts for shell, edit, write and search tool rows

Split from #1794. Sibling: #2024, which landed the carriage, the two-stage decode, the
fail-closed default, `turnevent.ToolUpdate.ResultDetail`, the `turnbridge` mapping and the
`protocol` wire field. **This slice adds no plumbing**: four composers and four doc-comment
corrections, all inside the seam #2024 built.

## Files read

- `internal/streamsup/parser.go` → `readLineCount` — the function being restructured. Its
  own doc says it is "narrowed to the ONE shape this ticket maps" and names #2025. It is a
  single-shape composer, **not** a dispatch with arms, so this slice builds the dispatch and
  renames it; the name is the ticket's one anticipated finding.
- `internal/streamsup/parser.go` → `toolResultSidecar`, `sidecarFile` — the decode targets.
  `sidecarFile`'s doc states the two rules this slice must extend: *a field that is never
  declared cannot leak*, and *pointers so an absent key is distinguishable from a present
  zero*. The second is exactly what `structuredPatch: []` needs.
- `internal/streamsup/parser.go` → `userToolResultLine` — the envelope is `tool_use_result`,
  snake_case, and **the rename stops there**: every key inside stays camelCase. Unchanged
  here, but it decides how every new fixture is wrapped.
- `internal/streamsup/parser.go` → `emitUser`, `countToolResultBlocks` — the one production
  call site, and the multi-block rule that stays untouched.
- `internal/streamsup/parser.go` → `maxResultDetailBytes` — the derivation to redo across
  five forms. Also `defaultMaxParseBuf`, which bounds a complete line and therefore bounds
  every string the two counting arms walk.
- `internal/turnevent/event.go` → `ToolUpdate` — doc comment #2 of the four to correct.
- `internal/turnbridge/outbound.go` → the `turnevent.ToolUpdate` arm — doc comment #3; the
  uncapped mapping whose stated reason must survive the correction.
- `internal/protocol/interactive.go` → `ToolResultPayload` — doc comment #4. No struct
  change: the field already exists and no fixture moves.
- `internal/streamsup/parser_test.go` → `sidecarLine`, `readSidecar`, `sidecarShellLine`,
  `TestParser_SidecarFailsClosed`, `TestReadLineCount_BoundedByConstruction` — the fixture
  helpers to reuse and the two tests whose expectations this slice changes.
- `internal/agentrun/jsonl/testdata/clean.jsonl` — the one committed write observation.
  Verified: `type: "create"`, `structuredPatch: []`, `content` 27686 B / 321 lines,
  `originalFile: null`, key set `content filePath originalFile structuredPatch type
  userModified`.
- `internal/e2e/realclaude/testdata/tool_result_sidecar_v2.1.239.json` — the committed
  stdout capture; its shell `user` line is already inlined as `sidecarShellLine`.
- `docs/knowledge/features/streamsup-package-content-blocks-are-held-as-json-rawmessage.md`
  § "Tool-result sidecar decode" — #2024's folded lesson. It states the finding this design
  inherits: **no sidecar path may ever emit `Unrecognized`**, because `emitUnrecognized`
  puts the offending bytes on the wire, not merely in a log.
- `docs/knowledge/features/protocol-package-drift-detectors.md` — checked, does not apply:
  every detector keys on a `Type*` constant and this slice adds none.

## Context

#2024 filled in one of five sidecar shapes. This slice fills in the other four, so the same
glance that works on a read row works on a shell, edit, write and search row.

Identification stays #1678's rule — driven by the available data, not by the names. Each
shape is self-identifying by its **key set**, so there is no correlation back to the
`tool_use` and no tool-name switch. Two consequences the census forces:

- **Identify by the presence of the named keys, never by an exact key set.** 288 of 4883
  observed shell sidecars (5.9%) carry a sixth key, and 90 edit/write sidecars carry an
  extra one. An arm keyed on "exactly these keys" loses all of them.
- **`structuredPatch` is `[]` on every observed create (240 of 240).** Present-but-empty
  must be distinguishable from absent, which is `sidecarFile`'s pointer rule again.

No ADR is warranted: four arms inside an established dispatch, no new decision to record.

### Size — measured against the boundary, with the reject-branch line addressed head-on

Four of the six boundaries hold with room: **4** production files (`parser.go`, plus one doc
comment each in `event.go`, `outbound.go`, `interactive.go`), **0** new exported types or
interfaces, **4** call sites for the rename (`emitUser` plus three inside one test function,
counted with `codegraph_callers`), and **5** acceptance criteria. The other two both need
stating rather than waving through.

**Total written work — the plan measures 387 lines of spec and projects ~600 more, so ~990
against the 800-line line.** The overage is real and is recorded here rather than argued
away. It does not resolve to a split, for a reason a split would make worse: both candidate
children (shell+search, edit+write) would re-do the same dispatch restructure and each carry
its own mandatory security review, so slicing *raises* total written work while halving it
per run. What the ceiling protects against is a budget miss, and that risk is measured on
this exact seam — #2024, the sibling in this file one day earlier, wrote a re-derived 1222
lines (390 spec + 832 feat) in one run without exhausting, and this slice's implementation
half is materially smaller than its 832 because it adds no plumbing: no wire field, no event
field, no fixture, no `protocol`/`turnbridge` test changes. The pipeline's own calibration
note puts the median merged PR at ~920 lines including spec. The floor rule does **not**
rescue this — both candidate children stand alone and are verifiable alone — so this is an
overage stated and accepted, not an overage explained away.

The sixth — reject branches ≤ 10 — needs stating too, because the
finished function ends at 12-15 no-count paths depending on how you count, and the refiner
called it "about ten … the boundary".

**The metric cannot be satisfied by splitting this ticket, because it is cumulative over a
function that already exists.** #2024 put 7-8 reject reasons into this dispatch. Any slicing
of the remaining four shapes leaves the *same* finished function behind, so both halves of
any split measure identically over the boundary; a split changes the count by zero. The
quantity a split can actually change is the branches **this slice adds**, which is 8 — two
per arm — and that is under the line. The boundary is a turn-cost proxy for state machines
whose reject branches each carry a per-branch log call; here there are zero log calls by
security design (see § Security review, [Error messages]), and every reject is a bare
`return ""` inside an `if` the arm needs anyway.

Recorded so the verifier can disagree with the reading rather than guess at it: the count on
the finished function is over, the count on the delta is under, and this ticket builds as
one.

## Design

Everything below is in `internal/streamsup/parser.go` unless named otherwise.

### The dispatch

`readLineCount` becomes `toolResultDetail(sidecar json.RawMessage) string` — same contract
(row text, or `""` for everything unrecognised), same total-function shape, now five arms
tried in a fixed order:

**read → shell → edit → write → search**, each returning `""` on non-match, and `""` at the
end when none matched. The order is stated so it is deterministic rather than incidental;
the observed key sets are disjoint, so no sidecar reaches a second arm having matched a
first. Fall-through is what makes AC5 hold for free: a `structuredPatch` sidecar matching
neither patch arm, and a write whose `type` is neither `create` nor `update`, both run off
the end.

### Decode targets — two stages, because one flat struct cannot honour the confinement rule

Stage 1 is `toolResultSidecar`, widened to a **shape discriminator**: safe scalars by value,
everything else by presence only.

```go
type jsonKey struct{}                                  // present iff the key is, no bytes kept
func (*jsonKey) UnmarshalJSON([]byte) error { return nil }
```

A `*jsonKey` field is non-nil exactly when the key is present with a non-null value, and
retains **no byte** of it — verified against `encoding/json` for a string, an object, `null`
and `[]`. That is what makes `structuredPatch: []` recognisable without decoding hunks, and
what lets `oldString` / `newString` — the entire pre- and post-edit text — act as
discriminators without being read into daemon state at all.

Stage 1 carries: `File *sidecarFile` (read, unchanged), `Stdout`/`Interrupted` (shell),
`StructuredPatch`/`OldString`/`NewString`/`Content`/`Mode` as `*jsonKey`, `Type *string`,
and `NumLines`/`NumFiles` as `*int64` (search's two top-level counts — note these are
*top-level*, where the read's identically-spelled `numLines` is nested under `file`).

Stage 2 exists for the three arms that need an unbounded claude-supplied value, and each
gets its **own narrow target** re-decoded from the same `json.RawMessage`:

| Arm | Stage-2 target | Why it cannot be a shared field |
| --- | --- | --- |
| shell | `struct{ Stdout string }` | — |
| edit | `struct{ StructuredPatch []patchHunk }`, `patchHunk{ Lines []string }` | — |
| write | `struct{ Content string }` | **`content` is spelled the same on a search sidecar, where it is the whole grep output.** A shared `Content` field would put it on the search arm's decode target, which the ticket forbids. |

`filePath`, `originalFile` and `filenames` are declared **nowhere**, in either stage.

### The five composers

| Arm | Matches on | Sends |
| --- | --- | --- |
| `readDetail` | `file.numLines` **and** `file.totalLines` | unchanged (`4 lines` / `40 of 256 lines`) |
| `shellDetail` | `stdout` **and** `interrupted` present | `countLines(stdout)` + `" lines"`; **empty stdout sends nothing** |
| `editDetail` | `structuredPatch`, `oldString`, `newString` all present | `+10 −3`, either half omitted when zero, nothing when both are |
| `writeDetail` | `structuredPatch` and `content` present, `type` is `create`/`update` | `created · 54 lines` / `updated · 54 lines`, including at 0 |
| `searchDetail` | `mode` present | `numLines` → `78 lines`; else `numFiles` → `5 files`; negative or neither → nothing |

`stderr` is not declared, so it cannot be counted into the shell number by accident — the
confinement rule doing double duty as a correctness one.

**`countLines(s string) int64`** pins the rule for the two arms that count rather than being
told: `""` is 0, and a trailing newline does not add a line, so `"a\nb\n"` and `"a\nb"` are
both 2. Implementation is `strings.Count` with one suffix test; no allocation, no split.

**The edit's two numbers come only from the `+`/`-` prefixes of the hunks' `lines`.**
`oldLines`/`newLines` are hunk *spans* with context included, so differencing them yields
the net change and not `+10 −3`; neither is declared. `+` and `-` here are the diff's own
ASCII prefix bytes — the U+2212 in the output is display text and never appears in input.

### Glyphs

`+` is U+002B; the separator is **U+2212 MINUS SIGN**, not a hyphen. The write row uses
**U+00B7 MIDDLE DOT** spaced on both sides. They are display text the client renders
verbatim, so they are contract, not formatting preference.

They are the first non-ASCII bytes this field has carried, and four committed doc comments
say it carries none. All four are corrected here — `maxResultDetailBytes`,
`turnevent.ToolUpdate`, the `turnevent.ToolUpdate` arm of `turnbridge`'s mapper, and
`protocol.ToolResultPayload`. **The load-bearing half of all four stays true and must:
no claude-supplied byte reaches the field.** Only the alphabet, the composer count and the
function name change.

### The bound, re-derived across all five forms

Every count formats through `strconv.FormatInt` over a non-negative `int64`, so each is at
most 19 digits:

| Form | Worst case | Bytes |
| --- | --- | --- |
| read, two-part | `19 + len(" of ") + 19 + len(" lines")` | **48** |
| edit, both halves | `len("+") + 19 + len(" ") + 3 + 19` | 43 |
| write | `len("updated") + 1 + 2 + 1 + 19 + len(" lines")` | 36 |
| shell / search | `19 + len(" lines")`, `19 + len(" files")` | 25 |

`maxResultDetailBytes` stays **48**. `encoding/json` escapes neither glyph — verified by
marshalling both — so 48 remains the wire cost too and `turnbridge`'s uncapped mapping keeps
its stated reason. What changes is that the constant's doc can no longer say every byte is
ASCII: it is digits, spaces, ASCII letters and **two multi-byte runes**, U+2212 and U+00B7,
and it should say so.

## Concurrency model

None added, stated rather than assumed. Every composer is a pure function over bytes the
caller already owns, called synchronously on the same os/exec forwarder goroutine that
already runs `consumeLine` → `emitUser`. No goroutine, no lock, no shared mutable state, no
shutdown path.

## Error handling

Unchanged from #2024, and that is the point: **every failure has the same answer — send no
count.** There is one signalling mechanism (the empty string) and no error value crosses a
layer boundary. The three stage-2 `json.Unmarshal` calls each fail closed the way stage 1
does; a type mismatch (`"stdout": 123`) costs a missing count and never a wrong one.

**No sidecar path emits an `Unrecognized`, ever** — inherited from #2024 as a confinement
control, not a tidiness one, and re-proven by every new case asserting the whole event list.
No new log call takes the line, the sidecar, or any part of either.

## Testing strategy

`internal/streamsup/parser_test.go`, extending the existing `#2024` block. Table-driven, one
raw line through the real parser via `collectEvents`, asserting the **whole event list** —
which is what keeps the no-`Unrecognized` rule enforced rather than asserted.

**Fixture provenance is per-shape and must be labelled per-shape**, because only two of five
are observed on stdout:

- **shell** — `sidecarShellLine`, already inlined from the committed stdout capture. Its
  expectation moves from "no count" to `1 lines`, so `TestParser_SidecarFailsClosed` loses
  that case to the new accept table.
- **write** — the nested sidecar object from `clean.jsonl`, **re-wrapped under
  `tool_use_result`**. A `user` line lifted whole from a transcript carries the camelCase
  envelope and would exercise nothing. Its `content` is 27686 B; the fixture keeps the
  observed `type`/`structuredPatch: []`/key set and substitutes a short content, since the
  count is computed by the daemon and not observed. Labelled as such.
- **edit, search** — no committed evidence on either surface. Authored from #1794's census
  and marked **authored-from-census, not observed-on-stdout**, so a later capture can
  confirm them. Deliberate: read and shell were byte-identical across both surfaces, #2023
  established the rename stops at the envelope, and fail-closed means a wrong guess costs a
  missing count, never a wrong one.

Scenarios, one per AC clause:

- **AC1 shell** — the captured stdout line sends its count; a case whose `stderr` is
  strictly longer than its `stdout` sends the `stdout` number only (a decoder counting the
  wrong field fails it); a case carrying `gitOperation` still sends its count (the
  presence-not-key-set rule); empty `stdout` sends nothing.
- **AC2 edit** — a hunk whose context lines **outnumber** its changed ones, so a count taken
  from `oldLines`/`newLines` produces a different number and fails; additions-only and
  removals-only each omit the empty half; the separator bytes are asserted as the exact
  U+2212 / U+002B runes rather than by eyeballing the literal.
- **AC3 write** — `create` and `update`, both with `structuredPatch: []`, so an arm
  requiring a non-empty patch fails; empty `content` still sends `created · 0 lines`; the
  trailing-newline rule pinned by a `"a\nb\n"` / `"a\nb"` pair reaching the same number.
- **AC4 search** — `numLines` present **alongside `numFiles: 0`**, which pins precedence
  rather than leaving it incidental; `numFiles` with no `numLines` sends a file count.
- **AC5 fail-closed** — `structuredPatch` matching neither arm; `mode` with neither count;
  `type: "delete"` with a patch and content; an edit whose halves are both zero. Each
  asserts the whole event list.
- **Confinement** — one case per new shape carrying a distinctive marker in every field the
  design refuses to declare (`filePath`, `originalFile`, `filenames`, search `content`,
  `stderr`), asserting the marker appears nowhere in the marshalled event. Markers are
  non-empty and distinctive because `strings.Contains(s, "")` is true for every `s`.
- **Bound** — `TestReadLineCount_BoundedByConstruction` is renamed with the function and
  extended: each of the other four forms at its worst case is shorter than
  `maxResultDetailBytes`, and the only non-ASCII runes any form can produce are U+2212 and
  U+00B7.

Non-vacuity is established by **mutation**, run under `go test -overlay` with no worktree
writes, not by argument: at minimum, flipping the edit arm to difference `oldLines`/
`newLines`, and forcing the write arm to require a non-empty `structuredPatch`. Each must
turn cases red.

Gate (§ B2): `go test -race ./internal/streamsup/... ./internal/turnevent/...
./internal/turnbridge/... ./internal/protocol/...`, `go vet ./...`, `go build ./cmd/pyry`,
`make cite-guard`.

## Open questions

1. **Does the shell arm key on `stdout` alone or on `stdout` + `interrupted`?** Resolved
   before the plan commit: **both**. `stderr` is the third observed member of the triple but
   declaring it — even as a presence marker — puts the shape's second unbounded string on
   the decode target for no gain, and two keys already guard against a future shape carrying
   a bare `stdout`.
2. **Does the write arm reuse the edit arm's decoded hunks?** No. It needs only that
   `structuredPatch` is *present*, which the stage-1 `*jsonKey` answers, so an `update`
   write never decodes its hunks at all.
3. **Should `searchDetail` send the two-part `78 of 120 lines` form?** No, per the ticket:
   `totalLines` rides along on 200 of 200 observed cases, but the read's two-part form
   exists because reads are partial 56.5% of the time and no equivalent has been measured
   for search. One number.
4. **A present-but-invalid `numLines` — reject, or fall back to `numFiles`?** Raised by the
   security pass and resolved before the plan commit: **reject.** Precedence is decided by
   *presence*, so falling back would let a malformed field silently change which quantity
   the row reports — a wrong count, which is the one outcome this design refuses. Pinned by
   a test case, not left to the code.

## Security review

**Verdict:** PASS

The assets this slice puts at risk are strictly richer than #2024's. Beyond the read's
`file.content`, these four shapes carry `originalFile` — **the entire pre-edit file** — on
both patch shapes, `filePath` and `filenames` (absolute paths disclosing the operator's home
and project layout), `content` on a search sidecar (the whole grep output), and `stderr`.
The consumer at the far end is a mobile client across a relay. Everything below is measured
against that, and against the fact that #2024 had **one** decode of the sidecar where this
design has four.

**Findings:**

- **[Trust boundaries] SHOULD FIX — the boundary widens from one decode to four, and must
  stay visibly one boundary.** `toolResultDetail` remains the single named crossing from
  untrusted stdout bytes into daemon state, but the checklist's "explicit, or scattered
  across three parses?" question now has a real edge: stage 1 plus three stage-2 targets are
  four `json.Unmarshal` sites. In Phase B: every one of them lives inside `toolResultDetail`
  or an arm it calls, all unexported, all in one file; **a stage-2 struct must never escape
  its composer** — each arm returns a `string`, never a decoded value, so no caller can hold
  claude's bytes. The stage-1 discriminator is what keeps this honest: it decodes *presence*,
  not values, so the widening buys shape recognition without buying exposure.

- **[Tokens, secrets, credentials] MUST-NOT list, satisfied structurally — with two
  deliberate exceptions that must not become three.** `originalFile`, `filePath`,
  `filenames`, `stderr` and the search shape's `content` are declared in **neither** stage,
  so they cannot be read into daemon state at all — `sidecarFile`'s rule, and the reason the
  write arm gets its own narrow `content` target instead of a shared field it would share
  with search. The two unbounded claude-supplied values that *are* decoded, because a count
  is computed from them, are the shell's `stdout` and the write's `content`; the edit's hunk
  `lines` are the third. In Phase B: **no composed string may be built from a sub-slice of
  any of them.** `countLines` returns an `int64` and the composers return
  `strconv.FormatInt` output, so no byte survives the composer and nothing pins the decode's
  allocation the way an `s[:n]` "cap" silently would.

- **[Error messages, logs, telemetry] MUST-NOT, satisfied by construction — and the stakes
  are higher than #2024 measured.** #2024 found that `emitUnrecognized` does not merely log:
  it puts the offending bytes on the wire as `turnevent.Unrecognized.Raw`, cut by
  `truncateRaw`, and ships them to the phone. On a read sidecar that would have leaked the
  file claude read. **On an edit sidecar it would leak `originalFile`, the entire pre-edit
  file, and on a search sidecar the grep output plus absolute paths.** The rule therefore
  holds unchanged and harder: no sidecar path emits an `Unrecognized`, ever, and no new log
  call takes the line, the sidecar, or any decoded value. No composer receives a
  `*slog.Logger`, which is what makes it structural rather than a habit.

- **[Network & I/O] No finding that changes the design; one real amplification named rather
  than inherited silently.** Per sidecar-bearing line, peak transient memory rises from
  #2024's ~2× the line (raw line + `json.RawMessage` copy) to ~3×, because a recognised
  shell, edit or write sidecar additionally materialises `stdout`, the hunk `lines`, or
  `content`. That multiplier is over a **bounded** quantity: `Parser.Write` caps its
  accumulator at `defaultMaxParseBuf`, so one line is roughly 4 MiB plus a single `Write`
  chunk (the correction #2024's own Revisions entry recorded). The allocation is transient,
  single-goroutine, and freed when `emitUser` returns. **A cheaper alternative was
  considered and rejected**: counting newlines on the raw JSON bytes via a custom
  `UnmarshalJSON` would avoid materialising `content`, but JSON escapes a newline as the two
  source bytes `\` `n`, so it would require hand-rolled string unescaping — a correctness
  hazard traded for a memory win on a bounded allocation. Outbound size is unchanged: 48
  bytes by construction (§ The bound), still the read form, still under 1.6% of the
  envelope headroom.

- **[Threat model alignment] One concrete gap found — a fifth stale claim the ticket did not
  enumerate.** `docs/protocol-mobile.md` § Security model treats an authenticated phone as
  the recipient, and the exposure this slice could add is unintended daemon→phone disclosure
  of local file contents and paths, addressed by the two structural controls above. But the
  ticket lists **four** doc comments falsified by the glyphs, and there are **five**:
  `internal/turnbridge/outbound_test.go` → `TestToolResultPayload_FitV2EnvelopeCap` justifies
  its 48-byte ASCII fill with "that IS the producer's alphabet — it emits no byte
  `encoding/json` escapes". After this lands the alphabet includes U+2212 and U+00B7, and
  the fill is still correct for a *different* reason: the read form is the longest of the
  five and happens to be the all-ASCII one. MUST FIX in Phase B — an uncorrected claim there
  would leave the envelope guarantee resting on a stated reason that is no longer true. (Not
  a MUST FIX against the plan: it is an addition to the work, not a hole in the design.)
  Reported as a comment on the ticket, per its own instruction about findings #2024 did not
  anticipate.

- **[File operations] Not applicable, by decision rather than by absence.** No path is
  opened, joined, canonicalised or `Stat`ed anywhere in this slice. `filePath` and
  `filenames` are present in the observed sidecars and deliberately undecoded: mapping
  either would put an operator's absolute paths on the wire for nothing the row needs.

- **[Subprocess / external command execution] Not applicable.** The sidecar is *output* of
  an already-spawned child. Nothing decoded here flows back into any argv, environment or
  stdin write, and nothing in this slice execs. The edit arm reads `+`/`-` diff prefixes as
  data and never as a command.

- **[Cryptographic primitives] Not applicable, stated rather than skipped.** No randomness,
  no key material, no derivation. The equality tests are `int64` comparisons of claude's own
  counts and two string comparisons of `type` against the public literals `"create"` and
  `"update"`; neither side of either is a secret, so `crypto/subtle` has nothing to protect
  and using it would imply a threat that does not exist.

- **[Concurrency] Not applicable, stated rather than assumed.** No goroutine, no lock, no
  shared mutable state, no shutdown path. Every composer is a pure function over bytes the
  caller already owns, running synchronously on the os/exec forwarder goroutine that already
  runs `consumeLine`.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02

## Revisions

### 2026-09-02 — implementation

**The design shipped as planned.** No arm changed shape between the plan and the code, and
all four Open questions resolved as written: the shell arm keys on `stdout` **and**
`interrupted`; the write arm never decodes hunks; `searchDetail` sends one number; and a
present-but-invalid `numLines` rejects rather than falling back to `numFiles`.

**The ticket's one anticipated finding, confirmed.** `readLineCount` was a single-shape
composer, not a dispatch. It is now `toolResultDetail` over `readDetail` / `shellDetail` /
`editDetail` / `writeDetail` / `searchDetail`, with `countLines` shared by the two arms that
count rather than being told. Four call sites moved, as counted.

**Two small departures from the plan's wording, neither a design change.** The stage-2
targets shipped as named types (`sidecarStdout`, `sidecarPatch`, `sidecarContent`) rather
than as the anonymous structs the plan sketched, so each can carry the doc that states why
it is separate. And the bound test was renamed *and* given a sibling —
`TestToolResultDetail_BoundedByConstruction` keeps the read's 48-byte measurement, while the
new `TestToolResultDetail_OtherFormsAreShorter` measures the other four and pins the
alphabet — rather than being extended in place, because the two assert different things.

**The security review's [Threat model] finding landed.** The fifth stale claim is corrected:
`TestToolResultPayload_FitV2EnvelopeCap` no longer justifies its all-ASCII 48-byte fill with
"that IS the producer's alphabet", which stopped being true here, but with the reason that
survives — the read form is the longest **of five** and happens to be the all-ASCII one. The
new `TestToolResultDetail_OtherFormsAreShorter` is what keeps that half honest. Reported as
a comment on the ticket. `maxResultDetailBytes` is unchanged at **48**: re-derived across all
five forms it beats edit's 43, write's 36 and shell/search's 25, so the envelope measurement
does not move and `TestToolResultPayload_FitV2EnvelopeCap` needed no re-measurement.

**Non-vacuity, established by mutation rather than by argument.** Three mutants under
`go test -overlay` (no worktree writes), each caught by precisely the case its acceptance
criterion names:

- `editDetail` differencing the hunks' `oldLines`/`newLines` instead of counting `+`/`-`
  prefixes: the context-heavy hunk and the multi-hunk case both red. This is the mutant that
  matters most — the wrong implementation is *plausible*, and a fixture whose context lines
  did not outnumber its changed ones would have agreed with it.
- `writeDetail` requiring a non-empty `structuredPatch`: the `structuredPatch: []` create
  went red, so the arm is proven against the form 240 of 240 observed creates carry.
- `shellDetail` counting `stderr` instead of `stdout`: the longer-stderr case went red,
  so "stderr is not counted into it" is measured rather than asserted.

**Size, measured.** 907 insertions across six files plus a 401-line spec — ~1300 written
lines against the ~990 the plan projected and the 800-line boundary. The overage the plan
stated up front is therefore larger than stated, and the reason is the one the plan named
and accepted: the boundary does not resolve to a split here. Recorded so the calibration is
against a real number, not the estimate — and so a future ticket in this file sizes against
1300 rather than against 750.
