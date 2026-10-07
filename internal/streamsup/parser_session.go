package streamsup

import (
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/transcript"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// maxModelField caps turnevent.ModelAnnounced's Model — claude's announced model
// identifier, off the system/init line. Applied at CONSTRUCTION, exactly as the
// caps above are, so an oversized value never enters the event stream, the push
// queue, or any log.
//
// MEASURED, not chosen. Three observations across two claude versions and three
// spawn shapes: claude-haiku-4-5-20251001 (25 bytes, the committed capture, where
// claude DATED the bare `haiku` alias it was spawned with),
// claude-haiku-4-5 (16 bytes, the permission_protocol_* captures, echoed
// unchanged), claude-sonnet-5 (15 bytes, #1582's recorded run, the machine default
// echoed unchanged). 256 is roughly 10x the observed maximum — near maxTaskFieldID's
// 9x over its 29-byte observation, and for the same reason: room for a naming
// scheme claude has not shipped yet, and still a hard cut on anything that has
// stopped being an identifier.
//
// A separate constant even though it currently equals maxTaskFieldID and
// maxRateLimitField: maxRateLimitField's paragraph applies verbatim — they bound
// different fields for different reasons, and folding them into one would make a
// future change to the task-id budget silently move this one.
//
// Deliberately NOT validModel's 64 (internal/relay/v2session_settings.go), and the
// distinction is the point rather than an oversight. That validator bounds a
// phone-supplied OVERRIDE the daemon accepts, and it enforces a charset besides.
// This cap bounds what claude ANNOUNCES, which the daemon neither controls nor may
// reject: unifying them would make a claude that echoes a longer identifier look
// like a malformed client request.
//
// The envelope arithmetic, in maxUnrecognizedRaw's style: worst case one
// ModelAnnounced carries 256 bytes of claude-derived text, 0.4% of the v2
// application-envelope cap of 65519 bytes (docs/protocol-mobile.md §
// Application-envelope size cap) — half RateLimited's 0.8% and the smallest
// contribution in the family. Escaping is mild for maxUnrecognizedRaw's reason.
// Amplification from input to retained bytes is near zero: systemInitLine holds one
// scalar and no array, so a 4 MiB line (defaultMaxParseBuf) yields at most 256
// retained bytes plus one bool.
//
// No RATE bound, and none is owed: init fires once per TURN, below the ~1-2 per
// turn minThinkingTokensPerEvent's gate already accepts for ThinkingProgress. The
// event is not a droppable delta (the droppable set is assistant_delta only, #610),
// so it holds a queue slot under the same existing backpressure the five sibling
// variants do. Revisit on an OBSERVED rate, as #1385 did.
const maxModelField = 256

// maxClaudeVersionField caps turnevent.SessionFacts's ClaudeCodeVersion — claude's
// own build, taken from the SAME system/init line maxModelField's field comes from
// (#2252). Applied at construction like every cap in this block, so an oversized
// value never enters the event stream, a queue, or any log.
//
// A separate constant even though it equals maxModelField and the three below it:
// maxRateLimitField's paragraph applies verbatim, and it applies hardest right here
// because this constant and its neighbour bound two keys of ONE line. Folding them
// into maxModelField would make a future change to the MODEL identifier's budget —
// the field with by far the most measured variety — silently move the budget for a
// version string and a posture keyword that have shown almost none.
//
// MEASURED, not chosen. Two observations across two committed captures: 2.1.220
// (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json) and 2.1.259
// (#2251's effort capture, all three of its init lines), both 7 bytes. 256 is ~36x
// the observed maximum, wider in ratio than maxModelField's ~10x and deliberately
// so: a version string is the field most likely to grow a suffix nobody predicted —
// a channel name, a build hash, a date — and unlike a model identifier it has no
// naming scheme the daemon has ever seen vary. Still a hard cut on anything that has
// stopped being a version.
//
// See maxPermissionModeField for the pair's envelope arithmetic and for why neither
// carries a rate bound.
const maxClaudeVersionField = 256

// maxPermissionModeField caps turnevent.SessionFacts's PermissionMode — the posture
// claude says the child is running under, from the same line (#2252). A separate
// constant for the neighbour's stated reason.
//
// MEASURED: bypassPermissions (17 bytes, the 2.1.220 capture) and default (7 bytes,
// all three 2.1.259 init lines). 256 is ~15x the observed maximum, near
// maxModelField's ratio and for its reason — room for a keyword claude has not
// shipped yet.
//
// A CAP AND NOT A MEMBERSHIP CHECK, and this is the one place in the file where the
// distinction is between two things that already exist side by side.
// permissionModeAllowed in envelope.go bounds a permission mode by MEMBERSHIP, and
// is right to: it gates what the DAEMON may ask for on a control request, which the
// daemon controls entirely. This value is claude's report of what it IS running,
// which the daemon neither controls nor may reject. An allow-list here would drop
// the first report of a posture we have not heard of — the case an operator most
// needs to see — so the cap is the only judgement made, exactly as it is the only
// one maxModelField makes.
//
// The envelope arithmetic for the pair, in maxUnrecognizedRaw's style: worst case
// one SessionFacts carries 512 bytes of claude-derived text plus ~37 bytes of
// DAEMON-authored names in TruncatedFields, ~0.8% of the v2 application-envelope cap
// of 65519 bytes (docs/protocol-mobile.md § Application-envelope size cap) — the
// same contribution maxCompactField's pair makes, and twice ModelAnnounced's.
// Amplification from input to retained bytes is near zero: systemInitLine holds
// three scalars and no array, so a 4 MiB line (defaultMaxParseBuf) yields at most
// 768 retained bytes across both events.
//
// No RATE bound for either, and none is owed: init fires once per TURN and produces
// at most one of these, so maxModelField's paragraph covers the pair unchanged — the
// event is not a droppable delta (the droppable set is assistant_delta only, #610)
// and holds a queue slot under the same existing backpressure. Revisit on an
// OBSERVED rate, as #1385 did.
const maxPermissionModeField = 256

// systemInitLine is the decoded payload of one system/init line. Kept separate
// from streamLine for systemTaskStartedLine's reason, and separate from every
// other subtype target because it shares no key with any of them.
//
// THREE FIELDS, and the OMISSIONS are still the point. The captured line carries 24
// keys — type, subtype, cwd, session_id, tools, mcp_servers, model, permissionMode,
// slash_commands, terminal_slash_commands, apiKeySource, claude_code_version,
// output_style, agents, skills, plugins, capabilities, analytics_disabled,
// product_feedback_disabled, uuid, memory_paths, messaging_socket_path,
// fast_mode_state, fast_mode_disabled_reason — and twenty-one are deliberately
// absent from this target. Four of them are why that matters: cwd, memory_paths and
// messaging_socket_path are the operator's local filesystem, and session_id is
// claude's session identity and NOT the daemon's conversation identity (#1380). See
// turnevent.ModelAnnounced's and turnevent.SessionFacts's docs. Absent from the
// DECODE TARGET is a stronger guarantee than the test's reflection sweep, because a
// field that is never declared cannot leak.
//
// The census read 22 keys until #2252 corrected it against the capture #2251
// committed, where all three init lines carry 24 and the newcomers are
// terminal_slash_commands, memory_paths and messaging_socket_path. The field COUNT
// moved in the same ticket for an unrelated reason, from one to three:
// claude_code_version and permissionMode now feed turnevent.SessionFacts.
//
// Plain strings, which is why truncateField's json.RawMessage exception does not
// reach this shape: encoding/json has already U+FFFD-replaced invalid input on
// decode, so our own cut is the only mid-rune hazard.
//
// A NON-STRING VALUE IN ANY OF THE THREE fails the whole decode and takes
// emitInitLine's undecodable arm, exactly as systemTaskUpdatedLine.TaskID does for a
// numeric task id — and that is the ONLY reachable undecodable case here, which is
// what tells a test how to build the fixture: consumeLine has already decoded this
// line into streamLine, so malformed JSON never reaches that function at all.
//
// THAT IS A RUNG-DISTURBANCE #2252 ACCEPTED RATHER THAN AVOIDED, and controlAckLine's
// doc names the shape of it: a new field on a shared decode target makes a
// non-string value in THAT field fail the WHOLE-line decode, so a line that emits
// ModelAnnounced today would newly emit nothing. One target was kept anyway, for
// three reasons. It is what makes the declared-field-set pin a single assertion
// rather than two that can drift apart. Both new keys are strings on all four
// committed init lines across two releases. And a second target would decode the
// same line twice and fire the undecodable Debug twice for one malformed line, which
// is a worse answer to a malformed line than the one this accepts.
type systemInitLine struct {
	Model string `json:"model"`
	// claude's own build. Its consumer is turnevent.SessionFacts, NOT
	// ModelAnnounced, and the two events are separate for that variant's stated
	// wire-compatibility reason.
	ClaudeCodeVersion string `json:"claude_code_version"`
	// claude's camelCase spelling, deliberately: this is the key as it appears on the
	// line, and the DAEMON's snake_case spelling (permission_mode) appears only where
	// the daemon names the field itself — in TruncatedFields and on the wire.
	PermissionMode string `json:"permissionMode"`
}

// conversationResetLine is the decoded payload of one top-level
// conversation_reset line. Kept separate from streamLine for
// systemTaskStartedLine's reason, and the field set is exactly what the parent
// ticket's capture shows minus what nothing reads.
//
// `uuid` is DELIBERATELY ABSENT — the one other key the captured shape carries.
// Nothing in the daemon reads it, and a field never declared cannot reach a log
// or an event: systemInitLine's argument for its own twenty-one omissions,
// applied here to the only omission available.
//
// A non-string new_conversation_id fails the whole decode and takes
// emitConversationReset's decline path, exactly as systemInitLine.Model does for
// emitModelAnnounced — and that is the ONLY reachable undecodable case, because
// consumeLine has already decoded this line into streamLine, so malformed JSON
// never reaches that function at all.
type conversationResetLine struct {
	NewConversationID string `json:"new_conversation_id"`
}

// emitInitLine decodes a system/init line ONCE and emits AT MOST TWO events from
// it, reporting that it CONSUMED the line on every path. Field mapping comes from
// the committed captures (internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json
// and #2251's effort_init_v2.1.259_sonnet_effort.json), never from a hand-built
// payload.
//
// TWO VARIANTS FROM ONE LINE, which is what #2252 changed and why this function
// exists at all. It is named after the LINE rather than after a variant, breaking
// the family's emit<Variant> convention deliberately: a function that emits two
// cannot honestly be named for one. The helpers below it keep the convention, take
// the DECODED struct rather than bytes, and each own one variant's gate.
//
// THE DECODE IS HOISTED HERE rather than duplicated per variant, and the undecodable
// arm with it. That is the whole reason for the shape: two decodes of the same line
// would fire the Debug below twice for one malformed line, which is a worse answer
// to a malformed line than sharing the arm. The order of the two calls is the order
// a client observes and is asserted by a test rather than left to a reader.
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field.
// streamLine's doc states the property it preserves: control shapes are read from
// the top level only and nested content is never re-scanned, which is what stops a
// tool result whose text is literally `{"type":"result"}` from forging a turn
// boundary. Decoding this payload from anywhere else would let claude's own tool
// output announce a model the daemon never ran, or a posture the child is not
// running under.
//
// Nothing is surfaced as an Unrecognized on any path, because keeping `system`
// whole on ignoredLineTypes is what makes "no system line reaches the unrecognized
// lane" structural, and that guarantee is worth more than surfacing a malformed
// line of a subtype we already know.
func (p *Parser) emitInitLine(line []byte) bool {
	var il systemInitLine
	if err := json.Unmarshal(line, &il); err != nil {
		// The subtype is a message-name keyword, not payload — the same class as
		// sl.Type in the drop log above, so this adds no new category of logged
		// content, and the message is byte-identical to the task siblings' arms.
		//
		// The err is deliberately NOT logged, and this is the sharpest instance of
		// that rule in the package: encoding/json QUOTES the offending input bytes
		// into its error text, so `"err", err` on a line whose model, version or
		// permission mode is long or revealing would put that value in the daemon log
		// through a channel no per-path attribute check can see. It is precisely the
		// value #833's posture — restated across internal/relay's v2session_settings.go
		// and internal/sessions' pool.go as "model / effort / YOLO values are NEVER
		// logged at any level" — exists to keep out. The house idiom points the other
		// way (CLAUDE.md: wrap errors with context), which is why it is stated here
		// rather than assumed; cmd/pyry's emit marshal-error path says it outright, and
		// both task siblings' arms already follow it.
		p.log.Debug("streamsup: dropping undecodable system line", "subtype", "init")
		return true
	}
	p.emitModelAnnounced(il)
	p.emitSessionFacts(il)
	return true
}

// emitModelAnnounced emits AT MOST ONE turnevent.ModelAnnounced from an already
// decoded system/init line.
//
// It took the raw line and owned the decode until #2252; the decode moved up to
// emitInitLine when the line gained a second consumer, and nothing about WHAT this
// arm emits moved with it — the gate, the cap and the verbatim rule below are
// unchanged.
//
// AN EMPTY model SUPPRESSES the event, and that DIVERGES from the task handlers,
// which treat a missing field as claude's choice and emit with the field empty.
// Here the model IS the whole payload: an event carrying an empty one would assert
// "claude announced a model" while naming none, which is a claim the line did not
// make. emitRateLimit's `case ""` rung is the precedent, and its formulation
// carries over — absence, a present-but-empty value, and a line carrying no such
// key all land here and are answered identically, which is what makes a plain
// string decode target sufficient.
//
// THAT DROP IS SILENT, unlike emitRateLimit's rungs, which each log a
// daemon-authored reason keyword. emitRateLimit fires once per RUN and has three
// distinguishable rungs; init fires once per TURN and has one non-undecodable drop
// reason, so a Debug there would put a record in the daemon log on every turn —
// reinstating in the log the per-turn noise row
// TestParser_IgnoredLineTypesStaySilent's doc exists to prevent in the event
// stream. emitThinkingProgress's `delta <= 0` arm is the precedent: consumed,
// silent, no log. The observable consequence is worth naming rather than
// discovering: before this arm every init produced one "streamsup: dropping stdout
// line" record per turn from the branch above, and after it a model-carrying init
// produces no record at all and a model-less one produces none either. That is one
// FEWER Debug per turn.
//
// Nothing is surfaced as an Unrecognized on any path, because keeping `system`
// whole on ignoredLineTypes is what makes "no system line reaches the unrecognized
// lane" structural, and that guarantee is worth more than surfacing a malformed
// line of a subtype we already know.
func (p *Parser) emitModelAnnounced(il systemInitLine) {
	// Absent, present-but-empty, and a line carrying no such key all land here and
	// are answered identically.
	if il.Model == "" {
		return
	}
	// No `bound` closure and no sequential-statements rule: one field means there is
	// no TruncatedFields ORDER for a composite literal to decide, which is the only
	// thing that rule protects.
	model, truncated := truncateField(il.Model, maxModelField)
	p.emit(turnevent.ModelAnnounced{
		// claude's value VERBATIM: no lowercasing, no alias expansion, no
		// date-stamping, no family mapping, no lookup against any published model
		// list. The cap is the only judgement made about it here — see the field's
		// doc for why repairing it would be inventing rather than reporting.
		Model:     model,
		Truncated: truncated,
	})
}

// emitSessionFacts emits AT MOST ONE turnevent.SessionFacts from an already decoded
// system/init line: claude's own build and the posture it says the child is running
// under (#2252). Field mapping comes from the committed captures — 2.1.220 in
// dropped_lines_v2.1.220.json and all three init lines of #2251's
// effort_init_v2.1.259_sonnet_effort.json — never from a hand-built payload.
//
// It exists for the reason turnevent.ModelAnnounced exists, applied to two more
// facts of the same shape: the daemon knows what it ASKED FOR and only claude knows
// what it GOT. An unexpected posture is visible here instead of being discarded with
// the rest of the line.
//
// NO effort FIELD, and its absence is MEASURED. #2251 captured this line under the
// production spawn shape with an effort actually set and no init line carries the
// key; effortInitPins holds that measurement and reddens if a later claude starts
// sending one.
//
// BOTH EMPTY SUPPRESSES the event, and ONE empty does not. That DIVERGES from
// emitModelAnnounced one arm up, and the divergence is the whole gate decision
// rather than an inconsistency. There the model IS the payload, so an event naming
// none asserts something the line did not say. Here two facts share one event: one
// present fact is still news, and the other's absence is claude's own choice —
// emitBackgroundTaskStarted's rule, which the task handlers follow for every field
// they carry. Both-empty is the only case that asserts nothing, so it is the only
// one dropped. Absence, a present-but-empty value, and a line carrying no such key
// are answered identically PER FIELD, which is what makes plain string fields
// sufficient.
//
// IT GATES ON THE DECODED VALUES, BEFORE THE CAP, because the question is about the
// LINE. One consequence is worth naming rather than discovering: truncateField
// DELETES invalid UTF-8 rather than replacing it, so a value made only of invalid
// bytes passes this gate and lands empty on the event. That residue already sits
// behind ModelAnnounced.Model's "never empty" claim, on the same helper, and is not
// repaired here — a second gate after the cap would suppress an event for a line
// that did carry a fact, which is the failure this arm's whole shape argues against.
//
// THAT DROP IS SILENT, for emitModelAnnounced's stated reason: init fires once per
// TURN, so a Debug on a routine drop reinstates a per-turn noise row in the log.
func (p *Parser) emitSessionFacts(il systemInitLine) {
	if il.ClaudeCodeVersion == "" && il.PermissionMode == "" {
		return
	}

	var cut []string
	bound := func(value, name string, limit int) string {
		out, truncated := truncateField(value, limit)
		if truncated {
			cut = append(cut, name)
		}
		return out
	}
	// Sequential statements rather than a composite literal, for
	// emitBackgroundTaskStarted's reason: TruncatedFields is ordered by these calls,
	// and inside a literal that order would rest on the left-to-right operand rule
	// rather than on something a reader sees. The second name is the DAEMON's —
	// permission_mode, not claude's permissionMode — exactly as the arm above spells
	// limit_type rather than claude's rateLimitType, and it matches the key the wire
	// payload publishes (#2253).
	version := bound(il.ClaudeCodeVersion, "claude_code_version", maxClaudeVersionField)
	mode := bound(il.PermissionMode, "permission_mode", maxPermissionModeField)
	if il.PermissionMode != "" {
		p.permissionModes.confirmInit(mode)
	}

	p.emit(turnevent.SessionFacts{
		// claude's values VERBATIM: no lowercasing, no alias expansion, no version
		// parsing, no normalising, and no lookup against any published list of releases
		// or permission modes. The cap is the only judgement made about either — see the
		// fields' docs, and maxPermissionModeField for why an allow-list here would drop
		// the first report of a posture nobody has seen.
		ClaudeCodeVersion: version,
		PermissionMode:    mode,
		// nil when nothing was cut: append never ran.
		TruncatedFields: cut,
	})
}

// emitConversationReset decodes one top-level conversation_reset line and emits AT
// MOST ONE turnevent.ConversationReset, reporting whether it CONSUMED the line
// (#2134). claude writes this announcement on its own stdout when a /clear, a
// plan-mode exit, or a fresh-session flow replaces the conversation, naming the id
// it mounted the fresh transcript under.
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field, per
// streamLine's stated property: control shapes are read from the top level only
// and nested content is never re-scanned. That is what stops a tool result whose
// text is literally `{"type":"conversation_reset","new_conversation_id":"…"}` from
// announcing a reset the session never had; the decode target reads only the
// top-level key, so a nested one is invisible to it.
//
// THE ID IS VALIDATED HERE, AND THAT IS THE POINT OF THE ARM. The field is
// daemon-external text that a consumer re-keys the session registry on and, one
// hop further, resolves as <dir>/<id>.jsonl. Gating at the parser is what lets
// every later consumer treat it as a canonical stem instead of re-deriving that
// question, and transcript.ValidStem is the existing predicate rather than a local
// regexp. The check cannot move into internal/turnevent — that package is
// stdlib-only and TestImportBoundary_StdlibOnly enforces it — so validating BEFORE
// construction is also what makes the emitted field canonical BY CONSTRUCTION.
//
// WELL-FORMED IS NOT AUTHENTIC. The gate proves the id is SHAPED like a session
// stem; it does not prove claude was entitled to name this one, and a buggy or
// compromised claude can announce any well-formed stem including another session's.
// Said here as well as at the field because this is where a reader would otherwise
// infer that a validated id is a trusted one.
//
// THIS ARM DELIBERATELY DOES NOT TAKE THE NEIGHBOURS' STRONGER GUARANTEE, and the
// next reader will want to "fix" that. emitRateLimit's and emitModelList's arms
// each document that emitUnrecognized stays unreachable for their type BY MATCHING
// rather than by list membership, and call it the stronger of the two. This one
// declines it on purpose: a reset the daemon CANNOT ACT ON is routed to the
// unrecognized lane, because an announcement the parser silently swallows is the
// exact defect this ticket fixes. Making the consume unconditional would restore
// that defect while looking like a tidy-up.
//
// consumeToolProgress is the SHAPE precedent, not the REASON precedent, and the
// difference matters because copying one without the other turns two decisions
// into a rule nobody can re-derive. That arm falls through because one VARIETY of
// its type is unmeasured; this one falls through because one PAYLOAD is unusable.
//
// ONE DECLINE RULE, NO RUNGS, AND NO DROP-REASON VOCABULARY. Undecodable, absent,
// present-but-empty, and non-canonical all return false and are answered
// identically — emitRateLimit's formulation widened by one case. Nothing is logged
// on any path, which DIVERGES from every sibling arm for a reason worth stating:
// those arms CONSUME their declines, so a Debug is the only trace they can leave,
// whereas here the decline is SURFACED as a turnevent.Unrecognized carrying the
// offending bytes to a client — strictly more visible than a Debug the production
// daemon does not print. A record beside it would be a second, weaker copy of the
// same fact. The consequence is that this function has NO logging surface at all,
// which is a stronger statement than choosing not to log the id.
func (p *Parser) emitConversationReset(line []byte) bool {
	var cr conversationResetLine
	if err := json.Unmarshal(line, &cr); err != nil {
		// The err is deliberately NOT logged, for emitModelAnnounced's sharpest
		// reason: encoding/json QUOTES the offending input into its error text, so
		// logging it would put claude's id in the daemon log through a channel no
		// per-path attribute check can see. Nothing is logged here at all.
		return false
	}
	// Absent, present-but-empty, and a value of any other shape all land here and
	// are answered identically, which is what makes a plain-string decode target
	// sufficient. ValidStem is an anchored full match, so it is the length cap too —
	// a truncateField beside it would be dead code.
	if !transcript.ValidStem(cr.NewConversationID) {
		return false
	}
	p.emit(turnevent.ConversationReset{
		// claude's value VERBATIM: no lowercasing, no trimming, no re-formatting. The
		// gate above decided whether to carry it at all; nothing here repairs it.
		NewConversationID: cr.NewConversationID,
	})
	return true
}
