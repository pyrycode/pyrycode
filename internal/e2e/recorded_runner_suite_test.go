//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
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

func runRecordedRunnerSuite(t *testing.T, name string) {
	t.Helper()
	profile, ok := claudetape.RunnerProfiles()[name]
	if !ok {
		t.Fatal("unknown direct runner recording profile")
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
	destination := filepath.Join(dir, name+".json")
	process := claudetape.PoolProcessConfig{Mode: mode, Scenario: name, Home: home}
	switch mode {
	case "capture":
		if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
			t.Fatal("runner suite capture requires explicit credentials")
		}
		status, err := exec.Command("git", "status", "--porcelain").Output()
		if err != nil || len(status) != 0 {
			t.Fatal("commit source before runner suite capture")
		}
		raw, err := exec.Command("git", "rev-parse", "HEAD").Output()
		if err != nil || strings.TrimSpace(string(raw)) != revision {
			t.Fatal("original test binary source changed")
		}
		process.Claude, err = exec.LookPath("claude")
		if err != nil {
			t.Fatal("runner suite capture requires Claude")
		}
		process.Claude, err = filepath.Abs(process.Claude)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		version, err := exec.CommandContext(ctx, process.Claude, "--version").Output()
		if err != nil {
			t.Fatal("cannot read runner provider version")
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
			t.Fatal("missing reviewed runner suite")
		}
		suite, err := claudetape.DecodeRunnerSuite(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		process.Producer = suite.Producer
	default:
		t.Fatal("unknown runner suite mode")
	}

	meta := filepath.Join(home, claudetape.RunnerMetadataDirectory)
	if os.Mkdir(meta, 0700) != nil {
		t.Fatal("cannot prepare private runner metadata")
	}
	if mode == "replay" {
		f, err := os.Open(destination)
		if err != nil {
			t.Fatal(err)
		}
		suite, err := claudetape.DecodeRunnerSuite(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		for role := range profile.Roles {
			if suite.Roles[role].Save(filepath.Join(meta, role+".json")) != nil {
				t.Fatal("cannot stage reviewed runner role")
			}
		}
	}
	configPath := filepath.Join(meta, "config.json")
	wrapper := filepath.Join(meta, "claude")
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\nexport PYRY_RUNNER_RECORDING_CONFIG=" + quote(configPath) + "\nexec " + quote(binary) + " -test.run=^TestRunnerTapeHelperProcess$ -- \"$@\"\n"
	if os.WriteFile(wrapper, []byte(script), 0700) != nil {
		t.Fatal("cannot create runner wrapper")
	}
	config := struct {
		Process                 claudetape.PoolProcessConfig
		Root, Revision, Wrapper string
	}{process, profile.Root, revision, wrapper}
	configRaw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if os.WriteFile(configPath, configRaw, 0600) != nil {
		t.Fatal("cannot create runner config")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "tool", "test2json", "-t", "-p", "runner-recording", binary, "-test.v", "-test.run=^"+regexp.QuoteMeta(profile.Root)+"$", "-test.timeout=20m")
	cmd.Env = os.Environ()
	if mode == "replay" {
		cmd.Env = recordedPoolOfflineEnv()
	}
	cmd.Env = append(cmd.Env, "PYRY_RUNNER_RECORDING_CONFIG="+configPath, "PATH="+meta+string(os.PathListSeparator)+os.Getenv("PATH"))
	stream, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if cmd.Start() != nil {
		t.Fatal("cannot run original runner root")
	}
	evidenceDir := os.Getenv("PYRY_CLAUDE_TAPE_EVIDENCE")
	if evidenceDir == "" {
		evidenceDir = t.TempDir()
	}
	if os.MkdirAll(evidenceDir, 0700) != nil {
		t.Fatal("cannot create runner evidence directory")
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
		if e.Test == profile.Root {
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
		t.Fatalf("original runner assertions failed: root=%s runs=%d passes=%d; private evidence=%s", profile.Root, runs, passes, evidence.Name())
	}
	// Control files are private, bounded, same-inode and identical to the parent
	// launch. Every role's immutable seal is audited after original cleanup.
	for filename, want := range map[string][]byte{"config.json": configRaw, "claude": []byte(script)} {
		path := filepath.Join(meta, filename)
		info, err := os.Lstat(path)
		perm := os.FileMode(0600)
		if filename == "claude" {
			perm = 0700
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != perm || info.Size() > 16384 {
			t.Fatal("runner control artifact changed")
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		opened, statErr := f.Stat()
		raw, readErr := io.ReadAll(io.LimitReader(f, 16385))
		closeErr := f.Close()
		if statErr != nil || !os.SameFile(info, opened) || readErr != nil || closeErr != nil || !bytes.Equal(raw, want) {
			t.Fatal("runner control bytes changed")
		}
	}
	roles, err := claudetape.AuditRunnerSuiteArtifacts(home, name)
	if err != nil {
		t.Fatal(err)
	}
	suite := &claudetape.RunnerSuite{Version: 1, Scenario: name, Producer: process.Producer, RunnerSHA256: process.Producer.BinarySHA256, Roles: roles}
	if err := suite.Validate(); err != nil {
		t.Fatal(err)
	}
	if mode == "capture" {
		if suite.Save(destination) != nil {
			t.Fatal("cannot publish complete runner capture")
		}
	}
	t.Logf("mode=%s scenario=%s original_runner_roots=1 roles=%d", mode, name, len(profile.Roles))
}
