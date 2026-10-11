//go:build e2e_realclaude

package streamsup

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadinessForwarderBeforeParser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observations.jsonl")
	t.Setenv("PYRY_E2E_READINESS_RECORD", path)
	p := NewParser(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.PostureGate().arm("private-request")
	readinessArm(p.PostureGate(), "session")
	var dst bytes.Buffer
	w := readinessStdout(&dst, p, "session")
	line := []byte(`{"type":"control_response","response":{"request_id":"private-request","subtype":"success"}}` + "\n")
	for _, part := range [][]byte{line[:20], line[20:]} {
		if n, err := w.Write(part); err != nil || n != len(part) {
			t.Fatal("forwarder changed write result")
		}
	}
	b, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(b, []byte("ack_forwarder_line")) || !bytes.Equal(dst.Bytes(), line) {
		t.Fatal("split acknowledgment did not reach forwarder witness and destination")
	}
	if p.PostureGate().ready() || bytes.Contains(b, []byte("private-request")) {
		t.Fatal("observer released gate or exposed request")
	}
}

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

func TestReadinessChildStartIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observations.jsonl")
	t.Setenv("PYRY_E2E_READINESS_RECORD", path)
	readinessChildEnv(nil, "identity-session")
	ObserveReadiness("child_started", "identity-session", true, os.Getpid())
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var witness struct {
		Child      string `json:"child"`
		ChildStart string `json:"child_start"`
		Start      string `json:"process_start"`
	}
	if json.Unmarshal(b, &witness) != nil || witness.Child == "" || witness.ChildStart != readinessKernelStart(os.Getpid()) || witness.Start != readinessProcessStart {
		t.Fatal("spawn observation lost kernel lifetime identity")
	}
	if readinessProcessStart != "" && witness.ChildStart == "" {
		t.Fatal("known kernel start exported as unknown")
	}
}
