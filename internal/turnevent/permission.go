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
// member; #707 adds Cancel. The remaining deferred inbound commands (Prompt /
// DropQueued) join the same set when their tickets land. Cancel keeps the marker
// here beside its sibling — the relocation to a separate file hinted above is
// optional churn this slice declines.
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

// Cancel is an inbound command: stop the current turn (the neutral form of a
// remote Esc / ACP session/cancel, #600). Fieldless — the daemon has one live
// turn context, so no correlation id is needed yet; #600 adds a session/turn id
// additively if ACP needs one.
//
// Cancel is declared vocabulary in this slice: the mobile interrupt frame routes
// to Esc directly via the relay seam (internal/relay handleInterrupt), it does
// not construct a Cancel value — mirroring how the mobile modal_cancel frame
// routes to ModalResolver.ResolveCancel rather than building a PermissionResponse.
// The mobile-wire → neutral-Cancel translator is the ACP adapter's job (#600);
// Cancel exists now so #600 has its target.
type Cancel struct{}

// Pure value types, so each marker is on a value receiver: the zero value
// satisfies its sum type.
func (PermissionRequest) isTurnEvent() {}
func (PermissionResponse) isInbound()  {}
func (Cancel) isInbound()              {}

var (
	_ Event   = PermissionRequest{}
	_ Inbound = PermissionResponse{}
	_ Inbound = Cancel{}
)
