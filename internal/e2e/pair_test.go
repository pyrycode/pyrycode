//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/pair"
)

// TestPair_E2E proves bare issuance selects daemon-owned state. The first arm
// reproduces the incident: PYRY_NAME points at a saved pyry-agent identity while
// the sole running service is pyry. The second arm covers deterministic
// multiplicity, environment non-selection, and explicit targeting.
func TestPair_E2E(t *testing.T) {
	t.Run("sole running service wins over saved PYRY_NAME identity", func(t *testing.T) {
		home := shortHome(t)
		fr := fakerelay.New(relayTestLogger())
		t.Cleanup(func() { _ = fr.Close() })
		relayURL := fr.URL() + "/v2/server"

		saved, err := paireddevice.Setup(paireddevice.Config{
			Home:         home,
			InstanceName: "pyry-agent",
			Relay:        relayURL,
			DeviceName:   "saved-phone",
		})
		if err != nil {
			t.Fatalf("setup saved pyry-agent identity: %v", err)
		}
		agentBefore := snapshotPairSecurityState(t, home, "pyry-agent")

		startPairingDaemon(t, home, "pyry", relayURL)
		r := RunBareInWithEnv(t, home, []string{"PYRY_NAME=pyry-agent"},
			"pair", "--name=ordinary-phone")
		if r.ExitCode != 0 {
			t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s",
				r.ExitCode, r.Stdout, r.Stderr)
		}
		if !bytes.HasPrefix(r.Stdout, []byte("Service: pyry\n")) {
			t.Errorf("stdout does not name pyry before pairing output:\n%s", r.Stdout)
		}
		payload := decodePairPayload(t, r.Stdout)
		if string(payload.Server) == string(saved.Server) {
			t.Errorf("pairing used saved pyry-agent server %q", saved.Server)
		}
		assertPairSecurityState(t, agentBefore)

		registry, err := devices.Load(pairRegistryPath(home, "pyry"))
		if err != nil {
			t.Fatalf("load running pyry registry: %v", err)
		}
		ordinary := findPairDevice(t, registry, "ordinary-phone")
		if ordinary.AllowRemotePermissions {
			t.Error("ordinary pairing unexpectedly grants remote permissions")
		}
		if !devices.VerifyToken(payload.Token, ordinary.TokenHash) {
			t.Error("daemon registry does not contain returned token")
		}

		privileged := RunBareIn(t, home, "pair", "-pyry-name=pyry",
			"--name=operator-phone", "--allow-remote-permissions")
		if privileged.ExitCode != 0 {
			t.Fatalf("explicit pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s",
				privileged.ExitCode, privileged.Stdout, privileged.Stderr)
		}
		registry, err = devices.Load(pairRegistryPath(home, "pyry"))
		if err != nil {
			t.Fatalf("reload running pyry registry: %v", err)
		}
		if !findPairDevice(t, registry, "operator-phone").AllowRemotePermissions {
			t.Error("explicit remote permission choice did not reach daemon")
		}
	})

	t.Run("multiple services require command-line selection", func(t *testing.T) {
		home := shortHome(t)
		fr := fakerelay.New(relayTestLogger())
		t.Cleanup(func() { _ = fr.Close() })
		relayURL := fr.URL() + "/v2/server"

		startPairingDaemon(t, home, "zulu", relayURL)
		startPairingDaemon(t, home, "alpha", relayURL)
		if err := os.Symlink(filepath.Join(home, "absent.sock"), filepath.Join(home, ".pyry", "stale.sock")); err != nil {
			t.Fatalf("create stale socket: %v", err)
		}
		before := snapshotPairSecurityState(t, home, "alpha", "zulu", "ghost", "stale")

		for _, tc := range []struct {
			name string
			env  []string
		}{
			{name: "no environment"},
			{name: "PYRY_NAME is not explicit", env: []string{"PYRY_NAME=zulu"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r := RunBareInWithEnv(t, home, tc.env, "pair", "--name=must-not-exist")
				assertPairFailure(t, r, "multiple running services found: alpha, zulu", "-pyry-name")
				assertPairSecurityState(t, before)
			})
		}

		missing := RunBareIn(t, home, "pair", "-pyry-name=ghost", "--name=must-not-exist")
		assertPairFailure(t, missing, "ghost", "connect")
		assertPairSecurityState(t, before)
		stale := RunBareIn(t, home, "pair", "-pyry-name=stale", "--name=must-not-exist")
		assertPairFailure(t, stale, "stale", "connect")
		assertPairSecurityState(t, before)

		selected := RunBareIn(t, home, "pair", "-pyry-name=alpha", "--name=chosen")
		if selected.ExitCode != 0 {
			t.Fatalf("explicit alpha pair exit=%d\nstdout:\n%s\nstderr:\n%s",
				selected.ExitCode, selected.Stdout, selected.Stderr)
		}
		if !bytes.HasPrefix(selected.Stdout, []byte("Service: alpha\n")) {
			t.Errorf("stdout does not name alpha before pairing output:\n%s", selected.Stdout)
		}
		alpha, err := devices.Load(pairRegistryPath(home, "alpha"))
		if err != nil {
			t.Fatalf("load alpha registry: %v", err)
		}
		findPairDevice(t, alpha, "chosen")
		assertPairSecurityState(t, snapshotSubset(before, "zulu", "ghost", "stale"))
	})
}

// TestPairFailures_E2E covers refusal paths that must not mint or initialize
// credential state: no daemon, stale sockets, an older status-capable daemon,
// and --relay rejection before even a status probe.
func TestPairFailures_E2E(t *testing.T) {
	t.Run("no daemon and stale socket", func(t *testing.T) {
		for _, stale := range []bool{false, true} {
			name := "no socket"
			if stale {
				name = "stale socket"
			}
			t.Run(name, func(t *testing.T) {
				home := shortHome(t)
				if stale {
					if err := os.MkdirAll(filepath.Join(home, ".pyry"), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(filepath.Join(home, "absent.sock"), filepath.Join(home, ".pyry", "stale.sock")); err != nil {
						t.Fatal(err)
					}
				}
				before := snapshotPairSecurityState(t, home, "pyry", "stale")
				r := RunBareIn(t, home, "pair", "--name=must-not-exist")
				assertPairFailure(t, r, "no running services found")
				assertPairSecurityState(t, before)
			})
		}
	})

	t.Run("older daemon and relay refusal", func(t *testing.T) {
		home := shortHome(t)
		old := startOldPairControl(t, home, "old")
		before := snapshotPairSecurityState(t, home, "old", "pyry")

		relay := RunBareIn(t, home, "pair", "--relay=", "--name=must-not-exist")
		if relay.ExitCode != 2 {
			t.Fatalf("--relay exit=%d want 2\nstdout:\n%s\nstderr:\n%s",
				relay.ExitCode, relay.Stdout, relay.Stderr)
		}
		if len(relay.Stdout) != 0 || !bytes.Contains(relay.Stderr, []byte("--relay is not supported")) {
			t.Errorf("--relay refusal output mismatch\nstdout:\n%s\nstderr:\n%s", relay.Stdout, relay.Stderr)
		}
		if got := old.requests(); len(got) != 0 {
			t.Fatalf("--relay contacted control socket with %v", got)
		}
		assertPairSecurityState(t, before)

		unsupported := RunBareIn(t, home, "pair", "--name=must-not-exist")
		assertPairFailure(t, unsupported, "old", "unknown verb", "pairing.mint")
		if got := old.requests(); len(got) != 2 || got[0] != control.VerbStatus || got[1] != control.VerbPairingMint {
			t.Errorf("old daemon requests=%v want [status pairing.mint]", got)
		}
		assertPairSecurityState(t, before)
	})
}

func startPairingDaemon(t *testing.T, home, name, relayURL string) *Harness {
	t.Helper()
	h := StartInWithEnv(t, home, []string{
		"PYRY_ALLOW_INSECURE_RELAY=1",
		"PYRY_MOBILE_V2=1",
	}, "-pyry-name="+name, "-pyry-relay="+relayURL)
	exposePairSocket(t, home, name, h.SocketPath)
	return h
}

func exposePairSocket(t *testing.T, home, name, target string) {
	t.Helper()
	link := filepath.Join(home, ".pyry", name+".sock")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("expose %s control socket: %v", name, err)
	}
}

func pairRegistryPath(home, name string) string {
	return filepath.Join(home, ".pyry", name, "devices.json")
}

func findPairDevice(t *testing.T, registry *devices.Registry, name string) devices.Device {
	t.Helper()
	for _, device := range registry.List() {
		if device.Name == name {
			return device
		}
	}
	t.Fatalf("device %q missing from registry: %+v", name, registry.List())
	return devices.Device{}
}

func assertPairFailure(t *testing.T, result RunResult, fragments ...string) {
	t.Helper()
	if result.ExitCode == 0 {
		t.Fatalf("pair unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s", result.Stdout, result.Stderr)
	}
	if len(result.Stdout) != 0 {
		t.Errorf("failed pair wrote pairing output:\n%s", result.Stdout)
	}
	for _, fragment := range fragments {
		if !bytes.Contains(result.Stderr, []byte(fragment)) {
			t.Errorf("stderr missing %q:\n%s", fragment, result.Stderr)
		}
	}
}

type pairFileSnapshot struct {
	path   string
	exists bool
	data   []byte
}

func snapshotPairSecurityState(t *testing.T, home string, names ...string) []pairFileSnapshot {
	t.Helper()
	var snapshots []pairFileSnapshot
	for _, name := range names {
		for _, filename := range []string{"server-id", "static_key.json", "devices.json"} {
			path := filepath.Join(home, ".pyry", name, filename)
			data, err := os.ReadFile(path)
			snapshot := pairFileSnapshot{path: path}
			switch {
			case err == nil:
				snapshot.exists = true
				snapshot.data = data
			case errors.Is(err, fs.ErrNotExist):
			default:
				t.Fatalf("snapshot %s: %v", path, err)
			}
			snapshots = append(snapshots, snapshot)
		}
	}
	return snapshots
}

func snapshotSubset(snapshots []pairFileSnapshot, names ...string) []pairFileSnapshot {
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	var subset []pairFileSnapshot
	for _, snapshot := range snapshots {
		if wanted[filepath.Base(filepath.Dir(snapshot.path))] {
			subset = append(subset, snapshot)
		}
	}
	return subset
}

func assertPairSecurityState(t *testing.T, snapshots []pairFileSnapshot) {
	t.Helper()
	for _, snapshot := range snapshots {
		data, err := os.ReadFile(snapshot.path)
		if !snapshot.exists {
			if !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("%s was created or became unreadable: %v", snapshot.path, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("read %s after pair refusal: %v", snapshot.path, err)
			continue
		}
		if !bytes.Equal(data, snapshot.data) {
			t.Errorf("%s changed across pair refusal", snapshot.path)
		}
	}
}

type oldPairControl struct {
	listener net.Listener
	mu       sync.Mutex
	verbs    []control.Verb
}

func startOldPairControl(t *testing.T, home, name string) *oldPairControl {
	t.Helper()
	dir := filepath.Join(home, ".pyry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir control dir: %v", err)
	}
	listener, err := net.Listen("unix", filepath.Join(dir, name+".sock"))
	if err != nil {
		t.Fatalf("listen old control socket: %v", err)
	}
	server := &oldPairControl{listener: listener}
	t.Cleanup(func() { _ = listener.Close() })
	go server.serve()
	return server
}

func (s *oldPairControl) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		var req control.Request
		if err := json.NewDecoder(conn).Decode(&req); err == nil {
			s.mu.Lock()
			s.verbs = append(s.verbs, req.Verb)
			s.mu.Unlock()
			response := control.Response{Error: `unknown verb "` + string(req.Verb) + `"`}
			if req.Verb == control.VerbStatus {
				response = control.Response{Status: &control.StatusPayload{Phase: "running"}}
			}
			_ = json.NewEncoder(conn).Encode(response)
		}
		_ = conn.Close()
	}
}

func (s *oldPairControl) requests() []control.Verb {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]control.Verb(nil), s.verbs...)
}

// TestPairList_E2E exercises `pyry pair list` against a real binary.
// The "empty" sub-test asserts the cold-start contract (exact stdout
// "No paired devices.\n", exit 0). The "after pair" sub-test pairs a
// device first, then asserts the device Name and 8-char token-hash
// prefix appear in the list output.
func TestPairList_E2E(t *testing.T) {
	t.Run("empty registry", func(t *testing.T) {
		home := t.TempDir()
		r := RunBareIn(t, home, "pair", "list")
		if r.ExitCode != 0 {
			t.Fatalf("pyry pair list exit=%d\nstdout:\n%s\nstderr:\n%s",
				r.ExitCode, r.Stdout, r.Stderr)
		}
		if !bytes.Equal(r.Stdout, []byte("No paired devices.\n")) {
			t.Errorf("stdout=%q want %q", string(r.Stdout), "No paired devices.\n")
		}
	})

	t.Run("after pair", func(t *testing.T) {
		home := t.TempDir()
		setupOfflinePair(t, home, "phone-a")

		registryPath := filepath.Join(home, ".pyry", "pyry", "devices.json")
		registry, err := devices.Load(registryPath)
		if err != nil {
			t.Fatalf("devices.Load(%q): %v", registryPath, err)
		}
		list := registry.List()
		if len(list) != 1 {
			t.Fatalf("registry has %d entries, want 1", len(list))
		}
		wantPrefix := list[0].TokenHash[:8]

		listResult := RunBareIn(t, home, "pair", "list")
		if listResult.ExitCode != 0 {
			t.Fatalf("pyry pair list exit=%d\nstdout:\n%s\nstderr:\n%s",
				listResult.ExitCode, listResult.Stdout, listResult.Stderr)
		}
		out := string(listResult.Stdout)
		if !strings.Contains(out, "phone-a") {
			t.Errorf("stdout missing device name 'phone-a':\n%s", out)
		}
		if !strings.Contains(out, wantPrefix) {
			t.Errorf("stdout missing token-prefix %q:\n%s", wantPrefix, out)
		}
	})
}

// TestPairRevoke_E2E exercises `pyry pair revoke` against a real binary.
// Three sub-tests cover: the success path (one of two devices is removed
// and the survivor is preserved), the not-found path (exit 1, exact
// stderr text, on-disk file byte-identical), and the missing-registry
// case (no file is created).
func TestPairRevoke_E2E(t *testing.T) {
	t.Run("removes one of two", func(t *testing.T) {
		home := t.TempDir()
		registryPath := filepath.Join(home, ".pyry", "pyry", "devices.json")

		setupOfflinePair(t, home, "phone-a")
		setupOfflinePair(t, home, "phone-b")

		// Capture phone-b's recorded fields so we can prove they survive
		// the revoke unchanged.
		preReg, err := devices.Load(registryPath)
		if err != nil {
			t.Fatalf("devices.Load(pre): %v", err)
		}
		var preBravo devices.Device
		for _, d := range preReg.List() {
			if d.Name == "phone-b" {
				preBravo = d
				break
			}
		}
		if preBravo.Name != "phone-b" {
			t.Fatalf("phone-b missing before revoke; registry=%+v", preReg.List())
		}

		r := RunBareIn(t, home, "pair", "revoke", "phone-a")
		if r.ExitCode != 0 {
			t.Fatalf("pyry pair revoke phone-a exit=%d\nstdout:\n%s\nstderr:\n%s",
				r.ExitCode, r.Stdout, r.Stderr)
		}
		if !bytes.Equal(r.Stdout, []byte("Revoked phone-a.\n")) {
			t.Errorf("stdout=%q want %q", string(r.Stdout), "Revoked phone-a.\n")
		}
		if len(r.Stderr) != 0 {
			t.Errorf("stderr=%q want empty", string(r.Stderr))
		}

		postReg, err := devices.Load(registryPath)
		if err != nil {
			t.Fatalf("devices.Load(post): %v", err)
		}
		list := postReg.List()
		if len(list) != 1 {
			t.Fatalf("registry has %d entries after revoke, want 1", len(list))
		}
		got := list[0]
		if got.Name != "phone-b" {
			t.Errorf("survivor.Name=%q want %q", got.Name, "phone-b")
		}
		if got.TokenHash != preBravo.TokenHash {
			t.Errorf("survivor.TokenHash=%q want %q", got.TokenHash, preBravo.TokenHash)
		}
		if !got.PairedAt.Equal(preBravo.PairedAt) {
			t.Errorf("survivor.PairedAt=%v want %v", got.PairedAt, preBravo.PairedAt)
		}
	})

	t.Run("not found", func(t *testing.T) {
		home := t.TempDir()
		registryPath := filepath.Join(home, ".pyry", "pyry", "devices.json")

		setupOfflinePair(t, home, "phone-a")

		preBytes, err := os.ReadFile(registryPath)
		if err != nil {
			t.Fatalf("ReadFile(pre): %v", err)
		}

		r := RunBareIn(t, home, "pair", "revoke", "ghost")
		if r.ExitCode != 1 {
			t.Fatalf("pyry pair revoke ghost exit=%d, want 1\nstdout:\n%s\nstderr:\n%s",
				r.ExitCode, r.Stdout, r.Stderr)
		}
		if !bytes.Equal(r.Stderr, []byte("pyry pair revoke: no device named ghost\n")) {
			t.Errorf("stderr=%q want %q", string(r.Stderr), "pyry pair revoke: no device named ghost\n")
		}
		if len(r.Stdout) != 0 {
			t.Errorf("stdout=%q want empty", string(r.Stdout))
		}

		postBytes, err := os.ReadFile(registryPath)
		if err != nil {
			t.Fatalf("ReadFile(post): %v", err)
		}
		if !bytes.Equal(preBytes, postBytes) {
			t.Errorf("registry bytes changed across not-found revoke\npre:\n%s\npost:\n%s",
				preBytes, postBytes)
		}
	})

	t.Run("missing registry", func(t *testing.T) {
		home := t.TempDir()
		registryPath := filepath.Join(home, ".pyry", "pyry", "devices.json")

		r := RunBareIn(t, home, "pair", "revoke", "ghost")
		if r.ExitCode != 1 {
			t.Fatalf("pyry pair revoke ghost (cold) exit=%d, want 1\nstdout:\n%s\nstderr:\n%s",
				r.ExitCode, r.Stdout, r.Stderr)
		}
		if !bytes.Equal(r.Stderr, []byte("pyry pair revoke: no device named ghost\n")) {
			t.Errorf("stderr=%q want %q", string(r.Stderr), "pyry pair revoke: no device named ghost\n")
		}
		if _, err := os.Stat(registryPath); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("registry file exists or stat error: err=%v (want fs.ErrNotExist)", err)
		}
	})
}

func setupOfflinePair(t *testing.T, home, deviceName string) pair.Payload {
	t.Helper()
	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:         home,
		InstanceName: "pyry",
		Relay:        "wss://fixture.invalid/v2/server",
		DeviceName:   deviceName,
	})
	if err != nil {
		t.Fatalf("setup offline pair fixture %q: %v", deviceName, err)
	}
	return payload
}

// decodePairPayload finds the encoded payload in pair output and decodes
// it. Render writes the QR (UTF-8 half-blocks) followed by a blank line,
// the encoded string on its own line, and a one-line instruction.
// Pulling out the encoded line means scanning each line for one that
// pair.Decode accepts.
func decodePairPayload(t *testing.T, stdout []byte) pair.Payload {
	t.Helper()
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if p, err := pair.Decode(line); err == nil {
			return p
		}
	}
	t.Fatalf("no decodable pair payload found in stdout:\n%s", stdout)
	return pair.Payload{}
}
