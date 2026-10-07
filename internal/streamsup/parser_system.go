package streamsup

import (
	"encoding/json"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// maxCompactField caps the two claude-authored fields one system/status line
// carries when compaction ENDS — compact_result and compact_error (#2227). 256, the
// family's value, and a separate constant for maxRateLimitField's stated reason.
//
// IT BOUNDS TWO SINKS AT ONE SITE (#2236, which is the correction: this paragraph
// used to say it was the only cap in the file bounding a LOG rather than an event,
// and that was true for one day). Both values now ride turnevent.Compacting to the
// wire as well as the Debug record, and the arm applies truncateField ONCE, passing
// the same two locals to both — so the cap cannot be right in one sink and wrong in
// the other. The envelope arithmetic the caps above do therefore has a term here
// after all: 512 bytes worst case, ~0.8% of the 65519-byte application-envelope cap.
// See emitCompactingStatus for the wire-facing blast radius in full.
//
// No RATE bound, and here that is derived rather than inherited: the log fires on
// the FALLING edge only, an edge can fall only from one that rose, and a rising
// edge is idempotent while open. So the ceiling is one record per completed
// compaction — not per line, whatever claude sends.
const maxCompactField = 256

// maxCompactTrigger caps the ONE claude-authored string a system/compact_boundary
// line publishes — compact_metadata.trigger (#2237). 256, the family's value, and a
// separate constant for maxRateLimitField's stated reason: the neighbour above names
// the two fields IT bounds, and widening either doc to cover a third field on a
// different line would make both less true than two constants are expensive.
//
// IT DROPS RATHER THAN CUTS, which is the whole judgement here and is why
// truncateField is deliberately not called on this value. maxTurnEndStopField's
// argument transfers verbatim: a client MATCHES this token against known values from
// an open set, so a cut token is indistinguishable from a token the client has never
// heard of — a state it must already handle. Carrying the empty value says exactly
// that and invents nothing. The neighbour's cut-not-drop answer is the opposite for
// the opposite reason: compact_error is prose, where a cut sentence still reads as
// what it is.
//
// No truncation report is owed, on maxTurnEndStopField's rule again: a dropped scalar
// is directly observable as the empty value, unlike an absence a consumer would have
// to infer.
//
// The envelope arithmetic: 256 bytes worst case, ~0.4% of the 65519-byte
// application-envelope cap, on a frame whose only other payload is a conversation id
// and two integers. This frame cannot approach that cap, which is the opposite of the
// situation #2002 had to bound.
//
// No RATE bound, and none is owed. One application per compact_boundary line, and
// each is an O(1) length test against a line already bounded by defaultMaxParseBuf.
// A stream emitting the line in a loop produces small frames that turnMarkFor's
// default classifies turnMarkNone, which makes every one of them droppable at the
// fan-in — the same posture turnevent.Unrecognized carries.
const maxCompactTrigger = 256

// maxBannerText caps the ONE claude-authored prose field a system/informational line
// publishes — content, which reaches the wire as turnevent.Banner.Text (#2319).
// Applied at CONSTRUCTION, exactly as every cap above is, so an oversized value never
// enters the event stream, the push queue, or any log.
//
// 4096 IS A PUBLISHED CONTRACT, not a number chosen here. turnevent.Banner.Text's doc
// and docs/protocol-mobile.md § banner both state 4 KiB as the bound this arm owes,
// and #2256 shipped the frame against that statement. It is the first cap in this file
// whose value was decided by a doc a client reads rather than by a measurement, and
// what backs it is #2256's own reasoning: the field carries arbitrary operator-facing
// prose — a hook's block reason wrapping its own stderr and echoing the operator's
// prompt back — so the observation this arm has (447 bytes of captured line, content
// included) bounds nothing about the next one.
//
// maxDenialProse IS THE WRONG CONSTANT TO REUSE, and it is the cheap wrong move rather
// than an unlikely one. It is 2 KiB, HALF the published contract, and since #2267 it
// already caps a claude-authored prose field the daemon spells `banner` —
// turnevent.ModelRefusalFallback.Banner. The field names match while the events do
// not: that one is a swap notice reporting its cut through a TruncatedFields slice,
// this one is a distinct variant reporting through a single bool. Reusing it would
// silently halve a bound a client was told to expect, at the one site whose name makes
// the mistake read as deliberate. The one-constant-over-several-fields form
// (maxTaskFieldID's, maxDenialProse's own) does not reach across that.
//
// IT CUTS RATHER THAN DROPS, the opposite answer to maxBannerLevel below and
// maxCompactField's reasoning: a cut sentence still reads as what it is, where a cut
// token would match nothing while still looking like one. The cut IS reported
// (turnevent.Banner.Truncated) because an emptied prose field cannot speak for itself
// the way an emptied token does — and because the report is the whole reason that
// field exists rather than a consumer measuring len(Text) against a bound it would
// then own a second copy of.
//
// The envelope arithmetic: worst case one Banner carries 4096 + maxBannerLevel = 4352
// bytes of claude-derived text, 6.6% of the 65519-byte v2 application-envelope cap
// (docs/protocol-mobile.md § Application-envelope size cap), against ToolCallDenied's
// 4864 and ModelRefusalFallback's 5120. NO FitV2EnvelopeCap MEASUREMENT IS TAKEN and
// #2256 declined one on the same ground: every such test in this repo guards an
// aggregate whose per-field caps compose badly, and one bounded string beside one
// bounded token on a frame carrying otherwise a conversation id and a bool is not that
// shape.
//
// No RATE bound, and none is owed. One application per informational line, an O(1)
// length test against a line already bounded by defaultMaxParseBuf, and no parser state
// is retained — which is why maxTurnDenials' second dimension has no analogue here.
// A stream emitting the line in a loop produces frames cmd/pyry's turnMarkFor answers
// turnMarkNone for (its opener set is a whitelist and this variant is not in it), so
// every one is droppable at the fan-in, with retention bounded by
// eventring.MaxEventsPerConversation. maxCompactTrigger's posture exactly.
const maxBannerText = 4 << 10

// maxBannerLevel caps the ONE claude-authored TOKEN a system/informational line
// publishes — level, which reaches the wire as turnevent.Banner.Level (#2319). 256,
// the family's value for a token.
//
// A SEPARATE CONSTANT FROM ITS NEIGHBOUR ABOVE, although both bound one field of one
// line, and the reason is that the one-constant-over-several-fields form serves fields
// of the same KIND taking the same answer — maxDenialProse's two prose fields,
// maxTaskFieldID's three ids. These two take OPPOSITE answers for opposite reasons, so
// a shared budget would be a number two unrelated decisions had to keep agreeing about.
//
// IT DROPS RATHER THAN CUTS, and the drop is UNREPORTED. maxCompactTrigger argues both
// halves in full and turnevent.Banner.Level restates them at the field: a client
// MATCHES this token against an open set, so a cut token is indistinguishable from one
// the client has never heard of — a state it must already handle — while carrying the
// empty value says exactly "no level I can offer you" and invents nothing. No report is
// owed because an emptied scalar is directly observable, unlike an absence a consumer
// would have to infer, and turnevent.Banner.Truncated is Text's answer alone.
//
// UNMEASURED AGAINST AN OVERFLOW, stated rather than left to be assumed from the
// number. The captured line's level is `warning`, 7 bytes, and claude's other
// documented values for the key are shorter still; 256 is the family's token value
// applied to a field nothing has been observed to stretch. Its rate and envelope terms
// are maxBannerText's, which counts this field in its worst case.
const maxBannerLevel = 256

// compactingEndedMsg is the Debug message the falling edge emits, as a literal so
// the test asserting the record's attribute set is closed can find it by message
// rather than by position.
const compactingEndedMsg = "streamsup: compaction ended"

// systemAPIRetryLine is the complete published subset of a system/api_retry
// line. The separate target keeps attempt counters out of streamLine's
// segmentation contract. If either field has a non-integer shape, decoding the
// pair fails and emitAPIRetry publishes the event contract's zero values.
type systemAPIRetryLine struct {
	Attempt    int `json:"attempt"`
	MaxRetries int `json:"max_retries"`
}

// systemStatusLine is the decoded payload of one system/status line — the subtype
// claude uses to announce that compaction started and that it finished (#2227).
// Kept separate from streamLine for resultStopLine's reason, and
// TestStreamLine_StaysSegmentationOnly enforces that boundary.
//
// Status IS THE STATE MACHINE'S ONLY INPUT and it is a plain string, per
// systemThinkingTokensLine's rule: absent, null and "" are one reading here
// (not compacting) and nothing acts differently on the three, so a *string would
// buy a distinction no consumer answers. A status of any other JSON type fails the
// whole decode and takes emitCompactingStatus's undecodable path, which touches
// nothing — the conservative direction, since a status this parser cannot read
// must not be read as a compaction ending.
//
// CompactResult and CompactError are declared for the LOG AND FOR THE EVENT (#2236
// widened the sink; #2227 declared them for the log alone), and in neither case for
// the machine: nothing branches on either, which is what makes the falling edge a
// function of Status alone (see emitCompactingStatus for why that width is the
// point). The captured line carries compaction's outcome across both fields;
// compact_metadata, which the sibling compact_boundary line carries, is deliberately
// NOT declared here. Absence from the decode target is a stronger guarantee than a
// sweep — #2237 is the ticket that publishes the trigger and the token counts, and
// until it lands those values are structurally unreachable from this code.
type systemStatusLine struct {
	Status        string `json:"status"`
	CompactResult string `json:"compact_result"`
	CompactError  string `json:"compact_error"`
}

// emitSystemSubtype maps one system line's subtype, reporting whether it
// CONSUMED the line. Called from consumeLine's ignoredLineTypes branch, so a
// subtype the switch does not match (false) falls through to that branch's
// existing content-free drop.
//
// The case arms are the ONE enumeration of the mapped set. Every comment that
// describes the drop rule points here instead of restating it, because none of
// them fails a build when it goes stale and adding a subtype IS adding a case.
//
// Unknown system subtypes fall through to silence DELIBERATELY, not by
// oversight. Surfacing them would reintroduce the per-turn noise row the
// 2026-07-27 measurement forbade — system is claude's highest-rate emitter — and
// would break the live zero-unrecognized gate
// (internal/e2e/realclaude/`drainForCompletedTurn` fatals on one)
// the next time a claude release adds a chatty subtype.
func (p *Parser) emitSystemSubtype(subtype string, line []byte) bool {
	switch subtype {
	case "task_started":
		return p.emitBackgroundTaskStarted(line)
	case "task_updated":
		return p.emitBackgroundTaskUpdated(line)
	case "task_notification":
		return p.emitBackgroundTaskNotification(line)
	case "task_progress":
		return p.emitBackgroundTaskProgress(line)
	case "background_tasks_changed":
		return p.emitBackgroundTaskRoster(line)
	case "thinking_tokens":
		return p.emitThinkingProgress(line)
	case "init":
		return p.emitInitLine(line)
	case "status":
		return p.emitCompactingStatus(line)
	case "compact_boundary":
		return p.emitCompactionBoundary(line)
	case "permission_denied":
		return p.emitPermissionDenied(line)
	case "model_refusal_fallback":
		return p.emitModelRefusalFallback(line)
	case "model_refusal_no_fallback":
		return p.emitModelRefusalNoFallback(line)
	case "informational":
		return p.emitInformationalBanner(line)
	case "api_retry":
		return p.emitAPIRetry(line)
	default:
		return false
	}
}

// emitAPIRetry maps every system/api_retry line to an active update. Unlike
// compacting, a repeated active line is not redundant: each observed line
// advances the attempt counter. A malformed counter pair is consumed and
// published as zeroes, which is ApiRetry's existing unparsed-counter contract.
func (p *Parser) emitAPIRetry(line []byte) bool {
	var retry systemAPIRetryLine
	if err := json.Unmarshal(line, &retry); err != nil {
		retry = systemAPIRetryLine{}
	}
	p.apiRetryOpen = true
	p.emit(turnevent.ApiRetry{
		Active:  true,
		Current: retry.Attempt,
		Total:   retry.MaxRetries,
	})
	return true
}

// clearAPIRetry publishes the one falling edge owed after retry state opens.
// Callers invoke it before mapping assistant, user, or result content so a
// client clears its retry display before rendering the line that ended it.
func (p *Parser) clearAPIRetry() {
	if !p.apiRetryOpen {
		return
	}
	p.apiRetryOpen = false
	p.emit(turnevent.ApiRetry{Active: false})
}

// emitInformationalBanner maps one system/informational line onto at most one
// turnevent.Banner (#2319), always consuming the line. It is the FIRST producer of
// that variant, which #2256 declared and shipped unwired; the field mapping and both
// cap answers come from the committed capture
// (internal/e2e/realclaude/testdata/operator_system_lines_v2.1.259.json, one line) and
// from the contract that variant's doc publishes, never from a hand-built payload.
//
// NAMED FOR THE SUBTYPE RATHER THAN THE EVENT, unlike emitCompactionBoundary beside
// it, and deliberately: the variant is shared, so an emitBanner would be a name no
// sibling subtype could also take. #2258 was to be that sibling and is CLOSED AS
// ANSWERED (2026-09-10) — the committed capture recorded system/notification and
// system/local_command_output unobserved with zero frames, so neither had a field set
// to map. The name still earns itself: the variant's second slot stays open for
// whichever subtype produces bytes first.
//
// system/informational is the line claude uses for non-error status text about the
// session — hook feedback, a UserPromptSubmit hook's block reason, text a slash
// command prints. Before this arm it fell to consumeLine's silent debug drop, so a
// prompt a hook refused was never answered AND never explained: indistinguishable, from
// the operator's side, from one that was accepted.
//
// NO DECODE-FAILURE GATE IS OWED, which is the one thing this arm's nearest precedent
// might suggest it needs. consumePermissionDeniedLine exists because a real denial
// spells `message` as a string where streamLine declares *streamMessage, failing the
// whole-line decode before sl.Type is read. The captured informational line decodes
// into streamLine CLEANLY with `message` absent — the record states both, as
// decodes_into_stream_line and message_json_type — so this arm is reached by the
// ordinary route and a second entry point would only widen a match whose safety is
// entirely in how little it matches.
//
// The decode's input is `line` — the TOP-LEVEL bytes — never a nested field, and here
// that is emitPermissionDenied's forgery argument held for a further arm. streamLine's
// doc states the property: control shapes are read from the top level only and nested
// content is never re-scanned, so a tool result whose text is literally an
// informational line cannot mint a banner in the operator's notice lane.
//
// THREE CONSUMING PATHS, in the order they appear below:
//
//   - undecodable → Debug naming the subtype, no event. emitPermissionDenied's and
//     emitCompactionBoundary's shared arm on its stated ground: the subtype is a
//     message-name keyword rather than payload, and no claude-authored field was
//     decoded on this path. The decode error is DISCARDED rather than logged, for
//     emitRecoveredDenials' reason — encoding/json QUOTES the offending input into its
//     error text, which on THIS line is the operator's own echoed prompt and a host
//     filesystem path.
//   - decodable, empty content → no event, nothing logged. The gate, argued below.
//   - content present → exactly one event, whatever the other two fields hold.
//
// IT GATES ON CONTENT, WHICH IS emitCompactionBoundary'S ANSWER AND NOT
// emitPermissionDenied'S, and the choice between those two is the decision this arm
// owed. That one gates on nothing because the SUBTYPE is the payload: nothing else on
// that surface separates a denied call from one that ran and failed, so a field-less
// line is still news. Here the TEXT is the payload. turnevent.Banner reports
// operator-facing text claude printed, Text is the field it exists to carry, and with
// content empty there is nothing operator-facing left: Level is a rendering attribute
// of nothing and StopsTurn is a report the daemon acts on nowhere. A client renders
// this frame as a first-class notice, so an empty one is visible chrome saying nothing
// — worse for the operator than the silence it replaces.
//
// The gate CANNOT re-drop a refusal, which is the hazard #2232 names for gating at all
// and the reason this one was checked against it rather than reasoned about in the
// abstract: claude composes the wrapper prose itself — the "blocked by hook" sentence,
// the hook's path, then the original prompt — so a hook refusing with an empty reason
// still yields non-empty content. What the gate can reach is a line with no text at
// all, which carries no refusal to lose. Its absence is pinned inside `make check` by
// TestParser_InformationalGatesOnEmptyContent, not left to the build-tagged
// classification row.
//
// NOTHING IS LOGGED ON THE EMITTING PATH, and on this arm that is the most
// load-bearing instance of the rule in this file. `content` is a hook's stderr: an
// operator-authored script's arbitrary output, which names absolute host paths in the
// captured line, echoes the operator's own prompt back verbatim, and could carry an
// environment variable the script chose to print. It is the field a drop site would be
// most tempted to explain itself with and the one that must never reach a log.
// emitThinkingProgress' posture otherwise applies: everything decoded reaches the
// event, so a second sink would be a record to keep in step for no diagnostic gain.
//
// IT READS AND WRITES NO PARSER STATE, emitCompactionBoundary's property and its
// consequence: a banner arriving inside a turn, between two, or with no turn ever
// opened maps identically, and no arm of consumeLine's turn-boundary reset has anything
// of this function's to reset. turnevent.Banner opens and closes no turn either.
func (p *Parser) emitInformationalBanner(line []byte) bool {
	var il systemInformationalLine
	if err := json.Unmarshal(line, &il); err != nil {
		p.log.Debug("streamsup: dropping undecodable system line", "subtype", "informational")
		return true
	}
	if il.Content == "" {
		return true
	}

	// The two bounds, applied at construction and by OPPOSITE answers — see
	// maxBannerText and maxBannerLevel for why one cuts and reports while the other
	// empties in silence. truncateField is deliberately not called on the level: the
	// value is emptied, not shortened, and no UTF-8 scrub is owed on that branch either
	// because encoding/json already replaced invalid input bytes with U+FFFD on the way
	// into a Go string and the only mid-rune hazard is a cut this branch does not make.
	text, truncated := truncateField(il.Content, maxBannerText)
	level := il.Level
	if len(level) > maxBannerLevel {
		level = ""
	}

	p.emit(turnevent.Banner{
		Level: level,
		Text:  text,
		// The producer's answer, carried as computed. turnevent.Banner.Truncated's doc
		// forbids a consumer re-deriving it from len(Text), which is the same
		// one-authority rule that keeps the cut here rather than at the bridge.
		Truncated: truncated,
		// Claude's prevent_continuation, renamed. A REPORT and never an actuator: nothing
		// in this package or downstream of it reads the field, which is what keeps a
		// fabricated line a misleading label rather than a self-service turn abort.
		StopsTurn: il.PreventContinuation,
	})
	return true
}

// systemInformationalLine is the decoded payload of one system/informational line.
// Kept separate from streamLine for systemTaskStartedLine's reason: that is the
// line-level SEGMENTATION struct and stays at Type/Subtype/Message, and
// TestStreamLine_StaysSegmentationOnly fails the build on a widening.
//
// The field set is exactly what the committed capture shows and nothing invented. The
// two keys the captured line also carries are deliberately absent — uuid, which nothing
// in the daemon reads, and session_id, which is claude's session identity and NOT the
// daemon's conversation identity. Absent from the DECODE TARGET is a stronger guarantee
// than a scrub or a test sweep, because a field that is never declared cannot leak;
// TestParser_InformationalDropsClaudesIdentityKeys is the witness rather than the
// mechanism, and the capture replay proves it against the real values.
//
// Every field is a concrete Go type — two plain strings and a plain bool — so a value
// of the wrong JSON type fails the whole decode and takes the undecodable path.
// Fail-closed, per emitInformationalBanner, and the bool is the reachable case: claude
// spelling prevent_continuation as the string "true" would cost the frame rather than
// producing a half-true one.
type systemInformationalLine struct {
	// Content is claude's prose, named for the key here and renamed to Text at the emit
	// site, per systemModelRefusalFallbackLine's note about where renames happen.
	Content             string `json:"content"`
	Level               string `json:"level"`
	PreventContinuation bool   `json:"prevent_continuation"`
}

// emitCompactingStatus maps one system/status line onto the turnevent.Compacting
// edge pair (#2227), always consuming the line. It is the producer #1074's wire
// frame has been waiting for: protocol.CompactingPayload, turnbridge.MapEvent's arm
// and the desktop's banner all shipped, and the only code that ever constructed the
// event was the terminal path #1348 deleted.
//
// THE SEAM IS OBSERVED, NOT ASSUMED. #2229 drove a live compacting turn against
// claude 2.1.259 and found compaction arriving as two `system` subtypes rather than
// a top-level type of its own: status:"compacting" while it runs, status:null plus
// compact_result/compact_error when it ends, and a sibling compact_boundary carrying
// the metadata. Both are subtypes, which is what makes zero unrecognized_message a
// structural property of this mapping rather than something it has to earn — see
// consumeLine's ignoredLineTypes branch.
//
// THE STATE MACHINE has two states and four transitions, and every one of them is a
// row of TestParser_CompactingEdges:
//
//   - closed + "compacting"  → open,   emit Active:true
//   - open   + "compacting"  → open,   SILENCE (no second rising edge)
//   - open   + anything else → closed, emit Active:false
//   - closed + anything else → closed, SILENCE (nothing to fall from)
//
// THE FALLING EDGE IS DELIBERATELY WIDE — any status that is not "compacting"
// closes it, not only the null the capture happened to record — and the width is
// the substance of this function rather than a loose comparison. The two failure
// directions are not symmetric. A missed falling edge leaves a banner asserting
// "claude is compacting" for the rest of the turn: a false statement to the operator,
// and the exact defect the criterion "the banner cannot stick" names. A spurious one
// ends a banner early, which is smaller and self-correcting. So compact_result is NOT
// a discriminator — a failed compaction closes the edge exactly as a successful one
// does — and a status value claude has not shipped yet closes it too. Narrowing this
// to a match on null would make the mapping depend on a field claude leaves EMPTY,
// which is the weakest thing in the capture to hang it on.
//
// AN UNDECODABLE LINE CONSUMES AND TOUCHES NOTHING, per emitThinkingProgress's
// precedent and for the same reason emitRateLimit's rung 3 stays silent: a status
// this parser cannot read is not evidence that compaction ended, and inventing the
// observation would be worse than missing it. The residual that leaves — an edge
// held open by a line we could not parse — is bounded by consumeLine's `result`
// reset, which closes it at the turn boundary whatever happened inside the turn.
// Surfacing it as an Unrecognized instead is refused for the family's standing
// reason, and here that refusal is load-bearing: it is what the zero-unrecognized
// live gate rests on for a subtype the daemon does in fact recognise.
//
// THE LOG IS NO LONGER AN EXCEPTION TO "NEVER THE CONTENT ITSELF" (#2236), and the
// reason is worth stating because the exception was argued at length one day earlier.
// #2227 logged compact_result and compact_error precisely BECAUSE they did not reach
// the wire, so the Debug record was the only place a failed compaction was visible at
// all — emitUnrecognized's rule inverted, and defensible only for as long as that
// was true. It is not true now: the falling edge publishes both. The record stays,
// byte-identical, because a daemon-side diagnostic is worth having beside the frame;
// but it is a bounded copy of something already on the wire rather than a carve-out,
// which is the ordinary shape emitUnrecognized itself has.
//
// EXACTLY TWO CLAUDE-AUTHORED STRINGS REACH THE EVENT, and the bound on them is
// applied ONCE (see the falling-edge arm below) so the two sinks cannot drift. Active
// is still a bool this function computes from a string comparison, no token count and
// no trigger crosses the transport, and compact_metadata is not even declared on the
// decode target — #2237 is the ticket that publishes those. So the wire-facing blast
// radius of a hostile line is a boolean plus 512 bytes claude wrote, both bounded
// here and neither read by anything in the daemon. The precedent governing the two
// strings is #2224's ErrorCategory, which publishes claude-authored error text on
// this same lane under a 256-byte bound; where this one departs from it — prose
// rather than a token set, cut rather than dropped — is argued at
// turnevent.Compacting's ErrorText.
func (p *Parser) emitCompactingStatus(line []byte) bool {
	var sl systemStatusLine
	if err := json.Unmarshal(line, &sl); err != nil {
		// The subtype is a message-name keyword, not payload — emitThinkingProgress's
		// formulation verbatim, and neither claude-authored field is logged here: on
		// this path they were never decoded.
		p.log.Debug("streamsup: dropping undecodable system line", "subtype", "status")
		return true
	}

	if sl.Status == compactingStatus {
		if !p.compacting {
			p.compacting = true
			p.emit(turnevent.Compacting{Active: true})
		}
		return true
	}
	if !p.compacting {
		return true
	}
	p.compacting = false
	// Bounded ONCE and used twice (#2236). The two locals reach the Debug record and
	// the event, so the record is byte-identical to the one #2227 shipped and the cap
	// cannot drift between the two sinks — there is only one place it is applied.
	result, resultTruncated := truncateField(sl.CompactResult, maxCompactField)
	detail, detailTruncated := truncateField(sl.CompactError, maxCompactField)
	p.log.Debug(compactingEndedMsg,
		"compact_result", result,
		"compact_error", detail,
		"truncated", resultTruncated || detailTruncated)
	p.emit(turnevent.Compacting{Active: false, Result: result, ErrorText: detail})
	return true
}

// compactingStatus is the ONE system/status value that opens the edge. Every other
// value, the empty string included, closes an open one — see emitCompactingStatus
// for why that asymmetry is the design rather than a missing case.
const compactingStatus = "compacting"

// emitCompactionBoundary maps one system/compact_boundary line onto at most one
// turnevent.CompactionBoundary (#2237), always consuming the line. It is the seventh
// arm of emitSystemSubtype and the last compaction line left unmapped: #2227 called
// this subtype the one measured-and-dropped member standing, and named this ticket
// its owner.
//
// IT IS A FRAME OF ITS OWN BECAUSE OF AN ORDERING, not because a wider Compacting
// payload was unappealing. The committed capture's compact turn runs
// status:"compacting", then status:null + compact_result — WHERE THE FALLING EDGE
// FIRES — then system/init, then this line. claude states the counts after the frame
// that would have carried them has already shipped, so the falling edge cannot carry
// them at any price. See turnevent.CompactionBoundary for what a client does with a
// conversation-scoped frame arriving behind a divider it has already drawn.
//
// IT READS AND WRITES NO PARSER STATE, and that one property is the whole of AC 3's
// second half and AC 4. p.compacting is neither consulted nor touched, so a boundary
// line following no rising edge maps identically to one following an edge, and no arm
// of consumeLine's turn-boundary reset has anything of this function's to reset.
// Whether an AUTO compaction announces itself with the same status lines is
// unmeasured — the capture drove a manual /compact — and statelessness is what makes
// that question not need an answer before this can ship.
//
// THE ALLOWLIST IS THE DECODE TARGET, not a filter step downstream of one.
// compactMetadata declares three fields, so encoding/json discards every other key
// including ones claude has not shipped yet. That matters more here than the phrase
// suggests: the observed line carries three uuids naming entries in the OPERATOR'S
// OWN TRANSCRIPT (preserved_segment, preserved_messages, logical_parent_uuid), and
// they sit unredacted in the committed capture. UnrecognizedMessagePayload's doc is
// this package's statement of why a model-adjacent blob is bounded at construction;
// the answer here is narrower than a cap and needs none — the target admits one token
// and two integers and no operator-authored text at all, so maxCompactField's
// truncateField pair has nothing to bound.
//
// THREE CONSUMING PATHS, in the order they appear below:
//
//   - undecodable → Debug naming the subtype, no event. emitCompactingStatus's
//     undecodable arm verbatim, on its stated ground: the subtype is a message-name
//     keyword rather than payload, and no claude-authored field was decoded on this
//     path. A count claude sent out of int range lands here too, which is fail-closed
//     — one absurd field costs the frame rather than producing a half-true one.
//   - decodable, no compact_metadata → no event, nothing logged. The metadata pointer
//     is the presence discriminator: a frame carrying no trigger and no count would be
//     a claim with no content.
//   - metadata present → one event, whatever the three fields hold. A trigger claude
//     omitted and a count claude omitted are both publishable facts, which is exactly
//     the distinction turnevent.CompactionBoundary's pointers exist to carry.
//
// NOTHING IS LOGGED ON THE EMITTING PATH, unlike emitCompactingStatus above. That
// function's Debug is an argued exception — #2227 needed a diagnostic for values that
// reached no wire, and #2236 kept it as a bounded copy of something now published.
// Here emitThinkingProgress's posture applies instead: everything decoded reaches the
// wire, so a second sink would be a record to keep in step with the frame for no
// diagnostic gain, and the trigger is the field a drop site would be most tempted to
// explain itself with.
func (p *Parser) emitCompactionBoundary(line []byte) bool {
	var bl systemCompactBoundaryLine
	if err := json.Unmarshal(line, &bl); err != nil {
		p.log.Debug("streamsup: dropping undecodable system line", "subtype", "compact_boundary")
		return true
	}
	if bl.CompactMetadata == nil {
		return true
	}

	// Bounded by DROPPING, never truncateField — see maxCompactTrigger for why a token
	// set parts company with the prose cap beside it. Applied here at construction, as
	// every cap in this package is, so an oversized value never enters an event.
	trigger := bl.CompactMetadata.Trigger
	if len(trigger) > maxCompactTrigger {
		trigger = ""
	}
	// The two counts cross UNBOUNDED and UNCLAMPED, on turnevent.RateLimited.ResetsAt's
	// rule: they are claude's numbers, not the daemon's, and an int cannot grow. The
	// pointers are claude's presence, carried rather than collapsed.
	p.emit(turnevent.CompactionBoundary{
		Trigger:    trigger,
		PreTokens:  bl.CompactMetadata.PreTokens,
		PostTokens: bl.CompactMetadata.PostTokens,
	})
	return true
}

// systemCompactBoundaryLine is the decode target for one system/compact_boundary
// line. Everything above the metadata is deliberately absent: claude's session_id and
// uuid are claude's identities rather than the daemon's (systemTaskStartedLine's
// rule), and logical_parent_uuid names an entry in the operator's own transcript.
//
// CompactMetadata is a POINTER so absence is decidable — jsonKey's presence-versus-
// present-zero rule applied to an object rather than to a key. A value decode cannot
// answer it: an absent object and one carrying no fields both give the zero struct.
type systemCompactBoundaryLine struct {
	CompactMetadata *compactMetadata `json:"compact_metadata"`
}

// compactMetadata IS THE ALLOWLIST. Three fields, and the exclusion of every other key
// on the object is structural — encoding/json discards what this does not declare —
// rather than a scrub somebody has to maintain as claude adds keys.
//
// The four observed keys deliberately not here, and they are not one class:
// cumulative_dropped_tokens and duration_ms are simply unasked-for, and an unused
// field is a claim nobody checks; preserved_segment and preserved_messages carry
// UUIDS NAMING ENTRIES IN THE OPERATOR'S OWN TRANSCRIPT, which is a different kind of
// reason and the one that would matter if a later ticket weighed adding them.
type compactMetadata struct {
	// Trigger is claude's own word for what started the compaction — "manual" observed,
	// "auto" documented. An open set; the bound is applied by the caller, not here, so
	// the decode target stays a pure shape declaration.
	Trigger string `json:"trigger"`
	// PreTokens and PostTokens are POINTERS so a count claude omitted is distinguishable
	// from a count of zero all the way to the client. post_tokens is optional in
	// claude's own shape and the committed capture happens to carry it, so absence is a
	// hermetic row's job rather than the fixture's — see
	// TestParser_CompactBoundaryPublishesTriggerAndCounts, where the absent and the
	// explicit-zero rows sit side by side precisely so a plain int cannot pass one while
	// failing the other.
	PreTokens  *int `json:"pre_tokens"`
	PostTokens *int `json:"post_tokens"`
}
