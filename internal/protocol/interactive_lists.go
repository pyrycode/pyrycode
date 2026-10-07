package protocol

import (
	"encoding/json"
)

// ModelListPayload is the body of an Envelope whose Type == TypeModelList
// (docs/protocol-mobile.md § model_list — that section lands with the fixtures in
// #1705). Binary → phone direction; the wire form of the model inventory claude
// returns from a control_request with subtype initialize: the set of models it
// will accept for this conversation. A SNAPSHOT of what claude will accept, not a
// delta, and conversation-scoped rather than turn-scoped — receiving one neither
// opens nor closes a turn.
//
// Declared here (#1704) ahead of its producer so a client can be written against
// the shape — the sequencing #1405 used ahead of #1410 and #1616 ahead of #1638.
// The producer has since landed: turnbridge.MapEvent's turnevent.ModelList arm
// constructs it (#1848), and cmd/pyry's resolveBoundModelList (#1857) is a second
// production path that routes a session's retained list back through that same
// arm rather than filling the fields itself.
//
// ConversationID is present and unfilled by this ticket — the producer supplies
// it at mapping time, the seam every v2 interactive payload uses. claude's own
// session_id is deliberately absent for BackgroundTaskStartedPayload's reason:
// claude's session identity is not the daemon's conversation identity.
//
// Models is claude's list reduced to ONE ROW PER FAMILY, in claude's own order,
// truncated from the tail by the producer. Claude Code publishes its family rows
// (default, opus, fable, sonnet, haiku) followed by pinned rows (claude-opus-5,
// claude-opus-4-7 and more); a pyry session follows the latest model of a family,
// so the producer drops each pinned row whose family row is present and keeps only
// the newest pinned row of a family that has none (internal/modelfamily.Reduce).
// That runs where streamsup first reads the list, before the entry cap, so
// DroppedModels below counts rows cut from the reduced list. The key is always
// present on the wire and never null; see MarshalJSON.
//
// DroppedModels is how many entries the producer cut beyond its entry cap that
// this frame does NOT carry; 0 when nothing was dropped, so the list's true size
// is len(Models) + DroppedModels. The decode now COUNTS IT (#1812): streamsup's
// maxModelListEntries bounds the entry count and turnevent.ModelList.DroppedModels
// carries what it cut, which is this field's honest source. #1848 joined the two:
// MapEvent's arm carries turnevent.ModelList.DroppedModels through verbatim and
// never recomputes it from len(Models). The field was declared ahead of both
// (#1704) because a wire with nowhere to put a drop discards it silently, and a
// permanent 0 reads as "nothing was dropped", which is a lie rather than a gap.
// The count reports here rather than as a name in a top-level truncated_fields —
// which is why this payload has none, BackgroundTaskRosterPayload's stated
// reason — because a name-only report loses HOW MANY were lost, and each
// dimension reports where it is decided: a text cut is a property of one entry
// and rides that entry as ModelOption.TruncatedFields.
//
// A lookup can MISS, and that is ordinary rather than an error. claude announces
// an identifier at least as specific as the one it was given
// (turnevent.ModelAnnounced's own doc, measured in #1601), so a client resolving a
// model_announced identifier against this list may find nothing; this shape does
// not assume every announced identifier appears here. ModelOption.DisplayName is
// the intended join — the announcement names a concrete dated identifier while a
// client's rows are alias families.
type ModelListPayload struct {
	ConversationID string        `json:"conversation_id"`
	Models         []ModelOption `json:"models"`
	DroppedModels  int           `json:"dropped_models"`
}

// MarshalJSON normalises a nil Models to an empty array, so a model list always
// serialises as "models":[] and never as "models":null.
//
// This implements BackgroundTaskRosterPayload.MarshalJSON's reason unchanged, and
// that whole rationale transfers: omitempty is out because an empty list is a
// POSITIVE statement rather than an absence, and between null and [], [] reads as
// an empty list where null reads as absent/unknown, so a client decoding into a
// non-optional array type never has to branch. ModelOption.MarshalJSON normalises
// its own EffortLevels for a DIFFERENT reason — read it there, so the asymmetry
// is not taken for an accident — and deliberately leaves TruncatedFields alone.
//
// This method cannot do the entry's job for it. Assigning a fresh slice to this
// copy's own Models field is safe, but reaching THROUGH it into p.Models[i] would
// mutate the caller's backing array, which for a shared payload is a data race as
// well as a correctness bug; and a payload-level normalisation would not fire at
// all when a ModelOption is marshalled on its own.
//
// Value receiver, so it applies to the value form a round trip and a bridge both
// take, and so the substitution lands on a copy rather than on the caller's
// slice. The type alias is the standard indirection that keeps json.Marshal from
// recursing back into this method.
func (p ModelListPayload) MarshalJSON() ([]byte, error) {
	if p.Models == nil {
		p.Models = []ModelOption{}
	}
	type alias ModelListPayload
	return json.Marshal(alias(p))
}

// RequestModelListPayload is the body of an Envelope whose Type ==
// TypeRequestModelList (docs/protocol-mobile.md § model_list, published by #2125).
// The frame a client sends to ask for a conversation's model menu on demand,
// rather than waiting for the live turn lane or the next connect.
//
// ONE DIRECTION ONLY, phone → binary, so there is no provenance to disambiguate:
// EVERY FIELD IS AN UNVERIFIED CLAIM, ALWAYS. The frame it is answered with —
// ModelListPayload above — rides the other way and shares no type with it, so the
// never-empty and DroppedModels contracts stated there say nothing about this one.
//
// IT NAMES A CONVERSATION, because a model menu is conversation-scoped on this
// wire and this wire is multi-conversation; TypeRequestSessionSettings and
// TypeRequestHistory both name one. THE ID IS A LOOKUP KEY, NEVER A VALUE TRUSTED
// AS SENT: it is resolved against the daemon's own registry, the reported id in
// the reply comes out of the RESOLVED RECORD rather than being echoed back, and
// NAMING A CONVERSATION IS NOT AUTHORIZATION — authorization is pairing, enforced
// structurally at the Noise_IK handshake.
//
// UNLIKE RequestHistoryPayload THE ID NEVER BECOMES A PATH COMPONENT. That single
// difference is why a decode failure here is TOLERATED rather than rejected: it
// leaves ConversationID empty, which reaches only a registry membership check and
// is refused there, where an empty path component would have resolved to a
// directory root. Do not copy this tolerance to a verb that joins the id into a
// path.
//
// CORRELATION RIDES THE ENVELOPE'S InReplyTo, so the payload carries NO
// REQUEST-ID KEY — TypeAttachmentStored's decision, transferred unchanged.
// TestRequestModelListPayload_WireKeys pins the key set so this is checked rather
// than reviewed.
//
// NO omitempty AND NO MarshalJSON, matching RequestSessionSettingsPayload — its
// stated reason applies verbatim. There is no presence contract: absent and empty
// are the SAME case, "no conversation named", which names nothing and is refused,
// so nothing needs to tell them apart. Keeping the key always on the wire lets a
// fixture pin the full shape, and TestRequestModelListPayload_ZeroValue_KeyPresent
// reddens if an omitempty is added later for tidiness.
//
// SECURITY: SENDING THIS FRAME IS NOT A CAPABILITY, and neither is receiving an
// answer. There is no per-verb gate beyond the negotiated interactive capability,
// which is settled at handshake and cannot be influenced by anything in this
// payload. The id is loggable only AFTER it has been resolved against the
// registry — raw it is an arbitrary client string, and that is the log-injection
// shape § Attachments already forbids for a filename. The payload carries no
// count and no length, so there is nothing here a hostile value could size.
type RequestModelListPayload struct {
	// ConversationID names the conversation whose model menu is wanted. A lookup
	// key resolved against the daemon's registry, and not authorization; the empty
	// string names nothing and resolves nothing.
	ConversationID string `json:"conversation_id"`
}

// ModelOption is one row of a ModelListPayload (docs/protocol-mobile.md
// § model_list, #1704). Its fields are a subset of the per-entry keys claude's
// initialize reply carries and nothing invented. description and supportsFastMode
// are deliberately not carried — neither has a named consumer — and supportsEffort
// is subsumed by EffortLevels once the empty encoding below is decided. Adding a
// field later is cheap, and every entry multiplies against the 65519-byte v2
// application-envelope cap.
//
// ResolvedModel is what Value resolves to RIGHT NOW: the concrete identifier. It
// closes the question pyrycode-desktop#561 raised and could not — that ticket's
// shape is "send the family name, it resolves to the newest in the family", and
// it flags that the operator is then moved to a new model without choosing to.
// Publishing the resolution BEFORE the first turn is what lets a client show
// which model a family currently means, instead of inferring it from an
// announcement after the fact.
//
// Value is the argument you pass. After the producer's family reduction (see
// ModelListPayload.Models) it is a family name on every row of a family claude
// publishes one for: an alias (sonnet), a bracketed variant (opus[1m]), or
// default. Only a family claude publishes no family row for keeps one pinned id,
// its newest (claude-fable-5[1m] in a 2.1.239 capture). A client still cannot
// derive a family by splitting Value on "-"; a multi_agent client reads Family. A
// pinned id a client sends anyway is resolved to its family by the daemon before
// it is checked against this list or stored.
//
// Value round-trips, and a client author reading only this struct has to be told
// what shape does. The only inbound path that accepts a model is
// set_session_settings, gated by internal/relay's validModel, whose rule (widened
// at #1838 for exactly these rows) accepts "" or, within a 64-byte bound, a value
// whose first byte is alphanumeric, whose remaining bytes are in [A-Za-z0-9._-],
// and which may carry ONE trailing bracket group — non-empty, balanced, unnested,
// the value's final element, and drawn from that same closed byte class. Every
// value claude has been measured to publish satisfies it, the bracketed variant
// rows included. The group is bounded that way rather than by adding two bytes to
// the charset because the rule is #845's argv-injection defense: read validModel
// for what each clause buys and for the two sinks that depend on it.
//
// DisplayName is claude's human label, carried because it is the cleanest way to
// match a per-turn model_announced identifier to a client's alias-family row
// without a mapping table.
//
// EffortLevels are the reasoning-effort levels this model supports. The key is
// always present on the wire and never null — see MarshalJSON, and read its
// rationale, which is NOT ModelListPayload.MarshalJSON's. Measured 2026-08-22,
// all five levels claude returns (low, medium, high, xhigh, max) are accepted by
// internal/relay's validEffort, whose enum is CLOSED — so a level claude adds in
// future would be published here and refused inbound. Since #1838 widened
// validModel this is the ONLY field in the struct carrying that direction hazard;
// Value carried the same one until then, and validEffort is deliberately not
// widened alongside it because no level claude publishes is refused today.
//
// SupportsAutoMode is whether claude accepts auto permission mode for this model:
// claude refuses the request per model, so a client greys the option out when
// this is false (pyrycode-desktop#682). It collides with nothing in the daemon's
// own vocabulary — set_permission_mode carries default / acceptEdits /
// bypassPermissions / plan, and auto is claude's mode name, which the daemon does
// not currently send. Absent in claude's reply (Haiku's entry omits it) decodes to
// false, which is the correct reading.
//
// TruncatedFields names THIS row's cut fields, null when nothing was cut. The
// producer records FOUR names, in this order: "resolved_model", "value",
// "display_name", "effort_levels". "effort_levels" is the one reporting on a
// LIST rather than a value, and it covers three outcomes — an element cut to fit
// the per-element cap, the list shortened to fit the count cap, or both —
// appearing at most once per entry in every case, because the report names FIELDS
// and a list is one field. turnevent.ModelOption's TruncatedFields is the source
// of truth for that vocabulary. Deliberately NOT normalised the way EffortLevels is — see
// MarshalJSON. It is load-bearing rather than decoration: a row that dropped it
// would present claude's cut text to a phone as complete, and would offer back a
// Value the client was never told was truncated.
//
// SECURITY: ResolvedModel, Value, DisplayName and every string in EffortLevels are
// claude-authored strings that crossed the subprocess trust boundary. They are
// safe to RENDER as inert text and must never be fed to an HTML sink, an
// attribute, or a URL; the daemon bounds them but does not sanitize them — no
// control-character or terminal-escape stripping happens on this path — so they
// stay untrusted, model-influenced text all the way to the client, and the render
// boundary owing the sanitization is the CLIENT's. Their bound is the producer's,
// decided at construction, so this struct re-decides no maximum and declares no
// charset check: a second cap here would be a second place the limit is decided,
// and the two could disagree silently.
//
// The family's convention sentence — it is a REPORT, never a control input — needs
// one amendment here, because Value is the first field in the family a client is
// meant to send BACK. Publishing a value does not make it trusted: it is still
// claude's text arriving on an inbound path, and the daemon re-validates it at
// internal/relay's validModel rather than trusting that it came from a list the
// daemon itself published.
//
// Agent and Family are set only on the merged list a multi_agent client receives
// (#2651): Agent is AgentClaude or AgentCodex, and Family is a Codex entry's family
// name or the family alias of a Claude entry's Value, which is the Value itself on
// a family row. Both are omitempty, and that is what keeps
// every other client's frame byte-identical to the one it read before they existed:
// no producer on an older client's path sets them, so neither key reaches it.
type ModelOption struct {
	ResolvedModel    string   `json:"resolved_model"`
	Value            string   `json:"value"`
	DisplayName      string   `json:"display_name"`
	EffortLevels     []string `json:"effort_levels"`
	SupportsAutoMode bool     `json:"supports_auto_mode"`
	TruncatedFields  []string `json:"truncated_fields"`
	Agent            string   `json:"agent,omitempty"`
	Family           string   `json:"family,omitempty"`
}

// MarshalJSON normalises a nil EffortLevels to an empty array, so a row always
// serialises as "effort_levels":[] and never as "effort_levels":null. It
// deliberately leaves TruncatedFields alone, which stays null when nothing was
// cut.
//
// The reason is NOT ModelListPayload.MarshalJSON's, and the asymmetry between the
// two is not an accident. An empty effort list is not a positive statement here,
// it is a COLLAPSE: Haiku's entry omits supportedEffortLevels entirely, and a
// client's behaviour is identical for absent and empty (no effort control). The
// wire therefore states ONE position for both, and [] is the one that spares every
// row an optional-array branch. The daemon-internal value does NOT keep the
// absent/empty distinction either: turnevent.ModelOption.EffortLevels reads an
// absent key, a JSON null and a published empty array as ONE reading, spelled nil
// (#1828). The wire's position is stated here independently of that spelling,
// because an undeclared position is one #1848 would have had to invent — and, the
// two having landed on the same collapse, #1848's mapping needed no fork to
// bridge.
//
// TruncatedFields is exempt for BackgroundTaskRosterPayload.MarshalJSON's own
// carve-out reason, unchanged: nil and [] say the identical thing there ("nothing
// was cut") and no consumer branches on the difference.
//
// This method cannot be folded into the payload's: a payload marshaller
// normalising entries in place would mutate the caller's backing array unless the
// slice were copied first, and it would not fire at all when a ModelOption is
// marshalled on its own.
//
// Value receiver and the type alias, for ModelListPayload.MarshalJSON's reasons.
func (o ModelOption) MarshalJSON() ([]byte, error) {
	if o.EffortLevels == nil {
		o.EffortLevels = []string{}
	}
	type alias ModelOption
	return json.Marshal(alias(o))
}

// SlashCommandListPayload is the body of an Envelope whose Type ==
// TypeSlashCommandList (docs/protocol-mobile.md § slash_command_list). Binary →
// phone direction; the wire
// form of the slash-command inventory claude returns from a control_request with
// subtype initialize, alongside the models array ModelListPayload carries: the
// set of commands this session in this working directory will accept. A SNAPSHOT
// of what claude will accept, not a delta, and conversation-scoped rather than
// turn-scoped — receiving one neither opens nor closes a turn.
//
// Declared here (#1727) ahead of its producer so a client can be written against
// the shape — the sequencing #1405 used ahead of #1410, #1616 ahead of #1638 and
// #1704 ahead of #1848. THE PRODUCER HAS SINCE LANDED: turnbridge.MapEvent's
// turnevent.SlashCommandList arm constructs it (#2001), which is the sibling
// sequencing paid off — that arm is the one every later consumer maps through
// rather than forking. Two consumers are blocked on the shape today: pyrycode-desktop#681,
// the Actions-menu grey-out that matches its menu entries against this list, and
// pyrycode-desktop#694, a type-ahead that filters the whole list live and renders
// each row as a name, an argument hint and a description.
//
// ConversationID is present and unfilled by this ticket — the producer supplies
// it at mapping time, the seam every v2 interactive payload uses. claude's own
// session_id is deliberately absent for BackgroundTaskStartedPayload's reason:
// claude's session identity is not the daemon's conversation identity.
//
// Commands is in claude's own order. The key is always present on the wire and
// never null — see MarshalJSON.
//
// The COUNT is workspace- and version-dependent, and no client may assume one:
// 51 entries against claude 2.1.239 in this repository, while an earlier hand
// count against 2.1.220 in a different working directory reported 74. That
// variation is the feature's whole point, and it is why the list is per session
// and per working directory rather than a one-off global — so a client must not
// cache one list across working directories.
//
// The payload's cost lives in the descriptions: the capture's 51 entries
// serialise to 14,277 bytes of compact UTF-8 against the 65519-byte v2
// application-envelope cap. Comfortable but not free, so a cut has to be
// REPORTABLE rather than silent, which is what DroppedCommands and
// SlashCommand.TruncatedFields are for.
//
// DroppedCommands is how many entries the producer cut that this frame does NOT
// carry; 0 when nothing was dropped. AN ENTRY CAP NOW EXISTS AND SOMETHING NOW
// COUNTS IT: streamsup's maxSlashCommandListEntries bounds the decoded entry count
// and turnevent.SlashCommandList.DroppedCommands carries what it cut (#1826). What
// is still missing is a PATH TO A CLIENT, so read the state of this key precisely
// rather than by inference. SOMETHING NOW WRITES IT — turnbridge.MapEvent's arm
// takes turnevent.SlashCommandList.DroppedCommands as its BASE, never recomputing
// the count from len(Commands) (#2001), and ADDS whatever its own frame-size cut
// drops on top (#2002), so this field is the sum of two cuts where the event-side
// field is the producer's alone — but cmd/pyry's
// interactiveTurnEmitterV2.Handle still has no case, so no frame of this type is
// produced at all, which means every value a client could observe here today is
// STILL the zero one and there is still no frame on which len(Commands) +
// DroppedCommands is the menu's true size. It becomes that the moment #2003 lands
// the emission, and not before. It was declared ahead of all of this
// because a wire with nowhere to put a drop discards it silently, while a permanent
// 0 reads as "nothing was dropped", which is a lie rather than a gap. #1719 owned
// the decode and is CLOSED; #1826 owned the cap and the count and is closed too;
// #2001 is where the field and that counter met, and is also where the CAUSES were
// settled — the field says entries were cut without naming a cause, deliberately,
// so that a drop for a shape reason lands in the same count. The count reports
// here rather than as a name in a top-level truncated_fields — which is why this
// payload has none, BackgroundTaskRosterPayload's stated reason — because a
// name-only report loses HOW MANY were lost, and each dimension reports where it
// is decided: a text cut is a property of one entry and rides that entry as
// SlashCommand.TruncatedFields.
//
// The per-entry key set is COMPLETE, and that is measured rather than assumed.
// Against the committed capture
// internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json (claude
// 2.1.239), name, description, argumentHint and aliases are the entire per-entry
// vocabulary: 42 of the 51 entries carry the first three, the other 9 carry all
// four, and there is no third key set. This shape adopts ALL FOUR, so unlike
// ModelOption it drops nothing — which means a key a later claude adds arrives as
// a documented gap against a stated measurement rather than as a silent drop.
type SlashCommandListPayload struct {
	ConversationID  string         `json:"conversation_id"`
	Commands        []SlashCommand `json:"commands"`
	DroppedCommands int            `json:"dropped_commands"`
}

// MarshalJSON normalises a nil Commands to an empty array, so a slash-command
// list always serialises as "commands":[] and never as "commands":null.
//
// This implements BackgroundTaskRosterPayload.MarshalJSON's reason unchanged, and
// that whole rationale transfers: omitempty is out because an empty list is a
// POSITIVE statement — it says claude offered nothing — rather than an absence,
// and between null and [], [] reads as an empty list where null reads as
// absent/unknown, so a client decoding into a non-optional array type never has
// to branch. SlashCommand.MarshalJSON normalises its own Aliases for a DIFFERENT
// reason — read it there, so the asymmetry is not taken for an accident — and
// deliberately leaves TruncatedFields alone.
//
// This method cannot do the entry's job for it. Assigning a fresh slice to this
// copy's own Commands field is safe, but reaching THROUGH it into p.Commands[i]
// would mutate the caller's backing array, which for a payload shared between an
// emitter goroutine and a per-connection fan-out is a data race as well as a
// correctness bug; and a payload-level normalisation would not fire at all when a
// SlashCommand is marshalled on its own.
//
// Value receiver, so it applies to the value form a round trip and a bridge both
// take, and so the substitution lands on a copy rather than on the caller's
// slice. The type alias is the standard indirection that keeps json.Marshal from
// recursing back into this method.
func (p SlashCommandListPayload) MarshalJSON() ([]byte, error) {
	if p.Commands == nil {
		p.Commands = []SlashCommand{}
	}
	type alias SlashCommandListPayload
	return json.Marshal(alias(p))
}

// SlashCommand is one row of a SlashCommandListPayload (docs/protocol-mobile.md
// § slash_command_list, #1727). Its fields are exactly the four per-entry keys
// claude's initialize reply carries and nothing invented; the measurement behind
// "exactly" is in SlashCommandListPayload's doc.
//
// All three strings cross, not just the name. The Actions-menu grey-out needs
// names to match against, but the type-ahead renders all three: at 51 entries the
// description is what makes the list usable rather than a wall of names, and the
// argument hint is what tells the operator that a command takes something after
// it.
//
// Name is NOT an identifier. One name in the capture is __remote-workflow, so no
// charset assumption belongs in this struct or in a client.
//
// ArgumentHint is EMPTY on 33 of the capture's 51 entries, and no entry omits the
// key — so an empty hint is the ordinary case rather than missing data. That is
// why no string key here is optional: eliding it would make the common row
// indistinguishable from a malformed one.
//
// Description may contain NEWLINES — claude-api's does — and 0x0a is the ONLY
// sub-0x20 byte anywhere across the 51 entries' four string fields. Newlines are
// therefore the control characters on this path rather than one class among
// several, and a client rendering a single-line row must handle that specific
// case.
//
// Aliases is what makes the grey-out correct, and a shape dropping it would break
// the first consumer. The desktop Actions menu's own reset entry is an ALIAS of
// clear, not a command name, so a client matching against Name alone greys out a
// command that works. It is also why the cheap source cannot answer the question:
// the same capture's system/init line carries slash_commands, the identical 51
// names in the identical order as bare strings, and not one of the 11 aliases.
// The key is always present on the wire and never null — see MarshalJSON, and
// read its rationale, which is NOT SlashCommandListPayload.MarshalJSON's.
//
// TruncatedFields names THIS row's cut fields — "name", "argument_hint",
// "description", "aliases", the wire names rather than the Go ones, as
// RateLimitedPayload.TruncatedFields uses its JSON tags — and is null when
// nothing was cut. All four are enumerated because all four are strings the
// SECURITY paragraph credits and a producer may cut; #1720 reads this line to
// pick the names its producer emits, so an under-enumeration here would
// under-cover a real field. Deliberately NOT normalised the way Aliases is — see
// MarshalJSON. It is load-bearing rather than decoration: a row that dropped it
// would present claude's cut text to a phone as complete.
//
// SECURITY: Name, ArgumentHint, Description and every string in Aliases are
// WORKSPACE-authored strings that crossed the subprocess trust boundary. That
// strengthens ModelOption's claude-authored warning rather than restating it: a
// command defined in a repository was written by whoever wrote that repository,
// which is a lower-trust origin than claude. They are safe to RENDER as inert
// text and must never be fed to an HTML sink, an attribute, or a URL; the daemon
// bounds them but does not sanitize them — no control-character or
// terminal-escape stripping happens on this path — so they stay untrusted text
// all the way to the client, and the render boundary owing the sanitization is
// the CLIENT's. Their bound is the producer's (#1719/#1720), decided at
// construction, so this struct re-decides no maximum and declares no charset
// check: a second cap here would be a second place the limit is decided, and the
// two could disagree silently.
//
// The family's convention sentence — it is a REPORT, never a control input —
// needs the amendment ModelOption.Value carries, for a different reason: a client
// is meant to send a Name BACK, as the text of an ordinary message, because
// sending the slash command IS the feature. Publishing a name does not make it
// trusted. It arrives inbound as ordinary message text, on a path that does not
// treat it as a command vocabulary and does not consult this list, and no field
// here reaches a child as an argv element. This frame declares no inbound verb —
// TypeSlashCommandList's own doc has that reasoning — and grants nothing.
type SlashCommand struct {
	Name            string   `json:"name"`
	ArgumentHint    string   `json:"argument_hint"`
	Description     string   `json:"description"`
	Aliases         []string `json:"aliases"`
	TruncatedFields []string `json:"truncated_fields"`
}

// MarshalJSON normalises a nil Aliases to an empty array, so a row always
// serialises as "aliases":[] and never as "aliases":null. It deliberately leaves
// TruncatedFields alone, which stays null when nothing was cut.
//
// The reason is NOT SlashCommandListPayload.MarshalJSON's, and the asymmetry
// between the two is not an accident. An empty alias list is not a positive
// statement here, it is a COLLAPSE — and that is measured rather than argued.
// claude never sends "aliases": []: zero of the capture's 51 entries carry an
// empty array, 42 omit the key entirely and 9 carry a non-empty one. An entry
// with no aliases and an entry with the key absent are the same statement, and a
// client must not have to branch on absent-vs-empty to match an alias, so the
// wire states ONE position for both and [] is the position that spares every row
// an optional-array branch. This is exactly ModelOption.MarshalJSON's
// EffortLevels collapse with the frequency INVERTED: the majority case here, the
// single exception (Haiku) there.
//
// THE DAEMON-INTERNAL VALUE COLLAPSES TOO, and #1825 settled it — the question
// this comment used to hand forward. turnevent.SlashCommand.Aliases reads an
// absent key, a JSON null and a published empty array as ONE reading, spelled
// nil, and owns the argument for it. It reached that answer the way
// turnevent.ModelOption.EffortLevels reached its own (#1828), by WEIGHING that
// precedent rather than inheriting it: the two lists' frequencies are inverted,
// so what carried was not the levels' conclusion but the observation that the
// frequency changes how often a collapse fires and not what either shape MEANS.
// So the two sides of this boundary agree, which is what makes this method's
// normalisation a one-shape job rather than a two-shape reconciliation. The
// wire's position would have been stated here either way, because an undeclared
// position is one #1720 would have to invent.
//
// TruncatedFields is exempt for BackgroundTaskRosterPayload.MarshalJSON's own
// carve-out reason, unchanged: nil and [] say the identical thing there ("nothing
// was cut") and no consumer branches on the difference.
//
// This method cannot be folded into the payload's: a payload marshaller
// normalising entries in place would mutate the caller's backing array unless the
// slice were copied first, and it would not fire at all when a SlashCommand is
// marshalled on its own.
//
// Value receiver and the type alias, for SlashCommandListPayload.MarshalJSON's
// reasons.
func (c SlashCommand) MarshalJSON() ([]byte, error) {
	if c.Aliases == nil {
		c.Aliases = []string{}
	}
	type alias SlashCommand
	return json.Marshal(alias(c))
}
