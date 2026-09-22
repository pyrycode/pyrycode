package main

import (
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/config"
)

// TestCheckDebugCapture covers the #1514 startup guard. The .cast recorder that
// debug_capture switched on was deleted with the terminal runner in #1348, so an
// operator who opts in must be told at startup rather than handed an empty debug
// bundle later. OFF and absent must stay silent: they are every existing config.
func TestCheckDebugCapture(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      config.Config
		wantErr  bool
		wantSubs []string
	}{
		{name: "absent key (zero value) starts", cfg: config.Config{}},
		{name: "debug_capture false starts", cfg: config.Config{DebugCapture: false, RelayURL: "wss://relay.example/"}},
		{
			name:    "debug_capture true is rejected",
			cfg:     config.Config{DebugCapture: true},
			wantErr: true,
			// The operator has a config file to edit: the message must name the
			// key, the ticket that removed the recorder, and the value to use.
			wantSubs: []string{"debug_capture", "#1348", "false"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkDebugCapture(tt.cfg)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("checkDebugCapture(%+v) err = %v, want nil", tt.cfg, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkDebugCapture(%+v) err = nil, want an error — the recorder no longer exists", tt.cfg)
			}
			for _, sub := range tt.wantSubs {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("err = %q, want it to contain %q", err, sub)
				}
			}
		})
	}
}
