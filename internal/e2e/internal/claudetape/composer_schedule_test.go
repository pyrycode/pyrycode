package claudetape

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestComposerOutputPreparationSchedule(t *testing.T) {
	response, offset := testComposerControlResponse(t)
	for _, tc := range []struct {
		name                          string
		prepare, observe, write, late time.Duration
		failPrepare, oversleep        bool
		wantStage                     string
	}{
		{name: "use-captured-gap", prepare: 120 * time.Millisecond},
		{name: "never-observe-early", prepare: time.Millisecond},
		{name: "preparation-overrun", prepare: 200 * time.Millisecond, wantStage: "before-wait"},
		{name: "late-input", late: time.Second, wantStage: "before-wait"},
		{name: "scheduler-stall", oversleep: true, wantStage: "after-wait"},
		{name: "observer-overrun", observe: 200 * time.Millisecond, wantStage: "forwarding"},
		{name: "write-backpressure", write: 200 * time.Millisecond, wantStage: "forwarding"},
		{name: "preparation-error", failPrepare: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin := time.Unix(0, 0)
			current := origin.Add(tc.late)
			entered, release := make(chan time.Time, 1), make(chan struct{})
			done := make(chan error, 1)
			home := "/tmp/composer-schedule"
			binding := &DaemonBinding{home: home, workdir: home + "/work", session: "33333333-3333-4333-8333-333333333333"}
			p, err := NewPhysicalNativeObserver(context.Background(), home, "composer", LifecycleRoleBinding{Role: "bootstrap", SessionID: binding.session, ConversationID: "44444444-4444-4444-8444-444444444444", WorkDir: binding.workdir})
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			var restored []byte
			observations, writes := 0, 0
			failure := errors.New("controlled preparation failure")
			go func() {
				done <- forwardPreparedLifecycleOutput(origin, offset, func() time.Time { return current }, func(d time.Duration) {
					current = current.Add(d)
					if tc.oversleep {
						current = current.Add(200 * time.Millisecond)
					}
				}, func() error {
					entered <- current
					<-release
					current = current.Add(tc.prepare)
					if tc.failPrepare {
						return failure
					}
					var err error
					restored, err = binding.Frame(response, true)
					return err
				}, func() error {
					observations++
					if current.Before(origin.Add(time.Duration(offset))) {
						return errors.New("early physical observation")
					}
					current = current.Add(tc.observe)
					if err := p.BeforeOutput(restored); err != nil {
						return err
					}
					return forwardLifecycleFrame(restored, nil, func(actual []byte) error {
						writes++
						current = current.Add(tc.write)
						if !bytes.Equal(actual, restored) {
							return errors.New("changed literal output")
						}
						return nil
					})
				})
			}()
			select {
			case start := <-entered:
				if start != origin.Add(tc.late) {
					t.Error("restoration started after spending the captured gap")
				}
			case <-time.After(5 * time.Second):
				t.Error("preparation gate was not reached")
			}
			close(release)
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("scheduling proof did not join")
			}
			var timing *LifecycleTimingError
			if tc.failPrepare {
				if !errors.Is(err, failure) || observations != 0 || writes != 0 {
					t.Fatal("preparation failure reached delivery", err)
				}
			} else if tc.wantStage != "" {
				if !errors.As(err, &timing) || timing.Stage != tc.wantStage {
					t.Fatalf("wanted timing rejection %s, got %v", tc.wantStage, err)
				}
			} else if err != nil || observations != 1 || writes != 1 {
				t.Fatal("restoration consumed the existing forwarding allowance", err)
			}
			if len(p.facts) != 0 || len(p.operations) != 0 {
				t.Fatal("control response invented physical proof")
			}
			t.Logf("due_ns=%d completed_ns=%d observations=%d writes=%d stage=%s", offset, current.Sub(origin).Nanoseconds(), observations, writes, tc.wantStage)
		})
	}
}

func TestComposerOutputValidationRejectsAmbiguousCarriers(t *testing.T) {
	for name, raw := range map[string]string{
		"duplicate-root":     `{"type":"control_response","type":"assistant"}`,
		"duplicate-nested":   `{"type":"control_response","response":{"value":1,"value":2}}`,
		"escaped-credential": `{"type":"control_response","response":"\u0073k-ant-synthetic-credential-1234567890"}`,
		"nested-carrier":     `{"type":"control_response","response":"{\"shadow\":\"\\u0073k-ant-synthetic-credential-1234567890\",\"shadow\":\"clean\"}"}`,
		"foreign-parent":     `{"type":"control_response","parent_tool_use_id":"foreign"}`,
		"malformed-parent":   `{"type":"control_response","parent_tool_use_id":32}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := "/tmp/composer-validation"
			p, err := NewPhysicalNativeObserver(context.Background(), home, "composer", LifecycleRoleBinding{Role: "bootstrap", SessionID: "33333333-3333-4333-8333-333333333333", ConversationID: "44444444-4444-4444-8444-444444444444", WorkDir: home + "/work"})
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if p.BeforeOutput([]byte(raw)) == nil || p.failed == nil || len(p.events) != 0 || len(p.facts) != 0 || len(p.operations) != 0 {
				t.Fatal("unsafe response reached physical parsing or proof")
			}
		})
	}
}
