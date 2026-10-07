package streamsup

import (
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/modelfamily"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// maxModelResolved caps turnevent.ModelOption.ResolvedModel — claude's concrete
// identifier for one entry of the initialize reply's models array. Applied at
// CONSTRUCTION, exactly as the caps above are, so an oversized value never enters
// the event stream, the push queue, or any log.
//
// MEASURED against the committed capture (#1688, claude 2.1.239, six entries):
// the longest resolvedModel is 25 bytes (claude-haiku-4-5-20251001), so 256 is
// roughly 10x the observation — maxModelField's multiple over the same identifier
// shape, and for its reason verbatim: room for a naming scheme claude has not
// shipped yet, and still a hard cut on anything that has stopped being an
// identifier.
//
// A separate constant even though it currently equals maxModelField,
// maxRateLimitField and maxTaskFieldID: maxRateLimitField's paragraph applies
// verbatim — they bound different fields for different reasons, and folding them
// into one would make a future change to one budget silently move this one.
//
// THE ENVELOPE ARITHMETIC, and it is COMPLETE: FOUR terms, every one of them bounded
// by a constant of the daemon's. Per ENTRY the worst case is maxModelResolved +
// maxModelValue + maxModelDisplayName + maxModelEffortLevelCount *
// maxModelEffortLevel = 256 + 256 + 256 + 256 = 1024 bytes of claude-derived text,
// 1.6% of the v2 application-envelope cap of 65519 bytes (docs/protocol-mobile.md §
// Application-envelope size cap). 1024 is written here as the MULTIPLICAND rather
// than as anything else, and the factor it is multiplied by is maxModelListEntries —
// which carries the aggregate arithmetic rather than restating it here, exactly as
// maxTaskRosterEntries carries the roster's. A per-entry text cap alone would leave a
// list's total a function of a number claude chooses, which is maxTaskRosterEntries'
// doctrine; the count bound supplies the missing factor, and the one constraint it
// had to satisfy is checked there: the observed list is already SIX entries, so
// maxTaskRosterEntries' 8 could not be reached for by analogy without checking that
// six fits with room left.
//
// THE FOURTH TERM IS A PRODUCT RATHER THAN A CAP, which is worth naming because it
// was the last dimension of this entry left open. An entry carries a level LIST, so
// its budget is a per-element cap times a count bound and not a single number: until
// both existed the honest per-entry figure was 768 + 32N with N claude's to choose,
// and a reader who lands here to learn what an entry costs would have inherited a
// completeness the numbers did not support. That the four terms come out EQUAL is
// maxModelEffortLevelCount's third bullet rather than a coincidence to lean on:
// 8 * 32 = 256 bytes is exactly one string field's cap, which lands this multiplicand
// on maxTaskRosterEntries' own 1024-byte entry unit.
//
// Amplification from input to retained bytes is bounded but NOT near 1 the way the
// scalar targets' is, and this is the one place that difference shows. The cap is
// applied AFTER json.Unmarshal, so a hostile array is materialised in transient
// memory before any of it is bounded — maxTaskRosterEntries' transient paragraph,
// verbatim. defaultMaxParseBuf caps the whole line at 4 MiB before the decoder sees
// it and the densest legal entry is `{}`, so one pathological line is order 100 MB
// of transient — the spike is real, and that constant is the whole of what bounds
// it.
//
// TRANSIENT AND RETAINED ARE TWO DIFFERENT FIGURES HERE, which is what makes the
// bound above the whole story rather than half of it. What is TRANSIENT is the
// unbounded decoded array itself: it lives from json.Unmarshal until emitModelList
// applies the caps, and is garbage from that point on — nothing downstream is ever
// handed it. What is RETAINED is only the CAPPED RESULT: at most
// maxModelListEntries entries, each bounded by the three string caps and by the
// level product above, and maxModelListEntries carries that aggregate rather than
// restating it here. TWO holders retain it, both in cmd/pyry — sessionModelHold
// keeps the decoded value for the session's life (#1840) and emitMapped's eventring
// append keeps the mapped payload per conversation (#1849) — so what outlives the
// line is the product, never the spike.
const maxModelResolved = 256

// maxModelValue caps turnevent.ModelOption.Value. maxModelResolved's paragraph
// applies verbatim — the measurement, the multiple, the separate-constant rule and
// the envelope arithmetic are ONE argument covering all three fields, stated once
// there rather than transcribed three times.
//
// The one sentence that is this field's own: Value is not a dated identifier and
// not even always a model name — an alias (sonnet), a bracketed variant
// (claude-fable-5[1m], the capture's longest at 18 bytes), or default — so what
// this cap bounds is claude's argument vocabulary rather than its naming scheme.
const maxModelValue = 256

// maxModelDisplayName caps turnevent.ModelOption.DisplayName. maxModelResolved's
// paragraph applies verbatim, for maxModelValue's reason.
//
// The one sentence that is this field's own: DisplayName is PROSE rather than an
// identifier ("Default (recommended)", the capture's longest at 21 bytes), so it
// has the weakest claim of the three to a naturally bounded length. That argues for
// keeping it level with its two siblings, NOT for giving it a smaller cap: a label
// claude lengthens is not a malformation, and a tighter budget would cut a
// legitimate one first while saving 0.x% of an envelope this list does not reach.
const maxModelDisplayName = 256

// maxModelEffortLevel caps ONE ELEMENT of turnevent.ModelOption.EffortLevels, not
// the list. maxModelResolved's paragraph applies verbatim for maxModelValue's
// reason — the separate-constant rule and the construction-time application are one
// argument covering all four fields, stated once there rather than transcribed a
// fourth time. A power of two, matching the family.
//
// MEASURED against the same capture: the longest level is 6 bytes (medium), so 32
// is roughly 5x the observation — a THINNER multiple than the three strings' 10x,
// for two reasons of which the second is the real one. A level is the most
// constrained shape in the family, a token from a menu claude publishes and a client
// renders as a control's options, not prose like DisplayName. And this cap is paid
// PER ELEMENT against a count claude chooses, so generosity here multiplies where
// the siblings' does not. 32 still admits every plausible future spelling —
// ultrathink is 10 bytes, extended-thinking 17 — which is the property that matters:
// a level claude adds later must DECODE, not arrive mangled.
//
// NOT sized to internal/relay's validEffort, whose CLOSED enum's longest member is
// the same 6 bytes. That enum bounds a PHONE-supplied override on an INBOUND path
// and is deliberately a different rule; sizing this cap to it would be applying the
// inbound rule outbound by the back door, and the level claude adds next is what
// both mistakes lose. See turnevent.ModelOption.EffortLevels, where the same
// separation is stated for the consumer.
//
// It bounds the ELEMENT and maxModelEffortLevelCount bounds the COUNT, which is what
// turns the multiplication sentence above from a warning into an arithmetic term:
// this cap is multiplied by a KNOWN factor now rather than by a number claude
// chooses, so raising it moves a per-entry budget a reader can compute instead of
// reopening one that cannot be computed at all. 32 * 8 = 256 bytes is that budget,
// and maxModelEffortLevelCount carries its derivation.
const maxModelEffortLevel = 32

// maxModelEffortLevelCount caps how many effort levels ONE
// turnevent.ModelOption retains — the COUNT maxModelEffortLevel's per-element cap
// cannot supply, and the last dimension of a model entry that was a function of a
// number claude chooses. It is the family's third CARDINALITY bound and the first
// that is per-ENTRY: maxTaskRosterEntries and maxModelListEntries bound a whole
// event's entries, this one bounds a list INSIDE one entry. Applied at CONSTRUCTION
// like every cap above, so an oversized payload never enters the event stream, the
// push queue, or any log. Overflow is REPORTED — turnevent.ModelOption.TruncatedFields
// names "effort_levels" — rather than silent, which is the property that makes a
// cardinality bound honest: a client shown three of claude's ten levels as a complete
// menu is a lie rather than a gap.
//
// THE NAME ENDS IN Count DELIBERATELY, and the plural maxModelEffortLevels was
// rejected rather than not considered. It would differ from maxModelEffortLevel by
// ONE character, both are int, and both bound the same field, so swapping them at a
// call site COMPILES and neither vet nor a type error would say so: an 8-byte element
// cap would cut `medium`, claude's ordinary output, on every child, and a 32-level
// count cap would bound nothing worth bounding. The Entries suffix the two other
// cardinality caps use is not available without maxModelEffortLevelEntries, which is
// worse. maxTaskPatch's separate-constant paragraph is this same instinct one step
// earlier — prevent a silent coupling before it can happen.
//
// The arithmetic, in maxTaskRosterEntries' style:
//
//   - The count itself: the observed list is FIVE levels — low, medium, high, xhigh,
//     max, in the committed capture (claude 2.1.239), identically in all three of
//     #1763's arms — so 8 is 1.6x the observation, essentially maxModelListEntries'
//     own 1.67x over six entries. A THINNER multiple than the text caps' 10x for
//     maxModelListEntries' stated reason: a cardinality overflow is REPORTED, and the
//     reported failure mode is what buys the thinner margin. Three slots above what
//     claude sends, so the level claude adds next is carried rather than cut.
//   - A power of two, matching every constant in this family except
//     maxModelListEntries, whose own doc explains why it alone is decimal.
//   - The LIST's budget: 8 * 32 = 256 bytes, exactly one string field's cap. That is
//     what puts maxModelResolved's per-entry multiplicand on 4 * 256 = 1024 bytes —
//     maxTaskRosterEntries' per-entry unit, to the byte — so the family has ONE entry
//     unit across both of its aggregate variants. Presented as the number both landed
//     on rather than as a rule the next variant must satisfy: the two entries have
//     different field sets (256 + 256 + 512 against 4 * 256), so the agreement is
//     arithmetic and a variant that re-derives its own unit is not violating anything.
//   - The aggregate is maxModelListEntries', which carries the product, the ceiling it
//     is measured against and why that ceiling. Cross-referenced rather than restated,
//     exactly as maxModelResolved does.
//
// NOT sized to the five levels claude sends, and NOT to internal/relay's validEffort
// whose closed enum has the same cardinality. A cap AT the observation fires the
// moment claude ships a sixth level, which is the failure maxModelListEntries' NOT 8
// paragraph rejects by name at its own scale — a cap firing on claude's ORDINARY
// output. The validEffort separation is maxModelEffortLevel's paragraph verbatim: a
// CLOSED enum bounding a PHONE-supplied override on an INBOUND path is deliberately a
// different rule, and sizing an outbound cap to it in either direction is applying the
// inbound rule by the back door.
//
// The cap is applied AFTER json.Unmarshal, so a hostile array is materialised in
// transient memory before it is shortened — maxTaskRosterEntries' accepted trade, with
// maxModelResolved's amplification paragraph bounding the exposure. This cap bounds
// what is RETAINED and what crosses the wire, which is the property that matters.
const maxModelEffortLevelCount = 8

// maxModelListEntries caps how many entries a turnevent.ModelList carries. It is
// the second application of maxTaskRosterEntries' doctrine and the constant that
// supplies the factor maxModelResolved's per-entry budget was missing: a per-entry
// text cap alone leaves a list's total size a function of a number claude chooses.
// Applied at CONSTRUCTION like every cap above, so an oversized payload never
// enters the event stream, the push queue, or any log. Overflow is REPORTED
// (turnevent.ModelList.DroppedModels), not silent — the one property that makes a
// cardinality bound honest, because a client showing six of claude's forty models
// as a complete menu is a lie rather than a gap.
//
// The number is DERIVED, and the derivation is written out because it is decimal
// rather than a power of two and a reader will ask why:
//
//   - Multiplicand: 1024 bytes per entry (maxModelResolved + maxModelValue +
//     maxModelDisplayName + maxModelEffortLevelCount * maxModelEffortLevel),
//     inherited from maxModelResolved's doc, which states it for this constant rather
//     than leaving it to be re-derived. All four terms are bounded by a constant of
//     the daemon's, so it is the per-entry TOTAL and not a floor on it.
//   - Ceiling: maxUnrecognizedRaw's whole-line 16 KiB, at 5/8 of it. That is the
//     package's ordering one level up — a whole KNOWN event must not approach the cap
//     on an entire UNKNOWN line — measured retained-against-retained, which is the
//     comparison the rule is about. 16384 * 5/8 = 10240, and 10240 / 1024 = 10 exactly.
//   - Floor: the observed list is SIX entries (the committed capture, claude
//     2.1.239), the check maxModelResolved's doc requires. 10 leaves four slots.
//   - Product: 10 * 1024 = 10240 bytes = 10 KiB, ten entries at the family's
//     one-kibibyte entry unit. 15.6% of the v2 application-envelope cap of 65519 bytes
//     (docs/protocol-mobile.md § Application-envelope size cap), which reads alongside
//     the family's 7.4%, 6.6% and 12.5%. Escaping is mild for maxUnrecognizedRaw's
//     reason verbatim: these are JSON string values, so the growth is quotes and
//     backslashes rather than a \u00XX expansion of every byte. Pathological all-quote
//     content roughly doubles it — ~20 KB, ~31% of the envelope. That doubling is
//     stated against the ENVELOPE only: the maxUnrecognizedRaw comparison above is on
//     RETAINED bytes, and mixing the two would measure a doubled wire figure against a
//     retained cap.
//
// THE CEILING MOVED AND THE PRODUCT GREW, which is written out because a reader
// re-running the arithmetic this doc used to carry will find it no longer closes. The
// old ceiling was 8192, and 8192 was never DERIVED as one: it is maxTaskRosterEntries'
// PRODUCT, whose doc NOTICED that 8 * 1024 lands on half of maxUnrecognizedRaw, and
// this constant inherited the noticed landmark as a constraint. What actually survives
// is the rule the roster stated, which fixes no particular fraction — so the fraction
// is stated PER SHAPE: the roster reads 1/2, this list reads 5/8. THE NEXT AGGREGATE
// VARIANT RE-DERIVES ITS OWN FRACTION rather than inheriting 5/8, because inheriting a
// noticed landmark as a constraint is exactly the mistake this paragraph undoes.
//
// It HAD to move, and the proof is short. Both factors have a doctrine floor. A level
// cap at claude's observed five fires on ORDINARY output, so maxModelEffortLevelCount
// is at least 6; an entry cap of 8 is rejected by name below, so this constant is at
// least 10. The smallest product consistent with both is 10 * (768 + 6 * 32) = 9600
// bytes, already above 8192. No pair of caps this family's own doctrine permits fits
// the inherited ceiling, so the ceiling was the only lever left rather than one of
// three. The two alternatives are closed where they live: a 10-byte per-level cap is
// what 8192 would need at ten entries and five levels, and maxModelEffortLevel argues
// 32 against spellings claude has not shipped (ultrathink is 10 bytes,
// extended-thinking 17); and RAISING maxUnrecognizedRaw would loosen the bound on an
// UNKNOWN line to make room for a KNOWN one, inverting the ordering rule it exists to
// state.
//
// WHICH NUMBERS MOVED: the ceiling, from 8192 to 5/8 of 16 KiB, and the multiplicand,
// from three strings to four terms. maxModelEffortLevel (32) did not — its own doc's
// argument is unchanged, and a thinner per-level cap is the alternative rejected
// above. This constant did not either: the NOT 8 paragraph rejects a smaller count on
// evidence that has not changed, and trading a bound on the dimension claude has never
// inflated (five levels of eight) for a tighter bound on the dimension claude is
// closest to (six entries of ten) is the wrong direction.
//
// NOT 8, borrowed from maxTaskRosterEntries by analogy. Two slots above an
// observation of six is not room, and the failure that buys is a cap firing on
// claude's ORDINARY output — the menu everyone sees, cut, on every child.
//
// NOT a power of two, unlike every other constant in this family, because neither
// neighbouring power fits: 8 is the too-tight case above, and 16 lands the product at
// 16384 bytes, EXACTLY maxUnrecognizedRaw's whole-line 16 KiB, so a KNOWN event would
// REACH the cap on an entire UNKNOWN line rather than staying below it. The decimal is
// what satisfies both binding constraints at once.
//
// The multiple over the observation is thinner than the text caps' 10x, and that is
// deliberate: a text cap's overflow mangles an identifier in place, while this one
// shortens a list and says BY HOW MANY, so a client can render "6 of 40" rather
// than a wrong menu. The reported failure mode is what buys the thinner margin.
//
// 1024 counts claude-derived text only. An entry can also carry up to four
// DAEMON-authored names in TruncatedFields (~55 bytes), which claude cannot inflate
// and which maxTaskRosterEntries' arithmetic likewise excludes.
//
// The cap is applied AFTER json.Unmarshal, so a hostile array is materialised in
// transient memory before it is shortened — maxTaskRosterEntries' accepted trade,
// with maxModelResolved's amplification paragraph bounding the exposure. This cap
// bounds what is RETAINED and what crosses the wire, which is the property that
// matters.
const maxModelListEntries = 10

// modelOptionLine is one element of that array, reduced to the five keys the
// mapping reads. The field set is exactly what turnevent.ModelOption carries and
// nothing invented: description, supportsEffort, supportsAdaptiveThinking and
// supportsFastMode are all absent by decision, not by oversight — see that type's
// doc. supportedEffortLevels is decoded HERE (#1827), which leaves no capability key
// claude sends unaccounted for.
//
// A plain []modelOptionLine on the container above, not a pointer to one, and no
// distinction is kept between an absent `models`, a null one and an empty array:
// emitModelAnnounced's formulation carries over verbatim — absent,
// present-but-empty, and a line carrying no such key all land in the same rung and
// are answered identically, which is what makes a plain decode target sufficient.
//
// The first three are plain strings, and so is every ELEMENT of EffortLevels,
// which is why truncateField's json.RawMessage exception does not reach this shape:
// encoding/json has already U+FFFD-replaced invalid input on decode, so our own cut
// is the only mid-rune hazard. A non-string value for any of the three — or a
// supportsAutoMode that is a string, a number, an object or an array, or a
// supportedEffortLevels that is a string, a number or an object, or a `models` that
// is a number, an object or a string, or a non-object `response` at either level —
// fails the WHOLE-LINE decode and takes emitModelList's undecodable rung, exactly as
// systemTaskUpdatedLine.TaskID does for a numeric task id. That type mismatch is
// the ONLY reachable failure there: consumeLine has already decoded this line into
// streamLine, so malformed JSON never reaches the function at all.
//
// A supportedEffortLevels ARRAY carrying a non-string element fails the same way,
// and it is named here rather than left to be inferred because it is the family's
// first ELEMENT-level mismatch and "the array decoded but one element was wrong" is
// the shape a reader would otherwise assume is tolerated. The decode is
// all-or-nothing at the LINE: there is no path that keeps an array's good elements
// and drops the bad one, which is the behaviour this field wants, since a menu that
// silently lost an element would be published as claude's complete one.
//
// JSON null is the one carve-out, and it has always applied to the three strings
// as much as to the bool: encoding/json documents unmarshalling a null into a
// non-pointer Go value as a NO-OP producing no error, so a null-valued key decodes
// cleanly and lands as the zero value rather than on the undecodable rung. For
// supportsAutoMode that is the same reading an absent key gets, which is what
// turnevent.ModelOption.SupportsAutoMode's doc argues is deliberate. For
// supportedEffortLevels it lands as nil, which is the same reading an absent key
// gets — and, since #1828, the same one a published [] gets too, the producer
// normalising that third shape onto this nil in emitModelList's boundEach. There is
// no branch in which a null reads as its own thing; the argument for the single
// reading is turnevent.ModelOption.EffortLevels'.
type modelOptionLine struct {
	ResolvedModel    string   `json:"resolvedModel"`
	Value            string   `json:"value"`
	DisplayName      string   `json:"displayName"`
	EffortLevels     []string `json:"supportedEffortLevels"`
	SupportsAutoMode bool     `json:"supportsAutoMode"`
}

// emitModelList decodes one top-level control_response line and emits AT MOST ONE
// turnevent.ModelList, from one rung, and AT MOST ONE turnevent.SlashCommandList, from
// either of two rungs. The two are decided INDEPENDENTLY under one shared subtype gate.
// It never emits an Unrecognized, and it returns nothing: consumeLine's case arm
// consumes the line by MATCHING, exactly as emitRateLimit's does, so unlike the
// emitSystemSubtype family there is no "did you handle it?" to report back. Field
// mapping comes from the committed capture, read in the tests through
// capturedInitializePayload, never from a hand-built payload.
//
// THE DISCRIMINANT is the substance of this mapping; the field copying is routine.
// claude answers three different requests on this one line type and only one of the
// replies carries a payload the daemon reads, so there is ONE SHARED PRECONDITION and
// then TWO INDEPENDENT DECISIONS. The precondition is
// subtype == controlResponseSuccess. Below it the two arrays are read as a LATTICE
// rather than a conjunction: a non-empty decoded models array decides the ModelList, a
// non-empty decoded commands array decides the SlashCommandList, and NEITHER GATES THE
// OTHER. It was a conjunction until #1891, when the commands-only corner stopped being
// a non-emitting one.
//
//   - The subtype half is what stops a payload being read out of a response that
//     reported FAILURE, and it is unchanged by the split into two decisions: it is
//     the precondition of BOTH emits. It is what makes the classification total as
//     well: every subtype that is not success — including an absent one — lands on
//     the nak rung rather than falling through to a shape test.
//   - The models half alone already excludes all three sibling shapes on record: a
//     set_permission_mode success (whose inner response is `{"mode":"default"}`), a
//     set_permission_mode NAK (which carries an `error` string and no inner
//     response), and an interrupt ack (which carries no inner response at all).
//     None of them has the key. That is a claim about which KEYS those shapes carry,
//     so it is scoped to the MODELS emit and does not transfer to the second one by
//     inheritance.
//   - The commands half needs the same exclusion ESTABLISHED, not borrowed, and it
//     holds: none of those three shapes carries a `commands` key either — the
//     set_permission_mode success has `mode` alone, the NAK has `error` and no inner
//     response, the interrupt ack has no inner response at all. So a non-empty
//     commands array excludes all three on record exactly as a non-empty models one
//     does, per key rather than by analogy.
//
// SO THE EMIT PARTITION IS NOT THE RUNG PARTITION: rungs 1-3 emit nothing, rung 4
// emits one SlashCommandList, and rung 5 emits one ModelList and, under its second
// gate, at most one SlashCommandList beside it. Exactly ONE rung can emit a ModelList;
// TWO can emit a SlashCommandList.
//
// IT RECOGNISES A SHAPE, NOT A CORRELATED REPLY, and the limit is written here so a
// later reader does not infer more than the code claims. This says "the line
// carries a success-subtype model list", not "this is the reply to the initialize
// request THIS daemon sent": claude authors the inner response object on every
// control response, so a future claude putting a models array inside some other ack
// would have that ack read as an inventory.
//
// WHERE A MIS-READ INVENTORY NOW GOES, written out because it used to go nowhere
// and that absence was once the whole bound. cmd/pyry's sessionModelHold retains it
// as the session's menu for the child's life (#1840),
// turnbridge.MapEvent's ModelList arm maps it onto protocol.ModelListPayload
// (#1848), and cmd/pyry's interactiveTurnEmitterV2.Handle emits the mapped frame to
// any interactive conn and appends it to the eventring (#1849). A menu the daemon
// never asked for would be presented to a client as one it did.
//
// THE TRADE STILL LANDS THE SAME WAY, and what decides it is the CONTENT bound
// rather than the audience. Correlating request_id would prove WHICH REPLY the
// bytes answered; it would not make the bytes more trustworthy, because the
// subprocess that could plant a models array in an unrelated ack is the same
// subprocess that authors the initialize reply — a correlated inventory is claude's
// own claim about itself exactly as an uncorrelated one is. That claim stays
// bounded whichever line carries it, and by the same three things: the caps above,
// nothing from the payload reaching a log (logControlResponse), and the render
// boundary the CLIENT owes (protocol.ModelOption's SECURITY paragraph). The
// alternative buys real provenance and costs new cross-object state:
// correlating it would mean retaining a minted id and comparing it here. This
// paragraph once deferred the question to whenever the value first reached a client;
// that HAPPENED, and the answer was re-taken here unchanged — recognise the shape,
// hold no cross-object parser state for provenance. No new trigger is set, because
// there is no later fact that would move the content bound.
//
// CORRECTED 2026-09-03 (#2064): the sentence above used to argue the point from
// mechanism — "nextControlID mints the id inline at the call site and nothing retains
// it, exactly as its two sibling writers discard theirs, so the parser holds no link
// to it". That is no longer true of the package. spawnAndWait hands its minted id to
// PostureGate.armedID, SetPermissionMode hands its id to retarget, and Parser holds a
// PostureGate — which is precisely a link from this parser to a retained id.
//
// THE CONCLUSION ABOVE IS UNCHANGED, and the distinction is what preserves it. That
// correlation is decided on its own decode target, controlAckLine, over the same
// top-level bytes, and it answers a DIFFERENT question: not "may this content be
// attributed to the daemon's initialize ask" but "did claude answer the specific
// request this daemon is holding turns for". controlAckLine declares subtype and
// request_id and NO payload key at all, so it cannot carry a models array into a
// decision, and it neither reads nor moves the rungs below. The content bound here is
// still the caps, the no-payload-to-a-log rule, and the client's render boundary —
// none of which a correlated id would tighten, for the reason the paragraph above
// gives: a correlated inventory is claude's own claim about itself exactly as an
// uncorrelated one is.
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field.
// streamLine's doc states the property it preserves: control shapes are read from
// the top level only and nested content is never re-scanned, which is what stops a
// tool result whose text is literally `{"type":"result"}` from forging a turn
// boundary. Decoding this payload from anywhere else would let claude's own tool
// output announce a model inventory the daemon never asked for — and, since #1853,
// a slash-command inventory too. commandEntryLine rides this same input; nothing
// reaches for a nested field to get at either array.
//
// FIVE RUNGS, total over the input, none of which can panic and none of which
// surfaces an Unrecognized:
//
//  1. The line will not decode into the shape — a `models` that is a number or an
//     object, a non-object `response` at either level — → undecodable, no event.
//  2. subtype is not success → nak, no event. The rung the arm's old doc named as
//     the accepted gap.
//  3. models absent, null, empty, or decoded empty, and NO commands either →
//     controlResponseAck, no event. This rung is the narrow one: it now means
//     "neither array", so its record is all-zero exactly as the two above it are.
//     Its suppression is NOT settled by the MODEL-LIST rung's naive reading. That
//     rung is named rather than called "the emitting rung", which since #1891 is two
//     of them and no longer picks one out.
//     emitRateLimit's rung 3 is the precedent, and its
//     argument carries over unchanged — a ModelList carrying zero entries names no
//     model, so it cannot serve the purpose the variant exists for, and emitting it
//     would be the daemon reporting an inventory it never observed. The safe failure
//     direction here is the false NEGATIVE, and the choice is made with BOTH
//     outcomes visible: a client on the live interactive lane reads the result
//     today (#1849). A false ZERO would reach that client's menu as "claude offers
//     no models". A false NEGATIVE shows no menu at all, or since #2124 the
//     DAEMON-WIDE one — cmd/pyry's sessionModelHold holds nothing, so
//     resolveBoundModelList never answers an empty list: it falls back to the
//     bootstrap child's retained vocabulary, and refuses only when nothing is
//     retained anywhere. Neither outcome weakens the footing, because the fallback
//     is drawn from the SAME binary and account and so is not a wrong menu either.
//     Both remaining outcomes are a MISSING or a BORROWED menu; only the false
//     positive is a WRONG one, and that asymmetry is the footing. It is stronger
//     now that either outcome is observable than it was when neither was.
//  4. success, the same empty-models reading as rung 3, and a NON-EMPTY commands
//     array → controlResponseCommandsOnly, and ONE SlashCommandList (#1891). It
//     differs from rung 3 in the keyword AND in the emit; what it shares with rung 3
//     is that the MODELS HALF IS READ IDENTICALLY, which is what still lets rung 3's
//     false-negative asymmetry be referenced rather than re-argued and keeps one copy
//     of it to correct. What the keyword split bought (#1890) is that the record names
//     the outcome instead of leaving the `commands` count to carry the distinction
//     alone; what this rung's emit rests on is emitSlashCommandList's own SUPPRESSION
//     RATHER THAN AN EMPTY EMIT paragraph, which travelled there with the construction,
//     declines to inherit rung 3's asymmetry and already covers this rung.
//  5. success and a non-empty models array → one ModelList. The `commands` array is
//     then read on this same rung under a SECOND, INDEPENDENT gate: non-empty → one
//     SlashCommandList beside it, absent / null / empty → the ModelList alone
//     (#1877). A non-empty `commands` with NO models does not reach here at all — it
//     is the controlResponseCommandsOnly rung above, which emits the same list from
//     its own discriminant. What this rung has that rung 4 does not is the ModelList.
//
// A per-entry field is never validated beyond its cap. An entry whose value is
// empty, whose resolvedModel is missing, or which carries no keys at all still
// becomes an entry: absence is claude's to choose, and emitBackgroundTaskStarted's
// doc is the standing answer — the field lands empty rather than inventing a
// validation rule. The capture's two four-key entries are the committed proof that
// a partial key set is claude's NORMAL output rather than a malformation. The gate
// that suppresses the event lives at the LIST level, not the entry level. That
// extends to the bool with one difference: what an absent supportsAutoMode READS AS
// is decided at turnevent.ModelOption's type rather than here, because the field's
// shape is the decision — there is no daemon code below implementing the collapse.
//
// The level LIST goes through TWO caps where each string goes through one —
// maxModelEffortLevel on every ELEMENT and maxModelEffortLevelCount on how many are
// RETAINED — and reports like neither: ONE name in TruncatedFields per entry whether
// that entry's levels were cut, its list was shortened, or both, because the report
// names FIELDS and a list is one field. What an absent
// supportedEffortLevels READS AS is likewise turnevent.ModelOption.EffortLevels' to
// say rather than this function's, and it SAYS that an absent key, a null and a
// published empty array are ONE reading, spelled nil (#1828). Unlike the bool that
// reading needs code, because encoding/json keeps the two shapes apart for free:
// boundEach's zero-length arm is where it lands, and it is the only normalisation
// anything below performs.
//
// NOTHING FROM THE PAYLOAD IS LOGGED, on any path — see logControlResponse, which
// is the one place that is decided.
func (p *Parser) emitModelList(line []byte) {
	var cr controlResponseLine
	if err := json.Unmarshal(line, &cr); err != nil {
		// The err is deliberately NOT logged, and emitModelAnnounced's undecodable arm
		// is where that rule is argued at length: encoding/json QUOTES the offending
		// input bytes into its error text, so `"err", err` would route claude's own
		// strings into the daemon log through a channel no per-attribute check can see.
		// #1853 made that strictly more load-bearing: with commandEntryLine declared,
		// the bytes a type error quotes are workspace-authored command names.
		p.logControlResponse(controlResponseUndecodable, 0, 0, 0, 0, 0)
		return
	}
	if cr.Response.Subtype != controlResponseSuccess {
		p.logControlResponse(controlResponseNAK, 0, 0, 0, 0, 0)
		return
	}
	// BELOW the success gate and ABOVE the empty-models block, and both halves are the
	// placement. Taking it above the subtype comparison would report a count off a
	// response that announced FAILURE. The lower bound binds HARDER since #1890 than
	// it did when this comment argued it from what the record would say: this int is
	// no longer only a number the record reports, it is the DISCRIMINANT between the
	// ack rung and the controlResponseCommandsOnly one. Taking it below the block
	// would not merely make a record read 0 — the branch could not be taken at all.
	// Since #1826 the block below is ALSO where the entry count is CUT, so the upper
	// bound binds for a third reason: a cut taken above the subtype comparison would
	// shorten an array off a response that announced FAILURE, on a path that emits
	// nothing at all.
	//
	// The COUNT bound runs HERE, above every rung, and maxSlashCommandListEntries' own
	// doc argues the placement: once rather than per rung, above the log call both
	// emitting rungs make before any emit, and above the emitter whose allocation it
	// bounds. Truncation is FROM THE TAIL — claude's order preserved, no ranking
	// invented, its ordering semantics being unobserved. The reslice shares the
	// decoder's backing array and that is deliberate; the constant's doc states why
	// this cut may do what boundAliases must not.
	commandEntries := cr.Response.Response.Commands
	var commandsDropped int
	if len(commandEntries) > maxSlashCommandListEntries {
		commandsDropped = len(commandEntries) - maxSlashCommandListEntries
		commandEntries = commandEntries[:maxSlashCommandListEntries]
	}
	// The EMITTED count since #1826, where it was the DECODED one — logControlResponse's
	// `models` and `dropped` are the pair this copies, and the two counts now mean the
	// same thing on both halves of the record. It is still the DISCRIMINANT between the
	// ack rung and the controlResponseCommandsOnly one, and the cap cannot disturb that:
	// being >= 1 it makes this int 0 exactly when the decoded length is 0.
	commands := len(commandEntries)
	entries := cr.Response.Response.Models
	if len(entries) == 0 {
		// An absent `models`, a null one, an empty array, and a response object
		// carrying no such key are ONE reading, and it is what both rungs below share;
		// the `commands` count is the only thing that separates them.
		if commands == 0 {
			// Rung 3, narrowed by #1890 to "neither array". The literal 0 rather than
			// `commands`: the guard proves the two identical, so no test can tell them
			// apart, and passing the literal says what this rung MEANS — an all-zero
			// record, exactly like the undecodable and nak rungs above it. The SIXTH
			// literal is the same choice for `commandsDropped` and rests on a proof one
			// step longer: the cap is >= 1, so a non-zero drop implies an emitted count of
			// at least the cap, which this guard has excluded. A test cannot tell the
			// literal from the variable here either.
			p.logControlResponse(controlResponseAck, 0, 0, 0, 0, 0)
			return
		}
		// Rung 4, and it EMITS since #1891: this rung was reached because the `commands`
		// array is non-empty, which is its own discriminant, so it reaches the emitter on
		// that array alone rather than behind a models list. The suppression is
		// emitSlashCommandList's PRECONDITION and is not repeated here — the same reason
		// rung 5's call is unconditional — and it cannot fire from this call site anyway,
		// the guard above having proved the slice non-empty. Its argument for suppressing
		// rather than emitting an empty list is that emitter's SUPPRESSION RATHER THAN AN
		// EMPTY EMIT paragraph, which covers this rung and is not restated here.
		//
		// The call sits BELOW logControlResponse and INSIDE this branch. Below the record
		// for the reason emitSlashCommandList's IT LOGS NOTHING paragraph rests on: the
		// seven attributes are written before anything below can run. Inside the branch
		// because falling through to the shared tail would run the models loop and put a
		// ModelList on a line that carries no models.
		//
		// It WIDENS which inputs reach the constructing-and-retaining path, which is
		// worth seeing at the site: before this, only a payload carrying BOTH arrays had
		// its WORKSPACE-authored command names copied into turnevent.SlashCommand values
		// and retained for the event's lifetime. The boundary itself does not move — one
		// decode target (commandEntryLine), one construction site, two caps
		// (maxSlashCommandName and, since #1904, maxSlashCommandDescription) — and the
		// argument passed is the same expression rung 5 passes, so there is no second
		// decode, loop or cap to keep in step. The cap COUNT grew with the field set while
		// the number of PLACES a cap is applied did not, which is what keeps this rung and
		// rung 5 in step without either one repeating the other.
		p.logControlResponse(controlResponseCommandsOnly, 0, 0, 0, commands, commandsDropped)
		p.emitSlashCommandList(commandEntries, commandsDropped)
		return
	}

	// ONE ROW PER FAMILY, reduced HERE, where claude's list is first read and
	// BEFORE the count bound below. Claude Code publishes its family rows (default,
	// opus, fable, sonnet, haiku) and then pinned rows (claude-opus-5,
	// claude-opus-4-7 and more); a pyry session follows the latest model of a
	// family, so modelfamily.Reduce drops every pinned row whose family row is
	// present and keeps only the newest pinned row of a family that has none.
	// Reducing first means the cap below counts families rather than pinned
	// versions, and every reader of this event (the retained holds, the saved
	// model_list.json, validation of an incoming pick, the Claude-only and merged
	// lists and the pushed updates) sees the same rows. It drops whole rows and
	// rewrites none, so #1600's verbatim rule still holds for every value that
	// survives. It reads the RAW value, before any text cap. It never empties a
	// non-empty list, so it cannot move a rung's classification.
	entries = modelfamily.Reduce(entries, func(e modelOptionLine) string { return e.Value })

	// Never nil and never empty: the rung above returned on both. The COUNT bound
	// runs before the loop, and truncation is FROM THE TAIL: claude's order is
	// preserved because no ranking is invented, its ordering semantics being
	// unobserved. It sits BELOW rung 3 and cannot move a rung's classification —
	// the cap is >= 1, so capping can neither create an empty list nor rescue one.
	var dropped int
	if len(entries) > maxModelListEntries {
		dropped = len(entries) - maxModelListEntries
		entries = entries[:maxModelListEntries]
	}

	models := make([]turnevent.ModelOption, 0, len(entries))
	// levelsDropped totals what the LEVEL-count bound cut across the RETAINED entries.
	// Entries the count cap above removed are already counted by `dropped`, and their
	// levels are never seen by the loop below, so nothing is counted twice.
	var levelsDropped int
	for _, entry := range entries {
		// The TEXT bound is per entry, so `cut` is per entry — which is the whole
		// reason this closure cannot be hoisted out of the loop.
		var cut []string
		// droppedLevels is per entry for `cut`'s reason, and its SCOPE is the whole of
		// what makes it correct: declared inside the loop, it cannot carry one entry's
		// drop onto the next, which is the mistake the "a cut on one entry does not
		// appear on the entries AFTER it" row exists to catch on the sibling path.
		var droppedLevels int
		bound := func(value, name string, limit int) string {
			out, truncated := truncateField(value, limit)
			if truncated {
				cut = append(cut, name)
			}
			return out
		}
		// boundEach is bound's sibling for the one field of this entry that is a LIST,
		// and it exists rather than a fourth bound call because a list is where ONE
		// name has to cover MANY values. Four properties, each load-bearing, in the
		// order the statements run:
		//
		//   - A ZERO-LENGTH input returns nil and appends nothing, so a published []
		//     reads as an absent key does. That is #1828's collapse implemented, and
		//     nil is the direction because it is this struct's own spelling for an
		//     empty list — see turnevent.ModelOption.TruncatedFields for the convention
		//     and ModelOption.EffortLevels for the argument. It normalises how Go
		//     spells ZERO and no element or position, so #1600's verbatim rule is
		//     untouched. The arm returns BEFORE everything below, so a zero-length list
		//     is neither counted against the cap nor named in TruncatedFields.
		//   - The COUNT bound then runs, and truncation is FROM THE TAIL for the
		//     entry-count cap's reason verbatim: claude's order is preserved because no
		//     ranking is invented, its ordering semantics being unobserved. Being >= 1
		//     it can neither create an empty list nor rescue one, so it cannot turn a
		//     non-empty list into the empty one whose reading #1828 settled, nor the
		//     other way about — the two mechanisms are independent by construction
		//     rather than by care. The > boundary matches truncateField's <=, and unlike
		//     maxModelListEntries' it is NOT an equivalent mutant: that block computes a
		//     count and a slice, both identity at len == cap, while this one also raises
		//     the report flag, so >= would name the field on a list nothing happened to.
		//   - Every SURVIVING element then goes through truncateField into a slice of
		//     the SAME length, so cutting an element neither drops it nor disturbs
		//     claude's order. That is the ELEMENT cap's property and not the closure's:
		//     the count bound above does shorten the list, which is why it reports.
		//   - The name is appended AT MOST ONCE, after the loop, and only if the list
		//     was shortened or some element was cut or both. Appending inside the loop
		//     would name the field once per cut level, which is the realistic mistake
		//     and the one the three-over-long row of TestParser_ModelListFieldsAreCapped
		//     exists to catch.
		//
		// The name is KEPT even though the closure now bounds the list's LENGTH too.
		// Bounding each element is still what it does per value, the count bound is one
		// statement before the loop, and the report is still one name; a rename would
		// buy no behaviour and would rot the by-symbol citations this function's own doc
		// and turnevent.ModelOption.EffortLevels make to it. The count constant is read
		// from package scope rather than taken as a second parameter beside `limit`: a
		// fourth int argument would put the two caps adjacent at the call site, which is
		// precisely the swap maxModelEffortLevelCount's naming paragraph spends itself
		// preventing, and unlike `bound` — three fields, three limits — this closure has
		// one call site and one field, so parameterizing buys nothing. It closes over
		// the same per-entry `cut` slice bound does, plus the per-entry droppedLevels
		// counter, and cannot be hoisted for the same reason.
		boundEach := func(values []string, name string, limit int) []string {
			if len(values) == 0 {
				return nil
			}
			var cutAny bool
			if len(values) > maxModelEffortLevelCount {
				droppedLevels = len(values) - maxModelEffortLevelCount
				values = values[:maxModelEffortLevelCount]
				cutAny = true
			}
			out := make([]string, len(values))
			for i, level := range values {
				var truncated bool
				out[i], truncated = truncateField(level, limit)
				cutAny = cutAny || truncated
			}
			if cutAny {
				cut = append(cut, name)
			}
			return out
		}
		// Sequential statements rather than a composite literal, for
		// emitBackgroundTaskStarted's reason: TruncatedFields is ordered by these
		// calls, and inside a literal that order would rest on the left-to-right
		// operand rule rather than on something a reader sees. The names are the
		// DAEMON's snake_case ones, not claude's camelCase keys.
		resolvedModel := bound(entry.ResolvedModel, "resolved_model", maxModelResolved)
		value := bound(entry.Value, "value", maxModelValue)
		displayName := bound(entry.DisplayName, "display_name", maxModelDisplayName)
		effortLevels := boundEach(entry.EffortLevels, "effort_levels", maxModelEffortLevel)
		// The per-entry drop joins the total HERE rather than inside the closure. A
		// counter the closure incremented directly would be one declared outside the
		// loop, and that is how a drop on one entry starts appearing on the next.
		levelsDropped += droppedLevels

		models = append(models, turnevent.ModelOption{
			// claude's values VERBATIM: no lowercasing, no alias expansion, no
			// date-stamping, no family mapping, no lookup against any published model
			// list (#1600's rule). The cap is the only judgement made about them here;
			// the family reduction above chose which rows survive and changed none.
			ResolvedModel: resolvedModel,
			Value:         value,
			DisplayName:   displayName,
			// Through `boundEach` rather than `bound`: the caps are per ELEMENT and per
			// COUNT while the report is per FIELD. claude's order survives both of them and
			// the list's cardinality survives an element CUT untouched — what changes the
			// cardinality is the count bound, which shortens FROM THE TAIL and says so in
			// TruncatedFields. #1600's verbatim rule covers the elements exactly as it covers
			// the three strings — no lowercasing and no canonicalisation into any effort
			// vocabulary of the daemon's own. internal/relay's validEffort is a CLOSED enum
			// on an INBOUND path and is deliberately not consulted here.
			EffortLevels: effortLevels,
			// Not through `bound`: a bool has no length to cut, carries none of claude's
			// bytes into the per-entry budget, and is therefore never named in
			// TruncatedFields. Absent, null and false arrive here already collapsed by
			// encoding/json — see turnevent.ModelOption.SupportsAutoMode for why that is
			// the intended reading rather than a distinction lost.
			SupportsAutoMode: entry.SupportsAutoMode,
			// nil when nothing was cut: append never ran.
			TruncatedFields: cut,
		})
	}

	p.logControlResponse(controlResponseModelList, len(models), dropped, levelsDropped, commands, commandsDropped)
	p.emit(turnevent.ModelList{Models: models, DroppedModels: dropped})

	// THE SECOND GATE, and it is INDEPENDENT of the models one: this rung was reached
	// because the models array is non-empty, and whether a SlashCommandList joins the
	// ModelList is decided on the `commands` array alone. The gate itself is
	// emitSlashCommandList's PRECONDITION rather than a guard written here, so a caller
	// inherits the suppression instead of repeating it and this call is unconditional.
	// The second call site that argument was written for EXISTS: rung 4 above calls the
	// same emitter with the same expression and likewise writes no guard of its own.
	//
	// THE ORDER IS DELIBERATE, and it is ONE RULE COVERING BOTH CALL SITES rather than
	// this rung's own arrangement: each rung emits its OWN DISCRIMINANT's event first,
	// then any independently gated one. Here the discriminant is the models array — the
	// rung exists because of it — so the ModelList goes first and the command inventory
	// follows; on rung 4 the discriminant is the `commands` array and the rule is
	// satisfied trivially, there being one event. On both rungs logControlResponse runs
	// before any emit. Gate order and statement order are then one order a reader checks
	// once, at two sites.
	//
	// The CALL sits BELOW logControlResponse, which is what makes "the record is
	// unchanged" structural rather than merely intended: seven attributes, the same
	// values and the same reason keyword, whatever happens below. emitSlashCommandList
	// logs nothing on any path, so that holds through the callee too.
	p.emitSlashCommandList(commandEntries, commandsDropped)
}
