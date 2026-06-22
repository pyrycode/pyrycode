// Package audit is the forensic sink for remote-permission decisions: it
// writes exactly one structured record per decision the modal control loop
// (#703) resolves — which device, which modal, what outcome, from where.
//
// SECURITY: an Entry carries ONLY non-secret identity. It has no field that
// can hold a plain device token or any other secret (honoring the
// internal/devices SECURITY contract): the device is identified by its
// SHA-256 hash and/or label, never the plain token. The package imports only
// log/slog — it never imports internal/devices, so it cannot reach a plain
// token. The writer emits a fixed attribute set; a future edit that adds a
// secret-bearing field is caught by the no-leak test in audit_test.go.
//
// The sink is write-only and local (ADR 025 §6 "Audit"): it reads nothing
// from the network and never emits to the wire. It is decoupled from the
// authorization gate (#702) — Log records the already-decided Outcome and
// never consults the gate or re-derives the decision.
package audit

import "log/slog"

// Entry is one resolved remote-permission decision to be recorded. It carries
// ONLY non-secret identity: the device's TokenHash / Name (NEVER the plain
// token — the struct has no field that could hold one), the one-time modal
// nonce, the modal class, the resolved outcome, and where the decision
// originated. It is constructed in-process by #703 and never decoded from the
// network.
type Entry struct {
	DeviceHash  string  // device.TokenHash (SHA-256 hex); "" for a no-device timeout
	DeviceLabel string  // device.Name
	ModalID     string  // protocol.ModalShownPayload.ModalID — the one-time nonce (#701)
	ModalClass  string  // protocol.ModalShownPayload.Class — e.g. "permission" (ADR 025 §6 "class")
	Outcome     Outcome // the self-contained decision classification (below)
	Source      Source  // where the decision originated (mirrors the wire set)
}

// Outcome is the security classification of a resolved remote-permission
// decision. Self-contained per this ticket (#712); #703 maps
// devices.RemotePermissionOutcome + eligibility onto it. String-backed so the
// serialized audit record is stable and human-readable regardless of constant
// order.
type Outcome string

const (
	OutcomeAllowed            Outcome = "allowed"             // eligible device + explicit allow (the sole grant)
	OutcomeDeniedUnauthorized Outcome = "denied_unauthorized" // denied: no authorization bit
	OutcomeDeniedTimeout      Outcome = "denied_timeout"      // denied: deny-on-timeout window elapsed
	OutcomeCancelled          Outcome = "cancelled"           // phone cancelled / dismissed (ESC)
	OutcomeDenied             Outcome = "denied"              // authorized phone explicitly chose a deny option
)

// Source is where the decision originated. The value set deliberately mirrors
// protocol.ModalDismissedPayload.Source's documented closed set {remote,
// local, timeout}, so #703 passes ONE source value to both the wire dismissal
// and this audit entry — no second, divergent source vocabulary.
type Source string

const (
	SourceRemote  Source = "remote"  // a remote inbound answer (the answering device/connection)
	SourceTimeout Source = "timeout" // the daemon's own internal safe-deny on timeout
	SourceLocal   Source = "local"   // resolved at the desktop TTY (ADR 025 §4 first-answer-wins; #706)
)

// Log writes exactly one structured audit record for a resolved
// remote-permission decision. It records the already-decided Entry verbatim —
// it does NOT consult the gate, re-derive the outcome, or touch any token. A
// nil logger defaults to slog.Default() (matching the repo's optional-logger
// convention); the write never panics. Emitted at slog.Info; the slog record's
// automatic timestamp satisfies ADR 025 §6's "time" requirement.
func Log(logger *slog.Logger, e Entry) {
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info("audit: remote permission decision",
		slog.String("device_hash", e.DeviceHash),
		slog.String("device_label", e.DeviceLabel),
		slog.String("modal_id", e.ModalID),
		slog.String("modal_class", e.ModalClass),
		slog.String("outcome", string(e.Outcome)),
		slog.String("source", string(e.Source)),
	)
}
