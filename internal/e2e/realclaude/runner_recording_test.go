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
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

type runnerRecordingConfig struct {
	Process                 claudetape.PoolProcessConfig
	Root, Revision, Wrapper string
}

func readRunnerRecordingConfig(path string) (runnerRecordingConfig, error) {
	var c runnerRecordingConfig
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 16384 {
		return c, os.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || claudetape.RunnerProfiles()[c.Process.Scenario].Root != c.Root || c.Root == "" || c.Revision == "" || c.Revision != poolRecordedRevision {
		return c, os.ErrInvalid
	}
	return c, nil
}
func runnerRecordingCurrent(t *testing.T) (runnerRecordingConfig, bool) {
	t.Helper()
	path := os.Getenv("PYRY_RUNNER_RECORDING_CONFIG")
	if path == "" {
		return runnerRecordingConfig{}, false
	}
	c, err := readRunnerRecordingConfig(path)
	if err != nil || c.Root != t.Name() {
		t.Fatal("runner recording does not own original root")
	}
	if c.Process.Mode == "replay" {
		for _, key := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR"} {
			if os.Getenv(key) != "" {
				t.Fatal("runner replay inherited provider configuration")
			}
		}
	} else if c.Process.Mode != "capture" || os.Getenv("CLAUDE_CONFIG_DIR") != "" || (os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "") {
		t.Fatal("runner capture lacks explicit credentials")
	}
	return c, true
}
func runnerRecordingHome(t *testing.T, parallel bool) string {
	t.Helper()
	c, ok := runnerRecordingCurrent(t)
	if !ok {
		if parallel {
			return liveHome(t)
		}
		return WithWorktreeAuthenticated(t)
	}
	info, err := os.Lstat(c.Process.Home)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("runner HOME is not private")
	}
	if !parallel {
		t.Setenv("HOME", c.Process.Home)
	}
	return c.Process.Home
}
func runnerRecordingNonce(t *testing.T, live int64) int64 {
	if _, ok := runnerRecordingCurrent(t); ok {
		return 3141592653
	}
	return live
}
func finishRunnerRecordingRole(t *testing.T, role string) {
	t.Helper()
	c, ok := runnerRecordingCurrent(t)
	if !ok || t.Failed() {
		return
	}
	if _, ok := claudetape.RunnerProfiles()[c.Process.Scenario].Roles[role]; !ok {
		t.Fatal("unknown runner role seal")
	}
	meta := filepath.Join(c.Process.Home, claudetape.RunnerMetadataDirectory)
	path := filepath.Join(meta, role)
	f, err := os.OpenFile(path+".finish", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("runner role already sealed")
	}
	if f.Close() != nil {
		t.Fatal("runner seal close failed")
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		var r poolRecordingReceipt
		if err := daemonRecordingRead(path+".receipt", &r); err == nil {
			if !r.Success || r.Frames == 0 {
				t.Fatalf("runner role did not seal: %s", r.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runner role seal timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
	digest, err := claudetape.AuditRunnerRole(c.Process.Home, c.Process.Scenario, role)
	if err != nil {
		t.Fatal(err)
	}
	f, err = os.OpenFile(path+".seal", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("runner role immutable seal exists")
	}
	_, writeErr := io.WriteString(f, digest)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal("runner role immutable seal write failed")
	}
}
func TestRunnerTapeHelperProcess(t *testing.T) {
	path := os.Getenv("PYRY_RUNNER_RECORDING_CONFIG")
	if path == "" {
		return
	}
	c, err := readRunnerRecordingConfig(path)
	if err != nil {
		os.Exit(2)
	}
	meta := filepath.Join(c.Process.Home, claudetape.RunnerMetadataDirectory)
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
	session := ""
	for i, arg := range args {
		if arg == "--session-id" && i+1 < len(args) {
			if session != "" {
				fail()
			}
			session = args[i+1]
		}
	}
	role := ""
	var owned claudetape.RunnerRole
	for name, spec := range claudetape.RunnerProfiles()[c.Process.Scenario].Roles {
		if spec.SessionID == session {
			role, owned = name, spec
		}
	}
	if role == "" {
		fail()
	}
	process := c.Process
	prefix := filepath.Join(meta, role)
	process.Path, process.Claim, process.Finish = prefix+".json", prefix+".claim", prefix+".finish"
	process.WorkDir = filepath.Join(process.Home, owned.Workdir)
	frames, err := claudetape.RunRunnerProcess(process, role, args, os.Stdin, os.Stdout, os.Stderr)
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
	// Hold the supervised wrapper through original runner cancellation. The
	// captured native child has already been waited; post-seal input is excluded.
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
