package turnevent

// ToolCallDenied reports that claude refused a tool call it was about to make.
// It maps claude's system/permission_denied line. Without it a client cannot
// tell a blocked call from one that ran and failed, because claude also writes
// the rejection text into an is_error tool_result. The line does not arrive in
// the daemon's production launch posture, so streamsup also recovers one marker
// per denied id no line announced from the `result` line's permission_denials,
// carrying ToolName and ToolCallID only.
//
// The name is deliberately not PermissionDenied, which would read as the
// negative answer of the PermissionRequest/PermissionResponse modal pair; this
// event reports claude refusing a call, whatever decided it. It opens and closes
// no turn.
//
// Every field is claude's, and so is the fact of the denial: a mistaken or
// fabricated line can name a call that ran and succeeded. The daemon does not
// verify the id against calls it saw, since that needs parser state and would
// drop a denial whose call preceded a session rotation. Instead the join is the
// client's (an id matching no seen call renders unattributed), and nothing in
// the daemon acts on any field.
//
// Every string is bounded at construction (streamsup's maxTaskFieldID and
// maxDenialProse). Tokens and ids are dropped past the cap and reported in
// DroppedFields; prose is cut and reported in TruncatedFields.
type ToolCallDenied struct {
	// ToolName is claude's name for the refused tool ("Bash" in all seven
	// captured lines): an open set carried verbatim, on TurnEnd.Outcome's rule.
	// Dropped rather than cut, since a consumer switches on it.
	ToolName string
	// ToolCallID is the refused call: claude's tool_use_id, under the name
	// ToolStart and ToolUpdate use, so a consumer joins the three directly. All
	// seven captured denials share their id with the tool_use before and the
	// tool_result after. Dropped rather than cut, because a cut join key matches
	// nothing while looking real (ModelWindow.ModelID's rule); the older
	// BackgroundTaskStarted.ToolCallID cuts the same identifier instead.
	ToolCallID string
	// Message is claude's rejection text, the prose it also writes into the
	// tool_result. Every captured line is a sandbox refusal naming the absolute
	// host paths of the session's allowed working directories. Cut rather than
	// dropped, since a cut sentence still reads as what it is.
	//
	// Security: claude-authored, bounded and not sanitized. A rule-based denial
	// may quote the refused command line. Safe to render as text, never to
	// execute or re-shell.
	Message string
	// DecisionReasonType is claude's word for what denied the call: "classifier",
	// "asyncAgent", "mode" and "rule" are documented (SDKPermissionDeniedMessage
	// in @anthropic-ai/claude-agent-sdk@0.3.263). Absent in all seven captured
	// lines at claude 2.1.239, as
	// TestParser_DenialCaptureYieldsEmptyDecisionReasons pins; decoded anyway
	// because the consumer wants it. Dropped rather than cut, on ToolName's rule.
	DecisionReasonType string
	// DecisionReason is claude's free-form explanation, absent in all seven
	// captured lines. Cut, reported and treated as Message is.
	DecisionReason string
	// TruncatedFields names cut fields ("message", "decision_reason") per
	// BackgroundTaskStarted.TruncatedFields.
	TruncatedFields []string
	// DroppedFields names the fields the producer emptied for exceeding their
	// caps ("tool_name", "tool_call_id", "decision_reason_type"), in declaration
	// order by daemon name; nil when nothing was dropped. Three fields here can
	// drop, so without it an empty value could not be told from one claude never
	// sent. A field named here was emptied by the daemon; an empty field named in
	// neither report was empty as claude sent it.
	DroppedFields []string
}

// ModelRefusalFallback reports that a turn ended with stop reason refusal, that
// claude retried it on a fallback model and, when Scope says so, that the
// session stays on that model. It maps claude's system/model_refusal_fallback
// line; without it a client sees the model change on the next ModelAnnounced
// with nothing explaining it. The name avoids "swapped" because a local-scope
// fallback is only a one-turn retry. It opens and closes no turn.
//
// It is claude's announcement of what it will run next; ModelAnnounced stays
// the authority on what it is running. Read the cause here and the result
// there: a client that updates its model state from FallbackModel trusts a
// prediction as an observation.
//
// Every field is claude's, and so is the fact of the fallback. The daemon has
// nothing to verify it against, and nothing in the daemon acts on any field (no
// routing, retry, model selection or session setting).
//
// The field set is documentation-derived, not capture-derived: read from the
// Claude Code headless docs and @anthropic-ai/claude-agent-sdk@0.3.263's
// sdk.d.ts against claude 2.1.259. No capture exists, because a refusal cannot
// be provoked without a prompt this repo should not contain, so every key is
// treated as optional. Five keys are not on the decode target: trigger and
// direction restate the subtype, request_id is API-internal, and
// retracted_message_uuids and refused_user_message_uuid name claude message ids
// no daemon surface can join, so a consumer cannot honour the retraction of the
// refused partial response.
//
// Every string is bounded at construction (streamsup's maxTaskFieldID and
// maxDenialProse) and valid UTF-8, nothing more: nothing strips control
// characters or terminal escapes, so a client owes the sanitization at its
// render boundary.
type ModelRefusalFallback struct {
	// Scope is how long the fallback lasts: "session" keeps the session on
	// FallbackModel, so a later ModelAnnounced reports a different model and this
	// event explains why; "local" retries only the refused turn. Documented, not
	// exhaustive: an open set on TurnEnd.Outcome's rule. Dropped rather than cut,
	// since a consumer matches it.
	Scope string
	// OriginalModel is the model that refused; FallbackModel is the one claude
	// retried on. Both verbatim per ModelAnnounced.Model's rule. Dropped rather
	// than cut, on ToolCallDenied.ToolCallID's rule: a consumer joins
	// FallbackModel against the ModelAnnounced it holds.
	OriginalModel string
	FallbackModel string
	// RefusalCategory is claude's open classification of what it refused
	// ("cyber" and "bio" are documented examples). It is claude's assertion about
	// a user's request, never the daemon's finding, and it is accusatory: a
	// mistaken line attributes a category to a user who triggered none. Render it
	// attributed to claude and never act on it. Dropped rather than cut, as a
	// matched token.
	RefusalCategory string
	// RefusalExplanation is claude's display prose about the refusal, and Banner
	// is the announcement text claude wrote for the swap (its `content` key, named
	// for its documented role). Both are cut rather than dropped.
	//
	// Security: claude-authored, bounded and not sanitized. Prose about a refused
	// request can quote or paraphrase the user's own words, or the command or code
	// claude declined to produce. Safe to render as text, never to execute or
	// re-shell.
	RefusalExplanation string
	Banner             string
	// TruncatedFields names cut fields ("refusal_explanation", "banner") per
	// BackgroundTaskStarted.TruncatedFields.
	TruncatedFields []string
	// DroppedFields names dropped fields ("scope", "original_model",
	// "fallback_model", "refusal_category", not claude's api_refusal_category)
	// per ToolCallDenied.DroppedFields. With a documentation-derived field set any
	// key may be absent, so the two reports are what separate an empty value
	// claude sent from one the daemon emptied.
	DroppedFields []string
}

// ModelRefusalNoFallback reports that claude ended a turn with stop reason
// refusal and did not retry it on another model. It maps claude's
// system/model_refusal_no_fallback line and is deliberately distinct from
// ModelRefusalFallback: this turn stopped, where that one was retried.
//
// It is claude's assertion, not a daemon finding, and ModelAnnounced remains the
// authority on which model claude is running. Nothing in the daemon acts on
// these fields (no routing, retry, teardown, model selection or session
// setting), so a fabricated or mistaken line can mislabel a refusal but cannot
// actuate the daemon.
//
// The field set is documentation-derived from the same sources as
// ModelRefusalFallback's, so every field is optional. An empty field named in
// neither report below means claude omitted or emptied it; a field named in a
// report means the daemon bounded it. request_id and refused_user_message_uuid
// are not on the decode target, so the refused partial cannot be retracted on
// this wire.
//
// Every string is bounded at construction (streamsup's maxTaskFieldID and
// maxDenialProse) and valid UTF-8, nothing more: control characters, terminal
// escapes and markup are not sanitized. It opens and closes no turn.
type ModelRefusalNoFallback struct {
	// OriginalModel is the model that refused, verbatim, without alias expansion
	// or validation against a model list. Dropped rather than cut, because a
	// client matches the identifier and a cut token looks real while joining to
	// nothing.
	OriginalModel string
	// RefusalCategory is claude's open classification string, such as "cyber" or
	// "bio": claude's assertion about the request, never the daemon's finding, so
	// a consumer must attribute it accordingly. Dropped rather than cut, as a
	// matched token.
	RefusalCategory string
	// RefusalExplanation is claude's display prose about the refusal; Banner is
	// the announcement text from claude's `content` key. Both may quote or
	// paraphrase the user's refused request, code or command line. Inert text:
	// render it with the consumer's sanitization, never execute or re-shell it.
	// Both are cut rather than dropped, since a shortened sentence stays useful.
	RefusalExplanation string
	Banner             string
	// TruncatedFields names cut fields ("refusal_explanation", "banner") per
	// BackgroundTaskStarted.TruncatedFields.
	TruncatedFields []string
	// DroppedFields names dropped fields ("original_model", "refusal_category")
	// per ToolCallDenied.DroppedFields.
	DroppedFields []string
}
