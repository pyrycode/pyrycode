//go:build e2e_realclaude

package streamsup

import (
	"bytes"
	"encoding/json"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// Kernel start ticks let the resource sidecar exclude a reused PID.
var readinessProcessStart = readinessKernelStart(os.Getpid())

func readinessKernelStart(pid int) string {
	b, e := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if e != nil {
		return ""
	}
	_, tail, ok := strings.Cut(string(b), ") ")
	if !ok {
		return ""
	}
	f := strings.Fields(tail)
	if len(f) <= 19 {
		return ""
	}
	return f[19]
}

var readinessEpoch = strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)

// ObserveReadiness records isolated live-test boundaries. Subject identifiers
// stay in a private join file; the capture exports only ordinals and booleans.
// Observation failures never change delivery; the capture rejects missing data.
func ObserveReadiness(boundary, subject string, ok bool, value ...int) {
	path := os.Getenv("PYRY_E2E_READINESS_RECORD")
	if path == "" {
		return
	}
	r := struct {
		At         time.Time `json:"at"`
		Boundary   string    `json:"boundary"`
		Subject    string    `json:"subject"`
		OK         bool      `json:"ok"`
		Value      int       `json:"value,omitempty"`
		Process    int       `json:"process"`
		Child      string    `json:"child,omitempty"`
		ChildStart string    `json:"child_start,omitempty"`
	}{At: time.Now().UTC(), Boundary: boundary, Subject: subject, OK: ok, Process: os.Getpid()}
	if child, ok := readinessChildren.Load(subject); ok {
		r.Child = child.(string)
	}
	if len(value) > 0 {
		r.Value = value[0]
		if boundary == "child_started" && ok {
			r.ChildStart = readinessKernelStart(r.Value)
		}
	}
	readinessAppend(r)
}

func readinessAppend(record any) {
	path := os.Getenv("PYRY_E2E_READINESS_RECORD")
	if path == "" {
		return
	}
	b, e := json.Marshal(record)
	if e != nil {
		return
	}
	var fields map[string]any
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if decoder.Decode(&fields) != nil {
		return
	}
	for _, arg := range os.Args[1:] {
		if name, ok := strings.CutPrefix(arg, "-pyry-name="); ok {
			home, _ := os.UserHomeDir()
			fields["owner"] = home + "/" + name
			fields["owner_home"] = home
			fields["instance"] = name
			break
		}
	}
	fields["epoch"] = readinessEpoch
	fields["process_start"] = readinessProcessStart
	readinessFileMu.Lock()
	defer readinessFileMu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return // Missing observations make the live capture fail.
	}
	_ = json.NewEncoder(f).Encode(fields) // Best effort, independently checked by capture.
	_ = f.Close()                         // Each append owns and closes its descriptor.
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

var readinessChildSequence atomic.Uint64
var readinessChildren sync.Map

func readinessChildEnv(env []string, session string) []string {
	if os.Getenv("PYRY_E2E_READINESS_RECORD") == "" {
		return env
	}
	child := readinessEpoch + "-" + strconv.FormatUint(readinessChildSequence.Add(1), 10)
	readinessChildren.Store(session, child)
	return append(env, "PYRY_E2E_READINESS_CHILD="+child)
}

// ObserveReadinessRequest retains private header joins for isolated diagnostics.
// Payloads are never retained; portable exporters replace joins with ordinals.
func ObserveReadinessRequest(phase, device string, env protocol.Envelope, ok bool) {
	if os.Getenv("PYRY_E2E_READINESS_RECORD") == "" {
		return
	}
	kind := env.Type
	if phase == "host_received" && protocol.IsKnownAppType(env) != nil {
		kind = "unknown"
	}
	var scope struct {
		ConversationID string `json:"conversation_id"`
	}
	_ = json.Unmarshal(env.Payload, &scope)
	if len(scope.ConversationID) > 128 {
		scope.ConversationID = ""
	}
	var reply uint64
	if env.InReplyTo != nil {
		reply = *env.InReplyTo
	}
	outcome := "unknown"
	if phase == "outbound_queued" && env.InReplyTo != nil {
		if env.Type == protocol.TypeError {
			outcome = "refused"
		} else {
			outcome = "correlated_reply"
		}
	}
	record := struct {
		At      time.Time `json:"at"`
		Phase   string    `json:"phase"`
		Kind    string    `json:"kind"`
		Device  string    `json:"device"`
		Subject string    `json:"subject"`
		Request uint64    `json:"request"`
		ReplyTo uint64    `json:"reply_to,omitempty"`
		Process int       `json:"process"`
		OK      bool      `json:"ok"`
		Outcome string    `json:"outcome"`
	}{time.Now().UTC(), phase, kind, device, scope.ConversationID, env.ID, reply, os.Getpid(), ok, outcome}
	readinessAppend(record)
}
