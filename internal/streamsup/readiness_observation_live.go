//go:build e2e_realclaude

package streamsup

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

type readinessForwarder struct {
	w       io.Writer
	p       *Parser
	session string
	partial []byte
}

func readinessStdout(w io.Writer, p *Parser, session string) io.Writer {
	if os.Getenv("PYRY_E2E_READINESS_RECORD") == "" || w == nil || p == nil {
		return w
	}
	return &readinessForwarder{w: w, p: p, session: session}
}

// Write witnesses os/exec's forwarding site before downstream parser work.
// It cannot measure kernel arrival or time spent before this forwarding call.
func (w *readinessForwarder) Write(b []byte) (int, error) {
	ObserveReadiness("stdout_forward_begin", w.session, true, len(b))
	w.partial = append(w.partial, b...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		readinessAck(w.p, w.partial[:i], "ack_forwarder_line")
		w.partial = w.partial[i+1:]
	}
	if len(w.partial) > defaultMaxParseBuf {
		w.partial = nil
	}
	w.partial = append([]byte(nil), w.partial...)
	n, err := w.w.Write(b)
	ObserveReadiness("stdout_forward_end", w.session, err == nil, n)
	return n, err
}

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
