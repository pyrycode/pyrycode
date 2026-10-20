package claudetape

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFallbackDiagnosticCopiesOnlyValidatedClosedCarriers(t *testing.T) {
	home := t.TempDir()
	if os.Chmod(home, 0700) != nil {
		t.Fatal("private home")
	}
	if os.Mkdir(filepath.Join(home, "recording-workers"), 0700) != nil {
		t.Fatal("workers")
	}
	path := FallbackWorkerJobPath(home, 1)
	foreign := filepath.Join(home, "foreign.json")
	tape := fallbackWorkerControlTape()
	tape.Frames[0].Data = strings.ReplaceAll(tape.Frames[0].Data, "Yes, please add the test.", "FOREIGN-DIAG-CARRIER")
	if tape.Save(foreign) != nil {
		t.Fatal("foreign carrier")
	}
	// A decoded job's arbitrary path and args must never redirect/copy diagnostics.
	job := FallbackWorkerJob{Version: 1, Home: home, Scenario: "conversation-active-switch", Path: foreign, Args: []string{"ANTHROPIC_API_KEY=must-not-copy"}}
	if PublishLifecycleControl(path, job) != nil {
		t.Fatal("job")
	}
	derived := strings.TrimSuffix(path, ".job") + ".json"
	if os.WriteFile(derived, []byte(`{"api_key":"sk-ant-synthetic-diagnostic-secret"}`), 0600) != nil {
		t.Fatal("invalid carrier")
	}
	if PublishLifecycleControl(strings.TrimSuffix(path, ".job")+".receipt", FallbackWorkerReceipt{TapeSHA256: "sk-ant-synthetic-receipt-secret"}) != nil {
		t.Fatal("receipt")
	}
	roles := promptSuiteControl().Roles
	scenario := "conversation-system-prompt"
	// Keep the diagnostic role inventory exact for its declared profile.
	job.Scenario = scenario
	if os.WriteFile(path, mustDiagnosticJSON(t, job), 0600) != nil {
		t.Fatal("rewrite control")
	}
	destination := filepath.Join(home, "diagnostic.json")
	if SaveLifecycleWorkerDiagnostic(home, scenario, destination, roles) != nil {
		t.Fatal("safe diagnostic")
	}
	raw, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"must-not-copy", "synthetic-diagnostic-secret", "synthetic-receipt-secret", "FOREIGN-DIAG-CARRIER"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("unverified carrier or string escaped")
		}
	}
	var d lifecycleWorkerDiagnostic
	if json.Unmarshal(raw, &d) != nil || !d.InvalidUnpromoted || len(d.Attempts) != 1 || d.Attempts[0].Tape != nil || d.Attempts[0].JobPathBound || d.Attempts[0].Receipt == nil || d.Attempts[0].Receipt.TapeSHA256 != "" {
		t.Fatal("diagnostic promoted unverified data")
	}
	if SaveLifecycleWorkerDiagnostic(home, scenario, destination, roles) == nil {
		t.Fatal("existing diagnostic replaced")
	}
	after, _ := os.ReadFile(destination)
	if string(after) != string(raw) {
		t.Fatal("existing bytes changed")
	}
	// Once the closed carrier itself validates, it can be retained as evidence,
	// still explicitly invalid/unpromoted until its source/ownership join passes.
	if os.Remove(derived) != nil || fallbackWorkerControlTape().Save(derived) != nil {
		t.Fatal("valid carrier")
	}
	next := filepath.Join(home, "diagnostic-valid.json")
	if SaveLifecycleWorkerDiagnostic(home, scenario, next, roles) != nil {
		t.Fatal("valid private carrier lost")
	}
	nextRaw, _ := os.ReadFile(next)
	if json.Unmarshal(nextRaw, &d) != nil || d.Attempts[0].Tape == nil || !d.InvalidUnpromoted {
		t.Fatal("carrier diagnostic missing")
	}
}
func mustDiagnosticJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
