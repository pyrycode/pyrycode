package relay

import (
	"encoding/json"
	"log/slog"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #2125 inbound request_model_list → on-demand model-menu fixtures ---

const (
	// modelReqBoundConvID is a conversation whose own bound session holds a menu.
	modelReqBoundConvID = "conv-model-bound"
	// modelReqUnboundConvID is a conversation the daemon HOSTS but that has no
	// bound session. Since #2124 the daemon-side resolver answers it anyway, from
	// the daemon-wide vocabulary, and it is the case this verb exists to serve —
	// pyrycode-desktop#1054's blank composer on a freshly created chat.
	modelReqUnboundConvID = "conv-model-unbound"
	// modelReqStarvedConvID is HOSTED but resolves to nothing: no vocabulary is
	// retained anywhere. The one condition that earns model_list.unavailable.
	modelReqStarvedConvID = "conv-model-starved"
	// modelReqForeignConvID is not one this daemon hosts. An arbitrary client
	// string as far as every gate is concerned.
	modelReqForeignConvID = "conv-model-foreign"
)

// fixtureModelList is the menu ModelListFor reports for modelReqBoundConvID.
//
// DroppedModels is deliberately NON-ZERO and deliberately disagrees with
// len(Models): the count is the producer's report of what it cut and must ride
// through untouched, never recomputed from the slice — the rule
// ModelListPayload's own doc states. A handler that rebuilt the payload instead
// of forwarding it reddens here.
var fixtureModelList = protocol.ModelListPayload{
	ConversationID: modelReqBoundConvID,
	Models: []protocol.ModelOption{
		{
			ResolvedModel:    "claude-opus-5-20260101",
			Value:            "default",
			DisplayName:      "Default (recommended)",
			EffortLevels:     []string{"low", "medium", "high"},
			SupportsAutoMode: true,
		},
		{
			ResolvedModel: "claude-fable-5-1",
			Value:         "fable",
			DisplayName:   "Fable",
			EffortLevels:  []string{},
		},
	},
	DroppedModels: 7,
}

// fixtureDaemonWideModelList is what an UNBOUND but hosted conversation resolves
// to: the daemon-wide vocabulary, stamped with that conversation's own id. The
// values are deliberately DISJOINT from fixtureModelList's, so a handler that
// answered the wrong conversation's menu is visible in the reply rather than only
// in a counter.
var fixtureDaemonWideModelList = protocol.ModelListPayload{
	ConversationID: modelReqUnboundConvID,
	Models: []protocol.ModelOption{{
		ResolvedModel:    "claude-sonnet-5",
		Value:            "sonnet",
		DisplayName:      "Sonnet",
		EffortLevels:     []string{"medium"},
		SupportsAutoMode: true,
	}},
	DroppedModels: 0,
}

// poisonedModelList is the fixture seam's REFUSAL return: a fully populated
// payload handed back alongside ok == false.
//
// ModelListFor's doc says a caller MUST NOT read the payload when the comma-ok is
// false, and the production cmd/pyry resolver happens to zero its refusal return
// — so against a zero-valued refusal a handler that DISCARDED the comma-ok would
// emit an empty-but-well-formed model_list and every refusal row below would pass
// while proving nothing. Poisoning it makes "fail closed on ok == false" a tested
// property of internal/relay rather than one borrowed from cmd/pyry.
// poisonedRunConfig is the same device for the run-configuration seam.
var poisonedModelList = protocol.ModelListPayload{
	ConversationID: "conv-refused-999",
	Models: []protocol.ModelOption{{
		ResolvedModel: "claude-refused-1-0",
		Value:         "refused",
		DisplayName:   "Refused",
		EffortLevels:  []string{"max"},
	}},
	DroppedModels: 42,
}

// modelSeamCounts records how often each seam was consulted, so a test can assert
// not merely what came back but which seams were read to say it — the difference
// between "no reply" and "no reply because nothing was consulted", which is the
// whole of AC #3.
//
// Atomic rather than plain ints: the closures run on the manager's Run dispatch
// goroutine while the assertions run on the test goroutine, so a plain counter is
// a data race under `go test -race`.
type modelSeamCounts struct {
	known    atomic.Int64
	resolves atomic.Int64
}

// modelReqSeams is the seam pair this verb consults, grouped so a test states one
// intent instead of threading two closures through every call.
type modelReqSeams struct {
	knownConv    func(string) bool
	modelListFor func(string) (protocol.ModelListPayload, bool)
}

// hostedConversations is the membership double: it hosts the three ids above and
// nothing else. Deliberately NOT the same predicate as the resolver below — the
// two seams answer different questions, and the starved conversation is hosted
// while resolving to nothing, which is exactly the pair that separates the two
// reject codes.
func hostedConversations(id string) bool {
	switch id {
	case modelReqBoundConvID, modelReqUnboundConvID, modelReqStarvedConvID:
		return true
	}
	return false
}

// resolveFixtureModelList is the ModelListFor double: it answers the bound and
// the unbound conversation with disjoint menus, and refuses — poisoned —
// everything else, the starved conversation included.
func resolveFixtureModelList(id string) (protocol.ModelListPayload, bool) {
	switch id {
	case modelReqBoundConvID:
		return fixtureModelList, true
	case modelReqUnboundConvID:
		return fixtureDaemonWideModelList, true
	}
	return poisonedModelList, false
}

// allModelReqSeams wires both seams the production shape wires.
func allModelReqSeams() modelReqSeams {
	return modelReqSeams{knownConv: hostedConversations, modelListFor: resolveFixtureModelList}
}

// countingModelReqSeams wires the same doubles and counts every consultation.
func countingModelReqSeams() (modelReqSeams, *modelSeamCounts) {
	c := &modelSeamCounts{}
	return modelReqSeams{
		knownConv: func(id string) bool {
			c.known.Add(1)
			return hostedConversations(id)
		},
		modelListFor: func(id string) (protocol.ModelListPayload, bool) {
			c.resolves.Add(1)
			return resolveFixtureModelList(id)
		},
	}, c
}

// modelReqManagerFor stands up a v2 manager paired for v2TestToken with the given
// seams. A sibling of readManagerFor rather than a parameter on it, the package's
// established posture: the two verbs wire disjoint seams and merging them would
// make every call site pass nils for the other half.
func modelReqManagerFor(t *testing.T, seams modelReqSeams, logger *slog.Logger) (mgr *V2SessionManager, frames chan protocol.RoutingEnvelope, rec *v2Recorder, respPub []byte) {
	t.Helper()
	var respPriv []byte
	respPriv, respPub = genV2Keypair(t)
	frames = make(chan protocol.RoutingEnvelope, 8)
	rec = &v2Recorder{}
	var stop func()
	mgr, stop = startManager(t, V2SessionConfig{
		Frames:            frames,
		Outbound:          rec.outbound,
		StaticPriv:        respPriv,
		Devices:           v2PairedRegistry(t, v2TestToken),
		ServerID:          v2TestServerID,
		Logger:            logger,
		KnownConversation: seams.knownConv,
		ModelListFor:      seams.modelListFor,
	})
	t.Cleanup(stop)
	return mgr, frames, rec, respPub
}

// sendModelListRequest opens an interactive-or-not conn, sends one
// request_model_list carrying rawPayload verbatim, and returns every application
// frame the daemon sent back on that conn.
//
// rawPayload is raw bytes rather than a typed payload so a row can send a
// MALFORMED body, which is a case the typed form cannot express.
func sendModelListRequest(t *testing.T, seams modelReqSeams, caps []string, reqID uint64, rawPayload string) (replies []protocol.Envelope) {
	t.Helper()
	mgr, frames, rec, respPub := modelReqManagerFor(t, seams, silentLogger())
	send, recv := openModalConn(t, mgr, frames, rec, respPub, "c-int", caps)

	frames <- sealAppFrameConn(t, send, "c-int", protocol.Envelope{
		ID:      reqID,
		Type:    protocol.TypeRequestModelList,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(rawPayload),
	})

	// Barrier: the request is enqueued ahead of this conn's noise_init on the same
	// Frames channel, so once the barrier conn is open the request has been fully
	// handled on the Run goroutine.
	openModalConn(t, mgr, frames, rec, respPub, "c-barrier", []string{protocol.CapabilityInteractive})

	for _, msg := range noiseMsgsForConn(t, rec, "c-int") {
		replies = append(replies, decryptAppFrame(t, msg, recv))
	}
	return replies
}

// TestV2Session_RequestModelList_AnswersWithTheConversationsMenu is AC #1: an
// interactive conn naming a conversation the daemon can answer for gets exactly
// one model_list, correlated by in_reply_to and carrying NO event_id, whose
// payload is the resolver's own.
//
// THE UNBOUND ROW IS THE POINT OF THE TICKET. A conversation with no bound
// session is answered from the daemon-wide vocabulary since #2124, and it is the
// case the live turn lane and the connect-time reconcile both miss for a chat
// created after the client connected. It is asserted here, in the package that
// owns the verb, rather than only in cmd/pyry where the fallback lives.
//
// The absent event_id is load-bearing twice, exactly as it is for the connect-time
// reconcile: the reply never enters the #647 turn-event replay ring, so this path
// adds no per-conversation ring memory, and forwardEnvelope's last_event_id dedup
// stays inert for it, so a reply cannot be dropped as already-seen.
func TestV2Session_RequestModelList_AnswersWithTheConversationsMenu(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		convID string
		want   protocol.ModelListPayload
	}{
		{
			name:   "a conversation whose bound session holds a menu",
			convID: modelReqBoundConvID,
			want:   fixtureModelList,
		},
		{
			name:   "a conversation with NO bound session, answered daemon-wide (#2124)",
			convID: modelReqUnboundConvID,
			want:   fixtureDaemonWideModelList,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			const reqID uint64 = 71
			replies := sendModelListRequest(t, allModelReqSeams(),
				[]string{protocol.CapabilityInteractive}, reqID,
				`{"conversation_id":"`+tc.convID+`"}`)

			if len(replies) != 1 {
				t.Fatalf("got %d app frame(s), want exactly 1 reply", len(replies))
			}
			reply := replies[0]
			if reply.Type != protocol.TypeModelList {
				t.Fatalf("reply Type = %q, want %q — the answer is the EXISTING frame, not a second outbound shape", reply.Type, protocol.TypeModelList)
			}
			if reply.InReplyTo == nil || *reply.InReplyTo != reqID {
				t.Errorf("reply InReplyTo = %v, want pointer to %d", reply.InReplyTo, reqID)
			}
			if reply.EventID != nil {
				t.Errorf("reply EventID = %d, want absent — a correlated reply must never enter the replay ring or advance a client's cursor", *reply.EventID)
			}

			var got protocol.ModelListPayload
			if err := json.Unmarshal(reply.Payload, &got); err != nil {
				t.Fatalf("decode model_list payload: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("payload = %+v, want %+v", got, tc.want)
			}
			if got.DroppedModels != tc.want.DroppedModels {
				t.Errorf("DroppedModels = %d, want %d — the count rides through from the producer and is NEVER recomputed from len(models)", got.DroppedModels, tc.want.DroppedModels)
			}
		})
	}
}

// TestV2Session_RequestModelList_NonInteractiveIsInert is AC #3: a conn that did
// not negotiate the interactive capability gets nothing at all — no model_list, no
// error, and no consultation of either seam, so it cannot learn whether the named
// conversation exists.
//
// The seam counters are what make this discriminating. "Zero frames" alone would
// also pass against a handler that resolved the conversation and then failed to
// send; asserting that NOTHING was consulted is the authz property, and it is why
// the capability gate must stay ahead of both the decode and the membership check.
//
// It names the BOUND conversation deliberately — the one row that would otherwise
// produce a reply — so a deleted gate is a loud failure rather than a silent one.
func TestV2Session_RequestModelList_NonInteractiveIsInert(t *testing.T) {
	t.Parallel()

	seams, counts := countingModelReqSeams()
	replies := sendModelListRequest(t, seams, nil, 72,
		`{"conversation_id":"`+modelReqBoundConvID+`"}`)

	if len(replies) != 0 {
		t.Fatalf("non-interactive conn got %d app frame(s), want 0 — no reply of any kind", len(replies))
	}
	if n := counts.known.Load(); n != 0 {
		t.Errorf("KnownConversation consulted %d time(s), want 0 — the capability gate must precede the membership check", n)
	}
	if n := counts.resolves.Load(); n != 0 {
		t.Errorf("ModelListFor consulted %d time(s), want 0 — the capability gate must precede the resolve", n)
	}
}

// TestV2Session_RequestModelList_Refuses is AC #2: every request the daemon cannot
// answer gets exactly one error frame correlated by in_reply_to — NEVER a
// model_list with an empty models array — under one of two codes.
//
// THE TWO ARMS ARE SEPARATED BY MEMBERSHIP, which is the design: KnownConversation
// answers "is this conversation ours", so a false there is the permanent
// conversation.not_found, and a resolver refusal PAST it is the retryable
// model_list.unavailable. The starved row is what proves the pair is not one
// check spelled twice — it is hosted and still resolves to nothing.
//
// The nil-seam rows land on the retryable arm by design rather than by accident: a
// distinguishable code would publish whether the host's model-list source is
// wired, which is a fact about the machine and not about the request.
//
// Every row runs against the POISONED refusal return, so a handler that discarded
// the comma-ok emits poisonedModelList and fails on Type here rather than passing
// vacuously.
func TestV2Session_RequestModelList_Refuses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		seams         modelReqSeams
		payload       string
		wantCode      string
		wantRetryable bool
	}{
		{
			name:     "a conversation this daemon does not host",
			seams:    allModelReqSeams(),
			payload:  `{"conversation_id":"` + modelReqForeignConvID + `"}`,
			wantCode: protocol.CodeConversationNotFound,
		},
		{
			// A type mismatch: valid JSON at the envelope level, undecodable into
			// the payload struct, so ConversationID is left empty.
			name:     "a payload that does not decode names no conversation",
			seams:    allModelReqSeams(),
			payload:  `{"conversation_id":123}`,
			wantCode: protocol.CodeConversationNotFound,
		},
		{
			// Not an object at all — the shape a bare or truncated frame from an
			// un-updated client decodes to.
			name:     "a non-object payload names no conversation",
			seams:    allModelReqSeams(),
			payload:  `"conv-model-bound"`,
			wantCode: protocol.CodeConversationNotFound,
		},
		{
			name:     "an absent conversation id names nothing",
			seams:    allModelReqSeams(),
			payload:  `{}`,
			wantCode: protocol.CodeConversationNotFound,
		},
		{
			name:     "a nil membership seam refuses everything",
			seams:    modelReqSeams{knownConv: nil, modelListFor: resolveFixtureModelList},
			payload:  `{"conversation_id":"` + modelReqBoundConvID + `"}`,
			wantCode: protocol.CodeConversationNotFound,
		},
		{
			name:          "hosted, but no vocabulary retained anywhere",
			seams:         allModelReqSeams(),
			payload:       `{"conversation_id":"` + modelReqStarvedConvID + `"}`,
			wantCode:      protocol.CodeModelListUnavailable,
			wantRetryable: true,
		},
		{
			name:          "hosted, but no model-list source wired",
			seams:         modelReqSeams{knownConv: hostedConversations, modelListFor: nil},
			payload:       `{"conversation_id":"` + modelReqBoundConvID + `"}`,
			wantCode:      protocol.CodeModelListUnavailable,
			wantRetryable: true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			const reqID uint64 = 73
			replies := sendModelListRequest(t, tc.seams,
				[]string{protocol.CapabilityInteractive}, reqID, tc.payload)

			if len(replies) != 1 {
				t.Fatalf("got %d app frame(s), want exactly 1 reject", len(replies))
			}
			reply := replies[0]
			if reply.Type != protocol.TypeModelList {
				// Deliberately spelled as the thing that must NOT happen: an empty
				// models array standing in for "unknown" is the failure AC #2 names.
				if reply.Type != protocol.TypeError {
					t.Fatalf("reply Type = %q, want %q", reply.Type, protocol.TypeError)
				}
			} else {
				t.Fatalf("reply Type = %q — a refusal must be an error frame, never a model_list with an empty models array", reply.Type)
			}
			if reply.InReplyTo == nil || *reply.InReplyTo != reqID {
				t.Errorf("reply InReplyTo = %v, want pointer to %d", reply.InReplyTo, reqID)
			}

			var got protocol.ErrorPayload
			if err := json.Unmarshal(reply.Payload, &got); err != nil {
				t.Fatalf("decode error payload: %v", err)
			}
			if got.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", got.Code, tc.wantCode)
			}
			if got.Retryable != tc.wantRetryable {
				t.Errorf("retryable = %v, want %v — the flag is what a client branches on, so it must not drift from the code", got.Retryable, tc.wantRetryable)
			}
			if got.Message == "" {
				t.Error("message is empty; every reject carries a static daemon-authored message")
			}
		})
	}
}

// TestV2Session_RequestModelList_MalformedPayloadReachesNoResolver pins the
// ORDERING that makes the tolerated decode safe.
//
// The payload carries a number where a string belongs — valid JSON on the wire,
// undecodable into the struct — so ConversationID is left empty. That empty id
// must be spent on the membership gate and go no further: the resolver is never
// consulted with it. Tolerating a decode failure is only defensible while the
// empty id reaches nothing but a registry lookup — the history verb cannot
// tolerate it because there the id becomes a path component, where an empty one
// resolves to the log root.
//
// It also pins that the id the gate is handed is the EMPTY one rather than a
// partially-populated attribution: a decode that failed names no conversation, so
// a refusal must not be attributed to whatever prefix happened to parse.
func TestV2Session_RequestModelList_MalformedPayloadReachesNoResolver(t *testing.T) {
	t.Parallel()

	seams, counts := countingModelReqSeams()
	replies := sendModelListRequest(t, seams,
		[]string{protocol.CapabilityInteractive}, 74, `{"conversation_id":123}`)

	if len(replies) != 1 {
		t.Fatalf("got %d app frame(s), want exactly 1 reject", len(replies))
	}
	if n := counts.known.Load(); n != 1 {
		t.Errorf("KnownConversation consulted %d time(s), want 1 — the empty id is spent here", n)
	}
	if n := counts.resolves.Load(); n != 0 {
		t.Errorf("ModelListFor consulted %d time(s), want 0 — an id membership refused must reach nothing below the gate", n)
	}
}
