package protocol

// CapabilityThreadCompact declares compact support separately from expansion.
// Vocabulary alone enables neither negotiation, handlers nor live publication.
const CapabilityThreadCompact = "thread_compact"

// ThreadProjectionCompact selects public roots with full non-tool/agent content
// and tool/agent headers. Omitted projection selects complete delivery. Selection
// is connection-and-conversation scoped, covering queries and subsequent live
// output. First selection or incompatible view change requires version-zero
// newest-window initialization. Reconnect resumes only successfully completed
// coverage with matching conversation/projection/epoch; other coverage is unusable.
// Unknown projections require refusal, never silent complete-view fallback.
const ThreadProjectionCompact = "compact-v1"

// ThreadCompactUpdatePayload declares a full root replacement or hidden-only
// Progress for the exclusive source interval (FromVersion, Version]. Exactly one
// of Item, Header or Progress is supplied. Item retains complete non-tool/agent
// content; Header carries tool/agent metadata only. Rows must be public roots.
// Progress explicitly accounts for consumed entries producing no root change.
// These intervals never certify complete inventory or downloaded subtree detail.
// Consumers apply only in matching scope after initialization; gaps require
// catch-up. Repeated rows replace by source identity, without opening descendants.
// Intervals may overlap when one consumed entry changes several roots: equal
// versions for different identities must not be discarded. Only a successful
// query marker establishes a completed recovery checkpoint.
type ThreadCompactUpdatePayload struct {
	Projection     string         `json:"projection"`
	ConversationID string         `json:"conversation_id"`
	Epoch          string         `json:"epoch"`
	FromVersion    uint64         `json:"from_version"`
	Version        uint64         `json:"version"`
	Item           *ThreadItem    `json:"item,omitempty"`
	Header         *ThreadSummary `json:"header,omitempty"`
	Progress       bool           `json:"progress"`
}
