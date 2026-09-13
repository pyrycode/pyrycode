//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
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

// TestRelayV2_ConversationHistory is the first end-to-end run of the
// conversation-history verb (#2116) — a real daemon reading a real on-disk log
// and answering a paired phone. Every piece under it is unit-tested inside its
// own package: the log and its cursor (#2112), the wire shapes (#2113), the
// handler and its rejects, and the cmd/pyry sentinel classifier; nothing until
// now has run the whole chain in one process.
//
// FOUR CLAIMS, ONE RUN:
//
//   - THE WALK. A conversation with recorded traffic is answered WITHOUT THE
//     CLIENT SENDING A MESSAGE FIRST, and echoing each page's cursor back walks to
//     the start returning every entry exactly once, newest-first.
//   - THE BOUNDARY. The walk terminates on `at_start` and never on an empty
//     `entries`: with six entries walked two at a time the third page fills
//     EXACTLY at the first entry and must report `at_start` false with a usable
//     cursor, and only the fourth call is the empty terminal one.
//   - THE REJECTS reach the wire as coded errors that echo nothing.
//   - NO STALL. A frame sent immediately after a request is serviced while that
//     request is still being answered.
//
// THE LOG IS SEEDED DIRECTLY ONTO THE HOST rather than produced by driving a
// turn, the same choice TestRelayV2_AttachmentRetrieval makes about a stored
// file and for the same reasons. The producers are #2114's and #2115's subject
// and are covered where they live; driving one here would make this run depend on
// them and would cost a full turn round trip to reach the interactive chokepoint.
// The seeding uses the log's own Append, so what lands is exactly what a producer
// would have left — same segment format, same id sequence — and it happens before
// the daemon starts, so this process is the only writer at any moment.
func TestRelayV2_ConversationHistory(t *testing.T) {
	const (
		initialUUID = "11111111-1111-4111-8111-111111111111"
		knownConvID = "77777777-7777-4777-8777-777777777777"
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
	seedBoundConversation(t, home, knownConvID, initialUUID)

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
	seen := make([]uint64, 0, seeded)
	cursor := ""
	pages := 0
	for {
		reqID := firstReqID + uint64(pages)
		sendRequestHistoryE2E(t, phone, send, reqID, protocol.RequestHistoryPayload{
			ConversationID: knownConvID,
			Cursor:         cursor,
			Limit:          pageSize,
		})
		page := awaitHistoryPage(t, phone, recv, reqID, replyDeadline)
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
		if pages > seeded+2 {
			t.Fatal("walk did not terminate")
		}
	}

	if pages != seeded/pageSize+1 {
		t.Errorf("walk took %d pages, want %d — %d full pages then the empty terminal one",
			pages, seeded/pageSize+1, seeded/pageSize)
	}
	if len(seen) != seeded {
		t.Fatalf("walked %d entries, want %d — none skipped, none repeated", len(seen), seeded)
	}
	for i, id := range seen {
		if want := uint64(seeded - i); id != want {
			t.Errorf("walked entry %d = id %d, want %d — newest-first, exactly once", i, id, want)
		}
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
