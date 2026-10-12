package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

// Predictable fixtures are synthetic, never provisioning credentials.
const admissionTestToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func admissionConfigFile(t *testing.T, data string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertConfigErrorSafe(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, secret := range []string{admissionTestToken, "private-identity", "private-parser-excerpt"} {
		if strings.Contains(fmt.Sprintf("%+v %#v", err, err), secret) {
			t.Error("config error exposed private input")
		}
	}
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	if errors.As(err, &syntax) || errors.As(err, &typ) {
		t.Error("config error retained unsafe decoder cause")
	}
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		for _, child := range e.Unwrap() {
			assertConfigErrorSafe(t, child)
		}
	case interface{ Unwrap() error }:
		assertConfigErrorSafe(t, e.Unwrap())
	}
}

func TestRelayAdmissionConfig(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, value string
		valid       bool
	}{
		{"empty object", `{}`, true},
		{"exact identities", `{" Identity ":"` + admissionTestToken + `","other":"` + admissionTestToken + `"}`, true},
		{"case distinct identities", `{"Selected":"` + admissionTestToken + `","selected":"` + strings.Repeat("b", 64) + `"}`, true},
		{"null map", `null`, false}, {"array map", `[]`, false}, {"string map", `"private-parser-excerpt"`, false},
		{"null entry", `{"private-identity":null}`, false}, {"numeric entry", `{"private-identity":123}`, false},
		{"empty entry", `{"private-identity":""}`, false},
		{"unselected invalid", `{"selected":"` + admissionTestToken + `","private-identity":"bad"}`, false},
		{"short", `{"id":"` + admissionTestToken[:63] + `"}`, false},
		{"long", `{"id":"` + admissionTestToken + `0"}`, false},
		{"uppercase", `{"id":"` + strings.ToUpper(admissionTestToken) + `"}`, false},
		{"nonhex", `{"id":"g` + admissionTestToken[1:] + `"}`, false},
		{"space", `{"id":" ` + admissionTestToken + `"}`, false},
		{"comma", `{"id":"` + admissionTestToken + `,` + admissionTestToken + `"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := admissionConfigFile(t, `{"relay_admission_tokens":`+tc.value+`}`)
			cfg, err := Load(path)
			if !tc.valid {
				if err == nil {
					t.Fatal("invalid admission config accepted")
				}
				assertConfigErrorSafe(t, err)
				if !reflect.DeepEqual(cfg, Config{}) {
					t.Error("failure returned partial config")
				}
				if err := UpdateMemory(path, MemorySettings{}); err == nil {
					t.Error("invalid admission update accepted")
				} else {
					assertConfigErrorSafe(t, err)
				}
				raw, readErr := os.ReadFile(path)
				if readErr != nil || string(raw) != `{"relay_admission_tokens":`+tc.value+`}` {
					t.Error("rejected update changed config bytes")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var want map[string]string
			if err := json.Unmarshal([]byte(tc.value), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg.RelayAdmissionTokens, want) {
				t.Error("admission map lost or normalized")
			}
			if cfg.RelayURL != DefaultConfig().RelayURL {
				t.Error("additive defaults lost")
			}
		})
	}
	for _, data := range []string{
		`{"relay_admission_tokens":{"id":"` + admissionTestToken + `"},"relay_url":123}`,
		`{"relay_admission_tokens":{"id":"` + admissionTestToken + `"},"memory":{"vault":"private-parser-excerpt"}}`,
		`{"relay_admission_tokens":{"id":"` + admissionTestToken + `"},"private-parser-excerpt": @}`,
	} {
		path := admissionConfigFile(t, data)
		_, err := Load(path)
		if err == nil {
			t.Fatal("malformed config accepted")
		}
		assertConfigErrorSafe(t, err)
		// Unknown scalar settings stay raw in UpdateMemory; nested object errors are rejected.
		err = UpdateMemory(path, MemorySettings{})
		assertConfigErrorSafe(t, err)
	}
}

func TestRelayAdmissionFieldBoundaries(t *testing.T) {
	t.Parallel()
	valid := `{"selected":"` + admissionTestToken + `"}`
	for _, tc := range []struct {
		name, data string
		public     bool
	}{
		{"uppercase storage bypass", `{"RELAY_ADMISSION_TOKENS":` + valid + `}`, true},
		{"mixed case field", `{"Relay_Admission_Tokens":` + valid + `}`, false},
		{"unicode folded field", `{"relay_admiſſion_tokens":` + valid + `}`, false},
		{"uppercase invalid unselected", `{"RELAY_ADMISSION_TOKENS":{"selected":"` + admissionTestToken + `","private-identity":null}}`, false},
		{"uppercase null override", `{"relay_admission_tokens":` + valid + `,"RELAY_ADMISSION_TOKENS":null}`, false},
		{"uppercase null before canonical", `{"RELAY_ADMISSION_TOKENS":null,"relay_admission_tokens":` + valid + `}`, false},
		{"duplicate field null first", `{"relay_admission_tokens":null,"relay_admission_tokens":` + valid + `}`, false},
		{"duplicate field null last", `{"relay_admission_tokens":` + valid + `,"relay_admission_tokens":null}`, false},
		{"duplicate field invalid unselected", `{"relay_admission_tokens":{"private-identity":"private-parser-excerpt"},"relay_admission_tokens":` + valid + `}`, false},
		{"duplicate valid fields", `{"relay_admission_tokens":` + valid + `,"relay_admission_tokens":` + valid + `}`, false},
		{"escaped duplicate field", `{"relay_admission_tokens":` + valid + `,"relay_admission_\u0074okens":` + valid + `}`, false},
		{"duplicate identity null first", `{"relay_admission_tokens":{"selected":null,"selected":"` + admissionTestToken + `"}}`, false},
		{"duplicate identity null last", `{"relay_admission_tokens":{"selected":"` + admissionTestToken + `","selected":null}}`, false},
		{"duplicate valid identities", `{"relay_admission_tokens":{"selected":"` + admissionTestToken + `","selected":"` + admissionTestToken + `"}}`, false},
		{"escaped duplicate identity", `{"relay_admission_tokens":{"selected":null,"\u0073elected":"` + admissionTestToken + `"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := admissionConfigFile(t, tc.data)
			if tc.public {
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := Load(path)
			if err == nil {
				t.Error("ambiguous admission configuration loaded")
			}
			assertConfigErrorSafe(t, err)
			if !reflect.DeepEqual(cfg, Config{}) {
				t.Error("failure returned partial config")
			}
			if err := UpdateMemory(path, MemorySettings{}); err == nil {
				t.Error("ambiguous admission update accepted")
			} else {
				assertConfigErrorSafe(t, err)
			}
			raw, err := os.ReadFile(path)
			if err != nil || string(raw) != tc.data {
				t.Error("rejected update changed config bytes")
			}
		})
	}
}

func TestRelayAdmissionStorage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name             string
		file, dir        os.FileMode
		admission, valid bool
	}{
		{"private", 0o600, 0o700, true, true},
		{"public file", 0o644, 0o700, true, false}, {"read only file", 0o400, 0o700, true, false},
		{"public parent", 0o600, 0o755, true, false}, {"setgid parent", 0o600, os.ModeSetgid | 0o700, true, false},
		{"legacy modes", 0o644, 0o755, false, true},
		{"legacy searchable parent", 0o600, 0o333, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := `{}`
			if tc.admission {
				data = `{"relay_admission_tokens":{}}`
			}
			path := admissionConfigFile(t, data)
			t.Cleanup(func() {
				if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
					t.Error(err)
				}
			})
			if err := os.Chmod(path, tc.file); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Dir(path), tc.dir); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
			if tc.valid && !tc.admission {
				if err := UpdateMemory(path, MemorySettings{}); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.valid {
				assertConfigErrorSafe(t, err)
				if err := UpdateMemory(path, MemorySettings{}); err == nil {
					t.Error("update repaired insecure storage")
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != tc.file.Perm() {
					t.Error("file permissions repaired")
				}
			}
		})
	}
}

func TestRelayAdmissionUpdateMemory(t *testing.T) {
	t.Parallel()
	data := `{"relay_admission_tokens":{" A ":"` + admissionTestToken + `","B":"` + strings.Repeat("b", 64) + `","b":"` + strings.Repeat("c", 64) + `"},"unknown":123456789012345678901234567890}`
	path := admissionConfigFile(t, data)
	for i := 0; i < 2; i++ {
		if err := UpdateMemory(path, MemorySettings{Capture: MemoryCapture{Agent: "claude"}}); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var before, after map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &before); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &after); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"relay_admission_tokens", "unknown"} {
			var a, b any
			// Map values are strings; unrelated number compared directly to avoid rounding.
			if key == "unknown" {
				if string(after[key]) != string(before[key]) {
					t.Error("unknown number changed")
				}
				continue
			}
			if err := json.Unmarshal(before[key], &a); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(after[key], &b); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(a, b) {
				t.Error("admission map changed")
			}
		}
	}
}

// Metadata injection proves effective ownership without privileged chown.
type admissionFileInfo struct {
	os.FileInfo
	uid uint32
}

func (i admissionFileInfo) Sys() any { return &syscall.Stat_t{Uid: i.uid} }
func TestRelayAdmissionOwnership(t *testing.T) {
	path := admissionConfigFile(t, `{"relay_admission_tokens":{}}`)
	file, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Geteuid())
	root := map[string]json.RawMessage{"relay_admission_tokens": json.RawMessage(`{}`)}
	for _, tc := range []struct {
		fileUID, parentUID uint32
		valid              bool
	}{
		{uid, uid, true}, {uid + 1, uid, false}, {uid, uid + 1, false},
	} {
		_, err := validateAdmission(root, admissionFileInfo{file, tc.fileUID}, admissionFileInfo{parent, tc.parentUID})
		if (err == nil) != tc.valid {
			t.Errorf("ownership validation: %v", err)
		}
		assertConfigErrorSafe(t, err)
	}
}
