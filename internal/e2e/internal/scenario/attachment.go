package scenario

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

type AttachmentReadConfig struct {
	Home, ConversationID, AttachmentID, Filename, Token, Prompt string
	Budget                                                      time.Duration
}

// AttachmentRead keeps upload-before-routing, request correlation, actual stored
// file checks and the original attributed reply/idle token assertion. Live uses
// an unpredictable token; recorded playback retains only protocol/data proof.
func AttachmentRead(t *testing.T, w Wire, c AttachmentReadConfig) {
	t.Helper()
	if c.Token == "" || strings.Contains(c.Prompt, c.Token) {
		t.Fatal("attachment witness is empty or leaked into prompt")
	}
	// The source is outside the daemon HOME/workdir, so listing the workspace
	// cannot find it. Hash the bytes read back, independently of the declaration.
	source := filepath.Join(t.TempDir(), c.Filename)
	if err := os.WriteFile(source, []byte(c.Token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(file)
	chunks := len(file) / protocol.MaxAttachmentChunkBytes
	if len(file)%protocol.MaxAttachmentChunkBytes != 0 {
		chunks++
	}
	chunks = max(1, chunks)
	if chunks != 1 {
		t.Fatal("attachment fixture is not exactly one chunk")
	}
	id := w.Send(protocol.TypeAttachmentChunk, protocol.AttachmentChunkPayload{ConversationID: c.ConversationID, AttachmentID: c.AttachmentID, Index: 0, TotalChunks: chunks, Filename: c.Filename, MimeType: "text/plain", Size: int64(len(file)), SHA256: hex.EncodeToString(sum[:]), Data: file})
	deadline := time.Now().Add(30 * time.Second)
	stored := false
	for time.Now().Before(deadline) {
		env := w.Next(deadline)
		if env.Type == protocol.TypeError {
			t.Fatal("attachment upload was refused")
		}
		if env.Type != protocol.TypeAttachmentStored || env.InReplyTo == nil || *env.InReplyTo != id {
			continue
		}
		var p protocol.AttachmentStoredPayload
		if json.Unmarshal(env.Payload, &p) != nil || p.AttachmentID != c.AttachmentID {
			t.Fatal("attachment receipt lost its identity")
		}
		stored = true
		break
	}
	if !stored {
		t.Fatal("attachment upload has no correlated stored receipt")
	}
	dir := filepath.Join(c.Home, ".pyry", "test", "conversations", c.ConversationID, "attachments", c.AttachmentID)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != c.Filename {
		t.Fatal("stored attachment directory/filename differs from receipt")
	}
	w.Send(protocol.TypeSendMessage, protocol.SendMessagePayload{ConversationID: c.ConversationID, MessageID: "m-attach", Text: c.Prompt, AttachmentIDs: []string{c.AttachmentID}})
	reply := AttachmentTurn(t, w.Next, c.ConversationID, c.Budget, w.Source)
	if !strings.Contains(reply, c.Token) {
		t.Fatal("attachment reply omitted the file-only witness")
	}
}

// AttachmentTurn retains the original attachment drain's alarms and milestones.
// Parser/rate alarms were not in that live drain and are not substituted here.
func AttachmentTurn(t *testing.T, next Next, convID string, budget time.Duration, sources ...SourceFacts) string {
	t.Helper()
	var turn sourceTurn
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		env := next(pollDeadline(deadline))
		turn.feed(t, convID, sourceFacts(t, sources, convID))
		switch env.Type {
		case protocol.TypeError, protocol.TypeModalShown:
			t.Fatal("attachment turn was refused or parked behind a modal")
		case protocol.TypeToolUse:
			var p protocol.ToolUsePayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid attachment tool-use frame")
			}
		case protocol.TypeAssistantDelta:
			t.Fatal("retired attachment reply delivered on wire")
		case protocol.TypeTurnState:
			var p protocol.TurnStatePayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid attachment turn state")
			}
			if turn.idle(t, env, convID) && strings.TrimSpace(turn.text.String()) != "" {
				return turn.text.String()
			}
		}
	}
	t.Fatal("attachment turn lacks nonempty owned text and terminal idle")
	return ""
}
