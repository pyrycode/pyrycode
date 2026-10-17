package protocol

import (
	"encoding/json"
	"errors"
)

// Query limits count ordered rows, excluding ties and required extras.
const (
	DefaultThreadQueryLimit = 50
	MaxThreadQueryLimit     = 256
)

// ErrInvalidThreadQueryLimit rejects negative limits without exposing input.
var ErrInvalidThreadQueryLimit = errors.New("protocol: invalid thread query limit")

// ThreadCatchUpPayload requests full changed/active items and parent closure.
// Version zero selects the newest window; nonzero catch-up is never truncated.
// Normalize Limit even for nonzero catch-up, which rejects negative values.
type ThreadCatchUpPayload struct {
	ConversationID string `json:"conversation_id"`
	Epoch          string `json:"epoch"`
	Version        uint64 `json:"version"`
	Limit          int    `json:"limit,omitempty"`
	// Projection also selects this conversation's live view; omission is complete.
	Projection string `json:"projection,omitempty"`
}

// ThreadPagePayload requests the ordered window below exclusive BeforeOrder.
// Limits count ordered rows; ties and parent extras may exceed them.
type ThreadPagePayload struct {
	ConversationID string `json:"conversation_id"`
	BeforeOrder    uint64 `json:"before_order"`
	Limit          int    `json:"limit,omitempty"`
	Projection     string `json:"projection,omitempty"`
	Epoch          string `json:"epoch,omitempty"` // Required for compact pages.
}

// NormalizeThreadQueryLimit defaults omitted/zero limits and clamps positives.
// Handlers must call it before selecting either window or nonzero catch-up.
func NormalizeThreadQueryLimit(limit int) (int, error) {
	if limit < 0 {
		return 0, ErrInvalidThreadQueryLimit
	}
	if limit == 0 {
		return DefaultThreadQueryLimit, nil
	}
	return min(limit, MaxThreadQueryLimit), nil
}

// ThreadItemsPayload is a row batch or a separate empty success marker.
// Only Final certifies coverage. Nonzero catch-up markers carry FromVersion for
// (FromVersion, Version]; window/page markers carry all four page fields, even
// zero/false. The range is [LowerOrder, UpperOrder), Continuation equals LowerOrder,
// and OlderExists is authoritative. Extras never expand that range.
// Ordinary batches omit all coverage fields and explicitly emit Final false.
// An object continuation selects ThreadUpdatePart assembly instead of this DTO;
// its final flag completes assembly only, never request coverage.
// Compact projection puts full public non-tool/agent roots in Items and public
// tool/agent roots in Headers. Order/ID determine placement across both arrays.
// Compact coverage certifies only that projection's root window/source interval,
// never complete inventory or detail. All data, parts and markers repeat scope.
// Errors, disconnects and partial replies advance no completed checkpoint.
type ThreadItemsPayload struct {
	ConversationID string       `json:"conversation_id"`
	Epoch          string       `json:"epoch"`
	Version        uint64       `json:"version"`
	Items          []ThreadItem `json:"items"`
	Final          bool         `json:"final"`
	FromVersion    *uint64      `json:"from_version,omitempty"`
	LowerOrder     *uint64      `json:"lower_order,omitempty"`
	UpperOrder     *uint64      `json:"upper_order,omitempty"`
	Continuation   *uint64      `json:"continuation,omitempty"`
	OlderExists    *bool        `json:"older_exists,omitempty"`

	Projection string          `json:"projection,omitempty"`
	Headers    []ThreadSummary `json:"headers,omitempty"`
}

func (p ThreadItemsPayload) MarshalJSON() ([]byte, error) {
	type wire ThreadItemsPayload
	if p.Items == nil {
		p.Items = []ThreadItem{}
	}
	return json.Marshal(wire(p))
}

// ThreadResetPayload terminates a correlated query without certifying partial
// items or advancing a completed checkpoint. ErrorPayload is terminal failure.
type ThreadResetPayload struct {
	ConversationID string `json:"conversation_id"`
	Epoch          string `json:"epoch"`
	Version        uint64 `json:"version"`
	Projection     string `json:"projection,omitempty"`
}
