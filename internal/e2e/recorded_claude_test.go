//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const tapeSession = "ecda0000-0000-4000-8000-000000000001"
const tapeConversation = "ecda0000-0000-4000-8000-000000000002"

func TestRecordedClaude(t *testing.T) {
	for _, scenario := range []string{"two-turn", "permission-deny"} {
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
	c := tapeProcessConfig{Mode: mode, Path: filepath.Join(dir, scenario+".json"), Home: home, Scenario: scenario, Finish: filepath.Join(home, "finish"), Receipt: filepath.Join(home, "receipt"), Claim: filepath.Join(home, "claimed")}
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
	wrapper := tapeWrapper(t, c)
	writeStreamInteractiveConfig(t, home, scenario == "permission-deny")
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
	seedBoundConversation(t, home, tapeConversation, tapeSession)
	extra := []string{"PYRY_ALLOW_INSECURE_RELAY=1", "PYRY_MOBILE_V2=1"}
	if mode == "replay" {
		extra = append(extra, "CLAUDE_CODE_OAUTH_TOKEN=", "ANTHROPIC_API_KEY=", "CLAUDE_CONFIG_DIR=")
	}
	socket, cmd, stdout, stderr, done := spawnWith(t, home, spawnOpts{claudeBin: wrapper, claudeArgs: []string{}, extraEnv: extra, extraFlags: []string{"-pyry-relay=" + relayURL, "-pyry-verbose"}})
	h := &Harness{SocketPath: socket, HomeDir: home, PID: cmd.Process.Pid, Stdout: stdout, Stderr: stderr, cmd: cmd, doneCh: done}
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
	transmit := func(kind string, payload any) {
		t.Helper()
		sequence++
		raw, e := json.Marshal(protocol.Envelope{ID: sequence, Type: kind, TS: time.Now().UTC(), Payload: mustJSON(t, payload)})
		if e != nil {
			t.Fatal(e)
		}
		cipher, e := send.Encrypt(raw)
		if e != nil {
			t.Fatal(e)
		}
		sendNoiseMsg(t, phone, cipher)
	}
	next := func(deadline time.Time) protocol.Envelope {
		t.Helper()
		for time.Now().Before(deadline) {
			if raw, e := os.ReadFile(c.Receipt); e == nil {
				var receipt tapeReceipt
				_ = json.Unmarshal(raw, &receipt)
				if !receipt.Success {
					t.Fatalf("Claude tape failed: %s", receipt.Error)
				}
			}
			raw, e := phone.ReceiveBytes(250 * time.Millisecond)
			if e != nil {
				continue
			}
			var inner protocol.InnerFrameV2
			if json.Unmarshal(raw, &inner) != nil {
				t.Fatal("invalid phone frame")
			}
			if inner.Type == protocol.TypeNoiseMsg {
				return decryptInnerEnvelope(t, inner, recv)
			}
		}
		t.Fatal("recorded daemon scenario deadline exceeded")
		return protocol.Envelope{}
	}
	turn := func(text string, permission bool) {
		t.Helper()
		transmit(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: tapeConversation, MessageID: fmt.Sprintf("tape-%d", sequence), Text: text})
		requestID := sequence
		deadline := time.Now().Add(150 * time.Second)
		sawAck, sawText, sawDismissed := false, false, false
		var modalID string
		for {
			env := next(deadline)
			switch env.Type {
			case protocol.TypeAck:
				if env.InReplyTo != nil && *env.InReplyTo == requestID {
					sawAck = true
				}
			case protocol.TypeModalShown:
				if !permission || modalID != "" {
					t.Fatal("unexpected or repeated permission request")
				}
				var shown protocol.ModalShownPayload
				if json.Unmarshal(env.Payload, &shown) != nil || shown.Class != "permission" || shown.ModalID == "" {
					t.Fatal("invalid permission surface")
				}
				modalID = shown.ModalID
				transmit(protocol.TypeModalAnswer, protocol.ModalAnswerPayload{ModalID: modalID, OptionID: "reject_once", AnswerToken: "tape-deny"})
			case protocol.TypeModalDismissed:
				var dismissed protocol.ModalDismissedPayload
				if json.Unmarshal(env.Payload, &dismissed) != nil {
					t.Fatal("invalid dismissal")
				}
				if dismissed.ModalID == modalID && modalID != "" {
					if dismissed.Outcome != "reject_once" || dismissed.Source != "remote" {
						t.Fatal("permission was not denied by this client")
					}
					sawDismissed = true
				}
			case protocol.TypeAssistantDelta:
				var delta protocol.AssistantDeltaPayload
				if json.Unmarshal(env.Payload, &delta) != nil {
					t.Fatal("invalid reply")
				}
				if delta.ConversationID == tapeConversation && strings.TrimSpace(delta.Text) != "" {
					sawText = true
				}
			case protocol.TypeTurnState:
				var state protocol.TurnStatePayload
				if json.Unmarshal(env.Payload, &state) != nil {
					t.Fatal("invalid turn state")
				}
				if state.ConversationID == tapeConversation && state.State == "idle" && sawText {
					if !sawAck || permission && !sawDismissed {
						t.Fatal("turn completed without request/permission evidence")
					}
					return
				}
			}
		}
	}
	if scenario == "two-turn" {
		turn("Without using tools, reply with a short greeting.", false)
		turn("Without using tools, reply with a short farewell.", false)
	} else {
		turn("Use the Write tool exactly once to create a file named tape-denied.txt containing hello. If denied, do not retry and do not use other tools. Reply with one short sentence after the decision.", true)
		if mode == "capture" {
			if _, e := os.Stat(filepath.Join(home, "tape-denied.txt")); !os.IsNotExist(e) {
				t.Fatal("denied tool created its file")
			}
		}
	}
	if err = os.WriteFile(c.Finish, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if raw, e := os.ReadFile(c.Receipt); e == nil {
			var receipt tapeReceipt
			if json.Unmarshal(raw, &receipt) != nil || !receipt.Success || receipt.Frames == 0 {
				t.Fatalf("incomplete Claude tape: %s", receipt.Error)
			}
			h.Stop(t)
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
	for _, scenario := range []string{"two-turn", "permission-deny"} {
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
			if err := claudetape.SameContract(load(baseline), load(fresh)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
