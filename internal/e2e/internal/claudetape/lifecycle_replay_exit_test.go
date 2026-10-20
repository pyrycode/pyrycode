package claudetape

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLifecycleReplayAfterProviderExit(t *testing.T) {
	for _, variant := range []string{"held-eofs", "early-eofs", "live-exhausted", "changed-input", "partial-input", "missing-input", "foreign-action", "missing-action", "terminal-rejected"} {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				var d LifecycleDiagnostic
				if ReadLifecycleControl(filepath.Join(root, "diagnostic.json"), 16384, &d) == nil {
					raw, _ := json.Marshal(d)
					t.Logf("terminal diagnostic=%s", raw)
				}
			})
			c := LifecycleBrokerConfig{PoolProcessConfig: PoolProcessConfig{Mode: "replay", Scenario: "session-rotation-bootstrap", Path: filepath.Join(root, "tape.json"), Claim: filepath.Join(root, "claim"), Finish: filepath.Join(root, "finish")}, BrokerParentPID: os.Getpid(), End: "rotate-session", Kill: filepath.Join(root, "action"), Deadline: time.Now().Add(15 * time.Second)}
			query := `{"type":"control_request","request_id":"final","request":{"subtype":"get_context_usage"}}`
			responses := []string{`{"type":"control_response","response":{"request_id":"final","subtype":"success"}}`, `{"type":"system","subtype":"status","status":"idle"}`}
			tape := &LifecycleTape{Version: 1, End: c.End, Tape: &Tape{Version: 2, Scenario: c.Scenario, Args: lifecycleProbeBinding{}.Args(), Files: map[string]json.RawMessage{"$cwd": json.RawMessage(`"probe"`)}, Producer: Producer{Revision: strings.Repeat("a", 40), BinarySHA256: strings.Repeat("b", 64), ClaudeVersion: "owned-probe", CapturedAt: time.Now().UTC().Format(time.RFC3339Nano)}, Frames: []Frame{
				{Direction: ClientCheckpointDirection, Data: json.RawMessage(`{"type":"client_checkpoint","sequence":1,"action":"lifecycle_rotate","conversation_id":"${LIFECYCLE_CONVERSATION}"}`)},
				{Direction: "stdin", Data: json.RawMessage(query)},
				{Direction: "stdout", Data: json.RawMessage(responses[0])},
				{Direction: "stdout", Data: json.RawMessage(responses[1])},
			}}}
			if err := tape.Save(c.Path); err != nil {
				t.Fatal(err)
			}
			pipe := func() (*os.File, *os.File) {
				r, w, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
				return r, w
			}
			inputR, inputW := pipe()
			outputR, outputW := pipe()
			errR, errW := pipe()
			providerInR, providerInW := pipe()
			providerOutR, providerOutW := pipe()
			providerErrR, providerErrW := pipe()
			readyR, readyW := pipe()
			jobPath := filepath.Join(root, "job.json")
			owner := exec.Command(os.Args[0], "-test.run=^TestLifecycleProbeProcess$")
			owner.Env = append(os.Environ(), "PYRY_LIFECYCLE_PROBE=sentinel", "PYRY_LIFECYCLE_PROBE_JOB="+jobPath)
			owner.Stdin, owner.Stdout, owner.Stderr = providerInR, providerOutW, providerErrW
			// The sentinel reads the job before sleeping; publish before starting it.
			if err := probeWrite(jobPath, lifecycleProbeJob{Config: c, Root: root}); err != nil {
				t.Fatal(err)
			}
			if err := owner.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if owner.ProcessState == nil {
					_ = owner.Process.Kill()
					_ = owner.Wait()
				}
			})
			_ = providerInR.Close()
			c.OwnerPID = owner.Process.Pid
			brokerJob := filepath.Join(root, "broker-job.json")
			if err := probeWrite(brokerJob, lifecycleProbeJob{Config: c, Root: root}); err != nil {
				t.Fatal(err)
			}
			broker := exec.Command(os.Args[0], "-test.run=^TestLifecycleProbeProcess$")
			broker.Env = append(os.Environ(), "PYRY_LIFECYCLE_PROBE=broker", "PYRY_LIFECYCLE_PROBE_JOB="+brokerJob)
			if variant == "terminal-rejected" {
				broker.Env = append(broker.Env, "PYRY_LIFECYCLE_PROBE_REJECT_TERMINAL=1")
			}
			broker.ExtraFiles = []*os.File{inputR, outputW, errW, providerInW, providerOutR, providerErrR, readyW}
			if err := broker.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if broker.ProcessState == nil {
					_ = broker.Process.Kill()
					_ = broker.Wait()
				}
			})
			for _, f := range broker.ExtraFiles {
				_ = f.Close()
			}
			_ = readyR.SetReadDeadline(time.Now().Add(5 * time.Second))
			var ready [1]byte
			if _, err := io.ReadFull(readyR, ready[:]); err != nil || ready[0] != 1 {
				t.Fatal("broker not ready", err)
			}
			if variant != "missing-action" {
				pid := owner.Process.Pid
				if variant == "foreign-action" {
					pid++
				}
				if err := PublishLifecycleControl(c.Kill, LifecycleKill{PID: pid, Signal: c.End}); err != nil {
					t.Fatal(err)
				}
				if variant != "foreign-action" {
					var ack LifecycleKill
					waitLifecycleProbe(t, c.Kill+".ready", &ack)
					if ack.PID != owner.Process.Pid || ack.Signal != c.End {
						t.Fatal("foreign acknowledgement")
					}
				}
			}
			assertPending := func() {
				t.Helper()
				time.Sleep(60 * time.Millisecond)
				if _, err := os.Lstat(filepath.Join(root, "receipt.json")); !os.IsNotExist(err) {
					t.Fatal("completed before independent teardown evidence")
				}
			}
			killOwner := func() {
				t.Helper()
				if err := owner.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				err := owner.Wait()
				status, ok := owner.ProcessState.Sys().(syscall.WaitStatus)
				if err == nil || !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
					t.Fatal("owned termination not proved")
				}
			}
			valid := variant == "held-eofs" || variant == "early-eofs" || variant == "live-exhausted"
			if variant != "live-exhausted" {
				killOwner()
			}
			if variant == "early-eofs" {
				_ = providerOutW.Close()
				_ = providerErrW.Close()
				assertPending()
			}
			if variant != "missing-input" && variant != "foreign-action" && variant != "missing-action" {
				request := query + "\n"
				if variant == "changed-input" {
					request = strings.Replace(request, "get_context_usage", "interrupt", 1)
				}
				if variant == "partial-input" {
					request = query
				}
				if _, err := io.WriteString(inputW, request); err != nil {
					t.Fatal(err)
				}
			}
			if valid || variant == "terminal-rejected" {
				reader := bufio.NewReader(outputR)
				_ = outputR.SetReadDeadline(time.Now().Add(5 * time.Second))
				for _, want := range responses {
					line, err := reader.ReadBytes('\n')
					var got, expected any
					if err != nil || json.Unmarshal(line, &got) != nil || json.Unmarshal([]byte(want), &expected) != nil {
						t.Fatal("matched final response lost", err)
					}
					a, _ := json.Marshal(got)
					b, _ := json.Marshal(expected)
					if string(a) != string(b) {
						t.Fatal("released response changed")
					}
				}
				if variant != "early-eofs" {
					assertPending()
					if variant == "live-exhausted" {
						killOwner()
					}
					_ = providerOutW.Close()
					assertPending()
				}
			}
			_ = inputW.Close()
			_ = providerOutW.Close()
			_ = providerErrW.Close()
			var receipt lifecycleProbeReceipt
			waitLifecycleProbe(t, filepath.Join(root, "receipt.json"), &receipt)
			if err := broker.Wait(); err != nil {
				t.Fatal(err)
			}
			if receipt.Success != valid || receipt.OwnerPID != owner.Process.Pid {
				t.Fatalf("success=%t want=%t frames=%d error=%s", receipt.Success, valid, receipt.Frames, receipt.Error)
			}
			if valid && receipt.Frames != 4 {
				t.Fatal("incomplete replay")
			}
			var diagnostic LifecycleDiagnostic
			waitLifecycleProbe(t, filepath.Join(root, "diagnostic.json"), &diagnostic)
			if diagnostic.Completed != valid || !diagnostic.InputSampled {
				t.Fatal("diagnostic completion differs from actual result")
			}
			if valid && (!diagnostic.OutputEOF || !diagnostic.StderrEOF || !diagnostic.OutputSampled || !diagnostic.StderrSampled || !diagnostic.ActionAcknowledged || !diagnostic.TerminalChecked || diagnostic.InputsMatched != 1 || diagnostic.OutputsDelivered != 2) {
				t.Fatal("diagnostic lost independent progress or completion evidence")
			}
			_ = errR.Close()
		})
	}
}
