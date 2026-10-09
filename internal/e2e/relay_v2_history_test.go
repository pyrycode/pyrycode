//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelayV2_LiveHistoryEntryID joins a producer's direct push to its served
// history entry over a real Noise session. Seed only prior traffic, then drive
// the operator producer through send_message instead of seeding the event tested.
func TestRelayV2_LiveHistoryEntryID(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111"
		convID      = "77777777-7777-4777-8777-777777777777"
		messageID   = "live-history-2861"
		text        = "match this live operator turn to durable history"
	)
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	pair, err := paireddevice.Setup(paireddevice.Config{
		Home: home, InstanceName: "test", Relay: relayURL, DeviceName: "phone-a",
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(pair.ServerStaticPubkey)
	if err != nil {
		t.Fatal(err)
	}
	seedBoundConversation(t, home, convID, initialUUID)
	store := history.New(filepath.Join(home, ".pyry", "test"))
	// History survives starts; ring/envelope counters start afresh. A large offset
	// makes either ID substitution fail, including startup frames on this session.
	for range 100 {
		if _, err := store.Append(conversations.ConversationID(convID), protocol.TypeMessage, json.RawMessage(`{}`), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	h := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL)
	t.Cleanup(func() { h.Stop(t) })
	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(ctx, fr.URL(), serverID, pair.Token, "phone-a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, pair.Token)
	sendSealedEnvelope(t, phone, send, protocol.Envelope{
		ID: 28610, Type: protocol.TypeSendMessage, TS: time.Now().UTC(),
		Payload: mustJSON(t, protocol.SendMessagePayload{ConversationID: convID, MessageID: messageID, Text: text}),
	})
	var live protocol.Envelope
	deadline := time.Now().Add(15 * time.Second)
	for {
		env, ok := nextAttachmentEnvelope(t, phone, recv, deadline)
		if !ok {
			t.Fatal("direct live operator message did not arrive")
		}
		if env.Type != protocol.TypeMessage {
			continue
		}
		var msg protocol.MessagePayload
		if err := json.Unmarshal(env.Payload, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.ConversationID == convID && msg.MessageID == messageID && msg.Role == "user" && msg.Text == text {
			live = env
			break
		}
	}
	if live.HistoryEntryID == nil || *live.HistoryEntryID <= 100 {
		t.Fatalf("history_entry_id=%v, want the new durable entry after the seeds", live.HistoryEntryID)
	}
	if live.EventID == nil || *live.HistoryEntryID == *live.EventID || *live.HistoryEntryID == live.ID {
		t.Fatalf("fixture must distinguish history, ring and envelope ids: %+v", live)
	}
	sendRequestHistoryE2E(t, phone, send, 28611, protocol.RequestHistoryPayload{ConversationID: convID, Limit: 128})
	page := awaitHistoryPage(t, phone, recv, 28611, 15*time.Second)
	matches := 0
	for _, entry := range page.Entries {
		if entry.Type == live.Type && bytes.Equal(entry.Payload, live.Payload) && entry.TS.Equal(live.TS) {
			matches++
			if entry.ID != *live.HistoryEntryID {
				t.Fatalf("served entry id=%d, live history_entry_id=%d", entry.ID, *live.HistoryEntryID)
			}
		}
	}
	if matches != 1 {
		t.Fatalf("found %d served entries matching live type, payload and timestamp, want one", matches)
	}
}

// TestRelayV2_ConversationHistory serves seeded history to a paired phone before
// any message is sent. The raw log has six legacy entries and a hidden restart
// divider projected into a nonvisual receipt. The walks preserve every seeded
// entry and the receipt exactly once, with their original IDs and timestamps,
// retaining raw cursors and AtStart even on receipt-only and empty terminal pages.
// Rejected requests expose no content and do not stall interrupts.
func TestRelayV2_ConversationHistory(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111"
		knownConvID = "77777777-7777-4777-8777-777777777777"
		// The bootstrap child's conversation, and the session knownConvID is bound
		// to, which no child runs during this test.
		bootstrapConvID = "66666666-6666-4666-8666-666666666666"
		knownSessionID  = "22222222-2222-4222-8222-222222222222"
		// Canonically shaped and never hosted: the membership gate must be what
		// refuses it, not a shape check.
		foreignConvID = "88888888-8888-4888-8888-888888888888"
		// A marker no daemon-authored string can contain, so "the reject echoed
		// the cursor" is checkable against what a leak would actually carry.
		badCursor     = "ZZ2116-E2E-CURSOR-ZZ"
		seeded        = 6
		pageSize      = 2
		firstReqID    = uint64(21160)
		foreignReqID  = uint64(21190)
		badCursorReq  = uint64(21191)
		replyDeadline = 15 * time.Second
	)

	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"

	payload, err := paireddevice.Setup(paireddevice.Config{
		Home:                   home,
		InstanceName:           "test",
		Relay:                  relayURL,
		DeviceName:             "phone-a",
		AllowRemotePermissions: false,
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	// The conversation must be in the registry the daemon loads at startup, or
	// KnownConversation refuses before any log is opened — the gate doing its job,
	// and it would make the walk below fail for the wrong reason.
	//
	// It is bound to a session no child runs, NOT the bootstrap's. Since #2739 every
	// stream event is recorded under the conversation its own session belongs to,
	// with or without a routed message, so the bootstrap child's startup frames land
	// in whichever conversation is bound to initialUUID — racing the walk if that
	// were knownConvID. A separate conversation takes the bootstrap session.
	convJSON := []byte(`{"conversations":[` +
		`{"id":"` + bootstrapConvID + `","cwd":"` + home + `","current_session_id":"` + initialUUID +
		`","is_promoted":false,"last_used_at":"2026-01-01T00:00:00Z"},` +
		`{"id":"` + knownConvID + `","cwd":"` + home + `","current_session_id":"` + knownSessionID +
		`","is_promoted":false,"last_used_at":"2026-01-01T00:00:00Z"}]}`)
	if err := os.MkdirAll(filepath.Join(home, ".pyry", "test"), 0o700); err != nil {
		t.Fatalf("mkdir instance dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".pyry", "test", "conversations.json"), convJSON, 0o600); err != nil {
		t.Fatalf("seed conversations.json: %v", err)
	}

	// Seeded through the log's own writer, over the instance directory the daemon
	// will open. Ids run 1..seeded, so the walk's expectation is exact.
	instanceDir := filepath.Join(home, ".pyry", "test")
	store := history.New(instanceDir)
	base := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	for i := 0; i < seeded; i++ {
		body := json.RawMessage(fmt.Sprintf(`{"text":"seeded-%d"}`, i))
		if _, err := store.Append(conversations.ConversationID(knownConvID), protocol.TypeAssistantDelta, body, base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("seed history entry %d: %v", i, err)
		}
	}

	h := StartStreamInteractiveWithRelay(t, home, initialUUID, relayURL)
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)
	waitBinaryHello(t, fr, serverID)
	raw, err := store.Page(conversations.ConversationID(knownConvID), "", seeded+2)
	if err != nil {
		t.Fatalf("read startup history: %v", err)
	}
	if len(raw.Entries) != seeded+1 || !raw.AtStart {
		t.Fatalf("startup raw history = %+v, want six seeds and one restart divider", raw)
	}
	divider := raw.Entries[0]
	var fact struct {
		Cause string `json:"cause"`
	}
	if err := json.Unmarshal(divider.Payload, &fact); err != nil {
		t.Fatal(err)
	}
	if divider.ID != seeded+1 || divider.Type != "session_divider" || divider.Shown == nil || *divider.Shown || fact.Cause != "daemon_restart" {
		t.Fatalf("unexpected startup divider: %+v", divider)
	}

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, payload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })
	send, recv := driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, payload.Token)

	// ── The walk ──
	// No send_message first: a client that has just opened a conversation asks for
	// history straight away, and that is the whole point of the verb.
	reqID := firstReqID
	for _, walk := range []struct {
		name   string
		limit  int
		counts []int
	}{
		{"partial_terminal", pageSize, []int{2, 2, 2, 1}},
		{"exact_fill", seeded + 1, []int{seeded + 1, 0}},
		{"hidden_only_head", 1, []int{1, 1, 1, 1, 1, 1, 1, 0}},
	} {
		t.Run(walk.name, func(t *testing.T) {
			seen := make([]uint64, 0, seeded)
			receipts := 0
			cursor := ""
			pages := 0
			for {
				rawPage, err := store.Page(conversations.ConversationID(knownConvID), cursor, walk.limit)
				if err != nil {
					t.Fatalf("read raw page: %v", err)
				}
				sendRequestHistoryE2E(t, phone, send, reqID, protocol.RequestHistoryPayload{
					ConversationID: knownConvID,
					Cursor:         cursor,
					Limit:          walk.limit,
				})
				page := awaitHistoryPage(t, phone, recv, reqID, replyDeadline)
				reqID++
				pages++
				if pages > len(walk.counts) {
					t.Fatal("walk did not terminate")
				}
				if len(page.Entries) != walk.counts[pages-1] {
					t.Errorf("page %d carried %d entries, want %d", pages, len(page.Entries), walk.counts[pages-1])
				}
				if page.Cursor != rawPage.Cursor || page.AtStart != rawPage.AtStart {
					t.Error("legacy page changed the raw cursor or AtStart")
				}
				if page.AtStart != (pages == len(walk.counts)) {
					t.Errorf("page %d at_start = %v, want %v", pages, page.AtStart, pages == len(walk.counts))
				}
				for _, e := range page.Entries {
					if e.ID == divider.ID {
						if e.Type != protocol.TypeBanner || !e.TS.Equal(divider.TS) {
							t.Fatalf("receipt type/timestamp changed: %+v", e)
						}
						var got map[string]any
						if err := json.Unmarshal(e.Payload, &got); err != nil {
							t.Fatalf("decode receipt: %v", err)
						}
						want := map[string]any{
							"conversation_id": knownConvID, "level": "info", "text": "",
							"truncated": false, "stops_turn": false,
						}
						if !reflect.DeepEqual(got, want) {
							t.Fatalf("receipt payload = %#v, want %#v", got, want)
						}
						receipts++
						continue
					}
					if e.ID == 0 || e.ID > seeded || e.Type != protocol.TypeAssistantDelta {
						t.Fatalf("legacy page included unexpected entry: %+v", e)
					}
					wantPayload := fmt.Sprintf(`{"text":"seeded-%d"}`, e.ID-1)
					if string(e.Payload) != wantPayload || !e.TS.Equal(base.Add(time.Duration(e.ID-1)*time.Second)) {
						t.Fatalf("seeded payload/timestamp changed: %+v", e)
					}
					seen = append(seen, e.ID)
				}
				if page.AtStart {
					if page.Cursor != "" {
						t.Error("terminal page cursor must be empty")
					}
					break
				}
				if page.Cursor == "" {
					t.Fatalf("page %d is not at_start and carries no cursor", pages)
				}
				cursor = page.Cursor
			}
			if receipts != 1 {
				t.Fatalf("walked %d restart receipts, want exactly one", receipts)
			}
			if len(seen) != seeded {
				t.Fatalf("walked %d entries, want %d — none skipped, none repeated", len(seen), seeded)
			}
			for i, id := range seen {
				if want := uint64(seeded - i); id != want {
					t.Errorf("walked entry %d = id %d, want %d — newest-first, exactly once", i, id, want)
				}
			}
		})
	}

	// ── A conversation this daemon does not host ──
	sendRequestHistoryE2E(t, phone, send, foreignReqID, protocol.RequestHistoryPayload{
		ConversationID: foreignConvID,
		Limit:          pageSize,
	})
	ep := awaitHistoryReject(t, phone, recv, foreignReqID, replyDeadline)
	if ep.Code != protocol.CodeConversationNotFound {
		t.Errorf("foreign conversation reject code = %q, want %q", ep.Code, protocol.CodeConversationNotFound)
	}
	if ep.Retryable {
		t.Error("conversation.not_found answered retryable; a conversation this daemon does not host is not a transient condition")
	}

	// ── A cursor the log refuses ──
	// AND the no-stall claim: the interrupt below is sent immediately after the
	// request, takes a Run-inline arm rather than the conn's app-frame worker, and
	// must be serviced while the request is still being answered off Run. The
	// daemon staying responsive enough to answer BOTH is the observable.
	sendRequestHistoryE2E(t, phone, send, badCursorReq, protocol.RequestHistoryPayload{
		ConversationID: knownConvID,
		Cursor:         badCursor,
		Limit:          pageSize,
	})
	sendInterruptFrame(t, phone, send, badCursorReq+1)

	ep = awaitHistoryReject(t, phone, recv, badCursorReq, replyDeadline)
	if ep.Code != protocol.CodeHistoryInvalidCursor {
		t.Errorf("bad-cursor reject code = %q, want %q — and one merged answer across all three cursor causes",
			ep.Code, protocol.CodeHistoryInvalidCursor)
	}
	if ep.Retryable {
		t.Error("history.invalid_cursor answered retryable; a cursor the log will not follow is permanent for the request as sent")
	}

	// The static-message claim, checked against what a leak would actually
	// contain rather than against a fixed string: the cursor, the conversation id
	// and a host path are the three values a message derived from an error would
	// carry, and internal/history's own errors DO format absolute paths.
	for what, needle := range map[string]string{
		"the rejected cursor": badCursor,
		"the conversation id": knownConvID,
		"a host path":         home,
	} {
		if strings.Contains(ep.Message, needle) {
			t.Errorf("the reject message carries %s; every refusal on this verb answers a static message", what)
		}
	}

	// The same three must be absent from the daemon's own log for the cursor —
	// nothing above internal/history validates a cursor's shape, so raw it is the
	// log-injection shape § Conversation history forbids. The conversation id is
	// deliberately NOT checked here: it is loggable past the membership gate.
	if strings.Contains(h.Stderr.String(), badCursor) {
		t.Error("the daemon logged the rejected cursor; a cursor is loggable nowhere")
	}
}

// sendRequestHistoryE2E seals one request_history under envID and sends it. The
// envelope id is load-bearing: the page and every reject correlate on it through
// in_reply_to, and the payload carries no request-id key.
func sendRequestHistoryE2E(t *testing.T, phone *fakephone.Client, send *noise.CipherState, envID uint64, req protocol.RequestHistoryPayload) {
	t.Helper()
	sendSealedEnvelope(t, phone, send, protocol.Envelope{
		ID:      envID,
		Type:    protocol.TypeRequestHistory,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, req),
	})
}

// sendInterruptFrame seals one interrupt, which the daemon handles INLINE on its
// Run goroutine. That is what makes it the right second frame for the no-stall
// claim: a frame that also routed through the conn's app-frame worker would prove
// nothing, since the worker is deliberately FIFO per conn.
func sendInterruptFrame(t *testing.T, phone *fakephone.Client, send *noise.CipherState, envID uint64) {
	t.Helper()
	sendSealedEnvelope(t, phone, send, protocol.Envelope{
		ID:   envID,
		Type: protocol.TypeInterrupt,
		TS:   time.Now().UTC(),
	})
}

func sendSealedEnvelope(t *testing.T, phone *fakephone.Client, send *noise.CipherState, env protocol.Envelope) {
	t.Helper()
	envBytes, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal %s envelope %d: %v", env.Type, env.ID, err)
	}
	ciphertext, err := send.Encrypt(envBytes)
	if err != nil {
		t.Fatalf("seal %s envelope %d: %v", env.Type, env.ID, err)
	}
	sendNoiseMsg(t, phone, ciphertext)
}

// awaitHistoryPage reads until the history_page correlated to inReplyTo arrives,
// failing on an error frame answering the same request and reading past anything
// else — the bootstrap session's own pushes share this conn.
//
// It reuses nextAttachmentEnvelope, whose name is the retrieval leg's but whose
// job is generic: decrypt the next envelope in arrival order, which is what keeps
// the receive nonce in lockstep. Classifying AFTER the decrypt is the load-bearing
// half; skipping a frame would desynchronise every later one.
func awaitHistoryPage(t *testing.T, phone *fakephone.Client, recv *noise.CipherState, inReplyTo uint64, within time.Duration) protocol.HistoryPagePayload {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		env, ok := nextAttachmentEnvelope(t, phone, recv, deadline)
		if !ok {
			t.Fatalf("no history_page arrived for request %d; a request must be answered, not dropped", inReplyTo)
		}
		if env.InReplyTo == nil || *env.InReplyTo != inReplyTo {
			continue
		}
		switch env.Type {
		case protocol.TypeHistoryPage:
			var p protocol.HistoryPagePayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("decode history_page payload: %v", err)
			}
			return p
		case protocol.TypeError:
			var ep protocol.ErrorPayload
			if err := json.Unmarshal(env.Payload, &ep); err != nil {
				t.Fatalf("request %d refused, and its error payload did not decode: %v", inReplyTo, err)
			}
			t.Fatalf("request %d refused with code %q (retryable=%v); the conversation is seeded and hosted",
				inReplyTo, ep.Code, ep.Retryable)
		}
	}
}

// awaitHistoryReject is awaitHistoryPage's mirror: it fails if a PAGE answers the
// request, so no row can pass while the daemon also served entries.
func awaitHistoryReject(t *testing.T, phone *fakephone.Client, recv *noise.CipherState, inReplyTo uint64, within time.Duration) protocol.ErrorPayload {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		env, ok := nextAttachmentEnvelope(t, phone, recv, deadline)
		if !ok {
			t.Fatalf("no error frame arrived for request %d; a refused request must be answered, not dropped", inReplyTo)
		}
		if env.InReplyTo == nil || *env.InReplyTo != inReplyTo {
			continue
		}
		switch env.Type {
		case protocol.TypeError:
			var ep protocol.ErrorPayload
			if err := json.Unmarshal(env.Payload, &ep); err != nil {
				t.Fatalf("decode error payload: %v", err)
			}
			return ep
		case protocol.TypeHistoryPage:
			t.Fatalf("a history_page answered request %d, which must be refused", inReplyTo)
		}
	}
}
