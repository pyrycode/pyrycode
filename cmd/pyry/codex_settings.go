package main

import (
	"github.com/pyrycode/pyrycode/internal/codexsup"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// codexTurnOverrides is claudeSettingsArgs' counterpart for a Codex session:
// it turns the session's stored settings into the overrides every turn/start
// carries. Model and effort pass through; the posture maps as
//
//	YOLO / bypassPermissions  never       dangerFullAccess  user
//	default, acceptEdits      granular    workspaceWrite    user
//	plan                      granular    readOnly          user
//	auto                      on-request  workspaceWrite    auto_review
//	dontAsk                   never       workspaceWrite    user
//	anything else, ""         granular    readOnly          user
//
// The posture is never empty. Codex keeps a turn's overrides for the thread's
// later turns, so an omitted field would keep the previous, possibly looser,
// one; asserting all three on every turn is what makes a tightening stick. The
// YOLO bit is read before the mode, as claudeSettingsArgs reads it: the bit is
// the authoritative half of the posture. Text is left empty for the caller.
func codexTurnOverrides(s sessions.SessionSettings) codexsup.TurnInput {
	in := codexsup.TurnInput{
		Model:             s.Model,
		Effort:            s.Effort,
		ApprovalPolicy:    codexsup.ApprovalGranular,
		Sandbox:           codexsup.SandboxReadOnly,
		ApprovalsReviewer: codexsup.ReviewerUser,
	}
	mode := s.PermissionMode
	if s.YOLO {
		mode = sessions.PermissionModeBypass
	}
	switch mode {
	case sessions.PermissionModeBypass:
		in.ApprovalPolicy, in.Sandbox = codexsup.ApprovalNever, codexsup.SandboxDangerFullAccess
	case "default", "acceptEdits":
		in.Sandbox = codexsup.SandboxWorkspaceWrite
	case "auto":
		in.ApprovalPolicy, in.Sandbox = codexsup.ApprovalOnRequest, codexsup.SandboxWorkspaceWrite
		in.ApprovalsReviewer = codexsup.ReviewerAutoReview
	case "dontAsk":
		in.ApprovalPolicy, in.Sandbox = codexsup.ApprovalNever, codexsup.SandboxWorkspaceWrite
	}
	return in
}
