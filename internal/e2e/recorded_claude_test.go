//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	scenariohelpers "github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const tapeSession = "ecda0000-0000-4000-8000-000000000001"
const tapeConversation = "ecda0000-0000-4000-8000-000000000002"

func TestMain(m *testing.M) {
	code := m.Run()
	// The recorded binary caches are shared across top-level tests. Retire
	// their private directories only after every consumer has finished.
	for _, binary := range []string{recordedPoolBinary.path, recordedMCPDaemon.path} {
		if binary == "" {
			continue
		}
		if err := os.RemoveAll(filepath.Dir(binary)); err != nil {
			fmt.Fprintf(os.Stderr, "remove recorded binary fixture: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

func TestRecordedClaude(t *testing.T) {
	selected := selectedRecordedScenarios(t)
	for _, name := range selected {
		if claudetape.MCPScenario(name) {
			buildRecordedMCPDaemon(t)
			break
		}
	}
	for _, name := range selected {
		if _, ok := recordedPoolProfiles[name]; ok || claudetape.DaemonSuiteProfiles()[name].Root != "" || claudetape.CLIProfiles()[name] != "" || claudetape.RunnerProfiles()[name].Root != "" || name == claudetape.RecoveryScenario || claudetape.LifecycleProfiles()[name].Root != "" {
			buildRecordedPoolBinary(t)
			break
		}
	}
	for _, scenario := range selected {
		t.Run(scenario, func(t *testing.T) {
			if _, ok := recordedPoolProfiles[scenario]; ok {
				runRecordedPool(t, scenario)
			} else if claudetape.DaemonSuiteProfiles()[scenario].Root != "" {
				runRecordedDaemonSuite(t, scenario)
			} else if claudetape.LifecycleProfiles()[scenario].Root != "" {
				runRecordedLifecycleSuite(t, scenario)
			} else if scenario == claudetape.RecoveryScenario {
				runRecordedRecoverySuite(t, scenario)
			} else if claudetape.RunnerProfiles()[scenario].Root != "" {
				runRecordedRunnerSuite(t, scenario)
			} else if claudetape.CLIProfiles()[scenario] != "" {
				runRecordedCLI(t, scenario)
			} else {
				runRecordedClaude(t, scenario, 0)
			}
		})

	}
}

func TestRecordedMCPSendFileWaitsForStartup(t *testing.T) {
	t.Setenv("PYRY_CLAUDE_TAPE_MODE", "replay")
	t.Setenv("PYRY_CLAUDE_TAPE_HOLD_INITIALIZE", "1")
	runRecordedLifecycleSuite(t, "mcp-send-file")
}

// The daemon may write a turn once claude confirms the permission posture, which
// can precede the initialize reply that triggers its MCP status ask. A message
// that reaches the daemon inside that window therefore reorders its stdin
// against the tape, which happened in HP release checks on 2026-10-19. Holding
// the initialize reply widens the window to two seconds, so a driver that does
// not wait for the replayed startup fails here on every run.
func TestRecordedClaudeFirstTurnWaitsForStartup(t *testing.T) {
	t.Setenv("PYRY_CLAUDE_TAPE_MODE", "replay")
	t.Setenv("PYRY_CLAUDE_TAPE_DIR", "")
	// The proof needs a tape whose posture write and MCP status ask both come
	// before the first turn. A recapture with another order must fail here
	// rather than let this test pass without exercising the window.
	f, err := os.Open(filepath.Join("testdata", "claude-tapes", "reply.json"))
	if err != nil {
		t.Fatal(err)
	}
	tape, err := claudetape.Decode(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	startup := map[string]bool{}
	for _, frame := range tape.Frames {
		var v struct {
			Type    string `json:"type"`
			Request struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		if frame.Direction != "stdin" || json.Unmarshal(frame.Data, &v) != nil {
			continue
		}
		if v.Type == "user" {
			break
		}
		startup[v.Request.Subtype] = true
	}
	if !startup["set_permission_mode"] || !startup["initialize"] || !startup["mcp_status"] {
		t.Fatal("reply tape no longer records posture, initialize and MCP status before its first turn")
	}
	runRecordedClaude(t, "reply", 2*time.Second)
}

func runRecordedClaude(t *testing.T, scenario string, holdInitialize time.Duration) {
	t.Helper()
	home := shortHome(t)
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
	c := tapeProcessConfig{Daemon: ensurePyryBuilt(t), Socket: shortSocketPath(t), Mode: mode, Path: filepath.Join(dir, scenario+".json"), Home: home, Scenario: scenario, Finish: filepath.Join(home, "finish"), Receipt: filepath.Join(home, "receipt"), Claim: filepath.Join(home, "claimed"), Ready: filepath.Join(home, "turn-ready"), HoldInitialize: holdInitialize}
	if mode == "capture" {
		if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
			t.Fatal("capture requires explicit Claude test credentials; no tests skipped")
		}
		c.Claude, err = exec.LookPath("claude")
		if err != nil {
			t.Fatal("capture requires Claude executable")
		}
		c.Claude, err = filepath.Abs(c.Claude)
		if err != nil {
			t.Fatal(err)
		}
		status, e := exec.Command("git", "status", "--porcelain").Output()
		if e != nil || len(status) != 0 {
			t.Fatal("commit source before capturing Claude behaviour")
		}
		rev, e := exec.Command("git", "rev-parse", "HEAD").Output()
		if e != nil {
			t.Fatal(e)
		}
		bin, e := os.ReadFile(ensurePyryBuilt(t))
		if e != nil {
			t.Fatal(e)
		}
		info, e := buildinfo.ReadFile(ensurePyryBuilt(t))
		if e != nil {
			t.Fatal("capture requires source-stamped daemon binary")
		}
		settings := map[string]string{}
		for _, setting := range info.Settings {
			settings[setting.Key] = setting.Value
		}
		if settings["vcs.revision"] != strings.TrimSpace(string(rev)) || settings["vcs.modified"] != "false" {
			t.Fatal("daemon binary does not match clean capture source")
		}
		sum := sha256.Sum256(bin)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		version, e := exec.CommandContext(ctx, c.Claude, "--version").Output()
		if e != nil {
			t.Fatal("cannot read Claude version")
		}
		c.Producer = claudetape.Producer{Revision: strings.TrimSpace(string(rev)), BinarySHA256: hex.EncodeToString(sum[:]), ClaudeVersion: strings.TrimSpace(string(version)), CapturedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	}
	if claudetape.RequiresClientCheckpoint(scenario) {
		c.CheckpointDir = filepath.Join(home, "client-checkpoints")
		if err := claudetape.NewCheckpointDirectory(c.CheckpointDir); err != nil {
			t.Fatal(err)
		}
	}
	var observer *recordedSourceObserver
	switch scenario {
	case "permission-context-absent", "permission-context-outside", "native-suggestions":
		observer = newRecordedSourceObserver(t)
		c.ObserverAddress = observer.listener.Addr().String()
	}
	wrapper := tapeWrapper(t, c)
	definition := recordedManifestFor(t).scenario(scenario)
	writeStreamInteractiveConfig(t, home, definition.StdioPermission)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	pairing, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: relayURL, DeviceName: "phone-a", AllowRemotePermissions: true})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := base64.StdEncoding.DecodeString(pairing.ServerStaticPubkey)
	if err != nil {
		t.Fatal(err)
	}
	seedBootstrapRegistry(t, home, tapeSession)
	if definition.InitialYOLO {
		path := filepath.Join(home, ".pyry", "test", "sessions.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		seed := strings.Replace(string(raw), `"bootstrap":true`, `"bootstrap":true,"yolo":true`, 1)
		if seed == string(raw) {
			t.Fatal("initial bypass seed was not applied")
		}
		if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	workdir := prepareRecordedWorkspace(t, scenario, home)
	seedBoundConversation(t, home, tapeConversation, tapeSession, workdir)
	extra := []string{"PYRY_ALLOW_INSECURE_RELAY=1", "PYRY_MOBILE_V2=1"}
	if mode == "replay" {
		extra = append(extra, "CLAUDE_CODE_OAUTH_TOKEN=", "ANTHROPIC_API_KEY=", "CLAUDE_CONFIG_DIR=")
	}

	_, cmd, stdout, stderr, done := spawnWith(t, home, spawnOpts{claudeBin: wrapper, claudeArgs: append([]string{}, definition.ClaudeArgs...), extraEnv: extra, extraFlags: []string{"-pyry-relay=" + relayURL, "-pyry-workdir=" + workdir, "-pyry-verbose", "-pyry-socket=" + c.Socket}})
	h := &Harness{SocketPath: c.Socket, HomeDir: home, PID: cmd.Process.Pid, Stdout: stdout, Stderr: stderr, cmd: cmd, doneCh: done}
	t.Cleanup(func() { h.Stop(t) })
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		f, err := os.CreateTemp("", "pyry-recorded-failure-*.log")
		if err != nil {
			t.Log("could not retain private daemon failure log")
			return
		}
		_, writeErr := f.WriteString(stderr.String())
		closeErr := f.Close()
		if writeErr == nil && closeErr == nil {
			t.Logf("private daemon failure log: %s", f.Name())
		}
	})
	if err = h.waitForReady(); err != nil {
		t.Fatal("recorded daemon did not become ready")
	}
	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(ctx, fr.URL(), serverID, pairing.Token, "phone-a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	var queueCaps []string
	if scenario == "ordinary-queue-placement" || scenario == "send-now-placement" {
		queueCaps = []string{protocol.CapabilityThread}
	}
	send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pub, pairing.Token, queueCaps...)
	sequence := uint64(100)
	transmit := func(kind string, payload any) uint64 {
		t.Helper()
		sequence++
		raw, e := json.Marshal(protocol.Envelope{ID: sequence, Type: kind, TS: time.Now().UTC(), Payload: recordedOptionalPayload(t, payload)})
		if e != nil {
			t.Fatal(e)
		}
		cipher, e := send.Encrypt(raw)
		if e != nil {
			t.Fatal(e)
		}
		sendNoiseMsg(t, phone, cipher)
		return sequence
	}
	type reading struct {
		raw []byte
		err error
	}
	var persistent chan reading
	var pending chan reading
	t.Cleanup(func() {
		if pending != nil {
			_ = phone.Close()
			<-pending
		}
	})
	if scenario == "native-suggestions" {
		persistent = make(chan reading, 128)
		stop := make(chan struct{})
		t.Cleanup(func() { close(stop) })
		go func() {
			for {
				raw, err := phone.ReceiveBytes(15 * time.Minute)
				select {
				case persistent <- reading{raw, err}:
				case <-stop:
					return
				}
				if err != nil {
					return
				}
			}
		}()
	}
	next := func(deadline time.Time) protocol.Envelope {
		t.Helper()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for time.Now().Before(deadline) {
			ch := persistent
			if ch == nil {
				if pending == nil {
					pending = make(chan reading, 1)
					ch, client := pending, phone
					go func() { raw, err := client.ReceiveBytes(15 * time.Minute); ch <- reading{raw, err} }()
				}
				ch = pending
			}
			timer := time.NewTimer(time.Until(deadline))
			defer timer.Stop()
			var result reading
			waiting := true
			for waiting {
				select {
				case result = <-ch:
					if persistent == nil {
						pending = nil
					}
					waiting = false
				case <-timer.C:
					return protocol.Envelope{}
				case <-tick.C:
					if raw, e := os.ReadFile(c.Receipt); e == nil {
						var receipt tapeReceipt
						if json.Unmarshal(raw, &receipt) != nil {
							t.Fatal("invalid Claude tape receipt")
						}
						if !receipt.Success {
							t.Fatalf("Claude tape failed: %s", receipt.Error)
						}
					}
				}
			}
			if errors.Is(result.err, fakephone.ErrReceiveTimeout) {
				return protocol.Envelope{}
			}
			if result.err != nil {
				t.Fatalf("test client connection ended before scenario completed: %v", result.err)
			}
			var inner protocol.InnerFrameV2
			if json.Unmarshal(result.raw, &inner) != nil {
				t.Fatal("invalid phone frame")
			}
			if inner.Type == protocol.TypeNoiseMsg {
				return decryptInnerEnvelope(t, inner, recv)
			}
		}
		return protocol.Envelope{}
	}

	if mode == "replay" {
		// Send the first message only after the tape has replayed everything the
		// daemon recorded before its first turn. See the startup-order test above.
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, e := os.Stat(c.Ready); e == nil {
				break
			}
			if raw, e := os.ReadFile(c.Receipt); e == nil {
				var receipt tapeReceipt
				if json.Unmarshal(raw, &receipt) != nil {
					t.Fatal("invalid Claude tape receipt")
				}
				if !receipt.Success {
					t.Fatalf("Claude tape failed before the first turn: %s", receipt.Error)
				}
			}
			if time.Now().After(deadline) {
				// AwaitsTurn needs the next recorded daemon input to be a user turn.
				// A driver that writes something else first needs its own rule.
				t.Fatal("Claude tape did not reach its recorded first user turn")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	reconnect := func() {
		_ = phone.Close()
		if pending != nil {
			<-pending
			pending = nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var err error
		phone, err = fakephone.Dial(ctx, fr.URL(), serverID, pairing.Token, "phone-a")
		if err != nil {
			t.Fatal("recorded phone reconnect failed")
		}
		send, recv = driveHandshakeToOpenDaemonInteractive(t, phone, pub, pairing.Token)
	}
	source := func() []claudetape.Frame {
		if observer == nil {
			t.Fatal("scenario requires a native source observer")
		}
		return observer.snapshot(t)
	}
	checkpoint := func(action claudetape.ClientCheckpoint) {
		t.Helper()
		if !claudetape.RequiresClientCheckpoint(scenario) || c.CheckpointDir == "" {
			t.Fatal("scenario has no client checkpoint profile")
		}
		if err := claudetape.PublishCheckpoint(c.CheckpointDir, action, 10*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	wire := scenariohelpers.ObserveSource(t, home, scenariohelpers.Wire{Send: transmit, Next: next})
	recordedScenarioDrivers[scenario](t, recordedScenarioContext{Checkpoint: checkpoint, Source: source, Reconnect: reconnect, Home: home, Workdir: workdir, Socket: c.Socket, Mode: mode, Wire: wire, Sequence: &sequence, DaemonLog: stderr.String})

	if t.Failed() {
		return
	}

	if err = os.WriteFile(c.Finish, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	sealBudget := 10 * time.Second
	if scenario == "native-suggestions" {
		sealBudget = 120 * time.Second
	}
	deadline := time.Now().Add(sealBudget)
	for time.Now().Before(deadline) {
		if raw, e := os.ReadFile(c.Receipt); e == nil {
			var receipt tapeReceipt
			if json.Unmarshal(raw, &receipt) != nil || !receipt.Success || receipt.Frames == 0 {
				t.Fatalf("incomplete Claude tape: %s", receipt.Error)
			}
			h.Stop(t)
			if _, err := os.Stat(c.Claim + ".duplicate"); err == nil {
				t.Fatal("unrecorded additional Claude process")
			}
			t.Logf("mode=%s scenario=%s frames=%d", mode, scenario, receipt.Frames)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Claude tape did not seal")
}

func TestClaudeTapeCompatibility(t *testing.T) {
	fresh := os.Getenv("PYRY_CLAUDE_TAPE_FRESH")
	if fresh == "" {
		return
	}
	baseline := os.Getenv("PYRY_CLAUDE_TAPE_DIR")
	if baseline == "" {
		baseline = "testdata/claude-tapes"
	}
	for _, scenario := range selectedRecordedScenarios(t) {
		t.Run(scenario, func(t *testing.T) {

			if profile, ok := claudetape.LifecycleProfiles()[scenario]; ok {
				load := func(dir string) *claudetape.LifecycleSuite {
					f, e := os.Open(filepath.Join(dir, scenario+".json"))
					if e != nil {
						t.Fatal(e)
					}
					defer f.Close()
					suite, e := claudetape.DecodeLifecycleSuite(f, profile.Roles)
					if e != nil {
						t.Fatal(e)
					}
					return suite
				}
				report, err := claudetape.CompareLifecycleSuite(load(baseline), load(fresh), profile.Roles, recordedManifestFor(t).scenario(scenario).ComparisonPolicy)
				if err != nil {
					t.Fatal("lifecycle outgoing contract changed")
				}
				raw, _ := json.Marshal(report)
				t.Logf("CLAUDE_COMPARISON %s", raw)
				return
			}

			if scenario == claudetape.RecoveryScenario {
				load := func(dir string) *claudetape.RecoverySuite {
					f, err := os.Open(filepath.Join(dir, scenario+".json"))
					if err != nil {
						t.Fatal(err)
					}
					defer f.Close()
					suite, err := claudetape.DecodeRecoverySuite(f)
					if err != nil {
						t.Fatal(err)
					}
					return suite
				}
				if claudetape.SameRecoveryContract(load(baseline), load(fresh)) != nil {
					t.Fatal("recovery outgoing contract changed")
				}
				raw, _ := json.Marshal(map[string]string{"scenario": scenario, "policy": "strict_ordered"})
				t.Logf("CLAUDE_COMPARISON %s", raw)
				return
			}
			if claudetape.RunnerProfiles()[scenario].Root != "" {
				load := func(dir string) *claudetape.RunnerSuite {
					f, err := os.Open(filepath.Join(dir, scenario+".json"))
					if err != nil {
						t.Fatal(err)
					}
					defer f.Close()
					suite, err := claudetape.DecodeRunnerSuite(f)
					if err != nil {
						t.Fatal(err)
					}
					return suite
				}
				if claudetape.SameRunnerSuiteContract(load(baseline), load(fresh)) != nil {
					t.Fatal("direct runner request contract changed")
				}
				raw, _ := json.Marshal(map[string]string{"scenario": scenario, "policy": "strict_ordered"})
				t.Logf("CLAUDE_COMPARISON %s", raw)
				return
			}
			if claudetape.CLIProfiles()[scenario] != "" {
				load := func(dir string) *claudetape.CLITape {
					f, err := os.Open(filepath.Join(dir, scenario+".json"))
					if err != nil {
						t.Fatal(err)
					}
					defer f.Close()
					tape, err := claudetape.DecodeCLI(f)
					if err != nil {
						t.Fatal(err)
					}
					return tape
				}
				if claudetape.SameCLIContract(load(baseline), load(fresh)) != nil {
					t.Fatal("CLI outgoing contract changed")
				}
				raw, _ := json.Marshal(map[string]string{"scenario": scenario, "policy": "strict_ordered"})
				t.Logf("CLAUDE_COMPARISON %s", raw)
				return
			}
			if profile, ok := claudetape.DaemonSuiteProfiles()[scenario]; ok {
				loadSuite := func(dir string) *claudetape.Suite {
					f, err := os.Open(filepath.Join(dir, scenario+".json"))
					if err != nil {
						t.Fatal(err)
					}
					defer f.Close()
					suite, err := claudetape.DecodeSuite(f, profile.Roles)
					if err != nil {
						t.Fatal(err)
					}
					return suite
				}
				if claudetape.SameSuiteContract(loadSuite(baseline), loadSuite(fresh), profile.Roles) != nil {
					t.Fatal("daemon suite request contract changed")
				}
				raw, _ := json.Marshal(map[string]string{"scenario": scenario, "policy": "strict_ordered"})
				t.Logf("CLAUDE_COMPARISON %s", raw)
				return
			}
			load := func(dir string) *claudetape.Tape {
				f, err := os.Open(filepath.Join(dir, scenario+".json"))
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				tape, err := claudetape.Decode(f)
				if err != nil {
					t.Fatal(err)
				}
				return tape
			}
			policy := recordedManifestFor(t).scenario(scenario).ComparisonPolicy
			left, right := load(baseline), load(fresh)
			report := map[string]any{"scenario": scenario, "policy": policy}
			switch policy {
			case "strict_ordered":
				if err := claudetape.SameContract(left, right); err != nil {
					t.Fatal(err)
				}
			case "native_coding_prefix":
				a, b, err := claudetape.SameNativeSuggestionContract(left, right, scenariohelpers.NativeSuggestionPrompts("tape-native-suggestions"), scenariohelpers.NativeSuggestionClearPrompt)
				if err != nil {
					t.Fatal(err)
				}
				report["baseline_coding_iterations"], report["fresh_coding_iterations"] = a, b
			default:
				t.Fatal("unsupported comparison policy")
			}
			raw, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("CLAUDE_COMPARISON %s", raw)
		})
	}
}

func recordedOptionalPayload(t *testing.T, payload any) json.RawMessage {
	t.Helper()
	if payload == nil {
		return nil
	}
	return mustJSON(t, payload)
}
