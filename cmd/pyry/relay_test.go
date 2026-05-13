package main

import (
	"testing"

	"github.com/pyrycode/pyrycode/internal/config"
)

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
