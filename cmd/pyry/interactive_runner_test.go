package main

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// TestSelectInteractiveRunner covers the #1081 composition-root selector after
// #1348 removed the terminal-driving interactive runner.
//
// The property that matters, and the reason this test is a gate rather than a
// description: NO configuration value selects a terminal runner. Before this,
// the empty value did, so an absent, empty, or hand-reset ~/.pyry/config.json
// brought the daemon up on the path four live specs failed against — silently,
// because an empty config is the shape a fresh install has. The explicit "pty"
// value is now a loud error, and it keeps its own arm so an operator with that
// key in a file is told the path was removed rather than that the value is
// unrecognised.
//
// This test outlives the code it was written against on purpose. With the
// terminal runner gone there is nothing for a default to fall back TO, so the
// only thing standing between a future contributor and a second path
// reappearing is this file.
func TestSelectInteractiveRunner(t *testing.T) {
	t.Parallel()

	logger := slog.Default()

	// assertStreamSelected proves the returned pair is the live stream wiring:
	// ONE sink instance handed to both the factory and the caller (the #1098
	// shared-instance property), demonstrated by pushing through sinkFor and
	// receiving the session-tagged envelope on the sink's own channel.
	assertStreamSelected := func(t *testing.T, value string) {
		t.Helper()
		factory, sink, err := selectInteractiveRunner(config.Config{InteractiveRunner: value}, logger, "")
		if err != nil {
			t.Fatalf("selectInteractiveRunner(%q) err = %v, want nil", value, err)
		}
		if factory == nil {
			t.Fatalf("selectInteractiveRunner(%q) factory = nil, want a non-nil stream-json RunnerFactory", value)
		}
		if sink == nil {
			t.Fatalf("selectInteractiveRunner(%q) sink = nil, want a non-nil turn-event sink", value)
		}
		sink.sinkFor("sess-x")(turnevent.TextChunk{MessageID: "m1", Text: "hi"})
		select {
		case env := <-sink.ch:
			if env.sessionID != "sess-x" {
				t.Errorf("envelope sessionID = %q, want %q", env.sessionID, "sess-x")
			}
		default:
			t.Errorf("returned sink did not receive the pushed event — not the live fan-in the factory feeds")
		}
	}

	t.Run("empty selects the stream runner, not a terminal one", func(t *testing.T) {
		t.Parallel()
		assertStreamSelected(t, "")
	})

	t.Run("stream-json selects the streamsup factory + a live sink", func(t *testing.T) {
		t.Parallel()
		assertStreamSelected(t, "stream-json")
	})

	t.Run("pty is rejected, and the error says it was removed", func(t *testing.T) {
		t.Parallel()
		factory, sink, err := selectInteractiveRunner(config.Config{InteractiveRunner: "pty"}, logger, "")
		if err == nil {
			t.Fatal(`selectInteractiveRunner("pty") err = nil, want an error — the terminal interactive runner was removed and must not be selectable`)
		}
		if factory != nil || sink != nil {
			t.Errorf("factory/sink = %v/%v, want nil/nil on error", factory, sink)
		}
		// An operator hitting this has a config file to edit, so the message has to
		// carry the ticket that explains the removal and the value to use instead.
		// Naming only the accepted set would read as a typo report for a value that
		// was correct until it was deleted.
		for _, want := range []string{"#1348", "stream-json"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q missing %q (must name the removal and the replacement)", err.Error(), want)
			}
		}
	})

	t.Run("unrecognised value aborts with an AC4 error, no fallback", func(t *testing.T) {
		t.Parallel()
		factory, sink, err := selectInteractiveRunner(config.Config{InteractiveRunner: "garbage"}, logger, "")
		if err == nil {
			t.Fatal(`selectInteractiveRunner("garbage") err = nil, want an error (no silent fallback)`)
		}
		if factory != nil || sink != nil {
			t.Errorf("factory/sink = %v/%v, want nil/nil on error", factory, sink)
		}
		// AC4: the error names the offending value AND the accepted set. "pty" is
		// deliberately NOT in that set any more, so this also catches a future
		// re-introduction of the removed value through the default arm.
		for _, want := range []string{"garbage", "stream-json"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q missing %q (must name the value and the accepted set)", err.Error(), want)
			}
		}
		if strings.Contains(err.Error(), `"pty"`) {
			t.Errorf("error %q offers \"pty\" as an accepted value; it was removed in #1348", err.Error())
		}
	})
}

// TestSelectsStreamRunner pins the predicate the composition root uses to decide
// whether to prepare the approval-tool config before the selector runs. It has
// to agree with selectInteractiveRunner on every value that reaches a factory,
// or a valid daemon starts with an empty --mcp-config.
func TestSelectsStreamRunner(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", true},
		{"stream-json", true},
		{"garbage", true}, // fails loudly one line later; the temp file is removed at shutdown
		{"pty", false},
	} {
		if got := selectsStreamRunner(config.Config{InteractiveRunner: tc.value}); got != tc.want {
			t.Errorf("selectsStreamRunner(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}
