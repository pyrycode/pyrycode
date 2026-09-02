# Producing `turnevent.SlashCommandList` (#1877)
**Producing `turnevent.SlashCommandList` (#1877).** `emitModelList`'s rung 4 gained a second,
independent gate on `commands`, below the existing `ModelList` emit: non-empty → each entry's `Name`
is cut at a new `maxSlashCommandName` (256 bytes, the family's usual multiple over the measured
capture) at construction and the entries emit as one `turnevent.SlashCommandList` beside the
`ModelList`; absent, null or empty emits nothing. No entry-count cap and no `DroppedCommands` —
that's #1826's, one array over the #1811→#1812 precedent — so `logControlResponse` gains no seventh
attribute: with no count cap the decoded count the sixth attribute already reports still equals the
emitted count. Producing is not publishing: `turnbridge.MapEvent` gains no arm and
`interactiveTurnEmitterV2.Handle` gains no case, so the value is logged by kind and dropped — that's
still #1720's.

**The construction moved into its own emitter (#1886).** The cap, the `turnevent.SlashCommand`
construction and the single emit no longer sit inline at `emitModelList`'s tail — they're
`emitSlashCommandList`, a method on `*Parser` taking the already-decoded `[]commandEntryLine`, so a
second classification rung — split out by #1890, below — can reach the same construction instead of
copying it. The
`len == 0` gate moved in as the new emitter's precondition rather than staying a guard at the call
site, making suppression a property of the callee; `emitModelList`'s tail call is unconditional. The
record stays `logControlResponse`'s, written by `emitModelList` before the call — the new emitter logs
nothing on any path. Behaviour is unchanged; only where the construction lives moved.

*Lesson: a doc comment moved with its code has to split by each sentence's SUBJECT, and `make check`
cannot see a wrong split.* Five sentences inside the moved block were deictic — "the models loop
**above**", "declares its own `cut` **here**", "the precedent is in **this same function**" — pointing
at surroundings rather than naming a symbol. None of the five named the function being moved out of,
so a symbol-level cite sweep missed all of them, and a green `make check` (behaviour-identical by
construction, no assertion touched) proved nothing about whether they still made sense in the new
home. The falsified set for a move is "every sentence pointing at its surroundings," not "every
sentence naming the moved symbol" — the second set is what tooling can check and the first is bigger.

*Lesson: a cite that survives a move can sit immediately next to one that doesn't.* `commandEntryLine`'s
doc has "`maxSlashCommandName` bounds Name at CONSTRUCTION in `emitSlashCommandList`" (repointed by
the move) directly followed by "this slice still lives from `json.Unmarshal` until `emitModelList`
returns" (unchanged — the decode and the frame holding the slice both stay in `emitModelList`, and the
new emitter returns before its caller does). The unit a relocation sweep has to work at is the
sentence, not the symbol or the paragraph; two adjacent sentences naming the same neighbouring symbol
can disagree on whether the move falsifies them.

*Lesson: a neighbouring rung's suppression argument can rest on a precondition this rung doesn't
have.* Rung 3's false-negative asymmetry (why an empty `models` array suppresses rather than emits an
empty `ModelList`) is justified by both outcomes — empty and missing — being observable to a client.
Nothing publishes `SlashCommandList` yet, so that footing doesn't transfer, and taking the argument
anyway would have left the empty-`commands` suppression resting on a premise that's false today. What
actually decided it: `controlResponseLine.Commands` collapses absent, null and a published `[]` onto
one nil slice before the gate ever runs, so the producer can't make the positive statement ("claude
offered nothing") an empty emit would assert. Before reusing a sibling rung's reasoning for a new
gate, check whether its premise still holds at the new call site — the shape of the argument
transferring is not the same as the argument being true.

*Lesson: a falsified `//` claim phrased about a category is the one grep won't find.* `eventKind`'s
`ModelAnnounced` arm carried a clause reading "nor for anything else the production producer emits" —
worded about *any* future producer rather than about `SlashCommandList` by name, sitting in the arm
for a different variant entirely, one nothing about slash commands would lead a reader to open.
Grepping the tree for `SlashCommandList` finds neither that clause nor the sentence justifying it.
When a change makes something true tree-wide (here: "streamsup now produces a second event kind"),
read each named doc block whole rather than grepping for the new type's name.

*Lesson: a sink-reachability enumeration carried over from the spec still needs checking against the
call graph, not just against the spec text.* The architect's own security review named the exact
paragraph to verify, and the developer's version still credited `turnbridge.MapEvent`'s `default` arm
with reach it doesn't have. Code review found `MapEvent` has exactly two production call sites,
`emitMapped` (reached only from `Handle`'s typed arms, none of which is `SlashCommandList`) and
`resolveBoundModelList` (which hands it a `ModelList` explicitly) — so `SlashCommandList` never
reaches `MapEvent` at all; it stops at `Handle`'s own default. The same tree already said as much
three files over, in a do-not-correct sentence this ticket preserved verbatim (`eventKind`'s
`SlashCommandList` arm: "`emitMapped`'s unmapped drop, which nothing routes it to"). The
spec-inherited enumeration and the surviving sentence disagreed, and only reading both caught it — an
enumeration transcribed from a spec is not verified by transcription.

**Proving the bound with a matrix (#1878).** #1877's own liveness proof — one over-cap row, one that
fits — became a full proof matrix, test-only, in the same file: the cap boundary at and either side of
`maxSlashCommandName`, the committed capture's fifty-one names verbatim, an exhaustive suppression
table, and the content-free log sweep extended to every rung. No production file changed.

*Lesson: a fixture-built row's expected OUTPUT, not only its input, decides which mutants it separates.*
The spec credited the exactly-at-the-cap row with sole redness against a halved `maxSlashCommandName`,
reasoning that the over-cap row's *input* is built from the same cap fixture and so follows the constant
down. Measured by mutation, the over-cap row and both mid-rune rows redden too, because their *expected
lengths* are written against that same fixture. What the exactly-at-the-cap row alone catches is
`truncateField`'s `<=` boundary itself — no input above or well below the cap can observe that operator
flipping to `<`. A row's claimed exclusivity needs checking against its assertions, not just its inputs.

*Lesson: a leak sentinel on a rung that decodes nothing is vacuous until paired with a line that actually
carries the swept data class.* The pre-existing undecodable-rung line carries no `commands` array, so
under a mutant that logs the decoded command name there, its `name` attribute comes back empty and only
the exact-attrs `reflect.DeepEqual` catches it — the content sweep never fires. A second line on the same
rung, carrying a real sentinel name, is what makes the sweep's own failure message name the leak. The same
code review pass is what surfaced the `encoding/json`-quoting correction above.

**The commands-only success gets its own keyword (#1890).** Rung 3's `ack` used to answer two
different payloads — a success carrying neither array, and one carrying a non-empty `commands` and
no `models` — with only the decoded count telling them apart on the record. Rung 3 now splits on
that count: `ack` narrows to "neither array" and reports an all-zero record by definition, and the
new `commands_only` keyword takes the payload the split carves out. `ack` deliberately stays rung 3
(not the new keyword) so `emitSlashCommandList`'s existing ordinal cite into the enumeration keeps
resolving — renumbering the other way would have rotted that cite silently, with no test and no gate
to catch it. This slice adds no call to `emitSlashCommandList` and emits nothing new; it only decides
which keyword the sibling that makes this rung emit will inherit, so that sibling moves no keyword
and adds none.

**The keyword had to name the payload's shape, not the daemon's reaction to it.** A name for the
emit (`command_list`, parallel to `model_list`) would have been false in this slice, where the rung
emits nothing; a name spelling "ack" would go false the moment the sibling lands. `commands_only`
survives both states because it describes what the payload IS rather than what happened to it — the
general form for any keyword decided ahead of the behavior it will eventually describe.

*Lesson: a closed-set arity claim rots by COUNT WORD, not by rung name, and a name-only sweep misses
it.* The ticket's own `//`-claim sweep was built by grepping for mentions of "the ack rung," which
found every claim naming that rung by name but missed five more that described the classification's
*cardinality* instead — "four rungs," "the three non-emitting rungs," "on any of the four rungs."
Those went stale from the split alone, independent of whether they mentioned `ack`. A rung-split
ticket needs two sweeps: one for the rung's name, one for how many rungs the prose says there are.

*Lesson: a test-local sentinel constant is itself a `//` claim.* `ackCommandSentinel` (the
leak-detection literal on the commands-only fixture) named the pre-split rung in both its identifier
and its string value. Every assertion using it stayed green through the split — nothing forced a
second look — because the constant's role in the test doesn't depend on what its name says. The only
reader it exists for is someone chasing a leak-detection failure message, and a stale name would have
misdirected exactly that reader. A sentinel constant's name is documentation with the same rot
exposure as a comment, just invisible to a `//`-claim grep.

*Lesson from code review: build the mutant before writing the counterfactual, because "would go
green" is often false when several tests cover the same outcome from different angles.* The spec (and
the PR body inheriting its wording) claimed that deleting one specific test row would let a
"`commands_only` on every success" mutant pass unnoticed. Code review built that exact mutant with
that exact row deleted and found three *other* test functions still caught it — the row's real value
(the paired negative that proves the split, and the reason `wantReason` is a row field rather than a
hardcoded literal) was true and unaffected, but the "goes green" clause overstated the row's
uniqueness. A claim about what a mutant would do is an empirical claim; write it after running the
mutant, not instead of running it.

**The entry count is bounded and the drop is counted — `maxSlashCommandListEntries` (128), #1826.**
`maxTaskRosterEntries`' doctrine's third application after `maxModelListEntries` (#1812): tail
truncation at construction, reported on `turnevent.SlashCommandList.DroppedCommands`, cut once in
`emitModelList` above both rungs that read the array so `logControlResponse` can report it and neither
rung needs its own cap. `commands` becomes the *emitted* count (mirroring `models`); `commands_dropped`
is the record's seventh, daemon-computed attribute.

*Lesson: a candidate set a sibling doc hands forward is a candidate set, not an answer — check it
against current observations before taking it.* `maxSlashCommandDescription`'s fraction table offered
6 or 8 entries, computed against the 1280-byte worst case. Both cut the committed capture's 51-entry
menu by over 40 entries — a cap firing on claude's ordinary output, which this family's own
`maxModelListEntries` doc rules out by name. Re-basing the fraction at the *retained* size (131.59
B/entry, not the worst case) still wasn't enough: a second, larger observation (74 entries, a different
claude version and working directory) already sits at 59% of the whole-line ceiling before any cap
fires, so no count clearing it keeps the worst-case product under the v2 envelope. The derivation that
resolves this has to say which constraint gives — here, the worst-case-product convention, kept instead
being the rule that a cap must not fire on ordinary output — and name it as spent rather than silently
picking whichever candidate is closest to hand. The next slice to read this constant's doc (#1720, the
wire producer) is the reason the "does not buy" half matters as much as the number: 128 entries is
headroom over both observations, but its worst case is still 2.5x the 65,519-byte application-envelope
cap, and nothing about the count being bounded means a frame built from it fits.

*Lesson: an inherited amplification claim needs recomputing per struct, not citing.* This family's
per-entry caps cross-reference `maxTaskRosterEntries`' "linear and near 1" transient-amplification
figure. `commandEntryLine`'s densest *legal* entry is `{},` — three bytes, because every key is
optional — for a 72-byte decoded struct: ~24x, not near 1. The inherited citation would have understated
a 4 MiB input line's transient allocation by two orders of magnitude (~100 MB, not ~4 MiB) at the exact
struct this ticket was bounding. Worth checking before reusing any "amplification is near 1" claim
against a struct whose fields are optional in a way the cited struct's aren't.

*Lesson: a byte-accurate re-derivation of a doc-stated measurement can disagree with a naive one, and
the disagreement is the tell for which is wrong.* Re-deriving `maxSlashCommandDescription`'s cited
6,711-byte retained figure through Go's own `truncateField` reproduced it exactly; a first pass using
`jq`'s `.[0:256]`, which slices by *codepoint*, returned 6,735 — 0.4% off, from cutting some multi-byte
runes short of where a byte-accurate cut would. Separately, the protocol package's 14,277-byte figure
for the same array turned out to be the *raw claude* bytes, not the capped daemon-side ones (11,403
after truncation, with Go's HTML-escaping). Two measurements that look like they're of the same thing
can be of different things; re-derive rather than transcribe, and check which side of a cap a cited
figure was taken from.

*Lesson: an equality against a fixture's own length is not a headroom proof.* `len(list.Commands) ==
len(want)` on the committed 51-entry capture holds against a cap of 1 exactly as well as against 128 —
it proves the cap didn't fire on *this* fixture, never that the cap has room above it. Proving headroom
(AC 4 here) needs a guard that fails loudly should the fixture ever grow to meet the cap —
`len(want) < cap`, asserted with `t.Fatalf` before the equality — so a future re-capture that
approaches the cap reddens the guard and names itself as the cause, rather than reading as a producer
regression.
