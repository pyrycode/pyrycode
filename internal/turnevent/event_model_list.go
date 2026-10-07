package turnevent

// ModelOption is one entry of a ModelList: the list's element type, NOT an
// Event, so it carries no marker — BackgroundTask's shape, for BackgroundTask's
// reason. Its five claude-authored fields — three strings, one string LIST and one
// bool — are exactly what protocol.ModelOption carries (#1704), in that type's
// declaration order, with nothing missing and nothing invented.
//
// Four per-entry keys the captured reply also carries are deliberately absent, and
// the ABSENCE is the guarantee: description — the longest string in the capture at
// 66 bytes, and the most tempting to carry — supportsFastMode, which only one of
// the six entries has, and supportsEffort and supportsAdaptiveThinking, which the
// four richer entries carry beside the keys this type does decode. None has a named
// consumer and protocol.ModelOption carries none of them at all, so decoding any
// would be untrusted prose bounded, retained and carried for nothing. That is the
// argument streamsup's systemInitLine makes about its own twenty-one omissions,
// and a field never declared on the producer's decode target cannot leak whatever
// a later sweep forgets to check. Of the two capability keys the richer entries do
// carry, BOTH are decoded here — supportsAutoMode by #1819 and supportedEffortLevels
// by #1827 — so neither is an omission. supportsEffort stays out with the other
// three: EffortLevels subsumes it completely, now that a zero-length effort menu has
// ONE reading (#1828). A nil EffortLevels says exactly what supportsEffort: false or
// an absent supportsEffort would say, so the key would add a second spelling of an
// answer this type already carries. The capture's six entries show the two co-varying
// perfectly — supportsEffort: true with five levels, both absent together — which is
// evidence for the subsumption rather than a guarantee of it; claude could publish
// them apart tomorrow and the omission would still hold, because nothing consumes
// supportsEffort.
type ModelOption struct {
	// ResolvedModel is what Value resolves to RIGHT NOW: the concrete identifier,
	// and the field a consumer wanting a dated one wants. VERBATIM, per
	// ModelAnnounced.Model's rule — no lowercasing, no alias expansion, no
	// date-stamping, no family mapping, and no lookup against any published model
	// list.
	//
	// It need not be dated and need not appear in any published list: the capture's
	// six entries resolve to claude-sonnet-5, claude-opus-5, claude-fable-5 and
	// claude-haiku-4-5-20251001, only the last of which carries a date.
	ResolvedModel string
	// Value is the argument you PASS to select this model, and it is NOT a dated
	// identifier: an alias (sonnet), a bracketed variant (claude-fable-5[1m]), or
	// default. A consumer cannot derive a family by splitting it on "-", and cannot
	// assume it round-trips — see protocol.ModelOption.Value for the inbound gap and
	// for why internal/relay's validModel is not to be widened to close it.
	Value string
	// DisplayName is claude's human LABEL for the entry ("Default (recommended)",
	// "Haiku 4.5"). PROSE, not an identifier: safe to RENDER as inert text, never a
	// key to match on. The daemon bounds it and does not sanitize it — no
	// control-character or terminal-escape stripping happens on this path — so it
	// stays untrusted, model-influenced text and the render boundary owing the
	// sanitization is the CLIENT's, exactly as protocol.ModelOption's SECURITY
	// paragraph states for the same three strings.
	DisplayName string
	// EffortLevels are the reasoning-effort levels claude published for THIS model,
	// so a client's effort control can offer exactly the levels claude accepts.
	// VERBATIM, per ModelAnnounced.Model's rule: claude's own strings in claude's own
	// order, with no lowercasing, no canonicalisation into any effort vocabulary of
	// the daemon's own, and no reordering — the capture's order (low, medium, high,
	// xhigh, max) is neither alphabetical nor sorted, and preserving it is what
	// carries claude's answer rather than the daemon's opinion of it.
	//
	// BOTH THE ELEMENT AND THE COUNT ARE BOUNDED. The producer caps every level string
	// AT CONSTRUCTION (streamsup's maxModelEffortLevel) and how MANY levels this list
	// retains (streamsup's maxModelEffortLevelCount), so neither an oversized level nor
	// an inflated menu enters the event stream, a queue, or a log. The count bound is
	// what a per-element cap alone cannot supply: the array's length is claude's to
	// choose, so without it a per-entry size stayed a function of a number claude
	// picks. See ModelList.Models, which states all three of the list's dimensions
	// together.
	//
	// AN ABSENT KEY, A JSON null AND A PUBLISHED EMPTY ARRAY ARE ONE READING, AND IT
	// IS SPELLED nil (#1828). The producer normalises a zero-length list at
	// construction — streamsup's emitModelList, in its boundEach closure's
	// zero-length arm — so nothing downstream has to ask which of the three it is
	// holding. That was a real fork rather than a shape Go forced: a nil slice and an
	// empty non-nil one are distinguishable at no cost, and encoding/json lands an
	// absent key and a null on the first and a [] on the second. Keeping them apart
	// was available and was deliberately not taken.
	//
	// THE ARGUMENT IS ABOUT AN EMPTY MENU, AND IT IS NOT SupportsAutoMode'S. That
	// field collapses because both of its readings drive the same client CONTROL and
	// the safe direction is asymmetric — the unsafe inverse being to grant on silence.
	// This one collapses because both readings leave the daemon holding the same EMPTY
	// HAND. The only reading under which absent and [] could differ is "absent means
	// UNKNOWN, [] means AFFIRMATIVELY NONE", and UNKNOWN is actionable only if there is
	// a fallback menu to offer instead. There is none: the daemon's one effort
	// vocabulary is internal/relay's validEffort, which the paragraph below forbids
	// applying to this list in either direction, for two separate reasons. So the two
	// readings issue the identical instruction to every consumer that can exist — THIS
	// IS NOT A MENU YOU MAY OFFER — not because they mean the same thing in the
	// abstract, but because the daemon holds no vocabulary in which they could differ
	// and has twice decided it never will.
	//
	// Three facts close it. First, claude's observed absence is not "declined to answer
	// this one question": in the committed capture the two entries omitting
	// supportedEffortLevels omit the ENTIRE capability block with it — no
	// supportsAutoMode, no supportsEffort, no supportsAdaptiveThinking — while the four
	// answering any capability question answer all of them and publish the full five
	// levels. The shape a genuine per-question refusal would take is exactly the shape
	// claude does not send. Second, a kept distinction is one NO CLIENT COULD EVER
	// OBSERVE, however long the daemon held it. It is not short-lived: cmd/pyry's
	// sessionModelHold keeps this value for the session's life (#1840), and
	// turnbridge.MapEvent's ModelList arm crosses this field as the slice it is, nil
	// left nil (#1848), so the distinction even survives the mapping. It dies at the
	// WIRE, because every path to a client ends at protocol.ModelOption.MarshalJSON,
	// which normalises nil to [] and states at its own type that an empty effort list
	// on the wire is a COLLAPSE rather than a positive statement (#1704). The two
	// sides having landed on the same collapse independently is why #1848's mapping
	// needed no fork to bridge them. Third, a kept distinction is one this house's own
	// comparison idiom cannot see: slices.Equal(nil, []string{}) reports TRUE where
	// reflect.DeepEqual reports false, so the next assertion written against this field
	// with the idiom every existing one uses would drop the distinction silently, with
	// nothing going red. That is a trap, and it is a fact about SLICES rather than
	// anything inherited from the bool.
	//
	// nil rather than []string{} because nil is this struct's own spelling for a
	// zero-length list — TruncatedFields below names the convention and its single
	// source. The other direction would leave two list fields of one struct disagreeing
	// about how "nothing" is spelled, and would allocate on a shape a third of the
	// capture's entries have.
	//
	// IT NORMALISES HOW GO SPELLS ZERO, AND NOTHING ELSE. The verbatim rule above
	// governs the ELEMENTS and their ORDER, and a list with no elements has exactly the
	// elements claude sent, in exactly claude's order. Said rather than left to be
	// noticed, because this field's doc opens by calling it VERBATIM and the collapse
	// would otherwise read as the first exception to that.
	//
	// WHAT IT GIVES UP is real and is accepted rather than waved away: the decode now
	// discards the evidence that claude sent [] rather than nothing at all. Should
	// claude one day send [] deliberately AND mean by it something that omitting the key
	// does not, this reading is wrong, and reopening it costs a ticket plus a *[]string
	// or a companion bool. That is the price of one reading, paid knowingly.
	//
	// A CUT LEVEL IS NOT A LEVEL CLAUDE PUBLISHED, AND A LEVEL CLAUDE PUBLISHED MAY BE
	// MISSING ENTIRELY. When TruncatedFields names "effort_levels", at least one
	// element is the daemon's prefix of a string claude sent, or the count bound
	// shortened the list from the tail, or both. Either way this list is no longer
	// claude's menu and must not be offered as one. The instruction is the same for
	// both, which is why one name covers them: a list that is not claude's whole
	// published menu is unofferable whether one level was mangled or ninety were
	// dropped.
	//
	// WHAT THAT GIVES UP, stated rather than waved away: the TRUE level count is not
	// recoverable from this event, where ModelList's true entry count is recoverable as
	// len(Models) + DroppedModels. A per-entry dropped-level integer is what would
	// recover it, and protocol.ModelOption has no field to carry one, so it would be
	// preserved only long enough for the mapping (#1848) to discard it — the argument
	// SupportsAutoMode makes against a *bool, one field over. Reopening it costs a
	// ticket plus a wire field. The daemon's own operational signal for the bound is
	// streamsup's control_response record, not this event.
	//
	// internal/relay's validEffort is NOT applied to this list, and is not to be
	// widened or narrowed to match it. It is a CLOSED enum bounding a phone-supplied
	// override on an INBOUND path: running claude's outbound list through it would
	// silently drop a level claude adds next, making the daemon's menu a lie, and
	// widening it to whatever claude published would let the subprocess extend what an
	// untrusted inbound frame may set. The two rules look interchangeable and are
	// deliberately not, which is why the separation is stated rather than left to be
	// noticed.
	//
	// BOUNDED AND UTF-8-VALID IS ALL THEY ARE, exactly as DisplayName is: these are
	// claude-authored strings that crossed the subprocess trust boundary, and nothing
	// on this path strips control characters or terminal escape sequences, so they
	// stay untrusted, model-influenced text and the render boundary owing the
	// sanitization is the CLIENT's — which is what protocol.ModelOption's SECURITY
	// paragraph already states for these same strings.
	EffortLevels []string
	// SupportsAutoMode is claude's own answer to whether it accepts AUTO permission
	// mode for this model, so a client's permission-mode menu can grey the option out
	// where claude refuses it. VERBATIM, per ModelAnnounced.Model's rule: the key's
	// value as claude sent it, with no daemon policy folded in and no inference from
	// any other field of the entry.
	//
	// AN ABSENT KEY, A JSON null AND AN EXPLICIT false ARE ONE READING — false. The
	// collapse is deliberate rather than fallen into: in Go the choice IS the field's
	// shape, and a *bool is the answer that keeps them apart. What decides it is that
	// the safe direction here is asymmetric and points at false. This field describes
	// a permission GRANT, so "claude refused auto for this model" and "claude said
	// nothing about auto for this model" drive the SAME client behaviour — grey the
	// option out — and no decision hangs between them. The unsafe collapse is the
	// inverse, granting on silence, which a claude that merely stopped sending the
	// key would walk into; nothing here does that.
	//
	// Three facts support it. protocol.ModelOption.SupportsAutoMode is already a
	// plain bool whose doc calls absent-decodes-to-false "the correct reading"
	// (#1704), so a pointer here would preserve a distinction only long enough for
	// the mapping (#1848) to discard it. claude has never sent false at all — in the
	// committed capture four entries carry true and two carry no capability key
	// whatsoever, identically in all three of #1763's arms — so a pointer would
	// defend a shape observed nowhere. And this file declares no pointer field of
	// this kind today; a field with no consumer asking for the third state is not
	// where the event vocabulary grows its first one.
	//
	// Never named in TruncatedFields, because a bool is never cut: it has no length,
	// truncateField never sees it, and it carries none of claude's bytes into any
	// per-entry budget.
	//
	// It is a REPORT to a client's menu and NEVER an authorization input. Nothing in
	// the daemon may branch on it to decide what it may SEND claude — claude decides
	// that when asked — because a daemon reading its own subprocess's claim as
	// permission is a subprocess authorizing itself.
	//
	// EffortLevels faced the same absent-versus-empty question, RE-DERIVED its answer
	// rather than inheriting this one, and landed on a collapse too — BY A DIFFERENT
	// ARGUMENT, which is why the coincidence must not be read as this paragraph having
	// set a precedent. The bool collapses because the safe direction is ASYMMETRIC:
	// silence and refusal drive the same control, and the unsafe inverse is granting on
	// silence. The list collapses because both of its readings leave the daemon holding
	// the same EMPTY HAND, there being no effort vocabulary of its own to fall back on
	// and a standing decision never to author one. A withheld grant is settled by asking
	// which direction is safe; an empty menu is settled by asking what there is to
	// offer. See EffortLevels for that argument in full (#1828).
	SupportsAutoMode bool
	// TruncatedFields names THIS entry's fields the producer cut to fit their caps,
	// in declaration order, using the DAEMON's snake_case names: "resolved_model",
	// "value", "display_name", "effort_levels". No name is translated — claude's
	// camelCase keys and these fields agree on which field they mean. nil when
	// nothing was cut, never an empty non-nil slice; BackgroundTask.TruncatedFields
	// is the convention's single source.
	//
	// "effort_levels" is the one name reporting on a LIST rather than a value, and it
	// covers three outcomes: one or more of that entry's levels were cut to fit the
	// per-element cap, the list was shortened to fit the count cap, or both. It appears
	// at most once per entry in every case, because this report names FIELDS and a list
	// is one field. It is last for the declaration-order reason and no other — the
	// producer's list bound runs after the three strings'.
	TruncatedFields []string
}

// ModelList is claude's inventory of selectable models: the models array of the
// initialize reply (#1811), which the daemon solicits once per child with
// streamsup's WriteInitialize and claude answers on a control_response line.
//
// It exists because it is the only thing that answers WHAT CAN BE RUN, and it
// answers it BEFORE the first turn — where ModelAnnounced reports what one turn
// got, this reports the whole set a client may choose from, with each alias's
// current resolution alongside it.
//
// An Event rather than parser-held session state, and the deciding fact is
// protocol.ModelListPayload's own doc: its ConversationID is supplied at MAPPING
// time, and mapping time is turnbridge.MapEvent, whose input is an Event. Session
// state would have obliged the publishing slice to build a second parser→relay path
// beside the one every other interactive payload already uses. It did not: #1849
// publishes through cmd/pyry's interactiveTurnEmitterV2.Handle, the same emitter
// every other interactive payload goes through, so the prediction held.
//
// IT IS PUBLISHED ON ONE DELIVERY PATH OF TWO, and the two have different answers,
// so a claim about where this value goes has to say which one it means.
//
// THE LIVE LANE, SHIPPED. turnbridge.MapEvent's ModelList arm maps this variant
// onto protocol.ModelListPayload, DroppedModels included (#1848), and cmd/pyry's
// interactiveTurnEmitterV2.Handle emits the mapped frame on the interactive turn
// lane (#1849); #1845 proves it reaches a connected client end to end. It is
// BEST-EFFORT rather than guaranteed: cmd/pyry's turnMarkFor answers turnMarkNone,
// so the fan-in classes the event droppable and can refuse it at droppableCap under
// load — so a client on that lane receives it as a property of the PATH, not a
// guarantee about any one exchange. Two holders retain it. cmd/pyry's
// sessionModelHold keeps this decoded value for the session's life, sitting ABOVE
// that droppable send (#1840); and #1849's emit appends the MAPPED payload to the
// eventring, retaining it per conversation as the replay source for a phone that
// reconnects.
//
// CONNECT-TIME DELIVERY, NOT SHIPPED. A client that connects AFTER the initialize
// exchange receives this today by no path at all. The eventring does not close
// that: replay is a RECONNECT mechanism driven by a last_event_id the client must
// already hold, and a client never connected for the exchange has none to
// advertise. #1863 landed the relay-side connect seam — internal/relay's
// V2SessionConfig.RetainedModelLists, drained by reconcileModelLists — but nothing
// in the tree fills it, and #1867 is the outstanding slice that will.
//
// A client can therefore read this producer's output, which is why the producer
// takes the false NEGATIVE on every ambiguous line rather than emitting a list it
// did not observe: a missing menu beats a wrong one, and that choice matters more
// now that either outcome is visible than it did when neither was.
//
// It opens and closes no turn, exactly as the background-task variants do not, and
// it is not even per-turn: one initialize exchange per child produces one of
// these. cmd/pyry's turnMarkFor answers it correctly by construction — its opener
// set is a whitelist and its default is turnMarkNone.
//
// claude's session_id is deliberately not a field, for BackgroundTaskStarted's
// reason: claude's session identity is NOT the daemon's conversation identity, and
// like every variant here this one carries no conversation identity at all — the
// bridge injects that.
type ModelList struct {
	// Models is the inventory in claude's own order, reduced to one row per family:
	// the producer drops each pinned row whose family row is present and keeps only
	// the newest pinned row of a family that has none (internal/modelfamily.Reduce,
	// applied before the entry cap below), because a pyry session follows the
	// latest model of a family. No ranking is invented and no surviving row is
	// changed. Never empty: the producer's gate does not emit on an empty array,
	// because a ModelList naming no model cannot serve the purpose this variant
	// exists for, and the reduction never empties a list.
	//
	// Each entry's three strings are bounded by the producer AT CONSTRUCTION
	// (streamsup's maxModelResolved / maxModelValue / maxModelDisplayName), so an
	// oversized payload never enters the event stream, a queue, or a log. So is the
	// entry COUNT (streamsup's maxModelListEntries), which is what a per-entry text
	// cap alone cannot supply: the array's length is claude's to choose, and a
	// per-entry cap alone would leave the total a function of that number. The list is
	// truncated FROM THE TAIL when the count cap fires, and the true size stays
	// recoverable as len(Models) + DroppedModels.
	//
	// THERE ARE THREE DIMENSIONS AND ALL THREE ARE BOUNDED. The per-entry TEXT by the
	// three string caps above and by streamsup's maxModelEffortLevel on each level,
	// reported per entry in ModelOption.TruncatedFields; the ENTRY count by streamsup's
	// maxModelListEntries, reported here as DroppedModels; and the per-entry LEVEL
	// count by streamsup's maxModelEffortLevelCount, reported on the entry it happened
	// to, as "effort_levels" in that entry's TruncatedFields. Each dimension reports at
	// the level where it happens, which is why the level count reports per entry and
	// the entry count reports on the list. The bool beside those strings is bounded by
	// nothing and needs no cap: it carries none of claude's bytes.
	Models []ModelOption
	// DroppedModels is how many entries claude sent beyond the producer's cap that
	// this event does NOT carry; 0 when nothing was dropped. The reduced list's true
	// size is len(Models) + DroppedModels; pinned rows the family reduction removed
	// are not counted, since they were removed by rule rather than cut for size.
	//
	// The count dimension reports HERE rather than in a top-level TruncatedFields
	// naming "models", and that is why this variant has no top-level
	// TruncatedFields at all — BackgroundTaskRoster.DroppedTasks' stated reason,
	// unchanged: a name-only report loses how many were lost, and the count is the
	// strictly more informative signal. Each dimension reports at the level where it
	// happens — a text cut is a property of one entry and rides that entry as
	// ModelOption.TruncatedFields.
	//
	// It is the first field on this variant that is DAEMON-derived rather than
	// claude-derived: an int computed from a slice length, carrying none of claude's
	// bytes.
	DroppedModels int
}
