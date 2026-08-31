# Two further lessons, from tightening that same table's assertions to pin the #1828 collapse
**Two further lessons, from tightening that same table's assertions to pin the #1828 collapse.**
First, a spec's mutation predictions are written against the assertion as it stands *today*, and a
ticket that both tightens an assertion and predicts mutants against the tightened version has to
re-run the prediction after the tightening, not before: #1828's spec predicted that reverting the
collapse the *other* way (`return []string{}}`) would leave the `published empty` row green, true
against the old `len(got) != 0` check but false the moment the row's assertion became `got == nil` —
both `[]string{}` values are non-nil, so that mutant now reddens all three zero-length rows together,
not the two the row names suggested. Second, once a collapse makes several rows decode to the
identical value, no mutant of the *production* code can prove they're three genuinely distinct wire
shapes rather than one shape asserted three times — only a mutant on the *fixture* (dropping the key
from one row's builder) can, because it reddens the `wantWire` guard while the decoded-value assertion
stays green. A collapse design's "are these really separate rows" question has to be answered by
mutating the fixture, not the code under test.

**The entry count is capped too (#1812) — `maxModelListEntries` (10), the family's second cardinality
bound after `maxTaskRosterEntries`.** A per-entry text cap alone leaves the list's total size a
function of a number claude chooses; the count bound supplies the missing factor, applied after rung 3
and before the entry loop, truncating **from the tail** so claude's order is preserved (the same
ordering rule `Tasks`/`Models` both state). 10 is derived, not chosen, and the observed six-entry
capture sets the floor with four slots of headroom — not the roster's 8, which would leave only two.

The per-entry multiplicand is **1024 bytes since #1821** — `maxModelResolved + maxModelValue +
maxModelDisplayName + (maxModelEffortLevelCount × maxModelEffortLevel) = 256×3 + 8×32` — the family's
one-kibibyte entry unit, shared byte-for-byte with `maxTaskRosterEntries`' own per-entry figure despite
the two entries having different field sets (256+256+512 vs. 4×256): a coincidence of arithmetic, not a
rule the *next* aggregate variant is bound by (see [ADR 036](../decisions/036-aggregate-cap-product-is-not-a-ceiling.md)).
`maxModelListEntries` itself did **not** move: `10 × 1024 = 10240` bytes (15.6% of the v2
application-envelope cap, `docs/protocol-mobile.md` § Application-envelope size cap). The doc no longer
measures that product against a fixed 8192 — 8192 was always `maxTaskRosterEntries`' own product,
noticed to land on half of `maxUnrecognizedRaw`'s 16 KiB, and #1821 demoted it from an inherited
ceiling back to that landmark. What actually bounds the product is `maxUnrecognizedRaw` itself, with
the fraction stated per shape rather than a shared constant: the roster is 1/2, the model list is 5/8
(10240/16384) — each aggregate variant re-derives its own fraction rather than inheriting the other's.

`turnevent.ModelList.DroppedModels` carries what was cut (`0` when nothing was), so
`len(Models) + DroppedModels` is the list's true size and a client can render "6 of 40" rather than
presenting a short menu as complete. `logControlResponse`'s record grew a fourth attribute, `dropped`,
for the reason the wire field's own doc argues about a permanent zero, one layer down: before #1848/#1849
published the event to a client, that Debug record was the only observable the cap had, and `models=N`
with no drop count would have read as "claude offers N models" even when it offered more. A fifth
attribute, `levels_dropped`, was added the same way for the level-count cap (#1821) — see above.

`ModelList` is a `turnevent.Event` (not parser-held session state) precisely because
`protocol.ModelListPayload`'s own doc names mapping time — `turnbridge.MapEvent` — as the seam every
v2 interactive payload supplies its `ConversationID` through; session state would have obliged a
second parser→relay path beside the one every other interactive payload already uses. `MapEvent` gained
a `turnevent.ModelList` arm in #1848 (previously falling to `default`, which dropped the event) and
`cmd/pyry`'s `Handle` has carried the emitting case since #1849, so a client does read a real count
today — best-effort on the live interactive turn lane; a client that missed it now also gets a
connect-time snapshot: #1863 shipped the relay-side seam nil in production, and #1867 wired the
daemon-side producer that fills it. See [protocol-package.md](protocol-package.md)'s Model-list payload
section.

*Test-writing lesson for the next per-entry accumulator built on `emitBackgroundTaskRoster`'s idiom
(the `cut` closure declared inside the per-entry loop).* An isolation row asserting "a cut on one
entry does not appear on a later entry" only reddens under a shared-accumulator mutant (`cut` hoisted
out of the loop) if the **clean** entry is placed *after* the cut one in the fixture: the cut entry's
report is appended to the shared slice before the next entry starts accumulating, so a clean-then-cut
ordering proves nothing about cross-entry leakage — only cut-then-clean does.

*Sharpened by #1821: a hoisted arithmetic accumulator needs a stronger fixture than a hoisted slice.*
The level-count cap's per-entry drop total (`droppedLevels`, summed into `levelsDropped`) is a running
`int`, not an appended slice, so the two-entry cut-then-clean fixture above does not catch it hoisted
out of the loop: with two over-cap entries the hoisted and correct totals can coincide by construction
(1+3 = 4 either way). It takes three entries with a **clean one in the middle** — over-by-one, clean,
over-by-three — before a hoisted counter's running total (5) diverges from the correct per-entry-reset
one (4). The same three-entry arrangement separately kills "report only the last entry's drop", "report
the largest single drop", and "report how many entries dropped anything" — three more wrong shapes an
accumulator can take that all read 3 against the same fixture. An accumulator's isolation fixture needs
checking against more shapes than the slice-append lesson above calls for; a slice and a running total
fail at different fixture sizes.

*Test-writing lesson for `maxModelListEntries`' boundary row (#1812), and for any future cardinality
cap shaped `if len(entries) > cap`.* At `len(entries) == cap` the block computes `dropped = 0` and
slices to identity either way, so `>` vs `>=` is an **equivalent mutant** there — no fixture can tell
them apart, and a boundary-row comment claiming otherwise (the phrasing `emitBackgroundTaskRoster`'s
own equivalent row inherited) overstates what the row proves. What the "exactly at the cap" row
actually pins is a cap that fires **one entry early** (`cap - 1`), verified live by overlaying
`maxModelListEntries = 9`, which reddens that row while `>` → `>=` does not. Also: a shared record's
attribute-set change (here, `logControlResponse` gaining `dropped`) can reach a `wantAttrs`-comparison
test the spec's own amendment list didn't enumerate — `TestParser_ControlResponseAckIsConsumedSilently`
compares with `reflect.DeepEqual` too and lives ~200 lines from the model-list block, so it only
surfaced at the first green run. Any future attribute on that record needs every `wantAttrs` map in the
file, not just the ones naming the feature that grew it.

*The equivalence above does not generalize, and #1821's level-count cap is the counterexample.* "`>`
vs `>=` is equivalent at `len == cap`" holds only while the guarded block computes *nothing but* a
count and a slice. `boundEach`'s count bound sits beside a report flag (`cutAny`) in the same block, so
at `len == cap` an `>=` mutant still computes a zero drop and an identity slice **but also fires the
report** — `"effort_levels"` gets named on a list nothing happened to. Measured sole-red on the
"exactly at the cap" row. Before inheriting a prior cap's equivalent-mutant note for a new one, check
whether the new guarded block reports anything the prior one didn't; folding a report into the same
block turns a boundary that used to prove nothing into one that does.

**Declaring `commands` alongside `models` (#1853).** `controlResponseLine`'s decode target grew a
second array, `commandEntryLine{ Name string }`, for claude's slash-command inventory — one field,
same reasoning as `modelOptionLine` and `systemInitLine`: absence from the decode target is a
stronger guarantee than a test sweep, so `argumentHint`/`description`/`aliases` stay undeclared
(as of #1853 — `description` joined in #1904, below; `argumentHint`/`aliases` remain undeclared, #1830 and #1825).
Declaring the array turns a `commands` that arrives as a number, a string or an object from
*silently ignored* into a whole-line decode failure on the undecodable rung — the same shape
guarantee `models` already had, extended to a second field. `logControlResponse` grew a sixth
attribute, `commands`, the **decoded** count (not the emitted count `models` reports): taken below
the success gate and above the empty-models block's return(s), so undecodable and nak report 0 —
originally making a payload carrying `commands` and no `models` distinguishable from one carrying
neither by count alone, without touching the classification itself. **#1890 changed that**: `ack`
now narrows to "neither array" and reports 0, and `commands_only` — a fifth rung, split out of what
used to be the whole of rung 3 — reports the count instead. The distinction moved from the count to
the keyword; see below.

*Testing lesson: a gate-placement claim needs a fixture where the two placements disagree.* The row
pinning "count taken below the subtype check, not above" only works because it pairs a
`subtype:"error"` NAK with a **non-empty** `commands` array — every other nak-rung row carries an
absent or empty array and reads 0 under either placement, so a mutant moving the count above the
gate survives all of them silently. The general form: when a design decision is "compute X after
check Y, not before," the pinning fixture must make X differ depending on which side of Y it's
computed on — an X that reads the same either way (because the fixture is empty/absent on the
inputs the reordering would affect) proves nothing about the ordering.

*Known future collision, partially resolved by #1904, below.* `commandEntryLine`'s comment used to
read "a later reader must not 'complete' this struct" — but `protocol.SlashCommand` already declares
all four keys today, and `protocol.SlashCommandListPayload`'s doc states outright that shape "adopts
all four... unlike ModelOption it drops nothing," with **#1720** open to publish it. Code review
flagged this as a SHOULD FIX (the comment borrowed `systemInitLine`'s register — keys the daemon must
*never* hold — for three keys the wire type was already committed to carrying) and it was left
unfixed at the time, deliberately non-blocking. #1904 declared the first of those three
(`description`) and rewrote the paragraph to name which omissions survive rather than forbid
completion outright: `argumentHint` is #1830's, `aliases` is #1825's. Whoever picks up the remaining
two now reads an explicit assignment instead of an absolute ban; #1720 still owns *publishing* the
shape once all four are decoded.
