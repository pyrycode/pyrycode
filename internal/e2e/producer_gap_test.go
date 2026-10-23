//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

type gapPumpGate struct {
	mu               sync.Mutex
	entered, release chan struct{}
	retired          chan struct{}
	generations      atomic.Int32
}

func (*gapPumpGate) Enabled(context.Context, slog.Level) bool { return true }
func (g *gapPumpGate) WithAttrs([]slog.Attr) slog.Handler     { return g }
func (g *gapPumpGate) WithGroup(string) slog.Handler          { return g }
func (g *gapPumpGate) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "fakerelay: binary connected" {
		g.generations.Add(1)
	}
	event := ""
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "event" {
			event = a.Value.String()
		}
		return true
	})
	if event == "v2.push.producer_gap.teardown" {
		select {
		case g.retired <- struct{}{}:
		default:
		}
	}
	if event == "transport.send.accepted" {
		g.mu.Lock()
		entered, release := g.entered, g.release
		g.entered, g.release = nil, nil
		g.mu.Unlock()
		if entered != nil {
			close(entered)
			<-release
		}
	}
	return nil
}

func gapAwait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("gap fixture barrier timed out")
		var zero T
		return zero
	}
}

// The real fake-Claude process owns completion independently of the phone's
// lifetime. History and the thread Store are the production recovery providers.
func TestRelayV2ProducerGapRecovery(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(fmt.Sprintf("reset=%t", reset), func(t *testing.T) {
			const token = "producer-gap-paired-device"
			const conv = "38450000-0000-4000-8000-000000000001"
			const session = "38450000-0000-4000-8000-000000000002"
			const server = "producer-gap-server"
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			hist := history.New(t.TempDir())
			store := thread.NewStore(hist)
			t.Cleanup(func() { _ = store.Shutdown() })
			id := conversations.ConversationID(conv)
			if err := store.Load(ctx, id); err != nil {
				t.Fatal(err)
			}
			var accepted atomic.Int32
			var completed atomic.Int32
			var queries atomic.Int32
			acceptedCh, completedCh := make(chan struct{}, 4), make(chan struct{}, 4)
			appendSource := func(typ string, payload any) error {
				raw, err := json.Marshal(payload)
				if err != nil {
					return err
				}
				_, err = hist.AppendWithMetadata(id, typ, raw, time.Now().UTC(), history.Metadata{Session: &history.SessionProvenance{Kind: "claude", SessionID: session}})
				return err
			}
			child := exec.CommandContext(ctx, ensureFakeClaudeBuilt(t), "--input-format", "stream-json", "--output-format", "stream-json")
			child.Env = append(os.Environ(), "PYRY_FAKE_CLAUDE_STREAM_JSON=1")
			stdin, err := child.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			childDone := make(chan error, 1)
			go func() {
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					var e struct {
						Type    string `json:"type"`
						Message struct {
							Content []struct {
								Text string `json:"text"`
							} `json:"content"`
						} `json:"message"`
					}
					if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
						childDone <- err
						return
					}
					turn := fmt.Sprintf("turn-%d", completed.Load()+1)
					if e.Type == "assistant" {
						for _, part := range e.Message.Content {
							if err := appendSource(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{ConversationID: conv, TurnID: turn, Text: part.Text}); err != nil {
								childDone <- err
								return
							}
						}
					}
					if e.Type == "result" {
						if err := appendSource(protocol.TypeTurnEnd, map[string]any{"conversation_id": conv, "turn_id": turn, "stop_reason": "end_turn"}); err != nil {
							childDone <- err
							return
						}
						completed.Add(1)
						completedCh <- struct{}{}
					}
				}
				if ctx.Err() != nil {
					childDone <- nil
				} else {
					childDone <- scanner.Err()
				}
			}()
			t.Cleanup(func() {
				cancel()
				_ = stdin.Close()
				_ = child.Wait()
				if err := gapAwait(t, childDone); err != nil {
					t.Error(err)
				}
			})
			gate := &gapPumpGate{retired: make(chan struct{}, 4)}
			logger := slog.New(gate)
			fr := fakerelay.New(logger)
			t.Cleanup(func() { _ = fr.Close() })
			conn, err := relay.Connect(ctx, relay.Config{ServerID: identity.ServerID(server), BinaryVersion: "0.0.1-test", RelayURL: fr.URL() + "/v2/server", Logger: logger, AllowInsecureScheme: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close(); _ = conn.Wait() })
			waitCtx, waitCancel := context.WithTimeout(ctx, 5*time.Second)
			defer waitCancel()
			if !fr.WaitBinary(waitCtx, server) {
				t.Fatal("binary registration missing")
			}
			key, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			reg := &devices.Registry{}
			reg.Add(devices.Device{TokenHash: devices.HashToken(token), Name: "gap-phone", PairedAt: time.Now().UTC()})
			cfg := relay.V2SessionConfig{Frames: conn.Frames(), Outbound: conn.Send, OutboundContext: conn.SendContext, Connected: conn.Connected,
				StaticPriv: key.Bytes(), Devices: reg, ServerID: server, Logger: logger, ThreadReady: func() bool { return true },
				KnownConversation: func(c string) bool { return c == conv },
				ThreadCatchUp: func(ctx context.Context, p protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
					queries.Add(1)
					return store.CatchUp(ctx, id, p.Epoch, p.Version, p.Limit)
				},
				ThreadPage: func(ctx context.Context, p protocol.ThreadPagePayload) (thread.QueryResult, error) {
					return store.HistoryPage(ctx, id, p.BeforeOrder, p.Limit)
				},
				ThreadLiveState: func() func() (relay.LiveState, bool) {
					reading := completed.Load()
					return func() (relay.LiveState, bool) {
						if reading == 0 {
							return relay.LiveState{}, false
						}
						n := reading
						reading = 0
						return relay.LiveState{Envelope: protocol.Envelope{Type: protocol.TypeTurnState, SessionID: json.RawMessage(`"` + session + `"`), Payload: mustJSON(t, map[string]any{"conversation_id": conv, "state": "idle", "turn_id": fmt.Sprintf("turn-%d", n)})}, ConversationID: conv, SessionGeneration: 1, Revision: uint64(n)}, true
					}
				},
				Handlers: map[string]dispatch.Handler{protocol.TypeSendMessage: func(_ context.Context, _ *dispatch.Conn, e protocol.Envelope) error {
					var p protocol.SendMessagePayload
					if err := json.Unmarshal(e.Payload, &p); err != nil {
						return err
					}
					if err := appendSource(protocol.TypeMessage, map[string]any{"role": "user", "text": p.Text, "message_id": p.MessageID}); err != nil {
						return err
					}
					accepted.Add(1)
					acceptedCh <- struct{}{}
					return json.NewEncoder(stdin).Encode(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": p.Text}}}})
				}},
			}
			mgr, err := relay.NewV2SessionManager(cfg)
			if err != nil {
				t.Fatal(err)
			}
			mgrDone := make(chan error, 1)
			go func() { mgrDone <- mgr.Run(ctx) }()
			t.Cleanup(func() { cancel(); <-mgrDone })
			type phonePeer struct {
				phone      *fakephone.Client
				send, recv *noise.CipherState
			}
			dial := func() phonePeer {
				t.Helper()
				p, err := fakephone.Dial(ctx, fr.URL(), server, token, "gap-phone")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = p.Close() })
				init, err := noise.NewInitiator(fakephone.InstallKey(token), key.PublicKey().Bytes())
				if err != nil {
					t.Fatal(err)
				}
				hello := protocol.Envelope{ID: 1, Type: protocol.TypeHello, TS: time.Now().UTC(), Payload: mustJSON(t, protocol.HelloClientPayload{Role: "client", DeviceName: "gap-phone", ClientVersion: "0.0.1-test", ProtocolVersions: []string{"v2"}, Token: token, Capabilities: []string{protocol.CapabilityInteractive, protocol.CapabilityThread}})}
				msg, err := init.WriteInit(mustJSON(t, hello))
				if err != nil {
					t.Fatal(err)
				}
				sendNoiseInit(t, p, msg)
				inner := readInnerFrame(t, p, 5*time.Second)
				raw, err := base64.StdEncoding.DecodeString(inner.Data)
				if err != nil {
					t.Fatal(err)
				}
				ack, send, recv, err := init.ReadResp(raw)
				if err != nil {
					t.Fatal(err)
				}
				var env protocol.Envelope
				var grant protocol.HelloAckPayload
				if json.Unmarshal(ack, &env) != nil || json.Unmarshal(env.Payload, &grant) != nil || !slices.Contains(grant.Capabilities, "thread") || !slices.Contains(grant.Capabilities, "interactive") {
					t.Fatalf("missing authenticated grants: %s", ack)
				}
				return phonePeer{p, send, recv}
			}
			send := func(p phonePeer, id uint64, typ string, payload any) {
				t.Helper()
				sealed, err := p.send.Encrypt(mustJSON(t, protocol.Envelope{ID: id, Type: typ, TS: time.Now().UTC(), Payload: mustJSON(t, payload)}))
				if err != nil {
					t.Fatal(err)
				}
				sendNoiseMsg(t, p.phone, sealed)
			}
			next := func(p phonePeer) protocol.Envelope {
				t.Helper()
				inner := readInnerFrame(t, p.phone, 5*time.Second)
				raw, err := base64.StdEncoding.DecodeString(inner.Data)
				if err != nil {
					t.Fatal(err)
				}
				plain, err := p.recv.Decrypt(raw)
				if err != nil {
					t.Fatal(err)
				}
				var env protocol.Envelope
				if err := json.Unmarshal(plain, &env); err != nil {
					t.Fatal(err)
				}
				return env
			}
			query := func(p phonePeer, request uint64, epoch string, version uint64) (protocol.ThreadItemsPayload, []protocol.ThreadItem, bool) {
				t.Helper()
				send(p, request, protocol.TypeThreadCatchUp, protocol.ThreadCatchUpPayload{ConversationID: conv, Epoch: epoch, Version: version, Limit: 128})
				var items []protocol.ThreadItem
				for {
					e := next(p)
					if e.InReplyTo == nil || *e.InReplyTo != request {
						continue
					}
					if e.Type == protocol.TypeThreadReset {
						return protocol.ThreadItemsPayload{}, nil, true
					}
					if e.Type != protocol.TypeThreadItems {
						t.Fatalf("query refusal: %s %s", e.Type, e.Payload)
					}
					var body protocol.ThreadItemsPayload
					if err := json.Unmarshal(e.Payload, &body); err != nil {
						t.Fatal(err)
					}
					items = append(items, body.Items...)
					if body.Final {
						return body, items, false
					}
				}
			}
			waitSource := func() {
				t.Helper()
				gapAwait(t, completedCh)
				deadline := time.Now().Add(5 * time.Second)
				for {
					snap := store.Snapshot(id)
					if snap.State == thread.StateUsable && snap.Version >= uint64(completed.Load()*3) {
						return
					}
					if time.Now().After(deadline) {
						t.Fatalf("source not folded: %+v", snap)
					}
					time.Sleep(time.Millisecond)
				}
			}
			phone, other := dial(), dial()
			send(phone, 2, protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: conv, MessageID: "first", Text: "first turn"})
			gapAwait(t, acceptedCh)
			waitSource()
			checkpoint, _, resetReply := query(phone, 3, "", 0)
			if resetReply || checkpoint.Version == 0 {
				t.Fatal("first checkpoint not completed")
			}
			if queries.Load() != 1 {
				t.Fatal("hello queried thread content")
			}
			send(phone, 4, protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: conv, MessageID: "second", Text: "accepted second turn"})
			gapAwait(t, acceptedCh)
			// Hold a selected control in the actual binary transport pump.
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			gate.mu.Lock()
			gate.entered, gate.release = entered, release
			gate.mu.Unlock()
			if err := mgr.Push(ctx, "c-1", protocol.Envelope{Type: protocol.TypeError, Payload: mustJSON(t, protocol.ErrorPayload{Code: "thread.query_failed", Message: "held control"})}); err != nil {
				t.Fatal(err)
			}
			gapAwait(t, entered)
			if err := mgr.Push(ctx, "c-1", protocol.Envelope{Type: protocol.TypeError, Payload: json.RawMessage(`{"dependent":true}`)}); err != nil {
				t.Fatal(err)
			}
			if err := mgr.RetireLiveState(ctx, "c-1"); err != nil {
				t.Fatal(err)
			}
			gapAwait(t, gate.retired)
			mgr.ActiveConns(ctx)
			unblock()
			sawControl := false
			for {
				raw, err := phone.phone.ReceiveBytes(5 * time.Second)
				if err != nil {
					code, ok := phone.phone.LastCloseStatus()
					if !ok || code != relay.StatusProducerGap {
						t.Fatalf("phone close=%d present=%t err=%v", code, ok, err)
					}
					if !sawControl {
						t.Fatal("phone close overtook selected control")
					}
					break
				}
				var inner protocol.InnerFrameV2
				if json.Unmarshal(raw, &inner) != nil {
					t.Fatal("bad terminal frame")
				}
				cipher, _ := base64.StdEncoding.DecodeString(inner.Data)
				plain, err := phone.recv.Decrypt(cipher)
				if err != nil {
					t.Fatal(err)
				}
				var e protocol.Envelope
				_ = json.Unmarshal(plain, &e)
				sawControl = sawControl || e.Type == protocol.TypeError && bytes.Contains(e.Payload, []byte("held control"))
				if string(e.Payload) == `{"dependent":true}` {
					t.Fatal("quarantined reading escaped")
				}
			}
			waitSource()
			// A peer query proves the original shared socket and its Noise lane work.
			_, _, _ = query(other, 2, "", 0)
			if gate.generations.Load() != 1 {
				t.Fatal("shared binary socket was replaced")
			}
			if reset {
				if err := store.Unload(id); err != nil {
					t.Fatal(err)
				}
				// Losing only the disposable index forces a new epoch while the
				// accepted source history and receiving session remain retained.
				dir, err := hist.LogDir(id)
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"thread-certificate.json", "thread-index-state.json"} {
					if err := os.Remove(filepath.Join(dir, name)); err != nil {
						t.Fatal(err)
					}
				}
				if err := store.Load(ctx, id); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(5 * time.Second)
				for store.Snapshot(id).State != thread.StateUsable {
					if time.Now().After(deadline) {
						t.Fatal("reset replay stalled")
					}
					time.Sleep(time.Millisecond)
				}
			}
			before := queries.Load()
			returning := dial()
			// Current source-owned idle must reconcile without querying history.
			for {
				e := next(returning)
				if e.Type != protocol.TypeTurnState || e.SessionStateCleared {
					continue
				}
				var p struct{ State, TurnID string }
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(e.Payload, &fields)
				_ = json.Unmarshal(fields["state"], &p.State)
				_ = json.Unmarshal(fields["turn_id"], &p.TurnID)
				if p.State != "idle" || p.TurnID != "turn-2" || string(e.SessionID) != `"`+session+`"` {
					t.Fatalf("wrong terminal source: %+v", e)
				}
				break
			}
			if queries.Load() != before {
				t.Fatal("reconnect hello implicitly queried content")
			}
			recovered, items, gotReset := query(returning, 2, checkpoint.Epoch, checkpoint.Version)
			if gotReset != reset {
				t.Fatalf("reset=%t want %t", gotReset, reset)
			}
			if gotReset {
				recovered, items, gotReset = query(returning, 3, "", 0)
			}
			if gotReset || recovered.Version <= checkpoint.Version {
				t.Fatal("recovery did not complete")
			}
			users, replies := 0, 0
			for _, item := range items {
				if item.Kind == "user_message" && item.Summary == "accepted second turn" {
					users++
					if item.Session != session {
						t.Fatalf("receiving source lost: %+v", item)
					}
				}
				if item.Kind == "assistant_message" && bytes.Contains(item.Content, []byte("accepted second turn")) {
					replies++
					if item.Active || item.Session != session || item.Turn != "turn-2" {
						t.Fatalf("second completion/source lost: %+v", item)
					}
				}
			}
			if users != 1 || replies != 1 || accepted.Load() != 2 || completed.Load() != 2 {
				t.Fatalf("recovery users=%d replies=%d accepted=%d completed=%d items=%+v", users, replies, accepted.Load(), completed.Load(), items)
			}
		})
	}
}
