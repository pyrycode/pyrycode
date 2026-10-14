package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestValidReplyFallbackVoice(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		text string
		ok   bool
	}{
		// The user's own next message.
		{"Yes, go ahead.", true},
		{"yes", true},
		{"Merge it and cut a release", true},
		{"No, keep the old behaviour for now", true},
		{"What does the log say about the restart?", true},
		{"Surely that only affects the Mac?", true},
		{"Use v1.2 instead", true},
		{"Ship it, e.g. tonight after the tests pass", true},
		{"Try again with the new token", true},
		// User-voice openers the assistant also uses.
		{"Sure, go ahead", true},
		{"Absolutely, ship it", true},
		{"Of course, merge it", true},
		{"Let me know when it's done", true},
		{"I'll test it tonight", true},
		{"I will check the logs myself", true},
		{"You're right, let's do that", true},
		{"Here's the error log", true},
		{"Sure thing, do it", true},
		{"Certainly not the Mac", true},
		// A short lead-in and one more sentence.
		{"Yes. Go ahead and merge", true},
		{"Thanks! Can you add tests too?", true},
		{"Sounds good. Ship it.", true},
		{"No. Keep the old prompt", true},
		// The assistant's voice.
		{"I apologize for the confusion", false},
		{"I apologise, that was my mistake", false},
		{"I'm sorry, I missed that", false},
		{"I\u2019m sorry about the restart", false},
		{"I'd be happy to help with that", false},
		{"I can help you set that up", false},
		{"Would you like me to merge it?", false},
		{"Do you want me to restart the daemon?", false},
		{"Great question about the relay", false},
		{"\"Would you like the full log?\"", false},
		{"Sure. Would you like me to merge it?", false},
		{"Thanks! I'd be happy to help", false},
		// More than one sentence beyond a short lead-in.
		{"Yes. Merge it. Then release", false},
		{"Merge it. Then cut a release", false},
		{"Why? It worked yesterday", false},
		{"The tests pass. Ship it", false},
		// More than twelve words.
		{"Please also update the docs and the changelog before you open the pull request", false},
		{"Thanks! Please also update the docs and the changelog before opening the PR", false},
	} {
		if got := validReplyFallback(tc.text); got != tc.ok {
			t.Errorf("validReplyFallback(%q) = %v, want %v", tc.text, got, tc.ok)
		}
	}
}

func TestReplyExchangeAssistantKeepsTail(t *testing.T) {
	t.Parallel()
	question := "Want me to open the pull request now?"
	for _, tc := range []struct {
		name  string
		reply string
	}{
		{"ascii", strings.Repeat("report line\n", 2000) + question},
		{"multibyte", strings.Repeat("raportti ääkkösillä 😀\n", 900) + question},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := replyExchangeAssistant(replyExchangePrefix(tc.reply), replyExchangeTail(tc.reply, replyExchangeBytes), len(tc.reply))
			if len(got) > replyExchangeBytes || !utf8.ValidString(got) {
				t.Fatalf("len=%d valid=%v", len(got), utf8.ValidString(got))
			}
			if !strings.HasSuffix(got, question) {
				t.Fatal("closing question dropped")
			}
			if !strings.HasPrefix(tc.reply, strings.SplitN(got, replyExchangeOmitted, 2)[0]) || strings.Count(got, replyExchangeOmitted) != 1 {
				t.Fatal("opening not kept before one omission marker")
			}
		})
	}
	short := "Done. Want me to merge it?"
	if got := replyExchangeAssistant(short, short, len(short)); got != short {
		t.Fatalf("short reply changed: %q", got)
	}
	exact := strings.Repeat("a", replyExchangeBytes)
	if got := replyExchangeAssistant(exact, exact, len(exact)); got != exact {
		t.Fatal("reply at the budget was clipped")
	}
}

// A streamed long reply reaches the fallback with its end, across chunks.
func TestReplyFallbackInputKeepsStreamedTail(t *testing.T) {
	_, s := newSuggestionEmitter(testConvID)
	s.beginWrite(testConvID, 1)
	s.noteDelivered(testConvID, msgqueue.QueuedMessage{ID: 1, Text: "status?"})
	var full strings.Builder
	for i := range 300 {
		chunk := strings.Repeat("x", 97) + string(rune('a'+i%26)) + "\n"
		full.WriteString(chunk)
		s.noteAssistantText(testConvID, turnevent.TextChunk{MessageID: "m", Text: chunk})
	}
	s.noteAssistantText(testConvID, turnevent.TextChunk{MessageID: "m", Text: "Restart pyrybox now?"})
	full.WriteString("Restart pyrybox now?")
	s.mu.Lock()
	c := s.convs[testConvID]
	got := replyExchangeAssistant(c.assistantText, c.assistantTail, c.assistantBytes)
	s.mu.Unlock()
	want := replyExchangeAssistant(replyExchangePrefix(full.String()), replyExchangeTail(full.String(), replyExchangeBytes), full.Len())
	if got != want || !strings.HasSuffix(got, "Restart pyrybox now?") || len(got) > replyExchangeBytes {
		t.Fatalf("streamed exchange len=%d, want len=%d", len(got), len(want))
	}
	// A new final message drops the previous message's tail.
	s.noteAssistantText(testConvID, turnevent.TextChunk{MessageID: "m2", Text: "Short."})
	s.mu.Lock()
	defer s.mu.Unlock()
	if got := replyExchangeAssistant(c.assistantText, c.assistantTail, c.assistantBytes); got != "Short." {
		t.Fatalf("new message kept old text: %q", got)
	}
}
