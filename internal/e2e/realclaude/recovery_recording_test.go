//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

type recoveryRecordingConfig struct {
	Process                                       claudetape.PoolProcessConfig
	Root, Revision, Wrapper, Daemon, DaemonSHA256 string
}

func readRecoveryRecordingConfig(path string) (recoveryRecordingConfig, error) {
	var c recoveryRecordingConfig
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 16384 {
		return c, os.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.Process.Scenario != claudetape.RecoveryScenario || c.Root != claudetape.RecoveryRoot || c.Revision == "" || c.Revision != poolRecordedRevision || !filepath.IsAbs(c.Process.Home) || !filepath.IsAbs(c.Daemon) || c.Wrapper != filepath.Join(c.Process.Home, claudetape.RecoveryMetadataDirectory, "claude") {
		return c, os.ErrInvalid
	}
	return c, nil
}
func recoveryRecordingCurrent(t *testing.T) (recoveryRecordingConfig, string, bool) {
	t.Helper()
	path := os.Getenv("PYRY_RECOVERY_RECORDING_CONFIG")
	if path == "" {
		return recoveryRecordingConfig{}, "", false
	}
	c, err := readRecoveryRecordingConfig(path)
	if err != nil {
		t.Fatal("invalid recovery recording config")
	}
	role := ""
	if t.Name() != c.Root {
		for _, name := range claudetape.RecoveryRoles() {
			if t.Name() == c.Root+"/"+name {
				role = name
			}
		}
		if role == "" {
			t.Fatal("recovery recording does not own original root")
		}
	}
	if c.Process.Mode == "replay" {
		for _, key := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR"} {
			if os.Getenv(key) != "" {
				t.Fatal("recovery replay inherited provider configuration")
			}
		}
	} else if c.Process.Mode != "capture" || os.Getenv("CLAUDE_CONFIG_DIR") != "" || (os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "") {
		t.Fatal("recovery capture lacks explicit credentials")
	}
	return c, role, true
}
func recoveryRecordingHome(t *testing.T) string {
	t.Helper()
	c, role, ok := recoveryRecordingCurrent(t)
	if !ok {
		return authenticatedHome(t)
	}
	if role == "" {
		role = "auth"
	}
	home := filepath.Join(c.Process.Home, role)
	info, err := os.Lstat(home)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("recovery HOME is not private")
	}
	return home
}
func recoveryRecordingDaemon(t *testing.T) (string, bool) {
	t.Helper()
	c, _, ok := recoveryRecordingCurrent(t)
	if !ok {
		return "", false
	}
	raw, err := os.ReadFile(c.Daemon)
	sum := sha256.Sum256(raw)
	if err != nil || hex.EncodeToString(sum[:]) != c.DaemonSHA256 {
		t.Fatal("recovery control-enabled daemon changed")
	}
	return c.Daemon, true
}
func recoveryRecordingRegisterSocket(t *testing.T, home, bin, socket string) {
	t.Helper()
	c, role, ok := recoveryRecordingCurrent(t)
	if !ok {
		return
	}
	if role == "" || home != filepath.Join(c.Process.Home, role) || bin != c.Daemon || !filepath.IsAbs(socket) {
		t.Fatal("recovery daemon launch escaped its role")
	}
	raw, err := os.ReadFile(bin)
	sum := sha256.Sum256(raw)
	if err != nil || hex.EncodeToString(sum[:]) != c.DaemonSHA256 {
		t.Fatal("recovery daemon checksum changed")
	}
	if daemonRecordingWrite(filepath.Join(c.Process.Home, claudetape.RecoveryMetadataDirectory, role+".endpoint"), claudetape.RecoveryEndpoint{Daemon: bin, Socket: socket}) != nil {
		t.Fatal("recovery endpoint rebound")
	}
}
func recoveryRecordingReadTranscript(path string) (claudetape.RecoveryTranscript, error) {
	var value claudetape.RecoveryTranscript
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > claudetape.MaxBytes {
		return value, os.ErrInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return value, err
	}
	opened, statErr := f.Stat()
	raw, readErr := io.ReadAll(io.LimitReader(f, claudetape.MaxBytes+1))
	closeErr := f.Close()
	if statErr != nil || !os.SameFile(info, opened) || readErr != nil || closeErr != nil || len(raw) > claudetape.MaxBytes {
		return value, os.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&value) != nil || d.Decode(new(any)) != io.EOF || value.Validate() != nil {
		return value, os.ErrInvalid
	}
	return value, nil
}

// The original root reaches this hook only after its own correlated completed
// turn, delivery and empty-queue observations. Capture reads the actual native
// JSONL; replay restores that exact witness before the unchanged marker checks.
func recoveryRecordingTranscript(t *testing.T, workdir, id string) {
	t.Helper()
	c, role, ok := recoveryRecordingCurrent(t)
	if !ok {
		return
	}
	home := filepath.Join(c.Process.Home, role)
	if role == "" || workdir != filepath.Join(home, "work") || id != liveBootstrapUUID {
		t.Fatal("recovery transcript escaped its original role")
	}
	meta := filepath.Join(c.Process.Home, claudetape.RecoveryMetadataDirectory)
	var endpoint claudetape.RecoveryEndpoint
	if daemonRecordingRead(filepath.Join(meta, role+".endpoint"), &endpoint) != nil || endpoint.Daemon != c.Daemon {
		t.Fatal("missing recovery endpoint")
	}
	bind := func(text string, restore bool) string {
		for _, v := range []struct{ actual, label string }{{c.Daemon, "${RECOVERY_DAEMON}"}, {endpoint.Socket, "${RECOVERY_SOCKET}"}, {home, "${RECOVERY_HOME}"}} {
			if restore {
				text = strings.ReplaceAll(text, v.label, v.actual)
			} else {
				text = strings.ReplaceAll(text, v.actual, v.label)
			}
		}
		return text
	}
	path := jsonlPathFor(workdir, id)
	rel, err := filepath.Rel(home, path)
	if err != nil || !filepath.IsAbs(path) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatal("recovery session path escaped HOME")
	}
	artifact := filepath.Join(meta, role+".transcript")
	if c.Process.Mode == "capture" {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > claudetape.MaxBytes {
			t.Fatal("missing actual recovered transcript")
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		opened, statErr := f.Stat()
		raw, readErr := io.ReadAll(io.LimitReader(f, claudetape.MaxBytes+1))
		closeErr := f.Close()
		if statErr != nil || !os.SameFile(info, opened) || readErr != nil || closeErr != nil || len(raw) > claudetape.MaxBytes {
			t.Fatal("recovered transcript read changed")
		}
		value := claudetape.RecoveryTranscript{SessionID: id, Snapshot: claudetape.CLISnapshot{Exists: true, Mode: uint32(info.Mode().Perm()), Data: bind(string(raw), false)}}
		if value.Validate() != nil || daemonRecordingWrite(artifact, value) != nil {
			t.Fatal("cannot capture recovered transcript witness")
		}
	} else {
		value, err := recoveryRecordingReadTranscript(artifact)
		if err != nil || value.SessionID != id {
			t.Fatal("invalid reviewed recovery witness")
		}
		if os.MkdirAll(filepath.Dir(path), 0700) != nil {
			t.Fatal("cannot prepare recovered session directory")
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(value.Snapshot.Mode))
		if err != nil {
			t.Fatal("recovered session witness already exists")
		}
		_, writeErr := io.WriteString(f, bind(value.Snapshot.Data, true))
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("cannot restore reviewed recovery witness")
		}
	}
}
func finishRecoveryRecording(t *testing.T) {
	t.Helper()
	c, role, ok := recoveryRecordingCurrent(t)
	if !ok || t.Failed() {
		return
	}
	if role == "" {
		t.Fatal("recovery seal requires a subtest role")
	}
	prefix := filepath.Join(c.Process.Home, claudetape.RecoveryMetadataDirectory, role)
	f, err := os.OpenFile(prefix+".finish", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("recovery role already sealed")
	}
	if f.Close() != nil {
		t.Fatal("recovery seal failed")
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		var r poolRecordingReceipt
		if daemonRecordingRead(prefix+".receipt", &r) == nil {
			if !r.Success || r.Frames == 0 {
				t.Fatalf("recovery child did not seal: %s", r.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovery seal timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
	digest, err := claudetape.AuditRecoveryRole(c.Process.Home, role)
	if err != nil {
		t.Fatal(err)
	}
	f, err = os.OpenFile(prefix+".seal", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("recovery immutable seal exists")
	}
	_, writeErr := io.WriteString(f, digest)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal("recovery immutable seal failed")
	}
}
func TestRecoveryTapeHelperProcess(t *testing.T) {
	path := os.Getenv("PYRY_RECOVERY_RECORDING_CONFIG")
	if path == "" {
		return
	}
	c, err := readRecoveryRecordingConfig(path)
	if err != nil {
		os.Exit(2)
	}
	meta := filepath.Join(c.Process.Home, claudetape.RecoveryMetadataDirectory)
	fail := func() { _ = os.WriteFile(filepath.Join(meta, "unexpected"), nil, 0600); os.Exit(2) }
	if c.Process.Mode == "capture" {
		exe, err := os.Executable()
		if err != nil {
			fail()
		}
		raw, err := os.ReadFile(exe)
		sum := sha256.Sum256(raw)
		if err != nil || hex.EncodeToString(sum[:]) != c.Process.Producer.BinarySHA256 {
			fail()
		}
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(os.Stdout, c.Process.Producer.ClaudeVersion)
		os.Exit(0)
	}
	role := ""
	for _, name := range claudetape.RecoveryRoles() {
		if os.Getenv("HOME") == filepath.Join(c.Process.Home, name) {
			role = name
		}
	}
	if role == "" {
		fail()
	}
	var endpoint claudetape.RecoveryEndpoint
	if daemonRecordingRead(filepath.Join(meta, role+".endpoint"), &endpoint) != nil || endpoint.Daemon != c.Daemon {
		fail()
	}
	process := c.Process
	prefix := filepath.Join(meta, role)
	process.Scenario += "-" + role
	process.Home = filepath.Join(process.Home, role)
	process.WorkDir = filepath.Join(process.Home, "work")
	process.Path, process.Claim, process.Finish = prefix+".json", prefix+".claim", prefix+".finish"
	frames, err := claudetape.RunRecoveryProcess(claudetape.RecoveryProcessConfig{PoolProcessConfig: process, Daemon: endpoint.Daemon, Socket: endpoint.Socket}, args, os.Stdin, os.Stdout, os.Stderr)
	receipt := poolRecordingReceipt{Success: err == nil, Frames: frames}
	if err != nil {
		receipt.Error = err.Error()
	}
	if daemonRecordingWrite(prefix+".receipt", receipt) != nil {
		fail()
	}
	if err != nil {
		fail()
	}
	fd := int(os.Stdin.Fd())
	if syscall.SetNonblock(fd, true) != nil {
		fail()
	}
	deadline := time.Now().Add(30 * time.Second)
	var discard [4096]byte
	for time.Now().Before(deadline) {
		n, e := syscall.Read(fd, discard[:])
		if n == 0 && e == nil {
			os.Exit(0)
		}
		if e != nil && e != syscall.EAGAIN && e != syscall.EWOULDBLOCK && e != syscall.EINTR {
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
	os.Exit(0)
}
