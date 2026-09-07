package relay

import (
	"errors"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
)

// redemptionLockWait bounds how long the redemption write blocks waiting for
// the devices lock. WithLock's DefaultLockWait is 5s and its doc says a
// request-path caller should pick a tighter bound rather than inherit it; this
// is such a caller. handleNoiseInit runs on the session's Run goroutine, so the
// bound is the worst-case stall for that session's frame processing, while the
// peers it contends with (`pyry pair`, `pyry pair revoke`, the push-token
// handler) each hold sub-millisecond regions.
const redemptionLockWait = 250 * time.Millisecond

// errRedemptionReloadFailed replaces the in-region Reload's own error before it
// can escape the locked region.
//
// SECURITY: readDevicesFile wraps a decode failure that can echo devices.json
// bytes, and a corrupt registry may carry a token_hash — so that error must
// never reach a log field. Substituting this static sentinel is what lets
// recordRedemption log its error unconditionally: WithLock names only the lock
// path by its own documented contract, and Save's wraps name a path or a fixed
// step word. Anyone adding an error path inside the region must keep that
// property true or narrow the log line instead.
var errRedemptionReloadFailed = errors.New("relay: devices reload failed inside the redemption lock")

// recordRedemption durably records that dev's pairing token has been redeemed,
// by clearing Device.RedeemBy and persisting the registry (#1528). Called from
// the v2 handshake's accept tail once dev has authenticated; #1529 enforces the
// deadline this write retires.
//
// Best effort by design: every failure is logged and swallowed, because a
// registry the daemon cannot write must not cost the phone a handshake that has
// already succeeded. Nothing here is a security decision — the accept happened
// before this ran.
//
// Two guards run before the lock, so a device with no deadline costs nothing at
// all — not a lock acquisition, not even the sidecar's creation. They read a
// snapshot Validate took before the lock, which makes them a fast path only:
// correctness is re-decided inside the region by ClearRedeemBy's return, so a
// deadline cleared by a raced writer between the two simply writes nothing.
//
// The region is Reload -> ClearRedeemBy -> Save. The Reload is not redundant
// with the handshake's earlier one: the lock excludes a writer from committing
// during the region, but says nothing about a `pyry pair` that committed
// between that reload and this acquisition. Reloading here makes disk
// authoritative for membership, which is what keeps the Save from dropping a
// record this daemon never read — and, in the revoke direction, what stops it
// resurrecting a device `pyry pair revoke` removed (the reload drops it from
// memory, ClearRedeemBy then reports no change, and no Save runs).
//
// A Save failure leaves memory cleared and disk not, and because
// reconcileDevices keeps the in-memory struct across later reloads, this daemon
// will not retry. It self-heals on the next restart, which re-reads the
// deadline from disk. If that restart lands after the window has passed, #1529
// rejects the device and the operator re-pairs — a fail-closed operational
// outcome, announced by the Warn below.
func (m *V2SessionManager) recordRedemption(connID string, dev devices.Device) {
	if m.cfg.DevicesPath == "" || dev.RedeemBy.IsZero() {
		return
	}
	err := devices.WithLock(m.cfg.DevicesPath, redemptionLockWait, func() error {
		if err := m.cfg.Devices.Reload(m.cfg.DevicesPath); err != nil {
			return errRedemptionReloadFailed
		}
		if !m.cfg.Devices.ClearRedeemBy(dev.TokenHash) {
			return nil
		}
		return m.cfg.Devices.Save(m.cfg.DevicesPath)
	})
	if err != nil {
		m.cfg.Logger.Warn("relay: v2 redemption persist failed",
			"event", "v2.devices.redeem_persist_failed",
			"conn_id", connID,
			"path", m.cfg.DevicesPath,
			"err", err)
	}
}
