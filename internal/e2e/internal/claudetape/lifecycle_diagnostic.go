package claudetape

import (
	"encoding/json"
	"errors"
	"os"
	"syscall"
)

// LifecycleDiagnostic is content-free terminal state, never completion authority.
type LifecycleDiagnostic struct {
	InputKind                                                     string
	Stage, ErrorClass                                             string
	Frames, PlayerNext, PlayerFrames, PendingInput, PendingOutput int
	InputEOF, OutputEOF, StderrEOF, Sealed, ActionAccepted        bool
	ActionAcknowledged, TerminalChecked, Completed                bool
	InputSampled, OutputSampled, StderrSampled                    bool
	InputsMatched, OutputsDelivered, ProviderInputsForwarded      int
	// Maxima retain the output ordinal for each timing substage independently.
	// Order: restoration, physical observation, replay projection, forwarding.
	OutputWorkMaxNS      [4]int64
	OutputWorkMaxOrdinal [4]int
}

func lifecycleInputKind(raw []byte) string {
	var frame struct {
		Type    string
		Request struct{ Subtype string }
	}
	if json.Unmarshal(raw, &frame) != nil {
		return "invalid"
	}
	switch frame.Type {
	case "user", "control_response":
		return frame.Type
	case "control_request":
		switch frame.Request.Subtype {
		case "initialize", "interrupt", "get_context_usage", "mcp_status", "get_settings":
			return "control_request/" + frame.Request.Subtype
		}
		return "control_request/other"
	default:
		return "other"
	}
}

func lifecycleErrorClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, syscall.EPIPE):
		return "broken-pipe"
	case errors.Is(err, os.ErrDeadlineExceeded):
		return "deadline"
	default:
		return "rejected"
	}
}
