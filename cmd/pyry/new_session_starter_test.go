package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/sessions"
)

// --- #2099 activeSessionStarter composition ---
//
// activeSessionStarter had NO unit test before #2099: its only mention in the test
// tree was a flow comment in the e2e suite. It gets one here because the id it
// resolves is now client-supplied, which turns its arm selection into the ticket's
// whole security surface — every AC-4 row is one of these arms, and each is far
// cheaper to pin here than through a daemon.
//
// All three seams are plain func fields, so the fakes are the seams themselves.
// conversations.ValidID is NOT injected: it is a pure function of a string, so
// feeding a malformed id exercises the real validator rather than a stand-in that
// could disagree with it.

const (
	starterConvA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" // the cursor's conversation
	starterConvB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" // the conversation the frame names
)

// restartFreshRunner is the minimal runner startFreshRunner recognises: it exposes
// RestartFresh and nothing else, so it takes the rotate-and-respawn arm. It records
// the id it was respawned under, which is what proves the rotation reached the
// runner rather than merely being computed.
type restartFreshRunner struct {
	baseRunner
	restarts []string
}

func (r *restartFreshRunner) RestartFresh(sessionID string) {
	r.restarts = append(r.restarts, sessionID)
}

// starterProbe captures what each seam was asked, so a test can assert on the
// QUESTION rather than only on the answer. Which id reached resolveBound is the
// single most load-bearing observation in this file: it is the difference between
// rotating the conversation the client named and rotating the cursor's.
type starterProbe struct {
	resolvedWith []string
	rotatedFrom  []sessions.SessionID
	logs         bytes.Buffer
}

// newStarter builds an activeSessionStarter over the probe, binding boundConv to
// runner. Any other conversation id resolves as unknown — which is also how an
// unbound one resolves, exactly as resolveBoundSession refuses both identically.
func (p *starterProbe) newStarter(cursor, boundConv string, runner sessions.Runner, rotateErr error) activeSessionStarter {
	return activeSessionStarter{
		currentConv: func() string { return cursor },
		resolveBound: func(convID string) (sessions.Runner, sessions.SessionID, bool) {
			p.resolvedWith = append(p.resolvedWith, convID)
			if runner == nil || convID != boundConv {
				return nil, "", false
			}
			return runner, sessions.SessionID("session-of-" + convID), true
		},
		rotate: func(old sessions.SessionID) (sessions.SessionID, error) {
			p.rotatedFrom = append(p.rotatedFrom, old)
			if rotateErr != nil {
				return "", rotateErr
			}
			return sessions.SessionID("fresh-" + string(old)), nil
		},
		log: slog.New(slog.NewJSONHandler(&p.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

// TestActiveSessionStarter_NamedConversationRotatesThatOne is AC-1 at the
// composition level: with the cursor parked on A and the frame naming B, every
// seam is asked about B and B alone.
//
// The cursor is deliberately set to a DIFFERENT bound conversation rather than
// left empty. Against an empty cursor a regression that ignored the named id
// would resolve "" and land inert, which reads as a pass; against A it would
// rotate A, which is the actual defect and reddens here.
func TestActiveSessionStarter_NamedConversationRotatesThatOne(t *testing.T) {
	t.Parallel()

	runner := &restartFreshRunner{}
	p := &starterProbe{}
	s := p.newStarter(starterConvA, starterConvB, runner, nil)

	if err := s.StartNewSession(starterConvB); err != nil {
		t.Fatalf("StartNewSession(%q) = %v, want nil", starterConvB, err)
	}

	if got := p.resolvedWith; len(got) != 1 || got[0] != starterConvB {
		t.Fatalf("resolveBound asked about %q, want exactly [%q] — the cursor's conversation must "+
			"never be consulted when the frame names one", got, starterConvB)
	}
	if got := p.rotatedFrom; len(got) != 1 || got[0] != sessions.SessionID("session-of-"+starterConvB) {
		t.Fatalf("rotate called with %q, want the session bound to the NAMED conversation", got)
	}
	if got := runner.restarts; len(got) != 1 || got[0] != "fresh-session-of-"+starterConvB {
		t.Fatalf("RestartFresh got %q, want the freshly minted id — the respawn must reach the runner", got)
	}
}

// TestActiveSessionStarter_UnnamedFollowsTheCursor is AC-3 at the composition
// level: an un-upgraded client's bare frame behaves exactly as before #2099.
func TestActiveSessionStarter_UnnamedFollowsTheCursor(t *testing.T) {
	t.Parallel()

	runner := &restartFreshRunner{}
	p := &starterProbe{}
	s := p.newStarter(starterConvA, starterConvA, runner, nil)

	if err := s.StartNewSession(""); err != nil {
		t.Fatalf("StartNewSession(\"\") = %v, want nil", err)
	}

	if got := p.resolvedWith; len(got) != 1 || got[0] != starterConvA {
		t.Fatalf("resolveBound asked about %q, want exactly [%q] (the cursor)", got, starterConvA)
	}
	if len(runner.restarts) != 1 {
		t.Fatalf("RestartFresh called %d times, want 1: the bare frame must still rotate the cursor's "+
			"conversation", len(runner.restarts))
	}
}

// TestActiveSessionStarter_InertArms walks AC-4's whole reject set plus the
// pre-existing no-cursor arm. Every row asserts the same three things: nothing
// rotated, nothing respawned, and no error surfaced to the handler (inert is
// success of a valid request, not a failure).
//
// wantResolved is the load-bearing column. A malformed id must be refused BEFORE
// it reaches the registry, so its row wants zero resolveBound calls; the registry
// rows want exactly one. A shape check that ran after the lookup would pass every
// other assertion here and fail only this one.
func TestActiveSessionStarter_InertArms(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		cursor       string
		named        string
		boundConv    string
		runner       sessions.Runner
		wantResolved int
		wantEvent    string
	}{
		{
			name:   "no cursor and nothing named",
			cursor: "", named: "", boundConv: starterConvA, runner: &restartFreshRunner{},
			wantResolved: 0,
			wantEvent:    "v2.new_session.no_active_conv",
		},
		{
			name:   "named id is not a UUID",
			cursor: starterConvA, named: "not-a-uuid", boundConv: starterConvB, runner: &restartFreshRunner{},
			wantResolved: 0,
			wantEvent:    "v2.new_session.invalid_conv_id",
		},
		{
			// The traversal shape the security review named: it must die at the shape
			// check, before anything could treat it as a lookup key or a path.
			name:   "named id is a traversal attempt",
			cursor: starterConvA, named: "../../etc/passwd", boundConv: starterConvB, runner: &restartFreshRunner{},
			wantResolved: 0,
			wantEvent:    "v2.new_session.invalid_conv_id",
		},
		{
			name:   "named id is a UUID with the wrong version nibble",
			cursor: starterConvA, named: "bbbbbbbb-bbbb-1bbb-8bbb-bbbbbbbbbbbb", boundConv: starterConvB, runner: &restartFreshRunner{},
			wantResolved: 0,
			wantEvent:    "v2.new_session.invalid_conv_id",
		},
		{
			name:   "named id is uppercase",
			cursor: starterConvA, named: strings.ToUpper(starterConvB), boundConv: starterConvB, runner: &restartFreshRunner{},
			wantResolved: 0,
			wantEvent:    "v2.new_session.invalid_conv_id",
		},
		{
			// Unknown-to-the-registry and known-but-unbound are ONE arm on purpose:
			// resolveBoundSession refuses both identically and never falls through to
			// the bootstrap session Pool.Lookup("") would return (#678). That
			// non-distinction is also what denies a paired-but-hostile client an
			// existence oracle over conversation ids.
			name:   "named id is well-shaped but unknown or unbound",
			cursor: starterConvA, named: starterConvB, boundConv: starterConvA, runner: &restartFreshRunner{},
			wantResolved: 1,
			wantEvent:    "v2.new_session.no_bound_session",
		},
		{
			name:   "named conversation has no live child",
			cursor: starterConvA, named: starterConvB, boundConv: starterConvB, runner: baseRunner{},
			wantResolved: 1,
			wantEvent:    "v2.new_session.no_restart",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := &starterProbe{}
			s := p.newStarter(tc.cursor, tc.boundConv, tc.runner, nil)

			if err := s.StartNewSession(tc.named); err != nil {
				t.Fatalf("StartNewSession(%q) = %v, want nil: an inert arm is success of a valid "+
					"request, not an error the handler should Warn about", tc.named, err)
			}

			if got := len(p.resolvedWith); got != tc.wantResolved {
				t.Errorf("resolveBound called %d times (%q), want %d", got, p.resolvedWith, tc.wantResolved)
			}
			if len(p.rotatedFrom) != 0 {
				t.Errorf("rotate called %d times, want 0: an inert arm must never rotate a session id",
					len(p.rotatedFrom))
			}
			if r, ok := tc.runner.(*restartFreshRunner); ok && len(r.restarts) != 0 {
				t.Errorf("RestartFresh called %d times, want 0: an inert arm must never respawn a child",
					len(r.restarts))
			}
			assertStarterRecord(t, p.logs.Bytes(), tc.wantEvent, tc.named)
		})
	}
}

// assertStarterRecord pins AC-4's "one debug log line carrying the id". It checks
// the record is at DEBUG — the id is client-supplied, and promoting it to Info
// would put attacker-chosen bytes in a default-level daemon log — and that the id
// travels as its own structured field rather than being interpolated into the
// message, so a consumer can drop it without parsing prose.
func assertStarterRecord(t *testing.T, raw []byte, wantEvent, wantConvID string) {
	t.Helper()

	var found int
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("decode log record %q: %v", line, err)
		}
		if rec["event"] != wantEvent {
			continue
		}
		found++
		if rec["level"] != "DEBUG" {
			t.Errorf("record %q level = %v, want DEBUG (the id is client-supplied)", wantEvent, rec["level"])
		}
		// The no-cursor arm has no id to carry: its information IS its existence.
		if wantConvID == "" {
			continue
		}
		if got, ok := rec["conversation_id"].(string); !ok || got != wantConvID {
			t.Errorf("record %q conversation_id = %v, want %q", wantEvent, rec["conversation_id"], wantConvID)
		}
	}
	if found != 1 {
		t.Errorf("found %d records with event=%q, want exactly 1\nlog:\n%s", found, wantEvent, raw)
	}
}

// TestActiveSessionStarter_RotateErrorPropagates pins that a REAL failure is not
// swallowed by the inert arms around it: RotateForNewSession losing its race
// returns an error, and handleNewSession is the thing that Warn-logs it. An
// implementation that returned nil here would report a failed rotation as a clean
// one.
func TestActiveSessionStarter_RotateErrorPropagates(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("session vanished between resolve and rotate")
	p := &starterProbe{}
	s := p.newStarter(starterConvA, starterConvB, &restartFreshRunner{}, wantErr)

	if err := s.StartNewSession(starterConvB); !errors.Is(err, wantErr) {
		t.Fatalf("StartNewSession = %v, want %v", err, wantErr)
	}
}

// TestActiveSessionStarter_NilLoggerDoesNotPanic pins the logger() fallback.
// activeSessionStarter is a constructor-less bag of seams built as a named-field
// literal, so an omitted log field is a reachable state, and a nil *slog.Logger
// panics on first use — on a remotely-driven path that is a latent crash on a
// rarely-hit inert arm.
func TestActiveSessionStarter_NilLoggerDoesNotPanic(t *testing.T) {
	t.Parallel()

	s := activeSessionStarter{
		currentConv:  func() string { return starterConvA },
		resolveBound: func(string) (sessions.Runner, sessions.SessionID, bool) { return nil, "", false },
		rotate: func(sessions.SessionID) (sessions.SessionID, error) {
			t.Fatal("rotate must not be reached from an inert arm")
			return "", nil
		},
		// log intentionally nil.
	}

	if err := s.StartNewSession(starterConvB); err != nil {
		t.Fatalf("StartNewSession = %v, want nil", err)
	}
}

// TestBoundedConvID pins the security review's one actionable finding: the
// invalid-shape arm is the only place an arbitrary client string reaches a log
// call, bounded upstream only by the application-envelope cap. It must be
// truncated, and the truncation must COPY rather than slice the decoded payload,
// so a log record cannot pin the whole frame's allocation.
func TestBoundedConvID(t *testing.T) {
	t.Parallel()

	t.Run("a short id is passed through unchanged", func(t *testing.T) {
		if got := boundedConvID(starterConvB); got != starterConvB {
			t.Errorf("boundedConvID(%q) = %q, want it unchanged", starterConvB, got)
		}
	})

	t.Run("an oversized id is truncated and marked", func(t *testing.T) {
		got := boundedConvID(strings.Repeat("x", 4096))
		if len(got) > maxLoggedConvID+len("…(truncated)") {
			t.Errorf("boundedConvID length = %d, want it bounded near %d", len(got), maxLoggedConvID)
		}
		if !strings.Contains(got, "truncated") {
			t.Errorf("boundedConvID(oversized) = %q, want an elision marker so a reader cannot mistake "+
				"the prefix for the whole value", got)
		}
	})

	t.Run("the result does not alias the input", func(t *testing.T) {
		big := strings.Repeat("y", 4096)
		got := boundedConvID(big)
		// A slice of big would share its backing array; a copy does not. unsafe-free
		// check: the prefix is equal but the string headers must not overlap, which
		// strings.Clone guarantees and a bare big[:n] does not.
		if len(got) >= len(big) {
			t.Fatalf("boundedConvID did not truncate a %d-byte input", len(big))
		}
		if strings.HasPrefix(big, got) {
			t.Errorf("boundedConvID returned a bare prefix %q of its input: it must carry an elision "+
				"marker, which also proves it is not a slice of the original", got)
		}
	})
}
