package scenario

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const OutsideReadModalCap = 3

type OutsideReadModal struct {
	ToolLabel, ReasonType           string
	BlockedPathPresent, DefaultToNo bool
}

func OutsideRead(t *testing.T, w Wire, convID string, deadline time.Time) (string, []string, *OutsideReadModal, int) {
	t.Helper()
	budget := time.Until(deadline)

	var (
		text     strings.Builder
		tools    []string
		seenTool = map[string]struct{}{}
		sawDelta bool
		first    *OutsideReadModal
		answered int
	)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if !sawDelta {
				t.Fatalf("no non-empty assistant_delta for %q within %s (modals answered: %d) — the "+
					"message naming the note never produced a turn, so this run measured nothing",
					convID, budget, answered)
			}
			t.Fatalf("claude replied for %q but the turn never reached terminal turn_state{idle} "+
				"within %s; %d byte(s) so far, %d modal(s) answered, tools %v",
				convID, budget, text.Len(), answered, tools)
		}
		env := w.Next(deadline)

		switch env.Type {
		case protocol.TypeError:
			var ep protocol.ErrorPayload
			if err := json.Unmarshal(env.Payload, &ep); err != nil {
				t.Fatalf("the message was refused and its error payload did not decode: %v", err)
			}
			t.Fatalf("the message naming the note was refused with code %q (retryable=%v) — the "+
				"refusal is the daemon's, never claude's, so check the seeded conversation binding "+
				"before reading anything into it", ep.Code, ep.Retryable)

		case protocol.TypeModalShown:
			var shown protocol.ModalShownPayload
			if err := json.Unmarshal(env.Payload, &shown); err != nil {
				t.Fatalf("decode modal_shown payload: %v", err)
			}
			if shown.Class != "permission" || shown.ModalID == "" {
				t.Fatalf("modal_shown class = %q with modal_id present = %t, want a permission modal "+
					"with a non-empty id", safeReadLabel(shown.Class), shown.ModalID != "")
			}
			if answered >= OutsideReadModalCap {
				t.Fatalf("claude raised more than %d permission modals for %q without reaching idle — "+
					"it keeps reissuing the tool past the cap despite the prompt's no-retry clause; "+
					"that is a finding to raise separately, not a cap to widen", OutsideReadModalCap, convID)
			}
			if first == nil {
				first = &OutsideReadModal{
					ToolLabel:          safeReadLabel(shown.Title),
					ReasonType:         safeReadLabel(shown.ReasonType),
					BlockedPathPresent: shown.BlockedPath != "",
					DefaultToNo:        shown.DefaultToNo,
				}
				t.Logf("#2474: permission modal raised during the outside-workspace read "+
					"(title=%q, reason_type=%q, default_to_no=%t) — answering allow_once so the read "+
					"proceeds and the token can still witness it. READ THE TITLE: a modal for some "+
					"other tool is not this read being gated",
					first.ToolLabel, first.ReasonType, first.DefaultToNo)
			}
			answered++
			w.Send(protocol.TypeModalAnswer, protocol.ModalAnswerPayload{ModalID: shown.ModalID, OptionID: string(turnevent.PermissionOptionKindAllowOnce), AnswerToken: "e2e-2474-posture-probe"})

		case protocol.TypeToolUse:
			var p protocol.ToolUsePayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode tool_use payload: %v", err)
			}
			if p.ConversationID != convID || p.Name == "" {
				continue
			}
			if _, dup := seenTool[p.Name]; dup || len(tools) >= 8 {
				continue
			}
			// Allowlisted, not merely truncated: these names are claude-authored and
			// reach a committed capture as well as a failure message.
			seenTool[p.Name] = struct{}{}
			tools = append(tools, safeReadLabel(p.Name))

		case protocol.TypeAssistantDelta:
			var p protocol.AssistantDeltaPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode assistant_delta payload: %v", err)
			}
			if p.ConversationID == convID && strings.TrimSpace(p.Text) != "" {
				sawDelta = true
				text.WriteString(p.Text)
			}

		case protocol.TypeTurnState:
			var st protocol.TurnStatePayload
			if err := json.Unmarshal(env.Payload, &st); err != nil {
				t.Fatalf("decode turn_state payload: %v", err)
			}
			if st.State != "idle" || st.ConversationID != convID || !sawDelta {
				continue
			}
			return text.String(), tools, first, answered
		}
	}
}
func safeReadLabel(s string) string {
	if s == "" {
		return "<empty>"
	}
	if len(s) > 64 {
		return "<invalid>"
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return "<invalid>"
	}
	return s
}
func RequireReadWitness(t *testing.T, reply, token string) {
	t.Helper()
	if token == "" || !strings.Contains(reply, token) {
		t.Fatal("outside read did not return its file-only witness")
	}
}
