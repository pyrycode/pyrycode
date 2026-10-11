//go:build e2e_realclaude

package streamsup

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

var readinessSubjects sync.Map
var readinessFileMu sync.Mutex

// ObserveReadiness records isolated live-test boundaries. Subject identifiers
// stay in a private join file; the capture exports only ordinals and booleans.
// Observation failures never change delivery; the capture rejects missing data.
func ObserveReadiness(boundary, subject string, ok bool, value ...int) {
	path := os.Getenv("PYRY_E2E_READINESS_RECORD")
	if path == "" {
		return
	}
	r := struct {
		At       time.Time `json:"at"`
		Boundary string    `json:"boundary"`
		Subject  string    `json:"subject"`
		OK       bool      `json:"ok"`
		Value    int       `json:"value,omitempty"`
	}{At: time.Now().UTC(), Boundary: boundary, Subject: subject, OK: ok}
	if len(value) > 0 {
		r.Value = value[0]
	}
	readinessFileMu.Lock()
	defer readinessFileMu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return // Missing observations make the live capture fail.
	}
	_ = json.NewEncoder(f).Encode(r) // Best effort, independently checked by capture.
	_ = f.Close()                    // Each append owns and closes its descriptor.
}

func readinessArm(g *PostureGate, session string) {
	if os.Getenv("PYRY_E2E_READINESS_RECORD") == "" || g == nil {
		return
	}
	readinessSubjects.Store(g, session)
	ObserveReadiness("posture_armed", session, !g.ready())
}

func readinessAck(p *Parser, line []byte, boundary string) bool {
	if os.Getenv("PYRY_E2E_READINESS_RECORD") == "" || p.postureGate == nil {
		return false
	}
	var ack struct {
		Type string `json:"type"`
		controlAckLine
	}
	if json.Unmarshal(line, &ack) != nil || ack.Type != "control_response" {
		return false
	}
	g := p.postureGate
	g.mu.Lock()
	match := g.armedID != "" && g.armedID == ack.Response.RequestID
	g.mu.Unlock()
	subject, known := readinessSubjects.Load(g)
	if !match || !known {
		return false
	}
	ok := ack.Response.Subtype == controlResponseSuccess
	ObserveReadiness(boundary, subject.(string), ok)
	return ok
}

func readinessReleased(p *Parser) {
	if subject, ok := readinessSubjects.Load(p.postureGate); ok {
		ObserveReadiness("gate_released", subject.(string), p.postureGate.ready())
	}
}
