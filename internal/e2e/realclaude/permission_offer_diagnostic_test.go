//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const permissionOfferDiagnosticPath = "/tmp/pyrycode-2365-permission-offer-diagnostic.json"

type permissionOfferDiagnostic struct {
	Ticket                string                `json:"ticket"`
	ClaudeVersion         string                `json:"claude_version"`
	SuggestionPresence    string                `json:"suggestion_presence"`
	Suppressed            bool                  `json:"suppressed"`
	PermissionSuggestions json.RawMessage       `json:"permission_suggestions,omitempty"`
	Validation            string                `json:"validation"`
	Outcome               string                `json:"outcome"`
	PublishedOffered      bool                  `json:"published_offered"`
	PublishedRuleCount    int                   `json:"published_rule_count"`
	CredentialScanApplied map[string]bool       `json:"credential_scan_applied"`
	Redaction             []dropcapSubstitution `json:"redaction"`
}

func writePermissionOfferDiagnostic(t *testing.T, path, home, workdir, claudeVersion string, source permissionObservation, published protocol.AlwaysAllowPayload) (string, string) {
	t.Helper()
	artifactDir := filepath.Dir(path)
	red := newDropcapRedactor(home, artifactDir, workdir, "", streamModalBootstrapUUID, time.Now().UnixNano())
	suggestions := append(json.RawMessage(nil), source.Request.PermissionSuggestions...)
	if len(suggestions) != 0 {
		suggestions = red.redact(suggestions)
	}
	validation := permbridge.DiagnoseAlwaysAllow(source.Request.PermissionSuggestions, source.Request.SuppressAlwaysAllowRule)
	outcome := permissionOfferDiagnosticOutcome(source.Request.PermissionSuggestions, source.Request.SuppressAlwaysAllowRule, validation, published.Offered)
	scanner := newDropcapScanner(home, artifactDir, workdir)
	record := permissionOfferDiagnostic{
		Ticket:                "2365",
		ClaudeVersion:         claudeVersion,
		SuggestionPresence:    permissionSuggestionPresence(source.Request.PermissionSuggestions),
		Suppressed:            source.Request.SuppressAlwaysAllowRule,
		PermissionSuggestions: suggestions,
		Validation:            validation,
		Outcome:               outcome,
		PublishedOffered:      published.Offered,
		PublishedRuleCount:    len(published.Rules),
		CredentialScanApplied: scanner.applied(),
		Redaction:             red.substitutions(),
	}
	blob, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatalf("marshal permission-offer diagnostic: %v", err)
	}
	if hits, _ := scanner.scan(blob); len(hits) != 0 {
		t.Fatalf("permission-offer diagnostic deny-scan found protected classes %v; nothing was written", hits)
	}
	if err := writePermissionOfferDiagnosticFile(path, append(blob, '\n')); err != nil {
		t.Fatalf("write permission-offer diagnostic: %v", err)
	}
	t.Logf("permission-offer diagnostic written: path=%s validation=%s outcome=%s", path, validation, outcome)
	return validation, outcome
}

func permissionSuggestionPresence(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "absent"
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "null"
	}
	return "present"
}

func permissionOfferDiagnosticOutcome(raw json.RawMessage, suppressed bool, validation string, published bool) string {
	if suppressed {
		return "suppressed"
	}
	if len(raw) == 0 {
		return "absent"
	}
	if validation != "offered" {
		return "rejected"
	}
	if !published {
		return "accepted_not_published"
	}
	return "offered"
}

func writePermissionOfferDiagnosticFile(path string, blob []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".pyrycode-2365-permission-offer-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if tmp != "" {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(blob); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("publish diagnostic: %w", err)
	}
	tmp = ""
	return nil
}

func TestWritePermissionOfferDiagnostic_RedactsAndClassifiesRejectedSource(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "synthetic-oauth-token-that-must-not-survive")
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-api-key-that-must-not-survive")
	home := t.TempDir()
	workdir := filepath.Join(home, "work")
	artifactPath := filepath.Join(t.TempDir(), "permission-offer.json")

	var source permissionObservation
	source.Request.PermissionSuggestions = json.RawMessage(`[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"` + workdir + `/witness"}]}]`)
	validation, outcome := writePermissionOfferDiagnostic(t, artifactPath, home, workdir, "2.1.test", source, protocol.AlwaysAllowPayload{})
	if validation != "update_0_behavior_empty" || outcome != "rejected" {
		t.Fatalf("diagnostic summary = (%q, %q), want (update_0_behavior_empty, rejected)", validation, outcome)
	}
	blob, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read diagnostic: %v", err)
	}
	info, err := os.Stat(artifactPath)
	if err != nil {
		t.Fatalf("stat diagnostic: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("diagnostic mode = %o, want %o", got, want)
	}
	for _, forbidden := range []string{home, workdir, os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"), os.Getenv("ANTHROPIC_API_KEY")} {
		if strings.Contains(string(blob), forbidden) {
			t.Fatalf("diagnostic retained a protected value")
		}
	}
	if !strings.Contains(string(blob), `$WORKDIR/witness`) {
		t.Fatalf("diagnostic did not preserve the redacted suggestion: %s", blob)
	}

	var record permissionOfferDiagnostic
	if err := json.Unmarshal(blob, &record); err != nil {
		t.Fatalf("decode diagnostic: %v", err)
	}
	if record.SuggestionPresence != "present" || record.Validation != validation || record.Outcome != outcome {
		t.Fatalf("diagnostic record = %+v", record)
	}
	if !record.CredentialScanApplied["CLAUDE_CODE_OAUTH_TOKEN"] || !record.CredentialScanApplied["ANTHROPIC_API_KEY"] {
		t.Fatalf("credential scan was not armed: %v", record.CredentialScanApplied)
	}
}

func TestPermissionObservation_PreservesAlwaysAllowSource(t *testing.T) {
	for _, raw := range []json.RawMessage{
		nil,
		json.RawMessage(`null`),
		json.RawMessage(`[]`),
		json.RawMessage(`[{"type":"addRules","rules":[{"toolName":"Bash"}]}]`),
	} {
		var before permissionObservation
		before.Request.PermissionSuggestions = raw
		before.Request.SuppressAlwaysAllowRule = true
		blob, err := json.Marshal(before)
		if err != nil {
			t.Fatal(err)
		}
		var after permissionObservation
		if err := json.Unmarshal(blob, &after); err != nil {
			t.Fatal(err)
		}
		if string(after.Request.PermissionSuggestions) != string(raw) || !after.Request.SuppressAlwaysAllowRule {
			t.Fatalf("observer changed always-allow source: before=%s after=%s suppressed=%t", raw, after.Request.PermissionSuggestions, after.Request.SuppressAlwaysAllowRule)
		}
	}
}

func TestPermissionOfferDiagnosticOutcome(t *testing.T) {
	for _, tc := range []struct {
		name       string
		raw        json.RawMessage
		suppressed bool
		validation string
		published  bool
		want       string
	}{
		{name: "absent", validation: "absent", want: "absent"},
		{name: "suppressed", raw: json.RawMessage(`[]`), suppressed: true, validation: "suppressed", want: "suppressed"},
		{name: "rejected", raw: json.RawMessage(`[]`), validation: "empty_batch", want: "rejected"},
		{name: "accepted not published", raw: json.RawMessage(`[]`), validation: "offered", want: "accepted_not_published"},
		{name: "offered", raw: json.RawMessage(`[]`), validation: "offered", published: true, want: "offered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := permissionOfferDiagnosticOutcome(tc.raw, tc.suppressed, tc.validation, tc.published); got != tc.want {
				t.Fatalf("outcome = %q, want %q", got, tc.want)
			}
		})
	}
}
