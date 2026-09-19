package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

const (
	replayAssistantLine = `{"type":"assistant","message":{"id":"fixture-message","role":"assistant","content":[{"type":"text","text":"from fixture"}]}}` + "\n"
	replayResultLine    = `{"type":"result","subtype":"success","session_id":"fixture-session"}` + "\n"
)

func TestRunStreamJSONConfigured_SingleFragmentReplay(t *testing.T) {
	t.Parallel()

	fragment := []byte(replayAssistantLine + "malformed fixture line\n" +
		`{"type":"future_fixture","payload":"preserve me"}` + "\n" + replayResultLine)
	var out bytes.Buffer
	runStreamJSONConfigured(
		strings.NewReader(userTurnLine("must not be echoed")+"\n"),
		&out,
		streamRunConfig{replay: &streamReplay{first: fragment}},
	)

	if !bytes.Equal(out.Bytes(), fragment) {
		t.Fatalf("replay bytes:\n got %q\nwant %q", out.Bytes(), fragment)
	}
	assertReplayEvents(t, out.Bytes())
}

func TestRunStreamJSONConfigured_TwoFragmentRelease(t *testing.T) {
	t.Parallel()

	const (
		firstFragment  = replayAssistantLine + `{"type":"future_fixture","phase":"first"}` + "\n"
		secondFragment = "malformed second fixture line\n" + replayResultLine
		firstAck       = `{"response":{"request_id":"during-replay","subtype":"success"},"type":"control_response"}` + "\n"
		secondAck      = `{"response":{"request_id":"after-release","subtype":"success"},"type":"control_response"}` + "\n"
	)

	releasePath := filepath.Join(t.TempDir(), "release")
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	defer stdinW.Close()

	var out synchronizedBuffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		runStreamJSONConfigured(stdinR, &out, streamRunConfig{
			replay: &streamReplay{
				first:       []byte(firstFragment),
				second:      []byte(secondFragment),
				releasePath: releasePath,
			},
			inputCloser: stdinR,
		})
	}()

	writeReplayInput(t, stdinW, userTurnLine("start replay"))
	waitForReplayOutput(t, &out, func(got string) bool { return got == firstFragment })

	writeReplayInput(t, stdinW, userTurnLine("must be ignored while held"))
	writeReplayInput(t, stdinW,
		`{"type":"control_request","request_id":"during-replay","request":{"subtype":"set_model","model":"sonnet"}}`)
	wantHeld := firstFragment + firstAck
	waitForReplayOutput(t, &out, func(got string) bool { return got == wantHeld })

	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatalf("create release signal: %v", err)
	}
	wantReleased := wantHeld + secondFragment
	waitForReplayOutput(t, &out, func(got string) bool { return got == wantReleased })

	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatalf("recreate release signal: %v", err)
	}
	writeReplayInput(t, stdinW,
		`{"type":"control_request","request_id":"after-release","request":{"subtype":"set_model","model":"haiku"}}`)
	wantFinal := wantReleased + secondAck
	waitForReplayOutput(t, &out, func(got string) bool { return got == wantFinal })

	if err := stdinW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream loop did not stop after stdin closed")
	}

	if got := out.String(); got != wantFinal {
		t.Fatalf("final replay bytes:\n got %q\nwant %q", got, wantFinal)
	}
	assertReplayEvents(t, []byte(wantReleased))
}

func TestRunStreamJSON_ReplayAbsentUsesCannedResponse(t *testing.T) {
	t.Parallel()

	const prompt = "ordinary canned turn"
	var out bytes.Buffer
	runStreamJSON(strings.NewReader(userTurnLine(prompt)+"\n"), &out,
		false, false, "", false, 0, false, "", false)

	if !strings.Contains(out.String(), `"text":"ordinary canned turn"`) {
		t.Fatalf("canned assistant echo missing from %q", out.String())
	}
	events := parseEmitted(t, out.Bytes())
	if len(events) != 2 {
		t.Fatalf("canned event count = %d, want 2: %+v", len(events), events)
	}
	if _, ok := events[0].(turnevent.TextChunk); !ok {
		t.Fatalf("event[0] = %T, want turnevent.TextChunk", events[0])
	}
	if _, ok := events[1].(turnevent.TurnEnd); !ok {
		t.Fatalf("event[1] = %T, want turnevent.TurnEnd", events[1])
	}
}

func TestLoadStreamReplay(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.stream-json")
	secondPath := filepath.Join(dir, "second.stream-json")
	releasePath := filepath.Join(dir, "release")
	if err := os.WriteFile(firstPath, []byte("first\n"), 0o600); err != nil {
		t.Fatalf("write first fragment: %v", err)
	}
	if err := os.WriteFile(secondPath, []byte("second"), 0o600); err != nil {
		t.Fatalf("write second fragment: %v", err)
	}

	tests := []struct {
		name        string
		firstPath   string
		secondPath  string
		releasePath string
		wantNil     bool
		wantSecond  bool
		wantErr     bool
	}{
		{name: "disabled", wantNil: true},
		{name: "single", firstPath: firstPath},
		{name: "paired", firstPath: firstPath, secondPath: secondPath, releasePath: releasePath, wantSecond: true},
		{name: "second without release", firstPath: firstPath, secondPath: secondPath, wantErr: true},
		{name: "release without second", firstPath: firstPath, releasePath: releasePath, wantErr: true},
		{name: "missing first", firstPath: filepath.Join(dir, "missing-first"), wantErr: true},
		{name: "missing second", firstPath: firstPath, secondPath: filepath.Join(dir, "missing-second"), releasePath: releasePath, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			replay, err := loadStreamReplay(tt.firstPath, tt.secondPath, tt.releasePath)
			if (err != nil) != tt.wantErr {
				t.Fatalf("loadStreamReplay() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if (replay == nil) != tt.wantNil {
				t.Fatalf("loadStreamReplay() replay nil = %v, want %v", replay == nil, tt.wantNil)
			}
			if replay == nil {
				return
			}
			if got := string(replay.first); got != "first\n" {
				t.Errorf("first fragment = %q, want %q", got, "first\\n")
			}
			if got := len(replay.second) > 0; got != tt.wantSecond {
				t.Errorf("second fragment present = %v, want %v", got, tt.wantSecond)
			}
			if got := replay.releasePath; got != tt.releasePath {
				t.Errorf("release path = %q, want %q", got, tt.releasePath)
			}
		})
	}
}

type synchronizedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func writeReplayInput(t *testing.T, w *os.File, line string) {
	t.Helper()
	if _, err := w.WriteString(line + "\n"); err != nil {
		t.Fatalf("write stream input: %v", err)
	}
}

func waitForReplayOutput(t *testing.T, out *synchronizedBuffer, ready func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := out.String()
		if ready(got) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := out.String()
	t.Fatalf("timed out waiting for replay output; got %q", got)
	return ""
}

func assertReplayEvents(t *testing.T, output []byte) {
	t.Helper()
	events := parseEmitted(t, output)
	var textChunks, turnEnds int
	for _, event := range events {
		switch event := event.(type) {
		case turnevent.TextChunk:
			textChunks++
			if event.Text != "from fixture" {
				t.Errorf("TextChunk.Text = %q, want %q", event.Text, "from fixture")
			}
		case turnevent.TurnEnd:
			turnEnds++
			if event.Reason != turnevent.TurnEndReasonEndTurn {
				t.Errorf("TurnEnd.Reason = %v, want %v", event.Reason, turnevent.TurnEndReasonEndTurn)
			}
		}
	}
	if textChunks != 1 || turnEnds != 1 {
		t.Fatalf("parsed replay events = %+v, want one TextChunk and one TurnEnd", events)
	}
}
