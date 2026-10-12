package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func liveIdentityEnvelope(t *testing.T, e protocol.Envelope, identity string) protocol.Envelope {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if identity != "" {
		raw = replaceObjectField(raw, "conversation_id", json.RawMessage(identity))
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestLiveStateConversationRouting(t *testing.T) {
	t.Parallel()
	for _, tag := range []string{"", "null", `"same-session"`} {
		t.Run(tag, func(t *testing.T) {
			readings := []LiveState{
				liveReading("tool_progress", "source-a", "same-tool", tag, 1, 1),
				liveReading("tool_progress", "source-b", "same-tool", tag, 1, 1),
			}
			m, _, rec, recv := liveFixture(t, threadCaps, func(c *V2SessionConfig) {
				c.ThreadLiveState = func() func() (LiveState, bool) {
					i := 0
					return func() (LiveState, bool) {
						if i == len(readings) {
							return LiveState{}, false
						}
						r := readings[i]
						i++
						return r, true
					}
				}
			})
			for i := 0; i < 6; i++ {
				m.drainOnce(t.Context())
			}
			seen := 1
			check := func(r LiveState, clear bool) {
				t.Helper()
				e, fields := suppliedWire(t, rec, recv, &seen)
				identity, _ := json.Marshal(r.ConversationID)
				if !bytes.Equal(fields["conversation_id"], identity) || string(fields["session_id"]) != tag {
					t.Fatalf("routing/provenance: conversation=%s session=%s", fields["conversation_id"], fields["session_id"])
				}
				for _, key := range []string{"generation", "session_generation", "revision", "reading_id", "last_event_id", "read_up_to"} {
					if _, ok := fields[key]; ok {
						t.Fatalf("internal/history metadata: %s", key)
					}
				}
				if clear {
					if !e.SessionStateCleared || string(e.Payload) != "{}" {
						t.Fatal("clear must stay empty")
					}
					if _, ok := fields["in_reply_to"]; ok {
						t.Fatal("correlated clear")
					}
				} else if e.SessionStateCleared || !bytes.Equal(e.Payload, r.Envelope.Payload) || !equalReply(e.InReplyTo, r.Envelope.InReplyTo) {
					t.Fatal("fresh payload/correlation changed")
				}
			}
			for _, r := range readings {
				check(r, true)
				check(r, false)
			}
			for _, old := range readings {
				r := old
				r.SessionGeneration = 2
				r.Envelope.Payload = json.RawMessage(`{}`)
				r.Envelope.SessionStateCleared = true
				reply := uint64(77)
				r.Envelope.InReplyTo = &reply
				if err := m.PushLiveState(t.Context(), v2TestConnID, r); err != nil {
					t.Fatal(err)
				}
				m.drainOnce(t.Context())
				check(r, true)
				r.Envelope = old.Envelope
				r.Envelope.InReplyTo = &reply
				r.Revision = 3
				if err := m.PushLiveState(t.Context(), v2TestConnID, r); err != nil {
					t.Fatal(err)
				}
				m.drainOnce(t.Context())
				check(r, false)
				// Duplicate clear and predecessor reply cannot affect either conversation.
				duplicate := r
				duplicate.Envelope.Payload = json.RawMessage(`{}`)
				duplicate.Envelope.SessionStateCleared = true
				stale := old
				stale.Envelope.InReplyTo = &reply
				for _, suppressed := range []LiveState{duplicate, stale} {
					if err := m.PushLiveState(t.Context(), v2TestConnID, suppressed); err != nil {
						t.Fatal(err)
					}
					m.drainOnce(t.Context())
				}
				if len(rec.snapshot()) != seen {
					t.Fatal("duplicate/stale source reached wire")
				}
			}
		})
	}
}

func equalReply(a, b *uint64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func TestLiveStateConversationIdentity(t *testing.T) {
	t.Parallel()
	for _, location := range []string{"envelope", "payload"} {
		for _, identity := range []string{"", `"source"`, "null", `""`, "true", "42", "{}", "[]", `"other"`} {
			for _, clear := range []bool{false, true} {
				if location == "payload" && clear {
					continue // A supplied clear's payload must be {}.
				}
				t.Run(fmt.Sprintf("%s/%s/%t", location, identity, clear), func(t *testing.T) {
					r := liveReading("turn_state", "source", "", "null", 1, 1)
					r.Envelope.Payload = json.RawMessage(`{}`)
					r.Envelope.SessionStateCleared = clear
					if location == "envelope" {
						r.Envelope = liveIdentityEnvelope(t, r.Envelope, identity)
					} else if identity != "" {
						r.Envelope.Payload = replaceObjectField(r.Envelope.Payload, "conversation_id", json.RawMessage(identity))
					}
					valid := identity == "" || identity == `"source"`
					m, _, rec, recv := liveFixture(t, threadCaps, nil)
					err := m.PushLiveState(t.Context(), v2TestConnID, r)
					if valid != (err == nil) {
						t.Fatalf("identity admission: %v", err)
					}
					if !valid && !errors.Is(err, errInvalidLiveState) {
						t.Fatalf("invalid identity error: %v", err)
					}
					seen := 1
					got := liveDrain(t, m, rec, recv, &seen, 3)
					if valid != (len(got) > 0) {
						t.Fatal("identity delivery changed")
					}
					// Detached cursor delivery also validates before any clear or watermark.
					m2, s2, rec2, recv2 := liveFixture(t, threadCaps, nil)
					s2.liveSnapshot = func() (LiveState, bool) { return r, true }
					seen2 := 1
					got = liveDrain(t, m2, rec2, recv2, &seen2, 2)
					if valid != (len(got) > 0) {
						t.Fatal("snapshot identity admission changed")
					}
					if !valid && len(s2.liveWatermarks) != 0 {
						t.Fatal("rejected snapshot advanced watermark")
					}
				})
			}
		}
	}
}

func TestLiveStateRoutingBounds(t *testing.T) {
	t.Parallel()
	for _, clear := range []bool{false, true} {
		for _, extra := range []int{0, 1} {
			t.Run(fmt.Sprintf("clear=%t/extra=%d", clear, extra), func(t *testing.T) {
				r := liveReading("turn_state", "source", "", `"s"`, 1, 1)
				r.Envelope.Payload = json.RawMessage(`{}`)
				r.Envelope.SessionStateCleared = clear
				// Size from independent wire JSON, including the new routing field.
				frame := r.Envelope
				frame.SessionStateCleared = true
				raw, _ := json.Marshal(frame)
				raw = replaceObjectField(raw, "conversation_id", json.RawMessage(`"source"`))
				r.Envelope.SessionID, _ = json.Marshal(strings.Repeat("s", 1+protocol.MaxThreadEnvelopeBytes-len(raw)+extra))
				m, _, rec, recv := liveFixture(t, threadCaps, nil)
				err := m.PushLiveState(t.Context(), v2TestConnID, r)
				if (extra == 0) != (err == nil) {
					t.Fatalf("routing bound admission: %v", err)
				}
				seen := 1
				got := liveDrain(t, m, rec, recv, &seen, 3)
				if (extra == 0) != (len(got) > 0) {
					t.Fatal("routing bound delivery")
				}
			})
		}
	}
}

func TestLiveStateReadingRoutingBound(t *testing.T) {
	t.Parallel()
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			r := liveReading("turn_state", "source", "", "", 1, 1)
			r.Envelope.Payload = json.RawMessage(`{"padding":""}`)
			raw, _ := json.Marshal(r.Envelope)
			raw = replaceObjectField(raw, "conversation_id", json.RawMessage(`"source"`))
			r.Envelope.Payload = json.RawMessage(`{"padding":"` + strings.Repeat("x", protocol.MaxThreadEnvelopeBytes-len(raw)+extra) + `"}`)
			m, _, rec, recv := liveFixture(t, threadCaps, nil)
			err := m.PushLiveState(t.Context(), v2TestConnID, r)
			if (extra == 0) != (err == nil) {
				t.Fatalf("reading bound admission: %v", err)
			}
			seen := 1
			got := liveDrain(t, m, rec, recv, &seen, 3)
			if extra == 0 && (len(got) != 2 || !bytes.Equal(got[1].Payload, r.Envelope.Payload)) {
				t.Fatal("exact-cap reading vanished")
			}
			if extra == 1 && len(got) != 0 {
				t.Fatal("oversized reading emitted clear or fresh")
			}
		})
	}
}

func TestLiveStateProjectedRoutingBound(t *testing.T) {
	t.Parallel()
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			r := liveReading(protocol.TypeModelList, "source", "", "null", 1, 1)
			r.ModelListUnprojected = true
			p := protocol.ModelListPayload{ConversationID: "source", Models: []protocol.ModelOption{{Value: "m"}}}
			r.Envelope.Payload, _ = json.Marshal(p)
			projected := p
			projected.Models = []protocol.ModelOption{{Value: ""}}
			e := r.Envelope
			e.Payload, _ = json.Marshal(projected)
			raw, _ := json.Marshal(e)
			raw = replaceObjectField(raw, "conversation_id", json.RawMessage(`"source"`))
			projected.Models[0].Value = strings.Repeat("x", protocol.MaxThreadEnvelopeBytes-len(raw)+extra)
			m, s, rec, recv := liveFixture(t, []string{"interactive", "thread", "multi_agent"}, func(c *V2SessionConfig) {
				c.MergedModelOptions = func([]protocol.ModelOption) []protocol.ModelOption { return projected.Models }
			})
			if err := m.PushLiveState(t.Context(), v2TestConnID, r); err != nil {
				t.Fatal(err)
			}
			m.drainOnce(t.Context())
			seen := 1
			clear, fields := suppliedWire(t, rec, recv, &seen)
			if !clear.SessionStateCleared || string(clear.Payload) != "{}" || string(fields["conversation_id"]) != `"source"` {
				t.Fatal("projected clear changed")
			}
			fresh := *s.livePending
			s.livePending = nil
			err := m.forwardLiveState(t.Context(), s, fresh)
			if extra == 0 {
				if err != nil {
					t.Fatal(err)
				}
				raw = threadPlain(t, rec.snapshot()[seen], recv)
				if len(raw) != protocol.MaxThreadEnvelopeBytes {
					t.Fatalf("projected envelope size=%d", len(raw))
				}
				var got map[string]json.RawMessage
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatal(err)
				}
				want, _ := json.Marshal(projected)
				if !bytes.Equal(got["payload"], want) || string(got["conversation_id"]) != `"source"` {
					t.Fatal("projected bytes changed")
				}
			} else if !errors.Is(err, errInvalidLiveState) || len(rec.snapshot()) != seen || s.liveWatermarks["source"].revisions[liveReadingKey{protocol.TypeModelList, ""}] != 0 {
				t.Fatal("oversized projection encrypted or advanced revision")
			}
		})
	}
}

func TestLiveStateConversationIdentityDetached(t *testing.T) {
	t.Parallel()
	m, _, rec, recv := liveFixture(t, threadCaps, nil)
	r := liveReading("turn_state", "source", "", "", 1, 1)
	r.Envelope.ConversationID = json.RawMessage(`"source"`)
	if err := m.PushLiveState(t.Context(), v2TestConnID, r); err != nil {
		t.Fatal(err)
	}
	copy(r.Envelope.ConversationID, `"other!"`)
	for i := 0; i < 3; i++ {
		m.drainOnce(t.Context())
	}
	seen := 1
	for i := 0; i < 2; i++ {
		_, fields := suppliedWire(t, rec, recv, &seen)
		if string(fields["conversation_id"]) != `"source"` {
			t.Fatal("borrowed routing identity mutated")
		}
	}
}

func TestLiveStateRoutingLegacyBytes(t *testing.T) {
	t.Parallel()
	for _, identity := range []string{"", `"source"`} {
		m, _, rec, recv := liveFixture(t, []string{"interactive"}, nil)
		r := liveReading("turn_state", "source", "", "", 1, 1)
		want, _ := json.Marshal(r.Envelope)
		r.Envelope = liveIdentityEnvelope(t, r.Envelope, identity)
		if err := m.PushLiveState(t.Context(), v2TestConnID, r); err != nil {
			t.Fatal(err)
		}
		m.drainOnce(t.Context())
		got := threadPlain(t, rec.snapshot()[1], recv)
		if !bytes.Equal(got, want) {
			t.Fatal("legacy envelope bytes changed")
		}
	}
}
