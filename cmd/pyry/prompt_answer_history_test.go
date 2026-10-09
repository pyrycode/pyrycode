package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/modalbridge"
	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/questionbridge"
)

func testPromptBridge(t *testing.T, store *history.Store, logger *slog.Logger) (*streamApprovalBridge, *modalResolverV2, *questionResolverV2, *chanBcast) {
	t.Helper()
	modal, questions, bc := modalbridge.New(), questionbridge.New(), newChanBcast("phone")
	b := newStreamApprovalBridge(permbridge.New(), modal, bc, func() string { return testConvID }, context.Background(), logger)
	b.questions, b.hist = questions, store
	b.sessionConv = func(string) (string, bool) { return testConvID, true }
	b.sessionHarness = func(string) (string, bool) { return "claude", true }
	m := newModalResolverV2(modal, logger)
	m.streamApprovals = b
	q := newQuestionResolverV2(questions, logger)
	q.bridge = b
	return b, m, q, bc
}

func testPromptSurface(t *testing.T, b *streamApprovalBridge, bc *chanBcast, req permbridge.Request, timeout time.Duration) (*permbridge.Pending, string, func()) {
	t.Helper()
	p, err := b.perm.Register(req.ToolUseID, req, timeout)
	if err != nil {
		t.Fatal(err)
	}
	retire := b.Surface(req)
	env := <-bc.pushed
	var ids struct {
		ModalID string `json:"modal_id"`
		BatchID string `json:"question_batch_id"`
	}
	if err := json.Unmarshal(env.Payload, &ids); err != nil {
		t.Fatal(err)
	}
	id := ids.ModalID
	if id == "" {
		id = ids.BatchID
	}
	if id == "" {
		t.Fatal("no surface correlation")
	}
	t.Cleanup(func() { b.perm.Resolve(req.ToolUseID, permbridge.Deny("cleanup")); retire() })
	return p, id, retire
}

func testPromptFact(t *testing.T, store *history.Store, id string) (history.Entry, promptAnswerFact, promptAnswerProjection) {
	t.Helper()
	entries := historyEntries(t, store, testConvID)
	if len(entries) != 1 {
		t.Fatalf("answer entries=%d, want exactly one", len(entries))
	}
	e := entries[0]
	var f promptAnswerFact
	if err := json.Unmarshal(e.Payload, &f); err != nil {
		t.Fatal(err)
	}
	var projection promptAnswerProjection
	if err := json.Unmarshal(f.Context, &projection); err != nil {
		t.Fatal(err)
	}
	if e.Type != historyPromptAnswered || f.CorrelationID != id || f.ConversationID != testConvID || f.Source != "remote" || f.ResolvedAt.IsZero() || !f.ResolvedAt.Equal(e.TS) || f.SessionID != "asking-session" || e.Shown == nil || !*e.Shown {
		t.Fatalf("bad durable fact: %#v, %#v", e, f)
	}
	return e, f, projection
}

func TestPromptAnswerHistoryPermission(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "unknown"} {
		for _, decision := range []string{"allow", "deny", "cancel"} {
			for _, grant := range []bool{false, true} {
				t.Run(agent+"/"+decision+"/"+map[bool]string{false: "plain", true: "grant"}[grant], func(t *testing.T) {
					dir := t.TempDir()
					store := history.New(dir)
					logger, logs := auditLogger()
					b, m, _, bc := testPromptBridge(t, store, logger)
					b.sessionHarness = func(string) (string, bool) { return agent, agent != "unknown" }
					req := permbridge.Request{ToolUseID: "reused-id", ToolName: "Bash", Input: json.RawMessage(`{"command":"/secret-host-path"}`), SessionID: "asking-session", Description: "secret-description", BlockedPath: "secret-blocked", DecisionReason: json.RawMessage(`"secret-reason"`)}
					if grant {
						req.AlwaysAllow = permbridge.SessionGrant("grant")
						if agent == "claude" {
							req.AlwaysAllow = permbridge.ParseAlwaysAllow(json.RawMessage(`[{"type":"addRules","destination":"session","behavior":"allow","rules":[{"toolName":"Bash","ruleContent":"secret-grant-input"}]}]`), false)
						}
					}
					pending, id, retire := testPromptSurface(t, b, bc, req, time.Hour)
					if len(historyEntries(t, store, testConvID)) != 0 {
						t.Fatal("open prompt saved")
					}
					if _, ok := m.ResolveAnswer(id, "allow_once", "secret-token", nil); ok {
						t.Fatal("unauthorized answer won")
					}
					if _, ok := m.ResolveAnswer(id, "secret-forged-option", "secret-token", eligibleDevice(t)); ok {
						t.Fatal("invalid answer won")
					}
					if strings.Contains(logs.String(), "secret-") {
						t.Fatal("prompt/answer content in log")
					}
					// Ownership must survive both routing and agent rebinding while parked.
					b.sessionConv = func(string) (string, bool) { return "successor", true }
					b.sessionHarness = func(string) (string, bool) { return "codex", true }
					var ok bool
					if decision == "cancel" {
						_, ok = m.ResolveCancel(id, eligibleDevice(t))
					} else {
						option := "allow_once"
						if decision == "deny" {
							option = "reject_once"
						}
						_, ok = m.ResolveAnswerWithAlwaysAllow(id, option, "secret-token", true, eligibleDevice(t))
					}
					if !ok {
						t.Fatal("resolution rejected")
					}
					v := pending.Await()
					effective := "deny"
					if decision == "allow" {
						effective = "allow"
					}
					if v.Behavior != effective || (effective == "allow" && !bytes.Equal(v.UpdatedInput, req.Input)) {
						t.Fatalf("child verdict changed: %#v", v)
					}
					retire()
					if _, ok := m.ResolveCancel(id, eligibleDevice(t)); ok {
						t.Fatal("duplicate consumed")
					}
					e, f, projection := testPromptFact(t, store, id)
					if f.Decision != decision || f.Behavior != effective || f.SessionGrant != (grant && decision == "allow") || projection.Tool != "Bash" || projection.Class == "" {
						t.Fatalf("decision/context: %#v %#v", f, projection)
					}
					if agent == "unknown" {
						if e.Session != nil {
							t.Fatal("invented provenance")
						}
					} else if e.Session == nil || e.Session.Kind != agent || e.Session.SessionID != req.SessionID {
						t.Fatalf("wrong asker: %#v", e.Session)
					}
					if strings.Contains(string(e.Payload), "secret-") {
						t.Fatal("unsafe context stored")
					}
					for _, reader := range []*history.Store{store, history.New(dir)} {
						watermark, err := reader.LatestDisplayableEntryID(conversations.ConversationID(testConvID))
						if err != nil || watermark != 1 {
							t.Fatalf("shown watermark=%d %v", watermark, err)
						}
						page := newHistoryPager(reader, logger)(testConvID, "", 1)
						raw, err := reader.Page(conversations.ConversationID(testConvID), "", 1)
						if err != nil || len(page.Entries) != 0 || page.Cursor != raw.Cursor || page.AtStart != raw.AtStart {
							t.Fatalf("legacy page=%#v", page)
						}
					}
					if legacyHistoryType(e.Type) {
						t.Fatal("legacy type admitted")
					}
					if _, ok := legacyRuntimeReceipt(testConvID, e.Type, e.Payload, &e.ID, e.TS); ok {
						t.Fatal("legacy receipt admitted")
					}
					select {
					case env := <-bc.pushed:
						t.Fatalf("saved answer published: %s", env.Type)
					default:
					}
					// A fresh surface reusing the parked key owns a fresh immutable snapshot.
					b.sessionConv = func(string) (string, bool) { return testConvID, true }
					p2, id2, retire2 := testPromptSurface(t, b, bc, req, time.Hour)
					if id2 == id {
						t.Fatal("surface identity reused")
					}
					m.ResolveAnswer(id2, "reject_once", "", eligibleDevice(t))
					p2.Await()
					retire2()
					if len(historyEntries(t, store, testConvID)) != 2 {
						t.Fatal("fresh reused key lost its fact")
					}
				})
			}
		}
	}
}

func TestPromptAnswerHistoryQuestion(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		for _, refuse := range []bool{false, true} {
			t.Run(agent+map[bool]string{false: "/answer", true: "/refusal"}[refuse], func(t *testing.T) {
				store := history.New(t.TempDir())
				b, _, q, bc := testPromptBridge(t, store, discardLogger())
				b.sessionHarness = func(string) (string, bool) { return agent, true }
				input := json.RawMessage(`{"questions":[{"question":"Pick strategy","header":"Strategy","options":[{"label":"rewrite","description":"replace wholesale"},{"label":"patch","description":"small changes"}],"multiSelect":true},{"question":"Explain","header":"Detail","options":[{"label":"yes","description":"affirm"},{"label":"no","description":"reject"}],"multiSelect":false}],"transport":"secret-envelope"}`)
				req := permbridge.Request{ToolUseID: "question", ToolName: questionbridge.ToolName, Input: input, SessionID: "asking-session"}
				pending, id, retire := testPromptSurface(t, b, bc, req, time.Hour)
				if len(historyEntries(t, store, testConvID)) != 0 || len(b.questions.(*questionbridge.Registry).Snapshot()) != 1 {
					t.Fatal("open batch not live-only")
				}
				answers := []protocol.QuestionAnswerEntry{{QuestionIndex: 0, Values: []string{"rewrite", "patch"}}, {QuestionIndex: 1, Values: []string{"free text"}}}
				if q.ResolveAnswer(protocol.QuestionAnswerPayload{QuestionBatchID: id, Answers: answers}, nil) {
					t.Fatal("unauthorized answer")
				}
				b.sessionConv = func(string) (string, bool) { return "successor", true }
				b.sessionHarness = func(string) (string, bool) { return "unknown", false }
				var ok bool
				if refuse {
					ok = q.ResolveRefusal(protocol.QuestionRefusedPayload{QuestionBatchID: id, AnswerToken: "secret-token"}, eligibleDevice(t))
				} else {
					ok = q.ResolveAnswer(protocol.QuestionAnswerPayload{QuestionBatchID: id, AnswerToken: "secret-token", Answers: answers}, eligibleDevice(t))
				}
				if !ok {
					t.Fatal("question not resolved")
				}
				v := pending.Await()
				env := <-bc.pushed
				if env.Type != protocol.TypeQuestionDismissed {
					t.Fatalf("unexpected publication %s", env.Type)
				}
				retire()
				if q.ResolveRefusal(protocol.QuestionRefusedPayload{QuestionBatchID: id}, eligibleDevice(t)) {
					t.Fatal("duplicate accepted")
				}
				e, f, projection := testPromptFact(t, store, id)
				want := "answer"
				behavior := "allow"
				if refuse {
					want = "refusal"
					behavior = "deny"
				}
				if f.Decision != want || f.Behavior != behavior || v.Behavior != behavior || e.Session.Kind != agent || len(projection.Questions) != 2 || projection.Questions[0].Index != 0 || !projection.Questions[0].MultiSelect || projection.Questions[0].Text != "Pick strategy" {
					t.Fatalf("question fact %#v %#v", f, projection)
				}
				if !refuse {
					if projection.Questions[0].Values[0].Text != "rewrite" || projection.Questions[0].Values[0].Meaning != "replace wholesale" || projection.Questions[1].Values[0].Text != "free text" || projection.Questions[1].Values[0].Meaning != "" {
						t.Fatal("selected meaning/free text lost")
					}
					if !bytes.Contains(v.UpdatedInput, []byte(`"free text"`)) {
						t.Fatal("child answer changed")
					}
				}
				if bytes.Contains(e.Payload, []byte("secret-")) {
					t.Fatal("transport/token stored")
				}
				select {
				case env := <-bc.pushed:
					t.Fatalf("extra publication %s", env.Type)
				default:
				}
			})
		}
	}
}

func TestPromptAnswerHistoryLosingResolution(t *testing.T) {
	for _, arm := range []string{"permission", "cancel", "answer", "refusal"} {
		for _, loss := range []string{"expiry", "teardown", "after-validation"} {
			t.Run(arm+"/"+loss, func(t *testing.T) {
				store := history.New(t.TempDir())
				b, m, q, bc := testPromptBridge(t, store, discardLogger())
				req := permbridge.Request{ToolUseID: "loser", ToolName: "Bash", Input: json.RawMessage(`{}`), SessionID: "asking-session"}
				question := arm == "answer" || arm == "refusal"
				if question {
					req.ToolName = questionbridge.ToolName
					req.Input = questionInput(t, "Pick", "Choice", "rewrite", "meaning")
				}
				timeout := time.Hour
				if loss == "expiry" {
					timeout = 20 * time.Millisecond
				}
				pending, id, retire := testPromptSurface(t, b, bc, req, timeout)
				if loss == "after-validation" && question {
					reg := b.questions.(*questionbridge.Registry)
					b.questions = &diagnosticQuestionRegistry{Registry: reg, afterResolve: func(string) { b.perm.Resolve(req.ToolUseID, permbridge.Deny("expired")) }}
				} else {
					if loss != "expiry" {
						b.perm.Resolve(req.ToolUseID, permbridge.Deny("teardown"))
					}
					pending.Await()
					if loss == "teardown" {
						retire()
						<-bc.pushed
					}
				}
				switch arm {
				case "permission":
					m.ResolveAnswer(id, "reject_once", "", eligibleDevice(t))
				case "cancel":
					m.ResolveCancel(id, eligibleDevice(t))
				case "answer":
					ok := q.ResolveAnswer(protocol.QuestionAnswerPayload{QuestionBatchID: id, Answers: []protocol.QuestionAnswerEntry{{QuestionIndex: 0, Values: []string{"rewrite"}}, {QuestionIndex: 1, Values: []string{"main"}}}}, eligibleDevice(t))
					if loss == "after-validation" && !ok {
						t.Fatal("question did not reach parked resolution")
					}
				case "refusal":
					q.ResolveRefusal(protocol.QuestionRefusedPayload{QuestionBatchID: id}, eligibleDevice(t))
				}
				if len(historyEntries(t, store, testConvID)) != 0 {
					t.Fatal("losing parked resolution saved")
				}
			})
		}
	}
}

func TestPromptAnswerHistoryStorage(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "nil", true: "failed"}[failed], func(t *testing.T) {
			logger, logs := auditLogger()
			var store *history.Store
			if failed {
				path := filepath.Join(t.TempDir(), "secret-storage-path")
				if err := os.WriteFile(path, []byte("not directory"), 0600); err != nil {
					t.Fatal(err)
				}
				store = history.New(path)
			}
			b, m, _, bc := testPromptBridge(t, store, logger)
			p, id, retire := testPromptSurface(t, b, bc, permbridge.Request{ToolUseID: "storage", ToolName: "Bash", Input: json.RawMessage(`{"secret":"secret-answer"}`)}, time.Hour)
			if _, ok := m.ResolveAnswer(id, "allow_once", "secret-token", eligibleDevice(t)); !ok {
				t.Fatal("storage blocked resolution")
			}
			if p.Await().Behavior != "allow" {
				t.Fatal("storage changed verdict")
			}
			retire()
			if _, ok := m.ResolveAnswer(id, "allow_once", "", eligibleDevice(t)); ok {
				t.Fatal("retried resolution")
			}
			if strings.Contains(logs.String(), "secret-") {
				t.Fatal("storage error/content leaked")
			}
			if failed && strings.Count(logs.String(), "prompt_answer.history_append_err") != 1 {
				t.Fatal("failure not logged once")
			}
		})
	}
}

func TestPromptAnswerHistoryBounded(t *testing.T) {
	store := history.New(t.TempDir())
	b, _, q, bc := testPromptBridge(t, store, discardLogger())
	req := permbridge.Request{ToolUseID: "large", ToolName: questionbridge.ToolName, Input: questionInput(t, "Pick", "Header", "rewrite", "meaning"), SessionID: "asking-session"}
	p, id, retire := testPromptSurface(t, b, bc, req, time.Hour)
	value := strings.Repeat("\x00界<&", maxPromptAnswerProjection/8)
	answers := []protocol.QuestionAnswerEntry{{QuestionIndex: 0, Values: []string{value}}, {QuestionIndex: 1, Values: []string{"main"}}}
	if !q.ResolveAnswer(protocol.QuestionAnswerPayload{QuestionBatchID: id, Answers: answers}, eligibleDevice(t)) {
		t.Fatal("large answer rejected")
	}
	v := p.Await()
	<-bc.pushed
	retire()
	_, f, _ := testPromptFact(t, store, id)
	if len(f.Context) > maxPromptAnswerProjection || !f.Truncated {
		t.Fatalf("unbounded projection %d truncated=%v", len(f.Context), f.Truncated)
	}
	var child answerVerdictInput
	if err := json.Unmarshal(v.UpdatedInput, &child); err != nil {
		t.Fatal(err)
	}
	values, ok := child.Answers["Pick"].([]any)
	if !ok || len(values) != 1 || values[0] != value {
		t.Fatal("bounded history changed child answer")
	}
}

func TestPromptAnswerHistoryUnauthenticatedCancel(t *testing.T) {
	store := history.New(t.TempDir())
	b, m, _, bc := testPromptBridge(t, store, discardLogger())
	p, id, retire := testPromptSurface(t, b, bc, permbridge.Request{ToolUseID: "nil-cancel", ToolName: "Bash", Input: json.RawMessage(`{}`)}, time.Hour)
	// Preserve the existing fail-closed dismissal contract without inventing an operator.
	if _, ok := m.ResolveCancel(id, nil); !ok {
		t.Fatal("cancel dismissal contract changed")
	}
	if p.Await().Behavior != "deny" {
		t.Fatal("nil cancel no longer denies")
	}
	retire()
	if len(historyEntries(t, store, testConvID)) != 0 {
		t.Fatal("unauthenticated cancel saved as operator answer")
	}
}

func TestPromptAnswerHistoryRetirement(t *testing.T) {
	for _, question := range []bool{false, true} {
		t.Run(map[bool]string{false: "permission", true: "question"}[question], func(t *testing.T) {
			store := history.New(t.TempDir())
			b, m, q, bc := testPromptBridge(t, store, discardLogger())
			req := permbridge.Request{ToolUseID: "retirement", ToolName: "Bash", Input: json.RawMessage(`{}`), SessionID: "asking-session"}
			if question {
				req.ToolName = questionbridge.ToolName
				req.Input = questionInput(t, "Pick", "Header", "rewrite", "meaning")
			}
			p, id, retire := testPromptSurface(t, b, bc, req, time.Hour)
			if question {
				reg := b.questions.(*questionbridge.Registry)
				// Delete ownership deterministically between consuming the surface and
				// winning the parked registry; the answer must use its earlier snapshot.
				b.questions = &diagnosticQuestionRegistry{Registry: reg, afterResolve: func(string) { retire() }}
				if !q.ResolveRefusal(protocol.QuestionRefusedPayload{QuestionBatchID: id}, eligibleDevice(t)) {
					t.Fatal("refusal lost")
				}
				p.Await()
				<-bc.pushed
			} else {
				retired := make(chan struct{})
				go func() { p.Await(); retire(); close(retired) }()
				if _, ok := m.ResolveAnswer(id, "allow_once", "", eligibleDevice(t)); !ok {
					t.Fatal("permission lost")
				}
				<-retired
			}
			e, _, _ := testPromptFact(t, store, id)
			if e.Session == nil || e.Session.Kind != "claude" {
				t.Fatal("retirement erased captured owner")
			}
			b.mu.Lock()
			remaining := len(b.promptOwners)
			b.mu.Unlock()
			if remaining != 0 {
				t.Fatal("retirement retained ownership")
			}
		})
	}
}
