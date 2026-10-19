//go:build e2e_realclaude

package realclaude

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

type lifecycleRecordingConfig struct {
	Process                                                          claudetape.PoolProcessConfig
	Root, Receipt, Wrapper, Daemon, DaemonSHA256, SourceBinarySHA256 string
}

func readLifecycleRecordingConfig(path string) (lifecycleRecordingConfig, error) {
	var c lifecycleRecordingConfig
	e := claudetape.ReadLifecycleControl(path, 16384, &c)
	if e != nil {
		return c, e
	}
	p, ok := claudetape.LifecycleProfiles()[c.Process.Scenario]
	if !ok || p.Root != c.Root || poolRecordedRevision == "" || c.Process.Producer.Revision != poolRecordedRevision || !filepath.IsAbs(c.Process.Home) || c.Process.WorkDir != filepath.Join(c.Process.Home, "work") || c.Process.Finish != filepath.Join(c.Process.Home, "finish") || c.Receipt != filepath.Join(c.Process.Home, "receipt") || c.Wrapper != filepath.Join(c.Process.Home, "recording-bin", "claude") || !filepath.IsAbs(c.Daemon) || len(c.SourceBinarySHA256) != 64 || len(c.DaemonSHA256) != 64 {
		return c, os.ErrInvalid
	}
	exe, e := os.Executable()
	if e != nil {
		return c, os.ErrInvalid
	}
	f, e := os.Open(exe)
	if e != nil {
		return c, os.ErrInvalid
	}
	h := sha256.New()
	_, e = io.Copy(h, f)
	_ = f.Close()
	if e != nil || hex.EncodeToString(h.Sum(nil)) != c.SourceBinarySHA256 {
		return c, os.ErrInvalid
	}
	if c.Process.Mode == "replay" {
		for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR"} {
			if os.Getenv(k) != "" {
				return c, os.ErrInvalid
			}
		}
	} else if c.Process.Mode != "capture" || os.Getenv("CLAUDE_CONFIG_DIR") != "" || (os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "") || c.Process.Producer.BinarySHA256 != c.SourceBinarySHA256 {
		return c, os.ErrInvalid
	}
	return c, nil
}
func lifecycleRecordingCurrent(t *testing.T) (lifecycleRecordingConfig, bool) {
	t.Helper()
	path := os.Getenv("PYRY_LIFECYCLE_RECORDING_CONFIG")
	if path == "" {
		return lifecycleRecordingConfig{}, false
	}
	c, e := readLifecycleRecordingConfig(path)
	if e != nil || c.Root != t.Name() {
		t.Fatal("lifecycle recording does not own original root")
	}
	return c, true
}
func lifecycleProviderEnvironment(t *testing.T) (string, string, bool) {
	t.Helper()
	c, ok := lifecycleRecordingCurrent(t)
	if !ok {
		return "", "", false
	}
	for _, dir := range []string{c.Process.Home, filepath.Join(c.Process.Home, "recording-roles"), filepath.Join(c.Process.Home, "recording-children")} {
		info, e := os.Lstat(dir)
		if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatal("lifecycle directory is not private")
		}
	}
	return c.Wrapper, c.Process.Home, true
}
func lifecycleRecordingRegisterSocket(t *testing.T, home, daemon, socket string) {
	t.Helper()
	c, ok := lifecycleRecordingCurrent(t)
	if !ok {
		return
	}
	raw, e := os.ReadFile(daemon)
	sum := sha256.Sum256(raw)
	if e != nil || home != c.Process.Home || daemon != c.Daemon || !filepath.IsAbs(socket) || hex.EncodeToString(sum[:]) != c.DaemonSHA256 {
		t.Fatal("lifecycle endpoint changed")
	}
	if daemonRecordingWrite(filepath.Join(home, "recording-endpoint.json"), daemonRecordingEndpoint{daemon, socket}) != nil {
		t.Fatal("lifecycle endpoint rebound")
	}
}
func lifecycleRecordingRegisterConversation(t *testing.T, conv string) {
	t.Helper()
	c, ok := lifecycleRecordingCurrent(t)
	if !ok {
		return
	}
	var registry struct {
		Conversations []struct {
			ID, Cwd string
			Session string `json:"current_session_id"`
		} `json:"conversations"`
	}
	raw, e := os.ReadFile(filepath.Join(c.Process.Home, ".pyry", "test", "conversations.json"))
	if e != nil || json.Unmarshal(raw, &registry) != nil {
		t.Fatal("cannot validate lifecycle conversation ownership")
	}
	role := daemonRecordingRole{Role: "created-1", ConversationID: conv}
	for _, row := range registry.Conversations {
		if row.ID == conv {
			if role.SessionID != "" {
				t.Fatal("duplicate lifecycle conversation")
			}
			role.SessionID, role.WorkDir = row.Session, row.Cwd
		}
	}
	if role.SessionID == "" || role.SessionID == claudetape.LifecycleBootstrapSession || role.WorkDir != filepath.Join(c.Process.Home, "work") {
		t.Fatal("lifecycle conversation lacks its independent session")
	}
	if daemonRecordingWrite(filepath.Join(c.Process.Home, "recording-roles", "created-1.json"), role) != nil {
		t.Fatal("additional lifecycle conversation")
	}
}
func lifecycleRecordingBeforeDelete(t *testing.T, home, conv string) {
	t.Helper()
	c, ok := lifecycleRecordingCurrent(t)
	if !ok {
		return
	}
	var role daemonRecordingRole
	if home != c.Process.Home || daemonRecordingRead(filepath.Join(home, "recording-roles", "created-1.json"), &role) != nil || role.ConversationID != conv {
		t.Fatal("lifecycle deletion escaped original conversation")
	}
	prefix := filepath.Join(home, "recording-children", "created-1")
	job, e := readLifecycleJob(prefix + ".job")
	if e != nil || job.Role != role.Role || job.SessionID != role.SessionID || job.ConversationID != conv || job.Config.OwnerPID <= 1 {
		t.Fatal("lifecycle deletion lacks owned provider launch")
	}
	if syscall.Kill(job.Config.OwnerPID, 0) != nil {
		t.Fatal("lifecycle child was not alive before original deletion")
	}
	// This marks the unchanged original wire action; it sends no signal itself.
	if claudetape.PublishLifecycleControl(prefix+".action", claudetape.LifecycleKill{PID: job.Config.OwnerPID, Signal: "delete-conversation"}) != nil {
		t.Fatal("lifecycle deletion checkpoint repeated")
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var ack claudetape.LifecycleKill
		if claudetape.ReadLifecycleControl(prefix+".action.ready", 16384, &ack) == nil {
			if ack.PID != job.Config.OwnerPID || ack.Signal != "delete-conversation" {
				t.Fatal("foreign lifecycle action acknowledgement")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("broker did not acknowledge original delete checkpoint")

}
func waitLifecycleReceipt(t *testing.T, path string) claudetape.LifecycleReceipt {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var r claudetape.LifecycleReceipt
		if daemonRecordingRead(path, &r) == nil {
			if !r.Success || r.Error != "" || r.Frames == 0 {
				t.Fatal("lifecycle child did not complete its declared boundary")
			}
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("lifecycle child boundary timed out")
	return claudetape.LifecycleReceipt{}
}
func finishLifecycleRecording(t *testing.T) {
	t.Helper()
	c, ok := lifecycleRecordingCurrent(t)
	if !ok || t.Failed() {
		return
	}
	_ = waitLifecycleReceipt(t, filepath.Join(c.Process.Home, "recording-children", "created-1.receipt"))
	prefix := filepath.Join(c.Process.Home, "recording-children", "created-1")
	job, err := readLifecycleJob(prefix + ".job")
	if err != nil || job.Config.OwnerPID <= 1 {
		t.Fatal("missing stopped lifecycle launch")
	}
	deadline := time.Now().Add(15 * time.Second)
	for syscall.Kill(job.Config.OwnerPID, 0) != syscall.ESRCH {
		if time.Now().After(deadline) {
			t.Fatal("original deletion left its owned process alive")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if daemonRecordingWrite(prefix+".stopped", claudetape.LifecycleStopObservation{PID: job.Config.OwnerPID, Kind: "not-running"}) != nil {
		t.Fatal("lifecycle stop witness repeated")
	}
	var bootstrap claudetape.LifecycleJob
	if claudetape.ReadLifecycleControl(filepath.Join(c.Process.Home, "recording-children", "bootstrap.job"), claudetape.MaxBytes, &bootstrap) != nil || bootstrap.Config.OwnerPID <= 1 || syscall.Kill(bootstrap.Config.OwnerPID, 0) != nil {
		t.Fatal("bootstrap child was not alive at original prefix seal")
	}

	if daemonRecordingWrite(c.Process.Finish, struct{}{}) != nil {
		t.Fatal("lifecycle prefix seal repeated")
	}
	_ = waitLifecycleReceipt(t, filepath.Join(c.Process.Home, "recording-children", "bootstrap.prefix"))
	digest, _, e := claudetape.AuditLifecycleArtifacts(c.Process.Home, c.Process.Scenario, poolRecordedRevision, c.SourceBinarySHA256, c.DaemonSHA256, false)
	if e != nil {
		t.Fatal(e)
	}
	if daemonRecordingWrite(c.Receipt, daemonRecordingReceipt{Success: true, Frames: 2, ArtifactSHA256: digest}) != nil {
		t.Fatal("lifecycle root receipt repeated")
	}
}
func readLifecycleJob(path string) (claudetape.LifecycleJob, error) {
	var j claudetape.LifecycleJob
	err := claudetape.ReadLifecycleControl(path, claudetape.MaxBytes, &j)
	return j, err
}
func TestLifecycleTapeHelperProcess(t *testing.T) {
	path := os.Getenv("PYRY_LIFECYCLE_RECORDING_CONFIG")
	if path == "" {
		return
	}
	c, e := readLifecycleRecordingConfig(path)
	if e != nil {
		os.Exit(2)
	}
	fail := func() {
		_ = os.WriteFile(filepath.Join(c.Process.Home, "recording-unexpected"), []byte("unowned lifecycle invocation"), 0600)
		os.Exit(2)
	}
	var args []string
	for i, a := range os.Args {
		if a == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(os.Stdout, c.Process.Producer.ClaudeVersion)
		os.Exit(0)
	}
	var endpoint daemonRecordingEndpoint
	if daemonRecordingRead(filepath.Join(c.Process.Home, "recording-endpoint.json"), &endpoint) != nil || endpoint.Daemon != c.Daemon {
		fail()
	}
	session := ""
	for i, a := range args {
		if a == "--session-id" && i+1 < len(args) {
			if session != "" {
				fail()
			}
			session = args[i+1]
		}
	}
	role := daemonRecordingRole{Role: "bootstrap", SessionID: claudetape.LifecycleBootstrapSession, WorkDir: c.Process.WorkDir}
	if session != claudetape.LifecycleBootstrapSession {
		if daemonRecordingRead(filepath.Join(c.Process.Home, "recording-roles", "created-1.json"), &role) != nil || role.Role != "created-1" || role.SessionID != session {
			fail()
		}
	}
	if session == "" || os.Getenv("HOME") != c.Process.Home {
		fail()
	}
	cwd, e := os.Getwd()
	if e != nil || cwd != role.WorkDir {
		fail()
	}
	prefix := filepath.Join(c.Process.Home, "recording-children", role.Role)
	process := c.Process
	process.Scenario += "-" + role.Role
	process.Path = prefix + ".json"
	process.Claim = prefix + ".claim"
	process.WorkDir = role.WorkDir
	job := claudetape.LifecycleJob{Config: claudetape.LifecycleBrokerConfig{PoolProcessConfig: process, OwnerPID: os.Getpid(), End: claudetape.LifecycleEnd(c.Process.Scenario, role.Role), Kill: prefix + ".action", Abort: prefix + ".abort"}, Args: args, Role: role.Role, ConversationID: role.ConversationID, SessionID: role.SessionID, WorkDir: role.WorkDir, Daemon: endpoint.Daemon, Socket: endpoint.Socket, SourceBinarySHA256: c.SourceBinarySHA256}
	if daemonRecordingWrite(prefix+".job", job) != nil {
		fail()
	}
	if claudetape.ExecLifecycleChild([]string{"-test.run=^TestLifecycleTapeBrokerProcess$", "--", prefix + ".job"}, os.Environ(), process.Claude, args, process.Mode == "capture") != nil {
		fail()
	}
	os.Exit(0)
}
func TestLifecycleTapeBrokerProcess(t *testing.T) {
	path := os.Getenv("PYRY_LIFECYCLE_RECORDING_CONFIG")
	if path == "" {
		return
	}
	c, e := readLifecycleRecordingConfig(path)
	if e != nil {
		os.Exit(2)
	}
	jobPath := ""
	for i, a := range os.Args {
		if a == "--" && i+2 == len(os.Args) {
			jobPath = os.Args[i+1]
		}
	}
	job, e := readLifecycleJob(jobPath)
	if e != nil || claudetape.LifecycleEnd(c.Process.Scenario, job.Role) == "" || jobPath != filepath.Join(c.Process.Home, "recording-children", job.Role+".job") || job.SourceBinarySHA256 != c.SourceBinarySHA256 || job.Config.OwnerPID != os.Getppid() || job.Config.Home != c.Process.Home || job.Config.Producer.Revision != poolRecordedRevision {
		os.Exit(2)
	}
	prefix := filepath.Join(c.Process.Home, "recording-children", job.Role)
	if daemonRecordingWrite(prefix+".broker-pid", os.Getpid()) != nil {
		os.Exit(2)
	}
	b, e := claudetape.NewDaemonBinding(job.Args, c.Process.Home, job.WorkDir, job.Daemon, job.Socket)
	if e != nil {
		os.Exit(2)
	}
	receipt := func(n int, err error) claudetape.LifecycleReceipt {
		r := claudetape.LifecycleReceipt{Success: err == nil, Frames: n, OwnerPID: job.Config.OwnerPID, BrokerPID: os.Getpid()}
		if err != nil {
			r.Error = "lifecycle boundary failed"
		}
		return r
	}
	job.Config.OnSeal = func(n int) error { return daemonRecordingWrite(prefix+".prefix", receipt(n, nil)) }
	n, e := claudetape.RunLifecycleBroker(job.Config, b)
	if daemonRecordingWrite(prefix+".receipt", receipt(n, e)) != nil {
		os.Exit(2)
	}
	if e != nil {
		_ = os.WriteFile(filepath.Join(c.Process.Home, "recording-unexpected"), []byte("lifecycle broker failed"), 0600)
		os.Exit(2)
	}
	os.Exit(0)
}
