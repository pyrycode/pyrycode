// Package turnevent defines the daemon-owned, neutral model of outbound turn
// events, plus the inbound commands in permission.go.
//
// The types are pure values: no transport, no I/O, standard library only
// (TestImportBoundary_StdlibOnly). Producers map claude's stream-json output
// (internal/streamsup) and Codex app-server output (internal/codexsup) into this
// model, and turnbridge.MapEvent maps it onto the internal/protocol wire types.
// The model is shaped closely on ACP so an ACP adapter can stay thin, but the
// daemon owns it, so ACP spec churn stays in that adapter and never reaches the
// daemon core or the mobile wire.
//
// No variant carries the daemon's conversation identity; the consumer injects it
// when mapping to the wire. Where a variant carries claude's session_id or uuid,
// its doc says so; otherwise both are deliberately absent, because claude's
// session is not the daemon's conversation. See
// docs/knowledge/features/turnevent-package.md.
package turnevent

// Event is the sealed sum type of outbound turn events. The isTurnEvent markers
// below and in permission.go list every variant, and the unexported marker keeps
// the set closed to this package.
//
// Consumers type-switch on it: turnbridge.MapEvent, and cmd/pyry's turnMarkFor,
// eventKind and interactiveTurnEmitterV2.Handle. Only turnMarkFor is checked for
// totality (TestTurnMarkFor_TotalOverEveryVariant reads these markers), so a new
// variant means checking every switch by hand; see turnevent-package.md § The
// three sum-type seams.
type Event interface{ isTurnEvent() }

// UnrecognizedSite names where in the producer's mapping an unmapped payload was
// found. String-backed so the value crosses the wire unchanged.
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
	// all. Kind is empty for this site: there is no type to report.
	UnrecognizedUndecodable UnrecognizedSite = "undecodable"
	// UnrecognizedCodexMethod is a Codex app-server notification whose method
	// codexsup's translator neither maps nor ignores. Kind is the method.
	UnrecognizedCodexMethod UnrecognizedSite = "codex_method"
	// UnrecognizedCodexItem is a Codex thread item whose type the translator
	// neither maps nor ignores. Kind is the item type.
	UnrecognizedCodexItem UnrecognizedSite = "codex_item"
)

// Unrecognized is a diagnostic marker: a producer met a payload it has no
// mapping for and dropped it. It exists so unknown claude or Codex output is
// visible when it arrives, not lost in a debug log production does not print.
//
// It is not the tolerate-and-drop path. Line types on the measured known-ignored
// list (streamsup's ignoredLineTypes) stay silent, and the system subtypes
// streamsup maps (emitSystemSubtype) become their own variants; only output
// outside both sets lands here. A row per turn would make the signal noise, so
// the known-ignored list is the design. These are prose pointers because
// turnevent must not import streamsup.
//
// Raw is a plain string, not json.RawMessage, because the producer truncates it
// at construction and a truncated blob is no longer valid JSON.
type Unrecognized struct {
	// Site is where the drop happened.
	Site UnrecognizedSite
	// Kind is the message or block `type` that had no mapping. Empty when Site
	// is UnrecognizedUndecodable.
	Kind string
	// Raw is the offending JSON, already cut to the producer's
	// maxUnrecognizedRaw.
	Raw string
	// Truncated reports whether Raw was cut.
	Truncated bool
}

// UserEcho is claude replaying a user message at the point it read it, under
// --replay-user-messages: every turn's opening message, and a message written
// into a running turn, right after the tool result it followed. The daemon
// matches the digest against what it wrote to place the operator's own message
// push.
//
// It carries only the SHA-256 of the echoed text, never the text. That text is
// the delivery payload, which for an attachment-bearing message names on-host
// paths, so no consumer of this stream can put it on the wire or in a log. It
// opens and closes no turn, and no client frame is built from it.
type UserEcho struct {
	TextSHA256 [32]byte
}

// PromptSuggestion is claude's own suggested next prompt, read off a top-level
// prompt_suggestion line that claude emits after the turn's result when prompt
// suggestions are enabled. Reusing it saves the daemon a model call of its own.
//
// Text is claude-authored and untrusted. The producer checks its shape (a
// non-blank string of at most maxPromptSuggestionBytes valid UTF-8 bytes with no
// line break) and carries it verbatim, with no trimming or repair. It does not
// vet the content: control characters and bidi marks other than line breaks
// pass through, and the text is model output, not the operator's words.
//
// It opens and closes no turn and names none: claude's uuid and session_id on
// the line establish no daemon turn, so neither is carried.
type PromptSuggestion struct {
	Text string
}

// The events are pure value types, so each marker is implemented on a value
// receiver: TextChunk{}, not only &TextChunk{}, satisfies Event.
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
