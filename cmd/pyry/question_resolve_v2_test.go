package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/questionbridge"
)

// The question-resolution tests (#1986) run over the #1973 question fixtures in
// stream_approval_test.go — newQuestionFixture, surfacedQuestion, goodAnswers,
// updatedInput, waitPush, lastQuestionShown, lastQuestionDismissed, questionLen —
// and the #703 audit/device helpers in modal_resolve_v2_test.go. Nothing new is
// introduced here beyond the resolver's own wiring and the two payload builders.

// testAnswerToken is the client-minted idempotency key both inbound payloads
// carry. It doubles as a leak sentinel: the resolver never reads it, so it must
// never reach a log field or an audit record.
const testAnswerToken = "ANSWER-TOKEN-5555"

// gatedResolver assembles the wiring the composition root builds: a resolver over
// the fixture's batch registry (a constructor parameter, since questionReg exists
// before the manager) with the fixture's bridge as its actuator (assigned after
// construction, since the bridge does not).
func gatedResolver(f questionFixture, logger *slog.Logger) *questionResolverV2 {
	r := newQuestionResolverV2(f.qreg, logger)
	r.bridge = f.bridge
	return r
}

func answerPayload(batchID string, answers []protocol.QuestionAnswerEntry) protocol.QuestionAnswerPayload {
	return protocol.QuestionAnswerPayload{QuestionBatchID: batchID, AnswerToken: testAnswerToken, Answers: answers}
}

func refusalPayload(batchID string) protocol.QuestionRefusedPayload {
	return protocol.QuestionRefusedPayload{QuestionBatchID: batchID, AnswerToken: testAnswerToken}
}

// assertOneRecord requires the audit trail to hold EXACTLY one record, naming
// batchID under the question class with the given outcome. The single-record half
// carries as much as the outcome does: a path that recorded twice would report one
// security decision as two to a forensic reader.
func assertOneRecord(t *testing.T, buf *bytes.Buffer, batchID, outcome string) {
	t.Helper()
	recs := auditRecords(t, buf)
	if len(recs) != 1 {
		t.Fatalf("audit records = %v, want exactly one for one security decision", recs)
	}
	rec := recs[0]
	if rec["modal_id"] != batchID || rec["modal_class"] != classQuestion || rec["outcome"] != outcome || rec["source"] != "remote" {
		t.Errorf("audit record = %v, want {modal_id:%q modal_class:%q outcome:%q source:remote}",
			rec, batchID, classQuestion, outcome)
	}
}

// questionArms drives the two inbound frames through one table, so every property
// asserted below is asserted for BOTH arms. A row that held for the answer and not
// the refusal is exactly the drift the shared gate helper exists to prevent.
var questionArms = []struct {
	name string
	call func(r *questionResolverV2, batchID string, dev *devices.Device) bool
}{
	{"answer", func(r *questionResolverV2, batchID string, dev *devices.Device) bool {
		return r.ResolveAnswer(answerPayload(batchID, goodAnswers()), dev)
	}},
	{"refusal", func(r *questionResolverV2, batchID string, dev *devices.Device) bool {
		return r.ResolveRefusal(refusalPayload(batchID), dev)
	}},
}

// AC-1, the answer arm: an answer from a device allowed to answer remote prompts
// resolves the parked approval to claude's answer verdict and sends exactly one
// question_dismissed.
//
// The verdict is read off the parked approval's OWN handle, so what is asserted is
// what claude receives rather than what this file believes was sent. The audit
// record is asserted here too, because "the gate admitted this device" and "the
// decision was recorded" are one event: a resolution that actuated without
// recording would pass every other assertion in this test.
func TestQuestionResolverV2_Answer_GatedDeviceResolvesToClaudesVerdict(t *testing.T) {
	t.Parallel()

	logger, logBuf := auditLogger()
	f := newQuestionFixture(t, testConvID, logger)
	_, pending, batchID := surfacedQuestion(t, f, multiQuestionText)
	r := gatedResolver(f, logger)

	if !r.ResolveAnswer(answerPayload(batchID, goodAnswers()), eligibleDevice(t)) {
		t.Fatal("ResolveAnswer = false for an outstanding batch and a gated device")
	}

	v := pending.Await()
	if v.Behavior != permbridge.BehaviorAllow {
		t.Fatalf("verdict behavior = %q, want allow", v.Behavior)
	}
	if _, answers := updatedInput(t, v); len(answers) != 2 {
		t.Errorf("answers has %d keys, want one per parked question", len(answers))
	}

	waitPush(t, f.pushed)
	if got := pushTypes(f.bcast.pushes); len(got) != 1 || got[0] != protocol.TypeQuestionDismissed {
		t.Fatalf("pushes = %v, want exactly one question_dismissed", got)
	}
	d := lastQuestionDismissed(t, f.bcast.pushes)
	if d.QuestionBatchID != batchID || d.Outcome != outcomeQuestionAnswered || d.Source != sourceQuestionRemote {
		t.Errorf("dismissal = {%q %q %q}, want {%q %q %q}",
			d.QuestionBatchID, d.Outcome, d.Source, batchID, outcomeQuestionAnswered, sourceQuestionRemote)
	}
	if _, ok := f.qreg.Lookup(batchID); ok {
		t.Error("batch still outstanding; a gated answer must consume the one-shot")
	}
	assertOneRecord(t, logBuf, batchID, "allowed")
}

// AC-1, the refusal arm: a refusal from a gated device resolves the parked
// approval to the refusal deny and sends exactly one question_dismissed.
//
// The deny message is compared against the daemon constant AND asserted to tell
// claude to wait: an edit trimming it to a bare "denied" satisfies the equality
// check while letting claude guess an answer and carry on, which is the failure the
// message exists to stop.
func TestQuestionResolverV2_Refusal_GatedDeviceResolvesToTheRefusalDeny(t *testing.T) {
	t.Parallel()

	logger, logBuf := auditLogger()
	f := newQuestionFixture(t, testConvID, logger)
	_, pending, batchID := surfacedQuestion(t, f, multiQuestionText)
	r := gatedResolver(f, logger)

	if !r.ResolveRefusal(refusalPayload(batchID), eligibleDevice(t)) {
		t.Fatal("ResolveRefusal = false for an outstanding batch and a gated device")
	}

	v := pending.Await()
	if v.Behavior != permbridge.BehaviorDeny {
		t.Fatalf("verdict behavior = %q, want deny", v.Behavior)
	}
	if v.Message != reasonQuestionRefused {
		t.Errorf("deny message = %q, want the daemon constant", v.Message)
	}
	if !strings.Contains(reasonQuestionRefused, "wait") {
		t.Error("the deny message must tell claude to wait for the user's message")
	}

	waitPush(t, f.pushed)
	if got := pushTypes(f.bcast.pushes); len(got) != 1 || got[0] != protocol.TypeQuestionDismissed {
		t.Fatalf("pushes = %v, want exactly one question_dismissed", got)
	}
	d := lastQuestionDismissed(t, f.bcast.pushes)
	if d.QuestionBatchID != batchID || d.Outcome != outcomeQuestionRefused || d.Source != sourceQuestionRemote {
		t.Errorf("dismissal = {%q %q %q}, want {%q %q %q}",
			d.QuestionBatchID, d.Outcome, d.Source, batchID, outcomeQuestionRefused, sourceQuestionRemote)
	}
	if _, ok := f.qreg.Lookup(batchID); ok {
		t.Error("batch still outstanding; a gated refusal must consume the one-shot")
	}
	assertOneRecord(t, logBuf, batchID, "denied")
}

// AC-2: ineligibility is decided BEFORE the batch is consumed. A device with the
// opt-in bit unset and a connection with no authenticated device at all both deny,
// on both arms.
//
// "Left outstanding" is asserted in the form that matters — STILL ANSWERABLE, not
// merely still present. Each row drives a legitimate gated answer through the same
// fixture afterwards and requires it to resolve, so an implementation that consumed
// the one-shot and then discovered the device was ineligible fails here even though
// the registry entry would look right to a membership check taken alone.
func TestQuestionResolverV2_GateDeniesBeforeConsume(t *testing.T) {
	t.Parallel()

	devs := []struct {
		name string
		dev  func(*testing.T) *devices.Device
	}{
		{"opt-in unset", func(t *testing.T) *devices.Device { return testDevice(t) }},
		{"no authenticated device", func(*testing.T) *devices.Device { return nil }},
	}
	for _, arm := range questionArms {
		for _, dc := range devs {
			t.Run(arm.name+"/"+dc.name, func(t *testing.T) {
				t.Parallel()

				logger, logBuf := auditLogger()
				f := newQuestionFixture(t, testConvID, logger)
				_, pending, batchID := surfacedQuestion(t, f, multiQuestionText)
				r := gatedResolver(f, logger)

				if arm.call(r, batchID, dc.dev(t)) {
					t.Error("resolution = true for a device that may not answer remote prompts")
				}
				if _, ok := f.perm.Lookup("tu-q1"); !ok {
					t.Error("claude's parked call was resolved by an ineligible device (the gate must precede the verdict)")
				}
				if _, ok := f.qreg.Lookup(batchID); !ok {
					t.Fatal("batch consumed despite the gate denial (the gate must precede the consume)")
				}
				if got := pushTypes(f.bcast.pushes); len(got) != 0 {
					t.Errorf("pushes = %v, want none; a denied frame dismisses nothing", got)
				}
				assertOneRecord(t, logBuf, batchID, "denied_unauthorized")

				// The half a membership check cannot carry: the batch is still
				// ANSWERABLE, so the legitimate operator — or the no-answer
				// backstop — can still resolve it.
				if !r.ResolveAnswer(answerPayload(batchID, goodAnswers()), eligibleDevice(t)) {
					t.Fatal("the batch left outstanding by the gate is no longer answerable")
				}
				if v := pending.Await(); v.Behavior != permbridge.BehaviorAllow {
					t.Errorf("verdict behavior = %q, want allow", v.Behavior)
				}
				waitPush(t, f.pushed)
			})
		}
	}
}

// AC-3: every security decision writes exactly one audit record, the three are
// distinguishable, each names the batch and the answering device's non-secret
// identity, and none carries a byte of the batch, the answer or the token.
//
// Three fixtures share ONE logger, so the buffer holds all three records together
// and the distinctness claim is made against what a forensic reader actually sees
// rather than against three separate runs. FOUR claude-authored sentinels (the
// input carries all four, so an input-wide sentinel would pass while a field echoed
// only the header or only a label), plus both answer values and the answer token.
func TestQuestionResolverV2_AuditRecordsAreDistinguishableAndContentFree(t *testing.T) {
	t.Parallel()

	const (
		secretText    = "SECRET-QUESTION-TEXT-1111"
		secretHeader  = "SECRET-HEADER-2222"
		secretLabel   = "SECRET-LABEL-3333"
		secretDesc    = "SECRET-DESCRIPTION-4444"
		secretAnswerA = "SECRET-ANSWER-VALUE-6666"
		secretAnswerB = "SECRET-ANSWER-VALUE-7777"
	)
	secretAnswers := []protocol.QuestionAnswerEntry{
		{QuestionIndex: 0, Values: []string{secretAnswerA}},
		{QuestionIndex: 1, Values: []string{secretAnswerB}},
	}

	logger, logBuf := auditLogger()

	// surfacedSecret parks and surfaces one batch whose four claude-authored
	// strings are the leak sentinels, on its own fixture (a consumed batch cannot
	// be reused, and each arm consumes one).
	surfacedSecret := func() (questionFixture, string) {
		f := newQuestionFixture(t, testConvID, logger)
		req, _ := parkApproval(t, f.perm, "tu-q1", questionbridge.ToolName,
			questionInput(t, secretText, secretHeader, secretLabel, secretDesc))
		f.bridge.Surface(req)
		batchID := lastQuestionShown(t, f.bcast.pushes).QuestionBatchID
		f.isolate()
		return f, batchID
	}

	allowed, allowedID := surfacedSecret()
	denied, deniedID := surfacedSecret()
	unauthorized, unauthorizedID := surfacedSecret()

	dev := eligibleDevice(t)
	if !gatedResolver(allowed, logger).ResolveAnswer(answerPayload(allowedID, secretAnswers), dev) {
		t.Fatal("ResolveAnswer = false for an outstanding batch and a gated device")
	}
	waitPush(t, allowed.pushed)
	if !gatedResolver(denied, logger).ResolveRefusal(refusalPayload(deniedID), dev) {
		t.Fatal("ResolveRefusal = false for an outstanding batch and a gated device")
	}
	waitPush(t, denied.pushed)
	if gatedResolver(unauthorized, logger).ResolveAnswer(answerPayload(unauthorizedID, secretAnswers), testDevice(t)) {
		t.Error("ResolveAnswer = true for a device that may not answer remote prompts")
	}

	recs := auditRecords(t, logBuf)
	if len(recs) != 3 {
		t.Fatalf("audit records = %d, want exactly one per security decision: %v", len(recs), recs)
	}
	want := map[string]string{allowedID: "allowed", deniedID: "denied", unauthorizedID: "denied_unauthorized"}
	seen := map[string]bool{}
	for _, rec := range recs {
		batchID, _ := rec["modal_id"].(string)
		outcome, _ := rec["outcome"].(string)
		if want[batchID] == "" {
			t.Fatalf("audit record names %q, which is none of the three batches: %v", batchID, rec)
		}
		if outcome != want[batchID] {
			t.Errorf("batch %s recorded outcome %q, want %q", batchID, outcome, want[batchID])
		}
		if seen[outcome] {
			t.Errorf("outcome %q recorded twice; the three decisions must be distinguishable", outcome)
		}
		seen[outcome] = true
		if rec["modal_class"] != classQuestion {
			t.Errorf("modal_class = %v, want %q so a forensic reader can tell a question from a modal", rec["modal_class"], classQuestion)
		}
		if rec["source"] != "remote" {
			t.Errorf("source = %v, want remote", rec["source"])
		}
		if rec["device_hash"] != dev.TokenHash || rec["device_label"] != dev.Name {
			t.Errorf("identity = {%v %v}, want the device's non-secret hash + label {%q %q}",
				rec["device_hash"], rec["device_label"], dev.TokenHash, dev.Name)
		}
	}

	for _, secret := range []string{secretText, secretHeader, secretLabel, secretDesc, secretAnswerA, secretAnswerB, testAnswerToken} {
		if strings.Contains(logBuf.String(), secret) {
			t.Errorf("the audit trail carries %q; no question text, answer value, option label or token may reach it", secret)
		}
	}
}

// AC-4: a frame naming an unknown or already-resolved batch is inert on every
// count — no verdict, no broadcast, no audit — because no security decision was
// made. Driven with a GATED device, so what the rows pin is the lookup ordering
// and not the gate: an implementation that gated first would write a record for the
// ineligible case and pass a version of this test taken with an ungated device.
func TestQuestionResolverV2_UnknownBatchIsInert(t *testing.T) {
	t.Parallel()

	rows := []struct {
		name      string
		runRetire bool // let the no-answer backstop consume the batch first
		batchID   func(surfaced string) string
	}{
		{name: "backstop already consumed", runRetire: true, batchID: func(s string) string { return s }},
		{name: "unknown batch id", batchID: func(string) string { return "never-surfaced" }},
		{name: "empty batch id", batchID: func(string) string { return "" }},
	}
	for _, arm := range questionArms {
		for _, row := range rows {
			t.Run(arm.name+"/"+row.name, func(t *testing.T) {
				t.Parallel()

				logger, logBuf := auditLogger()
				f := newQuestionFixture(t, testConvID, logger)
				req, _ := parkApproval(t, f.perm, "tu-q1", questionbridge.ToolName,
					questionInput(t, multiQuestionText, "Write strategy", "rewrite", "replace the file wholesale"))
				retire := f.bridge.Surface(req)
				surfaced := lastQuestionShown(t, f.bcast.pushes).QuestionBatchID
				if row.runRetire {
					retire()
				}
				f.isolate()

				if arm.call(gatedResolver(f, logger), row.batchID(surfaced), eligibleDevice(t)) {
					t.Error("resolution = true; there was no outstanding batch to consume")
				}
				if got := pushTypes(f.bcast.pushes); len(got) != 0 {
					t.Errorf("pushes = %v, want none", got)
				}
				if recs := auditRecords(t, logBuf); len(recs) != 0 {
					t.Errorf("audit records = %v, want none; no security decision was made", recs)
				}
				if !row.runRetire {
					if _, ok := f.perm.Lookup("tu-q1"); !ok {
						t.Error("claude's parked call was resolved by a frame that consumed nothing")
					}
				}
			})
		}
	}
}

// AC-5: on a daemon with no stream-approval bridge (foreground / PTY) both frames
// are a safe no-op — nothing resolves and nothing panics.
//
// Driven against an OUTSTANDING batch, so the row cannot pass vacuously through the
// unknown-batch path above: the resolver has something it could resolve and must
// still decline, because there is no actuator to resolve it with.
func TestQuestionResolverV2_NoBridgeIsASafeNoop(t *testing.T) {
	t.Parallel()

	for _, arm := range questionArms {
		t.Run(arm.name, func(t *testing.T) {
			t.Parallel()

			logger, logBuf := auditLogger()
			f := newQuestionFixture(t, testConvID, logger)
			_, _, batchID := surfacedQuestion(t, f, multiQuestionText)

			unwired := newQuestionResolverV2(f.qreg, logger)
			if arm.call(unwired, batchID, eligibleDevice(t)) {
				t.Error("resolution = true with no stream-approval bridge wired")
			}
			if _, ok := f.qreg.Lookup(batchID); !ok {
				t.Error("batch consumed by a resolver with no actuator")
			}
			if _, ok := f.perm.Lookup("tu-q1"); !ok {
				t.Error("claude's parked call was resolved with no actuator wired")
			}
			if got := pushTypes(f.bcast.pushes); len(got) != 0 {
				t.Errorf("pushes = %v, want none", got)
			}
			if recs := auditRecords(t, logBuf); len(recs) != 0 {
				t.Errorf("audit records = %v, want none; nothing was decided", recs)
			}
		})
	}
}
