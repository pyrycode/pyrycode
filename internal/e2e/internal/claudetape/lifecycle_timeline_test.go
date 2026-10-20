package claudetape

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestLifecycleTimingDiagnostic(t *testing.T) {
	for _, stage := range []string{"before-wait", "after-wait", "forwarding"} {
		t.Run(stage, func(t *testing.T) {
			origin := time.Unix(0, 0)
			current := origin
			if stage == "before-wait" {
				current = current.Add(2 * time.Second)
			}
			now := func() time.Time { return current }
			sleep := func(d time.Duration) {
				current = current.Add(d)
				if stage == "after-wait" {
					current = current.Add(time.Second)
				}
			}
			err := forwardLifecycleOutput(origin, int64(time.Second), now, sleep, func() error {
				current = current.Add(time.Second)
				return nil
			})
			var diagnostic *LifecycleTimingError
			if !errors.As(err, &diagnostic) || diagnostic.Stage != stage || diagnostic.DueNS != int64(time.Second) || diagnostic.CurrentNS != int64(2*time.Second) || diagnostic.LatenessNS != int64(time.Second) {
				t.Fatalf("missing numeric timing evidence for %s: %v", stage, err)
			}
		})
	}
}

func TestLifecycleTimelineRejectsForeignOrIncompleteSchedule(t *testing.T) {
	source := resumeSuiteControl("complete original native transcript")
	role := source.Roles["bootstrap"]
	if role.Validate() != nil {
		t.Fatal("valid timeline")
	}
	for _, kind := range []string{"foreign-role", "missing-type", "missing-output", "extra-output", "negative", "descending", "unbounded"} {
		t.Run(kind, func(t *testing.T) {
			raw, _ := json.Marshal(role)
			var tape LifecycleTape
			_ = json.Unmarshal(raw, &tape)
			switch kind {
			case "foreign-role":
				tape.Tape.Scenario = "conversation-lifecycle-bootstrap"
			case "missing-type":
				tape.Timeline.Kind = ""
			case "missing-output":
				tape.Timeline.OutputAfterNS = nil
			case "extra-output":
				tape.Timeline.OutputAfterNS = append(tape.Timeline.OutputAfterNS, 2)
			case "negative":
				tape.Timeline.OutputAfterNS[0] = -1
			case "descending":
				tape.Tape.Frames = append(tape.Tape.Frames, tape.Tape.Frames[1])
				tape.Timeline.OutputAfterNS = []int64{2, 1}
			case "unbounded":
				tape.Timeline.OutputAfterNS[0] = int64(21 * time.Minute)
			}
			if tape.Validate() == nil {
				t.Fatal("foreign or incomplete output schedule accepted")
			}
		})
	}
}
func TestLifecycleTimelineUsesAbsoluteDueTimes(t *testing.T) {
	origin := time.Now()
	current := origin
	calls := 0
	now := func() time.Time { return current }
	sleep := func(d time.Duration) { calls++; current = current.Add(d) }
	if waitLifecycleOutput(origin, int64(time.Second), now, sleep) != nil || !current.Equal(origin.Add(time.Second)) || calls != 1 {
		t.Fatal("first native phase was not retained")
	}
	current = current.Add(200 * time.Millisecond)
	if waitLifecycleOutput(origin, int64(2*time.Second), now, sleep) != nil || !current.Equal(origin.Add(2*time.Second)) {
		t.Fatal("input delay reset or compressed absolute phase")
	}
	current = origin.Add(3 * time.Second)
	if waitLifecycleOutput(origin, int64(2*time.Second), now, sleep) == nil {
		t.Fatal("late original input silently caught up")
	}
	current = origin
	if waitLifecycleOutput(origin, int64(time.Second), now, func(d time.Duration) { current = current.Add(d + 200*time.Millisecond) }) == nil {
		t.Fatal("scheduler stall silently changed phase")
	}
}

func TestLifecycleTimelineRejectsLateFinalForward(t *testing.T) {
	origin := time.Now()
	current := origin
	writes := 0
	now := func() time.Time { return current }
	sleep := func(d time.Duration) { current = current.Add(d) }
	err := forwardLifecycleOutput(origin, int64(time.Second), now, sleep, func() error { writes++; current = current.Add(200 * time.Millisecond); return nil })
	if err == nil || writes != 1 {
		t.Fatal("final native output backpressure escaped timing audit")
	}
	current = origin
	if err := forwardLifecycleOutput(origin, int64(time.Second), now, sleep, func() error { current = current.Add(50 * time.Millisecond); return nil }); err != nil {
		t.Fatal("bounded successful forwarding rejected", err)
	}
}

func TestLifecycleInputTimelineBindsActualPrecedingUser(t *testing.T) {
	role := resumeSuiteControl("native").Roles["bootstrap"]
	role.Tape.Scenario = "conversation-system-prompt-prompted"
	// One native output before and one after the successfully forwarded user.
	role.Tape.Frames = []Frame{{Direction: "stdout", Data: json.RawMessage(`{"type":"system","subtype":"init"}`)}, {Direction: "stdin", Data: json.RawMessage(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`)}, {Direction: "stdout", Data: json.RawMessage(`{"type":"result","subtype":"success"}`)}}
	role.Timeline = &LifecycleTimeline{Kind: lifecycleInputTimelineKind, OutputAfterNS: []int64{int64(time.Second), int64(200 * time.Millisecond)}, OutputAfterUser: []int{0, 1}}
	if validateLifecycleTimeline(role) != nil {
		t.Fatal("valid independent user epoch rejected")
	}
	for _, anchors := range [][]int{nil, {0}, {0, 0}, {1, 1}, {0, 2}, {0, -1}} {
		role.Timeline.OutputAfterUser = anchors
		if validateLifecycleTimeline(role) == nil {
			t.Fatal("missing, stale or future user anchor accepted")
		}
	}
	role.Timeline.OutputAfterUser = []int{0, 1}
	role.Tape.Scenario = "session-resume-bootstrap"
	if validateLifecycleTimeline(role) == nil {
		t.Fatal("idle activation phase reanchored to input")
	}
}
func TestLifecycleInputTimelineDoesNotResetAnExistingUserPhase(t *testing.T) {
	ready := time.Now()
	user := ready.Add(3 * time.Second)
	current := user
	now := func() time.Time { return current }
	sleep := func(d time.Duration) { current = current.Add(d) }
	if waitLifecycleOutput(ready, int64(200*time.Millisecond), now, sleep) == nil {
		t.Fatal("old readiness schedule concealed late user")
	}
	if waitLifecycleOutput(user, int64(200*time.Millisecond), now, sleep) != nil || !current.Equal(user.Add(200*time.Millisecond)) {
		t.Fatal("native user-relative latency not retained")
	}
	current = current.Add(500 * time.Millisecond)
	if waitLifecycleOutput(user, int64(400*time.Millisecond), now, sleep) == nil {
		t.Fatal("second output reset user epoch or caught up")
	}
}

func TestLifecyclePhysicalTimelineBindsEveryActualInputIncludingControl(t *testing.T) {
	for _, mutation := range []string{"none", "stale-control-anchor", "future", "missing", "foreign-v2", "mixed-fields", "nonphysical", "backward-within-control"} {
		t.Run(mutation, func(t *testing.T) {
			tape := &LifecycleTape{Tape: &Tape{Scenario: "composer-stop-bootstrap", Frames: []Frame{
				{Direction: "stdout", Data: json.RawMessage(`{"type":"system","subtype":"init"}`)},
				{Direction: "stdin", Data: json.RawMessage(`{"type":"user"}`)},
				{Direction: "stdout", Data: json.RawMessage(`{"type":"assistant"}`)},
				{Direction: "stdin", Data: json.RawMessage(`{"type":"control_request","request_id":"interrupt","request":{"subtype":"interrupt"}}`)},
				{Direction: "stdout", Data: json.RawMessage(`{"type":"result"}`)},
				{Direction: "stdout", Data: json.RawMessage(`{"type":"system"}`)},
			}}, Timeline: &LifecycleTimeline{Kind: lifecycleFrameTimelineKind, OutputAfterNS: []int64{100, 200, 10, 20}, OutputAfterInput: []int{0, 1, 2, 2}}}
			switch mutation {
			case "stale-control-anchor":
				tape.Timeline.OutputAfterInput = []int{0, 1, 1, 1}
			case "future":
				tape.Timeline.OutputAfterInput = []int{0, 1, 3, 3}
			case "missing":
				tape.Timeline.OutputAfterInput = nil
			case "foreign-v2":
				tape.Timeline.Kind = lifecycleInputTimelineKind
			case "mixed-fields":
				tape.Timeline.OutputAfterUser = []int{0, 1, 1, 1}
			case "nonphysical":
				tape.Tape.Scenario = "fallback-suggestion-bootstrap"
			case "backward-within-control":
				tape.Timeline.OutputAfterNS = []int64{100, 200, 20, 10}
			}
			err := validateLifecycleTimeline(tape)
			if (err == nil) != (mutation == "none") {
				t.Fatalf("physical native schedule mutation %s: %v", mutation, err)
			}
		})
	}
}
