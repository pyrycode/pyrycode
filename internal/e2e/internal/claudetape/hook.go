package claudetape

import (
	"encoding/json"
	"errors"
	"strings"
)

// RequireHookOrder rejects tapes whose refusal tail could cross the next input.
// A result is only a terminal source marker, not proof of provider success.
func (t *Tape) RequireHookOrder(block, live string) error {
	phase := 0
	for _, f := range t.Frames {
		var v struct {
			Type, Subtype, Content string
			PreventContinuation    bool `json:"prevent_continuation"`
		}
		if json.Unmarshal(f.Data, &v) != nil {
			return errors.New("invalid hook source object")
		}
		if f.Direction == "stdin" && v.Type == "user" {
			switch phase {
			case 0:
				if !IsUserText(f.Data, block) {
					return errors.New("hook blocked input mismatch")
				}
				phase = 1
			case 3:
				if !IsUserText(f.Data, live) {
					return errors.New("hook liveness input mismatch")
				}
				phase = 4
			default:
				return errors.New("hook user input crossed refusal boundary")
			}
		}
		if f.Direction == "stdout" {
			if v.Type == "system" && v.Subtype == "informational" && v.PreventContinuation && strings.Contains(v.Content, "blocked by the pyry 2255 capture rig: this prompt carries the rig marker") {
				if phase != 1 {
					return errors.New("hook refusal source out of order")
				}
				phase = 2
			}
			if v.Type == "result" {
				switch phase {
				case 2:
					phase = 3
				case 4:
					phase = 5
				default:
					return errors.New("hook result source out of order")
				}
			}
		}
	}
	if phase != 5 {
		return errors.New("incomplete hook refusal/liveness source boundary")
	}
	return nil
}
