package protocol

// StallPayload is the body of an Envelope whose Type == TypeStall
// (docs/protocol-mobile.md § stall). Binary → phone direction; the wire form
// of the internal-only turnevent.Stall onset marker. It carries conversation
// identity only — like turn_state, a stall is a coarse conversation-level
// signal, not turn-scoped, so there is no turn_id; and it is onset-only, so
// there is no clearing field (the phone self-clears on the next turn
// activity). The bridge (#608) supplies ConversationID because the internal
// Stall marker carries none.
type StallPayload struct {
	ConversationID string `json:"conversation_id"`
}

// ApiRetryPayload is the body of an Envelope whose Type == TypeApiRetry
// (docs/protocol-mobile.md § api_retry). Binary → phone direction; the wire
// form of the internal-only turnevent.ApiRetry status peer. Like turn_state it
// is a coarse conversation-level signal, not turn-scoped, so there is no
// turn_id. Active is the show (true) / clear (false) edge; Current/Total are the
// parsed `attempt N/M` counter ({0,0} when claude's counter did not parse). No
// field carries raw banner or screen text. The bridge (#608) supplies
// ConversationID because the internal ApiRetry marker carries none.
type ApiRetryPayload struct {
	ConversationID string `json:"conversation_id"`
	Active         bool   `json:"active"`
	Current        int    `json:"current"`
	Total          int    `json:"total"`
}

// CompactingPayload is the body of an Envelope whose Type == TypeCompacting
// (docs/protocol-mobile.md § compacting). Binary → phone direction; the wire
// form of the internal-only turnevent.Compacting status peer. Active is the show
// (true) / clear (false) edge, and ConversationID is bridge-supplied because the
// internal marker carries none. Not turn-scoped, so there is no turn_id.
//
// CORRECTED 2026-09-08 (#2236): this doc called the frame "banner-only (tui-driver
// streams no compaction progress)", and both halves were already false when #2227
// gave the frame its first real producer — #1348 had deleted the driver the
// parenthetical rests on, and the stream-json seam that replaced it reads claude's
// outcome off the closing system/status line. The frame now carries that outcome.
//
// IT IS THE FIRST claude-AUTHORED TEXT ON THIS FRAME, which is a class change rather
// than two more fields. Before this, every value here was the daemon's own — an id it
// assigned and a bool it computed from a string comparison — so a reader could treat
// the whole payload as trusted. Two of the four fields are now claude's, bounded but
// unsanitized, and the SECURITY paragraph at ErrorText is what a renderer must read
// before drawing either.
//
// No omitempty on either, per this file's rule as stated at ToolResultPayload:
// absence and the zero value mean the same thing, always emitting the key keeps the
// testdata fixture pinning the full shape, and a client built before this landed
// ignores the unknown keys while one built after decodes an older frame lacking them
// to the zero value without error.
type CompactingPayload struct {
	ConversationID string `json:"conversation_id"`
	Active         bool   `json:"active"`
	// Result is claude's own compact_result for the compaction that just ended —
	// "success" on the observed success path (claude 2.1.259). An OPEN SET carried
	// verbatim, on TurnEndPayload.Outcome's rule: treat an unrecognised token as
	// unknown rather than as an error, because claude may ship one at any time.
	//
	// ALWAYS EMPTY ON A RISING EDGE. A compaction that has just started has no
	// outcome to report, so a client rendering this field must gate on Active being
	// false. Empty on a FALLING edge means one of three things a client answers
	// identically: claude sent no key, claude sent an empty one, or the daemon closed
	// the banner at its own turn boundary with no closing line to read.
	//
	// Spelled compact_result rather than result because a bare `result` beside
	// `active` reads as the frame's own status — TurnEndPayload.ErrorCategory's
	// spelling argument, and it also keeps the key identical to claude's own, which
	// is what makes a daemon log line and a captured frame comparable by eye.
	Result string `json:"compact_result"`
	// ErrorText is claude's compact_error: free-form prose describing why a
	// compaction failed, absent entirely on the observed success path. It is the
	// field this frame exists to carry — before #2236 a failed compaction and a
	// successful one were the same two bytes on the wire, so a client could only
	// draw an ordinary banner for both.
	//
	// BOUNDED AT 256 BYTES BY THE DAEMON and CUT rather than dropped, unlike
	// ErrorCategory beside it on turn_end: that is a token set, where a cut token
	// would match no known value while looking like one; this is prose, where a cut
	// sentence still reads as what it is. The cut is NOT reported — a client cannot
	// distinguish a cut value from a short one and needs no such distinction.
	//
	// SECURITY: claude-authored text, bounded by the daemon and NOT sanitized, so
	// TurnEndPayload.Outcome's SECURITY paragraph applies for provenance — the render
	// boundary owing control-character and terminal-escape stripping is the CLIENT's.
	// IT DOES NOT APPLY FOR SHAPE, and a client that renders this like its turn_end
	// neighbours gets it wrong. Those are short category tokens; this is arbitrary
	// prose, and newlines, terminal escapes, markup, a URL and text impersonating
	// daemon chrome all fit inside 256 bytes. Render it as inert text —
	// UnrecognizedMessagePayload.Raw's rule, the closer neighbour on shape, never an
	// HTML sink, an attribute or a URL — and attribute it to claude rather than
	// showing it as the daemon's own finding, which is the trap
	// QuestionDismissedPayload's outcome field exists to avoid.
	ErrorText string `json:"compact_error"`
}

// CompactionBoundaryPayload is the body of an Envelope whose Type ==
// TypeCompactionBoundary (docs/protocol-mobile.md § compaction_boundary, #2237).
// Binary → phone direction; the wire form of turnevent.CompactionBoundary. Like
// compacting it is conversation-scoped rather than turn-scoped, so there is no
// turn_id.
//
// IT IS NOT A WIDER `compacting`, AND IT COULD NOT HAVE BEEN. claude states the
// trigger and the counts on a separate system/compact_boundary line that arrives
// AFTER the closing system/status line the falling edge is read off, so the numbers
// do not exist until that frame has shipped. A client applies this to the divider it
// has ALREADY drawn from compacting's falling edge — and, per the next paragraph,
// sometimes draws the divider from this frame alone.
//
// EVERY FIELD IS claude's, AND SO IS THE FACT OF THE BOUNDARY. That is a class change
// rather than a wider payload, and it is a step beyond the one #2236 made next door:
// CompactingPayload.active is a bool the DAEMON computes from a string comparison it
// makes itself, so a client can read that field as the daemon's own observation. No
// field here is. The daemon publishes this frame for a boundary line that followed no
// compacting edge at all — deliberately, so an auto-compaction announcing itself
// differently is still published — which means this frame can be the ONLY evidence a
// compaction happened. A fabricated line reading pre_tokens 999999 and post_tokens 1
// draws a plausible compaction mark where nothing was compacted. Render it as
// claude's ASSERTION, attributed to claude, never as the daemon's own finding, which
// is the trap QuestionDismissedPayload's outcome field exists to avoid. NOTHING IN
// THE DAEMON ACTS ON ANY FIELD HERE — no retry, no backoff, no teardown, no routing —
// which is what keeps a fabricated value a misleading label rather than an actuator.
//
// NOTHING ELSE FROM claude's compact_metadata CROSSES, and the exclusion is
// structural rather than a scrub: the daemon's decode target (streamsup's
// compactMetadata) declares these three fields, so encoding/json discards every other
// key including ones claude has not shipped yet. The line carries four more, and they
// are two different classes — cumulative_dropped_tokens and duration_ms are simply
// unasked-for, while preserved_segment, preserved_messages and logical_parent_uuid
// carry UUIDS NAMING ENTRIES IN THE OPERATOR'S OWN TRANSCRIPT. Neither claude's
// session_id nor claude's uuid crosses either, per this file's standing rule.
//
// THE TWO COUNTS DEPART FROM THIS FILE'S NO-omitempty RULE IN SHAPE BUT NOT IN
// SPIRIT, and the departure is the frame's whole point. Elsewhere here absence and
// the zero value mean the same thing; here they do not — post_tokens is optional in
// claude's own shape, so a client that collapses them renders "24k → 0 tokens" for a
// boundary claude reported without a post count. They are therefore POINTERS, and
// they still carry NO omitempty: an absent count marshals as a literal `null` rather
// than dropping the key, which keeps every key always present, keeps the testdata
// fixtures pinning the full shape, and states the absence instead of leaving a client
// to infer it. SessionTransitionPayload.WorkspaceCwd is the same shape for the same
// reason, and SessionSettingsPayload's pointer-plus-omitempty is what this
// deliberately is not. A client built before this landed ignores the whole frame; the
// daemon always emits all four keys.
type CompactionBoundaryPayload struct {
	ConversationID string `json:"conversation_id"`
	// Trigger is claude's own word for what started the compaction — `manual` on the
	// observed path (claude 2.1.259, a typed /compact), with `auto` claude's other
	// documented value. An OPEN SET carried verbatim, on TurnEndPayload.Outcome's rule:
	// treat an unrecognised token as unknown rather than as an error, because claude may
	// ship one at any time. Empty means claude named no trigger this client can be
	// offered — either it sent none, or the daemon dropped an oversized one.
	//
	// BOUNDED AT 256 BYTES BY THE DAEMON and DROPPED rather than cut, which is the
	// opposite of compact_error next door and the same answer turn_end's three strings
	// get. That field is prose, where a cut sentence still reads as what it is; this is
	// a token a client MATCHES, where a cut token would match no known value while
	// looking like one. The drop is not reported and needs no report: an empty value is
	// directly observable, unlike an absence a client would have to infer.
	//
	// SECURITY: claude-authored text, bounded by the daemon and NOT sanitized, so
	// CompactingPayload.ErrorText's SECURITY paragraph applies for provenance — the
	// render boundary owing control-character and terminal-escape stripping is the
	// CLIENT's. IT DOES NOT APPLY FOR SHAPE, and this one is the safer half of that
	// pair rather than the riskier: this is a short token from an open set, so it takes
	// TurnEndPayload.Outcome's rule — switch on it against known values — and never
	// UnrecognizedMessagePayload.Raw's prose latitude. A client that renders this token
	// verbatim into a sentence is rendering up to 256 bytes claude chose.
	Trigger string `json:"trigger"`
	// PreTokens and PostTokens are claude's context size before and after the
	// compaction, exactly as claude stated them.
	//
	// `null` MEANS claude STATED NO SUCH COUNT and `0` means claude stated zero, and a
	// client MUST NOT collapse them: rendering "24k → 0 tokens" for a boundary with no
	// post count is the failure this shape exists to prevent. Degrade where a count is
	// absent — say the conversation was compacted without claiming a size.
	//
	// NEITHER IS CLAMPED, RANGE-CHECKED OR ORDERED by the daemon, on
	// RateLimitedPayload's rule: they are claude's numbers, not the daemon's. post
	// greater than pre is not rejected and not corrected, and neither is a negative.
	// What the daemon does guarantee is that a value it could not decode as an integer
	// produces no frame at all rather than a frame with an invented number.
	PreTokens  *int `json:"pre_tokens"`
	PostTokens *int `json:"post_tokens"`
}

// The two values ResettingPayload.Phase can carry (#2453). A CLOSED set, and closed
// in the strong sense the daemon can actually keep: it authors both tokens itself
// rather than forwarding one of claude's, so a client switches on two cases and has
// no third to guess at. Contrast CompactingPayload.Result above, an OPEN set carried
// verbatim precisely because claude may ship a token at any time.
//
// The two are ORDERED, and one reset emits both — see ResettingPayload.
//
// The Reset prefix rather than Resetting names the DOMAIN, "a reset's phase", rather
// than the frame it rides on; SystemPromptStatus* in system_prompt.go is the
// package's precedent for both the shape and the spelling.
const (
	ResetPhaseWrappingUp = "wrapping_up" // the wrap-up turn is running; it writes the handoff note
	ResetPhaseRestarting = "restarting"  // the wrap-up turn is over; claude is being killed and respawned
)

// The three values ResettingPayload.Handoff can carry (#2453). A CLOSED set on the
// same terms as the phases above: the daemon authors all three.
//
// The set is what lets a client SAY WHETHER A NOTE WAS MADE, which is the part of a
// reset an operator cannot otherwise find out. It resolves once, on the phase change:
// pending throughout wrapping_up, then written or skipped on restarting.
//
// ResetHandoffSkipped IS A REPORTED OUTCOME, NOT A MISSING VALUE, and it merges every
// daemon-side reason a note was not made — the wrap-up turn produced nothing usable,
// it failed, or it was not run at all. All of them mean the same thing to a client:
// the successor starts without a note, and nothing the client sends would repair it.
// No reason field is declared, so no prose channel exists on this frame; whoever adds
// one owes it a bound and a sanitization statement, as CompactingPayload.ErrorText
// carries above.
const (
	ResetHandoffPending = "pending" // the note is not resolved yet; only ever seen with ResetPhaseWrappingUp
	ResetHandoffWritten = "written" // a handoff note was written for the successor
	ResetHandoffSkipped = "skipped" // no note was written, and the successor starts without one
)

// ResettingPayload is the body of an Envelope whose Type == TypeResetting
// (docs/protocol-mobile.md § resetting, #2453). Binary → phone direction, gated on
// the already-negotiated interactive capability. Like compacting it is
// conversation-scoped rather than turn-scoped, so there is no turn_id, and emitting
// it never opens or closes a turn.
//
// IT HAS compacting's SHAPE AND NONE OF ITS PROVENANCE, which is the one thing to
// carry away from the adjacency. Every status peer before it is the wire form of a
// stream-json detector reading claude's own output, and CompactingPayload beside it
// carries two claude-authored strings under a SECURITY paragraph. NOTHING ON THIS
// FRAME IS claude's: the id is one the daemon assigned, Active is a bool it computed,
// and Phase and Handoff are tokens it selected from the closed sets above. A client
// must not inherit the neighbour's render rules from the neighbourhood — there is no
// untrusted text here to strip, escape or attribute.
//
// THE HANDOFF NOTE'S CONTENT DOES NOT CROSS THIS WIRE, and that is the design
// decision the paragraph above rests on rather than a gap. The note is prose a claude
// wrap-up turn writes; Handoff reports only WHETHER one was made. A field carrying
// the note itself would be this frame's one genuinely untrusted value and would need
// a bound, a sanitization statement and a render rule; none is declared, so none is
// owed.
//
// ONE RESET EMITS TWO RISING EDGES BEFORE ONE FALLING EDGE, and this is where the
// frame departs from compacting's strict edge pair:
//
//	active:true  phase:wrapping_up  handoff:pending
//	active:true  phase:restarting   handoff:written | skipped
//	active:false phase:""           handoff:""
//
// A REPEATED active:true CARRYING A NEW PHASE IS A PHASE CHANGE, NOT A SECOND RESET.
// A client that treats every rising edge as a new reset draws two. Every rising
// sequence ends in a falling edge, on success and on every error path.
//
// Phase and Handoff are MEANINGFUL ONLY WHILE Active IS TRUE. A falling edge carries
// both as the empty string, so a client switching exhaustively over either set needs
// a case for "" — gate on Active rather than adding a fourth token. No omitempty on
// any field, per this file's rule as stated at ToolResultPayload: the key is always
// on the wire and its zero value is the statement.
type ResettingPayload struct {
	ConversationID string `json:"conversation_id"`
	Active         bool   `json:"active"`
	// Phase is ResetPhaseWrappingUp or ResetPhaseRestarting while Active, "" once the
	// reset is over. It exists so the pause has a name: the two phases fail
	// differently and take visibly different lengths of time.
	Phase string `json:"phase"`
	// Handoff is ResetHandoffPending, ResetHandoffWritten or ResetHandoffSkipped while
	// Active, "" once the reset is over. It resolves on the phase change, never
	// mid-phase.
	Handoff string `json:"handoff"`
}

// BannerPayload is the body of an Envelope whose Type == TypeBanner
// (docs/protocol-mobile.md § banner, #2256). Binary → phone direction; the wire form
// of turnevent.Banner. Like compacting and unrecognized_message it is
// conversation-scoped rather than turn-scoped, so there is no turn_id.
//
// THE ABSENT turn_id IS FORCED BY THE PRODUCER SET, not chosen for symmetry with those
// two. A prompt a hook refuses is never answered, so no turn exists to attribute the
// refusal to; a notification belongs to claude's own queue and rides no turn at all.
// There is no turn the daemon could honestly name, and naming one anyway would attach
// operator-facing text to work it did not come from.
//
// EVERY FIELD IS claude's, AND SO IS THE FACT OF THE BANNER — CompactionBoundaryPayload's
// class change, landing somewhere sharper. That frame at least reports a compaction the
// daemon can corroborate from its own parser state; this one reports that claude had
// something to say, which nothing else on the wire confirms or contradicts.
//
// IT IS THE FIRST FRAME HERE WHOSE WHOLE PURPOSE IS ARBITRARY claude-AUTHORED PROSE
// with no machine state anchoring it, and that is a class change rather than another
// prose field. CompactingPayload.ErrorText is prose attached to a compaction the daemon
// observed; UnrecognizedMessagePayload.Raw is prose explicitly labelled a parser gap. A
// client renders THIS one as a first-class notice, so text impersonating daemon chrome
// at level "warning" is the realistic abuse rather than a hypothetical one. Render the
// whole frame as claude's ASSERTION, attributed to claude, never as the daemon's own
// finding — the trap QuestionDismissedPayload's outcome field exists to avoid — and
// render Text as inert text on UnrecognizedMessagePayload.Raw's rule.
//
// NOTHING IN THE DAEMON ACTS ON ANY FIELD HERE — no retry, no backoff, no teardown, no
// routing — which is what keeps a fabricated banner a misleading label rather than an
// actuator. StopsTurn is where that constraint is most load-bearing and it is restated
// at the field, because the field NAME is what invites the mistake.
//
// IT CARRIES NO DATA CLASS AN INTERACTIVE GRANT DOES NOT ALREADY RECEIVE, which is why
// no narrower capability gate exists. The observed text embeds a host filesystem path
// and echoes the operator's own prompt back, and ToolUsePayload.Input's verbatim
// top-level fields already carry both to the same grant.
//
// No omitempty on any field, per this file's rule as stated at ToolResultPayload:
// absence and the zero value mean the same thing, always emitting the key keeps the
// testdata fixture pinning the full shape, and a client built before this landed
// ignores the unknown keys. Every field is a value type — two strings and two bools, no
// pointer and no slice, unlike CompactionBoundaryPayload's counts and ToolDeniedPayload's
// report slices — so this frame raises no aliasing question at either seam.
type BannerPayload struct {
	ConversationID string `json:"conversation_id"`
	// Level is claude's own key, adopted VERBATIM — the one field here that keeps
	// claude's spelling, where text and stops_turn are renames. It is consequently the
	// one word a vocabulary pin on this frame may NOT check for, the trap
	// TestSlashCommandListType_IsNotClaudesVocabulary records for `commands`.
	//
	// An OPEN SET, on TurnEndPayload.Outcome's rule: treat an unrecognised token as
	// unknown rather than as an error, because claude may ship one at any time.
	// `warning` is the OBSERVED value (claude 2.1.259, the single informational line in
	// the committed operator_system_lines capture); `info`, `notice` and `suggestion`
	// are claude's other DOCUMENTED values for the key and have not been observed on
	// this repo. The two groups are named separately on purpose — presenting four as a
	// set this repo has seen would overstate the evidence.
	//
	// BOUNDED BY THE PRODUCER AND DROPPED RATHER THAN CUT, which is the opposite answer
	// text beside it gets, and the pairing is the trap worth naming on this frame:
	// CompactionBoundaryPayload.Trigger argues it in full. This is a token a client
	// MATCHES, where a cut token matches no known value while still looking like one;
	// text is prose, where a cut sentence still reads as what it is. Empty means claude
	// named no level this client can be offered — either it sent none, or the daemon
	// dropped an oversized one. The drop is not reported and needs none: an emptied
	// scalar is directly observable.
	Level string `json:"level"`
	// Text is claude's `content`, renamed — the field this frame exists to carry. The
	// rename is half of the translation layer the vocabulary pin holds: `content`
	// beside `level` would read as the frame's own body rather than as what claude
	// printed.
	//
	// BOUNDED AT 4 KiB BY THE DAEMON, at the PRODUCER (#2319), and CUT rather than
	// dropped. The bound is stated here as the contract THAT ticket owed rather than as
	// a fact this package enforces: the enforcing constant is streamsup's maxBannerText,
	// landed beside maxCompactTrigger and maxDenialProse, on this repo's standing division
	// that claude's text is bounded where it crosses the subprocess boundary. This
	// shape and the bridge re-decide no maximum — a second cap site is a second place
	// the limit is decided and the two could disagree silently, which is the rule
	// ToolDeniedPayload's doc states.
	//
	// SECURITY: claude-authored text, bounded by the daemon and NOT sanitized, so
	// CompactingPayload.ErrorText's SECURITY paragraph applies for provenance — the
	// render boundary owing control-character and terminal-escape stripping is the
	// CLIENT's. IT APPLIES FOR SHAPE TOO, and this is the RISKIER half of the pair
	// CompactionBoundaryPayload.Trigger is the safer half of. Newlines, terminal
	// escapes, markup, a URL and text impersonating daemon chrome all fit inside 4 KiB,
	// and the observed line contains a host filesystem path and echoes the operator's
	// own prompt back. Never an HTML sink, an attribute or a URL.
	Text string `json:"text"`
	// Truncated says whether the daemon cut text to fit the bound above. It is the
	// PRODUCER's answer carried verbatim, never one the bridge recomputed from the
	// string's length: a second authority on the same fact is a second place it can be
	// decided differently.
	//
	// There is no companion dropped_fields slice, unlike ToolDeniedPayload's pair, and
	// none is owed. text is the only field the daemon cuts, so one bool names it
	// unambiguously — that frame needs a slice because it has six candidate fields —
	// and an emptied level is directly observable, exactly as
	// CompactionBoundaryPayload.Trigger's unreported drop is.
	Truncated bool `json:"truncated"`
	// StopsTurn is claude's `prevent_continuation`, renamed. It says claude will not
	// continue past this banner; the observed value is true, on a hook that refused a
	// prompt.
	//
	// A REPORT, NEVER AN ACTUATOR, and the constraint is restated here rather than left
	// to the type's doc because THIS FIELD'S NAME IS WHAT INVITES THE MISTAKE. It reads
	// like a lever, and a daemon that ever keyed a teardown, a retry suppression or a
	// queue decision on it would hand claude a self-service turn abort — a fabricated
	// line stopping work the operator asked for. A CLIENT owes the same restraint:
	// render it, do not let it cancel anything the operator did not cancel.
	StopsTurn bool `json:"stops_turn"`
}

// ThinkingProgressPayload is the body of an Envelope whose Type ==
// TypeThinkingProgress (docs/protocol-mobile.md § thinking_progress, #1386).
// Binary → phone direction; the wire form of turnevent.ThinkingProgress, the
// daemon's translation of claude's system/thinking_tokens line. It reports that
// claude is actively reasoning and roughly how much — claude's only mid-turn
// proof of life on the stream-json surface.
//
// Like turn_state it is a coarse conversation-level signal, NOT turn-scoped, so
// there is no turn_id, and receiving one neither opens nor closes a turn. The
// bridge supplies ConversationID because the internal event carries none;
// claude's own session_id is deliberately absent for BackgroundTaskStartedPayload's
// reason, and the parser drops it before the event exists (#1380/#1385).
//
// Both fields are claude's own integer readings, carried verbatim. There is no
// TruncatedFields, and its absence is a decision rather than an omission: unlike
// every sibling above this payload carries no claude-authored TEXT at all, so
// nothing is ever cut, and a permanently-nil field would claim a bound that does
// not exist. For the same reason there is no producer byte cap to mirror here.
//
// Two consumer hazards ride these numbers and are NOT restated here, because
// turnevent.ThinkingProgress's field comments are their single source of truth
// (with the measured numbers): EstimatedTokens is not monotonic across a turn,
// and the EstimatedTokensDelta values a client receives do not sum to the turn's
// total. A consumer-facing statement of both, plus the two reasons absence proves
// nothing, is in docs/protocol-mobile.md § thinking_progress.
type ThinkingProgressPayload struct {
	ConversationID       string `json:"conversation_id"`
	EstimatedTokens      int    `json:"estimated_tokens"`
	EstimatedTokensDelta int    `json:"estimated_tokens_delta"`
}

// RateLimitedPayload is the body of an Envelope whose Type == TypeRateLimited
// (docs/protocol-mobile.md § rate_limited, #1405). Binary → phone direction; the
// wire form of turnevent.RateLimited, which reports that claude's usage-limit
// window is in a state other than the one measured-benign one.
//
// Like ThinkingProgressPayload it is conversation-scoped rather than turn-scoped,
// so there is no turn_id, and receiving one neither opens nor closes a turn: a
// usage-limit window is orthogonal to whichever turn happened to observe it
// (turnevent.RateLimited's own doc). The bridge (#1410) supplies ConversationID
// because the internal event carries none. claude's session_id and uuid are
// deliberately absent for BackgroundTaskStartedPayload's reason plus #1380's —
// they are claude's session identity and claude's per-line message id, neither of
// which is the daemon's conversation identity, and the parser never decodes them
// so this payload cannot carry them even by accident.
//
// Status is the field that says WHY the frame fired, and its value set beyond the
// benign one is ALMOST ENTIRELY UNMEASURED: exactly one non-benign value is on
// record (allowed_warning, the weekly warning band) and no capture of a limit
// actually in force exists on any claude version. A
// plain string, not a closed enum, so the set gets measured the first time a real
// limit fires rather than the daemon inventing one it has no evidence for. The
// producer's gate is deliberately loud in the same direction: any non-empty status
// other than the measured-benign one emits, so an unrecognised status surfaces and a
// human looks rather than a real limit vanishing. Dropping Status from this payload
// would silence that one layer later.
//
// THE BENIGN STATUS IS NOT ALWAYS SILENT, and a client that assumed it was reads
// this frame backwards. Since #2250 the gate publishes a benign reading that FOLLOWS
// a non-benign one on the same parser — the falling edge, which exists so a client
// can take a quota banner down. Two things follow for a decode. The benign value is
// the ONE value worth comparing against, and it is the only one measured stable on
// every claude version on file; everything else stays an opaque label. And the frame
// is not the limit lifting: claude reports one window per line and chooses which, so
// a benign reading is claude declining to report a non-benign window. See
// docs/protocol-mobile.md § rate_limited, which states both readings for a client,
// and turnevent.RateLimited for the daemon-side argument.
//
// LimitType is WHICH limit is in force (five_hour and seven_day are the observed
// values, the second riding the one non-benign status), a
// plain string for Status's reason. The falling edge therefore names a DIFFERENT
// limit than the warning it clears — every benign reading on record says five_hour —
// so a client that keys a banner by this value never matches the frame that takes it
// down; key it on the conversation. ResetsAt is when claude says it lifts, as
// unix seconds, 0 when claude did not report it. Neither wire name tracks
// claude's key: claude's are rateLimitType and resetsAt under rate_limit_info,
// while these are turnevent.RateLimited's own field names in snake_case, so a
// claude rename does not move them. status coincides with claude's spelling but
// is the daemon's chosen name for the field — it is what the producer's bound()
// reports it as — and a generic English word rather than a vocabulary import.
// utilization coincides the same way and for the same reason, which is why it is
// NOT a member of the claude-key enumeration TestRateLimitedType_IsNotClaudesVocabulary
// forbids on the wire.
//
// Utilization is how much of the window claude says is spent, and it is a POINTER
// so null on the wire means claude reported nothing while 0 means claude reported
// an untouched window. A client that reads a missing reading as zero renders a
// fresh quota as an exhausted one, which is the failure this shape exists to
// prevent — turnevent.RateLimited.Utilization's own doc is the single source of
// truth for that argument and for why 0 cannot absorb absence the way ResetsAt's 0
// can. The daemon always emits the key, so an absent reading is a literal null and
// never a dropped key. It is not in TruncatedFields and has no cap, for ResetsAt's
// reason: a float64 cannot grow.
//
// TruncatedFields names the fields the producer cut to fit its cap, using these
// wire names ("status", "limit_type", in that order); it is null when nothing was
// cut, never an empty array, which is why this type has no MarshalJSON. The
// nil-normalising guard above is BackgroundTaskRosterPayload.Tasks's and does not
// generalise: an empty roster is a positive statement, whereas nothing-was-cut is
// an absence. It is load-bearing, not decoration — a payload that dropped it
// would present claude's truncated text to a phone as complete.
//
// SECURITY: Status and LimitType are claude-authored strings that crossed the
// subprocess trust boundary. They are safe to RENDER as inert text and must never
// be fed to an HTML sink, an attribute, or a URL; the daemon bounds them but does
// not sanitize them, so they stay untrusted, model-influenced text all the way to
// the client. Their bound is the producer's, decided at construction
// (internal/streamsup/parser.go's maxRateLimitField), so this struct re-decides no
// maximum: a second cap here would be a second place the limit is decided, and the
// two could disagree silently. The constraint on turnevent.RateLimited follows the
// data onto the wire — it is a REPORT, never a control input, so a client MUST NOT
// branch security-relevant behaviour on Status.
//
// BOTH NUMBERS ARE CLAUDE'S AND BOTH ARE UNVALIDATED IN BOTH DIRECTIONS, and saying
// so of one while adding a second is how a client concludes the second was checked.
// ResetsAt must not be assumed to lie in the future, or in a sane range at all.
// Utilization is NOT a bounded fraction: it must not be assumed to lie in 0..1
// either, so scaling a progress bar or a gauge by it without a range check is this
// field's realistic bug, and a value above one is not evidence of anything but what
// claude sent. Neither is clamped, rounded or rejected anywhere on the path.
type RateLimitedPayload struct {
	ConversationID  string   `json:"conversation_id"`
	Status          string   `json:"status"`
	LimitType       string   `json:"limit_type"`
	ResetsAt        int64    `json:"resets_at"`
	Utilization     *float64 `json:"utilization"`
	TruncatedFields []string `json:"truncated_fields"`
}
