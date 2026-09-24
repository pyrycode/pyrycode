package main

import (
	"errors"
	"testing"

	"github.com/pyrycode/pyrycode/internal/sessions"
)

// TestHarnessRunnerFactory_SelectsByHarness: claude and the empty harness reach
// the claude factory with the config intact; any other harness is refused
// without the claude factory ever being called (#2593).
func TestHarnessRunnerFactory_SelectsByHarness(t *testing.T) {
	errClaude := errors.New("claude factory reached")
	tests := []struct {
		harness    string
		wantClaude bool
	}{
		{harness: "", wantClaude: true},
		{harness: sessions.HarnessClaude, wantClaude: true},
		{harness: "codex", wantClaude: false},
	}
	for _, tc := range tests {
		t.Run("harness="+tc.harness, func(t *testing.T) {
			var got *sessions.RunnerConfig
			factory := harnessRunnerFactory(func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
				got = &cfg
				return nil, errClaude
			})
			_, err := factory(sessions.RunnerConfig{SessionID: "s-1", Harness: tc.harness})
			if tc.wantClaude {
				if !errors.Is(err, errClaude) || got == nil || got.SessionID != "s-1" {
					t.Fatalf("err = %v, config = %+v; want the claude factory called with the config", err, got)
				}
				return
			}
			if err == nil || errors.Is(err, errClaude) {
				t.Fatalf("err = %v, want a refusal that is not the claude factory's", err)
			}
			if got != nil {
				t.Fatalf("claude factory called with %+v for harness %q", got, tc.harness)
			}
		})
	}
}
