package claudetape

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type lifecycleProbeBinding struct{}

func (lifecycleProbeBinding) Args() []string { return []string{"test-owned-probe"} }
func (lifecycleProbeBinding) Frame(b []byte, _ bool) ([]byte, error) {
	return append([]byte(nil), b...), nil
}
func (lifecycleProbeBinding) Files() (map[string]json.RawMessage, error) {
	return map[string]json.RawMessage{"$cwd": json.RawMessage(`"probe"`)}, nil
}

type lifecycleProbeJob struct {
	Config   LifecycleBrokerConfig
	Root     string
	Provider string
}
type lifecycleProbeReceipt struct {
	Success          bool
	Frames, OwnerPID int
	Error            string
}

func probeWrite(path string, v any) error {
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	_, e = f.Write(raw)
	return e
}
func TestLifecycleProbeProcess(t *testing.T) {
	mode := os.Getenv("PYRY_LIFECYCLE_PROBE")
	if mode == "" {
		return
	}
	var j lifecycleProbeJob
	raw, e := os.ReadFile(os.Getenv("PYRY_LIFECYCLE_PROBE_JOB"))
	if e != nil || json.Unmarshal(raw, &j) != nil {
		os.Exit(2)
	}
	switch mode {
	case "exec":
		j.Config.OwnerPID = os.Getpid()
		path := filepath.Join(j.Root, "broker-job.json")
		if probeWrite(path, j) != nil {
			os.Exit(2)
		}
		env := append(os.Environ(), "PYRY_LIFECYCLE_PROBE=broker", "PYRY_LIFECYCLE_PROBE_JOB="+path)
		_ = os.Setenv("PYRY_LIFECYCLE_PROBE", "native")
		provider := os.Args[0]
		if j.Provider != "" {
			provider = j.Provider
		}
		e = ExecLifecycleChild([]string{"-test.run=^TestLifecycleProbeProcess$"}, env, provider, []string{"-test.run=^TestLifecycleProbeProcess$"}, j.Config.Mode == "capture")
		if e != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "native":
		if probeWrite(filepath.Join(j.Root, "native-pid.json"), os.Getpid()) != nil {
			os.Exit(2)
		}
		if strings.Contains(j.Config.Scenario, "input-pressure") {
			for {
				time.Sleep(time.Hour)
			}
		}
		scan := bufio.NewScanner(os.Stdin)
		for scan.Scan() {
			if strings.Contains(j.Config.Scenario, "output-pressure") {
				_, _ = io.WriteString(os.Stdout, `{"type":"assistant","message":{"content":"`+strings.Repeat("x", 1<<20)+`"}}`+"\n")
				continue
			}
			_, _ = io.WriteString(os.Stdout, "{\"type\":\"assistant\",\"message\":{\"content\":\"native reply\"}}\n")
		}
		os.Exit(0)
	case "broker":
		if probeWrite(filepath.Join(j.Root, "broker-pid.json"), os.Getpid()) != nil {
			os.Exit(2)
		}
		j.Config.OnSeal = func(n int) error {
			return probeWrite(filepath.Join(j.Root, "sealed.json"), lifecycleProbeReceipt{Success: true, Frames: n, OwnerPID: j.Config.OwnerPID})
		}
		n, e := RunLifecycleBroker(j.Config, lifecycleProbeBinding{})
		r := lifecycleProbeReceipt{Success: e == nil, Frames: n, OwnerPID: j.Config.OwnerPID}
		if e != nil {
			r.Error = e.Error()
		}
		if probeWrite(filepath.Join(j.Root, "receipt.json"), r) != nil {
			os.Exit(2)
		}
		os.Exit(0)
	default:
		os.Exit(2)
	}
}
func waitLifecycleProbe(t *testing.T, path string, target any) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, e := os.ReadFile(path)
		if e == nil && json.Unmarshal(raw, target) == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("owned lifecycle probe did not finish")
}
func TestLifecycleExecKeepsActualPIDAndJournalsAfterKill(t *testing.T) {
	producer := Producer{Revision: strings.Repeat("a", 40), BinarySHA256: strings.Repeat("b", 64), ClaudeVersion: "test-owned", CapturedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	saved := map[string]*LifecycleTape{}
	for _, variant := range []string{"capture", "replay", "capture-eof", "replay-eof", "capture-seal", "replay-seal", "capture-delete", "replay-delete", "capture-delete-partial", "replay-delete-partial", "delete-without-checkpoint", "unauthorized-kill", "foreign-pid", "partial-input", "changed-input", "abort"} {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			mode := "capture"
			if strings.HasPrefix(variant, "replay") || variant == "changed-input" {
				mode = "replay"
			}
			c := LifecycleBrokerConfig{PoolProcessConfig: PoolProcessConfig{Mode: mode, Scenario: "lifecycle-probe", Path: filepath.Join(root, "tape.json"), Claim: filepath.Join(root, "claim"), Finish: filepath.Join(root, "finish"), Producer: producer}, End: "kill", Kill: filepath.Join(root, "kill.json")}
			if strings.Contains(variant, "-delete") || variant == "delete-without-checkpoint" {
				c.End = "delete-conversation"
				c.Scenario = "conversation-lifecycle-created-1"
			}
			if variant == "abort" {
				c.Abort = filepath.Join(root, "abort")
			}
			if strings.HasSuffix(variant, "-eof") {
				c.End = "eof"
			}
			if strings.HasSuffix(variant, "-seal") {
				c.End = "seal"
			}
			if mode == "replay" {
				if saved[c.End] == nil {
					t.Fatal("missing native control")
				}
				if e := saved[c.End].Save(c.Path); e != nil {
					t.Fatal(e)
				}
			}
			jobPath := filepath.Join(root, "job.json")
			if e := probeWrite(jobPath, lifecycleProbeJob{Config: c, Root: root}); e != nil {
				t.Fatal(e)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestLifecycleProbeProcess$")
			cmd.Env = append(os.Environ(), "PYRY_LIFECYCLE_PROBE=exec", "PYRY_LIFECYCLE_PROBE_JOB="+jobPath)
			in, e := cmd.StdinPipe()
			if e != nil {
				t.Fatal(e)
			}
			out, e := cmd.StdoutPipe()
			if e != nil {
				t.Fatal(e)
			}
			var diagnostic strings.Builder
			cmd.Stderr = &diagnostic
			if cmd.Start() != nil {
				t.Fatal("probe start")
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = in.Close() })
			request := "{\"type\":\"user\",\"message\":{\"content\":\"request\"}}\n"
			if variant == "changed-input" {
				request = strings.Replace(request, "request", "changed", 1)
			}
			if variant == "partial-input" {
				request = strings.TrimSuffix(request, "\n")
			}
			if _, e := io.WriteString(in, request); e != nil {
				t.Fatal(e)
			}
			if variant != "partial-input" && variant != "changed-input" {
				line := make(chan string, 1)
				go func() { v, _ := bufio.NewReader(out).ReadString('\n'); line <- v }()
				select {
				case v := <-line:
					var got map[string]any
					if json.Unmarshal([]byte(v), &got) != nil || len(got) != 2 || got["type"] != "assistant" || !reflect.DeepEqual(got["message"], map[string]any{"content": "native reply"}) {
						t.Fatal("native/replayed reply changed", v)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("native/replayed reply absent")
				}
			}
			if mode == "capture" && variant != "partial-input" {
				var pid int
				waitLifecycleProbe(t, filepath.Join(root, "native-pid.json"), &pid)
				if pid != cmd.Process.Pid {
					t.Fatal("provider was not exec at daemon-owned PID")
				}
			}
			if (c.End == "kill" || c.End == "delete-conversation") && variant != "unauthorized-kill" && variant != "abort" && variant != "delete-without-checkpoint" {
				pid := cmd.Process.Pid
				if variant == "foreign-pid" {
					pid++
				}
				action := LifecycleKill{PID: pid, Signal: func() string {
					if c.End == "delete-conversation" {
						return "delete-conversation"
					}
					return "SIGKILL"
				}()}
				if strings.HasSuffix(variant, "-partial") {
					// Hold a genuinely partial pending file while the live broker
					// polls. It must neither accept nor reject this unpublished action.
					raw, _ := json.Marshal(action)
					pending := c.Kill + ".pending"
					f, err := os.OpenFile(pending, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = f.Write(raw[:len(raw)/2]); err != nil {
						t.Fatal(err)
					}
					time.Sleep(150 * time.Millisecond)
					if cmd.Process.Signal(syscall.Signal(0)) != nil {
						t.Fatal("partial pending action killed provider")
					}
					for _, path := range []string{c.Kill + ".ready", filepath.Join(root, "receipt.json")} {
						if _, err := os.Lstat(path); !os.IsNotExist(err) {
							t.Fatal("pending action affected broker")
						}
					}
					if _, err = f.Write(raw[len(raw)/2:]); err != nil {
						t.Fatal(err)
					}
					if err = f.Close(); err != nil {
						t.Fatal(err)
					}
					if err = os.Link(pending, c.Kill); err != nil {
						t.Fatal(err)
					}
					if err = os.Remove(pending); err != nil {
						t.Fatal(err)
					}
				} else if err := PublishLifecycleControl(c.Kill, action); err != nil {
					t.Fatal(err)
				}
			}
			if variant == "partial-input" {
				var pid int
				waitLifecycleProbe(t, filepath.Join(root, "native-pid.json"), &pid)
				if pid != cmd.Process.Pid {
					t.Fatal("native PID changed")
				}
			}
			if c.End == "seal" {
				if e := os.WriteFile(c.Finish, nil, 0600); e != nil {
					t.Fatal(e)
				}
				var seal lifecycleProbeReceipt
				waitLifecycleProbe(t, filepath.Join(root, "sealed.json"), &seal)
				if !seal.Success || seal.OwnerPID != cmd.Process.Pid || cmd.Process.Signal(syscall.Signal(0)) != nil {
					t.Fatal("immutable seal ended the owned process")
				}
				if _, e := os.Lstat(filepath.Join(root, "receipt.json")); !os.IsNotExist(e) {
					t.Fatal("broker ended before original cleanup")
				}
			}
			if c.End == "seal" || c.End == "eof" {
				_ = in.Close()
			}
			if variant == "abort" {
				if e := os.WriteFile(c.Abort, nil, 0600); e != nil {
					t.Fatal(e)
				}
			}
			if c.End == "delete-conversation" && variant != "delete-without-checkpoint" {
				var ack LifecycleKill
				waitLifecycleProbe(t, c.Kill+".ready", &ack)
				if ack.PID != cmd.Process.Pid || ack.Signal != "delete-conversation" {
					t.Fatal("foreign original action ack")
				}
			}
			if (c.End == "kill" || c.End == "delete-conversation") && variant != "changed-input" && variant != "abort" {
				signal := syscall.SIGKILL
				if c.End == "delete-conversation" {
					signal = syscall.SIGTERM
				}
				if e := cmd.Process.Signal(signal); e != nil {
					t.Fatal(e)
				}
			}
			e = cmd.Wait()
			_ = in.Close()
			if (c.End == "seal" || c.End == "eof") && e != nil {
				t.Fatal("original stdin EOF did not shut down owned process", e)
			}
			if (c.End == "kill" || c.End == "delete-conversation") && variant != "changed-input" {
				status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
				if e == nil || !ok || !status.Signaled() || status.Signal() != func() syscall.Signal {
					if c.End == "delete-conversation" {
						return syscall.SIGTERM
					}
					return syscall.SIGKILL
				}() {
					t.Fatal("original kill did not terminate its owned PID")
				}
			}
			var r lifecycleProbeReceipt
			waitLifecycleProbe(t, filepath.Join(root, "receipt.json"), &r)
			want := strings.HasPrefix(variant, "capture") || strings.HasPrefix(variant, "replay")
			if r.Success != want || r.OwnerPID != cmd.Process.Pid {
				t.Fatalf("broker receipt success=%v frames=%d owner-match=%v error=%s", r.Success, r.Frames, r.OwnerPID == cmd.Process.Pid, r.Error)
			}
			if want {
				wantFrames := 2
				if c.End == "delete-conversation" {
					wantFrames = 3
				}
				if r.Frames != wantFrames {
					t.Fatal("incomplete pipe journal")
				}
				f, e := os.Open(c.Path)
				if e != nil {
					t.Fatal(e)
				}
				tape, e := DecodeLifecycle(f)
				_ = f.Close()
				if e != nil {
					t.Fatal(e)
				}
				if mode == "capture" {
					saved[c.End] = tape
				}
			}
			if !want && mode == "capture" {
				if _, e := os.Lstat(c.Path); !os.IsNotExist(e) {
					t.Fatal("failed native prefix published")
				}
			}
			if mode == "replay" {
				if _, e := os.Lstat(filepath.Join(root, "native-pid.json")); !os.IsNotExist(e) {
					t.Fatal("replay executed provider")
				}
			}
		})
	}
}

func TestLifecycleBlockedPipesFailWithinWriteDeadline(t *testing.T) {
	for _, side := range []string{"input-pressure", "output-pressure", "trickle-output"} {
		t.Run(side, func(t *testing.T) {
			root := t.TempDir()
			c := LifecycleBrokerConfig{PoolProcessConfig: PoolProcessConfig{Mode: "capture", Scenario: "lifecycle-" + side, Path: filepath.Join(root, "tape.json"), Claim: filepath.Join(root, "claim"), Finish: filepath.Join(root, "finish"), Producer: Producer{Revision: strings.Repeat("a", 40), BinarySHA256: strings.Repeat("b", 64), ClaudeVersion: "test-owned", CapturedAt: time.Now().UTC().Format(time.RFC3339Nano)}}, End: "kill", Kill: filepath.Join(root, "kill.json")}
			if side == "trickle-output" {
				c.Scenario = "lifecycle-output-pressure"
				c.Deadline = time.Now().Add(2 * time.Second)
			}
			path := filepath.Join(root, "job.json")
			if probeWrite(path, lifecycleProbeJob{Config: c, Root: root}) != nil {
				t.Fatal("job")
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestLifecycleProbeProcess$")
			cmd.Env = append(os.Environ(), "PYRY_LIFECYCLE_PROBE=exec", "PYRY_LIFECYCLE_PROBE_JOB="+path)
			in, e := cmd.StdinPipe()
			if e != nil {
				t.Fatal(e)
			}
			out, e := cmd.StdoutPipe()
			if e != nil {
				t.Fatal(e)
			}
			defer out.Close()
			if cmd.Start() != nil {
				t.Fatal("start")
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = in.Close() })
			request := `{"type":"user","message":{"content":"request"}}` + "\n"
			if side == "input-pressure" {
				request = `{"type":"user","message":{"content":"` + strings.Repeat("x", 1<<20) + `"}}` + "\n"
			}
			var progressed atomic.Int64
			var readDone chan struct{}
			if side == "trickle-output" {
				readDone = make(chan struct{})
				go func() {
					defer close(readDone)
					var b [1024]byte
					for {
						n, e := out.Read(b[:])
						progressed.Add(int64(n))
						if e != nil {
							return
						}
						time.Sleep(20 * time.Millisecond)
					}
				}()
			}
			sent := make(chan struct{})
			go func() { _, _ = io.WriteString(in, request); close(sent) }()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case e := <-done:
				if e == nil {
					t.Fatal("blocked pipe unexpectedly succeeded")
				}
			case <-time.After(func() time.Duration {
				if side == "trickle-output" {
					return 5 * time.Second
				}
				return 20 * time.Second
			}()):
				t.Fatal("write deadline did not stop blocked forwarding")
			}
			_ = out.Close()
			if readDone != nil {
				<-readDone
				if progressed.Load() == 0 {
					t.Fatal("trickling control observed no partial progress")
				}
			}
			_ = in.Close()
			<-sent
			var receipt lifecycleProbeReceipt
			waitLifecycleProbe(t, filepath.Join(root, "receipt.json"), &receipt)
			if receipt.Success || receipt.OwnerPID != cmd.Process.Pid {
				t.Fatal("blocked pipe accepted")
			}
			if _, e := os.Lstat(c.Path); !os.IsNotExist(e) {
				t.Fatal("blocked prefix published")
			}
		})
	}
}
func TestLifecycleFailedExecReapsDetachedBroker(t *testing.T) {
	root := t.TempDir()
	bad := filepath.Join(root, "not-an-executable-image")
	if os.WriteFile(bad, []byte("invalid image\n"), 0700) != nil {
		t.Fatal("image")
	}
	c := LifecycleBrokerConfig{PoolProcessConfig: PoolProcessConfig{Mode: "capture", Scenario: "lifecycle-exec-failure", Path: filepath.Join(root, "tape.json"), Claim: filepath.Join(root, "claim"), Finish: filepath.Join(root, "finish")}, End: "kill", Kill: filepath.Join(root, "kill.json")}
	path := filepath.Join(root, "job.json")
	if probeWrite(path, lifecycleProbeJob{Config: c, Root: root, Provider: bad}) != nil {
		t.Fatal("job")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLifecycleProbeProcess$")
	cmd.Env = append(os.Environ(), "PYRY_LIFECYCLE_PROBE=exec", "PYRY_LIFECYCLE_PROBE_JOB="+path)
	if cmd.Run() == nil {
		t.Fatal("failed exec succeeded")
	}
	var pid int
	waitLifecycleProbe(t, filepath.Join(root, "broker-pid.json"), &pid)
	if pid <= 1 || syscall.Kill(pid, 0) != syscall.ESRCH {
		t.Fatal("failed exec left detached broker alive or unreaped")
	}
	if _, e := os.Lstat(c.Path); !os.IsNotExist(e) {
		t.Fatal("failed exec published prefix")
	}
}

func TestLifecycleControlPublicationIsExclusiveAndClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "action")
	want := LifecycleKill{PID: 42, Signal: "delete-conversation"}
	if err := PublishLifecycleControl(path, want); err != nil {
		t.Fatal(err)
	}
	var got LifecycleKill
	if err := ReadLifecycleControl(path, 16384, &got); err != nil || got != want {
		t.Fatal("complete published control missing", err)
	}
	if _, err := os.Lstat(path + ".pending"); !os.IsNotExist(err) {
		t.Fatal("pending publication remains")
	}
	if err := PublishLifecycleControl(path, LifecycleKill{PID: 43, Signal: "delete-conversation"}); err == nil {
		t.Fatal("publication overwrote authorization")
	}
	if err := ReadLifecycleControl(path, 16384, &got); err != nil || got != want {
		t.Fatal("exclusive publication altered original", err)
	}
}
