//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

func runRecordedCLI(t *testing.T, name string) {
	t.Helper()
	root, ok := claudetape.CLIProfiles()[name]
	if !ok {
		t.Fatal("unknown CLI recording profile")
	}
	mode := os.Getenv("PYRY_CLAUDE_TAPE_MODE")
	if mode == "" {
		mode = "replay"
	}
	dir := os.Getenv("PYRY_CLAUDE_TAPE_DIR")
	if dir == "" {
		dir = "testdata/claude-tapes"
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	binary, revision := buildRecordedPoolBinary(t)
	home := shortHome(t)
	meta := filepath.Join(home, claudetape.CLIMetadataDirectory)
	if os.Mkdir(meta, 0700) != nil {
		t.Fatal("cannot create private CLI metadata")
	}
	destination := filepath.Join(dir, name+".json")
	process := claudetape.CLIProcessConfig{Mode: mode, Scenario: name, Home: home, Path: filepath.Join(meta, "tape.json"), Claim: filepath.Join(meta, "claim")}
	switch mode {
	case "capture":
		if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
			t.Fatal("CLI capture requires explicit credentials")
		}
		status, err := exec.Command("git", "status", "--porcelain").Output()
		if err != nil || len(status) != 0 {
			t.Fatal("commit source before CLI capture")
		}
		raw, err := exec.Command("git", "rev-parse", "HEAD").Output()
		if err != nil || strings.TrimSpace(string(raw)) != revision {
			t.Fatal("original test binary source changed")
		}
		process.Claude, err = exec.LookPath("claude")
		if err != nil {
			t.Fatal("CLI capture requires Claude")
		}
		process.Claude, err = filepath.Abs(process.Claude)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		version, err := exec.CommandContext(ctx, process.Claude, "--version").Output()
		if err != nil {
			t.Fatal("cannot read CLI provider version")
		}
		raw, err = os.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		process.Producer = claudetape.Producer{Revision: revision, BinarySHA256: hex.EncodeToString(sum[:]), ClaudeVersion: strings.TrimSpace(string(version)), CapturedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	case "replay":
		f, err := os.Open(destination)
		if err != nil {
			t.Fatal("missing reviewed CLI")
		}
		tape, err := claudetape.DecodeCLI(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		process.Producer = tape.Producer
	default:
		t.Fatal("unknown CLI mode")
	}

	daemon := ensurePyryBuilt(t)
	daemonBytes, err := os.ReadFile(daemon)
	if err != nil {
		t.Fatal(err)
	}
	daemonSum := sha256.Sum256(daemonBytes)
	if mode == "capture" {
		info, err := buildinfo.ReadFile(daemon)
		if err != nil {
			t.Fatal("daemon build stamp unavailable")
		}
		settings := map[string]string{}
		for _, v := range info.Settings {
			settings[v.Key] = v.Value
		}
		if settings["vcs.revision"] != revision || settings["vcs.modified"] != "false" {
			t.Fatal("daemon does not match capture source")
		}
	}

	process.DaemonSHA256 = hex.EncodeToString(daemonSum[:])
	if mode == "replay" {
		f, err := os.Open(destination)
		if err != nil {
			t.Fatal(err)
		}
		tape, err := claudetape.DecodeCLI(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if tape.Save(process.Path) != nil {
			t.Fatal("cannot stage reviewed CLI recording")
		}
	}
	configPath := filepath.Join(meta, "config.json")
	wrapper := filepath.Join(meta, "claude")
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\nexport PYRY_CLI_RECORDING_CONFIG=" + quote(configPath) + "\nexec " + quote(binary) + " -test.run=^TestCLITapeHelperProcess$ -- \"$@\"\n"
	if os.WriteFile(wrapper, []byte(script), 0700) != nil {
		t.Fatal("cannot create CLI wrapper")
	}
	config := struct {
		Process                         claudetape.CLIProcessConfig
		Root, Revision, Wrapper, Daemon string
	}{process, root, revision, wrapper, daemon}
	configRaw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if os.WriteFile(configPath, configRaw, 0600) != nil {
		t.Fatal("cannot create CLI config")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "tool", "test2json", "-t", "-p", "cli-recording", binary, "-test.v", "-test.run=^"+regexp.QuoteMeta(root)+"$", "-test.timeout=20m")
	cmd.Env = os.Environ()
	if mode == "replay" {
		cmd.Env = recordedPoolOfflineEnv()
	}
	cmd.Env = append(cmd.Env, "PYRY_CLI_RECORDING_CONFIG="+configPath, "PYRY_E2E_BIN="+daemon, "PATH="+meta+string(os.PathListSeparator)+os.Getenv("PATH"))
	stream, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if cmd.Start() != nil {
		t.Fatal("cannot start original CLI root")
	}
	evidenceDir := os.Getenv("PYRY_CLAUDE_TAPE_EVIDENCE")
	if evidenceDir == "" {
		evidenceDir = t.TempDir()
	}
	if os.MkdirAll(evidenceDir, 0700) != nil {
		t.Fatal("cannot create CLI evidence directory")
	}
	evidence, err := os.OpenFile(filepath.Join(evidenceDir, name+"-"+mode+".jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer evidence.Close()
	scan := bufio.NewScanner(stream)
	scan.Buffer(make([]byte, 4096), claudetape.MaxBytes)
	runs, passes, rejected := 0, 0, false
	for scan.Scan() {
		line := scan.Bytes()
		if _, err := evidence.Write(append(append([]byte(nil), line...), '\n')); err != nil {
			t.Fatal(err)
		}
		var e struct{ Action, Test string }
		if json.Unmarshal(line, &e) != nil {
			rejected = true
			continue
		}
		if e.Action == "fail" || e.Action == "skip" {
			rejected = true
		}
		if e.Test == root {
			if e.Action == "run" {
				runs++
			}
			if e.Action == "pass" {
				passes++
			}
		}
	}
	waitErr := cmd.Wait()
	if waitErr != nil || scan.Err() != nil || ctx.Err() != nil || rejected || runs != 1 || passes != 1 {
		t.Fatalf("original CLI assertions failed: root=%s runs=%d passes=%d; private evidence=%s", root, runs, passes, evidence.Name())
	}
	// Exactly one invocation and no late helper artifacts, after all original
	// assertions and cleanup. Pin configuration and wrapper alongside the tape.
	entries, err := os.ReadDir(meta)
	if err != nil || len(entries) != 5 {
		t.Fatal("CLI artifact inventory changed")
	}
	expected := map[string]os.FileMode{"config.json": 0600, "claude": 0700, "claim": 0600, "tape.json": 0600, "receipt.json": 0600}
	artifacts := map[string][]byte{}
	for _, entry := range entries {
		mode, ok := expected[entry.Name()]
		info, err := os.Lstat(filepath.Join(meta, entry.Name()))
		if !ok || err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != mode || info.Size() > claudetape.MaxBytes {
			t.Fatal("CLI artifact ownership changed")
		}
		f, err := os.Open(filepath.Join(meta, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		opened, statErr := f.Stat()
		raw, readErr := io.ReadAll(io.LimitReader(f, claudetape.MaxBytes+1))
		closeErr := f.Close()
		if statErr != nil || !os.SameFile(info, opened) || readErr != nil || closeErr != nil || len(raw) > claudetape.MaxBytes {
			t.Fatal("CLI artifact read changed")
		}
		artifacts[entry.Name()] = raw
	}
	if string(artifacts["config.json"]) != string(configRaw) || string(artifacts["claude"]) != script || len(artifacts["claim"]) != 0 {
		t.Fatal("CLI control artifacts changed")
	}
	var receipt struct {
		Success bool
		Frames  int
		SHA256  string
	}
	d := json.NewDecoder(bytes.NewReader(artifacts["receipt.json"]))
	d.DisallowUnknownFields()
	if d.Decode(&receipt) != nil || d.Decode(new(any)) != io.EOF || !receipt.Success {
		t.Fatal("missing successful CLI receipt")
	}
	tape, err := claudetape.DecodeCLI(strings.NewReader(string(artifacts["tape.json"])))
	sum := sha256.Sum256(artifacts["tape.json"])
	if err != nil || tape.Scenario != name || receipt.Frames != len(tape.Frames) || receipt.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("CLI final recording changed")
	}
	if mode == "capture" {
		if tape.Save(destination) != nil {
			t.Fatal("cannot publish CLI candidate after original assertions")
		}
	}
	t.Logf("mode=%s scenario=%s original_cli_roots=1 frames=%d native_exit=%d", mode, name, len(tape.Frames), tape.ExitCode)
}
