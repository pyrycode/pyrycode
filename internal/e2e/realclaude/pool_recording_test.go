//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

// This optional transport adapter runs the original pool test body. It never
// changes the original credential path unless an explicit bounded recording
// profile has been supplied by the offline/capture parent.
var poolRecordedRevision string

type poolRecordingConfig struct {
	Process       claudetape.PoolProcessConfig
	Root, Receipt string
}
type poolRecordingReceipt struct {
	Success bool   `json:"success"`
	Frames  int    `json:"frames"`
	Error   string `json:"error,omitempty"`
}

var poolRecordingRoots = map[string]string{
	"TestInteractiveStream_InBandModelChange_LiveChildReportsNewModel":      "pool-model-change",
	"TestInteractiveStream_InBandBypassRevoke_LiveChildReportsDefaultMode":  "pool-bypass-revoke",
	"TestInteractiveStream_InBandBypassEscalate_LiveChildReportsBypassMode": "pool-bypass-escalate",
	"TestInteractiveStream_SpawnPostureGate_LiveChildAcksAndTurnFlows":      "pool-posture-gate",
}

func readPoolRecordingConfig(path string) (poolRecordingConfig, error) {
	var c poolRecordingConfig
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 16384 {
		return c, os.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || poolRecordingRoots[c.Root] != c.Process.Scenario || poolRecordingRoots[c.Root] == "" {
		return c, os.ErrInvalid
	}
	return c, nil
}

func poolProviderEnvironment(t *testing.T) (string, string, func()) {
	t.Helper()
	path := os.Getenv("PYRY_POOL_RECORDING_CONFIG")
	if path == "" {
		return resolveClaudeBin(t), WithWorktreeAuthenticated(t), func() {}
	}
	c, err := readPoolRecordingConfig(path)
	if err != nil || c.Root != t.Name() || poolRecordedRevision == "" || c.Process.Producer.Revision != poolRecordedRevision {
		t.Fatal("pool recording profile does not own this original test")
	}
	if c.Process.Mode == "replay" {
		for _, key := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR"} {
			if os.Getenv(key) != "" {
				t.Fatal("pool replay inherited provider credentials/configuration")
			}
		}
	} else if c.Process.Mode != "capture" || (os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "") {
		t.Fatal("pool capture requires explicit credentials")
	}
	info, err := os.Lstat(c.Process.Home)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("pool recording home must be private")
	}
	t.Setenv("HOME", c.Process.Home)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	dir := filepath.Join(c.Process.Home, "recording-bin")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nexport PYRY_POOL_RECORDING_CONFIG=" + quote(path) + "\nexec " + quote(exe) + " -test.run=^TestPoolTapeHelperProcess$ -- \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	// Error-only version diagnostics must describe this capture, including replay.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return wrapper, c.Process.Home, func() {
		if t.Failed() {
			return
		}
		if err := os.WriteFile(c.Process.Finish, nil, 0600); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			raw, err := os.ReadFile(c.Receipt)
			if err == nil {
				var r poolRecordingReceipt
				if json.Unmarshal(raw, &r) != nil || !r.Success || r.Frames == 0 {
					t.Fatalf("pool recording did not seal: %s", r.Error)
				}
				if _, err := os.Stat(c.Process.Claim + ".duplicate"); err == nil {
					t.Fatal("unrecorded additional pool child")
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("pool recording did not seal before original runner cleanup")
	}
}

func TestPoolTapeHelperProcess(t *testing.T) {
	path := os.Getenv("PYRY_POOL_RECORDING_CONFIG")
	if path == "" {
		return
	}
	c, err := readPoolRecordingConfig(path)
	if err != nil {
		os.Exit(2)
	}
	if poolRecordedRevision == "" || c.Process.Producer.Revision != poolRecordedRevision {
		os.Exit(2)
	}
	if c.Process.Mode == "capture" {
		exe, err := os.Executable()
		if err != nil {
			os.Exit(2)
		}
		f, err := os.Open(exe)
		if err != nil {
			os.Exit(2)
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, f)
		closeErr := f.Close()
		if copyErr != nil || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != c.Process.Producer.BinarySHA256 {
			os.Exit(2)
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
		if _, err := io.WriteString(os.Stdout, c.Process.Producer.ClaudeVersion+"\n"); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	frames, err := claudetape.RunPoolProcess(c.Process, args, os.Stdin, os.Stdout, os.Stderr)
	r := poolRecordingReceipt{Success: err == nil, Frames: frames}
	if err != nil {
		r.Error = err.Error()
	}
	// An additional invocation cannot overwrite the first producer's receipt.
	if _, err := os.Stat(c.Process.Claim + ".duplicate"); err == nil {
		os.Exit(1)
	}
	raw, _ := json.Marshal(r)
	f, writeErr := os.OpenFile(c.Receipt+".pending", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if writeErr != nil {
		os.Exit(2)
	}
	_, writeErr = f.Write(raw)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil || os.Rename(c.Receipt+".pending", c.Receipt) != nil {
		os.Exit(2)
	}
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
