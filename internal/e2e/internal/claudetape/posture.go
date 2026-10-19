package claudetape

import (
	"encoding/json"
	"errors"
)

// RequireModeBeforeUser verifies a correlated successful native posture write
// precedes the selected daemon-written user input; argv posture is insufficient.
func (t *Tape) RequireModeBeforeUser(mode string, ordinal int) error {
	if mode == "" || ordinal < 1 {
		return errors.New("invalid posture checkpoint")
	}
	sent := map[string]string{}
	currentRequest := ""
	confirmed := ""
	users := 0
	for _, f := range t.Frames {
		var v struct {
			Type      string
			RequestID string `json:"request_id"`
			Request   struct{ Subtype, Mode string }
			Response  struct {
				Subtype   string
				RequestID string `json:"request_id"`
				Response  struct{ Mode string }
			}
		}
		if json.Unmarshal(f.Data, &v) != nil {
			return errors.New("invalid posture source frame")
		}
		if f.Direction == "stdin" && v.Type == "control_request" && v.Request.Subtype == "set_permission_mode" {
			if v.RequestID == "" || sent[v.RequestID] != "" {
				return errors.New("invalid posture request identity")
			}
			sent[v.RequestID] = v.Request.Mode
			currentRequest = v.RequestID
			confirmed = ""
		}
		if f.Direction == "stdout" && v.Type == "control_response" && sent[v.Response.RequestID] != "" && v.Response.RequestID == currentRequest {
			if v.Response.Subtype == "success" && v.Response.Response.Mode == sent[v.Response.RequestID] {
				confirmed = v.Response.Response.Mode
			} else {
				confirmed = ""
			}
		}
		if f.Direction == "stdin" && v.Type == "user" {
			users++
			if users == ordinal {
				if confirmed != mode {
					return errors.New("user input preceded the confirmed native posture")
				}
				return nil
			}
		}
	}
	return errors.New("posture checkpoint user input missing")
}
