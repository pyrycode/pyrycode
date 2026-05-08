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
	want := Config{RelayURL: "wss://relay.pyrycode.dev"}
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
