package scenario

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Completing a turn during a source poll leaves a socket read outstanding.
// The next consumer must inherit that read, including its encrypted nonce order.
func TestPollingReceiveConsumerHandoff(t *testing.T) {
	for _, synchronous := range []bool{false, true} {
		name := "polling"
		if synchronous {
			name = "synchronous"
		}
		t.Run(name, func(t *testing.T) {
			serverRead := make(chan struct{}, 2)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				for {
					kind, data, err := conn.Read(r.Context())
					if err != nil {
						return
					}
					if conn.Write(r.Context(), kind, data) != nil {
						return
					}
					serverRead <- struct{}{}
				}
			}))
			defer srv.Close()
			phone, err := fakephone.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), "test", "test", "test")
			if err != nil {
				t.Fatal(err)
			}
			defer phone.Close()
			ip, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			rp, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			initiator, err := noise.NewInitiator(ip.Bytes(), rp.PublicKey().Bytes())
			if err != nil {
				t.Fatal(err)
			}
			responder, err := noise.NewResponder(rp.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			hello, err := initiator.WriteInit(nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := responder.ReadInit(hello); err != nil {
				t.Fatal(err)
			}
			ack, send, _, err := responder.WriteResp(nil)
			if err != nil {
				t.Fatal(err)
			}
			_, _, recv, err := initiator.ReadResp(ack)
			if err != nil {
				t.Fatal(err)
			}

			first := PollingReceive(t, phone, recv)
			if env := first(time.Now().Add(10 * time.Millisecond)); env.Type != "" {
				t.Fatal("unexpected frame before send")
			}
			for n := uint64(1); n <= 2; n++ {
				plain, err := json.Marshal(protocol.Envelope{ID: n, Type: protocol.TypeAck, Payload: json.RawMessage(`{}`)})
				if err != nil {
					t.Fatal(err)
				}
				cipher, err := send.Encrypt(plain)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(protocol.InnerFrameV2{Type: protocol.TypeNoiseMsg, Data: base64.StdEncoding.EncodeToString(cipher)})
				if err != nil {
					t.Fatal(err)
				}
				if err := phone.SendBytes(raw); err != nil {
					t.Fatal(err)
				}
				select {
				case <-serverRead:
				case <-time.After(time.Second):
					t.Fatal("server did not echo frame")
				}
				next := PollingReceive(t, phone, recv)
				if synchronous {
					next = Receive(t, phone, recv)
				}
				if got := next(time.Now().Add(time.Second)); got.ID != n || got.Type != protocol.TypeAck {
					t.Fatalf("consumer lost or reordered actual frame: got %+v, want ack %d", got, n)
				}
			}
		})
	}
}
