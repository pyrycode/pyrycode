package claudetape

import (
	"encoding/json"
	"testing"
	"time"
)

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
