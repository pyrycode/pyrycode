package main

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/audit"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/pair"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// classPairingMint is this verb's audit class, the field modal_class carries.
// Sits beside classTrust and classQuestion for the same reason those do: an audit
// record has to say which decision it is about, and audit.Entry's vocabulary is
// modal-shaped, so each family names itself.
const classPairingMint = "pairing_mint"

// wireMintLockWait bounds how long a wire mint blocks waiting for the devices
// lock.
//
// NOT pairLockWait, and the split is the same one redemptionLockWait draws. That
// bound is devices.DefaultLockWait (5s) because an operator-invoked one-shot is
// not a request path; this one IS a request path, with a client waiting on a
// reply and — because the handler runs on the conn's app-frame worker — that
// conn's later frames queued behind it. 250ms is generous against peers whose
// regions are all sub-millisecond (redemptionLockWait's stated promise, which
// mintDevice keeps by doing its key-free work outside the region), and a refusal
// is answered with a retryable code rather than a longer wait.
const wireMintLockWait = 250 * time.Millisecond

// pairingMinterV2 is the cmd/pyry implementation of relay.PairingMinter: the
// per-device authorization gate for an inbound mint_pairing, the audit record of
// every decision it makes, and the encode of what it minted.
//
// IT IS THE ONLY SCOPE THAT HOLDS ALL FOUR HALVES. The gate needs internal/audit,
// the mint needs mintDevice and the devices lock, the encode needs internal/pair,
// and the payload needs the daemon's own server id, relay URL and static public
// key. internal/relay imports none of those and must not learn to; the seam is
// what keeps that true, and this type is what stands behind it.
//
// EVERY FIELD IS DAEMON-AUTHORED and fixed for the process's life. Nothing a
// client sends is input to what is minted — the request names a label and nothing
// else — which is the property protocol.MintPairingPayload's block states and this
// struct's shape makes structural: there is no field here a frame could reach.
type pairingMinterV2 struct {
	devicesPath string
	relayURL    string
	serverID    identity.ServerID

	// staticPub is the binary's X25519 static PUBLIC key, base64-encoded exactly
	// as `pyry pair` prints it. The PRIVATE half is never held here — this type
	// has no field that could carry one, which is the same structural guarantee
	// audit.Entry makes about plain tokens.
	staticPub string

	logger *slog.Logger
}

// newPairingMinterV2 builds the minter over the values startRelayV2 already holds.
//
// pub is the static keypair's public half by value (keys.StaticKey.PublicKey's
// [32]byte accessor), encoded once here rather than per mint: it does not change
// while the daemon runs, and encoding it at construction keeps the request path
// free of key handling entirely.
//
// relayURL is THE DAEMON'S resolved relay URL — the one startRelay dials, which
// resolveRelayURL produced with PYRY_RELAY_URL consulted. It can differ from what
// resolveRelay hands the CLI, which consults no environment variable, and when
// they disagree this is the correct one: it names the relay the new device
// actually has to reach.
func newPairingMinterV2(devicesPath, relayURL string, serverID identity.ServerID, pub [32]byte, logger *slog.Logger) *pairingMinterV2 {
	return &pairingMinterV2{
		devicesPath: devicesPath,
		relayURL:    relayURL,
		serverID:    serverID,
		staticPub:   base64.StdEncoding.EncodeToString(pub[:]),
		logger:      logger,
	}
}

// MintPairing mints a pairing for another device on behalf of the requesting one,
// or refuses. It runs on the asking conn's app-frame worker.
//
// ORDER IS THE DESIGN, and it mirrors questionResolverV2.admit:
//
//  1. THE PRIVILEGE GATE FIRST, so nothing is created before it. A nil device (no
//     authenticated device on the connection), an unauthenticated one and one
//     whose opt-in bit is unset all deny — MayAnswerRemotePermission is
//     nil-receiver-safe, so the fail-closed reading needs no branch. The denial is
//     audited denied_unauthorized with the (possibly empty) non-secret identity,
//     and NO CSPRNG DRAW, NO LOCK AND NO REGISTRY READ has happened.
//  2. THE MINT, with allowRemotePermissions a LITERAL false. A minted device is
//     always unprivileged: protocol.MintPairingPayload has no field for the flag
//     by declaration, and this line is the second half of that guarantee. A stolen
//     privileged pairing can mint devices that watch and send, and can never mint
//     one that approves.
//  3. THE ENCODE, over four daemon-authored values, and one record of the mint.
//
// STEP 1 IS RE-ASKED INSIDE THE LOCK, which is the one thing here that is not a
// straight transcription of the question resolver's shape. v2 revocation is
// connection-scoped: handleNoiseInit reloads devices.json before each handshake's
// Validate, so a revoked device's LIVE session survives until it reconnects, and
// the *devices.Device this method is handed is the record as of connect time. On
// every other gated verb that staleness costs one more answered modal; on this one
// it would let a device the operator has just revoked mint a FRESH credential,
// defeating the only remedy there is for a stolen pairing. mintRequest.grantorHash
// closes it against the same snapshot the append mutates, and an errGrantorRevoked
// is the privilege refusal — audited, permanent — rather than a host failure.
//
// SECURITY: the returned string is a plaintext bearer credential. It is written
// into exactly one field of the result and read back nowhere here; no log record
// on any arm carries it, a length derived from it, or any substring of it. On both
// refusing outcomes the field is left empty, so a caller that ignored the outcome
// would emit nothing rather than a stale credential.
func (m *pairingMinterV2) MintPairing(requester *devices.Device, deviceName string) relay.PairingMintResult {
	if !requester.MayAnswerRemotePermission() {
		m.auditMint(requester, audit.OutcomeDeniedUnauthorized)
		return relay.PairingMintResult{Outcome: relay.PairingMintUnauthorized}
	}

	minted, err := mintDevice(mintRequest{
		devicesPath: m.devicesPath,
		lockWait:    wireMintLockWait,
		deviceName:  deviceName,
		// Never a variable, never a field, never anything a frame could reach.
		allowRemotePermissions: false,
		grantorHash:            requester.TokenHash,
	})
	switch {
	case errors.Is(err, errGrantorRevoked):
		m.auditMint(requester, audit.OutcomeDeniedUnauthorized)
		return relay.PairingMintResult{Outcome: relay.PairingMintUnauthorized}
	case err != nil:
		// The error is logged HERE and dies here: it wraps the absolute
		// devices.json path (devices.WithLock's and Registry.Save's own wraps),
		// which is fine in the daemon's own log and must never cross the seam,
		// where a handler could interpolate it into a reply. It cannot carry the
		// token — mintDevice's block states why.
		m.logger.Warn("pair: wire mint failed",
			"event", "pairing.mint.failed",
			"requesting_device", requester.Name,
			"error", err)
		return relay.PairingMintResult{Outcome: relay.PairingMintFailed}
	}

	// THE ONE RECORD OF A SUCCESSFUL MINT. It names both devices — who asked and
	// what was created — and neither the token, its hash, nor any part of the
	// encoded pairing.
	//
	// THE TWO NAMES DO NOT CARRY THE SAME GUARANTEE. The MINTED name is
	// display-safe by construction: the relay handler refused every C0 control,
	// DEL and C1 control in it before this method was called, which is the whole
	// point of the gate this ticket added. The REQUESTING name has no such gate —
	// a paired client can set its own devices.Device.Name through
	// register_push_token, whose handler passes the payload's device_name to
	// devices.Registry.UpdatePushRegistration by design, and nothing on that path
	// checks its shape. So requester.Name — in this record, in the failure record
	// above, and as DeviceLabel in auditMint below — can carry a control
	// character.
	//
	// THAT RESIDUAL IS PRE-EXISTING AND TRACKED IN #2219, NOT CLOSED HERE. The
	// same unchecked value already reaches the same kind of sink from
	// register_push_token's own logs, the rekey handler's, and auditQuestion's
	// audit record; a predicate applied at this one call site would close none of
	// those while reading as though the hazard were handled. The fix belongs where
	// the label enters the registry — one gate, the way MintPairingPayload's is one
	// gate — which is #2219's shape.
	m.logger.Info("pair: minted a pairing over the wire",
		"event", "pairing.mint.ok",
		"requesting_device", requester.Name,
		"minted_device", minted.name)

	return relay.PairingMintResult{
		Outcome: relay.PairingMintOK,
		Pairing: pair.Encode(pair.Payload{
			Server:             m.serverID,
			Relay:              m.relayURL,
			Token:              minted.token,
			ServerStaticPubkey: m.staticPub,
		}),
	}
}

// auditMint writes exactly one terminal-decision record for a refused mint,
// carrying only the non-secret device identity (empty for a nil device —
// auditQuestion's pattern, which this is the pairing-family twin of).
//
// ModalID IS DELIBERATELY EMPTY. A mint has no one-time nonce to name, and the
// field's documented meaning is the modal nonce (protocol.ModalShownPayload's
// ModalID); filling it with a conn id or the minted hash would put a different
// kind of value in a field an operator reads as one thing. classPairingMint in
// ModalClass is what makes the record identifiable instead.
//
// ONLY REFUSALS REACH HERE. A successful mint is not a remote-permission decision
// — nothing was allowed or denied about a modal — so it is recorded by the Info
// line above, in the daemon's own log, rather than in the forensic sink for
// permission outcomes.
//
// SECURITY: audit.Entry has no field that can hold a plain device token, and this
// call supplies none — the four values are an empty id, a compile-time class
// constant, the decided outcome and the fixed source.
func (m *pairingMinterV2) auditMint(dev *devices.Device, outcome audit.Outcome) {
	var deviceHash, deviceLabel string
	if dev != nil {
		deviceHash = dev.TokenHash
		deviceLabel = dev.Name
	}
	audit.Log(m.logger, audit.Entry{
		DeviceHash:  deviceHash,
		DeviceLabel: deviceLabel,
		ModalClass:  classPairingMint,
		Outcome:     outcome,
		Source:      audit.SourceRemote,
	})
}
