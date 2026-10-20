//go:build e2e_realclaude

package realclaude

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
	"github.com/pyrycode/pyrycode/internal/e2emcp"
)

type lifecycleRecordingConfig struct {
	WorkerWaits                                                      []*claudetape.FallbackProductionWait `json:",omitempty"`
	BrokerParentPID                                                  int
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
	if !ok || c.BrokerParentPID <= 1 || p.Root != c.Root || poolRecordedRevision == "" || c.Process.Producer.Revision != poolRecordedRevision || !filepath.IsAbs(c.Process.Home) || c.Process.WorkDir != filepath.Join(c.Process.Home, "work") || c.Process.Finish != filepath.Join(c.Process.Home, "finish") || c.Receipt != filepath.Join(c.Process.Home, "receipt") || c.Wrapper != filepath.Join(c.Process.Home, "recording-bin", "claude") || !filepath.IsAbs(c.Daemon) || len(c.SourceBinarySHA256) != 64 || len(c.DaemonSHA256) != 64 {
		return c, os.ErrInvalid
	}
	if len(c.WorkerWaits) > claudetape.LifecycleWorkerLimit(c.Process.Scenario) || c.Process.Mode == "capture" && len(c.WorkerWaits) != 0 {
		return c, os.ErrInvalid
	}
	for _, wait := range c.WorkerWaits {
		if claudetape.ValidateFallbackProductionWait(claudetape.FallbackWorkerStatus{ExitCode: 0}, wait) != nil {
			return c, os.ErrInvalid
		}
	}
	if lifecycleRecordingSource(c.SourceBinarySHA256) != nil {
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
	if c.Process.Scenario == "fallback-suggestion" {
		return lifecycleRecordingSuggestProvider(t, c), c.Process.Home, true
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
	if c.Process.Scenario == "conversation-active-switch" {
		var prior claudetape.LifecycleRoleBinding
		path := filepath.Join(c.Process.Home, "recording-roles", "created-1.json")
		if _, err := os.Lstat(path); err == nil {
			var e error
			prior, e = claudetape.ReadLifecycleRole(c.Process.Home, c.Process.Scenario, "created-1")
			if e != nil || prior.ConversationID == conv {
				t.Fatal("active switch repeated or foreign first conversation")
			}
			role.Role = "created-2"
		} else if !os.IsNotExist(err) {
			t.Fatal("active switch role inventory unreadable")
		}
	}
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
	if daemonRecordingWrite(filepath.Join(c.Process.Home, "recording-roles", role.Role+".json"), role) != nil {
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
	started := time.Now()
	// Observe entry without repeating the expensive executable validation.
	var observed lifecycleRecordingConfig
	if claudetape.ReadLifecycleControl(os.Getenv("PYRY_LIFECYCLE_RECORDING_CONFIG"), 16384, &observed) == nil && observed.Process.Scenario == "session-resume" {
		observeResumeSeal(t, observed, "finish-entry", started)
	}
	c, ok := lifecycleRecordingCurrent(t)
	if !ok || t.Failed() {
		return
	}
	if c.Process.Scenario == "session-resume" {
		observeResumeSeal(t, c, "config-validated", started)
		defer observeResumeSeal(t, c, "finish-return", started)
	}
	settleLifecycleRecording(t, c)
	profile := claudetape.LifecycleProfiles()[c.Process.Scenario]
	for _, role := range profile.Roles {
		prefix := filepath.Join(c.Process.Home, "recording-children", role)
		job, err := readLifecycleJob(prefix + ".job")
		if err != nil || job.Config.OwnerPID <= 1 {
			t.Fatal("missing lifecycle producer")
		}
		if claudetape.LifecycleSeals(job.Config.End) {
			if syscall.Kill(job.Config.OwnerPID, 0) != nil {
				t.Fatal("owned provider was not alive at original prefix seal")
			}
		} else {
			_ = waitLifecycleReceipt(t, prefix+".receipt")
			if syscall.Kill(job.Config.OwnerPID, 0) != syscall.ESRCH {
				t.Fatal("original rotation left its predecessor running")
			}
			if claudetape.PublishLifecycleControl(prefix+".stopped", claudetape.LifecycleStopObservation{PID: job.Config.OwnerPID, Kind: "not-running"}) != nil {
				t.Fatal("rotation stop witness repeated")
			}
		}
	}
	if daemonRecordingWrite(c.Process.Finish, struct{}{}) != nil {
		t.Fatal("lifecycle prefix seal repeated")
	}
	if c.Process.Scenario == "session-resume" {
		observeResumeSeal(t, c, "finish-published", started)
	}
	for _, role := range profile.Roles {
		if claudetape.LifecycleSeals(claudetape.LifecycleEnd(c.Process.Scenario, role)) {
			_ = waitLifecycleReceipt(t, filepath.Join(c.Process.Home, "recording-children", role+".prefix"))
		}
	}
	digest, _, e := claudetape.AuditLifecycleArtifacts(c.Process.Home, c.Process.Scenario, poolRecordedRevision, c.SourceBinarySHA256, c.DaemonSHA256, c.BrokerParentPID, false)
	if c.Process.Scenario == "session-resume" {
		observeResumeSeal(t, c, "prefix-audited", started)
	}
	if e != nil {
		t.Fatal(e)
	}
	if daemonRecordingWrite(c.Receipt, daemonRecordingReceipt{Success: true, Frames: len(profile.Roles), ArtifactSHA256: digest}) != nil {
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
	var args []string
	stage := "endpoint"
	fail := func() {
		if claudetape.MCPScenario(c.Process.Scenario) {
			cwd, _ := os.Getwd()
			diagnostic := claudetape.MCPInvocationFailure{Kind: "failed-unpromotable-mcp-invocation-v1", Stage: stage, Mode: c.Process.Mode, Scenario: c.Process.Scenario, Revision: c.Process.Producer.Revision, SourceBinarySHA256: c.SourceBinarySHA256, PID: os.Getpid(), ParentPID: os.Getppid(), Args: append([]string(nil), args...), Cwd: cwd, HomeMatches: os.Getenv("HOME") == c.Process.Home}
			if evidence := os.Getenv("PYRY_CLAUDE_TAPE_EVIDENCE"); filepath.IsAbs(evidence) {
				_ = claudetape.PublishMCPInvocationFailure(filepath.Join(evidence, c.Process.Scenario+"-invocation-failed.json"), diagnostic)
			}
		}
		_ = os.WriteFile(filepath.Join(c.Process.Home, "recording-unexpected"), []byte("unowned lifecycle invocation stage="+stage), 0600)
		os.Exit(2)
	}
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
	if lifecycleRecordingFiniteWorker(c, args) {
		return
	}
	stage = "session-flags"
	session := ""
	for i, a := range args {
		if (a == "--session-id" || a == "--resume") && i+1 < len(args) {
			if session != "" {
				fail()
			}
			session = args[i+1]
		}
	}
	stage = "role-binding"
	var role claudetape.LifecycleRoleBinding
	if c.Process.Scenario == "conversation-system-prompt" || c.Process.Scenario == "conversation-active-switch" {
		role, e = lifecycleRecordingMappedRole(c, session)
		if e != nil {
			fail()
		}
	} else if c.Process.Scenario == "session-resume" {
		role, e = lifecycleRecordingResumeRole(c, session, args)
		if e != nil {
			fail()
		}
	} else if _, _, _, reset := claudetape.LifecycleResetSeeds(c.Process.Scenario); reset {
		role, e = lifecycleRecordingResetRole(c, session)
		if e != nil {
			fail()
		}
	} else if c.Process.Scenario == "session-rotation" {
		role, e = lifecycleRecordingRotationRole(c, session)
		if e != nil {
			fail()
		}
	} else {
		role, e = claudetape.ReadLifecycleRole(c.Process.Home, c.Process.Scenario, "bootstrap")
		if e != nil {
			fail()
		}
		if session != role.SessionID {
			stage = "unmapped-session"
			role, e = claudetape.ReadLifecycleRole(c.Process.Home, c.Process.Scenario, "created-1")
			if e != nil || role.SessionID != session {
				fail()
			}
		}
	}
	if session == "" {
		stage = "empty-session"
		fail()
	}
	if os.Getenv("HOME") != c.Process.Home {
		stage = "home"
		fail()
	}
	stage = "cwd"
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
	job := claudetape.LifecycleJob{Config: claudetape.LifecycleBrokerConfig{PoolProcessConfig: process, OwnerPID: os.Getpid(), BrokerParentPID: c.BrokerParentPID, End: claudetape.LifecycleEnd(c.Process.Scenario, role.Role), Kill: prefix + ".action", Abort: prefix + ".abort"}, Args: args, Role: role.Role, ConversationID: role.ConversationID, SessionID: role.SessionID, WorkDir: role.WorkDir, Daemon: endpoint.Daemon, Socket: endpoint.Socket, SourceBinarySHA256: c.SourceBinarySHA256}
	stage = "job-publish"
	if daemonRecordingWrite(prefix+".job", job) != nil {
		fail()
	}
	var setup func(io.Reader) error
	if claudetape.MCPScenario(c.Process.Scenario) {
		setup = func(r io.Reader) error {
			capability, err := e2emcp.ReadCapability(r)
			if err != nil || capability.Mode != process.Mode {
				return os.ErrInvalid
			}
			if process.Mode == "capture" {
				if os.Setenv(e2emcp.SocketEnv, capability.Socket) != nil || os.Setenv(e2emcp.TokenEnv, capability.Token) != nil {
					return os.ErrInvalid
				}
			}
			return nil
		}
	}
	stage = "ready"
	if claudetape.ExecLifecycleChildViaBrokerReady(filepath.Join(c.Process.Home, "broker.sock"), prefix+".job", process.Claude, args, process.Mode == "capture", setup) != nil {
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
	if e != nil || claudetape.LifecycleEnd(c.Process.Scenario, job.Role) == "" || jobPath != filepath.Join(c.Process.Home, "recording-children", job.Role+".job") || job.SourceBinarySHA256 != c.SourceBinarySHA256 || job.Config.BrokerParentPID != c.BrokerParentPID || os.Getppid() != c.BrokerParentPID || job.Config.OwnerPID <= 1 || job.Config.Home != c.Process.Home || job.Config.Producer.Revision != poolRecordedRevision {
		os.Exit(2)
	}
	prefix := filepath.Join(c.Process.Home, "recording-children", job.Role)
	if claudetape.PublishLifecycleControl(prefix+".broker-pid", os.Getpid()) != nil {
		os.Exit(2)
	}
	b, e := claudetape.NewLifecycleDaemonBinding(job.Args, c.Process.Home, job.WorkDir, job.Daemon, job.Socket, c.Process.Scenario, job.Role)
	if e != nil {
		os.Exit(2)
	}
	receipt := func(n int, err error) claudetape.LifecycleReceipt {
		r := claudetape.LifecycleReceipt{Success: err == nil, Frames: n, OwnerPID: job.Config.OwnerPID, BrokerPID: os.Getpid()}
		if err != nil {
			r.Error = "lifecycle boundary failed"
			var timing *claudetape.LifecycleTimingError
			if errors.As(err, &timing) {
				r.Timing = timing
			}
			switch err.Error() {
			case "native output timeline missed absolute due time", "native output timeline scheduling drift", "native output timeline forwarding drift":
				r.Error = err.Error()
			}
		}
		return r
	}
	job.Config.OnSeal = func(n int) error { return daemonRecordingWrite(prefix+".prefix", receipt(n, nil)) }
	job.Config.OnInput = lifecycleRecordingInputWitness(job, c.Process.Home, c.Process.Scenario)
	var physical *claudetape.PhysicalRecordingSession
	if claudetape.PhysicalFamily(c.Process.Scenario) != "" {
		binding, err := claudetape.ReadLifecycleRole(c.Process.Home, c.Process.Scenario, job.Role)
		if err != nil {
			os.Exit(2)
		}
		physical, err = claudetape.NewPhysicalRecordingSession(job.Config, binding, c.SourceBinarySHA256, os.Getpid())
		if err != nil {
			os.Exit(2)
		}
		priorInput := job.Config.OnInput
		job.Config.OnInput = func(raw []byte) error {
			if priorInput != nil {
				if err := priorInput(raw); err != nil {
					return err
				}
			}
			actual, err := b.Frame(raw, true)
			if err != nil {
				return err
			}
			return physical.Input(actual)
		}
		job.Config.OnFailure = func(tape *claudetape.LifecycleTape, frames int) {
			_ = claudetape.RetainPhysicalFailedCapture(prefix+".failed-native", job.Config, c.SourceBinarySHA256, tape, frames)
		}
		job.Config.BeforeOutput = physical.Output
		job.Config.BeforeSeal = physical.Seal
	}
	job.Config.OnTerminalState = func(input, output, diagnostic, sealed bool) {
		if c.Process.Scenario == "session-resume" && os.Getenv("PYRY_CLAUDE_TAPE_EVIDENCE") != "" {
			path := filepath.Join(os.Getenv("PYRY_CLAUDE_TAPE_EVIDENCE"), "session-resume-"+job.Role+"-terminal.json")
			if daemonRecordingWrite(path, struct {
				OwnerPID, BrokerPID                    int
				InputEOF, OutputEOF, StderrEOF, Sealed bool
			}{job.Config.OwnerPID, os.Getpid(), input, output, diagnostic, sealed}) != nil {
				os.Exit(2)
			}
		}
	}
	var mcp *claudetape.MCPRecordingSession
	if claudetape.MCPScenario(c.Process.Scenario) {
		binding, err := claudetape.ReadLifecycleRole(c.Process.Home, c.Process.Scenario, job.Role)
		if err != nil {
			os.Exit(2)
		}
		mcp, err = claudetape.NewMCPRecordingSession(job.Config, binding, b, c.SourceBinarySHA256, c.DaemonSHA256, os.Getpid())
		if err != nil {
			os.Exit(2)
		}
		if mcp.Configure(&job.Config) != nil {
			_ = mcp.Close(false)
			os.Exit(2)
		}
		job.Config.OnFailure = func(tape *claudetape.LifecycleTape, frames int) { _ = mcp.RetainFailure(tape, frames) }
	}
	if c.Process.Scenario == "clear-reset" && os.Getenv("PYRY_CLAUDE_TAPE_EVIDENCE") != "" {
		job.Config.OnProgress = func(progress claudetape.LifecycleProgress) {
			path := filepath.Join(os.Getenv("PYRY_CLAUDE_TAPE_EVIDENCE"), "clear-reset-"+job.Role+"-terminal.json")
			if daemonRecordingWrite(path, struct {
				OwnerPID, BrokerPID int
				OwnerPresent        bool
				Progress            claudetape.LifecycleProgress
			}{job.Config.OwnerPID, os.Getpid(), syscall.Kill(job.Config.OwnerPID, 0) == nil, progress}) != nil {
				os.Exit(2)
			}
		}
	}
	n, e := claudetape.RunLifecycleBroker(job.Config, b)
	if mcp != nil {
		if err := mcp.Close(e == nil); err != nil && e == nil {
			e = err
		}
	}

	if physical != nil {
		if err := physical.Close(); err != nil && e == nil {
			e = err
		}
	}
	if daemonRecordingWrite(prefix+".receipt", receipt(n, e)) != nil {
		os.Exit(2)
	}
	if e != nil {
		_ = os.WriteFile(filepath.Join(c.Process.Home, "recording-unexpected"), []byte("lifecycle broker failed"), 0600)
		os.Exit(2)
	}
	os.Exit(0)
}
