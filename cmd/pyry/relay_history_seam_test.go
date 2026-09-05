package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/relay"
)

// --- #2116 HistoryPage seam adapter ---

const (
	hpTestConvID    = "1c4d7e90-3a52-4b18-8e6f-2116beef0001" // canonical UUIDv4
	hpTestOtherConv = "1c4d7e90-3a52-4b18-8e6f-2116beef0002" // canonical UUIDv4, a DIFFERENT log
)

// hpBufLogger pairs bufLogger with its buffer, so a row can assert what the
// adapter did and did not record.
func hpBufLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return bufLogger(buf), buf
}

// hpAppend writes n entries into convID's log and returns nothing: every
// assertion below reads them back through the seam, which is the surface under
// test. Driving a REAL history.Store rather than a double is the point — the
// claims here are about which of that package's sentinels reach which outcome,
// and a double would be asserting the mapping against itself.
func hpAppend(t *testing.T, store *history.Store, convID string, n int) {
	t.Helper()
	base := time.Date(2026, 9, 5, 9, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		payload := json.RawMessage(fmt.Sprintf(`{"seq":%d,"text":"entry-%d"}`, i, i))
		if _, err := store.Append(conversations.ConversationID(convID), "assistant_delta", payload, base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("seed entry %d: %v", i, err)
		}
	}
}

// TestNewHistoryPager_ServesAndWalksARealLog pins the success path against a real
// store: the entries come back newest-first, mapped key for key, with the payload
// bytes verbatim, and the cursor walks to the terminal page.
func TestNewHistoryPager_ServesAndWalksARealLog(t *testing.T) {
	t.Parallel()

	store := history.New(t.TempDir())
	hpAppend(t, store, hpTestConvID, 5)
	logger, _ := hpBufLogger()
	pager := newHistoryPager(store, logger)

	res := pager(hpTestConvID, "", 2)
	if res.Outcome != relay.HistoryPageOK {
		t.Fatalf("outcome = %v, want HistoryPageOK", res.Outcome)
	}
	if len(res.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(res.Entries))
	}
	if res.Entries[0].ID != 5 || res.Entries[1].ID != 4 {
		t.Errorf("ids = %d,%d, want 5,4 — newest-first", res.Entries[0].ID, res.Entries[1].ID)
	}
	if res.Entries[0].Type != "assistant_delta" {
		t.Errorf("type = %q, want the stored wire type", res.Entries[0].Type)
	}
	if !strings.Contains(string(res.Entries[0].Payload), `"text":"entry-4"`) {
		t.Errorf("payload = %s, want the stored bytes carried through unchanged", res.Entries[0].Payload)
	}
	if res.AtStart || res.Cursor == "" {
		t.Fatalf("first page = {at_start:%v cursor:%q}, want a usable cursor and no at_start", res.AtStart, res.Cursor)
	}

	// The walk terminates on AtStart, never on an empty Entries, and every entry
	// comes back exactly once.
	seen := len(res.Entries)
	cursor := res.Cursor
	for steps := 0; ; steps++ {
		if steps > 8 {
			t.Fatal("walk did not terminate")
		}
		next := pager(hpTestConvID, cursor, 2)
		if next.Outcome != relay.HistoryPageOK {
			t.Fatalf("walk step %d outcome = %v, want HistoryPageOK", steps, next.Outcome)
		}
		seen += len(next.Entries)
		if next.AtStart {
			if next.Cursor != "" {
				t.Errorf("terminal cursor = %q, want empty whenever at_start is set", next.Cursor)
			}
			break
		}
		cursor = next.Cursor
	}
	if seen != 5 {
		t.Errorf("walked %d entries, want 5 — none skipped, none repeated", seen)
	}
}

// TestNewHistoryPager_ClassifiesEveryOutcome is the adapter's whole job: which of
// internal/history's refusals reaches which outcome. Each row drives the REAL
// store into the real refusal, so a sentinel renamed or re-raised elsewhere in
// that package reddens here rather than silently re-routing a wire code.
func TestNewHistoryPager_ClassifiesEveryOutcome(t *testing.T) {
	t.Parallel()

	// A cursor this store really minted, for the OTHER conversation — the
	// foreign-cursor cause, which parseCursor binds against.
	foreignStore := history.New(t.TempDir())

	tests := []struct {
		name string
		// setup returns the store and the (conversation, cursor, limit) to ask
		// with. Returning the store lets the nil-store row exist at all.
		setup func(t *testing.T) (*history.Store, string, string, int)
		want  relay.HistoryPageOutcome
	}{
		{
			name: "a nil store is unavailable, not inert",
			setup: func(t *testing.T) (*history.Store, string, string, int) {
				return nil, hpTestConvID, "", 10
			},
			want: relay.HistoryPageUnavailable,
		},
		{
			// Not an error: a conversation predating the log, or one with no
			// traffic, reads as the empty terminal page.
			name: "a conversation with no log on disk is the terminal page",
			setup: func(t *testing.T) (*history.Store, string, string, int) {
				return history.New(t.TempDir()), hpTestConvID, "", 10
			},
			want: relay.HistoryPageOK,
		},
		{
			name: "a cursor that does not decode",
			setup: func(t *testing.T) (*history.Store, string, string, int) {
				s := history.New(t.TempDir())
				hpAppend(t, s, hpTestConvID, 3)
				return s, hpTestConvID, "not-a-cursor", 10
			},
			want: relay.HistoryPageBadCursor,
		},
		{
			name: "a cursor minted for another conversation",
			setup: func(t *testing.T) (*history.Store, string, string, int) {
				hpAppend(t, foreignStore, hpTestOtherConv, 3)
				other := newHistoryPager(foreignStore, bufLogger(&bytes.Buffer{}))(hpTestOtherConv, "", 1)
				if other.Outcome != relay.HistoryPageOK || other.Cursor == "" {
					t.Fatalf("could not mint a foreign cursor: %+v", other)
				}
				hpAppend(t, foreignStore, hpTestConvID, 3)
				return foreignStore, hpTestConvID, other.Cursor, 10
			},
			want: relay.HistoryPageBadCursor,
		},
		{
			name: "a cursor naming a position not in this log",
			setup: func(t *testing.T) (*history.Store, string, string, int) {
				// Minted against a log that exists, then presented to a store whose
				// directory has never been written for that conversation.
				donor := history.New(t.TempDir())
				hpAppend(t, donor, hpTestConvID, 3)
				page := newHistoryPager(donor, bufLogger(&bytes.Buffer{}))(hpTestConvID, "", 1)
				if page.Outcome != relay.HistoryPageOK || page.Cursor == "" {
					t.Fatalf("could not mint a cursor: %+v", page)
				}
				return history.New(t.TempDir()), hpTestConvID, page.Cursor, 10
			},
			want: relay.HistoryPageBadCursor,
		},
		{
			// Unreachable in production — the relay's membership gate fires first
			// and the registry holds canonical ids only — but the mapping is
			// asserted anyway, because "unreachable" is a property of the caller
			// and this adapter must answer the fail-safe way regardless.
			name: "a non-canonical conversation id is a daemon-side failure",
			setup: func(t *testing.T) (*history.Store, string, string, int) {
				return history.New(t.TempDir()), "../../etc/passwd", "", 10
			},
			want: relay.HistoryPageUnavailable,
		},
		{
			// Likewise unreachable: the handler substitutes its own page size for a
			// zero and refuses a negative before this is called.
			name: "a page size below one is a daemon-side failure",
			setup: func(t *testing.T) (*history.Store, string, string, int) {
				return history.New(t.TempDir()), hpTestConvID, "", 0
			},
			want: relay.HistoryPageUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, convID, cursor, limit := tt.setup(t)
			logger, buf := hpBufLogger()
			got := newHistoryPager(store, logger)(convID, cursor, limit)
			if got.Outcome != tt.want {
				t.Errorf("outcome = %v, want %v", got.Outcome, tt.want)
			}
			if got.Outcome != relay.HistoryPageOK && (len(got.Entries) != 0 || got.Cursor != "" || got.AtStart) {
				t.Errorf("a refusal carried a page: %+v — the three fields are meaningful only on HistoryPageOK", got)
			}
			// The error never crosses the seam and never reaches the log, because
			// internal/history's messages format ABSOLUTE FILESYSTEM PATHS. The
			// discriminant may; the path may not.
			if logged := buf.String(); strings.Contains(logged, t.TempDir()) {
				t.Errorf("adapter log leaked a host path: %s", logged)
			}
			// A cursor is loggable NOWHERE: nothing above internal/history validates
			// its shape.
			if cursor != "" && strings.Contains(buf.String(), cursor) {
				t.Errorf("adapter log echoed the cursor: %s", buf.String())
			}
		})
	}
}

// TestHistoryPageFailure_NamesEverySentinel pins the operator-facing
// discriminant. The wire answer merges most of these; the LOG must not, or a
// corrupt segment and an ordinary read error are indistinguishable to the person
// who has to fix one of them.
func TestHistoryPageFailure_NamesEverySentinel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("wrapped: %w", history.ErrInvalidCursor), "invalid_cursor"},
		{fmt.Errorf("wrapped: %w", history.ErrInvalidID), "invalid_id"},
		{fmt.Errorf("wrapped: %w", history.ErrInvalidPageSize), "invalid_page_size"},
		{fmt.Errorf("wrapped: %w", history.ErrNotContained), "not_contained"},
		{fmt.Errorf("wrapped: %w", history.ErrCorruptSegment), "corrupt_segment"},
		{fmt.Errorf("wrapped: %w", history.ErrUnknownVersion), "unknown_version"},
		{fmt.Errorf("open /var/log/seg-0001.jsonl: permission denied"), "read"},
	}
	for _, tt := range tests {
		if got := historyPageFailure(tt.err); got != tt.want {
			t.Errorf("historyPageFailure(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}
