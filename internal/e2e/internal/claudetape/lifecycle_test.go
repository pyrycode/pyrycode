package claudetape

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/agentrun"
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
	Config              LifecycleBrokerConfig
	Root                string
	Provider            string
	ContextCancel       bool
	ContextCancelAtSeal bool
	BrokerSocket        string
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
		if j.BrokerSocket != "" {
			e = ExecLifecycleChildViaBroker(j.BrokerSocket, path, provider, []string{"-test.run=^TestLifecycleProbeProcess$"}, j.Config.Mode == "capture")
		} else {
			e = ExecLifecycleChild([]string{"-test.run=^TestLifecycleProbeProcess$"}, env, provider, []string{"-test.run=^TestLifecycleProbeProcess$"}, j.Config.Mode == "capture")
		}
		if e != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "sentinel":
		for {
			time.Sleep(time.Hour)
		}
	case "native":
		if probeWrite(filepath.Join(j.Root, "native-pid.json"), os.Getpid()) != nil {
			os.Exit(2)
		}
		if j.BrokerSocket != "" {
			sentinel := exec.Command(os.Args[0], "-test.run=^TestLifecycleProbeProcess$")
			sentinel.Env = append(os.Environ(), "PYRY_LIFECYCLE_PROBE=sentinel")
			sentinel.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if sentinel.Start() != nil || probeWrite(filepath.Join(j.Root, "sentinel-pid.json"), sentinel.Process.Pid) != nil {
				os.Exit(2)
			}
			go func() {
				err := sentinel.Wait()
				status, ok := sentinel.ProcessState.Sys().(syscall.WaitStatus)
				_ = probeWrite(filepath.Join(j.Root, "sentinel-reaped.json"), err != nil && ok && status.Signaled() && status.Signal() == syscall.SIGKILL)
			}()
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
		j.Config.OnDiagnostic = func(d LifecycleDiagnostic) { _ = probeWrite(filepath.Join(j.Root, "diagnostic.json"), d) }
		if os.Getenv("PYRY_LIFECYCLE_PROBE_REJECT_TERMINAL") == "1" {
			j.Config.BeforeSeal = func(*Tape) error { return os.ErrInvalid }
		}
		if probeWrite(filepath.Join(j.Root, "broker-pid.json"), os.Getpid()) != nil {
			os.Exit(2)
		}
		j.Config.OnSeal = func(n int) error {
			return probeWrite(filepath.Join(j.Root, "sealed.json"), lifecycleProbeReceipt{Success: true, Frames: n, OwnerPID: j.Config.OwnerPID})
		}
		if j.ContextCancel || j.ContextCancelAtSeal {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			j.Config.Context = ctx
			if j.ContextCancelAtSeal {
				j.Config.BeforeSeal = func(*Tape) error { cancel(); return nil }
			}
			observer := func(_ []byte, forward func() error) error {
				time.AfterFunc(30*time.Millisecond, cancel)
				return forward()
			}
			if !j.ContextCancelAtSeal {
				if strings.Contains(j.Config.Scenario, "input-pressure") {
					j.Config.ForwardInput = observer
				} else {
					j.Config.ForwardOutput = observer
				}
			}
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
	for _, variant := range []string{"capture", "replay", "capture-eof", "replay-eof", "capture-seal", "replay-seal", "capture-seal-kill", "replay-seal-kill", "capture-seal-reap", "replay-seal-reap", "capture-rotate-reap", "replay-rotate-reap", "capture-idle-reap", "replay-idle-reap", "capture-delete", "replay-delete", "capture-delete-partial", "replay-delete-partial", "delete-without-checkpoint", "unauthorized-kill", "foreign-pid", "partial-input", "changed-input", "abort"} {
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
			if strings.Contains(variant, "-rotate") {
				c.End = "rotate-session"
				c.Scenario = "session-rotation-bootstrap"
			}
			if strings.Contains(variant, "-idle") {
				c.End = "idle-evict"
				c.Scenario = "session-resume-bootstrap"
			}
			if variant == "abort" {
				c.Abort = filepath.Join(root, "abort")
			}
			if strings.HasSuffix(variant, "-eof") {
				c.End = "eof"
			}
			if strings.Contains(variant, "-seal") {
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
			job := lifecycleProbeJob{Config: c, Root: root}
			var server *LifecycleBrokerServer
			if strings.HasSuffix(variant, "-reap") {
				job.Config.BrokerParentPID = os.Getpid()
				c.BrokerParentPID = os.Getpid()
				// macOS limits Unix socket paths to104bytes; TempDir names may be long.
				socketDir, err := os.MkdirTemp("/tmp", "lifecycle-broker-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
				job.BrokerSocket = filepath.Join(socketDir, "socket")
				brokerJob := filepath.Join(root, "broker-job.json")
				server, err = NewLifecycleBrokerServer(job.BrokerSocket, []string{brokerJob}, func(path string, peer int) (*exec.Cmd, io.Closer, error) {
					var got lifecycleProbeJob
					if ReadLifecycleControl(path, MaxBytes, &got) != nil {
						return nil, nil, os.ErrInvalid
					}
					expected := job
					expected.Config.OwnerPID = peer
					if !reflect.DeepEqual(got, expected) {
						return nil, nil, os.ErrInvalid
					}
					cmd := exec.Command(os.Args[0], "-test.run=^TestLifecycleProbeProcess$")
					cmd.Env = append(os.Environ(), "PYRY_LIFECYCLE_PROBE=broker", "PYRY_LIFECYCLE_PROBE_JOB="+path)
					return cmd, nil, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = server.Join(2 * time.Second) })
			}
			if e := probeWrite(jobPath, job); e != nil {
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
			if (c.End == "kill" || LifecycleAction(c.End) != "") && variant != "unauthorized-kill" && variant != "abort" && variant != "delete-without-checkpoint" {
				pid := cmd.Process.Pid
				if variant == "foreign-pid" {
					pid++
				}
				action := LifecycleKill{PID: pid, Signal: func() string {
					if LifecycleAction(c.End) != "" {
						return c.End
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

			if variant == "abort" {
				if e := os.WriteFile(c.Abort, nil, 0600); e != nil {
					t.Fatal(e)
				}
			}
			if LifecycleAction(c.End) != "" && variant != "delete-without-checkpoint" {
				var ack LifecycleKill
				waitLifecycleProbe(t, c.Kill+".ready", &ack)
				if ack.PID != cmd.Process.Pid || ack.Signal != c.End {
					t.Fatal("foreign original action ack")
				}
			}
			if LifecycleAction(c.End) != "" && variant != "delete-without-checkpoint" {
				// A metadata deletion leaves the native child alive. Its later
				// output and requests stay inside the strict journal until seal.
				if cmd.Process.Signal(syscall.Signal(0)) != nil {
					t.Fatal("metadata action stopped provider")
				}
				if _, e := io.WriteString(in, strings.Replace(request, "request", "tail request", 1)); e != nil {
					t.Fatal(e)
				}
				line := make(chan string, 1)
				go func() { v, _ := bufio.NewReader(out).ReadString('\n'); line <- v }()
				select {
				case v := <-line:
					var reply map[string]any
					if json.Unmarshal([]byte(v), &reply) != nil || reply["type"] != "assistant" {
						t.Fatal("post-deletion output lost")
					}
				case <-time.After(10 * time.Second):
					t.Fatal("post-deletion strict output absent")
				}
			}
			if c.End == "seal" || (c.End == "delete-conversation" && variant != "delete-without-checkpoint") {
				if e := os.WriteFile(c.Finish, nil, 0600); e != nil {
					t.Fatal(e)
				}
				var seal lifecycleProbeReceipt
				waitLifecycleProbe(t, filepath.Join(root, "sealed.json"), &seal)
				if !seal.Success || seal.OwnerPID != cmd.Process.Pid || cmd.Process.Signal(syscall.Signal(0)) != nil {
					t.Fatal("immutable prefix seal ended owned provider")
				}
				if _, e := os.Lstat(filepath.Join(root, "receipt.json")); !os.IsNotExist(e) {
					t.Fatal("broker ended before original cleanup")
				}
			}
			if strings.HasSuffix(variant, "-reap") {
				if mode == "capture" {
					var sentinelPID int
					waitLifecycleProbe(t, filepath.Join(root, "sentinel-pid.json"), &sentinelPID)
					if sentinelPID <= 1 || syscall.Kill(sentinelPID, 0) != nil {
						t.Fatal("owned detached descendant absent before reap")
					}
				}
				agentrun.ReapDescendantGroups(cmd.Process.Pid, slog.New(slog.NewTextHandler(io.Discard, nil)))
				if mode == "capture" {
					var reaped bool
					waitLifecycleProbe(t, filepath.Join(root, "sentinel-reaped.json"), &reaped)
					if !reaped {
						t.Fatal("production descendant reaper did not kill owned witness")
					}
				}
				var brokerPID int
				waitLifecycleProbe(t, filepath.Join(root, "broker-pid.json"), &brokerPID)
				if syscall.Kill(brokerPID, 0) != nil {
					t.Fatal("production reaper killed outer-owned broker")
				}
				if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			} else if strings.HasSuffix(variant, "-seal-kill") {
				// Keep the owner's input writer open while it waits for output
				// drain, just as exec.Cmd.Wait may do after killing the child.
				if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
			} else if c.End == "seal" || c.End == "eof" || c.End == "delete-conversation" {
				_ = in.Close()
			}
			if c.End == "kill" && variant != "changed-input" && variant != "abort" {
				if e := cmd.Process.Signal(syscall.SIGKILL); e != nil {
					t.Fatal(e)
				}
			}
			e = cmd.Wait()
			_ = in.Close()
			if (c.End == "seal" || c.End == "eof" || (c.End == "delete-conversation" && variant != "delete-without-checkpoint")) && e != nil && !strings.HasSuffix(variant, "-seal-kill") && !strings.HasSuffix(variant, "-reap") {
				t.Fatal("original stdin EOF did not shut down owned process", e)
			}
			if c.End == "kill" && variant != "changed-input" {
				status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
				if e == nil || !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
					t.Fatal("original kill did not terminate ownedPID")
				}
			}
			if strings.HasSuffix(variant, "-reap") {
				status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
				if e == nil || !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
					t.Fatal("original owner signal boundary changed")
				}
				if err := server.Join(5 * time.Second); err != nil {
					t.Fatal(err)
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
				if LifecycleAction(c.End) != "" {
					wantFrames = 5
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

func TestLifecycleContextInterruptsActualBlockedWriters(t *testing.T) {
	for _, side := range []string{"input-pressure", "output-pressure", "cancel-at-seal"} {
		t.Run(side, func(t *testing.T) {
			root := t.TempDir()
			c := LifecycleBrokerConfig{PoolProcessConfig: PoolProcessConfig{Mode: "capture", Scenario: "lifecycle-" + side, Path: filepath.Join(root, "tape.json"), Claim: filepath.Join(root, "claim"), Finish: filepath.Join(root, "finish"), Producer: Producer{Revision: strings.Repeat("a", 40), BinarySHA256: strings.Repeat("b", 64), ClaudeVersion: "test-owned", CapturedAt: time.Now().UTC().Format(time.RFC3339Nano)}}, End: "kill", Kill: filepath.Join(root, "kill.json")}
			if side == "cancel-at-seal" {
				c.End = "seal"
			}
			path := filepath.Join(root, "job.json")
			if probeWrite(path, lifecycleProbeJob{Config: c, Root: root, ContextCancel: side != "cancel-at-seal", ContextCancelAtSeal: side == "cancel-at-seal"}) != nil {
				t.Fatal("job")
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestLifecycleProbeProcess$")
			cmd.Env = append(os.Environ(), "PYRY_LIFECYCLE_PROBE=exec", "PYRY_LIFECYCLE_PROBE_JOB="+path)
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			if cmd.Start() != nil {
				t.Fatal("start")
			}
			waited := false
			defer func() {
				if !waited {
					cmd.Process.Kill()
					cmd.Wait()
				}
			}()
			request := `{"type":"user","message":{"content":"request"}}` + "\n"
			if side == "input-pressure" {
				request = `{"type":"user","message":{"content":"` + strings.Repeat("x", 1<<20) + `"}}` + "\n"
			}
			sent := make(chan struct{})
			go func() { io.WriteString(input, request); close(sent) }()
			if side == "cancel-at-seal" {
				if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || !strings.Contains(line, "native reply") {
					t.Fatal("source reply", err)
				}
				if os.WriteFile(c.Finish, nil, 0600) != nil {
					t.Fatal("finish")
				}
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				waited = true
				if err == nil {
					t.Fatal("cancelled actual writer succeeded")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("context cancellation waited for default10s write deadline")
			}
			input.Close()
			<-sent
			var receipt lifecycleProbeReceipt
			waitLifecycleProbe(t, filepath.Join(root, "receipt.json"), &receipt)
			if side == "cancel-at-seal" {
				if _, err := os.Lstat(filepath.Join(root, "sealed.json")); !os.IsNotExist(err) {
					t.Fatal("cancelled prefix advertised success")
				}
			}
			if receipt.Success || receipt.OwnerPID != cmd.Process.Pid {
				t.Fatal("cancelled prefix accepted")
			}
			if _, err := os.Lstat(c.Path); !os.IsNotExist(err) {
				t.Fatal("cancelled prefix published")
			}
		})
	}
}
