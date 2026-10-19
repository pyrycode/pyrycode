//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

type recordedPoolProfile struct{ Root, Workdir string }

var recordedPoolProfiles = map[string]recordedPoolProfile{
	"pool-model-change":    {"TestInteractiveStream_InBandModelChange_LiveChildReportsNewModel", "inband-model-work"},
	"pool-bypass-revoke":   {"TestInteractiveStream_InBandBypassRevoke_LiveChildReportsDefaultMode", "inband-bypass-revoke-work"},
	"pool-bypass-escalate": {"TestInteractiveStream_InBandBypassEscalate_LiveChildReportsBypassMode", "inband-bypass-revoke-work-2066"},
	"pool-posture-gate":    {"TestInteractiveStream_SpawnPostureGate_LiveChildAcksAndTurnFlows", "inband-bypass-revoke-work-2064"},
}
var recordedPoolBinary struct {
	sync.Once
	path, revision string
	err            error
}

func buildRecordedPoolBinary(t *testing.T) (string, string) {
	t.Helper()
	recordedPoolBinary.Do(func() {
		raw, err := exec.Command("git", "rev-parse", "HEAD").Output()
		if err != nil {
			recordedPoolBinary.err = err
			return
		}
		recordedPoolBinary.revision = strings.TrimSpace(string(raw))
		dir := t.TempDir()
		recordedPoolBinary.path = filepath.Join(dir, "pool.test")
		cmd := exec.Command("go", "test", "-c", "-race", "-tags=e2e_realclaude", "-ldflags=-X github.com/pyrycode/pyrycode/internal/e2e/realclaude.poolRecordedRevision="+recordedPoolBinary.revision, "-o", recordedPoolBinary.path, "./realclaude")
		cmd.Env = recordedPoolOfflineEnv()
		log, err := os.OpenFile(filepath.Join(dir, "compiler.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			recordedPoolBinary.err = err
			return
		}
		cmd.Stdout, cmd.Stderr = log, log
		recordedPoolBinary.err = cmd.Run()
		_ = log.Close()
		if recordedPoolBinary.err != nil {
			return
		}
		// Verify the linked source stamp through the actual helper entrypoint.
		control := struct {
			Process       claudetape.PoolProcessConfig
			Root, Receipt string
		}{
			Process: claudetape.PoolProcessConfig{Mode: "replay", Scenario: "pool-model-change", Producer: claudetape.Producer{Revision: recordedPoolBinary.revision, ClaudeVersion: "pool-build-stamp"}},
			Root:    recordedPoolProfiles["pool-model-change"].Root,
		}
		raw, err = json.Marshal(control)
		if err != nil {
			recordedPoolBinary.err = err
			return
		}
		config := filepath.Join(dir, "stamp-control.json")
		if err := os.WriteFile(config, raw, 0600); err != nil {
			recordedPoolBinary.err = err
			return
		}
		probe := exec.Command(recordedPoolBinary.path, "-test.run=^TestPoolTapeHelperProcess$", "--", "--version")
		probe.Env = append(recordedPoolOfflineEnv(), "PYRY_POOL_RECORDING_CONFIG="+config)
		raw, recordedPoolBinary.err = probe.Output()
		if recordedPoolBinary.err == nil && string(raw) != "pool-build-stamp\n" {
			recordedPoolBinary.err = os.ErrInvalid
		}
	})
	if recordedPoolBinary.err != nil {
		t.Fatalf("cannot compile original pool test assertions; private compiler log beside %s", recordedPoolBinary.path)
	}
	return recordedPoolBinary.path, recordedPoolBinary.revision
}
func recordedPoolOfflineEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if key == "CLAUDE_CODE_OAUTH_TOKEN" || key == "ANTHROPIC_API_KEY" || key == "CLAUDE_CONFIG_DIR" || key == "PYRY_POOL_RECORDING_CONFIG" {
			continue
		}
		env = append(env, kv)
	}
	return env
}
func runRecordedPool(t *testing.T, name string) {
	t.Helper()
	profile, ok := recordedPoolProfiles[name]
	if !ok {
		t.Fatal("unknown pool recording profile")
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
	process := claudetape.PoolProcessConfig{Mode: mode, Scenario: name, Home: home, WorkDir: filepath.Join(home, profile.Workdir), Path: filepath.Join(dir, name+".json"), Claim: filepath.Join(home, "claim"), Finish: filepath.Join(home, "finish")}
	switch mode {
	case "capture":
		if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
			t.Fatal("pool capture requires explicit credentials")
		}
		status, err := exec.Command("git", "status", "--porcelain").Output()
		if err != nil || len(status) != 0 {
			t.Fatal("commit source before pool capture")
		}
		raw, err := exec.Command("git", "rev-parse", "HEAD").Output()
		if err != nil || strings.TrimSpace(string(raw)) != revision {
			t.Fatal("pool test binary source changed")
		}
		process.Claude, err = exec.LookPath("claude")
		if err != nil {
			t.Fatal("pool capture requires Claude")
		}
		process.Claude, err = filepath.Abs(process.Claude)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		version, err := exec.CommandContext(ctx, process.Claude, "--version").Output()
		if err != nil {
			t.Fatal("cannot read pool provider version")
		}
		raw, err = os.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		process.Producer = claudetape.Producer{Revision: revision, BinarySHA256: hex.EncodeToString(sum[:]), ClaudeVersion: strings.TrimSpace(string(version)), CapturedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	case "replay":
		f, err := os.Open(process.Path)
		if err != nil {
			t.Fatal("missing reviewed pool tape")
		}
		tape, err := claudetape.Decode(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		process.Producer = tape.Producer
		process.Producer.Revision = revision
	default:
		t.Fatal("unknown pool recording mode")
	}
	config := struct {
		Process       claudetape.PoolProcessConfig
		Root, Receipt string
	}{process, profile.Root, filepath.Join(home, "receipt")}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, "pool-config.json")
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "tool", "test2json", "-t", "-p", "pool-recording", binary, "-test.v", "-test.run=^"+regexp.QuoteMeta(profile.Root)+"$", "-test.timeout=20m")
	cmd.Env = os.Environ()
	if mode == "replay" {
		cmd.Env = recordedPoolOfflineEnv()
	}
	cmd.Env = append(cmd.Env, "PYRY_POOL_RECORDING_CONFIG="+configPath)
	stream, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal("cannot run original pool assertions")
	}
	evidenceDir := os.Getenv("PYRY_CLAUDE_TAPE_EVIDENCE")
	if evidenceDir == "" {
		evidenceDir = home
	}
	if err := os.MkdirAll(evidenceDir, 0700); err != nil {
		t.Fatal(err)
	}
	evidence, err := os.OpenFile(filepath.Join(evidenceDir, name+"-"+mode+".jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer evidence.Close()
	scan := bufio.NewScanner(stream)
	scan.Buffer(make([]byte, 4096), claudetape.MaxBytes)
	runs, passed, rejected := 0, 0, false
	for scan.Scan() {
		line := scan.Bytes()
		if _, err := evidence.Write(append(append([]byte(nil), line...), '\n')); err != nil {
			t.Fatal(err)
		}
		var event struct{ Action, Test string }
		if json.Unmarshal(line, &event) != nil {
			rejected = true
			continue
		}
		if event.Action == "fail" || event.Action == "skip" {
			rejected = true
		}
		if event.Test == profile.Root {
			if event.Action == "run" {
				runs++
			}
			if event.Action == "pass" {
				passed++
			}
		}
	}
	err = cmd.Wait()
	if err != nil || scan.Err() != nil || ctx.Err() != nil || rejected || runs != 1 || passed != 1 {
		t.Fatalf("original pool assertions failed: mode=%s root=%s runs=%d passed=%d; private evidence=%s", mode, profile.Root, runs, passed, evidence.Name())
	}
	if _, err := os.Stat(process.Claim + ".duplicate"); err == nil {
		t.Fatal("additional pool child escaped the original assertions")
	}
	t.Logf("mode=%s scenario=%s original_pool_roots=1", mode, name)
}
