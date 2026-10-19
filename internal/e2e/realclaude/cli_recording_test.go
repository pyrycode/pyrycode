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
	"testing"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

type cliRecordingConfig struct {
	Process                         claudetape.CLIProcessConfig
	Root, Revision, Wrapper, Daemon string
}
type cliRecordingReceipt struct {
	Success bool
	Frames  int
	SHA256  string
}

func readCLIRecordingConfig(path string) (cliRecordingConfig, error) {
	var c cliRecordingConfig
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 16384 {
		return c, os.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || claudetape.CLIProfiles()[c.Process.Scenario] != c.Root || c.Root == "" || c.Revision == "" || c.Revision != poolRecordedRevision {
		return c, os.ErrInvalid
	}
	return c, nil
}
func cliRecordingCurrent(t *testing.T) (cliRecordingConfig, bool) {
	t.Helper()
	path := os.Getenv("PYRY_CLI_RECORDING_CONFIG")
	if path == "" {
		return cliRecordingConfig{}, false
	}
	c, err := readCLIRecordingConfig(path)
	if err != nil || c.Root != t.Name() {
		t.Fatal("CLI recording does not own this original root")
	}
	if c.Process.Mode == "replay" {
		for _, key := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR"} {
			if os.Getenv(key) != "" {
				t.Fatal("CLI replay inherited provider configuration")
			}
		}
	} else if c.Process.Mode != "capture" || os.Getenv("CLAUDE_CONFIG_DIR") != "" || (os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "") {
		t.Fatal("CLI capture lacks explicit credentials")
	}
	return c, true
}
func cliRecordingHome(t *testing.T) string {
	t.Helper()
	c, ok := cliRecordingCurrent(t)
	if !ok {
		return recoveryRecordingHome(t)
	}
	info, err := os.Lstat(c.Process.Home)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("CLI HOME is not private")
	}
	return c.Process.Home
}
func cliRecordingRunEnvironment(t *testing.T, opts RunOpts, bin string) []string {
	t.Helper()
	c, ok := cliRecordingCurrent(t)
	if !ok {
		return opts.ExtraEnv
	}
	if opts.UseTestBinaryAsFakePyry || opts.Workdir != c.Process.Home || bin != c.Daemon {
		t.Fatal("CLI recording launch escaped original binary or HOME")
	}
	raw, err := os.ReadFile(bin)
	sum := sha256.Sum256(raw)
	if err != nil || hex.EncodeToString(sum[:]) != c.Process.DaemonSHA256 {
		t.Fatal("CLI original binary changed")
	}
	for _, entry := range opts.ExtraEnv {
		for _, key := range []string{"HOME=", "PYRY_CLAUDE_BIN=", "CLAUDE_CONFIG_DIR=", "CLAUDE_CODE_OAUTH_TOKEN=", "ANTHROPIC_API_KEY="} {
			if strings.HasPrefix(entry, key) {
				t.Fatal("CLI recording environment override")
			}
		}
	}
	return append(append([]string(nil), opts.ExtraEnv...), "PYRY_CLAUDE_BIN="+c.Wrapper)
}
func TestCLITapeHelperProcess(t *testing.T) {
	path := os.Getenv("PYRY_CLI_RECORDING_CONFIG")
	if path == "" {
		return
	}
	c, err := readCLIRecordingConfig(path)
	if err != nil {
		os.Exit(2)
	}
	meta := filepath.Join(c.Process.Home, claudetape.CLIMetadataDirectory)
	fail := func() { _ = os.WriteFile(filepath.Join(meta, "unexpected"), nil, 0600); os.Exit(2) }
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
	c.Process.SnapshotPath = func(id string) string { return jsonlPathFor(c.Process.Home, id) }
	code, err := claudetape.RunCLIProcess(c.Process, args, os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fail()
	}
	raw, err := os.ReadFile(c.Process.Path)
	if err != nil {
		fail()
	}
	tape, err := claudetape.DecodeCLI(bytes.NewReader(raw))
	if err != nil {
		fail()
	}
	sum := sha256.Sum256(raw)
	if daemonRecordingWrite(filepath.Join(meta, "receipt.json"), cliRecordingReceipt{Success: true, Frames: len(tape.Frames), SHA256: hex.EncodeToString(sum[:])}) != nil {
		fail()
	}
	os.Exit(code)
}
