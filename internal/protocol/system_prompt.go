package protocol

// Conversation system-prompt READ payloads (#2152, docs/protocol-mobile.md
// § Reading a conversation's system prompt). The frame a client sends to ask what
// prompt one conversation holds, and the frame it is answered with. Wire
// vocabulary only: pure structs and their (de)serialization.
//
// Its own file rather than a home in conversations_write.go beside
// SetSystemPromptPayload, the write half (#2151): that file carries payloads that
// mutate the conversation RECORD, and this pair is a read of the prompt itself,
// with its own reply type and its own verdict vocabulary. settings.go, history.go
// and snapshot.go each own a frame family for the same reason.
//
// The handler that intercepts request_system_prompt, gates on the interactive
// capability, resolves the conversation and emits system_prompt is
// internal/relay's handleRequestSystemPrompt — NOT here.

// RequestSystemPromptPayload is the body of an Envelope whose Type ==
// TypeRequestSystemPrompt (docs/protocol-mobile.md § Reading a conversation's
// system prompt). Phone → binary direction; a client asking what system prompt one
// conversation holds and whether the running session was spawned with it.
//
// This is an inbound v2 *control* envelope, structurally like
// RequestSessionSettingsPayload (settings.go): the v2 session manager intercepts it
// at the dispatch boundary before dispatch.Route is called, so there is NO
// dispatch.Route handler for it. That is the one place it departs from its own
// write half, TypeSetSystemPrompt, which IS map-dispatched — a client sends the two
// verbs the same way, and the daemon routes them differently.
//
// ONE DIRECTION ONLY, phone → binary, so there is no provenance to disambiguate:
// EVERY FIELD IS AN UNVERIFIED CLAIM, ALWAYS. The frame it is answered with —
// SystemPromptPayload below — rides the other way and shares no field with it.
//
// THE ID IS A LOOKUP KEY, NEVER A VALUE TRUSTED AS SENT: it is resolved against the
// daemon's own conversations registry and is spent on nothing else. It reaches no
// reply, no log line, no error string and no filesystem path, and NAMING A
// CONVERSATION IS NOT AUTHORIZATION — authorization is pairing, enforced
// structurally at the Noise_IK handshake, plus the negotiated interactive
// capability the handler gates on.
//
// UNLIKE RequestHistoryPayload THE ID NEVER BECOMES A PATH COMPONENT. That single
// difference is why a decode failure is TOLERATED rather than rejected: it leaves
// ConversationID empty, which resolves nothing and is answered with the constant
// reply below, where an empty path component would have resolved to a directory
// root. Do not copy this tolerance to a verb that joins the id into a path.
//
// NO omitempty, matching RequestSessionSettingsPayload and RequestModelListPayload
// — their stated reason applies verbatim. There is no presence contract: absent and
// empty are the SAME case, "no conversation named", which names nothing and is
// answered with the constant reply, so nothing needs to tell them apart. Keeping the
// key always on the wire lets a fixture pin the full shape, and
// TestRequestSystemPromptPayload_ZeroValue_KeyPresent reddens if an omitempty is
// added later for tidiness.
//
// CORRELATION RIDES THE ENVELOPE'S InReplyTo, so the payload carries NO REQUEST-ID
// KEY — TypeAttachmentStored's decision, transferred unchanged.
type RequestSystemPromptPayload struct {
	// ConversationID names the conversation whose stored prompt is wanted. A
	// lookup key resolved against the daemon's registry, and not authorization;
	// the empty string names nothing and resolves nothing.
	ConversationID string `json:"conversation_id"`
}

// The three values SystemPromptPayload.SessionPromptStatus can carry (#2152). A
// CLOSED set: the daemon emits one of these three on every reply and never the
// empty string, so a client switches on three cases and has no fourth to guess at.
//
// The split exists because a stored prompt takes effect at the conversation's NEXT
// session start (set_system_prompt's own contract), so the stored value alone
// cannot tell an operator what the child they are typing at is actually running
// under. These three answer that, and only that.
//
// SystemPromptStatusNoSession MERGES several daemon states on purpose, and the
// merge is the reply's whole error handling: this daemon does not host the named
// conversation, it hosts one that is bound to no session, it hosts one bound to a
// session the pool no longer holds, the request named no conversation at all, or no
// resolver is wired (foreground / v1). All five mean the same thing to a client —
// there is no running session to compare against — and none of them is an error the
// client can repair by sending something different. Distinguishing the first would
// additionally make the verb a conversation-membership oracle.
const (
	SystemPromptStatusNoSession = "no_session" // nothing is running to compare the stored value against
	SystemPromptStatusMatches   = "matches"    // the running session was spawned with the stored value
	SystemPromptStatusDiffers   = "differs"    // the running session was spawned with a DIFFERENT value; the stored one applies at its next start
)

// SystemPromptPayload is the body of an Envelope whose Type == TypeSystemPrompt
// (docs/protocol-mobile.md § Reading a conversation's system prompt). Binary →
// phone direction; the daemon's answer to a request_system_prompt, correlated by
// InReplyTo.
//
// It is the READ half the #2151 cluster shipped without, and it carries the two
// things a client needs to render a prompt editor honestly: what is stored, and
// whether the child the operator is typing at is running under it.
//
// SystemPrompt IS A POINTER WITH omitempty, AND THAT PAIRING IS THE WHOLE CONTRACT.
// It mirrors conversations.Conversation.SystemPrompt's storage encoding field for
// field, and for that field's stated reason: omitempty on a pointer tests the
// POINTER, not the pointee, so nil omits the key while a non-nil pointer to "" still
// emits "system_prompt": "". The registry's three states therefore stay
// distinguishable on the wire —
//
//	absent key / null  no prompt is stored (the default)
//	""                 an explicitly empty prompt, a distinct stored state
//	any string         the stored text, verbatim, up to MaxSystemPromptBytes
//
// — which is what lets a client read a value and write it back through
// SetSystemPromptPayload, whose own SystemPrompt field is the same tri-state, without
// silently collapsing "explicitly empty" into "no prompt". A plain string with
// omitempty cannot express the split: both no-bytes states would serialize away and
// both would decode to "". Do NOT "fix" this to a plain string for symmetry with the
// neighbouring read payloads; TestSystemPromptPayload_TriStateSurvivesARoundTrip
// pins all three.
//
// SessionPromptStatus HAS NO omitempty and is ALWAYS one of the three constants
// above, never "". Unlike SessionSettingsPayload's permission_mode — whose "" is the
// no-session sentinel — this field spells that state out as "no_session", because
// this payload has no second all-zero field to read it beside.
//
// THE TWO FIELDS ARE INDEPENDENT and a client must not derive one from the other. A
// conversation storing text while nothing runs reports the text with "no_session"; a
// conversation storing nothing while a session runs that was spawned with nothing
// reports an absent key with "matches" — because Pool.SystemPromptFor collapses both
// no-bytes states to "" by design, so the daemon compares the COLLAPSED stored value
// and an explicitly empty prompt matches a session spawned with no operator text.
//
// THE SPAWNED-WITH TEXT IS DELIBERATELY NOT CARRIED. "differs" says the two
// disagree and stops there: echoing up to another 8192 bytes of operator text back
// over the wire to say so is what this shape rules out, and a client that wants to
// show a diff is a later ticket. Adding the field later is cheap and would need its
// own argument against the envelope cap.
//
// NO conversation_id EITHER, matching SessionSettingsPayload and unlike
// ModelListPayload. Correlation rides InReplyTo, and carrying an id would force a
// choice this reply refuses to make: an unhosted conversation has no
// registry-canonical id to report, and echoing back the client's own string is the
// shape every other verb here declines. Its absence is what lets the unhosted answer
// be byte-identical to the answer for a hosted conversation holding no prompt and
// running nothing.
type SystemPromptPayload struct {
	SystemPrompt        *string `json:"system_prompt,omitempty"`
	SessionPromptStatus string  `json:"session_prompt_status"`
}
