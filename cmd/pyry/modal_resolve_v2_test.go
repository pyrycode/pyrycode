package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/audit"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/supervisor"
	"github.com/pyrycode/tui-driver/pkg/tuidriver"
)

// secretModalBody is a distinctive modal prompt body used to prove the resolver
// never logs application content (the prompt/title/body) in any field.
const secretModalBody = "ULTRA-SECRET-MODAL-BODY-1234"

// fakeKeystroker records every safe-answer verb call and returns an injectable
// error. The resolver is called single-threaded in these tests, so no mutex is
// needed.
type fakeKeystroker struct {
	escCalls    int
	answerCalls []string // one entry per Answer(choice), in call order
	trustCalls  int
	err         error
}

func (f *fakeKeystroker) SendEsc() error {
	f.escCalls++
	return f.err
}

func (f *fakeKeystroker) Answer(choice string) error {
	f.answerCalls = append(f.answerCalls, choice)
	return f.err
}

func (f *fakeKeystroker) AcceptTrust() error {
	f.trustCalls++
	return f.err
}

// routedNothing reports whether no safe-answer verb was actuated.
func (f *fakeKeystroker) routedNothing() bool {
	return f.escCalls == 0 && f.trustCalls == 0 && len(f.answerCalls) == 0
}

// recordPermissionModal scripts one outstanding permission modal carrying body
// as its (defensively bounded) prompt and returns the minted modal_id.
func recordPermissionModal(t *testing.T, reg *modalbridge.Registry, body string) string {
	t.Helper()
	req, wireClass, ok := modalbridge.PermissionRequestForClass(tuidriver.ModalClassPermission, body)
	if !ok {
		t.Fatal("PermissionRequestForClass(permission) not ok")
	}
	payload, err := reg.Record(req, wireClass)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	return payload.ModalID
}

// recordTrustModal scripts one outstanding trust-folder modal (options
// proceed/exit) carrying body as its prompt and returns the minted modal_id.
// The trust-class sibling of recordPermissionModal.
func recordTrustModal(t *testing.T, reg *modalbridge.Registry, body string) string {
	t.Helper()
	req, wireClass, ok := modalbridge.PermissionRequestForClass(tuidriver.ModalClassTrustFolder, body)
	if !ok {
		t.Fatal("PermissionRequestForClass(trust) not ok")
	}
	payload, err := reg.Record(req, wireClass)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	return payload.ModalID
}

// auditLogger returns a JSON-backed slog logger (Debug level, so Warn/Debug are
// captured too) writing into buf, suitable for record-level assertions.
func auditLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// auditRecords parses every JSON log line in buf and returns those that are
// audit records (msg == "audit: remote permission decision").
func auditRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var recs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("parse log line %q: %v", line, err)
		}
		if m["msg"] == "audit: remote permission decision" {
			recs = append(recs, m)
		}
	}
	return recs
}

// testDevice is the ineligible baseline: a paired device with the
// remote-permission opt-in bit OFF (MayAnswerRemotePermission == false).
func testDevice(t *testing.T) *devices.Device {
	t.Helper()
	return &devices.Device{TokenHash: devices.HashToken("plain-device-token"), Name: "test-phone"}
}

// eligibleDevice is testDevice plus the remote-permission opt-in bit set: the
// gated device whose answers may route a keystroke.
func eligibleDevice(t *testing.T) *devices.Device {
	t.Helper()
	d := testDevice(t)
	d.AllowRemotePermissions = true
	return d
}

// TestModalResolverV2_Cancel_HappyPath proves a cancel consumes the modal,
// routes exactly one ESC, audits {cancelled, remote} with the non-secret
// identity fields (and no modal body), and returns a {cancelled, remote}
// dismissal. AC-1, AC-5.
func TestModalResolverV2_Cancel_HappyPath(t *testing.T) {
	t.Parallel()

	reg := modalbridge.New()
	modalID := recordPermissionModal(t, reg, secretModalBody)
	kb := &fakeKeystroker{}
	logger, logBuf := auditLogger()
	dev := testDevice(t)

	r := newModalResolverV2(reg, kb, logger)
	d, ok := r.ResolveCancel(modalID, dev)

	if !ok {
		t.Fatal("ResolveCancel ok = false, want true")
	}
	if d.Outcome != string(audit.OutcomeCancelled) || d.Source != string(audit.SourceRemote) {
		t.Errorf("dismissal = %+v, want {cancelled remote}", d)
	}
	if kb.escCalls != 1 {
		t.Errorf("SendEsc calls = %d, want 1", kb.escCalls)
	}
	// The modal is consumed: a second Resolve misses.
	if _, stillThere := reg.Resolve(modalID); stillThere {
		t.Error("modal still in registry after cancel; Resolve must consume it")
	}

	recs := auditRecords(t, logBuf)
	if len(recs) != 1 {
		t.Fatalf("audit records = %d, want 1", len(recs))
	}
	rec := recs[0]
	wantFields := map[string]string{
		"level":        "INFO",
		"outcome":      "cancelled",
		"source":       "remote",
		"modal_id":     modalID,
		"modal_class":  "permission",
		"device_hash":  dev.TokenHash,
		"device_label": dev.Name,
	}
	for k, want := range wantFields {
		if got, _ := rec[k].(string); got != want {
			t.Errorf("audit field %q = %q, want %q", k, got, want)
		}
	}
	// SECURITY: no modal body/prompt/title in ANY log field.
	if strings.Contains(logBuf.String(), secretModalBody) {
		t.Error("modal body leaked into a log field")
	}
	if strings.Contains(logBuf.String(), "Permission required") {
		t.Error("modal title leaked into a log field")
	}
}

// TestModalResolverV2_Cancel_UnknownID proves an unknown id is a no-op: no
// keystroke, no audit, (zero,false) return. AC-4.
func TestModalResolverV2_Cancel_UnknownID(t *testing.T) {
	t.Parallel()

	reg := modalbridge.New()
	kb := &fakeKeystroker{}
	logger, logBuf := auditLogger()

	r := newModalResolverV2(reg, kb, logger)
	d, ok := r.ResolveCancel("nonexistent-modal", testDevice(t))

	if ok {
		t.Error("ResolveCancel ok = true for unknown id, want false")
	}
	if d != (relay.ModalDismissal{}) {
		t.Errorf("dismissal = %+v, want zero", d)
	}
	if kb.escCalls != 0 {
		t.Errorf("SendEsc calls = %d, want 0", kb.escCalls)
	}
	if recs := auditRecords(t, logBuf); len(recs) != 0 {
		t.Errorf("audit records = %d, want 0", len(recs))
	}
}

// TestModalResolverV2_Cancel_AlreadyResolved proves the registry consume is the
// single idempotency gate: a second cancel of the same id is a no-op — no second
// keystroke, no second audit record. AC-4.
func TestModalResolverV2_Cancel_AlreadyResolved(t *testing.T) {
	t.Parallel()

	reg := modalbridge.New()
	modalID := recordPermissionModal(t, reg, secretModalBody)
	kb := &fakeKeystroker{}
	logger, logBuf := auditLogger()
	dev := testDevice(t)

	r := newModalResolverV2(reg, kb, logger)
	if _, ok := r.ResolveCancel(modalID, dev); !ok {
		t.Fatal("first ResolveCancel ok = false, want true")
	}
	d, ok := r.ResolveCancel(modalID, dev) // same id again
	if ok {
		t.Error("second ResolveCancel ok = true, want false (already consumed)")
	}
	if d != (relay.ModalDismissal{}) {
		t.Errorf("second dismissal = %+v, want zero", d)
	}
	if kb.escCalls != 1 {
		t.Errorf("SendEsc calls = %d, want 1 (no second keystroke)", kb.escCalls)
	}
	if recs := auditRecords(t, logBuf); len(recs) != 1 {
		t.Errorf("audit records = %d, want 1 (no second record)", len(recs))
	}
}

// TestModalResolverV2_Cancel_KeystrokeError proves a SendEsc error is tolerated:
// the modal is still consumed, a Warn (with the non-secret err) is logged, and
// the audit record + {cancelled, remote, true} return still happen.
func TestModalResolverV2_Cancel_KeystrokeError(t *testing.T) {
	t.Parallel()

	reg := modalbridge.New()
	modalID := recordPermissionModal(t, reg, secretModalBody)
	kb := &fakeKeystroker{err: supervisor.ErrNoLiveSession}
	logger, logBuf := auditLogger()
	dev := testDevice(t)

	r := newModalResolverV2(reg, kb, logger)
	d, ok := r.ResolveCancel(modalID, dev)

	if !ok {
		t.Fatal("ResolveCancel ok = false on keystroke error, want true (modal already consumed)")
	}
	if d.Outcome != string(audit.OutcomeCancelled) || d.Source != string(audit.SourceRemote) {
		t.Errorf("dismissal = %+v, want {cancelled remote}", d)
	}
	if _, stillThere := reg.Resolve(modalID); stillThere {
		t.Error("modal still in registry after keystroke-error cancel; it must be consumed")
	}
	if recs := auditRecords(t, logBuf); len(recs) != 1 {
		t.Errorf("audit records = %d, want 1 despite keystroke error", len(recs))
	}
	out := logBuf.String()
	if !strings.Contains(out, "modal_cancel.keystroke_err") {
		t.Errorf("expected a keystroke_err warn log; got:\n%s", out)
	}
	if strings.Contains(out, secretModalBody) {
		t.Error("modal body leaked into a log field on the keystroke-error path")
	}
}

// TestModalResolverV2_Answer_Authorized drives the four authorized answer paths
// (allow/deny × permission/trust) from a gated device and asserts each routes the
// correct safe-answer verb, consumes the modal, audits the right outcome with the
// non-secret identity fields, and returns a {option_id, remote} dismissal. AC-1.
func TestModalResolverV2_Answer_Authorized(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		trust     bool // record a trust modal instead of a permission modal
		optionID  string
		wantAudit string
		wantKey   func(t *testing.T, kb *fakeKeystroker)
	}{
		{
			name:      "allow_once permission routes Answer(1)",
			optionID:  "allow_once",
			wantAudit: "allowed",
			wantKey: func(t *testing.T, kb *fakeKeystroker) {
				if len(kb.answerCalls) != 1 || kb.answerCalls[0] != "1" {
					t.Errorf("answerCalls = %v, want [1]", kb.answerCalls)
				}
				if kb.escCalls != 0 || kb.trustCalls != 0 {
					t.Errorf("unexpected esc=%d trust=%d", kb.escCalls, kb.trustCalls)
				}
			},
		},
		{
			name:      "reject_once permission routes Answer(3)",
			optionID:  "reject_once", // 3rd option in the permission set -> key "3"
			wantAudit: "denied",
			wantKey: func(t *testing.T, kb *fakeKeystroker) {
				if len(kb.answerCalls) != 1 || kb.answerCalls[0] != "3" {
					t.Errorf("answerCalls = %v, want [3]", kb.answerCalls)
				}
			},
		},
		{
			name:      "proceed trust routes AcceptTrust",
			trust:     true,
			optionID:  "proceed",
			wantAudit: "allowed",
			wantKey: func(t *testing.T, kb *fakeKeystroker) {
				if kb.trustCalls != 1 {
					t.Errorf("trustCalls = %d, want 1", kb.trustCalls)
				}
				if len(kb.answerCalls) != 0 || kb.escCalls != 0 {
					t.Errorf("unexpected answer=%v esc=%d", kb.answerCalls, kb.escCalls)
				}
			},
		},
		{
			name:      "exit trust routes SendEsc",
			trust:     true,
			optionID:  "exit",
			wantAudit: "denied",
			wantKey: func(t *testing.T, kb *fakeKeystroker) {
				if kb.escCalls != 1 {
					t.Errorf("escCalls = %d, want 1", kb.escCalls)
				}
				if len(kb.answerCalls) != 0 || kb.trustCalls != 0 {
					t.Errorf("unexpected answer=%v trust=%d", kb.answerCalls, kb.trustCalls)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := modalbridge.New()
			var modalID, wantClass string
			if tt.trust {
				modalID = recordTrustModal(t, reg, secretModalBody)
				wantClass = "trust"
			} else {
				modalID = recordPermissionModal(t, reg, secretModalBody)
				wantClass = "permission"
			}
			kb := &fakeKeystroker{}
			logger, logBuf := auditLogger()
			dev := eligibleDevice(t)

			r := newModalResolverV2(reg, kb, logger)
			d, ok := r.ResolveAnswer(modalID, tt.optionID, "tok-1", dev)

			if !ok {
				t.Fatal("ResolveAnswer ok = false, want true")
			}
			// The WIRE dismissal Outcome is the option_id, not the audit class.
			if d.Outcome != tt.optionID || d.Source != string(audit.SourceRemote) {
				t.Errorf("dismissal = %+v, want {%s remote}", d, tt.optionID)
			}
			tt.wantKey(t, kb)

			// The modal is consumed: a follow-up Lookup misses.
			if _, stillThere := reg.Lookup(modalID); stillThere {
				t.Error("modal still in registry after answer; Resolve must consume it")
			}

			recs := auditRecords(t, logBuf)
			if len(recs) != 1 {
				t.Fatalf("audit records = %d, want 1", len(recs))
			}
			rec := recs[0]
			wantFields := map[string]string{
				"outcome":      tt.wantAudit,
				"source":       "remote",
				"modal_id":     modalID,
				"modal_class":  wantClass,
				"device_hash":  dev.TokenHash,
				"device_label": dev.Name,
			}
			for k, want := range wantFields {
				if got, _ := rec[k].(string); got != want {
					t.Errorf("audit field %q = %q, want %q", k, got, want)
				}
			}
			// SECURITY: no modal body in any log field.
			if strings.Contains(logBuf.String(), secretModalBody) {
				t.Error("modal body leaked into a log field")
			}
		})
	}
}

// TestModalResolverV2_Answer_UngatedDevice proves an ungated device's answer is
// denied fail-closed: no keystroke, the modal is NOT consumed (left outstanding
// for a legit local answer / #725 timeout), audit denied_unauthorized, (zero,
// false) return. AC-2.
func TestModalResolverV2_Answer_UngatedDevice(t *testing.T) {
	t.Parallel()

	reg := modalbridge.New()
	modalID := recordPermissionModal(t, reg, secretModalBody)
	kb := &fakeKeystroker{}
	logger, logBuf := auditLogger()
	dev := testDevice(t) // opt-in bit OFF -> ineligible

	r := newModalResolverV2(reg, kb, logger)
	d, ok := r.ResolveAnswer(modalID, "allow_once", "tok-1", dev)

	if ok {
		t.Error("ResolveAnswer ok = true for an ungated device, want false")
	}
	if d != (relay.ModalDismissal{}) {
		t.Errorf("dismissal = %+v, want zero", d)
	}
	if !kb.routedNothing() {
		t.Errorf("ungated device routed a keystroke: answer=%v esc=%d trust=%d", kb.answerCalls, kb.escCalls, kb.trustCalls)
	}
	// NOT consumed: still outstanding.
	if _, ok := reg.Lookup(modalID); !ok {
		t.Error("ungated answer consumed the modal; it must stay outstanding")
	}
	recs := auditRecords(t, logBuf)
	if len(recs) != 1 {
		t.Fatalf("audit records = %d, want 1", len(recs))
	}
	if got, _ := recs[0]["outcome"].(string); got != "denied_unauthorized" {
		t.Errorf("audit outcome = %q, want denied_unauthorized", got)
	}
}

// TestModalResolverV2_Answer_NilDevice proves the gate is structurally
// fail-closed for a nil (unauthenticated) device: denied_unauthorized with empty
// identity fields, no keystroke, no consume. AC-2.
func TestModalResolverV2_Answer_NilDevice(t *testing.T) {
	t.Parallel()

	reg := modalbridge.New()
	modalID := recordPermissionModal(t, reg, secretModalBody)
	kb := &fakeKeystroker{}
	logger, logBuf := auditLogger()

	r := newModalResolverV2(reg, kb, logger)
	d, ok := r.ResolveAnswer(modalID, "allow_once", "tok-1", nil)

	if ok {
		t.Error("ResolveAnswer ok = true for a nil device, want false")
	}
	if d != (relay.ModalDismissal{}) {
		t.Errorf("dismissal = %+v, want zero", d)
	}
	if !kb.routedNothing() {
		t.Error("nil device routed a keystroke")
	}
	if _, ok := reg.Lookup(modalID); !ok {
		t.Error("nil-device answer consumed the modal; it must stay outstanding")
	}
	recs := auditRecords(t, logBuf)
	if len(recs) != 1 {
		t.Fatalf("audit records = %d, want 1", len(recs))
	}
	rec := recs[0]
	if got, _ := rec["outcome"].(string); got != "denied_unauthorized" {
		t.Errorf("audit outcome = %q, want denied_unauthorized", got)
	}
	if got, _ := rec["device_hash"].(string); got != "" {
		t.Errorf("audit device_hash = %q, want empty for a nil device", got)
	}
	if got, _ := rec["device_label"].(string); got != "" {
		t.Errorf("audit device_label = %q, want empty for a nil device", got)
	}
}

// TestModalResolverV2_Answer_StaleModalID proves an unknown/stale id is a no-op:
// no keystroke, no audit (no security decision was made), (zero,false). AC-2.
func TestModalResolverV2_Answer_StaleModalID(t *testing.T) {
	t.Parallel()

	reg := modalbridge.New()
	kb := &fakeKeystroker{}
	logger, logBuf := auditLogger()

	r := newModalResolverV2(reg, kb, logger)
	d, ok := r.ResolveAnswer("nonexistent-modal", "allow_once", "tok-1", eligibleDevice(t))

	if ok {
		t.Error("ResolveAnswer ok = true for a stale id, want false")
	}
	if d != (relay.ModalDismissal{}) {
		t.Errorf("dismissal = %+v, want zero", d)
	}
	if !kb.routedNothing() {
		t.Error("stale id routed a keystroke")
	}
	if recs := auditRecords(t, logBuf); len(recs) != 0 {
		t.Errorf("audit records = %d, want 0 (no decision for an unknown modal)", len(recs))
	}
}

// TestModalResolverV2_Answer_ReplayIdempotent proves the modal_id one-shot
// consume is the single idempotency gate: a replayed/reordered answer for an
// already-resolved modal collapses to a row-1 no-op — no second keystroke, no
// second audit, no second dismissal. AC-2.
func TestModalResolverV2_Answer_ReplayIdempotent(t *testing.T) {
	t.Parallel()

	reg := modalbridge.New()
	modalID := recordPermissionModal(t, reg, secretModalBody)
	kb := &fakeKeystroker{}
	logger, logBuf := auditLogger()
	dev := eligibleDevice(t)

	r := newModalResolverV2(reg, kb, logger)
	if _, ok := r.ResolveAnswer(modalID, "allow_once", "tok-1", dev); !ok {
		t.Fatal("first ResolveAnswer ok = false, want true")
	}
	// Replay the same answer (even the same answer_token): the modal_id is gone.
	d, ok := r.ResolveAnswer(modalID, "allow_once", "tok-1", dev)
	if ok {
		t.Error("second ResolveAnswer ok = true, want false (already consumed)")
	}
	if d != (relay.ModalDismissal{}) {
		t.Errorf("second dismissal = %+v, want zero", d)
	}
	if len(kb.answerCalls) != 1 {
		t.Errorf("answerCalls = %v, want exactly 1 (no second keystroke)", kb.answerCalls)
	}
	if recs := auditRecords(t, logBuf); len(recs) != 1 {
		t.Errorf("audit records = %d, want 1 (no second record)", len(recs))
	}
}

// TestModalResolverV2_Answer_ForgedOption proves an option_id that is not a member
// of THIS modal's options (unknown id, or a wrong-class option) is rejected with
// no keystroke, no consume, no audit, and a Warn. AC-2 defense.
func TestModalResolverV2_Answer_ForgedOption(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		trust    bool
		optionID string
	}{
		{"unknown option id on permission", false, "bogus"},
		{"trust option on a permission modal", false, "proceed"},
		{"permission option on a trust modal", true, "allow_once"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := modalbridge.New()
			var modalID string
			if tt.trust {
				modalID = recordTrustModal(t, reg, secretModalBody)
			} else {
				modalID = recordPermissionModal(t, reg, secretModalBody)
			}
			kb := &fakeKeystroker{}
			logger, logBuf := auditLogger()

			r := newModalResolverV2(reg, kb, logger)
			d, ok := r.ResolveAnswer(modalID, tt.optionID, "tok-1", eligibleDevice(t))

			if ok {
				t.Error("ResolveAnswer ok = true for a non-member option, want false")
			}
			if d != (relay.ModalDismissal{}) {
				t.Errorf("dismissal = %+v, want zero", d)
			}
			if !kb.routedNothing() {
				t.Error("a forged/wrong-class option routed a keystroke")
			}
			// No consume: the modal stays outstanding.
			if _, ok := reg.Lookup(modalID); !ok {
				t.Error("a forged option consumed the modal; it must stay outstanding")
			}
			// No audit (malformed client frame, no security decision) but a Warn.
			if recs := auditRecords(t, logBuf); len(recs) != 0 {
				t.Errorf("audit records = %d, want 0 for a forged option", len(recs))
			}
			if !strings.Contains(logBuf.String(), "modal_answer.invalid_option") {
				t.Errorf("expected an invalid_option warn; got:\n%s", logBuf.String())
			}
		})
	}
}

// TestModalResolverV2_Answer_KeystrokeError proves a keystroke error on the
// committed path is tolerated, exactly like cancel: the modal is still consumed,
// the audit record + {option_id, remote, true} dismissal still happen, and a Warn
// carries the non-secret supervisor sentinel.
func TestModalResolverV2_Answer_KeystrokeError(t *testing.T) {
	t.Parallel()

	reg := modalbridge.New()
	modalID := recordPermissionModal(t, reg, secretModalBody)
	kb := &fakeKeystroker{err: supervisor.ErrNoLiveSession}
	logger, logBuf := auditLogger()
	dev := eligibleDevice(t)

	r := newModalResolverV2(reg, kb, logger)
	d, ok := r.ResolveAnswer(modalID, "allow_once", "tok-1", dev)

	if !ok {
		t.Fatal("ResolveAnswer ok = false on keystroke error, want true (modal already consumed)")
	}
	if d.Outcome != "allow_once" || d.Source != string(audit.SourceRemote) {
		t.Errorf("dismissal = %+v, want {allow_once remote}", d)
	}
	if _, stillThere := reg.Lookup(modalID); stillThere {
		t.Error("modal still outstanding after keystroke-error answer; it must be consumed")
	}
	recs := auditRecords(t, logBuf)
	if len(recs) != 1 {
		t.Fatalf("audit records = %d, want 1 despite keystroke error", len(recs))
	}
	if got, _ := recs[0]["outcome"].(string); got != "allowed" {
		t.Errorf("audit outcome = %q, want allowed", got)
	}
	out := logBuf.String()
	if !strings.Contains(out, "modal_answer.keystroke_err") {
		t.Errorf("expected a keystroke_err warn; got:\n%s", out)
	}
	if strings.Contains(out, secretModalBody) {
		t.Error("modal body leaked into a log field on the keystroke-error path")
	}
}

// TestModalResolverV2_Answer_NoBodyLeak proves no modal body/title reaches any
// log field across the allow, deny, and denied_unauthorized paths. SECURITY.
func TestModalResolverV2_Answer_NoBodyLeak(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		dev      func(t *testing.T) *devices.Device
		optionID string
	}{
		{"allowed", eligibleDevice, "allow_once"},
		{"denied", eligibleDevice, "reject_once"},
		{"denied_unauthorized", testDevice, "allow_once"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := modalbridge.New()
			modalID := recordPermissionModal(t, reg, secretModalBody)
			kb := &fakeKeystroker{}
			logger, logBuf := auditLogger()

			r := newModalResolverV2(reg, kb, logger)
			r.ResolveAnswer(modalID, tt.optionID, "tok-1", tt.dev(t))

			out := logBuf.String()
			if strings.Contains(out, secretModalBody) {
				t.Error("modal body leaked into a log field")
			}
			if strings.Contains(out, "Permission required") {
				t.Error("modal title leaked into a log field")
			}
		})
	}
}
