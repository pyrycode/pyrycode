//go:build e2e_realclaude

package realclaude

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestInteractiveStreamForwardsSubagentText(t *testing.T) {
	runParallel(t)
	h, convID := startStreamRunningTurnHarness(t)
	nonce := time.Now().UnixNano()
	scenario.SubagentText(t, sharedHarnessWire(t, h, 2), convID, fmt.Sprintf("Use the Agent tool exactly once and wait for it in the foreground. Ask the agent to write exactly twelve numbered paragraphs about reliable process supervision, with at least two complete sentences per paragraph, and not to use tools. After it returns, write your own distinct twelve numbered paragraphs on the same topic, again with at least two complete sentences each. Do not use any other tools. run=%d", nonce), 180*time.Second)
}

type subagentTextLane struct {
	turnID string
	seqs   []int
}

func (l *subagentTextLane) observe(t *testing.T, p protocol.AssistantDeltaPayload) {
	t.Helper()
	if l.turnID == "" {
		if p.Seq != 0 {
			t.Fatalf("first delta for parent %q has seq %d, want 0", p.ParentToolUseID, p.Seq)
		}
		l.turnID = p.TurnID
	} else if p.TurnID != l.turnID {
		t.Fatalf("parent %q changed turn_id from %q to %q", p.ParentToolUseID, l.turnID, p.TurnID)
	}
	if p.Seq != len(l.seqs) {
		t.Fatalf("parent %q seq = %d, want lane-local %d", p.ParentToolUseID, p.Seq, len(l.seqs))
	}
	l.seqs = append(l.seqs, p.Seq)
}

func assertSubagentTextLanes(t *testing.T, agentID string, lanes map[string]*subagentTextLane) {
	t.Helper()
	if agentID == "" {
		t.Fatal("no foreground Agent/Task tool_use observed")
	}
	main, child := lanes[""], lanes[agentID]
	if len(main.seqs) < 2 || len(child.seqs) == 0 {
		t.Fatalf("lane deltas main=%v child=%v, want an advancing main lane and a non-empty child lane", main.seqs, child.seqs)
	}
	if main.turnID == "" || child.turnID == "" || main.turnID == child.turnID {
		t.Fatalf("lane turn ids main=%q child=%q, want distinct non-empty ids", main.turnID, child.turnID)
	}
}

func nextSubagentTextEnvelope(t *testing.T, h *perConvHarness, deadline time.Time) (protocol.Envelope, bool) {
	t.Helper()
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return protocol.Envelope{}, false
		}
		raw, err := h.phone.ReceiveBytes(remaining)
		if err != nil {
			if errors.Is(err, fakephone.ErrReceiveTimeout) {
				return protocol.Envelope{}, false
			}
			t.Fatalf("phone receive: %v", err)
		}
		var inner protocol.InnerFrameV2
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Fatalf("decode inner frame: %v", err)
		}
		if inner.Type != protocol.TypeNoiseMsg {
			continue
		}
		cipher, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			t.Fatalf("decode inner data: %v", err)
		}
		plain, err := h.initRecv.Decrypt(cipher)
		if err != nil {
			t.Fatalf("phone decrypt (receive-nonce desync?): %v", err)
		}
		var env protocol.Envelope
		if err := json.Unmarshal(plain, &env); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		return env, true
	}
}
