//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestWorkspaceBase_DaemonAdvertisesAndSeedsProcessCwd(t *testing.T) {
	for _, folder := range []string{"elli-workspace", "pyry-workspace"} {
		t.Run(folder, func(t *testing.T) {
			home := shortHome(t)
			base := filepath.Join(home, folder)
			override := filepath.Join(home, "different-claude-workdir")
			for _, dir := range []string{base, override} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			wantBase, err := filepath.EvalSymlinks(base)
			if err != nil {
				t.Fatal(err)
			}
			fr := fakerelay.New(relayTestLogger())
			t.Cleanup(func() { _ = fr.Close() })
			relayURL := fr.URL() + "/v2/server"
			payload, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: relayURL, DeviceName: "phone-a"})
			if err != nil {
				t.Fatal(err)
			}
			pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
			if err != nil {
				t.Fatal(err)
			}
			convPath := filepath.Join(home, ".pyry", "test", "conversations.json")
			if err := os.WriteFile(convPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}

			// Use existing lifecycle infrastructure, with exec.Cmd.Dir representing the
			// service WorkingDirectory independently of the Claude spawn override.
			socket := shortSocketPath(t)
			cmd := exec.Command(ensurePyryBuilt(t), "--pyry-name=test", "--pyry-socket="+socket,
				"--pyry-claude="+writeSleepClaude(t, home), "--pyry-workdir="+override, "--pyry-relay="+relayURL)
			cmd.Dir = base
			cmd.Env = append(childEnv(home), "PYRY_ALLOW_INSECURE_RELAY=1")
			stdout, stderr := &safeBuffer{}, &safeBuffer{}
			cmd.Stdout, cmd.Stderr = stdout, stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			h := &Harness{SocketPath: socket, HomeDir: home, PID: cmd.Process.Pid, Stdout: stdout, Stderr: stderr, cmd: cmd, doneCh: done}
			t.Cleanup(func() { h.teardown(t) })
			if err := h.waitForReady(); err != nil {
				t.Fatal(err)
			}
			serverID := readPersistedServerID(t, home)
			waitBinaryHello(t, fr, serverID)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			phone, err := fakephone.Dial(ctx, fr.URL(), serverID, payload.Token, "phone-a")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = phone.Close() })
			initiator, err := noise.NewInitiator(fakephone.InstallKey(payload.Token), pubKey)
			if err != nil {
				t.Fatal(err)
			}
			initMsg, err := initiator.WriteInit(buildHelloEarly(t, payload.Token))
			if err != nil {
				t.Fatal(err)
			}
			sendNoiseInit(t, phone, initMsg)
			inner := readInnerFrame(t, phone, 3*time.Second)
			if inner.Type != protocol.TypeNoiseResp {
				t.Fatalf("handshake type = %q", inner.Type)
			}
			response, err := base64.StdEncoding.DecodeString(inner.Data)
			if err != nil {
				t.Fatal(err)
			}
			ackRaw, _, _, err := initiator.ReadResp(response)
			if err != nil {
				t.Fatal(err)
			}
			var ack protocol.Envelope
			if err := json.Unmarshal(ackRaw, &ack); err != nil {
				t.Fatal(err)
			}
			if ack.Type != protocol.TypeHelloAck {
				t.Fatalf("ack type = %q", ack.Type)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(ack.Payload, &fields); err != nil {
				t.Fatal(err)
			}
			var advertised string
			if err := json.Unmarshal(fields["workspace_root"], &advertised); err != nil {
				t.Fatalf("workspace_root absent or invalid: %v", err)
			}
			if advertised != wantBase {
				t.Errorf("advertised = %q, want process cwd %q", advertised, wantBase)
			}

			var file seededRegistryFile
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				raw, err := os.ReadFile(convPath)
				if err == nil && json.Unmarshal(raw, &file) == nil && file.Seeded {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			if !file.Seeded || len(file.Conversations) != 1 {
				t.Fatalf("seeded=%v rows=%d\n%s", file.Seeded, len(file.Conversations), stderr.String())
			}
			general := file.Conversations[0]
			if general.Cwd != filepath.Join(wantBase, "default") || !general.IsPromoted || general.Name == nil || *general.Name != "General" {
				t.Errorf("seed row = %+v, want promoted General under process cwd", general)
			}
			if file.WorkspaceLabels[general.Cwd] != "Default workspace" {
				t.Errorf("workspace labels = %v", file.WorkspaceLabels)
			}
			if _, err := os.Stat(filepath.Join(override, "default")); !os.IsNotExist(err) {
				t.Errorf("seed used Claude override: %v", err)
			}
			sessions := readRegistry(t, filepath.Join(home, ".pyry", "test", "sessions.json")).Sessions
			if len(sessions) != 2 {
				t.Errorf("sessions = %d, want bootstrap and General", len(sessions))
			}
			h.Stop(t)
		})
	}
}
