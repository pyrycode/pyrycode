package main

import (
	"context"
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
	next := &collectSink{}
	f := newSessionResetFollower(tag, adopt.fn, next.fn, discardLogger())

	f.Sink(turnevent.ConversationReset{NewConversationID: resetFollowSessionA})

	if got := adopt.recorded(); len(got) != 0 {
		t.Errorf("pool re-key calls = %v, want none — an equal id must not reach the pool", got)
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
// ErrSessionNotFound is what the pool answers when the rotation watcher observed the
// same rotation first and already re-keyed — which is every reset until #2137 retires
// the watcher. The conversation is then bound to the announced id, so the tag must
// follow: a follower that rotated only on the pool's SUCCESS would leave every later
// event tagged with the retired id, the drain's active-session gate would drop all of
// them, and the conversation would go dark until the daemon restarted.
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
func TestSessionResetFollower_PoolAnswerDecidesTheTag(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		err     error
		wantTag string
		why     string
	}{
		{
			name: "watcher won the race", err: sessions.ErrSessionNotFound,
			wantTag: resetFollowSessionB,
			why:     "the pool is already on the announced id, and a tag left behind blackholes this conversation",
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
			f := newSessionResetFollower(tag, adopt.fn, sink.sinkForTag(tag.ID), discardLogger())

			f.Sink(turnevent.ConversationReset{NewConversationID: resetFollowSessionB})
			if got := tag.ID(); got != tc.wantTag {
				t.Errorf("tag.ID() = %q after a %v from the pool, want %q — %s", got, tc.err, tc.wantTag, tc.why)
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
	f := newSessionResetFollower(tag, nil, next.fn, discardLogger())
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

	nilNext := newSessionResetFollower(newStreamSessionTag(resetFollowSessionA), nil, nil, discardLogger())
	for _, ev := range evs {
		nilNext.Sink(ev) // must not panic
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
