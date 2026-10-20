package claudetape

import (
	"encoding/json"
	"testing"
)

func fallbackSuggestionSuiteControl() *LifecycleSuite {
	tape := fallbackExchangeControl()
	tape.Scenario = "fallback-suggestion-bootstrap"
	timeline := &LifecycleTimeline{Kind: lifecycleTimelineKind, OutputAfterNS: []int64{}}
	for _, frame := range tape.Frames {
		if frame.Direction == "stdout" {
			timeline.OutputAfterNS = append(timeline.OutputAfterNS, 0)
		}
	}
	return &LifecycleSuite{Version: 1, Scenario: "fallback-suggestion", Producer: tape.Producer, DaemonSHA256: lifecycleSuiteControl().DaemonSHA256, Roles: map[string]*LifecycleTape{"bootstrap": {Version: 1, Tape: tape, End: "seal", Timeline: timeline}}, Workers: []LifecycleWorker{{SourceRole: "bootstrap", UserSequence: 1, Tape: fallbackWorkerControlTape()}}}
}
func TestFallbackSuggestionRequiresUncontestedFirstWorker(t *testing.T) {
	roles := LifecycleProfiles()["fallback-suggestion"].Roles
	if fallbackSuggestionSuiteControl().Validate(roles) != nil {
		t.Fatal("valid first fallback worker")
	}
	for _, mutation := range []string{"missing-worker", "failed-worker", "second-turn", "native-nonempty", "native-empty", "native-blank"} {
		t.Run(mutation, func(t *testing.T) {
			s := fallbackSuggestionSuiteControl()
			switch mutation {
			case "missing-worker":
				s.Workers = nil
			case "failed-worker":
				s.Workers[0].Tape.Status = FallbackWorkerStatus{ExitCode: 7}
			case "second-turn":
				s.Workers[0].UserSequence = 2
			default:
				text := "Native competing suggestion"
				if mutation == "native-empty" {
					text = ""
				}
				if mutation == "native-blank" {
					text = " "
				}
				raw, _ := json.Marshal(map[string]string{"type": "prompt_suggestion", "suggestion": text})
				s.Roles["bootstrap"].Tape.Frames = append(s.Roles["bootstrap"].Tape.Frames, Frame{Direction: "stdout", Data: raw})
				s.Roles["bootstrap"].Timeline.OutputAfterNS = append(s.Roles["bootstrap"].Timeline.OutputAfterNS, 0)
			}
			if s.Validate(roles) == nil {
				t.Fatal("unattributed fallback publication accepted")
			}
		})
	}
}
