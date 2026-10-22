package scenario

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestExtendedControls(t *testing.T) {
	for _, name := range []string{"family-valid", "family-dropped", "family-truncated", "family-duplicate", "family-refused", "family-unmoved", "family-wrong-model", "attachment-valid", "attachment-refused", "attachment-wrong-id", "attachment-no-directory", "attachment-wrong-filename", "attachment-no-token", "attachment-modal", "attachment-prompt-leak", "bypass-valid", "bypass-wrong-variant", "bypass-duplicate-labels", "bypass-missing-free-text", "bypass-unchosen-first", "bypass-history-session", "bypass-wrong-mode", "bypass-no-yolo", "bypass-session"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestExtendedHelperProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "PYRY_EXTENDED_CONTROL="+name)
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil || (err == nil) != strings.HasSuffix(name, "-valid") {
				t.Fatalf("extended acceptance err=%v output=%s", err, out)
			}
		})
	}
}

func TestExtendedHelperProcess(t *testing.T) {
	name := os.Getenv("PYRY_EXTENDED_CONTROL")
	if name == "" {
		return
	}
	frame := func(kind string, value any) protocol.Envelope {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.Envelope{Type: kind, Payload: raw}
	}
	home := t.TempDir()
	var rows []protocol.Envelope
	sequence, turns, answers, witness := uint64(0), 0, 0, 0
	appendTurn := func(text, model string) {
		rows = append(rows, frame(protocol.TypeModelAnnounced, protocol.ModelAnnouncedPayload{ConversationID: "conv", Model: model}), frame(protocol.TypeAssistantDelta, protocol.AssistantDeltaPayload{ConversationID: "conv", Text: text}), frame(protocol.TypeTurnState, protocol.TurnStatePayload{ConversationID: "conv", State: "idle"}))
	}
	correlate := func(kind string, value any) {
		env := frame(kind, value)
		id := sequence
		env.InReplyTo = &id
		rows = append(rows, env)
	}
	w := Wire{Send: func(kind string, value any) uint64 {
		sequence++
		switch kind {
		case protocol.TypeRequestModelList:
			menu := protocol.ModelListPayload{Models: []protocol.ModelOption{{Value: "haiku", ResolvedModel: "claude-haiku-4-5-20251001"}, {Value: "opus", ResolvedModel: "claude-opus-5-5"}}}
			if name == "family-dropped" {
				menu.DroppedModels = 1
			}
			if name == "family-truncated" {
				menu.Models[1].TruncatedFields = []string{"resolved_model"}
			}
			if name == "family-duplicate" {
				menu.Models = append(menu.Models, protocol.ModelOption{Value: "claude-opus-5-5", ResolvedModel: "claude-opus-5-5"})
			}
			correlate(protocol.TypeModelList, menu)
		case protocol.TypeSetSessionSettings:
			p := value.(protocol.SetSessionSettingsPayload)
			if p.SessionID != "session" || p.Model == nil || *p.Model != "claude-opus-5" || p.Effort != nil || p.PermissionMode != nil {
				t.Fatal("family update changed its model-only contract")
			}
			if name == "family-refused" {
				correlate(protocol.TypeError, protocol.ErrorPayload{})
			} else {
				correlate(protocol.TypeSessionSettingsUpdated, protocol.SessionSettingsUpdatedPayload{SessionID: "session"})
			}
		case protocol.TypeAttachmentChunk:
			p := value.(protocol.AttachmentChunkPayload)
			sum := sha256.Sum256(p.Data)
			if p.ConversationID != "conv" || p.AttachmentID != "attachment" || p.Index != 0 || p.TotalChunks != 1 || p.Size != int64(len(p.Data)) || p.SHA256 != hex.EncodeToString(sum[:]) {
				t.Fatal("attachment declaration lost its bytes/provenance")
			}
			dir := filepath.Join(home, ".pyry", "test", "conversations", "conv", "attachments", "attachment")
			if name != "attachment-no-directory" {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				filename := p.Filename
				if name == "attachment-wrong-filename" {
					filename = "wrong.txt"
				}
				if err := os.WriteFile(filepath.Join(dir, filename), p.Data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			id := p.AttachmentID
			if name == "attachment-wrong-id" {
				id = "other"
			}
			if name == "attachment-refused" {
				correlate(protocol.TypeError, protocol.ErrorPayload{})
			} else {
				correlate(protocol.TypeAttachmentStored, protocol.AttachmentStoredPayload{AttachmentID: id})
			}
		case protocol.TypeSendMessage:
			p := value.(protocol.SendMessagePayload)
			turns++
			if p.ConversationID != "conv" {
				t.Fatal("trigger lost conversation")
			}
			switch {
			case strings.HasPrefix(name, "family-"):
				model := "claude-haiku-4-5-20251001"
				if turns == 2 {
					model = "claude-opus-5-5"
				}
				if name == "family-unmoved" {
					model = "claude-haiku-4-5-20251001"
				}
				if name == "family-wrong-model" && turns == 2 {
					model = "unrelated"
				}
				appendTurn("reply", model)
			case strings.HasPrefix(name, "attachment-"):
				if len(p.AttachmentIDs) != 1 || p.AttachmentIDs[0] != "attachment" || strings.Contains(p.Text, "witness") {
					t.Fatal("attachment trigger lost its reference or disclosed contents")
				}
				if name == "attachment-modal" {
					rows = append(rows, frame(protocol.TypeModalShown, protocol.ModalShownPayload{}))
				}
				text := "witness"
				if name == "attachment-no-token" {
					text = "missing"
				}
				appendTurn(text, "model")
			case strings.HasPrefix(name, "bypass-"):
				if turns <= 2 {
					multi := turns == 2
					if name == "bypass-wrong-variant" {
						multi = !multi
					}
					options := []protocol.QuestionOption{{Label: "one"}, {Label: "two"}, {Label: "three"}, {Label: "four"}}
					if name == "bypass-duplicate-labels" {
						options[2].Label = "four"
					}
					rows = append(rows, frame(protocol.TypeQuestionShown, protocol.QuestionShownPayload{ConversationID: "conv", QuestionBatchID: fmt.Sprintf("batch-%d", turns), Questions: []protocol.Question{{MultiSelect: multi, Options: options}}}))
				} else {
					appendTurn("done", "model")
				}
			}
		case protocol.TypeQuestionAnswer:
			p := value.(protocol.QuestionAnswerPayload)
			answers++
			want := []string{"four"}
			if answers == 2 {
				want = []string{"four", "three", "custom"}
			}
			if p.QuestionBatchID != fmt.Sprintf("batch-%d", answers) || len(p.Answers) != 1 || strings.Join(p.Answers[0].Values, ",") != strings.Join(want, ",") {
				t.Fatal("bypass selection/free text changed")
			}
			now := time.Now().UTC()
			payload, _ := json.Marshal(map[string]any{"conversation_id": "conv", "correlation_id": p.QuestionBatchID, "decision": "answer", "source": "remote", "resolved_at": now, "context": map[string]any{"question": true}})
			shown := true
			session := "session"
			if name == "bypass-history-session" {
				session = "other"
			}
			entry := history.Entry{Type: "prompt_answered", Payload: payload, TS: now, Shown: &shown, Session: &history.SessionProvenance{Kind: "claude", SessionID: session}}
			raw, err := json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(home, ".pyry", "test", "conversations", "conv", "history")
			if err = os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(filepath.Join(dir, "segment-00000000000000000001.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.Write(append(raw, '\n'))
			_ = f.Close()
			if err != nil {
				t.Fatal(err)
			}
			rows = append(rows, frame(protocol.TypeQuestionDismissed, protocol.QuestionDismissedPayload{QuestionBatchID: p.QuestionBatchID, Outcome: "answered", Source: "remote"}))
			text := strings.Join(want, ",")
			if name == "bypass-missing-free-text" && answers == 2 {
				text = "four,three"
			}
			if name == "bypass-unchosen-first" {
				text = "one,four,three,custom"
			}
			appendTurn(text, "model")
		case protocol.TypeRequestSessionSettings:
			p := protocol.SessionSettingsPayload{SessionID: "session", PermissionMode: "bypassPermissions", YOLO: true}
			if name == "bypass-wrong-mode" {
				p.PermissionMode = "default"
			}
			if name == "bypass-no-yolo" {
				p.YOLO = false
			}
			if name == "bypass-session" {
				p.SessionID = "other"
			}
			correlate(protocol.TypeSessionSettings, p)
		default:
			t.Fatal("unexpected scenario action")
		}
		return sequence
	}, Next: func(deadline time.Time) protocol.Envelope {
		if len(rows) == 0 {
			time.Sleep(time.Millisecond)
			return protocol.Envelope{}
		}
		env := rows[0]
		rows = rows[1:]
		return env
	}}
	w = testSourceWire(t, w)
	budget := 40 * time.Millisecond
	switch {
	case strings.HasPrefix(name, "family-"):
		ModelFamily(t, w, "conv", "session", func(int) string { return "prompt" }, budget, budget)
	case strings.HasPrefix(name, "attachment-"):
		prompt := "read the file"
		if name == "attachment-prompt-leak" {
			prompt = "read witness"
		}
		AttachmentRead(t, w, AttachmentReadConfig{Home: home, ConversationID: "conv", AttachmentID: "attachment", Filename: "token.txt", Token: "witness", Prompt: prompt, Budget: budget})
	case strings.HasPrefix(name, "bypass-"):
		BypassQuestions(t, w, BypassQuestionsConfig{Home: home, ConversationID: "conv", SessionID: "session", Prompt: func(bool) string { return "question" }, FreeText: "custom", Witness: "body", Budget: budget, SurfaceBudget: budget, CheckWitness: func() { witness++ }})
		if witness != 1 || answers != 2 {
			t.Fatal("bypass ordinary witness or questions omitted")
		}
	}
	if len(rows) != 0 {
		t.Fatal("scenario left unread milestones")
	}
}
