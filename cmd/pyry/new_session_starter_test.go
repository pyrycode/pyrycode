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

// starterLiveChildPID is the pid a runner reports when its child is up. Any
// non-zero value serves; a memorable one makes a State() that leaked the zero value
// obvious in a failure message.
const starterLiveChildPID = 4242

// restartFreshRunner is the minimal runner startFreshRunner recognises: it exposes
// RestartFresh and nothing else, so it takes the rotate-and-respawn arm. It records
// the id it was respawned under, which is what proves the rotation reached the
// runner rather than merely being computed.
//
// childPID IS THE PRODUCTION SHAPE and the reason this double exists in two
// configurations rather than one. #2099 first shipped with AC-4's no-live-child row
// injecting a runner that lacked RestartFresh entirely — a shape production never
// produces for a bound conversation, since the sole implementation (streamRunner)
// exposes the method unconditionally and the pool assigns it at mint time. The row
// greened on the capability arm and the real arm was never exercised, which is what
// let a created-but-unmessaged conversation rotate. A faithful double therefore
// offers RestartFresh ALWAYS and carries liveness in the state, exactly as
// streamRunner does: zero until its Run loop publishes a spawned child's pid.
type restartFreshRunner struct {
	baseRunner
	childPID int
	restarts []string
}

func (r *restartFreshRunner) State() sessions.State {
	if r.childPID == 0 {
		// The unstarted runner's own snapshot: streamsup.Runner.State reports the
		// zero value until Run is entered, and Run is not entered until Activate.
		return sessions.State{}
	}
	return sessions.State{Phase: sessions.PhaseRunning, ChildPID: r.childPID}
}

func (r *restartFreshRunner) RestartFresh(sessionID string) {
	r.restarts = append(r.restarts, sessionID)
}

// liveRunner is a bound conversation whose child is up — the only shape a named
// new_session may rotate.
func liveRunner() *restartFreshRunner { return &restartFreshRunner{childPID: starterLiveChildPID} }

// starterProbe captures what each seam was asked, so a test can assert on the
// QUESTION rather than only on the answer. Which id reached resolveBound is the
// single most load-bearing observation in this file: it is the difference between
// rotating the conversation the client named and rotating the cursor's.
type starterProbe struct {
	resolvedWith []string
	rotatedFrom  []sessions.SessionID
	// The three #2521 seams, recorded on the same terms as the two above: which
	// conversation was asked for a persisted binding, which session was asked
	// whether it has ever run, and which conversation was revived.
	dormantAsked []string
	everRanAsked []sessions.SessionID
	revivedWith  []string
	logs         bytes.Buffer
}

// newStarter builds an activeSessionStarter over the probe, binding boundConv to
// runner. Any other conversation id resolves as unknown — which is also how an
// unbound one resolves, exactly as resolveBoundSession refuses both identically.
func (p *starterProbe) newStarter(cursor, boundConv string, runner sessions.Runner, rotateErr error) activeSessionStarter {
	return activeSessionStarter{
		currentConv: func() string { return cursor },
		resolveBound: func(convID string) (sessions.Runner, sessions.SessionID, string, bool) {
			p.resolvedWith = append(p.resolvedWith, convID)
			if runner == nil || convID != boundConv {
				return nil, "", "", false
			}
			return runner, sessions.SessionID("session-of-" + convID), "", true
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

	runner := liveRunner()
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
//
// The runner deliberately reports NO LIVE CHILD, which is what makes this the pin
// for the liveness guard's named-only asymmetry rather than a plain happy path. The
// same runner under a NAMED frame is inert (see the no-live-child row below); under
// a bare frame it must still rotate, because before #2099 an evicted cursor
// conversation rotated and came back up under the fresh id, and AC-3 promises that
// path is untouched. A guard applied to both arms would redden exactly here.
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
			cursor: "", named: "", boundConv: starterConvA, runner: liveRunner(),
			wantResolved: 0,
			wantEvent:    "v2.new_session.no_active_conv",
		},
		{
			name:   "named id is not a UUID",
			cursor: starterConvA, named: "not-a-uuid", boundConv: starterConvB, runner: liveRunner(),
			wantResolved: 0,
			wantEvent:    "v2.new_session.invalid_conv_id",
		},
		{
			// The traversal shape the security review named: it must die at the shape
			// check, before anything could treat it as a lookup key or a path.
			name:   "named id is a traversal attempt",
			cursor: starterConvA, named: "../../etc/passwd", boundConv: starterConvB, runner: liveRunner(),
			wantResolved: 0,
			wantEvent:    "v2.new_session.invalid_conv_id",
		},
		{
			name:   "named id is a UUID with the wrong version nibble",
			cursor: starterConvA, named: "bbbbbbbb-bbbb-1bbb-8bbb-bbbbbbbbbbbb", boundConv: starterConvB, runner: liveRunner(),
			wantResolved: 0,
			wantEvent:    "v2.new_session.invalid_conv_id",
		},
		{
			name:   "named id is uppercase",
			cursor: starterConvA, named: strings.ToUpper(starterConvB), boundConv: starterConvB, runner: liveRunner(),
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
			cursor: starterConvA, named: starterConvB, boundConv: starterConvA, runner: liveRunner(),
			wantResolved: 1,
			wantEvent:    "v2.new_session.no_bound_session",
		},
		{
			// AC-4's fourth row, in the shape production actually produces: a real
			// runner — RestartFresh and all — whose child has never spawned, which since
			// #2085 is the natural state of a conversation created but never messaged.
			// Rotating it rekeys the pool, persists sessions.json, rebinds the
			// conversation and broadcasts a session_transition for a chat that has never
			// had a turn, while RestartFresh spawns nothing. wantResolved is 1 and
			// rotatedFrom must stay empty: the refusal lands AFTER the registry resolves
			// it (the conversation is genuinely known and bound) and BEFORE any rotation.
			name:   "named conversation is bound but has no live child",
			cursor: starterConvA, named: starterConvB, boundConv: starterConvB, runner: &restartFreshRunner{},
			wantResolved: 1,
			wantEvent:    "v2.new_session.no_live_child",
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
	assertStarterRecordAt(t, raw, wantEvent, wantConvID, "DEBUG")
}

// assertStarterRecordAt is assertStarterRecord with the level named. It exists
// because #2521's revive failure is deliberately at WARN and not DEBUG: that arm
// reports the daemon's OWN stored state failing its own validator rather than a
// string a client just sent, which is the split resolveSpawnDir already makes for
// its spawn_dir_rejected record.
//
// It also pins the confidentiality property that arm turns on: the record carries
// the event and the conversation id and NOTHING else that could name a path. The
// underlying errors — resolveSpawnDir's confinement rejection and Pool.Revive's
// build failure — both can name one, so a future edit that "helpfully" attached
// the error would redden here rather than in production.
func assertStarterRecordAt(t *testing.T, raw []byte, wantEvent, wantConvID, wantLevel string) {
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
		if rec["level"] != wantLevel {
			t.Errorf("record %q level = %v, want %s", wantEvent, rec["level"], wantLevel)
		}
		for _, banned := range []string{"err", "error", "path", "cwd", "workspace", "spawn_dir"} {
			if _, present := rec[banned]; present {
				t.Errorf("record %q carries a %q field: a phone-influenced workspace path must "+
					"never reach a log (#833, Pool.Revive's contract)", wantEvent, banned)
			}
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
	s := p.newStarter(starterConvA, starterConvB, liveRunner(), wantErr)

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
		resolveBound: func(string) (sessions.Runner, sessions.SessionID, string, bool) { return nil, "", "", false },
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

// --- #2521 dormant reset ---
//
// Two states reach these arms, and the whole ticket is that they are NOT the same
// state as "created but never messaged": a session the pool retains with no
// running child, and one the pool has not materialised at all after a daemon
// restart. The seams stay plain func fields, so the fakes are the seams.

// dormantSeams configures the three #2521 seams for one row. The zero value is
// the refusing shape — no persisted binding, never activated, nothing to revive —
// so a test states only the answer it is about.
type dormantSeams struct {
	dormantID  sessions.SessionID // "" → resolveDormant refuses (unknown / unbound)
	dormantCwd string
	everRan    bool
	revived    sessions.Runner
	reviveErr  error
}

// newDormantStarter builds a starter whose resolveBound ALWAYS refuses — the
// after-daemon-restart shape, where Pool.Lookup misses because New materialised
// only the bootstrap — with the three #2521 seams wired over the probe.
func (p *starterProbe) newDormantStarter(d dormantSeams) activeSessionStarter {
	s := p.newStarter(starterConvA, starterConvB, nil, nil)
	s.resolveDormant = func(convID string) (sessions.SessionID, string, bool) {
		p.dormantAsked = append(p.dormantAsked, convID)
		if d.dormantID == "" {
			return "", "", false
		}
		return d.dormantID, d.dormantCwd, true
	}
	s.everRan = func(id sessions.SessionID) bool {
		p.everRanAsked = append(p.everRanAsked, id)
		return d.everRan
	}
	s.reviveBound = func(convID string, oldID sessions.SessionID, cwd string) (sessions.Runner, error) {
		p.revivedWith = append(p.revivedWith, convID)
		if d.reviveErr != nil {
			return nil, d.reviveErr
		}
		return d.revived, nil
	}
	return s
}

// dormantBoundID is the session id the persisted binding points at — canonical,
// because everything downstream of resolveDormant treats it as one.
const dormantBoundID = sessions.SessionID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")

// TestActiveSessionStarter_InPoolDormantPreviouslyUsedRotates is AC-1's
// retained-in-the-pool half: a NAMED frame on a conversation whose runner reports
// no child but whose session has run before must rotate.
//
// The runner is the same &restartFreshRunner{} the no-live-child inert row uses,
// and that is the point — the two rows differ ONLY in what everRan answers, so
// this test and that one together state that the never-used refusal was
// distinguished rather than removed.
//
// The wrap-up seam is deliberately left nil: a session with no live child has no
// child to ask for a handoff note, so the rotation must take the synchronous path
// and this must not defer.
func TestActiveSessionStarter_InPoolDormantPreviouslyUsedRotates(t *testing.T) {
	t.Parallel()

	runner := &restartFreshRunner{}
	p := &starterProbe{}
	s := p.newStarter(starterConvA, starterConvB, runner, nil)
	s.everRan = func(id sessions.SessionID) bool {
		p.everRanAsked = append(p.everRanAsked, id)
		return true
	}

	if err := s.StartNewSession(starterConvB); err != nil {
		t.Fatalf("StartNewSession(%q) = %v, want nil", starterConvB, err)
	}

	wantOld := sessions.SessionID("session-of-" + starterConvB)
	if got := p.everRanAsked; len(got) != 1 || got[0] != wantOld {
		t.Fatalf("everRan asked about %q, want exactly [%q]: the question is about the session "+
			"bound to the NAMED conversation", got, wantOld)
	}
	if got := p.rotatedFrom; len(got) != 1 || got[0] != wantOld {
		t.Fatalf("rotate called with %q, want exactly [%q]", got, wantOld)
	}
	if got := runner.restarts; len(got) != 1 || got[0] != "fresh-"+string(wantOld) {
		t.Fatalf("RestartFresh got %q, want the freshly minted id: a dormant reset must still "+
			"reach the runner, or the next message resumes the retired transcript", got)
	}
}

// TestActiveSessionStarter_AfterRestartDormantRevivesAndRotates is AC-1's other
// half and the reported defect: resolveBound refuses because the pool never
// materialised the session, and the frame must still rotate.
//
// revivedWith carries the CONVERSATION id rather than the session id because that
// is what Pool.Revive takes as its label — matching what create_conversation
// originally minted the session with, the posture sessionRouter.revive already
// keeps.
func TestActiveSessionStarter_AfterRestartDormantRevivesAndRotates(t *testing.T) {
	t.Parallel()

	runner := &restartFreshRunner{}
	p := &starterProbe{}
	s := p.newDormantStarter(dormantSeams{dormantID: dormantBoundID, everRan: true, revived: runner})

	if err := s.StartNewSession(starterConvB); err != nil {
		t.Fatalf("StartNewSession(%q) = %v, want nil", starterConvB, err)
	}

	if got := p.dormantAsked; len(got) != 1 || got[0] != starterConvB {
		t.Fatalf("resolveDormant asked about %q, want exactly [%q]", got, starterConvB)
	}
	if got := p.revivedWith; len(got) != 1 || got[0] != starterConvB {
		t.Fatalf("reviveBound called with %q, want exactly [%q]", got, starterConvB)
	}
	if got := p.rotatedFrom; len(got) != 1 || got[0] != dormantBoundID {
		t.Fatalf("rotate called with %q, want exactly [%q] — the PERSISTED binding", got, dormantBoundID)
	}
	if got := runner.restarts; len(got) != 1 || got[0] != "fresh-"+string(dormantBoundID) {
		t.Fatalf("RestartFresh got %q, want the freshly minted id", got)
	}
}

// TestActiveSessionStarter_DormantInertArms walks the dormant path's three
// refusals.
//
// wantRevive is the load-bearing column, and it is AC-3's "without registry
// mutation" stated as a claim about CALLS — no return value can carry it, because
// a frame refused before the revive and one refused after it look identical from
// outside. The never-activated row wants ZERO: Pool.Revive registers the session
// and persists sessions.json, so a gate that ran below it would mutate the
// registry for a conversation that must stay inert. The revive-failure row wants
// ONE, which is what keeps the zero above from being vacuously true of a seam
// that was simply never wired.
func TestActiveSessionStarter_DormantInertArms(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		seams      dormantSeams
		wantRevive int
		wantEvent  string
		wantLevel  string
	}{
		{
			// Unknown or unbound, resolved through the dormant path: it must land on
			// the SAME record the live path uses, so the frame still cannot be used to
			// ask "does this conversation exist?".
			name:      "no persisted binding",
			seams:     dormantSeams{},
			wantEvent: "v2.new_session.no_bound_session",
			wantLevel: "DEBUG",
		},
		{
			// The after-restart twin of the no-live-child row: a conversation created
			// but never messaged is a persisted entry too, and reviving and rotating it
			// would draw a delimiter for a chat that has never had a turn.
			name:      "persisted binding that has never been activated",
			seams:     dormantSeams{dormantID: dormantBoundID},
			wantEvent: "v2.new_session.dormant_never_used",
			wantLevel: "DEBUG",
		},
		{
			// The workspace the conversation recorded no longer passes confinement, or
			// the pool refused the materialisation. Inert, and the record carries no
			// error and no path.
			name:       "revive fails",
			seams:      dormantSeams{dormantID: dormantBoundID, everRan: true, reviveErr: errors.New("boom")},
			wantRevive: 1,
			wantEvent:  "v2.new_session.revive_failed",
			wantLevel:  "WARN",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := &starterProbe{}
			s := p.newDormantStarter(tc.seams)

			if err := s.StartNewSession(starterConvB); err != nil {
				t.Fatalf("StartNewSession(%q) = %v, want nil: an inert arm is success of a valid "+
					"request, not an error the handler should Warn about", starterConvB, err)
			}
			if got := len(p.revivedWith); got != tc.wantRevive {
				t.Errorf("reviveBound called %d times, want %d: a refused frame must mutate no registry",
					got, tc.wantRevive)
			}
			if len(p.rotatedFrom) != 0 {
				t.Errorf("rotate called %d times, want 0", len(p.rotatedFrom))
			}
			assertStarterRecordAt(t, p.logs.Bytes(), tc.wantEvent, starterConvB, tc.wantLevel)
		})
	}
}

// TestActiveSessionStarter_BareFrameNeverConsultsEverRan pins the asymmetry #2099
// introduced and this ticket keeps: the liveness guard is NAMED-ONLY, so a bare
// frame on a childless cursor conversation still rotates without the new gate
// being asked at all.
//
// everRan answers FALSE here deliberately. If the gate were applied to the bare
// path too, this frame would go inert — which is exactly the pre-#2099 behaviour
// AC-3 of that ticket promised not to disturb, and it would redden here.
func TestActiveSessionStarter_BareFrameNeverConsultsEverRan(t *testing.T) {
	t.Parallel()

	runner := &restartFreshRunner{}
	p := &starterProbe{}
	s := p.newStarter(starterConvA, starterConvA, runner, nil)
	s.everRan = func(id sessions.SessionID) bool {
		p.everRanAsked = append(p.everRanAsked, id)
		return false
	}

	if err := s.StartNewSession(""); err != nil {
		t.Fatalf("StartNewSession(\"\") = %v, want nil", err)
	}
	if len(p.everRanAsked) != 0 {
		t.Errorf("everRan asked %q, want no calls: the bare frame's rotation is #2099's preserved "+
			"path and must not acquire a new gate", p.everRanAsked)
	}
	if len(runner.restarts) != 1 {
		t.Errorf("RestartFresh called %d times, want 1", len(runner.restarts))
	}
}
