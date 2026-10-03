package streamsup

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// midTurnUserCapturePath is the #2728 capture, read from here rather than behind
// the e2e_realclaude tag so the echo shape it recorded is pinned under plain
// go test.
const midTurnUserCapturePath = "../e2e/realclaude/testdata/mid_turn_user_v2.1.280.json"

// TestParser_UserEchoEmitsDigestOnly feeds every echo line the capture recorded
// — each arm's opener and every mid-turn write — and requires exactly one
// UserEcho carrying the SHA-256 of the echoed text, no other event (no
// Unrecognized in particular, whose Raw would carry the delivery payload), and no
// byte of the text in the parser's log.
func TestParser_UserEchoEmitsDigestOnly(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(midTurnUserCapturePath)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	var capture struct {
		Arms []struct {
			Name   string `json:"name"`
			Echoes []struct {
				Line string `json:"line"`
			} `json:"echoes"`
		} `json:"arms"`
	}
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatalf("decode capture: %v", err)
	}
	seen := 0
	for _, arm := range capture.Arms {
		for _, echo := range arm.Echoes {
			seen++
			var line struct {
				Message struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(echo.Line), &line); err != nil || len(line.Message.Content) != 1 {
				t.Fatalf("%s: echo line does not hold one text block: %v", arm.Name, err)
			}
			text := line.Message.Content[0].Text

			var logs bytes.Buffer
			var got []turnevent.Event
			p := NewParser(func(ev turnevent.Event) { got = append(got, ev) },
				slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			_, _ = p.Write([]byte(echo.Line + "\n"))

			want := turnevent.UserEcho{TextSHA256: sha256.Sum256([]byte(text))}
			if len(got) != 1 || got[0] != want {
				t.Errorf("%s: events = %#v, want exactly one %#v", arm.Name, got, want)
			}
			// The echo's text is the delivery payload: no word of it may reach a log.
			for _, word := range strings.Fields(text) {
				if len(word) > 8 && strings.Contains(logs.String(), word) {
					t.Errorf("%s: parser log carries echo text %q", arm.Name, word)
				}
			}
		}
	}
	if seen < 4 {
		t.Fatalf("capture held %d echo lines; the reader expected the four arms' openers and writes", seen)
	}
}

// TestParser_SubagentReplayLineIsNotAnEcho pins the parent-id carve-out: a
// replayed line stamped with a spawning Agent call is a subagent's delegated
// prompt, which the harness guard drops in silence, and must not be mistaken for
// an echo of a message the daemon wrote.
func TestParser_SubagentReplayLineIsNotAnEcho(t *testing.T) {
	t.Parallel()
	line := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"delegated"}]},"parent_tool_use_id":"toolu_parent","isReplay":true}`
	if got := collectEvents(line); len(got) != 0 {
		t.Fatalf("events = %#v, want none", got)
	}
}
