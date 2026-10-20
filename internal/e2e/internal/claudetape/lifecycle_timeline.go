package claudetape

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// LifecycleTimeline records successful native stdout forwarding offsets from
// this role's broker readiness. Production arms the idle timer earlier; this
// preserves the provider phase without pretending readiness is that timer's
// activation epoch. Original timers, actions, EOF and ownership stay real.
type LifecycleTimeline struct {
	Kind             string  `json:"kind"`
	OutputAfterNS    []int64 `json:"output_after_ns"`
	OutputAfterUser  []int   `json:"output_after_user,omitempty"`
	OutputAfterInput []int   `json:"output_after_input,omitempty"`
}

const lifecycleTimelineKind = "provider-ready-stdout-v1"
const lifecycleFrameTimelineKind = "stdin-forwarded-stdout-v3"

func lifecycleFrameTimeline(scenario string) bool {
	return scenario == "stop-background-task-bootstrap" || scenario == "composer-stop-bootstrap"
}

const lifecycleInputTimelineKind = "user-forwarded-stdout-v2"

func lifecycleInputTimeline(scenario string) bool {
	return strings.HasPrefix(scenario, "conversation-system-prompt-") || strings.HasPrefix(scenario, "conversation-active-switch-") || scenario == "fallback-suggestion-bootstrap"
}
func lifecycleTimelineUser(raw []byte) bool {
	var v struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(raw, &v) == nil && v.Type == "user"
}

func lifecycleTimelineEnabled(scenario string) bool {
	return lifecycleFrameTimeline(scenario) || scenario == "fallback-suggestion-bootstrap" || scenario == "conversation-active-switch-bootstrap" || scenario == "conversation-active-switch-created-1" || scenario == "conversation-active-switch-created-2" || scenario == "session-resume-bootstrap" || scenario == "conversation-system-prompt-bare" || scenario == "conversation-system-prompt-bootstrap" || scenario == "conversation-system-prompt-prompted"
}
func validateLifecycleTimeline(tape *LifecycleTape) error {
	fail := errors.New("invalid native output timeline")
	if tape.Timeline == nil {
		return nil
	}
	kind := tape.Timeline.Kind
	if !lifecycleTimelineEnabled(tape.Tape.Scenario) || !(kind == lifecycleTimelineKind || kind == lifecycleInputTimelineKind && lifecycleInputTimeline(tape.Tape.Scenario) || kind == lifecycleFrameTimelineKind && lifecycleFrameTimeline(tape.Tape.Scenario)) {
		return fail
	}
	if lifecycleFrameTimeline(tape.Tape.Scenario) && kind != lifecycleFrameTimelineKind {
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
	anchored := kind == lifecycleInputTimelineKind || kind == lifecycleFrameTimelineKind
	anchors := tape.Timeline.OutputAfterUser
	if kind == lifecycleFrameTimelineKind {
		anchors = tape.Timeline.OutputAfterInput
		if len(tape.Timeline.OutputAfterUser) != 0 {
			return fail
		}
	} else if len(tape.Timeline.OutputAfterInput) != 0 {
		return fail
	}
	if anchored && len(anchors) != outputs || !anchored && len(anchors) != 0 {
		return fail
	}

	previous, lastAnchor := int64(-1), 0
	user, ordinal := 0, 0
	for _, frame := range tape.Tape.Frames {
		if frame.Direction == "stdin" && (kind == lifecycleFrameTimelineKind || lifecycleTimelineUser(frame.Data)) {
			user++
		}
		if frame.Direction != "stdout" {
			continue
		}
		anchor := 0
		if anchored {
			anchor = anchors[ordinal]
			if anchor != user {
				return fail
			}
		}
		offset := tape.Timeline.OutputAfterNS[ordinal]
		if anchor != lastAnchor {
			previous = -1
		}
		if offset < 0 || offset < previous || offset > int64(20*time.Minute) {
			return fail
		}
		previous, lastAnchor = offset, anchor
		ordinal++
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
		return lifecycleTimingFailure("before-wait", origin, offset, current)
	}
	if current.Before(due) {
		sleep(due.Sub(current))
	}
	if current = now(); current.After(due.Add(100 * time.Millisecond)) {
		return lifecycleTimingFailure("after-wait", origin, offset, current)
	}
	return nil
}

func forwardLifecycleOutput(origin time.Time, offset int64, now func() time.Time, sleep func(time.Duration), forward func() error) error {
	if err := waitLifecycleOutput(origin, offset, now, sleep); err != nil {
		return err
	}
	if err := forward(); err != nil {
		if current := now(); current.After(origin.Add(time.Duration(offset)).Add(100 * time.Millisecond)) {
			return lifecycleTimingFailure("forwarding", origin, offset, current)
		}
		return err
	}
	if current := now(); current.After(origin.Add(time.Duration(offset)).Add(100 * time.Millisecond)) {
		return lifecycleTimingFailure("forwarding", origin, offset, current)
	}
	return nil
}

// LifecycleTimingError contains only broker-generated timing metadata, never
// native frame data or arbitrary I/O error text. Ordinals are zero based.
type LifecycleTimingError struct {
	Stage                        string
	Output, Anchor               int
	DueNS, CurrentNS, LatenessNS int64
	StartupNS, ReadyNS           int64
	CompletionNS, WitnessNS      int64
	PriorOutputCompletionNS      int64
	// OutputWorkNS measures restoration, BeforeOutput observation, replay projection
	// and actual forwarding, in that order. It grants no completion authority.
	OutputWorkNS [4]int64
}

func (e *LifecycleTimingError) Error() string {
	switch e.Stage {
	case "before-wait":
		return "native output timeline missed absolute due time"
	case "after-wait":
		return "native output timeline scheduling drift"
	default:
		return "native output timeline forwarding drift"
	}
}

func lifecycleTimingFailure(stage string, origin time.Time, offset int64, current time.Time) error {
	return &LifecycleTimingError{Stage: stage, DueNS: offset, CurrentNS: current.Sub(origin).Nanoseconds(), LatenessNS: current.Sub(origin.Add(time.Duration(offset))).Nanoseconds()}
}
