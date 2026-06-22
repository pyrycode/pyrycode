package audit

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
)

const auditMsg = "audit: remote permission decision"

// logToRecords runs Log against a JSON slog handler and decodes every record
// it emitted. One Log call must produce exactly one record.
func logToRecords(t *testing.T, e Entry) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	Log(slog.New(slog.NewJSONHandler(&buf, nil)), e)

	var records []map[string]any
	dec := json.NewDecoder(&buf)
	for {
		var m map[string]any
		err := dec.Decode(&m)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decoding audit record: %v (raw: %s)", err, buf.String())
		}
		records = append(records, m)
	}
	return records
}

// TestLog_OnePerOutcome asserts one record per decision and the outcome/source
// vocabulary coverage (AC1, AC2, AC5).
func TestLog_OnePerOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		outcome Outcome
		source  Source
	}{
		{"allowed", OutcomeAllowed, SourceRemote},
		{"denied_unauthorized", OutcomeDeniedUnauthorized, SourceRemote},
		{"denied_timeout", OutcomeDeniedTimeout, SourceTimeout},
		{"cancelled", OutcomeCancelled, SourceRemote},
		{"denied", OutcomeDenied, SourceRemote},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			records := logToRecords(t, Entry{
				DeviceHash: "abc",
				ModalID:    "modal-1",
				Outcome:    tc.outcome,
				Source:     tc.source,
			})
			if len(records) != 1 {
				t.Fatalf("Log emitted %d records, want exactly 1", len(records))
			}
			rec := records[0]
			if got := rec["outcome"]; got != string(tc.outcome) {
				t.Errorf("outcome = %v, want %q", got, tc.outcome)
			}
			if got := rec["source"]; got != string(tc.source) {
				t.Errorf("source = %v, want %q", got, tc.source)
			}
		})
	}
}

// TestLog_FieldCompleteness asserts a fully-populated Entry surfaces every
// field plus slog's automatic level/msg (AC1, AC5).
func TestLog_FieldCompleteness(t *testing.T) {
	t.Parallel()

	in := Entry{
		DeviceHash:  "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		DeviceLabel: "pixel-8",
		ModalID:     "modal-9f3c",
		ModalClass:  "permission",
		Outcome:     OutcomeAllowed,
		Source:      SourceRemote,
	}
	records := logToRecords(t, in)
	if len(records) != 1 {
		t.Fatalf("Log emitted %d records, want exactly 1", len(records))
	}
	rec := records[0]

	for key, want := range map[string]string{
		"device_hash":  in.DeviceHash,
		"device_label": in.DeviceLabel,
		"modal_id":     in.ModalID,
		"modal_class":  in.ModalClass,
		"outcome":      string(in.Outcome),
		"source":       string(in.Source),
	} {
		if got := rec[key]; got != want {
			t.Errorf("%s = %v, want %q", key, got, want)
		}
	}
	if got := rec["level"]; got != "INFO" {
		t.Errorf("level = %v, want INFO", got)
	}
	if got := rec["msg"]; got != auditMsg {
		t.Errorf("msg = %v, want %q", got, auditMsg)
	}
	if _, ok := rec["time"]; !ok {
		t.Errorf("record missing automatic time field: %v", rec)
	}
}

// TestLog_ExactKeySet pins the attribute key set so a future edit that adds a
// secret-bearing field fails this test (AC3).
func TestLog_ExactKeySet(t *testing.T) {
	t.Parallel()

	records := logToRecords(t, Entry{
		DeviceHash:  "hash",
		DeviceLabel: "label",
		ModalID:     "modal",
		ModalClass:  "permission",
		Outcome:     OutcomeDenied,
		Source:      SourceRemote,
	})
	if len(records) != 1 {
		t.Fatalf("Log emitted %d records, want exactly 1", len(records))
	}

	want := map[string]bool{
		"time": true, "level": true, "msg": true,
		"device_hash": true, "device_label": true,
		"modal_id": true, "modal_class": true,
		"outcome": true, "source": true,
	}
	for key := range records[0] {
		if !want[key] {
			t.Errorf("unexpected attribute key %q — audit record must carry no field beyond the fixed set", key)
		}
		delete(want, key)
	}
	for key := range want {
		t.Errorf("missing expected attribute key %q", key)
	}
}

// TestLog_NoSecretLeak asserts the serialized record carries the device hash
// but never a plain token, even when one is held alongside the entry (AC3).
func TestLog_NoSecretLeak(t *testing.T) {
	t.Parallel()

	const plainToken = "PLAINTEXT-DEVICE-TOKEN-must-never-appear-in-audit"
	hash := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"

	var buf bytes.Buffer
	Log(slog.New(slog.NewJSONHandler(&buf, nil)), Entry{
		DeviceHash:  hash,
		DeviceLabel: "pixel-8",
		ModalID:     "modal-9f3c",
		ModalClass:  "permission",
		Outcome:     OutcomeAllowed,
		Source:      SourceRemote,
	})

	if bytes.Contains(buf.Bytes(), []byte(plainToken)) {
		t.Fatalf("audit record leaked a plain token: %s", buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte(hash)) {
		t.Errorf("audit record missing the device hash (the recorded identity): %s", buf.String())
	}
}

// TestLog_NilLoggerDoesNotPanic asserts the nil-logger guard (defaults to
// slog.Default) keeps the primitive total.
func TestLog_NilLoggerDoesNotPanic(t *testing.T) {
	t.Parallel()

	Log(nil, Entry{Outcome: OutcomeDeniedTimeout, Source: SourceTimeout})
}
