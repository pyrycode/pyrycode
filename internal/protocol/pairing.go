package protocol

import (
	"encoding/json"
	"fmt"
)

// Pairing-mint payloads (#2126, docs/protocol-mobile.md § Minting a pairing from a
// paired client). The frame an already-paired client sends to ask the daemon to
// mint a pairing for another device, and the frame it is answered with. Wire
// vocabulary only: two structs, one bound, and the decode hook that enforces it.
//
// Its own file rather than a home beside another family, for the reason
// system_prompt.go, history.go, settings.go and snapshot.go each have one: a frame
// family owns a file.
//
// The handler that intercepts mint_pairing, authenticates the asking conn, mints
// the token and emits pairing_minted is #2127's — NOT here. Nothing in this package
// reads crypto/rand, touches devices.json or imports internal/pair.
//
// NOTHING IN THIS FILE IS EVER LOGGED IN FULL. PairingMintedPayload.Pairing is a
// plaintext bearer credential and MintPairingPayload.DeviceName is remote-authored
// text; the per-field blocks below give each its own rule.

// MaxDeviceNameBytes bounds MintPairingPayload.DeviceName. UTF-8 BYTES, NOT RUNES,
// matching every other bound in this package and the envelope-cap arithmetic they
// budget against.
//
// The value is NOT borrowed from MaxAttachmentFilenameBytes, whose 255 is POSIX
// NAME_MAX because a filename is a sanitiser input for one path component. A device
// name is never a path, so that anchor does not transfer even though the constant's
// shape does. The anchor here is native: a device label is one human-typed line
// rendered in a `pyry pair list` column and in a client's device list, and 128 bytes
// is eight times the label `pyry pair` generates when the operator names none
// ("device-" plus eight hex characters) and far past any hand-typed name, while
// keeping devices.json records small and one listing row unwrapped.
//
// A LENGTH CEILING IS NOT A SAFETY PROPERTY — MaxAttachmentIDBytes' warning
// transfers, and it is the reason this paragraph exists rather than a caveat. 128
// bytes accommodates an ANSI escape run, a newline-injection payload or
// "../../../../etc/passwd" several times over, so this constant does nothing about
// any of them. See DeviceName's own block for who owes what.
const MaxDeviceNameBytes = 128

// MintPairingPayload is the body of an Envelope whose Type == TypeMintPairing
// (docs/protocol-mobile.md § Minting a pairing from a paired client). Phone →
// binary direction; an already-paired client asking the daemon to mint a pairing
// for ANOTHER device.
//
// This is an inbound v2 *control* envelope, structurally like
// RequestSystemPromptPayload (system_prompt.go): the v2 session manager intercepts
// it at the dispatch boundary before dispatch.Route is called, so there is no
// dispatch.Route handler for it. TypeMintPairing's block has the full lane argument
// and the authentication obligation it places on that handler.
//
// ONE DIRECTION ONLY, phone → binary, so there is no provenance to disambiguate:
// EVERY FIELD IS AN UNVERIFIED CLAIM, ALWAYS. The frame it is answered with —
// PairingMintedPayload below — rides the other way and shares no field with it.
//
// NOTHING A CLIENT SENDS IS INPUT TO WHAT THE DAEMON MINTS. The token, the server
// id, the relay URL and the static pubkey all come from daemon state; this payload
// names a label and nothing else. That is why there is no nonce, no request id
// (correlation rides the envelope's InReplyTo — TypeAttachmentStored's decision,
// transferred unchanged) and no server field, and
// TestMintPairingPayload_WireKeys pins the whole key set so the absences are
// checked rather than reviewed.
//
// THERE IS NO FIELD FOR THE REMOTE-PERMISSIONS FLAG, and its absence is a security
// property rather than an omission. devices.Device.AllowRemotePermissions is
// documented as never set or carried over the wire, and it is the one gate ADR 025
// § "Security model" places on answering a permission, trust or destructive modal
// from a remote surface. A field here would let an already-paired client mint a
// device MORE privileged than an operator granted at the CLI. A MINTED DEVICE IS
// ALWAYS UNPRIVILEGED; an operator who wants otherwise runs `pyry pair
// --allow-remote-permissions` with a shell on the host.
//
// A HOSTILE OR TRUNCATED PAYLOAD DECODES TO THE ZERO VALUE rather than to an error
// for every key-shaped failure, since every key is optional to encoding/json. The
// ONE decode rejection is the length bound below, and it is the package's first: a
// DECODE FAILURE IS A REJECTED FRAME, NEVER AN EMPTY-BUT-SUCCESSFUL MINT REQUEST.
// #2127 answers protocol.malformed on it.
type MintPairingPayload struct {
	// DeviceName is the label the minted device is filed under in the registry
	// (devices.Device.Name). OPTIONAL: absent and empty are the SAME case, "the
	// client named no device", which the handler answers by falling back to the
	// label `pyry pair` generates for an unnamed device. There is no presence
	// contract, so there is NO omitempty — RequestSystemPromptPayload's,
	// RequestSessionSettingsPayload's and RequestModelListPayload's stated reason,
	// and keeping the key always on the wire is what lets the committed fixture
	// pin the full shape. TestMintPairingPayload_ZeroValue_KeyPresent reddens if
	// an omitempty is added later for tidiness.
	//
	// AN UNVERIFIED CLAIM, ALWAYS, and the two hazards are named here because
	// MaxDeviceNameBytes does nothing about either. It is rendered to a TERMINAL
	// by `pyry pair list`, so an ANSI escape run in it is a display-forgery vector
	// the renderer owes escaping for; and it is a candidate for a LINE-ORIENTED
	// LOG, the same log-injection shape docs/protocol-mobile.md § Attachments
	// forbids for an unchecked filename. LOGGABLE ONLY AFTER SHAPE VALIDATION,
	// which no code in this package performs.
	//
	// IT NEVER BECOMES A PATH COMPONENT, and that is checked rather than assumed:
	// resolveDevicesPath applies sanitizeName to the INSTANCE name, never to a
	// device name, so this value is a JSON value inside devices.json and nothing
	// more. A future edit that changes that must bring its own canonical-shape
	// check; this bound would not be it.
	DeviceName string `json:"device_name"`
}

// UnmarshalJSON rejects a DeviceName longer than MaxDeviceNameBytes.
//
// THIS IS VALIDATION, NOT NORMALISATION, and it is a deliberate departure from the
// package's pure-DTO posture rather than a drift into one. SendMessagePayload's
// UnmarshalJSON — the only other hook here — collapses three empty wire forms and
// checks nothing; this one refuses. The argument is specific to this field and is
// not a general licence:
//
//   - Every other bounded string in this package (Filename, AttachmentID) is bounded
//     for a downstream consumer that must check the value's SHAPE anyway, so the
//     constant is cap arithmetic and enforcement belongs at that consumer.
//     device_name has no shape rule at all — it is a free display string — so length
//     is the only check that will ever exist, and there is no second validator
//     downstream to carry it.
//   - Three implementations are being written against this contract at once (#2127
//     and two client slices), none able to see the others. A bound living only in a
//     doc comment is a bound each of them implements differently or forgets.
//   - The reject is FAIL-CLOSED. Truncating instead would let the label a client
//     displays differ from the one the daemon stored, on a frame whose whole subject
//     is which device a credential belongs to.
//
// THE ERROR NEVER CARRIES THE BYTES. It names the category, the bound and the
// observed count — internal/pair's ErrInvalidPayload rule, applied because the value
// is remote-authored and the rejection reaches a line-oriented log. A device name's
// LENGTH is not sensitive; its content is untrusted.
//
// UNKNOWN FIELDS STAY IGNORED. This decodes through the alias rather than through a
// json.Decoder with DisallowUnknownFields, which would turn every future client
// sending a field this daemon predates into a rejected frame and break the
// forward-compatibility rule docs/protocol-mobile.md § Application message types
// states. TestMintPairingPayload_UnknownFieldIgnored pins it against exactly that
// edit.
//
// The alias type is a defined type with no methods, which is what keeps
// json.Unmarshal from recursing back into this method — SendMessagePayload's reason
// in the same direction. The receiver is a pointer, as encoding/json requires for an
// Unmarshaler, so this writes only through the caller's own value, and it writes
// NOTHING on the reject path.
func (p *MintPairingPayload) UnmarshalJSON(b []byte) error {
	type alias MintPairingPayload
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	if n := len(a.DeviceName); n > MaxDeviceNameBytes {
		return fmt.Errorf("protocol: mint_pairing device_name is %d bytes, over the %d-byte bound", n, MaxDeviceNameBytes)
	}
	*p = MintPairingPayload(a)
	return nil
}

// PairingMintedPayload is the body of an Envelope whose Type == TypePairingMinted
// (docs/protocol-mobile.md § Minting a pairing from a paired client). Binary → phone
// direction; the daemon's answer to a mint_pairing, correlated by InReplyTo.
//
// PAIRING IS A PLAINTEXT BEARER CREDENTIAL. It is never logged, no field decoded out
// of it is ever logged, and it must never leave the AEAD-sealed envelope — under
// docs/protocol-mobile.md § Security model threat 3 the relay sees only ciphertext,
// and carrying this string anywhere else would hand the token to the one party v2
// exists to keep it from. internal/pair's package doc binds ITS callers; that rule
// does not reach a different type in a different package on its own, which is why it
// is restated here.
//
// THE ENCODED STRING TRAVELS ALONE, and that is the frame's whole shape decision.
// The four fields inside it are pair.Payload's — server, relay, token,
// server_static_pubkey — and three of them are values the asking client already
// holds: it is paired, and connected through that relay to that server. Only the
// token is new, and it is the secret. Carrying the four flat alongside would put a
// bearer credential on the wire twice and invite a client to treat the loose copy as
// the less sensitive one. Carrying none of them leaves one source of truth, keeps
// the frame near 400 bytes against the 65519-byte application-envelope cap, and
// means this package needs no import of internal/pair — which is what keeps this
// leaf package leaf-shaped.
//
// NO FIELD OF THE REQUEST IS ECHOED INTO THE REPLY, and that is checked rather than
// borrowed from a neighbour that enumerated different fields: pair.Payload has
// exactly four fields and none of them is a name, so this payload is daemon-authored
// end to end and the client's device_name reaches no outbound frame.
//
// OPAQUE ON THIS WIRE. A client hands the string to its existing pairing dialog
// unchanged — both the phone and the desktop already accept a pasted pair.Encode
// string — so no client needs to parse one, and pair.Decode remains the only
// decoder anywhere.
//
// NO EXPIRY FIELD, deliberately. devices.Device.RedeemBy stamps an unredeemed
// pairing's deadline, but its semantics are #1528/#1529's and this slice cannot
// honour them; publishing the field ahead of the behaviour is the shape #2090
// stands as the example of. Whether a client should see the deadline is #2127's
// call — it is the first ticket blocked on the window work and so the first that
// knows the answer.
//
// CORRELATION RIDES THE ENVELOPE'S InReplyTo, so the payload carries NO REQUEST-ID
// KEY and no conversation id — this verb is not conversation-scoped at all.
type PairingMintedPayload struct {
	// Pairing is the pair.Encode string: the {server, relay, token,
	// server_static_pubkey} tuple as base64url (URL-safe alphabet, no padding).
	// A CREDENTIAL — see the block above for the handling rule.
	Pairing string `json:"pairing"`
}
