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

	"golang.org/x/sys/unix"
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
		effectiveGroup   bool
	}{
		{"private", 0o600, 0o700, true, true, false},
		{"public file", 0o644, 0o700, true, false, false}, {"read only file", 0o400, 0o700, true, false, false},
		{"public parent", 0o600, 0o755, true, false, false},
		{"setgid parent", 0o600, os.ModeSetgid | 0o700, true, false, false},
		{"setgid parent effective group", 0o600, os.ModeSetgid | 0o700, true, false, true},
		{"legacy modes", 0o644, 0o755, false, true, false},
		{"legacy searchable parent", 0o600, 0o333, false, true, false},
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
			var groupErr error
			if tc.effectiveGroup {
				admissionStorageMetadata(t, path, tc.file, 0o700)
				groupErr = os.Chown(filepath.Dir(path), -1, os.Getegid())
			}
			chmodErr := os.Chmod(filepath.Dir(path), tc.dir)
			file, parent := admissionStorageMetadata(t, path, tc.file, tc.dir)
			missingSetgid := tc.dir&os.ModeSetgid != 0 && parent.Mode() == os.ModeDir|0o700 &&
				file.Mode() == 0o600 && file.Sys().(*syscall.Stat_t).Uid == uint32(os.Geteuid()) &&
				parent.Sys().(*syscall.Stat_t).Uid == uint32(os.Geteuid())
			valid := tc.valid || missingSetgid
			cfg, loadErr := Load(path)
			updateErr := UpdateMemory(path, MemorySettings{})
			t.Logf("effective_group_requested=%v group_setup=%v parent_chmod=%v setgid_established=%v; Load: error=%v zero_config=%v admission_present=%v; UpdateMemory: error=%v",
				tc.effectiveGroup, groupErr, chmodErr, parent.Mode()&os.ModeSetgid != 0,
				loadErr, reflect.DeepEqual(cfg, Config{}), cfg.RelayAdmissionTokens != nil, updateErr)
			if chmodErr != nil && !missingSetgid {
				t.Error(chmodErr)
			}
			if parent.Mode() != os.ModeDir|tc.dir && !missingSetgid {
				t.Errorf("native prerequisite: requested parent mode %v, actual %v", os.ModeDir|tc.dir, parent.Mode())
			}
			if (loadErr == nil) != valid {
				t.Errorf("Load: valid=%v, error=%v", valid, loadErr)
			}
			if (updateErr == nil) != valid {
				t.Errorf("UpdateMemory: valid=%v, error=%v", valid, updateErr)
			}
			assertConfigErrorSafe(t, loadErr)
			assertConfigErrorSafe(t, updateErr)
			if loadErr != nil && !reflect.DeepEqual(cfg, Config{}) {
				t.Error("failure returned partial config")
			}
			if !valid || updateErr != nil {
				raw, err := os.ReadFile(path)
				if err != nil || string(raw) != data {
					t.Errorf("rejected update changed bytes: read error=%v", err)
				}
				afterFile, afterParent := admissionStorageMetadata(t, path, tc.file, tc.dir)
				if afterFile.Mode() != file.Mode() || afterParent.Mode() != parent.Mode() {
					t.Error("rejected update changed file or parent mode")
				}
			}
			if missingSetgid && !t.Failed() {
				t.Skipf("native prerequisite unmet: setgid did not persist: requested=%v actual=%v parent_uid=%d parent_gid=%d euid=%d egid=%d group_setup=%v chmod=%v; see fixture/filesystem metadata above",
					os.ModeDir|tc.dir, parent.Mode(), parent.Sys().(*syscall.Stat_t).Uid,
					parent.Sys().(*syscall.Stat_t).Gid, os.Geteuid(), os.Getegid(), groupErr, chmodErr)
			}
		})
	}
}

func admissionStorageMetadata(t *testing.T, path string, fileMode, parentMode os.FileMode) (os.FileInfo, os.FileInfo) {
	t.Helper()
	resolvedRoot, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(filepath.Dir(path), &fs); err != nil {
		t.Fatal(err)
	}
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("TMPDIR supplied=%q temp_root=%q resolved=%q fixture=%q resolved_fixture=%q fs_type=%#x fs_id=%v uid=%d gid=%d euid=%d egid=%d groups=%v",
		os.Getenv("TMPDIR"), os.TempDir(), resolvedRoot, path, resolvedPath, fs.Type, fs.Fsid, os.Getuid(), os.Getgid(), os.Geteuid(), os.Getegid(), groups)
	infos := make([]os.FileInfo, 0, 2)
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{{path, fileMode}, {filepath.Dir(path), os.ModeDir | parentMode}} {
		info, err := os.Stat(item.path)
		if err != nil {
			t.Fatal(err)
		}
		st := info.Sys().(*syscall.Stat_t)
		t.Logf("path=%q requested_mode=%v actual_mode=%v uid=%d gid=%d device=%d inode=%d",
			item.path, item.mode, info.Mode(), st.Uid, st.Gid, st.Dev, st.Ino)
		infos = append(infos, info)
	}
	return infos[0], infos[1]
}

type admissionModeInfo struct {
	os.FileInfo
	mode os.FileMode
}

func (i admissionModeInfo) Mode() os.FileMode { return i.mode }

func TestRelayAdmissionStorageModes(t *testing.T) {
	t.Parallel()
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
		name  string
		mode  os.FileMode
		valid bool
	}{
		{"private", os.ModeDir | 0o700, true},
		{"setgid parent", os.ModeDir | os.ModeSetgid | 0o700, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := admissionModeInfo{admissionFileInfo{file, uid}, 0o600}
			p := admissionModeInfo{admissionFileInfo{parent, uid}, tc.mode}
			tokens, err := validateAdmission(root, f, p)
			if (err == nil) != tc.valid {
				t.Errorf("valid=%v, error=%v", tc.valid, err)
			}
			if tc.valid && tokens == nil || !tc.valid && tokens != nil {
				t.Error("unexpected admission map presence")
			}
			assertConfigErrorSafe(t, err)
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
