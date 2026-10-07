package streamsup

import (
	"context"
	"io"
)

// BeginRotation arms the rotation gate: from this call until the next child binds
// its stdin, every WriteUserTurn refuses with ErrNoLiveChild instead of writing
// into the child the accompanying RestartFresh is about to kill. It is called
// strictly BEFORE the pool-side rotate() that re-keys the binding and fires the
// session_transition{clear}, so no turn accepted on the strength of that clear can
// reach the outgoing child (#1330, AC1).
//
// It returns the DISARM for the caller's error path — startFreshRunner runs it
// when rotate() fails, because a gate left armed by a rotation that never happened
// would refuse every turn until the next respawn, a wedge the rotation never
// earned. The disarm is LIVE ONLY IF NO LATER ARM HAS LANDED (the rotateGen
// stamp), mirroring openForDelivery's undo in cmd/pyry: the only way to obtain a
// disarm is to have placed the matching arm, and a second arm retires the first
// one's. See rotateGen for the two-frame sequence that needs it.
//
// The arm also captures the RELEASE-side threshold (#1482): the freshSeq value
// standing when it is placed, against which setStdin measures each binding child.
// That is why this method takes TWO leaf mutexes where it once took one. They are
// taken SEQUENTIALLY AND NEVER NESTED — no path in this package holds two locks at
// once, and that is what keeps the ordering safe. Should nesting ever become
// unavoidable, the order is restartMu → mu, the sequence established here; a
// mu → restartMu nesting is the inversion, and recognisable as one.
//
// The restartMu read comes FIRST, and the mu section then publishes rotating,
// rotateGen and armFreshSeq together. Arming first and stamping second would leave
// the gate observably armed beside a retired arm's threshold, which a bind landing
// in between would compare against — a disarm-by-stale-comparison bug.
//
// Residual window, deliberately left open: between the restartMu read and the mu
// acquisition a DIFFERENT frame's RestartFresh could land and a spawn take its
// snapshot, and that spawn's later bind would clear the arm being placed here.
// Closing it needs both leaf mutexes held at once, which the two-mutex charter
// forbids (see mu and restartMu). It is unreachable from a single new_session frame
// — startFreshRunner calls BeginRotation and RestartFresh in program order on one
// goroutine — and needs two concurrent frames to interleave inside a few
// instructions. The two-frame gap in its documented shape (frame 1's respawn still
// pending when frame 2 arms) IS closed: frame 2's arm reads the already-bumped
// counter, so frame 1's successor snapshot is not strictly greater.
//
// Like RestartFresh it drives only Runner-internal state — leaf mutexes, no
// channel, no call-out — so the sessions layer can call it with no Pool lock held,
// which startFreshRunner does. Safe from any goroutine.
func (r *Runner) BeginRotation() (abort func()) {
	r.restartMu.Lock()
	seq := r.freshSeq
	r.restartMu.Unlock()

	r.mu.Lock()
	r.rotating = true
	r.rotateGen++
	r.armFreshSeq = seq
	gen := r.rotateGen
	r.mu.Unlock()

	// The disarm leaves armFreshSeq alone: a threshold behind a disarmed gate is
	// inert, and the next arm overwrites it.
	return func() {
		r.mu.Lock()
		if r.rotateGen == gen {
			r.rotating = false
		}
		r.mu.Unlock()
	}
}

// BeginTeardown arms the teardown gate: from this call until the next child binds
// its stdin, every WriteUserTurn refuses with ErrNoLiveChild instead of writing into
// the child the accompanying kill is about to take down (#1513). Its three callers
// are in internal/sessions — both eviction arms of Session.runActive and
// Pool.UpdateSettings' restart branch — and each places it strictly BEFORE the
// cancel or Restart it pairs with, and before anything that publishes the teardown
// to clients, which is #1330's placement rule applied to a deliberate kill.
//
// It captures the RELEASE-side threshold in the same acquisition: the
// childGeneration value standing when the arm is placed, against which setStdin
// measures each binding child. Because setStdin bumps that counter on every bind,
// ANY successor child ends this window — including the Restart-driven and
// crash-off-the-backoff-ladder binds whose freshSeq snapshot reads EQUAL to
// armFreshSeq and which therefore leave a rotation arm standing (#1482). That
// difference is why this is a second gate rather than a third caller of
// BeginRotation: these teardowns END IN exactly the respawns a rotation must
// survive, so arming rotating here would wedge the session until some later
// RestartFresh landed.
//
// Unlike BeginRotation it takes ONE leaf mutex, not two: the threshold it needs
// lives under mu beside the flag, so there is no restartMu read to sequence and the
// residual window BeginRotation documents between its two acquisitions does not
// exist here.
//
// It returns NO disarm, deliberately. BeginRotation needs one because its gate can
// outlive a rotation that never happened, and nothing else would bring it down;
// this gate is self-clearing at the next bind of any child, so a stray arm costs at
// most the turns inside one respawn. Both callers are unconditional in any case —
// cancelSup always runs on the arms that reach it, and Restart returns nothing and
// cannot fail.
//
// Like BeginRotation it drives only Runner-internal state — one leaf mutex, no
// channel, no log call, no call-out — so the sessions layer calls it with no Pool
// lock held and it cannot delay the kill that follows it. Safe from any goroutine.
func (r *Runner) BeginTeardown() {
	r.mu.Lock()
	r.tearingDown = true
	r.armChildGen = r.childGeneration
	r.mu.Unlock()
}

// turnGate names which gate, if any, refused a turn. The zero value is "none", so a
// caller that only needs the boolean question can compare against gateOpen — which
// is what turnTarget does.
type turnGate uint8

const (
	gateOpen turnGate = iota
	gateRotation
	gateTeardown
)

// turnTargetWithGate reports the writer a turn may be written to — nil while either
// gate is armed and nil when no child is live — together with WHICH gate refused,
// under ONE r.mu acquisition. That single acquisition is the whole property: see
// mu's doc for why splitting the reads reopens #1330's race at nanosecond width.
// Widening the answer from a bool to a turnGate keeps every read inside it, so the
// charter is unchanged rather than stretched.
//
// gateRotation is tested FIRST, so a doubly-armed runner reports the rotation: it is
// the arm with the stricter release rule and the record an operator can act on.
//
// gateTeardown is reported only while a handle is BOUND. With stdin nil the method
// falls through to the nil-handle branch, and the refusal is byte-identical either
// way — nil writer, ErrNoLiveChild, zero bytes — so nothing is weakened: with no
// child bound the gate has nothing to protect. What this avoids is a log-volume
// regression, since an arm placed by an eviction that ends in no respawn stands
// until the session is next activated, and reporting it unconditionally would turn
// an evicted session's silent refusal into one record per delivery attempt for as
// long as it sits evicted. The rotation gate's own ordering is deliberately left
// byte-identical: its window always ends in a respawn, so a standing arm past one is
// the anomaly its record is argued to be loud about.
//
// The nil-handle branch returns an untyped nil rather than r.stdin, for the same
// reason Stdin() does: a typed-nil io.WriteCloser widened to io.Writer is not nil,
// and WriteTurn's no-live-child refusal keys on the interface being nil.
func (r *Runner) turnTargetWithGate() (w io.Writer, gate turnGate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rotating {
		return nil, gateRotation
	}
	if r.stdin == nil {
		return nil, gateOpen
	}
	if r.tearingDown {
		return nil, gateTeardown
	}
	return r.stdin, gateOpen
}

// turnTarget is turnTargetWithGate's boolean face, for callers that need only "may
// a turn be written?" and not which gate refused. Since #1513 that is the TEST-FACING
// face and has no production caller: WriteUserTurn, the only one there was, takes the
// fuller answer because the two gates carry different operator records. It is kept
// because the in-package assertion sites read the boolean and rewriting them all to
// compare against gateOpen would say nothing they do not already say — so a later
// reader should not go looking for the production consumer, there is none.
func (r *Runner) turnTarget() (w io.Writer, gated bool) {
	w, gate := r.turnTargetWithGate()
	return w, gate != gateOpen
}

// WriteUserTurn writes the user envelope to the live child's stdin and claims
// the turncommit gate, wrapping the reviewed WriteTurn free function (#1088). It
// is the delivery path the session pool dispatches through (send_message →
// Session.WriteUserTurn → here). A nil target yields ErrNoLiveChild without writing,
// the retryable no-live-child refusal, and there are THREE ways to get one: no live
// child, a rotation armed by BeginRotation (#1330), and a permission posture this
// child has not confirmed (#2064, whose own two states — awaiting an ack, and refused
// by claude — refuse identically and are told apart only by the record). A turncommit
// gate deny yields turncommit.ErrDropped with zero bytes written. All of it is
// WriteTurn's verbatim contract, so no new envelope construction is introduced.
//
// The rotation refusal deliberately reuses ErrNoLiveChild rather than minting a
// sentinel: that is already the retryable classification msgqueue and cmd/pyry
// agree on, and the e2e asserts as a contract check that stream WriteUserTurn
// returns only ErrNoLiveChild, turncommit.ErrDropped or nil
// (`TestRelayV2_StreamNewSessionRotatesAndRestartsFresh`). The discriminator an operator needs —
// "refused by the rotation gate" vs "no child yet" — is carried by the record
// below instead. Cost of refusing rather than blocking: the turn lands up to one
// msgqueue retry interval (1 s) later, against a give-up bound of 2 m.
//
// conversationID is accepted for interface conformance (sessions.Runner) and
// future outbound-cursor wiring (T4/T7); this slice does not yet track a cursor,
// so it is intentionally unused here — and it MUST NOT be logged: conversation ids
// are resolved daemon-side and stamped on the wire, never logged (cmd/pyry's
// stream_turn_busy.go, session_transition_v2.go), and streamsup logs none today.
// Neither may payload bytes be.
func (r *Runner) WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error {
	w, gate := r.turnTargetWithGate()
	// The POSTURE gate is read SECOND, in its own acquisition, and the order is the
	// correctness argument rather than a detail (#2064). arm() strictly precedes
	// setStdin, so a handle observed live has already had its child's gate armed;
	// reading the gate first and the handle second would let child N's release admit
	// a turn into child N+1. Two acquisitions rather than one because mu is a leaf and
	// must never be held across another object's lock — and unlike the rotation gate
	// this needs no single-acquisition treatment: turnTarget's two reads answer one
	// question about the same instant, whereas this answer for a GIVEN handle can only
	// become more permissive.
	//
	// Refusing by nilling the target reuses WriteTurn's verbatim contract — the
	// retryable ErrNoLiveChild, ZERO BYTES written, no new envelope construction —
	// which is the same classification the rotation gate above reuses and the one
	// msgqueue and cmd/pyry already agree is worth retrying.
	if w != nil && !r.postureGate.ready() {
		// Outside every acquisition, and INFO not Debug, for the rotation record's
		// reasons verbatim: a slow slog handler must not block the Run goroutine's
		// setStdin, and a Debug record anywhere in the daemon's stderr defeats #1330's
		// e2e instrument guard. The session id is the only field — never the mode
		// (#833 keeps settings values out of the daemon log) and never the request id.
		//
		// TWO RECORDS, because the two closed states need different operator actions
		// even though they refuse identically. Pending resolves itself, so its record
		// stays at Info. Refused does not: claude has ANSWERED, so nothing changes until
		// the operator changes the session's posture (which retargets this gate) or the
		// child is replaced — and repeating "not yet confirmed" at that point would
		// describe a round trip that already finished. Warn, and it names the cause; the
		// mode that was refused is deliberately still absent, and the record's existence
		// is the only bit of claude's answer that reaches the log.
		if r.postureGate.refusedByClaude() {
			r.log.Warn("streamsup: turn refused; claude refused this session's permission posture — change the posture or restart the session",
				"session", r.liveSessionID())
		} else {
			r.log.Info("streamsup: turn refused; permission posture not yet confirmed by claude",
				"session", r.liveSessionID())
		}
		w = nil
	}
	// A switch rather than two ifs: the causes are mutually exclusive over a
	// three-valued type, and stating that lets a fourth gate fail loudly at the
	// compiler rather than refuse silently with no record naming why.
	switch gate {
	case gateTeardown:
		// A deliberate teardown (#1513): an eviction, or the respawn
		// Pool.UpdateSettings drives for a setting with no in-band form. Its own
		// record rather than the rotation's, because "rotation in flight" would name a
		// cause that did not happen and send an operator looking for a new_session
		// frame there is none of. Every constraint on the rotation record below
		// applies here verbatim and for the same reasons: OUTSIDE r.mu, at Info and
		// not Debug, with the session id as the ONLY field.
		r.log.Info("streamsup: turn refused; session teardown in flight",
			"session", r.liveSessionID())
	case gateRotation:
		// Emitted OUTSIDE r.mu: a slow slog handler must never block the Run
		// goroutine's setStdin, which is the thing that ends this very window.
		//
		// INFO, NOT DEBUG, and the level is load-bearing rather than taste. #1330's
		// AC4 measures the fix against the e2e's AC-1 instrument guard, which greps
		// the daemon's WHOLE captured stderr for the literal "level=DEBUG"
		// (`TestRelayV2_StreamNewSessionRotatesAndRestartsFresh`) and which AC5 forbids
		// editing. A Debug record here fires on essentially every rotation, so it
		// would satisfy that guard from the fix's own diagnostic and turn the
		// measurement into a near-tautology — the false-green shape the ticket
		// spends a paragraph rejecting. Volume at Info is bounded and low: one
		// record per delivery attempt inside a window a respawn closes, and ~120
		// then a typed session_error in the pathological case where Run has already
		// returned — which is an anomaly worth being loud about. (Spec Open
		// question 1 left the level open on exactly this kind of evidence.)
		r.log.Info("streamsup: turn refused; new_session rotation in flight",
			"session", r.liveSessionID())
	}
	return WriteTurn(ctx, w, payload)
}

// Interrupt writes a single interrupt control_request line to the live child's
// stdin, ending the running turn (claude acks and emits a result with subtype
// error_during_execution, which the parser maps to a cancelled TurnEnd). The
// request_id is locally minted (not caller-supplied). When no child is live
// Stdin() is nil, so Interrupt returns the retryable ErrNoLiveChild without
// writing and without panicking — the safe no-op refusal.
//
// Interrupt is a concrete method on *Runner, deliberately NOT on sessions.Runner
// (#1077's placement rule: its dispatch lives in cmd/pyry, which can assert —
// unlike SetSpawnArgs (#1580), RevokeBypass (#1604) and SetPermissionMode (#2042),
// whose consumer is inside
// internal/sessions and which are ON the interface for exactly that reason):
// #1121's interrupt routing reaches it via its own narrow interface or a type
// assertion. It mirrors how
// *supervisor.Supervisor encapsulates SendEsc (#726) without that method being on
// the interface. Safe from any goroutine.
func (r *Runner) Interrupt() error {
	return WriteInterrupt(r.Stdin(), r.nextControlID())
}
