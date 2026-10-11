//go:build e2e_realclaude

package streamsup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadinessObservationMatching(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observations.jsonl")
	t.Setenv("PYRY_E2E_READINESS_RECORD", path)
	p := NewParser(nil, nil)
	g := p.PostureGate()
	g.arm("current-private-request")
	readinessArm(g, "test-session")
	for _, tc := range []struct {
		id, subtype string
		want        bool
	}{
		{"old-private-request", "success", false},
		{"current-private-request", "error", false},
		{"current-private-request", "success", true},
	} {
		line := []byte(`{"type":"control_response","response":{"request_id":"` + tc.id + `","subtype":"` + tc.subtype + `"}}`)
		if got := readinessAck(p, line, "ack_stdout_line"); got != tc.want {
			t.Errorf("matching success = %v, want %v", got, tc.want)
		}
		if g.ready() {
			t.Fatal("observation released permission gate")
		}
	}
	g.arm("replacement-private-request")
	if readinessAck(p, []byte(`{"type":"control_response","response":{"request_id":"current-private-request","subtype":"success"}}`), "ack_processing") {
		t.Fatal("old child acknowledged replacement")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "private-request") {
		t.Fatal("observation leaked request token")
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatal("invalid observation record")
		}
	}
}
