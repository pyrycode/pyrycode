package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/eventring"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

var threadCaps = []string{protocol.CapabilityInteractive, protocol.CapabilityThread}

func threadConfig(t *testing.T, frames chan protocol.RoutingEnvelope, rec *v2Recorder, priv []byte) V2SessionConfig {
	t.Helper()
	return V2SessionConfig{Frames: frames, Outbound: rec.outbound, StaticPriv: priv,
		Devices: v2PairedRegistry(t, v2TestToken), ServerID: v2TestServerID, Logger: silentLogger(),
		ThreadReady: func() bool { return true },
		ThreadLastShownVersion: func(id string) (uint64, bool) {
			switch id {
			case "zero":
				return 0, true
			case "other":
				return 37, true
			}
			return 0, false
		}, CodexConversation: gateCodexSeam}
}

func threadPlain(t *testing.T, env protocol.RoutingEnvelope, recv *noise.CipherState) []byte {
	t.Helper()
	raw, err := recv.Decrypt(decodeNoiseMsg(t, env))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestV2Session_ThreadNegotiation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		ready func() bool
		caps  []string
		want  bool
	}{
		{"absent", nil, threadCaps, false}, {"false", func() bool { return false }, threadCaps, false},
		{"unadvertised", func() bool { return true }, []string{"thread_fake"}, false},
		{"duplicates", func() bool { return true }, []string{"thread", "thread", "thread_fake"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			priv, pub := genV2Keypair(t)
			initPriv, _ := genV2Keypair(t)
			frames := make(chan protocol.RoutingEnvelope, 4)
			rec := &v2Recorder{}
			cfg := threadConfig(t, frames, rec, priv)
			cfg.ThreadReady = tc.ready
			sess, ack := driveToOpenCaps(t, cfg, frames, rec, pub, initPriv, v2TestToken, tc.caps)
			t.Cleanup(sess.stop)
			var env protocol.Envelope
			var p protocol.HelloAckPayload
			if err := json.Unmarshal(ack, &env); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if slices.Contains(p.Capabilities, "thread") != tc.want || slices.Contains(p.Capabilities, "thread_fake") {
				t.Fatalf("ack capabilities %v", p.Capabilities)
			}
			if tc.want && len(p.Capabilities) != 1 {
				t.Fatalf("duplicates: %v", p.Capabilities)
			}
			check := func() {
				conns := sess.mgr.ActiveConns(t.Context())
				if len(conns) != 1 || conns[0].Thread != tc.want || conns[0].Interactive != slices.Contains(tc.caps, "interactive") {
					t.Fatalf("snapshot: %#v", conns)
				}
			}
			check()
			initiator, err := noise.NewInitiator(initPriv, pub)
			if err != nil {
				t.Fatal(err)
			}
			msg, err := initiator.WriteInit(nil)
			if err != nil {
				t.Fatal(err)
			}
			frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, msg)
			envs := waitForEnvelopes(t, rec, 2)
			_, _, recv, err := initiator.ReadResp(decodeRespFrame(t, envs[1]))
			if err != nil {
				t.Fatal(err)
			}
			check()
			if err := sess.mgr.Push(t.Context(), v2TestConnID, protocol.Envelope{ID: 9, Type: protocol.TypeWorkspaceUpdated, Payload: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
			threadPlain(t, waitForEnvelopes(t, rec, 3)[2], recv)
		})
	}
}

func TestV2Session_ThreadSummaries(t *testing.T) {
	t.Parallel()
	const supplied = `{"id":"zero","name":null,"current_session_id":"stored","read_up_to":9,"latest_entry_id":83,"extra":false,"last_shown_version":999}`
	const legacy = `{"id":"zero","name":null,"current_session_id":"stored","read_up_to":9,"latest_entry_id":83,"extra":false}`
	const rows = `{"conversations":[` + supplied + `,{"id":"other","read_up_to":4},{"id":"unavailable","last_shown_version":999}],"extra":null}`
	for _, caps := range [][]string{{"interactive"}, threadCaps, {"interactive", "thread", "multi_agent"}} {
		t.Run(strings.Join(caps, "-"), func(t *testing.T) {
			priv, pub := genV2Keypair(t)
			initPriv, _ := genV2Keypair(t)
			frames := make(chan protocol.RoutingEnvelope, 8)
			rec := &v2Recorder{}
			cfg := threadConfig(t, frames, rec, priv)
			cfg.ConversationAgent = func(string) (string, bool) { return protocol.AgentClaude, true }
			cfg.Handlers = map[string]dispatch.Handler{protocol.TypeListConversations: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
				return c.Reply(ctx, env, protocol.TypeConversations, json.RawMessage(rows))
			}, protocol.TypeRenameConversation: func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error {
				return c.Reply(ctx, env, protocol.TypeConversationUpdated, env.Payload)
			}}
			sess, _ := driveToOpenCaps(t, cfg, frames, rec, pub, initPriv, v2TestToken, caps)
			t.Cleanup(sess.stop)
			isThread := slices.Contains(caps, "thread")
			assertRow := func(raw json.RawMessage, id string) {
				var row map[string]json.RawMessage
				if err := json.Unmarshal(raw, &row); err != nil {
					t.Fatal(err)
				}
				want, usable := cfg.ThreadLastShownVersion(id)
				got, exists := row["last_shown_version"]
				if exists != (isThread && usable) {
					t.Fatalf("%s reading presence: %s", id, raw)
				}
				if exists {
					var n uint64
					if err := json.Unmarshal(got, &n); err != nil || n != want {
						t.Fatalf("%s reading %s want %d", id, got, want)
					}
				}
				delete(row, "last_shown_version")
				delete(row, "agent")
				var expected map[string]json.RawMessage
				base := legacy
				if id == "other" {
					base = `{"id":"other","read_up_to":4}`
				} else if id == "unavailable" {
					base = `{"id":"unavailable"}`
				}
				if err := json.Unmarshal([]byte(base), &expected); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(row, expected) {
					t.Fatalf("row fields changed: %s", raw)
				}
			}
			pushed := protocol.Envelope{ID: 7, Type: protocol.TypeConversationUpdated, TS: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), Payload: json.RawMessage(supplied), SessionID: json.RawMessage(`"session"`)}
			if err := sess.mgr.Push(t.Context(), v2TestConnID, pushed); err != nil {
				t.Fatal(err)
			}
			raw := threadPlain(t, waitForEnvelopes(t, rec, 2)[1], sess.initRecv)
			if !isThread && string(raw) != `{"id":7,"type":"conversation_updated","ts":"2026-10-10T00:00:00Z","payload":`+legacy+`}` {
				t.Fatalf("legacy bytes: %s", raw)
			}
			var got protocol.Envelope
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			assertRow(got.Payload, "zero")
			if string(pushed.Payload) != supplied {
				t.Fatal("shared payload mutated")
			}
			for i, ask := range []protocol.Envelope{
				{ID: 10, Type: protocol.TypeListConversations, Payload: json.RawMessage(`{}`)},
				{ID: 11, Type: protocol.TypeRenameConversation, Payload: json.RawMessage(supplied)},
				{ID: 12, Type: protocol.TypeRenameConversation, Payload: json.RawMessage(`{"id":"unavailable","last_shown_version":999}`)},
			} {
				frames <- sealAppFrame(t, sess.initSend, ask)
				raw := threadPlain(t, waitForEnvelopes(t, rec, i+3)[i+2], sess.initRecv)
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatal(err)
				}
				if got.InReplyTo == nil || *got.InReplyTo != ask.ID {
					t.Fatalf("reply correlation: %s", raw)
				}
				if i == 0 {
					if !isThread && string(got.Payload) != `{"conversations":[`+legacy+`,{"id":"other","read_up_to":4},{"id":"unavailable"}],"extra":null}` {
						t.Fatalf("legacy list bytes: %s", got.Payload)
					}
					var p struct {
						Conversations []json.RawMessage `json:"conversations"`
					}
					if err := json.Unmarshal(got.Payload, &p); err != nil {
						t.Fatal(err)
					}
					for j, id := range []string{"zero", "other", "unavailable"} {
						assertRow(p.Conversations[j], id)
					}
				} else if i == 1 {
					assertRow(got.Payload, "zero")
					if !isThread && string(got.Payload) != legacy {
						t.Fatalf("legacy reply bytes %s", got.Payload)
					}
				} else {
					assertRow(got.Payload, "unavailable")
				}
			}
		})
	}
}

func TestV2Session_ThreadDelivery(t *testing.T) {
	t.Parallel()
	replaced := []string{"assistant_delta", "tool_use", "tool_result", "tool_denied", "turn_end", "session_transition", "message", "background_task_started", "background_task_updated", "background_task_roster", "queue_state", "banner", "compaction_boundary", "model_refusal_fallback", "model_refusal_no_fallback", "unrecognized_message"}
	progress := []string{"turn_state", "tool_progress", "background_task_progress", "thinking_progress", "api_retry", "compacting"}
	for _, caps := range [][]string{{"interactive"}, {"thread"}, threadCaps, {"interactive", "thread", "multi_agent"}} {
		t.Run(strings.Join(caps, "-"), func(t *testing.T) {
			priv, pub := genV2Keypair(t)
			initPriv, _ := genV2Keypair(t)
			frames := make(chan protocol.RoutingEnvelope, 4)
			rec := &v2Recorder{}
			cfg := threadConfig(t, frames, rec, priv)
			cfg.Handlers = map[string]dispatch.Handler{protocol.TypeListConversations: func(ctx context.Context, c *dispatch.Conn, ask protocol.Envelope) error {
				for _, typ := range []string{protocol.TypeThreadItemAdded, protocol.TypeThreadItemChanged, protocol.TypeThreadTextAppend} {
					if err := c.Reply(ctx, ask, typ, json.RawMessage(`{"conversation_id":"conv-codex","continuation":{"index":0,"final":true},"data":"secret"}`)); err != nil {
						return err
					}
				}
				return c.Reply(ctx, ask, protocol.TypeConversations, json.RawMessage(`{"conversations":[]}`))
			}}
			sess, _ := driveToOpenCaps(t, cfg, frames, rec, pub, initPriv, v2TestToken, caps)
			t.Cleanup(sess.stop)
			isThread := slices.Contains(caps, "thread")
			interactive := slices.Contains(caps, "interactive")
			multi := slices.Contains(caps, "multi_agent")
			var sent, want []protocol.Envelope
			add := func(typ, payload string, reply bool, clear bool) {
				env := protocol.Envelope{ID: uint64(len(sent) + 1), Type: typ, Payload: json.RawMessage(payload), SessionID: json.RawMessage(`null`), SessionStateCleared: clear}
				if reply {
					n := uint64(99)
					env.InReplyTo = &n
				}
				sent = append(sent, env)
			}
			for _, typ := range replaced {
				add(typ, `{"conversation_id":"conv-claude","text":"content"}`, false, false)
			}
			for _, typ := range progress {
				add(typ, `{"conversation_id":"conv-claude"}`, false, false)
			}
			add("assistant_delta", `{"conversation_id":"conv-claude","text":"reply"}`, true, false)
			add("tool_progress", `{}`, false, true)
			for _, typ := range []string{"thread_item_added", "thread_item_changed", "thread_text_append"} {
				for _, id := range []string{"conv-claude", "conv-codex", ""} {
					p, _ := json.Marshal(map[string]string{"conversation_id": id, "text": "item"})
					add(typ, string(p), true, false)
				}
			}
			add("workspace_updated", `{}`, false, false)
			for _, env := range sent {
				item := strings.HasPrefix(env.Type, "thread_")
				deliver := true
				if item {
					deliver = isThread && interactive && pushedConversationID(env) != "" && (multi || pushedConversationID(env) != "conv-codex")
				} else if isThread && env.InReplyTo == nil && slices.Contains(replaced, env.Type) {
					deliver = false
				} else if !isThread && env.SessionStateCleared {
					deliver = false
				}
				if deliver {
					if !isThread {
						env.SessionID = nil
						env.SessionStateCleared = false
					}
					want = append(want, env)
				}
			}
			for _, env := range sent {
				if err := sess.mgr.Push(t.Context(), v2TestConnID, env); err != nil {
					t.Fatal(err)
				}
			}
			got := gateFramesUntil(t, rec, v2TestConnID, sess.initRecv, "workspace_updated")
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("delivery frame mismatch: got %d frames, want %d; got types %v", len(got), len(want), func() []string {
					var types []string
					for _, e := range got {
						types = append(types, e.Type)
					}
					return types
				}())
			}
			before := len(rec.snapshot())
			frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{ID: 100, Type: protocol.TypeListConversations, Payload: json.RawMessage(`{}`)})
			n := 1
			if isThread && interactive && multi {
				n = 4
			}
			envs := waitForEnvelopes(t, rec, before+n)
			if n == 4 {
				for i, typ := range []string{"thread_item_added", "thread_item_changed", "thread_text_append"} {
					item := decryptAppFrame(t, envs[before+i], sess.initRecv)
					if item.Type != typ {
						t.Fatal(item.Type)
					}
				}
			}
			reply := decryptAppFrame(t, envs[before+n-1], sess.initRecv)
			if reply.Type != "conversations" {
				t.Fatal(reply.Type)
			}
		})
	}
}

func TestV2Session_ThreadReplay(t *testing.T) {
	t.Parallel()
	priv, pub := genV2Keypair(t)
	frames := make(chan protocol.RoutingEnvelope, 2)
	rec := &v2Recorder{}
	cfg := threadConfig(t, frames, rec, priv)
	cfg.RetainedBackgroundTaskRosters = func() []protocol.BackgroundTaskRosterPayload {
		return []protocol.BackgroundTaskRosterPayload{{ConversationID: gateClaudeConv}}
	}
	mgr, stop := startManager(t, cfg)
	t.Cleanup(stop)
	ring := eventring.New(eventring.MaxEventsPerConversation)
	ts := time.Now().UTC()
	ring.Append(gateClaudeConv, "assistant_delta", json.RawMessage(`{"conversation_id":"conv-claude"}`), ts)
	suppressed := ring.Append(gateClaudeConv, "tool_use", json.RawMessage(`{"conversation_id":"conv-claude"}`), ts)
	ring.Append(gateClaudeConv, "tool_progress", json.RawMessage(`{"conversation_id":"conv-claude","elapsed":5}`), ts)
	mgr.SetReplaySource(ring, func() string { return gateClaudeConv })
	last := uint64(1)
	recv := openGateConn(t, frames, rec, v2TestConnID, pub, v2TestInstallPriv, threadCaps, &last)
	live := protocol.Envelope{ID: 9, Type: "workspace_updated", Payload: json.RawMessage(`{}`)}
	if err := mgr.Push(t.Context(), v2TestConnID, protocol.Envelope{ID: 8, Type: "tool_use", EventID: &suppressed, Payload: json.RawMessage(`{"conversation_id":"conv-claude"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Push(t.Context(), v2TestConnID, live); err != nil {
		t.Fatal(err)
	}
	got := gateFramesUntil(t, rec, v2TestConnID, recv, "workspace_updated")
	if len(got) != 2 || got[0].Type != "tool_progress" {
		t.Fatalf("replay: %#v", got)
	}
	stop()
	if mgr.sessions[v2TestConnID].replayThrough != 3 || len(mgr.sessions[v2TestConnID].replayQueue) != 0 {
		t.Fatal("replay failed to advance/drain")
	}
}

func TestV2Session_ThreadContinuations(t *testing.T) {
	t.Parallel()
	for _, count := range []int{2900, 9000} {
		text := strings.Repeat("\x00\"\\<&", count)
		content, err := json.Marshal(map[string]string{"text": text})
		if err != nil {
			t.Fatal(err)
		}
		updates := []any{
			protocol.ThreadItemAddedPayload{ConversationID: gateClaudeConv, Epoch: "epoch", Version: 8, Item: protocol.ThreadItem{ID: 2, Rev: 8, Kind: "assistant_message", Content: content}},
			protocol.ThreadItemChangedPayload{ConversationID: gateClaudeConv, Epoch: "epoch", Version: 8, ItemID: 2, BaseRev: 7, Rev: 8, Changes: map[string]json.RawMessage{"content": content}},
			protocol.ThreadTextAppendPayload{ConversationID: gateClaudeConv, Epoch: "epoch", Version: 8, ItemID: 2, BaseRev: 7, Rev: 8, Text: text},
		}
		for _, update := range updates {
			t.Run(reflect.TypeOf(update).Name()+"-"+strconv.Itoa(count), func(t *testing.T) {
				priv, pub := genV2Keypair(t)
				initPriv, _ := genV2Keypair(t)
				frames := make(chan protocol.RoutingEnvelope, 2)
				rec := &v2Recorder{}
				sess, _ := driveToOpenCaps(t, threadConfig(t, frames, rec, priv), frames, rec, pub, initPriv, v2TestToken, threadCaps)
				t.Cleanup(sess.stop)
				base := protocol.Envelope{ID: 1, SessionID: json.RawMessage(`"fixed-session"`)}
				parts, err := protocol.EncodeThreadUpdate(base, update)
				if err != nil {
					t.Fatal(err)
				}
				if count == 2900 && len(parts) != 1 {
					t.Fatal("expected near-cap ordinary envelope")
				}
				if count == 9000 && len(parts) < 2 {
					t.Fatal("expected continuations")
				}
				for _, raw := range parts {
					var env protocol.Envelope
					if err := json.Unmarshal(raw, &env); err != nil {
						t.Fatal(err)
					}
					n := ^uint64(0)
					env.ID = n
					env.InReplyTo = &n
					env.EventID = &n
					env.HistoryEntryID = &n
					env.TS = time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.FixedZone("", -86340))
					env.SessionStateCleared = true
					env.PayloadEncrypted = true
					if err := sess.mgr.Push(t.Context(), v2TestConnID, env); err != nil {
						t.Fatal(err)
					}
				}
				envs := waitForEnvelopes(t, rec, len(parts)+1)
				var logical []byte
				for i := range parts {
					raw := threadPlain(t, envs[i+1], sess.initRecv)
					if len(raw) > protocol.MaxThreadEnvelopeBytes {
						t.Fatal("envelope exceeds cap")
					}
					var env protocol.Envelope
					var part protocol.ThreadUpdatePart
					if err := json.Unmarshal(raw, &env); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(env.Payload, &part); err != nil {
						t.Fatal(err)
					}
					if string(env.SessionID) != string(base.SessionID) {
						t.Fatal("session identity changed")
					}
					if part.Continuation == nil {
						logical = append(logical, env.Payload...)
					} else {
						if part.Continuation.Index != uint64(i) || part.Continuation.Offset != uint64(len(logical)) || part.Continuation.Final != (i == len(parts)-1) {
							t.Fatal("part reordered")
						}
						logical = append(logical, part.Data...)
					}
				}
				want, err := json.Marshal(update)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(logical, want) {
					t.Fatal("logical payload changed")
				}
			})
		}
	}
}

func TestV2Session_ThreadAuthenticationFailure(t *testing.T) {
	t.Parallel()
	priv, pub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	frames := make(chan protocol.RoutingEnvelope, 2)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, threadConfig(t, frames, rec, priv))
	t.Cleanup(stop)
	item := protocol.Envelope{Type: protocol.TypeThreadTextAppend, Payload: json.RawMessage(`{"conversation_id":"conv-claude","text":"secret"}`)}
	if err := mgr.Push(t.Context(), v2TestConnID, item); err != ErrConnNotFound {
		t.Fatalf("before admission: %v", err)
	}
	initiator, err := noise.NewInitiator(initPriv, pub)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := initiator.WriteInit(buildHelloEarlyDataCaps(t, "invalid-token", threadCaps))
	if err != nil {
		t.Fatal(err)
	}
	frames <- wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, msg)
	envs := waitForEnvelopes(t, rec, 2)
	_, _, recv, err := initiator.ReadResp(decodeRespFrame(t, envs[0]))
	if err != nil {
		t.Fatal(err)
	}
	failure := decryptAppFrame(t, envs[1], recv)
	if failure.Type != protocol.TypeError || envs[1].CloseCode != uint16(StatusUnauthorized) {
		t.Fatalf("unauthorized wire: %#v", failure)
	}
	if conns := mgr.ActiveConns(t.Context()); len(conns) != 0 {
		t.Fatalf("unauthenticated snapshot: %#v", conns)
	}
	if err := mgr.Push(t.Context(), v2TestConnID, item); err != ErrConnNotFound {
		t.Fatalf("after rejection: %v", err)
	}
}
