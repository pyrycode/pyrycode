//go:build e2e_realclaude

package realclaude

// TestRealClaude_PermissionProtocol_RegressionFixtures pins the null
// findings of the #383 permission-protocol spike as a regression
// contract against committed fixtures. Pure JSON parse + assert; no
// API call, no subprocess.
//
// When `claude` updates and the spike-runner regenerates fixtures per
// docs/knowledge/features/permission-protocol-spike.md, this test
// either passes (findings still hold) or fails with a message naming
// the offending fixture and which finding flipped. A failure is the
// signal to revisit the mobile-relay design implication intentionally,
// not silently.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

const fixtureGlob = "testdata/permission_protocol_v*_*.json"

var fixtureNameRE = regexp.MustCompile(`^permission_protocol_v[^_]+_([A-Za-z]+)\.json$`)

// forbiddenEventTypes lists the top-level `type` strings whose presence
// in stdout_events would mean a permission-gate event fired — i.e.
// spike finding #1 has flipped.
var forbiddenEventTypes = []string{
	"control_request",
	"permission_request",
	"tool_permission_request",
}

func TestRealClaude_PermissionProtocol_RegressionFixtures(t *testing.T) {
	matches, err := filepath.Glob(fixtureGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", fixtureGlob, err)
	}
	if len(matches) == 0 {
		t.Fatalf("no fixtures matched glob: %s (deleted fixture set must be loud)", fixtureGlob)
	}

	for _, path := range matches {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			assertRegressionFixture(t, path)
		})
	}
}

// assertRegressionFixture runs the six AC assertions against a single
// fixture file. Uses t.Errorf (not Fatalf) inside the per-finding
// checks so one run reports every flipped finding simultaneously —
// useful diagnostic when a claude release changes multiple behaviours.
func assertRegressionFixture(t *testing.T, path string) {
	t.Helper()
	name := filepath.Base(path)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var top struct {
		StdoutEvents           []json.RawMessage `json:"stdout_events"`
		ExitCode               int               `json:"exit_code"`
		ContextDeadlineTripped bool              `json:"context_deadline_tripped"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}

	expectedMode, err := expectedPermissionModeFromFilename(name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}

	// AC#6: structural sanity check.
	if top.ExitCode != 0 {
		t.Errorf("%s: exit_code = %d, want 0 (structural sanity check)", name, top.ExitCode)
	}
	if top.ContextDeadlineTripped {
		t.Errorf("%s: context_deadline_tripped = true, want false (structural sanity check)", name)
	}
	if len(top.StdoutEvents) < 7 {
		t.Errorf("%s: len(stdout_events) = %d, want >= 7 (structural sanity check)", name, len(top.StdoutEvents))
	}

	var (
		initEvent    json.RawMessage
		resultEvent  json.RawMessage
		forbiddenHit bool
	)
	for i, ev := range top.StdoutEvents {
		var hdr struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
		}
		if err := json.Unmarshal(ev, &hdr); err != nil {
			// Mirrors allowed_tools_enforcement_test.go:60-67: a single
			// malformed line must not abort the whole subtest.
			t.Errorf("%s: stdout_events[%d] header unmarshal: %v", name, i, err)
			continue
		}
		// AC#2: forbidden permission-gate event types must not appear.
		if slices.Contains(forbiddenEventTypes, hdr.Type) {
			t.Errorf("%s: forbidden event type observed: stdout_events[%d].type = %q (spike finding #1 flipped: permission gate fired on stdio)",
				name, i, hdr.Type)
			forbiddenHit = true
		}
		if hdr.Type == "system" && hdr.Subtype == "init" && initEvent == nil {
			initEvent = ev
		}
		if hdr.Type == "result" && hdr.Subtype == "success" && resultEvent == nil {
			resultEvent = ev
		}
	}
	_ = forbiddenHit // already reported via t.Errorf above; kept for clarity.

	// AC#3 + AC#5: init.tools contains "Bash" and init.permissionMode echoes expected.
	if initEvent == nil {
		t.Errorf("%s: system/init event not found in stdout_events (cannot check init.tools or init.permissionMode)", name)
	} else {
		var initBody struct {
			Tools          []string `json:"tools"`
			PermissionMode string   `json:"permissionMode"`
		}
		if err := json.Unmarshal(initEvent, &initBody); err != nil {
			t.Errorf("%s: system/init body unmarshal: %v", name, err)
		} else {
			if !slices.Contains(initBody.Tools, "Bash") {
				t.Errorf("%s: init.tools missing %q (spike finding #4 flipped: --allowed-tools may now gate the registry)",
					name, "Bash")
			}
			if initBody.PermissionMode != expectedMode {
				t.Errorf("%s: init.permissionMode = %q, want %q (spike finding #3 flipped: mode echo changed; for filename token 'auto' the expected echo is 'default')",
					name, initBody.PermissionMode, expectedMode)
			}
		}
	}

	// AC#4: result.permission_denials is empty.
	if resultEvent == nil {
		t.Errorf("%s: result/success event not found in stdout_events (cannot check permission_denials)", name)
	} else {
		var resultBody struct {
			PermissionDenials []json.RawMessage `json:"permission_denials"`
		}
		if err := json.Unmarshal(resultEvent, &resultBody); err != nil {
			t.Errorf("%s: result/success body unmarshal: %v", name, err)
		} else if len(resultBody.PermissionDenials) != 0 {
			t.Errorf("%s: result.permission_denials non-empty: len=%d (spike finding #1/#2 flipped: a gate fired and denied something)",
				name, len(resultBody.PermissionDenials))
		}
	}
}

// expectedPermissionModeFromFilename extracts the <mode> token from a
// fixture filename of the form `permission_protocol_v<ver>_<mode>.json`
// and applies the `auto -> default` synonym from spike finding #3.
func expectedPermissionModeFromFilename(name string) (string, error) {
	m := fixtureNameRE.FindStringSubmatch(name)
	if m == nil {
		return "", &fixtureNameError{name: name}
	}
	token := m[1]
	if token == "auto" {
		return "default", nil
	}
	return token, nil
}

type fixtureNameError struct{ name string }

func (e *fixtureNameError) Error() string {
	return "fixture filename does not match `permission_protocol_v<ver>_<mode>.json`: " + e.name
}
