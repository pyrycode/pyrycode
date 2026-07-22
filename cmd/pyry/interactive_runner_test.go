package main

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// TestSelectInteractiveRunner covers the #1081 composition-root selector: the ""
// / "pty" rollback path returns a nil factory AND a nil sink (byte-identical PTY
// startup), "stream-json" returns a live factory + sink, and any other value
// aborts with an AC4 error that names the offending value and the accepted set —
// never a silent PTY fallback.
func TestSelectInteractiveRunner(t *testing.T) {
	t.Parallel()

	logger := slog.Default()

	t.Run("empty selects PTY: nil factory and nil sink", func(t *testing.T) {
		t.Parallel()
		factory, sink, err := selectInteractiveRunner(config.Config{InteractiveRunner: ""}, logger, "")
		if err != nil {
			t.Fatalf("selectInteractiveRunner(\"\") err = %v, want nil", err)
		}
		if factory != nil {
			t.Errorf("factory = non-nil, want nil (nil RunnerFactory keeps supervisor.New, the rollback path)")
		}
		if sink != nil {
			t.Errorf("sink = %v, want nil (PTY path has no stream sink)", sink)
		}
	})

	t.Run("pty selects PTY: nil factory and nil sink", func(t *testing.T) {
		t.Parallel()
		factory, sink, err := selectInteractiveRunner(config.Config{InteractiveRunner: "pty"}, logger, "")
		if err != nil {
			t.Fatalf("selectInteractiveRunner(\"pty\") err = %v, want nil", err)
		}
		if factory != nil {
			t.Errorf("factory = non-nil, want nil")
		}
		if sink != nil {
			t.Errorf("sink = %v, want nil", sink)
		}
	})

	t.Run("stream-json selects the streamsup factory + a live sink", func(t *testing.T) {
		t.Parallel()
		factory, sink, err := selectInteractiveRunner(config.Config{InteractiveRunner: "stream-json"}, logger, "")
		if err != nil {
			t.Fatalf("selectInteractiveRunner(\"stream-json\") err = %v, want nil", err)
		}
		if factory == nil {
			t.Fatalf("factory = nil, want a non-nil stream-json RunnerFactory")
		}
		if sink == nil {
			t.Fatalf("sink = nil, want a non-nil turn-event sink (fed to both the factory and the drain)")
		}
		// The selector constructs exactly ONE newStreamTurnSink and hands it to both
		// newStreamRunnerFactory and the caller (the shared-instance property #1098
		// requires). Prove the returned sink is that live, correctly-tagging fan-in:
		// a non-blocking sinkFor push lands a session-tagged envelope on its channel.
		sink.sinkFor("sess-x")(turnevent.TextChunk{MessageID: "m1", Text: "hi"})
		select {
		case env := <-sink.ch:
			if env.sessionID != "sess-x" {
				t.Errorf("envelope sessionID = %q, want %q", env.sessionID, "sess-x")
			}
		default:
			t.Errorf("returned sink did not receive the pushed event — not the live fan-in the factory feeds")
		}
	})

	t.Run("unrecognised value aborts with an AC4 error, no fallback", func(t *testing.T) {
		t.Parallel()
		factory, sink, err := selectInteractiveRunner(config.Config{InteractiveRunner: "garbage"}, logger, "")
		if err == nil {
			t.Fatalf("selectInteractiveRunner(\"garbage\") err = nil, want an error (no silent PTY fallback)")
		}
		if factory != nil || sink != nil {
			t.Errorf("factory/sink = %v/%v, want nil/nil on error", factory, sink)
		}
		// AC4: the error names the offending value AND the full accepted set.
		for _, want := range []string{"garbage", "pty", "stream-json"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q missing %q (must name the value and the accepted set)", err.Error(), want)
			}
		}
	})
}
