//go:build e2e_realclaude

package realclaude

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func stopReaderSocket(t *testing.T) (*fakephone.Client, *websocket.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	peers := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		peers <- conn
		<-ctx.Done()
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(cancel)
	phone, err := fakephone.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), "rig", "rig", "rig")
	if err != nil {
		t.Fatal("dial reader control")
	}
	t.Cleanup(func() { _ = phone.Close() })
	select {
	case peer := <-peers:
		t.Cleanup(func() { _ = peer.CloseNow() })
		return phone, peer, ctx
	case <-ctx.Done():
		t.Fatal("reader control did not connect")
		return nil, nil, nil
	}
}

func TestStopTaskReader(t *testing.T) {
	phone, peer, ctx := stopReaderSocket(t)
	send, recv := completedTurnCipherPair(t)
	clientSend, peerRecv := completedTurnCipherPair(t)
	h := &perConvHarness{phone: phone, initRecv: recv, initSend: clientSend}
	reader := newStopTaskReader(t, h)
	receive := func(timeout time.Duration) (protocol.Envelope, bool) {
		return reader.receive(t, timeout)
	}
	sendFrame := func(env protocol.Envelope) {
		t.Helper()
		cipher, err := send.Encrypt(mustJSON(t, env))
		if err != nil {
			t.Fatal("encrypt reader control")
		}
		raw := mustJSON(t, protocol.InnerFrameV2{Type: protocol.TypeNoiseMsg, Data: base64.StdEncoding.EncodeToString(cipher)})
		if err := peer.Write(ctx, websocket.MessageText, raw); err != nil {
			t.Fatal("send reader control")
		}
	}
	fifo := filepath.Join(t.TempDir(), "hold.fifo")
	arrived, release := tpcapHoldFIFO(t, fifo)
	hold := newStopHoldOperation(t, t.TempDir(), fifo)
	setup := stopHeldTaskSetup{hold: hold}
	tool := protocol.ToolUsePayload{ConversationID: "bound", ToolUseID: "tool", Name: "Bash", Input: map[string]string{"command": stopShellQuote(hold.path), "run_in_background": "true"}}
	roster := protocol.BackgroundTaskRosterPayload{ConversationID: "bound", Tasks: []protocol.BackgroundTask{{TaskID: "held", ToolCallID: "tool", TaskType: "local_bash"}}}
	for _, env := range []protocol.Envelope{stopSetupEnvelope(t, protocol.TypeToolUse, tool), stopSetupEnvelope(t, protocol.TypeBackgroundTaskRoster, roster)} {
		sendFrame(env)
		got, ok := receive(time.Second)
		if !ok || setup.observe(got, "bound", fifo) != nil {
			t.Fatal("metadata before execution was not received")
		}
	}
	for range 3 {
		if _, ok := receive(100 * time.Millisecond); ok || setup.taskID() != "" {
			t.Fatal("idle polling established unexecuted identity")
		}
	}
	cmd := stopHoldCommand(ctx, stopShellQuote(hold.path))
	if err := cmd.Start(); err != nil {
		t.Fatal("start delayed hold")
	}
	t.Cleanup(func() { release(); _ = cmd.Cancel(); _ = cmd.Wait() })
	select {
	case <-arrived:
	case <-ctx.Done():
		t.Fatal("delayed execution did not arrive")
	}
	if setup.taskID() != "held" {
		t.Fatalf("delayed witness did not complete join: %s", setup.diagnostic())
	}
	sealEnvelope(t, phone, clientSend, stopSetupEnvelope(t, protocol.TypeStopBackgroundTask, protocol.StopBackgroundTaskPayload{ConversationID: "bound", TaskID: setup.taskID()}))
	_, raw, err := peer.Read(ctx)
	var inner protocol.InnerFrameV2
	if err != nil || json.Unmarshal(raw, &inner) != nil {
		t.Fatal("stop did not reach peer after idle")
	}
	cipher, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		t.Fatal("decode outbound control")
	}
	plain, err := peerRecv.Decrypt(cipher)
	var stop protocol.Envelope
	if err != nil || json.Unmarshal(plain, &stop) != nil || stop.Type != protocol.TypeStopBackgroundTask {
		t.Fatal("encrypted stop did not reach peer")
	}
	sendFrame(stopSetupEnvelope(t, protocol.TypeBackgroundTaskRoster, protocol.BackgroundTaskRosterPayload{ConversationID: "bound"}))
	got, ok := receive(time.Second)
	if !ok || got.Type != protocol.TypeBackgroundTaskRoster || setup.observe(got, "bound", fifo) != nil || setup.taskID() != "" {
		t.Fatal("frame after idle did not preserve ordered decryption and roster state")
	}
	peer.CloseRead(ctx)
}

func TestStopTaskReaderShutdown(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "socket read"
		if pending {
			name = "unconsumed frame"
		}
		t.Run(name, func(t *testing.T) {
			phone, peer, ctx := stopReaderSocket(t)
			reader := newStopTaskReader(t, &perConvHarness{phone: phone})
			if pending {
				if err := peer.Write(ctx, websocket.MessageText, []byte(`{}`)); err != nil {
					t.Fatal("send pending frame")
				}
			}
			peer.CloseRead(ctx)
			reader.stop()
			reader.stop()
		})
	}
}
