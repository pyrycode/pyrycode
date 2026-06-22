package turnevent

// PermissionRequest is an outbound Event: the daemon asks the consumer (phone /
// ACP client) to answer a permission modal, correlated to its inbound
// PermissionResponse by RequestID. ACP's session/request_permission (#600) maps
// onto this same shape, so it is adapter-neutral, not mobile-specific.
//
// It references the gating tool call by ToolCallID — the same by-id reference
// style ToolUpdate uses — rather than re-embedding tool details; the consumer
// already saw the matching ToolStart in the stream. ToolCallID is empty when a
// permission prompt has no backing tool call.
type PermissionRequest struct {
	RequestID  string             // correlates request <-> response
	ToolCallID string             // the tool call this gates; "" if not tool-triggered
	Title      string             // human-readable prompt text (e.g. "Do you want to proceed?")
	Options    []PermissionOption // ordered; the consumer renders + selects from these
}

// PermissionOption is one selectable answer in a PermissionRequest. Order within
// PermissionRequest.Options is significant — the consumer renders in order.
type PermissionOption struct {
	ID    string               // referenced by PermissionResponse.OptionID
	Label string               // human-readable ("Yes", "No, and don't ask again")
	Kind  PermissionOptionKind // the ACP semantic kind
}

// NewPermissionRequest assembles a PermissionRequest from its parts. It does NOT
// validate — constructing an invalid one (e.g. an option with an out-of-taxonomy
// Kind) is allowed and caught downstream via PermissionOptionKind.Valid(),
// matching the package's construct-then-validate-downstream convention.
func NewPermissionRequest(requestID, toolCallID, title string, options []PermissionOption) PermissionRequest {
	return PermissionRequest{
		RequestID:  requestID,
		ToolCallID: toolCallID,
		Title:      title,
		Options:    options,
	}
}

// Inbound is the sealed sum type of inbound commands the consumer sends back to
// the daemon. The unexported marker keeps the variant set closed to this
// package, mirroring Event and ToolContent. PermissionResponse is the first
// member; the deferred inbound-commands ticket adds Prompt / Cancel / DropQueued
// here (and may relocate this marker to its own file when it does).
type Inbound interface{ isInbound() }

// PermissionResponse is an inbound command answering a PermissionRequest, matched
// by RequestID. Exactly one outcome is meaningful: either OptionID names the
// selected PermissionOption.ID, or Cancelled is true (the consumer dismissed the
// modal without selecting). The package does not enforce that exclusivity —
// validation is the inbound parser's job, consistent with the package's stance.
type PermissionResponse struct {
	RequestID string // the PermissionRequest this answers
	OptionID  string // the selected PermissionOption.ID; "" when Cancelled
	Cancelled bool   // true => the consumer dismissed the modal without selecting
}

// Pure value types, so each marker is on a value receiver: the zero value
// satisfies its sum type.
func (PermissionRequest) isTurnEvent() {}
func (PermissionResponse) isInbound()  {}

var (
	_ Event   = PermissionRequest{}
	_ Inbound = PermissionResponse{}
)
