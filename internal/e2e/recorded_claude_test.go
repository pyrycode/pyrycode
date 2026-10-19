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
	scenariohelpers "github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const tapeSession = "ecda0000-0000-4000-8000-000000000001"
const tapeConversation = "ecda0000-0000-4000-8000-000000000002"

func TestRecordedClaude(t *testing.T) {
	for _, scenario := range []string{"two-turn", "permission-deny", "reply", "question-answer", "question-refuse", "permission-allow", "permission-cancel"} {
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
	wrapper := tapeWrapper(t, c)
	writeStreamInteractiveConfig(t, home, scenario != "two-turn" && scenario != "reply")
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
	_, cmd, stdout, stderr, done := spawnWith(t, home, spawnOpts{claudeBin: wrapper, claudeArgs: []string{}, extraEnv: extra, extraFlags: []string{"-pyry-relay=" + relayURL, "-pyry-verbose", "-pyry-socket=" + c.Socket}})
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
		raw, e := json.Marshal(protocol.Envelope{ID: sequence, Type: kind, TS: time.Now().UTC(), Payload: mustJSON(t, payload)})
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
	next := func(deadline time.Time) protocol.Envelope {
		t.Helper()
		type reading struct {
			raw []byte
			err error
		}
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for time.Now().Before(deadline) {
			ch := make(chan reading, 1)
			go func() { raw, err := phone.ReceiveBytes(time.Until(deadline)); ch <- reading{raw, err} }()
			var result reading
			waiting := true
			for waiting {
				select {
				case result = <-ch:
					waiting = false
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
		t.Fatal("recorded daemon scenario deadline exceeded")
		return protocol.Envelope{}
	}

	turn := func(text string) {
		t.Helper()
		id := transmit(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: tapeConversation, MessageID: fmt.Sprintf("tape-%d", sequence), Text: text})
		ack := false
		receive := func(deadline time.Time) protocol.Envelope {
			env := next(deadline)
			if env.Type == protocol.TypeAck && env.InReplyTo != nil && *env.InReplyTo == id {
				ack = true
			}
			return env
		}
		scenariohelpers.CompletedTurn(t, receive, tapeConversation, 150*time.Second, true)
		if !ack {
			t.Fatal("recorded turn lost its matching acknowledgement")
		}
	}
	permission := func(prompt, option, token string) {
		t.Helper()
		id := transmit(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: tapeConversation, MessageID: fmt.Sprintf("tape-%d", sequence), Text: prompt})
		ack := false
		receive := func(deadline time.Time) protocol.Envelope {
			env := next(deadline)
			if env.Type == protocol.TypeAck && env.InReplyTo != nil && *env.InReplyTo == id {
				ack = true
			}
			return env
		}
		shown := scenariohelpers.WaitPermission(t, receive, tapeConversation, 150*time.Second)
		if shown.Prompt != "Write" {
			t.Fatal("permission scenario did not surface Write")
		}
		scenariohelpers.ResolvePermission(t, scenariohelpers.Wire{Send: transmit, Next: receive}, tapeConversation, shown, option, token, 150*time.Second)
		if !ack {
			t.Fatal("permission trigger lost its matching acknowledgement")
		}
		decision := "deny"
		if option == "allow_once" {
			decision = "allow"
		}
		scenariohelpers.RequirePromptAnswerHistory(t, home, tapeConversation, shown.ModalID, decision, tapeSession)
	}

	switch scenario {
	case "reply":
		transmit(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: tapeConversation, MessageID: "reply", Text: "Without using tools, briefly describe reliable process supervision."})
		scenariohelpers.CompletedTurn(t, next, tapeConversation, 120*time.Second, false)
	case "question-answer":
		scenariohelpers.QuestionAnswer(t, scenariohelpers.Wire{Send: transmit, Next: next}, scenariohelpers.QuestionConfig{Home: home, ConversationID: tapeConversation, SessionID: tapeSession, HistorySession: tapeSession, Prompt: scenariohelpers.RecordedQuestionPrompt(), Followup: "Without using tools, reply with a short greeting after the model change.", ExpectedModel: "claude-haiku-4-5-20251001", SurfaceBudget: 180 * time.Second, UpdateBudget: 30 * time.Second, TurnBudget: 150 * time.Second})
	case "question-refuse":
		transmit(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: tapeConversation, MessageID: "question-refusal-trigger", Text: scenariohelpers.RecordedRefusalPrompt()})
		batch := scenariohelpers.WaitQuestion(t, next, tapeConversation, 180*time.Second)
		transmit(protocol.TypeQuestionRefused, protocol.QuestionRefusedPayload{QuestionBatchID: batch.QuestionBatchID, AnswerToken: "e2e-1995-refusal-token"})
		scenariohelpers.RefusedTurn(t, scenariohelpers.Wire{Send: transmit, Next: next}, tapeConversation, batch.QuestionBatchID, 150*time.Second)
		scenariohelpers.RequirePromptAnswerHistory(t, home, tapeConversation, batch.QuestionBatchID, "refusal", tapeSession)
	case "permission-allow":
		permission("Use the Write tool exactly once to create tape-allowed.txt in the current directory. Set file_path to its absolute path and content to exactly hello with no newline. Do not use other tools. After the decision, reply with one short sentence.", "allow_once", "tape-allow")
		if mode == "capture" {
			data, e := os.ReadFile(filepath.Join(home, "tape-allowed.txt"))
			if e != nil || string(data) != "hello" {
				t.Fatal("allowed Write did not create the exact live witness")
			}
		}
	case "permission-cancel":
		transmit(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: tapeConversation, MessageID: "permission-cancel-trigger", Text: scenariohelpers.CancelPermissionPrompt})
		shown := scenariohelpers.WaitPermission(t, next, tapeConversation, 150*time.Second)
		if shown.Prompt != "Write" {
			t.Fatal("cancellation did not surface Write")
		}
		scenariohelpers.CancelPermission(t, scenariohelpers.Wire{Send: transmit, Next: next}, tapeConversation, shown, 30*time.Second, 60*time.Second)
		scenariohelpers.RequirePromptAnswerHistory(t, home, tapeConversation, shown.ModalID, "cancel", tapeSession)
		if mode == "capture" {
			if _, e := os.Stat(filepath.Join(home, scenariohelpers.CancelPermissionWitness)); !os.IsNotExist(e) {
				t.Fatal("cancelled permission created its live witness")
			}
		}
	case "two-turn":
		turn("Without using tools, reply with a short greeting.")
		turn("Without using tools, reply with a short farewell.")
	case "permission-deny":
		permission("Use the Write tool exactly once to create a file named tape-denied.txt containing hello. If denied, do not retry and do not use other tools. Reply with one short sentence after the decision.", "reject_once", "tape-deny")
		if mode == "capture" {
			if _, e := os.Stat(filepath.Join(home, "tape-denied.txt")); !os.IsNotExist(e) {
				t.Fatal("denied tool created its file")
			}
		}
	default:
		t.Fatal("unknown recorded scenario")
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
	for _, scenario := range []string{"two-turn", "permission-deny", "reply", "question-answer", "question-refuse", "permission-allow", "permission-cancel"} {
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
