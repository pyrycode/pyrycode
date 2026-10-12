package streamsup

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
)

// This diagnostic replay proves the retained mobile turn's behavior. Its success
// cannot identify the rejecting guard in a different, unavailable recording.
func TestParser_ToolThenTextMobileCapture(t *testing.T) {
	t.Parallel()
	const name = "tool_then_text_mobile_stream_event_v2.1.280.json"
	testToolThenTextCapture(t, name, "mobile-daemon", "2.1.280")
	data, err := os.ReadFile("../e2e/realclaude/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Rows []string `json:"ordered_stdout"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	p := NewParser(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, row := range rec.Rows {
		if _, err := p.Write([]byte(row + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if p.streamMessageID != "" || p.streamBlockIndex != 0 || p.streamBlockType != "" ||
		p.streamBlockOpen || p.streamBlockTextDelta {
		t.Fatal("mobile result retained stream-event attribution or block state")
	}
}
