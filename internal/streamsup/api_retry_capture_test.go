package streamsup

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const apiRetryCaptureVersion = "2.1.259"

const apiRetryCapturePath = "../e2e/realclaude/testdata/api_retry_v" +
	apiRetryCaptureVersion + ".json"

type apiRetryCapture struct {
	IsCapture     bool   `json:"is_capture"`
	ClaudeVersion string `json:"claude_version"`
	Frames        []struct {
		Index           int    `json:"index"`
		Type            string `json:"type"`
		Subtype         string `json:"subtype"`
		APIRetry        bool   `json:"api_retry"`
		PayloadEncoding string `json:"payload_encoding"`
		Payload         string `json:"payload"`
	} `json:"frames"`
}

func readAPIRetryCapture(t *testing.T) apiRetryCapture {
	t.Helper()
	raw, err := os.ReadFile(apiRetryCapturePath)
	if err != nil {
		t.Fatalf("reading committed capture %s: %v", apiRetryCapturePath, err)
	}
	var capture apiRetryCapture
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatalf("decoding committed capture %s: %v", apiRetryCapturePath, err)
	}
	if !capture.IsCapture {
		t.Fatalf("%s: is_capture is false; replay requires captured Claude output", apiRetryCapturePath)
	}
	if got, _, _ := strings.Cut(capture.ClaudeVersion, " "); got != apiRetryCaptureVersion {
		t.Fatalf("%s: claude_version = %q, want release %q", apiRetryCapturePath,
			capture.ClaudeVersion, apiRetryCaptureVersion)
	}

	retryFrames := 0
	for _, frame := range capture.Frames {
		if frame.PayloadEncoding != "json-string" || frame.Payload == "" {
			t.Fatalf("%s: frame %d has payload encoding %q and empty=%t; every frame must retain replayable bytes",
				apiRetryCapturePath, frame.Index, frame.PayloadEncoding, frame.Payload == "")
		}
		var envelope struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
		}
		if err := json.Unmarshal([]byte(frame.Payload), &envelope); err != nil {
			t.Fatalf("%s: frame %d payload does not decode as JSON: %v", apiRetryCapturePath, frame.Index, err)
		}
		if envelope.Type != frame.Type || envelope.Subtype != frame.Subtype {
			t.Fatalf("%s: frame %d labels %q/%q disagree with payload %q/%q", apiRetryCapturePath,
				frame.Index, frame.Type, frame.Subtype, envelope.Type, envelope.Subtype)
		}
		payloadIsRetry := envelope.Type == "system" && envelope.Subtype == "api_retry"
		if frame.APIRetry != payloadIsRetry {
			t.Fatalf("%s: frame %d api_retry=%t but payload identifies retry=%t", apiRetryCapturePath,
				frame.Index, frame.APIRetry, payloadIsRetry)
		}
		if frame.APIRetry {
			retryFrames++
		}
	}
	if retryFrames == 0 {
		t.Fatalf("%s: capture has no frame flagged api_retry", apiRetryCapturePath)
	}
	return capture
}

func TestAPIRetryCaptureReplay(t *testing.T) {
	capture := readAPIRetryCapture(t)

	var events []turnevent.Event
	p := NewParser(func(ev turnevent.Event) { events = append(events, ev) }, discardLogger())
	for _, frame := range capture.Frames {
		if _, err := p.Write([]byte(frame.Payload + "\n")); err != nil {
			t.Fatalf("%s: replaying frame %d: %v", apiRetryCapturePath, frame.Index, err)
		}
	}

	var retries []turnevent.ApiRetry
	clearIndex := -1
	assistantIndex := -1
	for i, ev := range events {
		switch e := ev.(type) {
		case turnevent.ApiRetry:
			retries = append(retries, e)
			if !e.Active {
				if clearIndex >= 0 {
					t.Fatalf("second retry clear at event %d; first was %d", i, clearIndex)
				}
				clearIndex = i
			}
		case turnevent.TextChunk:
			if assistantIndex < 0 {
				assistantIndex = i
			}
		case turnevent.Unrecognized:
			t.Fatalf("captured turn emitted Unrecognized at event %d: site=%q kind=%q", i, e.Site, e.Kind)
		}
	}

	if len(retries) != 11 {
		t.Fatalf("retry events = %d, want 10 active updates and one clear", len(retries))
	}
	for i := 0; i < 10; i++ {
		wantCurrent := i + 1
		if got := retries[i]; !got.Active || got.Current != wantCurrent || got.Total != 10 {
			t.Errorf("retry event %d = %+v, want Active=true Current=%d Total=10", i, got, wantCurrent)
		}
	}
	if got := retries[10]; got != (turnevent.ApiRetry{Active: false}) {
		t.Errorf("last retry event = %+v, want one zero-valued inactive clear", got)
	}
	if clearIndex < 0 || assistantIndex < 0 || clearIndex >= assistantIndex {
		t.Fatalf("clear event index = %d, assistant event index = %d; clear must precede assistant output",
			clearIndex, assistantIndex)
	}
}
