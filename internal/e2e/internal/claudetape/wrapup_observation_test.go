package claudetape

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWrapUpObservationKinds(t *testing.T) {
	o := NewWrapUpObservation(strings.Repeat("a", 40), strings.Repeat("b", 64), strings.Repeat("c", 64))
	for _, step := range []struct{ stage, raw, kind string }{
		{"received", `{"type":"control_request","request_id":"private-id","request":{"subtype":"interrupt"}}`, "interrupt"},
		{"forwarded", `{"type":"user","message":{"content":"private prompt"}}`, "user"},
		{"output", `{"type":"control_response","response":{"subtype":"success","request_id":"foreign-id"}}`, "other"},
		{"output", `{"type":"control_response","response":{"subtype":"success","request_id":"private-id"}}`, "interrupt_ack"},
	} {
		o.Add(step.stage, []byte(step.raw), nil)
		if got := o.Events[len(o.Events)-1].Kind; got != step.kind {
			t.Errorf("kind=%s, want %s", got, step.kind)
		}
	}
	o.Add("forwarded", nil, errors.New("private error /secret/path"))
	if o.Events[len(o.Events)-1].Error != "rejected" {
		t.Fatal("arbitrary error escaped classification")
	}
	path := t.TempDir() + "/observation.json"
	if err := o.Save(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(raw), "private") || strings.Contains(string(raw), "secret") {
		t.Fatal("private material reached observation")
	}
	var restored WrapUpObservation
	if ReadLifecycleControl(path, 16384, &restored) != nil || restored.Validate(false) != nil {
		t.Fatal("observation did not round trip")
	}
	if o.Save(path) == nil {
		t.Fatal("observation overwrote existing evidence")
	}
	if os.Chmod(path, 0644) != nil || ReadWrapUpObservationJSON(path, 16384, &restored) != nil || restored.Validate(false) != nil {
		t.Fatal("reviewed fixture mode rejected")
	}
	link := path + ".link"
	if os.Symlink(path, link) != nil {
		t.Fatal("cannot create rejection control")
	}
	if ReadWrapUpObservationJSON(link, 16384, &restored) == nil {
		t.Fatal("symlink fixture accepted")
	}
}

func TestWrapUpObservationRetainsTruncatedPrefix(t *testing.T) {
	for _, reason := range []string{"event_limit", "elapsed_limit"} {
		t.Run(reason, func(t *testing.T) {
			o := NewWrapUpObservation(strings.Repeat("a", 40), strings.Repeat("b", 64), strings.Repeat("c", 64))
			o.Add("ready", nil, nil)
			if reason == "event_limit" {
				for len(o.Events) < 128 {
					o.Add("received", nil, nil)
				}
			} else {
				o.start = time.Now().Add(-21 * time.Minute)
			}
			prefix := append([]WrapUpObservationEvent(nil), o.Events...)
			o.Add("received", nil, nil)
			o.Add("complete", nil, nil)
			if !reflect.DeepEqual(o.Events, prefix) {
				t.Fatal("exhaustion changed the valid prefix")
			}
			path := t.TempDir() + "/partial.json"
			if err := o.Save(path); err != nil {
				t.Fatalf("failed to retain partial evidence: %v", err)
			}
			var restored WrapUpObservation
			if ReadWrapUpObservationJSON(path, 16384, &restored) != nil || restored.Validate(false) != nil || restored.Validate(true) == nil || !reflect.DeepEqual(restored.Events, prefix) {
				t.Fatal("partial evidence lost or accepted as complete")
			}
			raw, err := os.ReadFile(path)
			var classification struct{ Truncated string }
			if err != nil || json.Unmarshal(raw, &classification) != nil || classification.Truncated != reason {
				t.Fatal("missing explicit truncation classification")
			}
		})
	}
}

func TestWrapUpObservationValidation(t *testing.T) {
	newComplete := func() *WrapUpObservation {
		o := NewWrapUpObservation(strings.Repeat("a", 40), strings.Repeat("b", 64), strings.Repeat("c", 64))
		for _, stage := range []string{"ready", "action", "stdout_eof", "stderr_eof", "complete"} {
			o.Add(stage, nil, nil)
		}
		return o
	}
	for _, tc := range []struct {
		name   string
		change func(*WrapUpObservation)
	}{
		{"unknown-stage", func(o *WrapUpObservation) { o.Events[0].Stage = "secret" }},
		{"unknown-kind", func(o *WrapUpObservation) { o.Events[0].Kind = "secret" }},
		{"combined-kinds", func(o *WrapUpObservation) { o.Events[0].Kind = "other|user" }},
		{"unknown-error", func(o *WrapUpObservation) { o.Events[0].Error = "secret" }},
		{"unknown-truncation", func(o *WrapUpObservation) { o.Truncated = "secret" }},
		{"negative-time", func(o *WrapUpObservation) { o.Events[0].AfterNS = -1 }},
		{"time-reversal", func(o *WrapUpObservation) { o.Events[1].AfterNS = o.Events[4].AfterNS + 1 }},
		{"oversized-bytes", func(o *WrapUpObservation) { o.Events[0].Bytes = MaxBytes + 1 }},
		{"missing-eof", func(o *WrapUpObservation) { o.Events = append(o.Events[:2], o.Events[3:]...) }},
		{"duplicate-action", func(o *WrapUpObservation) { o.Events[0].Stage = "action" }},
		{"failed-completion", func(o *WrapUpObservation) { o.Events[4].Error = "rejected" }},
		{"overflow", func(o *WrapUpObservation) {
			for i := 0; i < 130; i++ {
				o.Add("received", nil, nil)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := newComplete()
			if o.Validate(true) != nil {
				t.Fatal("valid control rejected")
			}
			tc.change(o)
			if o.Validate(true) == nil {
				t.Fatal("invalid observation accepted")
			}
		})
	}
	partial := NewWrapUpObservation(strings.Repeat("a", 40), strings.Repeat("b", 64), strings.Repeat("c", 64))
	partial.Add("ready", nil, nil)
	if partial.Validate(true) == nil {
		t.Fatal("incomplete observation accepted")
	}
}
