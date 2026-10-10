package relay

import (
	"context"
)

// maxRetainedClientNameBytes, maxRetainedClientVersionBytes and
// maxRetainedClientFeaturesBytes bound what one conn may park on its V2Session
// from its own hello. An over-bound value
// is retained as "" — dropped, never truncated, so no value is invented that the
// client did not send.
//
// This is a RESOURCE bound, not a display policy, and the distinction is why it
// coexists with internal/sessions' much tighter admitClient rather than
// duplicating it. Unlike MintPairingPayload.DeviceName, which UnmarshalJSON
// refuses over protocol.MaxDeviceNameBytes, HelloClientPayload bounds none of
// these fields at decode; the only ceiling is the ~64KB application-envelope cap.
// Without this, one authenticated conn could park ~64KB per string for the session's
// lifetime AND have it copied into every ActiveConn snapshot — which the
// structured fan-out takes several times per turn, for every open conn. The
// values are deliberately loose: they are picked to make that amplification
// bounded, not to decide what a prompt may say.
const (
	maxRetainedClientNameBytes     = 256
	maxRetainedClientVersionBytes  = 64
	maxRetainedClientFeaturesBytes = 1024
)

// retainedClientField returns v when it is within bound, "" otherwise. Length is
// the only property it judges; see maxRetainedClientNameBytes.
func retainedClientField(v string, maxBytes int) string {
	if len(v) > maxBytes {
		return ""
	}
	return v
}

// ActiveConn is one open v2 session in the capability-aware enumeration: its
// routing conn-id, the negotiated interactive-capability decision recorded at
// handshake, and what the client reported about itself there. It holds no
// *V2Session, CipherState, key, or credential material, so the snapshot is safe
// to hand to a consumer goroutine. The downstream structured-stream fan-out selects
// interactive vs non-interactive conns on the Interactive flag.
//
// DeviceName, ClientVersion and ClientFeatures are REMOTE-AUTHORED, UNVALIDATED strings
// (#2148) — the only fields here that are not daemon-authored routing or decision
// data, which is why they carry an obligation the routing fields do not. A consumer
// MUST NOT log them, interpolate them into an error message, or render them
// without applying its own gate; internal/sessions' admitClient is the gate the
// one consumer that renders them uses. A consumer MUST ALSO NOT format this
// struct wholesale — "%+v", slog.Any — which would emit them into the daemon log
// by accident. Each is "" when absent or over its retention bound: 256 bytes
// for name, 64 for version and 1024 for features, inclusive. They are never
// persisted to the device registry as a group; features are memory-only.
//
// DeviceTokenHash is the TokenHash of the device the handshake AUTHENTICATED
// (s.device), which is what "this conn belongs to that device" must be decided on
// — never DeviceName, which any phone may set to another device's name (#2564). It
// is daemon-authored but credential-derived, so it too MUST NOT be logged.
type ActiveConn struct {
	ConnID          string
	Interactive     bool
	Thread          bool
	DeviceName      string
	ClientVersion   string
	ClientFeatures  string
	DeviceTokenHash string
}

// ActiveConns returns a snapshot of every session currently in V2StateOpen —
// the authenticated, token-validated sessions to which Push may deliver — each
// paired with its negotiated interactive flag. The result is an unordered set
// (Go's randomized map-iteration order); a caller that needs a stable order
// must sort it.
//
// Safe to call from any goroutine other than the dispatch goroutine: the
// request is funneled onto Run via m.snapshot so m.sessions is never read
// concurrently with an in-flight handshake transition, dispatchAppFrame
// reply, re-key swap, or closeWith teardown. It is the enumeration half of
// the server-initiated fan-out primitive — a consumer calls this, then Push on
// each conn-id, fanning interactive events only to conns with Interactive set.
// It is the capability-aware v2 analog of v1's dispatch.Dispatcher.ActiveConns().
//
// Sessions still handshaking (V2StateAwaitingInit) or handshake-complete-but-
// token-unvalidated (V2StateHandshakeComplete) are excluded — the same
// V2StateOpen security gate forwardEnvelope enforces, so the negotiated flag of an
// un-authenticated peer is never observable. A torn-down session (deleted from
// the map by closeWith) cannot appear.
//
// Returns nil on caller ctx cancellation, or when Run has already exited
// (Frames closed, no receiver on m.snapshot) and the caller's ctx then fires
// — both equivalent to "no open sessions" for the broadcast consumer, which
// fans out to nobody this round and re-enumerates on the next turn. nil and an
// empty non-nil slice are interchangeable (both len 0); a snapshot has no
// failure the caller can act on, so no error is returned.
func (m *V2SessionManager) ActiveConns(ctx context.Context) []ActiveConn {
	req := snapshotReq{reply: make(chan []ActiveConn, 1)}
	select {
	case m.snapshot <- req:
	case <-ctx.Done():
		return nil
	}
	select {
	case conns := <-req.reply:
		return conns
	case <-ctx.Done():
		return nil
	}
}

// handleActiveConns runs on Run's dispatch goroutine — the only site that
// reads m.sessions for the snapshot, serialised by Run's select against every
// map write (lazy-create in handleFrame, delete in closeWith, state
// transitions in the handshake handlers). No read can observe a half-updated
// map, a torn s.state, or a torn s.interactive (set before V2StateOpen on the
// same goroutine).
//
// The returned slice is freshly allocated and owned by the caller, and holds no
// *V2Session and no key or credential material. It is NOT uniformly daemon-authored
// routing data, though: alongside the conn-id and the negotiated interactive
// bool it carries DeviceName, ClientVersion and ClientFeatures, authored by the
// client and not display-validated in this package. See ActiveConn's doc for
// the obligation that places on a consumer — none of these fields may be logged and that the struct must never be formatted wholesale.
//
// Order is Go's randomized map-iteration order — an unordered set by design:
// the AC requires no ordering and the broadcast consumer fans out
// order-independently, so no O(n log n) sort is paid on the single dispatch
// goroutine.
func (m *V2SessionManager) handleActiveConns() []ActiveConn {
	out := make([]ActiveConn, 0, len(m.sessions))
	for connID, s := range m.sessions {
		if s.state == V2StateOpen {
			ac := ActiveConn{
				ConnID:         connID,
				Interactive:    s.interactive,
				Thread:         s.thread,
				DeviceName:     s.clientName,
				ClientVersion:  s.clientVersion,
				ClientFeatures: s.clientFeatures,
			}
			// s.device is bound before V2StateOpen on the accept path; the guard
			// only keeps a hand-built session from panicking the snapshot.
			if s.device != nil {
				ac.DeviceTokenHash = s.device.TokenHash
			}
			out = append(out, ac)
		}
	}
	return out
}
