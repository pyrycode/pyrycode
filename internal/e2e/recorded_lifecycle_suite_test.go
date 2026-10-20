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
	"reflect"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

func runRecordedLifecycleSuite(t *testing.T, name string) {
	t.Helper()
	profile, ok := claudetape.LifecycleProfiles()[name]
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
	sourceBytes, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sourceSum := sha256.Sum256(sourceBytes)
	sourceSHA := hex.EncodeToString(sourceSum[:])
	home := shortHome(t)
	process := claudetape.PoolProcessConfig{Mode: mode, Scenario: name, Home: home, WorkDir: filepath.Join(home, "work"), Path: filepath.Join(dir, name+".json"), Claim: filepath.Join(home, "claim"), Finish: filepath.Join(home, "finish")}
	switch mode {
	case "capture":
		if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
			t.Fatal("daemon suite capture requires explicit credentials")
		}
		status, err := exec.Command("git", "status", "--porcelain").Output()
		if err != nil || len(status) != 0 {
			t.Fatal("commit source before daemon suite capture")
		}
		raw, err := exec.Command("git", "rev-parse", "HEAD").Output()
		if err != nil || strings.TrimSpace(string(raw)) != revision {
			t.Fatal("original test binary source changed")
		}
		process.Claude, err = exec.LookPath("claude")
		if err != nil {
			t.Fatal("daemon suite capture requires Claude")
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
			t.Fatal("missing reviewed daemon suite")
		}
		suite, err := claudetape.DecodeLifecycleSuite(f, profile.Roles)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		process.Producer = suite.Producer
		process.Producer.Revision = revision
	default:
		t.Fatal("unknown daemon suite mode")
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
	children := filepath.Join(home, "recording-children")
	for _, dir := range []string{children, filepath.Join(home, "recording-roles"), filepath.Join(home, "recording-bin")} {
		if os.Mkdir(dir, 0700) != nil {
			t.Fatal("cannot prepare private role inventory")
		}
	}
	if mode == "replay" {
		f, err := os.Open(process.Path)
		if err != nil {
			t.Fatal(err)
		}
		suite, err := claudetape.DecodeLifecycleSuite(f, profile.Roles)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, role := range profile.Roles {
			if suite.Roles[role].Save(filepath.Join(children, role+".json")) != nil {
				t.Fatal("cannot stage reviewed role")
			}
		}
	}
	if len(claudetape.LifecycleSnapshotRoles(name)) > 0 {
		for _, dir := range []string{"recording-snapshots", "recording-snapshot-inputs"} {
			if os.Mkdir(filepath.Join(home, dir), 0700) != nil {
				t.Fatal("cannot prepare native transcript inventory")
			}
		}
		if mode == "replay" {
			f, err := os.Open(process.Path)
			if err != nil {
				t.Fatal(err)
			}
			suite, err := claudetape.DecodeLifecycleSuite(f, profile.Roles)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			for _, role := range claudetape.LifecycleSnapshotRoles(name) {
				if suite.Snapshots[role].Save(filepath.Join(home, "recording-snapshot-inputs", role+".json"), home) != nil {
					t.Fatal("cannot stage reviewed native transcript")
				}
			}
		}
	}
	workerWaits := []*claudetape.FallbackProductionWait{}
	workerLimit := claudetape.LifecycleWorkerLimit(name)
	if workerLimit > 0 {
		if os.Mkdir(filepath.Join(home, "recording-workers"), 0700) != nil {
			t.Fatal("cannot prepare private finite workers")
		}
		if mode == "replay" {
			file, err := os.Open(process.Path)
			if err != nil {
				t.Fatal(err)
			}
			suite, err := claudetape.DecodeLifecycleSuite(file, profile.Roles)
			file.Close()
			if err != nil {
				t.Fatal(err)
			}
			for i, worker := range suite.Workers {
				workerWaits = append(workerWaits, worker.ProductionWait)
				path := strings.TrimSuffix(claudetape.FallbackWorkerJobPath(home, i+1), ".job") + ".json"
				if worker.Tape.Save(path) != nil {
					t.Fatal("cannot stage reviewed finite worker")
				}
			}
		}
	}
	configPath := filepath.Join(home, "daemon-recording.json")
	wrapper := filepath.Join(home, "recording-bin", "claude")
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\nexport PYRY_LIFECYCLE_RECORDING_CONFIG=" + quote(configPath) + "\nexec " + quote(binary) + " -test.run=^TestLifecycleTapeHelperProcess$ -- \"$@\"\n"
	if os.WriteFile(wrapper, []byte(script), 0700) != nil {
		t.Fatal("cannot prepare daemon recording wrapper")
	}
	config := struct {
		WorkerWaits                              []*claudetape.FallbackProductionWait `json:",omitempty"`
		BrokerParentPID                          int
		Process                                  claudetape.PoolProcessConfig
		Root, Receipt, Wrapper                   string
		DaemonSHA256, Daemon, SourceBinarySHA256 string
	}{workerWaits, os.Getpid(), process, profile.Root, filepath.Join(home, "receipt"), wrapper, hex.EncodeToString(daemonSum[:]), daemon, sourceSHA}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configBytes := append([]byte(nil), raw...)
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}

	var brokerJobs []string
	for _, role := range profile.Roles {
		brokerJobs = append(brokerJobs, filepath.Join(children, role+".job"))
	}
	ownedBrokers := map[string]*exec.Cmd{}
	var ownedBrokersMu sync.Mutex
	server, err := claudetape.NewLifecycleBrokerServer(filepath.Join(home, "broker.sock"), brokerJobs, func(jobPath string, peer int) (*exec.Cmd, error) {
		var job claudetape.LifecycleJob
		if claudetape.ReadLifecycleControl(jobPath, claudetape.MaxBytes, &job) != nil || claudetape.ValidateLifecycleLaunch(job, jobPath, peer, os.Getpid(), process, sourceSHA, daemon) != nil {
			return nil, os.ErrInvalid
		}
		cmd := exec.Command(binary, "-test.run=^TestLifecycleTapeBrokerProcess$", "--", jobPath)
		cmd.Env = os.Environ()
		if mode == "replay" {
			cmd.Env = recordedPoolOfflineEnv()
		}
		cmd.Env = append(cmd.Env, "PYRY_LIFECYCLE_RECORDING_CONFIG="+configPath)
		ownedBrokersMu.Lock()
		ownedBrokers[job.Role] = cmd
		ownedBrokersMu.Unlock()
		return cmd, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Join(2 * time.Second) })

	// A failed original process cannot leave a detached broker/provider behind.
	// Abort files address only these closed per-role jobs; no captured PID is used.
	t.Cleanup(func() {
		for _, role := range profile.Roles {
			prefix := filepath.Join(children, role)
			if _, e := os.Lstat(prefix + ".receipt"); e == nil {
				continue
			}
			if _, e := os.Lstat(prefix + ".job"); e != nil {
				continue
			}
			f, e := os.OpenFile(prefix+".abort", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e == nil {
				_ = f.Close()
			}
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) {
				if _, e := os.Lstat(prefix + ".receipt"); e == nil {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
	})
	var workerServer *claudetape.LifecycleBrokerServer
	workerLaunchHashes := map[string][32]byte{}
	workerProofs := map[string]*recordedWorkerCompletion{}
	var workerLaunchMu sync.Mutex
	if workerLimit > 0 {
		jobs := []string{}
		for i := 1; i <= workerLimit; i++ {
			jobs = append(jobs, claudetape.FallbackWorkerJobPath(home, i))
		}
		workerServer, err = claudetape.NewLifecycleBrokerServer(filepath.Join(home, "worker.sock"), jobs, func(path string, peer int) (*exec.Cmd, error) {
			var job claudetape.FallbackWorkerJob
			if claudetape.ReadLifecycleControl(path, 16384, &job) != nil || claudetape.ValidateFallbackWorkerLaunch(job, path, peer, os.Getpid(), process, sourceSHA) != nil {
				return nil, os.ErrInvalid
			}
			if claudetape.ValidateFallbackReplayWait(job, path, mode, home, workerWaits) != nil {
				return nil, os.ErrInvalid
			}
			launchRaw, err := os.ReadFile(path)
			var sameJob claudetape.FallbackWorkerJob
			if err != nil || len(launchRaw) > 16384 || json.Unmarshal(launchRaw, &sameJob) != nil || !reflect.DeepEqual(job, sameJob) {
				return nil, os.ErrInvalid
			}
			workerLaunchMu.Lock()
			workerLaunchHashes[path] = sha256.Sum256(launchRaw)
			workerLaunchMu.Unlock()
			cmd := exec.Command(binary, "-test.run=^TestFallbackWorkerBrokerProcess$", "--", path)
			cmd.Env = os.Environ()
			if mode == "replay" {
				cmd.Env = recordedPoolOfflineEnv()
			}
			cmd.Env = append(cmd.Env, "PYRY_LIFECYCLE_RECORDING_CONFIG="+configPath)
			proof := &recordedWorkerCompletion{cmd: cmd}
			cmd.Stdout = proof
			workerLaunchMu.Lock()
			workerProofs[path] = proof
			workerLaunchMu.Unlock()
			return cmd, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = workerServer.Join(2 * time.Second) })
		t.Cleanup(func() {
			for i := 1; i <= workerLimit; i++ {
				path := strings.TrimSuffix(claudetape.FallbackWorkerJobPath(home, i), ".job") + ".abort"
				file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err == nil {
					file.Close()
				}
			}
		})
	}
	var launchAudit *claudetape.FallbackLaunchAudit
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "tool", "test2json", "-t", "-p", "daemon-recording", binary, "-test.v", "-test.run=^"+regexp.QuoteMeta(profile.Root)+"$", "-test.timeout=20m")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = os.Environ()
	if mode == "replay" {
		cmd.Env = recordedPoolOfflineEnv()
	}
	cmd.Env = append(cmd.Env, "PYRY_LIFECYCLE_RECORDING_CONFIG="+configPath, "PYRY_E2E_BIN="+daemon, "PATH="+filepath.Dir(wrapper)+string(os.PathListSeparator)+os.Getenv("PATH"))
	stream, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal("cannot run original daemon assertions")
	}
	if name == "session-resume" {
		launchAudit = claudetape.StartFallbackLaunchAudit(home, name, binary, revision, sourceSHA, os.Getpid(), cmd.Process.Pid)
		defer launchAudit.Stop()
	}
	evidenceDir := os.Getenv("PYRY_CLAUDE_TAPE_EVIDENCE")
	if evidenceDir == "" {
		evidenceDir = home
	}
	if err := os.MkdirAll(evidenceDir, 0700); err != nil {
		t.Fatal(err)
	}
	if launchAudit != nil {
		defer func() {
			if err := launchAudit.Save(filepath.Join(evidenceDir, name+"-launch-audit.json")); err != nil {
				t.Errorf("cannot retain launch audit: %v", err)
			}
		}()
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
		if raw, readErr := os.ReadFile(filepath.Join(home, "recording-unexpected")); readErr == nil && len(raw) < 4096 && claudetape.Safe(raw) == nil {
			t.Logf("daemon unexpected invocation: %s", raw)
		}
		// Preserve bounded helper failures before the disposable HOME is removed.
		// Native transcript bytes and operator data never enter this diagnostic.
		for _, role := range profile.Roles {
			var r struct {
				Success bool   `json:"success"`
				Frames  int    `json:"frames"`
				Error   string `json:"error"`
			}
			raw, readErr := os.ReadFile(filepath.Join(children, role+".receipt"))
			if readErr == nil && len(raw) < 16384 && json.Unmarshal(raw, &r) == nil && len(r.Error) < 512 && claudetape.Safe([]byte(r.Error)) == nil {
				t.Logf("daemon helper role=%s success=%t frames=%d error=%q", role, r.Success, r.Frames, r.Error)
			}
		}
		if mode == "capture" && claudetape.PhysicalFamily(name) != "" {
			// The detached broker publishes failure evidence after native EOF.
			// Join actual current children before reading, even on a failed root.
			_ = server.Join(15 * time.Second)
			var failed claudetape.PhysicalFailedCapture
			path := filepath.Join(children, "bootstrap.failed-native")
			if claudetape.ReadLifecycleControl(path, claudetape.MaxBytes, &failed) == nil && failed.Kind == "failed-unpromotable-native-observation-v1" && failed.Scenario == name+"-bootstrap" && failed.Revision == revision && failed.SourceBinarySHA256 == sourceSHA {
				destination := filepath.Join(evidenceDir, name+"-failed-unpromotable-native.json")
				if claudetape.PublishPhysicalFailedCapture(destination, failed) == nil {
					t.Log("failed native observation retained privately; supplies no fixture coverage")
				}
			}
		}
		t.Fatalf("original daemon assertions failed: mode=%s root=%s runs=%d passed=%d; private evidence=%s", mode, profile.Root, runs, passed, evidence.Name())
	}
	var receipt struct {
		Success        bool
		Frames         int
		ArtifactSHA256 string
	}

	if claudetape.ReadLifecycleControl(config.Receipt, 16384, &receipt) != nil || !receipt.Success || receipt.Frames != len(profile.Roles) {
		t.Fatal("original lifecycle assertions lack complete sealed proof")
	}
	for _, v := range []struct {
		path string
		mode os.FileMode
		want []byte
	}{{configPath, 0600, configBytes}, {wrapper, 0700, []byte(script)}} {
		info, e := os.Lstat(v.path)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != v.mode || info.Size() > claudetape.MaxBytes {
			t.Fatal("lifecycle control changed")
		}
		f, e := os.Open(v.path)
		if e != nil {
			t.Fatal(e)
		}
		opened, se := f.Stat()
		raw, re := io.ReadAll(io.LimitReader(f, claudetape.MaxBytes+1))
		ce := f.Close()
		if se != nil || !os.SameFile(info, opened) || re != nil || ce != nil || !bytes.Equal(raw, v.want) {
			t.Fatal("lifecycle control bytes changed during original cleanup")
		}
	}
	daemonAfter, e := os.ReadFile(daemon)
	afterSum := sha256.Sum256(daemonAfter)
	if e != nil || afterSum != daemonSum {
		t.Fatal("lifecycle daemon image changed")
	}
	if err := server.Join(15 * time.Second); err != nil {
		t.Fatal(err)
	}
	// Detached brokers close after provider EOF, independently of the original
	// root's test2json exit. Join their bounded terminal publication before the
	// immutable final audit; process exit alone is not a receipt barrier.
	deadline := time.Now().Add(15 * time.Second)
	for _, role := range profile.Roles {
		for {
			var terminal claudetape.LifecycleReceipt
			if claudetape.ReadLifecycleControl(filepath.Join(children, role+".receipt"), 16384, &terminal) == nil {
				break
			}
			if !time.Now().Before(deadline) {
				t.Fatal("lifecycle terminal publication timed out after original cleanup")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	brokerPIDs := map[string]int{}
	for role, cmd := range ownedBrokers {
		if cmd.Process != nil {
			brokerPIDs[role] = cmd.Process.Pid
		}
	}
	if err := claudetape.ValidatePhysicalBrokerOwnership(home, name, brokerPIDs); err != nil {
		t.Fatal(err)
	}
	if err := claudetape.ValidateConversationBrokerOwnership(home, name, brokerPIDs); err != nil {
		t.Fatal(err)
	}
	digest, tapes, err := claudetape.AuditLifecycleArtifacts(home, name, revision, sourceSHA, config.DaemonSHA256, os.Getpid(), true)
	if err != nil || digest != receipt.ArtifactSHA256 {
		for _, role := range profile.Roles {
			var terminal claudetape.LifecycleReceipt
			readErr := claudetape.ReadLifecycleControl(filepath.Join(children, role+".receipt"), 16384, &terminal)
			t.Logf("lifecycle terminal role=%s readable=%t success=%t frames=%d", role, readErr == nil, terminal.Success, terminal.Frames)
		}
		t.Fatalf("lifecycle role evidence changed during original cleanup: audit=%v digest_matches=%t", err, digest == receipt.ArtifactSHA256)
	}
	if workerServer != nil {
		if err := workerServer.Join(15 * time.Second); err != nil {
			for _, proof := range workerProofs {
				decoder := json.NewDecoder(bytes.NewReader(proof.raw))
				for {
					var record json.RawMessage
					if decoder.Decode(&record) != nil {
						break
					}
					var diagnostic struct{ WorkerError string }
					if len(record) < 1024 && json.Unmarshal(record, &diagnostic) == nil && diagnostic.WorkerError != "" && len(diagnostic.WorkerError) < 256 && claudetape.Safe([]byte(diagnostic.WorkerError)) == nil {
						t.Log(diagnostic.WorkerError)
					}
				}
			}
			t.Fatal(err)
		}
	}
	workers, err := claudetape.ReadLifecycleWorkers(home, name, mode, revision, sourceSHA, os.Getpid(), tapes, launchAudit)
	if err != nil {
		destination := filepath.Join(evidenceDir, name+"-invalid-worker-diagnostic.json")
		if diagnosticErr := claudetape.SaveLifecycleWorkerDiagnostic(home, name, destination, tapes); diagnosticErr == nil {
			t.Log("validated invalid/unpromoted worker carriers retained privately")
		}
		t.Fatal(err)
	}
	if len(workerLaunchHashes) != len(workers) {
		t.Fatal("finite worker attempts do not match kernel-owned launches")
	}
	for path, proof := range workerProofs {
		if proof.overflow || proof.cmd.Process == nil || claudetape.ValidateFallbackWorkerCompletion(proof.raw, proof.cmd.Process.Pid, path) != nil {
			t.Fatal("finite worker completion differs from actual owned broker")
		}
	}
	for path, want := range workerLaunchHashes {
		raw, err := os.ReadFile(path)
		if err != nil || sha256.Sum256(raw) != want {
			t.Fatal("finite worker launch changed during original cleanup")
		}
	}
	if mode == "replay" {
		file, err := os.Open(process.Path)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := claudetape.DecodeLifecycleSuite(file, profile.Roles)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !(len(saved.Workers) == 0 && len(workers) == 0) && !reflect.DeepEqual(saved.Workers, workers) {
			t.Fatal("replay finite worker inventory changed")
		}
	}
	if mode == "capture" {
		snapshots, _, err := claudetape.ReadLifecycleSnapshots(home, name, revision, sourceSHA)
		if err != nil {
			t.Fatal(err)
		}
		physical, err := claudetape.ReadPhysicalScenarioContract(home, name)
		if err != nil {
			t.Fatal(err)
		}
		suite := &claudetape.LifecycleSuite{Physical: physical, Version: 1, Scenario: name, Producer: process.Producer, DaemonSHA256: config.DaemonSHA256, Roles: tapes, Snapshots: snapshots, Workers: workers, Settlement: claudetape.RequiredConversationSettlement(name)}
		if err := suite.Save(process.Path, profile.Roles); err != nil {
			t.Fatalf("cannot publish complete original lifecycle capture: %v", err)
		}
	}

	t.Logf("mode=%s scenario=%s original_daemon_roots=1 child_roles=%d", mode, name, len(profile.Roles))
}

type recordedWorkerCompletion struct {
	cmd      *exec.Cmd
	raw      []byte
	overflow bool
}

func (p *recordedWorkerCompletion) Write(raw []byte) (int, error) {
	n := len(raw)
	if n > 16384-len(p.raw) {
		p.overflow = true
	} else {
		p.raw = append(p.raw, raw...)
	}
	return n, nil
}
