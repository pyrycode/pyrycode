// Package turnevent defines the neutral, daemon-owned outbound turn-event
// model for Phase 2 structured streaming (EPIC #596).
//
// These are pure value types — no transport, no I/O, standard library only.
// They are the stable internal contract that the event-stream bridge (#608)
// maps tui-driver Events() INTO and that the v2 wire types (#607) map OUT of.
// The mobile wire (now) and the future pyry acp adapter (#600) are thin
// adapters over this one model: it is shaped ~90% like ACP so the ACP adapter
// is near pass-through, but it is owned by us — so churn in the external ACP
// spec stays inside the ACP adapter and never reaches the daemon core or the
// mobile wire. Same containment logic the tui-driver substrate seal applies to
// claude's screen.
//
// This is the outbound turn-event core only. No Events() draining and no
// envelope mapping live here. Inbound commands (Prompt, Cancel, …) and the
// internal-only BusyState event are out of scope and get a home in a later
// ticket. The internal-only Stall event now lives here (mobile-sent,
// ACP-dropped); see its type doc.
package turnevent

// Event is the sealed sum type of outbound turn events: TextChunk,
// ThoughtChunk, ToolStart, ToolUpdate, ToolProgress, TurnEnd, BackgroundTaskStarted,
// BackgroundTaskUpdated, BackgroundTaskRoster, BackgroundTaskProgress,
// ThinkingProgress, ContextUsage, MCPStatus, the
// internal-only status peers Stall, ApiRetry, and Compacting, the compaction
// boundary CompactionBoundary, and the diagnostic marker Unrecognized.
// The unexported marker
// keeps the variant set
// closed to this package, so external ACP-spec churn cannot inject a variant.
// The bridge (#608) ranges a stream of Event and the wire adapter (#607)
// type-switches to map each kind.
type Event interface{ isTurnEvent() }

// UnrecognizedSite names WHERE in the stream-json line mapping a payload was
// found that the parser has no mapping for. String-backed so the producer's call
// site is enum-safe and the value crosses the wire unchanged.
type UnrecognizedSite string

const (
	// UnrecognizedLineType is a top-level stream-json line whose `type` is
	// neither mapped nor on the measured known-ignored list.
	UnrecognizedLineType UnrecognizedSite = "line_type"
	// UnrecognizedAssistantBlock is an assistant message content block whose
	// `type` is not text, thinking, or tool_use.
	UnrecognizedAssistantBlock UnrecognizedSite = "assistant_block"
	// UnrecognizedUserBlock is a user message content block whose `type` is not
	// tool_result.
	UnrecognizedUserBlock UnrecognizedSite = "user_block"
	// UnrecognizedUndecodable is a line or block that failed to JSON-decode at
	// all. Kind is empty for this site — there is no type to report.
	UnrecognizedUndecodable UnrecognizedSite = "undecodable"
	// UnrecognizedCodexMethod is a Codex app-server notification whose method
	// codexsup's translator neither maps nor ignores. Kind is the method.
	UnrecognizedCodexMethod UnrecognizedSite = "codex_method"
	// UnrecognizedCodexItem is a Codex thread item whose type the translator
	// neither maps nor ignores. Kind is the item type.
	UnrecognizedCodexItem UnrecognizedSite = "codex_item"
)

// Unrecognized is a diagnostic marker: the stream-json parser met a payload it
// has no mapping for and dropped it. It exists so genuinely unknown claude
// output becomes VISIBLE the moment it arrives, instead of vanishing into a
// debug log the production daemon does not print.
//
// It is deliberately NOT the parser's tolerate-and-drop path. Every UNMAPPED
// system subtype stays silent; only output outside that measured set reaches
// here. A row per turn would make the feature worthless noise, so the
// known-ignored list is the whole design.
//
// CORRECTED 2026-08-09 (#1404): rate_limit_event is no longer named among the
// silent set, because it is no longer ON the known-ignored list — it has its own
// arm in the parser's main switch (→ RateLimited above). A rate_limit_event line
// the parser's gate does not map is still silent, but now because that arm
// CONSUMES it rather than because a list says to, which makes this lane
// unreachable for the type by matching rather than by list membership.
//
// CORRECTED 2026-08-07 (#1380): system is no longer ignored WHOLESALE, so this
// no longer reads "system/*, rate_limit_event … exactly as before". The system
// subtypes streamsup maps become their own variants instead — BackgroundTaskStarted
// is the first. That changed what is SENT, not what is DRAWN, so the 2026-07-27
// noise measurement behind the drop rule stands. streamsup's ignoredLineTypes
// carries the full statement and its emitSystemSubtype is the one enumeration of
// the mapped set; a prose pointer, not an import, because turnevent must not
// depend on streamsup.
//
// Raw is a plain string, not json.RawMessage, because the producer truncates it
// at construction: a truncated blob is no longer valid JSON, so typing it as raw
// JSON would be a lie. Truncated says whether that happened. Like every variant
// here it carries no conversation identity — the bridge injects that.
type Unrecognized struct {
	// Site is where the drop happened.
	Site UnrecognizedSite
	// Kind is the message or block `type` that had no mapping. Empty when Site
	// is UnrecognizedUndecodable (nothing decoded, so no type was ever read).
	Kind string
	// Raw is the offending JSON, already truncated by the producer.
	Raw string
	// Truncated reports whether Raw was cut to fit the producer's cap.
	Truncated bool
}

// The events are pure value types, so each marker is implemented on a value
// receiver: TextChunk{}, not only &TextChunk{}, satisfies Event.
// UserEcho is claude replaying a user message at the point it read it, under
// --replay-user-messages (#2730). Every turn's opening message echoes this way,
// and so does a message written into a running turn, right after the tool result
// it followed. It carries only the SHA-256 of the echoed text, never the text:
// that text is the DELIVERY payload, which for an attachment-bearing message names
// on-host paths, so no consumer of this stream can put it on the wire or in a log.
// The daemon matches the digest against what it wrote to place the operator's own
// message push.
//
// It opens and closes no turn, and no client frame is built from it.
type UserEcho struct {
	TextSHA256 [32]byte
}

// PromptSuggestion is claude's own suggested next prompt (#2829), read off a
// top-level prompt_suggestion line, which claude emits after the turn's result
// when prompt suggestions are enabled. Reusing it saves the daemon a model call
// of its own.
//
// Text is claude-authored and UNTRUSTED. The producer has checked its shape —
// a non-blank string of at most 1024 valid UTF-8 bytes holding no line break —
// and carries it verbatim, with no trimming or other repair. It has NOT vetted
// the content: control characters and bidi marks other than line breaks pass
// through, and the text is model output, not an operator's words.
//
// It opens and closes no turn and names no turn: claude's uuid and session_id
// on the line establish no daemon turn attribution, so neither is carried. The
// bridge supplies the session, like every variant here.
type PromptSuggestion struct {
	Text string
}

func (TextChunk) isTurnEvent()              {}
func (ThoughtChunk) isTurnEvent()           {}
func (ToolStart) isTurnEvent()              {}
func (ToolUpdate) isTurnEvent()             {}
func (ToolProgress) isTurnEvent()           {}
func (TurnEnd) isTurnEvent()                {}
func (BackgroundTaskStarted) isTurnEvent()  {}
func (BackgroundTaskUpdated) isTurnEvent()  {}
func (BackgroundTaskRoster) isTurnEvent()   {}
func (BackgroundTaskProgress) isTurnEvent() {}
func (ThinkingProgress) isTurnEvent()       {}
func (RateLimited) isTurnEvent()            {}
func (ModelAnnounced) isTurnEvent()         {}
func (SessionFacts) isTurnEvent()           {}
func (ModelList) isTurnEvent()              {}
func (ContextUsage) isTurnEvent()           {}
func (MCPStatus) isTurnEvent()              {}
func (SlashCommandList) isTurnEvent()       {}
func (Stall) isTurnEvent()                  {}
func (ApiRetry) isTurnEvent()               {}
func (Compacting) isTurnEvent()             {}
func (CompactionBoundary) isTurnEvent()     {}
func (ToolCallDenied) isTurnEvent()         {}
func (ModelRefusalFallback) isTurnEvent()   {}
func (ModelRefusalNoFallback) isTurnEvent() {}
func (Banner) isTurnEvent()                 {}
func (ConversationReset) isTurnEvent()      {}
func (UserEcho) isTurnEvent()               {}
func (PromptSuggestion) isTurnEvent()       {}
func (Unrecognized) isTurnEvent()           {}

var (
	_ Event = TextChunk{}
	_ Event = ThoughtChunk{}
	_ Event = ToolStart{}
	_ Event = ToolUpdate{}
	_ Event = ToolProgress{}
	_ Event = TurnEnd{}
	_ Event = BackgroundTaskStarted{}
	_ Event = BackgroundTaskUpdated{}
	_ Event = BackgroundTaskRoster{}
	_ Event = BackgroundTaskProgress{}
	_ Event = ThinkingProgress{}
	_ Event = RateLimited{}
	_ Event = ModelAnnounced{}
	_ Event = SessionFacts{}
	_ Event = SlashCommandList{}
	_ Event = Stall{}
	_ Event = ApiRetry{}
	_ Event = Compacting{}
	_ Event = ModelRefusalNoFallback{}
	_ Event = ConversationReset{}
	_ Event = UserEcho{}
	_ Event = PromptSuggestion{}
	_ Event = Unrecognized{}
)
