//go:build e2e_realclaude

package realclaude

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestQueuedReadinessFold(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reference int
		duplicate bool
		want      bool
	}{
		{"matching delivery", 1, false, true}, {"unrelated acceptance", 9, false, false}, {"duplicate message", 1, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Now().UTC()
			entry := func(id uint64, kind, payload string) history.Entry {
				return history.Entry{ID: id, Type: kind, TS: at, Payload: json.RawMessage(payload)}
			}
			entries := []history.Entry{
				entry(1, "send_accepted", `{"conversation_id":"c","message_id":"m","accepted_at":"`+at.Format(time.RFC3339Nano)+`"}`),
				entry(2, "message", `{"conversation_id":"c","message_id":"m","role":"user","text":"private prompt"}`),
				entry(3, "send_delivered", `{"conversation_id":"c","accepted_entry_id":`+string(rune('0'+tc.reference))+`,"delivery_entry_id":2}`),
				entry(4, "main_turn_opened", `{"conversation_id":"c"}`),
				entry(5, "assistant_delta", `{"conversation_id":"other","text":"private other reply"}`),
				entry(6, "assistant_delta", `{"conversation_id":"c","text":"PiNg"}`),
			}
			if tc.duplicate {
				entries = append(entries, entries[1])
			}
			r := readinessTurn{Ordinal: 1}
			readinessFoldTurn(&r, entries, "c", "m", "private prompt")
			if r.ExactlyOnce != tc.want || !r.PromptMatches || !r.ReplyMatches {
				t.Fatalf("fold once=%t prompt=%t reply=%t", r.ExactlyOnce, r.PromptMatches, r.ReplyMatches)
			}
			b, err := json.Marshal(readinessCapture{Turns: []readinessTurn{r}, Witnesses: []readinessWitness{{Subject: "private session", Value: 12345, Boundary: "gate_released"}}})
			if err != nil || strings.Contains(string(b), "private") || strings.Contains(string(b), "12345") {
				t.Fatal("retained evidence leaked private join/content")
			}
		})
	}
}

func TestReadinessPrefixContract(t *testing.T) {
	rows := readinessPrefix()
	if len(rows) != 37 {
		t.Fatalf("prefix methods = %d", len(rows))
	}
	ping := readinessJavaHash("interactiveTurn_pingPrompt_streamsPingReplyIntoThread")
	for i, r := range rows {
		if readinessJavaHash(r.Method) >= ping {
			t.Fatalf("method outside prefix: %s", r.Method)
		}
		if i > 0 && readinessJavaHash(rows[i-1].Method) > readinessJavaHash(r.Method) {
			t.Fatal("prefix is not in Java hash order")
		}
		if r.Owner != "main" && len(r.Operations) != 0 {
			t.Fatalf("dedicated daemon load attributed to main: %s", r.Method)
		}
	}
	if got := readinessOpeningTypes(); strings.Join(got, ",") != "mcp_status_request,request_context_usage,request_session_settings,request_history" {
		t.Fatalf("opening control types: %v", got)
	}
}

func TestReadinessRetainedEvidence(t *testing.T) {
	dir := filepath.Join("testdata", "queued_turn_readiness")
	b, err := os.ReadFile(filepath.Join(dir, "diagnostic-runs.json"))
	if err != nil {
		t.Fatal("counted diagnostic manifest missing")
	}
	var manifest struct {
		Artifacts []struct {
			Path, SHA256, Source string
			Counts               map[string]int
		}
	}
	if json.Unmarshal(b, &manifest) != nil || len(manifest.Artifacts) == 0 {
		t.Fatal("usable artifact pins missing")
	}
	for _, a := range manifest.Artifacts {
		if filepath.Base(a.Path) != a.Path || a.Path == "diagnostic-runs.json" {
			t.Fatal("artifact path escapes capture directory")
		}
		b, err := os.ReadFile(filepath.Join(dir, a.Path))
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(b)) != a.SHA256 {
			t.Fatalf("missing or changed artifact: %s", a.Path)
		}
		var r readinessCapture
		if json.Unmarshal(b, &r) != nil || r.DaemonCommit != a.Source || !reflect.DeepEqual(r.Counts, a.Counts) {
			t.Fatalf("source/count pin mismatch: %s", a.Path)
		}
		if r.Counts["executed"] != r.Counts["passed"]+r.Counts["failed"] || r.Counts["skipped"] != 0 {
			t.Fatalf("unusable terminal counts: %s", a.Path)
		}
		for _, w := range r.Witnesses {
			if w.Subject != "" || w.Value != 0 {
				t.Fatal("portable witness retained private join")
			}
		}
	}
}

func TestReadinessConversationOrdinals(t *testing.T) {
	p := &readinessReplay{subjects: map[string]int{"seed": 0}}
	a := p.bind("a", "child-a")
	b := p.bind("b", "child-b")
	p.bind("seed", "seed-child")
	c := p.bind("c", "child-c")
	if a == b || a == c || b == c || c == 0 {
		t.Fatal("selecting seed reused a conversation ordinal")
	}
	if got := p.bind("a", "replacement-child"); got != a || p.subjects["replacement-child"] != a {
		t.Fatal("replacement lost conversation identity")
	}
	d := p.bind("d", "child-d")
	if d == c || d == a || d == b {
		t.Fatal("reactivation reset the allocation counter")
	}
}
