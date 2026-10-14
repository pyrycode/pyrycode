//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// streamModalReader observes the single ordered envelope stream across wait phases.
// The trace retains only metadata; application payloads never enter diagnostics.
type streamModalReader struct {
	t         *testing.T
	read      func(time.Time) (protocol.Envelope, bool)
	started   time.Time
	phase     string
	trace     []string
	omitted   int
	dismissed map[string]string
}

func (r *streamModalReader) next(deadline time.Time) (protocol.Envelope, bool) {
	r.t.Helper()
	if !time.Now().Before(deadline) {
		return protocol.Envelope{}, false
	}
	env, ok := r.read(deadline)
	if !ok {
		return env, false
	}
	var identity struct {
		ModalID string `json:"modal_id"`
	}
	if env.Type == protocol.TypeModalShown || env.Type == protocol.TypeModalDismissed {
		if err := json.Unmarshal(env.Payload, &identity); err != nil {
			r.t.Fatalf("decode modal identity: %v", err)
		}
	}
	var reply uint64
	if env.InReplyTo != nil {
		reply = *env.InReplyTo
	}
	entry := fmt.Sprintf("elapsed=%s phase=%s type=%s env_id=%d reply_id=%d modal_id=%s daemon_ts=%s",
		time.Since(r.started).Round(time.Microsecond), r.phase, env.Type, env.ID, reply,
		identity.ModalID, env.TS.UTC().Format(time.RFC3339Nano))
	const traceLimit = 128
	if len(r.trace) == traceLimit {
		r.trace = append(r.trace[:0], r.trace[1:]...)
		r.omitted++
	}
	r.trace = append(r.trace, entry)
	if env.Type == protocol.TypeModalDismissed && identity.ModalID != "" {
		if r.dismissed == nil {
			r.dismissed = make(map[string]string)
		}
		if _, exists := r.dismissed[identity.ModalID]; !exists {
			r.dismissed[identity.ModalID] = entry
		}
	}
	return env, true
}

func (r *streamModalReader) waitDismissed(deadline time.Time, modalID string) bool {
	for {
		if _, ok := r.dismissed[modalID]; ok {
			return true
		}
		if _, ok := r.next(deadline); !ok {
			return false
		}
	}
}

func (r *streamModalReader) diagnostics(daemon string) string {
	var lines []string
	for _, line := range strings.Split(daemon, "\n") {
		var fields []string
		relevant := false
		// Skip quoted slog values whole, so message text cannot impersonate fields.
		for line = strings.TrimSpace(line); line != ""; line = strings.TrimSpace(line) {
			key, rest, ok := strings.Cut(line, "=")
			if !ok {
				break
			}
			var value string
			if strings.HasPrefix(rest, `"`) {
				quoted, err := strconv.QuotedPrefix(rest)
				if err != nil {
					break
				}
				value, line = quoted, rest[len(quoted):]
			} else {
				value, line, _ = strings.Cut(rest, " ")
			}
			field := key + "=" + value
			switch key {
			case "time", "level", "event", "modal_id", "outcome", "source", "env_id", "conn_id", "pid":
				fields = append(fields, field)
			}
			if key == "modal_id" || key == "pid" || strings.HasPrefix(field, "event=stream_approval.") {
				relevant = true
			}
		}
		if relevant {
			lines = append(lines, strings.Join(fields, " "))
		}
	}
	return fmt.Sprintf("phase=%s elapsed=%s omitted=%d retained_dismissals=%v\nreceived:\n%s\ndaemon metadata:\n%s",
		r.phase, time.Since(r.started).Round(time.Microsecond), r.omitted,
		r.dismissed, strings.Join(r.trace, "\n"), strings.Join(lines, "\n"))
}

func TestStreamModalReaderOrdering(t *testing.T) {
	for _, tc := range []struct {
		name  string
		early bool
		found bool
	}{
		{name: "early", early: true, found: true},
		{name: "later", found: true},
		{name: "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shown := protocol.Envelope{Type: protocol.TypeModalShown, Payload: json.RawMessage(`{"modal_id":"target","prompt":"secret-payload-3412"}`)}
			dismissed := protocol.Envelope{Type: protocol.TypeModalDismissed, Payload: json.RawMessage(`{"modal_id":"target"}`)}
			other := protocol.Envelope{Type: protocol.TypeModalDismissed, Payload: json.RawMessage(`{"modal_id":"other"}`)}
			ack := protocol.Envelope{Type: protocol.TypeAck}
			frames := []protocol.Envelope{ack, shown, other}
			if tc.early {
				frames = []protocol.Envelope{shown, dismissed, ack}
			} else if tc.found {
				frames = append(frames, dismissed)
			}
			deadline := time.Now().Add(3 * time.Second)
			reads := 0
			r := streamModalReader{t: t, started: time.Now(), phase: "ack"}
			r.read = func(got time.Time) (protocol.Envelope, bool) {
				reads++
				if got != deadline {
					t.Fatalf("receive deadline changed: got %v want %v", got, deadline)
				}
				if len(frames) == 0 {
					return protocol.Envelope{}, false
				}
				env := frames[0]
				frames = frames[1:]
				return env, true
			}
			for {
				env, ok := r.next(deadline)
				if !ok {
					t.Fatal("missing scripted ack")
				}
				if env.Type == protocol.TypeAck {
					break
				}
			}
			r.phase = "child-exit-dismissal"
			if got := r.waitDismissed(deadline, "target"); got != tc.found {
				t.Fatalf("dismissed=%v want %v; %s", got, tc.found, r.diagnostics(""))
			}
			if tc.found {
				before := reads
				if !r.waitDismissed(deadline, "target") || reads != before {
					t.Fatal("repeated observation read another frame")
				}
			}
			t.Log(r.diagnostics(""))
		})
	}
}

func TestStreamModalReaderDeadline(t *testing.T) {
	r := streamModalReader{t: t, started: time.Now(), phase: "child-exit-dismissal"}
	deadline := time.Now().Add(5 * time.Millisecond)
	reads := 0
	r.read = func(got time.Time) (protocol.Envelope, bool) {
		reads++
		if got != deadline {
			t.Fatal("absolute deadline changed")
		}
		return protocol.Envelope{Type: protocol.TypeQueueState}, true
	}
	if r.waitDismissed(deadline, "absent") {
		t.Fatal("missing dismissal passed despite unrelated buffered frames")
	}
	before := reads
	if r.waitDismissed(time.Now().Add(-time.Second), "absent") || reads != before {
		t.Fatal("expired wait consumed buffered frames")
	}
}

func TestStreamModalReaderDiagnostics(t *testing.T) {
	const secret = "secret-payload-3412"
	r := streamModalReader{t: t, started: time.Now(), phase: "ack"}
	reads := 0
	r.read = func(time.Time) (protocol.Envelope, bool) {
		reads++
		if reads == 1 {
			return protocol.Envelope{Type: protocol.TypeModalDismissed, Payload: json.RawMessage(`{"modal_id":"target"}`)}, true
		}
		return protocol.Envelope{Type: protocol.TypeModalShown, Payload: mustJSON(t, map[string]string{"modal_id": "target", "prompt": secret, "answer_token": secret})}, true
	}
	for range 131 {
		r.next(time.Now().Add(time.Second))
	}
	if !r.waitDismissed(time.Now().Add(-time.Second), "target") || reads != 131 {
		t.Fatal("trace eviction lost the retained dismissal")
	}
	daemon := "time=now level=INFO msg=\"prompt modal_id=" + secret + "\" modal_id=target outcome=denied_timeout source=timeout token=" + secret + "\n" +
		"time=later level=DEBUG event=stream_approval.dismissed_push_err env_id=42 error=" + secret
	got := r.diagnostics(daemon)
	for _, want := range []string{"omitted=3", "retained_dismissals=map[target:", "phase=ack", "type=modal_shown", "modal_id=target", "outcome=denied_timeout", "event=stream_approval.dismissed_push_err", "env_id=42"} {
		if !strings.Contains(got, want) {
			t.Errorf("diagnostics missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, secret) || len(r.trace) != 128 {
		t.Fatal("diagnostics leaked content or exceeded trace bound")
	}
}
