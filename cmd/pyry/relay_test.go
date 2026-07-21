package main

import (
	"testing"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// TestScreenSnapshotterOrNil pins the typed-nil-in-interface trap (#1101): a nil
// *supervisor.Supervisor (the stream-json bootstrap path, where
// Session.Supervisor() returns nil per #1077) must become a GENUINE nil
// relay.ScreenSnapshotter so handleRequestSnapshot takes its post-gate offline
// arm. Assigning the pointer straight to the interface field would leave a
// non-nil interface holding a nil pointer — got == nil would be false — so the
// handler would skip that arm and call ScreenSnapshot on a nil receiver → panic.
func TestScreenSnapshotterOrNil(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		sup     *supervisor.Supervisor
		wantNil bool
	}{
		{
			name:    "nil supervisor yields genuine nil interface",
			sup:     nil,
			wantNil: true,
		},
		{
			// A zero-value &supervisor.Supervisor{} is a legal cross-package
			// non-nil pointer; the helper never calls ScreenSnapshot, so an
			// empty composite literal suffices to prove the non-nil arm.
			name:    "non-nil supervisor is passed through",
			sup:     &supervisor.Supervisor{},
			wantNil: false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := screenSnapshotterOrNil(tc.sup)
			if (got == nil) != tc.wantNil {
				t.Errorf("screenSnapshotterOrNil(%v): got == nil is %v, want %v",
					tc.sup, got == nil, tc.wantNil)
			}
		})
	}
}

func TestResolveRelayURL_Precedence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		flag  string
		env   string
		cfg   config.Config
		want  string
	}{
		{
			name: "flag wins over env and cfg",
			flag: "wss://flag/", env: "wss://env/", cfg: config.Config{RelayURL: "wss://cfg/"},
			want: "wss://flag/",
		},
		{
			name: "env wins over cfg when flag empty",
			flag: "", env: "wss://env/", cfg: config.Config{RelayURL: "wss://cfg/"},
			want: "wss://env/",
		},
		{
			name: "cfg used when flag and env empty",
			flag: "", env: "", cfg: config.Config{RelayURL: "wss://cfg/"},
			want: "wss://cfg/",
		},
		{
			name: "empty when all three empty",
			flag: "", env: "", cfg: config.Config{RelayURL: ""},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resolveRelayURL(tc.flag, tc.env, tc.cfg)
			if got != tc.want {
				t.Errorf("resolveRelayURL(%q,%q,%+v) = %q, want %q",
					tc.flag, tc.env, tc.cfg, got, tc.want)
			}
		})
	}
}
