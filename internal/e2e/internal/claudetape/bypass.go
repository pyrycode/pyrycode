package claudetape

import (
	"encoding/json"
	"errors"
	"slices"
)

// RequireBypassQuestions preserves the original observer's independent source
// proof: every init offers questions in bypass, exactly two unique real asks,
// at least three results, and no ordinary-tool host permission request.
func (t *Tape) RequireBypassQuestions() error {
	initSeen, requests, results := false, 0, 0
	ids := map[string]bool{}
	for _, f := range t.Frames {
		if f.Direction != "stdout" {
			continue
		}
		var v struct {
			Type, Subtype  string
			Tools          []string
			PermissionMode string `json:"permissionMode"`
			RequestID      string `json:"request_id"`
			Request        struct {
				Subtype   string
				ToolName  string `json:"tool_name"`
				ToolUseID string `json:"tool_use_id"`
			}
		}
		if json.Unmarshal(f.Data, &v) != nil {
			return errors.New("invalid bypass source frame")
		}
		switch {
		case v.Type == "system" && v.Subtype == "init":
			initSeen = true
			if !slices.Contains(v.Tools, "AskUserQuestion") || v.PermissionMode != "bypassPermissions" {
				return errors.New("init did not offer AskUserQuestion in bypass")
			}
		case v.Type == "control_request" && v.Request.Subtype == "can_use_tool":
			if v.Request.ToolName != "AskUserQuestion" || v.RequestID == "" || v.Request.ToolUseID == "" || ids[v.RequestID] {
				return errors.New("question source identity reused/missing or ordinary tool requested host approval")
			}
			ids[v.RequestID] = true
			requests++
		case v.Type == "result":
			results++
		}
	}
	if !initSeen || requests != 2 || results < 3 {
		return errors.New("incomplete bypass question source evidence")
	}
	return nil
}
