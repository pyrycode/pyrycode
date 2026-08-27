# #1727 — Slash-command-list payload, entry type and nil-encoding pins

Wire vocabulary only. Two exported types in `internal/protocol/interactive.go`, their two
`MarshalJSON` methods, two encoding pins, and one regression block appended to a test #1726
already shipped. No producer, no emit, no handler, no relay wiring, no committed fixtures,
no `docs/protocol-mobile.md` (that is #1718's), no `docs/knowledge/**` (the documentation
phase owns those).

## Files to read first

| Read | Symbol | What to extract |
|---|---|---|
| `internal/protocol/interactive.go` | `ModelListPayload` + its `MarshalJSON` | The payload doc form and the nil-list normalisation rationale, which transfers whole. This is the shape to mirror. |
| `internal/protocol/interactive.go` | `ModelOption` + its `MarshalJSON` | The entry doc form, the COLLAPSE rationale for a normalised list, the `TruncatedFields` carve-out, and the SECURITY paragraph this spec strengthens. |
| `internal/protocol/interactive.go` | `BackgroundTaskRosterPayload` + its `MarshalJSON` | The original "an empty list is a positive statement" reason both marshallers above cite by name. Read it so the citation is not repeated blind. |
| `internal/protocol/interactive_test.go` | `TestModelListPayload_NilModelsNormalises` | The exact pin shape: nil precondition, value/pointer subtests, post-marshal receiver-not-mutated assertion. |
| `internal/protocol/interactive_test.go` | `TestModelOption_NilSliceEncodings` | The three-way asymmetry pin: shared `assertEncodings` helper, value/pointer subtests, the nested-in-payload subtest and its backing-array assertion. |
| `internal/protocol/interactive_test.go` | `TestModelListType_IsNotClaudesVocabulary` | Its **closing payload-bytes block** is the half AC 4 adapts — and its literal check list contains `description`, which this shape adopts. Read it for the shape, not for the list. |
| `internal/protocol/interactive_test.go` | `TestSlashCommandListType_IsNotClaudesVocabulary` | The test this ticket **extends**. Its closing paragraph says there is no payload-bytes half "until #1727" — that paragraph becomes false and must be replaced. |
| `internal/protocol/codes.go` | `TypeSlashCommandList` | The doc block already carries the direction, the four-claude-words containment lattice, the guard classification and the why-no-request-verb reasoning. Do **not** restate any of it in `interactive.go`. |
| `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` | `control_responses[0].response.response.commands` | The 51-entry capture every measured figure in this spec is derived from. The `stdout_events` copy of the same array is byte-identical. |
| `docs/knowledge/features/protocol-package.md` | § "Model-list payload (#1704 shape…)" | **Two recorded defects not to repeat** — `ModelOption.TruncatedFields` enumerates 2 of 3 credited text fields, and `ModelListPayload`'s `DroppedModels` doc contradicts the shipped `docs/protocol-mobile.md`. Both are avoidable here at zero cost. |
| `docs/protocol-mobile.md` | § `model_list`, the `dropped_models` paragraph | The **honest** phrasing of an uncounted drop field. Mirror this wording in the Go doc from the start, not the Go doc's. |

## Context

The desktop's Actions menu offers reset, compact and knowledge capture as slash commands in
an ordinary message. Knowledge capture is workspace-specific, so a menu that always offers it
is wrong in most repositories and produces an "Unknown command" reply. claude already answers
the question: a `control_request` with subtype `initialize` on the child's held-open stdin
returns a `commands` array alongside the `models` array — one round trip, two payloads, no new
trust boundary.

This is the declare-then-emit sequencing used for `rate_limited` (#1405 → #1410),
`model_announced` (#1616 → #1638) and `model_list` (#1704 → #1693). The wire type constant and
both structural guard classifications landed in **#1726**; this slice declares the shape a
client decodes; **#1720** produces and emits it; **#1718** adds fixtures and the
`docs/protocol-mobile.md` section. Two consumers are blocked on this shape today:
pyrycode-desktop#681 (Actions-menu grey-out, matches menu entries against the list) and
pyrycode-desktop#694 (type-ahead, renders name + argument hint + description per row).

**No ADR is warranted.** This mirrors an established, twice-documented pattern rather than
choosing between alternatives. Everything decidable here is decided in the type's own doc
comment, which is where the three sibling payloads keep theirs.

### Measured facts this shape must respect

Every figure below was re-derived from the committed capture during this spec run; all match
the ticket body.

| Fact | Measurement |
|---|---|
| Entries | 51 |
| Key sets, and there is no third | 42 carry `name` + `description` + `argumentHint`; 9 carry those three **plus** `aliases` |
| Empty argument hints | 33 of 51 — and **no** entry omits the key |
| `"aliases": []` | **zero** entries; 42 omit the key entirely, 9 carry a non-empty array |
| Aliases | 11 across 9 entries: `clear`→`reset`,`new`; `code-review`→`review`; `doctor`→`checkup`; `loop`→`proactive`; `schedule`→`routines`; `config`→`settings`; `rename`→`name`; `usage`→`cost`,`stats`; `list-agents`→`peers` |
| Compact UTF-8 size of the array | 14,277 bytes |
| Longest name / hint / description | 24 (`fewer-permission-prompts`) / 121 / 1,145 bytes; mean description 207, median 69, 10 of 51 over 256 |
| Non-ASCII | 14 of 51 descriptions — bytes and runes genuinely disagree on this data |
| Sub-`0x20` bytes anywhere across all four string fields | `0x0a` only, in exactly one description (`claude-api`) |

Three consequences the design follows from:

1. **No string key can be optional.** An empty argument hint is the ordinary case (33/51), not
   missing data, so eliding it would make the common row indistinguishable from a malformed one.
2. **`aliases` is load-bearing.** The desktop's own **reset** entry is an *alias* of `clear`,
   not a command name. A client matching against `name` alone greys out a command that works.
   This is also why the cheap source cannot answer the question: the `system`/`init` line's
   `slash_commands` carries the same 51 names in the same order as bare strings and **none** of
   the 11 aliases.
3. **The count is workspace- and version-dependent.** 51 here against claude 2.1.239; an earlier
   hand count against 2.1.220 in a different working directory reported 74. No client may assume
   a count, which is why the list is per session and per working directory.

## Design

One production file: `internal/protocol/interactive.go`, appended after `ModelOption` and its
`MarshalJSON`. Two exported types, two value-receiver marshallers.

### Type names

`SlashCommandListPayload` and `SlashCommand`. The payload follows `ModelListPayload` /
`BackgroundTaskRosterPayload`; the entry follows `BackgroundTask` (the frame's subject noun,
not an invented `…Entry` / `…Option` suffix). `codegraph_search` confirms neither name exists
anywhere in the tree today — the only `SlashCommand*` symbols are `TypeSlashCommandList` and
`TestSlashCommandListType_IsNotClaudesVocabulary`.

### Wire shape

Contract sketch — the doc comments, not this block, are the deliverable's bulk:

```go
type SlashCommandListPayload struct {
    ConversationID  string         `json:"conversation_id"`
    Commands        []SlashCommand `json:"commands"`
    DroppedCommands int            `json:"dropped_commands"`
}

type SlashCommand struct {
    Name            string   `json:"name"`
    ArgumentHint    string   `json:"argument_hint"`
    Description     string   `json:"description"`
    Aliases         []string `json:"aliases"`
    TruncatedFields []string `json:"truncated_fields"`
}
```

No `omitempty` on any of the eight keys. Field order follows AC 1's own order, with
`TruncatedFields` last as `ModelOption` places it.

**The list key is `commands`, and that decision is load-bearing twice.** It mirrors `models` /
`tasks` (each sibling payload keys its list on the bare plural of its entry noun, adopting
claude's array key where that is the natural word — only the *wire type name* has to be the
daemon's, which is #1726's settled deliverable). And per the ticket's containment lattice it is
the choice that leaves **both** of claude's array keys checkable in AC 4's pin: the payload's
bytes contain `commands` but neither `slash_commands` nor `terminal_slash_commands`. Naming the
key `slash_commands` would adopt the names-only twin's spelling and shrink the pin to one word.

### What the doc comments must say

This is where ~two thirds of the production lines go. Target parity with `ModelListPayload` /
`ModelOption`, not more.

**Payload doc:**

- `ConversationID` is present and unfilled by this ticket — the producer supplies it at mapping
  time, the seam every v2 interactive payload uses. claude's own `session_id` is deliberately
  absent, for `BackgroundTaskStartedPayload`'s stated reason.
- `Commands` is in claude's own order. The key is always present and never null — see
  `MarshalJSON`.
- The count is workspace- and version-dependent (51 / 74 above); the payload is a **snapshot**
  of what this session in this working directory will accept, not a delta, and a client must not
  cache one list across working directories.
- Payload cost: 14,277 bytes for the capture's 51 entries against the 65519-byte v2
  application-envelope cap — comfortable but not free, and a cut has to be reportable rather
  than silent, which is what the two fields below are for.
- `DroppedCommands` — **use `docs/protocol-mobile.md` § `model_list`'s honest phrasing, not
  `ModelListPayload`'s Go doc.** How many entries the producer cut that this frame does not
  carry; 0 when nothing was dropped. **Nothing counts it yet**, and no entry cap is enforced
  yet — so do **not** write that `len(Commands) + DroppedCommands` is the menu's true size, and
  do not imply a cap exists. Declared anyway because a wire with nowhere to put a drop discards
  it silently and a permanent 0 reads as "nothing was dropped", which is a lie rather than a
  gap. #1719 owns making the decode record it; #1720 is where the field and a counter meet.
  (The package overview records that `ModelListPayload`'s Go doc says the opposite of the
  shipped protocol doc precisely because it was written the other way round. Do not inherit
  that.)
- The count reports at the payload and the text cut reports per entry, each where its dimension
  is decided — which is why this payload has no top-level `truncated_fields`, and why a
  name-only report would be wrong here: it loses **how many** were lost.
- **The complete-key-set statement (AC 1).** Name the capture file and the claude version:
  against `initialize_control_v2.1.239.json` (claude 2.1.239), `name`, `description`,
  `argumentHint` and `aliases` are the complete per-entry key set — 42 entries carry the first
  three, 9 carry all four, and there is no third key set. This shape adopts **all four**, so
  unlike `ModelOption` it drops nothing; a key a later claude adds therefore arrives as a
  documented gap against a stated measurement rather than as a silent drop.

**Entry doc:**

- All three strings cross because the type-ahead renders all three; at 51 entries the
  description is what makes the list usable rather than a wall of names.
- `ArgumentHint` is empty on 33 of 51 entries and the key is never omitted, so **empty is the
  ordinary case, not missing data**.
- `Name` is **not an identifier** — one name in the capture is `__remote-workflow`. No charset
  assumption belongs in this struct or in a client.
- `Aliases`: what makes the grey-out correct, with the `clear`→`reset` case named. The key is
  always present and never null — see `MarshalJSON`, whose reason is **not** the payload's.
- `Description` may contain newlines: `claude-api`'s does, and `0x0a` is the **only** sub-`0x20`
  byte anywhere across the 51 entries' four string fields — so newlines are the control
  characters on this path rather than one class among several, and a client rendering a
  single-line row must handle that specific case.
- `TruncatedFields` names **this row's** cut fields, null when nothing was cut, deliberately not
  normalised — see `MarshalJSON`. **Enumerate the vocabulary exhaustively and by wire name:**
  `"name"`, `"argument_hint"`, `"description"`, `"aliases"`. All four, matching exactly the
  fields the SECURITY paragraph credits. The sibling's doc enumerates 2 of the 3 it credits and
  is on record as a SHOULD FIX; #1720 reads this line to pick the names its producer emits, so
  an under-enumeration here under-covers a real field. Wire names, not Go field names — the
  precedent is `RateLimitedPayload`'s `TruncatedFields`, whose members are the JSON tags.
- **SECURITY paragraph** — carry `ModelOption`'s across with one strengthening: these strings
  are **workspace-authored**, not merely claude-authored. A command defined in a repository was
  written by whoever wrote that repository. They are safe to render as inert text and must never
  be fed to an HTML sink, an attribute, or a URL; the daemon bounds them but does not sanitize
  them — no control-character or terminal-escape stripping happens on this path — so the render
  boundary owing the sanitization is the **client's**.
- **Caps are the producer's.** This struct re-decides no maximum and declares no charset check:
  a second cap here would be a second place the limit is decided, and the two could disagree
  silently.
- **The report/control-input amendment.** The family's convention sentence — it is a REPORT,
  never a control input — needs the same amendment `ModelOption.Value` carries, for a different
  reason: a client is meant to send a **name back**, as the text of an ordinary message, since
  sending the slash command *is* the feature. Publishing a name does not make it trusted; it
  arrives inbound as ordinary message text on a path that does not treat it as a command
  vocabulary and does not consult this list. This frame declares no inbound verb (#1726's doc
  block has the reasoning) and grants nothing.

### The two marshallers

Both value receivers with the `type alias` indirection, for `ModelListPayload.MarshalJSON`'s
stated reasons. Signature + behaviour only — the bodies are three lines each and mirror the
siblings exactly:

- `func (p SlashCommandListPayload) MarshalJSON() ([]byte, error)` — normalises a nil `Commands`
  to `[]SlashCommand{}`, so the frame always serialises `"commands":[]` and never
  `"commands":null`. Reason: `BackgroundTaskRosterPayload`'s, unchanged — an empty list is a
  **positive statement** ("claude offered nothing"), `omitempty` is out because an absent key is
  not that statement, and `[]` spares a client decoding into a non-optional array type any
  branch. Asserted by `TestSlashCommandListPayload_NilCommandsNormalises`.
- `func (c SlashCommand) MarshalJSON() ([]byte, error)` — normalises a nil `Aliases` to
  `[]string{}` and **leaves `TruncatedFields` alone**. Asserted by
  `TestSlashCommand_NilSliceEncodings`.

**The entry's reason is a COLLAPSE, not the payload's positive statement, and the doc must say
so — it is measured here, not argued.** claude never sends `"aliases": []`: zero of 51 entries
carry an empty array, and 42 omit the key entirely. An entry with no aliases and an entry with
the key absent are the same statement, and a client must not branch on absent-vs-empty to match
an alias — so the wire states one position for both, and `[]` is the position that spares every
row an optional-array branch. This is exactly `EffortLevels`' COLLAPSE with the frequency
**inverted**: the majority case here, the single exception (Haiku) there. Whether the
daemon-internal value keeps the absent/empty distinction is **#1719's** call, as #1690 owns it
for the model list.

`TruncatedFields`' carve-out is `BackgroundTaskRosterPayload.MarshalJSON`'s, unchanged: nil and
`[]` say the identical thing there ("nothing was cut") and no consumer branches on the
difference.

The entry marshaller **cannot** fold into the payload's, and the doc says why in both places: a
payload marshaller normalising entries in place would reach through `p.Commands[i]` into the
caller's backing array, and it would not fire at all when a `SlashCommand` is marshalled alone.

## Concurrency model

No goroutines, no channels, no shutdown sequence — these are DTOs. The one concurrency-relevant
property is why both marshallers take **value** receivers and substitute onto their own copy:

- Assigning a fresh slice to the copy's own field is safe. Reaching **through** it into
  `p.Commands[i]` would mutate the caller's backing array — for a payload shared between the
  emitter goroutine and a per-connection fan-out that is a data race as well as a correctness
  bug.
- A pointer receiver would silently miss the value path that a round trip and a bridge both
  take.

Both properties are asserted, not merely documented — see the mutant matrix below.

## Error handling

No new failure modes and **no reject branches**. `json.Marshal` on the alias type is the only
error source in either method and its error propagates unwrapped, exactly as the three sibling
marshallers do. No sentinel errors, no validation, no bounds: every bound on this path is the
producer's (#1719/#1720), stated in the doc rather than re-decided here.

## Testing strategy

Three additions to `internal/protocol/interactive_test.go`. No fixtures — this slice ships none
(they are #1718's), and decoding `[]` always yields a non-nil slice, so **a nil list is
reachable only by constructing the value**. These are the only pins that can exist for AC 3.

**1. `TestSlashCommandListPayload_NilCommandsNormalises`** — mirrors
`TestModelListPayload_NilModelsNormalises`.

- Precondition: a payload built with only `ConversationID` set has nil `Commands`; `t.Fatalf`
  otherwise.
- Subtests `value` and `pointer` over the same payload: marshalled bytes must not contain
  `"commands":null` and must contain `"commands":[]`.
- After both: the caller's `Commands` is still nil (the marshaller did not write back).

**2. `TestSlashCommand_NilSliceEncodings`** — mirrors `TestModelOption_NilSliceEncodings`,
including its shared `assertEncodings` helper so the three assertions are stated once.

- Precondition: both `Aliases` and `TruncatedFields` nil.
- Assertions, applied in every subtest: no `"aliases":null`; `"aliases":[]` present;
  `"truncated_fields":null` present.
- Subtests `value` and `pointer` over a standalone entry.
- Subtest `nested in payload`: the same entry inside a `SlashCommandListPayload`, same three
  assertions — proving the entry marshaller fires *through* the payload's — plus the trailing
  `p.Commands[0].Aliases != nil` check, which is the **only** assertion that catches an
  in-place-normalising payload marshaller.
- After all subtests: the entry's own `Aliases` is still nil.

**3. Payload-bytes regression block appended to `TestSlashCommandListType_IsNotClaudesVocabulary`**
(AC 4). This extends the existing #1726 test rather than adding a new one, because that test's
own closing paragraph defers this half to #1727 by name, and because the naming half must not be
restated. Two obligations:

- **Replace that closing paragraph.** It currently reads "There is no payload-bytes half…this
  frame has no payload until #1727". Once this block lands the sentence is false, and a false
  doc comment is exactly the class no test covers. Replace it with what the block checks and why
  it is non-discriminating today.
- Marshal a fully-populated `SlashCommandListPayload` — one entry is enough: name `clear`,
  argument hint `""` (the ordinary case, 33/51), a short description, aliases
  `["reset", "new"]`, the row whose alias is the desktop's own Actions entry. Assert the bytes
  carry none of: **`argumentHint`**, **`slash_commands`**, **`terminal_slash_commands`**.

**The check list is derived, and two derivations must appear in the comment:**

- *Why so few per-entry spellings.* This shape adopts all four of claude's per-entry keys, so
  `name`, `description` and `aliases` are **red against the correct struct** and must not be
  checked. Only `argumentHint` differs (camelCase vs `argument_hint`), so it is the one
  per-entry spelling a check can name. **Do not copy the model-list pin's list** — it contains
  `description`, which this shape adopts; copying it verbatim ships a check that fails against
  the correct implementation.
- *Why `commands` is absent.* It is this payload's own wire key, so checking for it would be red
  against the correct shape — the same trap the sibling records for `models`.
  `terminal_slash_commands` is **subsumed** by `slash_commands` in the byte-containment
  direction (any bytes containing the longer contain the shorter), so `slash_commands` is the
  load-bearing check; the longer one is kept as the named statement of claude's fourth word,
  the same deliberate redundancy #1726 kept for its `initialize` equality check.
- There is **no deliberately-dropped-field half** here, unlike the sibling's: this shape drops
  nothing. Say that, so a reader does not look for the missing half.

**Mutant matrix — AC 3 names five, and each must have a sole owner.** Run these with
`go test -overlay=<abs-path json>` against a scratch copy rather than editing the worktree.

| Mutant | Turns red |
|---|---|
| `,omitempty` on `commands` | pin 1 (`"commands":[]` absent from the bytes) |
| `,omitempty` on `aliases` | pin 2 (`"aliases":[]` absent) |
| `,omitempty` on `truncated_fields` | pin 2 (`"truncated_fields":null` absent) |
| Delete either normaliser | pin 1 / pin 2 (`…:null` appears) |
| A third normaliser copied onto `TruncatedFields` | pin 2 (`"truncated_fields":[]`, stays-null assertion) |
| Pointer receiver on either marshaller | the `value` subtest of the matching pin |
| Payload marshaller normalising entries in place | pin 2's nested subtest **only** — the JSON is byte-identical, so the post-marshal backing-array assertion is the sole detector |

The last row is measured rather than assumed: code review on #1704 confirmed the in-place
mutant produces byte-identical output on the sibling, which is what makes that one assertion
load-bearing rather than decorative.

`make check` is the gate; this package is not behind a build tag and needs no `-run` filter.

## Size re-check (§ 4)

| Boundary | Limit | This spec |
|---|---|---|
| Production source files | ≤ 3 | **1** (`internal/protocol/interactive.go`) |
| Total written work | ≤ 400 | **~370** — see below |
| New exported types | ≤ 5 | **2** |
| Consumer call sites | ≤ 10 | **0** |
| Acceptance criteria | ≤ 5 | **4** |
| Reject branches | ≤ 10 | **0** |

The line estimate is anchored to the shape-matched analogue rather than to eye: `e527c4f`
(#1704) spent 194 insertions in `interactive.go` and 176 in `interactive_test.go` (re-derived
here with `git show --numstat`, both pure insertions — 370 total) on the same two types, the
same two marshallers and three tests. This slice's entry carries one field fewer, inherits no
equivalent of the `validModel` round-trip essay (~15 lines), and needs only the bytes half of
the third test (~30 lines with its comment, against ~65 for the whole sibling). Against that it
adds the complete-key-set statement, the aliases COLLAPSE measurement and the workspace-authored
strengthening. Net: ~195 production + ~145 test + ~30 spec-note lines ≈ 370, inside the boundary
but not by much — **the doc comments are the budget**, so match the sibling's density rather
than exceeding it.

## Open questions

1. **Byte-vs-rune bounding is #1719's, and it is real rather than theoretical.** 14 of the 51
   descriptions carry non-ASCII, so a bound applied to bytes can land mid-rune and emit invalid
   UTF-8 where a bound applied to runes cannot. This struct declares no bound and so does not
   decide it; it is written here only so the decode slice does not meet the question for the
   first time in review.
2. **The producer's entry cap is also a memory knob, and the arithmetic should be in front of
   #1720 before it picks one.** The v2 emitter in `cmd/pyry/interactive_turn_v2.go` appends every
   emitted payload's JSON to a per-conversation ring bounded by
   `eventring.MaxEventsPerConversation` (1024). At the capture's 14,277 bytes a single retained
   frame is ~14 KB, so a per-frame cap chosen only against the 65519-byte envelope is also
   choosing a per-conversation retention figure — and this frame is emitted per session and per
   working directory rather than once. Not this slice's decision; flagged so the producer's cap
   is chosen with both numbers in hand.
3. **What besides a cap may drop an entry.** `DroppedCommands` is documented here as "entries the
   producer cut that this frame does not carry" without naming a cause, deliberately: if #1720
   also drops an entry for a shape reason (an unusable name, a decode failure on one element),
   that count belongs in the same field and the doc must not have promised a cap-only meaning.
   #1720 settles the causes; #1718 documents them on the wire.

Also noted, not owned here: **two frames now publish a per-conversation menu from the same
`initialize` reply**, and a client author reading only one row must learn the other exists. That
cross-reference between § `slash_command_list` and § `model_list` is #1718's, per the ticket.

## Security review

**Verdict:** PASS

Walked adversarially against the checklist in `$AGENTS_REPO_PATH/architect/security-review.md`.
The subject is a DTO with no producer, so the productive question was not "what does this code
do to an attacker's input" but "what does this *declaration* let a downstream slice, or a client
author, get wrong". Findings:

- **[Trust boundaries] No MUST FIX — one explicit boundary, and this spec moves the trust label
  strictly downward.** All four strings crossed the subprocess boundary from claude's
  `control_response` and stay untrusted the whole way to the client; the type carries no
  validation and claims none. The strengthening is the finding worth stating: the sibling's
  SECURITY paragraph credits *claude-authored* text, but a command defined in a repository was
  authored by **whoever wrote that repository** — a lower-trust origin than claude, since a
  hostile repo can plant a description that a careless client renders. The spec requires that
  sentence verbatim in the entry doc, names the client as the boundary owing sanitization, and
  requires the report/control-input amendment because a client is meant to send a `name` back as
  ordinary message text. Nothing downstream may read a published name as pre-authorised: this
  frame declares no inbound verb (#1726's block) and grants nothing.
- **[Network & I/O] SHOULD FIX, routed to #1720, not fixable here.** The declaration is
  unbounded by design — `Commands` has no length cap and the four strings no size cap — so an
  unbounded producer could build a frame exceeding the 65519-byte v2 application envelope from a
  workspace with enough commands (the measured 14,277 bytes at 51 entries is 22% of the cap; the
  74-entry count observed on 2.1.220 scales toward a third). Declaring a second cap here is
  explicitly the wrong fix: two places deciding the limit can disagree silently. What this spec
  does instead is make the *report* of a cut mandatory and countable (`DroppedCommands` plus
  per-entry `TruncatedFields`), so a bounded producer cannot present a cut list as complete —
  which is the property a client's correctness depends on. Open question 2 hands #1720 the
  retention arithmetic (`eventring.MaxEventsPerConversation` = 1024 × ~14 KB) so its cap is not
  chosen against the envelope alone.
- **[Error messages, logs, telemetry] No findings, with one thing deliberately not done.** These
  types are constructed by nothing today, so nothing logs them. Neither marshaller adds context
  to its error, and that is correct rather than an omission: wrapping would put entry text into
  an error string, and these strings are workspace-authored — the sibling marshallers behave
  identically.
- **[Concurrency] No MUST FIX — the race is designed out and asserted.** The realistic hazard is
  a marshaller mutating a shared payload's backing array through `p.Commands[i]`, which would be
  a data race under the emitter's fan-out. Value receivers plus the entry-level marshaller
  prevent it; the post-marshal backing-array assertion in pin 2's nested subtest is the sole
  detector for the in-place mutant (byte-identical JSON otherwise), and it is required, not
  optional. `make check` runs this package under `-race`.
- **[Subprocess execution] No findings — nothing here reaches `exec.Command`.** Worth stating
  because the sibling's `Value` field *is* on an argv path (`internal/relay`'s `validModel`, the
  #845 argv-injection defense). No field of `SlashCommand` is passed to a child as an argument:
  a client sends a name back as the text of an ordinary message, not as a CLI argument. If a
  later slice ever routes a published name to argv, that slice — not this one — owes the
  validator, and it must not reason "the daemon published it, so it is safe".
- **[Tokens/credentials, File operations, Cryptographic primitives] Not applicable, by
  construction rather than by omission.** The slice adds two structs and two `json.Marshal`
  calls: no randomness, no key material, no filesystem path, no comparison against a secret. The
  only file this ticket reads is the committed capture, read by a human writing a doc comment,
  not by shipped code.
- **[Threat model alignment] Aligned with `docs/protocol-mobile.md` § Security model on the one
  clause that applies.** This is an outbound binary → phone report inside the existing Noise-IK
  session; it opens no new channel, adds no inbound surface, and is excluded from
  `inboundAppTypeSet` by #1726's already-shipped guard classification (`compat_test.go`'s
  partition and `cmd/pyry/relay_guard_test.go`'s `excludedTypes`), so a phone cannot send one
  into `dispatch.Route`. This slice touches neither guard, and must not.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-24
