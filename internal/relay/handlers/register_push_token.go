package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// msgUnauthorized is the user-facing message emitted in the
// auth.invalid_token error payload when a register_push_token frame
// arrives on a conn that has no authenticated Device. Should be
// unreachable in production once the dispatcher honours auth state; the
// handler emits a coherent envelope for the (dispatcher-bug) case.
const msgUnauthorized = "not authenticated; handshake required before register_push_token"

// msgBinaryBusy is the user-facing message emitted in the
// server.binary_busy error payload when registry persistence fails. The
// phone retries; on the retry, dedupe will succeed (in-memory is already
// updated) and no further write attempt occurs.
const msgBinaryBusy = "registry save in progress; retry"

// msgMalformed is the user-facing message emitted in the
// protocol.malformed error payload when RegisterPushTokenPayload cannot
// be JSON-decoded. The decode-error text is NOT echoed back (it could
// reflect attacker-controlled payload bytes); only this static string.
const msgMalformed = "malformed register_push_token payload"

// msgDeviceNameTooLong, msgDeviceNameUnsafe and msgPlatformUnsafe are the
// user-facing messages for the three display-safety rejects (#2219). All three
// are non-retryable, like msgMalformed: re-sending the same bytes fails
// identically, and the phone sends this frame on every WS connect.
//
// EACH BRANCH CARRIES ITS OWN, RenameWorkspace's posture rather than one shared
// string, so a client is told which field to fix. NAMING THE FIELD IS NOT AN
// ORACLE: the client authored both values, so the reply reports nothing it did
// not already hold — pairing.not_permitted's published reasoning — and a frame
// reaching here was AEAD-authenticated under the paired Noise session, so no
// third party can probe with one.
//
// NONE OF THEM NAMES THE VALUE, ITS LENGTH OR THE BOUND. The bytes are
// remote-authored and are exactly what the gate exists to keep out of a
// line-oriented sink; a reply quoting them would hand the payload straight to
// whatever renders the error. Omitting the bound additionally keeps a reply from
// being used to binary-search it — msgRenameWorkspaceLabelTooLong's reason.
const (
	msgDeviceNameTooLong = "device_name exceeds the maximum length"
	msgDeviceNameUnsafe  = "device_name contains a disallowed control character"
	msgPlatformUnsafe    = "platform contains a disallowed control character"
)

// pushFieldIsDisplaySafe reports whether a client-authored register_push_token
// field may be stored, logged and rendered. A refused rune is a C0 control
// (LF and CR, the log-injection shape docs/protocol-mobile.md § Attachments
// forbids for an unchecked filename, and ESC, which every ANSI escape run begins
// with), DEL, or a C1 control (U+0080–U+009F, which some terminals still act on
// and which a C0-only reading would miss).
//
// THE REFUSED SET IS mintLabelIsDisplaySafe's, EXACTLY, and it must stay that
// way: both gates guard the same devices.Device.Name field reaching the same
// sinks, and a set that drifted between them would leave one door narrower than
// the other with nothing to say which is right. internal/sessions'
// admissibleClientField is a third copy of this loop and is NOT the one to
// follow — it additionally refuses a double quote and invalid UTF-8 for reasons
// its own block states about a system prompt it renders, and neither transfers
// here. The set moves in all three or in none.
//
// IT IS A RESTATEMENT RATHER THAN A SHARED HELPER, deliberately. Reaching
// mintLabelIsDisplaySafe would make this package import its own parent
// (internal/relay) for a six-line loop; hoisting it into internal/protocol would
// contradict RenameWorkspacePayload's stated reason for keeping validation out of
// the DTO package, and would mean rewriting a just-landed security gate as a side
// effect of this one.
//
// UTF-8 VALIDITY IS NOT CHECKED, a fact about the decoder rather than an
// omission: encoding/json replaces every invalid byte and unpaired surrogate in
// its input with U+FFFD, so a decoded Go string is valid UTF-8 by construction.
// A check here could not be made to fail, and an assertion no test can redden is
// worse than none.
//
// THE EMPTY STRING PASSES. For device_name, absent and empty are the same case —
// "the client named no device" — which this frame has always accepted, and
// refusing it would change behaviour the ticket requires be left alone.
func pushFieldIsDisplaySafe(v string) bool {
	for _, r := range v {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return false
		}
	}
	return true
}

// RegisterPushToken returns a dispatch.Handler that processes a
// register_push_token frame from the phone. reg is the devices registry;
// registryPath is the canonical on-disk path passed to Save; logger is
// the daemon's slog logger used for every branch's structured event.
//
// SECURITY:
//   - The push token (p.Token, dev.PushToken) is opaque infrastructure
//     data (FCM/APNs registration id); not a secret on par with the
//     device auth token. It is logged at INFO when a write happens (as
//     a side-effect of the write event) and never as a field value at
//     any level.
//   - The Device.Name is logged on every authenticated branch that
//     predates #2219. The unauth branch has no name to log, and that
//     ticket's three reject branches deliberately log neither it nor the
//     payload's own value.
//   - p.DeviceName and p.Platform are CLIENT-AUTHORED and are gated here
//     before they can be stored (#2219) — see the block at the guards.
//     dev.Name, the value already in the registry, carries NO such
//     guarantee and Device.Name is not display-safe as a type invariant:
//     a name written before this gate existed is read back unchecked, and
//     `pyry pair --name` is operator-authored and deliberately ungated
//     (mintLabelIsDisplaySafe's block states why the check does not live
//     in the shared mint step). A consumer still owes its own escaping.
//
// Concurrency: the handler is stateless beyond the closure capture.
// reg's mutex serialises UpdatePushRegistration and Save independently;
// two concurrent calls for the same TokenHash interleave at the mutex
// boundary with documented last-writer-wins semantics.
func RegisterPushToken(reg *devices.Registry, registryPath string, logger *slog.Logger) dispatch.Handler {
	return func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
		dev := c.Auth()
		if dev == nil {
			logger.Warn("relay: register_push_token unauth",
				"event", "register_push_token.unauth",
				"conn_id", c.ConnID(),
				"code", protocol.CodeAuthInvalidToken)
			return replyError(ctx, c, env, protocol.CodeAuthInvalidToken, msgUnauthorized, false)
		}

		var p protocol.RegisterPushTokenPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			// err is NOT logged (#2219): encoding/json quotes offending input into
			// its error text, and a type error midway through a well-formed object
			// returns after the earlier fields are already populated with supplied
			// bytes. RenameWorkspace's malformed branch states the same.
			logger.Warn("relay: register_push_token malformed payload",
				"event", "register_push_token.malformed",
				"conn_id", c.ConnID(),
				"device_name", dev.Name)
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgMalformed, false)
		}

		// THE DISPLAY-SAFETY GATE (#2219), and its placement is the whole point.
		//
		// IT RUNS BEFORE THE DEDUPE COMPARISON BELOW, which is load-bearing rather
		// than stylistic. Dedupe acks WITHOUT writing when the payload triple equals
		// the stored one, so a device whose name was stored before this gate existed
		// could repeat that unsafe value on every connect and be acked, never
		// reaching a check placed after the comparison. Running first makes "an
		// unsafe value is refused" true of every frame rather than of every CHANGED
		// frame — TestRegisterPushToken_UnsafeNameMatchingStored_RefusedNotDeduped
		// is the assertion, and it is the only one that can tell the two orderings
		// apart.
		//
		// AND BEFORE UpdatePushRegistration, Reload AND Save, so a refused frame
		// mutates no registry row and creates no devices.json. This handler is that
		// method's only production caller, which is what makes one gate here cover
		// every sink Device.Name reaches: this handler's own records, the rekey and
		// handshake success logs, audit.Entry.DeviceLabel at auditQuestion, both
		// modalResolverV2 sites and pairingMinterV2.auditMint, the pairing.mint.ok
		// record, and a `pyry pair list` column on an operator's terminal.
		//
		// REFUSED, NEVER TRUNCATED OR REPAIRED. A shortened or scrubbed name is a
		// name the client did not send, on a frame whose whole subject is which
		// label a device is filed under — MintPairingPayload.UnmarshalJSON's
		// fail-closed argument, for the same field.
		//
		// Every branch logs event and conn_id and NOTHING ELSE — RenameWorkspace's
		// strict posture, not this handler's older one. The payload's bytes are what
		// the gate exists to exclude, and dev.Name is not logged here either: on a
		// device paired before this gate it is the pre-existing residual below, and
		// a brand-new record is not the place to surface it.
		//
		// token is deliberately UNCHECKED. The SECURITY block above establishes it
		// is never a log field value, and its only other sink is a JSON string
		// inside devices.json, which encoding/json escapes. platform gets the
		// character check but no byte bound: this ticket has no anchor for one, and
		// its residual ceiling is the ~64KB application-envelope cap — the posture
		// HelloClientPayload's fields are documented as taking.
		//
		// len() counts BYTES, which is the bound's unit; see
		// protocol.MaxDeviceNameBytes on why bytes and not runes.
		if len(p.DeviceName) > protocol.MaxDeviceNameBytes {
			logger.Warn("relay: register_push_token device_name over bound",
				"event", "register_push_token.device_name_too_long",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgDeviceNameTooLong, false)
		}
		if !pushFieldIsDisplaySafe(p.DeviceName) {
			logger.Warn("relay: register_push_token device_name not display-safe",
				"event", "register_push_token.device_name_unsafe",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgDeviceNameUnsafe, false)
		}
		if !pushFieldIsDisplaySafe(p.Platform) {
			logger.Warn("relay: register_push_token platform not display-safe",
				"event", "register_push_token.platform_unsafe",
				"conn_id", c.ConnID())
			return replyError(ctx, c, env, protocol.CodeProtocolMalformed, msgPlatformUnsafe, false)
		}

		if p.Platform == dev.Platform && p.Token == dev.PushToken && p.DeviceName == dev.Name {
			logger.Debug("relay: register_push_token dedupe",
				"event", "register_push_token.dedupe",
				"conn_id", c.ConnID(),
				"device_name", dev.Name)
			return replyAck(ctx, c, env)
		}

		if ok := reg.UpdatePushRegistration(dev.TokenHash, p.Platform, p.Token, p.DeviceName); !ok {
			logger.Warn("relay: register_push_token device gone mid-conn",
				"event", "register_push_token.gone_mid_conn",
				"conn_id", c.ConnID(),
				"device_name", dev.Name)
			return replyError(ctx, c, env, protocol.CodeAuthInvalidToken, msgUnauthorized, false)
		}

		// Reconcile disk into memory before the whole-file Save so a device
		// `pyry pair` added since startup is not erased by this write (#782).
		// Best-effort: on a read error, log path + a static reason and still
		// Save the known-good in-memory state (self-heal — no worse than the
		// pre-#782 blind Save). SECURITY: never log the wrapped err; a corrupt
		// devices.json can carry file bytes (a token_hash).
		if err := reg.Reload(registryPath); err != nil {
			logger.Warn("relay: register_push_token reload failed",
				"event", "register_push_token.reload_failed",
				"conn_id", c.ConnID(),
				"device_name", dev.Name,
				"path", registryPath)
		}

		if err := reg.Save(registryPath); err != nil {
			logger.Warn("relay: register_push_token save failed",
				"event", "register_push_token.save_failed",
				"conn_id", c.ConnID(),
				"device_name", dev.Name,
				"err", err)
			return replyError(ctx, c, env, protocol.CodeServerBinaryBusy, msgBinaryBusy, true)
		}

		logger.Info("relay: register_push_token write",
			"event", "register_push_token.write",
			"conn_id", c.ConnID(),
			"device_name", p.DeviceName,
			"platform", p.Platform)
		return replyAck(ctx, c, env)
	}
}

func replyAck(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
	payload, err := json.Marshal(protocol.AckPayload{})
	if err != nil {
		return fmt.Errorf("marshal ack payload: %w", err)
	}
	return c.Reply(ctx, env, protocol.TypeAck, payload)
}

func replyError(ctx context.Context, c *dispatch.Conn, env protocol.Envelope, code, message string, retryable bool) error {
	payload, err := json.Marshal(protocol.ErrorPayload{
		Code:      code,
		Message:   message,
		Retryable: retryable,
	})
	if err != nil {
		return fmt.Errorf("marshal error payload: %w", err)
	}
	return c.Reply(ctx, env, protocol.TypeError, payload)
}
