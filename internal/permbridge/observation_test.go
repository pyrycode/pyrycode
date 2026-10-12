//go:build permission_observe

package permbridge

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestObservationRegistry(t *testing.T) {
	for _, reason := range []string{"allow", "deny", "timeout", "immediate"} {
		t.Run(reason, func(t *testing.T) {
			r, o := New(), NewObservationRecorder()
			r.Observe(o)
			window := time.Hour
			if reason == "immediate" {
				window = 0
			}
			p, err := r.Register("request", sampleRequest("request"), window)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.Register("", Request{}, window); err != ErrDuplicateID {
				t.Fatal(err)
			}
			if reason != "immediate" {
				if _, err := r.Register("request", Request{}, window); err != ErrDuplicateID {
					t.Fatal(err)
				}
			}
			switch reason {
			case "allow":
				r.Resolve("request", Allow(json.RawMessage(`{"secret":"payload"}`)))
			case "deny":
				r.Resolve("request", Deny(reasonTimeout))
			case "timeout":
				r.mu.Lock()
				old := r.pending["request"]
				r.mu.Unlock()
				r.expire("request", old, 0)
			}
			v := awaitWithin(t, p, time.Second)
			want := reason
			if want == "immediate" {
				want = "timeout"
			}
			if (want == "allow") != (v.Behavior == BehaviorAllow) {
				t.Fatal(v)
			}
			if r.Resolve("request", Allow(nil)) {
				t.Fatal("second winner")
			}
			o.End()
			s := o.Snapshot()
			var accepted, terminal int
			for i, record := range s.Records {
				if record.Stage == "register" && record.Reason == "accepted" {
					accepted = i
					if record.Identity != p.ObservationIdentity() {
						t.Fatal("identity")
					}
				}
				if record.Stage == "terminal" {
					terminal++
					if record.Reason != want || i <= accepted || record.Identity != p.ObservationIdentity() {
						t.Fatal(record)
					}
				}
				if record.Reason == "empty" || record.Reason == "duplicate" {
					if record.Identity.Generation != 0 {
						t.Fatal("rejection impersonates lifetime")
					}
				}
			}
			if accepted == 0 || terminal != 1 || !s.Complete {
				t.Fatalf("snapshot: %+v", s)
			}
		})
	}
	t.Run("answerable", func(t *testing.T) {
		r, o := New(), NewObservationRecorder()
		r.Observe(o)
		calls := 0
		r.SetAnswerable(func(string, Request) bool { calls++; return calls == 1 })
		p, _ := r.Register("extended", Request{}, time.Hour)
		r.mu.Lock()
		old := r.pending["extended"]
		r.mu.Unlock()
		r.expire("extended", old, time.Hour)
		if _, ok := r.Lookup("extended"); !ok {
			t.Fatal("extension lost")
		}
		r.expire("extended", old, time.Hour)
		awaitWithin(t, p, time.Second)
		o.End()
		if calls != 2 || o.Snapshot().Counters["terminal"] != 1 {
			t.Fatal("answerability changed")
		}
	})
	t.Run("race", func(t *testing.T) {
		for i := 0; i < 30; i++ {
			r, o := New(), NewObservationRecorder()
			r.Observe(o)
			p, _ := r.Register("race", Request{}, time.Hour)
			r.mu.Lock()
			old := r.pending["race"]
			r.mu.Unlock()
			done := make(chan struct{})
			go func() { r.expire("race", old, 0); close(done) }()
			won := r.Resolve("race", Allow(nil))
			v := awaitWithin(t, p, time.Second)
			<-done
			o.End()
			n := 0
			for _, rec := range o.Snapshot().Records {
				if rec.Stage == "terminal" {
					n++
					if (rec.Reason == "allow") != won {
						t.Fatal(rec)
					}
				}
			}
			if n != 1 || (v.Behavior == BehaviorAllow) != won {
				t.Fatal("arbitration")
			}
		}
	})
}

func TestObservationReuseReset(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(fmt.Sprint(reset), func(t *testing.T) {
			r, o := New(), NewObservationRecorder()
			r.Observe(o)
			first, _ := r.Register("reuse", Request{SessionID: "session"}, time.Hour)
			r.mu.Lock()
			old := r.pending["reuse"]
			r.mu.Unlock()
			if reset {
				o.Reset()
			}
			if !o.Submit("bridge", "seen", first.ObservationIdentity()) {
				t.Fatal("originating identity rejected")
			}
			r.Resolve("reuse", Allow(nil))
			awaitWithin(t, first, time.Second)
			second, _ := r.Register("reuse", Request{SessionID: "session"}, time.Hour)
			r.expire("reuse", old, 0)
			if _, ok := r.Lookup("reuse"); !ok {
				t.Fatal("stale timer won")
			}
			r.Resolve("reuse", Deny("secret"))
			awaitWithin(t, second, time.Second)
			o.End()
			a, b := first.ObservationIdentity(), second.ObservationIdentity()
			if a.Generation == 0 || a.Generation == b.Generation || a.Session != b.Session || a.Request != b.Request {
				t.Fatal(a, b)
			}
			if reset && a.Epoch == b.Epoch {
				t.Fatal("epoch reused")
			}
			found := false
			for _, rec := range o.Snapshot().Records {
				if rec.Stage == "terminal" && rec.Identity == a {
					found = true
				}
			}
			if !found || reset && o.Snapshot().Complete {
				t.Fatal("reset identity/coverage")
			}
		})
	}
}

func TestObservationBounds(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run("records/reset="+fmt.Sprint(reset), func(t *testing.T) {
			o := NewObservationRecorder()
			for i := 0; i < ObservationMaxRecords+2; i++ {
				o.Submit("bridge", "seen", ObservationIdentity{})
			}
			r := New()
			r.Observe(o)
			p, _ := r.Register("full", Request{}, time.Hour)
			defer r.Resolve("full", Deny("cleanup"))
			if p.ObservationIdentity().Generation != 0 {
				t.Fatal("lost registration exposed generation")
			}
			if reset {
				o.Reset()
			}
			r.Resolve("full", Allow(nil))
			awaitWithin(t, p, time.Second)
			o.End()
			s := o.Snapshot()
			if !reset && len(s.Records) != ObservationMaxRecords || !s.Loss["record_capacity"] || s.Complete || s.Counters["terminal"] != 0 || o.pending != 0 {
				t.Fatal(s)
			}
		})
	}
	t.Run("identities", func(t *testing.T) {
		o := NewObservationRecorder()
		for i := 0; i < ObservationMaxIdentities; i++ {
			o.Identity("", "", fmt.Sprint(i), "", "")
		}
		id := o.Identity("", "", "extra", "", "")
		o.Identity(strings.Repeat("x", ObservationMaxIdentityInput+1), "", "", "", "")
		r := New()
		r.Observe(o)
		p, _ := r.Register("over-capacity", Request{}, 0)
		awaitWithin(t, p, time.Second)
		if p.ObservationIdentity().Request != "unknown" {
			t.Fatal("over capacity alias")
		}
		s := o.Snapshot()
		if id.Request != "unknown" || !s.Loss["identity_capacity"] || !s.Loss["identity_oversize"] || s.IdentityBytes != ObservationMaxIdentityBytes {
			t.Fatal(s)
		}
	})
	t.Run("stalled", func(t *testing.T) {
		r, o := New(), NewObservationRecorder()
		r.Observe(o)
		o.mu.Lock()
		done := make(chan struct{})
		go func() { p, _ := r.Register("stalled", Request{}, 0); p.Await(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			o.mu.Unlock()
			t.Fatal("observer stalled approval")
		}
		o.mu.Unlock()
		if !o.Snapshot().Loss["contention"] {
			t.Fatal("silent loss")
		}
		p, _ := r.Register("terminal", Request{}, time.Hour)
		o.mu.Lock()
		done = make(chan struct{})
		go func() { r.Resolve("terminal", Allow(nil)); p.Await(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			o.mu.Unlock()
			t.Fatal("terminal blocked")
		}
		o.mu.Unlock()
	})
	t.Run("saturation", func(t *testing.T) {
		o := NewObservationRecorder()
		o.mu.Lock()
		o.next = ObservationMaxCounter
		o.counts["bridge"] = ObservationMaxCounter
		o.epoch = ObservationMaxCounter
		o.mu.Unlock()
		if o.Identity("x", "", "", "", "").Conversation != "unknown" {
			t.Fatal("alias wrapped")
		}
		o.Submit("bridge", "seen", ObservationIdentity{})
		o.Reset()
		s := o.Snapshot()
		if s.Counters["bridge"] != ObservationMaxCounter || !s.Loss["identity_exhausted"] || !s.Loss["counter_exhausted"] {
			t.Fatal(s)
		}
		r := New()
		r.Observe(o)
		p, _ := r.Register("exhausted", Request{}, 0)
		awaitWithin(t, p, time.Second)
		if p.ObservationIdentity().Generation != 0 {
			t.Fatal("generation collision")
		}
	})
}

func TestObservationNumericalIdentity(t *testing.T) {
	for _, field := range []string{"run_overflow", "epoch_overflow", "generation_overflow", "both_overflow", "future_epoch", "unissued_generation", "alias_generation", "missing_run", "missing_epoch", "missing_generation", "reconstructed"} {
		t.Run(field, func(t *testing.T) {
			r, o := New(), NewObservationRecorder()
			r.Observe(o)
			p, _ := r.Register("request", Request{}, time.Hour)
			r.Resolve("request", Allow(nil))
			awaitWithin(t, p, time.Second)
			id := p.ObservationIdentity()
			switch field {
			case "run_overflow":
				id.Run = ObservationMaxCounter + 1
			case "epoch_overflow":
				id.Epoch = ObservationMaxCounter + 1
			case "generation_overflow":
				id.Generation = ObservationMaxCounter + 1
			case "both_overflow":
				id.Epoch, id.Generation = ObservationMaxCounter+1, ObservationMaxCounter+1
			case "future_epoch":
				id.Epoch++
			case "unissued_generation":
				id.Generation++
			case "alias_generation":
				id.Generation = 1
			case "missing_run":
				id.Run = 0
			case "missing_epoch":
				id.Epoch = 0
			case "missing_generation":
				id.Generation = 0
			case "reconstructed":
				id = ObservationIdentity{Run: id.Run, Epoch: id.Epoch, Generation: id.Generation, Request: id.Request}
			}
			if o.Submit("bridge", "seen", id) {
				t.Fatal("unissued metadata admitted")
			}
			o.End()
			s := o.Snapshot()
			if s.Complete || !s.Loss["invalid_identity"] || s.Counters["bridge"] != 0 {
				t.Fatal(s)
			}
		})
	}
}

func TestObservationContract(t *testing.T) {
	secret := "HOSTILE-credentials-prompt-command-tool-input-\n\""
	o := NewObservationRecorder()
	r := New()
	r.Observe(o)
	id := o.Identity(secret, secret, secret, secret, secret)
	aliases := []string{id.Conversation, id.Session, id.Request, id.Modal, id.Connection}
	seen := map[string]bool{}
	for _, a := range aliases {
		if a == "unknown" || seen[a] {
			t.Fatal("kind collision")
		}
		seen[a] = true
	}
	if id != o.Identity(secret, secret, secret, secret, secret) {
		t.Fatal("unstable")
	}
	unknown := o.Identity(secret, "", "", "", "")
	if unknown.Session != "unknown" {
		t.Fatal("inferred session")
	}
	if o.Snapshot().Complete {
		t.Fatal("open interval complete")
	}
	req := Request{ToolName: secret, Input: json.RawMessage(`{"password":"HOSTILE"}`), SessionID: secret, Description: secret, DecisionReason: json.RawMessage(`"HOSTILE"`)}
	p, _ := r.Register(secret, req, time.Hour)
	r.Resolve(secret, Deny(secret))
	awaitWithin(t, p, time.Second)
	o.Submit("bridge", "seen", id)
	o.End()
	s := o.Snapshot()
	data, err := json.Marshal(s)
	if err != nil || strings.Contains(string(data), "HOSTILE") {
		t.Fatal(string(data), err)
	}
	t.Logf("checked content-free snapshot: %s", data)
	s.Records[0].Reason = secret
	s.Counters["register"] = 999
	s.Loss["forged"] = true
	if o.Snapshot().Records[0].Reason == secret || o.Snapshot().Counters["register"] == 999 || o.Snapshot().Loss["forged"] {
		t.Fatal("snapshot aliases state")
	}
	o.Submit(secret, secret, id)
	if !o.Snapshot().Loss["invalid_vocabulary"] {
		t.Fatal("untrusted vocabulary")
	}
	o.Submit("bridge", "seen", ObservationIdentity{Session: id.Conversation})
	if !o.Snapshot().Loss["incomplete_coverage"] {
		t.Fatal("kind confusion")
	}
	other := NewObservationRecorder()
	if other.Snapshot().Run == s.Run {
		t.Fatal("run reused")
	}
	live := New()
	pending, _ := live.Register("before", Request{}, time.Hour)
	live.Observe(other)
	live.Resolve("before", Deny("cleanup"))
	awaitWithin(t, pending, time.Second)
	other.End()
	if other.Snapshot().Complete || !other.Snapshot().Loss["incomplete_coverage"] {
		t.Fatal("pre-existing pending invisible")
	}
	o.Submit("relay", "seen", id)
	if !o.Snapshot().Loss["closed_submission"] {
		t.Fatal("closed loss")
	}
}
