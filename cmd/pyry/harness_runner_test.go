package main

import (
	"errors"
	"testing"

	"github.com/pyrycode/pyrycode/internal/sessions"
)

// TestHarnessRunnerFactory_SelectsByHarness: claude and the empty harness reach
// the claude factory, codex reaches the codex factory (#2620), each with the
// config intact; any other harness is refused without either being called
// (#2593).
func TestHarnessRunnerFactory_SelectsByHarness(t *testing.T) {
	errClaude := errors.New("claude factory reached")
	errCodex := errors.New("codex factory reached")
	tests := []struct {
		harness string
		want    error
	}{
		{harness: "", want: errClaude},
		{harness: sessions.HarnessClaude, want: errClaude},
		{harness: "codex", want: errCodex},
		{harness: "gemini", want: nil},
	}
	for _, tc := range tests {
		t.Run("harness="+tc.harness, func(t *testing.T) {
			var got *sessions.RunnerConfig
			reached := func(err error) sessions.RunnerFactory {
				return func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
					got = &cfg
					return nil, err
				}
			}
			factory := harnessRunnerFactory(reached(errClaude), reached(errCodex))
			_, err := factory(sessions.RunnerConfig{SessionID: "s-1", Harness: tc.harness})
			if tc.want != nil {
				if !errors.Is(err, tc.want) || got == nil || got.SessionID != "s-1" {
					t.Fatalf("err = %v, config = %+v; want %v with the config", err, got, tc.want)
				}
				return
			}
			if err == nil || errors.Is(err, errClaude) || errors.Is(err, errCodex) {
				t.Fatalf("err = %v, want a refusal from neither factory", err)
			}
			if got != nil {
				t.Fatalf("a factory was called with %+v for harness %q", got, tc.harness)
			}
		})
	}
}
