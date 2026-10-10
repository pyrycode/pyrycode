package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnbridge"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

var liveFamilies = []string{"modal_shown", "modal_dismissed", "question_shown", "question_dismissed", "turn_state", "stall", "api_retry", "compacting", "thinking_progress", "tool_progress", "background_task_progress", "resetting", "rate_limited", "context_usage", "model_announced", "session_facts", "session_settings", "session_settings_updated", "mcp_status", "slash_command_list", "model_list", "reply_suggestion", "session_error"}

func liveReading(typ, conv, key, tag string, generation, revision uint64) LiveState {
	return LiveState{Envelope: protocol.Envelope{ID: revision, Type: typ, TS: time.Unix(1, 0).UTC(), Payload: json.RawMessage(`{"conversation_id":"` + conv + `","session_id":"payload-session","active":false,"revision":` + fmt.Sprint(revision) + `}`), SessionID: json.RawMessage(tag)}, ConversationID: conv, SessionGeneration: generation, ReadingID: key, Revision: revision}
}

// Direct authenticated admission gives the test deterministic Run-pass control.
func liveFixture(t *testing.T, caps []string, tweak func(*V2SessionConfig)) (*V2SessionManager, *V2Session, *v2Recorder, *noise.CipherState) {
	t.Helper()
	priv, pub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	rec := &v2Recorder{}
	cfg := threadConfig(t, make(chan protocol.RoutingEnvelope), rec, priv)
	cfg.KnownConversation = func(id string) bool { return id != "unknown" }
	if tweak != nil {
		tweak(&cfg)
	}
	mgr, err := NewV2SessionManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	init, err := noise.NewInitiator(initPriv, pub)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := init.WriteInit(buildHelloEarlyDataCaps(t, v2TestToken, caps))
	if err != nil {
		t.Fatal(err)
	}
	mgr.handleFrame(t.Context(), wrapInnerFrame(t, v2TestConnID, protocol.TypeNoiseInit, msg))
	_, _, recv, err := init.ReadResp(decodeRespFrame(t, rec.snapshot()[0]))
	if err != nil {
		t.Fatal(err)
	}
	s := mgr.sessions[v2TestConnID]
	t.Cleanup(func() { mgr.teardown(s) })
	return mgr, s, rec, recv
}

func liveDrain(t *testing.T, mgr *V2SessionManager, rec *v2Recorder, recv *noise.CipherState, seen *int, passes int) []protocol.Envelope {
	t.Helper()
	for i := 0; i < passes; i++ {
		before := len(rec.snapshot())
		mgr.drainOnce(t.Context())
		if len(rec.snapshot()) > before+1 {
			t.Fatal("more than one send in a Run pass")
		}
	}
	var out []protocol.Envelope
	for _, frame := range rec.snapshot()[*seen:] {
		raw := threadPlain(t, frame, recv)
		if len(raw) > protocol.MaxThreadEnvelopeBytes {
			t.Fatal("oversized plaintext")
		}
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		if mgr.sessions[v2TestConnID].thread && (env.EventID != nil || env.HistoryEntryID != nil) {
			t.Fatal("live state has replay/history identity")
		}
		out = append(out, env)
	}
	*seen = len(rec.snapshot())
	return out
}

func TestLiveStateWire(t *testing.T) {
	t.Parallel()
	for _, caps := range [][]string{threadCaps, {"interactive"}} {
		for _, tag := range []string{"", "null", `"source-session"`} {
			t.Run(strings.Join(caps, "-")+tag, func(t *testing.T) {
				m, _, rec, recv := liveFixture(t, caps, nil)
				seen := 1
				for familyIndex, typ := range liveFamilies {
					for _, reply := range []bool{false, true} {
						r := liveReading(typ, gateClaudeConv, "", tag, 1, uint64(familyIndex*2+1))
						if reply {
							n := uint64(77)
							r.Envelope.InReplyTo = &n
							if len(caps) == 1 {
								r.Revision++
								r.Envelope.Payload = replaceObjectField(r.Envelope.Payload, "revision", json.RawMessage(fmt.Sprint(r.Revision)))
							}
						}
						event := uint64(555)
						r.Envelope.EventID = &event
						r.Envelope.HistoryEntryID = &event
						if err := m.PushLiveState(t.Context(), v2TestConnID, r); err != nil {
							t.Fatal(err)
						}
						got := liveDrain(t, m, rec, recv, &seen, 3)
						if len(got) == 0 {
							t.Fatalf("missing %s", typ)
						}
						fresh := got[len(got)-1]
						if fresh.Type != typ || !bytes.Equal(fresh.Payload, r.Envelope.Payload) || !reflect.DeepEqual(fresh.InReplyTo, r.Envelope.InReplyTo) {
							t.Fatalf("payload/correlation changed: %s", typ)
						}
						want := tag
						if !strings.Contains(strings.Join(caps, "-"), "thread") {
							want = ""
							if len(got) != 1 || !reflect.DeepEqual(fresh.EventID, r.Envelope.EventID) || !reflect.DeepEqual(fresh.HistoryEntryID, r.Envelope.HistoryEntryID) {
								t.Fatal("legacy clear")
							}
						}
						if string(fresh.SessionID) != want {
							t.Fatalf("tag = %s, want %s", fresh.SessionID, want)
						}
						for _, clear := range got[:len(got)-1] {
							if !clear.SessionStateCleared || string(clear.Payload) != "{}" {
								t.Fatal("bad clear")
							}
						}
					}
				}
			})
		}
	}
}

func TestLiveStateGates(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		caps  []string
		conv  string
		known bool
		want  bool
	}{
		{"interactive", threadCaps, gateClaudeConv, true, true}, {"missing-interactive", []string{"thread"}, gateClaudeConv, true, false},
		{"codex", threadCaps, gateCodexConv, true, false}, {"multi-agent", []string{"thread", "interactive", "multi_agent"}, gateCodexConv, true, true},
		{"unknown", threadCaps, "unknown", true, false}, {"membership-unwired", threadCaps, gateClaudeConv, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, rec, recv := liveFixture(t, tc.caps, func(c *V2SessionConfig) {
				if !tc.known {
					c.KnownConversation = nil
				}
			})
			seen := 1
			r := liveReading("tool_progress", tc.conv, "tool", `"s"`, 1, 1)
			r.Envelope.SessionStateCleared = true
			r.Envelope.Payload = json.RawMessage(`{}`)
			n := uint64(42)
			r.Envelope.InReplyTo = &n
			if err := m.PushLiveState(t.Context(), v2TestConnID, r); err != nil {
				t.Fatal(err)
			}
			got := liveDrain(t, m, rec, recv, &seen, 3)
			if (len(got) == 1) != tc.want {
				t.Fatalf("clear gate: %#v", got)
			}
			r.Envelope.SessionStateCleared = false
			r.Envelope.Payload = json.RawMessage(`{}`)
			r.Revision = 2
			if err := m.PushLiveState(t.Context(), v2TestConnID, r); err != nil {
				t.Fatal(err)
			}
			got = liveDrain(t, m, rec, recv, &seen, 3)
			if (len(got) == 1) != tc.want {
				t.Fatalf("reading gate: %#v", got)
			}
		})
	}
}

func TestLiveStateOrdering(t *testing.T) {
	t.Parallel()
	snapshot := liveReading("tool_progress", gateClaudeConv, "tool-a", `"z-old"`, 1, 1)
	m, _, rec, recv := liveFixture(t, threadCaps, func(c *V2SessionConfig) {
		c.ThreadLiveState = func() func() (LiveState, bool) {
			n := 0
			return func() (LiveState, bool) { n++; return snapshot, n == 1 }
		}
	})
	seen := 1
	inputs := []LiveState{
		liveReading("tool_progress", gateClaudeConv, "tool-a", `"z-old"`, 1, 3),
		snapshot,
		liveReading("tool_progress", gateClaudeConv, "tool-b", `"z-old"`, 1, 1),
		liveReading("modal_shown", gateClaudeConv, "prompt-a", `"z-old"`, 1, 1),
		liveReading("modal_dismissed", gateClaudeConv, "prompt-a", `"z-old"`, 1, 2),
		liveReading("modal_shown", gateClaudeConv, "prompt-a", `"z-old"`, 1, 1),
		liveReading("modal_shown", gateClaudeConv, "prompt-b", `"z-old"`, 1, 1),
		liveReading("tool_progress", "other", "tool-a", `"z-old"`, 1, 1),
		liveReading("tool_progress", gateClaudeConv, "tool-a", `"a-new"`, 2, 1),
		liveReading("tool_progress", gateClaudeConv, "tool-b", `"z-old"`, 1, 99),
		liveReading("tool_progress", gateClaudeConv, "tool-a", `"b-newer"`, 3, 1),
		liveReading("tool_progress", gateClaudeConv, "tool-a", `"a-new"`, 2, 99),
	}
	n := uint64(98)
	inputs[len(inputs)-3].Envelope.InReplyTo = &n
	var fresh []string
	for _, r := range inputs {
		if err := m.PushLiveState(t.Context(), v2TestConnID, r); err != nil {
			t.Fatal(err)
		}
		for _, e := range liveDrain(t, m, rec, recv, &seen, 2) {
			if !e.SessionStateCleared {
				fresh = append(fresh, fmt.Sprintf("%s/%d", e.SessionID, e.ID))
			}
		}
	}
	if got := liveDrain(t, m, rec, recv, &seen, 4); len(got) != 0 {
		t.Fatalf("overtaken snapshot: %#v", got)
	}
	want := []string{`"z-old"/3`, `"z-old"/1`, `"z-old"/1`, `"z-old"/2`, `"z-old"/1`, `"z-old"/1`, `"a-new"/1`, `"b-newer"/1`}
	if !reflect.DeepEqual(fresh, want) {
		t.Fatalf("fresh: %v, want %v", fresh, want)
	}
	// A delayed explicit clear cannot erase a delivered fresh reading.
	r := inputs[len(inputs)-2]
	r.Envelope.Payload = json.RawMessage(`{}`)
	r.Envelope.SessionStateCleared = true
	if err := m.PushLiveState(t.Context(), v2TestConnID, r); err != nil {
		t.Fatal(err)
	}
	if got := liveDrain(t, m, rec, recv, &seen, 3); len(got) != 0 {
		t.Fatalf("late clear: %#v", got)
	}
}

func TestLiveStateConnect(t *testing.T) {
	t.Parallel()
	for _, caps := range [][]string{threadCaps, {"interactive"}} {
		calls := 0
		m, _, rec, recv := liveFixture(t, caps, func(c *V2SessionConfig) {
			c.RunningTurnPhases = func() []protocol.TurnStatePayload { calls++; return nil }
			c.ThreadLiveState = func() func() (LiveState, bool) {
				calls += 10
				i := 0
				return func() (LiveState, bool) {
					types := []string{"api_retry", "compacting", "modal_shown", "question_shown"}
					if i == len(types) {
						return LiveState{}, false
					}
					r := liveReading(types[i], gateClaudeConv, "", `null`, 1, 1)
					i++
					return r, true
				}
			}
		})
		seen := 1
		got := liveDrain(t, m, rec, recv, &seen, 15)
		if len(caps) == 2 {
			if calls != 10 || len(got) != 8 {
				t.Fatalf("connect calls=%d frames=%d", calls, len(got))
			}
			for i := 0; i < 8; i += 2 {
				if !got[i].SessionStateCleared || got[i+1].SessionStateCleared || !bytes.Contains(got[i+1].Payload, []byte(`"active":false`)) {
					t.Fatal("connect did not clear then supply current reading")
				}
			}
		} else if calls != 1 || len(got) != 0 {
			t.Fatalf("legacy provider: %d/%d", calls, len(got))
		}
	}
}

func TestLiveStatePacing(t *testing.T) {
	t.Parallel()
	up := false
	pulled := 0
	m, s, rec, recv := liveFixture(t, threadCaps, func(c *V2SessionConfig) {
		c.Connected = func() bool { return up }
		c.ThreadLiveState = func() func() (LiveState, bool) {
			return func() (LiveState, bool) {
				pulled++
				if pulled > pushQueueCap*2 {
					return LiveState{}, false
				}
				return liveReading("tool_progress", gateClaudeConv, fmt.Sprint(pulled), `"s"`, 1, 1), true
			}
		}
	})
	seen := 1
	if got := liveDrain(t, m, rec, recv, &seen, 3); len(got) != 0 || pulled != 0 {
		t.Fatal("down consumed state")
	}
	up = true
	got := liveDrain(t, m, rec, recv, &seen, pushQueueCap*2+4)
	if len(got) != pushQueueCap*2+1 || m.queues[v2TestConnID].overflowed {
		t.Fatalf("large snapshot: %d", len(got))
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := len(rec.snapshot())
	m.drainOnce(ctx)
	if len(rec.snapshot()) != before {
		t.Fatal("cancelled pump sent")
	}
	m.teardown(s)
	liveDrain(t, m, rec, recv, &seen, 3)
	if err := m.PushLiveState(t.Context(), v2TestConnID, liveReading("turn_state", gateClaudeConv, "", `null`, 1, 1)); err != ErrConnNotFound {
		t.Fatalf("teardown: %v", err)
	}
}

func TestLiveStateBounds(t *testing.T) {
	t.Parallel()
	m, _, rec, recv := liveFixture(t, threadCaps, nil)
	seen := 1
	for i, char := range []string{"x", "\x00"} {
		commands := make([]turnevent.SlashCommand, 128)
		for j := range commands {
			commands[j] = turnevent.SlashCommand{Name: strings.Repeat(char, 256), ArgumentHint: strings.Repeat(char, 256), Description: strings.Repeat(char, 256), Aliases: []string{strings.Repeat(char, 64), strings.Repeat(char, 64), strings.Repeat(char, 64), strings.Repeat(char, 64), strings.Repeat(char, 64), strings.Repeat(char, 64), strings.Repeat(char, 64), strings.Repeat(char, 64)}}
		}
		typ, payload, ok := turnbridge.MapEvent(turnevent.SlashCommandList{Commands: commands}, turnbridge.TurnContext{ConversationID: gateClaudeConv})
		if !ok {
			t.Fatal("fixture mapping")
		}
		r := liveReading(typ, gateClaudeConv, "", `"source"`, 1, uint64(i+1))
		r.Envelope.SessionID, _ = json.Marshal(strings.Repeat(char, 64))
		r.Envelope.Payload, _ = json.Marshal(payload)
		if len(r.Envelope.Payload) < 62000 {
			t.Fatal("fixture did not approach cap")
		}
		if err := m.PushLiveState(t.Context(), v2TestConnID, r); err != nil {
			t.Fatal(err)
		}
		got := liveDrain(t, m, rec, recv, &seen, 3)
		if len(got) == 0 || !bytes.Equal(got[len(got)-1].Payload, r.Envelope.Payload) {
			t.Fatal("supported reading vanished or was truncated")
		}
	}
	for _, tag := range []string{`""`, `true`, `42`, `{}`, `[]`} {
		r := liveReading("turn_state", gateClaudeConv, "", tag, 1, 8)
		if err := m.PushLiveState(t.Context(), v2TestConnID, r); err == nil {
			t.Fatalf("accepted tag %s", tag)
		}
	}
	r := liveReading("turn_state", gateClaudeConv, "", `null`, 1, 8)
	r.Envelope.SessionStateCleared = true
	if err := m.PushLiveState(t.Context(), v2TestConnID, r); err == nil {
		t.Fatal("accepted nonempty clear")
	}
	r.Envelope.SessionStateCleared = false
	r.Envelope.Payload = json.RawMessage(`{"text":"` + strings.Repeat("x", protocol.MaxThreadEnvelopeBytes) + `"}`)
	if err := m.PushLiveState(t.Context(), v2TestConnID, r); err == nil {
		t.Fatal("accepted oversize reading")
	}
}

func TestLiveStateGeneratedClearBound(t *testing.T) {
	t.Parallel()
	bad := liveReading("turn_state", gateClaudeConv, "", `"s"`, 1, 1)
	bad.Envelope.Payload = json.RawMessage(`{}`)
	raw, err := json.Marshal(bad.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	bad.Envelope.SessionID, err = json.Marshal(strings.Repeat("s", 1+protocol.MaxThreadEnvelopeBytes-len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(bad.Envelope)
	if err != nil || len(raw) != protocol.MaxThreadEnvelopeBytes {
		t.Fatalf("fresh fixture: bytes=%d err=%v", len(raw), err)
	}
	clear := bad.Envelope
	clear.SessionStateCleared = true
	raw, err = json.Marshal(clear)
	if err != nil || len(raw) <= protocol.MaxThreadEnvelopeBytes {
		t.Fatalf("clear fixture: bytes=%d err=%v", len(raw), err)
	}
	healthy := liveReading("turn_state", gateClaudeConv, "", `"healthy"`, 1, 1)
	other := liveReading("compacting", gateClaudeConv, "", `"healthy"`, 1, 1)
	for _, source := range []string{"queued", "snapshot"} {
		t.Run(source, func(t *testing.T) {
			readings := []LiveState{other}
			if source == "snapshot" {
				readings = []LiveState{bad, healthy, other}
			}
			m, s, rec, recv := liveFixture(t, threadCaps, func(c *V2SessionConfig) {
				c.ThreadLiveState = func() func() (LiveState, bool) {
					return func() (LiveState, bool) {
						if len(readings) == 0 {
							return LiveState{}, false
						}
						r := readings[0]
						readings = readings[1:]
						return r, true
					}
				}
			})
			if source == "queued" {
				if err := m.PushLiveState(t.Context(), v2TestConnID, bad); !errors.Is(err, errInvalidLiveState) {
					t.Errorf("oversized generated clear admission: %v", err)
				}
				if len(m.queues[v2TestConnID].items) != 0 {
					t.Error("invalid reading entered push queue")
				}
				if err := m.PushLiveState(t.Context(), v2TestConnID, healthy); err != nil {
					t.Fatal(err)
				}
			}
			seen := 1
			got := liveDrain(t, m, rec, recv, &seen, 8)
			if len(got) != 4 || !got[0].SessionStateCleared || got[1].SessionStateCleared ||
				!bytes.Equal(got[1].Payload, healthy.Envelope.Payload) || got[1].Type != healthy.Envelope.Type ||
				!got[2].SessionStateCleared || got[3].SessionStateCleared ||
				!bytes.Equal(got[3].Payload, other.Envelope.Payload) || got[3].Type != other.Envelope.Type {
				t.Errorf("healthy queued/snapshot readings did not drain: %d frames", len(got))
			}
			if s.livePending != nil || s.liveSnapshot != nil || len(m.queues[v2TestConnID].items) != 0 {
				t.Error("pump retained undeliverable state")
			}
			select {
			case <-m.drainCh:
			default:
			}
			m.drainOnce(t.Context())
			select {
			case <-m.drainCh:
				t.Error("exhausted pump keeps scheduling drains")
			default:
			}
		})
	}
}

func TestLiveStateClearFailure(t *testing.T) {
	t.Parallel()
	m, s, rec, _ := liveFixture(t, threadCaps, nil)
	r := liveReading("turn_state", gateClaudeConv, "", `"source"`, 1, 1)
	// A detached connection cannot deliver its clear or retain its fresh reading.
	delete(m.sessions, s.connID)
	err := m.forwardLiveState(t.Context(), s, r)
	m.sessions[s.connID] = s
	if !errors.Is(err, ErrConnNotFound) {
		t.Fatalf("clear forwarding error: %v", err)
	}
	if s.livePending != nil || len(s.liveWatermarks) != 0 || len(rec.snapshot()) != 1 {
		t.Fatal("failed clear retained delivery state or sent a frame")
	}
}

func TestLiveStateReplies(t *testing.T) {
	t.Parallel()
	for _, caps := range [][]string{threadCaps, {"interactive"}, {"thread"}} {
		m, s, rec, recv := liveFixture(t, caps, nil)
		for _, conv := range []string{gateClaudeConv, gateCodexConv} {
			r := liveReading("context_usage", conv, "", `"origin"`, 1, 1)
			n := uint64(17)
			r.Envelope.InReplyTo = &n
			raw, _ := json.Marshal(r.Envelope)
			before := len(rec.snapshot())
			m.forwardAppReply(s, protocol.RoutingEnvelope{ConnID: v2TestConnID, Frame: raw})
			allowed := !s.thread || (s.interactive && conv == gateClaudeConv)
			if (len(rec.snapshot()) == before+1) != allowed {
				t.Fatal("reply access gate")
			}
			if allowed {
				got := threadPlain(t, rec.snapshot()[before], recv)
				if !s.thread {
					raw = replaceObjectField(raw, "session_id", nil)
				}
				if !bytes.Equal(got, raw) {
					t.Fatalf("reply bytes changed: %s", got)
				}
			}
		}
	}
}

func TestLiveStateRun(t *testing.T) {
	t.Parallel()
	priv, pub := genV2Keypair(t)
	frames := make(chan protocol.RoutingEnvelope, 2)
	rec := &v2Recorder{}
	cfg := threadConfig(t, frames, rec, priv)
	cfg.ThreadLiveState = func() func() (LiveState, bool) {
		i := 0
		return func() (LiveState, bool) {
			i++
			return liveReading("tool_progress", gateClaudeConv, fmt.Sprint(i), `"s"`, 1, 1), i <= pushQueueCap*2
		}
	}
	mgr, stop := startManager(t, cfg)
	t.Cleanup(stop)
	recv := openGateConn(t, frames, rec, v2TestConnID, pub, v2TestInstallPriv, threadCaps, nil)
	msgs := waitForEnvelopes(t, rec, pushQueueCap*2+2)
	for _, frame := range msgs[1:] {
		threadPlain(t, frame, recv)
	}
	if got := mgr.ActiveConns(t.Context()); len(got) != 1 {
		t.Fatal("snapshot overflow closed connection")
	}
}
