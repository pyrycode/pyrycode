package turnevent

import (
	"reflect"
	"testing"
)

// NewPermissionRequest assembles every field, preserving option order and each
// option's ID / Label / Kind.
func TestNewPermissionRequest_FieldRoundTrip(t *testing.T) {
	t.Parallel()

	opts := []PermissionOption{
		{ID: "opt-allow", Label: "Yes", Kind: PermissionOptionKindAllowOnce},
		{ID: "opt-reject", Label: "No, and don't ask again", Kind: PermissionOptionKindRejectAlways},
	}
	got := NewPermissionRequest("req1", "tc1", "Do you want to proceed?", opts)
	want := PermissionRequest{
		RequestID:  "req1",
		ToolCallID: "tc1",
		Title:      "Do you want to proceed?",
		Options: []PermissionOption{
			{ID: "opt-allow", Label: "Yes", Kind: PermissionOptionKindAllowOnce},
			{ID: "opt-reject", Label: "No, and don't ask again", Kind: PermissionOptionKindRejectAlways},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewPermissionRequest: got %+v, want %+v", got, want)
	}
}

// PermissionRequest is an outbound Event, recoverable from a heterogeneous
// stream by type switch — the same seam the bridge (#608) relies on.
func TestPermissionRequest_IsEvent(t *testing.T) {
	t.Parallel()

	var _ Event = PermissionRequest{} // compile-time membership

	stream := []Event{
		TextChunk{MessageID: "m1", Text: "a"},
		NewPermissionRequest("req1", "tc1", "Proceed?", nil),
	}
	switch stream[1].(type) {
	case PermissionRequest:
		// recovered
	default:
		t.Fatalf("stream[1] (%T) did not type-switch to PermissionRequest", stream[1])
	}
}

// PermissionResponse carries the selected option id (the selected case).
func TestPermissionResponse_Selected(t *testing.T) {
	t.Parallel()

	got := PermissionResponse{RequestID: "req1", OptionID: "opt-allow", Cancelled: false}
	if got.RequestID != "req1" || got.OptionID != "opt-allow" || got.Cancelled {
		t.Errorf("selected PermissionResponse: got %+v", got)
	}
}

// PermissionResponse with Cancelled set carries no option id (the cancelled case).
func TestPermissionResponse_Cancelled(t *testing.T) {
	t.Parallel()

	got := PermissionResponse{RequestID: "req1", Cancelled: true}
	if got.OptionID != "" {
		t.Errorf("cancelled PermissionResponse: OptionID = %q, want empty", got.OptionID)
	}
	if !got.Cancelled {
		t.Errorf("cancelled PermissionResponse: Cancelled = false, want true")
	}
}

// PermissionResponse is an inbound command, recoverable from a []Inbound by
// type switch.
func TestPermissionResponse_IsInbound(t *testing.T) {
	t.Parallel()

	var _ Inbound = PermissionResponse{} // compile-time membership

	stream := []Inbound{PermissionResponse{RequestID: "req1", OptionID: "opt-allow"}}
	switch stream[0].(type) {
	case PermissionResponse:
		// recovered
	default:
		t.Fatalf("stream[0] (%T) did not type-switch to PermissionResponse", stream[0])
	}
}

// Cancel is an inbound command (#707), recoverable from a []Inbound by type
// switch — the same seam PermissionResponse rides. The ACP adapter (#600) is the
// only future producer of a Cancel value; the mobile interrupt path routes to
// Esc directly without constructing one.
func TestCancel_IsInbound(t *testing.T) {
	t.Parallel()

	var _ Inbound = Cancel{} // compile-time membership

	stream := []Inbound{Cancel{}}
	switch stream[0].(type) {
	case Cancel:
		// recovered
	default:
		t.Fatalf("stream[0] (%T) did not type-switch to Cancel", stream[0])
	}
}

// An option carrying a fabricated kind reports Kind.Valid() == false — proving
// the field is the enum, not a free string at the call site.
func TestPermissionOption_KindValidity(t *testing.T) {
	t.Parallel()

	good := PermissionOption{ID: "opt", Label: "Yes", Kind: PermissionOptionKindAllowOnce}
	if !good.Kind.Valid() {
		t.Errorf("canonical option kind reports Valid() == false: %+v", good)
	}
	bad := PermissionOption{ID: "opt", Label: "?", Kind: PermissionOptionKind("nope")}
	if bad.Kind.Valid() {
		t.Errorf("fabricated option kind reports Valid() == true: %+v", bad)
	}
}
