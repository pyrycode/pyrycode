package turnevent

// SlashCommand is one entry of a SlashCommandList: the list's element type, NOT
// an Event, so it carries no marker — BackgroundTask's and ModelOption's shape,
// for their reason.
//
// Its five fields are ALL FIVE of protocol.SlashCommand's (#1727), in that
// type's own declaration order: the command's name, its argument hint, its
// description, its aliases, and the report naming which of this entry's fields
// the producer cut. The set is COMPLETE as of #1825 — the vocabulary grew a
// field at a time across #1877 / #1904 / #1957 / #1825, exactly as ModelOption
// grew across #1819 / #1827 / #1828, and there is no further field promised.
//
// FIXING THE ORDER BEFORE THE SECOND FIELD EXISTS is the whole point of choosing
// it now, and Description (#1904) is the first evidence that the promise was
// kept: it landed BETWEEN Name and TruncatedFields — its mirrored position,
// leaving the gap ArgumentHint fills later — rather than being appended to the
// end where a field added without this rule would have gone. ArgumentHint
// (#1957) is the second, and it is the case the promise was actually WRITTEN
// for: Description had one declared field to land after, so no reordering could
// have got it wrong, where this field had to be INSERTED BETWEEN two that
// already existed and appending it would have compiled just as well. Aliases
// (#1825) is the third and last, and it pays the promise off at the END of the
// order rather than by insertion: it is the only one of the three whose
// mirrored slot sits after every field declared before it, so appending it was
// CORRECT here where appending ArgumentHint would have been wrong — the rule
// earns its keep by making that a checked fact rather than a coincidence.
// The mapping onto the wire type is now a field-for-field copy across the whole
// struct rather than a reordering a reader has to check.
type SlashCommand struct {
	// Name is claude's command name, VERBATIM, per ModelAnnounced.Model's rule:
	// no lowercasing, no canonicalisation, no prefix stripping, and no leading
	// "/" added or removed.
	//
	// IT IS NOT AN IDENTIFIER. One name in the committed capture is
	// __remote-workflow, so no charset assumption belongs in this struct or in a
	// consumer — protocol.SlashCommand's measured fact, carried to the daemon side
	// because this is the type a daemon-side consumer reads.
	//
	// Bounded by the producer AT CONSTRUCTION and never sanitized: see
	// SlashCommandList's SECURITY paragraph, which owns that statement for every
	// string on this type rather than having it diluted into a restatement here.
	Name string
	// ArgumentHint is claude's synopsis of what the command takes AFTER its
	// name, VERBATIM, per ModelAnnounced.Model's rule: no lowercasing, no
	// trimming, no charset filtering.
	//
	// AN EMPTY HINT IS THE ORDINARY CASE, NOT MISSING DATA, and a consumer must
	// not read one as absent or as a defect: 33 of the committed capture's 51
	// entries carry "" and NONE omits the key, so a command taking no argument
	// is the majority row. That measurement is also why
	// protocol.SlashCommand's wire tag carries no omitempty — eliding an empty
	// hint would make the common row indistinguishable from a malformed one —
	// and this doc does not re-derive it.
	//
	// IT IS A SYNOPSIS, NOT AN INVOCABLE TOKEN, which is what separates it from
	// Name rather than making it a second copy of Name's warning. A client is
	// meant to send a Name back as ordinary message text, sending the slash
	// command being the feature; this string is never sent back at all. It is
	// no more an identifier than Name is, and less: 13 of the capture's 18
	// non-empty hints carry `[` and `]` and ten carry `<` and `>`, so a
	// consumer rendering one is handling syntax-shaped text and not a slug.
	//
	// Bounded by the producer AT CONSTRUCTION under its OWN cap (streamsup's
	// maxSlashCommandArgumentHint, not Name's or Description's) and never
	// sanitized: see SlashCommandList's SECURITY paragraph, which owns that
	// statement for every string on this type.
	ArgumentHint string
	// Description is claude's own description of the command, VERBATIM, per
	// ModelAnnounced.Model's rule: no lowercasing, no trimming, no charset
	// filtering — and NO NEWLINE STRIPPING, which is named rather than left under
	// "verbatim" because this is the one string on this type a captured value
	// actually carries newlines in. protocol.SlashCommand's doc carries that
	// measurement and its narrowness, and this doc does not re-derive either.
	//
	// IT IS PROSE, NOT A LABEL, which is what separates it from Name rather than
	// making it a second copy of Name's warning: 14 of the capture's 51
	// descriptions carry non-ASCII where no name does, so a consumer rendering it
	// as a single-line row is handling multi-line, non-ASCII text and not an
	// identifier. It is also the field a truncation is least recoverable from —
	// nothing else on this lane carries a second copy of it — which is the trade
	// streamsup's maxSlashCommandDescription argues.
	//
	// Bounded by the producer AT CONSTRUCTION under its OWN cap
	// (streamsup's maxSlashCommandDescription, not Name's) and never sanitized:
	// see SlashCommandList's SECURITY paragraph, which owns that statement for
	// every string on this type.
	Description string
	// Aliases are claude's alternative names for the command, VERBATIM and in
	// claude's own order, per ModelAnnounced.Model's rule: no lowercasing, no
	// trimming, no charset filtering, no leading "/" added or removed, no
	// deduplication and no sorting.
	//
	// AN ALIAS IS NOT AN ENTRY. #1600's verbatim rule forbids expanding one into a
	// synthetic command of its own: the committed capture's `clear` carries `reset`
	// and `new`, and a producer that turned those into two more rows would be
	// inventing commands claude never published. They belong to the entry that
	// declares them and are matched against it.
	//
	// THEY ARE WHAT MAKES A CONSUMER'S MATCH CORRECT, which is the whole reason the
	// field exists rather than a nicety: the desktop Actions menu's own reset entry
	// is an alias of clear and not a command name, so a consumer matching its menu
	// against Name alone finds nothing for it and greys out a command that works.
	//
	// AN ABSENT KEY, A JSON null AND A PUBLISHED EMPTY ARRAY ARE ONE READING, spelled
	// nil, and the collapse is deliberate rather than an artefact of how Go decodes.
	// It is DECIDED HERE because protocol.SlashCommand.MarshalJSON asks this type to
	// decide it (#1825). The measurement behind it is that the distinction has never
	// been observed: ZERO of the committed capture's 51 entries carry an empty array,
	// 42 omit the key and 9 carry a non-empty one, so what a kept distinction would
	// separate is one observed shape from one claude has never sent. It also could
	// not survive the trip — protocol.SlashCommand.MarshalJSON publishes [] for both,
	// deliberately, so a client never has to branch on absent-versus-empty to match
	// an alias — and a daemon-internal difference erased one hop downstream is a
	// difference no consumer can act on.
	//
	// It is ModelOption.EffortLevels' collapse WEIGHED rather than inherited, with
	// the frequencies INVERTED: absence is the single exception there and the
	// majority here. What the frequency changes is how often the collapse fires, not
	// what either shape MEANS — an entry with no aliases and an entry with the key
	// absent issue a consumer the identical instruction, that there is no alternative
	// spelling to match against, and no behaviour branches on which claude meant.
	//
	// nil rather than []string{} for TruncatedFields' reason: nil is this struct's
	// own spelling for an empty list, and two list fields disagreeing on how to spell
	// empty is worse than picking a direction once. The trap that makes the choice
	// worth stating: slices.Equal(nil, []string{}) reports TRUE, so a design keeping
	// the distinction would carry a difference invisible to the comparison idiom
	// every assertion on such a field uses.
	//
	// Bounded by the producer AT CONSTRUCTION in TWO dimensions under TWO caps of
	// their own — streamsup's maxSlashCommandAlias on each string and
	// maxSlashCommandAliasCount on how many this slice retains — and never sanitized:
	// see SlashCommandList's SECURITY paragraph, which owns that statement for every
	// string on this type. It is the only field here a cut can SHORTEN THE LIST of
	// rather than only shorten a value of, and a cut on either dimension reports
	// once under "aliases".
	Aliases []string
	// TruncatedFields names THIS entry's fields the producer cut to fit their
	// caps, in declaration order, using the DAEMON's snake_case names. They agree
	// with protocol.SlashCommand.TruncatedFields' wire names, so a later mapping
	// is a copy rather than a translation. nil when nothing was cut, never an
	// empty non-nil slice; BackgroundTask.TruncatedFields is the convention's
	// single source.
	//
	// IT CAN CARRY "name", "argument_hint", "description" AND "aliases", IN THAT
	// ORDER, AND THE ENUMERATION GREW WITH THE FIELD SET UNTIL IT WAS COMPLETE.
	// #1904 was the first slice to extend it, #1957 the second and #1825 the last,
	// exactly as ModelOption.TruncatedFields grew to include "effort_levels" in
	// #1827 — and it is what turned the declaration ORDER above into a claim a test
	// can see, an enumeration of one having nothing to order.
	//
	// "argument_hint" is the only name here that is NOT byte-identical to claude's
	// own key for the field, which is argumentHint. These are the daemon's names, as
	// the paragraph above says; the other three coincide with claude's key and with
	// protocol.SlashCommand's wire name, and until that field landed the distinction
	// had no difference to see.
	//
	// "aliases" IS THE ONE NAME HERE THAT CAN MEAN TWO DIFFERENT CUTS — a string in
	// the list shortened, or the list itself shortened — and it says the same thing
	// either way and at most once per entry. A consumer reading it learns that the
	// alias set it holds is incomplete, which is the actionable fact for both; WHICH
	// dimension fired is not recoverable from this slice, exactly as
	// ModelOption.TruncatedFields' "effort_levels" does not distinguish its own two.
	//
	// A NAME FOR A FIELD THIS TYPE DOES NOT DECLARE MUST NEVER APPEAR. A producer
	// that cut a value this type does not carry has nothing to report here, because
	// the value is not on the type. That is the one way a partial field set could
	// produce a lie: a report telling a consumer that text it holds is incomplete,
	// when the type never held that text at all. The rule stands with an EMPTY
	// EXTENSION since #1825 — the type now declares every per-entry key claude sends,
	// so there is no field left for a producer to report and not carry — and it is
	// kept rather than retired because what it forbids is a producer inventing a
	// name, which no field count makes impossible.
	TruncatedFields []string
}

// SlashCommandList is claude's inventory of slash commands for this session in
// this working directory: the commands array of the initialize reply (#1854),
// the same exchange ModelList carries the models array of, which the daemon
// solicits once per child with streamsup's WriteInitialize and claude answers on
// a control_response line.
//
// It exists because it is the only thing that answers WHAT CAN BE INVOKED, and
// it answers it per WORKING DIRECTORY rather than globally: a command defined in
// a repository exists for that repository's sessions and nowhere else.
//
// DECLARED AHEAD OF ITS PRODUCER, which is this family's own sequencing — #1616
// ahead of #1638, #1704 ahead of #1848, #1727 ahead of #1720. The type
// declaration is where the field set and the security posture get decided, and
// deciding those in the same slice that also writes the decode is what makes such
// a slice oversized.
//
// THE PRODUCER HAS SINCE ARRIVED, and the work is split four ways — worth naming
// because the attributions are the easy thing to get wrong here.
// internal/streamsup's commandEntryLine holds the DECODE (#1853, in the tree, and
// COMPLETE since #1825 declared the fourth and last per-entry key); its
// emitSlashCommandList applies the PER-FIELD CAPS, constructs the entries and EMITS
// this list (#1877, in the tree — #1886 moved that construction out of emitModelList
// into an emitter of its own, so more than one call site reaches it); the ENTRY-COUNT
// BOUND, and the drop count that arrives with it, is #1826's and is IN THE TREE —
// maxSlashCommandListEntries, cut in emitModelList above both rungs that read the
// array, reported here as DroppedCommands; and the PUBLISH, which #1720 owned as one
// piece and which was SPLIT — turnbridge.MapEvent's arm is #2001 and is IN THE TREE,
// mapping this variant onto protocol.SlashCommandListPayload, with the FRAME-LEVEL
// byte bound that arm deliberately omitted added by #2002, also in the tree, which is
// why the wire's DroppedCommands is this event's count plus that cut rather than this
// count carried; cmd/pyry's interactiveTurnEmitterV2.Handle case is #2003 and is still
// open.
// #1719 is CLOSED and was the decode, so it names no future producer. What remains of
// the four is the EMISSION alone, and it is the only piece that was ever a WIRE change
// — the mapping is a daemon-internal translation onto a shape already declared.
//
// EVERY DIMENSION IS NOW BOUNDED and there is no unbounded one left to name. The
// per-entry TEXT by streamsup's four field caps, reported per entry in
// SlashCommand.TruncatedFields; the per-entry ALIAS count by
// maxSlashCommandAliasCount, reported on the entry it happened to; and the ENTRY
// count by maxSlashCommandListEntries, reported here as DroppedCommands — which is
// the factor a per-field cap alone cannot supply, exactly as maxModelListEntries
// supplied it for ModelList. What that does NOT give is a bound the WIRE can rely
// on, and that one is no longer missing either: turnbridge's
// maxSlashCommandListBytes (#2002) cuts the mapped list by MEASURED bytes where it
// reaches the wire. DroppedCommands' own doc states how the two cuts compose and
// why the count on this event is not the count on the wire.
//
// IT IS PUBLISHED BY NO PATH TODAY, AND THE REASON IS NOW THE HANDLE CASE ALONE.
// turnbridge.MapEvent grew its arm for this variant in #2001, so MapEvent's default
// no longer drops it — but cmd/pyry's interactiveTurnEmitterV2.Handle still has no
// case, so an event of this variant is logged by kind and discarded before anything
// reaches that arm. The mapping is therefore reachable only by a caller that hands
// MapEvent this variant explicitly, which is what #2003's Handle case and #2005's
// connect-time resolver will each be. ModelList is NOT the example of a variant the
// default drops any more — it grew its own MapEvent arm in #1848 and its own Handle
// case in #1849 — and this variant is now the example of one MAPPED but not yet
// EMITTED, a state the family had not previously had; both precedents have to be
// read off MapEvent and Handle themselves rather than inherited from this family's
// earlier tickets.
//
// It opens and closes no turn, exactly as the background-task variants do not,
// and it is not even per-turn: one initialize exchange per child produces one of
// these. cmd/pyry's turnMarkFor answers it correctly by construction — its opener
// set is a whitelist and its default is turnMarkNone.
//
// THE COUNT IS WORKSPACE- AND VERSION-DEPENDENT and no consumer may assume one;
// protocol.SlashCommandListPayload carries the measurement rather than this doc
// re-deriving it. That variation is why the list is per session and per working
// directory, and why a client must not cache one across working directories.
//
// THE DroppedCommands FIELD ARRIVED WITH ITS BOUND (#1826), one slice after the
// wire type declared its own, and the sequencing was the decision rather than an
// accident of it. protocol.SlashCommandListPayload declared its count AHEAD of any
// counter because a WIRE with nowhere to put a drop discards it silently, and
// adding a key later is a compatibility event. A daemon-internal struct is not a
// compatibility surface: adding a field to it is a local change, so this one waited
// for the ENTRY-COUNT BOUND that produces it — which is how ModelList.DroppedModels
// arrived with streamsup's maxModelListEntries and BackgroundTaskRoster.DroppedTasks
// with maxTaskRosterEntries. The asymmetry a reader who knew the wire type would
// once have read as an oversight is CLOSED, and so is the mapping between the two
// fields: turnbridge.MapEvent's arm carries this count onto
// protocol.SlashCommandListPayload.DroppedCommands verbatim (#2001), never
// recomputed from len(Commands) and never zeroed.
//
// claude's session_id is deliberately not a field, for BackgroundTaskStarted's
// reason: claude's session identity is NOT the daemon's conversation identity,
// and like every variant here this one carries no conversation identity at all —
// the bridge injects that.
//
// SECURITY: every string this type carries is WORKSPACE-AUTHORED and crossed the
// subprocess trust boundary. That STRENGTHENS ModelOption's claude-authored
// warning rather than restating it: a command defined in a repository was written
// by whoever wrote that repository, which is a LOWER-trust origin than claude's
// own strings. They are safe to RENDER as inert text and must never be fed to an
// HTML sink, an attribute, or a URL; the daemon bounds them but does not sanitize
// them — no control-character or terminal-escape stripping happens on this path —
// so they stay untrusted text all the way out, and the render boundary owing the
// sanitization is the CLIENT's.
//
// THE BOUND IS THE PRODUCER'S and is not decided here, so this type declares no
// maximum and no charset check. A second cap would be a second place the limit is
// decided and the two could disagree silently — protocol.SlashCommand's own
// stated reason, holding identically one layer in.
//
// IT IS A REPORT, NEVER A CONTROL INPUT, with protocol.SlashCommand's amendment:
// a client is meant to send a Name BACK, as the text of an ordinary message,
// because sending the slash command IS the feature. Publishing a name does not
// make it trusted. Nothing in the daemon may treat a value from this type as a
// command vocabulary, and no field here may reach a child as an argv element.
type SlashCommandList struct {
	// Commands is the inventory in claude's own order, unchanged: no ranking is
	// invented, its ordering semantics being unobserved. nil for a zero-length
	// list, never an empty non-nil slice; BackgroundTask.TruncatedFields is the
	// convention's single source.
	//
	// WHETHER AN EMPTY LIST IS EMITTED AT ALL IS THE PRODUCER'S GATE and is still
	// not decided here. It has been ANSWERED, one slice later than ModelList.Models
	// answered its own — #1811 declared that type and wrote its producer together,
	// where this one was declared first: streamsup's emitSlashCommandList SUPPRESSES
	// the empty list (#1877), so nothing reaches this field with zero entries. The
	// WIRE's position is what that producer slice READ, and it did not govern:
	// protocol.SlashCommandListPayload.MarshalJSON states that [] is a POSITIVE
	// statement, that claude offered nothing, but the decode collapses an absent
	// `commands`, a null one and a published [] onto one nil slice, so the producer
	// cannot tell that from "claude said nothing about commands" and will not
	// assert it. That position still governs how a list that WAS emitted
	// serialises, which is what #1720 reads it for.
	//
	// The entry COUNT is bounded too since #1826 (streamsup's
	// maxSlashCommandListEntries), which is what a per-entry text cap alone cannot
	// supply: the array's length is claude's — really the WORKSPACE's — to choose,
	// so a per-field cap alone leaves the total a function of a number the daemon
	// does not control. The list is truncated FROM THE TAIL when that cap fires,
	// and the true size stays recoverable as len(Commands) + DroppedCommands.
	Commands []SlashCommand
	// DroppedCommands is how many entries the producer cut beyond its entry cap
	// that this event does NOT carry; 0 when nothing was dropped. The list's true
	// size is len(Commands) + DroppedCommands.
	//
	// The count dimension reports HERE rather than in a top-level TruncatedFields
	// naming "commands", and that is why this variant has no top-level
	// TruncatedFields at all — BackgroundTaskRoster.DroppedTasks' stated reason,
	// unchanged: a name-only report loses how many were lost, and the count is the
	// strictly more informative signal. Each dimension reports at the level where
	// it happens — a text cut and an alias-list cut are both properties of ONE
	// entry and ride that entry as SlashCommand.TruncatedFields.
	//
	// It is the only field on this variant that is DAEMON-derived rather than
	// workspace-derived: an int computed from a slice length, carrying none of the
	// workspace's bytes. That is what makes it the one field here a consumer may
	// trust without the render boundary this type's SECURITY paragraph demands of
	// every other one.
	//
	// WHAT IT DOES NOT BOUND is worth stating where a reader will look for it: the
	// count cap makes the retained size a function of a daemon constant instead of
	// a workspace's, but 128 entries at the 1280-byte per-entry term is 163,840 raw
	// bytes, well past the 65519-byte v2 application-envelope cap, and Go's escaping
	// puts the wire worst case higher still. No count cap can close that gap —
	// maxSlashCommandListEntries carries the arithmetic.
	//
	// THE FRAME-LEVEL BOUND IS turnbridge's maxSlashCommandListBytes (#2002), a
	// MEASURED byte cut applied where this list is mapped onto the wire. It does not
	// change what THIS field counts: the number here is still the producer's cut
	// alone, entries streamsup dropped past its entry cap, because that is the only
	// cut that has happened by the time this event exists. The mapping ADDS its own
	// drops to protocol.SlashCommandListPayload.DroppedCommands rather than
	// replacing this one, so len(Commands) + DroppedCommands is this event's true
	// size here and the WIRE field is the true size after both cuts. A reader taking
	// the two fields for the same number will misattribute a frame cut to the
	// producer.
	DroppedCommands int
}
