package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// recordedAppend is one call channelPoster made to the durable log.
type recordedAppend struct {
	convID  conversations.ConversationID
	typ     string
	payload json.RawMessage
	ts      time.Time
}

// stubAppender stands in for history.Store.Append, narrowed to the signature
// channelPoster takes. A concrete *history.Store would put a real instance
// directory and a segment-file layout between the test and the one property
// under test: what bytes the poster hands the log.
type stubAppender struct {
	calls     []recordedAppend
	returnErr error
}

func (s *stubAppender) append(convID conversations.ConversationID, typ string, payload json.RawMessage, ts time.Time) (uint64, error) {
	s.calls = append(s.calls, recordedAppend{convID: convID, typ: typ, payload: payload, ts: ts})
	if s.returnErr != nil {
		return 0, s.returnErr
	}
	return uint64(len(s.calls)), nil
}

// stubCreate records what the poster asked channelCreator to create on the
// name-miss path, and answers with a canned id or error.
type stubCreate struct {
	gotCwd    string
	gotName   string
	calls     int
	returnID  string
	returnErr error
}

func (s *stubCreate) create(cwd, name string) (string, error) {
	s.calls++
	s.gotCwd, s.gotName = cwd, name
	if s.returnErr != nil {
		return "", s.returnErr
	}
	return s.returnID, nil
}

// addConversation puts one row in the registry with the promoted/archived state
// and name the test needs. The id is generated so no two rows collide.
func addConversation(t *testing.T, reg *conversations.Registry, name string, promoted, archived bool) conversations.ConversationID {
	t.Helper()
	id, err := conversations.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	reg.Create(conversations.Conversation{
		ID:         id,
		Name:       &name,
		Cwd:        "/home/op/project",
		IsPromoted: promoted,
		IsArchived: archived,
		LastUsedAt: time.Now().UTC(),
	})
	return id
}

// stubAnnouncer records what the poster handed the live fan-out (#2498). It
// returns nothing, which is the hook's contract rather than a simplification:
// a failed announcement must never turn a recorded post into a refusal.
type stubAnnouncer struct {
	calls []protocol.AssistantDeltaPayload
}

func (s *stubAnnouncer) announce(p protocol.AssistantDeltaPayload) {
	s.calls = append(s.calls, p)
}

// newTestPoster builds the poster under test over the package's existing
// quietLogger: these tests assert on returned values and recorded calls, never
// on log lines.
func newTestPoster(t *testing.T, reg *conversations.Registry, create *stubCreate, appender *stubAppender) func(name, text string) error {
	t.Helper()
	return newTestPosterAnnouncing(t, reg, create, appender, &stubAnnouncer{})
}

// newTestPosterAnnouncing is newTestPoster with the fan-out hook visible, for
// the cases whose property is what reached the wire. Split rather than threaded
// through every call site so the existing cases keep reading as what they are —
// assertions about the durable record.
func newTestPosterAnnouncing(t *testing.T, reg *conversations.Registry, create *stubCreate, appender *stubAppender, announcer *stubAnnouncer) func(name, text string) error {
	t.Helper()
	var announce func(protocol.AssistantDeltaPayload)
	if announcer != nil {
		announce = announcer.announce
	}
	return channelPoster(reg, create.create, "/home/op/default", appender.append, announce, quietLogger())
}

// TestChannelPoster_PostsIntoExactMatch is the happy path: a single promoted,
// non-archived channel with the requested name receives the content as ONE
// assistant_delta entry — the type the current clients draw as assistant text,
// live and from a served history page alike.
//
// It records the entry type #2498 changed and why. #2497 wrote a `message` entry
// with role assistant, which reaches a client and draws NOTHING: desktop's
// translateTimelineEvent returns a row only for role "user" and null for
// "assistant", on the live path and the history path both. The shape that draws
// is assistant_delta, so that is the shape the log holds.
//
// The payload is decoded rather than compared as bytes: the property is the
// SHAPE a client reads back, not this producer's field order.
func TestChannelPoster_PostsIntoExactMatch(t *testing.T) {
	reg, _ := newChannelTestRegistry(t, t.TempDir())
	want := addConversation(t, reg, "questions", true, false)

	create := &stubCreate{}
	appender := &stubAppender{}
	post := newTestPoster(t, reg, create, appender)

	const text = "What is the one thing you are avoiding today?"
	before := time.Now().UTC()
	if err := post("questions", text); err != nil {
		t.Fatalf("post: %v", err)
	}

	if create.calls != 0 {
		t.Errorf("create called %d time(s), want 0 — an exact match must not create", create.calls)
	}
	if len(appender.calls) != 1 {
		t.Fatalf("append calls = %d, want 1 — one message per call", len(appender.calls))
	}
	got := appender.calls[0]
	if got.convID != want {
		t.Errorf("appended to conversation %q, want the matched row %q", got.convID, want)
	}
	if got.typ != protocol.TypeAssistantDelta {
		t.Errorf("entry type = %q, want %q", got.typ, protocol.TypeAssistantDelta)
	}
	if got.ts.Before(before) || got.ts.After(time.Now().UTC().Add(time.Second)) {
		t.Errorf("entry ts = %v, want a stamp taken at the write", got.ts)
	}

	var delta protocol.AssistantDeltaPayload
	if err := json.Unmarshal(got.payload, &delta); err != nil {
		t.Fatalf("decode appended payload: %v", err)
	}
	if delta.Text != text {
		t.Errorf("payload text = %q, want %q", delta.Text, text)
	}
	if delta.ConversationID != string(want) {
		t.Errorf("payload conversation_id = %q, want %q", delta.ConversationID, want)
	}
	if !conversations.ValidID(delta.TurnID) {
		t.Errorf("payload turn_id = %q, want a freshly minted canonical id", delta.TurnID)
	}
	if delta.Seq != 0 {
		t.Errorf("payload seq = %d, want 0 — the first frame of the post's own turn", delta.Seq)
	}
	// The main lane. A post is the host's own text, not a subagent's, so joining
	// it to a parent tool call would claim an attribution nothing produced.
	if delta.ParentToolUseID != "" {
		t.Errorf("payload parent_tool_use_id = %q, want empty — a post is main-lane text", delta.ParentToolUseID)
	}
}

// TestChannelPoster_AnnouncesWhatItRecorded is AC#2 as ONE assertion rather than
// two derivations that agree today: the bytes handed to the durable log and the
// payload handed to the live fan-out must be the same value, so a client drawing
// the push and a client paging history draw the same message.
//
// It also pins the ORDER. The append runs first and the announcement second,
// because a failed append is a failed post while a failed push is not — so a
// post that answers success has always been recorded, and a post that refuses
// has pushed nothing.
func TestChannelPoster_AnnouncesWhatItRecorded(t *testing.T) {
	reg, _ := newChannelTestRegistry(t, t.TempDir())
	want := addConversation(t, reg, "questions", true, false)

	create := &stubCreate{}
	appender := &stubAppender{}
	announcer := &stubAnnouncer{}
	post := newTestPosterAnnouncing(t, reg, create, appender, announcer)

	const text = "the relay is back up"
	if err := post("questions", text); err != nil {
		t.Fatalf("post: %v", err)
	}

	if len(announcer.calls) != 1 {
		t.Fatalf("announce calls = %d, want 1 — one frame per single-chunk post", len(announcer.calls))
	}
	if len(appender.calls) != 1 {
		t.Fatalf("append calls = %d, want 1", len(appender.calls))
	}

	var recorded protocol.AssistantDeltaPayload
	if err := json.Unmarshal(appender.calls[0].payload, &recorded); err != nil {
		t.Fatalf("decode appended payload: %v", err)
	}
	if announcer.calls[0] != recorded {
		t.Errorf("announced %+v but recorded %+v; the pushed frame and the durable record are one shape",
			announcer.calls[0], recorded)
	}
	if recorded.ConversationID != string(want) {
		t.Errorf("recorded conversation_id = %q, want the matched row %q", recorded.ConversationID, want)
	}
}

// TestChannelPoster_AppendFailureAnnouncesNothing is the other half of that
// order: a post that could not be recorded must not appear on any client's
// screen. A frame drawn for a message the log does not hold would vanish on the
// next connect, which is worse than never having been drawn.
func TestChannelPoster_AppendFailureAnnouncesNothing(t *testing.T) {
	reg, _ := newChannelTestRegistry(t, t.TempDir())
	addConversation(t, reg, "questions", true, false)

	create := &stubCreate{}
	appender := &stubAppender{returnErr: errors.New("history: open segment: disk on fire")}
	announcer := &stubAnnouncer{}
	post := newTestPosterAnnouncing(t, reg, create, appender, announcer)

	if err := post("questions", "hello"); err == nil {
		t.Fatal("post = nil, want a failed append to fail the post")
	}
	if len(announcer.calls) != 0 {
		t.Errorf("announced %d frame(s) for a message that was never recorded", len(announcer.calls))
	}
}

// TestChannelPoster_NilAnnouncerStillRecords covers the daemon with no relay leg:
// startRelay returns before any manager exists when no URL is configured, so the
// hook is nil and there is nobody to tell. The post is still recorded and still
// answers success — AC#4's no-client-attached arm.
func TestChannelPoster_NilAnnouncerStillRecords(t *testing.T) {
	reg, _ := newChannelTestRegistry(t, t.TempDir())
	addConversation(t, reg, "questions", true, false)

	create := &stubCreate{}
	appender := &stubAppender{}
	post := newTestPosterAnnouncing(t, reg, create, appender, nil)

	if err := post("questions", "hello"); err != nil {
		t.Fatalf("post with no relay leg: %v", err)
	}
	if len(appender.calls) != 1 {
		t.Errorf("append calls = %d, want 1 — a nil hook changes nothing about the record", len(appender.calls))
	}
}

// TestChannelPoster_EachPostMintsItsOwnTurn is AC#3 stated as the property that
// enforces it: one post is one rendering because one post is one turn_id, and a
// client coalesces assistant_delta by that key. Two posts sharing a turn id
// would join into a single bubble, which is what a fallback-to-empty mint on an
// rng failure would have produced for every post at once.
func TestChannelPoster_EachPostMintsItsOwnTurn(t *testing.T) {
	reg, _ := newChannelTestRegistry(t, t.TempDir())
	addConversation(t, reg, "questions", true, false)

	create := &stubCreate{}
	appender := &stubAppender{}
	announcer := &stubAnnouncer{}
	post := newTestPosterAnnouncing(t, reg, create, appender, announcer)

	if err := post("questions", "first"); err != nil {
		t.Fatalf("post: %v", err)
	}
	if err := post("questions", "second"); err != nil {
		t.Fatalf("post: %v", err)
	}

	if len(announcer.calls) != 2 {
		t.Fatalf("announce calls = %d, want 2", len(announcer.calls))
	}
	if announcer.calls[0].TurnID == announcer.calls[1].TurnID {
		t.Errorf("both posts carry turn_id %q; a client coalesces on it and would draw one message",
			announcer.calls[0].TurnID)
	}
}

// TestChannelPoster_IgnoresArchivedAndUnpromoted is the mutation guard on the
// filter. ListFilter's fields are pointers that AND, and a nil field filters
// nothing — so a filter that set only IsPromoted would match the archived row
// here and post into a channel the operator retired. Both decoys carry the
// requested name, so a poster that scanned the whole registry finds three
// matches and refuses, and one that filtered on neither posts into the wrong row.
func TestChannelPoster_IgnoresArchivedAndUnpromoted(t *testing.T) {
	reg, _ := newChannelTestRegistry(t, t.TempDir())
	addConversation(t, reg, "questions", true, true)   // promoted but archived
	addConversation(t, reg, "questions", false, false) // a discussion, not a channel
	want := addConversation(t, reg, "questions", true, false)

	create := &stubCreate{}
	appender := &stubAppender{}
	post := newTestPoster(t, reg, create, appender)

	if err := post("questions", "hello"); err != nil {
		t.Fatalf("post: %v", err)
	}
	if len(appender.calls) != 1 {
		t.Fatalf("append calls = %d, want 1", len(appender.calls))
	}
	if got := appender.calls[0].convID; got != want {
		t.Errorf("appended to %q, want the one live channel %q", got, want)
	}
}

// TestChannelPoster_CreatesOnMiss covers AC#2's no-match arm: the channel is
// created under the daemon's default workspace — never a caller-supplied path,
// because this verb has no cwd on the wire — and the content lands in the row
// the creator returned.
func TestChannelPoster_CreatesOnMiss(t *testing.T) {
	reg, _ := newChannelTestRegistry(t, t.TempDir())
	addConversation(t, reg, "something-else", true, false)

	created, err := conversations.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	create := &stubCreate{returnID: string(created)}
	appender := &stubAppender{}
	post := newTestPoster(t, reg, create, appender)

	if err := post("questions", "hello"); err != nil {
		t.Fatalf("post: %v", err)
	}
	if create.calls != 1 {
		t.Fatalf("create calls = %d, want 1", create.calls)
	}
	if create.gotCwd != "/home/op/default" {
		t.Errorf("create got cwd %q, want the daemon's default workspace", create.gotCwd)
	}
	if create.gotName != "questions" {
		t.Errorf("create got name %q, want the requested label", create.gotName)
	}
	if len(appender.calls) != 1 || appender.calls[0].convID != created {
		t.Fatalf("append calls = %+v, want one into the created row %q", appender.calls, created)
	}
}

// TestChannelPoster_RefusesAmbiguousName covers AC#2's two-or-more arm. It
// asserts the count is named, that NOTHING was created or appended, and — the
// security-review line — that the refusal does not echo the requested name back,
// which reaches the wire verbatim.
func TestChannelPoster_RefusesAmbiguousName(t *testing.T) {
	reg, _ := newChannelTestRegistry(t, t.TempDir())
	addConversation(t, reg, "questions", true, false)
	addConversation(t, reg, "questions", true, false)
	addConversation(t, reg, "questions", true, false)

	create := &stubCreate{}
	appender := &stubAppender{}
	post := newTestPoster(t, reg, create, appender)

	err := post("questions", "hello")
	if err == nil {
		t.Fatal("post = nil, want a refusal naming the count")
	}
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("refusal %q does not name the count", err.Error())
	}
	if strings.Contains(err.Error(), "questions") {
		t.Errorf("refusal %q echoes the requested name; it reaches the wire verbatim", err.Error())
	}
	if create.calls != 0 || len(appender.calls) != 0 {
		t.Errorf("create=%d append=%d, want 0 and 0 — an ambiguous name posts nothing", create.calls, len(appender.calls))
	}
}

// TestChannelPoster_ForwardsCreateRefusal pins that the creator's own static
// refusals reach the caller unchanged. They are already constants for the reason
// channelCreator's doc block records, so re-wrapping them here would either
// double the prefix or replace a precise reason with a vague one.
func TestChannelPoster_ForwardsCreateRefusal(t *testing.T) {
	reg, _ := newChannelTestRegistry(t, t.TempDir())
	create := &stubCreate{returnErr: errors.New(msgChannelCwdRejected)}
	appender := &stubAppender{}
	post := newTestPoster(t, reg, create, appender)

	err := post("questions", "hello")
	if err == nil {
		t.Fatal("post = nil, want the creator's refusal")
	}
	if err.Error() != msgChannelCwdRejected {
		t.Errorf("err = %q, want the creator's reason verbatim", err.Error())
	}
	if len(appender.calls) != 0 {
		t.Errorf("append called %d time(s) after a failed create", len(appender.calls))
	}
}

// TestChannelPoster_AppendFailureIsAPostFailure is the reason this seam does not
// route through appendConversationHistory. That helper returns nothing by
// contract, so a caller cannot tell a dropped entry from a written one — correct
// for a stream producer whose frame has already gone out, and wrong here, where
// the durable record IS the deliverable. A cron that exits 0 having delivered
// nothing is the failure this test exists to prevent.
//
// It also pins that the refusal carries none of internal/history's error text,
// whose messages format absolute filesystem paths.
func TestChannelPoster_AppendFailureIsAPostFailure(t *testing.T) {
	reg, _ := newChannelTestRegistry(t, t.TempDir())
	addConversation(t, reg, "questions", true, false)

	create := &stubCreate{}
	appender := &stubAppender{returnErr: fmt.Errorf("history: open segment %q: disk on fire", "/home/op/.pyry/test/conversations/x/history/seg")}
	post := newTestPoster(t, reg, create, appender)

	err := post("questions", "hello")
	if err == nil {
		t.Fatal("post = nil, want a failed append to fail the post")
	}
	if err.Error() != msgChannelPostRecordFailed {
		t.Errorf("err = %q, want the static record-failed reason", err.Error())
	}
	if strings.Contains(err.Error(), "/home/op") || strings.Contains(err.Error(), "segment") {
		t.Errorf("refusal %q carries internal/history detail; it reaches the wire verbatim", err.Error())
	}
}

// TestChannelPoster_ChunksAnOversizedPost covers the split a post larger than
// maxDeltaTextBytes needs. It is not an optimisation: control.MaxChannelPostBytes
// is 64 KiB and the v2 application envelope caps at 65519 B, so a single-frame
// post was never deliverable at the size the verb already accepts.
//
// The chunks share ONE turn_id and carry consecutive seqs, which is what keeps
// AC#3 true at any size: a client coalescing on turn_id draws one message, not N.
// Rejoining them must reproduce the post byte-for-byte — splitDeltaText returns
// substrings, so that is a structural property rather than a hope.
func TestChannelPoster_ChunksAnOversizedPost(t *testing.T) {
	reg, _ := newChannelTestRegistry(t, t.TempDir())
	addConversation(t, reg, "questions", true, false)

	create := &stubCreate{}
	appender := &stubAppender{}
	announcer := &stubAnnouncer{}
	post := newTestPosterAnnouncing(t, reg, create, appender, announcer)

	text := strings.Repeat("s", maxDeltaTextBytes*2+7)
	if err := post("questions", text); err != nil {
		t.Fatalf("post: %v", err)
	}

	const wantChunks = 3
	if len(appender.calls) != wantChunks {
		t.Fatalf("append calls = %d, want %d", len(appender.calls), wantChunks)
	}
	if len(announcer.calls) != wantChunks {
		t.Fatalf("announce calls = %d, want %d — every recorded chunk is pushed", len(announcer.calls), wantChunks)
	}

	var rejoined strings.Builder
	for i, call := range appender.calls {
		if call.typ != protocol.TypeAssistantDelta {
			t.Errorf("chunk %d type = %q, want %q", i, call.typ, protocol.TypeAssistantDelta)
		}
		var delta protocol.AssistantDeltaPayload
		if err := json.Unmarshal(call.payload, &delta); err != nil {
			t.Fatalf("decode chunk %d: %v", i, err)
		}
		if delta.Seq != i {
			t.Errorf("chunk %d seq = %d, want %d", i, delta.Seq, i)
		}
		if delta.TurnID != announcer.calls[0].TurnID {
			t.Errorf("chunk %d turn_id = %q, want the post's single turn %q — chunks that do not "+
				"share a turn draw as separate messages", i, delta.TurnID, announcer.calls[0].TurnID)
		}
		if len(delta.Text) > maxDeltaTextBytes {
			t.Errorf("chunk %d carries %d bytes, over the %d-byte bound", i, len(delta.Text), maxDeltaTextBytes)
		}
		if announcer.calls[i] != delta {
			t.Errorf("chunk %d announced %+v but recorded %+v", i, announcer.calls[i], delta)
		}
		rejoined.WriteString(delta.Text)
	}
	if rejoined.String() != text {
		t.Errorf("rejoined %d bytes, want the posted %d — the chunks must reassemble exactly",
			rejoined.Len(), len(text))
	}
}

// TestChannelPoster_MaximumPostFitsEnvelopeCap is the deterministic per-frame cap
// test that ENFORCES the chunking above — the suspenders to maxDeltaTextBytes'
// belt, and different fabric from it. It posts a message of exactly
// control.MaxChannelPostBytes and measures the marshalled protocol.Envelope each
// chunk would be sealed into, not just its payload.
//
// The '<' fill is the row that binds the constant: encoding/json HTML-escapes
// '<', '>' and '&' into a six-byte \uXXXX form, so one input byte costs six on
// the wire. An ASCII fill would under-report by over 5x and prove nothing. The
// multi-byte row exists for the other property — a cut through a rune is
// silently repaired into U+FFFD, which only byte equality on the rejoined text
// can see.
//
// If this ever fails, LOWER maxDeltaTextBytes — never raise it.
func TestChannelPoster_MaximumPostFitsEnvelopeCap(t *testing.T) {
	tests := []struct {
		name string
		fill string
	}{
		{name: "html", fill: "<"},
		{name: "ascii", fill: "a"},
		{name: "cjk", fill: "世"},
		{name: "emoji", fill: "🙂"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg, _ := newChannelTestRegistry(t, t.TempDir())
			addConversation(t, reg, "questions", true, false)

			create := &stubCreate{}
			appender := &stubAppender{}
			announcer := &stubAnnouncer{}
			post := newTestPosterAnnouncing(t, reg, create, appender, announcer)

			// The largest message handleChannelPost admits, truncated to a whole
			// number of fill runes so the input itself is valid UTF-8.
			text := strings.Repeat(tc.fill, control.MaxChannelPostBytes/len(tc.fill))
			if err := post("questions", text); err != nil {
				t.Fatalf("post: %v", err)
			}
			if len(announcer.calls) == 0 {
				t.Fatal("a maximum-size post announced nothing")
			}

			var rejoined strings.Builder
			for i, p := range announcer.calls {
				payload, err := json.Marshal(p)
				if err != nil {
					t.Fatalf("marshal chunk %d: %v", i, err)
				}
				env, err := json.Marshal(protocol.Envelope{
					ID:      uint64(i + 1),
					Type:    protocol.TypeAssistantDelta,
					TS:      time.Now().UTC(),
					Payload: payload,
				})
				if err != nil {
					t.Fatalf("marshal envelope %d: %v", i, err)
				}
				if len(env) > maxV2AppEnvelope {
					t.Errorf("chunk %d seals into %d bytes, over the %d-byte v2 application-envelope cap; "+
						"lower maxDeltaTextBytes", i, len(env), maxV2AppEnvelope)
				}
				rejoined.WriteString(p.Text)
			}
			if rejoined.String() != text {
				t.Errorf("rejoined %d bytes, want the posted %d", rejoined.Len(), len(text))
			}
		})
	}
}

// TestParseChannelPostArgs covers AC#1's flag rules — every case here is a usage
// failure (exit 2) or a clean parse, and none of them touches the filesystem.
func TestParseChannelPostArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantName string
		wantText string
		wantFile string
		wantErr  bool
	}{
		{name: "text", args: []string{"--name", "questions", "--text", "hello"}, wantName: "questions", wantText: "hello"},
		{name: "file", args: []string{"--name", "questions", "--file", "/tmp/q.txt"}, wantName: "questions", wantFile: "/tmp/q.txt"},
		{name: "empty text is still a choice", args: []string{"--name", "q", "--text", ""}, wantName: "q", wantText: ""},
		{name: "neither flag", args: []string{"--name", "questions"}, wantErr: true},
		{name: "both flags", args: []string{"--name", "q", "--text", "hi", "--file", "/tmp/q.txt"}, wantErr: true},
		{name: "missing name", args: []string{"--text", "hello"}, wantErr: true},
		{name: "stray positional", args: []string{"--name", "q", "--text", "hi", "extra"}, wantErr: true},
		{name: "unknown flag", args: []string{"--name", "q", "--text", "hi", "--nope"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, text, file, err := parseChannelPostArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseChannelPostArgs(%q) = (%q,%q,%q, nil), want an error", tt.args, name, text, file)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseChannelPostArgs(%q): %v", tt.args, err)
			}
			if name != tt.wantName || text != tt.wantText || file != tt.wantFile {
				t.Errorf("= (%q,%q,%q), want (%q,%q,%q)", name, text, file, tt.wantName, tt.wantText, tt.wantFile)
			}
		})
	}
}

// TestChannelPostContent covers the exit-1 class: reading --file and bounding
// both sources at the cap. The at-the-cap case pins the boundary itself, not
// only the byte past it.
func TestChannelPostContent(t *testing.T) {
	dir := t.TempDir()

	write := func(t *testing.T, name string, n int) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(strings.Repeat("x", n)), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return path
	}

	t.Run("text passes through", func(t *testing.T) {
		got, err := channelPostContent("hello", "")
		if err != nil || got != "hello" {
			t.Fatalf("= (%q, %v), want (hello, nil)", got, err)
		}
	})

	t.Run("file is read whole", func(t *testing.T) {
		path := filepath.Join(dir, "q.txt")
		if err := os.WriteFile(path, []byte("line one\nline two\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		got, err := channelPostContent("", path)
		if err != nil {
			t.Fatalf("channelPostContent: %v", err)
		}
		if got != "line one\nline two\n" {
			t.Errorf("= %q, want the file's bytes unchanged", got)
		}
	})

	t.Run("missing file refuses", func(t *testing.T) {
		if _, err := channelPostContent("", filepath.Join(dir, "absent.txt")); err == nil {
			t.Fatal("= nil, want a refusal for an unreadable --file")
		}
	})

	t.Run("file at exactly the cap is accepted", func(t *testing.T) {
		got, err := channelPostContent("", write(t, "at-cap.txt", control.MaxChannelPostBytes))
		if err != nil {
			t.Fatalf("channelPostContent at the cap: %v", err)
		}
		if len(got) != control.MaxChannelPostBytes {
			t.Errorf("read %d bytes, want %d", len(got), control.MaxChannelPostBytes)
		}
	})

	t.Run("file one byte over the cap refuses", func(t *testing.T) {
		if _, err := channelPostContent("", write(t, "over-cap.txt", control.MaxChannelPostBytes+1)); err == nil {
			t.Fatal("= nil, want a refusal one byte past the cap")
		}
	})

	t.Run("text over the cap refuses", func(t *testing.T) {
		if _, err := channelPostContent(strings.Repeat("x", control.MaxChannelPostBytes+1), ""); err == nil {
			t.Fatal("= nil, want a refusal for over-cap --text")
		}
	})
}

// TestChannelVerdict pins the exit-code split AC#1 names — 1 for everything that
// is not a usage failure — and that the sub-verb reaches the prefix, which is
// the whole reason channelNewVerdict was generalised rather than twinned.
func TestChannelVerdict(t *testing.T) {
	tests := []struct {
		name     string
		sub      string
		err      error
		wantCode int
		wantLine string
	}{
		{name: "success", sub: "post", err: nil, wantCode: 0, wantLine: ""},
		{name: "post failure", sub: "post", err: errors.New("boom"), wantCode: 1, wantLine: "pyry channel post: boom"},
		{name: "new failure", sub: "new", err: errors.New("boom"), wantCode: 1, wantLine: "pyry channel new: boom"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, line := channelVerdict(tt.sub, tt.err)
			if code != tt.wantCode || line != tt.wantLine {
				t.Errorf("channelVerdict(%q, %v) = (%d, %q), want (%d, %q)",
					tt.sub, tt.err, code, line, tt.wantCode, tt.wantLine)
			}
		})
	}
}
