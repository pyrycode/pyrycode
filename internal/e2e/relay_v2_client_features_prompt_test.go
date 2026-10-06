//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestRelayV2_ClientFeaturesSpawnSnapshot(t *testing.T) {
	const before = "FEATURES-BEFORE-2898: Markdown tables"
	const after = "FEATURES-AFTER-2898: image previews"
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	paired, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: relayURL, DeviceName: "Phone"})
	if err != nil {
		t.Fatal(err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(paired.ServerStaticPubkey)
	if err != nil {
		t.Fatal(err)
	}
	writeStreamInteractiveConfig(t, home)
	wrapper := filepath.Join(home, "record-claude")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > '%s/argv.'$$\nexec '%s' \"$@\"\n", home, ensureFakeClaudeBuilt(t))
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	socket, cmd, stdout, stderr, done := spawnWith(t, home, spawnOpts{
		claudeBin: wrapper, claudeArgs: []string{},
		extraFlags: []string{"-pyry-workdir=" + home, "-pyry-relay=" + relayURL, "-pyry-active-cap=1", "-pyry-verbose"},
		extraEnv:   []string{"PYRY_ALLOW_INSECURE_RELAY=1", "PYRY_MOBILE_V2=1", "PYRY_FAKE_CLAUDE_STREAM_JSON=1"},
	})
	h := &Harness{SocketPath: socket, HomeDir: home, PID: cmd.Process.Pid, cmd: cmd, Stdout: stdout, Stderr: stderr, doneCh: done}
	t.Cleanup(func() { h.teardown(t) })
	if err := h.waitForReady(); err != nil {
		t.Fatal(err)
	}
	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)

	// Report-specific handshake stays local to this proof; production mapping is
	// exercised by the daemon, not duplicated in the test.
	connect := func(report string) (*fakephone.Client, func(protocol.Envelope), func(time.Time) (protocol.Envelope, bool)) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		phone, err := fakephone.Dial(ctx, fr.URL(), serverID, paired.Token, "Phone")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = phone.Close() })
		initiator, err := noise.NewInitiator(fakephone.InstallKey(paired.Token), pubKey)
		if err != nil {
			t.Fatal(err)
		}
		early := mustJSON(t, protocol.Envelope{ID: 1, Type: protocol.TypeHello, TS: time.Now().UTC(), Payload: mustJSON(t, protocol.HelloClientPayload{
			Role: "client", DeviceName: "Phone", ClientVersion: "1", ClientFeatures: report,
			ProtocolVersions: []string{"v2"}, Token: paired.Token,
			// Features must reach the prompt even without capability negotiation.
		})})
		init, err := initiator.WriteInit(early)
		if err != nil {
			t.Fatal(err)
		}
		sendNoiseInit(t, phone, init)
		inner := readInnerFrame(t, phone, 3*time.Second)
		if inner.Type != protocol.TypeNoiseResp {
			t.Fatal("missing noise response")
		}
		raw, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatal(err)
		}
		ack, send, recv, err := initiator.ReadResp(raw)
		if err != nil {
			t.Fatal(err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(ack, &env); err != nil || env.Type != protocol.TypeHelloAck {
			t.Fatal("missing authenticated hello ack")
		}
		seal, next := noiseWire(t, phone, send, recv)
		return phone, seal, next
	}
	phone, seal, next := connect(before)
	conv := createConversationOverWire(t, seal, next, 2)
	row := waitForConversation(t, home, conv)
	post := func(id, text string) {
		t.Helper()
		ends := func() int {
			count := 0
			for _, entry := range readHistoryEntries(home, id) {
				if entry.Type == protocol.TypeTurnEnd {
					count++
				}
			}
			return count
		}
		prior := ends()
		r := runVerb(t, socket, home, "conversation", "post", "--id="+id, "--text="+text)
		if r.ExitCode != 0 {
			t.Fatalf("post: %+v", r)
		}
		deadline := time.Now().Add(10 * time.Second)
		for ends() <= prior && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if ends() <= prior {
			t.Fatal("posted turn never completed")
		}
	}
	// Locate only files named by real child argv, including on reactivation.
	promptPath := func(sessionID string, wantLaunches int) string {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			paths, err := filepath.Glob(filepath.Join(home, "argv.*"))
			if err != nil {
				t.Fatal(err)
			}
			count, prompt := 0, ""
			for _, path := range paths {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				args := strings.Split(string(raw), "\n")
				if !strings.Contains(string(raw), "\n"+sessionID+"\n") {
					continue
				}
				for i, arg := range args {
					if arg == "--append-system-prompt-file" && i+1 < len(args) {
						prompt = args[i+1]
						count++
					}
				}
			}
			if count >= wantLaunches && prompt != "" {
				return prompt
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("child argv missing prompt path")
		return ""
	}
	post(conv, "first turn")
	path := promptPath(row.CurrentSessionID, 1)
	initial := mustReadFile(t, path)
	attributed := `"Phone" (version "1") (self-reported features "` + before + `")`
	if !strings.Contains(string(initial), attributed) {
		t.Fatalf("spawn prompt lacks attributed report: %s", initial)
	}
	if err := phone.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(h.Stderr.String(), "v2.peer_close.teardown") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(h.Stderr.String(), "v2.peer_close.teardown") {
		t.Fatal("old authenticated connection not torn down")
	}
	_, seal, next = connect(after)
	post(conv, "still active")
	if initial != mustReadFile(t, path) {
		t.Fatal("reconnect rewrote active prompt")
	}
	other := createConversationOverWire(t, seal, next, 3)
	post(other, "evict original through active cap")
	regPath := filepath.Join(home, ".pyry", "test", "sessions.json")
	waitForSessionState(t, regPath, row.CurrentSessionID, "evicted", 5*time.Second)
	post(conv, "reactivated")
	refreshed := mustReadFile(t, promptPath(row.CurrentSessionID, 2))
	if !strings.Contains(string(refreshed), `"Phone" (version "1") (self-reported features "`+after+`")`) || strings.Contains(refreshed, before) {
		t.Fatalf("reactivated prompt has stale report: %s", refreshed)
	}
	h.Stop(t)
	registry := mustReadFile(t, filepath.Join(home, ".pyry", "test", "devices.json"))
	logs := h.Stdout.String() + h.Stderr.String()
	for _, report := range []string{before, after} {
		if strings.Contains(registry, report) || strings.Contains(logs, report) {
			t.Fatal("report leaked to registry or daemon logs")
		}
	}
}
