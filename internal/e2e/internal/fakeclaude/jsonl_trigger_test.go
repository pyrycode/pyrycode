package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEmitStructuredJSONLIfTriggered pins the consumer-side empty-read gate that
// closes the #958 structured-stream e2e flake. Every producer drop is an
// os.WriteFile = open(O_CREATE|O_TRUNC) followed by a single write; between the
// truncate and the write the trigger exists but is empty. The ~50 ms poll-loop
// consumer must NOT consume-and-remove that empty file — doing so unlinks the
// trigger before the producer's content is ever visible, destroying the fixture
// (the load-bearing sync.Once dropFull line in the structured test) and leaving
// A with a partial `[tool_use turn_end]`-missing set. Intentionally UNTAGGED (no
// //go:build e2e), mirroring esc_detect_test.go, so the standard `go test` gate
// exercises the contract in-process — the e2e files never compile upstream of
// code-review.
func TestEmitStructuredJSONLIfTriggered(t *testing.T) {
	t.Parallel()

	// openTempSession returns an empty session file (O_APPEND|O_CREATE, the
	// openSession shape minus the "{}\n" seed) plus its path, so a caller can
	// os.Stat the path to detect any growth from emitStructuredJSONLIfTriggered.
	openTempSession := func(t *testing.T) (*os.File, string) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "session.jsonl")
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatalf("open temp session: %v", err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f, path
	}

	t.Run("empty trigger is skipped, not removed", func(t *testing.T) {
		t.Parallel()
		f, sessionPath := openTempSession(t)
		trigger := filepath.Join(t.TempDir(), "trigger")
		if err := os.WriteFile(trigger, nil, 0o600); err != nil {
			t.Fatalf("write empty trigger: %v", err)
		}

		emitStructuredJSONLIfTriggered(f, trigger)

		// The trigger must survive so the next poll consumes the producer's
		// content once its write lands — this is the exact bug (pre-fix the
		// trigger is os.Remove'd here).
		if _, err := os.Stat(trigger); err != nil {
			t.Errorf("empty trigger was removed (fixture destroyed): %v", err)
		}
		// Nothing was appended: an empty read is never a legitimate payload.
		if info, err := os.Stat(sessionPath); err != nil {
			t.Fatalf("stat session: %v", err)
		} else if info.Size() != 0 {
			t.Errorf("session grew on empty read: size %d, want 0", info.Size())
		}
	})

	t.Run("non-empty trigger is consumed and removed", func(t *testing.T) {
		t.Parallel()
		f, sessionPath := openTempSession(t)
		trigger := filepath.Join(t.TempDir(), "trigger")
		payload := []byte(`{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"hi"}]}}` + "\n")
		if err := os.WriteFile(trigger, payload, 0o600); err != nil {
			t.Fatalf("write trigger: %v", err)
		}

		emitStructuredJSONLIfTriggered(f, trigger)

		if _, err := os.Stat(trigger); !os.IsNotExist(err) {
			t.Errorf("non-empty trigger was not removed after consume: err=%v", err)
		}
		got, err := os.ReadFile(sessionPath)
		if err != nil {
			t.Fatalf("read session: %v", err)
		}
		if string(got) != string(payload) {
			t.Errorf("session content = %q, want %q", got, payload)
		}
	})

	t.Run("missing trigger is a no-op", func(t *testing.T) {
		t.Parallel()
		f, sessionPath := openTempSession(t)
		trigger := filepath.Join(t.TempDir(), "does-not-exist")

		emitStructuredJSONLIfTriggered(f, trigger) // must not panic

		if _, err := os.Stat(trigger); !os.IsNotExist(err) {
			t.Errorf("missing trigger materialised: err=%v", err)
		}
		if info, err := os.Stat(sessionPath); err != nil {
			t.Fatalf("stat session: %v", err)
		} else if info.Size() != 0 {
			t.Errorf("session grew on missing trigger: size %d, want 0", info.Size())
		}
	})
}
