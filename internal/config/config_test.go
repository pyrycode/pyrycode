package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	t.Parallel()
	got := DefaultConfig()
	want := Config{RelayURL: "wss://relay.pyrycode.dev", DebugCapture: false, StdioPermissionPrompt: false}
	if got != want {
		t.Errorf("DefaultConfig() = %+v, want %+v", got, want)
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()

	ptr := func(s string) *string { return &s }

	cases := []struct {
		name      string
		fileBody  *string
		want      Config
		wantErr   bool
		errSubstr string
	}{
		{
			name:     "missing file returns defaults",
			fileBody: nil,
			want:     Config{RelayURL: "wss://relay.pyrycode.dev"},
		},
		{
			name:     "valid full file overrides default",
			fileBody: ptr(`{"relay_url": "wss://my-relay.example/"}`),
			want:     Config{RelayURL: "wss://my-relay.example/"},
		},
		{
			name:     "partial file with missing fields keeps defaults",
			fileBody: ptr(`{}`),
			want:     Config{RelayURL: "wss://relay.pyrycode.dev"},
		},
		{
			name:     "absent stdio permission prompt is off",
			fileBody: ptr(`{"relay_url":"wss://my-relay.example/"}`),
			want:     Config{RelayURL: "wss://my-relay.example/", StdioPermissionPrompt: false},
		},
		{
			name:     "false stdio permission prompt stays off",
			fileBody: ptr(`{"stdio_permission_prompt":false}`),
			want:     Config{RelayURL: "wss://relay.pyrycode.dev", StdioPermissionPrompt: false},
		},
		{
			name:     "true stdio permission prompt is on",
			fileBody: ptr(`{"stdio_permission_prompt":true}`),
			want:     Config{RelayURL: "wss://relay.pyrycode.dev", StdioPermissionPrompt: true},
		},
		{
			// AC1: an absent debug_capture field decodes to OFF (Go zero value),
			// with relay_url still defaulted — the unset case, asserted explicitly.
			name:     "absent debug_capture is OFF",
			fileBody: ptr(`{"relay_url": "wss://my-relay.example/"}`),
			want:     Config{RelayURL: "wss://my-relay.example/", DebugCapture: false},
		},
		{
			// AC2: debug_capture:true in the file round-trips to ON, proving it
			// persists across a daemon restart (config is reloaded at start).
			name:     "debug_capture true persists",
			fileBody: ptr(`{"debug_capture": true}`),
			want:     Config{RelayURL: "wss://relay.pyrycode.dev", DebugCapture: true},
		},
		{
			name:     "relay_url and debug_capture coexist",
			fileBody: ptr(`{"relay_url": "wss://my-relay.example/", "debug_capture": true}`),
			want:     Config{RelayURL: "wss://my-relay.example/", DebugCapture: true},
		},
		{
			// #1081 AC1: an absent interactive_runner field decodes to "" (Go zero
			// value) — the PTY default — with relay_url still defaulted.
			name:     "absent interactive_runner is empty",
			fileBody: ptr(`{"relay_url": "wss://my-relay.example/"}`),
			want:     Config{RelayURL: "wss://my-relay.example/", InteractiveRunner: ""},
		},
		{
			name:     "interactive_runner pty decodes verbatim",
			fileBody: ptr(`{"interactive_runner": "pty"}`),
			want:     Config{RelayURL: "wss://relay.pyrycode.dev", InteractiveRunner: "pty"},
		},
		{
			name:     "interactive_runner stream-json decodes verbatim",
			fileBody: ptr(`{"interactive_runner": "stream-json"}`),
			want:     Config{RelayURL: "wss://relay.pyrycode.dev", InteractiveRunner: "stream-json"},
		},
		{
			// Load is parse-only: an unrecognised value decodes verbatim without
			// error. Enum validation is the composition-root selector's job (#1081),
			// mirroring how Load leaves DebugCapture unvalidated.
			name:     "unknown interactive_runner decodes verbatim (Load does not validate)",
			fileBody: ptr(`{"interactive_runner": "garbage"}`),
			want:     Config{RelayURL: "wss://relay.pyrycode.dev", InteractiveRunner: "garbage"},
		},
		{
			name:      "malformed JSON returns wrapped error",
			fileBody:  ptr(`{not json`),
			want:      Config{},
			wantErr:   true,
			errSubstr: "config: parse",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.json")
			if tc.fileBody != nil {
				if err := os.WriteFile(path, []byte(*tc.fileBody), 0o600); err != nil {
					t.Fatalf("write fixture: %v", err)
				}
			}
			got, err := Load(path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Load(%s) err = nil, want error containing %q", path, tc.errSubstr)
				}
				if !strings.Contains(err.Error(), tc.errSubstr) {
					t.Errorf("Load(%s) err = %q, want substring %q", path, err.Error(), tc.errSubstr)
				}
			} else if err != nil {
				t.Fatalf("Load(%s) unexpected err: %v", path, err)
			}
			if got != tc.want {
				t.Errorf("Load(%s) = %+v, want %+v", path, got, tc.want)
			}
		})
	}
}
