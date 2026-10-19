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

func TestRecordedClaude(t *testing.T) {
	for _, scenario := range selectedRecordedScenarios(t) {
		t.Run(scenario, func(t *testing.T) { runRecordedClaude(t, scenario) })
	}
}

func runRecordedClaude(t *testing.T, scenario string) {
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
	c := tapeProcessConfig{Daemon: ensurePyryBuilt(t), Socket: shortSocketPath(t), Mode: mode, Path: filepath.Join(dir, scenario+".json"), Home: home, Scenario: scenario, Finish: filepath.Join(home, "finish"), Receipt: filepath.Join(home, "receipt"), Claim: filepath.Join(home, "claimed")}
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
	send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pub, pairing.Token)
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
				ch = make(chan reading, 1)
				go func() { raw, err := phone.ReceiveBytes(time.Until(deadline)); ch <- reading{raw, err} }()
			}
			timer := time.NewTimer(time.Until(deadline))
			defer timer.Stop()
			var result reading
			waiting := true
			for waiting {
				select {
				case result = <-ch:
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
				t.Fatal("test client connection ended before scenario completed")
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

	reconnect := func() {
		_ = phone.Close()
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
	recordedScenarioDrivers[scenario](t, recordedScenarioContext{Checkpoint: checkpoint, Source: source, Reconnect: reconnect, Home: home, Workdir: workdir, Socket: c.Socket, Mode: mode, Wire: scenariohelpers.Wire{Send: transmit, Next: next}, Sequence: &sequence, DaemonLog: stderr.String})

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
