package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/dispatch"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	markReadConnID     = "c-mark-read"
	markReadRequestID  = uint64(53)
	markReadTargetID   = "44444444-4444-4444-8444-444444444444"
	markReadPayloadTag = "INJECTED_MARK_READ_MARKER_3f9"
)

// fakeLatestHistory is a deterministic historyLatestReader that records which
// conversations it was asked about.
type fakeLatestHistory struct {
	mu     sync.Mutex
	latest uint64
	err    error
	asked  []conversations.ConversationID
}

func (h *fakeLatestHistory) LatestEntryID(id conversations.ConversationID) (uint64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asked = append(h.asked, id)
	return h.latest, h.err
}

func (h *fakeLatestHistory) calls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.asked)
}

// newMarkReadReg seeds one muted, promoted row holding the given mark at a
// registry path that has never been written.
func newMarkReadReg(t *testing.T, held uint64) (*conversations.Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversations.json")
	reg, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	name := "read-target"
	reg.Create(conversations.Conversation{
		ID:         markReadTargetID,
		Name:       &name,
		Cwd:        "/work/read",
		IsPromoted: true,
		IsMuted:    true,
		ReadUpTo:   held,
		LastUsedAt: time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC),
	})
	return reg, path
}

func markReadRequest(payload string) protocol.Envelope {
	return protocol.Envelope{ID: markReadRequestID, Type: protocol.TypeMarkConversationRead, TS: time.Now().UTC(), Payload: []byte(payload)}
}

func markReadPayload(upTo string) string {
	return `{"conversation_id":"` + markReadTargetID + `","up_to":` + upTo + `}`
}

// runMarkRead runs one request on a fresh conn and returns the single reply.
func runMarkRead(t *testing.T, h dispatch.Handler, payload string) (protocol.Envelope, []byte) {
	t.Helper()
	out := make(chan protocol.RoutingEnvelope, 4)
	c := dispatch.NewTestConn(markReadConnID, out, nil)
	if err := h(context.Background(), c, markReadRequest(payload)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d replies, want 1", len(out))
	}
	resp := <-out
	var env protocol.Envelope
	if err := json.Unmarshal(resp.Frame, &env); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	if env.InReplyTo == nil || *env.InReplyTo != markReadRequestID {
		t.Fatalf("InReplyTo = %v, want %d", env.InReplyTo, markReadRequestID)
	}
	return env, resp.Frame
}

func storedMark(t *testing.T, reg *conversations.Registry) uint64 {
	t.Helper()
	cv, ok := reg.Get(markReadTargetID)
	if !ok {
		t.Fatal("seeded row missing")
	}
	return cv.ReadUpTo
}

// TestMarkConversationRead_Success covers the clamp-then-monotonic rule, the
// correlated reply on every success, and the push and save on an advance only.
func TestMarkConversationRead_Success(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		held, latest uint64
		upTo         string
		want         uint64
		advanced     bool
	}{
		{"advance", 2, 10, "6", 6, true},
		{"clamp over-large to latest", 2, 10, "18446744073709551615", 10, true},
		{"equal is no-op", 6, 10, "6", 6, false},
		{"lower is no-op", 6, 10, "0", 6, false},
		{"empty conversation stays at zero", 0, 0, "5", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg, path := newMarkReadReg(t, tc.held)
			ann := &recordingAnnouncer{}
			h := MarkConversationRead(reg, &fakeLatestHistory{latest: tc.latest}, path, ann.announce, testLogger(t))

			env, _ := runMarkRead(t, h, markReadPayload(tc.upTo))
			if env.Type != protocol.TypeConversationUpdated {
				t.Fatalf("Type = %q, payload %s", env.Type, env.Payload)
			}
			var got protocol.ConversationUpdatedPayload
			if err := json.Unmarshal(env.Payload, &got); err != nil {
				t.Fatal(err)
			}
			if got.ID != markReadTargetID || got.ReadUpTo != tc.want || !got.IsMuted || !got.IsPromoted || got.Cwd != "/work/read" {
				t.Fatalf("reply record = %+v, want read_up_to %d", got, tc.want)
			}
			if storedMark(t, reg) != tc.want {
				t.Fatalf("stored = %d, want %d", storedMark(t, reg), tc.want)
			}

			pushed := ann.records()
			_, statErr := os.Stat(path)
			if !tc.advanced {
				if len(pushed) != 0 || !errors.Is(statErr, fs.ErrNotExist) {
					t.Fatalf("no-op pushed %d records, saved=%v", len(pushed), statErr == nil)
				}
				return
			}
			if len(pushed) != 1 || !bytes.Equal(mustMarshal(t, pushed[0]), env.Payload) {
				t.Fatalf("pushed %+v, want exactly the reply record %s", pushed, env.Payload)
			}
			loaded, err := conversations.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cv, _ := loaded.Get(markReadTargetID); cv.ReadUpTo != tc.want {
				t.Fatalf("persisted = %d, want %d", cv.ReadUpTo, tc.want)
			}
		})
	}
}

// TestMarkConversationRead_Rejects pins the non-retryable refusals: nothing is
// read from history for an unproven id, nothing is mutated, saved or pushed,
// and the payload marker reaches neither the reply nor the log.
func TestMarkConversationRead_Rejects(t *testing.T) {
	t.Parallel()
	malformed := func(upTo string) string {
		return `{"conversation_id":"` + markReadTargetID + `","x":"` + markReadPayloadTag + `","up_to":` + upTo + `}`
	}
	cases := []struct {
		name, payload, code, msg string
	}{
		{"undecodable", `{"conversation_id":"` + markReadPayloadTag + `"`, protocol.CodeProtocolMalformed, msgMarkReadMalformed},
		{"missing", `{"conversation_id":"` + markReadTargetID + `","x":"` + markReadPayloadTag + `"}`, protocol.CodeProtocolMalformed, msgMarkReadMalformed},
		{"null", malformed("null"), protocol.CodeProtocolMalformed, msgMarkReadMalformed},
		{"negative", malformed("-1"), protocol.CodeProtocolMalformed, msgMarkReadMalformed},
		{"fractional", malformed("1.5"), protocol.CodeProtocolMalformed, msgMarkReadMalformed},
		{"exponent", malformed("1e2"), protocol.CodeProtocolMalformed, msgMarkReadMalformed},
		{"string", malformed(`"5"`), protocol.CodeProtocolMalformed, msgMarkReadMalformed},
		{"boolean", malformed("true"), protocol.CodeProtocolMalformed, msgMarkReadMalformed},
		{"overflow", malformed("18446744073709551616"), protocol.CodeProtocolMalformed, msgMarkReadMalformed},
		{"id wrong type", `{"conversation_id":7,"up_to":1,"x":"` + markReadPayloadTag + `"}`, protocol.CodeProtocolMalformed, msgMarkReadMalformed},
		{"unknown id", `{"conversation_id":"` + markReadPayloadTag + `","up_to":3}`, protocol.CodeConversationNotFound, msgMarkReadNotFound},
		{"empty id", `{"conversation_id":"","up_to":3,"x":"` + markReadPayloadTag + `"}`, protocol.CodeConversationNotFound, msgMarkReadNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logBuf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
			reg, path := newMarkReadReg(t, 2)
			hist := &fakeLatestHistory{latest: 10}
			ann := &recordingAnnouncer{}

			env, frame := runMarkRead(t, MarkConversationRead(reg, hist, path, ann.announce, logger), tc.payload)
			if env.Type != protocol.TypeError {
				t.Fatalf("Type = %q", env.Type)
			}
			if p := assertErrorPayload(t, env, tc.code, tc.msg); p.Retryable {
				t.Error("Retryable = true, want false")
			}
			if bytes.Contains(frame, []byte(markReadPayloadTag)) || strings.Contains(logBuf.String(), markReadPayloadTag) {
				t.Errorf("payload bytes leaked: frame %s log %s", frame, logBuf.String())
			}
			if hist.calls() != 0 {
				t.Errorf("history read %d times before the id was resolved", hist.calls())
			}
			if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) || storedMark(t, reg) != 2 || len(ann.records()) != 0 {
				t.Errorf("reject changed state: stat %v, mark %d, pushes %d", err, storedMark(t, reg), len(ann.records()))
			}
		})
	}
}

// TestMarkConversationRead_HistoryUnavailable covers a failed history read and
// an absent (nil or typed-nil) store: retryable history.unavailable, no change.
func TestMarkConversationRead_HistoryUnavailable(t *testing.T) {
	t.Parallel()
	cases := map[string]historyLatestReader{
		"read error": &fakeLatestHistory{err: errors.New("segment corrupt")},
		"nil":        nil,
		"typed nil":  (*history.Store)(nil),
	}
	for name, hist := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			reg, path := newMarkReadReg(t, 2)
			ann := &recordingAnnouncer{}
			env, _ := runMarkRead(t, MarkConversationRead(reg, hist, path, ann.announce, testLogger(t)), markReadPayload("5"))
			if p := assertErrorPayload(t, env, protocol.CodeHistoryUnavailable, msgMarkReadHistoryUnavail); !p.Retryable {
				t.Error("Retryable = false, want true")
			}
			if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) || storedMark(t, reg) != 2 || len(ann.records()) != 0 {
				t.Errorf("history failure changed state: stat %v, mark %d", err, storedMark(t, reg))
			}
		})
	}
}

// TestMarkConversationRead_SaveFailureThenRetry pins that a failed save is not
// acknowledged and leaves no unsaved advance behind: once the registry can be
// written again, the same request persists the mark and pushes it.
func TestMarkConversationRead_SaveFailureThenRetry(t *testing.T) {
	t.Parallel()
	reg, _ := newMarkReadReg(t, 2)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "conversations.json") // parent is a regular file
	ann := &recordingAnnouncer{}
	h := MarkConversationRead(reg, &fakeLatestHistory{latest: 10}, path, ann.announce, testLogger(t))

	env, _ := runMarkRead(t, h, markReadPayload("7"))
	if p := assertErrorPayload(t, env, protocol.CodeReadMarkUnavailable, msgMarkReadSaveUnavailable); !p.Retryable {
		t.Error("Retryable = false, want true")
	}
	if storedMark(t, reg) != 2 || len(ann.records()) != 0 {
		t.Fatalf("failed save left mark %d, pushes %d", storedMark(t, reg), len(ann.records()))
	}

	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	env, _ = runMarkRead(t, h, markReadPayload("7"))
	if env.Type != protocol.TypeConversationUpdated {
		t.Fatalf("retry Type = %q, payload %s", env.Type, env.Payload)
	}
	if pushed := ann.records(); len(pushed) != 1 || pushed[0].ReadUpTo != 7 {
		t.Fatalf("retry pushed %+v, want one record at 7", pushed)
	}
	loaded, err := conversations.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cv, _ := loaded.Get(markReadTargetID); cv.ReadUpTo != 7 {
		t.Fatalf("persisted = %d, want 7", cv.ReadUpTo)
	}
}

// TestMarkConversationRead_ReplyFailureStillPushes pins that a torn-down
// requester does not keep the other clients from hearing an advance.
func TestMarkConversationRead_ReplyFailureStillPushes(t *testing.T) {
	t.Parallel()
	reg, path := newMarkReadReg(t, 2)
	ann := &recordingAnnouncer{}
	h := MarkConversationRead(reg, &fakeLatestHistory{latest: 10}, path, ann.announce, testLogger(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := dispatch.NewTestConn(markReadConnID, make(chan protocol.RoutingEnvelope), nil)
	if err := h(ctx, c, markReadRequest(markReadPayload("4"))); err == nil {
		t.Fatal("handler returned nil for a failed reply")
	}
	if pushed := ann.records(); len(pushed) != 1 || pushed[0].ReadUpTo != 4 {
		t.Fatalf("pushed %+v, want one record at 4", pushed)
	}
}
