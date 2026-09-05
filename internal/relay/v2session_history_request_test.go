package relay

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// --- #2116 inbound conversation-history fixtures ---

// Sentinel values chosen so a substring scan of the log buffer or of a sealed
// reject frame cannot match them incidentally. The split is the one this verb's
// disclosure rules draw: the conversation id is loggable ONCE membership has
// established its shape, while the cursor and every payload byte are loggable
// and echoable NOWHERE — § Conversation history's "loggable only after their
// shape is validated", and nothing here ever validates a cursor's.
const (
	hrTestConvID     = "7d1a4e62-8b05-4c39-9f27-2116cafe0001" // canonical UUIDv4
	hrTestOtherConv  = "7d1a4e62-8b05-4c39-9f27-2116cafe0002" // canonical, NOT hosted
	hrTestCursorMark = "ZZ2116CURSORZZ"                       // NEVER logged, never echoed
	hrTestPayloadMar = "ZZ2116PAYLOADZZ"                      // NEVER logged, never echoed
	hrTestReqEnvID   = uint64(21160)
	hrTestOtherEnvID = uint64(21161)
)

// pageCall captures one HistoryPage call. All three arguments are retained: the
// claims this file makes are about which conversation reaches the seam, that the
// cursor reaches it BYTE FOR BYTE UNPARSED, and what limit the handler chose.
type pageCall struct {
	conversationID string
	cursor         string
	limit          int
}

// fakeHistoryLog is a relay-side test double for the HistoryPage seam backed by
// an in-test log, so a WALK is drivable rather than a single scripted answer.
//
// It reimplements history.Store.Page's CONTRACT, not its storage: entries are
// newest-first, a cursor names the index of the next older entry, AtStart is set
// when the fill reached index len(entries) — so a page that fills EXACTLY at the
// last entry reports AtStart false with a usable cursor and the call after it is
// the empty terminal one. Getting that boundary wrong in the double would make
// every walk assertion here vacuous, which is why it is spelled out.
type fakeHistoryLog struct {
	mu sync.Mutex

	// entries is the whole log, newest-first.
	entries []protocol.HistoryEntry
	// outcome, when not HistoryPageOK, is answered instead of a page.
	outcome HistoryPageOutcome
	calls   []pageCall
}

func (f *fakeHistoryLog) page(conversationID, cursor string, limit int) HistoryPageResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, pageCall{conversationID: conversationID, cursor: cursor, limit: limit})
	if f.outcome != HistoryPageOK {
		return HistoryPageResult{Outcome: f.outcome}
	}
	start := 0
	if cursor != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(cursor, "idx:"))
		if err != nil {
			return HistoryPageResult{Outcome: HistoryPageBadCursor}
		}
		start = n
	}
	if start >= len(f.entries) {
		return HistoryPageResult{AtStart: true}
	}
	end := start + limit
	if end > len(f.entries) {
		end = len(f.entries)
	}
	// The slice is sized from what the log holds, never from limit — the rule the
	// production adapter owes protocol.RequestHistoryPayload.Limit, kept here so
	// the double cannot model the bug it is meant to catch.
	out := make([]protocol.HistoryEntry, 0, end-start)
	out = append(out, f.entries[start:end]...)
	// A usable cursor even when the fill ended EXACTLY at the last entry, and
	// AtStart deliberately not set there — the next call starts past the end and
	// is the empty terminal page. Reporting AtStart on the last non-empty page is
	// the producer bug the walk test exists to catch, so the double must not model
	// it.
	return HistoryPageResult{Entries: out, Cursor: fmt.Sprintf("idx:%d", end)}
}

func (f *fakeHistoryLog) callSnapshot() []pageCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pageCall(nil), f.calls...)
}

// hrEntries builds n log entries, newest-first, each carrying payloadBytes of
// filler. The type alternates so an assertion about a type carrying no turn_id
// has a real subject.
func hrEntries(n, payloadBytes int) []protocol.HistoryEntry {
	out := make([]protocol.HistoryEntry, 0, n)
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		typ := protocol.TypeAssistantDelta
		if i%3 == 2 {
			typ = protocol.TypeSessionTransition
		}
		payload, err := json.Marshal(map[string]string{
			"marker": hrTestPayloadMar,
			"filler": strings.Repeat("x", payloadBytes),
		})
		if err != nil {
			panic(err) // a map of two strings; cannot fail
		}
		out = append(out, protocol.HistoryEntry{
			// Newest-first: the highest id comes first, matching the log's order.
			ID:      uint64(n - i),
			Type:    typ,
			Payload: payload,
			TS:      base.Add(-time.Duration(i) * time.Second),
		})
	}
	return out
}

// startHistoryConn stands up a manager with the history seams wired (either may
// be nil for the unwired postures), drives one paired handshake and returns the
// open session plus its log buffer. Every test here drives a real handshake and a
// real AEAD-sealed frame, so the interception is proven through dispatchAppFrame
// and the conn's appFrameWorker rather than by calling the handler directly.
func startHistoryConn(t *testing.T, knownConv func(string) bool, pager HistoryPager) (*openSession, *syncLogBuffer) {
	t.Helper()
	respPriv, respPub := genV2Keypair(t)
	initPriv, _ := genV2Keypair(t)
	reg := v2PairedRegistry(t, v2TestToken)
	logger, logBuf := bufferLogger()
	frames := make(chan protocol.RoutingEnvelope, 8)
	rec := &v2Recorder{}
	sess := driveToOpen(t, V2SessionConfig{
		Frames:            frames,
		Outbound:          rec.outbound,
		StaticPriv:        respPriv,
		Devices:           reg,
		ServerID:          v2TestServerID,
		Logger:            logger,
		KnownConversation: knownConv,
		HistoryPage:       pager,
	}, frames, rec, respPub, initPriv)
	t.Cleanup(sess.stop)
	return sess, logBuf
}

// sendRequestHistory seals one request_history envelope under the initiator's
// send state and hands it to the manager. The envelope ID IS load-bearing: the
// page and both rejects correlate on it through in_reply_to, and the payload
// carries no request-id key.
func sendRequestHistory(t *testing.T, sess *openSession, envID uint64, payload string) {
	t.Helper()
	sess.frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
		ID:      envID,
		Type:    protocol.TypeRequestHistory,
		TS:      time.Now().UTC(),
		Payload: json.RawMessage(payload),
	})
}

// historyPayload builds a well-formed request_history body.
func historyPayload(conversationID, cursor string, limit int) string {
	p, err := json.Marshal(protocol.RequestHistoryPayload{
		ConversationID: conversationID,
		Cursor:         cursor,
		Limit:          limit,
	})
	if err != nil {
		panic(err) // two strings and an int; cannot fail
	}
	return string(p)
}

// nextHistoryReply waits until the conn has sealed seen+1 frames and decrypts
// EXACTLY the one at index seen.
//
// A walk cannot use waitForReplies, which decrypts every frame on the conn from
// the start: the receive CipherState's nonce is in lockstep with the sender's, so
// re-decrypting a frame already consumed desynchronises the stream and every
// later frame fails authentication. One new frame per call is what keeps a
// multi-page walk drivable at all.
func nextHistoryReply(t *testing.T, sess *openSession, seen int) protocol.Envelope {
	t.Helper()
	waitForConnNoiseMsg(t, sess.rec, v2TestConnID, seen+1)
	msgs := noiseMsgsForConn(t, sess.rec, v2TestConnID)
	if len(msgs) <= seen {
		t.Fatalf("sealed frame count = %d, want more than %d", len(msgs), seen)
	}
	return decryptAppFrame(t, msgs[seen], sess.initRecv)
}

// decodeHistoryPage pins the reply's shape before reading it: a page answer is a
// history_page correlated to the request, never a bare error frame.
func decodeHistoryPage(t *testing.T, env protocol.Envelope, wantInReplyTo uint64) protocol.HistoryPagePayload {
	t.Helper()
	if env.Type != protocol.TypeHistoryPage {
		t.Fatalf("reply type = %q, want %q", env.Type, protocol.TypeHistoryPage)
	}
	if env.InReplyTo == nil || *env.InReplyTo != wantInReplyTo {
		t.Fatalf("page in_reply_to = %v, want pointer to %d", env.InReplyTo, wantInReplyTo)
	}
	var p protocol.HistoryPagePayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("decode history_page payload: %v", err)
	}
	return p
}

// TestV2Session_RequestHistory_ServesTheNewestPage is the first half of AC #1: a
// request naming a conversation with recorded traffic is answered with a page of
// its newest entries, WITHOUT the client having sent a message first — which is
// the whole point of the verb and is why this drives nothing but the request.
func TestV2Session_RequestHistory_ServesTheNewestPage(t *testing.T) {
	t.Parallel()

	log := &fakeHistoryLog{entries: hrEntries(4, 8)}
	sess, _ := startHistoryConn(t, knownOnly(hrTestConvID), log.page)

	sendRequestHistory(t, sess, hrTestReqEnvID, historyPayload(hrTestConvID, "", 2))

	page := decodeHistoryPage(t, waitForReplies(t, sess, 1)[0], hrTestReqEnvID)
	if len(page.Entries) != 2 {
		t.Fatalf("page entries = %d, want 2", len(page.Entries))
	}
	if page.Entries[0].ID != 4 || page.Entries[1].ID != 3 {
		t.Errorf("page ids = %d,%d, want 4,3 — newest-first", page.Entries[0].ID, page.Entries[1].ID)
	}
	if page.AtStart {
		t.Error("at_start = true on a page with three older entries behind it")
	}
	if page.Cursor == "" {
		t.Error("cursor is empty on a page that is not at_start; the walk cannot continue")
	}

	calls := log.callSnapshot()
	if len(calls) != 1 {
		t.Fatalf("seam calls = %d, want exactly 1", len(calls))
	}
	if calls[0].conversationID != hrTestConvID || calls[0].cursor != "" || calls[0].limit != 2 {
		t.Errorf("seam called with (%q, %q, %d), want (%q, %q, %d) — the frame's own values, unaltered",
			calls[0].conversationID, calls[0].cursor, calls[0].limit, hrTestConvID, "", 2)
	}
}

// TestV2Session_RequestHistory_WalksToTheStart is the rest of AC #1, and the
// BOUNDARY is what it exists for: a log of exactly six entries walked two at a
// time fills EXACTLY at the first entry on the third page, which must report
// at_start FALSE with a usable cursor, and only the fourth call is the empty
// terminal one. A producer that reports at_start on the last non-empty page
// passes a naive walk test and fails this one.
func TestV2Session_RequestHistory_WalksToTheStart(t *testing.T) {
	t.Parallel()

	const total = 6
	log := &fakeHistoryLog{entries: hrEntries(total, 8)}
	sess, _ := startHistoryConn(t, knownOnly(hrTestConvID), log.page)

	seen := make([]uint64, 0, total)
	cursor := ""
	pages := 0
	for {
		envID := hrTestReqEnvID + uint64(pages)
		sendRequestHistory(t, sess, envID, historyPayload(hrTestConvID, cursor, 2))
		page := decodeHistoryPage(t, nextHistoryReply(t, sess, pages), envID)
		pages++
		for _, e := range page.Entries {
			seen = append(seen, e.ID)
		}
		if page.AtStart {
			if page.Cursor != "" {
				t.Errorf("terminal page cursor = %q, want empty whenever at_start is set", page.Cursor)
			}
			if len(page.Entries) != 0 {
				t.Errorf("terminal page carried %d entries; the fill ended exactly at the first entry on the page before it", len(page.Entries))
			}
			break
		}
		if page.Cursor == "" {
			t.Fatalf("page %d is not at_start and carries no cursor; the walk cannot continue", pages)
		}
		cursor = page.Cursor
		if pages > total+2 {
			t.Fatal("walk did not terminate")
		}
	}

	if pages != 4 {
		t.Errorf("walk took %d pages, want 4 — three full pages then the empty terminal one", pages)
	}
	if len(seen) != total {
		t.Fatalf("walked %d entries, want %d — none skipped, none repeated", len(seen), total)
	}
	for i, id := range seen {
		if want := uint64(total - i); id != want {
			t.Errorf("walked entry %d = id %d, want %d — newest-first, exactly once", i, id, want)
		}
	}
}

// TestV2Session_RequestHistory_EmptyLogIsTheTerminalPage is AC #1's last clause:
// a conversation with no log — one predating the log, or with no traffic — is
// answered with the empty terminal page rather than an error. The distinction
// matters because "no entries" is the one shape a producer is tempted to treat as
// a failure.
func TestV2Session_RequestHistory_EmptyLogIsTheTerminalPage(t *testing.T) {
	t.Parallel()

	log := &fakeHistoryLog{}
	sess, _ := startHistoryConn(t, knownOnly(hrTestConvID), log.page)

	sendRequestHistory(t, sess, hrTestReqEnvID, historyPayload(hrTestConvID, "", 0))

	env := waitForReplies(t, sess, 1)[0]
	page := decodeHistoryPage(t, env, hrTestReqEnvID)
	if !page.AtStart || page.Cursor != "" || len(page.Entries) != 0 {
		t.Errorf("empty-log page = {entries:%d cursor:%q at_start:%v}, want {0 \"\" true}",
			len(page.Entries), page.Cursor, page.AtStart)
	}
	// The wire form is the one HistoryPagePayload.MarshalJSON normalises: [], not
	// null. A client whose array type is non-optional fails to decode null.
	if !strings.Contains(string(env.Payload), `"entries":[]`) {
		t.Errorf("empty page serialised as %s, want an explicit \"entries\":[]", env.Payload)
	}
}

// TestV2Session_RequestHistory_ExtentIsTheDaemonsDecision is AC #2: the reply's
// extent is never the client's ask. Each row asserts what the SEAM saw, which is
// the only place the substitution is observable — the reply looks the same either
// way.
func TestV2Session_RequestHistory_ExtentIsTheDaemonsDecision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		payload   string
		wantLimit int
	}{
		{
			// A literal 0 draws ErrInvalidPageSize from history.Store.Page, so the
			// substitution has to happen ABOVE the log or every client sending 0
			// gets an error.
			name:      "explicit zero asks the daemon to choose",
			payload:   historyPayload(hrTestConvID, "", 0),
			wantLimit: defaultHistoryPageEntries,
		},
		{
			// Same value, reached the other way: every key is optional to
			// encoding/json, so an omitted limit decodes to 0 and must mean the
			// same thing as a sent 0.
			name:      "omitted limit asks the daemon to choose",
			payload:   `{"conversation_id":"` + hrTestConvID + `","cursor":""}`,
			wantLimit: defaultHistoryPageEntries,
		},
		{
			name:      "a positive ask under the ceiling is honoured",
			payload:   historyPayload(hrTestConvID, "", 7),
			wantLimit: 7,
		},
		{
			// Clamped, never refused — and clamped to the relay's byte-derived
			// ceiling, which sits under history.MaxPageEntries because no page
			// above roughly 1365 entries can serialise inside the envelope cap.
			name:      "an ask above the ceiling is clamped, not refused",
			payload:   historyPayload(hrTestConvID, "", 1<<30),
			wantLimit: maxHistoryPageEntries,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			log := &fakeHistoryLog{entries: hrEntries(3, 8)}
			sess, _ := startHistoryConn(t, knownOnly(hrTestConvID), log.page)

			sendRequestHistory(t, sess, hrTestReqEnvID, tt.payload)
			decodeHistoryPage(t, waitForReplies(t, sess, 1)[0], hrTestReqEnvID)

			calls := log.callSnapshot()
			if len(calls) != 1 {
				t.Fatalf("seam calls = %d, want exactly 1", len(calls))
			}
			if calls[0].limit != tt.wantLimit {
				t.Errorf("seam saw limit %d, want %d", calls[0].limit, tt.wantLimit)
			}
		})
	}
}

// TestV2Session_RequestHistory_EveryRejectAnswersItsCode is AC #3. Every row
// asserts the WHOLE refusal — code, static message and retryable — because
// retryable is the field a client branches on and a code paired with the wrong
// flag turns a permanent fault into a hot loop. Exactly one frame per row, so no
// row can pass while also serving a page.
func TestV2Session_RequestHistory_EveryRejectAnswersItsCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		known         func(string) bool
		outcome       HistoryPageOutcome
		payload       string
		wantCode      string
		wantMsg       string
		wantRetryable bool
		wantSeamCalls int
	}{
		{
			name:          "a conversation the daemon does not host",
			known:         knownOnly(hrTestConvID),
			payload:       historyPayload(hrTestOtherConv, "", 10),
			wantCode:      protocol.CodeConversationNotFound,
			wantMsg:       msgHistoryConversationNotFound,
			wantRetryable: false,
		},
		{
			// A non-canonical id needs no shape check of its own: the registry
			// holds canonical ids only, so membership refuses it.
			name:          "a conversation id that is not of canonical shape",
			known:         knownOnly(hrTestConvID),
			payload:       historyPayload("../../etc/passwd", "", 10),
			wantCode:      protocol.CodeConversationNotFound,
			wantMsg:       msgHistoryConversationNotFound,
			wantRetryable: false,
		},
		{
			// A nil seam refuses everything — the only fail-safe reading, and the
			// one that keeps an unwired membership gate from opening the verb.
			name:          "a nil membership gate refuses everything",
			known:         nil,
			payload:       historyPayload(hrTestConvID, "", 10),
			wantCode:      protocol.CodeConversationNotFound,
			wantMsg:       msgHistoryConversationNotFound,
			wantRetryable: false,
		},
		{
			name:          "a negative limit",
			known:         knownOnly(hrTestConvID),
			payload:       historyPayload(hrTestConvID, "", -1),
			wantCode:      protocol.CodeHistoryInvalidPageSize,
			wantMsg:       msgHistoryInvalidPageSize,
			wantRetryable: false,
		},
		{
			name:          "a cursor the log refuses",
			known:         knownOnly(hrTestConvID),
			outcome:       HistoryPageBadCursor,
			payload:       historyPayload(hrTestConvID, hrTestCursorMark, 10),
			wantCode:      protocol.CodeHistoryInvalidCursor,
			wantMsg:       msgHistoryInvalidCursor,
			wantRetryable: false,
			wantSeamCalls: 1,
		},
		{
			name:          "a log that cannot be read",
			known:         knownOnly(hrTestConvID),
			outcome:       HistoryPageUnavailable,
			payload:       historyPayload(hrTestConvID, "", 10),
			wantCode:      protocol.CodeHistoryUnavailable,
			wantMsg:       msgHistoryUnavailable,
			wantRetryable: true,
			wantSeamCalls: 1,
		},
		{
			// Rejected, not tolerated as a zero value: an empty conversation id
			// names nothing, and joined into a path it resolves to the log ROOT
			// rather than erroring.
			name:          "a payload that does not decode",
			known:         knownOnly(hrTestConvID),
			payload:       `{"conversation_id":` + strconv.Quote(hrTestConvID) + `,"limit":"` + hrTestPayloadMar + `"}`,
			wantCode:      protocol.CodeHistoryInvalidRequest,
			wantMsg:       msgHistoryInvalidRequest,
			wantRetryable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			log := &fakeHistoryLog{entries: hrEntries(3, 8), outcome: tt.outcome}
			sess, logBuf := startHistoryConn(t, tt.known, log.page)

			sendRequestHistory(t, sess, hrTestReqEnvID, tt.payload)
			env := waitForReplies(t, sess, 1)[0]
			assertRejectFrame(t, env, tt.wantCode, tt.wantMsg, tt.wantRetryable, hrTestReqEnvID)

			if got := len(log.callSnapshot()); got != tt.wantSeamCalls {
				t.Errorf("seam calls = %d, want %d — a refusal before the gate must not reach the log", got, tt.wantSeamCalls)
			}

			// No refusal echoes the cursor, the conversation id, or any payload
			// byte, on the wire or into the log. Both markers are checked against
			// both surfaces, because a reject that is clean on the wire and
			// chatty in the log is the log-injection shape § Conversation history
			// forbids.
			frame := string(env.Payload)
			logged := logBuf.String()
			for _, banned := range []string{hrTestCursorMark, hrTestPayloadMar} {
				if strings.Contains(frame, banned) {
					t.Errorf("reject frame %s echoes %q", frame, banned)
				}
				if strings.Contains(logged, banned) {
					t.Errorf("reject log echoes %q", banned)
				}
			}
			// The conversation id is loggable only past the membership gate, and
			// on the wire never: a refusal that names it back is an echo.
			if strings.Contains(frame, hrTestConvID) || strings.Contains(frame, hrTestOtherConv) {
				t.Errorf("reject frame %s echoes a conversation id", frame)
			}
		})
	}
}

// TestV2Session_RequestHistory_UnwiredLogIsInert is AC #3's last clause: a daemon
// with no history seam wired consumes the frame WITHOUT PARSING IT, mirroring the
// nil AttachmentResolve guard. The two halves are separate claims — consumed, so
// dispatch.Route's unknown-type reply is not drawn, and unparsed, so an unwired
// daemon performs zero parsing of remote-authored bytes — and only the second one
// needs the deliberately undecodable payload below.
func TestV2Session_RequestHistory_UnwiredLogIsInert(t *testing.T) {
	t.Parallel()

	sess, logBuf := startHistoryConn(t, knownOnly(hrTestConvID), nil)

	sendRequestHistory(t, sess, hrTestReqEnvID, `{"conversation_id":"`+hrTestConvID+`","cursor":"`+hrTestCursorMark+`","limit":`+strconv.Quote(hrTestPayloadMar)+`}`)
	expectNoReply(t, sess)

	logged := logBuf.String()
	for _, banned := range []string{hrTestCursorMark, hrTestPayloadMar} {
		if strings.Contains(logged, banned) {
			t.Errorf("inert log echoes %q; the frame must be consumed without parsing", banned)
		}
	}
}

// TestV2Session_RequestHistory_ShortensAPageToFitTheEnvelopeCap is AC #2's byte
// clause AND the strongest guard this file carries against the tempting wrong
// implementation.
//
// SHORTENING BY TRUNCATING THE RETURNED SLICE IS A ONE-LINER AND IS WRONG: the
// page's cursor names the position just before the OLDEST entry the log returned,
// so dropping entries from the tail while keeping that cursor makes the client's
// next ask skip exactly the dropped ones. That gap is invisible to any test that
// checks one page, which is why this walks the whole log and asserts every entry
// exactly once — the same assertion the walk test makes, over entries big enough
// that the handler MUST shorten.
func TestV2Session_RequestHistory_ShortensAPageToFitTheEnvelopeCap(t *testing.T) {
	t.Parallel()

	// Six entries of ~20 KB: three overflow the 65519-byte cap, so a default-size
	// ask cannot be answered as asked and the handler has to narrow.
	const total = 6
	log := &fakeHistoryLog{entries: hrEntries(total, 20_000)}
	sess, _ := startHistoryConn(t, knownOnly(hrTestConvID), log.page)

	seen := make([]uint64, 0, total)
	cursor := ""
	pages := 0
	shortened := false
	for {
		envID := hrTestReqEnvID + uint64(pages)
		// Asks for the whole log every time; every page must come back shortened.
		sendRequestHistory(t, sess, envID, historyPayload(hrTestConvID, cursor, total))
		env := nextHistoryReply(t, sess, pages)
		if got := len(mustMarshal(t, env)); got > maxAppEnvelopeBytes {
			t.Fatalf("page envelope = %d bytes, over the %d-byte application-envelope cap", got, maxAppEnvelopeBytes)
		}
		page := decodeHistoryPage(t, env, envID)
		pages++
		if len(page.Entries) > 0 && len(page.Entries) < total {
			shortened = true
			// A page shortened for bytes must NOT claim the start of the log.
			if page.AtStart {
				t.Errorf("page %d was shortened to fit the cap and set at_start; at_start comes from the log, never from the shortening", pages)
			}
		}
		seen = append(seen, entryIDs(page.Entries)...)
		if page.AtStart {
			break
		}
		cursor = page.Cursor
		if pages > total+2 {
			t.Fatal("walk did not terminate")
		}
	}

	if !shortened {
		t.Fatal("no page came back shortened; the fixture no longer overflows the cap and this test proves nothing")
	}
	if len(seen) != total {
		t.Fatalf("walked %d entries, want %d — a page shortened by TRUNCATING rather than re-asking skips the dropped ones", len(seen), total)
	}
	for i, id := range seen {
		if want := uint64(total - i); id != want {
			t.Errorf("walked entry %d = id %d, want %d — none skipped, none repeated", i, id, want)
		}
	}

	// Every shortening is a fresh ask at a strictly smaller limit, never a slice
	// of an answer already in hand.
	for _, c := range log.callSnapshot() {
		if c.limit < 1 || c.limit > total {
			t.Errorf("seam saw limit %d, want it inside [1,%d]", c.limit, total)
		}
	}
}

// entryIDs is the projection the walk assertions compare on.
func entryIDs(entries []protocol.HistoryEntry) []uint64 {
	out := make([]uint64, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.ID)
	}
	return out
}

// TestV2Session_RequestHistory_OneOversizedEntryIsStillEmitted pins the floor of
// the shortening loop. An entry can be stored that no page can carry — nothing
// bounds a stored payload below history.MaxSegmentBytes — and the choice there is
// emit-anyway over refuse.
//
// REFUSING WOULD STALL THE WALK PERMANENTLY with the client learning nothing: it
// never receives the cursor, so it can never step past the entry. Emitting yields
// the transport's own message.too_long, which is a distinguishable answer, and the
// frame may well be deliverable since the budget is measured against real
// marshalled bytes rather than an estimate.
func TestV2Session_RequestHistory_OneOversizedEntryIsStillEmitted(t *testing.T) {
	t.Parallel()

	log := &fakeHistoryLog{entries: hrEntries(1, maxAppEnvelopeBytes+4096)}
	sess, logBuf := startHistoryConn(t, knownOnly(hrTestConvID), log.page)

	sendRequestHistory(t, sess, hrTestReqEnvID, historyPayload(hrTestConvID, "", 10))

	page := decodeHistoryPage(t, waitForReplies(t, sess, 1)[0], hrTestReqEnvID)
	if len(page.Entries) != 1 {
		t.Fatalf("page entries = %d, want the single oversized entry emitted rather than refused", len(page.Entries))
	}
	if !strings.Contains(logBuf.String(), "v2.history.oversized") {
		t.Error("no operator-visible record of an emitted page over the envelope cap")
	}
}

// TestV2Session_RequestHistory_DedupKeyHoldsWithoutTurnID pins the page/live
// boundary key named in the spec: (type, ts).
//
// THE SUBJECT IS DELIBERATELY A TYPE CARRYING NO turn_id. turn_id + seq is the
// obvious candidate and covers only turn-scoped payloads — session_transition,
// turn_state, stall, api_retry and compacting carry none — and session_transition
// is the sharpest case of all, because its producer skips the replay ring, so its
// live envelope carries no event_id either and the log is the only place it is
// retained. What both lanes DO carry for every type is the type string and the
// timestamp, and #2114's producers hoist ONE ts per logical event shared by the
// log entry and every per-conn envelope.
func TestV2Session_RequestHistory_DedupKeyHoldsWithoutTurnID(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 9, 5, 12, 0, 0, 123456789, time.UTC)
	stored := protocol.HistoryEntry{
		ID:      41,
		Type:    protocol.TypeSessionTransition,
		Payload: json.RawMessage(`{"reason":"clear"}`),
		TS:      ts,
	}
	log := &fakeHistoryLog{entries: []protocol.HistoryEntry{stored}}
	sess, _ := startHistoryConn(t, knownOnly(hrTestConvID), log.page)

	sendRequestHistory(t, sess, hrTestReqEnvID, historyPayload(hrTestConvID, "", 10))
	page := decodeHistoryPage(t, waitForReplies(t, sess, 1)[0], hrTestReqEnvID)
	if len(page.Entries) != 1 {
		t.Fatalf("page entries = %d, want 1", len(page.Entries))
	}
	got := page.Entries[0]

	// The live twin the daemon fans out for the same logical event, carrying the
	// SAME hoisted timestamp. Marshalled, because the claim is about the wire
	// form: time.Time's MarshalJSON is what both lanes render through, so equal
	// values must render equal strings.
	live := protocol.Envelope{ID: 9, Type: protocol.TypeSessionTransition, TS: ts, Payload: stored.Payload}
	liveJSON, err := json.Marshal(live)
	if err != nil {
		t.Fatalf("marshal the live twin: %v", err)
	}
	var liveWire struct {
		Type string `json:"type"`
		TS   string `json:"ts"`
	}
	if err := json.Unmarshal(liveJSON, &liveWire); err != nil {
		t.Fatalf("decode the live twin: %v", err)
	}
	var pageWire struct {
		Entries []struct {
			Type string `json:"type"`
			TS   string `json:"ts"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(mustMarshal(t, page), &pageWire); err != nil {
		t.Fatalf("decode the page's wire form: %v", err)
	}
	if len(pageWire.Entries) != 1 {
		t.Fatalf("page wire entries = %d, want 1", len(pageWire.Entries))
	}
	if pageWire.Entries[0].Type != liveWire.Type {
		t.Errorf("dedup key half `type`: page %q, live %q", pageWire.Entries[0].Type, liveWire.Type)
	}
	if pageWire.Entries[0].TS != liveWire.TS {
		t.Errorf("dedup key half `ts`: page %q, live %q — the two lanes must render one hoisted timestamp identically", pageWire.Entries[0].TS, liveWire.TS)
	}
	// The payload bytes are stored verbatim, so a client wanting a decisive match
	// on a timestamp collision has one.
	if string(got.Payload) != string(stored.Payload) {
		t.Errorf("page payload = %s, want the stored bytes verbatim %s", got.Payload, stored.Payload)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestV2Session_RequestHistory_DoesNotStallTheConn is AC #5, and it is the whole
// reason this handler runs off the Run goroutine.
//
// The mechanism under test is #965's: dispatchAppFrame TAGS a request_history and
// returns, so Run keeps servicing every other arm while the answer is being
// assembled. The second frame is therefore a RUN-INLINE arm — an interrupt on a
// non-interactive conn, which logs and needs no wiring — and its record appearing
// while the seam is still parked is the proof. A frame that also routed through
// the worker would prove nothing: the worker is deliberately FIFO per conn, so
// waiting behind the first frame there is the design rather than a stall.
func TestV2Session_RequestHistory_DoesNotStallTheConn(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	pager := func(conversationID, cursor string, limit int) HistoryPageResult {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return HistoryPageResult{AtStart: true}
	}

	sess, logBuf := startHistoryConn(t, knownOnly(hrTestConvID), pager)

	sendRequestHistory(t, sess, hrTestReqEnvID, historyPayload(hrTestConvID, "", 10))
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("the seam was never reached")
	}

	sess.frames <- sealAppFrame(t, sess.initSend, protocol.Envelope{
		ID:   hrTestOtherEnvID,
		Type: protocol.TypeInterrupt,
		TS:   time.Now().UTC(),
	})
	waitForLogContains(t, logBuf, "v2.interrupt.non_interactive")

	// Only now is the answer allowed to complete, so the record above provably
	// predates it.
	close(release)
	decodeHistoryPage(t, waitForReplies(t, sess, 1)[0], hrTestReqEnvID)
}
