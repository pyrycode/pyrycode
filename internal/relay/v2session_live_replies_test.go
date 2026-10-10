package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

var suppliedReplyCases = []struct{ request, reply, body string }{
	{protocol.TypeRequestContextUsage, protocol.TypeContextUsage, `{"conversation_id":"payload-conv","used_tokens":37,"window_tokens":100,"extra":{"keep":true}}`},
	{protocol.TypeMCPStatusRequest, protocol.TypeMCPStatus, `{"conversation_id":"payload-conv","servers":[{"name":"s","status":"failed","error":"reason","scope":"project","version":"1"}],"dropped_servers":7}`},
	{protocol.TypeRequestModelList, protocol.TypeModelList, `{"conversation_id":"payload-conv","models":[{"value":"m","display_name":"Model","agent":"claude","family":"family"}],"dropped_models":8}`},
	{protocol.TypeRequestSessionSettings, protocol.TypeSessionSettings, `{"session_id":"payload-session","model":"sonnet","effort":"high","yolo":true,"permission_mode":"bypassPermissions","used_tokens":37,"window_tokens":100,"extra":9}`},
	{protocol.TypeSetSessionSettings, protocol.TypeSessionSettingsUpdated, `{"session_id":"payload-session","extra":10}`},
}

func suppliedReplyFixture(t *testing.T, caps []string, tweak func(*V2SessionConfig)) (*V2SessionManager, chan protocol.RoutingEnvelope, *v2Recorder, *noise.CipherState, *noise.CipherState, func()) {
	t.Helper()
	priv, pub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	frames := make(chan protocol.RoutingEnvelope, 16)
	rec := &v2Recorder{}
	cfg := threadConfig(t, frames, rec, priv)
	tweak(&cfg)
	sess, _ := driveToOpenCaps(t, cfg, frames, rec, pub, initPriv, v2TestToken, caps)
	t.Cleanup(sess.stop)
	return sess.mgr, frames, rec, sess.initSend, sess.initRecv, sess.stop
}

func suppliedAsk(t *testing.T, frames chan protocol.RoutingEnvelope, send *noise.CipherState, typ string, id uint64) {
	t.Helper()
	frames <- sealAppFrame(t, send, protocol.Envelope{ID: id, Type: typ, TS: time.Now().UTC(), Payload: json.RawMessage(`{"conversation_id":"requested","session_id":"requested-session","model":"sonnet"}`)})
}

func suppliedWire(t *testing.T, rec *v2Recorder, recv *noise.CipherState, seen *int) (protocol.Envelope, map[string]json.RawMessage) {
	t.Helper()
	frames := waitForEnvelopes(t, rec, *seen+1)
	raw := threadPlain(t, frames[*seen], recv)
	*seen++
	var env protocol.Envelope
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if len(raw) > protocol.MaxThreadEnvelopeBytes {
		t.Fatal("oversized envelope")
	}
	if _, ok := fields["event_id"]; ok {
		t.Fatal("ring identity")
	}
	if _, ok := fields["history_entry_id"]; ok {
		t.Fatal("history identity")
	}
	return env, fields
}

func installSuppliedReply(c *V2SessionConfig, typ string, query func() (LiveState, bool), mutate func() (LiveState, error)) {
	switch typ {
	case protocol.TypeRequestContextUsage:
		c.ContextUsageReadingFor = func(context.Context, string) (LiveState, bool) { return query() }
	case protocol.TypeMCPStatusRequest:
		c.MCPStatusReadingFor = func(context.Context, string) (LiveState, bool) { return query() }
	case protocol.TypeRequestModelList:
		c.ModelListReadingFor = func(string, bool) (LiveState, bool) { return query() }
	case protocol.TypeRequestSessionSettings:
		c.SessionSettingsReadingFor = func(string) (LiveState, bool) { return query() }
	case protocol.TypeSetSessionSettings:
		c.UpdateSettingsReading = func(string, SettingsUpdate) (LiveState, error) { return mutate() }
	}
}

func TestSuppliedLiveReplies(t *testing.T) {
	t.Parallel()
	for _, tc := range suppliedReplyCases {
		tc.body = suppliedBody(t, tc.request, tc.body)
		for _, tag := range []string{"", "null", `"producing-session"`} {
			t.Run(tc.request+tag, func(t *testing.T) {
				var calls atomic.Int64
				r := liveReading(tc.reply, "source-a", "reading", tag, 5, 7)
				r.Envelope.Payload = json.RawMessage(tc.body)
				_, frames, rec, send, recv, _ := suppliedReplyFixture(t, threadCaps, func(c *V2SessionConfig) {
					installSuppliedReply(c, tc.request, func() (LiveState, bool) { calls.Add(1); return r, true }, func() (LiveState, error) { calls.Add(1); return r, nil })
				})
				seen := 1 // hello_ack is carried in noise_resp early data
				for i := uint64(41); i < 43; i++ {
					suppliedAsk(t, frames, send, tc.request, i)
					if i == 41 {
						clear, fields := suppliedWire(t, rec, recv, &seen)
						if !clear.SessionStateCleared || string(clear.Payload) != "{}" || clear.InReplyTo != nil {
							t.Fatalf("bad clear: %#v", clear)
						}
						if _, ok := fields["in_reply_to"]; ok {
							t.Fatal("correlated generated clear")
						}
					}
					got, fields := suppliedWire(t, rec, recv, &seen)
					if got.Type != tc.reply || !bytes.Equal(got.Payload, r.Envelope.Payload) || got.InReplyTo == nil || *got.InReplyTo != i {
						t.Fatalf("changed reply: %#v", got)
					}
					if string(fields["session_id"]) != tag {
						t.Fatalf("tag=%s want=%s", fields["session_id"], tag)
					}
					if got.SessionStateCleared {
						t.Fatal("fresh reading cleared")
					}
				}
				if calls.Load() != 2 {
					t.Fatalf("calls=%d", calls.Load())
				}
			})
		}
	}
}

func TestSuppliedSettingsEnrichment(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"values", "null", "unavailable", "memory-error", "legacy-client"} {
		t.Run(mode, func(t *testing.T) {
			var effortCalls, capCalls, memoryCalls atomic.Int64
			r := liveReading(protocol.TypeSessionSettings, "source", "settings", `"origin"`, 2, 3)
			r.Envelope.Payload = json.RawMessage(suppliedReplyCases[3].body)
			caps := []string{"interactive", "thread", "multi_agent"}
			if mode == "legacy-client" {
				caps = threadCaps
			}
			_, frames, rec, send, recv, _ := suppliedReplyFixture(t, caps, func(c *V2SessionConfig) {
				c.SessionSettingsReadingFor = func(string) (LiveState, bool) { return r, true }
				c.EffectiveEffortFor = func(_ context.Context, id string) (*string, bool) {
					effortCalls.Add(1)
					if id != "source" {
						t.Errorf("effort conversation=%s", id)
					}
					if mode == "null" {
						return nil, true
					}
					return strPtr("medium"), mode != "unavailable"
				}
				c.CapabilitiesFor = func(id, model string) (AgentCapabilities, bool) {
					capCalls.Add(1)
					if id != "payload-session" || model != "sonnet" {
						t.Errorf("capabilities keys=%s %s", id, model)
					}
					return AgentCapabilities{Interrupt: true, Models: []string{"sonnet"}, EffortLevels: []string{"high"}}, true
				}
				c.MemorySearchFor = func(_ context.Context, conv, id string) (protocol.MemorySearchReport, error) {
					memoryCalls.Add(1)
					if conv != "source" || id != "payload-session" {
						t.Errorf("memory keys=%s %s", conv, id)
					}
					return protocol.MemorySearchReport{Availability: "available", Providers: []protocol.MemorySearchProvider{}}, func() error {
						if mode == "memory-error" {
							return errors.New("private")
						}
						return nil
					}()
				}
			})
			suppliedAsk(t, frames, send, protocol.TypeRequestSessionSettings, 51)
			seen := 1
			suppliedWire(t, rec, recv, &seen)
			got, _ := suppliedWire(t, rec, recv, &seen)
			var expected, actual map[string]json.RawMessage
			json.Unmarshal(r.Envelope.Payload, &expected)
			json.Unmarshal(got.Payload, &actual)
			if mode != "unavailable" {
				if mode == "null" {
					expected["effective_effort"] = json.RawMessage(`null`)
				} else {
					expected["effective_effort"] = json.RawMessage(`"medium"`)
				}
			}
			if mode != "legacy-client" {
				expected["capabilities"], _ = json.Marshal(sessionCapabilities(AgentCapabilities{Interrupt: true, Models: []string{"sonnet"}, EffortLevels: []string{"high"}}))
			}
			availability := "available"
			if mode == "memory-error" {
				availability = "unknown"
			}
			expected["memory_search"], _ = json.Marshal(protocol.MemorySearchReport{Availability: availability, Providers: []protocol.MemorySearchProvider{}})
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("payload=%s expected=%#v", got.Payload, expected)
			}
			if effortCalls.Load() != 1 || memoryCalls.Load() != 1 || (capCalls.Load() == 1) != (mode != "legacy-client") {
				t.Fatal("enrichment call counts")
			}
		})
	}
}

func TestSuppliedLiveReplyOvertaken(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{protocol.TypeContextUsage, protocol.TypeMCPStatus} {
		for _, transition := range []bool{false, true} {
			t.Run(typ+fmt.Sprint(transition), func(t *testing.T) {
				started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
				old := liveReading(typ, "source-a", "same-reading", `"old"`, 2, 3)
				m, _, rec, recv := liveFixture(t, threadCaps, func(c *V2SessionConfig) {
					query := func(context.Context, string) (LiveState, bool) { close(started); <-release; return old, true }
					if typ == protocol.TypeContextUsage {
						c.ContextUsageReadingFor = query
					} else {
						c.MCPStatusReadingFor = query
					}
				})
				go func() {
					defer close(done)
					if typ == protocol.TypeContextUsage {
						m.resolveContextUsageRequest(t.Context(), m.sessions[v2TestConnID], 77, "requested", true)
					} else {
						m.resolveMCPStatusRequest(t.Context(), m.sessions[v2TestConnID], 77, "requested", true)
					}
				}()
				<-started
				fresh := old
				fresh.Revision++
				if transition {
					fresh.SessionGeneration++
				}
				fresh.Envelope.Payload = json.RawMessage(`{"fresh":true}`)
				if err := m.PushLiveState(t.Context(), v2TestConnID, fresh); err != nil {
					t.Fatal(err)
				}
				seen := 1
				got := liveDrain(t, m, rec, recv, &seen, 3)
				if len(got) != 2 {
					t.Fatalf("fresh delivery=%#v", got)
				}
				close(release)
				<-done
				if got := liveDrain(t, m, rec, recv, &seen, 3); len(got) != 0 {
					t.Fatalf("old reading restored: %#v", got)
				}
				// Another source conversation retains its independent generation/revision.
				old.ConversationID = "source-b"
				m.pushLiveReply(t.Context(), m.sessions[v2TestConnID], 78, old)
				got = liveDrain(t, m, rec, recv, &seen, 3)
				if len(got) != 2 || got[1].InReplyTo == nil || *got[1].InReplyTo != 78 {
					t.Fatal("conversation ordering conflated")
				}
			})
		}
	}
}

func TestSuppliedLiveReplyTeardown(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{protocol.TypeRequestContextUsage, protocol.TypeMCPStatusRequest} {
		t.Run(typ, func(t *testing.T) {
			started, ended := make(chan struct{}), make(chan struct{})
			mgr, frames, rec, send, _, stop := suppliedReplyFixture(t, threadCaps, func(c *V2SessionConfig) {
				query := func(ctx context.Context, _ string) (LiveState, bool) {
					close(started)
					<-ctx.Done()
					close(ended)
					return liveReading(protocol.TypeContextUsage, "source", "", `null`, 1, 1), true
				}
				if typ == protocol.TypeRequestContextUsage {
					c.ContextUsageReadingFor = query
				} else {
					c.MCPStatusReadingFor = query
				}
			})
			suppliedAsk(t, frames, send, typ, 66)
			<-started
			frames <- protocol.RoutingEnvelope{ConnID: v2TestConnID, CloseCode: 1000}
			select {
			case <-ended:
			case <-time.After(2 * time.Second):
				t.Fatal("query survived teardown")
			}
			if conns := mgr.ActiveConns(t.Context()); len(conns) != 0 {
				t.Fatal("connection survived teardown")
			}
			stop()
			if len(rec.snapshot()) != 1 {
				t.Fatal("teardown produced reading")
			}
		})
	}
}

func TestSuppliedSettingsEnrichmentTeardown(t *testing.T) {
	t.Parallel()
	for _, enrichment := range []string{"effective_effort", "memory_search"} {
		t.Run(enrichment, func(t *testing.T) {
			started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			releaseWorker := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(releaseWorker)
			waitForCancellation := func(ctx context.Context) {
				close(started)
				<-ctx.Done()
				close(canceled)
				// Hold the worker until both outstanding asks have observed cancellation.
				<-release
			}
			contextStarted, contextCanceled := make(chan struct{}), make(chan struct{})
			mcpStarted, mcpCanceled := make(chan struct{}), make(chan struct{})
			logger, logs := bufferLogger()
			mgr, frames, rec, send, recv, stop := suppliedReplyFixture(t, threadCaps, func(c *V2SessionConfig) {
				c.Logger = logger
				c.SessionSettingsReadingFor = func(string) (LiveState, bool) {
					r := liveReading(protocol.TypeSessionSettings, "source", "settings", `"origin"`, 1, 1)
					r.Envelope.Payload = json.RawMessage(suppliedReplyCases[3].body)
					return r, true
				}
				if enrichment == "effective_effort" {
					c.EffectiveEffortFor = func(ctx context.Context, _ string) (*string, bool) {
						waitForCancellation(ctx)
						return strPtr("medium"), true
					}
				} else {
					c.MemorySearchFor = func(ctx context.Context, _, _ string) (protocol.MemorySearchReport, error) {
						waitForCancellation(ctx)
						return protocol.MemorySearchReport{}, ctx.Err()
					}
				}
				c.ContextUsageReadingFor = func(ctx context.Context, _ string) (LiveState, bool) {
					close(contextStarted)
					<-ctx.Done()
					close(contextCanceled)
					return liveReading(protocol.TypeContextUsage, "source", "", `null`, 1, 1), true
				}
				c.MCPStatusReadingFor = func(ctx context.Context, _ string) (LiveState, bool) {
					close(mcpStarted)
					<-ctx.Done()
					close(mcpCanceled)
					return liveReading(protocol.TypeMCPStatus, "source", "", `null`, 1, 1), true
				}
			})
			await := func(ch <-chan struct{}, message string) {
				t.Helper()
				select {
				case <-ch:
				case <-time.After(2 * time.Second):
					t.Fatal(message)
				}
			}
			// A decrypted correlated reply first proves the authenticated connection works.
			suppliedAsk(t, frames, send, protocol.TypeRequestModelList, 61)
			seen := 1
			ack, _ := suppliedWire(t, rec, recv, &seen)
			if ack.Type != protocol.TypeError || ack.InReplyTo == nil || *ack.InReplyTo != 61 {
				t.Fatalf("unexpected authenticated reply: %#v", ack)
			}
			suppliedAsk(t, frames, send, protocol.TypeRequestContextUsage, 62)
			await(contextStarted, "context usage query did not start")
			suppliedAsk(t, frames, send, protocol.TypeMCPStatusRequest, 63)
			await(mcpStarted, "MCP status query did not start")
			suppliedAsk(t, frames, send, protocol.TypeRequestSessionSettings, 64)
			await(started, "settings enrichment did not start")
			frames <- protocol.RoutingEnvelope{ConnID: v2TestConnID, CloseCode: 1000}
			await(canceled, "settings enrichment survived requester teardown")
			await(contextCanceled, "context usage query survived requester teardown")
			await(mcpCanceled, "MCP status query survived requester teardown")
			if conns := mgr.ActiveConns(t.Context()); len(conns) != 0 {
				t.Fatal("connection survived teardown")
			}
			releaseWorker()
			waitForLogContains(t, logs, "v2.live_reply.dropped")
			stop()
			if len(rec.snapshot()) != seen {
				t.Fatal("teardown produced a reading or clear")
			}
		})
	}
}

func TestSuppliedLiveReplyCompatibility(t *testing.T) {
	t.Parallel()
	for _, tc := range suppliedReplyCases {
		tc.body = suppliedBody(t, tc.request, tc.body)
		for _, mode := range []string{"absent", "non-thread", "dual"} {
			t.Run(tc.request+mode, func(t *testing.T) {
				var legacy, optional atomic.Int64
				updater := &fakeSettingsUpdater{}
				var expected []byte
				caps := threadCaps
				if mode == "non-thread" {
					caps = []string{"interactive"}
				}
				_, frames, rec, send, recv, _ := suppliedReplyFixture(t, caps, func(c *V2SessionConfig) {
					switch tc.request {
					case protocol.TypeRequestContextUsage:
						c.ContextUsageFor = func(context.Context, string) (protocol.ContextUsagePayload, bool) {
							legacy.Add(1)
							return ctxUsageFixture, true
						}
						expected, _ = json.Marshal(ctxUsageFixture)
					case protocol.TypeMCPStatusRequest:
						c.MCPStatusFor = func(context.Context, string) (protocol.MCPStatusPayload, bool) {
							legacy.Add(1)
							return mcpStatusFixture, true
						}
						expected, _ = json.Marshal(mcpStatusFixture)
					case protocol.TypeRequestModelList:
						c.ModelListFor = func(string, bool) (protocol.ModelListPayload, bool) { legacy.Add(1); return fixtureModelList, true }
						expected, _ = json.Marshal(fixtureModelList)
					case protocol.TypeRequestSessionSettings:
						c.RunConfigFor = func(string) (RunConfig, bool) {
							legacy.Add(1)
							return RunConfig{SessionID: "legacy", Model: "sonnet", Effort: "high", YOLO: true, PermissionMode: "bypassPermissions", UsedTokens: 37, WindowTokens: 100}, true
						}
						expected, _ = json.Marshal(protocol.SessionSettingsPayload{SessionID: "legacy", Model: "sonnet", Effort: "high", YOLO: true, PermissionMode: "bypassPermissions", UsedTokens: 37, WindowTokens: 100})
					case protocol.TypeSetSessionSettings:
						c.SettingsUpdater = updater
						expected, _ = json.Marshal(protocol.SessionSettingsUpdatedPayload{SessionID: "requested-session"})
					}
					if mode != "absent" {
						r := liveReading(tc.reply, "source", "", `"origin"`, 1, 1)
						r.Envelope.Payload = json.RawMessage(tc.body)
						installSuppliedReply(c, tc.request, func() (LiveState, bool) { optional.Add(1); return r, true }, func() (LiveState, error) { optional.Add(1); return r, nil })
					}
				})
				suppliedAsk(t, frames, send, tc.request, 19)
				seen := 1
				if mode == "dual" {
					suppliedWire(t, rec, recv, &seen)
					expected = []byte(tc.body)
				}
				got, fields := suppliedWire(t, rec, recv, &seen)
				if got.Type != tc.reply || !bytes.Equal(got.Payload, expected) || got.InReplyTo == nil || *got.InReplyTo != 19 {
					t.Fatalf("legacy payload changed: %#v expected=%s", got, expected)
				}
				if mode != "dual" {
					if _, ok := fields["session_id"]; ok {
						t.Fatal("legacy source metadata")
					}
				}
				count := legacy.Load() + int64(len(updater.snapshot()))
				if mode == "dual" {
					if count != 0 || optional.Load() != 1 {
						t.Fatal("duplicate provider operation")
					}
				} else if count != 1 || optional.Load() != 0 {
					t.Fatal("fallback operation counts")
				}
			})
		}
	}
}

func TestSuppliedLiveReplyRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range suppliedReplyCases {
		tc.body = suppliedBody(t, tc.request, tc.body)
		t.Run(tc.request, func(t *testing.T) {
			var calls atomic.Int64
			_, frames, rec, send, recv, _ := suppliedReplyFixture(t, threadCaps, func(c *V2SessionConfig) {
				installSuppliedReply(c, tc.request, func() (LiveState, bool) {
					calls.Add(1)
					return liveReading(tc.reply, "poison", "", `"poison"`, 1, 1), false
				}, func() (LiveState, error) {
					calls.Add(1)
					return liveReading(tc.reply, "poison", "", `"poison"`, 1, 1), ErrSessionUnknown
				})
				c.RunConfigFor = func(string) (RunConfig, bool) { t.Error("legacy retried"); return RunConfig{}, false }
			})
			suppliedAsk(t, frames, send, tc.request, 71)
			seen := 1
			got, _ := suppliedWire(t, rec, recv, &seen)
			if got.InReplyTo == nil || *got.InReplyTo != 71 || got.SessionStateCleared || len(got.SessionID) != 0 {
				t.Fatalf("refusal=%#v", got)
			}
			if tc.request == protocol.TypeRequestSessionSettings {
				expected, _ := json.Marshal(protocol.SessionSettingsPayload{})
				if got.Type != protocol.TypeSessionSettings || !bytes.Equal(got.Payload, expected) {
					t.Fatal("no-session response changed")
				}
			} else {
				var p protocol.ErrorPayload
				json.Unmarshal(got.Payload, &p)
				code := map[string]string{protocol.TypeRequestContextUsage: protocol.CodeContextUsageUnavailable, protocol.TypeMCPStatusRequest: protocol.CodeMCPStatusUnavailable, protocol.TypeRequestModelList: protocol.CodeModelListUnavailable, protocol.TypeSetSessionSettings: protocol.CodeSessionNotFound}[tc.request]
				if got.Type != protocol.TypeError || p.Code != code {
					t.Fatalf("refusal mapping=%#v", got)
				}
			}
			if calls.Load() != 1 {
				t.Fatal("provider not called once")
			}
		})
	}
}

func TestSuppliedSettingsUpdateErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err   error
		code  string
		retry bool
	}{{ErrModelNotOffered, protocol.CodeProtocolMalformed, false}, {ErrEffortNotOffered, protocol.CodeProtocolMalformed, false}, {ErrModelVocabularyUnavailable, protocol.CodeModelListUnavailable, true}, {errors.New("disk"), protocol.CodeServerBinaryOffline, true}} {
		t.Run(tc.err.Error(), func(t *testing.T) {
			_, frames, rec, send, recv, _ := suppliedReplyFixture(t, threadCaps, func(c *V2SessionConfig) {
				c.UpdateSettingsReading = func(id string, u SettingsUpdate) (LiveState, error) {
					if id != "requested-session" || u.Model == nil || *u.Model != "sonnet" {
						t.Error("mutation input changed")
					}
					return LiveState{}, tc.err
				}
			})
			suppliedAsk(t, frames, send, protocol.TypeSetSessionSettings, 75)
			seen := 1
			got, _ := suppliedWire(t, rec, recv, &seen)
			var p protocol.ErrorPayload
			json.Unmarshal(got.Payload, &p)
			if got.Type != protocol.TypeError || p.Code != tc.code || p.Retryable != tc.retry || got.InReplyTo == nil || *got.InReplyTo != 75 {
				t.Fatalf("error=%#v", got)
			}
		})
	}
}

func suppliedBody(t *testing.T, request, fallback string) string {
	t.Helper()
	var value any
	switch request {
	case protocol.TypeRequestContextUsage:
		value = ctxUsageFixture
	case protocol.TypeMCPStatusRequest:
		value = mcpStatusFixture
	case protocol.TypeRequestModelList:
		value = fixtureModelList
	default:
		return fallback
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSuppliedLiveReplyWithheld(t *testing.T) {
	t.Parallel()
	for _, tc := range suppliedReplyCases {
		t.Run(tc.request, func(t *testing.T) {
			logger, logs := bufferLogger()
			mgr, frames, rec, send, recv, _ := suppliedReplyFixture(t, threadCaps, func(c *V2SessionConfig) {
				c.Logger = logger
				r := liveReading(tc.reply, gateCodexConv, "", `"codex-origin"`, 1, 1)
				// Empty supplied clears must still use source-conversation access gates.
				r.Envelope.SessionStateCleared = true
				r.Envelope.Payload = json.RawMessage(`{}`)
				installSuppliedReply(c, tc.request, func() (LiveState, bool) { return r, true }, func() (LiveState, error) { return r, nil })
			})
			suppliedAsk(t, frames, send, tc.request, 80)
			waitForLogContains(t, logs, "v2.live_reply.queued")
			// A permitted queued reply proves Run consumed the withheld predecessor.
			barrier := liveReading(protocol.TypeContextUsage, gateClaudeConv, "", `"claude-origin"`, 1, 1)
			if err := mgr.PushLiveState(t.Context(), v2TestConnID, barrier); err != nil {
				t.Fatal(err)
			}
			seen := 1
			clear, _ := suppliedWire(t, rec, recv, &seen)
			fresh, _ := suppliedWire(t, rec, recv, &seen)
			if clear.Type != protocol.TypeContextUsage || !clear.SessionStateCleared || string(clear.SessionID) != `"claude-origin"` || !bytes.Equal(fresh.Payload, barrier.Envelope.Payload) {
				t.Fatal("withheld source reached wire")
			}
			if len(rec.snapshot()) != 3 {
				t.Fatal("withheld source emitted an extra frame")
			}
		})
	}
}

func TestSuppliedModelProjection(t *testing.T) {
	t.Parallel()
	for _, multi := range []bool{false, true} {
		t.Run(fmt.Sprint(multi), func(t *testing.T) {
			caps := threadCaps
			if multi {
				caps = []string{"interactive", "thread", "multi_agent"}
			}
			_, frames, rec, send, recv, _ := suppliedReplyFixture(t, caps, func(c *V2SessionConfig) {
				c.ModelListReadingFor = func(conv string, got bool) (LiveState, bool) {
					if conv != "requested" || got != multi {
						t.Error("inventory projection changed")
					}
					r := liveReading(protocol.TypeModelList, "source", "", `null`, 1, 1)
					r.Envelope.Payload = json.RawMessage(suppliedBody(t, protocol.TypeRequestModelList, ""))
					return r, true
				}
			})
			suppliedAsk(t, frames, send, protocol.TypeRequestModelList, 88)
			seen := 1
			suppliedWire(t, rec, recv, &seen)
			got, _ := suppliedWire(t, rec, recv, &seen)
			if !bytes.Equal(got.Payload, []byte(suppliedBody(t, protocol.TypeRequestModelList, ""))) {
				t.Fatal("inventory payload changed")
			}
		})
	}
}
