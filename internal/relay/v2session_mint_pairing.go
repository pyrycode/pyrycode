package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// This file holds the inbound pairing-MINT interception (#2127): the handler
// behind dispatchAppFrame's TypeMintPairing case, the label gate it owns, and the
// two wire codes it mints. It serves the contract #2126 published and answers the
// two places docs/protocol-mobile.md said nothing answered the frame.
//
// IT MINTS NOTHING ITSELF, and that is the file's whole shape. Minting needs
// crypto/rand, internal/pair, internal/keys, internal/identity and internal/audit,
// none of which this package imports; the PairingMinter seam carries the work to
// cmd/pyry, which holds the daemon's relay URL, server id and static key already.
// This handler decodes, gates the one remote-authored string, and turns an outcome
// into a frame — the courier posture handleRequestHistory keeps toward the log.
//
// WHERE IT RUNS. On the conn's appFrameWorker, off the Run goroutine, reached from
// appFrameWorker's appFrameMintPairing arm — the fourth frame type to need that
// route after the two attachment legs and the history request, and the FIRST that
// WRITES host state: the mint takes the devices.json flock(2), reads the registry,
// appends a record and rewrites the file. Cross-process blocking I/O on the
// goroutine that owns the send CipherState is exactly the stall #965 removed.
//
// ONE EMISSION ROUTE, the history request's rather than the retrieval leg's. The
// answer is a single envelope rather than a stream, so there is no Push leg: the
// reply and every reject alike go through forwardToRun, where Run seals them under
// s.send. Emitting either from this goroutine would be a concurrent Encrypt on the
// single-owner send CipherState — a nonce reuse.
//
// NO BYTE BUDGETING, which is where it diverges from handleRequestHistory. The
// reply is one fixed-shape payload of roughly 400 bytes against the 65519-byte
// application-envelope cap, and the only contributor to its size that is not
// fixed-width is the daemon's OWN configured relay URL — operator-authored, and
// the URL this daemon dials. An absurd one yields the transport's own
// message.too_long rather than anything this file could improve on.
//
// SERIALISATION IS THE CONCURRENCY BOUND, and here it is load-bearing rather than
// incidental. The worker is strictly FIFO and there is one per conn, so a conn has
// at most one mint in flight; that is what bounds the achievable mint rate to one
// per client round trip and is the reason no rate limiter is built for a verb that
// creates credentials (docs/protocol-mobile.md § Security model threat 7's
// deferred posture, weighed in this ticket's spec rather than inherited in
// silence).

// mintLabelIsDisplaySafe reports whether a client-supplied device_name may be
// stored, logged and rendered.
//
// IT IS THE ONE GATE THIS FILE OWNS, and it exists because this ticket changes who
// can author a device label. protocol.MintPairingPayload bounds the value's LENGTH
// at decode and says so is not a safety property; its own block names two hazards
// the bound does nothing about and states the value is "LOGGABLE ONLY AFTER SHAPE
// VALIDATION, which no code in this package performs". This is that validation, at
// the trust boundary, before the label can reach devices.json, a line-oriented
// daemon log, or a `pyry pair list` column on an operator's terminal.
//
// ONE PREDICATE COVERS BOTH HAZARDS. A refused rune is a C0 control (which
// includes LF and CR, the log-injection shape § Attachments forbids for an
// unchecked filename, and ESC, which every ANSI escape run begins with), DEL, or a
// C1 control (U+0080–U+009F, which some terminals still act on and which a
// C0-only reading would miss).
//
// UTF-8 VALIDITY IS NOT CHECKED, and that is a fact about the decoder rather than
// an omission: encoding/json replaces every invalid byte and every unpaired
// surrogate in its input with U+FFFD, so a decoded Go string is valid UTF-8 by
// construction. A check here could not be made to fail, and an assertion no test
// can redden is worse than none.
//
// THE EMPTY STRING PASSES. Absent and empty are the same case — "the client named
// no device" — which the seam answers with the device-<hash8> fallback `pyry pair`
// generates. Refusing it would break the frame's only optional field.
//
// IT DOES NOT LIVE IN THE SHARED MINT STEP. `pyry pair --name` is operator-authored
// input reaching the same record through the same function, and this ticket must
// not change what that verb accepts.
func mintLabelIsDisplaySafe(name string) bool {
	for _, r := range name {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return false
		}
	}
	return true
}

// Static reply messages, one per wire code this path can answer with. All three
// are fixed constants and NONE is derived from an error value, a device name or
// any part of the encoded pairing: the errors behind pairing.unavailable format
// the absolute devices.json path, and the label behind protocol.malformed is the
// remote-authored string the gate above just refused.
const (
	msgMintInvalidRequest = "mint request rejected"
	msgMintNotPermitted   = "this device may not mint a pairing"
	msgMintUnavailable    = "the pairing could not be minted"
)

// The three answers, spelled once, reusing attachmentReject's shape so a code
// cannot be paired with the wrong retryable flag — the field a client actually
// branches on. Values rather than a map so each is addressable by name from the
// tests that pin the two codes this ticket minted.
//
// TWO ARE PERMANENT FOR THE REQUEST AS SENT and one is not. A malformed frame and
// an unprivileged device are both unchanged by retrying: the first needs a
// different frame, and the second needs `pyry pair --allow-remote-permissions` at
// a shell on the host. Every cause behind pairing.unavailable — a busy lock, a
// registry read or write failure, a refusing CSPRNG — can clear on its own.
var (
	rejectMintInvalidRequest = attachmentReject{protocol.CodeProtocolMalformed, msgMintInvalidRequest, false}
	rejectMintNotPermitted   = attachmentReject{protocol.CodePairingNotPermitted, msgMintNotPermitted, false}
	rejectMintUnavailable    = attachmentReject{protocol.CodePairingUnavailable, msgMintUnavailable, true}
)

// handleMintPairing answers one inbound mint_pairing with a freshly minted pairing
// for another device or with one coded reject. Intercepted in dispatchAppFrame
// before dispatch.Route, exactly like handleRequestHistory, and routed to this
// conn's appFrameWorker rather than handled inline on Run (see the file header).
//
// It takes the plaintext rather than the already-probed Envelope because the
// worker receives bytes: the frame is decoded here for the second and last time.
//
// ORDER IS THE DESIGN, and the ordering — not merely the presence of the checks —
// is what carries the security property:
//
//  1. A nil PairingMint makes the frame INERT — but still CONSUMED, so it no
//     longer draws dispatch.Route's unknown-type reply. Mirrors the nil
//     HistoryPage / AttachmentResolve guards and buys the same property: an
//     unwired daemon performs zero parsing of remote-authored bytes.
//  2. A PAYLOAD DECODE FAILURE IS REJECTED, NOT TOLERATED — the answer
//     protocol.MintPairingPayload's own block assigns here. It covers the
//     over-length device_name that type's UnmarshalJSON refuses, so the published
//     128-byte bound is enforced without this file restating it. NOTHING about the
//     failure is echoed or logged: encoding/json quotes offending input into its
//     error string and those bytes are remote-authored.
//  3. THE LABEL GATE FIRES BEFORE THE NAME CAN BE STORED, LOGGED OR RENDERED. See
//     mintLabelIsDisplaySafe. It is the precondition the seam's doc block states,
//     and this handler is the caller that discharges it.
//  4. The seam decides authorization and performs the mint. THIS HANDLER APPLIES
//     NO PRIVILEGE CHECK OF ITS OWN and must not grow one: the refusal has to be
//     audited through internal/audit, which lives in cmd/pyry, and a second
//     arbiter here would be a second thing to keep in agreement with it.
//
// THE REQUESTER IS s.device AND NOTHING ELSE. protocol.MintPairingPayload has no
// field naming a device, and s.device is the record handleNoiseInit bound after
// the presented token validated. A conn with no authenticated device passes nil,
// which devices.Device.MayAnswerRemotePermission denies — nil-receiver-safe
// precisely so the fail-closed reading needs no branch here.
//
// SECURITY — the never-log rule, which differs per value rather than applying
// uniformly. THE MINTED PAIRING IS NEVER LOGGED ON ANY ARM and reaches exactly one
// place: the pairing field of the success reply, sealed and unicast to the conn
// that asked. THE REQUESTED LABEL IS NEVER LOGGED HERE EITHER — the gate above is
// what makes it safe to log at all, and the one record that carries it is the
// seam implementation's, which is also the only scope that knows what the device
// was finally named. THE SUCCESS PATH LOGS NOTHING FROM THIS FILE: AC-4 asks for
// one event per successful mint, that event records a HOST STATE CHANGE rather
// than a delivery, and it belongs where the mint happened.
func (m *V2SessionManager) handleMintPairing(ctx context.Context, s *V2Session, plaintext []byte) {
	if m.cfg.PairingMint == nil {
		// Step 1.
		m.cfg.Logger.Debug("relay: v2 mint_pairing inert; no pairing minter wired",
			"event", "v2.pairing.mint.inert",
			"conn_id", s.connID)
		return
	}

	var env protocol.Envelope
	if err := json.Unmarshal(plaintext, &env); err != nil {
		// Unreachable: dispatchAppFrame decoded these same bytes to match the
		// type before enqueuing them. Reply nothing — there is no envelope id
		// left to correlate a reply to. NEVER echo err.
		m.cfg.Logger.Warn("relay: v2 mint_pairing envelope did not decode",
			"event", "v2.pairing.mint.envelope_err",
			"conn_id", s.connID)
		return
	}

	var req protocol.MintPairingPayload
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		// Step 2. NEVER echo err or any payload byte: encoding/json quotes the
		// offending input, and the over-length arm's own error names a byte count
		// taken from a remote claim.
		m.rejectMintPairing(ctx, s, env.ID, rejectMintInvalidRequest, "payload did not decode")
		return
	}

	if !mintLabelIsDisplaySafe(req.DeviceName) {
		// Step 3. The refused label is NOT logged — refusing it is precisely the
		// statement that it is not safe to put in a line-oriented record.
		m.rejectMintPairing(ctx, s, env.ID, rejectMintInvalidRequest, "device name is not a safe display string")
		return
	}

	// Step 4. The label has passed the gate; the seam owns everything else.
	res := m.cfg.PairingMint.MintPairing(s.device, req.DeviceName)
	switch res.Outcome {
	case PairingMintOK:
		m.emitPairingMinted(ctx, s, env.ID, res.Pairing)
	case PairingMintUnauthorized:
		m.rejectMintPairing(ctx, s, env.ID, rejectMintNotPermitted, "device is not privileged to mint")
	default:
		// PairingMintFailed and the zero value alike. The enum's zero is not OK
		// (see PairingMintOutcome), so an implementation that forgot to set an
		// outcome refuses rather than emitting an empty credential as a success.
		m.rejectMintPairing(ctx, s, env.ID, rejectMintUnavailable, "the host could not complete the mint")
	}
}

// emitPairingMinted seals nothing and sends nothing itself: it marshals the reply
// and hands it to Run, which owns the send CipherState.
//
// pairing is A PLAINTEXT BEARER CREDENTIAL. It is written into exactly one field
// and read back nowhere; no log record on this path — not the drop record below,
// not the marshal-failure records — carries it, a length derived from it, or any
// substring of it.
func (m *V2SessionManager) emitPairingMinted(ctx context.Context, s *V2Session, inReplyTo uint64, pairing string) {
	payload, err := json.Marshal(protocol.PairingMintedPayload{Pairing: pairing})
	if err != nil {
		// A closed struct of one string; marshal cannot fail in practice. NEVER
		// echo err — it would quote the credential.
		m.cfg.Logger.Warn("relay: v2 pairing_minted payload marshal failed",
			"event", "v2.pairing.mint.payload_marshal",
			"conn_id", s.connID)
		return
	}
	frame, err := m.mintPairingFrame(s, inReplyTo, protocol.TypePairingMinted, payload)
	if err != nil {
		return
	}
	if !m.forwardToRun(ctx, s, protocol.RoutingEnvelope{ConnID: s.connID, Frame: frame}) {
		// The record of the MINT is the seam implementation's and has already been
		// written; this one records only that the reply did not leave. The device
		// record it names stands, holds a token nobody received, and expires at
		// devices.RedemptionWindow — which is why writing it before the reply is
		// safe, and why the reverse ordering (emit, then persist) is ADR 021's
		// rejected one.
		m.cfg.Logger.Debug("relay: v2 pairing_minted dropped; session tearing down",
			"event", "v2.pairing.mint.reply_dropped",
			"conn_id", s.connID)
	}
}

// mintPairingFrame builds the outbound envelope and marshals it, shared by the
// reply and the reject so the correlation, the non-load-bearing envelope id and
// the UTC stamp are decided once for both answers.
//
// Its own helper rather than a call into historyFrame, matching the package's
// stated posture that each reply-owing handler owns its frame and error helpers —
// and it must be its own anyway, since that one's log events are named for the
// history path and would misfile every failure here.
func (m *V2SessionManager) mintPairingFrame(s *V2Session, inReplyTo uint64, typ string, payload json.RawMessage) ([]byte, error) {
	reply := protocol.Envelope{
		ID:        1, // non-load-bearing; the phone correlates on InReplyTo.
		Type:      typ,
		TS:        time.Now().UTC(),
		Payload:   payload,
		InReplyTo: &inReplyTo,
	}
	frame, err := json.Marshal(reply)
	if err != nil {
		// payload is already valid JSON and the rest is a closed struct; marshal
		// cannot fail in practice. NEVER echo err — on the success path it would
		// quote the credential.
		m.cfg.Logger.Warn("relay: v2 mint reply envelope marshal failed",
			"event", "v2.pairing.mint.envelope_marshal",
			"conn_id", s.connID,
			"reply_type", typ)
		return nil, err
	}
	return frame, nil
}

// rejectMintPairing records one refusal and answers it, so the log line and the
// wire frame cannot drift apart into two edits. reason is a DAEMON-AUTHORED
// constant chosen by the call site — it is what keeps the two arms sharing
// protocol.malformed diagnosable to an operator while the wire answer stays
// exactly as coarse as it must be.
//
// THE DEVICE NAME IS ABSENT FROM THIS SIGNATURE ON PURPOSE. There is no reject arm
// on which it may be logged — the gate's arm refuses it precisely because it is
// not safe to log, and the decode arm never had a decoded value at all — so it is
// not a parameter a future edit could pass "just for this one case".
func (m *V2SessionManager) rejectMintPairing(ctx context.Context, s *V2Session, inReplyTo uint64, rej attachmentReject, reason string) {
	m.cfg.Logger.Warn("relay: v2 mint_pairing refused",
		"event", "v2.pairing.mint.refused",
		"conn_id", s.connID,
		"code", rej.code,
		"reason", reason)
	m.mintPairingReplyError(ctx, s, inReplyTo, rej)
}

// mintPairingReplyError answers one refusal with a single TypeError envelope
// correlated to inReplyTo. rej supplies a STATIC message and the retryability
// docs/protocol-mobile.md § Error codes publishes for that code; no value derived
// from an error, a device name, a host path or the encoded pairing ever reaches
// this path.
func (m *V2SessionManager) mintPairingReplyError(ctx context.Context, s *V2Session, inReplyTo uint64, rej attachmentReject) {
	payload, err := json.Marshal(protocol.ErrorPayload{
		Code:      rej.code,
		Message:   rej.message,
		Retryable: rej.retryable,
	})
	if err != nil {
		// A closed struct of strings + bool; marshal cannot fail in practice.
		m.cfg.Logger.Warn("relay: v2 mint error reply marshal failed",
			"event", "v2.pairing.mint.err_marshal",
			"conn_id", s.connID,
			"code", rej.code)
		return
	}
	frame, err := m.mintPairingFrame(s, inReplyTo, protocol.TypeError, payload)
	if err != nil {
		return
	}
	if !m.forwardToRun(ctx, s, protocol.RoutingEnvelope{ConnID: s.connID, Frame: frame}) {
		m.cfg.Logger.Debug("relay: v2 mint reject dropped; session tearing down",
			"event", "v2.pairing.mint.reply_dropped",
			"conn_id", s.connID,
			"code", rej.code)
	}
}
