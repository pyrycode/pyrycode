package claudetape

import (
	"errors"
	"time"
)

// LifecycleTimeline records successful native stdout forwarding offsets from
// this role's broker readiness. Production arms the idle timer earlier; this
// preserves the provider phase without pretending readiness is that timer's
// activation epoch. Original timers, actions, EOF and ownership stay real.
type LifecycleTimeline struct {
	Kind          string  `json:"kind"`
	OutputAfterNS []int64 `json:"output_after_ns"`
}

const lifecycleTimelineKind = "provider-ready-stdout-v1"

func lifecycleTimelineEnabled(scenario string) bool { return scenario == "session-resume-bootstrap" }
func validateLifecycleTimeline(tape *LifecycleTape) error {
	fail := errors.New("invalid native output timeline")
	if tape.Timeline == nil {
		return nil
	}
	if !lifecycleTimelineEnabled(tape.Tape.Scenario) || tape.Timeline.Kind != lifecycleTimelineKind {
		return fail
	}
	outputs := 0
	for _, frame := range tape.Tape.Frames {
		if frame.Direction == "stdout" {
			outputs++
		}
	}
	if len(tape.Timeline.OutputAfterNS) != outputs {
		return fail
	}
	previous := int64(-1)
	for _, offset := range tape.Timeline.OutputAfterNS {
		if offset < 0 || offset < previous || offset > int64(20*time.Minute) {
			return fail
		}
		previous = offset
	}
	return nil
}

// Absolute due times prevent a late input or scheduling stall from silently
// compressing the captured gaps. This is a timing assertion, not a new timeout
// for production's unchanged idle or fallback windows.
func waitLifecycleOutput(origin time.Time, offset int64, now func() time.Time, sleep func(time.Duration)) error {
	due := origin.Add(time.Duration(offset))
	current := now()
	if current.After(due.Add(100 * time.Millisecond)) {
		return errors.New("native output timeline missed absolute due time")
	}
	if current.Before(due) {
		sleep(due.Sub(current))
	}
	if now().After(due.Add(100 * time.Millisecond)) {
		return errors.New("native output timeline scheduling drift")
	}
	return nil
}
