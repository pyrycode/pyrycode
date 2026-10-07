package streamsup

import (
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// maxSlashCommandName caps turnevent.SlashCommand.Name — claude's name for one entry
// of the initialize reply's commands array, the workspace's slash-command inventory.
// Applied at CONSTRUCTION, exactly as the model caps above are, so an oversized value
// never enters the event stream, the push queue, or any log.
//
// MEASURED against the committed capture (claude 2.1.239, byte-identical across all
// three responding arms): fifty-one entries, `name` present and non-empty on every
// one, longest 24 bytes (fewer-permission-prompts), mean 9.69, median 8, 494 bytes in
// total. So 256 is roughly 10.7x the observation — maxModelField's and
// maxModelResolved's multiple over the same identifier shape, and for their reason
// verbatim: room for a naming scheme claude has not shipped yet, and still a hard cut
// on anything that has stopped being a name.
//
// A separate constant even though it currently equals maxModelResolved, maxModelField
// and maxModelValue: maxRateLimitField's paragraph applies verbatim — they bound
// different fields for different reasons, and folding them into one would make a
// future change to one budget silently move this one.
//
// A BYTE CUT AND NOTHING ELSE. One captured name is outside [a-z0-9-]
// (__remote-workflow), the committed proof that no charset may be assumed, so #1600's
// verbatim rule governs here as it does for the model strings: no lowercasing, no
// trimming, no charset filtering, and no leading "/" added or removed. See
// turnevent.SlashCommand.Name, which states the same rule for the consumer.
//
// THE PER-ENTRY TERM, stated so the field slices add to ONE derived figure rather than
// each re-deriving it, and COMPLETE since #1825: one entry costs at most
// maxSlashCommandName + maxSlashCommandArgumentHint + maxSlashCommandDescription +
// maxSlashCommandAliasCount * maxSlashCommandAlias = 256 + 256 + 256 + 8 * 64 = 1280
// bytes of workspace-derived text. IT IS A SUM PLUS A PRODUCT, which is what the fourth
// field changed about the SHAPE of this term and not only its value: aliases are a
// nested array, so they add a third size dimension — entries x aliases x alias length —
// that a per-field byte cap alone does not cover, and maxModelResolved's own term has
// carried the same shape since #1821. The field this paragraph named as the one that
// would dominate the budget was the description, and it does — it arrived in #1904 with
// a cap of its own, whose doc carries that cap's derivation and the arithmetic this
// term hands forward. The argument hint joined in #1957 and costs the same 256 while
// contributing 530 bytes across the whole capture against the descriptions' 10,580,
// which is why the term grew by half again while the observation barely moved; aliases
// joined in #1825 and cost 512 while contributing 64, the same disproportion one step
// further. THE AGGREGATE IS SETTLED SINCE #1826 and no factor is outstanding. The one
// that was missing here was the entry COUNT alone, and maxSlashCommandListEntries
// supplied it exactly as maxModelListEntries supplied maxModelResolved's: 128 entries
// against this 1280-byte term. What that constant does NOT do, and what makes this
// aggregate unlike maxModelResolved's, is land under any ceiling the package cites —
// its own doc records which convention it spent to keep the cap off claude's ordinary
// output, and that arithmetic is cross-referenced rather than restated here. No field
// slice remains outstanding either.
//
// TRANSIENT AND RETAINED ARE TWO DIFFERENT FIGURES HERE TOO, which is what makes this
// bound the whole story rather than half of it. What is TRANSIENT is the unbounded
// decoded array, bounded one level up by defaultMaxParseBuf's 4 MiB whole-line cap
// before the decoder ever sees it — commandEntryLine's own paragraph. What is
// RETAINED is only this capped copy inside the emitted turnevent.SlashCommandList,
// and cmd/pyry's sessionSlashCommandHold now keeps THAT for the session's life (#2004),
// replacing it on each report rather than accumulating. So this cap no longer differs
// from maxModelResolved on the point it used to: what one child's initialize reply can
// hold onto is a capped list either way, and the retained figure here is bounded by
// this term times maxSlashCommandListEntries rather than by an event's lifetime.
const maxSlashCommandName = 256

// maxSlashCommandDescription caps turnevent.SlashCommand.Description — claude's own
// description of one entry of the initialize reply's commands array (#1904). Applied
// at CONSTRUCTION in emitSlashCommandList, exactly as every cap above is, so an
// oversized value never enters the event stream, the push queue, or any log.
//
// THE DOC-SHAPE PRECEDENT IS maxTaskRosterDescription, not the identifier-cap family
// this constant's neighbour belongs to. That constant bounds the same SHAPE under the
// same pressure — a prose description multiplied by a count claude chooses — and its
// two arguments are the two this derivation weighs, one pushing down and one up.
//
// MEASURED against the committed capture (claude 2.1.239, byte-identical across all
// three responding arms): fifty-one entries, `description` present and non-empty on
// every one, longest 1145 bytes (dataviz), then 1078, 1075, 1023 and 797, mean 207.5,
// median 69, 10,580 bytes in total. 10 are over 256 bytes, 16 over 128, 28 over 64.
// The names, for contrast, total 494 with a 24-byte longest. So 256 is ~1.2x the mean
// and ~3.7x the median — a far thinner multiple than maxSlashCommandName's 10.7x over
// the same capture, and the thinness is the decision rather than an accident of it.
//
// WHY THINNER, and it is maxTaskRosterDescription's MULTIPLICATION argument with the
// multiplier worse: a multiplied field earns a smaller unit budget than the same field
// carried once. The roster multiplies its description by 8; this list multiplies by
// claude's observed 51. That pushes BELOW the roster's 512, and 256 is where it lands.
//
// WHY NOT LOWER, and here maxTaskRosterDescription's ROLE argument INVERTS rather than
// carrying: a roster description's authoritative full-length copy already crossed the
// wire on the BackgroundTaskStarted its task_id joins back to, so a cut there loses
// nothing a consumer holding that event cannot recover. A CUT SLASH-COMMAND
// DESCRIPTION IS RECOVERABLE FROM NOTHING — no second copy of it exists on any lane.
// The bound on how far that pushes is the named consumer, pyrycode-desktop#694's
// type-ahead, which renders one row per command as a name, an argument hint and a
// description: 128 would cut 16 of the capture's 51 and 64 would cut 28, so over half
// the menu would arrive truncated for the one consumer the field exists for. 256 cuts
// 10.
//
// THE MULTIPLICAND IS THE SUM OF CAPS PLUS ONE PRODUCT, which is how maxModelListEntries
// derives its own and what makes the figure a worst case rather than a description of
// one capture: maxSlashCommandName + maxSlashCommandArgumentHint +
// maxSlashCommandDescription + maxSlashCommandAliasCount * maxSlashCommandAlias =
// 256 + 256 + 256 + 8 * 64 = 1280 bytes since #1825 added the fourth term. It became a
// sum plus a product rather than a longer sum because the fourth field is a NESTED
// ARRAY — see maxSlashCommandName's own statement of the term, which owns the shape.
//
// ROUNDNESS WAS A TWO-TERM TIEBREAKER AND THE LATER TERMS DISSOLVE IT, which is
// recorded rather than repaired. This paragraph used to read the sum's 512 as a unit a
// reader can hold — the virtue maxTaskRosterEntries' arithmetic paragraph claims by
// name for its own 1024 — and to note that no other candidate weighed here landed on
// one. Re-derived at three terms the candidates were 64 -> 576, 128 -> 640, 256 -> 768
// and 512 -> 1024, so the REJECTED 512 candidate became the round one and the
// tiebreaker inverted. At four terms it is dissolved outright: the sum is 1280 and the
// candidates around it land nowhere in particular. It does NOT re-open this constant's
// value: 256 was decided on the multiplication and role arguments below, which neither
// later term touches, with roundness standing beside them rather than under them.
// #1825's fourth term was NOT chosen to make the sum come out even — its own two
// constants record that the round candidate (a count cap of 4, a term of 256, a sum of
// 1024) was available and declined on an argument about what a cut alias costs.
//
// THE CEILING IS maxUnrecognizedRaw's whole-line 16 KiB, and 8192 is deliberately NOT
// cited as one. maxModelListEntries' doc records why: 8192 is maxTaskRosterEntries'
// PRODUCT, whose doc merely NOTICED that it lands on half of maxUnrecognizedRaw, and
// inheriting a noticed landmark as a constraint is the mistake that paragraph undoes.
// What survives is the RULE — a whole KNOWN event must not approach the cap on an
// entire UNKNOWN line, measured retained-against-retained — with the fraction stated
// PER SHAPE: the roster reads 1/2 and the model list 5/8.
//
// THIS SHAPE HAS NO FRACTION, and that is #1826's answer rather than a gap this doc
// left open. What this paragraph used to owe was arithmetic handed forward: at the
// 1280-byte per-entry term, 16384 * 1/2 = 8192 leaves 6 entries and 16384 * 5/8 = 10240
// leaves 8, and #1826 was to pick the fraction and the count from those two. IT PICKED
// NEITHER, and the candidate set is kept here rather than deleted because a reader
// re-deriving it will reach those same two numbers and needs to be told they were
// weighed and declined. Both CUT claude's ordinary output — a cap of 6 or 8 shortens
// the capture's fifty-one-entry menu by 43 or 45 — which maxModelListEntries' NOT 8
// paragraph rejects by name. maxSlashCommandListEntries took the OBSERVATION as its
// base instead of this worst-case term, landed on 128, and records there which
// convention it had to spend to do it; no fraction of maxUnrecognizedRaw survives the
// move, so none is stated for this shape. The term above is still the one derived
// figure a later slice multiplies, and it is why the term is stated here at all.
//
// WHAT THE OBSERVED CAPTURE COSTS AT THIS CAP is the sharpest figure in the
// derivation, and it is computed THROUGH truncateField — byte cut, then the
// empty-replacement scrub — rather than as a naive byte-cut sum. Name, argument hint,
// description and aliases across all 51 entries retain 6,711 bytes, which is under 8192
// AND under 10240, so today's real workspace fits inside BOTH established fractions
// after the cut. It happens to EQUAL the naive byte-cut sum at these caps, no captured
// value being cut mid-rune at any of them and no entry's alias list reaching its count
// cap, and saying so is cheaper than leaving a reader to wonder why the two agree. The
// fourth field moved that figure by 64 bytes, the whole of its captured footprint,
// because nothing about it is cut at all. No larger candidate for THIS field clears
// both: 512 retains 8,827 and clears only 5/8.
//
// A CAP FIRING ON CLAUDE'S ORDINARY OUTPUT IS UNAVOIDABLE FOR THIS FIELD, and saying
// so is honest where maxModelListEntries' NOT 8 paragraph could reject exactly that
// outcome for the COUNT. The descriptions alone total 10,580 bytes — already past
// 10240 and past 8192 before a single name is counted — and one of them is 1145 bytes
// by itself. It is stated against the FRACTION and not against the whole 16 KiB
// deliberately: uncut name+hint+description+aliases is 11,668, over both established
// fractions but UNDER 16384, so "past the whole ceiling uncut" would be false and would
// rest this argument on a premise a reader can knock down. The third field moved that
// figure and the fourth moved it again by its whole 64-byte footprint; the premise is
// re-checked at each field rather than assumed, this sentence being built to be
// knock-down-able and a stale number being exactly the knock-down. TruncatedFields is what makes the cut
// honest rather than silent.
//
// MID-RUNE IS ON THE LIVE PATH for this field where it is a corner case for the name:
// 14 of the 51 descriptions carry non-ASCII and no name does, so truncateField's
// empty-replacement DELETION — a cut value landing 1-3 bytes under the limit — is
// ordinary here. At 256 no entry in the capture is mid-rune reachable at all; at 128
// dataviz is (losing 1 byte) and at 64 artifact-capabilities is (losing 2).
//
// Escaping is mild for maxUnrecognizedRaw's reason, verbatim: these are JSON string
// values, so the growth is quotes and backslashes rather than a \u00XX expansion of
// every byte. The one wrinkle measured here is narrow and must not be widened into a
// claim about control characters: claude-api's description carries two 0x0a bytes, and
// 0x0a is the ONLY sub-0x20 byte anywhere across the entries' string fields
// (protocol.SlashCommand's doc carries that measurement). They arrive pre-escaped as
// two printable bytes each, and they are NOT stripped — #1600's verbatim rule governs
// and the client's render boundary owns sanitization.
//
// A separate constant even though it currently equals maxSlashCommandName — and
// maxModelResolved, maxModelField and maxModelValue: maxRateLimitField's paragraph
// applies verbatim, they bound different fields for different reasons, and folding
// them into one would make a future change to one budget silently move this one. This
// doc names its peers; theirs are left as written, #1877 having added
// maxSlashCommandName without reopening them either.
const maxSlashCommandDescription = 256

// maxSlashCommandArgumentHint caps turnevent.SlashCommand.ArgumentHint — claude's
// synopsis of what one entry of the initialize reply's commands array takes AFTER its
// name (#1957). Applied at CONSTRUCTION in emitSlashCommandList, exactly as every cap
// above is, so an oversized value never enters the event stream, the push queue, or
// any log.
//
// MEASURED against the committed capture (claude 2.1.239, byte-identical across all
// three responding arms): fifty-one entries, `argumentHint` PRESENT on every one and
// absent from none — but 33 of the 51 present-and-EMPTY, and 18 carrying text. Longest
// 121 bytes (auto-mode-setup, the only hint carrying non-ASCII), then 77, 55, 44, 41.
// Over the 18 non-empty: mean 29.4, median 17.5 — an EVEN count, so that median is the
// mean of the 9th and 10th of the sorted 18 (16 and 19) and not the 10th alone. Over
// all 51: mean 10.4, median 0. Seven exceed 24 bytes, five exceed 32, two exceed 64 and
// NONE exceeds 128. 530 bytes in total, against the names' 494 and the descriptions'
// 10,580.
//
// THE MULTIPLE IS OVER THE LONGEST, AND NAMING THAT BASE IS THIS DOC'S OWN OBLIGATION
// rather than a courtesy: 256 is 2.1x the longest observed hint, maxSlashCommandName's
// base and stated as its base. That constant takes its 10.7x over the LONGEST and
// maxSlashCommandDescription its ~1.2x and ~3.7x over the MEAN and MEDIAN OF ALL 51;
// neither had to flag the choice, because `name` and `description` are non-empty on
// every entry and the two populations coincide there. HERE THEY DO NOT. "Median 17.5"
// and "median 0" are both true of this field, and over all 51 the median is 0 — so no
// multiple over THAT base exists at all. The other bases are stated so a reader
// re-deriving one does not reach a different number and think this doc wrong: 8.7x the
// non-empty mean, 14.6x the non-empty median, 24.6x the all-51 mean. A figure quoted
// beside the description's 3.7x without its base compares two different measurements.
// maxSlashCommandAlias INHERITED this convention (#1825) and discharged it differently:
// `aliases` is absent on 42 of 51, so its own populations are even further apart than
// this field's — but the base it names is neither of them. It takes its multiple over
// the captured NAMES' longest, on a measured argument that an alias is drawn from the
// name population, and states its own two bases beside it.
//
// WHY NOT 128, and this is the decision rather than an accident of it: 121 is 94.5% of
// 128, so the cap would sit inside the observation's own noise. What 128 does NOT cost
// is any captured value — no hint is cut at 128 either, so the two candidates are
// INDISTINGUISHABLE on the observation and separable only on headroom. A hint is an
// argument synopsis, a function of how many flags a workspace command takes, so it has
// a growth direction a NAME does not, and auto-mode-setup's 121 bytes are the committed
// evidence that workspace authors write long ones. At 128 the claim that this cap does
// not fire on claude's ordinary output is one a point release can falsify with seven
// bytes.
//
// WHY NOT 64 OR BELOW is where this field's argument DIVERGES from the description's
// rather than copying it: at 64 two captured hints are cut, at 32 five and at 24 seven,
// so the cap fires on claude's ordinary output. maxSlashCommandDescription's doc accepts
// exactly that outcome for itself and says so honestly — but only because it has no
// alternative, its descriptions totalling 10,580 bytes with one at 1145 by itself. This
// field HAS an alternative, so accepting the same outcome here would be a choice rather
// than a necessity.
//
// WHY NOT LARGER is maxSlashCommandDescription's MULTIPLICATION argument verbatim: a
// multiplied field earns a smaller unit budget than the same field carried once, and
// this list multiplies by claude's observed 51. What does NOT carry is that constant's
// ROLE argument, the one that kept the description from going lower — this field's whole
// observed footprint is 530 bytes across the capture, ONE TWENTIETH of the
// descriptions', so nothing in the observation asks for more room than 2.1x the longest.
//
// MID-RUNE IS UNREACHABLE ON THE LIVE PATH at this cap, which is the property
// maxSlashCommandDescription's doc records for itself at its own: no captured hint is
// cut at 256, so none is mid-rune reachable, and auto-mode-setup's is the only captured
// value for which the question could arise at all. The cut SEMANTICS are inherited
// whole and unchanged — truncateField's <= boundary and its empty-replacement DELETION
// still govern a constructed value landing 1-3 bytes short.
//
// A separate constant even though it currently equals maxSlashCommandName and
// maxSlashCommandDescription — and maxModelResolved, maxModelField and maxModelValue:
// maxRateLimitField's paragraph applies verbatim, they bound different fields for
// different reasons, and folding them into one would make a future change to one budget
// silently move this one. The obligation is the sharper here for the numbers agreeing
// three ways on three adjacent fields of the SAME entry.
const maxSlashCommandArgumentHint = 256

// maxSlashCommandAlias caps ONE ELEMENT of turnevent.SlashCommand.Aliases, not the
// list — claude's alternative name for one entry of the initialize reply's commands
// array (#1825). Applied at CONSTRUCTION in emitSlashCommandList, exactly as every cap
// above is, so an oversized value never enters the event stream, the push queue, or any
// log. maxModelEffortLevel is the DOC-SHAPE precedent rather than the three sibling
// string caps: it is the family's only other cap on an element of a list INSIDE a list
// entry, and it bounds the same third size dimension.
//
// MEASURED against the committed capture (claude 2.1.239, byte-identical across all
// three responding arms): fifty-one entries, NINE carrying `aliases` and forty-two
// omitting the key, ZERO carrying an empty array. Eleven aliases in total, 64 bytes —
// against the names' 494, the hints' 530 and the descriptions' 10,580, the cheapest
// field of the four by an order of magnitude. Longest 9 bytes (proactive), shortest 3,
// mean 5.82, median 5. NONE carries non-ASCII.
//
// THE MULTIPLE IS OVER THE CAPTURED NAMES' LONGEST AND NOT OVER THIS FIELD'S OWN,
// which is maxSlashCommandArgumentHint's name-the-base obligation inherited with the
// problem different rather than worse. Both bases are stated so a reader re-deriving
// one does not reach a different number and think this doc wrong: 64 is 2.67x the
// longest captured NAME (24, fewer-permission-prompts) and 7.1x the longest captured
// ALIAS (9), also 11x the alias mean and 12.8x the alias median. The name witness is
// named because this doc INVITES re-derivation: __remote-workflow, which carries the
// charset fact everywhere else in this file, is only 17 bytes and would put a
// re-deriving reader at 3.76x.
//
// WHY THE NAME POPULATION IS THE RIGHT BASE, and it is MEASURED rather than assumed
// because the obvious intuition is false. "An alias is a short form of the name" would
// make this field's own longest the base — but THREE of the eleven captured aliases are
// strictly LONGER than the name they alias (checkup for doctor, proactive for loop,
// settings for config) and THREE more TIE it (routines for schedule, reset for clear,
// stats for usage), so six of the eleven are at least as long as the name they stand
// in for. An alias is simply another name for the command, written by the same
// workspace author in the same file, and one captured alias (name, for rename) is a
// token that could equally have been a command's own name. So the population an alias
// is drawn from is the NAME population, whose longest member the capture puts at 24
// bytes, and a cap that admits any name-shaped alias is what this field needs.
//
// WHY NOT 32: it is 3.6x this field's own longest but only 1.33x the longest captured
// name, so the cap would sit inside the noise of the population aliases come from —
// maxSlashCommandArgumentHint's WHY NOT 128 argument, with fewer-permission-prompts'
// 24 bytes as the committed evidence that this vocabulary reaches into that range. 16
// cuts a name-shaped alias outright.
//
// WHY NOT 256, maxSlashCommandName's own number: that cap's 10.7x is paid ONCE per
// entry, and this one is paid up to maxSlashCommandAliasCount times, so copying it
// would apply a singly-multiplied budget to a doubly-multiplied field.
// maxSlashCommandDescription's MULTIPLICATION argument one dimension further down, and
// maxModelEffortLevel's position in its own family: this is the SMALLEST unit budget in
// the slash-command entry, for the field with the smallest observed footprint and the
// most multiplication.
//
// MID-RUNE IS UNREACHABLE ON THE LIVE PATH at this cap, and by a wider margin than any
// sibling's: no captured alias carries non-ASCII at all and the longest is 9 bytes
// against 64, so no captured value is cut and none could be cut inside a rune. The cut
// SEMANTICS are inherited whole and unchanged — truncateField's <= boundary and its
// empty-replacement DELETION still govern a constructed value landing 1-3 bytes short.
//
// It bounds the ELEMENT and maxSlashCommandAliasCount bounds the COUNT, which is what
// turns the multiplication sentence above from a warning into an arithmetic term: this
// cap is multiplied by a KNOWN factor rather than by a number claude chooses, so
// raising it moves a per-entry budget a reader can compute. 64 * 8 = 512 bytes is that
// budget, and maxSlashCommandAliasCount carries its derivation.
//
// A separate constant even though it equals no sibling's number today: maxRateLimitField's
// paragraph applies verbatim — different fields, different reasons, and folding them
// would make a future change to one budget silently move another.
const maxSlashCommandAlias = 64

// maxSlashCommandAliasCount caps how many aliases ONE turnevent.SlashCommand retains —
// the COUNT maxSlashCommandAlias's per-element cap cannot supply, and the last
// unbounded dimension of a command entry (#1825). It is the family's second per-ENTRY
// cardinality bound, maxModelEffortLevelCount being the first; maxTaskRosterEntries and
// maxModelListEntries bound a whole event's entries where these two bound a list INSIDE
// one entry. Applied at CONSTRUCTION like every cap above. Overflow is REPORTED —
// turnevent.SlashCommand.TruncatedFields names "aliases" — rather than silent, which is
// the property that makes a cardinality bound honest.
//
// THE NAME ENDS IN Count DELIBERATELY, maxModelEffortLevelCount's paragraph verbatim
// and for its reason: the plural maxSlashCommandAliases would differ from
// maxSlashCommandAlias by ONE character, both are int, both bound the same field, so
// swapping them at a call site COMPILES and neither vet nor a type error would say so.
// A 64-alias count cap would bound nothing worth bounding and an 8-byte element cap
// would cut `proactive`, claude's ordinary output, on every child.
//
// The arithmetic, in maxModelEffortLevelCount's style:
//
//   - The count itself: SEVEN of the capture's nine alias-carrying entries hold ONE
//     alias and two hold two, so the observed maximum is 2 and over all fifty-one the
//     mean is 0.216 and the median 0. 8 is 4x that maximum, six slots above claude's
//     observed output.
//   - A power of two, matching every constant in this family except maxModelListEntries,
//     whose own doc explains why it alone is decimal.
//   - The LIST's budget: 8 * 64 = 512 bytes. That is TWICE one string field's cap,
//     where maxModelEffortLevelCount's product landed on exactly one — the two variants
//     have different field sets and different multiplicands, and a variant re-deriving
//     its own unit is not violating anything.
//   - The aggregate is maxSlashCommandDescription's, which carries the per-entry term
//     and the candidate fractions #1826 weighed and declined; that slice's own constant,
//     maxSlashCommandListEntries, carries the count it took instead. Cross-referenced
//     rather than restated.
//
// A THICKER MULTIPLE THAN THE FAMILY'S CARDINALITY CONVENTION, and that is the decision
// rather than an accident of it. maxModelListEntries takes 1.67x and
// maxModelEffortLevelCount 1.6x; applied literally here that convention lands on 3 or 4.
// Two arguments push past it, neither of which the existing cardinality caps have.
// FIRST, those bound vocabularies CLAUDE controls — a published effort menu, a
// published model list — while this bounds a list a WORKSPACE author writes in a
// repository, so its growth direction is not bounded by anything claude ships. A
// repository giving one command r, rev, review and code-rev is unremarkable and a cap
// of 4 cuts it, which is maxModelListEntries' NOT 8 failure at this scale: a cap firing
// on ordinary output. SECOND, and this is maxSlashCommandDescription's ROLE argument
// pointing the OTHER way for once: a cut description costs a truncated row, where a cut
// ALIAS costs a working command greyed out in the consumer's menu, because the alias
// the consumer was matching against is the one that was cut. That is the exact failure
// turnevent.SlashCommand.Aliases exists to prevent, so the dimension's own cap must not
// be the thing that causes it.
//
// WHY NOT 16: the product doubles for headroom nothing in the observation asks for, on
// the field with the smallest observed footprint in the entry — 64 bytes across the
// whole capture.
//
// THE PRODUCT WAS NOT CHOSEN TO MAKE THE PER-ENTRY SUM COME OUT ROUND, which
// maxSlashCommandDescription's roundness paragraph asks of this slice by name. 64 and 8
// are each derived above on their own populations and land on 512, putting the term at
// 1280. The round candidate WAS available and is declined rather than missed: a count
// cap of 4 gives 256 and a sum of 1024, and it is rejected on the ROLE argument above
// and not on the arithmetic.
//
// The cap is applied AFTER json.Unmarshal, so a hostile array is materialised in
// transient memory before it is shortened — maxTaskRosterEntries' accepted trade, with
// maxModelResolved's amplification paragraph bounding the exposure and
// defaultMaxParseBuf's 4 MiB whole-line cap bounding the input. This cap bounds what is
// RETAINED, which is the property that matters.
const maxSlashCommandAliasCount = 8

// maxSlashCommandListEntries caps how many entries a turnevent.SlashCommandList carries
// (#1826) — the LAST unbounded dimension of the initialize reply's commands array, and
// the factor the four per-field caps above cannot supply between them: the array's
// LENGTH is claude's, really the WORKSPACE's, to choose, so a per-entry text cap alone
// leaves the total a function of a number the daemon does not control. It is
// maxTaskRosterEntries' doctrine's third application after maxModelListEntries, and that
// constant is its structural twin in every part except the one this doc spends itself
// on. Applied at CONSTRUCTION like every cap above; truncation is FROM THE TAIL, so
// claude's order is preserved and no ranking is invented. Overflow is REPORTED —
// turnevent.SlashCommandList.DroppedCommands — rather than silent, which is the property
// that makes a cardinality bound honest.
//
// MEASURED against the committed capture (claude 2.1.239, byte-identical across all
// three responding arms): FIFTY-ONE entries. A second observation exists and is NOT from
// the capture — SEVENTY-FOUR, a hand count against claude 2.1.220 in a different working
// directory — and it is the binding one below. The count is working-directory AND
// version dependent, which is the feature's whole point, so nothing here may hardcode
// either figure; they are the population this cap is derived to clear, not a shape it
// may assume.
//
// THE NUMBERS DO NOT ALL FIT, AND SPENDING ONE OF THEM IS THIS CONSTANT'S SUBSTANCE.
// Three things this family holds true elsewhere cannot all hold here, and the derivation
// is worthless unless it says which one it gives up.
//
// THE HANDED-FORWARD CANDIDATES ARE NOT AVAILABLE AS WRITTEN, and declining them is the
// first step rather than an afterthought. maxSlashCommandDescription's fraction table
// computes this slice's options against the 1280-byte per-entry WORST CASE: 16384 * 1/2
// = 8192 leaves 6 entries and 16384 * 5/8 = 10240 leaves 8. Both CUT A 51-ENTRY MENU BY
// 43 OR 45 ENTRIES — a cap firing on claude's ORDINARY output, which maxModelListEntries'
// NOT 8 paragraph rejects by name and maxSlashCommandAliasCount's ROLE argument sharpens
// one dimension up: a cut command is a WORKING command greyed out in the named consumer's
// menu, pyrycode-desktop#694 rendering a type-ahead over the whole list. A candidate set
// offered by a doc paragraph is still a candidate set, and taking one mechanically
// because it was handed forward is the failure this paragraph exists to refuse.
//
// SO THE BASE MOVES FROM THE WORST CASE TO THE OBSERVATION, and the figure is the
// package's own rather than a new measurement: maxSlashCommandDescription computes what
// the capture's 51 entries RETAIN through truncateField — name, argument hint,
// description and aliases — at 6,711 bytes, which is 131.59 bytes per entry against the
// 1280-byte worst case. Re-derived at that base the same ceiling gives 62 entries at 1/2
// and 77 at 5/8, and 124 at the whole 16384.
//
// CHANGING THE BASE IS NOT ENOUGH, which is the result that decides everything below.
// The 74-entry observation RETAINS about 9,738 bytes — 59% of maxUnrecognizedRaw's whole
// UNKNOWN-line cap, before any cap fires and with nothing this constant can do about it.
// 1/2 CUTS that observation by twelve entries. 5/8 clears it by THREE, a 1.04x margin on
// a count that is workspace- and version-dependent by design, which is inside the
// observation's own noise. And the family's power-of-two convention leaves exactly two
// candidates above 74: 64, which cuts it, and 128, whose projected retained cost of
// 16,844 bytes is past the WHOLE 16384 ceiling that fraction 1 already forbids
// approaching.
//
// THAT IS maxModelListEntries' OWN SITUATION VERBATIM — "no pair of caps this family's
// own doctrine permits fits the inherited ceiling, so the ceiling was the only lever
// left rather than one of three" — and it is resolved the same way, by the ceiling
// giving. What is NOT copied is that constant's resolution: it moved to a FRACTION of a
// larger ceiling, and this shape has no ceiling it can meet at all. Both halves are
// checkable. The worst-case product exceeds the 65519-byte v2 application-envelope cap
// at any count clearing 74 (74 * 1280 = 94,720), the only count whose worst case fits
// being 51, the observation itself. And maxUnrecognizedRaw's rule is an ORDERING rule —
// a whole KNOWN event must not approach the bound on an entire UNKNOWN line — which
// presupposes the known event is the cheaper thing; for this shape that presupposition is
// already false at claude's ordinary output, at 0.59 of the ceiling, so applying it would
// set this number by how large an UNKNOWN line may be rather than by how large claude's
// KNOWN inventory actually is.
//
// WHAT IS SPENT, stated plainly because a derivation that hides its cost is worth
// nothing: the WORST-CASE-PRODUCT convention. What is KEPT is the no-fire-on-ordinary-
// output rule. What this cap therefore buys is exactly two things — the retained size
// stops being a function of a number the workspace chooses and becomes a function of a
// daemon constant, where before the only bound below it was defaultMaxParseBuf's 4 MiB;
// and the cut is COUNTED, so a shortened menu says by how much. What it does NOT buy is
// a frame that fits: 128 * 1280 = 163,840 raw bytes is 2.5x the 65519-byte envelope —
// and that is raw-byte arithmetic rather than a wire measurement, since Go escapes '<',
// '>', '&' and U+2028/U+2029 at six bytes each, so the true wire worst case is higher
// still. NO count cap can close that gap either way.
//
// THAT FRAME-LEVEL BOUND NOW EXISTS, and it is not here: turnbridge's
// maxSlashCommandListBytes (#2002) cuts the mapped list a second time, by MEASURED
// bytes, at the one place both wire consumers pass through. The two cuts compose rather
// than compete — this one bounds the RETAINED COUNT and reports it, that one bounds the
// SERIALISED SIZE and adds its own drops to the same number, so len(commands) +
// dropped_commands stays the list's true size after both. A reader arriving from there
// must still not read "the entry count is bounded" as "the frame fits": it does not,
// and a second, differently-shaped cut is exactly why.
//
// 128, AND THE THREE CONSTRAINTS LEAVE ONE SURVIVOR:
//
//   - ABOVE THE LARGER OBSERVATION. 1.73x over 74 and 2.51x over the capture's 51. That
//     is above the family's cardinality convention (maxModelListEntries 1.67x,
//     maxModelEffortLevelCount 1.6x) and below maxSlashCommandAliasCount's 4x, and it
//     earns the thicker end for that constant's own two reasons: this bounds a list a
//     WORKSPACE author writes in a repository, whose growth direction nothing claude
//     ships bounds, and a cut entry costs a working command greyed out in the consumer's
//     menu rather than a truncated row.
//   - A POWER OF TWO, matching every constant in this family except maxModelListEntries,
//     whose own doc explains why it alone is decimal.
//   - NOT GRATUITOUS. 256 is 3.5x the larger observation and projects 33,687 bytes
//     retained — over twice the whole-line ceiling and past half the envelope — for
//     headroom neither observation asks for. maxSlashCommandAliasCount's WHY NOT 16,
//     one dimension up.
//
// THE CUT IS IN emitModelList, NOT IN THE EMITTER, and the placement is load-bearing
// three ways. It runs ONCE above BOTH rungs that read the array, so there is no second
// cap for the two to keep in step. It is available to logControlResponse, which both
// rungs call BEFORE any emit, which is what lets the record report the drop at all.
// And it BOUNDS THE ALLOCATION in emitSlashCommandList, whose make() is sized from
// len(entries): moving this cut into that emitter's loop would compile, pass every
// assertion about the emitted value, and silently restore an allocation sized by the
// workspace.
//
// IT CANNOT MOVE A RUNG'S CLASSIFICATION, and that is structural rather than tested-in:
// the cap is >= 1, so the emitted count is 0 exactly when the decoded count is, and the
// ack rung's "neither array" test partitions against the commands-only rung exactly as
// it did. Capping can neither create an empty list nor rescue one.
//
// THE RESLICE IS DELIBERATE where boundAliases refuses the same shape, and the two are
// consistent rather than in tension. That closure must not return values[:n] because its
// result IS retained by the emitted event; this cut MAY reslice because its result is
// not — emitSlashCommandList appends CONSTRUCTED values into a fresh allocation, and the
// resliced header dies with emitModelList. maxModelListEntries' block reslices for this
// same reason.
//
// THE AMPLIFICATION IS COMPUTED HERE RATHER THAN INHERITED, because the paragraph this
// family usually cross-references is FALSE for this struct. maxTaskRosterEntries reports
// its own as "linear and near 1" on ~55 bytes of input per ~64-byte struct.
// commandEntryLine's densest legal entry is `{},` — THREE bytes, every key being optional
// and absence being claude's to choose — for a 72-byte struct (three string headers and
// one slice header). So a single defaultMaxParseBuf-sized 4 MiB line yields ~1.4M decoded
// entries and on the order of 100 MB of TRANSIENT allocation before this cut discards all
// but 128: an amplification near 24x, not near 1. It is bounded rather than unbounded —
// one line at a time, reclaimed, and nothing past emitModelList retains it — which is
// maxTaskRosterEntries' accepted trade, but the FIGURE is this struct's own.
const maxSlashCommandListEntries = 128

// commandEntryLine is one element of the initialize payload's `commands` array —
// claude's slash-command inventory for the workspace the child was spawned in
// (#1853). "Entry" rather than modelOptionLine's "option": that word names a menu
// choice the daemon publishes, and a slash command is not one.
//
// FOUR FIELDS OF FOUR SINCE #1825, AND THE OMISSION THIS PARAGRAPH USED TO DEFEND IS
// GONE RATHER THAN RELAXED. claude sends four keys per entry — name, argumentHint,
// description, aliases. Name arrived with the decode (#1853), Description with #1904,
// ArgumentHint with #1957 and Aliases with #1825, each when a slice had a consumer for
// it. The SLICE-SCOPED reading the paragraph insisted on is what made that sequence
// possible: a field that is never declared cannot reach a log or an event, which is
// what each omission bought until the slice that spent it. What a later reader must
// not do now is the mirror of what it forbade then — treat this key set as open and
// add a field for a key claude has not been observed to send. protocol.SlashCommand's
// own doc carries the measurement that the four ARE the whole vocabulary, so a fifth
// key arrives as a documented gap against a stated measurement rather than as a silent
// drop.
//
// THE MEMORY ARITHMETIC INVERTED WHEN THE SECOND FIELD LANDED AND IS SETTLED NOW, and
// what moved over the sequence was the ARGUMENT and not only the percentage. The
// captured array is 14,277 bytes compact; the fifty-one `name` strings inside it total
// 494, the fifty-one `argumentHint` strings 530, the fifty-one `description` strings
// 10,580 and the eleven aliases across nine entries 64 — so 11,668 of 14,277, 81.7%,
// becomes a Go string and nothing is kept out by omission at all. It was 96% when one
// field was declared, 77.6% when two were and 81.3% when three were, which is why the
// omission USED to be the whole of the memory story and is none of it now. The fourth
// field is the cheapest of the four by an order of magnitude, 64 bytes against the
// hints' 530 and the descriptions' 10,580, so it moved the percentage by four tenths of
// a point while moving the worst-case term by two thirds. What carries that weight is
// the per-field cap one step downstream, which bounds what is RETAINED whatever this
// struct decodes: the 11,668 is transient, and the capture's 51 entries retain 6,711
// bytes of it after maxSlashCommandName, maxSlashCommandArgumentHint,
// maxSlashCommandDescription and the alias pair have run.
//
// EVERY STRING HERE IS WORKSPACE-AUTHORED. A slash command defined in a repository
// was written by whoever wrote that repository, and the daemon reads it in whatever
// directory the operator points a session at. Name is nevertheless NOT validated —
// not for emptiness, not for charset, not for control bytes — and the reason is NO
// SINK rather than safe bytes. The capture carries `__remote-workflow`, which is the
// committed proof that no charset may be assumed.
//
// THAT VALIDATION QUESTION WAS LEFT OPEN FOR THE FIRST SLICE THAT READS Name, AND IT
// IS ANSWERED HERE. A byte-capped COPY of Name now leaves emitSlashCommandList inside
// a turnevent.SlashCommand (#1877), so the count is no longer the only thing that does.
// Validation stays REFUSED and the reason is still NO SINK — but a CHECKED no-sink
// rather than a structural impossibility, and the check is what this paragraph
// records. Name reaches: one field of a daemon-internal struct, the parser's emit
// callback, and from there cmd/pyry's interactiveTurnEmitterV2.Handle default, which
// has no case for the variant and logs it BY KIND through eventKind before discarding
// it. It stops THERE and does not reach turnbridge.MapEvent at all — an enumeration
// naming MapEvent's default would be crediting it with reach it does not have, since
// MapEvent's two production call sites are emitMapped, which only Handle's TYPED arms
// reach and none of those is SlashCommandList, and resolveBoundModelList, which hands
// it a ModelList explicitly. It reaches
// no exec.Command argument, no filepath.Join, no filepath.Match, no regexp, no log
// attribute, no eventring append and no wire frame. An ENUMERATION OF REACHED SINKS
// is the answer rather than a judgement about the bytes, and precisely because `[`,
// `*` and `?` are syntax to filepath.Match and to regexp with no shell anywhere in
// sight: "these look like identifiers" would be the wrong argument for a string one
// captured entry already spells __remote-workflow.
//
// DESCRIPTION WAS WALKED THROUGH THAT ENUMERATION ON ITS OWN (#1904) rather than
// inheriting Name's answer, because the enumeration above is a claim about Name
// established by inspection and not a property of the path. It rides the same struct
// field set, the same emit callback and the same two defaults, and it reaches the same
// empty set of sinks — so the answer is the same and the REASON it is the same is the
// re-walk, not the shared origin. What differs is only that the "these look like
// identifiers" argument was never available for it at all: this field is prose, and 14
// of the capture's 51 descriptions carry non-ASCII.
//
// THE ARGUMENT HINT WAS WALKED THROUGH IT ON ITS OWN TOO (#1957), inheriting NEITHER
// answer, for the reason the paragraph above gives: the enumeration is a claim about a
// value established by inspection and not a property of the path. It rides the same
// struct field set, the same emit callback and the same two defaults, and it reaches
// the same empty set of sinks — no exec.Command argument, no filepath.Join, no
// filepath.Match, no regexp, no log attribute, no eventring append and no wire frame.
// So the answer is the same and the REASON it is the same is the re-walk. What differs
// is that "these look like identifiers" is LEAST available here of the three: 13 of the
// capture's 18 non-empty hints carry `[` and `]`, ten carry `<` and `>`, seven carry
// `|` and one carries parentheses — and `[` alone is a character class to regexp and a
// bracket expression to filepath.Match. Where Name's enumeration had to reach for a
// single counter-example, syntax characters are this field's MAJORITY case. No captured
// hint carries `*`, `?` or a backslash, and the argument neither needs them nor claims
// them.
//
// EVERY STRING IN Aliases WAS WALKED THROUGH IT ON ITS OWN TOO (#1825), inheriting
// none of the three answers, for the reason the two paragraphs above give: the
// enumeration is a claim about a value established by inspection, not a property of the
// path. Each alias rides the same struct field set, the same emit callback and the same
// ONE default, and reaches the same empty set of sinks — no exec.Command argument, no
// filepath.Join, no filepath.Match, no regexp, no log attribute, no eventring append
// and no wire frame. So the answer is the same and the REASON it is the same is the
// re-walk. TWO things differ. The value is not one string but a LIST of them, so the
// enumeration is over every element and not over a field, which is why this paragraph
// says "every string in Aliases" rather than naming the field; the count is bounded by
// maxSlashCommandAliasCount, so the enumeration is over a bounded set rather than over
// a number claude chooses. And "these look like identifiers" is available here in a way
// it is not for the other three — every captured alias is inside [a-z], the shortest 3
// bytes and the longest 9 — which is exactly why it is REFUSED: an alias is drawn from
// the same population as a NAME, one member of which the capture spells
// __remote-workflow, so an argument from the observed alias charset would rest on nine
// entries' worth of coincidence. The empty sink set is the answer for this field as it
// is for the others.
//
// THE RE-OPEN TRIGGER, named rather than left to be noticed and covering ALL FOUR
// declared values: the first slice that gives any of them a SYNTAX sink — a path
// element, a match pattern, a regexp, an argv element, a log attribute — or that renders
// one into an HTML sink, an attribute or a URL inherits the question open again. #1720
// is the nearest such slice, and what it owes is the CLIENT-side render boundary
// turnevent.SlashCommandList's SECURITY paragraph already assigns.
//
// THE DECODED SLICE IS UNCAPPED HERE, and the bound is one level up rather than
// added: defaultMaxParseBuf caps the whole line at 4 MiB before the decoder sees it,
// which is already the whole of what bounds the models array's transient spike (see
// controlResponseLine's neighbouring paragraph). The arithmetic still favours this
// array on both sides, and #1825 is where the COMPARISON had to be re-derived rather
// than restated: this struct is FOUR fields where modelOptionLine is five, and the
// fourth is a []string exactly as modelOptionLine's EffortLevels is, so the two are now
// compared shape for shape rather than three-scalars against five-mixed. Per
// densest-legal element the worst-case transient is still a fraction of the
// already-accepted one — four fields against five, one nested list each — and a single
// 4 MiB line cannot maximise both. The direction is unchanged and the margin is
// thinner, which is why this is re-derived here and not simply restated.
//
// FOUR PER-FIELD CAPS DO EXIST ACROSS FIVE DIMENSIONS, one step downstream, which is
// what narrows the transient/retained contrast this paragraph used to draw:
// maxSlashCommandName bounds Name, maxSlashCommandArgumentHint bounds ArgumentHint,
// maxSlashCommandDescription bounds Description, and maxSlashCommandAlias with
// maxSlashCommandAliasCount bound Aliases in its two — all at CONSTRUCTION in
// emitSlashCommandList (#1877, #1904, #1957, #1825), where every cap in this package is
// applied. They are separate constants rather than one shared limit for
// maxRateLimitField's reason, and the description's is by far the largest contributor
// to what is retained. The transience is unchanged — this slice still lives from
// json.Unmarshal until emitModelList returns — but a BOUNDED COPY of each of the three
// strings and of a bounded number of bounded aliases is now retained inside the emitted
// turnevent.SlashCommandList. That lifetime is the models array's since #2004 and no
// longer shorter than it: cmd/pyry holds the CAPPED result of each for the session's
// life, this one in sessionSlashCommandHold and that one in sessionModelHold.
//
// The decode is all-or-nothing at the LINE, modelOptionLine's rule verbatim and at
// the ELEMENT level too: a `commands` that is a number, a string or an object, an
// element that is a bare string or a number, a `name`, an `argumentHint` or a
// `description` that is not a string, an `aliases` that is not an array, or an ELEMENT
// of `aliases` that is not a string all fail the WHOLE-LINE decode and take
// emitModelList's undecodable rung. The rule is the STRUCT's and not any one field's,
// which is why the second, third and fourth declared keys inherit it rather than
// earning branches of their own. The bare string is worth naming because it is the
// shape a future claude most plausibly sends: systemInitLine's line already spells this
// same inventory as bare strings under slash_commands.
//
// THE FOURTH KEY EXTENDS THAT RULE ONE LEVEL DEEPER AND THAT IS THE WHOLE OF WHAT IS
// NEW: `aliases` is the only declared key with an INSIDE, so it is the only one where a
// well-formed value of the right outer shape can still fail on an element. A
// `["reset", 5]` fails the whole line exactly as a numeric `name` does — the failure is
// the struct's, and no partial alias list survives carrying only the elements that
// happened to decode. The AVAILABILITY COST of declaring a key is real and is accepted
// here as it was three times before: a shape claude ships later that this struct cannot
// hold now costs the whole line, the models array with it, rather than being ignored.
// What makes that honest is the rung table, which carries a row per declared key and
// records the transition each time — a key that was undeclared and silently ignored
// becomes a decode-failure source the moment it is declared.
//
// JSON null is the one carve-out and it applies at ALL FOUR positions, for
// modelOptionLine's reason: encoding/json documents unmarshalling a null into a
// non-pointer Go value as a NO-OP producing no error, and documents a null into a SLICE
// as setting it nil — so a null `commands` lands as a nil slice, a null `name`,
// `argumentHint` or `description` lands as "" and a null `aliases` lands as nil, each a
// counted entry rather than a failed line. For all four that is also the reading an
// ABSENT key gets, so absent and null are one reading per position and no consumer can
// branch on the difference.
//
// THE FOURTH POSITION'S ZERO VALUE IS nil AND NOT "", which is why it is spelled out
// rather than folded into the sentence above: the collapse there is over three shapes
// and not two, a published `[]` being a shape the other three positions have no
// analogue for. This struct does NOT collapse it — an absent key and a null leave the
// field nil while a published `[]` leaves an empty non-nil slice — and the
// normalisation happens one step downstream at construction. turnevent.SlashCommand's
// own field doc owns that decision and the 0-of-51 measurement behind it.
//
// THAT COLLAPSE COSTS NOTHING OBSERVED FOR argumentHint, and it is written down rather
// than left implicit because it was the first field for which the question was worth
// asking at all. The key is present on all 51 captured entries and absent from none,
// while 33 of them carry "" — so an EMPTY hint is the ordinary case and an absent one
// is not observed. `aliases` INVERTS that and was decided on its own measurement rather
// than on this one: 42 of 51 entries omit the key, ZERO carry an empty array, and the
// collapse there folds a common absence into an emptiness claude has never sent. The
// distinguishability protocol.SlashCommand's doc protects is the WIRE's, and it is
// protected there by declaring no omitempty rather than by anything this decode does;
// #1819 is the precedent for stating on the type WHY a collapse is safe instead of
// leaving a reader to infer it.
type commandEntryLine struct {
	Name         string   `json:"name"`
	ArgumentHint string   `json:"argumentHint"`
	Description  string   `json:"description"`
	Aliases      []string `json:"aliases"`
}

// emitSlashCommandList emits AT MOST ONE turnevent.SlashCommandList for one decoded
// `commands` array — the initialize reply's slash-command inventory for the workspace
// the child was spawned in. It takes the DECODED entries rather than the line: the
// decode, its error path and the whole rung classification stay in emitModelList,
// and a second json.Unmarshal of the same bytes would add a second undecodable outcome
// to classify.
//
// IT LOGS NOTHING, on any path. The one record every control_response produces is
// logControlResponse's and is written by emitModelList BEFORE this call, which is what
// keeps "seven attributes, the same values, the same reason keyword" a structural
// property of the caller rather than a promise this function has to keep.
//
// THE GATE IS THIS EMITTER'S PRECONDITION, so every caller inherits the suppression
// rather than repeating it. An absent `commands`, a null one, an empty array and a
// response object carrying no such key are ONE reading and all return here —
// controlResponseLine.Commands is a plain slice precisely so that they are.
//
// SUPPRESSION RATHER THAN AN EMPTY EMIT, and the decision is this producer's to
// take: turnevent.SlashCommandList.Commands' doc hands it here explicitly. What
// decides it is that THE PRODUCER CANNOT MAKE THE STATEMENT AN EMPTY EMIT WOULD BE
// MAKING. protocol.SlashCommandListPayload.MarshalJSON declares a wire [] a
// POSITIVE statement — claude offered nothing — but by the time this line runs the
// decode has already collapsed absent, null and a published [] onto one nil slice,
// so the daemon cannot tell "claude offered nothing" from "claude said nothing
// about commands". Emitting an empty list would assert the first from evidence
// that cannot distinguish it from the second, which is inventing a distinction
// rather than reporting one. Rung 3's false-negative asymmetry is NOT the
// argument, and was weighed rather than inherited: it rests on both outcomes being
// observable to a client (#1849 made them so for models), and nothing publishes
// this list today (#1720), so that footing is unavailable here. The wire's []
// position is untouched either way — it governs how a list that WAS emitted
// serialises, which says nothing about whether to emit one, and #1720 reads it for
// that.
//
// WHERE THE CONSTRUCTED LIST GOES, and it is NOT where a mis-read model inventory
// goes — emitModelList's own paragraph on that names three destinations and this value
// reaches none of the three, so the two must not be read across. It goes to the
// parser's emit callback and, on the interactive lane, to cmd/pyry's
// interactiveTurnEmitterV2.Handle, which has NO CASE for this variant: it lands on that
// function's own default, which logs it by kind through eventKind and returns. Because
// that default returns, emitMapped never runs for it — and emitMapped is
// turnbridge.MapEvent's only caller on this lane, resolveBoundModelList being handed a
// ModelList explicitly — so the value never reaches MapEvent AT ALL. "It falls to
// MapEvent's default" is the wrong reason for the right conclusion. IT IS ALSO RETAINED
// since #2004, by cmd/pyry's sessionSlashCommandHold, for the session's life —
// maxSlashCommandName's doc states and owns that, as it owned the absence before it.
// MapEvent's arm has since LANDED
// (#2001) and that does not change a word above: an arm is not a route, and nothing on
// this lane calls MapEvent with this variant until Handle's case, which is #2003's and
// still open. What the value DOES reach is eventKind, whose
// SlashCommandList arm returns the variant NAME only — and that arm, not this doc,
// carries the enumeration of which drop sites are reachable for it and which are not,
// so there is one copy to correct when #2003's case lands.
func (p *Parser) emitSlashCommandList(entries []commandEntryLine, dropped int) {
	if len(entries) == 0 {
		return
	}
	// slashCommands rather than a name shared with the parameter: `entries` holds the
	// DECODED entries and this holds the CONSTRUCTED turnevent.SlashCommand values, so
	// two names keep the two apart. The shadowing this name used to avoid — the int
	// `commands` the record reports — is no longer a hazard: that int stays in
	// emitModelList.
	slashCommands := make([]turnevent.SlashCommand, 0, len(entries))
	for _, entry := range entries {
		// Declared INSIDE the loop, and that scope is the whole of what makes the report
		// per entry: a cut on one entry cannot appear on the entries after it.
		// emitModelList's models loop declares its own `cut` and `droppedLevels` inside
		// its own loop for the same reason.
		var cut []string
		// A `bound` closure since #1904, where a one-field entry needed none. The claim
		// this comment used to carry — that one field means there is no TruncatedFields
		// ORDER for a composite literal to decide, which was the only thing the device
		// protected — stopped being true the moment there were two fields to order, so
		// the premise went rather than the conclusion being defended. It is
		// emitModelList's own closure in shape and in scope: declared INSIDE the loop
		// because `cut` is, which is what keeps one entry's report off the entries after
		// it, and closing over that slice rather than returning a second value. Two
		// sequential truncateField calls would work equally well and were weighed; the
		// closure wins on there being ONE place the append happens rather than one per
		// field, so "declaration order == call order" is checked at the call sequence
		// below and nowhere else — which is what made INSERTING the third call in #1957
		// a one-line edit rather than a re-derivation of where the append happens. It
		// bounds THREE of the entry's four fields since #1825; the fourth is a list and
		// gets boundAliases below, which appends to this same `cut` slice so the ONE place
		// stays one place.
		// emitModelAnnounced's copy of the retired sentence is
		// about ModelAnnounced.Model's own single field and is untouched by this.
		bound := func(value, name string, limit int) string {
			out, truncated := truncateField(value, limit)
			if truncated {
				cut = append(cut, name)
			}
			return out
		}
		// boundAliases is bound's sibling for the one field of this entry that is a LIST,
		// and it exists rather than a fourth bound call because a list is where ONE name
		// has to cover MANY values — and, here, TWO cut dimensions. It is emitModelList's
		// boundEach in shape and in every property; what follows states only what this
		// copy owes on its own, the four properties themselves being that closure's doc.
		//
		//   - A ZERO-LENGTH input returns nil and appends nothing, so an absent key, a JSON
		//     null and a published [] all read alike. That is the collapse
		//     turnevent.SlashCommand.Aliases decides, implemented here; the arm returns
		//     BEFORE everything below, so an empty list is neither counted against the
		//     count cap nor named in the report.
		//   - The COUNT bound then runs, truncating FROM THE TAIL so claude's order
		//     survives, with a > boundary rather than >= so a list at exactly the cap is not
		//     named as cut. Its position is load-bearing for a SECOND reason this copy
		//     states because the sibling has no need to: it bounds the ALLOCATION below.
		//     An entry carrying a million aliases makes a slice of maxSlashCommandAliasCount
		//     and runs truncateField that many times, not a million of each.
		//   - Every SURVIVING element then goes through truncateField into a slice of the
		//     SAME length, so cutting an element neither drops it nor disturbs claude's
		//     order.
		//   - The name is appended AT MOST ONCE, after the loop, whether the cause was an
		//     over-long element, an over-long list or both.
		//
		// THE RESULT IS ALWAYS A FRESH ALLOCATION, never the resliced input, and that is
		// the one plausible optimisation this closure must refuse. Returning values[:n] on
		// the all-fit path would make the emitted event retain the DECODER's backing array
		// — up to a 4 MiB line's worth of string headers — for the event's whole lifetime,
		// where the point of these caps is that what is RETAINED is bounded.
		//
		// Named for its FIELD rather than boundEach, which is what emitModelList's copy is
		// called: two closures of one name in one package make every by-symbol citation to
		// either one ambiguous, and a citation is the only thing that would notice. It has
		// one call site and one field, so naming it for the field costs nothing. The
		// (values, name, limit) signature is KEPT despite that, because the report name at
		// the CALL SITE is what makes declaration order visible in the call sequence below;
		// the count constant is read from package scope rather than passed as a fourth int,
		// boundEach's stated reason — a fourth int argument puts the two caps adjacent at
		// the call site, which is the swap maxSlashCommandAliasCount's naming paragraph
		// spends itself preventing. It closes over the same per-entry `cut` slice bound
		// does and cannot be hoisted for the same reason.
		boundAliases := func(values []string, name string, limit int) []string {
			if len(values) == 0 {
				return nil
			}
			var cutAny bool
			if len(values) > maxSlashCommandAliasCount {
				values = values[:maxSlashCommandAliasCount]
				cutAny = true
			}
			out := make([]string, len(values))
			for i, alias := range values {
				var truncated bool
				out[i], truncated = truncateField(alias, limit)
				cutAny = cutAny || truncated
			}
			if cutAny {
				cut = append(cut, name)
			}
			return out
		}
		// Sequential statements in DECLARATION ORDER, emitModelList's models loop's rule
		// and now for its reason rather than by resemblance: TruncatedFields is ordered by
		// these calls, so `[]string{"name", "argument_hint", "description", "aliases"}` on
		// an entry with all four cut is a
		// property of the call sequence a reader sees. The names are the DAEMON's
		// snake_case ones, not claude's camelCase keys — and for all four they
		// coincide with the wire names protocol.SlashCommand.TruncatedFields documents, so
		// a later mapping is a copy rather than a translation.
		// That distinction was a PREDICTION until #1957 and is the live case now:
		// `name`, `description` and `aliases` are byte-identical as claude's key and as the
		// daemon's name, where the hint is claude's `argumentHint` against the daemon's
		// `argument_hint` — one field of four, which is what makes it a distinction rather
		// than a rule.
		//
		// The hint's call is INSERTED BETWEEN the other two rather than appended after
		// them. Appending it compiles, passes any membership check, and silently breaks
		// the declaration order turnevent.SlashCommand's doc promises and the wire
		// mapping depends on — which is why TestParser_SlashCommandFieldsAreCapped
		// compares TruncatedFields by exact equality rather than by membership. The alias
		// call IS appended, and correctly: its slot is the END of the declaration order on
		// both types, so this is the one of the three field slices where the placement that
		// compiles most easily is also the right one — which is why the exact-equality
		// comparison matters no less here.
		name := bound(entry.Name, "name", maxSlashCommandName)
		hint := bound(entry.ArgumentHint, "argument_hint", maxSlashCommandArgumentHint)
		description := bound(entry.Description, "description", maxSlashCommandDescription)
		aliases := boundAliases(entry.Aliases, "aliases", maxSlashCommandAlias)
		slashCommands = append(slashCommands, turnevent.SlashCommand{
			// claude's name VERBATIM (#1600's rule): no lowercasing, no trimming, no
			// charset filtering, no leading "/" added or removed. The cap is the only
			// judgement made about it here and it is a BYTE cut — see maxSlashCommandName,
			// and turnevent.SlashCommand.Name for the same rule stated at the consumer.
			Name: name,
			// claude's hint VERBATIM under the same #1600 rule: no lowercasing, no
			// trimming, no charset filtering. Its cap is its OWN
			// (maxSlashCommandArgumentHint), not Name's and not Description's, even though
			// all three numbers currently agree.
			//
			// AN EMPTY HINT IS CARRIED AS AN EMPTY STRING rather than dropped or defaulted,
			// and it is the ORDINARY case rather than an edge one: 33 of the capture's 51
			// entries carry "" and none omits the key. Nothing here branches on emptiness,
			// which is what keeps that true — turnevent.SlashCommand's own field doc states
			// the consequence for a consumer.
			//
			// BETWEEN Name and Description: protocol.SlashCommand's declaration order, and
			// the slot turnevent.SlashCommand's doc reserved for this field before it
			// existed.
			ArgumentHint: hint,
			// claude's description VERBATIM under the same #1600 rule, with NO NEWLINE
			// STRIPPING named explicitly because this is the field a captured value carries
			// newlines in — see protocol.SlashCommand's doc for that measurement and its
			// narrowness. Its cap is its OWN (maxSlashCommandDescription), not Name's, even
			// though the two numbers currently agree.
			//
			// BETWEEN ArgumentHint and TruncatedFields, which is protocol.SlashCommand's declaration
			// order minus the fields the daemon type does not declare yet — see
			// turnevent.SlashCommand, whose doc owns that promise and now cites this field as
			// the first evidence it was kept.
			Description: description,
			// Through `boundAliases` rather than `bound`: the caps are per ELEMENT and per
			// COUNT while the report is per FIELD. claude's order survives both of them and
			// the list's cardinality survives an element CUT untouched — what changes the
			// cardinality is the count bound, which shortens FROM THE TAIL and says so in
			// TruncatedFields. #1600's verbatim rule covers the elements exactly as it covers
			// the three strings, and one clause of it is this field's alone: NO EXPANSION INTO
			// SYNTHETIC ENTRIES. `reset` is an alias of `clear` and must not become a command
			// named `reset`; the loop this statement sits in appends exactly one
			// turnevent.SlashCommand per decoded entry, and that is the whole of what keeps
			// it true.
			//
			// AN ABSENT, NULL OR EMPTY LIST IS CARRIED AS nil rather than as an empty non-nil
			// slice, which is the closure's zero-length arm and the collapse
			// turnevent.SlashCommand.Aliases owns the argument for. Absence is the MAJORITY
			// case here where an EMPTY hint is the majority case above: 42 of the capture's
			// 51 entries omit the key and none publishes [].
			//
			// BETWEEN Description and TruncatedFields: protocol.SlashCommand's declaration
			// order, and the slot turnevent.SlashCommand's doc reserved for this field before
			// it existed. Its bound call is APPENDED after the other three rather than
			// inserted between them, which is what that slot means at the call site.
			Aliases: aliases,
			// nil when nothing was cut: append never ran.
			TruncatedFields: cut,
		})
	}
	// THE ENTRY-COUNT CAP IS THE CALLER'S AND THE DROP COUNT ARRIVES AS A PARAMETER
	// (#1826), which is the one place this emitter is not self-contained and the reason
	// is worth having at the site. logControlResponse's record is written by emitModelList
	// BEFORE this call and this function logs nothing on any path, so a count computed
	// HERE could not reach the record without threading it back out of a void-returning
	// function with two call sites. Cutting in the caller instead puts the number where
	// the record can read it, keeps ONE cut for BOTH rungs, and bounds the make() above —
	// maxSlashCommandListEntries' doc argues all three. The precedent is exact and in
	// this emitter's caller: #1811 emitted turnevent.ModelList with no entry-count cap
	// and #1812 added maxModelListEntries and DroppedModels in the next slice.
	//
	// `dropped` is 0 whenever the precondition returned above, by construction rather
	// than by a check here: the caller cuts to a cap of at least one, so an empty
	// argument slice cannot have come with a non-zero drop.
	p.emit(turnevent.SlashCommandList{Commands: slashCommands, DroppedCommands: dropped})
}
