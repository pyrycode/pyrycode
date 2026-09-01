package relay

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #1984 inbound question-control interception fixtures ---

// Sentinel values chosen so a substring scan of the log buffer cannot match them
// incidentally: an answer value and a payload key that MUST never be logged on any
// path, and the ids that MAY be (internal/protocol marks question_batch_id and
// answer_token safe to log, expressly so this handler invents no redaction rule).
const (
	qTestBatchID     = "qb-ZZ1984BATCHZZ"
	qTestAnswerToken = "tok-ZZ1984TOKENZZ"
	qTestAnswerValue = "ZZ1984ANSWERVALUEZZ"
	qTestPayloadKey  = "question_index" // a raw payload key; never a log field
)

// questionAnswerCall / questionRefusalCall capture one QuestionResolver call so a
// test can assert what the manager routed across the seam. The whole decoded
// payload is retained, not selected fields: the nested answers array is precisely
// what a tolerant decode would have silently emptied.
type questionAnswerCall struct {
	payload protocol.QuestionAnswerPayload
	dev     *devices.Device
}

type questionRefusalCall struct {
	payload protocol.QuestionRefusedPayload
	dev     *devices.Device
}

// fakeQuestionResolver is a relay-side test double for QuestionResolver: it
// records every call and reports consumed=true only for the configured
// answerOKFor / refusalOKFor batch id (any other id ⇒ the unknown-batch no-op).
// The mutex guards the cross-goroutine read — the Run goroutine writes, the test
// goroutine reads — mirroring fakeModalResolver.
type fakeQuestionResolver struct {
	mu           sync.Mutex
	answerOKFor  string
	refusalOKFor string
	answerCalls  []questionAnswerCall
	refusalCalls []questionRefusalCall
}

func (f *fakeQuestionResolver) ResolveAnswer(p protocol.QuestionAnswerPayload, dev *devices.Device) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answerCalls = append(f.answerCalls, questionAnswerCall{payload: p, dev: dev})
	return f.answerOKFor != "" && p.QuestionBatchID == f.answerOKFor
}

func (f *fakeQuestionResolver) ResolveRefusal(p protocol.QuestionRefusedPayload, dev *devices.Device) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refusalCalls = append(f.refusalCalls, questionRefusalCall{payload: p, dev: dev})
	return f.refusalOKFor != "" && p.QuestionBatchID == f.refusalOKFor
}

func (f *fakeQuestionResolver) answerSnapshot() []questionAnswerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]questionAnswerCall(nil), f.answerCalls...)
}

func (f *fakeQuestionResolver) refusalSnapshot() []questionRefusalCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]questionRefusalCall(nil), f.refusalCalls...)
}

// totalCalls is the synchronisation knob for the arms that route through the seam
// but emit no outbound envelope (all of them — this slice never replies).
func (f *fakeQuestionResolver) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.answerCalls) + len(f.refusalCalls)
}

// startQuestionConn stands up a manager with res wired (nil for the unwired
// posture), drives one paired interactive handshake, and returns the manager, the
// initiator's send CipherState, the recorder, the log buffer and the stop func.
// Every question test drives a real handshake and a real AEAD-sealed frame, so the
// interception is proven through dispatchAppFrame rather than by calling a handler
// directly.
func startQuestionConn(t *testing.T, res QuestionResolver) (*V2SessionManager, chan protocol.RoutingEnvelope, *noise.CipherState, *v2Recorder, *syncLogBuffer, func()) {
	t.Helper()
	respPriv, respPub := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	logger, logBuf := bufferLogger()
	frames := make(chan protocol.RoutingEnvelope, 4)
	rec := &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:           frames,
		Outbound:         rec.outbound,
		StaticPriv:       respPriv,
		Devices:          reg,
		ServerID:         v2TestServerID,
		Logger:           logger,
		QuestionResolver: res,
	})
	t.Cleanup(stop)
	send, _ := openModalConn(t, mgr, frames, rec, respPub, v2TestConnID, []string{protocol.CapabilityInteractive})
	return mgr, frames, send, rec, logBuf, stop
}

// sendQuestionFrame seals one control envelope under the initiator's send state
// and hands it to the manager's Frames channel. Envelope IDs are non-load-bearing
// here: this slice never replies, so nothing correlates on them.
func sendQuestionFrame(t *testing.T, frames chan protocol.RoutingEnvelope, send *noise.CipherState, ftype, payload string) {
	t.Helper()
	frames <- sealAppFrameConn(t, send, v2TestConnID, protocol.Envelope{
		ID:      31,
		Type:    ftype,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(payload),
	})
}

// answerPayload builds a well-formed question_answer body carrying two entries,
// one of them multi-value, so the nested array's survival across the seam is
// actually pinned rather than assumed from a single flat entry.
func answerPayload(batchID string) string {
	p, err := json.Marshal(protocol.QuestionAnswerPayload{
		QuestionBatchID: batchID,
		AnswerToken:     qTestAnswerToken,
		Answers: []protocol.QuestionAnswerEntry{
			{QuestionIndex: 0, Values: []string{qTestAnswerValue}},
			{QuestionIndex: 1, Values: []string{qTestAnswerValue + "-a", qTestAnswerValue + "-b"}},
		},
	})
	if err != nil {
		panic(err) // closed struct of strings/ints; cannot fail
	}
	return string(p)
}

// TestV2Session_QuestionAnswer_ReachesResolver drives a well-formed
// question_answer through the real Frames/Run loop and asserts the manager decodes
// it and hands the WHOLE payload plus the connection's device to the seam, emits
// no reply, and leaves the session open. Both arms — a batch the resolver consumes
// and one it does not — route identically through the handler: an unknown batch is
// the seam's to judge, not this handler's (AC #3).
func TestV2Session_QuestionAnswer_ReachesResolver(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		okFor       string
		wantLogEvnt string
	}{
		{"known batch consumed", qTestBatchID, "event=v2.question.answer.resolved"},
		{"unknown batch still handed off", "some-other-batch", "event=v2.question.answer.noop"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res := &fakeQuestionResolver{answerOKFor: tt.okFor}
			mgr, frames, send, rec, logBuf, stop := startQuestionConn(t, res)

			sendQuestionFrame(t, frames, send, protocol.TypeQuestionAnswer, answerPayload(qTestBatchID))
			waitForResolverCall(t, res.totalCalls, 1, "ResolveAnswer")
			waitForLogContains(t, logBuf, tt.wantLogEvnt)
			stop()

			calls := res.answerSnapshot()
			if len(calls) != 1 {
				t.Fatalf("ResolveAnswer calls = %d, want 1", len(calls))
			}
			if len(res.refusalSnapshot()) != 0 {
				t.Errorf("ResolveRefusal called %d times on a question_answer, want 0", len(res.refusalSnapshot()))
			}
			got := calls[0]
			if got.payload.QuestionBatchID != qTestBatchID {
				t.Errorf("QuestionBatchID = %q, want %q", got.payload.QuestionBatchID, qTestBatchID)
			}
			if got.payload.AnswerToken != qTestAnswerToken {
				t.Errorf("AnswerToken = %q, want %q", got.payload.AnswerToken, qTestAnswerToken)
			}
			// The nested array is the whole point of rejecting a tolerant decode:
			// pin both entries and the multi-value one's ordered values.
			if len(got.payload.Answers) != 2 {
				t.Fatalf("Answers len = %d, want 2 (nested array lost in transit)", len(got.payload.Answers))
			}
			if got.payload.Answers[0].QuestionIndex != 0 || len(got.payload.Answers[0].Values) != 1 ||
				got.payload.Answers[0].Values[0] != qTestAnswerValue {
				t.Errorf("Answers[0] = %+v, want index 0 with the single sentinel value", got.payload.Answers[0])
			}
			if got.payload.Answers[1].QuestionIndex != 1 || len(got.payload.Answers[1].Values) != 2 ||
				got.payload.Answers[1].Values[0] != qTestAnswerValue+"-a" ||
				got.payload.Answers[1].Values[1] != qTestAnswerValue+"-b" {
				t.Errorf("Answers[1] = %+v, want index 1 with both ordered values", got.payload.Answers[1])
			}
			// The device that crossed is THIS connection's, not merely non-nil.
			// Read after stop(), like TestV2Session_ModalControl_NilResolver does.
			s := mgr.sessions[v2TestConnID]
			if s == nil {
				t.Fatal("session missing after stop")
			}
			if got.dev != s.device {
				t.Errorf("device across seam = %p, want the conn's device %p", got.dev, s.device)
			}
			if msgs := noiseMsgsForConn(t, rec, v2TestConnID); len(msgs) != 0 {
				t.Errorf("question_answer drew %d noise_msg, want 0 (no reply, no broadcast)", len(msgs))
			}
			if s.State() != V2StateOpen {
				t.Errorf("session state after question_answer = %v, want V2StateOpen", s.State())
			}
		})
	}
}

// TestV2Session_QuestionRefusal_ReachesResolver is the refusal twin: both ids and
// the device cross, the answer arm is untouched, and nothing is replied.
func TestV2Session_QuestionRefusal_ReachesResolver(t *testing.T) {
	t.Parallel()

	res := &fakeQuestionResolver{refusalOKFor: qTestBatchID}
	mgr, frames, send, rec, logBuf, stop := startQuestionConn(t, res)

	body, err := json.Marshal(protocol.QuestionRefusedPayload{
		QuestionBatchID: qTestBatchID,
		AnswerToken:     qTestAnswerToken,
	})
	if err != nil {
		t.Fatalf("marshal refusal: %v", err)
	}
	sendQuestionFrame(t, frames, send, protocol.TypeQuestionRefused, string(body))
	waitForResolverCall(t, res.totalCalls, 1, "ResolveRefusal")
	waitForLogContains(t, logBuf, "event=v2.question.refusal.resolved")
	stop()

	calls := res.refusalSnapshot()
	if len(calls) != 1 {
		t.Fatalf("ResolveRefusal calls = %d, want 1", len(calls))
	}
	if n := len(res.answerSnapshot()); n != 0 {
		t.Errorf("ResolveAnswer called %d times on a question_refused, want 0", n)
	}
	got := calls[0]
	if got.payload.QuestionBatchID != qTestBatchID {
		t.Errorf("QuestionBatchID = %q, want %q", got.payload.QuestionBatchID, qTestBatchID)
	}
	if got.payload.AnswerToken != qTestAnswerToken {
		t.Errorf("AnswerToken = %q, want %q", got.payload.AnswerToken, qTestAnswerToken)
	}
	s := mgr.sessions[v2TestConnID]
	if s == nil {
		t.Fatal("session missing after stop")
	}
	if got.dev != s.device {
		t.Errorf("device across seam = %p, want the conn's device %p", got.dev, s.device)
	}
	if msgs := noiseMsgsForConn(t, rec, v2TestConnID); len(msgs) != 0 {
		t.Errorf("question_refused drew %d noise_msg, want 0", len(msgs))
	}
}

// TestV2Session_QuestionControl_NilResolver pins the behaviour change this slice
// delivers on its own: with NOTHING wired behind the seam, both frames are still
// consumed by the interception, so neither reaches dispatch.Route and neither
// draws the unknown-type error reply it drew before this ticket. Zero outbound
// noise_msg is that assertion.
func TestV2Session_QuestionControl_NilResolver(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ftype   string
		payload string
		logWant string
	}{
		{"answer inert", protocol.TypeQuestionAnswer, answerPayload(qTestBatchID), "event=v2.question.answer.inert"},
		{"refusal inert", protocol.TypeQuestionRefused, `{"question_batch_id":"` + qTestBatchID + `","answer_token":"` + qTestAnswerToken + `"}`, "event=v2.question.refusal.inert"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mgr, frames, send, rec, logBuf, stop := startQuestionConn(t, nil)

			sendQuestionFrame(t, frames, send, tt.ftype, tt.payload)
			waitForLogContains(t, logBuf, tt.logWant)
			stop()

			if msgs := noiseMsgsForConn(t, rec, v2TestConnID); len(msgs) != 0 {
				t.Errorf("unwired %s drew %d noise_msg, want 0 (no unknown-type reply)", tt.ftype, len(msgs))
			}
			if s := mgr.sessions[v2TestConnID]; s == nil || s.State() != V2StateOpen {
				t.Errorf("session state after unwired %s not V2StateOpen", tt.ftype)
			}
		})
	}
}

// TestV2Session_QuestionControl_DecodeFailure_Rejected is the arm the modal
// handlers' tolerant `_ = json.Unmarshal` would fail. Each fixture is well-formed
// JSON whose keys decode IN ORDER, so the decoder populates question_batch_id
// FIRST and only then fails — the partial-population hazard QuestionAnswerPayload's
// doc block names. The binding assertion is the CALL COUNT: a tolerant decode
// would still hand the seam a valid batch id with nil answers, which is exactly
// the empty-but-successful answer the contract forbids, and only "called zero
// times" rules it out. The reject record must not carry the partially-decoded id
// either — those bytes came from a frame that was refused.
func TestV2Session_QuestionControl_DecodeFailure_Rejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ftype   string
		payload string
		logWant string
	}{
		{
			name:    "answer with a non-array answers",
			ftype:   protocol.TypeQuestionAnswer,
			payload: `{"question_batch_id":"` + qTestBatchID + `","answer_token":"` + qTestAnswerToken + `","answers":"` + qTestAnswerValue + `"}`,
			logWant: "event=v2.question.answer.decode_err",
		},
		{
			name:    "answer with a non-object entry",
			ftype:   protocol.TypeQuestionAnswer,
			payload: `{"question_batch_id":"` + qTestBatchID + `","answer_token":"` + qTestAnswerToken + `","answers":[7]}`,
			logWant: "event=v2.question.answer.decode_err",
		},
		{
			name:    "refusal with a numeric token",
			ftype:   protocol.TypeQuestionRefused,
			payload: `{"question_batch_id":"` + qTestBatchID + `","answer_token":42}`,
			logWant: "event=v2.question.refusal.decode_err",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res := &fakeQuestionResolver{answerOKFor: qTestBatchID, refusalOKFor: qTestBatchID}
			mgr, frames, send, rec, logBuf, stop := startQuestionConn(t, res)

			sendQuestionFrame(t, frames, send, tt.ftype, tt.payload)
			waitForLogContains(t, logBuf, tt.logWant)
			stop()

			if n := res.totalCalls(); n != 0 {
				t.Errorf("resolver called %d times on an undecodable %s, want 0", n, tt.ftype)
			}
			if msgs := noiseMsgsForConn(t, rec, v2TestConnID); len(msgs) != 0 {
				t.Errorf("rejected %s drew %d noise_msg, want 0 (nothing echoed)", tt.ftype, len(msgs))
			}
			// The reject record carries event + conn_id only: no batch id (the
			// decode is what failed, so it is not trustworthy), no payload byte,
			// and no wrapped json error — encoding/json quotes offending input
			// into its error string, and those bytes are remote-authored.
			log := logBuf.String()
			if strings.Contains(log, qTestBatchID) {
				t.Errorf("reject log carries the partially-decoded batch id; got:\n%s", log)
			}
			if strings.Contains(log, qTestAnswerValue) {
				t.Errorf("reject log carries a payload value; got:\n%s", log)
			}
			if s := mgr.sessions[v2TestConnID]; s == nil || s.State() != V2StateOpen {
				t.Errorf("session state after a rejected %s not V2StateOpen", tt.ftype)
			}
		})
	}
}

// TestV2Session_QuestionControl_NullPayload_NotJudgedHere pins the sharpest edge
// of the reject rule. A `"payload":null` DECODES CLEANLY — encoding/json leaves the
// struct untouched and returns no error — so it is not a decode failure and must
// not be rejected as one. It reaches the seam as a zero-value payload whose empty
// batch id is an UNKNOWN batch, and AC #3 makes an unknown batch the seam
// implementer's to judge, not this handler's. Adding an empty-id reject here would
// install exactly the second arbiter that forbids.
//
// It is also the one shape that is genuinely unreachable as a decode failure
// through this harness: json.Marshal validates a json.RawMessage, so an envelope
// carrying malformed payload bytes cannot be constructed, and every reachable
// failure is a type mismatch inside valid JSON — which is the partial-population
// hazard the sibling test covers.
func TestV2Session_QuestionControl_NullPayload_NotJudgedHere(t *testing.T) {
	t.Parallel()

	res := &fakeQuestionResolver{answerOKFor: qTestBatchID}
	_, frames, send, rec, logBuf, stop := startQuestionConn(t, res)

	sendQuestionFrame(t, frames, send, protocol.TypeQuestionAnswer, `null`)
	waitForResolverCall(t, res.totalCalls, 1, "ResolveAnswer(null payload)")
	waitForLogContains(t, logBuf, "event=v2.question.answer.noop")
	stop()

	calls := res.answerSnapshot()
	if len(calls) != 1 {
		t.Fatalf("ResolveAnswer calls = %d, want 1 (a null payload is not a decode failure)", len(calls))
	}
	if got := calls[0].payload.QuestionBatchID; got != "" {
		t.Errorf("QuestionBatchID = %q, want %q", got, "")
	}
	if got := calls[0].payload.Answers; got != nil {
		t.Errorf("Answers = %+v, want nil", got)
	}
	if msgs := noiseMsgsForConn(t, rec, v2TestConnID); len(msgs) != 0 {
		t.Errorf("null-payload question_answer drew %d noise_msg, want 0", len(msgs))
	}
}

// TestV2Session_QuestionControl_LogsCarryNoPayload sweeps AC #4 across all three
// paths in one place — inert, rejected, handed off — for an answer value and for a
// raw payload key that must never become a log field. It also pins the escaping of
// question_batch_id, which internal/protocol marks safe to log and which is the
// first log field on an inbound handler whose BYTES an attacker fully controls: a
// batch id carrying a terminal escape must not reach a log reader's terminal raw.
func TestV2Session_QuestionControl_LogsCarryNoPayload(t *testing.T) {
	t.Parallel()

	escBatchID := "qb-esc-\x1b[31mZZ"
	escPayload, err := json.Marshal(protocol.QuestionAnswerPayload{
		QuestionBatchID: escBatchID,
		AnswerToken:     qTestAnswerToken,
		Answers:         []protocol.QuestionAnswerEntry{{QuestionIndex: 3, Values: []string{qTestAnswerValue}}},
	})
	if err != nil {
		t.Fatalf("marshal escape-bearing answer: %v", err)
	}

	tests := []struct {
		name    string
		wired   bool
		payload string
		logWant string
	}{
		{"inert", false, answerPayload(qTestBatchID), "event=v2.question.answer.inert"},
		{"rejected", true, `{"question_batch_id":"` + qTestBatchID + `","answers":"` + qTestAnswerValue + `"}`, "event=v2.question.answer.decode_err"},
		{"handed off", true, answerPayload(qTestBatchID), "event=v2.question.answer.resolved"},
		{"handed off with an escape-bearing id", true, string(escPayload), "event=v2.question.answer.noop"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var res QuestionResolver
			if tt.wired {
				res = &fakeQuestionResolver{answerOKFor: qTestBatchID}
			}
			_, frames, send, _, logBuf, stop := startQuestionConn(t, res)

			sendQuestionFrame(t, frames, send, protocol.TypeQuestionAnswer, tt.payload)
			waitForLogContains(t, logBuf, tt.logWant)
			stop()

			log := logBuf.String()
			if strings.Contains(log, qTestAnswerValue) {
				t.Errorf("log carries an answer value on the %s path; got:\n%s", tt.name, log)
			}
			if strings.Contains(log, qTestPayloadKey) {
				t.Errorf("log carries the raw payload key %q on the %s path; got:\n%s", qTestPayloadKey, tt.name, log)
			}
			if strings.ContainsRune(log, 0x1b) {
				t.Errorf("log carries a raw ESC byte on the %s path; got:\n%q", tt.name, log)
			}
		})
	}
}

// TestV2Session_QuestionControl_OtherFramesUnchanged proves the interception is
// two named cases and not a blanket default arm: an unrelated unknown type on the
// SAME conn still falls through to dispatch.Route and still draws its sealed
// unknown-type error reply, while the question frame beside it draws none. The
// unknown type is routed on the per-conn app-frame worker and the question frame
// inline on Run, so the two are awaited independently rather than assumed ordered.
func TestV2Session_QuestionControl_OtherFramesUnchanged(t *testing.T) {
	t.Parallel()

	_, frames, send, rec, logBuf, stop := startQuestionConn(t, nil)

	sendQuestionFrame(t, frames, send, "totally_unknown_type_1984", `{}`)
	sendQuestionFrame(t, frames, send, protocol.TypeQuestionAnswer, answerPayload(qTestBatchID))

	// noise_resp + exactly one sealed error reply.
	waitForEnvelopes(t, rec, 2)
	waitForLogContains(t, logBuf, "event=v2.question.answer.inert")
	stop()

	msgs := noiseMsgsForConn(t, rec, v2TestConnID)
	if len(msgs) != 1 {
		t.Fatalf("got %d noise_msg, want exactly 1 (the unknown type's reply, and nothing for the question frame)", len(msgs))
	}
}
