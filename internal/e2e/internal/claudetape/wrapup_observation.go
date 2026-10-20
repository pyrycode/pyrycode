package claudetape

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"slices"
	"syscall"
	"time"
)

// WrapUpObservation is diagnostic evidence, never input or completion authority.
// Times measure the broker boundary; daemon deadline activation stays unknown.
type WrapUpObservation struct {
	Kind, Revision, SourceSHA256, DaemonSHA256 string
	StartedUnixNS                              int64
	DeadlineNS                                 int64
	Events                                     []WrapUpObservationEvent
	start                                      time.Time
	interruptID                                string
	overflow                                   bool
}

type WrapUpObservationEvent struct {
	Stage, Kind, Error string
	AfterNS            int64
	Bytes              int
}

func NewWrapUpObservation(revision, source, daemon string) *WrapUpObservation {
	now := time.Now()
	return &WrapUpObservation{Kind: "native-bounded-wrapup-observation-v1", Revision: revision, SourceSHA256: source, DaemonSHA256: daemon, StartedUnixNS: now.UnixNano(), DeadlineNS: 1000000, start: now}
}

// Add retains only closed classifications. Request IDs used for correlation
// remain in memory and cannot enter the serialized record.
func (o *WrapUpObservation) Add(stage string, raw []byte, err error) {
	if o == nil {
		return
	}
	if len(o.Events) >= 128 {
		o.overflow = true
		return
	}
	after := time.Since(o.start).Nanoseconds()
	kind := "other"
	var v struct {
		Type      string
		RequestID string `json:"request_id"`
		Request   struct{ Subtype string }
		Response  struct {
			Subtype   string
			RequestID string `json:"request_id"`
		}
	}
	if json.Unmarshal(raw, &v) == nil {
		switch {
		case v.Type == "user":
			kind = "user"
		case v.Type == "control_request" && v.Request.Subtype == "interrupt":
			kind = "interrupt"
			if stage == "received" {
				o.interruptID = v.RequestID
			}
		case v.Type == "control_response" && v.Response.Subtype == "success" && o.interruptID != "" && v.Response.RequestID == o.interruptID:
			kind = "interrupt_ack"
		}
	}
	class := "none"
	switch {
	case err == nil:
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		class = "deadline"
	case errors.Is(err, syscall.EPIPE):
		class = "broken_pipe"
	default:
		class = "rejected"
	}
	o.Events = append(o.Events, WrapUpObservationEvent{stage, kind, class, after, len(raw)})
}

func (o *WrapUpObservation) Validate(complete bool) error {
	fail := errors.New("invalid native wrap-up observation")
	if o == nil || o.Kind != "native-bounded-wrapup-observation-v1" || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(o.Revision) || !physicalSHA256.MatchString(o.SourceSHA256) || !physicalSHA256.MatchString(o.DaemonSHA256) || o.StartedUnixNS <= 0 || o.StartedUnixNS > 4102444800000000000 || o.DeadlineNS != 1000000 || o.overflow || len(o.Events) == 0 || len(o.Events) > 128 {
		return fail
	}
	counts := map[string]int{}
	previous := int64(-1)
	for i, e := range o.Events {
		if !slices.Contains([]string{"ready", "received", "forwarded", "output", "action", "stdout_eof", "stderr_eof", "stdin_eof", "complete", "reset_send", "reset_observed", "predecessor_gone"}, e.Stage) || !slices.Contains([]string{"other", "user", "interrupt", "interrupt_ack"}, e.Kind) || !slices.Contains([]string{"none", "deadline", "broken_pipe", "rejected"}, e.Error) || e.AfterNS < previous || e.AfterNS < 0 || e.AfterNS > int64(20*time.Minute) || e.Bytes < 0 || e.Bytes > MaxBytes {
			return fail
		}
		counts[e.Stage]++
		if complete && (e.Stage == "stdout_eof" || e.Stage == "stderr_eof") && counts["action"] != 1 {
			return fail
		}
		previous = e.AfterNS
		if e.Stage == "complete" && (i != len(o.Events)-1 || e.Error != "none") {
			if complete {
				return fail
			}
		}
	}
	if complete {
		if o.Events[0].Stage != "ready" || o.Events[len(o.Events)-1].Stage != "complete" {
			return fail
		}
		for _, stage := range []string{"ready", "action", "stdout_eof", "stderr_eof", "complete"} {
			if counts[stage] != 1 {
				return fail
			}
		}
		for _, e := range o.Events {
			if e.Error != "none" {
				return fail
			}
		}
	}
	return nil
}

func (o *WrapUpObservation) Save(path string) error {
	if err := o.Validate(false); err != nil {
		return err
	}
	raw, err := json.Marshal(o)
	if err != nil || Safe(raw) != nil {
		return errors.New("unsafe native observation")
	}
	return PublishLifecycleControl(path, o)
}
