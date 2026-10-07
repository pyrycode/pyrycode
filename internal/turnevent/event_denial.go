package turnevent

// ToolCallDenied reports that claude refused a tool call it was about to make. It
// maps claude's system/permission_denied line (#2232), and it exists because
// nothing else on this surface distinguishes a BLOCKED call from one that ran and
// failed: claude announces the denial here and then writes the same rejection text
// into a tool_result carrying is_error, which is all a client could previously see.
//
// THE NAME IS THE DAEMON'S AND DELIBERATELY NOT PermissionDenied. That spelling
// would sit beside PermissionRequest and PermissionResponse in this package, which
// are the MODAL ask/answer pair, and a reader would take it for that pair's
// negative answer. It is not: NOTHING ASKED. A denial resolved by an operator
// answering a --permission-prompt-tool modal, or inside a PreToolUse hook, produces
// no permission_denied line at all (#2234 covers that gap). The words used here are
// ToolStart's and ToolUpdate's, which is also what keeps a claude rename of the
// subtype landing in the parser and nowhere else.
//
// It opens and closes no turn, exactly as CompactionBoundary does not.
//
// EVERY FIELD IS claude's, AND SO IS THE FACT OF THE DENIAL — CompactionBoundary's
// class of variant rather than Compacting's. A fabricated or simply mistaken line
// can name the ToolCallID of a call that ACTUALLY RAN AND SUCCEEDED, and a client
// that trusts it renders a successful call as blocked. The daemon does NOT verify
// the id against a call it saw and deliberately will not: the cross-check needs
// parser state this mapping refuses to hold, and it would silently drop a denial
// whose call preceded a session rotation, which is #2232's own defect restored. Two
// things bound the damage instead. The join is the CLIENT'S — an id matching no
// tool call it has seen renders as unattributed, never as a match. And NOTHING IN
// THE DAEMON ACTS ON ANY FIELD HERE: no retry, no backoff, no teardown and no
// routing is keyed on them, which is what keeps a fabricated value a misleading
// label rather than an actuator. Whoever first makes the daemon behave differently
// on one owes the review that changes that.
//
// The two keys the captured lines also carry are deliberately NOT fields, on
// BackgroundTaskStarted's rule: session_id, which is claude's session identity and
// NOT the daemon's conversation identity, and uuid, claude's per-line message id
// that nothing in the daemon reads. Both are absent from streamsup's decode target,
// so the exclusion is structural rather than a scrub. Like every variant here it
// carries no conversation identity of the DAEMON's — the bridge injects that.
//
// Every string is claude-derived and bounded by the producer AT CONSTRUCTION
// (streamsup's maxTaskFieldID / maxDenialProse), so an oversized payload never
// enters the event stream, a queue, or a log.
type ToolCallDenied struct {
	// ToolName is claude's name for the tool it refused ("Bash" in all seven
	// captured lines). An OPEN SET carried verbatim, on TurnEnd.Outcome's rule.
	//
	// DROPPED rather than cut when it exceeds its cap, on maxTurnEndStopField's
	// reasoning: a consumer switches on this token against claude's tool set, so a
	// cut name would match nothing while still looking like a tool. Unlike
	// CompactionBoundary.Trigger the drop IS reported — see DroppedFields.
	ToolName string
	// ToolCallID is the call claude refused: claude's tool_use_id, under the name
	// ToolStart and ToolUpdate already carry for the same identifier, so a consumer
	// joins the three with no vocabulary lookup. All seven captured denials share
	// their id with the assistant tool_use before them and the tool_result after.
	//
	// DROPPED rather than cut, and this is the field where that answer matters most:
	// the id is JOINED against a tool call the client already saw, so a cut id joins
	// to nothing while still looking like a real handle — strictly worse than an
	// absent one, which the client can see. ModelWindow.ModelID gives the identical
	// answer for the identical reason.
	//
	// THIS DIVERGES FROM BackgroundTaskStarted.ToolCallID, which cuts and reports the
	// same identifier under the same name, and the divergence is stated at both ends
	// rather than left for a reader to find. That field predates the join-key rule
	// maxTurnEndStopField and ModelWindow.ModelID settled; nothing joins on it today.
	ToolCallID string
	// Message is claude's rejection text — the prose it also writes into the
	// tool_result. Every captured line is a sandbox refusal naming the absolute host
	// paths of the session's allowed working directories.
	//
	// CUT rather than dropped, on maxCompactField's reasoning: a cut sentence still
	// reads as what it is. The cut is reported in TruncatedFields.
	//
	// SECURITY: claude-authored, bounded by the daemon and NOT sanitized. For a
	// rule-based denial it may quote the command line that was refused, which can
	// carry whatever an operator typed. Safe to RENDER as text, never to execute or
	// re-shell — BackgroundTaskStarted.Description's rule, and the same hazard.
	Message string
	// DecisionReasonType is claude's word for WHAT denied the call — "classifier",
	// "asyncAgent", "mode" and "rule" are the documented values
	// (SDKPermissionDeniedMessage in @anthropic-ai/claude-agent-sdk@0.3.263).
	//
	// OBSERVED ABSENT IN ALL SEVEN CAPTURED LINES at claude 2.1.239, and that absence
	// is PINNED as an observation rather than assumed away
	// (TestParser_DenialCaptureYieldsEmptyDecisionReasons). It is decoded anyway
	// because the consumer wants it and the decode is tolerant either way. Empty
	// therefore means claude sent nothing — a reader must not mistake it for a value,
	// which is what the two report slices below make decidable.
	//
	// DROPPED rather than cut when over-cap, on ToolName's reasoning: a token from a
	// documented set is matched, not read.
	DecisionReasonType string
	// DecisionReason is claude's free-form explanation of the denial, absent in all
	// seven captured lines exactly as DecisionReasonType is. CUT rather than dropped
	// and reported in TruncatedFields, on Message's reasoning, and it carries
	// Message's SECURITY reading verbatim.
	DecisionReason string
	// TruncatedFields names the fields the producer CUT to fit their caps, in
	// declaration order, using the DAEMON's snake_case names: "message",
	// "decision_reason". nil when nothing was cut, never an empty non-nil slice, so a
	// consumer can emit it as absent rather than [].
	TruncatedFields []string
	// DroppedFields names the fields the producer EMPTIED for exceeding their caps,
	// in declaration order, under the daemon's names: "tool_name", "tool_call_id"
	// (not claude's tool_use_id — the report names the field it describes),
	// "decision_reason_type". nil when nothing was dropped.
	//
	// A SECOND REPORT SLICE IS NEW TO THIS PACKAGE, and it is what makes the drop
	// answer usable on more than one field. CompactionBoundary.Trigger drops without
	// a report because it is the ONLY droppable value on that event, so an empty
	// trigger is unambiguous. Here three fields drop, and without this an empty
	// ToolName could not be told apart from a tool_name claude never sent — the same
	// absence-versus-value confusion DecisionReasonType's doc exists to prevent. A
	// field named here was emptied BY THE DAEMON; a field empty and named in neither
	// slice was empty when claude sent it. BackgroundTaskRoster.DroppedTasks already
	// gives the family this vocabulary for overflow-by-dropping.
	DroppedFields []string
}

// ModelRefusalFallback reports that a turn ended with stop reason `refusal`, that
// claude retried it on a fallback model, and — when Scope says so — that the
// session stays on that model. It maps claude's system/model_refusal_fallback line
// (#2267), and it exists because the swap was otherwise SILENT: a client saw the
// label change on the next ModelAnnounced with nothing explaining it.
//
// THE NAME IS THE DAEMON'S, and it joins the Model* family — ModelAnnounced,
// ModelList, ModelWindow — because a change of the model claude is running is that
// family's subject. It deliberately does not shorten to ModelSwapped: a
// local-scoped fallback is a one-turn retry rather than a swap, so the shorter name
// would over-claim on half the documented set.
//
// IT DOES NOT REPLACE ModelAnnounced, AND A CONSUMER MUST NOT TREAT IT AS THE
// AUTHORITY ON WHAT CLAUDE IS RUNNING. This event is claude's announcement of what
// it will run NEXT; ModelAnnounced is what it says it IS running. Read the swap's
// CAUSE here and its RESULT there. A client that updated its model state from
// FallbackModel rather than waiting for the following announcement would be
// trusting a prediction as an observation, and a mistaken line would then park it
// on a label claude never adopted.
//
// It opens and closes no turn, exactly as ModelAnnounced does not.
//
// EVERY FIELD IS claude's, AND SO IS THE FACT OF THE FALLBACK — CompactionBoundary's
// class of variant rather than Compacting's. The daemon verifies none of it and has
// nothing on this surface to verify it against. What bounds the damage is that
// NOTHING IN THE DAEMON ACTS ON ANY FIELD HERE: no routing, no retry, no model
// selection and no session setting is keyed on them, which is what keeps a
// fabricated value a misleading label rather than an actuator. Whoever first makes
// the daemon behave differently on one owes the review that changes that.
//
// FIVE OF CLAUDE'S ELEVEN KEYS ARE DELIBERATELY NOT FIELDS, on BackgroundTaskStarted's
// rule, and all five are absent from streamsup's decode target so the exclusion is
// structural rather than a scrub. `trigger` and `direction` are constants restating
// the subtype, which this type's identity already carries. `request_id` is an
// API-side identifier nothing in the daemon reads. `retracted_message_uuids` and
// `refused_user_message_uuid` name claude's MESSAGE identity, which no daemon
// surface can join against — assistant_delta carries turn_id and seq, not these. A
// consumer therefore CANNOT honour the retraction of the refused partial response,
// and that is a stated limit of this wire rather than an omission to fix here.
//
// THE FIELD SET IS DOCUMENTATION-DERIVED, NOT CAPTURE-DERIVED — read 2026-09-07 from
// the Claude Code headless docs and @anthropic-ai/claude-agent-sdk@0.3.263's
// sdk.d.ts against a daemon on claude 2.1.259. No capture of this line exists and
// none can be taken, because a refusal cannot be provoked without a prompt this repo
// should not contain. So every key is treated as optional: a type definition names
// keys the wire does not always send, the way `effort` is documented on system/init
// and absent from all 58 committed init lines. Nothing here is an observation.
//
// Every string is claude-derived and bounded by the producer AT CONSTRUCTION
// (streamsup's maxTaskFieldID / maxDenialProse), so an oversized payload never
// enters the event stream, a queue, or a log. Bounded and UTF-8-valid is ALL any of
// them is: nothing on this path strips control characters or terminal escape
// sequences, which is ModelAnnounced.Model's caveat and applies to all six fields.
// A client-facing slice owes the sanitization at its own render boundary.
type ModelRefusalFallback struct {
	// Scope is how long the fallback lasts: "session" means claude keeps the session
	// on FallbackModel, so a LATER ModelAnnounced will report a different model and
	// this event is the only thing that explains why; "local" means the retry was for
	// the one refused turn and the model reverts.
	//
	// AN OPEN SET carried verbatim, on TurnEnd.Outcome's rule — the two values above
	// are documented, not exhaustive, and a third is claude's to add.
	//
	// DROPPED rather than cut when over-cap, on maxTurnEndStopField's reasoning: a
	// consumer matches this token against the two values it knows, so a cut one
	// matches nothing while still looking like a scope. The drop IS reported — see
	// DroppedFields.
	Scope string
	// OriginalModel is the model that refused; FallbackModel is the one claude
	// retried on. Both are claude's identifiers carried VERBATIM per
	// ModelAnnounced.Model's rule: no lowercasing, no alias expansion, no
	// date-stamping, no family mapping, and no lookup against any published model
	// list. That doc states why the value is not reliably dated and need not appear
	// in any published list, which is an argument for carrying it untouched rather
	// than repairing it here.
	//
	// DROPPED rather than cut, on ToolCallDenied.ToolCallID's reasoning: a consumer
	// joins FallbackModel against the ModelAnnounced it already holds, so a cut label
	// joins to nothing while still looking like a real one — strictly worse than an
	// absent label, which the consumer can see.
	OriginalModel string
	FallbackModel string
	// RefusalCategory is claude's classification of what it refused — an OPEN string,
	// "cyber" and "bio" being the documented examples.
	//
	// IT IS CLAUDE'S ASSERTION ABOUT THE REQUEST, NEVER THE DAEMON'S FINDING, and the
	// distinction is load-bearing because this value is accusatory in a way no sibling
	// variant's fields are: it says a user's request drew that classification. The
	// daemon neither derives nor checks it. A mistaken or fabricated line therefore
	// attributes a category to a user who triggered none, which is
	// ToolCallDenied.ToolCallID's misattribution one degree worse. A consumer must
	// render it as claude's claim, attributed, and must not act on it.
	//
	// DROPPED rather than cut, on Scope's reasoning: a token, matched rather than read.
	RefusalCategory string
	// RefusalExplanation is claude's display-only prose about the refusal, and Banner
	// is the announcement text claude wrote for the swap itself (its `content` key —
	// named for the role the documentation gives it, which is documentation-derived
	// like every other statement about this line).
	//
	// CUT rather than dropped and reported in TruncatedFields, on
	// maxCompactField's reasoning: a cut sentence still reads as what it is.
	//
	// SECURITY: claude-authored, bounded by the daemon and NOT sanitized. These are
	// prose about a request that was REFUSED, so unlike ToolCallDenied.Message — which
	// describes a call the daemon made — they can quote or paraphrase the USER's own
	// words back out. They may also quote the command line or code claude declined to
	// produce. Safe to RENDER as text, never to execute or re-shell —
	// BackgroundTaskStarted.Description's rule, and the same hazard.
	RefusalExplanation string
	Banner             string
	// TruncatedFields names the fields the producer CUT to fit their caps, in
	// declaration order, using the DAEMON's snake_case names: "refusal_explanation",
	// "banner". nil when nothing was cut, never an empty non-nil slice, so a consumer
	// can emit it as absent rather than [].
	TruncatedFields []string
	// DroppedFields names the fields the producer EMPTIED for exceeding their caps, in
	// declaration order, under the daemon's names: "scope", "original_model",
	// "fallback_model", "refusal_category" — not claude's api_refusal_category, since
	// the report names the field it describes rather than the key it came from, which
	// is ToolCallDenied.DroppedFields' rule.
	//
	// Two report slices for ToolCallDenied's reason: four fields here drop, so an empty
	// Scope could not otherwise be told apart from a scope claude never sent. A field
	// named here was emptied BY THE DAEMON; a field empty and named in neither slice
	// was empty when claude sent it. That distinction is the whole of this type's
	// answer to a documentation-derived field set, where ANY key may simply be absent.
	DroppedFields []string
}

// ModelRefusalNoFallback reports that claude ended a turn with stop reason
// `refusal` and did not retry it on another model. It maps claude's
// system/model_refusal_no_fallback line (#2268) and is deliberately distinct from
// ModelRefusalFallback: one says the turn stopped, while the other says claude
// retried it.
//
// THIS IS CLAUDE'S ASSERTION, NOT A DAEMON FINDING. ModelAnnounced remains the
// authority on which model claude is running; this event only explains the cause of
// the stopped turn. Nothing in the daemon acts on these fields — no routing, retry,
// teardown, model selection, or session setting is keyed on them. A fabricated or
// mistaken line can therefore mislabel a refusal but cannot actuate the daemon.
//
// THE FIELD SET IS DOCUMENTATION-DERIVED, NOT CAPTURE-DERIVED. It was read
// 2026-09-07 from the Claude Code headless and Agent SDK documentation and
// @anthropic-ai/claude-agent-sdk@0.3.263's sdk.d.ts, against a daemon on claude
// 2.1.259. No capture exists or can safely be provoked, so every field is optional.
// An empty field named in neither report below means claude omitted or emptied it;
// a field named in a report means the daemon bounded it.
//
// request_id and refused_user_message_uuid are deliberately absent. The first is
// API-internal; the second names a claude message identity no daemon surface can
// join against. Retraction of the refused partial cannot be honoured on this wire.
//
// Every string is claude-derived and bounded by streamsup at construction. Bounded
// and UTF-8-valid is all the contract promises: control characters, terminal escape
// sequences, and markup are not sanitized. It opens and closes no turn.
type ModelRefusalNoFallback struct {
	// OriginalModel is the model that refused. It is carried verbatim, without alias
	// expansion or validation against a model list. Overflow DROPS rather than cuts
	// it, because a client matches the identifier and a cut token looks real while
	// joining to nothing.
	OriginalModel string
	// RefusalCategory is claude's open classification string, such as "cyber" or
	// "bio". It is claude's assertion about the request, never the daemon's finding,
	// and a consumer must attribute it accordingly. As a matched token it DROPS on
	// overflow rather than being cut.
	RefusalCategory string
	// RefusalExplanation is claude's display prose about the refusal; Banner is the
	// announcement text from claude's `content` key. Both may quote or paraphrase the
	// user's refused request, code, or command line. They are inert text: safe to
	// render with the consumer's sanitization, never to execute or re-shell.
	//
	// Both prose fields CUT on overflow because a shortened sentence remains useful.
	RefusalExplanation string
	Banner             string
	// TruncatedFields names CUT fields in declaration order using daemon names:
	// "refusal_explanation", then "banner". It is nil when nothing was cut.
	TruncatedFields []string
	// DroppedFields names DROPPED fields in declaration order using daemon names:
	// "original_model", then "refusal_category". It is nil when nothing was dropped.
	DroppedFields []string
}
