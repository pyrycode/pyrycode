package main

import (
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// The dormant half of the inbound new_session path (#2521).
//
// THE DEFECT IT CLOSES is a gap between two routes, not a wrong primitive. The
// message route already recovers a conversation whose session the pool has not
// materialised: sessionRouter.resolve re-materialises on ErrSessionNotFound
// through sessionRouter.revive, lazily, on first touch (#1487). new_session had
// no such arm, so after a daemon restart a Reset on an existing channel was
// silently inert while the identical conversation accepted a message — which is
// why "send one message first, then Reset" was the workaround the report
// described. The second, subtler half is that the pool RETAINING a session with
// no running child read the same as a conversation created but never messaged,
// because both report State().ChildPID == 0.
//
// Both arms funnel through one question — has this conversation ever run? —
// answered by Pool.EverActivated, whose doc carries why a persisted
// last_active_at against created_at is an exact reading and needs no migration.
// The never-used refusal (#2085) is preserved on both sides of the split; what
// changed is that it no longer answers for three states it was never about.

// resolveDormantSession answers a named conversation's PERSISTED binding: the
// session id it points at and the workspace it recorded, read from the
// conversation registry alone.
//
// IT DELIBERATELY DOES NOT CONSULT THE POOL, and that is what distinguishes it
// from resolveBoundSession rather than an omission. Its whole subject is the
// state where the pool's answer is a miss — after a daemon restart sessions.New
// materialises only the bootstrap, so every per-conversation session is a
// persisted entry with no *Session behind it — so a Lookup here would refuse
// exactly the rows the caller is asking about. The caller reaches this only
// after resolveBoundSession already refused, and Pool.Revive's take path returns
// the existing *Session unchanged if a racer materialised the id in between, so
// nothing is lost by not re-checking.
//
// THE CurrentSessionID == "" GUARD IS REPEATED RATHER THAN INHERITED. It is the
// #678 isolation enforcement point resolveBoundSession documents: the empty id
// resolves to the BOOTSTRAP session in Pool.Lookup, and it would reach
// Pool.Revive the same way — so an unbound conversation that got past here could
// have its frame revive and rotate the daemon's shared child. Every
// non-resolvable state answers ("", "", false) so the caller stays inert; this
// never falls through to bootstrap.
//
// The workspace comes back RAW — the unvalidated Conversation.Cwd, exactly as
// ChangeWorkspace stored it — for resolveBoundSession's stated reason: re-confining
// it belongs at the spawn site, which here is reviveBound's resolveSpawnDir.
func resolveDormantSession(convReg *conversations.Registry, convID string) (sessions.SessionID, string, bool) {
	conv, ok := convReg.Get(conversations.ConversationID(convID))
	if !ok || conv.CurrentSessionID == "" {
		return "", "", false
	}
	return sessions.SessionID(conv.CurrentSessionID), conv.Cwd, true
}

// hasEverRun asks the everRan seam whether oldID's session has ever been
// activated, tolerating the nil seam a pre-#2521 literal leaves behind.
//
// NIL ANSWERS FALSE, and the polarity is the fail-closed direction rather than an
// accident of phrasing: an unwired starter refuses every dormant reset, which is
// exactly the behaviour this package had before the seam existed. A free-standing
// method rather than a nil check at each of the two call sites, where the third
// edit would forget one — the argument reportNewSessionOutcome already records
// for itself.
func (a activeSessionStarter) hasEverRun(oldID sessions.SessionID) bool {
	if a.everRan == nil {
		return false
	}
	return a.everRan(oldID)
}

// reviveDormantBound is the after-daemon-restart arm of the inbound new_session
// path: it resolves the conversation's persisted binding, refuses one that has
// never run, and materialises the rest so the rotation can proceed (#2521).
//
// IT OWNS ITS OWN RECORDS, all three of them, which is why it returns a bare bool
// rather than an event name for the caller to log. The refusals are not
// interchangeable and the caller cannot tell them apart from a false: "this
// conversation is unknown or unbound" and "this conversation has never had a
// turn" are different facts about different rows, and the reported defect on this
// ticket was diagnosed from logs that could not separate them.
//
// THE ORDER OF THE THREE STEPS IS THE SECURITY PROPERTY, not a stylistic
// preference:
//
//  1. resolveDormant first, so an unknown or unbound conversation dies on the
//     SAME record the live path writes — the frame still cannot be used to ask
//     "does this conversation exist?", which is the non-distinction
//     resolveBoundSession's doc protects.
//  2. everRan second, ABOVE the revive. Pool.Revive registers the session and
//     persists sessions.json, so a gate below it would mutate the registry for a
//     conversation AC-3 requires to stay inert — and would let a paired client
//     drive that write by replaying a frame naming a chat that has never run.
//  3. reviveBound last, and only then. It is also the first step that touches the
//     filesystem (resolveSpawnDir's MkdirAll, then the pool's save), which keeps
//     the containment activeSessionStarter.resolveSpawnDir's own doc requires:
//     only a frame that will actually rotate pays for it.
//
// A FAILED REVIVE IS INERT, NOT AN ERROR RETURN, and that is a confidentiality
// decision. The two failures reachable there are resolveSpawnDir's confinement
// rejection, whose error names the resolved path and the $HOME boundary, and
// Pool.Revive's build or persist failure, which can name a settings path;
// handleNewSession Warn-logs whatever start returns, verbatim. Pool.Revive's own
// contract is that a phone-influenced workspace path must never reach a log, and
// sessionTranscriptDir's SECURITY paragraph closes the same channel (#833). So
// the error is dropped at this boundary and the record carries the event and the
// conversation id — already logged unbounded on every resolvable arm — and
// nothing else. The frame is re-sendable, which is the same fail-safe direction
// every other arm of this verb takes.
//
// At Warn rather than the other two arms' Debug, for resolveSpawnDir's stated
// reason: this one is about the daemon's own stored state failing its own
// validator, not about a string a client just sent.
//
// A nil seam on either of the two optional fields answers the pre-#2521 shape:
// resolveDormant nil refuses like an unknown conversation, and reviveBound nil
// cannot materialise anything — both leaving the after-restart case exactly as
// inert as it was.
func (a activeSessionStarter) reviveDormantBound(convID string) (sessions.Runner, sessions.SessionID, string, bool) {
	oldID, recordedCwd, ok := "", "", false
	if a.resolveDormant != nil {
		var id sessions.SessionID
		id, recordedCwd, ok = a.resolveDormant(convID)
		oldID = string(id)
	}
	if !ok {
		a.logger().Debug("relay: v2 new_session inert; conversation has no bound session",
			"event", "v2.new_session.no_bound_session",
			"conversation_id", convID)
		return nil, "", "", false
	}
	if !a.hasEverRun(sessions.SessionID(oldID)) {
		a.logger().Debug("relay: v2 new_session inert; dormant conversation has never run",
			"event", "v2.new_session.dormant_never_used",
			"conversation_id", convID)
		return nil, "", "", false
	}
	if a.reviveBound == nil {
		a.logger().Debug("relay: v2 new_session inert; conversation has no bound session",
			"event", "v2.new_session.no_bound_session",
			"conversation_id", convID)
		return nil, "", "", false
	}
	runner, err := a.reviveBound(convID, sessions.SessionID(oldID), recordedCwd)
	if err != nil || runner == nil {
		a.logger().Warn("relay: v2 new_session could not revive the conversation's saved session",
			"event", "v2.new_session.revive_failed",
			"conversation_id", convID)
		return nil, "", "", false
	}
	a.logger().Debug("relay: v2 new_session revived the conversation's saved session",
		"event", "v2.new_session.revived_dormant",
		"conversation_id", convID)
	return runner, sessions.SessionID(oldID), recordedCwd, true
}
