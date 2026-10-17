package protocol

import "time"

// Claude usage allowance frames are pure wire data, wired in
// handlers.RequestClaudeUsage. request_claude_usage uses authenticated
// paired-client map dispatch, with no conversation lookup and no
// interactive-capability gate, exactly as request_claude_account does.
//
// Each request is answered by exactly one unicast claude_usage correlated by
// Envelope.InReplyTo. The reading covers the whole Claude account and is held
// per daemon instance, in memory only. It is the latest rate_limit_event claude
// wrote on any Claude session, observed at ObservedAt; nothing refreshes it
// without a model call, and there is none until the first model call after
// daemon start.
//
// Every number is claude's own, verbatim and unvalidated: utilization has been
// observed as a 0-to-1 fraction but is not clamped, and a resets_at in the past
// passes through. An absent window or value is absent on the wire, never 0.

// CapabilityClaudeUsage lets a client detect that this daemon answers
// request_claude_usage. Detection only: it grants no access and gates nothing,
// and the verb is answered whether or not it was negotiated.
const CapabilityClaudeUsage = "claude_usage"

// ClaudeUsagePayload.State values. A client must accept an unknown state.
const (
	// ClaudeUsageStateNone means no reading has been stored since daemon start.
	ClaudeUsageStateNone = "none"
	// ClaudeUsageStateObserved means ObservedAt and at least one window are set.
	ClaudeUsageStateObserved = "observed"
)

// RequestClaudeUsagePayload is the body of request_claude_usage, client →
// daemon. It encodes as {}.
type RequestClaudeUsagePayload struct{}

// ClaudeUsagePayload is the body of claude_usage, daemon → client. State always
// serializes; ObservedAt and the windows are omitted in state none, and an
// observed reading omits a window claude's line did not carry.
type ClaudeUsagePayload struct {
	State string `json:"state"`
	// ObservedAt is the daemon's time when it read claude's line, not claude's.
	ObservedAt *time.Time         `json:"observed_at,omitempty"`
	FiveHour   *ClaudeUsageWindow `json:"five_hour,omitempty"`
	SevenDay   *ClaudeUsageWindow `json:"seven_day,omitempty"`
}

// ClaudeUsageWindow is one allowance window. Pointers with omitempty drop only
// an absent value, so a utilization of 0 serializes as 0.
type ClaudeUsageWindow struct {
	// Utilization is claude's utilization, as sent: observed as a fraction.
	Utilization *float64 `json:"utilization,omitempty"`
	// ResetsAt is claude's resetsAt in Unix seconds.
	ResetsAt *int64 `json:"resets_at,omitempty"`
}
