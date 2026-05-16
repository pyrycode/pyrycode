package keys

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadOrCreate_FreshCreate(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	const daemon = "test"

	sk, err := LoadOrCreate(base, daemon)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if sk == nil {
		t.Fatal("LoadOrCreate returned nil *StaticKey on success")
	}

	dir := filepath.Join(base, daemon)
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat parent dir: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Errorf("parent dir mode = %v, want 0700", mode)
	}

	path := filepath.Join(dir, "static_key.json")
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if mode := fileInfo.Mode().Perm(); mode != 0o600 {
		t.Errorf("key file mode = %v, want 0600", mode)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read key file: %v", err)
	}
	var d onDiskKey
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("unmarshal persisted file: %v", err)
	}
	if d.Version != schemaVersion {
		t.Errorf("on-disk version = %d, want %d", d.Version, schemaVersion)
	}
	if d.Algorithm != algorithmName {
		t.Errorf("on-disk algorithm = %q, want %q", d.Algorithm, algorithmName)
	}
	privBytes, err := base64.StdEncoding.DecodeString(d.PrivateKey)
	if err != nil {
		t.Fatalf("decode private_key: %v", err)
	}
	if len(privBytes) != 32 {
		t.Errorf("private_key length = %d, want 32", len(privBytes))
	}
	pubBytes, err := base64.StdEncoding.DecodeString(d.PublicKey)
	if err != nil {
		t.Fatalf("decode public_key: %v", err)
	}
	if len(pubBytes) != 32 {
		t.Errorf("public_key length = %d, want 32", len(pubBytes))
	}
	if !bytes.Equal(privBytes, sk.priv[:]) {
		t.Errorf("on-disk private_key disagrees with returned StaticKey")
	}
	if !bytes.Equal(pubBytes, sk.pub[:]) {
		t.Errorf("on-disk public_key disagrees with returned StaticKey")
	}

	priv, err := ecdh.X25519().NewPrivateKey(privBytes)
	if err != nil {
		t.Fatalf("ecdh.NewPrivateKey: %v", err)
	}
	if !bytes.Equal(priv.PublicKey().Bytes(), pubBytes) {
		t.Errorf("derived public != on-disk public")
	}
}

func TestLoadOrCreate_RoundTripStable(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	const daemon = "test"

	first, err := LoadOrCreate(base, daemon)
	if err != nil {
		t.Fatalf("first LoadOrCreate: %v", err)
	}
	path := filepath.Join(base, daemon, "static_key.json")
	preInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after first: %v", err)
	}
	preMtime := preInfo.ModTime()
	preBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after first: %v", err)
	}

	second, err := LoadOrCreate(base, daemon)
	if err != nil {
		t.Fatalf("second LoadOrCreate: %v", err)
	}
	if first.PublicKey() != second.PublicKey() {
		t.Errorf("public key changed across LoadOrCreate calls")
	}
	if first.PrivateKey() != second.PrivateKey() {
		t.Errorf("private key changed across LoadOrCreate calls")
	}

	postInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("re-stat: %v", err)
	}
	if !postInfo.ModTime().Equal(preMtime) {
		t.Errorf("mtime changed on load path: pre=%v post=%v", preMtime, postInfo.ModTime())
	}
	postBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if !bytes.Equal(preBytes, postBytes) {
		t.Errorf("file mutated on load path")
	}
}

func TestLoadOrCreate_InvalidDaemonName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"single dot", "."},
		{"double dot", ".."},
		{"forward slash", "foo/bar"},
		{"traversal", "foo/../bar"},
		{"backslash", "foo\\bar"},
		{"uppercase", "Foo"},
		{"embedded dot", "foo.bar"},
		{"leading hyphen", "-leading"},
		{"space", "foo bar"},
		{"nul byte", "foo\x00bar"},
		{"oversize", strings.Repeat("a", 65)},
	}
	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := t.TempDir()
			sk, err := LoadOrCreate(base, tt.input)
			if !errors.Is(err, ErrInvalidDaemonName) {
				t.Errorf("err = %v, want errors.Is ErrInvalidDaemonName", err)
			}
			if sk != nil {
				t.Errorf("StaticKey = %v, want nil on reject", sk)
			}

			// No filesystem touch: the package returns before any path is
			// constructed, so baseDir must remain empty regardless of how the
			// input would have decomposed under filepath.Join.
			entries, readErr := os.ReadDir(base)
			if readErr != nil {
				t.Fatalf("read baseDir: %v", readErr)
			}
			if len(entries) != 0 {
				t.Errorf("baseDir has %d entries after reject, want 0", len(entries))
			}
		})
	}
}

func TestLoadOrCreate_CorruptJSONReturnsSentinel(t *testing.T) {
	t.Parallel()
	knownPriv := bytes.Repeat([]byte{0x42}, 32)
	knownPub := derivePublic(t, knownPriv)
	mismatchedPub := bytes.Repeat([]byte{0x99}, 32)

	validJSON := func(version int, algorithm string, priv, pub []byte, createdAt string) string {
		// hand-rolled to allow created_at to be a non-RFC3339 string
		return fmt.Sprintf(
			`{"version":%d,"algorithm":%q,"private_key":%q,"public_key":%q,"created_at":%q}`,
			version,
			algorithm,
			base64.StdEncoding.EncodeToString(priv),
			base64.StdEncoding.EncodeToString(pub),
			createdAt,
		)
	}

	createdAt := time.Now().UTC().Format(time.RFC3339)

	cases := []struct {
		name     string
		contents string
	}{
		{"not json", "not json"},
		{"missing closing brace", validJSON(1, algorithmName, knownPriv, knownPub, createdAt)[:30]},
		{"version 2", validJSON(2, algorithmName, knownPriv, knownPub, createdAt)},
		{"version 0", validJSON(0, algorithmName, knownPriv, knownPub, createdAt)},
		{"wrong algorithm", validJSON(1, "X25519", knownPriv, knownPub, createdAt)},
		{"private not base64", `{"version":1,"algorithm":"Noise_25519","private_key":"@@@","public_key":"` + base64.StdEncoding.EncodeToString(knownPub) + `","created_at":"` + createdAt + `"}`},
		{"private wrong length", validJSON(1, algorithmName, bytes.Repeat([]byte{0x01}, 16), knownPub, createdAt)},
		{"public not base64", `{"version":1,"algorithm":"Noise_25519","private_key":"` + base64.StdEncoding.EncodeToString(knownPriv) + `","public_key":"@@@","created_at":"` + createdAt + `"}`},
		{"public wrong length", validJSON(1, algorithmName, knownPriv, bytes.Repeat([]byte{0x01}, 16), createdAt)},
		{"public mismatched private", validJSON(1, algorithmName, knownPriv, mismatchedPub, createdAt)},
		{"created_at not rfc3339", validJSON(1, algorithmName, knownPriv, knownPub, "yesterday")},
	}

	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := t.TempDir()
			const daemon = "test"
			dir := filepath.Join(base, daemon)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			path := filepath.Join(dir, "static_key.json")
			if err := os.WriteFile(path, []byte(tt.contents), 0o600); err != nil {
				t.Fatalf("seed fixture: %v", err)
			}

			sk, err := LoadOrCreate(base, daemon)
			if !errors.Is(err, ErrCorruptKeyFile) {
				t.Errorf("err = %v, want errors.Is ErrCorruptKeyFile", err)
			}
			if sk != nil {
				t.Errorf("StaticKey = %v, want nil on corrupt", sk)
			}

			post, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("re-read: %v", err)
			}
			if string(post) != tt.contents {
				t.Errorf("file mutated on corrupt-read path")
			}
		})
	}
}

func TestLoadOrCreate_CorruptJSONErrorDoesNotLeakPrivateKey(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	const daemon = "test"
	dir := filepath.Join(base, daemon)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "static_key.json")

	knownPriv := bytes.Repeat([]byte{0x01}, 32)
	privB64 := base64.StdEncoding.EncodeToString(knownPriv)
	pubB64 := base64.StdEncoding.EncodeToString(derivePublic(t, knownPriv))
	createdAt := time.Now().UTC().Format(time.RFC3339)

	// Mutate algorithm to a wrong value to trigger ErrCorruptKeyFile.
	contents := fmt.Sprintf(
		`{"version":1,"algorithm":"X25519","private_key":%q,"public_key":%q,"created_at":%q}`,
		privB64, pubB64, createdAt,
	)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}

	sk, err := LoadOrCreate(base, daemon)
	if !errors.Is(err, ErrCorruptKeyFile) {
		t.Fatalf("err = %v, want errors.Is ErrCorruptKeyFile", err)
	}
	if sk != nil {
		t.Fatalf("StaticKey = %v, want nil on corrupt", sk)
	}
	if strings.Contains(err.Error(), privB64) {
		t.Errorf("error message contains private_key base64: %q", err.Error())
	}
}

func TestLoadOrCreate_NonENOENTReadErrorIsNotCorruption(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	const daemon = "test"
	dir := filepath.Join(base, daemon)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	// Make the keystore path itself a directory so ReadFile returns a
	// non-ENOENT error (EISDIR or platform equivalent).
	if err := os.Mkdir(filepath.Join(dir, "static_key.json"), 0o700); err != nil {
		t.Fatalf("mkdir trap: %v", err)
	}

	sk, err := LoadOrCreate(base, daemon)
	if err == nil {
		t.Fatal("LoadOrCreate returned nil error for directory-as-file")
	}
	if errors.Is(err, ErrCorruptKeyFile) {
		t.Errorf("I/O error misclassified as ErrCorruptKeyFile: %v", err)
	}
	if errors.Is(err, ErrInvalidDaemonName) {
		t.Errorf("I/O error misclassified as ErrInvalidDaemonName: %v", err)
	}
	if sk != nil {
		t.Errorf("StaticKey = %v, want nil on error", sk)
	}
}

func derivePublic(t *testing.T, priv []byte) []byte {
	t.Helper()
	pk, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		t.Fatalf("derivePublic NewPrivateKey: %v", err)
	}
	return pk.PublicKey().Bytes()
}
