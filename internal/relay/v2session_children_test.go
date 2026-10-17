package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

var expansionCaps = append(slices.Clone(threadCaps), protocol.CapabilityThreadExpansion)

func testChildrenPeer(t *testing.T, provider func(context.Context, protocol.ThreadExpansionRequest) (thread.ChildPageResult, error), tweak func(*V2SessionConfig)) *testHostedPeer {
	t.Helper()
	return testHostedStartCaps(t, &testAppDiscovery{}, expansionCaps, func(c *V2SessionConfig) {
		c.ThreadReady = func() bool { return true }
		c.KnownConversation = func(id string) bool { return id == catchUpConversation }
		c.ThreadToolBody = func(context.Context, protocol.ThreadExpansionRequest) (thread.ToolBodyResult, error) {
			return testBodyResult(), nil
		}
		c.ThreadChildren = provider
		if tweak != nil {
			tweak(c)
		}
	})
}

func testChildrenResult(r protocol.ThreadExpansionRequest) thread.ChildPageResult {
	return thread.ChildPageResult{State: thread.StateUsable, ConversationID: conversations.ConversationID(r.ConversationID), Epoch: r.Epoch, Version: 90, Parent: r.ItemID, ParentRev: r.ExpectedRev, Complete: true}
}

func testChildrenRead(t *testing.T, p *testHostedPeer, id uint64) (protocol.ThreadChildrenPayload, int) {
	t.Helper()
	var result protocol.ThreadChildrenPayload
	total, frames := 0, 0
	for {
		e, _ := p.nextBefore(time.Now().Add(15 * time.Second))
		raw, _ := json.Marshal(e)
		total += len(raw)
		frames++
		if e.Type != protocol.TypeThreadChildren || e.ID < 2 || e.InReplyTo == nil || *e.InReplyTo != id || total > protocol.MaxThreadEnvelopeBytes {
			t.Fatalf("invalid child frame: type=%s total=%d", e.Type, total)
		}
		var part protocol.ThreadChildrenPayload
		if json.Unmarshal(e.Payload, &part) != nil || part.Epoch != "epoch" || part.Version != 90 || part.Rev != 2 || part.ConversationID != catchUpConversation {
			t.Fatal("capture identity", string(e.Payload))
		}
		if part.Final {
			if len(part.Children) != 0 || part.More == nil || len(result.Children) > 0 && frames != 2 {
				t.Fatal("bad completion", part)
			}
			part.Children = result.Children
			return part, total
		}
		if frames != 1 || part.More != nil || part.Cursor != nil {
			t.Fatal("fragmented page")
		}
		result = part
	}
}

func TestThreadExpansionHandshake(t *testing.T) {
	for mask := 0; mask < 64; mask++ {
		t.Run(fmt.Sprint(mask), func(t *testing.T) {
			var caps []string
			for i, cap := range []string{protocol.CapabilityThreadExpansion, protocol.CapabilityThread, protocol.CapabilityInteractive} {
				if mask&(1<<i) != 0 {
					caps = append(caps, cap)
				}
			}
			p := testHostedStartCaps(t, &testAppDiscovery{}, caps, func(c *V2SessionConfig) {
				c.ThreadReady = func() bool { return mask&8 != 0 }
				c.KnownConversation = func(string) bool { return true }
				if mask&16 != 0 {
					c.ThreadToolBody = func(context.Context, protocol.ThreadExpansionRequest) (thread.ToolBodyResult, error) {
						return testBodyResult(), nil
					}
				}
				if mask&32 != 0 {
					c.ThreadChildren = func(_ context.Context, r protocol.ThreadExpansionRequest) (thread.ChildPageResult, error) {
						return testChildrenResult(r), nil
					}
				}
			})
			if slices.Contains(p.ack.Capabilities, protocol.CapabilityThreadExpansion) != (mask == 63) || slices.Contains(p.ack.Capabilities, protocol.CapabilityThread) != (mask&10 == 10) {
				t.Fatal("capability matrix", p.ack.Capabilities)
			}
			p.request(40, protocol.TypeThreadReadChildren, testBodyPayload)
			if mask == 63 {
				testChildrenRead(t, p, 40)
			} else {
				testCatchUpError(t, p, 40)
			}
		})
	}
}

func TestThreadChildrenPages(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(fmt.Sprint(large), func(t *testing.T) {
			var selected atomic.Int32
			p := testChildrenPeer(t, func(_ context.Context, r protocol.ThreadExpansionRequest) (thread.ChildPageResult, error) {
				out := testChildrenResult(r)
				start := uint64(2)
				if r.Cursor != nil {
					start = r.Cursor.ID + 1
				}
				for id := start; id < 8 && len(out.Items) < r.Limit; id++ {
					row := thread.ChildSummary{ID: id, Order: 0, Rev: 3, Parent: r.ItemID, Kind: "tool_call", Session: "recorded-session", Headline: strings.Repeat("界", 210)}
					if large {
						row.Subtype = strings.Repeat("<&>", 1800)
					}
					out.Items = append(out.Items, row)
				}
				selected.Add(int32(len(out.Items)))
				out.More = start+uint64(len(out.Items)) < 8
				out.Complete = !out.More
				return out, nil
			}, nil)
			p.mgr.hostedMu.Lock()
			in := p.mgr.hostedInboxes[p.conn]
			in.threadJoins = map[string]threadJoin{catchUpConversation: {"epoch", 20}}
			for i := 1; i < maxThreadJoins; i++ {
				in.threadJoins[fmt.Sprint(i)] = threadJoin{"epoch", 20}
			}
			p.mgr.hostedMu.Unlock()
			r := protocol.ThreadExpansionRequest{ConversationID: catchUpConversation, Epoch: "epoch", ItemID: 1, ExpectedRev: 2, Limit: 2}
			var ids []uint64
			pages := 0
			for {
				raw, _ := json.Marshal(r)
				p.request(40, protocol.TypeThreadReadChildren, string(raw))
				got, _ := testChildrenRead(t, p, 40)
				pages++
				for _, row := range got.Children {
					if row.Parent != 1 || row.Session != "recorded-session" || !row.DetailOmitted || len([]rune(row.Headline)) != 200 {
						t.Fatal("summary", row)
					}
					ids = append(ids, row.ID)
				}
				if !*got.More {
					break
				}
				if got.Cursor == nil || got.Cursor.ID != ids[len(ids)-1] {
					t.Fatal("cursor skipped row")
				}
				r.Cursor = got.Cursor
			}
			if !slices.Equal(ids, []uint64{2, 3, 4, 5, 6, 7}) || !large && pages != 3 || large && (pages <= 3 || selected.Load() <= 6) {
				t.Fatal("paging", ids, pages, selected.Load())
			}
			r.ItemID, r.Cursor = 2, nil
			raw, _ := json.Marshal(r)
			p.request(41, protocol.TypeThreadReadChildren, string(raw))
			got, _ := testChildrenRead(t, p, 41)
			if got.ItemID != 2 || got.Children[0].Parent != 2 {
				t.Fatal("nested parent")
			}
			p.mgr.hostedMu.Lock()
			join, count := in.threadJoins[catchUpConversation], len(in.threadJoins)
			p.mgr.hostedMu.Unlock()
			if join.version != 20 || count != maxThreadJoins {
				t.Fatal("child altered joins")
			}
			testCatchUpLive(t, p, 21)
			e, _ := p.next()
			if e.Type != protocol.TypeThreadTextAppend {
				t.Fatal("unrelated live suppressed")
			}
		})
	}
}

func TestThreadChildrenAdmission(t *testing.T) {
	var calls atomic.Int32
	p := testChildrenPeer(t, func(_ context.Context, r protocol.ThreadExpansionRequest) (thread.ChildPageResult, error) {
		calls.Add(1)
		return testChildrenResult(r), nil
	}, nil)
	base := `{"conversation_id":"` + catchUpConversation + `","epoch":"epoch","item_id":1,"expected_rev":2,"cursor":{"conversation_id":"` + catchUpConversation + `","epoch":"epoch","item_id":1,"rev":2,"version":0,"order":0,"id":2}}`
	for _, cursor := range []bool{false, true} {
		for _, field := range []string{"conversation_id", "epoch", "item_id", "expected_rev", "rev", "version", "order", "id"} {
			if !cursor && slices.Contains([]string{"rev", "version", "order", "id"}, field) || cursor && field == "expected_rev" {
				continue
			}
			for _, value := range []string{"", "null", "true", "{}", "[]", "-1", "1.5", "18446744073709551616", `"wrong"`} {
				if !cursor && field == "epoch" && value == `"wrong"` {
					continue
				}
				var obj map[string]json.RawMessage
				json.Unmarshal([]byte(base), &obj)
				target := obj
				if cursor {
					target = nil
					json.Unmarshal(obj["cursor"], &target)
				} else {
					delete(obj, "cursor")
				}
				delete(target, field)
				if value != "" {
					target[field] = json.RawMessage(value)
				}
				if cursor {
					obj["cursor"], _ = json.Marshal(target)
				}
				raw, _ := json.Marshal(obj)
				p.request(40, protocol.TypeThreadReadChildren, string(raw))
				t.Run(fmt.Sprintf("cursor=%v/%s/%s", cursor, field, value), func(t *testing.T) { testCatchUpError(t, p, 40) })
			}
		}
	}
	for _, suffix := range []string{`,"cursor":null}`, `,"cursor":[]}`, `,"cursor":true}`, `,"epoch":""}`, `,"item_id":0}`, `,"expected_rev":0}`, `,"limit":null}`, `,"limit":-1}`, `,"limit":1.5}`, `,"limit":"2"}`, `,"conversation_id":"AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"}`, `,"conversation_id":"22222222-2222-4222-8222-222222222222"}`} {
		p.request(40, protocol.TypeThreadReadChildren, strings.TrimSuffix(testBodyPayload, "}")+suffix)
		testCatchUpError(t, p, 40)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request called provider", calls.Load())
	}
	for _, id := range []string{"", `,"id":null`, `,"id":"40"`, `,"id":-1`, `,"id":18446744073709551616`} {
		q := testChildrenPeer(t, nil, nil)
		q.raw(`{"type":"thread_read_children","payload":` + testBodyPayload + id + `}`)
		testHostedGone(t, q)
	}
	for _, limit := range []string{"", `,"limit":0`, `,"limit":1`, `,"limit":999`, `,"limit":18446744073709551615`} {
		q := testChildrenPeer(t, func(_ context.Context, r protocol.ThreadExpansionRequest) (thread.ChildPageResult, error) {
			want := 50
			if limit == `,"limit":1` {
				want = 1
			}
			if limit == `,"limit":999` || limit == `,"limit":18446744073709551615` {
				want = 256
			}
			if r.Limit != want {
				t.Error("limit", r.Limit, want)
			}
			return testChildrenResult(r), nil
		}, nil)
		q.request(40, protocol.TypeThreadReadChildren, strings.TrimSuffix(testBodyPayload, "}")+limit+`}`)
		testChildrenRead(t, q, 40)
	}
}

func TestThreadChildrenProviderScope(t *testing.T) {
	var calls atomic.Int32
	p := testChildrenPeer(t, func(_ context.Context, r protocol.ThreadExpansionRequest) (thread.ChildPageResult, error) {
		calls.Add(1)
		if r.ConversationID != catchUpConversation || r.ItemID != 1 {
			return thread.ChildPageResult{}, thread.ErrChildParentNotFound
		}
		if r.Epoch != "epoch" || r.ExpectedRev != 2 || r.Cursor != nil && (r.Cursor.Version != 0 || r.Cursor.Order != 0 || r.Cursor.ID != 2) {
			return thread.ChildPageResult{}, thread.ErrStaleChildPage
		}
		out := testChildrenResult(r)
		out.Version = 0
		return out, nil
	}, func(c *V2SessionConfig) { c.KnownConversation = func(string) bool { return true } })
	for _, payload := range []string{
		strings.ReplaceAll(testBodyPayload, catchUpConversation, "22222222-2222-4222-8222-222222222222"),
		strings.ReplaceAll(testBodyPayload, `"item_id":1`, `"item_id":99`),
		strings.ReplaceAll(testBodyPayload, `"epoch":"epoch"`, `"epoch":"old"`),
		strings.ReplaceAll(testBodyPayload, `"expected_rev":2`, `"expected_rev":3`),
	} {
		p.request(40, protocol.TypeThreadReadChildren, payload)
		testCatchUpError(t, p, 40)
	}
	for _, tuple := range []string{`"version":1,"order":0,"id":2`, `"version":0,"order":1,"id":2`, `"version":0,"order":0,"id":99`, `"version":0,"order":0,"id":2`} {
		payload := strings.TrimSuffix(testBodyPayload, "}") + `,"cursor":{"conversation_id":"` + catchUpConversation + `","epoch":"epoch","item_id":1,"rev":2,` + tuple + `}}`
		p.request(40, protocol.TypeThreadReadChildren, payload)
		if tuple != `"version":0,"order":0,"id":2` {
			testCatchUpError(t, p, 40)
			continue
		}
		e, _ := p.next()
		var page protocol.ThreadChildrenPayload
		if e.Type != protocol.TypeThreadChildren || json.Unmarshal(e.Payload, &page) != nil || !page.Final || page.Version != 0 {
			t.Fatal("zero cursor rejected")
		}
	}
	if calls.Load() != 8 {
		t.Fatal("provider scope admission", calls.Load())
	}
}

func TestThreadChildrenRefusals(t *testing.T) {
	for _, mode := range []string{"empty", "missing", "notloaded", "rebuilding", "unavailable", "error", "provider", "epoch", "revision", "parent", "conversation", "version", "no-fit", "inconsistent", "forged-parent"} {
		t.Run(mode, func(t *testing.T) {
			p := testChildrenPeer(t, func(_ context.Context, r protocol.ThreadExpansionRequest) (thread.ChildPageResult, error) {
				out := testChildrenResult(r)
				switch mode {
				case "notloaded":
					out.State = thread.StateNotLoaded
				case "rebuilding":
					out.State = thread.StateRebuilding
				case "unavailable":
					out.State = thread.StateUnavailable
				case "error":
					out.Err = errors.New("secret")
				case "provider", "forged-parent":
					return out, thread.ErrChildParentNotFound
				case "epoch":
					out.Epoch = "old"
				case "revision":
					out.ParentRev++
				case "parent":
					out.Parent++
				case "conversation":
					out.ConversationID = "22222222-2222-4222-8222-222222222222"
				case "version":
					out.Version++
				case "inconsistent":
					out.More = true
				case "no-fit":
					out.Items = []thread.ChildSummary{{ID: 2, Parent: 1, Rev: 1, Kind: "tool_call", Subtype: strings.Repeat("<&>", 70000)}}
				}
				return out, nil
			}, func(c *V2SessionConfig) {
				if mode == "missing" {
					c.ThreadChildren = nil
				}
			})
			payload := testBodyPayload
			if mode == "version" {
				payload = strings.TrimSuffix(payload, "}") + `,"cursor":{"conversation_id":"` + catchUpConversation + `","epoch":"epoch","item_id":1,"rev":2,"version":90,"order":0,"id":2}}`
			}
			for id := uint64(40); id < 42; id++ {
				p.request(id, protocol.TypeThreadReadChildren, payload)
				if mode == "empty" {
					got, _ := testChildrenRead(t, p, id)
					if *got.More || len(got.Children) != 0 {
						t.Fatal("empty page")
					}
				} else {
					testCatchUpError(t, p, id)
				}
			}
		})
	}
}

func TestThreadChildrenSharedSlot(t *testing.T) {
	for _, held := range []string{protocol.TypeThreadReadChildren, protocol.TypeThreadReadToolBody, protocol.TypeThreadCatchUp} {
		t.Run(held, func(t *testing.T) {
			entered, exited := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			hold := func(ctx context.Context) {
				if calls.Add(1) == 1 {
					close(entered)
					<-ctx.Done()
					close(exited)
				}
			}
			p := testChildrenPeer(t, func(ctx context.Context, r protocol.ThreadExpansionRequest) (thread.ChildPageResult, error) {
				if r.ConversationID == catchUpConversation && held == protocol.TypeThreadReadChildren {
					hold(ctx)
				}
				return testChildrenResult(r), ctx.Err()
			}, func(c *V2SessionConfig) {
				c.KnownConversation = func(string) bool { return true }
				c.ThreadToolBody = func(ctx context.Context, _ protocol.ThreadExpansionRequest) (thread.ToolBodyResult, error) {
					if held == protocol.TypeThreadReadToolBody {
						hold(ctx)
					}
					return testBodyResult(), ctx.Err()
				}
				c.ThreadCatchUp = func(ctx context.Context, _ protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
					if held == protocol.TypeThreadCatchUp {
						hold(ctx)
					}
					return thread.QueryResult{State: thread.StateUsable, Epoch: "epoch", Version: 20}, ctx.Err()
				}
			})
			if held == protocol.TypeThreadCatchUp {
				testCatchUpRequest(p, 40, 0)
			} else {
				p.request(40, held, testBodyPayload)
			}
			testHostedAwait(t, entered)
			for i, kind := range []string{protocol.TypeThreadReadChildren, protocol.TypeThreadReadToolBody, protocol.TypeThreadCatchUp} {
				if kind == protocol.TypeThreadCatchUp {
					testCatchUpRequest(p, uint64(41+i), 0)
				} else {
					p.request(uint64(41+i), kind, testBodyPayload)
				}
				testCatchUpError(t, p, uint64(41+i))
			}
			q := &testHostedPeer{t: t, mgr: p.mgr, frames: p.frames, rec: p.rec, conn: "other", pub: p.pub, events: p.events}
			q.open(expansionCaps)
			q.request(70, protocol.TypeThreadReadChildren, testBodyPayload)
			testChildrenRead(t, q, 70)
			q.request(71, protocol.TypeThreadReadChildren, strings.ReplaceAll(testBodyPayload, catchUpConversation, "22222222-2222-4222-8222-222222222222"))
			e, _ := q.next()
			var page protocol.ThreadChildrenPayload
			if e.Type != protocol.TypeThreadChildren || json.Unmarshal(e.Payload, &page) != nil || page.ConversationID == catchUpConversation || !page.Final {
				t.Fatal("other conversation misrouted")
			}
			testCatchUpLive(t, p, 91)
			testHostedAwait(t, exited)
			for range 2 {
				e, _ := p.next()
				if e.Type != protocol.TypeError && e.Type != protocol.TypeThreadTextAppend {
					t.Fatal("cancelled query succeeded")
				}
			}
			p.request(44, protocol.TypeThreadReadChildren, testBodyPayload)
			testChildrenRead(t, p, 44)
		})
	}
}

func TestThreadChildrenProviderShutdown(t *testing.T) {
	for _, mode := range []string{"disconnect", "reuse", "manager"} {
		t.Run(mode, func(t *testing.T) {
			entered, exited, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			p := testChildrenPeer(t, func(ctx context.Context, r protocol.ThreadExpansionRequest) (thread.ChildPageResult, error) {
				if calls.Add(1) == 1 {
					close(entered)
					<-ctx.Done()
					close(exited)
					<-release
				}
				return testChildrenResult(r), nil
			}, nil)
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			p.request(40, protocol.TypeThreadReadChildren, testBodyPayload)
			testHostedAwait(t, entered)
			if mode == "manager" {
				p.stop()
			} else {
				p.frames <- protocol.RoutingEnvelope{ConnID: p.conn, CloseCode: 1000}
				testHostedGone(t, p)
			}
			testHostedAwait(t, exited)
			if mode == "reuse" {
				p.open(expansionCaps)
				p.request(41, protocol.TypeThreadReadChildren, testBodyPayload)
				testChildrenRead(t, p, 41)
			}
			close(release)
		})
	}
}

func TestThreadChildrenInterrupted(t *testing.T) {
	for _, mode := range []string{"deleted", "expansion", "thread", "interactive", "codex", "revoked", "failed-final", "reuse", "manager"} {
		t.Run(mode, func(t *testing.T) {
			entered, exited, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var eligible atomic.Bool
			eligible.Store(true)
			var sends atomic.Int32
			var manager atomic.Pointer[V2SessionManager]
			p := testChildrenPeer(t, func(_ context.Context, r protocol.ThreadExpansionRequest) (thread.ChildPageResult, error) {
				out := testChildrenResult(r)
				out.Items = []thread.ChildSummary{{ID: 2, Rev: 1, Parent: 1, Order: 0, Kind: "agent"}}
				return out, nil
			}, func(c *V2SessionConfig) {
				c.KnownConversation = func(string) bool {
					if m := manager.Load(); m != nil {
						for _, s := range m.sessions {
							switch mode {
							case "expansion":
								s.threadExpansion = eligible.Load()
							case "thread":
								s.thread = eligible.Load()
							case "interactive":
								s.interactive = eligible.Load()
							}
						}
					}
					return mode != "deleted" || eligible.Load()
				}
				c.CodexConversation = func(string) bool { return mode == "codex" && !eligible.Load() }
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
						return errors.New("secret transport error")
					}
					return ctx.Err()
				}
			})
			manager.Store(p.mgr)
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			p.request(40, protocol.TypeThreadReadChildren, testBodyPayload)
			testHostedAwait(t, entered)
			switch mode {
			case "deleted", "expansion", "thread", "interactive", "codex":
				eligible.Store(false)
				close(release)
			case "revoked":
				p.mgr.cfg.Devices.Remove(v2TestDevName)
				close(release)
			case "failed-final":
				close(release)
			case "reuse":
				p.frames <- protocol.RoutingEnvelope{ConnID: p.conn, CloseCode: 1000}
			case "manager":
				p.stop()
			}
			testHostedAwait(t, exited)
			if slices.Contains([]string{"deleted", "expansion", "thread", "interactive", "codex"}, mode) {
				e, _ := p.next()
				var page protocol.ThreadChildrenPayload
				if json.Unmarshal(e.Payload, &page) != nil || page.Final {
					t.Fatal("expected data only")
				}
				if mode != "thread" && mode != "interactive" {
					testCatchUpError(t, p, 40)
				}
				deadline := time.Now().Add(5 * time.Second)
				for {
					p.mgr.hostedMu.Lock()
					pending := p.mgr.hostedInboxes[p.conn].query != nil
					p.mgr.hostedMu.Unlock()
					if !pending {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("query slot stuck")
					}
					time.Sleep(time.Millisecond)
				}
				eligible.Store(true)
				if mode == "thread" || mode == "interactive" {
					p.frames <- protocol.RoutingEnvelope{ConnID: p.conn, CloseCode: 1000}
					testHostedGone(t, p)
					p.open(expansionCaps)
				}
				p.request(41, protocol.TypeThreadReadChildren, testBodyPayload)
				testChildrenRead(t, p, 41)
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
				var page protocol.ThreadChildrenPayload
				json.Unmarshal(e.Payload, &page)
				if e.Type == protocol.TypeThreadChildren && page.Final {
					t.Fatal("false success")
				}
			}
			if mode == "reuse" {
				p.open(expansionCaps)
				p.request(41, protocol.TypeThreadReadChildren, testBodyPayload)
				testChildrenRead(t, p, 41)
			}
		})
	}
}
