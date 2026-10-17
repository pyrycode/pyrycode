package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

const testBodyPayload = `{"conversation_id":"` + catchUpConversation + `","epoch":"epoch","item_id":1,"expected_rev":2}`

func testBodyResult() thread.ToolBodyResult {
	return thread.ToolBodyResult{State: thread.StateUsable, ID: 1, Rev: 2, Kind: "tool_call", Epoch: "epoch", Version: 90, Content: json.RawMessage(`{"input":{},"output":"","future":[null,false,0]}`)}
}

func testBodyPeer(t *testing.T, provider func(context.Context, protocol.ThreadExpansionRequest) (thread.ToolBodyResult, error), tweak func(*V2SessionConfig), recipient func(*V2Session)) *testHostedPeer {
	t.Helper()
	var manager atomic.Pointer[V2SessionManager]
	p := testCatchUpPeer(t, func(context.Context, protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
		return thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 20}, nil
	}, func(c *V2SessionConfig) {
		c.ThreadToolBody = provider
		if tweak != nil {
			tweak(c)
		}
		known := c.KnownConversation
		c.KnownConversation = func(id string) bool {
			// This callback runs on Run. Synthetic recipients may exercise the
			// staged handler while real handshake negotiation remains withheld.
			if m := manager.Load(); m != nil {
				for _, s := range m.sessions {
					s.threadExpansion = true
					if recipient != nil {
						recipient(s)
					}
				}
			}
			return known(id)
		}
	})
	manager.Store(p.mgr)
	if slices.Contains(p.ack.Capabilities, protocol.CapabilityThreadExpansion) {
		t.Fatal("staged capability advertised")
	}
	return p
}

func testBodyRead(t *testing.T, p *testHostedPeer, id uint64) (protocol.ThreadToolBodyPayload, int) {
	t.Helper()
	var logical strings.Builder
	var result protocol.ThreadToolBodyPayload
	bytes, frames := 0, 0
	deadline := time.Now().Add(15 * time.Second)
	for {
		e, _ := p.nextBefore(deadline)
		raw, _ := json.Marshal(e)
		frames++
		if len(raw) > protocol.MaxThreadEnvelopeBytes || e.ID < 2 || e.InReplyTo == nil || *e.InReplyTo != id || e.Type != protocol.TypeThreadToolBody {
			t.Fatalf("invalid stamped frame: type=%s bytes=%d", e.Type, len(raw))
		}
		// Compare fully stamped envelopes at fixed stamp widths across requests.
		e.ID, e.TS = hostedMaxID, time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
		raw, _ = json.Marshal(e)
		bytes += len(raw)
		var part protocol.ThreadUpdatePart
		if err := json.Unmarshal(e.Payload, &part); err != nil {
			t.Fatal(err)
		}
		if part.Continuation != nil {
			if part.Continuation.Offset != uint64(logical.Len()) || part.Continuation.Index != uint64(frames-1) || part.ConversationID != catchUpConversation || part.Epoch != "epoch" || part.ItemID != 1 || part.Rev != 2 || part.Version != 90 {
				t.Fatal("assembly identity", part)
			}
			logical.WriteString(part.Data)
			if part.Continuation.Final {
				if uint64(logical.Len()) != part.Continuation.TotalBytes || json.Unmarshal([]byte(logical.String()), &result) != nil || result.Final {
					t.Fatal("invalid assembled body")
				}
			}
			continue
		}
		var body protocol.ThreadToolBodyPayload
		if err := json.Unmarshal(e.Payload, &body); err != nil {
			t.Fatal(err)
		}
		if body.Final {
			if frames < 2 || len(body.Content) != 0 || body.ConversationID != result.ConversationID || body.Epoch != result.Epoch || body.Version != result.Version || body.ItemID != result.ItemID || body.Rev != result.Rev || body.Kind != result.Kind {
				t.Fatal("success before complete captured body", body)
			}
			return result, bytes
		}
		result = body
	}
}

func TestThreadToolBodyResults(t *testing.T) {
	for _, kind := range []string{"tool_call", "agent"} {
		for _, large := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/large=%v", kind, large), func(t *testing.T) {
				want := testBodyResult()
				want.Kind = kind
				if large {
					want.Content, _ = json.Marshal(map[string]any{"input": strings.Repeat("<&>\n\"\\世界", 30000), "output": "", "future": []any{nil, false}})
				}
				var selected atomic.Int32
				inventory := map[uint64]thread.ToolBodyResult{1: want}
				p := testBodyPeer(t, func(_ context.Context, r protocol.ThreadExpansionRequest) (thread.ToolBodyResult, error) {
					selected.Add(1)
					if r.ConversationID != catchUpConversation || r.Epoch != "epoch" || r.ItemID != 1 || r.ExpectedRev != 2 || r.Cursor != nil {
						t.Error("request scope", r)
					}
					return inventory[r.ItemID], nil
				}, nil, nil)
				testCatchUpRequest(p, 39, 0)
				e, _ := p.next()
				if e.Type != protocol.TypeThreadItems {
					t.Fatal("missing full join")
				}
				var original threadJoin
				p.mgr.hostedMu.Lock()
				original = p.mgr.hostedInboxes[p.conn].threadJoins[catchUpConversation]
				p.mgr.hostedMu.Unlock()
				previous := 0
				for run := range 2 {
					p.request(uint64(40+run), protocol.TypeThreadReadToolBody, testBodyPayload)
					got, bytes := testBodyRead(t, p, uint64(40+run))
					if got.ConversationID != catchUpConversation || got.Epoch != want.Epoch || got.Kind != kind || got.ItemID != want.ID || got.Rev != want.Rev || got.Version != want.Version || !reflect.DeepEqual(got.Content, want.Content) || run > 0 && bytes != previous {
						t.Fatalf("body or work grew: bytes=%d previous=%d kind=%s", bytes, previous, got.Kind)
					}
					previous = bytes
					// Provider inventory has no bearing on the requested output.
					for i := uint64(2); i < 2002; i++ {
						inventory[i] = thread.ToolBodyResult{Content: json.RawMessage(`{"descendant":"unrelated"}`)}
					}
				}
				if selected.Load() != 2 {
					t.Fatal("selected extra rows", selected.Load())
				}
				p.mgr.hostedMu.Lock()
				join := p.mgr.hostedInboxes[p.conn].threadJoins[catchUpConversation]
				p.mgr.hostedMu.Unlock()
				if join != original || join.version != 20 {
					t.Fatal("body advanced full coverage", join)
				}
				live, _ := json.Marshal(protocol.ThreadTextAppendPayload{ConversationID: catchUpConversation, Epoch: "epoch", Version: 21, ItemID: 999, BaseRev: 1, Rev: 2, Text: "unrelated row"})
				if err := p.mgr.Push(t.Context(), p.conn, protocol.Envelope{Type: protocol.TypeThreadTextAppend, Payload: live}); err != nil {
					t.Fatal(err)
				}
				e, _ = p.next()
				if e.Type != protocol.TypeThreadTextAppend || e.InReplyTo != nil {
					t.Fatal("unrelated live row suppressed")
				}
			})
		}
	}
}

func TestThreadToolBodyAdmission(t *testing.T) {
	var calls atomic.Int32
	provider := func(context.Context, protocol.ThreadExpansionRequest) (thread.ToolBodyResult, error) {
		calls.Add(1)
		return testBodyResult(), nil
	}
	p := testBodyPeer(t, provider, nil, nil)
	for _, field := range []string{"conversation_id", "epoch", "item_id", "expected_rev"} {
		for _, value := range []string{"", "null", "true", "{}", "[]", "-1", "1.5", "18446744073709551616", `""`} {
			var payload map[string]json.RawMessage
			json.Unmarshal([]byte(testBodyPayload), &payload)
			delete(payload, field)
			if value != "" {
				payload[field] = json.RawMessage(value)
			}
			raw, _ := json.Marshal(payload)
			p.request(40, protocol.TypeThreadReadToolBody, string(raw))
			testCatchUpError(t, p, 40)
		}
	}
	for _, suffix := range []string{`,"item_id":0}`, `,"expected_rev":0}`, `,"item_id":null}`, `,"expected_rev":null}`, `,"epoch":null}`, `,"item_id":"1"}`, `,"expected_rev":"2"}`, `,"cursor":null}`, `,"cursor":{}}`, `,"conversation_id":"AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"}`, `,"conversation_id":"22222222-2222-4222-8222-222222222222"}`} {
		p.request(40, protocol.TypeThreadReadToolBody, strings.TrimSuffix(testBodyPayload, "}")+suffix)
		testCatchUpError(t, p, 40)
	}
	for _, gate := range []string{"expansion", "thread", "interactive", "codex", "missing", "revoked"} {
		t.Run(gate, func(t *testing.T) {
			p := testBodyPeer(t, provider, func(c *V2SessionConfig) {
				if gate == "missing" {
					c.ThreadToolBody = nil
				}
				if gate == "codex" {
					c.CodexConversation = func(string) bool { return true }
				}
			}, func(s *V2Session) {
				switch gate {
				case "expansion":
					s.threadExpansion = false
				case "thread":
					s.thread = false
				case "interactive":
					s.interactive = false
				case "codex":
					s.multiAgent = false
				}
			})
			if gate == "revoked" {
				p.mgr.cfg.Devices.Remove(v2TestDevName)
			}
			p.request(40, protocol.TypeThreadReadToolBody, testBodyPayload)
			if gate == "revoked" {
				testHostedGone(t, p)
			} else {
				testCatchUpError(t, p, 40)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("invalid admission reached provider", calls.Load())
	}
	for _, id := range []string{"", `,"id":null`, `,"id":"40"`, `,"id":-1`, `,"id":18446744073709551616`} {
		p := testBodyPeer(t, provider, nil, nil)
		p.raw(`{"type":"thread_read_tool_body","payload":` + testBodyPayload + id + `}`)
		testHostedGone(t, p)
	}
}

func TestThreadToolBodyRefusals(t *testing.T) {
	for _, mode := range []string{"notloaded", "rebuilding", "unavailable", "error", "provider", "private", "stale-epoch", "stale-rev", "wrong-id", "wrong-kind", "content"} {
		t.Run(mode, func(t *testing.T) {
			p := testBodyPeer(t, func(context.Context, protocol.ThreadExpansionRequest) (thread.ToolBodyResult, error) {
				r := testBodyResult()
				switch mode {
				case "notloaded":
					r.State = thread.StateNotLoaded
				case "rebuilding":
					r.State = thread.StateRebuilding
				case "unavailable":
					r.State = thread.StateUnavailable
				case "error":
					r.Err = errors.New("private details")
				case "provider", "private":
					return thread.ToolBodyResult{}, thread.ErrBodyNotFound
				case "stale-epoch":
					r.Epoch = "old"
				case "stale-rev":
					r.Rev++
				case "wrong-id":
					r.ID++
				case "wrong-kind":
					r.Kind = "message"
				case "content":
					r.Content = nil
				}
				return r, nil
			}, nil, nil)
			for id := uint64(40); id < 42; id++ {
				p.request(id, protocol.TypeThreadReadToolBody, testBodyPayload)
				testCatchUpError(t, p, id)
			}
		})
	}
}

func TestThreadToolBodySharedSlot(t *testing.T) {
	entered, exited := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	p := testBodyPeer(t, func(ctx context.Context, r protocol.ThreadExpansionRequest) (thread.ToolBodyResult, error) {
		if r.ConversationID != catchUpConversation {
			return thread.ToolBodyResult{}, thread.ErrBodyNotFound
		}
		if calls.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			close(exited)
			return thread.ToolBodyResult{}, ctx.Err()
		}
		return testBodyResult(), nil
	}, func(c *V2SessionConfig) { c.KnownConversation = func(string) bool { return true } }, nil)
	p.request(40, protocol.TypeThreadReadToolBody, testBodyPayload)
	testHostedAwait(t, entered)
	testCatchUpRequest(p, 41, 0)
	testCatchUpError(t, p, 41)
	p.request(42, protocol.TypeThreadReadToolBody, testBodyPayload)
	testCatchUpError(t, p, 42)
	q := &testHostedPeer{t: t, mgr: p.mgr, frames: p.frames, rec: p.rec, conn: "other", pub: p.pub, events: p.events}
	q.open(threadCaps)
	q.request(70, protocol.TypeThreadReadToolBody, strings.ReplaceAll(testBodyPayload, catchUpConversation, "22222222-2222-4222-8222-222222222222"))
	testCatchUpError(t, q, 70)
	q.request(71, protocol.TypeThreadReadToolBody, testBodyPayload)
	testBodyRead(t, q, 71)
	testCatchUpLive(t, p, 91)
	testHostedAwait(t, exited)
	for range 2 {
		e, _ := p.next()
		if e.Type != protocol.TypeError && e.Type != protocol.TypeThreadTextAppend {
			t.Fatal(e)
		}
	}
	p.request(43, protocol.TypeThreadReadToolBody, testBodyPayload)
	testBodyRead(t, p, 43)
}

func TestThreadToolBodyFullQuerySlot(t *testing.T) {
	entered, exited := make(chan struct{}), make(chan struct{})
	var bodyCalls atomic.Int32
	p := testBodyPeer(t, func(context.Context, protocol.ThreadExpansionRequest) (thread.ToolBodyResult, error) {
		bodyCalls.Add(1)
		return testBodyResult(), nil
	}, func(c *V2SessionConfig) {
		c.ThreadCatchUp = func(ctx context.Context, _ protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
			close(entered)
			<-ctx.Done()
			close(exited)
			return thread.QueryResult{}, ctx.Err()
		}
	}, nil)
	testCatchUpRequest(p, 40, 0)
	testHostedAwait(t, entered)
	p.request(41, protocol.TypeThreadReadToolBody, testBodyPayload)
	testCatchUpError(t, p, 41)
	if bodyCalls.Load() != 0 {
		t.Fatal("body bypassed full query slot")
	}
	testCatchUpLive(t, p, 91)
	testHostedAwait(t, exited)
	for range 2 {
		p.next()
	}
	p.mgr.hostedMu.Lock()
	in := p.mgr.hostedInboxes[p.conn]
	if in.threadJoins == nil {
		in.threadJoins = make(map[string]threadJoin)
	}
	for i := range maxThreadJoins {
		in.threadJoins[fmt.Sprint(i)] = threadJoin{"epoch", 10}
	}
	p.mgr.hostedMu.Unlock()
	p.request(42, protocol.TypeThreadReadToolBody, testBodyPayload)
	testBodyRead(t, p, 42)
	p.mgr.hostedMu.Lock()
	_, joined := in.threadJoins[catchUpConversation]
	p.mgr.hostedMu.Unlock()
	if joined {
		t.Fatal("body created a full-thread join")
	}
}

func TestThreadToolBodyProviderShutdown(t *testing.T) {
	for _, mode := range []string{"disconnect", "reuse", "manager"} {
		t.Run(mode, func(t *testing.T) {
			entered, exited, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			p := testBodyPeer(t, func(ctx context.Context, _ protocol.ThreadExpansionRequest) (thread.ToolBodyResult, error) {
				if calls.Add(1) == 1 {
					close(entered)
					<-ctx.Done()
					close(exited)
					<-release
				}
				return testBodyResult(), nil
			}, nil, nil)
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			p.request(40, protocol.TypeThreadReadToolBody, testBodyPayload)
			testHostedAwait(t, entered)
			if mode == "manager" {
				p.stop()
			} else {
				p.frames <- protocol.RoutingEnvelope{ConnID: p.conn, CloseCode: 1000}
				testHostedGone(t, p)
			}
			testHostedAwait(t, exited)
			if mode == "reuse" {
				p.open(append(slices.Clone(threadCaps), protocol.CapabilityThreadExpansion))
				if slices.Contains(p.ack.Capabilities, protocol.CapabilityThreadExpansion) {
					t.Fatal("negotiated staged expansion")
				}
				p.request(41, protocol.TypeThreadReadToolBody, testBodyPayload)
				testBodyRead(t, p, 41)
			}
			close(release)
		})
	}
}

func TestThreadToolBodyGenerationFence(t *testing.T) {
	p := testBodyPeer(t, func(context.Context, protocol.ThreadExpansionRequest) (thread.ToolBodyResult, error) {
		return testBodyResult(), nil
	}, nil, nil)
	p.mgr.hostedMu.Lock()
	q := &threadQuery{id: 40, inbox: p.mgr.hostedInboxes[p.conn]}
	p.mgr.hostedMu.Unlock()
	p.frames <- protocol.RoutingEnvelope{ConnID: p.conn, CloseCode: 1000}
	testHostedGone(t, p)
	p.open(threadCaps)
	p.request(41, protocol.TypeThreadReadToolBody, testBodyPayload)
	testBodyRead(t, p, 41)
	// Generation identity also fences a late refusal whose caller context has
	// not observed connection cancellation yet.
	if err := p.mgr.deliverThread(t.Context(), p.conn, threadQueryError(q.id), q); !errors.Is(err, ErrConnNotFound) {
		t.Fatal("stale generation delivered", err)
	}
	p.request(42, protocol.TypeThreadReadToolBody, testBodyPayload)
	testBodyRead(t, p, 42)
}

func TestThreadToolBodyInterrupted(t *testing.T) {
	for _, mode := range []string{"deleted", "capability", "revoked", "failed-final", "disconnect", "reuse", "manager"} {
		t.Run(mode, func(t *testing.T) {
			entered, exited, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var eligible atomic.Bool
			eligible.Store(true)
			var sends atomic.Int32
			p := testBodyPeer(t, func(context.Context, protocol.ThreadExpansionRequest) (thread.ToolBodyResult, error) {
				return testBodyResult(), nil
			}, func(c *V2SessionConfig) {
				c.KnownConversation = func(string) bool { return mode != "deleted" || eligible.Load() }
				out := c.HostedAppsSend
				c.HostedAppsSend = func(ctx context.Context, e protocol.RoutingEnvelope) error {
					var f struct {
						Type string `json:"type"`
					}
					json.Unmarshal(e.Frame, &f)
					target := int32(1)
					if mode == "failed-final" {
						target = 2
					}
					if f.Type != protocol.TypeNoiseMsg || sends.Add(1) != target {
						return out(ctx, e)
					}
					defer close(exited)
					if mode != "failed-final" {
						if err := out(ctx, e); err != nil {
							return err
						}
					}
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
					if mode == "failed-final" {
						return errors.New("private transport error")
					}
					return ctx.Err()
				}
			}, func(s *V2Session) {
				if mode == "capability" {
					s.threadExpansion = eligible.Load()
				}
			})
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			p.request(40, protocol.TypeThreadReadToolBody, testBodyPayload)
			testHostedAwait(t, entered)
			switch mode {
			case "deleted", "capability":
				eligible.Store(false)
				close(release)
			case "revoked":
				p.mgr.cfg.Devices.Remove(v2TestDevName)
				close(release)
			case "failed-final":
				close(release)
			case "disconnect", "reuse":
				p.frames <- protocol.RoutingEnvelope{ConnID: p.conn, CloseCode: 1000}
			case "manager":
				p.stop()
			}
			testHostedAwait(t, exited)
			if mode == "deleted" || mode == "capability" {
				e, _ := p.next()
				var body protocol.ThreadToolBodyPayload
				if json.Unmarshal(e.Payload, &body) != nil || body.Final {
					t.Fatal("expected data only")
				}
				testCatchUpError(t, p, 40)
				eligible.Store(true)
				p.request(41, protocol.TypeThreadReadToolBody, testBodyPayload)
				testBodyRead(t, p, 41)
				return
			}
			if mode != "manager" {
				testHostedGone(t, p)
			}
			for _, wire := range p.rec.snapshot()[p.seen:] {
				if wire.ConnID != p.conn || wire.CloseCode != 0 {
					continue
				}
				var e protocol.Envelope
				json.Unmarshal(threadPlain(t, wire, p.recv), &e)
				var body protocol.ThreadToolBodyPayload
				json.Unmarshal(e.Payload, &body)
				if e.Type == protocol.TypeThreadToolBody && body.Final {
					t.Fatal("false success")
				}
			}
			if mode == "reuse" {
				p.open(threadCaps)
				p.request(41, protocol.TypeThreadReadToolBody, testBodyPayload)
				testBodyRead(t, p, 41)
			}
		})
	}
}
