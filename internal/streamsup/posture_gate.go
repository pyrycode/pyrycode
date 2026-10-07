package streamsup

import (
	"sync"
)

// PostureGate holds a session's user turns until the child it is armed against has
// CONFIRMED the permission posture the daemon wrote to it (#2064). It is the first
// shipped control-ack reader in this package. Writers still return after their write;
// the parser now also retains an exact echoed mode as separate informational state.
//
// Its state is one string and one bool. armedID == "" is the OPEN state, so a gate
// nobody armed is open and an ungated runner behaves exactly as it did before this type
// existed. arm closes it against exactly one locally-minted request_id, release opens
// it only on an exact match, and ready reports open.
//
// The bool answers "and what happened to the request" for the one closed state that is
// TERMINAL rather than pending: refuse records that claude answered the armed id with a
// non-success subtype. It changes no decision — the gate is closed either way — it is
// what lets the turn path tell an operator "claude refused this posture" instead of
// repeating "not confirmed yet" forever. Every state change clears it, so the verdict
// never outlives the request it answers.
//
// A NAK'd gate is not a dead session: retarget points a CLOSED gate at the id of a
// later in-band posture write, so an operator's mode change is the recovery, and the
// next spawn arms a fresh id regardless. What no path does is OPEN the gate on a NAK —
// that would be fail-open, and under #2065 (every child launched in bypass) it would
// admit turns to a child whose downgrade claude has just refused.
//
// A fresh arm OVERWRITES the previous id rather than clearing a separate flag, and
// that is not a compression — it is what makes "a replacement child is never released
// by its predecessor's ack" hold with no bookkeeping. cmd/pyry builds ONE parser per
// session, not per child, so a dead child's buffered stdout can still be parsed after
// its successor has armed; the mismatched id refuses it.
//
// WHAT IT PROVES, AND WHAT IT DOES NOT. A released gate means "claude acked the
// request this daemon minted". It does NOT mean "claude enforces that posture" — an
// echoed posture is the child's claim about itself, the scope boundary
// interactive_stream_inband_bypass_revoke_test.go already draws for the ack it reads.
// A child that lies about its posture defeats this gate, and is also a child that
// could simply ignore the mode; the gate buys protection against BENIGN failure — an
// unsupported mode, a dropped line, a child slow to come up — not against a hostile
// one. Read that limit before treating a released gate as an enforcement guarantee.
//
// Every method is NIL-RECEIVER-SAFE, following sessionModelHold.ModelList's
// precedent, so neither the runner's turn path nor the parser's ack path needs a nil
// branch. The mutex is a LEAF: it is never held across a log call, a write, or
// another lock, and no path in this package holds it together with Runner.mu or
// Runner.restartMu.
type PostureGate struct {
	mu sync.Mutex
	// armedID is the request_id whose success ack opens this gate, or "" when the
	// gate is open. Never logged and never serialised — it is a correlation token,
	// not a capability: the daemon hands it to the child in the request line itself,
	// so there is nothing for unpredictability to buy and nextControlID's short
	// counter keeps the emitted line under PIPE_BUF.
	armedID string
	// refused records that claude answered armedID with a NON-SUCCESS subtype: a
	// definitive negative rather than a round trip still in flight. Read only to choose
	// which record the turn path writes, never to decide whether a turn passes — a
	// refused gate and a pending one both refuse.
	refused bool
}

// arm closes the gate against requestID, retiring whatever id stood before it and
// clearing any verdict that id had drawn. Called once per spawn — every spawn, since
// arm("") is how a spawn that writes nothing publishes the OPEN state instead of
// inheriting its predecessor's id — strictly BEFORE setStdin publishes that child's
// stdin handle. See spawnAndWait for why that ordering is the whole of "no user turn
// reaches the child before the write".
func (g *PostureGate) arm(requestID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.armedID = requestID
	g.refused = false
	g.mu.Unlock()
}

// release opens the gate if requestID is the one it is armed against, and does
// nothing otherwise. Called from the parser goroutine for every SUCCESS
// control_response; an undecodable line and a mismatched id never reach it or never
// match, and a NAK goes to refuse instead.
//
// There is deliberately NO requestID != "" guard. nextControlID formats an
// already-incremented uint64, so an armed gate's id is never empty and an ack
// carrying no request_id cannot match one; against an already-open gate the
// comparison succeeds and the assignment is a no-op. A guard here would defend a
// state that cannot exist. (refuse DOES need that guard, and the asymmetry is real —
// see there.)
func (g *PostureGate) release(requestID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.armedID == requestID {
		g.armedID = ""
		g.refused = false
	}
	g.mu.Unlock()
}

// refuse records that claude answered requestID with a non-success subtype. The gate
// STAYS CLOSED: a NAK is claude declining the posture, and opening on it would be
// fail-open — fatally so under #2065, where the child launches in bypass and a refused
// downgrade must not admit turns.
//
// Unlike release this DOES need the armedID != "" guard, and the asymmetry is the
// point rather than an inconsistency. release's no-op against an open gate is
// harmless; refuse's would not be — an open gate's id is "", so a NAK carrying no
// request_id would otherwise mark a gate that nothing is waiting on as refused and
// make the turn path report a refusal that never happened.
//
// What it does NOT do is end the session. The verdict is cleared by the next arm (the
// next spawn) and by retarget (an operator's in-band posture change), which is the
// recovery path a definitive answer has to leave open.
func (g *PostureGate) refuse(requestID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.armedID != "" && g.armedID == requestID {
		g.refused = true
	}
	g.mu.Unlock()
}

// retarget points a CLOSED gate at requestID, the id of a posture write made after the
// spawn's — SetPermissionMode's, once its line is on the wire. It is the recovery path
// for a spawn write claude NAK'd: without it the only opener is a spawn, so a NAK'd
// child stays healthy and refuses every turn until the daemon restarts.
//
// It never closes an OPEN gate, and that half is as load-bearing as the other. This
// ticket gates SPAWNS, and closing the gate on an in-band posture change would make
// every update start a fresh refusal window on a session that was working: every
// user turn until the ack lands, including a later /effort setting turn. Model
// changes use set_model and deliberately never call retarget; they are not posture
// confirmations and must not become turn-admission gates.
//
// CORRECTED 2026-09-03: this paragraph used to place those sends "immediately after the
// posture write in the same call", which inverts deliverSettingsInBand — it sends
// set_model and /effort first and calls SetPermissionMode last, so a gate closed there
// cannot reach the sends of the call that closed it. The carve-out is unchanged; only
// the mechanism it cited was wrong, and a reader who checks a false mechanism is a
// reader who deletes a carve-out that errs toward staying open.
//
// Retargeting a merely PENDING gate (a spawn ack still in flight) is intended, not
// collateral: the later write supersedes the earlier one, so the posture actually in
// force is the one whose ack should open the gate.
func (g *PostureGate) retarget(requestID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.armedID != "" {
		g.armedID = requestID
		g.refused = false
	}
	g.mu.Unlock()
}

// ready reports whether turns may flow — that is, whether no unconfirmed posture is
// outstanding. Safe from any goroutine, and cheap enough to sit on the per-turn path.
func (g *PostureGate) ready() bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.armedID == ""
}

// refusedByClaude reports whether the outstanding request was ANSWERED with a
// non-success subtype, as opposed to still being in flight. It discriminates the two
// closed states for the turn path's record and for nothing else — both refuse a turn,
// identically and with the same error.
//
// It is read in its own acquisition, after ready, and that is sufficient rather than
// sloppy: the pair is not a decision. ready alone decides the refusal; this decides
// only which of two records is written, so a state change landing between the two reads
// costs at most one turn's record naming the pending state after the answer arrived.
func (g *PostureGate) refusedByClaude() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.refused
}
