package main

import (
	"testing"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/questionbridge"
	"github.com/pyrycode/pyrycode/internal/relay"
)

var _ relay.DiagnosticQuestionResolver = (*questionResolverV2)(nil)

// Wrap only the bridge's registry: admission still reads the real outstanding
// batch, and a test can reproduce retirement at a later check without a race.
type diagnosticQuestionRegistry struct {
	*questionbridge.Registry
	beforeLookup  func(string)
	beforeResolve func(string)
	afterResolve  func(string)
	resolves      int
}

func (r *diagnosticQuestionRegistry) Lookup(id string) (protocol.QuestionShownPayload, bool) {
	if r.beforeLookup != nil {
		r.beforeLookup(id)
	}
	return r.Registry.Lookup(id)
}

func (r *diagnosticQuestionRegistry) Resolve(id string) (protocol.QuestionShownPayload, bool) {
	r.resolves++
	if r.beforeResolve != nil {
		r.beforeResolve(id)
	}
	batch, ok := r.Registry.Resolve(id)
	if ok && r.afterResolve != nil {
		r.afterResolve(id)
	}
	return batch, ok
}

func questionDiagnostic(r *questionResolverV2, arm, id string, answers []protocol.QuestionAnswerEntry, dev *devices.Device) (bool, string) {
	if arm == "answer" {
		return r.ResolveAnswerDiagnostic(answerPayload(id, answers), dev)
	}
	return r.ResolveRefusalDiagnostic(refusalPayload(id), dev)
}

func TestQuestionResolverV2_DiagnosticFailures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, reason string
		answerOnly   bool
		prepare      func(questionFixture, *questionResolverV2, *diagnosticQuestionRegistry, *string, **devices.Device)
	}{
		{"no actuator before lookup and gate", "no_actuator", false, func(_ questionFixture, r *questionResolverV2, _ *diagnosticQuestionRegistry, id *string, dev **devices.Device) {
			r.bridge, *id, *dev = nil, "unknown", nil
		}},
		{"unknown before gate", "unknown_or_retired_batch", false, func(_ questionFixture, _ *questionResolverV2, _ *diagnosticQuestionRegistry, id *string, dev **devices.Device) {
			*id, *dev = "unknown", nil
		}},
		{"empty before gate", "unknown_or_retired_batch", false, func(_ questionFixture, _ *questionResolverV2, _ *diagnosticQuestionRegistry, id *string, dev **devices.Device) {
			*id, *dev = "", nil
		}},
		{"retired before gate", "unknown_or_retired_batch", false, func(f questionFixture, _ *questionResolverV2, _ *diagnosticQuestionRegistry, id *string, dev **devices.Device) {
			f.qreg.Resolve(*id)
			*dev = nil
		}},
		{"unauthorized before correlation", "unauthorized_device", false, func(f questionFixture, _ *questionResolverV2, _ *diagnosticQuestionRegistry, id *string, dev **devices.Device) {
			delete(f.bridge.byQuestion, *id)
			*dev = nil
		}},
		{"correlation before parked request and validation", "missing_correlation", false, func(f questionFixture, _ *questionResolverV2, reg *diagnosticQuestionRegistry, id *string, _ **devices.Device) {
			delete(f.bridge.byQuestion, *id)
			f.perm.Resolve("tu-q1", permbridge.Deny("expired"))
			reg.beforeLookup = func(id string) { f.qreg.Resolve(id) }
		}},
		{"parked request before batch validation", "missing_parked_request", true, func(f questionFixture, _ *questionResolverV2, reg *diagnosticQuestionRegistry, _ *string, _ **devices.Device) {
			f.perm.Resolve("tu-q1", permbridge.Deny("expired"))
			reg.beforeLookup = func(id string) { f.qreg.Resolve(id) }
		}},
		{"validation lookup before verdict", "missing_batch_during_validation", true, func(f questionFixture, _ *questionResolverV2, reg *diagnosticQuestionRegistry, _ *string, _ **devices.Device) {
			reg.beforeLookup = func(id string) { f.qreg.Resolve(id) }
		}},
		{"verdict before consume", "verdict_rejected", true, func(f questionFixture, _ *questionResolverV2, reg *diagnosticQuestionRegistry, _ *string, _ **devices.Device) {
			reg.beforeResolve = func(id string) { f.qreg.Resolve(id) }
		}},
		{"one-shot lost", "resolution_lost", false, func(f questionFixture, _ *questionResolverV2, reg *diagnosticQuestionRegistry, _ *string, _ **devices.Device) {
			reg.beforeResolve = func(id string) { f.qreg.Resolve(id) }
		}},
	}
	for _, arm := range []string{"answer", "refusal"} {
		for _, tc := range cases {
			if tc.answerOnly && arm != "answer" {
				continue
			}
			for _, diagnostic := range []bool{true, false} {
				mode := "bool"
				if diagnostic {
					mode = "diagnostic"
				}
				t.Run(arm+"/"+tc.name+"/"+mode, func(t *testing.T) {
					t.Parallel()
					logger, logBuf := auditLogger()
					f := newQuestionFixture(t, testConvID, logger)
					_, _, id := surfacedQuestion(t, f, multiQuestionText)
					r := gatedResolver(f, logger)
					reg := &diagnosticQuestionRegistry{Registry: f.qreg}
					f.bridge.questions = reg
					dev := eligibleDevice(t)
					tc.prepare(f, r, reg, &id, &dev)
					// Earlier checks compete with a bad verdict; only the last
					// case needs a valid answer to reach the one-shot.
					var answers []protocol.QuestionAnswerEntry
					if tc.reason == "resolution_lost" {
						answers = goodAnswers()
					}
					var consumed bool
					if diagnostic {
						var reason string
						consumed, reason = questionDiagnostic(r, arm, id, answers, dev)
						if reason != tc.reason {
							t.Errorf("reason = %q, want %q", reason, tc.reason)
						}
					} else if arm == "answer" {
						consumed = r.ResolveAnswer(answerPayload(id, answers), dev)
					} else {
						consumed = r.ResolveRefusal(refusalPayload(id), dev)
					}
					if consumed || len(f.bcast.pushes) != 0 {
						t.Fatalf("declined attempt consumed=%v, pushes=%v", consumed, pushTypes(f.bcast.pushes))
					}
					if tc.reason == "unauthorized_device" {
						assertOneRecord(t, logBuf, id, "denied_unauthorized")
					} else if logBuf.Len() != 0 {
						t.Errorf("inert attempt logged or audited: %s", logBuf)
					}
					wantResolves := 0
					if tc.reason == "resolution_lost" {
						wantResolves = 1
					}
					if reg.resolves != wantResolves {
						t.Errorf("consume attempts = %d, want %d", reg.resolves, wantResolves)
					}
					if tc.reason == "verdict_rejected" {
						reg.beforeResolve = nil
						if ok, why := r.ResolveAnswerDiagnostic(answerPayload(id, goodAnswers()), dev); !ok || why != "resolved" {
							t.Fatalf("corrected answer = (%v, %q)", ok, why)
						}
						waitPush(t, f.pushed)
					}
				})
			}
		}
	}
}

func TestQuestionResolverV2_DiagnosticSuccess(t *testing.T) {
	t.Parallel()
	for _, arm := range []string{"answer", "refusal"} {
		for _, expires := range []bool{false, true} {
			name := "parked"
			if expires {
				name = "permission gone after consume"
			}
			t.Run(arm+"/"+name, func(t *testing.T) {
				t.Parallel()
				logger, logBuf := auditLogger()
				f := newQuestionFixture(t, testConvID, logger)
				_, pending, id := surfacedQuestion(t, f, multiQuestionText)
				reg := &diagnosticQuestionRegistry{Registry: f.qreg}
				if expires {
					reg.afterResolve = func(string) { f.perm.Resolve("tu-q1", permbridge.Deny("expired")) }
				}
				f.bridge.questions = reg
				r := gatedResolver(f, logger)
				if ok, why := questionDiagnostic(r, arm, id, goodAnswers(), eligibleDevice(t)); !ok || why != "resolved" {
					t.Fatalf("resolution = (%v, %q), want (true, resolved)", ok, why)
				}
				waitPush(t, f.pushed)
				wantAudit, wantBehavior, wantDismissal := "denied", permbridge.BehaviorDeny, outcomeQuestionRefused
				if arm == "answer" {
					wantAudit, wantBehavior, wantDismissal = "allowed", permbridge.BehaviorAllow, outcomeQuestionAnswered
				}
				if expires {
					wantBehavior = permbridge.BehaviorDeny
				}
				if v := pending.Await(); v.Behavior != wantBehavior {
					t.Errorf("verdict = %#v, want behavior %q", v, wantBehavior)
				}
				if got := lastQuestionDismissed(t, f.bcast.pushes); got.Outcome != wantDismissal || got.QuestionBatchID != id {
					t.Errorf("dismissal = %#v", got)
				}
				f.bridge.retireQuestion(id)
				if len(f.bcast.pushes) != 1 || reg.resolves != 2 {
					t.Errorf("pushes=%d, consumes=%d, want one dismissal and one attempt plus retire", len(f.bcast.pushes), reg.resolves)
				}
				assertOneRecord(t, logBuf, id, wantAudit)
			})
		}
	}
}

// A declined delegate must never turn into a successful decision audit, even
// though the admission lookup and device gate both passed.
type decliningQuestionActuator struct {
	answers, refusals int
}

func (a *decliningQuestionActuator) AnswerQuestionDiagnostic(string, []protocol.QuestionAnswerEntry) (bool, string) {
	a.answers++
	return false, "resolution_lost"
}

func (a *decliningQuestionActuator) RefuseQuestionDiagnostic(string) (bool, string) {
	a.refusals++
	return false, "resolution_lost"
}

func TestQuestionResolverV2_DeclinedDelegateOnceWithoutAudit(t *testing.T) {
	t.Parallel()
	for _, arm := range questionArms {
		for _, diagnostic := range []bool{true, false} {
			mode := "bool"
			if diagnostic {
				mode = "diagnostic"
			}
			t.Run(arm.name+"/"+mode, func(t *testing.T) {
				t.Parallel()
				logger, logBuf := auditLogger()
				f := newQuestionFixture(t, testConvID, logger)
				_, _, id := surfacedQuestion(t, f, multiQuestionText)
				r := gatedResolver(f, logger)
				a := &decliningQuestionActuator{}
				r.bridge = a
				if diagnostic {
					if consumed, reason := questionDiagnostic(r, arm.name, id, goodAnswers(), eligibleDevice(t)); consumed || reason != "resolution_lost" {
						t.Errorf("declined delegate result = (%v, %q)", consumed, reason)
					}
				} else if arm.call(r, id, eligibleDevice(t)) {
					t.Error("bool wrapper accepted declined delegate")
				}
				wantAnswers, wantRefusals := 1, 0
				if arm.name == "refusal" {
					wantAnswers, wantRefusals = 0, 1
				}
				if a.answers != wantAnswers || a.refusals != wantRefusals || logBuf.Len() != 0 {
					t.Errorf("attempts=(%d,%d), logs=%s", a.answers, a.refusals, logBuf)
				}
			})
		}
	}
}
