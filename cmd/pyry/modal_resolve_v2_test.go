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

// fakeKeystroker records SendEsc calls and returns an injectable error. The
// resolver is called single-threaded in these tests, so no mutex is needed.
type fakeKeystroker struct {
	escCalls int
	err      error
}

func (f *fakeKeystroker) SendEsc() error {
	f.escCalls++
	return f.err
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

func testDevice(t *testing.T) *devices.Device {
	t.Helper()
	return &devices.Device{TokenHash: devices.HashToken("plain-device-token"), Name: "test-phone"}
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

// TestModalResolverV2_Answer_NoOp proves the answer arm is a deferred no-op
// (AC-3, AC-5): no keystroke, no audit, the modal is NOT mutated (still
// resolvable afterward), and a (zero,false) return.
func TestModalResolverV2_Answer_NoOp(t *testing.T) {
	t.Parallel()

	reg := modalbridge.New()
	modalID := recordPermissionModal(t, reg, secretModalBody)
	kb := &fakeKeystroker{}
	logger, logBuf := auditLogger()
	dev := testDevice(t)

	r := newModalResolverV2(reg, kb, logger)
	d, ok := r.ResolveAnswer(modalID, "allow_once", "tok-1", dev)

	if ok {
		t.Error("ResolveAnswer ok = true, want false (deferred no-op)")
	}
	if d != (relay.ModalDismissal{}) {
		t.Errorf("dismissal = %+v, want zero", d)
	}
	if kb.escCalls != 0 {
		t.Errorf("SendEsc calls = %d, want 0 (answer routes no keystroke)", kb.escCalls)
	}
	if recs := auditRecords(t, logBuf); len(recs) != 0 {
		t.Errorf("audit records = %d, want 0 (answer writes no audit)", len(recs))
	}
	// The modal is NOT mutated: still in the registry.
	if _, ok := reg.Lookup(modalID); !ok {
		t.Error("modal missing from registry after answer no-op; it must be untouched")
	}
}
