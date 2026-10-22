//go:build e2e_realclaude

package realclaude

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// A pending read belongs to its connection; reconnect preserves the command's
// source boundary and sequence while replacing both transport cipher states.
func TestSharedHarnessWireReconnect(t *testing.T) {
	const conv = "37340000-0000-4000-8000-000000000001"
	h := &perConvHarness{home: t.TempDir()}
	store := history.New(filepath.Join(h.home, ".pyry", "test"))
	appendSource := func(text string) uint64 {
		t.Helper()
		id, err := store.Append(conversations.ConversationID(conv), protocol.TypeAssistantDelta,
			mustJSON(t, protocol.AssistantDeltaPayload{ConversationID: conv, TurnID: "owned", Text: text}), time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	appendSource("predecessor")
	peers := make(chan *websocket.Conn, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer peer.CloseNow()
		peers <- peer
		<-release
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wireSourceID uint64
	var w scenario.Wire
	for generation := 0; generation < 3; generation++ {
		phone, err := fakephone.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1), "server", "token", "phone")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = phone.Close() })
		peer := <-peers
		h.phone = phone
		var peerSend, peerRecv *noise.CipherState
		h.initSend, h.initRecv, peerSend, peerRecv = testReconnectCiphers(t)
		if generation == 0 {
			w = sharedHarnessWire(t, h, 2)
		}
		kind := protocol.TypeModalAnswer
		var payload any = protocol.ModalAnswerPayload{ModalID: "owned", OptionID: "reject_once", AnswerToken: "answer"}
		if generation == 0 {
			kind = protocol.TypeSendMessage
			payload = protocol.SendMessagePayload{ConversationID: conv, MessageID: "command", Text: "prompt"}
		}
		requestID := w.Send(kind, payload)
		_, raw, err := peer.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var inner protocol.InnerFrameV2
		if json.Unmarshal(raw, &inner) != nil || inner.Type != protocol.TypeNoiseMsg {
			t.Fatal("missing encrypted command")
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := peerRecv.Decrypt(cipher)
		if err != nil {
			t.Fatal(err)
		}
		var sent protocol.Envelope
		if json.Unmarshal(plain, &sent) != nil || sent.Type != kind || sent.ID != uint64(generation+2) || sent.ID != requestID {
			t.Fatal("reconnect lost command sequence or current send cipher")
		}
		if generation == 0 {
			wireSourceID = appendSource("current turn")
		}
		entries := w.Source(conv)
		if len(entries) != 1 || entries[0].ID != wireSourceID {
			t.Fatal("reconnect changed the pre-command source boundary")
		}
		cipher, err = peerSend.Encrypt(mustJSON(t, protocol.Envelope{ID: uint64(generation + 10),
			Type: protocol.TypeModalShown, InReplyTo: &requestID, Payload: mustJSON(t, protocol.ModalShownPayload{ModalID: "owned"})}))
		if err != nil {
			t.Fatal(err)
		}
		if err := peer.Write(ctx, websocket.MessageText, mustJSON(t, protocol.InnerFrameV2{Type: protocol.TypeNoiseMsg, Data: base64.StdEncoding.EncodeToString(cipher)})); err != nil {
			t.Fatal(err)
		}
		got := w.Next(time.Now().Add(time.Second))
		if got.Type != protocol.TypeModalShown || got.ID != uint64(generation+10) || got.InReplyTo == nil || *got.InReplyTo != requestID {
			t.Fatal("reconnect lost the current connection's correlated live frame")
		}
		if got := w.Next(time.Now().Add(time.Millisecond)); got.Type != "" {
			t.Fatal("empty source polling window returned a frame")
		}
		// Leave that read pending while the harness replaces this connection.
		_ = peer.CloseNow()
		_ = phone.Close()
	}
}

func testReconnectCiphers(t *testing.T) (initSend, initRecv, peerSend, peerRecv *noise.CipherState) {
	t.Helper()
	initiatorKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	responderKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	initiator, err := noise.NewInitiator(initiatorKey.Bytes(), responderKey.PublicKey().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	responder, err := noise.NewResponder(responderKey.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	init, err := initiator.WriteInit(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := responder.ReadInit(init); err != nil {
		t.Fatal(err)
	}
	response, peerSend, peerRecv, err := responder.WriteResp(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, initSend, initRecv, err = initiator.ReadResp(response)
	if err != nil {
		t.Fatal(err)
	}
	return initSend, initRecv, peerSend, peerRecv
}
