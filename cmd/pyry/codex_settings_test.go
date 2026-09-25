package main

import (
	"testing"

	"github.com/pyrycode/pyrycode/internal/codexsup"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// TestCodexTurnOverrides pins every row of the posture table, and that model
// and effort pass through unchanged.
func TestCodexTurnOverrides(t *testing.T) {
	granular, onRequest, never := codexsup.ApprovalGranular, codexsup.ApprovalOnRequest, codexsup.ApprovalNever
	readOnly, workspace, full := codexsup.SandboxReadOnly, codexsup.SandboxWorkspaceWrite, codexsup.SandboxDangerFullAccess
	user, auto := codexsup.ReviewerUser, codexsup.ReviewerAutoReview
	tests := []struct {
		name                       string
		s                          sessions.SessionSettings
		approval, sandbox, reviewr string
	}{
		{"default", sessions.SessionSettings{PermissionMode: "default"}, granular, workspace, user},
		{"acceptEdits", sessions.SessionSettings{PermissionMode: "acceptEdits"}, granular, workspace, user},
		{"plan", sessions.SessionSettings{PermissionMode: "plan"}, granular, readOnly, user},
		{"auto", sessions.SessionSettings{PermissionMode: "auto"}, onRequest, workspace, auto},
		{"dontAsk", sessions.SessionSettings{PermissionMode: "dontAsk"}, never, workspace, user},
		{"bypassPermissions", sessions.SessionSettings{PermissionMode: sessions.PermissionModeBypass}, never, full, user},
		// The bit is the authoritative half: it wins over a mode it contradicts.
		{"YOLO over plan", sessions.SessionSettings{PermissionMode: "plan", YOLO: true}, never, full, user},
		{"empty", sessions.SessionSettings{}, granular, readOnly, user},
		{"unknown", sessions.SessionSettings{PermissionMode: "Plan"}, granular, readOnly, user},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := codexTurnOverrides(tc.s)
			want := codexsup.TurnInput{ApprovalPolicy: tc.approval, Sandbox: tc.sandbox, ApprovalsReviewer: tc.reviewr}
			if got != want {
				t.Errorf("codexTurnOverrides(%+v) = %+v, want %+v", tc.s, got, want)
			}
		})
	}

	got := codexTurnOverrides(sessions.SessionSettings{Model: "gpt-6-luna", Effort: "low", PermissionMode: "plan"})
	if got.Model != "gpt-6-luna" || got.Effort != "low" || got.Text != "" {
		t.Errorf("model, effort, text = %q, %q, %q; want gpt-6-luna, low, empty", got.Model, got.Effort, got.Text)
	}
}

// TestResolveCodexModel: a stored model that is a held family's value becomes
// that family's newest version; anything else, including a family with no
// entries held, is sent as stored.
func TestResolveCodexModel(t *testing.T) {
	families := []turnevent.ModelOption{
		{Value: "sol", ResolvedModel: "gpt-6-sol"},
		{Value: "luna", ResolvedModel: "gpt-6-luna"},
		{Value: "terra"},
	}
	tests := []struct {
		name, model string
		families    []turnevent.ModelOption
		want        string
	}{
		{"family resolves", "luna", families, "gpt-6-luna"},
		{"unlisted version passes through", "gpt-5.6-sol", families, "gpt-5.6-sol"},
		{"resolved version passes through", "gpt-6-luna", families, "gpt-6-luna"},
		{"match is exact", "Luna", families, "Luna"},
		{"family with no entries held", "luna", nil, "luna"},
		{"family with an empty list", "luna", []turnevent.ModelOption{}, "luna"},
		{"entry with no version keeps the family", "terra", families, "terra"},
		{"empty model stays empty", "", families, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveCodexModel(tc.model, tc.families); got != tc.want {
				t.Errorf("resolveCodexModel(%q) = %q, want %q", tc.model, got, tc.want)
			}
		})
	}
}
