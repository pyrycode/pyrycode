package main

import (
	"errors"
	"strings"
	"testing"
)

// TestRemovedVerbs_RouterRejects pins the #1493 arms in runArgs: the verbs
// #1348 deleted fail loudly instead of falling through to runSupervisor.
//
// This drives the router rather than the sentinel strings on purpose. The
// regression it exists to prevent IS the missing wiring — a test that only
// inspected the messages would stay green with the arms deleted, and green is
// exactly the state where `pyry attach` starts a daemon in the operator's cwd.
func TestRemovedVerbs_RouterRejects(t *testing.T) {
	// Load-bearing, though inert in the shipped tree: the arms return before
	// runSupervisor is reached. It is the guard on this test's RED state. With
	// an arm deleted, runArgs falls through to runSupervisor, whose first
	// substantive step is confineWorkdirToHome. Pointing HOME at a fresh temp
	// dir puts the test binary's cwd outside it, so containment rejects and
	// runSupervisor returns an error BEFORE trustMark writes ~/.claude.json,
	// before the control socket binds, before the relay is contacted, and
	// before claude is spawned. The mutant fails on errors.Is with no side
	// effects — which is the constraint the ticket states.
	t.Setenv("HOME", t.TempDir())

	for _, tc := range []struct {
		verb string
		want error
	}{
		{"attach", errAttachRemoved},
		{"acp", errACPRemoved},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			err := runArgs([]string{"pyry", tc.verb})
			if err == nil {
				t.Fatalf("runArgs([pyry %s]) err = nil, want an error — main exits non-zero only on a non-nil return", tc.verb)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("runArgs([pyry %s]) err = %v, want %v", tc.verb, err, tc.want)
			}
			msg := err.Error()
			// An operator reading `pyry help` typed this, so the message has to
			// name the verb and the ticket that explains where it went.
			for _, want := range []string{tc.verb, "#1348"} {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q missing %q (must name the verb and the removal)", msg, want)
				}
			}
			// ...and must not hand the dead verb back as an invocation. Asserted
			// against err.Error(), not main's rendering: main emits
			// "pyry: attach", which is not the banned "pyry attach".
			if banned := "pyry " + tc.verb; strings.Contains(msg, banned) {
				t.Errorf("error %q renders %q as something to run; the verb was removed in #1348", msg, banned)
			}
		})
	}
}

// TestHelpTextDropsRemovedVerbs pins that helpText no longer advertises what
// #1348 deleted. It splits fields and reads the verb after "pyry" rather than
// substring-matching: "acp" is a substring of the surviving "mcp-approve"
// entry three lines below the block this ticket removes.
func TestHelpTextDropsRemovedVerbs(t *testing.T) {
	t.Parallel()

	advertised := map[string]bool{}
	for _, line := range strings.Split(helpText, "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "pyry" {
			advertised[f[1]] = true
		}
	}

	// Controls first: without these the scan below is vacuous — a broken
	// predicate or an empty helpText would report "attach absent"
	// unconditionally. mcp-approve doubles as proof this is not a substring
	// match that would also swallow the "acp" in "mcp-approve".
	for _, want := range []string{"status", "mcp-approve"} {
		if !advertised[want] {
			t.Fatalf("helpText does not advertise %q — the scan found %v, so its absence claims prove nothing", want, advertised)
		}
	}

	for _, gone := range []string{"attach", "acp"} {
		if advertised[gone] {
			t.Errorf("helpText still advertises `pyry %s`; the verb was removed in #1348", gone)
		}
	}
}
