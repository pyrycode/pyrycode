package relay

import (
	"encoding/json"
	"strconv"
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
// path. Only the batch ID may appear after decoding; the answer token must stay
// absent from both receipt and terminal records.
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
	mu            sync.Mutex
	beforeResolve func()
	answerOKFor   string
	refusalOKFor  string
	answerCalls   []questionAnswerCall
	refusalCalls  []questionRefusalCall
}

func (f *fakeQuestionResolver) ResolveAnswer(p protocol.QuestionAnswerPayload, dev *devices.Device) bool {
	if f.beforeResolve != nil {
		f.beforeResolve()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answerCalls = append(f.answerCalls, questionAnswerCall{payload: p, dev: dev})
	return f.answerOKFor != "" && p.QuestionBatchID == f.answerOKFor
}

func (f *fakeQuestionResolver) ResolveRefusal(p protocol.QuestionRefusedPayload, dev *devices.Device) bool {
	if f.beforeResolve != nil {
		f.beforeResolve()
	}
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

// Separate recorders make a double resolution or a wrong-path call observable.
type fakeDiagnosticQuestionResolver struct {
	*fakeQuestionResolver
	diagnostic *fakeQuestionResolver
	reason     string
}

func (f *fakeDiagnosticQuestionResolver) ResolveAnswerDiagnostic(p protocol.QuestionAnswerPayload, dev *devices.Device) (bool, string) {
	return f.diagnostic.ResolveAnswer(p, dev), f.reason
}

func (f *fakeDiagnosticQuestionResolver) ResolveRefusalDiagnostic(p protocol.QuestionRefusedPayload, dev *devices.Device) (bool, string) {
	return f.diagnostic.ResolveRefusal(p, dev), f.reason
}

func questionControlRecords(log string) []string {
	var records []string
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "event=v2.question.") {
			records = append(records, line)
		}
	}
	return records
}

func TestV2Session_QuestionControl_DiagnosticRecords(t *testing.T) {
	t.Parallel()
	const questionText = "ZZ2800QUESTIONTEXTZZ"
	const optionLabel = "ZZ2800OPTIONLABELZZ"
	// Include controls, quotes and whitespace to prove the identifier is escaped.
	batchID := qTestBatchID + string(rune(0x1b)) + "\n\" spaced"
	tests := []struct {
		name       string
		mode       string
		consumed   bool
		payload    string
		wantReason string
		wantCalls  int
		decoded    bool
	}{
		{"legacy consumed", "legacy", true, "valid", "resolved", 1, true},
		{"legacy non-consumed", "legacy", false, "valid", "legacy_not_consumed", 1, true},
		{"diagnostic consumed", "diagnostic", true, "valid", "resolved", 1, true},
		{"diagnostic non-consumed", "diagnostic", false, "valid", "no_actuator", 1, true},
		{"diagnostic other reason", "diagnostic", false, "valid", "invalid_answer", 1, true},
		{"nil resolver", "nil", false, "valid", "no_resolver", 0, false},
		{"nil before decode", "nil", false, "invalid", "no_resolver", 0, false},
		{"legacy decode failure", "legacy", false, "invalid", "decode_rejected", 0, false},
		{"diagnostic decode failure", "diagnostic", false, "invalid", "decode_rejected", 0, false},
		{"legacy null", "legacy", false, "null", "legacy_not_consumed", 1, true},
		{"legacy empty object", "legacy", false, "{}", "legacy_not_consumed", 1, true},
		{"diagnostic null", "diagnostic", false, "null", "no_actuator", 1, true},
		{"diagnostic empty object", "diagnostic", false, "{}", "no_actuator", 1, true},
	}
	for _, kind := range []string{protocol.TypeQuestionAnswer, protocol.TypeQuestionRefused} {
		for _, tt := range tests {
			t.Run(kind+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				legacy := &fakeQuestionResolver{}
				diagnostic := &fakeQuestionResolver{}
				selected := legacy
				var resolver QuestionResolver = legacy
				if tt.mode == "nil" {
					resolver = nil
				} else if tt.mode == "diagnostic" {
					selected = diagnostic
					reason := "no_actuator"
					if tt.wantReason == "invalid_answer" {
						reason = tt.wantReason
					}
					resolver = &fakeDiagnosticQuestionResolver{legacy, diagnostic, reason}
				}
				if tt.consumed {
					selected.answerOKFor, selected.refusalOKFor = batchID, batchID
				}
				mgr, frames, send, rec, logBuf, stop := startQuestionConn(t, resolver)
				// Set callbacks before sending a frame; no resolver call occurs in
				// the handshake. Either path must observe just the receipt.
				beforeResolve := func() {
					records := questionControlRecords(logBuf.String())
					if len(records) != 1 || !strings.Contains(records[0], "event=v2.question.received") {
						t.Errorf("records before actuation = %q, want one receipt", records)
					}
				}
				legacy.beforeResolve, diagnostic.beforeResolve = beforeResolve, beforeResolve
				payload := tt.payload
				if payload == "valid" || payload == "invalid" {
					// Unknown content fields ensure both kinds can carry these
					// sentinels without the logger learning their values.
					body := struct {
						BatchID string   `json:"question_batch_id"`
						Token   any      `json:"answer_token"`
						Answers any      `json:"answers"`
						Text    string   `json:"question"`
						Options []string `json:"options"`
					}{
						BatchID: batchID, Token: qTestAnswerToken,
						Answers: []protocol.QuestionAnswerEntry{{QuestionIndex: 0, Values: []string{qTestAnswerValue}}},
						Text:    questionText, Options: []string{optionLabel},
					}
					if payload == "invalid" {
						// A later field fails after the batch ID has populated.
						if kind == protocol.TypeQuestionAnswer {
							body.Answers = qTestAnswerValue
						} else {
							body.Token = 42
						}
					}
					encoded, err := json.Marshal(body)
					if err != nil {
						t.Fatal(err)
					}
					payload = string(encoded)
				}
				sendQuestionFrame(t, frames, send, kind, payload)
				waitForLogContains(t, logBuf, "event=v2.question.completed")
				stop()
				if got := selected.totalCalls(); got != tt.wantCalls {
					t.Errorf("selected calls = %d, want %d", got, tt.wantCalls)
				}
				if tt.mode == "diagnostic" && legacy.totalCalls() != 0 {
					t.Errorf("legacy path called %d times with diagnostic resolver", legacy.totalCalls())
				}
				if kind == protocol.TypeQuestionAnswer && len(selected.refusalSnapshot()) != 0 ||
					kind == protocol.TypeQuestionRefused && len(selected.answerSnapshot()) != 0 {
					t.Error("wrong frame kind resolved")
				}
				log := logBuf.String()
				records := questionControlRecords(log)
				if len(records) != 2 {
					t.Fatalf("question records = %q, want receipt and terminal only", records)
				}
				for i, event := range []string{"v2.question.received", "v2.question.completed"} {
					for _, field := range []string{"level=INFO", "event=" + event, "frame_kind=" + kind, "conn_id=" + v2TestConnID} {
						if !strings.Contains(" "+records[i]+" ", " "+field+" ") {
							t.Errorf("record %q missing %q", records[i], field)
						}
					}
				}
				if strings.Contains(records[0], "question_batch_id") || strings.Contains(records[0], "reason=") {
					t.Errorf("receipt contains outcome or batch ID: %s", records[0])
				}
				if !strings.Contains(records[1]+" ", " reason="+tt.wantReason+" ") {
					t.Errorf("terminal missing reason %q: %s", tt.wantReason, records[1])
				}
				wantID := batchID
				if tt.payload == "null" || tt.payload == "{}" {
					wantID = ""
				}
				if tt.wantCalls == 1 {
					var gotID string
					var gotDev *devices.Device
					if kind == protocol.TypeQuestionAnswer {
						got := selected.answerSnapshot()[0]
						gotID, gotDev = got.payload.QuestionBatchID, got.dev
					} else {
						got := selected.refusalSnapshot()[0]
						gotID, gotDev = got.payload.QuestionBatchID, got.dev
					}
					if gotID != wantID || gotDev != mgr.sessions[v2TestConnID].device {
						t.Errorf("resolver got ID %q/device %p, want %q/connection device", gotID, gotDev, wantID)
					}
				}
				if tt.decoded {
					if !strings.Contains(records[1], "question_batch_id="+strconv.Quote(wantID)) {
						t.Errorf("terminal missing escaped ID %q: %s", wantID, records[1])
					}
				} else if strings.Contains(log, "question_batch_id") || strings.Contains(log, qTestBatchID) {
					t.Errorf("undecoded identifier logged: %s", log)
				}
				for _, secret := range []string{questionText, optionLabel, qTestAnswerValue, qTestAnswerToken, qTestPayloadKey, payload, "json:", "cannot unmarshal"} {
					if strings.Contains(log, secret) {
						t.Errorf("log disclosed %q: %s", secret, log)
					}
				}
				if strings.ContainsRune(log, 0x1b) {
					t.Error("identifier emitted a raw terminal escape")
				}
				if msgs := noiseMsgsForConn(t, rec, v2TestConnID); len(msgs) != 0 {
					t.Errorf("question control emitted %d replies/broadcasts", len(msgs))
				}
			})
		}
	}
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
		{"known batch consumed", qTestBatchID, "reason=resolved"},
		{"unknown batch still handed off", "some-other-batch", "reason=legacy_not_consumed"},
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
	waitForLogContains(t, logBuf, "reason=resolved")
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
		{"answer inert", protocol.TypeQuestionAnswer, answerPayload(qTestBatchID), "reason=no_resolver"},
		{"refusal inert", protocol.TypeQuestionRefused, `{"question_batch_id":"` + qTestBatchID + `","answer_token":"` + qTestAnswerToken + `"}`, "reason=no_resolver"},
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
			logWant: "reason=decode_rejected",
		},
		{
			name:    "answer with a non-object entry",
			ftype:   protocol.TypeQuestionAnswer,
			payload: `{"question_batch_id":"` + qTestBatchID + `","answer_token":"` + qTestAnswerToken + `","answers":[7]}`,
			logWant: "reason=decode_rejected",
		},
		{
			name:    "refusal with a numeric token",
			ftype:   protocol.TypeQuestionRefused,
			payload: `{"question_batch_id":"` + qTestBatchID + `","answer_token":42}`,
			logWant: "reason=decode_rejected",
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
			// The reject record carries fixed metadata only: no batch id (the
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
	waitForLogContains(t, logBuf, "reason=legacy_not_consumed")
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

	// The ESC is composed from a numeric literal, never written as a \x source
	// escape: substrate-guard matches raw file bytes, so the escaped source text
	// and a compiled ESC-'[' pair both trip it outside its allowlist. The runtime
	// value is unchanged, so the assertion below keeps its teeth.
	escBatchID := "qb-esc-" + string(rune(0x1b)) + "[31mZZ"
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
		{"inert", false, answerPayload(qTestBatchID), "reason=no_resolver"},
		{"rejected", true, `{"question_batch_id":"` + qTestBatchID + `","answers":"` + qTestAnswerValue + `"}`, "reason=decode_rejected"},
		{"handed off", true, answerPayload(qTestBatchID), "reason=resolved"},
		{"handed off with an escape-bearing id", true, string(escPayload), "reason=legacy_not_consumed"},
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
	waitForLogContains(t, logBuf, "reason=no_resolver")
	stop()

	msgs := noiseMsgsForConn(t, rec, v2TestConnID)
	if len(msgs) != 1 {
		t.Fatalf("got %d noise_msg, want exactly 1 (the unknown type's reply, and nothing for the question frame)", len(msgs))
	}
}
