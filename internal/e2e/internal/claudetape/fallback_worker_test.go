package claudetape

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fallbackWorkerControlArgs() []string {
	return []string{"--print", "--model", "haiku", "--fallback-model", "", "--max-turns", "1", "--output-format", "stream-json", "--verbose", "--tools", "", "--disable-slash-commands", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--setting-sources", "", "--settings", `{"disableAllHooks":true,"autoMemoryEnabled":false}`, "--no-session-persistence", "--system-prompt", "A complete fixed worker system prompt."}
}
func fallbackWorkerControlTape() *FallbackWorkerTape {
	return &FallbackWorkerTape{DirectoryMode: 0700, DirectoryEmpty: true, Version: 1, Producer: lifecycleSuiteControl().Producer, Args: fallbackWorkerControlArgs(), Input: `{"assistant":"Would you like a Go test?","user":"Please ask me one question."}`, InputEOF: true, StdoutEOF: true, StderrEOF: true, Frames: []CLIFrame{{"stdout", "{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"Yes, please add the test.\"}\n"}}, Status: FallbackWorkerStatus{ExitCode: 0}}
}
func TestFallbackWorkerClosedCarrier(t *testing.T) {
	good := fallbackWorkerControlTape()
	raw, _ := json.MarshalIndent(good, "", "  ")
	if _, err := DecodeFallbackWorker(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"public-cwd", "nonempty-cwd", "stdin-open", "stdout-open", "stderr-open", "unknown-arg", "duplicate-arg", "tools", "hook", "missing-prompt", "invalid-utf8-prompt", "wrong-status", "unknown-signal", "bad-stdout", "credential-stderr", "duplicate-input", "extra-input"} {
		t.Run(kind, func(t *testing.T) {
			var tape FallbackWorkerTape
			_ = json.Unmarshal(raw, &tape)
			switch kind {
			case "public-cwd":
				tape.DirectoryMode = 0755
			case "nonempty-cwd":
				tape.DirectoryEmpty = false
			case "stdin-open":
				tape.InputEOF = false
			case "stdout-open":
				tape.StdoutEOF = false
			case "stderr-open":
				tape.StderrEOF = false
			case "unknown-arg":
				tape.Args = append(tape.Args, "--resume", "foreign")
			case "duplicate-arg":
				tape.Args = append(tape.Args, "--print")
			case "tools":
				for i, arg := range tape.Args {
					if arg == "--tools" {
						tape.Args[i+1] = "Bash"
					}
				}
			case "hook":
				for i, arg := range tape.Args {
					if arg == "--settings" {
						tape.Args[i+1] = `{"disableAllHooks":false}`
					}
				}
			case "invalid-utf8-prompt":
				tape.Args[len(tape.Args)-1] = string([]byte{0xff})
			case "missing-prompt":
				tape.Args = tape.Args[:len(tape.Args)-2]
			case "wrong-status":
				tape.Status = FallbackWorkerStatus{ExitCode: 0, Signal: 9}
			case "unknown-signal":
				tape.Status = FallbackWorkerStatus{ExitCode: -1, Signal: 15}
			case "bad-stdout":
				tape.Frames[0].Data = `{"type":"result"} trailing`
			case "credential-stderr":
				tape.Frames = append(tape.Frames, CLIFrame{"stderr", "CLAUDE_CODE_OAUTH_TOKEN=private-probe"})
			case "duplicate-input":
				tape.Input = `{"user":"hidden","user":"Please ask me one question.","assistant":"Would you like a Go test?"}`
			case "extra-input":
				tape.Input = `{"user":"Please ask me one question.","assistant":"Would you like a Go test?","extra":true}`
			}
			if tape.Validate() == nil {
				t.Fatal("incomplete or foreign worker carrier accepted")
			}
		})
	}
}
func TestFallbackWorkerNativeProcessBoundary(t *testing.T) {
	for _, kind := range []string{"capture-zero", "capture-seven", "capture-kill", "capture-retained-pipe", "replay-zero", "replay-kill", "observer-capture-zero", "observer-capture-seven", "observer-capture-kill", "observer-replay-zero", "observer-replay-kill"} {
		t.Run(kind, func(t *testing.T) {
			observed := strings.HasPrefix(kind, "observer-")
			kind = strings.TrimPrefix(kind, "observer-")
			home, err := os.MkdirTemp("/tmp", "fw-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(home)
			cwd := filepath.Join(home, "pyry-reply-owned")
			if os.Mkdir(cwd, 0700) != nil || os.Mkdir(filepath.Join(home, "recording-workers"), 0700) != nil {
				t.Fatal("private dirs")
			}
			daemon := struct{ PID int }{os.Getpid()}
			if PublishLifecycleControl(filepath.Join(home, "recording-worker-daemon.json"), daemon) != nil {
				t.Fatal("daemon")
			}
			mode := "capture"
			if strings.HasPrefix(kind, "replay") {
				mode = "replay"
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(home, "native")
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
			if os.WriteFile(script, []byte("#!/bin/sh\nexec "+quote(exe)+" -test.run=^TestFallbackWorkerOSHelper$ -- native \"$@\"\n"), 0700) != nil {
				t.Fatal("native")
			}
			producer := lifecycleSuiteControl().Producer
			base := PoolProcessConfig{Mode: mode, Scenario: "session-resume", Home: home, Claude: script, Producer: producer}
			if observed {
				base.Scenario = "fallback-suggestion"
			}
			path := FallbackWorkerJobPath(home, 1)
			tapePath := strings.TrimSuffix(path, ".job") + ".json"
			saved := fallbackWorkerControlTape()
			if strings.HasSuffix(kind, "kill") {
				saved.Status = FallbackWorkerStatus{ExitCode: -1, Signal: 9}
			}
			if mode == "replay" && saved.Save(tapePath) != nil {
				t.Fatal("stage")
			}
			sourceSHA := strings.Repeat("c", 64)
			var brokerCmd *exec.Cmd
			var brokerProof bytes.Buffer
			server, err := NewLifecycleBrokerServer(filepath.Join(home, "worker.sock"), []string{path}, func(path string, peer int) (*exec.Cmd, error) {
				var job FallbackWorkerJob
				if ReadLifecycleControl(path, 16384, &job) != nil || ValidateFallbackWorkerLaunch(job, path, peer, os.Getpid(), base, sourceSHA) != nil {
					return nil, os.ErrInvalid
				}
				cmd := exec.Command(exe, "-test.run=^TestFallbackWorkerOSHelper$", "--", "broker", path, mode)
				cmd.Env = append(os.Environ(), "PYRY_FINITE_WORKER_KIND="+kind)
				cmd.Stdout = &brokerProof
				brokerCmd = cmd
				return cmd, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer server.Join(2 * time.Second)
			cfg := FallbackWorkerJob{Version: 1, GroupPID: 0, DaemonPID: os.Getpid(), BrokerParentPID: os.Getpid(), Scenario: base.Scenario, Home: home, WorkDir: cwd, Claude: script, Path: tapePath, Abort: strings.TrimSuffix(path, ".job") + ".abort", SourceBinarySHA256: sourceSHA, Args: fallbackWorkerControlArgs(), Producer: producer}
			encoded, _ := json.Marshal(cfg)
			wrapperKind := "wrapper"
			if observed {
				wrapperKind = "observer"
			}
			cmd := exec.Command(exe, "-test.run=^TestFallbackWorkerOSHelper$", "--", wrapperKind, path, mode)
			env := []string{}
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if key != "HOME" && key != "CLAUDE_CONFIG_DIR" && key != "CLAUDE_CODE_OAUTH_TOKEN" && key != "ANTHROPIC_API_KEY" && key != "PYRY_FINITE_WORKER_CONTROL" {
					env = append(env, entry)
				}
			}
			env = append(env, "HOME="+home, "PYRY_FINITE_WORKER_CONTROL="+string(encoded), "PYRY_FINITE_WORKER_KIND="+kind)
			if mode == "capture" {
				env = append(env, "CLAUDE_CODE_OAUTH_TOKEN=synthetic-process-control")
			}
			cmd.Env = env
			cmd.Dir = cwd
			cmd.Stdin = strings.NewReader(saved.Input)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			outR, outW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer outR.Close()
			cmd.Stdout = outW
			var diagnostic bytes.Buffer
			cmd.Stderr = &diagnostic
			started := time.Now()
			if cmd.Start() != nil {
				t.Fatal("wrapper")
			}
			defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // only this current test-owned group
			outW.Close()
			output := make(chan []byte, 1)
			ready := make(chan struct{})
			go func() {
				var buf bytes.Buffer
				first := true
				p := make([]byte, 4096)
				for {
					n, e := outR.Read(p)
					if n > 0 {
						buf.Write(p[:n])
						if first {
							close(ready)
							first = false
						}
					}
					if e != nil {
						output <- buf.Bytes()
						return
					}
				}
			}()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			if strings.HasSuffix(kind, "kill") {
				select {
				case <-ready:
				case err := <-done:
					t.Fatalf("exited before native/replay output: %v %s", err, diagnostic.String())
				case <-time.After(15 * time.Second):
					_ = cmd.Process.Kill()
					t.Fatal("no output")
				}
				// The signal target is solely the current exec.Cmd group this test owns.
				if syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) != nil {
					t.Fatal("owned group cancellation")
				}
			}
			var waitErr error
			select {
			case waitErr = <-done:
			case <-time.After(20 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatal("worker join timed out")
			}
			joined := server.Join(5 * time.Second)
			if kind == "capture-retained-pipe" {
				if waitErr == nil || joined == nil || time.Since(started) > 8*time.Second {
					t.Fatal("trickling inherited pipe was not bounded")
				}
				if _, err := os.Lstat(tapePath); !os.IsNotExist(err) {
					t.Fatal("incomplete native EOF published a tape")
				}
				return
			}
			if joined != nil {
				t.Fatalf("broker join: %v stderr=%s", joined, diagnostic.String())
			}
			if brokerCmd == nil || ValidateFallbackWorkerCompletion(brokerProof.Bytes(), brokerCmd.Process.Pid, path) != nil {
				t.Fatal("completion not bound to actual outer-owned broker")
			}
			data := <-output
			if len(data) == 0 {
				t.Fatal("native output omitted")
			}
			var receipt FallbackWorkerReceipt
			var actualJob FallbackWorkerJob
			if ReadLifecycleControl(path, 16384, &actualJob) != nil {
				t.Fatal("missing actual helper claim")
			}
			if observed && actualJob.OwnerPID == cmd.Process.Pid || !observed && actualJob.OwnerPID != cmd.Process.Pid {
				t.Fatal("helper and group ownership conflated")
			}
			if ReadLifecycleControl(strings.TrimSuffix(path, ".job")+".receipt", 16384, &receipt) != nil || !receipt.Success || receipt.OwnerPID != actualJob.OwnerPID || receipt.GroupPID != cmd.Process.Pid {
				t.Fatal("no actual owned terminal receipt")
			}
			file, err := os.Open(tapePath)
			if err != nil {
				t.Fatal(err)
			}
			tape, err := DecodeFallbackWorker(file)
			file.Close()
			if err != nil {
				t.Fatal(err)
			}
			status := cmd.ProcessState.Sys().(syscall.WaitStatus)
			if status.Signaled() {
				if tape.Status.Signal != int(status.Signal()) || tape.Status.ExitCode != -1 {
					t.Fatal("actual cancellation differs")
				}
			} else if tape.Status.Signal != 0 || tape.Status.ExitCode != cmd.ProcessState.ExitCode() {
				t.Fatal("actual exit differs", waitErr)
			}
			if mode == "capture" && receipt.NativePID <= 1 || mode == "replay" && receipt.NativePID != 0 {
				t.Fatal("native ownership metadata wrong")
			}
			if observed {
				if syscall.Kill(receipt.OwnerPID, 0) != syscall.ESRCH || syscall.Kill(receipt.GroupPID, 0) != syscall.ESRCH || mode == "capture" && syscall.Kill(receipt.NativePID, 0) != syscall.ESRCH {
					t.Fatal("observer/helper/native terminal identity remains alive")
				}
				if tape.Status.Signal == 0 {
					var observation struct {
						OwnerPID, ChildPID int
						Status             FallbackWorkerStatus
					}
					if ReadLifecycleControl(filepath.Join(home, "observer-wait.json"), 16384, &observation) != nil || observation.OwnerPID != cmd.Process.Pid || observation.ChildPID != actualJob.OwnerPID || observation.Status != tape.Status {
						t.Fatal("actual observer Wait not joined to helper/native status")
					}
				}
			}
			if tape.Input != saved.Input || !reflect.DeepEqual(tape.Args, saved.Args) {
				t.Fatal("native exact input changed")
			}
			if mode == "replay" && !reflect.DeepEqual(tape, saved) {
				t.Fatal("replay mutated saved tape")
			}
		})
	}
}
func TestFallbackWorkerOSHelper(t *testing.T) {
	at := -1
	for i, arg := range os.Args {
		if arg == "--" {
			at = i + 1
			break
		}
	}
	if at < 0 {
		return
	}
	switch os.Args[at] {
	case "native":
		raw, err := io.ReadAll(os.Stdin)
		if err != nil || string(raw) != fallbackWorkerControlTape().Input || os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "synthetic-process-control" {
			os.Exit(3)
		}
		fmt.Println(`{"type":"result","subtype":"success","is_error":false,"result":"Yes, please add the test."}`)
		kind := os.Getenv("PYRY_FINITE_WORKER_KIND")
		if kind == "capture-retained-pipe" {
			exe, _ := os.Executable()
			child := exec.Command(exe, "-test.run=^TestFallbackWorkerOSHelper$", "--", "hold")
			child.Stdout = os.Stdout
			child.Stderr = os.Stderr
			if child.Start() != nil {
				os.Exit(3)
			}
			os.Exit(0)
		}
		if strings.HasSuffix(kind, "kill") {
			for {
				time.Sleep(time.Second)
			}
		}
		if strings.HasSuffix(kind, "seven") {
			os.Exit(7)
		}
		os.Exit(0)
	case "observer":
		exe, err := os.Executable()
		if err != nil {
			os.Exit(2)
		}
		child := exec.Command(exe, "-test.run=^TestFallbackWorkerOSHelper$", "--", "wrapper", os.Args[at+1], os.Args[at+2])
		child.Stdin = os.Stdin
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		child.Env = os.Environ()
		if child.Start() != nil {
			os.Exit(2)
		}
		_ = child.Wait()
		status := FallbackWorkerStatus{ExitCode: child.ProcessState.ExitCode()}
		wait := child.ProcessState.Sys().(syscall.WaitStatus)
		if wait.Signaled() {
			status = FallbackWorkerStatus{ExitCode: -1, Signal: int(wait.Signal())}
		}
		observation := struct {
			OwnerPID, ChildPID int
			Status             FallbackWorkerStatus
		}{os.Getpid(), child.Process.Pid, status}
		if PublishLifecycleControl(filepath.Join(os.Getenv("HOME"), "observer-wait.json"), observation) != nil {
			os.Exit(2)
		}
		if status.Signal != 0 {
			os.Exit(2)
		}
		os.Exit(status.ExitCode)
	case "wrapper":
		var job FallbackWorkerJob
		if json.Unmarshal([]byte(os.Getenv("PYRY_FINITE_WORKER_CONTROL")), &job) != nil {
			os.Exit(2)
		}
		job.OwnerPID = os.Getpid()
		job.GroupPID = syscall.Getpgrp()
		path := os.Args[at+1]
		if PublishLifecycleControl(path, job) != nil {
			os.Exit(2)
		}
		status, err := ConnectFallbackWorker(filepath.Join(job.Home, "worker.sock"), path, os.Environ())
		if err != nil {
			fmt.Fprintln(os.Stderr, "worker connection rejected")
			os.Exit(2)
		}
		if status.Signal != 0 {
			os.Exit(2)
		}
		os.Exit(status.ExitCode)
	case "hold":
		for i := 0; i < 100; i++ {
			fmt.Println(`{"type":"progress"}`)
			time.Sleep(20 * time.Millisecond)
		}
		os.Exit(0)
	case "broker":
		var job FallbackWorkerJob
		if ReadLifecycleControl(os.Args[at+1], 16384, &job) != nil {
			os.Exit(2)
		}
		var err error
		if os.Getenv("PYRY_FINITE_WORKER_KIND") == "capture-retained-pipe" {
			err = runFallbackWorkerBroker(job, os.Args[at+2], 250*time.Millisecond)
		} else {
			err = RunFallbackWorkerBroker(job, os.Args[at+2])
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "worker broker rejected")
			os.Exit(2)
		}
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

func TestFallbackWorkerInventoryRejectsForeignEvidence(t *testing.T) {
	for _, kind := range []string{"valid", "missing-job", "missing-receipt", "extra-file", "foreign-source", "foreign-owner", "foreign-daemon", "wrong-wait", "unknown-wait", "unjoined-cancel", "changed-input", "wrong-native", "mutate-output-after-receipt", "duplicate-attempt", "observer-valid", "observer-helper-wait", "observer-wrong-wait", "observer-live-helper", "observer-live-group"} {
		t.Run(kind, func(t *testing.T) {
			observed := strings.HasPrefix(kind, "observer-")
			kind = strings.TrimPrefix(kind, "observer-")
			home := t.TempDir()
			if os.Chmod(home, 0700) != nil {
				t.Fatal("private home")
			}
			dir := filepath.Join(home, "recording-workers")
			if os.Mkdir(dir, 0700) != nil {
				t.Fatal("private")
			}
			exited := func() int {
				cmd := exec.Command("true")
				if err := cmd.Run(); err != nil {
					t.Fatal(err)
				}
				return cmd.Process.Pid
			}
			owner, native := exited(), exited()
			producer := lifecycleSuiteControl().Producer
			path := FallbackWorkerJobPath(home, 1)
			job := FallbackWorkerJob{Version: 1, OwnerPID: owner, GroupPID: owner, DaemonPID: os.Getpid(), BrokerParentPID: os.Getpid(), Scenario: "session-resume", Home: home, WorkDir: filepath.Join(home, "pyry-reply-owned"), Claude: "/owned/native", Path: strings.TrimSuffix(path, ".job") + ".json", Abort: strings.TrimSuffix(path, ".job") + ".abort", SourceBinarySHA256: producer.BinarySHA256, Args: fallbackWorkerControlArgs(), Producer: producer}
			tape := fallbackWorkerControlTape()
			receipt := FallbackWorkerReceipt{Version: 1, OwnerPID: owner, GroupPID: owner, NativePID: native, BrokerPID: os.Getpid(), Success: true, Frames: len(tape.Frames), Status: tape.Status}
			observation := FallbackWorkerObservation{OwnerPID: owner, WaitCompleted: true, ExitObserved: true, Status: tape.Status}
			if observed {
				job.Scenario = "fallback-suggestion"
				job.GroupPID = exited()
				receipt.GroupPID = job.GroupPID
				observation.OwnerPID = job.GroupPID
			}

			switch kind {
			case "helper-wait":
				observation.OwnerPID = job.OwnerPID
			case "live-helper":
				job.OwnerPID = os.Getpid()
				receipt.OwnerPID = job.OwnerPID
				receipt.BrokerPID = exited()
			case "live-group":
				job.GroupPID = os.Getpid()
				receipt.GroupPID = job.GroupPID
				observation.OwnerPID = job.GroupPID
			case "foreign-source":
				job.SourceBinarySHA256 = strings.Repeat("f", 64)
			case "foreign-owner":
				receipt.OwnerPID = native
			case "foreign-daemon":
				job.DaemonPID = native
			case "wrong-wait":
				observation.Status.ExitCode = 7
			case "unknown-wait":
				observation.WaitCompleted = false
			case "unjoined-cancel":
				tape.Status = FallbackWorkerStatus{ExitCode: -1, Signal: 9}
				receipt.Status = tape.Status
				observation.Status = tape.Status
			case "changed-input":
				tape.Input = `{"assistant":"Foreign assistant output","user":"Please ask me one question."}`
			case "wrong-native":
				receipt.NativePID = 0
			}
			if PublishLifecycleControl(filepath.Join(home, "recording-worker-daemon.json"), struct{ PID int }{os.Getpid()}) != nil || PublishLifecycleControl(filepath.Join(home, "recording-worker-observations.json"), []FallbackWorkerObservation{observation}) != nil {
				t.Fatal("controls")
			}
			if kind != "missing-job" && PublishLifecycleControl(path, job) != nil {
				t.Fatal("job")
			}
			if tape.Save(job.Path) != nil {
				t.Fatal("tape")
			}
			savedRaw, _ := os.ReadFile(job.Path)
			sum := sha256.Sum256(savedRaw)
			receipt.TapeSHA256 = hex.EncodeToString(sum[:])
			if kind != "missing-receipt" && PublishLifecycleControl(strings.TrimSuffix(path, ".job")+".receipt", receipt) != nil {
				t.Fatal("receipt")
			}
			if kind == "mutate-output-after-receipt" {
				changed := bytes.ReplaceAll(savedRaw, []byte("Yes, please add the test."), []byte("Changed late native text."))
				if bytes.Equal(changed, savedRaw) || os.WriteFile(job.Path, changed, 0600) != nil {
					t.Fatal("mutation")
				}
			}
			if kind == "extra-file" {
				if os.WriteFile(filepath.Join(dir, "unexpected"), nil, 0600) != nil {
					t.Fatal("extra")
				}
			}
			if kind == "duplicate-attempt" {
				raw, _ := os.ReadFile(path)
				if os.WriteFile(FallbackWorkerJobPath(home, 2), raw, 0600) != nil {
					t.Fatal("duplicate")
				}
			}
			roles := map[string]*LifecycleTape{"bootstrap": {Version: 1, Tape: fallbackExchangeControl(), End: "idle-evict"}}
			workers, err := ReadLifecycleWorkers(home, job.Scenario, "capture", producer.Revision, producer.BinarySHA256, os.Getpid(), roles)
			if kind == "valid" {
				if err != nil || len(workers) != 1 || workers[0].SourceRole != "bootstrap" || workers[0].UserSequence != 1 {
					t.Fatal("valid complete source", err)
				}
			} else if err == nil {
				t.Fatal("foreign worker evidence accepted")
			}
		})
	}
}

func TestFallbackWorkerCompletionRejectsCleanupRewrite(t *testing.T) {
	home := t.TempDir()
	if os.Chmod(home, 0700) != nil {
		t.Fatal("private")
	}
	path := filepath.Join(home, "000001.job")
	tapePath := strings.TrimSuffix(path, ".job") + ".json"
	tape := fallbackWorkerControlTape()
	if tape.Save(tapePath) != nil {
		t.Fatal("tape")
	}
	raw, _ := os.ReadFile(tapePath)
	sum := sha256.Sum256(raw)
	receipt := FallbackWorkerReceipt{Version: 1, BrokerPID: os.Getpid(), OwnerPID: 2, GroupPID: 2, NativePID: 3, Success: true, Frames: len(tape.Frames), Status: tape.Status, TapeSHA256: hex.EncodeToString(sum[:])}
	if PublishLifecycleControl(strings.TrimSuffix(path, ".job")+".receipt", receipt) != nil {
		t.Fatal("receipt")
	}
	proof, _ := json.Marshal(receipt)
	if ValidateFallbackWorkerCompletion(proof, os.Getpid(), path) != nil {
		t.Fatal("valid outer capability")
	}
	if ValidateFallbackWorkerCompletion(proof, os.Getpid()+1, path) == nil {
		t.Fatal("foreign broker PID accepted")
	}
	changed := bytes.ReplaceAll(raw, []byte("Yes, please add the test."), []byte("Changed safe prose."))
	if bytes.Equal(raw, changed) || os.WriteFile(tapePath, changed, 0600) != nil {
		t.Fatal("mutation")
	}
	if ValidateFallbackWorkerCompletion(proof, os.Getpid(), path) == nil {
		t.Fatal("late tape rewrite accepted")
	}
	sum = sha256.Sum256(changed)
	receipt.TapeSHA256 = hex.EncodeToString(sum[:])
	forged, _ := json.Marshal(receipt)
	if os.WriteFile(strings.TrimSuffix(path, ".job")+".receipt", forged, 0600) != nil {
		t.Fatal("forgery")
	}
	if ValidateFallbackWorkerCompletion(proof, os.Getpid(), path) == nil {
		t.Fatal("forged disk receipt overrode outer-owned broker proof")
	}
}
