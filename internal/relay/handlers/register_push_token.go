package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// pushRegistryLockWait bounds how long this handler blocks waiting for the
// cross-process devices lock. WithLock's DefaultLockWait is 5s and its own doc
// points a request-path caller at a tighter bound; this is such a caller, and the
// value matches the two other request-path acquirers, redemptionLockWait and
// wireMintLockWait. The handler runs on the dispatcher's frame goroutine, so the
// bound is the worst-case stall for that conn's frame processing, while every
// peer it contends with holds a sub-millisecond region.
//
// A var rather than a const purely so a timing test can retune it — mirroring
// pairLockWait, in the opposite direction: an interleaving test must park the
// handler on a held lock for longer than its grace, which the production bound is
// deliberately too short for. Unexported, and production never reassigns it.
var pushRegistryLockWait = 250 * time.Millisecond

// errPushDeviceGone reports that the in-region reconcile left no device carrying
// this conn's TokenHash, so there was nothing to register a push token against.
//
// It is the ONLY value that escapes the locked region distinguishably. A busy
// lock, a sidecar mkdir/open failure and a Save failure all map to the same
// retryable server.binary_busy reply, so none of them needs telling apart at the
// reply layer — only "device gone" does, and it answers on the non-retryable
// auth.invalid_token branch instead. Matched with errors.Is, which works through
// WithLock because WithLock returns fn's error verbatim rather than wrapping it.
var errPushDeviceGone = errors.New("handlers: register_push_token device gone mid-conn")

// msgUnauthorized is the user-facing message emitted in the
// auth.invalid_token error payload when a register_push_token frame
// arrives on a conn that has no authenticated Device. Should be
// unreachable in production once the dispatcher honours auth state; the
// handler emits a coherent envelope for the (dispatcher-bug) case.
const msgUnauthorized = "not authenticated; handshake required before register_push_token"

// msgBinaryBusy is the user-facing message emitted in the
// server.binary_busy error payload when the registry persist does not commit —
// a busy devices lock, a sidecar failure, or a failed Save.
//
// THE RETRY GENUINELY RE-ATTEMPTS THE WRITE, which is what makes the reply
// meaningfully retryable and is the opposite of what this comment claimed before
// #1532. Dedupe compares the payload against c.Auth(), a per-connection snapshot
// taken once at handshake (s.device in internal/relay/v2session_handshake.go)
// that no write ever updates — so a retry on the same conn does NOT dedupe away,
// whatever the in-memory registry now holds. For a busy lock that is exactly the
// behaviour wanted: the contending writer holds a sub-millisecond region, so the
// retry finds the lock free.
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
// Concurrency: the handler is stateless beyond the closure capture. Its
// reconcile, mutation and save run as ONE region under devices.WithLock
// (#1532) — reg's mutex alone cannot serialise them, since `pyry pair` is a
// separate process and every writer rewrites the whole file. Two concurrent
// calls for the same TokenHash now serialise on the file lock rather than
// interleaving at the mutex boundary, with last-writer-wins between whole
// regions. The acquisition is bounded by pushRegistryLockWait and refuses
// rather than queues past it, so a contended lock costs the conn that bound at
// most and never an unlocked write. No goroutine is spawned.
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
		// AND BEFORE THE LOCKED REGION, so a refused frame mutates no registry row,
		// creates no devices.json, and — since #1532 — acquires no lock and creates
		// no lock sidecar either, which is the stronger witness the tests assert on
		// (WithLock creates the sidecar before running anything handed to it, so its
		// absence proves the region was never entered). This handler is that
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

		// THE LOCKED REGION (#1532): reconcile, mutate and save as one critical
		// section that excludes the other OS processes writing devices.json.
		//
		// Registry.mu CANNOT serialise this. `pyry pair` and this daemon are
		// separate processes, and every writer rewrites the whole file via atomic
		// rename, so last-writer-wins on the file: without the cross-process lock a
		// `pyry pair` committing between the reload and the Save is erased by this
		// write — permanently, since Reload reconciles memory FROM disk and a record
		// no longer on disk cannot be recovered. mintDevice, runPairRevoke (#1531)
		// and recordRedemption (#1528) already read a fresh snapshot in-region; this
		// handler was the last peer that could commit over them.
		//
		// THE RECONCILE RUNS BEFORE THE MUTATION, which is what buys the revoke
		// direction for free: a device `pyry pair revoke` removed is dropped by the
		// reload, UpdatePushRegistration then reports no match, and no Save runs, so
		// this write cannot resurrect a revoked credential. The same free guarantee
		// ClearRedeemBy gets from the same ordering. It is the one deliberate reply
		// change in this slice — such a frame is now refused on the gone-mid-conn
		// branch rather than acked.
		//
		// NO NESTED ACQUISITION. Reload, UpdatePushRegistration and Save take only
		// Registry.mu; WithLock is internal/devices' sole acquirer. A second
		// acquisition from this process would be a different open file description
		// and would contend with its own caller until the wait expired.
		//
		// Registry.mu is released BETWEEN the three calls, and that is safe rather
		// than overlooked: every in-process peer that WRITES holds this same file
		// lock, and the one that does not — the v2 handshake's own Reload — is
		// harmless because reconcileDevices keeps the in-memory survivor for a hash
		// present on both sides. Under the held lock disk cannot change, so such a
		// reload reconciles to the set this region already holds and preserves the
		// mutation below.
		err := devices.WithLock(registryPath, pushRegistryLockWait, func() error {
			// Reconcile disk into memory before the whole-file Save so a device
			// `pyry pair` added since startup is not erased by this write (#782).
			// NOT REDUNDANT UNDER THE LOCK: the lock excludes a writer from
			// committing DURING the region, but says nothing about one that
			// committed between this conn's handshake reload and this acquisition —
			// which is the whole point of recordRedemption's in-region reload.
			//
			// Best-effort: on a read error, log path + a static reason and still
			// Save the known-good in-memory state (self-heal — no worse than the
			// pre-#782 blind Save). THIS IS THE ONE PLACE THE PATTERN DIVERGES FROM
			// recordRedemption, which abandons its write instead; #782's contract
			// for this handler is to self-heal, so the divergence is deliberate.
			//
			// SECURITY: the error is consumed here and never returned from this
			// closure, so it cannot reach a log field. readDevicesFile wraps a
			// decode failure that can echo devices.json bytes, and a corrupt
			// registry may carry a token_hash. That containment is what makes the
			// err field on the failure branch below safe: what can escape this
			// region is WithLock's own errors (which name only the lock path, by its
			// documented contract), Save's wraps (a path or a fixed step word), and
			// the static errPushDeviceGone. Anyone adding an error path in here owes
			// that property, or must narrow the log line instead.
			if err := reg.Reload(registryPath); err != nil {
				logger.Warn("relay: register_push_token reload failed",
					"event", "register_push_token.reload_failed",
					"conn_id", c.ConnID(),
					"device_name", dev.Name,
					"path", registryPath)
			}
			if ok := reg.UpdatePushRegistration(dev.TokenHash, p.Platform, p.Token, p.DeviceName); !ok {
				return errPushDeviceGone
			}
			return reg.Save(registryPath)
		})
		switch {
		case errors.Is(err, errPushDeviceGone):
			logger.Warn("relay: register_push_token device gone mid-conn",
				"event", "register_push_token.gone_mid_conn",
				"conn_id", c.ConnID(),
				"device_name", dev.Name)
			return replyError(ctx, c, env, protocol.CodeAuthInvalidToken, msgUnauthorized, false)
		case errors.Is(err, devices.ErrLockBusy):
			// A DISTINCT EVENT FOR AN IDENTICAL REPLY. A busy lock, a sidecar
			// failure and a Save failure are all the same retryable refusal to the
			// phone — it can act on no distinction between them — so separating
			// them at the reply layer would be noise. The log is where an operator
			// tells "another writer held it" from "the disk write failed", and a
			// log event is not a reply, so nothing about the wire contract moves.
			//
			// device_name is deliberately NOT logged here, unlike the older
			// authenticated branches: dev.Name is the pre-gate residual (#2219) and
			// may carry control characters, and a brand-new branch takes that
			// ticket's strict posture rather than this handler's older one.
			logger.Warn("relay: register_push_token devices lock busy",
				"event", "register_push_token.lock_busy",
				"conn_id", c.ConnID(),
				"path", registryPath,
				"err", err)
			return replyError(ctx, c, env, protocol.CodeServerBinaryBusy, msgBinaryBusy, true)
		case err != nil:
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
