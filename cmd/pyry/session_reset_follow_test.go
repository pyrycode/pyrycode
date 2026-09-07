package main

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// resetFollowSessionA / …B are the two session ids an announced reset moves
// between. They are the shape transcript.ValidStem admits (canonical lowercase
// UUID stems), because that is what streamsup's emitConversationReset lets past
// and therefore the only shape the follower can ever be handed.
const (
	resetFollowSessionA = "aaaaaaaa-1111-4111-8111-111111111111"
	resetFollowSessionB = "bbbbbbbb-2222-4222-8222-222222222222"
	resetFollowSessionC = "cccccccc-3333-4333-8333-333333333333"
	// resetFollowSessionM is the id a DAEMON-DRIVEN rotation mints while an announced
	// reset is in flight — RotateForNewSession's or RotateBootstrapForSelfHeal's own
	// value, never one claude named. It is the id the registry ends on in #2176's
	// reproduction, and therefore the one the tag must end on too.
	resetFollowSessionM = "dddddddd-4444-4444-8444-444444444444"
)

// recordingAdopt is a sessions.RunnerConfig.AdoptAnnouncedReset double that
// records every (oldID, newID) pair it is handed and answers a scripted error.
// Recording the PAIR rather than a count is what lets a test assert the follower
// read the LIVE tag: the second reset's oldID is the first reset's newID or the
// test is meaningless.
type recordingAdopt struct {
	mu    sync.Mutex
	calls [][2]string
	err   error
}

func (r *recordingAdopt) fn(oldID, newID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, [2]string{oldID, newID})
	return r.err
}

func (r *recordingAdopt) recorded() [][2]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][2]string(nil), r.calls...)
}

// recordingAdoptRunner is a (*streamsup.Runner).AdoptSessionID double that records
// every id it is handed. Recording the IDS rather than a count is what lets a test
// assert the runner was moved onto the ANNOUNCED id rather than merely poked: an
// adoption of the wrong id resumes the wrong transcript on the next crash, which is
// the whole defect #2136 closes.
type recordingAdoptRunner struct {
	mu  sync.Mutex
	ids []string
}

func (r *recordingAdoptRunner) fn(newID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, newID)
}

func (r *recordingAdoptRunner) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

// collectSink is a downstream sink double that appends every event it is
// forwarded, so a test can assert the decorator forwards unchanged.
type collectSink struct {
	mu   sync.Mutex
	evs  []turnevent.Event
	seen []string // the tag as read AT forward time, one per event
	tag  *streamSessionTag
}

func (c *collectSink) fn(ev turnevent.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evs = append(c.evs, ev)
	if c.tag != nil {
		c.seen = append(c.seen, c.tag.ID())
	}
}

func (c *collectSink) events() []turnevent.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]turnevent.Event(nil), c.evs...)
}

// TestSessionResetFollower_RotatesTagAndRekeysPool pins AC 1's daemon half and the
// first clause of AC 2: an announced reset to a DIFFERENT id moves the runner's
// live tag onto the announced id and hands the pool exactly one (old, new) pair to
// re-key. The forward assertion rides along because a decorator that acted and
// then swallowed the event would pass every other test here.
func TestSessionResetFollower_RotatesTagAndRekeysPool(t *testing.T) {
	t.Parallel()

	tag := newStreamSessionTag(resetFollowSessionA)
	adopt := &recordingAdopt{}
	next := &collectSink{tag: tag}
	f := newSessionResetFollower(tag, adopt.fn, next.fn, discardLogger())

	f.Sink(turnevent.ConversationReset{NewConversationID: resetFollowSessionB})

	if got := tag.ID(); got != resetFollowSessionB {
		t.Errorf("tag.ID() = %q after the announcement, want the announced id %q", got, resetFollowSessionB)
	}
	want := [][2]string{{resetFollowSessionA, resetFollowSessionB}}
	if got := adopt.recorded(); len(got) != 1 || got[0] != want[0] {
		t.Errorf("pool re-key calls = %v, want exactly %v", got, want)
	}
	if got := next.events(); len(got) != 1 {
		t.Fatalf("forwarded %d events, want the reset forwarded unchanged", len(got))
	}
}

// TestSessionResetFollower_SecondResetRotatesFromTheFirst pins AC 2's second
// clause — "still reaches it after a SECOND announced reset in the same session".
//
// The load-bearing assertion is the second call's OLD id. A follower that captured
// the runner's construction-time session id instead of reading the live tag passes
// the single-reset test above and fails only here, which is exactly the defect
// #1133 fixed on the RestartFresh path and which this seam would otherwise
// reintroduce: the pool would be asked to rotate an id it no longer has, the
// re-key would be refused, and the conversation would go dark from the second
// reset onward.
func TestSessionResetFollower_SecondResetRotatesFromTheFirst(t *testing.T) {
	t.Parallel()

	tag := newStreamSessionTag(resetFollowSessionA)
	adopt := &recordingAdopt{}
	f := newSessionResetFollower(tag, adopt.fn, nil, discardLogger())

	f.Sink(turnevent.ConversationReset{NewConversationID: resetFollowSessionB})
	f.Sink(turnevent.ConversationReset{NewConversationID: resetFollowSessionC})

	want := [][2]string{
		{resetFollowSessionA, resetFollowSessionB},
		{resetFollowSessionB, resetFollowSessionC},
	}
	got := adopt.recorded()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("pool re-key calls = %v, want %v — the second reset must rotate FROM the first's id", got, want)
	}
	if id := tag.ID(); id != resetFollowSessionC {
		t.Errorf("tag.ID() = %q, want %q", id, resetFollowSessionC)
	}
}

// TestSessionResetFollower_EqualIDChangesNothing pins AC 3 in full: an announced id
// equal to the session's current one performs no re-key, no transition and no tag
// rotation. The pool half of AC 3 is pinned separately in internal/sessions; this is
// the half only the follower can answer, since the pool cannot guard a tag it does
// not own.
func TestSessionResetFollower_EqualIDChangesNothing(t *testing.T) {
	t.Parallel()

	tag := newStreamSessionTag(resetFollowSessionA)
	adopt := &recordingAdopt{}
	runner := &recordingAdoptRunner{}
	next := &collectSink{}
	f := newSessionResetFollower(tag, adopt.fn, next.fn, discardLogger())
	f.adoptRunner = runner.fn

	f.Sink(turnevent.ConversationReset{NewConversationID: resetFollowSessionA})

	if got := adopt.recorded(); len(got) != 0 {
		t.Errorf("pool re-key calls = %v, want none — an equal id must not reach the pool", got)
	}
	if got := runner.recorded(); len(got) != 0 {
		t.Errorf("runner adopted %v, want nothing — the runner already holds this id (#2136 AC4)", got)
	}
	if id := tag.ID(); id != resetFollowSessionA {
		t.Errorf("tag.ID() = %q, want the unchanged %q", id, resetFollowSessionA)
	}
	if got := next.events(); len(got) != 1 {
		t.Errorf("forwarded %d events, want the reset still forwarded unchanged", len(got))
	}
}

// TestSessionResetFollower_PoolAnswerDecidesTheTag pins the ordering decision the
// type's doc argues, and both rows have an inverse that is a REGRESSION rather than
// a missing feature — different regressions, which is why the two sentinels cannot
// share a row.
//
// A nil answer is this call having re-keyed — since #2137 the ordinary path, and the
// only answer that means the registry now stands on the announced id. It is a row
// rather than a separate test so the biconditional below is asserted over ALL THREE
// answers rather than over the two that decline.
//
// ErrSessionNotFound is what the pool answers when oldID is already gone. Until #2137
// that was every reset, because the rotation watcher observed the same rotation first
// and re-keyed ONTO THE SAME ANNOUNCED ID, which is what made keeping the tag there
// right. #2137 retired the watcher and the premise went with it: this row now models
// the producers that survive — a daemon-driven RotateForNewSession or
// RotateBootstrapForSelfHeal, which re-key onto a freshly MINTED id, or a Remove /
// eviction / create-rollback, which leave no successor at all. None of them can put
// the session on the announced id, so the follower declines and unwinds (#2176), and
// the regression this row now guards is the mirror image of the one it guarded
// before: a follower that kept the tag here leaves the registry on one id and the tag
// on another, and the drain's gate takes the conversation dark exactly as it would
// have on the old inverse.
//
// ErrSessionIDTaken is the opposite and looks alike only from the error's side.
// AdoptAnnouncedID returns it BEFORE rekeyLocked, so nothing was re-keyed and no
// conversation was rebound; the announced id belongs to a DIFFERENT live session. A
// tag left rotated there stamps this runner's later events with the other session's
// id, and the gate then ADMITS them into that conversation — one line on the
// supervised child's stdout moving a conversation's output to another conversation.
//
// The assertion each row ends on is therefore the CONSEQUENCE, not the field: the tag
// is only observable through the envelopes it stamps, so both rows feed a later event
// through the real sinkForTag and read back the id it actually carries. A tag
// assertion alone would pass a hypothetical follower that rotated correctly and
// stamped from somewhere else.
//
// #2136 adds the runner's own id to each row, and DERIVES its expectation from
// wantTag rather than declaring it, because the rule is a biconditional: the runner
// ends on the announced id exactly when the tag does. Written that way the table
// asserts the rule itself, a row added for a new sentinel gets the matching
// expectation for free, and a follower that adopted on one qualifying answer but not
// the other cannot pass. Its inverse is the third distinct regression here: a runner
// left on the pre-reset id spawns --resume <pre-reset> on its next crash, silently
// resurrecting the conversation the announcement cleared.
func TestSessionResetFollower_PoolAnswerDecidesTheTag(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		err     error
		wantTag string
		why     string
	}{
		{
			name: "pool re-keyed", err: nil,
			wantTag: resetFollowSessionB,
			why:     "this call performed the re-key, so the registry and the tag agree on the announced id",
		},
		{
			name: "session moved elsewhere or is gone", err: sessions.ErrSessionNotFound,
			wantTag: resetFollowSessionA,
			why: "every surviving producer of this sentinel puts the session on a MINTED id or on none, " +
				"so a tag kept on the announced id names something the registry never went to",
		},
		{
			name: "announced id already taken", err: sessions.ErrSessionIDTaken,
			wantTag: resetFollowSessionA,
			why:     "the pool refused and moved nothing, and a tag left rotated delivers this runner's events into the other session's conversation",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := newStreamTurnSink(8, discardLogger())
			tag := newStreamSessionTag(resetFollowSessionA)
			adopt := &recordingAdopt{err: tc.err}
			runner := &recordingAdoptRunner{}
			f := newSessionResetFollower(tag, adopt.fn, sink.sinkForTag(tag.ID), discardLogger())
			f.adoptRunner = runner.fn

			f.Sink(turnevent.ConversationReset{NewConversationID: resetFollowSessionB})
			if got := tag.ID(); got != tc.wantTag {
				t.Errorf("tag.ID() = %q after a %v from the pool, want %q — %s", got, tc.err, tc.wantTag, tc.why)
			}

			// The biconditional, derived rather than declared: the runner moves exactly
			// when the tag ended on the announced id.
			var wantAdopted []string
			if tc.wantTag == resetFollowSessionB {
				wantAdopted = []string{resetFollowSessionB}
			}
			if got := runner.recorded(); !slices.Equal(got, wantAdopted) {
				t.Errorf("runner adopted %v after a %v from the pool, want %v — the runner's id must end "+
					"where the tag ends, or its next crash respawn resumes the wrong transcript", got, tc.err, wantAdopted)
			}

			f.Sink(turnevent.TextChunk{MessageID: "m-after", Text: "AFTER-THE-ANSWER"})
			for i, id := range drainEnvelopeSessionIDs(sink) {
				if id != tc.wantTag {
					t.Errorf("envelope %d carries session id %q, want %q — %s", i, id, tc.wantTag, tc.why)
				}
			}
		})
	}
}

// drainEnvelopeSessionIDs empties the fan-in and returns the session id each
// envelope carries, in order. Non-blocking: the sink has no drain attached here, so
// what it holds is exactly what the closure stamped.
func drainEnvelopeSessionIDs(sink *streamTurnSink) []string {
	var ids []string
	for {
		select {
		case env := <-sink.ch:
			ids = append(ids, env.sessionID)
		default:
			return ids
		}
	}
}

// racingTag is a sessionTag double that fires a hook ONCE at the top of
// CompareAndSwap and then delegates to the real tag it embeds.
//
// It exists because #2176's first interleaving is otherwise not injectable: follow
// reads the live tag and conditionally writes it in two adjacent statements on one
// goroutine, and no external double sits between them. A competing rotation performed
// at the top of the delegated write IS that ordering, since the follower's goroutine
// does nothing else in the gap — which is exactly why the write has to be conditional
// rather than a store.
//
// The hook fires once so the UNWIND's own CompareAndSwap, on the rows that reach one,
// sees the tag the first firing left rather than a second rotation the reproduction
// does not contain.
type racingTag struct {
	*streamSessionTag
	once sync.Once
	hook func()
}

func (r *racingTag) CompareAndSwap(oldID, newID string) bool {
	if r.hook != nil {
		r.once.Do(r.hook)
	}
	return r.streamSessionTag.CompareAndSwap(oldID, newID)
}

// TestSessionResetFollower_CompetingRotationWins pins AC 2 of #2176: a daemon-driven
// rotation racing an announced reset ends with the tag on the id the REGISTRY holds,
// under both interleavings, and never on the announced id nor on the pre-announcement
// one.
//
// Both rows model the same production sequence. A phone's new_session reaches
// Pool.RotateForNewSession, which mints M, re-keys old → M under p.mu and only then
// returns; RestartFresh fires OnSessionRotate later still, which is tag.Rotate. So the
// registry reaching M strictly precedes the tag reaching it, and daemonRotate below
// keeps that order — the ordering is what makes "the tag ends where the tag was last
// written" and "the tag ends where the registry is" the same assertion.
//
// The pool then answers ErrSessionNotFound for the one reason that survives #2137: old
// is gone because that rotation re-keyed it onto a MINTED id. Neither row may end on
// the announced B (the registry never went there) nor on the pre-announcement A (the
// registry left it), which is why a bare unwind is not the fix and the writes have to
// be conditional in both directions.
//
// Each row asserts through the id a later event's envelope actually carries, not the
// tag field: a mis-tag is only harmful through what it stamps, and a tag-field
// assertion would pass a follower that stamped from somewhere else.
func TestSessionResetFollower_CompetingRotationWins(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		// landsBeforeWrite picks where the competing rotation is injected: before the
		// follower's own conditional write, or between that write and the pool's answer.
		landsBeforeWrite bool
		wantPoolCalls    int
		why              string
	}{
		{
			name: "before the follower's own tag write", landsBeforeWrite: true, wantPoolCalls: 0,
			why: "the follower's write must not land at all once the tag has moved, so the pool is never asked",
		},
		{
			name: "between that write and the pool's answer", landsBeforeWrite: false, wantPoolCalls: 1,
			why: "the follower's write landed and the pool was asked, so only the UNWIND can put the tag back — and it must not",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := newStreamTurnSink(8, discardLogger())
			live := newStreamSessionTag(resetFollowSessionA)
			runner := &recordingAdoptRunner{}

			// registry stands in for the pool entry: what AdoptAnnouncedID would find, and
			// what the drain's active-session gate would later compare against.
			registry := resetFollowSessionA
			daemonRotate := func() {
				registry = resetFollowSessionM
				live.Rotate(resetFollowSessionM)
			}

			adopt := &recordingAdopt{err: sessions.ErrSessionNotFound}
			var tag sessionTag = live
			poolFn := adopt.fn
			if tc.landsBeforeWrite {
				tag = &racingTag{streamSessionTag: live, hook: daemonRotate}
			} else {
				poolFn = func(oldID, newID string) error {
					daemonRotate()
					return adopt.fn(oldID, newID)
				}
			}

			f := newSessionResetFollower(tag, poolFn, sink.sinkForTag(live.ID), discardLogger())
			f.adoptRunner = runner.fn

			f.Sink(turnevent.ConversationReset{NewConversationID: resetFollowSessionB})

			if registry != resetFollowSessionM {
				t.Fatalf("registry = %q, want %q — the fixture never performed the competing rotation", registry, resetFollowSessionM)
			}
			if got := live.ID(); got != resetFollowSessionM {
				t.Errorf("tag.ID() = %q, want the id the registry holds %q — %s", got, resetFollowSessionM, tc.why)
			}
			if got := len(adopt.recorded()); got != tc.wantPoolCalls {
				t.Errorf("pool re-key calls = %d, want %d — %s", got, tc.wantPoolCalls, tc.why)
			}
			if got := runner.recorded(); len(got) != 0 {
				t.Errorf("runner adopted %v, want nothing — a runner left on the announced id spawns "+
					"--resume %s on its next crash while the registry is on %s", got, resetFollowSessionB, resetFollowSessionM)
			}

			f.Sink(turnevent.TextChunk{MessageID: "m-after", Text: "AFTER-THE-RACE"})
			ids := drainEnvelopeSessionIDs(sink)
			if len(ids) == 0 {
				t.Fatalf("no envelopes reached the fan-in, so the id assertion below would pass vacuously")
			}
			for i, id := range ids {
				if id != resetFollowSessionM {
					t.Errorf("envelope %d carries session id %q, want %q — the conversation is bound to the "+
						"registry's id, and the gate drops everything stamped with any other", i, id, resetFollowSessionM)
				}
			}
		})
	}
}

// TestSessionResetFollower_OneRecordPerDeclinedAnnouncement pins AC 4: every way the
// follower declines writes EXACTLY ONE record naming both the announced id and the id
// it read, and the two causes are told apart by the event key alone.
//
// "The session moved elsewhere or is gone" and "the announced id names a different
// live session" are different facts for an operator: the first is a race the daemon
// correctly stood down from, the second is claude naming an id that would have moved
// this conversation's output into another one. A single key for both would make the
// Warn-level record unreadable, and two records for one decline would make the count
// meaningless.
//
// The first two rows share a key deliberately — both ARE "another rotation won", and
// the record's current_session_id says where the tag actually ended, which is the
// operator-facing difference. announced_reset.already_applied does not survive at all:
// it named a case that is no longer "already applied", which is the whole of #2176.
func TestSessionResetFollower_OneRecordPerDeclinedAnnouncement(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		wire        func(live *streamSessionTag) (sessionTag, func(oldID, newID string) error)
		wantEvent   string
		wantCurrent string // "" = the key must be absent
	}{
		{
			name: "the tag moved before the follower's write",
			wire: func(live *streamSessionTag) (sessionTag, func(string, string) error) {
				return &racingTag{streamSessionTag: live, hook: func() { live.Rotate(resetFollowSessionM) }},
					func(string, string) error { return nil }
			},
			wantEvent: "announced_reset.superseded", wantCurrent: resetFollowSessionM,
		},
		{
			name: "the pool no longer has the session",
			wire: func(live *streamSessionTag) (sessionTag, func(string, string) error) {
				return live, func(string, string) error { return sessions.ErrSessionNotFound }
			},
			// The unwind lands here — nothing else moved the tag — so the record reports the
			// pre-announcement id, which is the whole point of AC 1.
			wantEvent: "announced_reset.superseded", wantCurrent: resetFollowSessionA,
		},
		{
			name: "the announced id names another live session",
			wire: func(live *streamSessionTag) (sessionTag, func(string, string) error) {
				return live, func(string, string) error { return sessions.ErrSessionIDTaken }
			},
			wantEvent: "announced_reset.refused",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			logger, buf := auditLogger()
			live := newStreamSessionTag(resetFollowSessionA)
			tag, adopt := tc.wire(live)
			f := newSessionResetFollower(tag, adopt, nil, logger)

			f.Sink(turnevent.ConversationReset{NewConversationID: resetFollowSessionB})

			recs := followRecords(t, buf)
			if len(recs) != 1 {
				t.Fatalf("wrote %d records for one declined announcement, want exactly 1: %v", len(recs), recs)
			}
			rec := recs[0]
			if got := rec["event"]; got != tc.wantEvent {
				t.Errorf("event = %v, want %q — the cause must be readable from this key alone, and "+
					"announced_reset.already_applied must not survive", got, tc.wantEvent)
			}
			if got := rec["session_id"]; got != resetFollowSessionB {
				t.Errorf("session_id = %v, want the announced id %q", got, resetFollowSessionB)
			}
			if got := rec["previous_session_id"]; got != resetFollowSessionA {
				t.Errorf("previous_session_id = %v, want the id the follower read %q", got, resetFollowSessionA)
			}
			got, present := rec["current_session_id"]
			switch {
			case tc.wantCurrent == "" && present:
				t.Errorf("current_session_id = %v, want it absent — the refusal record leaves the tag "+
					"where it read it and has nothing to report", got)
			case tc.wantCurrent != "" && got != tc.wantCurrent:
				t.Errorf("current_session_id = %v, want %q — this is the field that tells the two "+
					"superseded causes apart", got, tc.wantCurrent)
			}
		})
	}
}

// followRecords parses every JSON line in buf and returns the follower's own records —
// those carrying an "announced_reset." event key. auditRecords does not fit: it filters
// on one fixed msg, and these records deliberately carry different messages and levels.
func followRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var recs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		if ev, _ := rec["event"].(string); strings.HasPrefix(ev, "announced_reset.") {
			recs = append(recs, rec)
		}
	}
	return recs
}

// TestSessionResetFollower_ForwardsEveryVariantAndToleratesNilNext pins the decorator
// contract the four retention holds state: every event of every variant is forwarded
// unchanged, and a nil next forwards nothing rather than panicking.
func TestSessionResetFollower_ForwardsEveryVariantAndToleratesNilNext(t *testing.T) {
	t.Parallel()

	evs := []turnevent.Event{
		turnevent.TextChunk{MessageID: "m-1", Text: "hello"},
		turnevent.ConversationReset{NewConversationID: resetFollowSessionB},
		turnevent.TurnEnd{},
	}

	tag := newStreamSessionTag(resetFollowSessionA)
	next := &collectSink{}
	runner := &recordingAdoptRunner{}
	f := newSessionResetFollower(tag, nil, next.fn, discardLogger())
	f.adoptRunner = runner.fn
	for _, ev := range evs {
		f.Sink(ev)
	}
	if got := len(next.events()); got != len(evs) {
		t.Errorf("forwarded %d events, want all %d unchanged", got, len(evs))
	}
	// The tag still rotates with no pool wired: that half is this type's own, and a
	// runner built without the callback (a test pool) must not be left un-rotated.
	if id := tag.ID(); id != resetFollowSessionB {
		t.Errorf("tag.ID() = %q with a nil adopt, want %q", id, resetFollowSessionB)
	}
	// And the runner adopts along with it (#2136 AC3). The rule is about where the TAG
	// ends, not about which sentinel came back, so a follower with no pool callback at
	// all must move both halves rather than neither.
	if got := runner.recorded(); !slices.Equal(got, []string{resetFollowSessionB}) {
		t.Errorf("runner adopted %v with a nil adopt, want %v — the runner must follow the tag even "+
			"where there is no pool answer to wait for", got, []string{resetFollowSessionB})
	}

	nilNext := newSessionResetFollower(newStreamSessionTag(resetFollowSessionA), nil, nil, discardLogger())
	for _, ev := range evs {
		nilNext.Sink(ev) // must not panic, adoptRunner included
	}
}

// TestSessionResetFollower_ObservesPastASaturatedSink pins AC 5: with the turn-event
// fan-in saturated so that the reset event ITSELF is refused at the droppable
// watermark, the session is still re-keyed.
//
// It is TestSessionBackgroundTaskHold_RetainsPastASaturatedSink's shape, and the
// len(sink.ch) assertion BEFORE the reset is what makes the green a conjunction —
// "saturated, and observed anyway" — rather than a coincidence that the sink quietly
// queued the event after all.
//
// What it pins is that the observation does not depend on the fan-in ADMITTING the
// event, which is what rules out every observation point downstream of this channel —
// including busy.observe inside the drain loop, which the earlier design named and
// which fails exactly here. ConversationReset is droppable class (turnMarkFor's
// default arm, pinned by TestTurnMarkFor_TotalOverEveryVariant), so under load the
// sink refuses it; a reset lost under load is a conversation that goes dark for the
// rest of the session, which is when the daemon can least afford it.
func TestSessionResetFollower_ObservesPastASaturatedSink(t *testing.T) {
	t.Parallel()

	sink := newStreamTurnSink(1, discardLogger())
	tag := newStreamSessionTag(resetFollowSessionA)
	// Fill the one slot with a droppable event, so the reset below is refused at
	// droppableCap rather than admitted.
	sink.sinkForTag(tag.ID)(turnevent.TextChunk{MessageID: "m-0", Text: "filler"})
	if got := len(sink.ch); got != 1 {
		t.Fatalf("fan-in holds %d envelopes after the filler, want 1 (the fixture is not saturated)", got)
	}

	adopt := &recordingAdopt{}
	f := newSessionResetFollower(tag, adopt.fn, sink.sinkForTag(tag.ID), discardLogger())
	f.Sink(turnevent.ConversationReset{NewConversationID: resetFollowSessionB})

	if got := len(sink.ch); got != 1 {
		t.Errorf("fan-in holds %d envelopes, want 1 — the reset must have been refused, not queued", got)
	}
	want := [][2]string{{resetFollowSessionA, resetFollowSessionB}}
	if got := adopt.recorded(); len(got) != 1 || got[0] != want[0] {
		t.Errorf("pool re-key calls = %v after a reset the saturated sink dropped, want %v", got, want)
	}
	if id := tag.ID(); id != resetFollowSessionB {
		t.Errorf("tag.ID() = %q, want %q", id, resetFollowSessionB)
	}
}

// TestSessionResetFollower_ConcurrentSinkAndTagRead is the -race arm. The writer is
// production's stdout forwarder goroutine running Sink; the reader is every other
// goroutine that reads the tag — sinkForTag and exitForTag on the same forwarder,
// and the runner's own rotation path. streamSessionTag is an atomic word precisely so
// this pair needs no lock, and this test is what says so out loud.
func TestSessionResetFollower_ConcurrentSinkAndTagRead(t *testing.T) {
	t.Parallel()

	tag := newStreamSessionTag(resetFollowSessionA)
	f := newSessionResetFollower(tag, func(string, string) error { return nil }, nil, discardLogger())

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			f.Sink(turnevent.ConversationReset{NewConversationID: resetFollowSessionB})
			f.Sink(turnevent.TextChunk{MessageID: "m", Text: "x"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = tag.ID()
		}
	}()
	wg.Wait()

	if id := tag.ID(); id != resetFollowSessionB {
		t.Errorf("tag.ID() = %q after 500 announcements, want %q", id, resetFollowSessionB)
	}
}

// TestStreamTurnDrainV2_EventsAfterAnnouncedResetStillReachTheClient is AC 2's
// end-to-end shape at the unit level, and the one test here that fails on main.
//
// It drives the REAL chain: the follower's tag feeds the real sinkForTag, the real
// drain applies the real active-session gate, and the pool re-key is stubbed by
// moving the stub active session onto the announced id — which is what
// notifyTransition's rebindConversation does in production. Before the reset the
// child's events are admitted; after it, with the pool re-keyed, they are admitted
// ONLY if the tag rotated too. A follower that re-keyed the registry without moving
// the tag leaves every one of them tagged with the retired id, the gate drops them
// all, and this test reddens on the assertion below rather than on a drop record
// nobody reads.
func TestStreamTurnDrainV2_EventsAfterAnnouncedResetStillReachTheClient(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cur := &stubCursor{}
	cur.set(testConvID)
	active := &stubActiveSession{}
	active.set(resetFollowSessionA)
	bcast := newChanBcast("conn-a")
	emitter := newInteractiveTurnEmitterV2(cur, bcast, discardLogger())

	sink := newStreamTurnSink(0, discardLogger())
	cleanup := startStreamTurnDrainV2(ctx, sink, emitter, active.get, nil, discardLogger())
	defer func() { cancel(); cleanup() }() // cancel-then-join; joining first deadlocks

	tag := newStreamSessionTag(resetFollowSessionA)
	// The stub stands in for the pool: AdoptAnnouncedID's notifyTransition rebinds the
	// owning conversation, so the gate's answer moves to the announced id. Nothing
	// else about the pool matters to this assertion.
	adopt := func(_, newID string) error { active.set(newID); return nil }
	f := newSessionResetFollower(tag, adopt, sink.sinkForTag(tag.ID), discardLogger())

	f.Sink(turnevent.ConversationReset{NewConversationID: resetFollowSessionB})
	f.Sink(turnevent.TextChunk{MessageID: "m-after", Text: "AFTER-THE-RESET"})
	f.Sink(turnevent.TurnEnd{})

	// The TurnEnd is the last fed event and it flushes at the turn boundary rather
	// than on the coalesce timer, so observing its turn_state frame proves the whole
	// post-reset run crossed the gate — the drain is serial. No sleep, no poll.
	waitPushType(t, bcast.pushed, protocol.TypeTurnState)
}

// waitPushType drains pushed envelopes until one of type want arrives, reporting the
// types actually seen at the deadline — the same diagnosis-over-a-bare-timeout shape
// waitDropKind uses, and for its reason: "wrong frames kept coming" and "no frame at
// all" are different failures and must not read alike.
func waitPushType(t *testing.T, ch <-chan protocol.Envelope, want string) {
	t.Helper()
	start := time.Now()
	deadline := time.After(dropKindWaitTimeout)
	var seen []string
	for {
		select {
		case env := <-ch:
			seen = append(seen, env.Type)
			if env.Type == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out after %v waiting for a pushed %q frame; types seen: %v",
				time.Since(start).Round(time.Millisecond), want, seen)
		}
	}
}
