package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// The three values one announcement carries, shared by every case below. The
// filename is deliberately distinctive so the never-log assertions cannot pass
// against a buffer that simply never mentioned it — a substring like "file"
// would appear in half the daemon's vocabulary.
const (
	offerConvID = "44444444-4444-4444-8444-444444444444"
	offerAttID  = "55555555-5555-4555-8555-555555555555"
	offerName   = "pyry-2166-announced-leaf.bin"
)

// debugLogger captures at LevelDebug, which bufLogger's nil options do not: the
// push-error branch logs at Debug, so an Info-level handler would make the
// "it was logged, and content-free" assertions vacuous in the one direction
// that matters.
func debugLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// decodeOffer decodes one recorded push as an attachment_offered payload,
// failing the test if it is any other frame.
func decodeOffer(t *testing.T, p recordedPush) protocol.AttachmentOfferedPayload {
	t.Helper()
	if p.env.Type != protocol.TypeAttachmentOffered {
		t.Fatalf("pushed envelope type = %q, want %q", p.env.Type, protocol.TypeAttachmentOffered)
	}
	var got protocol.AttachmentOfferedPayload
	if err := json.Unmarshal(p.env.Payload, &got); err != nil {
		t.Fatalf("decode attachment_offered payload: %v", err)
	}
	return got
}

// AC-1: an announcement reaches every interactive-capable conn and no other.
// The #607 capability gate is the whole of the table's second and third rows —
// a non-interactive conn is attached and authenticated, so nothing but the flag
// separates it from a recipient.
func TestAttachmentOfferEmitterV2_Announce_GatesOnInteractive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		conns []relay.ActiveConn
		want  []string // conn ids that must receive the offer, in fan-out order
	}{
		{
			name:  "one interactive conn",
			conns: []relay.ActiveConn{{ConnID: "c1", Interactive: true}},
			want:  []string{"c1"},
		},
		{
			name:  "a non-interactive conn receives nothing",
			conns: []relay.ActiveConn{{ConnID: "c1", Interactive: false}},
			want:  nil,
		},
		{
			name: "mixed set delivers only to the interactive members",
			conns: []relay.ActiveConn{
				{ConnID: "c1", Interactive: true},
				{ConnID: "c2", Interactive: false},
				{ConnID: "c3", Interactive: true},
			},
			want: []string{"c1", "c3"},
		},
		{
			name:  "no conns at all",
			conns: nil,
			want:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{tc.conns}}
			var buf bytes.Buffer
			e := newAttachmentOfferEmitterV2(bcast, context.Background(), debugLogger(&buf))

			e.announce(offerConvID, offerAttID, offerName)

			var got []string
			for _, p := range bcast.pushes {
				got = append(got, p.connID)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("offer reached conns %v, want %v", got, tc.want)
			}
			for _, p := range bcast.pushes {
				payload := decodeOffer(t, p)
				if payload.ConversationID != offerConvID {
					t.Errorf("conversation_id = %q, want %q", payload.ConversationID, offerConvID)
				}
				if payload.AttachmentID != offerAttID {
					t.Errorf("attachment_id = %q, want %q", payload.AttachmentID, offerAttID)
				}
				if payload.Filename != offerName {
					t.Errorf("filename = %q, want %q — the announced name is passed through "+
						"verbatim, not re-derived", payload.Filename, offerName)
				}
			}
		})
	}
}

// One timestamp for the whole fan-out, and strictly increasing envelope ids
// across announcements. The shared timestamp is what makes two clients' copies
// of one announcement the same event; the monotonic id is what a client orders
// on. Both are properties of the loop rather than of any one push, so neither is
// visible from a single-conn case.
func TestAttachmentOfferEmitterV2_Announce_SharedTimestampMonotonicIDs(t *testing.T) {
	t.Parallel()
	bcast := &fakeInteractiveBcast{snapshots: [][]relay.ActiveConn{{
		{ConnID: "c1", Interactive: true},
		{ConnID: "c2", Interactive: true},
	}}}
	var buf bytes.Buffer
	e := newAttachmentOfferEmitterV2(bcast, context.Background(), debugLogger(&buf))

	e.announce(offerConvID, offerAttID, offerName)
	e.announce(offerConvID, "66666666-6666-4666-8666-666666666666", "second.bin")

	if len(bcast.pushes) != 4 {
		t.Fatalf("recorded %d pushes, want 4 (two conns × two announcements)", len(bcast.pushes))
	}
	if !bcast.pushes[0].env.TS.Equal(bcast.pushes[1].env.TS) {
		t.Errorf("the two conns' copies of one announcement carry different timestamps (%v, %v); "+
			"the timestamp is taken once per announcement, outside the per-conn loop",
			bcast.pushes[0].env.TS, bcast.pushes[1].env.TS)
	}
	for i := 1; i < len(bcast.pushes); i++ {
		if bcast.pushes[i].env.ID <= bcast.pushes[i-1].env.ID {
			t.Errorf("envelope id %d (push %d) does not exceed %d (push %d); ids must be strictly "+
				"increasing across the whole emitter",
				bcast.pushes[i].env.ID, i, bcast.pushes[i-1].env.ID, i-1)
		}
	}
}

// AC-3: a torn-down conn is logged and skipped, never fatal. relay.ErrConnNotFound
// is exactly what Push answers for a conn that closed between the ActiveConns
// snapshot and the push, which is the reachable race rather than a hypothetical.
// The second conn must still receive its copy.
func TestAttachmentOfferEmitterV2_Announce_TornDownConnDoesNotStopTheFanOut(t *testing.T) {
	t.Parallel()
	bcast := &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{
			{ConnID: "gone", Interactive: true},
			{ConnID: "live", Interactive: true},
		}},
		pushErr: map[string]error{"gone": relay.ErrConnNotFound},
	}
	var buf bytes.Buffer
	e := newAttachmentOfferEmitterV2(bcast, context.Background(), debugLogger(&buf))

	e.announce(offerConvID, offerAttID, offerName)

	if len(bcast.pushes) != 2 {
		t.Fatalf("recorded %d pushes, want 2 — a failing conn must not end the fan-out", len(bcast.pushes))
	}
	if bcast.pushes[1].connID != "live" {
		t.Errorf("second push went to %q, want the conn after the failing one", bcast.pushes[1].connID)
	}
	logs := buf.String()
	if !strings.Contains(logs, "attachment_offer.push_err") {
		t.Errorf("a dropped push logged no attachment_offer.push_err line; got:\n%s", logs)
	}
	// The transport sentinel is the diagnostic, and it is the only part of the
	// error safe to carry: Push's whole error surface is ErrConnNotFound and a
	// ctx error, neither of which names any content.
	if !strings.Contains(logs, relay.ErrConnNotFound.Error()) {
		t.Errorf("the push-error line names no transport sentinel; got:\n%s", logs)
	}
	assertOfferLogsContentFree(t, logs)
}

// AC-3: the loop returns early on daemon teardown rather than logging once per
// conn for a manager that is going away. The distinguishing assertion is the
// SECOND conn getting no push at all — a loop that merely skipped the error
// would push twice.
func TestAttachmentOfferEmitterV2_Announce_TeardownReturnsEarly(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	bcast := &fakeInteractiveBcast{
		snapshots: [][]relay.ActiveConn{{
			{ConnID: "c1", Interactive: true},
			{ConnID: "c2", Interactive: true},
		}},
		pushErr: map[string]error{"c1": context.Canceled, "c2": context.Canceled},
	}
	var buf bytes.Buffer
	e := newAttachmentOfferEmitterV2(bcast, ctx, debugLogger(&buf))

	e.announce(offerConvID, offerAttID, offerName)

	if len(bcast.pushes) > 1 {
		t.Errorf("recorded %d pushes after teardown, want at most 1 — the loop returns on the "+
			"first ctx error rather than walking every conn", len(bcast.pushes))
	}
	if strings.Contains(buf.String(), "attachment_offer.push_err") {
		t.Errorf("teardown logged a push-error line; a daemon going away is not a dropped push:\n%s",
			buf.String())
	}
}

// The § Attachments ban, which sanitising does not lift: no log line carries the
// filename, on any branch. Checked on all three — the success path, the
// dropped-push path, and a marshal failure — because the ban binds each
// separately and a future edit is most likely to reintroduce the name into an
// error branch.
func TestAttachmentOfferEmitterV2_Announce_NoBranchLogsTheFilename(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pushErr error
	}{
		{name: "successful fan-out"},
		{name: "dropped push", pushErr: relay.ErrConnNotFound},
		{name: "push refused by a ctx error", pushErr: context.Canceled},
		{name: "push refused by an unexpected error", pushErr: errors.New("boom")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bcast := &fakeInteractiveBcast{
				snapshots: [][]relay.ActiveConn{{{ConnID: "c1", Interactive: true}}},
			}
			if tc.pushErr != nil {
				bcast.pushErr = map[string]error{"c1": tc.pushErr}
			}
			var buf bytes.Buffer
			e := newAttachmentOfferEmitterV2(bcast, context.Background(), debugLogger(&buf))

			e.announce(offerConvID, offerAttID, offerName)

			assertOfferLogsContentFree(t, buf.String())
		})
	}
}

// assertOfferLogsContentFree is the never-log rule as an assertion. The two ids
// stay loggable once their shape is validated, which is why only the name is
// banned here.
func assertOfferLogsContentFree(t *testing.T, logs string) {
	t.Helper()
	if strings.Contains(logs, offerName) {
		t.Errorf("a log line carries the announced filename, which docs/protocol-mobile.md "+
			"§ Attachments bans for a privacy reason sanitising does not lift:\n%s", logs)
	}
	// The extension alone, in case a future edit logs a derived fragment rather
	// than the whole name.
	if strings.Contains(logs, "announced-leaf") {
		t.Errorf("a log line carries part of the announced filename:\n%s", logs)
	}
}
