package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// TestWriteVerdictResponse asserts writeVerdictResponse emits the right needle in the
// assistant line + a result line per verdict, verified through the REAL
// streamsup.Parser (parseEmitted, from stream_detect_test.go) — the same daemon-side
// consumer, so a shape bug is caught on the emit side (different fabric). The full
// dial→verdict loop needs a live daemon socket and is covered by the e2e
// (relay_v2_stream_modal_test.go), not here.
func TestWriteVerdictResponse(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		verdict approveVerdict
		needle  string
	}{
		{"allow", verdictAllow, approveAllowNeedle},
		{"deny", verdictDeny, approveDenyNeedle},
		{"error", verdictError, approveErrorNeedle},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			if err := writeVerdictResponse(&buf, "m1", tc.verdict); err != nil {
				t.Fatalf("writeVerdictResponse: %v", err)
			}
			events := parseEmitted(t, buf.Bytes())
			if len(events) != 2 {
				t.Fatalf("got %d events, want 2 (TextChunk + TurnEnd): %+v", len(events), events)
			}
			tc0, ok := events[0].(turnevent.TextChunk)
			if !ok {
				t.Fatalf("event[0] = %T, want turnevent.TextChunk", events[0])
			}
			if tc0.Text != tc.needle {
				t.Errorf("TextChunk.Text = %q, want the %s needle %q", tc0.Text, tc.name, tc.needle)
			}
			if _, ok := events[1].(turnevent.TurnEnd); !ok {
				t.Fatalf("event[1] = %T, want turnevent.TurnEnd", events[1])
			}
		})
	}
}

// TestApproveVerdictNeedlesDistinct guards the fail-closed oracle: the three needles
// must be pairwise distinct AND not substring-confusable, because the e2e discriminates
// them with strings.Contains — a daemon deny (approve-deny) must never read as a client
// error (approve-error) or a fail-open (approve-allow), which is what makes the timeout
// case's fail-closed proof airtight.
func TestApproveVerdictNeedlesDistinct(t *testing.T) {
	t.Parallel()
	needles := []string{approveAllowNeedle, approveDenyNeedle, approveErrorNeedle}
	for i := range needles {
		for j := i + 1; j < len(needles); j++ {
			if strings.Contains(needles[i], needles[j]) || strings.Contains(needles[j], needles[i]) {
				t.Errorf("needles %q and %q are substring-confusable; the e2e discriminates via strings.Contains", needles[i], needles[j])
			}
		}
	}
}
