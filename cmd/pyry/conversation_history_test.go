package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
)

// historyTS is a fixed append timestamp for the seam's own tests. UTC, matching
// what both producers hoist.
var historyTS = time.Date(2026, 9, 5, 8, 30, 0, 0, time.UTC)

// bufLogger returns a logger writing into buf at the default level, so a Warn
// the seam emits is captured while a neighbouring Debug is not.
func bufLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, nil))
}

// TestAppendConversationHistory_NilStoreIsSilent: a daemon with no durable log —
// and every emitter test that constructs an emitter without one — must emit
// exactly as before. The nil store neither panics nor logs.
func TestAppendConversationHistory_NilStoreIsSilent(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer

	appendConversationHistory(nil, bufLogger(&buf), "test.event", testConvID,
		"assistant_delta", json.RawMessage(`{"text":"hi"}`), historyTS)

	if buf.Len() != 0 {
		t.Fatalf("nil store logged %q; want silence", buf.String())
	}
}

// TestHistoryAppendFailure_Discriminants: the failure reason is the error's
// IDENTITY, resolved through the wrapping both sentinels arrive under.
func TestHistoryAppendFailure_Discriminants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"invalid id, wrapped", fmt.Errorf("%w: conversation %q", history.ErrInvalidID, "conv-x"), "invalid_id"},
		{"invalid payload, wrapped", fmt.Errorf("%w: not valid JSON", history.ErrInvalidPayload), "invalid_payload"},
		{"filesystem error", fmt.Errorf("history: open segment %q: %w", "/tmp/x", os.ErrPermission), "write"},
		{"unclassified error", errors.New("something else"), "write"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := historyAppendFailure(tt.err); got != tt.want {
				t.Fatalf("historyAppendFailure(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

// TestAppendConversationHistory_FailureLogsNoContent: the never-log-content
// posture. A refused append reports the conversation id and a content-free
// reason, and neither the payload bytes nor the store's filesystem path (which
// internal/history's own errors DO format) reach the log line.
func TestAppendConversationHistory_FailureLogsNoContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := history.New(dir)
	var buf bytes.Buffer

	// A non-canonical id is the cheap induction: Append refuses it with
	// ErrInvalidID before touching the filesystem at all.
	const badConv = "conv-x"
	const marker = "PAYLOAD-CONTENT-MUST-NOT-BE-LOGGED"
	appendConversationHistory(store, bufLogger(&buf), "test.event", badConv,
		"assistant_delta", json.RawMessage(`{"text":"`+marker+`"}`), historyTS)

	got := buf.String()
	if !strings.Contains(got, "reason=invalid_id") {
		t.Fatalf("log %q does not carry reason=invalid_id", got)
	}
	if !strings.Contains(got, badConv) {
		t.Fatalf("log %q does not carry the conversation id", got)
	}
	if strings.Contains(got, marker) {
		t.Fatalf("log leaked payload content: %q", got)
	}
	if strings.Contains(got, dir) {
		t.Fatalf("log leaked the store's filesystem path: %q", got)
	}
}
